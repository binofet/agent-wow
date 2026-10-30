package buddymail

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"
	"time"

	mailv1 "github.com/agent-wow/agent-wow/modules/buddy-mail/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func partyBagList(count int, mailID, firstLow uint32) []byte {
	row := make([]byte, 43)
	binary.LittleEndian.PutUint32(row[2:], mailID)
	binary.LittleEndian.PutUint32(row[23:], 61)
	row = append(row, []byte("Party bags\x00\x00")...)
	row = append(row, byte(count))
	for n := 0; n < count; n++ {
		a := make([]byte, 118)
		a[0] = byte(n)
		binary.LittleEndian.PutUint32(a[1:], firstLow+uint32(n))
		binary.LittleEndian.PutUint32(a[5:], partyBagEntry)
		binary.LittleEndian.PutUint32(a[101:], 1)
		row = append(row, a...)
	}
	binary.LittleEndian.PutUint16(row, uint16(len(row)))
	return append([]byte{1, 0, 0, 0, 1}, row...)
}

func bagAck(mailID, low, count, result uint32) []byte {
	p := make([]byte, 20)
	binary.LittleEndian.PutUint32(p, mailID)
	binary.LittleEndian.PutUint32(p[4:], 2)
	binary.LittleEndian.PutUint32(p[8:], result)
	if result == 1 {
		return p[:16]
	}
	binary.LittleEndian.PutUint32(p[12:], low)
	binary.LittleEndian.PutUint32(p[16:], count)
	return p
}

type bagFake struct {
	*fakeSession
	ackBag, addBag, failBag              bool
	ackMail, ackLow, ackCount, ackResult uint32
	mutation                             func(*worldv1.SnapshotResponse)
	beforeBagReturn                      func()
}

func (f *bagFake) addLocked(low uint32) {
	for _, slot := range f.w.InventoryBags[0].Slots {
		if slot.ItemGuid == 0 {
			slot.ItemGuid, slot.Entry, slot.StackCount, slot.MetadataKnown = bagItemGUID(low), partyBagEntry, 1, true
			f.w.InventoryRevision++
			return
		}
	}
}
func (f *bagFake) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	if r.Opcode != opcode.CMSGMailTakeItem {
		return f.fakeSession.SendPacket(ctx, r, opts...)
	}
	f.mu.Lock()
	f.sends = append(f.sends, proto.Clone(r).(*modv1.SendPacketRequest))
	low := binary.LittleEndian.Uint32(r.Payload[12:])
	if f.addBag {
		f.addLocked(low)
	}
	if f.mutation != nil {
		f.mutation(f.w)
	}
	ack, fail := f.ackBag, f.failBag
	p := bagAck(f.ackMail, f.ackLow, f.ackCount, f.ackResult)
	f.mu.Unlock()
	if ack {
		_, _ = f.s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSendMailResult, Payload: p})
	}
	if f.beforeBagReturn != nil {
		f.beforeBagReturn()
	}
	if fail {
		return nil, errors.New("ambiguous item write")
	}
	return &emptypb.Empty{}, nil
}

func bagRig(t *testing.T) (*Service, *bagFake) {
	t.Helper()
	s, f := rig(t)
	f.w.Self.ChannelObjectKnown, f.w.Self.ChannelSpellKnown = true, true
	f.w.InventoryKnown, f.w.InventoryRevision = true, 10
	for n := uint32(0); n < 5; n++ {
		b := &worldv1.InventoryBag{Bag: n, SlotKnown: true, ContentsKnown: true}
		if n == 0 {
			b.SlotCount = 16
			for slot := uint32(0); slot < 16; slot++ {
				b.Slots = append(b.Slots, &worldv1.BackpackSlot{Bag: 0, Slot: slot})
			}
		}
		f.w.InventoryBags = append(f.w.InventoryBags, b)
	}
	f.list = partyBagList(4, 77, 1000)
	g := &bagFake{fakeSession: f, ackBag: true, addBag: true, ackMail: 77, ackLow: 1000, ackCount: 1}
	s.session = g
	return s, g
}
func bagListing(t *testing.T, s *Service, f *bagFake, id string) *mailv1.ClaimBagAttachmentRequest {
	t.Helper()
	s.mu.Lock()
	s.nextList = time.Time{}
	s.mu.Unlock()
	v, err := s.List(context.Background(), &mailv1.ListRequest{})
	if err != nil || len(v.BagMails) != 1 {
		t.Fatalf("list=%+v err=%v", v, err)
	}
	m, a := v.BagMails[0], v.BagMails[0].Attachments[0]
	return &mailv1.ClaimBagAttachmentRequest{SessionId: v.SessionId, RequestId: id, ListRevision: v.ListRevision, MailboxGuid: v.MailboxGuid, MailId: m.MailId, AttachmentIndex: a.AttachmentIndex, ItemGuidLow: a.ItemGuidLow, ExpectedCount: a.Count, ExpectedInventoryRevision: f.w.InventoryRevision}
}

