package buddyactions

import (
	"context"
	"math"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func planLegNavigation(ctx context.Context, root string, mapID uint32, from, to position) ([]position, error) {
	points, err := buddynav.PlanLegContext(ctx, root, mapID, buddynav.Point{X: from.x, Y: from.y, Z: from.z}, buddynav.Point{X: to.x, Y: to.y, Z: to.z})
	if err != nil {
		return nil, err
	}
	path := make([]position, len(points))
	for i, p := range points {
		path[i] = position{x: p.X, y: p.Y, z: p.Z}
	}
	return path, nil
}

// PlanRouteLeg is observational only: no lease, controller, cast, packet or
// movement is changed. A returned endpoint is guidance, never a player pose or
// permission to move. TravelTo must independently plan it under normal custody.
func (s *Service) PlanRouteLeg(ctx context.Context, req *actionsv1.PlanRouteLegRequest) (*actionsv1.RouteLeg, error) {
	if req == nil || req.SessionId == "" || len(req.SessionId) > 128 || req.SelfGuid == 0 {
		return nil, status.Error(codes.InvalidArgument, "route leg requires exact body identity")
	}
	// Capture caller values before releasing the lock for potentially slow IO.
	sid, guid, revision, mapID := req.SessionId, req.SelfGuid, req.ExpectedPositionRevision, req.MapId
	goal := position{x: req.X, y: req.Y, z: req.Z}
	if !finite(goal) {
		return nil, status.Error(codes.InvalidArgument, "route leg destination must be finite")
	}
	s.mu.Lock()
	start, rawRevision, err := s.routePositionLocked(ctx, sid, guid, mapID)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if start.Revision != revision {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "route position revision changed")
	}
	from := position{x: start.X, y: start.Y, z: start.Z}
	if distance(from, goal) > buddynav.LegMaxTargetDistance {
		s.mu.Unlock()
		return nil, status.Error(codes.InvalidArgument, "route destination exceeds 1200yd budget")
	}
	planner, root := s.planLegPath, s.navDataDir
	s.mu.Unlock()
	if planner == nil {
		return nil, status.Error(codes.FailedPrecondition, "route planner unavailable")
	}
	path, err := planner(ctx, root, mapID, from, goal)
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, navigationDiagnostic(err, root))
	}
	length, valid := validRouteLeg(path, from, goal)
	if !valid {
		return nil, status.Error(codes.FailedPrecondition, "route planner returned invalid physical leg")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, currentRaw, err := s.routePositionLocked(ctx, sid, guid, mapID)
	if err != nil {
		return nil, err
	}
	if currentRaw != rawRevision || current.Revision != revision || current.X != start.X || current.Y != start.Y || current.Z != start.Z || current.Orientation != start.Orientation || current.Estimated != start.Estimated {
		return nil, status.Error(codes.FailedPrecondition, "route position changed during planning")
	}
	end := path[len(path)-1]
	return &actionsv1.RouteLeg{
		SessionId: sid, SelfGuid: guid, MapId: mapID, Start: current,
		// No revision/age is assigned to a planned geometry point.
		Endpoint:   &actionsv1.ClientPosition{Known: true, MapKnown: true, MapId: mapID, X: end.x, Y: end.y, Z: end.z},
		PathLength: float32(length), PointCount: uint32(len(path)),
		GoalReached: distance(end, goal) <= 2, ServerPositionRevision: rawRevision,
	}, nil
}

func (s *Service) routePositionLocked(ctx context.Context, sid string, guid uint64, mapID uint32) (*actionsv1.ClientPosition, uint64, error) {
	if s.closed || s.sessionID != sid || s.selfGUID != guid {
		return nil, 0, status.Error(codes.FailedPrecondition, "route body changed or closing")
	}
	world, err := s.snapshot(ctx)
	if err != nil {
		return nil, 0, status.Error(codes.FailedPrecondition, "route world observation unavailable")
	}
	p := s.clientPositionLocked()
	if !p.Known || !p.MapKnown || p.MapId != mapID {
		return nil, 0, status.Error(codes.FailedPrecondition, "route current map or position unknown")
	}
	// Stationary baselines remain valid without recurring movement packets.
	// An actively moving pose must be recent; quiet ability intent is unrelated.
	if (s.state.Moving || world.Self.MovementFlags&0xff != 0) && p.AgeMs > 2000 {
		return nil, 0, status.Error(codes.FailedPrecondition, "route moving position stale")
	}
	return p, world.Self.PositionRevision, nil
}

func validRouteLeg(path []position, start, goal position) (float64, bool) {
	if len(path) < 2 || len(path) > 256 || distance(path[0], start) > 2 {
		return 0, false
	}
	var length float64
	for i, p := range path {
		if !finite(p) {
			return 0, false
		}
		if i > 0 {
			step := distance(path[i-1], p)
			if step > 2 || math.Abs(float64(p.z-path[i-1].z)) > 1 {
				return 0, false
			}
			length += step
		}
	}
	end := path[len(path)-1]
	return length, length <= buddynav.LegMaxLength && !(distance(end, start) <= 1 && distance(goal, start) > 3)
}
