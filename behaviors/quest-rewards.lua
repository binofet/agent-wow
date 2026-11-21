-- Preferences over the exact current offer. Equipment suitability is based on
-- supplied character facts and learned weapon proficiencies, without equipping.
local uint32 = 4294967295
local races = {[1]=true,[2]=true,[3]=true,[4]=true,[5]=true,[6]=true,[7]=true,[8]=true,[10]=true,[11]=true}
local alliance = {[1]=true,[3]=true,[4]=true,[7]=true,[11]=true}
local body_slots = {[1]=true,[3]=true,[5]=true,[6]=true,[7]=true,[8]=true,[9]=true,[10]=true,[20]=true}
local common_slots = {[2]=true,[11]=true,[12]=true,[16]=true}
local weapon_slots = {[13]=true,[15]=true,[17]=true,[21]=true,[25]=true,[26]=true}
local caster_stats = {[0]=.02,[1]=.01,[5]=4,[6]=3,[7]=2,[18]=3,[21]=3,[30]=3,[31]=3,[32]=3,[36]=3,[41]=2,[42]=5,[43]=3,[45]=5,[46]=1,[47]=1}
local rogue_stats = {[1]=.01,[3]=4,[4]=2,[7]=2,[13]=1,[16]=3,[19]=3,[28]=3,[31]=3,[32]=3,[36]=3,[37]=3,[38]=2,[44]=2,[46]=1}
local uint_fields = {
    "itemClass","subclass","inventoryType","quality","itemLevel","requiredLevel","buyPrice","sellPrice",
    "allowableClass","allowableRace","requiredSkill","requiredSkillRank","requiredSpell","armor","delayMs",
    "flags","flags2","requiredReputationFaction","requiredReputationRank","requiredHonorRank","requiredCityRank",
}
local prerequisites = {"requiredSkill","requiredSkillRank","requiredSpell","requiredReputationFaction","requiredReputationRank","requiredHonorRank","requiredCityRank"}
local preferences = {
    [18]={[5]=5580,[8]=1159,[4]=2224},
    [21]={[5]=11192,[8]=11192,[4]=2186},
    [33]={[5]=80,[8]=80,[4]=6070},
}

local function integer(value, maximum)
    return type(value)=="number" and value>=0 and value<=maximum and value%1==0
end
local function bit(value, power)
    return math.floor(value/power)%2==1
end
local function zero(value)
    if value==nil then return 0 end
    return value
end
local function array(value, maximum)
    if type(value)~="table" or #value>maximum then return false end
    local count=0
    for key in pairs(value) do
        if not integer(key,maximum) or key<1 then return false end
        count=count+1
    end
    return count==#value
end
local function result(status, reason)
    return {status=status,reason=reason}
end
local function damage_value(damage, field)
    local value,decimal=damage[field],damage[field.."Decimal"]
    if decimal~=nil then
        if value~=nil or type(decimal)~="string" or #decimal>512 then return end
        value=tonumber(decimal)
        if value==nil then return end
    end
    if value==nil then value=0 end
    if type(value)~="number" or value-value~=0 then return end
    return value
end
local function add_sum(total, correction, term)
    local next_total=total+term
    if next_total-next_total~=0 then return next_total,0 end
    if math.abs(total)>=math.abs(term) then
        correction=correction+((total-next_total)+term)
    else
        correction=correction+((term-next_total)+total)
    end
    return next_total,correction
end
local function rows(items)
    if type(items)~="table" then return end
    if items.items~=nil then items=items.items end
    if type(items)~="table" then return end
    local out,count={},0
    for key,item in pairs(items) do
        count=count+1
        if count>128 or type(item)~="table" or not integer(item.itemId,2147483647) or item.itemId==0
           or out[tostring(item.itemId)] or type(key)=="string" and key~=tostring(item.itemId)
           or type(key)=="number" and not integer(key,128) then return end
        out[tostring(item.itemId)]=item
    end
    return out
