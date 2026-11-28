package buddyactions

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"math"
	"os"
	"testing"
	"time"
)

func TestIndependentNativeOwedDeckStopCannotAdoptAReplacementShip(t *testing.T) {
	const oldProgress = uint32(30000)
	for _, change := range []string{"same_hull", "same_guid_new_lifetime", "replacement_during_clock"} {
		t.Run(change, func(t *testing.T) {
			// The reverse of the retained 35-yard open-deck trip.
			r := newSurfaceTestRig(t, oldProgress, surfaceTestBow, surfaceTestDeck, true, true)
			seedSyntheticPortalWorldScope(r.fakeSession, 1)
			r.player(r.world.Self, surfaceTestBow, true)
			r.set("ride")
			r.tick(0)
			if len(r.packets) != 1 || r.packets[0].Opcode != opcode.MSGMoveStartForward || r.s.state.MotionUnconfirmed {
				t.Fatalf("first actual deck START failed packets=%d reason=%s detail=%s", len(r.packets), r.s.state.Reason, r.s.state.NavReason)
			}
			previous := r.frame(0)
			replace := func() {
				hull := r.world.MovingTransports[0]
				facts := hull.MovingTransport
				clock := uint32(10000)
				p, err := buddynav.LoadTransportPath(context.Background(), os.Getenv("BUDDY_NAV_SURFACE_TEST_DATA_DIR"), 292, 30, 3)
				if err != nil {
					t.Fatal(err)
				}
				src, err := p.Sample(uint64(clock))
				if err != nil {
					t.Fatal(err)
				}
				hull.X, hull.Y, hull.Z, hull.Orientation = src.Position.X, src.Position.Y, src.Position.Z, src.Yaw
				hull.PositionRevision++
				facts.ObjectRevision++
				facts.PositionRevision++
				facts.MovementRevision++
				facts.PathProgressMs = &clock
				facts.MovementAgeMs = 0
				r.world.Self.PassengerMovement = nil
				r.world.Owner.PassengerMovement = nil
			}
			if change == "same_guid_new_lifetime" {
				replace()
			}
			if change == "replacement_during_clock" {
				r.onClock = func() { r.onClock = nil; replace() }
			}
			state, err := r.s.Follow(context.Background(), &actionsv1.ToggleRequest{Enabled: false})
			if err != nil {
				t.Fatal(err)
			}
			n := countPackets(r.fakeSession, opcode.MSGMoveStop)
			if change == "same_hull" {
				if n != 1 || state.Moving {
					t.Fatalf("genuine owed stop refused; n=%d moving=%v", n, state.Moving)
				}
				return
			}
			if n != 0 {
				end := r.frame(len(r.packets) - 1)
				t.Fatalf("unwitnessed replacement transported prior attached body %.2f yd by STOP (old clock %d, new clock %d, same GUID %x)", math.Hypot(float64(end.X-previous.X), float64(end.Y-previous.Y)), previous.Transport.Time, end.Transport.Time, end.Transport.GUID)
			}
		})
	}
}
func TestIndependentNativeUncertainDeckNeedsOrdinaryPositiveRecovery(t *testing.T) {
	r := newSurfaceTestRig(t, 30000, surfaceTestBow, surfaceTestBow, false, true)
	seedSyntheticPortalWorldScope(r.fakeSession, 1)
	r.player(r.world.Self, surfaceTestBow, false)
	r.set("board")
	r.failSend = true
	r.tick(0)
	if len(r.packets) != 1 || !r.s.state.MotionUnconfirmed {
		t.Fatalf("wrong initial UNKNOWN %d %s", len(r.packets), r.s.state.Reason)
	}
	attempt := r.s.surfaceFollow.lastAttempt
	r.tick(200 * time.Millisecond)
	if len(r.packets) != 1 || !r.s.state.MotionUnconfirmed {
		t.Fatal("unresolved input repeated or became confirmed")
	}
	r.failSend = false
	r.portalCoreScope.MotionRevision++
	r.world.SelfMotionAuthority.TransportScope = clonePortalScope(r.portalCoreScope)
	r.player(r.world.Self, surfaceTestBow, true)
	r.tick(100 * time.Millisecond)
	if len(r.packets) != 1 || r.s.state.MotionUnconfirmed || r.s.surfaceFollow.lastAttempt != attempt {
		t.Fatalf("ordinary Tick after genuine later own stationary passenger: stillUnknown=%v writes=%d reason=%s", r.s.state.MotionUnconfirmed, len(r.packets), r.s.state.Reason)
	}
}
