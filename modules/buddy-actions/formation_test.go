package buddyactions

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func TestFormationKnownCompanionsHaveDistinctBoundedTrailingPlaces(t *testing.T) {
	now := time.Unix(1000, 0)
	owner := position{x: -8900, y: -150, z: 81}
	var points []position
	for _, identity := range []struct {
		guid  uint64
		class uint32
	}{{1, 5}, {4, 8}, {5, 4}, {8, 9}} {
		var f, same followFormation
		first := f.Target(now, identity.guid, identity.class, owner, false)
		got := f.Target(now.Add(time.Second), identity.guid, identity.class, owner, false)
		other := same.Target(now, identity.guid, identity.class, owner, false)
		if first.Ready || !got.Ready || got.Position != other.Position || got.IdleReposition || !finite(got.Position) {
			t.Fatal(identity, first, got, other)
		}
		// A stationary owner's later facing changes must not collapse the
		// fourth companion onto another slot or make anyone orbit the owner.
		turned := owner
		turned.o = math.Pi
		idle := f.Target(now.Add(time.Minute), identity.guid, identity.class, turned, false)
		if idle.Position != got.Position || !idle.Ready || idle.IdleReposition {
			t.Fatal("idle facing changed a personal place", identity, got, idle)
		}
		d := distance(got.Position, owner)
		if d < 2 || d > 6 || got.Position.x >= owner.x || got.Position.z != owner.z {
			t.Fatal("formation outside trailing bounds", identity, d, got.Position)
		}
		for _, p := range points {
			if distance(p, got.Position) < 3 {
				t.Fatal("companions overlap", p, got.Position)
			}
		}
		points = append(points, got.Position)
	}
}

func TestFormationPlanUsesLuaHysteresisAndOwnerArrival(t *testing.T) {
	now := time.Unix(1000, 0)
	owner := position{x: 20, y: 30, z: 7}
	var f followFormation
	slot := f.Target(now, 4, 8, owner, false)
	if !slot.Valid {
		t.Fatal("builtin Lua formation did not produce a valid slot", slot)
	}
	now = now.Add(time.Second)
	between := slot.Position
	between.x += float32((slot.StartDistance + slot.StopDistance) / 2)
	stationary := f.Plan(now, 4, 8, owner, false, between, false, false)
	if !stationary.Valid || !stationary.Hold || stationary.Phase != "within_personal_follow_space" {
		t.Fatal("stationary body did not stay inside the restart band", stationary)
	}
	moving := f.Plan(now, 4, 8, owner, false, between, true, false)
	if !moving.Valid || moving.Hold || moving.Phase != "following_formation" {
		t.Fatal("moving body stopped outside its arrival band", moving)
	}
	arrived := f.Plan(now, 4, 8, owner, false, slot.Position, true, false)
	if !arrived.Valid || !arrived.Hold || arrived.Phase != "within_personal_follow_space" {
		t.Fatal("moving body did not stop at the slot", arrived)
	}
	ownerArrival := f.Plan(now, 4, 8, owner, false, slot.Position, true, true)
	if !ownerArrival.Valid || ownerArrival.Hold {
		t.Fatal("personal-slot hysteresis displaced the verified owner-floor arrival", ownerArrival)
	}
	below := slot.Position
	below.z -= 2
	otherFloor := f.Plan(now, 4, 8, owner, false, below, false, false)
	if !otherFloor.Valid || otherFloor.Hold {
		t.Fatal("horizontal proximity claimed arrival on a different floor", otherFloor)
	}
}

func TestFormationPlanReactionNeverInterruptsContinuousMovement(t *testing.T) {
	now := time.Unix(1000, 0)
	var f followFormation
	owner := position{x: 20, y: 30, z: 7}
	self := position{z: 7}
	first := f.Plan(now, 4, 8, owner, true, self, false, false)
	if !first.Valid || first.Ready || !first.Hold || first.Phase != "formation_reaction_delay" {
		t.Fatal("new stationary follow start lacked its reaction delay", first)
	}
	continuous := f.Plan(now, 4, 8, owner, true, self, true, false)
	if !continuous.Valid || continuous.Ready || continuous.Hold || continuous.Phase != "following_formation" {
		t.Fatal("reaction delay interrupted an already moving body", continuous)
	}
	ready := f.Plan(now.Add(time.Second), 4, 8, owner, true, self, false, false)
	if !ready.Valid || !ready.Ready || ready.Hold {
		t.Fatal("formation never became ready", ready)
	}
}

