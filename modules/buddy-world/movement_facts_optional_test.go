package buddyworld

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func optionalMovementWire(t *testing.T) []byte {
	t.Helper()
	// Independent literal MovementInfo, without a mover GUID: all conditional
	// tails, distinct transport coordinates/times, signed seat and negative zero.
	b, err := hex.DecodeString("00122004000444332211" +
		"0000a03f000020c00000704000000080" +
		"810201000080400000a0400000c0400000e040" +
		"88776655ffffffffff0000003fddccbbaa" +
		"00000041000080be0000803f0000104100002041")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func optionalMovementRig(t *testing.T) *Service {
	t.Helper()
	s := NewService(1)
	s.now = func() time.Time { return time.Unix(4242, 0) }
	r := packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	if r.VisitRevision == 0 || !r.Self.MapKnown || !r.Self.Present {
		t.Fatal("unbound optional-movement fixture", r)
	}
	return s
}

func optionalHeartbeat(wire []byte) []byte {
	return append([]byte{1, 1}, wire...) // packed own GUID1, then MovementInfo
}

func optionalCreateWithSpline(wire []byte) []byte {
	b := new(bytes.Buffer)
	u32(b, 1)
	b.Write([]byte{3, 1, 1, 4, 0x20, 0}) // own PLAYER CREATE, living movement
	b.Write(wire)
	for _, speed := range []float32{2.5, 7, 4.5, 4.7, 2.5, 7, 4.5, 3.14, 3.14} {
		f32(b, speed)
	}
	u32(b, 0) // spline flags; no conditional facing field
	u32(b, 25)
	u32(b, 1000)
	u32(b, 0xabcdef01) // opaque ID, outside MovementInfo
	for n := 0; n < 3; n++ {
		f32(b, 0)
	}
	u32(b, 0)
	u32(b, 2)
	for _, value := range []float32{1, 2, 3, 8, 9, 10} {
		f32(b, value)
	}
	b.WriteByte(0)
	for _, value := range []float32{8, 9, 10} {
		f32(b, value)
	}
	valuesFixture(b, vitalFields())
	return b.Bytes()
}

func optionalAssertFullMovement(t *testing.T, r *worldv1.SnapshotResponse, wire []byte, source uint32) {
	t.Helper()
	m := r.SelfMovementFacts
	if m == nil || !m.Known || m.SelfGuid != 1 || m.VisitRevision != r.VisitRevision ||
		m.VisitRevision == 0 || m.Revision == 0 || m.SourceOpcode != source || m.AgeMs != 0 ||
		!m.MapKnown || m.MapId != 0 || !bytes.Equal(m.WireImage, wire) {
		t.Fatalf("full own MovementInfo not bound/reflected exactly: %v; wire %x", m, wire)
	}
	i := m.Info
	if i == nil || i.Flags != binary.LittleEndian.Uint32(wire) || i.ExtraFlags != 0x400 || i.Time != 0x11223344 ||
		i.X != 1.25 || i.Y != -2.5 || i.Z != 3.75 || math.Float32bits(i.Orientation) != 0x80000000 ||
		!i.PitchKnown || i.Pitch != 0.5 || i.FallTime != 0xaabbccdd || !i.SplineElevationKnown || i.SplineElevation != 10 {
		t.Fatal("MovementInfo scalar/presence projection shifted", i)
	}
	tr := i.Transport
	if tr == nil || tr.Guid != 0x0100000000000002 || tr.X != 4 || tr.Y != 5 || tr.Z != 6 || tr.Orientation != 7 ||
		tr.Time != 0x55667788 || tr.Seat != -1 || !tr.Time2Known || tr.Time2 != math.MaxUint32 {
		t.Fatal("transport tail projection shifted", tr)
	}
	j := i.Jump
	if j == nil || j.Velocity != 8 || j.SinAngle != -0.25 || j.CosAngle != 1 || j.XySpeed != 9 {
		t.Fatal("falling tail projection shifted", j)
	}
}

func TestOwnMovementOptionalFullReflectionAndExactWirePrefix(t *testing.T) {
	for _, kind := range []string{"heartbeat", "create", "compressed_create"} {
		t.Run(kind, func(t *testing.T) {
			s := optionalMovementRig(t)
			wire := optionalMovementWire(t)
			op := uint32(opcode.MSGMoveHeartbeat)
			body := optionalHeartbeat(wire)
			if kind != "heartbeat" {
				binary.LittleEndian.PutUint32(wire, binary.LittleEndian.Uint32(wire)|0x08000000)
				op, body = opcode.SMSGUpdateObject, optionalCreateWithSpline(wire)
				if kind == "compressed_create" {
					op, body = opcode.SMSGCompressedUpdateObject, compressed(t, body)
				}
			}
			r := packet(t, s, op, body)
			if r.MalformedPackets != 0 {
				t.Fatal("optional wire fixture rejected", r.LastParseError)
			}
			optionalAssertFullMovement(t, r, wire, op)
			if len(r.SelfMovementFacts.WireImage) != 82 {
				t.Fatal("mover GUID, speeds or CREATE spline leaked into MovementInfo prefix", r.SelfMovementFacts)
			}
		})
	}
}

func TestOwnMovementOptionalPresentZerosRemainKnownInProtoJSON(t *testing.T) {
	s := optionalMovementRig(t)
	b := new(bytes.Buffer)
	u32(b, 0x04201200)
	b.Write([]byte{0, 4})
	u32(b, 0)
	b.Write(make([]byte, 16)) // own XYZO
	b.WriteByte(0)            // packed transport GUID0 is present
	b.Write(make([]byte, 16+4+1+4+4+4+16+4))
	wire := b.Bytes()
	r := packet(t, s, opcode.MSGMoveHeartbeat, optionalHeartbeat(wire))
	m := r.SelfMovementFacts
	if r.MalformedPackets != 0 || m == nil || !m.Known || !bytes.Equal(m.WireImage, wire) || m.Info == nil ||
		m.Info.Transport == nil || m.Info.Transport.Guid != 0 || m.Info.Transport.Time != 0 || m.Info.Transport.Seat != 0 ||
		!m.Info.Transport.Time2Known || m.Info.Transport.Time2 != 0 || !m.Info.PitchKnown || m.Info.Pitch != 0 ||
		m.Info.Jump == nil || !m.Info.SplineElevationKnown || m.Info.SplineElevation != 0 {
		t.Fatal("wire-present zeros lost knownness", m)
	}
	encoded, err := protojson.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var projected map[string]any
	if err := json.Unmarshal(encoded, &projected); err != nil {
		t.Fatal(err)
	}
	info, ok := projected["info"].(map[string]any)
	if !ok {
		t.Fatalf("missing zero-valued info message: %s", encoded)
	}
	transport, ok := info["transport"].(map[string]any)
	if !ok || transport["time2Known"] != true || info["pitchKnown"] != true || info["splineElevationKnown"] != true {
		t.Fatalf("zero-valued tail presence not reflected: %s", encoded)
	}
	if _, ok := info["jump"].(map[string]any); !ok {
		t.Fatalf("zero-valued falling tuple not reflected: %s", encoded)
	}
}

func TestOwnMovementOptionalNonfiniteIsSoftUnknownWithoutRejectingLegacyPose(t *testing.T) {
	fields := []struct {
		name string
		off  int
	}{{"transport_x", 29}, {"transport_y", 33}, {"transport_z", 37}, {"transport_orientation", 41},
		{"pitch", 54}, {"jump_velocity", 62}, {"jump_sine", 66}, {"jump_cosine", 70}, {"jump_speed", 74}, {"elevation", 78}}
	for _, field := range fields {
		for _, bits := range []uint32{0x7fc12345, 0x7f800000, 0xff800000} {
			t.Run(field.name+"/"+hex.EncodeToString(binary.LittleEndian.AppendUint32(nil, bits)), func(t *testing.T) {
				s := optionalMovementRig(t)
				wire := optionalMovementWire(t)
				before := packet(t, s, opcode.MSGMoveHeartbeat, optionalHeartbeat(wire))
				binary.LittleEndian.PutUint32(wire[field.off:], bits)
				r := packet(t, s, opcode.MSGMoveHeartbeat, optionalHeartbeat(wire))
				m := r.SelfMovementFacts
				if r.MalformedPackets != before.MalformedPackets || !r.Self.PositionKnown || r.Self.PositionInferred ||
					r.Self.PositionRevision != before.Self.PositionRevision+1 || r.Self.X != 1.25 || r.Self.Y != -2.5 || r.Self.Z != 3.75 ||
					!r.Self.MovementFlagsKnown || r.Self.MovementFlags != 0x04201200 || r.Self.MovementTime != 0x11223344 {
					t.Fatal("optional nonfinite value changed legacy pose acceptance", r)
				}
				if m == nil || m.Known || m.Info != nil || len(m.WireImage) != 0 || m.Revision != before.SelfMovementFacts.Revision+1 ||
					m.SelfGuid != 1 || m.VisitRevision != r.VisitRevision || !m.MapKnown || m.SourceOpcode != opcode.MSGMoveHeartbeat {
					t.Fatal("optional nonfinite value retained reusable old/new bytes", m)
				}
			})
		}
	}
	// The new optional-float qualification is finite-only, not a new coordinate
	// policy. Legacy World never imposed its base-coordinate bound on these tails.
	s := optionalMovementRig(t)
	wire := optionalMovementWire(t)
	binary.LittleEndian.PutUint32(wire[29:], math.Float32bits(math.MaxFloat32))
	r := packet(t, s, opcode.MSGMoveHeartbeat, optionalHeartbeat(wire))
	if r.MalformedPackets != 0 || !r.SelfMovementFacts.Known || r.SelfMovementFacts.Info.Transport.X != math.MaxFloat32 {
		t.Fatal("finite optional tail acquired a new coordinate bound", r)
	}
}

func TestOwnMovementOptionalPreservesBaseBoundsAndPassiveFlags(t *testing.T) {
	for _, offset := range []int{10, 14, 18, 22} {
		for _, value := range []float32{1e6, -1e6, float32(math.NaN()), float32(math.Inf(1))} {
			s := optionalMovementRig(t)
			wire := optionalMovementWire(t)
			before := packet(t, s, opcode.MSGMoveHeartbeat, optionalHeartbeat(wire))
			binary.LittleEndian.PutUint32(wire[offset:], math.Float32bits(value))
			r := packet(t, s, opcode.MSGMoveHeartbeat, optionalHeartbeat(wire))
			if r.MalformedPackets != before.MalformedPackets+1 || !proto.Equal(r.Self, before.Self) ||
				!proto.Equal(r.SelfMovementFacts, before.SelfMovementFacts) {
				t.Fatalf("base bound changed at offset%d value%v: %v", offset, value, r)
			}
		}
	}
	s := optionalMovementRig(t)
	b := new(bytes.Buffer)
	u32(b, 0x70000000)
	b.Write([]byte{0, 0})
	u32(b, 0)
	for _, value := range []float32{999999, -999999, 12345, 999999} {
		f32(b, value)
	}
	u32(b, math.MaxUint32)
	r := packet(t, s, opcode.MSGMoveHeartbeat, optionalHeartbeat(b.Bytes()))
	m := r.SelfMovementFacts
	if r.MalformedPackets != 0 || !r.Self.PositionKnown || r.Self.MovementFlags != 0x70000000 ||
		m == nil || !m.Known || !bytes.Equal(m.WireImage, b.Bytes()) || m.Info.Flags != 0x70000000 ||
		m.Info.Transport != nil || m.Info.Jump != nil || m.Info.PitchKnown || m.Info.SplineElevationKnown || m.Info.FallTime != math.MaxUint32 {
		t.Fatal("legacy finite base range or passive high flags changed", r)
	}
}

func TestOwnMovementOptionalTruncationAndTrailingDataCannotCommitFacts(t *testing.T) {
	s := optionalMovementRig(t)
	wire := optionalMovementWire(t)
	baseline := packet(t, s, opcode.MSGMoveHeartbeat, optionalHeartbeat(wire))
	bad := make([][]byte, 0, len(wire)+3)
	heartbeat := optionalHeartbeat(wire)
	for end := 0; end < len(heartbeat); end++ {
		bad = append(bad, heartbeat[:end])
	}
	bad = append(bad, append(append([]byte(nil), heartbeat...), 0x99))
	for n, body := range bad {
		r := packet(t, s, opcode.MSGMoveHeartbeat, body)
		if r.MalformedPackets != baseline.MalformedPackets+uint64(n)+1 || !proto.Equal(r.Self, baseline.Self) ||
			!proto.Equal(r.SelfMovementFacts, baseline.SelfMovementFacts) {
			t.Fatalf("optional framing failure%d committed partial facts: %v", n, r)
		}
	}

	binary.LittleEndian.PutUint32(wire, binary.LittleEndian.Uint32(wire)|0x08000000)
	create := optionalCreateWithSpline(wire)
	for end := 10; end < 10+len(wire); end++ {
		beforeMalformed := s.malformed
		r := packet(t, s, opcode.SMSGUpdateObject, create[:end])
		if r.MalformedPackets != beforeMalformed+1 || !proto.Equal(r.Self, baseline.Self) ||
			!proto.Equal(r.SelfMovementFacts, baseline.SelfMovementFacts) {
			t.Fatalf("CREATE optional prefix%d committed partial facts: %v", end-10, r)
		}
	}
}
