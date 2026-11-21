package buddyinventory

import (
	"context"
	"encoding/binary"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/channelstate"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// This bounds one admission/write, not resting duration or a retry policy.
const consumableWriteLimit = 3 * time.Second

type consumableAttempt struct {
	view               *inventoryv1.ConsumableUse
	baselineMana       uint32
	baselineAurasKnown bool
	count              byte
}

func consumableBodySafe(w *worldv1.SnapshotResponse) bool {
	p := w.GetSelf()
	const unsafe = uint32(1 | 4 | 0x40000 | 0x80000 | 0x100000 | 0x400000 | 0x800000 | 0x1000000 | 0x2000000 | 0x8000000)
	return p != nil && p.AliveKnown && p.Alive && p.HealthKnown && p.Health != 0 && p.UnitFlagsKnown && p.UnitFlags&unsafe == 0 &&
		p.MapKnown && p.PositionKnown && finite(p.X) && finite(p.Y) && finite(p.Z) && p.MovementFlagsKnown && p.MovementFlags&0xff == 0 &&
		movementflags.GroundCompatible(p.MovementFlags, p.ExtraMovementFlags) && channelstate.Idle(p)
}

func consumableActionSafe(a *actionsv1.State, sid string, self uint64) bool {
	return a != nil && a.SessionId == sid && a.SelfGuid == self && !a.Moving && !a.Traveling && !a.MotionUnconfirmed &&
		!a.CastPending && !a.AutoAttacking && !a.AutoAttackRequested && !a.AutoAttackUnconfirmed && !a.Practicing
}

func (s *Service) consumableReadyLocked() bool {
	return s.known && !s.closed && s.active == nil && consumableBodySafe(s.view.w) &&
		consumableActionSafe(s.actions, s.state.SessionId, s.selfGUID) && s.actions.InteractionOwner == "" &&
		settledTrade(s.trade) && s.loot.GetPendingCorpseGuid() == 0 && s.loot.GetPendingGameObjectGuid() == 0 &&
		!s.prof.GetActive() && !s.prof.GetClosing()
}

// Drink and EatBuffFood preserve their deployed request/response prefixes.
// Empty-source compatibility requests are queued by the host into its current
// Lua policy. This module executes only an exact caller-selected owned item.
func (s *Service) Drink(ctx context.Context, r *inventoryv1.DrinkRequest) (*inventoryv1.DrinkReceipt, error) {
	op, mana, err := s.useConsumable(ctx, "drink", r.GetSessionId(), r.GetSelfGuid(), r.GetSource(), r.GetSpellId(), r.GetExpectedInventoryRevision(), r.GetRequestId())
	if err != nil {
		return nil, err
	}
	return &inventoryv1.DrinkReceipt{SessionId: r.SessionId, SelfGuid: r.SelfGuid, ItemGuid: op.Source.ItemGuid,
		ItemEntry: op.Source.Entry, SpellId: op.SpellId, StackCountBefore: op.Source.StackCount, TransportWritten: op.TransportWritten,
		Reason: op.Phase, BaselineMana: mana, BaselineInventoryRevision: op.BaselineInventoryRevision, RequestId: op.RequestId, Operation: op}, nil
}

func (s *Service) EatBuffFood(ctx context.Context, r *inventoryv1.EatBuffFoodRequest) (*inventoryv1.EatBuffFoodReceipt, error) {
	op, _, err := s.useConsumable(ctx, "eat_buff_food", r.GetSessionId(), r.GetSelfGuid(), r.GetSource(), r.GetSpellId(), r.GetExpectedInventoryRevision(), r.GetRequestId())
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	delay := s.metadata[op.Source.Entry].foodBuff(s.view.w.GetSelf()).waitMS
	s.mu.Unlock()
	return &inventoryv1.EatBuffFoodReceipt{SessionId: r.SessionId, SelfGuid: r.SelfGuid, ItemEntry: op.Source.Entry,
		SpellId: op.SpellId, FoodAuraSpellId: op.AuraSpellId, TransportWritten: op.TransportWritten, TimeUntilBuffMs: delay,
		RequestId: op.RequestId, Operation: op, Reason: op.Phase}, nil
}

func (s *Service) useConsumable(ctx context.Context, kind, sid string, self uint64, item *inventoryv1.ItemRef, spell uint32, revision uint64, id string) (*inventoryv1.ConsumableUse, uint32, error) {
	if sid == "" || self == 0 {
		return nil, 0, status.Error(codes.InvalidArgument, "current session and body required")
	}
	if item == nil {
		return nil, 0, fail("consumable_request_requires_current_Lua_policy_or_exact_selected_item")
	}
	if !requestPattern.MatchString(id) || spell == 0 || revision == 0 || itemUsePayload(item, spell, 0) == nil {
		return nil, 0, status.Error(codes.InvalidArgument, "exact request ID, owned source, use spell and inventory revision required")
	}
	ctx, cancel := context.WithTimeout(ctx, consumableWriteLimit)
	defer cancel()
	s.mu.Lock()
	if s.closed || sid != s.state.SessionId || self != s.selfGUID {
		s.mu.Unlock()
		return nil, 0, fail("current_connected_consumable_body_required")
	}
	if prior := s.consumableUses[id]; prior != nil {
		if prior.view.Kind != kind || prior.view.SpellId != spell || prior.view.BaselineInventoryRevision != revision || !proto.Equal(prior.view.Source, item) {
			s.mu.Unlock()
			return nil, 0, fail("consumable_request_id_already_bound")
		}
		result, mana := proto.Clone(prior.view).(*inventoryv1.ConsumableUse), prior.baselineMana
		s.mu.Unlock()
		return result, mana, nil // Never repeat even an ambiguous transport write.
	}
	if s.active != nil || s.consumableBusy {
		s.mu.Unlock()
		return nil, 0, fail("inventory_operation_active")
	}
	s.consumableBusy = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.consumableBusy = false; s.mu.Unlock() }()
	if err := s.observe(ctx, nil); err != nil {
		return nil, 0, fail("current_owned_consumable_inventory_required")
	}
	s.mu.Lock()
	_, owned := s.view.source(item)
	m := s.metadata[item.Entry]
	aura := spell
	valid := false
	if kind == "drink" {
		use, _ := m.manaDrink(s.view.w.GetSelf())
		valid = use == spell
	} else {
		food := m.foodBuff(s.view.w.GetSelf())
		aura, valid = food.aura, food.useSpell == spell && food.aura != 0
	}
	if !s.consumableReadyLocked() || s.view.w.InventoryRevision != revision || !owned || !valid {
		s.mu.Unlock()
		return nil, 0, fail("exact_usable_consumable_and_ready_stationary_body_required")
	}
	baseline := s.view
	baselineAura := s.state.GetConsumableContext().GetSelfAuras().GetRevision()
	baselineAurasKnown := s.consumableAurasAvailable
	s.mu.Unlock()
	lease := &actionsv1.InteractionLease{}
	acquired := s.now()
	if s.invoke(ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "trade", LeaseMs: uint32(consumableWriteLimit.Milliseconds())}, lease) != nil || lease.Token == "" {
		return nil, 0, fail("consumable_interaction_unavailable")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = s.invoke(cleanup, "buddy-actions", "release_interaction", &actionsv1.InteractionRelease{Owner: "trade", Token: lease.Token}, &actionsv1.State{})
	}()
	if !consumableActionSafe(lease.State, sid, self) || lease.State.InteractionOwner != "trade" || lease.State.InteractionLeaseRemainingMs == 0 {
		return nil, 0, fail("consumable_motion_stop_unconfirmed")
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	w := &worldv1.SnapshotResponse{}
	if s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, w) != nil {
		return nil, 0, fail("fresh_owned_consumable_inventory_required")
	}
	current, err := validateWorld(w, self)
	if err != nil || !consumableBodySafe(w) || w.InventoryRevision != revision || w.Self.MapId != baseline.w.Self.MapId || w.Self.PositionRevision != baseline.w.Self.PositionRevision {
		return nil, 0, fail("consumable_body_or_inventory_changed")
	}
	if _, ok := current.source(item); !ok {
		return nil, 0, fail("consumable_source_slot_or_stack_changed")
	}
	s.mu.Lock()
	if s.closed || ctx.Err() != nil || s.now().Sub(acquired) >= time.Duration(lease.State.InteractionLeaseRemainingMs)*time.Millisecond {
		s.mu.Unlock()
		return nil, 0, fail("consumable_admission_expired_before_write")
	}
	s.itemUseCount++
	op := &consumableAttempt{view: &inventoryv1.ConsumableUse{RequestId: id, Kind: kind, Source: proto.Clone(item).(*inventoryv1.ItemRef), SpellId: spell,
		AuraSpellId: aura, BaselineInventoryRevision: revision, BaselineAuraRevision: baselineAura, TransportAttempted: true, Phase: "transport_unconfirmed_no_retry"},
		baselineMana: w.Self.Power, baselineAurasKnown: baselineAurasKnown, count: s.itemUseCount}
	if s.consumableUses == nil {
		s.consumableUses = map[string]*consumableAttempt{}
	}
	s.consumableUses[id], s.lastConsumable = op, op
	s.state.LastConsumableUse = op.view
	s.state.Revision++
	s.mu.Unlock()
	_, sendErr := s.session.SendPacket(modcb.ForwardPolicySpell(ctx), &modv1.SendPacketRequest{Opcode: opcode.CMSGUseItem, Payload: itemUsePayload(item, spell, op.count)})
	s.mu.Lock()
	if sendErr == nil {
		op.view.TransportWritten, op.view.Phase = true, "transport_written_awaiting_observation"
		s.state.TransportWrites++
		s.state.Revision++
	}
	result := proto.Clone(op.view).(*inventoryv1.ConsumableUse)
	s.mu.Unlock()
	return result, op.baselineMana, nil
}

