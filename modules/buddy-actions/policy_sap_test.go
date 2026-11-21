package buddyactions

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func policySapContext(token string) context.Context {
	outgoing := modcb.WithPolicySpell(context.Background(), token)
	md, _ := metadata.FromOutgoingContext(outgoing)
	return metadata.NewIncomingContext(context.Background(), md)
}

func policySapFixture(t *testing.T) (*Service, *fakeSession, *actionsv1.SapRequest) {
	t.Helper()
	s, f, req := sapFixture(t)
	f.world.Self.ChannelSpellKnown = true
	s.controlTemplates = map[uint32]controlTemplate{555: {creatureType: 7}}
	return s, f, req
}

func TestPolicySapRefusesWithoutPreparatoryWrites(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   codes.Code
		change func(*Service, *fakeSession, *actionsv1.SapRequest)
	}{
		{"approach", codes.InvalidArgument, func(_ *Service, _ *fakeSession, r *actionsv1.SapRequest) { r.Approach = true }},
		{"cancel", codes.InvalidArgument, func(_ *Service, _ *fakeSession, r *actionsv1.SapRequest) { r.Cancel = true }},
		{"other session", codes.FailedPrecondition, func(_ *Service, _ *fakeSession, r *actionsv1.SapRequest) { r.SessionId = "previous" }},
		{"other learned kind", codes.FailedPrecondition, func(_ *Service, _ *fakeSession, r *actionsv1.SapRequest) { r.SpellId = 118 }},
		{"rank unknown", codes.FailedPrecondition, func(s *Service, _ *fakeSession, _ *actionsv1.SapRequest) { s.setKnownSpell(6770, false) }},
		{"moving", codes.FailedPrecondition, func(s *Service, _ *fakeSession, _ *actionsv1.SapRequest) { s.state.Moving = true }},
		{"movement unsettled", codes.FailedPrecondition, func(s *Service, _ *fakeSession, _ *actionsv1.SapRequest) { s.state.MotionUnconfirmed = true }},
		{"channel unknown", codes.FailedPrecondition, func(_ *Service, f *fakeSession, _ *actionsv1.SapRequest) { f.world.Self.ChannelSpellKnown = false }},
		{"channel active", codes.FailedPrecondition, func(_ *Service, f *fakeSession, _ *actionsv1.SapRequest) { f.world.Self.ChannelSpellId = 7302 }},
		{"weapon proof unknown", codes.FailedPrecondition, func(s *Service, _ *fakeSession, _ *actionsv1.SapRequest) { delete(s.weapons, 1000) }},
		{"target in combat", codes.FailedPrecondition, func(_ *Service, f *fakeSession, _ *actionsv1.SapRequest) {
			f.lookupCreatures[sheepTarget].UnitFlags = 0x80000
		}},
		{"target out of range", codes.FailedPrecondition, func(_ *Service, f *fakeSession, _ *actionsv1.SapRequest) { f.lookupCreatures[sheepTarget].X = 15 }},
		{"wrong observed GUID", codes.FailedPrecondition, func(_ *Service, f *fakeSession, _ *actionsv1.SapRequest) { f.lookupCreatures[sheepTarget].Guid++ }},
		{"owner target fallback", codes.FailedPrecondition, func(_ *Service, f *fakeSession, _ *actionsv1.SapRequest) {
			f.world.OwnerTarget = f.lookupCreatures[sheepTarget]
			delete(f.lookupCreatures, sheepTarget)
		}},
		{"learned Stealth absent on body", codes.FailedPrecondition, func(s *Service, f *fakeSession, _ *actionsv1.SapRequest) {
			s.setKnownSpell(1784, true)
			emitPolymorph(s, opcode.SMSGAuraUpdate, buffAura(1, 1, 1, 0, 0))
			f.world.Self.ShapeshiftForm = 0
			delete(s.controlTemplates, 555)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, req := policySapFixture(t)
			tc.change(s, f, req)
			_, err := s.Sap(policySapContext(strings.Repeat("a", 64)), req)
			if status.Code(err) != tc.code || len(f.packets) != 0 || s.supportOp != nil {
				t.Fatalf("unexpected admission: error=%v packets=%v op=%v", err, f.packets, s.supportOp)
			}
		})
	}

	t.Run("malformed attribution cannot fall back to legacy", func(t *testing.T) {
		s, f, req := policySapFixture(t)
		req.Approach = true
		_, err := s.Sap(policySapContext("malformed"), req)
		if status.Code(err) != codes.InvalidArgument || len(f.packets) != 0 {
			t.Fatalf("malformed attribution: error=%v packets=%v", err, f.packets)
		}
	})
}

