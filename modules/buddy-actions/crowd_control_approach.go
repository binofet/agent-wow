package buddyactions

import (
	"context"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"math"
)

// Physical route/LoS helpers implement caller-supplied positioning constraints.
// Intent, Stealth, rank, retries and cast sequencing belong to the Lua policy.

func (s *Service) crowdControlLineOfSightLocked(ctx context.Context, from, to position) bool {
	ray := s.crowdControlSight
	if ray == nil {
		ray = func(ctx context.Context, root string, mapID uint32, a, b position) (bool, error) {
			return buddynav.LineOfSightContext(ctx, root, mapID,
				buddynav.Point{X: a.x, Y: a.y, Z: a.z + 1.5}, buddynav.Point{X: b.x, Y: b.y, Z: b.z + 1.5})
		}
	}
	clear, err := ray(ctx, s.navDataDir, s.posMap, from, to)
	if err == nil {
		return clear
	}
	// Missing static collision data permits only a verified direct ground
	// corridor as a casting heuristic. The server still checks actual LOS.
	path, err := s.mapNavigationPath(ctx, s.navDataDir, s.posMap, from, to)
	return err == nil && directRemainingRoute(from, to, path, 0)
}

func (s *Service) rangedPositionPathLocked(ctx context.Context, maximum float64, target position, plan func(position, position) []position) []position {
	// Leave room for target movement and navmesh endpoint projection. Try the
	// caster's side of the target first, then angles around a possible obstacle.
	minRange, maxRange := math.Min(4, maximum/2), maximum-.5
	radius := min(maxRange-1, max(minRange+1, distance(s.pos, target)))
	heading := math.Atan2(float64(s.pos.y-target.y), float64(s.pos.x-target.x))
	canCast := func(p position) bool {
		d := distance(p, target)
		return ctx.Err() == nil && d >= minRange && d <= maxRange && s.crowdControlLineOfSightLocked(ctx, p, target)
	}
	for _, offset := range []float64{0, -math.Pi / 4, math.Pi / 4} {
		for _, scale := range []float64{1, .75, .5} {
			if ctx.Err() != nil {
				return nil
			}
			r := max(minRange+1, radius*scale)
			goal := position{x: target.x + float32(r*math.Cos(heading+offset)), y: target.y + float32(r*math.Sin(heading+offset)), z: target.z}
			path := plan(s.pos, goal)
			if len(path) > 0 && canCast(path[len(path)-1]) {
				return path
			}
		}
	}
	// In narrow or uneven corridors the radial positions can miss walkable
	// ground. Use a stand-off waypoint from the verified route in that case.
	if ctx.Err() == nil {
		path := plan(s.pos, target)
		for i := 1; i < len(path); i++ {
			if canCast(path[i]) {
				return path[:i+1]
			}
		}
	}
	return nil
}

func sapFlankPath(from, target position, plan func(position, position) []position) []position {
	relative := func(angle, radius float64) position {
		heading := float64(target.o) + angle
		return position{target.x + float32(radius*math.Cos(heading)), target.y + float32(radius*math.Sin(heading)), target.z, target.o}
	}
	for _, offset := range []float64{0, -.6, .6} {
		rear := relative(math.Pi+offset, 2.5)
		if path := plan(from, rear); sapRearPathValid(from, target, path) {
			return path
		}
	}
	rear := relative(math.Pi, 2.5)
	for _, side := range []float64{-math.Pi / 2, math.Pi / 2} {
		first := plan(from, relative(side, 5))
		if len(first) == 0 {
			continue
		}
		if second := plan(first[len(first)-1], rear); len(second) != 0 {
			if path := append(first, second...); sapRearPathValid(from, target, path) {
				return path
			}
		}
	}
	return nil
}

func sapRearPathValid(from, target position, path []position) bool {
	if len(path) == 0 || !behindEnemy(path[len(path)-1], target) || distance(path[len(path)-1], target) > 4.5 {
		return false
	}
	// Check whole segments, including any planner start normalization, so an
	// apparent rear route cannot cut through the mob on the way to its flank.
	for _, next := range path {
		dx, dy := float64(next.x-from.x), float64(next.y-from.y)
		ax, ay := float64(target.x-from.x), float64(target.y-from.y)
		length2 := dx*dx + dy*dy
		if length2 > 0 {
			t := max(0, min(1, (ax*dx+ay*dy)/length2))
			ax, ay = ax-t*dx, ay-t*dy
		}
		if math.Hypot(ax, ay) < 1.8 {
			return false
		}
		from = next
	}
	return true
}
