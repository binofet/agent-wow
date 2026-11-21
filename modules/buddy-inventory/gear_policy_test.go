package buddyinventory

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func gearTestValue(t *testing.T, message proto.Message) map[string]any {
	t.Helper()
	raw, err := protojson.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func runGearPolicy(t *testing.T, input map[string]any, comparison bool) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../behaviors/gear.lua")
	if err != nil {
		t.Fatal(err)
	}
	export := "function(frame)return gear.evaluate(frame.input)end"
	if comparison {
		export = `function(frame)local f=frame.input;local upgrade,score,reason=gear.compare(f.candidate,f.source,f.current,f.equipped,f.options);return {upgrade=upgrade,score=score,reason=reason}end`
	}
	source := "local gear=(function()\n" + string(raw) + "\nend)()\nreturn {decide=" + export + "}"
	vm, err := behavior.NewSessionWithOptions(context.Background(), "gear-test", source, behavior.SessionOptions{ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Close()
	out, err := vm.Decide(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	return out.Output
}
func stateGear(t *testing.T, s *Service) *inventoryv1.State {
	t.Helper()
	state := stateInventory(t, s)
	out := runGearPolicy(t, map[string]any{"inventory": gearTestValue(t, state)}, false)
	merged := gearTestValue(t, state)
	merged["items"] = out["items"]
	raw, err := json.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	if err = (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, state); err != nil {
		t.Fatal(err)
	}
	return state
}
func compareGearPolicy(t *testing.T, candidate itemMetadata, carried *worldv1.BackpackSlot, current itemMetadata, equipped *worldv1.EquipmentSlotState) bool {
	t.Helper()
	out := runGearPolicy(t, map[string]any{"candidate": gearTestValue(t, gearTemplate(candidate)), "source": gearTestValue(t, carried), "current": gearTestValue(t, gearTemplate(current)), "equipped": gearTestValue(t, equipped)}, true)
	return out["upgrade"] == true
}

// Native packet/effect fixtures use the same Lua comparison as the public host
// bridge before forwarding an exact action. No Go upgrade formula lives here.
func equipGearPolicyTest(t *testing.T, s *Service, r *inventoryv1.EquipGearRequest) (*inventoryv1.State, error) {
	t.Helper()
	exact := proto.Clone(r).(*inventoryv1.EquipGearRequest)
	if r.RequireConservativeUpgrade {
		state := stateGear(t, s)
		approved := false
		for _, row := range state.Items {
			approved = approved || proto.Equal(row.Item, r.Source) && row.GearEquipEligible && row.ConservativeUpgrade && row.GearEquipmentSlot == r.EquipmentSlot && row.ExpectedEquippedItemGuid == r.ExpectedEquippedItemGuid
		}
		if !approved {
			return nil, status.Error(codes.FailedPrecondition, "current shared Lua did not select this upgrade")
		}
		exact.RequireConservativeUpgrade = false
	}
	return s.EquipGear(context.Background(), exact)
}
