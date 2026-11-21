-- Shared reloadable buff policy. The harness supplies learned spell mechanics
-- and observed auras; this module owns rank, recipient, refresh and retry choices.
local M = {tuning={reserve_fraction=.35,reserve_fraction_by_class={[5]=.30,[8]=.30,[9]=.30},self_health_fraction=.95,party_health_fraction=.90,
    water_refresh_ms=60000,retry_ms=5000},self_reserve_by_class={[2]=.05}}
local state={blocked={}}
local function alive(p) return p and p.present==true and p.aliveKnown==true and p.alive==true end
local function guid_less(a,b) return #a<#b or #a==#b and a<b end
local function has(mask,bits)
    mask,bits=mask or 0,bits or 0
    while bits>0 do
        if bits%2==1 and mask%2==1 then return true end
        bits=(bits-bits%2)/2
        mask=(mask-mask%2)/2
    end
    return false
end
local function effect(spell,aura,misc)
    for _,e in ipairs(spell.auraEffects or {}) do
        if e.kind==6 and e.aura==aura and (misc==nil or e.misc==misc) then return true end
    end
    return false
end
local function armor_group(s)
    local f=s.familyFlags or {}
    if s.familySet==3 and has(f[1],302252032) then return 1 end -- published mage armor flags
    if s.familySet==5 and (has(f[2],536870944) or has(f[3],16)) then return 2 end
    if effect(s,22,1) then return 3 end
end
local function alternate_stronger(a,b)
    if a.familyId==b.familyId or (a.level or 0)<(b.level or 0) then return false end
    local x,y=armor_group(a),armor_group(b)
    return x~=nil and x==y
end
local function water(s) return effect(s,82) end
local function positive(a)
    return a.positive==true and (not a.durationKnown or (a.remainingMs or 0)>0)
end
local function observed_buff(obs,target,spell)
    local support=obs.support or {}
    for _,row in ipairs(support.playerAuras or {}) do
        if row.targetGuid==target then
            for _,aura in ipairs(row.auras or {}) do
                local known=(support.auraSpells or {})[tostring(aura.spellId)]
                if positive(aura) and known and known.metadataKnown==true then
                    local same=known.familyId==spell.familyId and (known.rank or 0)>=(spell.rank or 0)
                    if water(spell) then
                        if water(known) and (not aura.durationKnown or (aura.remainingMs or 0)>=M.tuning.water_refresh_ms) then return true end
                    elseif same or alternate_stronger(known,spell) then return true end
                end
            end
        end
    end
    return false
end
local function members(obs)
    local result={[obs.selfGuid]=true}
    local group=((obs.party or {}).group or {})
    if group.known==true and group.inGroup==true then
        for _,member in ipairs(group.members or {}) do
            if member.online==true then result[member.guid]=true end
        end
    end
    return result
end
local function healthy(p,fraction)
    return alive(p) and p.healthKnown==true and (p.maxHealth or 0)>0 and
        (p.health or 0)>=(p.maxHealth or 0)*fraction
end
local function prefer_rank(a,b)
    if not b then return true end
    if (a.level or 0)~=(b.level or 0) then return (a.level or 0)>(b.level or 0) end
    if (a.rank or 0)~=(b.rank or 0) then return (a.rank or 0)>(b.rank or 0) end
    return a.id<b.id
end
local function prefer(a,b)
    if not b then return true end
    if a.water~=b.water then return a.water end
    if a.selfOnly~=b.selfOnly then return a.selfOnly end
    if a.spell.familyId~=b.spell.familyId then return a.spell.familyId<b.spell.familyId end
    if a.selfTarget~=b.selfTarget then return a.selfTarget end
    return guid_less(a.target,b.target)
end
function M.on_event(event)
    -- A legacy enable call asks the current policy to check useful buffs. It
    -- cannot create an off state or bypass ordinary healing/combat priority.
    if event.kind=='buff_request' then state.ensureRequested=true end
    if event.kind=='encounter_observation' and type(event.value)=='table' then
        if state.session and state.session~=event.value.sessionId then state={blocked={}} end
        state.session=event.value.sessionId
    end
    local receipt=event.receipt
    if receipt and (receipt.payload or {}).capability=='buff' then
        local p=receipt.payload
        if state.last and p.target_guid==state.last.target and p.spell_id==state.last.spell then
            local key=state.last.target..':'..tostring(state.last.family)
            if receipt.outcome=='not_dispatched' then state.blocked[key]=nil
            elseif receipt.outcome=='confirmed' then state.blocked[key]=nil
            elseif receipt.outcome=='failed' or receipt.outcome=='unknown' or receipt.outcome=='settled_unverified' then
                state.blocked[key]={outcome=receipt.outcome}
            end
        end
    end
    return true
