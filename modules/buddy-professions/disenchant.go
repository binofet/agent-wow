package buddyprofessions

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const disenchantLimitation = "Explicit selected carried item only; disenchant permanently consumes it. The public item template omits DisenchantID, so the server decides that final eligibility. No equipment selection, loot-open, loot-release or automatic recovery. Unconfirmed or partial material retrieval remains pending for operator follow-up; logout cannot safely be vetoed by this module."

func disenchantSource(v worldView, guid uint64) (*worldv1.BackpackSlot, string) {
	if v.raw == nil || !carriedItemGUID(guid) {
		return nil, "explicit_owned_carried_item_guid_required"
	}
	// The view normally already passed this check. Keep this pure helper strict
	// when it is used by read-only selection or late reconciliation, too.
	if _, _, err := carriedInventory(v.raw); err != nil {
		return nil, "complete_private_five_bag_source_observation_required"
	}
	var selected *worldv1.BackpackSlot
	for _, bag := range v.raw.InventoryBags {
		if bag.ContainerGuid == guid {
			return nil, "equipped_container_cannot_be_selected_for_disenchant"
		}
		for _, row := range bag.Slots {
			if row.ItemGuid == guid {
				if selected != nil {
					return nil, "duplicate_selected_item_guid"
				}
				selected = row
			}
		}
	}
	if selected == nil || selected.Entry == 0 || !selected.MetadataKnown || selected.StackCount != 1 {
		return nil, "exact_selected_carried_single_item_required"
	}
	if v.self != nil {
		for _, hand := range []*worldv1.EquippedWeapon{v.self.Mainhand, v.self.Offhand} {
			if hand != nil && hand.SlotKnown && hand.ItemGuid == guid {
				return nil, "selected_item_is_observed_equipped"
			}
		}
	}
	return selected, ""
}

func (s *Service) queryDisenchantTemplates(ctx context.Context, w *worldv1.SnapshotResponse) {
	s.mu.Lock()
	guid := s.selectedDisenchantGUID
	if s.active != nil && s.active.spec.skill == disenchantSkill {
		guid = s.active.request.TargetGuid
	} else if s.pendingDisenchant != nil {
		guid = s.pendingDisenchant.request.TargetGuid
	}
	s.mu.Unlock()
	if guid == 0 {
		return
	}
	v, err := validateWorld(w, s.selfGUID)
	if err != nil {
		return
	}
	row, reason := disenchantSource(v, guid)
	if reason != "" {
		return
	}
	s.mu.Lock()
	if s.deRequested[row.Entry] || len(s.deRequested) >= 32 {
		s.mu.Unlock()
		return
	}
	s.deRequested[row.Entry] = true // Reserve before the ordinary query write.
	s.mu.Unlock()
	if _, err := s.session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: opcode.CMSGItemQuerySingle, Payload: disenchantQueryPacket(row.Entry)}); err == nil {
		s.mu.Lock()
		s.state.TransportWrites++
		s.mu.Unlock()
	}
}

func (s *Service) onDisenchantTemplatePacketLocked(p *modv1.WorldPacket) bool {
	if p.Opcode != opcode.SMSGItemQuerySingleResponse || len(p.Payload) < 4 {
		return false
	}
	entry := binary.LittleEndian.Uint32(p.Payload) & 0x7fffffff
	if !s.deRequested[entry] {
		return false
	}
	m, err := decodeDisenchantTemplate(p.Payload)
	if err != nil {
		s.state.MalformedPackets++
	} else {
		s.deTemplates[entry] = m
	}
	s.state.Revision++
	return true
}

func (s *Service) disenchantControllersLocked() string {
	a := s.actions
	if a == nil || a.Following || a.Healing || a.Assisting || a.FollowLeaseRemainingMs != 0 || a.HealingLeaseRemainingMs != 0 || a.AssistLeaseRemainingMs != 0 {
		return "follow_heal_assist_toggles_and_leases_must_be_off_for_selected_item_loss"
	}
	t := s.deTrade
	if t == nil || t.SessionId != s.state.SessionId || t.Phase != "IDLE" || t.PartnerGuid != 0 || t.PartnerAccepted || t.AcceptTransportWritten || len(t.OwnRequestedItems) != 0 || len(t.PartnerItems) != 0 || t.OwnRequestedCopper != 0 || t.PartnerCopper != 0 || t.JunkDisposalEnabled {
		return "actual_same_session_trade_and_junk_controller_must_be_idle"
	}
	if t.LastJunkDisposal != nil && t.LastJunkDisposal.Status != "SERVER_SLOT_REMOVAL_CONFIRMED" {
		return "previous_junk_disposal_not_confirmed"
	}
	return ""
}

