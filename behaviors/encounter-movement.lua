-- Spatial policy for a caller-selected action. This module never chooses a
-- combat target or interrupts autoswing. The encounter queue remains the one
-- authority; positioning advances alongside its active attack.
local M = {}
-- Ordinary companion role policy. Temporary waits use encounter tasks and
-- movement ownership, not the legacy Follow toggle. Every goal remains finite.
M.follow_owner_by_default = true
local formation_memory, formation_identity
local function follow_requested(nav)
    return nav.following == true or nav.following == false and M.follow_owner_by_default == true
end
local function portal_revision(value)
    if type(value)~="string" or #value==0 or #value>20 or byte_at(value,1)==48 or
       #value==20 and value>"18446744073709551615" then return false end
    for i=1,#value do
        local digit=byte_at(value,i)
        if digit<48 or digit>57 then return false end
    end
    return true
end
local function portal_uint32(value)
    return type(value)=="number" and value>=0 and value<=4294967295 and value%1==0
end
local function portal_binding_revision(value) return value=="0" or portal_revision(value) end
local function portal_age(value)
    if type(value)=="number" or type(value)=="string" then return tonumber(value) end
end
function M.portal_catchup(frame, obs, sequence)
    local nav=obs.navigation or {}
    local portal,self=nav.portal,(obs.players or {})[obs.selfGuid]
    local following=follow_requested(nav)
    if not encounter_formation or not following or nav.groundNavigationConfigured~=true or
       type(portal)~="table" or type(portal.catchingUp)~="boolean" or portal.notified~=false or portal.ownerAbsent~=true or
       not portal_revision(portal.witnessRevision) or not portal_binding_revision(portal.followRevision) or
       not portal_revision(portal.ownerGuid) or not portal_binding_revision(portal.sourceVisitId) or
       not portal_uint32(portal.triggerId) or portal.triggerId==0 or
       not portal_uint32(portal.sourceMapId) or not portal_uint32(portal.destinationMapId) or
       portal.sourceVisitId~=obs.visitId or
       portal.followRevision~=nav.followRevision or portal.sourceMapId~=obs.mapId or
       obs.sessionId~=frame.session_id or not portal_revision(obs.selfGuid) or not portal_revision(sequence) or
       type(self)~="table" or self.present~=true or self.aliveKnown~=true or self.alive~=true or
       self.positionKnown~=true or self.mapKnown~=true or self.mapId~=obs.mapId then return end
    local start,ground=portal.start,portal.ground
    if type(start)~="table" or start.known~=true or start.mapKnown~=true or start.mapId~=obs.mapId or
       not portal_revision(start.revision) or type(ground)~="table" or type(frame.clock)~="table" then return end
    local input={mode="portal_catch_up",nowMs=frame.clock.monotonicMs,
        selfGuid=obs.selfGuid,classId=self.classKnown and self.classId or 0,
        self={x=start.x or 0,y=start.y or 0,z=start.z or 0,moving=self.moving==true},
        owner={x=ground.x or 0,y=ground.y or 0,z=ground.z or 0,o=start.orientation or 0,moving=false,
            playerFlags=(nav.followOwner or {}).playerFlags}}
    -- Older clients omit both knownness fields and retain their native timing
    -- gate. Once either fact is advertised, unknown timing blocks the proposal.
    if portal.sightingAgeKnown~=nil or portal.disappearanceAgeKnown~=nil then
        input.mode="portal_follow"
        input.portal={sightingAgeKnown=portal.sightingAgeKnown,sightingAgeMs=portal_age(portal.sightingAgeMs),
            disappearanceAgeKnown=portal.disappearanceAgeKnown,disappearanceAgeMs=portal_age(portal.disappearanceAgeMs),
            catchingUp=portal.catchingUp,ownerAbsent=portal.ownerAbsent,following=following,
            sourceVisitId=portal.sourceVisitId,currentVisitId=obs.visitId,
            followRevision=portal.followRevision,currentFollowRevision=nav.followRevision}
        local timing=encounter_formation.decide(input)
        if timing.ready~=true then return end
        input.mode="portal_catch_up"
        input.portal=nil
    end
    local planned=encounter_formation.decide(input)
    if type(planned.candidates)~="table" or #planned.candidates==0 then return end
    return {sessionId=obs.sessionId,selfGuid=obs.selfGuid,visitId=obs.visitId,observationSequence=sequence,
        witnessRevision=portal.witnessRevision,followRevision=portal.followRevision,ownerGuid=portal.ownerGuid,
        start=start,candidates=planned.candidates}
