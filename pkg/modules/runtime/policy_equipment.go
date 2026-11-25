package modrt

import (
	"context"
	"encoding/json"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Lua selects the exact item and legal destination. Native Inventory owns fresh
// gates, the finite interaction lease, idempotency and observed swap proof.
func (p *SessionPolicy) resolveGearEquip(action map[string]any) (PolicyDispatch, error) {
	if action["sessionId"] != p.vm.Status().SessionID || action["selfGuid"] != p.body {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "current equipment body required")
	}
	params := map[string]any{}
	for _, key := range []string{"sessionId", "requestId", "expectedRevision", "expectedInventoryRevision", "source", "equipmentSlot", "expectedEquipmentRevision", "expectedEquippedItemGuid"} {
		params[key] = action[key]
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return PolicyDispatch{}, err
	}
	selected := &inventoryv1.EquipGearRequest{}
	if err = protojson.Unmarshal(raw, selected); err != nil || selected.Source == nil || selected.Source.ItemGuid == 0 || selected.Source.StackCount != 1 || selected.RequestId == "" || selected.ExpectedRevision == 0 || selected.ExpectedInventoryRevision == 0 || selected.ExpectedEquipmentRevision == 0 {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "exact equipment request required")
	}
	inst, method, err := p.manager.lookup("buddy-inventory", "equip_gear", true)
	if err != nil {
		return PolicyDispatch{}, err
	}
	req := dynamicpb.NewMessage(method.Descriptor().Input())
	if err = protojson.Unmarshal(raw, req); err != nil {
		return PolicyDispatch{}, err
	}
	return PolicyDispatch{Module: "buddy-inventory", Send: func(ctx context.Context) (behavior.ActionOutcome, map[string]any) {
		payload := map[string]any{"capability": "equip_gear", "requestId": selected.RequestId, "reconciliationRequired": true}
		origin := modsession.WriteOriginFromContext(ctx)
		if origin.Writer != "lua" || origin.Module != "buddy-inventory" || origin.ActionID == "" {
			return behavior.ActionNotDispatched, payload
		}
		result := dynamicpb.NewMessage(method.Descriptor().Output())
		if err := p.manager.invoke(ctx, publicCall, "lua", "buddy-inventory", inst, method, req, result); err != nil {
			// An RPC error alone cannot prove that Inventory never reserved or sent.
			payload["error"] = err.Error()
			return behavior.ActionSettledUnverified, payload
		}
		encoded, err := protojson.Marshal(result)
		var view map[string]any
		if err == nil && json.Unmarshal(encoded, &view) == nil {
			// Keep the action journal bounded; full owned item facts remain in
			// the normal inventory observation feed. The correlated native
			// operation receipt is retained without inferring completion.
			resultView := map[string]any{}
			for _, key := range []string{"sessionId", "selfGuid", "revision", "active", "operation"} {
				if value, ok := view[key]; ok {
					resultView[key] = value
				}
			}
			payload["result"] = resultView
		}
		return behavior.ActionSettledUnverified, payload
	}}, nil
}
