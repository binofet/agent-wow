package behavior

import (
	"context"
	lua "github.com/yuin/gopher-lua"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type gmRecoveryFixture struct {
	runtime                   *Runtime
	frame, observations, goal map[string]any
	members                   []any
}

func gmRecoveryRig(t *testing.T) *gmRecoveryFixture {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "gm-recovery.lua"))
	if err != nil {
		t.Fatal(err)
	}
	r := New()
	if _, err = r.Reload(string(source), ""); err != nil {
		t.Fatal(err)
	}
	goalMembers, observed := []any{}, []any{}
	for i, n := range []string{"Arlen", "Joel", "Mira", "Rook", "Nixwick"} {
		id := []string{"1", "3", "4", "5", "8"}[i]
		goalMembers = append(goalMembers, map[string]any{"guid": id, "name": n})
		observed = append(observed, map[string]any{"guid": id, "name": n, "known": true, "sessionId": "body-" + id, "mapId": 189, "x": 10, "y": 10, "z": 0, "positionKnown": true, "visibleToGm": false, "aliveKnown": true, "alive": false, "combatKnown": true, "inCombat": false, "hp": 0, "maxHp": 100, "manaUser": i != 1 && i != 3, "mana": 0, "maxMana": 100})
	}
	goal := map[string]any{"id": "recover-1", "destination": map[string]any{"mapId": 189, "x": 100, "y": 100, "z": 0, "tolerance": 5}, "members": goalMembers}
	obs := map[string]any{"gm": map[string]any{"guid": "87", "sessionId": "gm-current", "positionKnown": true, "positionRevision": "1", "observationRevision": "1", "mapId": 189, "x": 0, "y": 0, "z": 0}, "members": observed, "group": map[string]any{"known": true, "leaderGuid": "1", "members": goalMembers}, "threats": []any{}, "destinationRoute": map[string]any{"sessionId": "gm-current", "goalReached": true, "serverPositionRevision": "1", "start": map[string]any{"revision": "1"}, "endpoint": map[string]any{"known": true, "mapId": 189, "x": 100, "y": 100, "z": 0}}}
	return &gmRecoveryFixture{r, map[string]any{"goal": goal, "memory": map[string]any{}, "observations": obs, "pending": []any{}, "now": float64(1)}, obs, goal, observed}
}
func (f *gmRecoveryFixture) step(t *testing.T) map[string]any {
	t.Helper()
	out, err := evaluateGMRecoveryPolicy(f.runtime, f.frame)
	if err != nil {
		t.Fatal(err)
	}
	f.frame["memory"] = out["memory"]
	return out
}
func (f *gmRecoveryFixture) gm() map[string]any    { return f.observations["gm"].(map[string]any) }
func (f *gmRecoveryFixture) group() map[string]any { return f.observations["group"].(map[string]any) }
func (f *gmRecoveryFixture) member(name string) map[string]any {
	for _, row := range f.members {
		p := row.(map[string]any)
		if p["name"] == name {
			return p
		}
	}
	return nil
}
func recoveryAction(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	a, ok := out["action"].(map[string]any)
	if !ok {
		t.Fatalf("no action: %+v", out)
	}
	return a
}
func (f *gmRecoveryFixture) colocate(p map[string]any) {
	p["x"], p["y"], p["z"], p["mapId"], p["visibleToGm"] = 100, 100, 0, 189, true
}
func (f *gmRecoveryFixture) apply(t *testing.T, a map[string]any) {
	t.Helper()
	f.frame["last"] = map[string]any{"id": a["id"], "outcome": "transport_written"}
	if a["kind"] == "select_target" {
		f.observations["selection"] = map[string]any{"targetGuid": a["targetGuid"], "transportWritten": true}
		return
	}
	command := a["command"].(string)
	args := strings.Fields(command)
	switch {
	case strings.HasPrefix(command, ".go xyz"):
		f.colocate(f.gm())
	case strings.HasPrefix(command, ".group remove "):
		name := args[2]
		id := "87"
		if name != "Lotharen" {
			id = f.member(name)["guid"].(string)
		}
		rows := []any{}
		for _, row := range f.group()["members"].([]any) {
			if row.(map[string]any)["guid"] != id {
				rows = append(rows, row)
			}
		}
		f.group()["members"] = rows
	case strings.HasPrefix(command, ".group join "):
		name := args[3]
		p := f.member(name)
		if name == "Lotharen" {
			p = map[string]any{"guid": "87", "name": name}
		}
		f.group()["members"] = append(f.group()["members"].([]any), p)
	case strings.HasPrefix(command, ".group leader "):
		leader := "87"
		if args[2] != "Lotharen" {
			leader = f.member(args[2])["guid"].(string)
		}
		f.group()["leaderGuid"] = leader
	case strings.HasPrefix(command, ".groupsummon "):
		ids := map[any]bool{}
		for _, row := range f.group()["members"].([]any) {
			ids[row.(map[string]any)["guid"]] = true
		}
		for _, row := range f.members {
			p := row.(map[string]any)
			if ids[p["guid"]] {
				f.colocate(p)
			}
		}
	case strings.HasPrefix(command, ".revive "):
		f.member(args[1])["alive"] = true
	case strings.HasPrefix(command, ".modify "):
		id := f.observations["selection"].(map[string]any)["targetGuid"]
		for _, row := range f.members {
			p := row.(map[string]any)
			if p["guid"] == id {
				if args[1] == "hp" {
					p["hp"] = p["maxHp"]
				} else {
					p["mana"] = p["maxMana"]
				}
			}
		}
	default:
		t.Fatal("unexpected action", a)
	}
}
func TestGMRecoveryActualArrivalPrecedesSummonAndAcknowledgmentIsNotEffect(t *testing.T) {
	f := gmRecoveryRig(t)
	first := f.step(t)
	a := recoveryAction(t, first)
	if !strings.HasPrefix(a["command"].(string), ".go xyz") {
		t.Fatal(first)
	}
	f.frame["last"] = map[string]any{"id": a["id"], "outcome": "transport_written"}
	for i := 0; i < 3; i++ {
		out := f.step(t)
		if out["action"] != nil || out["done"] == true || out["phase"] != "observing_move" {
			t.Fatal("command ACK advanced arrival", out)
		}
	}
	f.colocate(f.gm())
	if a = recoveryAction(t, f.step(t)); a["command"] != ".group remove Nixwick" {
		t.Fatal(a)
	}
}
func TestGMRecoveryFiveMemberTransportRosterAndFullResources(t *testing.T) {
	f := gmRecoveryRig(t)
	commands := []string{}
	var finished bool
	for i := 0; i < 70; i++ {
		out := f.step(t)
		if out["error"] != nil {
			t.Fatal(out)
		}
		if out["done"] == true {
			if out["success"] != true {
				t.Fatal(out)
			}
			finished = true
			break
		}
		a := recoveryAction(t, out)
		if c, ok := a["command"].(string); ok {
			commands = append(commands, c)
			if strings.HasPrefix(c, ".revive") {
				if f.group()["leaderGuid"] != "1" || len(f.group()["members"].([]any)) != 5 {
					t.Fatal("revival before restored roster", f.group())
				}
				for _, row := range f.members {
					p := row.(map[string]any)
					if p["x"] != 100 || p["visibleToGm"] != true {
						t.Fatal("revived before all same-instance arrivals", p)
					}
				}
			}
		}
		f.apply(t, a)
	}
	if !finished {
		t.Fatal("task did not complete", commands)
	}
	n := 0
	for _, c := range commands {
		if strings.HasPrefix(c, ".groupsummon") {
			n++
		}
	}
	if n != 2 {
		t.Fatal("expected two staged group transports", commands)
	}
}
func TestGMRecoverySameMapCoordinatesWithoutVisibilityDoNotProveInstanceArrival(t *testing.T) {
	f := gmRecoveryRig(t)
	f.colocate(f.gm())
	for _, row := range f.members {
		p := row.(map[string]any)
		f.colocate(p)
		p["visibleToGm"] = false
	}
	for i := 0; i < 3; i++ {
		a := recoveryAction(t, f.step(t))
		f.apply(t, a)
	}
	out := f.step(t)
	a := recoveryAction(t, out)
	if a["command"] != ".groupsummon Arlen" {
		t.Fatal(out)
	}
	f.frame["last"] = map[string]any{"id": a["id"], "outcome": "transport_written"}
	out = f.step(t)
	if out["action"] != nil || out["phase"] != "observing_summon_first" {
		t.Fatal(out)
	}
}
func TestGMRecoverySessionReplacementAndThreatExposeResumableFailure(t *testing.T) {
	f := gmRecoveryRig(t)
	f.step(t)
	f.gm()["sessionId"] = "gm-replaced"
	out := f.step(t)
	if out["action"] != nil || out["error"] != "gm_session_changed" || out["done"] == true {
		t.Fatal(out)
	}
	f = gmRecoveryRig(t)
	f.observations["threats"] = []any{map[string]any{"guid": "bad", "mapId": 189, "positionKnown": true, "x": 101, "y": 100, "z": 0, "hostile": true, "engaged": true}}
	out = f.step(t)
	if out["error"] != "destination_under_attack" || out["blocker"] == nil || out["action"] != nil || out["done"] == true {
		t.Fatal(out)
	}
}
func TestGMRecoveryUnknownOrUndispatchedWriteNeverRepeatsAndCanObserveLateEffect(t *testing.T) {
	for _, outcome := range []string{"transport_unconfirmed", "not_dispatched"} {
		t.Run(outcome, func(t *testing.T) {
			f := gmRecoveryRig(t)
			a := recoveryAction(t, f.step(t))
			f.frame["last"] = map[string]any{"id": a["id"], "outcome": outcome, "error": "unavailable"}
			f.frame["now"] = 20
			for i := 0; i < 2; i++ {
				out := f.step(t)
				if out["action"] != nil || out["done"] == true {
					t.Fatal("ambiguous command replayed", out)
				}
			}
			f.colocate(f.gm())
			a = recoveryAction(t, f.step(t))
			if a["command"] != ".group remove Nixwick" {
				t.Fatal("late observed effect could not resume", a)
			}
		})
	}
}

