package buddyworld

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/hex"
	"math"
	"sort"
	"sync"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func packet(t *testing.T, s *Service, op uint32, b []byte) *worldv1.SnapshotResponse {
	t.Helper()
	if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: op, Payload: b}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Snapshot(context.Background(), &worldv1.SnapshotRequest{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func u32(b *bytes.Buffer, v uint32)  { _ = binary.Write(b, binary.LittleEndian, v) }
func f32(b *bytes.Buffer, v float32) { u32(b, math.Float32bits(v)) }
func login() []byte {
	b := new(bytes.Buffer)
	u32(b, 0)
	for _, v := range []float32{-8949.95, -132.493, 83.5312, 0} {
		f32(b, v)
	}
	return b.Bytes()
}

// This literal is the public 3.3.5a MovementInfo layout: packed player 3,
// flags, flags2, timestamp, XYZO, fallTime. It is not encoded by production code.
const heartbeatGolden = "0103010000000000d20400000000803f00000040000040400000003f00000000"

func TestLiteralLoginMovementAndStationaryFreshness(t *testing.T) {
	s := NewService(1)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	r := packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	if !r.Self.Present || !r.Self.MapKnown || !r.Self.PositionKnown || r.Self.MapId != 0 {
		t.Fatalf("login: %v", r)
	}
	b, err := hex.DecodeString(heartbeatGolden)
	if err != nil {
		t.Fatal(err)
	}
	r = packet(t, s, opcode.MSGMoveHeartbeat, b)
	if !r.Owner.Present || r.Owner.Guid != 3 || r.Owner.X != 1 || r.Owner.Y != 2 || r.Owner.Z != 3 || r.Owner.Orientation != 0.5 || r.Owner.MovementFlags != 1 || r.Owner.MovementTime != 1234 || !r.Owner.MapKnown {
		t.Fatalf("movement: %v", r)
	}
	before := r.Owner.PositionRevision
	now = now.Add(30 * time.Second)
	r, _ = s.Snapshot(context.Background(), nil)
	if !r.Owner.Present || !r.Owner.PositionKnown || r.Owner.PositionAgeMs != 30000 || r.Owner.PositionRevision != before {
		t.Fatalf("stationary: %v", r.Owner)
	}
}

func valuesFixture(b *bytes.Buffer, fields map[int]uint32) {
	indices := make([]int, 0, len(fields))
	high := 0
	for i := range fields {
		indices = append(indices, i)
		if i > high {
			high = i
		}
	}
	sort.Ints(indices)
	n := high/32 + 1
	b.WriteByte(byte(n))
	for block := 0; block < n; block++ {
		var mask uint32
		for _, i := range indices {
			if i/32 == block {
				mask |= 1 << uint(i%32)
			}
		}
		u32(b, mask)
	}
	for _, i := range indices {
		u32(b, fields[i])
	}
}
func createFixture(guid byte, fields map[int]uint32) []byte {
	b := new(bytes.Buffer)
	u32(b, 1)
	b.Write([]byte{3, 1, guid, 4, 0x20, 0})
	// flags, flags2, timestamp, XYZO, fallTime, all nine movement speeds.
	u32(b, 0)
	b.Write([]byte{0, 0})
	u32(b, 123)
	for _, v := range []float32{1, 2, 3, 0} {
		f32(b, v)
	}
	u32(b, 0)
	for _, v := range []float32{2.5, 7, 4.5, 4.7, 2.5, 7, 4.5, 3.14, 3.14} {
		f32(b, v)
	}
	valuesFixture(b, fields)
	return b.Bytes()
}
func vitalFields() map[int]uint32 {
	return map[int]uint32{unitBytes0: 1 | (5 << 8), unitHealth: 50, unitMaxHealth: 100, unitPower1: 80, unitMaxPower1: 120, unitFlags: 8}
}
func compressed(t *testing.T, b []byte) []byte {
	t.Helper()
	out := new(bytes.Buffer)
	u32(out, uint32(len(b)))
	z := zlib.NewWriter(out)
	if _, err := z.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestCreateCompressedVitalsAndZeroFieldDefaults(t *testing.T) {
	for _, compressedPacket := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "zlib"}[compressedPacket], func(t *testing.T) {
			s := NewService(1)
			packet(t, s, opcode.SMSGLoginVerifyWorld, login())
			op := uint32(opcode.SMSGUpdateObject)
			data := createFixture(3, vitalFields())
			if compressedPacket {
				op = opcode.SMSGCompressedUpdateObject
				data = compressed(t, data)
			}
			r := packet(t, s, op, data)
			p := r.Owner
			if r.MalformedPackets != 0 || !p.Present || !p.MapKnown || !p.PositionKnown || !p.HealthKnown || p.Health != 50 || p.MaxHealth != 100 || !p.PowerKnown || p.PowerType != 0 || p.Power != 80 || p.MaxPower != 120 || !p.AliveKnown || !p.Alive || !p.UnitFlagsKnown || !p.SpeedsKnown || p.RunSpeed != 7 {
				t.Fatalf("create: %v", r)
			}
			// Omitted public player flags are zero, so alive is known on CREATE.
			b := new(bytes.Buffer)
			u32(b, 1)
			b.Write([]byte{0, 1, 3})
			valuesFixture(b, map[int]uint32{unitHealth: 0})
			r = packet(t, s, opcode.SMSGUpdateObject, b.Bytes())
			if r.Owner.Alive || !r.Owner.AliveKnown || r.Owner.Health != 0 || r.Owner.MaxHealth != 100 {
				t.Fatalf("death: %v", r.Owner)
			}
		})
	}
}

