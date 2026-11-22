package buddyactions

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	buddyworld "github.com/agent-wow/agent-wow/modules/buddy-world"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/taxireference"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type settlementPolicyFixture struct {
	f               *activationPolicyFixture
	w               *buddyworld.Service
	scope           *modv1.TransportScope
	destination     taxireference.Node
	destinationNPC  uint64
	session         *behavior.Session
	decision        behavior.Decision
	lost            map[string]bool
	lastReservation uint64
	methods         []string
}

func newSettlementPolicyFixture(t *testing.T, multihop bool) *settlementPolicyFixture {
	t.Helper()
	known := []uint32{2, 12}
	if multihop {
		known = []uint32{2, 4, 12}
	}
	f := newActivationPolicyFixture(t, 12, 2, known...)
	return &settlementPolicyFixture{f: f, lost: map[string]bool{}, destinationNPC: 0xf13000012400002b}
}

func (p *settlementPolicyFixture) step() map[string]any {
	p.f.t.Helper()
	if p.session == nil {
		return p.f.step()
	}
	d, err := p.session.Decide(context.Background(), p.f.input)
	if err != nil {
		p.f.t.Fatal("actual persistent settlement Lua failed", err, p.session.Status())
	}
	p.decision = d
	p.f.input["memory"] = d.Output["memory"]
	return d.Output
}

func (p *settlementPolicyFixture) refresh() {
	p.f.t.Helper()
	if p.w != nil {
		view, err := p.w.Snapshot(context.Background(), nil)
		if err != nil {
			p.f.t.Fatal(err)
		}
		p.f.base.x.f.world = view
	}
	p.f.refresh()
}

func (p *settlementPolicyFixture) until(method string) map[string]any {
	p.f.t.Helper()
	// This bounds a finite test's assertions, not a Lua journey or elapsed time.
	for tick := 0; tick < 1000; tick++ {
		out := p.step()
		if action, ok := out["action"].(map[string]any); ok {
			if action["method"] == method {
				return action
			}
			if action["method"] != "buddy-actions.flightpath_reference" {
				p.f.t.Fatal("unexpected settlement stage action", method, action)
			}
			p.dispatch(action)
		}
		if out["resolve"] != nil || out["done"] == true {
			p.f.t.Fatal("task ended before expected action", method, out)
		}
	}
	p.f.t.Fatal("finite settlement fixture did not reach action", method, p.f.input)
	return nil
}

