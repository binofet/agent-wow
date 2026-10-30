package buddyworld

import (
	"context"
	"encoding/binary"
	"sync"
	"testing"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

const firstBag uint64 = 0x4000000000000200
const secondBag uint64 = 0x4000000000000201
const carriedItemGUID uint64 = 0x4000000000000220

func twoBagPlayerFields() map[int]uint32 {
	f := vitalFields()
	// PLAYER_FIELD_INV_SLOT_HEAD0x144 + equipped slot19*2, and slot20*2.
	f[0x16a], f[0x16b] = uint32(firstBag&0xffffffff), uint32(firstBag>>32)
	f[0x16c], f[0x16d] = uint32(secondBag&0xffffffff), uint32(secondBag>>32)
	return f
}
func containerFields(entry, count uint32, contents map[int]uint64) map[int]uint32 {
	f := map[int]uint32{3: entry, 6: 1, 0xe: 1, 0x40: count}
	for slot, guid := range contents {
		f[0x42+slot*2], f[0x43+slot*2] = uint32(guid&0xffffffff), uint32(guid>>32)
	}
	return f
}
func knownTwoBagInventory(t *testing.T) (*Service, *worldv1.SnapshotResponse) {
	t.Helper()
	s := NewIndependentService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, twoBagPlayerFields()))
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(firstBag, 2, containerFields(4496, 6, map[int]uint64{1: carriedItemGUID})))
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(secondBag, 2, containerFields(4498, 8, nil)))
	r := packet(t, s, opcode.SMSGUpdateObject, objectFixture(carriedItemGUID, 1, map[int]uint32{3: 32971, 6: 1, 0xe: 1}))
	if !r.InventoryKnown || len(r.InventoryBags) != 5 {
		t.Fatal("fixture inventory unknown", r.InventoryBags)
	}
	return s, r
}

func TestCarriedInventoryTwoActualBagsAndPartialUnknownBaselines(t *testing.T) {
	s := NewIndependentService(1)
	r := packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, twoBagPlayerFields()))
	if r.InventoryKnown || len(r.InventoryBags) != 0 || r.InventoryRevision != 0 {
		t.Fatal("partial player fields invented inventory baseline", r)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, createFixture(1, twoBagPlayerFields()))
	if r.InventoryKnown || len(r.InventoryBags) != 5 || !r.InventoryBags[0].ContentsKnown || len(r.InventoryBags[0].Slots) != 16 || r.InventoryBags[1].ContainerGuid != firstBag || r.InventoryBags[1].ContentsKnown || !r.InventoryBags[3].ContentsKnown || r.InventoryBags[3].SlotCount != 0 {
		t.Fatal("occupied unknown bag projected as empty", r.InventoryBags)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(firstBag, containerFields(4496, 6, nil)))
	if r.InventoryBags[1].ContentsKnown {
		t.Fatal("unknown container delta invented baseline")
	}
	r = packet(t, s, opcode.SMSGUpdateObject, objectFixture(firstBag, 1, containerFields(4496, 6, nil)))
	if r.InventoryBags[1].MetadataKnown || r.InventoryBags[1].ContentsKnown {
		t.Fatal("ITEM type masqueraded as CONTAINER", r.InventoryBags[1])
	}
	r = packet(t, s, opcode.SMSGCompressedUpdateObject, compressed(t, objectFixture(firstBag, 2, containerFields(4496, 6, map[int]uint64{1: carriedItemGUID}))))
	if r.InventoryKnown || !r.InventoryBags[1].ContentsKnown || len(r.InventoryBags[1].Slots) != 6 || r.InventoryBags[1].Slots[1].ItemGuid != carriedItemGUID || r.InventoryBags[1].Slots[1].MetadataKnown {
		t.Fatal("actual container slots not retained", r.InventoryBags)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, objectFixture(secondBag, 2, containerFields(4498, 8, nil)))
	if !r.InventoryKnown || len(r.InventoryBags[2].Slots) != 8 || !r.InventoryBags[2].Slots[7].MetadataKnown && r.InventoryBags[2].Slots[7].ItemGuid != 0 || len(r.Backpack) != 16 {
		t.Fatal(r.InventoryBags)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, objectFixture(carriedItemGUID, 1, map[int]uint32{3: 32971, 6: 1, 0xe: 1}))
	i := r.InventoryBags[1].Slots[1]
	if i.Bag != 1 || i.Slot != 1 || i.ItemGuid != carriedItemGUID || !i.MetadataKnown || i.Entry != 32971 || i.StackCount != 1 || r.InventoryBags[1].Entry != 4496 || !r.InventoryBags[1].MetadataKnown {
		t.Fatal(i)
	}
	// The observer never exposes another player's private bags.
	r = packet(t, s, opcode.SMSGUpdateObject, createFixture(3, twoBagPlayerFields()))
	if r.Owner != nil || !r.InventoryKnown {
		t.Fatal("independent inventory restored foreign player")
	}
}

