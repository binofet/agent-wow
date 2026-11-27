package buddyquests

import (
	"bytes"
	"context"
	"errors"
	"testing"

	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const necklaceGUID uint64 = 0x4000000000000042

func necklaceRig() (*Service, *fakeSession) {
	s, f := deedRig(1)
	f.world.Self.Level = 37
	f.world.VisitRevision = 3
	f.world.InventoryBags[1].Slots[3] = &worldv1.BackpackSlot{Bag: 1, Slot: 3, ItemGuid: necklaceGUID, Entry: 7666, StackCount: 1, MetadataKnown: true}
	return s, f
}

func offerOwnedStarter(t *testing.T, s *Service, f *fakeSession, guid uint64, id uint32) *questsv1.QuestRequest {
	t.Helper()
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode != opcode.CMSGQuestgiverQueryQuest || !bytes.Equal(p.Payload, questPacket(guid, id, false)) {
			t.Fatalf("unexpected starter query %v", p)
		}
		packet(t, s, opcode.SMSGQuestgiverQuestDetails, deedDetails(t, guid, id))
		return nil
	}
	out, err := s.Offer(context.Background(), &questsv1.OfferRequest{SessionId: "guard-body", NpcGuid: guid})
	if err != nil || !out.GetServerResponseReceived() || out.GetState().GetOffer().GetNpcGuid() != guid {
		t.Fatalf("actual item offer missing: %v, %v", out, err)
	}
	return &questsv1.QuestRequest{SessionId: "guard-body", NpcGuid: guid, QuestId: id, OfferRevision: out.State.Offer.Revision}
}

func acceptOwnedStarter(t *testing.T, s *Service, f *fakeSession, r *questsv1.QuestRequest) {
	t.Helper()
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode != opcode.CMSGQuestgiverAcceptQuest || !bytes.Equal(p.Payload, questPacket(r.NpcGuid, r.QuestId, true)) {
			t.Fatalf("unexpected item acceptance %v", p)
		}
		f.confirm(r.QuestId)
		return nil
	}
	if out, err := s.Accept(context.Background(), r); err != nil || out.GetState().GetLastAcceptance().GetStatus() != questsv1.AcceptanceStatus_CONFIRMED_IN_LOG {
		t.Fatal(out, err)
	}
}

func TestShatteredNecklaceRetainedThroughAcceptanceAndTurnedInAtNPC(t *testing.T) {
	s, f := necklaceRig()
	ctx := context.Background()
	r := offerOwnedStarter(t, s, f, necklaceGUID, 2198)
	r.QuestId = 2283 // The item is also a delivery objective for this different quest.
	out, err := s.Accept(ctx, r)
	if err == nil || len(f.packets) != 1 {
		t.Fatal("a delivery relationship replaced the item's start quest", out)
	}
	r.QuestId = 2198
	acceptOwnedStarter(t, s, f, r)
	if f.world.InventoryBags[1].Slots[3].ItemGuid != necklaceGUID || f.world.InventoryRevision != 10 {
		t.Fatal("acceptance must allow the same carried quest item to remain")
	}
	if _, err := s.Accept(ctx, r); err == nil || len(f.packets) != 2 {
		t.Fatal("duplicate acceptance wrote another packet")
	}
	if _, err := s.TurnIn(ctx, &questsv1.TurnInRequest{SessionId: "guard-body", NpcGuid: necklaceGUID, QuestId: 2198, QuestLogRevision: f.world.SelfQuestLogRevision}); err == nil || len(f.packets) != 2 {
		t.Fatal("item substituted for reward NPC")
	}
	rewardStarterAtNPC(t, s, f, 2198, 6826) // Talvash del Kissel.
}