end
local function complete_item(item)
    for _,field in ipairs(uint_fields) do
        if not integer(zero(item[field]),uint32) then return false end
    end
    local stats,damages=item.stats,item.damages
    if stats==nil then stats={} end
    if damages==nil then damages={} end
    if not array(stats,10) or not array(damages,2) then return false end
    for _,stat in ipairs(stats) do
        if type(stat)~="table" or not integer(zero(stat.type),uint32) or
           type(zero(stat.value))~="number" or zero(stat.value)%1~=0 or
           zero(stat.value)<-2147483648 or zero(stat.value)>2147483647 then return false end
    end
    for _,damage in ipairs(damages) do
        if type(damage)~="table" or not integer(zero(damage.school),6) then return false end
        local low,high=damage_value(damage,"minimum"),damage_value(damage,"maximum")
        if low==nil or high==nil or not (low>=0 and low<=high) then return false end
    end
    return true
end
local function requirements_met(item,input)
    if not bit(item.allowableClass or 0,2^(input.classId-1)) then return false end
    local mask=item.allowableRace or 0
    if input.raceId==nil then
        for race in pairs(races) do if not bit(mask,2^(race-1)) then return false end end
    elseif not bit(mask,2^(input.raceId-1)) then return false end
    local flags=item.flags2 or 0
    if bit(flags,1) or bit(flags,2) then
        if input.raceId==nil or bit(flags,1) and alliance[input.raceId] or bit(flags,2) and not alliance[input.raceId] then return false end
    end
    local level=item.requiredLevel or 0
    if level>0 and (input.level==nil or input.level<level) then return false end
    for _,field in ipairs(prerequisites) do if (item[field] or 0)~=0 then return false end end
    return not bit(item.flags or 0,16)
end
local function useful_score(item,input,proficiencies)
    if not requirements_met(item,input) then return end
    local class,subclass,slot=item.itemClass or 0,item.subclass or 0,item.inventoryType or 0
    local rogue=input.classId==4
    local weapon=class==2 and weapon_slots[slot] and proficiencies[subclass]
    if class==4 then
        if common_slots[slot] then
            if subclass~=0 and subclass~=1 and not (rogue and subclass==2) then return end
        elseif body_slots[slot] then
            if subclass~=1 and not (rogue and subclass==2) then return end
        elseif not (slot==23 and (input.classId==5 or input.classId==8) and (subclass==0 or subclass==1)) then return end
    elseif not weapon then return end
    local weights=rogue and rogue_stats or caster_stats
    local score=10+(item.armor or 0)*.15
    -- Match the existing subtotal: integer weights accumulate exactly before
    -- the first fractional term starts compensated floating-point addition.
    local stats,correction,floating=0,0,false
    for _,stat in ipairs(item.stats or {}) do
        local weight=weights[stat.type or 0] or 0
        local term=weight*(stat.value or 0)
        if floating then
            stats,correction=add_sum(stats,correction,term)
        else
            stats=stats+term
            floating=weight==.01 or weight==.02
        end
    end
    score=score+(stats+correction)
    if weapon and (item.delayMs or 0)>0 then
        local amount,damage_correction=0,0
        for _,damage in ipairs(item.damages or {}) do
            amount,damage_correction=add_sum(amount,damage_correction,
                (damage_value(damage,"minimum")+damage_value(damage,"maximum"))/2)
        end
        score=score+(amount+damage_correction)*1000/item.delayMs*(rogue and 4 or 1)
    end
    return score
end

-- uint32 price times uint32 count may exceed Lua's exact integer range.
-- Base-65536 limbs retain both halves for comparison, including one-copper ties.
local function vendor_value(price,count)
    local low=(price%65536)*(count%65536)
    local middle=math.floor(price/65536)*(count%65536)+(price%65536)*math.floor(count/65536)+math.floor(low/65536)
    return math.floor(price/65536)*math.floor(count/65536)+math.floor(middle/65536),
           (middle%65536)*65536+low%65536
end
local function better_vendor(high,low,best)
    return high>best.high or high==best.high and low>best.low
end
local function preferred_choice(choices,quest_id,class_id)
    local preferred=(preferences[quest_id] or {})[class_id]
    local selected
    for position,choice in ipairs(choices) do
        if choice.itemId==preferred then
            if selected then return result("invalid","reward_choices_invalid") end
            selected={status="selected",position=position,itemId=choice.itemId,choiceIndex=zero(choice.index),reason="quest_preference"}
        end
    end
    return selected or result("pending","reward_preference_unavailable")
end
local function decimal(value)
    if type(value)~="string" or #value==0 or #value>20 or #value>1 and byte_at(value,1)==48 or
       #value==20 and value>"18446744073709551615" then return false end
    for i=1,#value do local c=byte_at(value,i); if c<48 or c>57 then return false end end
    return true
