package buddyactions

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/areatriggerreference"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type areaTriggerCountingSession struct {
	sessionAPI
	worldReads  int
	beforeWorld func()
}

func (f *areaTriggerCountingSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module == "buddy-world" && r.Method == "snapshot" {
		f.worldReads++
		if f.beforeWorld != nil {
			f.beforeWorld()
		}
	}
	return f.sessionAPI.InvokeModule(ctx, r, opts...)
}

func newAreaTriggerFixture(t *testing.T) (*Service, *fakeSession, *actionsv1.NotifyAreaTriggerRequest, func(time.Duration), *areaTriggerCountingSession) {
	t.Helper()
	s, f, advance, _, volume := boxPortalEntryFixture(t, true)
	// The explicit caller missed the owner's entrance sighting. No native
	// owner witness, raw Follow toggle or pretend owner position is supplied.
	toggle(t, s, false)
	if s.followPortal.portal.trigger != 0 {
		t.Fatal("explicit fixture retained an owner witness")
	}
	lease, err := s.AcquireIdleInteraction(context.Background(), &actionsv1.IdleInteractionRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, Owner: "exploration", LeaseMs: 30000})
	if err != nil {
		t.Fatal(err)
	}
	client := &areaTriggerCountingSession{sessionAPI: s.session}
	s.session = client
	r := &actionsv1.NotifyAreaTriggerRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, Token: lease.Token, TriggerId: volume.trigger,
		ExpectedPositionRevision: s.posRevision, ClientRequestId: "explicit-entry-1"}
	return s, f, r, advance, client
}

func TestAreaTriggerReferencesAreBoundFilteredDetachedFacts(t *testing.T) {
	s, f, r, _, client := newAreaTriggerFixture(t)
	query := &actionsv1.AreaTriggerReferenceRequest{SessionId: r.SessionId, SelfGuid: r.SelfGuid, VisitId: r.VisitId, DestinationMapId: 349}
	view, err := s.AreaTriggerReference(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	var expected []areatriggerreference.Trigger
	for _, row := range areatriggerreference.All() {
		if row.SourceMap == f.world.Self.MapId && row.DestinationMap == query.DestinationMapId {
			expected = append(expected, row)
		}
	}
	if len(view.Volumes) != len(expected) || len(expected) < 2 || view.SourceCommit != areatriggerreference.Commit || view.SessionId != r.SessionId || view.VisitId != 1 {
		t.Fatal("reference read changed the factual collection", view)
	}
	for i, volume := range view.Volumes {
		row := expected[i]
		if !volume.ReferenceOnly || volume.TriggerId != row.ID || volume.SourceMapId != row.SourceMap || volume.DestinationMapId != row.DestinationMap ||
			volume.X != row.X || volume.Y != row.Y || volume.Z != row.Z || volume.Radius != row.Radius || volume.Length != row.Length || volume.Width != row.Width || volume.Height != row.Height || volume.Orientation != row.Orientation {
			t.Fatal("reference fields were ranked, projected or replaced", volume, row)
		}
	}
	view.Volumes[0].X = 123
	again, err := s.AreaTriggerReference(context.Background(), query)
	if err != nil || again.Volumes[0].X != expected[0].X || len(f.packets) != 0 || s.areaTrigger.revision != 0 || client.worldReads != 2 {
		t.Fatal("reference mutation escaped or sent gameplay", again, err, f.packets)
	}
	query.VisitId++
	if _, err := s.AreaTriggerReference(context.Background(), query); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("stale body admitted", err)
	}
}

