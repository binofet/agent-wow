package buddyactions

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func flightpathActivationSource(t *testing.T) string {
	t.Helper()
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "behaviors", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	return "local tasks=(function()\n" + read("task-stack.lua") + "\nend)()\nlocal planner=(function()\n" +
		read("flightpath-plan.lua") + "\nend)()\n" + read("flightpath-activation.lua")
}

func activationPolicySessionSource(source string) string {
	return "local activation=(function()\n" + source + `
end)()
local memory={}
return {
 decide=function(frame)
   local value=frame.input;value.memory=memory
   local out=activation.decide(value);memory=out.memory;return out
 end,
 on_event=function() return true end,
 export_state=function() return {version=1,data={memory=memory}} end,
 import_state=function(checkpoint) memory=checkpoint.data.memory;return true end,
}`
}

type activationPolicyFixture struct {
	t              *testing.T
	base           *flightpathPlanFixture
	r              *behavior.Runtime
	input          map[string]any
	source         string
	activation     *actionsv1.FlightpathActivationRequest
	activationCall int
	mutations      []string
	beforeActivate func(*actionsv1.FlightpathActivationRequest)
	basePackets    int
}

func newActivationPolicyFixture(t *testing.T, source, destination uint32, known ...uint32) *activationPolicyFixture {
	t.Helper()
	base := newFlightpathPlanFixture(t, source, destination, known...)
	// The menu is established through actual query/packet parsing above. End
	// its fixture-only lease so the Lua task acquires its own finite token.
	if _, err := base.x.s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "exploration", Token: base.x.token}); err != nil {
		t.Fatal(err)
	}
	r, err := behavior.NewWithOptions(behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	sourceCode := flightpathActivationSource(t)
	if _, err := r.Reload(sourceCode, ""); err != nil {
		t.Fatal(err)
	}
	f := &activationPolicyFixture{t: t, base: base, r: r, input: base.input, source: sourceCode, basePackets: len(base.x.f.packets)}
	f.refresh()
	return f
}

func (f *activationPolicyFixture) refresh() {
	f.t.Helper()
	observations := f.input["observations"].(map[string]any)
	observations["actions"] = flightpathPlanJSON(f.t, state(f.t, f.base.x.s))
	observations["world"] = flightpathPlanJSON(f.t, f.base.x.f.world)
	observations["flightpath"] = flightpathPlanJSON(f.t, f.base.x.observation(f.t))
}

func (f *activationPolicyFixture) step() map[string]any {
	f.t.Helper()
	out, err := f.r.Evaluate(f.input)
	if err != nil {
		f.t.Fatal("actual activation/planner/task-stack Lua failed", err, f.r.Status())
	}
	f.input["memory"] = out["memory"]
	return out
}

func (f *activationPolicyFixture) dispatch(action map[string]any) map[string]any {
	f.t.Helper()
	method, _ := action["method"].(string)
	b, err := json.Marshal(action["params"])
	if err != nil {
		f.t.Fatal(err)
	}
	decode := func(message proto.Message) {
		if err := protojson.Unmarshal(b, message); err != nil {
			f.t.Fatal("Lua action does not fit the actual native request", action, err)
		}
	}
	var response proto.Message
	var callErr error
	if method == "buddy-actions.flightpath_reference" {
		response = nil
		value := f.base.reference(action)
		f.input["last"] = map[string]any{"id": action["id"], "method": method, "effect": "read", "response": value}
		return value
	}
	f.mutations = append(f.mutations, method)
	switch method {
	case "buddy-actions.acquire_interaction":
		request := &actionsv1.InteractionRequest{}
		decode(request)
		if request.Owner != "exploration" || !request.Harvest || request.LeaseMs != 15000 {
			f.t.Fatal("Lua did not request finite guarded exploration custody", request)
		}
		response, callErr = f.base.x.s.AcquireInteraction(context.Background(), request)
	case "buddy-actions.activate_flightpath":
		request := &actionsv1.FlightpathActivationRequest{}
		decode(request)
		f.activation, f.activationCall = proto.Clone(request).(*actionsv1.FlightpathActivationRequest), f.activationCall+1
		if f.beforeActivate != nil {
			f.beforeActivate(request)
		}
		response, callErr = f.base.x.s.ActivateFlightpath(context.Background(), request)
	case "buddy-actions.release_interaction":
		request := &actionsv1.InteractionRelease{}
		decode(request)
		if request.SessionId != f.base.x.s.sessionID || request.SelfGuid != 1 || request.VisitId != 1 {
			f.t.Fatal("Lua cleanup lost its captured session/body/visit binding", request)
		}
		response, callErr = f.base.x.s.ReleaseInteraction(context.Background(), request)
	default:
		f.t.Fatal("activation task attempted an unrelated action", action)
	}
	if callErr != nil {
		f.t.Fatal("actual typed action fixture failed", method, callErr)
	}
	value := flightpathPlanJSON(f.t, response)
	f.input["last"] = map[string]any{"id": action["id"], "method": method, "effect": "mutation", "response": value}
	f.refresh()
	return value
}

