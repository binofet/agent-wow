package buddyworld

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const guardThomasObjectGUID uint64 = 0xf110000037000017

const lostGuardGUID uint64 = 0xf110000038000029

func TestLostGuardObjectsRemainExactTypedQuestObjects(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(guardThomasObjectGUID, map[int]uint32{objectEntry: 55, gameObjectBytes: 2<<8 | 1}))
	r := packet(t, s, opcode.SMSGCompressedUpdateObject, compressed(t, gameObjectFixture(lostGuardGUID, map[int]uint32{objectEntry: 56, gameObjectBytes: 2<<8 | 1, gameObjectFlags: 4})))
	if len(r.QuestObjects) != 2 || r.MalformedPackets != 0 || len(r.Mailboxes) != 0 {
		t.Fatal(r)
	}
	for _, p := range r.QuestObjects {
		if p.Entry != 55 && p.Entry != 56 || !p.Present || !p.MapKnown || !p.PositionKnown || !p.TypeKnown || p.GameObjectType != 2 || p.X != 1 || p.Y != 2 || p.Z != 3 || !p.FlagsKnown || !p.DynamicFlagsKnown {
			t.Fatal("invalid actual lost-guard object", p)
		}
		if p.Entry == 56 {
			p.Entry = 0
			p.Flags = 99
		}
	}
	r, _ = s.Snapshot(context.Background(), nil)
	if r.QuestObjects[1].Entry != 56 || r.QuestObjects[1].Flags != 4 {
		t.Fatal("snapshot shared lost-guard state", r)
	}
	// Ordinary prop population must not evict either protected quest-object kind.
	for i := uint64(1); i <= 160; i++ {
		entry := uint32(200000 + i)
		guid := uint64(0xf110000000000000) | uint64(entry)<<24 | i
		r = packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(guid, map[int]uint32{objectEntry: entry, gameObjectBytes: 2 << 8}))
	}
	if len(s.state.gameObjects) > maxGameObjects || len(r.QuestObjects) != 2 {
		t.Fatal("lost protected quest objects", r)
	}
}

func TestLostGuardObjectsRequiresExactEntryTypeAndGameObjectIdentity(t *testing.T) {
	for _, test := range []struct {
		name        string
		guid        uint64
		entry, kind uint32
	}{
		{"other_entry", lostGuardGUID, 57, 2 << 8},
		{"mailbox_type", lostGuardGUID, 56, 19 << 8},
		{"state_byte_is_not_type", lostGuardGUID, 56, 2},
		{"creature_guid", 0xf130000038000029, 56, 2 << 8},
		{"entry_guid_mismatch", guardThomasObjectGUID, 56, 2 << 8},
		{"zero_low_guid", 0xf110000038000000, 56, 2 << 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := NewService(1)
			packet(t, s, opcode.SMSGLoginVerifyWorld, login())
			r := packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(test.guid, map[int]uint32{objectEntry: test.entry, gameObjectBytes: test.kind}))
			if len(r.QuestObjects) != 0 {
				t.Fatal("invalid lost-guard exposed", r)
			}
		})
	}
}

func TestLostGuardObjectsUsesExistingAtomicAndRemovalLifecycle(t *testing.T) {
	for _, mode := range []string{"destroy", "outofrange", "transfer", "newworld"} {
		t.Run(mode, func(t *testing.T) {
			s := NewService(1)
			packet(t, s, opcode.SMSGLoginVerifyWorld, login())
			create := gameObjectFixture(lostGuardGUID, map[int]uint32{objectEntry: 56, gameObjectBytes: 2 << 8})
			for n := 0; n < len(create); n++ {
				packet(t, s, opcode.SMSGUpdateObject, create[:n])
			}
			if r, _ := s.Snapshot(context.Background(), nil); len(r.QuestObjects) != 0 {
				t.Fatal("partial lost-guard CREATE admitted", r)
			}
			packet(t, s, opcode.SMSGUpdateObject, create)
			packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(guardThomasObjectGUID, map[int]uint32{objectEntry: 55, gameObjectBytes: 2 << 8}))
			malformed := append(deltaFixture(lostGuardGUID, map[int]uint32{objectEntry: 57}), 255)
			binary.LittleEndian.PutUint32(malformed, 2)
			if r := packet(t, s, opcode.SMSGUpdateObject, malformed); len(r.QuestObjects) != 2 {
				t.Fatal("malformed packet changed valid objects", r)
			}
			var body bytes.Buffer
			switch mode {
			case "destroy":
				binary.Write(&body, binary.LittleEndian, lostGuardGUID)
				body.WriteByte(0)
				packet(t, s, opcode.SMSGDestroyObject, body.Bytes())
			case "outofrange":
				u32(&body, 1)
				body.WriteByte(4)
				u32(&body, 1)
				body.Write(packedFixture(lostGuardGUID))
				packet(t, s, opcode.SMSGUpdateObject, body.Bytes())
			case "transfer":
				packet(t, s, opcode.SMSGTransferPending, []byte{0, 0, 0, 0})
			case "newworld":
				packet(t, s, opcode.SMSGNewWorld, login())
			}
			r := packet(t, s, opcode.SMSGUpdateObject, deltaFixture(lostGuardGUID, map[int]uint32{objectEntry: 56, gameObjectBytes: 2 << 8}))
			for _, p := range r.QuestObjects {
				if p.Guid == lostGuardGUID {
					t.Fatal("removed lost-guard reappeared from a delta", r)
				}
			}
			if (mode == "destroy" || mode == "outofrange") && (len(r.QuestObjects) != 1 || r.QuestObjects[0].Guid != guardThomasObjectGUID) {
				t.Fatal("lost-guard removal changed other guard object", r)
			}
			if (mode == "transfer" || mode == "newworld") && (len(r.QuestObjects) != 0 || len(s.state.gameObjects) != 0) {
				t.Fatal("transfer retained quest objects", r)
			}
		})
	}
}
