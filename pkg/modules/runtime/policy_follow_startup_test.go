package modrt

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestPolicyStartupFollowUsesCurrentDefaultAndTemporaryWait(t *testing.T) {
	const sid = "startup-follow-policy"
	const key = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	const enabled = "M.follow_owner_by_default = true"
	ctx := context.Background()
	source := followFallbackBundle(t)
	disabled := strings.Replace(source, enabled, "M.follow_owner_by_default = false", 1)
	if disabled == source {
		t.Fatal("complete bundle did not expose the editable startup follow default")
	}
	manager := New(packagedPolicySpellRegistry(t), nil, nil)
	manager.started, manager.dir = true, "/private/"+sid
	manager.session.Identity = modsession.Identity{CharacterGUID: 4}
	for _, instance := range manager.instances {
		instance.healthy = true
	}
	t.Cleanup(manager.cancel)
	var goals []*actionsv1.FollowFormationRequest
	conn, err := grpc.NewClient("passthrough:///startup-follow", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, request, reply any, _ *grpc.ClientConn, _ grpc.UnaryInvoker, _ ...grpc.CallOption) error {
			md, _ := metadata.FromOutgoingContext(ctx)
			incoming := metadata.NewIncomingContext(context.Background(), md)
			deadline, finite := ctx.Deadline()
			if modcb.IncomingPolicyEncounterKey(incoming) != key || !finite || time.Until(deadline) <= 0 ||
				time.Until(deadline) > policyObservationInterval {
				t.Fatal("startup follow lost its private finite host scope", method)
			}
			switch method {
			case "/agentwow.policy.encounter.v1.Lease/Renew":
				if request.(*wrapperspb.UInt64Value).Value != 7 {
					t.Fatal("startup follow renewed an unrelated visit", request)
				}
			case actionsv1.BuddyActions_SetFollowFormation_FullMethodName:
				if modcb.IncomingPolicyEncounterVisit(incoming) != 7 {
					t.Fatal("startup follow lost its current native visit witness")
				}
				goals = append(goals, proto.Clone(request.(*actionsv1.FollowFormationRequest)).(*actionsv1.FollowFormationRequest))
			default:
				t.Fatal("startup default used an unexpected capability", method)
			}
			proto.Merge(reply.(proto.Message), &emptypb.Empty{})
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	manager.instances[actionsModule].conn = conn
	manager.instances[actionsModule].launch.Environment = map[string]string{modcb.PolicyEncounterKeyEnv: key}
	host, err := manager.StartPolicy(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.close)
	revision := host.vm.Status().Revision
	player := func(guid uint64, x, y float32) *actionsv1.EncounterPlayer {
		return &actionsv1.EncounterPlayer{Guid: guid, Present: true, AliveKnown: true, Alive: proto.Bool(true),
			MapKnown: true, MapId: proto.Uint32(0), PositionKnown: true, PositionRevision: 11,
			X: x, Y: y, Z: 3, ClassKnown: true, ClassId: 8, MovementKnown: true, HealthKnown: true, Health: 100, MaxHealth: 100}
	}
	self, owner := player(4, 0, 0), player(3, 10, 20)
	obs := &actionsv1.EncounterObservationResponse{SessionId: sid, SelfGuid: 4, VisitId: 7, MapId: proto.Uint32(0),
		HostOwned: proto.Bool(true), WorldObservationRevision: 1, SpellbookKnown: true,
		Party: &actionsv1.EncounterParty{Group: &partyv1.Group{Known: true, InGroup: true, GroupGuid: 1, LeaderGuid: 3,
			Members: []*partyv1.Member{{Guid: 4, Self: true, Online: true}, {Guid: 3, Online: true}}}},
		Players: map[string]*actionsv1.EncounterPlayer{"4": self, "3": owner},
		Spells:  map[string]*actionsv1.EncounterSpell{"133": {Known: true, Capability: "offense"}},
		Navigation: &actionsv1.NavigationObservation{Following: false, FollowRevision: 0,
			FollowFormationSupported: true, GroundNavigationConfigured: true, FollowOwner: owner}}
	var sequence string
	step := func(resolve func(map[string]any) (PolicyDispatch, error)) (map[string]any, *behavior.ActionReceipt) {
		t.Helper()
		raw, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(obs)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		native := value["navigation"].(map[string]any)
		if native["followRevision"] != strconv.FormatUint(obs.Navigation.FollowRevision, 10) {
			t.Fatal("observed startup counter lost its exact decimal form", native)
		}
		seq, err := host.Enqueue(behavior.Event{Kind: "encounter_observation", Value: value})
		if err != nil {
			t.Fatal(err)
		}
		sequence = strconv.FormatUint(seq, 10)
		call := context.WithValue(ctx, policyEncounterVisitKey{}, policyEncounterObservation{visit: 7, sequence: sequence})
		out, receipt, err := host.Step(call, map[string]any{"kind": "encounter", "observationSequence": sequence}, resolve)
		if err != nil {
			t.Fatal(err)
		}
		return out, receipt
	}
	checkGoal := func(want bool) map[string]any {
		t.Helper()
		before := len(goals)
		out, receipt := step(nil)
		expected := before
		if want {
			expected++
		}
		if receipt != nil || out["action"] != nil || (out["followFormation"] != nil) != want ||
			len(goals) != expected {
			t.Fatal("wrong finite startup follow decision", want, out, receipt, goals)
		}
		if want {
			got := goals[before]
			if got.SessionId != sid || got.SelfGuid != 4 || got.VisitId != 7 || got.OwnerGuid != 3 ||
				got.OwnerPositionRevision != 11 || got.FollowRevision != obs.Navigation.FollowRevision ||
				math.Abs(float64(got.X)-5.016) > .00001 || math.Abs(float64(got.Y)-20.08) > .00001 || got.Z != 3 {
				t.Fatal("startup default changed its body, counter or observed formation goal", got)
			}
		}
		return out
	}
	reload := func(next string) {
		t.Helper()
		revision, err = host.Reload(ctx, next, revision)
		if err != nil || host.vm.Status().SessionID != sid {
			t.Fatal("startup default edit changed the connected policy session", revision, err)
		}
		raw, err := manager.InvokeJSON(ctx, "session.policy_source", json.RawMessage(`{"sessionId":"startup-follow-policy","characterGuid":"4"}`))
		if err != nil {
			t.Fatal(err)
		}
		var view policySourceResponse
		if err := json.Unmarshal(raw, &view); err != nil || view.Revision != revision || view.Source != next ||
			view.FollowFallback.Revision != revision {
			t.Fatal("startup edit inspection lost its active source revision", view.Revision, err)
		}
	}

	startup := checkGoal(true)
	if goals[0].FollowRevision != 0 || host.vm.Status().Pending != 0 || obs.Navigation.Following {
		t.Fatal("startup default armed native following or reserved an action", goals[0], host.vm.Status())
	}
	current := policyEncounterObservation{visit: 7, sequence: sequence}
	goal := startup["followFormation"].(map[string]any)
	valid := map[string]any{}
	for name, value := range goal {
		valid[name] = value
	}
	valid["observationSequence"] = sequence
	beforeParser := len(goals)
	if err := host.refreshFollowFormation(ctx, map[string]any{"followFormation": valid}, current); err != nil ||
		len(goals) != beforeParser+1 || goals[beforeParser].FollowRevision != 0 {
		t.Fatal("canonical zero follow counter failed typed dispatch", err, goals)
	}
	for _, counter := range []any{"", "00", "+0", "-1", "1.0", "18446744073709551616", float64(0)} {
		bad := map[string]any{}
		for name, value := range valid {
			bad[name] = value
		}
		bad["followRevision"] = counter
		if err := host.refreshFollowFormation(ctx, map[string]any{"followFormation": bad}, current); err == nil {
			t.Fatal("malformed follow counter reached native dispatch", counter)
		}
	}
	for _, field := range []string{"selfGuid", "ownerGuid", "visitId", "ownerPositionRevision"} {
		bad := map[string]any{}
		for name, value := range valid {
			bad[name] = value
		}
		bad[field] = "0"
		if err := host.refreshFollowFormation(ctx, map[string]any{"followFormation": bad}, current); err == nil {
			t.Fatal("zero noncounter identity reached native dispatch", field)
		}
	}
	if len(goals) != beforeParser+1 {
		t.Fatal("rejected counter or identity emitted a typed goal", goals)
	}
	reload(disabled)
	checkGoal(false)
	reload(source)
	checkGoal(true)

	// A real uncertain action suppresses independent follow goals and survives
	// the same complete-source edits used to tune the startup default.
	self.Health = 20
	obs.Spells["2050"] = &actionsv1.EncounterSpell{Known: true, Ready: true, Capability: "heal"}
	obs.Healing = &actionsv1.HealingOptionsResponse{SessionId: sid, HostOwned: true,
		Caster:   &actionsv1.HealingCaster{Guid: 4, Ready: true, Mana: proto.Uint32(30)},
		Patients: []*actionsv1.HealingPatient{{Guid: 4, Health: 20, MaxHealth: 100}},
		Spells: []*actionsv1.HealingRank{{SpellId: 2050, Rank: proto.Uint32(1), Level: proto.Uint32(1),
			Cost: proto.Uint32(30), Amount: 45, CastMs: proto.Uint32(1500), MinRange: proto.Float64(0), MaxRange: proto.Float64(40)}}}
	beforeAction := len(goals)
	out, receipt := step(func(action map[string]any) (PolicyDispatch, error) {
		if action["capability"] != "heal" {
			t.Fatal("fixture did not produce the actual self-heal action", action)
		}
		return PolicyDispatch{Module: actionsModule, Send: func(context.Context) (behavior.ActionOutcome, map[string]any) {
			return behavior.ActionUnknown, map[string]any{"capability": "heal"}
		}}, nil
	})
	if receipt == nil || receipt.Outcome != behavior.ActionUnknown || out["followFormation"] != nil || len(goals) != beforeAction {
		t.Fatal("a current action did not suppress startup following", out, receipt, goals)
	}
	actionID := receipt.ActionID
	if err := host.vm.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	beforeReload, err := host.vm.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reload(disabled)
	afterReload, err := host.vm.State(ctx)
	if err != nil || !reflect.DeepEqual(beforeReload.Checkpoint, afterReload.Checkpoint) ||
		!reflect.DeepEqual(beforeReload.Pending, afterReload.Pending) || beforeReload.Status.Cursor != afterReload.Status.Cursor ||
		len(afterReload.Pending) != 1 || afterReload.Pending[0].Outcome != behavior.ActionUnknown {
		t.Fatal("startup source edit lost the checkpoint or uncertain receipt", afterReload, err)
	}
	obs.Healing, self.Health = nil, 100
	checkGoal(false)
	reload(source)
	checkGoal(false)
	if pending := host.vm.Outstanding(); len(pending) != 1 || pending[0].ActionID != actionID || pending[0].Outcome != behavior.ActionUnknown {
		t.Fatal("startup re-enable replaced the uncertain reservation", pending)
	}
	if _, err := host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{
		SessionID: sid, ActionID: actionID, Outcome: behavior.ActionConfirmed,
	}}); err != nil {
		t.Fatal(err)
	}
	checkGoal(true)

	// A native stop advances the observed counter and revokes the old goal.
	// A fresh attached decision can follow again at that current counter.
	obs.Navigation.FollowRevision = 1
	checkGoal(true)
	reload(disabled)
	checkGoal(false)
	reload(source)
	checkGoal(true)

	// Waiting is the existing finite Lua task. It owns movement until its
	// cancellation has released that ownership, while raw Following stays false.
	const taskID, movementOwner = "startup-follow-wait", "task:startup-follow-wait"
	obs.Navigation.OwnerGuid = 3
	obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
	if _, err := host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{
		"task": "wait_for_pull", "id": taskID, "ownerGuid": "3",
	}}); err != nil {
		t.Fatal(err)
	}
	resolveMovement := func(mode string) func(map[string]any) (PolicyDispatch, error) {
		return func(action map[string]any) (PolicyDispatch, error) {
			if action["capability"] != "movement_owner" || action["mode"] != mode || action["owner"] != movementOwner {
				t.Fatal("wait task did not use its existing movement ownership handler", action)
			}
			if mode == "acquire" && action["leaseMs"] != float64(3000) ||
				mode == "release" && action["token"] != "startup-wait-token" {
				t.Fatal("wait task lost its finite lease or granted token", action)
			}
			return PolicyDispatch{Module: actionsModule, Send: func(context.Context) (behavior.ActionOutcome, map[string]any) {
				result := map[string]any{}
				if mode == "acquire" {
					result["token"] = "startup-wait-token"
				}
				return behavior.ActionConfirmed, map[string]any{"capability": "movement_owner", "mode": mode,
					"owner": movementOwner, "result": result}
			}}, nil
		}
	}
	beforeWait := len(goals)
	acquiring, receipt := step(resolveMovement("acquire"))
	if receipt == nil || receipt.Outcome != behavior.ActionConfirmed || acquiring["followFormation"] != nil ||
		len(goals) != beforeWait {
		t.Fatal("task acquisition refreshed a follow goal", acquiring, receipt, goals)
	}
	obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: movementOwner, RemainingMs: 3000, Revision: 1}
	held := checkGoal(false)
	task, _ := held["task"].(map[string]any)
	if task["phase"] != "waiting_for_owner_pull" || obs.Navigation.Following || obs.Navigation.FollowRevision != 1 {
		t.Fatal("temporary wait lost its task phase or changed native follow intent", held)
	}
	if _, err := host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{
		"task": "cancel_task", "id": taskID,
	}}); err != nil {
		t.Fatal(err)
	}
	releasing, receipt := step(resolveMovement("release"))
	if receipt == nil || receipt.Outcome != behavior.ActionConfirmed || releasing["followFormation"] != nil ||
		len(goals) != beforeWait {
		t.Fatal("wait cancellation followed before ownership was released", releasing, receipt, goals)
	}
	obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Revision: 2}
	resumed := checkGoal(true)
	task, _ = resumed["task"].(map[string]any)
	if task["phase"] != "complete" || task["reason"] != "canceled" {
		t.Fatal("follow resumed without completing cancellation cleanup", resumed)
	}

	// Ordinary explicit following remains active while the default is off.
	obs.Navigation.Following, obs.Navigation.FollowRevision = true, 3
	reload(disabled)
	checkGoal(true)

}
