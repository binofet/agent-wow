package modrt

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"unicode/utf8"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// projectDisposalContext proves only the consistency of typed observations.
// Lua still chooses the armor slot, protects quest items, compares gear and
// decides disposal. This function does not refresh the cache or query peers.
func projectDisposalContext(inv *inventoryv1.State, world *worldv1.SnapshotResponse, journal *questsv1.JournalResponse, source *inventoryv1.ItemRef) (map[string]any, error) {
	context, equipment, err := disposalOwnedFrames(inv, world, journal, source)
	if err != nil {
		return nil, err
	}

	if len(inv.Items) > 160 {
		return nil, errors.New("bounded owned item facts required")
	}
	var selected *inventoryv1.ItemEligibility
	for _, row := range inv.Items {
		if row == nil || row.Item == nil {
			return nil, errors.New("typed owned item reference required")
		}
		if disposalSameSource(row.Item, source) {
			if selected != nil || row.GearFacts == nil {
				return nil, errors.New("one exact owned item gear facts row required")
			}
			selected = row
		}
	}
	if selected == nil || len(selected.GearFacts.Destinations) == 0 || len(selected.GearFacts.Destinations) > 19 {
		return nil, errors.New("actual physical gear destinations required")
	}
	candidates := make([]any, 0, len(selected.GearFacts.Destinations))
	referenced := map[uint32]bool{}
	var seenDestinations [19]bool
	for _, destination := range selected.GearFacts.Destinations {
		if destination == nil || destination.EquipmentSlot >= 19 || seenDestinations[destination.EquipmentSlot] ||
			destination.ExpectedEquippedItemGuid != equipment[destination.EquipmentSlot].ItemGuid ||
			utf8.RuneCountInString(destination.BlockedReason) > 128 || !utf8.ValidString(destination.BlockedReason) {
			return nil, errors.New("exact unique physical gear destination join required")
		}
		seenDestinations[destination.EquipmentSlot] = true
		row := equipment[destination.EquipmentSlot]
		encoded, err := disposalJSONFields(row, "slot", "entry", "stackCount", "durability", "maxDurability", "slotKnown", "metadataKnown", "enhancementsKnown", "hasEnhancements", "durabilityKnown", "itemGuid")
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, map[string]any{
			"destination": map[string]any{"equipmentSlot": float64(destination.EquipmentSlot),
				"expectedEquippedItemGuid": strconv.FormatUint(destination.ExpectedEquippedItemGuid, 10),
				"eligible":                 destination.Eligible, "blockedReason": destination.BlockedReason},
			"equipment": encoded,
		})
		referenced[row.Entry] = true
	}
	// Proto JSON omits an empty templates map; the Python factual projector
	// likewise requires an observed templates object before selecting its rows.
	if inv.GearContext == nil || len(inv.GearContext.Templates) == 0 {
		return nil, errors.New("cached gear template facts required")
	}
	templates := map[string]any{}
	for entry := range referenced {
		key := strconv.FormatUint(uint64(entry), 10)
		if row, exists := inv.GearContext.Templates[key]; entry != 0 && exists {
			value, err := disposalTemplateFacts(row)
			if err != nil {
				return nil, err
			}
			templates[key] = value
		}
	}
	inventory := context["inventory"].(map[string]any)
	facts, err := disposalJSONFields(&inventoryv1.GearItemFacts{EnhancementsKnown: selected.GearFacts.EnhancementsKnown,
		HasEnhancements: selected.GearFacts.HasEnhancements, DurabilityKnown: selected.GearFacts.DurabilityKnown,
		Durability: selected.GearFacts.Durability, MaxDurability: selected.GearFacts.MaxDurability},
		"enhancementsKnown", "hasEnhancements", "durabilityKnown", "durability", "maxDurability")
	if err != nil {
		return nil, err
	}
	item := map[string]any{"item": disposalSourceReference(source), "gearFacts": facts}
	if selected.MetadataKnown {
		item["metadataKnown"] = true
	}
	inventory["items"] = []any{item}
	inventory["gearContext"] = map[string]any{"templates": templates}
	context["equipmentCandidates"] = candidates
	context["facts"].(map[string]any)["physicalDestinationsKnown"] = true
	return boundedDisposalContext(context)
}