func (s *Service) consumableEvidenceLocked(v view, auras *actionsv1.PlayerAuras) {
	op := s.lastConsumable
	if op == nil || !op.view.TransportAttempted || op.view.ServerRejected {
		return
	}
	r := op.view
	r.ObservedInventoryRevision = v.w.InventoryRevision
	if v.w.InventoryRevision > r.BaselineInventoryRevision {
		found := false
		for _, slot := range v.slots {
			if slot.ItemGuid == r.Source.ItemGuid {
				found = true
				if slot.MetadataKnown && slot.Entry == r.Source.Entry && r.Source.StackCount > 1 && slot.StackCount == r.Source.StackCount-1 {
					r.InventoryConfirmed = true
				}
			}
		}
		if r.Source.StackCount == 1 && !found {
			r.InventoryConfirmed = true
		}
	}
	if op.baselineAurasKnown && auras != nil && auras.TargetGuid == s.selfGUID {
		for _, aura := range auras.Auras {
			if aura.SpellId == r.AuraSpellId && aura.CasterGuid == s.selfGUID && aura.Positive && aura.Revision > r.BaselineAuraRevision && (!aura.DurationKnown || aura.RemainingMs > 0) {
				r.AuraObserved = true
			}
		}
	}
	if r.InventoryConfirmed && r.AuraObserved {
		r.Phase = "confirmed_consumption_and_aura"
	}
}

func (s *Service) observeConsumableErrorLocked(p *modv1.WorldPacket) {
	op := s.lastConsumable
	if op == nil || !op.view.TransportAttempted || op.view.InventoryConfirmed || op.view.ServerRejected {
		return
	}
	var code uint32
	matched := false
	if p.Opcode == opcode.SMSGInventoryChangeFailure {
		failure, err := parseInventoryChangeFailure(p.Payload)
		if err != nil {
			s.state.MalformedPackets++
			return
		}
		code, matched = uint32(failure.code), failure.itemGUID1 == op.view.Source.ItemGuid || failure.itemGUID2 == op.view.Source.ItemGuid
	}
	if p.Opcode == opcode.SMSGCastFailed && len(p.Payload) == 6 && p.Payload[0] == op.count && binary.LittleEndian.Uint32(p.Payload[1:]) == op.view.SpellId {
		op.view.ServerRejected, op.view.ServerError, op.view.Phase = true, uint32(p.Payload[5]), "server_rejected"
		s.state.Revision++
		return
	}
	if matched && code != 0 {
		op.view.ServerRejected, op.view.ServerError, op.view.Phase = true, code, "server_rejected"
		s.state.Revision++
	}
}
