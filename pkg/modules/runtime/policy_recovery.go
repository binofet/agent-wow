package modrt

import (
	"context"
	"strconv"

	"github.com/agent-wow/agent-wow/pkg/behavior"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type policyRecovery struct {
	self, epoch uint64
	query       bool
}

func (p *SessionPolicy) resolveDeathAction(action map[string]any, capability string) (PolicyDispatch, error) {
	session, _ := action["sessionId"].(string)
	self, _ := action["selfGuid"].(string)
	epoch, _ := action["deathEpoch"].(string)
	selfID, selfErr := strconv.ParseUint(self, 10, 64)
	epochID, epochErr := strconv.ParseUint(epoch, 10, 64)
	if session != p.vm.Status().SessionID || self != p.body || selfErr != nil || selfID == 0 || epochErr != nil || epochID == 0 || epoch != strconv.FormatUint(epochID, 10) {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "current session, body and observed death epoch required")
	}
	op := &policySpell{vm: p.vm, capability: capability, recovery: &policyRecovery{self: selfID, epoch: epochID, query: capability == "query_corpse"}}
	dispatch, err := p.preparePolicyDispatch(capability, map[protoreflect.Name]protoreflect.Value{
		"session_id": protoreflect.ValueOfString(session), "self_guid": protoreflect.ValueOfUint64(selfID), "death_epoch": protoreflect.ValueOfUint64(epochID),
	}, op)
	if err == nil {
		dispatch.spell = nil
	}
	return dispatch, err
}

func (m *Manager) invokePolicyRecovery(ctx context.Context, inst *instance, md moddisc.Method, req proto.Message, op *policySpell) (behavior.ActionOutcome, map[string]any) {
	outcome, payload := m.invokePolicySpell(ctx, inst, md, req, op)
	m.policySpells.mu.Lock()
	delete(m.policySpells.active, op.token)
	m.policySpells.mu.Unlock()
	// A release/query does not hold the global spell ledger. Native death-bound
	// reservations and Lua checkpoint custody prevent replay while the body waits
	// for actual ghost/corpse observations, including an unrelated resurrection.
	if outcome == behavior.ActionTransportAccepted || outcome == behavior.ActionUnknown {
		outcome = behavior.ActionSettledUnverified
	}
	return outcome, payload
}
func (op *policySpell) matchesRecoveryPacket(code uint32, payload []byte) bool {
	if op.recovery.query {
		return code == opcode.MSGCorpseQuery && len(payload) == 0
	}
	return code == opcode.CMSGRepopRequest && len(payload) == 1 && payload[0] == 0
}
func (op *policySpell) recoveryPayload(phase string, written bool) map[string]any {
	return map[string]any{"capability": op.capability, "phase": phase, "self_guid": strconv.FormatUint(op.recovery.self, 10),
		"death_epoch": strconv.FormatUint(op.recovery.epoch, 10), "transport_written": written, "effect_observed": false}
}
