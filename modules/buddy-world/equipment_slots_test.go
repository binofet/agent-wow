package buddyworld

import (
	"context"
	"encoding/binary"
	"sync"
	"testing"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const armorItem uint64 = 0x4000000000000510

func equipmentFields(slot int, guid uint64) map[int]uint32 {
	f := vitalFields()
	f[0x144+slot*2], f[0x145+slot*2] = uint32(guid&0xffffffff), uint32(guid>>32)
	return f
}

func TestEquipmentSlotsRequireOwnCreateAndCoverAllNineteen(t *testing.T) {
	s := NewService(1)
	r := packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, equipmentFields(6, armorItem)))
	if r.SelfEquipmentKnown || len(r.SelfEquipment) != 0 || r.SelfEquipmentRevision != 0 {
		t.Fatal("partial values invented full equipment", r)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	if !r.SelfEquipmentKnown || len(r.SelfEquipment) != 19 || r.SelfEquipmentRevision != 1 {
		t.Fatal(r)
	}
	for slot, v := range r.SelfEquipment {
		if v.Slot != uint32(slot) || !v.SlotKnown || v.ItemGuid != 0 || v.MetadataKnown || v.DurabilityKnown {
			t.Fatal("omitted own CREATE slot must be known empty", v)
		}
	}
	fields := vitalFields()
	for slot := 0; slot < 19; slot++ {
		g := armorItem + uint64(slot)
		fields[0x144+slot*2], fields[0x145+slot*2] = uint32(g&0xffffffff), uint32(g>>32)
	}
	// Slot19 belongs to bags and must not become a twentieth equipment slot.
	fields[0x16a], fields[0x16b] = uint32(firstBag&0xffffffff), uint32(firstBag>>32)
	r = packet(t, s, opcode.SMSGCompressedUpdateObject, compressed(t, createFixture(1, fields)))
	if !r.SelfEquipmentKnown || len(r.SelfEquipment) != 19 || r.SelfEquipmentRevision != 2 {
		t.Fatal(r)
	}
	for slot, v := range r.SelfEquipment {
		if v.ItemGuid != armorItem+uint64(slot) || !v.SlotKnown || v.MetadataKnown {
			t.Fatal("wrong private equipment field offset", v)
		}
	}
	if r.Self.Mainhand.ItemGuid != armorItem+15 || r.Self.Offhand.ItemGuid != armorItem+16 {
		t.Fatal("existing main/offhand changed", r.Self)
	}
}

