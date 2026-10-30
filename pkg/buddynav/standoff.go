package buddynav

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const maxInteractionCandidates = 24

// PlanInteractionContext finds a fully verified ground route ending within
// three yards of an observed interaction point. NPC centers may occupy mesh
// clearance holes. Only nearby XY destinations at the observed Z are tried;
// every destination uses the ordinary complete-path and physical-floor checks.
func PlanInteractionContext(ctx context.Context, dataDir string, mapID uint32, start, npc Point) ([]Point, error) {
	return PlanInteractionWith(ctx, start, npc, func(call context.Context, a, b Point) ([]Point, error) {
		return PlanMapContext(call, dataDir, mapID, a, b)
	})
}

// PlanInteractionWith adapts an existing trusted ground planner. Controllers
// can retain their normal planner injection without adding a raw-route RPC.
// All attempts share one 250ms deadline and at most24 total goal queries.
func PlanInteractionWith(ctx context.Context, start, npc Point, plan func(context.Context, Point, Point) ([]Point, error)) ([]Point, error) {
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	if !worldPoint(start) || !worldPoint(npc) || plan == nil {
		return nil, errors.New("interaction navigation requires finite world points and a ground planner")
	}
	if distance(start, npc) > MaxRouteLength+3 {
		return nil, errors.New("interaction point exceeds complete route budget")
	}
	var first error
	attempts := 0
	try := func(goal Point) ([]Point, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		attempts++
		path, err := plan(ctx, start, goal)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == nil && !validInteractionRoute(path, start, goal, npc) {
			err = errors.New("interaction route failed finite, endpoint, layer or length validation")
		}
		return path, err
	}
	path, err := try(npc)
	if err == nil {
		return path, nil
	}
	first = err
	// Changing the goal can never repair an invalid start. In particular this
	// is not a snap, recovery movement, or a wider nearest-polygon query.
	if strings.Contains(err.Error(), "find navigation path: start:") || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	heading := math.Atan2(float64(start.Y-npc.Y), float64(start.X-npc.X))
	for _, radius := range []float64{1, 2, 2.5} {
		for _, eighth := range []float64{0, 1, -1, 2, -2, 3, -3, 4} {
			if attempts >= maxInteractionCandidates {
				break
			}
			angle := heading + eighth*math.Pi/4
			goal := Point{npc.X + float32(radius*math.Cos(angle)), npc.Y + float32(radius*math.Sin(angle)), npc.Z}
			path, err := try(goal)
			if err == nil {
				return path, nil
			}
			if strings.Contains(err.Error(), "find navigation path: start:") || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
		}
	}
	return nil, fmt.Errorf("no complete ground route ending within3yd of interaction point: %w", first)
}

func validInteractionRoute(path []Point, start, goal, npc Point) bool {
	if len(path) < 2 || len(path) > MaxPoints || distance(path[0], start) > EndpointTolerance || distance(path[len(path)-1], goal) > EndpointTolerance {
		return false
	}
	end := path[len(path)-1]
	if distance(end, npc) > 3 || math.Abs(float64(end.Z-npc.Z)) > EndpointTolerance {
		return false
	}
	length := distance(start, path[0])
	for i, p := range path {
		if !worldPoint(p) {
			return false
		}
		if i > 0 {
			length += distance(path[i-1], p)
		}
	}
	return length <= MaxRouteLength
}
