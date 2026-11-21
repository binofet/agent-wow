package modrt

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestPackagedActionsRoutesPortalCatchupContract(t *testing.T) {
	manager, ctx, writes := startPackagedPolicySpell(t)
	// The ordinary public registry must reach the binary's real custody gate,
	// rather than advertising a descriptor method the process cannot route.
	for method, body := range map[string]string{
		"buddy-actions.set_portal_catchup":   `{"sessionId":"offline","selfGuid":"11","visitId":"1","ownerGuid":"3","followRevision":"1","witnessRevision":"1","start":{"revision":"1"},"candidates":[{"x":1}]}`,
		"buddy-actions.set_follow_formation": `{"sessionId":"offline","selfGuid":"11","visitId":"1","ownerGuid":"3","ownerPositionRevision":"1","startDistance":2,"stopDistance":1}`,
	} {
		if _, err := manager.InvokeJSON(ctx, method, []byte(body)); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("%s did not route to the native private-host gate: %v", method, err)
		}
	}
	select {
	case write := <-writes:
		t.Fatalf("goal contract check emitted game traffic: %+v", write)
	default:
	}
}

// The full editable bundle proposes portal geometry through the production
// session host. The native RPC interceptor observes admission and lease scope.
func TestPolicyPortalCatchupReloadsInTheSameSession(t *testing.T) {
	const sid, key = "portal-policy-session", "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const witness = uint64(9007199254740993)
	bundle, err := exec.Command("python3", filepath.Join("..", "..", "..", "dev", "encounter-bundle")).Output()
	if err != nil {
		t.Fatal(err)
	}
	source := string(bundle)
	changed := strings.Replace(source, "local portal_ahead, portal_side = {8,12,16}, {-6,6}",
		"local portal_ahead, portal_side = {8,12,16}, {6,-6}", 1)
	if source == changed {
		t.Fatal("the complete bundle did not contain the editable portal candidate order")
	}
	manager := New(packagedPolicySpellRegistry(t), nil, nil)
	t.Cleanup(manager.cancel)
	vm, err := behavior.NewSession(context.Background(), sid, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vm.Close)
	initialRevision := vm.Status().Revision
	var goals []*actionsv1.PortalCatchupRequest
	var renewals []uint64
	denied := false
	conn, err := grpc.NewClient("passthrough:///portal-fixture", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, request, reply any, _ *grpc.ClientConn, _ grpc.UnaryInvoker, _ ...grpc.CallOption) error {
			md, _ := metadata.FromOutgoingContext(ctx)
			incoming := metadata.NewIncomingContext(context.Background(), md)
			deadline, finite := ctx.Deadline()
			if modcb.IncomingPolicyEncounterKey(incoming) != key || !finite || time.Until(deadline) <= 0 ||
				time.Until(deadline) > policyObservationInterval {
				t.Fatal("portal dispatch lost the private host or finite deadline", method, md)
			}
			switch method {
			case "/agentwow.policy.encounter.v1.Lease/Renew":
				renewals = append(renewals, request.(*wrapperspb.UInt64Value).Value)
			case actionsv1.BuddyActions_SetPortalCatchup_FullMethodName:
				if modcb.IncomingPolicyEncounterVisit(incoming) != 7 {
					t.Fatal("portal dispatch lost its native visit witness", md)
				}
				if denied {
					return status.Error(codes.FailedPrecondition, "portal witness changed")
				}
				goals = append(goals, proto.Clone(request.(*actionsv1.PortalCatchupRequest)).(*actionsv1.PortalCatchupRequest))
			default:
				t.Fatal("unexpected native capability", method)
			}
			proto.Merge(reply.(proto.Message), &emptypb.Empty{})
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	instance := manager.instances[actionsModule]
	instance.conn, instance.healthy = conn, true
	instance.launch.Environment = map[string]string{modcb.PolicyEncounterKeyEnv: key}
	host := &SessionPolicy{vm: vm, manager: manager, body: "4", lifetime: manager.ctx, source: source, builtin: source}

	self := &actionsv1.EncounterPlayer{Guid: 4, Present: true, AliveKnown: true, Alive: proto.Bool(true),
		MapKnown: true, MapId: proto.Uint32(0), PositionKnown: true, PositionRevision: 11,
		Z: 3, ClassKnown: true, ClassId: 5, MovementKnown: true, HealthKnown: true, Health: 100, MaxHealth: 100}
	start := &actionsv1.ClientPosition{Known: true, MapKnown: true, MapId: 0, Z: 3, Revision: 11}
	obs := &actionsv1.EncounterObservationResponse{
		SessionId: sid, SelfGuid: 4, VisitId: 7, MapId: proto.Uint32(0), HostOwned: proto.Bool(true),
		WorldObservationRevision: 1, SpellbookKnown: true,
		Party: &actionsv1.EncounterParty{Group: &partyv1.Group{Known: true, InGroup: true, GroupGuid: 1, LeaderGuid: 3,
			Members: []*partyv1.Member{{Guid: 4, Self: true, Online: true}, {Guid: 3, Online: true}}}},
		Players: map[string]*actionsv1.EncounterPlayer{"4": self},
		Spells:  map[string]*actionsv1.EncounterSpell{"585": {Known: true, Capability: "offense"}},
		Navigation: &actionsv1.NavigationObservation{Following: true, FollowRevision: 3, OwnerGuid: 3, GroundNavigationConfigured: true,
			MovementOwner: &actionsv1.MovementOwnerState{},
			Portal: &actionsv1.PortalFollowObservation{WitnessRevision: witness, SourceVisitId: 7, FollowRevision: 3,
				OwnerGuid: 3, SourceMapId: 0, DestinationMapId: 389, TriggerId: 100,
				Start: start, Ground: &actionsv1.PortalPoint{X: 100, Z: 3}, CatchingUp: true, OwnerAbsent: true,
				SightingAgeKnown: true, SightingAgeMs: 1000, DisappearanceAgeKnown: true, DisappearanceAgeMs: 1000}},
	}
	observe := func() string {
		t.Helper()
		raw, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(obs)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		sequence, err := host.Enqueue(behavior.Event{Kind: "encounter_observation", Value: value})
		if err != nil {
			t.Fatal(err)
		}
		return strconv.FormatUint(sequence, 10)
	}
	step := func(sequence string, resolve func(map[string]any) (PolicyDispatch, error)) (map[string]any, *behavior.ActionReceipt, error) {
		t.Helper()
		ctx := context.WithValue(context.Background(), policyEncounterVisitKey{}, policyEncounterObservation{visit: 7, sequence: sequence})
		return host.Step(ctx, map[string]any{"kind": "encounter", "observationSequence": sequence}, resolve)
	}
	assertGoal := func(index int, side float32) {
		t.Helper()
		if len(goals) != index+1 {
			t.Fatalf("expected %d portal goals, got %+v", index+1, goals)
		}
		got := goals[index]
		if got.SessionId != sid || got.SelfGuid != 4 || got.VisitId != 7 || got.OwnerGuid != 3 ||
			got.FollowRevision != 3 || got.WitnessRevision != witness || !proto.Equal(got.Start, start) ||
			len(got.Candidates) != 7 {
			t.Fatalf("portal goal lost exact identities, witness or start: %v", got)
		}
		want := []*actionsv1.PortalPoint{{X: 100, Z: 3}, {X: 8, Y: side, Z: 3}, {X: 8, Y: -side, Z: 3},
			{X: 12, Y: side, Z: 3}, {X: 12, Y: -side, Z: 3}, {X: 16, Y: side, Z: 3}, {X: 16, Y: -side, Z: 3}}
		for i, candidate := range got.Candidates {
			if !proto.Equal(candidate, want[i]) {
				t.Fatalf("portal candidate %d changed order or geometry: got %v want %v", i, candidate, want[i])
			}
		}
	}

	sequence := observe()
	first, receipt, err := step(sequence, nil)
	if err != nil || receipt != nil || first["action"] != nil || vm.Status().Pending != 0 ||
		len(renewals) != 1 || renewals[0] != 7 {
		t.Fatal("portal geometry did not use the existing finite host ownership", first, receipt, err, renewals, vm.Status())
	}
	assertGoal(0, -6)
	if _, err := host.Reload(context.Background(), "return {", initialRevision); err == nil || vm.Status().Revision != initialRevision {
		t.Fatal("broken Lua replaced the active portal policy", err, vm.Status())
	}
	revision, err := host.Reload(context.Background(), changed, initialRevision)
	if err != nil || revision == initialRevision || vm.Status().SessionID != sid {
		t.Fatal("portal source reload replaced the session or failed to advance", revision, err)
	}
	if _, err := host.Reload(context.Background(), source, initialRevision); err == nil || vm.Status().Revision != revision {
		t.Fatal("stale CAS replaced the reloaded portal policy", err, vm.Status())
	}
	sequence = observe()
	second, receipt, err := step(sequence, nil)
	if err != nil || receipt != nil || second["action"] != nil || len(renewals) != 2 || renewals[1] != 7 {
		t.Fatal("reloaded portal goal created a game action", second, receipt, err, renewals)
	}
	assertGoal(1, 6)
	denied = true
	sequence = observe()
	_, receipt, err = step(sequence, nil)
	if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != "portal witness changed" || receipt != nil || len(goals) != 2 {
		t.Fatal("native portal rejection was hidden or reserved as an action", receipt, err, goals)
	}
	denied = false
	current := policyEncounterObservation{visit: 7, sequence: second["portalCatchup"].(map[string]any)["observationSequence"].(string)}
	goal := second["portalCatchup"].(map[string]any)
	for _, field := range []string{"sessionId", "selfGuid", "visitId", "observationSequence", "ownerGuid", "witnessRevision", "followRevision"} {
		bad := map[string]any{}
		for name, value := range goal {
			bad[name] = value
		}
		bad[field] = "00"
		if err := host.refreshPortalCatchup(context.Background(), map[string]any{"portalCatchup": bad}, current); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("foreign or noncanonical portal field %s reached native code: %v", field, err)
		}
	}
	badStart := map[string]any{}
	for name, value := range goal["start"].(map[string]any) {
		badStart[name] = value
	}
	badStart["revision"] = float64(11)
	bad := map[string]any{}
	for name, value := range goal {
		bad[name] = value
	}
	bad["start"] = badStart
	if err := host.refreshPortalCatchup(context.Background(), map[string]any{"portalCatchup": bad}, current); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("numeric start revision bypassed canonical witness validation: %v", err)
	}
	if len(goals) != 2 {
		t.Fatal("rejected portal identity reached the native setter")
	}
	stale, receipt, err := step("1", nil)
	if err != nil || receipt != nil || stale["portalCatchup"] != nil || len(goals) != 2 {
		t.Fatal("a stale observation refreshed portal geometry", stale, receipt, err)
	}

	// A real self-heal proposal supersedes portal travel, and its uncertain
	// receipt keeps the goal suppressed while the same reservation is pending.
	self.Health = 20
	obs.Spells["2050"] = &actionsv1.EncounterSpell{Known: true, Ready: true, Capability: "heal"}
	obs.Healing = &actionsv1.HealingOptionsResponse{SessionId: sid, HostOwned: true,
		Caster:   &actionsv1.HealingCaster{Guid: 4, Ready: true, Mana: proto.Uint32(30)},
		Patients: []*actionsv1.HealingPatient{{Guid: 4, Health: 20, MaxHealth: 100}},
		Spells: []*actionsv1.HealingRank{{SpellId: 2050, Rank: proto.Uint32(1), Level: proto.Uint32(1),
			Cost: proto.Uint32(30), Amount: 45, CastMs: proto.Uint32(1500), MinRange: proto.Float64(0), MaxRange: proto.Float64(40)}}}
	sequence = observe()
	healing, receipt, err := step(sequence, func(action map[string]any) (PolicyDispatch, error) {
		if action["capability"] != "heal" {
			t.Fatal("expected the genuine self-heal policy action", action)
		}
		return PolicyDispatch{Module: actionsModule, Send: func(context.Context) (behavior.ActionOutcome, map[string]any) {
			return behavior.ActionUnknown, map[string]any{"capability": "heal"}
		}}, nil
	})
	if err != nil || receipt == nil || receipt.Outcome != behavior.ActionUnknown || healing["portalCatchup"] != nil || vm.Status().Pending != 1 {
		t.Fatal("a heal did not supersede portal travel", healing, receipt, err, vm.Status())
	}
	healID := receipt.ActionID
	obs.Healing, self.Health = nil, 100
	sequence = observe()
	pending, receipt, err := step(sequence, nil)
	if err != nil || receipt != nil || pending["portalCatchup"] != nil || len(goals) != 2 || vm.Status().Pending != 1 {
		t.Fatal("pending combat action refreshed portal travel", pending, receipt, err, vm.Status())
	}
	if _, err := host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{
		SessionID: sid, ActionID: healID, Outcome: behavior.ActionConfirmed, Payload: map[string]any{"capability": "heal"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{
		"task": "wait_for_pull", "id": "portal-hold", "ownerGuid": "3",
	}}); err != nil {
		t.Fatal(err)
	}
	sequence = observe()
	acquire, receipt, err := step(sequence, func(action map[string]any) (PolicyDispatch, error) {
		if action["capability"] != "movement_owner" || action["owner"] != "task:portal-hold" || action["mode"] != "acquire" {
			t.Fatal("wait task did not request finite movement ownership", action)
		}
		return PolicyDispatch{Module: actionsModule, Send: func(context.Context) (behavior.ActionOutcome, map[string]any) {
			return behavior.ActionConfirmed, map[string]any{"capability": "movement_owner", "owner": "task:portal-hold",
				"mode": "acquire", "result": map[string]any{"token": "portal-hold-token"}}
		}}, nil
	})
	if err != nil || receipt == nil || acquire["portalCatchup"] != nil {
		t.Fatal("task acquisition did not suppress portal geometry", acquire, receipt, err)
	}
	obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "task:portal-hold", RemainingMs: 3000, Revision: 1}
	sequence = observe()
	held, receipt, err := step(sequence, nil)
	task, _ := held["task"].(map[string]any)
	if err != nil || receipt != nil || held["portalCatchup"] != nil || len(goals) != 2 || task["phase"] != "waiting_for_owner_pull" {
		t.Fatal("active wait task leaked a portal goal", held, receipt, err)
	}
}
