package buddyprofessions

import (
	"fmt"
	"math"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

type skillRank struct{ value, max uint32 }

type worldView struct {
	skillsRevision, backpackRevision uint64
	skills                           map[uint32]skillRank
	counts                           map[uint32]uint32
	freeSlots                        uint32
	level                            uint32
	self                             *worldv1.PlayerState
	raw                              *worldv1.SnapshotResponse
}

// validateWorld consumes only the public snapshot of this session's character.
// It performs no queries or writes and infers neither ownership nor missing rows.
// Skill bonuses never augment the actual skill values used by requirements.
func validateWorld(w *worldv1.SnapshotResponse, selfGUID uint64) (worldView, error) {
	if selfGUID == 0 || selfGUID>>48 != 0 || uint32(selfGUID) == 0 || w == nil || w.Self == nil || w.Self.Guid != selfGUID || !w.Self.Present {
		return worldView{}, fmt.Errorf("actual own character is missing or mismatched")
	}
	p := w.Self
	if !p.AliveKnown || !p.Alive || !p.HealthKnown || p.Health == 0 || p.MaxHealth == 0 || p.Health > p.MaxHealth || p.PlayerFlags&0x10 != 0 || p.DynamicFlags&0x20 != 0 {
		return worldView{}, fmt.Errorf("actual own living health is unconfirmed")
	}
	if !p.ClassKnown || p.ClassId == 0 || p.ClassId > 11 || p.ClassId == 10 || !p.LevelKnown || p.Level == 0 || p.Level > 60 {
		return worldView{}, fmt.Errorf("actual own class and character level1..60 are required")
	}
	if !p.MapKnown || !p.PositionKnown {
		return worldView{}, fmt.Errorf("actual own map and position are unknown")
	}
	for _, coordinate := range []float32{p.X, p.Y, p.Z, p.Orientation} {
		if math.IsNaN(float64(coordinate)) || math.IsInf(float64(coordinate), 0) || math.Abs(float64(coordinate)) > 40000 {
			return worldView{}, fmt.Errorf("actual own position is invalid")
		}
	}
	if !p.UnitFlagsKnown || p.UnitFlags&0x80000 != 0 {
		return worldView{}, fmt.Errorf("actual own noncombat unit flags are unconfirmed")
	}
	if !w.SelfSkillsKnown || w.SelfSkillsRevision == 0 || len(w.SelfSkills) != 128 {
		return worldView{}, fmt.Errorf("complete actual own skill slots and revision are required")
	}
	view := worldView{
		skillsRevision:   w.SelfSkillsRevision,
		backpackRevision: w.BackpackRevision,
		skills:           make(map[uint32]skillRank),
		counts:           make(map[uint32]uint32),
		level:            p.Level,
		self:             p,
		raw:              w,
	}
	var skillSlots [128]bool
	for _, row := range w.SelfSkills {
		if row == nil || row.Slot >= 128 || skillSlots[row.Slot] {
			return worldView{}, fmt.Errorf("actual own skill slot is missing, duplicate or out of range")
		}
		skillSlots[row.Slot] = true
		if row.SkillId > math.MaxUint16 || row.Step > math.MaxUint16 || row.Value > row.MaxValue || row.MaxValue > math.MaxUint16 || row.TemporaryBonus < math.MinInt16 || row.TemporaryBonus > math.MaxInt16 || row.PermanentBonus < math.MinInt16 || row.PermanentBonus > math.MaxInt16 {
			return worldView{}, fmt.Errorf("actual own skill fields exceed their wire bounds")
		}
		if row.SkillId == 0 {
			if row.Step != 0 || row.Value != 0 || row.MaxValue != 0 || row.TemporaryBonus != 0 || row.PermanentBonus != 0 {
				return worldView{}, fmt.Errorf("empty actual own skill slot has nonzero fields")
			}
			continue
		}
		if _, duplicate := view.skills[row.SkillId]; duplicate || row.Value == 0 || row.MaxValue == 0 {
			return worldView{}, fmt.Errorf("actual own skill is duplicate or has no positive rank")
		}
		view.skills[row.SkillId] = skillRank{value: row.Value, max: row.MaxValue}
	}
	if !w.BackpackKnown || w.BackpackRevision == 0 || len(w.Backpack) != 16 {
		return worldView{}, fmt.Errorf("complete actual own backpack slots and revision are required")
	}
	var backpackSlots [16]bool
	itemGUIDs := make(map[uint64]bool)
	for _, row := range w.Backpack {
		if row == nil || row.Bag != 0 || row.Slot >= 16 || backpackSlots[row.Slot] {
			return worldView{}, fmt.Errorf("actual own backpack slot is missing, duplicate or out of range")
		}
		backpackSlots[row.Slot] = true
		if row.ItemGuid == 0 {
			if row.Entry != 0 || row.StackCount != 0 || row.MetadataKnown {
				return worldView{}, fmt.Errorf("empty actual own backpack slot has item metadata")
			}
			view.freeSlots++
			continue
		}
		if !row.MetadataKnown || row.Entry == 0 || row.StackCount == 0 || itemGUIDs[row.ItemGuid] {
			return worldView{}, fmt.Errorf("actual own backpack item metadata is incomplete or duplicated")
		}
		itemGUIDs[row.ItemGuid] = true
		if row.StackCount > math.MaxUint32-view.counts[row.Entry] {
			return worldView{}, fmt.Errorf("actual own backpack item count overflows")
		}
		view.counts[row.Entry] += row.StackCount
	}
	return view, nil
}
