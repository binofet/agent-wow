package buddyloot

import (
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const coldridgeCatchupGoal = "coldridge_706_catchup"
const coldridgeCatchupCrateGUID uint64 = 4611686018427389462
const maxColdridgeCatchupLootRequests = 64

type coldridgeCatchupLootRequest struct {
	sourceGUID              uint64
	session                 string
	startLevel, targetLevel uint32
	until, proofUntil       time.Time
	consumed                bool
	finished                bool
}

// This is a distinct ordinary one-body handoff, not either item quest's credit
// and not the old level4->5 goal. Both complete carried vectors and the actual
// protected delivery crate remain required even when a last store fills space.
func coldridgeCatchupLog(w *worldv1.SnapshotResponse, self uint64) bool {
	if self != 8 || w == nil || w.Self == nil || w.Self.Guid != self || !w.Self.Present || !w.Self.ClassKnown || w.Self.ClassId != 9 || !w.Self.LevelKnown || w.Self.Level < 5 || w.Self.Level > 8 || !w.SelfQuestLogKnown || len(w.SelfQuestLog) != 25 || !coldridgeCatchupInventory(w) {
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
	return quests[218] && quests[2160]
}

func coldridgeCatchupInventory(w *worldv1.SnapshotResponse) bool {
	if w == nil || !w.InventoryKnown || w.InventoryRevision == 0 || len(w.InventoryBags) != 5 {
		return false
	}
	bags, guids := [5]bool{}, map[uint64]bool{}
	crate, total := 0, 0
	validGUID := func(g uint64) bool { return g>>48 == 0x4000 && uint32(g) != 0 }
	for _, b := range w.InventoryBags {
		if b == nil || b.Bag > 4 || bags[b.Bag] || !b.SlotKnown || !b.ContentsKnown || b.SlotCount > 36 || len(b.Slots) != int(b.SlotCount) {
			return false
		}
		bags[b.Bag] = true
		if b.Bag == 0 {
			if b.ContainerGuid != 0 || b.Entry != 0 || b.MetadataKnown || b.SlotCount != 16 {
				return false
			}
		} else if b.ContainerGuid == 0 {
			if b.Entry != 0 || b.MetadataKnown || b.SlotCount != 0 {
				return false
			}
		} else {
			if !validGUID(b.ContainerGuid) || guids[b.ContainerGuid] || !b.MetadataKnown || b.Entry == 0 || b.SlotCount == 0 || b.Entry == 41599 && b.SlotCount != 20 {
				return false
			}
			guids[b.ContainerGuid] = true
		}
		total += len(b.Slots)
		if total > 160 {
			return false
		}
		slots := [36]bool{}
		for _, item := range b.Slots {
			if item == nil || item.Bag != b.Bag || item.Slot >= b.SlotCount || slots[item.Slot] {
				return false
			}
			slots[item.Slot] = true
			if item.ItemGuid == 0 {
				if item.Entry != 0 || item.StackCount != 0 || item.MetadataKnown {
					return false
				}
				continue
			}
			if !validGUID(item.ItemGuid) || guids[item.ItemGuid] || !item.MetadataKnown || item.Entry == 0 || item.StackCount == 0 {
				return false
			}
			guids[item.ItemGuid] = true
			if item.Entry == 7646 {
				if item.ItemGuid != coldridgeCatchupCrateGUID || item.StackCount != 1 {
					return false
				}
				crate++
			}
		}
	}
	return crate == 1
}

func coldridgeCatchupReceipt(w *worldv1.SnapshotResponse, a *actionsv1.State, self uint64, session string) bool {
	if !coldridgeCatchupLog(w, self) || a == nil || a.ColdridgeCatchup == nil || a.ColdridgeLeveling != nil || a.RoadClear != nil || session == "" || len(session) > 128 {
		return false
	}
	r := a.ColdridgeCatchup
	return validRoadClearRequestID(r.RequestId) && r.Goal == coldridgeCatchupGoal && r.StartLevel >= 5 && r.StartLevel <= 7 && r.TargetLevel == r.StartLevel+1 && r.MaxPulls == 1 && r.PullReserved && r.RemainingMs > 0 && r.RemainingMs <= 180000 && r.SelfGuid == self && r.SessionId == session && r.SourceGuid == r.TargetGuid && r.SourceGuid>>48 == 0xf130 && r.SourceGuid&0xffffff != 0 && uint32(r.SourceGuid>>24&0xffffff) == 706 && r.PositionRevision > 0 && r.PositionRevision == w.Self.PositionRevision && r.ObservedLevel == w.Self.Level && r.ObservedLevel >= r.StartLevel && r.ObservedLevel <= r.TargetLevel && r.TargetLevelObserved == (r.ObservedLevel == r.TargetLevel)
}

func coldridgeCatchupContext(w *worldv1.SnapshotResponse, a *actionsv1.State, self uint64, session string) bool {
	if !coldridgeCatchupReceipt(w, a, self, session) || a.MotionUnconfirmed || a.CastPending || a.AutoAttacking || a.AutoAttackRequested || a.AutoAttackUnconfirmed || !safeOwnQuestLootContext(w, a.Position) {
		return false
	}
	if !w.Self.ChannelSpellKnown || w.Self.ChannelSpellId != 0 || !w.Self.ChannelObjectKnown || w.Self.ChannelObjectGuid != 0 || w.Self.UnitFlags&0x2000 != 0 {
		return false
	}
	g := coldridgeLootGoal{catchupRequest: a.ColdridgeCatchup.RequestId}
	return g.baselineContainsPoint(w.Self.X, w.Self.Y, w.Self.Z) && g.containsPoint(a.Position.X, a.Position.Y, a.Position.Z)
}

func selectColdridgeCatchupLootGoal(w *worldv1.SnapshotResponse, a *actionsv1.State, self uint64, session string, c *worldv1.CreatureState) (coldridgeLootGoal, bool) {
	if !coldridgeCatchupContext(w, a, self, session) || c == nil || c.Guid != a.ColdridgeCatchup.SourceGuid {
		return coldridgeLootGoal{}, false
	}
	r := a.ColdridgeCatchup
	g := coldridgeLootGoal{corpseGUID: c.Guid, positionRevision: w.Self.PositionRevision, catchupRequest: r.RequestId, catchupSession: session, catchupStart: r.StartLevel, catchupTarget: r.TargetLevel, catchupObserved: w.Self.Level}
	return g, g.matchesCorpse(c)
}

func (g *coldridgeLootGoal) catchupContext(w *worldv1.SnapshotResponse, a *actionsv1.State, self, pending uint64) bool {
	if g.questID != 0 || g.levelingRequest != "" || g.roadRequest != "" || pending == 0 || pending != g.corpseGUID || !coldridgeCatchupContext(w, a, self, g.catchupSession) {
		return false
	}
	r := a.ColdridgeCatchup
	if r.RequestId != g.catchupRequest || r.SourceGuid != g.corpseGUID || r.StartLevel != g.catchupStart || r.TargetLevel != g.catchupTarget || w.Self.PositionRevision != g.positionRevision || w.Self.Level < g.catchupObserved || len(w.LootableCorpses) > 64 {
		return false
	}
	seen := false
	for _, c := range w.LootableCorpses {
		if c != nil && c.Guid == pending {
			if seen || c.Present && (!g.matchesCorpse(c) || c.AliveKnown && c.Alive || c.HealthKnown && c.Health != 0) {
				return false
			}
			seen = true
		}
	}
	g.catchupObserved = w.Self.Level
	// Eviction cannot replace the source of an already owned window.
	return true
}

// Reserve the body/request at its first selected handoff. STATUS latency spends
// the allowance; new observations can only shrink it. Neither enable nor finish
// clears these bounded session-local reservations.
func (s *Service) bindCatchupLootDeadline(g *coldridgeLootGoal, before time.Time, ms uint64) bool {
	if g == nil || g.catchupRequest == "" || ms == 0 || ms > 180000 {
		return false
	}
	if s.catchupRequests == nil {
		s.catchupRequests = map[string]coldridgeCatchupLootRequest{}
	}
	r, exists := s.catchupRequests[g.catchupRequest]
	if !exists {
		if len(s.catchupRequests) >= maxColdridgeCatchupLootRequests {
			return false
		}
		for _, old := range s.catchupRequests {
			if old.sourceGUID == g.corpseGUID {
				return false
			}
		}
		until := before.Add(time.Duration(ms) * time.Millisecond)
		r = coldridgeCatchupLootRequest{sourceGUID: g.corpseGUID, session: g.catchupSession, startLevel: g.catchupStart, targetLevel: g.catchupTarget, until: until, proofUntil: until}
		s.catchupRequests[g.catchupRequest] = r
	}
	if r.consumed || r.finished || r.sourceGUID != g.corpseGUID || r.session != g.catchupSession || r.startLevel != g.catchupStart || r.targetLevel != g.catchupTarget || !s.now().Before(r.until) || !s.now().Before(r.proofUntil) {
		return false
	}
	g.catchupUntil = r.until
	return true
}

func (s *Service) noteCatchupLootTTL(before time.Time, a *actionsv1.State) {
	g := &s.coldridgeGoal
	if g.catchupRequest == "" || a == nil || a.ColdridgeCatchup == nil {
		return
	}
	p := a.ColdridgeCatchup
	r, exists := s.catchupRequests[g.catchupRequest]
	if !exists || p.RequestId != g.catchupRequest || p.SourceGuid != r.sourceGUID || p.TargetGuid != r.sourceGUID || p.SessionId != r.session || p.StartLevel != r.startLevel || p.TargetLevel != r.targetLevel || p.RemainingMs > 180000 {
		return
	}
	until := before.Add(time.Duration(p.RemainingMs) * time.Millisecond)
	if until.Before(r.proofUntil) {
		r.proofUntil = until
		s.catchupRequests[g.catchupRequest] = r
	}
}

func (s *Service) catchupPacketAllowed(op uint32) bool {
	g := &s.coldridgeGoal
	if !s.questColdridge || g.catchupRequest == "" || op != opcode.CMSGLoot && op != opcode.CMSGLootMoney && op != opcode.CMSGAutostoreLootItem {
		return true
	}
	r, exists := s.catchupRequests[g.catchupRequest]
	if !exists || r.finished || r.sourceGUID != g.corpseGUID || r.session != s.state.SessionId || r.session != g.catchupSession || r.startLevel != g.catchupStart || r.targetLevel != g.catchupTarget || g.catchupUntil.IsZero() || !r.until.Equal(g.catchupUntil) || !s.now().Before(r.until) || !s.now().Before(r.proofUntil) {
		s.state.Reason = "coldridge_catchup_deadline_expired_no_further_loot_writes"
		return false
	}
	if op == opcode.CMSGLoot {
		if r.consumed {
			s.state.Reason = "coldridge_catchup_handoff_already_consumed"
			return false
		}
		r.consumed = true // before a potentially ambiguous native OPEN
		s.catchupRequests[g.catchupRequest] = r
		return true
	}
	if !r.consumed {
		s.state.Reason = "coldridge_catchup_window_has_no_consumed_open"
		return false
	}
	return true
}

func (s *Service) finishCatchupLootRequest(id string) {
	if id == "" {
		return
	}
	s.state.Enabled = false
	if r, ok := s.catchupRequests[id]; ok {
		r.finished = true
		s.catchupRequests[id] = r
	}
}
