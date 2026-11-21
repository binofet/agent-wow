package modrt

import (
	"context"
	"google.golang.org/protobuf/proto"
)

// Preserve pinned old binaries' installed interface. New exact clients delegate
// both old toggle values to the current Lua source; neither creates an off mode.
func legacyRecoveryIntent(name, path string, inst *instance) bool {
	if name != "buddy-actions" || path != "/warcraftbuddy.actions.v1.BuddyActions/Recovery" {
		return false
	}
	_, ok := inst.definition.RPC("approach_corpse")
	return ok
}
func (m *Manager) queueLegacyRecoveryIntent(ctx context.Context, inst *instance, result proto.Message) error {
	return m.queuePolicyActionInput(ctx, inst, result, "death_recovery_request", map[string]any{"source": "recovery"}, "lua_death_recovery_request_queued:")
}
