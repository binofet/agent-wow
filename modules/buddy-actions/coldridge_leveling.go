package buddyactions

import (
	"context"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const coldridgeLevelingGoalName = "coldridge_706_to_level_5"
const coldridgeLevelingWindow = 3 * time.Minute

// Kept independently of practiceTarget, which ordinary STOP must clear. This
// record never authorizes another creature or another offensive pull.
type coldridgeLevelingRun struct {
	requestID         string
	until             time.Time
	sourceGUID        uint64
	positionRevision  uint64
	pullReserved      bool
	observedLevel     uint32
	levelFiveObserved bool
	invalid           bool
}

func validColdridgeLevelingRequestID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if c < 'a' || c > 'z' {
			if c < 'A' || c > 'Z' {
				if c < '0' || c > '9' {
					if c != '_' && c != '-' {
						return false
					}
				}
			}
		}
	}
	return true
}

// The sole own active218 row identifies this observed progression stage. Its
// ITEM2004 objective is not a706 kill counter and is never incremented here.
// Historical182 reward acceptance remains a separate owner-held receipt.
func ownColdridgeLevelingContext(w *worldv1.SnapshotResponse, self uint64) bool {
	if self == 0 || self>>48 != 0 || w == nil || w.Self == nil || w.Self.Guid != self || !w.Self.Present || !w.Self.MapKnown || w.Self.MapId != 0 || !w.Self.PositionKnown || w.Self.PositionRevision == 0 || !w.Self.ClassKnown || w.Self.ClassId != 9 || !w.Self.LevelKnown || w.Self.Level < 4 || w.Self.Level > 5 || !w.SelfQuestLogKnown || len(w.SelfQuestLog) != 25 {
		return false
	}
	var slots [25]bool
	found := false
	for _, q := range w.SelfQuestLog {
		if q == nil || q.Slot >= 25 || slots[q.Slot] {
			return false
		}
		slots[q.Slot] = true
		if q.QuestId == 0 {
			continue
		}
		if found || q.QuestId != 218 || q.Complete || q.Failed || q.State&3 != 0 || len(q.ObjectiveCounts) != 4 {
			return false
		}
		for _, count := range q.ObjectiveCounts {
			if count != 0 {
				return false
			}
		}
		found = true
	}
	return found
}

func (s *Service) coldridgeLevelingActiveLocked() bool {
	return s.state.Practicing && s.coldridgeGoal.leveling
}

func (s *Service) coldridgeLevelingRunValidLocked() bool {
	r := &s.coldridgeLeveling
	return r.requestID != "" && !r.invalid && s.now().Before(r.until)
}

// The final snapshot/reservation can precede a scheduling delay. Check again
// at the packet boundary without obtaining another dependency observation.
func (s *Service) coldridgeLevelingCastAllowedLocked() bool {
	r := &s.coldridgeLeveling
	return s.coldridgeLevelingActiveLocked() && s.coldridgeLevelingRunValidLocked() && r.pullReserved && r.observedLevel == 4 && r.sourceGUID != 0 && s.practicePullStarted && s.practiceTarget == r.sourceGUID && s.pendingTarget == r.sourceGUID && s.pendingSpell == shadowBolt
}

