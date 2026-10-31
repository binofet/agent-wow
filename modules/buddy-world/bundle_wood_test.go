package buddyworld

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const bundleWoodGUID uint64 = 0xf11002b299000029

func TestBundleWoodAndMillyCratesRemainExactTypedQuestObjects(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(harvestGUID, map[int]uint32{objectEntry: 161557, gameObjectBytes: 3<<8 | 1}))
	r := packet(t, s, opcode.SMSGCompressedUpdateObject, compressed(t, gameObjectFixture(bundleWoodGUID, map[int]uint32{objectEntry: 176793, gameObjectBytes: 3<<8 | 1, gameObjectFlags: 4})))
	if len(r.QuestObjects) != 2 || r.MalformedPackets != 0 || len(r.Mailboxes) != 0 {
		t.Fatal(r)
	}
	for _, p := range r.QuestObjects {
		if p.Entry != 161557 && p.Entry != 176793 || !p.Present || !p.MapKnown || !p.PositionKnown || !p.TypeKnown || p.GameObjectType != 3 || p.X != 1 || p.Y != 2 || p.Z != 3 || !p.FlagsKnown || !p.DynamicFlagsKnown {
			t.Fatal("invalid actual quest object", p)
		}
		if p.Entry == 176793 {
			p.Entry = 0
			p.Flags = 99
		}
	}
	r, _ = s.Snapshot(context.Background(), nil)
	if r.QuestObjects[1].Entry != 176793 || r.QuestObjects[1].Flags != 4 {
		t.Fatal("snapshot shared bundle state", r)
	}
	// Ordinary prop population must not evict either protected quest-object kind.
	for i := uint64(1); i <= 160; i++ {
		entry := uint32(200000 + i)
		guid := uint64(0xf110000000000000) | uint64(entry)<<24 | i
		r = packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(guid, map[int]uint32{objectEntry: entry, gameObjectBytes: 3 << 8}))
	}
	if len(s.state.gameObjects) > maxGameObjects || len(r.QuestObjects) != 2 {
		t.Fatal("lost protected quest objects", r)
	}
}

func TestBundleWoodRequiresExactEntryTypeAndGameObjectIdentity(t *testing.T) {
	for _, test := range []struct {
		name        string
		guid        uint64
		entry, kind uint32
	}{
		{"other_entry", bundleWoodGUID, 176794, 3 << 8},
		{"mailbox_type", bundleWoodGUID, 176793, 19 << 8},
		{"state_byte_is_not_type", bundleWoodGUID, 176793, 3},
		{"creature_guid", 0xf13002b299000029, 176793, 3 << 8},
		{"entry_guid_mismatch", harvestGUID, 176793, 3 << 8},
		{"zero_low_guid", 0xf11002b299000000, 176793, 3 << 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := NewService(1)
			packet(t, s, opcode.SMSGLoginVerifyWorld, login())
			r := packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(test.guid, map[int]uint32{objectEntry: test.entry, gameObjectBytes: test.kind}))
			if len(r.QuestObjects) != 0 {
				t.Fatal("invalid bundle exposed", r)
			}
		})
	}
}

func TestBundleWoodUsesExistingAtomicAndRemovalLifecycle(t *testing.T) {
	for _, mode := range []string{"destroy", "outofrange", "transfer", "newworld"} {
		t.Run(mode, func(t *testing.T) {
			s := NewService(1)
			packet(t, s, opcode.SMSGLoginVerifyWorld, login())
			create := gameObjectFixture(bundleWoodGUID, map[int]uint32{objectEntry: 176793, gameObjectBytes: 3 << 8})
			for n := 0; n < len(create); n++ {
				packet(t, s, opcode.SMSGUpdateObject, create[:n])
			}
			if r, _ := s.Snapshot(context.Background(), nil); len(r.QuestObjects) != 0 {
				t.Fatal("partial bundle CREATE admitted", r)
			}
			packet(t, s, opcode.SMSGUpdateObject, create)
			packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(harvestGUID, map[int]uint32{objectEntry: 161557, gameObjectBytes: 3 << 8}))
			malformed := append(deltaFixture(bundleWoodGUID, map[int]uint32{objectEntry: 176794}), 255)
			binary.LittleEndian.PutUint32(malformed, 2)
			if r := packet(t, s, opcode.SMSGUpdateObject, malformed); len(r.QuestObjects) != 2 {
				t.Fatal("malformed packet changed valid objects", r)
			}
			var body bytes.Buffer
			switch mode {
			case "destroy":
				binary.Write(&body, binary.LittleEndian, bundleWoodGUID)
				body.WriteByte(0)
				packet(t, s, opcode.SMSGDestroyObject, body.Bytes())
			case "outofrange":
				u32(&body, 1)
				body.WriteByte(4)
				u32(&body, 1)
				body.Write(packedFixture(bundleWoodGUID))
				packet(t, s, opcode.SMSGUpdateObject, body.Bytes())
			case "transfer":
				packet(t, s, opcode.SMSGTransferPending, []byte{0, 0, 0, 0})
			case "newworld":
				packet(t, s, opcode.SMSGNewWorld, login())
			}
			r := packet(t, s, opcode.SMSGUpdateObject, deltaFixture(bundleWoodGUID, map[int]uint32{objectEntry: 176793, gameObjectBytes: 3 << 8}))
			for _, p := range r.QuestObjects {
				if p.Guid == bundleWoodGUID {
					t.Fatal("removed bundle reappeared from a delta", r)
				}
			}
			if (mode == "destroy" || mode == "outofrange") && (len(r.QuestObjects) != 1 || r.QuestObjects[0].Guid != harvestGUID) {
				t.Fatal("bundle removal changed Milly crate", r)
			}
			if (mode == "transfer" || mode == "newworld") && (len(r.QuestObjects) != 0 || len(s.state.gameObjects) != 0) {
				t.Fatal("transfer retained quest objects", r)
			}
		})
	}
}
