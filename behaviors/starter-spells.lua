-- Pure named-mission adapter. Shared spell and healing modules own every
-- rank, damage and resource comparison; native admission owns the exact cast.
local spells = assert(encounter_spells)
local healing = assert(encounter_healing)
local starter_movement = encounter_movement
local M = {}
M.coldridge_pull = {health_percent=95, base_mana_percent=60}
M.practice_targets = {entries={[299]=true},min_level=1,max_level=1,search_yards=120,order="nearest",
    area={min_x=-9599,max_x=-8400,min_y=-700,max_y=200,min_z=40,max_z=180}}

local function decimal(value)
    if type(value) ~= "string" or #value == 0 or #value > 20 or #value>1 and byte_at(value, 1) == 48 or
       #value == 20 and value > "18446744073709551615" then return false end
    for i=1,#value do local digit=byte_at(value,i); if digit<48 or digit>57 then return false end end
    return true
end

local function guid(value) return decimal(value) and value~="0" end

local function uint(value)
    return type(value)=="number" and value>=0 and value<=4294967295 and value%1==0
end

local function pull_readiness(self)
    local wait={kind="wait",spellId=0}
    local tuning=M.coldridge_pull
    if type(tuning)~="table" or not uint(tuning.health_percent) or tuning.health_percent>100 or
       not uint(tuning.base_mana_percent) or tuning.base_mana_percent>100 or
       self.classId~=9 or self.healthKnown~=true or not uint(self.health) or
       not uint(self.maxHealth) or self.maxHealth==0 or self.health>self.maxHealth or
       self.powerKnown~=true or self.powerType~=0 or not uint(self.power) or
       self.baseManaKnown~=true or not uint(self.baseMana) or self.baseMana==0 then return wait end
    -- uint32 observations multiplied by a percentage remain exact Lua numbers.
    local mana=math.floor((self.baseMana*tuning.base_mana_percent+99)/100)
    if self.health*100<self.maxHealth*tuning.health_percent or self.power<mana then return wait end
    return {kind="ready",spellId=0}
end

local function scalar(value, integral)
    return value==nil or type(value)=="number" and value>=0 and value<=
        (integral and 4294967295 or 9007199254740991) and (not integral or value%1==0)
end

local function cost(value)
    return decimal(value) or type(value)=="number" and value>=0 and
        value<=9007199254740991 and value%1==0
end

local function offense_mechanics(obs, expected_power)
    expected_power=expected_power or 0
    for key,row in pairs(obs.spells) do
        if type(row)=="table" and row.known==true and row.capability=="offense" then
            local power=row.powerType
            if type(power)~="number" or power<-2147483648 or power>2147483647 or power%1~=0 then return false end
            if power==expected_power then
                if not guid(key) or #key>10 or tonumber(key)>4294967295 or row.metadataKnown~=true or
                   not cost(row.cost) or not scalar(row.minRange,false) or row.maxRange==nil or
                   not scalar(row.maxRange,false) or (row.minRange or 0)>row.maxRange or
                   not scalar(row.directDamage,false) then return false end
                for _,field in ipairs({"familyId","rank","level","castTimeMs","globalCooldownMs","cooldownMs","categoryCooldownMs"}) do
                    if not scalar(row[field],true) then return false end
                end
                local ticks=row.periodicDamage
                if ticks~=nil then
                    if type(ticks)~="table" or #ticks>32 then return false end
                    local count=0
                    for index in pairs(ticks) do
                        if not uint(index) or index<1 or index>32 then return false end
                        count=count+1
                    end
                    if count~=#ticks then return false end
                    for _,tick in ipairs(ticks) do
                        if type(tick)~="table" or not scalar(tick.damagePerTick,false) or
                           not scalar(tick.periodMs,true) then return false end
                    end
                end
            end
        end
    end
    return true
end

local function pose(unit)
    if type(unit)~="table" or unit.positionKnown~=true then return false end
    for _,key in ipairs({"x","y","z"}) do
        local value=unit[key]
        -- Known protobuf poses omit zero coordinates in ordinary JSON.
        if value~=nil and (type(value)~="number" or value-value~=0) then return false end
    end
    return true
end

local function boolean(value) return value==nil or type(value)=="boolean" end

