package modrt

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"maps"
	"math"
	"strconv"
	"sync"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

const actionsModule = "buddy-actions"

type policySpellRegistry struct {
	mu      sync.Mutex
	active  map[string]*policySpell
	pending []*behavior.ActionReceipt
}

type policySpell struct {
	vm         *behavior.Session
	capability string
	spell      uint32
	target     uint64
	token      string
	origin     modsession.WriteOrigin
	admitted   bool
	attempted  bool
	written    bool
	transport  *behavior.ActionReceipt
	effect     *behavior.ActionReceipt
}

func (p *SessionPolicy) resolveKnownSpell(action map[string]any) (PolicyDispatch, error) {
	capability, _ := action["capability"].(string)
	sessionID, _ := action["sessionId"].(string)
	self, _ := action["selfGuid"].(string)
	target, _ := action["targetGuid"].(string)
	spell, _ := action["spellId"].(float64)
	targetGUID, err := strconv.ParseUint(target, 10, 64)
	if sessionID != p.vm.Status().SessionID || self != p.body || targetGUID == 0 || err != nil ||
		target != strconv.FormatUint(targetGUID, 10) || !(spell >= 1 && spell <= math.MaxUint32 && spell == math.Trunc(spell)) {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "current session, current body, exact target GUID and exact spell ID required")
	}

	method := capability
	switch capability {
	case "polymorph", "sap":
	case "heal":
		method = "cast_heal"
	case "offense":
		method = "cast_offense"
	case "cast_self":
		if target != self {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "self cast target must be the current body")
		}
	default:
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "unsupported policy spell capability")
	}
	inst, md, err := p.manager.lookup(actionsModule, method, true)
	if err != nil {
		return PolicyDispatch{}, err
	}
	req := dynamicpb.NewMessage(md.Descriptor().Input())
	values := map[protoreflect.Name]protoreflect.Value{
		"session_id": protoreflect.ValueOfString(sessionID),
		"spell_id":   protoreflect.ValueOfUint32(uint32(spell)),
	}
	if capability != "cast_self" {
		values["target_guid"] = protoreflect.ValueOfUint64(targetGUID)
	}
	if capability != "polymorph" && capability != "sap" {
		selfGUID, _ := strconv.ParseUint(self, 10, 64)
		values["self_guid"] = protoreflect.ValueOfUint64(selfGUID)
	}
	for name, value := range values {
		field := req.Descriptor().Fields().ByName(name)
		if field == nil || field.IsList() || field.IsMap() || !fieldMatchesValue(field.Kind(), value) {
			return PolicyDispatch{}, status.Error(codes.FailedPrecondition, "incompatible policy spell request descriptor")
		}
		req.Set(field, value)
	}
	if md.Descriptor().Output().FullName() != (&actionsv1.State{}).ProtoReflect().Descriptor().FullName() {
		return PolicyDispatch{}, status.Error(codes.FailedPrecondition, "incompatible policy spell result descriptor")
	}
	op := &policySpell{vm: p.vm, capability: capability, spell: uint32(spell), target: targetGUID}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return PolicyDispatch{}, err
	}
	op.token = hex.EncodeToString(nonce[:])
	return PolicyDispatch{
		Module: actionsModule,
		spell:  op,
		Send: func(ctx context.Context) (behavior.ActionOutcome, map[string]any) {
			return p.manager.invokePolicySpell(ctx, inst, md, req, op)
		},
	}, nil
}

func fieldMatchesValue(kind protoreflect.Kind, value protoreflect.Value) bool {
	switch value.Interface().(type) {
	case string:
		return kind == protoreflect.StringKind
	case uint32:
		return kind == protoreflect.Uint32Kind
	case uint64:
		return kind == protoreflect.Uint64Kind
	default:
		return false
	}
}