func TestCarriedInventoryRevisionIsAtomicAndNotChangedByUnrelatedObservations(t *testing.T) {
	s, r := knownTwoBagInventory(t)
	rev := r.InventoryRevision
	for _, data := range [][]byte{createFixture(1, twoBagPlayerFields()), deltaFixture(firstBag, containerFields(4496, 6, map[int]uint64{1: carriedItemGUID})), objectFixture(npcGUID, 3, npcFields(2)), objectFixture(0x4000000000000345, 1, map[int]uint32{3: 159, 6: 1, 0xe: 1})} {
		r = packet(t, s, opcode.SMSGUpdateObject, data)
		if r.InventoryRevision != rev || !r.InventoryKnown {
			t.Fatal("unchanged reconnect/values/NPC invented inventory mutation", r.InventoryRevision, rev)
		}
	}
	change := deltaFixture(carriedItemGUID, map[int]uint32{0xe: 2})
	bad := append([]byte(nil), change...)
	binary.LittleEndian.PutUint32(bad, 2)
	bad = append(bad, 255)
	for _, data := range [][]byte{bad, append(append([]byte(nil), change...), 255)} {
		r = packet(t, s, opcode.SMSGUpdateObject, data)
		if r.InventoryRevision != rev || r.InventoryBags[1].Slots[1].StackCount != 1 {
			t.Fatal("malformed packet partially committed inventory", r.InventoryBags)
		}
	}
	r = packet(t, s, opcode.SMSGUpdateObject, change)
	if r.InventoryRevision != rev+1 || r.InventoryBags[1].Slots[1].StackCount != 2 {
		t.Fatal(r.InventoryBags)
	}
	rev = r.InventoryRevision
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x16a: 0, 0x16b: 0}))
	if r.InventoryRevision != rev+1 || !r.InventoryKnown || r.InventoryBags[1].ContainerGuid != 0 || !r.InventoryBags[1].ContentsKnown || len(r.InventoryBags[1].Slots) != 0 {
		t.Fatal("two-half bag removal was not one observed mutation", r.InventoryBags)
	}
}

