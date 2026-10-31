package buddyactions

import worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"

const (
	fireball       uint32 = 133
	shadowBolt     uint32 = 686
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
		s.state.CombatSpellId = s.selectedSpellLocked(smite).id
	case 8:
		s.state.ClassName = "mage"
		s.state.CombatStyle = "mage_fireball"
		s.state.CombatSpellId = s.selectedSpellLocked(fireball).id
	case 9:
		s.state.ClassName = "warlock"
		s.state.CombatStyle = "warlock_shadow_bolt"
		s.state.CombatSpellId = s.selectedSpellLocked(shadowBolt).id
	case 4:
		s.state.ClassName = "rogue"
		s.state.CombatStyle = "rogue_melee"
		s.state.CombatSpellId = s.selectedSpellLocked(sinisterStrike).id
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
	if spell == 0 || spell > 0x7fffffff || known && !s.knownSpells[spell] && len(s.knownSpells) >= 4096 {
		return
	}
	if s.knownSpells == nil {
		s.knownSpells = make(map[uint32]bool)
	}
	if s.knownSpells[spell] != known {
		s.spellbookRevision++
	}
	if known {
		s.knownSpells[spell] = true
	} else {
		delete(s.knownSpells, spell)
	}
	if r := spellRankFor(spell); r.id != 0 {
		found := highestKnownSpell(s.knownSpells, r.family).id != 0
		switch r.family {
		case lesserHeal:
			s.state.KnownLesserHeal = found
		case smite:
			s.state.KnownSmite = found
		case fireball:
			s.state.KnownFireball = found
		case shadowBolt:
			s.state.KnownShadowBolt = found
		case sinisterStrike:
			s.state.KnownSinisterStrike = found
		}
		return
	}
	switch spell {
	case autoAttack:
		s.state.KnownAutoAttack = known
	case 13732:
		s.improvedSinister1 = known
	case 13863:
		s.improvedSinister2 = known
	}
}
func (s *Service) selectedSpellLocked(family uint32) spellRank {
	return highestKnownSpell(s.knownSpells, family)
}

func (s *Service) offensiveDamageLocked(spell, damage uint32) {
	if damage == 0 {
		return
	}
	switch spellRankFor(spell).family {
	case smite:
		s.state.SmiteDamageEvents++
		s.state.SmiteDamageTotal += uint64(damage)
	case fireball:
		s.state.FireballDamageEvents++
		s.state.FireballDamageTotal += uint64(damage)
	case shadowBolt:
		s.state.ShadowBoltDamageEvents++
		s.state.ShadowBoltDamageTotal += uint64(damage)
	case sinisterStrike:
		s.state.SinisterStrikeDamageEvents++
		s.state.SinisterStrikeDamageTotal += uint64(damage)
	default:
		return
	}
	s.state.AssistDamageEvents++
	s.state.AssistDamageTotal += uint64(damage)
}
