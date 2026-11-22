-- Compose with the maintained task-stack and flightpath-plan modules. A normal
-- activation selects ONE hop; its receipt never establishes start or arrival.
local M = {tuning={accept_unknown_fare=true,lease_ms=15000}}
local method="buddy-actions.activate_flightpath"
local maximum="18446744073709551615"
local function uint(n) return type(n)=="number" and n==n and n>=0 and n<=4294967295 and n==math.floor(n) end
local function identifier(v,zero)
    if type(v)~="string" or #v==0 or #v>20 or #v>1 and byte_at(v,1)==48 then return end
    for i=1,#v do local b=byte_at(v,i);if b<48 or b>57 then return end end
    if #v==20 and v>maximum or not zero and v=="0" then return end
    return v
end
local function revision(v) if v==nil then return "0" end;return identifier(v,true) end
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
local function same(a,b)
    if type(a)~=type(b) then return false end
    if type(a)~="table" then return a==b end
    for k,v in pairs(a) do if not same(v,b[k]) then return false end end
    for k in pairs(b) do if a[k]==nil then return false end end
    return true
end
local function quiet(context)
    local a=context.observations.actions or {};local self=(context.observations.world or {}).self or {}
    for _,key in ipairs({"moving","castPending","motionUnconfirmed","autoAttacking","autoAttackRequested",
                        "autoAttackUnconfirmed","practicing","traveling"}) do if a[key]==true then return false end end
    local flags,movement=self.unitFlags or 0,self.movementFlags or 0
    return (a.recovery or {}).active~=true and self.present==true and self.aliveKnown==true and self.alive==true and
        self.positionKnown==true and self.positionInferred~=true and self.mapKnown==true and self.unitFlagsKnown==true and
        uint(flags) and math.floor(flags/0x80000)%2==0 and self.movementFlagsKnown==true and uint(movement) and movement%256==0 and
        self.channelSpellKnown==true and self.channelObjectKnown==true and (self.channelSpellId or 0)==0 and revision(self.channelObjectGuid)=="0"
end
local function wallet(context)
    local world=context.observations.world or {}
    if world.selfCopperKnown==true and uint(world.selfCopper or 0) and identifier(world.selfCopperRevision) then
        return world.selfCopper or 0,world.selfCopperRevision
    end
end
local function current_scope(context,binding)
    local body=context.state.body or {};local observations=context.observations
    local world,actions,flight=observations.world or {},observations.actions or {},observations.flightpath or {}
    return body.sessionId==binding.sessionId and body.selfGuid==binding.selfGuid and (world.self or {}).guid==binding.selfGuid and
        world.visitRevision==binding.visitId and actions.sessionId==binding.sessionId and actions.selfGuid==binding.selfGuid and
        flight.sessionId==binding.sessionId and flight.selfGuid==binding.selfGuid and flight.visitBound==true and flight.visitId==binding.visitId
end
local function matches(op,request)
    return type(op)=="table" and request and op.revision==request.revision and op.sessionId==request.sessionId and
        op.selfGuid==request.selfGuid and op.visitId==request.visitId and op.npcGuid==request.npcGuid and
        op.menuRevision==request.menuRevision and op.sourceNode==request.sourceNode and op.destinationNode==request.destinationNode
end
local function terminal(op)
    if op.pending~=nil and op.pending~=false then return end
    if op.phase=="not_dispatched" and op.transportWritten~=true and op.transportUnknown~=true and
       op.replyKnown~=true and op.activationAccepted~=true then return "not_dispatched" end
    if op.phase=="server_rejected" and op.replyKnown==true and uint(op.replyCode) and op.replyCode>=1 and op.replyCode<=12 and
       op.activationAccepted~=true and (op.transportWritten==true or op.transportUnknown==true) then return "server_rejected" end
end
local function status(context,frame,phase,op)
    context.memory.activation={phase=phase,source=frame.binding,finalDestinationNode=frame.destination,
        destinationNode=frame.hop,activationRevision=frame.request and frame.request.revision,
        acceptUnknownFare=frame.request and frame.request.acceptUnknownFare,
        pending=op and op.pending==true or false,replyKnown=op and op.replyKnown==true or false,
        replyCode=op and op.replyKnown==true and (op.replyCode or 0) or nil,
        activationAccepted=op and op.activationAccepted==true or false,
        liveFareKnown=false,liveRouteKnown=false,flightStartedKnown=false,arrivalKnown=false}
end
local function finish(context,frame,reason,op)
    frame.finishing=true;frame.result=reason;frame.stage="release";status(context,frame,reason,op)
end
local function request_parameters(request)
    return {sessionId=request.sessionId,selfGuid=request.selfGuid,visitId=request.visitId,token=request.token,npcGuid=request.npcGuid,
        sourceNode=request.sourceNode,destinationNode=request.destinationNode,menuRevision=request.menuRevision,
        expectedRevision=request.expectedRevision,expectedCopperRevision=request.expectedCopperRevision,
        expectedCopper=request.expectedCopper,acceptUnknownFare=request.acceptUnknownFare}
