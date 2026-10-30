package buddyprofessions

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func (s *Service) queryHerbTemplates(ctx context.Context, w *worldv1.SnapshotResponse) {
	if w == nil || len(w.RequestedGameObjects) > 32 {
		return
	}
	query := []*worldv1.GameObjectState{}
	s.mu.Lock()
	for _, g := range w.RequestedGameObjects {
		if len(query) >= 4 || len(s.herbRequested) >= 32 {
			break
		}
		if g == nil || !g.Present || g.Guid>>48 != 0xf110 || g.Guid&0xffffff == 0 || g.Entry == 0 || uint32(g.Guid>>24&0xffffff) != g.Entry || !g.TypeKnown || g.GameObjectType != 3 || s.herbRequested[g.Entry] {
			continue
		}
		s.herbRequested[g.Entry] = true
		query = append(query, proto.Clone(g).(*worldv1.GameObjectState))
	}
	s.mu.Unlock()
	for _, g := range query {
		if _, err := s.session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: opcode.CMSGGameobjectQuery, Payload: herbQueryPacket(g.Entry, g.Guid)}); err == nil {
			s.mu.Lock()
			s.state.TransportWrites++
			s.mu.Unlock()
		}
	}
}

func (s *Service) onHerbTemplatePacketLocked(p *modv1.WorldPacket) bool {
	if p.Opcode != opcode.SMSGGameobjectQueryResponse || len(p.Payload) < 4 {
		return false
	}
	entry := binary.LittleEndian.Uint32(p.Payload) & 0x7fffffff
	if !s.herbRequested[entry] {
		return false
	}
	m, err := decodeHerbTemplate(p.Payload)
	if err != nil {
		s.state.MalformedPackets++
	} else {
		s.herbTemplates[m.entry] = m
	}
	s.state.Revision++
	return true
}

func herbSource(v worldView, a *actionsv1.State, guid, revision uint64) (*worldv1.GameObjectState, string) {
	if v.raw == nil || v.self == nil || len(v.raw.RequestedGameObjects) > 32 {
		return nil, "bounded_actual_herb_observation_required"
	}
	var g *worldv1.GameObjectState
	for _, row := range v.raw.RequestedGameObjects {
		if row != nil && row.Guid == guid {
			if g != nil {
				return nil, "duplicate_actual_herb_guid"
			}
			g = row
		}
	}
	if g == nil || guid>>48 != 0xf110 || guid&0xffffff == 0 || g.Entry == 0 || uint32(guid>>24&0xffffff) != g.Entry || !g.Present || !g.TypeKnown || g.GameObjectType != 3 || !g.MapKnown || g.MapId != v.self.MapId || !g.PositionKnown || revision == 0 || g.PositionRevision != revision {
		return nil, "exact_current_observed_chest_guid_type_map_pose_required"
	}
	if a == nil || a.Position == nil || !a.Position.Known || !a.Position.MapKnown || a.Position.MapId != g.MapId {
		return nil, "stopped_current_client_position_required"
	}
	p := a.Position
	for _, x := range []float32{g.X, g.Y, g.Z, p.X, p.Y, p.Z} {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) || math.Abs(float64(x)) > 40000 {
			return nil, "invalid_actual_herb_or_client_position"
		}
	}
	dx, dy, dz := float64(g.X-p.X), float64(g.Y-p.Y), float64(g.Z-p.Z)
	if dx*dx+dy*dy+dz*dz > 9 || math.Abs(dz) > 1.5 {
		return nil, "selected_herb_must_already_be_within_three_yards"
	}
	return g, ""
}

func (s *Service) herbalismTargetLocked(guid, revision uint64, own *operation) (*worldv1.GameObjectState, string) {
	if !s.viewKnown || s.view.self == nil {
		return nil, "actual_own_observations_required"
	}
	spec, _ := recipeForSpell(herbSpell)
	if reason := s.readinessLocked(spec, own); reason != "" {
		return nil, reason
	}
	g, reason := herbSource(s.view, s.actions, guid, revision)
	if reason != "" {
		return nil, reason
	}
	// LOCKED2 is expected when a compatible gathering spell is required.
	// Quest ACTIVATE/SPARKLE bits are not generic gathering readiness evidence.
	if !g.FlagsKnown || g.Flags & ^uint32(2) != 0 || !g.DynamicFlagsKnown || g.DynamicFlags&4 != 0 || !g.StateKnown || g.State != 1 || !g.CreatedByKnown || g.CreatedByGuid != 0 {
		return nil, "ordinary_ready_chest_flags_state_and_creator_required"
	}
	m, ok := s.herbTemplates[g.Entry]
	if !ok {
		return nil, "actual_public_single_herbalism_lock_template_required"
	}
	if rank, ok := s.view.skills[herbSkill]; !ok || rank.value < m.requiredSkill {
		return nil, "observed_pure_herbalism_rank_below_lock_requirement"
	}
	if s.herbAttempted[guid] && (own == nil || s.active != own || own.request.TargetGuid != guid) {
		return nil, "herb_guid_already_reserved_no_replay"
	}
	if len(s.herbAttempted) >= 256 && !s.herbAttempted[guid] {
		return nil, "finite_herbalism_target_receipt_capacity_reached"
	}
	return g, ""
}

