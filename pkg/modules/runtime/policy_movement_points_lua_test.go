package modrt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

const retainedPointProvider = `local kept={};local priorScope
local encounter_intent={cloud_points=function(now,scope,sightings)
 if priorScope~=scope then kept={};priorScope=scope end
 for _,v in ipairs(sightings)do
  if not kept[v.id]then kept[v.id]={x=v.x,y=v.y,z=v.z,radiusYards=99,expiresAtMs=now+20000}end
 end
 local result={};for _,v in pairs(kept)do result[#result+1]=v end;return result
end}
`

func acceptedMovementPointsSource(t testing.TB) string {
	t.Helper()
	raw, err := exec.Command("python3", filepath.Join("..", "..", "..", "dev", "encounter-bundle")).Output()
	if err != nil {
		t.Fatal(err)
	}
	return retainedPointProvider + string(raw)
}
func pointActorObservation(body string) map[string]any {
	return map[string]any{"sessionId": "point-actor-" + body, "selfGuid": body, "mapId": 349, "visitId": "1", "hostOwned": true, "hostRequested": true, "worldObservationRevision": "1", "spellbookKnown": true, "meleeKnown": true,
		"players":    map[string]any{body: map[string]any{"guid": body, "combatReachKnown": true, "combatReach": 1.5, "present": true, "aliveKnown": true, "alive": true, "mapKnown": true, "mapId": 349, "positionKnown": true, "positionRevision": "1", "x": 0, "y": 0, "z": 0, "orientation": 0, "movementKnown": true, "moving": false, "healthKnown": true, "health": 100, "maxHealth": 100, "combatKnown": true, "inCombat": false}},
		"targets":    map[string]any{"100": map[string]any{"guid": "100", "entry": 1, "present": true, "aliveKnown": true, "alive": true, "mapKnown": true, "mapId": 349, "positionKnown": true, "positionRevision": "1", "x": 10, "y": 0, "z": 0, "attackable": true, "engaged": true, "healthKnown": true, "health": 100, "maxHealth": 100}},
		"engagement": map[string]any{"100": map[string]any{"victimGuid": body}}, "spells": map[string]any{"133": map[string]any{"known": true, "ready": true, "available": true, "capability": "offense"}},
		"party":      map[string]any{"group": map[string]any{"known": true, "inGroup": true, "groupGuid": "7", "leaderGuid": "3", "members": []any{map[string]any{"guid": body, "online": true}, map[string]any{"guid": "3", "online": true}}}},
		"navigation": map[string]any{"movementPointsSupported": true, "movementOwner": map[string]any{"active": false}, "movementPoints": map[string]any{"reason": "observed_input_only"}}, "groundEffectsSupported": true, "groundEffects": []any{}}
}
func pointObject(now int64) map[string]any {
	return map[string]any{"guid": "999", "casterGuid": "100", "spellId": 21070, "x": 0, "y": 0, "z": 0, "radiusYards": 5, "firstObservedAtUnixMs": strconv.FormatInt(now, 10), "serverCastTimeMs": uint32(4294967295), "positionRevision": "1"}
}
func pointObserve(t testing.TB, vm *behavior.Session, obs map[string]any, kind string) behavior.Decision {
	t.Helper()
	seq, err := vm.Enqueue(behavior.Event{Kind: "encounter_observation", Value: obs})
	if err != nil {
		t.Fatal(err)
	}
	d, err := vm.Decide(context.Background(), map[string]any{"kind": kind, "observationSequence": strconv.FormatUint(seq, 10)})
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func TestMovementPointsFullAcceptedBundleCastThenEscapeThenResume(t *testing.T) {
	source := acceptedMovementPointsSource(t)
	vm, err := behavior.NewSession(context.Background(), "point-actor-4", source)
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Close()
	obs := pointActorObservation("4")
	ordinary := pointObserve(t, vm, obs, "encounter")
	first, ok := ordinary.Output["action"].(map[string]any)
	if !ok || first["capability"] != "offense" {
		t.Fatal("fixture did not select its original ordinary attack", ordinary.Output)
	}
	id, err := vm.ReserveAction(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().UnixMilli() - 1000
	obs["groundEffects"] = []any{pointObject(created)}
	obs["navigation"].(map[string]any)["movementPoints"].(map[string]any)["reason"] = "protected_cast_pending"
	advisory := pointObserve(t, vm, obs, "movement_points")
	if advisory.Output["action"] != nil {
		t.Fatal("advisory generated a gameplay action")
	}
	input, ok := advisory.Output["movementPoints"].(map[string]any)
	if !ok {
		t.Fatal("actual ground-effect input unavailable", advisory.Output)
	}
	row := input["points"].([]any)[0].(map[string]any)
	if row["radiusYards"] != float64(5) || row["expiresAtUnixMs"] != float64(created+20000) {
		t.Fatal("actual radius or first-observed clock was replaced", row)
	}
	duplicate := pointObject(created)
	duplicate["positionRevision"] = "2"
	duplicate["serverCastTimeMs"] = uint32(0)
	obs["groundEffects"] = []any{duplicate}
	repeated := pointObserve(t, vm, obs, "movement_points")
	repeatedPoint := repeated.Output["movementPoints"].(map[string]any)["points"].([]any)[0].(map[string]any)
	if repeatedPoint["expiresAtUnixMs"] != row["expiresAtUnixMs"] {
		t.Fatal("query/duplicate CREATE refreshed an old ground object", repeatedPoint, row)
	}
	held := pointObserve(t, vm, obs, "encounter")
	if held.Output["action"] != nil {
		t.Fatal("pending cast was cancelled/replaced", held.Output)
	}
	if vm.Status().Pending != 1 {
		t.Fatal("original cast reservation was lost")
	}
	if _, err := vm.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: "point-actor-4", ActionID: id, Outcome: behavior.ActionUnknown}}); err != nil {
		t.Fatal(err)
	}
	unknown := pointObserve(t, vm, obs, "encounter")
	if unknown.Output["action"] != nil || vm.Status().Pending != 1 {
		t.Fatal("unknown cast was cleared or retried", unknown.Output, vm.Status())
	}
	if _, err := vm.Reload(context.Background(), source+"\n-- source handoff\n", vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	reloaded := pointObserve(t, vm, obs, "movement_points")
	reloadPoint := reloaded.Output["movementPoints"].(map[string]any)["points"].([]any)[0].(map[string]any)
	if vm.Status().Pending != 1 || reloadPoint["expiresAtUnixMs"] != row["expiresAtUnixMs"] {
		t.Fatal("source handoff reset UNKNOWN or refreshed an old object", vm.Status(), reloadPoint)
	}
	if _, err := vm.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: "point-actor-4", ActionID: id, Outcome: behavior.ActionConfirmed}}); err != nil {
		t.Fatal(err)
	}
	obs["navigation"].(map[string]any)["movementPoints"].(map[string]any)["reason"] = "observed_input_only"
	escaped := pointObserve(t, vm, obs, "encounter")
	step, ok := escaped.Output["action"].(map[string]any)
	if !ok || step["capability"] != "movement_points_step" {
		t.Fatal("next ordinary cast won over an occupied-point safe yield", escaped.Output)
	}
	obs["groundEffects"] = []any{}
	resumed := pointObserve(t, vm, obs, "encounter")
	attack, ok := resumed.Output["action"].(map[string]any)
	if !ok || attack["capability"] != "offense" {
		t.Fatal("original role did not resume after the visible object disappeared", resumed.Output)
	}
	points := resumed.Output["movementPoints"].(map[string]any)["points"]
	if rows, ok := points.([]any); ok && len(rows) != 0 {
		t.Fatal("actor-retained trail resurrected an absent actual area", points)
	}
	obs["groundEffectsSupported"] = false
	unavailable := pointObserve(t, vm, obs, "encounter")
	if unavailable.Output["movementPoints"] != nil || unavailable.Output["movementPointStatus"].(map[string]any)["phase"] != "ground_effect_input_unavailable" {
		t.Fatal("missing facts were silently estimated", unavailable.Output)
	}
}
func TestMovementPointsPriorityKeepsExplicitAndFriendlySurvivalActions(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "behaviors", "movement-points.lua"))
	if err != nil {
		t.Fatal(err)
	}
	source := "local m=(function()\n" + string(raw) + `
end)();return {
 on_event=function()return true end,
 export_state=function()return {version=1,data={}}end,import_state=function()return true end,
 decide=function(f)local o=f.input.obs;local out={action=f.input.action,encounterOwnership={visitId="1"}}
 local points=m.project(f,o,f.input.points)
 m.prioritize(f,o,"1",out,points,f.input.explicit and out.action or nil);return out end}`
	for _, cap := range []string{"heal", "cleanse", "resurrect", "fear_break", "consume", "cancel_spell", "offense", "cast_spell"} {
		t.Run(cap, func(t *testing.T) {
			vm, err := behavior.NewSession(context.Background(), "point-actor-4", source)
			if err != nil {
				t.Fatal(err)
			}
			defer vm.Close()
			obs := pointActorObservation("4")
			decision, err := vm.Decide(context.Background(), map[string]any{"obs": obs, "action": map[string]any{"capability": cap}, "explicit": cap == "cast_spell", "points": []any{map[string]any{"x": 0, "y": 0, "z": 0, "radiusYards": 5, "expiresAtUnixMs": time.Now().UnixMilli() + 10000}}})
			if err != nil {
				t.Fatal(err)
			}
			got := decision.Output["action"].(map[string]any)["capability"]
			if cap == "offense" {
				if got != "movement_points_step" {
					t.Fatal("ordinary offense did not yield", decision.Output)
				}
			} else if got != cap {
				t.Fatal("explicit/survival choice was replaced", decision.Output)
			}
		})
	}
}

