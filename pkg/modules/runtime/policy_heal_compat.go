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

// The legacy empty HealOnce request is input to the body's current script,
// not a second patient/rank selector. Returning a State acknowledges the queued
// request only; normal observations and exact CastHeal produce cast receipts.
func (m *Manager) queueLegacyHeal(ctx context.Context, inst *instance, result proto.Message) error {
	catalog, _, err := m.observedSession()
	if err != nil {
		return err
	}
	m.mu.Lock()
	host := m.policy
	m.mu.Unlock()
	if host == nil || host.body != catalog.CharacterGUID {
		return status.Error(codes.FailedPrecondition, "HealOnce requires the current attached Lua policy; exact callers use CastHeal")
	}
	if _, err := host.observe(ctx, catalog.SessionID, catalog.CharacterGUID); err != nil {
		return err
	}
	method, ok := inst.definition.RPC("status")
	if !ok {
		return status.Error(codes.FailedPrecondition, "current action status unavailable")
	}
	view := dynamicpb.NewMessage(method.Descriptor().Output())
	if err := m.invoke(ctx, internalCall, "session-policy", "buddy-actions", inst, method, &emptypb.Empty{}, view); err != nil {
		return err
	}
	fields := view.Descriptor().Fields()
	sid, self, reason := fields.ByName("session_id"), fields.ByName("self_guid"), fields.ByName("reason")
	if sid == nil || self == nil || reason == nil || view.Get(sid).String() != catalog.SessionID ||
		strconv.FormatUint(view.Get(self).Uint(), 10) != catalog.CharacterGUID {
		return status.Error(codes.FailedPrecondition, "action status does not match the current policy body")
	}
	current, _, err := m.observedSession()
	if err != nil {
		return err
	}
	m.mu.Lock()
	same := m.policy == host
	m.mu.Unlock()
	if !same || current.SessionID != catalog.SessionID || current.CharacterGUID != catalog.CharacterGUID {
		return status.Error(codes.FailedPrecondition, "session changed before healing request")
	}
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	sequence, err := host.Enqueue(behavior.Event{Kind: "healing_request", Value: map[string]any{"source": "heal_once"}})
	if err != nil {
		return err
	}
	view.Set(reason, protoreflect.ValueOfString("lua_healing_request_queued:"+strconv.FormatUint(sequence, 10)))
	body, err := proto.Marshal(view)
	if err != nil {
		return err
	}
	return proto.Unmarshal(body, result)
}
