package buddymail

import (
	"context"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	mailv1 "github.com/agent-wow/agent-wow/modules/buddy-mail/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

const partyBagEntry uint32 = 41599

func bagItemGUID(low uint32) uint64  { return uint64(0x4000)<<48 | uint64(low) }
func validBagItemGUID(g uint64) bool { return g>>32 == 0x40000000 && uint32(g) != 0 }

// Require every carried location, including occupied-item metadata. World's
// inventory_known alone deliberately does not assert every item's metadata.
func bagInventory(w *worldv1.SnapshotResponse) ([]*worldv1.InventoryBag, bool) {
	if w == nil || !w.InventoryKnown || w.InventoryRevision == 0 || len(w.InventoryBags) != 5 {
		return nil, false
	}
	bags := make([]*worldv1.InventoryBag, 5)
	seen := make(map[uint64]bool, 164)
	for _, b := range w.InventoryBags {
		if b == nil || b.Bag > 4 || bags[b.Bag] != nil || !b.SlotKnown || !b.ContentsKnown || b.SlotCount > 36 || len(b.Slots) != int(b.SlotCount) {
			return nil, false
		}
		bags[b.Bag] = b
		if b.Bag == 0 {
			if b.ContainerGuid != 0 || b.Entry != 0 || b.MetadataKnown || b.SlotCount != 16 {
				return nil, false
			}
		} else if b.ContainerGuid == 0 {
			if b.Entry != 0 || b.MetadataKnown || b.SlotCount != 0 {
				return nil, false
			}
		} else {
			if !validBagItemGUID(b.ContainerGuid) || !b.MetadataKnown || b.Entry == 0 || b.SlotCount == 0 || seen[b.ContainerGuid] {
				return nil, false
			}
			seen[b.ContainerGuid] = true
		}
	}
	for _, b := range bags {
		if b == nil {
			return nil, false
		}
		slots := make([]bool, b.SlotCount)
		for _, item := range b.Slots {
			if item == nil || item.Bag != b.Bag || item.Slot >= b.SlotCount || slots[item.Slot] {
				return nil, false
			}
			slots[item.Slot] = true
			if item.ItemGuid == 0 {
				if item.Entry != 0 || item.StackCount != 0 || item.MetadataKnown {
					return nil, false
				}
			} else {
				if !validBagItemGUID(item.ItemGuid) || seen[item.ItemGuid] || !item.MetadataKnown || item.Entry == 0 || item.StackCount == 0 {
					return nil, false
				}
				seen[item.ItemGuid] = true
			}
		}
	}
	return bags, true
}

func bagBaseline(w *worldv1.SnapshotResponse, guid uint64) bool {
	if w != nil && w.Self != nil && (w.Self.Mainhand != nil && w.Self.Mainhand.ItemGuid == guid || w.Self.Offhand != nil && w.Self.Offhand.ItemGuid == guid) {
		return false
	}
	bags, ok := bagInventory(w)
	if !ok {
		return false
	}
	empty := false
	for _, b := range bags {
		if b.ContainerGuid == guid {
			return false
		}
		for _, item := range b.Slots {
			if item.ItemGuid == guid {
				return false
			}
			if b.Bag == 0 && item.ItemGuid == 0 {
				empty = true
			}
		}
	}
	return empty
}

func exactBagAddition(before, after *worldv1.SnapshotResponse, guid uint64) (uint32, uint32, bool) {
	old, ok := bagInventory(before)
	if !ok {
		return 0, 0, false
	}
	current, ok := bagInventory(after)
	if !ok || after.InventoryRevision <= before.InventoryRevision {
		return 0, 0, false
	}
	var found bool
	var bag, slot uint32
	for i, b := range old {
		n := current[i]
		if b.ContainerGuid != n.ContainerGuid || b.Entry != n.Entry || b.MetadataKnown != n.MetadataKnown || b.SlotCount != n.SlotCount {
			return 0, 0, false
		}
		rows := make(map[uint32]*worldv1.BackpackSlot, len(n.Slots))
		for _, item := range n.Slots {
			rows[item.Slot] = item
		}
		for _, item := range b.Slots {
			next := rows[item.Slot]
			if proto.Equal(item, next) {
				continue
			}
			if found || item.ItemGuid != 0 || next.ItemGuid != guid || next.Entry != partyBagEntry || next.StackCount != 1 || !next.MetadataKnown {
				return 0, 0, false
			}
			found, bag, slot = true, uint32(i), item.Slot
		}
	}
	return bag, slot, found
}

func (s *Service) applyBagInventoryLocked(w *worldv1.SnapshotResponse) {
	for _, r := range s.state.BagClaims {
		if r.Status == mailv1.BagClaimStatus_BAG_CLAIM_CONFIRMED || r.Status == mailv1.BagClaimStatus_BAG_CLAIM_SERVER_REJECTED || !r.TransportAttempted {
			continue
		}
		baseline := s.bagBaselines[r.Request.RequestId]
		if baseline == nil {
			continue
		}
		if w.InventoryRevision < r.ObservedInventoryRevision {
			continue
		}
		if w.InventoryRevision >= r.ObservedInventoryRevision {
			r.ObservedInventoryRevision = w.InventoryRevision
		}
		r.InventoryConfirmed = false
		if bag, slot, ok := exactBagAddition(baseline, w, r.ItemGuid); ok {
			r.InventoryConfirmed, r.ObservedBag, r.ObservedSlot = true, bag, slot
		}
		if r.ServerAcknowledged && r.InventoryConfirmed {
			r.Status, r.Reason = mailv1.BagClaimStatus_BAG_CLAIM_CONFIRMED, "bag_confirmed_by_server_and_inventory"
			s.state.BagsClaimed++
		}
	}
}

func (s *Service) applyBagResultLocked(result mailResult) {
	if result.action != 2 {
		return
	}
	for _, r := range s.state.BagClaims {
		if !r.TransportAttempted || r.Request.MailId != result.id || r.Status == mailv1.BagClaimStatus_BAG_CLAIM_CONFIRMED || r.Status == mailv1.BagClaimStatus_BAG_CLAIM_SERVER_REJECTED {
			continue
		}
		if result.result == 0 {
			if result.itemLow == r.Request.ItemGuidLow && result.count == 1 {
				r.ServerAcknowledged = true
			}
		} else if !r.ServerAcknowledged {
			r.ServerError, r.EquipmentError = result.result, result.equip
			r.Status, r.Reason = mailv1.BagClaimStatus_BAG_CLAIM_SERVER_REJECTED, "bag_claim_rejected_by_server"
		}
	}
}

func (s *Service) validateBagListLocked(r *mailv1.ClaimBagAttachmentRequest) bool {
	age := s.now().Sub(s.listAt)
	if s.closed || r.SessionId != s.state.SessionId || !s.state.ListKnown || age < 0 || age >= 15*time.Second || r.ListRevision != s.state.ListRevision || r.MailboxGuid != s.state.MailboxGuid {
		return false
	}
	for _, m := range s.state.BagMails {
		if m.MailId != r.MailId {
			continue
		}
		for _, a := range m.Attachments {
			if a.AttachmentIndex == r.AttachmentIndex && a.ItemGuidLow == r.ItemGuidLow && a.Entry == partyBagEntry && a.Count == r.ExpectedCount {
				return true
			}
		}
	}
	return false
}

func (s *Service) bagGate(ctx context.Context, mailbox uint64) (*worldv1.SnapshotResponse, error) {
	w, _, err := s.gate(ctx, mailbox)
	if err != nil {
		return nil, err
	}
	a := &actionsv1.State{}
	if s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, a) != nil || a.Moving || a.Traveling || a.Practicing || a.MotionUnconfirmed || a.CastPending || a.AutoAttacking || a.AutoAttackUnconfirmed {
		return nil, status.Error(codes.FailedPrecondition, "idle actions required")
	}
	p := w.Self
	if !p.ChannelObjectKnown || !p.ChannelSpellKnown || p.ChannelObjectGuid != 0 || p.ChannelSpellId != 0 || w.Owner != nil && w.Owner.UnitFlagsKnown && w.Owner.UnitFlags&0x80000 != 0 {
		return nil, status.Error(codes.FailedPrecondition, "channel or observed owner combat blocks bag claim")
	}
	return w, nil
}

