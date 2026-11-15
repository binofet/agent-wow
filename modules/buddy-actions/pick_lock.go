package buddyactions

import (
	"context"
	"encoding/binary"
	"fmt"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/gamecatalog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	pickLockSpell      = 1804
	lockpickingSkill   = 633
	pickLockCapability = "pick_lock"
)

func (s *Service) PickLock(ctx context.Context, r *actionsv1.PickLockRequest) (*actionsv1.State, error) {
	if r.GetSessionId() == "" || r.GetSessionId() != s.sessionID || r.GetTargetGuid()>>48 != 0xf110 {
		return nil, status.Error(codes.InvalidArgument, "current session and exact chest GUID required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.state.CastPending || s.state.InteractionOwner != "" || s.state.Traveling || s.state.MotionUnconfirmed {
		return nil, status.Error(codes.FailedPrecondition, "character is busy")
	}
	spell, known := s.learnedContractsLocked()[pickLockSpell]
	if !known || !s.knownSpells[pickLockSpell] {
		return nil, status.Error(codes.FailedPrecondition, "Pick Lock is not learned")
	}
	w, err := s.snapshot(ctx)
	if err != nil || safeSelfCombat(w, s.selfGUID) != "" || w.Self.GetClassId() != 4 || w.Self.UnitFlags&(0x80000|0x2000) != 0 || !s.refreshPositionLocked(w) {
		return nil, status.Error(codes.FailedPrecondition, "observed rogue must be alive and out of combat")
	}
	target, err := s.pickableChestLocked(w, r.TargetGuid, spell.MaxHostile)
	if err != nil {
		return nil, err
	}
	if !s.stopAutoAttackLocked(ctx) || !s.stopLocked(ctx) || !s.faceEnemyLocked(ctx, target) {
		return nil, status.Error(codes.FailedPrecondition, "could not stop and face chest")
	}
	w, err = s.snapshot(ctx)
	if err != nil || safeSelfCombat(w, s.selfGUID) != "" || w.Self.UnitFlags&(0x80000|0x2000) != 0 || !s.refreshPositionLocked(w) {
		return nil, status.Error(codes.FailedPrecondition, "character state changed before cast")
	}
	if _, err := s.pickableChestLocked(w, r.TargetGuid, spell.MaxHostile); err != nil {
		return nil, err
	}
	s.state.LastCastFailure = 0
	s.castTargetedSupportLocked(ctx, spell, r.TargetGuid, 0, pickLockCapability)
	return s.stateLocked(), nil
}

func (s *Service) pickableChestLocked(w *worldv1.SnapshotResponse, guid uint64, maxRange float64) (position, error) {
	entry := uint32(guid >> 24 & 0xffffff)
	record, exists := gamecatalog.Default().Container(entry)
	required, pickable := gamecatalog.LockpickingRequirement(record.LockID)
	if !exists || record.Type != 3 || !pickable {
		return position{}, status.Error(codes.FailedPrecondition, "catalog does not identify a pickable chest lock")
	}
	rank := uint32(0)
	if w.SelfSkillsKnown {
		for _, skill := range w.SelfSkills {
			if skill.GetSkillId() == lockpickingSkill {
				rank = skill.GetValue()
				break
			}
		}
	}
	if rank == 0 || rank < required {
		return position{}, status.Error(codes.FailedPrecondition, fmt.Sprintf("Lockpicking %d required; observed skill %d", required, rank))
	}
	for _, chest := range w.QuestObjects {
		if chest.GetGuid() != guid {
			continue
		}
		if chest.Entry != entry || !chest.Present || !chest.TypeKnown || chest.GameObjectType != 3 ||
			!chest.MapKnown || chest.MapId != s.posMap || !chest.PositionKnown ||
			!chest.FlagsKnown || chest.Flags != 2 || !chest.StateKnown || chest.State != 1 {
			break
		}
		pos := position{chest.X, chest.Y, chest.Z, chest.Orientation}
		if !finite(pos) || maxRange <= 0 || distance(s.pos, pos) > maxRange {
			return position{}, status.Error(codes.FailedPrecondition, "move within Pick Lock range of the chest")
		}
		return pos, nil
	}
	return position{}, status.Error(codes.FailedPrecondition, "exact ready locked chest is not observed")
}

func (s *Service) observePickLockLootLocked(p []byte) {
	op := s.supportOp
	if op == nil || op.view.Capability != pickLockCapability || !op.view.TransportWritten ||
		!s.now().Before(op.until) || len(p) < 14 || p[8] != 6 || len(p) != 14+int(p[13])*22 ||
		binary.LittleEndian.Uint64(p) != op.view.TargetGuid {
		return
	}
	op.view.EffectObserved = true
	op.view.Phase = "confirmed_loot_window"
	if s.pendingSpell == op.view.SpellId && s.castCount == op.count {
		s.state.CastPending = false
		s.pendingSpell, s.pendingTarget = 0, 0
	}
}
