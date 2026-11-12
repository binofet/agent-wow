package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func cold183Setup(t *testing.T, progress uint32) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	f := &fakeSession{world: cold183World(progress)}
	f.world.NearbyCreatures = []*worldv1.CreatureState{cold183Creature(-6237)}
	s := NewIndependentService(f, 8)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	s.SetNavigationDataDir("/fixture/client-data")
	s.planColdridgePath = coldStraight
	learnSpells(s, shadowBolt)
	return s, f, func(d time.Duration) { now = now.Add(d) }
}

func cold183Rig(t *testing.T, progress uint32) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := cold183Setup(t, progress)
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err != nil {
		t.Fatal("actual own183 fixture did not start", err)
	}
	return s, f, advance
}

func TestColdridge183PracticePlansThenCastsWithoutInventingProgress(t *testing.T) {
	s, f, _ := cold183Rig(t, 2)
	s.Tick(context.Background())
	if s.practiceTarget != cold183GUID || !state(t, s).NavAvailable || lastCast(f) != 0 || s.practicePullStarted {
		t.Fatal("planning invented an owned fight", state(t, s))
	}
	s.Tick(context.Background())
	got := state(t, s)
	if lastCast(f) != shadowBolt || s.pendingTarget != cold183GUID || !s.practicePullStarted || got.PracticePulls != 1 || got.PracticeKills != 0 || got.ShadowBoltDamageEvents != 0 || got.ServerAssistGo != 0 || cold183Quest(f.world).ObjectiveCounts[0] != 2 || s.coldridgeGoal.questID != 183 {
		t.Fatal("cast transport invented quest, damage or kill receipts", got)
	}
}

func TestColdridge183PracticeDoesNotSwitchToLater179Or170(t *testing.T) {
	s, f, _ := cold183Rig(t, 2)
	f.world.SelfQuestLog[10].QuestId = 170
	f.world.SelfQuestLog[20].QuestId = 179
	f.world.NearbyCreatures = append([]*worldv1.CreatureState{coldThreatCreature(707, -6239)}, f.world.NearbyCreatures...)
	s.Tick(context.Background())
	s.Tick(context.Background())
	if s.pendingTarget != cold183GUID || s.coldridgeGoal.questID != 183 || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("a later older quest replaced the bound183 goal", state(t, s))
	}
	cold183Quest(f.world).QuestId = 0
	s.Tick(context.Background())
	if state(t, s).Practicing || countPackets(f, opcode.CMSGCastSpell) != 1 || s.coldridgeGoal.questID != 183 {
		t.Fatal("missing183 was rescued by a different accepted quest", state(t, s))
	}
}

func TestColdridge183PracticeRechecksClaimAndResourcesAfterPlanning(t *testing.T) {
	for _, mode := range []string{"target_claim", "own_victim_without_cast", "combat_unknown_victim", "own_tap", "foreign_tap", "health94", "mana59", "wrong_faction", "wrong_profile", "counter_regressed", "unsupported_counter", "counts_done", "unknown_log", "self_level2", "position_revision", "no_nav", "unexpected_hostile"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := cold183Rig(t, 2)
			s.Tick(context.Background())
			c, q := f.world.NearbyCreatures[0], cold183Quest(f.world)
			switch mode {
			case "target_claim":
				c.TargetGuid = 3
			case "own_victim_without_cast":
				c.AttackVictimKnown, c.AttackVictimGuid = true, 8
			case "combat_unknown_victim":
				c.UnitFlags = 0x80000
			case "own_tap":
				c.DynamicFlags = 8
			case "foreign_tap":
				c.DynamicFlags = 4
			case "health94":
				f.world.Self.Health = 94
			case "mana59":
				f.world.Self.Power = uint32(manaBudget(f.world.Self, 60) - 1)
			case "wrong_faction":
				c.FactionTemplate = 36
			case "wrong_profile":
				c.MaxHealth = 72
			case "counter_regressed":
				q.ObjectiveCounts[0] = 1
			case "unsupported_counter":
				q.ObjectiveCounts[1] = 1
			case "counts_done":
				q.ObjectiveCounts[0] = 12
			case "unknown_log":
				f.world.SelfQuestLogKnown = false
			case "self_level2":
				f.world.Self.Level = 2
			case "position_revision":
				f.world.Self.PositionRevision++
			case "no_nav":
				s.navDataDir, s.practiceRoute = "", nil
			case "unexpected_hostile":
				f.world.NearbyCreatures = append(f.world.NearbyCreatures, &worldv1.CreatureState{Guid: cold183GUID + 1, Entry: 706, Present: true, AliveKnown: true, Alive: true, AttackVictimKnown: true, AttackVictimGuid: 8})
			}
			s.Tick(context.Background())
			if lastCast(f) != 0 || s.practicePullStarted || state(t, s).PracticePulls != 0 {
				t.Fatal("post-plan disproof authorized a183 pull", mode, state(t, s))
			}
		})
	}
}

