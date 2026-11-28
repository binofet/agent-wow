package buddyworld

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const testShipGUID uint64 = 0x1fc00000f0043210

// MotionTransport's actual CREATE2: STATIONARY_POSITION, LOWGUID, TRANSPORT
// and ROTATION. Both the progress counter and quaternion can be zero on wire.
func shipCreate(guid uint64, entry, kind, progress uint32) []byte {
	b := new(bytes.Buffer)
	u32(b, 1)
	b.WriteByte(3)
	b.Write(packedFixture(guid))
	b.WriteByte(5)
	binary.Write(b, binary.LittleEndian, uint16(0x252))
	for _, v := range []float32{100, 240, 18, .75} {
		f32(b, v)
	}
	u32(b, uint32(guid))
	u32(b, progress)
	binary.Write(b, binary.LittleEndian, uint64(0))
	valuesFixture(b, map[int]uint32{objectEntry: entry, 4: math.Float32bits(1.25), 8: 3015, 0x10: 300000,
		gameObjectDynamic: 0x789a0001, gameObjectBytes: kind<<8 | 1})
	return b.Bytes()
}

func shipPassengerInfo(guid uint64) movementinfo.Info {
	return movementinfo.Info{Flags: 0x201, ExtraFlags: 0x400, Time: 0xfffffff1, X: 328, Y: 100, Z: 32,
		Orientation: 2, TransportPresent: true,
		Transport: movementinfo.Transport{GUID: guid, X: 10, Y: -3, Z: 6, Orientation: .25,
			Time: 0xfffffffd, Time2Present: true, Time2: 0, Seat: -1}}
}

