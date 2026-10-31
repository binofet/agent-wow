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
	"github.com/agent-wow/agent-wow/pkg/harvestguard"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

type normalLootProof struct {
	window     lootWindow
	removed    map[byte]bool
	moneyClear bool
	at         time.Time
}

// Optional passive own-loot diagnostics, never a request to loot or operate a
// character. Party skinning readiness comes from the positive server flag below;
// a recipient-specific clear LOOTABLE bit alone is not global loot.isLooted().
func (s *Service) observeCorpseLootLocked(p *modv1.WorldPacket) {
	switch p.Opcode {
	case opcode.SMSGLootResponse:
		s.skinLoot = nil
		if len(p.Payload) < 9 || p.Payload[8] != 1 {
			return
		}
		w, err := decodeNormalCorpseLoot(p.Payload)
		if err != nil {
			return
		}
		delete(s.fullLoot, w.guid)
		s.skinLoot = &normalLootProof{window: w, removed: map[byte]bool{}, moneyClear: w.gold == 0, at: s.now()}
	case opcode.SMSGLootRemoved:
		if s.skinLoot == nil || len(p.Payload) != 1 {
			return
		}
		for _, slot := range s.skinLoot.window.slots {
			if slot.slot == p.Payload[0] {
				s.skinLoot.removed[slot.slot] = true
			}
		}
	case opcode.SMSGLootClearMoney:
		if s.skinLoot != nil && len(p.Payload) == 0 {
			s.skinLoot.moneyClear = true
		}
	case opcode.SMSGLootReleaseResponse:
		proof := s.skinLoot
		if proof == nil || len(p.Payload) != 9 || binary.LittleEndian.Uint64(p.Payload) != proof.window.guid {
			return
		}
		s.skinLoot = nil
		if p.Payload[8] != 1 || !proof.moneyClear || s.now().Sub(proof.at) < 0 || s.now().Sub(proof.at) > 60*time.Second {
			return
		}
		for _, slot := range proof.window.slots {
			if !proof.removed[slot.slot] {
				return
			}
		}
		for guid, at := range s.fullLoot {
			if s.now().Sub(at) < 0 || s.now().Sub(at) > 60*time.Second {
				delete(s.fullLoot, guid)
			}
		}
		if len(s.fullLoot) >= 32 {
			var oldest uint64
			var at time.Time
			for guid, t := range s.fullLoot {
				if oldest == 0 || t.Before(at) || t.Equal(at) && guid < oldest {
					oldest, at = guid, t
				}
			}
			delete(s.fullLoot, oldest)
		}
		s.fullLoot[proof.window.guid] = s.now()
		s.state.Revision++
	}
}

func (s *Service) querySkinTemplates(ctx context.Context, w *worldv1.SnapshotResponse) {
	if w == nil || len(w.SkinnableCorpses) > 32 {
		return
	}
	rows := append([]*worldv1.CreatureState(nil), w.SkinnableCorpses...)
	if w.RequestedCreature != nil {
		rows = append(rows, w.RequestedCreature)
	}
	query := []*worldv1.CreatureState{}
	s.mu.Lock()
	for _, c := range rows {
		if len(query) >= 4 || len(s.skinRequested) >= 32 {
			break
		}
		if c == nil || !c.Present || c.Guid>>48 != 0xf130 || uint32(c.Guid) == 0 || c.Entry == 0 || uint32(c.Guid>>24&0xffffff) != c.Entry || !c.UnitFlagsKnown || c.UnitFlags&0x04000000 == 0 || s.skinRequested[c.Entry] {
			continue
		}
		s.skinRequested[c.Entry] = true
		query = append(query, proto.Clone(c).(*worldv1.CreatureState))
	}
	s.mu.Unlock()
	for _, c := range query {
		if _, err := s.session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: opcode.CMSGCreatureQuery, Payload: skinQueryPacket(c.Entry, c.Guid)}); err == nil {
			s.mu.Lock()
			s.state.TransportWrites++
			s.mu.Unlock()
		}
	}
}