func BenchmarkMovementPointsFourPersistentAcceptedActors(b *testing.B) {
	source := acceptedMovementPointsSource(b)
	bodies := []string{"1", "4", "5", "8"}
	sessions := make([]*behavior.Session, 4)
	for i, body := range bodies {
		vm, err := behavior.NewSession(context.Background(), "point-actor-"+body, source)
		if err != nil {
			b.Fatal(err)
		}
		sessions[i] = vm
		defer vm.Close()
	}
	created := time.Now().UnixMilli()
	b.ReportAllocs()
	b.ResetTimer()
	var group sync.WaitGroup
	for actor, vm := range sessions {
		group.Add(1)
		go func() {
			defer group.Done()
			obs := pointActorObservation(bodies[actor])
			obs["groundEffects"] = []any{pointObject(created)}
			for step := actor; step < b.N; step += 4 {
				decision := pointObserve(b, vm, obs, "movement_points")
				if decision.Output["movementPoints"] == nil {
					b.Error("full accepted advisory did not emit its observed point")
					return
				}
			}
		}()
	}
	group.Wait()
	b.ReportMetric(float64(len(source)), "source_bytes")
}

func TestMovementPointsExistingRangedOwnershipNeverBorrowsForeignOrUnknownToken(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "behaviors", "encounter-commands.lua"))
	if err != nil {
		t.Fatal(err)
	}
	source := "local commands=(function()\n" + string(raw) + `
end)();return {
 on_event=function()return true end,export_state=function()return {version=1,data={}}end,import_state=function()return true end,
 decide=function(f)assert(commands.import_state({version=1,data={requests={},rangedHold=f.input.hold,rangedGround=f.input.ground}}));local owned,blocked=commands.movement_point_owner(f.input.obs,f);return {owned=owned,blocked=blocked}end}`
	for _, kind := range []string{"own", "foreign", "unknown", "pending", "old_visit", "old_body", "expired", "ground_pending", "ground_unknown", "ground_awaiting", "ground_old_scope"} {
		t.Run(kind, func(t *testing.T) {
			vm, err := behavior.NewSession(context.Background(), "point-actor-4", source)
			if err != nil {
				t.Fatal(err)
			}
			defer vm.Close()
			obs := pointActorObservation("4")
			obs["navigation"].(map[string]any)["movementOwner"] = map[string]any{"active": true, "owner": "ranged:owned"}
			hold := map[string]any{"owner": "ranged:owned", "token": "owned-fixture-token", "sessionId": "point-actor-4", "selfGuid": "4", "visitId": "1", "mapId": 349, "untilUnixMs": time.Now().UnixMilli() + 10000}
			switch kind {
			case "foreign":
				hold["owner"] = "other-task"
			case "unknown":
				hold["uncertain"] = true
			case "pending":
				hold["operation"] = map[string]any{}
			case "old_visit":
				hold["visitId"] = "2"
			case "old_body":
				hold["sessionId"] = "other-body"
			case "expired":
				hold["untilUnixMs"] = 1
			}
			var ground any
			if strings.HasPrefix(kind, "ground_") {
				g := map[string]any{"sessionId": "point-actor-4", "selfGuid": "4", "visitId": "1", "id": "owned", "key": "pose", "blocked": map[string]any{}}
				op := map[string]any{"kind": "move_body", "sequence": "1", "heading": "cloud:1", "x": 0, "y": 0, "z": 0}
				switch kind {
				case "ground_pending":
					g["operation"] = op
				case "ground_unknown":
					g["uncertain"] = true
				case "ground_awaiting":
					g["awaiting"] = op
				case "ground_old_scope":
					g["visitId"] = "2"
				}
				ground = g
			}
			result, err := vm.Decide(context.Background(), map[string]any{"obs": obs, "hold": hold, "ground": ground})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "own" {
				if result.Output["owned"].(map[string]any)["token"] != "owned-fixture-token" {
					t.Fatal("lost legitimate current ownership")
				}
			} else if result.Output["owned"] != nil {
				t.Fatal("borrowed foreign/unknown ownership", kind, result.Output)
			}
			if strings.HasPrefix(kind, "ground_") && result.Output["blocked"] != true {
				t.Fatal("retained local ground ledger was forgotten", kind, result.Output)
			}
		})
	}
}

