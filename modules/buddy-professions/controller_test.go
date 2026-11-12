package buddyprofessions

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Direct operations let finite fishing transitions run synchronously; no test
// waits for the production25-second fishing deadline or changes that deadline.
func professionDirectOperation(t *testing.T, s *Service, f *professionSessionFixture, spell uint32) *operation {
	t.Helper()
	spec, ok := recipeForSpell(spell)
	if !ok {
		t.Fatal("invalid fixture spell")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	op := &operation{spec: spec, baseline: s.view, token: "MOCK-owned-token", started: s.now(), ctx: ctx, cancel: cancel, castReserved: true, stored: map[byte]bool{}, removed: map[byte]bool{}, pushed: map[byte]bool{}, pushCounts: map[uint32]uint32{}, receipt: &professionsv1.Operation{OperationId: "MOCK-operation", RequestId: "MOCK-request", SkillId: spec.skill, SpellId: spell, CastCount: 9, Status: professionsv1.AttemptStatus_WAITING_CAST, BaselineSkillsRevision: s.view.skillsRevision, BaselineBackpackRevision: s.view.backpackRevision, BaselineInventoryRevision: s.view.inventoryRevision, BeforeRank: s.view.skills[spec.skill].value, AfterRank: s.view.skills[spec.skill].value}}
	s.active, s.last = op, op
	s.mu.Unlock()
	f.mu.Lock()
	f.token, f.actions.InteractionOwner = op.token, "professions"
	f.actions.InteractionLeaseRemainingMs = 30000
	f.mu.Unlock()
	t.Cleanup(func() {
		cancel()
		s.mu.Lock()
		if s.active == op {
			s.active = nil
		}
		s.mu.Unlock()
	})
	return op
}

func TestControllerWrongActorCountAndSpellDoNotConfirmCast(t *testing.T) {
	s, f := newProfessionServiceFixture(t)
	op := professionDirectOperation(t, s, f, 3275)
	for _, code := range []uint32{opcode.SMSGSpellStart, opcode.SMSGSpellGo} {
		for _, identity := range []struct {
			actor uint64
			count byte
			spell uint32
		}{{8, 9, 3275}, {7, 10, 3275}, {7, 9, 2538}} {
			professionPacket(t, s, code, professionCastReceipt(code, identity.actor, identity.count, identity.spell))
		}
	}
	professionPacket(t, s, opcode.SMSGItemPushResult, professionPushReceipt(8, true, true, 1251, 1, 1))
	professionPacket(t, s, opcode.SMSGItemPushResult, professionPushReceipt(7, false, false, 1251, 1, 1))
	if r := professionReceipt(s, op); r.OwnGoObserved || r.OwnStartObserved || r.OwnCreatedItemsObserved != 0 || s.step(op) {
		t.Fatal("foreign/mismatched receipt confirmed craft", r)
	}
	professionPacket(t, s, opcode.SMSGSpellGo, []byte{1})
	if s.state.MalformedPackets != 1 || op.receipt.OwnGoObserved {
		t.Fatal("partial GO became progress")
	}
}

func TestControllerCraftRequiresCastPushOutputAndReagentReceipts(t *testing.T) {
	for _, missing := range []string{"go", "created_push", "output", "reagent"} {
		t.Run(missing, func(t *testing.T) {
			s, f := newProfessionServiceFixture(t)
			op := professionDirectOperation(t, s, f, 3275)
			if missing != "go" {
				professionPacket(t, s, opcode.SMSGSpellGo, professionCastReceipt(opcode.SMSGSpellGo, 7, 9, 3275))
			}
			if missing != "created_push" {
				professionPacket(t, s, opcode.SMSGItemPushResult, professionPushReceipt(7, true, true, 1251, 1, 1))
			}
			f.world = proto.Clone(f.world).(*worldv1.SnapshotResponse)
			f.world.SelfSkillsRevision++
			f.world.SelfSkills[0].Value++
			f.world.BackpackRevision++
			f.world.InventoryRevision++
			if missing != "reagent" {
				f.world.InventoryBags[0].Slots[0].StackCount--
			}
			if missing != "output" {
				f.world.InventoryBags[0].Slots[1] = &worldv1.BackpackSlot{Slot: 1, ItemGuid: 0x4000000000000003, Entry: 1251, StackCount: 1, MetadataKnown: true}
			}
			professionObserve(t, s)
			if s.step(op) || op.receipt.Status == professionsv1.AttemptStatus_COMPLETED || !op.receipt.SkillIncreased {
				t.Fatal("skill delta or incomplete craft evidence became completion", op.receipt)
			}
		})
	}
}

func TestControllerItemPushUsesActualWireBackpackCoordinates(t *testing.T) {
	for _, tc := range []struct {
		bag      byte
		slot     uint32
		accepted bool
	}{{255, 23, true}, {255, 38, true}, {255, math.MaxUint32, true}, {0, 1, false}, {0, 24, false}, {255, 15, false}, {255, 22, false}, {255, 39, false}, {19, 24, false}} {
		t.Run(fmt.Sprintf("bag%d_slot%d", tc.bag, tc.slot), func(t *testing.T) {
			s, f := newProfessionServiceFixture(t)
			op := professionDirectOperation(t, s, f, 3275)
			p := professionPushReceipt(7, true, true, 1251, 1, 1)
			p[20] = tc.bag
			binary.LittleEndian.PutUint32(p[21:], tc.slot)
			professionPacket(t, s, opcode.SMSGItemPushResult, p)
			if (op.receipt.OwnCreatedItemsObserved == 1) != tc.accepted {
				t.Fatal("wire storage identity interpreted as normalized world slot", op.receipt)
			}
			if tc.accepted {
				professionPacket(t, s, opcode.SMSGItemPushResult, p)
				if op.receipt.OwnCreatedItemsObserved != 1 {
					t.Fatal("duplicate push inflated one-craft output receipt", op.receipt)
				}
			}
		})
	}
}

func TestControllerEveryWriteRequiresExactLease(t *testing.T) {
	for _, code := range []uint32{opcode.CMSGCastSpell, opcode.CMSGGameobjUse, opcode.CMSGAutostoreLootItem, opcode.CMSGCancelChannelling, opcode.CMSGLootRelease} {
		for _, mutation := range []string{"matching", "returned_token_mismatch", "same_label_replacement", "different_owner", "moving"} {
			t.Run(fmt.Sprintf("%x_%s", code, mutation), func(t *testing.T) {
				s, f := newProfessionServiceFixture(t)
				professionFishingReadyFixture(t, s, f)
				op := professionDirectOperation(t, s, f, 7620)
				if code == opcode.CMSGGameobjUse {
					professionBindBobber(t, s, f, op)
					f.world.OwnFishingBobbers[0].State, f.world.OwnFishingBobbers[0].Flags = 0, 0x20
					professionObserve(t, s)
				}
				switch mutation {
				case "returned_token_mismatch":
					f.renewToken = "MOCK-forged-new-token"
				case "same_label_replacement":
					f.token = "MOCK-replacement-token"
				case "different_owner":
					f.actions.InteractionOwner = "mail"
				case "moving":
					f.actions.Moving = true
				}
				err := s.writeOwned(context.Background(), op, code, []byte{0})
				if mutation == "matching" {
					if err != nil || len(f.packets) != 1 || len(f.events) != 2 || f.events[0] != "renew:"+op.token {
						t.Fatal("write did not immediately renew exact token", err, f.events)
					}
				} else if err == nil || len(f.packets) != 0 {
					t.Fatal("write bypassed actual token/state rejection", err, f.events)
				}
			})
		}
	}
}

func TestControllerFinalRenewalRechecksActualCookingFocusRange(t *testing.T) {
	s, f := newProfessionServiceFixture(t)
	f.world.Self.MapId = 1
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = 100, 0, 0
	f.world.InventoryBags[0].Slots[1] = &worldv1.BackpackSlot{Slot: 1, ItemGuid: 0x4000000000000077, Entry: 2672, StackCount: 1, MetadataKnown: true}
	f.world.SpellFocuses = []*worldv1.GameObjectState{{Guid: 0xf110000000000077, Entry: 100, Present: true, TypeKnown: true, GameObjectType: 8, MapKnown: true, MapId: 1, PositionKnown: true, X: 10, Y: 20, Z: 30, StateKnown: true, State: 1}}
	s.focusRequested[100] = true
	s.focuses[100] = focusTemplate{entry: 100, focusID: 4, radius: 3}
	f.actions.Position = &actionsv1.ClientPosition{Known: true, MapKnown: true, MapId: 1, X: 10.5, Y: 20, Z: 30, Estimated: true}
	professionObserve(t, s)
	if reason := s.reasonLocked(starterRecipes[1]); reason != "" {
		t.Fatal("initial in-range focus rejected", reason)
	}
	op := professionDirectOperation(t, s, f, 2538)
	// Only the upcoming exact-token renewal contains the corrected position;
	// no world/status reread updates the service before writeOwned.
	f.actions.Position = proto.Clone(f.actions.Position).(*actionsv1.ClientPosition)
	f.actions.Position.X = 100
	if err := s.writeOwned(context.Background(), op, opcode.CMSGCastSpell, castPacket(9, 2538)); err == nil || len(f.packets) != 0 {
		t.Fatal("cast ignored newly observed out-of-range focus", err, f.events)
	}
}

func TestControllerAmbiguousCastDoesNotResendAndCancelIsOwned(t *testing.T) {
	s, f := newProfessionServiceFixture(t)
	f.sendErr[opcode.CMSGCastSpell] = errors.New("MOCK response lost after potential delivery")
	r := professionRequest(s, "ambiguous", 3275)
	first, err := s.Attempt(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	professionWaitPacket(t, f, opcode.CMSGCastSpell)
	s.mu.Lock()
	op := s.active
	s.mu.Unlock()
	replay, err := s.Attempt(context.Background(), r)
	if err != nil || replay.Operation.OperationId != first.Operation.OperationId {
		t.Fatal("ambiguous duplicate was not replayed", err)
	}
	changed := proto.Clone(r).(*professionsv1.AttemptRequest)
	changed.SpellId = 2538
	if _, err := s.Attempt(context.Background(), changed); status.Code(err) != codes.AlreadyExists {
		t.Fatal("duplicate ID allowed changed input", err)
	}
	for _, cancel := range []*professionsv1.CancelRequest{{SessionId: "foreign", OperationId: first.Operation.OperationId}, {SessionId: "body", OperationId: "foreign"}} {
		if _, err := s.Cancel(context.Background(), cancel); status.Code(err) != codes.FailedPrecondition {
			t.Fatal("foreign cancellation accepted", err)
		}
	}
	if _, err := s.Cancel(context.Background(), &professionsv1.CancelRequest{SessionId: "body", OperationId: first.Operation.OperationId}); err != nil {
		t.Fatal(err)
	}
	professionWaitDone(t, op)
	if r := professionReceipt(s, op); r.CastTransportWritten || r.OwnGoObserved || !r.CancelRequested || !r.CleanupUnconfirmed || !r.LeaseReleased {
		t.Fatal("ambiguous cleanup facts incorrect", r)
	}
	f.mu.Lock()
	castWrites := 0
	for i, event := range f.events {
		if strings.HasPrefix(event, "send:") && (i == 0 || !strings.HasPrefix(f.events[i-1], "renew:")) {
			t.Fatal("owned write lacked immediate exact renewal", f.events)
		}
	}
	for _, p := range f.packets {
		if p.Opcode == opcode.CMSGCastSpell {
			castWrites++
		}
	}
	f.mu.Unlock()
	if castWrites != 1 {
		t.Fatal("ambiguous cast retried", castWrites)
	}
	if _, err := s.Attempt(context.Background(), professionRequest(s, "new-after-ambiguity", 3275)); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("unreconciled operation allowed fresh work", err)
	}
}

func TestControllerLateFinishCannotTouchReplacementOperation(t *testing.T) {
	s, f := newProfessionServiceFixture(t)
	old := professionDirectOperation(t, s, f, 7620)
	fresh := professionDirectOperation(t, s, f, 3275)
	fresh.receipt.OperationId = "MOCK-new-operation"
	fresh.token, f.token = "MOCK-replacement-token", "MOCK-replacement-token"
	before := proto.Clone(fresh.receipt)
	s.finish(old)
	if s.active != fresh || !proto.Equal(before, fresh.receipt) || len(f.packets) != 0 || len(f.releases) != 0 || f.token != fresh.token {
		t.Fatal("late finish touched replacement ownership", f.events)
	}
	if _, err := s.Cancel(context.Background(), &professionsv1.CancelRequest{SessionId: "body", OperationId: old.receipt.OperationId}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("old cancellation touched new operation", err)
	}
}

func TestControllerCleanupCannotReleaseSameLabelReplacementLease(t *testing.T) {
	s, f := newProfessionServiceFixture(t)
	op := professionDirectOperation(t, s, f, 3275)
	professionPacket(t, s, opcode.SMSGSpellGo, professionCastReceipt(opcode.SMSGSpellGo, 7, 9, 3275))
	f.token = "MOCK-same-owner-replacement"
	s.finish(op)
	if len(f.releases) != 0 || len(f.packets) != 0 || f.token != "MOCK-same-owner-replacement" || !op.receipt.CleanupUnconfirmed || op.receipt.LeaseReleased {
		t.Fatal("cleanup released another professions lease", f.events, op.receipt)
	}
}

func TestControllerCastRejectionRequiresCompleteMatchingFailureReceipt(t *testing.T) {
	s, f := newProfessionServiceFixture(t)
	op := professionDirectOperation(t, s, f, 3275)
	p := protocolFailureFixture(100, 4)
	binary.LittleEndian.PutUint32(p[1:], 3275)
	binary.LittleEndian.PutUint32(p[6:], 2589)
	professionPacket(t, s, opcode.SMSGCastFailed, p[:len(p)-1])
	wrong := append([]byte{}, p...)
	wrong[0] = 10
	professionPacket(t, s, opcode.SMSGCastFailed, wrong)
	if op.receipt.FailureCode != 0 || op.receipt.Status == professionsv1.AttemptStatus_SERVER_REJECTED {
		t.Fatal("partial or unrelated failure became own rejection", op.receipt)
	}
	professionPacket(t, s, opcode.SMSGCastFailed, p)
	if !s.step(op) || op.receipt.Status != professionsv1.AttemptStatus_SERVER_REJECTED || op.receipt.FailureCode != 100 || len(f.packets) != 0 {
		t.Fatal("actual matching server rejection was lost", op.receipt)
	}
}

func TestControllerFishingRejectedBeforeChannelNeedsNoChannelCancellation(t *testing.T) {
	s, f := newProfessionServiceFixture(t)
	professionFishingReadyFixture(t, s, f)
	op := professionDirectOperation(t, s, f, 7620)
	p := protocolFailureFixture(29, 8)
	binary.LittleEndian.PutUint32(p[6:], 2)
	binary.LittleEndian.PutUint32(p[10:], 1<<20)
	professionPacket(t, s, opcode.SMSGCastFailed, p)
	if !s.step(op) {
		t.Fatal("actual fishing cast rejection did not stop")
	}
	s.finish(op)
	r := professionReceipt(s, op)
	if r.Status != professionsv1.AttemptStatus_SERVER_REJECTED || r.FailureCode != 29 || r.OwnGoObserved || r.ChannelStartObserved || r.CancelTransportWritten || r.CleanupUnconfirmed || !r.LeaseReleased || len(f.packets) != 0 || len(f.releases) != 1 {
		t.Fatal("pre-channel rejection fabricated pending channel cleanup", r, f.events)
	}
}

func TestControllerActualPositionCorrectionCannotBeOverriddenByCachedEstimate(t *testing.T) {
	for _, code := range []uint32{opcode.CMSGCastSpell, opcode.CMSGGameobjUse, opcode.CMSGAutostoreLootItem} {
		t.Run(fmt.Sprintf("%x", code), func(t *testing.T) {
			s, f := newProfessionServiceFixture(t)
			f.world.Self.PositionRevision = 10
			f.actions.Position = &actionsv1.ClientPosition{Known: true, MapKnown: true, MapId: 0, X: f.world.Self.X, Y: f.world.Self.Y, Z: f.world.Self.Z, Estimated: true}
			spell := uint32(2538)
			if code == opcode.CMSGCastSpell {
				f.world.InventoryBags[0].Slots[1] = &worldv1.BackpackSlot{Slot: 1, ItemGuid: 0x4000000000000077, Entry: 2672, StackCount: 1, MetadataKnown: true}
				f.world.SpellFocuses = []*worldv1.GameObjectState{{Guid: 0xf110000000000077, Entry: 100, Present: true, TypeKnown: true, GameObjectType: 8, MapKnown: true, MapId: 0, PositionKnown: true, X: f.world.Self.X + 1, Y: f.world.Self.Y, Z: f.world.Self.Z, StateKnown: true, State: 1}}
				s.focusRequested[100] = true
				s.focuses[100] = focusTemplate{entry: 100, focusID: 4, radius: 3}
				professionObserve(t, s)
			} else {
				spell = 7620
				professionFishingReadyFixture(t, s, f)
			}
			op := professionDirectOperation(t, s, f, spell)
			if code != opcode.CMSGCastSpell {
				professionBindBobber(t, s, f, op)
				f.world.OwnFishingBobbers[0].State, f.world.OwnFishingBobbers[0].Flags = 0, 0x20
				professionObserve(t, s)
			}
			f.world = proto.Clone(f.world).(*worldv1.SnapshotResponse)
			f.world.Self.PositionRevision++
			f.world.Self.X += 500
			professionObserve(t, s)
			// The stopped action estimate remains unchanged through renewal.
			// Only the newer actual world position disproves the bound location.
			if code == opcode.CMSGCastSpell && !s.hasFocusLocked(4) {
				t.Fatal("fixture lost cached near-focus estimate")
			}
			if err := s.writeOwned(context.Background(), op, code, []byte{0}); err == nil || len(f.packets) != 0 {
				t.Fatal("cached estimate overrode newer actual position", err, f.events)
			}
		})
	}
}

func TestControllerFiniteContextStopDoesNotResendCast(t *testing.T) {
	s, f := newProfessionServiceFixture(t)
	op := professionDirectOperation(t, s, f, 3275)
	op.done = make(chan struct{})
	op.token, f.token, f.actions.InteractionOwner = "", "", ""
	f.beforeSend = func(p *modv1.SendPacketRequest) {
		if p.Opcode == opcode.CMSGCastSpell {
			op.cancel() // Exercise the finite-bound exit without a10-second wait.
		}
	}
	go s.run(op)
	professionWaitDone(t, op)
	r := professionReceipt(s, op)
	if r.Status != professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED || !strings.Contains(r.Reason, "finite_attempt_deadline") || !r.CleanupUnconfirmed || !r.LeaseReleased {
		t.Fatal("finite stop guessed an outcome", r)
	}
	casts := 0
	for _, packet := range f.packets {
		if packet.Opcode == opcode.CMSGCastSpell {
			casts++
		}
	}
	if casts != 1 {
		t.Fatal("finite stop repeated cast", f.events)
	}
}

func TestControllerCompletedCraftRetainsUnconfirmedCleanup(t *testing.T) {
	s, f := newProfessionServiceFixture(t)
	op := professionDirectOperation(t, s, f, 3275)
	op.receipt.OwnGoObserved, op.receipt.OutputInventoryObserved, op.receipt.ReagentsConsumedObserved = true, true, true
	op.receipt.OwnCreatedItemsObserved = 1
	f.releaseErr = errors.New("MOCK release outcome unknown")
	if !s.step(op) {
		t.Fatal("complete actual craft did not end work loop")
	}
	s.finish(op)
	if r := professionReceipt(s, op); !r.CleanupUnconfirmed || r.LeaseReleased || s.active != nil {
		t.Fatal("failed owned cleanup was reported as confirmed", r)
	}
	if len(f.releases) != 1 || len(f.packets) != 0 {
		t.Fatal("completed craft cleanup repeated or cancelled cast", f.events)
	}
}

func professionFishingReadyFixture(t *testing.T, s *Service, f *professionSessionFixture) {
	t.Helper()
	f.world = proto.Clone(f.world).(*worldv1.SnapshotResponse)
	f.world.Self.ShapeshiftFormKnown = true
	f.world.Self.ChannelObjectKnown = true
	f.world.Self.ChannelSpellKnown = true
	f.world.Self.Mainhand = &worldv1.EquippedWeapon{SlotKnown: true, ItemGuid: 0x4000000000000044, MetadataKnown: true, Entry: 6256, DurabilityKnown: true, Durability: 20, MaxDurability: 20}
	s.tools[6256] = &tradev1.ItemDetails{ItemId: 6256, Available: true, ItemClass: 2, Subclass: 20}
	professionObserve(t, s)
}

const professionBobberGUID = uint64(0xf110008b07000099)

func professionBobberFixture() *worldv1.GameObjectState {
	return &worldv1.GameObjectState{Guid: professionBobberGUID, Entry: 35591, Present: true, TypeKnown: true, GameObjectType: 17, MapKnown: true, MapId: 0, PositionKnown: true, X: -8947, Y: -132, Z: 83, PositionRevision: 11, FlagsKnown: true, CreatedByKnown: true, CreatedByGuid: 7, StateKnown: true, State: 1}
}

func professionBindBobber(t *testing.T, s *Service, f *professionSessionFixture, op *operation) {
	t.Helper()
	f.world = proto.Clone(f.world).(*worldv1.SnapshotResponse)
	f.world.Self.ChannelObjectGuid = professionBobberGUID
	f.world.Self.ChannelSpellId = 7620
	f.world.OwnFishingBobbers = []*worldv1.GameObjectState{professionBobberFixture()}
	professionObserve(t, s)
	professionPacket(t, s, opcode.SMSGSpellGo, professionCastReceipt(opcode.SMSGSpellGo, 7, 9, 7620))
	p := protocolPackedFixture(7)
	p = binary.LittleEndian.AppendUint32(p, 7620)
	p = binary.LittleEndian.AppendUint32(p, 17000)
	professionPacket(t, s, opcode.MSGChannelStart, p)
	if s.step(op) || op.receipt.BobberGuid != professionBobberGUID || !op.receipt.OwnBobberObserved {
		t.Fatal("own channel did not bind actual own bobber", op.receipt)
	}
}

func TestControllerFishingAdmissionRequiresObservedEquipmentAndIdleChannel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Service, *professionSessionFixture)
	}{
		{"old channel", func(_ *Service, f *professionSessionFixture) {
			f.world.Self.ChannelSpellId = 7620
			f.world.Self.ChannelObjectGuid = professionBobberGUID
		}},
		{"unknown channel", func(_ *Service, f *professionSessionFixture) { f.world.Self.ChannelSpellKnown = false }},
		{"excluded form", func(_ *Service, f *professionSessionFixture) { f.world.Self.ShapeshiftForm = 2 }},
		{"unknown form", func(_ *Service, f *professionSessionFixture) { f.world.Self.ShapeshiftFormKnown = false }},
		{"missing pole", func(_ *Service, f *professionSessionFixture) { f.world.Self.Mainhand = nil }},
		{"broken pole", func(_ *Service, f *professionSessionFixture) { f.world.Self.Mainhand.Durability = 0 }},
		{"wrong subclass", func(s *Service, _ *professionSessionFixture) { s.tools[6256].Subclass = 7 }},
		{"unsupported level", func(s *Service, _ *professionSessionFixture) { s.tools[6256].RequiredLevel = 2 }},
		{"old bobber", func(_ *Service, f *professionSessionFixture) {
			f.world.OwnFishingBobbers = []*worldv1.GameObjectState{professionBobberFixture()}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newProfessionServiceFixture(t)
			professionFishingReadyFixture(t, s, f)
			if reason := s.reasonLocked(starterRecipes[3]); reason != "" {
				t.Fatal("invalid ready fixture", reason)
			}
			tc.mutate(s, f)
			professionObserve(t, s)
			if _, err := s.Attempt(context.Background(), professionRequest(s, "blocked-fishing", 7620)); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
				t.Fatal("unsupported fishing readiness sent work", err)
			}
		})
	}
}

