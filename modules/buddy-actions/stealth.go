package buddyactions

import (
	"math"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

func observedRunSpeed(p *worldv1.PlayerState) float64 {
	if p == nil || !p.SpeedsKnown {
		return 0
	}
	v := float64(p.RunSpeed)
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0.1 || v > 14 {
		return 0
	}
	return v
}
func (s *Service) stealthedLocked() bool {
	set := s.buffAuras[s.selfGUID]
	if set == nil {
		return false
	}
	for _, a := range set.slots {
		if a.spell == 0 || a.durationKnown && a.remaining(s.now()) == 0 {
			continue
		}
		r, ok := s.spellData.Lookup(a.spell)
		if ok && r.Capability().Kind == spellcatalog.Stealth {
			return true
		}
	}
	return false
}

// Stealth is an approach to an already engaged party enemy, never a new pull.
func behindEnemy(self, target position) bool {
	dx, dy := float64(self.x-target.x), float64(self.y-target.y)
	return dx*math.Cos(float64(target.o))+dy*math.Sin(float64(target.o)) < -0.25
}