func TestSelfCreateRetainsMapAndRevisions(t *testing.T) {
	s := NewService(1)
	r := packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	rev := r.Self.PositionRevision
	r = packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	if !r.Self.MapKnown || r.Self.MapId != 0 || r.Self.PositionRevision <= rev {
		t.Fatalf("self create lost map or revision: %v", r.Self)
	}
}

func TestOutOfRangeDestroyAndTransfer(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(3, vitalFields()))
	// uint32 blockcount, type OUT_OF_RANGE, uint32 count, packed GUID3.
	r := packet(t, s, opcode.SMSGUpdateObject, []byte{1, 0, 0, 0, 4, 1, 0, 0, 0, 1, 3})
	if r.Owner.Present || r.Owner.PositionKnown || r.Owner.MapKnown {
		t.Fatal(r.Owner)
	}
	packet(t, s, opcode.SMSGUpdateObject, createFixture(3, vitalFields()))
	r = packet(t, s, opcode.SMSGDestroyObject, []byte{3, 0, 0, 0, 0, 0, 0, 0, 0})
	if r.Owner.Present {
		t.Fatal(r.Owner)
	}
	r = packet(t, s, opcode.SMSGTransferPending, []byte{1, 0, 0, 0})
	if r.Self.PositionKnown || r.Self.Present {
		t.Fatal(r.Self)
	}
}

func TestOptionalMovementAndUntrackedSplineCreate(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	b := new(bytes.Buffer)
	b.Write([]byte{1, 3})
	u32(b, 0x06201200)
	b.Write([]byte{0x20, 0x04})
	u32(b, 1)
	for i := 0; i < 4; i++ {
		f32(b, float32(i))
	}
	b.WriteByte(0)
	b.Write(make([]byte, 16+4+1+4))
	f32(b, 0.5)
	u32(b, 0)
	b.Write(make([]byte, 16))
	f32(b, 1)
	r := packet(t, s, opcode.MSGMoveHeartbeat, b.Bytes())
	if r.MalformedPackets != 0 || !r.Owner.PositionKnown || r.Owner.ExtraMovementFlags != 0x420 {
		t.Fatal(r)
	}
	// An unrelated NPC create with a spline precedes the owner CREATE.
	npc := createFixture(9, vitalFields())
	npc[7] = 3 // object type UNIT
	binary.LittleEndian.PutUint32(npc[10:], 0x08000000)
	// Insert spline create after movement info (30 bytes) and nine speeds.
	insert := 10 + 30 + 36
	sp := new(bytes.Buffer)
	u32(sp, 0x20000)
	f32(sp, 0.5)
	sp.Write(make([]byte, 28))
	u32(sp, 2)
	sp.Write(make([]byte, 24+1+12))
	npc = append(append(append([]byte{}, npc[:insert]...), sp.Bytes()...), npc[insert:]...)
	owner := createFixture(3, vitalFields())
	binary.LittleEndian.PutUint32(npc, 2)
	npc = append(npc, owner[4:]...)
	r = packet(t, s, opcode.SMSGUpdateObject, npc)
	if r.MalformedPackets != 0 || !r.Owner.HealthKnown || r.Owner.Health != 50 {
		t.Fatal(r)
	}
}

func TestMalformedPacketsAreAtomicAndDoNotReturnCallbackErrors(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(3, vitalFields()))
	good, _ := hex.DecodeString(heartbeatGolden)
	for n := 0; n < len(good); n++ {
		r := packet(t, s, opcode.MSGMoveHeartbeat, good[:n])
		if r.Owner.X != 1 || r.Owner.PositionRevision != 1 || r.MalformedPackets != uint64(n+1) {
			t.Fatalf("prefix %d applied: %v", n, r)
		}
	}
	bad := append([]byte{}, good...)
	binary.LittleEndian.PutUint32(bad[12:], 0x7fc00000)
	r := packet(t, s, opcode.MSGMoveHeartbeat, bad)
	if r.Owner.PositionRevision != 1 {
		t.Fatal("NaN applied")
	}
	// First update is valid, but an unknown second block must roll it back.
	b := new(bytes.Buffer)
	u32(b, 2)
	b.Write([]byte{0, 1, 3})
	valuesFixture(b, map[int]uint32{unitHealth: 1})
	b.WriteByte(255)
	r = packet(t, s, opcode.SMSGUpdateObject, b.Bytes())
	if r.Owner.Health != 50 || r.LastParseError == "" {
		t.Fatal(r)
	}
	for _, data := range [][]byte{{0xff, 0xff, 0xff, 0xff}, {4, 0, 0, 0, 1, 2, 3}, compressed(t, []byte{0, 0, 0, 0})[:6]} {
		packet(t, s, opcode.SMSGCompressedUpdateObject, data)
	}
	packet(t, s, 0xffff, []byte{1})
	r, _ = s.Snapshot(context.Background(), nil)
	if r.IgnoredPackets != 1 {
		t.Fatal(r)
	}
}

