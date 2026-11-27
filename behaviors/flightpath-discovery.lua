-- Standalone task-stack policy. It discovers one actual, nearby flightmaster;
-- a known-node mask is never a route, price, activation or arrival receipt.
local M = {tuning={interaction_yards=5,lease_ms=15000}}
local method = "buddy-actions.query_flightpath"
local maximum = "18446744073709551615"
local function integer(n) return type(n)=="number" and n==n and n>=0 and n<math.huge and n==math.floor(n) end
local function uint(v,zero)
    if type(v)~="string" or #v==0 or #v>20 or #v>1 and byte_at(v,1)==48 then return end
    for i=1,#v do local b=byte_at(v,i);if b<48 or b>57 then return end end
    if #v==20 and v>maximum or not zero and v=="0" then return end
    return v
end
local function revision(v) if v==nil then return "0" end;return uint(v,true) end
local function fresh(v)
    local age=revision(v)
    return age and (#age<4 or #age==4 and age<="2000")
end
local function successor(v)
    if v==maximum then return end
    local out,carry="",1
    for i=#v,1,-1 do
        local n=byte_at(v,i)-48+carry
        if n==10 then n,carry=0,1 else carry=0 end
        out=tostring(n)..out
    end
    return carry==1 and "1"..out or out
end
local function point(p)
    if type(p)~="table" or p.present~=true or p.positionKnown~=true or p.mapKnown~=true then return end
    for _,key in ipairs({"x","y","z"}) do
        local n=p[key] or 0
        if type(n)~="number" or n~=n or math.abs(n)>40000 then return end
    end
    return p
end
local function distance(a,b)
    return math.sqrt(((a.x or 0)-(b.x or 0))^2+((a.y or 0)-(b.y or 0))^2+((a.z or 0)-(b.z or 0))^2)
end
local function quiet(context)
    local a=context.observations.actions or {}
    local self=(context.observations.world or {}).self or {}
    for _,key in ipairs({"moving","castPending","motionUnconfirmed","autoAttacking","autoAttackRequested",
                        "autoAttackUnconfirmed","practicing","traveling"}) do
        if a[key]==true then return false end
    end
    if (a.recovery or {}).active==true or self.unitFlagsKnown~=true or self.movementFlagsKnown~=true or
       self.channelSpellKnown~=true or self.channelObjectKnown~=true then return false end
    local flags,movement=self.unitFlags or 0,self.movementFlags or 0
    return integer(flags) and math.floor(flags/0x80000)%2==0 and integer(movement) and movement%256==0 and
           (self.channelSpellId or 0)==0 and revision(self.channelObjectGuid)=="0"
end
local function bound(context,frame)
    local body=context.state.body or {}
    local world=context.observations.world or {}
    local fp=context.observations.flightpath or {}
    local a=context.observations.actions or {}
    local self=world.self or {}
    local visit=uint(world.visitRevision)
    if type(body.sessionId)~="string" or body.sessionId=="" or not uint(body.selfGuid) or
       a.sessionId~=body.sessionId or a.selfGuid~=body.selfGuid or self.guid~=body.selfGuid or
       fp.sessionId~=body.sessionId or fp.selfGuid~=body.selfGuid or fp.visitBound~=true or
       not visit or fp.visitId~=visit or not revision(fp.revision) then return end
    if frame.sessionId and (frame.sessionId~=body.sessionId or frame.selfGuid~=body.selfGuid or frame.visitId~=visit) then
        return false
    end
    return true,body,visit
end
local function choose(context,selected)
    local world=context.observations.world or {}
    local self=point(world.self)
    if not self or self.aliveKnown~=true or self.alive~=true then return end
    -- The normal mover need not receive an echo of its own walk. Use its
    -- bound Actions pose to select a candidate. Native still requires the
    -- exact current physical STOP before it can use an older World sample.
    local client=(context.observations.actions or {}).position
    local origin=self
    if client and client.known and client.mapKnown and (client.mapId or 0)==(self.mapId or 0) and uint(client.revision) then
        origin=client
    end
    local found,best,seen=nil,nil,{}
    local candidates={world.requestedCreature}
    for _,npc in ipairs(world.nearbyCreatures or {}) do candidates[#candidates+1]=npc end
    for _,npc in pairs(candidates) do
        local p=point(npc)
        if p and uint(p.guid) and (not selected or p.guid==selected) and p.aliveKnown==true and p.alive==true and
           (p.mapId or 0)==(self.mapId or 0) and uint(p.positionRevision) and
           (p.stationaryPositionKnown==true or fresh(p.positionAgeMs) and fresh(p.observationAgeMs)) and
           (not p.trajectory or p.trajectory.known~=true or p.trajectory.stopped==true and not p.trajectory.transportRelative) and
           (p.movementFlags or 0)%256==0 and (p.movementFlags or 0)%0x10000000<=0x100 and (p.movementFlags or 0)<0x80000000 and
           integer(p.npcFlags) and math.floor(p.npcFlags/8192)%2==1 then
            local prior=seen[p.guid]
            if prior and ((prior.x or 0)~=(p.x or 0) or (prior.y or 0)~=(p.y or 0) or (prior.z or 0)~=(p.z or 0) or
               prior.positionRevision~=p.positionRevision) then return nil,"flightmaster_views_disagree" end
            seen[p.guid]=p
            local d=distance(origin,p)
            if not best or d<best or d==best and p.guid<found.guid then found,best=p,d end
        end
    end
    return found,best
end
local function matches(op,request)
    return type(op)=="table" and request and op.revision==request.revision and op.sessionId==request.sessionId and
           op.selfGuid==request.selfGuid and op.visitId==request.visitId and op.npcGuid==request.npcGuid and op.mode==request.mode
end
local function terminal(op)
    if op.pending~=nil and op.pending~=false then return end
    if op.phase=="not_dispatched" and op.transportWritten~=true and op.transportUnknown~=true then return "not_dispatched" end
    if op.transportWritten~=true and op.transportUnknown~=true then return end
    if op.phase=="status_observed" and op.mode=="status" and op.statusKnown==true then return "status" end
    if op.phase=="learned" and op.mode=="discover" and op.statusKnown==true and op.nodeKnown==true and
       op.learningNoticeObserved==true and op.learned==true then return "learned" end
    if op.phase=="menu_observed" and op.mode=="discover" and op.menuKnown==true and integer(op.sourceNode) and op.sourceNode>0 and op.sourceNode<=4294967295 then
        return "menu"
    end
end
local function finish(context,frame,reason)
    frame.result=reason;frame.stage="release"
    context.memory.discovery={npcGuid=frame.npcGuid,result=reason,learned=frame.learned==true,
        sourceNode=frame.sourceNode,knownNodes=frame.knownNodes}
end
local function advance(context,frame,op)
    local kind=terminal(op)
    if kind=="not_dispatched" then finish(context,frame,"query_not_dispatched")
    elseif kind=="status" then frame.stage=op.nodeKnown==true and "menu" or "learn";frame.request=nil
    elseif kind=="learned" then frame.learned=true;frame.stage="menu";frame.request=nil
    elseif kind=="menu" then
        frame.sourceNode,frame.knownNodes=op.sourceNode,op.knownNodes or {}
        finish(context,frame,"menu_observed")
    end
    return kind
end
local function call_query(context,frame,mode)
    local fp=context.observations.flightpath or {}
    if not frame.waiting then
        if (fp.operation or {}).pending==true then return tasks.await(context,"waiting_for_native_flightpath_operation") end
        if not quiet(context) then return tasks.await(context,"waiting_for_quiet_flightpath_body") end
        local npc,d=choose(context,frame.npcGuid)
        if not npc or type(d)~="number" or d>M.tuning.interaction_yards then return tasks.await(context,"waiting_in_flightmaster_range") end
        local expected=revision(fp.revision)
        local next_revision=expected and successor(expected)
        if not next_revision then return tasks.await(context,"flightpath_revision_unavailable") end
        frame.request={sessionId=frame.sessionId,selfGuid=frame.selfGuid,visitId=frame.visitId,npcGuid=frame.npcGuid,
                       mode=mode,expectedRevision=expected,revision=next_revision}
    end
    local r=frame.request
    local reply,out=tasks.call(context,frame,method,{sessionId=r.sessionId,selfGuid=r.selfGuid,visitId=r.visitId,
        token=frame.token,npcGuid=r.npcGuid,mode=r.mode,expectedRevision=r.expectedRevision},"querying_flightpath_"..mode)
    if out then return out end
    -- Even an RPC reply is not a completed discovery. The current read feed
    -- must authenticate the exact retained native operation and its facts.
    frame.stage="observe"
    return tasks.await(context,"waiting_for_flightpath_observation")
end
local function handler(context,frame,parent_owned)
    local valid,body,visit=bound(context,frame)
    if valid==false then
        if not frame.aborted then frame.aborted=true;finish(context,frame,"body_or_visit_changed") end
    elseif not valid then return tasks.await(context,"flightpath_binding_unavailable") end
    local a=context.observations.actions or {}
    if frame.stage=="start" then
        local requested=(context.state.goal.params or {}).npcGuid
        if requested~=nil and not uint(requested) then return tasks.await(context,"invalid_flightmaster_guid") end
        local npc,d=choose(context,requested)
        if not npc then return tasks.await(context,type(d)=="string" and d or "waiting_for_observed_flightmaster") end
        if d>M.tuning.interaction_yards then return tasks.await(context,"waiting_in_flightmaster_range") end
        if (context.observations.flightpath.operation or {}).pending==true then return tasks.await(context,"waiting_for_native_flightpath_operation") end
        frame.sessionId,frame.selfGuid,frame.visitId,frame.npcGuid=body.sessionId,body.selfGuid,visit,npc.guid
        frame.stage=parent_owned and "status" or "acquire"
    end
    if parent_owned and not frame.waiting and frame.stage~="observe" then
        local token=(context.state.goal.params or {}).explorationToken
        if type(token)~="string" or token=="" or #token>128 or a.interactionOwner~="exploration" then
            return tasks.await(context,"waiting_for_parent_exploration_custody")
        end
        frame.token=token
    end
    if frame.stage=="acquire" then
        if not frame.waiting and not quiet(context) then return tasks.await(context,"waiting_for_quiet_flightpath_body") end
        if not frame.waiting and (a.interactionOwner or "")~="" then return tasks.await(context,"flightpath_interaction_owned_elsewhere") end
        local reply,out=tasks.call(context,frame,"buddy-actions.acquire_interaction",
            {owner="exploration",leaseMs=M.tuning.lease_ms,harvest=true},"acquiring_flightpath_custody")
        if out then return out end
        local state=reply.state or {}
        if type(reply.token)~="string" or reply.token=="" or state.sessionId~=frame.sessionId or state.selfGuid~=frame.selfGuid or
           state.interactionOwner~="exploration" then frame.stage="custody_unknown";return tasks.await(context,"flightpath_custody_unconfirmed") end
        frame.token=reply.token;frame.stage=frame.resume or "status";frame.resume=nil
    end
    if frame.stage=="status" or frame.stage=="learn" or frame.stage=="menu" then
        if not parent_owned and not frame.waiting and a.interactionOwner~="exploration" then
            if (a.interactionOwner or "")=="" then frame.token=nil;frame.resume=frame.stage;frame.stage="acquire" end
            return tasks.await(context,"flightpath_custody_unavailable")
        end
        return call_query(context,frame,frame.stage=="status" and "status" or "discover")
    end
    if frame.stage=="observe" then
        local fp=context.observations.flightpath or {}
        local op=fp.operation
        if not matches(op,frame.request) or revision(fp.revision)~=op.revision or not advance(context,frame,op) then
            return tasks.await(context,"waiting_for_exact_flightpath_receipt")
        end
        return tasks.await(context,"flightpath_receipt_observed")
    end
    if frame.stage=="release" then
        if parent_owned then tasks.pop(context,{status=frame.result});return end
        if frame.waiting and frame.waiting.method~="buddy-actions.release_interaction" then return tasks.await(context,"waiting_for_original_flightpath_call") end
        if frame.token and (a.interactionOwner=="exploration" or frame.waiting) then
            local reply,out=tasks.call(context,frame,"buddy-actions.release_interaction",{owner="exploration",token=frame.token},"releasing_flightpath_custody")
            if out then return out end
            if reply.sessionId~=frame.sessionId or reply.selfGuid~=frame.selfGuid or (reply.interactionOwner or "")~="" then
                frame.stage="cleanup_unknown";return tasks.await(context,"flightpath_cleanup_unconfirmed")
            end
        elseif (a.interactionOwner or "")~="" then return tasks.await(context,"flightpath_custody_replaced") end
        frame.token=nil;tasks.pop(context,{status=frame.result});return
    end
    return tasks.await(context,frame.stage=="cleanup_unknown" and "flightpath_cleanup_unconfirmed" or "flightpath_custody_unconfirmed")
end
local function decide(state,parent_owned)
    local memory=state.memory or {}
    local frame=(memory.stack or {})[#(memory.stack or {})]
    local pending=state.pending or {}
    if #pending>0 then
        local context={state=state,memory=memory,observations=state.observations or {}}
        local valid=frame and bound(context,frame)
        local row=#pending==1 and pending[1]
        local op=(context.observations.flightpath or {}).operation
        if valid==true and row and frame.waiting and row.id==frame.waiting.id and row.method==method and
           frame.waiting.method==method and row.effect=="mutation" and row.outcome=="pending" and
           row.params and frame.request and row.params.sessionId==frame.request.sessionId and row.params.selfGuid==frame.request.selfGuid and
           row.params.visitId==frame.request.visitId and row.params.npcGuid==frame.request.npcGuid and row.params.mode==frame.request.mode and
           revision(row.params.expectedRevision)==frame.request.expectedRevision and row.params.token==frame.token and matches(op,frame.request) and
           revision((context.observations.flightpath or {}).revision)==op.revision and terminal(op) then
            advance(context,frame,op)
            return tasks.resolve(context,frame,"flightpath","operation",op,"exact_native_flightpath_receipt_observed")
        end
        return {memory=memory,phase="blocked_on_ambiguous_call",done=false}
    end
    return tasks.run(state,{flightpath_discovery=function(context,frame) return handler(context,frame,parent_owned) end},"flightpath_discovery")
end
function M.decide(state) return decide(state,false) end
-- The parent supplies current finite custody and owns its cleanup. This mode
-- never acquires, renews, releases or changes movement/follow intent.
function M.decide_with_parent(state) return decide(state,true) end
return M