// projectDisposalOwnershipContext uses the same complete ownership frames for
// modes which do not consume physical gear destinations or gear templates.
// The requested fact shape never depends on the observed item's class.
func projectDisposalOwnershipContext(inv *inventoryv1.State, world *worldv1.SnapshotResponse, journal *questsv1.JournalResponse, source *inventoryv1.ItemRef) (map[string]any, error) {
	context, _, err := disposalOwnedFrames(inv, world, journal, source)
	if err != nil {
		return nil, err
	}
	return boundedDisposalContext(context)
}

func disposalOwnedFrames(inv *inventoryv1.State, world *worldv1.SnapshotResponse, journal *questsv1.JournalResponse, source *inventoryv1.ItemRef) (map[string]any, [19]*worldv1.EquipmentSlotState, error) {
	if inv == nil || world == nil || journal == nil || source == nil || world.Self == nil ||
		inv.SessionId == "" || utf8.RuneCountInString(inv.SessionId) > 128 || inv.Closing || inv.Revision == 0 ||
		inv.SelfGuid == 0 || world.Self.Guid != inv.SelfGuid || !world.Self.Present ||
		!inv.InventoryKnown || !world.InventoryKnown || inv.InventoryRevision == 0 || inv.InventoryRevision != world.InventoryRevision ||
		!inv.EquipmentKnown || !world.SelfEquipmentKnown || inv.EquipmentRevision == 0 || inv.EquipmentRevision != world.SelfEquipmentRevision ||
		!world.SelfQuestLogKnown || world.SelfQuestLogRevision == 0 ||
		journal.SessionId != inv.SessionId || journal.QuestLogRevision != world.SelfQuestLogRevision ||
		!disposalItemGUID(source.ItemGuid) || source.Entry == 0 || source.StackCount == 0 {
		return nil, [19]*worldv1.EquipmentSlotState{}, errors.New("current disposal body and matching cache/world/journal revisions required")
	}
	carried, guids, err := disposalCarriedFrame(inv.InventoryBags, source)
	if err != nil {
		return nil, [19]*worldv1.EquipmentSlotState{}, err
	}
	equipment, err := disposalEquipmentFrames(inv.Equipment, world.SelfEquipment, guids)
	if err != nil {
		return nil, [19]*worldv1.EquipmentSlotState{}, err
	}
	active, err := disposalActiveQuestRows(world.SelfQuestLog)
	if err != nil {
		return nil, equipment, err
	}
	log, err := disposalJournalFacts(journal)
	if err != nil {
		return nil, equipment, err
	}
	inventory, err := disposalJSONFields(&inventoryv1.State{SessionId: inv.SessionId, SelfGuid: inv.SelfGuid,
		InventoryKnown: inv.InventoryKnown, InventoryRevision: inv.InventoryRevision,
		EquipmentKnown: inv.EquipmentKnown, EquipmentRevision: inv.EquipmentRevision},
		"sessionId", "selfGuid", "inventoryKnown", "inventoryRevision", "equipmentKnown", "equipmentRevision")
	if err != nil {
		return nil, equipment, err
	}
	raw, err := disposalJSONFields(&worldv1.SnapshotResponse{InventoryKnown: world.InventoryKnown, InventoryRevision: world.InventoryRevision,
		SelfEquipmentKnown: world.SelfEquipmentKnown, SelfEquipmentRevision: world.SelfEquipmentRevision,
		SelfQuestLogKnown: world.SelfQuestLogKnown, SelfQuestLogRevision: world.SelfQuestLogRevision},
		"inventoryKnown", "inventoryRevision", "selfEquipmentKnown", "selfEquipmentRevision", "selfQuestLogKnown", "selfQuestLogRevision")
	if err != nil {
		return nil, equipment, err
	}
	raw["self"] = map[string]any{"guid": strconv.FormatUint(world.Self.Guid, 10), "present": true}
	owned, err := disposalJSONFields(carried, "metadataKnown", "enhancementsKnown", "hasEnhancements", "durabilityKnown", "durability", "maxDurability")
	if err != nil {
		return nil, equipment, err
	}
	for key, value := range disposalSourceReference(source) {
		owned[key] = value
	}
	return map[string]any{"inventory": inventory, "world": raw, "journal": log,
		"source": disposalSourceReference(source), "carriedSource": owned, "activeQuestRows": active,
		"facts": map[string]any{"inventoryLayoutKnown": true, "equipmentFramesMatch": true, "questLogFrameKnown": true}}, equipment, nil
}

