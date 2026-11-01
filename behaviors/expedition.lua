-- All mission identities and candidate references come from the goal, while
-- actual giver identity, availability and completion come from current APIs.
local function number(v) return tonumber(v) or 0 end
local function equal(a,b) return tostring(a or "") == tostring(b or "") end
local function contains(values, wanted)
    for _,value in ipairs(values or {}) do if equal(value,wanted) then return true end end
    return false
end
local function flag(value, bit) return math.floor(number(value)/bit)%2 == 1 end
local function position(context)
    local p = (context.observations.actions or {}).position or {}
    if p.known and p.mapKnown then return p end
end
local function distance(a,b)
    return math.sqrt((number(a.x)-number(b.x))^2+(number(a.y)-number(b.y))^2+(number(a.z)-number(b.z))^2)
end
local function quiet(context, allowLease)
    local a, w = context.observations.actions or {}, (context.observations.world or {}).self or {}
    if not equal(a.sessionId,context.state.body.sessionId) or not equal(a.selfGuid,context.state.body.selfGuid)
       or not equal(w.guid,context.state.body.selfGuid) or not w.present or not w.aliveKnown or not w.alive
       or not w.mapKnown or not w.unitFlagsKnown or flag(w.unitFlags,0x80000)
       or not w.healthKnown or number(w.maxHealth)<=0 or number(w.health)/number(w.maxHealth)<0.85 then return false end
    for _,bit in ipairs({1,4,0x40000,0x100000,0x400000,0x800000,0x1000000,0x2000000,0x8000000}) do
        if flag(w.unitFlags,bit) then return false end
    end
    local movement=number(w.movementFlags)
    if not w.movementFlagsKnown or movement<0 or movement>=0x80000000
       or movement%0x10000000>0x1ff or number(w.extraMovementFlags)~=0 then return false end
    if a.castPending or a.autoAttacking or a.autoAttackRequested or a.autoAttackUnconfirmed
       or a.motionUnconfirmed or a.moving or a.traveling or (a.recovery or {}).active then return false end
    return position(context) ~= nil and (allowLease or (a.interactionOwner or "") == "")
end
local function journal(context)
    local q = context.observations.quests or {}
    if not equal(q.sessionId,context.state.body.sessionId) or not q.questLogKnown
       or number(q.questLogRevision)<=0 or #(q.questLog or {})~=25 then return nil end
    local seen = {}
    for _,row in ipairs(q.questLog) do
        local id = number(row.questId)
        if id>0 then if seen[tostring(id)] then return nil end; seen[tostring(id)]=row end
    end
    return q,seen
end
local function giver(context, entry)
    local w = (context.observations.world or {}).self or {}
    local found
    for _,near in ipairs((context.observations.quests or {}).nearby or {}) do
        local npc=near.creature or {}
        if equal(npc.entry,entry) and npc.present and npc.aliveKnown and npc.alive and npc.mapKnown
           and equal(npc.mapId or 0,w.mapId or 0) and npc.positionKnown and number(npc.positionRevision)>0
           and npc.unitFlagsKnown and not flag(npc.unitFlags,0x80000) and flag(npc.npcFlags,2)
           and type(npc.guid)=="string" and npc.guid~="0" then
            if found then return nil end -- Ambiguous observed entry is not an interaction binding.
            found=near
        end
    end
    return found
end
local function reward_proof(context, frame)
    local q,log=journal(context)
    local receipt=(q or {}).lastReward or {}
    return q and frame.binding and equal(receipt.questId,frame.data.questId)
       and equal(receipt.npcGuid,frame.guid) and equal(receipt.offerRevision,frame.binding.offerRevision)
       and (receipt.status=="REWARD_CONFIRMED" or receipt.status==2)
       and receipt.serverRewardObserved and receipt.logRemovalConfirmed
       and equal(receipt.baselineLogRevision,frame.binding.baselineLogRevision)
       and number(receipt.confirmedLogRevision)>number(receipt.baselineLogRevision)
       and number(q.questLogRevision)>=number(receipt.confirmedLogRevision)
       and not log[tostring(frame.data.questId)] and receipt