end
function M.choose(obs,frame)
    local self=(obs.players or {})[obs.selfGuid]
    if (obs.hostOwned~=true and obs.hostRequested~=true) or obs.spellbookKnown~=true or not alive(self) or
       self.mapKnown~=true or self.mapId~=obs.mapId or self.positionKnown~=true or self.combatKnown~=true or self.inCombat==true then return end
    for _,npc in pairs(obs.targets or {}) do if alive(npc) and npc.engaged==true then return end end
    local now=frame.clock and frame.clock.monotonicMs
    if type(now)~='number' then return end
    -- This is a transient check request, not evidence of a cast or aura.
    state.ensureRequested=nil
    local group=members(obs)
    local ranks={}
    for text,s in pairs(obs.spells or {}) do
        local id=tonumber(text)
        if id and s.capability=='buff' and s.known==true and s.metadataKnown==true and
           s.available==true and s.ready==true and (s.familyId or 0)>0 then
            local r={};for k,v in pairs(s) do r[k]=v end;r.id=id
            if prefer_rank(r,ranks[r.familyId]) then ranks[r.familyId]=r end
        end
    end
    local best
    for _,s in pairs(ranks) do
        local suppressed=false
        for _,other in pairs(obs.spells or {}) do
            if other.known==true and other.metadataKnown==true and alternate_stronger(other,s) then suppressed=true end
        end
        local wet=water(s)
        local reserve_fraction=self.classKnown==true and M.tuning.reserve_fraction_by_class[self.classId] or M.tuning.reserve_fraction
        local self_reserve=self.classKnown==true and M.self_reserve_by_class[self.classId] or reserve_fraction
        local context=wet or healthy(self,M.tuning.self_health_fraction)
        if not wet then
            for target in pairs(group) do
                local p=(obs.players or {})[target]
                if alive(p) and p.mapKnown==true and p.mapId==obs.mapId and not healthy(p,M.tuning.party_health_fraction) then context=false end
            end
        end
        if not suppressed and context and self.powerKnown==true and self.powerType==s.powerType then
            for target in pairs(group) do
                local p=(obs.players or {})[target]
                local key=target..':'..tostring(s.familyId)
                local retry=state.blocked[key]
                if retry and not retry.at then retry.at=now+M.tuning.retry_ms end
                local mana_ok=not effect(s,29,3) or p and p.powerKnown==true and p.powerType==0
                local reserve=wet and 0 or (self.maxPower or 0)*(target==obs.selfGuid and self_reserve or reserve_fraction)
                if (self.power or 0)>=(s.cost or 0)+reserve and
                   alive(p) and p.positionKnown==true and p.mapKnown==true and p.mapId==obs.mapId and
                   (target==obs.selfGuid or s.targetRule=='ally') and mana_ok and
                   (not retry or now>=retry.at) and not observed_buff(obs,target,s) then
                    local candidate={target=target,spell=s,water=wet,selfOnly=s.targetRule=='self',selfTarget=target==obs.selfGuid}
                    if prefer(candidate,best) then best=candidate end
                end
            end
        end
    end
    if not best then return end
    local s,target=best.spell,best.target
    state.last={target=target,spell=s.id,family=s.familyId}
    local p=(obs.players or {})[target]
    local d=(self.x or 0)-(p.x or 0);local e=(self.y or 0)-(p.y or 0);local f=(self.z or 0)-(p.z or 0)
    local distance=math.sqrt(d*d+e*e+f*f)
    return {capability='buff',sessionId=obs.sessionId,selfGuid=obs.selfGuid,targetGuid=target,spellId=s.id,
        needsApproach=target~=obs.selfGuid and (distance>(s.maxRange or 0) or distance<(s.minRange or 0))}
end
function M.export_state() return {version=1,data=state} end
function M.import_state(saved)
    if saved.version~=1 or type(saved.data)~='table' or type(saved.data.blocked)~='table' then return false end
    state=saved.data;return true
end
return M
