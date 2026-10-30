package buddyworld

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func independentMovement(t *testing.T, guid uint64, x float32) []byte {
	t.Helper()
	b, err := hex.DecodeString(heartbeatGolden)
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(b[12:], mathFloatBits(x))
	return append(packedFixture(guid), b[2:]...)
}

func independentCreature(guid uint64, x float32) []byte {
	b := objectFixture(guid, 3, npcFields(2))
	// Object header, uint16 movement flags, then the public MovementInfo XYZO.
	offset := 4 + 1 + len(packedFixture(guid)) + 1 + 2 + 10
	binary.LittleEndian.PutUint32(b[offset:], mathFloatBits(x))
	return b
}

func TestIndependentWorldDoesNotObserveCompanionOwner(t *testing.T) {
	s := NewIndependentService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	packet(t, s, opcode.SMSGUpdateObject, independentCreature(npcGUID, 2))
	fields := vitalFields()
	fields[0x12], fields[0x13] = uint32(npcGUID&0xffffffff), uint32(npcGUID>>32)
	packet(t, s, opcode.SMSGUpdateObject, createFixture(3, fields))
	packet(t, s, opcode.MSGMoveHeartbeat, independentMovement(t, 3, 1000))
	r := packet(t, s, opcode.SMSGUpdateObject, deltaFixture(3, map[int]uint32{unitHealth: 1}))
	if r.Owner != nil || r.OwnerTarget != nil || s.state.players[1] != (observed{}) {
		t.Fatalf("independent mode retained an owner: %v", r)
	}
	if r.Self.Guid != 1 || r.Self.Health != 50 || r.Self.X != 1 || len(r.QuestGivers) != 1 {
		t.Fatalf("unrelated owner observations changed self: %v", r)
	}
	creature := r.QuestGivers[0]
	if !creature.DistanceToSelfObservedKnown || creature.DistanceToSelfObserved != 1 || creature.DistanceToOwnerObservedKnown || creature.DistanceToOwnerObserved != 0 {
		t.Fatalf("independent creature distance: %v", creature)
	}
	if r.MalformedPackets != 0 {
		t.Fatal(r.LastParseError)
	}
}

func TestIndependentWorldAllowsGUID3AsSelf(t *testing.T) {
	s := NewIndependentService(3)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(3, vitalFields()))
	packet(t, s, opcode.SMSGUpdateObject, independentCreature(npcGUID, 2))
	packet(t, s, opcode.MSGMoveHeartbeat, independentMovement(t, 3, 4))
	r := packet(t, s, opcode.SMSGUpdateObject, deltaFixture(3, map[int]uint32{
		unitHealth: 40, questLogFirst: 783, playerCoinage: 25,
		0x12: uint32(npcGUID & 0xffffffff), 0x13: uint32(npcGUID >> 32),
	}))
	if r.Owner != nil || r.OwnerTarget != nil || r.Self.Guid != 3 || !r.Self.Present || r.Self.X != 4 || r.Self.Health != 40 {
		t.Fatalf("independent GUID3 self state: %v", r)
	}
	if !r.SelfQuestLogKnown || r.SelfQuestLog[0].QuestId != 783 || !r.BackpackKnown || !r.SelfCopperKnown || r.SelfCopper != 25 {
		t.Fatalf("independent GUID3 own observations missing: %v", r)
	}
	if r.MalformedPackets != 0 {
		t.Fatal(r.LastParseError)
	}
}

