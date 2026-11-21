package behavior

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

const discoveryNPC = "17379390964339851265"

// These semantic fixtures use the actual maintained task-stack and Lua policy.
// Their explicit healthy budget is independent of the production CLI watchdog.
func discoverySource(t *testing.T) string {
	t.Helper()
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "behaviors", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	return "local tasks=(function()\n" + read("task-stack.lua") + "\nend)()\n" + read("flightpath-discovery.lua")
}

func discoveryInput() map[string]any {
	return map[string]any{
		"goal":   map[string]any{"id": "flight-discovery", "kind": "flightpath_discovery", "params": map[string]any{}},
		"body":   map[string]any{"sessionId": "flight-session", "selfGuid": "4"},
		"memory": map[string]any{}, "pending": []any{}, "now": float64(1),
		"observations": map[string]any{
			"world": map[string]any{
				"visitRevision": "7",
				"self": map[string]any{"guid": "4", "present": true, "aliveKnown": true, "alive": true,
					"positionKnown": true, "mapKnown": true, "positionRevision": "10", "unitFlagsKnown": true,
					"movementFlagsKnown": true, "channelSpellKnown": true, "channelObjectKnown": true},
				"nearbyCreatures": []any{map[string]any{"guid": discoveryNPC, "present": true,
					"aliveKnown": true, "alive": true, "positionKnown": true, "mapKnown": true,
					"npcFlags": float64(8192), "positionRevision": "11", "positionAgeMs": "100", "observationAgeMs": "2000", "x": float64(2)}},
			},
			"flightpath": map[string]any{"sessionId": "flight-session", "selfGuid": "4", "visitId": "7", "visitBound": true},
			"actions":    map[string]any{"sessionId": "flight-session", "selfGuid": "4"},
		},
	}
}

type discoveryFixture struct {
	t      *testing.T
	r      *Runtime
	input  map[string]any
	source string
}

