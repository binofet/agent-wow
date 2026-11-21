package buddyinventory

import (
	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	"strconv"
)

func (s *Service) gearContextLocked() *inventoryv1.GearContext {
	out := &inventoryv1.GearContext{Templates: map[string]*inventoryv1.GearTemplate{}}
	if !s.known {
		return out
	}
	self := s.view.w.GetSelf()
	out.ClassKnown, out.ClassId = self.GetClassKnown(), self.GetClassId()
	out.RaceKnown, out.RaceId = self.GetRaceKnown(), self.GetRaceId()
	out.LevelKnown, out.Level = self.GetLevelKnown(), self.GetLevel()
	// Only currently owned carried/equipped templates are part of this view.
	add := func(entry uint32) {
		if m := s.metadata[entry]; m.known {
			out.Templates[strconv.FormatUint(uint64(entry), 10)] = gearTemplate(m)
		}
	}

	for _, item := range s.view.slots {
		if item.ItemGuid != 0 && item.MetadataKnown {
			add(item.Entry)
		}
	}
	if equipment, known := equipmentView(s.view); known {
		for _, item := range equipment {
			if item.ItemGuid != 0 && item.MetadataKnown {
				add(item.Entry)
			}
		}
	}
	return out
}

func gearTemplate(m itemMetadata) *inventoryv1.GearTemplate {
	row := &inventoryv1.GearTemplate{ItemId: m.entry, Available: m.known, Name: m.name, ItemClass: m.class, Subclass: m.subclass,
		InventoryType: m.inventoryType, Quality: m.quality, Armor: m.armor, DelayMs: m.delay,
		Resistances: append([]uint32(nil), m.resistances[:]...), ArmorComparisonKnown: !m.specialArmor,
		Flags: m.flags, Flags2: m.flags2, HasSpell: m.hasSpell, ScalingDistribution: m.scalingDistribution, ScalingValue: m.scalingValue, RequiredLevel: m.requiredLevel}
	if m.statCount > uint32(len(m.stats)) {
		row.Available = false
		return row
	}
	for _, stat := range m.stats[:m.statCount] {
		row.Stats = append(row.Stats, &inventoryv1.GearStat{Type: stat.kind, Value: stat.value})
	}
	for _, damage := range m.damages {
		row.Damages = append(row.Damages, &inventoryv1.GearDamage{Minimum: damage.minimum, Maximum: damage.maximum, School: damage.school})
	}
	return row
}
