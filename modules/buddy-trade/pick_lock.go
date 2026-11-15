package buddytrade

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/gamecatalog"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	nonTradedSlot       = 6
	pickLockSpell       = 1804
	lockpickingSkill    = 633
	targetFlagTradeItem = 0x1000
)

type tradeServiceItem struct {
	entry, count, lockID uint32
	wrapped              bool
}

type tradePickLock struct {
	item                     tradeServiceItem
	count                    byte
	queued, accepted, castGo bool
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
		!owner.GetAlive() || !owner.GetUnitFlagsKnown() || owner.GetUnitFlags()&combatOrCombatPet != 0 || !w.GetSelfSkillsKnown() {
		return false
	}
	for _, skill := range w.GetSelfSkills() {
		if skill.GetSkillId() == lockpickingSkill && skill.GetValue() != 0 {
			value := int64(skill.GetValue()) + int64(skill.GetTemporaryBonus()) + int64(skill.GetPermanentBonus())
			return value >= int64(required)
		}
	}
	return false
}

func (s *Service) prepareTradePickLock(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	s.mu.Lock()
	item, revision := s.serviceItem, s.state.Revision
	ready := s.state.Phase == "OPEN" && s.state.PartnerGuid == ownerGUID && !s.pendingCancel &&
		s.partnerSpell == 0 && item.entry != 0 && item.count == 1 && !item.wrapped && s.pickLock.item != item
	if !s.pickLock.until.IsZero() && !s.now().Before(s.pickLock.until) {
		s.pickLock.until = time.Time{}
		if !s.pickLock.queued {
			s.state.Reason = "trade_pick_lock_outcome_unconfirmed"
		}
	}
	s.mu.Unlock()
	required, pickable := gamecatalog.LockpickingRequirement(item.lockID)
	if !ready || !pickable || s.token == "" || !canPickTradeLock(w, required) {
		return false
	}
	toolkit := &actionsv1.SpellToolkitState{}
	if err := s.invoke(ctx, "buddy-actions", "spell_toolkit", &emptypb.Empty{}, toolkit); err != nil ||
		!toolkit.SpellbookKnown || toolkit.SessionId != s.state.SessionId || toolkit.SelfGuid != s.selfGUID {
		return false
	}
	var castTime time.Duration
	for _, spell := range toolkit.Spells {
		if spell.GetSpellId() == pickLockSpell && spell.GetMetadataKnown() {
			castTime = time.Duration(spell.GetCastTimeMs()) * time.Millisecond
			break
		}
	}
	if castTime == 0 || s.nearby(ctx) != nil {
		return false
	}
	fresh, err := s.world(ctx)
	if err != nil || !canPickTradeLock(fresh, required) {
		return false
	}
	s.mu.Lock()
	if s.state.Revision != revision || s.state.Phase != "OPEN" || s.pendingCancel || s.serviceItem != item {
		s.mu.Unlock()
		return false
	}
	s.pickLockCount++
	if s.pickLockCount == 0 {
		s.pickLockCount++
	}
	count := s.pickLockCount
	s.pickLock = tradePickLock{item: item, count: count, until: s.now().Add(castTime + 3*time.Second)}
	s.state.Reason = "preparing_trade_pick_lock"
	s.mu.Unlock()
	// The trade target is a packed slot number, not an item GUID.
	p := append([]byte{count}, u32(pickLockSpell)...)
	p = append(p, 0)
	p = append(p, u32(targetFlagTradeItem)...)
	_ = s.send(ctx, opcode.CMSGCastSpell, append(p, 1, nonTradedSlot))
	return true
}

func (s *Service) tradePickLockPacketLocked(op uint32, p []byte) {
	attempt := &s.pickLock
	if attempt.item.entry == 0 || attempt.item != s.serviceItem {
		return
	}
	switch op {
	case opcode.SMSGCastFailed:
		if len(p) < 6 || binary.LittleEndian.Uint32(p[1:]) != pickLockSpell ||
			p[0] != attempt.count && !(attempt.accepted && p[0] == 0) {
			return
		}
		// The initial cast ends with DONT_REPORT after putting the spell in
		// the trade window. Only the extended trade state confirms that.
		if p[5] != 27 {
			attempt.queued = false
			attempt.until = time.Time{}
			s.state.Reason = fmt.Sprintf("trade_pick_lock_server_rejected_%d", p[5])
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