func (s *Service) herbTargetsLocked() []*professionsv1.HerbTarget {
	if !s.viewKnown || s.view.raw == nil || len(s.view.raw.RequestedGameObjects) > 32 {
		return nil
	}
	out := []*professionsv1.HerbTarget{}
	for _, g := range s.view.raw.RequestedGameObjects {
		if g == nil {
			continue
		}
		_, reason := s.herbalismTargetLocked(g.Guid, g.PositionRevision, nil)
		m := s.herbTemplates[g.Entry]
		out = append(out, &professionsv1.HerbTarget{Object: proto.Clone(g).(*worldv1.GameObjectState), Ready: reason == "", Reason: reason, RequiredSkill: m.requiredSkill, LockId: m.lockID})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Object.Guid < out[j].Object.Guid })
	return out
}

// Fresh dependency reads precede this function. Exact-token renewal precedes
// the LAST world read; only local validation/reservation follows before Send.
func (s *Service) herbWrite(ctx context.Context, op *operation, code uint32, payload []byte) (bool, error) {
	if code != opcode.CMSGCastSpell && code != opcode.CMSGAutostoreLootItem {
		return false, fail("unsupported_herbalism_mutation")
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
	if err := s.invoke(call, "buddy-world", "snapshot", &worldv1.SnapshotRequest{GameObjectEntry: uint32(op.request.TargetGuid >> 24 & 0xffffff)}, w); err != nil {
		return false, err
	}
	v, err := validateWorld(w, s.selfGUID)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	valid := s.active == op && !s.closed && !terminalStatus(op.receipt.Status) && !op.windowInvalid && call.Err() == nil && op.ctx.Err() == nil && op.baseline.self.PositionRevision > 0 && v.self.PositionRevision == op.baseline.self.PositionRevision && v.self.MapId == op.baseline.self.MapId && v.inventoryRevision >= s.state.InventoryRevision && v.skillsRevision >= s.state.SkillsRevision
	if valid {
		s.view, s.viewKnown = v, true
		s.state.InventoryKnown, s.state.InventoryRevision = true, v.inventoryRevision
		s.state.SkillsKnown, s.state.SkillsRevision = true, v.skillsRevision
		s.state.BackpackKnown, s.state.BackpackRevision = w.BackpackKnown, v.backpackRevision
		valid = s.activeSafetyLocked(op) == ""
		c, reason := herbSource(v, s.actions, op.request.TargetGuid, op.request.TargetPositionRevision)
		valid = valid && reason == "" && c.Entry == op.receipt.TargetEntry
		if code == opcode.CMSGCastSpell {
			_, reason = s.herbalismTargetLocked(op.request.TargetGuid, op.request.TargetPositionRevision, op)
			valid = valid && reason == "" && v.inventoryRevision == op.baseline.inventoryRevision && v.skillsRevision == op.baseline.skillsRevision && s.readinessLocked(op.spec, op) == "" && !op.castReserved
		} else {
			valid = valid && v.freeSlots > 0 && op.receipt.OwnGoObserved && op.window != nil && op.window.guid == op.request.TargetGuid
		}
	}
	if !valid {
		s.mu.Unlock()
		return false, fail("final_herbalism_body_source_inventory_skill_or_pose_changed")
	}
	if code == opcode.CMSGCastSpell {
		op.castReserved = true
		s.herbAttempted[op.request.TargetGuid] = true
		op.receipt.Status = professionsv1.AttemptStatus_WAITING_CAST
		op.receipt.Reason = "exact_target_herbalism_cast_reserved_once"
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

func (s *Service) setHerbReason(op *operation, reason string, status professionsv1.AttemptStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != op || terminalStatus(op.receipt.Status) || op.windowInvalid {
		return
	}
	op.receipt.Status, op.receipt.Reason = status, reason
	s.state.Revision++
}

func (s *Service) runHerb(op *operation) {
	defer close(op.done)
	defer op.cancel()
	defer s.finishHerb(op)
	lease := &actionsv1.InteractionLease{}
	call, cancel := context.WithTimeout(op.ctx, 2*time.Second)
	err := s.invoke(call, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "professions", LeaseMs: 30000}, lease)
	cancel()
	if err != nil || lease.Token == "" || lease.State == nil || lease.State.InteractionOwner != "professions" {
		s.setHerbReason(op, "herbalism_interaction_lease_unavailable", professionsv1.AttemptStatus_BLOCKED)
		return
	}
	s.mu.Lock()
	op.token = lease.Token
	s.mu.Unlock()
	call, cancel = context.WithTimeout(op.ctx, 2*time.Second)
	err = s.observe(call)
	cancel()
	if err != nil {
		s.setHerbReason(op, "herbalism_fresh_dependencies_unavailable", professionsv1.AttemptStatus_BLOCKED)
		return
	}
	s.mu.Lock()
	c, reason := s.herbalismTargetLocked(op.request.TargetGuid, op.request.TargetPositionRevision, op)
	if reason == "" {
		op.receipt.TargetEntry = c.Entry
	}
	s.mu.Unlock()
	if reason != "" {
		s.setHerbReason(op, reason, professionsv1.AttemptStatus_BLOCKED)
		return
	}
	attempted, err := s.herbWrite(op.ctx, op, opcode.CMSGCastSpell, herbCastPacket(byte(op.receipt.CastCount), op.request.TargetGuid))
	s.mu.Lock()
	op.receipt.CastTransportWritten = attempted && err == nil
	s.mu.Unlock()
	if err != nil {
		status := professionsv1.AttemptStatus_BLOCKED
		if attempted {
			status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
		}
		s.setHerbReason(op, "herbalism_cast_unconfirmed_no_retry: "+err.Error(), status)
		return
	}
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-op.ctx.Done():
			s.mu.Lock()
			cancelled := op.receipt.CancelRequested
			terminal := terminalStatus(op.receipt.Status)
			s.mu.Unlock()
			if !terminal {
				status := professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
				if cancelled {
					status = professionsv1.AttemptStatus_CANCELLED
				}
				s.setHerbReason(op, "finite_herbalism_stopped_no_retry", status)
			}
			return
		case <-ticker.C:
			call, cancel = context.WithTimeout(op.ctx, time.Second)
			err = s.observe(call)
			cancel()
			if err != nil {
				s.setHerbReason(op, "herbalism_observations_unavailable_no_retry", professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED)
				return
			}
			if s.stepHerb(op) {
				return
			}
		}
	}
}

