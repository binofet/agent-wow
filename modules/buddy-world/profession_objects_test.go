package buddyworld

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"sync"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const bobberGUID uint64 = 0xf110008b0700002a
const focusGUID uint64 = 0xf11000007b00002b

// Literal public CREATE: GO entry35591, createdBy1, flags0 and bytes1=0x1101
// (state READY1/type FISHINGNODE17). The zero creator high half is omitted.
const ownBobberGolden = "0100000003d92a078b10f1055003000000803f00000040000040400000803f00000040000040400000003f000000002a00000700000000000000000148020200078b0000010000000000000001110000"

func bobberFields(self uint64) map[int]uint32 {
	return map[int]uint32{objectEntry: 35591, gameObjectCreatedBy: uint32(self), gameObjectCreatedBy + 1: uint32(self >> 32), gameObjectBytes: 17<<8 | 1}
}

func TestOwnBobberLiteralFullCreateAndCompressedOwnership(t *testing.T) {
	golden, err := hex.DecodeString(ownBobberGolden)
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
		if r.MalformedPackets != 0 || len(r.OwnFishingBobbers) != 1 || len(r.Mailboxes) != 0 || len(r.QuestObjects) != 0 || len(r.SpellFocuses) != 0 {
			t.Fatal(r)
		}
		p := r.OwnFishingBobbers[0]
		if p.Guid != bobberGUID || p.Entry != 35591 || !p.CreatedByKnown || p.CreatedByGuid != 1 || !p.StateKnown || p.State != 1 || p.GameObjectType != 17 || !p.FlagsKnown || p.Flags != 0 || !p.DynamicFlagsKnown || !p.PositionKnown || !p.MapKnown || p.X != 1 || p.Y != 2 || p.Z != 3 {
			t.Fatal(p)
		}
		p.CreatedByGuid, p.State = 3, 0
		r, _ = s.Snapshot(context.Background(), nil)
		if r.OwnFishingBobbers[0].CreatedByGuid != 1 || r.OwnFishingBobbers[0].State != 1 {
			t.Fatal("snapshot modified observer ownership/state")
		}
	}
}

func TestSpellFocusesAreActualTypeEightObjectsWithoutInventedTemplateMetadata(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	r := packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(focusGUID, map[int]uint32{objectEntry: 123, gameObjectBytes: 8 << 8}))
	if len(r.SpellFocuses) != 1 || len(r.QuestObjects) != 0 || len(r.Mailboxes) != 0 || len(r.OwnFishingBobbers) != 0 {
		t.Fatal(r)
	}
	p := r.SpellFocuses[0]
	if p.GameObjectType != 8 || p.Entry != 123 || !p.StateKnown || p.State != 0 || !p.CreatedByKnown || p.CreatedByGuid != 0 || !p.PositionKnown || !p.FlagsKnown {
		t.Fatal("full CREATE zero omission knowledge lost", p)
	}
	// No focusID is invented from object type8; the consumer must normally
	// query this entry's template and independently verify cooking focus4.
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(focusGUID, map[int]uint32{gameObjectBytes: 8}))
	if len(r.SpellFocuses) != 0 {
		t.Fatal("state8 was mistaken for type8")
	}
}

func TestBobberTypeEntryCreatorAndIndependentSelfMustAllMatch(t *testing.T) {
	for _, mode := range []string{"foreign", "no_owner", "wrong_type", "wrong_entry", "guid_entry", "partial"} {
		t.Run(mode, func(t *testing.T) {
			s := NewService(1)
			fields, guid := bobberFields(1), bobberGUID
			switch mode {
			case "foreign":
				fields[gameObjectCreatedBy] = 3
			case "no_owner":
				delete(fields, gameObjectCreatedBy)
			case "wrong_type":
				fields[gameObjectBytes] = 3<<8 | 1
			case "wrong_entry":
				fields[objectEntry] = 35592
			case "guid_entry":
				guid = focusGUID
			}
			data := gameObjectFixture(guid, fields)
			if mode == "partial" {
				data = deltaFixture(guid, fields)
			}
			if r := packet(t, s, opcode.SMSGUpdateObject, data); len(r.OwnFishingBobbers) != 0 {
				t.Fatal("foreign/unproven bobber became own", mode, r)
			}
		})
	}
	s := NewIndependentService(6)
	packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(bobberGUID, bobberFields(3)))
	if r := packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(bobberGUID+1, bobberFields(6))); r.Owner != nil || len(r.OwnFishingBobbers) != 1 || r.OwnFishingBobbers[0].CreatedByGuid != 6 {
		t.Fatal("independent observer leaked owner/fellow bobber", r)
	}
}

