-- A parent owns the finite exploration lease and supplies explorationToken.
-- This child composes the maintained planner and selects ONE normal hop.
-- An activation receipt never establishes flight start, arrival or a fare.
local M = {tuning={accept_unknown_fare=true,complete_hops=true,continue_hops=true,retry_refused_queries=false,adopt_transfers=true,retry_refused_adoptions=false}}
local method="buddy-actions.activate_flightpath"
local maximum="18446744073709551615"
local function uint(n) return type(n)=="number" and n==n and n>=0 and n<=4294967295 and n==math.floor(n) end
local identifier,same=planner.identifier,planner.same
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
        currentVisitId=frame.currentVisitId,adoptionRevision=frame.adoptionRevision,
        liveFareKnown=false,liveRouteKnown=false,flightStartedKnown=false,arrivalKnown=false}
end
local function finish(context,frame,reason,op)
    frame.result=reason;frame.stage="complete";status(context,frame,reason,op)
end
local function request_parameters(request)
    local out={};for key,value in pairs(request) do if key~="revision" then out[key]=value end end;return out
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
local function valid_token(v) return type(v)=="string" and #v>0 and #v<=128 end
local function intent_changed(frame,params)
    return frame.destination~=params.destinationNode or frame.parentToken~=params.explorationToken or
        frame.goalFarePresent~=(params.acceptUnknownFare~=nil) or frame.goalFare~=params.acceptUnknownFare
