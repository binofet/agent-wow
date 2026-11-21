package buddyinventory

import "testing"

func TestGearPreviewDistinguishesPublicOccupiedEmptyAndUnknownSlots(t *testing.T) {
	item := func(id uint32, armor int) map[string]any {
		return map[string]any{"itemId": id, "available": true, "itemClass": 4,
			"inventoryType": 5, "armor": armor, "armorComparisonKnown": true}
	}
	// This is the owner's public addon shape. It observes slot, entry and
	// enhancement context, without exposing an owned item's private GUID.
	ownerSlot := func(entry uint32) map[string]any {
		return map[string]any{"slot": 4, "slotKnown": true, "entry": entry,
			"metadataKnown": true, "enhancementsKnown": true, "durabilityKnown": true}
	}
	for _, tc := range []struct {
		name    string
		facts   map[string]any
		current map[string]any
		upgrade bool
		score   float64
		reason  string
	}{
		{"occupied better than candidate", ownerSlot(90002), item(90002, 20), false, -5, "comparison_not_an_upgrade"},
		{"occupied worse than candidate", ownerSlot(90002), item(90002, 10), true, 5, ""},
		{"occupied identical stats", ownerSlot(90002), item(90002, 15), false, 0, ""},
		{"occupied template unavailable", ownerSlot(90002), nil, false, 0, "displaced_item_template_pending"},
		{"occupied template belongs to another entry", ownerSlot(90003), item(90002, 10), false, 0, "displaced_item_template_mismatch"},
		{"slot not observed", map[string]any{"enhancementsKnown": true}, item(90002, 10), false, 0, "equipment_slot_observation_pending"},
		{"explicitly unknown slot", map[string]any{"slotKnown": false, "entry": 90002, "enhancementsKnown": true}, item(90002, 10), false, 0, "equipment_slot_observation_pending"},
		{"unknown slot with zero guid", map[string]any{"itemGuid": "0"}, nil, false, 0, "equipment_slot_observation_pending"},
		{"known empty addon slot", ownerSlot(0), nil, true, 1, "empty_equipment_slot"},
		{"known empty sparse private slot", map[string]any{"slot": 4, "slotKnown": true}, nil, true, 1, "empty_equipment_slot"},
		{"known empty explicit private slot", map[string]any{"slot": 4, "slotKnown": true, "itemGuid": "0"}, nil, true, 1, "empty_equipment_slot"},
		{"contradictory zero guid and occupied entry", map[string]any{"slotKnown": true, "itemGuid": "0", "entry": 90002}, item(90002, 10), false, 0, "equipment_slot_observation_pending"},
		{"occupied private guid compatibility", map[string]any{"itemGuid": "4611686018427387906", "enhancementsKnown": true}, item(90002, 10), true, 5, ""},
		{"occupied enhancement unknown", map[string]any{"slotKnown": true, "entry": 90002}, item(90002, 10), false, 0, "enhancement_context_pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := runGearPolicy(t, map[string]any{"mode": "preview", "candidate": item(90001, 15),
				"candidateFacts": map[string]any{"enhancementsKnown": true}, "current": tc.current,
				"currentFacts": tc.facts}, false)
			if out["upgrade"] != tc.upgrade || out["scoreGain"] != tc.score || out["reason"] != tc.reason {
				t.Fatal("shared preview confused slot occupancy or bypassed its actual baseline", out)
			}
		})
	}
}
