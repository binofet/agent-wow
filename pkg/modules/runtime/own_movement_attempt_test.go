package modrt

import (
	"context"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"strings"
	"testing"
)

func TestOwnMovementAttemptReadRequiresExactPrivateCurrentEvidence(t *testing.T) {
	m := catalogManager(t, 4, "aw-4")
	id := strings.Repeat("a", 64)
	scope := &modv1.TransportScope{ConnectionId: "core", SelfGuid: 4, VisitBound: true, VisitRevision: 9, MotionRevision: 12}
	packet := &modv1.WorldPacket{Opcode: 0xb7, Payload: []byte{1}, TransportScope: scope}
	m.session.TransportScope = func() *modv1.TransportScope { return proto.Clone(scope).(*modv1.TransportScope) }
	m.session.OwnMovementReport = func() *modv1.WorldPacket { t.Fatal("exact read used uncorrelated legacy getter"); return nil }
	m.session.OwnMovementAttempt = func(got string) *modv1.WorldPacket {
		if got != id {
			return nil
		}
		return proto.Clone(packet).(*modv1.WorldPacket)
	}
	fields := map[string]any{"sessionId": "aw-4", "selfGuid": "4", "attemptId": id}
	if _, err := m.invokeModule(context.Background(), actionsModule, movementReportRequest(t, fields)); err != nil {
		t.Fatal(err)
	}
	fields["attemptId"] = strings.Repeat("b", 64)
	if _, err := m.invokeModule(context.Background(), actionsModule, movementReportRequest(t, fields)); err == nil {
		t.Fatal("borrowed other result")
	}
	fields["attemptId"] = id
	if _, err := m.invokeModule(context.Background(), "buddy-world", movementReportRequest(t, fields)); err == nil {
		t.Fatal("foreign caller")
	}
	m.session.OwnMovementAttempt = nil
	if _, err := m.invokeModule(context.Background(), actionsModule, movementReportRequest(t, fields)); err == nil {
		t.Fatal("old host fabricated result")
	}
}

func TestOwnMovementAttemptCallbackDoesNotGrantPolicyAttribution(t *testing.T) {
	m := catalogManager(t, 4, "aw-4")
	id := strings.Repeat("a", 64)
	var got modsession.WriteOrigin
	m.session.SendPacket = func(ctx context.Context, _ uint32, _ []byte) error {
		got = modsession.WriteOriginFromContext(ctx)
		return nil
	}
	out := modcb.WithMovementAttempt(context.Background(), id)
	md, _ := metadata.FromOutgoingContext(out)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	if err := m.modulePacketWriter("aw-4", actionsModule)(ctx, 181, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if got.MovementAttempt != id || got.ActionID != "" || got.Generation != 0 || got.Writer != "module" {
		t.Fatal("diagnostic ID became authority", got)
	}
	if err := m.modulePacketWriter("aw-4", "buddy-world")(ctx, 181, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if got.MovementAttempt != "" {
		t.Fatal("foreign module borrowed diagnostic")
	}
}
