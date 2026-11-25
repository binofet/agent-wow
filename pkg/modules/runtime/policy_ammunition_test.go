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

func (p *gearInputPeer) SelectAmmo(_ context.Context, r *inventoryv1.SelectAmmoRequest) (*inventoryv1.State, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ammoWrites = append(p.ammoWrites, proto.Clone(r).(*inventoryv1.SelectAmmoRequest))
	return proto.Clone(p.state).(*inventoryv1.State), nil
}
func TestAmmoLuaDecisionUsesTypedNativeRequestAndRetainsUnknownAcrossReload(t *testing.T) {
	m, peer := gearInputManager(t)
	peer.state.Items = []*inventoryv1.ItemEligibility{{Item: &inventoryv1.ItemRef{Slot: 2, ItemGuid: 0x4000000000000031, Entry: 11285, StackCount: 200}, MetadataKnown: true, AmmoSelectEligible: true}}
	peer.state.Equipment[17] = &worldv1.EquipmentSlotState{Slot: 17, SlotKnown: true, ItemGuid: 0x4000000000000033, Entry: 9400, StackCount: 1, MetadataKnown: true}
	peer.state.AmmoKnown, peer.state.AmmoRevision = true, 1
	read := func(name string) string {
		b, e := os.ReadFile("../../../behaviors/" + name)
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	source := "local gear=(function()\n" + read("gear.lua") + "\nend)()\nlocal auto=(function()\n" + read("auto-equipment.lua") + "\nend)()\n" + `
return {on_event=auto.on_event,export_state=auto.export_state,import_state=auto.import_state,
decide=function(f) local out=auto.decide(f,{sessionId=f.session_id,selfGuid="6",players={["6"]={present=true,aliveKnown=true,alive=true,combatKnown=true,inCombat=false,movementKnown=true,moving=false}}},#f.pending==0,gear);auto.commit(out.action);return out end}`
	host, err := m.StartPolicyWithOptions(context.Background(), source, behavior.SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := protojson.Marshal(peer.state)
	var v map[string]any
	json.Unmarshal(raw, &v)
	if _, err = host.Enqueue(behavior.Event{Kind: "inventory_observation", Value: v}); err != nil {
		t.Fatal(err)
	}
	out, receipt, err := host.Step(context.Background(), map[string]any{"kind": "tick"}, nil)
	if err != nil || receipt == nil || len(peer.ammoWrites) != 1 || len(peer.writes) != 0 {
		t.Fatal(out, receipt, err, peer.ammoWrites)
	}
	r := peer.ammoWrites[0]
	if r.Source.Entry != 11285 || r.Source.StackCount != 200 || r.ExpectedAmmoRevision != 1 || r.ExpectedAmmoEntry != 0 || r.ExpectedRangedItemGuid != peer.state.Equipment[17].ItemGuid {
		t.Fatal(r)
	}
	if _, err = host.Reload(context.Background(), source+"\n-- source changed without clearing custody", host.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 3; n++ {
		host.Enqueue(behavior.Event{Kind: "inventory_observation", Value: v})
		if _, _, err = host.Step(context.Background(), map[string]any{"kind": "tick"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(peer.ammoWrites) != 1 {
		t.Fatal("unconfirmed ammo repeated", peer.ammoWrites)
	}
	peer.state.AmmoEntry = 11285
	peer.state.AmmoRevision = 2
	peer.state.Operation = &inventoryv1.Operation{Kind: "select_ammo", RequestId: r.RequestId, Status: "CONFIRMED", LeaseReleased: true, AmmoConfirmed: true, InventoryConfirmed: true, EquipmentConfirmed: true}
	raw, _ = protojson.Marshal(peer.state)
	v = nil
	json.Unmarshal(raw, &v)
	host.Enqueue(behavior.Event{Kind: "inventory_observation", Value: v})
	out, _, err = host.Step(context.Background(), map[string]any{"kind": "tick"}, nil)
	if err != nil || out["phase"] != "ammunition_confirmed" || len(peer.ammoWrites) != 1 {
		t.Fatal(out, err, peer.ammoWrites)
	}
	host.Enqueue(behavior.Event{Kind: "inventory_observation", Value: v})
	out, _, err = host.Step(context.Background(), map[string]any{"kind": "tick"}, nil)
	if err != nil || out["phase"] != "ammunition_ready" || len(peer.ammoWrites) != 1 {
		t.Fatal(out, err, peer.ammoWrites)
	}
}
