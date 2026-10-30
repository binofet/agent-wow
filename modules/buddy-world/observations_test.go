package buddyworld

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const npcGolden = "0100000003d92a230130f10320000000000000007b0000000000803f00000040000040400000000000000000000020400000e0400000904066669640000020400000e04000009040c3f54840c3f548400308000001010000000000040023010000640000006400000002000000"
const npcGUID uint64 = 0xf13000012300002a

func packedFixture(guid uint64) []byte {
	b := []byte{0}
	for i := uint(0); i < 8; i++ {
		if v := byte(guid >> (i * 8)); v != 0 {
			b[0] |= 1 << i
			b = append(b, v)
		}
	}
	return b
}
func objectFixture(guid uint64, typ byte, fields map[int]uint32) []byte {
	b := new(bytes.Buffer)
	u32(b, 1)
	b.WriteByte(3)
	b.Write(packedFixture(guid))
	b.WriteByte(typ)
	if typ == 1 || typ == 2 {
		b.Write([]byte{0, 0})
		valuesFixture(b, fields)
		return b.Bytes()
	}
	base := createFixture(3, fields)
	b.Write(base[8:])
	return b.Bytes()
}
func deltaFixture(guid uint64, fields map[int]uint32) []byte {
	b := new(bytes.Buffer)
	u32(b, 1)
	b.WriteByte(0)
	b.Write(packedFixture(guid))
	valuesFixture(b, fields)
	return b.Bytes()
}
func npcFields(flags uint32) map[int]uint32 {
	return map[int]uint32{3: 291, 0x18: 100, 0x20: 100, 0x52: flags}
}

func TestQuestgiverLiteralGoldenCreateAndCompressed(t *testing.T) {
	golden, err := hex.DecodeString(npcGolden)
	if err != nil {
		t.Fatal(err)
	}
	for _, zipped := range []bool{false, true} {
		s := NewService(1)
		packet(t, s, opcode.SMSGLoginVerifyWorld, login())
		op, data := uint32(opcode.SMSGUpdateObject), golden
		if zipped {
			op, data = opcode.SMSGCompressedUpdateObject, compressed(t, golden)
		}
		r := packet(t, s, op, data)
		if r.MalformedPackets != 0 || len(r.QuestGivers) != 1 {
			t.Fatalf("create: %v", r)
		}
		p := r.QuestGivers[0]
		if p.Guid != npcGUID || p.Entry != 291 || p.NpcFlags != 2 || !p.Present || !p.MapKnown || p.MapId != 0 || !p.PositionKnown || p.X != 1 || p.Y != 2 || p.Z != 3 || !p.HealthKnown || !p.AliveKnown || !p.Alive || !p.DistanceToSelfObservedKnown {
			t.Fatal(p)
		}
	}
}

func TestNPCDeltaDeathLootStopOutOfRangeAndTransfer(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(npcGUID, 3, npcFields(2)))
	r := packet(t, s, opcode.SMSGUpdateObject, deltaFixture(npcGUID, map[int]uint32{0x18: 0, 0x4f: 1}))
	if len(r.LootableCorpses) != 1 || r.LootableCorpses[0].Alive || !r.LootableCorpses[0].AliveKnown {
		t.Fatal(r)
	}
	// Primary PacketBuilder::WriteStopMovement: packedGUID,0,XYZ,splineID,type1.
	b := new(bytes.Buffer)
	b.Write(packedFixture(npcGUID))
	b.WriteByte(0)
	for _, v := range []float32{4, 5, 6} {
		f32(b, v)
	}
	u32(b, 123)
	b.WriteByte(1)
	r = packet(t, s, opcode.SMSGMonsterMove, b.Bytes())
	if len(r.LootableCorpses) != 1 || r.LootableCorpses[0].X != 4 || !r.LootableCorpses[0].PositionKnown {
		t.Fatal(r)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(npcGUID, map[int]uint32{0x52: 0, 0x4f: 0}))
	if len(r.QuestGivers) != 0 || len(r.LootableCorpses) != 0 {
		t.Fatal(r)
	}
	packet(t, s, opcode.SMSGUpdateObject, deltaFixture(npcGUID, map[int]uint32{0x52: 2}))
	b.Reset()
	u32(b, 1)
	b.WriteByte(4)
	u32(b, 1)
	b.Write(packedFixture(npcGUID))
	r = packet(t, s, opcode.SMSGUpdateObject, b.Bytes())
	if len(r.QuestGivers) != 0 {
		t.Fatal(r)
	}
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(npcGUID, 3, npcFields(2)))
	r = packet(t, s, opcode.SMSGNewWorld, login())
	if len(r.QuestGivers) != 0 || len(s.state.creatures) != 0 {
		t.Fatal(r)
	}
}

