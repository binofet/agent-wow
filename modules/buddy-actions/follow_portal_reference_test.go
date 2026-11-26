package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"strconv"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/areatriggerreference"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

// Keep the established legacy fixture order while production consumes every
// qualified reference, including boxes. No geometry is duplicated in tests.
var dungeonEntrances = func() []dungeonPortal {
	var result []dungeonPortal
	for _, id := range []uint32{78, 228, 324, 322, 45} {
		for _, row := range areatriggerreference.All() {
			if row.ID == id {
				result = append(result, portalReference(row))
			}
		}
	}
	return result
}()

func portalEntryRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	s.sessionID = f.party.SessionId
	// Synthetic counterpart of the same valid entry observed by both queues.
	f.world.VisitRevision = 1
	s.encounterVisit = encounterVisit{revision: 1, mapID: f.world.Self.MapId, bound: true}
	seedSyntheticPortalWorldScope(f, 9)
	return s, f, advance
}

func seedSyntheticPortalWorldScope(f *fakeSession, coreVisit uint64) {
	// Independent domains: World visit1 is deliberately paired with Core9.
	// Known=false is correct on ordinary ground: this is parsed correlation,
	// not permission to acknowledge a taxi spline or a fabricated raw pose.
	f.world.SelfMotionAuthority = &worldv1.OwnMotionAuthority{SelfGuid: f.world.Self.Guid,
		VisitRevision: f.world.VisitRevision, Revision: 1,
		TransportScope: &modv1.TransportScope{ConnectionId: "synthetic-actions-portal-connection", SelfGuid: f.world.Self.Guid,
			VisitBound: true, VisitRevision: coreVisit, MapId: f.world.Self.MapId, MotionRevision: 1}}
	f.portalCoreScope = clonePortalScope(f.world.SelfMotionAuthority.TransportScope)
}

// This fake models only the private read contract. Real Core owner-loop tests
// independently prove successful-write retention, invalidation and final scope
// fencing; synthetic World fixtures do not establish a realm packet effect.
func (f *fakeSession) retainSyntheticOwnMovementReport(r *modv1.SendPacketRequest) {
	if r.Opcode != opcode.MSGMoveStartForward && r.Opcode != opcode.MSGMoveHeartbeat && r.Opcode != opcode.MSGMoveStop {
		return
	}
	if f.world.GetSelf().GetGuid() == 0 {
		return
	}
	if f.portalCoreScope == nil {
		visit := f.world.GetVisitRevision()
		if visit == 0 {
			visit = 1
		}
		f.portalCoreScope = &modv1.TransportScope{ConnectionId: "synthetic-actions-portal-connection", SelfGuid: f.world.Self.Guid,
			VisitBound: true, VisitRevision: visit, MapId: f.world.Self.MapId}
	}
	f.portalCoreScope.MotionRevision++
	f.ownMovementReport = &modv1.WorldPacket{Opcode: r.Opcode, Payload: append([]byte(nil), r.Payload...),
		TransportScope: clonePortalScope(f.portalCoreScope)}
}

func (f *fakeSession) validateSyntheticPortalScope(r *modv1.SendPacketRequest) error {
	if r.Opcode == opcode.MSGMoveStop && r.TransportScope != nil && !proto.Equal(r.TransportScope, f.portalCoreScope) {
		return modcb.PacketNotDispatchedError("synthetic portal STOP scope superseded before transport")
	}
	if r.Opcode == opcode.CMSGAreatrigger && (r.TransportScope == nil || f.ownMovementReport == nil ||
		!proto.Equal(r.TransportScope, f.ownMovementReport.TransportScope) || !proto.Equal(r.TransportScope, f.portalCoreScope)) {
		return modcb.PacketNotDispatchedError("synthetic current portal report scope required")
	}
	return nil
}

func (f *fakeSession) readSyntheticOwnMovementReport(ctx context.Context, r *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
	value := &structpb.Struct{}
	if r.Request == nil || r.Request.UnmarshalTo(value) != nil || value.Fields["sessionId"].GetStringValue() == "" ||
		value.Fields["selfGuid"].GetStringValue() != strconv.FormatUint(f.world.GetSelf().GetGuid(), 10) {
		return nil, errors.New("invalid movement-report identity")
	}
	attempt := value.Fields["attemptId"].GetStringValue()
	if f.ownMovementReport == nil || attempt != "" && attempt != f.ownMovementAttempt || !proto.Equal(f.ownMovementReport.TransportScope, f.portalCoreScope) {
		return nil, status.Error(codes.FailedPrecondition, "current own movement report unavailable")
	}
	packet := proto.Clone(f.ownMovementReport).(*modv1.WorldPacket)
	if f.onOwnMovementReport != nil {
		f.onOwnMovementReport(packet)
	}
	packed, err := anypb.New(packet)
	return &modv1.InvokeModuleResponse{Result: packed}, firstPortalReportError(ctx.Err(), err)
}

func firstPortalReportError(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

func portalFixtureArrive(t *testing.T, s *Service, f *fakeSession, destination uint32) {
	t.Helper()
	f.world.Self.MapId = destination
	f.world.Self.PositionRevision++
	f.world.VisitRevision++
	seedSyntheticPortalWorldScope(f, f.world.GetSelfMotionAuthority().GetTransportScope().GetVisitRevision()+1)
	entry := binary.LittleEndian.AppendUint32(nil, destination)
	for _, value := range []float32{f.world.Self.X, f.world.Self.Y, f.world.Self.Z, f.world.Self.Orientation} {
		entry = binary.LittleEndian.AppendUint32(entry, math.Float32bits(value))
	}
	for _, p := range []*modv1.WorldPacket{
		{Opcode: opcode.SMSGTransferPending, Payload: binary.LittleEndian.AppendUint32(nil, destination)},
		{Opcode: opcode.SMSGNewWorld, Payload: entry},
	} {
		if _, err := s.OnPacket(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
}