func TestAreaTriggerExplicitEntryHasNoOwnerWitnessAndExactReplay(t *testing.T) {
	s, f, r, advance, client := newAreaTriggerFixture(t)
	op, err := s.NotifyAreaTrigger(context.Background(), r)
	if err != nil || !op.Pending || !op.TransportWritten || op.TransportUnknown || op.NotDispatched || op.Revision != 1 ||
		op.SourceScope.GetVisitRevision() != 9 || s.followPortal.portal.trigger != 0 || s.state.Following ||
		countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.CMSGAreatrigger) != 1 {
		t.Fatal("explicit entry depended on a false owner witness or lost its packet receipt", op, err, s.followPortal, f.packets)
	}
	for _, packet := range f.packets {
		if packet.Opcode == opcode.CMSGAreatrigger && (!bytes.Equal(packet.Payload, binary.LittleEndian.AppendUint32(nil, r.TriggerId)) || packet.TransportScope == nil) {
			t.Fatal("explicit entry did not use the scoped ID-only packet", packet)
		}
	}
	writes, reads := len(f.packets), client.worldReads
	advance(time.Minute)
	f.failSnapshot = true
	replay, err := s.NotifyAreaTrigger(context.Background(), proto.Clone(r).(*actionsv1.NotifyAreaTriggerRequest))
	if err != nil || !proto.Equal(op, replay) || len(f.packets) != writes || client.worldReads != reads {
		t.Fatal("replay read peers or sent again", replay, err)
	}
	conflict := proto.Clone(r).(*actionsv1.NotifyAreaTriggerRequest)
	conflict.ExpectedPositionRevision++
	if _, err := s.NotifyAreaTrigger(context.Background(), conflict); status.Code(err) != codes.AlreadyExists {
		t.Fatal("CID rebound", err)
	}
	next := proto.Clone(r).(*actionsv1.NotifyAreaTriggerRequest)
	next.ClientRequestId = "new-id-cannot-replace-pending"
	next.ExpectedRevision = 1
	if _, err := s.NotifyAreaTrigger(context.Background(), next); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("pending entry overwritten", err)
	}
	encoded, err := protojson.Marshal(op)
	if err != nil || bytes.Contains(encoded, []byte(r.Token)) {
		t.Fatal("receipt leaked private custody", string(encoded), err)
	}
	op.SourceScope.ConnectionId = "caller-mutated"
	f.failSnapshot = false
	observed, err := s.AreaTriggerObservation(context.Background(), &emptypb.Empty{})
	if err != nil || observed.SourceScope.ConnectionId == op.SourceScope.ConnectionId || observed.ClientRequestId != r.ClientRequestId {
		t.Fatal("receipt was not detached", observed, err)
	}
}

func TestAreaTriggerKnownRefusalIsInspectableAndNeverReplayed(t *testing.T) {
	for _, kind := range []string{"initial World read", "late Core scope"} {
		t.Run(kind, func(t *testing.T) {
			s, f, r, _, client := newAreaTriggerFixture(t)
			if kind == "initial World read" {
				f.failSnapshot = true
			} else {
				f.onOwnMovementReport = func(p *modv1.WorldPacket) { p.TransportScope.MotionRevision++; f.onOwnMovementReport = nil }
			}
			op, err := s.NotifyAreaTrigger(context.Background(), r)
			if err != nil || op.Revision != 1 || op.Pending || !op.NotDispatched || op.TransportWritten || op.TransportUnknown || op.Error == "" || countPackets(f, opcode.CMSGAreatrigger) != 0 {
				t.Fatal("zero-trigger refusal lacked its exact retained receipt", op, err, f.packets)
			}
			if kind == "late Core scope" && countPackets(f, opcode.MSGMoveStop) != 1 {
				t.Fatal("the prior STOP truth was hidden", f.packets)
			}
			reads, writes := client.worldReads, len(f.packets)
			replay, err := s.NotifyAreaTrigger(context.Background(), r)
			seen, readErr := s.AreaTriggerObservation(context.Background(), &emptypb.Empty{})
			if err != nil || readErr != nil || !proto.Equal(op, replay) || !proto.Equal(op, seen) || client.worldReads != reads || len(f.packets) != writes {
				t.Fatal("known refusal replayed", replay, seen, err, readErr)
			}
			f.failSnapshot = false
			next := proto.Clone(r).(*actionsv1.NotifyAreaTriggerRequest)
			next.ClientRequestId = "explicit-fresh-intent"
			next.ExpectedRevision = 1
			next.ExpectedPositionRevision = s.posRevision
			newOp, err := s.NotifyAreaTrigger(context.Background(), next)
			if err != nil || !newOp.TransportWritten || newOp.Revision != 2 || countPackets(f, opcode.CMSGAreatrigger) != 1 {
				t.Fatal("fresh known-safe intent failed", newOp, err)
			}
			old, err := s.NotifyAreaTrigger(context.Background(), r)
			if err != nil || !proto.Equal(old, op) || s.areaTrigger.operation.Revision != 2 {
				t.Fatal("historical retry replaced the current slot", old, err)
			}
		})
	}
}

