-- Select among observed eligible sale stacks and actual in-range vendors.
-- Native execution binds the exact source, vendor, revisions and wallet proof.
local UINT32=4294967295
local function integer(value, minimum, maximum)
    return type(value)=="number" and value>=minimum and value<=maximum and value==math.floor(value)
end
local function decimal(value, allow_zero)
    if type(value)~="string" or #value<1 or #value>20 then return false end
    if value=="0" then return allow_zero==true end
    if byte_at(value,1)==48 then return false end
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
local function less_guid(a,b)
    return #a<#b or #a==#b and a<b
end
local input_fields={sessionId=true,selfGuid=true,revision=true,inventoryRevision=true,
    copperKnown=true,copper=true,copperRevision=true,candidates=true,vendors=true}
local source_fields={bag=true,slot=true,itemGuid=true,entry=true,stackCount=true}
local candidate_fields={source=true,unitSellPriceCopper=true}
local vendor_fields={guid=true,distance=true}

local function choose(input)
    assert(fields(input,input_fields) and token(input.sessionId) and decimal(input.selfGuid) and
        decimal(input.revision) and decimal(input.inventoryRevision) and type(input.copperKnown)=="boolean" and
        integer(input.copper,0,UINT32) and decimal(input.copperRevision,true),"vendor_policy_identity_or_wallet_invalid")
    assert(array(input.candidates,160) and array(input.vendors,96),"vendor_policy_collection_invalid")
    local selected,vendor
    local locations,guids,vendor_guids={},{},{}
    for _,candidate in ipairs(input.candidates) do
        assert(fields(candidate,candidate_fields) and integer(candidate.unitSellPriceCopper,1,UINT32),
            "vendor_policy_candidate_invalid")
        local source=candidate.source
        assert(fields(source,source_fields) and integer(source.bag,0,4) and integer(source.slot,0,35) and
            decimal(source.itemGuid) and integer(source.entry,1,UINT32) and integer(source.stackCount,1,UINT32),
            "vendor_policy_source_invalid")
        local location=source.bag*36+source.slot
        assert(not locations[location] and not guids[source.itemGuid],"vendor_policy_duplicate_source")
        locations[location],guids[source.itemGuid]=true,true
        if selected==nil or source.bag<selected.source.bag or
            source.bag==selected.source.bag and source.slot<selected.source.slot then selected=candidate end
    end
    for _,candidate in ipairs(input.vendors) do
        assert(fields(candidate,vendor_fields) and decimal(candidate.guid) and type(candidate.distance)=="number" and
            candidate.distance>=0 and candidate.distance<=4.5 and not vendor_guids[candidate.guid],
            "vendor_policy_vendor_invalid")
        vendor_guids[candidate.guid]=true
        if vendor==nil or candidate.distance<vendor.distance or
            candidate.distance==vendor.distance and less_guid(candidate.guid,vendor.guid) then vendor=candidate end
    end
    local result={sessionId=input.sessionId,selfGuid=input.selfGuid,revision=input.revision,
        inventoryRevision=input.inventoryRevision,copperKnown=input.copperKnown,copper=input.copper,
        copperRevision=input.copperRevision}
    if vendor==nil then
        result.action,result.reason="blocked","no_actual_in_range_vendor_no_travel_attempted"
    elseif selected==nil then
        result.action,result.reason="complete","no_observed_safe_junk_sale"
    elseif not input.copperKnown or input.copperRevision=="0" then
        result.action,result.reason="blocked","actual_wallet_observation_required"
    else
        local source=selected.source
        result.action="sell"
        result.source={bag=source.bag,slot=source.slot,itemGuid=source.itemGuid,entry=source.entry,stackCount=source.stackCount}
        result.vendorGuid,result.unitSellPriceCopper=vendor.guid,selected.unitSellPriceCopper
    end
    return result
end
return {decide=choose}
