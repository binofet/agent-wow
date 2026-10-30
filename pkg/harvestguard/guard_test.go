package harvestguard

import (
	"testing"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
)

func guardFixture() *worldv1.SnapshotResponse {
	p := func(guid uint64, x float32) *worldv1.PlayerState {
		return &worldv1.PlayerState{Guid: guid, Present: true, MapKnown: true, PositionKnown: true, X: x, AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, MaxHealth: 100, UnitFlagsKnown: true, MovementFlagsKnown: true}
	}
	return &worldv1.SnapshotResponse{Self: p(1, 0), Owner: p(3, 10), NearbyPlayers: []*worldv1.PlayerState{p(4, 20)}}
}

func guardAttacker(victim uint64, age uint64) *worldv1.CreatureState {
	return &worldv1.CreatureState{Guid: 0xf13000012b000001, Present: true, MapKnown: true, AliveKnown: true, Alive: true, HealthKnown: true, Health: 10, AttackVictimKnown: true, AttackVictimGuid: victim, AttackAgeMs: age}
}

func TestWorldReasonUsesActualCombatAndPreFlagAttackerEvidence(t *testing.T) {
	for _, mode := range []string{"safe", "self_combat", "owner_combat", "party_combat", "fresh_self", "fresh_owner", "fresh_party", "fresh_boundary", "stale", "unrelated", "dead", "unknown_alive", "other_map", "exact_lookup"} {
		t.Run(mode, func(t *testing.T) {
			w := guardFixture()
			c := guardAttacker(1, 0)
			switch mode {
			case "self_combat":
				w.Self.UnitFlags = 0x80000
			case "owner_combat":
				w.Owner.UnitFlags = 0x80000
			case "party_combat":
				w.NearbyPlayers[0].UnitFlags = 0x80000
			case "fresh_owner":
				c.AttackVictimGuid = 3
			case "fresh_party":
				c.AttackVictimGuid = 4
			case "fresh_boundary":
				c.AttackAgeMs = 5000
			case "stale":
				c.AttackAgeMs = 5001
			case "unrelated":
				c.AttackVictimGuid = 99
			case "dead":
				c.Alive, c.Health = false, 0
			case "unknown_alive":
				c.AliveKnown = false
			case "other_map":
				c.MapId = 1
			}
			if mode != "safe" && mode != "self_combat" && mode != "owner_combat" && mode != "party_combat" {
				if mode == "exact_lookup" {
					w.RequestedCreature = c
				} else {
					w.NearbyCreatures = []*worldv1.CreatureState{c}
				}
			}
			before := proto.Clone(w)
			reason := WorldReason(w, 1, []uint64{1, 3, 4})
			wantSafe := mode == "safe" || mode == "stale" || mode == "unrelated" || mode == "dead" || mode == "other_map"
			if (reason == "") != wantSafe || !proto.Equal(w, before) {
				t.Fatal(mode, reason)
			}
		})
	}
}

func TestWorldReasonPrioritizesActionableHealingWithoutInventingParty(t *testing.T) {
	for _, mode := range []string{"self", "owner", "party", "threshold", "old_scratch", "far", "afk", "stale_moving", "stranger"} {
		t.Run(mode, func(t *testing.T) {
			w := guardFixture()
			p := w.NearbyPlayers[0]
			if mode == "self" {
				p = w.Self
			} else if mode == "owner" {
				p = w.Owner
			}
			p.Health = 84
			switch mode {
			case "threshold":
				p.Health = 85
			case "old_scratch":
				p.Health, p.VitalsAgeMs = 95, 300000
			case "far":
				p.X = 36
			case "afk":
				p.PlayerFlags = 2
			case "stale_moving":
				p.MovementFlags, p.PositionAgeMs = 1, 2001
			case "stranger":
				p.Guid, p.UnitFlags = 99, 0x80000
			}
			reason := WorldReason(w, 1, []uint64{1, 3, 4})
			wantBlock := mode == "self" || mode == "owner" || mode == "party" || mode == "far"
			if (reason != "") != wantBlock {
				t.Fatal(mode, reason)
			}
		})
	}
}

func TestWorldReasonConservativelyYieldsWhenRawOwnPoseLagsClientRange(t *testing.T) {
	w := guardFixture()
	w.Self.X, w.Self.PositionRevision = 0, 1
	w.NearbyPlayers[0].X, w.NearbyPlayers[0].Health = 40, 40
	// A same-revision client estimate20yd along a guarded leg would put the
	// observed injured actor in the normal35yd healing range. This pure helper
	// receives raw world evidence only, so it must not infer no healing need
	// from raw distance40 and continue suppressing the enabled controller.
	if WorldReason(w, 1, []uint64{1, 3, 4}) != "harvest_observed_healing_need_takes_priority" {
		t.Fatal("stale raw own pose suppressed healing priority")
	}
}

func TestWorldReasonRequiresBoundedActualProtectedIdentity(t *testing.T) {
	for _, guids := range [][]uint64{nil, {4}, {1, 1}, {1, 0}, {1, 0xf130000001000001}, make([]uint64, 43)} {
		if reason := WorldReason(guardFixture(), 1, guids); reason == "" {
			t.Fatal("invalid protected proof accepted", guids)
		}
	}
	w := guardFixture()
	w.NearbyPlayers = append(w.NearbyPlayers, w.NearbyPlayers[0])
	if WorldReason(w, 1, []uint64{1, 4}) == "" {
		t.Fatal("duplicate observed protected body accepted")
	}
}
