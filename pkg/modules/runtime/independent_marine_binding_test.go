package modrt

import (
	"context"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc/metadata"
	"strings"
	"testing"
)

func TestIndependentScopedMovementAttemptRemainsBoundToNativeMovementCaller(t *testing.T) {
	m := catalogManager(t, 4, "aw-4")
	id := strings.Repeat("a", 64)
	scope := &modv1.TransportScope{ConnectionId: "core", SelfGuid: 4, VisitBound: true, VisitRevision: 3, MotionRevision: 8}
	var origin modsession.WriteOrigin
	m.session.SendScopedPacket = func(ctx context.Context, _ uint32, _ []byte, _ *modv1.TransportScope) error {
		origin = modsession.WriteOriginFromContext(ctx)
		return nil
	}
	md, _ := metadata.FromOutgoingContext(modcb.WithMovementAttempt(context.Background(), id))
	ctx := metadata.NewIncomingContext(context.Background(), md)
	for _, module := range []string{actionsModule, "buddy-world"} {
		if err := m.moduleScopedPacketWriter("aw-4", module)(ctx, 181, []byte{1}, scope); err != nil {
			t.Fatal(err)
		}
		if origin.Module != module || origin.Writer != "module" || origin.ActionID != "" {
			t.Fatal(origin)
		}
		if (origin.MovementAttempt == id) != (module == actionsModule) {
			t.Errorf("module %s has own-Actions movement diagnostic=%v", module, origin.MovementAttempt == id)
		}
	}
}
