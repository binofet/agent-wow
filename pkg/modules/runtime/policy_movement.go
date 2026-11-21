package modrt

import (
	"context"
	"encoding/binary"
	"math"
	"math/bits"
	"strconv"

	"github.com/agent-wow/agent-wow/pkg/behavior"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type policyMovement struct {
	self     uint64
	stop     bool
	body     bool
	jump     bool
	sequence uint64
	phase    string
	detail   string
}

func (p *SessionPolicy) resolveBodyMovement(action map[string]any) (PolicyDispatch, error) {
	sessionID, _ := action["sessionId"].(string)
	self, _ := action["selfGuid"].(string)
	visit, _ := action["visitId"].(string)
	sequence, _ := action["sequenceId"].(string)
	selfID, _ := strconv.ParseUint(self, 10, 64)
	visitID, visitErr := strconv.ParseUint(visit, 10, 64)
	sequenceID, sequenceErr := strconv.ParseUint(sequence, 10, 64)
	bearing, _ := action["bearingRadians"].(float64)
	duration, _ := action["durationMs"].(float64)
	jump, _ := action["jump"].(bool)
	if sessionID != p.vm.Status().SessionID || self != p.body || selfID == 0 || visitErr != nil || visitID == 0 ||
		sequenceErr != nil || sequenceID == 0 || visit != strconv.FormatUint(visitID, 10) || sequence != strconv.FormatUint(sequenceID, 10) ||
		math.IsNaN(bearing) || math.IsInf(bearing, 0) || math.Abs(bearing) > math.Pi+.001 ||
		math.IsNaN(duration) || math.IsInf(duration, 0) || duration != math.Trunc(duration) || duration < 0 || duration > 500 || !jump && duration < 1 {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "current body, visit, sequence and bounded relative input required")
	}
	values := map[protoreflect.Name]protoreflect.Value{
		"session_id": protoreflect.ValueOfString(sessionID), "self_guid": protoreflect.ValueOfUint64(selfID),
		"visit_id": protoreflect.ValueOfUint64(visitID), "sequence_id": protoreflect.ValueOfUint64(sequenceID),
		"bearing_radians": protoreflect.ValueOfFloat32(float32(bearing)), "duration_ms": protoreflect.ValueOfUint32(uint32(duration)), "jump": protoreflect.ValueOfBool(jump),
	}
	op := &policySpell{vm: p.vm, capability: "move_body", encounterVisit: visitID,
		movement: &policyMovement{self: selfID, body: true, jump: jump, sequence: sequenceID}}
	dispatch, err := p.preparePolicyDispatch("move_body", values, op)
	if err == nil {
		dispatch.spell = nil
	}
	return dispatch, err
}

func (p *SessionPolicy) resolveMovement(action map[string]any, capability string) (PolicyDispatch, error) {
	sessionID, _ := action["sessionId"].(string)
	self, _ := action["selfGuid"].(string)
	target, _ := action["targetGuid"].(string)
	visit, _ := action["visitId"].(string)
	selfGUID, _ := strconv.ParseUint(self, 10, 64)
	targetGUID, targetErr := strconv.ParseUint(target, 10, 64)
	visitID, visitErr := strconv.ParseUint(visit, 10, 64)
	radius, _ := action["rangeYards"].(float64)
	stop := capability == "stop_movement"
	if sessionID != p.vm.Status().SessionID || self != p.body || selfGUID == 0 || targetErr != nil || targetGUID == 0 || target != strconv.FormatUint(targetGUID, 10) ||
		visitErr != nil || visitID == 0 || visit != strconv.FormatUint(visitID, 10) || math.IsNaN(radius) || math.IsInf(radius, 0) || radius > math.MaxFloat32 || !stop && radius <= 0 {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "current session, body, exact target, visit and finite positive range required")
	}
	los, _ := action["requireLos"].(bool)
	behind, _ := action["behind"].(bool)
	values := map[protoreflect.Name]protoreflect.Value{
		"session_id": protoreflect.ValueOfString(sessionID), "self_guid": protoreflect.ValueOfUint64(selfGUID),
		"target_guid": protoreflect.ValueOfUint64(targetGUID), "visit_id": protoreflect.ValueOfUint64(visitID),
		"range_yards": protoreflect.ValueOfFloat32(float32(radius)), "stop": protoreflect.ValueOfBool(stop),
		"require_los": protoreflect.ValueOfBool(los), "behind": protoreflect.ValueOfBool(behind),
	}
	op := &policySpell{vm: p.vm, capability: capability, target: targetGUID, encounterVisit: visitID, movement: &policyMovement{self: selfGUID, stop: stop}}
	dispatch, err := p.preparePolicyDispatch("approach_target", values, op)
	if err == nil {
		// Motion is a physical step, not a cast awaiting a server spell effect.
		// A fresh world pose determines the next step; it never holds the spell
		// action ledger or claims that a navigation estimate was a confirmed hit.
		dispatch.spell = nil
	}
	return dispatch, err
}

