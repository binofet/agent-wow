package world

import (
	"context"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/buddynav"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestPassengerStopRetainsExactLocalPositionAsShipMoves(t *testing.T) {
	scope := &modv1.TransportScope{ConnectionId: "surface-test", SelfGuid: 11, VisitBound: true, VisitRevision: 3, MotionRevision: 9}
	state := transportScopeState{scope: scope}
	info := movementinfo.Info{Flags: 0x201, X: -3900, Y: -600, Z: 6, TransportPresent: true,
		Transport: movementinfo.Transport{GUID: 0x1fc0000000000001, X: 4, Y: -2, Z: 6, Time: 41230, Seat: -1}}
	wire, _ := movementinfo.MoverFrame(11, info)
	prior := &modv1.WorldPacket{Opcode: opcode.MSGMoveHeartbeat, Payload: wire, TransportScope: scope}
	stop := info
	stop.Flags = 0x200
	stop.X += 4
	stop.Transport.Time += 200
	encoded, _ := movementinfo.MoverFrame(11, stop)
	if err := state.reserveMovementStop(encoded, scope, prior, false, true); err != nil {
		t.Fatal("a ship's world motion blocked an actual local STOP", err)
	}
	for _, changed := range []string{"local position", "other ship", "missing prior", "ordinary ground writer", "superseded source",
		"same clock different world position", "same clock different world heading", "ship clock went backwards",
		"prior motion superseded", "future prior", "other prior visit", "other prior mover"} {
		t.Run(changed, func(t *testing.T) {
			q, previous, requested, route := stop, proto.Clone(prior).(*modv1.WorldPacket), scope, true
			switch changed {
			case "local position":
				q.Transport.X++
			case "other ship":
				q.Transport.GUID++
			case "missing prior":
				previous = nil
			case "ordinary ground writer":
				route = false
			case "superseded source":
				requested = &modv1.TransportScope{ConnectionId: scope.ConnectionId, SelfGuid: 11, VisitBound: true, VisitRevision: 3, MotionRevision: 8}
			case "same clock different world position":
				q.Transport.Time = info.Transport.Time
			case "same clock different world heading":
				q.X, q.Transport.Time, q.Orientation = info.X, info.Transport.Time, 1
			case "ship clock went backwards":
				q.Transport.Time = info.Transport.Time - 1
			case "prior motion superseded":
				previous.TransportScope.MotionRevision--
			case "future prior":
				previous.TransportScope.MotionRevision++
			case "other prior visit":
				previous.TransportScope.VisitRevision++
			case "other prior mover":
				previous.TransportScope.SelfGuid++
			}
			payload, _ := movementinfo.MoverFrame(11, q)
			if err := state.reserveMovementStop(payload, requested, previous, false, route); err == nil {
				t.Fatal("passenger STOP accepted changed physical custody")
			}
		})
	}
}

func TestPassengerSocketStopRequiresGroundContact(t *testing.T) {
	for _, tc := range []struct {
		name             string
		landed, attached bool
	}{
		{"airborne passenger", false, true},
		{"airborne passenger claiming dry ground", false, false},
		{"landed on deck", true, true},
		{"landed on shore", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, conn := newBodyContinuitySession(t)
			c.observe(opcode.SMSGLoginVerifyWorld, positionBody(0, 7, 8, 9, 0))
			c.observe(opcode.SMSGUpdateObject, bodyPlayerCreate(99, 7, 8, 9, 0))
			ctx := modsession.WithRouteBodyInput(context.Background(), false)
			write := func(op uint32, i movementinfo.Info) error {
				t.Helper()
				wire, err := movementinfo.MoverFrame(99, i)
				if err != nil {
					t.Fatal(err)
				}
				_, _, err = c.e.writeContext(ctx, op, wire, c.e.transport.snapshot())
				return err
			}
			jump := movementinfo.Info{Flags: 0x1201, X: 7, Y: 8, Z: 9, Time: 1000, TransportPresent: true,
				Transport:   movementinfo.Transport{GUID: 0x1fc00000f0000001, X: 1, Y: 2, Z: 3, Time: 4000, Seat: -1},
				JumpPresent: true, Jump: movementinfo.Jump{Velocity: buddynav.BodyJumpVelocity, CosAngle: 1, XYSpeed: 7}}
			if err := write(opcode.MSGMoveJump, jump); err != nil {
				t.Fatal(err)
			}
			stop := jump
			stop.Flags, stop.JumpPresent, stop.Jump = 0x200, false, movementinfo.Jump{}
			if !tc.attached {
				stop.Flags, stop.TransportPresent, stop.Transport = 0, false, movementinfo.Transport{}
			}
			if tc.landed {
				stop.Time += 800
				stop.X += 5.6
				if tc.attached {
					stop.Transport.Time += 800
					stop.Transport.X += 5.6
				}
				land := stop
				land.Flags |= 1
				land.FallTime = 800
				if err := write(opcode.MSGMoveFallLand, land); err != nil {
					t.Fatal(err)
				}
			}
			// The previous socket write may still prove the body through a
			// subsequent fully classified, irrelevant World update.
			c.observe(opcode.SMSGCompressedUpdateObject, compressedBodyUpdates(t, bodyValuesBlock(23, 0x19, 17)))
			before := conn.writes
			err := write(opcode.MSGMoveStop, stop)
			if tc.landed {
				if err != nil || conn.writes != before+1 {
					t.Fatalf("grounded passenger STOP: %v, socket writes %d to %d", err, before, conn.writes)
				}
			} else if !modcb.PacketWasNotDispatched(err) || conn.writes != before {
				t.Fatalf("airborne passenger STOP: %v, socket writes %d to %d", err, before, conn.writes)
			}
		})
	}
}