func TestControllerFishingForeignBobberAndUnboundTerminalEventsIgnored(t *testing.T) {
	s, f := newProfessionServiceFixture(t)
	professionFishingReadyFixture(t, s, f)
	op := professionDirectOperation(t, s, f, 7620)
	professionPacket(t, s, opcode.SMSGFishNotHooked, nil)
	if op.receipt.Status == professionsv1.AttemptStatus_SERVER_REJECTED {
		t.Fatal("unbound old fishing terminal event rejected this operation")
	}
	f.world.Self.ChannelObjectGuid, f.world.Self.ChannelSpellId = professionBobberGUID, 7620
	bobber := professionBobberFixture()
	bobber.CreatedByGuid = 8
	f.world.OwnFishingBobbers = []*worldv1.GameObjectState{bobber}
	professionObserve(t, s)
	professionPacket(t, s, opcode.SMSGSpellGo, professionCastReceipt(opcode.SMSGSpellGo, 7, 9, 7620))
	channel := binary.LittleEndian.AppendUint32(protocolPackedFixture(7), 7620)
	channel = binary.LittleEndian.AppendUint32(channel, 17000)
	professionPacket(t, s, opcode.MSGChannelStart, channel)
	if s.step(op) || op.receipt.OwnBobberObserved || op.receipt.BobberGuid != 0 || len(f.packets) != 0 {
		t.Fatal("foreign creator bobber was used", op.receipt)
	}
}

