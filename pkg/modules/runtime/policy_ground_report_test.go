package modrt

import (
	"context"
	"fmt"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestOrdinaryGroundBodyKeepsPerFrameAttemptAndLuaOrigin(t *testing.T) {
	x := newInstanceEntryFixture(t)
	writes := 0
	var actionID string
	x.m.session.SendPacket = func(ctx context.Context, code uint32, body []byte) error {
		writes++
		o := modsession.WriteOriginFromContext(ctx)
		if o.MovementAttempt != fmt.Sprintf("%064x", writes) || o.Module != actionsModule || o.Writer != "lua" || o.SessionID != friendlyEncounterSession || o.Generation == 0 || o.ActionID == "" || actionID != "" && actionID != o.ActionID {
			t.Fatalf("frame %d lost its physical attempt or original policy origin: %+v", code, o)
		}
		actionID = o.ActionID
		return nil
	}
	x.handle = func(ctx context.Context, method string, req, result proto.Message) error {
		if method != actionsv1.BuddyActions_MoveBody_FullMethodName {
			t.Fatal(method)
		}
		writer := x.m.modulePacketWriter(friendlyEncounterSession, actionsModule)
		for i, op := range []uint32{opcode.MSGMoveSetFacing, opcode.MSGMoveStartForward, opcode.MSGMoveHeartbeat, opcode.MSGMoveStop} {
			packetCtx := instanceEntryIncoming(modcb.WithMovementAttempt(ctx, fmt.Sprintf("%064x", i+1)))
			if err := writer(packetCtx, op, policyMovementPacket(23)); !modcb.PacketWasNotDispatched(err) {
				t.Fatal("different body", err)
			}
			if err := writer(packetCtx, op, policyMovementPacket(22)); err != nil {
				t.Fatal(err)
			}
		}
		proto.Merge(result, &actionsv1.State{SessionId: friendlyEncounterSession, SelfGuid: 22, BodyMovement: &actionsv1.BodyMovementState{SequenceId: 17, VisitId: 1, Phase: "body_input_written", TransportWritten: true}})
		return nil
	}
	if got := x.step(t, policyBodyIdleAction()); got.Outcome != behavior.ActionSettledUnverified || writes != 4 {
		t.Fatalf("receipt=%v writes=%d", got, writes)
	}
}
