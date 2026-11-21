package behavior

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
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
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const disposalArmorSourceGUID = "4611686018427387905"
const disposalArmorWornGUID = "4611686018427387906"

func disposalArmorComposition(t *testing.T, gearEdit func(string) string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "gear.lua"))
	if err != nil {
		t.Fatal(err)
	}
	gear := string(body)
	if gearEdit != nil {
		gear = gearEdit(gear)
	}
	return "local gear=(function()\n" + gear + "\nend)()\nlocal disposal=(function()\n" + disposalPolicySource(t) + `
end)()
return {decide=function(input) return disposal.classify(input.mode,input.item,input.context,gear) end}`
}

func disposalArmorFrame(t *testing.T) map[string]any {
	t.Helper()
	item := disposalTemplate(t, true)
	item["armor"], item["armorComparisonKnown"] = 20, true
	item["stats"] = []any{}
	source := map[string]any{"bag": 0, "slot": 3, "itemGuid": disposalArmorSourceGUID, "entry": 907000, "stackCount": 1}
	facts := map[string]any{"enhancementsKnown": true, "hasEnhancements": false, "durabilityKnown": true, "durability": 20, "maxDurability": 22}
	bags := []any{}
	for bag := 0; bag < 5; bag++ {
		slots := []any{}
		if bag == 0 {
			for slot := 0; slot < 16; slot++ {
				slots = append(slots, map[string]any{"bag": bag, "slot": slot, "itemGuid": "0"})
			}
		}
		bags = append(bags, map[string]any{"bag": bag, "slotKnown": true, "contentsKnown": true, "containerGuid": "0", "slotCount": len(slots), "slots": slots})
	}
	carried := map[string]any{}
	for k, v := range source {
		carried[k] = v
	}
	carried["metadataKnown"] = true
	for k, v := range facts {
		carried[k] = v
	}
	// Inventory.gearFactsLocked reports the physical chest destination even
	// when actual_class_race_level_required blocks equipping this source.
	// A retention comparison must not filter it by the Eligible bit.
	facts["destinations"] = []any{map[string]any{"equipmentSlot": 4,
		"expectedEquippedItemGuid": disposalArmorWornGUID, "eligible": false,
		"blockedReason": "actual_class_race_level_required"}}
	bags[0].(map[string]any)["slots"].([]any)[3] = carried
	equipment := []any{}
	for slot := 0; slot < 19; slot++ {
		equipment = append(equipment, map[string]any{"slot": slot, "slotKnown": true, "itemGuid": "0"})
	}
	equipment[4] = map[string]any{"slot": 4, "slotKnown": true, "itemGuid": disposalArmorWornGUID, "metadataKnown": true, "entry": 907001, "stackCount": 1, "enhancementsKnown": true, "hasEnhancements": false, "durabilityKnown": true, "durability": 30, "maxDurability": 40}
	log := []any{}
	for slot := 0; slot < 25; slot++ {
		log = append(log, map[string]any{"slot": slot, "questId": 0})
	}
	log[7].(map[string]any)["questId"] = 99000 // Synthetic complete typed journal, not a realm quest claim.
	worn := map[string]any{"itemId": 907001, "available": true, "itemClass": 4, "subclass": 1, "inventoryType": 5, "quality": 1, "armor": 25, "armorComparisonKnown": true, "stats": []any{}}
	inventory := map[string]any{"sessionId": "armor-session", "selfGuid": "4", "inventoryKnown": true, "inventoryRevision": "11", "inventoryBags": bags, "equipmentKnown": true, "equipmentRevision": "12", "equipment": equipment,
		"items": []any{map[string]any{"item": source, "metadataKnown": true, "gearFacts": facts}}, "gearContext": map[string]any{"templates": map[string]any{"907001": worn}}}
	world := map[string]any{"self": map[string]any{"guid": "4", "present": true}, "inventoryKnown": true, "inventoryRevision": "11", "selfEquipmentKnown": true, "selfEquipmentRevision": "12", "selfEquipment": equipment, "selfQuestLogKnown": true, "selfQuestLogRevision": "13", "selfQuestLog": log}
	journal := map[string]any{"sessionId": "armor-session", "questLogRevision": "13", "entries": []any{map[string]any{"questId": 99000, "slot": 7, "templateKnown": true, "itemProtectionKnown": true, "logStateKnown": true, "targets": []any{}}}}
	frame := map[string]any{"mode": "gray_armor_destroy", "item": item, "context": map[string]any{"inventory": inventory, "world": world, "journal": journal, "source": source}}
	// Use actual protojson omission rules. Complete zero-valued message rows
	// remain present; omitted scalar zero is not an invented unknown field.
	for key, message := range map[string]proto.Message{"inventory": &inventoryv1.State{}, "world": &worldv1.SnapshotResponse{}, "journal": &questsv1.JournalResponse{}, "source": &inventoryv1.ItemRef{}} {
		context := frame["context"].(map[string]any)
		context[key] = disposalArmorProtoJSON(t, context[key], message)
	}
	frame["item"] = disposalArmorProtoJSON(t, frame["item"], &tradev1.ItemDetails{})
	return disposalArmorClone(t, frame)
}

