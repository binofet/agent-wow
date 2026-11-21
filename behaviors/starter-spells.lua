-- Pure named-mission adapter. Shared spell and healing modules own every
-- rank, damage and resource comparison; native admission owns the exact cast.
local spells = assert(encounter_spells)
local healing = assert(encounter_healing)
local M = {}

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

local function scalar(value, integral)
    return value==nil or type(value)=="number" and value>=0 and value<=
        (integral and 4294967295 or 9007199254740991) and (not integral or value%1==0)
end

local function cost(value)
    return decimal(value) or type(value)=="number" and value>=0 and
        value<=9007199254740991 and value%1==0
end

local function offense_mechanics(obs)
    for key,row in pairs(obs.spells) do
        if type(row)=="table" and row.known==true and row.capability=="offense" then
            local power=row.powerType
            if type(power)~="number" or power<-2147483648 or power>2147483647 or power%1~=0 then return false end
            if power==0 then
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

function M.decide(input, override)
    local wait={kind="wait",spellId=0}
    if type(input)~="table" or type(input.sessionId)~="string" or #input.sessionId==0 or
       not guid(input.selfGuid) or type(input.autoAttackKnown)~="boolean" then return wait end
    local obs,target=input.observation,input.target
    if type(obs)~="table" or obs.sessionId~=input.sessionId or obs.selfGuid~=input.selfGuid or
       type(obs.players)~="table" or type(obs.spells)~="table" or type(target)~="table" or
       not guid(target.guid) then return wait end
    local self=obs.players[input.selfGuid]
    if type(self)~="table" or self.guid~=input.selfGuid or self.classKnown~=true or
       not uint(self.classId) or self.classId==0 or self.powerKnown~=true or
       not uint(self.power) or not uint(self.powerType) or not pose(self) or not pose(target) then return wait end
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