func TestControllerFishingRechecksBoundBobberAndChannelBeforeReel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*worldv1.SnapshotResponse)
	}{
		{"cleared channel", func(w *worldv1.SnapshotResponse) { w.Self.ChannelSpellId = 0; w.Self.ChannelObjectGuid = 0 }},
		{"different channel", func(w *worldv1.SnapshotResponse) { w.Self.ChannelSpellId = 1234 }},
		{"different object", func(w *worldv1.SnapshotResponse) { w.Self.ChannelObjectGuid++ }},
		{"creator changed", func(w *worldv1.SnapshotResponse) { w.OwnFishingBobbers[0].CreatedByGuid = 8 }},
		{"type changed", func(w *worldv1.SnapshotResponse) { w.OwnFishingBobbers[0].GameObjectType = 3 }},
		{"map changed", func(w *worldv1.SnapshotResponse) { w.OwnFishingBobbers[0].MapId = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newProfessionServiceFixture(t)
			professionFishingReadyFixture(t, s, f)
			op := professionDirectOperation(t, s, f, 7620)
			professionBindBobber(t, s, f, op)
			f.world = proto.Clone(f.world).(*worldv1.SnapshotResponse)
			f.world.OwnFishingBobbers[0].State = 0
			f.world.OwnFishingBobbers[0].Flags = 0x20
			tc.mutate(f.world)
			professionObserve(t, s)
			s.step(op)
			if len(f.packets) != 0 || op.reelReserved {
				t.Fatal("stale/foreign channel or bobber triggered reel", f.events)
			}
		})
	}
}

