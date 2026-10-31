package buddyactions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

const roadClearGoal = "normal_1718_road_clear"
const roadClearWindow = 180 * time.Second

type roadClearRun struct {
	selected, active, invalid, reserved, death            bool
	requestID, session, phase                             string
	target, source, baseline, deathRevision               uint64
	level, maxHealth                                      uint32
	origin                                                position
	originRevision                                        uint64
	originAt                                              time.Time
	originEstimated                                       bool
	until                                                 time.Time
	path                                                  []position
	goObserved, damageObserved                            bool
	targetRevision                                        uint64
	targetOrigin                                          position
	meleeAt                                               time.Time
	waitingResult, resultGo, resultDamage                 bool
	resultUntil                                           time.Time
	damageFrames                                          map[[32]byte]bool
	castHealth                                            uint32
	request                                               *actionsv1.RoadClearRequest
	adds                                                  map[uint64]*worldv1.CreatureState
	observedPackets                                       uint64
	victimObserved, contactKnown, stopObserved, deathHint bool
	contactRaw, contactAim                                position
	contactRevision                                       uint64
	contactMovement                                       uint32
	contactTrajectory                                     *worldv1.CreatureTrajectory
	progressAt, stopAt                                    time.Time
}

// This observer is independent of CastPending. The maintained worker exists
// before login, so only its first full INITIAL with no earlier cast/ambiguity
// can seed observed idle. Later INITIAL packets never heal unknown state.
// Every outgoing own cast reserves busy before transport; exact typed results
// resolve it. This is latest observed state, not a public-RPC drain barrier.
type roadCastObservation struct {
	known, busy, started, poisoned bool
	spell                          uint32
	count                          byte
	target                         uint64
}

func roadArea(p position) bool {
	return finite(p) && p.x >= -6260 && p.x <= -6060 && p.y >= 25 && p.y <= 175 && p.z >= 400 && p.z <= 450
}
func roadProfile(c *worldv1.CreatureState) bool {
	return c != nil && c.Guid>>48 == 0xf130 && c.Guid&0xffffff != 0 && uint32(c.Guid>>24)&0xffffff == 1718 && c.Entry == 1718 && c.MapKnown && c.MapId == 0 && c.LevelKnown && (c.Level == 3 && c.MaxHealth == 71 || c.Level == 4 && c.MaxHealth == 86) && c.FactionTemplate == 59
}
func (s *Service) roadSnapshotLocked() *actionsv1.RoadClearState {
	r := &s.road
	if !r.selected {
		return nil
	}
	remaining := uint64(0)
	now := s.now()
	level := uint32(0)
	if s.lastWorld != nil && s.lastWorld.Self != nil && s.lastWorld.Self.Guid == s.selfGUID && s.lastWorld.Self.LevelKnown {
		level = s.lastWorld.Self.Level
	}
	if r.death && !r.invalid {
		w := s.lastWorld
		if !roadOwnWorld(w, false) || w.ObservedPackets < max(r.observedPackets, s.roadWorldPackets) || w.Self.PositionRevision != r.baseline || !s.refreshPositionLocked(w) || distance(s.pos, r.origin) > .05 || s.state.Moving || s.state.MotionUnconfirmed || s.state.AutoAttacking || s.state.AutoAttackRequested || s.state.AutoAttackUnconfirmed || s.state.CastPending || !s.roadCast.known || s.roadCast.busy || s.roadCast.poisoned {
			r.invalid, r.phase = true, "terminal_world_proof_unavailable"
		} else {
			r.observedPackets, s.roadWorldPackets = w.ObservedPackets, w.ObservedPackets
		}
	}
	if !r.invalid && !s.closed && level == 5 && (r.active || r.death && s.roadCast.known && !s.roadCast.busy && !s.roadCast.poisoned) && now.Before(r.until) {
		remaining = uint64(min(roadClearWindow, r.until.Sub(now)).Milliseconds())
	}
	age := uint64(0)
	if now.After(r.originAt) {
		age = uint64(now.Sub(r.originAt).Milliseconds())
	}
	return &actionsv1.RoadClearState{RequestId: r.requestID, Goal: roadClearGoal, TargetGuid: r.target, SourceGuid: r.source,
		SelfGuid: s.selfGUID, SessionId: r.session, PositionRevision: r.baseline,
		Origin:      &actionsv1.ClientPosition{Known: true, MapKnown: true, MapId: 0, X: r.origin.x, Y: r.origin.y, Z: r.origin.z, Orientation: r.origin.o, Estimated: r.originEstimated, Revision: r.originRevision, AgeMs: age},
		RemainingMs: remaining, MaxPulls: 1, PullReserved: r.reserved, Active: r.active, Phase: r.phase, ObservedLevel: level,
		SelectedEntry: 1718, SelectedLevel: r.level, SelectedMaxHealth: r.maxHealth, SelectedFactionTemplate: 59,
		SourceDeathObserved: r.death, SourceDeathPositionRevision: r.deathRevision}
}
func (s *Service) roadStopLocked(ctx context.Context, reason string) {
	s.road.active = false
	s.road.phase = reason
	if !s.road.death {
		s.road.invalid = true
	}
	// STOP is movement cleanup, never a claim that an ordinary spell canceled.
	s.stopLocked(ctx)
	s.state.Reason = "road_clear_" + reason
}
func (s *Service) roadSessionLocked(ctx context.Context, expected string) bool {
	if s.session == nil {
		return false
	}
	call, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	q, _ := anypb.New(&emptypb.Empty{})
	v, err := s.session.InvokeModule(call, &modv1.InvokeModuleRequest{Module: "buddy-party", Method: "state", Request: q})
	a := &partyv1.StateResponse{}
	return err == nil && v != nil && v.Result != nil && v.Result.UnmarshalTo(a) == nil && expected != "" && len(expected) <= 128 && a.SessionId == expected && call.Err() == nil
}
func (s *Service) roadWorldLocked(ctx context.Context, target uint64) (*worldv1.SnapshotResponse, error) {
	q, _ := anypb.New(&worldv1.SnapshotRequest{CreatureGuid: target, CreatureFactionTemplate: 59})
	v, err := s.session.InvokeModule(ctx, &modv1.InvokeModuleRequest{Module: "buddy-world", Method: "snapshot", Request: q})
	if err != nil {
		return nil, err
	}
	w := &worldv1.SnapshotResponse{}
	if v == nil || v.Result == nil || v.Result.UnmarshalTo(w) != nil {
		return nil, status.Error(codes.Unavailable, "road world missing")
	}
	w.Owner, w.OwnerTarget = nil, nil
	if w.ObservedPackets < s.roadWorldPackets {
		return nil, status.Error(codes.FailedPrecondition, "road world epoch counter regressed")
	}
	s.roadWorldPackets = w.ObservedPackets
	s.lastWorld = w
	s.updateClassLocked()
	return w, nil
}
func roadTarget(w *worldv1.SnapshotResponse, guid uint64) *worldv1.CreatureState {
	if w != nil && w.RequestedCreature != nil && w.RequestedCreature.Guid == guid {
		return w.RequestedCreature
	}
	return nil
}