func (s *Service) coldridgeLevelingMovementAllowedLocked(w *worldv1.SnapshotResponse, p position) bool {
	// Historical leveling records do not govern later exploration/trainer travel.
	if !s.coldridgeGoal.leveling || !s.state.Practicing && (s.state.InteractionOwner != "loot" || !s.coldridgeLeveling.pullReserved) {
		return true
	}
	s.observeColdridgeLevelingLevelLocked(w)
	r := &s.coldridgeLeveling
	if !s.coldridgeLevelingRunValidLocked() || !ownColdridgeLevelingContext(w, s.selfGUID) {
		return false
	}
	self := w.Self
	// Recheck the last world, rather than the caller's pre-clock safety view.
	// Only an already-started own pull may be in combat; pre-pull and corpse
	// travel still require idle safety.
	safety := safeQuestCharacter(w, s.selfGUID)
	if s.state.Practicing && s.practicePullStarted {
		safety = safePracticeCharacter(w, s.selfGUID)
	}
	if safety != "" || !self.HealthKnown || self.Health == 0 || !self.SpeedsKnown || !(math.Abs(float64(self.RunSpeed)-7) <= .01) || self.UnitFlags&0x2000 != 0 || self.ChannelSpellKnown && self.ChannelSpellId != 0 || self.ChannelObjectKnown && self.ChannelObjectGuid != 0 {
		return false
	}
	return self.PositionRevision == r.positionRevision && (!s.state.Practicing || self.Level == 4) && s.coldridgeGoal.baselineContains(playerPosition(self)) && s.coldridgeGoal.contains(s.pos) && s.coldridgeGoal.contains(p)
}

func (s *Service) observeColdridgeLevelingLevelLocked(w *worldv1.SnapshotResponse) {
	r := &s.coldridgeLeveling
	if r.requestID == "" {
		return
	}
	if w == nil || w.Self == nil || w.Self.Guid != s.selfGUID || !w.Self.Present || !w.Self.ClassKnown || w.Self.ClassId != 9 || !w.Self.LevelKnown || w.Self.Level < 4 || w.Self.Level < r.observedLevel {
		r.invalid = true
		return
	}
	r.observedLevel = w.Self.Level
	if w.Self.Level == 5 {
		r.levelFiveObserved = true
	}
}

func (s *Service) coldridgeLevelingSnapshotLocked() *actionsv1.ColdridgeLevelingState {
	r := &s.coldridgeLeveling
	if r.requestID == "" || !s.coldridgeGoal.leveling {
		return nil
	}
	s.observeColdridgeLevelingLevelLocked(s.lastWorld)
	remaining := uint64(0)
	now := s.now()
	if !r.invalid && now.Before(r.until) {
		remaining = uint64(r.until.Sub(now).Milliseconds())
	}
	return &actionsv1.ColdridgeLevelingState{RequestId: r.requestID, Goal: coldridgeLevelingGoalName,
		TargetLevel: 5, MaxPulls: 1, SourceGuid: r.sourceGUID, PositionRevision: r.positionRevision,
		PullReserved: r.pullReserved, RemainingMs: remaining, ObservedLevel: r.observedLevel,
		LevelFiveObserved: r.levelFiveObserved, SelfGuid: s.selfGUID}
}

// Reserve before the first potentially sent offense; ambiguity consumes the
// sole pull. The caller supplies its final post-facing requested-target world.
func (s *Service) reserveColdridgeLevelingPullLocked(w *worldv1.SnapshotResponse, guid uint64) bool {
	r := &s.coldridgeLeveling
	s.observeColdridgeLevelingLevelLocked(w)
	if !s.coldridgeLevelingActiveLocked() || r.invalid || r.requestID == "" || r.pullReserved || !s.now().Before(r.until) || !ownColdridgeLevelingContext(w, s.selfGUID) || w.Self.Level != 4 || w.Self.PositionRevision != r.positionRevision || guid>>48 != 0xf130 || guid&0xffffff == 0 || uint32(guid>>24&0xffffff) != 706 {
		return false
	}
	target := practiceTargetObservation(w, guid)
	if guid != s.practiceTarget || !s.coldridgeGoal.eligible(target, w.Self) || !s.coldridgeGoal.positionEvidenceWithin(target) {
		return false
	}
	r.sourceGUID, r.pullReserved = guid, true
	return true
}

