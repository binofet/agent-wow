package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Normal Acore MiscHandler/QueryHandler/Player protocol. Release is one byte;
// corpse query is empty; reclaim is the actual own corpse's raw GUID. Neither
// a successful write nor corpse disappearance is evidence of being alive.
const ghostFlag = uint32(0x10)
const ghostWaterWalking = uint32(0x10000000)

func planRecoveryNavigation(ctx context.Context, root string, mapID uint32, from, corpse position) ([]position, error) {
	points, err := buddynav.PlanRecoveryLegContext(ctx, root, mapID, buddynav.Point{X: from.x, Y: from.y, Z: from.z}, buddynav.Point{X: corpse.x, Y: corpse.y, Z: corpse.z})
	if err != nil {
		return nil, err
	}
	path := make([]position, len(points))
	for i, p := range points {
		path[i] = position{x: p.X, y: p.Y, z: p.Z}
	}
	return path, nil
}

type deathRecovery struct {
	state                                                                               actionsv1.RecoveryState
	enabledUntil, deadline, started, lastAlive, delayAt, delayUntil, queryAt, reclaimAt time.Time
	queryPending                                                                        bool
	readyForDeath                                                                       bool
	path                                                                                []position
	index                                                                               int
	mapID                                                                               uint32
	revision                                                                            uint64
	expected                                                                            position
	token                                                                               string
	releasePositionRevision                                                             uint64
	releaseMap                                                                          uint32
	graveyardPoseObserved                                                               bool
}

func (r *deathRecovery) snapshot(now time.Time) *actionsv1.RecoveryState {
	s := proto.Clone(&r.state).(*actionsv1.RecoveryState)
	if s.Active && now.Before(r.delayUntil) {
		s.ReclaimDelayRemainingMs = uint64(r.delayUntil.Sub(now).Milliseconds())
	}
	return s
}

