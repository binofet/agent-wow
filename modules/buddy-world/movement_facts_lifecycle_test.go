package buddyworld

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func ownMovementRig(t *testing.T) (*Service, func(time.Duration)) {
	t.Helper()
	s := NewService(1)
	now := time.Unix(2000, 0)
	s.now = func() time.Time { return now }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	r := packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	if !r.SelfMovementFacts.Known || r.SelfMovementFacts.Info == nil {
		t.Fatal("fixture did not retain actual own CREATE movement", r)
	}
	return s, func(elapsed time.Duration) { now = now.Add(elapsed) }
}

// Literal field order follows the qualified complete MovementInfo wire layout,
// independent of the production encoder. The packed transport GUID contains a
// redundant zero byte; an exact received image must preserve that byte too.
func ownMovementOptionalWire(seat int8) ([]byte, map[string]int) {
	b := new(bytes.Buffer)
	offsets := map[string]int{}
	u32(b, 0x06201200) // Transport, flying/swimming pitch, fall jump, spline elevation.
	_ = binary.Write(b, binary.LittleEndian, uint16(0x420))
	u32(b, 123)
	put := func(name string, value float32) { offsets[name] = b.Len(); f32(b, value) }
	put("x", 1)
	put("y", 2)
	put("z", 3)
	put("orientation", .5)
	b.Write([]byte{3, 42, 0}) // Actual transport GUID42 with an explicit zero byte.
	put("transport_x", 4)
	put("transport_y", 5)
	put("transport_z", 6)
	put("transport_orientation", .75)
	u32(b, 7)
	b.WriteByte(byte(seat))
	u32(b, 8)
	put("pitch", .125)
	u32(b, 9)
	put("jump_velocity", 10)
	put("jump_sin", .25)
	put("jump_cos", .75)
	put("jump_xy", 11)
	put("spline_elevation", 12)
	return b.Bytes(), offsets
}

func ownMovementMessage(guid uint64, info []byte) []byte {
	return append(packedFixture(guid), info...)
}

func ownSplineWithID(t *testing.T, literal string, id uint32) []byte {
	t.Helper()
	b := selfSplinePacket(t, literal)
	// packed own GUID2 + toggle1 + XYZ12 precede the opaque spline ID.
	binary.LittleEndian.PutUint32(b[15:], id)
	return b
}