func TestControllerFishingLootPushAndRemovalEitherOrder(t *testing.T) {
	for _, pushFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(pushFirst), func(t *testing.T) {
			s, f := newProfessionServiceFixture(t)
			professionFishingReadyFixture(t, s, f)
			op := professionDirectOperation(t, s, f, 7620)
			professionBindBobber(t, s, f, op)
			f.world.OwnFishingBobbers[0].State, f.world.OwnFishingBobbers[0].Flags = 0, 0x20
			professionObserve(t, s)
			if s.step(op) || !op.reelReserved || len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGGameobjUse {
				t.Fatal("ready own bobber did not reel once", op.receipt)
			}
			window := protocolFishingLootFixture(1)
			binary.LittleEndian.PutUint64(window, professionBobberGUID)
			binary.LittleEndian.PutUint32(window[9:], 0)
			bad := append([]byte{}, window...)
			bad[8] = 1
			professionPacket(t, s, opcode.SMSGLootResponse, bad)
			if op.window != nil {
				t.Fatal("ordinary loot type became fishing")
			}
			foreign := append([]byte{}, window...)
			binary.LittleEndian.PutUint64(foreign, professionBobberGUID+1)
			professionPacket(t, s, opcode.SMSGLootResponse, foreign)
			if op.window != nil {
				t.Fatal("foreign fishing window became own")
			}
			professionPacket(t, s, opcode.SMSGLootResponse, window)
			if s.step(op) || !op.stored[0] || len(f.packets) != 2 || f.packets[1].Opcode != opcode.CMSGAutostoreLootItem {
				t.Fatal("fishing slot was not reserved once", op.receipt)
			}
			push := func() {
				professionPacket(t, s, opcode.SMSGItemPushResult, professionPushReceipt(7, false, false, 6291, 1, 1))
			}
			removed := func() { professionPacket(t, s, opcode.SMSGLootRemoved, []byte{0}) }
			if pushFirst {
				push()
				removed()
			} else {
				removed()
				push()
			}
			if !op.pushed[0] || !op.removed[0] || op.receipt.OwnLootedItemsObserved != 1 {
				t.Fatal("real looted flags/order did not prove pickup", op.receipt)
			}
			if s.step(op) || !op.releaseReserved || len(f.packets) != 3 || f.packets[2].Opcode != opcode.CMSGLootRelease {
				t.Fatal("release did not reserve once", op.receipt)
			}
			professionPacket(t, s, opcode.SMSGLootReleaseResponse, append(fullGUIDPacket(professionBobberGUID), 1))
			professionPacket(t, s, opcode.MSGChannelUpdate, binary.LittleEndian.AppendUint32(protocolPackedFixture(7), 0))
			if s.step(op) {
				t.Fatal("cast/loot transport receipts alone completed fishing")
			}
			f.world = proto.Clone(f.world).(*worldv1.SnapshotResponse)
			f.world.BackpackRevision++
			f.world.InventoryRevision++
			f.world.InventoryBags[0].Slots[1] = &worldv1.BackpackSlot{Slot: 1, ItemGuid: 0x4000000000000077, Entry: 6291, StackCount: 1, MetadataKnown: true}
			professionObserve(t, s)
			if !s.step(op) || op.receipt.Status != professionsv1.AttemptStatus_COMPLETED || !op.receipt.OutputInventoryObserved || op.receipt.SkillIncreased {
				t.Fatal("actual fishing evidence did not complete gray catch", op.receipt)
			}
			if len(f.packets) != 3 {
				t.Fatal("fishing write repeated", f.events)
			}
		})
	}
}