func TestOwnChannelNeedsObservedHalvesAndFullCreateKnownZero(t *testing.T) {
	s := NewService(1)
	r := packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	if r.Self.ChannelObjectKnown || r.Self.ChannelSpellKnown {
		t.Fatal("login position invented channel state")
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitChannelObject: uint32(bobberGUID & 0xffffffff), unitChannelSpell: 7620}))
	if r.Self.ChannelObjectKnown || !r.Self.ChannelSpellKnown || r.Self.ChannelSpellId != 7620 {
		t.Fatal("partial channel GUID became fully known", r.Self)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	if !r.Self.ChannelObjectKnown || r.Self.ChannelObjectGuid != 0 || !r.Self.ChannelSpellKnown || r.Self.ChannelSpellId != 0 {
		t.Fatal("valid self CREATE didn't baseline omitted channel to zero", r.Self)
	}
	fields := map[int]uint32{unitChannelObject: uint32(bobberGUID & 0xffffffff), unitChannelObject + 1: uint32(bobberGUID >> 32), unitChannelSpell: 7620}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, fields))
	if !r.Self.ChannelObjectKnown || r.Self.ChannelObjectGuid != bobberGUID || !r.Self.ChannelSpellKnown || r.Self.ChannelSpellId != 7620 {
		t.Fatal(r.Self)
	}
	packet(t, s, opcode.SMSGUpdateObject, createFixture(3, fields))
	r, _ = s.Snapshot(context.Background(), nil)
	if r.Owner.ChannelObjectKnown || r.Owner.ChannelObjectGuid != 0 || r.Owner.ChannelSpellKnown || r.Owner.ChannelSpellId != 0 || r.Self.ChannelObjectGuid != bobberGUID {
		t.Fatal("foreign public fields leaked into own channel view", r)
	}
	fields[unitChannelObject], fields[unitChannelObject+1], fields[unitChannelSpell] = 0, 0, 0
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, fields))
	if !r.Self.ChannelObjectKnown || r.Self.ChannelObjectGuid != 0 || !r.Self.ChannelSpellKnown || r.Self.ChannelSpellId != 0 {
		t.Fatal("observed channel stop was not retained", r.Self)
	}
}

func TestProfessionObjectStateTransitionsAndLifetime(t *testing.T) {
	for _, mode := range []string{"destroy", "out_of_range", "transfer", "new_world"} {
		t.Run(mode, func(t *testing.T) {
			s := NewService(1)
			packet(t, s, opcode.SMSGLoginVerifyWorld, login())
			fields := vitalFields()
			fields[unitChannelObject], fields[unitChannelObject+1], fields[unitChannelSpell] = uint32(bobberGUID&0xffffffff), uint32(bobberGUID>>32), 7620
			packet(t, s, opcode.SMSGUpdateObject, createFixture(1, fields))
			packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(bobberGUID, bobberFields(1)))
			packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(focusGUID, map[int]uint32{objectEntry: 123, gameObjectBytes: 8 << 8}))
			r := packet(t, s, opcode.SMSGUpdateObject, deltaFixture(bobberGUID, map[int]uint32{gameObjectBytes: 17 << 8, gameObjectFlags: 0x20}))
			if len(r.OwnFishingBobbers) != 1 || r.OwnFishingBobbers[0].State != 0 || r.OwnFishingBobbers[0].Flags != 0x20 {
				t.Fatal("actual bobber-ready fields were lost", r)
			}
			switch mode {
			case "destroy":
				packet(t, s, opcode.SMSGDestroyObject, append(binary.LittleEndian.AppendUint64(nil, bobberGUID), 0))
				packet(t, s, opcode.SMSGDestroyObject, append(binary.LittleEndian.AppendUint64(nil, focusGUID), 0))
			case "out_of_range":
				b := new(bytes.Buffer)
				u32(b, 1)
				b.WriteByte(4)
				u32(b, 2)
				b.Write(packedFixture(bobberGUID))
				b.Write(packedFixture(focusGUID))
				packet(t, s, opcode.SMSGUpdateObject, b.Bytes())
			case "transfer":
				packet(t, s, opcode.SMSGTransferPending, []byte{0, 0, 0, 0})
			case "new_world":
				packet(t, s, opcode.SMSGNewWorld, login())
			}
			r, _ = s.Snapshot(context.Background(), nil)
			if len(r.OwnFishingBobbers) != 0 || len(r.SpellFocuses) != 0 {
				t.Fatal("removed profession object remained usable", mode, r)
			}
			if (mode == "transfer" || mode == "new_world") && (r.Self.ChannelObjectKnown || r.Self.ChannelSpellKnown || r.Self.ChannelObjectGuid != 0 || r.Self.ChannelSpellId != 0) {
				t.Fatal("world change retained a stale channel", mode, r.Self)
			}
		})
	}
}

