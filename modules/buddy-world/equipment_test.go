package buddyworld

import (
	"context"
	"encoding/binary"
	"sync"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const offhandItem uint64 = 0x4000000000000042

func offhandFields() map[int]uint32 {
	f := vitalFields()
	// PRIVATE PLAYER_FIELD_INV_SLOT_HEAD0x144 + slot16 *2, source Player.h.
	f[0x164], f[0x165] = uint32(offhandItem&0xffffffff), uint32(offhandItem>>32)
	return f
}

func TestOwnOffhandRequiresCreateBaselineAndKnowsOmittedEmptySlot(t *testing.T) {
	s := NewService(1)
	r := packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	if r.Self.Offhand.SlotKnown || r.Self.OffhandRevision != 0 {
		t.Fatal("login invented equipment knowledge", r.Self)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x164: uint32(offhandItem & 0xffffffff), 0x165: uint32(offhandItem >> 32)}))
	if r.Self.Offhand.SlotKnown || r.Self.Offhand.ItemGuid != 0 || r.Self.OffhandRevision != 0 {
		t.Fatal("partial values invented full equipment baseline", r.Self)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	if !r.Self.Offhand.SlotKnown || r.Self.Offhand.ItemGuid != 0 || r.Self.Offhand.MetadataKnown || r.Self.OffhandRevision != 1 {
		t.Fatal("omitted own CREATE fields must be known empty", r.Self)
	}
	r = packet(t, s, opcode.SMSGCompressedUpdateObject, compressed(t, deltaFixture(1, map[int]uint32{0x164: uint32(offhandItem & 0xffffffff), 0x165: uint32(offhandItem >> 32)})))
	if !r.Self.Offhand.SlotKnown || r.Self.Offhand.ItemGuid != offhandItem || r.Self.Offhand.MetadataKnown || r.Self.OffhandRevision != 2 {
		t.Fatal(r.Self)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x164: 0, 0x165: 0}))
	if !r.Self.Offhand.SlotKnown || r.Self.Offhand.ItemGuid != 0 || r.Self.OffhandRevision != 3 {
		t.Fatal("unequip was not observed", r.Self)
	}
}

func TestOffhandMetadataOwnershipAndWholePacketRevision(t *testing.T) {
	s := NewService(1)
	r := packet(t, s, opcode.SMSGUpdateObject, createFixture(1, offhandFields()))
	if !r.Self.Offhand.SlotKnown || r.Self.Offhand.ItemGuid != offhandItem || r.Self.OffhandRevision != 1 {
		t.Fatal(r.Self)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, objectFixture(offhandItem, 1, map[int]uint32{3: 15925, 6: 1, 0xe: 1, 0x3c: 10, 0x3d: 10}))
	if !r.Self.Offhand.MetadataKnown || r.Self.Offhand.Entry != 15925 || !r.Self.Offhand.DurabilityKnown || r.Self.Offhand.Durability != 10 || r.Self.OffhandRevision != 2 {
		t.Fatal(r.Self)
	}
	for _, data := range [][]byte{createFixture(1, offhandFields()), deltaFixture(1, map[int]uint32{0x164: uint32(offhandItem & 0xffffffff), 0x165: uint32(offhandItem >> 32), unitHealth: 30}), deltaFixture(offhandItem, map[int]uint32{0x3c: 10, 0x3d: 10})} {
		r = packet(t, s, opcode.SMSGUpdateObject, data)
		if r.Self.OffhandRevision != 2 {
			t.Fatal("unchanged reconnect/values invented equipment change", r.Self)
		}
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(offhandItem, map[int]uint32{0x3c: 0}))
	if !r.Self.Offhand.DurabilityKnown || r.Self.Offhand.Durability != 0 || r.Self.OffhandRevision != 3 {
		t.Fatal(r.Self)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(offhandItem, map[int]uint32{6: 3}))
	if r.Self.Offhand.MetadataKnown || r.Self.Offhand.Entry != 0 || r.Self.Offhand.DurabilityKnown || r.Self.OffhandRevision != 4 {
		t.Fatal("foreign item metadata trusted", r.Self)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(offhandItem, map[int]uint32{6: 1, 0x3c: 11}))
	if !r.Self.Offhand.MetadataKnown || r.Self.Offhand.DurabilityKnown || r.Self.OffhandRevision != 5 {
		t.Fatal("invalid durability accepted", r.Self)
	}
	// A two-half slot change is one committed equipment revision.
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x164: 0, 0x165: 0}))
	if r.Self.OffhandRevision != 6 {
		t.Fatal(r.Self)
	}
}