func (s *Service) disenchantTargetLocked(guid uint64, own *operation) (*worldv1.BackpackSlot, string) {
	if !s.viewKnown || s.view.self == nil {
		return nil, "actual_own_observations_required"
	}
	spec, _ := recipeForSpell(disenchantSpell)
	if reason := s.readinessLocked(spec, own); reason != "" {
		return nil, reason
	}
	if reason := s.disenchantControllersLocked(); reason != "" {
		return nil, reason
	}
	row, reason := disenchantSource(s.view, guid)
	if reason != "" {
		return nil, reason
	}
	m, ok := s.deTemplates[row.Entry]
	if !ok || !m.known || m.entry != row.Entry {
		return nil, "complete_actual_public_selected_item_template_required"
	}
	if m.class != 2 && m.class != 4 || m.quality < 2 || m.quality > 4 || m.containerSlots != 0 || m.requiredSkill == ^uint32(0) {
		return nil, "selected_item_not_supported_weapon_or_armor_quality_two_to_four"
	}
	if rank, ok := s.view.skills[disenchantSkill]; !ok || rank.value < m.requiredSkill {
		return nil, "observed_pure_enchanting_below_actual_disenchant_requirement"
	}
	if s.deAttempted[guid] && (own == nil || s.active != own || own.request.TargetGuid != guid) {
		return nil, "selected_item_guid_already_reserved_no_replay"
	}
	if len(s.deAttempted) >= 256 && !s.deAttempted[guid] {
		return nil, "finite_selected_item_receipt_capacity_reached"
	}
	return row, ""
}

func (s *Service) disenchantItemLocked() *professionsv1.DisenchantItem {
	guid := s.selectedDisenchantGUID
	if s.active != nil && s.active.spec.skill == disenchantSkill {
		guid = s.active.request.TargetGuid
	} else if s.pendingDisenchant != nil {
		guid = s.pendingDisenchant.request.TargetGuid
	}
	if guid == 0 {
		return nil
	}
	out := &professionsv1.DisenchantItem{ItemGuid: guid, Limitation: disenchantLimitation}
	if row, reason := disenchantSource(s.view, guid); reason == "" {
		out.Bag, out.Slot, out.Entry, out.StackCount = row.Bag, row.Slot, row.Entry, row.StackCount
		m := s.deTemplates[row.Entry]
		out.TemplateKnown, out.Quality, out.ItemClass, out.RequiredSkill = m.known, m.quality, m.class, m.requiredSkill
	}
	_, out.Reason = s.disenchantTargetLocked(guid, nil)
	out.Ready = out.Reason == ""
	return out
}

