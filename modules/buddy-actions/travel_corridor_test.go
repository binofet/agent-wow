package buddyactions

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// Adapt the existing per-test synthetic geometry to an already plotted strip.
// Production always constructs the normal retained mesh.
type fixtureTravelSurface struct{ path []buddynav.Point }

func (f *fixtureTravelSurface) Destination() buddynav.Point { return f.path[len(f.path)-1] }
func (f *fixtureTravelSurface) PathAhead(ctx context.Context, _, _ buddynav.Point) ([]buddynav.Point, error) {
	return f.path, ctx.Err()
}
func fixtureTravelPath(plan func(context.Context, string, uint32, position, position) ([]position, error), densify bool) openTravelSurface {
	return func(ctx context.Context, dir string, id uint32, a, b buddynav.Point) (travelSurface, error) {
		path, err := plan(ctx, dir, id, navPosition(a), navPosition(b))
		if err != nil {
			return nil, err
		}
		if len(path) == 0 {
			return nil, errors.New("fixture did not supply a route")
		}
		if densify {
			samples := []position{path[0]}
			for i := 1; i < len(path); i++ {
				samples = append(samples, denseRouteLeg(path[i-1], path[i])[1:]...)
			}
			path = samples
		}
		points := make([]buddynav.Point, len(path))
		for i, p := range path {
			points[i] = swimPoint(p)
		}
		return &fixtureTravelSurface{path: points}, nil
	}
}

type straightTripSurface struct {
	goal     buddynav.Point
	prefixes int
}

func (x *straightTripSurface) Destination() buddynav.Point { return x.goal }
func (x *straightTripSurface) PathAhead(ctx context.Context, from, goal buddynav.Point) ([]buddynav.Point, error) {
	x.prefixes++
	if goal != x.goal {
		return nil, errors.New("goal changed")
	}
	end := goal
	if end.X-from.X > 80 {
		end.X = from.X + 80
	}
	p := denseRouteLeg(navPosition(from), navPosition(end))
	out := make([]buddynav.Point, len(p))
	for i, v := range p {
		out[i] = swimPoint(v)
	}
	return out, ctx.Err()
}

func TestDirectLongTravelUsesOneStripAcrossPrefixesAndRenewedCustody(t *testing.T) {
	s, f, _, r := explorationRig(t)
	s.sessionID = f.party.SessionId
	f.world.VisitRevision = 7
	mountForTravel(t, s, f.world.Self, 23238)
	r.X += 600
	goal := buddynav.Point{X: r.X, Y: r.Y, Z: r.Z}
	baked := &straightTripSurface{goal: goal}
	opens := 0
	s.openTravel = func(_ context.Context, _ string, _ uint32, _, target buddynav.Point) (travelSurface, error) {
		opens++
		if target != goal {
			t.Fatal("searched a local prefix instead of the requested destination", target)
		}
		return baked, nil
	}
	s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
		t.Fatal("retained ground trip called the legacy physical planner")
		return nil, nil
	}
	for operation := 0; operation < 4; operation++ {
		start := s.pos
		out, err := s.TravelTo(t.Context(), r)
		if err != nil || out.Moving || out.MotionUnconfirmed || out.Traveling {
			t.Fatal(operation, out, err)
		}
		if operation == 0 && (out.Reason != "exploration_ground_progress_checkpoint" || distance(start, s.pos) <= 200) {
			t.Fatal("premature fixed-distance/first-prefix stop", out)
		}
		if out.Reason == "exploration_within_distance" {
			if distance(s.pos, navPosition(goal)) > 3 || opens != 1 || baked.prefixes < 5 || countPackets(f, opcode.MSGMoveStop) != operation+1 {
				t.Fatal("false or redundant arrival", s.pos, opens, baked.prefixes, out)
			}
			return
		}
		if out.Reason != "exploration_ground_progress_checkpoint" {
			t.Fatal(out)
		}
		if _, err = s.ReleaseInteraction(t.Context(), &actionsv1.InteractionRelease{Owner: "exploration", Token: r.Token}); err != nil {
			t.Fatal(err)
		}
		held, err := s.AcquireInteraction(t.Context(), &actionsv1.InteractionRequest{Owner: "exploration", LeaseMs: 30000})
		if err != nil {
			t.Fatal(err)
		}
		r.Token = held.Token
	}
	t.Fatal("long trip did not reach its destination")
}

func TestCopiedTravelReadOfferAndActualMovementStayOnSameStrip(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_POOL_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set BUDDY_NAV_POOL_TEST_DATA_DIR to copied assets")
	}
	s, f, advance := rig(t)
	s.sessionID = f.party.SessionId
	f.world.VisitRevision = 3
	f.world.Owner = nil
	me := f.world.Self
	me.MapId = 0
	me.X, me.Y, me.Z = -5248.4316, -583.0111, 409.1023
	mountForTravel(t, s, me, 23238)
	s.SetNavigationDataDir(dir)
	s.approachDelay = func(c context.Context, d time.Duration) error { advance(d); return c.Err() }
	var opens int
	s.openTravel = func(c context.Context, dir string, id uint32, a, b buddynav.Point) (travelSurface, error) {
		opens++
		return openGroundTravel(c, dir, id, a, b)
	}
	s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
		t.Fatal("read-issued dry endpoint replanned on a second surface")
		return nil, nil
	}
	for leg := 0; leg < 2; leg++ {
		if _, err := s.snapshot(t.Context()); err != nil {
			t.Fatal(err)
		}
		start := s.clientPositionLocked()
		route, err := s.PlanRouteLeg(t.Context(), &actionsv1.PlanRouteLegRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, ExpectedPositionRevision: start.Revision, MapId: 0, X: -5421.9102, Y: -2930.01, Z: 347.25})
		if err != nil || route.GoalReached || route.GetEndpoint() == nil || route.PathLength < 140 || opens != 1 {
			t.Fatal(route, err, opens)
		}
		held, err := s.AcquireInteraction(t.Context(), &actionsv1.InteractionRequest{Owner: "exploration", LeaseMs: 30000})
		if err != nil {
			t.Fatal(err)
		}
		ep := route.Endpoint
		out, err := s.TravelTo(t.Context(), &actionsv1.TravelToRequest{Token: held.Token, MapId: 0, X: ep.X, Y: ep.Y, Z: ep.Z, ArrivalDistance: .75})
		if err != nil || out.Reason != "exploration_within_distance" || out.MotionUnconfirmed || out.Moving || distance(s.pos, position{x: ep.X, y: ep.Y, z: ep.Z}) > .75 || opens != 1 {
			t.Fatal(out, err, opens)
		}
		if _, err = s.ReleaseInteraction(t.Context(), &actionsv1.InteractionRelease{Owner: "exploration", Token: held.Token}); err != nil {
			t.Fatal(err)
		}
	}
}
