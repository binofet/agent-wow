package modrt

import (
	"context"

	"google.golang.org/protobuf/proto"
)

// New exact-buff modules delegate the old enable request to the current Lua
// source. Explicitly pinned pre-CastBuff modules keep their deployed RPC; no
// legacy buff selector is restored in the new source. Disabling only clears
// compatibility status and never turns off the shared Lua maintenance policy.
func legacyBuffEnsureRequest(name, path string, inst *instance, request proto.Message) bool {
	if name != "buddy-actions" || path != "/warcraftbuddy.actions.v1.BuddyActions/Buffs" {
		return false
	}
	if _, exact := inst.definition.RPC("cast_buff"); !exact {
		return false
	}
	view := request.ProtoReflect()
	enabled := view.Descriptor().Fields().ByName("enabled")
	return enabled != nil && view.Get(enabled).Bool()
}

func (m *Manager) queueLegacyBuffEnsure(ctx context.Context, inst *instance, result proto.Message) error {
	return m.queuePolicyActionInput(ctx, inst, result, "buff_request", map[string]any{"source": "buffs"}, "lua_buff_request_queued:")
}