func newDiscoveryFixture(t *testing.T) *discoveryFixture {
	t.Helper()
	r, err := NewWithOptions(SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	source := discoverySource(t)
	if _, err := r.Reload(source, ""); err != nil {
		t.Fatal(err)
	}
	return &discoveryFixture{t: t, r: r, input: discoveryInput(), source: source}
}

func (f *discoveryFixture) observation(name string) map[string]any {
	return f.input["observations"].(map[string]any)[name].(map[string]any)
}

func (f *discoveryFixture) step() map[string]any {
	f.t.Helper()
	out, err := f.r.Evaluate(f.input)
	if err != nil {
		f.t.Fatalf("discovery evaluation: %v; runtime=%+v", err, f.r.Status())
	}
	f.input["memory"] = out["memory"]
	return out
}

func discoveryAction(t *testing.T, out map[string]any, method string) map[string]any {
	t.Helper()
	a, ok := out["action"].(map[string]any)
	if !ok || a["method"] != method || out["done"] == true {
		t.Fatalf("wanted %s action, got %#v", method, out)
	}
	return a
}

func discoveryNoEffect(t *testing.T, out map[string]any) {
	t.Helper()
	if out["action"] != nil || out["resolve"] != nil || out["done"] == true {
		t.Fatalf("unexpected discovery effect: %#v", out)
	}
}

func (f *discoveryFixture) reply(action, response map[string]any) {
	f.input["last"] = map[string]any{"id": action["id"], "method": action["method"], "effect": "mutation", "response": response}
}

func (f *discoveryFixture) acquire() map[string]any {
	f.t.Helper()
	a := discoveryAction(f.t, f.step(), "buddy-actions.acquire_interaction")
	p := a["params"].(map[string]any)
	if p["owner"] != "exploration" || p["harvest"] != true || p["leaseMs"] != float64(15000) {
		f.t.Fatal("discovery lost finite guarded exploration custody", p)
	}
	f.observation("actions")["interactionOwner"] = "exploration"
	f.reply(a, map[string]any{"token": "own-flight-token", "state": f.observation("actions")})
	return discoveryAction(f.t, f.step(), "buddy-actions.query_flightpath")
}

func (f *discoveryFixture) operation(action map[string]any, revision, phase string) map[string]any {
	p := action["params"].(map[string]any)
	return map[string]any{"sessionId": p["sessionId"], "selfGuid": p["selfGuid"], "visitId": p["visitId"],
		"npcGuid": p["npcGuid"], "mode": p["mode"], "revision": revision, "phase": phase, "transportWritten": true}
}

func (f *discoveryFixture) observe(action, op map[string]any) {
	f.observation("flightpath")["revision"] = op["revision"]
	f.observation("flightpath")["operation"] = op
	f.reply(action, f.observation("flightpath"))
	discoveryNoEffect(f.t, f.step()) // Consume the exact RPC return first.
}

func (f *discoveryFixture) knownMenuQuery() map[string]any {
	f.t.Helper()
	a := f.acquire()
	if p := a["params"].(map[string]any); p["mode"] != "status" || p["expectedRevision"] != "0" || p["npcGuid"] != discoveryNPC {
		f.t.Fatal("initial status did not use current exact CAS and NPC", p)
	}
	op := f.operation(a, "1", "status_observed")
	op["statusKnown"], op["nodeKnown"] = true, true
	f.observe(a, op)
	discoveryNoEffect(f.t, f.step()) // Only the fresh read advances the stage.
	return discoveryAction(f.t, f.step(), "buddy-actions.query_flightpath")
}

func TestFlightpathDiscoveryChoosesOnlyActualInRangeFlightmasters(t *testing.T) {
	for _, name := range []string{"no_npc", "wrong_flag", "wrong_map", "far", "stale_npc", "missing_npc_revision", "unbound_visit", "body_mismatch", "explicit_other", "views_disagree"} {
		t.Run(name, func(t *testing.T) {
			f := newDiscoveryFixture(t)
			w := f.observation("world")
			npc := w["nearbyCreatures"].([]any)[0].(map[string]any)
			switch name {
			case "no_npc":
				w["nearbyCreatures"] = []any{}
			case "wrong_flag":
				npc["npcFlags"] = float64(128)
			case "wrong_map":
				npc["mapId"] = float64(1)
			case "far":
				npc["z"] = float64(6)
			case "stale_npc":
				npc["observationAgeMs"] = "2001"
			case "missing_npc_revision":
				delete(npc, "positionRevision")
			case "unbound_visit":
				f.observation("flightpath")["visitBound"] = false
			case "body_mismatch":
				f.observation("actions")["selfGuid"] = "5"
			case "explicit_other":
				f.input["goal"].(map[string]any)["params"] = map[string]any{"npcGuid": "17379390964339851266"}
			case "views_disagree":
				duplicate := cloneSessionObject(npc)
				duplicate["x"] = float64(3)
				w["requestedCreature"] = duplicate
			}
			discoveryNoEffect(t, f.step())
		})
	}
	t.Run("nearest_and_explicit_selection", func(t *testing.T) {
		f := newDiscoveryFixture(t)
		w := f.observation("world")
		other := cloneSessionObject(w["nearbyCreatures"].([]any)[0].(map[string]any))
		other["guid"], other["x"] = "17379390964339851266", float64(1)
		w["nearbyCreatures"] = append(w["nearbyCreatures"].([]any), other)
		a := f.acquire()
		if a["params"].(map[string]any)["npcGuid"] != other["guid"] {
			t.Fatal("Lua did not choose nearest actual flightmaster", a)
		}
		g := newDiscoveryFixture(t)
		g.input["goal"].(map[string]any)["params"] = map[string]any{"npcGuid": discoveryNPC}
		g.observation("world")["nearbyCreatures"] = w["nearbyCreatures"]
		if got := g.acquire()["params"].(map[string]any)["npcGuid"]; got != discoveryNPC {
			t.Fatal("explicit observed flightmaster was replaced", got)
		}
	})
}

func TestFlightpathDiscoveryKnownNodeNeedsExplicitMenuAndCleanup(t *testing.T) {
	f := newDiscoveryFixture(t)
	a := f.knownMenuQuery()
	if p := a["params"].(map[string]any); p["mode"] != "discover" || p["expectedRevision"] != "1" {
		t.Fatal("known status was mistaken for a menu", p)
	}
	op := f.operation(a, "2", "menu_observed")
	op["menuKnown"], op["sourceNode"], op["knownNodes"] = true, float64(19), []any{float64(19), float64(22)}
	f.observe(a, op)
	discoveryNoEffect(t, f.step())
	release := discoveryAction(t, f.step(), "buddy-actions.release_interaction")
	if release["params"].(map[string]any)["token"] != "own-flight-token" {
		t.Fatal("cleanup did not retain exact owned token", release)
	}
	delete(f.observation("actions"), "interactionOwner")
	f.reply(release, f.observation("actions"))
	out := f.step()
	if out["done"] != true || out["action"] != nil {
		t.Fatal("discovery did not finish after observed menu and own cleanup", out)
	}
	result := out["memory"].(map[string]any)["discovery"].(map[string]any)
	if result["result"] != "menu_observed" || result["sourceNode"] != float64(19) || result["learned"] != false {
		t.Fatal("discovery invented learning or lost raw menu facts", result)
	}
	for _, key := range []string{"arrived", "cost", "fee", "activated", "route"} {
		if _, exists := result[key]; exists {
			t.Fatal("discovery invented travel or price facts", result)
		}
	}
}

func TestFlightpathDiscoveryLearningNoticeNeedsStatusThenSeparateMenu(t *testing.T) {
	f := newDiscoveryFixture(t)
	status := f.acquire()
	op := f.operation(status, "1", "status_observed")
	op["statusKnown"] = true // ProtoJSON omits the explicitly false nodeKnown.
	f.observe(status, op)
	discoveryNoEffect(t, f.step())
	learn := discoveryAction(t, f.step(), "buddy-actions.query_flightpath")
	op = f.operation(learn, "2", "awaiting_learning_status")
	op["pending"], op["learningNoticeObserved"] = true, true
	f.observe(learn, op)
	discoveryNoEffect(t, f.step())
	delete(f.observation("actions"), "interactionOwner") // Lease expiry is no receipt.
	f.input["now"] = float64(1e12)
	discoveryNoEffect(t, f.step())
	op["phase"], op["pending"], op["learned"], op["nodeKnown"] = "learned", false, true, true
	discoveryNoEffect(t, f.step()) // Missing matching statusKnown still cannot learn.
	op["statusKnown"] = true
	discoveryNoEffect(t, f.step())
	// The completed learning query permits reacquiring finite custody, not a
	// timeout-based replay of the old query. The menu gets a new native CAS.
	discoveryNoEffect(t, f.step())
	acquire := discoveryAction(t, f.step(), "buddy-actions.acquire_interaction")
	f.observation("actions")["interactionOwner"] = "exploration"
	f.reply(acquire, map[string]any{"token": "new-flight-token", "state": f.observation("actions")})
	menu := discoveryAction(t, f.step(), "buddy-actions.query_flightpath")
	if p := menu["params"].(map[string]any); p["mode"] != "discover" || p["expectedRevision"] != "2" || p["token"] != "new-flight-token" {
		t.Fatal("learning did not require a fresh explicit menu query", p)
	}
	if f.input["memory"].(map[string]any)["discovery"] != nil {
		t.Fatal("learning was mislabeled as menu completion", f.input["memory"])
	}
}

func TestFlightpathDiscoveryRejectsUnrelatedOrIncompleteNativeReceipts(t *testing.T) {
	for _, name := range []string{"revision", "head_revision", "session", "body", "visit", "npc", "mode", "pending", "missing_source", "missing_menu"} {
		t.Run(name, func(t *testing.T) {
			f := newDiscoveryFixture(t)
			a := f.knownMenuQuery()
			op := f.operation(a, "2", "menu_observed")
			op["menuKnown"], op["sourceNode"] = true, float64(19)
			f.observe(a, op)
			switch name {
			case "revision":
				op["revision"] = "3"
			case "head_revision":
				f.observation("flightpath")["revision"] = "3"
			case "session":
				op["sessionId"] = "other-session"
			case "body":
				op["selfGuid"] = "5"
			case "visit":
				op["visitId"] = "8"
			case "npc":
				op["npcGuid"] = "17379390964339851266"
			case "mode":
				op["mode"] = "status"
			case "pending":
				op["pending"] = true
			case "missing_source":
				delete(op, "sourceNode")
			case "missing_menu":
				delete(op, "menuKnown")
			}
			discoveryNoEffect(t, f.step())
			if f.input["memory"].(map[string]any)["discovery"] != nil {
				t.Fatal("unrelated receipt completed discovery")
			}
		})
	}
}

func discoveryPending(action map[string]any) map[string]any {
	return map[string]any{"id": action["id"], "method": action["method"], "params": cloneSessionObject(action["params"].(map[string]any)),
		"effect": "mutation", "outcome": "pending", "error": "transport reply unavailable"}
}

func TestFlightpathDiscoveryUnknownRPCSurvivesCASReloadAndOnlyExactReceiptResolves(t *testing.T) {
	f := newDiscoveryFixture(t)
	a := f.acquire()
	row := discoveryPending(a)
	delete(row["params"].(map[string]any), "expectedRevision") // Canonical ProtoJSON zero omission.
	f.input["pending"] = []any{row}
	f.input["last"] = map[string]any{"id": a["id"], "method": a["method"], "effect": "mutation", "error": "reply unavailable"}
	op := f.operation(a, "1", "transport_unknown")
	op["pending"], op["transportUnknown"] = true, true
	f.observation("flightpath")["revision"], f.observation("flightpath")["operation"] = "1", op
	discoveryNoEffect(t, f.step())
	before := cloneSessionObject(f.input["memory"].(map[string]any))
	old := f.r.Status().Revision
	if _, err := f.r.Reload(f.source+"\n-- edited discovery tuning source", old); err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.Reload(f.source, old); !errors.Is(err, ErrRevisionConflict) {
		t.Fatal("stale source CAS was accepted", err)
	}
	delete(f.observation("actions"), "interactionOwner")
	f.input["now"] = float64(1e12)
	discoveryNoEffect(t, f.step())
	if !reflect.DeepEqual(before, f.input["memory"]) {
		t.Fatal("reload or lease expiry cleared the unknown query", before, f.input["memory"])
	}
	op["pending"], op["phase"], op["statusKnown"], op["nodeKnown"] = false, "status_observed", true, true
	out := f.step()
	resolution, ok := out["resolve"].(map[string]any)
	if !ok || resolution["id"] != a["id"] || resolution["observation"] != "flightpath" || resolution["path"] != "operation" || out["action"] != nil || out["done"] == true {
		t.Fatal("exact terminal native receipt did not resolve only its old reservation", out)
	}
	if !reflect.DeepEqual(resolution["equals"], op) {
		t.Fatal("resolution lost the exact native receipt including transport history", resolution)
	}
	f.input["pending"] = []any{}
	discoveryNoEffect(t, f.step()) // Reacquire later; never replay this status call.
}

func TestFlightpathDiscoverySessionReservationAndReloadPreserveWaitingTask(t *testing.T) {
	source := `local discovery=(function()
` + discoverySource(t) + `
end)()
local memory={}
return {
 decide=function(frame)
   local value=frame.input;value.memory=memory
   local out=discovery.decide(value);memory=out.memory;return out
 end,
 on_event=function() return true end,
 export_state=function() return {version=1,data={memory=memory}} end,
 import_state=function(checkpoint) memory=checkpoint.data.memory;return true end,
}`
	s, err := NewSessionWithOptions(context.Background(), "flight-session", source, SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	input := discoveryInput()
	decide := func() Decision {
		t.Helper()
		d, err := s.Decide(context.Background(), input)
		if err != nil {
			t.Fatalf("discovery session: %v; status=%+v", err, s.Status())
		}
		return d
	}
	acquireDecision := decide()
	acquire := discoveryAction(t, acquireDecision.Output, "buddy-actions.acquire_interaction")
	id, err := s.ReserveAction(acquireDecision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(Event{Kind: "action_result", Receipt: &ActionReceipt{SessionID: "flight-session", ActionID: id, Outcome: ActionConfirmed}}); err != nil {
		t.Fatal(err)
	}
	observations := input["observations"].(map[string]any)
	actions := observations["actions"].(map[string]any)
	actions["interactionOwner"] = "exploration"
	input["last"] = map[string]any{"id": acquire["id"], "method": acquire["method"], "effect": "mutation",
		"response": map[string]any{"token": "own-flight-token", "state": actions}}
	queryDecision := decide()
	query := discoveryAction(t, queryDecision.Output, "buddy-actions.query_flightpath")
	id, err = s.ReserveAction(queryDecision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(Event{Kind: "action_result", Receipt: &ActionReceipt{SessionID: "flight-session", ActionID: id, Outcome: ActionUnknown}}); err != nil {
		t.Fatal(err)
	}
	input["pending"] = []any{discoveryPending(query)}
	input["last"] = map[string]any{"id": query["id"], "method": query["method"], "effect": "mutation", "error": "unknown transport"}
	discoveryNoEffect(t, decide().Output)
	before, err := s.State(context.Background())
	if err != nil || len(before.Pending) != 1 || before.Pending[0].ActionID != id || before.Pending[0].Outcome != ActionUnknown {
		t.Fatal("real session did not retain unknown native query reservation", before, err)
	}
	newRevision, err := s.Reload(context.Background(), source+"\n-- current-source reload", before.Status.Revision)
	if err != nil || newRevision == before.Status.Revision {
		t.Fatal("current source CAS did not reload", newRevision, err)
	}
	if _, err := s.Reload(context.Background(), source, before.Status.Revision); !errors.Is(err, ErrRevisionConflict) {
		t.Fatal("stale CAS replaced current discovery source", err)
	}
	after, err := s.State(context.Background())
	if err != nil || !reflect.DeepEqual(before.Pending, after.Pending) || !reflect.DeepEqual(before.Checkpoint, after.Checkpoint) {
		t.Fatal("reload lost committed task or actual reserved receipt", before, after, err)
	}
	discoveryNoEffect(t, decide().Output)
	if _, err := s.ReserveAction(queryDecision); !errors.Is(err, ErrFenced) {
		t.Fatal("old query ticket was admitted twice", err)
	}
}

func TestFlightpathDiscoveryVisitReplacementCleansOnlyItsToken(t *testing.T) {
	f := newDiscoveryFixture(t)
	query := f.acquire()
	op := f.operation(query, "1", "status_observed")
	op["statusKnown"], op["nodeKnown"] = true, true
	f.observe(query, op)
	discoveryNoEffect(t, f.step())
	f.observation("world")["visitRevision"] = "8"
	f.observation("flightpath")["visitId"] = "8"
	release := discoveryAction(t, f.step(), "buddy-actions.release_interaction")
	if p := release["params"].(map[string]any); p["owner"] != "exploration" || p["token"] != "own-flight-token" {
		t.Fatal("replaced visit attempted unrelated cleanup", p)
	}
	delete(f.observation("actions"), "interactionOwner")
	f.reply(release, f.observation("actions"))
	out := f.step()
	if out["done"] != true || out["memory"].(map[string]any)["discovery"].(map[string]any)["result"] != "body_or_visit_changed" {
		t.Fatal("replaced visit continued discovery or invented success", out)
	}
}

func TestFlightpathDiscoveryLargeCASRemainsExact(t *testing.T) {
	f := newDiscoveryFixture(t)
	f.observation("flightpath")["revision"] = "9007199254740993"
	a := f.acquire()
	if a["params"].(map[string]any)["expectedRevision"] != "9007199254740993" {
		t.Fatal("CAS rounded through a Lua number", a)
	}
	op := f.operation(a, "9007199254740994", "status_observed")
	op["statusKnown"], op["nodeKnown"] = true, true
	f.observe(a, op)
	discoveryNoEffect(t, f.step())
	next := discoveryAction(t, f.step(), "buddy-actions.query_flightpath")
	if next["params"].(map[string]any)["expectedRevision"] != "9007199254740994" {
		t.Fatal("next query lost exact observed native revision", next)
	}
}

func TestFlightpathDiscoveryPendingResolutionRequiresOriginalExactRequest(t *testing.T) {
	for _, name := range []string{"token", "counter", "id", "second_pending", "unknown_not_dispatched", "written_not_dispatched", "missing_send_history"} {
		t.Run(name, func(t *testing.T) {
			f := newDiscoveryFixture(t)
			a := f.acquire()
			row := discoveryPending(a)
			f.input["pending"] = []any{row}
			op := f.operation(a, "1", "status_observed")
			op["statusKnown"], op["nodeKnown"] = true, true
			f.observation("flightpath")["revision"], f.observation("flightpath")["operation"] = "1", op
			switch name {
			case "token":
				row["params"].(map[string]any)["token"] = "different-owner-token"
			case "counter":
				row["params"].(map[string]any)["expectedRevision"] = "1"
			case "id":
				row["id"] = "another-task:1"
			case "second_pending":
				f.input["pending"] = []any{row, cloneSessionObject(row)}
			case "unknown_not_dispatched":
				op["phase"], op["transportUnknown"] = "not_dispatched", true
				delete(op, "transportWritten")
			case "written_not_dispatched":
				op["phase"] = "not_dispatched"
			case "missing_send_history":
				delete(op, "transportWritten")
			}
			discoveryNoEffect(t, f.step())
		})
	}
}

func TestFlightpathDiscoveryNativeGuardRefusalCleansUpWithoutQueryRetry(t *testing.T) {
	f := newDiscoveryFixture(t)
	query := f.acquire()
	op := f.operation(query, "1", "not_dispatched")
	delete(op, "transportWritten")
	op["error"] = "flightpath_flightmaster_out_of_range"
	f.observe(query, op) // Native reserved this exact query, then refused it.
	discoveryNoEffect(t, f.step())
	release := discoveryAction(t, f.step(), "buddy-actions.release_interaction")
	if release["params"].(map[string]any)["token"] != "own-flight-token" {
		t.Fatal("refused query abandoned or replaced its own custody", release)
	}
	delete(f.observation("actions"), "interactionOwner")
	f.reply(release, f.observation("actions"))
	out := f.step()
	if out["done"] != true || out["action"] != nil || out["memory"].(map[string]any)["serial"] != float64(3) {
		t.Fatal("guard refusal retried the query or failed to clean up", out)
	}
	result := out["memory"].(map[string]any)["discovery"].(map[string]any)
	if result["result"] != "query_not_dispatched" || result["learned"] != false || result["sourceNode"] != nil {
		t.Fatal("native guard refusal invented discovery success", result)
	}
}

func TestFlightpathDiscoveryUnconfirmedCustodyReplyNeverReplaysAcquire(t *testing.T) {
	f := newDiscoveryFixture(t)
	a := discoveryAction(t, f.step(), "buddy-actions.acquire_interaction")
	f.reply(a, map[string]any{})
	discoveryNoEffect(t, f.step())
	f.input["now"] = float64(1e12)
	discoveryNoEffect(t, f.step())
	if got := f.input["memory"].(map[string]any)["serial"]; got != float64(1) {
		t.Fatal("unconfirmed acquisition was issued again", got)
	}
}

func TestFlightpathDiscoveryWaitsForFactualQuietBodyBeforeCustody(t *testing.T) {
	for _, name := range []string{"moving", "castPending", "motionUnconfirmed", "autoAttacking", "autoAttackRequested", "autoAttackUnconfirmed", "practicing", "traveling", "recovery", "combat", "movement_unknown", "movement", "channel_unknown", "channel"} {
		t.Run(name, func(t *testing.T) {
			f := newDiscoveryFixture(t)
			a := f.observation("actions")
			self := f.observation("world")["self"].(map[string]any)
			switch name {
			case "recovery":
				a["recovery"] = map[string]any{"active": true}
			case "combat":
				self["unitFlags"] = float64(0x80000)
			case "movement_unknown":
				delete(self, "movementFlagsKnown")
			case "movement":
				self["movementFlags"] = float64(1)
			case "channel_unknown":
				delete(self, "channelObjectKnown")
			case "channel":
				self["channelSpellId"] = float64(123)
			default:
				a[name] = true
			}
			discoveryNoEffect(t, f.step())
			if got := f.input["memory"].(map[string]any)["serial"]; got != float64(0) {
				t.Fatal("predictably unavailable custody was reserved", got)
			}
		})
	}
}
