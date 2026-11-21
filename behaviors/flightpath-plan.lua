-- Standalone task-stack planner. Static directed base-price references plus
-- a witnessed known-node mask do not establish an offered route or live fare.
local M = {search_revision="1",tuning={equal_cost_path="lower",equal_distance_node="lower",excluded_path_ids={}}}
-- Bump search_revision when editing a function body. Declared tie/exclusion
-- edits are detected directly; no host revision or reload event is invented.
function M.allow_path(path)
    for _,id in ipairs(M.tuning.excluded_path_ids) do if path.pathId==id then return false end end
    return true
end
local method="buddy-actions.flightpath_reference"
local function uint(n) return type(n)=="number" and n==n and n>=0 and n<=4294967295 and n==math.floor(n) end
local function positive(n) return uint(n) and n>0 end
local function policy_key()
    if type(M.search_revision)~="string" or M.search_revision=="" or
       M.tuning.equal_cost_path~="lower" and M.tuning.equal_cost_path~="higher" or
       M.tuning.equal_distance_node~="lower" and M.tuning.equal_distance_node~="higher" or
       type(M.tuning.excluded_path_ids)~="table" then return end
    local key=M.search_revision.."|"..M.tuning.equal_cost_path.."|"..M.tuning.equal_distance_node.."|"
    for _,id in ipairs(M.tuning.excluded_path_ids) do
        if not positive(id) then return end
        key=key..tostring(id)..","
    end
    return key
end
local function identity(s)
    if type(s)~="string" or #s==0 or #s>20 or byte_at(s,1)==48 then return end
    for i=1,#s do local b=byte_at(s,i);if b<48 or b>57 then return end end
    return #s<20 or s<="18446744073709551615"
end
local function same(a,b)
    if type(a)~=type(b) then return false end
    if type(a)~="table" then return a==b end
    for k,v in pairs(a) do if not same(v,b[k]) then return false end end
    for k in pairs(b) do if a[k]==nil then return false end end
    return true
end
local function index(nodes,id)
    local lo,hi=1,#nodes
    while lo<=hi do
        local middle=math.floor((lo+hi)/2)
        if nodes[middle]==id then return middle end
        if nodes[middle]<id then lo=middle+1 else hi=middle-1 end
    end
end
local function menu(state)
    local observations=state.observations or {}
    local world,actions,flight=observations.world or {},observations.actions or {},observations.flightpath or {}
    local body=state.body or {};local op=flight.operation or {}
    if type(body.sessionId)~="string" or body.sessionId=="" or not identity(body.selfGuid) or
       actions.sessionId~=body.sessionId or actions.selfGuid~=body.selfGuid or (world.self or {}).guid~=body.selfGuid or
       flight.sessionId~=body.sessionId or flight.selfGuid~=body.selfGuid or flight.visitBound~=true or
       not identity(world.visitRevision) or flight.visitId~=world.visitRevision or
       not identity(op.revision) or flight.revision~=op.revision or op.sessionId~=body.sessionId or
       op.selfGuid~=body.selfGuid or op.visitId~=world.visitRevision or not identity(op.npcGuid) or
       op.mode~="discover" or op.phase~="menu_observed" or op.menuKnown~=true or
       op.pending~=nil and op.pending~=false or op.transportWritten~=true and op.transportUnknown~=true or
       not positive(op.sourceNode) or type(op.knownNodes)~="table" or #op.knownNodes==0 or #op.knownNodes>448 then return end
    local fingerprint,previous="",0
    for _,node in ipairs(op.knownNodes) do
        -- 448 is the protocol mask width, not a journey or route limit.
        if not positive(node) or node>448 or node<=previous then return end
        fingerprint=fingerprint..tostring(node)..",";previous=node
    end
    local binding={sessionId=body.sessionId,selfGuid=body.selfGuid,visitId=world.visitRevision,
                   menuRevision=op.revision,npcGuid=op.npcGuid,sourceNode=op.sourceNode,maskFingerprint=fingerprint}
    return binding,op.knownNodes
end
local function visited(frame,i)
    local word=math.floor((i-1)/32)+1;local bit=2^((i-1)%32)
    return math.floor(frame.visited[word]/bit)%2==1
end
local function visit(frame,i)
    local word=math.floor((i-1)/32)+1
    frame.visited[word]=frame.visited[word]+2^((i-1)%32)
end
local function prefer(candidate,previous,mode)
    return previous==0 or mode=="higher" and candidate>previous or mode=="lower" and candidate<previous
