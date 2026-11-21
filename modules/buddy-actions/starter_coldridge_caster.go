package buddyactions

import (
	"context"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

// The bound Coldridge mission supplies target and pull authority. The same
// shared Lua selector used by the other named missions chooses the exact rank.
func (s *Service) starterColdridgeCasterLocked(ctx context.Context, original *worldv1.SnapshotResponse, target *worldv1.CreatureState, aimed position) bool {
	session, self, until, quest, guid := s.sessionID, s.selfGUID, s.practiceUntil, s.coldridgeGoal.questID, target.Guid
	pulled := s.practicePullStarted
	current := func() bool {
		return ctx.Err() == nil && s.sessionID == session && s.selfGUID == self && !s.closed &&
			!s.clientControlLost && !s.clientControlRestoring && s.state.Practicing &&
			s.practiceUntil == until && s.now().Before(until) && s.coldridgeGoal.questID == quest &&
			s.practiceTarget == guid && s.state.PracticeTargetGuid == guid && s.practicePullStarted == pulled &&
			!s.fargodeep.active && !s.questApproaching && !s.recovery.state.Active && s.state.InteractionOwner == ""
	}
	if !current() || s.blockProtectedOffenseLocked(ctx, guid) || !s.starterNamedOffenseAvailableLocked(original, target) {
		return false
	}
	selected := s.starterSpellDecisionLocked(ctx, original, target, aimed)
	if !current() {
		return false
	}
	if !selected.valid {
		s.state.Reason = s.starterSpells.failure()
		return false
	}
	if selected.kind != "cast" {
		s.state.Reason = "coldridge_lua_did_not_select_damage_spell"
		return false
	}
	if !s.stopAutoAttackLocked(ctx) || !current() {
		return false
	}
	if !s.stopLocked(ctx) || !current() {
		return false
	}
	if !s.faceEnemyLocked(ctx, aimed) || !current() {
		return false
	}
	rank, valid := s.coldridgeCasterWriteAllowedLocked(ctx, original, aimed, selected.spell.ID, current)
	if !valid {
		if current() {
			s.state.Reason = "coldridge_caster_prewrite_observations_changed"
		}
		return false
	}
	if !s.writeStarterNamedOffenseLocked(ctx, rank, guid) {
		return false
	}
	if !current() {
		return false
	}
	// An admitted attempt, including an uncertain transport outcome, owns the
	// first pull. A wait or rejected write cannot grant finishing authority;
	// nor can the write restore a task replaced during its callback.
	if !s.practicePullStarted {
		s.practicePullStarted, s.practiceRoute = true, nil
		s.state.PracticePulls++
	}
	return true
}

func (s *Service) coldridgeCasterWriteAllowedLocked(ctx context.Context, original *worldv1.SnapshotResponse, aimed position, spell uint32, current func() bool) (spellcatalog.Spell, bool) {
	w, target, point, valid := s.coldridgeCastObservationsLocked(ctx, original, aimed, current)
	if !valid || !current() {
		return spellcatalog.Spell{}, false
	}
	selected := s.starterSpellDecisionLocked(ctx, w, target, point)
	if !current() || !selected.valid || selected.kind != "cast" || selected.spell.ID != spell {
		return spellcatalog.Spell{}, false
	}
	w, target, point, valid = s.coldridgeCastObservationsLocked(ctx, original, aimed, current)
	if !valid || !current() || !s.starterSpellFactsMatchLocked(selected, w, target, point) ||
		!s.starterNamedOffenseAvailableLocked(w, target) || w.Self.ClassId != 9 || !observedOffenseCaster(w.Self) ||
		selected.spell.Capability().Kind != spellcatalog.Damage || !s.offenseRankReadyLocked(w.Self, selected.spell) ||
		!offenseLineInRange(s.pos, point, selected.spell) {
		return spellcatalog.Spell{}, false
	}
	return selected.spell, true
}
