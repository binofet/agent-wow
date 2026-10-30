package buddyquests

import (
	"context"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func permitQuestApproach(f *fakeSession) {
	f.mu.Lock()
	f.world.QuestGivers[0].X = 6
	f.mu.Unlock()
	f.onApproach = func(_ context.Context, r *actionsv1.ApproachQuestRequest) (*actionsv1.State, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.lease || r.Token != "fixture-token" || r.NpcGuid != npcGUID {
			return nil, status.Error(codes.FailedPrecondition, "invalid lease")
		}
		f.actions.Position.X = 3
		f.actions.Reason = "quest_approach_within_distance"
		return proto.Clone(f.actions).(*actionsv1.State), nil
	}
}

func approaches(f *fakeSession) int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.approaches) }

func TestExplicitQuestQueriesApproachOnlyUnderOwnedLease(t *testing.T) {
	f := fixture()
	permitQuestApproach(f)
	s := NewService(f, "body-one", 1)
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode == opcode.CMSGQuestgiverHello {
			packet(t, s, opcode.SMSGQuestgiverQuestList, unhex(t, listHex))
		}
		return nil
	}
	result, err := s.Offer(context.Background(), &questsv1.OfferRequest{SessionId: "body-one", NpcGuid: npcGUID})
	if err != nil || !result.ServerResponseReceived || approaches(f) != 1 {
		t.Fatal(result, err, approaches(f))
	}
	if n, a, released, held := f.counters(); n != 2 || a != 1 || released != 1 || held {
		t.Fatal(n, a, released, held)
	}
	// A fresh details request may make a separately authorized short approach.
	f.mu.Lock()
	f.actions.Position.X = 0
	f.mu.Unlock()
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode == opcode.CMSGQuestgiverQueryQuest {
			packet(t, s, opcode.SMSGQuestgiverQuestDetails, unhex(t, detailsHex))
		}
		return nil
	}
	result, err = s.Details(context.Background(), request(result.State.Offer))
	if err != nil || !result.ServerResponseReceived || approaches(f) != 2 {
		t.Fatal(result, err, approaches(f))
	}
}

func TestStaleOrOwnedPickupCannotAcquireLeaseOrApproach(t *testing.T) {
	for _, mode := range []string{"stale", "owned", "unknown log"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r := offered(t)
			permitQuestApproach(f)
			switch mode {
			case "stale":
				r.OfferRevision++
			case "owned":
				f.confirm(7)
			case "unknown log":
				f.mu.Lock()
				f.world.SelfQuestLogKnown = false
				f.mu.Unlock()
			}
			if _, err := s.Accept(context.Background(), r); err == nil {
				t.Fatal("invalid pickup accepted")
			}
			if n, a, released, held := f.counters(); n != 2 || a != 1 || released != 1 || held || approaches(f) != 0 {
				t.Fatal(n, a, released, held, approaches(f))
			}
		})
	}
}

func TestOfferExpiryDuringLeasePreventsAnyApproach(t *testing.T) {
	for _, method := range []string{"accept", "details", "reward"} {
		t.Run(method, func(t *testing.T) {
			var s *Service
			var f *fakeSession
			var pickup *questsv1.QuestRequest
			var reward *questsv1.RewardRequest
			if method == "reward" {
				s, f, reward = turnInRig(t, false, false)
			} else {
				s, f, pickup = offered(t)
			}
			permitQuestApproach(f)
			now := time.Now()
			s.now = func() time.Time { return now }
			f.onAcquire = func() { now = now.Add(31 * time.Second) }
			var err error
			switch method {
			case "accept":
				_, err = s.Accept(context.Background(), pickup)
			case "details":
				_, err = s.Details(context.Background(), pickup)
			case "reward":
				_, err = s.ClaimReward(context.Background(), reward)
			}
			if err == nil || approaches(f) != 0 {
				t.Fatal("expired offer allowed movement", err, approaches(f))
			}
			if _, _, _, held := f.counters(); held {
				t.Fatal("expired offer leaked lease")
			}
		})
	}
}

