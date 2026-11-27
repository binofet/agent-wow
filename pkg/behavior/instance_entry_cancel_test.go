package behavior

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanceledEntryWalkAfterIndependentWorldTransfer(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("..", "..", "behaviors", name+".lua"))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	source := "local encounter_instance_entry=(function()\n" + read("instance-entry") +
		"\nend)()\nlocal tasks=(function()\n" + read("encounter-tasks") + "\nend)()\n" + `
return {decide=function(input)
    local op={capability="travel_to",sessionId="s",selfGuid="1",visitId="9",mapId=0,token="original"}
    local task={id="front",kind="enter_instance",sessionId="s",selfGuid="1",ownerGuid="3",
        visitId="9",mapId=0,destinationMapId=70,allowedTriggerIds={286},stage="travel",serial=0,
        token="original",leaseVisit="9",finishing="canceled",history={{id="prior-receipt"}}}
    task[input.field]=op
    local self={guid="1",present=true,mapKnown=true,mapId=70}
    local obs={sessionId="s",selfGuid="1",visitId="10",mapId=70,players={["1"]=self}}
    local frame={pending={}}
    if input.case=="pending" then frame.pending={42}
    elseif input.case=="source" then obs.visitId="9";obs.mapId=0;self.mapId=0
    elseif input.case=="stale" then obs.visitId="8"
    elseif input.case=="other destination" then obs.mapId=1;self.mapId=1
    elseif input.case=="other body" then obs.selfGuid="5"
    elseif input.case=="no body" then self.present=false
    elseif input.case=="other token" then op.token="other"
    elseif input.case=="other operation" then op.capability="move_body"
    elseif input.case=="active" then task.finishing=nil
    elseif input.case=="trigger sent" then task.notifyId="front:1" end
    assert(tasks.import_state({version=1,data={current=task,retired={}}}))
    local function step()
        return tasks.step(obs,frame,1,{},false,true)
    end
    local v=step()
    if input.case~="arrived" then
        local state=tasks.export_state().data
        return {held=v.hold==true and state.current~=nil and state.current[input.field]==op and
            state.current.canceledApproach==nil and v.action==nil and state.retired.front==nil}
    end
    assert(v.action.capability=="instance_entry_status" and v.action.visitId=="10")
    tasks.commit(v,v.action)
    tasks.on_event({kind="action_result",receipt={outcome="confirmed",payload={
        capability="instance_entry_status",result={sessionId="s",selfGuid="1",position={mapKnown=true,mapId=70}}}}})
    v=step()
    local saved=tasks.export_state()
    assert(tasks.import_state(saved))
    local state=tasks.export_state().data
    local old=state.retired.front.task
    return {retired=state.current==nil and v.action==nil and state.last.reason=="canceled" and
        old.destinationVisit==nil and old.notifyId==nil and old.canceledApproach.observedVisit=="10" and
        old.canceledApproach[input.field]==op and old.history[1].id=="prior-receipt" and old.lastLease.token=="original"}
end}`
	for _, field := range []string{"operation", "uncertain"} {
		for _, scenario := range []string{"arrived", "pending", "source", "stale", "other destination", "other body", "no body", "other token", "other operation", "active", "trigger sent"} {
			t.Run(field+"/"+scenario, func(t *testing.T) {
				r := New()
				load(t, r, source)
				out, err := r.Evaluate(map[string]any{"field": field, "case": scenario})
				key := "held"
				if scenario == "arrived" {
					key = "retired"
				}
				if err != nil || out[key] != true {
					t.Fatalf("%s: output %v, error %v", key, out, err)
				}
			})
		}
	}
}
