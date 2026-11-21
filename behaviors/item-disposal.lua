-- Classify observed static templates for disposal policy. Eligibility here
-- establishes no item ownership, exact operation authority or game effect.
local UINT32=4294967295
local base_fields={"itemClass","subclass","inventoryType","quality","flags","flags2","sellPrice"}
local requirements={"requiredSkill","requiredSkillRank","requiredSpell","requiredHonorRank",
    "requiredCityRank","requiredReputationFaction","requiredReputationRank"}
local raw_fields={"containerSlots","bonding","pageText","startQuest","lockId","maxDurability","bagFamily","duration"}
local ordinary_raw={"containerSlots","bonding","pageText","startQuest","lockId","bagFamily","duration"}
local armor_slots={[1]=true,[3]=true,[5]=true,[6]=true,[7]=true,[8]=true,[9]=true,[10]=true,[16]=true,[20]=true}

local function zero(value)
    if value==nil then return 0 end
    return value
end

local function uint(value)
    return type(value)=="number" and value>=0 and value<=UINT32 and value==math.floor(value)
end

local function object(value)
    if type(value)~="table" then return false end
    for key in pairs(value) do if type(key)~="string" then return false end end
    return true
end

local function uint_fields(value,fields)
    for _,field in ipairs(fields) do if not uint(zero(value[field])) then return false end end
    return true
end

local function all_zero(value,fields)
    for _,field in ipairs(fields) do if zero(value[field])~=0 then return false end end
    return true
end

local function result(status,reason)
    return {status=status,reason=reason}
end

local function classify(mode,item)
    if mode=="gray_armor_destroy" then return result("unknown","armor_context_required") end
    if mode~="vendor_sale" and mode~="misc_destroy" then return result("unknown","disposal_mode_unknown") end
    if not object(item) or item.available~=true or
        item.pending~=nil and type(item.pending)~="boolean" or item.pending==true then
        return result("unknown","complete_item_template_required")
    end
    if not uint(item.itemId) or item.itemId==0 or item.itemId>=2147483648 or
        not uint_fields(item,base_fields) or not uint_fields(item,requirements) or
        item.hasSpell~=nil and type(item.hasSpell)~="boolean" then
        return result("unknown","item_template_fields_invalid")
    end
    local raw=item.rawTemplateFacts
    if not object(raw) then return result("unknown","raw_template_facts_required") end
    if not uint_fields(raw,raw_fields) or zero(raw.bonding)>5 then
        return result("unknown","raw_template_fields_invalid")
    end
    if zero(item.quality)~=0 or zero(item.flags)~=0 or zero(item.flags2)~=0 or item.hasSpell==true or
        not all_zero(raw,ordinary_raw) then
        return result("keep","functional_or_non_gray_item")
    end
    if mode=="vendor_sale" and zero(item.sellPrice)==0 then return result("keep","positive_sale_price_required") end
    if zero(item.itemClass)==15 and zero(item.subclass)==0 and zero(item.inventoryType)==0 and zero(raw.maxDurability)==0 then
        return result("eligible","ordinary_gray_miscellaneous")
    end
    if mode=="vendor_sale" and zero(item.itemClass)==4 and zero(item.subclass)>=1 and zero(item.subclass)<=4 and
        armor_slots[zero(item.inventoryType)] and all_zero(item,requirements) then
        return result("eligible","ordinary_gray_armor")
    end
    return result("keep","unsupported_disposal_template")
end

return {
    classify=classify,
    decide=function(input)
        if type(input)~="table" then return result("unknown","disposal_input_invalid") end
        return classify(input.mode,input.item)
    end,
}