end
function M.follow_formation(frame, obs, sequence)
    local nav = obs.navigation or {}
    local owner, self = nav.followOwner, (obs.players or {})[obs.selfGuid]
    local following = follow_requested(nav)
    if not following then formation_memory, formation_identity = nil, nil end
    if not encounter_formation or nav.followFormationSupported ~= true or not following or
       not owner or not self or owner.present ~= true or owner.aliveKnown ~= true or owner.alive ~= true or
       owner.positionKnown ~= true or owner.mapKnown ~= true or owner.mapId ~= obs.mapId or
       owner.movementKnown ~= true or self.aliveKnown ~= true or self.alive ~= true or
       self.positionKnown ~= true or self.movementKnown ~= true then return end
    local identity = obs.sessionId .. ":" .. obs.selfGuid .. ":" .. obs.visitId .. ":" .. tostring(nav.followRevision)
    if identity ~= formation_identity then formation_memory, formation_identity = nil, identity end
    local planned = encounter_formation.decide({
        mode=nav.groundNavigationConfigured == false and "near_owner" or "formation",
        nowMs=frame.clock.monotonicMs, selfGuid=obs.selfGuid, classId=self.classKnown and self.classId or 0,
        owner={x=owner.x or 0,y=owner.y or 0,z=owner.z or 0,o=owner.orientation or 0,moving=owner.moving == true,playerFlags=owner.playerFlags},
        self={x=self.x or 0,y=self.y or 0,z=self.z or 0,moving=self.moving == true},
        ownerArrival=nav.ownerArrival == true, memory=formation_memory,
    })
    formation_memory = planned.memory
    local target = planned.position
    if not target then return end
    return {sessionId=obs.sessionId,selfGuid=obs.selfGuid,visitId=obs.visitId,observationSequence=sequence,
        ownerGuid=owner.guid,ownerPositionRevision=owner.positionRevision,followRevision=nav.followRevision,
        x=target.x,y=target.y,z=target.z,orientation=target.o,
        startDistance=planned.startDistance,stopDistance=planned.stopDistance,
        ready=planned.ready == true,hold=planned.hold == true,phase=planned.phase}
end
local water_probe
function M.export_state()
    return {memory=formation_memory,identity=formation_identity,waterProbe=water_probe}
end
function M.import_state(saved)
    if type(saved) ~= "table" then return false end
    formation_memory, formation_identity, water_probe = saved.memory, saved.identity, saved.waterProbe
    return true
end

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
    if capability == "move_body" and water_probe and payload.sequence_id == water_probe.sequence then
        water_probe.refused = receipt.outcome == "not_dispatched" and payload.phase == "local_physics_refused"
        water_probe.uncertain = payload.reconciliationRequired == true or receipt.outcome == "unknown"
    end
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

M.current_pose=pose