func TestIndependentWorldZeroGUIDCannotMutateDisabledOwner(t *testing.T) {
	s := NewIndependentService(1)
	if player(&s.state.players, 0) != nil {
		t.Fatal("zero GUID resolved to the disabled owner")
	}
	attack := new(bytes.Buffer)
	_ = binary.Write(attack, binary.LittleEndian, uint64(0))
	_ = binary.Write(attack, binary.LittleEndian, npcGUID)
	power := append(packedFixture(0), 0)
	power = append(power, 10, 0, 0, 0)
	for _, fixture := range []struct {
		name string
		op   uint32
		data []byte
	}{
		{"login", opcode.SMSGLoginVerifyWorld, login()},
		{"create", opcode.SMSGUpdateObject, objectFixture(0, 4, vitalFields())},
		{"values", opcode.SMSGUpdateObject, deltaFixture(0, map[int]uint32{unitHealth: 10})},
		{"movement", opcode.MSGMoveHeartbeat, independentMovement(t, 0, 4)},
		{"power", opcode.SMSGPowerUpdate, power},
		{"attack", opcode.SMSGAttackstart, attack.Bytes()},
		{"root", opcode.SMSGForceMoveRoot, []byte{0, 0, 0, 0, 0}},
		{"destroy", opcode.SMSGDestroyObject, make([]byte, 9)},
		{"transfer", opcode.SMSGTransferPending, []byte{0, 0, 0, 0}},
		{"new world", opcode.SMSGNewWorld, login()},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			r := packet(t, s, fixture.op, fixture.data)
			if r.MalformedPackets != 0 || r.Owner != nil || r.OwnerTarget != nil || s.state.players[1] != (observed{}) {
				t.Fatalf("zero GUID mutated disabled owner: %v", r)
			}
		})
	}
}

func TestIndependentWorldRetainsAndRanksCreaturesNearSelf(t *testing.T) {
	s := NewIndependentService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	// GUID3 is visible but cannot move the retention or ranking reference.
	packet(t, s, opcode.MSGMoveHeartbeat, independentMovement(t, 3, 1000))
	// Insert distant creatures first to exercise replacement at capacity.
	for i := 179; i >= 0; i-- {
		packet(t, s, opcode.SMSGUpdateObject, independentCreature(npcGUID+uint64(i), float32(i+1)))
	}
	r, err := s.Snapshot(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.state.creatures) != maxCreatures || len(r.QuestGivers) != maxCreatureResults || len(r.NearbyCreatures) != 32 {
		t.Fatalf("independent population bounds: %v", r)
	}
	for i := 0; i < 180; i++ {
		_, retained := s.state.creatures[npcGUID+uint64(i)]
		if retained != (i < maxCreatures) {
			t.Fatalf("creature at self distance %d retained=%v", i, retained)
		}
	}
	for i, p := range r.QuestGivers {
		if p.Guid != npcGUID+uint64(i) || !p.DistanceToSelfObservedKnown || p.DistanceToSelfObserved != float32(i) || p.DistanceToOwnerObservedKnown {
			t.Fatalf("independent questgiver rank %d: %v", i, p)
		}
	}
	if r.Owner != nil || r.OwnerTarget != nil || r.MalformedPackets != 0 {
		t.Fatal(r)
	}
}

func TestDefaultWorldRetainsCompanionOwnerRanking(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	packet(t, s, opcode.SMSGUpdateObject, createFixture(3, vitalFields()))
	packet(t, s, opcode.MSGMoveHeartbeat, independentMovement(t, 3, 1000))
	packet(t, s, opcode.SMSGUpdateObject, independentCreature(npcGUID, 2))
	packet(t, s, opcode.SMSGUpdateObject, independentCreature(npcGUID+1, 999))
	r := packet(t, s, opcode.SMSGUpdateObject, deltaFixture(3, map[int]uint32{
		0x12: uint32((npcGUID + 1) & 0xffffffff), 0x13: uint32((npcGUID + 1) >> 32),
	}))
	if r.Owner == nil || r.Owner.Guid != OwnerGUID || !r.Owner.Present || r.OwnerTarget == nil || r.OwnerTarget.Guid != npcGUID+1 {
		t.Fatalf("companion owner behavior changed: %v", r)
	}
	if len(r.QuestGivers) != 2 || r.QuestGivers[0].Guid != npcGUID+1 || !r.QuestGivers[0].DistanceToOwnerObservedKnown || r.QuestGivers[0].DistanceToOwnerObserved != 1 {
		t.Fatalf("companion owner ranking changed: %v", r)
	}
}
