package buddytraining

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"

	trainingv1 "github.com/agent-wow/agent-wow/modules/buddy-training/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func professionWire(spec professionSpec, usable byte) []byte {
	p := trainerWire(usable)
	binary.LittleEndian.PutUint32(p[8:], 2)
	binary.LittleEndian.PutUint32(p[16:], spec.wrapper)
	if primarySkill(spec.skill) {
		binary.LittleEndian.PutUint32(p[29:], 1)
	}
	p[33] = 5
	return p
}

func knownSkills(f *fakeSession, ids ...uint32) {
	f.world.SelfSkillsKnown = true
	f.world.SelfSkillsRevision++
	f.world.SelfSkills = make([]*worldv1.SkillState, 128)
	for i := range f.world.SelfSkills {
		f.world.SelfSkills[i] = &worldv1.SkillState{Slot: uint32(i)}
	}
	for i, id := range ids {
		f.world.SelfSkills[i].SkillId = id
		f.world.SelfSkills[i].Step, f.world.SelfSkills[i].Value, f.world.SelfSkills[i].MaxValue = 1, 1, 75
	}
	f.world.SelfFreePrimarySlotsKnown = true
	count := 0
	for _, id := range ids {
		if primarySkill(id) {
			count++
		}
	}
	f.world.SelfFreePrimarySlots = uint32(2 - count)
	f.world.SelfFreePrimarySlotsRevision++
}

func professionRig(t *testing.T, skill uint32) (*Service, *fakeSession, *trainingv1.ProfessionBuyRequest, professionSpec) {
	t.Helper()
	f := fixture()
	f.world.Self.Level = 5
	f.world.NearbyCreatures[0].NpcFlags = 0x50
	knownSkills(f)
	s := NewService(f, "body", 1)
	p, ok := professionForSkill(skill)
	if !ok {
		t.Fatal(skill)
	}
	f.onSend = func(_ context.Context, q *modv1.SendPacketRequest) error {
		if q.Opcode == opcode.CMSGTrainerList {
			deliver(s, opcode.SMSGTrainerList, professionWire(p, 0))
		}
		return nil
	}
	out, err := s.List(context.Background(), &trainingv1.ListRequest{SessionId: "body", NpcGuid: trainerGUID, TrainerType: 2})
	if err != nil {
		t.Fatal(err)
	}
	pair := []uint32{393, 165}
	switch skill {
	case 197, 333:
		pair = []uint32{197, 333}
	case 182, 171:
		pair = []uint32{182, 171}
	case 186, 202:
		pair = []uint32{186, 202}
	case 164, 755, 773:
		pair = []uint32{skill, 393}
	}
	return s, f, &trainingv1.ProfessionBuyRequest{Purchase: &trainingv1.BuyRequest{SessionId: "body", NpcGuid: trainerGUID, OfferRevision: out.Offer.Revision, SpellId: p.wrapper, ExpectedCostCopper: 10, MaxSpendCopper: 10}, SkillId: skill, AllowedPrimarySkills: pair}, p
}

func TestProfessionCatalogMatchesVerifiedApprenticeWrapperAndSkillPairs(t *testing.T) {
	want := map[uint32][2]uint32{129: {3279, 3273}, 164: {2020, 2018}, 165: {2155, 2108}, 171: {2275, 2259}, 182: {2372, 2366}, 185: {2551, 2550}, 186: {2581, 2575}, 197: {3911, 3908}, 202: {4039, 4036}, 333: {7414, 7411}, 356: {7733, 7620}, 393: {8615, 8613}, 755: {25245, 25229}, 773: {45375, 45357}}
	if len(initialProfessions) != len(want) {
		t.Fatal(initialProfessions)
	}
	for _, p := range initialProfessions {
		if [2]uint32{p.wrapper, p.learned} != want[p.skill] || !primarySkill(p.skill) && !secondarySkill(p.skill) || p.name == "" {
			t.Fatal(p)
		}
		wire := professionWire(p, 0)
		o, err := decodeList(wire)
		if err != nil {
			t.Fatal(err)
		}
		annotateProfessions(o)
		q := o.Spells[0]
		points := uint32(0)
		if primarySkill(p.skill) {
			points = 1
		}
		if o.TrainerType != 2 || q.ProfessionSkillId != p.skill || q.LearnedSpellId != p.learned || q.ProfessionName != p.name || q.PrimaryProfession != primarySkill(p.skill) || q.PointCost[1] != points || q.Usable != 0 || q.CostCopper != 10 {
			t.Fatal(o)
		}
		for n := range len(wire) {
			if _, err := decodeList(wire[:n]); err == nil {
				t.Fatal("truncated profession offer accepted", n)
			}
		}
	}
	if !primarySkill(164) || !primarySkill(755) || !primarySkill(773) || primarySkill(185) || primarySkill(356) || primarySkill(129) {
		t.Fatal("primary/secondary classification incorrect")
	}
}

