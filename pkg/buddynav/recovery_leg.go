package buddynav

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/o0olele/detour-go/detour"

	"github.com/azerothcore/AzerothGhost/pathfinding"
)

const (
	RecoveryMaxTargetDistance = 1200.0
	RecoveryMaxLegLength      = 180.0
	recoveryCorridorLength    = 2400.0
	recoveryCorridorNodes     = 16384
)

// PlanRecoveryLegContext returns one physically verified short leg toward an
// observed corpse on the actual current map. A complete Detour corridor to the
// actual corpse supplies guidance only; no unsampled corridor is returned for
// movement. Every returned leg uses the unchanged normal floor/ray/water/step
// sampler, at most 256 points and 180yd. No direct-line or guessed-height route
// is available when the complete corridor, local assets or physical proof fail.
// All corridor/leg attempts share one second and the same nine-tile/24MiB cache.
func PlanRecoveryLegContext(ctx context.Context, dataDir string, mapID uint32, start, corpse Point) (points []Point, err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	defer func() {
		if recover() != nil {
			points, err = nil, errors.New("invalid recovery navigation data or query")
		}
	}()
	w, err := newRecoveryWindow(mapID, start, corpse)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
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
	// A long complete corridor needs a larger bounded topology search than a
	// normal short route. Keep its query separate: every physical leg still uses
	// the cached normal 4096-node query, and the gate makes both exclusive.
	normal := p.query
	guide := detour.DtAllocNavMeshQuery()
	if st := guide.Init(p.mesh, recoveryCorridorNodes); detour.DtStatusFailed(st) {
		return nil, errors.New("initialize bounded recovery corridor query")
	}
	p.query = guide
	defer func() { p.query = normal }()
	return recoveryLeg(ctx, p, w, start, corpse, func(goal Point) ([]Point, error) {
		p.query = normal
		defer func() { p.query = guide }()
		return rerouteBounded(ctx, p, func() ([]Point, error) {
			return planWithin(ctx, p, w.contains, fmt.Sprintf("map %d recovery tile window", mapID), start, goal)
		})
	})
}

func newRecoveryWindow(mapID uint32, start, corpse Point) (mapWindow, error) {
	if !clientMap(mapID) || !worldPoint(start) || !worldPoint(corpse) {
		return mapWindow{}, errors.New("recovery navigation requires a valid client map and finite world points")
	}
	if distance(start, corpse) > RecoveryMaxTargetDistance {
		return mapWindow{}, errors.New("observed corpse exceeds 1200yd recovery target budget")
	}
	x, y := mapGrid(Point{(start.X + corpse.X) / 2, (start.Y + corpse.Y) / 2, (start.Z + corpse.Z) / 2})
	w := mapWindow{mapID: mapID, x: x, y: y}
	if !w.contains(start) || !w.contains(corpse) {
		return mapWindow{}, errors.New("recovery endpoints exceed bounded nine-tile window")
	}
	return w, nil
}

// Physical is deliberately a separate step: the long corridor never becomes
// movement authority. Capture all guidance points before short-leg replanning
// replaces the planner's route-surface bindings.
func recoveryLeg(ctx context.Context, p *planner, w mapWindow, start, corpse Point, physical func(Point) ([]Point, error)) ([]Point, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r, err := p.findPath(start, corpse)
	if err != nil {
		return nil, fmt.Errorf("complete recovery corridor: %w", err)
	}
	if r == nil || r.Type != pathfinding.PathfindNormal || len(r.Points) < 2 || len(r.Points) > MaxPoints {
		return nil, errors.New("recovery requires a complete normal bounded mesh corridor")
	}
	raw := make([]Point, len(r.Points))
	var length float64
	for i, v := range r.Points {
		raw[i] = Point{v.X, v.Y, v.Z}
		if !w.contains(raw[i]) {
			return nil, errors.New("recovery corridor leaves local tile window")
		}
		if i > 0 {
			length += distance(raw[i-1], raw[i])
		}
	}
	if distance(raw[0], start) > EndpointTolerance || distance(raw[len(raw)-1], corpse) > EndpointTolerance || length > recoveryCorridorLength {
		return nil, errors.New("recovery corridor endpoint or 2400yd length bound failed")
	}
	var candidates []Point
	for _, budget := range []float64{150, 100, 50} {
		var goal Point
		if length <= budget {
			goal = corpse
		} else {
			goal, err = recoveryCorridorPoint(p, raw, budget)
			if err != nil {
				return nil, err
			}
		}
		duplicate := false
		for _, old := range candidates {
			if distance(old, goal) < .01 {
				duplicate = true
				break
			}
		}
		if !duplicate {
			candidates = append(candidates, goal)
		}
	}
	var last error
	for _, goal := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path, err := physical(goal)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err == nil && validRecoveryLeg(path, w, start, goal, corpse) {
			return path, nil
		}
		if err == nil {
			err = errors.New("physical recovery leg exceeded 180yd or endpoint bounds")
		}
		last = err
	}
	return nil, fmt.Errorf("no verified recovery leg: %w", last)
}

func recoveryCorridorPoint(p *planner, raw []Point, budget float64) (Point, error) {
	for i := 1; i < len(raw); i++ {
		a, b := raw[i-1], raw[i]
		d := distance(a, b)
		if d <= .001 {
			continue
		}
		if budget > d {
			budget -= d
			continue
		}
		f := float32(budget / d)
		q := Point{a.X + (b.X-a.X)*f, a.Y + (b.Y-a.Y)*f, a.Z + (b.Z-a.Z)*f}
		height, _, bound, err := p.groundOnRoute(q, a, b)
		if err != nil || !bound || !finite(height) {
			return Point{}, errors.New("recovery waypoint lacks exact corridor polygon height")
		}
		q.Z = height
		return q, nil
	}
	return Point{}, errors.New("recovery corridor has no progressing waypoint")
}

func validRecoveryLeg(path []Point, w mapWindow, start, goal, corpse Point) bool {
	if len(path) < 2 || len(path) > MaxPoints || distance(path[0], start) > EndpointTolerance || distance(path[len(path)-1], goal) > EndpointTolerance {
		return false
	}
	if distance(path[len(path)-1], start) <= 1 && distance(corpse, start) > 3 {
		return false
	}
	var length float64
	for i, q := range path {
		if !w.contains(q) {
			return false
		}
		if i > 0 {
			step := distance(path[i-1], q)
			if step > 2 || math.Abs(float64(path[i-1].Z-q.Z)) > 1 {
				return false
			}
			length += step
		}
	}
	return length <= RecoveryMaxLegLength
}
