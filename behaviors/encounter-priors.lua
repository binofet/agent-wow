-- Model-editable encounter knowledge. Engage means assist/focus, not pull.
-- A deliberate `engage {pull=true}` overrides this default for that exact GUID.
-- The exact current explicit cast_spell proposal also wins; automatic casts do not.
-- These are roles and observed hazards, never inferred pack links or aggro.
-- Creeping Sludge carries Poison Shock (22638, triggering 22595 every 2 s).
-- Its five-yard pulse follows the living carrier. Noxious Slime (12221)
-- instead casts the stationary Noxious Cloud (21070) observed by World.
-- The backline rule controls pull admission. An authored standing ranged
-- stance is opt-in; the ranged_only command supplies its own finite order.
-- Build 12340 Sonic Burst (8281) hits enemies within 10 yards of the live
-- Diemetradon, including the boundary. Its six-second silence is a target
-- aura; the creature never leaves a ground effect behind.
local sonic={id="maraudon_sonic_burst",mapId=349,creatureEntry=13323,combatStandoff=true,
    minRangeYards=10,maxRangeYards=26,clearanceYards=4,durationMs=5000,attackWitnessMaxAgeMs=5000}
local rules = {
    {id="maraudon_sludge_backline", mapId=349, creatureEntry=12222,
     tankGuid="3", minRangeYards=14, maxRangeYards=36, automaticRange=false,
     clearanceYards=2, durationMs=600000, attackWitnessMaxAgeMs=5000,
     pulseRadiusYards=5, pulseIntervalMs=2000},
    sonic,
}
-- The accepted actor intent may designate another tank. Bind it when the
-- modules are composed, including on source reload.
local tank=false
local function set_role(role) tank=role=="tank" end
local function rule(obs)
    if tank then return end
    for _,r in ipairs(rules) do
        if not r.combatStandoff and obs.mapId==r.mapId and obs.selfGuid~=r.tankGuid then
            for _,npc in pairs(obs.targets or {}) do
                if npc.entry==r.creatureEntry and npc.present==true and npc.aliveKnown==true and npc.alive==true then return r end
            end
        end
    end
end
local function admission(obs,id,explicitPull,r)
    r=r or rule(obs)
    local npc=(obs.targets or {})[id]
    if not r or not npc or npc.entry~=r.creatureEntry then return true,"outside_prior" end
    if explicitPull==id then return true,"explicit_pull_override" end
    local a=npc.attackObservation
    local victim=a and a.victimGuid
    local player=victim and (obs.players or {})[victim]
    local member=victim==obs.selfGuid or victim==r.tankGuid
    local group=(obs.party or {}).group or {}
    if group.known==true and group.inGroup==true then
        for _,row in ipairs(group.members or {}) do
            if row.guid==victim and row.online==true then member=true end
        end
    end
    local age=a and tonumber(a.ageMs or "0")
    local sustained=r.combatStandoff and a and a.targetKnown==true and a.targetGuid==victim and
        npc.unitFlagsKnown==true and math.floor((npc.unitFlags or 0)/524288)%2==1 and
        player and player.combatKnown==true and player.inCombat==true
    if a and a.victimKnown==true and member and player and player.present==true and
       player.mapKnown==true and player.mapId==obs.mapId and npc.mapKnown==true and npc.mapId==obs.mapId and
       age and age>=0 and (age<=r.attackWitnessMaxAgeMs or sustained) and
       (a.targetKnown~=true or a.targetGuid==victim) then
        return true,victim==r.tankGuid and "tank_attack_witness" or "party_attack_defense"
    end
    return false,"waiting_for_tank_attack_witness"
end
-- Only a living carrier currently fighting this party activates caster
-- spacing. An idle sighting cannot acquire the body or change formation.
local function fighting_carrier(obs,id,n,r)
    if not n or n.entry~=r.creatureEntry or n.present~=true or n.aliveKnown~=true or n.alive~=true or
       n.mapKnown~=true or n.mapId~=obs.mapId or not admission(obs,id,nil,r) then return false end
    local victim=(obs.players or {})[(n.attackObservation or {}).victimGuid]
    local self=(obs.players or {})[obs.selfGuid]
    return victim and victim.combatKnown==true and victim.inCombat==true or
           self and self.combatKnown==true and self.inCombat==true
