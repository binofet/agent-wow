package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func receiveResurrectionOffer(t *testing.T, s *Service, caster uint64) {
	t.Helper()
	p := binary.LittleEndian.AppendUint64(nil, caster)
	p = binary.LittleEndian.AppendUint32(p, 1)
	p = append(p, 0, 0, 0)
	if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGResurrectRequest, Payload: p}); err != nil {
		t.Fatal(err)
	}
}

func observedOfferRequest(t *testing.T, s *Service) *actionsv1.AcceptResurrectionRequest {
	t.Helper()
	v, err := s.EncounterObservation(context.Background(), &emptypb.Empty{})
	if err != nil || v.GetRecovery().GetResurrectionOffer() == nil {
		t.Fatal("actual offer not reflected", v, err)
	}
	offer := v.Recovery.ResurrectionOffer
	return &actionsv1.AcceptResurrectionRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, CasterGuid: offer.CasterGuid,
		DeathEpoch: v.Recovery.DeathEpoch, ExpectedOfferRevision: offer.Revision}
}

func TestResurrectionOfferNativeMaintenanceIsPassiveAndAgeIsNotAnExpiry(t *testing.T) {
	s, f, advance := recoveryRig(t)
	receiveResurrectionOffer(t, s, 3)
	s.Tick(context.Background())
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGResurrectResponse) != 0 || countPackets(f, opcode.CMSGRepopRequest) != 0 {
		t.Fatal("native maintenance chose acceptance or release", f.packets)
	}
	r := observedOfferRequest(t, s)
	advance(61 * time.Second)
	v, _ := s.EncounterObservation(context.Background(), &emptypb.Empty{})
	offer := v.GetRecovery().GetResurrectionOffer()
	if offer == nil || offer.Revision != r.ExpectedOfferRevision || offer.AgeMs != 61000 || offer.ResponseAttempted || offer.ReclaimDelayMs != nil {
		t.Fatal("local policy lifetime was fabricated as a server fact", offer)
	}
	if _, err := s.AcceptResurrection(context.Background(), r); err != nil || countPackets(f, opcode.CMSGResurrectResponse) != 1 {
		t.Fatal("exact caller could not accept the still observed offer", err, f.packets)
	}
	if state(t, s).Recovery.AliveObserved || !state(t, s).Recovery.ResurrectionOffer.ResponseWritten {
		t.Fatal("transport invented actual life", state(t, s))
	}
}

type resurrectionNoWriteSession struct {
	*fakeSession
	refuse bool
}

func (f *resurrectionNoWriteSession) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	if f.refuse {
		return nil, modcb.PacketNotDispatchedError("current callback body changed")
	}
	return f.fakeSession.SendPacket(ctx, r, opts...)
}

func TestResurrectionOfferExactIdentityAndKnownNoWriteVersusUnknownCustody(t *testing.T) {
	ctx := context.Background()
	s, f, _ := recoveryRig(t)
	receiveResurrectionOffer(t, s, 3)
	r := observedOfferRequest(t, s)
	for _, change := range []func(*actionsv1.AcceptResurrectionRequest){
		func(r *actionsv1.AcceptResurrectionRequest) { r.SessionId = "foreign" },
		func(r *actionsv1.AcceptResurrectionRequest) { r.SelfGuid = 7 },
		func(r *actionsv1.AcceptResurrectionRequest) { r.CasterGuid = 8 },
		func(r *actionsv1.AcceptResurrectionRequest) { r.DeathEpoch++ },
		func(r *actionsv1.AcceptResurrectionRequest) { r.ExpectedOfferRevision++ },
	} {
		copy := *r
		change(&copy)
		if _, err := s.AcceptResurrection(ctx, &copy); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
			t.Fatal("foreign/stale identity wrote a response", &copy, err, f.packets)
		}
	}
	callback := &resurrectionNoWriteSession{fakeSession: f, refuse: true}
	s.session = callback
	if _, err := s.AcceptResurrection(ctx, r); !modcb.PacketWasNotDispatched(err) || s.resurrectionOffer.consumed || len(f.packets) != 0 {
		t.Fatal("known pretransport refusal stranded acceptance", err, s.resurrectionOffer, f.packets)
	}
	callback.refuse = false
	f.failSend = true
	if _, err := s.AcceptResurrection(ctx, r); err != nil || !s.resurrectionOffer.consumed || !s.resurrectionOffer.unknown || s.resurrectionOffer.written || len(f.packets) != 1 {
		t.Fatal("unknown write lost custody or claimed success", err, s.resurrectionOffer, f.packets)
	}
	// Normal Acore resurrection effects do not issue a second offer while
	// request data is pending. Repeated bytes supply no new offer nonce and
	// must not reopen an uncertain response from the same caster/death.
	receiveResurrectionOffer(t, s, 3)
	if s.resurrectionOffer.revision != r.ExpectedOfferRevision || !s.resurrectionOffer.consumed || !s.resurrectionOffer.unknown {
		t.Fatal("duplicate offer erased response custody", s.resurrectionOffer)
	}
	f.failSend = false
	if _, err := s.AcceptResurrection(ctx, r); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 1 {
		t.Fatal("unknown acceptance replayed", err, f.packets)
	}
	f.world.Self.Alive, f.world.Self.Health = true, 100
	s.EncounterObservation(ctx, &emptypb.Empty{})
	if s.resurrectionOffer != nil {
		t.Fatal("actual life did not settle old offer")
	}
	f.world.Self.Alive, f.world.Self.Health = false, 0
	receiveResurrectionOffer(t, s, 3)
	fresh := observedOfferRequest(t, s)
	if fresh.DeathEpoch <= r.DeathEpoch || fresh.ExpectedOfferRevision <= r.ExpectedOfferRevision {
		t.Fatal("new actual death reused stale offer identity", fresh, r)
	}
	if _, err := s.AcceptResurrection(ctx, r); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 1 {
		t.Fatal("prior death request accepted a new offer", err)
	}
	if _, err := s.AcceptResurrection(ctx, fresh); err != nil || len(f.packets) != 2 {
		t.Fatal("new actual death could not accept its own offer", err, f.packets)
	}
}
