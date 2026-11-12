package buddyactions

import (
	"context"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func lookupDiscovery(target *worldv1.CreatureState) []*worldv1.CreatureState {
	items := make([]*worldv1.CreatureState, 0, 32)
	if target != nil {
		items = append(items, target)
	}
	for len(items) < 32 {
		items = append(items, &worldv1.CreatureState{Guid: enemyGUID + 100 + uint64(len(items)), Entry: 69, Present: true})
	}
	return items
}

func lookupPracticeRig(t *testing.T) (*Service, *fakeSession, func(time.Duration), *worldv1.CreatureState) {
	t.Helper()
	s, f, advance := practiceRig(t, 8)
	target := f.world.NearbyCreatures[0]
	target.X = -8890 // Requires an ordinary mesh approach before the first pull.
	f.world.NearbyCreatures = lookupDiscovery(target)
	f.lookupCreatures = map[uint64]*worldv1.CreatureState{target.Guid: target}
	s.Tick(context.Background())
	if !s.state.Moving || s.practiceTarget != target.Guid || countPackets(f, opcode.CMSGCastSpell) != 0 || len(f.lookups) != 0 {
		t.Fatal("initial selection did not use bounded discovery", s.stateLocked(), f.packets, f.lookups)
	}
	f.world.NearbyCreatures = lookupDiscovery(nil)
	return s, f, advance, target
}

func TestPracticeKeepsSelectedCachedWolfAfterDiscoveryCapLoss(t *testing.T) {
	s, f, advance, target := lookupPracticeRig(t)
	serverPosition := playerPosition(f.world.Self)
	for step := 0; step < 40 && countPackets(f, opcode.CMSGCastSpell) == 0; step++ {
		advance(200 * time.Millisecond)
		s.Tick(context.Background())
	}
	got := state(t, s)
	if !got.Practicing || got.PracticePulls != 1 || got.LastCastTargetGuid != target.Guid || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("selected target was lost or replaced", got, f.packets)
	}
	if len(f.lookups) == 0 || len(f.world.NearbyCreatures) != 32 || playerPosition(f.world.Self) != serverPosition || distance(s.pos, serverPosition) < 10 {
		t.Fatal("test did not preserve stale server position and capped discovery", got)
	}
	for _, guid := range f.lookups {
		if guid != target.Guid {
			t.Fatal("lookup discovered another target", guid)
		}
	}
	if got.PracticeKills != 0 || got.AssistDamageEvents != 0 {
		t.Fatal("cached observation invented combat receipts", got)
	}
}

func TestPracticeLookupAbsenceClaimsAndUnsafeStateNeverAuthorizePull(t *testing.T) {
	for _, mode := range []string{"absent", "wrong_guid", "claim", "unsafe", "map", "lookup_error", "active_trajectory", "stale_movement"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, target := lookupPracticeRig(t)
			// OwnerTarget is not a fallback authority for independent practice.
			f.world.OwnerTarget = proto.Clone(target).(*worldv1.CreatureState)
			switch mode {
			case "absent":
				delete(f.lookupCreatures, target.Guid)
			case "wrong_guid":
				target.Guid++
			case "claim":
				target.AttackVictimKnown, target.AttackVictimGuid = true, 3
			case "unsafe":
				target.UnitFlags = 2
			case "map":
				target.MapId = 1
			case "lookup_error":
				f.failSnapshot = true
			case "active_trajectory":
				target.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 5000, AgeMs: 100}
			case "stale_movement":
				target.MovementFlags, target.PositionAgeMs = 1, 1501
			}
			advance(200 * time.Millisecond)
			s.Tick(context.Background())
			if s.state.Moving || s.state.PracticePulls != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 || s.state.PracticeKills != 0 {
				t.Fatal("unusable observation authorized movement or combat", s.stateLocked(), f.packets)
			}
			waiting := mode == "active_trajectory" || mode == "stale_movement"
			if s.state.Practicing != waiting {
				t.Fatal("missing/unsafe target was not stopped; position uncertainty must wait", s.stateLocked())
			}
		})
	}
}

func TestPracticeLookupFinishesOnlyTheOwnedFightAfterGoal(t *testing.T) {
	for _, mode := range []string{"own_fight", "other_victim", "stale_attack", "absent"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := practiceRig(t, 8)
			target := f.world.NearbyCreatures[0]
			s.Tick(context.Background())
			if !s.practicePullStarted || countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatal("fixture did not start its selected fight")
			}
			f.lookupCreatures = map[uint64]*worldv1.CreatureState{target.Guid: target}
			f.world.NearbyCreatures = nil
			f.world.Self.Level, f.world.Self.UnitFlags = 2, 0x80000
			target.AttackVictimKnown, target.AttackVictimGuid, target.AttackAgeMs = true, s.selfGUID, 100
			switch mode {
			case "other_victim":
				target.AttackVictimGuid = 3
			case "stale_attack":
				target.AttackAgeMs = 30001
			case "absent":
				delete(f.lookupCreatures, target.Guid)
			}
			advance(200 * time.Millisecond)
			s.Tick(context.Background())
			if s.state.Practicing != (mode == "own_fight") || mode == "own_fight" && s.state.PracticePhase != "finishing_current_fight_to_level2" || countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatal("goal completion lost fight ownership or authorized another pull", s.stateLocked())
			}
			if mode == "absent" && s.state.PracticePhase != "level_two_observed" {
				t.Fatal("missing target hid the already observed goal", s.stateLocked())
			}
		})
	}
}

func TestPracticeLookupDeathAndUnselectedObservationsDoNotInventProgress(t *testing.T) {
	s, f, advance, target := lookupPracticeRig(t)
	target.Health, target.Alive = 0, false
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if s.practiceTarget != 0 || s.state.PracticePhase != "target_death_observed" || s.state.PracticeKills != 0 || s.state.PracticePulls != 0 || s.state.Moving {
		t.Fatal("observed death invented a kill receipt or pull", s.stateLocked())
	}

	s, f, _ = practiceRig(t, 8)
	f.world.RequestedCreature = f.world.NearbyCreatures[0]
	f.world.OwnerTarget = f.world.NearbyCreatures[0]
	f.world.NearbyCreatures = nil
	s.Tick(context.Background())
	if s.practiceTarget != 0 || s.state.PracticePulls != 0 || len(f.lookups) != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("unselected requested/owner observation became a discovery source", s.stateLocked())
	}
}

func TestPracticeLookupFallbackRejectsOversizedDiscovery(t *testing.T) {
	_, f, _ := practiceRig(t, 8)
	target := f.world.NearbyCreatures[0]
	f.world.NearbyCreatures = append(lookupDiscovery(nil), target)
	if practiceTargetObservation(f.world, target.Guid) != nil {
		t.Fatal("oversized discovery supplied a fallback target")
	}
}
