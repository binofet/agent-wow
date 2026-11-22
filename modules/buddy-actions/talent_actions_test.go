package buddyactions

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func talentRig(t *testing.T) (*Service, *fakeSession) {
	t.Helper()
	s, f, _ := rig(t)
	s.sessionID = f.party.SessionId
	s.talents.entropy = bytes.NewReader(bytes.Repeat([]byte{17}, 16))
	return s, f
}
func observeTalentPacket(t *testing.T, s *Service, free uint32, active byte, specs ...talentSpec) {
	t.Helper()
	if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGTalentsInfo, Payload: playerTalentPacket(free, active, specs...)}); err != nil {
		t.Fatal(err)
	}
}
func readTalent(t *testing.T, s *Service) *actionsv1.TalentStateResponse {
	t.Helper()
	v, err := s.TalentState(context.Background(), &actionsv1.TalentStateRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func talentRequest(s *Service, view *actionsv1.TalentStateResponse, id string) *actionsv1.LearnTalentRequest {
	return &actionsv1.LearnTalentRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, ExpectedRevision: view.Revision,
		SpecIndex: view.ActiveSpecIndex, TalentId: 123, RankIndex: 0, RequestId: id}
}

func TestPlayerTalentObservationUsesFullOwnPacketAndDetachedFacts(t *testing.T) {
	s, f := talentRig(t)
	if view := readTalent(t, s); view.Known || view.Revision != "" {
		t.Fatal("absence invented an empty build", view)
	}
	observeTalentPacket(t, s, 31, 0, talentSpec{glyphs: make([]uint16, 6)})
	view := readTalent(t, s)
	if !view.Known || view.FreePoints != 31 || len(view.Specs) != 1 || len(view.Specs[0].Glyphs) != 6 || view.Revision == "" {
		t.Fatal(view)
	}
	view.Specs[0].Glyphs[0] = 999
	if readTalent(t, s).Specs[0].Glyphs[0] != 0 {
		t.Fatal("caller mutated the saved server facts")
	}
	revision := view.Revision
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGTalentsInfo, Payload: []byte{1, 99}})
	if readTalent(t, s).Revision != revision {
		t.Fatal("pet packet replaced player facts")
	}
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGTalentsInfo, Payload: []byte{0, 1}})
	bad := readTalent(t, s)
	if bad.FreePoints != 31 || bad.Revision != revision || bad.ObservationError == "" {
		t.Fatal("partial packet replaced complete facts", bad)
	}
	if _, err := s.LearnTalent(context.Background(), talentRequest(s, bad, "partial")); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
		t.Fatal("malformed update admitted a spend", err, f.packets)
	}
}

func TestLearnTalentWritesExactNormalRankOnceAndWaitsForObservedRank(t *testing.T) {
	s, f := talentRig(t)
	observeTalentPacket(t, s, 31, 0, talentSpec{glyphs: make([]uint16, 6)})
	r := talentRequest(s, readTalent(t, s), "first")
	written, err := s.LearnTalent(context.Background(), r)
	if err != nil || !written.Operation.TransportWritten || written.Operation.RequestedRankObserved || len(f.packets) != 1 {
		t.Fatal(written, err, f.packets)
	}
	if p := f.packets[0]; p.Opcode != opcode.CMSGLearnTalent || !bytes.Equal(p.Payload, []byte{123, 0, 0, 0, 0, 0, 0, 0}) {
		t.Fatal("not the ordinary talent/rank packet", p)
	}
	if _, err = s.LearnTalent(context.Background(), r); err != nil || len(f.packets) != 1 {
		t.Fatal("same request replayed", err, f.packets)
	}
	changed := proto.Clone(r).(*actionsv1.LearnTalentRequest)
	changed.TalentId++
	if _, err = s.LearnTalent(context.Background(), changed); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("same ID changed meaning", err)
	}
	changed = proto.Clone(r).(*actionsv1.LearnTalentRequest)
	changed.RequestId = "another"
	if _, err = s.LearnTalent(context.Background(), changed); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 1 {
		t.Fatal("another write consumed the same observation", err)
	}
	observeTalentPacket(t, s, 30, 0, talentSpec{ranks: []talentRank{{123, 0}}})
	observed := readTalent(t, s)
	if !observed.Operation.RequestedRankObserved || observed.Operation.Phase != "requested_rank_observed" || observed.Revision == r.ExpectedRevision {
		t.Fatal("actual rank did not reconcile receipt", observed)
	}
	next := talentRequest(s, observed, "next")
	next.RankIndex = 1
	if _, err = s.LearnTalent(context.Background(), next); err != nil || len(f.packets) != 2 || binary.LittleEndian.Uint32(f.packets[1].Payload[4:]) != 1 {
		t.Fatal("exact caller-selected next rank failed", err)
	}
}

