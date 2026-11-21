package buddyactions

import (
	"context"
	"encoding/binary"
	"sort"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/channelstate"
	"github.com/agent-wow/agent-wow/pkg/gamecatalog"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	fearBreakCapability = "fear_break"
	fearAura            = 7
	fleeingUnitFlag     = 0x00800000
	pvpBreakSpell       = 42292
	humanBreakSpell     = 59752
)

func (s *Service) isObservedFearLocked(a observedBuffAura) bool {
	if a.spell == 0 || !a.negative || a.durationKnown && a.remaining(s.now()) == 0 {
		return false
	}
	if s.spellData == nil {
		s.spellData = spellcatalog.Default()
	}
	r, ok := s.spellData.Lookup(a.spell)
	if !ok {
		return false
	}
	for _, e := range r.Effects {
		if e.Kind == 6 && e.Aura == fearAura {
			return true
		}
	}
	return false
}

func fearBreakSpell(spell spellcatalog.Spell) bool {
	const purgesImmuneAuras = 0x8000
	if !spell.TimingKnown || spell.CastMs != 0 || spell.Cost != 0 || spell.CostPerLevel != 0 || spell.CostPercent != 0 || spell.PerSecond != 0 ||
		spell.Stances != 0 || spell.ItemClass != -1 || spell.Attrs[0]&0x40 != 0 || spell.Attrs[1]&purgesImmuneAuras == 0 {
		return false
	}
	for _, e := range spell.Effects {
		if e.Kind == 6 && e.A == 1 && e.B == 0 && e.Aura == 77 && (e.Misc == 5 || (spell.ID == humanBreakSpell || spell.ID == pvpBreakSpell) && e.Misc == 1) {
			return true
		}
	}
	return false
}

func observedFearBreakItem(item *worldv1.EquipmentSlotState) bool {
	return item != nil && (item.GetSlot() == 13 || item.GetSlot() == 14) && item.GetSlotKnown() && item.GetMetadataKnown() &&
		item.GetItemGuid()>>48 == 0x4000 && item.GetStackCount() == 1 && gamecatalog.TrinketHasUseSpell(item.GetEntry(), pvpBreakSpell)
}

// Instant self-targeted control breaks need no movement preparation, even
// while fear gives movement to the server. Other live casts/channels retain
// custody. Unknown movement is not falsely reconciled by a self cast.
func (s *Service) controlBreakCasterLocked(w *worldv1.SnapshotResponse) bool {
	self := w.GetSelf()
	return !s.closed && !s.bodyMoving && !s.state.CastPending && s.state.InteractionOwner == "" &&
		self.GetGuid() == s.selfGUID && self.GetPresent() && self.GetAliveKnown() && self.GetAlive() &&
		self.GetHealthKnown() && self.GetHealth() > 0 && self.GetUnitFlagsKnown() && self.GetUnitFlags()&fleeingUnitFlag != 0 && channelstate.Idle(self)
}

