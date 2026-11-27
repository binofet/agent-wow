-- An explicit owner destination is intent, never an owner-location witness.
-- This frame belongs to encounter-tasks' existing checkpoint/action ledger.
local pi=3.141592653589793
local M={tuning={lease_ms=30000,renew_below_ms=28000,regroup_yards=20,inward_margin=.25}}
local function finite(n) return type(n)=="number" and n==n and n>-math.huge and n<math.huge end
local function uint(n) return finite(n) and n>=0 and n<=4294967295 and n==math.floor(n) end
local function ident(s,zero)
    if type(s)~="string" or #s==0 or #s>20 or #s>1 and byte_at(s,1)==48 then return false end
    for i=1,#s do local b=byte_at(s,i);if b<48 or b>57 then return false end end
    return (zero or s~="0") and (#s<20 or s<="18446744073709551615")
end
local function later_visit(current,prior)
    return ident(current) and ident(prior) and (#current>#prior or #current==#prior and current>prior)
end
local function taskid(s)
    if type(s)~="string" or #s==0 or #s>80 then return false end
    for i=1,#s do
        local b=byte_at(s,i)
        if not (b>=48 and b<=57 or b>=65 and b<=90 or b>=97 and b<=122 or b==45 or b==46 or b==58 or b==95) then return false end
    end
    return true
end
local function point(p) return type(p)=="table" and finite(p.x) and finite(p.y) and finite(p.z) end
local function contains(v,p)
    if not point(p) then return false end
    local dx,dy,dz=(p.x or 0)-v.x,(p.y or 0)-v.y,(p.z or 0)-v.z
    local m=M.tuning.inward_margin
    if v.radius>0 then return v.radius>m and dx*dx+dy*dy+dz*dz<=(v.radius-m)^2 end
    local c,s=math.cos(v.orientation),math.sin(v.orientation)
    return v.length/2>m and v.width/2>m and v.height/2>m and
        math.abs(dx*c+dy*s)<=v.length/2-m and math.abs(-dx*s+dy*c)<=v.width/2-m and math.abs(dz)<=v.height/2-m
end
local function allowed(task,id)
    for _,value in ipairs(task.allowedTriggerIds or {}) do if value==id then return true end end
    return false
end
function M.request(v,observed)
    if not taskid(v.id) or not ident(v.ownerGuid) or not uint(v.destinationMapId) or
       type(v.allowedTriggerIds)~="table" or #v.allowedTriggerIds<1 or #v.allowedTriggerIds>16 or
       v.triggerId~=nil and (not uint(v.triggerId) or v.triggerId==0) then return end
    for _,id in ipairs(v.allowedTriggerIds) do if not uint(id) or id==0 then return end end
    if not observed or not ident(observed.selfGuid) or not ident(observed.visitId) or type(observed.sessionId)~="string" or
       observed.mapId==v.destinationMapId or v.sessionId~=nil and v.sessionId~=observed.sessionId or
       v.selfGuid~=nil and v.selfGuid~=observed.selfGuid or v.visitId~=nil and v.visitId~=observed.visitId then return end
    if v.startRecovery~=nil and (type(v.startRecovery)~="table" or not finite(v.startRecovery.absoluteBearingRadians) or
       math.abs(v.startRecovery.absoluteBearingRadians)>pi or not uint(v.startRecovery.durationMs) or
       v.startRecovery.durationMs<1 or v.startRecovery.durationMs>500) then return end
    if v.routeGoal~=nil and not point(v.routeGoal) then return end
    local task={id=v.id,kind="enter_instance",ownerGuid=v.ownerGuid,destinationMapId=v.destinationMapId,
        triggerId=v.triggerId,allowedTriggerIds=v.allowedTriggerIds,requested=observed,sessionId=observed.sessionId,
        selfGuid=observed.selfGuid,visitId=observed.visitId,mapId=observed.mapId,stage="status",serial=0,
        startRecovery=v.startRecovery,routeGoal=v.routeGoal,history={}}
    if task.triggerId and not allowed(task,task.triggerId) then return end
    return task
end
function M.validate(task)
    local saved=task.canceledApproach
    return task.kind=="enter_instance" and taskid(task.id) and ident(task.selfGuid) and ident(task.visitId) and ident(task.ownerGuid) and
        type(task.sessionId)=="string" and uint(task.destinationMapId) and type(task.allowedTriggerIds)=="table" and
        type(task.history)=="table" and type(task.serial)=="number" and (not task.operation or type(task.operation.capability)=="string") and
        (not saved or type(saved)=="table" and later_visit(saved.observedVisit,task.visitId) and
         (not saved.cleanupVisit and saved.cleanupMapId==nil or
         later_visit(saved.cleanupVisit,saved.observedVisit) and uint(saved.cleanupMapId)))
end
local function base(task,obs,cap)
    return {capability=cap,sessionId=task.sessionId,selfGuid=task.selfGuid,visitId=obs.visitId}
end
local function result_bound(task,r) return r and r.sessionId==task.sessionId and r.selfGuid==task.selfGuid end
local function retire_absent_lease(task,refusal)
    task.lastLease={token=task.token,visitId=task.leaseVisit,absentOrReplacedObserved=true,releaseRefusal=refusal}
    task.token,task.leaseVisit=nil,nil
end
local function observe_operation(task,r)
    if not result_bound(task,r) or not ident(r.revision,true) then task.error="trigger_observation_unbound";return end
    task.nativeRevision=r.revision
    if task.notifyId then
        if r.clientRequestId~=task.notifyId or r.visitId~=task.visitId or r.triggerId~=task.volume.triggerId then
            task.error="trigger_operation_identity_unavailable";return
        end
        task.native=r
        if r.destinationObserved==true and r.pending~=true and ident(r.observedVisitId) and r.observedVisitId~=task.visitId then
            task.destinationVisit=r.observedVisitId;task.stage="regroup"
        elseif r.notDispatched==true and r.pending~=true then
            task.error=r.error or "trigger_not_dispatched";task.blocked=true
        else task.stage="observe_trigger" end
    elseif r.pending==true then task.error="another_trigger_attempt_pending";task.blocked=true
    else task.stage="prepare" end
end
function M.on_event(task,event)
    local receipt=event.receipt;local op=task.operation
    if not receipt or not op then return end
    local p=receipt.payload or {};if p.capability~=op.capability then return end
    local r=p.result;local outcome=receipt.outcome
    local known_trigger=(op.capability=="notify_area_trigger" or op.capability=="area_trigger_observation") and type(r)=="table"
    if outcome=="unknown" or p.reconciliationRequired==true and not known_trigger then
        task.history[#task.history+1]={operation=op,receipt=receipt}
        task.operation=nil
        if op.capability=="notify_area_trigger" then task.stage="observe_trigger"
        else task.uncertain=op;task.error="operation_reply_unknown" end
        return
    end
    if outcome=="not_dispatched" or outcome=="failed" then
        task.operation=nil;task.error=p.error or p.grpc_message or outcome
        if op.capability=="release_interaction" then
            -- Actions checks the exact token before attempting its physical STOP.
            -- A new exploration lease can have the same public owner label.
            if task.finishing=="canceled" and task.canceledApproach and task.token and op.token==task.token and
               op.owner=="exploration" and op.sessionId==task.sessionId and op.selfGuid==task.selfGuid and
               p.self_guid==op.selfGuid and p.visit_id==op.visitId and
               outcome=="not_dispatched" and p.phase=="admission_rejected" and p.transport_written==false and
               p.grpc_status=="FailedPrecondition" and p.grpc_message=="interaction lease does not match" then
                retire_absent_lease(task,receipt)
            else task.releaseRefused=true end
            task.cleanupStatus=nil
        else task.blocked=true end
        return
    end
    if outcome~="confirmed" and outcome~="settled_unverified" then return end
    task.operation=nil
    if op.capability=="instance_entry_status" then
        if not result_bound(task,r) or type(r.position)~="table" then task.error="status_reply_unbound";task.blocked=true;return end
        task.actions=r
        if op.cleanup then task.cleanupStatus={visitId=op.visitId,state=r} end
        if task.stage=="status" then task.stage="references" elseif task.stage=="refresh" then task.stage=task.afterRefresh end
    elseif op.capability=="area_trigger_reference" then
        if not result_bound(task,r) or r.visitId~=task.visitId then task.error="reference_reply_unbound";task.blocked=true;return end
        local chosen,distance
        for _,v in ipairs(r.volumes or {}) do
            if v.referenceOnly==true and v.sourceMapId==task.mapId and v.destinationMapId==task.destinationMapId and
               allowed(task,v.triggerId) and (not task.triggerId or v.triggerId==task.triggerId) and point(v) then
                local pos=task.actions.position
                local d=((pos.x or 0)-v.x)^2+((pos.y or 0)-v.y)^2+((pos.z or 0)-v.z)^2
                if not distance or d<distance then chosen,distance=v,d end
            end
        end
        if not chosen then task.error="allowed_reference_unavailable";task.blocked=true;return end
        task.volume=chosen
        if task.routeGoal and not contains(chosen,task.routeGoal) then task.error="route_goal_outside_reference";task.blocked=true;return end
        task.stage="observe_trigger"
    elseif op.capability=="area_trigger_observation" or op.capability=="notify_area_trigger" then observe_operation(task,r)
    elseif op.capability=="acquire_idle_interaction" then
        if not r or type(r.token)~="string" or r.token=="" or not result_bound(task,r.state) or
           r.state.interactionOwner~="exploration" or op.token and op.token~=r.token then
            task.uncertain=op;task.error="lease_reply_unbound";return
        end
        task.token=r.token;task.leaseVisit=op.visitId;task.actions=r.state;task.cleanupStatus=nil;task.releaseRefused=nil;task.stage=task.afterLease or "prepare"
    elseif op.capability=="release_interaction" then
        if p.phase~="interaction_released" then task.uncertain=op;task.error="release_reply_unverified";return end
        task.lastLease={token=task.token,visitId=task.leaseVisit};task.token=nil;task.leaseVisit=nil
    elseif op.capability=="plan_route_leg" then
        if not result_bound(task,r) or r.mapId~=task.mapId or not point(r.endpoint) or
           not r.start or r.start.revision~=op.expectedPositionRevision then task.error="route_reply_unbound";task.blocked=true;return end
        task.route=r;task.stage="travel"
    elseif op.capability=="travel_to" then
        if not result_bound(task,r) or r.motionUnconfirmed==true then
            task.uncertain=op;task.history[#task.history+1]={operation=op,receipt=receipt};task.error="travel_effect_unconfirmed";return
        end
        task.route=nil;task.actions=r;task.stage="refresh";task.afterRefresh="prepare"
    elseif op.capability=="move_body" then
        if p.transport_written~=true or p.phase~="body_input_written" and p.phase~="body_settled_written" then
            task.uncertain=op;task.history[#task.history+1]={operation=op,receipt=receipt};task.error="body_recovery_write_unconfirmed";return
        end
        if p.phase=="body_settled_written" then
            if task.recoverySettled then task.blocked=true;task.error="recovery_requires_another_explicit_input" end
            task.recoverySettled=true
        else task.recoveryDone=true end
        task.stage="refresh";task.afterRefresh="prepare"
    end
end
function M.commit(task,chosen)
    if not chosen then return end
    local op={};for k,v in pairs(chosen) do op[k]=v end
    if task.cleanupRead and chosen.capability=="instance_entry_status" then op.cleanup=true;task.cleanupRead=nil end
    task.operation=op
    if chosen.capability=="notify_area_trigger" then task.serial=task.serial+1;task.notifyId=chosen.clientRequestId end
end
local function view(task,phase,action)
    return {task=task,hold=true,action=action,status={id=task.id,kind=task.kind,phase=phase,error=task.error,
        destinationMapId=task.destinationMapId,triggerId=task.volume and task.volume.triggerId,
        clientRequestId=task.notifyId,delivery=task.native,uncertain=task.uncertain~=nil}}
end
local function request(task,obs,cap) return base(task,obs,cap) end
local function release(task,obs)
    local a=request(task,obs,"release_interaction");a.owner="exploration";a.token=task.token;return a
end
local function cleanup(task,obs)
    if not task.token then return nil,true end
    local checked=task.cleanupStatus
    if not checked or checked.visitId~=obs.visitId then
        task.cleanupRead=true
        return request(task,obs,"instance_entry_status"),false
    end
    local owner=checked.state.interactionOwner or ""
    if owner=="" or owner~="exploration" then
        -- An observed absent/replaced lease retires only this local token.
        -- No ambiguous acquisition/release or trigger attempt is cleared.
        retire_absent_lease(task);return nil,true
    end
    if task.releaseRefused then
        task.cleanupStatus=nil;task.cleanupRead=true
        return request(task,obs,"instance_entry_status"),false
    end
    return release(task,obs),false
end
local function regroup(task,obs)
    local a,b=(obs.players or {})[obs.selfGuid],(obs.players or {})[task.ownerGuid]
    if not a or not b or a.present~=true or b.present~=true or a.aliveKnown~=true or a.alive~=true or
       b.aliveKnown~=true or b.alive~=true or a.positionKnown~=true or b.positionKnown~=true or
       a.mapKnown~=true or b.mapKnown~=true or a.mapId~=task.destinationMapId or b.mapId~=task.destinationMapId or
       a.positionAgeKnown~=true or b.positionAgeKnown~=true or a.positionInferred==true or b.positionInferred==true or
       a.positionEstimated==true or b.positionEstimated==true or not ident(a.positionAgeMs,true) or not ident(b.positionAgeMs,true) or tonumber(a.positionAgeMs)>2000 or tonumber(b.positionAgeMs)>2000 then return false end
    return ((a.x or 0)-(b.x or 0))^2+((a.y or 0)-(b.y or 0))^2+((a.z or 0)-(b.z or 0))^2<=M.tuning.regroup_yards^2
end
local function canceled_cleanup_visit(task,obs,self)
    local saved=task.canceledApproach
    if not self or self.present~=true or self.mapKnown~=true or not uint(obs.mapId) or self.mapId~=obs.mapId then return false end
    local prior=saved.cleanupVisit or saved.observedVisit
    if obs.visitId==prior then return obs.mapId==(saved.cleanupMapId or task.destinationMapId) end
    if not later_visit(obs.visitId,prior) then return false end
    -- The original observed arrival stays in history. A later visit must use
    -- its own current status before the task releases its original lease.
    saved.cleanupVisit,saved.cleanupMapId=obs.visitId,obs.mapId
    return true
end
function M.step(task,obs,frame,sequence,in_combat,can_act)
    local function propose(phase,a) return view(task,phase,can_act and a or nil) end
    if obs.sessionId~=task.sessionId or obs.selfGuid~=task.selfGuid then
        task.finishing="body_changed";return view(task,"body_changed_custody_retained")
    end
    local self=(obs.players or {})[obs.selfGuid]
    if self and self.aliveKnown==true and self.alive==false then task.finishing=task.finishing or "dead" end
    -- A canceled walk on the old map cannot arrive at its old coordinates
    -- after another transfer. Keep its unsettled operation in the checkpoint.
    local walk=task.operation or task.uncertain
    if task.finishing=="canceled" and not task.notifyId and not task.destinationVisit and not task.canceledApproach and
       later_visit(obs.visitId,task.visitId) and
       obs.mapId==task.destinationMapId and self and self.present==true and self.mapKnown==true and self.mapId==obs.mapId and
       type(frame.pending)=="table" and next(frame.pending)==nil and not (task.operation and task.uncertain) and
       (not walk or walk.capability=="travel_to" and walk.sessionId==task.sessionId and walk.selfGuid==task.selfGuid and
        walk.visitId==task.visitId and walk.mapId==task.mapId and walk.token==task.token and task.token) then
        task.canceledApproach={observedVisit=obs.visitId,operation=task.operation,uncertain=task.uncertain}
        task.operation,task.uncertain=nil,nil
    end
    if task.operation then return view(task,"waiting_for_"..task.operation.capability) end
    if task.uncertain then return view(task,"reconcile_"..task.uncertain.capability) end
    if task.notifyId and not task.destinationVisit and not (task.native and task.native.notDispatched==true) then
        return propose("observing_trigger_delivery",request(task,obs,"area_trigger_observation"))
    end
    if task.destinationVisit or task.canceledApproach then
        if task.destinationVisit and (obs.visitId~=task.destinationVisit or obs.mapId~=task.destinationMapId) or
           task.canceledApproach and not canceled_cleanup_visit(task,obs,self) then return view(task,"waiting_for_destination_observation") end
        local a,done=cleanup(task,obs)
        if not done then return propose("reconciling_old_visit_custody",a) end
        if task.finishing or regroup(task,obs) then task.finishing=task.finishing or "regroup_observed";return {task=task,done=true,status={id=task.id,kind=task.kind,phase="complete",reason=task.finishing}} end
        local waiting=view(task,"waiting_for_fresh_owner_regroup")
        -- The crossing and parent cleanup are observed. Resume the unchanged
        -- normal follow intent while separately awaiting defensible regroup.
        waiting.hold=false
        return waiting
    end
    if obs.visitId~=task.visitId or obs.mapId~=task.mapId then return view(task,"unlinked_visit_change_custody_retained") end
    if task.finishing or in_combat then
        local a,done=cleanup(task,obs)
        if not done then return propose("reconciling_entry_custody",a) end
        if task.finishing then return {task=task,done=true,status={id=task.id,kind=task.kind,phase="complete",reason=task.finishing}} end
        return view(task,"waiting_for_current_encounter")
    end
    if task.blocked then
        local a,done=cleanup(task,obs)
        if not done then return propose("reconciling_refused_entry_custody",a) end
        return view(task,"entry_refused_inspect_before_retry")
    end
    if task.stage=="status" or task.stage=="refresh" then return propose("reading_current_entry_status",request(task,obs,"instance_entry_status")) end
    if task.stage=="references" then local a=request(task,obs,"area_trigger_reference");a.destinationMapId=task.destinationMapId;return propose("reading_allowed_entry_references",a) end
    if task.stage=="observe_trigger" then return propose("reading_trigger_operation",request(task,obs,"area_trigger_observation")) end
    local state=task.actions or {};local p=state.position or {}
    if state.sessionId~=task.sessionId or state.selfGuid~=task.selfGuid or p.known~=true or p.mapKnown~=true or p.mapId~=task.mapId or not ident(p.revision) then
        task.afterRefresh=task.stage=="travel" and "travel" or "prepare";task.stage="refresh"
        return view(task,"current_client_position_required")
    end
    local owner=state.interactionOwner or "";local ms=tonumber(state.interactionLeaseRemainingMs or "0")
    if owner~="" and (owner~="exploration" or not task.token) then return view(task,"foreign_interaction_custody") end
    if owner=="" and task.token then task.error="owned_lease_lapsed";task.blocked=true;return view(task,"owned_lease_lapsed") end
    if not task.token or not ms or ms<=M.tuning.renew_below_ms then
        local a=request(task,obs,"acquire_idle_interaction");a.owner="exploration";a.token=task.token;a.leaseMs=M.tuning.lease_ms
        task.afterLease=task.stage;return propose(task.token and "renewing_entry_custody" or "acquiring_entry_custody",a)
    end
    if task.startRecovery and not task.recoveryDone then
        local self=(obs.players or {})[obs.selfGuid]
        if not self or not finite(self.orientation) then return view(task,"actual_orientation_required_for_recovery") end
        local bearing=(task.startRecovery.absoluteBearingRadians-self.orientation+pi)%(2*pi)-pi
        local a=request(task,obs,"move_body");a.bearingRadians=bearing;a.durationMs=task.startRecovery.durationMs;a.jump=false
        a.sequenceId=sequence;a.idleToken=task.token
        return propose("executing_explicit_measured_start_recovery",a)
    end
    if contains(task.volume,p) then
        local a=request(task,obs,"notify_area_trigger");a.token=task.token;a.triggerId=task.volume.triggerId
        a.expectedPositionRevision=p.revision;a.expectedRevision=task.nativeRevision
        a.clientRequestId=task.id..":"..(task.serial+1)
        return propose("notifying_actual_actor_in_volume",a)
    end
    if task.stage=="travel" and task.route then
        local endpoint=task.route.endpoint
        local a=request(task,obs,"travel_to");a.token=task.token;a.mapId=task.mapId
        a.x,a.y,a.z=endpoint.x or 0,endpoint.y or 0,endpoint.z or 0;a.arrivalDistance=.5
        return propose("walking_verified_entry_leg",a)
    end
    local a=request(task,obs,"plan_route_leg");a.expectedPositionRevision=p.revision;a.mapId=task.mapId
    local goal=task.routeGoal or task.volume;a.x,a.y,a.z=goal.x,goal.y,goal.z
    return propose("planning_complete_ground_entry_route",a)
end
return M