func TestBagListUsesOwnedConsoleSenderAndPreservesClaimProof(t *testing.T) {
	for _, self := range []uint64{1, 4, 5, 6, 8} {
		t.Run(fmt.Sprint(self), func(t *testing.T) {
			s, f := bagRig(t)
			s.self, f.w.Self.Guid = self, self
			binary.LittleEndian.PutUint64(f.list[5+7:], self)
			request := bagListing(t, s, f, "self-sender")
			state, err := s.ClaimBagAttachment(context.Background(), request)
			if err != nil || state.LastBagClaim == nil || state.LastBagClaim.Status != mailv1.BagClaimStatus_BAG_CLAIM_CONFIRMED || !state.LastBagClaim.ServerAcknowledged || !state.LastBagClaim.InventoryConfirmed || !state.LastBagClaim.LeaseReleased {
				t.Fatal(state, err)
			}
			for _, sender := range []uint64{0, self + 1} {
				s, f = bagRig(t)
				s.self, f.w.Self.Guid = self, self
				binary.LittleEndian.PutUint64(f.list[5+7:], sender)
				state, err = s.List(context.Background(), &mailv1.ListRequest{})
				want := 0
				if sender == 0 {
					want = 1
				}
				if err != nil || state.ListedMailCount != 1 || len(state.BagMails) != want || len(f.sends) != 1 {
					t.Fatal("sender filtering must not claim", sender, state, err)
				}
			}
		})
	}
}

func TestBagClaimNeedsExactAckAndInventory(t *testing.T) {
	for _, name := range []string{"both", "ack_only", "inventory_only", "wrong_item", "wrong_count", "wrong_mail", "unchanged_revision", "other_inventory_changed", "unknown_metadata", "duplicate_guid", "server_rejected", "transport_error"} {
		t.Run(name, func(t *testing.T) {
			s, f := bagRig(t)
			r := bagListing(t, s, f, "op")
			want := mailv1.BagClaimStatus_BAG_CLAIM_UNCONFIRMED
			switch name {
			case "both":
				want = mailv1.BagClaimStatus_BAG_CLAIM_CONFIRMED
			case "ack_only":
				f.addBag = false
			case "inventory_only":
				f.ackBag = false
			case "wrong_item":
				f.ackLow++
			case "wrong_count":
				f.ackCount = 2
			case "wrong_mail":
				f.ackMail++
			case "unchanged_revision":
				f.mutation = func(w *worldv1.SnapshotResponse) { w.InventoryRevision-- }
			case "other_inventory_changed":
				f.mutation = func(w *worldv1.SnapshotResponse) {
					w.InventoryBags[0].Slots[1] = &worldv1.BackpackSlot{Slot: 1, ItemGuid: bagItemGUID(9999), Entry: 1, StackCount: 1, MetadataKnown: true}
				}
			case "unknown_metadata":
				f.mutation = func(w *worldv1.SnapshotResponse) { w.InventoryBags[0].Slots[0].MetadataKnown = false }
			case "duplicate_guid":
				f.mutation = func(w *worldv1.SnapshotResponse) {
					w.InventoryBags[0].Slots[1] = proto.Clone(w.InventoryBags[0].Slots[0]).(*worldv1.BackpackSlot)
					w.InventoryBags[0].Slots[1].Slot = 1
				}
			case "server_rejected":
				f.addBag = false
				f.ackResult = 1
				want = mailv1.BagClaimStatus_BAG_CLAIM_SERVER_REJECTED
			case "transport_error":
				f.ackBag = false
				f.addBag = false
				f.failBag = true
				want = mailv1.BagClaimStatus_BAG_CLAIM_WRITE_UNCONFIRMED
			}
			v, err := s.ClaimBagAttachment(context.Background(), r)
			if err != nil || v.LastBagClaim.Status != want || !v.LastBagClaim.LeaseReleased || !proto.Equal(v.LastBagClaim.Request, r) || !v.LastBagClaim.TransportAttempted {
				t.Fatalf("claim=%+v err=%v", v, err)
			}
			if len(f.sends) != 2 || f.sends[1].Opcode != opcode.CMSGMailTakeItem || len(f.sends[1].Payload) != 16 || f.releases != 2 {
				t.Fatal("packet/release count")
			}
			if v.LastClaim != nil || v.ClaimReserved {
				t.Fatal("changed stipend semantics")
			}
			if v.BagsClaimed != uint32(map[bool]int{true: 1, false: 0}[want == mailv1.BagClaimStatus_BAG_CLAIM_CONFIRMED]) {
				t.Fatal("optimistic bag count")
			}
		})
	}
}

