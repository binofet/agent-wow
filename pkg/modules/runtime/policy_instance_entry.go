package modrt

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"math/bits"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"
)

type policyInstanceEntry struct {
	self                                                 uint64
	trigger                                              uint32 // Zero denotes a packet-free read.
	stopAttempted, stopWritten, triggerAttempted         bool
	unknown                                              bool
	stopScope                                            *modv1.TransportScope
	triggerWritten, triggerUnknown, triggerNotDispatched bool
}

func instanceEntryUint64(value any, zero bool) (uint64, bool) {
	text, ok := value.(string)
	n, err := strconv.ParseUint(text, 10, 64)
	return n, ok && err == nil && (zero || n != 0) && text == strconv.FormatUint(n, 10)
}

func instanceEntryNumber(value any, limit float64) (float64, bool) {
	n, ok := value.(float64)
	return n, ok && !math.IsNaN(n) && !math.IsInf(n, 0) && math.Abs(n) <= limit
}

func (p *SessionPolicy) instanceEntryIdentity(sid, self string, guid uint64) bool {
	vm := p.vm.Status()
	if vm.Closed || vm.SessionID != sid || self != p.body {
		return false
	}
	m := p.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ctx.Err() == nil && !m.closing && !m.preparing && !m.prepared && m.err == nil && filepath.Base(m.dir) == sid && m.session.Identity.CharacterGUID == guid
}

// Exact installed contracts prevent a familiar alias or message name from
// silently routing to a different field layout, including nested volumes.
func instanceEntryDescriptorMatches(got, want protoreflect.MessageDescriptor, seen map[protoreflect.FullName]bool) bool {
	if got.FullName() != want.FullName() || !proto.Equal(protodesc.ToDescriptorProto(got), protodesc.ToDescriptorProto(want)) {
		return false
	}
	if seen[want.FullName()] {
		return true
	}
	seen[want.FullName()] = true
	for i := 0; i < want.Fields().Len(); i++ {
		field := want.Fields().Get(i)
		if field.Kind() == protoreflect.MessageKind && !instanceEntryDescriptorMatches(got.Fields().ByNumber(field.Number()).Message(), field.Message(), seen) {
			return false
		}
	}
	return true
}

