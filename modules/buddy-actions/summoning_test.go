package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func summoningFixture(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	s.sessionID = f.party.SessionId
	f.party.Group.GroupGuid = 77
	f.world.Owner = nil // the online member is outside visibility
	f.world.VisitRevision = 1
	_, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLoginVerifyWorld, Payload: make([]byte, 20), TransportScope: &modv1.TransportScope{ConnectionId: "ordinary-test", SelfGuid: 1, VisitBound: true, VisitRevision: 9, MotionRevision: 21}})
	if err != nil {
		t.Fatal(err)
	}
	return s, f, advance
}

func summoningRequest(s *Service, entry uint32, kind uint32) (*actionsv1.UseSummoningObjectRequest, *worldv1.GameObjectState) {
	guid := uint64(0xf110)<<48 | uint64(entry)<<24 | 27
	r := &actionsv1.UseSummoningObjectRequest{SessionId: s.sessionID, SelfGuid: 1, VisitId: 1, ExpectedGroupGuid: 77, ObjectGuid: guid, ExpectedCreatorGuid: 3}
	obj := &worldv1.GameObjectState{Guid: guid, Entry: entry, GameObjectType: kind, Present: true, MapKnown: true, PositionKnown: true, X: 2, TypeKnown: true, CreatedByKnown: true, CreatedByGuid: 3, FlagsKnown: true}
	return r, obj
}

func TestSummoningPortalAndStoneUseExactObjectAndPartySelection(t *testing.T) {
	for _, stage := range []struct{ entry, kind uint32 }{{194108, 18}, {194097, 23}, {179944, 18}} {
		s, f, _ := summoningFixture(t)
		r, obj := summoningRequest(s, stage.entry, stage.kind)
		f.world.RequestedGameObjects = []*worldv1.GameObjectState{obj}
		if stage.kind == 23 {
			r.SelectedMemberGuid = 3
			if _, err := s.UseSummoningObject(context.Background(), r); err == nil || len(f.packets) != 0 {
				t.Fatal("stone opened without the observed target")
			}
			selected, err := s.SelectTarget(context.Background(), &actionsv1.SelectTargetRequest{SessionId: s.sessionID, SelfGuid: 1, TargetGuid: 3, VisitId: 1, ExpectedGroupGuid: 77})
			if err != nil || !selected.GetTransportWritten() || selected.SelectionObserved || len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGSetSelection || f.packets[0].TransportScope.VisitRevision != 9 {
				t.Fatal("remote group selection", selected, err)
			}
			f.world.Self.TargetKnown, f.world.Self.TargetGuid = true, 3
		}
		count := len(f.packets)
		out, err := s.UseSummoningObject(context.Background(), r)
		if err != nil || !out.GetLastUse().GetTransportWritten() || out.GetUseRevision() != 1 || out.GetOffer() != nil || len(f.packets) != count+1 {
			t.Fatal("stage use", out, err)
		}
		if got := f.packets[count]; got.Opcode != opcode.CMSGGameobjUse || got.GetTransportScope().GetVisitRevision() != 9 || len(got.Payload) != 8 || binary.LittleEndian.Uint64(got.Payload) != obj.Guid {
			t.Fatal("wrong object write", got)
		}
		if _, err = s.UseSummoningObject(context.Background(), r); err != nil || len(f.packets) != count+1 {
			t.Fatal("replayed object use", err)
		}
	}
}

func TestSummoningObjectRejectsStaleOrUnusableFacts(t *testing.T) {
	for _, reason := range []string{"object_gone", "wrong_creator", "out_of_range", "unselectable", "not_ritual", "own_channel", "offline_creator", "wrong_group", "old_visit", "revision"} {
		t.Run(reason, func(t *testing.T) {
			s, f, _ := summoningFixture(t)
			r, obj := summoningRequest(s, 194108, 18)
			f.world.RequestedGameObjects = []*worldv1.GameObjectState{obj}
			switch reason {
			case "object_gone":
				obj.Present = false
			case "wrong_creator":
				r.ExpectedCreatorGuid = 1
			case "out_of_range":
				obj.X = 30
			case "unselectable":
				obj.Flags = 0x10
			case "not_ritual":
				obj.GameObjectType = 17
			case "own_channel":
				obj.CreatedByGuid, r.ExpectedCreatorGuid = 1, 1
			case "offline_creator":
				f.party.Group.Members[1].Online = false
			case "wrong_group":
				f.party.Group.GroupGuid = 78
			case "old_visit":
				r.VisitId = 2
			case "revision":
				r.ExpectedUseRevision = 2
			}
			if _, err := s.UseSummoningObject(context.Background(), r); err == nil || len(f.packets) != 0 {
				t.Fatal("unexpected use", err, f.packets)
			}
		})
	}
}

