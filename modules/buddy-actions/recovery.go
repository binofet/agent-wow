package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
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

func planRecoveryNavigation(ctx context.Context, root string, mapID uint32, from, corpse position, arrival float64) ([]position, error) {
	points, err := buddynav.PlanRecoveryLegRangeContext(ctx, root, mapID, buddynav.Point{X: from.x, Y: from.y, Z: from.z}, buddynav.Point{X: corpse.x, Y: corpse.y, Z: corpse.z}, arrival)
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
	state                        actionsv1.RecoveryState
	delayAt, delayUntil          time.Time
	queryPending                 bool
	lifeKnown, lastObservedAlive bool
	path                         []position
	index                        int
	mapID                        uint32
	revision                     uint64
	expected                     position
	token                        string
	releasePositionRevision      uint64
	releaseMap                   uint32
	graveyardPoseObserved        bool
}

func (r *deathRecovery) snapshot(now time.Time) *actionsv1.RecoveryState {
	s := proto.Clone(&r.state).(*actionsv1.RecoveryState)
	s.Enabled = true // Compatibility capability fact, never an ordinary off switch.
	s.CorpseQueryPending = r.queryPending
	if now.Before(r.delayUntil) {
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
	// Compatibility input no longer owns a standing recovery enable switch.
	// Attached policy callers queue an ensure request; a direct old toggle can
	// only reconcile this body's existing physical motion, never disable Lua.
	s.recovery.state.Enabled = true
	if !request.Enabled && s.recoveryOwnsLocked() {
		s.finishRecoveryMotionLocked(ctx, "stopped", "legacy_stop_of_owned_physical_motion")
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

// A physical owner is retired only after its STOP is known. Unknown transport
// and definite pre-write STOP refusal retain custody for fresh reconciliation.
func (s *Service) finishRecoveryMotionLocked(ctx context.Context, phase, reason string) bool {
	r := &s.recovery
	if s.recoveryOwnsLocked() {
		if !s.stopLocked(ctx) || s.state.MotionUnconfirmed {
			r.state.MovementUnconfirmed = s.state.MotionUnconfirmed
			r.state.Reason = "waiting_for_owned_stop"
			return false
		}
		s.state.InteractionOwner = ""
		s.interactionToken = ""
	}
	r.token = ""
	r.path = nil
	r.state.Active = false
	r.state.MovementUnconfirmed = false
	r.state.Phase = phase
	r.state.Reason = reason
	return true
}

// Life identity is raw observation bookkeeping, independent of recovery policy
// enablement. A new positive death retires only that death's packet reservations.
func (s *Service) observeRecoveryLifeLocked(w *worldv1.SnapshotResponse) {
	r := &s.recovery
	p := w.GetSelf()
	if p == nil || p.Guid != s.selfGUID || !p.Present || !p.AliveKnown || !p.HealthKnown || p.MaxHealth == 0 || p.Health > p.MaxHealth {
		return
	}
	if !p.Alive && (!r.lifeKnown || r.lastObservedAlive) {
		r.state = actionsv1.RecoveryState{Enabled: true, Active: r.state.Active,
			DeathEpoch: r.state.DeathEpoch + 1, Phase: "observed_death", MovementUnconfirmed: s.state.MotionUnconfirmed}
		r.queryPending = false
		r.path = nil
		r.releasePositionRevision, r.releaseMap = 0, 0
		r.graveyardPoseObserved = false
	}
	r.lifeKnown, r.lastObservedAlive = true, p.Alive
	if p.Alive && r.state.DeathEpoch != 0 {
		r.state.AliveObserved = true
	}
	if !p.Alive && p.PlayerFlags&ghostFlag != 0 {
		r.state.GhostObserved = true
		if !r.state.ReleaseAttempted || (p.PositionRevision != r.releasePositionRevision || p.MapId != r.releaseMap) && p.MovementFlagsKnown && p.MovementFlags&0xff == 0 {
			r.graveyardPoseObserved = true
		}
	}
	r.state.GhostMovementSupported = ghostSelf(w, s.selfGUID) && r.graveyardPoseObserved && !s.clientControlLost && !s.clientControlRestoring
	r.state.OwnCorpse = nil
	if c := w.OwnCorpse; c != nil {
		r.state.OwnCorpse = &actionsv1.RecoveryCorpse{Guid: c.Guid, OwnerGuid: c.OwnerGuid, Present: c.Present, FlagsKnown: c.FlagsKnown, Flags: c.Flags,
			Position: &actionsv1.ClientPosition{Known: c.PositionKnown, MapKnown: c.MapKnown, MapId: c.MapId, X: c.X, Y: c.Y, Z: c.Z, Revision: c.PositionRevision}}
	}
}

// Native maintenance only reconciles an existing physical execution. It never
// chooses release, a route, reclaim, a recovery deadline or a future retry.
func (s *Service) recoveryTickLocked(ctx context.Context) bool {
	r := &s.recovery
	if r.token == "" {
		return false
	}
	if !s.recoveryOwnsLocked() {
		r.token = ""
		r.path = nil
		r.state.Active = false
		return false
	}
	w, err := s.snapshot(ctx)
	if err != nil || !recoverySelf(w, s.selfGUID) {
		s.stopLocked(ctx)
		r.state.Reason = "waiting_for_actual_self_observation"
		return true
	}
	if s.controlRecoveryLocked(ctx) {
		r.state.Reason = "waiting_for_controllable_position_before_owned_stop"
		return true
	}
	w = s.lastWorld
	if !recoverySelf(w, s.selfGUID) {
		return true
	}
	if w.Self.Alive {
		r.state.AliveObserved = true
		r.state.Phase = "alive_observed"
		if s.controlRecoveryLocked(ctx) {
			r.state.Reason = "waiting_for_controllable_position_before_owned_stop"
			return true
		}
		w = s.lastWorld
		if !recoverySelf(w, s.selfGUID) || !w.Self.Alive {
			return true
		}
		s.refreshPositionLocked(w)
		return !s.finishRecoveryMotionLocked(ctx, "alive_observed", "actual_alive_receipt_shared_Lua_resource_policy")
	}
	// This is the existing finite interaction write lease, not a gameplay
	// deadline. No fresh Lua input means physical travel must stop.
	if !s.now().Before(s.interactionUntil) {
		s.refreshPositionLocked(w)
		return !s.finishRecoveryMotionLocked(ctx, "movement_lease_expired", "fresh_Lua_input_required")
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
				r.state.CorpseQueryRevision++
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
			r.state.CorpseQueryRevision++
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
