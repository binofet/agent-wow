package modrt

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

type thermapluggPolicyFixture struct {
	manager *Manager
	host    *SessionPolicy
	source  string
	sid     string
}

func newThermapluggPolicyFixture(t *testing.T) *thermapluggPolicyFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bundle, err := exec.CommandContext(ctx, "python3", "-B", filepath.Join("..", "..", "..", "dev", "encounter-bundle")).Output()
	if err != nil {
		t.Fatal("actual complete encounter bundle did not compose", err)
	}
	helper, err := os.ReadFile(filepath.Join("..", "..", "..", "behaviors", "encounter-buttons.lua"))
	if err != nil || !strings.Contains(string(bundle), string(helper)) || !strings.Contains(behaviors.StarterSpellSource, string(helper)) {
		t.Fatal("full bundle or maintained starter source omitted the actual button helper", err)
	}
	const sid = "thermaplugg-policy"
	m := catalogManager(t, 4, sid)
	t.Cleanup(m.cancel)
	m.session.SendPacket = func(context.Context, uint32, []byte) error {
		t.Fatal("pure Thermaplugg callback attempted a physical packet")
		return nil
	}
	m.session.SendScopedPacket = func(context.Context, uint32, []byte, *modv1.TransportScope) error {
		t.Fatal("pure Thermaplugg callback attempted a scoped physical packet")
		return nil
	}
	// Semantic assertions use the complete accepted source at100ms. Default
	// value/node limits remain; native selector tests own the stock5ms proof.
	host, err := m.StartPolicyWithOptions(context.Background(), string(bundle), behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.close)
	if _, err := host.Enqueue(behavior.Event{Kind: "fixture_thermaplugg_ingress", Value: map[string]any{"unchanged": true}}); err != nil {
		t.Fatal(err)
	}
	return &thermapluggPolicyFixture{manager: m, host: host, source: string(bundle), sid: sid}
}

