package buddynav

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/o0olele/detour-go/detour"
)

// A destination route may cross a disconnected mesh only on a complete measured
// trajectory. Ground and swim samples retain their movement mode; air samples
// retain the original physical time since leaving support.
type DestinationRoute struct {
	// Next follows a measured stationary settlement onto a solid intermediate floor.
	Next              *DestinationRoute
	Approach          []Point
	Fall              []BodyMotionSample
	Bearing, RunSpeed float64
	Onward            []SwimPoint
}

type destinationEdge struct {
	start         Point
	bearing, cost float64
}

// PlanWaterEntryContext connects a supported start to a lower destination via
// a swimmable landing. The terminal corridor can include an exit onto dry land.
func PlanWaterEntryContext(ctx context.Context, dir string, mapID uint32, start, dest Point, speed float64) (route DestinationRoute, err error) {
	if !worldPoint(start) || !worldPoint(dest) || speed < .1 || speed > 14 || math.IsNaN(speed) || dest.Z >= start.Z || horizontalDistance(start, dest) > MaxRouteLength {
		return route, errors.New("a finite lower destination and observed run speed are required")
	}
	defer func() {
		if recover() != nil {
			route, err = DestinationRoute{}, errors.New("invalid water entry geometry")
		}
	}()
	body := func(p Point, bearing float64) ([]BodyMotionSample, error) {
		return PlanBodyLandingContext(ctx, dir, mapID, p, bearing, speed, 500*time.Millisecond, false)
	}
	swim := func(p Point) ([]SwimPoint, error) { return PlanSwimContext(ctx, dir, mapID, p, dest) }
	direct := math.Atan2(float64(dest.Y-start.Y), float64(dest.X-start.X))
	try := func(approach []Point, bearing float64) (DestinationRoute, bool) {
		p := approach[len(approach)-1]
		length := 0.
		for i := 1; i < len(approach); i++ {
			length += distance(approach[i-1], approach[i])
		}
		// The mesh is eroded inward from collision. Measure consecutive
		// supported steps from the approached boundary to the actual rim.
		for i := 0; i < 8 && ctx.Err() == nil && length < MaxRouteLength; i++ {
			frames, e := body(p, bearing)
			if e != nil || len(frames) < 2 || frames[0].Settling {
				return DestinationRoute{}, false
			}
			last := frames[len(frames)-1]
			if last.Swimming {
				tail, e := swim(last.Point)
				if e != nil || len(tail) < 2 || !tail[0].Swimming || distance(tail[0].Point, last.Point) > .025 || distance(tail[len(tail)-1].Point, dest) > EndpointTolerance {
					return DestinationRoute{}, false
				}
				for j := 1; j < len(frames); j++ {
					length += horizontalDistance(frames[j-1].Point, frames[j].Point)
				}
				for j := 1; j < len(tail); j++ {
					length += distance(tail[j-1].Point, tail[j].Point)
				}
				if length > MaxRouteLength {
					return DestinationRoute{}, false
				}
				return DestinationRoute{Approach: approach, Fall: frames, RunSpeed: speed, Bearing: bearing, Onward: tail}, true
			}
			// A shorter dry fall is not a walkable chord.
			if last.FallTime != 0 || last.Point == p {
				return DestinationRoute{}, false
			}
			for _, frame := range frames[1:] {
				if frame.FallTime != 0 || frame.Settling {
					return DestinationRoute{}, false
				}
				length += distance(p, frame.Point)
				approach = append(approach, frame.Point)
				p = frame.Point
			}
		}
		return DestinationRoute{}, false
	}
	if r, ok := try([]Point{start}, direct); ok {
		return r, nil
	}
	edges, e := destinationEdges(ctx, dir, mapID, start, dest)
	if e != nil {
		return route, e
	}
	for _, edge := range edges {
		if e := ctx.Err(); e != nil {
			return route, e
		}
		approach, e := PlanMapContext(ctx, dir, mapID, start, edge.start)
		if e != nil || len(approach) < 2 {
			continue
		}
		if r, ok := try(approach, edge.bearing); ok {
			return r, nil
		}
	}
	if e := ctx.Err(); e != nil {
		return route, e
	}
	return route, errors.New("no connected, collision-verified water entry to destination")
}

