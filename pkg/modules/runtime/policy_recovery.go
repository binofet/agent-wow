package modrt

import (
	"context"
	"encoding/binary"
	"math"
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
	corpse      uint64
	caster      uint64
	offer       uint64
	phase       string
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
	method := capability
	values := map[protoreflect.Name]protoreflect.Value{
		"session_id": protoreflect.ValueOfString(session), "self_guid": protoreflect.ValueOfUint64(selfID), "death_epoch": protoreflect.ValueOfUint64(epochID),
	}
	if capability == "accept_resurrection" {
		caster, _ := action["casterGuid"].(string)
		revision, _ := action["expectedOfferRevision"].(string)
		casterID, ce := strconv.ParseUint(caster, 10, 64)
		offerID, re := strconv.ParseUint(revision, 10, 64)
		if ce != nil || re != nil || casterID == 0 || casterID == selfID || casterID>>48 != 0 || offerID == 0 ||
			caster != strconv.FormatUint(casterID, 10) || revision != strconv.FormatUint(offerID, 10) {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "exact caster and observed offer revision required")
		}
		op.recovery.caster, op.recovery.offer = casterID, offerID
		values["caster_guid"] = protoreflect.ValueOfUint64(casterID)
		values["expected_offer_revision"] = protoreflect.ValueOfUint64(offerID)
	}
	if capability == "approach_corpse" || capability == "stop_corpse" || capability == "reclaim_corpse" {
		revision, _ := action["expectedPositionRevision"].(string)
		pose, err := strconv.ParseUint(revision, 10, 64)
		if err != nil || pose == 0 || revision != strconv.FormatUint(pose, 10) {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "exact current pose revision required")
		}
		values["expected_position_revision"] = protoreflect.ValueOfUint64(pose)
		if capability == "reclaim_corpse" {
			guid, _ := action["corpseGuid"].(string)
			rev, _ := action["expectedCorpseRevision"].(string)
			corpse, e := strconv.ParseUint(guid, 10, 64)
			cr, re := strconv.ParseUint(rev, 10, 64)
			if e != nil || re != nil || corpse == 0 || cr == 0 || guid != strconv.FormatUint(corpse, 10) || rev != strconv.FormatUint(cr, 10) {
				return PolicyDispatch{}, status.Error(codes.InvalidArgument, "exact corpse GUID and revision required")
			}
			op.recovery.corpse = corpse
			values["corpse_guid"] = protoreflect.ValueOfUint64(corpse)
			values["expected_corpse_revision"] = protoreflect.ValueOfUint64(cr)
		} else {
			method = "approach_corpse"
			stop := capability == "stop_corpse"
			op.movement = &policyMovement{self: selfID, stop: stop}
			values["stop"] = protoreflect.ValueOfBool(stop)
			if !stop {
				query, _ := action["corpseQueryRevision"].(string)
				qr, e := strconv.ParseUint(query, 10, 64)
				radius, _ := action["rangeYards"].(float64)
				if e != nil || qr == 0 || query != strconv.FormatUint(qr, 10) || math.IsNaN(radius) || math.IsInf(radius, 0) || radius <= 0 || radius > math.MaxFloat32 {
					return PolicyDispatch{}, status.Error(codes.InvalidArgument, "exact corpse query and finite range required")
				}
				values["corpse_query_revision"] = protoreflect.ValueOfUint64(qr)
				values["range_yards"] = protoreflect.ValueOfFloat32(float32(radius))
			}
		}
	}
	dispatch, err := p.preparePolicyDispatch(method, values, op)
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
	if op.capability == "accept_resurrection" {
		return code == opcode.CMSGResurrectResponse && len(payload) == 9 && binary.LittleEndian.Uint64(payload) == op.recovery.caster && payload[8] == 1
	}
	if op.capability == "approach_corpse" || op.capability == "stop_corpse" {
		return op.matchesMovementPacket(code, payload)
	}
	if op.capability == "reclaim_corpse" {
		return code == opcode.CMSGReclaimCorpse && len(payload) == 8 && binary.LittleEndian.Uint64(payload) == op.recovery.corpse
	}
	if op.recovery.query {
		return code == opcode.MSGCorpseQuery && len(payload) == 0
	}
	return code == opcode.CMSGRepopRequest && len(payload) == 1 && payload[0] == 0
}
func (op *policySpell) recoveryPayload(phase string, written bool) map[string]any {
	out := map[string]any{"capability": op.capability, "phase": phase, "self_guid": strconv.FormatUint(op.recovery.self, 10),
		"death_epoch": strconv.FormatUint(op.recovery.epoch, 10), "transport_written": written, "effect_observed": false, "native_phase": op.recovery.phase}
	if op.capability == "accept_resurrection" {
		out["caster_guid"] = strconv.FormatUint(op.recovery.caster, 10)
		out["offer_revision"] = strconv.FormatUint(op.recovery.offer, 10)
	}
	return out
}
