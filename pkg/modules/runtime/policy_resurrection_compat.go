package modrt

import (
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"strconv"
)

func legacyResurrectionRequest(name, path string, request proto.Message) bool {
	if name != "buddy-actions" || path != "/warcraftbuddy.actions.v1.BuddyActions/ResurrectOnce" {
		return false
	}
	m := request.ProtoReflect()
	field := m.Descriptor().Fields().ByName("spell_id")
	return field != nil && m.Get(field).Uint() == 0
}
func (m *Manager) queueLegacyResurrection(ctx context.Context, inst *instance, request, result proto.Message) error {
	view := request.ProtoReflect()
	fields := view.Descriptor().Fields()
	sid, self, patient := fields.ByName("session_id"), fields.ByName("self_guid"), fields.ByName("target_guid")
	if sid == nil || self == nil || patient == nil {
		return status.Error(codes.InvalidArgument, "session, caster and patient required")
	}
	body, target := view.Get(self).Uint(), view.Get(patient).Uint()
	if view.Get(sid).String() == "" || body == 0 || body>>48 != 0 || target == 0 || target>>48 != 0 || target == body {
		return status.Error(codes.InvalidArgument, "current session, caster and distinct player GUID required")
	}
	return m.queuePolicyActionInput(ctx, inst, result, "resurrection_request", map[string]any{
		"source": "resurrect_once", "sessionId": view.Get(sid).String(), "selfGuid": strconv.FormatUint(body, 10), "targetGuid": strconv.FormatUint(target, 10)}, "lua_resurrection_request_queued:")
}