func (s *Service) bagLeaseCurrent(ctx context.Context, token string) bool {
	r := &actionsv1.InteractionLease{}
	if s.invoke(ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "mail", Token: token, LeaseMs: 15000}, r) != nil {
		return false
	}
	a := r.State
	return r.Token == token && a != nil && a.InteractionOwner == "mail" && !a.Moving && !a.Traveling && !a.Practicing && !a.MotionUnconfirmed && !a.CastPending && !a.AutoAttacking && !a.AutoAttackUnconfirmed
}

func (s *Service) acquireBagLease(ctx context.Context) (string, error) {
	r := &actionsv1.InteractionLease{}
	if err := s.invoke(ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "mail", LeaseMs: 15000}, r); err != nil {
		return "", err
	}
	if r.Token == "" || len(r.Token) > 128 {
		return "", status.Error(codes.FailedPrecondition, "mail lease token unconfirmed")
	}
	// Return a valid token even when the remaining response is unsafe, so the
	// caller can release that exact reservation without attempting an item take.
	a := r.State
	if a == nil || a.InteractionOwner != "mail" || a.Moving || a.Traveling || a.Practicing || a.MotionUnconfirmed || a.CastPending || a.AutoAttacking || a.AutoAttackUnconfirmed {
		return r.Token, status.Error(codes.FailedPrecondition, "mail lease state unconfirmed")
	}
	return r.Token, nil
}

