-- Classify observed static templates for disposal policy. Eligibility here
-- establishes no item ownership, exact operation authority or game effect.
local UINT32=4294967295
local base_fields={"itemClass","subclass","inventoryType","quality","flags","flags2","sellPrice"}
local requirements={"requiredSkill","requiredSkillRank","requiredSpell","requiredHonorRank",
    "requiredCityRank","requiredReputationFaction","requiredReputationRank"}
local raw_fields={"containerSlots","bonding","pageText","startQuest","lockId","maxDurability","bagFamily","duration"}
local ordinary_raw={"containerSlots","bonding","pageText","startQuest","lockId","bagFamily","duration"}
local armor_slots={[1]=true,[3]=true,[5]=true,[6]=true,[7]=true,[8]=true,[9]=true,[10]=true,[16]=true,[20]=true}
local equipment_slots={[1]=0,[3]=2,[5]=4,[6]=5,[7]=6,[8]=7,[9]=8,[10]=9,[16]=14,[20]=4}

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

local function array(value,limit)
    if type(value)~="table" then return false end
    local count=0
    for key in pairs(value) do
        if not uint(key) or key<1 or key>limit then return false end
        count=count+1
    end
    if count~= #value or count>limit then return false end
    for i=1,count do if value[i]==nil then return false end end
    return true
end

local decimal_syntax={}
local function decimal(value,allow_zero)
    if type(value)~="string" or #value==0 or #value>20 then return false end
    if value=="0" then return allow_zero==true end
    if decimal_syntax[value] then return true end
    if byte_at(value,1)==48 then return false end
    for i=1,#value do
        local b=byte_at(value,i)
        if b<48 or b>57 then return false end
    end
    if #value==20 and value>"18446744073709551615" then return false end
    decimal_syntax[value]=true
    return true
end

local function guid(value)
    if value==nil then return "0" end
    if value=="0" then return value end
    if decimal(value) and #value==19 and value>="4611686018427387905" and value<="4611967493404098559" then return value end
end

local function session(value)
    if type(value)~="string" or #value==0 or #value>128 then return false end
    for i=1,#value do
        local b=byte_at(value,i)
        if not (b>=48 and b<=57 or b>=65 and b<=90 or b>=97 and b<=122 or b==45 or b==46 or b==95) then return false end
    end
    return true
end

local function boolean(value)
    return value==nil or type(value)=="boolean"
end

local function reference(value)
    return object(value) and uint(zero(value.bag)) and zero(value.bag)<=4 and
        uint(zero(value.slot)) and zero(value.slot)<=35 and value.itemGuid~="0" and guid(value.itemGuid)==value.itemGuid and
        uint(value.entry) and value.entry>0 and uint(value.stackCount) and value.stackCount>0
end

local function same_reference(a,b)
    return type(a)=="table" and type(b)=="table" and zero(a.bag)==zero(b.bag) and zero(a.slot)==zero(b.slot) and
        a.itemGuid==b.itemGuid and a.entry==b.entry and a.stackCount==b.stackCount
end

local function instance(value)
    return type(value)=="table" and boolean(value.enhancementsKnown) and boolean(value.hasEnhancements) and
        boolean(value.durabilityKnown) and uint(zero(value.durability)) and uint(zero(value.maxDurability)) and
        (value.durabilityKnown~=true or zero(value.durability)<=zero(value.maxDurability))
end

local function same_instance(a,b)
    for _,key in ipairs({"enhancementsKnown","hasEnhancements","durabilityKnown"}) do
        if (a[key]==true)~=(b[key]==true) then return false end
    end
    return zero(a.durability)==zero(b.durability) and zero(a.maxDurability)==zero(b.maxDurability)
end

local function matching_equipment(inventory,world)
    if inventory.equipmentKnown~=true or world.selfEquipmentKnown~=true or
        not decimal(inventory.equipmentRevision) or inventory.equipmentRevision~=world.selfEquipmentRevision or
        not array(inventory.equipment,19) or #inventory.equipment~=19 then return end
    local rows={}
    for _,row in ipairs(inventory.equipment) do
        if type(row)~="table" or row.slotKnown~=true or not uint(zero(row.slot)) or zero(row.slot)>18 or
            rows[zero(row.slot)] then return end
        rows[zero(row.slot)]=row
    end
    return rows
end

