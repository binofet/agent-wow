package buddyworld

import "time"

// MovementPacketBuilder::{WriteMonsterMove,WriteStopMovement,WriteCreate} and
// MoveSplineInit::{Launch,Stop} define these public 3.3.5a packet layouts.
// https://github.com/azerothcore/azerothcore-wotlk/tree/master/src/server/game/Movement/Spline
type observedTrajectory struct {
	known, destinationKnown, cyclic, relative, stopped bool
	controlReturned                                    bool
	duration, elapsed, flags                           uint32
	at                                                 time.Time
	destination                                        [3]float32
	transport                                          uint64
	seat                                               int8
}

// These modes do not establish arrival at a finite endpoint. Enter_Cycle is
// intentionally absent: the producer emits it for real cyclic flying paths.
const unsafeEndpointFlags uint32 = 0x400 | 0x400000 | 0x02000000 | 0x04000000 | 0xf0000000

const splineMotionFlags uint32 = 0x080000ff

func point(c *cursor) ([3]float32, error) {
	p := [3]float32{c.f32(), c.f32(), c.f32()}
	if c.err != nil {
		return p, c.err
	}
	for _, v := range p {
		if !validFloat(v) {
			return p, errFloat
		}
	}
	return p, nil
}

// monsterMove consumes the remainder after the packed mover GUID. It validates
// the complete tail before updating either the header position or trajectory.
func monsterMove(c *cursor, p *observed, transport bool, now time.Time) error {
	t := observedTrajectory{known: true, relative: transport, at: now}
	if transport {
		t.transport = c.guid()
		t.seat = int8(c.u8())
	}
	toggle := c.u8()
	start, err := point(c)
	if err != nil {
		return err
	}
	c.u32()
	kind := c.u8()
	if c.err != nil {
		return c.err
	}
	if toggle > 1 || kind > 4 {
		return errLayout
	}
	if kind == 1 {
		if err := c.done(); err != nil {
			return err
		}
		t.stopped = true
		t.destinationKnown = true
		t.destination = start
	} else {
		switch kind {
		case 2:
			if _, err := point(c); err != nil {
				return err
			}
		case 3:
			c.u64()
		case 4:
			if angle := c.f32(); !validFloat(angle) {
				return errFloat
			}
		}
		t.flags = c.u32()
		t.cyclic = t.flags&0x80000 != 0
		// The producer removes these fields from the active flags word; facing
		// is encoded in kind and animation ID in its own optional byte.
		if t.flags&0x381ff != 0 || t.flags&0xa00 == 0xa00 || t.flags&0x200000 != 0 && t.flags&0xa00 != 0 {
			return errLayout
		}
		if t.flags&0x200000 != 0 {
			c.u8()
			c.u32()
		}
		t.duration = c.u32()
		if t.duration == 0 {
			return errBounds
		}
		if t.flags&0x800 != 0 {
			if acceleration := c.f32(); !validFloat(acceleration) {
				return errFloat
			}
			c.u32()
		}
		nodes := c.u32()
		if c.err != nil {
			return c.err
		}
		if nodes == 0 || nodes > maxBlocks {
			return errBounds
		}
		if t.flags&0x42000 != 0 {
			if uint64(nodes)*12 > uint64(len(c.b)-c.off) {
				return errShort
			}
			for i := uint32(0); i < nodes; i++ {
				t.destination, err = point(c)
				if err != nil {
					return err
				}
			}
		} else {
			t.destination, err = point(c)
			if err != nil {
				return err
			}
			// Linear intermediate nodes are signed packed XYZ offsets around
			// (start+destination)/2; each consumes exactly one uint32.
			c.skip(int(nodes-1) * 4)
		}
		if err := c.done(); err != nil {
			return err
		}
		t.destinationKnown = !t.cyclic && t.flags&unsafeEndpointFlags == 0
	}
	if p == nil {
		return nil
	}
	if p.isSelf && (transport || !t.stopped) {
		// The controller cannot steer or report a stable self pose while the
		// server moves it. Keep the last spline for control-return recovery.
		p.positionKnown = false
		p.observedAt = now
	} else if transport {
		// Start and destination are transport-relative, not world coordinates.
		p.positionKnown = false
		p.observedAt = now
	} else {
		if err := position(p, start[0], start[1], start[2], p.orientation, now); err != nil {
			return err
		}
	}
	if transport {
		p.movementFlags |= 0x200
	} else {
		p.movementFlags &^= 0x200
	}
	p.movementKnown = p.isSelf && t.stopped && !transport
	if t.stopped {
		p.movementFlags &^= splineMotionFlags
	} else {
		p.movementFlags |= 0x08000000
	}
	p.trajectory = t
	return nil
}

func settleSelfSpline(p *observed, now time.Time) {
	t := p.trajectory
	const parabolicOrAnimation = 0x800 | 0x200000
	if !p.present || !p.mapKnown || p.positionKnown || !t.controlReturned || !t.destinationKnown || t.stopped || t.relative || t.flags&parabolicOrAnimation != 0 {
		return
	}
	arrival := t.at.Add(time.Duration(t.duration) * time.Millisecond)
	if now.Before(arrival) {
		return
	}
	latestObservation := p.observedAt
	if err := position(p, t.destination[0], t.destination[1], t.destination[2], p.orientation, arrival); err != nil {
		return
	}
	if latestObservation.After(arrival) {
		p.observedAt = latestObservation
	}
	p.movementFlags &^= splineMotionFlags
	p.movementKnown = true
}