func (p *settlementPolicyFixture) dispatch(action map[string]any) {
	t := p.f.t
	t.Helper()
	method := action["method"].(string)
	if method == "buddy-actions.flightpath_reference" {
		p.f.dispatch(action)
		return
	}
	p.methods = append(p.methods, method)
	if p.session != nil {
		id, err := p.session.ReserveAction(p.decision)
		if err != nil {
			t.Fatal("actual Session reservation failed", err)
		}
		p.lastReservation = id
	}
	b, err := json.Marshal(action["params"])
	if err != nil {
		t.Fatal(err)
	}
	var request, response proto.Message
	switch method {
	case "buddy-actions.activate_flightpath":
		request = &actionsv1.FlightpathActivationRequest{}
	case "buddy-world.complete_spline":
		request = &worldv1.CompleteSplineRequest{}
	case "buddy-world.retire_spline_completion":
		request = &worldv1.RetireSplineCompletionRequest{}
	case "buddy-actions.query_flightpath":
		request = &actionsv1.FlightpathQueryRequest{}
	case "buddy-actions.settle_flightpath":
		request = &actionsv1.SettleFlightpathRequest{}
	default:
		t.Fatal("settlement policy issued unrelated method", action)
	}
	if err := protojson.Unmarshal(b, request); err != nil {
		t.Fatal("Lua parameters do not match the actual typed API", action, err)
	}
	switch r := request.(type) {
	case *actionsv1.FlightpathActivationRequest:
		p.f.activation = proto.Clone(r).(*actionsv1.FlightpathActivationRequest)
		p.f.activationCall++
		response, err = p.f.base.x.s.ActivateFlightpath(context.Background(), r)
	case *worldv1.CompleteSplineRequest:
		response, err = p.w.CompleteSpline(context.Background(), r)
	case *worldv1.RetireSplineCompletionRequest:
		response, err = p.w.RetireSplineCompletion(context.Background(), r)
	case *actionsv1.FlightpathQueryRequest:
		response, err = p.f.base.x.s.QueryFlightpath(context.Background(), r)
	case *actionsv1.SettleFlightpathRequest:
		response, err = p.f.base.x.s.SettleFlightpath(context.Background(), r)
	}
	if err != nil {
		t.Fatal("actual native settlement action refused its fixture", method, err)
	}
	p.f.input["last"] = map[string]any{"id": action["id"], "method": method, "effect": "mutation", "response": flightpathPlanJSON(t, response)}
	if p.lost[method] {
		params := flightpathPlanJSON(t, request) // Maintained dev/task-runtime stores canonical typed ProtoJSON.
		if method == "buddy-world.complete_spline" && action["params"].(map[string]any)["expectedRevision"] == "0" {
			if _, present := params["expectedRevision"]; present {
				t.Fatal("first completion fixture did not exercise canonical zero omission", params)
			}
		}
		p.f.input["pending"] = []any{map[string]any{"id": action["id"], "method": method, "effect": "mutation", "outcome": "pending", "params": params, "error": "host response lost"}}
		p.f.input["last"] = map[string]any{"id": action["id"], "method": method, "effect": "mutation", "error": "host response lost"}
	}
	if p.session != nil {
		outcome := behavior.ActionSettledUnverified
		if p.lost[method] {
			outcome = behavior.ActionUnknown
		}
		if _, err := p.session.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: p.session.Status().SessionID, ActionID: p.lastReservation, Outcome: outcome}}); err != nil {
			t.Fatal(err)
		}
	}
	p.refresh()
}

func (p *settlementPolicyFixture) accept() {
	t := p.f.t
	t.Helper()
	action := p.until("buddy-actions.activate_flightpath")
	p.dispatch(action)
	if !p.lost["buddy-actions.activate_flightpath"] {
		activationPolicyNoEffect(t, p.step())
	}
	p.scope = &modv1.TransportScope{ConnectionId: "settlement-policy-stream", SelfGuid: 1, VisitBound: true, VisitRevision: 9, MapId: 0, MotionRevision: 10}
	flightpathSettlementPacket(t, p.f.base.x, opcode.SMSGActivatetaxireply, []byte{0, 0, 0, 0}, cloneFlightpathScope(p.scope))
	p.refresh()
}

func settlementPolicyGUID(guid uint64) []byte {
	b := []byte{0}
	for i := uint(0); i < 8; i++ {
		if v := byte(guid >> (8 * i)); v != 0 {
			b[0] |= 1 << i
			b = append(b, v)
		}
	}
	return b
}

func settlementPolicyValues(fields map[int]uint32) []byte {
	keys := make([]int, 0, len(fields))
	high := 0
	for key := range fields {
		keys = append(keys, key)
		if key > high {
			high = key
		}
	}
	sort.Ints(keys)
	b := []byte{byte(high/32 + 1)}
	for block := 0; block <= high/32; block++ {
		mask := uint32(0)
		for _, key := range keys {
			if key/32 == block {
				mask |= 1 << uint(key%32)
			}
		}
		b = binary.LittleEndian.AppendUint32(b, mask)
	}
	for _, key := range keys {
		b = binary.LittleEndian.AppendUint32(b, fields[key])
	}
	return b
}

func settlementPolicyFloats(b []byte, values ...float32) []byte {
	for _, value := range values {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(value))
	}
	return b
}

func (p *settlementPolicyFixture) worldPacket(op uint32, body []byte) {
	t := p.f.t
	t.Helper()
	p.scope.MotionRevision++
	if _, err := p.w.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: op, Payload: body, TransportScope: cloneFlightpathScope(p.scope)}); err != nil {
		t.Fatal(err)
	}
	p.refresh()
}

