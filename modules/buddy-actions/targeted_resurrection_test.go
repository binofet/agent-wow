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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestTargetedResurrectionCastsLearnedRedemptionOnExactAllianceNonpartyTarget(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		change func(*Service, *fakeSession, *worldv1.PlayerState, *actionsv1.ResurrectOnceRequest)
		target uint64
	}{
		{name: "alliance player", target: 7},
		{name: "alliance corpse only", target: 0xf10100000000003a, change: func(_ *Service, f *fakeSession, p *worldv1.PlayerState, _ *actionsv1.ResurrectOnceRequest) {
			f.world.NearbyPlayers = nil
			f.world.PlayerCorpses = []*worldv1.CorpseState{{Guid: 0xf10100000000003a, OwnerGuid: p.Guid, Present: true, MapKnown: true, PositionKnown: true, X: p.X, FlagsKnown: true, RaceKnown: true, RaceId: p.RaceId}}
		}},
		{name: "opposite faction", change: func(_ *Service, _ *fakeSession, p *worldv1.PlayerState, _ *actionsv1.ResurrectOnceRequest) {
			p.RaceId = 5
		}},
		{name: "unknown faction", change: func(_ *Service, _ *fakeSession, p *worldv1.PlayerState, _ *actionsv1.ResurrectOnceRequest) {
			p.RaceKnown = false
		}},
		{name: "alive", change: func(_ *Service, _ *fakeSession, p *worldv1.PlayerState, _ *actionsv1.ResurrectOnceRequest) {
			p.Alive, p.Health = true, 100
		}},
		{name: "out of range", change: func(_ *Service, _ *fakeSession, p *worldv1.PlayerState, _ *actionsv1.ResurrectOnceRequest) { p.X = 40 }},
		{name: "caster combat", change: func(_ *Service, f *fakeSession, _ *worldv1.PlayerState, _ *actionsv1.ResurrectOnceRequest) {
			f.world.Self.UnitFlags |= 0x80000
		}},
		{name: "unlearned rank", change: func(_ *Service, _ *fakeSession, _ *worldv1.PlayerState, r *actionsv1.ResurrectOnceRequest) {
			r.SpellId = 10322
		}},
		{name: "wrong session body", change: func(_ *Service, _ *fakeSession, _ *worldv1.PlayerState, r *actionsv1.ResurrectOnceRequest) {
			r.SelfGuid = 4
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, advance := rig(t)
			s.sessionID = "actions-test-body"
			f.world.Self.ClassId, f.world.Self.RaceKnown, f.world.Self.RaceId = 2, true, 1
			f.world.Self.BaseManaKnown, f.world.Self.BaseMana = true, 100
			p := proto.Clone(f.world.Owner).(*worldv1.PlayerState)
			p.Guid, p.RaceKnown, p.RaceId, p.Alive, p.Health = 7, true, 7, false, 0
			f.world.NearbyPlayers = []*worldv1.PlayerState{p}
			learnSpells(s, 7328)
			req := &actionsv1.ResurrectOnceRequest{SessionId: s.sessionID, SelfGuid: 1, TargetGuid: p.Guid}
			if tc.change != nil {
				tc.change(s, f, p, req)
			}
			toolkit, err := s.SpellToolkit(ctx, &emptypb.Empty{})
			if err != nil || !toolkit.GetTargetedResurrectionSupported() {
				t.Fatal("installed capability was not advertised", toolkit, err)
			}
			st, err := s.ResurrectOnce(ctx, req)
			if tc.target == 0 {
				if status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
					t.Fatal("unsafe resurrection was dispatched", st, err, f.packets)
				}
				return
			}
			op := st.GetLastSpellOperation()
			if err != nil || len(f.packets) != 1 || lastCast(f) != 7328 || op.GetTargetGuid() != tc.target || !op.GetTransportWritten() || op.GetEffectObserved() {
				t.Fatal("expected exactly one Redemption write", st, err, f.packets)
			}
			write := f.packets[0]
			target, _, _ := readGUID(write.Payload[10:])
			mask := binary.LittleEndian.Uint32(write.Payload[6:])
			if target != tc.target || (mask == 0x8000) != (tc.target != p.Guid) {
				t.Fatal("incorrect wire target", write)
			}
			advance(20 * time.Second)
			if _, err := s.ResurrectOnce(ctx, req); status.Code(err) != codes.FailedPrecondition || countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatal("same death was replayed", err, f.packets)
			}
		})
	}
}

func TestExplicitNonpartyResurrectionOfferRequiresExactCasterAndConsumesOnce(t *testing.T) {
	ctx := context.Background()
	s, f, _ := rig(t)
	s.sessionID = "actions-test-body"
	f.world.Self.Alive, f.world.Self.Health = false, 0
	req := &actionsv1.AcceptResurrectionRequest{SessionId: s.sessionID, SelfGuid: 1, CasterGuid: 7}
	if _, err := s.AcceptResurrection(ctx, req); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("accepted without a server offer", err)
	}
	p := binary.LittleEndian.AppendUint64(nil, 7)
	p = binary.LittleEndian.AppendUint32(p, 1)
	p = append(p, 0, 0, 0)
	s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGResurrectRequest, Payload: p})
	s.Tick(ctx)
	if countPackets(f, opcode.CMSGResurrectResponse) != 0 {
		t.Fatal("unsolicited nonparty offer auto-accepted")
	}
	req.CasterGuid = 8
	if _, err := s.AcceptResurrection(ctx, req); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("accepted offer from a different caster", err)
	}
	req.CasterGuid = 7
	st, err := s.AcceptResurrection(ctx, req)
	if err != nil || st.GetReason() != "resurrection_accepted_waiting_for_alive_observation" || countPackets(f, opcode.CMSGResurrectResponse) != 1 {
		t.Fatal("expected one server response", st, err, f.packets)
	}
	write := f.packets[len(f.packets)-1]
	if write.Opcode != opcode.CMSGResurrectResponse || binary.LittleEndian.Uint64(write.Payload) != 7 || write.Payload[8] != 1 {
		t.Fatal("incorrect response to server offer", write)
	}
	if _, err := s.AcceptResurrection(ctx, req); status.Code(err) != codes.FailedPrecondition || countPackets(f, opcode.CMSGResurrectResponse) != 1 {
		t.Fatal("response was replayed", err, f.packets)
	}
}