func TestBagPrewriteBlockRetainsRequestAndNoAttempt(t *testing.T) {
	for _, name := range []string{"stale_revision", "unknown_inventory", "missing_bag", "unknown_slot", "already_owned", "owned_container", "full_backpack", "mail_changed", "post_lease_change", "channel", "owner_combat", "moving"} {
		t.Run(name, func(t *testing.T) {
			s, f := bagRig(t)
			r := bagListing(t, s, f, "blocked")
			switch name {
			case "stale_revision":
				r.ExpectedInventoryRevision++
			case "unknown_inventory":
				f.w.InventoryKnown = false
			case "missing_bag":
				f.w.InventoryBags = f.w.InventoryBags[:4]
			case "unknown_slot":
				f.w.InventoryBags[0].Slots[0].MetadataKnown = true
			case "already_owned":
				f.addLocked(r.ItemGuidLow)
			case "owned_container":
				b := f.w.InventoryBags[1]
				b.ContainerGuid = bagItemGUID(r.ItemGuidLow)
				b.Entry = partyBagEntry
				b.MetadataKnown = true
				b.SlotCount = 1
				b.Slots = []*worldv1.BackpackSlot{{Bag: 1, Slot: 0}}
			case "full_backpack":
				for n, slot := range f.w.InventoryBags[0].Slots {
					slot.ItemGuid = bagItemGUID(2000 + uint32(n))
					slot.Entry = 1
					slot.StackCount = 1
					slot.MetadataKnown = true
				}
			case "mail_changed":
				r.MailId++
			case "post_lease_change":
				f.afterAcquire = func() { f.mu.Lock(); f.w.InventoryRevision++; f.mu.Unlock() }
			case "channel":
				f.w.Self.ChannelSpellId = 1
			case "owner_combat":
				f.w.Owner = &worldv1.PlayerState{UnitFlagsKnown: true, UnitFlags: 0x80000}
			case "moving":
				f.a.Moving = true
			}
			v, err := s.ClaimBagAttachment(context.Background(), r)
			if err != nil || v.LastBagClaim.Status != mailv1.BagClaimStatus_BAG_CLAIM_BLOCKED || !proto.Equal(v.LastBagClaim.Request, r) || !v.LastBagClaim.LeaseReleased || v.LastBagClaim.TransportAttempted || v.BagsClaimReserved != 0 || v.OperationInFlight || len(f.sends) != 1 {
				t.Fatalf("v=%+v err=%v sends=%d", v, err, len(f.sends))
			}
		})
	}
}

