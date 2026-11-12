package buddyprofessions

import (
	"math"
	"testing"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
)

func professionObservationFixture() *worldv1.SnapshotResponse {
	w := &worldv1.SnapshotResponse{
		Self: &worldv1.PlayerState{
			Guid: 7, Present: true, AliveKnown: true, Alive: true,
			HealthKnown: true, Health: 50, MaxHealth: 50,
			ClassKnown: true, ClassId: 8, LevelKnown: true, Level: 1,
			MapKnown: true, MapId: 0, PositionKnown: true,
			X: -8949, Y: -132, Z: 83, UnitFlagsKnown: true,
		},
		SelfSkillsKnown: true, SelfSkillsRevision: 4,
		BackpackKnown: true, BackpackRevision: 9,
		InventoryKnown: true, InventoryRevision: 9,
	}
	for slot := range 128 {
		w.SelfSkills = append(w.SelfSkills, &worldv1.SkillState{Slot: uint32(slot)})
	}
	for slot := range 16 {
		w.Backpack = append(w.Backpack, &worldv1.BackpackSlot{Slot: uint32(slot)})
	}
	w.SelfSkills[0] = &worldv1.SkillState{Slot: 0, SkillId: 129, Step: 1, Value: 12, MaxValue: 75, TemporaryBonus: 5, PermanentBonus: -3}
	w.SelfSkills[42] = &worldv1.SkillState{Slot: 42, SkillId: 185, Step: 1, Value: 23, MaxValue: 75}
	w.SelfSkills[126] = &worldv1.SkillState{Slot: 126, SkillId: 356, Step: 1, Value: 3, MaxValue: 75}
	w.SelfSkills[127] = &worldv1.SkillState{Slot: 127, SkillId: 197, Step: 1, Value: 10, MaxValue: 75}
	w.Backpack[0] = &worldv1.BackpackSlot{Slot: 0, ItemGuid: 0x4000000000000001, Entry: 2589, StackCount: 5, MetadataKnown: true}
	w.Backpack[15] = &worldv1.BackpackSlot{Slot: 15, ItemGuid: 0x4000000000000002, Entry: 2589, StackCount: 2, MetadataKnown: true}
	for bag := uint32(0); bag < 5; bag++ {
		row := &worldv1.InventoryBag{Bag: bag, SlotKnown: true, ContentsKnown: true}
		if bag == 0 {
			row.SlotCount, row.Slots = 16, w.Backpack
		}
		w.InventoryBags = append(w.InventoryBags, row)
	}
	return w
}

func TestValidateWorldUsesCompleteOwnRawRanksAndBackpackCounts(t *testing.T) {
	w := professionObservationFixture()
	before := proto.Clone(w)
	view, err := validateWorld(w, 7)
	if err != nil {
		t.Fatal(err)
	}
	if view.skillsRevision != 4 || view.backpackRevision != 9 || view.inventoryRevision != 9 || view.level != 1 || view.self != w.Self || view.raw != w || len(view.skills) != 4 || view.skills[129] != (skillRank{12, 75}) || view.skills[197] != (skillRank{10, 75}) || view.counts[2589] != 7 || view.freeSlots != 14 {
		t.Fatalf("incorrect validated observation: %+v", view)
	}
	if !proto.Equal(w, before) {
		t.Fatal("validation mutated public observations")
	}
	// Slot order is irrelevant; the complete wire slot identities are required.
	w.SelfSkills[0], w.SelfSkills[127] = w.SelfSkills[127], w.SelfSkills[0]
	w.InventoryBags[0].Slots[0], w.InventoryBags[0].Slots[15] = w.InventoryBags[0].Slots[15], w.InventoryBags[0].Slots[0]
	if reordered, err := validateWorld(w, 7); err != nil || reordered.counts[2589] != 7 || reordered.skills[129].value != 12 {
		t.Fatal("valid reordered slots rejected", reordered, err)
	}
}

