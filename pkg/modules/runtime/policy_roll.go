package modrt

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"strconv"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const inventoryModule = "buddy-inventory"

type policyRoll struct {
	self, visit, revision uint64
	slot                  uint32
	choice                string
	wireChoice            byte
	requestID             string
}

func canonicalRollIdentity(action map[string]any, key string) (uint64, bool) {
	text, ok := action[key].(string)
	value, err := strconv.ParseUint(text, 10, 64)
	return value, ok && err == nil && value != 0 && text == strconv.FormatUint(value, 10)
}

func (p *SessionPolicy) resolveRoll(action map[string]any) (PolicyDispatch, error) {
	sessionID, _ := action["sessionId"].(string)
	self, selfOK := canonicalRollIdentity(action, "selfGuid")
	visit, visitOK := canonicalRollIdentity(action, "visitId")
	target, targetOK := canonicalRollIdentity(action, "rollGuid")
	revision, revisionOK := canonicalRollIdentity(action, "offerRevision")
	slot, slotOK := action["lootSlot"].(float64)
	choice, _ := action["choice"].(string)
	choices := map[string]byte{"pass": 0, "need": 1, "greed": 2, "disenchant": 3}
	wireChoice, choiceOK := choices[choice]
	if sessionID != p.vm.Status().SessionID || strconv.FormatUint(self, 10) != p.body ||
		!selfOK || !visitOK || !targetOK || !revisionOK || !choiceOK ||
		!slotOK || !(slot >= 0 && slot <= 255 && slot == math.Trunc(slot)) {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "current body, visit and exact offered roll choice required")
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return PolicyDispatch{}, err
	}
	roll := &policyRoll{self: self, visit: visit, revision: revision, slot: uint32(slot),
		choice: choice, wireChoice: wireChoice, requestID: hex.EncodeToString(nonce[:])}
	op := &policySpell{vm: p.vm, capability: "loot_roll", target: target, roll: roll}
	values := map[protoreflect.Name]protoreflect.Value{
		"session_id":              protoreflect.ValueOfString(sessionID),
		"request_id":              protoreflect.ValueOfString(roll.requestID),
		"roll_guid":               protoreflect.ValueOfUint64(target),
		"expected_offer_revision": protoreflect.ValueOfUint64(revision),
		"visit_id":                protoreflect.ValueOfUint64(visit),
		"expected_loot_slot":      protoreflect.ValueOfUint32(uint32(slot)),
		"choice":                  protoreflect.ValueOfString(choice),
	}
	return p.preparePolicyDispatch("roll", values, op)
}

func (op *policySpell) matchesRollPacket(code uint32, body []byte) bool {
	return code == opcode.CMSGLootRoll && len(body) == 13 &&
		binary.LittleEndian.Uint64(body) == op.target &&
		binary.LittleEndian.Uint32(body[8:]) == op.roll.slot && body[12] == op.roll.wireChoice
}

func (op *policySpell) rollPayload(phase string, written, observed bool) map[string]any {
	return map[string]any{
		"capability": "loot_roll", "phase": phase, "choice": op.roll.choice,
		"roll_guid":      strconv.FormatUint(op.target, 10),
		"offer_revision": strconv.FormatUint(op.roll.revision, 10),
		"visit_id":       strconv.FormatUint(op.roll.visit, 10),
		"loot_slot":      op.roll.slot, "request_id": op.roll.requestID,
		"transport_written": written, "server_vote_observed": observed,
	}
}

func (op *policySpell) decodeRollEffect(body []byte) (behavior.ActionOutcome, map[string]any, error) {
	var native inventoryv1.RollReceipt
	if err := proto.Unmarshal(body, &native); err != nil {
		return "", nil, err
	}
	roll := op.roll
	if native.GetRequestId() != roll.requestID || native.GetRollGuid() != op.target ||
		native.GetOfferRevision() != roll.revision || native.GetVisitId() != roll.visit ||
		native.GetChoice() != roll.choice || native.GetLootSlot() != roll.slot {
		return "", nil, nil
	}
	outcome := behavior.ActionOutcome("")
	switch native.GetPhase() {
	case "server_vote_observed":
		if native.GetServerVoteObserved() {
			outcome = behavior.ActionConfirmed
		}
	case "outcome_unconfirmed_no_retry":
		outcome = behavior.ActionSettledUnverified
	case "not_dispatched":
		outcome = behavior.ActionNotDispatched
	}
	if outcome == "" {
		return "", nil, nil
	}
	return outcome, op.rollPayload(native.GetPhase(), native.GetTransportWritten(), native.GetServerVoteObserved()), nil
}

// A read can observe a native deadline retirement without an incoming world
// packet. Correlate that retained receipt before queuing the newer feed value.
func (m *Manager) reconcileRollReceipts(sessionID string, raw []byte) error {
	var state inventoryv1.RollState
	if err := protojson.Unmarshal(raw, &state); err != nil {
		return err
	}
	if state.GetSessionId() != sessionID {
		return errors.New("roll receipt feed changed session")
	}
	tokens := make(map[string]string)
	m.policySpells.mu.Lock()
	for token, op := range m.policySpells.active {
		if op.roll != nil && op.origin.SessionID == sessionID {
			tokens[op.roll.requestID] = token
		}
	}
	m.policySpells.mu.Unlock()
	for _, receipt := range state.GetReceipts() {
		if token := tokens[receipt.GetRequestId()]; token != "" {
			body, err := proto.Marshal(receipt)
			if err != nil {
				return err
			}
			m.recordPolicyEffect(inventoryModule, token, body, false)
		}
	}
	return nil
}
