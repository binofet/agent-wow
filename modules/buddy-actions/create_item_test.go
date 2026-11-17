package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/types/known/emptypb"
)

func itemCreationFixture(t *testing.T) (*Service, *fakeSession, func(time.Duration), *actionsv1.CreateItemRequest) {
	t.Helper()
	s, f, advance := rig(t)
	s.sessionID = "creation-test-body"
	s.applySpellbookLocked(map[uint32]bool{5504: true, 5505: true, 118: true})
	f.world.Self.ClassId = 8
	f.world.InventoryKnown, f.world.InventoryRevision = true, 7
	for i := range 5 {
		bag := &worldv1.InventoryBag{Bag: uint32(i), SlotKnown: true, ContentsKnown: true}
		f.world.InventoryBags = append(f.world.InventoryBags, bag)
	}
	backpack := f.world.InventoryBags[0]
	backpack.SlotCount = 16
	for i := range 16 {
		backpack.Slots = append(backpack.Slots, &worldv1.BackpackSlot{Slot: uint32(i)})
	}
	backpack.Slots[2] = &worldv1.BackpackSlot{Slot: 2, ItemGuid: 0x4000000000000211, Entry: 2288, StackCount: 3, MetadataKnown: true}
	return s, f, advance, &actionsv1.CreateItemRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, SpellId: 5505}
}

func creationGo(s *Service, spell uint32) []byte {
	p := append(packedGUID(s.selfGUID), packedGUID(s.selfGUID)...)
	p = append(p, s.castCount)
	p = binary.LittleEndian.AppendUint32(p, spell)
	p = binary.LittleEndian.AppendUint32(p, 0x100)
	p = binary.LittleEndian.AppendUint32(p, 0)
	p = append(p, 1)
	p = binary.LittleEndian.AppendUint64(p, s.selfGUID)
	p = append(p, 0)
	p = binary.LittleEndian.AppendUint32(p, 0) // implicit self
	return p
}

func creationPush(actor uint64, created, entry, added, total uint32) []byte {
	p := make([]byte, 45)
	binary.LittleEndian.PutUint64(p, actor)
	binary.LittleEndian.PutUint32(p[8:], 1)
	binary.LittleEndian.PutUint32(p[12:], created)
	binary.LittleEndian.PutUint32(p[16:], 1)
	p[20] = 0xff
	binary.LittleEndian.PutUint32(p[21:], 0xffffffff)
	binary.LittleEndian.PutUint32(p[25:], entry)
	binary.LittleEndian.PutUint32(p[37:], added)
	binary.LittleEndian.PutUint32(p[41:], total)
	return p
}

