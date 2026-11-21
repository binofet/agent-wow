package buddyactions

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestServiceTravelProductionWatchdogStopsStartedJourneyWithoutFallback(t *testing.T) {
	for _, kind := range []string{"vendor", "trainer"} {
		t.Run(kind, func(t *testing.T) {
			r := serviceTravelPolicyRig(t, kind)
			sid, self := r.s.sessionID, r.s.selfGUID
			owner, token, until := r.s.state.InteractionOwner, r.s.interactionToken, r.s.interactionUntil
			var stalled *behavior.Runtime
			var revision string
			delays := 0
			r.s.approachDelay = func(ctx context.Context, d time.Duration) error {
				r.advance(d)
				delays++
				if delays != 1 || !r.s.state.Moving || !r.s.state.Traveling ||
					countPackets(r.f, opcode.MSGMoveStartForward) != 1 || len(r.f.packets) != 1 ||
					r.s.sessionID != sid || r.s.selfGUID != self || r.s.interactionToken != token {
					t.Fatal("watchdog was not installed in the first actual owned moving journey", delays, r.s.stateLocked(), r.f.packets)
				}
				// Deliberately replace the healthy mechanical fixture evaluator
				// with a fresh runtime using the production five-millisecond
				// default. It has no last-good policy to hide the stalled read.
				stalled = behavior.New()
				var err error
				revision, err = stalled.Reload("return {decide=function(input) while true do end end}", "")
				if err != nil {
					t.Fatal("could not load the production-budget stalled policy", err)
				}
				r.s.formation.runtime = stalled
				return ctx.Err()
			}

			got, err := r.travel(context.Background(), r.request)
			if err != nil || got == nil || stalled == nil || delays != 1 {
				t.Fatal("travel did not exercise the started-journey watchdog", got, err, delays)
			}
			if got.Reason != kind+"_travel_policy_unavailable" || got.Moving || got.Traveling || got.MotionUnconfirmed ||
				got.AutoAttackUnconfirmed || r.s.questApproaching || r.s.formation.runtime != nil || r.s.formation.remote != nil {
				t.Fatal("watchdog failed open, installed a fallback or invented an uncertain movement", got, r.f.packets)
			}
			if len(r.f.packets) != 2 || r.f.packets[0].Opcode != opcode.MSGMoveStartForward || r.f.packets[1].Opcode != opcode.MSGMoveStop ||
				countPackets(r.f, opcode.MSGMoveHeartbeat) != 0 {
				t.Fatal("failed decision allowed another step, retry or unrelated game action", r.f.packets)
			}
			if got.SessionId != sid || got.SelfGuid != self || got.InteractionOwner != owner ||
				r.s.sessionID != sid || r.s.selfGUID != self || r.s.interactionToken != token ||
				r.s.interactionUntil != until || r.request.Token != token || !r.s.now().Before(until) {
				t.Fatal("watchdog changed the journey's body, ownership or original live lease", got)
			}
			failure := got.GetLastFollowPolicyFailure()
			assertFollowFailureText(t, failure)
			if failure.Mode != "service_travel" || failure.Stage != "evaluate" || failure.SourceRevision != revision ||
				r.s.formation.sourceRevision != revision || !strings.Contains(failure.Error, "context deadline exceeded") {
				t.Fatal("watchdog lost its exact failed mode, revision or deadline cause", failure)
			}
			state := stalled.Status()
			if state.Evaluations != 1 || state.Failures != 1 || state.QuarantinedRevision != revision ||
				state.Revision != "" || state.LastGoodRevision != "" {
				t.Fatal("stalled policy was retried or replaced by an older successful policy", state)
			}
		})
	}
}
