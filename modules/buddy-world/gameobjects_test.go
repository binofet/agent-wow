package buddyworld

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const mailboxGUID uint64 = 0xf11000007b00002a

// Public GO CREATE framing: LOWGUID|STATIONARY_POSITION|POSITION|ROTATION,
// no transport, world XYZ + transport fallbackXYZ, orientation, lowGUID,
// packedrotation, then public entry and GAMEOBJECT_BYTES_1 typebyte1.
const mailboxGolden = "0100000003c92a7b10f1055003000000803f00000040000040400000803f00000040000040400000003f000000002a000000000000000000000001080002007b00000001130000"

func gameObjectFixture(guid uint64, fields map[int]uint32) []byte {
	b := new(bytes.Buffer)
	u32(b, 1)
	b.WriteByte(3)
	b.Write(packedFixture(guid))
	b.WriteByte(5)
	_ = binary.Write(b, binary.LittleEndian, uint16(0x350))
	b.WriteByte(0)
	for _, v := range []float32{1, 2, 3, 1, 2, 3, .5, 0} {
		f32(b, v)
	}
	u32(b, uint32(guid))
	_ = binary.Write(b, binary.LittleEndian, uint64(0))
	valuesFixture(b, fields)
	return b.Bytes()
}

func TestMailboxLiteralGoldenAndCompressed(t *testing.T) {
	b, err := hex.DecodeString(mailboxGolden)
	if err != nil {
		t.Fatal(err)
	}
	for _, zipped := range []bool{false, true} {
		s := NewService(1)
		packet(t, s, opcode.SMSGLoginVerifyWorld, login())
		op, data := uint32(opcode.SMSGUpdateObject), b
		if zipped {
			op, data = opcode.SMSGCompressedUpdateObject, compressed(t, b)
		}
		r := packet(t, s, op, data)
		if r.MalformedPackets != 0 || len(r.Mailboxes) != 1 {
			t.Fatal(r)
		}
		p := r.Mailboxes[0]
		if p.Guid != mailboxGUID || p.Entry != 123 || !p.Present || !p.MapKnown || p.MapId != 0 || !p.PositionKnown || p.X != 1 || p.Y != 2 || p.Z != 3 || p.Orientation != .5 || !p.TypeKnown || p.GameObjectType != 19 || p.PositionRevision != 1 {
			t.Fatal(p)
		}
		p.X = 999
		r, _ = s.Snapshot(context.Background(), nil)
		if r.Mailboxes[0].X != 1 {
			t.Fatal("snapshot sharedmutableposition")
		}
	}
}
func TestMailboxTypeByteDeltaMovementAndLifecycle(t *testing.T) {
	s := NewService(1)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	r := packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(mailboxGUID, map[int]uint32{objectEntry: 123, gameObjectBytes: 19}))
	if len(r.Mailboxes) != 0 {
		t.Fatal("mistook statebyte0 for typebyte1")
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(mailboxGUID, map[int]uint32{gameObjectBytes: 19<<8 | 1}))
	if len(r.Mailboxes) != 1 {
		t.Fatal(r)
	}
	b := new(bytes.Buffer)
	u32(b, 1)
	b.WriteByte(1)
	b.Write(packedFixture(mailboxGUID))
	_ = binary.Write(b, binary.LittleEndian, uint16(0x40))
	for _, v := range []float32{4, 5, 6, 1} {
		f32(b, v)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, b.Bytes())
	if r.Mailboxes[0].X != 4 || r.Mailboxes[0].PositionRevision != 2 {
		t.Fatal(r)
	}
	now = now.Add(time.Second)
	r, _ = s.Snapshot(context.Background(), nil)
	if r.Mailboxes[0].PositionAgeMs != 1000 || !r.Mailboxes[0].Present {
		t.Fatal(r)
	}
	for _, mode := range []string{"destroy", "outofrange", "transfer", "newworld"} {
		packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(mailboxGUID, map[int]uint32{objectEntry: 123, gameObjectBytes: 19 << 8}))
		b.Reset()
		switch mode {
		case "destroy":
			_ = binary.Write(b, binary.LittleEndian, mailboxGUID)
			b.WriteByte(0)
			r = packet(t, s, opcode.SMSGDestroyObject, b.Bytes())
		case "outofrange":
			u32(b, 1)
			b.WriteByte(4)
			u32(b, 1)
			b.Write(packedFixture(mailboxGUID))
			r = packet(t, s, opcode.SMSGUpdateObject, b.Bytes())
		case "transfer":
			r = packet(t, s, opcode.SMSGTransferPending, []byte{0, 0, 0, 0})
		default:
			r = packet(t, s, opcode.SMSGNewWorld, login())
		}
		if len(r.Mailboxes) != 0 || len(s.state.gameObjects) != 0 {
			t.Fatalf("%s retained stale mailbox", mode)
		}
	}
}
func TestMailboxMalformedPacketsRollBackWithOwnWallet(t *testing.T) {
	b, err := hex.DecodeString(mailboxGolden)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	for n := 0; n < len(b); n++ {
		packet(t, s, opcode.SMSGUpdateObject, b[:n])
	}
	if len(s.state.gameObjects) != 0 {
		t.Fatal("truncatedCREATE filled cache")
	}
	data := deltaFixture(1, map[int]uint32{playerCoinage: 10000})
	data = append(data, b[4:]...)
	data = append(data, 255)
	binary.LittleEndian.PutUint32(data, 3)
	r := packet(t, s, opcode.SMSGUpdateObject, data)
	if len(r.Mailboxes) != 0 || r.SelfCopper != 0 || r.SelfCopperRevision != 1 {
		t.Fatal("malformedtail partiallycommitted", r)
	}
}
func TestMailboxPopulationBoundAndNPCIsolation(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	for i := uint64(1); i <= 160; i++ {
		guid := uint64(0xf11000007b000000) | i
		packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(guid, map[int]uint32{objectEntry: 123, gameObjectBytes: 19 << 8}))
	}
	r, _ := s.Snapshot(context.Background(), nil)
	if len(s.state.gameObjects) != maxGameObjects || len(r.Mailboxes) != 32 || len(s.state.creatures) != 0 {
		t.Fatal("mailbox population unbounded")
	}
	for i := uint64(200); i <= 400; i++ {
		guid := uint64(0xf11000007b000000) | i
		packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(guid, map[int]uint32{objectEntry: 123}))
	}
	r, _ = s.Snapshot(context.Background(), nil)
	if len(r.Mailboxes) != 32 || len(s.state.gameObjects) != 128 {
		t.Fatal("ordinary props evicted all mailboxes")
	}
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(npcGUID, 3, map[int]uint32{objectEntry: 123, gameObjectBytes: 19 << 8}))
	r, _ = s.Snapshot(context.Background(), nil)
	if len(r.Mailboxes) != 32 || len(s.state.creatures) != 1 {
		t.Fatal("NPC typebyte leaked into mailboxcache")
	}
}
