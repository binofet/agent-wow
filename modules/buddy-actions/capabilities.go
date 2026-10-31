package buddyactions

import (
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
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
	p := s.lastWorld.GetSelf()
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
func (s *Service) bestHealLocked(p, target *worldv1.PlayerState) spellcatalog.Spell {
	var best spellcatalog.Spell
	score := float64(-1)
	for _, r := range s.candidatesLocked(spellcatalog.Heal) {
		if r.Power != 0 || !p.PowerKnown || p.PowerType != 0 || uint64(p.Power) < spellCost(r, p) {
			continue
		}
		if s.now().Before(s.spellReady[r.ID]) {
			continue
		}
		if target.Guid != s.selfGUID && (r.Capability().Target == "self" || distance(s.pos, playerPosition(target)) > r.MaxFriendly || distance(s.pos, playerPosition(target)) < r.MinFriendly) {
			continue
		}
		amount := float64(0)
		for _, e := range r.Effects {
			if e.Kind == 10 {
				amount += float64(e.Points) + math.Max(0, float64(e.Dice)-1)/2
			}
		}
		if amount > score || amount == score && (best.ID == 0 || r.Rank > best.Rank) {
			best, score = r, amount
		}
	}
	return best
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
func (s *Service) bestDamageLocked(p *worldv1.PlayerState, target position) spellcatalog.Spell {
	var best spellcatalog.Spell
	score := float64(-1)
	for _, r := range s.candidatesLocked(spellcatalog.Damage) {
		if r.Power != 0 || !p.PowerKnown || p.PowerType != 0 || uint64(p.Power) < spellCost(r, p)+s.healingReserveLocked(p) || s.now().Before(s.spellReady[r.ID]) || distance(s.pos, target) > r.MaxHostile-2 || distance(s.pos, target) < r.MinHostile {
			continue
		}
		damage := float64(0)
		for _, e := range r.Effects {
			if e.Kind == 2 {
				damage += float64(e.Points) + math.Max(0, float64(e.Dice)-1)/2
			}
		}
		value := damage / math.Max(1.5, float64(r.CastMs)/1000)
		if value > score {
			best, score = r, value
		}
	}
	return best
}
