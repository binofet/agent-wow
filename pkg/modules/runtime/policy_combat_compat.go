package modrt

import (
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"strconv"
)

func (m *Manager) queueLegacyEngage(ctx context.Context, inst *instance, req, result proto.Message) error {
	fields := req.ProtoReflect().Descriptor().Fields()
	enabled := req.ProtoReflect().Get(fields.ByName("enabled")).Bool()
	target := req.ProtoReflect().Get(fields.ByName("target_guid")).Uint()
	if enabled && target == 0 {
		return status.Error(codes.InvalidArgument, "explicit engage target required")
	}
	value := map[string]any{"task": "engage", "enabled": enabled, "source": "engage_npc"}
	if target != 0 {
		value["targetGuid"] = strconv.FormatUint(target, 10)
	}
	return m.queueActionPolicyInput(ctx, inst, result, "command", value, "lua_combat_request_queued:")
}