func TestBagImmutableDedupeAndUnknownBlocksAcrossLists(t *testing.T) {
	s, f := bagRig(t)
	r := bagListing(t, s, f, "unknown")
	f.ackBag = false
	f.addBag = false
	v, err := s.ClaimBagAttachment(context.Background(), r)
	if err != nil || v.LastBagClaim.Status != mailv1.BagClaimStatus_BAG_CLAIM_UNCONFIRMED {
		t.Fatal(v, err)
	}
	if _, err = s.ClaimBagAttachment(context.Background(), r); err != nil || len(f.sends) != 2 {
		t.Fatal("duplicate wrote", err)
	}
	changed := proto.Clone(r).(*mailv1.ClaimBagAttachmentRequest)
	changed.ExpectedInventoryRevision++
	if _, err = s.ClaimBagAttachment(context.Background(), changed); status.Code(err) != codes.AlreadyExists {
		t.Fatal("mutated id accepted", err)
	}
	next := bagListing(t, s, f, "new-after-refresh")
	v, err = s.ClaimBagAttachment(context.Background(), next)
	if err != nil || v.LastBagClaim.Status != mailv1.BagClaimStatus_BAG_CLAIM_BLOCKED || len(f.sends) != 3 || v.BagsClaimReserved != 1 {
		t.Fatal("unknown bypass", v, err)
	}
	// Late real evidence resolves the old receipt without any write.
	f.mu.Lock()
	f.addLocked(r.ItemGuidLow)
	f.mu.Unlock()
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSendMailResult, Payload: bagAck(r.MailId, r.ItemGuidLow, 1, 0)})
	v, err = s.GetState(context.Background(), &emptypb.Empty{})
	if err != nil || v.BagClaims[0].Status != mailv1.BagClaimStatus_BAG_CLAIM_CONFIRMED || v.BagsClaimed != 1 || len(f.sends) != 3 {
		t.Fatal("late proof", v, err)
	}
	_, _ = s.GetState(context.Background(), &emptypb.Empty{})
	if s.state.BagsClaimed != 1 {
		t.Fatal("double counted")
	}
}

