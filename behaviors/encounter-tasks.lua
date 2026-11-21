-- Temporary script tasks. The host transports structured intent and finite
-- movement ownership; this module owns the task's conditions and lifecycle.
local M = {tuning={lease_ms=3000,renew_below_ms=1500,response_yards=10,anchor_yards=2}}
local state = {retired={}}
local function number(n) return type(n)=="number" and n==n and n>-math.huge and n<math.huge end
local function id(s) return type(s)=="string" and #s>0 and #s<=80 end
local function guid(s)
    if not id(s) or #s>20 or byte_at(s,1)==48 then return end
    for i=1,#s do local b=byte_at(s,i);if b<48 or b>57 then return end end
    if #s<20 or s<="18446744073709551615" then return s end
end
local function alive(p) return p and p.present==true and p.aliveKnown==true and p.alive==true end
local function position(p,map)
    if not p or p.present~=true or p.positionKnown~=true or p.mapKnown~=true or p.mapId~=map then return end
    local x,y,z=p.x or 0,p.y or 0,p.z or 0
    if number(x) and number(y) and number(z) then return {x=x,y=y,z=z} end
end
local function near(a,b,r)
    return a and b and (a.x-b.x)^2+(a.y-b.y)^2+(a.z-b.z)^2<=r*r
end
local function finish(task,reason) task.finishing=task.finishing or reason end
local function pop(task)
    state.last={id=task.id,kind=task.kind,phase="complete",reason=task.finishing}
    state.retired[task.id]=task.finishing
    state.current=nil
end
local function request(v)
    if not id(v.id) or v.ownerGuid~=nil and not guid(v.ownerGuid) or
       v.responseYards~=nil and (not number(v.responseYards) or v.responseYards<=0) or
       v.deadlineMonotonicMs~=nil and (not number(v.deadlineMonotonicMs) or v.deadlineMonotonicMs<0) then return end
    return {id=v.id,kind="wait_for_pull",ownerGuid=v.ownerGuid,responseYards=v.responseYards,
            deadline=v.deadlineMonotonicMs,stage="requested"}
end

function M.on_event(event)
    if event.kind=="encounter_observation" and type(event.value)=="table" then
        local o=event.value
        state.observed={sessionId=o.sessionId,selfGuid=o.selfGuid,visitId=o.visitId,mapId=o.mapId}
    end
    local task=state.current
    local receipt=event.receipt
    if task and task.operation and receipt then
        local p=receipt.payload or {}
        local op=task.operation
        if p.capability=="movement_owner" and p.owner==op.owner and p.mode==op.mode then
            if receipt.outcome=="confirmed" then
                local result=p.result or {}
                if op.mode=="release" then task.token=nil;task.released=true
                elseif type(result.token)=="string" and #result.token>0 then
                    task.token=result.token;task.granted=true
                else task.uncertain=true end
                task.operation=nil
            elseif receipt.outcome=="not_dispatched" or receipt.outcome=="failed" then
                task.operation=nil;task.refusal=p.error or receipt.outcome
                if op.mode=="renew" then task.token=nil end
            elseif receipt.outcome=="settled_unverified" or receipt.outcome=="unknown" then
                task.operation=nil;task.uncertain=true;task.uncertainUntil=nil
            end
        end
    end
    if event.kind~="command" or type(event.value)~="table" then return true end
    local v=event.value
    if v.task=="cancel_task" or v.task=="wait_for_pull" and v.cancel==true then
        if task and (v.id==nil or v.id==task.id) then
            finish(task,"canceled")
            if not task.sessionId and not task.operation then pop(task) end
        end
        if state.next and (v.id==nil or v.id==state.next.id) then
            state.retired[state.next.id]="canceled";state.next=nil
        end
    elseif v.task=="wait_for_pull" then
        local next_task=request(v)
        if not next_task then state.inputError="invalid_wait_for_pull_intent"
        elseif not state.retired[next_task.id] and (not task or task.id~=next_task.id) then
            if task then finish(task,"superseded") end
            if state.next then state.retired[state.next.id]="superseded_before_start" end
            next_task.requested=state.observed
            state.next=next_task;state.inputError=nil
        end
    elseif task and v.supersedes==task.id then finish(task,"superseded") end
    return true
end

