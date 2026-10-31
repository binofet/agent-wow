package buddyloot

import (
	"context"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/types/known/emptypb"
)

func ownFargodeepQuest(w *worldv1.SnapshotResponse, self uint64) bool {
	if self != 1 && self != 4 && self != 5 || w == nil || w.Self == nil || w.Self.Guid != self || !w.Self.MapKnown || w.Self.MapId != 0 || !w.SelfQuestLogKnown || len(w.SelfQuestLog) != 25 {
		return false
	}
	slots, ids, found := [25]bool{}, map[uint32]bool{}, false
	for _, q := range w.SelfQuestLog {
		if q == nil || q.Slot >= 25 || slots[q.Slot] || q.QuestId != 0 && ids[q.QuestId] {
			return false
		}
		slots[q.Slot] = true
		if q.QuestId != 0 {
			ids[q.QuestId] = true
		}
		if (q.QuestId == 47 || q.QuestId == 60) && !q.Failed && q.State&2 == 0 {
			found = true // COMPLETE remains eligible while peers collect their items.
		}
	}
	return found
}

func fargodeepLootPoint(x, y, z float32) bool {
	return x >= -10000 && x <= -9700 && y >= 50 && y <= 320 && z >= -10 && z <= 65
}

// This is the observed corpse from this body's completed finite encounter, not
// a general exception for arbitrary dead kobolds or another character's quest.
// LOOTABLE is checked separately by eligibleWithin before opening. It may clear
// after the last item while the actual item/release receipts are still pending.
func fargodeepLootContext(w *worldv1.SnapshotResponse, a *actionsv1.State, self, target uint64) bool {
	if !ownFargodeepQuest(w, self) || a == nil || a.MotionUnconfirmed || !safeOwnQuestLootContext(w, a.Position) || a.Fargodeep == nil || a.Fargodeep.Active || a.Fargodeep.TargetGuid != target || a.Fargodeep.Phase != "target_death_observed" || a.Fargodeep.EngagerGuid != 1 && a.Fargodeep.EngagerGuid != 4 && a.Fargodeep.EngagerGuid != 5 || !fargodeepLootPoint(a.Position.X, a.Position.Y, a.Position.Z) {
		return false
	}
	if o := w.Owner; o != nil && o.Present && (o.Guid != 3 || !o.UnitFlagsKnown || o.UnitFlags&0x80000 != 0) {
		return false
	}
	if len(w.NearbyPlayers) > 64 {
		return false
	}
	players := map[uint64]*worldv1.PlayerState{self: w.Self}
	for _, p := range w.NearbyPlayers {
		if p != nil && (p.Guid == 1 || p.Guid == 4 || p.Guid == 5) {
			if players[p.Guid] != nil {
				return false
			}
			players[p.Guid] = p
		}
	}
	for _, guid := range []uint64{1, 4, 5} {
		p := players[guid]
		if p == nil || !p.Present || !p.MapKnown || p.MapId != 0 || !p.AliveKnown || !p.Alive || !p.HealthKnown || p.Health == 0 || !p.UnitFlagsKnown || p.UnitFlags&0x80000 != 0 {
			return false
		}
	}
	var corpse *worldv1.CreatureState
	consider := func(c *worldv1.CreatureState) bool {
		if c == nil || c.Guid != target {
			return true
		}
		if corpse != nil && (corpse.Entry != c.Entry || corpse.Present != c.Present || corpse.MapKnown != c.MapKnown || corpse.MapId != c.MapId || corpse.HealthKnown != c.HealthKnown || corpse.Health != c.Health || corpse.AliveKnown != c.AliveKnown || corpse.Alive != c.Alive || corpse.DynamicFlagsKnown != c.DynamicFlagsKnown || corpse.DynamicFlags != c.DynamicFlags) {
			return false
		}
		corpse = c
		return true
	}
	if len(w.LootableCorpses) > 64 || len(w.NearbyCreatures) > 32 || !consider(w.RequestedCreature) {
		return false
	}
	for _, list := range [][]*worldv1.CreatureState{w.LootableCorpses, w.NearbyCreatures} {
		for _, c := range list {
			if !consider(c) {
				return false
			}
		}
	}
	c := corpse
	if c == nil || c.Guid>>48 != 0xf130 || c.Guid&0xffffff == 0 || uint32(c.Guid>>24&0xffffff) != c.Entry || c.Entry != 40 && c.Entry != 475 || !c.Present || !c.MapKnown || c.MapId != 0 || !c.HealthKnown || c.Health != 0 || !c.AliveKnown || c.Alive || !c.LevelKnown || c.Level < 5 || c.Level > 7 || !c.UnitFlagsKnown || !c.DynamicFlagsKnown || c.DynamicFlags&8 == 0 {
		return false
	}
	x, y, z, known := corpsePosition(c)
	return known && fargodeepLootPoint(x, y, z)
}

// The retained encounter receipt is necessary but cannot authorize acting after
// a party or body change. This read always precedes the final world response.
func (s *Service) fargodeepPartyCurrent(ctx context.Context) bool {
	p := &partyv1.StateResponse{}
	if s.state.SessionId == "" || s.invoke(ctx, "buddy-party", "state", &emptypb.Empty{}, p) != nil || p.SessionId != s.state.SessionId || p.Group == nil || !p.Group.Known || !p.Group.InGroup || p.Group.GroupGuid == 0 || len(p.Group.Members) < 3 || len(p.Group.Members) > 40 {
		return false
	}
	seen, online := map[uint64]bool{}, map[uint64]bool{}
	for _, m := range p.Group.Members {
		if m == nil || m.Guid == 0 || m.Guid>>48 != 0 || seen[m.Guid] || m.Self != (m.Guid == s.selfGUID) {
			return false
		}
		seen[m.Guid], online[m.Guid] = true, m.Online
	}
	return online[1] && online[4] && online[5]
}

func (s *Service) lootWorldRequest() *worldv1.SnapshotRequest {
	r := &worldv1.SnapshotRequest{}
	if s.questFargodeep {
		r.CreatureGuid = s.state.PendingCorpseGuid
	}
	return r
}