func TestMovementPointsUnauthoredDefaultBundleHasNoAdvisorySideEffects(t *testing.T) {
	source := strings.TrimPrefix(acceptedMovementPointsSource(t), retainedPointProvider)
	vm, err := behavior.NewSession(context.Background(), "point-actor-4", source)
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Close()
	if vm.HasExport(movementPointsHook) {
		t.Fatal("ordinary default registered an unauthored point hook")
	}
	result := pointObserve(t, vm, pointActorObservation("4"), "encounter")
	if result.Output["movementPoints"] != nil || result.Output["movementPointStatus"] != nil {
		t.Fatal("unrelated default policy got point status", result.Output)
	}
}

func TestMovementPointsFirstEstimatedPoseAndContinuedStepStayHonest(t *testing.T) {
	source := acceptedMovementPointsSource(t)
	vm, err := behavior.NewSession(context.Background(), "point-actor-4", source)
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Close()
	obs := pointActorObservation("4")
	own := obs["players"].(map[string]any)["4"].(map[string]any)
	own["positionEstimated"] = true
	obs["groundEffects"] = []any{pointObject(time.Now().UnixMilli() - 1000)}
	first := pointObserve(t, vm, obs, "encounter")
	if first.Output["action"].(map[string]any)["capability"] != "movement_points_step" || first.Output["movementPointStatus"].(map[string]any)["poseEvidence"] != "native_self_estimate" {
		t.Fatal("legitimate first native self estimate was blocked", first.Output)
	}
	id, err := vm.ReserveAction(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = vm.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: "point-actor-4", ActionID: id, Outcome: behavior.ActionSettledUnverified, Payload: map[string]any{"capability": "movement_points_step", "transport_written": true, "effect_observed": false}}}); err != nil {
		t.Fatal(err)
	}
	own["x"] = 1.4
	nav := obs["navigation"].(map[string]any)
	nav["bodyMovement"] = map[string]any{"sequenceId": first.Output["action"].(map[string]any)["sequenceId"], "visitId": "1", "transportWritten": true, "positionEstimated": true, "phase": "body_input_written", "x": 1.4, "y": 0, "z": 0}
	continued := pointObserve(t, vm, obs, "encounter")
	if continued.Output["action"].(map[string]any)["capability"] != "movement_points_step" || continued.Output["movementPointStatus"].(map[string]any)["poseEvidence"] != "correlated_native_estimate" {
		t.Fatal("first1.4yd input stranded actor inside5yd", continued.Output)
	}
	nav["motionUnconfirmed"] = true
	unknown := pointObserve(t, vm, obs, "encounter")
	if action, ok := unknown.Output["action"].(map[string]any); ok && action["capability"] == "movement_points_step" {
		t.Fatal("uncertain motion was replayed", unknown.Output)
	}
}
func TestMovementPointsCoincidentVisibleObjectsKeepDistinctLifetimes(t *testing.T) {
	vm, err := behavior.NewSession(context.Background(), "point-actor-4", acceptedMovementPointsSource(t))
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Close()
	now := time.Now().UnixMilli()
	obs := pointActorObservation("4")
	older := pointObject(now - 19000)
	newer := pointObject(now - 5000)
	newer["guid"] = "1000"
	obs["groundEffects"] = []any{older, newer}
	decision := pointObserve(t, vm, obs, "movement_points")
	points := decision.Output["movementPoints"].(map[string]any)["points"].([]any)
	latest := float64(0)
	for _, raw := range points {
		p := raw.(map[string]any)
		if p["expiresAtUnixMs"].(float64) > latest {
			latest = p["expiresAtUnixMs"].(float64)
		}
		if p["radiusYards"] != float64(5) {
			t.Fatal("mixed witness radius", p)
		}
	}
	if latest != float64(now+15000) {
		t.Fatal("coincident later ground object was clipped to the older expiry", points)
	}
	obs["groundEffects"] = []any{newer}
	again := pointObserve(t, vm, obs, "movement_points")
	for _, raw := range again.Output["movementPoints"].(map[string]any)["points"].([]any) {
		if raw.(map[string]any)["expiresAtUnixMs"] != latest {
			t.Fatal("remaining object's lifetime changed", again.Output)
		}
	}
}
func TestMovementPointsUrgentHealApproachKeepsItsExistingSupportPriority(t *testing.T) {
	for _, kind := range []string{"urgent_heal", "ordinary_dps"} {
		t.Run(kind, func(t *testing.T) {
			vm, err := behavior.NewSession(context.Background(), "point-actor-4", acceptedMovementPointsSource(t))
			if err != nil {
				t.Fatal(err)
			}
			defer vm.Close()
			obs := pointActorObservation("4")
			obs["groundEffects"] = []any{pointObject(time.Now().UnixMilli() - 1000)}
			if kind == "urgent_heal" {
				obs["players"].(map[string]any)["3"] = map[string]any{"guid": "3", "present": true, "aliveKnown": true, "alive": true, "healthKnown": true, "health": 20, "maxHealth": 100, "mapKnown": true, "mapId": 349, "positionKnown": true, "x": 45, "y": 0, "z": 0}
				obs["spells"].(map[string]any)["2050"] = map[string]any{"known": true, "ready": true, "available": true, "capability": "heal", "metadataKnown": true, "maxRange": 40}
				obs["healing"] = map[string]any{"sessionId": "point-actor-4", "hostOwned": true, "caster": map[string]any{"guid": "4", "ready": true, "mana": 30}, "patients": []any{map[string]any{"guid": "3", "health": 20, "maxHealth": 100, "distance": 45}}, "spells": []any{map[string]any{"spellId": 2050, "rank": 1, "level": 1, "cost": 10, "amount": 40, "castMs": 1500, "minRange": 0, "maxRange": 40}}}
			} else {
				obs["targets"].(map[string]any)["100"].(map[string]any)["x"] = 45
				spell := obs["spells"].(map[string]any)["133"].(map[string]any)
				spell["ready"] = false
				spell["metadataKnown"] = true
				spell["maxRange"] = 30
			}
			result := pointObserve(t, vm, obs, "encounter")
			action, ok := result.Output["action"].(map[string]any)
			if !ok {
				t.Fatal("fixture did not produce its meaningful choice", result.Output)
			}
			if kind == "urgent_heal" {
				if action["capability"] != "approach_target" || action["targetGuid"] != "3" {
					t.Fatal("urgent support positioning was overwritten", result.Output)
				}
			} else if action["capability"] != "movement_points_step" {
				t.Fatal("ordinary DPS did not yield to the occupied point", result.Output)
			}
		})
	}
}