func TestGMRecoveryRemoteNamedPlayerFirstMovesThenRequiresObservedSafeArrival(t *testing.T) {
	f := gmRecoveryRig(t)
	delete(f.goal, "destination")
	f.goal["player"] = map[string]any{"guid": "3", "name": "Joel"}
	a := recoveryAction(t, f.step(t))
	if a["command"] != ".appear Joel" {
		t.Fatal(a)
	}
	f.frame["last"] = map[string]any{"id": a["id"], "outcome": "transport_written"}
	if out := f.step(t); out["action"] != nil {
		t.Fatal("missing arrival reissued appear", out)
	}
	p := f.member("Joel")
	f.colocate(p)
	f.observations["destinationPlayer"] = p
	out := f.step(t)
	if out["action"] != nil {
		t.Fatal("destination visible does not mean GM arrived", out)
	}
	f.colocate(f.gm())
	a = recoveryAction(t, f.step(t))
	if a["command"] != ".group remove Nixwick" {
		t.Fatal(a)
	}
}
func TestGMRecoveryUnengagedHostileStillBlocksRevivingIntoPack(t *testing.T) {
	f := gmRecoveryRig(t)
	f.observations["threats"] = []any{map[string]any{"guid": "pack", "mapId": 189, "positionKnown": true, "x": 101, "y": 100, "z": 0, "alive": true, "hostile": true, "engaged": false}}
	out := f.step(t)
	if out["error"] != "destination_under_attack" || out["action"] != nil {
		t.Fatal(out)
	}
}