func (f *activationPolicyFixture) untilActivation() map[string]any {
	f.t.Helper()
	for tick := 0; tick < 1000; tick++ {
		out := f.step()
		if out["done"] == true || out["resolve"] != nil {
			f.t.Fatal("activation fixture completed or resolved custody before its selected write", out)
		}
		if action, ok := out["action"].(map[string]any); ok {
			if action["method"] == "buddy-actions.activate_flightpath" {
				return action
			}
			f.dispatch(action)
		}
	}
	f.t.Fatal("finite planning fixture did not select its activation")
	return nil
}

func activationPolicyNoEffect(t *testing.T, out map[string]any) {
	t.Helper()
	if out["action"] != nil || out["resolve"] != nil || out["done"] == true {
		t.Fatal("unsettled activation emitted another action or completion", out)
	}
}

func (f *activationPolicyFixture) replyCode(code uint32) {
	f.base.x.packet(f.t, opcode.SMSGActivatetaxireply, binary.LittleEndian.AppendUint32(nil, code))
	f.refresh()
}

func TestFlightpathActivationLuaSelectsActualPlannedFirstHopAndKeepsAcceptanceUnsettled(t *testing.T) {
	f := newActivationPolicyFixture(t, 12, 2, 2, 4, 12)
	action := f.untilActivation()
	f.dispatch(action)
	r := f.activation
	if r.SourceNode != 12 || r.DestinationNode != 4 || r.MenuRevision != 2 || r.ExpectedRevision != 0 ||
		!r.AcceptUnknownFare || r.ExpectedCopper != 12345 || r.ExpectedCopperRevision != 7 || r.NpcGuid != f.base.x.npc {
		t.Fatal("Lua sent final journey instead of its selected hop or lost exact wallet/menu intent", r)
	}
	if len(f.base.x.f.packets) != f.basePackets+1 {
		t.Fatal("planning/custody emitted unexpected gameplay packets", f.base.x.f.packets)
	}
	packet := f.base.x.f.packets[len(f.base.x.f.packets)-1]
	if packet.Opcode != opcode.CMSGActivatetaxi || len(packet.Payload) != 16 || binary.LittleEndian.Uint64(packet.Payload) != r.NpcGuid ||
		binary.LittleEndian.Uint32(packet.Payload[8:]) != 12 || binary.LittleEndian.Uint32(packet.Payload[12:]) != 4 {
		t.Fatal("selected single hop was not the actual normal activation packet", packet)
	}
	activationPolicyNoEffect(t, f.step()) // Consume actual RPC return.
	f.replyCode(0)
	activationPolicyNoEffect(t, f.step())
	status := f.input["memory"].(map[string]any)["activation"].(map[string]any)
	if status["phase"] != "accepted_unsettled" || status["pending"] != true || status["activationAccepted"] != true ||
		status["replyKnown"] != true || status["replyCode"] != float64(0) {
		t.Fatal("actual acceptance receipt was not preserved as unsettled", status)
	}
	for _, key := range []string{"liveFareKnown", "liveRouteKnown", "flightStartedKnown", "arrivalKnown"} {
		if status[key] != false {
			t.Fatal("Lua invented a quote, eligibility, start or arrival", status)
		}
	}
	f.base.x.advance(20 * time.Second)
	f.refresh() // Actual native lease expiry still cannot settle activation.
	f.input["now"] = float64(1e12)
	activationPolicyNoEffect(t, f.step())
	if f.activationCall != 1 || len(f.mutations) != 2 || len(f.base.x.f.packets) != f.basePackets+1 {
		t.Fatal("accepted/expired activation replayed or released custody with a STOP", f.activationCall, f.mutations, f.base.x.f.packets)
	}
}