func setAreaTriggerDestinationFacts(t *testing.T, s *Service, f *fakeSession, op *actionsv1.AreaTriggerOperation) {
	t.Helper()
	source := op.SourceScope
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = 10, 20, 30
	portalFixtureArrive(t, s, f, op.DestinationMapId)
	current := f.world.SelfMotionAuthority.TransportScope
	current.MotionRevision = source.MotionRevision + 3
	current.WorldTransfer = &modv1.ObservedWorldTransfer{Revision: 2, Known: true, Phase: "observed", ConnectionId: source.ConnectionId, SelfGuid: source.SelfGuid,
		FromVisitRevision: source.VisitRevision, FromMapId: source.MapId, FromMotionRevision: source.MotionRevision, TargetMapKnown: true, TargetMapId: op.DestinationMapId,
		PendingMotionRevision: source.MotionRevision + 1, EntryMotionRevision: source.MotionRevision + 2, WorldportAckMotionRevision: source.MotionRevision + 3,
		ToVisitRevision: source.VisitRevision + 1, ToMapId: op.DestinationMapId, WorldportAckWritten: true}
	f.portalCoreScope = clonePortalScope(current)
	f.ownMovementReport = nil
	// The flat Core facts are synthetic protocol correlation. Core/World's
	// separate tests qualify their parser/ACK mechanics; this does not claim
	// a realm accepted this trigger or admitted the same party instance copy.
}

func TestAreaTriggerUnknownRequiresActualAdjacentDestinationAndKeepsHistory(t *testing.T) {
	s, f, r, advance, _ := newAreaTriggerFixture(t)
	f.failSend, f.failSendOpcode = true, opcode.CMSGAreatrigger
	op, err := s.NotifyAreaTrigger(context.Background(), r)
	if err != nil || !op.Pending || !op.TransportUnknown || op.TransportWritten {
		t.Fatal(op, err)
	}
	f.failSend = false
	advance(time.Minute)
	toggle(t, s, true)
	for range 3 {
		s.Tick(context.Background())
	}
	if countPackets(f, opcode.CMSGAreatrigger) != 1 || s.followPortal.notified {
		t.Fatal("follow or lease expiry replayed the explicit slot", s.followPortal, f.packets)
	}
	setAreaTriggerDestinationFacts(t, s, f, op)
	valid := proto.Clone(f.world).(*worldv1.SnapshotResponse)
	for _, kind := range []string{"map only", "World ahead of Actions", "unknown ACK", "foreign connection", "inferred pose", "skipped visit"} {
		f.world = proto.Clone(valid).(*worldv1.SnapshotResponse)
		s.encounterVisit = encounterVisit{revision: 2, mapID: op.DestinationMapId, bound: true}
		switch kind {
		case "map only":
			f.world.SelfMotionAuthority.TransportScope.WorldTransfer = nil
		case "World ahead of Actions":
			s.encounterVisit.revision = 1
		case "unknown ACK":
			f.world.SelfMotionAuthority.TransportScope.WorldTransfer.WorldportAckUnknown = true
		case "foreign connection":
			f.world.SelfMotionAuthority.TransportScope.ConnectionId = "other"
		case "inferred pose":
			f.world.Self.PositionInferred = true
		case "skipped visit":
			f.world.VisitRevision = 3
			s.encounterVisit.revision = 3
			f.world.SelfMotionAuthority.VisitRevision = 3
		}
		seen, err := s.AreaTriggerObservation(context.Background(), &emptypb.Empty{})
		if err != nil || !seen.Pending || seen.DestinationObserved {
			t.Fatal("unsupported facts settled the physical operation", kind, seen, err)
		}
	}
	f.world = valid
	s.encounterVisit = encounterVisit{revision: 2, mapID: op.DestinationMapId, bound: true}
	seen, err := s.AreaTriggerObservation(context.Background(), &emptypb.Empty{})
	if err != nil || seen.Pending || !seen.DestinationObserved || seen.ObservedVisitId != 2 || !seen.TransportUnknown || seen.TransportWritten ||
		seen.Error != op.Error || !proto.Equal(seen.SourceScope, op.SourceScope) || countPackets(f, opcode.CMSGAreatrigger) != 1 {
		t.Fatal("actual adjacent observation erased original uncertainty or caused replay", seen, err, f.packets)
	}
}