end
local function wire_uint(value)
    return integer(value,9007199254740991) or decimal(value)
end
local function matches_uint(value,expected) return value==expected or value==tostring(expected) end
local function starter_handoff(input)
    local function ready(choice) return {status="ready",choice=choice} end
    local function invalid() return result("invalid","starter_reward_facts_invalid") end
    local entries={[783]=197,[5261]=196,[33]=196}
    if not integer(input.questId,uint32) or not array(input.ownQuestIds,25) then return invalid() end
    local active=false
    for _,q in ipairs(input.ownQuestIds) do
        if not integer(q,uint32) then return invalid() end
        if q==input.questId then active=true end
    end
    local entry=entries[input.questId]
    if not active or not entry then return ready({kind="ignore"}) end
    if type(input.sessionId)~="string" or #input.sessionId==0 or #input.sessionId>128 or
       type(input.now)~="number" or input.now-input.now~=0 or type(input.autonomous)~="boolean" or
       not array(input.candidates,64) then return invalid() end
    if input.claimReservation~=nil and input.claimReservation~=false then
        if not array(input.claimReservation,3) or #input.claimReservation~=3 then return invalid() end
        for _,part in ipairs(input.claimReservation) do
            if type(part)~="string" or #part>128 then return invalid() end
        end
    end
    local giver
    for _,c in ipairs(input.candidates) do
        if type(c)~="table" or not integer(c.priority,uint32) or type(c.distance)~="number" or
           not (c.distance>=0 and c.distance<=9007199254740991) or not decimal(c.guid) or
           not integer(c.entry,uint32) then return invalid() end
        if c.entry==entry and (not giver or c.priority<giver.priority or c.priority==giver.priority and
           (c.distance<giver.distance or c.distance==giver.distance and
           (#c.guid<#giver.guid or #c.guid==#giver.guid and c.guid<giver.guid))) then giver=c end
    end
    if not giver then
        if input.autonomous then return ready({kind="travel",entry=entry}) end
        return ready({kind="wait",phase="waiting_for_observed_prerequisite_reward_giver"})
    end
    local offer=input.turnInOffer
    local offered=false
    if type(offer)=="table" then
        if not wire_uint(zero(offer.questId)) then return invalid() end
        if matches_uint(zero(offer.questId),input.questId) then
            if not decimal(offer.npcGuid) then return invalid() end
            if offer.npcGuid==giver.guid and offer.source=="reward_offer" and offer.canComplete==true then
                if not decimal(offer.expiresInMs) then return invalid() end
                offered=offer.expiresInMs~="0" and offer.rewardRequested~=true
            end
        end
    end
    if offered then
        local choices=offer.choices
        if not array(choices,6) then return result("invalid","reward_choices_invalid") end
        local indices={}
        for _,c in ipairs(choices) do
            if type(c)~="table" or not integer(c.itemId,uint32) or c.itemId==0 or not integer(c.count,uint32) or
               c.count==0 or not integer(zero(c.index),5) or indices[zero(c.index)] then
                return result("invalid","reward_choices_invalid")
            end
            indices[zero(c.index)]=true
        end
        local position=#choices==0 and 0 or 1
        if #choices>1 then
            local class=0
            if input.classKnown==true then
                if not wire_uint(input.classId) then return invalid() end
                class=type(input.classId)=="string" and tonumber(input.classId) or input.classId
            end
            local preferred=preferred_choice(choices,input.questId,class)
            if preferred.status~="selected" then
                return ready({kind="wait",phase="waiting_for_verified_prerequisite_reward_choice"})
            end
            position=preferred.position
        end
        if not decimal(offer.revision) or offer.revision=="0" then return result("invalid","reward_offer_revision_unknown") end
        local bound=input.claimReservation
        local used=type(bound)=="table" and #bound==3 and bound[1]==input.sessionId and bound[2]==giver.guid and bound[3]==offer.revision
        if not used then
            local choice=choices[position]
            return ready({kind="claim_reward",questId=input.questId,npcGuid=giver.guid,offerRevision=offer.revision,
                choicePosition=position,choiceIndex=choice and zero(choice.index) or 0,itemId=choice and choice.itemId or 0})
        end
    end
    local last=input.lastMutation
    if last==nil then last={} end
    if type(last)~="table" then return invalid() end
    if last.method=="buddy-quests.turn_in" then
        if not wire_uint(zero(last.questId)) then return invalid() end
        if matches_uint(zero(last.questId),input.questId) then
            local at=zero(last.at)
            if type(at)~="number" or at-at~=0 then return invalid() end
            if input.now-at<60 then return ready({kind="wait",phase="waiting_for_prerequisite_turn_in_response"}) end
        end
    end
    if not decimal(input.questLogRevision) then return invalid() end
    return ready({kind="turn_in",questId=input.questId,npcGuid=giver.guid})
end
local function decide(input)
    if input.mode=="starter_handoff" then return starter_handoff(input) end
    local choices=input.choices
    if not array(choices,6) or #choices<1 then return result("invalid","reward_choices_invalid") end
    if input.mode=="offer_preference" then
        for position,choice in ipairs(choices) do
            if type(choice)~="table" or not integer(choice.itemId,2147483647) or choice.itemId==0 then
                return result("invalid","reward_choices_invalid")
            end
        end
        return preferred_choice(choices,input.questId,input.classId)
    end
    local indices,ids={},{}
    for _,choice in ipairs(choices) do
        if type(choice)~="table" then return result("invalid","reward_choices_invalid") end
        local index,id,count=zero(choice.index),choice.itemId,choice.count
        if not integer(index,5) or not integer(id,2147483647) or id==0 or not integer(count,uint32) or count==0
           or indices[index] or ids[tostring(id)] then return result("invalid","reward_choices_invalid") end
        indices[index],ids[tostring(id)]=true,true
    end
    local metadata=rows(input.items)
    if not metadata then return result("invalid","reward_metadata_invalid") end
    local vendor_only=input.mode=="vendor_value"
    if not vendor_only and input.mode~="useful" then return result("invalid","reward_mode_invalid") end
    for _,choice in ipairs(choices) do
        local item=metadata[tostring(choice.itemId)]
        if not item or item.available~=true or item.pending~=nil and item.pending~=false then
            return result("pending","reward_choice_metadata_unknown")
        end
        if vendor_only then
            if item.sellPriceKnown~=true or not integer(item.sellPrice,1000000000) or choice.count>1000 then
                return result("pending","reward_choice_metadata_unknown")
            end
        elseif not complete_item(item) then return result("invalid","reward_metadata_invalid") end
    end
    local proficiencies={}
    if not vendor_only then
        if input.classId~=4 and input.classId~=5 and input.classId~=8 then return result("pending","reward_class_unknown") end
        if input.level~=nil and (not integer(input.level,255) or input.level==0) then return result("invalid","reward_level_invalid") end
        if input.raceId~=nil and not races[input.raceId] then return result("invalid","reward_race_invalid") end
        if not array(input.weaponSubclasses,21) then return result("invalid","reward_proficiencies_invalid") end
        for _,subclass in ipairs(input.weaponSubclasses) do
            if not integer(subclass,20) then return result("invalid","reward_proficiencies_invalid") end
            proficiencies[subclass]=true
        end
    end
    local best
    for position,choice in ipairs(choices) do
        local item=metadata[tostring(choice.itemId)]
        local score=not vendor_only and useful_score(item,input,proficiencies) or nil
        if score==nil or score<=0 then score=0 end
        local high,low=vendor_value(item.sellPrice or 0,choice.count)
        local better=not best or score>best.score
        if best and score==best.score then
            if better_vendor(high,low,best) then better=true
            elseif high==best.high and low==best.low then
                -- Keep existing explicit vendor-only catalogs' offer-index tie.
                if vendor_only then better=(choice.index or 0)<(best.choice.index or 0)
                else better=choice.itemId<best.choice.itemId or choice.itemId==best.choice.itemId and (choice.index or 0)<(best.choice.index or 0) end
            end
        end
        if better then best={position=position,choice=choice,score=score,high=high,low=low} end
    end
    local out={status="selected",position=best.position,itemId=best.choice.itemId,choiceIndex=best.choice.index or 0,
               reason=vendor_only and "highest_verified_vendor_value" or best.score>0 and "useful" or "vendor",
               valueHigh=best.high,valueLow=best.low}
    if best.high<2097152 then out.vendorValueCopper=best.high*4294967296+best.low end
    return out
end
return {decide=decide}
