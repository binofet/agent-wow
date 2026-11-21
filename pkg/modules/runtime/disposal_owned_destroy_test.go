package modrt

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func disposalOwnedDestroyInput(t *testing.T, p *disposalOwnedPeer) map[string]any {
	t.Helper()
	input := disposalOwnedInput(t, p)
	input["mode"] = "owned_destroy"
	return input
}

func disposalOwnedDestroyMisc(p *disposalOwnedPeer) {
	p.item = &tradev1.ItemDetails{ItemId: p.source.Entry, Available: true, ItemClass: 15, RawTemplateFacts: &tradev1.RawTemplateFacts{}}
	p.inventory.InventoryBags[0].Slots[3].Durability, p.inventory.InventoryBags[0].Slots[3].MaxDurability = 0, 0
	p.inventory.Items[0].GearFacts.Durability, p.inventory.Items[0].GearFacts.MaxDurability = 0, 0
	p.inventory.Items[0].GearFacts.Destinations = nil
	p.inventory.GearContext = nil
}

func TestOwnedDestroyActualBundleKeepsMiscAndGrayFactualRequirementsDistinct(t *testing.T) {
	for _, test := range []struct {
		name, want string
		change     func(*disposalOwnedPeer)
		physical   bool
	}{
		{"complete_gray", "eligible", func(*disposalOwnedPeer) {}, true},
		{"misc_without_gear", "eligible", disposalOwnedDestroyMisc, false},
		{"gray_without_destinations", "unknown", func(p *disposalOwnedPeer) { p.inventory.Items[0].GearFacts.Destinations = nil }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, p := disposalOwnedManager(t)
			test.change(p)
			host := disposalOwnedStart(t, m, followFallbackBundle(t))
			before, err := host.vm.State(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			options := host.vm.Options()
			input := disposalOwnedDestroyInput(t, p)
			frame, witness, err := m.ownedDisposalInput(context.Background(), p.inventory.SessionId, "4", input)
			if err != nil || witness == nil {
				t.Fatal("valid common ownership was lost", frame, witness, err)
			}
			factual := frame["context"].(map[string]any)
			facts := factual["facts"].(map[string]any)
			if (facts["physicalDestinationsKnown"] == true) != test.physical {
				t.Fatal("gear failure fabricated or removed physical coverage", facts)
			}
			if !test.physical {
				if _, present := factual["equipmentCandidates"]; present {
					t.Fatal("common fallback invented gear destination rows")
				}
				if _, present := factual["inventory"].(map[string]any)["items"]; present {
					t.Fatal("common fallback exposed unvalidated gear facts")
				}
			}
			p.calls = nil
			result := disposalOwnedRead(t, m, disposalOwnedRequest(t, p, input))
			decision := result["decision"].(map[string]any)
			if result["available"] != true || result["revision"] != host.vm.Status().Revision || decision["status"] != test.want || decision["nativeContext"] == nil ||
				!reflect.DeepEqual(p.calls, disposalOwnedTrace()) || p.writes != 0 {
				t.Fatal("same accepted classifier lost mode union or exact read-only facts", result, p.calls, p.writes)
			}
			if !reflect.DeepEqual(options, host.vm.Options()) || len(host.vm.Outstanding()) != 0 {
				t.Fatal("read-only collector changed evaluation options or reserved an action")
			}
			assertFollowFallbackState(t, host, before)
		})
	}
}

func TestOwnedDestroyInvalidGearCannotGrantGrayEligibility(t *testing.T) {
	for name, mutate := range map[string]func(*disposalOwnedPeer){
		"missing_gear_facts":   func(p *disposalOwnedPeer) { p.inventory.Items[0].GearFacts = nil },
		"missing_selected_row": func(p *disposalOwnedPeer) { p.inventory.Items = nil },
		"missing_destinations": func(p *disposalOwnedPeer) { p.inventory.Items[0].GearFacts.Destinations = nil },
		"destination_guid_changed": func(p *disposalOwnedPeer) {
			p.inventory.Items[0].GearFacts.Destinations[0].ExpectedEquippedItemGuid++
		},
		"duplicate_destinations": func(p *disposalOwnedPeer) {
			p.inventory.Items[0].GearFacts.Destinations = append(p.inventory.Items[0].GearFacts.Destinations,
				&inventoryv1.GearDestination{EquipmentSlot: 4, ExpectedEquippedItemGuid: p.inventory.Equipment[4].ItemGuid})
		},
		"missing_template_context": func(p *disposalOwnedPeer) { p.inventory.GearContext = nil },
		"unbounded_template_stats": func(p *disposalOwnedPeer) {
			p.inventory.GearContext.Templates["907001"].Stats = make([]*inventoryv1.GearStat, 11)
		},
	} {
		t.Run(name, func(t *testing.T) {
			m, p := disposalOwnedManager(t)
			mutate(p)
			disposalOwnedStart(t, m, followFallbackBundle(t))
			input := disposalOwnedDestroyInput(t, p)
			frame, _, err := m.ownedDisposalInput(context.Background(), p.inventory.SessionId, "4", input)
			if err != nil || frame["context"].(map[string]any)["facts"].(map[string]any)["physicalDestinationsKnown"] == true {
				t.Fatal("gear-only failure blocked common ownership or fabricated coverage", frame, err)
			}
			p.calls = nil
			decision := disposalOwnedRead(t, m, disposalOwnedRequest(t, p, input))["decision"].(map[string]any)
			if decision["status"] != "unknown" || decision["nativeContext"] == nil || !reflect.DeepEqual(p.calls, disposalOwnedTrace()) || p.writes != 0 {
				t.Fatal("invalid armor facts granted eligibility", decision, p.calls, p.writes)
			}
		})
	}
}

