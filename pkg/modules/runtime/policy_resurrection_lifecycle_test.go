package modrt

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"google.golang.org/protobuf/proto"
)

func TestPolicyResurrectionObservedLifeSettlesUnknownLedgerBeforeNewDeath(t *testing.T) {
	ctx := context.Background()
	const sid = "unknown-resurrection"
	script, err := os.ReadFile(filepath.Join("..", "..", "..", "behaviors", "support.lua"))
	if err != nil {
		t.Fatal(err)
	}
	source := "local support=(function()\n" + string(script) + "\nend)()\n" + `
 return {on_event=support.on_event,export_state=support.export_state,import_state=support.import_state,
 decide=function(frame)
   support.on_event({kind="encounter_observation",value=frame.input})
   if #frame.pending~=0 then return {} end
   return {action=support.resurrect(frame.input,frame)}
 end}`
	vm, err := behavior.NewSession(ctx, sid, source)
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Close()
	input := map[string]any{"sessionId": sid, "selfGuid": "4", "visitId": "1", "hostOwned": true,
		"players": map[string]any{"4": map[string]any{"present": true, "aliveKnown": true, "alive": true}, "3": map[string]any{"present": true, "aliveKnown": true, "alive": false}},
		"support": map[string]any{"resurrections": []any{map[string]any{"patientGuid": "3", "castTargetGuid": "3", "spellId": float64(2006), "ready": true}}}}
	first, err := vm.Decide(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	id, err := vm.ReserveAction(first)
	if err != nil {
		t.Fatal(err)
	}
	manager := New(nil, nil, nil)
	defer manager.cancel()
	token := strings.Repeat("a", 64)
	op := &policySpell{vm: vm, capability: "resurrect", spell: 2006, target: 3, castTarget: 3, attempted: true, token: token,
		origin: modsession.WriteOrigin{SessionID: sid, Module: actionsModule, Writer: "lua", ActionID: strconv.FormatUint(id, 10)}}
	manager.policySpells.active = map[string]*policySpell{token: op}
	host := &SessionPolicy{vm: vm, manager: manager}
	if err = host.recordSpellTransport(op, &behavior.ActionReceipt{SessionID: sid, ActionID: id, Outcome: behavior.ActionUnknown, Payload: op.payload("transport_unconfirmed_no_retry", false, false, false)}); err != nil {
		t.Fatal(err)
	}
	if err = vm.Drain(ctx); err != nil || vm.Status().Pending != 1 {
		t.Fatal("unknown native write lost custody", vm.Status(), err)
	}
	if pending, err := vm.Decide(ctx, input); err != nil || len(pending.Output) != 0 {
		t.Fatal("pending resurrection replayed", pending.Output, err)
	}
	// The native support boundary emits this terminal receipt only after an
	// actual alive observation; absence of own GO must not become own success.
	native := &actionsv1.SpellOperation{Capability: "resurrect", SpellId: 2006, TargetGuid: 3, Phase: "observed_alive_without_cast_confirmation"}
	body, err := proto.Marshal(native)
	if err != nil {
		t.Fatal(err)
	}
	manager.recordPolicyEffect(token, body, false)
	if err = vm.Drain(ctx); err != nil || vm.Status().Pending != 0 {
		t.Fatal("positive life left the attached VM pending forever", vm.Status(), err)
	}
	if op.effect == nil || op.effect.Outcome != behavior.ActionSettledUnverified || op.effect.Payload["effect_observed"] != false || op.effect.Payload["server_cast_go"] != false {
		t.Fatal("unattributed life invented own success", op.effect)
	}
	// Until the module receives that actual life observation, even a terminal
	// unverified receipt must not rearm a still-dead patient.
	if same, err := vm.Decide(ctx, input); err != nil || len(same.Output) != 0 {
		t.Fatal("same death rearmed by unverified settlement", same.Output, err)
	}
	patient := input["players"].(map[string]any)["3"].(map[string]any)
	patient["alive"] = true
	if _, err = vm.Decide(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err = vm.Reload(ctx, source+"\n-- same attached body", vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	patient["alive"] = false
	next, err := vm.Decide(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	action, _ := next.Output["action"].(map[string]any)
	if action["capability"] != "resurrect" || action["targetGuid"] != "3" {
		t.Fatal("later observed death remained hidden", next.Output)
	}
	nextID, err := vm.ReserveAction(next)
	if err != nil || nextID <= id {
		t.Fatal("later Lua selection cannot reserve after positive life", nextID, err)
	}
}