func rewardStarterAtNPC(t *testing.T, s *Service, f *fakeSession, questID, npcEntry uint32) {
	t.Helper()
	ctx := context.Background()
	npc := fixture().world.QuestGivers[0]
	npc.Entry, npc.Guid = npcEntry, 0xf130000000000007|uint64(npcEntry)<<24
	f.world.QuestGivers = []*worldv1.CreatureState{npc}
	f.world.SelfQuestLog[0].Complete = true
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		switch p.Opcode {
		case opcode.CMSGQuestgiverCompleteQuest:
			if !bytes.Equal(p.Payload, turnInPacket(npc.Guid, questID)) {
				t.Fatal("wrong NPC turn-in")
			}
			packet(t, s, opcode.SMSGQuestgiverOfferReward, objectReward(npc.Guid, questID))
		case opcode.CMSGQuestgiverChooseReward:
			if !bytes.Equal(p.Payload, rewardPacket(npc.Guid, questID, 0)) {
				t.Fatal("wrong reward request")
			}
			packet(t, s, opcode.SMSGQuestgiverQuestComplete, rewardReceiptFixture(questID))
			f.world.InventoryBags[1].Slots[3] = &worldv1.BackpackSlot{Bag: 1, Slot: 3}
			f.world.InventoryRevision++
			f.confirm(0)
		default:
			t.Fatalf("unexpected turn-in packet %v", p)
		}
		return nil
	}
	query := &questsv1.TurnInRequest{SessionId: "guard-body", NpcGuid: npc.Guid, QuestId: questID, QuestLogRevision: f.world.SelfQuestLogRevision}
	turn, err := s.TurnIn(ctx, query)
	if err != nil || turn.GetState().GetTurnInOffer() == nil {
		t.Fatal(turn, err)
	}
	reward, err := s.ClaimReward(ctx, &questsv1.RewardRequest{SessionId: "guard-body", NpcGuid: npc.Guid, QuestId: questID, OfferRevision: turn.State.TurnInOffer.Revision, NoChoice: true})
	if err != nil || !reward.GetState().GetLastReward().GetServerRewardObserved() || !reward.GetState().GetLastReward().GetLogRemovalConfirmed() || f.lease {
		t.Fatal(reward, err)
	}
}

func TestOwnedQuestStarterBindsVisitLevelCountAndManualQuest(t *testing.T) {
	for name, change := range map[string]func(*fakeSession){
		"level below necklace requirement":       func(f *fakeSession) { f.world.Self.Level = 36 },
		"impossible necklace stack":              func(f *fakeSession) { f.world.InventoryBags[1].Slots[3].StackCount = 2 },
		"no own visit":                           func(f *fakeSession) { f.world.VisitRevision = 0 },
		"quest only auto-accepted by addon flag": func(f *fakeSession) { f.world.InventoryBags[1].Slots[3].Entry = 6497 },
		"client auto-accept flag":                func(f *fakeSession) { f.world.InventoryBags[1].Slots[3].Entry = 24414 },
	} {
		t.Run(name, func(t *testing.T) {
			s, f := necklaceRig()
			change(f)
			_, err := s.Offer(context.Background(), &questsv1.OfferRequest{SessionId: "guard-body", NpcGuid: necklaceGUID})
			if err == nil || f.acquired != 0 || len(f.packets) != 0 {
				t.Fatal("ineligible item affected body", err)
			}
		})
	}
	for name, change := range map[string]func(*fakeSession){
		"same-map visit change": func(f *fakeSession) { f.world.VisitRevision++ },
		"another copy":          func(f *fakeSession) { f.world.InventoryBags[1].Slots[3].ItemGuid++ },
		"new own log":           func(f *fakeSession) { f.world.SelfQuestLogKnown = false },
	} {
		t.Run(name, func(t *testing.T) {
			s, f := necklaceRig()
			r := offerOwnedStarter(t, s, f, necklaceGUID, 2198)
			f.onAcquire = func() { f.actions.InteractionLeaseRemainingMs = 15000; change(f) }
			if _, err := s.Accept(context.Background(), r); err == nil || len(f.packets) != 1 || f.lease {
				t.Fatal("stale starter accepted", err)
			}
		})
	}
}

func TestOwnedQuestStarterAmbiguitySurvivesOtherOfferAndReplacement(t *testing.T) {
	s, f := necklaceRig()
	ctx := context.Background()
	necklace := offerOwnedStarter(t, s, f, necklaceGUID, 2198)
	f.onSend = func(context.Context, *modv1.SendPacketRequest) error {
		return errors.New("original acceptance write outcome unknown")
	}
	if _, err := s.Accept(ctx, necklace); err == nil {
		t.Fatal("ambiguous receipt missing")
	}
	other := offerOwnedStarter(t, s, f, ownedDeedGUID, 184)
	if _, err := s.Accept(ctx, other); err == nil || len(f.packets) != 3 || s.state.LastAcceptance.QuestId != 2198 {
		t.Fatal("second acceptance overwrote the original unresolved receipt", err)
	}
	f.world.InventoryBags[1].Slots[3].ItemGuid++
	f.world.InventoryRevision++
	if _, err := s.Offer(ctx, &questsv1.OfferRequest{SessionId: "guard-body", NpcGuid: necklaceGUID + 1}); err == nil || len(f.packets) != 3 {
		t.Fatal("second item copy reopened the unresolved quest", err)
	}
	f.confirm(2198)
	if got := snapshot(t, s).LastAcceptance; got.Status != questsv1.AcceptanceStatus_CONFIRMED_IN_LOG || got.TransportWritten {
		t.Fatal("late actual quest log did not reconcile ambiguous original item", got)
	}
	other = offerOwnedStarter(t, s, f, ownedDeedGUID, 184)
	acceptOwnedStarter(t, s, f, other)
}