func (m *Manager) invokePolicySpell(ctx context.Context, inst *instance, md moddisc.Method, req proto.Message, op *policySpell) (behavior.ActionOutcome, map[string]any) {
	origin := modsession.WriteOriginFromContext(ctx)
	if origin.Writer != "lua" || origin.Module != actionsModule || origin.ActionID == "" {
		return behavior.ActionNotDispatched, op.payload("missing_action_origin", false, false, false)
	}
	bridge := modcb.PolicySpellService
	if op.capability == "offense" || op.capability == "sap" {
		bridge = modcb.PolicyHostileSpellService
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	ready, err := grpc_health_v1.NewHealthClient(inst.conn).Check(probeCtx, &grpc_health_v1.HealthCheckRequest{Service: bridge})
	cancel()
	if err != nil || ready.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		return behavior.ActionNotDispatched, op.payload("policy_spell_bridge_unavailable", false, false, false)
	}
	spells := &m.policySpells
	spells.mu.Lock()
	op.origin, op.admitted = origin, true
	if spells.active == nil {
		spells.active = make(map[string]*policySpell)
	}
	spells.active[op.token] = op
	spells.mu.Unlock()

	state := &actionsv1.State{}
	err = m.invoke(modcb.WithPolicySpell(ctx, op.token), publicCall, "lua", actionsModule, inst, md, req, state)
	spells.mu.Lock()
	defer spells.mu.Unlock()
	op.admitted = false
	switch {
	case op.written:
		return behavior.ActionTransportAccepted, op.payload("transport_written", true, false, false)
	case op.attempted:
		return behavior.ActionUnknown, op.payload("transport_unconfirmed_no_retry", false, false, false)
	}
	switch status.Code(err) {
	case codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound, codes.PermissionDenied:
		return behavior.ActionNotDispatched, op.payload("admission_rejected", false, false, false)
	}
	return behavior.ActionUnknown, op.payload("dispatch_unconfirmed_no_retry", false, false, false)
}

func (m *Manager) writePolicySpell(ctx context.Context, token, sessionID, module string, code uint32, payload []byte) error {
	spells := &m.policySpells
	spells.mu.Lock()
	op := spells.active[token]
	if op == nil || !op.admitted || op.attempted || op.origin.SessionID != sessionID ||
		op.origin.Module != module || code != opcode.CMSGCastSpell || !op.matchesPacket(payload) {
		spells.mu.Unlock()
		return status.Error(codes.PermissionDenied, "invalid policy spell callback")
	}
	op.attempted = true
	spells.mu.Unlock()

	err := m.session.SendPacket(modsession.WithWriteOrigin(ctx, op.origin), code, payload)
	spells.mu.Lock()
	op.written = err == nil
	spells.mu.Unlock()
	return err
}

func (op *policySpell) matchesPacket(body []byte) bool {
	if len(body) < 10 || binary.LittleEndian.Uint32(body[1:]) != op.spell || body[5] != 0 {
		return false
	}
	mask := binary.LittleEndian.Uint32(body[6:])
	if mask != 2 || len(body) < 11 {
		return false
	}
	body = body[10:]
	var guid uint64
	n := 1
	for i := range 8 {
		if body[0]&(1<<i) != 0 {
			if n == len(body) {
				return false
			}
			guid |= uint64(body[n]) << (8 * i)
			n++
		}
	}
	return guid == op.target && n == len(body)
}

func (op *policySpell) payload(phase string, written, castGo, observed bool) map[string]any {
	return map[string]any{
		"capability": op.capability, "spell_id": op.spell,
		"target_guid": strconv.FormatUint(op.target, 10), "phase": phase,
		"transport_written": written, "server_cast_go": castGo, "effect_observed": observed,
	}
}

func (p *SessionPolicy) recordSpellTransport(op *policySpell, receipt *behavior.ActionReceipt) error {
	spells := &p.manager.policySpells
	spells.mu.Lock()
	defer spells.mu.Unlock()
	owned := *receipt
	owned.Payload = maps.Clone(receipt.Payload)
	op.transport = &owned
	spells.pending = append(spells.pending, &owned)
	if op.effect != nil && !(receipt.Outcome == behavior.ActionUnknown && op.effect.Outcome == behavior.ActionUnknown) {
		spells.pending = append(spells.pending, op.effect)
	}
	if op.effect != nil || !op.attempted {
		delete(spells.active, op.token)
	}
	return spells.flush(p.vm)
}

