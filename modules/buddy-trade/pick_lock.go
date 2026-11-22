package buddytrade

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const (
	nonTradedSlot       = 6
	pickLockSpell       = 1804
	lockpickingSkill    = 633
	targetFlagTradeItem = 0x1000
	spellFailedTryAgain = 132
)

type tradeServiceItem struct {
	entry, count, lockID uint32
	wrapped              bool
}

type tradePickLock struct {
	item                     tradeServiceItem
	count                    byte
	queued, accepted, castGo bool
	retry                    bool
	until                    time.Time
}

func (s *Service) serviceOfferReadyLocked() bool {
	return s.partnerSpell == 0 && (s.serviceItem.entry == 0 ||
		s.pickLock.queued && s.pickLock.item == s.serviceItem)
}

func canPickTradeLock(w *worldv1.SnapshotResponse, required uint32) bool {
	self, owner := w.GetSelf(), w.GetOwner()
	const combatOrCombatPet = 0x80000 | 0x2000
	if !self.GetClassKnown() || self.GetClassId() != 4 || !self.GetAlive() ||
		!self.GetUnitFlagsKnown() || self.GetUnitFlags()&combatOrCombatPet != 0 ||
		!owner.GetAlive() || !owner.GetUnitFlagsKnown() || owner.GetUnitFlags()&combatOrCombatPet != 0 || !w.GetSelfSkillsKnown() || w.GetSelfSkillsRevision() == 0 {
		return false
	}
	facts, skill := tradeServiceSkills(w)
	if facts["complete"] != true || skill == nil || skill.Value == 0 {
		return false
	}
	value := int64(skill.Value) + int64(skill.TemporaryBonus) + int64(skill.PermanentBonus)
	return value >= int64(required)
}

// True means an observed service window remains handled here, including a
// policy wait or retained uncertain attempt. Only queued server proof lets the
// ordinary consent branch proceed; no preparation failure falls back to Go.
func (s *Service) prepareTradePickLock(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	s.mu.Lock()
	if s.serviceItem.entry == 0 || s.pickLock.queued && s.pickLock.item == s.serviceItem && !s.pickLock.retry && !s.pickLock.castGo {
		s.mu.Unlock()
		return false
	}
	if !s.pickLock.until.IsZero() && !s.now().Before(s.pickLock.until) {
		s.pickLock.until = time.Time{}
		if !s.pickLock.queued {
			s.state.Reason = "trade_pick_lock_outcome_unconfirmed"
		}
	}
	s.mu.Unlock()
	toolkit, err := s.serviceToolkit(ctx)
	if err != nil {
		s.servicePolicyWait("trade_service_spellbook_unavailable")
		return true
	}
	admission, reason := s.requestTradeService(ctx, w, toolkit)
	if admission == nil {
		s.servicePolicyWait(reason)
		return true
	}
	if err := admission.lease.renew(ctx, s); err != nil {
		s.servicePolicyWait("trade_service_lease_unconfirmed")
		return true
	}
	if err := s.nearby(ctx); err != nil {
		s.servicePolicyWait("trade_service_partner_not_nearby")
		return true
	}
	currentToolkit, err := s.serviceToolkit(ctx)
	if err != nil || admission.checkToolkit(currentToolkit) != nil {
		s.servicePolicyWait("trade_service_spellbook_changed")
		return true
	}
	// Nearby and Toolkit both cross Actions. Prove the exact same retained
	// token again before the final authoritative observation.
	if err := admission.lease.renew(ctx, s); err != nil {
		s.servicePolicyWait("trade_service_lease_unconfirmed")
		return true
	}
	fresh, err := s.world(ctx)
	if err != nil {
		s.servicePolicyWait("trade_service_final_world_unavailable")
		return true
	}
	s.mu.Lock()
	if !admission.readyLocked(ctx, s, fresh) {
		s.mu.Unlock()
		s.servicePolicyWait("trade_service_context_changed_before_prepare")
		return true
	}
	s.pickLockCount++
	if s.pickLockCount == 0 {
		s.pickLockCount++
	}
	count := s.pickLockCount
	s.pickLock = tradePickLock{item: admission.lease.service, count: count, until: admission.reserveAt.Add(admission.castTime + 3*time.Second)}
	s.state.Reason = "preparing_trade_pick_lock"
	s.mu.Unlock()
	// The trade target is a packed slot number, not an item GUID.
	p := append([]byte{count}, u32(pickLockSpell)...)
	p = append(p, 0)
	p = append(p, u32(targetFlagTradeItem)...)
	if ctx.Err() == nil {
		_ = s.send(ctx, opcode.CMSGCastSpell, append(p, 1, nonTradedSlot))
	}
	return true
}

func (s *Service) tradePickLockPacketLocked(op uint32, p []byte) {
	attempt := &s.pickLock
	if attempt.item.entry == 0 || attempt.item != s.serviceItem {
		return
	}
	switch op {
	case opcode.SMSGCastFailed:
		if len(p) < 6 || binary.LittleEndian.Uint32(p[1:]) != pickLockSpell {
			return
		}
		waitingForStage := s.state.Phase == "OPEN" && !attempt.until.IsZero() && p[0] == attempt.count
		waitingForCompletion := s.state.Phase == "ACCEPT_SENT" && attempt.accepted && attempt.queued && p[0] == 0
		if !waitingForStage && !waitingForCompletion {
			return
		}
		// The initial cast ends with DONT_REPORT after putting the spell in
		// the trade window. Only the extended trade state confirms that.
		if p[5] != 27 {
			attempt.queued = false
			attempt.until = time.Time{}
			// TRY_AGAIN is an explicit rejection before any lock effect. A new
			// attempt still waits for an open trade and rechecks the same slot.
			attempt.retry = p[5] == spellFailedTryAgain && len(p) == 6
			if attempt.retry {
				s.state.Reason = "trade_pick_lock_retry_pending"
			} else {
				s.state.Reason = fmt.Sprintf("trade_pick_lock_server_rejected_%d", p[5])
			}
		}
	case opcode.SMSGSpellGo:
		if attempt.accepted && tradePickLockCastGo(p, s.selfGUID) {
			attempt.castGo = true
			if s.state.Phase == "COMPLETE" {
				s.state.Reason = "server_confirmed_trade_and_pick_lock_cast"
			}
		}
	}
}

func tradePickLockCastGo(p []byte, self uint64) bool {
	_, n := tradePackedGUID(p)
	if n == 0 {
		return false
	}
	caster, size := tradePackedGUID(p[n:])
	if size == 0 || caster != self {
		return false
	}
	p = p[n+size:]
	// Triggered on trade completion: server-generated count, spell ID,
	// cast flags, clock, no unit hits/misses, and a real item target.
	if len(p) < 20 || p[0] != 0 || binary.LittleEndian.Uint32(p[1:]) != pickLockSpell ||
		p[13] != 0 || p[14] != 0 || binary.LittleEndian.Uint32(p[15:]) != targetFlagTradeItem {
		return false
	}
	item, size := tradePackedGUID(p[19:])
	return size != 0 && item>>48 == 0x4000
}

func tradePackedGUID(p []byte) (uint64, int) {
	if len(p) == 0 {
		return 0, 0
	}
	var guid uint64
	n := 1
	for i := range 8 {
		if p[0]&(1<<i) == 0 {
			continue
		}
		if n == len(p) || p[n] == 0 {
			return 0, 0
		}
		guid |= uint64(p[n]) << (8 * i)
		n++
	}
	return guid, n
}