func (s *Service) StartRoadClear(ctx context.Context, q *actionsv1.RoadClearRequest) (*actionsv1.State, error) {
	if q == nil || !validColdridgeLevelingRequestID(q.RequestId) || q.SessionId == "" || len(q.SessionId) > 128 || q.TargetGuid>>48 != 0xf130 || q.TargetGuid&0xffffff == 0 || uint32(q.TargetGuid>>24)&0xffffff != 1718 || q.PositionRevision == 0 || q.ExpectedPosition == nil || !q.ExpectedPosition.Known || !q.ExpectedPosition.MapKnown || q.ExpectedPosition.MapId != 0 || q.ExpectedPosition.Revision == 0 || !roadArea(position{q.ExpectedPosition.X, q.ExpectedPosition.Y, q.ExpectedPosition.Z, q.ExpectedPosition.Orientation}) {
		return nil, status.Error(codes.InvalidArgument, "explicit request, session, observed1718 and exact stopped pose required")
	}
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.independent || s.selfGUID != 8 || s.roadEpochSession != "" && s.roadEpochSession != q.SessionId {
		return nil, status.Error(codes.FailedPrecondition, "independent GUID8 road scope required")
	}
	if old, ok := s.roadRequests[q.RequestId]; ok {
		if old != q.TargetGuid || s.road.requestID != q.RequestId || s.road.session != q.SessionId || !proto.Equal(q, s.road.request) {
			return nil, status.Error(codes.FailedPrecondition, "road request already consumed")
		}
		return s.stateLocked(), nil
	}
	if len(s.roadRequests) >= 64 || s.roadBodies[q.TargetGuid] || s.road.active || s.road.waitingResult || s.state.Following || s.state.Healing || s.state.Assisting || s.state.Practicing || s.state.Traveling || s.questApproaching || s.state.InteractionOwner != "" || s.state.Moving || s.state.MotionUnconfirmed || s.state.CastPending || s.state.AutoAttacking || s.state.AutoAttackRequested || s.state.AutoAttackUnconfirmed || s.autoTarget != 0 || s.recovery.state.Enabled || s.fargodeep.active {
		return nil, status.Error(codes.FailedPrecondition, "unused request and confirmed exclusive stopped actions required")
	}
	if !s.roadEpochKnown || !s.roadSpellbookKnown || !s.knownSpells[shadowBolt] {
		return nil, status.Error(codes.FailedPrecondition, "complete valid INITIAL_SPELLS with686 required")
	}
	if !s.roadCast.known || s.roadCast.busy {
		return nil, status.Error(codes.FailedPrecondition, "road_clear_current_cast_unknown_or_busy")
	}
	begin := s.now()
	if !s.roadSessionLocked(ctx, q.SessionId) {
		return nil, status.Error(codes.FailedPrecondition, "current body session unavailable")
	}
	w, err := s.roadWorldLocked(ctx, q.TargetGuid)
	expected := position{q.ExpectedPosition.X, q.ExpectedPosition.Y, q.ExpectedPosition.Z, q.ExpectedPosition.Orientation}
	if err != nil || !roadOwnWorld(w, true) || w.Self.PositionRevision != q.PositionRevision || !s.refreshPositionLocked(w) || s.posMap != 0 || !roadArea(s.pos) || distance(s.pos, expected) > .05 || s.posRevision != q.ExpectedPosition.Revision || s.posEstimated != q.ExpectedPosition.Estimated || q.ExpectedPosition.AgeMs > 2000 || math.Abs(float64(s.pos.o-expected.o)) > .01 || s.clientPositionLocked().AgeMs > 2000 {
		return nil, status.Error(codes.FailedPrecondition, "fresh healthy own level5 warlock, full observations and exact pose required")
	}
	c := roadTarget(w, q.TargetGuid)
	if !roadProfile(c) || !roadLivingTarget(c, true) || distance(s.pos, playerCreaturePosition(c)) < 16 || distance(s.pos, playerCreaturePosition(c)) > 25 || !roadAdds(w, q.TargetGuid, []position{s.pos, playerCreaturePosition(c)}) {
		return nil, status.Error(codes.FailedPrecondition, "fresh stationary unclaimed1718 at16..25yd with isolated route required")
	}
	adds, ok := roadHeldAdds(w, q.TargetGuid)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "complete stationary59 baseline required")
	}
	if s.roadRequests == nil {
		s.roadRequests = map[string]uint64{}
		s.roadBodies = map[uint64]bool{}
	}
	s.roadRequests[q.RequestId] = q.TargetGuid
	s.roadBodies[q.TargetGuid] = true
	s.roadEpochSession = q.SessionId
	s.road = roadClearRun{selected: true, active: true, requestID: q.RequestId, session: q.SessionId, target: q.TargetGuid, baseline: q.PositionRevision, origin: s.pos, originRevision: s.posRevision, until: begin.Add(roadClearWindow), phase: "planning_stationary_pull", level: c.Level, maxHealth: c.MaxHealth, targetRevision: c.PositionRevision, targetOrigin: playerCreaturePosition(c), damageFrames: map[[32]byte]bool{}}
	s.road.request = proto.Clone(q).(*actionsv1.RoadClearRequest)
	s.road.originAt, s.road.originEstimated = s.posAt, s.posEstimated
	s.road.adds, s.road.observedPackets = adds, w.ObservedPackets
	s.coldridgeGoal = coldridgePracticeGoal{} // Hide only publication, retaining irreversible706 history.
	path, err := s.mapNavigationPath(ctx, s.navDataDir, 0, s.pos, playerCreaturePosition(c))
	if err != nil || routeRemaining(s.pos, path, 0) > 60 || !roadPath(path, s.pos, playerCreaturePosition(c)) {
		s.roadStopLocked(ctx, "navigation_unavailable_request_consumed")
		return s.stateLocked(), nil
	}
	s.road.path = path
	if !s.roadSessionLocked(ctx, q.SessionId) {
		s.roadStopLocked(ctx, "session_changed_during_plan")
		return s.stateLocked(), nil
	}
	w, err = s.roadWorldLocked(ctx, q.TargetGuid)
	if err != nil || !s.roadFrameLocked(w, true) || !roadAdds(w, q.TargetGuid, path) || ctx.Err() != nil || !s.now().Before(s.road.until) {
		s.roadStopLocked(ctx, "observations_changed_during_plan")
		return s.stateLocked(), nil
	}
	s.road.phase = "armed_stationary_waiting_for_cast"
	s.state.Reason = "road_clear_armed_stationary"
	return s.stateLocked(), nil
}
func playerCreaturePosition(c *worldv1.CreatureState) position {
	return position{c.X, c.Y, c.Z, c.Orientation}
}