end
local function newer(a,b)
    a,b=revision(a),revision(b)
    return a and b and (#a>#b or #a==#b and a>b)
end
local function source(a,b)
    if not a or not b then return false end
    for _,key in ipairs({"connectionId","selfGuid","visitRevision","splineRevision","splineSourceRevision","splineSourceOpcode"}) do
        if a[key]==nil or a[key]~=b[key] then return false end
    end
    return a.visitBound==true and b.visitBound==true and (a.mapId or 0)==(b.mapId or 0) and
        a.splineIdKnown==true and b.splineIdKnown==true and (a.splineId or 0)==(b.splineId or 0)
end
local function complete_match(op,request,expected)
    if not op or op.revision~=expected or not request or not same(op.transportScope,request.transportScope) then return false end
    for _,key in ipairs({"selfGuid","visitRevision","authorityRevision","movementRevision","splineRevision"}) do
        if op[key]~=request[key] then return false end
    end
    return true
end
local methods={complete="buddy-world.complete_spline",retire="buddy-world.retire_spline_completion",
               probe="buddy-actions.query_flightpath",settle="buddy-actions.settle_flightpath",
               adopt="buddy-actions.adopt_flightpath_transfer"}
local after={complete="ground",retire="ground",probe="menu",settle="settled",adopt="adopted"}
local function issue(frame,kind,params,expected)
    frame.effect={kind=kind,params=params,revision=expected};frame.stage="effect"
end
local function ground(w,op)
    local a,m,p=w.selfMotionAuthority or {},w.selfMovementFacts or {},w.self or {}
    local i=m.info or {};local scope=a.transportScope
    return scope and source(scope,op.transportScope) and source(m.transportScope,scope) and
        m.known==true and m.selfGuid==op.selfGuid and m.visitRevision==op.visitRevision and
        newer(m.revision,op.movementRevision) and newer(m.transportScope.motionRevision,op.transportScope.motionRevision) and
        a.unitFlagsKnown==true and uint(a.unitFlags or 0) and math.floor((a.unitFlags or 0)/0x100000)%2==0 and
        newer(a.unitFlagsMotionRevision,op.transportScope.motionRevision) and p.present==true and
        p.aliveKnown==true and p.alive==true and p.positionKnown==true and p.positionInferred~=true and
        (p.x or 0)==(i.x or 0) and (p.y or 0)==(i.y or 0) and (p.z or 0)==(i.z or 0)
end
local function world_binding(w)
    local a=w.selfMotionAuthority or {}
    return {selfGuid=(w.self or {}).guid,visitRevision=w.visitRevision,authorityRevision=a.revision,
        movementRevision=(w.selfMovementFacts or {}).revision,unitFlagsRevision=a.unitFlagsRevision,transportScope=a.transportScope}
end
local function flightmaster(w)
    local p=w.self or {};local best,distance
    for _,npc in ipairs(w.nearbyCreatures or {}) do
        if identifier(npc.guid) and npc.present==true and npc.aliveKnown==true and npc.alive==true and
           npc.positionKnown==true and npc.mapKnown==true and (npc.mapId or 0)==(p.mapId or 0) and
           uint(npc.npcFlags or 0) and math.floor((npc.npcFlags or 0)/0x2000)%2==1 then
            local x,y,z=(npc.x or 0)-(p.x or 0),(npc.y or 0)-(p.y or 0),(npc.z or 0)-(p.z or 0)
            local d=x*x+y*y+z*z
            if d<=25 and math.abs(z)<=2.5 and (not distance or d<distance) then best,distance=npc.guid,d end
        end
    end
    return best
end
local function adopted_segment(w,op,frame,request,expected)
    local segment=op.currentSegment;local receipt=(w.splineCompletion or {}).transferAdoption
    if type(segment)~="table" or type(receipt)~="table" or not identifier(segment.worldAdoptionRevision) or
       segment.worldAdoptionRevision~=receipt.revision or receipt.selfGuid~=frame.request.selfGuid or
       receipt.fromVisitRevision~=(frame.currentVisitId or frame.request.visitId) or receipt.toVisitRevision~=w.visitRevision or
       segment.visitId~=w.visitRevision or segment.parentLeaseRequired~=true then return end
    local edge=receipt.transfer or {};local core=edge.core or {}
    if edge.known~=true or edge.continuationKnown~=true or edge.predecessorQualified~=true or
       segment.worldTransferRevision~=edge.revision or segment.coreTransferRevision~=core.revision or
       not same(segment.sourceScope,edge.currentScope) or
       not source(segment.sourceScope,(w.selfMotionAuthority or {}).transportScope) then return end
    if request and (receipt.revision~=expected or request.activationRevision~=op.revision or
       request.visitId~=receipt.toVisitRevision or request.worldTransferRevision~=edge.revision or
       request.coreTransferRevision~=core.revision or not same(request.transportScope,edge.currentScope) or
       request.authorityRevision~=edge.currentAuthorityRevision or request.movementRevision~=edge.currentMovementRevision or
       request.splineRevision~=edge.currentSplineRevision or request.unitFlagsRevision~=edge.currentUnitFlagsRevision) then return end
    return segment,receipt
end
local function propose_adoption(context,frame,op,w,previous)
    local edge=w.worldTransfer or {};local core=edge.core or {};local old=edge.predecessorCompletion or {}
    local a=w.selfMotionAuthority or {};local c=w.splineCompletion or {}
    local current=op.currentSegment
    local prior=frame.currentSource
    if prior then
        if not current or current.visitId~=frame.currentVisitId or current.worldAdoptionRevision~=frame.adoptionRevision or
           not same(current.sourceScope,prior) then return tasks.await(context,"waiting_for_current_booking_segment") end
    elseif current~=nil then return tasks.await(context,"waiting_for_current_booking_segment") end
    if M.tuning.adopt_transfers~=true or op.pending~=true or op.activationAccepted~=true or
       op.replyKnown~=true or (op.replyCode or 0)~=0 or not op.acceptanceScope or edge.known~=true or
       (context.observations.flightpath or {}).visitId~=w.visitRevision or
       edge.continuationKnown~=true or edge.predecessorQualified~=true or edge.fromVisitRevision~=(frame.currentVisitId or frame.request.visitId) or
       edge.toVisitRevision~=w.visitRevision or edge.selfGuid~=frame.request.selfGuid or a.known~=true or
       not same(edge.currentScope,a.transportScope) or not identifier(edge.revision) or not identifier(core.revision) or
       not frame.completion or not complete_match(old,frame.completion,frame.completionRevision) or
       (old.transportWritten~=true and old.transportUnknown~=true) or old.retired==true or
       not source(old.transportScope,edge.predecessorAuthority and edge.predecessorAuthority.transportScope) then
        return tasks.await(context,"waiting_for_adjacent_accepted_transfer")
    end
    local expected=revision(c.revision);local next_revision=expected and successor(expected)
    if not next_revision then return tasks.await(context,"transfer_adoption_revision_unavailable") end
    local request={sessionId=frame.request.sessionId,selfGuid=frame.request.selfGuid,visitId=w.visitRevision,
        activationRevision=frame.request.revision,worldTransferRevision=edge.revision,expectedCompletionRevision=expected,
        authorityRevision=a.revision,movementRevision=(w.selfMovementFacts or {}).revision,splineRevision=a.splineRevision,
        unitFlagsRevision=a.unitFlagsRevision,transportScope=a.transportScope,coreTransferRevision=core.revision}
    if previous and same(request,previous) then return tasks.await(context,"waiting_for_fresh_transfer_evidence") end
    issue(frame,"adopt",request,next_revision)
end
local function progress(context,frame,flight,op)
    local w=context.observations.world or {};local a=w.selfMotionAuthority or {};local c=w.splineCompletion or {}
    local proof=c.operation or {};local binding=world_binding(w)
    status(context,frame,frame.stage,op);context.memory.activation.flightStartedKnown=frame.completion~=nil
    if (w.self or {}).guid~=frame.request.selfGuid or
       (context.state.body or {}).sessionId~=frame.request.sessionId then return tasks.await(context,"flight_scope_replaced") end
    -- Consume an existing effect's matching reply before changing the visit or
    -- effect kind. A transfer may reach observations before the next decision.
    if frame.stage=="effect" then
        local e=frame.effect;local _,out=tasks.call(context,frame,methods[e.kind],e.params,"flight_"..e.kind)
        if out then return out end;frame.stage=after[e.kind]
        local attempt=flight.transferAttempt or {}
        if e.kind=="adopt" and attempt.phase=="not_invoked" and attempt.worldInvoked~=true and
           same(attempt.request,e.params) then frame.stage="adoption_refused" end
    end
    if frame.stage=="adoption_refused" then
        if M.tuning.retry_refused_adoptions~=true then return tasks.await(context,"transfer_adoption_not_invoked") end
        return propose_adoption(context,frame,op,w,frame.effect.params)
    end
    if frame.stage=="adopted" then
        local segment,receipt=adopted_segment(w,op,frame,frame.effect and frame.effect.params,frame.effect and frame.effect.revision)
        if not segment then return tasks.await(context,"waiting_for_native_transfer_binding") end
        frame.currentVisitId,frame.currentSource=segment.visitId,segment.sourceScope
        frame.adoptionRevision=receipt.revision;frame.completion,frame.completionRevision=nil,nil
        frame.effect=nil;frame.stage="flight"
    end
    if w.visitRevision~=(frame.currentVisitId or frame.request.visitId) then
        if frame.waiting then return tasks.await(context,"waiting_for_previous_flight_effect") end
        return propose_adoption(context,frame,op,w)
    end
    if frame.stage=="flight" then
        local acceptance=frame.currentSource or op.acceptanceScope
        if M.tuning.complete_hops~=true or a.known~=true or not a.transportScope or
           not acceptance or a.transportScope.connectionId~=acceptance.connectionId or
           a.transportScope.visitRevision~=acceptance.visitRevision or
           (frame.currentSource and not source(a.transportScope,frame.currentSource)) or
           (not frame.currentSource and not newer(a.transportScope.splineSourceRevision,acceptance.motionRevision)) or
           revision(a.durationRemainingMs)~="0" then return tasks.await(context,"waiting_for_qualified_normal_spline") end
        binding.unitFlagsRevision=nil;binding.splineRevision=a.splineRevision;binding.expectedRevision=revision(c.revision)
        frame.completion=binding;frame.completionRevision=successor(binding.expectedRevision)
        if not frame.completionRevision then return tasks.await(context,"completion_revision_unavailable") end
        issue(frame,"complete",binding,frame.completionRevision);return
    end
    if frame.stage=="ground" then
        if not complete_match(proof,frame.completion,frame.completionRevision) then return tasks.await(context,"waiting_for_exact_completion_receipt") end
        if proof.phase=="not_dispatched" then
            if a.known==true and a.transportScope and a.transportScope.splineRevision~=proof.transportScope.splineRevision then frame.stage="flight";return end
            return tasks.await(context,"completion_refused_for_current_source")
        end
        if proof.retired==true then
            if c.retirementCurrent~=true then return tasks.await(context,"retired_flight_source_unavailable") end
            frame.stage="probe"
        elseif c.retirementAvailable==true and ground(w,proof) then
            binding.expectedRevision=revision(c.revision)
            issue(frame,"retire",binding,successor(binding.expectedRevision));return
        else return tasks.await(context,"actual_landing_evidence_unavailable") end
    end
    if frame.stage=="probe" then
        local params=context.state.goal.params or {};local npc=flightmaster(w);local age=revision((w.self or {}).positionAgeMs)
        if c.retirementCurrent~=true or not quiet(context) or not age or newer(age,"2000") or not npc or not valid_token(params.explorationToken) or
           (context.observations.actions or {}).interactionOwner~="exploration" then return tasks.await(context,"waiting_for_arrival_menu_authority") end
        if frame.currentVisitId and params.explorationToken==frame.parentToken then
            return tasks.await(context,"fresh_parent_visit_lease_required")
        end
        local request={sessionId=frame.request.sessionId,selfGuid=frame.request.selfGuid,visitId=frame.currentVisitId or frame.request.visitId,
            token=params.explorationToken,npcGuid=npc,mode="arrival",expectedRevision=revision(flight.revision),
            activationRevision=frame.request.revision,completionRevision=c.revision}
        issue(frame,"probe",request,successor(request.expectedRevision));return
    end
    if frame.stage=="menu" then
        local menu=flight.operation or {};local e=frame.effect
        if menu.revision==e.revision and menu.phase=="not_dispatched" then
            if M.tuning.retry_refused_queries==true then frame.stage="probe";return end
            return tasks.await(context,"arrival_query_not_dispatched")
        end
        if menu.revision~=e.revision or menu.npcGuid~=e.params.npcGuid or menu.activationRevision~=frame.request.revision or
           menu.completionRevision~=c.revision or menu.menuKnown~=true or menu.pending==true or
           menu.sourceNode~=frame.hop then return tasks.await(context,"waiting_for_actual_destination_menu") end
        binding.visitId=binding.visitRevision;binding.visitRevision=nil;binding.sessionId=frame.request.sessionId
        binding.activationRevision=frame.request.revision;binding.menuRevision=menu.revision;binding.completionRevision=c.revision
        issue(frame,"settle",binding,frame.request.revision);return
    end
    if frame.stage=="settled" then
        if op.pending==true or op.arrivalKnown~=true or op.phase~="observed_arrival" then return tasks.await(context,"waiting_for_native_arrival_settlement") end
        status(context,frame,"arrived",op);context.memory.activation.arrivalKnown=true;context.memory.activation.flightStartedKnown=true
        if frame.hop==(context.state.goal.params or {}).destinationNode then frame.result="arrived";frame.stage="complete"
        elseif M.tuning.continue_hops==true then
            context.memory.plan=nil;context.memory.stack[#context.memory.stack]={kind="flightpath_activation",stage="start",data={}}
        else return tasks.await(context,"hop_arrived_continuation_disabled") end
    end
end
local function effect_receipt(context,frame)
    local e=frame.effect;if not e then return end
    local w=context.observations.world or {};local c=w.splineCompletion or {}
    local f=context.observations.flightpath or {};local op=c.operation
    local attempt=f.transferAttempt or {}
    if e.kind=="adopt" and attempt.phase=="not_invoked" and attempt.worldInvoked~=true and
       same(attempt.request,e.params) then return "flightpath","transferAttempt",attempt end
    if e.kind=="adopt" and matches(f.activation,frame.request) and
       adopted_segment(w,f.activation,frame,e.params,e.revision) then return "flightpath","activation",f.activation end
    if e.kind=="complete" and complete_match(op,e.params,e.revision) and
       (op.transportWritten==true or op.transportUnknown==true or op.phase=="not_dispatched") then return "world","splineCompletion.operation",op end
    local predecessor=((c.transferAdoption or {}).transfer or {}).predecessorCompletion
    if e.kind=="complete" and complete_match(predecessor,e.params,e.revision) and
       (predecessor.transportWritten==true or predecessor.transportUnknown==true) then
        return "world","splineCompletion.transferAdoption.transfer.predecessorCompletion",predecessor
    end
    if e.kind=="retire" and op and op.retired==true and op.retirementRevision==e.revision and
       op.retiredMovementRevision==e.params.movementRevision and op.retiredUnitFlagsRevision==e.params.unitFlagsRevision and
       same(op.retirementScope,e.params.transportScope) then return "world","splineCompletion.operation",op end
    if e.kind=="probe" then
        op=f.operation
        if op and op.revision==e.revision and op.npcGuid==e.params.npcGuid and op.mode=="arrival" and
           op.activationRevision==e.params.activationRevision and op.completionRevision==e.params.completionRevision and
           (op.transportWritten==true or op.transportUnknown==true or op.phase=="not_dispatched") then return "flightpath","operation",op end
    end
    if e.kind=="settle" and matches(f.activation,frame.request) and f.activation.arrivalKnown==true and f.activation.pending~=true and
       (f.operation or {}).revision==e.params.menuRevision and c.revision==e.params.completionRevision then return "flightpath","activation",f.activation end
end
local function effect_parameters(params,expected)
    if type(params)~="table" then return false end
    for key in pairs(params) do if expected[key]==nil then return false end end
    for key,value in pairs(expected) do
        if key=="expectedRevision" then
            if revision(params[key])~=value then return false end
        elseif not same(params[key],value) then return false end
    end
    return true
end
local function handler(context,frame)
    if frame.stage=="complete" then tasks.pop(context,{status=frame.result});return end
    local flight=context.observations.flightpath or {};local actions=context.observations.actions or {}
    -- Once proposed/issued, the exact request survives all later scope or
    -- parent-token changes until its native receipt can settle this child.
    if not frame.request then
        if (flight.activation or {}).pending==true then return tasks.await(context,"waiting_for_existing_native_activation") end
        local binding=planner.menu_binding(context.state)
        local params=context.state.goal.params or {}
        if frame.binding and (not binding or not same(frame.binding,binding) or intent_changed(frame,params)) then
            finish(context,frame,"task_binding_changed")
        elseif frame.planKey and frame.planKey~=planner.policy_key() then finish(context,frame,"planner_policy_changed")
        elseif not binding then return tasks.await(context,"waiting_for_current_flightpath_menu")
        elseif not valid_token(params.explorationToken) then return tasks.await(context,"parent_exploration_token_required")
        elseif actions.interactionOwner~="exploration" then return tasks.await(context,"waiting_for_parent_exploration_custody")
        elseif frame.stage=="start" then
            if not uint(params.destinationNode) or params.destinationNode==0 or
               params.acceptUnknownFare~=nil and type(params.acceptUnknownFare)~="boolean" then return tasks.await(context,"invalid_activation_intent") end
            frame.binding,frame.destination,frame.parentToken=binding,params.destinationNode,params.explorationToken
            frame.goalFarePresent,frame.goalFare=params.acceptUnknownFare~=nil,params.acceptUnknownFare
            local key=planner.policy_key()
            if type(key)~="string" or key=="" then return tasks.await(context,"planner_tuning_unavailable") end
            local plan=context.memory.plan
            if plan and plan.status=="planned" and same(plan.source,binding) and plan.destinationNode==frame.destination and plan.policyKey==key then
                frame.planKey=plan.policyKey;frame.stage="ready"
            else context.memory.plan=nil;frame.stage="plan_wait";tasks.push(context,"flightpath_plan",{});return end
        end
    end
    if frame.stage=="plan_wait" then
        local plan=context.memory.plan
        if not plan or plan.status~="planned" or not same(plan.source,frame.binding) or plan.destinationNode~=frame.destination or
           type(plan.policyKey)~="string" or plan.policyKey=="" or plan.policyKey~=planner.policy_key() then finish(context,frame,"reference_plan_unavailable")
        else frame.planKey=plan.policyKey;frame.stage="ready" end
    end
    if frame.stage=="ready" then
        local plan=context.memory.plan;local nodes=plan and plan.nodeIds or {}
        if not plan or plan.status~="planned" or not same(plan.source,frame.binding) or plan.destinationNode~=frame.destination or
           plan.policyKey~=frame.planKey or nodes[1]~=frame.binding.sourceNode then finish(context,frame,"reference_plan_changed")
        elseif not nodes[2] then finish(context,frame,"destination_is_menu_source")
        else
            frame.hop=nodes[2]
            local accept=M.tuning.accept_unknown_fare
            if frame.goalFarePresent then accept=frame.goalFare end
            if accept~=true then status(context,frame,"unknown_fare_not_accepted");return tasks.await(context,"unknown_fare_not_accepted") end
            local copper,copper_revision=wallet(context)
            if copper==nil then return tasks.await(context,"waiting_for_known_wallet") end
            if not quiet(context) then return tasks.await(context,"waiting_for_quiet_activation_body") end
            local expected=revision(flight.activationRevision);local next_revision=expected and successor(expected)
            if not next_revision then return tasks.await(context,"activation_revision_unavailable") end
            frame.request={sessionId=frame.binding.sessionId,selfGuid=frame.binding.selfGuid,visitId=frame.binding.visitId,
                token=frame.parentToken,npcGuid=frame.binding.npcGuid,sourceNode=frame.binding.sourceNode,destinationNode=frame.hop,
                menuRevision=frame.binding.menuRevision,expectedRevision=expected,revision=next_revision,
                expectedCopperRevision=copper_revision,expectedCopper=copper,acceptUnknownFare=true}
            frame.stage="activate";status(context,frame,"activation_proposed")
        end
    end
    if frame.stage=="activate" then
        local reply,out=tasks.call(context,frame,method,request_parameters(frame.request),"activating_selected_hop")
        if out then return out end
        frame.stage="observe"
        return tasks.await(context,"waiting_for_exact_activation_receipt")
    end
    if frame.stage=="observe" then
        local op=flight.activation
        if not matches(op,frame.request) or revision(flight.activationRevision)~=op.revision then return tasks.await(context,"waiting_for_exact_activation_receipt") end
        local result=terminal(op)
        if result then finish(context,frame,result,op)
        elseif op.activationAccepted==true and op.replyKnown==true and (op.replyCode or 0)==0 and op.acceptanceScope then frame.stage="flight"
        else status(context,frame,op.phase or "activation_unsettled",op);return tasks.await(context,"activation_unsettled") end
    end
    if frame.request and frame.stage~="activate" and frame.stage~="observe" and frame.stage~="complete" then
        local op=flight.activation
        if not matches(op,frame.request) then return tasks.await(context,"waiting_for_exact_activation_receipt") end
        return progress(context,frame,flight,op)
    end
    if frame.stage=="complete" then tasks.pop(context,{status=frame.result});return end
    return tasks.await(context,"waiting_for_activation_authority")
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
        if frame and frame.waiting and row and row.id==frame.waiting.id and row.effect=="mutation" and row.outcome=="pending" then
            if row.method==method and frame.waiting.method==method and same_parameters(row.params,frame.request) and
               matches(op,frame.request) and op.activationAccepted==true and op.replyKnown==true and
               (op.replyCode or 0)==0 and op.acceptanceScope then
                frame.stage="flight"
                return tasks.resolve(context,frame,"flightpath","activation",op,"accepted_booking_still_reserved")
            end
            local e=frame.effect
            if e and row.method==methods[e.kind] and frame.waiting.method==row.method and effect_parameters(row.params,e.params) then
                local observation,path,receipt=effect_receipt(context,frame)
                if observation then
                    frame.stage=path=="transferAttempt" and "adoption_refused" or after[e.kind]
                    return tasks.resolve(context,frame,observation,path,receipt,"native_receipt_delivery_state_retained")
                end
            end
        end
        return {memory=memory,phase="blocked_on_ambiguous_call",done=false}
    end
    local stack=memory.stack or {};local frame=stack[#stack];local parent=stack[#stack-1]
    if frame and frame.kind=="flightpath_plan" and parent and parent.kind=="flightpath_activation" and
       not parent.request and intent_changed(parent,state.goal.params or {}) then
        -- Abandon only this child's pure planner frame, which reserves no
        -- gameplay mutation; the activation parent records the changed intent.
        tasks.pop({state=state,memory=memory,observations=state.observations or {}},{status="activation_intent_changed"})
    end
    return tasks.run(state,{flightpath_activation=handler,flightpath_plan=planner.handler},"flightpath_activation")
end
return M
