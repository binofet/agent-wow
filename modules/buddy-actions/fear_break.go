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
	return item != nil && (item.GetSlot() == 12 || item.GetSlot() == 13) && item.GetSlotKnown() && item.GetMetadataKnown() &&
		item.GetItemGuid()>>48 == 0x4000 && item.GetStackCount() == 1 && gamecatalog.TrinketHasUseSpell(item.GetEntry(), pvpBreakSpell)
}

// Instant self-targeted control breaks need no movement preparation, even
// while fear gives movement to the server. Other live casts/channels retain
// custody. Unknown movement is not falsely reconciled by a self cast.
func (s *Service) controlBreakCasterLocked(w *worldv1.SnapshotResponse) bool {
	self := w.GetSelf()
	return !s.closed && !s.bodyMoving && !s.state.CastPending &&
		self.GetGuid() == s.selfGUID && self.GetPresent() && self.GetAliveKnown() && self.GetAlive() &&
		self.GetHealthKnown() && self.GetHealth() > 0 && self.GetUnitFlagsKnown() && self.GetUnitFlags()&fleeingUnitFlag != 0 && channelstate.Idle(self)
}

// Unavailable/partial aura baselines cannot prove that an in-flight source's
// application ended. A real changed slot or newer complete cache can do so.
func (s *Service) controlBreakRetiredLocked(slot uint32, prior observedBuffAura) bool {
	set := s.buffAuras[s.selfGUID]
	if set == nil {
		return false
	}
	current := set.slots[slot]
	if current.spell != prior.spell || current.caster != prior.caster {
		return current.revision > prior.revision || set.complete && set.fullRevision > prior.revision
	}
	return current.durationKnown && current.remaining(s.now()) == 0
}

// Called on actual aura packets as well as reads, so a removal and a new
// same-caster application between policy samples do not retain stale custody.
func (s *Service) retireControlBreaksLocked() {
	for slot, prior := range s.controlBreakBindings {
		if s.controlBreakRetiredLocked(slot, prior) {
			delete(s.controlBreakBindings, slot)
		}
	}
	if op := s.supportOp; op != nil && op.view.Capability == fearBreakCapability && op.breakAuraRevision != 0 {
		if s.controlBreakRetiredLocked(op.breakAuraSlot, op.breakAura) {
			if s.pendingSpell == op.view.SpellId && s.castCount == op.count {
				s.state.CastPending = false
				s.pendingSpell, s.pendingTarget = 0, 0
			}
			if !op.view.EffectObserved {
				op.view.Phase = "observed_control_clear_effect_unverified"
			}
		}
	}
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
	s.retireControlBreaksLocked()
	for slot, prior := range s.controlBreakBindings {
		if set := s.buffAuras[s.selfGUID]; set != nil {
			current := set.slots[slot]
			if current.revision != 0 && current.spell == prior.spell && current.caster == prior.caster {
				out.AttemptedAuraRevisions = append(out.AttemptedAuraRevisions, current.revision)
			}
		}
	}
	sort.Slice(out.AttemptedAuraRevisions, func(i, j int) bool { return out.AttemptedAuraRevisions[i] < out.AttemptedAuraRevisions[j] })
	caster := len(out.FearAuras) > 0 && s.controlBreakCasterLocked(w)
	for _, r := range s.learnedContractsLocked() {
		if r.ID == pvpBreakSpell || !fearBreakSpell(r) {
			continue
		}
		out.Options = append(out.Options, &actionsv1.ControlBreakOption{SpellId: r.ID, Rank: r.Rank, CooldownMs: r.CooldownMs, CategoryCooldownMs: r.CategoryCooldownMs,
			Ready: caster && (r.GcdMs == 0 || !s.now().Before(s.nextHeal) && !s.now().Before(s.nextAssist)) && !s.spellOnCooldownLocked(r)})
	}
	if w.GetSelfEquipmentKnown() {
		for _, item := range w.SelfEquipment {
			if observedFearBreakItem(item) {
				r, known := s.controlBreakItemSpellLocked(item)
				if !known {
					continue
				}
				out.Options = append(out.Options, &actionsv1.ControlBreakOption{SpellId: pvpBreakSpell, ItemGuid: item.ItemGuid, ItemSlot: item.Slot, CooldownMs: r.CooldownMs, CategoryCooldownMs: r.CategoryCooldownMs,
					Ready: caster && !s.spellOnCooldownLocked(r)})
			}
		}
	}
	sort.Slice(out.Options, func(i, j int) bool {
		a, b := out.Options[i], out.Options[j]
		return a.SpellId < b.SpellId || a.SpellId == b.SpellId && a.ItemGuid < b.ItemGuid
	})
	return out
}

// Item-template cooldowns override DBC values, including the five-minute
// Insignia versus two-minute Medallion. No native source/rank choice is made.
func (s *Service) controlBreakItemSpellLocked(item *worldv1.EquipmentSlotState) (spellcatalog.Spell, bool) {
	r, ok := s.spellData.Lookup(pvpBreakSpell)
	facts, known := gamecatalog.FearBreakItemCooldown(item.Entry)
	if !ok || !known {
		return r, false
	}
	if facts.CooldownMs >= 0 || facts.CategoryCooldownMs >= 0 {
		r.Category = facts.Category
		r.CooldownMs = uint32(max(facts.CooldownMs, 0))
		r.CategoryCooldownMs = uint32(max(facts.CategoryCooldownMs, 0))
	}
	return r, true
}

// Public spell_linked_spell rows attach only these mechanical cooldown
// triggers. Their exact category/duration remains in the pinned spell catalog.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/data/sql/base/db_world/spell_linked_spell.sql
func controlBreakCooldownTrigger(id uint32) uint32 {
	switch id {
	case humanBreakSpell, pvpBreakSpell:
		return 72752
	case 7744:
		return 72757
	}
	return 0
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
		spell, learned = s.controlBreakItemSpellLocked(item)
	} else if r.ItemSlot != 0 || r.SpellId == pvpBreakSpell {
		learned = false
	}
	if !learned || !s.spellbookKnown || !fearBreakSpell(spell) || s.spellOnCooldownLocked(spell) ||
		spell.GcdMs != 0 && (s.now().Before(s.nextHeal) || s.now().Before(s.nextAssist)) {
		return nil, status.Error(codes.FailedPrecondition, "exact ready learned or equipped control break required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if s.controlBreakBindings == nil {
		s.controlBreakBindings = map[uint32]observedBuffAura{}
	}
	s.controlBreakBindings[r.AuraSlot] = aura
	cooldown := s.reserveControlBreakCooldownLocked(spell, w.VisitRevision)
	s.castCount++
	until := s.now().Add(3 * time.Second)
	s.supportOp = &supportAttempt{view: actionsv1.SpellOperation{SpellId: spell.ID, TargetGuid: s.selfGUID, Capability: fearBreakCapability, Phase: "waiting_server"},
		owner: s.selfGUID, count: s.castCount, until: until, policySpellToken: modcb.PolicySpellToken(ctx), breakAuraRevision: aura.revision, breakAuraSlot: r.AuraSlot, breakAura: aura, breakItem: r.ItemGuid, cooldown: cooldown}
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
		s.releaseControlBreakCooldownLocked(cooldown)
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
