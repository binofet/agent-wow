package buddynav

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/o0olele/detour-go/detour"
)

// ErrGroundDestination identifies a local formation slot without reachable
// ground. Its caller can then plan toward the actual observed owner.
var ErrGroundDestination = errors.New("destination has no reachable ground")

// Destination is the point on the mesh reached by the current strip.
func (c *GroundCorridor) Destination() Point { return c.goal }

// ContainsGroundSegment checks a local steering adjustment against the
// retained polygon strip and its detailed ground surface, in either direction.
// The corridor and its goal are left intact.
func (c *GroundCorridor) ContainsGroundSegment(ctx context.Context, from, to Point) bool {
	if ctx.Err() != nil || len(c.strip) == 0 {
		return false
	}
	first, ok := c.CellAt(from, 0, len(c.strip)-1)
	if !ok {
		return false
	}
	last, ok := c.CellAt(to, 0, len(c.strip)-1)
	if !ok {
		return false
	}
	if first > last {
		first, last = last, first
		from, to = to, from
	}
	_, err := c.surface.directGroundPath(ctx, from, to, c.strip[first:last+1])
	return err == nil
}

// FormationDestination projects a relative formation offset from the owner's
// actual polygon. A nearby bridge or lower floor cannot receive the offset by
// winning a nearest-polygon query for an invented slot altitude.
func (c *GroundCorridor) FormationDestination(ctx context.Context, owner, slot Point) (Point, error) {
	if err := ctx.Err(); err != nil {
		return Point{}, err
	}
	if c.search == nil || !worldPoint(owner) || !worldPoint(slot) {
		return Point{}, errors.New("invalid corridor or formation input")
	}
	s := c.search
	x, y := mapGrid(owner)
	if err := s.loadNeighbors(ctx, groundTile{x, y}); err != nil {
		return Point{}, err
	}
	owner, ref, err := s.bind(owner)
	if err != nil {
		return Point{}, fmt.Errorf("formation owner floor: %w", err)
	}
	a, b := recast(owner), recast(slot)
	var visited [64]detour.DtPolyRef
	var count int
	var pos [3]float32
	var hit bool
	st := s.query.MoveAlongSurface(ref, a[:], b[:], s.filter, pos[:], visited[:], &count, len(visited), &hit)
	if st != detour.DT_SUCCESS || count == 0 || math.Hypot(float64(pos[0]-b[0]), float64(pos[2]-b[2])) > .02 {
		return Point{}, ErrGroundDestination
	}
	q := Point{X: pos[2], Y: pos[0], Z: pos[1]}
	h, err := s.groundHeightAt(visited[count-1], q)
	if err != nil {
		return Point{}, fmt.Errorf("%w: %v", ErrGroundDestination, err)
	}
	q.Z = h
	return q, nil
}

// Update retains the surviving ordered polygons and their loaded tile owner.
// Each corridor belongs to one controller; its caller serializes updates.
// A changed body pose is bound in precisely the same way after ordinary
// movement, landing or server correction.
func (c *GroundCorridor) Update(ctx context.Context, from, goal Point) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.search == nil || !worldPoint(from) || !worldPoint(goal) {
		return errors.New("missing retained corridor or invalid endpoints")
	}
	s := c.search
	for _, p := range []Point{from, goal} {
		x, y := mapGrid(p)
		if err := s.loadNeighbors(ctx, groundTile{x, y}); err != nil {
			return err
		}
	}
	from, first, err := s.bind(from)
	if err != nil {
		return fmt.Errorf("rider ground polygon: %w", err)
	}
	goal, last, err := s.bind(goal)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrGroundDestination, err)
	}
	if from == c.start && goal == c.goal && first == c.strip[0] && last == c.strip[len(c.strip)-1] {
		return nil
	}

	startIndex := slices.Index(c.strip, first)
	endIndex := slices.Index(c.strip, last)
	var strip []detour.DtPolyRef
	switch {
	case first == last:
		strip = []detour.DtPolyRef{first}
	case startIndex >= 0 && endIndex >= startIndex:
		strip = c.strip[startIndex : endIndex+1]
	case startIndex >= 0 && endIndex < 0 && distance(c.goal, goal) < distance(from, goal):
		suffix, err := s.searchStrip(ctx, c.goal, goal, c.strip[len(c.strip)-1], last)
		if err != nil {
			return err
		}
		strip = joinGroundStrips(c.strip[startIndex:], suffix)
	default:
		// Off-strip body, reversed destination or a wholly different goal.
		// Reuse the streamed mesh while searching from the true current floor.
		strip, err = s.searchStrip(ctx, from, goal, first, last)
		if err != nil {
			return err
		}
	}
	out, err := s.funnel(ctx, recast(from), recast(goal), strip, false)
	if err != nil {
		return err
	}
	*c = out
	return nil
}

// A suffix for a moving target can backtrack into the retained prefix.
// Splice where it last intersects, preserving ordered links without a loop.
func joinGroundStrips(prefix, suffix []detour.DtPolyRef) []detour.DtPolyRef {
	until := make(map[detour.DtPolyRef]int, len(prefix))
	for i, ref := range prefix {
		until[ref] = i + 1
	}
	for i := len(suffix) - 1; i >= 0; i-- {
		if count, ok := until[suffix[i]]; ok {
			path := make([]detour.DtPolyRef, 0, count+len(suffix)-i-1)
			path = append(path, prefix[:count]...)
			return append(path, suffix[i+1:]...)
		}
	}
	return nil
}

// PathAhead returns one ground movement prefix for the current body and goal.
// An open chord is valid only within this route's polygon strip. At a bend,
// funnel crossings bind each XY segment to its actual detail surface.
func (c *GroundCorridor) PathAhead(ctx context.Context, from, goal Point) ([]Point, error) {
	if err := c.Update(ctx, from, goal); err != nil {
		return nil, err
	}
	if distance(c.start, c.goal) <= LegMaxLength {
		profile, err := c.surface.directGroundPath(ctx, c.start, c.goal, c.strip)
		if err == nil {
			return denseGroundPrefix(profile), nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	out, err := c.search.funnel(ctx, recast(c.start), recast(c.goal), c.strip, true)
	return out.leg, err
}

// Dense movement records are interpolated on already-profiled detail spans.
// Reaching the per-call prefix capacity leaves the full corridor intact.
func denseGroundPrefix(profile []Point) []Point {
	if len(profile) == 0 {
		return nil
	}
	path := []Point{profile[0]}
	var length float64
	for i := 1; i < len(profile); i++ {
		a, b := profile[i-1], profile[i]
		d := distance(a, b)
		if d == 0 {
			continue
		}
		n := max(1, int(math.Ceil(d)))
		for j := 1; j <= n; j++ {
			q := groundChordPoint(a, b, float32(j)/float32(n))
			step := distance(path[len(path)-1], q)
			if length+step > LegMaxLength || len(path) >= MaxPoints {
				return path
			}
			path = append(path, q)
			length += step
		}
	}
	return path
}
