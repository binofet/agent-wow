package modrt

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Shared with the collector's typed RPC harness. All GUIDs are observed item
// namespace values; blocked physical destinations remain factual rows.
func disposalFactualFixture() (*inventoryv1.State, *worldv1.SnapshotResponse, *questsv1.JournalResponse, *inventoryv1.ItemRef, *tradev1.ItemDetails) {
	source := &inventoryv1.ItemRef{Slot: 3, ItemGuid: 0x4000000000000001, Entry: 907000, StackCount: 1}
	inv := &inventoryv1.State{SessionId: "disposal-facts-body", SelfGuid: 4, Revision: 1, InventoryKnown: true,
		InventoryRevision: 20, EquipmentKnown: true, EquipmentRevision: 21}
	for bag := uint32(0); bag < 5; bag++ {
		row := &worldv1.InventoryBag{Bag: bag, SlotKnown: true, ContentsKnown: true}
		if bag == 0 {
			row.SlotCount = 16
			for slot := uint32(0); slot < 16; slot++ {
				row.Slots = append(row.Slots, &worldv1.BackpackSlot{Slot: slot})
			}
			row.Slots[3] = &worldv1.BackpackSlot{Slot: 3, ItemGuid: source.ItemGuid, Entry: source.Entry, StackCount: 1,
				MetadataKnown: true, EnhancementsKnown: true, DurabilityKnown: true, Durability: 20, MaxDurability: 22}
		}
		inv.InventoryBags = append(inv.InventoryBags, row)
	}
	for slot := uint32(0); slot < 19; slot++ {
		inv.Equipment = append(inv.Equipment, &worldv1.EquipmentSlotState{Slot: slot, SlotKnown: true})
	}
	inv.Equipment[4] = &worldv1.EquipmentSlotState{Slot: 4, SlotKnown: true, ItemGuid: 0x4000000000000002,
		Entry: 907001, StackCount: 1, MetadataKnown: true, EnhancementsKnown: true, DurabilityKnown: true, Durability: 30, MaxDurability: 40}
	inv.Items = []*inventoryv1.ItemEligibility{{Item: proto.Clone(source).(*inventoryv1.ItemRef), MetadataKnown: true,
		GearFacts: &inventoryv1.GearItemFacts{EnhancementsKnown: true, DurabilityKnown: true, Durability: 20, MaxDurability: 22,
			Destinations: []*inventoryv1.GearDestination{{EquipmentSlot: 4, ExpectedEquippedItemGuid: inv.Equipment[4].ItemGuid,
				Eligible: false, BlockedReason: "actual_class_race_level_required"}}}}}
	inv.GearContext = &inventoryv1.GearContext{Templates: map[string]*inventoryv1.GearTemplate{
		"907001": {ItemId: 907001, Available: true, ItemClass: 4, Subclass: 1, InventoryType: 20, Armor: 30, ArmorComparisonKnown: true},
		"907000": {ItemId: 907000, Available: true, ItemClass: 4, Subclass: 1, InventoryType: 20, Armor: 20, ArmorComparisonKnown: true}}}
	world := &worldv1.SnapshotResponse{Self: &worldv1.PlayerState{Guid: 4, Present: true}, InventoryKnown: true, InventoryRevision: 20,
		SelfEquipmentKnown: true, SelfEquipmentRevision: 21, SelfQuestLogKnown: true, SelfQuestLogRevision: 22}
	for _, row := range inv.Equipment {
		world.SelfEquipment = append(world.SelfEquipment, proto.Clone(row).(*worldv1.EquipmentSlotState))
	}
	for slot := uint32(0); slot < 25; slot++ {
		world.SelfQuestLog = append(world.SelfQuestLog, &worldv1.QuestLogEntry{Slot: slot})
	}
	world.SelfQuestLog[0].QuestId = 99000
	journal := &questsv1.JournalResponse{SessionId: inv.SessionId, QuestLogRevision: 22,
		Entries: []*questsv1.QuestJournalEntry{{QuestId: 99000, TemplateKnown: true, LogStateKnown: true, ItemProtectionKnown: true,
			Targets: []*questsv1.QuestObjective{{Kind: "ITEM", TargetId: 2004, RequiredCount: 1}, {Kind: "SOURCE_ITEM", TargetId: 5000}, {Kind: "CREATURE"}}}}}
	item := &tradev1.ItemDetails{ItemId: source.Entry, Available: true, ItemClass: 4, Subclass: 1, InventoryType: 20,
		SellPrice: 5, Armor: 20, ArmorComparisonKnown: true, RawTemplateFacts: &tradev1.RawTemplateFacts{}}
	return inv, world, journal, source, item
}

