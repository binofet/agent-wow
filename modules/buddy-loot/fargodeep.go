package buddyloot

import (
	"context"
	"math"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"google.golang.org/protobuf/types/known/emptypb"
)

func ownFargodeepQuest(w *worldv1.SnapshotResponse, self uint64) bool {
	active, _ := ownFargodeepQuestProgress(w, self)
	return active
}

func ownFargodeepQuestProgress(w *worldv1.SnapshotResponse, self uint64) (active, incomplete bool) {
	if self != 1 && self != 4 && self != 5 || w == nil || w.Self == nil || w.Self.Guid != self || !w.Self.MapKnown || w.Self.MapId != 0 || !w.SelfQuestLogKnown || len(w.SelfQuestLog) != 25 {
		return false, false
	}
	slots, ids, found := [25]bool{}, map[uint32]bool{}, false
	for _, q := range w.SelfQuestLog {
		if q == nil || q.Slot >= 25 || slots[q.Slot] || q.QuestId != 0 && ids[q.QuestId] {
			return false, false
		}
		slots[q.Slot] = true
		if q.QuestId != 0 {
			ids[q.QuestId] = true
		}
		if (q.QuestId == 47 || q.QuestId == 60) && !q.Failed && q.State&2 == 0 {
			found = true // COMPLETE remains eligible while peers collect their items.
			incomplete = incomplete || !q.Complete && q.State&1 == 0
		}
	}
	return found, incomplete
}

func fargodeepLootPoint(x, y, z float32) bool {
	return x >= -10000 && x <= -9700 && y >= 50 && y <= 320 && z >= -10 && z <= 65
}

func fargodeepRetainedLoot(a *actionsv1.State, target uint64) bool {
	return a != nil && a.Fargodeep != nil && !a.Fargodeep.Active && a.Fargodeep.TargetGuid == target && target != 0 && a.Fargodeep.Phase == "target_death_observed" && (a.Fargodeep.EngagerGuid == 1 || a.Fargodeep.EngagerGuid == 4 || a.Fargodeep.EngagerGuid == 5)
}

// New incidental sources require an incomplete own collection quest. An exact
// retained encounter keeps its previous COMPLETE-quest round-robin behavior.
func fargodeepLootSelection(w *worldv1.SnapshotResponse, a *actionsv1.State, self, target uint64) bool {
	_, incomplete := ownFargodeepQuestProgress(w, self)
	return (incomplete || fargodeepRetainedLoot(a, target)) && fargodeepLootContext(w, a, self, target)
}

// Actual public corpse and recipient-rights observations authorize ordinary
// looting, without inventing a prior kill receipt. The operation binds one GUID.
// LOOTABLE is checked separately by eligibleWithin before opening. It may clear
// after the last item while the actual item/release receipts are still pending.
func fargodeepLootContext(w *worldv1.SnapshotResponse, a *actionsv1.State, self, target uint64) bool {
	if !ownFargodeepQuest(w, self) || a == nil || target == 0 || a.MotionUnconfirmed || a.CastPending || a.AutoAttacking || a.AutoAttackRequested || a.AutoAttackUnconfirmed || a.Practicing || a.GetRecovery().GetActive() || a.GetFargodeep().GetActive() || a.InteractionOwner != "" && a.InteractionOwner != "loot" || (a.Moving || a.Traveling) && a.InteractionOwner != "loot" || !safeOwnQuestLootContext(w, a.Position) || !fargodeepLootPoint(a.Position.X, a.Position.Y, a.Position.Z) {
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
		const unsafe = uint32(0x1 | 0x4 | 0x40000 | 0x80000 | 0x100000 | 0x400000 | 0x800000 | 0x1000000 | 0x2000000 | 0x8000000)
		if p == nil || !p.Present || !p.MapKnown || p.MapId != 0 || !p.PositionKnown || !p.AliveKnown || !p.Alive || !p.HealthKnown || p.Health == 0 || p.MaxHealth == 0 || p.Health > p.MaxHealth || uint64(p.Health)*100 < uint64(p.MaxHealth)*85 || !p.UnitFlagsKnown || p.UnitFlags&unsafe != 0 || p.PlayerFlags&2 != 0 || !p.MovementFlagsKnown || !movementflags.GroundCompatible(p.MovementFlags, p.ExtraMovementFlags) || guid != self && p.MovementFlags&0x1ff != 0 {
			return false
		}
		x, y, z := p.X, p.Y, p.Z
		if guid == self {
			x, y, z = a.Position.X, a.Position.Y, a.Position.Z
		}
		dx, dy, dz := float64(x-a.Position.X), float64(y-a.Position.Y), float64(z-a.Position.Z)
		d := dx*dx + dy*dy + dz*dz
		if math.IsNaN(d) || math.IsInf(d, 0) || d > 35*35 {
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

// Neither a quest nor a retained encounter authorizes acting after a party or
// body change. This read always precedes the final world response.
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
