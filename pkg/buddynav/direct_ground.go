package buddynav

import (
	"context"
	"errors"
	"math"
	"slices"

	"github.com/o0olele/detour-go/detour"
)

// directGroundPath follows a straight XY chord only when Detour traverses
// exactly the supplied, already searched corridor in order and ends on its
// destination polygon. The caller owns this mesh and may supply a contiguous
// substrip for local steering.
func (p *planner) directGroundPath(ctx context.Context, start, goal Point, corridor []detour.DtPolyRef) ([]Point, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(corridor) == 0 || !worldPoint(start) || !worldPoint(goal) {
		return nil, errors.New("missing direct ground corridor or invalid endpoints")
	}
	// A 2-D ray cannot distinguish a nearby deck from the expected floor.
	// Recheck the actual 3-D endpoints even when they fit its height tolerance.
	for _, end := range [...]struct {
		point Point
		ref   detour.DtPolyRef
	}{{start, corridor[0]}, {goal, corridor[len(corridor)-1]}} {
		found, _, err := p.groundRef(end.point)
		if err != nil || found != end.ref {
			return nil, errors.New("direct ground endpoint is outside its searched cell")
		}
	}
	a, b := recast(start), recast(goal)
	visited := make([]detour.DtPolyRef, len(corridor))
	var count int
	var hit float32
	st := p.query.Raycast(corridor[0], a[:], b[:], p.filter, &hit, nil, visited, &count, len(visited))
	// The no-wall value alone says nothing about the destination floor.
	if st != detour.DT_SUCCESS || hit != math.MaxFloat32 || count != len(corridor) || !slices.Equal(visited, corridor) {
		return nil, errors.New("direct ground ray is blocked or leaves the searched polygon strip")
	}

	var path []Point
	from := start
	var previousClimb float32
	for _, ref := range corridor {
		var tile *detour.DtMeshTile
		var poly *detour.DtPoly
		if st := p.mesh.GetTileAndPolyByRef(ref, &tile, &poly); detour.DtStatusFailed(st) {
			return nil, errors.New("direct ground polygon is unavailable")
		}
		// Clip the original chord on the exact polygon reached by the ray.
		// The funnel can turn corners; direct steering must not do so.
		var verts [detour.DT_VERTS_PER_POLYGON * 3]float32
		for i := range int(poly.VertCount) {
			n := int(poly.Verts[i]) * 3
			copy(verts[3*i:3*i+3], tile.Verts[n:n+3])
		}
		var enter, exit float32
		var inEdge, outEdge int
		if !detour.DtIntersectSegmentPoly2D(a[:], b[:], verts[:], int(poly.VertCount), &enter, &exit, &inEdge, &outEdge) ||
			horizontalDistance(from, groundChordPoint(start, goal, enter)) > .02 {
			return nil, errors.New("incomplete direct ground polygon crossing")
		}
		to := groundChordPoint(start, goal, exit)
		part, err := p.groundSegmentProfile(ctx, from, to, ref)
		if err != nil {
			return nil, err
		}
		// Independently baked tiles can have a small measured step at the
		// same portal. Retain both heights and honor both bake climb limits.
		climb := tile.Header.WalkableClimb
		if !finite(climb) || climb < 0 || (len(path) > 0 && math.Abs(float64(path[len(path)-1].Z-part[0].Z)) > float64(min(previousClimb, climb))+.001) {
			return nil, errors.New("direct ground portal exceeds the baked walkable climb")
		}
		previousClimb = climb
		for _, q := range part {
			path = appendGroundPoint(path, q)
		}
		from = to
	}
	if len(path) == 0 || distance(path[0], start) > EndpointTolerance || distance(path[len(path)-1], goal) > EndpointTolerance {
		return nil, errors.New("direct ground endpoints are on a different layer")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return path, nil
}

func groundChordPoint(a, b Point, t float32) Point {
	if t == 1 {
		return b
	}
	return Point{a.X + (b.X-a.X)*t, a.Y + (b.Y-a.Y)*t, a.Z + (b.Z-a.Z)*t}
}

// groundSegmentProfile projects a straight XY segment already clipped to one
// ground polygon. Only crossings of baked detail triangle edges can change
// the grade. Within each covered triangle the height is linear.
func (p *planner) groundSegmentProfile(ctx context.Context, from, to Point, ref detour.DtPolyRef) ([]Point, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var tile *detour.DtMeshTile
	var poly *detour.DtPoly
	if st := p.mesh.GetTileAndPolyByRef(ref, &tile, &poly); detour.DtStatusFailed(st) || poly.GetType() != detour.DT_POLYTYPE_GROUND || poly.Flags != 1 {
		return nil, errors.New("ground profile requires an ordinary ground polygon")
	}
	id := p.mesh.DecodePolyIdPoly(ref)
	if !worldPoint(from) || !worldPoint(to) || int(id) >= len(tile.DetailMeshes) {
		return nil, errors.New("ground polygon has no detail surface")
	}
	detail := tile.DetailMeshes[id]
	if detail.TriCount == 0 || uint64(detail.TriBase)+uint64(detail.TriCount) > uint64(len(tile.DetailTris)/4) ||
		uint64(detail.VertBase)+uint64(detail.VertCount) > uint64(len(tile.DetailVerts)/3) {
		return nil, errors.New("ground polygon detail data is incomplete")
	}
	vertex := func(i uint8) []float32 {
		if i < poly.VertCount {
			n := int(poly.Verts[i]) * 3
			return tile.Verts[n : n+3]
		}
		n := (int(detail.VertBase) + int(i-poly.VertCount)) * 3
		return tile.DetailVerts[n : n+3]
	}
	a, b := recast(from), recast(to)
	cuts := []float32{0, 1}
	var boundary map[float32]Point
	for j := 0; j < int(detail.TriCount); j++ {
		n := (int(detail.TriBase) + j) * 4
		tri := tile.DetailTris[n : n+3]
		for _, v := range tri {
			if int(v) >= int(poly.VertCount)+int(detail.VertCount) || (v < poly.VertCount && int(poly.Verts[v]) >= len(tile.Verts)/3) {
				return nil, errors.New("ground detail triangle vertex is unavailable")
			}
		}
		for i := range 3 {
			v := vertex(tri[i])
			// A baked boundary vertex can round one coordinate unit away
			// from a funnel edge running along it. Retain the actual vertex
			// and its grade even when a strict edge intersection misses it.
			if t, ok := groundChordVertex(from, to, v); ok {
				if boundary == nil {
					boundary = make(map[float32]Point)
				}
				boundary[t] = Point{v[2], v[0], v[1]}
				cuts = append(cuts, t)
			}
			var along, edge float32
			if detour.DtIntersectSegSeg2D(a[:], b[:], v, vertex(tri[(i+1)%3]), &along, &edge) &&
				along > 0 && along < 1 && edge >= 0 && edge <= 1 {
				cuts = append(cuts, along)
			}
		}
	}
	slices.Sort(cuts)
	cuts = slices.Compact(cuts)
	var profile []Point
	for i, cut := range cuts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := groundChordPoint(from, to, cut)
		if v, ok := boundary[cut]; ok {
			q = v
		}
		height, err := p.groundHeightAt(ref, q)
		if err != nil {
			return nil, err
		}
		q.Z = height
		if i > 0 {
			// Disconnected triangles in the same polygon do not supply support
			// across a hole, even when both boundary points have a height.
			mid := groundChordPoint(profile[len(profile)-1], q, .5)
			mh, err := p.groundHeightAt(ref, mid)
			if err != nil || math.Abs(float64(mh-(profile[len(profile)-1].Z+height)/2)) > .02 {
				return nil, errors.New("incomplete or discontinuous ground detail surface")
			}
		}
		profile = appendGroundPoint(profile, q)
	}
	return profile, nil
}