func TestAreaTriggerRequiresAcquiredIdleBindingAndExcludesAutoUnknown(t *testing.T) {
	for _, kind := range []string{"legacy lease", "wrong visit binding", "automatic UNKNOWN"} {
		t.Run(kind, func(t *testing.T) {
			s, f, r, _, _ := newAreaTriggerFixture(t)
			switch kind {
			case "legacy lease":
				s.idleInteraction = idleInteractionBinding{}
			case "wrong visit binding":
				s.idleInteraction.visit++
			case "automatic UNKNOWN":
				s.followPortal.notified = true
			}
			before := s.idleInteraction
			if _, err := s.NotifyAreaTrigger(context.Background(), r); status.Code(err) != codes.FailedPrecondition {
				t.Fatal("foreign/uncertain custody admitted", err)
			}
			if len(f.packets) != 0 || s.areaTrigger.revision != 0 || !reflect.DeepEqual(before, s.idleInteraction) {
				t.Fatal("refusal mutated custody or wrote", s.idleInteraction, f.packets)
			}
		})
	}
}

func TestAreaTriggerLateAdmissionRefusalsRetainExactZeroTriggerReceipt(t *testing.T) {
	for _, kind := range []string{"other volume", "controller revision", "inferred World pose", "late token replacement", "Core first same-map visit"} {
		t.Run(kind, func(t *testing.T) {
			s, f, r, _, _ := newAreaTriggerFixture(t)
			switch kind {
			case "other volume":
				r.TriggerId = 3134
			case "controller revision":
				r.ExpectedPositionRevision++
			case "inferred World pose":
				f.world.Self.PositionInferred = true
			case "late token replacement":
				f.onClock = func() {
					s.interactionToken = "replacement-token"
					s.idleInteraction.token = s.interactionToken
				}
			case "Core first same-map visit":
				f.onClock = func() {
					f.portalCoreScope.VisitRevision = 10
					f.ownMovementReport = nil
				}
			}
			op, err := s.NotifyAreaTrigger(context.Background(), r)
			if err != nil || op.Revision != 1 || op.ClientRequestId != r.ClientRequestId || !op.NotDispatched || op.Pending || op.Error == "" ||
				len(f.packets) != 0 || s.state.MotionUnconfirmed {
				t.Fatal("late physical/custody refusal lacked exact retained zero-trigger proof", kind, op, err, f.packets)
			}
			if kind == "late token replacement" && s.interactionToken != "replacement-token" {
				t.Fatal("old refusal overwrote replacement custody")
			}
		})
	}
}