type disposalFactFrame struct {
	inv     *inventoryv1.State
	world   *worldv1.SnapshotResponse
	journal *questsv1.JournalResponse
	source  *inventoryv1.ItemRef
}

func disposalFrame() disposalFactFrame {
	i, w, j, s, _ := disposalFactualFixture()
	return disposalFactFrame{i, w, j, s}
}

func disposalFactNativeJSON(t *testing.T, message proto.Message) map[string]any {
	t.Helper()
	encoded, err := protojson.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func disposalPythonContext(t *testing.T, f disposalFactFrame) map[string]any {
	t.Helper()
	input := map[string]any{"inventory": disposalFactNativeJSON(t, f.inv), "world": disposalFactNativeJSON(t, f.world),
		"journal": disposalFactNativeJSON(t, f.journal), "source": disposalFactNativeJSON(t, f.source)}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", "-B", "-c", `import importlib.util,json,sys
spec=importlib.util.spec_from_file_location("production_disposal",sys.argv[1])
module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
json.dump(module.project_context(json.load(sys.stdin)),sys.stdout,allow_nan=False)`, filepath.Join("..", "..", "..", "dev", "item_disposal.py"))
	command.Stdin = bytes.NewReader(encoded)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("production Python projector: %v: %s", err, output)
	}
	var value map[string]any
	if err := json.Unmarshal(output, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestDisposalFactsMatchProductionPythonOnTypedNativeFrames(t *testing.T) {
	cases := map[string]func(*disposalFactFrame){
		"blocked_destination_and_zero_omissions": func(*disposalFactFrame) {},
		"all_actual_destinations": func(f *disposalFactFrame) {
			f.inv.Items[0].GearFacts.Destinations = append(f.inv.Items[0].GearFacts.Destinations,
				&inventoryv1.GearDestination{EquipmentSlot: 0, Eligible: true})
		},
		"template_presence_signed_stats_and_float_format": func(f *disposalFactFrame) {
			row := f.inv.GearContext.Templates["907001"]
			row.Stats = []*inventoryv1.GearStat{{Type: 5, Value: -3}, {Type: math.MaxUint32}}
			row.Damages = []*inventoryv1.GearDamage{{Minimum: .1, Maximum: .3}}
			row.Resistances = []uint32{0, 1, 0, 0, 0, 0}
		},
		"missing_referenced_template_is_policy_unknown": func(f *disposalFactFrame) { delete(f.inv.GearContext.Templates, "907001") },
		"unknown_or_enhanced_traits_stay_factual": func(f *disposalFactFrame) {
			f.inv.Items[0].MetadataKnown = false
			f.inv.Items[0].GearFacts.HasEnhancements = true
			f.inv.InventoryBags[0].Slots[3].HasEnhancements = true
			f.inv.Equipment[4].DurabilityKnown = false
			f.world.SelfEquipment[4].DurabilityKnown = false
		},
		"empty_active_log": func(f *disposalFactFrame) { f.world.SelfQuestLog[0].QuestId = 0; f.journal.Entries = nil },
		"max_ids_and_canonical_uint64_revisions": func(f *disposalFactFrame) {
			f.inv.InventoryRevision, f.world.InventoryRevision = math.MaxUint64, math.MaxUint64
			f.inv.EquipmentRevision, f.world.SelfEquipmentRevision = math.MaxUint64-1, math.MaxUint64-1
			f.world.SelfQuestLogRevision, f.journal.QuestLogRevision = math.MaxUint64-2, math.MaxUint64-2
			f.world.SelfQuestLog[0].QuestId, f.journal.Entries[0].QuestId = math.MaxUint32, math.MaxUint32
		},
		"unused_large_or_malformed_extra_fields": func(f *disposalFactFrame) {
			f.inv.GearContext.PolicyError = strings.Repeat("x", 35000)
			f.inv.GearContext.Templates["907001"].Name = strings.Repeat("n", 35000)
			f.inv.GearContext.Templates["unused"] = &inventoryv1.GearTemplate{Damages: []*inventoryv1.GearDamage{{Maximum: float32(math.Inf(1))}}}
			f.journal.Entries[0].Title = strings.Repeat("t", 35000)
			f.world.Self.X = float32(math.NaN())
		},
		"shuffled_full_frames": func(f *disposalFactFrame) {
			for left, right := 0, 18; left < right; left, right = left+1, right-1 {
				f.inv.Equipment[left], f.inv.Equipment[right] = f.inv.Equipment[right], f.inv.Equipment[left]
			}
			for left, right := 0, 24; left < right; left, right = left+1, right-1 {
				f.world.SelfQuestLog[left], f.world.SelfQuestLog[right] = f.world.SelfQuestLog[right], f.world.SelfQuestLog[left]
			}
			f.inv.InventoryBags[0], f.inv.InventoryBags[4] = f.inv.InventoryBags[4], f.inv.InventoryBags[0]
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := disposalFrame()
			change(&f)
			before := []proto.Message{proto.Clone(f.inv), proto.Clone(f.world), proto.Clone(f.journal), proto.Clone(f.source)}
			actual, err := projectDisposalContext(f.inv, f.world, f.journal, f.source)
			if err != nil {
				t.Fatal(err)
			}
			if expected := disposalPythonContext(t, f); expected == nil || !reflect.DeepEqual(actual, expected) {
				a, _ := json.Marshal(actual)
				b, _ := json.Marshal(expected)
				t.Fatalf("Go/Python projection differs\nGo %s\nPython %s", a, b)
			}
			for index, current := range []proto.Message{f.inv, f.world, f.journal, f.source} {
				if !proto.Equal(before[index], current) {
					t.Fatal("projector mutated native facts", index)
				}
			}
			if _, found := actual["inventory"].(map[string]any)["equipment"]; found {
				t.Fatal("full equipment leaked instead of compact physical joins")
			}
			if _, found := actual["world"].(map[string]any)["selfQuestLog"]; found {
				t.Fatal("raw log leaked instead of covered sparse active rows")
			}
		})
	}
}

func TestDisposalFactsRejectMalformedFramesLikeProductionPython(t *testing.T) {
	cases := map[string]func(*disposalFactFrame){
		"partial_bags":        func(f *disposalFactFrame) { f.inv.InventoryBags = f.inv.InventoryBags[:4] },
		"duplicate_bags":      func(f *disposalFactFrame) { f.inv.InventoryBags[4].Bag = 0 },
		"unknown_bag":         func(f *disposalFactFrame) { f.inv.InventoryBags[0].ContentsKnown = false },
		"slot_count":          func(f *disposalFactFrame) { f.inv.InventoryBags[0].SlotCount = 15 },
		"duplicate_slots":     func(f *disposalFactFrame) { f.inv.InventoryBags[0].Slots[15].Slot = 0 },
		"empty_slot_metadata": func(f *disposalFactFrame) { f.inv.InventoryBags[0].Slots[0].MetadataKnown = true },
		"carried_alias": func(f *disposalFactFrame) {
			f.inv.InventoryBags[0].Slots[0] = proto.Clone(f.inv.InventoryBags[0].Slots[3]).(*worldv1.BackpackSlot)
			f.inv.InventoryBags[0].Slots[0].Slot = 0
		},
		"carried_namespace":            func(f *disposalFactFrame) { f.inv.InventoryBags[0].Slots[0].ItemGuid = 4 },
		"metadata_unknown_with_entry":  func(f *disposalFactFrame) { f.inv.InventoryBags[0].Slots[3].MetadataKnown = false },
		"source_mismatch":              func(f *disposalFactFrame) { f.source.ItemGuid++ },
		"partial_equipment":            func(f *disposalFactFrame) { f.inv.Equipment = f.inv.Equipment[:18] },
		"duplicate_equipment_slot":     func(f *disposalFactFrame) { f.inv.Equipment[18].Slot = 0 },
		"equipped_carried_alias":       func(f *disposalFactFrame) { f.inv.Equipment[4].ItemGuid = f.source.ItemGuid },
		"equipped_namespace":           func(f *disposalFactFrame) { f.inv.Equipment[4].ItemGuid = 4 },
		"equipped_empty_contradiction": func(f *disposalFactFrame) { f.inv.Equipment[0].Entry = 100 },
		"equipped_stack":               func(f *disposalFactFrame) { f.inv.Equipment[4].StackCount = 2 },
		"equipment_trait_changed":      func(f *disposalFactFrame) { f.world.SelfEquipment[4].HasEnhancements = true },
		"equipment_durability":         func(f *disposalFactFrame) { f.inv.Equipment[4].Durability = 41 },
		"duplicate_source_facts": func(f *disposalFactFrame) {
			f.inv.Items = append(f.inv.Items, proto.Clone(f.inv.Items[0]).(*inventoryv1.ItemEligibility))
		},
		"missing_source_facts": func(f *disposalFactFrame) { f.inv.Items = nil },
		"missing_destinations": func(f *disposalFactFrame) { f.inv.Items[0].GearFacts.Destinations = nil },
		"duplicate_destinations": func(f *disposalFactFrame) {
			f.inv.Items[0].GearFacts.Destinations = append(f.inv.Items[0].GearFacts.Destinations, proto.Clone(f.inv.Items[0].GearFacts.Destinations[0]).(*inventoryv1.GearDestination))
		},
		"destination_guid_changed": func(f *disposalFactFrame) { f.inv.Items[0].GearFacts.Destinations[0].ExpectedEquippedItemGuid++ },
		"destination_reason": func(f *disposalFactFrame) {
			f.inv.Items[0].GearFacts.Destinations[0].BlockedReason = strings.Repeat("界", 129)
		},
		"missing_template_context": func(f *disposalFactFrame) { f.inv.GearContext = nil },
		"too_many_stats": func(f *disposalFactFrame) {
			f.inv.GearContext.Templates["907001"].Stats = make([]*inventoryv1.GearStat, 11)
			for i := range f.inv.GearContext.Templates["907001"].Stats {
				f.inv.GearContext.Templates["907001"].Stats[i] = &inventoryv1.GearStat{}
			}
		},
		"nonfinite_damage": func(f *disposalFactFrame) {
			f.inv.GearContext.Templates["907001"].Damages = []*inventoryv1.GearDamage{{Maximum: float32(math.Inf(1))}}
		},
		"too_many_resistances":   func(f *disposalFactFrame) { f.inv.GearContext.Templates["907001"].Resistances = make([]uint32, 7) },
		"partial_log":            func(f *disposalFactFrame) { f.world.SelfQuestLog = f.world.SelfQuestLog[:24] },
		"duplicate_log_slots":    func(f *disposalFactFrame) { f.world.SelfQuestLog[24].Slot = 0 },
		"duplicate_active_quest": func(f *disposalFactFrame) { f.world.SelfQuestLog[1].QuestId = 99000 },
		"journal_failure_bound":  func(f *disposalFactFrame) { f.journal.Entries[0].Failure = strings.Repeat("界", 129) },
		"journal_target_bound":   func(f *disposalFactFrame) { f.journal.Entries[0].Targets[0].Kind = strings.Repeat("x", 33) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := disposalFrame()
			change(&f)
			if actual, err := projectDisposalContext(f.inv, f.world, f.journal, f.source); err == nil || actual != nil {
				t.Fatal("malformed facts projected", actual, err)
			}
			if expected := disposalPythonContext(t, f); expected != nil {
				t.Fatal("Python accepted structurally rejected facts", expected)
			}
		})
	}
}

func TestDisposalFactsRequireCurrentCacheBodyAndPositiveMatchingRevisions(t *testing.T) {
	cases := map[string]func(*disposalFactFrame){
		"cold":                    func(f *disposalFactFrame) { f.inv.InventoryKnown = false },
		"closing":                 func(f *disposalFactFrame) { f.inv.Closing = true },
		"missing_revision":        func(f *disposalFactFrame) { f.inv.Revision = 0 },
		"wrong_body":              func(f *disposalFactFrame) { f.world.Self.Guid++ },
		"absent_body":             func(f *disposalFactFrame) { f.world.Self.Present = false },
		"missing_session":         func(f *disposalFactFrame) { f.inv.SessionId = "" },
		"wrong_journal_session":   func(f *disposalFactFrame) { f.journal.SessionId = "foreign" },
		"stale_inventory":         func(f *disposalFactFrame) { f.world.InventoryRevision++ },
		"stale_equipment":         func(f *disposalFactFrame) { f.world.SelfEquipmentRevision++ },
		"stale_journal":           func(f *disposalFactFrame) { f.journal.QuestLogRevision++ },
		"zero_inventory":          func(f *disposalFactFrame) { f.world.InventoryRevision = 0; f.inv.InventoryRevision = 0 },
		"zero_equipment":          func(f *disposalFactFrame) { f.world.SelfEquipmentRevision = 0; f.inv.EquipmentRevision = 0 },
		"zero_quest":              func(f *disposalFactFrame) { f.world.SelfQuestLogRevision = 0; f.journal.QuestLogRevision = 0 },
		"world_inventory_unknown": func(f *disposalFactFrame) { f.world.InventoryKnown = false },
		"world_equipment_unknown": func(f *disposalFactFrame) { f.world.SelfEquipmentKnown = false },
		"world_quest_unknown":     func(f *disposalFactFrame) { f.world.SelfQuestLogKnown = false },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := disposalFrame()
			change(&f)
			for _, project := range []func(*inventoryv1.State, *worldv1.SnapshotResponse, *questsv1.JournalResponse, *inventoryv1.ItemRef) (map[string]any, error){projectDisposalContext, projectDisposalOwnershipContext} {
				if actual, err := project(f.inv, f.world, f.journal, f.source); err == nil || actual != nil {
					t.Fatal("cold/stale/foreign observations projected", actual, err)
				}
			}
		})
	}
	for _, f := range []disposalFactFrame{{}, {inv: &inventoryv1.State{}}} {
		if actual, err := projectDisposalContext(f.inv, f.world, f.journal, f.source); err == nil || actual != nil {
			t.Fatal("missing frames accepted")
		}
	}
}

func TestDisposalOwnershipContextDoesNotInventGearRequirementsForMisc(t *testing.T) {
	f := disposalFrame()
	f.inv.Items[0].GearFacts.Destinations = nil
	f.inv.GearContext = nil
	actual, err := projectDisposalOwnershipContext(f.inv, f.world, f.journal, f.source)
	if err != nil || actual["carriedSource"].(map[string]any)["itemGuid"] != strconv.FormatUint(f.source.ItemGuid, 10) {
		t.Fatal(actual, err)
	}
	if _, present := actual["equipmentCandidates"]; present {
		t.Fatal("ownership mode invented gear destinations")
	}
	if _, present := actual["facts"].(map[string]any)["physicalDestinationsKnown"]; present {
		t.Fatal("ownership mode invented physical coverage")
	}
	if actual, err := projectDisposalContext(f.inv, f.world, f.journal, f.source); err == nil || actual != nil {
		t.Fatal("gray mode lost actual destination prerequisite")
	}
}

func TestDisposalFactsKeepFullFrameValidationWithCompactMaximumInventory(t *testing.T) {
	f := disposalFrame()
	nextGUID := uint64(0x4000000000001000)
	for _, bag := range f.inv.InventoryBags {
		if bag.Bag != 0 {
			bag.ContainerGuid = nextGUID
			nextGUID++
			bag.Entry = 41599
			bag.MetadataKnown = true
			bag.SlotCount = 36
			bag.Slots = nil
		}
		for slot := uint32(0); slot < bag.SlotCount; slot++ {
			if bag.Bag == 0 && slot == f.source.Slot {
				continue
			}
			row := &worldv1.BackpackSlot{Bag: bag.Bag, Slot: slot, ItemGuid: nextGUID, Entry: 100, StackCount: 1, MetadataKnown: true}
			nextGUID++
			if bag.Bag == 0 {
				bag.Slots[slot] = row
			} else {
				bag.Slots = append(bag.Slots, row)
			}
		}
	}
	for slot, row := range f.inv.Equipment {
		if slot != 4 {
			row.ItemGuid = nextGUID
			nextGUID++
			row.Entry = uint32(1000 + slot)
			row.StackCount = 1
			row.MetadataKnown = true
		}
		f.world.SelfEquipment[slot] = proto.Clone(row).(*worldv1.EquipmentSlotState)
	}
	actual, err := projectDisposalContext(f.inv, f.world, f.journal, f.source)
	if err != nil {
		t.Fatal(err)
	}
	if expected := disposalPythonContext(t, f); !reflect.DeepEqual(actual, expected) {
		t.Fatal("full160/full19 projection differs from production projector")
	}
	encoded, _ := json.Marshal(actual)
	if len(encoded) >= modcb.DisposalPolicyMaxBytes || len(actual["equipmentCandidates"].([]any)) != 1 {
		t.Fatal("maximum raw frame lost compact projection", len(encoded))
	}
	// A duplicate hidden in the final physical slot must still be rejected.
	f.inv.InventoryBags[4].Slots[35].ItemGuid = f.source.ItemGuid
	if actual, err := projectDisposalContext(f.inv, f.world, f.journal, f.source); err == nil || actual != nil {
		t.Fatal("unprojected full-frame alias escaped validation")
	}
}

func TestDisposalFactsOutputIsDetachedAndBudgetIsNotRaised(t *testing.T) {
	f := disposalFrame()
	actual, err := projectDisposalContext(f.inv, f.world, f.journal, f.source)
	if err != nil {
		t.Fatal(err)
	}
	before := proto.Clone(f.inv)
	actual["carriedSource"].(map[string]any)["entry"] = float64(1)
	actual["equipmentCandidates"].([]any)[0].(map[string]any)["equipment"].(map[string]any)["entry"] = float64(2)
	actual["inventory"].(map[string]any)["gearContext"].(map[string]any)["templates"].(map[string]any)["907001"].(map[string]any)["armor"] = float64(3)
	if !proto.Equal(before, f.inv) {
		t.Fatal("returned map aliases native frames")
	}
	for slot := uint32(0); slot < 25; slot++ {
		f.world.SelfQuestLog[slot].QuestId = slot + 1
		entry := &questsv1.QuestJournalEntry{QuestId: slot + 1, Slot: slot, TemplateKnown: true, LogStateKnown: true, ItemProtectionKnown: true}
		for target := 0; target < 32; target++ {
			entry.Targets = append(entry.Targets, &questsv1.QuestObjective{Kind: strings.Repeat("x", 32), TargetId: 100, RequiredCount: 1})
		}
		if slot == 0 {
			f.journal.Entries = nil
		}
		f.journal.Entries = append(f.journal.Entries, entry)
	}
	if actual, err := projectDisposalContext(f.inv, f.world, f.journal, f.source); err == nil || actual != nil {
		t.Fatal("context exceeded existing32KiB budget", actual)
	}
}
