-- Data adapter and ordinary safe-yield priority. Actors own the provider's
-- effect rules/history; native code owns collision/ground and actual packets.
local M={}
local function number(v) return type(v)=="number" and v==v and v~=math.huge and v~=-math.huge end
local function unix_ms(v)
    if type(v)=="string" then
        if #v==0 or #v>16 or #v>1 and byte_at(v,1)==48 then return nil end
        for i=1,#v do local c=byte_at(v,i);if c<48 or c>57 then return nil end end
        v=tonumber(v)
    end
    if number(v) and v>=0 and v<=9007199254740991 and v==math.floor(v) then return v end
end
-- Reference rule is actor-owned Lua. These are visible ground objects, not
-- inferred slime centers or a claim that an unseen area is globally absent.
M.reference_spells={[21070]=true}
M.reference_duration_ms=20000
function M.ground_sightings(obs)
    if obs.groundEffectsSupported~=true or obs.groundEffectsOverflow==true then return nil end
    local rows={}
    for _,effect in ipairs(obs.groundEffects or {}) do
        if type(effect)~="table" then return nil end
        if M.reference_spells[effect.spellId] then
            if type(effect.guid)~="string" or #effect.guid==0 or not number(effect.x) or not number(effect.y) or not number(effect.z) or
               not number(effect.radiusYards) or effect.radiusYards<=0 or not unix_ms(effect.firstObservedAtUnixMs) then return nil end
            rows[#rows+1]={id=effect.guid..":"..obs.visitId,sourceGuid=effect.casterGuid,casterGuid=effect.casterGuid,groundEffectGuid=effect.guid,spellId=effect.spellId,
                x=effect.x,y=effect.y,z=effect.z,radiusYards=effect.radiusYards,observedAtMs=unix_ms(effect.firstObservedAtUnixMs),
                positionRevision=effect.positionRevision,serverCastTimeMs=effect.serverCastTimeMs,
                estimated=false,kind="observed_ground_effect",castObserved=false}
        end
    end
    return rows
end
function M.bind_visible(points,sightings)
    if type(points)~="table" then return nil end
    local bound={}
    for _,p in ipairs(points) do
        if type(p)~="table" then return nil end
        local match
        for _,witness in ipairs(sightings) do
            if p.x==witness.x and p.y==witness.y and p.z==witness.z and number(p.radiusYards) and p.radiusYards>0 and
               (not match or witness.observedAtMs>match.observedAtMs) then match=witness end
        end
        if match then
            local expires=p.expiresAtUnixMs or p.expiresAtMs
            if number(expires) then bound[#bound+1]={x=p.x,y=p.y,z=p.z,radiusYards=match.radiusYards,groundEffectGuid=match.groundEffectGuid,
                expiresAtUnixMs=math.min(expires,match.observedAtMs+M.reference_duration_ms)} end
        end
    end
    return bound
end
function M.project(frame,obs,points)
    if type(points)~="table" or not number(frame.clock.unixMs) then return nil end
    local active={}
    for _,p in ipairs(points) do
        if type(p)~="table" then return nil end
        local expires=p.expiresAtUnixMs or p.expiresAtMs
        if not (number(p.x) and number(p.y) and number(p.z) and number(p.radiusYards) and p.radiusYards>0 and number(expires)) then
            return nil
        end
        if expires>frame.clock.unixMs then active[#active+1]={x=p.x,y=p.y,z=p.z,radiusYards=p.radiusYards,expiresAtUnixMs=expires,groundEffectGuid=p.groundEffectGuid} end
    end
    return {sessionId=obs.sessionId,selfGuid=obs.selfGuid,mapId=obs.mapId,visitId=obs.visitId,points=active}
end
local function reference_pose(obs,own,nav)
    if own.positionEstimated~=true then return "observed_self" end
    -- Actions publishes this flag only for its own current-visit native pose,
    -- distinct from inferred external coordinates. Step rechecks ground/body.
    if own.guid~=obs.selfGuid or own.movementKnown~=true or own.moving~=false or nav.motionUnconfirmed==true then return nil end
    local body=nav.bodyMovement
    if type(body)=="table" and body.visitId==obs.visitId and type(body.sequenceId)=="string" and #body.sequenceId>0 and body.sequenceId~="0" and
       body.transportWritten==true and body.positionEstimated==true and (body.phase=="body_input_written" or body.phase=="body_settled_written") and
       number(body.x) and number(body.y) and number(body.z) and body.x==own.x and body.y==own.y and body.z==own.z then return "correlated_native_estimate" end
    return "native_self_estimate"
end
function M.prioritize(frame,obs,sequence,out,input,explicitSpell,ownMovement,taskHeld)
    if type(input)~="table" or type(out.encounterOwnership)~="table" then return end
    local nav=obs.navigation or {}
    local own=(obs.players or {})[obs.selfGuid]
    local pose=own and reference_pose(obs,own,nav)
    if nav.movementPointsSupported~=true or not own or own.present~=true or own.aliveKnown~=true or own.alive~=true or
       own.positionKnown~=true or own.positionInferred==true or not pose or own.mapKnown~=true or own.mapId~=obs.mapId or
       not number(own.x) or not number(own.y) or not number(own.z) then return end
    local inside=false
    for _,p in ipairs(input.points) do
        if p.groundEffectGuid then
            if own.combatReachKnown~=true or not number(own.combatReach) or own.combatReach<0 then
                out.movementPointStatus={phase="observed_effect_clearance_unknown",insideKnown=false};return
            end
            local radius=p.radiusYards+own.combatReach
            if (own.x-p.x)^2+(own.y-p.y)^2+(own.z-p.z)^2<radius*radius then inside=true end
        elseif math.abs(own.z-p.z)<=2 and (own.x-p.x)^2+(own.y-p.y)^2<p.radiusYards*p.radiusYards then inside=true end
    end
    if not inside then out.movementPointStatus={phase="outside_authored_points",inside=false};return end
    local owner=nav.movementOwner
    local owns=type(ownMovement)=="table" and type(ownMovement.token)=="string" and #ownMovement.token>0 and
        type(owner)=="table" and owner.active==true and ownMovement.owner==owner.owner
    if taskHeld or #frame.pending~=0 or nav.motionUnconfirmed==true or (nav.movementPoints or {}).reason=="protected_cast_pending" or
       type(owner)~="table" or owner.active==true and not owns then
        out.movementPointStatus={phase="waiting_for_existing_operation",inside=true};return
    end
    local action=out.action
    local cap=action and action.capability
    -- Keep deliberate owner spells and friendly survival choices intact. This
    -- is editable Lua priority, not an engine rule or cancellation permission.
    if action and (action==explicitSpell or cap=="heal" or cap=="cleanse" or cap=="resurrect" or cap=="fear_break" or cap=="consume" or cap=="cancel_spell") then
        out.movementPointStatus={phase="explicit_or_survival_action_priority",inside=true};return
    end
    if cap and cap~="offense" and cap~="cast_spell" and cap~="buff" and cap~="approach_target" and cap~="melee_start" then
        out.movementPointStatus={phase="existing_task_action_priority",inside=true};return
    end
    out.action={capability="movement_points_step",sessionId=obs.sessionId,selfGuid=obs.selfGuid,mapId=obs.mapId,
        visitId=obs.visitId,sequenceId=sequence,movementOwner=owns and ownMovement.owner or nil,movementToken=owns and ownMovement.token or nil}
    out.followFormation,out.portalCatchup=nil,nil
    out.movementPointStatus={phase="outward_step_requested",inside=true,poseEvidence=pose,gameEffectClaimed=false}
end
return M
