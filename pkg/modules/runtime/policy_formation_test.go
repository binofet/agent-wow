package modrt

import (
	"context"
	"encoding/json"
	"math"
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

// Run the editable development bundle through the persistent session host.
// The intercepted RPC is the production typed dispatch, with no game transport.
func TestPolicyFollowFormationReloadsInTheSameSession(t *testing.T) {
	const sid, key = "formation-policy-session", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bundle, err := exec.Command("python3", filepath.Join("..", "..", "..", "dev", "encounter-bundle")).Output()
	if err != nil {
		t.Fatal(err)
	}
	source := string(bundle)
	changed := strings.Replace(source, "[8]={5.1,.2}", "[8]={7.1,1.2}", 1)
	changed = strings.Replace(changed, "local reaction=200+50*last", "local reaction=0", 1)
	if source == changed || strings.Contains(changed, "[8]={5.1,.2}") || strings.Contains(changed, "local reaction=200+50*last") {
		t.Fatal("the complete bundle did not contain the expected editable role and reaction policy")
	}

	manager := New(packagedPolicySpellRegistry(t), nil, nil)
	t.Cleanup(manager.cancel)
	vm, err := behavior.NewSession(context.Background(), sid, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vm.Close)
	initialRevision := vm.Status().Revision
	var goals []*actionsv1.FollowFormationRequest
	var renewals []uint64
	denyGoal := false
	conn, err := grpc.NewClient("passthrough:///formation-fixture", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, request, reply any, _ *grpc.ClientConn, _ grpc.UnaryInvoker, _ ...grpc.CallOption) error {
			md, _ := metadata.FromOutgoingContext(ctx)
			incoming := metadata.NewIncomingContext(context.Background(), md)
			deadline, finite := ctx.Deadline()
			if modcb.IncomingPolicyEncounterKey(incoming) != key || !finite || time.Until(deadline) <= 0 ||
				time.Until(deadline) > policyObservationInterval {
				t.Fatal("formation dispatch lost its private identity or finite call deadline", method, md, deadline)
			}
			switch method {
			case "/agentwow.policy.encounter.v1.Lease/Renew":
				renewals = append(renewals, request.(*wrapperspb.UInt64Value).Value)
			case actionsv1.BuddyActions_SetFollowFormation_FullMethodName:
				if denyGoal {
					return status.Error(codes.FailedPrecondition, "observed owner pose changed")
				}
				if modcb.IncomingPolicyEncounterVisit(incoming) != 7 {
					t.Fatal("formation goal lost its current native visit witness", md)
				}
				goals = append(goals, proto.Clone(request.(*actionsv1.FollowFormationRequest)).(*actionsv1.FollowFormationRequest))
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

	player := func(guid uint64, x, y float32) *actionsv1.EncounterPlayer {
		return &actionsv1.EncounterPlayer{Guid: guid, Present: true, AliveKnown: true, Alive: proto.Bool(true),
			MapKnown: true, MapId: proto.Uint32(0), PositionKnown: true, PositionRevision: 11,
			X: x, Y: y, Z: 3, ClassKnown: true, ClassId: 8, MovementKnown: true}
	}
	self, owner := player(4, 0, 0), player(3, 10, 20)
	obs := &actionsv1.EncounterObservationResponse{
		SessionId: sid, SelfGuid: 4, VisitId: 7, MapId: proto.Uint32(0), HostOwned: proto.Bool(true),
		WorldObservationRevision: 1, SpellbookKnown: true,
		Party: &actionsv1.EncounterParty{Group: &partyv1.Group{Known: true, InGroup: true, GroupGuid: 1, LeaderGuid: 3,
			Members: []*partyv1.Member{{Guid: 4, Self: true, Online: true}, {Guid: 3, Online: true}}}},
		Players: map[string]*actionsv1.EncounterPlayer{"4": self, "3": owner},
		Spells:  map[string]*actionsv1.EncounterSpell{"133": {Known: true, Capability: "offense"}},
		Navigation: &actionsv1.NavigationObservation{Following: true, FollowFormationSupported: true, GroundNavigationConfigured: true,
			FollowRevision: 3, FollowOwner: owner},
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
		seq, err := host.Enqueue(behavior.Event{Kind: "encounter_observation", Value: value})
		if err != nil {
			t.Fatal(err)
		}
		return strconv.FormatUint(seq, 10)
	}
	step := func(sequence string, resolve func(map[string]any) (PolicyDispatch, error)) (map[string]any, *behavior.ActionReceipt) {
		t.Helper()
		ctx := context.WithValue(context.Background(), policyEncounterVisitKey{}, policyEncounterObservation{visit: 7, sequence: sequence})
		out, receipt, err := host.Step(ctx, map[string]any{"kind": "encounter", "observationSequence": sequence}, resolve)
		if err != nil {
			t.Fatal(err)
		}
		return out, receipt
	}
	assertGoal := func(index int, x, y float64, ready, hold bool) {
		t.Helper()
		if len(goals) != index+1 {
			t.Fatalf("expected %d typed goals, got %+v", index+1, goals)
		}
		got := goals[index]
		if got.SessionId != sid || got.SelfGuid != 4 || got.VisitId != 7 || got.OwnerGuid != 3 ||
			got.OwnerPositionRevision != owner.PositionRevision || got.FollowRevision != 3 ||
			math.Abs(float64(got.X)-x) > .00001 || math.Abs(float64(got.Y)-y) > .00001 ||
			got.Z != 3 || got.Orientation != 0 || got.StartDistance != 1.8 || got.StopDistance != .8 ||
			got.Ready != ready || got.Hold != hold {
			t.Fatalf("wrong Lua-selected typed formation goal: %v", got)
		}
	}

	sequence := observe()
	first, receipt := step(sequence, nil)
	if receipt != nil || first["action"] != nil || len(renewals) != 1 || renewals[0] != 7 {
		t.Fatal("formation created an action reservation or skipped native ownership", first, receipt, renewals)
	}
	assertGoal(0, 5.016, 20.08, false, true)
	revision, err := host.Reload(context.Background(), changed, initialRevision)
	if err != nil || revision == initialRevision || host.vm.Status().SessionID != sid {
		t.Fatal("complete-source reload replaced the session or failed to advance policy", revision, err)
	}
	if _, err := host.Reload(context.Background(), source, initialRevision); err == nil {
		t.Fatal("stale source revision replaced the new formation policy")
	}
	// A real stopped-to-moving transition evaluates the reloaded reaction.
	owner.Moving, owner.PositionRevision = true, 12
	sequence = observe()
	second, receipt := step(sequence, nil)
	if receipt != nil || second["action"] != nil || len(renewals) != 2 || renewals[1] != 7 {
		t.Fatal("reloaded formation acquired an unrelated reservation", second, receipt, renewals)
	}
	assertGoal(1, 3.016, 21.08, true, false)

	current := policyEncounterObservation{visit: 7, sequence: sequence}
	goal := second["followFormation"].(map[string]any)
	denyGoal = true
	if err := host.refreshFollowFormation(context.Background(), second, current); status.Code(err) != codes.FailedPrecondition ||
		status.Convert(err).Message() != "observed owner pose changed" {
		t.Fatalf("native formation denial was hidden: %v", err)
	}
	denyGoal = false
	for _, field := range []string{"sessionId", "selfGuid", "visitId", "observationSequence"} {
		bad := map[string]any{}
		for name, value := range goal {
			bad[name] = value
		}
		bad[field] = "99"
		if err := host.refreshFollowFormation(context.Background(), map[string]any{"followFormation": bad}, current); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("foreign %s reached the formation RPC: %v", field, err)
		}
	}
	if len(goals) != 2 {
		t.Fatal("a rejected formation identity reached the native route")
	}
	stale, _ := step("1", nil)
	if stale["followFormation"] != nil || len(goals) != 2 {
		t.Fatal("an older observation refreshed the goal", stale)
	}

	// A genuine action from this same bundle holds a pending reservation.
	// Its uncertain result must suppress the independent formation refresh.
	self.HealthKnown, self.Health, self.MaxHealth = true, 20, 100
	obs.Spells["2050"] = &actionsv1.EncounterSpell{Known: true, Ready: true, Capability: "heal"}
	obs.Healing = &actionsv1.HealingOptionsResponse{SessionId: sid, HostOwned: true,
		Caster:   &actionsv1.HealingCaster{Guid: 4, Ready: true, Mana: proto.Uint32(30)},
		Patients: []*actionsv1.HealingPatient{{Guid: 4, Health: 20, MaxHealth: 100}},
		Spells: []*actionsv1.HealingRank{{SpellId: 2050, Rank: proto.Uint32(1), Level: proto.Uint32(1),
			Cost: proto.Uint32(30), Amount: 45, CastMs: proto.Uint32(1500), MinRange: proto.Float64(0), MaxRange: proto.Float64(40)}}}
	sequence = observe()
	_, receipt = step(sequence, func(action map[string]any) (PolicyDispatch, error) {
		if action["capability"] != "heal" {
			t.Fatal("expected a real policy heal to reserve the action ledger", action)
		}
		return PolicyDispatch{Module: actionsModule, Send: func(context.Context) (behavior.ActionOutcome, map[string]any) {
			return behavior.ActionUnknown, map[string]any{"capability": "heal"}
		}}, nil
	})
	if receipt == nil || receipt.Outcome != behavior.ActionUnknown || vm.Status().Pending != 1 {
		t.Fatal("the uncertain heal did not remain reserved", receipt, vm.Status())
	}
	obs.Healing = nil
	self.Health = 100
	sequence = observe()
	pending, receipt := step(sequence, nil)
	if receipt != nil || pending["followFormation"] != nil || len(goals) != 2 || vm.Status().Pending != 1 {
		t.Fatal("a pending action leaked a formation refresh", pending, receipt, goals, vm.Status())
	}
}

// The simple owner-distance policy also stays editable when ground navigation
// is absent. Reload changes the Lua response envelope for this same live host.
func TestPolicyNearOwnerFollowReloadsInTheSameSession(t *testing.T) {
	const sid, key = "near-owner-policy-session", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	bundle, err := exec.Command("python3", filepath.Join("..", "..", "..", "dev", "encounter-bundle")).Output()
	if err != nil {
		t.Fatal(err)
	}
	source := string(bundle)
	changed := strings.Replace(source, "local near_stop, near_limit = 3,30", "local near_stop, near_limit = 2,40", 1)
	if source == changed {
		t.Fatal("the complete bundle did not contain the editable near-owner response envelope")
	}
	manager := New(packagedPolicySpellRegistry(t), nil, nil)
	t.Cleanup(manager.cancel)
	vm, err := behavior.NewSession(context.Background(), sid, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vm.Close)
	initialRevision := vm.Status().Revision
	var goals []*actionsv1.FollowFormationRequest
	var renewals []uint64
	conn, err := grpc.NewClient("passthrough:///near-owner-fixture", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, request, reply any, _ *grpc.ClientConn, _ grpc.UnaryInvoker, _ ...grpc.CallOption) error {
			md, _ := metadata.FromOutgoingContext(ctx)
			incoming := metadata.NewIncomingContext(context.Background(), md)
			deadline, finite := ctx.Deadline()
			if modcb.IncomingPolicyEncounterKey(incoming) != key || !finite || time.Until(deadline) <= 0 ||
				time.Until(deadline) > policyObservationInterval {
				t.Fatal("near-owner request lost its private identity or finite deadline", method)
			}
			switch method {
			case "/agentwow.policy.encounter.v1.Lease/Renew":
				renewals = append(renewals, request.(*wrapperspb.UInt64Value).Value)
			case actionsv1.BuddyActions_SetFollowFormation_FullMethodName:
				if modcb.IncomingPolicyEncounterVisit(incoming) != 7 {
					t.Fatal("near-owner request lost the native visit witness")
				}
				goals = append(goals, proto.Clone(request.(*actionsv1.FollowFormationRequest)).(*actionsv1.FollowFormationRequest))
			default:
				t.Fatal("unexpected near-owner capability", method)
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
	player := func(guid uint64, x float32) *actionsv1.EncounterPlayer {
		return &actionsv1.EncounterPlayer{Guid: guid, Present: true, AliveKnown: true, Alive: proto.Bool(true),
			MapKnown: true, MapId: proto.Uint32(0), PositionKnown: true, PositionRevision: 11,
			X: x, Z: 3, ClassKnown: true, ClassId: 8, MovementKnown: true}
	}
	self, owner := player(4, 0), player(3, 35)
	obs := &actionsv1.EncounterObservationResponse{
		SessionId: sid, SelfGuid: 4, VisitId: 7, MapId: proto.Uint32(0), HostOwned: proto.Bool(true),
		WorldObservationRevision: 1, SpellbookKnown: true,
		Players: map[string]*actionsv1.EncounterPlayer{"4": self, "3": owner},
		Spells:  map[string]*actionsv1.EncounterSpell{"133": {Known: true, Capability: "offense"}},
		Navigation: &actionsv1.NavigationObservation{Following: true, FollowFormationSupported: true,
			GroundNavigationConfigured: false, FollowRevision: 3, FollowOwner: owner},
	}
	step := func(radius float64, hold bool) {
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
		seq := strconv.FormatUint(sequence, 10)
		ctx := context.WithValue(context.Background(), policyEncounterVisitKey{}, policyEncounterObservation{visit: 7, sequence: seq})
		before := len(goals)
		out, receipt, err := host.Step(ctx, map[string]any{"kind": "encounter", "observationSequence": seq}, nil)
		if err != nil || receipt != nil || out["action"] != nil || len(goals) != before+1 {
			t.Fatalf("near-owner goal did not pass the existing typed pipeline: %+v, %+v, %v, %+v", out, receipt, err, goals)
		}
		got := goals[before]
		if got.SessionId != sid || got.SelfGuid != 4 || got.VisitId != 7 || got.OwnerGuid != 3 ||
			got.OwnerPositionRevision != 11 || got.FollowRevision != 3 || got.X != 35 || got.Y != 0 ||
			got.Z != 3 || got.Orientation != 0 || got.StartDistance != radius || got.StopDistance != radius ||
			!got.Ready || got.Hold != hold || len(renewals) != before+1 || renewals[before] != 7 {
			t.Fatalf("near-owner source lost its exact owner goal or response envelope: %v, leases %+v", got, renewals)
		}
	}
	step(3, true) // The default maximum gap is 30 yards.
	revision, err := host.Reload(context.Background(), changed, initialRevision)
	if err != nil || revision == initialRevision || vm.Status().SessionID != sid {
		t.Fatal("near-owner reload replaced the session or retained the previous policy", revision, err)
	}
	step(2, false) // A 35-yard owner is inside the revised 40-yard envelope.
	self.X = 32.5
	step(2, false) // The old 3-yard stop distance would have held this body.
	self.X = 34
	step(2, true)
}
