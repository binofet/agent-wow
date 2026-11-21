package buddyinventory

import (
	"math"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

func consumableMetadata(m itemMetadata, p *worldv1.PlayerState) *inventoryv1.ConsumableMetadata {
	if !m.known || m.class != 0 || m.subclass != 5 {
		return nil
	}
	view := &inventoryv1.ConsumableMetadata{}
	drink, mana := m.manaDrink(p)
	food := m.foodBuff(p)
	for _, id := range m.onUseSpells {
		if id == 0 {
			continue
		}
		spell, known := spellcatalog.Default().Lookup(id)
		if !known {
			continue
		}
		row := &inventoryv1.ConsumableSpell{SpellId: id, Name: spell.Name, DrinkSupported: drink == id, FoodSupported: food.useSpell == id && food.aura != 0}
		if row.DrinkSupported {
			row.ManaPerTick = mana
		}
		if row.FoodSupported {
			row.FoodAuraSpellId, row.FoodDelayMs = food.aura, food.waitMS
			buff, _ := spellcatalog.Default().Lookup(food.aura)
			for _, effect := range buff.Effects {
				if effect.Kind == 6 && effect.A == 1 && effect.B == 0 {
					row.FoodEffects = append(row.FoodEffects, &inventoryv1.ConsumableAuraEffect{AuraType: effect.Aura, MiscValue: effect.Misc, Points: effect.Points, Dice: effect.Dice})
				}
			}
		}
		view.Usable = view.Usable || row.DrinkSupported || row.FoodSupported
		view.Spells = append(view.Spells, row)
	}
	if !view.Usable {
		view.BlockedReason = "ordinary_usable_self_drink_or_buff_food_effect_unproven"
	}
	return view
}

func (s *Service) consumableTotalsLocked() {
	known := s.view.w.GetSelf().GetLevelKnown()
	var count uint64
	for _, item := range s.state.Items {
		if !item.MetadataKnown {
			known = false
			continue
		}
		for _, spell := range item.GetConsumable().GetSpells() {
			if spell.DrinkSupported {
				count += uint64(item.Item.StackCount)
			}
			if spell.FoodSupported {
				s.state.BuffFoodAvailable = true
			}
		}
	}
	if known && count <= math.MaxUint32 {
		value := uint32(count)
		s.state.ManaDrinkCount = &value
	}
}

// These are catalog mechanics for a single owned item's ordinary use spell.
// They do not select an item, rank, reserve, recipient or time to consume it.
func manaDrinkPower(spell spellcatalog.Spell) int32 {
	if spell.CastMs != 0 || spell.DurationIndex == 0 || spell.TargetMask != 0 {
		return 0
	}
	var regen, periodic int32
	var manaRegen bool
	for _, e := range spell.Effects {
		if e.Kind != 6 || e.A != 1 || e.B != 0 || e.Trigger != 0 || e.Chain != 0 || e.Radius != 0 || e.Misc != 0 {
			return 0
		}
		switch e.Aura {
		case 85: // SPELL_AURA_MOD_POWER_REGEN with the mana power type.
			manaRegen, regen = true, e.Points
		case 226: // Drinks such as Spring Water supply the amount via periodic dummy.
			if e.PeriodMs > 0 {
				periodic = e.Points
			}
		default:
			return 0
		}
	}
	if !manaRegen {
		return 0
	}
	return max(regen, periodic)
}

func (m itemMetadata) manaDrink(p *worldv1.PlayerState) (uint32, int32) {
	if !m.known || m.class != 0 || m.subclass != 5 || m.inventoryType != 0 || m.hasRequirement || m.startQuest != 0 ||
		m.requiredLevel != 0 && (!p.LevelKnown || p.Level < m.requiredLevel) {
		return 0, 0
	}
	var selected uint32
	var power int32
	for _, id := range m.onUseSpells {
		if id == 0 {
			continue
		}
		spell, ok := spellcatalog.Default().Lookup(id)
		strength := manaDrinkPower(spell)
		// The normal server item handler can cast multiple on-use spells.
		// This primitive accepts an ordinary mana drink with one use effect.
		if selected != 0 || !ok || strength <= 0 {
			return 0, 0
		}
		selected, power = id, strength
	}
	return selected, power
}

type foodBuff struct {
	useSpell, aura, waitMS uint32
}

func (m itemMetadata) foodBuff(p *worldv1.PlayerState) foodBuff {
	if !m.known || m.class != 0 || m.subclass != 5 || m.inventoryType != 0 || m.hasRequirement || m.startQuest != 0 ||
		m.requiredLevel != 0 && (!p.LevelKnown || p.Level < m.requiredLevel) {
		return foodBuff{}
	}
	var result foodBuff
	for _, id := range m.onUseSpells {
		if id == 0 {
			continue
		}
		spell, ok := spellcatalog.Default().Lookup(id)
		if result.useSpell != 0 || !ok || spell.CastMs != 0 || spell.DurationIndex == 0 || spell.TargetMask != 0 ||
			(spell.Name != "Food" && spell.Name != "Refreshment") {
			return foodBuff{}
		}
		result.useSpell = id
		for _, effect := range spell.Effects {
			// A timed, self-applied trigger must point to a Well Fed aura.
			if effect.Kind != 6 || effect.A != 1 || effect.B != 0 || effect.Aura != 23 || effect.PeriodMs == 0 {
				continue
			}
			buff, known := spellcatalog.Default().Lookup(effect.Trigger)
			if !known || buff.Name != "Well Fed" || buff.DurationIndex == 0 || buff.TargetMask != 0 {
				continue
			}
			result.aura, result.waitMS = buff.ID, effect.PeriodMs
		}
	}
	return result
}
