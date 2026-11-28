package world

import (
	"context"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	sctx "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestIndependentPhysicalPassengerStop(t *testing.T) {
	for _, key := range []string{"control", "same-clock-world-displacement", "airborne-with-no-landing", "superseded-scope", "different-visit"} {
		t.Run(key, func(t *testing.T) {
			c, conn := newBodyContinuitySession(t)
			c.observe(opcode.SMSGLoginVerifyWorld, positionBody(0, 7, 8, 9, 0))
			c.observe(opcode.SMSGUpdateObject, bodyPlayerCreate(99, 7, 8, 9, 0))
			f := movementinfo.Info{Flags: 0x201, Time: 900, X: 7, Y: 8, Z: 9, TransportPresent: true, Transport: movementinfo.Transport{GUID: 0x1fc0000000000123, X: 1, Y: 2, Z: 1, Time: 500, Seat: -1}}
			c.observe(opcode.SMSGUpdateObject, routeHullCreate(f.Transport.GUID))
			ctx := sctx.WithRouteBodyInput(context.Background(), false)
			op := uint32(opcode.MSGMoveStartForward)
			if key == "airborne-with-no-landing" {
				op = opcode.MSGMoveJump
				f.Flags = 0x1201
				f.JumpPresent = true
				f.Jump = movementinfo.Jump{Velocity: float32(buddynav.BodyJumpVelocity), CosAngle: 1, XYSpeed: 7}
			}
			wire, err := movementinfo.MoverFrame(99, f)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = c.e.writeContext(ctx, op, wire, c.e.transport.snapshot()); err != nil {
				t.Fatalf("first frame: %v", err)
			}
			if conn.writes != 1 || c.e.ownMovementReport == nil {
				t.Fatalf("actual first socket frame not retained %d", conn.writes)
			}
			f.Flags = 0x200
			f.JumpPresent = false
			f.Jump = movementinfo.Jump{}
			f.Time++
			if key == "same-clock-world-displacement" {
				f.X += 300
			}
			stop, err := movementinfo.MoverFrame(99, f)
			if err != nil {
				t.Fatal(err)
			}
			if key == "superseded-scope" || key == "different-visit" {
				before := proto.Clone(c.e.ownMovementReport).(*modv1.WorldPacket)
				if key == "superseded-scope" {
					before.TransportScope.MotionRevision++
				} else {
					before.TransportScope.VisitRevision++
				}
				if err := c.e.transport.reserveMovementStop(stop, c.e.transport.snapshot(), before, false, true); err == nil {
					t.Error("passenger STOP accepted prior without valid current physical scope")
				}
				return
			}
			_, _, err = c.e.writeContext(ctx, opcode.MSGMoveStop, stop, c.e.transport.snapshot())
			if key == "control" {
				if err != nil || conn.writes != 2 {
					t.Fatal("proper passenger STOP failed", err, conn.writes)
				}
				return
			}
			if err == nil || conn.writes != 1 {
				t.Errorf("invalid %s physically wrote STOP: %v, socket writes %d", key, err, conn.writes)
			}
		})
	}
}
