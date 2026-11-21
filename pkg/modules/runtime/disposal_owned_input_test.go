package modrt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
)

type disposalOwnedPeer struct {
	inventory *inventoryv1.State
	world     *worldv1.SnapshotResponse
	journal   *questsv1.JournalResponse
	source    *inventoryv1.ItemRef
	item      *tradev1.ItemDetails
	calls     []string
	fail      map[string]error
	before    func(string)
	writes    int
}

func (p *disposalOwnedPeer) invoke(_ context.Context, method string, request, result any, _ *grpc.ClientConn, _ grpc.UnaryInvoker, _ ...grpc.CallOption) error {
	p.calls = append(p.calls, method)
	if p.before != nil {
		p.before(method)
	}
	if err := p.fail[method]; err != nil {
		return err
	}
	in, ok := request.(proto.Message)
	if !ok || proto.Size(in) != 0 {
		return fmt.Errorf("unexpected disposal fact request %T: %v", request, request)
	}
	var observed proto.Message
	switch method {
	case worldv1.BuddyWorld_Snapshot_FullMethodName:
		if _, ok := request.(*worldv1.SnapshotRequest); !ok {
			return fmt.Errorf("untyped World snapshot %T", request)
		}
		observed = p.world
	case inventoryv1.BuddyInventory_RawGearFacts_FullMethodName:
		if _, ok := request.(*emptypb.Empty); !ok {
			return fmt.Errorf("untyped raw gear facts %T", request)
		}
		observed = p.inventory
	case questsv1.BuddyQuests_Journal_FullMethodName:
		if _, ok := request.(*questsv1.JournalRequest); !ok {
			return fmt.Errorf("untyped quest journal %T", request)
		}
		observed = p.journal
	default:
		// In particular, GetState would enter Inventory.observe -> Trade.state
		// while a future Trade caller holds opMu. Never silently fake that path.
		return fmt.Errorf("unexpected disposal dependency %s", method)
	}
	body, err := proto.Marshal(observed)
	if err != nil {
		return err
	}
	out, ok := result.(proto.Message)
	if !ok {
		return fmt.Errorf("untyped fact result %T", result)
	}
	return proto.Unmarshal(body, out)
}