func (s *Service) onSkinTemplatePacketLocked(p *modv1.WorldPacket) bool {
	if p.Opcode != opcode.SMSGCreatureQueryResponse {
		return false
	}
	if len(p.Payload) < 4 {
		return true
	}
	entry := binary.LittleEndian.Uint32(p.Payload) & 0x7fffffff
	if !s.skinRequested[entry] {
		return true
	}
	m, err := decodeSkinTemplate(p.Payload)
	if err != nil {
		s.state.MalformedPackets++
	} else {
		s.skinTemplates[m.entry] = m
	}
	s.state.Revision++
	return true
}

// Conservative pure-skill eligibility. Server CheckCast uses effective skill
// and a skill<100 branch; these canonical minimums never rely on hidden bonuses.
func skinRequiredSkill(level uint32) uint32 {
	if level < 10 {
		return 0
	}
	if level < 20 {
		return (level - 10) * 10
	}
	return level * 5
}

func skinCreature(w *worldv1.SnapshotResponse, guid uint64) *worldv1.CreatureState {
	if w == nil {
		return nil
	}
	if c := w.RequestedCreature; c != nil && c.Guid == guid {
		return c
	}
	for _, list := range [][]*worldv1.CreatureState{w.SkinnableCorpses, w.NearbyCreatures} {
		if len(list) > 32 {
			return nil
		}
		for _, c := range list {
			if c != nil && c.Guid == guid {
				return c
			}
		}
	}
	return nil
}

func skinSource(v worldView, a *actionsv1.State, guid, revision uint64) (*worldv1.CreatureState, string) {
	return skinSourceRange(v, a, guid, revision, 3)
}

func skinSourceRange(v worldView, a *actionsv1.State, guid, revision uint64, maxDistance float64) (*worldv1.CreatureState, string) {
	c := skinCreature(v.raw, guid)
	if c == nil || guid>>48 != 0xf130 || uint32(guid) == 0 || c.Entry == 0 || uint32(guid>>24&0xffffff) != c.Entry || !c.Present || !c.HealthKnown || c.Health != 0 || c.MaxHealth == 0 || !c.AliveKnown || c.Alive || !c.UnitFlagsKnown || !c.DynamicFlagsKnown || !c.MapKnown || c.MapId != v.self.MapId || !c.PositionKnown || revision == 0 || c.PositionRevision != revision || !c.LevelKnown || c.Level == 0 || c.Level > 60 {
		return nil, "exact_current_dead_corpse_flags_level_pose_required"
	}
	if t := c.Trajectory; t != nil && t.Known && (!t.Stopped || t.TransportRelative || t.Cyclic) {
		return nil, "corpse_trajectory_not_stopped_world_position"
	}
	if a == nil || a.Position == nil || !a.Position.Known || !a.Position.MapKnown || a.Position.MapId != c.MapId {
		return nil, "stopped_current_client_position_required"
	}
	p := a.Position
	dx, dy, dz := float64(c.X-p.X), float64(c.Y-p.Y), float64(c.Z-p.Z)
	if math.IsNaN(dx+dy+dz) || math.IsInf(dx+dy+dz, 0) || dx*dx+dy*dy+dz*dz > maxDistance*maxDistance || math.Abs(dz) > 1.5 {
		if maxDistance == 40 {
			return nil, "selected_corpse_outside_forty_yard_approach_bound"
		}
		return nil, "selected_corpse_must_already_be_within_three_yards"
	}
	return c, ""
}

