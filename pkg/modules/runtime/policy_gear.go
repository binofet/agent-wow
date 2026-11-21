package modrt

import (
	"context"
	"encoding/json"
	"strconv"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

const policyGearEntryPoint = "gear_evaluation"

func (p *SessionPolicy) publishGearEvaluation(source, revision string) {
	p.gearEvaluation.Store(&policyFollowFallback{source: source, revision: revision,
		entryPoint: policyGearEntryPoint, available: p.vm.HasExport(policyGearEntryPoint)})
}
func (p *SessionPolicy) gearDecision(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
	current := p.gearEvaluation.Load()
	return p.readonlyExportDecision(ctx, current, func() bool { return p.gearEvaluation.Load() == current }, input)
}

// Compatibility projection is the current accepted Lua export, never a Go
// comparison. An unavailable evaluator leaves raw facts readable and explicit.
func (m *Manager) decorateGearState(ctx context.Context, result proto.Message) {
	state := &inventoryv1.State{}
	raw, err := proto.Marshal(result)
	if err != nil || proto.Unmarshal(raw, state) != nil || state.GearContext == nil {
		return
	}
	m.mu.Lock()
	host := m.policy
	m.mu.Unlock()
	if host == nil {
		return
	}
	catalog, _, err := m.observedSession()
	if err != nil || state.SessionId != catalog.SessionID || strconv.FormatUint(state.SelfGuid, 10) != catalog.CharacterGUID || host.body != catalog.CharacterGUID {
		return
	}
	if _, err = host.observe(ctx, catalog.SessionID, catalog.CharacterGUID); err != nil {
		return
	}
	encoded, err := protojson.Marshal(state)
	if err != nil {
		return
	}
	var input map[string]any
	if json.Unmarshal(encoded, &input) != nil {
		return
	}
	available, revision, decision, err := host.gearDecision(ctx, map[string]any{"inventory": input})
	if !available || err != nil {
		state.GearContext.PolicyError = "current_gear_policy_export_unavailable"
		if err != nil {
			state.GearContext.PolicyError = err.Error()
		}
		encoded, _ := proto.Marshal(state)
		_ = proto.Unmarshal(encoded, result)
		return
	}
	rows, ok := decision["items"].([]any)
	if !ok && len(state.Items) != 0 {
		return
	}
	for _, entry := range state.Items {
		encoded, _ := protojson.Marshal(entry.Item)
		var expected map[string]any
		json.Unmarshal(encoded, &expected)
		for _, value := range rows {
			row, ok := value.(map[string]any)
			if !ok {
				continue
			}
			source, ok := row["item"].(map[string]any)
			if !ok || !sameJSONItemRef(expected, source) {
				continue
			}
			slot, slotOK := jsonUint(row["gearEquipmentSlot"], 18)
			old, oldOK := jsonGUID(row["expectedEquippedItemGuid"])
			if !slotOK || !oldOK {
				continue
			}
			entry.GearEquipEligible, _ = row["gearEquipEligible"].(bool)
			entry.ConservativeUpgrade, _ = row["conservativeUpgrade"].(bool)
			entry.GearEquipmentSlot, entry.ExpectedEquippedItemGuid = uint32(slot), old
			entry.GearBlockedReason, _ = row["gearBlockedReason"].(string)
			break
		}
	}
	state.GearContext.PolicyRevision = revision
	encoded, err = proto.Marshal(state)
	if err == nil {
		_ = proto.Unmarshal(encoded, result)
	}
}
func sameJSONItemRef(a, b map[string]any) bool {
	for _, field := range []string{"bag", "slot", "entry", "stackCount"} {
		x, xok := jsonUint(a[field], 1<<32-1)
		y, yok := jsonUint(b[field], 1<<32-1)
		if !xok || !yok || x != y {
			return false
		}
	}
	x, xok := jsonGUID(a["itemGuid"])
	y, yok := jsonGUID(b["itemGuid"])
	return xok && yok && x == y
}
func jsonUint(value any, max uint64) (uint64, bool) {
	if value == nil {
		return 0, true
	}
	n, ok := value.(float64)
	return uint64(n), ok && n >= 0 && n <= float64(max) && float64(uint64(n)) == n
}
func jsonGUID(value any) (uint64, bool) {
	if value == nil {
		return 0, true
	}
	text, ok := value.(string)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(text, 10, 64)
	return n, err == nil
}

// The old require-upgrade option is evaluated against this body's current Lua
// and fresh module facts, then forwarded as an exact mechanical request.
func (m *Manager) resolveLegacyGearComparison(ctx context.Context, inst *instance, request proto.Message) (proto.Message, error) {
	selected := &inventoryv1.EquipGearRequest{}
	raw, err := proto.Marshal(request)
	if err != nil || proto.Unmarshal(raw, selected) != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid exact gear request")
	}
	if !selected.RequireConservativeUpgrade {
		return request, nil
	}
	state := &inventoryv1.State{}
	method, ok := inst.definition.RPC("state")
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "current inventory facts unavailable")
	}
	if err = m.invoke(ctx, internalCall, "session-policy", "buddy-inventory", inst, method, &emptypb.Empty{}, state); err != nil {
		return nil, err
	}
	catalog, _, err := m.observedSession()
	if err != nil {
		return nil, err
	}
	if state.SessionId != selected.SessionId || selected.SessionId != catalog.SessionID || strconv.FormatUint(state.SelfGuid, 10) != catalog.CharacterGUID ||
		selected.ExpectedRevision != state.Revision || selected.ExpectedInventoryRevision != state.InventoryRevision || selected.ExpectedEquipmentRevision != state.EquipmentRevision || state.GearContext.GetPolicyRevision() == "" {
		return nil, status.Error(codes.FailedPrecondition, "current identity-bound Lua gear evaluation required")
	}
	approved := false
	for _, row := range state.Items {
		if proto.Equal(row.Item, selected.Source) && row.GearEquipmentSlot == selected.EquipmentSlot && row.ExpectedEquippedItemGuid == selected.ExpectedEquippedItemGuid && row.GearEquipEligible && row.ConservativeUpgrade {
			approved = true
			break
		}
	}
	if !approved {
		return nil, status.Error(codes.FailedPrecondition, "current Lua policy did not select this exact upgrade")
	}
	m.mu.Lock()
	host := m.policy
	m.mu.Unlock()
	if host == nil || host.vm.Status().Revision != state.GearContext.PolicyRevision {
		return nil, status.Error(codes.FailedPrecondition, "gear policy changed before exact action admission")
	}
	selected.RequireConservativeUpgrade = false
	raw, _ = proto.Marshal(selected)
	exact := request.ProtoReflect().Type().New().Interface()
	if err = proto.Unmarshal(raw, exact); err != nil {
		return nil, err
	}
	return exact, nil
}
