package buddyprofessions

import (
	"context"
	"fmt"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestHarvestOnlyAllowsEnabledExactIdleOrdinaryLoot(t *testing.T) {
	s := NewService(nil, "body", 7)
	for _, skill := range []uint32{393, 182, 333, 129, 185, 356} {
		for _, mode := range []string{"idle", "empty", "disabled", "window", "corpse", "object", "session", "unknown_session"} {
			s.loot = &lootv1.State{SessionId: "body", Enabled: true, Phase: "idle"}
			switch mode {
			case "empty":
				s.loot.Phase = ""
			case "disabled":
				s.loot.Phase = "disabled"
			case "window":
				s.loot.Phase = "waiting_release"
			case "corpse":
				s.loot.PendingCorpseGuid = 1
			case "object":
				s.loot.PendingGameObjectGuid = 1
			case "session":
				s.loot.SessionId = "other"
			case "unknown_session":
				s.loot.SessionId = ""
			}
			want := mode == "idle" && (skill == 393 || skill == 182)
			if s.ordinaryLootIdleLocked(skill) != want {
				t.Fatal(skill, mode, "enabled idle policy changed")
			}
		}
	}
}

func TestSkinHarvestApproachableDoesNotRelaxReadyOrProof(t *testing.T) {
	for _, mode := range []string{"far_ready", "outside", "height", "party_tap", "template", "rank", "knife", "inventory", "book", "flags"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := skinningFixture(t)
			s.mu.Lock()
			defer s.mu.Unlock()
			c := s.view.raw.RequestedCreature
			c.X = s.actions.Position.X + 10
			s.actions.Following, s.actions.Healing, s.actions.Assisting = true, true, true
			s.loot = &lootv1.State{SessionId: "body", Enabled: true, Phase: "idle"}
			switch mode {
			case "outside":
				c.X = s.actions.Position.X + 40.01
			case "height":
				c.Z = s.actions.Position.Z + 1.51
			case "party_tap":
				c.DynamicFlags &^= 8
			case "template":
				delete(s.skinTemplates, c.Entry)
			case "rank":
				c.Level = 30
			case "knife":
				s.view.counts[7005] = 0
			case "inventory":
				s.view.freeSlots = 0
			case "book":
				s.book.known = false
			case "flags":
				c.DynamicFlags = 1
			}
			rows := s.skinningTargetsLocked()
			if len(rows) != 1 || rows[0].Ready || rows[0].Approachable != (mode == "far_ready") {
				t.Fatal(rows)
			}
			if mode == "far_ready" && !strings.Contains(rows[0].Reason, "three_yards") {
				t.Fatal("cast range was relaxed", rows[0])
			}
		})
	}
}

func TestHerbHarvestDiscoveryNeedsFullTemplateAndRangeEligibility(t *testing.T) {
	for _, mode := range []string{"far_ready", "outside", "height", "template", "rank", "flags", "inventory", "duplicate", "conflicting"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := herbBehaviorFixture(t)
			s.mu.Lock()
			defer s.mu.Unlock()
			g := s.view.raw.RequestedGameObjects[0]
			g.X = s.actions.Position.X + 10
			s.view.raw.RequestedGameObjects = nil
			s.view.raw.NearbyGatheringObjects = []*worldv1.GameObjectState{g}
			s.loot = &lootv1.State{SessionId: "body", Enabled: true, Phase: "idle"}
			s.actions.Following, s.actions.Healing, s.actions.Assisting = true, true, true
			switch mode {
			case "outside":
				g.X = s.actions.Position.X + 40.01
			case "height":
				g.Z = s.actions.Position.Z + 1.51
			case "template":
				delete(s.herbTemplates, g.Entry)
			case "rank":
				m := s.herbTemplates[g.Entry]
				m.requiredSkill = 100
				s.herbTemplates[g.Entry] = m
			case "flags":
				g.Flags |= 4
			case "inventory":
				s.view.freeSlots = 0
			case "duplicate":
				s.view.raw.NearbyGatheringObjects = append(s.view.raw.NearbyGatheringObjects, g)
			case "conflicting":
				other := proto.Clone(g).(*worldv1.GameObjectState)
				other.Entry++
				s.view.raw.RequestedGameObjects = []*worldv1.GameObjectState{other}
			}
			rows := s.herbTargetsLocked()
			if mode == "duplicate" || mode == "conflicting" {
				for _, row := range rows {
					if row.Ready || row.Approachable {
						t.Fatal("ambiguous candidate became actionable", rows)
					}
				}
				return
			}
			if len(rows) != 1 || rows[0].Ready || rows[0].Approachable != (mode == "far_ready") {
				t.Fatal(rows)
			}
		})
	}
}