func TestFlightpathActivationLuaUnknownTransportOrReplyCannotReplay(t *testing.T) {
	for _, name := range []string{"unknown_transport", "unknown_reply"} {
		t.Run(name, func(t *testing.T) {
			f := newActivationPolicyFixture(t, 12, 2, 2, 4, 12)
			action := f.untilActivation()
			if name == "unknown_transport" {
				f.base.x.f.failSend = true
			}
			f.dispatch(action)
			activationPolicyNoEffect(t, f.step())
			if name == "unknown_reply" {
				f.replyCode(99)
			}
			activationPolicyNoEffect(t, f.step())
			before := f.base.x.observation(t).Activation
			if before == nil || !before.Pending {
				t.Fatal("fixture did not retain an actual ambiguous activation", before)
			}
			f.base.x.advance(time.Hour)
			f.refresh()
			f.input["now"] = float64(1e12)
			activationPolicyNoEffect(t, f.step())
			if f.activationCall != 1 || len(f.mutations) != 2 || !proto.Equal(before, f.base.x.observation(t).Activation) {
				t.Fatal("time, lease expiry or unrecognized reply cleared/replayed activation", before, f.mutations)
			}
		})
	}
}

func activationPolicyPending(t *testing.T, action map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(action["params"])
	if err != nil {
		t.Fatal(err)
	}
	r := &actionsv1.FlightpathActivationRequest{}
	if err := protojson.Unmarshal(b, r); err != nil {
		t.Fatal(err)
	}
	return map[string]any{"id": action["id"], "method": action["method"], "effect": "mutation", "outcome": "pending",
		"params": flightpathPlanJSON(t, r), "error": "host reply unavailable"}
}

