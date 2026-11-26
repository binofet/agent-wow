package buddynav

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/azerothcore/AzerothGhost/pathfinding"
	"github.com/o0olele/detour-go/detour"
)

// SwimPoint marks the movement mode required at a verified route sample.
type SwimPoint struct {
	Point
	Swimming bool
}

// PlanSwimContext uses the current map's ground and ordinary water polygons.
// The route retains physical floors in shallow water and switches to solid-
// collision-checked submerged movement only over a verified water corridor.
func PlanSwimContext(ctx context.Context, dataDir string, mapID uint32, start, dest Point) (points []SwimPoint, err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	defer func() {
		if recover() != nil {
			points, err = nil, errors.New("invalid swim navigation data or query")
		}
	}()
	window, err := newMapWindow(mapID, start, dest)
	if err != nil {
		return nil, err
	}
	directory, err := mapDataDirectory(dataDir)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-currentMapPlanners.gate:
	}
	defer func() { currentMapPlanners.gate <- struct{}{} }()
	p, err := cachedMapPlanner(ctx, directory, window)
	if err != nil {
		return nil, err
	}
	if start.X == dest.X && start.Y == dest.Y {
		return p.planVerticalSwim(ctx, start, dest)
	}
	_, err = rerouteBounded(ctx, p, func() ([]Point, error) {
		points, err = planSwimWithin(ctx, p, window.contains, start, dest)
		return nil, err
	})
	return points, err
}

// Detour collapses a straight path with identical XY positions. A vertical
// swim still needs exact water at that XY and collision checks along the dive.
func (p *planner) planVerticalSwim(ctx context.Context, start, dest Point) ([]SwimPoint, error) {
	upper := start
	if dest.Z > start.Z {
		upper = dest
	}
	ref, surface, err := p.swimEndpoint(upper)
	if err != nil {
		return nil, err
	}
	var flags uint16
	if st := p.mesh.GetPolyFlags(ref, &flags); detour.DtStatusFailed(st) || flags&8 == 0 || upper.Z > surface {
		return nil, errors.New("vertical swimming requires an observed water column")
	}

	surfacePoint := upper
	surfacePoint.Z = surface
	p.routeSurfaces = []routeSurface{{from: surfacePoint, to: surfacePoint, ref: ref}}
	sample, err := swimSample(ctx, p, surfacePoint, surfacePoint, surfacePoint)
	if err != nil {
		return nil, err
	}
	if !sample.Swimming {
		return nil, errors.New("vertical swimming requires deeper than wading water")
	}
	// Prove that both endpoints connect to the known water surface; a ray
	// only between the endpoints could attest a room beneath the pool.
	lower := start
	if dest.Z < start.Z {
		lower = dest
	}
	a, b := sample.Point, lower
	a.Z, b.Z = a.Z+swimBodyRayHeight, b.Z+swimBodyRayHeight
	clear, err := p.swimSegmentClear(ctx, a, b)
	if err != nil {
		return nil, err
	}
	if !clear {
		return nil, errors.New("vertical swim intersects solid collision")
	}
	n := max(1, int(math.Ceil(distance(start, dest))))
	path := make([]SwimPoint, n+1)
	for i := range path {
		q := start
		q.Z += (dest.Z - start.Z) * float32(i) / float32(n)
		path[i] = SwimPoint{Point: q, Swimming: true}
	}
	return path, nil
}

type swimBackend interface {
	findSwimPath(Point, Point) (*pathfinding.PathResult, error)
	groundOnRoute(Point, Point, Point) (float32, bool, bool, error)
	terrain(Point) (float32, bool, error)
	floor(context.Context, Point, float32, float32) (float32, error)
	swimSegmentClear(context.Context, Point, Point) (bool, error)
}

func (p *planner) findSwimPath(start, dest Point) (*pathfinding.PathResult, error) {
	p.routeSurfaces = nil
	first, firstSurface, err := p.swimEndpoint(start)
	if err != nil {
		return nil, fmt.Errorf("swim start: %w", err)
	}
	last, lastSurface, err := p.swimEndpoint(dest)
	if err != nil {
		return nil, fmt.Errorf("swim destination: %w", err)
	}
	start.Z, dest.Z = firstSurface, lastSurface
	return p.findPathBetween(start, dest, first, last)
}