func (s *Service) stepHerb(op *operation) bool {
	s.mu.Lock()
	if s.active != op || terminalStatus(op.receipt.Status) || op.windowInvalid {
		s.mu.Unlock()
		return true
	}
	if reason := s.activeSafetyLocked(op); reason != "" {
		op.receipt.Status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
		op.receipt.Reason = reason
		s.mu.Unlock()
		return true
	}
	r := op.receipt
	if !r.OwnGoObserved || op.window == nil {
		s.mu.Unlock()
		return false
	}
	for _, slot := range op.window.slots {
		if !op.stored[slot.slot] {
			op.stored[slot.slot] = true
			s.mu.Unlock()
			if _, err := s.herbWrite(op.ctx, op, opcode.CMSGAutostoreLootItem, storeLootPacket(slot.slot)); err != nil {
				s.setHerbReason(op, "herbalism_store_unconfirmed_no_retry: "+err.Error(), professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED)
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
		s.mu.Unlock()
		if err := s.herbCleanupWrite(op.ctx, op, opcode.CMSGLootRelease, releaseLootPacket(op.request.TargetGuid)); err != nil {
			s.setHerbReason(op, "herbalism_release_unconfirmed_no_retry", professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED)
			return true
		}
		return false
	}
	counts := map[uint32]uint32{}
	for _, slot := range op.window.slots {
		counts[slot.entry] += slot.count
	}
	inventory := s.view.inventoryRevision > op.baseline.inventoryRevision
	for entry, before := range op.baseline.counts {
		if uint64(s.view.counts[entry]) != uint64(before)+uint64(counts[entry]) {
			inventory = false
		}
	}
	for entry, after := range s.view.counts {
		if uint64(after) != uint64(op.baseline.counts[entry])+uint64(counts[entry]) {
			inventory = false
		}
	}
	for entry, added := range counts {
		if uint64(s.view.counts[entry]) != uint64(op.baseline.counts[entry])+uint64(added) {
			inventory = false
		}
	}
	r.OutputInventoryObserved = inventory && len(counts) > 0
	if inventory {
		r.ObservedInventoryRevision = s.view.inventoryRevision
	}
	if r.LootReleaseObserved && inventory && len(counts) > 0 {
		r.Status = professionsv1.AttemptStatus_COMPLETED
		r.Reason = "exact_target_herbalism_GO_loot_slots_push_release_and_newer_allbag_delta_observed"
		s.state.Revision++
		s.mu.Unlock()
		return true
	}
	if len(counts) == 0 && r.LootReleaseObserved {
		r.Status = professionsv1.AttemptStatus_COMPLETED
		r.Reason = "exact_target_herbalism_GO_empty_window_and_release_observed_no_materials"
		s.state.Revision++
		s.mu.Unlock()
		return true
	}
	s.mu.Unlock()
	return false
}

func (s *Service) onHerbPacketLocked(op *operation, p *modv1.WorldPacket) {
	if !op.castReserved {
		return
	}
	if op.windowInvalid && (p.Opcode == opcode.SMSGLootRemoved || p.Opcode == opcode.SMSGItemPushResult || p.Opcode == opcode.SMSGLootReleaseResponse) {
		return
	}
	r := op.receipt
	switch p.Opcode {
	case opcode.SMSGSpellStart, opcode.SMSGSpellGo, opcode.SMSGCastFailed, opcode.SMSGSpellFailure:
		e, err := decodeHerbCast(p.Opcode, p.Payload)
		if err != nil {
			s.state.MalformedPackets++
			return
		}
		// Only CAST_FAILED omits its caster on the wire. Every START, GO and
		// SPELL_FAILURE must name this actual actor, including during cleanup.
		if e.spell != herbSpell || uint32(e.count) != r.CastCount || p.Opcode != opcode.SMSGCastFailed && e.caster != s.selfGUID {
			return
		}
		if e.kind != "failed" && e.target != r.TargetGuid {
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
				r.Reason = fmt.Sprintf("actual_herbalism_server_cast_failure_%d", e.failure)
			}
		}
	case opcode.SMSGLootResponse:
		// AUTOSTORE names only a slot. Any contrary current-window observation
		// permanently invalidates that authority, including for cleanup release.
		// Do not re-arm this operation from a later response after ambiguity.
		invalidate := func() {
			op.windowInvalid = true
			r.CleanupUnconfirmed = true
			if !terminalStatus(r.Status) {
				r.Status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
				r.Reason = "herbalism_current_loot_window_changed_or_unparseable_no_further_window_writes"
			}
			s.state.Revision++
		}
		if op.windowInvalid {
			return
		}
		if len(p.Payload) >= 10 && binary.LittleEndian.Uint64(p.Payload) == r.TargetGuid && p.Payload[8] == 0 {
			if len(p.Payload) != 10 {
				s.state.MalformedPackets++
				invalidate()
				return
			}
			if op.window != nil {
				invalidate()
				return
			}
			if !terminalStatus(r.Status) {
				r.Status = professionsv1.AttemptStatus_SERVER_REJECTED
				r.Reason = fmt.Sprintf("actual_herbalism_loot_error_%d", p.Payload[9])
			}
			return
		}
		w, err := decodeHerbLoot(p.Payload)
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
			return
		}
		if op.releaseReserved && binary.LittleEndian.Uint64(p.Payload) == r.TargetGuid {
			if p.Payload[8] == 1 {
				r.LootReleaseObserved = true
			} else {
				if !terminalStatus(r.Status) {
					r.Status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
					r.Reason = "actual_herbalism_release_not_confirmed"
				}
			}
		}
	case opcode.SMSGInventoryChangeFailure:
		if len(p.Payload) == 0 || p.Payload[0] == 0 {
			return
		}
		if len(p.Payload) != 18 && len(p.Payload) != 22 && len(p.Payload) != 38 {
			s.state.MalformedPackets++
			return
		}
		// The GUID-less inventory error is not a cast/target acknowledgement.
		// It cannot settle an automatic herbalism window that has not arrived.
		if !terminalStatus(r.Status) {
			r.Status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
			r.Reason = fmt.Sprintf("unattributed_herbalism_inventory_error_%d", p.Payload[0])
		}
	}
	s.state.Revision++
}

