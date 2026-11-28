package buddynav

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// PlanStaticWalkContext checks a straight walk on installed static collision
// geometry. Every foot sample must have model support and dry body clearance.
// It neither selects another destination nor returns a partial walk.
func PlanStaticWalkContext(ctx context.Context, dataDir string, mapID uint32, start, dest Point) ([]Point, error) {
	w, err := newMapWindow(mapID, start, dest)
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
	p, err := cachedMapPlanner(ctx, dir, w)
	if err != nil {
		return nil, err
	}
	return p.staticWalk(ctx, start, dest)
}

func (p *planner) staticWalk(ctx context.Context, start, dest Point) ([]Point, error) {
	// The existing body probe samples each 50 ms. For a spatial route, use
	// one sample per foot radius (0.25 yd). The movement writer owns speed.
	const samplePace = .25 / .05
	horizontal := horizontalDistance(start, dest)
	if horizontal < .01 || math.Ceil(horizontal/.25)+1 > MaxPoints {
		return nil, errors.New("static walk exceeds the local foot sample range")
	}
	floor := func(q Point, above, below float32) (float32, bool, error) {
		return p.floors.heightContext(ctx, q, q.Z, above, above+below)
	}
	clear := func(a, b Point) (bool, error) { return p.floors.lineOfSight(ctx, a, b) }
	bearing := math.Atan2(float64(dest.Y-start.Y), float64(dest.X-start.X))
	duration := time.Duration(math.Round(horizontal / samplePace * float64(time.Second)))
	steps, err := planBodyMotion(ctx, start, bearing, samplePace, duration, false, floor, clear)
	if err != nil {
		return nil, err
	}
	last := steps[len(steps)-1]
	if last.Settling || horizontalDistance(last.Point, dest) > .01 || math.Abs(float64(last.Point.Z-dest.Z)) > .5 {
		return nil, errors.New("static walk does not join both endpoints on the measured floor")
	}
	water := func(q Point, top, bottom float32) (float32, bool, error) {
		return p.deepWaterBetween(ctx, q, top, bottom)
	}
	path := make([]Point, 0, len(steps))
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h, known, err := p.terrain(step.Point)
		if err != nil {
			return nil, err
		}
		if known && h > step.Point.Z+.025 {
			return nil, errors.New("static walk intersects terrain above its supporting model")
		}
		if err := verifyDryImpulseContact(step.Point, water, clear); err != nil {
			return nil, fmt.Errorf("static walk water column: %w", err)
		}
		path = append(path, step.Point)
	}
	return path, nil
}
