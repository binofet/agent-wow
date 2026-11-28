package buddynav

import (
	"context"
	"errors"
	"fmt"

	"github.com/o0olele/detour-go/detour"
)

// An eroded mesh boundary can be near a real walkable floor without
// containing the observed body. Projection chooses an A* endpoint only.
// The caller must still walk the entire connection on measured ground.
func (p *planner) groundAccessPoint(q Point) (Point, error) {
	_, _, err := p.groundRef(q)
	if err == nil {
		return q, nil
	}
	if !errors.Is(err, errNoGroundPolygon) && !errors.Is(err, errOutsideGroundPolygon) {
		return Point{}, err
	}
	v := recast(q)
	extent := [3]float32{EndpointTolerance, EndpointTolerance, EndpointTolerance}
	dry := *p.filter
	dry.SetExcludeFlags(dry.GetExcludeFlags() | 8)
	var ref detour.DtPolyRef
	var nearest [3]float32
	if st := p.query.FindNearestPoly(v[:], extent[:], &dry, &ref, nearest[:]); st != detour.DT_SUCCESS || ref == 0 {
		return Point{}, err
	}
	var flags uint16
	anchor := Point{nearest[2], nearest[0], nearest[1]}
	if p.mesh.GetPolyFlags(ref, &flags) != detour.DT_SUCCESS || flags != 1 || !worldPoint(anchor) || distance(q, anchor) > EndpointTolerance {
		return Point{}, err
	}
	return anchor, nil
}

func (p *planner) groundAccessRoute(ctx context.Context, w mapWindow, name string, start, dest Point) ([]Point, error) {
	from, err := p.groundAccessPoint(start)
	if err != nil {
		return nil, fmt.Errorf("ground access at start: %w", err)
	}
	to, err := p.groundAccessPoint(dest)
	if err != nil {
		return nil, fmt.Errorf("ground access at destination: %w", err)
	}
	route, err := p.groundRoute(ctx, w.contains, name, from, to)
	if err != nil || start == from && dest == to {
		return route, err
	}
	// Check the body on the actual funnel segments, including each corner.
	clearRay := func(a, b Point) (bool, error) { return p.floors.lineOfSight(ctx, a, b) }
	for i := 1; i < len(route); i++ {
		if err := p.clearGroundSweep(ctx, groundSweep{route[i-1], route[i], true}, clearRay); err != nil {
			return nil, fmt.Errorf("body clearance on mesh corridor: %w", err)
		}
	}
	if start != from {
		access, err := p.staticWalk(ctx, start, route[0])
		if err != nil {
			return nil, fmt.Errorf("physical start connector: %w", err)
		}
		if distance(access[len(access)-1], route[0]) > .025 {
			return nil, errors.New("physical start connector does not meet the corridor floor")
		}
		route = append(access[:len(access)-1], route...)
	} else if distance(route[0], start) > .025 {
		return nil, errors.New("body is not on the corridor's starting floor")
	}
	if dest != to {
		access, err := p.staticWalk(ctx, route[len(route)-1], dest)
		if err != nil {
			return nil, fmt.Errorf("physical destination connector: %w", err)
		}
		route = append(route, access[1:]...)
	}
	if len(route) > MaxPoints || groundRouteLength(route) > MaxRouteLength {
		return nil, errors.New("ground access and mesh corridor exceed the route budget")
	}
	return route, nil
}