// AllLootRemovedFromCorpse sets SKINNABLE only after global normal loot is
// exhausted (including the no-loot death path). UNIT_FIELD_FLAGS is public;
// unlike the recipient-masked LOOTABLE bit, this positive flag is not hidden
// from party members who did not receive a normal loot window. TAPPED_BY_PLAYER
// is generated by isTappedBy for the player or their loot-recipient group.
// Require both signals, so masked LOOTABLE alone and other parties' corpses
// never authorize autonomous skinning. The server rechecks loot.isLooted on cast.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Creature/Creature.cpp
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/LootHandler.cpp
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Unit/Unit.cpp
func partyClearedSkinningCorpse(c *worldv1.CreatureState) bool {
	return c != nil && c.Guid>>48 == 0xf130 && uint32(c.Guid) != 0 && c.Entry != 0 && uint32(c.Guid>>24&0xffffff) == c.Entry && c.Present && c.HealthKnown && c.Health == 0 && c.MaxHealth > 0 && c.AliveKnown && !c.Alive && c.UnitFlagsKnown && c.DynamicFlagsKnown && c.UnitFlags&0x04000000 != 0 && c.DynamicFlags&1 == 0 && c.DynamicFlags&8 != 0
}

func (s *Service) skinningTargetLocked(guid, revision uint64, own *operation) (*worldv1.CreatureState, string) {
	return s.skinningTargetRangeLocked(guid, revision, own, 3)
}

func (s *Service) skinningTargetRangeLocked(guid, revision uint64, own *operation, maxDistance float64) (*worldv1.CreatureState, string) {
	if !s.viewKnown || s.view.self == nil {
		return nil, "actual_own_observations_required"
	}
	spec, _ := recipeForSpell(skinSpell)
	if reason := s.readinessForApproachLocked(spec, own, maxDistance == 40); reason != "" {
		return nil, reason
	}
	c, reason := skinSourceRange(s.view, s.actions, guid, revision, maxDistance)
	if reason != "" {
		return nil, reason
	}
	if !partyClearedSkinningCorpse(c) {
		if c.DynamicFlags&8 == 0 {
			return nil, "actual_corpse_tapped_by_self_or_party_required"
		}
		return nil, "known_skinnable_and_normal_lootable_clear_required"
	}
	m, ok := s.skinTemplates[c.Entry]
	if !ok {
		return nil, "actual_public_creature_template_query_required"
	}
	if m.typeFlags&(0x100|0x200|0x8000) != 0 {
		return nil, "alternate_harvest_profession_not_supported"
	}
	if rank, ok := s.view.skills[skinSkill]; !ok || rank.value < skinRequiredSkill(c.Level) {
		return nil, "observed_pure_skinning_rank_below_creature_requirement"
	}
	if s.skinAttempted[guid] && (own == nil || s.active != own || own.request.TargetGuid != guid) {
		return nil, "corpse_skinning_already_reserved_no_replay"
	}
	if len(s.skinAttempted) >= 256 && !s.skinAttempted[guid] {
		return nil, "finite_skinning_target_receipt_capacity_reached"
	}
	return c, ""
}

func (s *Service) skinningTargetsLocked() []*professionsv1.SkinningTarget {
	if !s.viewKnown || s.view.raw == nil || len(s.view.raw.SkinnableCorpses) > 32 {
		return nil
	}
	out := []*professionsv1.SkinningTarget{}
	for _, c := range s.view.raw.SkinnableCorpses {
		if c == nil {
			continue
		}
		_, reason := s.skinningTargetLocked(c.Guid, c.PositionRevision, nil)
		_, approachReason := s.skinningTargetRangeLocked(c.Guid, c.PositionRevision, nil, 40)
		out = append(out, &professionsv1.SkinningTarget{Creature: proto.Clone(c).(*worldv1.CreatureState), FullyLootedObserved: partyClearedSkinningCorpse(c), Ready: reason == "", Reason: reason, RequiredSkill: skinRequiredSkill(c.Level), Approachable: approachReason == "", ApproachReason: approachReason})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Creature.Guid < out[j].Creature.Guid })
	return out
}