func boundedDisposalContext(context map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(context)
	if err != nil || len(encoded) > modcb.DisposalPolicyMaxBytes {
		return nil, errors.New("bounded compact disposal context required")
	}
	return context, nil
}

func disposalItemGUID(guid uint64) bool { return guid>>48 == 0x4000 && guid&0x0000ffffffffffff != 0 }

func disposalSameSource(a, b *inventoryv1.ItemRef) bool {
	return a != nil && b != nil && a.Bag == b.Bag && a.Slot == b.Slot && a.ItemGuid == b.ItemGuid && a.Entry == b.Entry && a.StackCount == b.StackCount
}

func disposalSourceReference(row *inventoryv1.ItemRef) map[string]any {
	return map[string]any{"bag": float64(row.Bag), "slot": float64(row.Slot), "itemGuid": strconv.FormatUint(row.ItemGuid, 10), "entry": float64(row.Entry), "stackCount": float64(row.StackCount)}
}

func disposalCarriedFrame(rows []*worldv1.InventoryBag, source *inventoryv1.ItemRef) (*worldv1.BackpackSlot, map[uint64]bool, error) {
	if len(rows) != 5 {
		return nil, nil, errors.New("complete five-bag frame required")
	}
	var bags [5]*worldv1.InventoryBag
	guids := map[uint64]bool{}
	total := 0
	for _, row := range rows {
		if row == nil || row.Bag >= 5 || bags[row.Bag] != nil || !row.SlotKnown || !row.ContentsKnown || row.SlotCount > 36 || len(row.Slots) != int(row.SlotCount) || row.ContainerGuid != 0 && !disposalItemGUID(row.ContainerGuid) {
			return nil, nil, errors.New("complete unique bag frame required")
		}
		if row.Bag == 0 && (row.ContainerGuid != 0 || row.Entry != 0 || row.MetadataKnown || row.SlotCount != 16) ||
			row.Bag != 0 && row.ContainerGuid == 0 && (row.Entry != 0 || row.MetadataKnown || row.SlotCount != 0) ||
			row.ContainerGuid != 0 && (guids[row.ContainerGuid] || !row.MetadataKnown || row.Entry == 0 || row.SlotCount == 0) {
			return nil, nil, errors.New("bag container identity inconsistent")
		}
		if row.ContainerGuid != 0 {
			guids[row.ContainerGuid] = true
		}
		bags[row.Bag] = row
		total += int(row.SlotCount)
	}
	if total > 160 {
		return nil, nil, errors.New("carried slot count bound")
	}
	var selected *worldv1.BackpackSlot
	for _, bag := range bags {
		var seen [36]bool
		for _, row := range bag.Slots {
			if row == nil || row.Bag != bag.Bag || row.Slot >= bag.SlotCount || seen[row.Slot] || row.ItemGuid != 0 && !disposalItemGUID(row.ItemGuid) {
				return nil, nil, errors.New("complete unique carried slots required")
			}
			seen[row.Slot] = true
			if row.ItemGuid == 0 && (row.Entry != 0 || row.StackCount != 0 || row.MetadataKnown) ||
				row.ItemGuid != 0 && (guids[row.ItemGuid] || row.MetadataKnown && (row.Entry == 0 || row.StackCount == 0) || !row.MetadataKnown && (row.Entry != 0 || row.StackCount != 0)) {
				return nil, nil, errors.New("owned carried slot identity inconsistent")
			}
			if row.ItemGuid != 0 {
				guids[row.ItemGuid] = true
			}
			if row.Bag == source.Bag && row.Slot == source.Slot {
				if row.ItemGuid != source.ItemGuid || row.Entry != source.Entry || row.StackCount != source.StackCount {
					return nil, nil, errors.New("exact observed source changed")
				}
				selected = row
			}
		}
	}
	if selected == nil {
		return nil, nil, errors.New("observed source location required")
	}
	return selected, guids, nil
}

