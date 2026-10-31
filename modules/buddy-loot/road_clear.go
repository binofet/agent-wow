package buddyloot

import (
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const roadClearGoal = "normal_1718_road_clear"

type roadLootRequest struct {
	sourceGUID      uint64
	until           time.Time
	proofUntil      time.Time
	consumed        bool
	observedPackets uint64
	causalBroken    bool
}

// A causal epoch cannot be repaired by polling or another Enable. This private
// record survives an unopened operation's finish and never governs706 loot.
func roadRequestEpoch(r *roadLootRequest, w *worldv1.SnapshotResponse) bool {
	if r == nil || r.causalBroken {
		return false
	}
	if w == nil || w.MalformedPackets != 0 || w.ObservedPackets == 0 || w.ObservedPackets < r.observedPackets {
		r.causalBroken = true
		return false
	}
	r.observedPackets = w.ObservedPackets
	return true
}

func (s *Service) roadPreflightEpoch(w *worldv1.SnapshotResponse, a *actionsv1.RoadClearState, beforeStatus time.Time) bool {
	if a == nil || s.selfGUID != 8 || a.SelfGuid != 8 || s.state.SessionId == "" || a.SessionId != s.state.SessionId || a.Goal != roadClearGoal || !validRoadClearRequestID(a.RequestId) || !a.PullReserved || a.TargetGuid != a.SourceGuid || a.SourceGuid>>48 != 0xf130 || a.SourceGuid&0xffffff == 0 || uint32(a.SourceGuid>>24&0xffffff) != 1718 || a.RemainingMs == 0 || a.RemainingMs > 180000 {
		return false
	}
	r, exists := s.roadRequests[a.RequestId]
	if !exists {
		if len(s.roadRequests) >= 64 {
			return false
		}
		for _, previous := range s.roadRequests {
			if previous != nil && previous.sourceGUID == a.SourceGuid && (previous.consumed || previous.causalBroken) {
				return false
			}
		}
		if s.roadRequests == nil {
			s.roadRequests = make(map[string]*roadLootRequest)
		}
		until := beforeStatus.Add(time.Duration(a.RemainingMs) * time.Millisecond)
		r = &roadLootRequest{sourceGUID: a.SourceGuid, until: until, proofUntil: until}
		s.roadRequests[a.RequestId] = r
	}
	return r != nil && r.sourceGUID == a.SourceGuid && roadRequestEpoch(r, w)
}

// The road goal is a separate, stationary, one-source operation. These bounds
// never change the Coldridge quest or706 leveling regions.
func roadClearPoint(x, y, z float32) bool {
	return x >= -6260 && x <= -6060 && y >= 25 && y <= 175 && z >= 400 && z <= 450
}

func roadClearProfile(c *worldv1.CreatureState) bool {
	return c != nil && c.Guid>>48 == 0xf130 && c.Guid&0xffffff != 0 && uint32(c.Guid>>24&0xffffff) == 1718 && c.Entry == 1718 && c.MapKnown && c.MapId == 0 && c.LevelKnown && c.FactionTemplate == 59 && (c.Level == 3 && c.MaxHealth == 71 || c.Level == 4 && c.MaxHealth == 86)
}

// A coherent own log is context only. No quest, objective or counter authorizes
// this operation, and a selected death is not asserted to be quest credit.
func roadClearLootLog(w *worldv1.SnapshotResponse, self uint64) bool {
	if self != 8 || w == nil || w.Self == nil || w.Self.Guid != self || !w.Self.Present || !w.Self.ClassKnown || w.Self.ClassId != 9 || !w.Self.LevelKnown || w.Self.Level != 5 || !w.SelfQuestLogKnown || len(w.SelfQuestLog) != 25 {
		return false
	}
	var slots [25]bool
	quests := make(map[uint32]bool)
	for _, q := range w.SelfQuestLog {
		if q == nil || q.Slot >= 25 || slots[q.Slot] || q.QuestId != 0 && (quests[q.QuestId] || len(q.ObjectiveCounts) != 4) || len(q.ObjectiveCounts) != 0 && len(q.ObjectiveCounts) != 4 {
			return false
		}
		slots[q.Slot] = true
		if q.QuestId != 0 {
			quests[q.QuestId] = true
		}
		for _, count := range q.ObjectiveCounts {
			if count > 0xffff || q.QuestId == 0 && count != 0 {
				return false
			}
		}
	}
	return true
}

func validRoadClearRequestID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, c := range []byte(id) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func roadClearLootReceipt(w *worldv1.SnapshotResponse, a *actionsv1.State, self uint64, session string) bool {
	if !roadClearLootLog(w, self) || w.MalformedPackets != 0 || w.ObservedPackets == 0 || a == nil || a.RoadClear == nil || !safeOwnQuestLootContext(w, a.Position) || a.Moving || a.MotionUnconfirmed || a.CastPending || a.AutoAttacking || a.AutoAttackRequested || a.AutoAttackUnconfirmed {
		return false
	}
	if w.Self.MaxHealth == 0 || w.Self.Health > w.Self.MaxHealth || w.Self.UnitFlags&0x2000 != 0 || w.Self.ChannelSpellKnown && w.Self.ChannelSpellId != 0 || w.Self.ChannelObjectKnown && w.Self.ChannelObjectGuid != 0 {
		return false
	}
	for _, v := range []float32{w.Self.X, w.Self.Y, w.Self.Z} {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) || math.Abs(float64(v)) > 40000 {
			return false
		}
	}
	r, p := a.RoadClear, a.RoadClear.Origin
	if !validRoadClearRequestID(r.RequestId) || r.Goal != roadClearGoal || session == "" || r.SessionId != session || r.SelfGuid != self || r.ObservedLevel != 5 || r.MaxPulls != 1 || !r.PullReserved || !r.SourceDeathObserved || r.SourceDeathPositionRevision == 0 || r.RemainingMs == 0 || r.RemainingMs > 180000 || r.TargetGuid != r.SourceGuid || r.SourceGuid>>48 != 0xf130 || r.SourceGuid&0xffffff == 0 || uint32(r.SourceGuid>>24&0xffffff) != 1718 || r.PositionRevision == 0 || r.PositionRevision != w.Self.PositionRevision || r.SelectedEntry != 1718 || r.SelectedFactionTemplate != 59 || !(r.SelectedLevel == 3 && r.SelectedMaxHealth == 71 || r.SelectedLevel == 4 && r.SelectedMaxHealth == 86) {
		return false
	}
	if p == nil || !p.Known || !p.MapKnown || p.MapId != 0 || p.Revision == 0 || a.Position.Revision < p.Revision || !roadClearPoint(p.X, p.Y, p.Z) || !roadClearPoint(a.Position.X, a.Position.Y, a.Position.Z) {
		return false
	}
	d := math.Pow(float64(p.X-a.Position.X), 2) + math.Pow(float64(p.Y-a.Position.Y), 2) + math.Pow(float64(p.Z-a.Position.Z), 2)
	return d <= .05*.05
}

