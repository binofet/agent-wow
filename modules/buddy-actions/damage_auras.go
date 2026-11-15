package buddyactions

import (
	"time"

	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

type damageAuraSlot struct {
	target uint64
	slot   uint8
}

// Estimate several early ticks so damage-over-time spells can compete on
// enemies that will survive the application. Finish decisions use direct damage.
func periodicDamage(spell spellcatalog.Spell) float64 {
	const evaluationWindow = 8 * time.Second
	var damage float64
	for _, effect := range spell.Effects {
		if effect.Kind == 6 && effect.Aura == 3 && effect.PeriodMs != 0 && effect.Points > 0 {
			ticks := evaluationWindow / (time.Duration(effect.PeriodMs) * time.Millisecond)
			damage += float64(max(1, ticks)) * float64(effect.Points)
		}
	}
	return damage
}

func (s *Service) clearOwnDamageAurasLocked(guid uint64) {
	if guid == s.selfGUID {
		clear(s.ownDamageAuras)
		return
	}
	for key := range s.ownDamageAuras {
		if key.target == guid {
			delete(s.ownDamageAuras, key)
		}
	}
}

func (s *Service) observeOwnDamageAurasLocked(guid uint64, updates []auraDelta, complete bool) {
	if complete {
		s.clearOwnDamageAurasLocked(guid)
	}
	for _, update := range updates {
		key := damageAuraSlot{target: guid, slot: update.slot}
		delete(s.ownDamageAuras, key)
		aura := update.aura
		if !aura.negative || aura.caster != s.selfGUID {
			continue
		}
		spell, known := s.learnedContractsLocked()[aura.spell]
		if !known || periodicDamage(spell) == 0 {
			continue
		}
		if s.ownDamageAuras == nil {
			s.ownDamageAuras = make(map[damageAuraSlot]observedBuffAura)
		}
		aura.at = s.now()
		s.ownDamageAuras[key] = aura
	}
}

func (s *Service) hasOwnDamageAuraLocked(guid uint64, family uint32) bool {
	now := s.now()
	for key, aura := range s.ownDamageAuras {
		if aura.durationKnown && aura.remaining(now) == 0 {
			delete(s.ownDamageAuras, key)
			continue
		}
		if key.target == guid {
			if spell, known := s.learnedContractsLocked()[aura.spell]; known && spell.Family == family {
				return true
			}
		}
	}
	return false
}