local function action(obs,task,mode)
    return {capability="movement_owner",sessionId=obs.sessionId,selfGuid=obs.selfGuid,
            owner="task:"..task.id,mode=mode,token=mode~="acquire" and task.token or nil,
            leaseMs=mode~="release" and M.tuning.lease_ms or nil}
end
local function status(task,phase)
    return {id=task.id,kind=task.kind,phase=phase,ownerGuid=task.ownerGuid,
            anchor=task.anchor,responseYards=task.responseYards,reason=task.finishing,
            deadlineMonotonicMs=task.deadline,error=task.refusal}
end
local function pulled_to_anchor(obs,task,targets)
    for target,npc in pairs(targets or {}) do
        local attack=(obs.engagement or {})[target]
        if npc.engaged==true and alive(npc) and attack and
           (attack.victimGuid==task.ownerGuid or target==obs.observedOwnerFocus) and
           near(task.anchor,position((obs.targets or {})[target],obs.mapId),task.responseYards) then return target end
    end
end

-- Called once per current observation. Proposals do not reserve an operation:
-- commit() is called only if the encounter scheduler actually selects it.
function M.step(obs,frame,sequence,targets,in_combat,can_act)
    if not state.current and state.next then state.current,state.next=state.next,nil end
    local task=state.current
    if not task then return {status=state.inputError and {phase=state.inputError} or state.last} end
    local players=obs.players or {}
    local self=players[obs.selfGuid]
    local bound=task.sessionId and task or task.requested
    if bound and (bound.sessionId~=obs.sessionId or bound.selfGuid~=obs.selfGuid) then
        finish(task,"body_changed");pop(task)
        if state.next then state.retired[state.next.id]="body_changed";state.next=nil end
        return {status=state.last}
    end
    local clock=frame.clock
    local nav=obs.navigation or {}
    if type(clock)~="table" or not number(clock.monotonicMs) then return {status=status(task,"task_clock_unavailable")} end
    local now=clock.monotonicMs
    local owner=nav.movementOwner
    if type(owner)~="table" then return {status=status(task,"movement_owner_unavailable")} end
    if bound and (bound.visitId~=obs.visitId or bound.mapId~=obs.mapId) then finish(task,"visit_changed") end
    if self and self.aliveKnown==true and self.alive==false then finish(task,"dead") end
    if task.deadline and now>=task.deadline then finish(task,"deadline") end
    if task.ownerGuid and players[task.ownerGuid] and players[task.ownerGuid].aliveKnown==true and
       players[task.ownerGuid].alive==false then finish(task,"owner_dead") end
    if task.uncertain then
        task.uncertainUntil=task.uncertainUntil or now+M.tuning.lease_ms
        if now<task.uncertainUntil or owner.active==true then
            return {status=status(task,"reconciling_movement_ownership"),hold=true,task=task}
        end
        task.uncertain,task.uncertainUntil,task.token,task.granted=nil,nil,nil,nil
    end
    if task.released then pop(task);return {status=state.last} end
    if task.operation then return {status=status(task,"waiting_for_movement_ownership"),hold=true,task=task} end
    if task.token and (owner.active~=true or owner.owner~="task:"..task.id) then
        task.token,task.granted=nil,nil
    end
    if task.anchor and not task.finishing then
        local current=position(self,obs.mapId)
        if current and not near(task.anchor,current,M.tuning.anchor_yards) then finish(task,"anchor_abandoned")
        elseif pulled_to_anchor(obs,task,targets) then finish(task,"pull_arrived") end
    end
    if task.finishing then
        if task.token and owner.active==true and owner.owner=="task:"..task.id then
            return {status=status(task,"releasing_movement_ownership"),task=task,
                    action=can_act and action(obs,task,"release") or nil}
        end
        if owner.active==true and owner.owner=="task:"..task.id then
            return {status=status(task,"waiting_for_owned_lease_expiry"),task=task}
        end
        pop(task);return {status=state.last}
    end
    if not alive(self) or not position(self,obs.mapId) then
        return {status=status(task,"waiting_for_body_observation"),task=task,hold=true}
    end
    if not task.sessionId then
        if in_combat then return {status=status(task,"waiting_for_current_encounter")} end
        task.ownerGuid=task.ownerGuid or guid(nav.ownerGuid)
        if not task.ownerGuid then return {status=status(task,"waiting_for_owner_observation")} end
        if not alive(self) or not position(self,obs.mapId) then return {status=status(task,"waiting_for_body_observation")} end
        task.sessionId,task.selfGuid,task.visitId,task.mapId=obs.sessionId,obs.selfGuid,obs.visitId,obs.mapId
        task.responseYards=task.responseYards or M.tuning.response_yards
    end
    if owner.active==true and owner.owner~="task:"..task.id then
        return {status=status(task,"movement_owned_elsewhere")}
    end
    if not task.token then
        if owner.active==true then return {status=status(task,"waiting_for_owned_lease_expiry"),task=task} end
        return {status=status(task,"acquiring_movement_ownership"),task=task,hold=true,
                action=can_act and action(obs,task,"acquire") or nil}
    end
    if not task.anchor then
        if self.movementKnown~=true or self.moving==true then
            local target=(obs.targetMovement or {}).targetGuid
            local stop=can_act and guid(target) and {capability="stop_movement",sessionId=obs.sessionId,
                selfGuid=obs.selfGuid,visitId=obs.visitId,observationSequence=sequence,targetGuid=target,rangeYards=0} or nil
            return {status=status(task,"waiting_for_stopped_anchor"),task=task,hold=true,action=stop}
        end
        task.anchor=position(self,obs.mapId)
    end
    local remaining=tonumber(owner.remainingMs)
    local renew=not number(remaining) or remaining<=M.tuning.renew_below_ms
    return {status=status(task,"waiting_for_owner_pull"),task=task,hold=true,
            action=can_act and renew and action(obs,task,"renew") or nil}