// The only normal writes are one fixed ITEM cast and individually reserved
// AUTOSTOREs. Dependency refreshes precede this function; exact-token renewal
// precedes the LAST world snapshot, then only pure local gates precede Send.
func (s *Service) disenchantWrite(ctx context.Context, op *operation, code uint32, payload []byte) (bool, error) {
	if code != opcode.CMSGCastSpell && code != opcode.CMSGAutostoreLootItem {
		return false, fail("unsupported_selected_disenchant_mutation")
	}
	call, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	select {
	case s.observeGate <- struct{}{}:
		defer func() { <-s.observeGate }()
	case <-call.Done():
		return false, call.Err()
	}
	if err := s.renew(call, op); err != nil {
		return false, err
	}
	w := &worldv1.SnapshotResponse{}
	if err := s.invoke(call, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, w); err != nil {
		return false, err
	}
	v, err := validateWorld(w, s.selfGUID)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	valid := s.active == op && !s.closed && !terminalStatus(op.receipt.Status) && !op.windowInvalid && call.Err() == nil && op.ctx.Err() == nil && op.request.TargetPositionRevision == 0 && op.baseline.self.PositionRevision > 0 && v.self.PositionRevision == op.baseline.self.PositionRevision && v.self.MapId == op.baseline.self.MapId && v.inventoryRevision >= s.state.InventoryRevision && v.skillsRevision >= s.state.SkillsRevision
	if valid {
		s.view, s.viewKnown = v, true
		s.state.InventoryKnown, s.state.InventoryRevision = true, v.inventoryRevision
		s.state.SkillsKnown, s.state.SkillsRevision = true, v.skillsRevision
		s.state.BackpackKnown, s.state.BackpackRevision = w.BackpackKnown, v.backpackRevision
		valid = s.activeSafetyLocked(op) == "" && s.disenchantControllersLocked() == ""
		row, reason := disenchantSource(v, op.request.TargetGuid)
		valid = valid && reason == "" && row.Entry == op.receipt.TargetEntry && row.Bag == op.receipt.SourceBag && row.Slot == op.receipt.SourceSlot && row.StackCount == 1
		before, baselineReason := disenchantSource(op.baseline, op.request.TargetGuid)
		valid = valid && baselineReason == "" && row.Entry == before.Entry && row.Bag == before.Bag && row.Slot == before.Slot && row.StackCount == before.StackCount
		if code == opcode.CMSGCastSpell {
			_, reason = s.disenchantTargetLocked(op.request.TargetGuid, op)
			valid = valid && reason == "" && v.inventoryRevision == op.baseline.inventoryRevision && v.skillsRevision == op.baseline.skillsRevision && !op.castReserved
		} else {
			_, reason = s.disenchantTargetLocked(op.request.TargetGuid, op)
			valid = valid && reason == "" && v.freeSlots > 0 && op.receipt.OwnGoObserved && !op.receipt.LootReleaseObserved && op.window != nil && op.window.guid == op.request.TargetGuid
		}
	}
	if !valid {
		s.mu.Unlock()
		return false, fail("final_selected_disenchant_body_source_inventory_skill_or_pose_changed")
	}
	if code == opcode.CMSGCastSpell {
		op.castReserved = true
		s.deAttempted[op.request.TargetGuid] = true
		op.receipt.Status = professionsv1.AttemptStatus_WAITING_CAST
		op.receipt.Reason = "selected_item_disenchant_cast_reserved_once"
	}
	s.state.Revision++
	s.mu.Unlock()
	_, err = s.session.SendPacket(call, &modv1.SendPacketRequest{Opcode: code, Payload: payload})
	if err == nil {
		s.mu.Lock()
		s.state.TransportWrites++
		s.mu.Unlock()
	}
	return true, err
}

func (s *Service) setDisenchantReason(op *operation, reason string, status professionsv1.AttemptStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != op || terminalStatus(op.receipt.Status) || op.windowInvalid {
		return
	}
	op.receipt.Status, op.receipt.Reason = status, reason
	s.state.Revision++
}

func (s *Service) runDisenchant(op *operation) {
	defer close(op.done)
	defer op.cancel()
	defer s.finishDisenchant(op)
	lease := &actionsv1.InteractionLease{}
	call, cancel := context.WithTimeout(op.ctx, 2*time.Second)
	err := s.invoke(call, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "professions", LeaseMs: 30000}, lease)
	cancel()
	if err != nil || lease.Token == "" || lease.State == nil || lease.State.InteractionOwner != "professions" {
		s.setDisenchantReason(op, "disenchant_interaction_lease_unavailable", professionsv1.AttemptStatus_BLOCKED)
		return
	}
	s.mu.Lock()
	op.token = lease.Token
	s.mu.Unlock()
	call, cancel = context.WithTimeout(op.ctx, 2*time.Second)
	err = s.observe(call)
	cancel()
	if err != nil {
		s.setDisenchantReason(op, "disenchant_fresh_dependencies_unavailable", professionsv1.AttemptStatus_BLOCKED)
		return
	}
	s.mu.Lock()
	row, reason := s.disenchantTargetLocked(op.request.TargetGuid, op)
	if reason == "" {
		before, baselineReason := disenchantSource(op.baseline, op.request.TargetGuid)
		if baselineReason != "" || row.Entry != op.receipt.TargetEntry || row.Bag != op.receipt.SourceBag || row.Slot != op.receipt.SourceSlot || row.Entry != before.Entry || row.Bag != before.Bag || row.Slot != before.Slot {
			reason = "selected_item_source_changed_since_explicit_attempt_reservation"
		}
	}
	s.mu.Unlock()
	if reason != "" {
		s.setDisenchantReason(op, reason, professionsv1.AttemptStatus_BLOCKED)
		return
	}
	attempted, err := s.disenchantWrite(op.ctx, op, opcode.CMSGCastSpell, disenchantCastPacket(byte(op.receipt.CastCount), op.request.TargetGuid))
	s.mu.Lock()
	op.receipt.CastTransportWritten = attempted && err == nil
	s.mu.Unlock()
	if err != nil {
		status := professionsv1.AttemptStatus_BLOCKED
		if attempted {
			status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
		}
		s.setDisenchantReason(op, "disenchant_cast_unconfirmed_no_retry: "+err.Error(), status)
		return
	}
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-op.ctx.Done():
			status := professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
			s.mu.Lock()
			if op.receipt.CancelRequested {
				status = professionsv1.AttemptStatus_CANCELLED
			}
			s.mu.Unlock()
			s.setDisenchantReason(op, "finite_selected_disenchant_stopped_no_retry", status)
			return
		case <-ticker.C:
			call, cancel = context.WithTimeout(op.ctx, time.Second)
			err = s.observe(call)
			cancel()
			if err != nil {
				s.setDisenchantReason(op, "disenchant_observations_unavailable_no_retry", professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED)
				return
			}
			if s.stepDisenchant(op) {
				return
			}
		}
	}
}

