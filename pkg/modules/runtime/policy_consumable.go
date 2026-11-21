package modrt

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strconv"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/dynamicpb"
)

const consumableInventoryModule = "buddy-inventory"

type policyConsumable struct {
	source    *inventoryv1.ItemRef
	requestID string
}

func (p *SessionPolicy) resolveConsumable(action map[string]any) (PolicyDispatch, error) {
	sid, _ := action["sessionId"].(string)
	self, _ := action["selfGuid"].(string)
	kind, _ := action["kind"].(string)
	if sid != p.vm.Status().SessionID || self != p.body || (kind != "drink" && kind != "eat_buff_food") {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "current body and exact consumable kind required")
	}
	params := map[string]any{"sessionId": sid, "selfGuid": self, "source": action["source"], "spellId": action["spellId"], "expectedInventoryRevision": action["expectedInventoryRevision"], "requestId": action["requestId"]}
	body, err := json.Marshal(params)
	if err != nil {
		return PolicyDispatch{}, err
	}
	selected := &inventoryv1.DrinkRequest{}
	if err = protojson.Unmarshal(body, selected); err != nil || selected.Source == nil || selected.SpellId == 0 || selected.ExpectedInventoryRevision == 0 || selected.RequestId == "" {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "exact consumable source, use spell, revision and request ID required")
	}
	inst, method, err := p.manager.lookup(consumableInventoryModule, kind, true)
	if err != nil {
		return PolicyDispatch{}, err
	}
	req := dynamicpb.NewMessage(method.Descriptor().Input())
	if err = protojson.Unmarshal(body, req); err != nil {
		return PolicyDispatch{}, err
	}
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return PolicyDispatch{}, err
	}
	op := &policySpell{vm: p.vm, capability: "consume", spell: selected.SpellId, target: selected.SelfGuid, token: hex.EncodeToString(nonce[:]), consumable: &policyConsumable{source: selected.Source, requestID: selected.RequestId}}
	return PolicyDispatch{Module: consumableInventoryModule, Send: func(ctx context.Context) (behavior.ActionOutcome, map[string]any) {
		payload := map[string]any{"capability": "consume", "kind": kind, "requestId": selected.RequestId, "itemGuid": strconv.FormatUint(selected.Source.ItemGuid, 10), "reconciliationRequired": true}
		origin := modsession.WriteOriginFromContext(ctx)
		if origin.Writer != "lua" || origin.Module != consumableInventoryModule || origin.ActionID == "" {
			return behavior.ActionNotDispatched, payload
		}
		ready, err := grpc_health_v1.NewHealthClient(inst.conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: modcb.PolicyConsumableService})
		if err != nil || ready.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
			payload["error"] = "exact_consumable_bridge_unavailable"
			return behavior.ActionNotDispatched, payload
		}
		registry := &p.manager.policySpells
		registry.mu.Lock()
		op.origin, op.admitted = origin, true
		if registry.active == nil {
			registry.active = map[string]*policySpell{}
		}
		registry.active[op.token] = op
		registry.mu.Unlock()
		defer func() {
			registry.mu.Lock()
			op.admitted = false
			delete(registry.active, op.token)
			registry.mu.Unlock()
		}()
		result := dynamicpb.NewMessage(method.Descriptor().Output())
		err = p.manager.invoke(modcb.WithPolicySpell(ctx, op.token), publicCall, "lua", consumableInventoryModule, inst, method, req, result)
		registry.mu.Lock()
		attempted, written := op.attempted, op.written
		registry.mu.Unlock()
		payload["transport_written"] = written
		if err != nil {
			payload["error"] = err.Error()
			if !attempted {
				switch status.Code(err) {
				case codes.InvalidArgument, codes.FailedPrecondition, codes.PermissionDenied, codes.NotFound:
					return behavior.ActionNotDispatched, payload
				}
			}
			return behavior.ActionSettledUnverified, payload
		}
		raw, marshalErr := protojson.Marshal(result)
		var receipt map[string]any
		if marshalErr == nil && json.Unmarshal(raw, &receipt) == nil {
			payload["result"] = receipt
		}
		// Consumption/aura observations arrive in the Inventory read feed. Keep
		// the exact intent in Lua without blocking urgent healing or defense.
		return behavior.ActionSettledUnverified, payload
	}}, nil
}

func (op *policySpell) matchesConsumablePacket(code uint32, payload []byte) bool {
	item := op.consumable.source
	bag, slot := byte(0), byte(0)
	if item.Bag == 0 && item.Slot < 16 {
		bag, slot = 255, byte(23+item.Slot)
	} else if item.Bag >= 1 && item.Bag <= 4 && item.Slot < 36 {
		bag, slot = byte(18+item.Bag), byte(item.Slot)
	} else {
		return false
	}
	if code != opcode.CMSGUseItem || len(payload) != 24 || payload[0] != bag || payload[1] != slot || binary.LittleEndian.Uint32(payload[3:]) != op.spell || binary.LittleEndian.Uint64(payload[7:]) != item.ItemGuid {
		return false
	}
	for _, value := range payload[15:] {
		if value != 0 {
			return false
		}
	}
	return true
}