func (s *Service) herbCleanupWrite(ctx context.Context, op *operation, code uint32, p []byte) error {
	if code != opcode.CMSGLootRelease && code != opcode.CMSGCancelCast {
		return fail("unsupported_herbalism_cleanup_mutation")
	}
	call, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := s.renew(call, op); err != nil {
		return err
	}
	s.mu.Lock()
	valid := s.active == op
	if code == opcode.CMSGLootRelease {
		valid = valid && !op.windowInvalid && op.window != nil && op.window.guid == op.request.TargetGuid && len(p) == 8 && binary.LittleEndian.Uint64(p) == op.request.TargetGuid
	}
	s.mu.Unlock()
	if !valid {
		return fail("herb_cleanup_operation_replaced")
	}
	_, err := s.session.SendPacket(call, &modv1.SendPacketRequest{Opcode: code, Payload: p})
	if err == nil {
		s.mu.Lock()
		s.state.TransportWrites++
		s.mu.Unlock()
	}
	return err
}

func herbCleanupPending(op *operation) bool {
	r := op.receipt
	return op.windowInvalid || op.window != nil && !r.LootReleaseObserved || op.castReserved && !r.OwnGoObserved && r.FailureCode == 0 || op.castReserved && r.OwnGoObserved && op.window == nil && r.Status != professionsv1.AttemptStatus_SERVER_REJECTED
}

