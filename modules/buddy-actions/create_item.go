package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type itemCreation struct {
	before uint64
	mapID  uint32
}

// CreateItem dispatches one learned, direct, reagentless self cast. Only a
// matching spell event, own created-item receipt and newer carried inventory
// can confirm output; the server determines quantity and bag placement.
func (s *Service) CreateItem(ctx context.Context, r *actionsv1.CreateItemRequest) (*actionsv1.State, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() != s.selfGUID || r.GetSpellId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "current body and exact learned creation rank required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireInteractionLocked()
	s.observeItemCreationLocked(nil)
	if s.closed || r.GetSessionId() != s.sessionID || s.itemCreationPendingLocked() != nil ||
		s.state.CastPending || s.state.Moving || s.state.Traveling || s.state.MotionUnconfirmed ||
		s.state.AutoAttacking || s.state.AutoAttackRequested || s.state.AutoAttackUnconfirmed ||
		s.autoTarget != 0 || s.autoStopping || s.state.InteractionOwner != "" || s.state.Practicing ||
		s.engage.active || s.fargodeep.active || s.crowdControl.active || s.recovery.state.Active ||
		s.now().Before(s.nextHeal) || s.now().Before(s.nextAssist) {
		return nil, status.Error(codes.FailedPrecondition, "current session and idle item-creation custody required")
	}
	spell, learned := s.learnedContractsLocked()[r.GetSpellId()]
	if !s.spellbookKnown || !learned || spell.Capability().Kind != spellcatalog.CreateItem || s.now().Before(s.spellReady[spell.ID]) {
		return nil, status.Error(codes.FailedPrecondition, "exact learned item-creation rank required")
	}
	w, err := s.snapshot(ctx)
	if err != nil || safeQuestCharacter(w, s.selfGUID) != "" {
		return nil, status.Error(codes.FailedPrecondition, "current living out-of-combat caster observation required")
	}
	self := w.Self
	if self.MovementFlags&0xff != 0 || self.ChannelSpellId != 0 || !self.PowerKnown || int32(self.PowerType) != spell.Power ||
		uint64(self.Power) < spellCost(spell, self) || !s.casterRequirementsLocked(spell) {
		return nil, status.Error(codes.FailedPrecondition, "stationary caster with observed power and spell requirements required")
	}
	entry := spell.CreatedItem()
	before, ok := carriedCreationCount(w, entry)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "complete own carried inventory required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.castSupportLocked(ctx, spell, s.selfGUID, 0)
	op := s.supportOp
	op.creation = &itemCreation{before: before, mapID: self.MapId}
	op.view.CreatedItemEntry = entry
	op.view.BaselineInventoryRevision = w.InventoryRevision
	return s.stateLocked(), nil
}

// World marks InventoryKnown only after observing the complete carried layout.
func carriedCreationCount(w *worldv1.SnapshotResponse, entry uint32) (uint64, bool) {
	if w == nil || !w.InventoryKnown || w.InventoryRevision == 0 || len(w.InventoryBags) != 5 {
		return 0, false
	}
	var bags [5]bool
	var count uint64
	for _, bag := range w.InventoryBags {
		if bag == nil || bag.Bag >= uint32(len(bags)) || bags[bag.Bag] || !bag.SlotKnown || !bag.ContentsKnown ||
			bag.SlotCount > 36 || len(bag.Slots) != int(bag.SlotCount) {
			return 0, false
		}
		bags[bag.Bag] = true
		var slots [36]bool
		for _, row := range bag.Slots {
			if row == nil || row.Bag != bag.Bag || row.Slot >= bag.SlotCount || slots[row.Slot] ||
				row.ItemGuid != 0 && (!row.MetadataKnown || row.Entry == 0 || row.StackCount == 0) {
				return 0, false
			}
			slots[row.Slot] = true
			if row.ItemGuid != 0 && row.Entry == entry {
				count += uint64(row.StackCount)
			}
		}
	}
	return count, true
}

func (s *Service) itemCreationPendingLocked() *supportAttempt {
	if op := s.supportOp; op != nil && op.creation != nil {
		switch op.view.Phase {
		case "waiting_server", "waiting_created_item", "transport_unconfirmed_no_retry":
			return op
		}
	}
	return nil
}

func (s *Service) finishItemCreationLocked(phase string) {
	if op := s.itemCreationPendingLocked(); op != nil {
		op.view.Phase = phase
		if s.pendingSpell == op.view.SpellId && s.castCount == op.count {
			s.pendingSpell, s.pendingTarget, s.state.CastPending = 0, 0, false
		}
	}
}

// SendNewItem reports exact actor and created-versus-received flags. Direct
// self creation uses received=1 and created=1; ordinary loot or trade cannot
// substitute. The count proof is also checked against a newer bag snapshot.
func (s *Service) observeCreatedItemPushLocked(p []byte) error {
	op := s.itemCreationPendingLocked()
	if op == nil || op.view.ItemPushObserved || !s.now().Before(op.until) {
		return nil
	}
	if len(p) != 45 {
		return errors.New("malformed item creation push")
	}
	received, created, chat := binary.LittleEndian.Uint32(p[8:]), binary.LittleEndian.Uint32(p[12:]), binary.LittleEndian.Uint32(p[16:])
	if received > 1 || created > 1 || chat > 1 {
		return errors.New("malformed item creation push flags")
	}
	if binary.LittleEndian.Uint64(p) != s.selfGUID || received != 1 || created != 1 ||
		binary.LittleEndian.Uint32(p[25:]) != op.view.CreatedItemEntry {
		return nil
	}
	added, total := binary.LittleEndian.Uint32(p[37:]), binary.LittleEndian.Uint32(p[41:])
	if added == 0 || uint64(total) != op.creation.before+uint64(added) {
		return nil
	}
	op.view.CreatedItemCount, op.view.ItemPushObserved = added, true
	return nil
}

func (s *Service) observeItemCreationLocked(w *worldv1.SnapshotResponse) {
	op := s.itemCreationPendingLocked()
	if op == nil {
		return
	}
	if !s.now().Before(op.until) {
		s.finishItemCreationLocked("outcome_unconfirmed_no_retry")
		return
	}
	if !op.view.ServerCastGo || !op.view.ItemPushObserved || w.GetSelf().GetGuid() != s.selfGUID ||
		!w.GetSelf().GetPresent() || !w.GetSelf().GetMapKnown() || w.GetSelf().GetMapId() != op.creation.mapID ||
		w.GetInventoryRevision() <= op.view.BaselineInventoryRevision {
		return
	}
	if count, ok := carriedCreationCount(w, op.view.CreatedItemEntry); ok && count == op.creation.before+uint64(op.view.CreatedItemCount) {
		op.view.ObservedInventoryRevision = w.InventoryRevision
		op.view.InventoryConfirmed, op.view.EffectObserved = true, true
		s.finishItemCreationLocked("confirmed_item")
	}
}
