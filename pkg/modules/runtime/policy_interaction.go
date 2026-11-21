package modrt

import (
	"encoding/binary"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"math"
	"math/bits"
	"strconv"
)

// The shared Lua policy chooses when custody should yield. This adapter binds
// its exact observed lease to the current body and permits only a normal STOP.
func (p *SessionPolicy) resolveInteractionRelease(action map[string]any) (PolicyDispatch, error) {
	sid, _ := action["sessionId"].(string)
	self, _ := action["selfGuid"].(string)
	visit, _ := action["visitId"].(string)
	owner, _ := action["owner"].(string)
	token, _ := action["token"].(string)
	body, bodyErr := strconv.ParseUint(self, 10, 64)
	visitID, visitErr := strconv.ParseUint(visit, 10, 64)
	if sid != p.vm.Status().SessionID || self != p.body || bodyErr != nil || body == 0 || visitErr != nil || visitID == 0 || visit != strconv.FormatUint(visitID, 10) || owner == "" || token == "" {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "current body, visit and exact interaction owner/token required")
	}
	values := map[protoreflect.Name]protoreflect.Value{
		"owner": protoreflect.ValueOfString(owner), "token": protoreflect.ValueOfString(token),
		"session_id": protoreflect.ValueOfString(sid), "self_guid": protoreflect.ValueOfUint64(body), "visit_id": protoreflect.ValueOfUint64(visitID),
	}
	op := &policySpell{vm: p.vm, capability: "release_interaction", encounterVisit: visitID, movement: &policyMovement{self: body, stop: true}}
	dispatch, err := p.preparePolicyDispatch("release_interaction", values, op)
	// Movement uncertainty is fenced by the native physical writer; a metadata
	// release must not occupy the global ledger and suppress healing indefinitely.
	if err == nil {
		dispatch.spell = nil
	}
	return dispatch, err
}

// Release uses the existing native STOP in the body's current medium. Reuse
// the ordinary packet checks, including exact GUID and finite coordinates;
// swimming adds only its defined finite pitch field, never a movement goal.
func (op *policySpell) matchesInteractionStop(code uint32, body []byte) bool {
	if code != opcode.MSGMoveStop || len(body) < 1 {
		return false
	}
	n := 1 + bits.OnesCount8(body[0])
	if len(body) < n+30 {
		return false
	}
	flags := binary.LittleEndian.Uint32(body[n:])
	if flags&1 != 0 {
		return false
	}
	if flags&0x200000 != 0 {
		if len(body) != n+34 {
			return false
		}
		pitch := float64(math.Float32frombits(binary.LittleEndian.Uint32(body[n+26:])))
		if math.IsNaN(pitch) || math.IsInf(pitch, 0) {
			return false
		}
		clean := append([]byte(nil), body[:n+26]...)
		clean = append(clean, body[n+30:]...)
		binary.LittleEndian.PutUint32(clean[n:], flags&^uint32(0x200000))
		body = clean
	}
	ground := *op
	ground.capability = ""
	return ground.matchesMovementPacket(code, body)
}
