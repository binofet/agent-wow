package buddynav

import (
	"math"

	"github.com/o0olele/detour-go/detour"
)

// walkableWithEndpointRetry retains the ordinary surface ray as its fast path.
// A thin polygon at a stair landing can win the midpoint nearest-poly query
// while its float32 edge rejects the ray. Try at most the two endpoint-derived
// polygons, requiring both endpoints on that same ground layer and a successful
// ray. This never searches arbitrary neighbors or accepts a blocked surface.
func (p *planner) walkable(a, b Point) error {
	primary := p.walkablePrimary(a, b)
	if primary == nil {
		return nil
	}
	var tried detour.DtPolyRef
	for _, hint := range [2]Point{a, b} {
		ref, _, err := p.groundRef(hint)
		if err != nil || ref == tried || !p.nearPoly(ref, a) || !p.nearPoly(ref, b) {
			continue
		}
		tried = ref
		if p.endpointPolygonRay(ref, a, b) {
			return nil
		}
	}
	return primary
}

func (p *planner) endpointPolygonRay(ref detour.DtPolyRef, a, b Point) bool {
	x, y := recast(a), recast(b)
	for _, v := range [2]*[3]float32{&x, &y} {
		var snapped [3]float32
		if st := p.query.ClosestPointOnPoly(ref, v[:], snapped[:], nil); detour.DtStatusFailed(st) ||
			math.Hypot(float64(snapped[0]-v[0]), float64(snapped[2]-v[2])) > 0.02 {
			return false
		}
		expectedHeight := v[1]
		if err := p.insetRayPoint(ref, &snapped); err != nil {
			return false
		}
		var height float32
		if st := p.query.GetPolyHeight(ref, snapped[:], &height); detour.DtStatusFailed(st) ||
			!finite(height) || math.Abs(float64(height-expectedHeight)) > 0.1 {
			return false
		}
		*v = snapped
	}
	var hit float32
	var visited [32]detour.DtPolyRef
	var count int
	st := p.query.Raycast(ref, x[:], y[:], p.filter, &hit, nil, visited[:], &count, len(visited))
	return !detour.DtStatusFailed(st) && st&detour.DT_BUFFER_TOO_SMALL == 0 &&
		count > 0 && finite(hit) && hit >= 1-1e-5
}
