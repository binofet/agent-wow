package buddyquests

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"

	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func rewardFixture(choices bool) []byte {
	b := new(bytes.Buffer)
	_ = binary.Write(b, binary.LittleEndian, npcGUID)
	_ = binary.Write(b, binary.LittleEndian, uint32(783))
	b.WriteString("A Threat Within\x00Well done.\x00")
	b.WriteByte(1)
	values := []uint32{0x80008, 0, 0} // flags,suggested,emotes
	if choices {
		values = append(values, 2, 100, 1, 10, 200, 2, 20)
	} else {
		values = append(values, 0)
	}
	values = append(values, 0, 0, 40) // fixed-item count,money,XP
	values = append(values, 0, 0, 8, 0, 0, 0, 0, 0, 0)
	values = append(values, make([]uint32, 15)...)
	for _, v := range values {
		_ = binary.Write(b, binary.LittleEndian, v)
	}
	return b.Bytes()
}
func requirementsFixture(complete bool) []byte {
	b := new(bytes.Buffer)
	_ = binary.Write(b, binary.LittleEndian, npcGUID)
	_ = binary.Write(b, binary.LittleEndian, uint32(783))
	b.WriteString("A Threat Within\x00Bring the items.\x00")
	values := []uint32{0, 0, 0, 8, 0, 0, 1, 117, 2, 123, 0, 4, 8, 16}
	if complete {
		values[10] = 3
	}
	for _, v := range values {
		_ = binary.Write(b, binary.LittleEndian, v)
	}
	return b.Bytes()
}
func rewardReceiptFixture(id uint32) []byte {
	p := binary.LittleEndian.AppendUint32(nil, id)
	for _, value := range []uint32{40, 0, 0, 0, 0} {
		p = binary.LittleEndian.AppendUint32(p, value)
	}
	return p
}

func TestTurnInWireAndStrictServerOfferLayouts(t *testing.T) {
	if got := hex.EncodeToString(turnInPacket(npcGUID, 783)); got != "01000033000030f10f030000" {
		t.Fatal(got)
	}
	if got := hex.EncodeToString(rewardPacket(npcGUID, 783, 1)); got != "01000033000030f10f03000001000000" {
		t.Fatal(got)
	}
	for _, tc := range []struct {
		data   []byte
		reward bool
	}{{rewardFixture(true), true}, {requirementsFixture(true), false}} {
		o, err := decodeTurnIn(tc.data, tc.reward)
		if err != nil || o.QuestId != 783 || !o.CanComplete || o.NpcGuid != npcGUID || o.Title != "A Threat Within" {
			t.Fatal(o, err)
		}
		if tc.reward && (len(o.Choices) != 2 || o.Choices[1].Index != 1 || o.Choices[1].ItemId != 200 || o.Choices[1].Count != 2 || o.RewardXp != 40) {
			t.Fatal(o)
		}
		if !tc.reward && (len(o.RequiredItems) != 1 || o.RequiredItems[0].ItemId != 117 || o.RequiredItems[0].Count != 2) {
			t.Fatal(o)
		}
		for n := range len(tc.data) {
			if _, err := decodeTurnIn(tc.data[:n], tc.reward); err == nil {
				t.Fatalf("truncated offer accepted%d", n)
			}
		}
		if _, err := decodeTurnIn(append(bytes.Clone(tc.data), 0), tc.reward); err == nil {
			t.Fatal("trailing offer accepted")
		}
	}
	r, err := decodeRewardReceipt(rewardReceiptFixture(783))
	if err != nil || r.QuestId != 783 || r.RewardXp != 40 {
		t.Fatal(r, err)
	}
	for n := range 24 {
		if _, err := decodeRewardReceipt(rewardReceiptFixture(783)[:n]); err == nil {
			t.Fatal("truncated receipt", n)
		}
	}
	if _, err := decodeRewardReceipt(append(rewardReceiptFixture(783), 0)); err == nil {
		t.Fatal("trailing receipt accepted")
	}
}