end
local function acceptance_proof(context, frame)
    local q,log=journal(context)
    local receipt=(q or {}).lastAcceptance or {}
    return q and frame.binding and log[tostring(frame.data.questId)]
       and equal(receipt.questId,frame.data.questId) and equal(receipt.npcGuid,frame.guid)
       and equal(receipt.offerRevision,frame.binding.offerRevision)
       and (receipt.status=="CONFIRMED_IN_LOG" or receipt.status==2)
       and equal(receipt.baselineLogRevision,frame.binding.baselineLogRevision)
       and number(receipt.confirmedLogRevision)>number(receipt.baselineLogRevision)
       and number(q.questLogRevision)>=number(receipt.confirmedLogRevision) and receipt
end
local handlers,extensions={},{}
local function defer_child(context,frame,reason)
    -- Only a definite, fully idle child can yield to another quest. Pending
    -- calls, an owned/replacement lease and uncertain cleanup stay on stack.
    if frame.waiting or not quiet(context,false) or (context.observations.quests or {}).actionInFlight then
        return tasks.await(context,reason)
    end
    tasks.pop(context,{status="blocked",reason=reason,taskKind=frame.kind,data=frame.data})
    return tasks.await(context,"quest_child_deferred")
end
local function deferred(context,kind,id)
    local row=(context.memory.deferredQuests or {})[kind..":"..tostring(id)]
    return row and number(context.state.now)<number(row.retryAfter)
end