func TestFlightpathActivationLuaOnlyExactNotDispatchedResolvesLostHostReply(t *testing.T) {
	for _, name := range []string{"exact", "wrong_wallet_request", "wrong_counter", "wrong_destination", "contradictory_history", "extra_request_field"} {
		t.Run(name, func(t *testing.T) {
			f := newActivationPolicyFixture(t, 12, 2, 2, 4, 12)
			action := f.untilActivation()
			f.beforeActivate = func(*actionsv1.FlightpathActivationRequest) {
				f.base.x.f.world.SelfCopper--
				f.base.x.f.world.SelfCopperRevision++
			}
			f.dispatch(action)
			op := f.base.x.observation(t).Activation
			if op == nil || op.Pending || op.Phase != "not_dispatched" || !op.MoneyBeforeKnown || op.MoneyBeforeCopper == f.activation.ExpectedCopper ||
				op.TransportWritten || op.TransportUnknown || len(f.base.x.f.packets) != f.basePackets {
				t.Fatal("fixture did not reserve then refuse the actual changed wallet", op, f.activation, f.base.x.f.packets)
			}
			row := activationPolicyPending(t, action)
			f.input["last"] = map[string]any{"id": action["id"], "method": action["method"], "effect": "mutation", "error": "host reply unavailable"}
			f.input["pending"] = []any{row}
			view := f.input["observations"].(map[string]any)["flightpath"].(map[string]any)
			observed := view["activation"].(map[string]any)
			switch name {
			case "wrong_wallet_request":
				row["params"].(map[string]any)["expectedCopper"] = float64(1)
			case "wrong_counter":
				row["params"].(map[string]any)["expectedRevision"] = "1"
			case "wrong_destination":
				observed["destinationNode"] = float64(2)
			case "contradictory_history":
				observed["transportUnknown"] = true
			case "extra_request_field":
				row["params"].(map[string]any)["unrecognized"] = true
			}
			out := f.step()
			if name != "exact" {
				activationPolicyNoEffect(t, out)
				return
			}
			resolution, ok := out["resolve"].(map[string]any)
			if !ok || resolution["id"] != action["id"] || resolution["observation"] != "flightpath" || resolution["path"] != "activation" || out["action"] != nil || out["done"] == true {
				t.Fatal("exact typed zero-send receipt failed to reconcile only its original reservation", out)
			}
			f.input["pending"] = []any{}
			release := f.step()["action"].(map[string]any)
			if release["method"] != "buddy-actions.release_interaction" {
				t.Fatal("zero-send refusal retried activation instead of cleanup", release)
			}
			f.dispatch(release)
			out = f.step()
			if out["done"] != true || f.activationCall != 1 || len(f.base.x.f.packets) != f.basePackets {
				t.Fatal("zero-send cleanup replayed activation or failed to finish", out, f.mutations, f.base.x.f.packets)
			}
		})
	}
}

func TestFlightpathActivationLuaKnownServerRejectionCleansUpWithoutRetry(t *testing.T) {
	for _, lostReply := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal_rpc", true: "lost_rpc"}[lostReply], func(t *testing.T) {
			f := newActivationPolicyFixture(t, 12, 2, 2, 4, 12)
			action := f.untilActivation()
			f.dispatch(action)
			if lostReply {
				f.input["pending"] = []any{activationPolicyPending(t, action)}
				f.input["last"] = map[string]any{"id": action["id"], "method": action["method"], "effect": "mutation", "error": "host reply lost"}
			} else {
				activationPolicyNoEffect(t, f.step())
			}
			f.replyCode(12)
			out := f.step()
			if lostReply {
				resolution, ok := out["resolve"].(map[string]any)
				if !ok || resolution["id"] != action["id"] || resolution["path"] != "activation" || out["action"] != nil {
					t.Fatal("exact known rejection failed to reconcile only its lost RPC", out)
				}
				f.input["pending"] = []any{}
				out = f.step()
			}
			release := out["action"].(map[string]any)
			if release["method"] != "buddy-actions.release_interaction" {
				t.Fatal("known server rejection did not select own-token cleanup", out)
			}
			f.dispatch(release)
			out = f.step()
			if out["done"] != true || f.activationCall != 1 || !reflect.DeepEqual(f.mutations, []string{"buddy-actions.acquire_interaction", "buddy-actions.activate_flightpath", "buddy-actions.release_interaction"}) {
				t.Fatal("server rejection became flight completion or automatic retry", out, f.mutations)
			}
			result := out["memory"].(map[string]any)["activation"].(map[string]any)
			if result["phase"] != "server_rejected" || result["arrivalKnown"] != false || result["flightStartedKnown"] != false {
				t.Fatal("server rejection fabricated flight completion", result)
			}
		})
	}
}

