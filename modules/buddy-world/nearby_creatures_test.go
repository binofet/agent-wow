package buddyworld

import (
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestNearbyCreaturesBoundedLevelAndMalformedDeltaRollback(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	for i := 0; i < 50; i++ {
		fields := npcFields(0)
		fields[3] = 299
		fields[0x36] = 1
		guid := npcGUID + uint64(i)
		packet(t, s, opcode.SMSGUpdateObject, objectFixture(guid, 3, fields))
	}
	r := packet(t, s, opcode.SMSGUpdateObject, deltaFixture(npcGUID, map[int]uint32{0x36: 2}))
	if len(r.NearbyCreatures) != 32 || !r.NearbyCreatures[0].LevelKnown || r.NearbyCreatures[0].Level != 2 || r.NearbyCreatures[0].Guid != npcGUID {
		t.Fatal(r)
	}
	bad := deltaFixture(npcGUID, map[int]uint32{0x36: 1})
	r = packet(t, s, opcode.SMSGUpdateObject, bad[:len(bad)-1])
	if r.NearbyCreatures[0].Level != 2 || r.MalformedPackets != 1 {
		t.Fatal("partial level update committed", r)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(npcGUID, map[int]uint32{0x36: 0}))
	if r.NearbyCreatures[0].LevelKnown {
		t.Fatal("unknown level became eligible", r.NearbyCreatures[0])
	}
}