func TestQuestgiverNPCMovementAndDestroy(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(npcGUID, 3, npcFields(2)))
	m, _ := hex.DecodeString(heartbeatGolden)
	m = append(packedFixture(npcGUID), m[2:]...)
	r := packet(t, s, opcode.MSGMoveHeartbeat, m)
	if r.QuestGivers[0].PositionRevision != 2 || r.QuestGivers[0].MovementFlags != 1 {
		t.Fatal(r)
	}
	b := new(bytes.Buffer)
	_ = binary.Write(b, binary.LittleEndian, npcGUID)
	b.WriteByte(0)
	r = packet(t, s, opcode.SMSGDestroyObject, b.Bytes())
	if len(r.QuestGivers) != 0 || len(s.state.creatures) != 0 {
		t.Fatal(r)
	}
}

func TestNPCAndPlayerChangesRollbackTogether(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(npcGUID, 3, npcFields(2)))
	first := deltaFixture(npcGUID, map[int]uint32{0x18: 0, 0x4f: 1})
	second := deltaFixture(1, map[int]uint32{0x9e: 783})
	b := append(append([]byte{}, first...), second[4:]...)
	binary.LittleEndian.PutUint32(b, 3)
	b = append(b, 255)
	r := packet(t, s, opcode.SMSGUpdateObject, b)
	if r.MalformedPackets != 1 || r.QuestGivers[0].Health != 100 || len(r.LootableCorpses) != 0 || r.SelfQuestLog[0].QuestId != 0 {
		t.Fatal(r)
	}
	// Every prefix of a valid NPC CREATE is rejected without filling the cache.
	for length := 0; length < len(first); length++ {
		packet(t, s, opcode.SMSGUpdateObject, first[:length])
	}
	r, _ = s.Snapshot(context.Background(), nil)
	if r.QuestGivers[0].Health != 100 {
		t.Fatal(r)
	}
}

func TestQuestLogOwnCreateZeroBaselineAndStrideFive(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	r, _ := s.Snapshot(context.Background(), nil)
	if r.SelfQuestLogKnown || len(r.SelfQuestLog) != 0 {
		t.Fatal(r)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	if !r.SelfQuestLogKnown || len(r.SelfQuestLog) != 25 {
		t.Fatal(r)
	}
	rev := r.SelfQuestLogRevision
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x9e: 783, 0x9f: 2, 0xa0: 123, 0x9e + 24*5: 42}))
	if r.SelfQuestLog[0].QuestId != 783 || r.SelfQuestLog[1].QuestId != 0 || r.SelfQuestLog[24].QuestId != 42 || r.SelfQuestLogRevision != rev+4 {
		t.Fatal(r)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(3, map[int]uint32{0x9e: 999}))
	if r.SelfQuestLog[0].QuestId != 783 || r.SelfQuestLogRevision != rev+4 {
		t.Fatal("owner log changed self log", r)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x9e: 0}))
	if r.SelfQuestLog[0].QuestId != 0 || r.SelfQuestLogRevision != rev+5 {
		t.Fatal(r)
	}
	// A malformed own CREATE must not establish a new empty baseline.
	bad := createFixture(1, vitalFields())
	r = packet(t, s, opcode.SMSGUpdateObject, bad[:len(bad)-1])
	if !r.SelfQuestLogKnown || r.SelfQuestLog[24].QuestId != 42 || r.SelfQuestLogRevision != rev+5 {
		t.Fatal(r)
	}
}

func TestBoundedNPCPopulationAndNearestResults(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	for i := 0; i < 180; i++ {
		guid := uint64(0xf130000123000000) | uint64(i+1)
		b := objectFixture(guid, 3, npcFields(2))
		packed := packedFixture(guid)
		// MovementInfo x starts10bytes after the uint16 movement-update flags.
		offset := 4 + 1 + len(packed) + 1 + 2 + 10
		binary.LittleEndian.PutUint32(b[offset:], mathFloatBits(float32(i)))
		packet(t, s, opcode.SMSGUpdateObject, b)
	}
	r, _ := s.Snapshot(context.Background(), nil)
	if len(s.state.creatures) != 128 || len(r.QuestGivers) != 64 {
		t.Fatalf("cache%d results%d", len(s.state.creatures), len(r.QuestGivers))
	}
	for i := 1; i < len(r.QuestGivers); i++ {
		if r.QuestGivers[i-1].DistanceToSelfObserved > r.QuestGivers[i].DistanceToSelfObserved {
			t.Fatal("not nearest ordered")
		}
	}
	if r.QuestGivers[0].X != 1 {
		t.Fatal("nearest candidate was evicted", r.QuestGivers[0])
	}
}

func mathFloatBits(v float32) uint32 {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, v)
	return binary.LittleEndian.Uint32(b.Bytes())
}

