package world

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/modules/runtime"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

type encounterReplayAction struct {
	ID, ActorGUID, Capability, TargetGUID string
	SpellID                               uint32
}

type encounterReplayFrame struct {
	ID       string
	Focus    string
	Emit     []encounterReplayAction
	Statuses map[string]string
}

type encounterReplayBody struct {
	world        *Session
	policy       *modrt.SessionPolicy
	trace        *GameplayTrace
	id, guid     string
	mana         int
	heal, damage int
	attempts     int
	knownSpells  []int
}

type encounterReplayCast struct {
	expected  encounterReplayAction
	body      *encounterReplayBody
	id        uint64
	status    string
	transport behavior.ActionOutcome
}

type encounterTransportFrame struct {
	FrameID           string          `json:"frameId"`
	TransportAttempts []GameplayWrite `json:"transportAttempts"`
}

type encounterTransportReport struct {
	Schema         string                       `json:"schema"`
	Mode           string                       `json:"mode"`
	FixtureSHA256  string                       `json:"fixtureSHA256"`
	PolicyRevision string                       `json:"policyRevision"`
	Frames         []encounterTransportFrame    `json:"frames"`
	Sessions       map[string]map[string]any    `json:"sessions"`
	SessionAliases map[string]string            `json:"sessionAliases"`
	ActionAliases  map[string]map[string]string `json:"actionAliases"`
}

func readEncounterBundle(t *testing.T) string {
	t.Helper()
	read := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	root := filepath.Join("..", "..", "behaviors")
	source := "local encounter_tactics = (function()\n" + read(filepath.Join(root, "encounter-tactics.lua")) + "\nend)()\n" +
		"local encounter_spells = (function()\n" + read(filepath.Join(root, "encounter-spells.lua")) + "\nend)()\n"
	healing := os.Getenv("WB_LIFE37_HEALING_LUA")
	if healing == "" {
		healing = filepath.Join(root, "healing.lua")
	}
	return source + "local encounter_healing = (function()\n" + read(healing) + "\nend)()\n" +
		"local encounter_self_mana = (function()\n" + read(filepath.Join(root, "self-mana.lua")) + "\nend)()\n" +
		"local encounter_buffs = (function()\n" + read(filepath.Join(root, "buffs.lua")) + "\nend)()\n" +
		"local encounter_control_recovery = (function()\n" + read(filepath.Join(root, "control-recovery.lua")) + "\nend)()\n" +
		"local encounter_support = (function()\n" + read(filepath.Join(root, "support.lua")) + "\nend)()\n" +
		"local encounter_movement = (function()\n" + read(filepath.Join(root, "encounter-movement.lua")) + "\nend)()\n" +
		"local encounter_commands = (function()\n" + read(filepath.Join(root, "encounter-commands.lua")) + "\nend)()\n" +
		"local encounter_tasks = (function()\n" + read(filepath.Join(root, "encounter-tasks.lua")) + "\nend)()\n" +
		read(filepath.Join(root, "encounter-session.lua"))
}

// TestEncounterSessionReplay runs the real persistent host against framed mock
// world peers. The resolver and inbound world/effect observations are isolated
// test fixtures. Fixture.Emit is an assertion oracle after each Step.
func TestEncounterSessionReplay(t *testing.T) {
	path := os.Getenv("WB_LIFE37_FIXTURE")
	if path == "" {
		t.Skip("set WB_LIFE37_FIXTURE and WB_LIFE37_INPUTS to the synthetic replay files")
	}
	t.Run("fixed_revision", func(t *testing.T) { runEncounterSessionReplay(t, path, false) })
	t.Run("pending_reload_and_uncertain_death", func(t *testing.T) { runEncounterSessionReplay(t, path, true) })
}

