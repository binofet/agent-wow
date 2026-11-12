package buddynav

import (
	"errors"
	"math"

	"github.com/o0olele/detour-go/detour"
)

const maxGroundCandidates = 128

// Detour's nearest-poly metric includes vertical distance. In a sloped cave it
// can prefer a nearby edge over the polygon actually containing the player's
// XY, because the navmesh and independently measured floor differ slightly.
// Keep that fast path, but if it fails our containment test inspect only the
// SAME bounded query box. This selects another containing polygon, never moves
// the requested point or widens the 2cm XY/2yd layer tolerances. Full routes
// still require all ordinary physical-floor, surface-ray and step checks.
func (p *planner) groundRef(q Point) (detour.DtPolyRef, float32, error) {
	v := recast(q)
	ext := [3]float32{0.5, 2, 0.5}
	var ref detour.DtPolyRef
	var nearest [3]float32
	primary := errors.New("no nearby ground polygon")
	if st := p.query.FindNearestPoly(v[:], ext[:], p.filter, &ref, nearest[:]); !detour.DtStatusFailed(st) && ref != 0 {
		h, err := p.groundHeightAt(ref, q)
		if err == nil {
			return ref, h, nil
		}
		primary = err
	}
	var refs [maxGroundCandidates]detour.DtPolyRef
	var count int
	st := p.query.QueryPolygons(v[:], ext[:], p.filter, refs[:], &count, len(refs))
	if detour.DtStatusFailed(st) || st&detour.DT_BUFFER_TOO_SMALL != 0 || count < 0 || count > len(refs) {
		return 0, 0, errors.New("ground polygon candidate query is incomplete")
	}
	var selected detour.DtPolyRef
	var height float32
	best := math.Inf(1)
	for _, candidate := range refs[:count] {
		h, err := p.groundHeightAt(candidate, q)
		d := math.Abs(float64(h - q.Z))
		if err == nil && d <= EndpointTolerance && (d < best || d == best && candidate < selected) {
			selected, height, best = candidate, h, d
		}
	}
	if selected == 0 {
		return 0, 0, primary
	}
	return selected, height, nil
}

func (p *planner) groundHeightAt(ref detour.DtPolyRef, q Point) (float32, error) {
	var flags uint16
	if st := p.mesh.GetPolyFlags(ref, &flags); detour.DtStatusFailed(st) || !walkSurfaceFlags(flags) {
		return 0, errors.New("polygon is not ground or shallow-wading candidate")
	}
	v := recast(q)
	var nearest [3]float32
	var over bool
	if st := p.query.ClosestPointOnPoly(ref, v[:], nearest[:], &over); detour.DtStatusFailed(st) || (!over && math.Hypot(float64(nearest[0]-v[0]), float64(nearest[2]-v[2])) > 0.02) {
		return 0, errors.New("sample is outside ground polygon")
	}
	var height float32
	if st := p.query.GetPolyHeight(ref, nearest[:], &height); detour.DtStatusFailed(st) {
		// Same existing portal-boundary retry: at most1cm inside this exact
		// ground polygon, with no terrain or neighboring-layer substitution.
		if err := p.insetRayPoint(ref, &nearest); err != nil {
			return 0, err
		}
		if st := p.query.GetPolyHeight(ref, nearest[:], &height); detour.DtStatusFailed(st) {
			return 0, errors.New("ground polygon height is unavailable")
		}
	}
	if !finite(height) {
		return 0, errors.New("ground polygon height is non-finite")
	}
	return height, nil
}
