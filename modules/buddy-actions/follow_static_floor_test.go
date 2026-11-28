package buddyactions

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestFollowMeasuredStaticDock(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_SURFACE_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set BUDDY_NAV_SURFACE_TEST_DATA_DIR for copied client map 0 collision and navigation")
	}
	goal := position{x: -3897.7754, y: -606.0532, z: 5.288626}
	clearDock := position{x: -3921.0068, y: -617.12384, z: 5.1705594}
	for _, tc := range []struct {
		name string
		from position
	}{
		{"clear floor", clearDock},
		{"cargo obstructs the direct line", position{x: -3925.4214, y: -621.6484, z: 5.1416316}},
		{"owner lost before first write", clearDock},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, req, host, advance := followGoalFixture(t)
			s.SetNavigationDataDir(dir)
			var meshErr error
			s.planMapPath = func(ctx context.Context, dir string, mapID uint32, from, to position) ([]position, error) {
				points, err := planMapNavigation(ctx, dir, mapID, from, to)
				meshErr = err
				return points, err
			}
			self, owner := f.world.Self, f.world.Owner
			self.X, self.Y, self.Z = tc.from.x, tc.from.y, tc.from.z
			self.PositionRevision++
			owner.X, owner.Y, owner.Z = goal.x, goal.y, goal.z
			owner.PositionRevision++
			req.X, req.Y, req.Z, req.OwnerPositionRevision = goal.x, goal.y, goal.z, owner.PositionRevision
			ctx := context.Background()
			tick := func() {
				t.Helper()
				if _, err := s.RenewPolicyEncounterLease(host, wrapperspb.UInt64(1)); err != nil {
					t.Fatal(err)
				}
				if _, err := s.SetFollowFormation(host, req); err != nil {
					t.Fatal(err)
				}
				s.Tick(ctx)
			}
			withdrewOwner := false
			if tc.name == "owner lost before first write" {
				f.onClock = func() { withdrewOwner, owner.Present = true, false }
			}
			tick()
			// A cold copied tile can spend the first 250ms planning window on
			// geometry. Nothing may move before the next normal scoped replan.
			if len(f.packets) == 0 && strings.Contains(s.state.NavReason, context.DeadlineExceeded.Error()) {
				for range 4 {
					advance(200 * time.Millisecond)
					tick()
				}
			}
			if !missingFormationEndpoint(meshErr) {
				t.Fatalf("expected an uncovered mesh endpoint: %v", meshErr)
			}
			if tc.name != "clear floor" {
				if len(f.packets) != 0 || s.state.Moving ||
					tc.name == "owner lost before first write" && !withdrewOwner ||
					tc.from != clearDock && !strings.Contains(s.state.NavReason, "body probe corridor intersects static collision") {
					t.Fatal(s.state.Reason, s.state.NavReason, f.packets)
				}
				return
			}
			if countPackets(f, opcode.MSGMoveStartForward) != 1 || s.state.NavReason != "client_measured_static_floor" {
				t.Fatal(s.state.Reason, s.state.NavReason)
			}
			first := s.pos
			for n := 0; s.state.Moving && n < 40; n++ {
				advance(200 * time.Millisecond)
				before := s.pos
				tick()
				if delta := distance(before, s.pos); delta > float64(self.RunSpeed)*.2+.005 ||
					n == 0 && math.Abs(delta-float64(self.RunSpeed)*.2) > .02 {
					t.Fatalf("physical stride %.4f at observed speed %v", delta, self.RunSpeed)
				}
			}
			if s.state.Moving || !followAtTarget(s.pos, goal, req.StopDistance) || countPackets(f, opcode.MSGMoveStop) != 1 ||
				countPackets(f, opcode.MSGMoveHeartbeat) == 0 || countPackets(f, opcode.MSGMoveStartForward) != 1 {
				t.Fatal(first, s.pos, s.state.Reason, s.state.NavReason, len(f.packets))
			}
			t.Logf("normal Follow travelled %.2f yards to %+v using %d ground frames", distance(first, s.pos), s.pos, len(f.packets))
		})
	}
}
