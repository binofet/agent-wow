package buddyactions

import (
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	contract "github.com/agent-wow/agent-wow/pkg/behaviorcontract"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"math"
	"sort"
	"time"
)

func (s *Service) learnedContractsLocked() map[uint32]spellcatalog.Spell {
	if s.spellData == nil {
		s.spellData = spellcatalog.Default()
	}
	if s.capabilityCatalog == s.spellData && s.capabilityRevision == s.spellbookRevision && s.capabilities != nil {
		return s.capabilities
	}
	s.capabilities = map[uint32]spellcatalog.Spell{}
	for id, known := range s.knownSpells {
		if known {
			if r, ok := s.spellData.Lookup(id); ok {
				s.capabilities[id] = r
			}
		}
	}
	s.capabilityRevision, s.capabilityCatalog = s.spellbookRevision, s.spellData
	return s.capabilities
}
func (s *Service) candidatesLocked(kind spellcatalog.Kind) []spellcatalog.Spell {
	var out []spellcatalog.Spell
	for _, r := range s.learnedContractsLocked() {
		if r.Capability().Kind == kind && s.casterRequirementsLocked(r) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Level != out[j].Level {
			return out[i].Level > out[j].Level
		}
		if out[i].Rank != out[j].Rank {
			return out[i].Rank > out[j].Rank
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (s *Service) casterRequirementsLocked(r spellcatalog.Spell) bool {
	return casterRequirementsObserved(r, s.lastWorld.GetSelf())
}

func casterRequirementsObserved(r spellcatalog.Spell, p *worldv1.PlayerState) bool {
	for _, test := range []struct {
		index   int
		present bool
	}{{0, true}, {2, false}} {
		id := r.Conditions[test.index]
		if id == 0 {
			continue
		}
		if p == nil || !p.AuraStateKnown || id > 32 || (p.AuraState&(1<<(id-1)) != 0) != test.present {
			return false
		}
	}
	if r.Stances != 0 || r.StancesNot != 0 {
		if p == nil || !p.ShapeshiftFormKnown || p.ShapeshiftForm > 32 {
			return false
		}
		bit := uint32(0)
		if p.ShapeshiftForm > 0 {
			bit = 1 << (p.ShapeshiftForm - 1)
		}
		if r.Stances != 0 && r.Stances&bit == 0 && !(p.ShapeshiftForm == 0 && r.Attrs[2]&0x80000 != 0) || r.StancesNot&bit != 0 {
			return false
		}
	}
	return true
}
func spellCost(r spellcatalog.Spell, p *worldv1.PlayerState) uint64 {
	if p == nil {
		return math.MaxUint64
	}
	return uint64(r.Cost) + uint64(r.CostPerLevel)*uint64(p.Level) + manaBudget(p, uint64(r.CostPercent))
}
func directHealing(spell spellcatalog.Spell) float64 {
	var total float64
	for _, effect := range spell.Effects {
		if effect.Kind == 10 {
			total += float64(effect.Points) + math.Max(0, float64(effect.Dice)-1)/2
		}
	}
	return total
}
func (s *Service) reserveSpellCooldownLocked(r spellcatalog.Spell) {
	if s.spellReady == nil {
		s.spellReady = map[uint32]time.Time{}
	}
	delay := r.CooldownMs
	if r.CategoryCooldownMs > delay {
		delay = r.CategoryCooldownMs
	}
	s.spellReady[r.ID] = s.now().Add(time.Duration(delay) * time.Millisecond)
}

func (s *Service) implicitSelfSpellLocked(id uint32, target uint64) bool {
	r, ok := s.learnedContractsLocked()[id]
	return ok && target == s.selfGUID && r.Capability().Target == "self"
}

func (s *Service) healingReserveLocked(p *worldv1.PlayerState) uint64 {
	reserve := uint64(0)
	for _, r := range s.candidatesLocked(spellcatalog.Heal) {
		if r.Power == 0 {
			cost := spellCost(r, p)
			if cost > reserve {
				reserve = cost
			}
		}
	}
	return reserve
}
func directDamage(r spellcatalog.Spell) float64 {
	var damage float64
	for _, e := range r.Effects {
		if e.Kind == 2 {
			damage += float64(e.Points) + math.Max(0, float64(e.Dice)-1)/2
		}
	}
	return damage
}

func (s *Service) bestDamageLocked(p *worldv1.PlayerState, target position, finishHealth uint32, targetGUID uint64) spellcatalog.Spell {
	var best spellcatalog.Spell
	var choices []contract.Spell
	eligible := map[uint32]spellcatalog.Spell{}
	score := float64(-1)
	targetRange := distance(s.pos, target)
	interruptedHere := targetGUID != 0 && s.state.LastCastTargetGuid == targetGUID &&
		(s.state.LastCastFailure == spellFailedInterrupted || s.state.LastCastFailure == spellFailedInterruptedCombat)
	for _, r := range s.candidatesLocked(spellcatalog.Damage) {
		if r.Power != 0 || !p.PowerKnown || p.PowerType != 0 || uint64(p.Power) < spellCost(r, p)+s.healingReserveLocked(p) ||
			s.now().Before(s.spellReady[r.ID]) || targetRange > r.MaxHostile-2 || targetRange < r.MinHostile {
			continue
		}
		damage := directDamage(r)
		periodic := periodicDamage(r)
		if periodic > 0 && targetGUID != 0 && s.hasOwnDamageAuraLocked(targetGUID, r.Family) {
			continue
		}
		if periodic > 0 && (targetGUID == 0 || finishHealth != 0) && damage == 0 {
			continue // A late or position-only cast needs immediate damage.
		}
		amount := damage
		if finishHealth == 0 && targetGUID != 0 {
			amount += periodic
		}
		value := amount / math.Max(1.5, float64(r.CastMs)/1000)
		if interruptedHere && r.CastMs != 0 {
			value *= 0.5 // A long cast just failed on this target.
		}
		// Prefer the fastest affordable spell expected to finish this enemy.
		// Otherwise keep the usual damage-per-second policy.
		kills := finishHealth > 0 && damage >= float64(finishHealth)
		bestKills := finishHealth > 0 && directDamage(best) >= float64(finishHealth)
		if kills && !bestKills || kills && bestKills && (r.CastMs < best.CastMs || r.CastMs == best.CastMs && value > score) ||
			!kills && !bestKills && value > score {
			best, score = r, value
		}
		eligible[r.ID] = r
		choices = append(choices, contract.Spell{SpellId: r.ID, Cost: uint32(spellCost(r, p)), Amount: amount, CastMs: r.CastMs, Score: value})
	}
	if len(choices) > 0 && finishHealth == 0 && (s.behavior != nil || s.dungeonTrace != nil && s.dungeonTrace.Active()) {
		decision := s.behaviorDecisionLocked(contract.Input{Kind: "damage_spell", Self: combatant(p), Spells: choices})
		if decision.SpellId != 0 {
			if r, ok := eligible[decision.SpellId]; ok {
				return r
			}
			s.behaviorRejection = "unknown_or_ineligible_damage_spell_using_native_decision"
		}
	}
	return best
}
