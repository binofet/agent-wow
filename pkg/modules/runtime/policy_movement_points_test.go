package modrt

import (
	"context"
	"encoding/json"
	"google.golang.org/grpc/health/grpc_health_v1"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"
)

// An in-flight native component may be supplied read-only while the two
// reviewed halves are composed. Normal integrated tests use their own tree.
func movementPointsRegistry(t *testing.T) *moddisc.Registry {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"buddy-actions", "buddy-world", "buddy-party"} {
		source := filepath.Join("..", "..", "..", "modules", name)
		if name == "buddy-actions" && os.Getenv("BUDDY_MOVEMENT_POINTS_NATIVE_SOURCE") != "" {
			source = filepath.Join(os.Getenv("BUDDY_MOVEMENT_POINTS_NATIVE_SOURCE"), "modules", name)
		}
		target := filepath.Join(root, name)
		if err := os.Mkdir(target, 0700); err != nil {
			t.Fatal(err)
		}
		for _, file := range []string{"module.yaml", "module.pb", "compose.yaml"} {
			raw, err := os.ReadFile(filepath.Join(source, file))
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(target, file), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	registry, err := moddisc.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := registry.Lookup(actionsModule)
	if _, ok := definition.RPC("set_movement_points"); !ok {
		t.Fatal("combined native movement-points component is required in the tested tree")
	}
	return registry
}

const movementPointsTestSource = `local s={events=0};return {
 movement_points=function()return {}end,
 on_event=function(e)if e.kind=="encounter_observation"then s.events=s.events+1;s.obs=e.value end;return true end,
 decide=function(f)
  if f.input.kind=="movement_points" then
   local o=s.obs;local points={}
   if o.worldObservationRevision~="2"then points[1]={x=s.events,y=0,z=0,radiusYards=5,expiresAtUnixMs=f.clock.unixMs+100}end
   return {encounterOwnership={visitId=o.visitId,observationSequence=f.input.observationSequence},movementPoints={sessionId=f.session_id,selfGuid="4",mapId=0,visitId=o.visitId,points=points}}
  end
  return {action={kind="test_selected_action"}}
 end,
 export_state=function()return {version=1,data=s}end,
 import_state=function(c)s=c.data;return true end}`

type movementPointsTestRig struct {
	manager      *Manager
	host         *SessionPolicy
	mu           sync.Mutex
	updates      []map[string]any
	reads        uint64
	onRead       func()
	blockCast    chan struct{}
	castEntered  chan struct{}
	castCalls    int
	stepRequests []map[string]any
	setterError  error
	leaseError   error
	stepResponse map[string]any
	callOrder    []string
}

func newMovementPointsRig(t *testing.T, source string) *movementPointsTestRig {
	return newMovementPointsActorRig(t, source, "4")
}
func newMovementPointsActorRig(t *testing.T, source, body string, options ...behavior.SessionOptions) *movementPointsTestRig {
	t.Helper()
	m := New(movementPointsRegistry(t), nil, nil)
	m.started = true
	m.dir = "/synthetic/points"
	id, _ := strconv.ParseUint(body, 10, 64)
	m.session.Identity = modsession.Identity{CharacterGUID: id}
	for _, inst := range m.instances {
		inst.healthy = true
	}
	r := &movementPointsTestRig{manager: m}
	conn, err := grpc.NewClient("passthrough:///synthetic-points", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithUnaryInterceptor(func(ctx context.Context, method string, request, reply any, _ *grpc.ClientConn, _ grpc.UnaryInvoker, _ ...grpc.CallOption) error {
		if method == grpc_health_v1.Health_Check_FullMethodName {
			reply.(*grpc_health_v1.HealthCheckResponse).Status = grpc_health_v1.HealthCheckResponse_SERVING
			return nil
		}
		if method == "/agentwow.policy.encounter.v1.Lease/Renew" {
			r.mu.Lock()
			r.callOrder = append(r.callOrder, "lease")
			err := r.leaseError
			r.mu.Unlock()
			return err
		}
		if method == actionsv1.BuddyActions_CastHeal_FullMethodName {
			r.mu.Lock()
			r.castCalls++
			block, entered := r.blockCast, r.castEntered
			r.mu.Unlock()
			if entered != nil {
				close(entered)
			}
			if block != nil {
				select {
				case <-block:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		switch method {
		case actionsv1.BuddyActions_EncounterObservation_FullMethodName:
			r.reads++
			if r.onRead != nil {
				r.onRead()
			}
			raw, _ := json.Marshal(map[string]any{"sessionId": "points", "selfGuid": body, "visitId": "1", "mapId": 0, "hostRequested": true, "worldObservationRevision": r.reads})
			return protojson.Unmarshal(raw, reply.(proto.Message))

		case "/warcraftbuddy.actions.v1.BuddyActions/StepMovementPoints":
			raw, _ := protojson.Marshal(request.(proto.Message))
			var value map[string]any
			if err := json.Unmarshal(raw, &value); err != nil {
				return err
			}
			r.stepRequests = append(r.stepRequests, value)
			r.callOrder = append(r.callOrder, "step")
			if r.stepResponse != nil {
				definition, _ := m.registry.Lookup(actionsModule)
				method, _ := definition.RPC("step_movement_points")
				decoded := dynamicpb.NewMessage(method.Descriptor().Output())
				raw, err := json.Marshal(r.stepResponse)
				if err != nil {
					return err
				}
				if err = protojson.Unmarshal(raw, decoded); err != nil {
					return err
				}
				wire, err := proto.Marshal(decoded)
				if err != nil {
					return err
				}
				return proto.Unmarshal(wire, reply.(proto.Message))
			}
			return nil
		case "/warcraftbuddy.actions.v1.BuddyActions/SetMovementPoints":
			r.callOrder = append(r.callOrder, "set")
			if r.setterError != nil {
				return r.setterError
			}
			md, _ := metadata.FromOutgoingContext(ctx)
			incoming := metadata.NewIncomingContext(context.Background(), md)
			if modcb.IncomingPolicyEncounterKey(incoming) != strings.Repeat("a", 64) || modcb.IncomingPolicyEncounterVisit(incoming) != 1 {
				t.Error("data setter lost private current encounter identity")
			}
			raw, _ := protojson.Marshal(request.(proto.Message))
			var value map[string]any
			if err := json.Unmarshal(raw, &value); err != nil {
				return err
			}
			r.updates = append(r.updates, value)
			return nil
		default:
			t.Errorf("unexpected RPC %s", method)
			return nil
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	m.instances[actionsModule].conn = conn
	m.instances[actionsModule].launch.Environment = map[string]string{modcb.PolicyEncounterKeyEnv: strings.Repeat("a", 64)}
	var configured behavior.SessionOptions
	if len(options) > 0 {
		configured = options[0]
	}
	host, err := m.StartPolicyWithOptions(context.Background(), source, configured)
	if err != nil {
		t.Fatal(err)
	}
	r.host = host
	t.Cleanup(func() { m.cancel(); host.close(); conn.Close() })
	return r
}

func TestMovementPointsPersistentAdvisoryDuringActualBlockedSend(t *testing.T) {
	r := newMovementPointsRig(t, movementPointsTestSource)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	sends := 0
	go func() {
		_, _, err := r.host.Step(context.Background(), map[string]any{"kind": "encounter"}, func(map[string]any) (PolicyDispatch, error) {
			return PolicyDispatch{Module: actionsModule, Send: func(context.Context) (behavior.ActionOutcome, map[string]any) {
				sends++
				close(entered)
				<-release
				return behavior.ActionUnknown, map[string]any{"phase": "synthetic_unknown_no_replay"}
			}}, nil
		})
		finished <- err
	}()
	<-entered
	defer func() {
		close(release)
		if err := <-finished; err != nil {
			t.Error(err)
		}
	}()
	before, receipts := r.host.vm.Inspect()
	if len(receipts) != 1 {
		t.Fatalf("actual pending reservation missing: %v", receipts)
	}
	for range 3 {
		if err := r.host.stepMovementPointsObservation(context.Background(), "points"); err != nil {
			t.Fatal(err)
		}
	}
	after, pending := r.host.vm.Inspect()
	if before.Revision != after.Revision || before.Epoch != after.Epoch || len(pending) != 1 || pending[0].ActionID != receipts[0].ActionID || sends != 1 {
		t.Fatal("advisory disturbed the entered action", before, after, pending, sends)
	}
	if len(r.updates) != 3 {
		t.Fatal("fresh advisory updates starved behind Send", r.updates)
	}
	if _, ok := r.updates[1]["points"]; ok {
		t.Fatal("empty replacement did not clear the source-expired data", r.updates[1])
	}
	if r.updates[0]["points"].([]any)[0].(map[string]any)["x"] != float64(1) || r.updates[2]["points"].([]any)[0].(map[string]any)["x"] != float64(3) {
		t.Fatal("persistent source/event history was reset", r.updates)
	}
}

func TestMovementPointsReloadFencesOldObservationAndClearsInput(t *testing.T) {
	r := newMovementPointsRig(t, movementPointsTestSource)
	r.host.dispatchDone = make(chan struct{})
	r.host.dispatchAwaiting.Store(true)
	if err := r.host.stepMovementPointsObservation(context.Background(), "points"); err != nil {
		t.Fatal(err)
	}
	r.host.dispatchAwaiting.Store(false)
	r.host.dispatchDone = nil // no actual Send in this source-fence fixture
	revision := r.host.vm.Status().Revision
	source := strings.Replace(movementPointsTestSource, "movement_points=function()return {}end,", "", 1)
	if _, err := r.host.Reload(context.Background(), source, revision); err != nil {
		t.Fatal(err)
	}
	if len(r.updates) != 2 {
		t.Fatal("reload did not clear old points", r.updates)
	}
	if _, ok := r.updates[1]["points"]; ok {
		t.Fatal("reload retained old source geometry")
	}
	if r.host.vm.HasExport(movementPointsHook) {
		t.Fatal("removed hook remained registered")
	}
	reads := r.reads
	if err := r.host.stepMovementPointsObservation(context.Background(), "points"); err != nil || r.reads != reads {
		t.Fatal("legacy source was evaluated as advisory", err)
	}
}

func TestMovementPointsAdvisoryCannotDispatchGameplay(t *testing.T) {
	source := strings.Replace(movementPointsTestSource, `return {encounterOwnership=`, `return {action={kind="forbidden"},encounterOwnership=`, 1)
	r := newMovementPointsRig(t, source)
	r.host.dispatchDone = make(chan struct{})
	r.host.dispatchAwaiting.Store(true)
	defer func() { r.host.dispatchAwaiting.Store(false); r.host.dispatchDone = nil }()
	if err := r.host.stepMovementPointsObservation(context.Background(), "points"); err == nil {
		t.Fatal("advisory accepted a gameplay action")
	}
	if len(r.updates) != 0 || r.host.vm.Status().Pending != 0 {
		t.Fatal("advisory dispatched or reserved", r.updates, r.host.vm.Status())
	}
}

func TestMovementPointsLateSourceReadCannotCrossReload(t *testing.T) {
	r := newMovementPointsRig(t, movementPointsTestSource)
	r.host.dispatchDone = make(chan struct{})
	r.host.dispatchAwaiting.Store(true)
	defer func() { r.host.dispatchAwaiting.Store(false); r.host.dispatchDone = nil }()
	r.onRead = func() {
		r.onRead = nil
		_, err := r.host.Reload(context.Background(), strings.Replace(movementPointsTestSource, "events=0", "events=10", 1), r.host.vm.Status().Revision)
		if err != nil {
			t.Error(err)
		}
	}
	if err := r.host.stepMovementPointsObservation(context.Background(), "points"); err == nil {
		t.Fatal("stale source read was admitted")
	}
	if len(r.updates) != 0 {
		t.Fatal("reloaded source received old geometry", r.updates)
	}
}

func TestMovementPointsDriverRefreshesWhileRealTypedSendIsBlocked(t *testing.T) {
	testMovementPointsDriver(t, "4")
}
func testMovementPointsDriver(t *testing.T, body string) {
	source := strings.Replace(movementPointsTestSource, `return {action={kind="test_selected_action"}}`, `if #f.pending>0 then return {}end;return {action={capability="heal",sessionId=f.session_id,selfGuid="4",targetGuid="3",spellId=2050}}`, 1)
	source = strings.ReplaceAll(source, `selfGuid="4"`, `selfGuid="`+body+`"`)
	r := newMovementPointsActorRig(t, source, body)
	r.blockCast = make(chan struct{})
	r.castEntered = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.host.runObservation(ctx, "points", []policyFeed{{phase: "encounter", method: encounterObservationMethod}})
	}()
	defer func() { cancel(); <-done }()
	select {
	case <-r.castEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("actual typed Send never entered")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.mu.Lock()
		updates, calls := len(r.updates), r.castCalls
		r.mu.Unlock()
		if updates >= 2 {
			if calls != 1 {
				t.Fatal("advisory replayed the active action", calls)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("advisory starved behind blocked typed Send")
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, pending := r.host.vm.Inspect()
	if len(pending) != 1 {
		t.Fatal("entered cast reservation disappeared", pending)
	}
}

func TestMovementPointsFourConcurrentActorInputs(t *testing.T) {
	for _, body := range []string{"1", "4", "5", "8"} {
		t.Run(body, func(t *testing.T) { t.Parallel(); testMovementPointsDriver(t, body) })
	}
}

func TestMovementPointsMissingScopeCannotBecomeMapZeroOrEmptyClear(t *testing.T) {
	r := newMovementPointsRig(t, movementPointsTestSource)
	base := map[string]any{"sessionId": "points", "selfGuid": "4", "visitId": "1", "mapId": float64(0), "points": []any{}}
	for _, kind := range []string{"missing_map", "unknown_map", "missing_points", "unknown_points", "wrong_body", "wrong_session", "unknown_visit"} {
		request := map[string]any{}
		for k, v := range base {
			request[k] = v
		}
		switch kind {
		case "missing_map":
			delete(request, "mapId")
		case "unknown_map":
			request["mapId"] = nil
		case "missing_points":
			delete(request, "points")
		case "unknown_points":
			request["points"] = nil
		case "wrong_body":
			request["selfGuid"] = "5"
		case "wrong_session":
			request["sessionId"] = "other"
		case "unknown_visit":
			request["visitId"] = "0"
		}
		if err := r.host.refreshMovementPoints(context.Background(), map[string]any{"movementPoints": request}); err == nil {
			t.Fatal("unknown scope admitted", kind)
		}
	}
	if len(r.updates) != 0 {
		t.Fatal("invalid inputs reached native setter", r.updates)
	}
	if err := r.host.refreshMovementPoints(context.Background(), map[string]any{"movementPoints": base}); err != nil || len(r.updates) != 1 {
		t.Fatal("explicit known map zero/empty replacement failed", err, r.updates)
	}
}

func TestMovementPointsOldSourceKeepsSynchronousCadenceDuringBlockedSend(t *testing.T) {
	source := strings.Replace(movementPointsTestSource, "movement_points=function()return {}end,", "", 1)
	source = strings.Replace(source, `return {action={kind="test_selected_action"}}`, `return {action={capability="heal",sessionId=f.session_id,selfGuid="4",targetGuid="3",spellId=2050}}`, 1)
	r := newMovementPointsRig(t, source)
	r.blockCast = make(chan struct{})
	r.castEntered = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.host.runObservation(ctx, "points", []policyFeed{{phase: "encounter", method: encounterObservationMethod}})
	}()
	defer func() { cancel(); <-done }()
	select {
	case <-r.castEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("legacy cast did not enter")
	}
	time.Sleep(300 * time.Millisecond)
	r.mu.Lock()
	reads, updates, calls := r.reads, len(r.updates), r.castCalls
	r.mu.Unlock()
	if reads != 1 || updates != 0 || calls != 1 {
		t.Fatal("unregistered source was advanced by advisory work", reads, updates, calls)
	}
}

func TestMovementPointsExplicitHardConstraintsArePreservedNotInvented(t *testing.T) {
	r := newMovementPointsRig(t, movementPointsActionSource("movement_points_step", true))
	action := map[string]any{"capability": "movement_points_step", "sessionId": "points", "selfGuid": "4", "mapId": float64(0), "visitId": "1", "sequenceId": "77",
		"targetConstraints": map[string]any{"sessionId": "points", "selfGuid": "4", "visitId": "1", "targetGuid": "100", "minRangeYards": float64(5), "rangeYards": float64(30)}}
	dispatch, err := r.host.resolveMovementPointsStep(action)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = r.host.Step(context.Background(), nil, func(map[string]any) (PolicyDispatch, error) { return dispatch, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(r.stepRequests) != 1 {
		t.Fatal("step did not traverse the existing dispatch", r.stepRequests)
	}
	constraints := r.stepRequests[0]["targetConstraints"].(map[string]any)
	if constraints["targetGuid"] != "100" || constraints["minRangeYards"] != float64(5) || constraints["rangeYards"] != float64(30) {
		t.Fatal("explicit hard geometry was silently dropped", r.stepRequests)
	}
	action["targetConstraints"].(map[string]any)["selfGuid"] = "5"
	if _, err := r.host.resolveMovementPointsStep(action); err == nil {
		t.Fatal("foreign nested target geometry admitted")
	}
	delete(action, "targetConstraints")
	dispatch, err = r.host.resolveMovementPointsStep(action)
	if err != nil {
		t.Fatal(err)
	}
	_ = dispatch
	if _, present := action["targetConstraints"]; present {
		t.Fatal("ordinary attack preference was invented as a hard constraint")
	}
}

func movementPointsActionSource(cap string, data bool) string {
	point := ``
	if data {
		point = `,movementPoints={sessionId=f.session_id,selfGuid="4",mapId=0,visitId="1",points={}}`
	}
	extra := ``
	if cap == "heal" {
		extra = `,targetGuid="3",spellId=2050`
	}
	return strings.Replace(movementPointsTestSource, `return {action={kind="test_selected_action"}}`, `return {encounterOwnership={visitId="1",observationSequence=f.input.observationSequence},action={capability="`+cap+`",sessionId=f.session_id,selfGuid="4",mapId=0,visitId="1",sequenceId="77"`+extra+`}`+point+`}`, 1)
}
func TestMovementPointsFirstFrameRenewsThenSetsBeforeReservation(t *testing.T) {
	r := newMovementPointsRig(t, movementPointsActionSource("movement_points_step", true))
	r.stepResponse = map[string]any{"sessionId": "points", "selfGuid": "4", "movementPoints": map[string]any{"reason": "already_clear"}}
	ctx := context.WithValue(context.Background(), policyEncounterVisitKey{}, policyEncounterObservation{visit: 1, sequence: "1"})
	_, receipt, err := r.host.Step(ctx, map[string]any{"kind": "encounter", "observationSequence": "1"}, nil)
	if err != nil || receipt == nil || receipt.Outcome != behavior.ActionNotDispatched {
		t.Fatal("known no-op became an unknown movement", receipt, err)
	}
	if strings.Join(r.callOrder, ",") != "lease,set,step" {
		t.Fatal("first step used missing/stale geometry", r.callOrder)
	}
}
func TestMovementPointsMissingOrFailedSetNeverEntersEscapeButAllowsUrgentHeal(t *testing.T) {
	for _, kind := range []string{"missing", "failed", "urgent_heal"} {
		t.Run(kind, func(t *testing.T) {
			cap := "movement_points_step"
			if kind == "urgent_heal" {
				cap = "heal"
			}
			r := newMovementPointsRig(t, movementPointsActionSource(cap, kind != "missing"))
			if kind != "missing" {
				r.setterError = status.Error(codes.Unavailable, "synthetic setter unavailable")
			}
			_, receipt, err := r.host.Step(context.Background(), map[string]any{"kind": "encounter"}, nil)
			if kind == "urgent_heal" {
				if err != nil || receipt == nil || r.castCalls != 1 {
					t.Fatal("optional data failure starved urgent heal", receipt, err, r.castCalls)
				}
			} else if err == nil || receipt != nil || r.host.vm.Status().Pending != 0 || len(r.stepRequests) != 0 {
				t.Fatal("escape consumed stale input or reserved an action", receipt, err, r.stepRequests, r.host.vm.Status())
			}
		})
	}
}
func TestMovementPointsNoOpProofNeverUsesStaleBodyOrLosesUnknownFlag(t *testing.T) {
	for _, kind := range []string{"stale_prior", "clearance_unknown", "clearance_unknown_motion", "current_written", "current_unconfirmed", "motion_unknown", "wrong_body"} {
		t.Run(kind, func(t *testing.T) {
			r := newMovementPointsRig(t, movementPointsActionSource("movement_points_step", true))
			response := map[string]any{"sessionId": "points", "selfGuid": "4", "movementPoints": map[string]any{"reason": "protected_operation_pending"}}
			if strings.HasPrefix(kind, "clearance_unknown") {
				response["movementPoints"] = map[string]any{"reason": "observed_effect_clearance_unknown"}
			}
			switch kind {
			case "clearance_unknown_motion":
				response["motionUnconfirmed"] = true
			case "stale_prior":
				response["bodyMovement"] = map[string]any{"sequenceId": "3", "visitId": "1", "transportWritten": true}
			case "current_written":
				response["bodyMovement"] = map[string]any{"sequenceId": "77", "visitId": "1", "transportWritten": true}
			case "current_unconfirmed":
				response["bodyMovement"] = map[string]any{"sequenceId": "77", "visitId": "1", "phase": "body_input_unconfirmed"}
			case "motion_unknown":
				response["motionUnconfirmed"] = true
			case "wrong_body":
				response["selfGuid"] = "5"
			}
			r.stepResponse = response
			_, receipt, err := r.host.Step(context.Background(), map[string]any{"kind": "encounter"}, nil)
			if err != nil || receipt == nil {
				t.Fatal(receipt, err)
			}
			if kind == "stale_prior" || kind == "clearance_unknown" {
				if receipt.Outcome != behavior.ActionNotDispatched || receipt.Payload["reconciliationRequired"] == true {
					t.Fatal("unrelated old body row was attributed to this invocation", receipt)
				}
			} else if receipt.Outcome == behavior.ActionNotDispatched || receipt.Payload["reconciliationRequired"] != true || receipt.Payload["effect_observed"] != false {
				t.Fatal("uncertainty was dropped or guessed not-dispatched", receipt)
			}
		})
	}
}
