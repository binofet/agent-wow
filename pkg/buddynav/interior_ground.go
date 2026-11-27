package buddynav

import (
	"context"
	"errors"
	"math"
)

// At a MAP hole the actual floor belongs to collision geometry. The mesh
// can run directly over static furniture on that same layer. Check the full
// body along the path and try a short walk beside a measured obstruction.
func (p *planner) groundRoute(ctx context.Context, contains func(Point) bool, name string, start, dest Point) ([]Point, error) {
	plan := func(a, b Point) ([]Point, error) {
		return rerouteBounded(ctx, p, func() ([]Point, error) { return planWithin(ctx, p, contains, name, a, b) })
	}
	path, err := plan(start, dest)
	if err != nil {
		return nil, err
	}
	// Every sampled segment is at most two yards. Its four XY corners cover
	// all of the eight-yard hole cells a diagonal can cross.
	holes := make([]bool, len(path))
	visitsHole := false
	for i := 1; i < len(path); i++ {
		a, b := path[i-1], path[i]
		for _, q := range [...]Point{a, b, {a.X, b.Y, a.Z}, {b.X, a.Y, b.Z}} {
			tile := p.terrainTile(q)
			if tile.err != nil {
				return nil, tile.err
			}
			if tile.holeAt(q) {
				holes[i], visitsHole = true, true
				break
			}
		}
	}
	if !visitsHole {
		return path, nil
	}
	clear := func(a, b Point) (bool, error) { return p.floors.lineOfSight(ctx, a, b) }
	for i := 1; i < len(path); i++ {
		a, b := path[i-1], path[i]
		// A normal verified staircase can meet an ankle-height chord on a
		// riser. Retain that step proof on unchanged ground. On the newly
		// exposed layer, check the feet as well.
		err := p.clearGroundSweep(ctx, groundSweep{a, b, holes[i]}, clear)
		if errors.Is(err, errBodyCollision) {
			return p.clearInteriorGround(ctx, path, dest, contains, plan, clear)
		}
		if err != nil {
			return nil, err
		}
	}
	return path, nil
}

type groundSweep struct {
	from, to Point
	feet     bool
}

// Repeated ordinary requests share static geometry under this planner's gate.
// The endpoint pair and the tested body heights identify an exact result.
func (p *planner) clearGroundSweep(ctx context.Context, s groundSweep, clearRay bodyClear) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if visible, ok := p.groundSweeps[s]; ok {
		if visible {
			return nil
		}
		return errBodyCollision
	}
	bearing := math.Atan2(float64(s.to.Y-s.from.Y), float64(s.to.X-s.from.X))
	err := clearBodyAtHeights(s.from, s.to, bearing, s.feet, clearRay)
	if err != nil && !errors.Is(err, errBodyCollision) {
		return err
	}
	if p.groundSweeps == nil {
		p.groundSweeps = make(map[groundSweep]bool)
	}
	if len(p.groundSweeps) >= 2*MaxPoints {
		clear(p.groundSweeps)
	}
	p.groundSweeps[s] = err == nil
	return err
}

func (p *planner) clearInteriorGround(ctx context.Context, path []Point, dest Point, contains func(Point) bool,
	plan func(Point, Point) ([]Point, error), clear bodyClear) ([]Point, error) {
	firstBlocked := func(route []Point) (int, error) {
		for i := 1; i < len(route); i++ {
			err := p.clearGroundSweep(ctx, groundSweep{route[i-1], route[i], true}, clear)
			if errors.Is(err, errBodyCollision) {
				return i, nil
			}
			if err != nil {
				return 0, err
			}
		}
		return 0, nil
	}
	hit, err := firstBlocked(path)
	if err != nil {
		return nil, err
	}
	var walked []Point
	var walkedLength float64
	for turns := 0; hit != 0 && turns < 4; turns++ {
		a, b := path[hit-1], path[hit]
		remaining := groundRouteLength(path[hit-1:])
		bearing := math.Atan2(float64(b.Y-a.Y), float64(b.X-a.X))
		found := false
		for _, radius := range [...]float64{3, 1} {
			for _, turn := range [...]float64{math.Pi / 2, -math.Pi / 2, math.Pi / 4, -math.Pi / 4, 3 * math.Pi / 8, -3 * math.Pi / 8} {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				q := Point{a.X + float32(radius*math.Cos(bearing+turn)), a.Y + float32(radius*math.Sin(bearing+turn)), a.Z}
				if !contains(q) {
					continue
				}
				leg, e := plan(path[0], q)
				if e != nil || len(leg) < 2 || leg[0] != path[0] {
					continue
				}
				n, e := firstBlocked(leg)
				if e != nil {
					return nil, e
				}
				if n != 0 {
					continue
				}
				end := leg[len(leg)-1]
				onward, e := plan(end, dest)
				if e != nil || len(onward) < 2 || end != onward[0] {
					continue
				}
				legLength := groundRouteLength(leg)
				if len(walked)+len(leg)-1+len(onward) > MaxPoints ||
					walkedLength+legLength+groundRouteLength(onward) > MaxRouteLength {
					continue
				}
				n, e = firstBlocked(onward)
				if e != nil {
					return nil, e
				}
				if n != 0 && groundRouteLength(onward[n-1:])+1 >= remaining {
					continue
				}
				walked = append(walked, leg[:len(leg)-1]...)
				walkedLength += legLength
				path, hit, found = onward, n, true
				break
			}
			if found {
				break
			}
		}
		if !found {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if hit != 0 {
		return nil, errBodyCollision
	}
	return append(walked, path...), nil
}

func groundRouteLength(path []Point) float64 {
	var length float64
	for i := 1; i < len(path); i++ {
		length += distance(path[i-1], path[i])
	}
	return length
}
