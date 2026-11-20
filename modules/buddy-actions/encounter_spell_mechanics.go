package buddyactions

import (
	"slices"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

// Reflection copies existing catalog mechanics. Rank, rotation, conservation
// and cooldown-use choices belong to the reloadable Lua policy.
func encounterSpellMechanics(spell spellcatalog.Spell, self *worldv1.PlayerState) *actionsv1.EncounterSpell {
	view := &actionsv1.EncounterSpell{
		Known: true, MetadataKnown: true, FamilyId: spell.Family, Rank: spell.Rank,
		Level: spell.Level, Name: spell.Name, CastTimeMs: spell.CastMs,
		Cost: spellCost(spell, self), PowerType: spell.Power,
		MinRange: spell.MinHostile, MaxRange: spell.MaxHostile, DirectDamage: directDamage(spell),
		CooldownMs: spell.CooldownMs, CategoryCooldownMs: spell.CategoryCooldownMs, GlobalCooldownMs: spell.GcdMs,
		SelfCastSupported: supportsSelfCast(spell),
	}
	if spell.Capability().Target == "ally" || spell.Capability().Target == "self" {
		view.MinRange, view.MaxRange = spell.MinFriendly, spell.MaxFriendly
	}
	for _, effect := range spell.Effects {
		if effect.Kind == 6 && effect.Aura == 3 && effect.PeriodMs != 0 {
			view.PeriodicDamage = append(view.PeriodicDamage, &actionsv1.EncounterPeriodicDamage{
				DamagePerTick: float64(effect.Points), PeriodMs: effect.PeriodMs,
			})
		}
	}
	return view
}

func (s *Service) encounterRankResourcesAvailable(spell spellcatalog.Spell, self *worldv1.PlayerState) bool {
	cost := spellCost(spell, self)
	return !s.spellOnCooldownLocked(spell) && casterRequirementsObserved(spell, self) &&
		(cost == 0 || self.PowerKnown && int32(self.PowerType) == spell.Power && uint64(self.Power) >= cost)
}

func (s *Service) encounterSelfAura(family uint32) (known, active bool) {
	set := s.buffAuras[s.selfGUID]
	if set == nil {
		return false, false
	}
	for _, aura := range set.slots {
		if aura.spell == 0 || aura.durationKnown && aura.remaining(s.now()) == 0 {
			continue
		}
		if spell, ok := s.spellData.Lookup(aura.spell); ok && spell.Family == family {
			return true, true
		}
	}
	return set.complete, false
}

func (s *Service) encounterOwnDamageFamilies(target uint64) []uint32 {
	var families []uint32
	seen := map[uint32]bool{}
	for key, aura := range s.ownDamageAuras {
		if key.target != target || aura.durationKnown && aura.remaining(s.now()) == 0 {
			continue
		}
		if spell, known := s.learnedContractsLocked()[aura.spell]; known && !seen[spell.Family] {
			families = append(families, spell.Family)
			seen[spell.Family] = true
		}
	}
	slices.Sort(families)
	return families
}