func (p *SessionPolicy) resolveInstanceEntry(action map[string]any, capability string) (PolicyDispatch, error) {
	sid, _ := action["sessionId"].(string)
	self, _ := action["selfGuid"].(string)
	guid, selfOK := instanceEntryUint64(self, false)
	visit, visitOK := instanceEntryUint64(action["visitId"], false)
	if sid == "" || len(sid) > 128 || !selfOK || !visitOK || !p.instanceEntryIdentity(sid, self, guid) {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "current canonical session body and observed visit required")
	}
	allowed := map[string]bool{"capability": true, "sessionId": true, "selfGuid": true, "visitId": true}
	params := map[string]any{}
	var request, response proto.Message
	var path, method string
	op := &policySpell{vm: p.vm, capability: capability, encounterVisit: visit, instanceEntry: &policyInstanceEntry{self: guid}}
	add := func(names ...string) {
		for _, name := range names {
			allowed[name] = true
			if value, exists := action[name]; exists {
				params[name] = value
			}
		}
	}
	switch capability {
	case "area_trigger_reference":
		method, path = capability, actionsv1.BuddyActions_AreaTriggerReference_FullMethodName
		request, response = &actionsv1.AreaTriggerReferenceRequest{}, &actionsv1.AreaTriggerReferences{}
		add("sessionId", "selfGuid", "visitId", "destinationMapId")
	case "area_trigger_observation":
		method, path = capability, actionsv1.BuddyActions_AreaTriggerObservation_FullMethodName
		request, response = &emptypb.Empty{}, &actionsv1.AreaTriggerOperation{}
	case "instance_entry_status":
		method, path = "status", actionsv1.BuddyActions_Status_FullMethodName
		request, response = &emptypb.Empty{}, &actionsv1.State{}
	case "notify_area_trigger":
		method, path = capability, actionsv1.BuddyActions_NotifyAreaTrigger_FullMethodName
		request, response = &actionsv1.NotifyAreaTriggerRequest{}, &actionsv1.AreaTriggerOperation{}
		add("sessionId", "selfGuid", "visitId", "token", "triggerId", "expectedPositionRevision", "expectedRevision", "clientRequestId")
	case "plan_route_leg":
		method, path = capability, actionsv1.BuddyActions_PlanRouteLeg_FullMethodName
		request, response = &actionsv1.PlanRouteLegRequest{}, &actionsv1.RouteLeg{}
		add("sessionId", "selfGuid", "expectedPositionRevision", "mapId", "x", "y", "z")
	case "travel_to":
		method, path = capability, actionsv1.BuddyActions_TravelTo_FullMethodName
		request, response = &actionsv1.TravelToRequest{}, &actionsv1.State{}
		add("token", "mapId", "x", "y", "z", "arrivalDistance")
		op.instanceEntry, op.movement = nil, &policyMovement{self: guid, idleBound: true}
	default:
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "unsupported instance-entry capability")
	}
	for key := range action {
		if !allowed[key] {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "unexpected instance-entry action field")
		}
	}
	for _, key := range []string{"triggerId", "destinationMapId", "mapId"} {
		if !allowed[key] {
			continue
		}
		n, ok := instanceEntryNumber(action[key], math.MaxUint32)
		if !ok || n < 0 || n != math.Trunc(n) || key == "triggerId" && n == 0 {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "explicit exact uint32 map or trigger required")
		}
	}
	for _, key := range []string{"expectedPositionRevision", "expectedRevision"} {
		if allowed[key] {
			n, ok := instanceEntryUint64(action[key], key == "expectedRevision")
			if !ok || key == "expectedRevision" && n == math.MaxUint64 {
				return PolicyDispatch{}, status.Error(codes.InvalidArgument, "exact canonical instance-entry revision required")
			}
		}
	}
	if allowed["x"] {
		for _, key := range []string{"x", "y", "z"} {
			if _, ok := instanceEntryNumber(action[key], math.MaxFloat32); !ok {
				return PolicyDispatch{}, status.Error(codes.InvalidArgument, "explicit finite route coordinate required")
			}
		}
	}
	for _, key := range []string{"token", "clientRequestId"} {
		if allowed[key] {
			text, ok := action[key].(string)
			if !ok || text == "" || len(text) > 128 || strings.TrimSpace(text) != text {
				return PolicyDispatch{}, status.Error(codes.InvalidArgument, "exact bounded custody token and client request identity required")
			}
		}
	}
	if value, supplied := action["arrivalDistance"]; supplied {
		n, ok := instanceEntryNumber(value, math.MaxFloat32)
		if !ok || n != 0 && (n < .5 || n > 3) {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "arrival distance must preserve the native0/default or0.5..3yard range")
		}
	}
	raw, err := json.Marshal(params)
	if err != nil || protojson.Unmarshal(raw, request) != nil {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "instance-entry request cannot be encoded")
	}
	if notify, ok := request.(*actionsv1.NotifyAreaTriggerRequest); ok {
		op.instanceEntry.trigger = notify.TriggerId
	}
	return p.prepareObservedAction(capability, sid, self, guid, visit, method, path, request, response, op)
}