local function orientation(unit)
    local value=unit.orientation
    return value==nil or type(value)=="number" and value-value==0
end

local function weapon_choice(obs, target, self, override)
    local wait={kind="wait",spellId=0}
    if self.classId~=4 or self.powerType~=3 or not offense_mechanics(obs,3) or
       not orientation(self) or not orientation(target) or not boolean(self.comboKnown) then return wait end
    if self.comboKnown==true then
        local points,combo_target=self.comboPoints or 0,self.comboTargetGuid or "0"
        if not uint(points) or points>5 or not decimal(combo_target) or points>0 and not guid(combo_target) then return wait end
    end
    local projected,eligible={},{}
    for key,value in pairs(obs) do projected[key]=value end
    for key,row in pairs(obs.spells) do
        if type(row)=="table" and row.known==true and row.capability=="offense" and row.powerType==3 then
            for _,field in ipairs({"weaponStrike","damageModelKnown","requiresComboPoints","requiresBehind","requiresFacing","requiresStealth","ready","available"}) do
                if not boolean(row[field]) then return wait end
            end
            if not scalar(row.weaponDamageMultiplier,false) or not scalar(row.comboPointsGenerated,true) or
               row.weaponBonusDamage~=nil and (type(row.weaponBonusDamage)~="number" or row.weaponBonusDamage-row.weaponBonusDamage~=0) then return wait end
            -- A combo finisher's unknown scaling is handled by the existing
            -- exact-target/five-point guard. Unknown base weapon damage has
            -- no invented score and cannot compete with an observed model.
            if row.weaponStrike==true and (row.damageModelKnown==true or row.requiresComboPoints==true) then eligible[key]=row end
        end
    end
    projected.spells=eligible
    local selected=spells.damage_spell(projected,target,override,
        {instant_only=true,require_metadata=true,power_type=3,max_cost=self.power})
    if selected then return {kind="cast",spellId=selected} end
    return {kind="melee",spellId=0}
end

