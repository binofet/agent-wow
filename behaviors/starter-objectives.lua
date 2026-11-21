-- Starter mission objectives and practice lifecycle. The caller owns observed
-- facts, exact native toggles, durable mutation reservations and receipt custody.
local M = {}
local function array(v,max)
    if type(v)~="table" or #v>max then return false end
    local n=0
    for k in pairs(v) do
        if type(k)~="number" or k<1 or k>max or k%1~=0 then return false end
        n=n+1
    end
    return n==#v
end
local function ids(v,max)
    if not array(v,max) then return end
    local set={}
    for _,q in ipairs(v) do
        if type(q)~="number" or q<=0 or q>4294967295 or q%1~=0 then return end
        local key=tostring(q)
        if set[key] then return end
        set[key]=true
    end
    return set
end
local function decimal(v)
    if type(v)~="string" or #v==0 or #v>20 or #v>1 and byte_at(v,1)==48 or
       #v==20 and v>"18446744073709551615" then return false end
    for i=1,#v do local c=byte_at(v,i); if c<48 or c>57 then return false end end
    return true
end
local function number(v) return type(v)=="number" and v-v==0 and math.abs(v)<=9007199254740991 end
local function wait(phase) return {kind="wait",phase=phase} end
local function ready(choice) return {status="ready",choice=choice} end
local function invalid() return {status="invalid"} end
local function practice(input,active)
    local a=input.actions or {}
    if a.practicing==true then
        local lease=a.practiceLeaseRemainingMs
        if not decimal(lease) then return invalid() end
        if #lease>5 or #lease==5 and lease>"60000" then
            return ready(wait(active["33"] and "practicing_for_wolf_quest" or "practicing_to_level_2"))
        end
    end
    if not number(input.now) or not number(input.nextMutationAt) then return invalid() end
    if input.now<input.nextMutationAt then return ready(wait()) end
    return ready({kind="enable_practice",practiceOwned=true})
end
function M.decide(input)
    if type(input)~="table" then return invalid() end
    local active,desired,confirmed=ids(input.activeQuestIds,25),ids(input.desiredQuestIds,25),ids(input.confirmedQuestIds,3)
    if not active or not desired or not confirmed or not decimal(input.selfGuid) or type(input.autonomous)~="boolean" then return invalid() end
    local missing,exact={},true
    for q in pairs(desired) do if not active[q] then missing[q]=true; exact=false end end
    for q in pairs(active) do if not desired[q] then exact=false end end
    local complete,failed=input.wolfComplete==true,input.wolfFailed==true
    local wolves=active["33"] and not complete and not failed
    local known,level=input.levelKnown==true,input.level
    if known and (type(level)~="number" or level<1 or level>80 or level%1~=0) then return invalid() end
    local xp=known and level==1 and (input.selfGuid=="4" or input.selfGuid=="5") and missing["18"] and (active["3903"] or confirmed["33"])
    local needed=wolves and known and level>=1 and level<=5 or xp
    local routine,a=input.routine,input.actions
    if routine==nil then routine={} end
    if a==nil then a={} end
    if type(routine)~="table" or type(a)~="table" then return invalid() end
    if input.stage=="classify" then
        if exact and not (input.autonomous and routine.practiceOwned==true) then
            return ready(wait("matched_requested_active_quest_ids"))
        end
        local self,owner=input.self or {},input.owner or {}
        if input.autonomous then
            if self.present~=true or self.aliveKnown~=true or self.alive~=true then
                return ready(wait("waiting_for_alive_observed_character"))
            end
            return ready({kind="hold_follow"})
        end
        if owner.objectKnown~=true then return ready(wait("waiting_for_alive_present_owner_and_character")) end
        if not decimal(owner.guid) then return invalid() end
        if owner.guid~="3" or self.present~=true or self.aliveKnown~=true or self.alive~=true or
           owner.present~=true or owner.aliveKnown~=true or owner.alive~=true then
            return ready(wait("waiting_for_alive_present_owner_and_character"))
        end
        if owner.awayFlagsKnown~=true then return invalid() end
        if owner.away==true then return ready(wait("waiting_for_alive_present_owner_and_character")) end
        return ready({kind="continue"})
    elseif input.stage=="post_hold" then
        if input.autonomous then
            if a.practicePhase=="finishing_current_fight_to_level2" or a.practicePhase=="finishing_current_fight_to_goal" or
               not needed and (complete or known and level>=2) and a.practicing==true then
                return ready(wait("finishing_current_practice_engagement_before_quests"))
            end
            if (input.self or {}).combatFlagsKnown~=true then return invalid() end
            if input.self.inCombat==true then return ready(wait("waiting_out_of_combat_before_quests")) end
            if exact then return ready(wait("matched_requested_active_quest_ids")) end
        end
        if input.actionInFlight==true then return ready(wait("waiting_for_existing_quest_operation")) end
        if failed then return ready(wait("waiting_for_failed_wolf_prerequisite_resolution")) end
        if needed then
            if input.autonomous then return ready({kind="practice",lootRequired=wolves==true}) end
            return ready(wait(wolves and "waiting_for_own_wolf_objectives" or "waiting_for_level_2"))
        end
        if wolves then return ready(wait("waiting_for_supported_wolf_practice_level")) end
        return ready({kind="continue_quests"})
    elseif input.stage=="loot" then
        local loot=input.loot or {}
        if loot.known~=true then return ready(wait("waiting_for_wolf_loot_module")) end
        if loot.enabled==true then return ready({kind="continue"}) end
        return ready({kind="enable_loot",phase="waiting_for_confirmed_wolf_looting"})
    elseif input.stage=="practice" then
        return practice(input,active)
    elseif input.stage=="before_quests" then
        if a.practicing==true or routine.practiceOwned==true and
           (a.autoAttacking==true or a.autoAttackRequested==true or a.autoAttackUnconfirmed==true) then
            return ready({kind="disable_practice"})
        end
        if a.autoAttackUnconfirmed==true then return ready(wait("waiting_for_practice_attack_stop")) end
        local choice={kind="continue_quests"}
        if routine.practiceOwnerPresent==true then choice.practiceOwned=false end
        return ready(choice)
    elseif input.stage=="fallback" then
        if next(missing)==nil then return ready(wait("requested_ids_present_extra_quests_unchanged")) end
        local phase=known and level<2 and "waiting_for_level_2" or "waiting_for_level_observation"
        if input.autonomous and known and level==1 and (missing["18"] or missing["3903"]) then
            return ready({kind="practice",phase=phase})
        end
        return ready(wait(phase))
    end
    return invalid()
end
return M
