package buddyactions

import (
	"context"
	"math"
	"slices"
	"strings"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
)

type swimTrail struct {
	mapID           uint32
	visit, revision uint64
	points          []buddynav.Point
}

func swimPoint(p position) buddynav.Point {
	return buddynav.Point{X: p.x, Y: p.y, Z: p.z}
}

func (t *swimTrail) observe(w *worldv1.SnapshotResponse, selfGUID uint64) {
	self, owner := w.GetSelf(), w.GetOwner()
	if self.GetGuid() != selfGUID || !self.GetMapKnown() {
		return
	}
	if t.visit != w.VisitRevision || t.mapID != self.MapId || owner.GetMapKnown() && owner.GetMapId() != self.MapId {
		*t = swimTrail{mapID: self.MapId, visit: w.VisitRevision}
	}
	if owner.GetGuid() != ownerGUID || !owner.GetPresent() || !owner.GetAlive() ||
		!owner.GetMapKnown() || owner.MapId != self.MapId || !owner.GetPositionKnown() ||
		!owner.GetMovementFlagsKnown() || owner.MovementFlags&swimMovementFlag == 0 ||
		!swimFollowCompatible(owner.MovementFlags, owner.ExtraMovementFlags) ||
		owner.PositionAgeMs > 2000 || t.revision == owner.PositionRevision ||
		!finite(playerPosition(owner)) {
		return
	}
	p := swimPoint(playerPosition(owner))
	t.revision = owner.PositionRevision
	if len(t.points) != 0 {
		d := trailDistance(t.points[len(t.points)-1], p)
		if d < .1 {
			return
		}
		// A teleport, visibility gap or discontinuous track cannot create
		// a shortcut with unobserved depth between its endpoints.
		if d > buddynav.MaxSwimTrailStep {
			t.points = t.points[:0]
		}
	}
	if len(t.points) == buddynav.MaxPoints {
		t.points = slices.Delete(t.points, 0, 1)
	}
	t.points = append(t.points, p)
}

func trailDistance(a, b buddynav.Point) float64 {
	return math.Hypot(math.Hypot(float64(a.X-b.X), float64(a.Y-b.Y)), float64(a.Z-b.Z))
}

func disconnectedSwimRoute(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "path is partial") ||
		strings.Contains(err.Error(), "no ground or water polygon at endpoint"))
}

// Join previously observed water through an ordinary mixed route, then use
// the actual submerged poses of the leader. Surface projection cannot model
// an opening underneath a wall that separates two Detour lakes.
func (s *Service) swimRouteViaLeaderLocked(ctx context.Context, target buddynav.Point, plan func(context.Context, string, uint32, buddynav.Point, buddynav.Point) ([]buddynav.SwimPoint, error)) []buddynav.SwimPoint {
	return s.swimTrail.routeFrom(ctx, s.navDataDir, s.posMap, swimPoint(s.pos), target, s.swimming, s.lastWorld.GetOwner(), plan)
}

func (t swimTrail) routeFrom(ctx context.Context, dataDir string, mapID uint32, start, target buddynav.Point, swimming bool, goal *worldv1.PlayerState, plan func(context.Context, string, uint32, buddynav.Point, buddynav.Point) ([]buddynav.SwimPoint, error)) []buddynav.SwimPoint {
	if len(t.points) < 2 || t.mapID != mapID {
		return nil
	}
	last := t.points[len(t.points)-1]
	if trailDistance(last, target) > buddynav.MaxSwimTrailStep || goal.GetGuid() != ownerGUID || !goal.GetPresent() {
		return nil
	}
	type joinCandidate struct {
		i        int
		distance float64
	}
	joins := make([]joinCandidate, len(t.points))
	for i, p := range t.points {
		joins[i] = joinCandidate{i, trailDistance(start, p)}
	}
	slices.SortStableFunc(joins, func(a, b joinCandidate) int {
		switch {
		case a.distance < b.distance:
			return -1
		case a.distance > b.distance:
			return 1
		default:
			return b.i - a.i
		}
	})
	call, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	for _, candidate := range joins {
		if call.Err() != nil {
			return nil
		}
		tail := t.points[candidate.i:]
		var path []buddynav.SwimPoint
		proof := tail
		if swimming && candidate.distance <= buddynav.MaxSwimTrailStep {
			proof = make([]buddynav.Point, 1, len(tail)+1)
			proof[0] = start
			proof = append(proof, tail...)
			path = []buddynav.SwimPoint{{Point: start, Swimming: true}}
		} else {
			joined, err := plan(call, dataDir, mapID, start, tail[0])
			if err != nil || len(joined) == 0 || !joined[len(joined)-1].Swimming {
				continue
			}
			path = joined
		}
		if len(proof) < 2 || buddynav.ValidateSwimTrailContext(call, dataDir, mapID, proof) != nil {
			continue
		}
		for _, p := range proof {
			if trailDistance(path[len(path)-1].Point, p) > .005 {
				path = append(path, buddynav.SwimPoint{Point: p, Swimming: true})
			}
		}
		if goal.MovementFlags&swimMovementFlag == 0 && trailDistance(last, target) > .005 {
			shore, err := plan(call, dataDir, mapID, last, target)
			if err != nil || len(shore) == 0 || shore[len(shore)-1].Swimming {
				continue
			}
			path = append(path, shore...)
		}
		if len(path) <= buddynav.MaxPoints {
			return path
		}
	}
	return nil
}
