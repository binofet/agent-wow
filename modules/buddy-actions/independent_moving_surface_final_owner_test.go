package buddyactions

import (
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestIndependentDeckContinuationUsesFinalReceivedOwnerPoint(t *testing.T) {
	for _, change := range []string{"same_point", "owner_walked_back", "different_owner", "new_host"} {
		t.Run(change, func(t *testing.T) {
			r := newSurfaceTestRig(t, 10000, surfaceTestDeck, surfaceTestBow, true, true)
			r.set("ride")
			r.tick(0)
			if len(r.packets) != 1 || r.packets[0].Opcode != opcode.MSGMoveStartForward {
				t.Fatal("missing own deck START")
			}
			r.onClock = func() {
				r.onClock = nil
				if change == "new_host" {
					r.s.hostEncounterEpoch++
					return
				}
				if change == "different_owner" {
					r.world.Owner.Guid++
					return
				}
				r.progress += 100
				sample, err := r.s.surfaceFollow.paths[surfaceTestShip].Sample(uint64(r.progress))
				if err != nil {
					t.Fatal(err)
				}
				r.tr = buddynav.SurfaceTransform{Origin: sample.Position, Yaw: sample.Yaw, Scale: 1}
				local := surfaceTestBow
				if change == "owner_walked_back" {
					local = surfaceTestDeck
				}
				r.player(r.world.Owner, local, true)
				r.world.ObservedPackets++
			}
			r.tick(100 * time.Millisecond)
			var forward int
			for n := 1; n < len(r.packets); n++ {
				if r.frame(n).Flags&1 != 0 {
					forward++
				}
			}
			if change == "same_point" {
				if forward != 1 || r.packets[1].Opcode != opcode.MSGMoveHeartbeat {
					t.Fatalf("same-point final World read lost its progress, forward=%d", forward)
				}
			} else if forward != 0 {
				t.Fatalf("native sent a stale forward heartbeat after final World already supplied %s: %d packets, phase %s, nav %s", change, len(r.packets), r.s.state.Reason, r.s.state.NavReason)
			}
		})
	}
}
