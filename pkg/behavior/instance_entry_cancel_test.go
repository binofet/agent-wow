package behavior

import (
	"os"
	"path/filepath"
	"testing"
)

func entryTaskSources(t *testing.T) string {
	t.Helper()
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("..", "..", "behaviors", name+".lua"))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	return "local encounter_instance_entry=(function()\n" + read("instance-entry") +
		"\nend)()\nlocal tasks=(function()\n" + read("encounter-tasks") + "\nend)()\n"
}

func TestCanceledEntryWalkAfterIndependentWorldTransfer(t *testing.T) {
	source := entryTaskSources(t) + `
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

func TestCanceledEntryRetiresOnlyItsOwnObsoleteLease(t *testing.T) {
	source := entryTaskSources(t) + `
return {decide=function(input)
    local walk={capability="travel_to",sessionId="s",selfGuid="1",visitId="9",mapId=0,token="original"}
    local task={id="front",kind="enter_instance",sessionId="s",selfGuid="1",ownerGuid="3",visitId="9",
        mapId=0,destinationMapId=70,allowedTriggerIds={286},stage="travel",serial=0,token="original",
        leaseVisit="9",finishing="canceled",history={{id="original-receipt"}},uncertain=walk}
    local self={guid="1",present=true,mapKnown=true,mapId=70}
    local obs={sessionId="s",selfGuid="1",visitId="10",mapId=70,players={["1"]=self}}
    assert(tasks.import_state({version=1,data={current=task,retired={}}}))
    local function step() return tasks.step(obs,{pending={}},1,{},false,true) end
    local function status(view,owner)
        assert(view.action.capability=="instance_entry_status" and view.action.visitId==obs.visitId)
        tasks.commit(view,view.action)
        -- A late travel response cannot be consumed by the new status request.
        tasks.on_event({kind="action_result",receipt={outcome="unknown",payload={capability="travel_to"}}})
        assert(task.operation.capability=="instance_entry_status")
        tasks.on_event({kind="action_result",receipt={outcome="confirmed",payload={
            capability="instance_entry_status",result={sessionId="s",selfGuid="1",interactionOwner=owner,
                position={known=true,mapKnown=true,mapId=obs.mapId}}}}})
    end
    if input.anotherVisit then
        status(step(),"")
        self.mapId,obs.mapId,obs.visitId=input.nextMap,input.nextMap,"11"
        status(step(),"")
        assert(tasks.import_state(tasks.export_state()))
        obs.visitId="10"
        assert(step().status.phase=="waiting_for_destination_observation")
        obs.visitId="11"
    else
        status(step(),"exploration")
        local v=step()
        assert(v.action.capability=="release_interaction" and v.action.token=="original")
        tasks.commit(v,v.action)
        local p={capability="release_interaction",phase="admission_rejected",transport_written=false,
            self_guid="1",visit_id="10",grpc_status="FailedPrecondition",grpc_message="interaction lease does not match"}
        local outcome="not_dispatched"
        if input.case=="uncertain STOP" then outcome="unknown";p.phase="dispatch_unconfirmed_no_retry"
        elseif input.case=="owed STOP" then p.grpc_message="interaction movement stop unconfirmed"
        elseif input.case=="written" then p.transport_written=true
        elseif input.case=="foreign visit" then p.visit_id="11"
        elseif input.case=="foreign body" then p.self_guid="3" end
        tasks.on_event({kind="action_result",receipt={action_id="24",outcome=outcome,payload=p}})
        assert(tasks.import_state(tasks.export_state()))
        if input.case~="replaced lease" then
            local held=step()
            return {ok=held.hold==true and task.token=="original" and task.lastLease==nil and
                task.canceledApproach.uncertain==walk and (not held.action or held.action.capability=="instance_entry_status")}
        end
    end
    local v=step()
    local retired=tasks.export_state().data.retired.front
    assert(retired and retired.task.canceledApproach.uncertain==walk and retired.task.canceledApproach.observedVisit=="10")
    local lease=retired.task.lastLease
    return {ok=v.action==nil and v.status.reason=="canceled" and retired.task.destinationVisit==nil and
        retired.task.notifyId==nil and retired.task.history[1].id=="original-receipt" and
        lease.token=="original" and lease.visitId=="9" and lease.absentOrReplacedObserved==true and
        (input.anotherVisit and retired.task.canceledApproach.cleanupVisit=="11" or
         lease.releaseRefusal and lease.releaseRefusal.action_id=="24" and lease.releaseRefusal.payload.transport_written==false)}
end}`
	for _, scenario := range []struct {
		name       string
		movedAgain bool
		nextMap    int
	}{
		{"same map later visit", true, 70}, {"left after arrival", true, 0},
		{"replaced lease", false, 0}, {"uncertain STOP", false, 0}, {"owed STOP", false, 0},
		{"written", false, 0}, {"foreign visit", false, 0}, {"foreign body", false, 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			r := New()
			load(t, r, source)
			out, err := r.Evaluate(map[string]any{"case": scenario.name, "anotherVisit": scenario.movedAgain, "nextMap": scenario.nextMap})
			if err != nil || out["ok"] != true {
				t.Fatalf("cleanup: %v, %v", out, err)
			}
		})
	}
}
