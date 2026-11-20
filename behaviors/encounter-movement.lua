-- Spatial policy for a caller-selected action. This module never chooses a
-- combat target or interrupts autoswing. The encounter queue remains the one
-- authority; positioning advances alongside its active attack.
local M = {}
local function number(value)
    return type(value) == "number" and value == value and value > -math.huge and value < math.huge
end
local function pose(unit)
    if type(unit) ~= "table" or unit.present ~= true or unit.positionKnown ~= true or
       not number(unit.x) or not number(unit.y) or not number(unit.z) then return end
    local x,y,z,o = unit.x,unit.y,unit.z,unit.orientation or 0
    local t = unit.trajectory
    if t and t.known == true and t.stopped ~= true then
        if t.cyclic == true or t.transportRelative == true or t.destinationKnown ~= true or
           not number(t.durationMs) or t.durationMs <= 0 or not number(t.ageMs) or t.ageMs < 0 or
           not number(t.destinationX) or not number(t.destinationY) or not number(t.destinationZ) then return end
        local fraction = math.min(1,t.ageMs/t.durationMs)
        x,y,z = x+(t.destinationX-x)*fraction,y+(t.destinationY-y)*fraction,z+(t.destinationZ-z)*fraction
    end
    return {x=x,y=y,z=z,o=number(o) and o or 0}
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
    local validated = view.targetGuid == target and view.visitId == obs.visitId and view.ready == true and
        view.rangeYards == radius and (view.requireLos == true) == (options.requireLos == true) and
        (view.behind == true) == (options.behind == true)
    if validated and (geometric or not unit) and (not self or self.moving ~= true) then
        return {ready=true,phase="in_position"}
    end
    -- If already within range, request the same mechanic once to stop/face
    -- and check LoS. A failed cast is never repaired by merely complaining
    -- about distance; the next observation continues the approach.
    return {ready=false,phase="positioning",action=action(obs,sequence,target,options,false)}
end
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