func TestValidateWorldRejectsUnknownForeignDeadOrInvalidSelf(t *testing.T) {
	cases := map[string]func(*worldv1.SnapshotResponse){
		"nil_self":                   func(w *worldv1.SnapshotResponse) { w.Self = nil },
		"foreign_self":               func(w *worldv1.SnapshotResponse) { w.Self.Guid = 3 },
		"not_present":                func(w *worldv1.SnapshotResponse) { w.Self.Present = false },
		"alive_unknown":              func(w *worldv1.SnapshotResponse) { w.Self.AliveKnown = false },
		"dead":                       func(w *worldv1.SnapshotResponse) { w.Self.Alive = false },
		"health_unknown":             func(w *worldv1.SnapshotResponse) { w.Self.HealthKnown = false },
		"zero_health":                func(w *worldv1.SnapshotResponse) { w.Self.Health = 0 },
		"zero_max_health":            func(w *worldv1.SnapshotResponse) { w.Self.MaxHealth = 0 },
		"health_exceeds_max":         func(w *worldv1.SnapshotResponse) { w.Self.Health = 51 },
		"ghost":                      func(w *worldv1.SnapshotResponse) { w.Self.PlayerFlags = 0x10 },
		"dead_dynamic_flag":          func(w *worldv1.SnapshotResponse) { w.Self.DynamicFlags = 0x20 },
		"class_unknown":              func(w *worldv1.SnapshotResponse) { w.Self.ClassKnown = false },
		"zero_class":                 func(w *worldv1.SnapshotResponse) { w.Self.ClassId = 0 },
		"unused_class":               func(w *worldv1.SnapshotResponse) { w.Self.ClassId = 10 },
		"class_outside_wire_catalog": func(w *worldv1.SnapshotResponse) { w.Self.ClassId = 12 },
		"level_unknown":              func(w *worldv1.SnapshotResponse) { w.Self.LevelKnown = false },
		"zero_level":                 func(w *worldv1.SnapshotResponse) { w.Self.Level = 0 },
		"character_level_above_60":   func(w *worldv1.SnapshotResponse) { w.Self.Level = 61 },
		"map_unknown":                func(w *worldv1.SnapshotResponse) { w.Self.MapKnown = false },
		"position_unknown":           func(w *worldv1.SnapshotResponse) { w.Self.PositionKnown = false },
		"nan_position":               func(w *worldv1.SnapshotResponse) { w.Self.X = float32(math.NaN()) },
		"infinite_position":          func(w *worldv1.SnapshotResponse) { w.Self.Z = float32(math.Inf(1)) },
		"invalid_orientation":        func(w *worldv1.SnapshotResponse) { w.Self.Orientation = float32(math.Inf(-1)) },
		"position_out_of_bounds":     func(w *worldv1.SnapshotResponse) { w.Self.Y = -40001 },
		"unit_flags_unknown":         func(w *worldv1.SnapshotResponse) { w.Self.UnitFlagsKnown = false },
		"combat":                     func(w *worldv1.SnapshotResponse) { w.Self.UnitFlags = 0x80000 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w := professionObservationFixture()
			mutate(w)
			assertProfessionObservationRejected(t, w, 7)
		})
	}
	for _, guid := range []uint64{0, 0xf130000001000001, 1 << 32} {
		w := professionObservationFixture()
		w.Self.Guid = guid
		assertProfessionObservationRejected(t, w, guid)
	}
	assertProfessionObservationRejected(t, nil, 7)
	w := professionObservationFixture()
	w.Self.Level, w.Self.ClassId = 60, 11
	if _, err := validateWorld(w, 7); err != nil {
		t.Fatal("valid character level60/class11 rejected", err)
	}
}

func TestValidateWorldRejectsPartialOrInconsistentSkillSlots(t *testing.T) {
	cases := map[string]func(*worldv1.SnapshotResponse){
		"unknown":                   func(w *worldv1.SnapshotResponse) { w.SelfSkillsKnown = false },
		"zero_revision":             func(w *worldv1.SnapshotResponse) { w.SelfSkillsRevision = 0 },
		"missing_slot":              func(w *worldv1.SnapshotResponse) { w.SelfSkills = w.SelfSkills[:127] },
		"extra_slot":                func(w *worldv1.SnapshotResponse) { w.SelfSkills = append(w.SelfSkills, &worldv1.SkillState{Slot: 128}) },
		"nil_slot":                  func(w *worldv1.SnapshotResponse) { w.SelfSkills[1] = nil },
		"duplicate_empty_slot":      func(w *worldv1.SnapshotResponse) { w.SelfSkills[2].Slot = 1 },
		"out_of_range_slot":         func(w *worldv1.SnapshotResponse) { w.SelfSkills[127].Slot = 128 },
		"duplicate_skill_id":        func(w *worldv1.SnapshotResponse) { w.SelfSkills[127].SkillId = 129 },
		"zero_rank":                 func(w *worldv1.SnapshotResponse) { w.SelfSkills[0].Value = 0 },
		"zero_max":                  func(w *worldv1.SnapshotResponse) { w.SelfSkills[0].MaxValue = 0 },
		"rank_above_max":            func(w *worldv1.SnapshotResponse) { w.SelfSkills[0].Value = 76 },
		"skill_id_overflow":         func(w *worldv1.SnapshotResponse) { w.SelfSkills[0].SkillId = 65536 },
		"step_overflow":             func(w *worldv1.SnapshotResponse) { w.SelfSkills[0].Step = 65536 },
		"max_overflow":              func(w *worldv1.SnapshotResponse) { w.SelfSkills[0].MaxValue = 65536 },
		"empty_rank":                func(w *worldv1.SnapshotResponse) { w.SelfSkills[1].Value = 1 },
		"empty_step":                func(w *worldv1.SnapshotResponse) { w.SelfSkills[1].Step = 1 },
		"empty_max":                 func(w *worldv1.SnapshotResponse) { w.SelfSkills[1].MaxValue = 1 },
		"empty_temporary_bonus":     func(w *worldv1.SnapshotResponse) { w.SelfSkills[1].TemporaryBonus = 1 },
		"empty_permanent_bonus":     func(w *worldv1.SnapshotResponse) { w.SelfSkills[1].PermanentBonus = -1 },
		"temporary_bonus_overflow":  func(w *worldv1.SnapshotResponse) { w.SelfSkills[0].TemporaryBonus = 32768 },
		"permanent_bonus_underflow": func(w *worldv1.SnapshotResponse) { w.SelfSkills[0].PermanentBonus = -32769 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w := professionObservationFixture()
			mutate(w)
			assertProfessionObservationRejected(t, w, 7)
		})
	}
	w := professionObservationFixture()
	w.SelfSkills[0].TemporaryBonus, w.SelfSkills[0].PermanentBonus = math.MinInt16, math.MaxInt16
	w.SelfSkills[127].Value, w.SelfSkills[127].MaxValue = math.MaxUint16, math.MaxUint16
	if view, err := validateWorld(w, 7); err != nil || view.skills[129].value != 12 || view.skills[197].max != math.MaxUint16 {
		t.Fatal("valid wire bounds or raw ranks rejected", view, err)
	}
}