// Snapshot all actual options without choosing a spell, rank, trinket or aura.
func (s *Service) controlBreakObservationLocked(w *worldv1.SnapshotResponse) *actionsv1.ControlRecoveryObservation {
	out := &actionsv1.ControlRecoveryObservation{FleeingKnown: w.GetSelf().GetUnitFlagsKnown(), Fleeing: w.GetSelf().GetUnitFlags()&fleeingUnitFlag != 0}
	if set := s.buffAuras[s.selfGUID]; set != nil {
		for slot, a := range set.slots {
			if s.isObservedFearLocked(a) {
				out.FearAuras = append(out.FearAuras, &actionsv1.ObservedAura{Slot: uint32(slot), SpellId: a.spell, Revision: a.revision, Negative: true, MetadataKnown: true, DurationKnown: a.durationKnown, RemainingMs: a.remaining(s.now())})
			}
		}
	}
	// Revisions also advance on a same-application refresh. A changed duration
	// cannot retire an unknown write; require observed removal/replacement of
	// that exact slot/spell/caster before admitting a new application there.
	for slot, prior := range s.controlBreakBindings {
		current := observedBuffAura{}
		if set := s.buffAuras[s.selfGUID]; set != nil {
			current = set.slots[slot]
		}
		if !s.isObservedFearLocked(current) || current.spell != prior.spell || current.caster != prior.caster {
			delete(s.controlBreakBindings, slot)
		} else {
			out.AttemptedAuraRevisions = append(out.AttemptedAuraRevisions, current.revision)
		}
	}
	sort.Slice(out.AttemptedAuraRevisions, func(i, j int) bool { return out.AttemptedAuraRevisions[i] < out.AttemptedAuraRevisions[j] })
	if op := s.supportOp; op != nil && op.view.Capability == fearBreakCapability && op.breakAuraRevision != 0 {
		current := observedBuffAura{}
		if set := s.buffAuras[s.selfGUID]; set != nil {
			current = set.slots[op.breakAuraSlot]
		}
		if !s.isObservedFearLocked(current) || current.spell != op.breakAura.spell || current.caster != op.breakAura.caster {
			if s.pendingSpell == op.view.SpellId && s.castCount == op.count {
				s.state.CastPending = false
				s.pendingSpell, s.pendingTarget = 0, 0
			}
			if !op.view.EffectObserved {
				op.view.Phase = "observed_control_clear_effect_unverified"
			}
		}
	}
	caster := len(out.FearAuras) > 0 && s.controlBreakCasterLocked(w)
	for _, r := range s.learnedContractsLocked() {
		if r.ID == pvpBreakSpell || !fearBreakSpell(r) {
			continue
		}
		out.Options = append(out.Options, &actionsv1.ControlBreakOption{SpellId: r.ID, Rank: r.Rank, CooldownMs: r.CooldownMs, CategoryCooldownMs: r.CategoryCooldownMs,
			Ready: caster && (r.GcdMs == 0 || !s.now().Before(s.nextHeal) && !s.now().Before(s.nextAssist)) && !s.spellOnCooldownLocked(r) && (r.Category == 0 || !s.now().Before(s.fearBreakReady))})
	}
	if w.GetSelfEquipmentKnown() {
		for _, item := range w.SelfEquipment {
			if observedFearBreakItem(item) {
				out.Options = append(out.Options, &actionsv1.ControlBreakOption{SpellId: pvpBreakSpell, ItemGuid: item.ItemGuid, ItemSlot: item.Slot, CooldownMs: 120000,
					Ready: caster && !s.now().Before(s.fearBreakReady)})
			}
		}
	}
	sort.Slice(out.Options, func(i, j int) bool {
		a, b := out.Options[i], out.Options[j]
		return a.SpellId < b.SpellId || a.SpellId == b.SpellId && a.ItemGuid < b.ItemGuid
	})
	return out
}

