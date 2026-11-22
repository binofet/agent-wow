package buddyactions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func flightpathPlanSource(t *testing.T) string {
	t.Helper()
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "behaviors", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	return "local tasks=(function()\n" + read("task-stack.lua") + "\nend)()\n" + read("flightpath-plan.lua")
}

func flightpathPlanJSON(t *testing.T, message proto.Message) map[string]any {
	t.Helper()
	b, err := protojson.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(b, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

type flightpathPlanFixture struct {
	t          *testing.T
	x          *flightpathRig
	r          *behavior.Runtime
	input      map[string]any
	source     string
	queries    []uint32
	baseReads  int
	baseSends  int
	baseCopper uint32
}

func newFlightpathPlanFixture(t *testing.T, source, destination uint32, known ...uint32) *flightpathPlanFixture {
	t.Helper()
	x := newFlightpathRig(t)
	// These are synthetic realm packets through the actual native parser and
	// query methods. They are neither a live flight nor an activation receipt.
	x.query(t, "status")
	x.packet(t, opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc, 1))
	x.query(t, "discover")
	x.packet(t, opcode.SMSGShowtaxinodes, flightpathMenuPayload(x.npc, source, known...))
	view := x.observation(t)
	if view.Operation.Pending || !view.Operation.MenuKnown || view.Operation.Phase != "menu_observed" {
		t.Fatal("fixture did not establish an actual parsed menu", view)
	}
	r, err := behavior.NewWithOptions(behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	luaSource := flightpathPlanSource(t)
	if _, err := r.Reload(luaSource, ""); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"goal":   map[string]any{"id": "reference-plan", "kind": "flightpath_plan", "params": map[string]any{"destinationNode": destination}},
		"body":   map[string]any{"sessionId": x.s.sessionID, "selfGuid": strconv.FormatUint(x.s.selfGUID, 10)},
		"memory": map[string]any{}, "pending": []any{}, "now": float64(1),
		"observations": map[string]any{"world": flightpathPlanJSON(t, x.f.world),
			"actions": flightpathPlanJSON(t, state(t, x.s)), "flightpath": flightpathPlanJSON(t, view)},
	}
	return &flightpathPlanFixture{t: t, x: x, r: r, input: input, source: luaSource,
		baseReads: x.client.worldReads, baseSends: x.client.sendCalls, baseCopper: x.f.world.SelfCopper}
}

func (f *flightpathPlanFixture) step() map[string]any {
	f.t.Helper()
	out, err := f.r.Evaluate(f.input)
	if err != nil {
		f.t.Fatalf("actual flightpath Lua: %v; runtime=%+v", err, f.r.Status())
	}
	f.input["memory"] = out["memory"]
	return out
}

func (f *flightpathPlanFixture) reference(action map[string]any) map[string]any {
	f.t.Helper()
	if action["method"] != "buddy-actions.flightpath_reference" {
		f.t.Fatal("reference planner attempted a non-read action", action)
	}
	b, err := json.Marshal(action["params"])
	if err != nil {
		f.t.Fatal(err)
	}
	request := &actionsv1.FlightpathReferenceRequest{}
	if err := protojson.Unmarshal(b, request); err != nil {
		f.t.Fatal("Lua did not select an exact typed reference node", err, action)
	}
	f.queries = append(f.queries, request.NodeId)
	response, err := f.x.s.FlightpathReference(context.Background(), request)
	if err != nil {
		f.t.Fatal(err)
	}
	return flightpathPlanJSON(f.t, response)
}

func (f *flightpathPlanFixture) reply(action, response map[string]any) {
	f.input["last"] = map[string]any{"id": action["id"], "method": action["method"], "effect": "read", "response": response}
}

func (f *flightpathPlanFixture) run() map[string]any {
	f.t.Helper()
	// A fixture assertion bound, not a policy journey limit. The actual
	// Dijkstra search is finite over the witnessed protocol mask.
	for tick := 0; tick < 1000; tick++ {
		out := f.step()
		if out["resolve"] != nil {
			f.t.Fatal("read-only planning tried to resolve a gameplay reservation", out)
		}
		if action, ok := out["action"].(map[string]any); ok {
			f.reply(action, f.reference(action))
		}
		if out["done"] == true {
			f.assertNoNativeEffects()
			return out["memory"].(map[string]any)["plan"].(map[string]any)
		}
	}
	f.t.Fatal("finite reference fixture did not terminate", f.input)
	return nil
}

func (f *flightpathPlanFixture) assertNoNativeEffects() {
	f.t.Helper()
	if f.x.client.worldReads != f.baseReads || f.x.client.sendCalls != f.baseSends || f.x.f.world.SelfCopper != f.baseCopper ||
		f.x.s.state.Traveling || f.x.s.state.Moving {
		f.t.Fatal("reference planning queried World, wrote transport, moved or spent", f.x.client.order, f.x.s.stateLocked())
	}
}

func flightpathPlanNoEffect(t *testing.T, out map[string]any) {
	t.Helper()
	if out["action"] != nil || out["resolve"] != nil || out["done"] == true {
		t.Fatal("unqualified plan produced an effect or completion", out)
	}
}

func TestFlightpathPlanLuaChoosesOnlyKnownReferencePathsAndActualBaseSum(t *testing.T) {
	for _, tc := range []struct {
		name         string
		source, dest uint32
		known        []uint32
		nodes, paths []any
		copper       float64
	}{
		{"cheaper_multihop", 12, 2, []uint32{2, 4, 12}, []any{float64(12), float64(4), float64(2)}, []any{float64(252), float64(7)}, 220},
		{"unvisited_intermediate_excluded", 12, 2, []uint32{2, 12}, []any{float64(12), float64(2)}, []any{float64(22)}, 330},
		{"known_zero_reference_price", 26, 27, []uint32{26, 27}, []any{float64(26), float64(27)}, []any{float64(101)}, 0},
		{"duplicate_zero_tie", 175, 178, []uint32{175, 178}, []any{float64(175), float64(178)}, []any{float64(733)}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFlightpathPlanFixture(t, tc.source, tc.dest, tc.known...)
			plan := f.run()
			if plan["status"] != "planned" || plan["referenceOnly"] != true || plan["liveFareKnown"] != false || plan["liveRouteKnown"] != false ||
				!reflect.DeepEqual(plan["nodeIds"], tc.nodes) || !reflect.DeepEqual(plan["pathIds"], tc.paths) || plan["referenceBaseCopper"] != tc.copper {
				t.Fatal("Lua selected a wrong reference route, sum or live claim", plan)
			}
			binding := plan["source"].(map[string]any)
			if binding["sessionId"] != f.x.s.sessionID || binding["selfGuid"] != "1" || binding["visitId"] != "1" || binding["menuRevision"] != "2" ||
				binding["npcGuid"] != strconv.FormatUint(f.x.npc, 10) || binding["sourceNode"] != float64(tc.source) {
				t.Fatal("reference route lost its actual menu binding", binding)
			}
			info := plan["info"].(map[string]any)
			if info["nameLocale"] != "frFR" || info["nodesSha256"] == nil || info["nodeUrl"] == nil {
				t.Fatal("actual ProtoJSON source provenance was lost or renamed", info)
			}
			for _, key := range []string{"arrived", "activated", "cost", "liveFare", "offered"} {
				if _, exists := plan[key]; exists {
					t.Fatal("reference route invented gameplay facts", plan)
				}
			}
		})
	}
}

