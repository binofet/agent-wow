package buddyworld

import (
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestQuestProgressStateFourPackedCountsExpiryAndAtomicRollback(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	r := packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	base := r.SelfQuestLogRevision
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x9e: 783, 0x9f: 1, 0xa0: 0xffff0001, 0xa1: 0x80000002, 0xa2: 123456}))
	q := r.SelfQuestLog[0]
	if q.QuestId != 783 || q.State != 1 || !q.Complete || q.Failed || q.ExpireTime != 123456 || len(q.ObjectiveCounts) != 4 || q.ObjectiveCounts[0] != 1 || q.ObjectiveCounts[1] != 65535 || q.ObjectiveCounts[2] != 2 || q.ObjectiveCounts[3] != 32768 || r.SelfQuestLogRevision != base+5 {
		t.Fatal(q, r.SelfQuestLogRevision)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x9f: 2, 0xa0: 0xffff0002}))
	if q := r.SelfQuestLog[0]; q.QuestId != 783 || q.Complete || !q.Failed || q.ObjectiveCounts[0] != 2 || r.SelfQuestLogRevision != base+7 {
		t.Fatal(q, r.SelfQuestLogRevision)
	}
	// Repeated values do not invent progress; malformed deltas roll back both
	// the individual counters and the revision used by quest confirmation.
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x9f: 2, 0xa0: 0xffff0002}))
	bad := deltaFixture(1, map[int]uint32{0xa0: 100, 0xa2: 0})
	r = packet(t, s, opcode.SMSGUpdateObject, bad[:len(bad)-1])
	if r.SelfQuestLogRevision != base+7 || r.SelfQuestLog[0].ObjectiveCounts[0] != 2 || r.SelfQuestLog[0].ExpireTime != 123456 {
		t.Fatal(r)
	}
}

func TestActualClassBaseManaMeleeReachAndOwnedMainhandObservations(t *testing.T) {
	s := NewService(5)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	fields := vitalFields()
	fields[unitBytes0] = 1 | 4<<8 | 3<<24
	fields[0x36], fields[0x78], fields[0x42], fields[0x3e] = 1, 0, mathFloatBits(1.5), 2000
	item := uint64(0x4000000000000042)
	fields[0x162], fields[0x163] = uint32(item), uint32(item>>32)
	r := packet(t, s, opcode.SMSGUpdateObject, createFixture(5, fields))
	if !r.Self.ClassKnown || r.Self.ClassId != 4 || !r.Self.LevelKnown || r.Self.Level != 1 || r.Self.PowerType != 3 || r.Self.BaseManaKnown || !r.Self.CombatReachKnown || r.Self.CombatReach != 1.5 || !r.Self.BaseAttackTimeKnown || r.Self.BaseAttackTimeMs != 2000 || !r.Self.Mainhand.SlotKnown || r.Self.Mainhand.ItemGuid != item || r.Self.Mainhand.MetadataKnown {
		t.Fatal(r.Self)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, objectFixture(item, 1, map[int]uint32{3: 2092, 6: 5, 0xe: 1, 0x3c: 10, 0x3d: 10}))
	if !r.Self.Mainhand.MetadataKnown || r.Self.Mainhand.Entry != 2092 || !r.Self.Mainhand.DurabilityKnown || r.Self.Mainhand.Durability != 10 {
		t.Fatal(r.Self.Mainhand)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(item, map[int]uint32{0x3c: 0}))
	if !r.Self.Mainhand.DurabilityKnown || r.Self.Mainhand.Durability != 0 {
		t.Fatal("broken weapon did not remain explicit", r.Self.Mainhand)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(item, map[int]uint32{6: 3}))
	if r.Self.Mainhand.MetadataKnown {
		t.Fatal("foreign-owned mainhand metadata trusted", r.Self.Mainhand)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(5, map[int]uint32{unitBytes0: 1 | 8<<8, 0x78: 100, 0x162: 0, 0x163: 0}))
	if r.Self.ClassId != 8 || !r.Self.BaseManaKnown || r.Self.BaseMana != 100 || r.Self.Mainhand.ItemGuid != 0 || r.Self.Mainhand.MetadataKnown {
		t.Fatal(r.Self)
	}
}