func (p *settlementPolicyFixture) qualifySpline() {
	t := p.f.t
	t.Helper()
	catalog, err := taxireference.Default()
	if err != nil {
		t.Fatal(err)
	}
	node, ok := catalog.Node(p.f.activation.DestinationNode)
	if !ok || node.MapID != 0 {
		t.Fatal("maintained node reference fixture unavailable", node)
	}
	p.destination = node
	p.w = buddyworld.NewIndependentService(1)
	p.w.ConfigureSession(p.f.base.x.client)
	p.worldPacket(opcode.SMSGLoginVerifyWorld, settlementPolicyFloats(binary.LittleEndian.AppendUint32(nil, 0), 0, 0, 0, 0))
	info, err := movementinfo.Encode(movementinfo.Info{Time: 123})
	if err != nil {
		t.Fatal(err)
	}
	b := binary.LittleEndian.AppendUint32(nil, 1)
	b = append(b, 3, 1, 1, 4, 0x20, 0)
	b = append(b, info...)
	for _, speed := range []float32{2.5, 7, 4.5, 4.7, 2.5, 7, 4.5, 3.14, 3.14} {
		b = settlementPolicyFloats(b, speed)
	}
	b = append(b, settlementPolicyValues(map[int]uint32{0x17: 1 | (5 << 8), 0x18: 50, 0x20: 100, 0x36: 5, 0x3b: 0x100000, 0x492: 12345})...)
	p.worldPacket(opcode.SMSGUpdateObject, b)
	// The destination flightmaster is independently observed in this synthetic
	// packet; the static reference supplies only fixture coordinates.
	b = binary.LittleEndian.AppendUint32(nil, 1)
	b = append(b, 3)
	b = append(b, settlementPolicyGUID(p.destinationNPC)...)
	b = append(b, 3, 0x40, 0)
	b = settlementPolicyFloats(b, float32(node.X)+1, float32(node.Y), float32(node.Z), 0)
	b = append(b, settlementPolicyValues(map[int]uint32{3: 0x124, 0x18: 100, 0x20: 100, 0x52: 0x2000})...)
	p.worldPacket(opcode.SMSGUpdateObject, b)
	b = []byte{1, 1, 0}
	b = settlementPolicyFloats(b, 0, 0, 0)
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = append(b, 0)
	b = binary.LittleEndian.AppendUint32(b, 0x2000)
	b = binary.LittleEndian.AppendUint32(b, 1)
	b = binary.LittleEndian.AppendUint32(b, 2)
	b = settlementPolicyFloats(b, 0, 0, 0, float32(node.X), float32(node.Y), float32(node.Z))
	p.scope.SplineIdKnown, p.scope.SplineActive = true, true
	p.scope.SplineRevision = 41
	p.scope.SplineSourceRevision = p.scope.MotionRevision + 1
	p.scope.SplineSourceOpcode = opcode.SMSGMonsterMove
	p.worldPacket(opcode.SMSGMonsterMove, b)
	// This waits for an actual one-millisecond protocol cadence guard, not for
	// arrival. The assertion deadline is test-only and never enters Lua state.
	deadline := time.Now().Add(time.Second)
	for p.f.base.x.f.world.SelfMotionAuthority.DurationRemainingMs != 0 {
		if time.Now().After(deadline) {
			t.Fatal("actual World ACK cadence failed to become due")
		}
		runtime.Gosched()
		p.refresh()
	}
	if !p.f.base.x.f.world.SelfMotionAuthority.Known {
		t.Fatal("real World parser did not qualify standalone source", p.f.base.x.f.world)
	}
	p.f.base.x.npc = p.destinationNPC
}

func (p *settlementPolicyFixture) ground() {
	t := p.f.t
	t.Helper()
	info, err := movementinfo.Encode(movementinfo.Info{Time: 500, X: float32(p.destination.X), Y: float32(p.destination.Y), Z: float32(p.destination.Z)})
	if err != nil {
		t.Fatal(err)
	}
	p.worldPacket(opcode.MSGMoveHeartbeat, append([]byte{1, 1}, info...))
	b := binary.LittleEndian.AppendUint32(nil, 1)
	b = append(b, 0, 1, 1)
	b = append(b, settlementPolicyValues(map[int]uint32{0x3b: 0})...)
	p.worldPacket(opcode.SMSGUpdateObject, b)
	if !p.f.base.x.f.world.SplineCompletion.RetirementAvailable {
		t.Fatal("actual ground MI and taxi clear did not qualify retirement", p.f.base.x.f.world)
	}
}