-- Generic local adjustment only. The agent owns point production/deduplication
-- in cloud_points; these are current-scope circles, not a native cloud sensor.
-- No route/lease/controller is created. An ordinary bounded body input uses
-- the same writer/physics checks and leaves the normal follow intent enabled.
function M.adjust_cloud_points(frame,obs,sequence,out,points,passivePrior,ownHold)
    if type(points)~="table" or #frame.pending~=0 then return false end
    local self=(obs.players or {})[obs.selfGuid]
    local nav=obs.navigation or {}
    local owns=type(ownHold)=="table" and type(ownHold.token)=="string" and #ownHold.token>0 and
        ownHold.sessionId==obs.sessionId and ownHold.selfGuid==obs.selfGuid and ownHold.visitId==obs.visitId and ownHold.mapId==obs.mapId and
        not ownHold.operation and not ownHold.uncertain and not ownHold.released and type(nav.movementOwner)=="table" and
        nav.movementOwner.active==true and nav.movementOwner.owner==ownHold.owner
    if nav.bodyMovementSupported~=true or nav.motionUnconfirmed==true or
       type(nav.movementOwner)~="table" or nav.movementOwner.active==true and not owns or
       not self or self.present~=true or self.aliveKnown~=true or self.alive~=true or
       self.positionKnown~=true or self.positionInferred==true or self.mapKnown~=true or self.mapId~=obs.mapId or
       self.movementKnown~=true or not number(self.x) or not number(self.y) or not number(self.z) or
       not number(self.orientation or 0) then return false end
    local cap=out.action and out.action.capability
    if cap and cap~="approach_target" and cap~="offense" and cap~="melee_start" then return false end
    local now=frame.clock and frame.clock.unixMs
    if not number(now) then return false end
    local target=out.followFormation or out.action and (obs.targets or {})[out.action.targetGuid]
    local ux,uy=math.cos(self.orientation or 0),math.sin(self.orientation or 0)
    if target and number(target.x) and number(target.y) then
        local dx,dy=target.x-self.x,target.y-self.y
        local d=math.sqrt(dx*dx+dy*dy)
        if d>0 then ux,uy=dx/d,dy/d end
    end
    local length=2.8 -- entire200ms input at the existing native maximum14yd/s
    local rx,ry,nearest,nx,ny,contributor=0,0,nil,nil,nil,nil
    local hit=false
    -- One linear pass, no all-or-nothing point-count cutoff. Cheap local
    -- filtering keeps dense20s emitter output useful, even a late underfoot
    -- point. The ordinary Lua VM's existing execution/memory budgets apply.
    for _,p in ipairs(points) do
        -- Scope and emission/deduplication belong to the agent's producer.
        -- Expiry is absolute Unixms: reading a point never extends it.
        local expires=type(p)=="table" and (p.expiresAtUnixMs or p.expiresAtMs)
        if type(p)=="table" and type(p.x)=="number" and math.abs(p.x-self.x)<7.8 and
           type(p.y)=="number" and math.abs(p.y-self.y)<7.8 and type(p.z)=="number" and math.abs(p.z-self.z)<5 and
           p.radiusYards==5 and number(expires) and expires>now and expires<=now+20000 then
            local dx,dy=self.x-p.x,self.y-p.y
            local projection=math.max(0,math.min(length,-dx*ux-dy*uy))
            local cx,cy=dx+projection*ux,dy+projection*uy
            local dz=self.z-p.z
            local d2=dx*dx+dy*dy
            if d2+dz*dz<25 or cx*cx+cy*cy+dz*dz<25 then
                hit=true
                local d=math.sqrt(d2)
                local vx,vy=dx,dy
                if d2>=25 then
                    vx,vy=-uy,ux
                    if vx*dx+vy*dy<0 then vx,vy=-vx,-vy end
                    d=1
                end
                if d<.000001 then vx,vy=-uy,ux;d=1 end
                local weight=1/math.max(d,.25)
                rx,ry=rx+vx/d*weight,ry+vy/d*weight
                if not nearest or d2+dz*dz<nearest then
                    nearest,nx,ny=d2+dz*dz,vx/d,vy/d
                    contributor={x=p.x,y=p.y,z=p.z,radiusYards=5,expiresAtMs=expires}
                end
            end
        end
    end
    if not hit then return false end
    -- Inside overlapping circles a useful exit may initially approach one
    -- redundant point. Do not veto every improving escape per historical point.
    if rx*rx+ry*ry<.000001 then rx,ry=nx,ny end
    local best,dot
    for i=0,15 do
        local angle=i*.392699081698724
        local value=rx*math.cos(angle)+ry*math.sin(angle)
        if not dot or value>dot then best,dot=angle,value end
    end
    local bearing=best-(self.orientation or 0)
    while bearing>3.141592653589793 do bearing=bearing-6.283185307179586 end
    while bearing< -3.141592653589793 do bearing=bearing+6.283185307179586 end
    -- A passive prior is not a blanket veto, but its existing creature
    -- clearance remains a constraint on this one local input.
    if passivePrior then
        local vx,vy=math.cos(best),math.sin(best)
        local radius=passivePrior.minRangeYards+passivePrior.clearanceYards
        for _,n in pairs(obs.targets or {}) do
            if n.entry==passivePrior.creatureEntry and n.present==true and n.alive~=false then
                if n.positionKnown~=true or n.positionInferred==true or n.positionEstimated==true or n.mapKnown~=true or n.mapId~=obs.mapId or
                   not number(n.x) or not number(n.y) or not number(n.z) then return false end
                local dx,dy=self.x-n.x,self.y-n.y
                local q=math.max(0,math.min(length,-dx*vx-dy*vy))
                if (dx+q*vx)^2+(dy+q*vy)^2+(self.z-n.z)^2<radius*radius then
                    out.cloudAdjustment={phase="existing_prior_clearance",effectVerified=false};return false
                end
            end
        end
    end
    out.action={capability="move_body",sessionId=obs.sessionId,selfGuid=obs.selfGuid,visitId=obs.visitId,
        observationSequence=sequence,sequenceId=sequence,bearingRadians=bearing,durationMs=200,jump=false}
    out.followFormation=nil;out.portalCatchup=nil
    out.cloudAdjustment={phase="local_point_repulsion",effectVerified=false,referencePoseEstimated=self.positionEstimated==true,
        point=contributor,distanceYards=math.sqrt(nearest),clearanceYards=math.sqrt(nearest)-5,existingRoleWriter=owns==true}
    return true
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
    if nav.following ~= true or not self then return {} end
    local epoch=obs.sessionId..":"..obs.selfGuid..":"..obs.visitId..":"..(nav.followRevision or "0")..":"..(self.positionRevision or "0")
    if water_probe and water_probe.epoch==epoch and water_probe.uncertain then return {} end
    if nav.goalKnown~=true then return {} end
    local action = recovery_probe(obs,sequence,"follow",nav.phase,{x=nav.goalX or 0,y=nav.goalY or 0,z=nav.goalZ or 0})
    return {action=action}
