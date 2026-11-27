package buddynav

import (
	"container/heap"
	"context"
	"errors"
	"math"
	"slices"

	"github.com/o0olele/detour-go/detour"
)

// Detour's Go port indexes its query pool with uint16. On the same polygon
// graph continue to support larger searches with a growable A* frontier.
// Edge weights are Euclidean centroid distances; the same metric to the goal
// is a consistent heuristic. Detour still supplies portal links and the funnel.
type groundNode struct {
	ref            detour.DtPolyRef
	center         Point
	cost, estimate float64
	parent         *groundNode
	index          int
}

type groundQueue []*groundNode

func (q groundQueue) Len() int { return len(q) }
func (q groundQueue) Less(i, j int) bool {
	a, b := q[i], q[j]
	if a.estimate == b.estimate {
		return a.ref < b.ref
	}
	return a.estimate < b.estimate
}
func (q groundQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i]; q[i].index = i; q[j].index = j }
func (q *groundQueue) Push(v any)   { n := v.(*groundNode); n.index = len(*q); *q = append(*q, n) }
func (q *groundQueue) Pop() any {
	items := *q
	last := len(items) - 1
	n := items[last]
	items[last] = nil
	*q = items[:last]
	n.index = -1
	return n
}

func groundCenter(tile *detour.DtMeshTile, p *detour.DtPoly) Point {
	var center Point
	for _, v := range p.Verts[:p.VertCount] {
		i := 3 * int(v)
		center.X += tile.Verts[i+2]
		center.Y += tile.Verts[i]
		center.Z += tile.Verts[i+1]
	}
	n := float32(p.VertCount)
	return Point{center.X / n, center.Y / n, center.Z / n}
}

func (s *groundSearch) largeCorridor(ctx context.Context, from, to detour.DtPolyRef) ([]detour.DtPolyRef, error) {
	nodes := make(map[detour.DtPolyRef]*groundNode)
	node := func(ref detour.DtPolyRef) (*groundNode, error) {
		if n := nodes[ref]; n != nil {
			return n, nil
		}
		var tile *detour.DtMeshTile
		var poly *detour.DtPoly
		if st := s.mesh.GetTileAndPolyByRef(ref, &tile, &poly); detour.DtStatusFailed(st) {
			return nil, errors.New("invalid linked ground polygon")
		}
		if poly.Flags != 1 || poly.GetType() != detour.DT_POLYTYPE_GROUND {
			return nil, nil
		}
		n := &groundNode{ref: ref, center: groundCenter(tile, poly), cost: math.Inf(1), index: -1}
		nodes[ref] = n
		return n, nil
	}
	start, err := node(from)
	if err != nil {
		return nil, err
	}
	goal, err := node(to)
	if err != nil {
		return nil, err
	}
	if start == nil || goal == nil {
		return nil, errors.New("endpoints require dry ground")
	}
	start.cost = 0
	start.estimate = distance(start.center, goal.center)
	var queue groundQueue
	heap.Push(&queue, start)
	for queue.Len() > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cur := heap.Pop(&queue).(*groundNode)
		if cur == goal {
			var path []detour.DtPolyRef
			for n := cur; n != nil; n = n.parent {
				path = append(path, n.ref)
			}
			slices.Reverse(path)
			return path, nil
		}
		if err := s.connectFrontier(ctx, cur.ref); err != nil {
			return nil, err
		}
		var tile *detour.DtMeshTile
		var poly *detour.DtPoly
		s.mesh.GetTileAndPolyByRefUnsafe(cur.ref, &tile, &poly)
		for i := poly.FirstLink; i != detour.DT_NULL_LINK; i = tile.Links[i].Next {
			link := tile.Links[i]
			if link.Ref == 0 {
				continue
			}
			next, err := node(link.Ref)
			if err != nil {
				return nil, err
			}
			if next == nil {
				continue
			}
			cost := cur.cost + distance(cur.center, next.center)
			if cost >= next.cost {
				continue
			}
			next.parent = cur
			next.cost = cost
			next.estimate = cost + distance(next.center, goal.center)
			if next.index < 0 {
				heap.Push(&queue, next)
			} else {
				heap.Fix(&queue, next.index)
			}
		}
	}
	return nil, errors.New("destination is disconnected from the start on the dry ground mesh")
}