// BreakControl writes only the caller-selected rank/equipped source against an
// exact observed fear application. Lua owns usefulness, ordering and retries.
func (s *Service) BreakControl(ctx context.Context, r *actionsv1.BreakControlRequest) (*actionsv1.State, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetSpellId() == 0 || r.GetAuraSlot() >= 255 || r.GetAuraRevision() == 0 || r.GetAuraSpellId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "current body, exact spell/source and observed fear slot/revision/spell required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	token, supplied := modcb.IncomingPolicySpell(ctx)
	if supplied && token == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid policy spell token")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.session == nil || s.sessionID != r.SessionId || s.selfGUID != r.SelfGuid {
		return nil, status.Error(codes.FailedPrecondition, "current connected body required")
	}
	s.expireInteractionLocked()
	w, err := s.snapshot(ctx)
	if err != nil || !s.controlBreakCasterLocked(w) {
		return nil, status.Error(codes.FailedPrecondition, "current living feared caster with idle cast/channel custody required")
	}
	if supplied && !s.admitsPolicyEncounterLocked(ctx, w) {
		return nil, status.Error(codes.FailedPrecondition, "active policy lease for current visit required")
	}
	set := s.buffAuras[s.selfGUID]
	if set == nil {
		return nil, status.Error(codes.FailedPrecondition, "current observed fear required")
	}
	aura := set.slots[r.AuraSlot]
	if aura.revision != r.AuraRevision || aura.spell != r.AuraSpellId || !s.isObservedFearLocked(aura) {
		return nil, status.Error(codes.FailedPrecondition, "current unconsumed fear application required")
	}
	if prior, ok := s.controlBreakBindings[r.AuraSlot]; ok && prior.spell == aura.spell && prior.caster == aura.caster {
		return nil, status.Error(codes.FailedPrecondition, "current unconsumed fear application required")
	}
	spell, learned := s.learnedContractsLocked()[r.SpellId]
	var item *worldv1.EquipmentSlotState
	if r.ItemGuid != 0 {
		if r.SpellId != pvpBreakSpell || !w.SelfEquipmentKnown {
			return nil, status.Error(codes.FailedPrecondition, "exact observed equipped fear-break source required")
		}
		for _, source := range w.SelfEquipment {
			if source.GetItemGuid() == r.ItemGuid && source.GetSlot() == r.ItemSlot && observedFearBreakItem(source) {
				item = source
			}
		}
		if item == nil {
			return nil, status.Error(codes.FailedPrecondition, "exact observed equipped fear-break source required")
		}
		spell, learned = s.spellData.Lookup(r.SpellId)
	} else if r.ItemSlot != 0 || r.SpellId == pvpBreakSpell {
		learned = false
	}
	if !learned || !s.spellbookKnown || !fearBreakSpell(spell) || s.spellOnCooldownLocked(spell) ||
		spell.GcdMs != 0 && (s.now().Before(s.nextHeal) || s.now().Before(s.nextAssist)) ||
		(item != nil || spell.Category != 0) && s.now().Before(s.fearBreakReady) {
		return nil, status.Error(codes.FailedPrecondition, "exact ready learned or equipped control break required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if s.controlBreakBindings == nil {
		s.controlBreakBindings = map[uint32]observedBuffAura{}
	}
	priorReady, priorSpell := s.fearBreakReady, s.spellReady[spell.ID]
	s.controlBreakBindings[r.AuraSlot] = aura
	s.reserveSpellCooldownLocked(spell)
	if item != nil || spell.ID == humanBreakSpell {
		s.fearBreakReady = s.now().Add(2 * time.Minute)
	} else if spell.CategoryCooldownMs > 0 {
		s.fearBreakReady = s.now().Add(time.Duration(spell.CategoryCooldownMs) * time.Millisecond)
	}
	s.castCount++
	until := s.now().Add(3 * time.Second)
	s.supportOp = &supportAttempt{view: actionsv1.SpellOperation{SpellId: spell.ID, TargetGuid: s.selfGUID, Capability: fearBreakCapability, Phase: "waiting_server"},
		owner: s.selfGUID, count: s.castCount, until: until, policySpellToken: modcb.PolicySpellToken(ctx), breakAuraRevision: aura.revision, breakAuraSlot: r.AuraSlot, breakAura: aura, breakItem: r.ItemGuid}
	s.state.CastPending = true
	s.pendingSpell, s.pendingTarget = spell.ID, s.selfGUID
	s.castUntil = until
	s.state.LastCastSpellId, s.state.LastCastTargetGuid, s.state.LastCastFailure = spell.ID, s.selfGUID, 0
	wire, payload := uint32(opcode.CMSGCastSpell), spellPacket(s.castCount, spell.ID, s.selfGUID)
	if item != nil {
		wire, payload = opcode.CMSGUseItem, fearBreakItemPacket(item, s.castCount, spell.ID)
	}
	err = s.sendPacketLocked(modcb.ForwardPolicySpell(ctx), wire, payload)
	if modcb.PacketWasNotDispatched(err) {
		delete(s.controlBreakBindings, r.AuraSlot)
		s.fearBreakReady, s.spellReady[spell.ID] = priorReady, priorSpell
		s.state.CastPending = false
		s.pendingSpell, s.pendingTarget = 0, 0
		s.supportOp.view.Phase = "not_dispatched"
		return nil, err
	}
	ready := s.now().Add(time.Duration(spell.GcdMs) * time.Millisecond)
	if s.nextHeal.Before(ready) {
		s.nextHeal = ready
	}
	if s.nextAssist.Before(ready) {
		s.nextAssist = ready
	}
	if err != nil {
		s.supportOp.view.Phase = "transport_unconfirmed_no_retry"
		return s.stateLocked(), err
	}
	s.supportOp.view.TransportWritten = true
	s.state.Reason = "fear_break_waiting_for_server"
	return s.stateLocked(), nil
}

func fearBreakItemPacket(item *worldv1.EquipmentSlotState, count byte, spell uint32) []byte {
	p := make([]byte, 24)
	p[0], p[1], p[2] = 255, byte(item.Slot), count
	binary.LittleEndian.PutUint32(p[3:], spell)
	binary.LittleEndian.PutUint64(p[7:], item.ItemGuid)
	return p
}