func TestValidateWorldRejectsPartialOrInconsistentCarriedBackpackSlots(t *testing.T) {
	cases := map[string]func(*worldv1.SnapshotResponse){
		"unknown":       func(w *worldv1.SnapshotResponse) { w.InventoryKnown = false },
		"zero_revision": func(w *worldv1.SnapshotResponse) { w.InventoryRevision = 0 },
		"missing_slot":  func(w *worldv1.SnapshotResponse) { w.InventoryBags[0].Slots = w.InventoryBags[0].Slots[:15] },
		"extra_slot": func(w *worldv1.SnapshotResponse) {
			w.InventoryBags[0].Slots = append(w.InventoryBags[0].Slots, &worldv1.BackpackSlot{Slot: 16})
		},
		"nil_slot":              func(w *worldv1.SnapshotResponse) { w.InventoryBags[0].Slots[1] = nil },
		"bag_not_zero":          func(w *worldv1.SnapshotResponse) { w.InventoryBags[0].Slots[1].Bag = 1 },
		"duplicate_empty_slot":  func(w *worldv1.SnapshotResponse) { w.InventoryBags[0].Slots[2].Slot = 1 },
		"out_of_range_slot":     func(w *worldv1.SnapshotResponse) { w.InventoryBags[0].Slots[15].Slot = 16 },
		"empty_entry":           func(w *worldv1.SnapshotResponse) { w.InventoryBags[0].Slots[1].Entry = 2589 },
		"empty_count":           func(w *worldv1.SnapshotResponse) { w.InventoryBags[0].Slots[1].StackCount = 1 },
		"empty_known_metadata":  func(w *worldv1.SnapshotResponse) { w.InventoryBags[0].Slots[1].MetadataKnown = true },
		"unknown_item_metadata": func(w *worldv1.SnapshotResponse) { w.InventoryBags[0].Slots[0].MetadataKnown = false },
		"zero_item_entry":       func(w *worldv1.SnapshotResponse) { w.InventoryBags[0].Slots[0].Entry = 0 },
		"zero_item_count":       func(w *worldv1.SnapshotResponse) { w.InventoryBags[0].Slots[0].StackCount = 0 },
		"duplicate_item_guid": func(w *worldv1.SnapshotResponse) {
			w.InventoryBags[0].Slots[15].ItemGuid = w.InventoryBags[0].Slots[0].ItemGuid
		},
		"aggregate_overflow": func(w *worldv1.SnapshotResponse) { w.InventoryBags[0].Slots[0].StackCount = math.MaxUint32 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w := professionObservationFixture()
			mutate(w)
			assertProfessionObservationRejected(t, w, 7)
		})
	}
	w := professionObservationFixture()
	w.InventoryBags[0].Slots[0].StackCount = math.MaxUint32 - 2
	if view, err := validateWorld(w, 7); err != nil || view.counts[2589] != math.MaxUint32 {
		t.Fatal("bounded uint32 total rejected", view, err)
	}
}

func assertProfessionObservationRejected(t *testing.T, w *worldv1.SnapshotResponse, guid uint64) {
	t.Helper()
	view, err := validateWorld(w, guid)
	if err == nil {
		t.Fatal("incomplete or inconsistent observations accepted")
	}
	if view.self != nil || view.raw != nil || view.skills != nil || view.counts != nil || view.skillsRevision != 0 || view.backpackRevision != 0 || view.inventoryRevision != 0 || view.freeSlots != 0 || view.level != 0 {
		t.Fatal("rejection exposed partial optimistic facts", view)
	}
}
