package buddyactions

import (
	"math"
	"time"
)

// formationTarget describes a personal place near the owner, not permission to
// move there. The caller retains world safety, navigation and movement control.
type formationTarget struct {
	Position       position
	StartDistance  float64
	StopDistance   float64
	Ready          bool // Gates a new start; it must not interrupt an existing move.
	IdleReposition bool // No idle fidgeting in this first formation controller.
}

// followFormation is session-local and called under the controller lock. It
// holds travel heading while the owner pauses, so turning to talk or look does
// not make the companions orbit them.
type followFormation struct {
	initialized bool
	guid        uint64
	classID     uint32
	heading     float64
	ownerMoving bool
	readyAt     time.Time
	lastAt      time.Time
}

func (f *followFormation) Reset() { *f = followFormation{} }

func formationAngle(value float64) float64 {
	value = math.Mod(value, 2*math.Pi)
	if value < 0 {
		value += 2 * math.Pi
	}
	return value
}

func formationAngleDifference(a, b float64) float64 {
	return math.Remainder(a-b, 2*math.Pi)
}

func formationIdentity(guid uint64, classID uint32) (rear, side, start, stop float64, reaction time.Duration) {
	// In the owner's heading frame, rear is behind and side is to the left.
	// The four supported roles remain more than three yards apart even with
	// their small stable identity variation. No actor aims at the owner's feet.
	switch classID {
	case 5: // Priest: trailing right.
		rear, side = 3.8, -3.1
	case 8: // Mage: further behind.
		rear, side = 5.1, 0.2
	case 4: // Rogue: closer, to the left.
		rear, side = 2.4, 3.5
	case 9: // Warlock: wider to the right, clear of the priest.
		rear, side = 1.0, -5.2
	default:
		switch guid % 3 {
		case 0:
			rear, side = 3.8, -3.1
		case 1:
			rear, side = 5.1, 0.2
		default:
			rear, side = 2.4, 3.5
		}
	}
	// A fixed integer mix avoids random state, wall-clock phase changes and
	// coordinated movement after a restart. It contains no external identity.
	h := guid + 0x9e3779b97f4a7c15
	h = (h ^ (h >> 30)) * 0xbf58476d1ce4e5b9
	h = (h ^ (h >> 27)) * 0x94d049bb133111eb
	h ^= h >> 31
	rear += (float64(h%241) - 120) / 1000
	side += (float64((h>>16)%241) - 120) / 1000
	start = 1.8 + float64((h>>32)%201)/1000
	stop = 0.8 + float64((h>>48)%151)/1000
	reaction = time.Duration(200+50*(guid%10)) * time.Millisecond
	return
}

func (f *followFormation) Target(now time.Time, selfGUID uint64, classID uint32, owner position, ownerMoving bool) formationTarget {
	rear, side, start, stop, reaction := formationIdentity(selfGUID, classID)
	out := formationTarget{StartDistance: start, StopDistance: stop}
	if selfGUID == 0 || !finite(owner) {
		f.Reset()
		return out
	}
	newIdentity := !f.initialized || f.guid != selfGUID || f.classID != classID || now.Before(f.lastAt)
	if newIdentity {
		*f = followFormation{initialized: true, guid: selfGUID, classID: classID, heading: formationAngle(float64(owner.o)), ownerMoving: ownerMoving, readyAt: now.Add(reaction), lastAt: now}
	} else if ownerMoving {
		if !f.ownerMoving {
			// A real new movement may adopt a new heading after a stationary
			// turn. Its personal delay occurs before any new follow start.
			f.heading = formationAngle(float64(owner.o))
			f.readyAt = now.Add(reaction)
		} else {
			delta := formationAngleDifference(float64(owner.o), f.heading)
			// Ignore small facing noise and bound larger changes to 90 degrees
			// per second. A late tick cannot jump around the owner in one step.
			if math.Abs(delta) > 0.3 {
				elapsed := math.Max(0, math.Min(now.Sub(f.lastAt).Seconds(), 0.4))
				limit := elapsed * math.Pi / 2
				delta = math.Max(-limit, math.Min(delta, limit))
				f.heading = formationAngle(f.heading + delta)
			}
		}
	}
	f.lastAt, f.ownerMoving = now, ownerMoving
	c, sn := math.Cos(f.heading), math.Sin(f.heading)
	out.Position = position{
		x: owner.x + float32(-rear*c-side*sn),
		y: owner.y + float32(-rear*sn+side*c),
		z: owner.z,
		o: float32(f.heading),
	}
	if !finite(out.Position) {
		f.Reset()
		out.Position = position{}
		return out
	}
	out.Ready = !now.Before(f.readyAt)
	return out
}