func TestProfessionDiscoveryAndListingPreserveClassDefaults(t *testing.T) {
	s, f, r, _ := professionRig(t, 393)
	d, err := s.DiscoverProfessions(context.Background(), &emptypb.Empty{})
	if err != nil || len(d.Nearby) != 1 || !d.Nearby[0].Interactable || !d.State.ProfessionsKnown || len(d.State.PrimaryProfessions) != 0 || !d.State.FreePrimarySlotsKnown || d.State.FreePrimarySlots != 2 {
		t.Fatal(d, err)
	}
	d, err = s.Discover(context.Background(), &emptypb.Empty{})
	if err != nil || len(d.Nearby) != 0 {
		t.Fatal("class discover began selecting profession trainers", d, err)
	}
	before := len(f.packets)
	if _, err := s.List(context.Background(), &trainingv1.ListRequest{SessionId: "body", NpcGuid: trainerGUID}); err == nil || len(f.packets) != before {
		t.Fatal("default class list reached profession trainer", err)
	}
	if _, err := s.Buy(context.Background(), r.Purchase); err == nil || len(f.packets) != before {
		t.Fatal("default class buy purchased profession", err)
	}
}

func TestProfessionPurchaseRequiresActualNewSkillAndExactLearningReceipt(t *testing.T) {
	for _, mode := range []string{"confirmed", "trainer_known", "no_skill", "no_learn", "wrong_learn", "no_ack", "rejected", "ambiguous", "inline_ack_error"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r, spec := professionRig(t, 393)
			f.onSend = func(ctx context.Context, q *modv1.SendPacketRequest) error {
				switch q.Opcode {
				case opcode.CMSGTrainerBuySpell:
					if mode == "ambiguous" {
						return errors.New("uncertain write")
					}
					if mode == "rejected" {
						deliver(s, opcode.SMSGTrainerBuyFailed, binary.LittleEndian.AppendUint32(buyPacket(trainerGUID, spec.wrapper), 2))
						return nil
					}
					if mode != "no_skill" {
						f.mu.Lock()
						knownSkills(f, spec.skill)
						f.wallet.Copper, f.wallet.CopperRevision = 90, 2
						f.mu.Unlock()
					}
					if mode != "no_learn" && mode != "trainer_known" {
						id := spec.learned
						if mode == "wrong_learn" {
							id = 2050
						}
						deliver(s, opcode.SMSGLearnedSpell, append(binary.LittleEndian.AppendUint32(nil, id), 0, 0))
					}
					if mode != "no_ack" {
						deliver(s, opcode.SMSGTrainerBuySucceeded, buyPacket(trainerGUID, spec.wrapper))
					} else {
						return errors.New("no actual trainer ACK")
					}
					if mode == "inline_ack_error" {
						_, _ = s.GetState(ctx, &emptypb.Empty{})
						return errors.New("ambiguous transport after actual proof")
					}
				case opcode.CMSGTrainerList:
					usable := byte(0)
					if mode == "trainer_known" {
						usable = 2
					}
					deliver(s, opcode.SMSGTrainerList, professionWire(spec, usable))
				}
				return nil
			}
			out, err := s.BuyProfession(context.Background(), r)
			if err != nil || out.ActionInFlight || f.lease || f.acquired != f.released {
				t.Fatal(out, err)
			}
			want := trainingv1.PurchaseStatus_TRAINER_SUCCEEDED
			switch mode {
			case "confirmed", "trainer_known", "inline_ack_error":
				want = trainingv1.PurchaseStatus_TRAINING_CONFIRMED
			case "rejected":
				want = trainingv1.PurchaseStatus_SERVER_REJECTED
			case "no_ack", "ambiguous":
				want = trainingv1.PurchaseStatus_UNCONFIRMED_TIMEOUT
			}
			p := out.LastPurchase
			if p.Status != want || p.ProfessionSkillId != 393 || p.BaselineSkillsRevision != 1 {
				t.Fatal("incorrect profession proof", p)
			}
			if want == trainingv1.PurchaseStatus_TRAINING_CONFIRMED && (!p.ProfessionSkillObserved || p.ConfirmedSkillsRevision != 2 || !p.ServerSuccessObserved) {
				t.Fatal("confirmed without actual new skill", p)
			}
			if len(f.packets) < 2 || hex.EncodeToString(f.packets[1].Payload) != "01000055010030f1a7210000" || f.packets[1].Opcode != opcode.CMSGTrainerBuySpell {
				t.Fatal("normal apprentice purchase wire differs", f.packets)
			}
			before := len(f.packets)
			r.Purchase.OfferRevision = out.Offer.Revision
			if _, err := s.BuyProfession(context.Background(), r); mode != "rejected" && (err == nil || len(f.packets) != before) {
				t.Fatal("profession purchase blindly replayed", err)
			}
		})
	}
}

