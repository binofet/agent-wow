package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/areatriggerreference"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
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
	return s, f, advance
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
	visit := f.world.GetVisitRevision()
	if visit == 0 {
		visit = 1
	}
	motion := uint64(1)
	if f.ownMovementReport != nil {
		motion = f.ownMovementReport.GetTransportScope().GetMotionRevision() + 1
	}
	f.ownMovementReport = &modv1.WorldPacket{Opcode: r.Opcode, Payload: append([]byte(nil), r.Payload...),
		TransportScope: &modv1.TransportScope{ConnectionId: "synthetic-actions-portal-connection", SelfGuid: f.world.Self.Guid,
			VisitBound: true, VisitRevision: visit, MapId: f.world.Self.MapId, MotionRevision: motion}}
}

func (f *fakeSession) validateSyntheticPortalScope(r *modv1.SendPacketRequest) error {
	if r.Opcode == opcode.CMSGAreatrigger && (r.TransportScope == nil || f.ownMovementReport == nil ||
		!proto.Equal(r.TransportScope, f.ownMovementReport.TransportScope)) {
		return modcb.PacketNotDispatchedError("synthetic current portal report scope required")
	}
	return nil
}

func (f *fakeSession) readSyntheticOwnMovementReport(ctx context.Context, r *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
	value := &structpb.Struct{}
	if r.Request == nil || r.Request.UnmarshalTo(value) != nil || value.Fields["sessionId"].GetStringValue() == "" ||
		value.Fields["selfGuid"].GetStringValue() != strconv.FormatUint(f.world.GetSelf().GetGuid(), 10) || f.ownMovementReport == nil {
		return nil, errors.New("current own movement report unavailable")
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
