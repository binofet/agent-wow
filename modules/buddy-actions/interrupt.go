package buddyactions

import (
	"math"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

type enemyCastIdentity struct {
	guid     uint64
	revision uint64
}

func (s *Service) interruptAvailableLocked(r spellcatalog.Spell, self *worldv1.PlayerState, enemy *worldv1.CreatureState) bool {
	p, known, _ := creaturePosition(enemy)
	if !known || !r.RangeKnown || r.MaxHostile <= 0 || !self.PowerKnown || int32(self.PowerType) != r.Power ||
		r.CastMs != 0 || s.now().Before(s.spellReady[r.ID]) ||
		r.GcdMs > 0 && (s.now().Before(s.nextAssist) || s.now().Before(s.nextHeal)) ||
		uint64(self.Power) < spellCost(r, self) || !s.casterRequirementsLocked(r) {
		return false
	}
	reach := r.MaxHostile
	if self.CombatReachKnown && enemy.CombatReachKnown {
		reach = math.Max(reach, float64(self.CombatReach+enemy.CombatReach)+4.0/3.0)
	}
	dist := distance(s.pos, p)
	return dist >= r.MinHostile && dist <= reach
}