func TestBoundedKnownPlayersAndConcurrentSnapshots(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGUpdateObject, Payload: createFixture(9, vitalFields())})
				s.Snapshot(context.Background(), nil)
			}
		}()
	}
	wg.Wait()
	r, _ := s.Snapshot(context.Background(), nil)
	if r.Owner.Present || r.Owner.Guid != 3 || r.Self.Guid != 1 || r.ObservedPackets != 801 {
		t.Fatal(r)
	}
}

func TestServerCorrectionRootSpeedAndUnpredictedTrajectory(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	force := []byte{1, 1, 0, 0, 0, 0}
	r := packet(t, s, opcode.SMSGForceMoveRoot, force)
	if !r.Self.MovementFlagsKnown || r.Self.MovementFlags&0x800 == 0 {
		t.Fatal(r.Self)
	}
	r = packet(t, s, opcode.SMSGForceMoveUnroot, force)
	if r.Self.MovementFlags&0x800 != 0 {
		t.Fatal(r.Self)
	}
	b := new(bytes.Buffer)
	b.Write(force)
	b.WriteByte(0)
	f32(b, 3.5)
	r = packet(t, s, opcode.SMSGForceRunSpeedChange, b.Bytes())
	if r.Self.RunSpeed != 3.5 {
		t.Fatal(r.Self)
	}
	m, _ := hex.DecodeString(heartbeatGolden)
	m[1] = 1
	b.Reset()
	b.Write(m[:2])
	u32(b, 5)
	b.Write(m[2:])
	r = packet(t, s, opcode.MSGMoveTeleportAck, b.Bytes())
	if r.Self.X != 1 || r.Self.PositionRevision != 3 {
		t.Fatal(r.Self)
	}
	r = packet(t, s, opcode.SMSGMoveKnockBack, append([]byte{1, 1}, make([]byte, 20)...))
	if r.Self.PositionKnown || r.Self.MovementFlagsKnown {
		t.Fatal(r.Self)
	}
}

func FuzzObservationPackets(f *testing.F) {
	golden, _ := hex.DecodeString(heartbeatGolden)
	f.Add(uint32(opcode.MSGMoveHeartbeat), golden)
	f.Add(uint32(opcode.SMSGUpdateObject), createFixture(3, vitalFields()))
	f.Add(uint32(opcode.SMSGCompressedUpdateObject), []byte{4, 0, 0, 0})
	spline, _ := hex.DecodeString(monsterLinearGolden)
	f.Add(uint32(opcode.SMSGMonsterMove), spline)
	stop, _ := hex.DecodeString(monsterStopGolden)
	f.Add(uint32(opcode.SMSGMonsterMove), stop)
	npc, _ := hex.DecodeString(npcGolden)
	f.Add(uint32(opcode.SMSGUpdateObject), npc)
	mailbox, _ := hex.DecodeString(mailboxGolden)
	f.Add(uint32(opcode.SMSGUpdateObject), mailbox)
	f.Add(uint32(opcode.SMSGUpdateObject), deltaFixture(1, map[int]uint32{playerCoinage: 10000}))
	f.Add(uint32(opcode.SMSGUpdateObject), deltaFixture(1, map[int]uint32{0x9e: 783, 0x172: 42, 0x173: 0x40000000}))
	f.Add(uint32(opcode.SMSGUpdateObject), objectFixture(0x4000000000000042, 1, map[int]uint32{3: 159, 6: 1, 0xe: 2}))
	f.Fuzz(func(t *testing.T, op uint32, payload []byte) {
		s := NewService(1)
		if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: op, Payload: payload}); err != nil {
			t.Fatal("packet callback must never fail", err)
		}
		r, _ := s.Snapshot(context.Background(), nil)
		if r.Self.Guid != 1 || r.Owner.Guid != 3 {
			t.Fatal("tracked identity changed")
		}
		if len(r.Mailboxes) > 32 || len(s.state.gameObjects) > maxGameObjects {
			t.Fatal("mailbox observation bounds exceeded")
		}
	})
}
