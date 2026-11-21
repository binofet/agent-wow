package buddyactions

import (
	"context"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

func (s *Service) starterNamedOffenseAvailableLocked(w *worldv1.SnapshotResponse, target *worldv1.CreatureState) bool {
	return s.starterSpells.uncertainStrike == nil && !s.closed && !s.clientControlLost && !s.clientControlRestoring &&
		safeSelfCombat(w, s.selfGUID) == "" && !s.state.CastPending && !s.state.Moving && !s.state.Traveling &&
		!s.state.MotionUnconfirmed && !s.autoStopping && !s.state.AutoAttackUnconfirmed && s.state.InteractionOwner == "" &&
		!s.recovery.state.Active && !s.now().Before(s.nextAssist) && !s.now().Before(s.nextHeal) &&
		s.failedAssistTarget != target.GetGuid() && !s.targetProtection.blocks(target.GetGuid(), s.now()) &&
		exactOffenseTarget(w, target, target.GetGuid(), nil)
}

func (s *Service) practiceCasterMechanicsLocked(w *worldv1.SnapshotResponse, target *worldv1.CreatureState, point position, rank spellcatalog.Spell) bool {
	self := w.GetSelf()
	return s.starterNamedOffenseAvailableLocked(w, target) && (self.ClassId == 5 || self.ClassId == 8) &&
		observedOffenseCaster(self) && rank.Capability().Kind == spellcatalog.Damage &&
		s.offenseRankReadyLocked(self, rank) && offenseLineInRange(s.pos, point, rank)
}

func (s *Service) practiceCasterWriteAllowedLocked(ctx context.Context, targetGUID uint64, spell uint32) bool {
	w, target, point, valid := s.practiceOffenseAuthorityLocked(ctx, targetGUID)
	if !valid {
		return false
	}
	selected := s.starterSpellDecisionLocked(ctx, w, target, point)
	if !selected.valid || selected.kind != "cast" || selected.spell.ID != spell {
		return false
	}
	w, target, point, valid = s.practiceOffenseAuthorityLocked(ctx, targetGUID)
	return valid && s.starterSpellFactsMatchLocked(selected, w, target, point) &&
		s.practiceCasterMechanicsLocked(w, target, point, selected.spell)
}

// Northshire's already selected wolf and verified approach remain mission
// authority. The existing shared Lua policy supplies the only spell choice.
func (s *Service) starterPracticeCasterLocked(ctx context.Context, w *worldv1.SnapshotResponse, target *worldv1.CreatureState, point position) bool {
	if s.blockProtectedOffenseLocked(ctx, target.Guid) {
		return false
	}
	selected := s.starterSpellDecisionLocked(ctx, w, target, point)
	if !selected.valid {
		s.state.Reason = s.starterSpells.failure()
		return false
	}
	if selected.kind != "cast" {
		s.state.Reason = "practice_lua_did_not_select_damage_spell"
		return false
	}
	if !s.stopAutoAttackLocked(ctx) || !s.stopLocked(ctx) || !s.faceEnemyLocked(ctx, point) {
		return false
	}
	if !s.practiceCasterWriteAllowedLocked(ctx, target.Guid, selected.spell.ID) {
		s.state.Reason = "practice_caster_prewrite_observations_changed"
		return false
	}
	return s.writeStarterNamedOffenseLocked(ctx, selected.spell, target.Guid)
}