// Prepare a typed, visit-bound Lua action. A returned mutation never proves its game effect.
func (p *SessionPolicy) prepareObservedAction(capability, sid, self string, guid, visit uint64, method, path string, request, response proto.Message, op *policySpell) (PolicyDispatch, error) {
	inst, md, err := p.manager.lookup(actionsModule, method, true)
	if err != nil {
		return PolicyDispatch{}, err
	}
	if md.Path() != path || !instanceEntryDescriptorMatches(md.Descriptor().Input(), request.ProtoReflect().Descriptor(), map[protoreflect.FullName]bool{}) ||
		!instanceEntryDescriptorMatches(md.Descriptor().Output(), response.ProtoReflect().Descriptor(), map[protoreflect.FullName]bool{}) {
		return PolicyDispatch{}, status.Error(codes.FailedPrecondition, "installed action contract is incompatible")
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return PolicyDispatch{}, err
	}
	op.token = hex.EncodeToString(nonce[:])
	return PolicyDispatch{Module: actionsModule, Send: func(ctx context.Context) (behavior.ActionOutcome, map[string]any) {
		payload := map[string]any{"capability": capability, "phase": "not_dispatched", "transport_written": false, "effect_observed": false}
		current, observed := ctx.Value(policyEncounterVisitKey{}).(policyEncounterObservation)
		origin := modsession.WriteOriginFromContext(ctx)
		if !observed || current.visit != visit || !p.instanceEntryIdentity(sid, self, guid) || origin.SessionID != sid || origin.Writer != "lua" || origin.Module != actionsModule || origin.ActionID == "" {
			payload["error"] = "action lost its current observed body/visit"
			return behavior.ActionNotDispatched, payload
		}
		probe, cancel := context.WithTimeout(ctx, 2*time.Second)
		ready, err := grpc_health_v1.NewHealthClient(inst.conn).Check(probe, &grpc_health_v1.HealthCheckRequest{Service: modcb.PolicySpellService})
		cancel()
		if err != nil || ready.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
			payload["error"] = "action callback bridge unavailable"
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
		result := response.ProtoReflect().Type().New().Interface()
		call := modcb.WithPolicyEncounterAccess(modcb.WithPolicySpell(ctx, op.token), inst.launch.Environment[modcb.PolicyEncounterKeyEnv], visit)
		callErr := p.manager.invoke(call, publicCall, "lua", actionsModule, inst, md, request, result)
		registry.mu.Lock()
		attempted, written := op.attempted, op.written
		unknown := op.instanceEntry != nil && op.instanceEntry.unknown || op.movement != nil && op.movement.unknown || op.summoning != nil && op.summoning.unknown
		registry.mu.Unlock()
		payload["transport_written"] = written
		mutation := capability == "notify_area_trigger" || capability == "travel_to" || op.summoning != nil
		if callErr != nil {
			payload["error"], payload["phase"] = callErr.Error(), "rpc_failed"
			if !mutation || !attempted && instanceEntryKnownRefusal(callErr) {
				return behavior.ActionNotDispatched, payload
			}
			payload["reconciliationRequired"] = true
			return behavior.ActionSettledUnverified, payload
		}
		bound := p.instanceEntryIdentity(sid, self, guid)
		switch value := result.(type) {
		case *actionsv1.SelectTargetResponse:
			bound = bound && value.SessionId == sid && value.SelfGuid == guid && value.TargetGuid == request.(*actionsv1.SelectTargetRequest).TargetGuid
			unknown = unknown || !value.TransportWritten
		case *actionsv1.SummoningObservation:
			bound = bound && value.SessionId == sid && value.SelfGuid == guid && value.VisitId == visit
			switch r := request.(type) {
			case *actionsv1.UseSummoningObjectRequest:
				bound = bound && value.UseRevision == r.ExpectedUseRevision+1 && proto.Equal(value.GetLastUse().GetRequest(), r)
				unknown = unknown || value.GetLastUse().GetTransportUnknown()
			case *actionsv1.RespondToSummonRequest:
				offer := value.GetOffer()
				bound = bound && offer.GetSequence() == r.OfferSequence && offer.GetSummonerGuid() == r.SummonerGuid &&
					offer.GetVisitId() == r.VisitId && offer.RequestedAccept != nil && r.GetAccept() == offer.GetRequestedAccept()
				unknown = unknown || offer.GetTransportUnknown()
			}
		case *actionsv1.AreaTriggerReferences:
			bound = bound && value.SessionId == sid && value.SelfGuid == guid && value.VisitId == visit
		case *actionsv1.AreaTriggerOperation:
			bound = bound && value.SessionId == sid && value.SelfGuid == guid && value.VisitId != 0
			payload["phase"] = value.Phase
			if notify, ok := request.(*actionsv1.NotifyAreaTriggerRequest); ok {
				bound = bound && value.VisitId == visit && value.TriggerId == notify.TriggerId && value.ClientRequestId == notify.ClientRequestId &&
					value.ExpectedPositionRevision == notify.ExpectedPositionRevision && value.Revision == notify.ExpectedRevision+1
			}
			unknown = unknown || value.TransportUnknown
		case *actionsv1.State:
			bound = bound && value.SessionId == sid && value.SelfGuid == guid
			if capability == "travel_to" {
				// A returned unary call can still owe a STOP or retain physical
				// uncertainty. It does not complete the travel task.
				unknown = unknown || value.MotionUnconfirmed || value.Moving || value.Traveling
			}
		case *actionsv1.RouteLeg:
			bound = bound && value.SessionId == sid && value.SelfGuid == guid && value.MapId == request.(*actionsv1.PlanRouteLegRequest).MapId
		}
		encoded, encodeErr := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(result)
		var value map[string]any
		if !bound || encodeErr != nil || json.Unmarshal(encoded, &value) != nil {
			payload["error"], payload["phase"] = "action reply lost its exact body or request binding", "reply_unverified"
			if mutation {
				payload["reconciliationRequired"] = true
				return behavior.ActionSettledUnverified, payload
			}
			return behavior.ActionNotDispatched, payload
		}
		payload["result"] = value
		if capability != "notify_area_trigger" && capability != "area_trigger_observation" {
			payload["phase"] = capability + "_returned"
		}
		if unknown {
			payload["reconciliationRequired"] = true
		}
		if mutation {
			return behavior.ActionSettledUnverified, payload
		}
		return behavior.ActionConfirmed, payload // Typed metadata, never a physical effect.
	}}, nil
}

