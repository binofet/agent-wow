package modrt

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"math"
	"strconv"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Lua chooses the owner, lifetime and reason. Only the distinct installed idle
// contract can receive this exact-body request; legacy acquisition is no fallback.
func (p *SessionPolicy) resolveIdleInteraction(action map[string]any) (PolicyDispatch, error) {
	sid, _ := action["sessionId"].(string)
	self, _ := action["selfGuid"].(string)
	visit, _ := action["visitId"].(string)
	owner, _ := action["owner"].(string)
	token, tokenOK := action["token"].(string)
	body, bodyErr := strconv.ParseUint(self, 10, 64)
	visitID, visitErr := strconv.ParseUint(visit, 10, 64)
	if sid == "" || sid != p.vm.Status().SessionID || self != p.body || bodyErr != nil || body == 0 || self != strconv.FormatUint(body, 10) ||
		visitErr != nil || visitID == 0 || visit != strconv.FormatUint(visitID, 10) || owner == "" || action["token"] != nil && !tokenOK {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "current session body visit and interaction owner required")
	}
	ms, msOK := action["leaseMs"].(float64)
	if action["leaseMs"] != nil && !msOK || math.IsNaN(ms) || math.IsInf(ms, 0) || ms < 0 || ms > math.MaxUint32 || ms != math.Trunc(ms) {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "finite integer leaseMs required")
	}
	inst, method, err := p.manager.lookup(actionsModule, "acquire_idle_interaction", true)
	if err != nil {
		return PolicyDispatch{}, err
	}
	requestType := (&actionsv1.IdleInteractionRequest{}).ProtoReflect().Descriptor()
	if method.Path() != actionsv1.BuddyActions_AcquireIdleInteraction_FullMethodName ||
		method.Descriptor().Input().FullName() != requestType.FullName() ||
		method.Descriptor().Output().FullName() != (&actionsv1.InteractionLease{}).ProtoReflect().Descriptor().FullName() {
		return PolicyDispatch{}, status.Error(codes.FailedPrecondition, "installed idle interaction contract unavailable")
	}
	values := map[protoreflect.Name]protoreflect.Value{
		"session_id": protoreflect.ValueOfString(sid), "self_guid": protoreflect.ValueOfUint64(body),
		"visit_id": protoreflect.ValueOfUint64(visitID), "owner": protoreflect.ValueOfString(owner),
		"token": protoreflect.ValueOfString(token), "lease_ms": protoreflect.ValueOfUint32(uint32(ms)),
	}
	request := dynamicpb.NewMessage(method.Descriptor().Input())
	for name, value := range values {
		field := request.Descriptor().Fields().ByName(name)
		expected := requestType.Fields().ByName(name)
		if field == nil || field.IsList() || field.IsMap() || field.Number() != expected.Number() || !fieldMatchesValue(field.Kind(), value) {
			return PolicyDispatch{}, status.Error(codes.FailedPrecondition, "installed idle interaction request is incompatible")
		}
		request.Set(field, value)
	}
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return PolicyDispatch{}, err
	}
	op := &policySpell{vm: p.vm, capability: "acquire_idle_interaction", token: hex.EncodeToString(nonce[:])}
	return PolicyDispatch{Module: actionsModule, Send: func(ctx context.Context) (behavior.ActionOutcome, map[string]any) {
		payload := map[string]any{"capability": "acquire_idle_interaction", "owner": owner, "visitId": visit,
			"transport_written": false, "effect_observed": false}
		origin := modsession.WriteOriginFromContext(ctx)
		if origin.Writer != "lua" || origin.Module != actionsModule || origin.ActionID == "" {
			return behavior.ActionNotDispatched, payload
		}
		ready, err := grpc_health_v1.NewHealthClient(inst.conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: modcb.PolicySpellService})
		if err != nil || ready.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
			payload["error"] = "idle_interaction_callback_bridge_unavailable"
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
		lease := &actionsv1.InteractionLease{}
		err = p.manager.invoke(modcb.WithPolicySpell(ctx, op.token), publicCall, "lua", actionsModule, inst, method, request, lease)
		if err != nil {
			payload["error"] = err.Error()
			switch status.Code(err) {
			case codes.InvalidArgument, codes.FailedPrecondition, codes.PermissionDenied, codes.NotFound, codes.Unimplemented:
				return behavior.ActionNotDispatched, payload
			}
			// The finite lease may exist even if its reply was lost. Lua retains
			// that uncertainty and reconciles the observed token; no global spell
			// receipt or automatic acquisition replay is invented here.
			payload["reconciliationRequired"] = true
			return behavior.ActionSettledUnverified, payload
		}
		state := lease.GetState()
		if lease.GetToken() == "" || state.GetSessionId() != sid || state.GetSelfGuid() != body ||
			state.GetInteractionOwner() != owner || state.GetInteractionHarvest() || state.GetInteractionLeaseRemainingMs() == 0 ||
			token != "" && lease.GetToken() != token {
			payload["error"] = "idle interaction lease reply is not bound to the request"
			payload["reconciliationRequired"] = true
			return behavior.ActionSettledUnverified, payload
		}
		raw, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(lease)
		var result map[string]any
		if err != nil || json.Unmarshal(raw, &result) != nil {
			payload["error"] = "idle interaction lease reply unavailable"
			payload["reconciliationRequired"] = true
			return behavior.ActionSettledUnverified, payload
		}
		payload["result"], payload["phase"] = result, "idle_interaction_acquired"
		return behavior.ActionConfirmed, payload // Lease metadata, never a game effect.
	}}, nil
}
