-- Spatial policy for a caller-selected action. This module never chooses a
-- combat target or interrupts autoswing. The encounter queue remains the one
-- authority; positioning advances alongside its active attack.
local M = {}
M.recovery_tuning = {stalled_observations=3, progress_yards=.3, step_ms=250,
    probes={{0,false},{1.5707963268,false},{0,true},{-1.5707963268,false},
            {3.1415926536,false},{0,true},{1.5707963268,true},{0,false},
            {-1.5707963268,true},{3.1415926536,true},{0,true}}}
local recovery
-- Only the last native positioning result is cached. A rejected cast revokes
-- that spatial evidence until an approach rechecks it; it never changes the
-- encounter target queue or invents a successful cast.
local invalid_target, checked_target
function M.on_event(event)
    local receipt = event.receipt
    if not receipt then return true end
    local payload = receipt.payload or {}
    local capability, target = payload.capability, payload.target_guid
    if capability == "move_body" and recovery and payload.sequence_id == recovery.probeSequence then
        if payload.phase == "body_settled_written" then
            -- Falling consumed the input; retry this caller-selected probe
            -- after checking the normal route from the supported position.
            recovery.index = math.max(1,recovery.index-1)
        end
        recovery.checkRoute = true
    end
    if (receipt.outcome == "failed" or receipt.outcome == "not_dispatched") and
       (capability == "offense" or capability == "polymorph" or capability == "sap" or capability == "heal" or capability == "cleanse") and
       type(target) == "string" then
        invalid_target, checked_target = target, nil
    elseif capability == "approach_target" and receipt.outcome == "settled_unverified" and target == invalid_target then
        checked_target = target
    end
    return true
end
local function number(value)
    return type(value) == "number" and value == value and value > -math.huge and value < math.huge
end
local function pose(unit)
    if type(unit) ~= "table" or unit.present ~= true or unit.positionKnown ~= true then return end
    local x,y,z,o = unit.x or 0,unit.y or 0,unit.z or 0,unit.orientation or 0
    if not number(x) or not number(y) or not number(z) then return end
    local t = unit.trajectory
    if t and t.known == true and t.stopped ~= true then
        -- Protobuf JSON encodes uint64 ages as decimal strings. Convert this
        -- elapsed-time scalar; keep GUIDs as exact strings throughout policy.
        local age = tonumber(t.ageMs)
        if t.cyclic == true or t.transportRelative == true or t.destinationKnown ~= true or
           not number(t.durationMs) or t.durationMs <= 0 or not number(age) or age < 0 or
           not number(t.destinationX) or not number(t.destinationY) or not number(t.destinationZ) then return end
        local fraction = math.min(1,age/t.durationMs)
        x,y,z = x+(t.destinationX-x)*fraction,y+(t.destinationY-y)*fraction,z+(t.destinationZ-z)*fraction
    end
    return {x=x,y=y,z=z,o=number(o) and o or 0}
end

local rejected_route = {follow_navigation_unavailable=true,follow_navigation_invalid_step=true,
    follow_navigation_invalid_target=true}