func shipMovement(t *testing.T, i movementinfo.Info) []byte {
	t.Helper()
	b, err := movementinfo.Encode(i)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func passengerCreate(t *testing.T, guid uint64, i movementinfo.Info) []byte {
	t.Helper()
	b := new(bytes.Buffer)
	u32(b, 1)
	b.WriteByte(3)
	b.Write(packedFixture(guid))
	b.WriteByte(4)
	binary.Write(b, binary.LittleEndian, uint16(0x20))
	b.Write(shipMovement(t, i))
	for _, v := range []float32{2.5, 7, 4.5, 4.7, 2.5, 7, 4.5, 3.14, 3.14} {
		f32(b, v)
	}
	valuesFixture(b, vitalFields())
	return b.Bytes()
}

func TestMovingTransportReceivedHullAndPlayerCoordinates(t *testing.T) {
	s := NewService(1)
	now := time.Unix(200, 0)
	s.now = func() time.Time { return now }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	r := packet(t, s, opcode.SMSGCompressedUpdateObject, compressed(t, shipCreate(testShipGUID, 87123, 15, 0)))
	if r.MalformedPackets != 0 || len(r.MovingTransports) != 1 {
		t.Fatalf("complete moving-transport CREATE missing: %v", r.MovingTransports)
	}
	hull := r.MovingTransports[0]
	f := hull.MovingTransport
	if hull.Guid != testShipGUID || hull.Entry != 87123 || hull.GameObjectType != 15 || !hull.MapKnown || !hull.PositionKnown ||
		hull.X != 100 || hull.Y != 240 || hull.Z != 18 || hull.Orientation != .75 || hull.DynamicFlags != 0x789a0001 ||
		f.GetDisplayId() != 3015 || f.GetScale() != 1.25 || f.GetPeriodMs() != 300000 || f.PathProgressMs == nil || *f.PathProgressMs != 0 ||
		f.PackedRotation == nil || *f.PackedRotation != 0 || f.PositionRevision == 0 || f.PositionRevision != hull.PositionRevision ||
		f.MovementRevision == 0 || f.SourceOpcode != opcode.SMSGCompressedUpdateObject || f.GetPathFraction() != 0x789a ||
		f.PathFractionRevision != f.MovementRevision || len(r.QuestObjects)+len(r.Mailboxes) != 0 {
		t.Fatal(hull)
	}
	now = now.Add(2 * time.Second)
	i := shipPassengerInfo(testShipGUID)
	packet(t, s, opcode.MSGMoveHeartbeat, ownMovementMessage(3, shipMovement(t, i)))
	packet(t, s, opcode.SMSGCompressedUpdateObject, compressed(t, passengerCreate(t, 6, i)))
	now = now.Add(500 * time.Millisecond)
	r, err := s.Snapshot(context.Background(), &worldv1.SnapshotRequest{GameObjectEntry: 87123})
	if err != nil || len(r.RequestedGameObjects) != 1 || r.RequestedGameObjects[0].MovingTransport == nil ||
		r.Self.PassengerMovement != nil || !r.Owner.Present || r.Owner.Guid != 3 {
		t.Fatal("ordinary snapshot/entry lookup lost moving transport or fabricated own boarding")
	}
	for _, p := range []*worldv1.PlayerState{r.Owner, nearbyByGUID(t, r, 6)} {
		m := p.PassengerMovement
		if m == nil || m.VisitRevision != r.VisitRevision || m.Revision <= f.MovementRevision || m.AgeMs != 500 ||
			m.Info.X != 328 || m.Info.Y != 100 || m.Info.Z != 32 || m.Info.Orientation != 2 || m.Info.Flags != i.Flags || m.Info.Time != i.Time ||
			m.Info.Transport.Guid != testShipGUID || m.Info.Transport.X != 10 || m.Info.Transport.Y != -3 || m.Info.Transport.Z != 6 ||
			m.Info.Transport.Orientation != .25 || m.Info.Transport.Time != i.Transport.Time || m.Info.Transport.Seat != -1 ||
			!m.Info.Transport.Time2Known || m.Info.Transport.Time2 != 0 {
			t.Fatal(m)
		}
	}
	if r.Owner.PassengerMovement.SourceOpcode != opcode.MSGMoveHeartbeat || nearbyByGUID(t, r, 6).PassengerMovement.SourceOpcode != opcode.SMSGCompressedUpdateObject {
		t.Fatal("lost actual passenger source")
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(testShipGUID, map[int]uint32{gameObjectDynamic: 0x80000001}))
	hull = r.MovingTransports[0]
	if hull.X != 100 || hull.PositionAgeMs != 2500 || hull.MovingTransport.MovementAgeMs != 2500 ||
		hull.MovingTransport.MovementRevision != f.MovementRevision || r.Owner.PassengerMovement.AgeMs != 500 ||
		hull.MovingTransport.GetPathFraction() != 0x8000 || hull.MovingTransport.PathFractionAgeMs != 0 ||
		hull.MovingTransport.PathFractionRevision <= f.PathFractionRevision || hull.MovingTransport.PathFractionSourceOpcode != opcode.SMSGUpdateObject {
		t.Fatal("a passenger or unrelated scalar rewrote the hull's measured pose/clock", hull, r.Owner.PassengerMovement)
	}
	now = now.Add(time.Second)
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(testShipGUID, map[int]uint32{gameObjectFlags: 0x20}))
	if f := r.MovingTransports[0].MovingTransport; f.MovementAgeMs != 3500 || f.PathFractionAgeMs != 1000 {
		t.Fatal("unrelated field renewed a transport clock", f)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, passengerCreate(t, 1, i))
	if r.Self.PassengerMovement.GetInfo().GetTransport().GetGuid() != testShipGUID ||
		r.SelfMovementFacts.Info.GetTransport().GetSeat() != -1 {
		t.Fatal("own CREATE lost original attachment")
	}
	r = packet(t, s, opcode.MSGMoveStartSwim, ownMovementMessage(1, shipMovement(t, movementinfo.Info{Flags: 0x200000, PitchPresent: true, X: 330, Y: 101, Z: 12})))
	if m := r.Self.PassengerMovement; m == nil || m.Info.Transport != nil || m.Info.Flags != 0x200000 || m.SourceOpcode != opcode.MSGMoveStartSwim {
		t.Fatal("actual disembark did not replace the old attachment", m)
	}
}