func (s *Service) Recovery(ctx context.Context, request *actionsv1.ToggleRequest) (*actionsv1.State, error) {
	if request == nil {
		return nil, status.Error(codes.InvalidArgument, "recovery toggle required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, status.Error(codes.FailedPrecondition, "session is closing")
	}
	r := &s.recovery
	if request.Enabled && s.road.active {
		return nil, status.Error(codes.FailedPrecondition, "road-clear operation is active")
	}
	if request.Enabled {
		wasEnabled := r.state.Enabled
		r.state.Enabled = true
		r.enabledUntil = s.now().Add(lease)
		if r.state.DeathEpoch == 0 {
			r.state.Phase = "enabled_waiting_for_observed_death"
		}
		if !wasEnabled && r.state.DeathEpoch != 0 && r.state.Phase == "cancelled" && !r.state.MovementUnconfirmed && s.now().Before(r.deadline) {
			r.state.Active = true
			r.state.Phase = "resuming_existing_death_epoch"
			r.path = nil
		}
	} else {
		r.state.Enabled = false
		if s.recoveryOwnsLocked() {
			s.explicitStopLocked(ctx)
			r.state.MovementUnconfirmed = s.state.MotionUnconfirmed
		}
		s.endRecoveryLocked(ctx, "cancelled", "explicit_recovery_disabled")
	}
	return s.stateLocked(), nil
}

func recoverySelf(w *worldv1.SnapshotResponse, self uint64) bool {
	p := w.GetSelf()
	return p != nil && p.Guid == self && p.Present && p.MapKnown && p.PositionKnown && finite(playerPosition(p)) && p.AliveKnown && p.HealthKnown && p.MaxHealth > 0 && p.Health <= p.MaxHealth
}
func ghostSelf(w *worldv1.SnapshotResponse, self uint64) bool {
	if !recoverySelf(w, self) {
		return false
	}
	p := w.Self
	return !p.Alive && p.PlayerFlags&ghostFlag != 0 && p.MovementFlagsKnown && p.MovementFlags&^uint32(0x1ff|ghostWaterWalking) == 0 && p.ExtraMovementFlags == 0 && p.UnitFlagsKnown && p.UnitFlags&(1|4|0x40000|0x80000|0x100000|0x400000|0x800000|0x1000000|0x2000000|0x8000000) == 0 && p.SpeedsKnown && !math.IsNaN(float64(p.RunSpeed)) && p.RunSpeed >= 7 && p.RunSpeed <= 10.51
}
func (s *Service) recoveryOwnsLocked() bool {
	return s.recovery.token != "" && s.state.InteractionOwner == "recovery" && s.interactionToken == s.recovery.token
}
func (s *Service) endRecoveryLocked(ctx context.Context, phase, reason string) {
	r := &s.recovery
	if s.recoveryOwnsLocked() {
		if !s.stopLocked(ctx) {
			r.state.MovementUnconfirmed = true
			phase = "movement_unconfirmed_no_retry"
		}
		if !s.state.Moving && !s.state.MotionUnconfirmed {
			s.state.InteractionOwner = ""
			s.interactionToken = ""
			r.token = ""
		}
	}
	r.state.Active = false
	r.state.Phase = phase
	r.state.Reason = reason
	r.path = nil
	if !s.recoveryOwnsLocked() && s.state.InteractionOwner != "recovery" {
		r.token = ""
	}
}
func (s *Service) beginRecoveryLocked() {
	r := &s.recovery
	epoch := r.state.DeathEpoch + 1
	r.state = actionsv1.RecoveryState{Enabled: true, Active: true, DeathEpoch: epoch, Phase: "observed_death"}
	r.started = s.now()
	r.deadline = s.now().Add(10 * time.Minute)
	r.path = nil
	r.index = 0
	r.queryPending = false
	r.queryAt = time.Time{}
	r.reclaimAt = time.Time{}
	r.readyForDeath = false
	r.graveyardPoseObserved = false
	// If this body joined an already released ghost, no timer packet may remain.
	// Player.cpp's source maximum is120s. Waiting that whole maximum grants no
	// resurrection; the server still validates its actual timer and corpse.
	if r.delayAt.IsZero() || r.delayAt.Before(r.lastAlive) {
		r.delayUntil = s.now().Add(120 * time.Second)
	} else {
		r.state.ReclaimDelayKnown = true
	}
	// Actual death cancels this controller's living combat intents. Historical
	// cast/damage receipts and all other modules' uncertain item ledgers survive.
	s.state.CastPending = false
	s.pendingSpell = 0
	s.pendingTarget = 0
	s.state.AutoAttacking = false
	s.state.AutoAttackRequested = false
	s.state.AutoAttackUnconfirmed = false
	s.autoTarget = 0
	s.autoStopping = false
	s.state.Practicing = false
}

// Returns true while recovery owns the tick. It does not turn on follow/heal/
// assist, restore revoked intent, clear another module's ledger or use a healer.
func (s *Service) recoveryTickLocked(ctx context.Context) (handled bool) {
	r := &s.recovery
	defer func() {
		if handled {
			s.state.Reason = "death_recovery_" + r.state.Phase
		}
	}()
	if !r.state.Enabled {
		return false
	}
	if !s.now().Before(r.enabledUntil) {
		s.endRecoveryLocked(ctx, "expired", "recovery_enable_lease_expired")
		r.state.Enabled = false
		return false
	}
	if r.state.Active && r.token != "" {
		if !s.recoveryOwnsLocked() {
			r.token = ""
			r.state.Active = false
			r.state.Phase = "blocked"
			r.state.Reason = "recovery_custody_changed_no_retry"
			return true
		}
		s.interactionUntil = s.now().Add(15 * time.Second)
	}
	w, err := s.snapshot(ctx)
	if err != nil || !recoverySelf(w, s.selfGUID) {
		if r.state.Active {
			if s.recoveryOwnsLocked() {
				s.stopLocked(ctx)
			}
			r.state.Reason = "waiting_for_actual_self_observation"
			return true
		}
		return false
	}
	p := w.Self
	if p.Alive {
		r.lastAlive = s.now()
		r.readyForDeath = true
		if r.state.DeathEpoch != 0 {
			r.state.AliveObserved = true
		}
		if !r.state.Active {
			return false
		}
		r.state.AliveObserved = true
		r.state.Phase = "resting_after_actual_alive_receipt"
		s.refreshPositionLocked(w)
		s.stopLocked(ctx)
		if s.state.MotionUnconfirmed {
			r.state.MovementUnconfirmed = true
			r.state.Reason = "alive_but_stop_unconfirmed"
			return true
		}
		if s.state.CastPending && !s.now().Before(s.castUntil) {
			s.endRecoveryLocked(ctx, "alive_cast_unconfirmed", "rest_cast_timeout_no_retry")
			return false
		}
		if p.UnitFlagsKnown && p.UnitFlags&0x80000 != 0 {
			// Existing self preservation takes over immediately after resurrection.
			s.endRecoveryLocked(ctx, "alive_in_combat", "actual_alive_receipt_defense_priority")
			return false
		}
		if s.state.Healing && !s.state.CastPending {
			s.triageHealLocked(ctx, w, true)
		}
		manaReady := p.PowerKnown && (p.PowerType != 0 || p.MaxPower > 0 && uint64(p.Power)*100 >= uint64(p.MaxPower)*75)
		if uint64(p.Health)*100 >= uint64(p.MaxHealth)*90 && manaReady && !s.state.CastPending {
			r.state.Rested = true
			s.endRecoveryLocked(ctx, "complete", "actual_alive_and_recovered_health_observed")
			return false
		}
		if !s.now().Before(r.deadline) {
			s.endRecoveryLocked(ctx, "alive_rest_timeout", "alive_observed_rest_not_complete")
			return false
		}
		return true
	}
	if r.state.Active && r.state.AliveObserved {
		// A new actual death during recuperation is a new epoch, not a retry
		// of the previous epoch's already consumed release/reclaim requests.
		s.beginRecoveryLocked()
	}
	if !r.state.Active {
		if r.state.DeathEpoch != 0 && !r.readyForDeath {
			return true
		}
		if s.state.InteractionOwner != "" {
			return false
		}
		s.beginRecoveryLocked()
	}
	if !s.now().Before(r.deadline) {
		s.endRecoveryLocked(ctx, "recovery_timeout", "finite_recovery_deadline_no_retry")
		return true
	}
	if s.state.InteractionOwner != "" && !s.recoveryOwnsLocked() {
		r.state.Reason = "waiting_for_other_interaction_cleanup"
		return true
	}
	if r.token == "" {
		s.refreshPositionLocked(w)
		// Release does not carry a position. Actual HP0/non-ghost death may
		// release even when the pre-death movement result was uncertain; only
		// the later authoritative graveyard pose can authorize ghost movement.
		unreleasedDeath := p.Health == 0 && p.PlayerFlags&ghostFlag == 0
		if !unreleasedDeath && (s.state.MotionUnconfirmed || !s.stopLocked(ctx)) {
			s.endRecoveryLocked(ctx, "blocked", "death_movement_stop_unconfirmed")
			return true
		}
		r.token = fmt.Sprintf("death-recovery-%d", r.state.DeathEpoch)
		s.interactionToken = r.token
		s.state.InteractionOwner = "recovery"
	}
	s.interactionUntil = s.now().Add(15 * time.Second)
	if p.PlayerFlags&ghostFlag == 0 {
		if p.Health != 0 {
			r.state.Reason = "dead_state_not_releaseable"
			return true
		}
		if !r.state.ReleaseAttempted {
			r.releasePositionRevision, r.releaseMap = p.PositionRevision, p.MapId
			r.state.ReleaseAttempted = true
			r.state.Phase = "release_requested"
			r.state.ReleaseWritten = s.sendLocked(ctx, opcode.CMSGRepopRequest, []byte{0})
			r.path = nil
		} else {
			r.state.Reason = "waiting_for_actual_ghost_and_graveyard_teleport"
		}
		return true
	}
	r.state.GhostObserved = true
	if r.state.ReleaseAttempted && !r.graveyardPoseObserved {
		if p.PositionRevision == r.releasePositionRevision && p.MapId == r.releaseMap || !p.MovementFlagsKnown || p.MovementFlags&0xff != 0 {
			r.state.Phase = "waiting_for_actual_graveyard_position_baseline"
			return true
		}
		r.graveyardPoseObserved = true
	}
	if !ghostSelf(w, s.selfGUID) {
		s.stopLocked(ctx)
		r.state.Reason = "waiting_for_supported_actual_ghost_movement"
		return true
	}
	s.refreshPositionLocked(w)
	if s.state.MotionUnconfirmed {
		s.endRecoveryLocked(ctx, "blocked", "ghost_movement_unconfirmed_no_retry")
		return true
	}
	r.state.MovementUnconfirmed = false
	if s.state.Moving && r.path == nil && !s.stopLocked(ctx) {
		s.endRecoveryLocked(ctx, "blocked", "graveyard_movement_stop_unconfirmed")
		return true
	}
	if !r.state.CorpseQueryKnown && !r.queryPending {
		r.queryPending = true
		r.queryAt = s.now()
		r.state.Phase = "querying_actual_corpse"
		if !s.sendLocked(ctx, opcode.MSGCorpseQuery, nil) {
			r.state.Reason = "corpse_query_transport_unconfirmed"
		}
		return true
	}
	if !r.state.CorpseQueryKnown {
		if s.now().Sub(r.queryAt) > 5*time.Second {
			s.endRecoveryLocked(ctx, "blocked", "corpse_query_no_actual_response")
		}
		return true
	}
	if !r.state.CorpseFound || r.state.CorpseLocation == nil {
		s.endRecoveryLocked(ctx, "blocked", "server_reports_no_reclaimable_corpse")
		return true
	}
	location := r.state.CorpseLocation
	if p.MapId != location.MapId || p.MapId != r.state.CorpseMapId {
		s.endRecoveryLocked(ctx, "blocked", "corpse_requires_map_transition_not_supported")
		return true
	}
	to := position{x: location.X, y: location.Y, z: location.Z}
	if distance(s.pos, to) > 35 {
		if r.path == nil {
			if r.state.VerifiedLegs >= 32 {
				s.endRecoveryLocked(ctx, "blocked", "finite_ghost_route_leg_limit")
				return true
			}
			from, revision := s.pos, s.positionRevision
			var path []position
			var planErr error
			if s.planRecoveryPath != nil {
				path, planErr = s.planRecoveryPath(ctx, s.navDataDir, p.MapId, from, to)
			} else if distance(from, to) <= 200 {
				path, planErr = s.mapNavigationPath(ctx, s.navDataDir, p.MapId, from, to)
			} else {
				planErr = errors.New("bounded multi-leg recovery planner unavailable")
			}
			if planErr == nil && (len(path) < 2 || !validMapNavigationPath(path, from, path[len(path)-1]) || routeRemaining(from, path, 0) > 180) {
				planErr = errors.New("verified recovery leg exceeded route/endpoint bounds")
			}
			if planErr != nil {
				s.state.NavReason = navigationDiagnostic(planErr, s.navDataDir)
				s.endRecoveryLocked(ctx, "blocked", "corpse_navigation_unavailable")
				return true
			}
			fresh, e := s.snapshot(ctx)
			if e != nil || !ghostSelf(fresh, s.selfGUID) || fresh.Self.MapId != p.MapId || fresh.Self.PositionRevision != revision {
				s.endRecoveryLocked(ctx, "blocked", "ghost_changed_during_planning")
				return true
			}
			r.path, r.index, r.mapID, r.revision, r.expected = path, 0, p.MapId, revision, from
			r.state.VerifiedLegs++
		}
		if p.MapId != r.mapID || p.PositionRevision != r.revision || distance(s.pos, r.expected) > .05 {
			s.endRecoveryLocked(ctx, "blocked", "ghost_authoritative_pose_changed_replan_required")
			return true
		}
		if routeRemaining(s.pos, r.path, r.index) <= .75 {
			if !s.stopLocked(ctx) {
				s.endRecoveryLocked(ctx, "blocked", "ghost_leg_stop_unconfirmed")
				return true
			}
			r.path = nil
			r.state.Phase = "planning_next_verified_ghost_leg"
			return true
		}
		r.state.Phase = "following_verified_ghost_route"
		if !s.mapNavigationStepLocked(ctx, r.path, &r.index) {
			s.endRecoveryLocked(ctx, "blocked", "ghost_route_step_or_transport_failed")
			return true
		}
		r.expected = s.pos
		return true
	}
	if !s.stopLocked(ctx) {
		s.endRecoveryLocked(ctx, "blocked", "corpse_arrival_stop_unconfirmed")
		return true
	}
	r.path = nil
	if s.now().Before(r.delayUntil) {
		r.state.Phase = "waiting_for_reclaim_delay"
		return true
	}
	c := w.OwnCorpse
	if c == nil || c.Guid>>48 != 0xf101 || c.OwnerGuid != s.selfGUID || !c.Present || !c.MapKnown || c.MapId != p.MapId || !c.PositionKnown || !finite(position{x: c.X, y: c.Y, z: c.Z}) || !c.FlagsKnown || c.Flags&1 != 0 || distance(position{x: c.X, y: c.Y, z: c.Z}, to) > 2 || distance(s.pos, position{x: c.X, y: c.Y, z: c.Z}) > 35 {
		r.state.Phase = "waiting_for_actual_own_corpse_guid"
		return true
	}
	r.state.CorpseGuid = c.Guid
	if !r.state.ReclaimAttempted {
		// Clock and STOP can run callbacks. Recheck actual ghost/map/corpse
		// immediately before reserving the single normal reclaim write.
		fresh, e := s.snapshot(ctx)
		if e != nil || !ghostSelf(fresh, s.selfGUID) || fresh.Self.MapId != p.MapId || fresh.Self.PositionRevision != s.positionRevision || !sameRecoveryCorpse(fresh.OwnCorpse, c) {
			r.state.Reason = "corpse_changed_before_reclaim"
			return true
		}
		r.state.ReclaimAttempted = true
		r.reclaimAt = s.now()
		r.state.Phase = "reclaim_requested"
		r.state.ReclaimWritten = s.sendLocked(ctx, opcode.CMSGReclaimCorpse, binary.LittleEndian.AppendUint64(nil, c.Guid))
	} else if s.now().Sub(r.reclaimAt) > 10*time.Second {
		s.endRecoveryLocked(ctx, "outcome_unconfirmed", "reclaim_without_actual_alive_receipt_no_retry")
	}
	return true
}

func sameRecoveryCorpse(a, b *worldv1.CorpseState) bool {
	return a != nil && b != nil && a.Guid == b.Guid && a.OwnerGuid == b.OwnerGuid && a.Present && a.MapKnown && a.MapId == b.MapId && a.PositionKnown && a.X == b.X && a.Y == b.Y && a.Z == b.Z && a.PositionRevision == b.PositionRevision && a.FlagsKnown && a.Flags == b.Flags && a.Flags&1 == 0
}

func (s *Service) recoveryPacketLocked(op uint32, payload []byte) (bool, error) {
	r := &s.recovery
	switch op {
	case opcode.MSGCorpseQuery:
		if len(payload) == 1 && payload[0] == 0 {
			if r.queryPending {
				r.state.CorpseQueryKnown = true
				r.state.CorpseFound = false
				r.queryPending = false
			}
			return true, nil
		}
		if len(payload) != 25 || payload[0] != 1 || binary.LittleEndian.Uint32(payload[21:]) != 0 {
			return true, errors.New("invalid corpse query response")
		}
		mapID, actual := binary.LittleEndian.Uint32(payload[1:]), binary.LittleEndian.Uint32(payload[17:])
		p := position{x: math.Float32frombits(binary.LittleEndian.Uint32(payload[5:])), y: math.Float32frombits(binary.LittleEndian.Uint32(payload[9:])), z: math.Float32frombits(binary.LittleEndian.Uint32(payload[13:]))}
		if mapID > 10000 || actual > 10000 || !finite(p) {
			return true, errors.New("invalid corpse query location")
		}
		if r.queryPending {
			r.state.CorpseQueryKnown = true
			r.state.CorpseFound = true
			r.state.CorpseMapId = actual
			r.state.CorpseLocation = &actionsv1.ClientPosition{Known: true, MapKnown: true, MapId: mapID, X: p.x, Y: p.y, Z: p.z}
			r.queryPending = false
		}
		return true, nil
	case opcode.SMSGCorpseReclaimDelay:
		if len(payload) != 4 || binary.LittleEndian.Uint32(payload) > 120000 {
			return true, errors.New("invalid corpse reclaim delay")
		}
		r.delayAt = s.now()
		r.delayUntil = s.now().Add(time.Duration(binary.LittleEndian.Uint32(payload)) * time.Millisecond)
		r.state.ReclaimDelayKnown = true
		return true, nil
	case opcode.SMSGDeathReleaseLoc:
		if len(payload) != 16 {
			return true, errors.New("invalid graveyard location")
		}
		mapID := binary.LittleEndian.Uint32(payload)
		p := position{x: math.Float32frombits(binary.LittleEndian.Uint32(payload[4:])), y: math.Float32frombits(binary.LittleEndian.Uint32(payload[8:])), z: math.Float32frombits(binary.LittleEndian.Uint32(payload[12:]))}
		if !finite(p) || mapID != math.MaxUint32 && mapID > 10000 {
			return true, errors.New("invalid graveyard position")
		}
		if mapID == math.MaxUint32 {
			r.state.GraveyardLocation = nil
		} else {
			r.state.GraveyardLocation = &actionsv1.ClientPosition{Known: true, MapKnown: true, MapId: mapID, X: p.x, Y: p.y, Z: p.z}
		}
		return true, nil
	}
	return false, nil
}
