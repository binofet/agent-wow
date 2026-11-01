package buddyactions

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func routeLegRig(t *testing.T) (*Service, *fakeSession, *actionsv1.PlanRouteLegRequest) {
	t.Helper()
	s, f, _ := formationRig(t, 4, 8)
	s.sessionID = "route-test-body"
	f.world.Owner = nil // The observer needs no owner or party authority.
	if _, err := s.snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.planLegPath = func(_ context.Context, _ string, _ uint32, from, goal position) ([]position, error) {
		end := goal
		if end.x-from.x > 150 {
			end.x = from.x + 150
		}
		return denseRouteLeg(from, end), nil
	}
	p := s.clientPositionLocked()
	return s, f, &actionsv1.PlanRouteLegRequest{SessionId: s.sessionID, SelfGuid: 4, ExpectedPositionRevision: p.Revision, MapId: p.MapId, X: p.X + 300, Y: p.Y, Z: p.Z}
}

func denseRouteLeg(from, to position) []position {
	n := int(math.Ceil(distance(from, to)))
	if n < 1 {
		n = 1
	}
	path := make([]position, n+1)
	for i := range path {
		f := float32(i) / float32(n)
		path[i] = position{x: from.x + (to.x-from.x)*f, y: from.y + (to.y-from.y)*f, z: from.z + (to.z-from.z)*f}
	}
	return path
}

func TestRouteLegReadOnlyWithLiveIntentsAndNoOwner(t *testing.T) {
	s, f, req := routeLegRig(t)
	s.state.Following, s.state.Healing, s.state.Assisting, s.state.Buffing = true, true, true, true
	s.state.CastPending, s.state.Traveling = true, true
	s.state.InteractionOwner, s.interactionToken = "professions", "existing-token"
	s.interactionUntil = s.now().Add(-time.Second) // A query must not expire it.
	f.world.Self.Alive = false                     // Geometry is not death-controller authority.
	f.world.Self.Health = 0
	f.world.Self.PositionAgeMs = 90000 // A quiet baseline need not heartbeat.
	before := proto.Clone(&s.state)
	out, err := s.PlanRouteLeg(context.Background(), req)
	if err != nil || out.SessionId != req.SessionId || out.SelfGuid != 4 || out.Start.X != f.world.Self.X || out.Endpoint.X != f.world.Self.X+150 || out.PointCount != 151 || out.PathLength != 150 || out.GoalReached || out.ServerPositionRevision != 1 {
		t.Fatal(out, err)
	}
	if len(f.packets) != 0 || f.clock != 0 || s.interactionToken != "existing-token" || !proto.Equal(before, &s.state) {
		t.Fatal("read changed controller state or wrote packets", s.state.String(), f.packets)
	}
	req.X = out.Endpoint.X
	out, err = s.PlanRouteLeg(context.Background(), req)
	if err != nil || !out.GoalReached {
		t.Fatal("short complete goal", out, err)
	}
}

func TestRouteLegUsesActualClientEstimateAndPreservesRawPose(t *testing.T) {
	s, f, req := routeLegRig(t)
	s.posKnown, s.posMap, s.positionRevision = true, req.MapId, f.world.Self.PositionRevision
	s.pos = playerPosition(f.world.Self)
	s.pos.x += 40
	s.posEstimated, s.posAt, s.posRevision = true, s.now(), 37
	req.ExpectedPositionRevision, req.X = 37, s.pos.x+300
	out, err := s.PlanRouteLeg(context.Background(), req)
	if err != nil || !out.Start.Estimated || out.Start.Revision != 37 || out.Start.X != s.pos.x || out.Endpoint.X != s.pos.x+150 || f.world.Self.X == out.Start.X {
		t.Fatal(out, err)
	}
}

func TestRouteLegAllowsFreshMovementAndCapturesRequestValues(t *testing.T) {
	s, f, req := routeLegRig(t)
	s.state.Moving = true
	f.world.Self.MovementFlags, f.world.Self.PositionAgeMs = 1, 1000
	original := proto.Clone(req).(*actionsv1.PlanRouteLegRequest)
	s.planLegPath = func(_ context.Context, _ string, mapID uint32, from, goal position) ([]position, error) {
		if mapID != original.MapId || goal.x != original.X {
			t.Fatal("planner received caller-mutated values")
		}
		// The query releases its lock to plan. Its post-check must retain the
		// original request identity and goal rather than reread caller storage.
		req.SessionId, req.SelfGuid, req.ExpectedPositionRevision = "replacement", 87, 100
		req.MapId, req.X = 1, from.x+150
		end := from
		end.x += 150
		return denseRouteLeg(from, end), nil
	}
	out, err := s.PlanRouteLeg(context.Background(), req)
	if err != nil || out.SessionId != original.SessionId || out.SelfGuid != original.SelfGuid || out.GoalReached || !s.state.Moving || len(f.packets) != 0 {
		t.Fatal("query stopped motion or changed its request binding", out, err)
	}
}