func TestControllerFishingNotHookedStopsWithoutReel(t *testing.T) {
	s, f := newProfessionServiceFixture(t)
	professionFishingReadyFixture(t, s, f)
	op := professionDirectOperation(t, s, f, 7620)
	professionBindBobber(t, s, f, op)
	professionPacket(t, s, opcode.SMSGFishNotHooked, nil)
	if !s.step(op) || op.receipt.Status != professionsv1.AttemptStatus_SERVER_REJECTED || op.receipt.Reason != "fish_not_hooked" || len(f.packets) != 0 {
		t.Fatal("not-hooked did not stop bound operation", op.receipt)
	}
}

func TestControllerFishingRejectsAggregateOverflowBeforeStore(t *testing.T) {
	for _, sameEntry := range []bool{true, false} {
		t.Run(fmt.Sprint(sameEntry), func(t *testing.T) {
			s, f := newProfessionServiceFixture(t)
			professionFishingReadyFixture(t, s, f)
			op := professionDirectOperation(t, s, f, 7620)
			op.reelReserved, op.receipt.BobberGuid = true, professionBobberGUID
			p := protocolFishingLootFixture(2)
			binary.LittleEndian.PutUint64(p, professionBobberGUID)
			binary.LittleEndian.PutUint32(p[9:], 0)
			binary.LittleEndian.PutUint32(p[19:], math.MaxUint32)
			if sameEntry {
				binary.LittleEndian.PutUint32(p[37:], 6291)
			}
			binary.LittleEndian.PutUint32(p[41:], 1)
			professionPacket(t, s, opcode.SMSGLootResponse, p)
			s.step(op)
			if len(f.packets) != 0 || op.window != nil {
				t.Fatal("overflowing fishing counts authorized pickup", op.receipt)
			}
		})
	}
}

