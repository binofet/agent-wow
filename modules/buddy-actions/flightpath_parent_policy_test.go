package buddyactions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func flightpathParentPolicySource(t *testing.T) string {
	t.Helper()
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "behaviors", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	return "local tasks=(function()\n" + read("task-stack.lua") + "\nend)()\n" +
		"local planner=(function()\n" + read("flightpath-plan.lua") + "\nend)()\n" +
		"local flightpath=(function()\n" + read("flightpath-activation.lua") + "\nend)()\n" +
		"local discovery=(function()\n" + read("flightpath-discovery.lua") + "\nend)()\n" + read("flightpath-parent.lua")
}

func newFlightpathParentPolicyFixture(t *testing.T) (*settlementPolicyFixture, string) {
	t.Helper()
	p := newSettlementPolicyFixture(t, false)
	x := p.f.base.x
	// The existing native fixture supplies a setup lease. The real parent must
	// acquire its own visit-bound idle token rather than adopt that setup token.
	if _, err := x.s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "exploration", Token: x.token,
		SessionId: x.s.sessionID, SelfGuid: x.s.selfGUID, VisitId: x.f.world.VisitRevision}); err != nil {
		t.Fatal("setup lease cleanup", err)
	}
	p.f.input["goal"].(map[string]any)["params"] = map[string]any{"destinationNode": float64(2), "acceptUnknownFare": true}
	source := flightpathParentPolicySource(t)
	session, err := behavior.NewSessionWithOptions(context.Background(), x.s.sessionID, activationPolicySessionSource(source),
		behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal("actual composed parent session", err)
	}
	t.Cleanup(func() { session.Close() })
	p.session = session
	p.refresh()
	return p, source
}

// Only the parent custody methods extend dispatch. Child actions retain the
// established actual typed Actions/World fixture and the very same Session.
func flightpathParentPolicyDispatch(t *testing.T, p *settlementPolicyFixture, action map[string]any) {
	t.Helper()
	method := action["method"].(string)
	if method != "buddy-actions.acquire_idle_interaction" && method != "buddy-actions.release_interaction" {
		p.dispatch(action)
		return
	}
	packed, err := json.Marshal(action["params"])
	if err != nil {
		t.Fatal(err)
	}
	id, err := p.session.ReserveAction(p.decision)
	if err != nil {
		t.Fatal("parent did not share the actual Session action ledger", err)
	}
	p.lastReservation = id
	p.methods = append(p.methods, method)
	var response proto.Message
	x := p.f.base.x
	if method == "buddy-actions.acquire_idle_interaction" {
		request := &actionsv1.IdleInteractionRequest{}
		if err := protojson.Unmarshal(packed, request); err != nil {
			t.Fatal("parent idle request failed actual typed contract", err, action)
		}
		if request.SessionId != x.s.sessionID || request.SelfGuid != x.s.selfGUID || request.VisitId != x.f.world.VisitRevision || request.Owner != "exploration" {
			t.Fatal("parent did not bind current idle custody", request)
		}
		response, err = x.s.AcquireIdleInteraction(context.Background(), request)
	} else {
		request := &actionsv1.InteractionRelease{}
		if err := protojson.Unmarshal(packed, request); err != nil {
			t.Fatal("parent release request failed actual typed contract", err, action)
		}
		if request.SessionId != x.s.sessionID || request.SelfGuid != x.s.selfGUID || request.VisitId != x.f.world.VisitRevision || request.Owner != "exploration" {
			t.Fatal("parent did not bind release to current identity", request)
		}
		response, err = x.s.ReleaseInteraction(context.Background(), request)
	}
	if err != nil {
		t.Fatal("actual native parent custody refused fixture", method, err)
	}
	p.f.input["last"] = map[string]any{"id": action["id"], "method": method, "effect": "mutation", "response": flightpathPlanJSON(t, response)}
	if _, err := p.session.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{
		SessionID: p.session.Status().SessionID, ActionID: id, Outcome: behavior.ActionSettledUnverified}}); err != nil {
		t.Fatal(err)
	}
	p.refresh()
}