func TestFlightpathPlanLuaRetainsRawInvalidRowsWithoutUsingThem(t *testing.T) {
	f := newFlightpathPlanFixture(t, 352, 353, 352, 353)
	plan := f.run()
	if plan["status"] != "unreachable" || plan["nodeIds"] != nil || plan["pathIds"] != nil || !reflect.DeepEqual(f.queries, []uint32{352}) {
		t.Fatal("raw sentinel endpoints became a reference route", plan, f.queries)
	}
	response, err := f.x.s.FlightpathReference(context.Background(), &actionsv1.FlightpathReferenceRequest{NodeId: 352})
	if err != nil || len(response.OutgoingPaths) != 2 || response.OutgoingPaths[0].DestinationNode != ^uint32(0) || response.OutgoingPaths[0].EndpointsKnown {
		t.Fatal("fixture failed to exercise actual unfiltered sentinel rows", response, err)
	}
}

func TestFlightpathPlanLuaRequiresCurrentExactMenuAndKnownDestination(t *testing.T) {
	for _, name := range []string{"no_menu", "pending", "no_send_history", "status_only", "unbound_visit", "body", "visit", "revision", "destination_unknown", "source_unknown"} {
		t.Run(name, func(t *testing.T) {
			f := newFlightpathPlanFixture(t, 12, 2, 2, 4, 12)
			observations := f.input["observations"].(map[string]any)
			flight := observations["flightpath"].(map[string]any)
			op := flight["operation"].(map[string]any)
			switch name {
			case "no_menu":
				delete(op, "menuKnown")
			case "pending":
				op["pending"] = true
			case "no_send_history":
				delete(op, "transportWritten")
			case "status_only":
				op["phase"], op["mode"] = "status_observed", "status"
			case "unbound_visit":
				delete(flight, "visitBound")
			case "body":
				op["selfGuid"] = "9"
			case "visit":
				op["visitId"] = "2"
			case "revision":
				flight["revision"] = "3"
			case "destination_unknown":
				f.input["goal"].(map[string]any)["params"].(map[string]any)["destinationNode"] = float64(19)
			case "source_unknown":
				op["knownNodes"] = []any{float64(2), float64(4)}
			}
			flightpathPlanNoEffect(t, f.step())
			f.assertNoNativeEffects()
		})
	}
}

