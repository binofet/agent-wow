package buddyworld

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"math"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// PacketBuilder::WriteMonsterMove/WriteLinearPath, not a production encoder:
// NPC GUID; start10,20,30; ID0x12345678; normal; flags0; duration1000;
// two path nodes; destination14,24,34 FIRST; one compressed intermediate node.
const monsterLinearGolden = "d92a230130f100000020410000a0410000f041785634120000000000e803000002000000000060410000c0410000084202108000"

// WriteStopMovement has no active-spline tail: actual stop11,22,33, ID, type1.
const monsterStopGolden = "d92a230130f100000030410000b041000004427956341201"

func splineRig(t *testing.T) *Service {
	t.Helper()
	s := NewService(1)
	s.now = func() time.Time { return time.Unix(1234, 0) }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(npcGUID, 3, npcFields(2)))
	return s
}

func splineLiteral(t *testing.T, value string) []byte {
	t.Helper()
	data, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestMonsterMoveLiteralStartThenExactCorpseStop(t *testing.T) {
	s := splineRig(t)
	r := packet(t, s, opcode.SMSGMonsterMove, splineLiteral(t, monsterLinearGolden))
	if r.MalformedPackets != 0 || len(r.QuestGivers) != 1 {
		t.Fatal(r)
	}
	p := r.QuestGivers[0]
	if !p.PositionKnown || p.X != 10 || p.Y != 20 || p.Z != 30 || p.PositionAgeMs != 0 || p.MovementFlags&0x08000000 == 0 {
		t.Fatal("active movement must expose the observed start, not its future endpoint", p)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(npcGUID, map[int]uint32{unitHealth: 0, unitDynamicFlags: 1}))
	if len(r.LootableCorpses) != 1 {
		t.Fatal(r)
	}
	r = packet(t, s, opcode.SMSGMonsterMove, splineLiteral(t, monsterStopGolden))
	p = r.LootableCorpses[0]
	if r.MalformedPackets != 0 || !p.PositionKnown || p.X != 11 || p.Y != 22 || p.Z != 33 || p.MovementFlags&0x08000000 != 0 {
		t.Fatal("STOP must replace the start with actual stopped corpse coordinates", p)
	}
}

// Source-framed variants exercise optional fields before the path. Facing
// coordinates are not movement destinations, and animation precedes duration.
func monsterSplineFixture(kind byte, flags uint32) []byte {
	b := new(bytes.Buffer)
	b.Write(packedFixture(npcGUID))
	b.WriteByte(0)
	for _, v := range []float32{10, 20, 30} {
		f32(b, v)
	}
	u32(b, 123)
	b.WriteByte(kind)
	switch kind {
	case 2:
		for _, v := range []float32{100, 200, 300} {
			f32(b, v)
		}
	case 3:
		_ = binary.Write(b, binary.LittleEndian, uint64(3))
	case 4:
		f32(b, 1.5)
	}
	u32(b, flags)
	if flags&0x200000 != 0 {
		b.WriteByte(7)
		u32(b, 250)
	}
	u32(b, 1000)
	if flags&0x800 != 0 {
		f32(b, 9.8)
		u32(b, 250)
	}
	if flags&(0x40000|0x2000) != 0 {
		u32(b, 2)
		for _, v := range []float32{12, 22, 32, 14, 24, 34} {
			f32(b, v)
		}
	} else {
		u32(b, 1)
		for _, v := range []float32{14, 24, 34} {
			f32(b, v)
		}
	}
	return b.Bytes()
}

func TestMonsterMoveFacingAnimationParabolicAndSmoothFraming(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  byte
		flags uint32
	}{
		{"normal", 0, 0}, {"face point", 2, 0}, {"face target", 3, 0}, {"face angle", 4, 0},
		{"animation", 0, 0x200000}, {"parabolic", 0, 0x800},
		{"catmull", 0, 0x40000}, {"flying", 0, 0x2000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := splineRig(t)
			r := packet(t, s, opcode.SMSGMonsterMove, monsterSplineFixture(tc.kind, tc.flags))
			if r.MalformedPackets != 0 || !r.QuestGivers[0].PositionKnown || r.QuestGivers[0].X != 10 {
				t.Fatal(r)
			}
		})
	}
}

func TestMonsterMoveTruncationAndTrailingBytesRollBackWholeObservation(t *testing.T) {
	for _, full := range [][]byte{
		splineLiteral(t, monsterLinearGolden), splineLiteral(t, monsterStopGolden),
		monsterSplineFixture(2, 0x200000), monsterSplineFixture(3, 0x800), monsterSplineFixture(4, 0x40000),
	} {
		for n := 0; n <= len(full); n++ {
			data := full[:n]
			if n == len(full) {
				data = append(append([]byte{}, full...), 0)
			}
			s := splineRig(t)
			r := packet(t, s, opcode.SMSGMonsterMove, data)
			p := r.QuestGivers[0]
			if r.MalformedPackets != 1 || !p.PositionKnown || p.X != 1 || p.Y != 2 || p.Z != 3 || p.MovementFlags&0x08000000 != 0 {
				t.Fatalf("packet length%d/%d partially committed or accepted: %v", n, len(full), r)
			}
		}
	}
}

