package modrt

import (
	"context"
	"encoding/json"
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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestPolicyPortalIntentUsesCurrentLuaDefaultWithoutNativeFollowing(t *testing.T) {
	const sid = "finite-portal-intent"
	const key = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	const enabled = "M.follow_owner_by_default = true"
	const witness = uint64(9007199254740993)
	ctx := context.Background()
	source := followFallbackBundle(t)
	disabled := strings.Replace(source, enabled, "M.follow_owner_by_default = false", 1)
	tuned := strings.Replace(source, portalTimingDefaults, portalTimingTuned, 1)
	if disabled == source || tuned == source {
		t.Fatal("complete bundle did not expose the current follow and portal timing choices")
	}
	manager := New(packagedPolicySpellRegistry(t), nil, nil)
	manager.started, manager.dir = true, "/private/"+sid
	manager.session.Identity = modsession.Identity{CharacterGUID: 4}
	for _, instance := range manager.instances {
		instance.healthy = true
	}
	t.Cleanup(manager.cancel)
	var goals []*actionsv1.PortalCatchupRequest
	conn, err := grpc.NewClient("passthrough:///finite-portal-intent", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, request, reply any, _ *grpc.ClientConn, _ grpc.UnaryInvoker, _ ...grpc.CallOption) error {
			md, _ := metadata.FromOutgoingContext(ctx)
			incoming := metadata.NewIncomingContext(context.Background(), md)
			deadline, finite := ctx.Deadline()
			if modcb.IncomingPolicyEncounterKey(incoming) != key || !finite || time.Until(deadline) <= 0 ||
				time.Until(deadline) > policyObservationInterval {
				t.Fatal("portal intent lost private finite host authority", method)
			}
			switch method {
			case "/agentwow.policy.encounter.v1.Lease/Renew":
				if request.(*wrapperspb.UInt64Value).Value != 7 {
					t.Fatal("portal intent renewed a foreign visit", request)
				}
			case actionsv1.BuddyActions_SetPortalCatchup_FullMethodName:
				if modcb.IncomingPolicyEncounterVisit(incoming) != 7 {
					t.Fatal("portal intent lost its source visit witness")
				}
				goals = append(goals, proto.Clone(request.(*actionsv1.PortalCatchupRequest)).(*actionsv1.PortalCatchupRequest))
			default:
				t.Fatal("finite portal intent invoked an unrelated capability", method)
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
	self := &actionsv1.EncounterPlayer{Guid: 4, Present: true, AliveKnown: true, Alive: proto.Bool(true),
		MapKnown: true, MapId: proto.Uint32(0), PositionKnown: true, PositionRevision: 11,
		Z: 3, ClassKnown: true, ClassId: 5, MovementKnown: true, HealthKnown: true, Health: 100, MaxHealth: 100}
	start := &actionsv1.ClientPosition{Known: true, MapKnown: true, Z: 3, Revision: 11}
	portal := &actionsv1.PortalFollowObservation{WitnessRevision: witness, SourceVisitId: 7, FollowRevision: 0,
		OwnerGuid: 3, SourceMapId: 0, DestinationMapId: 389, TriggerId: 100, Start: start,
		Ground: &actionsv1.PortalPoint{X: 10, Z: 3}, OwnerAbsent: true,
		SightingAgeKnown: true, SightingAgeMs: 1000, DisappearanceAgeKnown: true, DisappearanceAgeMs: 500}
	obs := &actionsv1.EncounterObservationResponse{SessionId: sid, SelfGuid: 4, VisitId: 7, MapId: proto.Uint32(0),
		HostOwned: proto.Bool(true), WorldObservationRevision: 1, SpellbookKnown: true,
		Players: map[string]*actionsv1.EncounterPlayer{"4": self},
		Party: &actionsv1.EncounterParty{Group: &partyv1.Group{Known: true, InGroup: true, GroupGuid: 1, LeaderGuid: 3,
			Members: []*partyv1.Member{{Guid: 4, Self: true, Online: true}, {Guid: 3, Online: true}}}},
		Spells: map[string]*actionsv1.EncounterSpell{"585": {Known: true, Capability: "offense"}},
		Navigation: &actionsv1.NavigationObservation{Following: false, FollowRevision: 0, OwnerGuid: 3,
			GroundNavigationConfigured: true, Portal: portal}}
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
	check := func(want bool) map[string]any {
		t.Helper()
		before := len(goals)
		out, receipt := step(nil)
		count := before
		if want {
			count++
		}
		if receipt != nil || out["action"] != nil || len(goals) != count || (out["portalCatchup"] != nil) != want ||
			obs.Navigation.Following {
			t.Fatal("wrong finite portal intent while native Following is false", want, out, receipt, goals)
		}
		if want {
			goal := goals[before]
			candidates := 1
			if portal.CatchingUp {
				candidates = 7
			}
			if goal.SessionId != sid || goal.SelfGuid != 4 || goal.VisitId != 7 || goal.OwnerGuid != 3 ||
				goal.FollowRevision != obs.Navigation.FollowRevision || goal.WitnessRevision != witness ||
				!proto.Equal(goal.Start, start) || len(goal.Candidates) != candidates ||
				!proto.Equal(goal.Candidates[0], portal.Ground) {
				t.Fatal("finite intent lost its exact current portal witness", goal)
			}
		}
		return out
	}
	reload := func(next string) {
		t.Helper()
		revision, err = host.Reload(ctx, next, revision)
		if err != nil || host.vm.Status().SessionID != sid {
			t.Fatal("portal intent edit replaced the connected session", revision, err)
		}
	}
	initial := check(true) // A nearby portal uses the same finite setter at counter zero.
	goal := initial["portalCatchup"].(map[string]any)
	current := policyEncounterObservation{visit: 7, sequence: sequence}
	beforeInvalid := len(goals)
	for _, counter := range []any{"", "00", "+0", "-1", "1.0", "18446744073709551616", float64(0)} {
		bad := map[string]any{}
		for name, value := range goal {
			bad[name] = value
		}
		bad["followRevision"] = counter
		if err := host.refreshPortalCatchup(ctx, map[string]any{"portalCatchup": bad}, current); status.Code(err) != codes.InvalidArgument {
			t.Fatal("malformed portal follow counter reached the native setter", counter, err)
		}
	}
	for _, field := range []string{"selfGuid", "visitId", "ownerGuid", "witnessRevision", "start"} {
		bad := map[string]any{}
		for name, value := range goal {
			bad[name] = value
		}
		bad[field] = "0"
		if field == "start" {
			start := map[string]any{}
			for name, value := range goal["start"].(map[string]any) {
				start[name] = value
			}
			start["revision"] = "0"
			bad[field] = start
		}
		if err := host.refreshPortalCatchup(ctx, map[string]any{"portalCatchup": bad}, current); status.Code(err) != codes.InvalidArgument {
			t.Fatal("zero portal identity or witness was treated as a counter", field, err)
		}
	}
	if len(goals) != beforeInvalid {
		t.Fatal("rejected portal fields emitted a typed request")
	}

	reload(disabled)
	check(false)
	reload(source)
	check(true)
	portal.Ground.X, portal.CatchingUp = 100, true
	check(true) // A distant portal retains the ordered bend candidates.
	obs.Navigation.FollowRevision, portal.FollowRevision = 1, 1
	portal.Ground.X, portal.CatchingUp = 10, false
	check(true) // Fresh current intent remains usable after a native Follow(false).
	portal.DisappearanceAgeMs = 100
	check(false)
	reload(tuned)
	check(true) // The current full source also owns the portal wait delay.

	// The shared scheduler suppresses this newly admitted portal path while a
	// genuine action has an uncertain result.
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
			t.Fatal("expected the actual self-heal scheduler path", action)
		}
		return PolicyDispatch{Module: actionsModule, Send: func(context.Context) (behavior.ActionOutcome, map[string]any) {
			return behavior.ActionUnknown, map[string]any{"capability": "heal"}
		}}, nil
	})
	if receipt == nil || receipt.Outcome != behavior.ActionUnknown || out["portalCatchup"] != nil ||
		len(goals) != beforeAction || host.vm.Status().Pending != 1 {
		t.Fatal("a current action leaked a portal goal", out, receipt, goals)
	}
	actionID := receipt.ActionID
	obs.Healing, self.Health = nil, 100
	check(false)
	if pending := host.vm.Outstanding(); len(pending) != 1 || pending[0].ActionID != actionID || pending[0].Outcome != behavior.ActionUnknown {
		t.Fatal("portal suppression lost the uncertain action reservation", pending)
	}
	if _, err := host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{
		SessionID: sid, ActionID: actionID, Outcome: behavior.ActionConfirmed,
	}}); err != nil {
		t.Fatal(err)
	}
	check(true)

	// The existing finite wait task remains the way to hold position.
	obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
	if _, err := host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{
		"task": "wait_for_pull", "id": "portal-intent-wait", "ownerGuid": "3",
	}}); err != nil {
		t.Fatal(err)
	}
	beforeWait := len(goals)
	out, receipt = step(func(action map[string]any) (PolicyDispatch, error) {
		if action["capability"] != "movement_owner" || action["mode"] != "acquire" ||
			action["owner"] != "task:portal-intent-wait" || action["leaseMs"] != float64(3000) {
			t.Fatal("wait task bypassed finite movement ownership", action)
		}
		return PolicyDispatch{Module: actionsModule, Send: func(context.Context) (behavior.ActionOutcome, map[string]any) {
			return behavior.ActionConfirmed, map[string]any{"capability": "movement_owner", "mode": "acquire",
				"owner": "task:portal-intent-wait", "result": map[string]any{"token": "portal-intent-token"}}
		}}, nil
	})
	if receipt == nil || receipt.Outcome != behavior.ActionConfirmed || out["portalCatchup"] != nil || len(goals) != beforeWait {
		t.Fatal("task acquisition leaked a finite portal goal", out, receipt, goals)
	}
	obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "task:portal-intent-wait", RemainingMs: 3000, Revision: 1}
	held := check(false)
	task, _ := held["task"].(map[string]any)
	if task["phase"] != "waiting_for_owner_pull" {
		t.Fatal("portal intent ignored the actual finite wait handler", held)
	}
}