func flightpathParentPolicyUntil(t *testing.T, p *settlementPolicyFixture, method string) map[string]any {
	t.Helper()
	// A finite assertion bound only; no gameplay deadline enters parent memory.
	for range 1000 {
		out := p.step()
		if action, ok := out["action"].(map[string]any); ok {
			if action["method"] == method {
				return action
			}
			if action["method"] != "buddy-actions.flightpath_reference" {
				t.Fatal("unexpected parent/child stage action", method, action)
			}
			flightpathParentPolicyDispatch(t, p, action)
		}
		if out["done"] == true || out["resolve"] != nil {
			t.Fatal("parent ended/resolved before expected native stage", method, out)
		}
	}
	t.Fatal("composed parent did not reach expected finite test stage", method, p.f.input)
	return nil
}

func TestFlightpathParentPolicyOwnsFiniteCustodyAcrossActualSameVisitLanding(t *testing.T) {
	p, _ := newFlightpathParentPolicyFixture(t)
	x := p.f.base.x
	goal, err := json.Marshal(p.f.input["goal"])
	if err != nil {
		t.Fatal(err)
	}
	flightpathParentPolicyDispatch(t, p, flightpathParentPolicyUntil(t, p, "buddy-actions.acquire_idle_interaction"))
	departureToken := x.s.interactionToken
	if departureToken == "" || departureToken == x.token || x.s.idleInteraction.visit != 1 {
		t.Fatal("parent adopted setup custody instead of acquiring its own finite token")
	}
	// All child stages below use the original real native fixture: activation,
	// server0, actual World parser, one completion image and actual ground MI /
	// taxi-clear retirement. Scope metadata is synthetic owner-loop evidence;
	// this test does not assert Core generation or booking causality.
	p.accept()
	if p.f.activation.Token != departureToken || p.f.activationCall != 1 {
		t.Fatal("child activation did not consume contextual parent custody", p.f.activation)
	}
	p.qualifySpline()
	p.dispatch(flightpathParentPolicyUntil(t, p, "buddy-world.complete_spline"))
	x.advance(31 * time.Second)
	p.refresh()
	if x.s.interactionToken != "" || x.s.state.InteractionOwner != "" || x.s.flightpathActivation.operation.ArrivalKnown {
		t.Fatal("departure lease expiry was confused with arrival or renewed in flight")
	}
	p.ground()
	p.dispatch(flightpathParentPolicyUntil(t, p, "buddy-world.retire_spline_completion"))
	fresh := flightpathParentPolicyUntil(t, p, "buddy-actions.acquire_idle_interaction")
	parent := p.f.input["memory"].(map[string]any)["flightpathParent"].(map[string]any)
	if parent["childReply"].(map[string]any)["method"] != "buddy-world.retire_spline_completion" {
		t.Fatal("parent did not retain the child's known retirement reply before custody call", parent)
	}
	flightpathParentPolicyDispatch(t, p, fresh)
	arrivalToken := x.s.interactionToken
	if arrivalToken == "" || arrivalToken == departureToken || x.s.idleInteraction.visit != 1 || x.s.flightpathActivation.operation.CurrentSegment != nil {
		t.Fatal("ordinary same-visit landing could not acquire fresh parent custody")
	}
	query := flightpathParentPolicyUntil(t, p, "buddy-actions.query_flightpath")
	if query["params"].(map[string]any)["token"] != arrivalToken {
		t.Fatal("child arrival retained expired departure token", query)
	}
	p.dispatch(query)
	p.arriveMenu(p.f.activation.DestinationNode)
	p.dispatch(flightpathParentPolicyUntil(t, p, "buddy-actions.settle_flightpath"))
	flightpathParentPolicyDispatch(t, p, flightpathParentPolicyUntil(t, p, "buddy-actions.release_interaction"))
	completed := false
	for range 8 {
		out := p.step()
		if out["action"] != nil || out["resolve"] != nil {
			t.Fatal("parent repeated an effect after successful cleanup", out)
		}
		if out["done"] == true {
			completed = true
			break
		}
	}
	if !completed || x.s.interactionToken != "" || x.s.state.InteractionOwner != "" || p.f.activationCall != 1 ||
		!x.s.flightpathActivation.operation.ArrivalKnown || x.s.flightpathActivation.operation.Pending {
		t.Fatal("actual destination settlement and parent release did not finish the single journey")
	}
	want := []string{"buddy-actions.acquire_idle_interaction", "buddy-actions.activate_flightpath", "buddy-world.complete_spline",
		"buddy-world.retire_spline_completion", "buddy-actions.acquire_idle_interaction", "buddy-actions.query_flightpath",
		"buddy-actions.settle_flightpath", "buddy-actions.release_interaction"}
	if !reflect.DeepEqual(p.methods, want) {
		t.Fatal("child acquired custody or parent introduced another controller/effect", p.methods, want)
	}
	currentGoal, err := json.Marshal(p.f.input["goal"])
	if err != nil || string(currentGoal) != string(goal) || countPackets(x.f, opcode.CMSGActivatetaxi) != 1 || countPackets(x.f, opcode.CMSGMoveSplineDone) != 1 ||
		countPackets(x.f, opcode.MSGMoveStartForward) != 0 || countPackets(x.f, opcode.MSGMoveStop) != 0 {
		t.Fatal("parent changed immutable goal or issued duplicate booking/movement", err)
	}
}