func TestLearnTalentUnknownWriteRemainsPrivateNoReplayUntilActualRank(t *testing.T) {
	s, f := talentRig(t)
	observeTalentPacket(t, s, 31, 0, talentSpec{})
	r := talentRequest(s, readTalent(t, s), "unknown")
	f.failSend = true
	if _, err := s.LearnTalent(context.Background(), r); err == nil {
		t.Fatal("lost write was reported successful")
	}
	f.failSend = false
	if got, err := s.LearnTalent(context.Background(), r); err != nil || !got.Operation.TransportOutcomeUnknown || got.Operation.TransportWritten || len(f.packets) != 1 {
		t.Fatal("unknown was replayed or upgraded to written", got, err)
	}
	observeTalentPacket(t, s, 31, 0, talentSpec{})
	view := readTalent(t, s)
	if !view.Operation.TransportOutcomeUnknown || view.Operation.RequestedRankObserved {
		t.Fatal("uncorrelated unchanged snapshot invented rejection or success", view)
	}
	if _, err := s.LearnTalent(context.Background(), talentRequest(s, view, "alternate")); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 1 {
		t.Fatal("unknown write lost custody", err)
	}
	observeTalentPacket(t, s, 30, 0, talentSpec{ranks: []talentRank{{123, 0}}})
	view = readTalent(t, s)
	if !view.Operation.RequestedRankObserved || !view.Operation.TransportOutcomeUnknown || view.Operation.TransportWritten {
		t.Fatal("positive state must preserve transport uncertainty", view)
	}
	newRank := talentRequest(s, view, "after-effect")
	newRank.RankIndex = 1
	if _, err := s.LearnTalent(context.Background(), newRank); err != nil || len(f.packets) != 2 {
		t.Fatal("observed effect did not free future explicit choices", err)
	}
}

type talentRefusingSession struct {
	*fakeSession
	refuse bool
}

func (f *talentRefusingSession) SendPacket(ctx context.Context, p *modv1.SendPacketRequest, options ...grpc.CallOption) (*emptypb.Empty, error) {
	if f.refuse {
		return nil, modcb.PacketNotDispatchedError("pre-transport refusal")
	}
	return f.fakeSession.SendPacket(ctx, p, options...)
}

func TestLearnTalentDefiniteNoWriteAllowsFreshChoiceWithoutReplayingRequest(t *testing.T) {
	s, f := talentRig(t)
	transport := &talentRefusingSession{fakeSession: f, refuse: true}
	s.session = transport
	observeTalentPacket(t, s, 31, 0, talentSpec{})
	r := talentRequest(s, readTalent(t, s), "refused")
	if _, err := s.LearnTalent(context.Background(), r); !modcb.PacketWasNotDispatched(err) || len(f.packets) != 0 {
		t.Fatal("lost definite pre-write proof", err)
	}
	transport.refuse = false
	if got, err := s.LearnTalent(context.Background(), r); err != nil || got.Operation.Phase != "not_dispatched" || len(f.packets) != 0 {
		t.Fatal("same refused request replayed", got, err)
	}
	fresh := proto.Clone(r).(*actionsv1.LearnTalentRequest)
	fresh.RequestId = "fresh"
	if _, err := s.LearnTalent(context.Background(), fresh); err != nil || len(f.packets) != 1 {
		t.Fatal("definitive refusal blocked a new explicit choice", err)
	}
}

func TestLearnTalentExactBodySpecRevisionAndInstanceFences(t *testing.T) {
	s, f := talentRig(t)
	observeTalentPacket(t, s, 31, 0, talentSpec{}, talentSpec{})
	r := talentRequest(s, readTalent(t, s), "bound")
	for _, edit := range []func(*actionsv1.LearnTalentRequest){
		func(v *actionsv1.LearnTalentRequest) { v.SessionId = "foreign" },
		func(v *actionsv1.LearnTalentRequest) { v.SelfGuid++ },
		func(v *actionsv1.LearnTalentRequest) { v.ExpectedRevision = "stale" },
		func(v *actionsv1.LearnTalentRequest) { v.SpecIndex = 1 },
	} {
		v := proto.Clone(r).(*actionsv1.LearnTalentRequest)
		edit(v)
		if _, err := s.LearnTalent(context.Background(), v); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
			t.Fatal("unbound request wrote", v, err)
		}
	}
	recreated := NewService(f, s.selfGUID)
	recreated.sessionID = s.sessionID
	recreated.talents.entropy = bytes.NewReader(bytes.Repeat([]byte{18}, 16))
	observeTalentPacket(t, recreated, 31, 0, talentSpec{}, talentSpec{})
	if readTalent(t, recreated).Revision == r.ExpectedRevision {
		t.Fatal("same-SID module recreation reused a revision")
	}
	if _, err := recreated.LearnTalent(context.Background(), r); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
		t.Fatal("old instance spend survived recreation", err)
	}
	s.talents.entropy, s.talents.epoch = io.LimitReader(bytes.NewReader(nil), 0), [16]byte{}
	observeTalentPacket(t, s, 31, 0, talentSpec{})
	if v := readTalent(t, s); !v.Known || v.Revision != "" || v.ObservationError == "" {
		t.Fatal("entropy failure hid raw facts or fabricated an identity", v)
	}
	if _, err := s.LearnTalent(context.Background(), r); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
		t.Fatal("unbound observation admitted spend", err)
	}
}