func TestOwnedDestroyMalformedCommonFramesNeverReachLuaFallback(t *testing.T) {
	for name, mutate := range map[string]func(*disposalOwnedPeer){
		"partial_bags": func(p *disposalOwnedPeer) { p.inventory.InventoryBags = p.inventory.InventoryBags[:4] },
		"duplicate_carried_guid": func(p *disposalOwnedPeer) {
			p.inventory.InventoryBags[0].Slots[0].ItemGuid = p.source.ItemGuid
		},
		"source_changed":                  func(p *disposalOwnedPeer) { p.inventory.InventoryBags[0].Slots[3].StackCount++ },
		"partial_equipment":               func(p *disposalOwnedPeer) { p.inventory.Equipment = p.inventory.Equipment[:18] },
		"same_revision_equipment_changed": func(p *disposalOwnedPeer) { p.world.SelfEquipment[4].Durability++ },
		"inventory_unknown":               func(p *disposalOwnedPeer) { p.inventory.InventoryKnown = false },
		"cache_closing":                   func(p *disposalOwnedPeer) { p.inventory.Closing = true },
		"world_inventory_unknown":         func(p *disposalOwnedPeer) { p.world.InventoryKnown = false },
		"partial_raw_log":                 func(p *disposalOwnedPeer) { p.world.SelfQuestLog = p.world.SelfQuestLog[:24] },
		"duplicate_raw_log":               func(p *disposalOwnedPeer) { p.world.SelfQuestLog[24].Slot = 0 },
		"foreign_journal":                 func(p *disposalOwnedPeer) { p.journal.SessionId = "foreign" },
		"stale_journal":                   func(p *disposalOwnedPeer) { p.journal.QuestLogRevision++ },
	} {
		t.Run(name, func(t *testing.T) {
			m, p := disposalOwnedManager(t)
			disposalOwnedDestroyMisc(p)
			mutate(p)
			disposalOwnedStart(t, m, disposalTestSource(`disposal_evaluation=function(input)error("malformed common frame reached Lua")end,`))
			_, err := m.invokeModule(context.Background(), "buddy-trade", disposalOwnedRequest(t, p, disposalOwnedDestroyInput(t, p)))
			if status.Code(err) != codes.FailedPrecondition || strings.Contains(status.Convert(err).Message(), "malformed common frame reached Lua") ||
				!reflect.DeepEqual(p.calls, disposalOwnedTrace()) || p.writes != 0 {
				t.Fatal("common failure was reinterpreted as optional armor coverage", err, p.calls, p.writes)
			}
		})
	}
}

func TestOwnedDestroyMissingPolicyFactsRemainUnknownWithoutUsingNativeEligibility(t *testing.T) {
	for name, mutate := range map[string]func(*disposalOwnedPeer){
		"missing_worn_template":     func(p *disposalOwnedPeer) { delete(p.inventory.GearContext.Templates, "907001") },
		"missing_item_protection":   func(p *disposalOwnedPeer) { p.journal.Entries[0].ItemProtectionKnown = false },
		"missing_template_presence": func(p *disposalOwnedPeer) { p.item.RawTemplateFacts = nil },
		"malformed_worn_stats": func(p *disposalOwnedPeer) {
			p.inventory.GearContext.Templates["907001"].Stats = []*inventoryv1.GearStat{{Type: 2, Value: 1}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			m, p := disposalOwnedManager(t)
			mutate(p)
			p.inventory.Items[0].SafeJunkSaleEligible = true
			p.inventory.Items[0].ConservativeUpgrade = true
			disposalOwnedStart(t, m, followFallbackBundle(t))
			decision := disposalOwnedRead(t, m, disposalOwnedRequest(t, p, disposalOwnedDestroyInput(t, p)))["decision"].(map[string]any)
			if decision["status"] != "unknown" || p.writes != 0 {
				t.Fatal("missing semantic evidence was replaced by native eligibility", decision, p.writes)
			}
		})
	}
}

