package buddyworld

import (
	"context"
	"encoding/binary"
	"sync"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func skillFields() map[int]uint32 {
	f := vitalFields()
	// Public UpdateFields: 0x27c skillID/step, next value/max and signed bonuses.
	f[0x27c], f[0x27d], f[0x27e] = 197|1<<16, 1|75<<16, 0xfffe|3<<16
	f[0x3fd] = 2
	return f
}

func TestOwnSkillsRequireFullCreateAndRetainExactPackedFields(t *testing.T) {
	s := NewService(1)
	r := packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	if r.SelfSkillsKnown || len(r.SelfSkills) != 0 || r.SelfFreePrimarySlotsKnown {
		t.Fatal("position invented skill knowledge")
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, skillFields()))
	if r.SelfSkillsKnown || len(r.SelfSkills) != 0 || !r.SelfFreePrimarySlotsKnown || r.SelfFreePrimarySlots != 2 {
		t.Fatal("partial values established complete skill baseline", r)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, createFixture(1, skillFields()))
	if !r.SelfSkillsKnown || len(r.SelfSkills) != 128 || !r.SelfFreePrimarySlotsKnown || r.SelfFreePrimarySlots != 2 {
		t.Fatal(r)
	}
	first := r.SelfSkills[0]
	if first.Slot != 0 || first.SkillId != 197 || first.Step != 1 || first.Value != 1 || first.MaxValue != 75 || first.TemporaryBonus != -2 || first.PermanentBonus != 3 {
		t.Fatal(first)
	}
	for i, skill := range r.SelfSkills[1:] {
		if skill.Slot != uint32(i+1) || skill.SkillId != 0 || skill.Value != 0 || skill.MaxValue != 0 {
			t.Fatal("omitted create skill fields were not known zero", skill)
		}
	}
}

func TestOwnSkillRevisionsAreWholePacketChangesNotReconnectOrUnrelatedFields(t *testing.T) {
	s := NewService(1)
	r := packet(t, s, opcode.SMSGUpdateObject, createFixture(1, skillFields()))
	if r.SelfSkillsRevision != 1 || r.SelfFreePrimarySlotsRevision != 1 {
		t.Fatal(r)
	}
	for _, data := range [][]byte{createFixture(1, skillFields()), deltaFixture(1, map[int]uint32{0x27c: 197 | 1<<16, 0x27d: 1 | 75<<16, 0x3fd: 2, unitHealth: 45})} {
		r = packet(t, s, opcode.SMSGUpdateObject, data)
		if r.SelfSkillsRevision != 1 || r.SelfFreePrimarySlotsRevision != 1 {
			t.Fatal("identical reconnect or unrelated field changed revision", r)
		}
	}
	r = packet(t, s, opcode.SMSGCompressedUpdateObject, compressed(t, deltaFixture(1, map[int]uint32{0x27c: 197 | 2<<16, 0x27d: 2 | 150<<16, 0x27e: 0x8000 | 0xffff0000, 0x3fd: 1})))
	if r.SelfSkillsRevision != 2 || r.SelfFreePrimarySlotsRevision != 2 || r.SelfFreePrimarySlots != 1 || r.SelfSkills[0].Step != 2 || r.SelfSkills[0].Value != 2 || r.SelfSkills[0].MaxValue != 150 || r.SelfSkills[0].TemporaryBonus != -32768 || r.SelfSkills[0].PermanentBonus != -1 {
		t.Fatal(r)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	if r.SelfSkillsRevision != 3 || r.SelfFreePrimarySlotsRevision != 3 || r.SelfSkills[0].SkillId != 0 || r.SelfFreePrimarySlots != 0 {
		t.Fatal("new full create did not clear removed skills/points", r)
	}
}

func TestSkillsAndFreePrimaryIgnoreForeignAndMalformedWholePackets(t *testing.T) {
	s := NewService(1)
	fields := skillFields()
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, fields))
	for _, guid := range []uint64{2, 3} {
		packet(t, s, opcode.SMSGUpdateObject, createFixture(byte(guid), vitalFields()))
		packet(t, s, opcode.SMSGUpdateObject, deltaFixture(guid, map[int]uint32{0x27c: 333, 0x27d: 450 | 450<<16, 0x3fd: 0}))
	}
	delta := deltaFixture(1, map[int]uint32{0x27c: 333, 0x27d: 450 | 450<<16, 0x3fd: 0})
	for n := range len(delta) {
		packet(t, s, opcode.SMSGUpdateObject, delta[:n])
	}
	packet(t, s, opcode.SMSGUpdateObject, append(append([]byte(nil), delta...), 255))
	// Valid first block plus a malformed second block must roll back both arrays.
	combined := append([]byte(nil), delta...)
	binary.LittleEndian.PutUint32(combined, 2)
	combined = append(combined, 0)
	packet(t, s, opcode.SMSGUpdateObject, combined)
	r, _ := s.Snapshot(context.Background(), nil)
	if !r.SelfSkillsKnown || r.SelfSkillsRevision != 1 || r.SelfSkills[0].SkillId != 197 || r.SelfSkills[0].Value != 1 || r.SelfFreePrimarySlots != 2 || r.SelfFreePrimarySlotsRevision != 1 {
		t.Fatal("foreign or malformed packet changed skills", r)
	}
	unknown := NewService(1)
	create := createFixture(1, fields)
	for n := range len(create) {
		packet(t, unknown, opcode.SMSGUpdateObject, create[:n])
	}
	u, _ := unknown.Snapshot(context.Background(), nil)
	if u.SelfSkillsKnown || u.SelfFreePrimarySlotsKnown || u.SelfSkillsRevision != 0 || len(u.SelfSkills) != 0 {
		t.Fatal("malformed create established a baseline", u)
	}
}

func TestIndependentSkillsObserveOnlyActualSelfIncludingLastWireSlot(t *testing.T) {
	s := NewIndependentService(6)
	f := skillFields()
	f[0x27c+127*3], f[0x27c+127*3+1] = 773|1<<16, 1|75<<16
	r := packet(t, s, opcode.SMSGUpdateObject, createFixture(6, f))
	if !r.SelfSkillsKnown || r.SelfSkills[127].Slot != 127 || r.SelfSkills[127].SkillId != 773 || r.Owner != nil {
		t.Fatal(r)
	}
	packet(t, s, opcode.SMSGUpdateObject, createFixture(3, skillFields()))
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(3, map[int]uint32{0x27c: 333, 0x3fd: 0}))
	if r.SelfSkillsRevision != 1 || r.SelfSkills[0].SkillId != 197 || r.SelfFreePrimarySlots != 2 || r.Owner != nil {
		t.Fatal("independent character retained owner skills", r)
	}
}

func TestSkillSnapshotsArePrivateDuringConcurrentUpdates(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, skillFields()))
	var work sync.WaitGroup
	for i := range 4 {
		work.Add(1)
		go func(n int) {
			defer work.Done()
			for j := range 30 {
				r, err := s.Snapshot(context.Background(), nil)
				if err != nil || !r.SelfSkillsKnown || len(r.SelfSkills) != 128 {
					t.Error("missing complete snapshot", err)
					return
				}
				r.SelfSkills[0].SkillId = 999
				packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x27d: uint32(n+j+1) | 150<<16}))
			}
		}(i)
	}
	work.Wait()
	r, _ := s.Snapshot(context.Background(), nil)
	if r.SelfSkills[0].SkillId != 197 {
		t.Fatal("caller mutated skill cache")
	}
}