func TestFormationKeepsFullUnsignedIdentityAndDetachedMemory(t *testing.T) {
	now := time.Unix(1000, 0)
	owner := position{x: 20, y: 30, z: 7}
	var a, b followFormation
	first := a.Target(now, math.MaxUint64-1, 0, owner, false)
	other := b.Target(now, math.MaxUint64, 0, owner, false)
	if !first.Valid || !other.Valid || first.Position == other.Position ||
		a.memory["guid"] != "18446744073709551614" || b.memory["guid"] != "18446744073709551615" {
		t.Fatal("adjacent full-width GUIDs lost their exact identity", first, other, a.memory, b.memory)
	}
	before := a.memory
	saved := make(map[string]any, len(before))
	for key, value := range before {
		saved[key] = value
	}
	a.Target(now.Add(time.Second), math.MaxUint64-1, 0, position{o: 2}, true)
	if !reflect.DeepEqual(before, saved) {
		t.Fatal("Lua evaluation mutated the previous detached checkpoint", before, saved)
	}
	a.Reset()
	if a.memory != nil {
		t.Fatal("formation reset retained policy state", a.memory)
	}
}

func TestFormationPlanRejectsInvalidInputsWithoutUsableTarget(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, malformed := range []struct {
		name  string
		owner position
		self  position
		guid  uint64
	}{
		{"unknown identity", position{}, position{}, 0},
		{"owner NaN", position{x: float32(math.NaN())}, position{}, 4},
		{"self infinity", position{}, position{z: float32(math.Inf(1))}, 4},
		{"slot outside position bound", position{x: -40000}, position{}, 4},
	} {
		t.Run(malformed.name, func(t *testing.T) {
			var f followFormation
			f.Target(now, 4, 8, position{}, false)
			got := f.Plan(now, malformed.guid, 8, malformed.owner, false, malformed.self, false, false)
			if got.Valid || got.Ready || got.Position != (position{}) || !finite(got.Position) || f.memory != nil {
				t.Fatal("invalid Lua formation input left a usable target or state", got, f.memory)
			}
		})
	}
}

func TestFormationRuntimeFailureStopsOneBodyAndRecoversNextTick(t *testing.T) {
	now := time.Unix(1000, 0)
	var failed, sibling followFormation
	if !failed.Target(now, 4, 8, position{}, false).Valid || !sibling.Target(now, 1, 5, position{}, false).Valid {
		t.Fatal("builtin formation could not initialize")
	}
	kept := sibling.runtime
	failed.runtime = behavior.New() // An unavailable policy must fail closed.
	got := failed.Target(now.Add(time.Second), 4, 8, position{}, false)
	if got.Valid || got.Ready || got.Position != (position{}) || failed.runtime != nil || failed.memory != nil {
		t.Fatal("runtime failure retained usable formation state", got, failed)
	}
	if next := sibling.Target(now.Add(time.Second), 1, 5, position{}, false); !next.Valid || sibling.runtime != kept {
		t.Fatal("another body's runtime failure disturbed this body", next)
	}
	if next := failed.Target(now.Add(2*time.Second), 4, 8, position{}, false); !next.Valid || next.Ready {
		t.Fatal("fresh runtime did not recover with a new reaction delay", next)
	}
	kept = failed.runtime
	failed.Reset()
	if failed.memory != nil || failed.runtime != kept {
		t.Fatal("ordinary reset discarded the compiled builtin or retained memory", failed)
	}
}

func TestFormationRotatesWithTravelHeadingAndTranslatesWithOwner(t *testing.T) {
	now := time.Unix(1000, 0)
	var a, b followFormation
	p := a.Target(now, 1, 5, position{}, true).Position
	q := b.Target(now, 1, 5, position{x: 20, y: 30, z: 7, o: math.Pi / 2}, true).Position
	if math.Abs(float64(q.x-(20-p.y))) > 0.0001 || math.Abs(float64(q.y-(30+p.x))) > 0.0001 || q.z != 7 {
		t.Fatal("offset did not rotate in owner's frame", p, q)
	}
}

func TestFormationOwnerIdleTurnsAndTimeDoNotCauseOrbitOrFidget(t *testing.T) {
	now := time.Unix(1000, 0)
	var f followFormation
	p := f.Target(now, 4, 8, position{x: 10, y: 20, z: 3}, false).Position
	for i, heading := range []float32{1.2, 3.1, 5.9, 0.2, -1.4} {
		got := f.Target(now.Add(time.Duration(i+1)*time.Minute), 4, 8, position{x: 10, y: 20, z: 3, o: heading}, false)
		if !got.Ready || got.Position != p || got.IdleReposition {
			t.Fatal("stationary owner caused orbit or fidget", got, p)
		}
	}
}

