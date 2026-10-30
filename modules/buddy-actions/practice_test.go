package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func practiceRig(t *testing.T, class uint32) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = -8933, -136, 81
	f.world.Self.LevelKnown, f.world.Self.Level, f.world.Self.ClassId = true, 1, class
	f.world.Self.Health, f.world.Self.MaxHealth = 100, 100
	f.world.Self.BaseManaKnown, f.world.Self.BaseMana = true, 100
	f.world.Owner = nil
	f.world.NearbyCreatures = []*worldv1.CreatureState{{Guid: enemyGUID, Entry: 299, Present: true, MapKnown: true, PositionKnown: true, X: -8930, Y: -136, Z: 81, HealthKnown: true, Health: 55, MaxHealth: 55, AliveKnown: true, Alive: true, LevelKnown: true, Level: 1, CombatReachKnown: true, CombatReach: 1.5}}
	if class == 4 {
		f.world.Self.PowerType, f.world.Self.Power, f.world.Self.MaxPower = 3, 100, 100
		f.world.Self.BaseAttackTimeKnown, f.world.Self.BaseAttackTimeMs = true, 1600
		f.world.Self.CombatReachKnown, f.world.Self.CombatReach = true, 1.5
		f.world.Self.Mainhand = &worldv1.EquippedWeapon{SlotKnown: true, ItemGuid: 0x4000000000000042, MetadataKnown: true, Entry: 2092, DurabilityKnown: true, Durability: 16, MaxDurability: 16}
		s.weapons[2092] = weaponProof{entry: 2092, class: 2, subclass: 15, inventory: 13, valid: true}
		learnSpells(s, autoAttack, sinisterStrike)
	} else {
		learnSpells(s, fireball)
	}
	s.SetNavigationDataDir("/fixture/client-data")
	s.planPath = func(_ context.Context, _ string, a, b position) ([]position, error) { return []position{a, b}, nil }
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return s, f, advance
}

func TestPracticePullsOnlyObservedLevelOneWolfWithoutOwnerAndUsesActualClass(t *testing.T) {
	for _, class := range []uint32{4, 8} {
		s, f, _ := practiceRig(t, class)
		s.Tick(context.Background())
		want := fireball
		if class == 4 {
			want = sinisterStrike
		}
		if lastCast(f) != want || state(t, s).PracticePulls != 1 || !state(t, s).Practicing || state(t, s).AssistDamageEvents != 0 {
			t.Fatal(state(t, s), f.packets)
		}
		if class == 4 && countPackets(f, opcode.CMSGAttackswing) != 1 {
			t.Fatal("rogue ordinary attack missing")
		}
	}
}

func TestPracticeNeverPullsUnhealthyUnreadyWrongOrClaimedCreatures(t *testing.T) {
	for _, mode := range []string{"health", "mana", "entry", "level", "unknownlevel", "claimed", "combat", "no_spell", "spline"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := practiceRig(t, 8)
			c := f.world.NearbyCreatures[0]
			switch mode {
			case "health":
				f.world.Self.Health = 94
			case "mana":
				f.world.Self.Power = 39
			case "entry":
				c.Entry = 69
			case "level":
				c.Level = 2
			case "unknownlevel":
				c.LevelKnown = false
			case "claimed":
				c.AttackVictimKnown = true
				c.AttackVictimGuid = 3
			case "combat":
				f.world.Self.UnitFlags = 0x80000
			case "no_spell":
				s.state.KnownFireball = false
			case "spline":
				c.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 1000, AgeMs: 100}
			}
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 || state(t, s).PracticePulls != 0 {
				t.Fatal(state(t, s), f.packets)
			}
		})
	}
}

func TestPracticeUsesNavigationForRogueApproachAndStopsAtLevelTwoOrDanger(t *testing.T) {
	for _, mode := range []string{"level", "dead", "unknown", "expiry", "disable", "teleport"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := practiceRig(t, 4)
			f.world.NearbyCreatures[0].X = -8920
			s.Tick(context.Background())
			if !state(t, s).Moving || countPackets(f, opcode.CMSGAttackswing) != 0 {
				t.Fatal(state(t, s), f.packets)
			}
			advance(200 * time.Millisecond)
			switch mode {
			case "level":
				f.world.Self.Level = 2
			case "dead":
				f.world.Self.Alive = false
			case "unknown":
				f.world.Self.LevelKnown = false
			case "expiry":
				advance(11 * time.Minute)
			case "disable":
				s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: false})
			case "teleport":
				f.world.Self.X += 20
				f.world.Self.PositionRevision++
			}
			s.Tick(context.Background())
			if state(t, s).Practicing || state(t, s).Moving || countPackets(f, opcode.MSGMoveStop) != 1 {
				t.Fatal(state(t, s), f.packets)
			}
		})
	}
}

func TestPracticeDamageAndKillCreditRequireActualOwnServerReceipts(t *testing.T) {
	s, f, _ := practiceRig(t, 8)
	s.Tick(context.Background())
	if state(t, s).PracticeKills != 0 || state(t, s).AssistDamageEvents != 0 {
		t.Fatal("transport invented results")
	}
	p := binary.LittleEndian.AppendUint64(nil, 3)
	p = binary.LittleEndian.AppendUint64(p, enemyGUID)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGPartykilllog, Payload: p})
	if state(t, s).PracticeKills != 0 {
		t.Fatal("foreign credit")
	}
	binary.LittleEndian.PutUint64(p, 1)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGPartykilllog, Payload: p})
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGPartykilllog, Payload: p})
	if state(t, s).PracticeKills != 1 || state(t, s).AssistDamageEvents != 0 {
		t.Fatal(state(t, s), f.packets)
	}
}