func TestProfessionBuyFailsClosedOnMissingConflictingOrChangedEvidence(t *testing.T) {
	for _, mode := range []string{"unknown", "partial", "nil_slot", "duplicate_slot", "duplicate_skill", "unknown_free", "zero_free", "third", "unrelated_existing", "jewelcrafting", "already", "wrong_wrapper", "outside_pair", "duplicate_pair", "unsupported_pair", "unknown_level", "low_level", "points", "ability", "required_skill", "mapping", "wrong_trainer_type", "changed_under_lease", "learned_under_lease", "wallet_under_lease", "cancel_under_lease", "regressed_skills"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r, _ := professionRig(t, 393)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "unknown":
				f.world.SelfSkillsKnown = false
			case "partial":
				f.world.SelfSkills = f.world.SelfSkills[:127]
			case "nil_slot":
				f.world.SelfSkills[5] = nil
			case "duplicate_slot":
				f.world.SelfSkills[5].Slot = 4
			case "duplicate_skill":
				knownSkills(f, 165, 165)
			case "unknown_free":
				f.world.SelfFreePrimarySlotsKnown = false
			case "zero_free":
				f.world.SelfFreePrimarySlots = 0
			case "third":
				knownSkills(f, 165, 182)
				f.world.SelfFreePrimarySlots = 1 // Server configuration cannot authorize a third.
			case "unrelated_existing":
				knownSkills(f, 333)
			case "jewelcrafting":
				knownSkills(f, 755)
			case "already":
				knownSkills(f, 393)
			case "wrong_wrapper":
				r.Purchase.SpellId = 8613
			case "outside_pair":
				r.AllowedPrimarySkills = []uint32{197, 333}
			case "duplicate_pair":
				r.AllowedPrimarySkills = []uint32{393, 393}
			case "unsupported_pair":
				r.AllowedPrimarySkills = []uint32{393, 185}
			case "unknown_level":
				f.world.Self.LevelKnown = false
			case "low_level":
				f.world.Self.Level = 4
			case "points":
				s.state.Offer.Spells[0].PointCost[1] = 0
			case "ability":
				s.state.Offer.Spells[0].RequiredAbilities[0] = 2050
			case "required_skill":
				s.state.Offer.Spells[0].RequiredSkill = 393
			case "mapping":
				s.state.Offer.Spells[0].LearnedSpellId = 2050
			case "wrong_trainer_type":
				s.state.Offer.TrainerType = 0
			case "changed_under_lease":
				f.onAcquire = func() { knownSkills(f, 333) }
			case "learned_under_lease":
				f.onAcquire = func() { knownSkills(f, 393) }
			case "wallet_under_lease":
				f.onAcquire = func() { f.wallet.Copper, f.wallet.CopperRevision = 0, 2 }
			case "cancel_under_lease":
				f.onAcquire = cancel
			case "regressed_skills":
				f.world.SelfSkillsRevision = 0
			}
			before := len(f.packets)
			if out, err := s.BuyProfession(ctx, r); err == nil || len(f.packets) != before || f.lease || f.acquired != f.released {
				t.Fatal("unsafe profession purchase wrote or leaked lease", out, err, f.packets)
			}
		})
	}
}