func (s *Service) stepDisenchant(op *operation) bool {
	s.mu.Lock()
	if s.active != op {
		s.mu.Unlock()
		return true
	}
	s.reconcileDisenchantLocked(op)
	if terminalStatus(op.receipt.Status) || op.windowInvalid {
		s.mu.Unlock()
		return true
	}
	if reason := s.activeSafetyLocked(op); reason != "" {
		op.receipt.Status, op.receipt.Reason = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED, reason
		s.mu.Unlock()
		return true
	}
	if !op.receipt.OwnGoObserved || op.window == nil {
		s.mu.Unlock()
		return false
	}
	for _, slot := range op.window.slots {
		if !op.stored[slot.slot] {
			if op.receipt.LootReleaseObserved {
				op.receipt.Status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
				op.receipt.Reason = "item_window_closed_before_all_selected_material_stores_no_retry"
				s.mu.Unlock()
				return true
			}
			op.stored[slot.slot] = true
			s.mu.Unlock()
			if _, err := s.disenchantWrite(op.ctx, op, opcode.CMSGAutostoreLootItem, storeLootPacket(slot.slot)); err != nil {
				s.setDisenchantReason(op, "disenchant_store_unconfirmed_no_retry: "+err.Error(), professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED)
				return true
			}
			return false
		}
		if !op.removed[slot.slot] || !op.pushed[slot.slot] {
			s.mu.Unlock()
			return false
		}
	}
	// The server releases/destroys the selected item after its final pickup.
	// Sending LOOT_RELEASE here can destroy unclaimed materials and is forbidden.
	s.mu.Unlock()
	return false
}

// Read-only reconciliation is valid after the worker finishes. It cannot open,
// store, release, recast or recover anything. Missing evidence stays pending.
func (s *Service) reconcileDisenchantLocked(op *operation) {
	if op == nil || op.spec.skill != disenchantSkill || !op.castReserved || op.windowInvalid || !s.viewKnown || s.view.raw == nil {
		return
	}
	r := op.receipt
	if rank, ok := s.view.skills[disenchantSkill]; ok && s.view.skillsRevision > r.BaselineSkillsRevision && rank.value >= r.BeforeRank {
		r.AfterRank, r.ObservedSkillsRevision = rank.value, s.view.skillsRevision
		r.SkillIncreased = rank.value > r.BeforeRank
	}
	if s.view.inventoryRevision <= op.baseline.inventoryRevision {
		return
	}
	if _, _, err := carriedInventory(s.view.raw); err != nil {
		return
	}
	absent := true
	for _, bag := range s.view.raw.InventoryBags {
		if bag.ContainerGuid == op.request.TargetGuid {
			absent = false
		}
		for _, row := range bag.Slots {
			if row.ItemGuid == op.request.TargetGuid {
				absent = false
			}
		}
	}
	for _, hand := range []*worldv1.EquippedWeapon{s.view.self.Mainhand, s.view.self.Offhand} {
		if hand != nil && hand.SlotKnown && hand.ItemGuid == op.request.TargetGuid {
			absent = false
		}
	}
	r.InputConsumedObserved = absent
	if absent {
		r.ObservedInventoryRevision = s.view.inventoryRevision
	}
	if !absent || !r.OwnGoObserved || op.window == nil || len(op.window.slots) == 0 || op.window.guid != op.request.TargetGuid || !r.LootReleaseObserved {
		return
	}
	added := map[uint32]uint64{}
	for _, slot := range op.window.slots {
		if !op.stored[slot.slot] || !op.removed[slot.slot] || !op.pushed[slot.slot] {
			return
		}
		added[slot.entry] += uint64(slot.count)
	}
	if op.baseline.counts[r.TargetEntry] == 0 {
		return
	}
	expected := map[uint32]uint64{}
	for entry, n := range op.baseline.counts {
		expected[entry] = uint64(n)
	}
	expected[r.TargetEntry]--
	for entry, n := range added {
		expected[entry] += n
	}
	for entry, n := range expected {
		if uint64(s.view.counts[entry]) != n {
			return
		}
	}
	for entry, n := range s.view.counts {
		if uint64(n) != expected[entry] {
			return
		}
	}
	r.OutputInventoryObserved = len(added) > 0
	r.ObservedInventoryRevision = s.view.inventoryRevision
	// Positive material facts never erase an actual matched server rejection.
	// Such contradictory receipts remain pending for operator follow-up.
	if r.Status == professionsv1.AttemptStatus_SERVER_REJECTED {
		return
	}
	if s.pendingDisenchant == op && !r.LeaseReleased {
		r.Status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
		r.Reason = "selected_disenchant_materials_proven_but_owned_lease_cleanup_unconfirmed"
		r.CleanupUnconfirmed = true
		return
	}
	r.Status = professionsv1.AttemptStatus_COMPLETED
	r.Reason = "selected_item_own_GO_material_push_remove_auto_release_consumption_and_exact_allbag_delta_observed"
	if s.pendingDisenchant == op {
		r.CleanupUnconfirmed = !r.LeaseReleased
		if r.LeaseReleased {
			s.pendingDisenchant = nil
		}
	}
	s.state.Revision++
}

