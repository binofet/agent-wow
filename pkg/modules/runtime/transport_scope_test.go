package modrt

import (
	"context"
	"testing"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modtest "github.com/agent-wow/agent-wow/pkg/modules/internal/testutil"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/protobuf/proto"
)

func TestTransportScopeWriterPreservesTrustedOriginAndNeverFallsBack(t *testing.T) {
	m := New(nil, nil, nil)
	t.Cleanup(m.cancel)
	legacy, scoped := 0, 0
	m.session.SendPacket = func(context.Context, uint32, []byte) error { legacy++; return nil }
	writer := m.moduleScopedPacketWriter("session", "buddy-world")
	scope := &modv1.TransportScope{ConnectionId: "transport", SelfGuid: 1, VisitBound: true, VisitRevision: 3,
		MotionRevision: 8, SplineIdKnown: true, SplineRevision: 4}
	body := []byte{1, 2}
	if err := writer(context.Background(), 0x2c9, body, scope); !modcb.PacketWasNotDispatched(err) || legacy != 0 {
		t.Fatal("missing scoped host fell back to opaque send", err, legacy)
	}
	m.session.SendScopedPacket = func(ctx context.Context, op uint32, b []byte, got *modv1.TransportScope) error {
		scoped++
		if modsession.WriteOriginFromContext(ctx) != (modsession.WriteOrigin{SessionID: "session", Module: "buddy-world", Writer: "module"}) ||
			op != 0x2c9 || !proto.Equal(got, scope) {
			t.Fatal("scope/provenance changed across runtime callback", got)
		}
		b[0], got.ConnectionId = 9, "callee-mutation"
		return nil
	}
	if err := writer(context.Background(), 0x2c9, body, scope); err != nil || scoped != 1 || legacy != 0 {
		t.Fatal("scoped packet did not reach its exact host", err, scoped, legacy)
	}
	if body[0] != 1 || scope.ConnectionId != "transport" {
		t.Fatal("runtime callback shared mutable scope/payload ownership")
	}
}

func TestTransportScopePublicationKeepsCapturedMetadataDetachedInFIFO(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b"} {
		modtest.WriteModule(t, root, name, "")
	}
	registry, err := moddisc.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	m := New(registry, nil, nil)
	t.Cleanup(m.cancel)
	scope := &modv1.TransportScope{ConnectionId: "source", SelfGuid: 1, MotionRevision: 10,
		SplineIdKnown: true, SplineId: 0, SplineRevision: 2,
		WorldTransfer: &modv1.ObservedWorldTransfer{Revision: 3, Phase: "pending", TargetMapKnown: true, TargetMapId: 530}}
	if err := m.PublishObserved(0x236, []byte{1}, scope); err != nil {
		t.Fatal(err)
	}
	scope.MotionRevision = 11
	if err := m.PublishObserved(0x236, []byte{2}, scope); err != nil {
		t.Fatal(err)
	}
	scope.ConnectionId, scope.MotionRevision = "caller-mutation", 99
	scope.WorldTransfer.TargetMapId = 571
	a, b := m.nextPacket(m.instances["a"]), m.nextPacket(m.instances["b"])
	if a.TransportScope.ConnectionId != "source" || a.TransportScope.MotionRevision != 10 || !a.TransportScope.SplineIdKnown ||
		a.TransportScope.SplineId != 0 || a.TransportScope.WorldTransfer.TargetMapId != 530 || !proto.Equal(a, b) {
		t.Fatal("publication refreshed or lost captured source metadata", a, b)
	}
	a.TransportScope.ConnectionId, a.Payload[0] = "subscriber-mutation", 9
	a.TransportScope.WorldTransfer.Phase = "subscriber-mutation"
	if b.TransportScope.ConnectionId != "source" || b.Payload[0] != 1 || b.TransportScope.WorldTransfer.Phase != "pending" {
		t.Fatal("subscribers share mutable packet/scope state", a, b)
	}
	for _, name := range []string{"a", "b"} {
		next := m.nextPacket(m.instances[name])
		if next.Payload[0] != 2 || next.TransportScope.MotionRevision != 11 {
			t.Fatal("scope was sampled at consumption instead of publication", next)
		}
	}
	if err := m.Publish(0x236, []byte{3}); err != nil {
		t.Fatal(err)
	}
	if packet := m.nextPacket(m.instances["a"]); packet.TransportScope != nil {
		t.Fatal("legacy publication fabricated transport authority", packet)
	}
}