local function recovery_probe(obs,sequence,key,phase,goal)
    local nav = obs.navigation or {}
    if nav.bodyMovementSupported ~= true or nav.motionUnconfirmed == true then return end
    local self = (obs.players or {})[obs.selfGuid]
    local from = pose(self)
    if not from or not number(goal.x) or not number(goal.y) or not number(goal.z) then return end
    local identity = obs.sessionId .. ":" .. obs.visitId .. ":" .. key
    if not recovery or recovery.identity ~= identity or recovery.serverRevision ~= self.positionRevision then
        recovery = {identity=identity,serverRevision=self.positionRevision,index=1,stalled=0}
    end
    if recovery.checkRoute then recovery.checkRoute=false; return end
    if not rejected_route[phase] then
        if phase == "within_follow_formation" or
           phase == "within_personal_follow_space" or phase == "in_position" then recovery=nil end
        return
    end
    if recovery.sequence ~= sequence then
        local before = recovery.last
        local threshold = M.recovery_tuning.progress_yards
        local progressed = before and ((before.x-from.x)^2+(before.y-from.y)^2+(before.z-from.z)^2) > threshold*threshold
        if progressed then recovery.stalled=0
        else recovery.stalled=recovery.stalled+1 end
        recovery.last,recovery.sequence = from,sequence
    end
    if recovery.stalled < M.recovery_tuning.stalled_observations or self.moving == true then return end
    -- Choose a bearing towards the current goal in Lua. Quantized headings
    -- avoid converting GUIDs or requiring new runtime math/target selectors.
    local dx,dy = goal.x-from.x,goal.y-from.y
    local toward,best = 0,nil
    for i=-4,3 do
        local offset = i*.7853981634
        local score = dx*math.cos(from.o+offset)+dy*math.sin(from.o+offset)
        if not best or score > best then toward,best=offset,score end
    end
    local probes = M.recovery_tuning.probes
    local probe = probes[(recovery.index-1)%#probes+1]
    local bearing = toward+probe[1]
    while bearing > 3.1415926536 do bearing=bearing-6.2831853072 end
    while bearing < -3.1415926536 do bearing=bearing+6.2831853072 end
    recovery.index,recovery.probeSequence = recovery.index+1,sequence
    return {capability="move_body",sessionId=obs.sessionId,selfGuid=obs.selfGuid,visitId=obs.visitId,
        observationSequence=sequence,sequenceId=sequence,bearingRadians=bearing,
        durationMs=probe[2] and 0 or M.recovery_tuning.step_ms,jump=probe[2]}
end

function M.idle_recovery(obs,sequence)
    local nav = obs.navigation or {}
    local self = (obs.players or {})[obs.selfGuid]
    if nav.following ~= true or nav.goalKnown ~= true or not self then return {} end
    local action = recovery_probe(obs,sequence,"follow",nav.phase,{x=nav.goalX or 0,y=nav.goalY or 0,z=nav.goalZ or 0})
    return {action=action}
end
local function action(obs,sequence,target,options,stop)
    return {capability=stop and "stop_movement" or "approach_target",sessionId=obs.sessionId,
        selfGuid=obs.selfGuid,targetGuid=target,visitId=obs.visitId,observationSequence=sequence,
        rangeYards=options.rangeYards,requireLos=options.requireLos == true,behind=options.behind == true}
end
function M.decide(obs,sequence,target,options)
    options = options or {}
    local radius = options.rangeYards
    if type(obs) ~= "table" or type(target) ~= "string" or not number(radius) or radius <= 0 then
        return {ready=false,phase="movement_intent_invalid"}
    end
    local self = (obs.players or {})[obs.selfGuid]
    local unit = options.target or (obs.targets or {})[target] or (obs.players or {})[target]
    local from,to = pose(self),pose(unit)
    local geometric = false
    if from and to and (unit.mapKnown ~= true or unit.mapId == obs.mapId) then
        local dx,dy,dz=from.x-to.x,from.y-to.y,from.z-to.z
        geometric = dx*dx+dy*dy+dz*dz <= radius*radius
        if options.behind then geometric = geometric and dx*math.cos(to.o)+dy*math.sin(to.o) < 0 end
    end
    local view = obs.targetMovement or {}
    local validated = (invalid_target ~= target or checked_target == target) and view.targetGuid == target and view.visitId == obs.visitId and view.ready == true and
        view.rangeYards == radius and (view.requireLos == true) == (options.requireLos == true) and
        (view.behind == true) == (options.behind == true)
    if validated and (geometric or not unit) and (not self or self.moving ~= true) then
        recovery=nil
        if invalid_target == target then invalid_target, checked_target = nil, nil end
        return {ready=true,phase="in_position"}
    end
    if to and view.targetGuid == target and view.visitId == obs.visitId then
        local probe = recovery_probe(obs,sequence,target,view.phase,to)
        if probe then return {ready=false,phase="probing_route_recovery",action=probe} end
    end
    -- If already within range, request the same mechanic once to stop/face
    -- and check LoS. A failed cast is never repaired by merely complaining
    -- about distance; the next observation continues the approach.
    return {ready=false,phase="positioning",action=action(obs,sequence,target,options,false)}
end
function M.needs_position(target) return invalid_target == target end
function M.for_spell(obs,sequence,target,spell,options)
    options = options or {}
    local range = spell and spell.maxRange
    if not number(range) or range <= 0 then return {ready=false,phase="spell_range_unknown"} end
    return M.decide(obs,sequence,target,{rangeYards=range,requireLos=true,behind=options.behind == true,target=options.target})
end
function M.stop(obs,sequence,target)
    return action(obs,sequence,target,{rangeYards=0},true)
end
return M
