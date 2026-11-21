-- This policy compares observed gear. The native roll capability owns the
-- immutable offer, current body/visit, deadline, one vote and server receipt.
-- Joel's assigned disenchanter is the verified Mira body GUID4. This role is
-- authored Lua intent; the observed self GUID still binds every offer/vote.
local M = {tuning = {future_levels = 3, pass_below_ms = 1500, disenchanter_guid = "4"}}
local observation, sequence, proposed
local state = {reservations = {}}
local UINT32 = 4294967295
local armor_slots = {[1]=0,[3]=2,[5]=4,[20]=4,[6]=5,[7]=6,[8]=7,[9]=8,[10]=9,[16]=14}
local armor_skills = {[1]=415,[2]=414,[3]=413,[4]=293}
local max_armor = {[1]=4,[2]=4,[3]=3,[4]=2,[5]=1,[6]=4,[7]=3,[8]=1,[9]=1,[11]=2}

local function uint(n, limit)
    return type(n)=="number" and n>=0 and n<=limit and n==math.floor(n)
end
local function decimal(s, zero)
    if type(s)~="string" or #s<1 or #s>20 then return false end
    if s=="0" then return zero==true end
    if byte_at(s,1)==48 then return false end
    for i=1,#s do local b=byte_at(s,i);if b<48 or b>57 then return false end end
    return #s<20 or s<="18446744073709551615"
