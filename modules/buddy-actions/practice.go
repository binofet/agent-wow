package buddyactions

import (
	"context"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func safePracticeCharacter(w *worldv1.SnapshotResponse, selfGUID uint64) string {
	if w == nil || w.Self == nil {
		return "character_unknown"
	}
	// An active practice fight may set IN_COMBAT. All other own safety
	// conditions are identical to local quest travel and still fail closed.
	if !w.Self.MapKnown || w.Self.MapId != 0 {
		return "practice_map_zero_required"
	}
	copy := proto.Clone(w.Self).(*worldv1.PlayerState)
	copy.UnitFlags &^= 0x80000
	return safeQuestCharacter(&worldv1.SnapshotResponse{Self: copy}, selfGUID)
}

func (s *Service) stopPracticeLocked(ctx context.Context, why string) {
	s.state.Practicing = false
	s.state.PracticePhase = why
	s.state.Reason = "practice_" + why
	s.practiceRoute = nil
	s.practiceTarget = 0
	s.state.PracticeTargetGuid = 0
	s.practicePullStarted = false
	s.stopLocked(ctx)
	s.stopAutoAttackLocked(ctx)
	s.lastStep = s.now()
}

func (s *Service) Practice(ctx context.Context, r *actionsv1.ToggleRequest) (*actionsv1.State, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "practice toggle is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, status.Error(codes.FailedPrecondition, "session is closing")
	}
	if s.hostEncounterKey != "" {
		return s.inactiveNativeEncounterLocked(r.Enabled)
	}
	if !r.Enabled {
		s.autoStopping, s.state.AutoAttackUnconfirmed = false, false
		s.stopPracticeLocked(ctx, "disabled")
		return s.stateLocked(), nil
	}
	if s.fargodeep.active || s.state.InteractionOwner != "" || s.questApproaching || s.state.CastPending || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed {
		return nil, status.Error(codes.FailedPrecondition, "practice requires confirmed idle action ownership")
	}
	w, err := s.snapshot(ctx)
	if err == nil && w != nil && w.Self != nil && w.Self.ClassKnown && w.Self.ClassId == 9 {
		return s.startColdridgePracticeLocked(ctx, w)
	}
	if err != nil || safeQuestCharacter(w, s.selfGUID) != "" || !w.Self.MapKnown || w.Self.MapId != 0 || !northshire(playerPosition(w.Self)) || !s.refreshPositionLocked(w) {
		return nil, status.Error(codes.FailedPrecondition, "practice requires a safely observed Northshire body")
	}
	selected, _, valid := s.practiceLifecycleDecisionLocked(ctx, w, "start")
	if !valid || selected.kind != "start" {
		return nil, status.Error(codes.FailedPrecondition, "current Lua practice lifecycle did not authorize this goal")
	}
	if !s.stopForPracticeLifecycleLocked(ctx, &selected) {
		return nil, status.Error(codes.FailedPrecondition, "practice stop is unconfirmed")
	}
	fresh, err := s.snapshot(ctx)
	if err != nil || !s.practiceLifecycleFactsMatchLocked(ctx, selected, fresh) {
		return nil, status.Error(codes.FailedPrecondition, "practice body, intent or policy changed before goal admission")
	}
	now := s.now()
	if !selected.binding.current(s, ctx, fresh) || !s.practiceLifecycleSourceCurrentLocked(selected.revision) {
		return nil, status.Error(codes.FailedPrecondition, "practice body, intent or policy changed before goal admission")
	}
	s.state.Practicing, s.state.PracticePhase = true, selected.phase
	s.practiceUntil = now.Add(time.Duration(selected.leaseMs) * time.Millisecond)
	s.practiceQuestGoal = selected.questGoal
	s.practiceTarget, s.state.PracticeTargetGuid = 0, 0
	s.practiceRoute = nil
	s.practicePullStarted = false
	s.failedAssistTarget = 0
	s.lastStep = now
	return s.stateLocked(), nil
}

// The accepted Lua owns scenario identity/level; this is packet eligibility.
func practiceCreatureAvailable(c *worldv1.CreatureState, mapID uint32) bool {
	return c != nil && c.Guid>>48 == 0xf130 && c.Present && c.MapKnown && c.MapId == mapID && c.LevelKnown && c.HealthKnown && c.MaxHealth > 0 && c.Health > 0 && c.AliveKnown && c.Alive && c.NpcFlags == 0 && c.UnitFlags&(0x2|0x80|0x100|0x10000|0x2000000|0x80000000) == 0 && c.UnitFlags_2&1 == 0
}