func TestMonsterMoveRejectsInvalidCoordinatesAndCounts(t *testing.T) {
	for _, change := range []func([]byte){
		func(p []byte) { binary.LittleEndian.PutUint32(p[7:], math.Float32bits(float32(math.NaN()))) },
		func(p []byte) { binary.LittleEndian.PutUint32(p[36:], math.Float32bits(float32(math.Inf(1)))) },
		func(p []byte) { binary.LittleEndian.PutUint32(p[32:], 0) },
		func(p []byte) { binary.LittleEndian.PutUint32(p[32:], ^uint32(0)) },
	} {
		s := splineRig(t)
		data := splineLiteral(t, monsterLinearGolden)
		change(data)
		r := packet(t, s, opcode.SMSGMonsterMove, data)
		if r.MalformedPackets != 1 || r.QuestGivers[0].X != 1 {
			t.Fatal(r)
		}
	}
}

func TestCyclicMonsterMoveHasNoFiniteDestination(t *testing.T) {
	s := splineRig(t)
	data := monsterSplineFixture(0, 0x40000|0x80000)
	// Ground cyclic CatmullRom: two real points followed by the fake zero point.
	data = append([]byte{}, data[:32]...)
	data = binary.LittleEndian.AppendUint32(data, 3)
	for _, v := range []float32{12, 22, 32, 14, 24, 34, 0, 0, 0} {
		data = binary.LittleEndian.AppendUint32(data, math.Float32bits(v))
	}
	r := packet(t, s, opcode.SMSGMonsterMove, data)
	p := r.QuestGivers[0]
	if r.MalformedPackets != 0 || !p.PositionKnown || p.X != 10 || p.Trajectory == nil || !p.Trajectory.Known || !p.Trajectory.Cyclic || p.Trajectory.DestinationKnown {
		t.Fatal("cyclic fake zero must not become a world destination", p)
	}
}

func TestTransportMonsterMoveCoordinatesStayRelative(t *testing.T) {
	s := splineRig(t)
	const transportGUID = uint64(0xf120000056000011)
	plain := splineLiteral(t, monsterLinearGolden)
	moverLength := len(packedFixture(npcGUID))
	data := append([]byte{}, plain[:moverLength]...)
	data = append(data, packedFixture(transportGUID)...)
	data = append(data, 2) // int8 transport seat.
	data = append(data, plain[moverLength:]...)
	r := packet(t, s, opcode.SMSGMonsterMoveTransport, data)
	p := r.QuestGivers[0]
	if r.MalformedPackets != 0 || p.PositionKnown || p.Trajectory == nil || !p.Trajectory.Known || !p.Trajectory.TransportRelative || p.Trajectory.TransportGuid != transportGUID || p.Trajectory.TransportSeat != 2 {
		t.Fatal("relative transport coordinates were exposed as world XYZ", p)
	}
}

func TestSplineEndpointAgeDoesNotInventFreshObservedArrival(t *testing.T) {
	s := splineRig(t)
	now := time.Unix(1234, 0)
	s.now = func() time.Time { return now }
	r := packet(t, s, opcode.SMSGMonsterMove, splineLiteral(t, monsterLinearGolden))
	p := r.QuestGivers[0]
	if p.Trajectory == nil || !p.Trajectory.Known || !p.Trajectory.DestinationKnown || p.Trajectory.DestinationX != 14 || p.Trajectory.DestinationY != 24 || p.Trajectory.DestinationZ != 34 || p.Trajectory.DurationMs != 1000 || p.Trajectory.AgeMs != 0 {
		t.Fatal("missing advertised endpoint or duration", p)
	}
	before := p.PositionRevision
	now = now.Add(2 * time.Second)
	r, err := s.Snapshot(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	p = r.QuestGivers[0]
	if p.Trajectory.AgeMs != 2000 || p.X != 10 || p.Y != 20 || p.Z != 30 || p.PositionAgeMs != 2000 || p.PositionRevision != before {
		t.Fatal("elapsed trajectory promoted its endpoint into a fresh server observation", p)
	}
}

func TestUnsupportedSplineModesNeverAdvertiseArrival(t *testing.T) {
	for _, flags := range []uint32{0x400, 0x400000, 0x80000000} {
		s := splineRig(t)
		r := packet(t, s, opcode.SMSGMonsterMove, monsterSplineFixture(0, flags))
		p := r.QuestGivers[0]
		if r.MalformedPackets != 0 || !p.PositionKnown || p.X != 10 || p.Trajectory == nil || p.Trajectory.DestinationKnown {
			t.Fatalf("unsupported flags%x advertised endpoint arrival: %v", flags, r)
		}
	}
	for _, flags := range []uint32{1, 0x100, 0x8000, 0x200 | 0x800, 0x200000 | 0x200} {
		s := splineRig(t)
		r := packet(t, s, opcode.SMSGMonsterMove, monsterSplineFixture(0, flags))
		if r.MalformedPackets != 1 || r.QuestGivers[0].X != 1 {
			t.Fatalf("producer-masked or contradictory flags%x accepted: %v", flags, r)
		}
	}
}
