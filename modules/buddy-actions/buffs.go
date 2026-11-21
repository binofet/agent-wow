package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

type buffAttempt struct {
	view             actionsv1.BuffOperation
	baseline         uint64
	count            byte
	until            time.Time
	policySpellToken string
}

// Compatibility status only. Lua owns all automatic buff decisions.
func (s *Service) Buffs(_ context.Context, r *actionsv1.ToggleRequest) (*actionsv1.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.state.Buffing = r != nil && r.Enabled
		if s.state.Buffing {
			s.buffUntil = s.now().Add(lease)
		}
		s.state.BuffReason = "lua_policy_owns_buff_choices"
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
}
func (s *Service) buffTimeoutLocked() {
	if s.state.Buffing && !s.now().Before(s.buffUntil) {
		s.state.Buffing = false
	}
	if s.buffOp != nil && s.buffOp.view.Phase == "waiting_server" && !s.now().Before(s.buffOp.until) {
		s.buffUnconfirmedLocked("buff_effect_not_observed")
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
}
func (s *Service) buffRejectedLocked() {
	s.buffOp.view.Phase = "server_rejected"
	s.state.BuffReason = "buff_server_rejected"
	s.state.CastPending = false
	s.pendingSpell, s.pendingTarget = 0, 0
}

// Normalize only observed outcomes into the common policy action receipt.
func (op *buffAttempt) reportPolicySpellEffect(ctx context.Context) {
	phase := ""
	switch op.view.Phase {
	case "confirmed":
		phase = "confirmed_aura"
	case "server_rejected":
		phase = "server_rejected_no_replay"
	case "outcome_unconfirmed":
		phase = "outcome_unconfirmed_no_retry"
	}
	if phase == "" {
		return
	}
	effect := &actionsv1.SpellOperation{Capability: "buff", SpellId: op.view.SpellId, TargetGuid: op.view.TargetGuid,
		Phase: phase, TransportWritten: op.view.TransportWritten, ServerCastGo: op.view.ServerCastGo,
		EffectObserved: op.view.AuraConfirmed}
	if modcb.ReportPolicySpellEffect(ctx, op.policySpellToken, effect) {
		op.policySpellToken = ""
	}
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

// Reserve one exact caller-selected cast. This ledger correlates the ordinary
// cast packet and fresh aura update; it never retries or selects another spell.
func (s *Service) startBuffLocked(ctx context.Context, r spellcatalog.Spell, target uint64) {
	s.buffSequence++
	s.castCount++
	s.reserveSpellCooldownLocked(r)
	s.buffOp = &buffAttempt{view: actionsv1.BuffOperation{Sequence: s.buffSequence, SpellId: r.ID, TargetGuid: target, Phase: "waiting_server"},
		baseline: s.buffAuraRevision, count: s.castCount, until: s.now().Add(time.Duration(r.CastMs)*time.Millisecond + 3*time.Second),
		policySpellToken: modcb.PolicySpellToken(ctx)}
	s.state.BuffAttempts++
	gcd := s.now().Add(time.Duration(r.GcdMs) * time.Millisecond)
	if s.nextHeal.Before(gcd) {
		s.nextHeal = gcd
	}
	if s.nextAssist.Before(gcd) {
		s.nextAssist = gcd
	}
	s.state.CastPending = true
	s.pendingSpell, s.pendingTarget = r.ID, target
	s.castUntil = s.buffOp.until
	s.state.LastCastSpellId, s.state.LastCastTargetGuid, s.state.LastCastFailure = r.ID, target, 0
	if !s.sendLocked(ctx, opcode.CMSGCastSpell, spellPacket(s.castCount, r.ID, target)) {
		s.buffUnconfirmedLocked("buff_transport_outcome_unconfirmed_no_retry")
		return
	}
	s.buffOp.view.TransportWritten = true
	s.state.BuffReason = "buff_transport_written_waiting_for_cast_and_aura"
}