func TestHerbApproachUsesCurrentClientPoseBeyondLastServerPosition(t *testing.T) {
	s, _ := herbBehaviorFixture(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.view.self.X, s.view.self.Y, s.view.self.Z = 0, 0, 0
	s.actions.Position.X, s.actions.Position.Y, s.actions.Position.Z = 40, 0, 0
	g := s.view.raw.RequestedGameObjects[0]
	g.X, g.Y, g.Z = 50, 0, 0
	s.view.raw.RequestedGameObjects = nil
	s.view.raw.NearbyGatheringObjects = []*worldv1.GameObjectState{g}
	rows := s.herbTargetsLocked()
	if len(rows) != 1 || !rows[0].Approachable || rows[0].Ready {
		t.Fatal("current10yd approach and3yd cast bounds disagree", rows)
	}
	s.actions.Position.X = 9
	rows = s.herbTargetsLocked()
	if len(rows) != 1 || rows[0].Approachable || rows[0].Ready {
		t.Fatal("current41yd distance exceeded approach bound", rows)
	}
}

func TestHarvestRevokedLeaseCleanupObservesOnlyNoReplacementWrites(t *testing.T) {
	for _, mode := range []string{"released", "replacement", "moving", "traveling", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			s, f := herbBehaviorFixture(t)
			op := herbBehaviorDirect(t, s, f)
			f.mu.Lock()
			f.token = "revoked"
			f.actions.InteractionOwner = ""
			switch mode {
			case "replacement":
				f.actions.InteractionOwner = "quests"
			case "moving":
				f.actions.Moving = true
			case "traveling":
				f.actions.Traveling = true
			case "unknown":
				f.actions.MotionUnconfirmed = true
			}
			before := len(f.packets)
			f.mu.Unlock()
			if s.releaseHarvestLease(context.Background(), op, op.token) != (mode == "released") {
				t.Fatal("revocation proof wrong", mode)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.packets) != before || len(f.releases) != 0 {
				t.Fatal("cleanup wrote to revoked/replacement lease")
			}
		})
	}
}

func TestHarvestLeaseRequiresExactProtectedBodyAndOwner(t *testing.T) {
	for _, a := range []*actionsv1.State{nil, {}, {InteractionHarvest: true}, {InteractionHarvest: true, HarvestGuardGuids: []uint64{7}}, {InteractionHarvest: true, HarvestGuardGuids: []uint64{7, 7}}, {InteractionHarvest: true, HarvestGuardGuids: []uint64{7, 3, 0}}} {
		if validHarvestLease(a, 7) {
			t.Fatal("invalid guard accepted", a)
		}
	}
	if !validHarvestLease(&actionsv1.State{InteractionHarvest: true, HarvestGuardGuids: []uint64{3, 7}}, 7) {
		t.Fatal("exact guard rejected")
	}
}

func TestHarvestLastWorldThreatAndHealingGateBeforeCastOrStore(t *testing.T) {
	for _, herb := range []bool{false, true} {
		for _, store := range []bool{false, true} {
			for _, mode := range []string{"clear", "self_attack_before_flag", "owner_attack_before_flag", "party_attack_before_flag", "party_combat", "owner_combat", "self_injury", "party_injury"} {
				t.Run(fmt.Sprintf("herb%t/store%t/%s", herb, store, mode), func(t *testing.T) {
					var s *Service
					var f *professionSessionFixture
					var op *operation
					if herb {
						s, f = herbBehaviorFixture(t)
						op = herbBehaviorDirect(t, s, f)
					} else {
						s, f = skinningFixture(t)
						op = skinningDirect(t, s, f)
					}
					code := uint32(opcode.CMSGCastSpell)
					payload := skinCastPacket(9, op.request.TargetGuid)
					if herb {
						payload = herbCastPacket(9, op.request.TargetGuid)
					}
					if store {
						code, payload = opcode.CMSGAutostoreLootItem, storeLootPacket(0)
						op.receipt.OwnGoObserved = true
						op.window = &lootWindow{guid: op.request.TargetGuid}
					}
					f.mu.Lock()
					f.actions.HarvestGuardGuids = []uint64{7, 3, 4}
					f.worldHook = func(_ *worldv1.SnapshotRequest) {
						w := f.world
						victim := uint64(7)
						switch mode {
						case "owner_attack_before_flag":
							victim = 3
						case "party_attack_before_flag":
							victim = 4
						case "owner_combat":
							w.Owner = &worldv1.PlayerState{Guid: 3, Present: true, MapKnown: true, UnitFlagsKnown: true, UnitFlags: 0x80000}
						case "party_combat":
							w.NearbyPlayers = []*worldv1.PlayerState{{Guid: 4, Present: true, MapKnown: true, UnitFlagsKnown: true, UnitFlags: 0x80000}}
						case "self_injury":
							w.Self.Health = 1
						case "party_injury":
							w.NearbyPlayers = []*worldv1.PlayerState{{Guid: 4, Present: true, MapKnown: true, UnitFlagsKnown: true, HealthKnown: true, Health: 1, MaxHealth: 100, AliveKnown: true, Alive: true, PositionKnown: true, X: w.Self.X, Y: w.Self.Y, Z: w.Self.Z, MovementFlagsKnown: true}}
						}
						if strings.Contains(mode, "attack_before_flag") {
							w.NearbyCreatures = append(w.NearbyCreatures, &worldv1.CreatureState{Guid: 0xf130000123000055, Present: true, MapKnown: true, AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, AttackVictimKnown: true, AttackVictimGuid: victim, AttackAgeMs: 1})
						}
					}
					f.rpcEvents = nil
					before := len(f.packets)
					f.mu.Unlock()
					var attempted bool
					var err error
					if herb {
						attempted, err = s.herbWrite(context.Background(), op, code, payload)
					} else {
						attempted, err = s.skinWrite(context.Background(), op, code, payload)
					}
					if attempted != (mode == "clear") || (err == nil) != (mode == "clear") {
						t.Fatal(attempted, err)
					}
					f.mu.Lock()
					defer f.mu.Unlock()
					if mode != "clear" && len(f.packets) != before {
						t.Fatal("final threat gate wrote gameplay")
					}
					want := []string{"buddy-actions/acquire_interaction", "buddy-world/snapshot"}
					if mode == "clear" {
						want = append(want, fmt.Sprintf("send:%x", code))
					}
					if strings.Join(f.rpcEvents, "|") != strings.Join(want, "|") {
						t.Fatal("RPC after final world or wrong proof order", f.rpcEvents)
					}
				})
			}
		}
	}
}
