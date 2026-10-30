package buddyprofessions

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/types/known/emptypb"
)

func (s *Service) renew(ctx context.Context, op *operation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	current := s.active == op && op.token != ""
	token := op.token
	s.mu.Unlock()
	if !current {
		return fail("operation no longer owns an interaction token")
	}
	out := &actionsv1.InteractionLease{}
	if err := s.invoke(ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "professions", Token: token, LeaseMs: 30000}, out); err != nil {
		return err
	}
	if out.Token != token || out.State == nil || out.State.InteractionOwner != "professions" || out.State.InteractionLeaseRemainingMs < 2000 || out.State.Moving || out.State.MotionUnconfirmed {
		return fail("exact interaction lease or stopped position changed")
	}
	s.mu.Lock()
	s.actions = out.State
	s.mu.Unlock()
	return ctx.Err()
}
func (s *Service) writeOwned(ctx context.Context, op *operation, code uint32, payload []byte) error {
	call, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	// Observation queries and dependency refreshes must precede the final
	// world proof. Cleanup packets retain their separate exact-token path.
	needsWorld := code == opcode.CMSGCastSpell || code == opcode.CMSGGameobjUse || code == opcode.CMSGAutostoreLootItem
	if needsWorld {
		select {
		case s.observeGate <- struct{}{}:
			defer func() { <-s.observeGate }()
		case <-call.Done():
			return call.Err()
		}
	}
	if err := s.renew(call, op); err != nil {
		return err
	}
	if needsWorld {
		w := &worldv1.SnapshotResponse{}
		if err := s.invoke(call, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, w); err != nil {
			return err
		}
		v, err := validateWorld(w, s.selfGUID)
		if err != nil {
			return err
		}
		s.mu.Lock()
		if v.inventoryRevision < s.state.InventoryRevision || v.skillsRevision < s.state.SkillsRevision {
			s.mu.Unlock()
			return fail("final carried inventory or skill observation regressed")
		}
		s.view, s.viewKnown = v, true
		s.state.InventoryKnown, s.state.InventoryRevision = true, v.inventoryRevision
		s.state.BackpackKnown, s.state.BackpackRevision = w.BackpackKnown, v.backpackRevision
		s.state.SkillsKnown, s.state.SkillsRevision = true, v.skillsRevision
		valid := s.active == op && s.activeSafetyLocked(op) == ""
		if code == opcode.CMSGCastSpell {
			valid = valid && v.inventoryRevision == op.baseline.inventoryRevision && v.skillsRevision == op.baseline.skillsRevision && s.readinessLocked(op.spec, op) == ""
		}
		if code == opcode.CMSGAutostoreLootItem {
			valid = valid && v.freeSlots > 0
		}
		s.mu.Unlock()
		if !valid {
			return fail("final actual body, inventory, skills or source changed before write")
		}
	}
	s.mu.Lock()
	valid := s.active == op
	if valid && (code == opcode.CMSGCastSpell || code == opcode.CMSGGameobjUse || code == opcode.CMSGAutostoreLootItem) {
		valid = s.activeSafetyLocked(op) == ""
		if code == opcode.CMSGCastSpell && op.spec.focusID != 0 {
			valid = valid && s.hasFocusLocked(op.spec.focusID)
		}
		if code == opcode.CMSGGameobjUse {
			valid = valid && s.bobberReadyLocked(op)
		}
	}
	s.mu.Unlock()
	if !valid || call.Err() != nil {
		return fail("operation ended before transport")
	}
	_, err := s.session.SendPacket(call, &modv1.SendPacketRequest{Opcode: code, Payload: payload})
	if err == nil {
		s.mu.Lock()
		s.state.TransportWrites++
		s.mu.Unlock()
	}
	return err
}

func (s *Service) setReason(op *operation, reason string, status professionsv1.AttemptStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != op {
		return
	}
	op.receipt.Reason = reason
	op.receipt.Status = status
	s.state.Revision++
}