end
local function same_parameters(params,request)
    if type(params)~="table" then return false end
    local expected=request_parameters(request)
    for key in pairs(params) do if expected[key]==nil then return false end end
    for key,value in pairs(expected) do
        local observed=params[key]
        if key=="expectedRevision" then observed=revision(observed)
        elseif key=="expectedCopper" then observed=observed or 0 end
        if observed~=value then return false end
    end
    return true
end
local function handler(context,frame)
    local flight=context.observations.flightpath or {};local actions=context.observations.actions or {}
    -- A proposed or issued activation keeps its original identity even if the
    -- body/menu/lease later changes. Only its exact native receipt settles it.
    if not frame.request and not frame.finishing then
        if (flight.activation or {}).pending==true then return tasks.await(context,"waiting_for_existing_native_activation") end
        local binding=planner.menu_binding(context.state)
        local params=context.state.goal.params or {}
        if not binding then
            if frame.binding then finish(context,frame,"menu_binding_changed")
            else return tasks.await(context,"waiting_for_current_flightpath_menu") end
        elseif frame.binding and (not same(frame.binding,binding) or frame.destination~=params.destinationNode or
               frame.goalFarePresent~=(params.acceptUnknownFare~=nil) or frame.goalFare~=params.acceptUnknownFare) then
            finish(context,frame,"task_binding_changed")
        elseif frame.planKey and frame.planKey~=planner.policy_key() then
            finish(context,frame,"planner_policy_changed")
        elseif frame.stage=="start" then
            if not uint(params.destinationNode) or params.destinationNode==0 or
               params.acceptUnknownFare~=nil and type(params.acceptUnknownFare)~="boolean" then return tasks.await(context,"invalid_activation_intent") end
            frame.binding,frame.destination=binding,params.destinationNode
            frame.goalFarePresent,frame.goalFare=params.acceptUnknownFare~=nil,params.acceptUnknownFare
            local key=planner.policy_key()
            if type(key)~="string" or key=="" then return tasks.await(context,"planner_tuning_unavailable") end
            local plan=context.memory.plan
            if plan and plan.status=="planned" and same(plan.source,binding) and plan.destinationNode==frame.destination and
               plan.policyKey==key then
                frame.planKey=plan.policyKey;frame.stage="ready"
            else context.memory.plan=nil;frame.stage="plan_wait";tasks.push(context,"flightpath_plan",{});return end
        end
    end
    if frame.stage=="plan_wait" then
        local plan=context.memory.plan
        if not plan or plan.status~="planned" or not same(plan.source,frame.binding) or plan.destinationNode~=frame.destination or
           type(plan.policyKey)~="string" or plan.policyKey=="" or plan.policyKey~=planner.policy_key() then
            finish(context,frame,"reference_plan_unavailable")
        else frame.planKey=plan.policyKey;frame.stage="ready" end
    end
    if frame.stage=="ready" then
        local plan=context.memory.plan;local nodes=plan and plan.nodeIds or {}
        if not plan or plan.status~="planned" or not same(plan.source,frame.binding) or plan.destinationNode~=frame.destination or
           plan.policyKey~=frame.planKey or nodes[1]~=frame.binding.sourceNode then finish(context,frame,"reference_plan_changed")
        elseif not nodes[2] then finish(context,frame,"destination_is_menu_source")
        else
            frame.hop=nodes[2]
            local accept=frame.goalFarePresent and frame.goalFare or M.tuning.accept_unknown_fare
            -- Explicit false must not fall through Lua's false-or-default rule.
            if frame.goalFarePresent then accept=frame.goalFare end
            if accept~=true then status(context,frame,"unknown_fare_not_accepted");return tasks.await(context,"unknown_fare_not_accepted") end
            if wallet(context)==nil then return tasks.await(context,"waiting_for_known_wallet") end
            if not quiet(context) then return tasks.await(context,"waiting_for_quiet_activation_body") end
            frame.stage="acquire"
        end
    end
    if frame.stage=="acquire" then
        if not frame.waiting then
            if not quiet(context) then return tasks.await(context,"waiting_for_quiet_activation_body") end
            if (actions.interactionOwner or "")~="" then return tasks.await(context,"activation_interaction_owned_elsewhere") end
        end
        local reply,out=tasks.call(context,frame,"buddy-actions.acquire_interaction",{owner="exploration",leaseMs=M.tuning.lease_ms,harvest=true},"acquiring_activation_custody")
        if out then return out end
        local state=reply.state or {}
        if type(reply.token)~="string" or reply.token=="" or state.sessionId~=frame.binding.sessionId or state.selfGuid~=frame.binding.selfGuid or
           state.interactionOwner~="exploration" then frame.stage="custody_unknown";return tasks.await(context,"activation_custody_unconfirmed") end
        frame.token=reply.token;frame.stage="activate"
    end
    if frame.stage=="activate" then
        if not frame.waiting then
            if not quiet(context) then return tasks.await(context,"waiting_for_quiet_activation_body") end
            local copper,copper_revision=wallet(context)
            if copper==nil then return tasks.await(context,"waiting_for_known_wallet") end
            local expected=revision(flight.activationRevision);local next_revision=expected and successor(expected)
            if not next_revision then return tasks.await(context,"activation_revision_unavailable") end
            if actions.interactionOwner~="exploration" then finish(context,frame,"activation_custody_lost")
            else
                local accept=frame.goalFarePresent and frame.goalFare or M.tuning.accept_unknown_fare
                if frame.goalFarePresent then accept=frame.goalFare end
                if accept~=true then finish(context,frame,"unknown_fare_not_accepted")
                else
                    frame.request={sessionId=frame.binding.sessionId,selfGuid=frame.binding.selfGuid,visitId=frame.binding.visitId,
                        token=frame.token,npcGuid=frame.binding.npcGuid,sourceNode=frame.binding.sourceNode,destinationNode=frame.hop,
                        menuRevision=frame.binding.menuRevision,expectedRevision=expected,revision=next_revision,
                        expectedCopperRevision=copper_revision,expectedCopper=copper,acceptUnknownFare=true}
                    status(context,frame,"activation_proposed")
                end
            end
        end
        if frame.request then
            local reply,out=tasks.call(context,frame,method,request_parameters(frame.request),"activating_selected_hop")
            if out then return out end
            frame.stage="observe"
            return tasks.await(context,"waiting_for_exact_activation_receipt")
        end
    end
    if frame.stage=="observe" then
        local op=flight.activation
        if not matches(op,frame.request) or revision(flight.activationRevision)~=op.revision then return tasks.await(context,"waiting_for_exact_activation_receipt") end
        local result=terminal(op)
        if result then finish(context,frame,result,op)
        else
            status(context,frame,op.phase or "activation_unsettled",op)
            return tasks.await(context,"activation_unsettled")
        end
    end
    if frame.stage=="release" then
        if frame.waiting and frame.waiting.method=="buddy-actions.acquire_interaction" then
            local reply,out=tasks.call(context,frame,"buddy-actions.acquire_interaction",{})
            if out then return out end
            local state=reply.state or {}
            if type(reply.token)~="string" or reply.token=="" or state.sessionId~=frame.binding.sessionId or
               state.selfGuid~=frame.binding.selfGuid or state.interactionOwner~="exploration" then
                frame.stage="custody_unknown";return tasks.await(context,"activation_custody_unconfirmed")
            end
            frame.token=reply.token
        end
        if frame.token and not frame.waiting and not current_scope(context,frame.binding) then
            -- The old finite lease has no authority over a replaced scope.
            -- Forget only this task's token; native expiry owns its cleanup.
            frame.token=nil
        end
        if frame.waiting and frame.waiting.method~="buddy-actions.release_interaction" then return tasks.await(context,"waiting_for_original_activation_call") end
        if frame.token and (actions.interactionOwner=="exploration" or frame.waiting) then
            local reply,out=tasks.call(context,frame,"buddy-actions.release_interaction",{owner="exploration",token=frame.token,
                sessionId=frame.binding.sessionId,selfGuid=frame.binding.selfGuid,visitId=frame.binding.visitId},"releasing_activation_custody")
            if out then return out end
            if reply.sessionId~=frame.binding.sessionId or reply.selfGuid~=frame.binding.selfGuid or (reply.interactionOwner or "")~="" then
                frame.stage="cleanup_unknown";return tasks.await(context,"activation_cleanup_unconfirmed")
            end
        elseif frame.token and (actions.interactionOwner or "")~="" then return tasks.await(context,"activation_custody_replaced") end
        frame.token=nil;tasks.pop(context,{status=frame.result});return
    end
    return tasks.await(context,frame.stage=="cleanup_unknown" and "activation_cleanup_unconfirmed" or "activation_custody_unconfirmed")
end
function M.decide(state)
    local memory=state.memory or {};local pending=state.pending or {}
    if #pending>0 then
        local context={state=state,memory=memory,observations=state.observations or {}}
        local frame=(memory.stack or {})[#(memory.stack or {})]
        local row=#pending==1 and pending[1]
        local flight=context.observations.flightpath or {};local op=flight.activation
        local result=frame and frame.request and matches(op,frame.request) and terminal(op)
        if frame and frame.kind=="flightpath_activation" and frame.request and frame.waiting and row and row.id==frame.waiting.id and
           row.method==method and frame.waiting.method==method and row.effect=="mutation" and row.outcome=="pending" and
           same_parameters(row.params,frame.request) and matches(op,frame.request) and revision(flight.activationRevision)==op.revision and
           result then
            finish(context,frame,result,op)
            return tasks.resolve(context,frame,"flightpath","activation",op,"exact_native_activation_"..result)
        end
        return {memory=memory,phase="blocked_on_ambiguous_call",done=false}
    end
    return tasks.run(state,{flightpath_activation=handler,flightpath_plan=planner.handler},"flightpath_activation")
end
return M