func TestBagCapAcrossEnvelopesAndAttachmentReplay(t *testing.T) {
	s, f := bagRig(t)
	for n := uint32(0); n < 5; n++ {
		f.list = partyBagList(1, 80+n, 1000+n)
		f.ackMail, f.ackLow = 80+n, 1000+n
		r := bagListing(t, s, f, fmt.Sprintf("op%d", n))
		v, err := s.ClaimBagAttachment(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		if n < 4 && v.LastBagClaim.Status != mailv1.BagClaimStatus_BAG_CLAIM_CONFIRMED {
			t.Fatal(v)
		}
		if n == 4 && (v.LastBagClaim.Status != mailv1.BagClaimStatus_BAG_CLAIM_BLOCKED || v.BagsClaimReserved != 4 || v.BagsClaimed != 4) {
			t.Fatal("cap", v)
		}
	}
	if len(f.sends) != 9 {
		t.Fatal("more than four item sends")
	}
	// Separate IDs never authorize another possible send for the same GUID.
	s, f = bagRig(t)
	r := bagListing(t, s, f, "one")
	_, _ = s.ClaimBagAttachment(context.Background(), r)
	r.RequestId = "two"
	v, err := s.ClaimBagAttachment(context.Background(), r)
	if err != nil || v.LastBagClaim.Status != mailv1.BagClaimStatus_BAG_CLAIM_BLOCKED || len(f.sends) != 2 {
		t.Fatal("attachment replay", v, err)
	}
}

func TestBagCancellationAndLogoutNeverReplay(t *testing.T) {
	s, f := bagRig(t)
	r := bagListing(t, s, f, "canceled")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ClaimBagAttachment(ctx, r); status.Code(err) != codes.Canceled {
		t.Fatal(err)
	}
	if len(f.sends) != 1 || len(s.state.BagClaims) != 0 {
		t.Fatal("canceled request mutated")
	}
	f.ackBag = false
	f.addBag = false
	f.mutation = func(*worldv1.SnapshotResponse) { _, _ = s.BeforeLogout(context.Background(), &emptypb.Empty{}) }
	v, err := s.ClaimBagAttachment(context.Background(), r)
	if err != nil || v.LastBagClaim.Status != mailv1.BagClaimStatus_BAG_CLAIM_UNCONFIRMED || !v.LastBagClaim.LeaseReleased || len(f.sends) != 2 {
		t.Fatal(v, err)
	}
	_, _ = s.ClaimBagAttachment(context.Background(), r)
	_, _ = s.GetState(context.Background(), &emptypb.Empty{})
	if len(f.sends) != 2 {
		t.Fatal("logout replay")
	}
}

func TestBagPostLeaseCancelAndOwnerCombatReleasePromptly(t *testing.T) {
	t.Run("cancel_after_lease", func(t *testing.T) {
		s, f := bagRig(t)
		r := bagListing(t, s, f, "cancel-after-lease")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f.afterAcquire = cancel
		v, err := s.ClaimBagAttachment(ctx, r)
		if err != nil || v.LastBagClaim.Status != mailv1.BagClaimStatus_BAG_CLAIM_BLOCKED || !v.LastBagClaim.LeaseReleased || v.LastBagClaim.TransportAttempted || len(f.sends) != 1 || f.releases != 2 {
			t.Fatal(v, err)
		}
	})
	t.Run("owner_combat_after_one_packet", func(t *testing.T) {
		s, f := bagRig(t)
		r := bagListing(t, s, f, "owner-combat")
		s.timeout = 5 * time.Second
		f.ackBag, f.addBag = false, false
		f.mutation = func(w *worldv1.SnapshotResponse) {
			w.Owner = &worldv1.PlayerState{UnitFlagsKnown: true, UnitFlags: 0x80000}
		}
		start := time.Now()
		v, err := s.ClaimBagAttachment(context.Background(), r)
		if err != nil || time.Since(start) > time.Second || v.LastBagClaim.Status != mailv1.BagClaimStatus_BAG_CLAIM_UNCONFIRMED || !v.LastBagClaim.LeaseReleased || len(f.sends) != 2 || f.releases != 2 {
			t.Fatal(v, err)
		}
	})
}

func TestBagHistoryBoundAndStaleLateInventoryCannotConfirm(t *testing.T) {
	t.Run("bounded_history", func(t *testing.T) {
		s, f := bagRig(t)
		r := bagListing(t, s, f, "blocked")
		r.MailId++
		for n := 0; n < 128; n++ {
			r.RequestId = fmt.Sprintf("blocked%d", n)
			if _, err := s.ClaimBagAttachment(context.Background(), r); err != nil {
				t.Fatal(n, err)
			}
		}
		r.RequestId = "overflow"
		if _, err := s.ClaimBagAttachment(context.Background(), r); status.Code(err) != codes.ResourceExhausted {
			t.Fatal(err)
		}
		if len(s.state.BagClaims) != 128 || len(f.sends) != 1 {
			t.Fatal("history/write bound")
		}
		r.RequestId = "blocked0"
		if _, err := s.ClaimBagAttachment(context.Background(), r); err != nil {
			t.Fatal("dedupe at capacity", err)
		}
	})
	t.Run("older_late_snapshot", func(t *testing.T) {
		s, f := bagRig(t)
		r := bagListing(t, s, f, "old-snapshot")
		f.ackBag, f.addBag = false, false
		_, _ = s.ClaimBagAttachment(context.Background(), r)
		f.w.InventoryRevision = 20
		_, _ = s.GetState(context.Background(), &emptypb.Empty{})
		f.addLocked(r.ItemGuidLow)
		f.w.InventoryRevision = 11
		_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSendMailResult, Payload: bagAck(r.MailId, r.ItemGuidLow, 1, 0)})
		v, err := s.GetState(context.Background(), &emptypb.Empty{})
		if err != nil || v.LastBagClaim.Status == mailv1.BagClaimStatus_BAG_CLAIM_CONFIRMED || v.LastBagClaim.ObservedInventoryRevision != 20 {
			t.Fatal(v, err)
		}
	})
}

