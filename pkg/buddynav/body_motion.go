package buddynav

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// Normal client jump/fall physics. MovementInfo carries initial vertical speed,
// direction and horizontal speed; it never grants flight or a teleport.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Movement/Spline/MovementUtil.cpp
const BodyJumpVelocity = 7.955547
const BodyGravity = 19.29110527038574

// The ordinary body-input flight envelope also bounds gravity settling. Its
// maximum support search is derived from gravity, rather than a scene Z or
// an unrelated distance cap. The owned action has a longer finite deadline.
const BodyMotionHorizon = 1100 * time.Millisecond

type BodyMotionSample struct {
	At       time.Duration
	Point    Point
	Settling bool
}

// PlanBodyMotionContext measures one caller-selected local input independently
// of FindPath and the full route's ledge/step checks. It requires real physical
// support and body clearance at every sample, including a supported landing.
func PlanBodyMotionContext(ctx context.Context, dataDir string, mapID uint32, start Point, bearing, speed float64, duration time.Duration, jump bool) ([]BodyMotionSample, error) {
	if !worldPoint(start) || math.IsNaN(bearing) || math.IsInf(bearing, 0) || speed < .1 || speed > 14 || math.IsNaN(speed) ||
		!jump && (duration < time.Millisecond || duration > 500*time.Millisecond) {
		return nil, errors.New("finite current pose, observed speed and bounded body input required")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	end := start
	end.X += float32(math.Cos(bearing) * speed)
	end.Y += float32(math.Sin(bearing) * speed)
	window, err := newMapWindow(mapID, start, end)
	if err != nil {
		return nil, err
	}
	dir, err := mapDataDirectory(dataDir)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-currentMapPlanners.gate:
	}
	defer func() { currentMapPlanners.gate <- struct{}{} }()
	p, err := cachedMapPlanner(ctx, dir, window)
	if err != nil {
		return nil, err
	}
	floor := func(q Point, above, below float32) (float32, bool, error) {
		// heightContext measures depth from its elevated ray origin, whereas
		// this adapter's below parameter measures from q.Z itself.
		h, known, err := p.floors.heightContext(ctx, q, q.Z, above, above+below)
		if err != nil {
			return 0, false, err
		}
		if terrain, ok := p.terrain(q); ok && finite(terrain) && terrain >= q.Z-below && terrain <= q.Z+above && (!known || terrain > h) {
			h, known = terrain, true
		}
		return h, known, nil
	}
	return planBodyMotion(ctx, start, bearing, speed, duration, jump, floor, func(a, b Point) (bool, error) {
		return p.floors.lineOfSight(ctx, a, b)
	})
}