func (s *Service) onDisenchantPacketLocked(op *operation, p *modv1.WorldPacket) {
	if !op.castReserved {
		return
	}
	if op.windowInvalid && (p.Opcode == opcode.SMSGLootRemoved || p.Opcode == opcode.SMSGItemPushResult || p.Opcode == opcode.SMSGLootReleaseResponse) {
		return
	}
	r := op.receipt
	invalidate := func() {
		op.windowInvalid = true
		r.CleanupUnconfirmed = true
		if !terminalStatus(r.Status) {
			r.Status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
			r.Reason = "selected_disenchant_current_window_changed_or_unparseable_no_more_writes"
		}
		s.state.Revision++
	}
	switch p.Opcode {
	case opcode.SMSGSpellStart, opcode.SMSGSpellGo, opcode.SMSGCastFailed, opcode.SMSGSpellFailure:
		e, err := decodeDisenchantCast(p.Opcode, p.Payload)
		if err != nil {
			s.state.MalformedPackets++
			return
		}
		if e.spell != disenchantSpell || uint32(e.count) != r.CastCount || p.Opcode != opcode.SMSGCastFailed && e.caster != s.selfGUID || e.kind != "failed" && e.target != r.TargetGuid {
			return
		}
		switch e.kind {
		case "start":
			r.OwnStartObserved = true
		case "go":
			r.OwnGoObserved = true
		case "failed":
			r.FailureCode = uint32(e.failure)
			if !terminalStatus(r.Status) {
				r.Status = professionsv1.AttemptStatus_SERVER_REJECTED
				r.Reason = fmt.Sprintf("actual_selected_disenchant_server_cast_failure_%d", e.failure)
			}
		}
	case opcode.SMSGLootResponse:
		if op.windowInvalid {
			return
		}
		if len(p.Payload) == 10 && binary.LittleEndian.Uint64(p.Payload) == r.TargetGuid && p.Payload[8] == 0 && op.window == nil {
			if !terminalStatus(r.Status) {
				r.Status = professionsv1.AttemptStatus_SERVER_REJECTED
				r.Reason = fmt.Sprintf("actual_selected_disenchant_loot_error_%d", p.Payload[9])
			}
			return
		}
		w, err := decodeDisenchantLoot(p.Payload)
		if err != nil {
			s.state.MalformedPackets++
			invalidate()
			return
		}
		if w.guid != r.TargetGuid || op.window != nil {
			invalidate()
			return
		}
		op.window = &w
		r.LootWindowObserved = true
		if !terminalStatus(r.Status) {
			r.Status = professionsv1.AttemptStatus_WAITING_LOOT
		}
	case opcode.SMSGLootRemoved:
		if len(p.Payload) != 1 {
			s.state.MalformedPackets++
			return
		}
		if op.window == nil {
			return
		}
		for _, slot := range op.window.slots {
			if slot.slot == p.Payload[0] && op.stored[slot.slot] && !op.removed[slot.slot] {
				op.removed[slot.slot] = true
				r.LootSlotsRemoved++
			}
		}
	case opcode.SMSGItemPushResult:
		e, err := decodePush(p.Payload)
		if err != nil {
			s.state.MalformedPackets++
			return
		}
		if op.window == nil || e.player != s.selfGUID || e.received || e.created || !carriedPushLocation(s.view, e.bag, e.slot) {
			return
		}
		for _, slot := range op.window.slots {
			if op.stored[slot.slot] && !op.pushed[slot.slot] && slot.entry == e.entry && slot.count == e.added {
				op.pushed[slot.slot] = true
				op.pushCounts[e.entry] += e.added
				r.OwnLootedItemsObserved += e.added
				break
			}
		}
	case opcode.SMSGLootReleaseResponse:
		if len(p.Payload) != 9 {
			s.state.MalformedPackets++
			invalidate()
			return
		}
		if binary.LittleEndian.Uint64(p.Payload) != r.TargetGuid {
			invalidate()
			return
		}
		if op.window != nil && p.Payload[8] == 1 {
			r.LootReleaseObserved = true // Automatic release; never sent by us.
		} else if !terminalStatus(r.Status) {
			r.Status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
			r.Reason = "selected_disenchant_auto_release_not_confirmed"
		}
	case opcode.SMSGInventoryChangeFailure:
		if len(p.Payload) == 0 || p.Payload[0] == 0 {
			return
		}
		if len(p.Payload) != 18 && len(p.Payload) != 22 && len(p.Payload) != 38 {
			s.state.MalformedPackets++
			return
		}
		if !terminalStatus(r.Status) {
			r.Status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
			r.Reason = fmt.Sprintf("unattributed_selected_disenchant_inventory_error_%d", p.Payload[0])
		}
	}
	s.reconcileDisenchantLocked(op)
	s.state.Revision++
}

