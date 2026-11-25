package buddyactions

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"math"
)

func validTargetBandRequest(r *actionsv1.ApproachTargetRequest) bool {
	if r == nil {
		return false
	}
	minimum, clearance := float64(r.MinRangeYards), float64(r.ClearanceYards)
	if math.IsNaN(minimum) || math.IsInf(minimum, 0) || minimum < 0 || math.IsNaN(clearance) || math.IsInf(clearance, 0) || clearance < 0 {
		return false
	}
	if !r.Stop && minimum >= float64(r.RangeYards) || minimum > 0 && r.Behind || len(r.ClearanceTargetGuids) > 0 && clearance <= 0 || len(r.ClearanceTargetGuids) == 0 && clearance != 0 {
		return false
	}
	seen := map[uint64]bool{}
	for _, id := range r.ClearanceTargetGuids {
		if id == 0 || id == r.SelfGuid || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
func sameClearanceTargets(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func targetClearancePositions(w *worldv1.SnapshotResponse, ids []uint64, mapID uint32) ([]position, bool) {
	out := make([]position, 0, len(ids))
	for _, id := range ids {
		p, ok := targetMovementPosition(w, id, mapID)
		if !ok {
			return nil, false
		}
		out = append(out, p)
	}
	return out, true
}
func targetClearanceReady(p position, hazards []position, minimum float64) bool {
	for _, h := range hazards {
		if distance(p, h) < minimum {
			return false
		}
	}
	return true
}
func segmentDistance(a, b, p position) float64 {
	dx, dy, dz := float64(b.x-a.x), float64(b.y-a.y), float64(b.z-a.z)
	length := dx*dx + dy*dy + dz*dz
	t := 0.0
	if length > 0 {
		t = max(0, min(1, (float64(p.x-a.x)*dx+float64(p.y-a.y)*dy+float64(p.z-a.z)*dz)/length))
	}
	x, y, z := float64(a.x-p.x)+t*dx, float64(a.y-p.y)+t*dy, float64(a.z-p.z)+t*dz
	return math.Sqrt(x*x + y*y + z*z)
}

// Outside an exclusion, complete segments stay outside it. When already inside,
// withdrawal cannot move closer on any segment; the endpoint must clear it.
func targetBandPathValid(path []position, from, target position, minimum, maximum float64, hazards []position, clearance float64) bool {
	if len(path) < 2 || !validMapNavigationPath(path, from, path[len(path)-1]) {
		return false
	}
	end := path[len(path)-1]
	d := distance(end, target)
	if d < minimum || d > maximum || !targetClearanceReady(end, hazards, clearance) {
		return false
	}
	obstacles := append([]position(nil), hazards...)
	radii := make([]float64, len(obstacles))
	for i := range radii {
		radii[i] = clearance
	}
	if minimum > 0 {
		obstacles = append(obstacles, target)
		radii = append(radii, minimum)
	}
	for i, h := range obstacles {
		previous := from
		floor := min(radii[i], distance(from, h))
		for _, next := range path {
			if segmentDistance(previous, next, h) < floor-.05 {
				return false
			}
			// Tolerance must not accumulate across a finely sampled route.
			floor = max(floor, min(radii[i], distance(next, h)))
			previous = next
		}
	}
	return true
}
func (s *Service) targetBandPathLocked(ctx context.Context, target position, minimum, maximum float64, requireLos bool, hazards []position, clearance float64) []position {
	margin := min(.5, (maximum-minimum)/4)
	heading := math.Atan2(float64(s.pos.y-target.y), float64(s.pos.x-target.x))
	for _, offset := range []float64{0, -math.Pi / 4, math.Pi / 4, -math.Pi / 2, math.Pi / 2, -3 * math.Pi / 4, 3 * math.Pi / 4, math.Pi} {
		for _, radius := range []float64{(minimum + maximum) / 2, maximum - margin, minimum + margin} {
			if ctx.Err() != nil {
				return nil
			}
			goal := position{x: target.x + float32(radius*math.Cos(heading+offset)), y: target.y + float32(radius*math.Sin(heading+offset)), z: target.z}
			path, err := s.mapNavigationPath(ctx, s.navDataDir, s.posMap, s.pos, goal)
			if err == nil && targetBandPathValid(path, s.pos, target, minimum, maximum, hazards, clearance) && (!requireLos || s.crowdControlLineOfSightLocked(ctx, path[len(path)-1], target)) {
				return path
			}
		}
	}
	return nil
}
