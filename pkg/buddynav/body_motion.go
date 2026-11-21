package buddynav

import (
	"context"
	"errors"
	"math"
	"time"
)

// Normal client jump/fall physics. MovementInfo carries initial vertical speed,
// direction and horizontal speed; it never grants flight or a teleport.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Movement/Spline/MovementUtil.cpp
const BodyJumpVelocity = 7.955547
const BodyGravity = 19.29110527038574

type BodyMotionSample struct {
	At    time.Duration
	Point Point
}

// PlanBodyMotionContext measures one caller-selected local input independently
// of FindPath and the full route's ledge/step checks. It requires real physical
// support and body clearance at every sample, including a supported landing.
func PlanBodyMotionContext(ctx context.Context, dataDir string, mapID uint32, start Point, bearing, speed float64, duration time.Duration, jump bool) ([]BodyMotionSample, error) {
	if !worldPoint(start) || math.IsNaN(bearing) || math.IsInf(bearing, 0) || speed < .1 || speed > 14 || math.IsNaN(speed) ||
		!jump && (duration < 100*time.Millisecond || duration > 500*time.Millisecond) {
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
		h, known, err := p.floors.heightContext(ctx, q, q.Z, above, below)
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
	h, known, err := floor(start, .5, .75)
	if err != nil || !known || math.Abs(float64(h-start.Z)) > .5 {
		return nil, errors.New("body probe has no observed nearby physical support")
	}
	points := []BodyMotionSample{{Point: start}}
	limit := duration
	if jump {
		limit = 1100 * time.Millisecond
	}
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
		q := Point{start.X + float32(math.Cos(bearing)*speed*seconds), start.Y + float32(math.Sin(bearing)*speed*seconds), start.Z}
		if jump {
			q.Z += float32(BodyJumpVelocity*seconds - BodyGravity*seconds*seconds/2)
		}
		above, below := float32(.5), float32(1)
		if jump {
			above, below = .5, 3
		}
		h, known, err = floor(q, above, below)
		if err != nil || !known || !finite(h) {
			return nil, errors.New("body probe lacks a measured physical floor")
		}
		previous := points[len(points)-1].Point
		landed := jump && BodyJumpVelocity-BodyGravity*seconds < 0 && q.Z <= h+.025
		if !jump {
			if math.Abs(float64(h-previous.Z)) > .5 {
				return nil, errors.New("body ground probe intersects an unsupported step")
			}
			q.Z = h
		} else if landed {
			if h < start.Z-1 || h > start.Z+float32(BodyJumpVelocity*BodyJumpVelocity/(2*BodyGravity)) || previous.Z < h-.05 {
				return nil, errors.New("body hop has no reachable nearby landing")
			}
			q.Z = h
		} else if q.Z < h-.025 {
			return nil, errors.New("body hop intersects physical floor before clearing it")
		}
		// Parallel foot/torso/head rays bound a short body corridor; a clear
		// center ray alone must not clip a wall, low ceiling or ledge corner.
		for _, height := range []float32{.1, .9, 1.7} {
			for _, side := range []float64{-.25, 0, .25} {
				a, b := previous, q
				dx, dy := float32(-math.Sin(bearing)*side), float32(math.Cos(bearing)*side)
				a.X, a.Y, a.Z = a.X+dx, a.Y+dy, a.Z+height
				b.X, b.Y, b.Z = b.X+dx, b.Y+dy, b.Z+height
				visible, err := clear(a, b)
				if err != nil || !visible {
					return nil, errors.New("body probe corridor intersects static collision")
				}
			}
		}
		points = append(points, BodyMotionSample{At: at, Point: q})
		if landed || !jump && at >= duration {
			return points, nil
		}
	}
	return nil, errors.New("body hop did not find a supported landing within normal flight time")
}