func TestMovingTransportLifetimeAndMalformedInput(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	for _, bad := range []struct {
		guid uint64
		kind uint32
	}{
		{0x1fc0000000000000, 15}, {0x1fc0000100000001, 15}, {0xf110000001000003, 15}, {testShipGUID, 3},
	} {
		if got := packet(t, s, opcode.SMSGUpdateObject, shipCreate(bad.guid, 87123, bad.kind, 17)); len(got.MovingTransports) != 0 {
			t.Fatal("invalid moving transport identity/type", bad, got.MovingTransports)
		}
	}
	for _, mode := range []string{"destroy", "out of range", "malformed destroy", "malformed compressed", "recreate", "changed template"} {
		t.Run(mode, func(t *testing.T) {
			packet(t, s, opcode.SMSGUpdateObject, shipCreate(testShipGUID, 87123, 15, 42))
			rider := shipPassengerInfo(testShipGUID)
			packet(t, s, opcode.MSGMoveHeartbeat, ownMovementMessage(3, shipMovement(t, rider)))
			r := packet(t, s, opcode.SMSGUpdateObject, passengerCreate(t, 6, rider))
			if r.Owner.PassengerMovement == nil || nearbyByGUID(t, r, 6).PassengerMovement == nil {
				t.Fatal("ride fixture has no passenger")
			}
			switch mode {
			case "destroy", "malformed destroy":
				b := binary.LittleEndian.AppendUint64(nil, testShipGUID)
				if mode == "destroy" {
					b = append(b, 0)
				}
				r = packet(t, s, opcode.SMSGDestroyObject, b)
			case "out of range":
				b := binary.LittleEndian.AppendUint32(nil, 1)
				b = binary.LittleEndian.AppendUint32(append(b, 4), 1)
				r = packet(t, s, opcode.SMSGUpdateObject, append(b, packedFixture(testShipGUID)...))
			case "malformed compressed":
				r = packet(t, s, opcode.SMSGCompressedUpdateObject, []byte{3})
			case "recreate":
				r = packet(t, s, opcode.SMSGUpdateObject, shipCreate(testShipGUID, 87123, 15, 99))
			case "changed template":
				r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(testShipGUID, map[int]uint32{objectEntry: 87124}))
			}
			if r.Owner.PassengerMovement != nil || nearbyByGUID(t, r, 6).PassengerMovement != nil ||
				(mode == "recreate") != (len(r.MovingTransports) == 1) {
				t.Fatal("old passenger survived lost or replaced ship", mode)
			}
		})
	}
	packet(t, s, opcode.SMSGUpdateObject, shipCreate(testShipGUID, 87123, 15, 17))
	for _, bad := range []movementinfo.Info{shipPassengerInfo(0), shipPassengerInfo(0x0100000000000002), shipPassengerInfo(3), shipPassengerInfo(testShipGUID)} {
		if bad.Transport.GUID == testShipGUID {
			bad.Transport.Z = float32(math.NaN())
		}
		packet(t, s, opcode.MSGMoveHeartbeat, ownMovementMessage(3, shipMovement(t, shipPassengerInfo(testShipGUID))))
		r := packet(t, s, opcode.MSGMoveHeartbeat, ownMovementMessage(3, shipMovement(t, bad)))
		if r.Owner.PassengerMovement != nil || len(r.MovingTransports) != 1 {
			t.Fatal("bad rider geometry projected as an attachment/departure", r.Owner)
		}
	}
}

func TestTransportWorldportKeepsDeckOffsetsOutOfWorldPose(t *testing.T) {
	for _, form := range []string{"ship", "plain", "truncated", "overlap", "missing after known attachment"} {
		t.Run(form, func(t *testing.T) {
			s := NewService(1)
			packet(t, s, opcode.SMSGLoginVerifyWorld, login())
			packet(t, s, opcode.SMSGUpdateObject, shipCreate(testShipGUID, 87123, 15, 27))
			old := packet(t, s, opcode.SMSGUpdateObject, passengerCreate(t, 1, shipPassengerInfo(testShipGUID)))
			pending := binary.LittleEndian.AppendUint32(nil, 1)
			if form == "ship" || form == "truncated" || form == "overlap" {
				pending = binary.LittleEndian.AppendUint32(pending, 87123)
				pending = binary.LittleEndian.AppendUint32(pending, 0)
				if form == "truncated" {
					pending = pending[:9]
				}
			}
			if form != "missing after known attachment" {
				packet(t, s, opcode.SMSGTransferPending, pending)
			}
			if form == "overlap" {
				packet(t, s, opcode.SMSGTransferPending, pending[:4])
			}
			b := new(bytes.Buffer)
			u32(b, 1)
			for _, v := range []float32{2, -3, 6, .25} {
				f32(b, v)
			}
			r := packet(t, s, opcode.SMSGNewWorld, b.Bytes())
			if r.VisitRevision != old.VisitRevision+1 || !r.Self.Present || !r.Self.MapKnown || r.Self.MapId != 1 ||
				len(r.MovingTransports) != 0 || r.Self.PassengerMovement != nil || r.SelfMovementFacts.Known ||
				r.Self.PositionKnown != (form == "plain") || form == "plain" && r.Self.X != 2 {
				t.Fatal("incorrect coordinate domain at worldport", form, r.Self, r.SelfMovementFacts)
			}
			r = packet(t, s, opcode.SMSGUpdateObject, passengerCreate(t, 1, shipPassengerInfo(testShipGUID)))
			if !r.Self.PositionKnown || r.Self.X != 328 || r.Self.PassengerMovement.GetVisitRevision() != r.VisitRevision ||
				r.Self.PassengerMovement.GetInfo().GetTransport().GetX() != 10 || len(r.MovingTransports) != 0 {
				t.Fatal("genuine new-visit passenger pose did not recover independently", r.Self)
			}
		})
	}
}