local function opportunity(context,frame)
    local entries=context.state.goal.params.opportunities or {}
    assert(type(entries)=="table" and #entries<=4,"bounded optional children required")
    context.memory.opportunityTasks=context.memory.opportunityTasks or {}
    if frame.opportunity and frame.child then
        local row=context.memory.opportunityTasks[frame.opportunity]
        row.status=frame.child.status;row.reason=frame.child.reason;row.confirmed=frame.child.confirmed
        frame.opportunity=nil;frame.child=nil
    end
    if frame.waiting or frame.token or frame.child or not quiet(context,false) then return false end
    local seen={}
    for _,entry in ipairs(entries) do
        local kind=entry.kind;local cooldown=entry.cooldownSeconds or 60;local limit=entry.maxRuns or 1
        assert(type(kind)=="string" and #kind<=48 and extensions[kind] and not seen[kind]
            and type(entry.params)=="table" and type(cooldown)=="number" and cooldown>=5 and cooldown<=300
            and type(limit)=="number" and limit%1==0 and limit>=1 and limit<=8,"registered bounded child required")
        seen[kind]=true
        local row=context.memory.opportunityTasks[kind] or {runs=0,nextAt=0}
        context.memory.opportunityTasks[kind]=row
        if number(row.runs)<limit and number(context.state.now)>=number(row.nextAt) then
            row.runs=number(row.runs)+1;row.nextAt=number(context.state.now)+cooldown
            frame.opportunity=kind;tasks.push(context,kind,entry.params);return true
        end
    end
    return false
end

-- Geometry is obtained before custody; it is never an observed pose or
-- permission to move. TravelTo independently plans the returned endpoint.
local function same_point(a,b)
    return type(a)=="table" and type(b)=="table" and a.known and a.mapKnown and b.known and b.mapKnown
        and equal(a.mapId or 0,b.mapId or 0) and number(a.x)==number(b.x)
        and number(a.y)==number(b.y) and number(a.z)==number(b.z)
end
local function geometry(p,map)
    if type(p)~="table" or not p.known or not p.mapKnown or not equal(p.mapId or 0,map or 0) then return false end
    for _,key in ipairs({"x","y","z"}) do
        if p[key]~=nil and (type(p[key])~="number" or p[key]~=p[key] or math.abs(p[key])>40000) then return false end
    end
    return true
end
local function travel_blocked(context,reason)
    tasks.pop(context,{status="blocked",reason=reason})
end
handlers.travel=function(context,frame)
    local a=context.observations.actions or {}
    if frame.stage=="start" then
        if opportunity(context,frame) then return end
        if not frame.waiting then
            if not quiet(context,false) then return tasks.await(context,"travel_waiting_for_safe_idle") end
            local p=position(context)
            if not equal(p.mapId or 0,frame.data.mapId or 0) then travel_blocked(context,"travel_map_change_required");return end
            if distance(p,frame.data)<=3 then tasks.pop(context,{status="arrived"});return end
            if distance(p,frame.data)>=1200 then travel_blocked(context,"travel_goal_outside_native_plan_bound");return end
            if number(frame.legs)>=32 then travel_blocked(context,"travel_leg_budget");return end
            frame.planStart={known=true,mapKnown=true,mapId=p.mapId or 0,x=number(p.x),y=number(p.y),z=number(p.z),revision=p.revision}
        end
        local start=frame.planStart
        local reply,out=tasks.call(context,frame,"buddy-actions.plan_route_leg",{sessionId=context.state.body.sessionId,
            selfGuid=context.state.body.selfGuid,expectedPositionRevision=start.revision,mapId=frame.data.mapId or 0,
            x=frame.data.x,y=frame.data.y,z=frame.data.z},"planning_verified_native_route_leg")
        if out then return out end
        if reply.readError then
            context.memory.lastRoutePlanError={error=reply.readError,goal=frame.data,at=context.state.now}
            travel_blocked(context,"travel_route_plan_unavailable");return
        end
        local p=position(context);local endpoint=reply.endpoint
        if not equal(reply.sessionId,context.state.body.sessionId) or not equal(reply.selfGuid,context.state.body.selfGuid)
           or not equal(reply.mapId or 0,frame.data.mapId or 0) or not same_point(reply.start,start)
           or not equal((reply.start or {}).revision,start.revision) or not same_point(p,start)
           or not equal(p.revision,start.revision) or number(reply.serverPositionRevision)<=0
           or not geometry(endpoint,frame.data.mapId) or number(endpoint.revision)~=0
           or number(reply.pointCount)<2 or number(reply.pointCount)>256
           or number(reply.pathLength)<=0 or number(reply.pathLength)>180
           or distance(start,endpoint)>180 or distance(start,endpoint)<=1 then
            travel_blocked(context,"travel_route_plan_binding_invalid");return
        end
        frame.destination={mapId=endpoint.mapId or 0,x=number(endpoint.x),y=number(endpoint.y),z=number(endpoint.z)}
        frame.stage="acquire"
    end
    if frame.stage=="acquire" then
        if not frame.waiting and (not quiet(context,false) or not same_point(position(context),frame.planStart)) then
            travel_blocked(context,"travel_pose_changed_before_custody");return
        end
        local reply,out=tasks.call(context,frame,"buddy-actions.acquire_interaction",
            {owner="exploration",leaseMs=30000},"acquiring_travel_custody")
        if out then return out end
        if type(reply.token)~="string" or reply.token=="" then return tasks.await(context,"travel_token_unavailable") end
        frame.token=reply.token;frame.stage="move"
    end
    if frame.stage=="move" then
        if not frame.waiting and (not quiet(context,true) or a.interactionOwner~="exploration") then
            frame.blocked="travel_guard_changed";frame.stage="release"
        else
            local to=frame.destination
            local reply,out=tasks.call(context,frame,"buddy-actions.travel_to",
                {token=frame.token,mapId=to.mapId,x=to.x,y=to.y,z=to.z,arrivalDistance=0.75},"traveling_verified_native_route")
            if out then return out end
            local p=reply.position or {}
            if not equal(reply.sessionId,context.state.body.sessionId) or not equal(reply.selfGuid,context.state.body.selfGuid)
               or reply.reason~="exploration_within_distance" or reply.motionUnconfirmed or reply.moving or reply.traveling
               or not geometry(p,to.mapId) or distance(p,to)>0.75 then
                frame.blocked="travel_arrival_not_confirmed"
            end
            frame.stage="release"
        end
    end
    if frame.stage=="release" then
        if frame.waiting or (a.interactionOwner or "")~="" then
            if (a.interactionOwner or "")~="exploration" and not frame.waiting then
                return tasks.await(context,"travel_lease_replaced")
            end
            local reply,out=tasks.call(context,frame,"buddy-actions.release_interaction",
                {owner="exploration",token=frame.token},"releasing_travel_custody")
            if out then return out end
            if not equal(reply.sessionId,context.state.body.sessionId) or not equal(reply.selfGuid,context.state.body.selfGuid)
               or (reply.interactionOwner or "")~="" or reply.motionUnconfirmed or reply.moving or reply.traveling then
                return tasks.await(context,"travel_cleanup_unconfirmed")
            end
        elseif a.motionUnconfirmed or a.moving or a.traveling then return tasks.await(context,"travel_cleanup_unconfirmed") end
        frame.token=nil;frame.legs=number(frame.legs)+1
        if frame.blocked then travel_blocked(context,frame.blocked);return end
        local p=position(context)
        if not p then return tasks.await(context,"travel_position_unknown") end
        if not geometry(p,frame.destination.mapId) or distance(p,frame.destination)>0.75 then
            travel_blocked(context,"travel_arrival_not_observed");return
        end
        frame.destination=nil;frame.planStart=nil;frame.stage="start"
        return tasks.await(context,"travel_leg_arrived")
    end
end

local function locate(context,frame)
    if frame.waiting and frame.waiting.method=="buddy-quests.discover" then
        local _,out=tasks.call(context,frame,"buddy-quests.discover",{},"discovering_actual_giver")
        if out then return nil,out end
        frame.discoveryAt=context.state.now
    end
    if frame.child then
        if frame.child.status=="blocked" then return nil,defer_child(context,frame,frame.child.reason) end
        frame.child=nil
    end
    local near=giver(context,frame.data.npcEntry)
    local p=position(context)
    if near and p and near.canInteract and near.distanceKnown and distance(p,near.creature)<=4
       and frame.discoveryAt and context.state.now-frame.discoveryAt<=10 then
        frame.guid=near.creature.guid;return near,nil
    end
    local to=near and near.creature or frame.data
    if near then to={mapId=to.mapId or 0,x=number(to.x),y=number(to.y),z=number(to.z)} end
    if type(to.x)~="number" or type(to.y)~="number" or type(to.z)~="number" then
        return nil,defer_child(context,frame,"giver_reference_unknown")
    end
    if not p or not equal(p.mapId or 0,to.mapId or 0) then return nil,defer_child(context,frame,"giver_map_or_position_unknown") end
    if distance(p,to)>3 then
        tasks.push(context,"travel",{mapId=to.mapId or 0,x=to.x,y=to.y,z=to.z});return nil,nil
    end
    if not frame.discoveryAt or context.state.now-frame.discoveryAt>=30 or frame.waiting then
        local _,out=tasks.call(context,frame,"buddy-quests.discover",{},"discovering_actual_giver")
        if out then return nil,out end
        frame.discoveryAt=context.state.now
    end
    return nil,defer_child(context,frame,"waiting_for_observed_actionable_giver")
end

handlers.hand_in=function(context,frame)
    local q,log=journal(context)
    if not q then return tasks.await(context,"own_quest_log_unknown") end
    local receipt=reward_proof(context,frame)
    if receipt then
        context.memory.completed=context.memory.completed or {}
        context.memory.completed[tostring(frame.data.questId)]=receipt
        tasks.pop(context,{status="reward_confirmed",taskKind=frame.kind,data=frame.data});return
    end
    if frame.stage=="receipt" then return tasks.await(context,"waiting_for_reward_and_own_log_removal") end
    if frame.stage=="claim" and frame.waiting then
        local _,out=tasks.call(context,frame,"buddy-quests.claim_reward",{},"claiming_exact_offered_reward")
        if out then return out end
        frame.stage="receipt";return tasks.await(context,"waiting_for_reward_and_own_log_removal")
    end
    if frame.stage=="query" and frame.waiting then
        local _,out=tasks.call(context,frame,"buddy-quests.turn_in",{},"requesting_actual_reward_offer")
        if out then return out end
        frame.stage="claim"
    end
    local own=log[tostring(frame.data.questId)]
    if not own or not own.complete or own.failed then return defer_child(context,frame,"complete_own_quest_required") end
    if not quiet(context,false) or q.actionInFlight then return tasks.await(context,"hand_in_waiting_for_safe_idle") end
    if frame.stage=="start" then
        local near,out=locate(context,frame)
        if out or not near then return out end
        frame.stage="query"
    end
    if frame.stage=="query" then
        local near=giver(context,frame.data.npcEntry)
        if not near or not near.canInteract or not equal(near.creature.guid,frame.guid) then
            return defer_child(context,frame,"hand_in_giver_binding_changed")
        end
        local _,out=tasks.call(context,frame,"buddy-quests.turn_in",{sessionId=context.state.body.sessionId,
            npcGuid=frame.guid,questId=frame.data.questId,questLogRevision=q.questLogRevision},"requesting_actual_reward_offer")
        if out then return out end
        frame.stage="claim"
    end
    if frame.stage=="claim" then
        local near=giver(context,frame.data.npcEntry)
        local offer=q.turnInOffer or {}
        if not near or not near.canInteract or not equal(near.creature.guid,frame.guid)
           or not equal(offer.npcGuid,frame.guid) or not equal(offer.questId,frame.data.questId)
           or offer.source~="reward_offer" or not offer.canComplete or offer.rewardRequested
           or number(offer.expiresInMs)<=0 or number(offer.revision)<=0 then
            if offer.rewardRequested or frame.binding then return tasks.await(context,"fresh_bound_reward_offer_required") end
            return defer_child(context,frame,"fresh_bound_reward_offer_required")
        end
        local choices=offer.choices or {}
        if #choices>1 then return defer_child(context,frame,"reward_choice_requires_selection") end
        local request={sessionId=context.state.body.sessionId,npcGuid=frame.guid,questId=frame.data.questId,
            offerRevision=offer.revision,noChoice=#choices==0,choiceIndex=0,expectedItemId=0}
        if #choices==1 then
            if number(choices[1].itemId)<=0 then return defer_child(context,frame,"reward_item_unknown") end
            request.choiceIndex=choices[1].index or 0;request.expectedItemId=choices[1].itemId
        end
        frame.binding=frame.binding or {offerRevision=offer.revision,baselineLogRevision=q.questLogRevision}
        local _,out=tasks.call(context,frame,"buddy-quests.claim_reward",request,"claiming_exact_offered_reward")
        if out then return out end
        frame.stage="receipt"
        return tasks.await(context,"waiting_for_reward_and_own_log_removal")
    end
end

handlers.followup=function(context,frame)
    local q,log=journal(context)
    if not q then return tasks.await(context,"own_quest_log_unknown") end
    local receipt=acceptance_proof(context,frame)
    if receipt then
        context.memory.accepted=context.memory.accepted or {};context.memory.accepted[tostring(frame.data.questId)]=receipt
        tasks.pop(context,{status="acceptance_confirmed",taskKind=frame.kind,data=frame.data});return
    end
    if log[tostring(frame.data.questId)] and frame.stage=="start" then
        tasks.pop(context,{status="already_in_own_log",taskKind=frame.kind,data=frame.data});return
    end
    if frame.stage=="receipt" then return tasks.await(context,"waiting_for_acceptance_in_own_log") end
    if frame.stage=="accept" and frame.waiting then
        local _,out=tasks.call(context,frame,"buddy-quests.accept",{},"accepting_actual_followup_offer")
        if out then return out end
        frame.stage="receipt";return tasks.await(context,"waiting_for_acceptance_in_own_log")
    end
    if not quiet(context,false) or q.actionInFlight then return tasks.await(context,"followup_waiting_for_safe_idle") end
    if frame.stage=="start" then
        local near,out=locate(context,frame)
        if out or not near then return out end
        frame.stage="offer"
    end
    if frame.stage=="offer" then
        local _,query=tasks.call(context,frame,"buddy-quests.offer",{sessionId=context.state.body.sessionId,npcGuid=frame.guid},
                                "querying_observed_followup_giver")
        if query then return query end
        frame.stage="accept"
    end
    local offer=q.offer or {}
    if not equal(offer.npcGuid,frame.guid) or number(offer.expiresInMs)<=0 or number(offer.revision)<=0 then
        return defer_child(context,frame,"fresh_followup_offer_required")
    end
    local available=false
    for _,row in ipairs(offer.quests or {}) do
        if equal(row.questId,frame.data.questId) and row.available and not row.acceptRequested then available=true end
    end
    if not available then tasks.pop(context,{status="followup_not_offered",taskKind=frame.kind,data=frame.data});return end
    frame.binding=frame.binding or {offerRevision=offer.revision,baselineLogRevision=q.questLogRevision}
    local _,out=tasks.call(context,frame,"buddy-quests.accept",{sessionId=context.state.body.sessionId,npcGuid=frame.guid,
        questId=frame.data.questId,offerRevision=offer.revision},"accepting_actual_followup_offer")
    if out then return out end
    frame.stage="receipt";return tasks.await(context,"waiting_for_acceptance_in_own_log")
end

handlers.journal_sync=function(context,frame)
    local q,log=journal(context)
    if not q then return tasks.await(context,"own_quest_log_unknown") end
    if opportunity(context,frame) then return end
    local params=context.state.goal.params
    local cooldown=params.blockedChildCooldownSeconds or 60
    assert(type(cooldown)=="number" and cooldown>=5 and cooldown<=300,"bounded blocked-child cooldown required")
    if frame.child then
        local child=frame.child;frame.child=nil
        if child.status=="blocked" and child.data and number(child.data.questId)>0 then
            context.memory.deferredQuests=context.memory.deferredQuests or {}
            local key=child.taskKind..":"..tostring(child.data.questId)
            local prior=context.memory.deferredQuests[key] or {}
            context.memory.deferredQuests[key]={taskKind=child.taskKind,data=child.data,reason=child.reason,
                retryAfter=number(context.state.now)+cooldown,attempts=number(prior.attempts)+1}
        elseif child.taskKind and child.data and context.memory.deferredQuests then
            context.memory.deferredQuests[child.taskKind..":"..tostring(child.data.questId)]=nil
        end
        if child.status=="reward_confirmed" and number(child.data.followupQuestId)>0
           and contains(params.targetQuestIds,child.data.followupQuestId) then
            local row=child.data
            tasks.push(context,"followup",{questId=row.followupQuestId,npcEntry=row.npcEntry,mapId=row.mapId,
                x=row.x,y=row.y,z=row.z});return
        end
    end
    local cooling=false
    for _,priority in ipairs({true,false}) do
        for _,row in ipairs(params.handIns or {}) do
            local own=log[tostring(row.questId)]
            if own and own.complete and not own.failed then
                if deferred(context,"hand_in",row.questId) then cooling=true
                elseif contains(params.initialHandInQuestIds,row.questId)==priority then
                    tasks.push(context,"hand_in",row);return
                end
            end
        end
    end
    -- A blocked pre-accept followup stays eligible after its cooldown. An
    -- acceptance already dispatched never reaches this queue without proof.
    for _,row in pairs(context.memory.deferredQuests or {}) do
        if row.taskKind=="followup" and contains(params.targetQuestIds,row.data.questId) and not log[tostring(row.data.questId)] then
            if deferred(context,"followup",row.data.questId) then cooling=true
            else tasks.push(context,"followup",row.data);return end
        end
    end
    context.memory.remaining={}
    for _,id in ipairs(params.targetQuestIds or {}) do
        local own=log[tostring(id)]
        context.memory.remaining[#context.memory.remaining+1]={questId=id,inLog=own~=nil,complete=own and own.complete==true or false}
    end
    if cooling then return tasks.await(context,"waiting_for_deferred_quests") end
    if not (params.rendezvous or {}).known then return tasks.await(context,"remaining_objectives_rendezvous_unknown") end
    return tasks.await(context,"remaining_objectives_require_behavior")
end

local policy={handlers=handlers}
function policy.attach(child)
    assert(type(child)=="table" and type(child.handlers)=="table","child module handlers required")
    for kind,handler in pairs(child.handlers) do
        assert(type(kind)=="string" and type(handler)=="function" and not handlers[kind],"distinct child handler required")
        handlers[kind]=handler;extensions[kind]=true
    end
    return policy
end
local function decide(state)
    if #(state.pending or {})>0 then
        local memory=state.memory or {}
        local frame=(memory.stack or {})[#(memory.stack or {})]
        local context={state=state,memory=memory,observations=state.observations or {}}
        local reserved=false
        for _,row in ipairs(state.pending or {}) do
            if frame and frame.waiting and row.id==frame.waiting.id and row.method==frame.waiting.method then reserved=true end
        end
        if reserved then
            local receipt=frame.kind=="hand_in" and frame.waiting.method=="buddy-quests.claim_reward" and reward_proof(context,frame)
            if receipt then
                frame.stage="receipt"
                return tasks.resolve(context,frame,"quests","lastReward.confirmedLogRevision",receipt.confirmedLogRevision,"reward_and_log_removal_observed")
            end
            receipt=frame.kind=="followup" and frame.waiting.method=="buddy-quests.accept" and acceptance_proof(context,frame)
            if receipt then
                frame.stage="receipt"
                return tasks.resolve(context,frame,"quests","lastAcceptance.confirmedLogRevision",receipt.confirmedLogRevision,"acceptance_in_own_log_observed")
            end
        end
        return {memory=memory,phase="blocked_on_ambiguous_call",done=false}
    end
    return tasks.run(state,handlers,state.goal.kind)
end

-- Metadata only: reservations, retries and cooldown clocks are not progress.
-- A persistent fault keeps one phase/key while its freshness is renewed, so
-- the existing brain's durable dedup governs repair rather than every tick.
local function progress(state,out)
    local memory,now=out.memory,number(state.now)
    local p=memory.progress or {since=now,seen={},count=0}
    memory.progress=p
    local advanced=false
    for _,frame in ipairs(memory.stack or {}) do
        local key=tostring(frame.kind)..":"..tostring((frame.data or {}).questId or "")..":"..tostring(frame.stage)
        if not p.seen[key] and number(p.count)<64 then
            p.seen[key]=true;p.count=number(p.count)+1;advanced=true
        end
    end
    local context={state=state,memory=memory,observations=state.observations or {}}
    local q=journal(context)
    if q and number(q.questLogRevision)>number(p.questRevision) then
        p.questRevision=q.questLogRevision;advanced=true
    end
    local pose=position(context)
    if pose and not pose.estimated then
        if not p.pose or not equal(p.pose.mapId or 0,pose.mapId or 0) or distance(p.pose,pose)>=1 then
            p.pose={mapId=pose.mapId or 0,x=number(pose.x),y=number(pose.y),z=number(pose.z)};advanced=true
        end
    end
    local last=state.last or {};local reply=last.response or {}
    if last.id~=p.arrival and last.method=="buddy-actions.travel_to" and not last.error
       and equal(reply.sessionId,state.body.sessionId) and equal(reply.selfGuid,state.body.selfGuid)
       and reply.reason=="exploration_within_distance" and not reply.motionUnconfirmed and not reply.moving
       and not reply.traveling and geometry(reply.position,(reply.position or {}).mapId) then
        p.arrival=last.id;advanced=true
    end
    if advanced or now<number(p.since) then p.since=now end
    local reason,key=out.phase,nil
    for id,row in pairs(memory.deferredQuests or {}) do
        if type(row.reason)=="string" and (not key or id<key) then key=id;reason=row.reason end
    end
    p.blockedReason=type(reason)=="string" and #reason>0 and #reason<=128 and reason or "mission_waiting"
    if not out.done and now-number(p.since)>=30 then
        memory.health={code="stalled",phase=p.blockedReason,since=now,noProgressSince=p.since}
    else memory.health=nil end
    return out
end
policy.decide=function(state) return progress(state,decide(state)) end
return policy