func disposalOwnedManager(t *testing.T) (*Manager, *disposalOwnedPeer) {
	t.Helper()
	inv, world, journal, source, item := disposalFactualFixture()
	p := &disposalOwnedPeer{inventory: proto.Clone(inv).(*inventoryv1.State), world: proto.Clone(world).(*worldv1.SnapshotResponse),
		journal: proto.Clone(journal).(*questsv1.JournalResponse), source: proto.Clone(source).(*inventoryv1.ItemRef), item: proto.Clone(item).(*tradev1.ItemDetails), fail: map[string]error{}}
	registry, err := moddisc.Load(filepath.Join("..", "..", "..", "modules"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(registry, nil, nil)
	m.started, m.dir = true, "/private/"+inv.SessionId
	m.session.Identity = modsession.Identity{CharacterGUID: inv.SelfGuid}
	m.session.SendPacket = func(context.Context, uint32, []byte) error {
		p.writes++
		return errors.New("read-only disposal wrote a packet")
	}
	conn, err := grpc.NewClient("passthrough:///disposal-owned-facts", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithUnaryInterceptor(p.invoke))
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range m.instances {
		inst.healthy = true
	}
	for _, name := range []string{"buddy-world", "buddy-inventory", "buddy-quests"} {
		m.instances[name].conn = conn
	}
	t.Cleanup(func() { m.cancel(); conn.Close() })
	return m, p
}

func disposalOwnedObject(t *testing.T, message proto.Message) map[string]any {
	t.Helper()
	raw, err := protojson.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func disposalOwnedInput(t *testing.T, p *disposalOwnedPeer) map[string]any {
	t.Helper()
	return map[string]any{"sessionId": p.inventory.SessionId, "selfGuid": strconv.FormatUint(p.inventory.SelfGuid, 10),
		"kind": "owned_disposal", "mode": "gray_armor_destroy", "item": disposalOwnedObject(t, p.item), "source": disposalOwnedObject(t, p.source),
		"expected": map[string]any{"inventoryRevision": strconv.FormatUint(p.world.InventoryRevision, 10),
			"equipmentRevision": strconv.FormatUint(p.world.SelfEquipmentRevision, 10), "questLogRevision": strconv.FormatUint(p.world.SelfQuestLogRevision, 10)}}
}

func disposalOwnedRequest(t *testing.T, p *disposalOwnedPeer, input map[string]any) *modv1.InvokeModuleRequest {
	t.Helper()
	return disposalTestRequest(t, map[string]any{"sessionId": p.inventory.SessionId, "selfGuid": strconv.FormatUint(p.inventory.SelfGuid, 10), "input": input})
}

func disposalOwnedRead(t *testing.T, m *Manager, request *modv1.InvokeModuleRequest) map[string]any {
	t.Helper()
	response, err := m.invokeModule(context.Background(), "buddy-trade", request)
	value := &structpb.Struct{}
	if err != nil || response.GetResult().UnmarshalTo(value) != nil {
		t.Fatal("owned disposal callback failed", response, err)
	}
	return value.AsMap()
}

func disposalOwnedStart(t *testing.T, m *Manager, source string) *SessionPolicy {
	t.Helper()
	host, err := m.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.close)
	return host
}

func disposalOwnedTrace() []string {
	return []string{worldv1.BuddyWorld_Snapshot_FullMethodName, inventoryv1.BuddyInventory_RawGearFacts_FullMethodName, questsv1.BuddyQuests_Journal_FullMethodName}
}

func TestOwnedDisposalCollectsOnlyRawFactsAndOverwritesLuaWitness(t *testing.T) {
	m, p := disposalOwnedManager(t)
	source := disposalTestSource(`disposal_evaluation=function(input)
 return {status="eligible",observed={mode=input.mode,sessionId=input.sessionId,selfGuid=input.selfGuid,
 inventoryRevision=input.context.inventory.inventoryRevision,sourceGuid=input.context.carriedSource.itemGuid,
 destination=input.context.equipmentCandidates[1].destination},
 nativeContext={sessionId="spoof",selfGuid="999",source={itemGuid="1"},world={selfEquipmentRevision="0"}}} end,`)
	host := disposalOwnedStart(t, m, source)
	before, err := host.vm.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	input := disposalOwnedInput(t, p)
	input["mode"], p.calls = "gray_armor_destroy", nil
	result := disposalOwnedRead(t, m, disposalOwnedRequest(t, p, input))
	decision := result["decision"].(map[string]any)
	observed := decision["observed"].(map[string]any)
	if result["available"] != true || result["revision"] != host.vm.Status().Revision || decision["status"] != "eligible" || observed["mode"] != "gray_armor_destroy" ||
		observed["sessionId"] != p.inventory.SessionId || observed["selfGuid"] != "4" || observed["inventoryRevision"] != "20" || observed["sourceGuid"] != strconv.FormatUint(p.source.ItemGuid, 10) {
		t.Fatal("native factual frame was not passed to accepted export", result)
	}
	destination := observed["destination"].(map[string]any)
	if destination["eligible"] != false || destination["blockedReason"] != p.inventory.Items[0].GearFacts.Destinations[0].BlockedReason {
		t.Fatal("host filtered or invented physical destination eligibility", destination)
	}
	witness := decision["nativeContext"].(map[string]any)
	world := witness["world"].(map[string]any)
	carried := witness["source"].(map[string]any)
	if len(witness) != 4 || witness["sessionId"] != p.inventory.SessionId || witness["selfGuid"] != "4" ||
		carried["itemGuid"] != strconv.FormatUint(p.source.ItemGuid, 10) || carried["bag"] != float64(p.source.Bag) || carried["slot"] != float64(p.source.Slot) ||
		carried["entry"] != float64(p.source.Entry) || carried["stackCount"] != float64(p.source.StackCount) || carried["metadataKnown"] != true || world["inventoryRevision"] != "20" ||
		world["selfEquipmentRevision"] != "21" || world["selfQuestLogRevision"] != "22" {
		t.Fatal("Lua replaced the host-owned witness", witness)
	}
	rawWorld := disposalOwnedObject(t, p.world)
	if !reflect.DeepEqual(world["selfEquipment"], rawWorld["selfEquipment"]) || !reflect.DeepEqual(world["selfQuestLog"], rawWorld["selfQuestLog"]) ||
		len(world["selfEquipment"].([]any)) != 19 || len(world["selfQuestLog"].([]any)) != 25 {
		t.Fatal("host witness lost actual complete raw vectors", world)
	}
	for key := range world {
		switch key {
		case "self", "inventoryKnown", "inventoryRevision", "selfEquipmentKnown", "selfEquipmentRevision", "selfEquipment", "selfQuestLogKnown", "selfQuestLogRevision", "selfQuestLog":
		default:
			t.Fatal("unrelated World state leaked into exact witness", key)
		}
	}
	if !reflect.DeepEqual(p.calls, disposalOwnedTrace()) || p.writes != 0 {
		t.Fatal("collector used Inventory.state/Trade or a game write", p.calls, p.writes)
	}
	assertFollowFallbackState(t, host, before)
	if len(host.vm.Outstanding()) != 0 {
		t.Fatal("read-only factual callback allocated action authority")
	}
}

func TestOwnedDisposalActualMiscellaneousDoesNotRequireArmorDestinations(t *testing.T) {
	m, p := disposalOwnedManager(t)
	p.item = &tradev1.ItemDetails{ItemId: p.source.Entry, Available: true, ItemClass: 15, RawTemplateFacts: &tradev1.RawTemplateFacts{}}
	p.inventory.InventoryBags[0].Slots[3].Durability, p.inventory.InventoryBags[0].Slots[3].MaxDurability = 0, 0
	p.inventory.Items[0].GearFacts.Durability, p.inventory.Items[0].GearFacts.MaxDurability = 0, 0
	p.inventory.Items[0].GearFacts.Destinations = nil
	p.inventory.GearContext = nil
	host := disposalOwnedStart(t, m, followFallbackBundle(t))
	for _, test := range []struct{ mode, want string }{{"misc_destroy", "eligible"}, {"vendor_sale", "keep"}} {
		input := disposalOwnedInput(t, p)
		input["mode"], p.calls = test.mode, nil
		result := disposalOwnedRead(t, m, disposalOwnedRequest(t, p, input))
		decision := result["decision"].(map[string]any)
		if result["available"] != true || result["revision"] != host.vm.Status().Revision || decision["status"] != test.want || decision["nativeContext"] == nil ||
			!reflect.DeepEqual(p.calls, disposalOwnedTrace()) || p.writes != 0 {
			t.Fatal("ordinary miscellaneous item incorrectly required armor facts or lost Lua mode decision", test.mode, result, p.calls, p.writes)
		}
	}
	p.calls = nil
	if _, err := m.invokeModule(context.Background(), "buddy-trade", disposalOwnedRequest(t, p, disposalOwnedInput(t, p))); status.Code(err) != codes.FailedPrecondition ||
		!reflect.DeepEqual(p.calls, disposalOwnedTrace()) || p.writes != 0 {
		t.Fatal("gray armor classification accepted absent physical destinations", err, p.calls, p.writes)
	}
}

func TestOwnedDisposalUsesCurrentAcceptedSourceWithoutCallerContextAuthority(t *testing.T) {
	m, p := disposalOwnedManager(t)
	source := disposalTestSource(`disposal_evaluation=function(input)return {status="eligible"}end,`)
	host := disposalOwnedStart(t, m, source)
	request := disposalOwnedRequest(t, p, disposalOwnedInput(t, p))
	if result := disposalOwnedRead(t, m, request); result["decision"].(map[string]any)["status"] != "eligible" {
		t.Fatal(result)
	}
	old := host.vm.Status().Revision
	revision, err := host.Reload(context.Background(), strings.Replace(source, `status="eligible"`, `status="keep"`, 1), old)
	if err != nil || revision == old {
		t.Fatal("source edit did not replace accepted export", revision, err)
	}
	p.calls = nil
	result := disposalOwnedRead(t, m, request)
	if result["revision"] != revision || result["decision"].(map[string]any)["status"] != "keep" || !reflect.DeepEqual(p.calls, disposalOwnedTrace()) {
		t.Fatal("owned callback used stale classification or lost fresh facts", result, p.calls)
	}
	for _, key := range []string{"context", "nativeContext", "eligible"} {
		input := disposalOwnedInput(t, p)
		input[key], p.calls = map[string]any{"eligible": true}, nil
		if _, err := m.invokeModule(context.Background(), "buddy-trade", disposalOwnedRequest(t, p, input)); status.Code(err) != codes.FailedPrecondition || len(p.calls) != 0 {
			t.Fatal("caller-supplied fact or eligibility bypassed exact owned shape", key, err, p.calls)
		}
	}
}

func TestOwnedDisposalRejectsMalformedInputBeforeAnyFactRead(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"unknown_mode":          func(input map[string]any) { input["mode"] = "destroy_anything" },
		"source_array":          func(input map[string]any) { input["source"] = []any{} },
		"source_unknown_field":  func(input map[string]any) { input["source"].(map[string]any)["eligible"] = true },
		"source_missing_guid":   func(input map[string]any) { delete(input["source"].(map[string]any), "itemGuid") },
		"source_entry_mismatch": func(input map[string]any) { input["source"].(map[string]any)["entry"] = float64(99000) },
		"item_array":            func(input map[string]any) { input["item"] = []any{} },
		"item_unknown_field":    func(input map[string]any) { input["item"].(map[string]any)["eligible"] = true },
		"expected_array":        func(input map[string]any) { input["expected"] = []any{} },
		"expected_extra":        func(input map[string]any) { input["expected"].(map[string]any)["other"] = "1" },
		"expected_number":       func(input map[string]any) { input["expected"].(map[string]any)["inventoryRevision"] = float64(20) },
		"expected_zero":         func(input map[string]any) { input["expected"].(map[string]any)["equipmentRevision"] = "0" },
		"expected_leading_zero": func(input map[string]any) { input["expected"].(map[string]any)["questLogRevision"] = "022" },
		"expected_overflow": func(input map[string]any) {
			input["expected"].(map[string]any)["inventoryRevision"] = "18446744073709551616"
		},
	} {
		t.Run(name, func(t *testing.T) {
			m, p := disposalOwnedManager(t)
			disposalOwnedStart(t, m, disposalTestSource(`disposal_evaluation=function(input)return {status="eligible"}end,`))
			input := disposalOwnedInput(t, p)
			mutate(input)
			if _, err := m.invokeModule(context.Background(), "buddy-trade", disposalOwnedRequest(t, p, input)); status.Code(err) != codes.FailedPrecondition || len(p.calls) != 0 || p.writes != 0 {
				t.Fatal("malformed owned request reached native dependencies", err, p.calls, p.writes)
			}
		})
	}
	m, p := disposalOwnedManager(t)
	disposalOwnedStart(t, m, disposalTestSource(`disposal_evaluation=function(input)return {status="eligible"}end,`))
	request := disposalOwnedRequest(t, p, disposalOwnedInput(t, p))
	if _, err := m.invokeModule(context.Background(), "buddy-inventory", request); status.Code(err) != codes.PermissionDenied || len(p.calls) != 0 {
		t.Fatal("foreign module collected owned facts", err, p.calls)
	}
	input := disposalOwnedInput(t, p)
	input["sessionId"] = "previous-body"
	if _, err := m.invokeModule(context.Background(), "buddy-trade", disposalOwnedRequest(t, p, input)); status.Code(err) != codes.InvalidArgument || len(p.calls) != 0 {
		t.Fatal("nested session mismatch read native facts", err, p.calls)
	}
	input = disposalOwnedInput(t, p)
	input["selfGuid"] = "5"
	foreign := disposalTestRequest(t, map[string]any{"sessionId": p.inventory.SessionId, "selfGuid": "5", "input": input})
	if _, err := m.invokeModule(context.Background(), "buddy-trade", foreign); status.Code(err) != codes.FailedPrecondition || len(p.calls) != 0 {
		t.Fatal("foreign owned body read native facts", err, p.calls)
	}
}

func TestOwnedDisposalRejectsChangedBeforeFrameAndUnmatchedNativeFacts(t *testing.T) {
	for name, test := range map[string]struct {
		mutate func(*disposalOwnedPeer)
		reads  int
	}{
		"world_body":                      {func(p *disposalOwnedPeer) { p.world.Self.Guid++ }, 1},
		"inventory_revision":              {func(p *disposalOwnedPeer) { p.world.InventoryRevision++ }, 1},
		"equipment_revision":              {func(p *disposalOwnedPeer) { p.world.SelfEquipmentRevision++ }, 1},
		"quest_revision":                  {func(p *disposalOwnedPeer) { p.world.SelfQuestLogRevision++ }, 1},
		"cache_body":                      {func(p *disposalOwnedPeer) { p.inventory.SelfGuid++ }, 2},
		"cache_session":                   {func(p *disposalOwnedPeer) { p.inventory.SessionId = "other-body" }, 2},
		"cache_inventory_revision":        {func(p *disposalOwnedPeer) { p.inventory.InventoryRevision++ }, 3},
		"cache_equipment_revision":        {func(p *disposalOwnedPeer) { p.inventory.EquipmentRevision++ }, 3},
		"cache_unknown":                   {func(p *disposalOwnedPeer) { p.inventory.InventoryKnown = false }, 3},
		"cache_closing":                   {func(p *disposalOwnedPeer) { p.inventory.Closing = true }, 3},
		"cache_no_gear_facts":             {func(p *disposalOwnedPeer) { p.inventory.Items[0].GearFacts = nil }, 3},
		"cache_old_shape":                 {func(p *disposalOwnedPeer) { p.inventory.Items = nil; p.inventory.GearContext = nil }, 3},
		"source_changed":                  {func(p *disposalOwnedPeer) { p.inventory.InventoryBags[0].Slots[3].StackCount++ }, 3},
		"same_revision_equipment_changed": {func(p *disposalOwnedPeer) { p.world.SelfEquipment[4].Durability++ }, 3},
		"journal_session":                 {func(p *disposalOwnedPeer) { p.journal.SessionId = "other-body" }, 3},
		"journal_revision":                {func(p *disposalOwnedPeer) { p.journal.QuestLogRevision++ }, 3},
	} {
		t.Run(name, func(t *testing.T) {
			m, p := disposalOwnedManager(t)
			disposalOwnedStart(t, m, disposalTestSource(`disposal_evaluation=function(input)error("unmatched frame reached Lua")end,`))
			request := disposalOwnedRequest(t, p, disposalOwnedInput(t, p))
			test.mutate(p)
			_, err := m.invokeModule(context.Background(), "buddy-trade", request)
			if status.Code(err) != codes.FailedPrecondition || strings.Contains(status.Convert(err).Message(), "unmatched frame reached Lua") ||
				!reflect.DeepEqual(p.calls, disposalOwnedTrace()[:test.reads]) || p.writes != 0 {
				t.Fatal("unmatched native frame reached classification or wrong dependency", err, p.calls, p.writes)
			}
		})
	}
}

func TestOwnedDisposalUnavailableRawFactsNeverFallsBackToInventoryState(t *testing.T) {
	for _, stop := range []struct {
		method string
		code   codes.Code
		reads  int
	}{
		{worldv1.BuddyWorld_Snapshot_FullMethodName, codes.Unavailable, 1},
		{inventoryv1.BuddyInventory_RawGearFacts_FullMethodName, codes.Unimplemented, 2},
		{inventoryv1.BuddyInventory_RawGearFacts_FullMethodName, codes.FailedPrecondition, 2},
		{questsv1.BuddyQuests_Journal_FullMethodName, codes.Unavailable, 3},
	} {
		t.Run(fmt.Sprint(stop.method, stop.code), func(t *testing.T) {
			m, p := disposalOwnedManager(t)
			disposalOwnedStart(t, m, disposalTestSource(`disposal_evaluation=function(input)error("missing facts reached Lua")end,`))
			p.fail[stop.method] = status.Error(stop.code, "old or unavailable native fact producer")
			_, err := m.invokeModule(context.Background(), "buddy-trade", disposalOwnedRequest(t, p, disposalOwnedInput(t, p)))
			if status.Code(err) != codes.FailedPrecondition || strings.Contains(status.Convert(err).Message(), "missing facts reached Lua") ||
				!reflect.DeepEqual(p.calls, disposalOwnedTrace()[:stop.reads]) || p.writes != 0 {
				t.Fatal("unavailable fact producer caused fallback, classification or write", err, p.calls, p.writes)
			}
		})
	}
}

func TestOwnedDisposalSourceFailureAndSessionReplacementDiscardCollectedWitness(t *testing.T) {
	for _, failure := range []string{"export_error", "session_replaced", "canceled"} {
		t.Run(failure, func(t *testing.T) {
			m, p := disposalOwnedManager(t)
			source := disposalTestSource(`disposal_evaluation=function(input)return {status="eligible"}end,`)
			if failure == "export_error" {
				source = disposalTestSource(`disposal_evaluation=function(input)error("accepted disposal export failed")end,`)
			}
			host := disposalOwnedStart(t, m, source)
			before, err := host.vm.State(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p.before = func(method string) {
				if method == questsv1.BuddyQuests_Journal_FullMethodName {
					if failure == "session_replaced" {
						m.dir = "/private/replaced-disposal-body"
					}
					if failure == "canceled" {
						cancel()
					}
				}
			}
			response, err := m.invokeModule(ctx, "buddy-trade", disposalOwnedRequest(t, p, disposalOwnedInput(t, p)))
			if err == nil || response != nil || !reflect.DeepEqual(p.calls, disposalOwnedTrace()) || p.writes != 0 {
				t.Fatal("failed or stale classification returned a usable witness", response, err, p.calls, p.writes)
			}
			assertFollowFallbackState(t, host, before)
		})
	}
}

func TestOwnedDisposalMissingCurrentExportSkipsUnavailablePeersAndFencesPublicationGap(t *testing.T) {
	for _, export := range []string{"", `disposal_evaluation=true,`} {
		m, p := disposalOwnedManager(t)
		source := disposalTestSource(export)
		host := disposalOwnedStart(t, m, source)
		for _, method := range disposalOwnedTrace() {
			p.fail[method] = status.Error(codes.Unavailable, "no facts should be queried without the accepted export")
		}
		request := disposalOwnedRequest(t, p, disposalOwnedInput(t, p))
		result := disposalOwnedRead(t, m, request)
		if result["available"] != false || result["revision"] != host.vm.Status().Revision || len(p.calls) != 0 || p.writes != 0 {
			t.Fatal("unavailable accepted export queried peers or invented availability", result, p.calls, p.writes)
		}
		changed := disposalTestSource(`disposal_evaluation=function(input)return {status="eligible"}end,`)
		if _, err := host.vm.Reload(context.Background(), changed, host.vm.Status().Revision); err != nil {
			t.Fatal(err)
		}
		if _, err := m.invokeModule(context.Background(), "buddy-trade", request); status.Code(err) != codes.FailedPrecondition || len(p.calls) != 0 {
			t.Fatal("unpublished source revived stale availability or queried native facts", err, p.calls)
		}
	}
	m, p := disposalOwnedManager(t)
	result := disposalOwnedRead(t, m, disposalOwnedRequest(t, p, disposalOwnedInput(t, p)))
	if result["available"] != false || result["revision"] != "" || len(p.calls) != 0 || p.writes != 0 {
		t.Fatal("missing session policy queried native facts", result, p.calls, p.writes)
	}
}

func TestPureDisposalCallbackCannotManufactureNativeContext(t *testing.T) {
	m, p := disposalOwnedManager(t)
	disposalOwnedStart(t, m, disposalTestSource(`disposal_evaluation=function(input)return {status="eligible",nativeContext={sessionId="spoof",selfGuid="999"}}end,`))
	input := disposalTestInput(p.inventory.SessionId)
	result := disposalOwnedRead(t, m, disposalOwnedRequest(t, p, input))
	decision := result["decision"].(map[string]any)
	if result["available"] != true || decision["status"] != "eligible" || decision["nativeContext"] != nil || len(p.calls) != 0 || p.writes != 0 {
		t.Fatal("pure classification manufactured a native factual witness", result, p.calls, p.writes)
	}
}