func TestOffhandForeignAndMalformedUpdatesNeverAlterPrivateProof(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, offhandFields()))
	packet(t, s, opcode.SMSGUpdateObject, createFixture(3, offhandFields()))
	packet(t, s, opcode.SMSGUpdateObject, deltaFixture(3, map[int]uint32{0x164: 999, 0x165: 0}))
	bad := deltaFixture(1, map[int]uint32{0x164: 0, 0x165: 0})
	for n := 0; n < len(bad); n++ {
		packet(t, s, opcode.SMSGUpdateObject, bad[:n])
	}
	packet(t, s, opcode.SMSGUpdateObject, append(bad, 0xff))
	// A complete earlier block must also roll back when a later block is bad.
	compound := append([]byte(nil), bad...)
	binary.LittleEndian.PutUint32(compound, 2)
	compound = append(compound, 0xff)
	r := packet(t, s, opcode.SMSGUpdateObject, compound)
	if !r.Self.Offhand.SlotKnown || r.Self.Offhand.ItemGuid != offhandItem || r.Self.OffhandRevision != 1 || r.Owner.Offhand != nil || r.Owner.OffhandRevision != 0 {
		t.Fatal("malformed/foreign private equipment escaped", r)
	}
}

func TestOffhandLifecycleInvalidatesEquipmentUntilNewOwnCreate(t *testing.T) {
	for _, mode := range []string{"destroy", "out_of_range", "transfer", "new_world"} {
		t.Run(mode, func(t *testing.T) {
			s := NewService(1)
			packet(t, s, opcode.SMSGLoginVerifyWorld, login())
			packet(t, s, opcode.SMSGUpdateObject, createFixture(1, offhandFields()))
			var r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitHealth: 45}))
			switch mode {
			case "destroy":
				b := make([]byte, 9)
				binary.LittleEndian.PutUint64(b, 1)
				r = packet(t, s, opcode.SMSGDestroyObject, b)
			case "out_of_range":
				b := []byte{1, 0, 0, 0, 4, 1, 0, 0, 0}
				b = append(b, packedFixture(1)...)
				r = packet(t, s, opcode.SMSGUpdateObject, b)
			case "transfer":
				r = packet(t, s, opcode.SMSGTransferPending, []byte{1, 0, 0, 0})
			case "new_world":
				r = packet(t, s, opcode.SMSGNewWorld, login())
			}
			if r.Self.Offhand.SlotKnown || r.Self.Offhand.ItemGuid != 0 || r.Self.OffhandRevision != 2 {
				t.Fatal("stale equipment survived world lifecycle", r.Self)
			}
			r = packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
			if !r.Self.Offhand.SlotKnown || r.Self.Offhand.ItemGuid != 0 || r.Self.OffhandRevision != 3 {
				t.Fatal(r.Self)
			}
		})
	}
}

func TestOffhandSnapshotsAndIndependentIdentitiesStayPrivateAndBounded(t *testing.T) {
	s := NewIndependentService(6)
	packet(t, s, opcode.SMSGUpdateObject, createFixture(6, offhandFields()))
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(offhandItem, 1, map[int]uint32{3: 15925, 6: 6, 0xe: 1}))
	for i := uint64(100); i < 100+maxItems+10; i++ {
		packet(t, s, opcode.SMSGUpdateObject, objectFixture(0x4000000000000000|i, 1, map[int]uint32{3: 123, 6: 6, 0xe: 1}))
	}
	if len(s.state.items) > maxItems || s.state.items[offhandItem] == nil {
		t.Fatal("offhand evicted or item cache unbounded")
	}
	r, _ := s.Snapshot(context.Background(), nil)
	if r.Owner != nil || !r.Self.Offhand.MetadataKnown || r.Self.Offhand.ItemGuid != offhandItem {
		t.Fatal(r)
	}
	r.Self.Offhand.ItemGuid, r.Self.Offhand.Entry = 999, 999
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 30 {
				r, err := s.Snapshot(context.Background(), nil)
				if err != nil || r.Self.Offhand.ItemGuid != offhandItem || r.Self.Offhand.Entry != 15925 {
					t.Error("snapshot ownership/copy changed", r, err)
					return
				}
				r.Self.Offhand.Entry = 0
			}
		}()
	}
	wg.Wait()
}
