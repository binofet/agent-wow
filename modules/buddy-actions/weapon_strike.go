package buddyactions

import (
	"errors"
	"math"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

type comboObservation struct {
	known  bool
	target uint64
	points uint32
}

// Unit::SendComboPoints sends packed target GUID then uint8 count to its own
// player. Preserve unknown until an actual receipt; casts never infer points.
func (s *Service) observeComboPacket(code uint32, payload []byte) (bool, error) {
	if code == opcode.SMSGTransferPending && (len(payload) == 4 || len(payload) == 12) ||
		(code == opcode.SMSGNewWorld || code == opcode.SMSGLoginVerifyWorld) && validEncounterWorldEntry(payload) {
		s.weaponCombo = comboObservation{}
	}
	if code != opcode.SMSGUpdateComboPoints {
		return false, nil
	}
	target, size, err := readGUID(payload)
	if err != nil || len(payload) != size+1 || payload[size] > 5 || target == 0 && payload[size] != 0 {
		return true, errors.New("malformed own combo-point observation")
	}
	s.weaponCombo = comboObservation{known: true, target: target, points: uint32(payload[size])}
	return true, nil
}

func instantWeaponStrike(spell spellcatalog.Spell) bool {
	return spell.Capability().Kind == spellcatalog.WeaponStrike && spell.CastMs == 0
}

func (s *Service) offenseSpellCost(spell spellcatalog.Spell, self *worldv1.PlayerState) uint64 {
	cost := spellCost(spell, self)
	if spell.Family == sinisterStrike && spell.Power == 3 && cost == 45 {
		if s.improvedSinister2 {
			return 40
		}
		if s.improvedSinister1 {
			return 42
		}
	}
	return cost
}

func (s *Service) strikeWeaponReady(self *worldv1.PlayerState, spell spellcatalog.Spell) bool {
	if spell.ItemClass < 0 {
		return true
	}
	if !s.weaponReady(self) {
		return false
	}
	proof := s.weapons[self.Mainhand.Entry]
	return int32(proof.class) == spell.ItemClass && proof.subclass < 32 &&
		(spell.SubclassMask == 0 || spell.SubclassMask&(1<<proof.subclass) != 0) && proof.inventory < 32 &&
		(spell.InventoryMask == 0 || spell.InventoryMask&(1<<proof.inventory) != 0)
}

func observedWeaponCaster(self *worldv1.PlayerState) bool {
	return self != nil && self.ClassKnown && self.ClassId != 0 && self.UnitFlagsKnown &&
		self.UnitFlags&(0x20000|0x200000) == 0 && self.ChannelSpellKnown && self.ChannelSpellId == 0 &&
		self.ChannelObjectKnown && self.ChannelObjectGuid == 0
}

func (s *Service) offenseCustodyFor(spell spellcatalog.Spell) bool {
	if !instantWeaponStrike(spell) {
		return s.offenseCustodyAvailableLocked()
	}
	return !s.closed && !s.clientControlLost && !s.clientControlRestoring && !s.state.CastPending &&
		!s.state.Traveling && !s.state.MotionUnconfirmed && !s.state.AutoAttackUnconfirmed &&
		(!s.state.AutoAttackRequested || s.acknowledgedSwingActiveLocked()) && !s.autoStopping && s.state.InteractionOwner == "" && !s.state.Practicing &&
		!s.fargodeep.active && !s.recovery.state.Active &&
		!s.now().Before(s.nextAssist) && !s.now().Before(s.nextHeal)
}

// AutoAttackRequested records the still-desired attack and remains true after
// ATTACKSTART. A current matching server acknowledgement releases the start
// reservation so instant strikes may coexist with the active swing.
func (s *Service) acknowledgedSwingActiveLocked() bool {
	return s.state.AutoAttacking && s.autoTarget != 0 && s.state.AutoAttackTargetGuid == s.autoTarget &&
		s.encounterVisit.bound && s.meleeAcknowledgedVisit == s.encounterVisit.revision &&
		s.pendingMeleeOperationLocked() == nil
}

func (s *Service) offenseCasterFor(self *worldv1.PlayerState, spell spellcatalog.Spell) bool {
	if instantWeaponStrike(spell) {
		return observedWeaponCaster(self)
	}
	return observedOffenseCaster(self)
}

func (s *Service) strikeTargetReady(target uint64, spell spellcatalog.Spell) bool {
	return !spell.RequiresComboPoints() || s.weaponCombo.known && s.weaponCombo.points > 0 && s.weaponCombo.target == target
}

func (s *Service) reflectWeaponStrike(view *actionsv1.EncounterSpell, spell spellcatalog.Spell, self *worldv1.PlayerState) {
	view.WeaponStrike, view.RequiresBehind, view.RequiresStealth = instantWeaponStrike(spell), spell.Behind, spell.Attrs[0]&0x20000 != 0
	view.RequiresComboPoints = spell.RequiresComboPoints()
	view.RequiresFacing = spell.Facing&1 != 0
	view.Cost = s.offenseSpellCost(spell, self)
	view.DamageModelKnown = !view.RequiresComboPoints
	multiplier, bonus, hasWeaponDamage := float64(1), float64(0), false
	for _, effect := range spell.Effects {
		switch effect.Kind {
		case 80:
			view.ComboPointsGenerated += uint32(max(0, effect.Points))
		case 17, 58, 121:
			bonus += float64(effect.Points) + math.Max(0, float64(effect.Dice)-1)/2
			hasWeaponDamage = true
		case 31:
			multiplier *= float64(effect.Points) / 100
			hasWeaponDamage = true
		}
	}
	if hasWeaponDamage {
		view.WeaponDamageMultiplier, view.WeaponBonusDamage = multiplier, bonus
		proof := s.weapons[self.GetMainhand().GetEntry()]
		view.DamageModelKnown = proof.damageKnown
		view.DirectDamage = 0
		if proof.damageKnown {
			view.DirectDamage = (proof.damageMin + proof.damageMax) / 2
		}
		// Preserve the catalog effect order: Backstab applies its flat bonus
		// before its weapon percentage, whereas other strikes may differ.
		for _, effect := range spell.Effects {
			switch effect.Kind {
			case 17, 58, 121:
				view.DirectDamage += float64(effect.Points) + math.Max(0, float64(effect.Dice)-1)/2
			case 31:
				view.DirectDamage *= float64(effect.Points) / 100
			}
		}
	}
}