func TestFlightpathActivationLuaExplicitFalseAndUnknownWalletIssueNoMutation(t *testing.T) {
	for _, name := range []string{"explicit_false", "wallet_unknown", "wallet_revision_unknown"} {
		t.Run(name, func(t *testing.T) {
			f := newActivationPolicyFixture(t, 12, 2, 2, 4, 12)
			world := f.input["observations"].(map[string]any)["world"].(map[string]any)
			if name == "explicit_false" {
				f.input["goal"].(map[string]any)["params"].(map[string]any)["acceptUnknownFare"] = false
			} else if name == "wallet_unknown" {
				delete(world, "selfCopperKnown")
			} else {
				delete(world, "selfCopperRevision")
			}
			for tick := 0; tick < 30; tick++ {
				out := f.step()
				if action, ok := out["action"].(map[string]any); ok {
					if action["method"] != "buddy-actions.flightpath_reference" {
						t.Fatal("unknown fare preference or wallet absence issued a mutation", name, action)
					}
					f.dispatch(action)
				}
				if out["done"] == true || out["resolve"] != nil {
					t.Fatal("missing activation authority completed or resolved a mutation", out)
				}
			}
			if len(f.mutations) != 0 || len(f.base.x.f.packets) != f.basePackets {
				t.Fatal("preflight await had native side effects", f.mutations)
			}
		})
	}
}

func TestFlightpathActivationLuaFullMaskCompositionKeepsDefaultValueLimits(t *testing.T) {
	known := make([]uint32, 448)
	for i := range known {
		known[i] = uint32(i + 1)
	}
	f := newActivationPolicyFixture(t, 12, 2, known...)
	action := f.untilActivation()
	f.dispatch(action)
	activationPolicyNoEffect(t, f.step())
	activationPolicyNoEffect(t, f.step())
	if f.activation.DestinationNode != 4 || behavior.MaxJSONBytes != 32768 || behavior.MaxInputNodes != 4096 || behavior.MaxNodes != 2048 {
		t.Fatal("full-mask composition changed limits or selected a wrong hop", f.activation)
	}
}

func TestFlightpathActivationLuaSameSessionFareEditAndUnknownReservationSurviveCAS(t *testing.T) {
	f := newActivationPolicyFixture(t, 12, 2, 2, 4, 12)
	denied := strings.Replace(f.source, "accept_unknown_fare=true", "accept_unknown_fare=false", 1)
	if denied == f.source {
		t.Fatal("fixture did not edit the actual Lua unknown-fare choice")
	}
	wrap := activationPolicySessionSource
	s, err := behavior.NewSessionWithOptions(context.Background(), f.base.x.s.sessionID, wrap(denied), behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	decide := func() behavior.Decision {
		t.Helper()
		d, err := s.Decide(context.Background(), f.input)
		if err != nil {
			t.Fatal("actual composed persistent Lua failed", err, s.Status())
		}
		return d
	}
	waiting := false
	for tick := 0; tick < 30; tick++ {
		out := decide().Output
		if action, ok := out["action"].(map[string]any); ok {
			if action["method"] != "buddy-actions.flightpath_reference" {
				t.Fatal("disabled unknown-fare tuning issued a mutation", action)
			}
			f.dispatch(action)
		}
		if out["phase"] == "unknown_fare_not_accepted" {
			waiting = true
			break
		}
	}
	if !waiting || len(f.mutations) != 0 {
		t.Fatal("fixture did not prove the actual Lua fare refusal")
	}
	before, err := s.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reload(context.Background(), wrap(f.source), before.Status.Revision); err != nil {
		t.Fatal(err)
	}
	after, err := s.State(context.Background())
	if err != nil || !reflect.DeepEqual(before.Checkpoint, after.Checkpoint) || s.Status().SessionID != f.base.x.s.sessionID {
		t.Fatal("fare CAS changed body or lost the bound child plan", before, after, err)
	}
	acquireDecision := decide()
	acquire := acquireDecision.Output["action"].(map[string]any)
	if acquire["method"] != "buddy-actions.acquire_interaction" {
		t.Fatal("same-session Lua fare edit did not change the next choice", acquire)
	}
	acquireID, err := s.ReserveAction(acquireDecision)
	if err != nil {
		t.Fatal(err)
	}
	f.dispatch(acquire)
	if _, err := s.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: s.Status().SessionID, ActionID: acquireID, Outcome: behavior.ActionConfirmed}}); err != nil {
		t.Fatal(err)
	}
	activationDecision := decide()
	action := activationDecision.Output["action"].(map[string]any)
	if action["method"] != "buddy-actions.activate_flightpath" {
		t.Fatal("edited Lua did not select the actual activation", action)
	}
	id, err := s.ReserveAction(activationDecision)
	if err != nil {
		t.Fatal(err)
	}
	f.dispatch(action)
	f.input["pending"] = []any{activationPolicyPending(t, action)}
	f.input["last"] = map[string]any{"id": action["id"], "method": action["method"], "effect": "mutation", "error": "host reply lost"}
	if _, err := s.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: s.Status().SessionID, ActionID: id, Outcome: behavior.ActionUnknown}}); err != nil {
		t.Fatal(err)
	}
	activationPolicyNoEffect(t, decide().Output)
	before, err = s.State(context.Background())
	if err != nil || len(before.Pending) != 1 || before.Pending[0].ActionID != id || before.Pending[0].Outcome != behavior.ActionUnknown {
		t.Fatal("actual Session did not retain the uncertain activation reservation", before, err)
	}
	if _, err := s.Reload(context.Background(), wrap(denied), before.Status.Revision); err != nil {
		t.Fatal(err)
	}
	after, err = s.State(context.Background())
	if err != nil || !reflect.DeepEqual(before.Pending, after.Pending) || !reflect.DeepEqual(before.Checkpoint, after.Checkpoint) {
		t.Fatal("source edit cleared waiting mutation or native intent", before, after, err)
	}
	activationPolicyNoEffect(t, decide().Output)
	if _, err := s.ReserveAction(activationDecision); !errors.Is(err, behavior.ErrFenced) || f.activationCall != 1 {
		t.Fatal("old activation ticket survived CAS or was replayed", err, f.activationCall)
	}
}