func (s *Service) roadFrameLocked(w *worldv1.SnapshotResponse, initial bool) bool {
	r := &s.road
	if !r.active || r.invalid || r.stopObserved || !s.now().Before(r.until) || !roadOwnWorld(w, initial) || w.ObservedPackets < r.observedPackets || w.Self.PositionRevision != r.baseline || !s.refreshPositionLocked(w) || s.posMap != 0 || !roadArea(s.pos) || distance(s.pos, r.origin) > .05 || s.state.Moving || s.state.MotionUnconfirmed || s.state.AutoAttacking || s.state.AutoAttackRequested || s.state.AutoAttackUnconfirmed || s.state.InteractionOwner != "" || !s.roadSpellbookKnown || !s.knownSpells[shadowBolt] || s.roadCast.poisoned || !roadHeldAddsEqual(w, r.target, r.adds) {
		return false
	}
	r.observedPackets = w.ObservedPackets
	c := roadTarget(w, r.target)
	if !roadProfile(c) || c.Level != r.level || c.MaxHealth != r.maxHealth || !roadLivingTarget(c, initial) || !roadAdds(w, r.target, r.path) {
		return false
	}
	aim, aimKnown, _ := creaturePosition(c)
	d := distance(s.pos, aim)
	if initial {
		return c.PositionRevision == r.targetRevision && distance(playerCreaturePosition(c), r.targetOrigin) <= .25 && d >= 16 && d <= 25 && s.clientPositionLocked().AgeMs <= 2000 && s.roadCast.known && !s.roadCast.busy && !s.roadCast.poisoned
	}
	if aimKnown && (!roadArea(aim) || d > 25) || !c.TargetKnown || c.TargetGuid != 0 && c.TargetGuid != s.selfGUID || c.AttackVictimKnown && c.AttackVictimGuid != 0 && c.AttackVictimGuid != s.selfGUID || c.DynamicFlags&4 != 0 && c.DynamicFlags&8 == 0 {
		return false
	}
	// Inside the initial standoff, only an actually observed current own victim
	// at the existing stationary melee-proximity guard permits another bolt.
	if r.contactKnown && !roadContactSame(r, c) {
		return false
	}
	return s.roadEngaged(w, c) || r.waitingResult && s.now().Before(r.resultUntil) && c.TargetGuid == 0 && c.UnitFlags&0x80000 == 0 && (!c.AttackVictimKnown || c.AttackVictimGuid == 0)
}
func (s *Service) roadEngaged(w *worldv1.SnapshotResponse, c *worldv1.CreatureState) bool {
	return s.road.victimObserved && !s.road.stopObserved && c.AttackVictimKnown && c.AttackVictimGuid == 8 && w.Self.UnitFlags&0x80000 != 0
}
func (s *Service) roadProximityLocked(w *worldv1.SnapshotResponse) bool {
	c := roadTarget(w, s.road.target)
	p, known, estimated := creaturePosition(c)
	if !known || !roadArea(p) {
		return false
	}
	d := distance(s.pos, p)
	if d >= 16 && !estimated {
		return true
	}
	r := &s.road
	if !s.roadEngaged(w, c) || !w.Self.CombatReachKnown || !c.CombatReachKnown || !finiteReach(w.Self.CombatReach) || !finiteReach(c.CombatReach) || d > math.Max(5, float64(w.Self.CombatReach+c.CombatReach)+4.0/3.0)-.5 {
		return false
	}
	if !r.contactKnown {
		if r.meleeAt.IsZero() || s.now().Sub(r.meleeAt) < 0 || s.now().Sub(r.meleeAt) > 5*time.Second {
			return false
		}
		r.contactKnown, r.contactRaw, r.contactAim, r.contactRevision, r.contactMovement = true, playerCreaturePosition(c), p, c.PositionRevision, c.MovementFlags
		r.contactTrajectory = roadTrajectory(c.Trajectory)
	}
	return roadContactSame(r, c) && !r.progressAt.IsZero() && s.now().Sub(r.progressAt) >= 0 && s.now().Sub(r.progressAt) <= 5*time.Second
}
func finiteReach(v float32) bool {
	return !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0) && v > 0 && v <= 5
}