func TestPracticeRequiresVerifiedRouteBeforeEvenNearbyMagePull(t *testing.T) {
	for _, mode := range []string{"assets", "partial", "bent"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := practiceRig(t, 8)
			switch mode {
			case "assets":
				s.navDataDir = ""
			case "partial":
				s.planPath = func(context.Context, string, position, position) ([]position, error) {
					return nil, errors.New("partial")
				}
			case "bent":
				s.planPath = func(_ context.Context, _ string, a, b position) ([]position, error) {
					return []position{a, {x: a.x, y: a.y + 6, z: a.z}, {x: b.x, y: a.y + 6, z: a.z}, b}, nil
				}
			}
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 0 || state(t, s).PracticePulls != 0 {
				t.Fatal("nearby mage pulled before complete direct ground route", state(t, s), f.packets)
			}
			if mode == "bent" {
				if !state(t, s).Moving || !state(t, s).NavAvailable {
					t.Fatal(state(t, s))
				}
			} else if state(t, s).Practicing || state(t, s).Moving || len(f.packets) != 0 {
				t.Fatal(state(t, s), f.packets)
			}
		})
	}
}

func finishPracticeCast(s *Service) {
	p := smiteEvent(true, s.castCount, enemyGUID)
	binary.LittleEndian.PutUint32(p[5:], s.pendingSpell)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: p})
}

func TestPracticeWaitsForOutOfCombatRegenerationBeforeNextPull(t *testing.T) {
	s, f, advance := practiceRig(t, 8)
	s.Tick(context.Background())
	finishPracticeCast(s)
	f.world.NearbyCreatures[0].Health = 0
	f.world.NearbyCreatures[0].Alive = false
	f.world.Self.Health = 70
	s.Tick(context.Background())
	s.Tick(context.Background())
	if state(t, s).PracticePhase != "recovering_before_next_pull" || state(t, s).PracticePulls != 1 {
		t.Fatal(state(t, s))
	}
	// A new observed wolf and actual normal regeneration permit a second pull.
	f.world.NearbyCreatures[0].Guid = enemyGUID + 1
	f.world.NearbyCreatures[0].Health = 55
	f.world.NearbyCreatures[0].Alive = true
	f.world.Self.Health = 95
	advance(3 * time.Second)
	s.Tick(context.Background())
	if state(t, s).PracticePulls != 2 || countPackets(f, opcode.CMSGCastSpell) != 2 {
		t.Fatal(state(t, s), f.packets)
	}
}

func TestPracticeLevelTwoFromPartyXPFinishesOnlyExistingProvenOwnFight(t *testing.T) {
	s, f, advance := practiceRig(t, 8)
	s.Tick(context.Background())
	finishPracticeCast(s)
	f.world.Self.Level = 2
	f.world.Self.UnitFlags = 0x80000
	f.world.NearbyCreatures[0].AttackVictimKnown = true
	f.world.NearbyCreatures[0].AttackVictimGuid = 1
	advance(3 * time.Second)
	s.Tick(context.Background())
	if !state(t, s).Practicing || state(t, s).PracticePhase != "finishing_current_fight_to_level2" || state(t, s).PracticePulls != 1 || countPackets(f, opcode.CMSGCastSpell) != 2 {
		t.Fatal(state(t, s), f.packets)
	}
	finishPracticeCast(s)
	f.world.NearbyCreatures[0].Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 1000, AgeMs: 100}
	s.Tick(context.Background())
	if !state(t, s).Practicing || state(t, s).PracticePhase != "finishing_current_fight_to_level2" {
		t.Fatal("transient movement hid finishing-fight ownership", state(t, s))
	}
	f.world.NearbyCreatures[0].Trajectory = nil
	f.world.NearbyCreatures[0].Health = 0
	f.world.NearbyCreatures[0].Alive = false
	s.Tick(context.Background())
	if state(t, s).Practicing || state(t, s).PracticePulls != 1 || state(t, s).PracticePhase != "level_two_observed" {
		t.Fatal(state(t, s))
	}
}

func TestPracticeServerCastFailureAndUnexpectedHostileStopWithoutNewPull(t *testing.T) {
	for _, mode := range []string{"failure", "hostile"} {
		s, f, _ := practiceRig(t, 8)
		s.Tick(context.Background())
		if mode == "failure" {
			p := []byte{s.castCount}
			p = binary.LittleEndian.AppendUint32(p, fireball)
			p = append(p, 49)
			s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGCastFailed, Payload: p})
		} else {
			f.world.NearbyCreatures = append(f.world.NearbyCreatures, &worldv1.CreatureState{Guid: enemyGUID + 2, Entry: 69, Present: true, AliveKnown: true, Alive: true, AttackVictimKnown: true, AttackVictimGuid: 1})
		}
		s.Tick(context.Background())
		if state(t, s).Practicing || state(t, s).PracticePulls != 1 || countPackets(f, opcode.CMSGCastSpell) != 1 {
			t.Fatal(state(t, s), f.packets)
		}
	}
}