func TestOwnedDestroySharedSourceEditsAndFaultsCannotSettlePendingAction(t *testing.T) {
	ctx := context.Background()
	m, p := disposalOwnedManager(t)
	disposalOwnedDestroyMisc(p)
	// Preserve the real bundle's pure classifier and state hooks; this fixture
	// makes only the persistent decision emit one reservable test action.
	source := "local actual=(function()\n" + followFallbackBundle(t) + "\nend)()\n" +
		`actual.decide=function(frame)return {action={kind="owned_destroy_test_pending"}}end
return actual`
	host := disposalOwnedStart(t, m, source)
	dispatches := 0
	_, receipt, err := host.Step(ctx, nil, func(map[string]any) (PolicyDispatch, error) {
		return PolicyDispatch{Module: "buddy-trade", Send: func(context.Context) (behavior.ActionOutcome, map[string]any) {
			dispatches++
			return behavior.ActionUnknown, map[string]any{"requestId": "retained-owned-destroy"}
		}}, nil
	})
	if err != nil || receipt == nil || receipt.Outcome != behavior.ActionUnknown || dispatches != 1 {
		t.Fatal("fixture did not reserve an unknown action", receipt, err, dispatches)
	}
	if err := host.vm.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := host.vm.State(ctx)
	if err != nil || len(before.Pending) != 1 {
		t.Fatal("pending fixture lost native uncertainty", before, err)
	}
	request := disposalOwnedRequest(t, p, disposalOwnedDestroyInput(t, p))
	if decision := disposalOwnedRead(t, m, request)["decision"].(map[string]any); decision["status"] != "eligible" {
		t.Fatal("actual shared classifier did not initially admit miscellaneous item", decision)
	}
	assertFollowFallbackState(t, host, before)
	changed := strings.Replace(source, `return result("eligible","ordinary_gray_miscellaneous")`, `return result("keep","edited_owned_destroy_retention")`, 1)
	if changed == source {
		t.Fatal("actual shared classifier edit did not match")
	}
	revision, err := host.Reload(ctx, changed, host.vm.Status().Revision)
	if err != nil {
		t.Fatal(err)
	}
	before, err = host.vm.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.calls = nil
	result := disposalOwnedRead(t, m, request)
	if result["revision"] != revision || result["decision"].(map[string]any)["status"] != "keep" || !reflect.DeepEqual(p.calls, disposalOwnedTrace()) {
		t.Fatal("accepted edit did not govern same-body owned admission", result, p.calls)
	}
	assertFollowFallbackState(t, host, before)
	broken := strings.Replace(changed, `return result("keep","edited_owned_destroy_retention")`, `error("current owned destroy classifier failed")`, 1)
	if _, err := host.Reload(ctx, broken, revision); err != nil {
		t.Fatal(err)
	}
	before, err = host.vm.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.invokeModule(ctx, "buddy-trade", request); status.Code(err) != codes.FailedPrecondition || !strings.Contains(status.Convert(err).Message(), "current owned destroy classifier failed") {
		t.Fatal("faulted current source fell back to older eligibility", err)
	}
	assertFollowFallbackState(t, host, before)
	pending := host.vm.Outstanding()
	if len(pending) != 1 || pending[0].ActionID != receipt.ActionID || pending[0].Outcome != behavior.ActionUnknown || dispatches != 1 || p.writes != 0 {
		t.Fatal("read-only evaluation settled, replayed or wrote the pending action", pending, dispatches, p.writes)
	}
}

func TestOwnedDestroyRetainsWholeCallbackAndDefaultRuntimeBudgets(t *testing.T) {
	if modcb.DisposalPolicyMaxBytes != 32*1024 || behavior.MaxJSONBytes != 32*1024 || behavior.MaxInputNodes != 4096 {
		t.Fatal("owned disposal raised an existing callback or default runtime budget")
	}
	m, p := disposalOwnedManager(t)
	disposalOwnedStart(t, m, followFallbackBundle(t))
	input := disposalOwnedDestroyInput(t, p)
	input["item"].(map[string]any)["name"] = strings.Repeat("x", modcb.DisposalPolicyMaxBytes)
	if _, err := m.invokeModule(context.Background(), "buddy-trade", disposalOwnedRequest(t, p, input)); status.Code(err) != codes.InvalidArgument || len(p.calls) != 0 || p.writes != 0 {
		t.Fatal("oversized whole owned callback bypassed its existing bound", err, p.calls, p.writes)
	}
	// Exercise the real unconfigured stateless evaluator, separately from the
	// accepted Session export which retains its existing inherited options.
	runtime := behavior.New()
	if _, err := runtime.Reload(`return {decide=function(input)return {count=#input.rows}end}`, ""); err != nil {
		t.Fatal(err)
	}
	if out, err := runtime.Evaluate(map[string]any{"rows": []any{float64(1), float64(2)}}); err != nil || out["count"] != float64(2) {
		t.Fatal("default evaluator fixture was not usable", out, err)
	}
	rows := make([]any, behavior.MaxInputNodes+1)
	for index := range rows {
		rows[index] = float64(index)
	}
	frame := map[string]any{"rows": rows}
	encoded, err := json.Marshal(frame)
	if err != nil || len(encoded) >= behavior.MaxJSONBytes {
		t.Fatal("node-limit fixture accidentally exceeded byte limit", len(encoded), err)
	}
	if _, err := runtime.Evaluate(frame); err == nil {
		t.Fatal("default evaluator accepted more than its existing input node budget")
	}
}