func TestRouteLegRejectsStaleOrUnknownBindingsBeforePlanner(t *testing.T) {
	for _, mode := range []string{"nil", "sid", "guid", "revision", "map", "unknown", "motion_unknown", "moving_stale", "nan", "budget", "closing", "world_error"} {
		t.Run(mode, func(t *testing.T) {
			s, f, req := routeLegRig(t)
			calls := 0
			s.planLegPath = func(context.Context, string, uint32, position, position) ([]position, error) {
				calls++
				return nil, nil
			}
			switch mode {
			case "nil":
				req = nil
			case "sid":
				req.SessionId = "another-body"
			case "guid":
				req.SelfGuid++
			case "revision":
				req.ExpectedPositionRevision++
			case "map":
				req.MapId = 1
			case "unknown":
				f.world.Self.PositionKnown = false
			case "motion_unknown":
				s.state.MotionUnconfirmed = true
			case "moving_stale":
				f.world.Self.MovementFlags, f.world.Self.PositionAgeMs = 1, 2001
			case "nan":
				req.Z = float32(math.NaN())
			case "budget":
				req.X = f.world.Self.X + 1201
			case "closing":
				s.closed = true
			case "world_error":
				f.failSnapshot = true
			}
			if _, err := s.PlanRouteLeg(context.Background(), req); err == nil || calls != 0 || len(f.packets) != 0 {
				t.Fatal("unbound planner or write", calls, err, f.packets)
			}
		})
	}
}

func TestRouteLegRechecksBodyAndPoseAfterPlanning(t *testing.T) {
	for _, mode := range []string{"raw_revision", "raw_position", "map", "sid", "closed", "client_revision", "client_position", "uncertainty", "world_error"} {
		t.Run(mode, func(t *testing.T) {
			s, f, req := routeLegRig(t)
			s.planLegPath = func(_ context.Context, _ string, _ uint32, from, _ position) ([]position, error) {
				end := from
				end.x += 150
				switch mode {
				case "raw_revision":
					f.world.Self.PositionRevision++
				case "raw_position":
					f.world.Self.X++
				case "map":
					f.world.Self.MapId++
				case "sid":
					s.sessionID = "replacement"
				case "closed":
					s.closed = true
				case "client_revision", "client_position":
					s.posKnown, s.posMap, s.positionRevision = true, req.MapId, f.world.Self.PositionRevision
					s.pos, s.posAt, s.posRevision = from, s.now(), req.ExpectedPositionRevision
					if mode == "client_revision" {
						s.posRevision++
					} else {
						s.pos.x++
					}
				case "uncertainty":
					s.state.MotionUnconfirmed = true
				case "world_error":
					f.failSnapshot = true
				}
				return denseRouteLeg(from, end), nil
			}
			if _, err := s.PlanRouteLeg(context.Background(), req); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
				t.Fatal("returned stale authority", err, f.packets)
			}
		})
	}
}

func TestRouteLegRejectsMalformedOutputAndCancellation(t *testing.T) {
	for _, mode := range []string{"missing", "nan", "step", "height", "length", "start", "no_progress", "error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, f, req := routeLegRig(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.planLegPath = func(_ context.Context, _ string, _ uint32, from, _ position) ([]position, error) {
				end := from
				end.x += 150
				path := denseRouteLeg(from, end)
				switch mode {
				case "missing":
					return nil, nil
				case "nan":
					path[50].z = float32(math.NaN())
				case "step":
					path[50].x += 5
				case "height":
					path[50].z += 1.1
				case "length":
					end.x = from.x + 181
					path = denseRouteLeg(from, end)
				case "start":
					path[0].x -= 3
				case "no_progress":
					path = []position{from, from}
				case "error":
					return nil, errors.New("/client/navigation/private map failure")
				case "cancel":
					cancel()
				}
				return path, nil
			}
			_, err := s.PlanRouteLeg(ctx, req)
			if err == nil || len(f.packets) != 0 {
				t.Fatal(mode, err, f.packets)
			}
			if mode == "cancel" && status.Code(err) != codes.Canceled {
				t.Fatal(err)
			}
		})
	}
}
