package buddyactions

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestFlightpathReleaseContractPreservesExistingRPCsAndResolvesAllImports(t *testing.T) {
	// This descriptor is shipped with the binary. NewFiles resolves its whole
	// imported closure instead of relying on the test process's global registry.
	raw, err := os.ReadFile("module.pb")
	if err != nil {
		t.Fatal(err)
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(raw, &set); err != nil {
		t.Fatal(err)
	}
	if _, err := protodesc.NewFiles(&set); err != nil {
		t.Fatal("Actions release descriptor has an unresolved imported contract", err)
	}
	registry, err := moddisc.Load("..")
	if err != nil {
		t.Fatal("release registry or another module's imported descriptors are incomplete", err)
	}
	actions, ok := registry.Lookup("buddy-actions")
	if !ok {
		t.Fatal("Actions manifest missing")
	}
	compiled := actionsv1.File_modules_buddy_actions_actions_proto.Services().ByName("BuddyActions")
	// Every public alias from efc1a13 remains routed to the same compiled RPC.
	const baseline = `select_target SelectTarget
approach_target ApproachTarget
move_body MoveBody
interrupt_cast InterruptCast
set_follow_formation SetFollowFormation
set_portal_catchup SetPortalCatchup
follow Follow
assist Assist
engage_npc EngageNPC
resurrect_once ResurrectOnce
accept_resurrection AcceptResurrection
pick_lock PickLock
create_item CreateItem
cast_self CastSelf
healing_options HealingOptions
self_mana_observation SelfManaObservation
encounter_observation EncounterObservation
cast_heal CastHeal
cast_offense CastOffense
cast_cleanse CastCleanse
cast_buff CastBuff
melee Melee
polymorph Polymorph
sap Sap
heal_once HealOnce
status Status
buffs Buffs
spell_toolkit SpellToolkit
reload_behavior ReloadBehavior
behavior Behavior
acquire_movement AcquireMovement
release_movement ReleaseMovement
acquire_interaction AcquireInteraction
release_interaction ReleaseInteraction
approach_loot ApproachLoot
approach_quest ApproachQuest
travel_quest TravelQuest
travel_trainer TravelTrainer
travel_vendor TravelVendor
travel_to TravelTo
plan_route_leg PlanRouteLeg
exit_crypt ExitCrypt
practice Practice
fargodeep_encounter FargodeepEncounter
recovery Recovery
release_spirit ReleaseSpirit
query_corpse QueryCorpse
approach_corpse ApproachCorpse
reclaim_corpse ReclaimCorpse
flightpath_observation FlightpathObservation
query_flightpath QueryFlightpath`
	for _, line := range strings.Split(baseline, "\n") {
		pair := strings.Fields(line)
		method, ok := actions.RPC(pair[0])
		if !ok || method.Path() != "/warcraftbuddy.actions.v1.BuddyActions/"+pair[1] {
			t.Fatal("release removed or rerouted an Actions RPC", pair, method.Path())
		}
		descriptor := method.Descriptor()
		found := false
		for i := 0; i < compiled.Methods().Len(); i++ {
			candidate := compiled.Methods().Get(i)
			if string(candidate.Name()) == pair[1] {
				found = candidate.Input().FullName() == descriptor.Input().FullName() && candidate.Output().FullName() == descriptor.Output().FullName()
			}
		}
		if !found {
			t.Fatal("manifest descriptor disagrees with the generated compiled method", pair)
		}
	}
	if actions.Manifest().ScriptAPI["flightpath_observation"].Effect != moddisc.EffectRead ||
		actions.Manifest().ScriptAPI["query_flightpath"].Effect != moddisc.EffectMutation {
		t.Fatal("flightpath query was mislabeled as a pure read")
	}
	for _, op := range []uint16{opcode.SMSGTaxinodeStatus, opcode.SMSGNewTaxiPath, opcode.SMSGShowtaxinodes} {
		method, ok := actions.Packet(op)
		if !ok || method.Path() != "/warcraftbuddy.actions.v1.BuddyActions/OnPacket" {
			t.Fatal("taxi receipt opcode is not subscribed to the compiled handler", op)
		}
	}
}

func TestFlightpathPublishedDescriptorRoutesBothRPCsToCompiledPrimitives(t *testing.T) {
	registry, err := moddisc.Load("..")
	if err != nil {
		t.Fatal(err)
	}
	actions, ok := registry.Lookup("buddy-actions")
	if !ok {
		t.Fatal("missing Actions release contract")
	}
	x := newFlightpathRig(t)
	listener := bufconn.Listen(64 << 10)
	server := grpc.NewServer()
	actionsv1.RegisterBuddyActionsServer(server, x.s)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///flightpath-contract", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, alias := range []string{"query_flightpath", "flightpath_observation"} {
		method, ok := actions.RPC(alias)
		if !ok {
			t.Fatal("flightpath alias missing", alias)
		}
		input, output := dynamicpb.NewMessage(method.Descriptor().Input()), dynamicpb.NewMessage(method.Descriptor().Output())
		if alias == "query_flightpath" {
			raw, err := protojson.Marshal(x.request(t, "status"))
			if err != nil {
				t.Fatal(err)
			}
			if err := protojson.Unmarshal(raw, input); err != nil {
				t.Fatal(err)
			}
		}
		if err := conn.Invoke(ctx, method.Path(), input, output); err != nil {
			t.Fatal("published flightpath route did not reach its real primitive", alias, err)
		}
		raw, err := protojson.Marshal(output)
		var view actionsv1.FlightpathObservationResponse
		if err != nil || protojson.Unmarshal(raw, &view) != nil || view.SessionId != x.s.sessionID || view.SelfGuid != x.s.selfGUID ||
			view.Revision != 1 || !view.GetOperation().GetPending() || x.client.worldReads != 1 || x.client.sendCalls != 1 ||
			len(x.f.packets) != 1 || x.f.packets[0].Opcode != opcode.CMSGTaxinodeStatusQuery {
			t.Fatal("published query/read lost the exact receipt or dispatched twice", alias, &view, err, x.f.packets)
		}
	}
}
