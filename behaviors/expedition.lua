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
-- Selection is a preference over the current offer, never an equip/upgrade proof.
-- A composition may supply read-only normal-query or public-template metadata.
local rewardConfig
local function reward_choice(context,choices)
    if #choices==0 then return nil end
    if #choices==1 then
        if number(choices[1].itemId)<=0 then return nil,"reward_item_unknown" end
        return choices[1],"only_offered_choice"
    end
    local config=context.state.goal.params.rewardSelection or rewardConfig
    if type(config)~="table" or config.mode~="vendor_value" or type(config.items)~="table" then
        return nil,"reward_choice_requires_selection"
    end
    if #choices>6 then return nil,"reward_choices_invalid" end
    local best,bestValue,indices,ids=nil,-1,{},{}
    for _,choice in ipairs(choices) do
        local id,index,count=number(choice.itemId),number(choice.index),number(choice.count)
        local item=config.items[tostring(id)]
        if id<=0 or id%1~=0 or index<0 or index>=6 or index%1~=0 or count<1 or count>1000 or count%1~=0
           or indices[tostring(index)] or ids[tostring(id)] then return nil,"reward_choices_invalid" end
        indices[tostring(index)],ids[tostring(id)]=true,true
        if type(item)~="table" or not equal(item.itemId,id) or item.available~=true or item.sellPriceKnown~=true
           or type(item.sellPrice)~="number" or item.sellPrice<0 or item.sellPrice>1000000000
           or item.sellPrice%1~=0 then return nil,"reward_choice_metadata_unknown" end
        local value=item.sellPrice*count
        if value>bestValue or (value==bestValue and index<number(best.index)) then best,bestValue=choice,value end
    end
    return best,"highest_verified_vendor_value",bestValue
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
-- Static anchors are candidate query destinations, never observed presence
-- or permission to move without the native planner's verified endpoint.
local function travel_anchor(context,frame,p)
    local anchors=context.state.goal.params.travelAnchors or {}
    if type(anchors)~="table" or #anchors>16 then return nil end
    local best,index,remaining=nil,nil,distance(p,frame.data)
    for i,anchor in ipairs(anchors) do
        local valid=type(anchor)=="table" and type(anchor.mapId)=="number"
            and anchor.mapId>=0 and anchor.mapId%1==0 and equal(anchor.mapId,p.mapId or 0)
        for _,axis in ipairs({"x","y","z"}) do
            local v=type(anchor)=="table" and anchor[axis] or nil
            if type(v)~="number" or v~=v or math.abs(v)>40000 then valid=false end
        end
        if valid and not (frame.anchorTried or {})[tostring(i)] then
            local left=distance(anchor,frame.data)
            if distance(p,anchor)<=1000 and remaining-left>=100 and (not best or left<distance(best,frame.data)) then
                best,index=anchor,i
            end
        end
    end
    return best,index
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
            if number(frame.legs)>=32 then travel_blocked(context,"travel_leg_budget");return end
            frame.planGoal=frame.data;frame.planAnchor=nil
            if distance(p,frame.data)>=1200 then
                frame.planGoal,frame.planAnchor=travel_anchor(context,frame,p)
                if not frame.planGoal then travel_blocked(context,"travel_goal_outside_native_plan_bound");return end
            end
            frame.planStart={known=true,mapKnown=true,mapId=p.mapId or 0,x=number(p.x),y=number(p.y),z=number(p.z),revision=p.revision}
        end
        local start=frame.planStart;local goal=frame.planGoal or frame.data
        local reply,out=tasks.call(context,frame,"buddy-actions.plan_route_leg",{sessionId=context.state.body.sessionId,
            selfGuid=context.state.body.selfGuid,expectedPositionRevision=start.revision,mapId=frame.data.mapId or 0,
            x=goal.x,y=goal.y,z=goal.z},"planning_verified_native_route_leg")
        if out then return out end
        if reply.readError then
            context.memory.lastRoutePlanError={error=reply.readError,goal=goal,at=context.state.now}
            if frame.planAnchor then
                frame.anchorTried=frame.anchorTried or {};frame.anchorTried[tostring(frame.planAnchor)]=true
                frame.planGoal=nil;frame.planAnchor=nil
                return tasks.await(context,"travel_anchor_candidate_unavailable")
            end
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
            frame.partialReplyKnown=equal(reply.sessionId,context.state.body.sessionId)
                and equal(reply.selfGuid,context.state.body.selfGuid) and not reply.motionUnconfirmed
                and geometry(p,to.mapId) and number(p.revision)>0
            -- A definite native safety stop is not a failed destination. Keep
            -- its correlated reason, release custody, then await the ordinary
            -- safety gate before planning a fresh leg. This proves no arrival.
            frame.safetyInterrupted=frame.partialReplyKnown and not reply.moving and not reply.traveling
                and reply.reason=="exploration_character_unsafe_or_in_combat"
            if not equal(reply.sessionId,context.state.body.sessionId) or not equal(reply.selfGuid,context.state.body.selfGuid)
               or reply.reason~="exploration_within_distance" or reply.motionUnconfirmed or reply.moving or reply.traveling
               or not geometry(p,to.mapId) or distance(p,to)>0.75 then
                frame.blocked="travel_arrival_not_confirmed"
                if frame.partialReplyKnown then
                    context.memory.lastTravelStop={requestId=context.state.last.id,reason=reply.reason,
                        navReason=reply.navReason,position=p,destination=to,at=context.state.now}
                end
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
        local p=position(context)
        if frame.blocked=="travel_arrival_not_confirmed" and frame.safetyInterrupted and frame.legs<32
           and equal(a.sessionId,context.state.body.sessionId) and equal(a.selfGuid,context.state.body.selfGuid)
           and number(a.interactionLeaseRemainingMs)==0 and geometry(p,frame.destination.mapId)
           and number(p.revision)>0 then
            frame.blocked=nil;frame.partialReplyKnown=nil;frame.safetyInterrupted=nil
            frame.destination=nil;frame.planStart=nil;frame.planGoal=nil;frame.planAnchor=nil;frame.anchorTried=nil
            frame.stage="start"
            return tasks.await(context,"travel_waiting_for_safe_idle")
        end
        if frame.blocked=="travel_arrival_not_confirmed" and frame.partialReplyKnown
           and frame.legs<32 and quiet(context,false) and number(a.interactionLeaseRemainingMs)==0
           and geometry(p,frame.destination.mapId) and number(p.revision)>0
           and geometry(frame.planStart,frame.destination.mapId)
           and distance(p,frame.planStart)>=3
           and distance(frame.planStart,frame.destination)-distance(p,frame.destination)>=3 then
            context.memory.verifiedPartialLegs=number(context.memory.verifiedPartialLegs)+1
            frame.blocked=nil;frame.partialReplyKnown=nil;frame.destination=nil;frame.planStart=nil
            frame.planGoal=nil;frame.planAnchor=nil;frame.anchorTried=nil;frame.stage="start"
            return tasks.await(context,"partial_progress_replan")
        end
        if frame.blocked then travel_blocked(context,frame.blocked);return end
        if not p then return tasks.await(context,"travel_position_unknown") end
        if not geometry(p,frame.destination.mapId) or distance(p,frame.destination)>0.75 then
            travel_blocked(context,"travel_arrival_not_observed");return
        end
        frame.partialReplyKnown=nil;frame.destination=nil;frame.planStart=nil
        frame.planGoal=nil;frame.planAnchor=nil;frame.anchorTried=nil;frame.stage="start"
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
        local selected,selectionReason,selectionValue=reward_choice(context,choices)
        if #choices>0 and not selected then return defer_child(context,frame,selectionReason) end
        local request={sessionId=context.state.body.sessionId,npcGuid=frame.guid,questId=frame.data.questId,
            offerRevision=offer.revision,noChoice=#choices==0,choiceIndex=0,expectedItemId=0}
        if selected then
            request.choiceIndex=selected.index or 0;request.expectedItemId=selected.itemId
            frame.rewardSelection={itemId=selected.itemId,choiceIndex=request.choiceIndex,reason=selectionReason,
                vendorValueCopper=selectionValue,offerRevision=offer.revision}
            context.memory.rewardSelections=context.memory.rewardSelections or {}
            context.memory.rewardSelections[tostring(frame.data.questId)]=frame.rewardSelection
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
    if log[tostring(frame.data.questId)] and not frame.binding then
        tasks.pop(context,{status="already_in_own_log",taskKind=frame.kind,data=frame.data});return
    end
    local near=giver(context,frame.data.npcEntry)
    if not near or not near.canInteract or not near.distanceKnown or not equal(near.creature.guid,frame.guid)
       or not position(context) or distance(position(context),near.creature)>4 then
        return defer_child(context,frame,"acceptance_giver_binding_changed")
    end
    local offer=q.offer or {}
    if not equal(offer.npcGuid,frame.guid) or number(offer.expiresInMs)<=0 or number(offer.revision)<=0 then
        return defer_child(context,frame,"fresh_followup_offer_required")
    end
    local available=false
    for _,row in ipairs(offer.quests or {}) do
        if equal(row.questId,frame.data.questId) and row.available and not row.acceptRequested then available=true end
    end
    if not available then
        if frame.kind=="pickup" then return defer_child(context,frame,"pickup_not_offered") end
        tasks.pop(context,{status="followup_not_offered",taskKind=frame.kind,data=frame.data});return
    end
    frame.binding=frame.binding or {offerRevision=offer.revision,baselineLogRevision=q.questLogRevision}
    local _,out=tasks.call(context,frame,"buddy-quests.accept",{sessionId=context.state.body.sessionId,npcGuid=frame.guid,
        questId=frame.data.questId,offerRevision=offer.revision},"accepting_actual_followup_offer")
    if out then return out end
    frame.stage="receipt";return tasks.await(context,"waiting_for_acceptance_in_own_log")
end

handlers.pickup=handlers.followup

local function rendezvous(context,frame,target,cooling)
    local memory=context.memory
    if not target or not target.known then
        return tasks.await(context,cooling and "waiting_for_deferred_quests" or "remaining_objectives_rendezvous_unknown")
    end
    local valid=type(target.mapId or 0)=="number" and number(target.mapId)>=0 and number(target.mapId)%1==0
    for _,axis in ipairs({"x","y","z"}) do
        if type(target[axis])~="number" or target[axis]~=target[axis] or math.abs(target[axis])>40000 then valid=false end
    end
    if not valid then return tasks.await(context,"rendezvous_reference_invalid") end
    local p=position(context)
    if not quiet(context,false) then return tasks.await(context,"rendezvous_waiting_for_safe_idle") end
    if not equal(p.mapId or 0,target.mapId or 0) then return tasks.await(context,"rendezvous_map_change_required") end
    if distance(p,target)<=3 then
        memory.rendezvous={status="arrived",sessionId=context.state.body.sessionId,selfGuid=context.state.body.selfGuid,
            mapId=p.mapId or 0,x=number(p.x),y=number(p.y),z=number(p.z),positionRevision=p.revision,
            estimated=p.estimated or false,observedAt=context.state.now}
        if cooling then return tasks.await(context,"waiting_for_deferred_quests") end
        return tasks.await(context,memory.journalSync.targetIdsPresent and "journal_ids_present_rendezvous_arrived"
            or "rendezvous_arrived_missing_target_ids")
    end
    local previous=memory.rendezvous or {}
    if previous.status=="blocked" and number(context.state.now)<number(previous.retryAfter) then
        return tasks.await(context,"rendezvous_blocked")
    end
    frame.rendezvousPending=true
    memory.rendezvous={status="traveling",target={mapId=target.mapId or 0,x=target.x,y=target.y,z=target.z}}
    tasks.push(context,"travel",{mapId=target.mapId or 0,x=target.x,y=target.y,z=target.z})
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
        if frame.rendezvousPending then
            frame.rendezvousPending=nil
            context.memory.rendezvous={status=child.status=="blocked" and "blocked" or "arrival_unconfirmed",
                reason=child.reason,retryAfter=number(context.state.now)+cooldown}
        end
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
    local pickups=params.pickups or {}
    assert(type(pickups)=="table" and #pickups<=64,"bounded configured pickups required")
    for _,row in ipairs(pickups) do
        assert(type(row)=="table" and type(row.questId)=="number" and row.questId>0 and row.questId%1==0
            and type(row.npcEntry)=="number" and row.npcEntry>0 and row.npcEntry%1==0,"configured quest/giver required")
        if contains(params.targetQuestIds,row.questId) and not log[tostring(row.questId)] then
            if deferred(context,"pickup",row.questId) or deferred(context,"followup",row.questId) then cooling=true
            else tasks.push(context,"pickup",row);return end
        end
    end
    context.memory.remaining={}
    local sync={targetIdsPresent=true,missingQuestIds={},unfinishedQuestIds={},completeQuestIds={},questLogRevision=q.questLogRevision}
    for _,id in ipairs(params.targetQuestIds or {}) do
        local own=log[tostring(id)]
        context.memory.remaining[#context.memory.remaining+1]={questId=id,inLog=own~=nil,complete=own and own.complete==true or false}
        if not own then sync.targetIdsPresent=false;sync.missingQuestIds[#sync.missingQuestIds+1]=id
        elseif own.complete and not own.failed then sync.completeQuestIds[#sync.completeQuestIds+1]=id
        else sync.unfinishedQuestIds[#sync.unfinishedQuestIds+1]=id end
    end
    context.memory.journalSync=sync
    return rendezvous(context,frame,params.rendezvous,cooling)
end

local policy={handlers=handlers}
function policy.configureRewards(config) rewardConfig=config;return policy end
function policy.attach(child)
    assert(type(child)=="table" and type(child.handlers)=="table","child module handlers required")
    for kind,handler in pairs(child.handlers) do
        assert(type(kind)=="string" and type(handler)=="function" and not handlers[kind],"distinct child handler required")
        handlers[kind]=handler;extensions[kind]=true
    end
    return policy
end
-- An absent current lease can retire an uncertain release reservation, but
-- cannot establish a transport ACK, arrival, or any item/quest mutation.
local function released_custody_revision(context,frame,row)
    if #(context.state.pending or {})~=1 or frame.kind~="travel" or frame.stage~="release"
       or frame.waiting.method~="buddy-actions.release_interaction" or row.effect~="mutation"
       or row.outcome~="pending" or type(row.error)~="string" or row.error==""
       or type(frame.token)~="string" or frame.token=="" then return nil end
    local params=row.params or {}
    if params.owner~="exploration" or params.token~=frame.token then return nil end
    local a=context.observations.actions or {};local p=a.position or {}
    if not quiet(context,false) or (a.interactionOwner or "")~=""
       or number(a.interactionLeaseRemainingMs)~=0 or a.moving or a.traveling or a.motionUnconfirmed
       or not geometry(p,(frame.destination or frame.data or {}).mapId)
       or number(p.revision)<=0 then return nil end
    return p.revision
end

-- Losing the old token prevents a delayed native TravelTo from writing.
-- This abandons only the obsolete reservation, without accepting its route.
local function abandoned_navigation_revision(context,frame,row)
    if #(context.state.pending or {})~=1 or frame.kind~="travel" or frame.stage~="move"
       or frame.waiting.method~="buddy-actions.travel_to" or row.effect~="mutation"
       or row.outcome~="pending" or type(row.error)~="string" or row.error==""
       or type(frame.token)~="string" or frame.token=="" or type(frame.destination)~="table" then return nil end
    local params=row.params or {};local to=frame.destination
    if params.token~=frame.token or not equal(params.mapId or 0,to.mapId or 0) then return nil end
    for _,axis in ipairs({"x","y","z"}) do
        if params[axis]~=nil and type(params[axis])~="number" then return nil end
        if number(params[axis])~=number(to[axis]) then return nil end
    end
    local a=context.observations.actions or {};local p=a.position or {}
    if type(a.reason)~="string" or a.reason=="" or #a.reason>128
       or not quiet(context,false) or (a.interactionOwner or "")~=""
       or number(a.interactionLeaseRemainingMs)~=0 or a.moving or a.traveling or a.motionUnconfirmed
       or not geometry(p,to.mapId) or number(p.revision)<=0 then return nil end
    return p.revision
end

local function decide(state)
    if #(state.pending or {})>0 then
        local memory=state.memory or {}
        local frame=(memory.stack or {})[#(memory.stack or {})]
        local context={state=state,memory=memory,observations=state.observations or {}}
        local reserved,row=false,nil
        for _,candidate in ipairs(state.pending or {}) do
            if frame and frame.waiting and candidate.id==frame.waiting.id and candidate.method==frame.waiting.method then reserved=true;row=candidate end
        end
        if reserved then
            local abandoned=abandoned_navigation_revision(context,frame,row)
            if abandoned then
                frame.stage="release";frame.blocked="unknown_travel_abandoned";frame.partialReplyKnown=nil
                return tasks.resolve(context,frame,"actions","position.revision",abandoned,"navigation_abandoned_custody_absent_no_arrival")
            end
            local revision=released_custody_revision(context,frame,row)
            if revision then
                return tasks.resolve(context,frame,"actions","position.revision",revision,"custody_absent_observed_no_transport_ack")
            end
            local receipt=frame.kind=="hand_in" and frame.waiting.method=="buddy-quests.claim_reward" and reward_proof(context,frame)
            if receipt then
                frame.stage="receipt"
                return tasks.resolve(context,frame,"quests","lastReward.confirmedLogRevision",receipt.confirmedLogRevision,"reward_and_log_removal_observed")
            end
            receipt=(frame.kind=="followup" or frame.kind=="pickup") and frame.waiting.method=="buddy-quests.accept" and acceptance_proof(context,frame)
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
    -- Count only forward movement verified by the travel handler after cleanup.
    -- Replanning and dispatch alone never renew the watchdog.
    if number(memory.verifiedPartialLegs)>number(p.verifiedPartialLegs) then
        p.verifiedPartialLegs=memory.verifiedPartialLegs;advanced=true
    end
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
    if out.phase=="quest_child_deferred" then
        -- The just-popped child is still on its parent for one tick.
        -- Report its actual blocker, never an older sibling's cooldown.
        local parent=(memory.stack or {})[#(memory.stack or {})]
        local child=(parent or {}).child or {}
        if child.status=="blocked" then reason=child.reason end
    end
    if out.phase=="waiting_for_deferred_quests" then
        for id,row in pairs(memory.deferredQuests or {}) do
            if type(row.reason)=="string" and (not key or id<key) then key=id;reason=row.reason end
        end
    end
    p.blockedReason=type(reason)=="string" and #reason>0 and #reason<=128 and reason or "mission_waiting"
    -- A newly dispatched native travel RPC has its own bounded wait. Do not
    -- count its reservation as progress or inherit a sibling's expired clock.
    -- Planning and custody dispatches are fresh host operations, not stalled waits.
    -- Suppress only this dispatch tick; retain the original progress clock.
    local freshDispatch=out.action and (out.action.method=="buddy-actions.plan_route_leg"
        or out.action.method=="buddy-actions.acquire_interaction"
        or out.action.method=="buddy-actions.release_interaction")
    local travelWaiting=out.action and out.action.method=="buddy-actions.travel_to"
    for _,pending in ipairs(state.pending or {}) do
        if pending.method=="buddy-actions.travel_to" and pending.outcome=="pending"
           and number(pending.reservedAt)>0 and now>=number(pending.reservedAt)
           and now-number(pending.reservedAt)<40 then travelWaiting=true end
    end
    -- A host-validated resolution is a local transition, not a stalled wait.
    -- It does not renew the progress clock or establish a gameplay outcome.
    if not out.done and not out.resolve and not freshDispatch and not travelWaiting and now-number(p.since)>=30 then
        memory.health={code="stalled",phase=p.blockedReason,since=now,noProgressSince=p.since}
    else memory.health=nil end
    return out
end
policy.decide=function(state) return progress(state,decide(state)) end
return policy
