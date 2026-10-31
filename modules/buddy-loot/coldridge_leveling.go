package buddyloot

import (
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const coldridgeLevelingGoal = "coldridge_706_to_level_5"

const maxColdridgeLevelingLootRequests = 64

type coldridgeLevelingLootRequest struct {
	sourceGUID uint64
	until      time.Time
	proofUntil time.Time
	consumed   bool
}

// Anchor before STATUS, rather than its arrival: dependency latency spends the
// server's remaining allowance. A later observation can never renew this ID.
func (s *Service) bindLevelingLootDeadline(g *coldridgeLootGoal, beforeStatus time.Time, remainingMS uint64) bool {
	if g == nil || g.levelingRequest == "" || remainingMS == 0 || remainingMS > 180000 {
		return false
	}
	if s.levelingRequests == nil {
		s.levelingRequests = make(map[string]coldridgeLevelingLootRequest)
	}
	r, exists := s.levelingRequests[g.levelingRequest]
	if !exists {
		if len(s.levelingRequests) >= maxColdridgeLevelingLootRequests {
			return false
		}
		until := beforeStatus.Add(time.Duration(remainingMS) * time.Millisecond)
		r = coldridgeLevelingLootRequest{sourceGUID: g.corpseGUID, until: until, proofUntil: until}
		s.levelingRequests[g.levelingRequest] = r
	}
	if r.consumed || r.sourceGUID != g.corpseGUID || !s.now().Before(r.until) || !s.now().Before(r.proofUntil) {
		return false
	}
	g.levelingUntil = r.until
	return true
}

// A newer actions receipt may shorten its authority, never replenish the
// original deadline. Include that actions RPC's latency in the shorter proof.
func (s *Service) noteLevelingLootTTL(before time.Time, a *actionsv1.State) {
	g := &s.coldridgeGoal
	if g.levelingRequest == "" || a == nil || a.ColdridgeLeveling == nil {
		return
	}
	p := a.ColdridgeLeveling
	r, exists := s.levelingRequests[g.levelingRequest]
	if !exists || p.RequestId != g.levelingRequest || p.SourceGuid != r.sourceGUID || p.RemainingMs > 180000 {
		return
	}
	until := before.Add(time.Duration(p.RemainingMs) * time.Millisecond)
	if until.Before(r.proofUntil) {
		r.proofUntil = until
		s.levelingRequests[g.levelingRequest] = r
	}
}

// This is the last local guard before SendPacket. Only OPEN consumes an ID;
// its owned window can continue under the original deadline. RELEASE remains
// permitted for bounded cleanup even when that deadline has expired.
func (s *Service) levelingPacketAllowed(op uint32) bool {
	g := &s.coldridgeGoal
	if !s.questColdridge || g.levelingRequest == "" || op != opcode.CMSGLoot && op != opcode.CMSGLootMoney && op != opcode.CMSGAutostoreLootItem {
		return true
	}
	r, exists := s.levelingRequests[g.levelingRequest]
	if !exists || r.sourceGUID != g.corpseGUID || g.levelingUntil.IsZero() || !r.until.Equal(g.levelingUntil) || !s.now().Before(g.levelingUntil) || !s.now().Before(r.proofUntil) {
		s.state.Reason = "coldridge_leveling_local_deadline_expired_no_further_loot_writes"
		return false
	}
	if op == opcode.CMSGLoot {
		if r.consumed {
			s.state.Reason = "coldridge_leveling_handoff_already_consumed"
			return false
		}
		r.consumed = true // reserve before the first potentially ambiguous OPEN
		s.levelingRequests[g.levelingRequest] = r
		return true
	}
	if !r.consumed {
		s.state.Reason = "coldridge_leveling_window_has_no_consumed_open"
		return false
	}
	return true
}

// This is a finite exact-source handoff, not quest182 or quest218 kill credit.
// The sole active quest218 remains an item objective with four zero counters.
func coldridgeLevelingLog(w *worldv1.SnapshotResponse, self uint64) bool {
	if self == 0 || self>>48 != 0 || w == nil || w.Self == nil || w.Self.Guid != self || !w.Self.Present || !w.Self.ClassKnown || w.Self.ClassId != 9 || !w.Self.LevelKnown || w.Self.Level < 4 || w.Self.Level > 5 || !w.SelfQuestLogKnown || len(w.SelfQuestLog) != 25 {
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
		if q.QuestId != 218 || found || q.Failed || q.State&3 != 0 || q.Complete || len(q.ObjectiveCounts) != 4 {
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

func coldridgeLevelingReceipt(w *worldv1.SnapshotResponse, a *actionsv1.State, self uint64) bool {
	if !coldridgeLevelingLog(w, self) || a == nil || a.ColdridgeLeveling == nil {
		return false
	}
	r := a.ColdridgeLeveling
	return len(r.RequestId) >= 1 && len(r.RequestId) <= 64 && r.Goal == coldridgeLevelingGoal && r.TargetLevel == 5 && r.MaxPulls == 1 && r.PullReserved && r.RemainingMs > 0 && r.RemainingMs <= 180000 && r.SelfGuid == self && r.SourceGuid>>48 == 0xf130 && r.SourceGuid&0xffffff != 0 && uint32(r.SourceGuid>>24&0xffffff) == 706 && r.PositionRevision > 0 && r.PositionRevision == w.Self.PositionRevision && r.ObservedLevel == w.Self.Level && r.LevelFiveObserved == (w.Self.Level == 5)
}

func coldridgeLevelingContext(w *worldv1.SnapshotResponse, a *actionsv1.State, self uint64) bool {
	if !coldridgeLevelingReceipt(w, a, self) || !safeOwnQuestLootContext(w, a.Position) {
		return false
	}
	g := coldridgeLootGoal{levelingRequest: a.ColdridgeLeveling.RequestId}
	return g.baselineContainsPoint(w.Self.X, w.Self.Y, w.Self.Z) && g.containsPoint(a.Position.X, a.Position.Y, a.Position.Z)
}

func selectColdridgeLootGoalWithActions(w *worldv1.SnapshotResponse, a *actionsv1.State, self uint64, c *worldv1.CreatureState) (coldridgeLootGoal, bool) {
	if a != nil && a.ColdridgeLeveling != nil {
		if coldridgeLevelingContext(w, a, self) && c != nil && c.Guid == a.ColdridgeLeveling.SourceGuid && w.Self.Level >= c.Level {
			g := coldridgeLootGoal{corpseGUID: c.Guid, positionRevision: w.Self.PositionRevision, levelingRequest: a.ColdridgeLeveling.RequestId, levelingLevel: c.Level, levelingMaxHP: c.MaxHealth}
			if g.matchesCorpse(c) {
				return g, true
			}
		}
		return coldridgeLootGoal{}, false
	}
	return selectColdridgeLootGoal(w, self, c)
}

// All production callers supply the current actions response. Legacy quest
// validation stays unchanged; leveling never falls through to a quest or owner.
func (g *coldridgeLootGoal) contextWithActions(w *worldv1.SnapshotResponse, a *actionsv1.State, self uint64, pendingGUID uint64) bool {
	if g == nil || a == nil {
		return false
	}
	if g.levelingRequest == "" {
		return g.context(w, a.Position, self, pendingGUID)
	}
	if g.questID != 0 || g.corpseGUID == 0 || pendingGUID != g.corpseGUID || !coldridgeLevelingContext(w, a, self) || a.ColdridgeLeveling.RequestId != g.levelingRequest || a.ColdridgeLeveling.SourceGuid != g.corpseGUID || w.Self.PositionRevision != g.positionRevision || w.Self.Level < g.levelingLevel || len(w.LootableCorpses) > 64 {
		return false
	}
	seen := false
	for _, c := range w.LootableCorpses {
		if c != nil && c.Guid == g.corpseGUID {
			if seen || c.Present && !g.matchesCorpse(c) {
				return false
			}
			seen = true
		}
	}
	// An owned loot window may outlive discovery of its still-bound source.
	return true
}
