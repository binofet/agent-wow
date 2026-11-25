package buddyworld

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const cloudGUID uint64 = 0xf10000000000002a

// A public TYPEID_DYNAMICOBJECT CREATE carries its own stationary world pose.
// The caster does not need to have a known position.
func groundCreate(guid uint64, kind uint32, x float32) []byte {
	b := new(bytes.Buffer)
	u32(b, 1)
	b.WriteByte(3)
	b.Write(packedFixture(guid))
	b.WriteByte(6)
	_ = binary.Write(b, binary.LittleEndian, uint16(0x40))
	for _, v := range []float32{x, -262.5, -60.25, 0} {
		f32(b, v)
	}
	valuesFixture(b, map[int]uint32{
		6: 0x19000002, 7: 0xf1300000, 8: kind, 9: 21070,
		10: math.Float32bits(5), 11: 0xfffffff0,
	})
	return b.Bytes()
}

func TestGroundEffectsUseObjectPoseAndIndependentLifetimes(t *testing.T) {
	s := NewService(1)
	now := time.UnixMilli(100_000)
	s.now = func() time.Time { return now }
	before, _ := s.Snapshot(context.Background(), nil)
	if before.GroundEffectsSupported {
		t.Fatal("unbound visit advertised coverage")
	}
	r := packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	if !r.GroundEffectsSupported || len(r.GroundEffects) != 0 {
		t.Fatal("expected bound empty observation", r)
	}
	r = packet(t, s, opcode.SMSGCompressedUpdateObject, compressed(t, groundCreate(cloudGUID, 1, 811.25)))
	if r.MalformedPackets != 0 || len(r.GroundEffects) != 1 {
		t.Fatal("cloud CREATE not decoded", r)
	}
	e := r.GroundEffects[0]
	if e.Guid != cloudGUID || e.CasterGuid != 0xf130000019000002 || e.SpellId != 21070 ||
		e.RadiusYards != 5 || e.X != 811.25 || e.Y != -262.5 || e.Z != -60.25 ||
		e.FirstObservedAtUnixMs != 100_000 || e.GetServerCastTimeMs() != 0xfffffff0 || e.PositionRevision != 1 {
		t.Fatal("wrong ground-effect identity, geometry, or time source", e)
	}
	now = now.Add(6 * time.Second)
	r = packet(t, s, opcode.SMSGUpdateObject, groundCreate(cloudGUID+1, 1, 816.75))
	if len(r.GroundEffects) != 2 || r.GroundEffects[0].X != 811.25 ||
		r.GroundEffects[1].X != 816.75 || r.GroundEffects[1].FirstObservedAtUnixMs != 106_000 {
		t.Fatal("overlapping objects lost their individual poses or observation times", r)
	}
	now = now.Add(time.Second)
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(cloudGUID, map[int]uint32{10: math.Float32bits(4.5)}))
	if r.GroundEffects[0].FirstObservedAtUnixMs != 100_000 || r.GroundEffects[0].RadiusYards != 4.5 {
		t.Fatal("value update renewed observation or lost new radius", r.GroundEffects[0])
	}
	r = packet(t, s, opcode.SMSGUpdateObject, groundCreate(cloudGUID, 1, 812))
	if r.GroundEffects[0].PositionRevision != 2 || r.GroundEffects[0].FirstObservedAtUnixMs != 100_000 || r.GroundEffects[0].X != 812 {
		t.Fatal("repeated visible CREATE renewed object lifetime or missed pose", r.GroundEffects[0])
	}
	b := new(bytes.Buffer)
	_ = binary.Write(b, binary.LittleEndian, cloudGUID)
	b.WriteByte(0)
	r = packet(t, s, opcode.SMSGDestroyObject, b.Bytes())
	if len(r.GroundEffects) != 1 || r.GroundEffects[0].Guid != cloudGUID+1 {
		t.Fatal("destroyed object still reported or other patch lost", r)
	}
	b.Reset()
	u32(b, 1)
	b.WriteByte(4)
	u32(b, 1)
	b.Write(packedFixture(cloudGUID + 1))
	r = packet(t, s, opcode.SMSGUpdateObject, b.Bytes())
	if len(r.GroundEffects) != 0 {
		t.Fatal("out-of-range object retained", r)
	}
	now = now.Add(time.Second)
	r = packet(t, s, opcode.SMSGUpdateObject, groundCreate(cloudGUID+1, 1, 816))
	if r.GroundEffects[0].FirstObservedAtUnixMs != 108_000 {
		t.Fatal("new visibility inherited an old lifetime", r)
	}
	r = packet(t, s, opcode.SMSGNewWorld, login())
	if len(r.GroundEffects) != 0 || r.GroundEffectsOverflow {
		t.Fatal("same-map world transfer retained a previous visit", r)
	}
}