end
local function caster_standoff(obs)
    local self=(obs.players or {})[obs.selfGuid]
    if tank or obs.mapId~=sonic.mapId or obs.hostTargetRangeBandSupported~=true or
       not self or self.classKnown~=true or self.classId~=5 and self.classId~=8 and self.classId~=9 or
       self.aliveKnown~=true or self.alive~=true or self.mapKnown~=true or self.mapId~=obs.mapId or
       self.positionInferred==true or not encounter_movement or not encounter_movement.current_pose(self) then return end
    local fighting=false
    for id,n in pairs(obs.targets or {}) do
        if fighting_carrier(obs,id,n,sonic) then fighting=true;break end
    end
    if not fighting then return end
    local capability=self.classId==5 and "heal" or "offense"
    local reach=0
    for _,spell in pairs(obs.spells or {}) do
        if spell.known==true and spell.metadataKnown==true and spell.capability==capability and spell.weaponStrike~=true and
           type(spell.maxRange)=="number" and spell.maxRange>reach then reach=spell.maxRange end
    end
    if self.classId~=5 then return sonic,nil,reach>0 and reach or nil end
    -- Use a currently visible party tank. The followed owner is the ordinary
    -- party's anchor when its roster carries no tank assignment.
    local group=(obs.party or {}).group or {}
    if group.known~=true or group.inGroup~=true then return end
    local anchor,owner
    for _,member in ipairs(group.members or {}) do
        local p=(obs.players or {})[member.guid]
        if member.online==true and member.guid~=obs.selfGuid and p and p.aliveKnown==true and p.alive==true and
           p.mapKnown==true and p.mapId==obs.mapId and p.positionInferred~=true and encounter_movement.current_pose(p) then
            if member.guid==(obs.navigation or {}).ownerGuid then owner=member.guid end
            if math.floor((member.roles or 0)/2)%2==1 or (member.flags or 0)%2==1 then
                if not anchor or member.guid==owner then anchor=member.guid end
            end
        end
    end
    anchor=anchor or owner
    if anchor and reach>0 then return sonic,anchor,reach end
end
-- Pull admission is standing knowledge; positioning is needed only for an
-- observed near hazard or an actual current party fight. Visibility alone is
-- not a movement request. Unknown geometry remains conservative.
local function positioning(obs,r)
    r=r or rule(obs)
    if not r then return false,"outside_prior" end
    local self=(obs.players or {})[obs.selfGuid]
    if not self or self.present~=true or self.aliveKnown~=true or self.alive~=true or
       self.combatKnown~=true or self.inCombat==true or self.mapKnown~=true or
       self.mapId~=obs.mapId or self.positionKnown~=true or self.positionInferred==true then
        return true,"positioning_facts_unavailable"
    end
    local age=self.positionAgeKnown==true and tonumber(self.positionAgeMs or "0")
    if self.positionEstimated~=true and self.positionAgeKnown==true and (not age or age<0 or age>r.attackWitnessMaxAgeMs) then return true,"stale_own_pose" end
    for _,v in ipairs({self.x or 0,self.y or 0,self.z or 0}) do
        if type(v)~="number" or v~=v or v==math.huge or v==-math.huge then return true,"positioning_facts_unavailable" end
    end
    local band=r.minRangeYards+r.clearanceYards
    for id,npc in pairs(obs.targets or {}) do
        if npc.entry==r.creatureEntry and npc.present==true and npc.alive~=false then
            if npc.aliveKnown~=true or npc.mapKnown~=true or npc.mapId~=obs.mapId or npc.positionKnown~=true or
               npc.positionInferred==true or npc.positionEstimated==true then return true,"positioning_facts_unavailable" end
            for _,v in ipairs({npc.x or 0,npc.y or 0,npc.z or 0}) do
                if type(v)~="number" or v~=v or v==math.huge or v==-math.huge then return true,"positioning_facts_unavailable" end
            end
            if type(npc.attackObservation)~="table" then return true,"attack_facts_unavailable" end
            local admitted,why=admission(obs,id)
            if admitted and why~="outside_prior" then return true,why end
            -- Historical engagement and an old victim's current combat do
            -- not identify this creature's present fight. Admission above
            -- requires the actual known/fresh/noncontradictory attack tuple.
            local dx,dy,dz=(self.x or 0)-(npc.x or 0),(self.y or 0)-(npc.y or 0),(self.z or 0)-(npc.z or 0)
            if dx*dx+dy*dy+dz*dz<=band*band then return true,"near_hazard" end
        end
    end
    return false,"passive_distant"
end
local function pulse_radius(obs,id,r)
    r=r or rule(obs)
    local n=(obs.targets or {})[id]
    if r and r.automaticRange~=true and n and n.entry==r.creatureEntry and n.present==true and
       n.aliveKnown==true and n.alive==true and n.mapKnown==true and n.mapId==obs.mapId and
       admission(obs,id,nil,r) then return r.pulseRadiusYards end
end