func TestFlightpathParentPolicyRenewalKeepsKnownPlannerReplyThroughSessionCAS(t *testing.T) {
	p, source := newFlightpathParentPolicyFixture(t)
	x := p.f.base.x
	flightpathParentPolicyDispatch(t, p, flightpathParentPolicyUntil(t, p, "buddy-actions.acquire_idle_interaction"))
	token := x.s.interactionToken
	read := flightpathParentPolicyUntil(t, p, "buddy-actions.flightpath_reference")
	p.dispatch(read)
	x.advance(11 * time.Second)
	p.refresh()
	renewal := flightpathParentPolicyUntil(t, p, "buddy-actions.acquire_idle_interaction")
	if renewal["params"].(map[string]any)["token"] != token {
		t.Fatal("parent renewal tried to replace its existing token", renewal)
	}
	parent := p.f.input["memory"].(map[string]any)["flightpathParent"].(map[string]any)
	if parent["childReply"].(map[string]any)["id"] != read["id"] {
		t.Fatal("renewal overwrote an unconsumed known planner reply", parent)
	}
	flightpathParentPolicyDispatch(t, p, renewal)
	if err := p.session.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err := p.session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	edited := activationPolicySessionSource(source + "\n-- parent semantic CAS keeps one custody/child checkpoint\n")
	if _, err := p.session.Reload(context.Background(), edited, before.Status.Revision); err != nil {
		t.Fatal(err)
	}
	after, err := p.session.State(context.Background())
	if err != nil || before.Status.Revision == after.Status.Revision || !reflect.DeepEqual(before.Checkpoint, after.Checkpoint) ||
		!reflect.DeepEqual(before.Pending, after.Pending) || p.session.Status().SessionID != x.s.sessionID {
		t.Fatal("CAS replaced memory, ledger or body during interposed renewal", before, after, err)
	}
	action := flightpathParentPolicyUntil(t, p, "buddy-actions.activate_flightpath")
	p.dispatch(action)
	if p.f.activationCall != 1 || p.f.activation.Token != token || x.s.interactionToken != token ||
		p.f.activation.SourceNode != 12 || p.f.activation.DestinationNode != 2 {
		t.Fatal("known child reply was stranded or replanned into another booking after CAS", p.f.activation)
	}
}