func (s *Service) finishHerb(op *operation) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.mu.Lock()
	if s.active != op {
		s.mu.Unlock()
		return
	}
	token := op.token
	window := !op.windowInvalid && op.window != nil && !op.receipt.LootReleaseObserved
	release := window && !op.releaseReserved
	if release {
		op.releaseReserved = true
	}
	cancelCast := op.castReserved && op.receipt.OwnStartObserved && !op.receipt.OwnGoObserved && op.receipt.FailureCode == 0 && !op.cancelReserved
	if cancelCast {
		op.cancelReserved = true
	}
	s.mu.Unlock()
	if token != "" && release {
		_ = s.herbCleanupWrite(ctx, op, opcode.CMSGLootRelease, releaseLootPacket(op.request.TargetGuid))
	}
	if token != "" && cancelCast {
		err := s.herbCleanupWrite(ctx, op, opcode.CMSGCancelCast, binary.LittleEndian.AppendUint32([]byte{byte(op.receipt.CastCount)}, herbSpell))
		s.mu.Lock()
		op.receipt.CancelTransportWritten = err == nil
		s.mu.Unlock()
	}
	until := time.Now().Add(1100 * time.Millisecond)
	for time.Now().Before(until) {
		s.mu.Lock()
		late := !op.windowInvalid && op.window != nil && !op.receipt.LootReleaseObserved && !op.releaseReserved
		if late {
			op.releaseReserved = true
		}
		pending := herbCleanupPending(op)
		s.mu.Unlock()
		if late && token != "" {
			_ = s.herbCleanupWrite(ctx, op, opcode.CMSGLootRelease, releaseLootPacket(op.request.TargetGuid))
		}
		if !pending {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(20 * time.Millisecond):
		}
	}
	s.mu.Lock()
	op.receipt.CleanupUnconfirmed = herbCleanupPending(op)
	op.receipt.LeaseReleased = token == ""
	s.mu.Unlock()
	if token != "" {
		err := s.renew(ctx, op)
		out := &actionsv1.State{}
		if err == nil {
			err = s.invoke(ctx, "buddy-actions", "release_interaction", &actionsv1.InteractionRelease{Owner: "professions", Token: token}, out)
		}
		s.mu.Lock()
		op.receipt.LeaseReleased = err == nil && out.InteractionOwner == ""
		op.receipt.CleanupUnconfirmed = op.receipt.CleanupUnconfirmed || !op.receipt.LeaseReleased
		s.mu.Unlock()
	}
	s.mu.Lock()
	if s.active == op {
		if op.receipt.Status == professionsv1.AttemptStatus_COMPLETED && op.receipt.CleanupUnconfirmed {
			op.receipt.Status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
			op.receipt.Reason = "herbalism_receipts_complete_owned_cleanup_unconfirmed"
		}
		s.active = nil
		s.state.Revision++
	}
	s.mu.Unlock()
}