func TestFlightpathPlanLuaReplacementAbortsInsteadOfReplanning(t *testing.T) {
	for _, name := range []string{"menu_revision", "mask", "npc", "body", "visit", "destination"} {
		t.Run(name, func(t *testing.T) {
			f := newFlightpathPlanFixture(t, 12, 2, 2, 4, 12)
			out := f.step()
			if out["action"] == nil {
				t.Fatal("fixture did not capture the menu before replacement", out)
			}
			observations := f.input["observations"].(map[string]any)
			flight := observations["flightpath"].(map[string]any)
			op := flight["operation"].(map[string]any)
			switch name {
			case "menu_revision":
				flight["revision"], op["revision"] = "3", "3"
			case "mask":
				op["knownNodes"] = []any{float64(2), float64(12)}
			case "npc":
				op["npcGuid"] = "17379390964339851265"
			case "body":
				f.input["body"].(map[string]any)["selfGuid"] = "9"
			case "visit":
				observations["world"].(map[string]any)["visitRevision"] = "2"
			case "destination":
				f.input["goal"].(map[string]any)["params"].(map[string]any)["destinationNode"] = float64(4)
			}
			out = f.step()
			if out["done"] != true || out["action"] != nil || out["resolve"] != nil {
				t.Fatal("replacement started another search or mutation", out)
			}
			plan := out["memory"].(map[string]any)["plan"].(map[string]any)
			if plan["status"] != "aborted" || plan["nodeIds"] != nil || plan["pathIds"] != nil {
				t.Fatal("replaced menu retained a usable reference route", plan)
			}
			f.assertNoNativeEffects()
		})
	}
}

func TestFlightpathPlanLuaReadErrorCanRetryButUnknownMutationStaysReserved(t *testing.T) {
	f := newFlightpathPlanFixture(t, 12, 2, 2, 4, 12)
	first := f.step()["action"].(map[string]any)
	f.input["last"] = map[string]any{"id": first["id"], "method": first["method"], "effect": "read", "error": "read temporarily unavailable"}
	flightpathPlanNoEffect(t, f.step())
	retry := f.step()["action"].(map[string]any)
	if retry["id"] == first["id"] || !reflect.DeepEqual(retry["params"], first["params"]) {
		t.Fatal("descriptor-proven failed read did not retain the same factual lookup", first, retry)
	}
	before, err := json.Marshal(f.input["memory"])
	if err != nil {
		t.Fatal(err)
	}
	f.input["pending"] = []any{map[string]any{"id": "other-task:7", "method": "buddy-actions.query_flightpath", "effect": "mutation", "outcome": "pending", "error": "unknown transport"}}
	f.input["now"] = float64(1e12)
	flightpathPlanNoEffect(t, f.step())
	after, err := json.Marshal(f.input["memory"])
	if err != nil || string(before) != string(after) {
		t.Fatal("unknown mutation or elapsed time cleared task/read custody", err, string(before), string(after))
	}
	f.assertNoNativeEffects()
}