end

function M.commit(view,chosen)
    if view and view.task and chosen and chosen.capability=="movement_owner" then
        view.task.operation={mode=chosen.mode,owner=chosen.owner}
    end
end

-- Healing and actual attacks on a player at the anchor remain ordinary
-- encounter decisions. A distant pull is allowed to come to the held area.
function M.allow(view,obs,proposed)
    if not proposed or not view or not view.hold then return true end
    local cap=proposed.capability
    if cap=="movement_owner" or cap=="heal" or cap=="cleanse" or cap=="cast_self" or cap=="resurrect" or cap=="interrupt" or
       cap=="melee_stop" or cap=="stop_movement" then return true end
    local task=view.task
    local anchor=task and (task.anchor or position((obs.players or {})[obs.selfGuid],obs.mapId))
    local patient=(obs.players or {})[proposed.targetGuid]
    if cap=="approach_target" and patient and (patient.alive==false or
       patient.healthKnown==true and number(patient.health) and number(patient.maxHealth) and patient.health<patient.maxHealth) then return true end
    if cap=="approach_target" then
        for _,row in ipairs((obs.support or {}).resurrections or {}) do
            if proposed.targetGuid==row.patientGuid or proposed.targetGuid==row.castTargetGuid then return true end
        end
        for _,row in ipairs((obs.healing or {}).patients or {}) do
            if proposed.targetGuid==row.guid and number(row.health) and number(row.maxHealth) and row.health<row.maxHealth then return true end
        end
    end
    local npc=(obs.targets or {})[proposed.targetGuid]
    local range=task and (task.responseYards or M.tuning.response_yards)
    if anchor and near(anchor,position(npc,obs.mapId),range) then return true end
    local attack=(obs.engagement or {})[proposed.targetGuid]
    local victim=attack and (obs.players or {})[attack.victimGuid]
    if cap=="move_body" and anchor then
        for _,incoming in pairs(obs.engagement or {}) do
            local patient=(obs.players or {})[incoming.victimGuid]
            if alive(patient) and near(anchor,position(patient,obs.mapId),range) then return true end
        end
    end
    return anchor and alive(victim) and near(anchor,position(victim,obs.mapId),range) or false
end

function M.export_state() return {version=1,data=state} end
function M.import_state(checkpoint)
    if type(checkpoint)~="table" or checkpoint.version~=1 or type(checkpoint.data)~="table" or
       type(checkpoint.data.retired)~="table" then return false end
    for _,task in pairs({checkpoint.data.current,checkpoint.data.next}) do
        if type(task)~="table" or not id(task.id) or task.kind~="wait_for_pull" or
           task.token~=nil and (type(task.token)~="string" or #task.token==0) or
           task.deadline~=nil and (not number(task.deadline) or task.deadline<0) then return false end
    end
    state=checkpoint.data
    return true
end
return M
