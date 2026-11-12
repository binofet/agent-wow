package buddytraining

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	trainingv1 "github.com/agent-wow/agent-wow/modules/buddy-training/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func classCostRig(t *testing.T, cost, copper uint32) (*Service, *fakeSession, *trainingv1.BuyRequest) {
	t.Helper()
	f := fixture()
	f.wallet.Copper = copper
	s := NewService(f, "body", 1)
	wire := trainerWire(0)
	binary.LittleEndian.PutUint32(wire[21:], cost)
	f.onSend = func(_ context.Context, q *modv1.SendPacketRequest) error {
		if q.Opcode == opcode.CMSGTrainerList {
			deliver(s, opcode.SMSGTrainerList, wire)
		}
		if q.Opcode == opcode.CMSGTrainerBuySpell {
			deliver(s, opcode.SMSGLearnedSpell, []byte{2, 8, 0, 0, 0, 0})
			deliver(s, opcode.SMSGTrainerBuySucceeded, buyPacket(trainerGUID, 2050))
			f.mu.Lock()
			f.wallet.Copper -= cost
			f.wallet.CopperRevision++
			f.mu.Unlock()
		}
		return nil
	}
	out, e := s.List(context.Background(), &trainingv1.ListRequest{SessionId: "body", NpcGuid: trainerGUID})
	if e != nil {
		t.Fatal(e)
	}
	return s, f, &trainingv1.BuyRequest{SessionId: "body", NpcGuid: trainerGUID, OfferRevision: out.Offer.Revision,
		SpellId: 2050, ExpectedCostCopper: cost, MaxSpendCopper: max(1, cost), ReserveCopper: 100}
}
func buyWrites(f *fakeSession) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, q := range f.packets {
		if q.Opcode == opcode.CMSGTrainerBuySpell {
			count++
		}
	}
	return count
}
func TestClassPurchaseWalletReserveAndHundredGoldCeiling(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		cost, wallet, budget uint32
		want                 codes.Code
	}{
		{"later_rank_above_one_gold", 50000, 50100, 50000, codes.OK},
		{"hundred_gold", 1000000, 1000100, 1000000, codes.OK},
		{"above_ceiling", 1000001, 2000000, 1000001, codes.InvalidArgument},
		{"wallet_reserve", 50000, 50099, 50000, codes.FailedPrecondition},
		{"free_no_wallet", 0, 0, 1, codes.OK},
		{"no_authorization_budget", 0, 100, 0, codes.InvalidArgument},
		{"price_above_authorization", 50000, 100000, 49999, codes.FailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, r := classCostRig(t, tc.cost, tc.wallet)
			r.MaxSpendCopper = tc.budget
			out, e := s.Buy(context.Background(), r)
			if status.Code(e) != tc.want {
				t.Fatalf("out=%v error=%v", out, e)
			}
			expected := 0
			if tc.want == codes.OK {
				expected = 1
				if out.LastPurchase.Status != trainingv1.PurchaseStatus_TRAINING_CONFIRMED {
					t.Fatal(out)
				}
			}
			if buyWrites(f) != expected {
				t.Fatal("unexpected purchase writes", buyWrites(f))
			}
		})
	}
}
func TestClassReserveRecheckedAfterLease(t *testing.T) {
	s, f, r := classCostRig(t, 50000, 50100)
	f.onAcquire = func() { f.wallet.Copper = 50099; f.wallet.CopperRevision++ }
	_, e := s.Buy(context.Background(), r)
	if status.Code(e) != codes.FailedPrecondition || buyWrites(f) != 0 || f.lease {
		t.Fatalf("%v writes=%d lease=%v", e, buyWrites(f), f.lease)
	}
}
func TestProfessionPurchaseStillLimitedToOneGold(t *testing.T) {
	s, f, r, _ := professionRig(t, 393)
	r.Purchase.MaxSpendCopper = 10001
	_, e := s.BuyProfession(context.Background(), r)
	if status.Code(e) != codes.InvalidArgument || buyWrites(f) != 0 {
		t.Fatal(e)
	}
}
func TestAmbiguousClassPurchaseNeedsActualFreshTrainerKnowledge(t *testing.T) {
	for _, usable := range []byte{0, 1, 2} {
		t.Run(string(rune('0'+usable)), func(t *testing.T) {
			s, f, r := classCostRig(t, 10, 1000)
			f.onSend = func(_ context.Context, q *modv1.SendPacketRequest) error {
				if q.Opcode == opcode.CMSGTrainerBuySpell {
					return errors.New("ambiguous transport")
				}
				if q.Opcode == opcode.CMSGTrainerList {
					deliver(s, opcode.SMSGTrainerList, trainerWire(usable))
				}
				return nil
			}
			out, e := s.Buy(context.Background(), r)
			if e != nil || out.LastPurchase.Status != trainingv1.PurchaseStatus_UNCONFIRMED_TIMEOUT {
				t.Fatalf("%v %v", out, e)
			}
			// An unsolicited or malformed message must not retire the reservation.
			deliver(s, opcode.SMSGTrainerList, trainerWire(2))
			if s.state.LastPurchase.Status != trainingv1.PurchaseStatus_UNCONFIRMED_TIMEOUT {
				t.Fatal("unsolicited list resolved ambiguity")
			}
			out, e = s.List(context.Background(), &trainingv1.ListRequest{SessionId: "body", NpcGuid: trainerGUID})
			if e != nil {
				t.Fatal(e)
			}
			p := out.LastPurchase
			want := trainingv1.PurchaseStatus_UNCONFIRMED_TIMEOUT
			if usable == 2 {
				want = trainingv1.PurchaseStatus_KNOWN_AT_TRAINER
			}
			if p.Status != want || p.ServerSuccessObserved || p.CopperDebitObserved || p.LearnedSpellObserved {
				t.Fatal(p)
			}
			r.OfferRevision = out.Offer.Revision
			// Unknown/unlearned ambiguity still blocks every other purchase; known
			// status clears that guard but this actual known offer itself is not buyable.
			_, e = s.Buy(context.Background(), r)
			if status.Code(e) != codes.FailedPrecondition || buyWrites(f) != 1 {
				t.Fatalf("%v writes=%d", e, buyWrites(f))
			}
			if usable == 2 {
				s.mu.Lock()
				s.state.Offer.Spells = append(s.state.Offer.Spells, &trainingv1.Spell{SpellId: 2051, CostCopper: 10, PointCost: []uint32{0, 0}})
				probe := proto.Clone(r).(*trainingv1.BuyRequest)
				probe.SpellId = 2051
				err := s.boundBuyLocked(probe)
				s.mu.Unlock()
				if err != nil {
					t.Fatal("positive knowledge did not unblock a different spell", err)
				}
				// A late failure cannot overwrite positive current knowledge. A late
				// matching success may upgrade it to the genuine original receipt.
				deliver(s, opcode.SMSGTrainerBuyFailed, append(buyPacket(trainerGUID, 2050), 1, 0, 0, 0))
				if s.state.LastPurchase.Status != trainingv1.PurchaseStatus_KNOWN_AT_TRAINER {
					t.Fatal(s.state.LastPurchase)
				}
				deliver(s, opcode.SMSGTrainerBuySucceeded, buyPacket(trainerGUID, 2050))
				if s.state.LastPurchase.Status != trainingv1.PurchaseStatus_TRAINING_CONFIRMED {
					t.Fatal(s.state.LastPurchase)
				}
			}
		})
	}
}
func TestProfessionAmbiguityCannotUseClassKnowledgeEscape(t *testing.T) {
	s, f, r, spec := professionRig(t, 393)
	f.onSend = func(_ context.Context, q *modv1.SendPacketRequest) error {
		if q.Opcode == opcode.CMSGTrainerBuySpell {
			return errors.New("ambiguous transport")
		}
		if q.Opcode == opcode.CMSGTrainerList {
			deliver(s, opcode.SMSGTrainerList, professionWire(spec, 2))
		}
		return nil
	}
	out, e := s.BuyProfession(context.Background(), r)
	if e != nil || out.LastPurchase.Status != trainingv1.PurchaseStatus_UNCONFIRMED_TIMEOUT {
		t.Fatalf("%v %v", out, e)
	}
	out, e = s.List(context.Background(), &trainingv1.ListRequest{SessionId: "body", NpcGuid: trainerGUID, TrainerType: 2})
	if e != nil || out.LastPurchase.Status != trainingv1.PurchaseStatus_UNCONFIRMED_TIMEOUT || out.LastPurchase.RefreshedTrainerKnows {
		t.Fatalf("%v %v", out, e)
	}
}