func TestProfessionObjectsAndChannelRollbackWholeMalformedPacket(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	golden, _ := hex.DecodeString(ownBobberGolden)
	for n := range len(golden) {
		packet(t, s, opcode.SMSGUpdateObject, golden[:n])
	}
	if len(s.state.gameObjects) != 0 {
		t.Fatal("truncated bobber CREATE entered cache")
	}
	packet(t, s, opcode.SMSGUpdateObject, golden)
	data := deltaFixture(1, map[int]uint32{unitChannelObject: uint32(bobberGUID & 0xffffffff), unitChannelObject + 1: uint32(bobberGUID >> 32), unitChannelSpell: 7620})
	data = append(data, deltaFixture(bobberGUID, map[int]uint32{gameObjectBytes: 17 << 8, gameObjectFlags: 0x20})[4:]...)
	data = append(data, 255)
	binary.LittleEndian.PutUint32(data, 3)
	r := packet(t, s, opcode.SMSGUpdateObject, data)
	if r.Self.ChannelObjectGuid != 0 || r.Self.ChannelSpellId != 0 || len(r.OwnFishingBobbers) != 1 || r.OwnFishingBobbers[0].State != 1 || r.OwnFishingBobbers[0].Flags != 0 {
		t.Fatal("malformed tail committed a partial channel/bite", r)
	}
}

func TestProfessionObjectCachesAreBoundedAndOwnBobberSurvivesProps(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(bobberGUID, bobberFields(1)))
	for i := uint64(1); i < 200; i++ {
		packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(focusGUID+i, map[int]uint32{objectEntry: 123, gameObjectBytes: 8 << 8}))
		packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(bobberGUID+i, bobberFields(3)))
	}
	r, _ := s.Snapshot(context.Background(), nil)
	if len(s.state.gameObjects) != maxGameObjects || len(r.OwnFishingBobbers) != 1 || r.OwnFishingBobbers[0].Guid != bobberGUID || len(r.SpellFocuses) != 32 {
		t.Fatal("foreign/ordinary objects evicted own bobber or escaped limits", r)
	}
	for i := uint64(1); i < 10; i++ {
		packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(bobberGUID+1000+i, bobberFields(1)))
	}
	r, _ = s.Snapshot(context.Background(), nil)
	if len(r.OwnFishingBobbers) != 4 || len(r.SpellFocuses) > 32 || len(s.state.gameObjects) > maxGameObjects {
		t.Fatal("profession object output bounds escaped", r)
	}
	var work sync.WaitGroup
	for range 3 {
		work.Add(1)
		go func() {
			defer work.Done()
			for range 10 {
				r, _ := s.Snapshot(context.Background(), nil)
				r.OwnFishingBobbers[0].CreatedByGuid = 3
				r.SpellFocuses[0].State = 255
				packet(t, s, opcode.SMSGUpdateObject, deltaFixture(bobberGUID, map[int]uint32{gameObjectFlags: 0x20}))
			}
		}()
	}
	work.Wait()
	r, _ = s.Snapshot(context.Background(), nil)
	if r.OwnFishingBobbers[0].CreatedByGuid != 1 || r.SpellFocuses[0].State != 0 {
		t.Fatal("concurrent snapshots mutated public observations", r)
	}
}