func TestGMRecoveryHoldPreservesReceiptsAndClearsOnlyRetiredCombatIntent(t *testing.T) {
	hold, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "gm-recovery-hold.lua"))
	if err != nil {
		t.Fatal(err)
	}
	source := `local wrap=(function() ` + string(hold) + ` end)()
 local saved={version=1,data={encounter={engaged={old=true},focusGuid="bad",burstIntent={},bindings={keep=true}},commands={data={requests={1},killOrder={2},focus="bad"}},pendingReceipt="uncertain-777"}}
 local events=0
 local original={export_state=function()return saved end,import_state=function(v)saved=v;return true end,on_event=function(e)events=events+1;return true end,decide=function(f)return {action={kind="offense",capability="damage"}} end}
 local held=wrap(original)
 return {decide=function(frame)
   held.on_event({kind="encounter_observation",sequence="4",value={sessionId="gm-test",visitId="2"}})
   local out=held.decide({session_id="gm-test",cursor="4",pending={{id="777"}},input={kind="encounter",observationSequence="4"}})
   local state=held.export_state()
   return {phase=out.phase,owns=out.encounterOwnership~=nil,events=events,engagedEmpty=next(state.data.encounter.engaged)==nil,
       retiredCommands=#state.data.commands.data.requests==0,receipt=state.data.pendingReceipt,binding=state.data.encounter.bindings.keep}
 end}`
	r := New()
	if _, err = r.Reload(source, ""); err != nil {
		t.Fatal(err)
	}
	out, err := evaluateGMRecoveryPolicy(r, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if out["phase"] != "gm_recovery_hold" || out["owns"] != true || out["engagedEmpty"] != true || out["retiredCommands"] != true || out["receipt"] != "uncertain-777" || out["binding"] != true || out["events"] != float64(1) {
		t.Fatal(out)
	}
}
func TestGMRecoveryCancellationRestoresObservedRosterBeforeReportingCleanup(t *testing.T) {
	f := gmRecoveryRig(t)
	for i := 0; i < 4; i++ {
		a := recoveryAction(t, f.step(t))
		f.apply(t, a)
	}
	f.frame["cancelled"] = true
	for i := 0; i < 8; i++ {
		out := f.step(t)
		if out["cleanupComplete"] == true {
			if out["cancelled"] != true || out["success"] == true {
				t.Fatal(out)
			}
			return
		}
		a := recoveryAction(t, out)
		c := a["command"].(string)
		if strings.HasPrefix(c, ".revive") || strings.HasPrefix(c, ".groupsummon") {
			t.Fatal("cancel continued gameplay", a)
		}
		f.apply(t, a)
	}
	t.Fatal("cancel did not restore roster")
}
func TestGMRecoveryFinalVerificationRechecksRosterAndColocation(t *testing.T) {
	f := gmRecoveryRig(t)
	var saved map[string]any
	for i := 0; i < 70; i++ {
		out := f.step(t)
		if out["done"] == true {
			saved = out["memory"].(map[string]any)
			break
		}
		f.apply(t, recoveryAction(t, out))
	}
	if saved == nil {
		t.Fatal("setup did not reach verify")
	}
	f.member("Rook")["visibleToGm"] = false
	out := f.step(t)
	if out["done"] == true || out["error"] != "party_left_verified_destination" {
		t.Fatal("stale arrival accepted at completion", out)
	}
	f.member("Rook")["visibleToGm"] = true
	f.group()["leaderGuid"] = "87"
	out = f.step(t)
	if out["done"] == true || out["error"] != "original_party_not_restored" {
		t.Fatal("stale roster accepted at completion", out)
	}
}

