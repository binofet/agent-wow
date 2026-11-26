package modrt

import (
	"context"
	"fmt"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestPolicyMeasuredWaterBindingAndUncertainStop(t *testing.T) {
	for _, mode := range []string{"ground_or_water", "follow_water", "unknown_stop", "first_refused", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			x := newInstanceEntryFixture(t)
			writes := 0
			x.m.session.SendScopedPacket = func(ctx context.Context, op uint32, _ []byte, scope *modv1.TransportScope) error {
				if scope == nil || !modsession.IsBodyInput(ctx) || !modcb.ValidMovementAttempt(modsession.WriteOriginFromContext(ctx).MovementAttempt) {
					t.Fatal("unscoped source")
				}

				writes++
				if mode == "unknown_stop" && op == opcode.MSGMoveStop {
					return status.Error(codes.Unknown, "uncertain wet STOP")
				}
				if mode == "first_refused" {
					return modcb.PacketNotDispatchedError("scope changed before first write")
				}
				return nil
			}
			action := policyBodyIdleAction()
			landing := mode
			if mode == "legacy" {
				landing = ""
			} else {
				if mode != "follow_water" {
					landing = "ground_or_water"
				}
				action["landing"] = landing
			}
			x.handle = func(ctx context.Context, method string, request, result proto.Message) error {
				if method != actionsv1.BuddyActions_MoveBody_FullMethodName {
					t.Fatal(method)
				}
				r := request.ProtoReflect()
				if r.Get(r.Descriptor().Fields().ByName("landing")).String() != landing {
					t.Fatal("landing lost in Lua/native dispatch", r)
				}
				w := x.m.modulePacketWriter(friendlyEncounterSession, actionsModule)
				in := instanceEntryIncoming(ctx)
				send := func(op uint32, i movementinfo.Info) error {
					b, e := movementinfo.Encode(i)
					if e != nil {
						t.Fatal(e)
					}
					wire := append([]byte{1, 22}, b...)
					if mode == "legacy" {
						return w(in, op, wire)
					}
					if e := w(in, op, wire); !modcb.PacketWasNotDispatched(e) {
						t.Fatal("measured crossing accepted unscoped frame", op, e)
					}
					scoped := x.m.moduleScopedPacketWriter(friendlyEncounterSession, actionsModule)
					if e := scoped(in, op, wire, instanceEntryScope()); !modcb.PacketWasNotDispatched(e) {
						t.Fatal("body frame without an attempt identity entered transport", op, e)
					}
					withAttempt := instanceEntryIncoming(modcb.WithMovementAttempt(ctx, fmt.Sprintf("%064x", op)))
					return scoped(withAttempt, op, wire, instanceEntryScope())
				}
				fall := movementinfo.Info{Flags: 0x1001, FallTime: 13000, JumpPresent: true, Jump: movementinfo.Jump{CosAngle: 1, XYSpeed: 7}}
				entry := movementinfo.Info{Flags: movementinfo.SwimmingFlag | 1, PitchPresent: true, X: 11, Y: 12, Z: -700}
				stop := entry
				stop.Flags = movementinfo.SwimmingFlag
				if mode == "legacy" {
					if e := send(opcode.MSGMoveHeartbeat, fall); !modcb.PacketWasNotDispatched(e) {
						t.Fatal("legacy unrequested long fall", e)
					}
					if e := send(opcode.MSGMoveStartSwim, entry); !modcb.PacketWasNotDispatched(e) {
						t.Fatal("legacy unrequested water entry", e)
					}
					return nil
				}
				if mode == "first_refused" {
					if e := send(opcode.MSGMoveSetFacing, movementinfo.Info{}); !modcb.PacketWasNotDispatched(e) {
						t.Fatal(e)
					}
					proto.Merge(result, &actionsv1.State{BodyMovement: &actionsv1.BodyMovementState{VisitId: 1, SequenceId: 17, Phase: "body_scope_not_dispatched"}})
					return nil
				}
				if e := send(opcode.MSGMoveHeartbeat, fall); e != nil {
					t.Fatal("measured long forward fall", e)
				}
				if e := send(opcode.MSGMoveStartSwim, entry); e != nil {
					t.Fatal("measured swimming transition", e)
				}
				e := send(opcode.MSGMoveStop, stop)
				if mode == "unknown_stop" {
					if e == nil {
						t.Fatal("missing simulated lost STOP")
					}
					if again := send(opcode.MSGMoveStop, stop); !modcb.PacketWasNotDispatched(again) {
						t.Fatal("retained unknown token replayed water STOP", again)
					}
				} else if e != nil {
					t.Fatal(e)
				}
				phase := "body_input_written"
				if e != nil {
					phase = "transport_unconfirmed_no_retry"
				}
				proto.Merge(result, &actionsv1.State{MotionUnconfirmed: e != nil, BodyMovement: &actionsv1.BodyMovementState{VisitId: 1, SequenceId: 17, Phase: phase, TransportWritten: true}})
				return nil
			}
			receipt := x.step(t, action)
			if mode == "legacy" {
				if writes != 0 {
					t.Fatal(writes)
				}
				return
			}
			if mode == "first_refused" {
				if receipt.Outcome != behavior.ActionNotDispatched || writes != 1 {
					t.Fatal(receipt, writes)
				}
				return
			}
			if receipt.Outcome != behavior.ActionSettledUnverified || receipt.Payload["effect_observed"] != false || writes != 3 || (receipt.Payload["reconciliationRequired"] == true) != (mode == "unknown_stop") {
				t.Fatal("incorrect water custody", receipt, writes)
			}
		})
	}
}
