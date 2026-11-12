package buddynav

import (
	"errors"
	"math"

	"github.com/o0olele/detour-go/detour"
)

type routeSurface struct {
	from, to Point
	ref      detour.DtPolyRef
}

// Raw straight-path vertices describe polygon crossings, not a physical
// straight Z chord across the detail mesh. Re-sample only the exact polygon
// entered by the complete route segment. No nearest-poly or wider-height search
// can substitute another layer here. Observed endpoints retain their2yd guards.
func (p *planner) groundOnRoute(q, from, to Point) (height float32, water, bound bool, err error) {
	var ref detour.DtPolyRef
	for _, s := range p.routeSurfaces {
		if distance(s.from, from) <= .002 && distance(s.to, to) <= .002 {
			ref = s.ref
			break
		}
	}
	if ref == 0 {
		return 0, false, false, nil
	}
	bound = true
	if !finite(q.X) || !finite(q.Y) || !finite(q.Z) {
		return 0, false, true, errors.New("non-finite route sample")
	}
	// Adaptive stair samples must remain on the same verified XY segment.
	dx, dy := float64(to.X-from.X), float64(to.Y-from.Y)
	length2 := dx*dx + dy*dy
	if length2 > 0 {
		x, y := float64(q.X-from.X), float64(q.Y-from.Y)
		t := (x*dx + y*dy) / length2
		if t < -.002 || t > 1.002 || math.Abs(x*dy-y*dx)/math.Sqrt(length2) > .02 {
			return 0, false, true, errors.New("sample leaves verified route XY segment")
		}
	} else if math.Hypot(float64(q.X-from.X), float64(q.Y-from.Y)) > .02 {
		return 0, false, true, errors.New("sample leaves verified route XY point")
	}
	var flags uint16
	if st := p.mesh.GetPolyFlags(ref, &flags); detour.DtStatusFailed(st) || !walkSurfaceFlags(flags) {
		return 0, false, true, errors.New("route polygon is not a permitted ground/wading surface")
	}
	v := recast(q)
	var nearest [3]float32
	var over bool
	if st := p.query.ClosestPointOnPoly(ref, v[:], nearest[:], &over); detour.DtStatusFailed(st) || !over && math.Hypot(float64(nearest[0]-v[0]), float64(nearest[2]-v[2])) > .02 {
		return 0, false, true, errors.New("sample leaves verified corridor polygon")
	}
	if st := p.query.GetPolyHeight(ref, nearest[:], &height); detour.DtStatusFailed(st) {
		if e := p.insetRayPoint(ref, &nearest); e != nil {
			return 0, false, true, e
		}
		if st := p.query.GetPolyHeight(ref, nearest[:], &height); detour.DtStatusFailed(st) {
			return 0, false, true, errors.New("verified route polygon height unavailable")
		}
	}
	return height, flags&8 != 0, true, nil
}