func disposalArmorProtoJSON(t *testing.T, value any, message proto.Message) map[string]any {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := protojson.Unmarshal(body, message); err != nil {
		t.Fatal(err)
	}
	body, err = protojson.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func disposalArmorClone(t *testing.T, v map[string]any) map[string]any {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err = json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func disposalArmorContext(v map[string]any) map[string]any { return v["context"].(map[string]any) }
func disposalArmorInventory(v map[string]any) map[string]any {
	return disposalArmorContext(v)["inventory"].(map[string]any)
}
func disposalArmorWorld(v map[string]any) map[string]any {
	return disposalArmorContext(v)["world"].(map[string]any)
}
func disposalArmorJournal(v map[string]any) map[string]any {
	return disposalArmorContext(v)["journal"].(map[string]any)
}
func disposalArmorSelected(v map[string]any) map[string]any {
	return disposalArmorInventory(v)["items"].([]any)[0].(map[string]any)
}
func disposalArmorWorn(v map[string]any) map[string]any {
	return disposalArmorInventory(v)["gearContext"].(map[string]any)["templates"].(map[string]any)["907001"].(map[string]any)
}
func disposalArmorEquipment(v map[string]any, world bool) []any {
	if world {
		return disposalArmorWorld(v)["selfEquipment"].([]any)
	}
	return disposalArmorInventory(v)["equipment"].([]any)
}
func disposalArmorResult(t *testing.T, r *Runtime, frame map[string]any, want string) map[string]any {
	t.Helper()
	var out map[string]any
	// Full native frames go through the real mechanical projector. Already
	// projected frames are also tested directly to cover the Lua fact fences.
	if context, ok := frame["context"].(map[string]any); frame["mode"] == "gray_armor_destroy" && ok {
		if inventory, ok := context["inventory"].(map[string]any); ok && inventory["inventoryBags"] != nil {
			projected := disposalArmorCapture(t, frame)
			if projected.Frame == nil {
				if projected.Result["status"] != "unknown" || projected.Source != "" {
					t.Fatal("invalid raw context did not stop before evaluation", projected)
				}
				out = projected.Result
			} else {
				frame = projected.Frame
			}
		}
	}
	if out == nil {
		var err error
		before := r.Status()
		started := time.Now()
		out, err = r.Evaluate(frame)
		elapsed, status := time.Since(started), r.Status()
		if err != nil || status.Failures > before.Failures ||
			status.QuarantinedRevision != "" && status.QuarantinedRevision != before.QuarantinedRevision {
			t.Fatalf("Evaluate failed after %s (configured limit %s); Runtime.Status=%+v: %v", elapsed, r.evaluationBudget(), status, err)
		}
	}
	return disposalArmorAssertResult(t, out, want)
}

func disposalArmorAssertResult(t *testing.T, out map[string]any, want string) map[string]any {
	t.Helper()
	if want == "retained" {
		if out["status"] != "keep" && out["status"] != "unknown" {
			t.Fatalf("unsafe disposal: %v", out)
		}
	} else if out["status"] != want {
		t.Fatalf("got %v, want %s", out, want)
	}
	if reason, ok := out["reason"].(string); !ok || reason == "" || len(reason) > 128 {
		t.Fatalf("invalid result %v", out)
	}
	return out
}

// Healthy semantic matrices use an explicit budget. Product-default execution
// remains a separate proof through the artifact-validated production helper.
func disposalArmorSemanticRuntime(t *testing.T, source string) *Runtime {
	t.Helper()
	r, err := NewWithOptions(SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	load(t, r, source)
	return r
}

func disposalArmorRuntime(t *testing.T) *Runtime {
	t.Helper()
	return disposalArmorSemanticRuntime(t, disposalArmorComposition(t, nil))
}

func disposalArmorProductionResult(t *testing.T, source string, frame map[string]any, want string) map[string]any {
	t.Helper()
	before := disposalArmorClone(t, frame)
	out, err := evaluateDisposalProduction(t, source, frame)
	if err != nil {
		t.Fatalf("production disposal evaluation failed: %v", err)
	}
	if !reflect.DeepEqual(frame, before) {
		t.Fatal("production disposal evaluation mutated observed facts")
	}
	return disposalArmorAssertResult(t, out, want)
}

func TestItemDisposalArmorFullContextAndEqualWornArmor(t *testing.T) {
	for _, armor := range []int{20, 25} {
		t.Run(strconv.Itoa(armor), func(t *testing.T) {
			frame := disposalArmorFrame(t)
			disposalArmorWorn(frame)["armor"] = float64(armor)
			before := disposalArmorClone(t, frame)
			disposalArmorResult(t, disposalArmorRuntime(t), frame, "eligible")
			if !reflect.DeepEqual(frame, before) {
				t.Fatal("policy mutated factual context")
			}
		})
	}
	t.Run("reordered_frames", func(t *testing.T) {
		frame := disposalArmorFrame(t)
		for _, rows := range [][]any{disposalArmorEquipment(frame, false), disposalArmorEquipment(frame, true), disposalArmorWorld(frame)["selfQuestLog"].([]any)} {
			for a, b := 0, len(rows)-1; a < b; a, b = a+1, b-1 {
				rows[a], rows[b] = rows[b], rows[a]
			}
		}
		disposalArmorResult(t, disposalArmorRuntime(t), frame, "eligible")
	})
}

func TestItemDisposalArmorSharedComparisonRetainsInferiorOrSpecialReplacement(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"lower_armor": func(v map[string]any) { disposalArmorWorn(v)["armor"] = 19 },
		"missing_worn_template": func(v map[string]any) {
			delete(disposalArmorInventory(v)["gearContext"].(map[string]any)["templates"].(map[string]any), "907001")
		},
		"special_worn":              func(v map[string]any) { disposalArmorWorn(v)["hasSpell"] = true },
		"unknown_worn_coverage":     func(v map[string]any) { delete(disposalArmorWorn(v), "armorComparisonKnown") },
		"source_special":            func(v map[string]any) { v["item"].(map[string]any)["hasSpell"] = true },
		"source_unknown_comparison": func(v map[string]any) { delete(v["item"].(map[string]any), "armorComparisonKnown") },
		"old_source_no_field32":     func(v map[string]any) { delete(v["item"].(map[string]any), "rawTemplateFacts") },
		"source_stats": func(v map[string]any) {
			v["item"].(map[string]any)["stats"] = []any{map[string]any{"type": 5, "value": 1}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := disposalArmorFrame(t)
			change(v)
			want := "retained"
			if name == "old_source_no_field32" {
				want = "unknown"
			}
			if name == "lower_armor" {
				want = "keep"
			}
			disposalArmorResult(t, disposalArmorRuntime(t), v, want)
		})
	}
}

func TestItemDisposalArmorEnhancementAndDurabilityProofIsRequired(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"source_enhanced": func(v map[string]any) {
			disposalArmorSelected(v)["gearFacts"].(map[string]any)["hasEnhancements"] = true
		},
		"source_consistently_enhanced": func(v map[string]any) {
			disposalArmorSelected(v)["gearFacts"].(map[string]any)["hasEnhancements"] = true
			disposalArmorInventory(v)["inventoryBags"].([]any)[0].(map[string]any)["slots"].([]any)[3].(map[string]any)["hasEnhancements"] = true
		},
		"source_unknown_enhancements": func(v map[string]any) {
			delete(disposalArmorSelected(v)["gearFacts"].(map[string]any), "enhancementsKnown")
		},
		"source_evidence_mismatch": func(v map[string]any) { disposalArmorSelected(v)["gearFacts"].(map[string]any)["durability"] = 1 },
		"worn_enhanced": func(v map[string]any) {
			for _, rows := range [][]any{disposalArmorEquipment(v, false), disposalArmorEquipment(v, true)} {
				rows[4].(map[string]any)["hasEnhancements"] = true
			}
		},
		"worn_unknown_enhancements": func(v map[string]any) {
			for _, rows := range [][]any{disposalArmorEquipment(v, false), disposalArmorEquipment(v, true)} {
				delete(rows[4].(map[string]any), "enhancementsKnown")
			}
		},
		"broken_worn": func(v map[string]any) {
			for _, rows := range [][]any{disposalArmorEquipment(v, false), disposalArmorEquipment(v, true)} {
				rows[4].(map[string]any)["durability"] = 0
			}
		},
		"unknown_worn_durability": func(v map[string]any) {
			for _, rows := range [][]any{disposalArmorEquipment(v, false), disposalArmorEquipment(v, true)} {
				delete(rows[4].(map[string]any), "durabilityKnown")
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := disposalArmorFrame(t)
			change(v)
			disposalArmorResult(t, disposalArmorRuntime(t), v, "retained")
		})
	}
}

func TestItemDisposalArmorTypedQuestItemAndSourceItemProtection(t *testing.T) {
	t.Run("unrelated_item", func(t *testing.T) {
		unrelated := disposalArmorFrame(t)
		disposalArmorJournal(unrelated)["entries"].([]any)[0].(map[string]any)["targets"] = []any{map[string]any{"kind": "ITEM", "targetId": 2004, "requiredCount": 1}}
		disposalArmorResult(t, disposalArmorRuntime(t), unrelated, "eligible")
	})
	for _, kind := range []string{"ITEM", "SOURCE_ITEM"} {
		counts := []int{1}
		if kind == "SOURCE_ITEM" {
			counts = append(counts, 0)
		}
		for _, count := range counts {
			t.Run(kind+string(rune('0'+count)), func(t *testing.T) {
				v := disposalArmorFrame(t)
				entry := disposalArmorJournal(v)["entries"].([]any)[0].(map[string]any)
				entry["targets"] = []any{map[string]any{"kind": kind, "targetId": 907000, "requiredCount": count}}
				disposalArmorResult(t, disposalArmorRuntime(t), v, "keep")
			})
		}
	}
	for name, change := range map[string]func(map[string]any){
		"missing_journal":         func(v map[string]any) { delete(disposalArmorContext(v), "journal") },
		"stale_journal":           func(v map[string]any) { disposalArmorJournal(v)["questLogRevision"] = "12" },
		"foreign_journal_session": func(v map[string]any) { disposalArmorJournal(v)["sessionId"] = "other-session" },
		"missing_template":        func(v map[string]any) { disposalArmorJournal(v)["entries"] = []any{} },
		"unknown_protection": func(v map[string]any) {
			delete(disposalArmorJournal(v)["entries"].([]any)[0].(map[string]any), "itemProtectionKnown")
		},
		"unknown_template": func(v map[string]any) {
			disposalArmorJournal(v)["entries"].([]any)[0].(map[string]any)["templateKnown"] = false
		},
		"unknown_log_state": func(v map[string]any) {
			disposalArmorJournal(v)["entries"].([]any)[0].(map[string]any)["logStateKnown"] = false
		},
		"wrong_journal_slot": func(v map[string]any) { disposalArmorJournal(v)["entries"].([]any)[0].(map[string]any)["slot"] = 8 },
		"short_log": func(v map[string]any) {
			disposalArmorWorld(v)["selfQuestLog"] = disposalArmorWorld(v)["selfQuestLog"].([]any)[:24]
		},
		"duplicate_log_slot": func(v map[string]any) { disposalArmorWorld(v)["selfQuestLog"].([]any)[0].(map[string]any)["slot"] = 1 },
		"malformed_item_target": func(v map[string]any) {
			disposalArmorJournal(v)["entries"].([]any)[0].(map[string]any)["targets"] = []any{map[string]any{"kind": "ITEM", "targetId": false, "requiredCount": 1}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := disposalArmorFrame(t)
			change(v)
			disposalArmorResult(t, disposalArmorRuntime(t), v, "retained")
		})
	}
}

func TestItemDisposalArmorQuestIdentifiersDoNotAllocateUnboundedSparseArrays(t *testing.T) {
	for _, quest := range []uint32{1, 99000, 4294967295} {
		for _, protected := range []bool{false, true} {
			t.Run(strconv.FormatUint(uint64(quest), 10)+"/protected_"+strconv.FormatBool(protected), func(t *testing.T) {
				frame := disposalArmorFrame(t)
				disposalArmorWorld(frame)["selfQuestLog"].([]any)[7].(map[string]any)["questId"] = quest
				entry := disposalArmorJournal(frame)["entries"].([]any)[0].(map[string]any)
				entry["questId"] = quest
				want := "eligible"
				if protected {
					entry["targets"] = []any{map[string]any{"kind": "ITEM", "targetId": 907000, "requiredCount": 1}}
					want = "keep"
				}
				disposalArmorResult(t, disposalArmorRuntime(t), frame, want)
			})
		}
	}
}

func TestItemDisposalArmorFullEquipmentAndRevisionIdentity(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"missing_context":          func(v map[string]any) { delete(v, "context") },
		"foreign_body":             func(v map[string]any) { disposalArmorWorld(v)["self"].(map[string]any)["guid"] = "5" },
		"absent_body":              func(v map[string]any) { disposalArmorWorld(v)["self"].(map[string]any)["present"] = false },
		"inventory_revision":       func(v map[string]any) { disposalArmorWorld(v)["inventoryRevision"] = "12" },
		"equipment_revision":       func(v map[string]any) { disposalArmorWorld(v)["selfEquipmentRevision"] = "11" },
		"short_equipment":          func(v map[string]any) { disposalArmorInventory(v)["equipment"] = disposalArmorEquipment(v, false)[:18] },
		"short_world_equipment":    func(v map[string]any) { disposalArmorWorld(v)["selfEquipment"] = disposalArmorEquipment(v, true)[:18] },
		"duplicate_equipment_slot": func(v map[string]any) { disposalArmorEquipment(v, false)[0].(map[string]any)["slot"] = 1 },
		"unrelated_unknown_slot":   func(v map[string]any) { disposalArmorEquipment(v, false)[18].(map[string]any)["slotKnown"] = false },
		"worn_identity_changed": func(v map[string]any) {
			disposalArmorEquipment(v, true)[4].(map[string]any)["itemGuid"] = "4611686018427387910"
		},
		"source_is_worn": func(v map[string]any) {
			for _, rows := range [][]any{disposalArmorEquipment(v, false), disposalArmorEquipment(v, true)} {
				rows[4].(map[string]any)["itemGuid"] = disposalArmorSourceGUID
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := disposalArmorFrame(t)
			change(v)
			disposalArmorResult(t, disposalArmorRuntime(t), v, "retained")
		})
	}
}

