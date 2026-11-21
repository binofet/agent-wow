package modrt

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"

	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/dynamicpb"
)

type policyCorpse struct {
	close bool
	money bool
	slot  *byte
}

func (p *SessionPolicy) resolveCorpseLoot(action map[string]any, capability string) (PolicyDispatch, error) {
	sid, _ := action["sessionId"].(string)
	self, _ := action["selfGuid"].(string)
	if sid != p.vm.Status().SessionID || self != p.body {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "current session and exact owned body required")
	}
	params := map[string]any{"sessionId": sid, "selfGuid": self, "corpseGuid": action["corpseGuid"]}
	if capability != "loot_open" && action["sourceGuid"] != nil {
		params["sourceGuid"] = action["sourceGuid"]
	}
	method := "close_corpse"
	if value, ok := action["retireUnconfirmed"]; ok {
		params["retireUnconfirmed"] = value
	}
	if capability == "loot_money" || capability == "loot_slot" {
		method = "take_money"
		if capability == "loot_slot" {
			method = "take_slot"
		}
		for _, key := range []string{"openRequestId", "windowRevision", "requestId", "slot", "expectedItemId", "expectedCount", "expectedInventoryRevision"} {
			if value, ok := action[key]; ok {
				params[key] = value
			}
		}
	}
	if capability == "loot_open" {
		method = "open_corpse"
		for _, key := range []string{"requestId", "expectedPositionRevision", "expectedCorpseRevision", "expectedInventoryRevision"} {
			params[key] = action[key]
		}
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return PolicyDispatch{}, err
	}
	inst, rpc, err := p.manager.lookup("buddy-loot", method, true)
	if err != nil {
		return PolicyDispatch{}, err
	}
	req := dynamicpb.NewMessage(rpc.Descriptor().Input())
	if err = protojson.Unmarshal(encoded, req); err != nil {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, err.Error())
	}
	selected := &lootv1.CloseCorpseRequest{}
	base, _ := json.Marshal(map[string]any{"sessionId": sid, "selfGuid": self, "corpseGuid": action["corpseGuid"], "sourceGuid": action["sourceGuid"]})
	if err = protojson.Unmarshal(base, selected); err != nil || selected.CorpseGuid == 0 && selected.SourceGuid == 0 || selected.CorpseGuid != 0 && selected.SourceGuid != 0 {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "one exact observed loot source GUID required")
	}
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return PolicyDispatch{}, err
	}
	target := selected.CorpseGuid
	if target == 0 {
		target = selected.SourceGuid
	}
	op := &policySpell{vm: p.vm, capability: capability, target: target,
		corpse: &policyCorpse{close: capability == "loot_close"}, token: hex.EncodeToString(nonce[:])}
	if capability == "loot_money" {
		op.corpse.money = true
	}
	if capability == "loot_slot" {
		item := &lootv1.TakeLootRequest{}
		if err = protojson.Unmarshal(encoded, item); err != nil || item.Slot > 255 {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "exact loot slot required")
		}
		slot := byte(item.Slot)
		op.corpse.slot = &slot
	}
	return PolicyDispatch{Module: "buddy-loot", Send: func(ctx context.Context) (behavior.ActionOutcome, map[string]any) {
		origin := modsession.WriteOriginFromContext(ctx)
		payload := map[string]any{"capability": capability, "source_guid": strconv.FormatUint(op.target, 10), "corpse_guid": strconv.FormatUint(selected.CorpseGuid, 10), "request_id": action["requestId"]}
		if origin.Module != "buddy-loot" || origin.Writer != "lua" || origin.ActionID == "" {
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
		result := dynamicpb.NewMessage(rpc.Descriptor().Output())
		err := p.manager.invoke(modcb.WithPolicySpell(ctx, op.token), publicCall, "lua", "buddy-loot", inst, rpc, req, result)
		registry.mu.Lock()
		attempted, written := op.attempted, op.written
		registry.mu.Unlock()
		payload["opening_transport_attempted"], payload["transport_written"] = attempted, written
		if err != nil {
			payload["grpc_status"], payload["grpc_message"] = status.Code(err).String(), status.Convert(err).Message()
			if !attempted {
				switch status.Code(err) {
				case codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound, codes.PermissionDenied:
					return behavior.ActionNotDispatched, payload
				}
			}
			// A source-bound native receipt, not replay of the RPC, reconciles
			// unknown transport. Loot uncertainty cannot block healing/defense.
			return behavior.ActionSettledUnverified, payload
		}
		raw, _ := protojson.Marshal(result)
		var receipt map[string]any
		if json.Unmarshal(raw, &receipt) == nil {
			payload["result"] = receipt
			if capability == "loot_open" && receipt["openingTransportAttempted"] != true && !attempted {
				return behavior.ActionNotDispatched, payload
			}
			if (capability == "loot_money" || capability == "loot_slot") && receipt["transportAttempted"] != true && !attempted {
				return behavior.ActionNotDispatched, payload
			}
		}
		return behavior.ActionSettledUnverified, payload
	}}, nil
}