func (p *settlementPolicyFixture) arriveMenu(source uint32) {
	p.f.t.Helper()
	flightpathSettlementPacket(p.f.t, p.f.base.x, opcode.SMSGShowtaxinodes, flightpathMenuPayload(p.destinationNPC, source, 2, 4, 12), cloneFlightpathScope(p.scope))
	p.refresh()
}

func TestFlightpathSettlementLuaUsesActualNativeCompletionRetirementAndArrivalMenu(t *testing.T) {
	for _, multihop := range []bool{false, true} {
		t.Run(map[bool]string{false: "final_destination", true: "next_known_hop"}[multihop], func(t *testing.T) {
			p := newSettlementPolicyFixture(t, multihop)
			p.accept()
			activationPolicyNoEffect(t, p.step())
			p.qualifySpline()
			p.dispatch(p.until("buddy-world.complete_spline"))
			for i := 0; i < 3; i++ {
				activationPolicyNoEffect(t, p.step())
			}
			if p.f.base.x.observation(t).Activation.ArrivalKnown {
				t.Fatal("ACK or due duration invented arrival")
			}
			p.ground()
			p.dispatch(p.until("buddy-world.retire_spline_completion"))
			p.dispatch(p.until("buddy-actions.query_flightpath"))
			activationPolicyNoEffect(t, p.step())
			if p.f.base.x.observation(t).Activation.ArrivalKnown {
				t.Fatal("retirement/query write invented destination menu")
			}
			p.arriveMenu(p.f.activation.DestinationNode)
			p.dispatch(p.until("buddy-actions.settle_flightpath"))
			if !multihop {
				out := p.step()
				if out["done"] != true || out["action"] != nil || out["memory"].(map[string]any)["activation"].(map[string]any)["arrivalKnown"] != true {
					t.Fatal("actual final-node settlement did not complete Lua task", out)
				}
			} else {
				action := p.until("buddy-actions.activate_flightpath")
				p.dispatch(action)
				if p.f.activation.SourceNode != 4 || p.f.activation.DestinationNode != 2 || p.f.activation.ExpectedRevision != 1 || p.f.activation.MenuRevision != 3 {
					t.Fatal("continuation did not replan from actual arrival menu", p.f.activation)
				}
			}
			want := []string{"buddy-actions.activate_flightpath", "buddy-world.complete_spline", "buddy-world.retire_spline_completion", "buddy-actions.query_flightpath", "buddy-actions.settle_flightpath"}
			if multihop {
				want = append(want, "buddy-actions.activate_flightpath")
			}
			if !reflect.DeepEqual(p.methods, want) {
				t.Fatal("unexpected mutation sequence/custody action", p.methods, want)
			}
		})
	}
}