func (s *Service) roadTickLocked(ctx context.Context) {
	r := &s.road
	if !r.active {
		return
	}
	if ctx.Err() != nil || !s.now().Before(r.until) {
		s.roadStopLocked(ctx, "deadline_or_cancellation")
		return
	}
	if !s.roadSessionLocked(ctx, r.session) {
		s.roadStopLocked(ctx, "session_changed")
		return
	}
	w, err := s.roadWorldLocked(ctx, r.target)
	if err != nil {
		s.roadStopLocked(ctx, "world_unavailable")
		return
	}
	c := roadTarget(w, r.target)
	now := s.now()
	if r.stopObserved && !now.Before(r.stopAt.Add(2*time.Second)) || r.waitingResult && !now.Before(r.resultUntil) {
		s.roadStopLocked(ctx, "terminal_or_result_window_expired_no_retry")
		return
	}
	deathAim, deathKnown, _ := creaturePosition(c)
	if roadProfile(c) && c.Level == r.level && c.MaxHealth == r.maxHealth && c.Present && c.AliveKnown && !c.Alive && c.HealthKnown && c.Health == 0 && c.PositionKnown && c.PositionRevision > 0 && c.ObservationAgeMs <= 5000 && deathKnown && roadArea(deathAim) && (!r.contactKnown || roadTerminalPose(r, c)) && r.reserved && r.goObserved && r.damageObserved && !s.state.CastPending && roadOwnWorld(w, false) && w.ObservedPackets >= r.observedPackets && roadHeldAddsEqual(w, r.target, r.adds) && roadAdds(w, r.target, r.path) && w.Self.PositionRevision == r.baseline && s.refreshPositionLocked(w) && distance(s.pos, r.origin) <= .05 {
		r.death, r.deathRevision, r.active, r.phase = true, c.PositionRevision, false, "selected_death_observed"
		r.waitingResult = false
		s.state.Reason = "road_clear_selected_death_observed_no_quest_credit"
		return
	}
	// A genuine stop at the pinned contact can precede the death update. It
	// revokes offense immediately; only the later actual health-zero branch
	// above can grant a corpse handoff. The living contact guard stays exact.
	if !r.stopObserved && r.contactKnown && roadProfile(c) && c.Level == r.level && c.MaxHealth == r.maxHealth && c.Present && c.AliveKnown && c.HealthKnown && r.reserved && r.goObserved && r.damageObserved && !roadContactSame(r, c) && roadTerminalPose(r, c) {
		r.stopObserved, r.deathHint, r.stopAt = true, true, s.now()
	}
	if r.stopObserved {
		if r.deathHint && s.now().Sub(r.stopAt) >= 0 && s.now().Before(r.stopAt.Add(2*time.Second)) && roadOwnWorld(w, false) && w.ObservedPackets >= r.observedPackets && roadHeldAddsEqual(w, r.target, r.adds) && roadProfile(c) && w.Self.PositionRevision == r.baseline {
			r.phase = "waiting_for_actual_selected_death"
			return
		}
		s.roadStopLocked(ctx, "selected_attack_stopped_no_retry")
		return
	}
	if !s.roadFrameLocked(w, !r.reserved) {
		s.roadStopLocked(ctx, "own_target_or_add_state_unsafe")
		return
	}
	if r.waitingResult {
		if !s.now().Before(r.resultUntil) {
			s.roadStopLocked(ctx, "cast_damage_result_unknown_no_retry")
			return
		}
		if c.Health > r.castHealth {
			s.roadStopLocked(ctx, "target_health_regressed_no_retry")
			return
		}
		if s.state.CastPending || !r.resultGo || !r.resultDamage || c.Health >= r.castHealth {
			return
		}
		r.waitingResult = false
		r.progressAt = s.now().Add(-time.Duration(min(c.ObservationAgeMs, uint64(24*time.Hour/time.Millisecond))) * time.Millisecond)
	}
	if s.state.CastPending {
		if s.pendingSpell != shadowBolt || s.pendingTarget != r.target || !s.now().Before(s.castUntil) {
			s.roadStopLocked(ctx, "cast_result_unknown_no_retry")
		}
		return
	}
	if !s.roadCast.known || s.roadCast.busy || s.failedAssistTarget == r.target {
		s.roadStopLocked(ctx, "current_cast_unknown_or_failed")
		return
	}
	if r.reserved && !s.roadEngaged(w, c) {
		r.phase = "waiting_for_actual_selected_attack"
		return
	}
	if !s.roadProximityLocked(w) {
		if t := c.Trajectory; t != nil && t.Known && !t.Stopped && !t.Cyclic && !t.TransportRelative && t.DurationMs > 0 && t.DurationMs <= 10000 {
			if t.AgeMs > uint64(t.DurationMs)+5000 {
				s.roadStopLocked(ctx, "selected_approach_without_melee_result")
				return
			}
			r.phase = "waiting_for_actual_selected_approach_and_melee"
			return
		}
		if r.contactKnown {
			s.roadStopLocked(ctx, "stationary_combat_progress_missing")
			return
		}
		if !r.progressAt.IsZero() && s.now().Sub(r.progressAt) > 5*time.Second {
			s.roadStopLocked(ctx, "selected_melee_progress_missing")
			return
		}
		r.phase = "waiting_for_proven_stationary_melee_proximity"
		return
	}
	if s.now().Before(s.nextAssist) || s.now().Before(s.nextHeal) {
		return
	}
	if uint64(w.Self.Power) < manaBudget(w.Self, 10) {
		s.roadStopLocked(ctx, "mana_unavailable")
		return
	}
	// Facing obtains a clock and a world internally. Rebind the session and
	// exact requested target afterward; no dependency call follows that world.
	p, _, _ := creaturePosition(c)
	if !s.roadFaceLocked(ctx, p) {
		s.roadStopLocked(ctx, "facing_unconfirmed")
		return
	}
	if !s.roadSessionLocked(ctx, r.session) {
		s.roadStopLocked(ctx, "session_changed_after_facing")
		return
	}
	w, err = s.roadWorldLocked(ctx, r.target)
	currentAim, currentKnown, _ := creaturePosition(roadTarget(w, r.target))
	if err != nil || !s.roadFrameLocked(w, !r.reserved) || !s.roadProximityLocked(w) || !currentKnown || distance(currentAim, p) > .25 || uint64(w.Self.Power) < manaBudget(w.Self, 10) || ctx.Err() != nil || !s.now().Before(r.until) || !s.roadCast.known || s.roadCast.busy || s.roadCast.poisoned {
		s.roadStopLocked(ctx, "final_observations_changed")
		return
	}
	if !r.reserved {
		r.reserved, r.source = true, r.target
	}
	r.waitingResult, r.resultGo, r.resultDamage = true, false, false
	r.resultUntil = s.now().Add(5 * time.Second)
	r.castHealth = roadTarget(w, r.target).Health
	r.damageFrames = map[[32]byte]bool{}
	if !s.castOffenseLocked(ctx, shadowBolt, r.target, 3*time.Second) {
		s.roadStopLocked(ctx, "cast_write_unknown_no_retry")
		return
	}
	r.phase = "own_cast_pending"
}
func (s *Service) roadFaceLocked(ctx context.Context, aim position) bool {
	clock, err := s.session.GetClock(ctx, &emptypb.Empty{})
	if err != nil || clock == nil {
		return false
	}
	if !s.roadSessionLocked(ctx, s.road.session) {
		return false
	}
	w, err := s.roadWorldLocked(ctx, s.road.target)
	current, currentKnown, _ := creaturePosition(roadTarget(w, s.road.target))
	if err != nil || !s.roadFrameLocked(w, !s.road.reserved) || !s.roadProximityLocked(w) || !currentKnown || distance(current, aim) > .25 || ctx.Err() != nil || !s.now().Before(s.road.until) {
		return false
	}
	facing := s.pos
	facing.o = float32(math.Atan2(float64(aim.y-facing.y), float64(aim.x-facing.x)))
	if facing.o < 0 {
		facing.o += 2 * math.Pi
	}
	if !s.sendLocked(ctx, opcode.MSGMoveSetFacing, movementPacket(s.selfGUID, clock.ClientTimeMs, facing, false)) {
		s.state.MotionUnconfirmed = true
		return false
	}
	s.pos = facing
	s.posEstimated = true
	s.posAt = s.now()
	s.posRevision++
	return true
}