func TestApproachMustActuallyPassFreshRangeGate(t *testing.T) {
	for _, mode := range []string{"range", "motion", "owner", "error", "expired", "own log changed"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r := offered(t)
			permitQuestApproach(f)
			now := time.Now()
			s.now = func() time.Time { return now }
			f.onApproach = func(_ context.Context, _ *actionsv1.ApproachQuestRequest) (*actionsv1.State, error) {
				if mode == "error" {
					return nil, status.Error(codes.DeadlineExceeded, "bounded approach timeout")
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				f.actions.Reason = "quest_approach_within_distance"
				if mode != "range" {
					f.actions.Position.X = 3
				}
				if mode == "motion" {
					f.actions.Moving = true
				}
				if mode == "owner" {
					f.actions.InteractionOwner = "trade"
				}
				if mode == "expired" {
					now = now.Add(31 * time.Second)
				}
				if mode == "own log changed" {
					f.world.SelfQuestLog[0].QuestId = 7
					f.world.SelfQuestLogRevision++
				}
				return proto.Clone(f.actions).(*actionsv1.State), nil
			}
			if _, err := s.Accept(context.Background(), r); err == nil {
				t.Fatal("invalid post-approach state accepted")
			}
			if n, _, _, held := f.counters(); n != 2 || held || approaches(f) != 1 {
				t.Fatal("quest write or lease leak", n, held, approaches(f))
			}
		})
	}
}

func TestAcceptTurnInAndRewardApproachThenKeepNormalConfirmationRules(t *testing.T) {
	s, f, r := offered(t)
	permitQuestApproach(f)
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode == opcode.CMSGQuestgiverAcceptQuest {
			f.confirm(7)
		}
		return nil
	}
	accepted, err := s.Accept(context.Background(), r)
	if err != nil || approaches(f) != 1 || accepted.State.LastAcceptance.Status != questsv1.AcceptanceStatus_CONFIRMED_IN_LOG {
		t.Fatal(accepted, err)
	}

	f = fixture()
	f.world.SelfQuestLog[0].QuestId = 783
	permitQuestApproach(f)
	s = NewService(f, "body-one", 1)
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode == opcode.CMSGQuestgiverCompleteQuest {
			packet(t, s, opcode.SMSGQuestgiverOfferReward, rewardFixture(false))
		}
		return nil
	}
	turned, err := s.TurnIn(context.Background(), &questsv1.TurnInRequest{SessionId: "body-one", NpcGuid: npcGUID, QuestId: 783, QuestLogRevision: 1})
	if err != nil || approaches(f) != 1 || turned.State.TurnInOffer == nil {
		t.Fatal(turned, err)
	}
	f.mu.Lock()
	f.actions.Position.X = 0
	f.mu.Unlock()
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode == opcode.CMSGQuestgiverChooseReward {
			f.confirm(0)
			packet(t, s, opcode.SMSGQuestgiverQuestComplete, rewardReceiptFixture(783))
		}
		return nil
	}
	claimed, err := s.ClaimReward(context.Background(), &questsv1.RewardRequest{SessionId: "body-one", NpcGuid: npcGUID, QuestId: 783, OfferRevision: turned.State.TurnInOffer.Revision, NoChoice: true})
	if err != nil || approaches(f) != 2 || claimed.State.LastReward.Status != questsv1.RewardStatus_REWARD_CONFIRMED {
		t.Fatal(claimed, err)
	}
}

func TestTurnInLogMutationDuringLeaseCannotWalk(t *testing.T) {
	f := fixture()
	f.world.SelfQuestLog[0].QuestId = 783
	permitQuestApproach(f)
	s := NewService(f, "body-one", 1)
	f.onAcquire = func() { f.world.SelfQuestLogRevision++ }
	if _, err := s.TurnIn(context.Background(), &questsv1.TurnInRequest{SessionId: "body-one", NpcGuid: npcGUID, QuestId: 783, QuestLogRevision: 1}); err == nil {
		t.Fatal("changed turn-in log accepted")
	}
	if n, _, _, held := f.counters(); n != 0 || held || approaches(f) != 0 {
		t.Fatal(n, held, approaches(f))
	}
}
