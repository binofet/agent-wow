package modrt

import (
	"context"
	"errors"
	"fmt"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestSelectedPassengerPolicyCallback(t *testing.T) {
	const ship uint64 = 0x1fc0000000000021
	for _, which := range []string{"forward", "jump to water", "uncertain heartbeat", "unwritten stop", "native refusal", "other capability"} {
		t.Run(which, func(t *testing.T) {
			x := newInstanceEntryFixture(t)
			scope := instanceEntryScope() // Actual Core scope 9. The exact Lua/World visit below is 1.
			if err := x.m.PublishObserved(opcode.SMSGUpdateObject, nil, scope); err != nil {
				t.Fatal(err)
			}
			count := x.m.worldPackets
			writes := 0
			var priorAction string
			x.m.session.SendScopedPacket = func(ctx context.Context, code uint32, body []byte, physical *modv1.TransportScope) error {
				origin := modsession.WriteOriginFromContext(ctx)
				h, ok := modsession.RouteHull(ctx)
				if !ok || h.GUID != ship || h.Birth != 4 || h.Packets != count || !modsession.IsSelfBodyInput(ctx) || !modsession.IsRouteBodyInput(ctx) ||
					!proto.Equal(scope, physical) || origin.Module != actionsModule || origin.SessionID != friendlyEncounterSession || origin.Writer != "lua" || origin.Generation == 0 ||
					origin.ActionID == "" || priorAction != "" && origin.ActionID != priorAction || !modcb.ValidMovementAttempt(origin.MovementAttempt) {
					t.Fatal("physical write lost its exact original Lua operation", h, physical, origin)
				}
				priorAction = origin.ActionID
				writes++
				if which == "unwritten stop" && code == opcode.MSGMoveStop {
					return modcb.PacketNotDispatchedError("definite physical refusal")
				}
				if which == "uncertain heartbeat" && code == opcode.MSGMoveHeartbeat {
					return errors.New("socket reply lost")
				}
				return nil
			}
			action := policyBodyIdleAction()
			if which == "jump to water" {
				action["jump"] = true
				action["landing"] = "ground_or_water"
			}
			if which == "other capability" {
				action = instanceEntryAction("travel_to")
			}
			x.handle = func(ctx context.Context, method string, req, result proto.Message) error {
				if which == "native refusal" {
					proto.Merge(result, &actionsv1.State{BodyMovement: &actionsv1.BodyMovementState{VisitId: 1, SequenceId: 17, Phase: "body_scope_not_dispatched"}})
					return nil
				}
				writer := x.m.moduleScopedPacketWriter(friendlyEncounterSession, actionsModule)
				passenger := movementinfo.Info{Flags: 0x201, Time: 100, TransportPresent: true, Transport: movementinfo.Transport{GUID: ship, Seat: -1, Time: 30000}}
				mark := func(v, sequence, birth uint64, id int) context.Context {
					selected := modcb.WithSurfaceBodySelection(ctx, v, sequence)
					return instanceEntryIncoming(modcb.WithMovementAttempt(modcb.WithRouteBodyInput(modcb.WithRouteHull(selected, ship, birth, count), false), fmt.Sprintf("%064x", id)))
				}
				frame := func(self uint64, info movementinfo.Info) []byte {
					b, e := movementinfo.MoverFrame(self, info)
					if e != nil {
						t.Fatal(e)
					}
					return b
				}
				first := uint32(opcode.MSGMoveStartForward)
				if which == "jump to water" {
					first = opcode.MSGMoveJump
					passenger.Flags |= 0x1000
					passenger.JumpPresent = true
					passenger.Jump = movementinfo.Jump{Velocity: 7.955547, CosAngle: 1, XYSpeed: 7}
				}
				out := frame(22, passenger)
				if which == "other capability" {
					if e := writer(mark(1, 17, 4, 1), first, out, scope); !modcb.PacketWasNotDispatched(e) || writes != 0 {
						t.Fatal("different capability authorized passenger", e)
					}
					return nil
				}
				deny := func(c context.Context, code uint32, p []byte, sc *modv1.TransportScope) {
					before := writes
					if e := writer(c, code, p, sc); !modcb.PacketWasNotDispatched(e) || writes != before {
						t.Fatalf("invalid physical input: op=%d, err=%v, writes=%d", code, e, writes)
					}
				}
				for _, c := range []context.Context{instanceEntryIncoming(ctx), mark(9, 17, 4, 1), mark(1, 18, 4, 1), mark(1, 17, 0, 1)} {
					deny(c, first, out, scope)
				}
				deny(mark(1, 17, 4, 1), first, frame(23, passenger), scope)
				if e := x.m.modulePacketWriter(friendlyEncounterSession, actionsModule)(mark(1, 17, 4, 1), first, out); !modcb.PacketWasNotDispatched(e) {
					t.Fatal("unscoped callback", e)
				}
				if e := x.m.moduleScopedPacketWriter(friendlyEncounterSession, "buddy-chat")(mark(1, 17, 4, 1), first, out, scope); !modcb.PacketWasNotDispatched(e) {
					t.Fatal("wrong caller", e)
				}
				x.m.mu.Lock()
				x.m.preparing = true
				x.m.mu.Unlock()
				deny(mark(1, 17, 4, 7), first, out, scope)
				x.m.mu.Lock()
				x.m.preparing = false
				x.m.mu.Unlock()
				if e := writer(mark(1, 17, 4, 1), first, out, scope); e != nil {
					t.Fatal("authorized initial selected frame", e)
				}
				deny(mark(1, 17, 4, 2), first, out, scope)
				passenger.Time += 50
				passenger.Transport.Time += 50
				if passenger.JumpPresent {
					passenger.FallTime = 50
				}
				beat := frame(22, passenger)
				deny(mark(1, 17, 5, 2), opcode.MSGMoveHeartbeat, beat, scope)
				x.m.mu.Lock()
				x.m.session.Identity.CharacterGUID = 23
				x.m.mu.Unlock()
				deny(mark(1, 17, 4, 8), opcode.MSGMoveHeartbeat, beat, scope)
				x.m.mu.Lock()
				x.m.session.Identity.CharacterGUID = 22
				x.m.mu.Unlock()
				later := proto.Clone(scope).(*modv1.TransportScope)
				later.VisitRevision++
				deny(mark(1, 17, 4, 2), opcode.MSGMoveHeartbeat, beat, later)
				if e := writer(mark(1, 17, 4, 2), opcode.MSGMoveHeartbeat, beat, scope); which == "uncertain heartbeat" {
					if e == nil || modcb.PacketWasNotDispatched(e) {
						t.Fatal("lost physical uncertainty", e)
					}
					deny(mark(1, 17, 4, 3), opcode.MSGMoveHeartbeat, beat, scope)
					proto.Merge(result, &actionsv1.State{MotionUnconfirmed: true, BodyMovement: &actionsv1.BodyMovementState{VisitId: 1, SequenceId: 17, Phase: "body_scope_unconfirmed_no_retry", TransportWritten: true}})
					return nil
				} else if e != nil {
					t.Fatal("measured second frame", e)
				}
				deny(mark(1, 17, 4, 3), opcode.MSGMoveHeartbeat, beat, scope)
				stop := passenger
				stop.Time++
				stop.Flags = 0x200
				if which == "jump to water" {
					stop = movementinfo.Info{Flags: movementinfo.SwimmingFlag, PitchPresent: true, Time: 1000}
					deny(mark(1, 17, 4, 3), opcode.MSGMoveStop, frame(22, stop), scope)
					if e := writer(mark(1, 17, 4, 3), opcode.MSGMoveStartSwim, frame(22, stop), scope); e != nil {
						t.Fatal("physical water contact", e)
					}
				} else {
					over := passenger
					over.Time = 201
					deny(mark(1, 17, 4, 3), opcode.MSGMoveHeartbeat, frame(22, over), scope)
				}
				deny(mark(1, 17, 4, 1), opcode.MSGMoveStop, frame(22, stop), scope)
				if e := writer(mark(1, 17, 4, 4), opcode.MSGMoveStop, frame(22, stop), scope); which == "unwritten stop" {
					if !modcb.PacketWasNotDispatched(e) {
						t.Fatal(e)
					}
					proto.Merge(result, &actionsv1.State{MotionUnconfirmed: true, BodyMovement: &actionsv1.BodyMovementState{VisitId: 1, SequenceId: 17, Phase: "body_scope_unconfirmed_no_retry", TransportWritten: true}})
					return nil
				} else if e != nil {
					t.Fatal("measured terminal stop", e)
				}
				deny(mark(1, 17, 4, 5), opcode.MSGMoveStop, frame(22, stop), scope)
				proto.Merge(result, &actionsv1.State{BodyMovement: &actionsv1.BodyMovementState{VisitId: 1, SequenceId: 17, Phase: "body_input_written", TransportWritten: true}})
				return nil
			}
			receipt := x.step(t, action)
			switch which {
			case "native refusal":
				if receipt.Outcome != behavior.ActionNotDispatched || writes != 0 {
					t.Fatal(receipt, writes)
				}
			case "unwritten stop":
				if receipt.Outcome != behavior.ActionSettledUnverified || receipt.Payload["reconciliationRequired"] != true || receipt.Payload["transport_written"] != true || writes != 3 {
					t.Fatal(receipt, writes)
				}
			case "other capability":
				if writes != 0 {
					t.Fatal(writes)
				}
			case "uncertain heartbeat":
				if receipt.Outcome != behavior.ActionSettledUnverified || receipt.Payload["reconciliationRequired"] != true || writes != 2 {
					t.Fatal(receipt, writes)
				}
			default:
				if receipt.Payload["effect_observed"] != false || receipt.Payload["transport_written"] != true || writes < 3 {
					t.Fatal(receipt, writes)
				}
			}
		})
	}
}