func TestCarriedInventoryForeignMissingInvalidAndDuplicateRefsFailClosed(t *testing.T) {
	for _, mode := range []string{"foreign_container", "foreign_item", "destroy_container", "destroy_item", "invalid_guid", "duplicate_bag", "duplicate_item", "bag_inside_itself"} {
		t.Run(mode, func(t *testing.T) {
			s, before := knownTwoBagInventory(t)
			var r *worldv1.SnapshotResponse
			switch mode {
			case "foreign_container":
				r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(firstBag, map[int]uint32{6: 3}))
			case "foreign_item":
				r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(carriedItemGUID, map[int]uint32{6: 3}))
			case "destroy_container":
				r = packet(t, s, opcode.SMSGDestroyObject, append(binary.LittleEndian.AppendUint64(nil, firstBag), 0))
			case "destroy_item":
				r = packet(t, s, opcode.SMSGDestroyObject, append(binary.LittleEndian.AppendUint64(nil, carriedItemGUID), 0))
			case "invalid_guid":
				r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(firstBag, map[int]uint32{0x44: 3, 0x45: 0}))
			case "duplicate_bag":
				r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x16c: uint32(firstBag & 0xffffffff), 0x16d: uint32(firstBag >> 32)}))
			case "duplicate_item":
				r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(secondBag, map[int]uint32{0x42: uint32(carriedItemGUID & 0xffffffff), 0x43: uint32(carriedItemGUID >> 32)}))
			case "bag_inside_itself":
				r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(firstBag, map[int]uint32{0x44: uint32(firstBag & 0xffffffff), 0x45: uint32(firstBag >> 32)}))
			}
			if r.InventoryRevision != before.InventoryRevision+1 {
				t.Fatal("change not revision-bound", r.InventoryRevision, before.InventoryRevision)
			}
			if mode == "foreign_item" || mode == "destroy_item" {
				i := r.InventoryBags[1].Slots[1]
				if i.MetadataKnown || i.Entry != 0 || i.StackCount != 0 || i.ItemGuid != carriedItemGUID {
					t.Fatal("stale/foreign contents metadata trusted", i)
				}
			} else if r.InventoryKnown {
				t.Fatal("invalid layout remained actionable", r.InventoryBags)
			}
		})
	}
}

func TestCarriedInventoryBoundsAndProtectedCacheRetainAll160Slots(t *testing.T) {
	s := NewService(1)
	fields := vitalFields()
	next := uint64(0x4000000000001000)
	for slot := 0; slot < 16; slot++ {
		g := next
		next++
		fields[0x172+slot*2], fields[0x173+slot*2] = uint32(g&0xffffffff), uint32(g>>32)
	}
	for bag := 0; bag < 4; bag++ {
		g := firstBag + uint64(bag)
		fields[0x16a+bag*2], fields[0x16b+bag*2] = uint32(g&0xffffffff), uint32(g>>32)
	}
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, fields))
	for bag := 0; bag < 4; bag++ {
		contents := map[int]uint64{}
		for slot := 0; slot < 36; slot++ {
			contents[slot] = next
			next++
		}
		packet(t, s, opcode.SMSGUpdateObject, objectFixture(firstBag+uint64(bag), 2, containerFields(4500+uint32(bag), 36, contents)))
	}
	for g := uint64(0x4000000000001000); g < next; g++ {
		packet(t, s, opcode.SMSGUpdateObject, objectFixture(g, 1, map[int]uint32{3: 159, 6: 1, 0xe: 1}))
	}
	for i := uint64(0); i < 300; i++ {
		packet(t, s, opcode.SMSGUpdateObject, objectFixture(0x4000000000002000+i, 1, map[int]uint32{3: 159, 6: 1, 0xe: 1}))
	}
	r, _ := s.Snapshot(context.Background(), nil)
	if !r.InventoryKnown || len(s.state.items) != 256 {
		t.Fatal(r.InventoryKnown, len(s.state.items))
	}
	n := 0
	for _, b := range r.InventoryBags {
		for _, i := range b.Slots {
			n++
			if !i.MetadataKnown {
				t.Fatal("referenced carried item evicted", b.Bag, i)
			}
		}
	}
	if n != 160 {
		t.Fatal(n)
	}
	before := r.InventoryRevision
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(firstBag, map[int]uint32{0x40: 37}))
	if r.MalformedPackets != 1 || r.InventoryRevision != before || !r.InventoryKnown || r.InventoryBags[1].SlotCount != 36 {
		t.Fatal("oversized container escaped atomic bound", r)
	}
}

