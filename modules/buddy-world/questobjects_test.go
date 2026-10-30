package buddyworld

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const harvestGUID uint64 = 0xf11002771500002a

func TestHarvestChestExactEntryTypeFlagsAndAtomicSnapshot(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	b := gameObjectFixture(harvestGUID, map[int]uint32{objectEntry: 161557, gameObjectBytes: 3<<8 | 1, gameObjectFlags: 4, gameObjectDynamic: 0x12340009})
	r := packet(t, s, opcode.SMSGCompressedUpdateObject, compressed(t, b))
	if len(r.QuestObjects) != 1 || len(r.Mailboxes) != 0 || r.MalformedPackets != 0 {
		t.Fatal(r)
	}
	p := r.QuestObjects[0]
	if p.Guid != harvestGUID || p.Entry != 161557 || p.GameObjectType != 3 || !p.TypeKnown || !p.Present || !p.MapKnown || !p.PositionKnown || p.X != 1 || p.Y != 2 || p.Z != 3 || !p.FlagsKnown || p.Flags != 4 || !p.DynamicFlagsKnown || p.DynamicFlags != 0x12340009 {
		t.Fatal(p)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(harvestGUID, map[int]uint32{gameObjectFlags: 0, gameObjectDynamic: 1}))
	if r.QuestObjects[0].Flags != 0 || r.QuestObjects[0].DynamicFlags != 1 {
		t.Fatal(r)
	}
	r.QuestObjects[0].Entry = 0
	r, _ = s.Snapshot(context.Background(), nil)
	if r.QuestObjects[0].Entry != 161557 {
		t.Fatal("sharedmutablesnapshot")
	}
	for _, fields := range []map[int]uint32{{objectEntry: 161558}, {objectEntry: 161557, gameObjectBytes: 19 << 8}, {objectEntry: 161557, gameObjectBytes: 3}} {
		r = packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(harvestGUID, fields))
		if len(r.QuestObjects) != 0 {
			t.Fatal("acceptedwrongentry/typebyte", r)
		}
	}
}
func TestHarvestChestBaselineLifecycleAndMalformedRollback(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	b := gameObjectFixture(harvestGUID, map[int]uint32{objectEntry: 161557, gameObjectBytes: 3 << 8})
	for n := 0; n < len(b); n++ {
		packet(t, s, opcode.SMSGUpdateObject, b[:n])
	}
	r, _ := s.Snapshot(context.Background(), nil)
	if len(r.QuestObjects) != 0 {
		t.Fatal("partialcratecreatecommitted")
	}
	r = packet(t, s, opcode.SMSGUpdateObject, b)
	if len(r.QuestObjects) != 1 || !r.QuestObjects[0].FlagsKnown || r.QuestObjects[0].Flags != 0 || !r.QuestObjects[0].DynamicFlagsKnown {
		t.Fatal(r)
	}
	delta := deltaFixture(harvestGUID, map[int]uint32{objectEntry: 161558})
	delta = append(delta, 255)
	binary.LittleEndian.PutUint32(delta, 2)
	r = packet(t, s, opcode.SMSGUpdateObject, delta)
	if len(r.QuestObjects) != 1 {
		t.Fatal("malformedtailchangedcrate")
	}
	r = packet(t, s, opcode.SMSGNewWorld, login())
	if len(r.QuestObjects) != 0 || len(s.state.gameObjects) != 0 {
		t.Fatal("maptransferkeptcrate")
	}
}
