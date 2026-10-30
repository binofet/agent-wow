package buddyactions

import (
	"math"
	"testing"
	"time"
)

func TestFormationKnownCompanionsHaveDistinctBoundedTrailingPlaces(t *testing.T) {
	now := time.Unix(1000, 0)
	owner := position{x: -8900, y: -150, z: 81}
	var points []position
	for _, identity := range []struct {
		guid  uint64
		class uint32
	}{{1, 5}, {4, 8}, {5, 4}} {
		var f, same followFormation
		first := f.Target(now, identity.guid, identity.class, owner, false)
		got := f.Target(now.Add(time.Second), identity.guid, identity.class, owner, false)
		other := same.Target(now, identity.guid, identity.class, owner, false)
		if first.Ready || !got.Ready || got.Position != other.Position || got.IdleReposition || !finite(got.Position) {
			t.Fatal(identity, first, got, other)
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
