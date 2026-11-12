package buddyquests

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

const ownedDeedGUID uint64 = 0x4000000000001972

func deedRig(bag uint32) (*Service, *fakeSession) {
	s, f, _ := guardObjectRig(55)
	f.world.QuestObjects = nil
	f.world.Self.LevelKnown = true
	f.world.Self.Level = 9
	f.world.InventoryKnown = true
	f.world.InventoryRevision = 10
	for n := uint32(0); n < 5; n++ {
		b := &worldv1.InventoryBag{Bag: n, SlotKnown: true, ContentsKnown: true}
		if n == 0 {
			b.SlotCount = 16
		}
		if n == bag && n != 0 {
			b.ContainerGuid = 0x40000000000ba001
			b.Entry = 41599
			b.MetadataKnown = true
			b.SlotCount = 20
		}
		for i := uint32(0); i < b.SlotCount; i++ {
			b.Slots = append(b.Slots, &worldv1.BackpackSlot{Bag: n, Slot: i})
		}
		if n == bag {
			i := uint32(5)
			b.Slots[i] = &worldv1.BackpackSlot{Bag: n, Slot: i, ItemGuid: ownedDeedGUID, Entry: 1972, StackCount: 1, MetadataKnown: true}
		}
		f.world.InventoryBags = append(f.world.InventoryBags, b)
	}
	return s, f
}
func deedDetails(t *testing.T, guid uint64, id uint32) []byte {
	p := unhex(t, detailsHex)
	binary.LittleEndian.PutUint64(p, guid)
	binary.LittleEndian.PutUint32(p[16:], id)
	return p
}
func offeredDeed(t *testing.T, bag uint32) (*Service, *fakeSession, *questsv1.QuestRequest) {
	t.Helper()
	s, f := deedRig(bag)
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode != opcode.CMSGQuestgiverQueryQuest || !bytes.Equal(p.Payload, questPacket(ownedDeedGUID, 184, false)) {
			t.Error("not normal exact item-giver query", p)
		}
		packet(t, s, opcode.SMSGQuestgiverQuestDetails, deedDetails(t, ownedDeedGUID, 184))
		return nil
	}
	out, err := s.Offer(context.Background(), &questsv1.OfferRequest{SessionId: "guard-body", NpcGuid: ownedDeedGUID})
	if err != nil || !out.ServerResponseReceived || out.State.Offer == nil || out.State.Offer.NpcGuid != ownedDeedGUID || out.State.Offer.Quests[0].QuestId != 184 {
		t.Fatal(out, err)
	}
	return s, f, &questsv1.QuestRequest{SessionId: "guard-body", NpcGuid: ownedDeedGUID, QuestId: 184, OfferRevision: out.State.Offer.Revision}
}
func TestOwnedWestfallDeedStartsOnly184FromBackpackAndEquippedBag(t *testing.T) {
	for _, bag := range []uint32{0, 1, 4} {
		s, f, r := offeredDeed(t, bag)
		f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
			if p.Opcode != opcode.CMSGQuestgiverAcceptQuest || !bytes.Equal(p.Payload, questPacket(ownedDeedGUID, 184, true)) {
				t.Error("wrong actual item accept", p)
			}
			f.mu.Lock()
			f.world.SelfQuestLog[0].QuestId = 184
			f.world.SelfQuestLogRevision++
			f.world.InventoryBags[bag].Slots[5] = &worldv1.BackpackSlot{Bag: bag, Slot: 5}
			f.world.InventoryRevision++
			f.mu.Unlock()
			return nil
		}
		out, err := s.Accept(context.Background(), r)
		if err != nil || out.State.LastAcceptance.Status != questsv1.AcceptanceStatus_CONFIRMED_IN_LOG || out.State.LastAcceptance.ConfirmedLogRevision <= out.State.LastAcceptance.BaselineLogRevision {
			t.Fatal(out, err)
		}
		if _, err = s.Accept(context.Background(), r); err == nil || len(f.packets) != 2 {
			t.Fatal("item acceptance repeated", err)
		}
		if _, err = s.TurnIn(context.Background(), &questsv1.TurnInRequest{SessionId: "guard-body", NpcGuid: ownedDeedGUID, QuestId: 184}); err == nil {
			t.Fatal("item became remote reward giver")
		}
		if _, err = decodeTurnIn(objectReward(ownedDeedGUID, 184), true); err == nil {
			t.Fatal("item reward frame accepted")
		}
		if _, _, released, held := f.counters(); released != 2 || held {
			t.Fatal("item leaked lease", released, held)
		}
	}
}
func TestUnownedOrUnprovenQuestItemsNeverAcquireOrWrite(t *testing.T) {
	cases := map[string]func(*fakeSession){
		"unknown level":        func(f *fakeSession) { f.world.Self.LevelKnown = false },
		"level7":               func(f *fakeSession) { f.world.Self.Level = 7 },
		"missing":              func(f *fakeSession) { f.world.InventoryBags[1].Slots[5] = &worldv1.BackpackSlot{Bag: 1, Slot: 5} },
		"delivery item1971":    func(f *fakeSession) { f.world.InventoryBags[1].Slots[5].Entry = 1971 },
		"other item":           func(f *fakeSession) { f.world.InventoryBags[1].Slots[5].Entry = 2722 },
		"unknown metadata":     func(f *fakeSession) { f.world.InventoryBags[1].Slots[5].MetadataKnown = false },
		"unknown inventory":    func(f *fakeSession) { f.world.InventoryKnown = false },
		"missing bag":          func(f *fakeSession) { f.world.InventoryBags = f.world.InventoryBags[:4] },
		"unknown contents":     func(f *fakeSession) { f.world.InventoryBags[1].ContentsKnown = false },
		"unknown bag metadata": func(f *fakeSession) { f.world.InventoryBags[1].MetadataKnown = false },
		"partial layout":       func(f *fakeSession) { f.world.InventoryBags[1].Slots = f.world.InventoryBags[1].Slots[:6] },
		"wrong logical bag":    func(f *fakeSession) { f.world.InventoryBags[1].Slots[5].Bag = 2 },
		"duplicate guid": func(f *fakeSession) {
			p := proto.Clone(f.world.InventoryBags[1].Slots[5]).(*worldv1.BackpackSlot)
			p.Slot = 6
			f.world.InventoryBags[1].Slots[6] = p
		},
		"stack mismatch":  func(f *fakeSession) { f.world.InventoryBags[1].Slots[5].StackCount = 2 },
		"unknown own log": func(f *fakeSession) { f.world.SelfQuestLogKnown = false },
		"already own184":  func(f *fakeSession) { f.world.SelfQuestLog[0].QuestId = 184 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s, f := deedRig(1)
			change(f)
			if _, err := s.Offer(context.Background(), &questsv1.OfferRequest{SessionId: "guard-body", NpcGuid: ownedDeedGUID}); err == nil {
				t.Fatal("invalid quest item accepted")
			}
			if n, a, _, held := f.counters(); n != 0 || a != 0 || held {
				t.Fatal("unowned item affected actor", n, a, held)
			}
		})
	}
	s, f := deedRig(1)
	if _, err := s.Offer(context.Background(), &questsv1.OfferRequest{SessionId: "guard-body", NpcGuid: ownedDeedGUID + 1}); err == nil || len(f.packets) != 0 || f.acquired != 0 {
		t.Fatal("arbitrary item GUID admitted", err)
	}
}
func TestDeedRechecksInventoryEpochBodySafetyAndLeaseBeforeQueryOrAccept(t *testing.T) {
	cases := map[string]func(*fakeSession){
		"revision": func(f *fakeSession) { f.world.InventoryRevision++ },
		"moved slot": func(f *fakeSession) {
			b := f.world.InventoryBags[1]
			b.Slots[5], b.Slots[6] = b.Slots[6], b.Slots[5]
			b.Slots[5].Slot = 5
			b.Slots[6].Slot = 6
		},
		"entry":      func(f *fakeSession) { f.world.InventoryBags[1].Slots[5].Entry = 1971 },
		"combat":     func(f *fakeSession) { f.world.Self.UnitFlags = 0x80000 },
		"cast":       func(f *fakeSession) { f.actions.CastPending = true },
		"correction": func(f *fakeSession) { f.world.Self.X = 100; f.world.Self.PositionRevision++ },
		"expiry":     func(f *fakeSession) { f.actions.InteractionLeaseRemainingMs = 0 },
		"transfer":   func(f *fakeSession) { f.world.Self.MapKnown = false },
	}
	for name, change := range cases {
		for _, accept := range []bool{false, true} {
			t.Run(name+map[bool]string{true: "accept", false: "query"}[accept], func(t *testing.T) {
				var s *Service
				var f *fakeSession
				var r *questsv1.QuestRequest
				if accept {
					s, f, r = offeredDeed(t, 1)
				} else {
					s, f = deedRig(1)
				}
				before := len(f.packets)
				f.onAcquire = func() { f.actions.InteractionLeaseRemainingMs = 15000; change(f) }
				var err error
				if accept {
					_, err = s.Accept(context.Background(), r)
				} else {
					_, err = s.Offer(context.Background(), &questsv1.OfferRequest{SessionId: "guard-body", NpcGuid: ownedDeedGUID})
				}
				if err == nil || len(f.packets) != before || f.lease {
					t.Fatal("stale/unsafe item write or leaked lease", err)
				}
			})
		}
	}
}
func TestDeedActualDetailsAndAmbiguousAcceptNeverGeneralizeOrReplay(t *testing.T) {
	s, f, r := offeredDeed(t, 1)
	bad := proto.Clone(r).(*questsv1.QuestRequest)
	bad.QuestId = 332
	if _, err := s.Accept(context.Background(), bad); err == nil || len(f.packets) != 1 {
		t.Fatal("other item quest accepted", err)
	}
	f.onSend = func(context.Context, *modv1.SendPacketRequest) error {
		return errors.New("lost acceptance write response")
	}
	if _, err := s.Accept(context.Background(), r); err == nil {
		t.Fatal("write ambiguity hidden")
	}
	if _, err := s.Accept(context.Background(), r); err == nil || len(f.packets) != 2 {
		t.Fatal("ambiguous item accept repeated", err)
	}
	before := f.acquired
	if _, err := s.Offer(context.Background(), &questsv1.OfferRequest{SessionId: "guard-body", NpcGuid: ownedDeedGUID}); err == nil || len(f.packets) != 2 || f.acquired != before {
		t.Fatal("new menu bypassed uncertain deed reservation", err)
	}
	f.confirm(184)
	if got := snapshot(t, s).LastAcceptance; got.Status != questsv1.AcceptanceStatus_CONFIRMED_IN_LOG || got.TransportWritten {
		t.Fatal("late actual own log proof lost", got)
	}
	for _, id := range []uint32{184, 332} {
		s, f = deedRig(1)
		f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
			packet(t, s, opcode.SMSGQuestgiverQuestDetails, deedDetails(t, ownedDeedGUID+1, id))
			return errors.New("done")
		}
		_, _ = s.Offer(context.Background(), &questsv1.OfferRequest{SessionId: "guard-body", NpcGuid: ownedDeedGUID})
		if s.state.Offer != nil {
			t.Fatal("foreign item or other quest details bound")
		}
	}
}

func TestOrdinaryLostGuardDirectDetailsAreNotSwallowedAsPlayerShares(t *testing.T) {
	for _, entry := range []uint32{55, 56} {
		s, f, guid := guardObjectRig(entry)
		id := uint32(45)
		if entry == 56 {
			id = 71
		}
		f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
			if p.Opcode != opcode.CMSGGameobjUse {
				t.Error("GO hello changed")
			}
			packet(t, s, opcode.SMSGQuestgiverQuestDetails, deedDetails(t, guid, id))
			return nil
		}
		out, err := s.Offer(context.Background(), &questsv1.OfferRequest{SessionId: "guard-body", NpcGuid: guid})
		if err != nil || !out.ServerResponseReceived || out.State.Offer == nil || out.State.Offer.Quests[0].QuestId != id || out.State.PendingShare != nil || out.State.MalformedPackets != 0 {
			t.Fatal("ordinary direct details routed into share parser", out, err)
		}
	}
}