func TestItemDisposalArmorExactCarriedSourceAndCompleteLayout(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"wrong_source_guid": func(v map[string]any) {
			disposalArmorContext(v)["source"].(map[string]any)["itemGuid"] = "4611686018427387910"
		},
		"wrong_source_entry":      func(v map[string]any) { disposalArmorContext(v)["source"].(map[string]any)["entry"] = 117 },
		"stacked_source":          func(v map[string]any) { disposalArmorContext(v)["source"].(map[string]any)["stackCount"] = 2 },
		"missing_eligibility_row": func(v map[string]any) { disposalArmorInventory(v)["items"] = []any{} },
		"duplicate_eligibility_row": func(v map[string]any) {
			rows := disposalArmorInventory(v)["items"].([]any)
			disposalArmorInventory(v)["items"] = append(rows, rows[0])
		},
		"unknown_source_metadata": func(v map[string]any) { disposalArmorSelected(v)["metadataKnown"] = false },
		"short_bag_layout": func(v map[string]any) {
			disposalArmorInventory(v)["inventoryBags"] = disposalArmorInventory(v)["inventoryBags"].([]any)[:4]
		},
		"unknown_contents": func(v map[string]any) {
			disposalArmorInventory(v)["inventoryBags"].([]any)[0].(map[string]any)["contentsKnown"] = false
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := disposalArmorFrame(t)
			change(v)
			disposalArmorResult(t, disposalArmorRuntime(t), v, "retained")
		})
	}
}