func turnInRig(t *testing.T, choices, requirements bool) (*Service, *fakeSession, *questsv1.RewardRequest) {
	t.Helper()
	f := fixture()
	f.world.SelfQuestLog[0].QuestId = 783
	f.world.BackpackKnown, f.world.BackpackRevision = true, 1
	for i := range 16 {
		f.world.Backpack = append(f.world.Backpack, &worldv1.BackpackSlot{Slot: uint32(i)})
	}
	s := NewService(f, "body-one", 1)
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		switch p.Opcode {
		case opcode.CMSGQuestgiverCompleteQuest:
			if requirements {
				packet(t, s, opcode.SMSGQuestgiverRequestItems, requirementsFixture(true))
			} else {
				packet(t, s, opcode.SMSGQuestgiverOfferReward, rewardFixture(choices))
			}
		case opcode.CMSGQuestgiverRequestReward:
			packet(t, s, opcode.SMSGQuestgiverOfferReward, rewardFixture(choices))
		}
		return nil
	}
	result, err := s.TurnIn(context.Background(), &questsv1.TurnInRequest{SessionId: "body-one", NpcGuid: npcGUID, QuestId: 783, QuestLogRevision: 1})
	if err != nil || !result.ServerResponseReceived || result.State.TurnInOffer == nil || result.State.TurnInOffer.Source != "reward_offer" {
		t.Fatal(result, err)
	}
	o := result.State.TurnInOffer
	r := &questsv1.RewardRequest{SessionId: "body-one", NpcGuid: npcGUID, QuestId: 783, OfferRevision: o.Revision, NoChoice: !choices}
	if choices {
		r.ChoiceIndex, r.ExpectedItemId = 1, 200
	}
	return s, f, r
}

func TestTurnInNeedsServerOfferAndRewardNeedsReceiptPlusLogRemoval(t *testing.T) {
	s, f, r := turnInRig(t, true, true)
	if n, a, released, held := f.counters(); n != 2 || a != 1 || released != 1 || held {
		t.Fatal("inspection claimed a reward or leaked lease", n, a, released, held)
	}
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode == opcode.CMSGQuestgiverChooseReward {
			if len(p.Payload) != 16 || binary.LittleEndian.Uint32(p.Payload[12:]) != 1 {
				t.Error("wrong reward index")
			}
			packet(t, s, opcode.SMSGQuestgiverQuestComplete, rewardReceiptFixture(783))
			f.mu.Lock()
			f.world.SelfQuestLog[0].QuestId = 0
			f.world.SelfQuestLogRevision++
			f.world.Backpack[0] = &worldv1.BackpackSlot{ItemGuid: 0x4000000000000001, Entry: 200, StackCount: 2, MetadataKnown: true}
			f.world.BackpackRevision++
			f.mu.Unlock()
		}
		return nil
	}
	result, err := s.ClaimReward(context.Background(), r)
	if err != nil || result.State.LastReward.Status != questsv1.RewardStatus_REWARD_CONFIRMED || !result.State.LastReward.LogRemovalConfirmed || !result.State.LastReward.ServerRewardObserved || !result.State.LastReward.RewardItemsObservedInBackpack {
		t.Fatal(result, err)
	}
	if _, err := s.ClaimReward(context.Background(), r); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("reward repeated", err)
	}
}

func TestWrongRewardChoicesCannotAcquireLeaseOrWrite(t *testing.T) {
	s, f, r := turnInRig(t, true, false)
	for _, bad := range []*questsv1.RewardRequest{
		{SessionId: r.SessionId, NpcGuid: r.NpcGuid, QuestId: r.QuestId, OfferRevision: r.OfferRevision, NoChoice: true},
		{SessionId: r.SessionId, NpcGuid: r.NpcGuid, QuestId: r.QuestId, OfferRevision: r.OfferRevision, ChoiceIndex: 5, ExpectedItemId: 200},
		{SessionId: r.SessionId, NpcGuid: r.NpcGuid, QuestId: r.QuestId, OfferRevision: r.OfferRevision, ChoiceIndex: 1, ExpectedItemId: 999},
		{SessionId: "old", NpcGuid: r.NpcGuid, QuestId: r.QuestId, OfferRevision: r.OfferRevision, ChoiceIndex: 1, ExpectedItemId: 200},
	} {
		if _, err := s.ClaimReward(context.Background(), bad); err == nil {
			t.Fatal("invalid reward accepted", bad)
		}
	}
	if n, a, _, _ := f.counters(); n != 1 || a != 1 {
		t.Fatal("invalid selection changed gameplay", n, a)
	}
}