func (s *Service) roadPacketLocked(op uint32, p []byte) {
	if op == opcode.SMSGInitialSpells {
		known, err := initialSpellKnown(p, shadowBolt)
		s.roadSpellbookKnown = err == nil && known
		if err == nil && p[0] > 1 {
			s.roadSpellbookKnown = false
		}
		if err == nil {
			ids := map[uint32]bool{}
			for i := 0; i < int(binary.LittleEndian.Uint16(p[1:])); i++ {
				id := binary.LittleEndian.Uint32(p[3+i*6:])
				if id == 0 || ids[id] {
					s.roadSpellbookKnown = false
				}
				ids[id] = true
			}
		}
		// Fresh maintained Services subscribe before login. Only the first full
		// login baseline with no earlier outbound/unknown cast can seed idle.
		if !s.roadInitialSeen && s.roadSpellbookKnown && !s.roadCast.busy && !s.roadCast.poisoned {
			s.roadCast.known = true
			s.roadEpochKnown = true
		}
		s.roadInitialSeen = true
		if err != nil && s.road.active {
			s.road.active, s.road.invalid, s.road.phase = false, true, "spellbook_invalidated"
		}
		return
	}
	if op == opcode.SMSGLearnedSpell || op == opcode.SMSGRemovedSpell || op == opcode.SMSGSupercededSpell {
		_, _, err := spellKnowledgePacket(op, p)
		if err != nil {
			s.roadSpellbookKnown = false
		}
		return
	}
	if op == opcode.SMSGSpellnonmeleedamagelog {
		caster, victim, spell, damage, err := decodeSpellDamage(p)
		if err != nil && s.road.active {
			s.road.active, s.road.invalid, s.road.phase = false, true, "damage_packet_unknown"
			return
		}
		if err == nil && s.road.active && victim == s.selfGUID && caster != s.selfGUID && damage > 0 {
			// This mode has only reviewed ordinary selected-source melee.
			// Incoming spell damage never establishes contact or safe adds.
			s.road.active, s.road.invalid, s.road.phase = false, true, "unexpected_incoming_spell_damage"
			return
		}
		if err == nil && caster == s.selfGUID && s.road.active && s.road.reserved {
			h := sha256.Sum256(p)
			if victim != s.road.source || spell != shadowBolt || damage == 0 || !s.road.waitingResult || !s.road.resultGo || !s.now().Before(s.road.resultUntil) || s.road.damageFrames[h] {
				s.road.active, s.road.invalid, s.road.phase = false, true, "damage_result_ambiguous_no_retry"
				return
			}
			s.road.damageFrames[h] = true
			s.road.damageObserved, s.road.resultDamage = true, true
		}
		return
	}
	if op == opcode.SMSGAttackerstateupdate {
		attacker, victim, damage, err := decodeMeleeDamage(p)
		if err != nil && s.road.active {
			s.road.active, s.road.invalid, s.road.phase = false, true, "melee_packet_unknown"
			return
		}
		if err == nil && s.road.active && victim == s.selfGUID && damage > 0 {
			if attacker != s.road.source {
				s.road.active, s.road.invalid, s.road.phase = false, true, "unexpected_melee_attacker"
			} else {
				s.road.meleeAt = s.now()
				s.road.progressAt = s.road.meleeAt
			}
		}
		return
	}
	if op == opcode.SMSGAttackstart {
		if len(p) != 16 {
			if s.road.active {
				s.road.active, s.road.invalid, s.road.phase = false, true, "attack_packet_unknown"
			}
			return
		}
		a, v := binary.LittleEndian.Uint64(p), binary.LittleEndian.Uint64(p[8:])
		if s.road.active && v == s.selfGUID && a != s.road.target {
			s.road.active, s.road.invalid, s.road.phase = false, true, "unexpected_typed_attacker"
			return
		}
		if s.road.active && a == s.road.target {
			if !s.road.reserved || v != s.selfGUID {
				s.road.active, s.road.invalid, s.road.phase = false, true, "selected_victim_changed"
			} else {
				s.road.victimObserved = true
			}
		}
		if s.road.active && a == s.selfGUID {
			s.road.active, s.road.invalid, s.road.phase = false, true, "unexpected_auto_attack"
		}
		return
	}
	if op == opcode.SMSGAttackstop {
		a, n, e := readGUID(p)
		if e != nil {
			if s.road.active {
				s.road.active, s.road.invalid, s.road.phase = false, true, "attack_stop_unknown"
			}
			return
		}
		v, m, e := readGUID(p[n:])
		if e != nil || len(p) != n+m+4 {
			if s.road.active && a == s.road.target {
				s.road.active, s.road.invalid, s.road.phase = false, true, "attack_stop_unknown"
			}
			return
		}
		dead := binary.LittleEndian.Uint32(p[n+m:])
		if s.road.active && a == s.road.target {
			s.road.victimObserved = false
			s.road.stopObserved = true
			s.road.stopAt = s.now()
			s.road.deathHint = dead == 1 && (v == s.selfGUID || v == 0)
			if dead > 1 || !s.road.deathHint {
				s.road.active, s.road.invalid, s.road.phase = false, true, "selected_attack_stopped_no_retry"
			}
		}
		return
	}
	if op == opcode.SMSGSpellStart || op == opcode.SMSGSpellGo {
		guid, count, spell, offset, err := castEvent(p)
		if err != nil {
			s.roadPoisonCastLocked()
			return
		}
		if guid != s.selfGUID {
			return
		}
		// Only a complete supported unit-target frame can establish this mode's
		// cast proof. Trying candidate GUIDs never creates target authority.
		target := uint64(0)
		for _, v := range []uint64{s.selfGUID, s.pendingTarget, s.road.target, s.roadCast.target} {
			if v != 0 && validCastTargetBody(p, offset, op == opcode.SMSGSpellGo, v) {
				target = v
				break
			}
		}
		if target == 0 {
			s.roadPoisonCastLocked()
			return
		}
		if op == opcode.SMSGSpellStart {
			if s.road.active && (!s.state.CastPending || spell != shadowBolt || count != s.castCount || target != s.road.target) {
				s.road.active, s.road.invalid, s.road.phase = false, true, "unexpected_own_cast"
			}
			if s.roadCast.poisoned {
				return
			}
			if s.roadCast.busy && (s.roadCast.spell != spell || s.roadCast.count != count || s.roadCast.target != target) {
				s.roadPoisonCastLocked()
				return
			}
			s.roadCast = roadCastObservation{busy: true, started: true, spell: spell, count: count, target: target}
			return
		}
		if !s.roadCast.poisoned && s.roadCast.busy && s.roadCast.started && s.roadCast.spell == spell && s.roadCast.count == count && s.roadCast.target == target {
			s.roadCast = roadCastObservation{known: true}
			if s.road.active && s.state.CastPending && s.pendingSpell == shadowBolt && s.pendingTarget == s.road.target && count == s.castCount {
				if !roadGoHit(p, offset, target) {
					s.road.active, s.road.invalid, s.road.phase = false, true, "cast_missed_no_retry"
				} else {
					s.road.goObserved, s.road.resultGo = true, true
				}
			}
		} else {
			s.roadPoisonCastLocked()
		}
		return
	}
	if op == opcode.SMSGCastFailed || op == opcode.SMSGSpellFailure || op == opcode.SMSGSpellFailedOther {
		count, spell := byte(0), uint32(0)
		if op == opcode.SMSGCastFailed {
			if len(p) != 6 {
				s.roadPoisonCastLocked()
				return
			}
			count, spell = p[0], binary.LittleEndian.Uint32(p[1:])
		} else {
			guid, n, err := readGUID(p)
			if err != nil {
				s.roadPoisonCastLocked()
				return
			}
			if guid != s.selfGUID {
				return
			}
			if len(p) != n+6 {
				s.roadPoisonCastLocked()
				return
			}
			count, spell = p[n], binary.LittleEndian.Uint32(p[n+1:])
		}
		// Failure does not grant a retry or a source-death receipt. Legacy packet
		// handling still accounts exact matching module failures below this hook.
		if s.roadCast.poisoned || !s.roadCast.busy || s.roadCast.count != count || s.roadCast.spell != spell {
			s.roadPoisonCastLocked()
			return
		}
		if s.road.active {
			s.road.active, s.road.invalid, s.road.phase = false, true, "cast_failed_no_retry"
		}
		s.roadCast = roadCastObservation{known: true}
	}
}
func (s *Service) roadPoisonCastLocked() {
	s.roadCast.known, s.roadCast.poisoned = false, true
	if s.road.active {
		s.road.active, s.road.invalid, s.road.phase = false, true, "current_cast_unknown_no_retry"
	}
}
func roadGoHit(p []byte, offset int, target uint64) bool {
	i := offset + 8
	if i >= len(p) {
		return false
	}
	n := int(p[i])
	i++
	found := false
	if n != 1 {
		return false
	}
	if n > (len(p)-i)/8 {
		return false
	}
	for j := 0; j < n; j++ {
		if binary.LittleEndian.Uint64(p[i:]) == target {
			found = true
		}
		i += 8
	}
	return found && i < len(p) && p[i] == 0
}

// Keep the retained terminal receipt immutable through Stop; a later explicit
// non-road operation may hide publication but can never erase replay fences.
func (s *Service) roadHideLocked() { s.road.selected = false }

func (s *Service) roadSendAllowedLocked(ctx context.Context, op uint32, p []byte) bool {
	if !s.road.active {
		return true
	}
	if op == opcode.MSGMoveStop {
		return true
	}
	if op != opcode.MSGMoveSetFacing && op != opcode.CMSGCastSpell {
		return false
	}
	if ctx.Err() != nil || !s.now().Before(s.road.until) || !s.roadFrameLocked(s.lastWorld, !s.road.reserved) || !s.roadProximityLocked(s.lastWorld) {
		return false
	}
	if op == opcode.CMSGCastSpell {
		return s.road.reserved && s.road.source == s.pendingTarget && s.pendingSpell == shadowBolt && bytes.Equal(p, spellPacket(s.castCount, shadowBolt, s.road.source)) && s.roadCast.known && !s.roadCast.busy && !s.roadCast.poisoned
	}
	return true
}