-- A pulse has a moving carrier. Bind each center to the current pose
-- revision, fresh party-attack witness and short observation horizon. A
-- later observation replaces the center; there is no trail behind the NPC.
local function pulse_points(obs,sequence,now,history)
    local points,positions={},{}
    local r=rule(obs)
    if not r or r.automaticRange==true or type(r.pulseRadiusYards)~="number" or
       type(r.pulseIntervalMs)~="number" or type(now)~="number" then return points,positions end
    for id,n in pairs(obs.targets or {}) do
        if n.entry==r.creatureEntry and n.present==true and n.aliveKnown==true and n.alive==true and
           n.mapKnown==true and n.mapId==obs.mapId and n.positionKnown==true and
           n.positionInferred~=true and n.positionEstimated~=true and type(n.positionRevision)=="string" and
           n.positionRevision~="0" and n.positionRevision~="" then
            local old=history[id]
            local same=old and old.revision==n.positionRevision
            local seenAt=same and old.seenAt or now
            local attack=n.attackObservation
            local age=attack and tonumber(attack.ageMs or "0")
            local victim=attack and attack.victimGuid
            local repeated=old and (old.sequence==sequence or old.worldRevision==obs.worldObservationRevision and
                old.attackAge==age and old.victim==victim and same)
            local sample=repeated and old.sample or now
            positions[id]={revision=n.positionRevision,seenAt=seenAt,sequence=sequence,sample=sample,
                worldRevision=obs.worldObservationRevision,attackAge=age,victim=victim}
            local pose=encounter_movement and encounter_movement.current_pose(n)
            if pose and age and age>=0 and pulse_radius(obs,id,r) then
                local trajectory=n.trajectory
                local traveled=trajectory and trajectory.known==true and tonumber(trajectory.ageMs or "0")
                -- A fresh active spline also locates the carrier between
                -- position revisions. Stop at its observed destination time.
                local poseUntil=seenAt+r.pulseIntervalMs
                if not trajectory or trajectory.known~=true or trajectory.stopped==true then
                    -- A stationary visible carrier can pulse repeatedly at
                    -- one unchanged revision. Each new witnessed attack
                    -- corroborates that center for one more pulse interval.
                    poseUntil=math.max(poseUntil,sample-age+r.pulseIntervalMs)
                elseif traveled and traveled>=0 and type(trajectory.durationMs)=="number" and traveled<trajectory.durationMs then
                    poseUntil=sample+math.min(r.pulseIntervalMs,trajectory.durationMs-traveled)
                end
                local expires=math.min(poseUntil,sample+r.attackWitnessMaxAgeMs-age)
                if now<expires then
                    points[#points+1]={x=pose.x,y=pose.y,z=pose.z,radiusYards=r.pulseRadiusYards,
                        expiresAtUnixMs=expires,carrierGuid=id}
                end
            end
        end
    end
    return points,positions
end
local function filter(obs,out,status,explicitPull,requestedSpell)
    local r=rule(obs)
    if not r then return end
    local action=out.action
    local id=action and (action.targetGuid or (action.targets or {}).objectGuid) or out.focusGuid
    local allowed,reason=admission(obs,id,explicitPull)
    if requestedSpell and requestedSpell.capability=="cast_spell" and
       (requestedSpell.targets or {}).objectGuid==id then allowed,reason=true,"explicit_spell_request" end
    local waiting=false
    for target,npc in pairs(obs.targets or {}) do
        if npc.entry==r.creatureEntry and not admission(obs,target,explicitPull,r) then waiting=true end
    end
    if not id and waiting then allowed,reason=false,"waiting_for_tank_attack_witness" end
    out.encounterPrior={id=r.id,tankGuid=r.tankGuid,role="backline",targetGuid=id,allowed=allowed,reason=reason}
    if waiting and r.automaticRange==true and positioning(obs,r) then out.followFormation,out.portalCatchup=nil,nil end
    if allowed then return end
    local cap=action and action.capability
    local npc=id and (obs.targets or {})[id]
    -- Only the already range-checked withdrawal may move before engagement.
    -- Do not turn a rejected offensive proposal into an uncorrelated retreat.
    local withdrawal=status and status.phase=="withdrawing_ground_input"
    if cap=="approach_target" and npc and npc.entry==r.creatureEntry and action.minRangeYards then
        local self=(obs.players or {})[obs.selfGuid]
        if self and self.positionKnown==true and npc.positionKnown==true and
           self.positionInferred~=true and (self.positionEstimated~=true or
               obs.hostTargetRangeBandSupported==true and action.sessionId==obs.sessionId and action.selfGuid==obs.selfGuid and action.visitId==obs.visitId and
               type(action.minRangeYards)=="number" and action.minRangeYards>=r.minRangeYards+r.clearanceYards and
               type(action.clearanceYards)=="number" and action.clearanceYards>=r.minRangeYards+r.clearanceYards) and npc.positionInferred~=true and npc.positionEstimated~=true then
            local dx,dy,dz=(self.x or 0)-(npc.x or 0),(self.y or 0)-(npc.y or 0),(self.z or 0)-(npc.z or 0)
            withdrawal=dx*dx+dy*dy+dz*dz<(r.minRangeYards+r.clearanceYards)^2
        end
    end
    local cleanup=cap=="movement_owner" or cap=="stop_movement" or cap=="melee_stop" or cap=="cancel_spell"
    local directed=action and (action.targetGuid or (action.targets or {}).objectGuid)
    if not cleanup and not withdrawal and (cap=="move_body" and r.automaticRange==true or directed and npc and npc.entry==r.creatureEntry) then out.action=nil end
end
return {rules=rules,set_role=set_role,rule=rule,caster_standoff=caster_standoff,fighting_carrier=fighting_carrier,admission=admission,positioning=positioning,pulse_radius=pulse_radius,pulse_points=pulse_points,filter=filter}