func TestEquipmentMetadataOwnedCurrentEpochAndAtomicRevision(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, equipmentFields(6, armorItem)))
	r := packet(t, s, opcode.SMSGUpdateObject, objectFixture(armorItem, 1, map[int]uint32{3: 2962, 6: 1, 0xe: 1, 0x3c: 12, 0x3d: 20}))
	v := r.SelfEquipment[6]
	if !r.SelfEquipmentKnown || !v.MetadataKnown || v.Entry != 2962 || v.StackCount != 1 || !v.DurabilityKnown || v.Durability != 12 || v.MaxDurability != 20 || r.SelfEquipmentRevision != 2 {
		t.Fatal(v, r.SelfEquipmentRevision)
	}
	for _, data := range [][]byte{createFixture(1, equipmentFields(6, armorItem)), deltaFixture(1, map[int]uint32{unitHealth: 33}), deltaFixture(armorItem, map[int]uint32{0x3c: 12}), objectFixture(armorItem+100, 1, map[int]uint32{3: 2962, 6: 1, 0xe: 1})} {
		r = packet(t, s, opcode.SMSGUpdateObject, data)
		if r.SelfEquipmentRevision != 2 {
			t.Fatal("unchanged/unrelated packet changed equipment revision", r.SelfEquipmentRevision)
		}
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(armorItem, map[int]uint32{0x3c: 0, 0xe: 2}))
	if r.SelfEquipmentRevision != 3 || r.SelfEquipment[6].Durability != 0 || !r.SelfEquipment[6].DurabilityKnown || r.SelfEquipment[6].StackCount != 2 {
		t.Fatal("one whole packet must advance once and preserve broken durability", r.SelfEquipment[6])
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(armorItem, map[int]uint32{0x3c: 21}))
	if r.SelfEquipment[6].DurabilityKnown || r.SelfEquipmentRevision != 4 {
		t.Fatal("invalid durability trusted", r.SelfEquipment[6])
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(armorItem, map[int]uint32{6: 3}))
	if r.SelfEquipment[6].MetadataKnown || r.SelfEquipment[6].Entry != 0 || r.SelfEquipment[6].DurabilityKnown || r.SelfEquipmentRevision != 5 {
		t.Fatal("foreign item metadata trusted", r.SelfEquipment[6])
	}
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(armorItem, 1, map[int]uint32{3: 2962, 6: 1, 0xe: 1}))
	packet(t, s, opcode.SMSGNewWorld, login())
	r = packet(t, s, opcode.SMSGUpdateObject, createFixture(1, equipmentFields(6, armorItem)))
	if !r.SelfEquipmentKnown || r.SelfEquipment[6].MetadataKnown {
		t.Fatal("old-world owned item reused as current equipment proof", r.SelfEquipment[6])
	}
	r = packet(t, s, opcode.SMSGUpdateObject, objectFixture(armorItem, 1, map[int]uint32{3: 2962, 6: 1, 0xe: 1}))
	if !r.SelfEquipment[6].MetadataKnown {
		t.Fatal("fresh owned CREATE did not restore proof", r.SelfEquipment[6])
	}
}

func TestEquipmentMalformedForeignInvalidAndDuplicateFailClosed(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, equipmentFields(6, armorItem)))
	packet(t, s, opcode.SMSGUpdateObject, createFixture(3, equipmentFields(6, armorItem+1)))
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(8, 4, equipmentFields(6, armorItem+2)))
	change := deltaFixture(1, map[int]uint32{0x150: 0, 0x151: 0})
	for n := 0; n < len(change); n++ {
		packet(t, s, opcode.SMSGUpdateObject, change[:n])
	}
	bad := append(append([]byte(nil), change...), 0xff)
	packet(t, s, opcode.SMSGUpdateObject, bad)
	binary.LittleEndian.PutUint32(bad, 2)
	r := packet(t, s, opcode.SMSGUpdateObject, bad)
	if r.SelfEquipmentRevision != 1 || r.SelfEquipment[6].ItemGuid != armorItem || r.Owner.Offhand != nil {
		t.Fatal("malformed/foreign packet mutated private equipment", r)
	}
	if len(r.NearbyPlayers) != 1 || r.NearbyPlayers[0].Mainhand != nil || r.NearbyPlayers[0].Offhand != nil {
		t.Fatal("ambient private equipment escaped", r.NearbyPlayers)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x144: 99, 0x145: 0}))
	if r.SelfEquipmentKnown || r.SelfEquipment[0].SlotKnown || r.SelfEquipment[0].MetadataKnown {
		t.Fatal("invalid equipment reference trusted", r.SelfEquipment[0])
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x144: uint32(armorItem & 0xffffffff), 0x145: uint32(armorItem >> 32)}))
	if r.SelfEquipmentKnown {
		t.Fatal("duplicate reference trusted as coherent layout")
	}
}