func instanceEntryKnownRefusal(err error) bool {
	switch status.Code(err) {
	case codes.InvalidArgument, codes.FailedPrecondition, codes.PermissionDenied, codes.NotFound, codes.Unimplemented, codes.Aborted, codes.AlreadyExists, codes.ResourceExhausted:
		return true
	}
	return false
}

func (op *policySpell) matchesInstanceEntryPacket(code uint32, body []byte) bool {
	entry := op.instanceEntry
	if entry == nil || entry.trigger == 0 {
		return false
	}
	if code == opcode.CMSGAreatrigger {
		return len(body) == 4 && binary.LittleEndian.Uint32(body) == entry.trigger
	}
	if code != opcode.MSGMoveStop || len(body) == 0 {
		return false
	}
	n := 1 + bits.OnesCount8(body[0])
	if len(body) < n+30 {
		return false
	}
	var guid uint64
	i := 1
	for bit := uint(0); bit < 8; bit++ {
		if body[0]&(1<<bit) != 0 {
			guid |= uint64(body[i]) << (bit * 8)
			i++
		}
	}
	info, size, err := movementinfo.DecodePrefix(body[n:])
	return guid == entry.self && err == nil && n+size == len(body) && info.AllFinite() && info.Flags&0xff == 0 && movementflags.GroundCompatible(info.Flags, uint32(info.ExtraFlags))
}

func (m *Manager) writePolicyInstanceEntryScoped(ctx context.Context, token, sessionID, module string, code uint32, payload []byte, scope *modv1.TransportScope) error {
	registry := &m.policySpells
	registry.mu.Lock()
	op := registry.active[token]
	if op == nil || !op.admitted || op.instanceEntry == nil ||
		!(op.capability == "notify_area_trigger" || op.capability == "enter_corpse_instance" && op.recovery != nil && op.recovery.epoch != 0 && op.recovery.self == op.instanceEntry.self && op.recovery.trigger == op.instanceEntry.trigger) ||
		op.origin.Writer != "lua" || op.origin.ActionID == "" || op.origin.SessionID != sessionID || op.origin.Module != module || module != actionsModule ||
		scope == nil || scope.SelfGuid != op.instanceEntry.self || !scope.VisitBound || scope.VisitRevision == 0 || scope.MotionRevision == 0 || scope.ConnectionId == "" ||
		!op.matchesInstanceEntryPacket(code, payload) || ctx.Err() != nil || m.session.SendScopedPacket == nil {
		registry.mu.Unlock()
		return modcb.PacketNotDispatchedError("scoped callback is outside the exact instance-entry dispatch")
	}
	entry := op.instanceEntry
	if code == opcode.MSGMoveStop {
		if entry.stopAttempted || entry.triggerAttempted {
			registry.mu.Unlock()
			return modcb.PacketNotDispatchedError("instance-entry STOP already attempted")
		}
		entry.stopAttempted = true
		entry.stopScope = proto.Clone(scope).(*modv1.TransportScope)
	} else {
		if entry.triggerAttempted || !entry.stopWritten || entry.unknown || entry.stopScope == nil ||
			scope.ConnectionId != entry.stopScope.ConnectionId || scope.SelfGuid != entry.stopScope.SelfGuid ||
			scope.VisitRevision != entry.stopScope.VisitRevision || scope.MapId != entry.stopScope.MapId || scope.MotionRevision < entry.stopScope.MotionRevision {
			registry.mu.Unlock()
			return modcb.PacketNotDispatchedError("instance-entry trigger already attempted or lacks its known STOP")
		}
		entry.triggerAttempted = true
	}
	op.attempted = true
	origin := op.origin
	registry.mu.Unlock()
	err := m.session.SendScopedPacket(modsession.WithWriteOrigin(ctx, origin), code, append([]byte(nil), payload...), proto.Clone(scope).(*modv1.TransportScope))
	registry.mu.Lock()
	if code == opcode.MSGMoveStop {
		entry.stopWritten = err == nil
	} else {
		entry.triggerWritten = err == nil
		entry.triggerNotDispatched = modcb.PacketWasNotDispatched(err)
		entry.triggerUnknown = err != nil && !entry.triggerNotDispatched
	}
	entry.unknown = entry.unknown || err != nil && !modcb.PacketWasNotDispatched(err)
	op.written = op.written || err == nil
	registry.mu.Unlock()
	return err
}
