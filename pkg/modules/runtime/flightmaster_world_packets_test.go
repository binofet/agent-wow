package modrt

import (
	"context"
	"encoding/binary"
	"path/filepath"
	"testing"

	buddyworld "github.com/agent-wow/agent-wow/modules/buddy-world"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/metadata"
)

func TestFlightmasterWorldCountMatchesTheActualOrderedSubscriber(t *testing.T) {
	registry, err := moddisc.Load(filepath.Join("..", "..", "..", "modules"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(registry, nil, nil)
	t.Cleanup(m.cancel)
	m.dir = t.TempDir()
	sid := filepath.Base(m.dir)
	m.session.Identity.CharacterGUID = 99
	w := buddyworld.NewIndependentService(99)
	scope := &modv1.TransportScope{ConnectionId: "test", SelfGuid: 99, VisitBound: true, VisitRevision: 9, MotionRevision: 5}
	npc := binary.LittleEndian.AppendUint64(nil, 0xf130000625000045)
	inst := m.instances["buddy-world"]
	consume := func() {
		t.Helper()
		p := m.nextPacket(inst)
		if p == nil {
			t.Fatal("World did not subscribe to packet")
		}
		if _, err := w.OnPacket(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	metadataForWorld := func() context.Context {
		t.Helper()
		view, err := w.Snapshot(context.Background(), &worldv1.SnapshotRequest{})
		if err != nil {
			t.Fatal(err)
		}
		out := modcb.WithFlightpathWorldPackets(context.Background(), view.ObservedPackets+view.IgnoredPackets+view.MalformedPackets)
		md, _ := metadata.FromOutgoingContext(out)
		return metadata.NewIncomingContext(context.Background(), md)
	}
	writes := 0
	m.session.SendScopedPacket = func(ctx context.Context, _ uint32, _ []byte, _ *modv1.TransportScope) error {
		if !modsession.FlightpathWorldCurrent(ctx) {
			return modcb.PacketNotDispatchedError("World observation changed at final check")
		}
		writes++
		return nil
	}
	m.session.SendPacket = func(context.Context, uint32, []byte) error { t.Fatal("unexpected unscoped query"); return nil }
	if err := m.PublishObserved(opcode.SMSGLoginVerifyWorld, make([]byte, 20), scope); err != nil {
		t.Fatal(err)
	}
	consume()
	write := m.moduleScopedPacketWriter(sid, actionsModule)
	old := metadataForWorld()
	if err := write(old, opcode.CMSGTaxinodeStatusQuery, npc, scope); err != nil {
		t.Fatal(err)
	}
	// A queue entry--even before its parser has run--invalidates the earlier
	// World observation. Consuming it never makes the earlier count current.
	if err := m.PublishObserved(opcode.SMSGMonsterMove, []byte{1, 42, 0}, scope); err != nil {
		t.Fatal(err)
	}
	for _, ctx := range []context.Context{old, metadataForWorld()} {
		if err := write(ctx, opcode.CMSGTaxinodeStatusQuery, npc, scope); !modcb.PacketWasNotDispatched(err) {
			t.Fatal(err)
		}
	}
	consume()
	if err := write(old, opcode.CMSGTaxinodeStatusQuery, npc, scope); !modcb.PacketWasNotDispatched(err) {
		t.Fatal(err)
	}
	current := metadataForWorld()
	if err := write(current, opcode.CMSGTaxiqueryavailablenodes, npc, scope); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		write(context.Background(), opcode.CMSGTaxinodeStatusQuery, npc, scope),
		write(current, opcode.CMSGActivatetaxi, npc, scope),
		write(current, opcode.CMSGTaxinodeStatusQuery, []byte{0}, scope),
		m.modulePacketWriter(sid, actionsModule)(current, opcode.CMSGTaxinodeStatusQuery, npc),
		m.moduleScopedPacketWriter(sid, "buddy-world")(current, opcode.CMSGTaxinodeStatusQuery, npc, scope),
		m.moduleScopedPacketWriter("different-session", actionsModule)(current, opcode.CMSGTaxinodeStatusQuery, npc, scope),
	} {
		if !modcb.PacketWasNotDispatched(err) {
			t.Fatal("scope leaked", err)
		}
	}
	if writes != 2 {
		t.Fatal(writes)
	}
}