func (s *Service) observeBagLeaseRelease(ctx context.Context) {
	if !s.bagLeaseAbsent(ctx) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.OperationInFlight {
		return
	}
	for _, r := range s.state.BagClaims {
		r.LeaseReleased = true
	}
}

func (s *Service) bagLeaseAbsent(ctx context.Context) bool {
	a := &actionsv1.State{}
	return s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, a) == nil && a.InteractionOwner != "mail"
}

func (s *Service) releaseBagLease(token string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	a := &actionsv1.State{}
	return s.invoke(ctx, "buddy-actions", "release_interaction", &actionsv1.InteractionRelease{Owner: "mail", Token: token}, a) == nil && a.InteractionOwner == ""
}

func (s *Service) ClaimBagAttachment(ctx context.Context, in *mailv1.ClaimBagAttachmentRequest) (out *mailv1.State, err error) {
	if in == nil || len(in.RequestId) == 0 || len(in.RequestId) > 128 || in.SessionId == "" || len(in.SessionId) > 128 || in.MailId == 0 || in.MailboxGuid == 0 || in.ItemGuidLow == 0 || in.AttachmentIndex > 3 || in.ExpectedCount != 1 || in.ExpectedInventoryRevision == 0 {
		return nil, status.Error(codes.InvalidArgument, "exact bag attachment request required")
	}
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	r := proto.Clone(in).(*mailv1.ClaimBagAttachmentRequest)
	s.mu.Lock()
	for _, receipt := range s.state.BagClaims {
		if receipt.Request.RequestId == r.RequestId {
			if !proto.Equal(receipt.Request, r) {
				s.mu.Unlock()
				return nil, status.Error(codes.AlreadyExists, "request id has another immutable payload")
			}
			out = s.snapshotLocked()
			out.LastBagClaim = proto.Clone(receipt).(*mailv1.BagClaimReceipt)
			s.mu.Unlock()
			return out, nil
		}
	}
	if s.closed || s.state.OperationInFlight {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "mail operation unavailable or busy")
	}
	if len(s.state.BagClaims) >= 128 {
		s.mu.Unlock()
		return nil, status.Error(codes.ResourceExhausted, "bag claim history full")
	}
	receipt := &mailv1.BagClaimReceipt{Request: r, Status: mailv1.BagClaimStatus_BAG_CLAIM_REQUESTED, Reason: "bag_claim_preflight", BaselineInventoryRevision: r.ExpectedInventoryRevision, ItemGuid: bagItemGUID(r.ItemGuidLow), Entry: partyBagEntry, Count: 1, LeaseReleased: true}
	blocked := ""
	for _, prior := range s.state.BagClaims {
		if !prior.LeaseReleased || prior.TransportAttempted && prior.Status != mailv1.BagClaimStatus_BAG_CLAIM_CONFIRMED && prior.Status != mailv1.BagClaimStatus_BAG_CLAIM_SERVER_REJECTED {
			blocked = "prior_bag_claim_outcome_unconfirmed"
		}
		if prior.TransportAttempted && prior.Request.ItemGuidLow == r.ItemGuidLow {
			blocked = "attachment_already_attempted"
		}
	}
	if s.state.BagsClaimReserved >= 4 {
		blocked = "four_bag_attempt_limit"
	}
	s.state.BagClaims = append(s.state.BagClaims, receipt)
	s.state.LastBagClaim = receipt
	if blocked != "" {
		receipt.Status = mailv1.BagClaimStatus_BAG_CLAIM_BLOCKED
		receipt.Reason = blocked
		out = s.snapshotLocked()
		s.mu.Unlock()
		return out, nil
	}
	s.state.OperationInFlight = true
	s.mu.Unlock()
	call, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	token := ""
	defer func() {
		if token != "" {
			released := s.releaseBagLease(token)
			s.mu.Lock()
			receipt.LeaseReleased = released
			s.mu.Unlock()
		}
		s.mu.Lock()
		needsReleaseObservation := !receipt.LeaseReleased
		s.mu.Unlock()
		if needsReleaseObservation {
			cleanup, cancelCleanup := context.WithTimeout(context.Background(), 2*time.Second)
			absent := s.bagLeaseAbsent(cleanup)
			cancelCleanup()
			if absent {
				s.mu.Lock()
				receipt.LeaseReleased = true
				s.mu.Unlock()
			}
		}
		s.mu.Lock()
		s.state.OperationInFlight = false
		s.pendingList = false
		s.signalLocked()
		out = s.snapshotLocked()
		out.LastBagClaim = proto.Clone(receipt).(*mailv1.BagClaimReceipt)
		s.mu.Unlock()
	}()
	block := func(reason string) {
		s.mu.Lock()
		receipt.Status, receipt.Reason = mailv1.BagClaimStatus_BAG_CLAIM_BLOCKED, reason
		s.mu.Unlock()
	}
	s.mu.Lock()
	valid := s.validateBagListLocked(r)
	s.mu.Unlock()
	if !valid {
		block("bag_list_changed_or_not_exact")
		return nil, nil
	}
	w, e := s.bagGate(call, r.MailboxGuid)
	if e != nil {
		block("mailbox_or_character_gate_blocked")
		return nil, nil
	}
	if w.InventoryRevision != r.ExpectedInventoryRevision || !bagBaseline(w, receipt.ItemGuid) {
		block("full_inventory_baseline_or_empty_backpack_required")
		return nil, nil
	}
	s.mu.Lock()
	receipt.LeaseReleased = false
	s.mu.Unlock()
	token, e = s.acquireBagLease(call)
	if e != nil {
		block("fresh_mail_lease_required")
		return nil, nil
	}
	if !s.bagLeaseCurrent(call, token) {
		block("mail_lease_or_idle_state_changed")
		return nil, nil
	}
	w, e = s.bagGate(call, r.MailboxGuid)
	if e != nil || w.InventoryRevision != r.ExpectedInventoryRevision || !bagBaseline(w, receipt.ItemGuid) {
		block("mailbox_or_inventory_changed_after_lease")
		return nil, nil
	}
	if !s.bagLeaseCurrent(call, token) {
		block("mail_lease_changed_before_write")
		return nil, nil
	}
	s.mu.Lock()
	if !s.validateBagListLocked(r) || call.Err() != nil {
		receipt.Status = mailv1.BagClaimStatus_BAG_CLAIM_BLOCKED
		receipt.Reason = "bag_list_or_context_changed_before_write"
		s.mu.Unlock()
		return nil, nil
	}
	if s.bagBaselines == nil {
		s.bagBaselines = make(map[string]*worldv1.SnapshotResponse)
	}
	s.bagBaselines[r.RequestId] = proto.Clone(w).(*worldv1.SnapshotResponse)
	receipt.Status, receipt.Reason, receipt.TransportAttempted = mailv1.BagClaimStatus_BAG_CLAIM_REQUESTED, "waiting_for_bag_server_and_inventory", true
	receipt.ObservedInventoryRevision = w.InventoryRevision
	s.state.BagsClaimReserved++
	s.mu.Unlock()
	e = s.write(call, opcode.CMSGMailTakeItem, takeItemPacket(r.MailboxGuid, r.MailId, r.ItemGuidLow))
	s.mu.Lock()
	if e == nil {
		receipt.TransportWritten = true
	} else if receipt.Status != mailv1.BagClaimStatus_BAG_CLAIM_SERVER_REJECTED && receipt.Status != mailv1.BagClaimStatus_BAG_CLAIM_CONFIRMED {
		receipt.Status, receipt.Reason = mailv1.BagClaimStatus_BAG_CLAIM_WRITE_UNCONFIRMED, "bag_write_unconfirmed_no_retry"
	}
	s.mu.Unlock()
	if e != nil {
		return nil, nil
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if observed, e := s.world(call); e == nil {
			s.mu.Lock()
			s.applyBagInventoryLocked(observed)
			s.mu.Unlock()
			if observed.Self.UnitFlagsKnown && observed.Self.UnitFlags&0x80000 != 0 || observed.Owner != nil && observed.Owner.UnitFlagsKnown && observed.Owner.UnitFlags&0x80000 != 0 {
				break
			}
		}
		s.mu.Lock()
		done := s.closed || receipt.Status == mailv1.BagClaimStatus_BAG_CLAIM_CONFIRMED || receipt.Status == mailv1.BagClaimStatus_BAG_CLAIM_SERVER_REJECTED
		s.mu.Unlock()
		if done || call.Err() != nil {
			break
		}
		select {
		case <-call.Done():
		case <-ticker.C:
		}
	}
	s.mu.Lock()
	if receipt.Status == mailv1.BagClaimStatus_BAG_CLAIM_REQUESTED {
		receipt.Status, receipt.Reason = mailv1.BagClaimStatus_BAG_CLAIM_UNCONFIRMED, "bag_outcome_unconfirmed_no_retry"
	}
	s.mu.Unlock()
	return nil, nil
}