func runEncounterSessionReplay(t *testing.T, path string, exerciseReload bool) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Schema, Provenance, GroupGUID, LeaderGUID string
		MapID                                     uint32
		Actors                                    map[string]struct {
			Role, SessionID string
			KnownSpells     []int
		}
		Frames    []encounterReplayFrame
		TargetIDs map[string]string
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || fixture.Schema != "wb.lua-transition.dungeon-replay/1" ||
		fixture.Provenance != "synthetic" || len(fixture.Frames) != 14 || len(fixture.Actors) != 5 {
		t.Fatalf("expected the 14-frame fixture: %v", err)
	}
	inputs, err := os.Open(os.Getenv("WB_LIFE37_INPUTS"))
	if err != nil {
		t.Fatal(err)
	}
	defer inputs.Close()
	source := readEncounterBundle(t)
	revision := fmt.Sprintf("%x", sha256.Sum256([]byte(source)))
	report := encounterTransportReport{
		Schema: "wb.life37.gameplay-write/1", Mode: "instrumented_controller",
		FixtureSHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), PolicyRevision: revision,
		Sessions: make(map[string]map[string]any), SessionAliases: make(map[string]string),
		ActionAliases: make(map[string]map[string]string),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	bodies := make(map[string]*encounterReplayBody)
	order := make([]string, 0, len(fixture.Actors))
	for guid := range fixture.Actors {
		order = append(order, guid)
	}
	slices.Sort(order)
	for _, actor := range order {
		spec := fixture.Actors[actor]
		body := startEncounterReplayBody(t, ctx, source, actor, fixture.MapID)
		body.knownSpells = spec.KnownSpells
		if spec.Role == "priest" && slices.Contains(spec.KnownSpells, 2050) {
			body.mana, body.heal = 45, 2050
		}
		if spec.Role == "priest" && slices.Contains(spec.KnownSpells, 585) {
			body.damage = 585
		}
		bodies[actor], report.SessionAliases[spec.SessionID] = body, body.id
	}

	casts := make(map[string]*encounterReplayCast)
	auras := make(map[string]map[string]any)
	reader := bufio.NewScanner(inputs)
	for _, expected := range fixture.Frames {
		if !reader.Scan() {
			t.Fatalf("missing input %s: %v", expected.ID, reader.Err())
		}
		var input map[string]any
		decoder := json.NewDecoder(bytes.NewReader(reader.Bytes()))
		decoder.UseNumber()
		if err := decoder.Decode(&input); err != nil || input["frameId"] != expected.ID {
			t.Fatalf("wrong input for %s: %v", expected.ID, err)
		}
		now, _ := input["atMs"].(json.Number).Int64()
		receiveEncounterEffects(t, input, casts, auras)
		for label, status := range expected.Statuses {
			if cast := casts[label]; cast == nil || cast.status != status {
				t.Fatalf("%s %s: wrong correlated effect state %+v, want %s", expected.ID, label, cast, status)
			}
		}
		result := encounterTransportFrame{FrameID: expected.ID, TransportAttempts: []GameplayWrite{}}
		assertions := make(map[string]encounterReplayAction)
		for _, action := range expected.Emit {
			assertions[action.ActorGUID] = action
		}
		for _, actor := range order {
			body := bodies[actor]
			obs := map[string]any{
				"sessionId": body.id, "selfGuid": actor, "visitId": "synthetic-rfc", "mapId": fixture.MapID,
				"players": input["players"], "targets": input["targets"], "party": input["party"],
				"engagement": input["lastEngagement"], "observedOwnerFocus": input["observedOwnerFocus"],
				"plan": map[string]any{
					"id": "rfc-marked-first-pull", "groupGuid": fixture.GroupGUID, "leaderGuid": fixture.LeaderGUID,
					"killOrder": []any{"skull", "moon"}, "controls": []any{map[string]any{
						"mark": "moon", "technique": "polymorph", "casterGuid": "9103", "spellId": 118, "refresh": 4000,
					}},
				},
				"controlCapabilities": []any{map[string]any{"guid": "9103", "technique": "polymorph", "ready": true}},
				"controlObserved":     currentEncounterAuras(now, auras),
			}
			spells := make(map[string]any)
			for _, spell := range fixture.Actors[actor].KnownSpells {
				spells[strconv.Itoa(spell)] = map[string]any{"known": true, "ready": true}
			}
			obs["spells"] = spells
			if body.damage != 0 {
				obs["damage"] = map[string]any{"spellId": body.damage, "scope": "runner"}
			}
			if body.heal != 0 {
				healing, err := body.policy.Enqueue(behavior.Event{Kind: "healing_observation", Value: replayHealingObservation(body, input)})
				if err != nil {
					t.Fatal(err)
				}
				obs["healingSequence"] = strconv.FormatUint(healing, 10)
			}
			seq, err := body.policy.Enqueue(behavior.Event{Kind: "encounter_observation", Value: obs})
			if err != nil {
				t.Fatal(err)
			}
			before := body.trace.Snapshot()
			output, receipt, err := body.policy.Step(ctx, map[string]any{"observationSequence": strconv.FormatUint(seq, 10)},
				body.resolveFixtureSpell)
			if err != nil {
				t.Fatalf("%s %s: %v", expected.ID, actor, err)
			}
			assertReplayQueue(t, input, output, expected.Focus, fixture.TargetIDs["skull"], fixture.TargetIDs["protected"])
			if exerciseReload && expected.ID == "foreign_control" && actor == fixture.LeaderGUID {
				assertForeignAuraHolds(t, ctx, body, obs, fixture.TargetIDs["skull"], actor)
			}
			want := assertions[actor]
			after := body.trace.Snapshot()
			if after.Dropped != 0 || len(before.Attempts) != body.attempts {
				t.Fatalf("%s: unexpected transport activity", expected.ID)
			}
			writes := after.Attempts[body.attempts:]
			body.attempts = len(after.Attempts)
			result.TransportAttempts = append(result.TransportAttempts, writes...)
			if want.ID == "" {
				if output["action"] != nil || receipt != nil || len(writes) != 0 {
					t.Fatalf("%s %s: unexpected proposal/write %+v %+v", expected.ID, actor, output["action"], writes)
				}
				continue
			}
			cast := assertEncounterCast(t, expected.ID, body, want, output, receipt, writes, revision)
			casts[want.ID] = cast
			report.ActionAliases[want.ID] = map[string]string{"sessionId": body.id, "actionId": strconv.FormatUint(cast.id, 10)}
		}
		if exerciseReload && expected.ID == "control_cast_go" {
			mage := casts["cc-1"].body
			next, err := mage.policy.Reload(ctx, source+"\n-- replace with the control effect pending\n", revision)
			if err != nil || next == revision {
				t.Fatalf("reload failed while Polymorph waited for its aura: %v", err)
			}
		}
		if exerciseReload && expected.ID == "skull_engaged" {
			assertUncertainSkullDeathHolds(t, ctx, casts["cc-1"].body, input, fixture.TargetIDs["skull"],
				fixture.GroupGUID, fixture.LeaderGUID, fixture.MapID)
		}
		report.Frames = append(report.Frames, result)
		t.Logf("%s focus=%s writes=%d", expected.ID, expected.Focus, len(result.TransportAttempts))
	}
	if reader.Scan() || reader.Err() != nil {
		t.Fatal("unexpected extra replay input", reader.Err())
	}
	for _, actor := range order {
		body := bodies[actor]
		if err := body.world.Close(); err != nil {
			t.Fatal(err)
		}
		end := body.trace.Snapshot()
		if len(end.Attempts) != body.attempts || end.Dropped != 0 {
			t.Fatalf("%s: unexpected terminal transport %+v", actor, end)
		}
		report.Sessions[body.id] = map[string]any{"attemptCount": len(end.Attempts), "dropped": end.Dropped}
	}
	if file := os.Getenv("WB_LIFE37_ENCOUNTER_CAPTURE"); file != "" && !exerciseReload {
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertForeignAuraHolds(t *testing.T, ctx context.Context, body *encounterReplayBody, obs map[string]any, skull, leader string) {
	t.Helper()
	targets := obs["targets"].(map[string]any)
	skullTarget := targets[skull].(map[string]any)
	skullTarget["engaged"] = true
	defer func() { skullTarget["engaged"] = false }()
	obs["engagement"] = map[string]any{skull: map[string]any{"victimGuid": leader}}
	seq, err := body.policy.Enqueue(behavior.Event{Kind: "encounter_observation", Value: obs})
	if err != nil {
		t.Fatal(err)
	}
	out, receipt, err := body.policy.Step(ctx, map[string]any{"observationSequence": strconv.FormatUint(seq, 10)}, nil)
	if err != nil || receipt != nil || out["focusGuid"] != nil || out["action"] != nil {
		t.Fatalf("foreign aura unlocked an engaged Skull without the assigned caster: %+v %+v %v", out, receipt, err)
	}
}

func assertUncertainSkullDeathHolds(t *testing.T, ctx context.Context, body *encounterReplayBody, input map[string]any,
	skull, groupID, leaderID string, mapID uint32) {
	t.Helper()
	party := input["party"].(map[string]any)
	marks := party["raidTargets"].(map[string]any)
	withoutSkull := make([]any, 0, len(marks["marks"].([]any)))
	for _, raw := range marks["marks"].([]any) {
		if raw.(map[string]any)["targetGuid"] != skull {
			withoutSkull = append(withoutSkull, raw)
		}
	}
	targets := make(map[string]any)
	for key, row := range input["targets"].(map[string]any) {
		if key != skull {
			targets[key] = row
		}
	}
	obs := map[string]any{
		"sessionId": body.id, "selfGuid": body.guid, "visitId": "synthetic-rfc", "mapId": mapID,
		"players": input["players"], "targets": targets, "engagement": input["lastEngagement"],
		"party": map[string]any{"group": party["group"], "raidTargets": map[string]any{
			"complete": true, "groupGuid": groupID, "revision": "3",
			"marks": withoutSkull,
		}},
		"plan": map[string]any{"id": "rfc-marked-first-pull", "groupGuid": groupID,
			"leaderGuid": leaderID, "killOrder": []any{"skull", "moon"}},
	}
	seq, err := body.policy.Enqueue(behavior.Event{Kind: "encounter_observation", Value: obs})
	if err != nil {
		t.Fatal(err)
	}
	out, receipt, err := body.policy.Step(ctx, map[string]any{"observationSequence": strconv.FormatUint(seq, 10)}, nil)
	if err != nil || receipt != nil || out["phase"] != "waiting_for_mark_skull" || out["focusGuid"] != nil || out["action"] != nil {
		t.Fatalf("unobserved Skull death escaped the policy guard: %+v %+v %v", out, receipt, err)
	}
}

func startEncounterReplayBody(t *testing.T, ctx context.Context, source, guid string, mapID uint32) *encounterReplayBody {
	t.Helper()
	identity, err := strconv.ParseUint(guid, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	trace := NewGameplayTrace(32)
	wire := make(chan []byte, 32)
	peerDone := make(chan struct{})
	conn := peerConnection(t, func(peer *testPeer) {
		defer close(peerDone)
		if got := peer.expect(opcode.CMSGPlayerLogin); !bytes.Equal(got, binary.LittleEndian.AppendUint64(nil, identity)) {
			t.Errorf("wrong login for %s", guid)
		}
		peer.send(opcode.SMSGLoginVerifyWorld, positionBody(mapID, 1, 2, 3, .5))
		peer.sync(1)
		for {
			op, payload, err := peer.read()
			if err != nil {
				return
			}
			if op != opcode.CMSGCastSpell {
				t.Errorf("unexpected opcode 0x%x", op)
			}
			wire <- payload
		}
	})
	manager := modrt.New(nil, nil, nil)
	session, err := enter(ctx, conn, Realm{1, "Offline"}, Character{identity, "Replay"}, nil, quietTimings(),
		Options{Modules: manager, GameplayTrace: trace})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	raw, err := session.InvokeJSON(ctx, "session.describe", nil)
	var catalog modrt.SessionCatalog
	if err != nil || json.Unmarshal(raw, &catalog) != nil || catalog.CharacterGUID != guid || catalog.SessionID == "" {
		t.Fatalf("unexpected session identity: %s %v", raw, err)
	}
	policy, err := manager.StartPolicy(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	body := &encounterReplayBody{world: session, policy: policy, trace: trace, id: catalog.SessionID, guid: guid}
	t.Cleanup(func() {
		_ = session.Close()
		<-peerDone
		captured := trace.Snapshot()
		if len(wire) != len(captured.Attempts) {
			t.Errorf("%s: peer did not read all captured writes", guid)
			return
		}
		for _, attempt := range captured.Attempts {
			payload := <-wire
			if fmt.Sprintf("%x", payload) != attempt.PayloadHex {
				t.Errorf("%s: peer payload %x != trace %s", guid, payload, attempt.PayloadHex)
			}
		}
	})
	return body
}

func (body *encounterReplayBody) resolveFixtureSpell(action map[string]any) (modrt.PolicyDispatch, error) {
	target, _ := action["targetGuid"].(string)
	guid, err := strconv.ParseUint(target, 10, 64)
	spell, ok := action["spellId"].(float64)
	capability, _ := action["capability"].(string)
	switch capability {
	case "polymorph", "heal", "offense":
	default:
		return modrt.PolicyDispatch{}, errors.New("unsupported fixture mechanic")
	}
	if err != nil || guid == 0 || !ok || spell <= 0 || spell != float64(uint32(spell)) ||
		action["sessionId"] != body.id || action["selfGuid"] != body.guid ||
		!slices.Contains(body.knownSpells, int(spell)) {
		return modrt.PolicyDispatch{}, errors.New("invalid fixture spell proposal")
	}
	return modrt.PolicyDispatch{Module: "buddy-actions", Send: func(ctx context.Context) (behavior.ActionOutcome, map[string]any) {
		if err := body.world.SendPacket(ctx, opcode.CMSGCastSpell, fixtureSpellBody(byte(body.attempts+1), uint32(spell), guid)); err != nil {
			return behavior.ActionUnknown, nil
		}
		if capability == "heal" {
			body.mana -= 30 // Simulated spell cost for the next capability snapshot.
		}
		return behavior.ActionTransportAccepted, action
	}}, nil
}

func assertEncounterCast(t *testing.T, frame string, body *encounterReplayBody, want encounterReplayAction,
	output map[string]any, receipt *behavior.ActionReceipt, writes []GameplayWrite, revision string) *encounterReplayCast {
	t.Helper()
	action, _ := output["action"].(map[string]any)
	if action["capability"] != want.Capability || action["selfGuid"] != want.ActorGUID ||
		action["targetGuid"] != want.TargetGUID || action["spellId"] != float64(want.SpellID) ||
		receipt == nil || receipt.Outcome != behavior.ActionTransportAccepted || len(writes) != 1 {
		t.Fatalf("%s: wanted %+v, obtained %+v %+v %+v", frame, want, action, receipt, writes)
	}
	write := writes[0]
	origin := write.Origin
	if write.Outcome != GameplayWritten || !write.AnyBytes || write.Origin.ActionID != strconv.FormatUint(receipt.ActionID, 10) ||
		origin.SessionID != body.id || origin.Writer != "lua" || origin.Module != "buddy-actions" ||
		origin.Generation != 1 || origin.PolicyRevision != revision {
		t.Fatalf("%s: wrong authoritative transport provenance: %+v", frame, write)
	}
	return &encounterReplayCast{expected: want, body: body, id: receipt.ActionID,
		status: "submitted", transport: receipt.Outcome}
}

func receiveEncounterEffects(t *testing.T, input map[string]any, casts map[string]*encounterReplayCast, auras map[string]map[string]any) {
	t.Helper()
	now, _ := input["atMs"].(json.Number).Int64()
	events, _ := input["events"].([]any)
	for _, value := range events {
		event := value.(map[string]any)
		kind, _ := event["kind"].(string)
		target, _ := event["targetGuid"].(string)
		if kind == "aura_apply" {
			rev, _ := event["auraRevision"].(json.Number)
			ms, _ := event["remainingMs"].(json.Number).Int64()
			auras[target] = map[string]any{"active": true, "casterGuid": event["actorGuid"], "spellId": event["spellId"],
				"revision": rev.String(), "durationKnown": event["durationKnown"], "expiresAtMs": now + ms}
		}
		label, _ := event["actionId"].(string)
		cast := casts[label]
		if cast == nil || event["actorGuid"] != cast.expected.ActorGUID || target != cast.expected.TargetGUID ||
			event["spellId"] != json.Number(strconv.Itoa(int(cast.expected.SpellID))) {
			continue
		}
		switch kind {
		case "transport":
			if cast.transport != behavior.ActionTransportAccepted {
				t.Fatal("fixture transport observation was not present in real transport")
			}
		case "cast_go":
			cast.status = "cast_observed"
		case "aura_apply", "heal_effect", "damage_effect":
			if cast.status != "cast_observed" {
				t.Fatalf("%s: effect precedes cast evidence", label)
			}
			if _, err := cast.body.policy.Enqueue(behavior.Event{Kind: "fixture_effect", Receipt: &behavior.ActionReceipt{
				SessionID: cast.body.id, ActionID: cast.id, Outcome: behavior.ActionConfirmed, Payload: event,
			}}); err != nil {
				t.Fatal(err)
			}
			cast.status = "effect_observed"
		}
	}
}

func currentEncounterAuras(now int64, auras map[string]map[string]any) map[string]any {
	active := make(map[string]any)
	for target, aura := range auras {
		remaining := aura["expiresAtMs"].(int64) - now
		if remaining > 0 {
			aura["remainingMs"] = int(remaining)
			active[target] = aura
		}
	}
	return active
}

func replayHealingObservation(body *encounterReplayBody, input map[string]any) map[string]any {
	players := input["players"].(map[string]any)
	patients := make([]any, 0, len(players))
	for guid, raw := range players {
		p := raw.(map[string]any)
		if p["alive"] == true {
			patients = append(patients, map[string]any{"guid": guid, "health": p["health"], "maxHealth": p["maxHealth"], "distance": 5})
		}
	}
	return map[string]any{
		"caster": map[string]any{"guid": body.guid, "ready": true, "mana": body.mana}, "patients": patients,
		"spells": []any{map[string]any{"spellId": body.heal, "cost": 30, "amount": 45, "castMs": 1500,
			"minRange": 0, "maxRange": 40, "level": 1, "rank": 1}},
	}
}

func assertReplayQueue(t *testing.T, input, output map[string]any, focus, skull, controlled string) {
	t.Helper()
	if got, _ := output["focusGuid"].(string); got != focus {
		t.Fatalf("%s: focus %q, want %q: %+v", input["frameId"], got, focus, output)
	}
	rows, _ := output["queue"].([]any)
	observed := input["targets"].(map[string]any)
	count := 0
	for _, target := range observed {
		if target.(map[string]any)["alive"] == true {
			count++
		}
	}
	if len(rows) != count {
		t.Fatalf("%s: expected %d visible live rows, got %+v", input["frameId"], count, rows)
	}
	seen := make(map[string]bool)
	for index, raw := range rows {
		row := raw.(map[string]any)
		id := row["targetGuid"].(string)
		npc, present := observed[id].(map[string]any)
		if seen[id] || !present || npc["alive"] != true || (id == focus && row["protected"] != false) {
			t.Fatalf("%s: invalid queue row %+v", input["frameId"], row)
		}
		if id == controlled && input["frameId"] != "visible" && row["protected"] != true {
			t.Fatalf("%s: assigned control exposed", input["frameId"])
		}
		if index == 0 && (input["frameId"] == "marked" || input["frameId"] == "skull_engaged") && id != skull {
			t.Fatalf("%s: Skull lost its pre-pull priority", input["frameId"])
		}
		seen[id] = true
	}
}