func TestItemDisposalArmorRequiresActualNativeDestinationBindings(t *testing.T) {
	t.Run("blocked_physical_destination_still_compares", func(t *testing.T) {
		frame := disposalArmorFrame(t)
		destination := disposalArmorSelected(frame)["gearFacts"].(map[string]any)["destinations"].([]any)[0].(map[string]any)
		destination["eligible"] = false
		disposalArmorResult(t, disposalArmorRuntime(t), frame, "eligible")
	})
	t.Run("fresh_actual_equipment_guid_matches_destination", func(t *testing.T) {
		frame := disposalArmorFrame(t)
		const replacement = "4611686018427387910"
		for _, rows := range [][]any{disposalArmorEquipment(frame, false), disposalArmorEquipment(frame, true)} {
			rows[4].(map[string]any)["itemGuid"] = replacement
		}
		disposalArmorSelected(frame)["gearFacts"].(map[string]any)["destinations"].([]any)[0].(map[string]any)["expectedEquippedItemGuid"] = replacement
		disposalArmorResult(t, disposalArmorRuntime(t), frame, "eligible")
	})
	for name, change := range map[string]func(map[string]any){
		"old_missing_destinations": func(v map[string]any) { delete(disposalArmorSelected(v)["gearFacts"].(map[string]any), "destinations") },
		"empty_destinations": func(v map[string]any) {
			disposalArmorSelected(v)["gearFacts"].(map[string]any)["destinations"] = []any{}
		},
		"malformed_destinations": func(v map[string]any) { disposalArmorSelected(v)["gearFacts"].(map[string]any)["destinations"] = false },
		"duplicate_destination": func(v map[string]any) {
			facts := disposalArmorSelected(v)["gearFacts"].(map[string]any)
			rows := facts["destinations"].([]any)
			facts["destinations"] = append(rows, rows[0])
		},
		"wrong_destination_guid": func(v map[string]any) {
			disposalArmorSelected(v)["gearFacts"].(map[string]any)["destinations"].([]any)[0].(map[string]any)["expectedEquippedItemGuid"] = "4611686018427387910"
		},
		"malformed_destination_slot": func(v map[string]any) {
			disposalArmorSelected(v)["gearFacts"].(map[string]any)["destinations"].([]any)[0].(map[string]any)["equipmentSlot"] = false
		},
		"equipment_changed_since_destination": func(v map[string]any) {
			for _, rows := range [][]any{disposalArmorEquipment(v, false), disposalArmorEquipment(v, true)} {
				rows[4].(map[string]any)["itemGuid"] = "4611686018427387910"
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			frame := disposalArmorFrame(t)
			change(frame)
			disposalArmorResult(t, disposalArmorRuntime(t), frame, "unknown")
		})
	}
}