func TestCreateItemNeedsRankExactGoCreatedPushAndNewOwnBag(t *testing.T) {
	for _, pushFirst := range []bool{false, true} {
		s, f, _, req := itemCreationFixture(t)
		ctx := context.Background()
		toolkit, err := s.SpellToolkit(ctx, &emptypb.Empty{})
		if err != nil {
			t.Fatal(err)
		}
		entries := map[uint32]uint32{}
		for _, spell := range toolkit.Spells {
			if spell.Capability == "create_item" {
				entries[spell.Rank] = spell.CreatedItemEntry
			}
		}
		if len(entries) != 2 || entries[1] != 5350 || entries[2] != 2288 {
			t.Fatal("learned rank outputs", entries)
		}
		st, err := s.CreateItem(ctx, req)
		if err != nil || !st.CastPending || !st.LastSpellOperation.TransportWritten ||
			st.LastSpellOperation.CreatedItemEntry != 2288 || st.LastSpellOperation.BaselineInventoryRevision != 7 {
			t.Fatal(st, err)
		}
		got := f.packets[0]
		target, _, _ := readGUID(got.Payload[10:])
		if len(f.packets) != 1 || got.Opcode != opcode.CMSGCastSpell || binary.LittleEndian.Uint32(got.Payload[1:]) != 5505 || target != s.selfGUID {
			t.Fatal("expected one self-cast of exact rank", got)
		}
		emit := func(op uint32, payload []byte) {
			_, err := s.OnPacket(ctx, &modv1.WorldPacket{Opcode: op, Payload: payload})
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, push := range [][]byte{
			creationPush(3, 1, 2288, 6, 9), creationPush(1, 0, 2288, 6, 9),
			creationPush(1, 1, 5350, 6, 9), creationPush(1, 1, 2288, 6, 10),
		} {
			emit(opcode.SMSGItemPushResult, push)
		}
		st, _ = s.Status(ctx, &emptypb.Empty{})
		if st.LastSpellOperation.ItemPushObserved {
			t.Fatal("foreign or inexact push accepted", st)
		}
		goEvent := func() { emit(opcode.SMSGSpellGo, creationGo(s, 5505)) }
		pushEvent := func() { emit(opcode.SMSGItemPushResult, creationPush(1, 1, 2288, 6, 9)) }
		if pushFirst {
			pushEvent()
		}
		goEvent()
		if !pushFirst {
			pushEvent()
		}
		st, _ = s.Status(ctx, &emptypb.Empty{})
		if !st.CastPending || !st.LastSpellOperation.ServerCastGo || !st.LastSpellOperation.ItemPushObserved ||
			st.LastSpellOperation.EffectObserved || st.LastSpellOperation.Phase != "waiting_created_item" {
			t.Fatal("claimed creation before a newer exact bag", st)
		}
		f.world.InventoryRevision++
		f.world.InventoryBags[0].Slots[2].StackCount = 8
		st, _ = s.Status(ctx, &emptypb.Empty{})
		if st.LastSpellOperation.EffectObserved {
			t.Fatal("wrong bag count accepted", st)
		}
		f.world.InventoryBags[0].Slots[2].StackCount = 9
		f.world.InventoryRevision++
		st, _ = s.Status(ctx, &emptypb.Empty{})
		if st.CastPending || !st.LastSpellOperation.EffectObserved || !st.LastSpellOperation.InventoryConfirmed ||
			st.LastSpellOperation.CreatedItemCount != 6 || st.LastSpellOperation.ObservedInventoryRevision != 9 ||
			st.LastSpellOperation.Phase != "confirmed_item" || len(f.packets) != 1 {
			t.Fatal("exact bag receipt didn't confirm single operation", st, f.packets)
		}
	}
}

func TestCreateItemRefusesWrongRankBodyAndUnsafeOrUnobservedInventory(t *testing.T) {
	for _, tc := range []string{"unlearned", "not creation", "wrong body", "combat", "moving", "mana", "unknown inventory"} {
		t.Run(tc, func(t *testing.T) {
			s, f, _, req := itemCreationFixture(t)
			switch tc {
			case "unlearned":
				req.SpellId = 5506
			case "not creation":
				req.SpellId = 118
			case "wrong body":
				req.SessionId = "old"
			case "combat":
				f.world.Self.UnitFlags = 0x80000
			case "moving":
				f.world.Self.MovementFlags = 1
			case "mana":
				f.world.Self.Power = 2
			case "unknown inventory":
				f.world.InventoryKnown = false
			}
			if _, err := s.CreateItem(context.Background(), req); err == nil || len(f.packets) != 0 {
				t.Fatal("unsafe create dispatch", err, f.packets)
			}
		})
	}
}

func TestCreateItemWaitingReceiptExpiresWithoutRetry(t *testing.T) {
	s, f, advance, req := itemCreationFixture(t)
	ctx := context.Background()
	if _, err := s.CreateItem(ctx, req); err != nil {
		t.Fatal(err)
	}
	_, _ = s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: creationGo(s, 5505)})
	advance(7 * time.Second)
	st, _ := s.Status(ctx, &emptypb.Empty{})
	if st.CastPending || st.LastSpellOperation.Phase != "outcome_unconfirmed_no_retry" ||
		st.LastSpellOperation.EffectObserved || len(f.packets) != 1 {
		t.Fatal(st, f.packets)
	}
}