func TestMovementPointsObservedSphereUsesOwnReachAndPreservesUnknownAction(t *testing.T) {
	for _, tc := range []struct {
		name     string
		x, z     float64
		known    bool
		reach    float64
		wantStep bool
	}{
		{"center572_still_inside", 5.72, 0, true, 1.5, true}, {"radius650_clear", 6.5, 0, true, 1.5, false},
		{"vertical572_inside", 0, 5.72, true, 1.5, true}, {"diagonal3D_clear", 5.72, 5, true, 1.5, false},
		{"small_actual_reach", 5.72, 0, true, .5, false}, {"unknown_not_offense_veto", 5.72, 0, false, 1.5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vm, err := behavior.NewSession(context.Background(), "point-actor-4", acceptedMovementPointsSource(t))
			if err != nil {
				t.Fatal(err)
			}
			defer vm.Close()
			obs := pointActorObservation("4")
			own := obs["players"].(map[string]any)["4"].(map[string]any)
			own["x"], own["z"], own["combatReachKnown"], own["combatReach"] = tc.x, tc.z, tc.known, tc.reach
			obs["groundEffects"] = []any{pointObject(time.Now().UnixMilli() - 1000)}
			d := pointObserve(t, vm, obs, "encounter")
			action, ok := d.Output["action"].(map[string]any)
			if !ok {
				t.Fatal("unrelated ordinary decision lost", d.Output)
			}
			if (action["capability"] == "movement_points_step") != tc.wantStep {
				t.Fatal(d.Output)
			}
			point := d.Output["movementPoints"].(map[string]any)["points"].([]any)[0].(map[string]any)
			if point["radiusYards"] != float64(5) || point["groundEffectGuid"] != "999" {
				t.Fatal("source radius/identity changed", point)
			}
			if !tc.known && d.Output["movementPointStatus"].(map[string]any)["phase"] != "observed_effect_clearance_unknown" {
				t.Fatal(d.Output)
			}
		})
	}
}