func TestControllerFishingAbortReleasesOpenWindowAndWaitsForActualAcks(t *testing.T) {
	s, f := newProfessionServiceFixture(t)
	professionFishingReadyFixture(t, s, f)
	op := professionDirectOperation(t, s, f, 7620)
	professionBindBobber(t, s, f, op)
	f.world.OwnFishingBobbers[0].State, f.world.OwnFishingBobbers[0].Flags = 0, 0x20
	professionObserve(t, s)
	if s.step(op) || !op.reelReserved {
		t.Fatal("ready bobber was not used")
	}
	p := protocolFishingLootFixture(1)
	binary.LittleEndian.PutUint64(p, professionBobberGUID)
	binary.LittleEndian.PutUint32(p[9:], 0)
	professionPacket(t, s, opcode.SMSGLootResponse, p)
	if op.window == nil {
		t.Fatal("actual fishing window missing")
	}
	if _, err := s.Cancel(context.Background(), &professionsv1.CancelRequest{SessionId: "body", OperationId: op.receipt.OperationId}); err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	go func() { s.finish(op); close(finished) }()
	professionWaitPacket(t, f, opcode.CMSGLootRelease)
	professionWaitPacket(t, f, opcode.CMSGCancelChannelling)
	s.mu.Lock()
	if s.active != op || op.receipt.LeaseReleased || op.receipt.LootReleaseObserved || op.receipt.ChannelStopObserved {
		s.mu.Unlock()
		t.Fatal("cleanup transport manufactured acknowledgements")
	}
	s.mu.Unlock()
	// Receipts arrive after both writes, while cleanup still owns the attempt.
	professionPacket(t, s, opcode.SMSGLootReleaseResponse, append(fullGUIDPacket(professionBobberGUID), 1))
	professionPacket(t, s, opcode.MSGChannelUpdate, binary.LittleEndian.AppendUint32(protocolPackedFixture(7), 0))
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("acknowledged cleanup did not finish")
	}
	if r := professionReceipt(s, op); !r.LootReleaseObserved || !r.ChannelStopObserved || !r.LeaseReleased || r.CleanupUnconfirmed {
		t.Fatal("actual asynchronous cleanup was not confirmed", r)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.packets) != 3 || len(f.releases) != 1 {
		t.Fatal("abort repeated release/cancel or picked up items", f.events)
	}
}