func (s *Service) run(op *operation) {
	if op.spec.skill == 393 {
		s.runSkin(op)
		return
	}
	defer close(op.done)
	defer op.cancel()
	defer s.finish(op)
	lease := &actionsv1.InteractionLease{}
	acquire, cancel := context.WithTimeout(op.ctx, 2*time.Second)
	err := s.invoke(acquire, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "professions", LeaseMs: 30000}, lease)
	cancel()
	if err != nil || lease.Token == "" || lease.State == nil || lease.State.InteractionOwner != "professions" {
		s.setReason(op, "professions_interaction_lease_unavailable", professionsv1.AttemptStatus_BLOCKED)
		return
	}
	s.mu.Lock()
	op.token = lease.Token
	s.mu.Unlock()
	check, cancel := context.WithTimeout(op.ctx, 2*time.Second)
	err = s.observe(check)
	cancel()
	s.mu.Lock()
	if err == nil && (s.view.skillsRevision != op.baseline.skillsRevision || s.view.inventoryRevision != op.baseline.inventoryRevision || !s.book.known || !s.book.has(op.spec.spell)) {
		err = fail("own skills, backpack or recipe knowledge changed before cast")
	}
	if err == nil {
		if reason := s.readinessLocked(op.spec, op); reason != "" {
			err = fail(reason)
		}
	}
	if err == nil && s.active == op && op.ctx.Err() == nil {
		op.castReserved = true
		op.receipt.Status = professionsv1.AttemptStatus_WAITING_CAST
		op.receipt.Reason = "cast_reserved_once"
		s.state.Revision++
	} else if err == nil {
		err = fail("attempt cancelled before cast")
	}
	s.mu.Unlock()
	if err != nil {
		s.setReason(op, err.Error(), professionsv1.AttemptStatus_BLOCKED)
		return
	}
	err = s.writeOwned(op.ctx, op, opcode.CMSGCastSpell, castPacket(byte(op.receipt.CastCount), op.spec.spell))
	s.mu.Lock()
	op.receipt.CastTransportWritten = err == nil
	if err != nil {
		op.receipt.Reason = "cast_transport_unconfirmed_waiting_for_actual_receipts"
	}
	s.state.Revision++
	s.mu.Unlock()
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-op.ctx.Done():
			s.mu.Lock()
			cancelled := op.receipt.CancelRequested
			s.mu.Unlock()
			if cancelled {
				s.setReason(op, "owned_attempt_cancelled", professionsv1.AttemptStatus_CANCELLED)
			} else {
				s.setReason(op, "finite_attempt_deadline; outcome_unconfirmed", professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED)
			}
			return
		case <-ticker.C:
			read, cancel := context.WithTimeout(op.ctx, time.Second)
			err = s.observe(read)
			cancel()
			if err != nil {
				s.setReason(op, "active_observations_unavailable; outcome_unconfirmed", professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED)
				return
			}
			if done := s.step(op); done {
				return
			}
		}
	}
}