func TestCarriedInventoryLifecycleDoesNotReuseStaleContainerBaseline(t *testing.T) {
	for _, mode := range []string{"destroy_self", "out_of_range", "transfer", "new_world"} {
		t.Run(mode, func(t *testing.T) {
			s, before := knownTwoBagInventory(t)
			var r *worldv1.SnapshotResponse
			switch mode {
			case "destroy_self":
				r = packet(t, s, opcode.SMSGDestroyObject, []byte{1, 0, 0, 0, 0, 0, 0, 0, 0})
			case "out_of_range":
				r = packet(t, s, opcode.SMSGUpdateObject, []byte{1, 0, 0, 0, 4, 1, 0, 0, 0, 1, 1})
			case "transfer":
				r = packet(t, s, opcode.SMSGTransferPending, []byte{1, 0, 0, 0})
			case "new_world":
				r = packet(t, s, opcode.SMSGNewWorld, login())
			}
			if r.InventoryKnown || len(r.InventoryBags) != 0 || r.InventoryRevision <= before.InventoryRevision {
				t.Fatal(r.InventoryBags, r.InventoryRevision)
			}
			r = packet(t, s, opcode.SMSGUpdateObject, createFixture(1, twoBagPlayerFields()))
			if r.InventoryKnown || r.InventoryBags[1].ContentsKnown {
				t.Fatal("old container baseline reused after lifecycle", r.InventoryBags)
			}
			r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(firstBag, map[int]uint32{0x40: 6}))
			if r.InventoryBags[1].ContentsKnown {
				t.Fatal("partial delta restored stale container knowledge")
			}
			packet(t, s, opcode.SMSGUpdateObject, objectFixture(firstBag, 2, containerFields(4496, 6, nil)))
			r = packet(t, s, opcode.SMSGUpdateObject, objectFixture(secondBag, 2, containerFields(4498, 8, nil)))
			if !r.InventoryKnown {
				t.Fatal(r.InventoryBags)
			}
		})
	}
}

func TestCarriedInventorySnapshotsArePrivateCopiesAndConcurrent(t *testing.T) {
	s, r := knownTwoBagInventory(t)
	r.InventoryBags[1].Slots[1].Entry = 999
	r.InventoryBags[1].ContainerGuid = 3
	r, _ = s.Snapshot(context.Background(), nil)
	if r.InventoryBags[1].ContainerGuid != firstBag || r.InventoryBags[1].Slots[1].Entry != 32971 {
		t.Fatal("mutable snapshot aliases state")
	}
	before := proto.Clone(r).(*worldv1.SnapshotResponse)
	packet(t, s, opcode.SMSGUpdateObject, createFixture(3, twoBagPlayerFields()))
	after, _ := s.Snapshot(context.Background(), nil)
	if !proto.Equal(before.InventoryBags[1], after.InventoryBags[1]) || after.InventoryRevision != before.InventoryRevision {
		t.Fatal("foreign private fields changed own inventory")
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGUpdateObject, Payload: deltaFixture(carriedItemGUID, map[int]uint32{0xe: 1})})
				s.Snapshot(context.Background(), nil)
			}
		}()
	}
	wg.Wait()
}

func TestCarriedInventoryForeignOwnershipCannotRenewPrivateContentsByOwnerDelta(t *testing.T) {
	s, _ := knownTwoBagInventory(t)
	for _, guid := range []uint64{firstBag, carriedItemGUID} {
		packet(t, s, opcode.SMSGUpdateObject, deltaFixture(guid, map[int]uint32{6: 3}))
		r := packet(t, s, opcode.SMSGUpdateObject, deltaFixture(guid, map[int]uint32{6: 1}))
		if guid == firstBag && (r.InventoryKnown || r.InventoryBags[1].ContentsKnown || r.InventoryBags[1].MetadataKnown) {
			t.Fatal("owner-only delta renewed private container contents", r.InventoryBags)
		}
		if guid == carriedItemGUID && r.InventoryBags[1].ContentsKnown && r.InventoryBags[1].Slots[1].MetadataKnown {
			t.Fatal("owner-only delta renewed private stack metadata")
		}
		if guid == firstBag {
			packet(t, s, opcode.SMSGUpdateObject, objectFixture(firstBag, 2, containerFields(4496, 6, map[int]uint64{1: carriedItemGUID})))
		}
	}
	r := packet(t, s, opcode.SMSGUpdateObject, objectFixture(carriedItemGUID, 1, map[int]uint32{3: 32971, 6: 1, 0xe: 1}))
	if !r.InventoryKnown || !r.InventoryBags[1].Slots[1].MetadataKnown {
		t.Fatal("fresh actual own CREATE did not restore inventory", r.InventoryBags)
	}
}