// Fresh dependency reads precede this function. Exact-token renewal precedes
// the LAST world read; only local validation/reservation follows before Send.
func (s *Service) skinWrite(ctx context.Context, op *operation, code uint32, payload []byte) (bool, error) {
	if code != opcode.CMSGCastSpell && code != opcode.CMSGAutostoreLootItem {
		return false, fail("unsupported_skinning_mutation")
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
	if err := s.invoke(call, "buddy-world", "snapshot", &worldv1.SnapshotRequest{CreatureGuid: op.request.TargetGuid}, w); err != nil {
		return false, err
	}
	v, err := validateWorld(w, s.selfGUID)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	valid := s.active == op && !op.windowInvalid && !terminalStatus(op.receipt.Status) && !s.closed && call.Err() == nil && op.ctx.Err() == nil && op.baseline.self.PositionRevision > 0 && v.self.PositionRevision == op.baseline.self.PositionRevision && v.self.MapId == op.baseline.self.MapId && v.inventoryRevision >= s.state.InventoryRevision && v.skillsRevision >= s.state.SkillsRevision && harvestguard.WorldReason(w, s.selfGUID, op.harvestGuard) == ""
	if valid {
		s.view, s.viewKnown = v, true
		s.state.InventoryKnown, s.state.InventoryRevision = true, v.inventoryRevision
		s.state.SkillsKnown, s.state.SkillsRevision = true, v.skillsRevision
		s.state.BackpackKnown, s.state.BackpackRevision = w.BackpackKnown, v.backpackRevision
		valid = s.activeSafetyLocked(op) == "" && v.counts[7005] > 0
		c, reason := skinSource(v, s.actions, op.request.TargetGuid, op.request.TargetPositionRevision)
		valid = valid && reason == "" && c.Entry == op.receipt.TargetEntry
		if code == opcode.CMSGCastSpell {
			_, reason = s.skinningTargetLocked(op.request.TargetGuid, op.request.TargetPositionRevision, op)
			valid = valid && reason == "" && v.inventoryRevision == op.baseline.inventoryRevision && v.skillsRevision == op.baseline.skillsRevision && s.readinessLocked(op.spec, op) == "" && !op.castReserved
		} else {
			valid = valid && v.freeSlots > 0 && op.receipt.OwnGoObserved && op.window != nil
		}
	}
	if !valid {
		s.mu.Unlock()
		return false, fail("final_skinning_body_source_inventory_skill_or_pose_changed")
	}
	if code == opcode.CMSGCastSpell {
		op.castReserved = true
		s.skinAttempted[op.request.TargetGuid] = true
		op.receipt.Status = professionsv1.AttemptStatus_WAITING_CAST
		op.receipt.Reason = "exact_target_skinning_cast_reserved_once"
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

func (s *Service) setSkinReason(op *operation, reason string, status professionsv1.AttemptStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != op || terminalStatus(op.receipt.Status) {
		return
	}
	op.receipt.Status, op.receipt.Reason = status, reason
	s.state.Revision++
}

func (s *Service) runSkin(op *operation) {
	defer close(op.done)
	defer op.cancel()
	defer s.finishSkin(op)
	lease := &actionsv1.InteractionLease{}
	call, cancel := context.WithTimeout(op.ctx, 2*time.Second)
	err := s.invoke(call, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "professions", LeaseMs: 30000, Harvest: true}, lease)
	cancel()
	if err != nil || lease.Token == "" || lease.State == nil || lease.State.InteractionOwner != "professions" || !validHarvestLease(lease.State, s.selfGUID) {
		s.setSkinReason(op, "skinning_interaction_lease_unavailable", professionsv1.AttemptStatus_BLOCKED)
		return
	}
	s.mu.Lock()
	op.token = lease.Token
	op.harvestGuard = append([]uint64(nil), lease.State.HarvestGuardGuids...)
	s.mu.Unlock()
	call, cancel = context.WithTimeout(op.ctx, 2*time.Second)
	err = s.observe(call)
	cancel()
	if err != nil {
		s.setSkinReason(op, "skinning_fresh_dependencies_unavailable", professionsv1.AttemptStatus_BLOCKED)
		return
	}
	s.mu.Lock()
	c, reason := s.skinningTargetLocked(op.request.TargetGuid, op.request.TargetPositionRevision, op)
	if reason == "" {
		op.receipt.TargetEntry = c.Entry
		op.receipt.FullyLootedObserved = true
	}
	s.mu.Unlock()
	if reason != "" {
		s.setSkinReason(op, reason, professionsv1.AttemptStatus_BLOCKED)
		return
	}
	attempted, err := s.skinWrite(op.ctx, op, opcode.CMSGCastSpell, skinCastPacket(byte(op.receipt.CastCount), op.request.TargetGuid))
	s.mu.Lock()
	op.receipt.CastTransportWritten = attempted && err == nil
	s.mu.Unlock()
	if err != nil {
		status := professionsv1.AttemptStatus_BLOCKED
		if attempted {
			status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
		}
		s.setSkinReason(op, "skinning_cast_unconfirmed_no_retry: "+err.Error(), status)
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
				s.setSkinReason(op, "finite_skinning_stopped_no_retry", status)
			}
			return
		case <-ticker.C:
			call, cancel = context.WithTimeout(op.ctx, time.Second)
			err = s.observe(call)
			cancel()
			if err != nil {
				s.setSkinReason(op, "skinning_observations_unavailable_no_retry", professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED)
				return
			}
			if s.stepSkin(op) {
				return
			}
		}
	}
}