// A previously selected target may bypass discovery truncation, using the same
// cached server observation. Missing observations never authorize a new pull.
func practiceTargetObservation(w *worldv1.SnapshotResponse, guid uint64) *worldv1.CreatureState {
	if guid == 0 || w == nil || w.Self == nil || !w.Self.MapKnown {
		return nil
	}
	target := w.RequestedCreature
	if target == nil {
		// An older world module can still expose the target in its normal list.
		if len(w.NearbyCreatures) > 32 {
			return nil
		}
		for _, c := range w.NearbyCreatures {
			if c != nil && c.Guid == guid {
				target = c
				break
			}
		}
	}
	if target == nil || target.Guid != guid || !target.Present || !target.MapKnown || target.MapId != w.Self.MapId {
		return nil
	}
	return target
}

func (s *Service) practiceTickLocked(ctx context.Context) {
	// This is the caller's finite native execution lease. Lua supplied its
	// duration and still owns goal completion, readiness and target choice.
	if !s.now().Before(s.practiceUntil) {
		s.stopPracticeLocked(ctx, "goal_expired")
		return
	}
	w, err := s.snapshot(ctx, s.practiceTarget)
	if err == nil && w != nil && w.Self != nil && w.Self.ClassKnown && w.Self.ClassId == 9 {
		s.coldridgePracticeTickLocked(ctx, w)
		return
	}
	if err != nil || safePracticeCharacter(w, s.selfGUID) != "" || !w.Self.LevelKnown || !w.Self.ClassKnown ||
		(w.Self.ClassId != 4 && w.Self.ClassId != 5 && w.Self.ClassId != 8) || !w.Self.HealthKnown || !w.Self.PowerKnown ||
		!northshire(playerPosition(w.Self)) || !w.Self.SpeedsKnown || math.Abs(float64(w.Self.RunSpeed)-7) > .01 ||
		!s.refreshPositionLocked(w) || !northshire(s.pos) || s.state.MotionUnconfirmed {
		s.stopPracticeLocked(ctx, "own_state_unknown_or_unsafe")
		return
	}
	self := w.Self
	if s.state.AutoAttackUnconfirmed {
		s.stopLocked(ctx)
		s.state.PracticePhase = "waiting_for_server_attack_stop"
		return
	}
	// Exact supported spell/resource/weapon mechanics remain native. No health
	// percentage, mana reserve, goal choice or candidate ordering lives here.
	if self.ClassId == 8 && !s.state.KnownFireball || self.ClassId == 5 && !s.state.KnownSmite || self.ClassId == 4 && (!s.state.KnownAutoAttack || !s.state.KnownSinisterStrike) {
		s.stopPracticeLocked(ctx, "required_spell_unknown")
		return
	}
	if self.ClassId != 4 && self.PowerType != 0 || self.ClassId == 4 && self.PowerType != 3 {
		s.stopPracticeLocked(ctx, "class_resource_type_unknown")
		return
	}
	if self.ClassId != 4 && self.UnitFlags&0x2000 != 0 || self.ClassId == 4 && (!self.CombatReachKnown || !self.BaseAttackTimeKnown || self.UnitFlags&(0x20000|0x200000) != 0) {
		s.stopPracticeLocked(ctx, "class_combat_state_unknown_or_unsafe")
		return
	}
	if self.ClassId == 4 && !s.weaponReady(self) {
		s.stopLocked(ctx)
		s.stopAutoAttackLocked(ctx)
		s.requestWeaponLocked(ctx, self)
		s.state.PracticePhase = "verifying_equipped_weapon"
		return
	}
	if len(w.NearbyCreatures) > 32 {
		s.stopPracticeLocked(ctx, "creature_observations_invalid")
		return
	}
	original := s.practiceLifecycleBindingLocked(w)
	selected, fresh, valid := s.practiceLifecycleDecisionLocked(ctx, w, "tick")
	if !valid {
		// A missing/broken current source never chooses a native fallback or
		// discards the selected target's uncertain cast/attack receipt.
		s.pausePracticeLifecycleLocked(ctx, original)
		return
	}
	w, self = fresh, fresh.Self
	switch selected.kind {
	case "stop":
		s.stopPracticeLocked(ctx, selected.phase)
		return
	case "wait":
		if s.stopForPracticeLifecycleLocked(ctx, &selected) {
			s.state.PracticePhase = selected.phase
		}
		return
	case "target_dead":
		target := practiceTargetObservation(w, selected.target)
		if target == nil || !target.HealthKnown || target.Health != 0 || !target.AliveKnown || target.Alive {
			return
		}
		if !s.stopForPracticeLifecycleLocked(ctx, &selected) {
			return
		}
		s.practiceTarget, s.state.PracticeTargetGuid = 0, 0
		s.practiceRoute, s.practicePullStarted = nil, false
		s.practiceQuestGoal, s.state.PracticePhase = selected.questGoal, selected.phase
		return
	case "select":
		if s.practiceTarget != 0 || self.UnitFlags&0x80000 != 0 || !s.stopForPracticeLifecycleLocked(ctx, &selected) {
			return
		}
		// STOP callbacks cannot silently replace the candidate frame or policy.
		fresh, err = s.snapshot(ctx)
		if err != nil || !s.practiceLifecycleFactsMatchLocked(ctx, selected, fresh) {
			return
		}
		var candidate *worldv1.CreatureState
		for _, c := range fresh.NearbyCreatures {
			if c != nil && c.Guid == selected.target {
				if candidate != nil {
					return
				}
				candidate = c
			}
		}
		point, known, _ := creaturePosition(candidate)
		if !practiceCreatureAvailable(candidate, fresh.Self.MapId) || candidate.UnitFlags&0x80000 != 0 || candidate.DynamicFlags != 0 ||
			candidate.AttackVictimKnown || candidate.TargetKnown && candidate.TargetGuid != 0 || !known || !northshire(point) {
			return
		}
		s.practiceTarget, s.state.PracticeTargetGuid = candidate.Guid, candidate.Guid
		s.practiceRoute, s.practicePullStarted, s.practiceReplans = nil, false, 0
		s.practiceKillCredited = false
		w, self = fresh, fresh.Self
	case "continue":
	default:
		return
	}
	s.practiceQuestGoal = selected.questGoal
	target := practiceTargetObservation(w, s.practiceTarget)
	if selected.finishing {
		s.state.PracticePhase = selected.phase
		session, self, until, targetGUID, visit := s.sessionID, s.selfGUID, s.practiceUntil, s.practiceTarget, s.encounterVisit
		defer func() {
			if s.state.Practicing && s.sessionID == session && s.selfGUID == self && s.practiceUntil == until && s.practiceTarget == targetGUID && s.encounterVisit == visit {
				s.state.PracticePhase = selected.phase
			}
		}()
	}
	// A newly selected row is still in the unranked actual discovery frame.
	if target == nil && selected.kind == "select" {
		for _, c := range w.NearbyCreatures {
			if c != nil && c.Guid == s.practiceTarget {
				target = c
				break
			}
		}
	}
	if !practiceCreatureAvailable(target, w.Self.MapId) || target.AttackVictimKnown && target.AttackVictimGuid != s.selfGUID || target.DynamicFlags&4 != 0 && target.DynamicFlags&8 == 0 || s.failedAssistTarget == s.practiceTarget {
		s.stopPracticeLocked(ctx, "target_unknown_claimed_or_cast_failed")
		return
	}
	if self.ClassId == 4 && !target.CombatReachKnown {
		s.stopPracticeLocked(ctx, "target_melee_range_unknown")
		return
	}
	if self.UnitFlags&0x80000 != 0 && !s.practicePullStarted {
		s.stopPracticeLocked(ctx, "unexpected_combat_before_pull")
		return
	}
	targetPosition, known, _ := creaturePosition(target)
	if !known || !northshire(targetPosition) {
		s.stopLocked(ctx)
		s.state.PracticePhase = "waiting_for_observed_wolf_position"
		return
	}
	if s.state.CastPending {
		s.stopLocked(ctx)
		if !s.now().Before(s.castUntil) {
			s.stopPracticeLocked(ctx, "cast_timeout_no_retry")
		}
		return
	}
	var arrived bool
	w, target, targetPosition, arrived = s.practiceApproachLocked(ctx, w, target, targetPosition)
	if !arrived {
		return
	}
	self = w.Self
	if !s.practicePullStarted {
		pull, current, authorized := s.practiceLifecycleDecisionLocked(ctx, w, "pull")
		if !authorized || pull.kind != "pull" || pull.target != target.Guid {
			if authorized && pull.kind == "stop" {
				s.stopPracticeLocked(ctx, pull.phase)
			} else {
				s.stopLocked(ctx)
				s.state.PracticePhase = "waiting_for_current_pull_readiness"
			}
			return
		}
		w, self = current, current.Self
		target = practiceTargetObservation(w, pull.target)
		if !practiceCreatureAvailable(target, w.Self.MapId) {
			return
		}
		targetPosition, known, _ = creaturePosition(target)
		if !known || !northshire(targetPosition) {
			return
		}
		s.practiceQuestGoal = pull.questGoal
		selected.phase, selected.finishing = pull.phase, pull.finishing
		s.practicePullStarted, s.practiceRoute = true, nil
		s.state.PracticePulls++
	}
	if self.ClassId == 4 {
		s.starterRogueAssistLocked(ctx, w, target, targetPosition)
	} else {
		s.starterPracticeCasterLocked(ctx, w, target, targetPosition)
	}
	s.state.PracticePhase = selected.phase
}