func TestFlightpathSettlementLuaCASChoiceAndUnknownCompletionReservation(t *testing.T) {
	p := newSettlementPolicyFixture(t, false)
	source := p.f.source
	disabled := strings.Replace(source, "complete_hops=true", "complete_hops=false", 1)
	if disabled == source {
		t.Fatal("editable completion choice missing")
	}
	s, err := behavior.NewSessionWithOptions(context.Background(), p.f.base.x.s.sessionID, activationPolicySessionSource(disabled), behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p.session = s
	p.accept()
	activationPolicyNoEffect(t, p.step())
	p.qualifySpline()
	for i := 0; i < 3; i++ {
		activationPolicyNoEffect(t, p.step())
	}
	before, err := s.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Reload(context.Background(), activationPolicySessionSource(source), before.Status.Revision); err != nil {
		t.Fatal(err)
	}
	after, err := s.State(context.Background())
	if err != nil || !reflect.DeepEqual(before.Checkpoint, after.Checkpoint) || s.Status().SessionID != p.f.base.x.s.sessionID {
		t.Fatal("CAS changed session or finite task memory", before, after, err)
	}
	action := p.until("buddy-world.complete_spline")
	p.lost[action["method"].(string)] = true
	p.f.base.x.f.failSend = true
	p.dispatch(action)
	if err := s.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err = s.State(context.Background())
	if err != nil || len(before.Pending) != 1 || before.Pending[0].Outcome != behavior.ActionUnknown {
		t.Fatal("actual Session did not preserve unknown host reservation", before, err)
	}
	if _, err = s.Reload(context.Background(), activationPolicySessionSource(disabled), before.Status.Revision); err != nil {
		t.Fatal(err)
	}
	after, err = s.State(context.Background())
	if err != nil || !reflect.DeepEqual(before.Pending, after.Pending) || !reflect.DeepEqual(before.Checkpoint, after.Checkpoint) {
		t.Fatal("reload lost waiting completion or uncertainty", before, after, err)
	}
	out := p.step()
	resolution, ok := out["resolve"].(map[string]any)
	if !ok || resolution["id"] != action["id"] || resolution["observation"] != "world" || resolution["path"] != "splineCompletion.operation" || out["action"] != nil {
		t.Fatal("canonical-zero unknown completion did not reconcile only its host call", out)
	}
	native := p.f.base.x.f.world.SplineCompletion.Operation
	if !native.Pending || !native.TransportUnknown || native.Retired || native.ArrivalKnown {
		t.Fatal("host resolution cleared native uncertain attempt", native)
	}
	if _, err := s.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: s.Status().SessionID, ActionID: p.lastReservation, Outcome: behavior.ActionSettledUnverified}}); err != nil {
		t.Fatal(err)
	}
	p.f.input["pending"] = []any{}
	p.f.base.x.f.failSend = false
	activationPolicyNoEffect(t, p.step())
	p.ground()
	p.dispatch(p.until("buddy-world.retire_spline_completion"))
	if got := p.f.base.x.f.world.SplineCompletion; !got.Operation.Retired || got.Operation.Pending || !got.Operation.TransportUnknown {
		t.Fatal("actual ground retirement lost send uncertainty", got)
	}
	if err := s.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if final, err := s.State(context.Background()); err != nil || len(final.Pending) != 0 {
		t.Fatal("settled host calls remained pending after native retirement", final, err)
	}
	count := 0
	for _, method := range p.methods {
		if method == "buddy-world.complete_spline" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("CAS replayed scoped completion", p.methods)
	}
}

func TestFlightpathSettlementLuaLostWrittenCompletionRequiresExactCanonicalPendingRow(t *testing.T) {
	for _, name := range []string{"exact", "foreign_id", "different_scope", "extra_parameter", "different_receipt"} {
		t.Run(name, func(t *testing.T) {
			p := newSettlementPolicyFixture(t, false)
			p.accept()
			activationPolicyNoEffect(t, p.step())
			p.qualifySpline()
			action := p.until("buddy-world.complete_spline")
			p.lost["buddy-world.complete_spline"] = true
			p.dispatch(action)
			row := p.f.input["pending"].([]any)[0].(map[string]any)
			switch name {
			case "foreign_id":
				row["id"] = "foreign-task:1"
			case "different_scope":
				row["params"].(map[string]any)["transportScope"].(map[string]any)["connectionId"] = "other-stream"
			case "extra_parameter":
				row["params"].(map[string]any)["extra"] = true
			case "different_receipt":
				p.f.input["observations"].(map[string]any)["world"].(map[string]any)["splineCompletion"].(map[string]any)["operation"].(map[string]any)["movementRevision"] = "999"
			}
			out := p.step()
			if name != "exact" {
				activationPolicyNoEffect(t, out)
				return
			}
			resolution, ok := out["resolve"].(map[string]any)
			if !ok || resolution["id"] != action["id"] || resolution["path"] != "splineCompletion.operation" || out["action"] != nil || out["done"] == true {
				t.Fatal("canonical zero-omitted pending row did not resolve exact known write", out)
			}
			op := p.f.base.x.f.world.SplineCompletion.Operation
			if !op.TransportWritten || op.TransportUnknown || op.Retired || op.ArrivalKnown {
				t.Fatal("lost host call invented physical settlement", op)
			}
			p.f.input["pending"] = []any{}
			activationPolicyNoEffect(t, p.step())
			if len(p.methods) != 2 {
				t.Fatal("written completion was replayed", p.methods)
			}
		})
	}
}