func (m *Manager) recordSpellEffect(module string, trailer metadata.MD) {
	token, body, ok := modcb.PolicySpellEffect(trailer)
	if module != actionsModule || !ok {
		return
	}
	var native actionsv1.SpellOperation
	if err := proto.Unmarshal(body, &native); err != nil {
		m.logger.Warn("invalid native spell receipt", "error", err)
		return
	}
	spells := &m.policySpells
	spells.mu.Lock()
	defer spells.mu.Unlock()
	op := spells.active[token]
	if op == nil || !op.attempted || op.effect != nil || native.SpellId != op.spell ||
		native.TargetGuid != op.target || native.Capability != op.capability {
		return
	}
	outcome := spellEffectOutcome(&native)
	if outcome == "" {
		return
	}
	if op.transport != nil && op.transport.Outcome == behavior.ActionUnknown && outcome == behavior.ActionUnknown {
		delete(spells.active, token)
		return
	}
	id, _ := strconv.ParseUint(op.origin.ActionID, 10, 64)
	op.effect = &behavior.ActionReceipt{
		SessionID: op.origin.SessionID, ActionID: id, Outcome: outcome,
		Payload: op.payload(native.Phase, native.TransportWritten, native.ServerCastGo, native.EffectObserved),
	}
	if op.transport != nil {
		delete(spells.active, token)
		spells.pending = append(spells.pending, op.effect)
		// Enqueue does not call Lua. Leave a blocked receipt for the next
		// policy boundary rather than replaying the packet or the cast.
		if err := spells.flush(op.vm); err != nil && !errors.Is(err, behavior.ErrEventBackpressure) {
			m.logger.Warn("could not enqueue native spell receipt", "error", err)
		}
	}
}

func spellEffectOutcome(effect *actionsv1.SpellOperation) behavior.ActionOutcome {
	switch effect.Phase {
	case "confirmed_aura", "confirmed_effect", "confirmed_heal":
		if effect.ServerCastGo && effect.EffectObserved {
			return behavior.ActionConfirmed
		}
	case "server_rejected_no_replay", "server_cast_go_no_target_hit":
		return behavior.ActionFailed
	case "channel_ended_effect_unverified":
		if effect.ChannelStarted && effect.ChannelEnded {
			return behavior.ActionSettledUnverified
		}
	case "outcome_unconfirmed_no_retry", "server_cast_go_effect_unverified",
		"target_lost_outcome_unconfirmed", "map_changed_outcome_unconfirmed", "world_transfer_outcome_unconfirmed":
		// Actions only publishes these phases after retiring native cast custody.
		return behavior.ActionSettledUnverified
	case "transport_unconfirmed_no_retry":
		return behavior.ActionUnknown
	}
	return ""
}

// flush is called with mu held and retains buffered receipts on backpressure.
func (s *policySpellRegistry) flush(vm *behavior.Session) error {
	for len(s.pending) != 0 {
		receipt := s.pending[0]
		if _, err := vm.Enqueue(behavior.Event{Kind: "action_result", Receipt: receipt}); err != nil {
			return err
		}
		s.pending[0] = nil
		s.pending = s.pending[1:]
	}
	return nil
}

func (p *SessionPolicy) flushSpellReceipts(ctx context.Context) error {
	spells := &p.manager.policySpells
	spells.mu.Lock()
	err := spells.flush(p.vm)
	spells.mu.Unlock()
	if !errors.Is(err, behavior.ErrEventBackpressure) {
		return err
	}
	if err := p.vm.Drain(ctx); err != nil {
		return err
	}
	spells.mu.Lock()
	defer spells.mu.Unlock()
	return spells.flush(p.vm)
}
