package buddyactions

import (
	"context"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
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
)

// The actual Core owner-loop timeout test proves delayed successful socket
// writes. This adapter exposes that same private result through the maintained
// Actions callback; only STOP returns a caller timeout, after recording bytes.
type explorationStopResultSession struct {
	sessionAPI
	f                    *fakeSession
	id                   string
	available, uncertain bool
	reads                int
}

func (x *explorationStopResultSession) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	if r.Opcode != opcode.MSGMoveStop {
		return x.sessionAPI.SendPacket(ctx, r, opts...)
	}
	md, _ := metadata.FromOutgoingContext(ctx)
	x.id = modcb.IncomingMovementAttempt(metadata.NewIncomingContext(ctx, md))
	if !x.uncertain {
		if _, err := x.f.SendPacket(context.Background(), r, opts...); err != nil {
			return nil, err
		}
	}
	return nil, status.Error(codes.DeadlineExceeded, "STOP caller deadline; transport result not yet delivered")
}
func (x *explorationStopResultSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module != "session" || r.Method != modcb.OwnMovementReportMethod {
		return x.sessionAPI.InvokeModule(ctx, r, opts...)
	}
	x.reads++
	v := &structpb.Struct{}
	if r.Request.UnmarshalTo(v) != nil || v.AsMap()["attemptId"] != x.id || x.id == "" || !x.available || x.uncertain || x.f.ownMovementReport == nil || !proto.Equal(x.f.ownMovementReport.TransportScope, x.f.portalCoreScope) {
		return nil, status.Error(codes.FailedPrecondition, "no exact current successful write")
	}
	a, _ := anypb.New(proto.Clone(x.f.ownMovementReport))
	return &modv1.InvokeModuleResponse{Result: a}, nil
}
func explorationStopResultRig(t *testing.T) (*Service, *fakeSession, *explorationStopResultSession, *actionsv1.TravelToRequest, func(time.Duration)) {
	t.Helper()
	s, f, advance, r := explorationRig(t)
	s.sessionID = f.party.SessionId
	f.world.VisitRevision = 1
	s.encounterVisit = encounterVisit{revision: 1, mapID: r.MapId, bound: true}
	seedSyntheticPortalWorldScope(f, 9)
	x := &explorationStopResultSession{sessionAPI: f, f: f}
	s.session = x
	return s, f, x, r, advance
}
func TestExplorationStopExactWriteUsesNormalTravelAndReleaseWithoutReplay(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "active lease", true: "expired original lease"}[expired], func(t *testing.T) {
			s, f, x, r, advance := explorationStopResultRig(t)
			out, err := s.TravelTo(context.Background(), r)
			if err != nil || !out.MotionUnconfirmed || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 || distance(s.pos, position{x: r.X, y: r.Y, z: r.Z}) > 3 {
				t.Fatal("fixture did not reach destination and lose STOP response", out, err, f.packets)
			}
			original := proto.Clone(out.LastTransportFailure)
			packets, writes := len(f.packets), out.TransportWrites
			// Missing/private-old-host evidence does not turn the timeout into success.
			if _, err = s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "exploration", Token: r.Token}); status.Code(err) != codes.FailedPrecondition || !s.state.MotionUnconfirmed || len(f.packets) != packets {
				t.Fatal("missing proof released or replayed", err, s.stateLocked())
			}
			if expired {
				advance(31 * time.Second)
				s.stateLocked()
				if s.interactionToken != r.Token || s.state.InteractionOwner != "exploration" {
					t.Fatal("expired unresolved STOP lost its lease", s.stateLocked())
				}
			}
			x.available = true
			out, err = s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "exploration", Token: r.Token, SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1})
			if err != nil || out.MotionUnconfirmed || out.Moving || out.InteractionOwner != "" || s.interactionToken != "" || out.TransportWrites != writes+1 || len(f.packets) != packets || !proto.Equal(original, out.LastTransportFailure) || !modcb.ValidMovementAttempt(x.id) {
				t.Fatal("exact STOP did not retire only physical cleanup", out, err, x.id, f.packets)
			}
			if _, err = s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "exploration", Token: r.Token}); status.Code(err) != codes.FailedPrecondition || len(f.packets) != packets {
				t.Fatal("repeated release wrote", err)
			}
		})
	}
}
func TestExplorationStopExactWriteRetainsUnknownAcrossBarriers(t *testing.T) {
	for _, mode := range []string{"actual unknown", "wrong attempt", "changed SID", "changed self", "World visit", "Core visit", "map", "raw pose", "control lost", "foreign token", "foreign owner", "movement owner", "new Actions write", "later identical movement", "no-op inbound barrier", "wrong payload", "dead self"} {
		t.Run(mode, func(t *testing.T) {
			s, f, x, r, _ := explorationStopResultRig(t)
			if mode == "actual unknown" {
				x.uncertain = true
			}
			out, err := s.TravelTo(context.Background(), r)
			if err != nil || !out.MotionUnconfirmed {
				t.Fatal("fixture", out, err)
			}
			original := proto.Clone(out.LastTransportFailure)
			x.available = true
			switch mode {
			case "wrong attempt":
				x.id = strings.Repeat("f", 64)
			case "changed SID":
				s.sessionID += "replacement"
			case "changed self":
				s.selfGUID++
			case "World visit":
				s.encounterVisit.revision++
				f.world.VisitRevision++
			case "Core visit":
				f.portalCoreScope.VisitRevision++
			case "map":
				f.world.Self.MapId++
			case "raw pose":
				f.world.Self.PositionRevision++
			case "control lost":
				s.clientControlLost = true
			case "foreign token":
				s.interactionToken = "replacement"
			case "foreign owner":
				s.state.InteractionOwner = "quests"
			case "movement owner":
				s.movementOwner = movementOwner{owner: "task:other", token: "other", until: s.now().Add(time.Minute)}
			case "new Actions write":
				s.transportSequence++
			case "later identical movement":
				f.portalCoreScope.MotionRevision++
				f.ownMovementReport.TransportScope = clonePortalScope(f.portalCoreScope)
				x.id = strings.Repeat("e", 64)
			case "no-op inbound barrier":
				f.portalCoreScope.MotionRevision++
			case "wrong payload":
				f.ownMovementReport.Payload[0] ^= 1
			case "dead self":
				f.world.Self.Alive = false
			}
			n := len(f.packets)
			_, err = s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "exploration", Token: r.Token})
			if status.Code(err) != codes.FailedPrecondition || !s.state.MotionUnconfirmed || len(f.packets) != n || !proto.Equal(original, s.state.LastTransportFailure) {
				t.Fatal("barrier discarded UNKNOWN or replayed", mode, err, s.stateLocked(), f.packets)
			}
		})
	}
}
