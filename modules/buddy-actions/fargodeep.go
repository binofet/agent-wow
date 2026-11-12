package buddyactions

import (
	"context"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

type fargodeepEncounter struct {
	active              bool
	target, engager     uint64
	session, phase      string
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

// Unknown in-flight neighbors cannot be treated as absent. The maximum normal
// ground displacement from the actual header bounds their possible proximity;
// no point along the unobserved patrol is fabricated.
func fargodeepNeighborNear(c *worldv1.CreatureState, target position) bool {
	if p, known := fargodeepCreaturePosition(c); known {
		return distance(p, target) < 8
	}
	if fargodeepPatrol(c) {
		start := position{c.X, c.Y, c.Z, c.Orientation}
		age, duration := float64(c.Trajectory.AgeMs), float64(c.Trajectory.DurationMs)
		end := position{c.Trajectory.DestinationX, c.Trajectory.DestinationY, c.Trajectory.DestinationZ, c.Orientation}
		return distance(start, target) < 8+7*age/1000 && distance(end, target) < 8+7*math.Abs(duration-age)/1000
	}
	return c != nil && c.PositionKnown && finite(position{c.X, c.Y, c.Z, c.Orientation}) && distance(position{c.X, c.Y, c.Z, c.Orientation}, target) < 8
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
func eligibleFargodeepKobold(c *worldv1.CreatureState, level uint32) bool {
	return c != nil && c.Guid>>48 == 0xf130 && uint32(c.Guid>>24)&0xffffff == c.Entry && (c.Entry == 40 || c.Entry == 475) && c.Present && c.MapKnown && c.MapId == 0 && c.LevelKnown && c.Level >= 5 && c.Level <= 7 && uint64(c.Level) <= uint64(level)+1 && c.AliveKnown && c.Alive && c.HealthKnown && c.Health > 0 && c.MaxHealth >= c.Health && c.NpcFlags == 0 && c.UnitFlagsKnown && c.DynamicFlagsKnown && c.UnitFlags&(0x2|0x80|0x100|0x10000|0x2000000|0x80000000) == 0 && c.UnitFlags_2&1 == 0 && c.DynamicFlags&0x20 == 0
}
func fargodeepCharacter(w *worldv1.SnapshotResponse, self uint64) bool {
	p := w.GetSelf()
	return (self == 1 || self == 4 || self == 5) && safeSelfCombat(w, self) == "" && p.MapId == 0 && p.LevelKnown && p.Level >= 4 && p.Level <= 10 && p.ClassKnown && map[uint64]uint32{1: 5, 4: 8, 5: 4}[self] == p.ClassId && p.MaxHealth > 0 && p.Health <= p.MaxHealth && p.SpeedsKnown && math.Abs(float64(p.RunSpeed)-7) <= .01
}

func (s *Service) fargodeepPartyLocked(ctx context.Context, w *worldv1.SnapshotResponse, session string, ready bool, target position) (map[uint64]bool, string) {
	if !fargodeepCharacter(w, s.selfGUID) || len(w.NearbyPlayers) > 64 {
		return nil, "own_state_unknown_or_unsafe"
	}
	if w.Owner != nil && w.Owner.Present {
		return nil, "owner_returned"
	}
	call, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	req, _ := anypb.New(&emptypb.Empty{})
	result, err := s.session.InvokeModule(call, &modv1.InvokeModuleRequest{Module: "buddy-party", Method: "state", Request: req})
	p := &partyv1.StateResponse{}
	if err != nil || result == nil || result.Result == nil || result.Result.UnmarshalTo(p) != nil || p.SessionId != session || p.Group == nil || !p.Group.Known || !p.Group.InGroup || len(p.Group.Members) < 3 || len(p.Group.Members) > 40 {
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
		if ready && (v.UnitFlags&0x80000 != 0 || uint64(v.Health)*100 < uint64(v.MaxHealth)*95 || !v.PowerKnown || class == 4 && (v.PowerType != 3 || v.Power < 90) || class != 4 && (v.PowerType != 0 || manaBudget(v, 45) == 0 || uint64(v.Power) < manaBudget(v, 45))) {
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
	if r.TargetGuid == 0 || (r.EngagerGuid != 1 && r.EngagerGuid != 4 && r.EngagerGuid != 5) || r.ExpectedQuestLogRevision == 0 {
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
	active, incomplete := ownFargodeepQuests(w)
	if err != nil || !active || r.EngagerGuid == s.selfGUID && !incomplete || w.SelfQuestLogRevision != r.ExpectedQuestLogRevision || !fargodeepCharacter(w, s.selfGUID) || !s.refreshPositionLocked(w) {
		return nil, status.Error(codes.FailedPrecondition, "own quest/body observations changed")
	}
	target := practiceTargetObservation(w, r.TargetGuid)
	p, known := fargodeepCreaturePosition(target)
	if !known && fargodeepPatrol(target) {
		// Arming has no movement/attack authority. Wait for a usable fresh pose
		// rather than losing support-before-engager arming to a patrol update.
		p = position{target.X, target.Y, target.Z, target.Orientation}
		end := position{target.Trajectory.DestinationX, target.Trajectory.DestinationY, target.Trajectory.DestinationZ, target.Orientation}
		known = distance(s.pos, end) <= 35
	}
	if !eligibleFargodeepKobold(target, w.Self.Level) || !known || !fargodeepArea(p) || !fargodeepArea(s.pos) || distance(s.pos, p) > 35 || s.failedAssistTarget == r.TargetGuid {
		return nil, status.Error(codes.FailedPrecondition, "actual eligible kobold unavailable")
	}
	if _, reason := s.fargodeepPartyLocked(ctx, w, r.SessionId, false, p); reason != "" {
		return nil, status.Error(codes.FailedPrecondition, reason)
	}
	if !s.stopLocked(ctx) {
		return nil, status.Error(codes.FailedPrecondition, "movement stop is unconfirmed")
	}
	if s.fargodeepSeen == nil {
		s.fargodeepSeen = map[uint64]bool{}
	}
	s.fargodeepSeen[r.TargetGuid] = true // Reserve before any future pull; never replay an uncertain target.
	s.fargodeep = fargodeepEncounter{active: true, target: r.TargetGuid, engager: r.EngagerGuid, session: r.SessionId, phase: "armed_waiting_for_designated_engager", until: s.now().Add(2 * time.Minute)}
	s.lastStep = s.now()
	return s.stateLocked(), nil
}

func (s *Service) fargodeepTickLocked(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	if !s.fargodeep.active {
		return false
	}
	f := &s.fargodeep
	fail := func(reason string) bool { s.stopFargodeepLocked(ctx, reason); return true }
	if !s.now().Before(f.until) || !s.state.Assisting || !s.now().Before(s.assistUntil) {
		return fail("authorization_expired")
	}
	if s.state.MotionUnconfirmed || !fargodeepCharacter(w, s.selfGUID) || !s.refreshPositionLocked(w) {
		return fail("own_state_unknown_or_unsafe")
	}
	// Snapshot the exact bound GUID even when it falls outside the nearest32 view.
	fresh, err := s.snapshot(ctx, f.target)
	if err != nil || !fargodeepCharacter(fresh, s.selfGUID) || !s.refreshPositionLocked(fresh) {
		return fail("observations_unavailable")
	}
	w = fresh
	target := practiceTargetObservation(w, f.target)
	if target != nil && target.AliveKnown && !target.Alive && target.HealthKnown && target.Health == 0 {
		return fail("target_death_observed")
	}
	active, incomplete := ownFargodeepQuests(w)
	if !active || !f.pulled && f.engager == s.selfGUID && !incomplete {
		return fail("own_quest_goal_complete_or_unknown")
	}
	if !eligibleFargodeepKobold(target, w.Self.Level) || s.failedAssistTarget == f.target {
		return fail("target_unsafe_or_cast_failed")
	}
	p, known := fargodeepCreaturePosition(target)
	waitingPatrol := !known && fargodeepPatrol(target)
	if waitingPatrol {
		p = position{target.X, target.Y, target.Z, target.Orientation}
	}
	if !known && !waitingPatrol || !fargodeepArea(p) || !fargodeepArea(s.pos) || distance(s.pos, p) > 35 {
		return fail("target_position_unavailable_or_outside_range")
	}
	members, reason := s.fargodeepPartyLocked(ctx, w, f.session, false, p)
	if reason != "" {
		return fail(reason)
	}
	if len(w.NearbyCreatures) > 32 {
		return fail("creature_observations_overbound")
	}
	// An add attacking this body takes existing self-defense priority. No new
	// enemy is selected or pulled by this finite encounter.
	for _, c := range w.NearbyCreatures {
		if c != nil && c.Guid != f.target && c.Present && c.AliveKnown && c.Alive && c.AttackVictimKnown && members[c.AttackVictimGuid] && c.AttackAgeMs <= 30000 {
			s.stopFargodeepLocked(ctx, "additional_attacker_observed")
			return false
		}
	}
	engaged := target.AttackVictimKnown && members[target.AttackVictimGuid] && target.AttackAgeMs <= 30000
	if engaged {
		victim := observedHealingPlayer(w, target.AttackVictimGuid)
		engaged = victim != nil && (victim.UnitFlags&0x80000 != 0 || target.AttackAgeMs <= 5000)
	}
	if target.AttackVictimKnown && target.AttackVictimGuid != 0 && !members[target.AttackVictimGuid] || target.DynamicFlags&4 != 0 && target.DynamicFlags&8 == 0 {
		return fail("target_claimed_or_attacking_foreign_player")
	}
	if waitingPatrol {
		if !s.stopLocked(ctx) {
			return fail("movement_stop_unconfirmed")
		}
		s.stopAutoAttackLocked(ctx)
		f.route = nil
		f.phase = "waiting_for_fresh_patrol_position"
		return true
	}
	if engaged && !f.pulled {
		// A kobold can aggro during the verified approach. Actual ATTACKSTART
		// changes this to defense; it must not demand out-of-combat pull gates.
		f.pulled, f.route = true, nil
	}
	if !engaged && f.engager != s.selfGUID {
		s.stopLocked(ctx)
		f.phase = "waiting_for_actual_party_attack_start"
		return true
	}
	if !engaged && !f.pulled {
		if _, reason = s.fargodeepPartyLocked(ctx, w, f.session, true, p); reason != "" {
			s.stopLocked(ctx)
			f.phase = reason
			return true
		}
		if target.UnitFlags&0x80000 != 0 || target.DynamicFlags != 0 || target.AttackVictimKnown && target.AttackVictimGuid != 0 || !target.TargetKnown || target.TargetGuid != 0 {
			return fail("initial_target_not_idle_unclaimed")
		}
		for _, c := range w.NearbyCreatures {
			if c != nil && c.Guid != f.target && c.Present && c.AliveKnown && c.Alive && c.NpcFlags == 0 {
				if fargodeepNeighborNear(c, p) {
					s.stopLocked(ctx)
					f.phase = "waiting_for_isolated_kobold"
					return true
				}
			}
		}
	}
	if w.Self.ClassId == 5 && (!s.state.Healing || !s.now().Before(s.healingUntil) || !s.state.KnownLesserHeal) {
		return fail("priest_healing_intent_required")
	}
	if s.state.CastPending {
		s.stopLocked(ctx)
		f.phase = "waiting_for_actual_cast_result"
		return true
	}
	if s.state.AutoAttackUnconfirmed {
		s.stopLocked(ctx)
		f.phase = "waiting_for_server_attack_stop"
		return true
	}
	stand := float64(28) // Existing verified conservative Smite range.
	if w.Self.ClassId == 8 {
		stand = 33 // Existing verified conservative Fireball range.
	} else if w.Self.ClassId == 4 {
		stand = 3.5
	}
	// A fresh in-range unit-target spell needs no walking authority. The server
	// validates spell LOS/range; a rejected spell is never damage confirmation.
	// Every actual approach still requires the complete verified ground route.
	if f.engager == s.selfGUID && (w.Self.ClassId == 4 && !f.pulled || distance(s.pos, p) > stand) {
		if f.route != nil && (f.routeRevision != s.positionRevision || distance(f.routeExpected, s.pos) > .05) {
			f.route = nil
		}
		if f.route == nil || distance(f.routeTarget, p) > 1.5 || !f.pulled && distance(f.routeTarget, p) > .25 {
			if !s.stopLocked(ctx) {
				return fail("movement_stop_unconfirmed")
			}
			if f.replans >= 12 {
				return fail("route_replan_limit")
			}
			f.replans++ // Count every attempted fresh route, including moving-target races.
			path, err := s.mapNavigationPath(ctx, s.navDataDir, 0, s.pos, p)
			if err != nil || routeRemaining(s.pos, path, 0) > 60 {
				s.state.NavAvailable = false
				s.state.NavReason = "mission_route_exceeds_60yd"
				if err != nil {
					s.state.NavReason = navigationDiagnostic(err, s.navDataDir)
				}
				return fail("navigation_unavailable")
			}
			// A plan is no authority to reuse older body, party, quest or target views.
			after, err := s.snapshot(ctx, f.target)
			if err != nil || !fargodeepCharacter(after, s.selfGUID) || !s.refreshPositionLocked(after) || distance(s.pos, path[0]) > 2 {
				return fail("observations_changed_during_plan")
			}
			current := practiceTargetObservation(after, f.target)
			cp, ok := fargodeepCreaturePosition(current)
			if !eligibleFargodeepKobold(current, after.Self.Level) {
				return fail("target_changed_during_plan")
			}
			if !ok && fargodeepPatrol(current) || ok && (distance(cp, p) > .25 || current.PositionRevision != target.PositionRevision) {
				f.route = nil
				f.phase = "waiting_to_replan_current_patrol_position"
				return true
			}
			if !ok {
				return fail("target_changed_during_plan")
			}
			if _, why := s.fargodeepPartyLocked(ctx, after, f.session, !f.pulled, p); why != "" {
				return fail(why)
			}
			if a, i := ownFargodeepQuests(after); !a || !f.pulled && !i {
				return fail("quest_changed_during_plan")
			}
			w, target = after, current
			f.route, f.routeIndex, f.routeTarget = path, 0, p
			f.routeRevision, f.routeExpected = s.positionRevision, s.pos
			s.state.NavAvailable, s.state.NavReason = true, "client_navmesh_route_verified"
			f.phase = "verified_route_waiting_fresh_frame"
			return true
		}
		if distance(s.pos, p) > stand || routeRemaining(s.pos, f.route, f.routeIndex) > stand || !directRemainingRoute(s.pos, p, f.route, f.routeIndex) {
			if !s.stopAutoAttackLocked(ctx) {
				return true
			}
			if !s.mapNavigationStepLocked(ctx, f.route, &f.routeIndex) {
				return fail("navigation_step_failed")
			}
			f.routeRevision, f.routeExpected = s.positionRevision, s.pos
			f.phase = "approaching_designated_kobold_on_verified_route"
			return true
		}
	}
	if !engaged && f.pulled {
		s.stopLocked(ctx)
		f.phase = "waiting_for_actual_party_attack_start"
		return true
	}
	if !s.stopLocked(ctx) {
		return fail("movement_stop_unconfirmed")
	}
	if engaged {
		f.pulled = true
		f.route = nil
	}
	// This verified encounter is its own target authority. Shared class gates
	// still enforce known ranks, health/level, mana reserves, actual melee reach,
	// spell failures and server cast/attack receipts. Supports never chase.
	if w.Self.ClassId == 4 && f.engager != s.selfGUID && (!w.Self.CombatReachKnown || !target.CombatReachKnown || distance(s.pos, p) > math.Max(5, float64(w.Self.CombatReach+target.CombatReach)+4.0/3.0)-.5) {
		f.phase = "support_target_outside_melee_range"
		return true
	}
	attempts := s.state.AssistAttempts
	s.assistObservedTargetLocked(ctx, w, target, true)
	if s.state.AssistAttempts > attempts || s.autoTarget == f.target {
		f.pulled = true
		f.route = nil
	}
	if f.active {
		f.phase = "waiting_for_class_gate"
		if engaged {
			f.phase = "fighting_actual_bound_kobold"
		} else if f.pulled {
			f.phase = "initial_attack_written_waiting_for_attack_start"
		}
	}
	return true
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
	f := &s.fargodeep
	refuse := func() bool {
		f.active = false
		f.phase = "prewrite_observations_changed"
		s.state.Reason = "fargodeep_prewrite_observations_changed"
		return false
	}
	if ctx.Err() != nil || !f.active || !s.now().Before(f.until) || targetGUID != f.target || !s.state.Assisting || !s.now().Before(s.assistUntil) || s.state.InteractionOwner != "" || s.state.MotionUnconfirmed {
		return refuse()
	}
	before, revision, mapID := s.pos, s.positionRevision, s.posMap
	w, err := s.snapshot(ctx, f.target)
	if err != nil || !fargodeepCharacter(w, s.selfGUID) || !s.refreshPositionLocked(w) || s.positionRevision != revision || s.posMap != mapID || distance(before, s.pos) > .05 {
		return refuse()
	}
	active, incomplete := ownFargodeepQuests(w)
	c := practiceTargetObservation(w, f.target)
	p, known := fargodeepCreaturePosition(c)
	if !active || !f.pulled && f.engager == s.selfGUID && !incomplete || !eligibleFargodeepKobold(c, w.Self.Level) || !known || !fargodeepArea(p) || !fargodeepArea(s.pos) || s.failedAssistTarget == f.target {
		return refuse()
	}
	members, reason := s.fargodeepPartyLocked(ctx, w, f.session, false, p)
	if reason != "" || c.DynamicFlags&4 != 0 && c.DynamicFlags&8 == 0 {
		return refuse()
	}
	engaged := c.AttackVictimKnown && members[c.AttackVictimGuid] && c.AttackAgeMs <= 30000
	if engaged {
		victim := observedHealingPlayer(w, c.AttackVictimGuid)
		engaged = victim != nil && (victim.UnitFlags&0x80000 != 0 || c.AttackAgeMs <= 5000)
	}
	if !engaged {
		if f.engager != s.selfGUID || f.pulled || c.UnitFlags&0x80000 != 0 || c.DynamicFlags != 0 || c.AttackVictimKnown && c.AttackVictimGuid != 0 || !c.TargetKnown || c.TargetGuid != 0 {
			return refuse()
		}
		// Ground-route authority is needed for movement and melee, never for
		// an otherwise valid in-range unit-target spell cast from this pose.
		if (movement || w.Self.ClassId == 4) && (f.route == nil || f.routeRevision != s.positionRevision || distance(f.routeExpected, s.pos) > .05 || distance(f.routeTarget, p) > .25 || !movement && !directRemainingRoute(s.pos, p, f.route, f.routeIndex)) {
			return refuse()
		}
		if _, reason = s.fargodeepPartyLocked(ctx, w, f.session, true, p); reason != "" {
			return refuse()
		}
		if len(w.NearbyCreatures) > 32 {
			return refuse()
		}
		for _, other := range w.NearbyCreatures {
			if other != nil && other.Guid != f.target && other.Present && other.AliveKnown && other.Alive && other.NpcFlags == 0 {
				if fargodeepNeighborNear(other, p) {
					return refuse()
				}
			}
		}
	}
	if movement {
		// Only the designated body may walk its already verified current route.
		// This replaces the normal owner-presence gate, not the map/pose guard.
		if f.engager != s.selfGUID || f.route == nil || f.routeRevision != s.positionRevision || distance(f.routeExpected, s.pos) > .05 || distance(s.pos, p) > 35 || distance(f.routeTarget, p) > 1.5 {
			return refuse()
		}
		return true
	}
	self := w.Self
	if self.ClassId == 4 {
		if spell != 0 && (!spellFamily(spell, sinisterStrike) || !self.PowerKnown || self.PowerType != 3 || self.Power < s.state.SinisterStrikeEnergyCost) {
			return false
		}
		return s.state.KnownAutoAttack && s.weaponReady(self) && self.UnitFlags&(0x20000|0x200000) == 0 && self.BaseAttackTimeKnown && self.CombatReachKnown && c.CombatReachKnown && distance(s.pos, p) <= math.Max(5, float64(self.CombatReach+c.CombatReach)+4.0/3.0)-.5
	}
	rank := spellRankFor(spell)
	rangeLimit := float64(28)
	if self.ClassId == 8 {
		rangeLimit = 33
	}
	cost, reserve := manaBudget(self, uint64(rank.manaPercent)), uint64(0)
	if self.ClassId == 5 {
		if !s.state.Healing || !s.now().Before(s.healingUntil) || !s.state.KnownLesserHeal {
			return refuse()
		}
		reserve = manaBudget(self, uint64(s.selectedSpellLocked(lesserHeal).manaPercent))
	}
	if rank.id == 0 || self.UnitFlags&0x2000 != 0 || !self.PowerKnown || self.PowerType != 0 || cost == 0 || uint64(self.Power) < cost+reserve || distance(s.pos, p) > rangeLimit {
		return refuse()
	}
	return true
}
