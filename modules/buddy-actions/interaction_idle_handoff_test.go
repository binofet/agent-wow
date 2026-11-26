package buddyactions

import (
	"context"
	"fmt"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func idleExplorationHandoffFixture(t *testing.T) (*Service, *fakeSession, func(time.Duration), *actionsv1.TravelToRequest) {
	t.Helper()
	s, f, advance, r := explorationRig(t)
	if _, err := s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "exploration", Token: r.Token}); err != nil {
		t.Fatal(err)
	}
	s.sessionID = "idle-then-ordinary"
	s.encounterVisit = encounterVisit{revision: 1, mapID: f.world.Self.MapId, bound: true}
	f.world.VisitRevision = 1
	seedSyntheticPortalWorldScope(f, 9)
	held, err := s.AcquireIdleInteraction(context.Background(), &actionsv1.IdleInteractionRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, Owner: "exploration", LeaseMs: 30000})
	if err != nil {
		t.Fatal(err)
	}
	r.Token = held.Token
	return s, f, advance, r
}

func TestFreshOrdinaryInteractionAfterIdleTravelUsesOnlyItsNewLease(t *testing.T) {
	for _, expiry := range []bool{false, true} {
		t.Run(fmt.Sprintf("expiry=%t", expiry), func(t *testing.T) {
			s, f, advance, r := idleExplorationHandoffFixture(t)
			before := s.pos
			first, err := s.TravelTo(context.Background(), r)
			if err != nil || first.Reason != "exploration_within_distance" || first.Moving || first.MotionUnconfirmed || distance(before, s.pos) < 7 || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 {
				t.Fatal("idle leg did not settle", first, err, s.pos, f.packets)
			}
			oldToken, oldBinding := r.Token, s.idleInteraction
			if expiry {
				advance(31 * time.Second)
			} else if _, err = s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, Owner: "exploration", Token: oldToken}); err != nil {
				t.Fatal(err)
			}
			held, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "exploration", LeaseMs: 30000})
			if err != nil || held.Token == oldToken || held.Token == "" {
				t.Fatal("fresh ordinary lease refused", held, err)
			}
			r.Token = held.Token
			r.X = s.pos.x + 12
			r.Y = s.pos.y
			before = s.pos
			packets := len(f.packets)
			second, err := s.TravelTo(context.Background(), r)
			if err != nil || second.Reason != "exploration_within_distance" || second.Moving || second.MotionUnconfirmed || distance(before, s.pos) < 7 || countPackets(f, opcode.MSGMoveStartForward) != 2 || countPackets(f, opcode.MSGMoveStop) != 2 {
				t.Fatal("old idle binding contaminated new ordinary travel", second, err, oldBinding, s.idleInteraction, s.pos, f.packets[packets:])
			}
			if s.idleInteraction != (idleInteractionBinding{}) {
				t.Fatal("new ordinary lease inherited idle provenance", s.idleInteraction)
			}
			if _, err = s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "exploration", Token: held.Token}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIdleBindingIsNotClearedByOrdinaryRenewalOrRefusedHandoff(t *testing.T) {
	for _, mode := range []string{"same_token_renewal", "foreign_acquire", "expired_unknown", "released_cast_pending"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, r := idleExplorationHandoffFixture(t)
			binding, token := s.idleInteraction, r.Token
			request := &actionsv1.InteractionRequest{Owner: "exploration", LeaseMs: 30000}
			switch mode {
			case "same_token_renewal":
				request.Token = token
			case "expired_unknown":
				advance(31 * time.Second)
				s.state.MotionUnconfirmed = true
			case "released_cast_pending":
				if _, err := s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "exploration", Token: token}); err != nil {
					t.Fatal(err)
				}
				s.state.CastPending = true
			}
			n := len(f.packets)
			result, err := s.AcquireInteraction(context.Background(), request)
			if mode == "same_token_renewal" {
				if err != nil || result.Token != token {
					t.Fatal(result, err)
				}
			} else if status.Code(err) != codes.FailedPrecondition {
				t.Fatal("refused handoff admitted", result, err)
			}
			if s.idleInteraction != binding || len(f.packets) != n || mode == "expired_unknown" && (!s.state.MotionUnconfirmed || s.interactionToken != token || s.state.InteractionOwner != "exploration") {
				t.Fatal("unsettled or renewed custody was changed", s.idleInteraction, s.stateLocked(), f.packets[n:])
			}
		})
	}
}
