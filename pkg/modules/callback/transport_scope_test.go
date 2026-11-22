package modcb

import (
	"context"
	"testing"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
)

func TestScopedPacketCallbackNeverFallsBackOrAliasesCaller(t *testing.T) {
	scope := &modv1.TransportScope{ConnectionId: "connection", SelfGuid: 1, VisitBound: true,
		VisitRevision: 2, MotionRevision: 9, SplineIdKnown: true, SplineId: 0, SplineRevision: 4}
	req := &modv1.SendPacketRequest{Opcode: 0x2c9, Payload: []byte{1, 2, 3}, TransportScope: scope}
	legacy, scoped := 0, 0
	server := New(context.Background(), "buddy-world", Handlers{
		SendPacket: func(context.Context, uint32, []byte) error { legacy++; return nil },
		SendScopedPacket: func(_ context.Context, op uint32, body []byte, got *modv1.TransportScope) error {
			scoped++
			if op != req.Opcode || got.ConnectionId != "connection" || !got.SplineIdKnown || got.SplineId != 0 {
				t.Fatal("scope changed before transport handler", got)
			}
			body[0], got.ConnectionId = 99, "handler-mutation"
			return PacketNotDispatchedError("stale transport scope")
		},
	})
	if _, err := server.SendPacket(context.Background(), req); !PacketWasNotDispatched(err) || legacy != 0 || scoped != 1 {
		t.Fatal("scoped refusal fell back to opaque transport", err, legacy, scoped)
	}
	if scope.ConnectionId != "connection" || req.Payload[0] != 1 {
		t.Fatal("callback handler mutated the received request", req)
	}
	server = New(context.Background(), "legacy", Handlers{
		SendPacket: func(context.Context, uint32, []byte) error { legacy++; return nil },
		Clock:      func() uint32 { return 123 },
	})
	if _, err := server.SendPacket(context.Background(), req); !PacketWasNotDispatched(err) || legacy != 0 {
		t.Fatal("unsupported scope was silently discarded", err, legacy)
	}
	if _, err := server.SendPacket(context.Background(), &modv1.SendPacketRequest{Opcode: 7}); err != nil || legacy != 1 {
		t.Fatal("legacy packet path changed", err, legacy)
	}
	if clock, err := server.GetClock(context.Background(), nil); err != nil || clock.ClientTimeMs != 123 {
		t.Fatal("legacy clock path changed", clock, err)
	}
}