func planBodyMotion(ctx context.Context, start Point, bearing, speed float64, duration time.Duration, jump bool,
	floor func(Point, float32, float32) (float32, bool, error), clear func(Point, Point) (bool, error)) ([]BodyMotionSample, error) {
	seconds := BodyMotionHorizon.Seconds()
	maxDrop := BodyGravity * seconds * seconds / 2
	h, known, err := floor(start, .5, float32(maxDrop))
	if err != nil {
		return nil, fmt.Errorf("body start support query: start_z=%g measured_z=%g known=%t: %w", start.Z, h, known, err)
	}
	if !known || !finite(h) {
		return nil, fmt.Errorf("body start support unknown: start_z=%g measured_z=%g known=%t fall_horizon=%s", start.Z, h, known, BodyMotionHorizon)
	}
	drop := float64(start.Z) - float64(h)
	// The same measured contact band applies on either side of the observed
	// pose; float32 floor sampling may place support slightly above its Z.
	if drop < -.025 || drop > maxDrop {
		return nil, fmt.Errorf("body start outside measured support: start_z=%g measured_z=%g drop=%g maximum_gravity_drop=%g", start.Z, h, drop, maxDrop)
	}
	if drop > .025 {
		// Complete only the fall. A later Lua decision may request the step
		// or hop from the landed pose; never jump from unsupported midair.
		return planBodySettling(ctx, start, h, bearing, clear)
	}
	points := []BodyMotionSample{{Point: start}}
	limit := duration
	if jump {
		limit = BodyMotionHorizon
	}
	minimumLanding := start.Z + float32(BodyJumpVelocity*seconds-BodyGravity*seconds*seconds/2)
	maximumLanding := start.Z + float32(BodyJumpVelocity*BodyJumpVelocity/(2*BodyGravity))
	for at := 50 * time.Millisecond; at <= limit+50*time.Millisecond; at += 50 * time.Millisecond {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if at > limit {
			if jump {
				break
			}
			at = limit
		}
		seconds := at.Seconds()
		previous := points[len(points)-1].Point
		q := Point{start.X + float32(math.Cos(bearing)*speed*seconds), start.Y + float32(math.Sin(bearing)*speed*seconds), previous.Z}
		if jump {
			q.Z = start.Z + float32(BodyJumpVelocity*seconds-BodyGravity*seconds*seconds/2)
		}
		above, below := float32(.5), float32(1)
		if jump {
			// Search to the lowest landing reachable during this finite
			// flight. A fixed depth from the apex can hide valid support.
			below = float32(math.Max(0, float64(q.Z-minimumLanding)))
		}
		h, known, err = floor(q, above, below)
		if err != nil || !known || !finite(h) {
			return nil, fmt.Errorf("body probe lacks a measured physical floor: at=%s sample=(%g,%g,%g) search=[%g,%g] measured_z=%g known=%t error=%v", at, q.X, q.Y, q.Z, q.Z-below, q.Z+above, h, known, err)
		}
		landed := jump && BodyJumpVelocity-BodyGravity*seconds < 0 && q.Z <= h+.025
		if !jump {
			if math.Abs(float64(h-previous.Z)) > .5 {
				return nil, errors.New("body ground probe intersects an unsupported step")
			}
			q.Z = h
		} else if landed {
			if h < minimumLanding || h > maximumLanding || previous.Z < h-.05 {
				return nil, errors.New("body hop has no reachable nearby landing")
			}
			q.Z = h
		} else if q.Z < h-.025 {
			return nil, errors.New("body hop intersects physical floor before clearing it")
		}
		if err := clearBodyMotionSegment(previous, q, bearing, clear); err != nil {
			return nil, err
		}
		points = append(points, BodyMotionSample{At: at, Point: q})
		if landed || !jump && at >= duration {
			return points, nil
		}
	}
	return nil, errors.New("body hop did not find a supported landing within normal flight time")
}

// A fall starts at the exact observed unsupported pose, with zero initial
// vertical velocity. Every XY stays fixed and the terminal Z is the measured
// physical floor, reached after its actual normal-gravity flight time.
func planBodySettling(ctx context.Context, start Point, floor float32, bearing float64, clear func(Point, Point) (bool, error)) ([]BodyMotionSample, error) {
	flight := time.Duration(math.Sqrt(2*(float64(start.Z)-float64(floor))/BodyGravity) * float64(time.Second))
	if flight <= 0 || flight > BodyMotionHorizon {
		return nil, fmt.Errorf("body settling flight outside envelope: start_z=%g measured_z=%g duration=%s", start.Z, floor, flight)
	}
	points := []BodyMotionSample{{Point: start, Settling: true}}
	for at := 50 * time.Millisecond; ; at += 50 * time.Millisecond {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		landed := at >= flight
		if landed {
			at = flight
		}
		q := start
		seconds := at.Seconds()
		q.Z = start.Z - float32(BodyGravity*seconds*seconds/2)
		if landed {
			q.Z = floor
		}
		if err := clearBodyMotionSegment(points[len(points)-1].Point, q, bearing, clear); err != nil {
			return nil, err
		}
		points = append(points, BodyMotionSample{At: at, Point: q, Settling: true})
		if landed {
			return points, nil
		}
	}
}

func clearBodyMotionSegment(from, to Point, bearing float64, clear func(Point, Point) (bool, error)) error {
	// Parallel foot/torso/head rays bound the same short body corridor for
	// ordinary movement and gravity-only settling.
	for _, height := range []float32{.1, .9, 1.7} {
		for _, side := range []float64{-.25, 0, .25} {
			a, b := from, to
			dx, dy := float32(-math.Sin(bearing)*side), float32(math.Cos(bearing)*side)
			a.X, a.Y, a.Z = a.X+dx, a.Y+dy, a.Z+height
			b.X, b.Y, b.Z = b.X+dx, b.Y+dy, b.Z+height
			visible, err := clear(a, b)
			if err != nil || !visible {
				return errors.New("body probe corridor intersects static collision")
			}
		}
	}
	return nil
}