func TestItemDisposalArmorContextDoesNotChangeOtherModes(t *testing.T) {
	for _, mode := range []string{"vendor_sale", "misc_destroy"} {
		for _, armor := range []bool{false, true} {
			t.Run(mode+"/armor_"+strconv.FormatBool(armor), func(t *testing.T) {
				v := disposalArmorFrame(t)
				v["context"], v["mode"] = false, mode
				want := "eligible"
				if !armor {
					v["item"] = disposalTemplate(t, false)
				} else if mode == "misc_destroy" {
					want = "keep"
				}
				disposalArmorResult(t, disposalArmorRuntime(t), v, want)
			})
		}
	}
}

func TestItemDisposalArmorUsesEditableActualSharedGearScorer(t *testing.T) {
	source := disposalArmorComposition(t, nil)
	r := disposalArmorSemanticRuntime(t, source)
	first := r.Status().Revision
	frame := disposalArmorFrame(t)
	before := disposalArmorClone(t, frame)
	disposalArmorResult(t, r, frame, "eligible")
	editScorer := func(gear string) string {
		const expression = "local gain=(candidate.armor or 0)-(current.armor or 0)"
		if strings.Count(gear, expression) != 1 {
			t.Fatal("real shared armor score expression changed")
		}
		return strings.Replace(gear, expression, expression+"-100", 1)
	}
	edited := disposalArmorComposition(t, editScorer)
	second, err := r.Reload(edited, first)
	if err != nil || second == first {
		t.Fatal(second, err)
	}
	disposalArmorResult(t, r, frame, "keep")
	if revision, err := r.Reload(source, first); !errors.Is(err, ErrRevisionConflict) || revision != second {
		t.Fatal("stale scorer revision replaced edited policy", revision, err)
	}
	disposalArmorResult(t, r, frame, "keep")
	if _, err = r.Reload(source, second); err != nil {
		t.Fatal(err)
	}
	disposalArmorResult(t, r, frame, "eligible")
	if !reflect.DeepEqual(frame, before) {
		t.Fatal("scorer edit mutated observed facts")
	}
	// Product-default availability is independently required for the actual
	// host source/frame, before and after this exact shared-scorer source edit.
	productionSource, projected := disposalArmorHostProjection(t, frame)
	disposalArmorProductionResult(t, productionSource, projected, "eligible")
	disposalArmorProductionResult(t, editScorer(productionSource), projected, "keep")
	if !reflect.DeepEqual(frame, before) {
		t.Fatal("production scorer edit mutated the original observed facts")
	}
}