func ownMovementSnapshot(t *testing.T, s *Service) *worldv1.SnapshotResponse {
	t.Helper()
	r, err := s.Snapshot(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestOwnMovementFactsKeepEveryOptionalTailAndDetachedExactWire(t *testing.T) {
	s, _ := ownMovementRig(t)
	wire, _ := ownMovementOptionalWire(-3)
	message := ownMovementMessage(1, wire)
	r := packet(t, s, opcode.MSGMoveHeartbeat, message)
	m := r.SelfMovementFacts
	i := m.GetInfo()
	if r.MalformedPackets != 0 || !m.Known || m.SelfGuid != 1 || m.VisitRevision != 1 || !m.MapKnown || m.MapId != 0 ||
		m.SourceOpcode != opcode.MSGMoveHeartbeat || m.AgeMs != 0 || i == nil || i.Flags != 0x06201200 || i.ExtraFlags != 0x420 ||
		i.Time != 123 || i.X != 1 || i.Y != 2 || i.Z != 3 || i.Orientation != .5 || !bytes.Equal(m.WireImage, wire) {
		t.Fatal("complete own MovementInfo lost its body/visit/base/wire facts", r)
	}
	tr := i.Transport
	if tr == nil || tr.Guid != 42 || tr.X != 4 || tr.Y != 5 || tr.Z != 6 || tr.Orientation != .75 || tr.Time != 7 ||
		tr.Seat != -3 || !tr.Time2Known || tr.Time2 != 8 || !i.PitchKnown || i.Pitch != .125 || i.FallTime != 9 ||
		i.Jump == nil || i.Jump.Velocity != 10 || i.Jump.SinAngle != .25 || i.Jump.CosAngle != .75 || i.Jump.XySpeed != 11 ||
		!i.SplineElevationKnown || i.SplineElevation != 12 {
		t.Fatal("optional-tail facts were skipped or reordered", i)
	}
	decoded, _, err := movementinfo.DecodePrefix(wire)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := movementinfo.Encode(decoded)
	if err != nil || bytes.Equal(canonical, m.WireImage) {
		t.Fatal("fixture did not distinguish received bytes from canonical encoding", canonical, m.WireImage, err)
	}
	before := proto.Clone(m).(*worldv1.OwnMovementFacts)
	message[2] ^= 0xff
	wire[0] ^= 0xff
	m.WireImage[0] ^= 0xff
	m.Info.Transport.X = 999
	if after := ownMovementSnapshot(t, s).SelfMovementFacts; !proto.Equal(before, after) {
		t.Fatal("packet or snapshot aliases changed retained own facts", before, after)
	}
	copy := s.state.clone()
	copy.ownMovement.wire[0] ^= 0xff
	if !bytes.Equal(s.state.ownMovement.wire, before.WireImage) {
		t.Fatal("packet clone shared mutable retained wire bytes")
	}
}

func TestOwnMovementFactsRejectMalformedWholePacketsAtomically(t *testing.T) {
	s, _ := ownMovementRig(t)
	wire, _ := ownMovementOptionalWire(-1)
	message := ownMovementMessage(1, wire)
	before := packet(t, s, opcode.MSGMoveHeartbeat, message).SelfMovementFacts
	for n := 0; n < len(message); n++ {
		r := packet(t, s, opcode.MSGMoveHeartbeat, message[:n])
		if !proto.Equal(before, r.SelfMovementFacts) {
			t.Fatal("truncated own MovementInfo partially committed facts", n, before, r.SelfMovementFacts)
		}
	}
	r := packet(t, s, opcode.MSGMoveHeartbeat, append(bytes.Clone(message), 0))
	if !proto.Equal(before, r.SelfMovementFacts) {
		t.Fatal("trailing MovementInfo byte partially committed own facts", r)
	}
	// A valid own CREATE block precedes a malformed second block. Its newly
	// received bytes and changed pose must disappear with the whole packet.
	update := createFixture(1, vitalFields())
	binary.LittleEndian.PutUint32(update, 2)
	update = append(update, 1, 1, 3)
	oldMalformed := r.MalformedPackets
	r = packet(t, s, opcode.SMSGUpdateObject, update)
	if r.MalformedPackets != oldMalformed+1 || !proto.Equal(before, r.SelfMovementFacts) {
		t.Fatal("later malformed block leaked earlier own movement facts", r)
	}
}

func TestOwnMovementFactsDoNotRefreshOnForeignVitalsControlOrClientEstimate(t *testing.T) {
	s, advance := ownMovementRig(t)
	before := ownMovementSnapshot(t, s)
	advance(2 * time.Second)
	packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitHealth: 40}))
	foreign, err := hex.DecodeString(heartbeatGolden)
	if err != nil {
		t.Fatal(err)
	}
	packet(t, s, opcode.MSGMoveHeartbeat, foreign)
	packet(t, s, opcode.SMSGClientControlUpdate, []byte{1, 1, 1})
	current := ownMovementSnapshot(t, s)
	publishDiscovery(t, s, &worldv1.ClientMovementRequest{SelfGuid: 1, MapId: current.Self.MapId,
		ServerPositionRevision: current.Self.PositionRevision, Sequence: 1, Valid: true, X: 4, Y: 2, Z: 3})
	current = ownMovementSnapshot(t, s)
	if current.DiscoveryAnchor == nil || !current.DiscoveryAnchor.Estimated {
		t.Fatal("fixture did not exercise the actual client estimate boundary", current)
	}
	before.SelfMovementFacts.AgeMs = 2000
	before.SelfSplineFacts.AgeMs = 2000
	if !proto.Equal(before.SelfMovementFacts, current.SelfMovementFacts) || !proto.Equal(before.SelfSplineFacts, current.SelfSplineFacts) {
		t.Fatal("nonmovement observation refreshed raw movement revision, source or age", before, current)
	}
}

