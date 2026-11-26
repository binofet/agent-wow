package buddyactions

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func idleRetainedCast(t *testing.T) (*Service, *fakeSession, *actionsv1.IdleInteractionRequest) {
	t.Helper()
	s, f, advance, r := idleInteractionRig(t)
	s.applySpellbookLocked(map[uint32]bool{133: true})
	if _, err := s.CastSpell(context.Background(), &actionsv1.CastSpellRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, SpellId: 133,
	}); err != nil {
		t.Fatal(err)
	}
	advance(time.Minute)
	s.supportSnapshotLocked(nil)
	if s.supportOp.view.Phase != "outcome_unconfirmed_no_retry" || s.supportOp.view.VisitId != 1 || s.state.CastPending {
		t.Fatal("fixture did not retain actual timed-out cast", &s.supportOp.view)
	}
	return s, f, r
}

func idleNewLivingVisit(t *testing.T, s *Service, f *fakeSession, r *actionsv1.IdleInteractionRequest) {
	t.Helper()
	p := binary.LittleEndian.AppendUint32(nil, f.world.Self.MapId)
	for i := 0; i < 4; i++ { p = binary.LittleEndian.AppendUint32(p, 0) }
	if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGNewWorld, Payload: p}); err != nil {
		t.Fatal(err)
	}
	r.VisitId = s.encounterVisit.revision
	f.world.VisitRevision = r.VisitId
}

func TestIdleInteractionPriorVisitUnknownCastDoesNotOwnCurrentLivingBody(t *testing.T) {
	s, f, r := idleRetainedCast(t)
	before := proto.Clone(&s.supportOp.view)
	idleNewLivingVisit(t, s, f, r)
	writes := len(f.packets)
	lease, err := s.AcquireIdleInteraction(context.Background(), r)
	if err != nil || lease.GetToken() == "" { t.Fatal("prior visit UNKNOWN blocks current idle body", lease, err) }
	if !proto.Equal(before, &s.supportOp.view) || len(f.packets) != writes {
		t.Fatal("admission rewrote/replayed historical cast", &s.supportOp.view, f.packets)
	}
	r.Token = lease.Token
	if _, err := s.AcquireIdleInteraction(context.Background(), r); err != nil { t.Fatal("renewal lost visit proof", err) }
}

func TestIdleInteractionPriorVisitUnknownCastKeepsCurrentPhysicalGuards(t *testing.T) {
	cases := map[string]func(*Service, *fakeSession, *actionsv1.IdleInteractionRequest){
		"same_visit": func(s *Service, f *fakeSession, r *actionsv1.IdleInteractionRequest) {
			r.VisitId = 1; s.encounterVisit.revision = 1; f.world.VisitRevision = 1
		},
		"unbound_old_visit": func(s *Service, _ *fakeSession, _ *actionsv1.IdleInteractionRequest) { s.supportOp.view.VisitId = 0 },
		"future_old_visit": func(s *Service, _ *fakeSession, _ *actionsv1.IdleInteractionRequest) { s.supportOp.view.VisitId = 3 },
		"unbound_operation": func(s *Service, _ *fakeSession, _ *actionsv1.IdleInteractionRequest) { s.supportOp.view.OperationRevision = 0 },
		"changed_body": func(s *Service, f *fakeSession, r *actionsv1.IdleInteractionRequest) { s.selfGUID = 8; f.world.Self.Guid = 8; r.SelfGuid = 8 },
		"changed_session": func(s *Service, _ *fakeSession, r *actionsv1.IdleInteractionRequest) { s.sessionID = "replacement"; r.SessionId = s.sessionID },
		"request_body": func(_ *Service, _ *fakeSession, r *actionsv1.IdleInteractionRequest) { r.SelfGuid = 8 },
		"world_body": func(_ *Service, f *fakeSession, _ *actionsv1.IdleInteractionRequest) { f.world.Self.Guid = 8 },
		"world_visit": func(_ *Service, f *fakeSession, _ *actionsv1.IdleInteractionRequest) { f.world.VisitRevision = 1 },
		"transfer_unbound": func(s *Service, _ *fakeSession, _ *actionsv1.IdleInteractionRequest) { s.encounterVisit.bound = false },
		"world_unavailable": func(_ *Service, f *fakeSession, _ *actionsv1.IdleInteractionRequest) { f.failSnapshot = true },
		"dead": func(_ *Service, f *fakeSession, _ *actionsv1.IdleInteractionRequest) { f.world.Self.Alive = false; f.world.Self.Health = 0 },
		"life_unknown": func(_ *Service, f *fakeSession, _ *actionsv1.IdleInteractionRequest) { f.world.Self.AliveKnown = false },
		"channel_active": func(_ *Service, f *fakeSession, _ *actionsv1.IdleInteractionRequest) { f.world.Self.ChannelSpellId = 10 },
		"channel_unknown": func(_ *Service, f *fakeSession, _ *actionsv1.IdleInteractionRequest) { f.world.Self.ChannelObjectKnown = false },
		"pending_cast": func(s *Service, _ *fakeSession, _ *actionsv1.IdleInteractionRequest) { s.state.CastPending = true },
		"pending_spell": func(s *Service, _ *fakeSession, _ *actionsv1.IdleInteractionRequest) { s.pendingSpell = 133 },
		"pending_target": func(s *Service, _ *fakeSession, _ *actionsv1.IdleInteractionRequest) { s.pendingTarget = 17 },
		"motion_unknown": func(s *Service, _ *fakeSession, _ *actionsv1.IdleInteractionRequest) { s.state.MotionUnconfirmed = true },
		"control_lost": func(s *Service, _ *fakeSession, _ *actionsv1.IdleInteractionRequest) { s.clientControlLost = true },
		"transport_unknown": func(s *Service, _ *fakeSession, _ *actionsv1.IdleInteractionRequest) { s.supportOp.view.Phase = "transport_unconfirmed_no_retry" },
		"channeling": func(s *Service, _ *fakeSession, _ *actionsv1.IdleInteractionRequest) { s.supportOp.view.Phase = "channeling" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, f, r := idleRetainedCast(t)
			idleNewLivingVisit(t, s, f, r)
			mutate(s, f, r)
			before := idleRawSnapshot(t, s)
			writes := len(f.packets)
			if _, err := s.AcquireIdleInteraction(context.Background(), r); err == nil { t.Fatal("unverified body admitted") }
			if !reflect.DeepEqual(before, idleRawSnapshot(t, s)) || len(f.packets) != writes {
				t.Fatal("refusal changed custody or wrote a packet")
			}
		})
	}
}
