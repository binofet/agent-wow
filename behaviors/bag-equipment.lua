-- Choose among observed eligible bags. The host binds this proposal to the
-- current owned source, empty equipment slot and exact inventory revision.
local function integer(value, minimum, maximum)
    return type(value)=="number" and value>=minimum and value<=maximum and value==math.floor(value)
end

local function decimal(value)
    if type(value)~="string" or #value<1 or #value>20 or byte_at(value,1)==48 then return false end
    for i=1,#value do
        local b=byte_at(value,i)
        if b<48 or b>57 then return false end
    end
    return #value<20 or value<="18446744073709551615"
end

local function token(value)
    if type(value)~="string" or #value<1 or #value>128 then return false end
    for i=1,#value do
        local b=byte_at(value,i)
        if not (b>=48 and b<=57 or b>=65 and b<=90 or b>=97 and b<=122 or b==45 or b==46 or b==95) then return false end
    end
    return true
end

local function fields(value, allowed)
    if type(value)~="table" then return false end
    for key in pairs(value) do if not allowed[key] then return false end end
    return true
end

local function array(value, maximum)
    if type(value)~="table" or #value>maximum then return false end
    local count=0
    for key in pairs(value) do
        if not integer(key,1,#value) then return false end
        count=count+1
    end
    return count==#value
end

local input_fields={sessionId=true,selfGuid=true,inventoryRevision=true,candidates=true,emptyEquipmentSlots=true}
local candidate_fields={source=true,containerSlots=true}
local source_fields={bag=true,slot=true,itemGuid=true,entry=true,stackCount=true}

local function choose(input)
    assert(fields(input,input_fields) and token(input.sessionId) and decimal(input.selfGuid) and
        decimal(input.inventoryRevision),"bag_policy_identity_invalid")
    assert(array(input.candidates,160) and array(input.emptyEquipmentSlots,4),"bag_policy_collection_invalid")
    local selected,empty
    local locations,guids,slots={},{},{}
    for _,candidate in ipairs(input.candidates) do
        assert(fields(candidate,candidate_fields) and integer(candidate.containerSlots,1,36),"bag_policy_candidate_invalid")
        local source=candidate.source
        assert(fields(source,source_fields) and integer(source.bag,0,4) and integer(source.slot,0,35) and
            decimal(source.itemGuid) and integer(source.entry,1,4294967295) and source.stackCount==1,
            "bag_policy_source_invalid")
        local location=source.bag*36+source.slot
        assert(not locations[location] and not guids[source.itemGuid],"bag_policy_duplicate_source")
        locations[location],guids[source.itemGuid]=true,true
        if selected==nil or source.bag<selected.bag or source.bag==selected.bag and source.slot<selected.slot then
            selected=source
        end
    end
    for _,slot in ipairs(input.emptyEquipmentSlots) do
        assert(integer(slot,19,22) and not slots[slot],"bag_policy_empty_slot_invalid")
        slots[slot]=true
        if empty==nil or slot<empty then empty=slot end
    end
    if selected==nil or empty==nil then
        return {action="none",sessionId=input.sessionId,selfGuid=input.selfGuid,inventoryRevision=input.inventoryRevision,
            reason="no_owned_bag_for_empty_slot"}
    end
    return {action="equip",sessionId=input.sessionId,selfGuid=input.selfGuid,inventoryRevision=input.inventoryRevision,
        source={bag=selected.bag,slot=selected.slot,itemGuid=selected.itemGuid,entry=selected.entry,stackCount=selected.stackCount},
        equipmentSlot=empty}
end

return {decide=choose}
