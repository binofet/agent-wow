package buddyactions

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"testing"
)

type ordinaryDeadlineBeforePacket struct {
	sessionAPI
	cancel      context.CancelFunc
	clientCalls int
}

func (x *ordinaryDeadlineBeforePacket) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, o ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	out, err := x.sessionAPI.InvokeModule(ctx, r, o...)
	if err == nil && r.Module == "buddy-world" && r.Method == "snapshot" {
		x.cancel()
	}
	return out, err
}
func (x *ordinaryDeadlineBeforePacket) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, o ...grpc.CallOption) (*emptypb.Empty, error) {
	x.clientCalls++
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	return x.sessionAPI.SendPacket(ctx, r, o...)
}
func TestOrdinaryFollowCanceledBeforePacketDoesNotManufactureUnknown(t *testing.T) {
	for _, op := range []uint32{opcode.MSGMoveStartForward, opcode.MSGMoveStop} {
		for _, priorUnknown := range []bool{false, true} {
			name := opcode.WorldName(op)
			if priorUnknown {
				name += "_prior_unknown"
			}
			t.Run(name, func(t *testing.T) {
				s, f, _, _, _ := idleBodyFixture(t, false)
				seedSyntheticPortalWorldScope(f, 9)
				s.refreshPositionLocked(f.world)
				s.state.Following = true
				s.state.MotionUnconfirmed = priorUnknown
				if priorUnknown {
					s.transportSequence = 9
					s.state.LastTransportFailure = &actionsv1.TransportFailure{Sequence: 9, Opcode: opcode.MSGMoveStartForward, Error: "original unknown", OutcomeUnknown: true}
				}
				original := proto.Clone(s.state.LastTransportFailure)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				ctx = context.WithValue(ctx, ordinaryFollowMovementKey{}, true)
				x := &ordinaryDeadlineBeforePacket{sessionAPI: f, cancel: cancel}
				s.session = x
				before := s.transportSequence
				if s.movementLocked(ctx, op, s.pos, op == opcode.MSGMoveStartForward) || ctx.Err() == nil || x.clientCalls != 0 || len(f.packets) != 0 || s.transportSequence != before || s.state.MotionUnconfirmed != priorUnknown || s.ordinaryMovement != nil || !proto.Equal(original, s.state.LastTransportFailure) {
					t.Fatal("expired pre-dispatch context invoked a packet or changed uncertainty", ctx.Err(), x.clientCalls, s.transportSequence, s.stateLocked(), f.packets)
				}
			})
		}
	}
}