func TestGMRecoveryObservedTeleportArrivalDoesNotNeedWalkingRoute(t *testing.T) {
	f := gmRecoveryRig(t)
	f.colocate(f.gm())
	f.gm()["positionRevision"], f.gm()["observationRevision"] = "7", "7"
	route := f.observations["destinationRoute"].(map[string]any)
	route["goalReached"] = false
	route["serverPositionRevision"] = "6"
	a := recoveryAction(t, f.step(t))
	if a["command"] != ".group remove Nixwick" {
		t.Fatal("walking route incorrectly blocked an observed teleport arrival", a)
	}
}
func TestGMRecoveryRevivesObservedSummonedMemberWithoutWalkingCorridor(t *testing.T) {
	f := gmRecoveryRig(t)
	f.goal["members"].([]any)[0].(map[string]any)["rpcUrl"] = "http://127.0.0.1:8086/rpc"
	patient := f.member("Arlen")
	patient["observationRevision"] = "7"
	patient["landingRoute"] = map[string]any{"goalReached": false, "error": "no walking corridor"}
	for i := 0; i < 30; i++ {
		out := f.step(t)
		if out["error"] != nil {
			t.Fatal("walking route blocked observed teleport landing", out)
		}
		a := recoveryAction(t, out)
		if a["command"] == ".revive Arlen" {
			if patient["visibleToGm"] != true || patient["x"] != 100 || patient["y"] != 100 || patient["alive"] != false {
				t.Fatal("revival did not require the actual summoned body", patient)
			}
			return
		}
		f.apply(t, a)
	}
	t.Fatal("observed summoned member was not revived")
}