func (s *Service) step(op *operation) bool {
	s.mu.Lock()
	if s.active != op {
		s.mu.Unlock()
		return true
	}
	r := op.receipt
	if terminalStatus(r.Status) {
		s.mu.Unlock()
		return true
	}
	if reason := s.activeSafetyLocked(op); reason != "" {
		r.Status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
		r.Reason = reason
		s.state.Revision++
		s.mu.Unlock()
		return true
	}
	if op.spec.skill != 356 {
		if r.OwnGoObserved && r.OwnCreatedItemsObserved >= op.spec.itemCount && r.OutputInventoryObserved && r.ReagentsConsumedObserved {
			r.Status = professionsv1.AttemptStatus_COMPLETED
			r.Reason = "actual_craft_cast_created_item_and_owned_inventory_delta_observed"
			s.state.Revision++
			s.mu.Unlock()
			return true
		}
		if r.OwnGoObserved {
			r.Status = professionsv1.AttemptStatus_WAITING_OBSERVATIONS
			r.Reason = "waiting_for_actual_created_item_and_inventory_delta"
		}
		s.mu.Unlock()
		return false
	}
	if r.OwnGoObserved && r.ChannelStartObserved && r.BobberGuid == 0 {
		s.bindBobberLocked(op)
	}
	if r.BobberGuid != 0 && !op.reelReserved && s.bobberReadyLocked(op) {
		op.reelReserved = true
		r.SplashObserved = true
		r.Status = professionsv1.AttemptStatus_WAITING_LOOT
		r.Reason = "own_ready_bobber_use_reserved_once"
		s.state.Revision++
		guid := r.BobberGuid
		s.mu.Unlock()
		if err := s.writeOwned(op.ctx, op, opcode.CMSGGameobjUse, gameObjectUsePacket(guid)); err != nil {
			s.setReason(op, "bobber_use_transport_unconfirmed_no_retry", professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED)
			return true
		}
		return false
	}
	if op.window == nil {
		s.mu.Unlock()
		return false
	}
	if op.window.gold != 0 {
		r.Status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
		r.Reason = "unsupported_fishing_money_window"
		s.mu.Unlock()
		return true
	}
	for _, slot := range op.window.slots {
		if !op.stored[slot.slot] {
			op.stored[slot.slot] = true
			s.state.Revision++
			s.mu.Unlock()
			if err := s.writeOwned(op.ctx, op, opcode.CMSGAutostoreLootItem, storeLootPacket(slot.slot)); err != nil {
				s.setReason(op, "fishing_slot_transport_unconfirmed_no_retry", professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED)
				return true
			}
			return false
		}
		if !op.removed[slot.slot] || !op.pushed[slot.slot] {
			s.mu.Unlock()
			return false
		}
	}
	if !op.releaseReserved {
		op.releaseReserved = true
		s.state.Revision++
		guid := r.BobberGuid
		s.mu.Unlock()
		if err := s.writeOwned(op.ctx, op, opcode.CMSGLootRelease, releaseLootPacket(guid)); err != nil {
			s.setReason(op, "fishing_release_transport_unconfirmed_no_retry", professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED)
			return true
		}
		return false
	}
	counts := make(map[uint32]uint32)
	for _, slot := range op.window.slots {
		counts[slot.entry] += slot.count
	}
	inventory := true
	for entry, n := range counts {
		before, after := op.baseline.counts[entry], s.view.counts[entry]
		if after < before || after-before < n {
			inventory = false
		}
	}
	if len(counts) > 0 {
		inventory = inventory && s.view.inventoryRevision > op.baseline.inventoryRevision
		r.OutputInventoryObserved = inventory
		r.ObservedBackpackRevision = s.view.backpackRevision
		r.ObservedInventoryRevision = s.view.inventoryRevision
	}
	if r.LootReleaseObserved && r.ChannelStopObserved && inventory {
		r.Status = professionsv1.AttemptStatus_COMPLETED
		r.Reason = "own_fishing_channel_bobber_window_release_and_inventory_observed"
		if len(counts) == 0 {
			r.Reason = "own_fishing_window_empty_no_catch_items"
		}
		s.state.Revision++
		s.mu.Unlock()
		return true
	}
	s.mu.Unlock()
	return false
}

func (s *Service) bindBobberLocked(op *operation) {
	p := s.view.self
	guid := p.ChannelObjectGuid
	if !p.ChannelObjectKnown || guid == 0 || !p.ChannelSpellKnown || p.ChannelSpellId != op.spec.spell {
		return
	}
	if object := s.currentBobberLocked(op, guid); object != nil {
		op.receipt.BobberGuid = guid
		op.receipt.BobberRevision = object.PositionRevision
		op.receipt.OwnBobberObserved = true
		op.receipt.Status = professionsv1.AttemptStatus_WAITING_BOBBER
		op.receipt.Reason = "own_created_bobber_bound_to_actual_channel"
		s.state.Revision++
	}
}

func (s *Service) bobberReadyLocked(op *operation) bool {
	object := s.currentBobberLocked(op, op.receipt.BobberGuid)
	return object != nil && object.Flags&0x20 != 0 && object.State == 0 && object.PositionRevision >= op.receipt.BobberRevision
}

func (s *Service) currentBobberLocked(op *operation, guid uint64) *worldv1.GameObjectState {
	if !s.viewKnown || guid == 0 || s.view.self == nil {
		return nil
	}
	p := s.view.self
	if !p.ChannelObjectKnown || p.ChannelObjectGuid != guid || !p.ChannelSpellKnown || p.ChannelSpellId != op.spec.spell {
		return nil
	}
	for _, object := range worldObjects(s.view.raw, "own_fishing_bobbers") {
		if object == nil || object.Guid != guid || !object.Present || !object.TypeKnown || object.GameObjectType != 17 || object.Entry != 35591 || !object.MapKnown || object.MapId != p.MapId || !object.PositionKnown || !object.FlagsKnown || !object.StateKnown || !object.CreatedByKnown || object.CreatedByGuid != s.selfGUID {
			continue
		}
		return object
	}
	return nil
}

func terminalStatus(v professionsv1.AttemptStatus) bool {
	return v == professionsv1.AttemptStatus_COMPLETED || v == professionsv1.AttemptStatus_BLOCKED || v == professionsv1.AttemptStatus_SERVER_REJECTED || v == professionsv1.AttemptStatus_CANCELLED || v == professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
}

