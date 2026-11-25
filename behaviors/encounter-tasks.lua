-- Temporary script tasks. The host transports structured intent and finite
-- movement ownership; this module owns the task's conditions and lifecycle.
local M = {tuning={lease_ms=3000,renew_below_ms=1500,response_yards=10,anchor_yards=2}}
local state = {retired={}}
local instance_entry = encounter_instance_entry
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
local function witnessed_position(player,map)
    if player and player.positionEstimated~=true and player.positionInferred~=true then return position(player,map) end
end
local function finish(task,reason) task.finishing=task.finishing or reason end
local function pop(task)
    state.last={id=task.id,kind=task.kind,phase="complete",reason=task.finishing}
    if task.kind=="enter_instance" then state.retired[task.id]={reason=task.finishing,task=task}
    else state.retired[task.id]=task.finishing end
    state.current=nil
end
local function request(v)
    if not id(v.id) or v.ownerGuid~=nil and not guid(v.ownerGuid) or
       v.responseYards~=nil and (not number(v.responseYards) or v.responseYards<=0) or
       v.deadlineMonotonicMs~=nil and (not number(v.deadlineMonotonicMs) or v.deadlineMonotonicMs<0) then return end
    if v.task=="rally_to" then
        if not guid(v.guideGuid) or not v.deadlineMonotonicMs or
           v.destinationGuid~=nil and not guid(v.destinationGuid) then return end
        return {id=v.id,kind=v.task,guideGuid=v.guideGuid,destinationGuid=v.destinationGuid,
                responseYards=v.responseYards or 5,deadline=v.deadlineMonotonicMs}
    end
    return {id=v.id,kind="wait_for_pull",ownerGuid=v.ownerGuid,responseYards=v.responseYards,
            deadline=v.deadlineMonotonicMs,stage="requested"}
end