end
local function revision(n) return decimal(n) or uint(n,9007199254740991) and n>0 end
local function list(t, limit)
    if type(t)~="table" or #t>limit then return false end
    local count=0
    for k in pairs(t) do if not uint(k,#t) or k==0 then return false end;count=count+1 end
    return count==#t
end
local function absent_false(v) return v==nil or v==false end
local function bit(mask, n) return uint(mask,UINT32) and math.floor(mask/2^n)%2==1 end
local function millis(v)
    if type(v)=="string" then
        if not decimal(v,true) or #v>6 then return nil end
        v=tonumber(v)
    end
    if uint(v,600000) then return v end
end
local function profile(item)
    if type(item)~="table" or item.available~=true or not absent_false(item.pending) or
        not uint(item.itemId,UINT32) or item.itemId==0 or not uint(item.quality or 0,4) or
        (item.flags or 0)~=0 or (item.flags2 or 0)~=0 or not absent_false(item.hasSpell) or
        not uint(item.itemLevel or 0,UINT32) or not uint(item.armor or 0,UINT32) then return nil end
    if item.itemClass==4 and item.armorComparisonKnown==true and armor_skills[item.subclass] and
        armor_slots[item.inventoryType]~=nil and (item.inventoryType~=16 or item.subclass==1) then
        return {kind="armor",slot=armor_slots[item.inventoryType],skill=armor_skills[item.subclass]}
    end
    if item.itemClass==2 and item.weaponComparisonKnown==true then
        local skill
        if item.inventoryType==13 or item.inventoryType==21 then
            if item.subclass==4 then skill=54 elseif item.subclass==15 then skill=173 end
        elseif item.inventoryType==17 and item.subclass==10 then skill=136 end
        if skill then return {kind="weapon",slot=15,skill=skill,twohand=item.inventoryType==17} end
    end
end
local function equipment(view)
    if view.equipmentKnown~=true or not revision(view.equipmentRevision) or not list(view.equipment,19) or
        #view.equipment~=19 then return nil end
    local slots,guids={},{}
    for _,row in ipairs(view.equipment) do
        if type(row)~="table" or not uint(row.slot or 0,18) or row.slotKnown~=true or slots[row.slot or 0] then return nil end
        local guid=row.itemGuid or "0"
        if not decimal(guid,true) then return nil end
        if guid=="0" then
            if (row.entry or 0)~=0 or (row.stackCount or 0)~=0 or row.metadataKnown==true then return nil end
        elseif guids[guid] or row.metadataKnown~=true or not uint(row.entry,UINT32) or row.entry==0 or
            (row.stackCount or 1)~=1 then return nil end
        guids[guid]=true;slots[row.slot or 0]=row
    end
    return slots
end
local function skills(view)
    if view.skillsKnown~=true or view.skillsComplete~=true or not revision(view.skillsRevision) or not list(view.skills or {},128) then return nil end
    local out={}
    for _,s in ipairs(view.skills or {}) do
        if type(s)~="table" or not uint(s.skillId,65535) or s.skillId==0 or out[s.skillId]~=nil or
            not uint(s.value or 0,65535) or not uint(s.maxValue or 0,65535) or (s.value or 0)>(s.maxValue or 0) then return nil end
        out[s.skillId]=s.value or 0
    end
    return out
end
local function classification(view,offer,templates,gear)
    local item=templates[tostring(offer.itemEntry)]
    local next=profile(item)
    if not next or offer.itemCount~=1 or (offer.randomSuffix or 0)~=0 or (offer.randomProperty or 0)~=0 then return "unknown" end
    local player=view.player
    if type(player)~="table" or player.guid~=view.selfGuid or player.present~=true or player.mapKnown~=true or
        (player.mapId or 0)~=(offer.mapId or 0) or player.classKnown~=true or player.raceKnown~=true or player.levelKnown~=true or
        not uint(player.classId,11) or not max_armor[player.classId] or not uint(player.raceId,11) or player.raceId==0 or
        not uint(player.level,UINT32) or player.level==0 then return "unknown" end
    if not uint(item.allowableClass,UINT32) or not uint(item.allowableRace,UINT32) then return "unknown" end
    if not bit(item.allowableClass,player.classId-1) or not bit(item.allowableRace,player.raceId-1) or
        next.kind=="armor" and item.subclass>max_armor[player.classId] then return "not_useful" end
    for _,field in ipairs({"requiredSpell","requiredHonorRank","requiredCityRank","requiredReputationFaction","requiredReputationRank"}) do
        if (item[field] or 0)~=0 then return "unknown" end
    end
    if not uint(item.requiredLevel or 0,UINT32) then return "unknown" end
    if (item.requiredLevel or 0)>player.level+M.tuning.future_levels then return "not_useful" end
    local ranks,slots=skills(view),equipment(view)
    if not ranks or not slots or (ranks[next.skill] or 0)==0 then return "unknown" end
    if not uint(item.requiredSkill or 0,65535) or not uint(item.requiredSkillRank or 0,65535) then return "unknown" end
    if (item.requiredSkill or 0)~=0 and ((ranks[item.requiredSkill] or 0)==0 or
        ranks[item.requiredSkill]<(item.requiredSkillRank or 0)) then return "unknown" end
    if next.twohand and (slots[16].itemGuid or "0")~="0" then return "unknown" end
    local equipped=slots[next.slot]
    if (equipped.itemGuid or "0")~="0" and
        (equipped.enhancementsKnown~=true or not absent_false(equipped.hasEnhancements) or equipped.durabilityKnown~=true or
        not uint(equipped.durability or 0,UINT32) or not uint(equipped.maxDurability or 0,UINT32) or
        (equipped.maxDurability or 0)>0 and ((equipped.durability or 0)==0 or equipped.durability>equipped.maxDurability)) then return "unknown" end
    local current=templates[tostring(equipped.entry)]
    if (equipped.itemGuid or "0")~="0" then
        local old=profile(current)
        if not old or old.kind~=next.kind or old.slot~=next.slot then return "unknown" end
    end
    if type(gear)~="table" or type(gear.preview)~="function" then return "unknown" end
    -- The candidate is an unowned offer, with no invented item GUID or
    -- instance enhancements. Actual worn facts were validated above.
    local compared=gear.preview{candidate=item,candidateFacts={},current=current,currentFacts=equipped,
        context={classId=player.classId},options={allow_unknown_enhancements=true}}
    if type(compared)~="table" or type(compared.upgrade)~="boolean" or type(compared.scoreGain)~="number" or
        compared.scoreGain~=compared.scoreGain or compared.scoreGain-compared.scoreGain~=0 or
        type(compared.reason)~="string" or #compared.reason>128 then return "unknown" end
    if compared.upgrade then return "useful" end
    if compared.reason=="" or compared.reason=="comparison_not_an_upgrade" then return "not_useful" end
    return "unknown"
end
local function offer_valid(o,visit)
    return type(o)=="table" and decimal(o.rollGuid) and decimal(o.revision) and o.visitId==visit and
        uint(o.mapId or 0,UINT32) and uint(o.lootSlot or 0,UINT32) and uint(o.itemEntry,UINT32) and o.itemEntry>0 and
        uint(o.randomSuffix or 0,UINT32) and uint(o.randomProperty or 0,UINT32) and uint(o.itemCount,UINT32) and o.itemCount>0 and
        uint(o.offeredMask,15) and millis(o.remainingMs)~=nil
end
local function key(o) return o.visitId..":"..o.rollGuid..":"..o.revision end
local function earlier(a,b)
    local am,bm=millis(a.remainingMs),millis(b.remainingMs)
    if am~=bm then return am<bm end
    if #a.rollGuid~=#b.rollGuid then return #a.rollGuid<#b.rollGuid end
    return a.rollGuid<b.rollGuid
end
function M.on_event(event)
    if event.kind=="loot_roll_observation" then
        observation,sequence=nil,nil
        if decimal(event.sequence) then observation,sequence=event.value,event.sequence end
    end
    return true
end
function M.decide(frame,gear)
    proposed=nil
    local input=frame.input or {}
    if input.kind~="loot_roll" or observation==nil or input.observationSequence~=sequence then return {} end
    local view=observation
    observation,sequence=nil,nil
    if type(view)~="table" or type(view.sessionId)~="string" or #view.sessionId<1 or #view.sessionId>128 or
        frame.session_id~=view.sessionId or not decimal(view.selfGuid) or not decimal(view.visitId) or
        not list(view.offers,16) or not list(view.metadata or {},35) then return {} end
    if state.sessionId and (state.sessionId~=view.sessionId or state.selfGuid~=view.selfGuid) then return {} end
    local templates,seen={},{}
    for _,item in ipairs(view.metadata or {}) do
        if type(item)~="table" or not uint(item.itemId,UINT32) or item.itemId==0 or templates[tostring(item.itemId)] then return {} end
        templates[tostring(item.itemId)]=item
    end
    local ordered={}
    for _,o in ipairs(view.offers) do
        if not offer_valid(o,view.visitId) or seen[o.rollGuid] then return {} end
        seen[o.rollGuid]=true
        if o.available==true and o.phase=="pending" then
            local at=#ordered+1
            while at>1 and earlier(o,ordered[at-1]) do ordered[at]=ordered[at-1];at=at-1 end
            ordered[at]=o
        end
    end
    local count=0
    for _ in pairs(state.reservations) do count=count+1 end
    if count>=256 then return {} end
    for _,o in ipairs(ordered) do
        if millis(o.remainingMs)>0 and not state.reservations[key(o)] then
            local result=classification(view,o,templates,gear)
            local choice
            if result=="useful" and bit(o.offeredMask,1) then choice="need"
            elseif result=="not_useful" and view.selfGuid==M.tuning.disenchanter_guid and bit(o.offeredMask,3) then choice="disenchant"
            elseif (result=="not_useful" or millis(o.remainingMs)<=M.tuning.pass_below_ms) and bit(o.offeredMask,0) then choice="pass" end
            if choice then
                local action={capability="loot_roll",sessionId=view.sessionId,selfGuid=view.selfGuid,visitId=view.visitId,
                    rollGuid=o.rollGuid,offerRevision=o.revision,choice=choice,lootSlot=o.lootSlot or 0,
                    mapId=o.mapId or 0,itemEntry=o.itemEntry,randomSuffix=o.randomSuffix or 0,
                    randomProperty=o.randomProperty or 0,count=o.itemCount,offeredMask=o.offeredMask}
                proposed={action=action,key=key(o),choice=choice,sessionId=view.sessionId,selfGuid=view.selfGuid}
                return {action=action}
            end
        end
    end
    return {}
end
-- Only the final scheduler winner reserves a vote. A cancellation, loot
-- close or interaction release can replace this proposal before transport.
function M.commit(action)
    if proposed and action==proposed.action then
        state.sessionId,state.selfGuid=proposed.sessionId,proposed.selfGuid
        state.reservations[proposed.key]=proposed.choice
    end
    proposed=nil
end
function M.export_state() return {version=1,data=state} end
function M.import_state(checkpoint)
    proposed=nil
    if type(checkpoint)~="table" or checkpoint.version~=1 or type(checkpoint.data)~="table" or
        type(checkpoint.data.reservations)~="table" then return false end
    local saved=checkpoint.data
    if saved.sessionId~=nil and (type(saved.sessionId)~="string" or #saved.sessionId<1 or #saved.sessionId>128 or not decimal(saved.selfGuid)) then return false end
    local count=0
    for k,v in pairs(saved.reservations) do
        if type(k)~="string" or #k<5 or #k>62 or (v~="need" and v~="pass" and v~="disenchant") then return false end
        count=count+1
    end
    if count>256 or count>0 and not saved.sessionId then return false end
    state=saved;observation,sequence=nil,nil
    return true
end
return M