local function quest_protection(world,journal,entry)
    if world.selfQuestLogKnown~=true or not decimal(world.selfQuestLogRevision) or
        world.selfQuestLogRevision~=journal.questLogRevision or not array(world.selfQuestLog,25) or
        #world.selfQuestLog~=25 or not array(journal.entries or {},25) then return end
    -- uint32 quest IDs must not expand the VM's dense numeric table storage.
    local slots,active,covered={},{},{}
    for _,row in ipairs(world.selfQuestLog) do
        if type(row)~="table" or not uint(zero(row.slot)) or zero(row.slot)>24 or slots[zero(row.slot)] or
            not uint(zero(row.questId)) then return end
        slots[zero(row.slot)]=true
        if zero(row.questId)~=0 then
            local id=tostring(row.questId)
            if active[id] then return end
            active[id]=zero(row.slot)
        end
    end
    local protected=false
    for _,row in ipairs(journal.entries or {}) do
        if type(row)~="table" or not uint(row.questId) or row.questId==0 then return end
        local id=tostring(row.questId)
        if active[id]==nil or covered[id] or not uint(zero(row.slot)) or zero(row.slot)~=active[id] or
            row.logStateKnown~=true or row.templateKnown~=true or row.itemProtectionKnown~=true or
            row.failure~=nil and row.failure~="" or not array(row.targets or {},20) then return end
        covered[id]=true
        for _,target in ipairs(row.targets or {}) do
            if type(target)~="table" or type(target.kind)~="string" or #target.kind==0 or #target.kind>32 or
                type(zero(target.targetId))~="number" or zero(target.targetId)~=math.floor(zero(target.targetId)) or
                zero(target.targetId)< -2147483648 or zero(target.targetId)>2147483647 or not uint(zero(target.requiredCount)) then return end
            if target.kind=="ITEM" or target.kind=="SOURCE_ITEM" then
                if zero(target.targetId)<=0 then return end
                if target.targetId==entry then protected=true end
            end
        end
    end
    for id in pairs(active) do if not covered[id] then return end end
    return protected
end

local function stats_valid(rows)
    if not array(rows or {},10) then return false end
    for _,row in ipairs(rows or {}) do
        if not object(row) or not uint(zero(row.type)) or type(zero(row.value))~="number" or
            zero(row.value)~=math.floor(zero(row.value)) or zero(row.value)< -2147483648 or zero(row.value)>2147483647 then return false end
    end
    return true
end