func TestRepeatableItemQuestNeedsARewardedCycleAndAnotherPhysicalStarter(t *testing.T) {
	const deckGUID = necklaceGUID + 9
	for _, rewarded := range []bool{false, true} {
		s, f := necklaceRig()
		slot := &worldv1.BackpackSlot{Bag: 1, Slot: 3, ItemGuid: deckGUID, Entry: 19228, StackCount: 1, MetadataKnown: true}
		f.world.InventoryBags[1].Slots[3] = slot
		acceptOwnedStarter(t, s, f, offerOwnedStarter(t, s, f, deckGUID, 7907))
		if rewarded {
			rewardStarterAtNPC(t, s, f, 7907, 14847)
		} else {
			f.confirm(0) // An observed removal without any reward.
		}
		f.world.InventoryBags[1].Slots[3] = slot
		f.world.InventoryRevision++
		before := len(f.packets)
		request := &questsv1.OfferRequest{SessionId: "guard-body", NpcGuid: deckGUID}
		if _, err := s.Offer(context.Background(), request); err == nil || len(f.packets) != before {
			t.Fatal("replayed prior physical starter")
		}
		slot.ItemGuid++
		f.world.InventoryRevision++
		if rewarded {
			actual := offerOwnedStarter(t, s, f, slot.ItemGuid, 7907)
			acceptOwnedStarter(t, s, f, actual)
		} else {
			request.NpcGuid = slot.ItemGuid
			if _, err := s.Offer(context.Background(), request); err == nil || len(f.packets) != before {
				t.Fatal("unrewarded cycle became reusable")
			}
		}
	}
}

func TestStackableQuestStarterRetainsExactOriginallyOfferedCount(t *testing.T) {
	s, f := necklaceRig()
	f.world.Self.Level = 78
	slot := f.world.InventoryBags[1].Slots[3]
	slot.Entry, slot.StackCount = 46955, 2
	request := offerOwnedStarter(t, s, f, slot.ItemGuid, 14095)
	slot.StackCount--
	if _, err := s.Accept(context.Background(), request); err == nil || len(f.packets) != 1 || f.acquired != 1 {
		t.Fatal("item quantity changed across the offer/accept boundary", err)
	}
}

func TestShatteredNecklaceKnownFullLogRejectionNeedsChangedCapacityAndFreshOffer(t *testing.T) {
	s, f := necklaceRig()
	ctx := context.Background()
	for i := 0; i < 24; i++ {
		f.world.SelfQuestLog[i].QuestId = uint32(100 + i)
	}
	original := offerOwnedStarter(t, s, f, necklaceGUID, 2198)
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode != opcode.CMSGQuestgiverAcceptQuest {
			t.Fatal(p)
		}
		f.world.SelfQuestLog[24].QuestId = 124
		f.world.SelfQuestLogRevision++
		packet(t, s, opcode.SMSGQuestlogFull, nil)
		return nil
	}
	out, err := s.Accept(ctx, original)
	if err != nil || out.GetState().GetLastAcceptance().GetStatus() != questsv1.AcceptanceStatus_SERVER_REJECTED {
		t.Fatal(out, err)
	}
	request := &questsv1.OfferRequest{SessionId: "guard-body", NpcGuid: necklaceGUID}
	if _, err := s.Offer(ctx, request); err == nil || len(f.packets) != 2 {
		t.Fatal("unchanged full journal reopened the starter", err)
	}
	f.world.SelfQuestLog[24].QuestId = 0
	f.world.SelfQuestLogRevision++
	if _, err := s.Accept(ctx, original); err == nil || len(f.packets) != 2 {
		t.Fatal("used earlier offer after rejection", err)
	}
	acceptOwnedStarter(t, s, f, offerOwnedStarter(t, s, f, necklaceGUID, 2198))
}
