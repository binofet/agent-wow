package buddyworld

import (
	"encoding/binary"
	"slices"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// Literal aura fields follow AuraApplication::BuildUpdatePacket in build 12340.
func fixtureCreatureAura(slot byte, spell uint32, flags, level, count byte, caster uint64, duration, remaining uint32) []byte {
	row := binary.LittleEndian.AppendUint32([]byte{slot}, spell)
	if spell == 0 {
		return row
	}
	row = append(row, flags, level, count)
	if flags&8 == 0 {
		row = append(row, packedFixture(caster)...)
	}
	if flags&0x20 != 0 {
		row = binary.LittleEndian.AppendUint32(row, duration)
		row = binary.LittleEndian.AppendUint32(row, remaining)
	}
	return row
}

func TestVisibleCreatureAurasCarryObservedSlotsAndDuration(t *testing.T) {
	s := NewService(1)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	create := func(id uint64) {
		packet(t, s, opcode.SMSGUpdateObject, objectFixture(id, 3, npcFields(0)))
	}
	create(npcGUID)
	create(npcGUID + 1)
	view := func(id uint64) *worldv1.VisibleCreatureAuras {
		t.Helper()
		v, err := s.Snapshot(t.Context(), &worldv1.SnapshotRequest{CreatureGuid: id})
		if err != nil || v.GetRequestedCreature() == nil {
			t.Fatalf("creature lookup %v: %v", v, err)
		}
		return v.RequestedCreature.VisibleAuras
	}
	send := func(id uint64, all bool, rows ...[]byte) {
		t.Helper()
		body := packedFixture(id)
		for _, row := range rows {
			body = append(body, row...)
		}
		op := uint32(opcode.SMSGAuraUpdate)
		if all {
			op = opcode.SMSGAuraUpdateAll
		}
		packet(t, s, op, body)
	}
	if view(npcGUID) != nil {
		t.Fatal("no aura packet supplied an empty list")
	}
	bad := fixtureCreatureAura(7, 18173, 0xa1, 46, 3, npcGUID+1, 12000, 11000)
	good := fixtureCreatureAura(2, 8269, 0x19, 48, 0, 0, 0, 0)
	send(npcGUID, false, bad)
	now = now.Add(1700 * time.Millisecond)
	v := view(npcGUID)
	if v == nil || v.Complete || v.FullRevision != nil || len(v.Slots) != 1 || v.Slots[0].Slot != 7 ||
		v.Slots[0].SpellId != 18173 || v.Slots[0].CasterGuid == nil || v.Slots[0].GetCasterGuid() != npcGUID+1 ||
		v.Slots[0].CasterLevel != 46 || v.Slots[0].StacksOrCharges != 3 || v.Slots[0].GetRemainingMs() != 9300 ||
		v.Slots[0].GetMaxDurationMs() != 12000 || !v.Slots[0].Negative || v.Slots[0].Positive || v.Slots[0].Flags != 0xa1 ||
		v.Revision == 0 || v.Revision != view(npcGUID).Revision || view(npcGUID+1) != nil {
		t.Fatalf("sparse aura %v", v)
	}
	send(npcGUID, false, fixtureCreatureAura(5, 0, 0, 0, 0, 0, 0, 0))
	v = view(npcGUID)
	if v.Complete || !slices.Equal(v.ClearedSlots, []uint32{5}) || len(v.Slots) != 1 {
		t.Fatal(v)
	}
	send(npcGUID, true, bad, good)
	v = view(npcGUID)
	if !v.Complete || v.GetFullRevision() != v.Revision || len(v.Slots) != 2 || len(v.ClearedSlots) != 0 || v.Slots[0].Slot != 2 ||
		v.Slots[0].GetCasterGuid() != npcGUID || v.Slots[0].StacksOrCharges != 0 || v.Slots[0].CasterLevel != 48 ||
		v.Slots[0].RemainingMs != nil || v.Slots[0].MaxDurationMs != nil || !v.Slots[0].Positive || v.Slots[0].Negative || v.Slots[1].Slot != 7 {
		t.Fatal(v)
	}
	send(npcGUID, false, fixtureCreatureAura(7, 18173, 0xa1, 46, 2, npcGUID+1, 12000, 700))
	now = now.Add(800 * time.Millisecond)
	v = view(npcGUID)
	if !v.Complete || v.GetFullRevision() == 0 || v.GetFullRevision() >= v.Revision || len(v.Slots) != 2 || v.Slots[1].StacksOrCharges != 2 || v.Slots[1].RemainingMs == nil ||
		v.Slots[1].GetRemainingMs() != 0 || v.Slots[1].SpellId != 18173 {
		t.Fatal("elapsed displayed timer changed the received slot", v)
	}
	send(npcGUID, false, fixtureCreatureAura(7, 0, 0, 0, 0, 0, 0, 0))
	if v = view(npcGUID); !v.Complete || len(v.Slots) != 1 || v.Slots[0].Slot != 2 {
		t.Fatal(v)
	}
	send(npcGUID, true)
	if v = view(npcGUID); v == nil || !v.Complete || len(v.Slots) != 0 {
		t.Fatal("observed empty list", v)
	}
	send(npcGUID+1, true, bad)
	send(npcGUID, false, good[:len(good)-1])
	if view(npcGUID) != nil || !view(npcGUID+1).GetComplete() {
		t.Fatal("malformed bound update contaminated another creature or retained complete")
	}
	send(npcGUID, false, bad)
	if v = view(npcGUID); v == nil || v.Complete || len(v.Slots) != 1 {
		t.Fatal("delta restored unobserved slots", v)
	}
}

func TestVisibleCreatureAurasFollowExactCreatureLifetime(t *testing.T) {
	for _, change := range []string{"out_of_range", "destroy", "recreate", "transfer", "relogin"} {
		t.Run(change, func(t *testing.T) {
			s := NewService(1)
			packet(t, s, opcode.SMSGLoginVerifyWorld, login())
			data := append(packedFixture(npcGUID), fixtureCreatureAura(3, 8269, 0x19, 46, 1, 0, 0, 0)...)
			read := func() *worldv1.VisibleCreatureAuras {
				snap, err := s.Snapshot(t.Context(), &worldv1.SnapshotRequest{CreatureGuid: npcGUID})
				if err != nil {
					t.Fatal(err)
				}
				return snap.GetRequestedCreature().GetVisibleAuras()
			}
			packet(t, s, opcode.SMSGAuraUpdateAll, data)
			create := objectFixture(npcGUID, 3, npcFields(0))
			packet(t, s, opcode.SMSGUpdateObject, create)
			if read() != nil {
				t.Fatal("pre-create data attached to later body")
			}
			packet(t, s, opcode.SMSGAuraUpdateAll, data)
			if !read().GetComplete() {
				t.Fatal("initial full update missing")
			}
			switch change {
			case "out_of_range":
				clear := []byte{1, 0, 0, 0, 4, 1, 0, 0, 0}
				packet(t, s, opcode.SMSGUpdateObject, append(clear, packedFixture(npcGUID)...))
			case "destroy":
				packet(t, s, opcode.SMSGDestroyObject, append(binary.LittleEndian.AppendUint64(nil, npcGUID), 0))
			case "transfer":
				packet(t, s, opcode.SMSGTransferPending, make([]byte, 4))
				packet(t, s, opcode.SMSGNewWorld, login())
			case "relogin":
				packet(t, s, opcode.SMSGLoginVerifyWorld, login())
			}
			if change != "recreate" {
				packet(t, s, opcode.SMSGAuraUpdateAll, data)
				if read() != nil {
					t.Fatal("absent creature has an aura set")
				}
			}
			packet(t, s, opcode.SMSGUpdateObject, create)
			if read() != nil {
				t.Fatal("same GUID inherited former visibility")
			}
			packet(t, s, opcode.SMSGAuraUpdateAll, packedFixture(npcGUID))
			if v := read(); !v.GetComplete() || len(v.Slots) != 0 {
				t.Fatal("fresh observed empty list missing", v)
			}
		})
	}
}

func TestVisibleCreatureAurasWithdrawUncertainLifetime(t *testing.T) {
	first, other := npcGUID, npcGUID+1
	target := binary.LittleEndian.AppendUint64(nil, first)
	for _, tc := range []struct {
		name                   string
		op                     uint32
		data                   []byte
		firstKnown, otherKnown bool
	}{
		{"short keyed destroy", opcode.SMSGDestroyObject, target, false, true},
		{"trailing keyed destroy", opcode.SMSGDestroyObject, append(append([]byte{}, target...), 0, 0), false, true},
		{"unreadable destroy", opcode.SMSGDestroyObject, []byte{1, 2, 3}, false, false},
		{"unrelated keyed destroy", opcode.SMSGDestroyObject, binary.LittleEndian.AppendUint64(nil, 4), true, true},
		{"malformed self destroy", opcode.SMSGDestroyObject, binary.LittleEndian.AppendUint64(nil, 1), false, false},
		{"malformed transfer", opcode.SMSGTransferPending, []byte{1}, false, false},
		{"malformed world", opcode.SMSGNewWorld, login()[:17], false, false},
		{"malformed login", opcode.SMSGLoginVerifyWorld, login()[:12], false, false},
		{"unreadable aura subject", opcode.SMSGAuraUpdate, []byte{0xff}, false, false},
		{"malformed object batch", opcode.SMSGUpdateObject, []byte{1, 0, 0, 0, 255}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewService(1)
			packet(t, s, opcode.SMSGLoginVerifyWorld, login())
			for _, id := range []uint64{first, other} {
				packet(t, s, opcode.SMSGUpdateObject, objectFixture(id, 3, npcFields(0)))
				packet(t, s, opcode.SMSGAuraUpdateAll, packedFixture(id))
			}
			snap := packet(t, s, tc.op, tc.data)
			if snap.MalformedPackets != 1 {
				t.Fatalf("did not reach malformed callback: %v", snap)
			}
			for i, id := range []uint64{first, other} {
				snap, err := s.Snapshot(t.Context(), &worldv1.SnapshotRequest{CreatureGuid: id})
				want := []bool{tc.firstKnown, tc.otherKnown}[i]
				if err != nil || snap.GetRequestedCreature().GetVisibleAuras().GetComplete() != want {
					t.Fatalf("wrong lifetime for %d: %v %v", id, snap.GetRequestedCreature().GetVisibleAuras(), err)
				}
			}
		})
	}
}
