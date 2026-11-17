package buddyactions

import (
	"context"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const offenseCapability = "offense"

// CastOffense is one stationary cast against the caller-selected current
// attacker. It does not choose a spell or victim or start an attack controller.
func (s *Service) CastOffense(ctx context.Context, r *actionsv1.CastOffenseRequest) (*actionsv1.State, error) {
	target := r.GetTargetGuid()
	kind := target >> 48
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetSpellId() == 0 ||
		target&0xffffff == 0 || kind != 0xf130 && kind != 0xf150 {
		return nil, status.Error(codes.InvalidArgument, "session, exact caster, creature and learned rank required")
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
	s.expireInteractionLocked()
	s.expireExplicitCastLocked()
	if s.closed || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID || s.clientControlLost || s.clientControlRestoring ||
		s.state.CastPending || s.state.Moving || s.state.Traveling || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed ||
		s.state.AutoAttackRequested || s.state.AutoAttacking || s.autoTarget != 0 || s.autoStopping || s.state.InteractionOwner != "" ||
		s.state.Practicing || s.engage.active || s.fargodeep.active || s.crowdControl.active || s.recovery.state.Active ||
		s.now().Before(s.nextAssist) || s.now().Before(s.nextHeal) || s.targetProtection.blocks(target, s.now()) {
		return nil, status.Error(codes.FailedPrecondition, "current body and available attack custody required")
	}
	spell := s.learnedContractsLocked()[r.SpellId]
	if !s.offenseRankSupportedLocked(spell) {
		return nil, status.Error(codes.FailedPrecondition, "exact learned hostile damage rank ready required")
	}

	// Read group first so the world observation used for the cast follows that
	// callback. Exact self-defense remains available without a group.
	var online map[uint64]bool
	if !s.independent {
		party := s.readPartyStateLocked(ctx)
		if party.GetSessionId() == s.sessionID {
			online = s.onlineHealingGroup(party.GetGroup())
		}
	}
	w, err := s.snapshot(ctx, target)
	if err != nil || safeSelfCombat(w, s.selfGUID) != "" || !s.refreshPositionLocked(w) {
		return nil, status.Error(codes.FailedPrecondition, "fresh living caster observation required")
	}
	self, hostile := w.Self, w.RequestedCreature
	if !self.ClassKnown || self.ClassId == 0 || self.MovementFlags&0xff != 0 || self.UnitFlags&0x2000 != 0 ||
		!self.ChannelSpellKnown || self.ChannelSpellId != 0 || !self.ChannelObjectKnown || self.ChannelObjectGuid != 0 ||
		!s.offenseRankReadyLocked(self, spell) {
		return nil, status.Error(codes.FailedPrecondition, "stationary caster with ready resources required")
	}
	if !exactOffenseTarget(w, hostile, target, online) || s.targetProtection.blocks(target, s.now()) {
		return nil, status.Error(codes.FailedPrecondition, "selected attackable creature must be attacking this body or its current party")
	}
	to, known, _ := creaturePosition(hostile)
	if !known || !offenseLineInRange(s.pos, to, spell) {
		return nil, status.Error(codes.FailedPrecondition, "selected creature is outside observed spell range or facing")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}

	now := s.now()
	s.castCount++
	s.reserveSpellCooldownLocked(spell)
	if spell.Category != 0 && spell.CategoryCooldownMs != 0 {
		if s.categoryReady == nil {
			s.categoryReady = make(map[uint32]time.Time)
		}
		s.categoryReady[spell.Category] = now.Add(time.Duration(spell.CategoryCooldownMs) * time.Millisecond)
	}
	s.nextAssist = now.Add(max(1500*time.Millisecond, time.Duration(spell.GcdMs)*time.Millisecond))
	s.nextHeal = s.nextAssist
	s.castUntil = now.Add(max(5*time.Second, time.Duration(spell.CastMs)*time.Millisecond+1500*time.Millisecond))
	s.pendingSpell, s.pendingTarget = spell.ID, target
	s.state.CastPending = true
	s.protectedCastCancelAttempted = false
	s.state.AssistAttempts++
	s.state.LastCastFailure = 0
	s.state.LastCastSpellId, s.state.LastCastTargetGuid = spell.ID, target
	op := s.reserveExplicitCastLocked(token, spell.ID, target, offenseCapability, directOffenseLog(spell))
	if s.sendLocked(ctx, opcode.CMSGCastSpell, spellPacket(s.castCount, spell.ID, target)) {
		op.view.TransportWritten = true
		s.state.Reason = "offense_transport_written_waiting_for_server"
	} else {
		op.view.Phase = "transport_unconfirmed_no_retry"
	}
	return s.stateLocked(), nil
}

func (s *Service) offenseRankSupportedLocked(spell spellcatalog.Spell) bool {
	return s.spellbookKnown && spell.ID != 0 && s.knownSpells[spell.ID] && spell.Capability().Kind == spellcatalog.Damage &&
		spell.TargetMask&^uint32(2) == 0 && !spell.Behind && spell.Facing&^uint32(1) == 0
}

// offenseRankReadyLocked checks only the selected rank and current caster
// mechanics. The caller retains responsibility for custody and target gates.
func (s *Service) offenseRankReadyLocked(self *worldv1.PlayerState, spell spellcatalog.Spell) bool {
	return s.offenseRankSupportedLocked(spell) && self != nil && self.PowerKnown && int32(self.PowerType) == spell.Power &&
		!s.spellOnCooldownLocked(spell) && s.casterRequirementsLocked(spell) &&
		uint64(self.Power) >= spellCost(spell, self)
}

func exactOffenseTarget(w *worldv1.SnapshotResponse, c *worldv1.CreatureState, target uint64, online map[uint64]bool) bool {
	if c == nil || c.Guid != target || !c.Present || !c.MapKnown || c.MapId != w.Self.MapId ||
		!c.AliveKnown || !c.Alive || !c.HealthKnown || c.Health == 0 || c.MaxHealth == 0 || c.Health > c.MaxHealth ||
		!c.UnitFlagsKnown || c.UnitFlags&(0x2|0x80|0x100|0x10000|0x2000000|0x80000000) != 0 ||
		c.UnitFlags_2&1 != 0 || c.DynamicFlags&0x20 != 0 {
		return false
	}
	if ongoingAttack(c, w.Self) {
		return true
	}
	return partyDefenseEligible(w, c, online)
}

func offenseLineInRange(from, to position, spell spellcatalog.Spell) bool {
	d := distance(from, to)
	if !(d >= spell.MinHostile && d <= spell.MaxHostile) {
		return false
	}
	dx, dy := float64(to.x-from.x), float64(to.y-from.y)
	return spell.Facing == 0 || dx == 0 && dy == 0 ||
		math.Abs(math.Remainder(math.Atan2(dy, dx)-float64(from.o), 2*math.Pi)) <= math.Pi/2
}

func directOffenseLog(spell spellcatalog.Spell) bool {
	direct := 0
	for _, effect := range spell.Effects {
		if effect.A != 6 || effect.B != 0 || effect.Chain > 1 || effect.Radius != 0 || effect.Trigger != 0 {
			return false
		}
		if effect.Kind == 2 && effect.Aura == 0 && effect.PeriodMs == 0 {
			direct++
		}
	}
	return direct == 1
}

// The ordinary damage log lacks a cast count. An exact matching GO has
// already bound this single direct effect to its own current cast reservation.
func (s *Service) observeOffenseDamageLocked(target uint64, spell, damage uint32) {
	op := s.pendingExplicitCastLocked()
	if op == nil || op.view.Capability != offenseCapability || !op.view.ServerCastGo ||
		!op.effectLogEligible || op.view.TargetGuid != target || op.view.SpellId != spell {
		return
	}
	if damage == 0 {
		s.endExplicitCastLocked("server_cast_go_effect_unverified")
		return
	}
	op.view.EffectObserved = true
	s.endExplicitCastLocked("confirmed_effect")
	s.state.Reason = "server_damage_effect_confirmed"
}
