package world

import (
	"context"
	"os"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func orphanedTaskHost(t *testing.T) *behavior.Session {
	t.Helper()
	task, err := os.ReadFile("../../behaviors/encounter-tasks.lua")
	if err != nil {
		t.Fatal(err)
	}
	buff, err := os.ReadFile("../../behaviors/buffs.lua")
	if err != nil {
		t.Fatal(err)
	}
	source := "local module=(function()\n" + string(task) + "\nend)()\nlocal buffs=(function()\n" + string(buff) + "\nend)()\n" + `return {decide=function(frame)
 local i=frame.input
 if i.saved then assert(module.import_state(i.saved)) end
 if i.receipt then module.on_event({receipt=i.receipt}) end
 if i.command then module.on_event({kind="command",value=i.command}) end
 local view=module.step(i.obs,{clock=i.clock,pending=i.pending},i.sequence,{},false,true)
 local buff=not view.hold and buffs.choose(i.obs,{clock=i.clock}) or nil
 return {status=view.status,action=view.action,buff=buff,saved=module.export_state()}
 end,on_event=function(event)return true end,export_state=module.export_state,import_state=module.import_state}`
	h, err := behavior.NewSession(context.Background(), "buffs-lua", source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	return h
}
func orphanedTaskInput() map[string]any {
	obs := buffPolicyObservation()
	obs["visitId"] = "1"
	obs["navigation"] = map[string]any{"movementOwner": map[string]any{}}
	obs["players"].(map[string]any)["4"].(map[string]any)["movementKnown"] = true
	installBuffAura(obs, "4", 1244, 600000, false)
	return map[string]any{"obs": obs, "clock": map[string]any{"monotonicMs": float64(0)}, "sequence": "1", "pending": []any{},
		"saved": map[string]any{"version": float64(1), "data": map[string]any{"retired": map[string]any{}, "current": map[string]any{
			"id": "cancelled-rally", "kind": "rally_to", "guideGuid": "3", "responseYards": float64(5), "deadline": float64(900000),
			"sessionId": "buffs-lua", "selfGuid": "4", "mapId": float64(0), "visitId": "1", "finishing": "canceled", "token": "prior-token",
			"operation": map[string]any{"mode": "renew", "owner": "task:cancelled-rally"}}}}}
}
func decideOrphan(t *testing.T, h *behavior.Session, i map[string]any) map[string]any {
	t.Helper()
	d, e := h.Decide(context.Background(), i)
	if e != nil {
		t.Fatal(e)
	}
	return d.Output
}
func TestCancelledOrphanedRenewRetiresOnlyAfterFreshObservedFiniteExpiryThenBuffsOwner(t *testing.T) {
	for _, receipt := range []string{"missing", "mismatched"} {
		t.Run(receipt, func(t *testing.T) {
			h := orphanedTaskHost(t)
			i := orphanedTaskInput()
			if receipt == "mismatched" {
				i["receipt"] = map[string]any{"outcome": "confirmed", "payload": map[string]any{"capability": "movement_owner", "mode": "renew", "owner": "task:other"}}
			}
			first := decideOrphan(t, h, i)
			if first["status"].(map[string]any)["phase"] != "waiting_for_movement_ownership" {
				t.Fatal(first)
			}
			delete(i, "saved")
			delete(i, "receipt")
			i["clock"] = map[string]any{"monotonicMs": float64(3001)}
			if out := decideOrphan(t, h, i); out["status"].(map[string]any)["phase"] != "waiting_for_movement_ownership" {
				t.Fatal("same snapshot retired custody", out)
			}
			i["sequence"] = "2"
			out := decideOrphan(t, h, i)
			if out["action"] != nil || out["status"].(map[string]any)["phase"] != "complete" || out["status"].(map[string]any)["reason"] != "canceled" {
				t.Fatal("orphan replayed or remained stuck", out)
			}
			b, _ := out["buff"].(map[string]any)
			if b["targetGuid"] != "3" || b["spellId"] != float64(1244) || b["needsApproach"] != false {
				t.Fatal("normal in-range Fortitude did not resume", out)
			}
			i["sequence"] = "3"
			if out = decideOrphan(t, h, i); out["action"] != nil {
				t.Fatal("retired custody replayed", out)
			}
		})
	}
}
func TestOrphanedRenewPreservesPendingUnknownActiveMotionAndClockUncertainty(t *testing.T) {
	for _, mode := range []string{"pending_unknown", "missing_ledger", "active_owner", "unconfirmed_motion", "moving", "unknown_movement", "clock_regression", "same_commit_observation"} {
		t.Run(mode, func(t *testing.T) {
			h := orphanedTaskHost(t)
			i := orphanedTaskInput()
			if mode == "same_commit_observation" {
				i["saved"].(map[string]any)["data"].(map[string]any)["current"].(map[string]any)["operation"].(map[string]any)["observationSequence"] = "1"
			}
			decideOrphan(t, h, i)
			delete(i, "saved")
			nav := i["obs"].(map[string]any)["navigation"].(map[string]any)
			self := i["obs"].(map[string]any)["players"].(map[string]any)["4"].(map[string]any)
			switch mode {
			case "pending_unknown":
				i["pending"] = []any{map[string]any{"action_id": "99", "outcome": "unknown"}}
			case "missing_ledger":
				delete(i, "pending")
			case "active_owner":
				nav["movementOwner"] = map[string]any{"active": true, "owner": "task:cancelled-rally"}
			case "unconfirmed_motion":
				nav["motionUnconfirmed"] = true
			case "moving":
				self["moving"] = true
			case "unknown_movement":
				delete(self, "movementKnown")
			}
			i["clock"] = map[string]any{"monotonicMs": float64(4000)}
			if mode != "same_commit_observation" {
				i["sequence"] = "2"
			}
			if mode == "clock_regression" {
				i["clock"] = map[string]any{"monotonicMs": float64(-1)}
			}
			out := decideOrphan(t, h, i)
			if out["status"].(map[string]any)["phase"] != "waiting_for_movement_ownership" || out["action"] != nil || out["buff"] != nil {
				t.Fatal("unsettled custody was cleared", out)
			}
		})
	}
}