func TestEquipmentLifecycleAndCacheProtection(t *testing.T) {
	for _, mode := range []string{"destroy_self", "out_of_range", "transfer", "new_world", "destroy_item"} {
		t.Run(mode, func(t *testing.T) {
			s := NewIndependentService(6)
			packet(t, s, opcode.SMSGUpdateObject, createFixture(6, equipmentFields(6, armorItem)))
			packet(t, s, opcode.SMSGUpdateObject, objectFixture(armorItem, 1, map[int]uint32{3: 2962, 6: 6, 0xe: 1}))
			for i := uint64(1000); i < 1000+maxItems+10; i++ {
				packet(t, s, opcode.SMSGUpdateObject, objectFixture(0x4000000000000000|i, 1, map[int]uint32{3: 1, 6: 6, 0xe: 1}))
			}
			if len(s.state.items) > maxItems || s.state.items[armorItem] == nil {
				t.Fatal("referenced armor evicted or cache unbounded")
			}
			var r *worldv1.SnapshotResponse
			switch mode {
			case "destroy_self", "destroy_item":
				guid := uint64(6)
				if mode == "destroy_item" {
					guid = armorItem
				}
				b := make([]byte, 9)
				binary.LittleEndian.PutUint64(b, guid)
				r = packet(t, s, opcode.SMSGDestroyObject, b)
			case "out_of_range":
				r = packet(t, s, opcode.SMSGUpdateObject, append([]byte{1, 0, 0, 0, 4, 1, 0, 0, 0}, packedFixture(6)...))
			case "transfer":
				r = packet(t, s, opcode.SMSGTransferPending, []byte{1, 0, 0, 0})
			case "new_world":
				r = packet(t, s, opcode.SMSGNewWorld, login())
			}
			if mode == "destroy_item" {
				if !r.SelfEquipmentKnown || r.SelfEquipment[6].MetadataKnown {
					t.Fatal(r.SelfEquipment)
				}
			} else if r.SelfEquipmentKnown || len(r.SelfEquipment) != 0 {
				t.Fatal("stale equipment baseline survived lifecycle", r.SelfEquipment)
			}
			if r.Owner != nil || r.SelfEquipmentRevision != 3 {
				t.Fatal(r.SelfEquipmentRevision, r.Owner)
			}
		})
	}
}

func TestEquipmentAndCarriedDurabilitySnapshotsArePrivateCopies(t *testing.T) {
	s := NewIndependentService(6)
	f := equipmentFields(6, armorItem)
	f[0x172], f[0x173] = uint32((armorItem+1)&0xffffffff), uint32((armorItem+1)>>32)
	packet(t, s, opcode.SMSGUpdateObject, createFixture(6, f))
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(armorItem, 1, map[int]uint32{3: 2962, 6: 6, 0xe: 1, 0x3c: 4, 0x3d: 12}))
	r := packet(t, s, opcode.SMSGUpdateObject, objectFixture(armorItem+1, 1, map[int]uint32{3: 2962, 6: 6, 0xe: 1, 0x3c: 0, 0x3d: 12}))
	if !r.InventoryBags[0].Slots[0].DurabilityKnown || r.InventoryBags[0].Slots[0].Durability != 0 || r.InventoryBags[0].Slots[0].MaxDurability != 12 || !r.Backpack[0].DurabilityKnown {
		t.Fatal("broken carried armor observation missing", r.InventoryBags[0].Slots[0])
	}
	rev := r.InventoryRevision
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(armorItem+1, map[int]uint32{0x3c: 12}))
	if r.InventoryRevision != rev+1 || r.InventoryBags[0].Slots[0].Durability != 12 {
		t.Fatal(r.InventoryRevision, r.InventoryBags[0])
	}
	r.SelfEquipment[6].Entry, r.InventoryBags[0].Slots[0].Durability = 999, 999
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				r, err := s.Snapshot(context.Background(), nil)
				if err != nil || !r.SelfEquipmentKnown || r.SelfEquipment[6].Entry != 2962 || r.InventoryBags[0].Slots[0].Durability != 12 {
					t.Error("private snapshot alias", r, err)
					return
				}
				r.SelfEquipment[6].Entry = 0
			}
		}()
	}
	wg.Wait()
}