// Recheck safety without requiring materials that this operation consumed.
func (s *Service) activeSafetyLocked(op *operation) string {
	if !s.viewKnown || s.view.self == nil {
		return "active_own_observations_unavailable"
	}
	if s.view.self.MapId != op.baseline.self.MapId {
		return "actual_map_changed_during_attempt"
	}
	// Actions pauses its world refresh while the interaction lease is held.
	// A newer actual correction invalidates that cached movement estimate.
	if s.view.self.PositionRevision != op.baseline.self.PositionRevision {
		return "actual_position_changed_during_attempt"
	}
	if owner := s.view.raw.Owner; owner != nil && owner.Present && owner.UnitFlagsKnown && owner.UnitFlags&0x80000 != 0 {
		return "observed_owner_combat_takes_priority"
	}
	if s.actions == nil || s.actions.Moving || s.actions.MotionUnconfirmed || s.actions.Traveling || s.actions.Practicing || s.actions.CastPending || s.actions.AutoAttacking || s.actions.AutoAttackRequested || s.actions.AutoAttackUnconfirmed || (s.actions.InteractionOwner != "" && s.actions.InteractionOwner != "professions") {
		return "controller_changed_during_attempt"
	}
	if s.loot == nil || s.loot.Enabled || s.loot.PendingCorpseGuid != 0 || s.loot.PendingGameObjectGuid != 0 || (s.loot.Phase != "" && s.loot.Phase != "idle" && s.loot.Phase != "disabled") {
		return "loot_controller_changed_during_attempt"
	}
	return ""
}

