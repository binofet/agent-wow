package modrt

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strconv"

	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/dynamicpb"
)

// A gather proposal binds one caller-selected opening spell and object. It
// cannot authorize a different cast, a movement packet or a later retry.
type policyGather struct{ openSpell uint32 }

func gatherCounter(value any) bool {
	text, ok := value.(string)
	n, err := strconv.ParseUint(text, 10, 64)
	return ok && err == nil && n != 0 && strconv.FormatUint(n, 10) == text
}

func (p *SessionPolicy) resolveGatherLoot(action map[string]any) (PolicyDispatch, error) {
	sid, _ := action["sessionId"].(string)
	self, _ := action["selfGuid"].(string)
	if sid == "" || sid != p.vm.Status().SessionID || self != p.body || !gatherCounter(self) {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "current owned session body required")
	}
	params := map[string]any{"sessionId": sid, "selfGuid": self}
	for _, key := range []string{"sourceGuid", "visitId", "expectedPositionRevision", "expectedObjectRevision", "expectedInventoryRevision", "expectedQuestLogRevision"} {
		if !gatherCounter(action[key]) {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "exact gather source visit and revisions required")
		}
		params[key] = action[key]
	}
	for _, key := range []string{"questId", "itemId", "openSpell"} {
		value, ok := jsonUint(action[key], 1<<32-1)
		if !ok || value == 0 {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "exact positive gather recipe fields required")
		}
		params[key] = action[key]
	}
	requestID, _ := action["requestId"].(string)
	approach, approachOK := action["approach"].(bool)
	if requestID == "" || len(requestID) > 128 || !approachOK {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "exact request ID and explicit approach choice required")
	}
	params["requestId"], params["approach"] = requestID, approach
	encoded, err := json.Marshal(params)
	if err != nil {
		return PolicyDispatch{}, err
	}
	selected := &lootv1.OpenGatherRequest{}
	if err = protojson.Unmarshal(encoded, selected); err != nil {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, err.Error())
	}
	inst, method, err := p.manager.lookup("buddy-loot", "open_gather", true)
	if err != nil {
		return PolicyDispatch{}, err // No legacy opening or native selector fallback.
	}
	expected := selected.ProtoReflect().Descriptor()
	if method.Path() != lootv1.BuddyLoot_OpenGather_FullMethodName ||
		method.Descriptor().Input().FullName() != expected.FullName() ||
		method.Descriptor().Output().FullName() != (&lootv1.CorpseOperation{}).ProtoReflect().Descriptor().FullName() {
		return PolicyDispatch{}, status.Error(codes.FailedPrecondition, "installed exact gather contract unavailable")
	}
	for i := 0; i < expected.Fields().Len(); i++ {
		want := expected.Fields().Get(i)
		field := method.Descriptor().Input().Fields().ByName(want.Name())
		if field == nil || field.Number() != want.Number() || field.Kind() != want.Kind() || field.IsList() || field.IsMap() {
			return PolicyDispatch{}, status.Error(codes.FailedPrecondition, "installed gather request is incompatible")
		}
	}
	request := dynamicpb.NewMessage(method.Descriptor().Input())
	if err = protojson.Unmarshal(encoded, request); err != nil {
		return PolicyDispatch{}, status.Error(codes.FailedPrecondition, err.Error())
	}
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return PolicyDispatch{}, err
	}
	op := &policySpell{vm: p.vm, capability: "loot_gather", target: selected.SourceGuid,
		gather: &policyGather{openSpell: selected.OpenSpell}, token: hex.EncodeToString(nonce[:])}
	return PolicyDispatch{Module: "buddy-loot", Send: func(ctx context.Context) (behavior.ActionOutcome, map[string]any) {
		payload := map[string]any{"capability": "loot_gather", "source_guid": action["sourceGuid"], "request_id": requestID,
			"opening_transport_attempted": false, "transport_written": false}
		origin := modsession.WriteOriginFromContext(ctx)
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
		result := dynamicpb.NewMessage(method.Descriptor().Output())
		err := p.manager.invoke(modcb.WithPolicySpell(ctx, op.token), publicCall, "lua", "buddy-loot", inst, method, request, result)
		registry.mu.Lock()
		attempted, written := op.attempted, op.written
		registry.mu.Unlock()
		payload["opening_transport_attempted"], payload["transport_written"] = attempted, written
		if err != nil {
			payload["grpc_status"], payload["grpc_message"] = status.Code(err).String(), status.Convert(err).Message()
			if !attempted {
				switch status.Code(err) {
				case codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound, codes.PermissionDenied, codes.Unimplemented:
					return behavior.ActionNotDispatched, payload
				}
			}
			payload["reconciliationRequired"] = true
			return behavior.ActionSettledUnverified, payload
		}
		raw, _ := protojson.Marshal(result)
		var receipt map[string]any
		typed := &lootv1.CorpseOperation{}
		if json.Unmarshal(raw, &receipt) != nil || protojson.Unmarshal(raw, typed) != nil || typed.SessionId != selected.SessionId ||
			typed.SelfGuid != selected.SelfGuid || typed.SourceGuid != selected.SourceGuid || typed.RequestId != selected.RequestId {
			payload["reconciliationRequired"] = true
			return behavior.ActionSettledUnverified, payload
		}
		payload["result"] = receipt
		if typed.Terminal && typed.Phase == "not_dispatched" && !typed.OpeningTransportAttempted && !attempted {
			return behavior.ActionNotDispatched, payload
		}
		// A selected approach may own native source custody before the opening
		// packet exists. Its later mechanical writes are not this RPC's Lua
		// transport; no dead callback token or same-request replay is retained.
		payload["source_reserved"], payload["reconciliationRequired"] = true, true
		return behavior.ActionSettledUnverified, payload
	}}, nil
}

func (op *policySpell) matchesGatherPacket(code uint32, body []byte) bool {
	if op.gather == nil || code != opcode.CMSGCastSpell || len(body) < 11 {
		return false
	}
	want := make([]byte, 11)
	want[0] = body[0] // Native cast-count sequence, not gameplay spell selection.
	binary.LittleEndian.PutUint32(want[1:], op.gather.openSpell)
	binary.LittleEndian.PutUint32(want[6:], 0x800) // TARGET_FLAG_GAMEOBJECT.
	for i := uint(0); i < 8; i++ {
		if b := byte(op.target >> (i * 8)); b != 0 {
			want[10] |= 1 << i
			want = append(want, b)
		}
	}
	return bytes.Equal(body, want)
}