func TestObservedRaceByteSelfOwnerAndNearbyStayConsistent(t *testing.T) {
	s := NewService(1)
	for _, race := range []uint32{1, 2, 3, 4, 5, 6, 7, 8, 10, 11, 0, 9, 255} {
		f := vitalFields()
		// UNIT_FIELD_BYTES_0 absolute0x17: race byte0,class byte1,gender byte2,power byte3.
		f[0x17] = race | 9<<8 | 1<<16 | 3<<24
		packet(t, s, opcode.SMSGUpdateObject, createFixture(1, f))
		packet(t, s, opcode.SMSGUpdateObject, createFixture(3, f))
		r := packet(t, s, opcode.SMSGUpdateObject, objectFixture(8, 4, f))
		known := race > 0 && race <= 11 && race != 9
		for _, p := range []*worldv1.PlayerState{r.Self, r.Owner, r.NearbyPlayers[0]} {
			if p.RaceId != race || p.RaceKnown != known || !p.ClassKnown || p.ClassId != 9 || p.PowerType != 3 {
				t.Fatal("race byte offset/public projection mismatch", p)
			}
		}
	}
	before, _ := s.Snapshot(context.Background(), nil)
	bad := append(deltaFixture(1, map[int]uint32{0x17: 1 | 5<<8}), 255)
	r := packet(t, s, opcode.SMSGUpdateObject, bad)
	if r.Self.RaceId != before.Self.RaceId || r.Self.ClassId != before.Self.ClassId {
		t.Fatal("malformed race update committed", r.Self)
	}
}

func TestOwnedItemEnhancementBaselineAndChangesAreNotGuessed(t *testing.T) {
	for _, field := range []int{0x16, 0x18, 0x37, 0x39, 0x3a, 0x3b} {
		t.Run(string(rune('A'+field-0x16)), func(t *testing.T) {
			s := NewService(1)
			f := equipmentFields(6, armorItem)
			f[0x172], f[0x173] = uint32((armorItem+1)&0xffffffff), uint32((armorItem+1)>>32)
			packet(t, s, opcode.SMSGUpdateObject, createFixture(1, f))
			r := packet(t, s, opcode.SMSGUpdateObject, deltaFixture(armorItem, map[int]uint32{field: 123}))
			if r.SelfEquipment[6].EnhancementsKnown {
				t.Fatal("unknown item delta invented enhancement baseline")
			}
			packet(t, s, opcode.SMSGUpdateObject, objectFixture(armorItem, 1, map[int]uint32{3: 2962, 6: 1, 0xe: 1}))
			r = packet(t, s, opcode.SMSGUpdateObject, objectFixture(armorItem+1, 1, map[int]uint32{3: 2962, 6: 1, 0xe: 1}))
			if !r.SelfEquipment[6].EnhancementsKnown || r.SelfEquipment[6].HasEnhancements || !r.InventoryBags[0].Slots[0].EnhancementsKnown || r.InventoryBags[0].Slots[0].HasEnhancements {
				t.Fatal("omitted CREATE zero enhancement baseline lost", r)
			}
			erev, irev := r.SelfEquipmentRevision, r.InventoryRevision
			r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(armorItem, map[int]uint32{field: 123}))
			if !r.SelfEquipment[6].EnhancementsKnown || !r.SelfEquipment[6].HasEnhancements || r.SelfEquipmentRevision != erev+1 {
				t.Fatal(r.SelfEquipment[6])
			}
			r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(armorItem+1, map[int]uint32{field: 123}))
			if !r.InventoryBags[0].Slots[0].HasEnhancements || !r.Backpack[0].HasEnhancements || r.InventoryRevision != irev+1 {
				t.Fatal(r.InventoryBags[0].Slots[0])
			}
			r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(armorItem+1, map[int]uint32{6: 3}))
			if r.InventoryBags[0].Slots[0].EnhancementsKnown || r.Backpack[0].EnhancementsKnown {
				t.Fatal("foreign enhancement authority leaked")
			}
			packet(t, s, opcode.SMSGNewWorld, login())
			r = packet(t, s, opcode.SMSGUpdateObject, createFixture(1, f))
			if r.SelfEquipment[6].EnhancementsKnown || r.InventoryBags[0].Slots[0].EnhancementsKnown {
				t.Fatal("old epoch enhancement proof reused")
			}
		})
	}
}