func (m *Manager) invokePolicyMovement(ctx context.Context, inst *instance, md moddisc.Method, req proto.Message, op *policySpell) (behavior.ActionOutcome, map[string]any) {
	outcome, payload := m.invokePolicySpell(ctx, inst, md, req, op)
	m.policySpells.mu.Lock()
	delete(m.policySpells.active, op.token)
	m.policySpells.mu.Unlock()
	if outcome == behavior.ActionTransportAccepted || outcome == behavior.ActionUnknown {
		outcome = behavior.ActionSettledUnverified
	}
	return outcome, payload
}

func (op *policySpell) movementPayload(phase string, written bool) map[string]any {
	if op.movement.body && op.movement.phase != "" {
		phase = op.movement.phase
	}
	return map[string]any{"capability": op.capability, "phase": phase, "self_guid": strconv.FormatUint(op.movement.self, 10),
		"target_guid": strconv.FormatUint(op.target, 10), "visit_id": strconv.FormatUint(op.encounterVisit, 10),
		"sequence_id":       strconv.FormatUint(op.movement.sequence, 10),
		"transport_written": written, "effect_observed": false, "detail": op.movement.detail}
}

func (op *policySpell) matchesMovementPacket(code uint32, body []byte) bool {
	if op.capability == "release_interaction" {
		return op.matchesInteractionStop(code, body)
	}
	switch code {
	case opcode.MSGMoveStartForward, opcode.MSGMoveHeartbeat, opcode.MSGMoveStop, opcode.MSGMoveSetFacing:
	case opcode.MSGMoveJump:
		if !op.movement.body || !op.movement.jump {
			return false
		}
	case opcode.MSGMoveFallLand:
		if !op.movement.body {
			return false
		}
	default:
		return false
	}
	if op.movement.stop && code != opcode.MSGMoveStop {
		return false
	}
	if len(body) < 1 {
		return false
	}
	n := 1 + bits.OnesCount8(body[0])
	// MovementInfo: flags4, extraFlags2, time4, position/orientation16,
	// fallTime4. Ground packets have no optional transport/spline/swim tail.
	if len(body) < n+30 {
		return false
	}
	var guid uint64
	index := 1
	for shift := uint(0); shift < 8; shift++ {
		if body[0]&(1<<shift) != 0 {
			guid |= uint64(body[index]) << (shift * 8)
			index++
		}
	}
	flags := binary.LittleEndian.Uint32(body[n:])
	if binary.LittleEndian.Uint16(body[n+4:]) != 0 {
		return false
	}
	// Allow only the requested forward motion and observed passive ground
	// modifiers, plus normal jumping or gravity settling for an explicit body
	// input. A route-only movement cannot introduce airborne packets.
	allowedFlags := uint32(1) | movementflags.PassiveModifiers
	falling := flags&0x1000 != 0
	if op.movement.body {
		allowedFlags |= 0x1000
	}
	if guid != op.movement.self || flags & ^allowedFlags != 0 ||
		!falling && len(body) != n+30 || falling && len(body) != n+46 ||
		code == opcode.MSGMoveJump && !falling || code == opcode.MSGMoveFallLand && falling {
		return false
	}
	if falling {
		values := [4]float64{}
		for i := range values {
			values[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(body[n+30+i*4:])))
			if math.IsNaN(values[i]) || math.IsInf(values[i], 0) {
				return false
			}
		}
		jump := op.movement.jump && math.Abs(values[0]-7.955547) <= .0001 && values[3] >= .1 && values[3] <= 14
		settling := values[0] == 0 && values[3] == 0 && flags&1 == 0
		if (!jump && !settling) || code == opcode.MSGMoveJump && !jump ||
			math.Abs(values[1]*values[1]+values[2]*values[2]-1) > .001 || binary.LittleEndian.Uint32(body[n+26:]) > 1100 {
			return false
		}
	}
	for offset := n + 10; offset < n+26; offset += 4 {
		value := float64(math.Float32frombits(binary.LittleEndian.Uint32(body[offset:])))
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}
