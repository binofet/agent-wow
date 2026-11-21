package behavior

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	"google.golang.org/protobuf/encoding/protojson"
)

func disposalPolicySource(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "item-disposal.lua"))
	if err != nil {
		t.Fatal(err)
	}
	return string(source)
}

func disposalRuntime(t *testing.T) *Runtime {
	t.Helper()
	r := New()
	load(t, r, disposalPolicySource(t))
	return r
}

func disposalTemplate(t *testing.T, armor bool) map[string]any {
	t.Helper()
	item := &tradev1.ItemDetails{ItemId: 907000, Available: true, ItemClass: 15, SellPrice: 5,
		RawTemplateFacts: &tradev1.RawTemplateFacts{}}
	if armor {
		item.ItemClass, item.Subclass, item.InventoryType = 4, 1, 5
		item.RawTemplateFacts.MaxDurability = 22
	}
	encoded, err := protojson.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func disposalResult(t *testing.T, r *Runtime, mode string, item any, want string) map[string]any {
	t.Helper()
	out, err := r.Evaluate(map[string]any{"mode": mode, "item": item})
	if err != nil {
		t.Fatal(err)
	}
	reason, ok := out["reason"].(string)
	if out["status"] != want || len(out) != 2 || !ok || len(reason) == 0 || len(reason) > 128 {
		t.Fatalf("mode %s: got %v, want status %s and bounded reason", mode, out, want)
	}
	return out
}

func TestItemDisposalObservedZeroAndUnknownTemplatePresence(t *testing.T) {
	r := disposalRuntime(t)
	for _, mode := range []string{"vendor_sale", "misc_destroy"} {
		t.Run(mode, func(t *testing.T) {
			base := disposalTemplate(t, false)
			if facts := base["rawTemplateFacts"].(map[string]any); len(facts) != 0 {
				t.Fatalf("zero-valued protojson fixture must retain an empty facts object: %v", base)
			}
			if _, exists := base["quality"]; exists {
				t.Fatal("fixture did not exercise omitted proto3 zero scalars")
			}
			disposalResult(t, r, mode, base, "eligible")
			for _, missing := range []string{"rawTemplateFacts", "available", "itemId"} {
				item := disposalTemplate(t, false)
				delete(item, missing)
				disposalResult(t, r, mode, item, "unknown")
			}
			for _, facts := range []any{nil, false, "unknown", []any{1}} {
				item := disposalTemplate(t, false)
				item["rawTemplateFacts"] = facts
				disposalResult(t, r, mode, item, "unknown")
			}
			for _, changes := range []map[string]any{
				{"available": false}, {"pending": true}, {"itemId": 0}, {"itemId": uint32(1 << 31)},
			} {
				item := disposalTemplate(t, false)
				for k, v := range changes {
					item[k] = v
				}
				disposalResult(t, r, mode, item, "unknown")
			}
			item := disposalTemplate(t, false)
			item["pending"], item["itemId"] = false, uint32(1<<31-1)
			disposalResult(t, r, mode, item, "eligible")
		})
	}
}

func TestItemDisposalCommonFunctionalPropertiesKeepMiscellaneousItems(t *testing.T) {
	r := disposalRuntime(t)
	for _, mode := range []string{"vendor_sale", "misc_destroy"} {
		for _, field := range []string{"quality", "flags", "flags2", "hasSpell"} {
			t.Run(mode+"/"+field, func(t *testing.T) {
				item := disposalTemplate(t, false)
				item[field] = 1
				if field == "hasSpell" {
					item[field] = true
				}
				disposalResult(t, r, mode, item, "keep")
			})
		}
		for _, field := range []string{"containerSlots", "pageText", "startQuest", "lockId", "maxDurability", "bagFamily", "duration"} {
			for _, value := range []uint32{1, math.MaxUint32} {
				t.Run(fmt.Sprintf("%s/%s/%d", mode, field, value), func(t *testing.T) {
					item := disposalTemplate(t, false)
					item["rawTemplateFacts"].(map[string]any)[field] = value
					disposalResult(t, r, mode, item, "keep")
				})
			}
		}
		for bonding := 0; bonding <= 5; bonding++ {
			item := disposalTemplate(t, false)
			item["rawTemplateFacts"].(map[string]any)["bonding"] = bonding
			want := "keep"
			if bonding == 0 {
				want = "eligible"
			}
			disposalResult(t, r, mode, item, want)
		}
	}
}

func TestItemDisposalVendorArmorSlotAndSubclassMatchExistingNativeScope(t *testing.T) {
	r := disposalRuntime(t)
	allowedSlots := map[int]bool{1: true, 3: true, 5: true, 6: true, 7: true, 8: true, 9: true, 10: true, 16: true, 20: true}
	for subclass := 0; subclass <= 10; subclass++ {
		for slot := 0; slot <= 28; slot++ {
			t.Run(fmt.Sprintf("subclass%d/slot%d", subclass, slot), func(t *testing.T) {
				item := disposalTemplate(t, true)
				item["subclass"], item["inventoryType"] = subclass, slot
				want := "keep"
				if subclass >= 1 && subclass <= 4 && allowedSlots[slot] {
					want = "eligible"
				}
				disposalResult(t, r, "vendor_sale", item, want)
			})
		}
	}
	for _, class := range []int{0, 1, 2, 3, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 16} {
		item := disposalTemplate(t, true)
		item["itemClass"] = class
		disposalResult(t, r, "vendor_sale", item, "keep")
	}
	for _, field := range []string{"subclass", "inventoryType"} {
		item := disposalTemplate(t, false)
		item[field] = 1
		disposalResult(t, r, "vendor_sale", item, "keep")
		disposalResult(t, r, "misc_destroy", item, "keep")
	}
}

func TestItemDisposalSpecialRequirementsApplyOnlyToVendorArmor(t *testing.T) {
	r := disposalRuntime(t)
	for _, field := range []string{"requiredSkill", "requiredSkillRank", "requiredSpell", "requiredHonorRank",
		"requiredCityRank", "requiredReputationFaction", "requiredReputationRank"} {
		t.Run(field, func(t *testing.T) {
			armor, misc := disposalTemplate(t, true), disposalTemplate(t, false)
			armor[field], misc[field] = uint32(math.MaxUint32), uint32(math.MaxUint32)
			disposalResult(t, r, "vendor_sale", armor, "keep")
			disposalResult(t, r, "vendor_sale", misc, "eligible")
			disposalResult(t, r, "misc_destroy", misc, "eligible")
		})
	}
	for _, armor := range []bool{false, true} {
		item := disposalTemplate(t, armor)
		item["requiredLevel"], item["allowableClass"], item["allowableRace"] = 80, 1, 1
		item["stats"] = []any{map[string]any{"type": 5, "value": 7}, map[string]any{"type": 7, "value": -3}}
		item["armor"], item["delayMs"] = 300, 2800
		disposalResult(t, r, "vendor_sale", item, "eligible")
		if !armor {
			disposalResult(t, r, "misc_destroy", item, "eligible")
		}
	}
}

func TestItemDisposalPriceAndDurabilityDifferByExistingMode(t *testing.T) {
	r := disposalRuntime(t)
	for _, armor := range []bool{false, true} {
		for _, price := range []int{0, 5} {
			for _, durability := range []int{0, 22} {
				item := disposalTemplate(t, armor)
				item["sellPrice"] = price
				item["rawTemplateFacts"].(map[string]any)["maxDurability"] = durability
				vendor, destroy := "keep", "keep"
				if price > 0 && (armor || durability == 0) {
					vendor = "eligible"
				}
				if !armor && durability == 0 {
					destroy = "eligible"
				}
				disposalResult(t, r, "vendor_sale", item, vendor)
				disposalResult(t, r, "misc_destroy", item, destroy)
			}
		}
	}
}

func TestItemDisposalMalformedBooleanAndNumericFactsRemainUnknown(t *testing.T) {
	r := disposalRuntime(t)
	changes := []struct {
		field string
		value any
	}{
		{"available", 1}, {"pending", 0}, {"hasSpell", 0}, {"itemId", "907000"},
		{"itemClass", 1.5}, {"subclass", -1}, {"inventoryType", uint64(1 << 32)},
		{"quality", false}, {"flags", "0"}, {"flags2", -1}, {"sellPrice", true},
		{"requiredSkill", "0"}, {"requiredSkillRank", false}, {"requiredSpell", -1},
		{"requiredHonorRank", 0.5}, {"requiredCityRank", uint64(1 << 32)},
		{"requiredReputationFaction", true}, {"requiredReputationRank", "1"},
	}
	for _, mode := range []string{"vendor_sale", "misc_destroy"} {
		for _, change := range changes {
			t.Run(mode+"/"+change.field, func(t *testing.T) {
				item := disposalTemplate(t, false)
				item[change.field] = change.value
				disposalResult(t, r, mode, item, "unknown")
			})
		}
		for _, field := range []string{"containerSlots", "bonding", "pageText", "startQuest", "lockId", "maxDurability", "bagFamily", "duration"} {
			t.Run(mode+"/raw/"+field, func(t *testing.T) {
				item := disposalTemplate(t, false)
				item["rawTemplateFacts"].(map[string]any)[field] = false
				disposalResult(t, r, mode, item, "unknown")
			})
		}
		item := disposalTemplate(t, false)
		item["rawTemplateFacts"].(map[string]any)["bonding"] = 6
		disposalResult(t, r, mode, item, "unknown")
		for _, invalid := range []any{nil, "item", false, []any{1}} {
			disposalResult(t, r, mode, invalid, "unknown")
		}
	}
}

func TestItemDisposalGrayArmorDestructionRequiresUnavailableContext(t *testing.T) {
	r := disposalRuntime(t)
	for _, item := range []any{disposalTemplate(t, true), disposalTemplate(t, false), nil} {
		out := disposalResult(t, r, "gray_armor_destroy", item, "unknown")
		if out["reason"] != "armor_context_required" {
			t.Fatal("static template classification invented armor destruction proof", out)
		}
	}
	disposalResult(t, r, "other_mode", disposalTemplate(t, false), "unknown")
}

func TestItemDisposalCASPolicyEditChangesSameRuntimeAndItem(t *testing.T) {
	r := New()
	source := disposalPolicySource(t)
	first := load(t, r, source)
	item := disposalTemplate(t, false)
	beforeJSON, _ := json.Marshal(item)
	disposalResult(t, r, "vendor_sale", item, "eligible")
	// The same exported classifier can be composed into an edited Lua policy.
	// No game transport, character reconnect or compiled Go change is involved.
	edited := "local classifier=(function()\n" + source + `
end)()
return {decide=function(input)
    local decision=classifier.classify(input.mode,input.item)
    if decision.status=="eligible" then return {status="keep",reason="edited_policy_retains_item"} end
    return decision
end}`
	second, err := r.Reload(edited, first)
	if err != nil || second == first {
		t.Fatal("policy edit did not establish a new revision", second, err)
	}
	out := disposalResult(t, r, "vendor_sale", item, "keep")
	if out["reason"] != "edited_policy_retains_item" || r.Status().Revision != second {
		t.Fatal(out, r.Status())
	}
	if got, err := r.Reload(source, first); !errors.Is(err, ErrRevisionConflict) || got != second {
		t.Fatal("stale source writer replaced the edited classifier", got, err)
	}
	disposalResult(t, r, "vendor_sale", item, "keep")
	afterJSON, _ := json.Marshal(item)
	if !reflect.DeepEqual(beforeJSON, afterJSON) {
		t.Fatal("classification changed its observed template input")
	}
}