func thermapluggPolicyJSON(t *testing.T, message proto.Message) map[string]any {
	t.Helper()
	raw, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func thermapluggPolicyGUID(entry uint32, low uint64) uint64 {
	return 0xf110000000000000 | uint64(entry)<<24 | low
}

func thermapluggPolicyInput(t *testing.T, sid string) map[string]any {
	t.Helper()
	self := &worldv1.PlayerState{Guid: 4, Present: true, MapKnown: true, MapId: 90, PositionKnown: true, PositionRevision: 1,
		ClassKnown: true, ClassId: 4, AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, MaxHealth: 100,
		MovementFlagsKnown: true, UnitFlagsKnown: true, ChannelObjectKnown: true, ChannelSpellKnown: true}
	owner := proto.Clone(self).(*worldv1.PlayerState)
	owner.Guid, owner.ClassId = 3, 1
	boss := &worldv1.CreatureState{Guid: 0xf130000000000000 | uint64(7800)<<24 | 20, Entry: 7800, Present: true, MapKnown: true, MapId: 90,
		AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, MaxHealth: 100, UnitFlagsKnown: true, UnitFlags: 0x80000}
	candidates := make([]any, 0, 2)
	for index, pair := range [][2]uint32{{142211, 142214}, {142210, 142215}} {
		x := float32(5 + index*15)
		face := &worldv1.GameObjectState{Guid: thermapluggPolicyGUID(pair[0], uint64(101+index)), Entry: pair[0], Present: true, MapKnown: true, MapId: 90,
			PositionKnown: true, PositionRevision: 1, X: x, TypeKnown: true, GameObjectType: 1, StateKnown: true, State: 0}
		button := proto.Clone(face).(*worldv1.GameObjectState)
		button.Guid, button.Entry, button.FlagsKnown = thermapluggPolicyGUID(pair[1], uint64(201+index)), pair[1], true
		candidates = append(candidates, map[string]any{"face": thermapluggPolicyJSON(t, face), "button": thermapluggPolicyJSON(t, button)})
	}
	return map[string]any{"selection": "thermaplugg_buttons", "sessionId": sid, "selfGuid": "4", "visitId": "19", "nowMs": float64(10000),
		"independent": false, "assisting": true, "self": thermapluggPolicyJSON(t, self), "owner": thermapluggPolicyJSON(t, owner),
		"clientPosition": map[string]any{"x": float64(0), "y": float64(0), "z": float64(0)}, "bosses": []any{thermapluggPolicyJSON(t, boss)},
		"observationsKnown": true, "candidates": candidates, "feedback": "", "memory": map[string]any{},
		"current": map[string]any{"faceGuid": "0", "buttonGuid": "0", "faceEntry": float64(0), "buttonEntry": float64(0), "approaching": false,
			"attempt": map[string]any{"attempted": false, "pending": false, "written": false, "unknown": false}}}
}

func (x *thermapluggPolicyFixture) callback(t *testing.T, input map[string]any) (map[string]any, error) {
	t.Helper()
	before, err := x.host.vm.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	original, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := starterSpellHostRequest(t, map[string]any{"sessionId": x.sid, "selfGuid": "4", "input": input})
	response, callErr := x.manager.invokeModule(context.Background(), actionsModule, request)
	assertFollowFallbackState(t, x.host, before)
	after, err := json.Marshal(input)
	if err != nil || string(after) != string(original) {
		t.Fatal("pure callback mutated supplied native facts or attempt history", err)
	}
	if callErr != nil {
		return nil, callErr
	}
	value := &structpb.Struct{}
	if response.GetResult().UnmarshalTo(value) != nil {
		t.Fatal("actual private callback lost its typed result", response)
	}
	fields := value.AsMap()
	if len(fields) != 3 || fields["revision"] != x.host.vm.Status().Revision {
		t.Fatal("callback returned another accepted source identity", fields, x.host.vm.Status())
	}
	decision, ok := fields["decision"].(map[string]any)
	if !ok {
		t.Fatal("callback omitted its object decision", fields)
	}
	if fields["available"] == false {
		if len(decision) != 0 {
			t.Fatal("missing current export returned an old choice", fields)
		}
		return nil, nil
	}
	for key := range decision {
		switch key {
		case "kind", "faceGuid", "buttonGuid", "faceEntry", "buttonEntry", "memory":
		default:
			t.Fatal("selector returned an unrecognized decision field", decision)
		}
	}
	if len(decision) != 6 || decision["memory"] == nil {
		t.Fatal("selector omitted its exact pair or returned memory", decision)
	}
	return decision, nil
}

func (x *thermapluggPolicyFixture) expect(t *testing.T, input map[string]any, kind string, pairIndex int) map[string]any {
	t.Helper()
	decision, err := x.callback(t, input)
	if err != nil || decision == nil || decision["kind"] != kind {
		t.Fatal("actual full-bundle selector chose another lifecycle", kind, decision, err)
	}
	if pairIndex >= 0 {
		row := input["candidates"].([]any)[pairIndex].(map[string]any)
		face, button := row["face"].(map[string]any), row["button"].(map[string]any)
		if decision["faceGuid"] != face["guid"] || decision["buttonGuid"] != button["guid"] || decision["faceEntry"] != face["entry"] || decision["buttonEntry"] != button["entry"] {
			t.Fatal("choice lost the exact observed device pair", decision, row)
		}
	}
	return decision
}

func (x *thermapluggPolicyFixture) reload(t *testing.T, from, to string) string {
	t.Helper()
	if strings.Count(x.source, from) != 1 {
		t.Fatal("actual production source tuning anchor is missing or ambiguous", from)
	}
	changed := strings.Replace(x.source, from, to, 1)
	before, session := x.host.vm.Status().Revision, x.host.vm
	revision, err := x.host.Reload(context.Background(), changed, before)
	if err != nil || revision == before || x.host.vm != session || x.host.vm.Status().SessionID != x.sid {
		t.Fatal("same-session accepted source CAS failed", revision, err)
	}
	x.source = changed
	return revision
}

func TestPolicyThermapluggFullBundleCallbackAndSameSessionTuningCAS(t *testing.T) {
	x := newThermapluggPolicyFixture(t)
	input := thermapluggPolicyInput(t, x.sid)
	input["observationsKnown"], input["candidates"] = false, []any{}
	x.expect(t, input, "observe", -1) // Cheap active Rogue observation requests full facts.
	input = thermapluggPolicyInput(t, x.sid)
	x.expect(t, input, "choose", 0)
	tied := thermapluggPolicyInput(t, x.sid)
	tied["candidates"].([]any)[1].(map[string]any)["button"].(map[string]any)["x"] = float64(5)
	x.expect(t, tied, "choose", 0) // Equal distance preserves observed pair order.
	locked := thermapluggPolicyInput(t, x.sid)
	locked["candidates"].([]any)[0].(map[string]any)["button"].(map[string]any)["flags"] = float64(2)
	x.expect(t, locked, "choose", 1)
	initial := x.host.vm.Status().Revision
	revision := x.reload(t, "role_classes={[4]=true},prefer_nearest=true", "role_classes={[4]=true},prefer_nearest=false")
	x.expect(t, input, "choose", 1)
	accepted := x.host.starterSpell.Load()
	if _, err := x.host.Reload(context.Background(), x.source, initial); !errors.Is(err, behavior.ErrRevisionConflict) || x.host.starterSpell.Load() != accepted {
		t.Fatal("stale selector CAS replaced current source", err)
	}
	input["self"].(map[string]any)["classId"] = float64(8)
	x.expect(t, input, "idle", -1)
	revision = x.reload(t, "role_classes={[4]=true}", "role_classes={[8]=true}")
	x.expect(t, input, "choose", 1)
	input["self"] = nil
	x.expect(t, input, "idle", -1)
	raw, err := x.manager.InvokeJSON(context.Background(), "session.policy_source", json.RawMessage(`{"sessionId":"`+x.sid+`","characterGuid":"4"}`))
	var view policySourceResponse
	if err != nil || json.Unmarshal(raw, &view) != nil || view.Revision != revision || !view.StarterSpell.Available ||
		view.StarterSpell.EntryPoint != policyStarterSpellEntryPoint || view.StarterSpell.Revision != revision {
		t.Fatal("reflected accepted source did not advertise the current pure export", string(raw), err)
	}
}

func TestPolicyThermapluggUnknownAttemptSurvivesMemoryResetAndScopeChanges(t *testing.T) {
	x := newThermapluggPolicyFixture(t)
	input := thermapluggPolicyInput(t, x.sid)
	row := input["candidates"].([]any)[0].(map[string]any)
	face, button := row["face"].(map[string]any), row["button"].(map[string]any)
	current := input["current"].(map[string]any)
	for key, value := range map[string]any{"faceGuid": face["guid"], "buttonGuid": button["guid"], "faceEntry": face["entry"], "buttonEntry": button["entry"]} {
		current[key] = value
	}
	attempt := map[string]any{"attempted": true, "pending": true, "written": false, "unknown": true, "sessionId": x.sid, "selfGuid": "4", "visitId": "19",
		"sentAtMs": float64(9000), "faceGuid": face["guid"], "buttonGuid": button["guid"], "faceEntry": face["entry"], "buttonEntry": button["entry"]}
	current["attempt"] = attempt
	input["observationsKnown"], input["nowMs"] = false, float64(9799)
	x.expect(t, input, "wait", 0)
	input["nowMs"] = float64(9800)
	x.expect(t, input, "observe", 0)
	input["observationsKnown"], input["nowMs"] = true, float64(10000)
	decision := x.expect(t, input, "wait", 0)
	input["memory"], input["observationsKnown"], input["nowMs"] = decision["memory"], false, float64(10799)
	x.expect(t, input, "wait", 0)
	input["nowMs"] = float64(10800)
	x.expect(t, input, "observe", 0)
	input["memory"], input["observationsKnown"], input["nowMs"] = map[string]any{}, true, float64(1000000)
	x.reload(t, "role_classes={[4]=true},prefer_nearest=true", "role_classes={[4]=true},prefer_nearest=false")
	x.expect(t, input, "wait", 0) // Neither a timer, memory reset nor a source edit reuses the button.
	for _, field := range []string{"sessionId", "selfGuid", "visitId"} {
		old := attempt[field]
		attempt[field] = map[string]string{"sessionId": "old-session", "selfGuid": "5", "visitId": "18"}[field]
		input["memory"] = map[string]any{}
		x.expect(t, input, "wait", -1)
		attempt[field] = old
	}
	face["state"] = float64(1)
	x.expect(t, input, "confirmed", 0) // Only the actual exact face-closure observation confirms.
	x.manager.session.Identity.CharacterGUID = 5
	if _, err := x.callback(t, input); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("private callback accepted a replaced current body", err)
	}
}