type disposalArmorCaptured struct {
	Source string         `json:"source"`
	Frame  map[string]any `json:"frame"`
	Result map[string]any `json:"result"`
}

func disposalArmorCapture(t *testing.T, frame map[string]any) disposalArmorCaptured {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "dev", "item_disposal.py"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	const script = `
import importlib.util,json,sys
spec=importlib.util.spec_from_file_location("armor_projection",sys.argv[1])
module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
class Capture:
    def evaluate(self,source,frame):
        self.source,self.frame=source,frame
        return {"status":"keep","reason":"captured facts only"}
value=json.load(sys.stdin);capture=Capture()
result=module.classify(value["item"],value["mode"],context=value.get("context"),bridge=capture)
print(json.dumps({"source":getattr(capture,"source",""),"frame":getattr(capture,"frame",None),"result":result},separators=(",",":")))
`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", "-B", "-c", script, path)
	command.Stdin = bytes.NewReader(body)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("actual host projection: %v\n%s", err, output)
	}
	var projected disposalArmorCaptured
	if err := json.Unmarshal(output, &projected); err != nil {
		t.Fatal(err)
	}
	return projected
}

func disposalArmorHostProjection(t *testing.T, frame map[string]any) (string, map[string]any) {
	t.Helper()
	projected := disposalArmorCapture(t, frame)
	if projected.Frame == nil {
		t.Fatal("complete factual fixture did not reach Lua", projected.Result)
	}
	if strings.TrimSpace(projected.Source) != strings.TrimSpace(disposalArmorComposition(t, nil)) {
		t.Fatal("test and host do not compose the same real shared scorer and classifier")
	}
	context := disposalArmorContext(projected.Frame)
	if context["facts"].(map[string]any)["questLogFrameKnown"] != true {
		t.Fatal("projected quest-log coverage is not positively known")
	}
	if context["facts"].(map[string]any)["physicalDestinationsKnown"] != true {
		t.Fatal("projected native physical destinations are not positively known")
	}
	if _, exists := disposalArmorInventory(projected.Frame)["equipment"]; exists {
		t.Fatal("compact context retained the unrelated equipment vector")
	}
	rawDestinations := disposalArmorSelected(frame)["gearFacts"].(map[string]any)["destinations"].([]any)
	candidates := context["equipmentCandidates"].([]any)
	if len(candidates) != len(rawDestinations) {
		t.Fatal("projection filtered or invented a native physical destination")
	}
	for index, value := range candidates {
		candidate := value.(map[string]any)
		destination := candidate["destination"].(map[string]any)
		raw := rawDestinations[index].(map[string]any)
		gotEligible, _ := destination["eligible"].(bool)
		wantEligible, _ := raw["eligible"].(bool)
		if destination["equipmentSlot"] != raw["equipmentSlot"] ||
			destination["expectedEquippedItemGuid"] != raw["expectedEquippedItemGuid"] ||
			gotEligible != wantEligible || destination["blockedReason"] != raw["blockedReason"] {
			t.Fatal("projection changed a factual native destination", candidate, raw)
		}
		var observed map[string]any
		for _, row := range disposalArmorEquipment(frame, false) {
			equipment := row.(map[string]any)
			slot := equipment["slot"]
			if slot == nil {
				slot = float64(0)
			}
			if slot == destination["equipmentSlot"] {
				observed = equipment
			}
		}
		if observed == nil || candidate["equipment"].(map[string]any)["itemGuid"] != observed["itemGuid"] ||
			destination["expectedEquippedItemGuid"] != observed["itemGuid"] {
			t.Fatal("projection did not join the actual observed equipment GUID", candidate, observed)
		}
	}
	if _, exists := disposalArmorWorld(projected.Frame)["selfQuestLog"]; exists {
		t.Fatal("compact context unexpectedly retained the raw quest-log vector")
	}
	// Every nonzero ID from the complete actual producer frame must survive
	// projection, with its exact slot. Large uint32 IDs remain part of the test.
	identity := func(row map[string]any) string {
		values := []any{row["slot"], row["questId"]}
		for index, value := range values {
			if value == nil {
				values[index] = 0
			}
		}
		body, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	expected := map[string]bool{}
	for _, value := range disposalArmorWorld(frame)["selfQuestLog"].([]any) {
		row := value.(map[string]any)
		quest := row["questId"]
		if quest == nil {
			continue
		}
		encoded, err := json.Marshal(quest)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != "0" {
			expected[identity(row)] = true
		}
	}
	observed := map[string]bool{}
	for _, value := range context["activeQuestRows"].([]any) {
		key := identity(value.(map[string]any))
		if observed[key] {
			t.Fatal("projection duplicated an active quest row")
		}
		observed[key] = true
	}
	if !reflect.DeepEqual(observed, expected) {
		t.Fatalf("projection lost or changed an observed active quest: got %v, want %v", observed, expected)
	}
	measured := &budget{nodeLimit: MaxInputNodes}
	if _, err := toLua(nil, projected.Frame, measured); err != nil {
		t.Fatal(err)
	}
	compact, _ := json.Marshal(projected.Frame)
	t.Logf("actual host frame: %d bytes, %d nodes, %d source bytes", len(compact), measured.nodes, len(projected.Source))
	return projected.Source, projected.Frame
}

func TestItemDisposalArmorActualHostProjectionFitsRealRuntime(t *testing.T) {
	source, frame := disposalArmorHostProjection(t, disposalArmorFrame(t))
	disposalArmorProductionResult(t, source, frame, "eligible")
}

func TestItemDisposalArmorRequiresPositiveMechanicalCoverageFacts(t *testing.T) {
	source, frame := disposalArmorHostProjection(t, disposalArmorFrame(t))
	// Malformed mechanical coverage must refuse before calling the scorer.
	// Load the real disposal module with a trap at that boundary, avoiding the
	// unrelated shared scorer's initialization in every refusal case. Positive
	// controls and actual-host/full-layout tests keep the real shared scorer.
	refusalSource := "local disposal=(function()\n" + disposalPolicySource(t) + `
end)()
local called=false
local gear={compare=function()called=true;error("invalid coverage reached the gear scorer")end}
return {decide=function(input)
 local out=disposal.classify(input.mode,input.item,input.context,gear)
 if called then error("invalid coverage attempted a caught gear comparison")end
 return out
end}`
	fresh := func(t *testing.T) *Runtime {
		return disposalArmorSemanticRuntime(t, refusalSource)
	}
	t.Run("complete_facts", func(t *testing.T) {
		r := disposalArmorSemanticRuntime(t, source)
		disposalArmorResult(t, r, frame, "eligible")
	})
	t.Run("missing_scorer", func(t *testing.T) {
		disposalArmorResult(t, disposalArmorSemanticRuntime(t, disposalPolicySource(t)), frame, "unknown")
	})
	for _, field := range []string{"inventoryLayoutKnown", "equipmentFramesMatch", "questLogFrameKnown", "physicalDestinationsKnown"} {
		for _, condition := range []struct {
			name  string
			value any
		}{{"missing", nil}, {"false", false}, {"wrong_type", 1}} {
			t.Run(field+"/"+condition.name, func(t *testing.T) {
				changed := disposalArmorClone(t, frame)
				facts := disposalArmorContext(changed)["facts"].(map[string]any)
				if condition.value == nil {
					delete(facts, field)
				} else {
					facts[field] = condition.value
				}
				disposalArmorResult(t, fresh(t), changed, "unknown")
			})
		}
	}
	for name, change := range map[string]func(map[string]any){
		"equipment_candidates_missing": func(v map[string]any) { delete(disposalArmorContext(v), "equipmentCandidates") },
		"equipment_candidates_empty":   func(v map[string]any) { disposalArmorContext(v)["equipmentCandidates"] = []any{} },
		"equipment_candidates_duplicate": func(v map[string]any) {
			context := disposalArmorContext(v)
			rows := context["equipmentCandidates"].([]any)
			context["equipmentCandidates"] = append(rows, rows[0])
		},
		"equipment_candidate_guid_mismatch": func(v map[string]any) {
			disposalArmorContext(v)["equipmentCandidates"].([]any)[0].(map[string]any)["destination"].(map[string]any)["expectedEquippedItemGuid"] = "4611686018427387910"
		},
		"active_rows_missing": func(v map[string]any) { delete(disposalArmorContext(v), "activeQuestRows") },
		"active_rows_duplicate": func(v map[string]any) {
			context := disposalArmorContext(v)
			rows := context["activeQuestRows"].([]any)
			context["activeQuestRows"] = append(rows, rows[0])
		},
		"active_rows_extra": func(v map[string]any) {
			context := disposalArmorContext(v)
			context["activeQuestRows"] = append(context["activeQuestRows"].([]any), map[string]any{"slot": 8, "questId": 99001})
		},
		"active_rows_wrong_slot": func(v map[string]any) {
			disposalArmorContext(v)["activeQuestRows"].([]any)[0].(map[string]any)["slot"] = 8
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := disposalArmorClone(t, frame)
			change(changed)
			disposalArmorResult(t, fresh(t), changed, "unknown")
		})
	}
}