func TestFlightpathSettlementLuaElapsedAckDoneAndInferredPoseAreNotArrival(t *testing.T) {
	p := newSettlementPolicyFixture(t, false)
	p.accept()
	activationPolicyNoEffect(t, p.step())
	p.qualifySpline()
	p.dispatch(p.until("buddy-world.complete_spline"))
	activationPolicyNoEffect(t, p.step())
	// These deliberately non-authoritative candidate view edits test what Lua
	// refuses. The actual native World receipt and booking remain unchanged.
	p.f.input["now"] = float64(1e12)
	w := p.f.input["observations"].(map[string]any)["world"].(map[string]any)
	self := w["self"].(map[string]any)
	self["positionKnown"], self["positionInferred"] = true, true
	self["x"], self["y"], self["z"] = p.destination.X, p.destination.Y, p.destination.Z
	w["selfSplineFacts"].(map[string]any)["flags"] = float64(0x2100) // A candidate Done flag is not a receipt.
	for i := 0; i < 3; i++ {
		activationPolicyNoEffect(t, p.step())
	}
	if len(p.methods) != 2 || p.f.base.x.observation(t).Activation.ArrivalKnown || p.f.base.x.f.world.SplineCompletion.Operation.Retired {
		t.Fatal("timer/write/Done/inferred view invented arrival or retirement", p.methods)
	}
}

func TestFlightpathSettlementLuaRetiredInvalidSourceNeverResubmitsRetirement(t *testing.T) {
	p := newSettlementPolicyFixture(t, false)
	p.accept()
	activationPolicyNoEffect(t, p.step())
	p.qualifySpline()
	p.dispatch(p.until("buddy-world.complete_spline"))
	p.ground()
	p.dispatch(p.until("buddy-world.retire_spline_completion"))
	p.worldPacket(opcode.SMSGClientControlUpdate, []byte{1, 1, 1})
	if p.f.base.x.f.world.SplineCompletion.RetirementCurrent {
		t.Fatal("native control supersession failed to withdraw live retirement")
	}
	for i := 0; i < 3; i++ {
		activationPolicyNoEffect(t, p.step())
	}
	if !reflect.DeepEqual(p.methods, []string{"buddy-actions.activate_flightpath", "buddy-world.complete_spline", "buddy-world.retire_spline_completion"}) {
		t.Fatal("invalid retired source retried state CAS or probed arrival", p.methods)
	}
}

func TestFlightpathSettlementLuaOnlyExactAcceptedBookingResolvesLostActivationCall(t *testing.T) {
	for _, name := range []string{"exact", "foreign_pending", "extra_parameter", "different_receipt"} {
		t.Run(name, func(t *testing.T) {
			p := newSettlementPolicyFixture(t, false)
			p.lost["buddy-actions.activate_flightpath"] = true
			p.accept()
			row := p.f.input["pending"].([]any)[0].(map[string]any)
			switch name {
			case "foreign_pending":
				row["id"] = "another-task:99"
			case "extra_parameter":
				row["params"].(map[string]any)["extra"] = true
			case "different_receipt":
				p.f.input["observations"].(map[string]any)["flightpath"].(map[string]any)["activation"].(map[string]any)["destinationNode"] = float64(4)
			}
			out := p.step()
			if name != "exact" {
				activationPolicyNoEffect(t, out)
				return
			}
			resolve, ok := out["resolve"].(map[string]any)
			if !ok || resolve["observation"] != "flightpath" || resolve["path"] != "activation" || out["action"] != nil || out["done"] == true {
				t.Fatal("exact accepted call did not reconcile host receipt", out)
			}
			if op := p.f.base.x.observation(t).Activation; !op.Pending || !op.ActivationAccepted || op.ArrivalKnown || op.AcceptanceScope == nil {
				t.Fatal("host resolution retired physical booking", op)
			}
			p.f.input["pending"] = []any{}
			activationPolicyNoEffect(t, p.step())
			if len(p.methods) != 1 {
				t.Fatal("accepted lost RPC was replayed", p.methods)
			}
		})
	}
}