func TestPolicyThermapluggCurrentInvalidFailedAndMissingExportNeverReuseOldChoice(t *testing.T) {
	x := newThermapluggPolicyFixture(t)
	input := thermapluggPolicyInput(t, x.sid)
	x.expect(t, input, "choose", 0)
	x.reload(t, "role_classes={[4]=true},prefer_nearest=true", `role_classes={[4]=true},prefer_nearest="invalid"`)
	x.expect(t, input, "wait", -1)
	x.reload(t, `role_classes={[4]=true},prefer_nearest="invalid"`, "role_classes={[4]=true},prefer_nearest=true")
	x.expect(t, input, "choose", 0)
	failed := x.reload(t, "return encounter_button_policy.decide(input)", `error("active Thermaplugg selector failure")`)
	for range 2 {
		decision, err := x.callback(t, input)
		if decision != nil || status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "active Thermaplugg selector failure") {
			t.Fatal("failed current handler revived an old choice", decision, err)
		}
	}
	if metadata := x.host.starterSpellMetadata(); metadata.Revision != failed || !strings.Contains(metadata.LastError, "active Thermaplugg selector failure") {
		t.Fatal("current selector fault lost its exact source revision", metadata)
	}
	x.reload(t, "starter_spell=starter_spells and function(input)", "starter_spell_absent=starter_spells and function(input)")
	if decision, err := x.callback(t, input); err != nil || decision != nil {
		t.Fatal("missing current export returned an old Thermaplugg decision", decision, err)
	}
	if view := x.host.starterSpellMetadata(); view.Available || view.Revision != x.host.vm.Status().Revision {
		t.Fatal("absent current export retained advertised older source", view)
	}
}