func TestGMRecoveryAlreadyReadyPartyCompletesWithoutMutatingRoster(t *testing.T) {
	f := gmRecoveryRig(t)
	f.colocate(f.gm())
	for _, row := range f.members {
		p := row.(map[string]any)
		f.colocate(p)
		p["alive"], p["hp"], p["mana"] = true, 100, 100
	}
	out := f.step(t)
	if out["done"] != true || out["success"] != true || out["action"] != nil {
		t.Fatal("ready party unnecessarily transported or regrouped", out)
	}
}

func TestGMRecoveryCombatStopWaitsForActualCombatFlagBeforeTransport(t *testing.T) {
	f := gmRecoveryRig(t)
	f.colocate(f.gm())
	p := f.member("Arlen")
	p["inCombat"] = true
	a := recoveryAction(t, f.step(t))
	if a["command"] != ".combatstop Arlen" {
		t.Fatal(a)
	}
	f.frame["last"] = map[string]any{"id": a["id"], "outcome": "transport_written"}
	out := f.step(t)
	if out["action"] != nil || out["phase"] != "observing_combatstop_Arlen" {
		t.Fatal("transport ACK cleared combat", out)
	}
	p["inCombat"] = false
	a = recoveryAction(t, f.step(t))
	if a["command"] != ".group remove Nixwick" {
		t.Fatal(a)
	}
}

func TestGMRecoveryRealCreatureFactionWithoutRelationNeverClaimsSafeDestination(t *testing.T) {
	f := gmRecoveryRig(t)
	f.observations["threats"] = []any{map[string]any{"guid": "scarlet", "mapId": 189, "positionKnown": true, "x": 101, "y": 100, "z": 0, "alive": true, "factionTemplate": 67, "engaged": false}}
	out := f.step(t)
	if out["error"] != "destination_hostility_unclassified" || out["action"] != nil || out["blocker"] == nil {
		t.Fatal("unexposed hostile flag was treated as known friendly", out)
	}
	f = gmRecoveryRig(t)
	f.goal["hostileFactions"] = []any{67}
	f.observations["threats"] = []any{map[string]any{"guid": "scarlet", "mapId": 189, "positionKnown": true, "x": 101, "y": 100, "z": 0, "alive": true, "factionTemplate": 67, "engaged": false}}
	if out = f.step(t); out["error"] != "destination_under_attack" || out["action"] != nil {
		t.Fatal("explicit hostile faction not respected", out)
	}
}

func evaluateGMRecoveryPolicy(r *Runtime, input map[string]any) (map[string]any, error) {
	L := newVM()
	defer L.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	L.SetContext(ctx)
	fn, err := decisionFunction(L, L.NewFunctionFromProto(r.active.proto))
	if err != nil {
		return nil, err
	}
	state, err := toLua(L, input, &budget{ctx: ctx, nodeLimit: MaxInputNodes})
	if err != nil {
		return nil, err
	}
	if err = L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, state); err != nil {
		return nil, err
	}
	out, err := fromLua(L.Get(-1), &budget{ctx: ctx}, map[*lua.LTable]bool{}, 0)
	if err != nil {
		return nil, err
	}
	return out.(map[string]any), nil
}