func TestBagAllInventoryLayoutsAndOtherSlotsMustMatch(t *testing.T) {
	s, f := bagRig(t)
	before := proto.Clone(f.w).(*worldv1.SnapshotResponse)
	b := before.InventoryBags[1]
	b.ContainerGuid = bagItemGUID(7000)
	b.Entry = partyBagEntry
	b.MetadataKnown = true
	b.SlotCount = 2
	b.Slots = []*worldv1.BackpackSlot{{Bag: 1, Slot: 0, ItemGuid: bagItemGUID(7001), Entry: 2320, StackCount: 4, MetadataKnown: true}, {Bag: 1, Slot: 1}}
	for _, name := range []string{"valid_bag_addition", "unknown_layout", "changed_container", "changed_stack", "duplicate_bag", "duplicate_slot", "invalid_guid"} {
		t.Run(name, func(t *testing.T) {
			after := proto.Clone(before).(*worldv1.SnapshotResponse)
			after.InventoryRevision++
			after.InventoryBags[1].Slots[1] = &worldv1.BackpackSlot{Bag: 1, Slot: 1, ItemGuid: bagItemGUID(1000), Entry: partyBagEntry, StackCount: 1, MetadataKnown: true}
			switch name {
			case "unknown_layout":
				after.InventoryBags[1].ContentsKnown = false
			case "changed_container":
				after.InventoryBags[1].ContainerGuid = bagItemGUID(7002)
			case "changed_stack":
				after.InventoryBags[1].Slots[0].StackCount--
			case "duplicate_bag":
				after.InventoryBags[4].Bag = 3
			case "duplicate_slot":
				after.InventoryBags[1].Slots[1].Slot = 0
			case "invalid_guid":
				after.InventoryBags[1].Slots[1].ItemGuid = 1000
			}
			bag, slot, ok := exactBagAddition(before, after, bagItemGUID(1000))
			if ok != (name == "valid_bag_addition") || ok && (bag != 1 || slot != 1) {
				t.Fatal(name, bag, slot, ok)
			}
		})
	}
	_ = s
}

func TestBagConcurrentDuplicateCannotObserveTerminalPreflightBlock(t *testing.T) {
	s, f := bagRig(t)
	r := bagListing(t, s, f, "concurrent")
	checked := false
	f.afterAcquire = func() {
		if checked {
			return
		}
		checked = true
		v, err := s.ClaimBagAttachment(context.Background(), r)
		if err != nil || v.LastBagClaim.Status != mailv1.BagClaimStatus_BAG_CLAIM_REQUESTED || !v.OperationInFlight || v.LastBagClaim.TransportAttempted {
			t.Fatal(v, err)
		}
	}
	v, err := s.ClaimBagAttachment(context.Background(), r)
	if err != nil || !checked || v.LastBagClaim.Status != mailv1.BagClaimStatus_BAG_CLAIM_CONFIRMED || len(f.sends) != 2 {
		t.Fatal(v, err)
	}
}

func TestBagAmbiguousTransportCannotOverwriteConfirmedEvidence(t *testing.T) {
	s, f := bagRig(t)
	r := bagListing(t, s, f, "transport-after-proof")
	f.failBag = true
	f.beforeBagReturn = func() { _, _ = s.GetState(context.Background(), &emptypb.Empty{}) }
	v, err := s.ClaimBagAttachment(context.Background(), r)
	if err != nil || v.LastBagClaim.Status != mailv1.BagClaimStatus_BAG_CLAIM_CONFIRMED || !v.LastBagClaim.ServerAcknowledged || !v.LastBagClaim.InventoryConfirmed || v.BagsClaimed != 1 || !v.LastBagClaim.LeaseReleased {
		t.Fatal(v, err)
	}
	v, err = s.GetState(context.Background(), &emptypb.Empty{})
	if err != nil || v.BagsClaimed != 1 || len(f.sends) != 2 {
		t.Fatal("proof replay", v, err)
	}
}