func TestPolicySapAcquiresExactCreatureTypeBeforeCasting(t *testing.T) {
	s, f, req := policySapFixture(t)
	delete(s.controlTemplates, 555)
	ctx := policySapContext(strings.Repeat("c", 64))
	out, err := s.Sap(ctx, req)
	if status.Code(err) != codes.FailedPrecondition || state(t, s).GetReason() != "sap_target_type_pending" || out.GetCastPending() ||
		len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGCreatureQuery || s.supportOp != nil {
		t.Fatal("missing type must acquire only a read observation", out, err, f.packets)
	}
	emitPolymorph(s, opcode.SMSGCreatureQueryResponse, sapTemplateReply(555, 7))
	out, err = s.Sap(ctx, req)
	if err != nil || !out.GetCastPending() || len(f.packets) != 2 || f.packets[1].Opcode != opcode.CMSGCastSpell {
		t.Fatal("observed eligible type did not permit the selected exact cast", out, err, f.packets)
	}
}

type policySapTransport struct {
	*fakeSession
	token string
}

func (f *policySapTransport) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	md, _ := metadata.FromOutgoingContext(ctx)
	f.token = modcb.PolicySpellToken(metadata.NewIncomingContext(context.Background(), md))
	return f.fakeSession.SendPacket(ctx, r, opts...)
}

type policySapTrailer struct{ md metadata.MD }

func (*policySapTrailer) Method() string               { return "OnPacket" }
func (*policySapTrailer) SetHeader(metadata.MD) error  { return nil }
func (*policySapTrailer) SendHeader(metadata.MD) error { return nil }
func (tr *policySapTrailer) SetTrailer(md metadata.MD) error {
	tr.md = metadata.Join(tr.md, md)
	return nil
}

func TestPolicySapWritesExactCastAndReportsCorrelatedAura(t *testing.T) {
	s, f, req := policySapFixture(t)
	transport := &policySapTransport{fakeSession: f}
	s.session = transport
	token := strings.Repeat("b", 64)
	ctx := policySapContext(token)
	st, err := s.Sap(ctx, req)
	if err != nil || !st.GetCastPending() || !st.GetLastSpellOperation().GetTransportWritten() ||
		st.GetLastSpellOperation().GetEffectObserved() || len(f.packets) != 1 || len(f.lookups) != 1 ||
		f.lookups[0] != req.TargetGuid || transport.token != token {
		t.Fatalf("dispatch: %v %v packets=%v lookup=%v token=%q", st, err, f.packets, f.lookups, transport.token)
	}
	write := f.packets[0]
	target, _, err := readGUID(write.Payload[10:])
	if err != nil || write.Opcode != opcode.CMSGCastSpell || binary.LittleEndian.Uint32(write.Payload[1:]) != req.SpellId || target != req.TargetGuid {
		t.Fatalf("unexpected spell write: %v", write)
	}
	if _, err := s.Sap(ctx, req); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 1 {
		t.Fatalf("another write while Sap outstanding: %v %v", err, f.packets)
	}

	packet := func(op uint32, bytes []byte) metadata.MD {
		t.Helper()
		tr := &policySapTrailer{}
		ctx := grpc.NewContextWithServerTransportStream(context.Background(), tr)
		if _, err := s.OnPacket(ctx, &modv1.WorldPacket{Opcode: op, Payload: bytes}); err != nil {
			t.Fatal(err)
		}
		return tr.md
	}
	goFor := func(count byte, caster uint64) []byte {
		p := polymorphGo(count, caster, target, true)
		binary.LittleEndian.PutUint32(p[2*len(packedGUID(caster))+1:], req.SpellId)
		return p
	}
	assertNoReceipt := func(md metadata.MD) {
		t.Helper()
		if _, _, ok := modcb.PolicySpellEffect(md); ok {
			t.Fatalf("uncorrelated or incomplete observation reported an effect: %v", md)
		}
	}
	assertNoReceipt(packet(opcode.SMSGSpellGo, goFor(s.castCount, 2)))
	assertNoReceipt(packet(opcode.SMSGSpellGo, goFor(s.castCount+1, 1)))
	if s.supportOp.view.ServerCastGo || !s.state.CastPending {
		t.Fatal("uncorrelated GO changed the pending Sap")
	}
	assertNoReceipt(packet(opcode.SMSGSpellGo, goFor(s.castCount, 1)))
	if !s.supportOp.view.ServerCastGo || !s.state.CastPending {
		t.Fatal("correlated GO must wait for the aura")
	}
	assertNoReceipt(packet(opcode.SMSGAuraUpdate, sheepAura(target+1, 1, req.SpellId, 20000)))
	assertNoReceipt(packet(opcode.SMSGAuraUpdate, sheepAura(target, 2, req.SpellId, 20000)))

	aura := sheepAura(target, 1, req.SpellId, 20000)
	gotToken, body, ok := modcb.PolicySpellEffect(packet(opcode.SMSGAuraUpdate, aura))
	got := &actionsv1.SpellOperation{}
	if !ok || gotToken != token || proto.Unmarshal(body, got) != nil || got.Phase != "confirmed_aura" ||
		!got.ServerCastGo || !got.EffectObserved || got.SpellId != req.SpellId || got.TargetGuid != target || got.Capability != "sap" {
		t.Fatalf("exact confirmed aura receipt: %v %q %v", ok, gotToken, got)
	}
	assertNoReceipt(packet(opcode.SMSGAuraUpdate, aura))
}