local function flag(value,mask) return math.floor((value or 0)/mask)%2==1 end
local function practice_target(input,self)
    local wait={kind="wait",targetGuid="0"}
    local stop={kind="stop",targetGuid="0"}
    local t=M.practice_targets
    if type(t)~="table" or type(t.entries)~="table" or type(t.area)~="table" or self.present~=true or self.mapKnown~=true or not pose(self) or
       type(t.search_yards)~="number" or t.search_yards~=t.search_yards or t.search_yards<0 or
       t.search_yards==math.huge or (t.order~="nearest" and t.order~="farthest") or
       not uint(t.min_level) or not uint(t.max_level) or t.min_level>t.max_level or
       type(input.candidates)~="table" or not decimal(input.currentTargetGuid) then return wait end
    local a=t.area
    for _,key in ipairs({"min_x","max_x","min_y","max_y","min_z","max_z"}) do
        if type(a[key])~="number" or a[key]-a[key]~=0 then return wait end
    end
    local function eligible(c)
        if type(c)~="table" or not guid(c.guid) or c.creatureObject~=true or c.present~=true or
           c.mapKnown~=true or c.mapId~=self.mapId or t.entries[c.entry]~=true or
           c.levelKnown~=true or not uint(c.level) or c.level<t.min_level or c.level>t.max_level or
           c.healthKnown~=true or c.unitFlagsKnown~=true or not uint(c.health) or not uint(c.maxHealth) or c.maxHealth==0 or
           c.health==0 or c.health>c.maxHealth or c.protected==true or c.aliveKnown~=true or c.alive~=true or not pose(c) or
           (c.npcFlags or 0)~=0 or flag(c.unitFlags2,1) then return false end
        for _,field in ipairs({"npcFlags","unitFlags","unitFlags2","dynamicFlags"}) do
            if not uint(c[field] or 0) then return false end
        end
        for _,mask in ipairs({2,128,256,65536,33554432,2147483648}) do
            if flag(c.unitFlags,mask) then return false end
        end
        local x,y,z=c.x or 0,c.y or 0,c.z or 0
        return x>=a.min_x and x<=a.max_x and y>=a.min_y and y<=a.max_y and z>=a.min_z and z<=a.max_z
    end
    for _,c in ipairs(input.candidates) do
        if type(c)~="table" then return wait end
        if c.present==true and c.aliveKnown==true and c.alive==true and
           c.attackVictimKnown==true and c.attackVictimGuid==input.selfGuid and
           (c.attackAgeMs or 0)<=30000 and t.entries[c.entry]~=true then return stop end
    end
    local best,score
    for _,c in ipairs(input.candidates) do
        local valid=eligible(c)
        if input.currentTargetGuid~="0" then
            if c.guid==input.currentTargetGuid then
                if valid and (c.attackVictimKnown~=true or c.attackVictimGuid==input.selfGuid) and
                   (not flag(c.dynamicFlags,4) or flag(c.dynamicFlags,8)) and c.guid~=input.failedTargetGuid then
                    return {kind="target",targetGuid=c.guid}
                end
                return not pose(c) and wait or stop
            end
        elseif valid and not flag(c.unitFlags,524288) and (c.dynamicFlags or 0)==0 and
           c.attackVictimKnown~=true and (c.targetKnown~=true or c.targetGuid==nil or c.targetGuid=="0") then
            local dx,dy,dz=(c.x or 0)-(self.x or 0),(c.y or 0)-(self.y or 0),(c.z or 0)-(self.z or 0)
            local d=dx*dx+dy*dy+dz*dz
            if d<t.search_yards*t.search_yards and (not best or t.order=="nearest" and d<score or
               t.order=="farthest" and d>score or d==score and (#c.guid<#best.guid or #c.guid==#best.guid and c.guid<best.guid)) then
                best,score=c,d
            end
        end
    end
    if best then return {kind="target",targetGuid=best.guid} end
    return wait
end

function M.decide(input, override, movement)
    local wait=type(input)=="table" and input.selection=="practice_approach" and
        {kind="wait",standYards=0} or {kind="wait",spellId=0}
    if type(input)~="table" or type(input.sessionId)~="string" or #input.sessionId==0 or
       not guid(input.selfGuid) or type(input.autoAttackKnown)~="boolean" then return wait end
    local obs,target=input.observation,input.target
    if type(obs)~="table" or obs.sessionId~=input.sessionId or obs.selfGuid~=input.selfGuid or
       type(obs.players)~="table" then return wait end
    local self=obs.players[input.selfGuid]
    if input.selection=="practice_target" then
        if type(self)~="table" or self.guid~=input.selfGuid then return {kind="wait",targetGuid="0"} end
        return practice_target(input,self)
    end
    if input.selection=="practice_approach" then
        movement=movement or starter_movement
        if type(self)~="table" or self.guid~=input.selfGuid or type(target)~="table" or not guid(target.guid) or
           type(movement)~="table" or type(movement.practice_approach)~="function" then
            return {kind="wait",standYards=0}
        end
        return movement.practice_approach(input)
    end
    if type(self)~="table" or self.guid~=input.selfGuid or self.classKnown~=true or
       not uint(self.classId) or self.classId==0 or self.powerKnown~=true or
       not uint(self.power) or not uint(self.powerType) then return wait end
    if input.selection=="coldridge_pull_readiness" then return pull_readiness(self) end
    if type(obs.spells)~="table" or type(target)~="table" or not guid(target.guid) or
       not pose(self) or not pose(target) then return wait end
    if input.selection~=nil then
        if input.selection=="weapon_strike" then return weapon_choice(obs,target,self,override) end
        if input.selection~="coldridge_cast" or self.classId~=9 or type(input.pullRequired)~="boolean" then return wait end
        if input.pullRequired and pull_readiness(self).kind~="ready" then return {kind="pull_wait",spellId=0} end
    end
    local reserve=healing.resource_reserve(obs)
    if reserve==nil or not offense_mechanics(obs) then return wait end
    if self.classId==4 then return {kind="melee",spellId=0} end
    local options={}
    for key,value in pairs(spells.starter_options) do options[key]=value end
    options.mana_reserve=reserve
    options.max_cost=math.max(0,self.power-reserve)
    local selected
    if self.powerType==0 and reserve<=self.power then selected=spells.damage_spell(obs,target,override,options) end
    if selected then return {kind="cast",spellId=selected} end
    if input.autoAttackKnown then return {kind="melee",spellId=0} end
    return wait
end

return M
