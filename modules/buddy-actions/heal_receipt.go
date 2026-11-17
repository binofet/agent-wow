package buddyactions

import (
	"encoding/binary"
	"errors"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

func (s *Service) reserveHealReceiptLocked(token string, heal spellcatalog.Spell, target uint64) *supportAttempt {
	// A heal log has no cast count. Only a single direct effect admits the
	// matching log after the count-bearing GO for this reserved cast.
	direct := false
	if len(heal.Effects) == 1 && heal.TargetMask&^uint32(2) == 0 {
		e := heal.Effects[0]
		direct = e.Kind == 10 && (e.A == 1 || e.A == 21) && e.B == 0 &&
			e.Aura == 0 && e.Trigger == 0 && e.PeriodMs == 0 && e.Chain <= 1 && e.Radius == 0
	}
	return s.reserveExplicitCastLocked(token, heal.ID, target, string(spellcatalog.Heal), direct)
}

func (s *Service) reserveExplicitCastLocked(token string, spell uint32, target uint64, capability string, effectLogEligible bool) *supportAttempt {
	op := &supportAttempt{
		view: actionsv1.SpellOperation{
			SpellId: spell, TargetGuid: target, Capability: capability, Phase: "waiting_server",
		},
		count: s.castCount, until: s.castUntil, policySpellToken: token, effectLogEligible: effectLogEligible,
	}
	s.supportOp = op
	return op
}

func (s *Service) pendingExplicitCastLocked() *supportAttempt {
	op := s.supportOp
	if op != nil && (op.view.Capability == string(spellcatalog.Heal) || op.view.Capability == offenseCapability) && s.state.CastPending &&
		s.castCount == op.count && s.pendingSpell == op.view.SpellId && s.pendingTarget == op.view.TargetGuid {
		return op
	}
	return nil
}

func (s *Service) endExplicitCastLocked(phase string) {
	if op := s.pendingExplicitCastLocked(); op != nil {
		op.view.Phase = phase
		s.state.CastPending = false
		s.pendingSpell, s.pendingTarget = 0, 0
	}
}

func (s *Service) expireExplicitCastLocked() {
	if op := s.pendingExplicitCastLocked(); op != nil && !s.now().Before(op.until) {
		s.endExplicitCastLocked("outcome_unconfirmed_no_retry")
	}
}

func (s *Service) explicitCastPacketLocked(code uint32, p []byte) (bool, error) {
	s.expireExplicitCastLocked()
	op := s.pendingExplicitCastLocked()
	if op == nil {
		return code == opcode.SMSGSpellheallog, nil
	}
	healing := op.view.Capability == string(spellcatalog.Heal)
	bad := errors.New("malformed exact cast event")
	switch code {
	case opcode.SMSGTransferPending, opcode.SMSGNewWorld:
		if code == opcode.SMSGNewWorld && len(p) == 20 || code == opcode.SMSGTransferPending && (len(p) == 4 || len(p) == 12) {
			s.endExplicitCastLocked("world_transfer_outcome_unconfirmed")
		}
		return false, nil // The normal lifecycle handler also consumes this event.
	case opcode.SMSGCastFailed, opcode.SMSGSpellFailure, opcode.SMSGSpellFailedOther:
		n := 0
		if code != opcode.SMSGCastFailed {
			caster, size, err := readGUID(p)
			if err != nil {
				return true, bad
			}
			if caster != s.selfGUID {
				return true, nil
			}
			n = size
		}
		if len(p) < n+6 {
			return true, bad
		}
		if p[n] != op.count || binary.LittleEndian.Uint32(p[n+1:]) != op.view.SpellId {
			return true, nil
		}
		if len(p) != n+6 {
			return true, bad
		}
		if op.view.ServerCastGo {
			// A rejection after GO cannot establish the absence of an effect.
			s.endExplicitCastLocked("server_cast_go_effect_unverified")
		} else if healing {
			op.view.Phase = "server_rejected_no_replay"
			s.castFailedLocked(uint32(p[n+5]), "server_cast_failed_healing_paused")
		} else {
			s.endExplicitCastLocked("server_rejected_no_replay")
			s.state.LastCastFailure = uint32(p[n+5])
			s.state.Reason = "server_offense_cast_rejected"
		}
		return true, nil
	case opcode.SMSGSpellStart, opcode.SMSGSpellGo:
		caster, count, spell, offset, err := castEvent(p)
		if err != nil {
			return true, bad
		}
		source, _, _ := readGUID(p)
		if caster != s.selfGUID || source != s.selfGUID || count != op.count || spell != op.view.SpellId {
			return true, nil
		}
		goEvent := code == opcode.SMSGSpellGo
		if !validCastTargetBody(p, offset, goEvent, op.view.TargetGuid, s.implicitSelfSpellLocked(spell, op.view.TargetGuid)) {
			return true, bad
		}
		if op.view.ServerCastGo {
			return true, nil
		}
		if !goEvent {
			if healing {
				s.state.ServerCastStarts++
			} else {
				s.state.ServerAssistStarts++
			}
			s.state.Reason = "server_cast_started"
			return true, nil
		}
		op.view.ServerCastGo = true
		if healing {
			s.state.ServerCastGo++
			s.state.Reason = "server_spell_go_health_effect_unverified"
		} else {
			s.state.ServerAssistGo++
			s.state.Reason = "server_offensive_spell_go_damage_effect_unverified"
		}
		hits := offset + 8
		if !op.effectLogEligible || len(p) < hits+10 || p[hits] != 1 ||
			binary.LittleEndian.Uint64(p[hits+1:]) != op.view.TargetGuid || p[hits+9] != 0 {
			s.endExplicitCastLocked("server_cast_go_effect_unverified")
		}
		return true, nil
	case opcode.SMSGSpellheallog:
		if !healing || !op.view.ServerCastGo {
			return true, nil
		}
		target, caster, spell, effective, err := decodeHealReceipt(p)
		if err != nil {
			return true, err
		}
		if target != op.view.TargetGuid || caster != s.selfGUID || spell != op.view.SpellId {
			return true, nil
		}
		if effective == 0 {
			s.endExplicitCastLocked("server_cast_go_effect_unverified")
			return true, nil
		}
		op.view.EffectObserved = true
		s.endExplicitCastLocked("confirmed_heal")
		s.state.Reason = "server_heal_effect_confirmed"
		return true, nil
	}
	return false, nil
}

// Unit::SendHealSpellLog sends the target before the caster. Overheal is
// calculated after heal absorption, so effective healing is heal minus overheal.
// https://github.com/azerothcore/azerothcore-wotlk/blob/993b45959556302a4eccded066085793b4b55322/src/server/game/Entities/Unit/Unit.cpp#L8153-L8183
func decodeHealReceipt(p []byte) (target, caster uint64, spell, effective uint32, err error) {
	bad := errors.New("malformed heal log")
	target, n, err := readGUID(p)
	if err != nil || target == 0 {
		return 0, 0, 0, 0, bad
	}
	caster, m, err := readGUID(p[n:])
	if err != nil || caster == 0 {
		return 0, 0, 0, 0, bad
	}
	p = p[n+m:]
	if len(p) != 18 || p[16] > 1 || p[17] != 0 {
		return 0, 0, 0, 0, bad
	}
	spell = binary.LittleEndian.Uint32(p)
	heal, overheal := binary.LittleEndian.Uint32(p[4:]), binary.LittleEndian.Uint32(p[8:])
	if spell == 0 || overheal > heal {
		return 0, 0, 0, 0, bad
	}
	return target, caster, spell, heal - overheal, nil
}