func TestOwnMovementFactsClearAtActualWorldAndBodyBoundaries(t *testing.T) {
	for _, name := range []string{"same_map_login", "same_map_new_world", "transfer", "destroy", "out_of_range", "create_during_transfer"} {
		t.Run(name, func(t *testing.T) {
			s, _ := ownMovementRig(t)
			packet(t, s, opcode.SMSGMonsterMove, ownSplineWithID(t, monsterLinearGolden, 0))
			switch name {
			case "same_map_login":
				packet(t, s, opcode.SMSGLoginVerifyWorld, login())
			case "same_map_new_world":
				packet(t, s, opcode.SMSGNewWorld, login())
			case "transfer", "create_during_transfer":
				packet(t, s, opcode.SMSGTransferPending, []byte{0, 0, 0, 0})
				if name == "create_during_transfer" {
					packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
				}
			case "destroy":
				packet(t, s, opcode.SMSGDestroyObject, append(binary.LittleEndian.AppendUint64(nil, 1), 0))
			case "out_of_range":
				b := binary.LittleEndian.AppendUint32(nil, 1)
				b = append(b, 4)
				b = binary.LittleEndian.AppendUint32(b, 1)
				packet(t, s, opcode.SMSGUpdateObject, append(b, packedFixture(1)...))
			}
			r := ownMovementSnapshot(t, s)
			if r.MalformedPackets != 0 || r.SelfMovementFacts.Known || r.SelfMovementFacts.Info != nil || len(r.SelfMovementFacts.WireImage) != 0 || r.SelfSplineFacts.Known || r.SelfSplineFacts.IdKnown ||
				r.SelfMovementFacts.VisitRevision != r.VisitRevision || r.SelfSplineFacts.VisitRevision != r.VisitRevision {
				t.Fatal("world/body boundary retained or rebound old own raw facts", name, r)
			}
		})
	}
	t.Run("create_before_verification", func(t *testing.T) {
		s := NewService(1)
		r := packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
		if r.VisitRevision != 0 || r.SelfMovementFacts.Known || r.SelfMovementFacts.Info != nil || len(r.SelfMovementFacts.WireImage) != 0 {
			t.Fatal("unverified own CREATE fabricated bound movement facts", r)
		}
	})
}

func TestOwnSplineIDsRemainOpaqueIncludingZeroAndActualStop(t *testing.T) {
	s, _ := ownMovementRig(t)
	for _, id := range []uint32{0, ^uint32(0), 0} {
		before := ownMovementSnapshot(t, s).SelfSplineFacts.Revision
		r := packet(t, s, opcode.SMSGMonsterMove, ownSplineWithID(t, monsterLinearGolden, id))
		f := r.SelfSplineFacts
		if !f.Known || !f.IdKnown || f.SplineId != id || f.Revision != before+1 || f.SourceOpcode != opcode.SMSGMonsterMove || f.SelfGuid != 1 || f.VisitRevision != 1 || f.Stopped {
			t.Fatal("opaque spline ID was treated as missing, ordered or deduplicated", id, f)
		}
	}
	r := packet(t, s, opcode.SMSGMonsterMove, ownSplineWithID(t, monsterStopGolden, 0))
	if !r.SelfSplineFacts.Known || !r.SelfSplineFacts.IdKnown || r.SelfSplineFacts.SplineId != 0 || !r.SelfSplineFacts.Stopped || !r.SelfSplineFacts.DestinationKnown ||
		r.SelfSplineFacts.DestinationX != 11 || r.SelfSplineFacts.DestinationY != 22 || r.SelfSplineFacts.DestinationZ != 33 || r.Self.PositionInferred {
		t.Fatal("actual own STOP lost its observed ID/pose or became inferred", r)
	}
}

