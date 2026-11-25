package buddyactions

import (
	"context"
	"math"
	"reflect"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

type fargodeepEncounter struct {
	active              bool
	target, engager     uint64
	session, phase      string
	bodySession         string
	self, visit         uint64
	until               time.Time
	pulled              bool
	route               []position
	routeIndex, replans int
	routeRevision       uint64
	routeExpected       position
	routeTarget         position
}

// This is the fixed quest47/60 mission scene, not a generic navigation fence.
// Public creature.sql rows for40/475 in Fargodeep lie inside these bounds.
func fargodeepArea(p position) bool {
	return finite(p) && p.x >= -10000 && p.x <= -9700 && p.y >= 50 && p.y <= 320 && p.z >= -10 && p.z <= 65
}

// A patrol packet contains an actual start and an advertised endpoint, not
// enough information to interpolate its path through the mine. Only a recent
// start or recently elapsed ordinary endpoint is position authority here.
func fargodeepPatrol(c *worldv1.CreatureState) bool {
	if c == nil || !c.PositionKnown || c.MovementFlags & ^uint32(0x080001ff) != 0 {
		return false
	}
	t := c.Trajectory
	if t == nil || !t.Known || t.Stopped || t.Cyclic || t.TransportRelative || !t.DestinationKnown || t.DurationMs == 0 || t.DurationMs > 120000 {
		return false
	}
	start := position{c.X, c.Y, c.Z, c.Orientation}
	end := position{t.DestinationX, t.DestinationY, t.DestinationZ, c.Orientation}
	return fargodeepArea(start) && fargodeepArea(end) && distance(start, end) <= 60
}

func fargodeepCreaturePosition(c *worldv1.CreatureState) (position, bool) {
	if c == nil || c.MovementFlags & ^uint32(0x080001ff) != 0 {
		return position{}, false
	}
	t := c.Trajectory
	if t != nil && t.Known && !t.Stopped {
		if !fargodeepPatrol(c) {
			return position{}, false
		}
		if t.AgeMs >= uint64(t.DurationMs) {
			p, known, _ := creaturePosition(c)
			return p, known && t.AgeMs-uint64(t.DurationMs) <= 5000
		}
		// This is the actual packet header, never a start/end chord estimate.
		if c.PositionAgeMs <= 750 && t.AgeMs <= 750 {
			return position{c.X, c.Y, c.Z, c.Orientation}, true
		}
		return position{}, false
	}
	if c.MovementFlags&0x080000ff != 0 && (t == nil || !t.Known || !t.Stopped) {
		return position{}, false
	}
	p, known, _ := creaturePosition(c)
	return p, known
}

func (f *fargodeepEncounter) snapshot(now time.Time) *actionsv1.FargodeepEncounterState {
	out := &actionsv1.FargodeepEncounterState{Active: f.active, TargetGuid: f.target, EngagerGuid: f.engager, Phase: f.phase}
	if f.active && now.Before(f.until) {
		out.RemainingMs = uint64(f.until.Sub(now).Milliseconds())
	}
	return out
}

// Item objectives are authoritative only in the own quest log's COMPLETE bit.
// Its four small counters are creature objective counters, not item quantities.
func ownFargodeepQuests(w *worldv1.SnapshotResponse) (active, incomplete bool) {
	if w == nil || !w.SelfQuestLogKnown || len(w.SelfQuestLog) != 25 {
		return
	}
	slots, ids := [25]bool{}, map[uint32]bool{}
	for _, q := range w.SelfQuestLog {
		if q == nil || q.Slot >= 25 || slots[q.Slot] || q.QuestId != 0 && ids[q.QuestId] {
			return false, false
		}
		slots[q.Slot] = true
		if q.QuestId != 0 {
			ids[q.QuestId] = true
		}
		if (q.QuestId == 47 || q.QuestId == 60) && !q.Failed && q.State&2 == 0 {
			active = true
			incomplete = incomplete || !q.Complete && q.State&1 == 0
		}
	}
	return
}
func fargodeepCharacter(w *worldv1.SnapshotResponse, self uint64) bool {
	p := w.GetSelf()
	return (self == 1 || self == 4 || self == 5) && safeSelfCombat(w, self) == "" && p.MapId == 0 && p.LevelKnown && p.Level >= 4 && p.Level <= 10 && p.ClassKnown && map[uint64]uint32{1: 5, 4: 8, 5: 4}[self] == p.ClassId && p.MaxHealth > 0 && p.Health <= p.MaxHealth && p.SpeedsKnown && math.Abs(float64(p.RunSpeed)-7) <= .01
}

func (s *Service) readFargodeepPartyLocked(ctx context.Context) *partyv1.StateResponse {
	call, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	req, _ := anypb.New(&emptypb.Empty{})
	result, err := s.session.InvokeModule(call, &modv1.InvokeModuleRequest{Module: "buddy-party", Method: "state", Request: req})
	p := &partyv1.StateResponse{}
	if err != nil || result == nil || result.Result == nil || result.Result.UnmarshalTo(p) != nil {
		return nil
	}
	return p
}

func (s *Service) fargodeepPartyLocked(ctx context.Context, w *worldv1.SnapshotResponse, session string, ready bool, target position) (map[uint64]bool, string) {
	p := s.readFargodeepPartyLocked(ctx)
	members, reason := s.fargodeepPartyObservationLocked(w, p, session, ready, target)
	return members, reason
}

func (s *Service) fargodeepPartyObservationLocked(w *worldv1.SnapshotResponse, p *partyv1.StateResponse, session string, ready bool, target position) (map[uint64]bool, string) {
	if !fargodeepCharacter(w, s.selfGUID) || len(w.NearbyPlayers) > 64 {
		return nil, "own_state_unknown_or_unsafe"
	}
	if w.Owner != nil && w.Owner.Present {
		return nil, "owner_returned"
	}

	if p == nil || p.SessionId != session || p.Group == nil || !p.Group.Known || !p.Group.InGroup || len(p.Group.Members) < 3 || len(p.Group.Members) > 40 {
		return nil, "current_party_unknown_or_changed"
	}
	seen, online := map[uint64]bool{}, map[uint64]bool{}
	for _, m := range p.Group.Members {
		if m == nil || m.Guid == 0 || m.Guid>>48 != 0 || seen[m.Guid] || m.Self != (m.Guid == s.selfGUID) {
			return nil, "current_party_unknown_or_changed"
		}
		seen[m.Guid], online[m.Guid] = true, m.Online
	}
	for guid, class := range map[uint64]uint32{1: 5, 4: 8, 5: 4} {
		v := observedHealingPlayer(w, guid)
		if !online[guid] || !safeHealingPlayer(v, 0) || !v.ClassKnown || v.ClassId != class || !v.LevelKnown || v.Level < 4 || v.Level > 10 || v.MaxHealth == 0 || v.Health > v.MaxHealth || v.PlayerFlags&2 != 0 {
			return nil, "companion_observation_unknown_or_unsafe"
		}
		pos := playerPosition(v)
		if guid == s.selfGUID {
			pos = s.pos
		}
		if distance(s.pos, pos) > 35 || ready && distance(pos, target) > 28 {
			return nil, "companions_outside_support_range"
		}
		if ready && (!v.HealthKnown || v.Health == 0 || v.UnitFlags&0x80000 != 0 || !v.PowerKnown || class == 4 && v.PowerType != 3 || class != 4 && v.PowerType != 0) {
			return nil, "companions_recovering_before_pull"
		}
	}
	return map[uint64]bool{1: true, 4: true, 5: true}, ""
}

func (s *Service) stopFargodeepLocked(ctx context.Context, reason string) {
	s.fargodeep.active = false
	s.fargodeep.phase = reason
	s.fargodeep.route = nil
	s.stopLocked(ctx)
	s.stopAutoAttackLocked(ctx)
	s.lastStep = s.now()
	s.state.Reason = "fargodeep_" + reason
}
func (s *Service) FargodeepEncounter(ctx context.Context, r *actionsv1.FargodeepEncounterRequest) (*actionsv1.State, error) {
	if r == nil || len(r.SessionId) == 0 || len(r.SessionId) > 128 {
		return nil, status.Error(codes.InvalidArgument, "canonical body session is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.independent {
		return nil, status.Error(codes.FailedPrecondition, "companion encounter unavailable")
	}
	if s.hostEncounterKey != "" {
		return s.inactiveNativeEncounterLocked(r.Enabled)
	}
	if !r.Enabled {
		if s.fargodeep.session != "" && (r.SessionId != s.fargodeep.session || r.TargetGuid != 0 && r.TargetGuid != s.fargodeep.target || r.EngagerGuid != 0 && r.EngagerGuid != s.fargodeep.engager) {
			return nil, status.Error(codes.FailedPrecondition, "encounter binding changed")
		}
		if !s.fargodeep.active {
			return s.stateLocked(), nil
		}
		s.stopFargodeepLocked(ctx, "disabled")
		return s.stateLocked(), nil
	}
	if r.TargetGuid == 0 || r.EngagerGuid == 0 || r.EngagerGuid>>48 != 0 || r.ExpectedQuestLogRevision == 0 {
		return nil, status.Error(codes.InvalidArgument, "target, designated engager and own log revision are required")
	}
	if s.fargodeep.active {
		if s.fargodeep.target == r.TargetGuid && s.fargodeep.engager == r.EngagerGuid && s.fargodeep.session == r.SessionId {
			return s.stateLocked(), nil
		}
		return nil, status.Error(codes.FailedPrecondition, "another encounter is active")
	}
	if s.fargodeepSeen[r.TargetGuid] || len(s.fargodeepSeen) >= 128 {
		return nil, status.Error(codes.FailedPrecondition, "target already reserved or encounter limit reached")
	}
	if !s.state.Assisting || !s.now().Before(s.assistUntil) || s.state.InteractionOwner != "" || s.state.Traveling || s.state.Practicing || s.questApproaching || s.state.CastPending || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed || s.autoTarget != 0 {
		return nil, status.Error(codes.FailedPrecondition, "encounter requires enabled assistance and confirmed idle ownership")
	}
	w, err := s.snapshot(ctx, r.TargetGuid)
	if err != nil || w.GetSelf().GetGuid() != s.selfGUID || w.SelfQuestLogRevision != r.ExpectedQuestLogRevision ||
		safeSelfCombat(w, s.selfGUID) != "" || !s.refreshPositionLocked(w) {
		return nil, status.Error(codes.FailedPrecondition, "own quest/body observations changed")
	}
	previous := s.fargodeep
	candidate := fargodeepEncounter{active: true, target: r.TargetGuid, engager: r.EngagerGuid, session: r.SessionId,
		bodySession: s.sessionID, self: s.selfGUID, visit: w.VisitRevision,
		phase: "admission_pending", until: s.now().Add(2 * time.Minute)}
	s.fargodeep = candidate
	ctx = s.fargodeepReadinessContextLocked(ctx)
	refuse := func(reason string) (*actionsv1.State, error) {
		// Never restore an earlier task over a callback's replacement owner.
		if reflect.DeepEqual(s.fargodeep, candidate) {
			s.fargodeep = previous
		}
		return nil, status.Error(codes.FailedPrecondition, reason)
	}
	decision, _, valid := s.fargodeepPolicySnapshotLocked(ctx, w, s.readFargodeepPartyLocked(ctx), "arm")
	if !valid || decision.kind != "arm" {
		return refuse("accepted Fargodeep policy refused current observations")
	}
	if !s.stopLocked(ctx) {
		return refuse("movement stop is unconfirmed")
	}
	party := s.readFargodeepPartyLocked(ctx)
	fresh, err := s.snapshot(ctx, r.TargetGuid)
	if err != nil || !s.fargodeepReadinessMatchesLocked(ctx, decision, fresh, party, false) {
		return refuse("Fargodeep admission source or observations changed")
	}
	if s.fargodeepSeen == nil {
		s.fargodeepSeen = map[uint64]bool{}
	}
	s.fargodeepSeen[r.TargetGuid] = true // Reserve before any future pull; never replay an uncertain target.
	s.fargodeep.phase = decision.phase
	s.lastStep = s.now()
	return s.stateLocked(), nil
}

func (s *Service) fargodeepTickLocked(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	if !s.fargodeep.active {
		return false
	}
	ctx = s.fargodeepReadinessContextLocked(ctx)
	f := &s.fargodeep
	if f.bodySession != s.sessionID || f.self != s.selfGUID || w == nil || w.VisitRevision != f.visit {
		f.active, f.route, f.phase = false, nil, "body_or_visit_changed"
		return true
	}
	if s.state.MotionUnconfirmed || safeSelfCombat(w, s.selfGUID) != "" || !s.refreshPositionLocked(w) {
		s.stopFargodeepLocked(ctx, "own_state_unknown_or_unsafe")
		return true
	}
	fresh, err := s.snapshot(ctx, f.target)
	if err != nil || !s.refreshPositionLocked(fresh) {
		s.stopFargodeepLocked(ctx, "observations_unavailable")
		return true
	}
	decision, current, valid := s.fargodeepPolicySnapshotLocked(ctx, fresh, s.readFargodeepPartyLocked(ctx), "tick")
	if !valid {
		if s.fargodeepCleanupCurrentLocked(ctx) {
			s.stopLocked(ctx)
			f.phase = "readiness_policy_unavailable_or_changed"
		}
		return true
	}
	f.pulled, f.phase = decision.pulled, decision.phase
	ctx.Value(fargodeepReadinessKey{}).(*fargodeepReadinessScope).pulled = f.pulled
	switch decision.kind {
	case "stop":
		s.stopFargodeepLocked(ctx, decision.phase)
	case "hold":
		s.stopLocked(ctx)
		if s.fargodeepAttemptCurrentLocked(ctx) {
			s.stopAutoAttackLocked(ctx)
		}
	case "approach":
		s.fargodeepRouteStepLocked(ctx, current)
	case "attack":
		if !s.stopLocked(ctx) {
			s.stopFargodeepLocked(ctx, "movement_stop_unconfirmed")
			return true
		}
		attempts := s.state.AssistAttempts
		s.starterAssistObservedTargetLocked(ctx, current, practiceTargetObservation(current, f.target))
		if !s.fargodeepAttemptCurrentLocked(ctx) {
			return true
		}
		if s.state.AssistAttempts > attempts || s.autoTarget == f.target {
			f.pulled, f.route = true, nil
			if !decision.pulled {
				f.phase = "initial_attack_written_waiting_for_attack_start"
			}
		}
	}
	return true
}

// Lua requests an approach; this cache solves and writes one physical step.
// A route never authorizes an enemy, a role, a pull or the next gameplay step.
func (s *Service) fargodeepRouteStepLocked(ctx context.Context, w *worldv1.SnapshotResponse) {
	f := &s.fargodeep
	target := practiceTargetObservation(w, f.target)
	p, known := fargodeepCreaturePosition(target)
	if !known {
		return
	}
	if f.route != nil && (f.routeRevision != s.positionRevision || distance(f.routeExpected, s.pos) > .05) {
		f.route = nil
	}
	if f.route == nil || distance(f.routeTarget, p) > .25 {
		if !s.stopLocked(ctx) {
			s.stopFargodeepLocked(ctx, "movement_stop_unconfirmed")
			return
		}
		if f.replans >= 12 {
			s.stopFargodeepLocked(ctx, "route_replan_limit")
			return
		}
		f.replans++
		path, err := s.mapNavigationPath(ctx, s.navDataDir, s.posMap, s.pos, p)
		if err != nil || routeRemaining(s.pos, path, 0) > 60 {
			s.state.NavAvailable, s.state.NavReason = false, "mission_route_exceeds_60yd"
			if err != nil {
				s.state.NavReason = navigationDiagnostic(err, s.navDataDir)
			}
			s.stopFargodeepLocked(ctx, "navigation_unavailable")
			return
		}
		fresh, err := s.snapshot(ctx, f.target)
		if err != nil || !s.refreshPositionLocked(fresh) || len(path) == 0 || distance(s.pos, path[0]) > 2 {
			s.stopFargodeepLocked(ctx, "observations_changed_during_plan")
			return
		}
		current := practiceTargetObservation(fresh, f.target)
		cp, valid := fargodeepCreaturePosition(current)
		if !valid || distance(cp, p) > .25 || current.PositionRevision != target.PositionRevision {
			f.route, f.phase = nil, "waiting_to_replan_current_patrol_position"
			return
		}
		decision, _, valid := s.fargodeepPolicySnapshotLocked(ctx, fresh, s.readFargodeepPartyLocked(ctx), "tick")
		if !valid || decision.kind != "approach" {
			return
		}
		f.route, f.routeIndex, f.routeTarget = path, 0, p
		f.routeRevision, f.routeExpected = s.positionRevision, s.pos
		s.state.NavAvailable, s.state.NavReason = true, "client_navmesh_route_verified"
		f.phase = "verified_route_waiting_fresh_frame"
		return
	}
	if !s.stopAutoAttackLocked(ctx) {
		return
	}
	moved := s.mapNavigationStepLocked(ctx, f.route, &f.routeIndex)
	if !s.fargodeepCleanupCurrentLocked(ctx) {
		return
	}
	if !moved {
		s.stopFargodeepLocked(ctx, "navigation_step_failed")
		return
	}
	f.routeRevision, f.routeExpected = s.positionRevision, s.pos
}

// STOP and facing callbacks can change observations. Rebind the actual hostile
// write to current body, party, quest, target and resources, without using Joel
// or a target-selection packet as authority.
func (s *Service) fargodeepWriteAllowedLocked(ctx context.Context, targetGUID uint64, spell uint32) bool {
	return s.fargodeepActionAllowedLocked(ctx, targetGUID, spell, false)
}

func (s *Service) fargodeepMovementAllowedLocked(ctx context.Context) bool {
	return s.fargodeepActionAllowedLocked(ctx, s.fargodeep.target, 0, true)
}

func (s *Service) fargodeepActionAllowedLocked(ctx context.Context, targetGUID uint64, spell uint32, movement bool) bool {
	return s.fargodeepObservedActionAllowedLocked(s.fargodeepReadinessContextLocked(ctx), targetGUID, spell, movement, nil, nil)
}

// Each pure read is followed by the complete native guard again. Carried
// readiness/spell decisions bound the passes and prevent recursive evaluation.
func (s *Service) fargodeepObservedActionAllowedLocked(ctx context.Context, targetGUID uint64, spell uint32, movement bool, selected *starterSpellDecision, readiness *fargodeepReadinessDecision) bool {
	f := &s.fargodeep
	current := func() bool {
		return ctx.Err() == nil && s.fargodeepAttemptCurrentLocked(ctx) && s.fargodeepReadinessCurrentLocked(ctx) &&
			f.active && targetGUID == f.target && s.now().Before(f.until) && s.state.Assisting && s.now().Before(s.assistUntil) &&
			s.state.InteractionOwner == "" && !s.state.MotionUnconfirmed
	}
	refuse := func() bool {
		if s.fargodeepAttemptCurrentLocked(ctx) {
			f.active, f.phase = false, "prewrite_observations_changed"
			s.state.Reason = "fargodeep_prewrite_observations_changed"
		}
		return false
	}
	if !current() {
		return false
	}
	party := s.readFargodeepPartyLocked(ctx)
	before, revision, mapID := s.pos, s.positionRevision, s.posMap
	w, err := s.snapshot(ctx, f.target)
	if !current() {
		return false
	}
	if w != nil && (f.bodySession != s.sessionID || f.self != s.selfGUID || w.VisitRevision != f.visit) {
		f.active, f.route, f.phase = false, nil, "body_or_visit_changed"
		return false
	}
	if err != nil || safeSelfCombat(w, s.selfGUID) != "" || !s.refreshPositionLocked(w) || s.positionRevision != revision || s.posMap != mapID || distance(before, s.pos) > .05 {
		return refuse()
	}
	c := practiceTargetObservation(w, f.target)
	p, known := fargodeepCreaturePosition(c)
	if !known {
		return refuse()
	}
	wanted, stage := "attack", "offense"
	if movement {
		wanted, stage = "approach", "movement"
	}
	if readiness == nil {
		decision, valid := s.fargodeepReadinessDecisionLocked(ctx, w, party, stage)
		if !valid || decision.kind != wanted {
			return false
		}
		return s.fargodeepObservedActionAllowedLocked(ctx, targetGUID, spell, movement, selected, &decision)
	}
	if readiness.kind != wanted || !s.fargodeepReadinessMatchesLocked(ctx, *readiness, w, party, false) {
		return false
	}
	if !current() {
		return false
	}
	if movement {
		return f.route != nil && f.routeRevision == s.positionRevision && distance(f.routeExpected, s.pos) <= .05 &&
			distance(f.routeTarget, p) <= 1.5
	}
	self := w.Self
	if self.ClassId == 4 {
		if !s.state.KnownAutoAttack || !s.weaponReady(self) || self.UnitFlags&(0x20000|0x200000) != 0 || !self.BaseAttackTimeKnown ||
			!self.CombatReachKnown || !c.CombatReachKnown || distance(s.pos, p) > math.Max(5, float64(self.CombatReach+c.CombatReach)+4.0/3.0)-.5 {
			return refuse()
		}
		if spell == 0 {
			return true
		}
		rank := s.learnedContractsLocked()[spell]
		if !s.starterWeaponMechanicsLocked(w, c, p, rank) {
			return refuse()
		}
		if selected != nil {
			return selected.kind == "cast" && selected.spell.ID == spell && s.starterSpellFactsMatchLocked(*selected, w, c, p)
		}
		decision := s.starterWeaponDecisionLocked(ctx, w, c, p)
		if !s.fargodeepAttemptCurrentLocked(ctx) || !s.fargodeepSelectionRevisionLocked(ctx) {
			return false
		}
		if !decision.valid || decision.kind != "cast" || decision.spell.ID != spell {
			return refuse()
		}
		return s.fargodeepObservedActionAllowedLocked(ctx, targetGUID, spell, false, &decision, nil)
	}
	rank, learned := s.learnedContractsLocked()[spell]
	cost := spellCost(rank, self)
	if !learned || rank.Capability().Kind != spellcatalog.Damage || self.UnitFlags&0x2000 != 0 ||
		!self.PowerKnown || int32(self.PowerType) != rank.Power || cost == 0 || uint64(self.Power) < cost ||
		!s.encounterRankResourcesAvailable(rank, self) || distance(s.pos, p) < rank.MinHostile || distance(s.pos, p) > rank.MaxHostile ||
		self.ClassId == 5 && !s.hasLearnedHealingLocked() {
		return refuse()
	}
	if selected != nil {
		if selected.kind != "cast" || selected.spell.ID != spell || !s.starterSpellFactsMatchLocked(*selected, w, c, p) {
			return refuse()
		}
		return true
	}
	decision := s.starterSpellDecisionLocked(ctx, w, c, p)
	if !s.fargodeepAttemptCurrentLocked(ctx) || !s.fargodeepSelectionRevisionLocked(ctx) {
		return false
	}
	if !decision.valid || decision.kind != "cast" || decision.spell.ID != spell {
		return refuse()
	}
	return s.fargodeepObservedActionAllowedLocked(ctx, targetGUID, spell, false, &decision, nil)
}