func TestRewardTransportAndObjectiveCompleteNeverConfirmReward(t *testing.T) {
	s, f, r := turnInRig(t, false, false)
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode == opcode.CMSGQuestgiverChooseReward {
			packet(t, s, opcode.SMSGQuestupdateComplete, []byte{0x0f, 3, 0, 0})
			packet(t, s, opcode.SMSGGossipComplete, nil)
			packet(t, s, opcode.SMSGQuestgiverQuestComplete, rewardReceiptFixture(999))
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	result, err := s.ClaimReward(ctx, r)
	if err != nil || result.State.LastReward.Status != questsv1.RewardStatus_REWARD_UNCONFIRMED || result.State.LastReward.ServerRewardObserved {
		t.Fatal(result, err)
	}
	if _, err := s.ClaimReward(context.Background(), r); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("uncertain claim retried", err)
	}
	packet(t, s, opcode.SMSGQuestgiverQuestComplete, rewardReceiptFixture(783))
	if snapshot(t, s).LastReward.Status == questsv1.RewardStatus_REWARD_CONFIRMED {
		t.Fatal("receipt without new log removal confirmed reward")
	}
	f.confirm(0)
	if snapshot(t, s).LastReward.Status != questsv1.RewardStatus_REWARD_CONFIRMED {
		t.Fatal("late proof was lost")
	}
}

func TestIncompleteRequirementsDoNotRequestOrClaimRewards(t *testing.T) {
	f := fixture()
	f.world.SelfQuestLog[0].QuestId = 783
	s := NewService(f, "body-one", 1)
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode == opcode.CMSGQuestgiverCompleteQuest {
			packet(t, s, opcode.SMSGQuestgiverRequestItems, requirementsFixture(false))
		}
		return nil
	}
	result, err := s.TurnIn(context.Background(), &questsv1.TurnInRequest{SessionId: "body-one", NpcGuid: npcGUID, QuestId: 783, QuestLogRevision: 1})
	if err != nil || result.State.TurnInOffer.CanComplete || result.State.TurnInOffer.Source != "requirements" {
		t.Fatal(result, err)
	}
	if n, _, _, _ := f.counters(); n != 1 {
		t.Fatal("advanced incomplete quest", n)
	}
}

func TestTurnInSessionOwnLogAndRangeGates(t *testing.T) {
	for name, change := range map[string]func(*fakeSession, *questsv1.TurnInRequest){
		"session":      func(_ *fakeSession, r *questsv1.TurnInRequest) { r.SessionId = "old" },
		"log revision": func(_ *fakeSession, r *questsv1.TurnInRequest) { r.QuestLogRevision++ },
		"not active":   func(f *fakeSession, _ *questsv1.TurnInRequest) { f.world.SelfQuestLog[0].QuestId = 999 },
		"range":        func(f *fakeSession, _ *questsv1.TurnInRequest) { f.world.QuestGivers[0].X = 5 },
		"dead":         func(f *fakeSession, _ *questsv1.TurnInRequest) { f.world.Self.Alive = false },
	} {
		t.Run(name, func(t *testing.T) {
			f := fixture()
			f.world.SelfQuestLog[0].QuestId = 783
			s := NewService(f, "body-one", 1)
			r := &questsv1.TurnInRequest{SessionId: "body-one", NpcGuid: npcGUID, QuestId: 783, QuestLogRevision: 1}
			change(f, r)
			if _, err := s.TurnIn(context.Background(), r); err == nil {
				t.Fatal("invalid turn-in accepted")
			}
			if n, _, _, held := f.counters(); n != 0 || held {
				t.Fatal("invalid turn-in wrote or leaked lease", n, held)
			}
		})
	}
}