func TestOwnSplineSettlementMarksOnlyLegacyPoseAndNeverSynthesizesRawFlags(t *testing.T) {
	s, advance := ownMovementRig(t)
	base, err := hex.DecodeString(heartbeatGolden)
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(base[2:], 0x08000001)
	packet(t, s, opcode.MSGMoveHeartbeat, ownMovementMessage(1, base[2:]))
	before := packet(t, s, opcode.SMSGMonsterMove, ownSplineWithID(t, monsterLinearGolden, 0))
	packet(t, s, opcode.SMSGClientControlUpdate, []byte{1, 1, 1})
	advance(time.Second)
	after := ownMovementSnapshot(t, s)
	if !after.Self.PositionKnown || !after.Self.PositionInferred || after.Self.X != 14 || after.Self.Y != 24 || after.Self.Z != 34 || after.Self.MovementFlags&splineMotionFlags != 0 {
		t.Fatal("existing own spline settlement changed or lacks inferred provenance", after)
	}
	before.SelfMovementFacts.AgeMs, before.SelfSplineFacts.AgeMs = 1000, 1000
	if !proto.Equal(before.SelfMovementFacts, after.SelfMovementFacts) || !proto.Equal(before.SelfSplineFacts, after.SelfSplineFacts) || after.SelfMovementFacts.Info.Flags != 0x08000001 {
		t.Fatal("elapsed spline or control return invented a raw endpoint/flags/receipt", before, after)
	}
	base, _ = hex.DecodeString(heartbeatGolden)
	r := packet(t, s, opcode.MSGMoveHeartbeat, ownMovementMessage(1, base[2:]))
	if r.Self.PositionInferred || !r.SelfMovementFacts.Known || r.SelfMovementFacts.SourceOpcode != opcode.MSGMoveHeartbeat || r.SelfMovementFacts.Revision != after.SelfMovementFacts.Revision+1 {
		t.Fatal("new actual movement did not replace inferred legacy position", r)
	}
}

func TestOwnCreateSplineRetainsActualTransportIdentityAndZeroSplineID(t *testing.T) {
	s, _ := ownMovementRig(t)
	wire, _ := ownMovementOptionalWire(-1)
	binary.LittleEndian.PutUint32(wire, binary.LittleEndian.Uint32(wire)|0x08000000)
	b := new(bytes.Buffer)
	u32(b, 1)
	b.WriteByte(3)
	b.Write(packedFixture(1))
	b.WriteByte(4)
	_ = binary.Write(b, binary.LittleEndian, uint16(0x20))
	b.Write(wire)
	for _, speed := range []float32{2.5, 7, 4.5, 4.7, 2.5, 7, 4.5, 3.14, 3.14} {
		f32(b, speed)
	}
	u32(b, 0) // Spline flags.
	u32(b, 0) // Elapsed time.
	u32(b, 1000)
	u32(b, 0) // Present opaque spline ID0.
	for _, value := range []float32{1, 1, 0} {
		f32(b, value)
	}
	u32(b, 0)
	u32(b, 1)
	for _, value := range []float32{10, 20, 30} {
		f32(b, value)
	}
	b.WriteByte(0)
	for _, value := range []float32{11, 22, 33} {
		f32(b, value)
	}
	valuesFixture(b, vitalFields())
	r := packet(t, s, opcode.SMSGUpdateObject, b.Bytes())
	f := r.SelfSplineFacts
	if r.MalformedPackets != 0 || !r.SelfMovementFacts.Known || !bytes.Equal(r.SelfMovementFacts.WireImage, wire) || !f.Known || !f.IdKnown || f.SplineId != 0 ||
		!f.TransportRelative || f.TransportGuid != 42 || f.TransportSeat != -1 || f.SourceOpcode != opcode.SMSGUpdateObject || f.VisitRevision != 1 {
		t.Fatal("own CREATE spline invented transport identity or dropped ID0", r)
	}
}