func TestProfessionSecondAssignedSkillRetainsFirstAndExcludesSecondarySkills(t *testing.T) {
	s, f, r, spec := professionRig(t, 393)
	knownSkills(f, 165)
	f.world.SelfSkills[5] = &worldv1.SkillState{Slot: 5, SkillId: 185, Value: 1, MaxValue: 75, Step: 1}
	f.onSend = func(_ context.Context, q *modv1.SendPacketRequest) error {
		if q.Opcode == opcode.CMSGTrainerBuySpell {
			f.mu.Lock()
			knownSkills(f, 165, 393)
			f.mu.Unlock()
			deliver(s, opcode.SMSGLearnedSpell, append(binary.LittleEndian.AppendUint32(nil, spec.learned), 0, 0))
			deliver(s, opcode.SMSGTrainerBuySucceeded, buyPacket(trainerGUID, spec.wrapper))
		} else if q.Opcode == opcode.CMSGTrainerList {
			deliver(s, opcode.SMSGTrainerList, professionWire(spec, 2))
		}
		return nil
	}
	out, err := s.BuyProfession(context.Background(), r)
	if err != nil || out.LastPurchase.Status != trainingv1.PurchaseStatus_TRAINING_CONFIRMED || len(out.PrimaryProfessions) != 2 || out.PrimaryProfessions[0].SkillId != 165 || out.PrimaryProfessions[1].SkillId != 393 || out.FreePrimarySlots != 0 {
		t.Fatal(out, err)
	}
}

func TestProfessionReadSnapshotsAreClonedAndUnknownCannotBecomeProof(t *testing.T) {
	s, f, _, _ := professionRig(t, 393)
	knownSkills(f, 165)
	out, err := s.GetState(context.Background(), &emptypb.Empty{})
	if err != nil || !out.ProfessionsKnown || len(out.PrimaryProfessions) != 1 {
		t.Fatal(out, err)
	}
	out.PrimaryProfessions[0].SkillId = 393
	f.world.SelfSkillsKnown = false
	out, err = s.GetState(context.Background(), &emptypb.Empty{})
	if err != nil || out.ProfessionsKnown || len(out.PrimaryProfessions) != 0 {
		t.Fatal(out, err)
	}
	f.world.SelfSkillsKnown = true
	out, _ = s.GetState(context.Background(), &emptypb.Empty{})
	if out.PrimaryProfessions[0].SkillId != 165 {
		t.Fatal("snapshot mutation reached cache", out)
	}
	if _, err := s.BuyProfession(context.Background(), &trainingv1.ProfessionBuyRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
}

func TestSecondaryTrainingUsesNoPrimarySlotAndPreservesExistingPrimaries(t *testing.T) {
	for _, skill := range []uint32{129, 185, 356} {
		for _, withPair := range []bool{false, true} {
			s, f, r, spec := professionRig(t, skill)
			knownSkills(f, 164, 773)
			f.world.SelfFreePrimarySlotsKnown = false
			if !withPair {
				r.AllowedPrimarySkills = nil
			}
			f.onSend = func(_ context.Context, q *modv1.SendPacketRequest) error {
				if q.Opcode == opcode.CMSGTrainerBuySpell {
					f.mu.Lock()
					knownSkills(f, 164, 773, skill)
					f.world.SelfFreePrimarySlotsKnown = false
					f.mu.Unlock()
					deliver(s, opcode.SMSGLearnedSpell, append(binary.LittleEndian.AppendUint32(nil, spec.learned), 0, 0))
					deliver(s, opcode.SMSGTrainerBuySucceeded, buyPacket(trainerGUID, spec.wrapper))
				} else if q.Opcode == opcode.CMSGTrainerList {
					deliver(s, opcode.SMSGTrainerList, professionWire(spec, 2))
				}
				return nil
			}
			out, err := s.BuyProfession(context.Background(), r)
			if err != nil || out.LastPurchase.Status != trainingv1.PurchaseStatus_TRAINING_CONFIRMED || len(out.PrimaryProfessions) != 2 || out.PrimaryProfessions[0].SkillId != 164 || out.PrimaryProfessions[1].SkillId != 773 || len(out.SecondaryProfessions) != 1 || out.SecondaryProfessions[0].SkillId != skill {
				t.Fatal("secondary training changed primaries or needed primary slot", skill, out, err)
			}
			before := len(f.packets)
			r.Purchase.OfferRevision = out.Offer.Revision
			if _, err := s.BuyProfession(context.Background(), r); err == nil || len(f.packets) != before {
				t.Fatal("secondary already known was relearned", err)
			}
		}
	}
}
