package modrt

import (
	"context"
	"strconv"

	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/emptypb"
)

func legacyConsumableRequest(name, path string, req proto.Message) (string, bool) {
	if name != consumableInventoryModule {
		return "", false
	}
	kind := ""
	switch path {
	case "/warcraftbuddy.inventory.v1.BuddyInventory/Drink":
		kind = "drink"
	case "/warcraftbuddy.inventory.v1.BuddyInventory/EatBuffFood":
		kind = "eat_buff_food"
	default:
		return "", false
	}
	m := req.ProtoReflect()
	fields := m.Descriptor().Fields()
	if source := fields.ByName("source"); source != nil && m.Has(source) {
		return "", false
	}
	for _, name := range []protoreflect.Name{"spell_id", "expected_inventory_revision", "request_id"} {
		if f := fields.ByName(name); f != nil && m.Has(f) {
			return "", false
		}
	}
	return kind, true
}

// A legacy request acknowledges ordered input only. The current policy owns
// item choice, thresholds, reserves, movement ownership and eventual completion.
func (m *Manager) queueLegacyConsumable(ctx context.Context, inst *instance, req, result proto.Message, kind string) error {
	catalog, _, err := m.observedSession()
	if err != nil {
		return err
	}
	input := req.ProtoReflect()
	fields := input.Descriptor().Fields()
	sid, self := fields.ByName("session_id"), fields.ByName("self_guid")
	if sid == nil || self == nil || input.Get(sid).String() != catalog.SessionID || strconv.FormatUint(input.Get(self).Uint(), 10) != catalog.CharacterGUID {
		return status.Error(codes.FailedPrecondition, "current consumable request body required")
	}
	m.mu.Lock()
	host := m.policy
	m.mu.Unlock()
	if host == nil || host.body != catalog.CharacterGUID {
		return status.Error(codes.FailedPrecondition, "consumable selection requires the current attached Lua policy; exact callers supply source")
	}
	if _, err = host.observe(ctx, catalog.SessionID, catalog.CharacterGUID); err != nil {
		return err
	}
	method, ok := inst.definition.RPC("state")
	if !ok {
		return status.Error(codes.FailedPrecondition, "current Inventory state unavailable")
	}
	view := dynamicpb.NewMessage(method.Descriptor().Output())
	if err = m.invoke(ctx, internalCall, "session-policy", consumableInventoryModule, inst, method, &emptypb.Empty{}, view); err != nil {
		return err
	}
	seen := view.Descriptor().Fields()
	stateSID, stateSelf := seen.ByName("session_id"), seen.ByName("self_guid")
	if stateSID == nil || stateSelf == nil || view.Get(stateSID).String() != catalog.SessionID || strconv.FormatUint(view.Get(stateSelf).Uint(), 10) != catalog.CharacterGUID {
		return status.Error(codes.FailedPrecondition, "Inventory state does not match the current body")
	}
	current, _, err := m.observedSession()
	if err != nil {
		return err
	}
	m.mu.Lock()
	same := m.policy == host
	m.mu.Unlock()
	if !same || current.SessionID != catalog.SessionID || current.CharacterGUID != catalog.CharacterGUID {
		return status.Error(codes.FailedPrecondition, "session changed before consumable request")
	}
	if err = ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	sequence, err := host.Enqueue(behavior.Event{Kind: "consumable_request", Value: map[string]any{"kind": kind, "source": "legacy_rpc"}})
	if err != nil {
		return err
	}
	out := result.ProtoReflect()
	outFields := out.Descriptor().Fields()
	if f := outFields.ByName("session_id"); f != nil {
		out.Set(f, protoreflect.ValueOfString(catalog.SessionID))
	}
	if f := outFields.ByName("self_guid"); f != nil {
		out.Set(f, protoreflect.ValueOfUint64(input.Get(self).Uint()))
	}
	if f := outFields.ByName("reason"); f != nil {
		out.Set(f, protoreflect.ValueOfString("lua_consumable_request_queued:"+strconv.FormatUint(sequence, 10)))
	}
	return nil
}