func TestFormationReactionIsStaggeredAndOnlyRestartsForNewMovement(t *testing.T) {
	now := time.Unix(1000, 0)
	var priest, mage, rogue followFormation
	for _, f := range []struct {
		state *followFormation
		guid  uint64
		class uint32
	}{{&priest, 1, 5}, {&mage, 4, 8}, {&rogue, 5, 4}} {
		if f.state.Target(now, f.guid, f.class, position{}, false).Ready {
			t.Fatal("instant coordinated start")
		}
	}
	if !priest.Target(now.Add(300*time.Millisecond), 1, 5, position{}, false).Ready || mage.Target(now.Add(300*time.Millisecond), 4, 8, position{}, false).Ready || rogue.Target(now.Add(300*time.Millisecond), 5, 4, position{}, false).Ready {
		t.Fatal("companions did not retain separate response timing")
	}
	ready := now.Add(time.Second)
	if !mage.Target(ready, 4, 8, position{}, false).Ready {
		t.Fatal("formation never became ready")
	}
	if mage.Target(ready.Add(time.Second), 4, 8, position{o: 2}, true).Ready {
		t.Fatal("new owner movement did not stagger new start")
	}
	if !mage.Target(ready.Add(1500*time.Millisecond), 4, 8, position{o: 2}, true).Ready {
		t.Fatal("continuous movement repeatedly postponed reaction")
	}
}

func TestFormationIgnoresSmallMovingHeadingNoiseAndBoundsTurns(t *testing.T) {
	now := time.Unix(1000, 0)
	var f followFormation
	p := f.Target(now, 5, 4, position{}, true).Position
	for i, heading := range []float32{0.1, -0.12, 0.2, -0.15} {
		got := f.Target(now.Add(time.Duration(i+1)*200*time.Millisecond), 5, 4, position{o: heading}, true)
		if got.Position != p {
			t.Fatal("small heading noise moved personal place", got, p)
		}
	}
	turn := f.Target(now.Add(time.Second), 5, 4, position{o: math.Pi / 2}, true)
	if turn.Position.o <= 0 || turn.Position.o > math.Pi/10+0.0001 {
		t.Fatal("turn was not rate bounded", turn)
	}
	for i := 1; i <= 8; i++ {
		turn = f.Target(now.Add(time.Second+time.Duration(i)*200*time.Millisecond), 5, 4, position{o: math.Pi / 2}, true)
	}
	if math.Abs(formationAngleDifference(float64(turn.Position.o), math.Pi/2)) > 0.3 {
		t.Fatal("formation failed to follow sustained turn", turn)
	}
}

func TestFormationHysteresisHasAStableHoldBand(t *testing.T) {
	var f followFormation
	got := f.Target(time.Unix(1000, 0), 1, 5, position{}, false)
	if got.StopDistance < 0.75 || got.StopDistance > 1 || got.StartDistance < 1.7 || got.StartDistance > 2.1 || got.StartDistance-got.StopDistance < 0.8 {
		t.Fatal("no useful movement hysteresis", got)
	}
}

func TestFormationResetClockIdentityAndInvalidInputStartFresh(t *testing.T) {
	now := time.Unix(1000, 0)
	var f followFormation
	f.Target(now, 1, 5, position{}, false)
	if !f.Target(now.Add(time.Second), 1, 5, position{}, false).Ready {
		t.Fatal("not initialized")
	}
	if f.Target(now.Add(2*time.Second), 4, 8, position{}, false).Ready {
		t.Fatal("identity change retained ready state")
	}
	if f.Target(now, 4, 8, position{}, false).Ready {
		t.Fatal("clock reversal retained ready state")
	}
	f.Reset()
	if f.Target(now, 4, 8, position{}, false).Ready {
		t.Fatal("reset retained ready state")
	}
	if got := f.Target(now, 4, 8, position{x: float32(math.NaN())}, false); got.Ready || !finite(got.Position) {
		t.Fatal("nonfinite input escaped", got)
	}
	if got := f.Target(now, 0, 8, position{}, false); got.Ready {
		t.Fatal("unknown actor became ready", got)
	}
}