func disposalEquipmentFrames(first, second []*worldv1.EquipmentSlotState, carried map[uint64]bool) ([19]*worldv1.EquipmentSlotState, error) {
	var frames [2][19]*worldv1.EquipmentSlotState
	for index, rows := range [][]*worldv1.EquipmentSlotState{first, second} {
		if len(rows) != 19 {
			return frames[0], errors.New("complete nineteen-slot equipment frame required")
		}
		guids := map[uint64]bool{}
		for _, row := range rows {
			if row == nil || row.Slot >= 19 || frames[index][row.Slot] != nil || !row.SlotKnown ||
				row.ItemGuid == 0 && (row.Entry != 0 || row.StackCount != 0 || row.MetadataKnown) ||
				row.ItemGuid != 0 && (!disposalItemGUID(row.ItemGuid) || carried[row.ItemGuid] || guids[row.ItemGuid] || !row.MetadataKnown || row.Entry == 0 || row.StackCount != 1) ||
				row.DurabilityKnown && row.Durability > row.MaxDurability {
				return frames[0], errors.New("complete unique equipment identity and traits required")
			}
			frames[index][row.Slot], guids[row.ItemGuid] = row, true
		}
	}
	for slot, a := range frames[0] {
		b := frames[1][slot]
		if a.ItemGuid != b.ItemGuid || a.Entry != b.Entry || a.StackCount != b.StackCount || a.MetadataKnown != b.MetadataKnown ||
			a.EnhancementsKnown != b.EnhancementsKnown || a.HasEnhancements != b.HasEnhancements ||
			a.DurabilityKnown != b.DurabilityKnown || a.Durability != b.Durability || a.MaxDurability != b.MaxDurability {
			return frames[0], errors.New("cached and current equipment frames changed")
		}
	}
	return frames[0], nil
}

func disposalActiveQuestRows(rows []*worldv1.QuestLogEntry) ([]any, error) {
	if len(rows) != 25 {
		return nil, errors.New("complete raw quest log required")
	}
	var seen [25]bool
	quests := map[uint32]bool{}
	active := []any{}
	for _, row := range rows {
		if row == nil || row.Slot >= 25 || seen[row.Slot] || row.QuestId != 0 && quests[row.QuestId] {
			return nil, errors.New("unique raw quest slots and active IDs required")
		}
		seen[row.Slot] = true
		if row.QuestId != 0 {
			quests[row.QuestId] = true
			active = append(active, map[string]any{"slot": float64(row.Slot), "questId": float64(row.QuestId)})
		}
	}
	return active, nil
}