end
local function action(obs,sequence,target,options,stop)
    return {capability=stop and "stop_movement" or "approach_target",sessionId=obs.sessionId,
        selfGuid=obs.selfGuid,targetGuid=target,visitId=obs.visitId,observationSequence=sequence,
        rangeYards=options.rangeYards,minRangeYards=options.minRangeYards,clearanceTargetGuids=options.clearanceTargetGuids,clearanceYards=options.clearanceYards,requireLos=options.requireLos == true,behind=options.behind == true}
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
        geometric = dx*dx+dy*dy+dz*dz <= radius*radius and dx*dx+dy*dy+dz*dz >= (options.minRangeYards or 0)^2
        if options.behind then geometric = geometric and dx*math.cos(to.o)+dy*math.sin(to.o) < 0 end
        if options.requireFacing then geometric = geometric and (-dx)*math.cos(from.o)+(-dy)*math.sin(from.o)>=0 end
    end
    local view = obs.targetMovement or {}
    local validated = (invalid_target ~= target or checked_target == target) and view.targetGuid == target and view.visitId == obs.visitId and view.ready == true and
        math.abs((view.rangeYards or 0)-radius)<=.00001*math.max(1,radius) and math.abs((view.minRangeYards or 0)-(options.minRangeYards or 0))<=.00001*math.max(1,radius) and
        math.abs((view.clearanceYards or 0)-(options.clearanceYards or 0))<=.00001*math.max(1,radius) and (view.requireLos == true) == (options.requireLos == true) and
        (view.behind == true) == (options.behind == true)
    local expected,actual=options.clearanceTargetGuids or {},view.clearanceTargetGuids or {}
    if #expected~=#actual then validated=false else for i,id in ipairs(expected) do if actual[i]~=id then validated=false end end end
    -- A ready receipt binds the earlier geometry. A living carrier can move
    -- without changing its GUID or the friendly cast target's position.
    if validated and #expected>0 then
        for _,id in ipairs(expected) do
            local carrier=(obs.targets or {})[id] or (obs.players or {})[id]
            local at=pose(carrier)
            if not from or not at or carrier.mapKnown~=true or carrier.mapId~=obs.mapId or
               carrier.positionInferred==true or carrier.positionEstimated==true or
               (from.x-at.x)^2+(from.y-at.y)^2+(from.z-at.z)^2<(options.clearanceYards or 0)^2 then
                validated=false;break
            end
        end
    end
    if validated and (geometric or not unit) and (not self or self.moving ~= true) then
        recovery=nil
        if invalid_target == target then invalid_target, checked_target = nil, nil end
        return {ready=true,phase="in_position"}
    end
    if not options.minRangeYards and not options.clearanceTargetGuids and to and view.targetGuid == target and view.visitId == obs.visitId then
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
    local copied={}
    for key,value in pairs(options) do copied[key]=value end
    copied.rangeYards=math.min(options.rangeYards or range,range);copied.requireLos=true
    return M.decide(obs,sequence,target,copied)
end

-- Named Practice uses its verified native route rather than the ordinary
-- encounter actuator. This is the same spatial module's finite approach plan.
M.practice_tuning = {melee_yards=3.5, caster_yards=25}
function M.practice_approach(input)
    local wait={kind="wait",standYards=0}
    if type(input)~="table" or type(input.observation)~="table" or type(input.observation.players)~="table" then return wait end
    local obs,target,route=input.observation,input.target,input.route
    local self=(obs.players or {})[obs.selfGuid]
    local tuning=M.practice_tuning
    if type(self)~="table" or self.classKnown~=true or
       (self.classId~=4 and self.classId~=5 and self.classId~=8) or
       type(tuning)~="table" or type(route)~="table" or type(route.known)~="boolean" then return wait end
    local radius=self.classId==4 and tuning.melee_yards or tuning.caster_yards
    if not number(radius) or radius<=0 or radius>240 then return wait end
    local from,to=pose(self),pose(target)
    if not from or not to or self.mapKnown~=true or target.mapKnown~=true or
       self.mapId~=obs.mapId or target.mapId~=obs.mapId then return wait end
    local dx,dy,dz=from.x-to.x,from.y-to.y,from.z-to.z
    local approach=dx*dx+dy*dy+dz*dz>radius*radius
    if route.known then
        if not number(route.remainingYards) or route.remainingYards<0 or type(route.direct)~="boolean" then return wait end
        approach=approach or route.remainingYards>radius or not route.direct
    end
    return {kind=approach and "approach" or "arrived",standYards=radius}
end
function M.stop(obs,sequence,target)
    return action(obs,sequence,target,{rangeYards=0},true)
end
return M