function M.on_event(event)
    if event.kind=="encounter_observation" and type(event.value)=="table" then
        local o=event.value
        state.observed={sessionId=o.sessionId,selfGuid=o.selfGuid,visitId=o.visitId,mapId=o.mapId}
    end
    local task=state.current
    if task and task.kind=="enter_instance" and instance_entry then instance_entry.on_event(task,event) end
    local receipt=event.receipt
    if task and task.kind~="enter_instance" and task.operation and receipt then
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
    elseif v.task=="wait_for_pull" or v.task=="enter_instance" or v.task=="rally_to" then
        local next_task
        if v.task=="enter_instance" then next_task=instance_entry and instance_entry.request(v,state.observed)
        else next_task=request(v) end
        if not next_task then state.inputError="invalid_"..v.task.."_intent"
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
            guideGuid=task.guideGuid,destinationGuid=task.destinationGuid,
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
function M.step(obs,frame,sequence,targets,in_combat,can_act,pause_rally)
    if not state.current and state.next then state.current,state.next=state.next,nil end
    local task=state.current
    if not task then return {status=state.inputError and {phase=state.inputError} or state.last} end
    if task.kind=="enter_instance" then
        local view=instance_entry and instance_entry.step(task,obs,frame,sequence,in_combat,can_act)
        if not view then return {status={phase="instance_entry_handler_unavailable"},hold=true,task=task} end
        if view.done then pop(task) end
        return view
    end
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
    if task.released then
        if task.pausedForEncounter and not task.finishing then
            task.released,task.granted,task.approached,task.stopIssued=nil,nil,nil,nil
        else pop(task);return {status=state.last} end
    end
    task.observationSequence=sequence
    if task.operation then
        local op=task.operation
        -- A lost/malformed custody receipt is not permission to replay it.
        -- Only fresh observations of an empty native ledger and absent lease,
        -- across the entire finite lease horizon, can retire this orphan.
        local pending=frame.pending
        local quiet=type(pending)=="table" and #pending==0 and owner.active~=true and
            nav.motionUnconfirmed~=true and self and self.movementKnown==true and self.moving~=true
        if not quiet or op.observationSequence==sequence or
           op.orphanedAt and (not number(op.orphanedAt) or now<op.orphanedAt) then
            op.orphanedAt,op.orphanedSequence=nil,nil
        elseif not op.orphanedAt then
            op.orphanedAt,op.orphanedSequence=now,sequence
        elseif sequence~=op.orphanedSequence and now-op.orphanedAt>=
               (number(op.leaseMs) and op.leaseMs>0 and op.leaseMs or M.tuning.lease_ms) then
            task.operation,task.token,task.granted=nil,nil,nil
            task.refusal="movement_lease_absence_observed_after_orphaned_receipt"
        end
        if task.operation then return {status=status(task,"waiting_for_movement_ownership"),hold=true,task=task} end
    end
    if task.token and (owner.active~=true or owner.owner~="task:"..task.id) then
        task.token,task.granted=nil,nil
    end
    if task.anchor and not task.finishing then
        local current=position(self,obs.mapId)
        if current and not near(task.anchor,current,M.tuning.anchor_yards) then finish(task,"anchor_abandoned")
        elseif pulled_to_anchor(obs,task,targets) then finish(task,"pull_arrived") end
    end
    local rally=task.kind=="rally_to"
    if rally and not task.finishing then
        local guide=players[task.guideGuid]
        local destination=players[task.destinationGuid or task.guideGuid]
        if in_combat then finish(task,"combat")
        elseif task.guideGuid==obs.selfGuid or task.destinationGuid==obs.selfGuid then finish(task,"self_target")
        elseif guide and guide.aliveKnown==true and guide.alive==false then finish(task,"guide_dead")
        elseif alive(self) and self.movementKnown==true and self.moving~=true and alive(destination) and
               near(witnessed_position(self,obs.mapId),witnessed_position(destination,obs.mapId),task.responseYards) then
            finish(task,"rendezvous_observed")
        end
    end
    if rally and not task.finishing then task.pausedForEncounter=pause_rally==true or nil end
    if task.finishing or task.pausedForEncounter then
        if task.token and owner.active==true and owner.owner=="task:"..task.id then
            if rally and bound and bound.visitId==obs.visitId and bound.mapId==obs.mapId then
                if nav.motionUnconfirmed==true or not self or self.movementKnown~=true then
                    return {status=status(task,"waiting_for_motion_confirmation"),task=task}
                end
                if self.moving==true then
                    local motion=obs.targetMovement or {}
                    local own=task.approached and not task.stopIssued and motion.targetGuid==task.guideGuid and motion.visitId==obs.visitId
                    return {status=status(task,"waiting_for_stopped_rally"),task=task,
                            action=can_act and own and encounter_movement.stop(obs,sequence,task.guideGuid) or nil}
                end
            end
            return {status=status(task,"releasing_movement_ownership"),task=task,
                    action=can_act and action(obs,task,"release") or nil}
        end
        if owner.active==true and owner.owner=="task:"..task.id then
            return {status=status(task,"waiting_for_owned_lease_expiry"),task=task}
        end
        if task.pausedForEncounter and not task.finishing then
            return {status=status(task,"paused_for_nearby_hazard"),task=task,hold=true}
        end
        pop(task);return {status=state.last}
    end
    if not alive(self) or not position(self,obs.mapId) then
        return {status=status(task,"waiting_for_body_observation"),task=task,hold=true}
    end
    if not task.sessionId then
        if in_combat then return {status=status(task,"waiting_for_current_encounter")} end
        if not rally then
            task.ownerGuid=task.ownerGuid or guid(nav.ownerGuid)
            if not task.ownerGuid then return {status=status(task,"waiting_for_owner_observation")} end
        end
        if not alive(self) or not position(self,obs.mapId) then return {status=status(task,"waiting_for_body_observation")} end
        task.sessionId,task.selfGuid,task.visitId,task.mapId=obs.sessionId,obs.selfGuid,obs.visitId,obs.mapId
        task.responseYards=task.responseYards or M.tuning.response_yards
    end
    if owner.active==true and owner.owner~="task:"..task.id then
        return {status=status(task,"movement_owned_elsewhere"),task=task}
    end
    if rally and (not alive(players[task.guideGuid]) or not witnessed_position(players[task.guideGuid],obs.mapId)) then
        if task.token then finish(task,"guide_lost") end
        return {status=status(task,"waiting_for_visible_guide"),task=task,hold=true}
    end
    if not task.token then
        if owner.active==true then return {status=status(task,"waiting_for_owned_lease_expiry"),task=task} end
        return {status=status(task,"acquiring_movement_ownership"),task=task,hold=true,
                action=can_act and action(obs,task,"acquire") or nil}
    end
    if rally then
        local view={status=status(task,"walking_to_guide"),task=task,hold=true}
        if nav.motionUnconfirmed==true then view.status.phase="waiting_for_motion_confirmation";return view end
        local remaining=tonumber(owner.remainingMs)
        if not number(remaining) or remaining<=M.tuning.renew_below_ms then
            if can_act then view.action=action(obs,task,"renew") end
        elseif near(position(self,obs.mapId),position(players[task.guideGuid],obs.mapId),task.responseYards)
               and self.movementKnown==true and self.moving~=true then
            view.status.phase=witnessed_position(self,obs.mapId) and "waiting_with_guide" or "waiting_for_position_witness"
        elseif can_act then
            view.action=encounter_movement.decide(obs,sequence,task.guideGuid,
                {target=players[task.guideGuid],rangeYards=task.responseYards}).action
        end
        return view
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
    if view and view.task and view.task.kind=="rally_to" and chosen==view.action and chosen then
        if chosen.capability=="approach_target" then view.task.approached=true
        elseif chosen.capability=="stop_movement" then view.task.stopIssued=true end
    end
    if view and view.task and view.task.kind=="enter_instance" and instance_entry then
        instance_entry.commit(view.task,chosen);return
    end
    if view and view.task and chosen and chosen.capability=="movement_owner" then
        view.task.operation={mode=chosen.mode,owner=chosen.owner,leaseMs=chosen.leaseMs,
            observationSequence=view.task.observationSequence}
    end
end

-- Healing and actual attacks on a player at the anchor remain ordinary
-- encounter decisions. A distant pull is allowed to come to the held area.
function M.allow(view,obs,proposed)
    if not proposed or not view or not view.hold then return true end
    local cap=proposed.capability
    if view.task and view.task.kind=="rally_to" and
       (view.action==proposed or view.task.pausedForEncounter and view.rangedWithdrawal==proposed) then return true end
    if view.task and view.task.kind=="enter_instance" then
        if view.action==proposed then return true end
        -- Entry owns voluntary travel only; current defense and healing retain
        -- their ordinary scheduler and native custody checks.
        return cap~="approach_target" and cap~="move_body" or false
    end
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
        local rally=type(task)=="table" and task.kind=="rally_to" and guid(task.guideGuid) and
            (task.destinationGuid==nil or guid(task.destinationGuid)) and number(task.responseYards) and
            task.responseYards>0 and number(task.deadline)
        if type(task)~="table" or not id(task.id) or (task.kind~="wait_for_pull" and not rally and (not instance_entry or not instance_entry.validate(task))) or
           task.pausedForEncounter~=nil and type(task.pausedForEncounter)~="boolean" or
           task.token~=nil and (type(task.token)~="string" or #task.token==0) or
           task.deadline~=nil and (not number(task.deadline) or task.deadline<0) then return false end
    end
    state=checkpoint.data
    return true
end
return M
