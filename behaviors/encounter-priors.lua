-- Model-editable encounter knowledge. Engage means assist/focus, not pull.
-- A deliberate `engage {pull=true}` overrides this default for that exact GUID.
-- The exact current explicit cast_spell proposal also wins; automatic casts do not.
-- These are roles and observed hazards, never inferred pack links or aggro.
-- Pull admission remains a standing prior. Automatic positioning activates only
-- once per body/map/visit for durationMs; expiry/cancel needs a new explicit
-- ranged_only order to renew. A new visit may activate its own finite stance.
local rules = {
    {id="maraudon_sludge_backline", mapId=349, creatureEntry=12222,
     tankGuid="3", minRangeYards=14, maxRangeYards=36, clearanceYards=2,
     durationMs=600000, attackWitnessMaxAgeMs=5000},
}
local function rule(obs)
    for _,r in ipairs(rules) do
        if obs.mapId==r.mapId and obs.selfGuid~=r.tankGuid then
            for _,npc in pairs(obs.targets or {}) do
                if npc.entry==r.creatureEntry and npc.present==true and npc.aliveKnown==true and npc.alive==true then return r end
            end
        end
    end
end
local function admission(obs,id,explicitPull)
    local r=rule(obs)
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
    if a and a.victimKnown==true and member and player and player.present==true and
       player.mapKnown==true and player.mapId==obs.mapId and npc.mapKnown==true and npc.mapId==obs.mapId and
       age and age>=0 and age<=r.attackWitnessMaxAgeMs and
       (a.targetKnown~=true or a.targetGuid==victim) then
        return true,victim==r.tankGuid and "tank_attack_witness" or "party_attack_defense"
    end
    return false,"waiting_for_tank_attack_witness"
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
        if npc.entry==r.creatureEntry and not admission(obs,target,explicitPull) then waiting=true end
    end
    if not id and waiting then allowed,reason=false,"waiting_for_tank_attack_witness" end
    out.encounterPrior={id=r.id,tankGuid=r.tankGuid,role="backline",targetGuid=id,allowed=allowed,reason=reason}
    if waiting then out.followFormation,out.portalCatchup=nil,nil end
    if allowed then return end
    local cap=action and action.capability
    local npc=id and (obs.targets or {})[id]
    -- Only the already range-checked withdrawal may move before engagement.
    -- Do not turn a rejected offensive proposal into an uncorrelated retreat.
    local withdrawal=status and status.phase=="withdrawing_ground_input"
    if cap=="approach_target" and npc and npc.entry==r.creatureEntry and action.minRangeYards then
        local self=(obs.players or {})[obs.selfGuid]
        if self and self.positionKnown==true and npc.positionKnown==true and
           self.positionInferred~=true and self.positionEstimated~=true and npc.positionInferred~=true and npc.positionEstimated~=true then
            local dx,dy,dz=(self.x or 0)-(npc.x or 0),(self.y or 0)-(npc.y or 0),(self.z or 0)-(npc.z or 0)
            withdrawal=dx*dx+dy*dy+dz*dz<(r.minRangeYards+r.clearanceYards)^2
        end
    end
    local cleanup=cap=="movement_owner" or cap=="stop_movement" or cap=="melee_stop" or cap=="cancel_spell"
    if not cleanup and not withdrawal and (cap=="move_body" or npc and npc.entry==r.creatureEntry) then out.action=nil end
end
return {rules=rules,rule=rule,admission=admission,filter=filter}
