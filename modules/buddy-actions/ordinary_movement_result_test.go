package buddyactions

import (
	"context"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
	"strings"
	"testing"
)

type ordinaryResultSession struct {
	sessionAPI
	f         *fakeSession
	id        string
	available bool
}

func (x *ordinaryResultSession) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, o ...grpc.CallOption) (*emptypb.Empty, error) {
	md, _ := metadata.FromOutgoingContext(ctx)
	x.id = modcb.IncomingMovementAttempt(metadata.NewIncomingContext(ctx, md))
	if _, err := x.f.SendPacket(context.Background(), r, o...); err != nil {
		return nil, err
	}
	// Existing legacy fake retains only START/heartbeat/STOP. Core's actual
	// normal body image also accepts this stationary facing packet.
	if r.Opcode == opcode.MSGMoveSetFacing {
		x.f.portalCoreScope.MotionRevision++
		x.f.ownMovementReport = &modv1.WorldPacket{Opcode: r.Opcode, Payload: append([]byte(nil), r.Payload...), TransportScope: clonePortalScope(x.f.portalCoreScope)}
	}
	return nil, status.Error(codes.DeadlineExceeded, "caller expired after write")
}
func (x *ordinaryResultSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, o ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module == "session" && r.Method == modcb.OwnMovementReportMethod {
		v := &structpb.Struct{}
		if r.Request.UnmarshalTo(v) != nil || v.AsMap()["attemptId"] != x.id || !x.available || x.f.ownMovementReport == nil {
			return nil, status.Error(codes.FailedPrecondition, "no exact result")
		}
		p, _ := anypb.New(proto.Clone(x.f.ownMovementReport))
		return &modv1.InvokeModuleResponse{Result: p}, nil
	}
	return x.sessionAPI.InvokeModule(ctx, r, o...)
}
func TestOrdinaryFollowExactWriteReconcilesWithoutReplayOrUnknownRewrite(t *testing.T) {
	for _, op := range []uint32{opcode.MSGMoveStartForward, opcode.MSGMoveHeartbeat, opcode.MSGMoveStop, opcode.MSGMoveSetFacing} {
		moving := op == opcode.MSGMoveStartForward || op == opcode.MSGMoveHeartbeat
		for _, mode := range []string{"exact", "missing", "wrong attempt", "changed visit", "changed body", "changed raw pose", "foreign owner", "newer attempt", "wrong packet"} {
			t.Run(mode+" "+opcode.WorldName(op), func(t *testing.T) {
				s, f, _, _, _ := idleBodyFixture(t, true)
				s.state.InteractionOwner = ""
				s.interactionToken = ""
				s.idleInteraction = idleInteractionBinding{}
				s.state.Following = true
				x := &ordinaryResultSession{sessionAPI: f, f: f, available: true}
				s.session = x
				s.refreshPositionLocked(f.world)
				if s.movementLocked(context.Background(), op, s.pos, moving) || !s.state.MotionUnconfirmed || s.ordinaryMovement == nil || !modcb.ValidMovementAttempt(x.id) {
					t.Fatal("fixture did not retain timed-out attempt", s.stateLocked(), x.id)
				}
				original := proto.Clone(s.state.LastTransportFailure)
				switch mode {
				case "missing":
					x.available = false
				case "wrong attempt":
					x.id = strings.Repeat("b", 64)
				case "changed visit":
					s.encounterVisit.revision++
				case "changed body":
					s.selfGUID++
				case "changed raw pose":
					f.world.Self.PositionRevision++
				case "foreign owner":
					s.state.InteractionOwner = "other"
				case "newer attempt":
					s.transportSequence++
				case "wrong packet":
					f.ownMovementReport.Payload[0] ^= 1
				}
				s.reconcileOrdinaryMovementLocked(context.Background(), f.world)
				if len(f.packets) != 1 || !proto.Equal(original, s.state.LastTransportFailure) {
					t.Fatal("replayed or rewrote UNKNOWN", f.packets, s.state.LastTransportFailure)
				}
				if mode == "exact" {
					if s.state.MotionUnconfirmed || s.state.Moving != moving || s.ordinaryMovement != nil || s.state.TransportWrites != 1 {
						t.Fatal("known write not restored", s.stateLocked())
					}
					s.reconcileOrdinaryMovementLocked(context.Background(), f.world)
					if s.state.TransportWrites != 1 || len(f.packets) != 1 {
						t.Fatal("exact result counted or sent twice", s.stateLocked(), f.packets)
					}
				} else if !s.state.MotionUnconfirmed {
					t.Fatal("unverified attempt released custody", mode)
				}
			})
		}
	}
}