func TestFlightpathActivationLuaCachedPlanMustMatchCurrentPlannerPolicy(t *testing.T) {
	f := newActivationPolicyFixture(t, 12, 2, 2, 4, 12)
	f.base.baseReads, f.base.baseSends = f.base.x.client.worldReads, f.base.x.client.sendCalls
	oldPlan := f.base.run()
	if !reflect.DeepEqual(oldPlan["nodeIds"], []any{float64(12), float64(4), float64(2)}) {
		t.Fatal("fixture did not cache the original cheaper first hop", oldPlan)
	}
	f.input["memory"] = map[string]any{"plan": oldPlan}
	edited := strings.Replace(f.source, "excluded_path_ids={}", "excluded_path_ids={252}", 1)
	if edited == f.source {
		t.Fatal("fixture did not edit the actual planner exclusion")
	}
	if _, err := f.r.Reload(edited, f.r.Status().Revision); err != nil {
		t.Fatal(err)
	}
	action := f.untilActivation()
	f.dispatch(action)
	plan := f.input["memory"].(map[string]any)["plan"].(map[string]any)
	if f.activation.DestinationNode != 2 || !reflect.DeepEqual(plan["pathIds"], []any{float64(22)}) || plan["referenceBaseCopper"] != float64(330) || plan["policyKey"] == oldPlan["policyKey"] {
		t.Fatal("activation used an old cached hop after the Lua planner edit", f.activation, plan)
	}
}

