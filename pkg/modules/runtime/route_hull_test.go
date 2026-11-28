package modrt

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestNativeHullMetadataRequiresTheSameScopedRouteAndObservedWorld(t *testing.T) {
	registry, err := moddisc.Load(filepath.Join("..", "..", "..", "modules"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(registry, nil, nil)
	t.Cleanup(m.cancel)
	m.dir = t.TempDir()
	m.session.Identity.CharacterGUID = 99
	scope := &modv1.TransportScope{ConnectionId: "ship-session", SelfGuid: 99, VisitBound: true, VisitRevision: 1, MotionRevision: 4}
	const ship uint64 = 0x1fc0000000000077
	if err := m.PublishObserved(opcode.SMSGLoginVerifyWorld, make([]byte, 20), scope); err != nil {
		t.Fatal(err)
	}
	writes := 0
	m.session.SendScopedPacket = func(ctx context.Context, op uint32, payload []byte, scoped *modv1.TransportScope) error {
		w, ok := modsession.RouteHull(ctx)
		if !ok || w != (modcb.RouteHull{GUID: ship, Birth: 34, Packets: 1}) || !modsession.IsRouteBodyInput(ctx) {
			t.Fatal("authenticated route lost the selected ship")
		}
		writes++
		return nil
	}
	ctx := modcb.WithMovementAttempt(modcb.WithRouteBodyInput(context.Background(), false), strings.Repeat("0", 64))
	verified := modcb.WithRouteHull(ctx, ship, 34, 1)
	wire, _ := movementinfo.MoverFrame(99, movementinfo.Info{Flags: 0x201, TransportPresent: true,
		Transport: movementinfo.Transport{GUID: ship, Seat: -1}})
	native := m.moduleScopedPacketWriter(filepath.Base(m.dir), actionsModule)
	if err := native(instanceEntryIncoming(verified), opcode.MSGMoveStartForward, wire, scope); err != nil || writes != 1 {
		t.Fatal("valid hull source", err, writes)
	}
	for _, bad := range []context.Context{
		ctx, modcb.WithRouteHull(ctx, ship+1, 34, 1), modcb.WithRouteHull(ctx, ship, 0, 1),
		modcb.WithRouteHull(ctx, ship, 34, 2), modcb.WithRouteHull(verified, ship, 34, 1),
		modcb.WithRouteHull(context.Background(), ship, 34, 1), modcb.WithPolicySpell(verified, "foreign"),
	} {
		if err := native(instanceEntryIncoming(bad), opcode.MSGMoveStartForward, wire, scope); !modcb.PacketWasNotDispatched(err) {
			t.Fatal("invalid hull binding reached the native writer", err)
		}
	}
	other := m.moduleScopedPacketWriter(filepath.Base(m.dir), "buddy-chat")
	if err := other(instanceEntryIncoming(verified), opcode.MSGMoveStartForward, wire, scope); !modcb.PacketWasNotDispatched(err) {
		t.Fatal("other module admitted", err)
	}
	if err := m.modulePacketWriter(filepath.Base(m.dir), actionsModule)(instanceEntryIncoming(verified), opcode.MSGMoveStartForward, wire); !modcb.PacketWasNotDispatched(err) || writes != 1 {
		t.Fatal("unscoped hull admitted", err, writes)
	}
}