func TestItemDisposalArmorFullCarriedLayoutAndOccupiedEquipmentBudget(t *testing.T) {
	frame := disposalArmorFrame(t)
	inv, world := disposalArmorInventory(frame), disposalArmorWorld(frame)
	guid := func(offset int) string { return strconv.FormatUint(uint64(0x4000000000100000)+uint64(offset), 10) }
	// All 160 carried slots are factual occupied references. Only the exact
	// selected row carries metadata and comparison facts, as the adapter emits.
	sourceRow := inv["inventoryBags"].([]any)[0].(map[string]any)["slots"].([]any)[3]
	bags := []any{}
	for bag := 0; bag < 5; bag++ {
		count := 36
		if bag == 0 {
			count = 16
		}
		rows := []any{}
		for slot := 0; slot < count; slot++ {
			var row any = map[string]any{"bag": bag, "slot": slot, "itemGuid": guid(bag*100 + slot + 1)}
			if bag == 0 && slot == 3 {
				row = sourceRow
			}
			rows = append(rows, row)
		}
		header := map[string]any{"bag": bag, "slotKnown": true, "contentsKnown": true, "slotCount": count, "slots": rows}
		if bag > 0 {
			header["containerGuid"], header["metadataKnown"], header["entry"] = guid(10000+bag), true, 88000+bag
		}
		bags = append(bags, header)
	}
	inv["inventoryBags"] = bags
	templates := inv["gearContext"].(map[string]any)["templates"].(map[string]any)
	slots := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 11, 12, 12, 16, 13, 14, 15, 19}
	for slot := 0; slot < 19; slot++ {
		if slot == 4 {
			continue
		}
		entry := 907100 + slot
		row := map[string]any{"slot": slot, "slotKnown": true, "itemGuid": guid(20000 + slot), "metadataKnown": true, "entry": entry, "stackCount": 1, "enhancementsKnown": true, "durabilityKnown": true, "durability": 20, "maxDurability": 30}
		for _, rows := range [][]any{disposalArmorEquipment(frame, false), disposalArmorEquipment(frame, true)} {
			rows[slot] = row
		}
		templates[strconv.Itoa(entry)] = map[string]any{"itemId": entry, "available": true, "itemClass": 4, "subclass": 1, "inventoryType": slots[slot], "quality": 1, "armor": 5, "armorComparisonKnown": true}
	}
	templates["907000"] = map[string]any{"itemId": 907000, "available": true, "itemClass": 4, "subclass": 1, "inventoryType": 5, "armor": 20, "armorComparisonKnown": true}
	context := disposalArmorContext(frame)
	context["inventory"] = disposalArmorProtoJSON(t, inv, &inventoryv1.State{})
	context["world"] = disposalArmorProtoJSON(t, world, &worldv1.SnapshotResponse{})
	source, projected := disposalArmorHostProjection(t, frame)
	disposalArmorProductionResult(t, source, projected, "eligible")
}