func TestGroundEffectsIgnoreOtherTypesAndRejectPartialGeometry(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, groundCreate(cloudGUID+1, 2, 812))
	packet(t, s, opcode.SMSGUpdateObject, groundCreate(0xf110000000000032, 1, 813))
	r := packet(t, s, opcode.SMSGUpdateObject, groundCreate(cloudGUID, 1, 811))
	if len(r.GroundEffects) != 1 || r.GroundEffects[0].Guid != cloudGUID {
		t.Fatal("non-area dynamic object or wrong GUID leaked into observations", r)
	}
	b := deltaFixture(cloudGUID, map[int]uint32{10: math.Float32bits(10)})
	binary.LittleEndian.PutUint32(b, 2)
	b = append(b, 0xff)
	r = packet(t, s, opcode.SMSGUpdateObject, b)
	if r.MalformedPackets != 1 || len(r.GroundEffects) != 1 || r.GroundEffects[0].RadiusYards != 5 {
		t.Fatal("incomplete packet changed geometry", r)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(cloudGUID, map[int]uint32{10: 0x7fc00000}))
	if len(r.GroundEffects) != 0 {
		t.Fatal("non-finite radius reported", r)
	}
}

func TestGroundEffectsReportIncompleteCoverageUntilVisitChanges(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	for i := 0; i <= maxGameObjects; i++ {
		packet(t, s, opcode.SMSGUpdateObject, groundCreate(cloudGUID+uint64(i), 1, float32(i)))
	}
	r, _ := s.Snapshot(context.Background(), nil)
	if !r.GroundEffectsOverflow || len(r.GroundEffects) != maxGameObjects || len(s.state.groundEffects) != maxGameObjects {
		t.Fatal("population exceeded the cache or hid incomplete coverage")
	}
	r = packet(t, s, opcode.SMSGTransferPending, []byte{1, 0, 0, 0})
	if len(r.GroundEffects) != 0 || r.GroundEffectsOverflow || len(s.state.groundEffects) != 0 {
		t.Fatal("transfer retained geometry or loss of coverage")
	}
}

// Literal layout from pinned b8c DynamicObject.cpp:31 and
// Object.cpp:376-446: LOWGUID|STATIONARY_POSITION|POSITION = 0x150.
// POSITION takes precedence over the stationary-only four-float layout.
func groundPinnedCreate(guid uint64, x float32) []byte {
	b := new(bytes.Buffer)
	u32(b, 1)
	b.WriteByte(3)
	b.Write(packedFixture(guid))
	b.WriteByte(6)
	_ = binary.Write(b, binary.LittleEndian, uint16(0x150))
	b.WriteByte(0)
	for _, v := range []float32{x, 2, 3, x, 2, 3, .5, 0} {
		f32(b, v)
	}
	u32(b, uint32(guid))
	// The public CREATE baseline omits the caster GUID's zero high word.
	valuesFixture(b, map[int]uint32{6: 3, 8: 1, 9: 21070, 10: math.Float32bits(5)})
	return b.Bytes()
}

func TestGroundEffectsPinnedNativeFlagsAndSparsePublicWords(t *testing.T) {
	s := NewService(1)
	now := time.UnixMilli(500000)
	s.now = func() time.Time { return now }
	r := packet(t, s, opcode.SMSGUpdateObject, groundPinnedCreate(cloudGUID, 7))
	if r.GroundEffectsSupported || len(r.GroundEffects) != 0 {
		t.Fatal("unknown map produced usable effect", r)
	}
	loginMap := login()
	binary.LittleEndian.PutUint32(loginMap, 349)
	packet(t, s, opcode.SMSGLoginVerifyWorld, loginMap)
	r = packet(t, s, opcode.SMSGCompressedUpdateObject, compressed(t, groundPinnedCreate(cloudGUID, 7)))
	if r.MalformedPackets != 0 || !r.GroundEffectsSupported || len(r.GroundEffects) != 1 {
		t.Fatal("actual native flags failed", r)
	}
	e := r.GroundEffects[0]
	if e.CasterGuid != 3 || e.X != 7 || e.Y != 2 || e.Z != 3 || e.RadiusYards != 5 || e.FirstObservedAtUnixMs != 500000 {
		t.Fatal("sparse native fields or position misread", e)
	}
	now = now.Add(time.Second)
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(cloudGUID, map[int]uint32{6: 4}))
	if len(r.GroundEffects) != 1 || r.GroundEffects[0].CasterGuid != 4 || r.GroundEffects[0].X != 7 || r.GroundEffects[0].FirstObservedAtUnixMs != 500000 {
		t.Fatal("sparse value delta reset map/geometry/time", r)
	}
	// A complete new CREATE followed by an invalid block must roll back the map.
	malformed := groundPinnedCreate(cloudGUID+1, 9)
	binary.LittleEndian.PutUint32(malformed, 2)
	malformed = append(malformed, 0xff)
	r = packet(t, s, opcode.SMSGUpdateObject, malformed)
	if r.MalformedPackets != 1 || len(r.GroundEffects) != 1 || r.GroundEffects[0].Guid != cloudGUID {
		t.Fatal("new effect escaped atomic rollback", r)
	}
}
