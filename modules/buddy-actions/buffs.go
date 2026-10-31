package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"sort"
	"time"
)

type buffKey struct {
	target uint64
	family uint32
}
type buffBinding struct {
	blocked             bool
	bouncedSpell        uint32
	bouncedFullRevision uint64
	until               time.Time
}
type buffAttempt struct {
	view     actionsv1.BuffOperation
	key      buffKey
	baseline uint64
	count    byte
	until    time.Time
}

func (s *Service) Buffs(_ context.Context, r *actionsv1.ToggleRequest) (*actionsv1.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.state.Buffing = r != nil && r.Enabled
		if s.state.Buffing {
			s.buffUntil = s.now().Add(lease)
			s.state.BuffReason = "enabled_waiting_for_safe_known_spell"
		} else {
			s.state.BuffReason = "explicitly_disabled"
		}
	}
	return s.stateLocked(), nil
}
func (s *Service) buffUnconfirmedLocked(reason string) {
	op := s.buffOp
	if op == nil || op.view.Phase != "waiting_server" {
		return
	}
	op.view.Phase = "outcome_unconfirmed"
	s.state.BuffReason = reason
	if s.pendingSpell == op.view.SpellId && s.pendingTarget == op.view.TargetGuid && s.castCount == op.count {
		s.pendingSpell, s.pendingTarget = 0, 0
		s.state.CastPending = false
	}
	// The binding was reserved before SendPacket and remains blocked across
	// toggles. Other cast/DE/item uncertainty is never cleared here.
}
func (s *Service) buffTimeoutLocked() {
	if s.state.Buffing && !s.now().Before(s.buffUntil) {
		s.state.Buffing = false
		s.state.BuffReason = "buff_lease_expired"
	}
	if s.buffOp != nil && s.buffOp.view.Phase == "waiting_server" && !s.now().Before(s.buffOp.until) {
		s.buffUnconfirmedLocked("buff_result_timeout_no_retry")
	}
}
func (s *Service) confirmBuffLocked() {
	op := s.buffOp
	if op == nil || op.view.Phase != "waiting_server" || !op.view.ServerCastGo || !op.view.AuraConfirmed {
		return
	}
	op.view.Phase = "confirmed"
	s.state.BuffsConfirmed++
	s.state.BuffReason = "server_cast_and_fresh_own_aura_confirmed"
	s.buffBindings[op.key].blocked = false
	s.buffBindings[op.key].until = s.now().Add(5 * time.Second)
}
func (s *Service) buffRejectedLocked() {
	op := s.buffOp
	op.view.Phase = "server_rejected"
	s.state.BuffReason = "buff_server_rejected_cooldown"
	s.buffBindings[op.key].blocked = false
	s.buffBindings[op.key].until = s.now().Add(30 * time.Second)
	if s.state.LastCastFailure == 9 { // SPELL_FAILED_AURA_BOUNCED: stronger existing effect.
		b := s.buffBindings[op.key]
		b.blocked = true
		b.bouncedSpell = op.view.SpellId
		if set := s.buffAuras[op.key.target]; set != nil {
			b.bouncedFullRevision = set.fullRevision
		}
	}
	s.state.CastPending = false
	s.pendingSpell, s.pendingTarget = 0, 0
}
func (s *Service) buffCastPacketLocked(code uint32, p []byte) (bool, error) {
	op := s.buffOp
	if op == nil || op.view.Phase != "waiting_server" || s.pendingSpell != op.view.SpellId || s.pendingTarget != op.view.TargetGuid || s.castCount != op.count {
		return false, nil
	}
	bad := errors.New("malformed correlated buff cast event")
	switch code {
	case opcode.SMSGCastFailed:
		if len(p) < 6 {
			return false, nil
		}
		if p[0] != op.count || binary.LittleEndian.Uint32(p[1:]) != op.view.SpellId {
			return false, nil
		}
		if len(p) != 6 {
			return true, bad
		}
		s.state.LastCastFailure = uint32(p[5])
		s.buffRejectedLocked()
		return true, nil
	case opcode.SMSGSpellFailure, opcode.SMSGSpellFailedOther:
		guid, n, err := readGUID(p)
		if err != nil || len(p) < n+6 {
			return false, nil
		}
		if guid != s.selfGUID || p[n] != op.count || binary.LittleEndian.Uint32(p[n+1:]) != op.view.SpellId {
			return false, nil
		}
		if len(p) != n+6 {
			return true, bad
		}
		s.state.LastCastFailure = uint32(p[n+5])
		s.buffRejectedLocked()
		return true, nil
	case opcode.SMSGSpellStart, opcode.SMSGSpellGo:
		guid, count, spell, off, err := castEvent(p)
		if err != nil {
			return false, nil
		}
		if guid != s.selfGUID || count != op.count || spell != op.view.SpellId {
			return false, nil
		}
		if !validCastTargetBody(p, off, code == opcode.SMSGSpellGo, op.view.TargetGuid, s.implicitSelfSpellLocked(op.view.SpellId, op.view.TargetGuid)) {
			return true, bad
		}
		if code == opcode.SMSGSpellGo {
			op.view.ServerCastGo = true
			s.state.CastPending = false
			s.pendingSpell, s.pendingTarget = 0, 0
			s.confirmBuffLocked()
		}
		return true, nil
	}
	return false, nil
}
func (s *Service) hasBuffLocked(target uint64, r buffSpell) bool {
	set := s.buffAuras[target]
	if set == nil {
		return false
	}
	for _, a := range set.slots {
		if a.spell == 0 || !a.positive || a.durationKnown && a.remaining(s.now()) == 0 {
			continue
		}
		known := buffMetadata(a.spell)
		if known.family == r.family && known.rank >= r.rank || strongerArmorAura(a.spell, r.family) {
			return true
		}
	}
	return false
}
func buffHealthy(p *worldv1.PlayerState, mapID uint32, percent uint64) bool {
	return safeHealingPlayer(p, mapID) && p.HealthKnown && p.Health > 0 && p.MaxHealth > 0 && p.Health <= p.MaxHealth && uint64(p.Health)*100 >= uint64(p.MaxHealth)*percent && p.UnitFlags&0x80000 == 0
}
func (s *Service) buffContextLocked(w *worldv1.SnapshotResponse, members map[uint64]bool) bool {
	if safeSelfCombat(w, s.selfGUID) != "" || !buffHealthy(w.Self, w.Self.MapId, 95) || !w.Self.ClassKnown || w.Self.UnitFlags&0x2000 != 0 || w.Self.MovementFlags&0xff != 0 || !w.Self.PowerKnown || w.Self.PowerType != 0 || w.Self.MaxPower == 0 || w.Self.Power > w.Self.MaxPower {
		return false
	}
	if s.now().Before(s.nextHeal) || s.now().Before(s.nextAssist) {
		return false
	}
	if s.closed || !s.state.Buffing || !s.now().Before(s.buffUntil) || !s.spellbookKnown || s.state.InteractionOwner != "" || s.recovery.state.Active || s.state.Practicing || s.fargodeep.active || s.state.Traveling || s.state.Moving || s.state.CastPending || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed || s.state.AutoAttacking || s.state.AutoAttackRequested || s.autoTarget != 0 {
		return false
	}
	for guid, online := range members {
		if !online || guid == s.selfGUID {
			continue
		}
		p := observedHealingPlayer(w, guid)
		if p != nil && p.Present && p.MapKnown && p.MapId == w.Self.MapId && (!buffHealthy(p, w.Self.MapId, 90)) {
			return false
		}
	}
	return true
}
func (s *Service) buffTargetLocked(w *worldv1.SnapshotResponse, members map[uint64]bool, r buffSpell, guid uint64) bool {
	if !s.knownSpells[r.id] || guid != s.selfGUID && (!r.party || !members[guid] || s.independent) {
		return false
	}
	target := observedHealingPlayer(w, guid)
	if !buffHealthy(target, w.Self.MapId, 90) || guid != s.selfGUID && (target.PlayerFlags&2 != 0 || distance(s.pos, playerPosition(target)) > 28) {
		return false
	}
	// Intellect is useful for observed mana users; no class inference is needed.
	if intellectBuff(r.id) && (!target.PowerKnown || target.PowerType != 0) {
		return false
	}
	if s.hasBuffLocked(guid, r) {
		return false
	}
	for id, known := range s.knownSpells {
		if known && strongerArmorAura(id, r.family) {
			return false
		}
	}
	if b := s.buffBindings[buffKey{guid, r.family}]; b != nil {
		if s.now().Before(b.until) {
			return false
		}
		if b.blocked {
			if b.bouncedSpell == 0 {
				return false
			} // Ambiguous writes never rearm here.
			set := s.buffAuras[guid]
			if r.id == b.bouncedSpell && (set == nil || set.fullRevision <= b.bouncedFullRevision) {
				return false
			}
		}
	}
	budget := manaBudget(w.Self, uint64(r.manaPercent))
	reserve := (uint64(w.Self.MaxPower)*35 + 99) / 100
	return budget > 0 && uint64(w.Self.Power) >= budget+reserve
}
func (s *Service) buffTickLocked(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	s.buffTimeoutLocked()
	if !s.state.Buffing {
		return false
	}
	s.pruneBuffAurasLocked(w)
	if s.buffOp != nil && s.buffOp.view.Phase == "waiting_server" || s.now().Before(s.nextBuff) {
		return false
	}
	members := map[uint64]bool{s.selfGUID: true}
	if !s.independent {
		for g, on := range s.healingPartyLocked(ctx) {
			members[g] = on
		}
	}
	if !s.buffContextLocked(w, members) || !s.refreshPositionLocked(w) {
		s.state.BuffReason = "buff_waiting_for_healthy_idle_out_of_combat_context"
		return false
	}
	targets := []uint64{s.selfGUID}
	for guid, on := range members {
		if guid != s.selfGUID && on {
			targets = append(targets, guid)
		}
	}
	sort.Slice(targets[1:], func(i, j int) bool { return targets[i+1] < targets[j+1] })
	families := []uint32{}
	seenFamilies := map[uint32]bool{}
	for id, known := range s.knownSpells {
		if known {
			r := buffMetadata(id)
			if r.id != 0 && !seenFamilies[r.family] {
				seenFamilies[r.family] = true
				families = append(families, r.family)
			}
		}
	}
	sort.Slice(families, func(i, j int) bool {
		a, b := buffMetadata(families[i]), buffMetadata(families[j])
		if a.party != b.party {
			return !a.party
		}
		return families[i] < families[j]
	})
	for _, family := range families {
		r := highestBuff(s.knownSpells, family)
		if r.id == 0 {
			continue
		}
		for _, target := range targets {
			if !s.buffTargetLocked(w, members, r, target) {
				continue
			}
			// Re-read membership first, then the actual own/target observations. No
			// callback remains between this final guard and the reserved normal cast.
			freshMembers := map[uint64]bool{s.selfGUID: true}
			if !s.independent {
				for g, on := range s.healingPartyLocked(ctx) {
					freshMembers[g] = on
				}
			}
			fresh, err := s.snapshot(ctx)
			if err != nil || fresh.Self == nil || fresh.Self.ClassId != w.Self.ClassId || !s.buffContextLocked(fresh, freshMembers) || !s.refreshPositionLocked(fresh) || !s.buffTargetLocked(fresh, freshMembers, r, target) {
				s.state.BuffReason = "buff_context_changed_before_cast"
				return false
			}
			if s.buffBindings == nil {
				s.buffBindings = make(map[buffKey]*buffBinding)
			}
			key := buffKey{target, r.family}
			if len(s.buffBindings) >= 512 && s.buffBindings[key] == nil {
				s.state.BuffReason = "buff_binding_capacity_no_retry"
				return false
			}
			s.buffBindings[key] = &buffBinding{blocked: true}
			s.buffSequence++
			s.castCount++
			s.buffOp = &buffAttempt{view: actionsv1.BuffOperation{Sequence: s.buffSequence, SpellId: r.id, TargetGuid: target, Phase: "waiting_server"}, key: key, baseline: s.buffAuraRevision, count: s.castCount, until: s.now().Add(8 * time.Second)}
			s.state.BuffAttempts++
			gcd := s.now().Add(1500 * time.Millisecond)
			if s.nextHeal.Before(gcd) {
				s.nextHeal = gcd
			}
			if s.nextAssist.Before(gcd) {
				s.nextAssist = gcd
			}
			s.nextBuff = s.now().Add(2 * time.Second)
			s.state.CastPending = true
			s.pendingSpell, s.pendingTarget = r.id, target
			s.castUntil = s.buffOp.until
			s.state.LastCastSpellId, s.state.LastCastTargetGuid = r.id, target
			s.state.LastCastFailure = 0
			if !s.sendLocked(ctx, opcode.CMSGCastSpell, spellPacket(s.castCount, r.id, target)) {
				s.buffUnconfirmedLocked("buff_transport_outcome_unconfirmed_no_retry")
				s.state.Buffing = false
				return true
			}
			s.buffOp.view.TransportWritten = true
			s.state.BuffReason = "buff_transport_written_waiting_for_cast_and_aura"
			return true
		}
	}
	s.state.BuffReason = "buffs_already_observed_or_no_eligible_known_buff"
	return false
}
