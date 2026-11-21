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
	vm             *behavior.Session
	capability     string
	spell          uint32
	target         uint64
	castTarget     uint64 // Resurrection may target the observed corpse owned by target.
	encounterVisit uint64
	auraSlot       uint32
	auraSpell      uint32
	auraRevision   uint64
	melee          *policyMelee
	movement       *policyMovement
	recovery       *policyRecovery
	consumable     *policyConsumable
	corpse         *policyCorpse
	token          string
	origin         modsession.WriteOrigin
	admitted       bool
	attempted      bool
	written        bool
	transport      *behavior.ActionReceipt
	effect         *behavior.ActionReceipt
}

func (p *SessionPolicy) resolveKnownSpell(action map[string]any) (PolicyDispatch, error) {
	capability, _ := action["capability"].(string)
	if capability == "loot_resume_source" {
		return p.resolveLootCapacityResume(action)
	}
	if capability == "release_spirit" || capability == "query_corpse" || capability == "approach_corpse" || capability == "stop_corpse" || capability == "reclaim_corpse" {
		return p.resolveDeathAction(action, capability)
	}
	if capability == "loot_open" || capability == "loot_close" || capability == "loot_money" || capability == "loot_slot" {
		return p.resolveCorpseLoot(action, capability)
	}
	if capability == "consume" {
		return p.resolveConsumable(action)
	}
	if capability == "movement_owner" {
		return p.resolveMovementOwner(action)
	}
	if capability == "move_body" {
		return p.resolveBodyMovement(action)
	}
	if capability == "approach_target" || capability == "stop_movement" {
		return p.resolveMovement(action, capability)
	}
	if capability == "melee_start" || capability == "melee_stop" {
		return p.resolveMelee(action, capability)
	}
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
	case "buff":
		method = "cast_buff"
	case "heal":
		method = "cast_heal"
	case "offense":
		method = "cast_offense"
	case "resurrect":
		method = "resurrect_once"
	case "interrupt":
		method = "interrupt_cast"
	case "cleanse":
		method = "cast_cleanse"
	case "cast_self":
		if target != self {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "self cast target must be the current body")
		}
	default:
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "unsupported policy spell capability")
	}
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
	op := &policySpell{vm: p.vm, capability: capability, spell: uint32(spell), target: targetGUID}
	if capability == "resurrect" {
		op.castTarget = targetGUID
		if raw, supplied := action["castTargetGuid"]; supplied {
			text, ok := raw.(string)
			value, err := strconv.ParseUint(text, 10, 64)
			if !ok || err != nil || value == 0 || text != strconv.FormatUint(value, 10) || value != targetGUID && value>>48 != 0xf101 {
				return PolicyDispatch{}, status.Error(codes.InvalidArgument, "observed patient or owned corpse cast target required")
			}
			op.castTarget = value
		}
	}
	if capability == "interrupt" {
		text, _ := action["castRevision"].(string)
		value, err := strconv.ParseUint(text, 10, 64)
		if err != nil || value == 0 || text != strconv.FormatUint(value, 10) {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "exact enemy cast revision required")
		}
		values["cast_revision"] = protoreflect.ValueOfUint64(value)
	}
	if capability == "cleanse" {
		slot, slotOK := action["auraSlot"].(float64)
		aura, auraOK := action["auraSpellId"].(float64)
		text, _ := action["auraRevision"].(string)
		revision, err := strconv.ParseUint(text, 10, 64)
		if !slotOK || math.IsNaN(slot) || slot < 0 || slot >= 255 || slot != math.Trunc(slot) ||
			!auraOK || math.IsNaN(aura) || aura < 1 || aura > math.MaxUint32 || aura != math.Trunc(aura) ||
			err != nil || revision == 0 || text != strconv.FormatUint(revision, 10) {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "exact observed aura slot, spell and revision required")
		}
		op.auraSlot, op.auraSpell, op.auraRevision = uint32(slot), uint32(aura), revision
		values["aura_slot"] = protoreflect.ValueOfUint32(op.auraSlot)
		values["aura_spell_id"] = protoreflect.ValueOfUint32(op.auraSpell)
		values["aura_revision"] = protoreflect.ValueOfUint64(revision)
	}
	return p.preparePolicyDispatch(method, values, op)
}