func TestBackpackSlotRefsOwnedMetadataAndAtomicUpdates(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	r := packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	if !r.BackpackKnown || len(r.Backpack) != 16 || r.Backpack[0].ItemGuid != 0 {
		t.Fatal(r)
	}
	item := uint64(0x4000000000000042)
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x172: uint32(item), 0x173: uint32(item >> 32)}))
	if r.Backpack[0].MetadataKnown || r.Backpack[0].ItemGuid != item {
		t.Fatal(r)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, objectFixture(item, 1, map[int]uint32{3: 159, 6: 1, 0xe: 3}))
	if !r.Backpack[0].MetadataKnown || r.Backpack[0].Entry != 159 || r.Backpack[0].StackCount != 3 {
		t.Fatal(r)
	}
	rev := r.BackpackRevision
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(item, map[int]uint32{0xe: 2}))
	if r.Backpack[0].StackCount != 2 || r.BackpackRevision <= rev {
		t.Fatal(r)
	}
	bad := deltaFixture(item, map[int]uint32{0xe: 1})
	binary.LittleEndian.PutUint32(bad, 2)
	bad = append(bad, 255)
	r = packet(t, s, opcode.SMSGUpdateObject, bad)
	if r.Backpack[0].StackCount != 2 {
		t.Fatal("partial item mutation", r)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(item, map[int]uint32{6: 3}))
	if r.Backpack[0].MetadataKnown {
		t.Fatal("foreign ownership accepted", r)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x172: 0, 0x173: 0}))
	if r.Backpack[0].ItemGuid != 0 {
		t.Fatal(r)
	}
}

func TestBoundedItemsAndConcurrentMixedWorldSnapshots(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	for i := uint64(1); i < 150; i++ {
		packet(t, s, opcode.SMSGUpdateObject, objectFixture(0x4000000000000000|i, 1, map[int]uint32{3: 159, 6: 1, 0xe: 1}))
	}
	if len(s.state.items) != 128 {
		t.Fatal(len(s.state.items))
	}
	s.now = time.Now
	var wg sync.WaitGroup
	for worker := 0; worker < 6; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGUpdateObject, Payload: objectFixture(npcGUID, 3, npcFields(2))})
				s.Snapshot(context.Background(), nil)
			}
		}()
	}
	wg.Wait()
	if len(s.state.creatures) > 128 || len(s.state.items) > 128 {
		t.Fatal("unbounded cache")
	}
}

func TestOwnerTargetAndObservedAttackVictim(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(3, vitalFields()))
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(npcGUID, 3, npcFields(0)))
	r := packet(t, s, opcode.SMSGUpdateObject, deltaFixture(3, map[int]uint32{0x12: uint32(npcGUID & 0xffffffff), 0x13: uint32(npcGUID >> 32)}))
	if !r.Owner.TargetKnown || r.Owner.TargetGuid != npcGUID || r.OwnerTarget == nil || r.OwnerTarget.Guid != npcGUID {
		t.Fatal(r)
	}
	b := new(bytes.Buffer)
	_ = binary.Write(b, binary.LittleEndian, npcGUID)
	_ = binary.Write(b, binary.LittleEndian, uint64(3))
	r = packet(t, s, opcode.SMSGAttackstart, b.Bytes())
	if !r.OwnerTarget.AttackVictimKnown || r.OwnerTarget.AttackVictimGuid != 3 || r.OwnerTarget.TargetGuid != 0 {
		t.Fatal(r)
	}
	packet(t, s, opcode.SMSGAttackstart, b.Bytes()[:15])
	r, _ = s.Snapshot(context.Background(), nil)
	if r.OwnerTarget.AttackVictimGuid != 3 || r.MalformedPackets != 1 {
		t.Fatal(r)
	}
	stop := append(packedFixture(npcGUID), packedFixture(3)...)
	stop = append(stop, 0, 0, 0, 0)
	r = packet(t, s, opcode.SMSGAttackstop, stop)
	if r.OwnerTarget.AttackVictimKnown || r.OwnerTarget.AttackVictimGuid != 0 {
		t.Fatal(r)
	}
	packet(t, s, opcode.SMSGAttackstart, b.Bytes())
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(npcGUID, map[int]uint32{0x18: 0}))
	if r.OwnerTarget.AttackVictimKnown {
		t.Fatal("death kept attack evidence", r)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(3, map[int]uint32{0x12: 0, 0x13: 0}))
	if r.OwnerTarget != nil || r.Owner.TargetGuid != 0 {
		t.Fatal(r)
	}
}

func TestActiveSplineHeaderCannotCommitPartialPosition(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(npcGUID, 3, npcFields(2)))
	b := new(bytes.Buffer)
	b.Write(packedFixture(npcGUID))
	b.WriteByte(0)
	for _, v := range []float32{50, 60, 70} {
		f32(b, v)
	}
	u32(b, 1)
	b.WriteByte(0)
	r := packet(t, s, opcode.SMSGMonsterMove, b.Bytes())
	if !r.QuestGivers[0].PositionKnown || r.QuestGivers[0].PositionRevision != 1 || r.QuestGivers[0].X != 1 || r.MalformedPackets != 1 {
		t.Fatal(r)
	}
}