func fixtureOffer(t *testing.T, s *Service, caster, seq uint64) {
	t.Helper()
	raw := binary.LittleEndian.AppendUint64(nil, caster)
	raw = binary.LittleEndian.AppendUint32(raw, 27)
	raw = binary.LittleEndian.AppendUint32(raw, 10_000)
	scope := clonePortalScope(s.summoning.visitScope)
	scope.SummonOfferSequence = seq
	if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSummonRequest, Payload: raw, TransportScope: scope}); err != nil {
		t.Fatal(err)
	}
}

func TestSummonResponseRequiresCurrentLiveOfferAndNeverReplays(t *testing.T) {
	for _, accept := range []bool{false, true} {
		s, f, advance := summoningFixture(t)
		r := &actionsv1.RespondToSummonRequest{SessionId: s.sessionID, SelfGuid: 1, VisitId: 1, SummonerGuid: 3, OfferSequence: 4, Accept: proto.Bool(accept)}
		if _, err := s.RespondToSummon(context.Background(), r); err == nil {
			t.Fatal("synthesized an offer")
		}
		fixtureOffer(t, s, 3, 3)
		fixtureOffer(t, s, 3, 4)
		stale := proto.Clone(r).(*actionsv1.RespondToSummonRequest)
		stale.OfferSequence = 3
		if _, err := s.RespondToSummon(context.Background(), stale); err == nil {
			t.Fatal("accepted superseded offer")
		}
		f.world.Self.UnitFlags = 0x80000
		if _, err := s.RespondToSummon(context.Background(), r); err == nil {
			t.Fatal("consumed an offer in combat")
		}
		f.world.Self.UnitFlags = 0
		r.ExpectedGroupGuid = 77
		f.party.Group.Members[1].Online = false
		if _, err := s.RespondToSummon(context.Background(), r); err == nil {
			t.Fatal("consumed an offer after the selected group member went offline")
		}
		f.party.Group.Members[1].Online = true
		f.failSend = accept // exercise both an acknowledged decline and ambiguous accept
		out, err := s.RespondToSummon(context.Background(), r)
		o := out.GetOffer()
		if err != nil || o.GetPending() || o.GetRequestedAccept() != accept || o.GetTransportUnknown() != accept || o.GetTransportWritten() == accept || len(f.packets) != 1 {
			t.Fatal("summon response", out, err)
		}
		packet := f.packets[0]
		if packet.Opcode != opcode.CMSGSummonResponse || packet.TransportScope.SummonOfferSequence != 4 || len(packet.Payload) != 9 || binary.LittleEndian.Uint64(packet.Payload) != 3 || (packet.Payload[8] == 1) != accept {
			t.Fatal("wrong response", packet)
		}
		if _, err = s.RespondToSummon(context.Background(), r); err != nil || len(f.packets) != 1 {
			t.Fatal("retried a consumed offer", err)
		}
		fixtureOffer(t, s, 3, 5)
		r.OfferSequence = 5
		advance(11 * time.Second)
		state, _ := s.ObserveSummoning(context.Background(), nil)
		if state.Offer.Pending {
			t.Fatal("expired offer still pending")
		}
		if _, err = s.RespondToSummon(context.Background(), r); err == nil || len(f.packets) != 1 {
			t.Fatal("expired offer written", err)
		}
	}
}

func TestSummoningUseRetainsAmbiguousWrite(t *testing.T) {
	s, f, _ := summoningFixture(t)
	r, obj := summoningRequest(s, 194108, 18)
	f.world.RequestedGameObjects = []*worldv1.GameObjectState{obj}
	f.failSend = true
	out, err := s.UseSummoningObject(context.Background(), r)
	if err != nil || !out.GetLastUse().GetTransportUnknown() || out.LastUse.TransportWritten {
		t.Fatal(out, err)
	}
	if _, err = s.UseSummoningObject(context.Background(), r); err != nil || len(f.packets) != 1 {
		t.Fatal("unknown use replay", err)
	}
}