func (p *SessionPolicy) preparePolicyDispatch(method string, values map[protoreflect.Name]protoreflect.Value, op *policySpell) (PolicyDispatch, error) {
	inst, md, err := p.manager.lookup(actionsModule, method, true)
	if err != nil {
		return PolicyDispatch{}, err
	}
	req := dynamicpb.NewMessage(md.Descriptor().Input())
	for name, value := range values {
		field := req.Descriptor().Fields().ByName(name)
		if field == nil || field.IsList() || field.IsMap() || !fieldMatchesValue(field.Kind(), value) {
			return PolicyDispatch{}, status.Error(codes.FailedPrecondition, "incompatible policy action request descriptor")
		}
		req.Set(field, value)
	}
	if md.Descriptor().Output().FullName() != (&actionsv1.State{}).ProtoReflect().Descriptor().FullName() {
		return PolicyDispatch{}, status.Error(codes.FailedPrecondition, "incompatible policy action result descriptor")
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return PolicyDispatch{}, err
	}
	op.token = hex.EncodeToString(nonce[:])
	return PolicyDispatch{
		Module: actionsModule,
		spell:  op,
		Send: func(ctx context.Context) (behavior.ActionOutcome, map[string]any) {
			if op.recovery != nil {
				return p.manager.invokePolicyRecovery(ctx, inst, md, req, op)
			}
			if op.movement != nil {
				return p.manager.invokePolicyMovement(ctx, inst, md, req, op)
			}
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
	case bool:
		return kind == protoreflect.BoolKind
	case float32:
		return kind == protoreflect.FloatKind
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
	if op.capability == "resurrect" || op.capability == "interrupt" || op.capability == "cleanse" || op.capability == "buff" {
		bridge = modcb.PolicySupportSpellService
	}
	switch {
	case op.melee != nil:
		bridge = modcb.PolicyMeleeService
	case op.capability == "offense" || op.capability == "sap":
		bridge = modcb.PolicyHostileSpellService
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	ready, err := grpc_health_v1.NewHealthClient(inst.conn).Check(probeCtx, &grpc_health_v1.HealthCheckRequest{Service: bridge})
	cancel()
	if err != nil || ready.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		return behavior.ActionNotDispatched, policyDispatchError(op.payload("policy_spell_bridge_unavailable", false, false, false), err)
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
	call := modcb.WithPolicySpell(ctx, op.token)
	if op.encounterVisit != 0 {
		call = modcb.WithPolicyEncounterAccess(call, inst.launch.Environment[modcb.PolicyEncounterKeyEnv], op.encounterVisit)
	}
	err = m.invoke(call, publicCall, "lua", actionsModule, inst, md, req, state)
	spells.mu.Lock()
	defer spells.mu.Unlock()
	op.admitted = false
	if op.recovery != nil && state.Recovery != nil {
		op.recovery.phase = state.Recovery.Phase
	}
	if op.movement != nil && op.movement.body && state.BodyMovement != nil &&
		state.BodyMovement.SequenceId == op.movement.sequence && state.BodyMovement.VisitId == op.encounterVisit {
		op.movement.phase = state.BodyMovement.Phase
		op.movement.detail = state.BodyMovement.Detail
	}
	switch {
	case op.written:
		return behavior.ActionTransportAccepted, policyDispatchError(op.payload("transport_written", true, false, false), err)
	case op.attempted:
		return behavior.ActionUnknown, policyDispatchError(op.payload("transport_unconfirmed_no_retry", false, false, false), err)
	}
	if err == nil && op.movement != nil && op.movement.body && op.movement.phase == "local_physics_refused" {
		// The binding measured and refused the input before any packet write.
		// This is a known non-dispatch, not an ambiguous transport outcome.
		return behavior.ActionNotDispatched, op.payload(op.movement.phase, false, false, false)
	}
	switch status.Code(err) {
	case codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound, codes.PermissionDenied:
		return behavior.ActionNotDispatched, policyDispatchError(op.payload("admission_rejected", false, false, false), err)
	}
	return behavior.ActionUnknown, policyDispatchError(op.payload("dispatch_unconfirmed_no_retry", false, false, false), err)
}

// Preserve the actual RPC status separately from the compatible policy phase.
// A transport return or an admission error is never a server game effect.
func policyDispatchError(payload map[string]any, err error) map[string]any {
	if err != nil {
		rpc := status.Convert(err)
		payload["grpc_status"], payload["grpc_message"] = rpc.Code().String(), rpc.Message()
	}
	return payload
}

func (m *Manager) writePolicySpell(ctx context.Context, token, sessionID, module string, code uint32, payload []byte) error {
	if handled, err := m.writePolicyReadQuery(ctx, token, sessionID, module, code, payload); handled {
		return err
	}
	spells := &m.policySpells
	spells.mu.Lock()
	op := spells.active[token]
	if op == nil || !op.admitted || op.attempted && op.movement == nil || op.origin.SessionID != sessionID ||
		op.origin.Module != module || !op.matchesActionPacket(code, payload) {
		spells.mu.Unlock()
		return modcb.PacketNotDispatchedError("invalid policy spell callback")
	}
	op.attempted = true
	spells.mu.Unlock()

	err := m.session.SendPacket(modsession.WithWriteOrigin(ctx, op.origin), code, payload)
	spells.mu.Lock()
	op.written = op.written || err == nil
	spells.mu.Unlock()
	return err
}

func (op *policySpell) matchesActionPacket(code uint32, payload []byte) bool {
	if op.recovery != nil {
		return op.matchesRecoveryPacket(code, payload)
	}
	if op.corpse != nil {
		if op.corpse.money {
			return code == opcode.CMSGLootMoney && len(payload) == 0
		}
		if op.corpse.slot != nil {
			return code == opcode.CMSGAutostoreLootItem && len(payload) == 1 && payload[0] == *op.corpse.slot
		}
		want := uint32(opcode.CMSGLoot)
		if op.corpse.close {
			want = opcode.CMSGLootRelease
		}
		return code == want && len(payload) == 8 && binary.LittleEndian.Uint64(payload) == op.target
	}
	if op.consumable != nil {
		return op.matchesConsumablePacket(code, payload)
	}
	if op.movement != nil {
		return op.matchesMovementPacket(code, payload)
	}
	if op.melee != nil {
		if op.melee.stop {
			return code == opcode.CMSGAttackstop && len(payload) == 0
		}
		return code == opcode.CMSGAttackswing && len(payload) == 8 && binary.LittleEndian.Uint64(payload) == op.target
	}
	return code == opcode.CMSGCastSpell && op.matchesPacket(payload)
}

func (op *policySpell) matchesPacket(body []byte) bool {
	if len(body) < 10 || binary.LittleEndian.Uint32(body[1:]) != op.spell || body[5] != 0 {
		return false
	}
	mask := binary.LittleEndian.Uint32(body[6:])
	target := op.target
	if op.castTarget != 0 {
		target = op.castTarget
	}
	wantMask := uint32(2)
	if op.capability == "resurrect" && target>>48 == 0xf101 {
		wantMask = 0x8000
	}
	if mask != wantMask || len(body) < 11 {
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
	return guid == target && n == len(body)
}

func (op *policySpell) payload(phase string, written, castGo, observed bool) map[string]any {
	if op.recovery != nil {
		return op.recoveryPayload(phase, written)
	}
	if op.movement != nil {
		return op.movementPayload(phase, written)
	}
	if op.melee != nil {
		return op.meleePayload(phase, written, observed)
	}
	out := map[string]any{
		"capability": op.capability, "spell_id": op.spell,
		"target_guid": strconv.FormatUint(op.target, 10), "phase": phase,
		"transport_written": written, "server_cast_go": castGo, "effect_observed": observed,
	}
	if op.capability == "cleanse" {
		out["aura_slot"], out["aura_spell_id"] = op.auraSlot, op.auraSpell
		out["aura_revision"] = strconv.FormatUint(op.auraRevision, 10)
	}
	return out
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
	if module != actionsModule {
		return
	}
	if token, body, ok := modcb.PolicySpellEffect(trailer); ok {
		m.recordPolicyEffect(token, body, false)
	}
	if token, body, ok := modcb.PolicyMeleeEffect(trailer); ok {
		m.recordPolicyEffect(token, body, true)
	}
}

func (m *Manager) recordPolicyEffect(token string, body []byte, melee bool) {
	spells := &m.policySpells
	spells.mu.Lock()
	defer spells.mu.Unlock()
	op := spells.active[token]
	if op == nil || !op.attempted || op.effect != nil || (op.melee != nil) != melee {
		return
	}
	outcome, payload, err := op.decodeEffect(body)
	if err != nil {
		m.logger.Warn("invalid native policy action receipt", "error", err)
		return
	}
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
		Payload: payload,
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

func (op *policySpell) decodeEffect(body []byte) (behavior.ActionOutcome, map[string]any, error) {
	if op.melee != nil {
		return op.decodeMeleeEffect(body)
	}
	var native actionsv1.SpellOperation
	if err := proto.Unmarshal(body, &native); err != nil {
		return "", nil, err
	}
	target := op.target
	if op.castTarget != 0 {
		target = op.castTarget
	}
	if native.SpellId != op.spell || native.TargetGuid != target || native.Capability != op.capability {
		return "", nil, nil
	}
	return spellEffectOutcome(&native), op.payload(native.Phase, native.TransportWritten, native.ServerCastGo, native.EffectObserved), nil
}

func spellEffectOutcome(effect *actionsv1.SpellOperation) behavior.ActionOutcome {
	switch effect.Phase {
	case "confirmed_aura", "confirmed_effect", "confirmed_heal", "confirmed_alive":
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
		"target_lost_outcome_unconfirmed", "map_changed_outcome_unconfirmed", "world_transfer_outcome_unconfirmed", "observed_alive_without_cast_confirmation":
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