func TestFlightpathPlanLuaFullMaskFitsActualDefaultValueLimits(t *testing.T) {
	known := make([]uint32, 448)
	priorKnown := make([]any, 448)
	for i := range known {
		known[i], priorKnown[i] = uint32(i+1), float64(i+1)
	}
	f := newFlightpathPlanFixture(t, 2, 4, known...)
	// Retain an earlier task's ordinary discovery memory. Only execution has
	// a healthy semantic allowance; Runtime ValueBytes remains the real default.
	f.input["memory"] = map[string]any{"discovery": map[string]any{"knownNodes": priorKnown, "sourceNode": float64(2)}}
	plan := f.run()
	if plan["status"] != "planned" || len(plan["knownNodes"].([]any)) != 448 || plan["referenceBaseCopper"] != float64(110) {
		t.Fatal("full observed mask did not produce the expected default-budget plan", plan)
	}
	if f.input["memory"].(map[string]any)["discovery"] == nil {
		t.Fatal("planner discarded prior discovery memory to fit its budget")
	}
	if behavior.MaxJSONBytes != 32768 || behavior.MaxInputNodes != 4096 || behavior.MaxNodes != 2048 {
		t.Fatal("fixture must exercise the maintained default value limits")
	}
}

func TestFlightpathPlanLuaSameSessionCASPreservesReadAndChangesTieChoice(t *testing.T) {
	f := newFlightpathPlanFixture(t, 175, 178, 14, 175, 178)
	wrapped := func(source string) string {
		return "local planner=(function()\n" + source + `
end)()
local memory={}
return {
 decide=function(frame)
   local value=frame.input;value.memory=memory
   local out=planner.decide(value);memory=out.memory;return out
 end,
 on_event=function() return true end,
 export_state=function() return {version=1,data={memory=memory}} end,
 import_state=function(checkpoint) memory=checkpoint.data.memory;return true end,
}`
	}
	s, err := behavior.NewSessionWithOptions(context.Background(), f.x.s.sessionID, wrapped(f.source), behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	decide := func() map[string]any {
		t.Helper()
		d, err := s.Decide(context.Background(), f.input)
		if err != nil {
			t.Fatal("persistent actual planner Lua failed", err, s.Status())
		}
		return d.Output
	}
	first := decide()["action"].(map[string]any)
	f.reply(first, f.reference(first))
	flightpathPlanNoEffect(t, decide()) // The old policy has relaxed paths now.
	waiting := decide()["action"].(map[string]any)
	if waiting["params"].(map[string]any)["nodeId"] != float64(14) {
		t.Fatal("fixture did not issue the next read after earlier relaxation", waiting)
	}
	before, err := s.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	oldMemory := before.Checkpoint["data"].(map[string]any)["memory"].(map[string]any)
	oldFrame := oldMemory["stack"].([]any)[0].(map[string]any)
	if oldFrame["previousPath"].([]any)[2] != float64(733) {
		t.Fatal("fixture did not contain the old tie decision before editing", oldFrame)
	}
	edited := strings.Replace(f.source, `equal_cost_path="lower"`, `equal_cost_path="higher"`, 1)
	if edited == f.source {
		t.Fatal("fixture did not edit the actual Lua path tie policy")
	}
	revision, err := s.Reload(context.Background(), wrapped(edited), before.Status.Revision)
	if err != nil || revision == before.Status.Revision || s.Status().SessionID != f.x.s.sessionID {
		t.Fatal("same-session current-source CAS failed", revision, err, s.Status())
	}
	if _, err := s.Reload(context.Background(), wrapped(f.source), before.Status.Revision); !errors.Is(err, behavior.ErrRevisionConflict) {
		t.Fatal("stale CAS replaced current reference policy", err)
	}
	after, err := s.State(context.Background())
	if err != nil || !reflect.DeepEqual(before.Checkpoint, after.Checkpoint) || len(after.Pending) != 0 {
		t.Fatal("reload lost the bound search/read or invented a gameplay reservation", before, after, err)
	}
	flightpathPlanNoEffect(t, decide()) // The exact old read has no reply yet.
	stillWaiting, err := s.State(context.Background())
	if err != nil || !reflect.DeepEqual(before.Checkpoint, stillWaiting.Checkpoint) {
		t.Fatal("tuning change dropped the outstanding old read or frontier early", before, stillWaiting, err)
	}
	f.reply(waiting, f.reference(waiting))
	restarted := decide()
	flightpathPlanNoEffect(t, restarted)
	if restarted["phase"] != "flightpath_policy_changed_restarting" {
		t.Fatal("edited tuning used old decisions after consuming the old read", restarted)
	}
	restartedMemory := restarted["memory"].(map[string]any)
	restartedFrame := restartedMemory["stack"].([]any)[0].(map[string]any)
	if restartedMemory["serial"] != float64(2) || restartedFrame["previousPath"].([]any)[2] != float64(0) {
		t.Fatal("restart replayed the old call or retained its old tie decision", restartedMemory)
	}
	ids := map[any]bool{first["id"]: true, waiting["id"]: true}
	for tick := 0; tick < 10; tick++ {
		out := decide()
		if out["resolve"] != nil {
			t.Fatal("read-only CAS planner resolved unrelated action custody", out)
		}
		if action, ok := out["action"].(map[string]any); ok {
			if ids[action["id"]] {
				t.Fatal("restarted search replayed an old read ID", action)
			}
			ids[action["id"]] = true
			f.reply(action, f.reference(action))
		}
		if out["done"] == true {
			plan := out["memory"].(map[string]any)["plan"].(map[string]any)
			if plan["status"] != "planned" || !reflect.DeepEqual(plan["pathIds"], []any{float64(793)}) || plan["referenceBaseCopper"] != float64(0) ||
				!strings.Contains(plan["policyKey"].(string), "|higher|") || !reflect.DeepEqual(f.queries, []uint32{175, 14, 175, 14, 178}) {
				t.Fatal("edited actual Lua did not change the tied real reference choice", plan)
			}
			f.assertNoNativeEffects()
			return
		}
	}
	t.Fatal("edited finite reference search did not finish")
}

func TestFlightpathPlanLuaDeclaredExclusionsAndCompletedTaskLifecycle(t *testing.T) {
	f := newFlightpathPlanFixture(t, 175, 178, 175, 178)
	edited := strings.Replace(f.source, "excluded_path_ids={}", "excluded_path_ids={733}", 1)
	if edited == f.source {
		t.Fatal("fixture did not edit the declared Lua path exclusions")
	}
	if _, err := f.r.Reload(edited, f.r.Status().Revision); err != nil {
		t.Fatal(err)
	}
	plan := f.run()
	if !reflect.DeepEqual(plan["pathIds"], []any{float64(793)}) || plan["referenceBaseCopper"] != float64(0) || !strings.HasSuffix(plan["policyKey"].(string), "733,") {
		t.Fatal("declared Lua exclusion was not applied to the real reference rows", plan)
	}
	if _, err := f.r.Reload(f.source, f.r.Status().Revision); err != nil {
		t.Fatal(err)
	}
	out := f.step()
	if out["done"] != true || out["action"] != nil || !reflect.DeepEqual(out["memory"].(map[string]any)["plan"], plan) {
		t.Fatal("a completed task was silently restarted by a tuning edit", out)
	}
	f.assertNoNativeEffects()
}

func TestFlightpathPlanLuaCompletedPlanIsBoundToItsActualMenuRevision(t *testing.T) {
	f := newFlightpathPlanFixture(t, 12, 2, 2, 4, 12)
	if plan := f.run(); plan["status"] != "planned" {
		t.Fatal("fixture did not finish its original reference plan", plan)
	}
	// An explicit new synthetic realm menu has the same IDs but a genuinely
	// newer native operation. It cannot refresh the old completed task's plan.
	f.x.query(t, "discover")
	f.x.packet(t, opcode.SMSGShowtaxinodes, flightpathMenuPayload(f.x.npc, 12, 2, 4, 12))
	f.input["observations"].(map[string]any)["flightpath"] = flightpathPlanJSON(t, f.x.observation(t))
	f.baseReads, f.baseSends = f.x.client.worldReads, f.x.client.sendCalls
	out := f.step()
	if out["done"] != true || out["action"] != nil || out["resolve"] != nil {
		t.Fatal("new menu silently restarted a completed task", out)
	}
	plan := out["memory"].(map[string]any)["plan"].(map[string]any)
	if plan["status"] != "aborted" || plan["nodeIds"] != nil || plan["pathIds"] != nil || plan["source"].(map[string]any)["menuRevision"] != "2" {
		t.Fatal("new native menu retained or rebound an old reference route", plan)
	}
	f.assertNoNativeEffects()
}
