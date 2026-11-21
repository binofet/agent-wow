package modrt

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
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

const portalTimingDefaults = "local portal_wait_ms, portal_seen_ms, portal_catchup_ms = 500,20000,75000"
const portalTimingTuned = "local portal_wait_ms, portal_seen_ms, portal_catchup_ms = 100,40000,90000"

func TestPolicyPortalTimingCurrentSourceReloadLeavesPersistentStateUntouched(t *testing.T) {
	ctx := context.Background()
	manager := catalogManager(t, 4, "portal-timing-pure")
	t.Cleanup(manager.cancel)
	source := followFallbackBundle(t)
	changed := strings.Replace(source, portalTimingDefaults, portalTimingTuned, 1)
	if changed == source {
		t.Fatal("complete bundle did not contain the editable portal timing policy")
	}
	host, err := manager.StartPolicy(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.close)
	revision := host.vm.Status().Revision
	readMetadata := func() {
		t.Helper()
		raw, err := manager.InvokeJSON(ctx, "session.policy_source", json.RawMessage(`{"sessionId":"portal-timing-pure","characterGuid":"4"}`))
		if err != nil {
			t.Fatal(err)
		}
		var view policySourceResponse
		if err := json.Unmarshal(raw, &view); err != nil {
			t.Fatal(err)
		}
		if view.Revision != revision || view.FollowFallback.Revision != revision ||
			view.FollowFallback.EntryPoint != "follow_formation" || !view.FollowFallback.Available {
			t.Fatal("source inspection lost the active portal timing revision", view.FollowFallback)
		}
	}
	check := func(seen, absent float64, catchingUp, eligible, ready bool) {
		t.Helper()
		input := followFallbackTestInput("portal_follow")
		input["portal"] = map[string]any{
			"sightingAgeKnown": true, "sightingAgeMs": seen,
			"disappearanceAgeKnown": true, "disappearanceAgeMs": absent,
			"catchingUp": catchingUp, "ownerAbsent": true, "following": true,
			"sourceVisitId": "7", "currentVisitId": "7", "followRevision": "3", "currentFollowRevision": "3",
		}
		available, gotRevision, decision, err := host.followDecision(ctx, input)
		if err != nil || !available || gotRevision != revision || decision["eligible"] != eligible || decision["ready"] != ready {
			t.Fatalf("wrong current-source portal timing at seen=%v absent=%v catchup=%t: %t %s %+v %v",
				seen, absent, catchingUp, available, gotRevision, decision, err)
		}
	}
	before, err := host.vm.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	readMetadata()
	check(1000, 499, false, true, false)
	check(1000, 500, false, true, true)
	check(20000, 500, false, true, true)
	check(20001, 500, false, false, false)
	check(75000, 500, true, true, true)
	check(75001, 500, true, false, false)
	check(1000, 100, false, true, false)
	check(30000, 1000, false, false, false)
	check(80000, 1000, true, false, false)
	assertFollowFallbackState(t, host, before)

	revision, err = host.Reload(ctx, changed, revision)
	if err != nil || host.vm.Status().SessionID != "portal-timing-pure" {
		t.Fatal("portal timing reload changed the connected policy session", revision, err)
	}
	afterReload, err := host.vm.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	readMetadata()
	check(1000, 100, false, true, true)
	check(30000, 1000, false, true, true)
	check(80000, 1000, true, true, true)
	check(40000, 100, false, true, true)
	check(40001, 100, false, false, false)
	check(90000, 100, true, true, true)
	check(90001, 100, true, false, false)
	assertFollowFallbackState(t, host, afterReload)
	if afterReload.Status.Cursor != 0 || afterReload.Status.Buffered != 0 || afterReload.Status.Pending != 0 ||
		afterReload.LatestDecision != nil {
		t.Fatal("portal fallback timing evaluated persistent hooks", afterReload)
	}
}