end
local function initialize(frame,nodes,key)
    frame.distance,frame.previous,frame.previousPath,frame.visited={},{},{},{}
    for i=1,#nodes do frame.distance[i],frame.previous[i],frame.previousPath[i]=-1,0,0 end
    for i=1,math.ceil(#nodes/32) do frame.visited[i]=0 end
    frame.distance[index(nodes,frame.binding.sourceNode)]=0
    frame.current=nil;frame.policyKey=key;frame.stage="choose"
end
local function finish(context,frame,status,reason,nodes)
    local plan={referenceOnly=true,status=status,reason=reason,destinationNode=frame.destination,
                source=frame.binding,info=frame.info,policyKey=frame.policyKey,liveFareKnown=false,liveRouteKnown=false}
    if status=="planned" then
        local reverse_nodes,reverse_paths={},{}
        local current=index(nodes,frame.destination)
        plan.referenceBaseCopper=frame.distance[current]
        while current and current>0 do
            reverse_nodes[#reverse_nodes+1]=nodes[current]
            if frame.previous[current]>0 then reverse_paths[#reverse_paths+1]=frame.previousPath[current] end
            current=frame.previous[current]
        end
        plan.nodeIds,plan.pathIds={},{}
        for i=#reverse_nodes,1,-1 do plan.nodeIds[#plan.nodeIds+1]=reverse_nodes[i] end
        for i=#reverse_paths,1,-1 do plan.pathIds[#plan.pathIds+1]=reverse_paths[i] end
    end
    -- Scratch arrays are not an adjacency cache and do not accompany the
    -- completed route. Discard them before the host serializes the result.
    frame.distance,frame.previous,frame.previousPath,frame.visited=nil,nil,nil,nil
    if status=="planned" then plan.knownNodes=nodes end
    context.memory.plan=plan
    tasks.pop(context,{status=status})
end
local function handler(context,frame)
    local binding,nodes=menu(context.state)
    if not binding then
        if frame.binding then finish(context,frame,"aborted","menu_binding_unavailable");return end
        return tasks.await(context,"waiting_for_current_flightpath_menu")
    end
    local destination=(context.state.goal.params or {}).destinationNode
    if frame.binding and (not same(binding,frame.binding) or destination~=frame.destination) then
        finish(context,frame,"aborted","menu_or_destination_changed");return
    end
    local key=policy_key()
    if not key then return tasks.await(context,"flightpath_tuning_unavailable") end
    if frame.stage=="start" then
        if not positive(destination) or not index(nodes,destination) then return tasks.await(context,"destination_not_in_known_mask") end
        local source=index(nodes,binding.sourceNode)
        if not source then return tasks.await(context,"source_not_in_known_mask") end
        frame.binding,frame.destination=binding,destination
        initialize(frame,nodes,key)
    elseif frame.policyKey~=key then
        -- An issued read belongs to the old search until its exact result or
        -- descriptor-proven read error is consumed. Never drop or replay it.
        if frame.waiting then
            local reply,out=tasks.call(context,frame,method,{nodeId=nodes[frame.current]})
            if out then return out end
        end
        initialize(frame,nodes,key)
        return tasks.await(context,"flightpath_policy_changed_restarting")
    end
    if frame.stage=="choose" then
        local chosen,best=0,-1
        for i,node in ipairs(nodes) do
            local cost=frame.distance[i]
            if not visited(frame,i) and cost>=0 and (best<0 or cost<best or cost==best and
               prefer(node,chosen>0 and nodes[chosen] or 0,M.tuning.equal_distance_node)) then chosen,best=i,cost end
        end
        if chosen==0 then finish(context,frame,"unreachable","no_reference_path_in_known_mask");return end
        frame.current=chosen;frame.stage="reference"
    end
    local reply,out=tasks.call(context,frame,method,{nodeId=nodes[frame.current]},"reading_flightpath_reference")
    if out then return out end
    if reply.readError then return tasks.await(context,"flightpath_reference_read_unavailable") end
    if reply.referenceOnly~=true or reply.liveFareKnown==true or reply.liveRouteKnown==true or
       reply.nodeFound~=true or type(reply.node)~="table" or reply.node.nodeId~=nodes[frame.current] or
       type(reply.info)~="table" or not positive(reply.info.claimedBuild) or type(reply.info.nameLocale)~="string" or
       reply.info.nameLocale=="" then
        finish(context,frame,"aborted","reference_facts_unavailable");return
    end
    if frame.info and not same(frame.info,reply.info) then finish(context,frame,"aborted","reference_source_changed");return end
    frame.info=reply.info
    visit(frame,frame.current)
    if nodes[frame.current]==frame.destination then finish(context,frame,"planned",nil,nodes);return end
    for _,path in ipairs(reply.outgoingPaths or {}) do
        local target=positive(path.destinationNode) and index(nodes,path.destinationNode)
        local copper=path.referenceBaseCopper or 0
        if target and not visited(frame,target) and path.endpointsKnown==true and path.referencePriceKnown==true and
           positive(path.pathId) and path.sourceNode==nodes[frame.current] and uint(copper) and M.allow_path(path)==true then
            local candidate=frame.distance[frame.current]+copper
            if frame.distance[target]<0 or candidate<frame.distance[target] or candidate==frame.distance[target] and
               prefer(path.pathId,frame.previousPath[target],M.tuning.equal_cost_path) then
                frame.distance[target],frame.previous[target],frame.previousPath[target]=candidate,frame.current,path.pathId
            end
        end
    end
    frame.stage="choose"
    return tasks.await(context,"reference_node_expanded")
end
function M.decide(state)
    local memory=state.memory or {}
    if #(state.pending or {})>0 then return {memory=memory,phase="blocked_on_ambiguous_call",done=false} end
    if memory.plan and memory.plan.source then
        local binding=menu(state)
        local destination=(state.goal.params or {}).destinationNode
        if not same(binding,memory.plan.source) or destination~=memory.plan.destinationNode then
            memory.plan={referenceOnly=true,status="aborted",reason="menu_or_destination_changed",
                         source=memory.plan.source,destinationNode=memory.plan.destinationNode,policyKey=memory.plan.policyKey,
                         liveFareKnown=false,liveRouteKnown=false}
        end
    end
    return tasks.run(state,{flightpath_plan=handler},"flightpath_plan")
end
return M