func disposalTemplateFacts(row *inventoryv1.GearTemplate) (map[string]any, error) {
	if row == nil || len(row.Stats) > 10 || len(row.Resistances) > 6 || len(row.Damages) > 2 {
		return nil, errors.New("bounded referenced gear template required")
	}
	// Ignore unused template fields before proto JSON conversion, just as the
	// Python projector does. No name/quality/class-profile decision is made here.
	value, err := disposalJSONFields(&inventoryv1.GearTemplate{ItemId: row.ItemId, Available: row.Available,
		ItemClass: row.ItemClass, Subclass: row.Subclass, InventoryType: row.InventoryType, Armor: row.Armor, DelayMs: row.DelayMs,
		Flags2: row.Flags2, ScalingDistribution: row.ScalingDistribution, ScalingValue: row.ScalingValue,
		ArmorComparisonKnown: row.ArmorComparisonKnown, HasSpell: row.HasSpell, Resistances: row.Resistances},
		"itemId", "available", "itemClass", "subclass", "inventoryType", "armor", "delayMs", "flags2", "scalingDistribution", "scalingValue", "armorComparisonKnown", "hasSpell", "resistances")
	if err != nil {
		return nil, err
	}
	stats, damages := []any{}, []any{}
	for _, stat := range row.Stats {
		if stat == nil {
			return nil, errors.New("typed referenced gear stat required")
		}
		stats = append(stats, map[string]any{"type": float64(stat.Type), "value": float64(stat.Value)})
	}
	for _, damage := range row.Damages {
		if damage == nil || math.IsNaN(float64(damage.Minimum)) || math.IsInf(float64(damage.Minimum), 0) || math.IsNaN(float64(damage.Maximum)) || math.IsInf(float64(damage.Maximum), 0) || math.Abs(float64(damage.Minimum)) > 1<<53-1 || math.Abs(float64(damage.Maximum)) > 1<<53-1 {
			return nil, errors.New("finite exact-range damage facts required")
		}
		// Use proto JSON's float formatting, rather than widening a float32 and
		// exposing binary rounding which the Python native-frame path never saw.
		encoded, err := disposalJSONFields(damage, "school", "minimum", "maximum")
		if err != nil {
			return nil, err
		}
		for _, key := range []string{"school", "minimum", "maximum"} {
			if _, present := encoded[key]; !present {
				encoded[key] = float64(0)
			}
		}
		damages = append(damages, encoded)
	}
	value["stats"], value["damages"] = stats, damages
	return value, nil
}

func disposalJournalFacts(journal *questsv1.JournalResponse) (map[string]any, error) {
	if len(journal.Entries) > 25 {
		return nil, errors.New("bounded typed journal entries required")
	}
	log := map[string]any{"sessionId": journal.SessionId, "questLogRevision": strconv.FormatUint(journal.QuestLogRevision, 10)}
	entries := []any{}
	for _, row := range journal.Entries {
		if row == nil || len(row.Targets) > 32 || utf8.RuneCountInString(row.Failure) > 128 {
			return nil, errors.New("bounded typed journal entry required")
		}
		entry, err := disposalJSONFields(&questsv1.QuestJournalEntry{QuestId: row.QuestId, Slot: row.Slot, TemplateKnown: row.TemplateKnown,
			Failure: row.Failure, LogStateKnown: row.LogStateKnown, ItemProtectionKnown: row.ItemProtectionKnown},
			"questId", "slot", "templateKnown", "failure", "logStateKnown", "itemProtectionKnown")
		if err != nil {
			return nil, err
		}
		targets := []any{}
		for _, target := range row.Targets {
			if target == nil || utf8.RuneCountInString(target.Kind) > 32 || !utf8.ValidString(target.Kind) {
				return nil, errors.New("bounded typed quest target required")
			}
			targets = append(targets, map[string]any{"kind": target.Kind, "targetId": float64(target.TargetId), "requiredCount": float64(target.RequiredCount)})
		}
		entry["targets"] = targets
		entries = append(entries, entry)
	}
	log["entries"] = entries
	return log, nil
}

func disposalJSONFields(message proto.Message, names ...string) (map[string]any, error) {
	encoded, err := protojson.Marshal(message)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return nil, err
	}
	fields := map[string]any{}
	for _, name := range names {
		if value, present := raw[name]; present {
			fields[name] = value
		}
	}
	return fields, nil
}