func TestControllerFishingLateWindowDuringCleanupAndMissingWindowRemainDistinct(t *testing.T) {
	for _, lateWindow := range []bool{true, false} {
		t.Run(fmt.Sprint(lateWindow), func(t *testing.T) {
			s, f := newProfessionServiceFixture(t)
			professionFishingReadyFixture(t, s, f)
			op := professionDirectOperation(t, s, f, 7620)
			professionBindBobber(t, s, f, op)
			f.world.OwnFishingBobbers[0].State, f.world.OwnFishingBobbers[0].Flags = 0, 0x20
			professionObserve(t, s)
			if s.step(op) || !op.reelReserved {
				t.Fatal("reel not reserved")
			}
			if _, err := s.Cancel(context.Background(), &professionsv1.CancelRequest{SessionId: "body", OperationId: op.receipt.OperationId}); err != nil {
				t.Fatal(err)
			}
			finished := make(chan struct{})
			go func() { s.finish(op); close(finished) }()
			professionWaitPacket(t, f, opcode.CMSGCancelChannelling)
			if lateWindow {
				p := protocolFishingLootFixture(1)
				binary.LittleEndian.PutUint64(p, professionBobberGUID)
				binary.LittleEndian.PutUint32(p[9:], 0)
				professionPacket(t, s, opcode.SMSGLootResponse, p)
				professionPacket(t, s, opcode.SMSGLootResponse, p)
				professionWaitPacket(t, f, opcode.CMSGLootRelease)
				professionPacket(t, s, opcode.SMSGLootReleaseResponse, append(fullGUIDPacket(professionBobberGUID), 1))
			}
			professionPacket(t, s, opcode.MSGChannelUpdate, binary.LittleEndian.AppendUint32(protocolPackedFixture(7), 0))
			select {
			case <-finished:
			case <-time.After(2 * time.Second):
				t.Fatal("finite late-window cleanup did not end")
			}
			r := professionReceipt(s, op)
			if !r.ChannelStopObserved || !r.LeaseReleased || r.CleanupUnconfirmed == lateWindow || r.LootReleaseObserved != lateWindow {
				t.Fatal("missing and confirmed late responses conflated", r)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			counts := map[uint32]int{}
			for _, packet := range f.packets {
				counts[packet.Opcode]++
			}
			if counts[opcode.CMSGGameobjUse] != 1 || counts[opcode.CMSGCancelChannelling] != 1 || counts[opcode.CMSGAutostoreLootItem] != 0 || counts[opcode.CMSGLootRelease] != int(boolUint(lateWindow)) {
				t.Fatal("late cleanup retried work or picked up items", f.events)
			}
		})
	}
}