func TestFlightpathActivationLuaPlannerCASDuringAcquireConsumesOriginalThenCleansUp(t *testing.T) {
	f := newActivationPolicyFixture(t, 12, 2, 2, 4, 12)
	s, err := behavior.NewSessionWithOptions(context.Background(), f.base.x.s.sessionID, activationPolicySessionSource(f.source), behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	decide := func() behavior.Decision {
		t.Helper()
		d, err := s.Decide(context.Background(), f.input)
		if err != nil {
			t.Fatal(err, s.Status())
		}
		return d
	}
	var original behavior.Decision
	var acquire map[string]any
	for tick := 0; tick < 30; tick++ {
		d := decide()
		if action, ok := d.Output["action"].(map[string]any); ok {
			if action["method"] == "buddy-actions.acquire_interaction" {
				original, acquire = d, action
				break
			}
			f.dispatch(action)
		}
	}
	if acquire == nil {
		t.Fatal("fixture did not reach a pending original acquisition")
	}
	id, err := s.ReserveAction(original)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.State(context.Background())
	if err != nil || len(before.Pending) != 1 {
		t.Fatal("actual original acquisition was not reserved", before, err)
	}
	edited := strings.Replace(f.source, "excluded_path_ids={}", "excluded_path_ids={252}", 1)
	if _, err := s.Reload(context.Background(), activationPolicySessionSource(edited), before.Status.Revision); err != nil {
		t.Fatal(err)
	}
	activationPolicyNoEffect(t, decide().Output) // No original acquire reply yet.
	if len(f.mutations) != 0 || f.activationCall != 0 {
		t.Fatal("planner edit replayed acquisition or wrote the old hop", f.mutations)
	}
	f.dispatch(acquire)
	if _, err := s.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: s.Status().SessionID, ActionID: id, Outcome: behavior.ActionConfirmed}}); err != nil {
		t.Fatal(err)
	}
	out := decide().Output
	release, ok := out["action"].(map[string]any)
	if !ok || release["method"] != "buddy-actions.release_interaction" || release["params"].(map[string]any)["token"] == "" {
		t.Fatal("planner edit discarded the original acquisition or failed to clean its exact token", out)
	}
	f.dispatch(release)
	out = decide().Output
	if out["done"] != true || f.activationCall != 0 || len(f.base.x.f.packets) != f.basePackets {
		t.Fatal("policy-change cleanup wrote the stale hop or failed to finish", out, f.mutations)
	}
	result := out["memory"].(map[string]any)["activation"].(map[string]any)
	if result["phase"] != "planner_policy_changed" {
		t.Fatal("task did not retain the reason it abandoned the old hop", result)
	}
}

func TestFlightpathActivationLuaUnownedAbortDoesNotWaitForForeignLease(t *testing.T) {
	f := newActivationPolicyFixture(t, 12, 2, 2, 4, 12)
	out := f.step()
	if action, ok := out["action"].(map[string]any); !ok || action["method"] != "buddy-actions.flightpath_reference" {
		t.Fatal("fixture did not begin its read-only child plan", out)
	}
	observations := f.input["observations"].(map[string]any)
	flight := observations["flightpath"].(map[string]any)
	flight["revision"], flight["operation"].(map[string]any)["revision"] = "3", "3"
	observations["actions"].(map[string]any)["interactionOwner"] = "trade"
	out = f.step()
	if out["done"] != true || out["action"] != nil || out["resolve"] != nil || len(f.mutations) != 0 {
		t.Fatal("task that never owned custody waited for or stopped a foreign lease", out, f.mutations)
	}
}

func TestFlightpathActivationLuaOldScopeConsumesAcquireWithoutReleasingNewBody(t *testing.T) {
	f := newActivationPolicyFixture(t, 12, 2, 2, 4, 12)
	var acquire map[string]any
	for tick := 0; tick < 30; tick++ {
		out := f.step()
		if action, ok := out["action"].(map[string]any); ok {
			if action["method"] == "buddy-actions.acquire_interaction" {
				acquire = action
				break
			}
			f.dispatch(action)
		}
	}
	if acquire == nil {
		t.Fatal("fixture did not preserve an original acquisition call")
	}
	f.input["body"].(map[string]any)["selfGuid"] = "9"
	activationPolicyNoEffect(t, f.step()) // Wait for the original body's call.
	f.dispatch(acquire)                   // Its exact original reply arrives late.
	out := f.step()
	if out["done"] != true || out["action"] != nil || out["resolve"] != nil || f.activationCall != 0 ||
		!reflect.DeepEqual(f.mutations, []string{"buddy-actions.acquire_interaction"}) || len(f.base.x.f.packets) != f.basePackets {
		t.Fatal("late old-body acquisition was dropped, replayed or released into a new body", out, f.mutations)
	}
}