func TestFlightpathSettlementLuaWrongDestinationMenuCannotSettleOrRebook(t *testing.T) {
	for _, name := range []string{"wrong_node", "foreign_npc", "stale_scope", "changed_visit", "body_visit_replaced"} {
		t.Run(name, func(t *testing.T) {
			p := newSettlementPolicyFixture(t, false)
			p.accept()
			activationPolicyNoEffect(t, p.step())
			p.qualifySpline()
			p.dispatch(p.until("buddy-world.complete_spline"))
			p.ground()
			p.dispatch(p.until("buddy-world.retire_spline_completion"))
			p.dispatch(p.until("buddy-actions.query_flightpath"))
			scope := cloneFlightpathScope(p.scope)
			npc, node := p.destinationNPC, uint32(2)
			switch name {
			case "wrong_node":
				node = 12
			case "foreign_npc":
				npc++
			case "stale_scope":
				scope.MotionRevision--
			case "changed_visit":
				scope.VisitRevision++
			case "body_visit_replaced":
				entry := settlementPolicyFloats(binary.LittleEndian.AppendUint32(nil, 0), float32(p.destination.X), float32(p.destination.Y), float32(p.destination.Z), 0)
				p.scope.VisitRevision++
				p.scope.SplineActive = false
				p.worldPacket(opcode.SMSGNewWorld, entry)
				flightpathSettlementPacket(t, p.f.base.x, opcode.SMSGNewWorld, entry, cloneFlightpathScope(p.scope))
			}
			flightpathSettlementPacket(t, p.f.base.x, opcode.SMSGShowtaxinodes, flightpathMenuPayload(npc, node, 2, 4, 12), scope)
			p.refresh()
			for i := 0; i < 3; i++ {
				activationPolicyNoEffect(t, p.step())
			}
			if len(p.methods) != 4 || p.f.base.x.observation(t).Activation.ArrivalKnown || p.f.activationCall != 1 {
				t.Fatal("unmatched destination menu settled or rebooked", name, p.methods)
			}
		})
	}
}

func TestFlightpathSettlementLuaRequiresAcceptanceScopeAndDefersChangedGoalUntilArrival(t *testing.T) {
	t.Run("missing_acceptance_scope", func(t *testing.T) {
		p := newSettlementPolicyFixture(t, false)
		p.dispatch(p.until("buddy-actions.activate_flightpath"))
		activationPolicyNoEffect(t, p.step())
		p.f.base.x.packet(t, opcode.SMSGActivatetaxireply, []byte{0, 0, 0, 0})
		p.refresh()
		for i := 0; i < 3; i++ {
			activationPolicyNoEffect(t, p.step())
		}
		if op := p.f.base.x.observation(t).Activation; op.AcceptanceScope != nil || !op.Pending || op.ArrivalKnown || len(p.methods) != 1 {
			t.Fatal("unscoped acceptance authorized completion/settlement", op, p.methods)
		}
	})
	t.Run("new_goal_after_paid_hop", func(t *testing.T) {
		p := newSettlementPolicyFixture(t, false)
		p.accept()
		activationPolicyNoEffect(t, p.step())
		paid := proto.Clone(p.f.activation).(*actionsv1.FlightpathActivationRequest)
		p.f.input["goal"].(map[string]any)["params"].(map[string]any)["destinationNode"] = float64(4)
		p.qualifySpline()
		p.dispatch(p.until("buddy-world.complete_spline"))
		p.ground()
		p.dispatch(p.until("buddy-world.retire_spline_completion"))
		p.dispatch(p.until("buddy-actions.query_flightpath"))
		p.arriveMenu(2)
		if !proto.Equal(paid, p.f.activation) {
			t.Fatal("in-flight goal change rewrote paid request", paid, p.f.activation)
		}
		p.dispatch(p.until("buddy-actions.settle_flightpath"))
		next := p.until("buddy-actions.activate_flightpath")
		p.dispatch(next)
		if p.f.activation.SourceNode != 2 || p.f.activation.DestinationNode != 4 || p.f.activation.ExpectedRevision != 1 {
			t.Fatal("old target falsely completed instead of replanning current goal after observed arrival", p.f.activation)
		}
	})
}