func TestPolicyPortalTimingControlsTypedGoalsAfterSameSessionReload(t *testing.T) {
	const sid, key = "portal-timing-host", "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	const witness = uint64(9007199254740993)
	ctx := context.Background()
	source := followFallbackBundle(t)
	changed := strings.Replace(source, portalTimingDefaults, portalTimingTuned, 1)
	if changed == source {
		t.Fatal("complete bundle did not contain the editable portal timing policy")
	}
	manager := New(packagedPolicySpellRegistry(t), nil, nil)
	manager.started, manager.dir = true, "/private/"+sid
	manager.session.Identity = modsession.Identity{CharacterGUID: 4}
	for _, instance := range manager.instances {
		instance.healthy = true
	}
	t.Cleanup(manager.cancel)
	var goals []*actionsv1.PortalCatchupRequest
	renewals := 0
	conn, err := grpc.NewClient("passthrough:///portal-timing", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, request, reply any, _ *grpc.ClientConn, _ grpc.UnaryInvoker, _ ...grpc.CallOption) error {
			md, _ := metadata.FromOutgoingContext(ctx)
			incoming := metadata.NewIncomingContext(context.Background(), md)
			deadline, finite := ctx.Deadline()
			if modcb.IncomingPolicyEncounterKey(incoming) != key || !finite || time.Until(deadline) <= 0 ||
				time.Until(deadline) > policyObservationInterval {
				t.Fatal("timed portal goal lost its private finite host scope", method)
			}
			switch method {
			case "/agentwow.policy.encounter.v1.Lease/Renew":
				if request.(*wrapperspb.UInt64Value).Value != 7 {
					t.Fatal("portal timing lost current encounter ownership", request)
				}
				renewals++
			case actionsv1.BuddyActions_SetPortalCatchup_FullMethodName:
				if modcb.IncomingPolicyEncounterVisit(incoming) != 7 {
					t.Fatal("portal goal lost its source visit witness")
				}
				goals = append(goals, proto.Clone(request.(*actionsv1.PortalCatchupRequest)).(*actionsv1.PortalCatchupRequest))
			default:
				t.Fatal("unexpected timed portal capability", method)
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
	initialRevision := host.vm.Status().Revision
	start := &actionsv1.ClientPosition{Known: true, MapKnown: true, Z: 3, Revision: 11}
	portal := &actionsv1.PortalFollowObservation{WitnessRevision: witness, SourceVisitId: 7, FollowRevision: 3,
		OwnerGuid: 3, SourceMapId: 0, DestinationMapId: 389, TriggerId: 100, Start: start,
		Ground: &actionsv1.PortalPoint{X: 100, Z: 3}, OwnerAbsent: true,
		SightingAgeKnown: true, DisappearanceAgeKnown: true}
	obs := &actionsv1.EncounterObservationResponse{SessionId: sid, SelfGuid: 4, VisitId: 7, MapId: proto.Uint32(0),
		HostOwned: proto.Bool(true), WorldObservationRevision: 1, SpellbookKnown: true,
		Players: map[string]*actionsv1.EncounterPlayer{"4": {Guid: 4, Present: true, AliveKnown: true, Alive: proto.Bool(true),
			MapKnown: true, MapId: proto.Uint32(0), PositionKnown: true, PositionRevision: 11,
			Z: 3, ClassKnown: true, ClassId: 5, MovementKnown: true}},
		Spells: map[string]*actionsv1.EncounterSpell{"585": {Known: true, Capability: "offense"}},
		Navigation: &actionsv1.NavigationObservation{Following: true, FollowRevision: 3, OwnerGuid: 3,
			GroundNavigationConfigured: true, Portal: portal}}
	step := func(seen, absent uint64, catchingUp, wantGoal bool) {
		t.Helper()
		portal.SightingAgeMs, portal.DisappearanceAgeMs, portal.CatchingUp = seen, absent, catchingUp
		raw, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(obs)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		rawPortal := value["navigation"].(map[string]any)["portal"].(map[string]any)
		if rawPortal["sightingAgeMs"] != strconv.FormatUint(seen, 10) ||
			rawPortal["disappearanceAgeMs"] != strconv.FormatUint(absent, 10) ||
			rawPortal["witnessRevision"] != strconv.FormatUint(witness, 10) {
			t.Fatal("raw portal timing or witness lost exact protobuf JSON strings", rawPortal)
		}
		sequence, err := host.Enqueue(behavior.Event{Kind: "encounter_observation", Value: value})
		if err != nil {
			t.Fatal(err)
		}
		seq := strconv.FormatUint(sequence, 10)
		call := context.WithValue(ctx, policyEncounterVisitKey{}, policyEncounterObservation{visit: 7, sequence: seq})
		before := len(goals)
		out, receipt, err := host.Step(call, map[string]any{"kind": "encounter", "observationSequence": seq}, nil)
		if err != nil || receipt != nil || out["action"] != nil || host.vm.Status().Pending != 0 {
			t.Fatal("portal timing created an action reservation", out, receipt, err)
		}
		gotGoal := len(goals) == before+1
		if gotGoal != wantGoal || (out["portalCatchup"] != nil) != wantGoal {
			t.Fatalf("wrong attached timing decision at seen=%d absent=%d catchup=%t: %+v goals=%d", seen, absent, catchingUp, out, len(goals)-before)
		}
		if wantGoal {
			goal := goals[before]
			if goal.SessionId != sid || goal.SelfGuid != 4 || goal.VisitId != 7 || goal.OwnerGuid != 3 ||
				goal.WitnessRevision != witness || goal.FollowRevision != 3 || !proto.Equal(goal.Start, start) ||
				len(goal.Candidates) != 7 || goal.Candidates[0].X != 100 || goal.Candidates[1].X != 8 || goal.Candidates[1].Y != -6 {
				t.Fatal("timing changed the exact witnessed portal geometry", goal)
			}
		}
	}
	step(1000, 499, true, false)
	step(1000, 500, true, true)
	step(80000, 1000, true, false)
	revision, err := host.Reload(ctx, changed, initialRevision)
	if err != nil || revision == initialRevision || host.vm.Status().SessionID != sid {
		t.Fatal("attached timing reload changed the character session", revision, err)
	}
	step(1000, 100, true, true)
	step(80000, 1000, true, true)
	step(90001, 100, true, false)
	if renewals != 6 {
		t.Fatal("timing decisions skipped ordinary finite encounter renewal", renewals)
	}
}
