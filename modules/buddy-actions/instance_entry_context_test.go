package buddyactions

import (
	"context"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
)

type instanceEntryAttributionSession struct {
	sessionAPI
	tokens   []string
	canceled []bool
}

func (f *instanceEntryAttributionSession) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	md, _ := metadata.FromOutgoingContext(ctx)
	values := md.Get("agentwow-policy-spell")
	token := ""
	if len(values) == 1 {
		token = values[0]
	}
	f.tokens = append(f.tokens, token)
	f.canceled = append(f.canceled, ctx.Err() != nil)
	return f.sessionAPI.SendPacket(ctx, r, opts...)
}

func instanceEntryPolicyContext() (context.Context, string) {
	token := strings.Repeat("c", 64)
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("agentwow-policy-spell", token)), token
}

func TestAreaTriggerForwardsDispatchIdentityToScopedStopAndTrigger(t *testing.T) {
	s, f, r, _, _ := newAreaTriggerFixture(t)
	client := &instanceEntryAttributionSession{sessionAPI: s.session}
	s.session = client
	ctx, token := instanceEntryPolicyContext()
	op, err := s.NotifyAreaTrigger(ctx, r)
	if err != nil || !op.TransportWritten || len(client.tokens) != 2 || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.CMSGAreatrigger) != 1 {
		t.Fatal(op, err, client.tokens, f.packets)
	}
	for i, got := range client.tokens {
		if got != token || client.canceled[i] {
			t.Fatal("native packet lost the exact dispatch identity", i, got, client.canceled)
		}
	}
}

func TestInstanceEntryTravelCancellationKeepsDispatchIdentityOnOwedStop(t *testing.T) {
	s, f, advance, r := explorationRig(t)
	client := &instanceEntryAttributionSession{sessionAPI: s.session}
	s.session = client
	base, token := instanceEntryPolicyContext()
	ctx, cancel := context.WithCancel(base)
	defer cancel()
	s.approachDelay = func(ctx context.Context, d time.Duration) error { advance(d); cancel(); return ctx.Err() }
	_, err := s.TravelTo(ctx, r)
	if err == nil || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 || len(client.tokens) != 2 || s.state.MotionUnconfirmed {
		t.Fatal("canceled owned travel did not send its normal bounded cleanup", err, client.tokens, f.packets)
	}
	for i, got := range client.tokens {
		if got != token || client.canceled[i] {
			t.Fatal("normal movement/cleanup lost policy attribution or used canceled transport", i, got, client.canceled)
		}
	}
}

func instanceEntryIdleTravelFixture(t *testing.T) (*Service, *fakeSession, func(time.Duration), *actionsv1.TravelToRequest) {
	t.Helper()
	s, f, advance, r := explorationRig(t)
	s.sessionID = f.party.SessionId
	s.encounterVisit = encounterVisit{revision: 1, mapID: 1, bound: true}
	f.world.VisitRevision = 1
	seedSyntheticPortalWorldScope(f, 9)
	s.session = &idleBodyReportSession{sessionAPI: s.session, f: f}
	if _, err := s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "exploration", Token: r.Token}); err != nil {
		t.Fatal(err)
	}
	lease, err := s.AcquireIdleInteraction(context.Background(), &actionsv1.IdleInteractionRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, Owner: "exploration", LeaseMs: 30000})
	if err != nil {
		t.Fatal(err)
	}
	r.Token = lease.Token
	return s, f, advance, r
}

func TestInstanceEntryIdleTravelCannotCarryTokenAcrossSameMapVisit(t *testing.T) {
	for _, seam := range []string{"before World", "after World", "matching current visit"} {
		t.Run(seam, func(t *testing.T) {
			s, f, _, r := instanceEntryIdleTravelFixture(t)
			token := s.interactionToken
			change := func() { s.encounterVisit.revision = 2; f.world.VisitRevision = 2 }
			switch seam {
			case "before World":
				change()
			case "after World":
				client := &areaTriggerCountingSession{sessionAPI: s.session}
				client.beforeWorld = func() { change(); client.beforeWorld = nil }
				s.session = client
			}
			got, err := s.TravelTo(context.Background(), r)
			if err != nil || got == nil || s.interactionToken != token || s.idleInteraction.visit != 1 {
				t.Fatal("travel refusal changed the parent's binding", got, err, s.idleInteraction)
			}
			if seam == "matching current visit" {
				if countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 || got.Reason != "exploration_within_distance" {
					t.Fatal("current idle visit no longer travels normally", got, f.packets)
				}
			} else if len(f.packets) != 0 || got.Reason != "exploration_idle_body_or_visit_changed" {
				t.Fatal("same-map visit replacement carried the old idle token", seam, got, f.packets)
			}
		})
	}
}
