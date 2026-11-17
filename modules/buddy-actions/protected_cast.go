package buddyactions

import (
	"context"
	"encoding/binary"

	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

func (s *Service) cancelProtectedCastLocked(ctx context.Context) {
	now := s.now()
	if s.closed || !s.state.CastPending || s.protectedCastCancelAttempted || !now.Before(s.castUntil) || ctx.Err() != nil {
		return
	}
	if op := s.pendingExplicitCastLocked(); op != nil && op.view.ServerCastGo {
		return // Waiting for an effect receipt does not leave the cast interruptible.
	}
	// An overflow may inhibit new offense, but only a matching observed aura
	// authorizes interruption of this already-dispatched cast.
	s.targetProtection.expire(now)
	for key := range s.targetProtection.auras {
		if key.target != s.pendingTarget {
			continue
		}
		spell, ok := spellcatalog.Default().Lookup(s.pendingSpell)
		if !ok || spell.CastMs == 0 {
			return
		}
		capability := spell.Capability()
		if capability.Target != "enemy" || (capability.Kind != spellcatalog.Damage && capability.Kind != spellcatalog.WeaponStrike) {
			return
		}
		s.protectedCastCancelAttempted = true
		s.cancelCastCount++
		// HandleCancelCastOpcode reads an independent cancel counter and spell ID.
		// https://github.com/azerothcore/azerothcore-wotlk/blob/993b45959556302a4eccded066085793b4b55322/src/server/game/Handlers/SpellHandler.cpp#L562-L574
		payload := binary.LittleEndian.AppendUint32([]byte{s.cancelCastCount}, spell.ID)
		if s.sendLocked(ctx, opcode.CMSGCancelCast, payload) {
			s.state.Reason = "protected_target_cast_cancel_waiting_for_server"
		} else {
			s.state.Reason = "protected_target_cast_cancel_transport_unconfirmed"
		}
		return // The normal spell result or deadline still owns cast settlement.
	}
}
