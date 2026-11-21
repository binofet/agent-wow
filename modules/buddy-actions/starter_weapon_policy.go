package buddyactions

import (
	"context"
	"math"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

const starterWeaponSelection = "weapon_strike"

// These are checks on one exact selected rank, never an ability comparison.
// Named tasks retain their own authority instead of borrowing ordinary combat's
// custody (which deliberately excludes both Practice and Fargodeep).
func (s *Service) starterWeaponMechanicsLocked(w *worldv1.SnapshotResponse, target *worldv1.CreatureState, point position, rank spellcatalog.Spell) bool {
	self := w.GetSelf()
	return s.starterNamedOffenseAvailableLocked(w, target) && self.ClassId == 4 && observedWeaponCaster(self) &&
		instantWeaponStrike(rank) && s.offenseRankReadyLocked(self, rank) && s.strikeTargetReady(target.GetGuid(), rank) &&
		offenseLineInRange(s.pos, point, rank) && (!rank.Behind || behindEnemy(s.pos, point))
}

// Practice has no party prerequisite. Recheck its existing solo mission
// authority after policy/STOP/facing callbacks, including the witnessed fight
// that it is already finishing after its quest or level goal changes.
func (s *Service) practiceWeaponAuthorityLocked(ctx context.Context, targetGUID uint64) (*worldv1.SnapshotResponse, *worldv1.CreatureState, position, bool) {
	w, target, point, valid := s.practiceOffenseAuthorityLocked(ctx, targetGUID)
	return w, target, point, valid && w.Self.ClassId == 4
}

func (s *Service) practiceOffenseAuthorityLocked(ctx context.Context, targetGUID uint64) (*worldv1.SnapshotResponse, *worldv1.CreatureState, position, bool) {
	until := s.practiceUntil
	current := func() bool {
		return ctx.Err() == nil && s.state.Practicing && s.practicePullStarted && targetGUID == s.practiceTarget &&
			s.practiceUntil == until && s.now().Before(until) && !s.closed && !s.clientControlLost && !s.clientControlRestoring &&
			s.state.InteractionOwner == "" && !s.state.MotionUnconfirmed && !s.recovery.state.Active
	}
	return s.practiceTaskObservationsLocked(ctx, targetGUID, current)
}

func (s *Service) practiceTaskObservationsLocked(ctx context.Context, targetGUID uint64, current func() bool) (*worldv1.SnapshotResponse, *worldv1.CreatureState, position, bool) {
	if !current() {
		return nil, nil, position{}, false
	}
	before, revision, mapID := s.pos, s.positionRevision, s.posMap
	w, err := s.snapshot(ctx, targetGUID)
	if err != nil || !current() || safePracticeCharacter(w, s.selfGUID) != "" || !s.refreshPositionLocked(w) ||
		s.positionRevision != revision || s.posMap != mapID || distance(before, s.pos) > .05 {
		return nil, nil, position{}, false
	}
	self, target := w.Self, practiceTargetObservation(w, targetGUID)
	point, known, _ := creaturePosition(target)
	if !self.ClassKnown || (self.ClassId != 4 && self.ClassId != 5 && self.ClassId != 8) || !self.LevelKnown || !self.HealthKnown || !self.PowerKnown ||
		!northshire(playerPosition(self)) || !northshire(s.pos) || !known || !northshire(point) ||
		!self.SpeedsKnown || math.Abs(float64(self.RunSpeed)-7) > .01 ||
		!eligiblePracticeWolf(target) || !exactOffenseTarget(w, target, targetGUID, nil) || len(w.NearbyCreatures) > 32 ||
		target.AttackVictimKnown && target.AttackVictimGuid != s.selfGUID || target.DynamicFlags&4 != 0 && target.DynamicFlags&8 == 0 ||
		s.failedAssistTarget == targetGUID {
		return nil, nil, position{}, false
	}
	if self.ClassId == 4 {
		if self.PowerType != 3 || !self.BaseAttackTimeKnown || !self.CombatReachKnown || !target.CombatReachKnown ||
			!s.state.KnownAutoAttack || !s.state.KnownSinisterStrike || !s.weaponReady(self) || self.UnitFlags&(0x20000|0x200000) != 0 {
			return nil, nil, position{}, false
		}
	} else if self.PowerType != 0 || self.UnitFlags&0x2000 != 0 || self.ClassId == 5 && !s.state.KnownSmite || self.ClassId == 8 && !s.state.KnownFireball {
		return nil, nil, position{}, false
	}
	active, incomplete := ownWolfQuest(w)
	goal := incomplete && self.Level >= 1 && self.Level <= 5 || !active && self.Level == 1 && self.ClassId != 5 && !s.practiceQuestGoal
	finishing := s.practicePullStarted && self.UnitFlags&0x80000 != 0 && target.AttackVictimKnown && target.AttackVictimGuid == s.selfGUID && target.AttackAgeMs <= 30000
	if !goal && !finishing {
		return nil, nil, position{}, false
	}
	for _, other := range w.NearbyCreatures {
		if other != nil && other.Entry != 299 && other.Present && other.AliveKnown && other.Alive &&
			other.AttackVictimKnown && other.AttackVictimGuid == s.selfGUID && other.AttackAgeMs <= 30000 {
			return nil, nil, position{}, false
		}
	}
	return w, target, point, current()
}

func (s *Service) starterWeaponWriteAllowedLocked(ctx context.Context, targetGUID uint64, spell uint32) bool {
	if s.fargodeep.active {
		return s.fargodeepWriteAllowedLocked(ctx, targetGUID, spell)
	}
	w, target, point, valid := s.practiceWeaponAuthorityLocked(ctx, targetGUID)
	if !valid {
		return false
	}
	selected := s.starterWeaponDecisionLocked(ctx, w, target, point)
	if !selected.valid || selected.kind != "cast" || selected.spell.ID != spell {
		return false
	}
	w, target, point, valid = s.practiceWeaponAuthorityLocked(ctx, targetGUID)
	return valid && s.starterSpellFactsMatchLocked(selected, w, target, point) &&
		s.starterWeaponMechanicsLocked(w, target, point, selected.spell)
}

func (s *Service) castStarterWeaponLocked(ctx context.Context, rank spellcatalog.Spell, target uint64) bool {
	if !s.starterWeaponWriteAllowedLocked(ctx, target, rank.ID) {
		s.state.Reason = "starter_strike_prewrite_observations_changed"
		return false
	}
	return s.writeStarterNamedOffenseLocked(ctx, rank, target)
}

// Execute one exact rank after the caller's final policy and native admission.
// Both named attack paths borrow this same bounded receipt and uncertainty hold.
func (s *Service) writeStarterNamedOffenseLocked(ctx context.Context, rank spellcatalog.Spell, target uint64) bool {
	if ctx.Err() != nil || s.starterSpells.uncertainStrike != nil || s.targetProtection.blocks(target, s.now()) {
		return false
	}
	now := s.now()
	s.castCount++
	s.reserveSpellCooldownLocked(rank)
	if rank.Category != 0 && rank.CategoryCooldownMs != 0 {
		if s.categoryReady == nil {
			s.categoryReady = make(map[uint32]time.Time)
		}
		s.categoryReady[rank.Category] = now.Add(time.Duration(rank.CategoryCooldownMs) * time.Millisecond)
	}
	s.nextAssist = now.Add(time.Duration(rank.GcdMs) * time.Millisecond)
	s.nextHeal = s.nextAssist
	s.castUntil = now.Add(max(5*time.Second, time.Duration(rank.CastMs)*time.Millisecond+1500*time.Millisecond))
	s.pendingSpell, s.pendingTarget = rank.ID, target
	s.protectedCastCancelAttempted = false
	s.state.CastPending = true
	s.state.AssistAttempts++
	s.state.LastCastFailure = 0
	s.state.LastCastSpellId, s.state.LastCastTargetGuid = rank.ID, target
	op := s.reserveExplicitCastLocked("", rank.ID, target, offenseCapability, directOffenseLog(rank))
	op.owner = target // Bind terminal uncertainty to this named task's target.
	if s.sendLocked(ctx, opcode.CMSGCastSpell, spellPacket(s.castCount, rank.ID, target)) {
		op.view.TransportWritten = true
		s.state.Reason = "starter_strike_transport_written_waiting_for_server"
	} else {
		op.view.Phase = "transport_unconfirmed_no_retry"
		s.starterSpells.uncertainStrike = op
		s.failedAssistTarget, s.failedAssistCastTarget = target, target
		s.state.Reason = "starter_strike_transport_unconfirmed_no_retry"
	}
	return true
}
