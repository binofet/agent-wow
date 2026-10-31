package buddyactions

import (
	"context"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const coldridgeCatchupGoalName = "coldridge_706_catchup"
const coldridgeCatchupWindow = 3 * time.Minute
const coldridgeCatchupCrateGUID uint64 = 4611686018427389462

// This separate record never changes the old level4->5 request/history or goal.
// A source is published only after reserving its first potentially sent offense.
type coldridgeCatchupRun struct {
	request                                                   *actionsv1.ColdridgeCatchupRequest
	until                                                     time.Time
	startLevel, observedLevel                                 uint32
	targetGUID, sourceGUID, positionRevision                  uint64
	pullReserved, targetLevelObserved, invalid, castAttempted bool
}

// Both actually accepted item quests are context, never a706 kill objective.
func ownColdridgeCatchupContext(w *worldv1.SnapshotResponse, self uint64) bool {
	if self != 8 || w == nil || w.Self == nil || w.Self.Guid != self || !w.Self.Present || !w.Self.MapKnown || w.Self.MapId != 0 || !w.Self.PositionKnown || w.Self.PositionRevision == 0 || !w.Self.ClassKnown || w.Self.ClassId != 9 || !w.Self.LevelKnown || w.Self.Level < 5 || w.Self.Level > 8 || !w.SelfQuestLogKnown || len(w.SelfQuestLog) != 25 {
		return false
	}
	if !w.Self.ChannelSpellKnown || w.Self.ChannelSpellId != 0 || !w.Self.ChannelObjectKnown || w.Self.ChannelObjectGuid != 0 {
		return false
	}
	slots := [25]bool{}
	quests := map[uint32]bool{}
	for _, q := range w.SelfQuestLog {
		if q == nil || q.Slot >= 25 || slots[q.Slot] {
			return false
		}
		slots[q.Slot] = true
		if q.QuestId == 0 {
			if q.Failed || q.Complete || q.State != 0 || len(q.ObjectiveCounts) != 0 && len(q.ObjectiveCounts) != 4 {
				return false
			}
			for _, n := range q.ObjectiveCounts {
				if n != 0 {
					return false
				}
			}
			continue
		}
		if quests[q.QuestId] || q.Failed || len(q.ObjectiveCounts) != 4 {
			return false
		}
		quests[q.QuestId] = true
		for _, n := range q.ObjectiveCounts {
			if n != 0 {
				return false
			}
		}
		switch q.QuestId {
		case 218:
			if q.Complete || q.State != 0 {
				return false
			}
		case 2160:
			if !q.Complete || q.State != 1 {
				return false
			}
		default:
			return false
		}
	}
	valid, _ := catchupInventory(w)
	return quests[218] && quests[2160] && valid
}

// Complete known carried vectors preserve the actual delivery crate. Free space
// is required before a new pull, not during last-slot loot release cleanup.
func catchupInventory(w *worldv1.SnapshotResponse) (bool, bool) {
	if w == nil || !w.InventoryKnown || w.InventoryRevision == 0 || len(w.InventoryBags) != 5 {
		return false, false
	}
	bags := [5]bool{}
	guids := map[uint64]bool{}
	crate, free, total := 0, false, 0
	validGUID := func(g uint64) bool { return g>>48 == 0x4000 && uint32(g) != 0 }
	for _, b := range w.InventoryBags {
		if b == nil || b.Bag > 4 || bags[b.Bag] || !b.SlotKnown || !b.ContentsKnown || b.SlotCount > 36 || len(b.Slots) != int(b.SlotCount) {
			return false, false
		}
		bags[b.Bag] = true
		if b.Bag == 0 {
			if b.ContainerGuid != 0 || b.Entry != 0 || b.MetadataKnown || b.SlotCount != 16 {
				return false, false
			}
		} else if b.ContainerGuid == 0 {
			if b.Entry != 0 || b.MetadataKnown || b.SlotCount != 0 {
				return false, false
			}
		} else {
			if !validGUID(b.ContainerGuid) || guids[b.ContainerGuid] || !b.MetadataKnown || b.Entry == 0 || b.SlotCount == 0 || b.Entry == 41599 && b.SlotCount != 20 {
				return false, false
			}
			guids[b.ContainerGuid] = true
		}
		total += len(b.Slots)
		if total > 160 {
			return false, false
		}
		slots := [36]bool{}
		for _, item := range b.Slots {
			if item == nil || item.Bag != b.Bag || item.Slot >= b.SlotCount || slots[item.Slot] {
				return false, false
			}
			slots[item.Slot] = true
			if item.ItemGuid == 0 {
				if item.Entry != 0 || item.StackCount != 0 || item.MetadataKnown {
					return false, false
				}
				if b.Bag == 0 || b.Entry == 41599 && b.SlotCount == 20 {
					free = true
				}
				continue
			}
			if !validGUID(item.ItemGuid) || guids[item.ItemGuid] || !item.MetadataKnown || item.Entry == 0 || item.StackCount == 0 {
				return false, false
			}
			guids[item.ItemGuid] = true
			if item.Entry == 7646 {
				if item.ItemGuid != coldridgeCatchupCrateGUID || item.StackCount != 1 {
					return false, false
				}
				crate++
			}
		}
	}
	return crate == 1, free
}

func catchupProfile(c *worldv1.CreatureState) bool {
	return matches182Profile(c) && c.Level == 3 && c.MaxHealth == 71
}
func catchupFreshPull(c *worldv1.CreatureState) bool {
	// These are retained server-cache ages, not the age of this RPC response.
	return c != nil && c.PositionKnown && c.PositionRevision > 0 && c.PositionAgeMs <= 30000 && c.ObservationAgeMs <= 30000
}
func (s *Service) coldridgeCatchupActiveLocked() bool {
	return s.state.Practicing && s.coldridgeGoal.catchup
}
func (s *Service) observeColdridgeCatchupLevelLocked(w *worldv1.SnapshotResponse) {
	r := &s.coldridgeCatchup
	if r.request == nil {
		return
	}
	if w == nil || w.Self == nil || w.Self.Guid != s.selfGUID || !w.Self.Present || !w.Self.ClassKnown || w.Self.ClassId != 9 || !w.Self.LevelKnown || w.Self.Level < r.startLevel || w.Self.Level < r.observedLevel || w.Self.Level > r.startLevel+1 {
		r.invalid = true
		return
	}
	r.observedLevel = w.Self.Level
	if w.Self.Level == r.startLevel+1 {
		r.targetLevelObserved = true
	}
}
func (s *Service) coldridgeCatchupRunValidLocked() bool {
	r := &s.coldridgeCatchup
	return r.request != nil && !r.invalid && s.now().Before(r.until)
}
func (s *Service) coldridgeCatchupSnapshotLocked() *actionsv1.ColdridgeCatchupState {
	r := &s.coldridgeCatchup
	if r.request == nil || !s.coldridgeGoal.catchup {
		return nil
	}
	s.observeColdridgeCatchupLevelLocked(s.lastWorld)
	remaining := uint64(0)
	now := s.now()
	if !r.invalid && now.Before(r.until) {
		remaining = uint64(r.until.Sub(now).Milliseconds())
	}
	return &actionsv1.ColdridgeCatchupState{RequestId: r.request.RequestId, Goal: coldridgeCatchupGoalName, StartLevel: r.startLevel, TargetLevel: r.startLevel + 1, MaxPulls: 1, SourceGuid: r.sourceGUID, TargetGuid: r.targetGUID, PositionRevision: r.positionRevision, PullReserved: r.pullReserved, RemainingMs: remaining, ObservedLevel: r.observedLevel, TargetLevelObserved: r.targetLevelObserved, SelfGuid: s.selfGUID, SessionId: r.request.SessionId}
}
func (s *Service) reserveColdridgeCatchupPullLocked(w *worldv1.SnapshotResponse, guid uint64) bool {
	r := &s.coldridgeCatchup
	s.observeColdridgeCatchupLevelLocked(w)
	if !s.coldridgeCatchupActiveLocked() || !s.coldridgeCatchupRunValidLocked() || r.pullReserved || guid != r.targetGUID || guid != s.practiceTarget || !ownColdridgeCatchupContext(w, s.selfGUID) || w.Self.Level != r.startLevel || w.Self.PositionRevision != r.positionRevision {
		return false
	}
	c := practiceTargetObservation(w, guid)
	_, free := catchupInventory(w)
	if !free || !catchupFreshPull(c) || !unclaimedColdridgePull(c) || c.Health != c.MaxHealth || !s.coldridgeGoal.eligible(c, w.Self) || !s.coldridgeGoal.positionEvidenceWithin(c) {
		return false
	}
	r.sourceGUID, r.pullReserved = guid, true
	return true
}
func (s *Service) coldridgeCatchupCastAllowedLocked() bool {
	r, w := &s.coldridgeCatchup, s.lastWorld
	s.observeColdridgeCatchupLevelLocked(w)
	if !s.coldridgeCatchupActiveLocked() || !s.coldridgeCatchupRunValidLocked() || !r.pullReserved || r.observedLevel != r.startLevel || !ownColdridgeCatchupContext(w, s.selfGUID) || safePracticeCharacter(w, s.selfGUID) != "" {
		return false
	}
	self := w.Self
	if self.Level != r.startLevel || !self.HealthKnown || self.Health == 0 || !self.PowerKnown || !self.BaseManaKnown || self.BaseMana == 0 || self.PowerType != 0 || uint64(self.Power) < manaBudget(self, 10) || !self.SpeedsKnown || math.Abs(float64(self.RunSpeed)-7) > .01 || self.PositionRevision != r.positionRevision || self.UnitFlags&0x2000 != 0 || self.ChannelSpellId != 0 || self.ChannelObjectGuid != 0 {
		return false
	}
	c := practiceTargetObservation(w, r.sourceGUID)
	p, known, _ := creaturePosition(c)
	if !known || distance(s.pos, p) > 25 || !s.coldridgeGoal.contains(s.pos) || !s.coldridgeGoal.baselineContains(playerPosition(self)) {
		return false
	}
	allowed := s.practicePullStarted && r.sourceGUID == r.targetGUID && s.practiceTarget == r.sourceGUID && s.pendingTarget == r.sourceGUID && s.pendingSpell == shadowBolt && s.coldridgeGoal.eligible(c, self) && s.coldridgeGoal.positionEvidenceWithin(c) && (!c.AttackVictimKnown || c.AttackVictimGuid == s.selfGUID) && (c.DynamicFlags&4 == 0 || c.DynamicFlags&8 != 0)
	if !allowed || !r.castAttempted && (!catchupFreshPull(c) || !unclaimedColdridgePull(c) || c.Health != c.MaxHealth) {
		return false
	}
	r.castAttempted = true // reserve before a potentially ambiguous actual CAST write
	return true
}
func (s *Service) coldridgeCatchupMovementAllowedLocked(w *worldv1.SnapshotResponse, p position) bool {
	r := &s.coldridgeCatchup
	if !s.coldridgeGoal.catchup || !s.state.Practicing && (s.state.InteractionOwner != "loot" || !r.pullReserved) {
		return true
	}
	s.observeColdridgeCatchupLevelLocked(w)
	if !s.coldridgeCatchupRunValidLocked() || !ownColdridgeCatchupContext(w, s.selfGUID) {
		return false
	}
	safety := safeQuestCharacter(w, s.selfGUID)
	if s.state.Practicing && s.practicePullStarted {
		safety = safePracticeCharacter(w, s.selfGUID)
	}
	self := w.Self
	return safety == "" && self.HealthKnown && self.Health > 0 && self.SpeedsKnown && math.Abs(float64(self.RunSpeed)-7) <= .01 && self.UnitFlags&0x2000 == 0 && (!self.ChannelSpellKnown || self.ChannelSpellId == 0) && (!self.ChannelObjectKnown || self.ChannelObjectGuid == 0) && self.PositionRevision == r.positionRevision && (!s.state.Practicing || self.Level == r.startLevel) && s.coldridgeGoal.baselineContains(playerPosition(self)) && s.coldridgeGoal.contains(s.pos) && s.coldridgeGoal.contains(p)
}
func (s *Service) coldridgeCatchupCorpseContextLocked(w *worldv1.SnapshotResponse, c *worldv1.CreatureState) bool {
	r := &s.coldridgeCatchup
	s.observeColdridgeCatchupLevelLocked(w)
	return s.coldridgeCatchupRunValidLocked() && r.pullReserved && r.sourceGUID != 0 && ownColdridgeCatchupContext(w, s.selfGUID) && w.Self.PositionRevision == r.positionRevision && c != nil && c.Guid == r.sourceGUID && catchupProfile(c) && s.coldridgeGoal.baselineContains(playerPosition(w.Self)) && s.coldridgeGoal.contains(s.pos) && s.coldridgeGoal.positionEvidenceWithin(c)
}

func (s *Service) StartColdridgeCatchup(ctx context.Context, q *actionsv1.ColdridgeCatchupRequest) (*actionsv1.State, error) {
	if q == nil || !validColdridgeLevelingRequestID(q.RequestId) || q.ExpectedStartLevel < 5 || q.ExpectedStartLevel > 7 || q.SessionId == "" || len(q.SessionId) > 128 || q.TargetGuid>>48 != 0xf130 || q.TargetGuid&0xffffff == 0 || uint32(q.TargetGuid>>24&0xffffff) != 706 {
		return nil, status.Error(codes.InvalidArgument, "explicit bounded catch-up request/session/level/706 required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.independent || s.selfGUID != 8 {
		return nil, status.Error(codes.FailedPrecondition, "own independent Nixwick session required")
	}
	if old, seen := s.coldridgeCatchupRequests[q.RequestId]; seen {
		if !proto.Equal(old, q) || s.coldridgeCatchup.request == nil || s.coldridgeCatchup.request.RequestId != q.RequestId {
			return nil, status.Error(codes.FailedPrecondition, "catch-up request already consumed or changed")
		}
		return s.stateLocked(), nil
	}
	if len(s.coldridgeCatchupRequests) >= 64 || s.coldridgeCatchupBodies[q.TargetGuid] || s.road.active || s.state.Practicing || s.state.Following || s.state.Healing || s.state.Assisting || s.state.Traveling || s.questApproaching || s.state.InteractionOwner != "" || s.state.CastPending || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed || s.state.AutoAttacking || s.autoTarget != 0 || s.recovery.snapshot(s.now()).Active {
		return nil, status.Error(codes.FailedPrecondition, "unused source and confirmed idle catch-up actions required")
	}
	begin := s.now()
	if !s.roadSessionLocked(ctx, q.SessionId) {
		return nil, status.Error(codes.FailedPrecondition, "current own session required")
	}
	w, err := s.snapshot(ctx, q.TargetGuid)
	g := coldridgePracticeGoal{catchup: true, startLevel: q.ExpectedStartLevel, targetGUID: q.TargetGuid}
	if err != nil || !ownColdridgeCatchupContext(w, s.selfGUID) || w.Self.Level != q.ExpectedStartLevel || safeQuestCharacter(w, s.selfGUID) != "" || !w.Self.HealthKnown || w.Self.Health != w.Self.MaxHealth || w.Self.MaxHealth == 0 || !w.Self.PowerKnown || w.Self.PowerType != 0 || w.Self.Power != w.Self.MaxPower || w.Self.MaxPower == 0 || !w.Self.BaseManaKnown || w.Self.BaseMana == 0 || !s.state.KnownShadowBolt || !g.baselineContains(playerPosition(w.Self)) || !s.refreshPositionLocked(w) || !g.contains(s.pos) || s.navDataDir == "" || s.planColdridge182Path == nil {
		return nil, status.Error(codes.FailedPrecondition, "actual idle full-resource level5..7 own context and measured troll staging required")
	}
	c := practiceTargetObservation(w, q.TargetGuid)
	_, free := catchupInventory(w)
	if len(w.NearbyCreatures) >= 32 || !free || !g.eligible(c, w.Self) || !unclaimedColdridgePull(c) || c.Health != c.MaxHealth || !catchupFreshPull(c) || !g.positionEvidenceWithin(c) {
		return nil, status.Error(codes.FailedPrecondition, "exact fresh unclaimed observed level3/71/190706 and free known carried space required")
	}
	if s.coldridgeCatchupRequests == nil {
		s.coldridgeCatchupRequests = map[string]*actionsv1.ColdridgeCatchupRequest{}
	}
	if s.coldridgeCatchupBodies == nil {
		s.coldridgeCatchupBodies = map[uint64]bool{}
	}
	request := proto.Clone(q).(*actionsv1.ColdridgeCatchupRequest)
	s.coldridgeCatchupRequests[q.RequestId] = request
	s.coldridgeCatchupBodies[q.TargetGuid] = true
	s.coldridgeCatchup = coldridgeCatchupRun{request: request, until: begin.Add(coldridgeCatchupWindow), startLevel: q.ExpectedStartLevel, observedLevel: q.ExpectedStartLevel, targetGUID: q.TargetGuid, positionRevision: w.Self.PositionRevision}
	s.coldridgeGoal = g
	s.roadHideLocked()
	if !s.stopAutoAttackLocked(ctx) || !s.stopLocked(ctx) {
		s.coldridgeCatchup.invalid = true
		return nil, status.Error(codes.FailedPrecondition, "catch-up start stop unconfirmed; request/source consumed")
	}
	if !s.now().Before(s.coldridgeCatchup.until) {
		s.coldridgeCatchup.invalid = true
		return nil, status.Error(codes.FailedPrecondition, "catch-up admission deadline spent; request/source consumed")
	}
	s.state.Practicing, s.state.PracticePhase = true, "waiting_for_selected_coldridge_catchup_pull"
	s.practiceUntil, s.practiceQuestGoal = s.coldridgeCatchup.until, false
	s.practiceTarget, s.state.PracticeTargetGuid = q.TargetGuid, q.TargetGuid
	s.practiceRoute, s.practicePullStarted, s.failedAssistTarget = nil, false, 0
	s.practiceReplans, s.practiceKillCredited = 0, false
	s.lastStep = s.now()
	return s.stateLocked(), nil
}