func (s *Service) coldridgeLevelingCorpseContextLocked(w *worldv1.SnapshotResponse, corpse *worldv1.CreatureState) bool {
	r := &s.coldridgeLeveling
	s.observeColdridgeLevelingLevelLocked(w)
	goal := coldridgePracticeGoal{leveling: true}
	return !r.invalid && r.requestID != "" && r.pullReserved && r.sourceGUID != 0 && s.now().Before(r.until) && ownColdridgeLevelingContext(w, s.selfGUID) && w.Self.PositionRevision == r.positionRevision && corpse != nil && corpse.Guid == r.sourceGUID && matches182Profile(corpse) && corpse.Guid>>48 == 0xf130 && corpse.Guid&0xffffff != 0 && uint32(corpse.Guid>>24&0xffffff) == 706 && w.Self.Level >= corpse.Level && goal.baselineContains(playerPosition(w.Self)) && goal.contains(s.pos) && goal.positionEvidenceWithin(corpse)
}

// StartColdridgeLeveling is deliberately separate from ordinary Practice.
// A repeated request ID returns its retained record without renewing time or
// pull authority. New IDs cannot replace an active window or replay an old one.
func (s *Service) StartColdridgeLeveling(ctx context.Context, r *actionsv1.ColdridgeLevelingRequest) (*actionsv1.State, error) {
	if r == nil || !validColdridgeLevelingRequestID(r.RequestId) {
		return nil, status.Error(codes.InvalidArgument, "bounded leveling request ID required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.independent {
		return nil, status.Error(codes.FailedPrecondition, "own independent session required")
	}
	if _, seen := s.coldridgeLevelingRequests[r.RequestId]; seen {
		if s.coldridgeLeveling.requestID != r.RequestId {
			return nil, status.Error(codes.FailedPrecondition, "leveling request already consumed")
		}
		return s.stateLocked(), nil
	}
	if len(s.coldridgeLevelingRequests) >= 64 || s.state.Practicing || s.state.Following || s.state.Healing || s.state.Assisting || s.state.Traveling || s.questApproaching || s.state.InteractionOwner != "" || s.state.CastPending || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed || s.state.AutoAttacking || s.autoTarget != 0 || s.recovery.snapshot(s.now()).Active {
		return nil, status.Error(codes.FailedPrecondition, "leveling requires unused request and confirmed idle actions")
	}
	w, err := s.snapshot(ctx)
	goal := coldridgePracticeGoal{leveling: true}
	if err != nil || !ownColdridgeLevelingContext(w, s.selfGUID) || w.Self.Level != 4 || safeQuestCharacter(w, s.selfGUID) != "" || !s.state.KnownShadowBolt || s.coldridgeLeveling.observedLevel >= 5 || s.coldridgeLeveling.invalid || !goal.baselineContains(playerPosition(w.Self)) || !s.refreshPositionLocked(w) || !goal.contains(s.pos) || s.navDataDir == "" || s.planColdridge182Path == nil {
		return nil, status.Error(codes.FailedPrecondition, "actual own level4 warlock,218 context, known Shadow Bolt and verified troll position required")
	}
	if s.coldridgeLevelingRequests == nil {
		s.coldridgeLevelingRequests = make(map[string]struct{})
	}
	s.coldridgeLevelingRequests[r.RequestId] = struct{}{}
	s.coldridgeLeveling = coldridgeLevelingRun{requestID: r.RequestId, until: s.now().Add(coldridgeLevelingWindow), positionRevision: w.Self.PositionRevision, observedLevel: 4}
	if !s.stopAutoAttackLocked(ctx) || !s.stopLocked(ctx) {
		return nil, status.Error(codes.FailedPrecondition, "leveling start stop is unconfirmed; request consumed")
	}
	s.state.Practicing, s.state.PracticePhase = true, "waiting_for_safe_coldridge_level_5_pull"
	s.coldridgeGoal = goal
	s.practiceUntil, s.practiceQuestGoal = s.coldridgeLeveling.until, false
	s.practiceTarget, s.state.PracticeTargetGuid = 0, 0
	s.practiceRoute, s.practicePullStarted, s.failedAssistTarget = nil, false, 0
	s.practiceReplans, s.practiceKillCredited = 0, false
	s.lastStep = s.now()
	return s.stateLocked(), nil
}