func TestRewardFailureAndFollowupAreServerObservedOnly(t *testing.T) {
	s, f, r := turnInRig(t, false, false)
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode == opcode.CMSGQuestgiverChooseReward {
			packet(t, s, opcode.SMSGQuestgiverOfferReward, rewardFixture(false))
		}
		return nil
	}
	result, err := s.ClaimReward(context.Background(), r)
	if err != nil || result.State.LastReward.Status != questsv1.RewardStatus_REWARD_REJECTED {
		t.Fatal(result, err)
	}
	s, f, r = turnInRig(t, false, false)
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode == opcode.CMSGQuestgiverChooseReward {
			f.confirm(0)
			packet(t, s, opcode.SMSGQuestgiverQuestComplete, rewardReceiptFixture(783))
			followup := unhex(t, detailsHex)
			binary.LittleEndian.PutUint32(followup[16:], 7)
			packet(t, s, opcode.SMSGQuestgiverQuestDetails, followup)
		}
		return nil
	}
	result, err = s.ClaimReward(context.Background(), r)
	if err != nil || result.State.Offer == nil || result.State.Offer.Quests[0].QuestId != 7 || result.State.LastReward.Status != questsv1.RewardStatus_REWARD_CONFIRMED {
		t.Fatal(result, err)
	}
	if n, _, _, _ := f.counters(); n != 2 {
		t.Fatal("automatically accepted followup", n)
	}
}

func TestRewardAmbiguousWriteConsumesOfferAndReleasesLease(t *testing.T) {
	s, f, r := turnInRig(t, false, false)
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode == opcode.CMSGQuestgiverChooseReward {
			return status.Error(codes.DeadlineExceeded, "ambiguous write")
		}
		return nil
	}
	if _, err := s.ClaimReward(context.Background(), r); err == nil {
		t.Fatal("ambiguous write reported success")
	}
	if st := snapshot(t, s); st.LastReward.Status != questsv1.RewardStatus_REWARD_WRITE_UNCONFIRMED || !st.TurnInOffer.RewardRequested {
		t.Fatal(st)
	}
	if _, err := s.ClaimReward(context.Background(), r); err == nil {
		t.Fatal("ambiguous write retried")
	}
	if n, a, released, held := f.counters(); n != 2 || a != 2 || released != 2 || held {
		t.Fatal("ambiguous claim retried or leaked", n, a, released, held)
	}
}

func TestNoChoiceRewardRequiresExplicitNoChoiceAndUnsolicitedPacketsAreIgnored(t *testing.T) {
	s, f, r := turnInRig(t, false, false)
	r.NoChoice = false
	if _, err := s.ClaimReward(context.Background(), r); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	before := snapshot(t, s).TurnInOffer.Revision
	packet(t, s, opcode.SMSGQuestgiverOfferReward, rewardFixture(true))
	packet(t, s, opcode.SMSGQuestgiverQuestComplete, rewardReceiptFixture(783))
	packet(t, s, opcode.SMSGQuestgiverOfferReward, []byte{1, 2})
	st := snapshot(t, s)
	if st.TurnInOffer.Revision != before || st.LastReward != nil || st.MalformedPackets != 1 {
		t.Fatal(st)
	}
	if n, a, _, held := f.counters(); n != 1 || a != 1 || held {
		t.Fatal(n, a, held)
	}
}

func FuzzTurnInPackets(f *testing.F) {
	f.Add(rewardFixture(true))
	f.Add(requirementsFixture(true))
	f.Add(rewardReceiptFixture(783))
	f.Fuzz(func(t *testing.T, p []byte) { decodeTurnIn(p, true); decodeTurnIn(p, false); decodeRewardReceipt(p) })
}