-- Gray armor needs a factual projection of Inventory.State, World and Journal.
-- The caller validates the full five-bag layout and both full equipment frames,
-- then retains carriedSource, one equipment frame and facts.inventoryLayoutKnown
-- / equipmentFramesMatch. These attest shape/identity consistency, not fullness
-- or disposal eligibility. Lua owns protection, retention and worn-slot choice.
-- The fourth argument is the actual shared gear module, composed by the caller.
local function gray_armor(item,context,gear)
    decimal_syntax={}
    if type(context)~="table" then return result("unknown","armor_context_required") end
    if not object(gear) or type(gear.compare)~="function" then return result("unknown","shared_armor_comparison_required") end
    if not object(item) or item.available~=true or item.pending~=nil and type(item.pending)~="boolean" or item.pending==true then
        return result("unknown","complete_item_template_required")
    end
    if not uint(item.itemId) or item.itemId==0 or item.itemId>=2147483648 or not uint_fields(item,base_fields) or
        not uint_fields(item,requirements) or not uint(zero(item.armor)) or not boolean(item.hasSpell) or
        not boolean(item.armorComparisonKnown) or not stats_valid(item.stats) then return result("unknown","item_template_fields_invalid") end
    if not object(item.rawTemplateFacts) then return result("unknown","raw_template_facts_required") end
    if not uint_fields(item.rawTemplateFacts,raw_fields) or zero(item.rawTemplateFacts.bonding)>5 then return result("unknown","raw_template_fields_invalid") end
    if item.armorComparisonKnown~=true then return result("unknown","armor_comparison_facts_required") end
    -- Preserve grayArmorJunk's separate template scope: raw facts are known,
    -- but vendor/misc raw-zero rules and RequiredLevel do not govern this mode.
    if zero(item.itemClass)~=4 or zero(item.subclass)<1 or zero(item.subclass)>4 or not armor_slots[zero(item.inventoryType)] or
        zero(item.quality)~=0 or zero(item.sellPrice)==0 or zero(item.flags)~=0 or zero(item.flags2)~=0 or item.hasSpell==true or
        #(item.stats or {})~=0 or not all_zero(item,requirements) then return result("keep","unsupported_gray_armor_template") end
    local inventory,world,journal,source=context.inventory,context.world,context.journal,context.source
    if type(inventory)~="table" or type(world)~="table" or type(journal)~="table" or not reference(source) or
        source.entry~=item.itemId or source.stackCount~=1 or not session(inventory.sessionId) or
        inventory.sessionId~=journal.sessionId or not decimal(inventory.selfGuid) or type(world.self)~="table" or
        world.self.present~=true or world.self.guid~=inventory.selfGuid or inventory.inventoryKnown~=true or
        world.inventoryKnown~=true or not decimal(inventory.inventoryRevision) or inventory.inventoryRevision~=world.inventoryRevision then
        return result("unknown","armor_identity_or_inventory_context_changed")
    end
    if type(context.facts)~="table" or context.facts.inventoryLayoutKnown~=true or context.facts.equipmentFramesMatch~=true then
        return result("unknown","checked_inventory_and_equipment_context_required")
    end
    local carried=context.carriedSource
    if not same_reference(carried,source) or carried.metadataKnown~=true or not instance(carried) then
        return result("unknown","exact_owned_armor_source_required")
    end
    if not array(inventory.items or {},160) then return result("unknown","owned_armor_facts_required") end
    local facts
    for _,row in ipairs(inventory.items or {}) do
        if type(row)~="table" or type(row.item)~="table" then return result("unknown","owned_armor_facts_invalid") end
        if same_reference(row.item,source) then
            if facts or row.metadataKnown~=true or not instance(row.gearFacts) then return result("unknown","owned_armor_facts_invalid") end
            facts=row.gearFacts
        end
    end
    if not facts or not same_instance(facts,carried) or facts.enhancementsKnown~=true then return result("unknown","owned_armor_enhancement_context_required") end
    if facts.hasEnhancements==true then return result("keep","enhanced_armor_retained") end
    local equipment=matching_equipment(inventory,world)
    if not equipment then return result("unknown","matching_full_equipment_context_required") end
    local protected=quest_protection(world,journal,item.itemId)
    if protected==nil then return result("unknown","complete_quest_item_protection_required") end
    if protected then return result("keep","active_quest_item_retained") end
    local worn=equipment[equipment_slots[item.inventoryType]]
    if not worn or guid(worn.itemGuid)=="0" or worn.itemGuid==source.itemGuid then return result("keep","worn_armor_replacement_required") end
    if not guid(worn.itemGuid) or worn.metadataKnown~=true or not uint(worn.entry) or worn.entry==0 or worn.stackCount~=1 then
        return result("unknown","usable_worn_armor_facts_required")
    end
    if not instance(worn) or worn.durabilityKnown~=true or zero(worn.maxDurability)>0 and zero(worn.durability)==0 or worn.enhancementsKnown~=true then
        return result("unknown","usable_worn_armor_facts_required")
    end
    if worn.hasEnhancements==true then return result("unknown","enhanced_worn_armor_comparison_unknown") end
    local templates=object(inventory.gearContext) and inventory.gearContext.templates
    local replacement=object(templates) and templates[tostring(worn.entry)]
    if not object(replacement) or replacement.available~=true or replacement.itemId~=worn.entry or replacement.itemClass~=4 or
        equipment_slots[replacement.inventoryType]~=equipment_slots[item.inventoryType] or replacement.armorComparisonKnown~=true or
        not uint(zero(replacement.armor)) or not uint(zero(replacement.flags2)) or not uint(zero(replacement.scalingDistribution)) or
        not uint(zero(replacement.scalingValue)) or not boolean(replacement.hasSpell) or not stats_valid(replacement.stats) then
        return result("unknown","complete_worn_armor_template_required")
    end
    if replacement.resistances~=nil then
        if not array(replacement.resistances,6) or #replacement.resistances~=6 then return result("unknown","worn_armor_resistances_unknown") end
        for _,value in ipairs(replacement.resistances) do if not uint(value) then return result("unknown","worn_armor_resistances_unknown") end end
    end
    local discardFacts={itemGuid=source.itemGuid,enhancementsKnown=facts.enhancementsKnown,hasEnhancements=facts.hasEnhancements,
        durabilityKnown=facts.durabilityKnown,durability=zero(facts.durability),maxDurability=zero(facts.maxDurability)}
    local upgrade,gain,reason=gear.compare(replacement,worn,item,discardFacts,{minimum_score_gain=-1,
        allow_enhanced_comparison=false,allow_unknown_enhancements=false,allow_special_templates=false,require_usable_durability=true})
    if type(upgrade)~="boolean" or type(gain)~="number" or gain~=gain or gain-gain~=0 or type(reason)~="string" or #reason>128 then
        return result("unknown","shared_armor_comparison_invalid")
    end
    if upgrade and gain>=0 and reason=="" then return result("eligible","known_worn_armor_dominates") end
    if reason=="" or reason=="comparison_not_an_upgrade" then return result("keep","worn_armor_does_not_dominate") end
    return result("unknown","shared_armor_comparison_unavailable")
end

local function classify(mode,item,context,gear)
    if mode=="gray_armor_destroy" then return gray_armor(item,context,gear) end
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
        return classify(input.mode,input.item,input.context,input.gear)
    end,
}