func TestColdridge183PracticeUsesFinalRequestedTargetReadAfterFacing(t *testing.T) {
	for _, mode := range []string{"unchanged", "claimed", "removed_with_other_quests", "failed", "regressed", "done", "unsupported_counter", "map", "revision", "health", "mana", "target_moved"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := cold183Rig(t, 2)
			s.Tick(context.Background())
			faced := false
			f.onClock = func() {
				f.onClock = nil
				faced = true
				q := cold183Quest(f.world)
				switch mode {
				case "claimed":
					f.world.NearbyCreatures[0].TargetGuid = 3
				case "removed_with_other_quests":
					q.QuestId = 0
					f.world.SelfQuestLog[10].QuestId, f.world.SelfQuestLog[20].QuestId = 170, 179
				case "failed":
					q.Failed = true
				case "regressed":
					q.ObjectiveCounts[0] = 1
				case "done":
					q.ObjectiveCounts[0] = 12
				case "unsupported_counter":
					q.ObjectiveCounts[3] = 1
				case "map":
					f.world.Self.MapId = 1
				case "revision":
					f.world.Self.PositionRevision++
				case "health":
					f.world.Self.Health = 94
				case "mana":
					f.world.Self.Power = 0
				case "target_moved":
					f.world.NearbyCreatures[0].X += 4
				}
			}
			s.Tick(context.Background())
			if !faced || countPackets(f, opcode.MSGMoveSetFacing) != 1 {
				t.Fatal("fixture did not reach the successful-facing boundary", state(t, s))
			}
			want := uint64(0)
			if mode == "unchanged" {
				want = 1
			}
			if uint64(countPackets(f, opcode.CMSGCastSpell)) != want || state(t, s).PracticePulls != want || s.practicePullStarted != (want == 1) {
				t.Fatal("CAST reused earlier disproven183 authority", mode, state(t, s))
			}
			if len(f.lookups) == 0 || f.lookups[len(f.lookups)-1] != cold183GUID {
				t.Fatal("last read did not request the selected exact target")
			}
		})
	}
}

func TestColdridge183PracticeFinishesOnlyExistingRecentOwnFight(t *testing.T) {
	for _, proof := range []string{"own", "foreign", "stale", "missing"} {
		t.Run(proof, func(t *testing.T) {
			s, f, advance := cold183Rig(t, 11)
			s.Tick(context.Background())
			s.Tick(context.Background())
			// Adapt the complete source-shaped frame to this fixture's self8,
			// pending Shadow Bolt and selected708, rather than the old self1 wolf.
			goPacket := smiteEvent(true, s.castCount, cold183GUID)
			goPacket[1], goPacket[3] = 8, 8
			binary.LittleEndian.PutUint32(goPacket[5:], shadowBolt)
			s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: goPacket})
			if state(t, s).CastPending || state(t, s).ServerAssistGo != 1 {
				t.Fatal("fixture server receipt did not bind the own pending cast", state(t, s))
			}
			cold183Quest(f.world).ObjectiveCounts[0] = 12
			f.world.Self.UnitFlags = 0x80000
			c := f.world.NearbyCreatures[0]
			c.AttackVictimKnown, c.AttackVictimGuid, c.AttackAgeMs = true, 8, 100
			switch proof {
			case "foreign":
				c.AttackVictimGuid = 3
			case "stale":
				c.AttackAgeMs = 30001
			case "missing":
				c.AttackVictimKnown = false
			}
			advance(3 * time.Second)
			s.Tick(context.Background())
			wantCasts := 1
			if proof == "own" {
				wantCasts = 2
			}
			if countPackets(f, opcode.CMSGCastSpell) != wantCasts || state(t, s).Practicing != (proof == "own") || state(t, s).PracticePulls != 1 {
				t.Fatal("finished objective extended an unproven or new fight", proof, state(t, s))
			}
			c.Health, c.Alive, f.world.Self.UnitFlags = 0, false, 0
			s.Tick(context.Background())
			if state(t, s).Practicing || state(t, s).PracticeKills != 0 {
				t.Fatal("observed target death invented credit or resumed practice", state(t, s))
			}
		})
	}
}

func TestColdridge183PracticeRetainsSealedBoundaryAndTenMinuteGoal(t *testing.T) {
	for _, y := range []float32{700, 701.332, 719.99316, 747.95306} {
		s, f, _ := cold183Setup(t, 0)
		f.world.Self.Y, f.world.NearbyCreatures[0].Y = 699, y
		if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err != nil {
			t.Fatal(err)
		}
		s.Tick(context.Background())
		s.Tick(context.Background())
		want := 0
		if y == 700 {
			want = 1
		}
		if countPackets(f, opcode.CMSGCastSpell) != want || y > 700 && (state(t, s).Moving || s.practiceTarget != 0) {
			t.Fatal("observed708 expanded the sealed y boundary", y, state(t, s))
		}
	}
	s, f, advance := cold183Rig(t, 0)
	s.Tick(context.Background())
	advance(10 * time.Minute)
	s.Tick(context.Background())
	if state(t, s).Practicing || lastCast(f) != 0 || state(t, s).PracticePhase != "goal_expired" {
		t.Fatal("183 extended the ten-minute practice bound", state(t, s))
	}
}