func (s *Service) stepSkin(op *operation) bool {
	s.mu.Lock()
	if s.active != op || terminalStatus(op.receipt.Status) {
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
			if _, err := s.skinWrite(op.ctx, op, opcode.CMSGAutostoreLootItem, storeLootPacket(slot.slot)); err != nil {
				s.setSkinReason(op, "skinning_store_unconfirmed_no_retry: "+err.Error(), professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED)
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
		if err := s.skinCleanupWrite(op.ctx, op, opcode.CMSGLootRelease, releaseLootPacket(op.request.TargetGuid)); err != nil {
			s.setSkinReason(op, "skinning_release_unconfirmed_no_retry", professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED)
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
		r.Reason = "exact_target_skinning_GO_loot_slots_push_release_and_newer_allbag_delta_observed"
		s.state.Revision++
		s.mu.Unlock()
		return true
	}
	if len(counts) == 0 && r.LootReleaseObserved {
		r.Status = professionsv1.AttemptStatus_COMPLETED
		r.Reason = "exact_target_skinning_GO_empty_window_and_release_observed_no_materials"
		s.state.Revision++
		s.mu.Unlock()
		return true
	}
	s.mu.Unlock()
	return false
}

func (s *Service) onSkinPacketLocked(op *operation, p *modv1.WorldPacket) {
	if !op.castReserved {
		return
	}
	if op.windowInvalid && (p.Opcode == opcode.SMSGLootRemoved || p.Opcode == opcode.SMSGItemPushResult || p.Opcode == opcode.SMSGLootReleaseResponse) {
		return
	}
	r := op.receipt
	switch p.Opcode {
	case opcode.SMSGSpellStart, opcode.SMSGSpellGo, opcode.SMSGCastFailed, opcode.SMSGSpellFailure:
		e, err := decodeSkinCast(p.Opcode, p.Payload)
		if err != nil {
			s.state.MalformedPackets++
			return
		}
		// Only CAST_FAILED omits its caster on the wire. Every START, GO and
		// SPELL_FAILURE must name this actual actor, including during cleanup.
		if e.spell != skinSpell || uint32(e.count) != r.CastCount || p.Opcode != opcode.SMSGCastFailed && e.caster != s.selfGUID {
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
				r.Reason = fmt.Sprintf("actual_skinning_server_cast_failure_%d", e.failure)
			}
		}
	case opcode.SMSGLootResponse:
		// A slot-only store/release cannot retain authority after a newer
		// foreign, repeated or unparseable current-window observation.
		invalidate := func() {
			op.windowInvalid = true
			r.CleanupUnconfirmed = true
			if !terminalStatus(r.Status) {
				r.Status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
				r.Reason = "skinning_current_loot_window_changed_or_unparseable_no_further_window_writes"
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
				r.Reason = fmt.Sprintf("actual_skinning_loot_error_%d", p.Payload[9])
			}
			return
		}
		w, err := decodeSkinLoot(p.Payload)
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
					r.Reason = "actual_skinning_release_not_confirmed"
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
		// It cannot settle an automatic skinning window that has not arrived.
		if !terminalStatus(r.Status) {
			r.Status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
			r.Reason = fmt.Sprintf("unattributed_skinning_inventory_error_%d", p.Payload[0])
		}
	}
	s.state.Revision++
}

func (s *Service) skinCleanupWrite(ctx context.Context, op *operation, code uint32, p []byte) error {
	call, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := s.renew(call, op); err != nil {
		return err
	}
	s.mu.Lock()
	valid := s.active == op && (code != opcode.CMSGLootRelease || !op.windowInvalid && op.window != nil && op.window.guid == op.request.TargetGuid)
	s.mu.Unlock()
	if !valid {
		return fail("skin_cleanup_operation_replaced")
	}
	_, err := s.session.SendPacket(call, &modv1.SendPacketRequest{Opcode: code, Payload: p})
	if err == nil {
		s.mu.Lock()
		s.state.TransportWrites++
		s.mu.Unlock()
	}
	return err
}

func skinCleanupPending(op *operation) bool {
	r := op.receipt
	return op.windowInvalid || op.window != nil && !r.LootReleaseObserved || op.castReserved && !r.OwnGoObserved && r.FailureCode == 0 || op.castReserved && r.OwnGoObserved && op.window == nil && r.Status != professionsv1.AttemptStatus_SERVER_REJECTED
}

func (s *Service) finishSkin(op *operation) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.mu.Lock()
	if s.active != op {
		s.mu.Unlock()
		return
	}
	token := op.token
	window := op.window != nil && !op.receipt.LootReleaseObserved
	release := window && !op.windowInvalid && !op.releaseReserved
	if release {
		op.releaseReserved = true
	}
	cancelCast := op.castReserved && op.receipt.OwnStartObserved && !op.receipt.OwnGoObserved && op.receipt.FailureCode == 0 && !op.cancelReserved
	if cancelCast {
		op.cancelReserved = true
	}
	s.mu.Unlock()
	if token != "" && release {
		_ = s.skinCleanupWrite(ctx, op, opcode.CMSGLootRelease, releaseLootPacket(op.request.TargetGuid))
	}
	if token != "" && cancelCast {
		err := s.skinCleanupWrite(ctx, op, opcode.CMSGCancelCast, binary.LittleEndian.AppendUint32([]byte{byte(op.receipt.CastCount)}, skinSpell))
		s.mu.Lock()
		op.receipt.CancelTransportWritten = err == nil
		s.mu.Unlock()
	}
	until := time.Now().Add(1100 * time.Millisecond)
	for time.Now().Before(until) {
		s.mu.Lock()
		late := op.window != nil && !op.windowInvalid && !op.receipt.LootReleaseObserved && !op.releaseReserved
		if late {
			op.releaseReserved = true
		}
		pending := skinCleanupPending(op)
		s.mu.Unlock()
		if late && token != "" {
			_ = s.skinCleanupWrite(ctx, op, opcode.CMSGLootRelease, releaseLootPacket(op.request.TargetGuid))
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
	op.receipt.CleanupUnconfirmed = skinCleanupPending(op)
	op.receipt.LeaseReleased = token == ""
	s.mu.Unlock()
	if token != "" {
		released := s.releaseHarvestLease(ctx, op, token)
		s.mu.Lock()
		op.receipt.LeaseReleased = released
		op.receipt.CleanupUnconfirmed = op.receipt.CleanupUnconfirmed || !op.receipt.LeaseReleased
		s.mu.Unlock()
	}
	s.mu.Lock()
	if s.active == op {
		if op.receipt.Status == professionsv1.AttemptStatus_COMPLETED && op.receipt.CleanupUnconfirmed {
			op.receipt.Status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
			op.receipt.Reason = "skinning_receipts_complete_owned_cleanup_unconfirmed"
		}
		s.active = nil
		s.state.Revision++
	}
	s.mu.Unlock()
}