func (p *planner) swimEndpoint(q Point) (detour.DtPolyRef, float32, error) {
	if ref, height, err := p.groundRef(q); err == nil && math.Abs(float64(height-q.Z)) <= EndpointTolerance {
		return ref, height, nil
	}
	// A submerged endpoint can lie below ordinary nearest-ground tolerances.
	// Permit only an exact XY water polygon above that point; prove the
	// intervening column with a collision ray before returning the route.
	v := recast(q)
	ext := [3]float32{.02, MaxRouteLength, .02}
	var refs [maxGroundCandidates]detour.DtPolyRef
	var count int
	st := p.query.QueryPolygons(v[:], ext[:], p.filter, refs[:], &count, len(refs))
	if detour.DtStatusFailed(st) || st&detour.DT_BUFFER_TOO_SMALL != 0 || count < 0 || count > len(refs) {
		return 0, 0, errors.New("water polygon candidate query is incomplete")
	}
	var best detour.DtPolyRef
	var height float32
	for _, ref := range refs[:count] {
		var flags uint16
		if st := p.mesh.GetPolyFlags(ref, &flags); detour.DtStatusFailed(st) || !walkSurfaceFlags(flags) || flags&8 == 0 {
			continue
		}
		h, err := p.groundHeightAt(ref, q)
		if err == nil && h >= q.Z && (best == 0 || h < height || h == height && ref < best) {
			best, height = ref, h
		}
	}
	if best == 0 {
		return 0, 0, errors.New("no ground or water polygon at endpoint")
	}
	return best, height, nil
}

func (p *planner) swimSegmentClear(ctx context.Context, a, b Point) (bool, error) {
	return p.floors.lineOfSight(ctx, a, b)
}

// Feet stay just below the established mesh water level, leaving clearance for
// the collision chord through the body instead of tracing on the floor.
const swimImmersion = .6
const swimBodyRayHeight = .4

