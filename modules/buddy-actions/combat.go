package buddyactions

import worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"

const (
	fireball       uint32 = 133
	sinisterStrike uint32 = 1752
	autoAttack     uint32 = 6603
)

func (s *Service) updateClassLocked() {
	s.state.ClassKnown = false
	s.state.ClassId = 0
	s.state.ClassName = ""
	s.state.CombatStyle = ""
	s.state.CombatSpellId = 0
	s.state.SinisterStrikeEnergyCost = 45
	s.state.MainhandWeaponVerified = false
	if s.improvedSinister2 {
		s.state.SinisterStrikeEnergyCost = 40
	} else if s.improvedSinister1 {
		s.state.SinisterStrikeEnergyCost = 42
	}
	if s.lastWorld == nil || s.lastWorld.Self == nil || s.lastWorld.Self.Guid != s.selfGUID || !s.lastWorld.Self.ClassKnown {
		return
	}
	p := s.lastWorld.Self
	s.state.ClassKnown = true
	s.state.ClassId = p.ClassId
	switch p.ClassId {
	case 5:
		s.state.ClassName = "priest"
		s.state.CombatStyle = "priest_smite"
		s.state.CombatSpellId = smite
	case 8:
		s.state.ClassName = "mage"
		s.state.CombatStyle = "mage_fireball"
		s.state.CombatSpellId = fireball
	case 4:
		s.state.ClassName = "rogue"
		s.state.CombatStyle = "rogue_melee"
		s.state.CombatSpellId = sinisterStrike
	default:
		s.state.ClassName = "unsupported"
	}
	s.state.MainhandWeaponVerified = s.weaponReady(p)
}

func manaBudget(p *worldv1.PlayerState, percent uint64) uint64 {
	base := uint64(p.MaxPower)
	if p.BaseManaKnown && p.BaseMana > 0 {
		base = uint64(p.BaseMana)
	}
	return (base*percent + 99) / 100
}

func (s *Service) setKnownSpell(spell uint32, known bool) {
	switch spell {
	case lesserHeal:
		s.state.KnownLesserHeal = known
	case smite:
		s.state.KnownSmite = known
	case fireball:
		s.state.KnownFireball = known
	case sinisterStrike:
		s.state.KnownSinisterStrike = known
	case autoAttack:
		s.state.KnownAutoAttack = known
	case 13732:
		s.improvedSinister1 = known
	case 13863:
		s.improvedSinister2 = known
	}
}

func (s *Service) offensiveDamageLocked(spell, damage uint32) {
	if damage == 0 {
		return
	}
	switch spell {
	case smite:
		s.state.SmiteDamageEvents++
		s.state.SmiteDamageTotal += uint64(damage)
	case fireball:
		s.state.FireballDamageEvents++
		s.state.FireballDamageTotal += uint64(damage)
	case sinisterStrike:
		s.state.SinisterStrikeDamageEvents++
		s.state.SinisterStrikeDamageTotal += uint64(damage)
	default:
		return
	}
	s.state.AssistDamageEvents++
	s.state.AssistDamageTotal += uint64(damage)
}