func TestGMRecoveryHoldDefendsObservedAttackAndHealsWhileTaskIsBlocked(t *testing.T) {
	hold, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "gm-recovery-hold.lua"))
	if err != nil {
		t.Fatal(err)
	}
	source := `local wrap=(function() ` + string(hold) + ` end)()
 return {decide=function(frame)
  local original={export_state=function()return {version=1,data={encounter={engaged={old=true}}}} end,
   import_state=function(s)return true end,on_event=function(e)return true end,
   decide=function(f)return {phase="ordinary",action={kind="cast",capability=frame.approach and "approach_target" or frame.heal and "heal" or "offense",targetGuid="3"}} end}
  local held=wrap(original)
  held.on_event({kind="encounter_observation",sequence="7",value={sessionId="aw-current",visitId="1",
   players={ ["3"]={guid="3",present=true,aliveKnown=true,alive=true,combatKnown=true,inCombat=frame.attacked==true,healthKnown=true,health=(frame.heal or frame.approach) and 50 or 100,maxHealth=100}},
   targets={ ["99"]={guid="99",present=true,aliveKnown=true,alive=true}},
   engagement=frame.attackEvent and { ["99"]={victimGuid="3",ageMs=0}} or {}}})
  return held.decide({session_id="aw-current",cursor="7",pending={},input={kind="encounter",observationSequence="7"}})
 end}`
	r := New()
	if _, err = r.Reload(source, ""); err != nil {
		t.Fatal(err)
	}
	for _, input := range []map[string]any{{"attacked": true}, {"attackEvent": true}, {"heal": true}, {"approach": true}} {
		out, err := evaluateGMRecoveryPolicy(r, input)
		if err != nil || out["action"] == nil || out["phase"] != "ordinary" {
			t.Fatal("recovery disabled live defense/healing", input, out, err)
		}
	}
	out, err := evaluateGMRecoveryPolicy(r, map[string]any{})
	if err != nil || out["action"] != nil || out["phase"] != "gm_recovery_hold" {
		t.Fatal("quiet hold repulled stale target", out, err)
	}
}

func TestGMRecoveryReconcilesObservedResourceMaximumChangeWithoutRepeatingTransport(t *testing.T) {
	for _, kind := range []string{"mana", "health"} {
		t.Run(kind, func(t *testing.T) {
			f := gmRecoveryRig(t)
			f.colocate(f.gm())
			for _, row := range f.members {
				p := row.(map[string]any)
				f.colocate(p)
				p["alive"], p["hp"], p["mana"] = true, 100, 100
			}
			patient := f.member("Arlen")
			value, maximum, command := "mana", "maxMana", ".modify mana "
			if kind == "health" {
				value, maximum, command = "hp", "maxHp", ".modify hp "
			}
			patient[value] = 0
			selectAction := recoveryAction(t, f.step(t))
			if selectAction["kind"] != "select_target" || selectAction["targetGuid"] != "1" {
				t.Fatal(selectAction)
			}
			f.apply(t, selectAction)
			old := recoveryAction(t, f.step(t))
			if old["command"] != command+"100" {
				t.Fatal(old)
			}
			// Actual first refill arrives while a legitimate stat buff changes max.
			f.apply(t, old)
			patient[maximum] = 125
			f.frame["now"] = 20
			next := recoveryAction(t, f.step(t))
			if next["command"] != command+"125" || next["id"] == old["id"] {
				t.Fatal("changed maximum remained blocked or repeated old request", next)
			}
			memory := f.frame["memory"].(map[string]any)
			if memory["stage"] != kind || memory["resourceReconciled"] == nil {
				t.Fatal("resource correction restarted recovery", memory)
			}
			f.apply(t, next)
			out := f.step(t)
			if out["done"] != true || out["success"] != true || out["action"] != nil {
				t.Fatal("corrected maximum did not finish in place", out)
			}
		})
	}
}
func TestGMRecoveryMaximumChangeAloneCannotReplayUnobservedRefill(t *testing.T) {
	f := gmRecoveryRig(t)
	f.colocate(f.gm())
	for _, row := range f.members {
		p := row.(map[string]any)
		f.colocate(p)
		p["alive"], p["hp"], p["mana"] = true, 100, 100
	}
	patient := f.member("Arlen")
	patient["mana"] = 0
	f.apply(t, recoveryAction(t, f.step(t)))
	old := recoveryAction(t, f.step(t))
	patient["maxMana"] = 125
	f.frame["last"] = map[string]any{"id": old["id"], "outcome": "unconfirmed", "error": "transport unknown"}
	out := f.step(t)
	if out["action"] != nil || out["phase"] != "observing_mana_Arlen" {
		t.Fatal("max change or ACK invented refill success", out)
	}
	f.frame["now"] = 20
	out = f.step(t)
	if out["action"] != nil || out["error"] != "effect_not_observed:mana_Arlen" {
		t.Fatal("uncertain old write was replayed after timeout", out)
	}
}