func selectRoadClearLootGoal(w *worldv1.SnapshotResponse, a *actionsv1.State, self uint64, session string, c *worldv1.CreatureState) (coldridgeLootGoal, bool) {
	if !roadClearLootReceipt(w, a, self, session) || c == nil || c.Guid != a.RoadClear.SourceGuid {
		return coldridgeLootGoal{}, false
	}
	r := a.RoadClear
	g := coldridgeLootGoal{corpseGUID: c.Guid, positionRevision: r.PositionRevision, roadRequest: r.RequestId, roadSession: r.SessionId, roadLevel: r.SelectedLevel, roadMaxHP: r.SelectedMaxHealth, roadDeathRevision: r.SourceDeathPositionRevision, roadOriginX: r.Origin.X, roadOriginY: r.Origin.Y, roadOriginZ: r.Origin.Z, roadOriginRevision: r.Origin.Revision, roadObservedPackets: w.ObservedPackets}
	return g, g.matchesCorpse(c)
}

func (g *coldridgeLootGoal) roadContext(w *worldv1.SnapshotResponse, a *actionsv1.State, self uint64, pending uint64) bool {
	if g == nil || g.roadRequest == "" || g.questID != 0 || g.levelingRequest != "" || pending != g.corpseGUID || !roadRequestEpoch(g.roadRecord, w) || !roadClearLootReceipt(w, a, self, g.roadSession) || w.ObservedPackets < g.roadObservedPackets {
		return false
	}
	r := a.RoadClear
	if r.RequestId != g.roadRequest || r.SourceGuid != g.corpseGUID || r.PositionRevision != g.positionRevision || r.SelectedLevel != g.roadLevel || r.SelectedMaxHealth != g.roadMaxHP || r.SourceDeathPositionRevision != g.roadDeathRevision || r.Origin.X != g.roadOriginX || r.Origin.Y != g.roadOriginY || r.Origin.Z != g.roadOriginZ || r.Origin.Revision != g.roadOriginRevision || len(w.LootableCorpses) > 64 {
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
	// The owned pending window survives discovery eviction. It never grants
	// permission to open another source or to infer a killer from death alone.
	g.roadObservedPackets = w.ObservedPackets
	return true
}

func (s *Service) bindRoadLootDeadline(g *coldridgeLootGoal, before time.Time, remaining uint64) bool {
	if g == nil || g.roadRequest == "" || g.roadObservedPackets == 0 || remaining == 0 || remaining > 180000 {
		return false
	}
	if s.roadRequests == nil {
		s.roadRequests = make(map[string]*roadLootRequest)
	}
	r, exists := s.roadRequests[g.roadRequest]
	if !exists {
		if len(s.roadRequests) >= 64 {
			return false
		}
		for _, previous := range s.roadRequests {
			if previous != nil && previous.sourceGUID == g.corpseGUID && (previous.consumed || previous.causalBroken) {
				return false
			}
		}
		until := before.Add(time.Duration(remaining) * time.Millisecond)
		r = &roadLootRequest{sourceGUID: g.corpseGUID, until: until, proofUntil: until, observedPackets: g.roadObservedPackets}
		s.roadRequests[g.roadRequest] = r
	}
	if r.causalBroken || r.consumed || r.sourceGUID != g.corpseGUID || !s.now().Before(r.until) || !s.now().Before(r.proofUntil) {
		return false
	}
	if g.roadObservedPackets < r.observedPackets {
		r.causalBroken = true
		return false
	}
	r.observedPackets = g.roadObservedPackets
	g.roadUntil = r.until
	g.roadRecord = r
	return true
}

func (s *Service) noteRoadLootTTL(before time.Time, a *actionsv1.State) {
	g := &s.coldridgeGoal
	if g.roadRequest == "" || a == nil || a.RoadClear == nil {
		return
	}
	p := a.RoadClear
	r, exists := s.roadRequests[g.roadRequest]
	if !exists || r == nil || p.RequestId != g.roadRequest || p.SourceGuid != r.sourceGUID || p.RemainingMs > 180000 {
		return
	}
	if until := before.Add(time.Duration(p.RemainingMs) * time.Millisecond); until.Before(r.proofUntil) {
		r.proofUntil = until
		s.roadRequests[g.roadRequest] = r
	}
}

func (s *Service) roadPacketAllowed(op uint32) bool {
	g := &s.coldridgeGoal
	if !s.questColdridge || g.roadRequest == "" || op != opcode.CMSGLoot && op != opcode.CMSGLootMoney && op != opcode.CMSGAutostoreLootItem {
		return true
	}
	r, exists := s.roadRequests[g.roadRequest]
	if !exists || r == nil || r.causalBroken || r.sourceGUID != g.corpseGUID || g.roadUntil.IsZero() || !r.until.Equal(g.roadUntil) || !s.now().Before(r.until) || !s.now().Before(r.proofUntil) || op != opcode.CMSGLoot && !r.consumed {
		s.state.Reason = "road_clear_local_deadline_expired_no_further_loot_writes"
		return false
	}
	if op == opcode.CMSGLoot {
		if r.consumed {
			s.state.Reason = "road_clear_handoff_already_consumed"
			return false
		}
		r.consumed = true // reserve before a potentially ambiguous OPEN
		s.roadRequests[g.roadRequest] = r
	}
	return true
}
