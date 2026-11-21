package modrt

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
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