// Only boundaries of polygons reachable from the observed upper layer nominate
// entry sites. A Detour edge is a search hint, never a permitted airborne chord.
func destinationEdges(ctx context.Context, dir string, mapID uint32, start, dest Point) ([]destinationEdge, error) {
	projection := dest
	projection.Z = start.Z
	w, e := newMapWindow(mapID, start, projection)
	if e != nil {
		return nil, e
	}
	dir, e = mapDataDirectory(dir)
	if e != nil {
		return nil, e
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-currentMapPlanners.gate:
	}
	defer func() { currentMapPlanners.gate <- struct{}{} }()
	p, e := cachedMapPlanner(ctx, dir, w)
	if e != nil {
		return nil, e
	}
	first, _, e := p.groundRef(start)
	if e != nil {
		return nil, e
	}
	v := recast(start)
	var refs [MaxPoints]detour.DtPolyRef
	var count int
	st := p.query.FindPolysAroundCircle(first, v[:], float32(math.Min(MaxRouteLength, math.Max(12, distance(start, dest)))), p.filter, refs[:], nil, nil, &count, len(refs))
	if detour.DtStatusFailed(st) {
		return nil, fmt.Errorf("cannot inspect reachable ground polygons: %v", st)
	}
	var edges []destinationEdge
	for _, ref := range refs[:count] {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		var t *detour.DtMeshTile
		var poly *detour.DtPoly
		if detour.DtStatusFailed(p.mesh.GetTileAndPolyByRef(ref, &t, &poly)) || poly.VertCount < 3 || poly.Flags != 1 {
			continue
		}
		var cx, cy float64
		for _, idx := range poly.Verts[:poly.VertCount] {
			k := int(idx) * 3
			cx += float64(t.Verts[k+2])
			cy += float64(t.Verts[k])
		}
		cx /= float64(poly.VertCount)
		cy /= float64(poly.VertCount)
		var walls [32 * 6]float32
		var n int
		if detour.DtStatusFailed(p.query.GetPolyWallSegments(ref, p.filter, walls[:], nil, &n, len(walls)/6)) {
			continue
		}
		for i := 0; i < n; i++ {
			a := walls[i*6:]
			dx, dy := float64(a[5]-a[2]), float64(a[3]-a[0])
			length := math.Hypot(dx, dy)
			if length < .5 {
				continue
			}
			nx, ny := -dy/length, dx/length
			if (float64(a[2])-cx)*nx+(float64(a[0])-cy)*ny < 0 {
				nx, ny = -nx, -ny
			}
			f := math.Max(.1, math.Min(.9, ((float64(dest.X)-float64(a[2]))*dx+(float64(dest.Y)-float64(a[0]))*dy)/(length*length)))
			for _, frac := range []float64{f, .5} {
				x, y := float64(a[2])+frac*dx, float64(a[0])+frac*dy
				pos := Point{float32(x - .5*nx), float32(y - .5*ny), float32(float64(a[1]) + frac*float64(a[4]-a[1]))}
				h, e := p.groundHeightAt(ref, pos)
				if e != nil {
					continue
				}
				pos.Z = h
				if pos.Z <= dest.Z {
					continue
				}
				cost := distance(start, pos) + horizontalDistance(pos, dest)
				if cost > MaxRouteLength {
					continue
				}
				toward := math.Atan2(float64(dest.Y-pos.Y), float64(dest.X-pos.X))
				if nx*math.Cos(toward)+ny*math.Sin(toward) > .2 {
					edges = append(edges, destinationEdge{pos, toward, cost})
				}
				edges = append(edges, destinationEdge{pos, math.Atan2(ny, nx), cost})
			}
		}
	}
	sort.SliceStable(edges, func(i, j int) bool { return edges[i].cost < edges[j].cost })
	return edges, nil
}