func disenchantUnsettled(op *operation) bool {
	if op.windowInvalid {
		return true
	}
	if !op.castReserved {
		return false
	}
	if op.receipt.Status == professionsv1.AttemptStatus_COMPLETED {
		return false
	}
	return !(op.receipt.FailureCode != 0 && !op.receipt.OwnGoObserved && op.window == nil)
}

func (s *Service) finishDisenchant(op *operation) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.mu.Lock()
	if s.active != op {
		s.mu.Unlock()
		return
	}
	token := op.token
	cancelCast := op.castReserved && op.receipt.OwnStartObserved && !op.receipt.OwnGoObserved && op.window == nil && op.receipt.FailureCode == 0 && !op.cancelReserved
	if cancelCast {
		op.cancelReserved = true
	}
	s.mu.Unlock()
	// Only a still-owned in-progress spell may be cancelled. An item window is
	// never closed here: closing partial disenchant loot can destroy materials.
	if token != "" && cancelCast && s.renew(ctx, op) == nil {
		_, err := s.session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: opcode.CMSGCancelCast, Payload: binary.LittleEndian.AppendUint32([]byte{byte(op.receipt.CastCount)}, disenchantSpell)})
		s.mu.Lock()
		op.receipt.CancelTransportWritten = err == nil
		if err == nil {
			s.state.TransportWrites++
		}
		s.mu.Unlock()
	}
	until := time.Now().Add(1100 * time.Millisecond)
	for time.Now().Before(until) {
		s.mu.Lock()
		s.reconcileDisenchantLocked(op)
		pending := disenchantUnsettled(op)
		s.mu.Unlock()
		if !pending {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(20 * time.Millisecond):
		}
	}
	released := token == ""
	if token != "" {
		err := s.renew(ctx, op)
		out := &actionsv1.State{}
		if err == nil {
			err = s.invoke(ctx, "buddy-actions", "release_interaction", &actionsv1.InteractionRelease{Owner: "professions", Token: token}, out)
		}
		released = err == nil && out.InteractionOwner == ""
	}
	s.mu.Lock()
	if s.active == op {
		op.receipt.LeaseReleased = released
		s.reconcileDisenchantLocked(op)
		op.receipt.CleanupUnconfirmed = !released || disenchantUnsettled(op)
		if op.receipt.CleanupUnconfirmed {
			s.pendingDisenchant = op
			if !terminalStatus(op.receipt.Status) || op.receipt.Status == professionsv1.AttemptStatus_COMPLETED {
				op.receipt.Status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
				op.receipt.Reason = "selected_disenchant_pending_no_window_close_or_automatic_recovery"
			}
		}
		s.active = nil
		s.state.Revision++
	}
	s.mu.Unlock()
}
