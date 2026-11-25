package modrt

import (
	"context"
	"encoding/json"
	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"os"
	"testing"
)

func TestActorEquipmentUsesAcceptedLuaAndExactNativeRequest(t *testing.T) {
	for _, leader := range []string{"0", "1"} {
		t.Run("leader-"+leader, func(t *testing.T) {
			m, peer := gearInputManager(t)
			peer.state.Items[0].GearFacts.HasEnhancements = false
			gear, err := os.ReadFile("../../../behaviors/gear.lua")
			if err != nil {
				t.Fatal(err)
			}
			auto, err := os.ReadFile("../../../behaviors/auto-equipment.lua")
			if err != nil {
				t.Fatal(err)
			}
			source := "local gear=(function()\n" + string(gear) + "\nend)()\nlocal auto=(function()\n" + string(auto) + "\nend)()\n" + `
return {on_event=auto.on_event,export_state=auto.export_state,import_state=auto.import_state,
decide=function(f) return auto.decide(f,{sessionId=f.session_id,selfGuid="6",party={leaderGuid="` + leader + `"},players={["6"]={present=true,aliveKnown=true,alive=true,combatKnown=true,inCombat=false,movementKnown=true,moving=false}}},#f.pending==0,gear) end}`
			host, err := m.StartPolicyWithOptions(context.Background(), source, behavior.SessionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := protojson.Marshal(peer.state)
			var view map[string]any
			json.Unmarshal(raw, &view)
			if _, err = host.Enqueue(behavior.Event{Kind: "inventory_observation", Value: view}); err != nil {
				t.Fatal(err)
			}
			out, receipt, err := host.Step(context.Background(), map[string]any{"kind": "tick"}, nil)
			if err != nil || receipt == nil || len(peer.writes) != 1 {
				t.Fatal(out, receipt, err, peer.writes)
			}
			req := peer.writes[0]
			if req.Source.ItemGuid != peer.state.Items[0].Item.ItemGuid || req.EquipmentSlot != 6 || req.ExpectedEquippedItemGuid != peer.state.Equipment[6].ItemGuid || req.RequireConservativeUpgrade {
				t.Fatal(req)
			}
			// The RPC response contains no correlated operation: never infer a failed
			// send or retry an uncertain request, including through source checkpoint.
			if _, err = host.Reload(context.Background(), source+"\n-- accepted edit", host.vm.Status().Revision); err != nil {
				t.Fatal(err)
			}
			if _, err = host.Enqueue(behavior.Event{Kind: "inventory_observation", Value: view}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				if _, _, err = host.Step(context.Background(), map[string]any{"kind": "tick"}, nil); err != nil {
					t.Fatal(err)
				}
			}
			if len(peer.writes) != 1 {
				t.Fatal("uncertain equipment repeated", peer.writes)
			}
			// Only the matching terminal native operation plus actual equipment
			// and displaced-item observations can release this Lua attempt.
			peer.state.Operation = &inventoryv1.Operation{Kind: "equip_gear", RequestId: req.RequestId, Status: "CONFIRMED", LeaseReleased: true, InventoryConfirmed: true, EquipmentConfirmed: true, ObservedInventoryRevision: 3, ObservedEquipmentRevision: 4}
			peer.state.InventoryRevision = 3
			peer.state.EquipmentRevision = 4
			peer.state.Equipment[6].ItemGuid = req.Source.ItemGuid
			peer.state.InventoryBags = nil
			raw, _ = protojson.Marshal(peer.state)
			json.Unmarshal(raw, &view)
			host.Enqueue(behavior.Event{Kind: "inventory_observation", Value: view})
			out, _, err = host.Step(context.Background(), map[string]any{"kind": "tick"}, nil)
			if err != nil || out["phase"] != "equipment_swap_not_observed" {
				t.Fatal(out, err)
			}
			// Deliberately incomplete swap proof remains blocked, never a retry.
			if len(peer.writes) != 1 {
				t.Fatal(peer.writes)
			}

			peer.state.Equipment[6].Entry = req.Source.Entry
			peer.state.InventoryBags = []*worldv1.InventoryBag{{Bag: req.Source.Bag, Slots: []*worldv1.BackpackSlot{{Slot: req.Source.Slot, ItemGuid: req.ExpectedEquippedItemGuid, Entry: 99, StackCount: 1, MetadataKnown: true}}}}
			peer.state.Items = nil
			raw, _ = protojson.Marshal(peer.state)
			view = nil
			json.Unmarshal(raw, &view)
			host.Enqueue(behavior.Event{Kind: "inventory_observation", Value: view})
			out, _, err = host.Step(context.Background(), map[string]any{"kind": "tick"}, nil)
			if err != nil || out["phase"] != "equipment_confirmed" || len(peer.writes) != 1 {
				t.Fatal(out, err, peer.writes)
			}
		})
	}
}

func TestActorEquipmentWaitsForActiveUnknownAndLegalDestination(t *testing.T) {
	for _, mode := range []string{"active", "closing", "unknown", "illegal", "enhanced", "busy-policy"} {
		t.Run(mode, func(t *testing.T) {
			m, peer := gearInputManager(t)
			peer.state.Items[0].GearFacts.HasEnhancements = false
			switch mode {
			case "active":
				peer.state.Active = true
			case "closing":
				peer.state.Closing = true
			case "unknown":
				peer.state.Operation = &inventoryv1.Operation{Kind: "equip_gear", Status: "OUTCOME_UNCONFIRMED", RequestId: "old"}
			case "illegal":
				peer.state.Items[0].GearFacts.Destinations[0].Eligible = false
			case "enhanced":
				peer.state.Items[0].GearFacts.HasEnhancements = true
			}
			gear, _ := os.ReadFile("../../../behaviors/gear.lua")
			auto, _ := os.ReadFile("../../../behaviors/auto-equipment.lua")
			source := "local gear=(function()\n" + string(gear) + "\nend)()\nlocal auto=(function()\n" + string(auto) + "\nend)()\n" + `
return {on_event=auto.on_event,export_state=auto.export_state,import_state=auto.import_state,
decide=function(f)return auto.decide(f,{sessionId=f.session_id,selfGuid="6",players={["6"]={present=true,aliveKnown=true,alive=true,combatKnown=true,inCombat=false,movementKnown=true,moving=false}}},f.input.available,gear)end}`
			host, err := m.StartPolicyWithOptions(context.Background(), source, behavior.SessionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			before := proto.Clone(peer.state)
			raw, _ := protojson.Marshal(peer.state)
			var view map[string]any
			json.Unmarshal(raw, &view)
			host.Enqueue(behavior.Event{Kind: "inventory_observation", Value: view})
			out, receipt, err := host.Step(context.Background(), map[string]any{"available": mode != "busy-policy"}, nil)
			if err != nil || receipt != nil || out["action"] != nil || len(peer.writes) != 0 || !proto.Equal(before, peer.state) {
				t.Fatal(out, receipt, err, peer.writes)
			}
		})
	}
}