func groundChordVertex(a, b Point, v []float32) (float32, bool) {
	dx, dy := float64(b.X)-float64(a.X), float64(b.Y)-float64(a.Y)
	length2 := dx*dx + dy*dy
	if length2 == 0 {
		return 0, false
	}
	t := ((float64(v[2])-float64(a.X))*dx + (float64(v[0])-float64(a.Y))*dy) / length2
	if t <= 0 || t >= 1 {
		return 0, false
	}
	ulp := func(x float32) float64 {
		return float64(math.Nextafter32(x, float32(math.Inf(1)))) - float64(x)
	}
	near := math.Abs(float64(a.X)+t*dx-float64(v[2])) <= ulp(v[2]) &&
		math.Abs(float64(a.Y)+t*dy-float64(v[0])) <= ulp(v[0])
	return float32(t), near && float32(t) > 0 && float32(t) < 1
}

// Keep turns and changes of grade. Coplanar detail triangles can be crossed
// at the caller's observed movement speed in a single straight segment.
func appendGroundPoint(path []Point, q Point) []Point {
	n := len(path)
	if n > 0 && path[n-1] == q {
		return path
	}
	if n >= 2 {
		a, b := path[n-2], path[n-1]
		x, y, z := float64(b.X)-float64(a.X), float64(b.Y)-float64(a.Y), float64(b.Z)-float64(a.Z)
		u, v, w := float64(q.X)-float64(b.X), float64(q.Y)-float64(b.Y), float64(q.Z)-float64(b.Z)
		if x*v == y*u && x*w == z*u && y*w == z*v && x*u+y*v+z*w > 0 {
			path[n-1] = q
			return path
		}
	}
	return append(path, q)
}
