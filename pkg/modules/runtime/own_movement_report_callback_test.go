package modrt

import (
	"context"
	"encoding/json"
	"testing"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

func movementReportRequest(t *testing.T, fields map[string]any) *modv1.InvokeModuleRequest {
	t.Helper()
	value, err := structpb.NewStruct(fields)
	if err != nil {
		t.Fatal(err)
	}
	packed, err := anypb.New(value)
	if err != nil {
		t.Fatal(err)
	}
	return &modv1.InvokeModuleRequest{Module: "session", Method: modcb.OwnMovementReportMethod, Request: packed}
}

func TestOwnMovementReportReadIsPrivateBoundDetachedAndCurrent(t *testing.T) {
	m := catalogManager(t, 4, "aw-4")
	scope := &modv1.TransportScope{ConnectionId: "actual-transport", SelfGuid: 4, VisitBound: true, VisitRevision: 9, MotionRevision: 12}
	packet := &modv1.WorldPacket{Opcode: 0xb7, Payload: []byte{1, 2, 3}, TransportScope: scope}
	reads := 0
	m.session.OwnMovementReport = func() *modv1.WorldPacket { reads++; return proto.Clone(packet).(*modv1.WorldPacket) }
	m.session.TransportScope = func() *modv1.TransportScope { return proto.Clone(scope).(*modv1.TransportScope) }
	request := movementReportRequest(t, map[string]any{"sessionId": "aw-4", "selfGuid": "4"})
	result, err := m.invokeModule(context.Background(), actionsModule, request)
	if err != nil {
		t.Fatal(err)
	}
	out := &modv1.WorldPacket{}
	if result.Result.UnmarshalTo(out) != nil || !proto.Equal(packet, out) {
		t.Fatal("exact image/scope changed", result)
	}
	out.Payload[0], out.TransportScope.MotionRevision = 99, 99
	if packet.Payload[0] != 1 || scope.MotionRevision != 12 {
		t.Fatal("caller mutated native report")
	}
	if _, err := m.invokeModule(context.Background(), "buddy-world", request); status.Code(err) != codes.PermissionDenied || reads != 1 {
		t.Fatal("foreign module reached own report", err, reads)
	}
	if _, err := m.InvokeJSON(context.Background(), "session.own_movement_report", json.RawMessage(`{"sessionId":"aw-4","selfGuid":"4"}`)); err == nil || reads != 1 {
		t.Fatal("private read leaked a public host route", err, reads)
	}
	m.session.TransportScope = func() *modv1.TransportScope {
		changed := proto.Clone(scope).(*modv1.TransportScope)
		changed.MotionRevision++
		return changed
	}
	if _, err := m.invokeModule(context.Background(), actionsModule, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("old report was relabeled with newer scope", err)
	}
}

func TestOwnMovementReportRejectsForeignMalformedUnavailableAndClosingIdentity(t *testing.T) {
	m := catalogManager(t, 4, "aw-4")
	reads := 0
	m.session.OwnMovementReport = func() *modv1.WorldPacket { reads++; return nil }
	m.session.TransportScope = func() *modv1.TransportScope { return nil }
	for _, fields := range []map[string]any{{}, {"sessionId": "other", "selfGuid": "4"},
		{"sessionId": "aw-4", "selfGuid": "5"}, {"sessionId": "aw-4", "selfGuid": "04"},
		{"sessionId": "aw-4", "selfGuid": "4", "extra": true}} {
		if _, err := m.invokeModule(context.Background(), actionsModule, movementReportRequest(t, fields)); err == nil {
			t.Fatal("accepted malformed/foreign identity", fields)
		}
	}
	if reads != 0 {
		t.Fatal("foreign identity read transport")
	}
	request := movementReportRequest(t, map[string]any{"sessionId": "aw-4", "selfGuid": "4"})
	if _, err := m.invokeModule(context.Background(), actionsModule, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("invented absent report", err)
	}
	m.closing = true
	if _, err := m.invokeModule(context.Background(), actionsModule, request); status.Code(err) != codes.FailedPrecondition || reads != 1 {
		t.Fatal("closing session read transport", err, reads)
	}
}
