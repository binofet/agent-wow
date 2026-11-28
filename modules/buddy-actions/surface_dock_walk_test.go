package buddyactions

import (
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestMenethilWalksBetweenDockAndDeck(t *testing.T) {
	for _, mode := range []string{"leave", "board"} {
		t.Run(mode, func(t *testing.T) {
			const dwell = 40000 // stable map-0 dwell on path 292
			r := newSurfaceTestRig(t, dwell, surfaceTestDeck, surfaceTestDeck, true, true)
			path, err := buddynav.LoadTransportPath(r.ctx, r.s.navDataDir, 292, 30, 1)
			if err != nil {
				t.Fatal(err)
			}
			h := r.world.MovingTransports[0]
			h.Entry, h.MovingTransport.Template.Entry = 176231, 176231
			h.MovingTransport.Template.AccelerationRate = 1
			h.MovingTransport.PeriodMs = proto.Uint32(path.Period())
			// Use the accepted own write's world position at its write time,
			// 281 ms before the observed hull age of 93214 ms. The retained
			// older received passenger offsets were not the current write.
			written, err := path.Sample(431711304 + 93214 - 281)
			if err != nil {
				t.Fatal(err)
			}
			if written.Stopped || dockInput(path, 431711304+93214, time.Millisecond) || !dockInput(path, dwell, time.Second) {
				t.Fatal("field sample must be departing and the walking replay must be docked")
			}
			deck := (buddynav.SurfaceTransform{Origin: written.Position, Yaw: written.Yaw, Scale: 1}).Local(buddynav.Point{X: -3897.0408, Y: -595.9428, Z: 6.072269})
			dock := r.tr.Local(buddynav.Point{X: -3889.1987, Y: -609.7545, Z: 5.239325})
			boarding := mode == "board"
			if boarding {
				r.player(r.world.Self, dock, false)
				r.player(r.world.Owner, deck, true)
			} else {
				r.player(r.world.Self, deck, true)
				r.player(r.world.Owner, dock, false)
			}
			r.world.Self.RunSpeed, r.world.Self.WalkSpeed = 7.5600004, 2.5
			r.ownerGoal()
			mesh, err := r.s.surfaceMeshLocked(r.ctx, h)
			if err != nil {
				t.Fatal(err)
			}
			if support, err := hullFoot(r.ctx, mesh.Collision(), deck); err != nil || !support {
				t.Fatalf("field write lost its measured hull support: %v %v", deck, err)
			}
			r.set(mode)
			for range 260 {
				r.tick(50 * time.Millisecond)
				if countPackets(r.fakeSession, opcode.MSGMoveJump) > 0 || countPackets(r.fakeSession, opcode.MSGMoveStartSwim) > 0 {
					t.Fatal("requested dry walking", r.s.surfaceFollow.phase)
				}
				body := r.s.surfaceFollow.body
				if body != nil && body.info.TransportPresent == boarding && body.packet.Opcode == opcode.MSGMoveStop && !r.s.state.MotionUnconfirmed {
					if len(r.packets) < 3 || countPackets(r.fakeSession, opcode.MSGMoveStartForward) == 0 {
						t.Fatalf("no ordinary walking frames: %v", r.packets)
					}
					if body.info.TransportPresent && body.info.Transport.GUID != r.ship {
						t.Fatal("wrong ship", body.info)
					}
					t.Logf("%s in %d written movement frames, terminal XYZ=(%.3f,%.3f,%.3f)", mode, len(r.packets), body.info.X, body.info.Y, body.info.Z)
					return
				}
				if r.s.surfaceObservationLocked(r.ctx, r.world).GetJumpRequired() {
					t.Fatalf("stationary dry dock requested a jump: %s %s at %+v", r.s.surfaceFollow.phase, r.s.state.NavReason, r.s.pos)
				}
				if body != nil && body.info.TransportPresent == boarding {
					if boarding {
						r.set("ride")
					} else {
						r.set("")
					}
				} else {
					r.set(mode)
				}
			}
			t.Fatalf("no dry grounded arrival: phase=%s reason=%s writes=%d pos=%+v", r.s.surfaceFollow.phase, r.s.state.NavReason, len(r.packets), r.s.pos)
		})
	}
}
