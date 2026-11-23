-- Editable instructed-flight/exploration caller. The host retains ONE durable
-- task memory/pending ledger; this parent owns custody, never the child.
-- A lost lease RPC remains unknown even after its finite lease could expire.
local M={tuning={lease_ms=15000,renew_below_ms=5000}}
local acquire="buddy-actions.acquire_idle_interaction"
local release="buddy-actions.release_interaction"
local function uint(n) return type(n)=="number" and n==n and n>=0 and n<=4294967295 and n==math.floor(n) end
local function remaining(actions)
    local v=actions.interactionLeaseRemainingMs
    if v==nil then return 0 end
    if type(v)~="string" or not planner.identifier(v,true) or #v>5 then return end
    return tonumber(v)
end
local function copy(t) local out={};for k,v in pairs(t or {}) do out[k]=v end;return out end
local function top(memory) local s=memory.stack or {};return s[#s] end
local function await(context,p,phase) p.phase=phase;return tasks.await(context,phase) end
local function bound(state)
    local b=state.body or {};local o=state.observations or {}
    local w,a,f=o.world or {},o.actions or {},o.flightpath or {}
    if type(b.sessionId)~="string" or b.sessionId=="" or not planner.identifier(b.selfGuid) or
       a.sessionId~=b.sessionId or a.selfGuid~=b.selfGuid or (w.self or {}).guid~=b.selfGuid or
       f.sessionId~=b.sessionId or f.selfGuid~=b.selfGuid or f.visitBound~=true or
       not planner.identifier(w.visitRevision) or f.visitId~=w.visitRevision then return end
    return b,w,a,f
end
local function retired(w)
    local c=w.splineCompletion or {};local op=c.operation or {}
    return c.retirementCurrent==true and c.selfGuid==(w.self or {}).guid and c.visitRevision==w.visitRevision and
        op.retired==true and op.pending~=true and op.visitRevision==w.visitRevision
end
local function quiet(a,w)
    for _,key in ipairs({"moving","castPending","motionUnconfirmed","autoAttacking","autoAttackRequested",
                        "autoAttackUnconfirmed","practicing","traveling"}) do if a[key]==true then return false end end
    local s=w.self or {};local flags,movement=s.unitFlags or 0,s.movementFlags or 0
    return (a.recovery or {}).active~=true and s.present==true and s.aliveKnown==true and s.alive==true and
        s.healthKnown==true and uint(s.health) and uint(s.maxHealth) and s.health>0 and s.health<=s.maxHealth and
        s.positionKnown==true and s.positionInferred~=true and s.mapKnown==true and planner.identifier(s.positionRevision) and
        s.unitFlagsKnown==true and uint(flags) and math.floor(flags/0x80000)%2==0 and math.floor(flags/0x100000)%2==0 and
        s.movementFlagsKnown==true and uint(movement) and movement%256==0 and s.channelSpellKnown==true and
        (s.channelSpellId or 0)==0 and s.channelObjectKnown==true and (s.channelObjectGuid==nil or s.channelObjectGuid=="0")
end
local function child_state(state,p)
    local out=copy(state);out.goal=copy(state.goal);out.goal.params=copy(state.goal.params)
    out.goal.params.explorationToken=p.lease and p.lease.token or ""
    -- An intervening custody RPC must not overwrite a child's known reply.
    out.last=p.childReply or state.last
    return out
end
local function consume(context,p)
    local reply,out=tasks.call(context,p,p.method,p.request,"flight_parent_"..p.operation)
    if out then return out end
    local state=p.method==acquire and reply.state or reply
    if type(state)~="table" or state.sessionId~=p.sessionId or state.selfGuid~=p.selfGuid then
        p.unverified=true;return await(context,p,"flight_parent_custody_reply_unverified")
    end
    if p.method==acquire then
        if type(reply.token)~="string" or reply.token=="" or #reply.token>128 or state.interactionOwner~="exploration" or
           not remaining(state) or remaining(state)<=0 or p.request.token and reply.token~=p.request.token then
            p.unverified=true;return await(context,p,"flight_parent_custody_reply_unverified")
        end
        p.lease={token=reply.token,visitId=p.request.visitId,requestId=p.callId}
        p.visitId=p.request.visitId
    elseif (state.interactionOwner or "")~="" then
        p.unverified=true;return await(context,p,"flight_parent_release_reply_unverified")
    else
        p.lastLease=p.lease;p.lease=nil
    end
    p.method,p.request,p.operation,p.callId=nil,nil,nil,nil
end
local function call(context,p,method,request,operation)
    p.method,p.request,p.operation=method,request,operation
    local _,out=tasks.call(context,p,method,request,"flight_parent_"..operation)
    p.callId=p.waiting and p.waiting.id
    return out
end
local function custody(context,p,b,w,a)
    local owner=a.interactionOwner or "";local ms=remaining(a)
    if not ms then return await(context,p,"flight_parent_lease_observation_unavailable") end
    if p.lease and owner=="" then p.lease.lapsedObserved=true end
    if owner~="" and (owner~="exploration" or not p.lease or p.lease.lapsedObserved) then
        return await(context,p,"flight_parent_foreign_custody")
    end
    if not quiet(a,w) then return await(context,p,"flight_parent_waiting_for_idle_body") end
    local request={sessionId=b.sessionId,selfGuid=b.selfGuid,visitId=w.visitRevision,owner="exploration"}
    if owner=="exploration" and p.lease.visitId~=w.visitRevision then
        request.token=p.lease.token
        return call(context,p,release,request,"release_old_visit")
    end
    request.leaseMs=M.tuning.lease_ms
    if owner=="exploration" then request.token=p.lease.token end
    if owner=="" or ms<=M.tuning.renew_below_ms then
        return call(context,p,acquire,request,owner=="" and "acquire" or "renew")
    end
end
function M.decide(state)
    local memory=state.memory or {};memory.serial=memory.serial or 0;state.memory=memory
    local context={state=state,memory=memory,observations=state.observations or {}}
    local p=memory.flightpathParent
    local b,w,a,f=bound(state)
    if not b then return tasks.await(context,"flight_parent_binding_unavailable") end
    local goal=state.goal or {};local params=goal.params or {}
    if not p then
        if memory.stack and #memory.stack>0 or #(state.pending or {})>0 then
            return tasks.await(context,"flight_parent_existing_task_requires_its_owner")
        end
        if (f.activation or {}).pending==true then
            return tasks.await(context,"flight_parent_existing_booking_requires_its_owner")
        end
        if type(goal.id)~="string" or goal.id=="" or not uint(params.destinationNode) or params.destinationNode==0 or
           type(params.acceptUnknownFare)~="boolean" then return tasks.await(context,"flight_parent_explicit_goal_required") end
        p={sessionId=b.sessionId,selfGuid=b.selfGuid,visitId=w.visitRevision,goalId=goal.id,destination=params.destinationNode,
           acceptUnknownFare=params.acceptUnknownFare,stage="prepare"};memory.flightpathParent=p
    end
    if p.sessionId~=b.sessionId or p.selfGuid~=b.selfGuid or p.goalId~=goal.id or
       p.destination~=params.destinationNode or p.acceptUnknownFare~=params.acceptUnknownFare then
        return await(context,p,"flight_parent_binding_changed")
    end
    if p.unverified then return await(context,p,"flight_parent_custody_reply_unverified") end
    if not uint(M.tuning.lease_ms) or M.tuning.lease_ms<1000 or M.tuning.lease_ms>30000 or
       not uint(M.tuning.renew_below_ms) or M.tuning.renew_below_ms<1000 or M.tuning.renew_below_ms>=M.tuning.lease_ms then
        return await(context,p,"flight_parent_lease_tuning_invalid")
    end
    local pending=state.pending or {}
    if #pending>0 then
        if p.waiting then return await(context,p,"flight_parent_unknown_custody_no_replay") end
        local child=p.stage=="discover" and discovery.decide_with_parent or flightpath.decide
        return child(child_state(state,p))
    end
    if p.waiting then
        local out=consume(context,p)
        if out then return out end
    end
    local frame=top(memory);local last=state.last
    if frame and frame.waiting then
        if last and last.id==frame.waiting.id and last.method==frame.waiting.method and not last.error then
            p.childReply=last
        elseif not p.childReply then
            local child=p.stage=="discover" and discovery.decide_with_parent or flightpath.decide
            return child(child_state(state,p))
        end
    end
    local booking=f.activation or {}
    local paid=booking.pending==true
    if p.visitId~=w.visitRevision and not paid then
        return await(context,p,"flight_parent_unbooked_visit_replaced")
    end
    local activation=frame
    if frame and frame.kind=="flightpath_plan" then activation=(memory.stack or {})[#memory.stack-1] end
    if p.bookingIssued and activation and activation.kind=="flightpath_activation" and not activation.request and not paid and
       booking.revision==p.lastBookingRevision and booking.phase=="observed_arrival" and booking.arrivalKnown==true and
       booking.activationAccepted==true and booking.replyKnown==true and (booking.replyCode or 0)==0 then
        -- Only the child's new, unissued hop after actual settlement may restart
        -- pure planning when a known lease changes. Prior unknowns stay retained.
        p.bookingIssued=false
    end
    if p.stage=="cleanup" then
        if paid then return await(context,p,"flight_parent_booking_still_pending") end
        if p.lease and (a.interactionOwner or "")=="exploration" and not p.lease.lapsedObserved then
            if not quiet(a,w) then return await(context,p,"flight_parent_cleanup_waiting_for_idle") end
            return call(context,p,release,{sessionId=b.sessionId,selfGuid=b.selfGuid,visitId=w.visitRevision,
                owner="exploration",token=p.lease.token},"release")
        end
        if (a.interactionOwner or "")~="" then return await(context,p,"flight_parent_foreign_custody") end
        p.phase="complete";return {memory=memory,phase="flight_parent_complete",done=true}
    end
    -- Never renew or carry a lease through flight. A new-visit lease is obtained
    -- only after the current source has actual retired-ground evidence; native
    -- admission independently rechecks the complete accepted-booking proof.
    if not paid or retired(w) then
        local out=custody(context,p,b,w,a)
        if out then return out end
    end
    local childState=child_state(state,p)
    if p.stage=="prepare" then
        local menu=planner.menu_binding(childState)
        if menu and (params.npcGuid==nil or params.npcGuid==menu.npcGuid) then p.stage="flight"
        else p.stage="discover" end
    end
    local out=(p.stage=="discover" and discovery.decide_with_parent or flightpath.decide)(childState)
    if out.action and out.action.method=="buddy-actions.activate_flightpath" then
        p.bookingIssued=true;p.lastBookingRevision=(memory.activation or {}).activationRevision
    end
    local waiting=top(memory)
    if p.childReply and (not waiting or not waiting.waiting or waiting.waiting.id~=p.childReply.id) then p.childReply=nil end
    if out.done==true then
        if p.stage=="discover" and (memory.discovery or {}).result=="menu_observed" then
            p.stage="flight";memory.stack=nil;out.done=false;out.phase="flight_parent_menu_ready"
        elseif p.stage=="flight" and not p.bookingIssued and (memory.activation or {}).phase=="task_binding_changed" then
            -- Replacing an expired known lease may restart only pure prebooking
            -- planning. An issued/unknown booking never enters this branch.
            p.stage="prepare";memory.stack=nil;out.done=false;out.phase="flight_parent_prebooking_custody_changed"
        else p.stage="cleanup";out.done=false;out.phase="flight_parent_cleanup" end
    end
    return out
end
return M
