package buddyworld

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"testing"
)

func groupFullFixture(guid byte, zone uint16) []byte {
	// Canonical mana user without a pet; one active aura.
	b := binary.LittleEndian.AppendUint32([]byte{0, 1, guid}, 0x41bf7)
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = binary.LittleEndian.AppendUint32(b, 99)
	b = binary.LittleEndian.AppendUint32(b, 100)
	for _, n := range []uint16{33, 100, 42, zone, 4, 5} {
		b = binary.LittleEndian.AppendUint16(b, n)
	}
	b = binary.LittleEndian.AppendUint64(b, 1)
	b = binary.LittleEndian.AppendUint32(b, 458)
	b = append(b, 0)
	return append(b, make([]byte, 11)...)
}

func TestRemoteOwnerMapComesFromLivingPartyAfterPhysicalDeparture(t *testing.T) {
	s := NewService(1)
	deliver := func(op uint32, b []byte, visit, rev uint64) *worldv1.PlayerState {
		t.Helper()
		_, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: op, Payload: b, TransportScope: &modv1.TransportScope{ConnectionId: "party-socket", SelfGuid: 1, VisitBound: true, VisitRevision: visit, MapId: 0, MotionRevision: rev}})
		if err != nil {
			t.Fatal(err)
		}
		w, _ := s.Snapshot(context.Background(), &worldv1.SnapshotRequest{})
		return w.Owner
	}
	full := groupFullFixture(3, 1337)
	deliver(opcode.SMSGLoginVerifyWorld, login(), 8, 1)
	wire, _ := hex.DecodeString(heartbeatGolden)
	visible := deliver(opcode.MSGMoveHeartbeat, wire, 8, 2)
	if !visible.Present || !visible.PositionKnown {
		t.Fatal(visible)
	}
	if p := deliver(opcode.SMSGPartyMemberStatsFull, full, 8, 3); !p.Present || p.MapId != 0 {
		t.Fatal("foreign-map report superseded a visible owner", p)
	}
	gone := append(binary.LittleEndian.AppendUint64(nil, 3), 0)
	deliver(opcode.SMSGDestroyObject, gone, 8, 4)
	for _, x := range []struct {
		p          []byte
		visit, rev uint64
	}{{full, 8, 3}, {full, 9, 5}, {groupFullFixture(6, 1337), 8, 5}, {groupFullFixture(3, 1517), 8, 6}} {
		if p := deliver(opcode.SMSGPartyMemberStatsFull, x.p, x.visit, x.rev); p.Present || p.MapKnown || p.PositionKnown {
			t.Fatal("stale, foreign or outdoor zone", p)
		}
	}
	p := deliver(opcode.SMSGPartyMemberStatsFull, full, 8, 7)
	if !p.MapKnown || p.MapId != 70 || p.Present || p.PositionKnown || p.X != 0 || p.Y != 0 || p.Z != 0 || p.PositionRevision != visible.PositionRevision {
		t.Fatal("true remote zone or sighting", p)
	}
	death := binary.LittleEndian.AppendUint32([]byte{1, 3}, 1)
	death = binary.LittleEndian.AppendUint16(death, 5)
	if p = deliver(opcode.SMSGPartyMemberStats, death, 8, 8); p.MapKnown {
		t.Fatal("dead owner kept a usable destination", p)
	}
	if p = deliver(opcode.SMSGPartyMemberStatsFull, full, 8, 7); p.MapKnown {
		t.Fatal("backlogged living FULL after death", p)
	}
	if p = deliver(opcode.SMSGPartyMemberStatsFull, full, 8, 9); !p.MapKnown {
		t.Fatal("new genuine report ignored", p)
	}
	if p = deliver(opcode.SMSGPartyMemberStatsFull, append(append([]byte(nil), full...), 99), 8, 10); p.MapKnown {
		t.Fatal("malformed report retained map", p)
	}
	if p = deliver(opcode.SMSGPartyMemberStatsFull, full, 8, 11); !p.MapKnown {
		t.Fatal(p)
	}
	if p = deliver(opcode.SMSGGroupDestroyed, nil, 8, 12); p.MapKnown {
		t.Fatal("membership ended", p)
	}
	deliver(opcode.SMSGNewWorld, login(), 9, 13)
	if p = deliver(opcode.SMSGPartyMemberStatsFull, full, 9, 14); p.MapKnown || p.Present {
		t.Fatal("new visit without departure", p)
	}
}