func planSwimWithin(ctx context.Context, b swimBackend, contains func(Point) bool, start, dest Point) ([]SwimPoint, error) {
	route, err := b.findSwimPath(start, dest)
	if err != nil {
		return nil, err
	}
	if route == nil || route.Type != pathfinding.PathfindNormal || len(route.Points) < 2 || len(route.Points) > MaxPoints {
		return nil, errors.New("swim navigation did not return a complete mesh path")
	}
	points := make([]Point, len(route.Points))
	var rawLength float64
	for i, raw := range route.Points {
		q := Point{raw.X, raw.Y, raw.Z}
		if !contains(q) {
			return nil, errors.New("swim route leaves its verified map window")
		}
		if i > 0 {
			rawLength += distance(points[i-1], q)
		}
		points[i] = q
	}
	if rawLength > MaxRouteLength || horizontalDistance(points[0], start) > .02 || horizontalDistance(points[len(points)-1], dest) > .02 {
		return nil, errors.New("swim route exceeds its distance or endpoint bounds")
	}
	out := make([]SwimPoint, 0, len(points))
	var length float64
	appendSample := func(sample SwimPoint) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !contains(sample.Point) || len(out) >= MaxPoints {
			return errors.New("swim samples exceed verified map or path bounds")
		}
		if len(out) != 0 {
			last := out[len(out)-1]
			distance := distance(last.Point, sample.Point)
			if distance < .001 && last.Swimming == sample.Swimming {
				return nil
			}
			length += distance
			if length > MaxRouteLength || distance > 2 {
				return fmt.Errorf("swim or shore movement exceeds a verified step: %+v -> %+v", last, sample)
			}
			if last.Swimming || sample.Swimming {
				a, z := last.Point, sample.Point
				a.Z, z.Z = a.Z+swimBodyRayHeight, z.Z+swimBodyRayHeight
				clear, err := b.swimSegmentClear(ctx, a, z)
				if err != nil {
					return err
				}
				if !clear {
					return errors.New("swim or shore movement intersects collision")
				}
			}
		}
		out = append(out, sample)
		return nil
	}
	vertical := func(from, to SwimPoint) error {
		n := int(math.Ceil(distance(from.Point, to.Point)))
		for i := 1; i <= n; i++ {
			p := from
			p.Z += (to.Z - from.Z) * float32(i) / float32(n)
			if err := appendSample(p); err != nil {
				return err
			}
		}
		return nil
	}
	var previousRaw Point
	var add func(q, a, z Point, depth int) error
	add = func(q, a, z Point, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		sample, err := swimSample(ctx, b, q, a, z)
		if err != nil {
			return fmt.Errorf("swim sample at %+v: %w", q, err)
		}
		if len(out) == 0 && sample.Swimming && start.Z < sample.Z {
			underwater := SwimPoint{Point: Point{sample.X, sample.Y, start.Z}, Swimming: true}
			if err := appendSample(underwater); err != nil {
				return err
			}
			if err := vertical(underwater, sample); err != nil {
				return err
			}
		}
		if len(out) != 0 {
			last := out[len(out)-1]
			if !last.Swimming && !sample.Swimming && math.Abs(float64(sample.Z-last.Z)) > 1 {
				if depth >= 4 {
					return &physicalDiscontinuity{from: previousRaw, to: q, floorFrom: last.Point, floorTo: sample.Point}
				}
				mid := Point{(previousRaw.X + q.X) / 2, (previousRaw.Y + q.Y) / 2, (previousRaw.Z + q.Z) / 2}
				if err := add(mid, a, z, depth+1); err != nil {
					return err
				}
				return add(q, a, z, depth+1)
			}
		}
		if err := appendSample(sample); err != nil {
			return err
		}
		previousRaw = q
		return nil
	}
	for i := 1; i < len(points); i++ {
		a, z := points[i-1], points[i]
		n := max(1, int(math.Ceil(distance(a, z))))
		first := 1
		if i == 1 {
			first = 0
		}
		for j := first; j <= n; j++ {
			t := float32(j) / float32(n)
			q := Point{a.X + (z.X-a.X)*t, a.Y + (z.Y-a.Y)*t, a.Z + (z.Z-a.Z)*t}
			if err := add(q, a, z, 0); err != nil {
				return nil, err
			}
		}
	}
	last := out[len(out)-1]
	if last.Swimming && dest.Z < last.Z {
		if err := vertical(last, SwimPoint{Point: Point{last.X, last.Y, dest.Z}, Swimming: true}); err != nil {
			return nil, err
		}
	}
	if distance(out[0].Point, start) > EndpointTolerance || distance(out[len(out)-1].Point, dest) > EndpointTolerance {
		return nil, errors.New("physical swimmer or shore endpoint is outside its observed layer")
	}
	return out, nil
}

func horizontalDistance(a, b Point) float64 {
	return math.Hypot(float64(a.X-b.X), float64(a.Y-b.Y))
}

func swimSample(ctx context.Context, b swimBackend, q, from, to Point) (SwimPoint, error) {
	h, water, bound, err := b.groundOnRoute(q, from, to)
	if err != nil {
		return SwimPoint{}, err
	}
	if !bound || !finite(h) {
		return SwimPoint{}, errors.New("swim sample lacks a verified corridor polygon")
	}
	terrain, known, err := b.terrain(q)
	if err != nil {
		return SwimPoint{}, err
	}
	if !known || !finite(terrain) || terrain <= -10000 {
		terrain = float32(math.NaN()) // VMAP-only dungeon floors remain usable.
	}
	floor, err := b.floor(ctx, q, h, terrain)
	if water && (errors.Is(err, errNoPhysicalFloor) || err == nil && h-floor > MaxWadingDepth) {
		q.Z = h - swimImmersion
		return SwimPoint{Point: q, Swimming: true}, nil
	}
	if err != nil {
		if errors.Is(err, errNoPhysicalFloor) {
			err = &physicalFloorGap{at: Point{q.X, q.Y, h}}
		}
		return SwimPoint{}, err
	}
	if !finite(floor) || floor < h-2 || floor > h+.5 || water && floor > h+.01 {
		return SwimPoint{}, errors.New("physical floor does not match the verified corridor layer")
	}
	q.Z = floor
	return SwimPoint{Point: q}, nil
}
