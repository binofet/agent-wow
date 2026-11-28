package buddyactions

import (
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestIndependentRideAfterNewOwnerHeartbeat(t *testing.T) {
	for _, tc := range []struct {
		name     string
		aboard   bool
		expected uint32
	}{
		{"same_deck_point", true, opcode.MSGMoveHeartbeat},
		{"owner_disembarked", false, opcode.MSGMoveStop},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSurfaceTestRig(t, 10000, surfaceTestDeck, surfaceTestBow, true, true)
			r.set("ride")
			r.tick(0)
			start := r.frame(0)
			if len(r.packets) != 1 || r.packets[0].Opcode != opcode.MSGMoveStartForward || !start.TransportPresent || start.Transport.GUID != surfaceTestShip {
				t.Fatalf("expected one genuine deck START, frame=%v", start)
			}
			owner := r.world.Owner
			oldLocal := surfaceOwnerPoint(r.world)
			oldPassenger, oldPose := owner.PassengerMovement.Revision, owner.PositionRevision
			r.advance(100 * time.Millisecond)
			r.world.Self.PassengerMovement.AgeMs += 100
			r.world.MovingTransports[0].MovingTransport.MovementAgeMs += 100
			r.world.ObservedPackets++
			r.progress += 100
			pose, err := r.s.surfaceFollow.paths[surfaceTestShip].Sample(uint64(r.progress))
			if err != nil {
				t.Fatal(err)
			}
			r.tr = buddynav.SurfaceTransform{Origin: pose.Position, Yaw: pose.Yaw, Scale: 1}
			r.player(owner, surfaceTestBow, tc.aboard)
			if owner.PassengerMovement.Revision <= oldPassenger || owner.PositionRevision <= oldPose ||
				tc.aboard && surfaceDistance(oldLocal, surfaceOwnerPoint(r.world)) > .001 {
				t.Fatalf("bad fresh passenger fixture: owner=%v", owner)
			}
			// The Lua formation setter has not run again. Its admitted goal and lease are still current.
			r.tick(0)
			end := r.frame(1)
			op := r.packets[1].Opcode
			travelled := surfaceDistance(surfaceLocalPoint(start), surfaceLocalPoint(end))
			if len(r.packets) != 2 || op != tc.expected || !end.TransportPresent || end.Transport.GUID != surfaceTestShip ||
				tc.aboard && (end.Flags&1 == 0 || travelled < .1) || !tc.aboard && (end.Flags&1 != 0 || travelled > .002) {
				t.Fatalf("after owner change %d→%d: writes=%d secondOpcode=%d want=%d localDelta=%.5fyd moving=%v phase=%s nav=%s",
					oldPassenger, owner.PassengerMovement.Revision, len(r.packets), op, tc.expected, travelled, r.s.state.Moving, r.s.state.Reason, r.s.state.NavReason)
			}
		})
	}
}