func (s *Service) finish(op *operation) {
	// Cleanup has a separate tiny bound and can only renew the SAME live token.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.mu.Lock()
	if s.active != op {
		s.mu.Unlock()
		return
	}
	token := op.token
	needsCancel := s.cleanupCastPendingLocked(op)
	// Fishing cancellation has no cast-count field. Prove that the current
	// channel still belongs to this attempt before sending that wider packet.
	canCancel := op.spec.skill != 356
	if op.spec.skill == 356 && s.viewKnown && s.view.self != nil && op.receipt.OwnGoObserved && op.receipt.ChannelStartObserved {
		p := s.view.self
		canCancel = p.ChannelSpellKnown && p.ChannelSpellId == op.spec.spell && p.ChannelObjectKnown && p.ChannelObjectGuid != 0 && (op.receipt.BobberGuid == 0 || p.ChannelObjectGuid == op.receipt.BobberGuid)
	}
	s.mu.Unlock()
	if token != "" {
		s.releaseWindowForCleanup(ctx, op)
	}
	if token != "" && needsCancel && canCancel {
		s.mu.Lock()
		reserve := !op.cancelReserved
		op.cancelReserved = true
		s.mu.Unlock()
		if reserve {
			code := uint32(opcode.CMSGCancelCast)
			payload := binary.LittleEndian.AppendUint32([]byte{byte(op.receipt.CastCount)}, op.spec.spell)
			if op.spec.skill == 356 {
				code = opcode.CMSGCancelChannelling
				payload = cancelChannelPacket(op.spec.spell)
			}
			err := s.writeOwned(ctx, op, code, payload)
			s.mu.Lock()
			op.receipt.CancelTransportWritten = err == nil
			s.mu.Unlock()
		}
	}
	// Keep the operation bound while asynchronous release/cancel receipts arrive.
	// Reserve time within the same bound for releasing the interaction lease.
	waitUntil := time.Now().Add(1200 * time.Millisecond)
	if deadline, ok := ctx.Deadline(); ok && waitUntil.After(deadline.Add(-400*time.Millisecond)) {
		waitUntil = deadline.Add(-400 * time.Millisecond)
	}
	for time.Now().Before(waitUntil) {
		if token != "" {
			// A response to the earlier reel may arrive during cleanup.
			s.releaseWindowForCleanup(ctx, op)
		}
		s.mu.Lock()
		pending := s.cleanupWindowPendingLocked(op) || s.cleanupCastPendingLocked(op)
		s.mu.Unlock()
		if !pending {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(20 * time.Millisecond):
		}
	}
	s.mu.Lock()
	op.receipt.CleanupUnconfirmed = s.cleanupWindowPendingLocked(op) || s.cleanupCastPendingLocked(op)
	s.mu.Unlock()
	// A late/ambiguous release is never repeated or sent with a replacement token.
	if token != "" {
		if err := s.renew(ctx, op); err == nil {
			out := &actionsv1.State{}
			err = s.invoke(ctx, "buddy-actions", "release_interaction", &actionsv1.InteractionRelease{Owner: "professions", Token: token}, out)
			s.mu.Lock()
			op.receipt.LeaseReleased = err == nil && out.InteractionOwner == ""
			if !op.receipt.LeaseReleased {
				op.receipt.CleanupUnconfirmed = true
			}
			s.mu.Unlock()
		} else {
			s.mu.Lock()
			op.receipt.CleanupUnconfirmed = true
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	if s.active == op {
		if op.receipt.Status == professionsv1.AttemptStatus_COMPLETED && op.receipt.CleanupUnconfirmed {
			op.receipt.Status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
			op.receipt.Reason = "completion_receipts_observed_but_owned_cleanup_unconfirmed"
		}
		s.active = nil
		s.state.Revision++
	}
	s.mu.Unlock()
}

func (s *Service) cleanupWindowPendingLocked(op *operation) bool {
	return op.window != nil && !op.receipt.LootReleaseObserved || (op.reelReserved && op.window == nil && op.receipt.Status != professionsv1.AttemptStatus_SERVER_REJECTED)
}

func (s *Service) cleanupCastPendingLocked(op *operation) bool {
	r := op.receipt
	if !op.castReserved || (r.FailureCode != 0 && !r.OwnGoObserved && !r.ChannelStartObserved) {
		return false
	}
	if op.spec.skill == 356 {
		return !r.ChannelStopObserved
	}
	return !r.OwnGoObserved && r.FailureCode == 0
}

func (s *Service) releaseWindowForCleanup(ctx context.Context, op *operation) {
	s.mu.Lock()
	if s.active != op || op.window == nil || op.releaseReserved {
		s.mu.Unlock()
		return
	}
	op.releaseReserved = true
	guid := op.receipt.BobberGuid
	s.state.Revision++
	s.mu.Unlock()
	// Never retry an ambiguous release, including one reserved by step().
	_ = s.writeOwned(ctx, op, opcode.CMSGLootRelease, releaseLootPacket(guid))
}

func (s *Service) OnPacket(_ context.Context, packet *modv1.WorldPacket) (*emptypb.Empty, error) {
	if packet == nil {
		return &emptypb.Empty{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch packet.Opcode {
	case opcode.SMSGInitialSpells, opcode.SMSGLearnedSpell, opcode.SMSGRemovedSpell, opcode.SMSGSupercededSpell:
		if err := s.book.onPacket(packet.Opcode, packet.Payload); err != nil {
			s.state.MalformedPackets++
		}
		s.state.Revision++
		return &emptypb.Empty{}, nil
	case opcode.SMSGGameobjectQueryResponse:
		if len(packet.Payload) < 4 {
			return &emptypb.Empty{}, nil
		}
		entry := binary.LittleEndian.Uint32(packet.Payload) & 0x7fffffff
		if !s.focusRequested[entry] {
			return &emptypb.Empty{}, nil
		}
		meta, err := decodeFocusTemplate(packet.Payload)
		if err != nil {
			s.state.MalformedPackets++
		} else {
			s.focuses[meta.entry] = meta
		}
		s.state.Revision++
		return &emptypb.Empty{}, nil
	}
	if s.onSkinTemplatePacketLocked(packet) {
		return &emptypb.Empty{}, nil
	}
	s.observeCorpseLootLocked(packet)
	op := s.active
	if op != nil && op.spec.skill == 393 {
		s.onSkinPacketLocked(op, packet)
		return &emptypb.Empty{}, nil
	}
	if op == nil || !op.castReserved {
		return &emptypb.Empty{}, nil
	}
	r := op.receipt
	switch packet.Opcode {
	case opcode.SMSGSpellStart, opcode.SMSGSpellGo, opcode.SMSGCastFailed, opcode.SMSGSpellFailure, opcode.MSGChannelStart, opcode.MSGChannelUpdate, opcode.SMSGFishNotHooked, opcode.SMSGFishEscaped:
		e, err := decodeCast(packet.Opcode, packet.Payload)
		if err != nil {
			s.state.MalformedPackets++
			return &emptypb.Empty{}, nil
		}
		if e.kind == "channel_start" {
			if op.spec.skill == 356 && e.caster == s.selfGUID && e.spell == op.spec.spell {
				r.ChannelStartObserved = true
			}
			break
		}
		if e.kind == "channel_update" {
			if op.spec.skill == 356 && e.caster == s.selfGUID && e.durationMs == 0 && r.ChannelStartObserved && r.OwnGoObserved {
				r.ChannelStopObserved = true
			}
			break
		}
		if e.kind == "fish_not_hooked" || e.kind == "fish_escaped" {
			if op.spec.skill == 356 && r.ChannelStartObserved && r.OwnGoObserved && (op.reelReserved || r.OwnBobberObserved) {
				r.Status = professionsv1.AttemptStatus_SERVER_REJECTED
				r.Reason = e.kind
			}
			break
		}
		if e.spell != op.spec.spell || uint32(e.count) != r.CastCount || packet.Opcode != opcode.SMSGCastFailed && e.caster != s.selfGUID {
			return &emptypb.Empty{}, nil
		}
		switch e.kind {
		case "start":
			if e.caster == s.selfGUID {
				r.OwnStartObserved = true
			}
		case "go":
			if e.caster == s.selfGUID {
				r.OwnGoObserved = true
			}
		case "failed":
			r.FailureCode = uint32(e.failure)
			r.Status = professionsv1.AttemptStatus_SERVER_REJECTED
			r.Reason = fmt.Sprintf("actual_server_cast_failure_%d", e.failure)
		}
	case opcode.SMSGItemPushResult:
		e, err := decodePush(packet.Payload)
		if err != nil {
			s.state.MalformedPackets++
			break
		}
		// Item-push uses raw equipment slots, unlike the normalized world bag.
		// The base backpack is bag255 and absolute slots23..38; existing-stack
		// pushes can omit the slot with UINT32_MAX.
		if e.player != s.selfGUID || !carriedPushLocation(s.view, e.bag, e.slot) {
			break
		}
		if op.spec.skill != 356 && e.received && e.created && e.entry == op.spec.itemEntry && e.added == op.spec.itemCount {
			r.OwnCreatedItemsObserved = e.added
		}
		if op.spec.skill == 356 && !e.received && !e.created && op.window != nil {
			for _, slot := range op.window.slots {
				if op.stored[slot.slot] && !op.pushed[slot.slot] && e.entry == slot.entry && e.added == slot.count {
					op.pushed[slot.slot] = true
					op.pushCounts[e.entry] += e.added
					r.OwnLootedItemsObserved += e.added
					break
				}
			}
		}
	case opcode.SMSGLootResponse:
		if op.spec.skill != 356 || !op.reelReserved || op.window != nil {
			break
		}
		window, err := decodeFishingLoot(packet.Payload)
		if err != nil {
			s.state.MalformedPackets++
			break
		}
		if window.guid == r.BobberGuid {
			counts := make(map[uint32]uint32)
			var total uint32
			for _, slot := range window.slots {
				if slot.count > math.MaxUint32-counts[slot.entry] || slot.count > math.MaxUint32-total {
					s.state.MalformedPackets++
					r.Status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
					r.Reason = "fishing_loot_count_overflow"
					return &emptypb.Empty{}, nil
				}
				counts[slot.entry] += slot.count
				total += slot.count
			}
			op.window = &window
			r.LootWindowObserved = true
		}
	case opcode.SMSGLootRemoved:
		if len(packet.Payload) != 1 {
			s.state.MalformedPackets++
			break
		}
		slot := packet.Payload[0]
		if op.spec.skill == 356 && op.stored[slot] && !op.removed[slot] {
			op.removed[slot] = true
			r.LootSlotsRemoved++
		}
	case opcode.SMSGLootReleaseResponse:
		if len(packet.Payload) != 9 {
			s.state.MalformedPackets++
			break
		}
		if op.spec.skill == 356 && op.releaseReserved && binary.LittleEndian.Uint64(packet.Payload) == r.BobberGuid && packet.Payload[8] == 1 {
			r.LootReleaseObserved = true
		}
	case opcode.SMSGInventoryChangeFailure:
		if len(packet.Payload) < 1 || (packet.Payload[0] == 0 && len(packet.Payload) != 1) || (packet.Payload[0] != 0 && len(packet.Payload) != 18 && len(packet.Payload) != 22 && len(packet.Payload) != 38) {
			s.state.MalformedPackets++
			break
		}
		if packet.Payload[0] != 0 {
			r.Status = professionsv1.AttemptStatus_SERVER_REJECTED
			r.Reason = "actual_inventory_change_failure"
		}
	}
	s.state.Revision++
	return &emptypb.Empty{}, nil
}
