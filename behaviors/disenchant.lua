-- Party comparisons choose one carried source. Native professions revalidates
-- the exact item and owns the destructive cast, material receipts and cleanup.
local UINT32 = 4294967295
local PARTY = {"1", "3", "4", "5", "8"}
local CLASSES = {["1"] = 5, ["3"] = 1, ["4"] = 8, ["5"] = 4, ["8"] = 9}
local ARMOR = {[1] = 4, [4] = 2, [5] = 1, [8] = 1, [9] = 1}
local PROFICIENCY = {[1] = "415", [2] = "414", [3] = "413", [4] = "293"}
local SLOTS = {[1] = 0, [3] = 2, [5] = 4, [20] = 4, [6] = 5,
  [7] = 6, [8] = 7, [9] = 8, [10] = 9, [16] = 14}

local function uint(value, maximum)
  return type(value) == "number" and value >= 0 and value <= maximum and value == math.floor(value)
end

local function decimal(value)
  if type(value) ~= "string" or #value == 0 or #value > 20 or byte_at(value, 1) == 48 then return false end
  for i = 1, #value do
    local b = byte_at(value, i)
    if b < 48 or b > 57 then return false end
  end
  return #value < 20 or value <= "18446744073709551615"
end

local function positive_revision(value)
  return decimal(value) or uint(value, 9007199254740991) and value > 0
end

local function array(value, maximum)
  if type(value) ~= "table" or #value > maximum then return false end
  local count = 0
  for key in pairs(value) do
    if not uint(key, #value) or key == 0 then return false end
    count = count + 1
  end
  return count == #value
end

local function ref(source)
  return type(source) == "table" and uint(source.bag or 0, 4) and uint(source.slot or 0, 35) and
    decimal(source.itemGuid) and uint(source.entry, UINT32) and source.entry > 0 and
    uint(source.stackCount, UINT32) and source.stackCount > 0
end

local function same_ref(a, b)
  return ref(a) and ref(b) and (a.bag or 0) == (b.bag or 0) and (a.slot or 0) == (b.slot or 0) and
    a.itemGuid == b.itemGuid and a.entry == b.entry and a.stackCount == b.stackCount
end

local function candidates(input)
  local choices, offset = {}, input.offset or 0
  if not array(input.sources, 160) or not uint(offset, 9007199254740983) then
    return {sources = {}, offset = 0, reason = "no_carried_candidate"}
  end
  local guids, slots = {}, {}
  for _, source in ipairs(input.sources) do
    if not ref(source) or guids[source.itemGuid] then
      return {sources = {}, offset = offset, reason = "owned_item_enhancement_context_required"}
    end
    guids[source.itemGuid] = true
    local key = (source.bag or 0) * 36 + (source.slot or 0)
    if slots[key] then return {sources = {}, offset = offset, reason = "owned_item_enhancement_context_required"} end
    slots[key] = source
  end
  -- Logical bags and slot addresses give a fixed bounded ordering.
  for key = 0, 179 do
    local source = slots[key]
    if source and source.stackCount == 1 then choices[#choices + 1] = source end
  end
  if #choices == 0 then return {sources = {}, offset = offset, reason = "no_carried_candidate"} end
  if input.selected ~= nil then
    for _, source in ipairs(choices) do
      if same_ref(source, input.selected) then return {sources = {source}, offset = offset, reason = ""} end
    end
    return {sources = {}, offset = offset, reason = "disenchant_source_changed_during_comparison"}
  end
  local result, start = {}, offset % #choices
  for i = 1, math.min(8, #choices) do result[i] = choices[(start + i - 1) % #choices + 1] end
  return {sources = result, offset = offset + 8, reason = ""}
end

local function templates(rows, maximum)
  if not array(rows, maximum) then return nil end
  local out = {}
  for _, row in ipairs(rows) do
    if type(row) ~= "table" or not uint(row.itemId, UINT32) or row.itemId == 0 or out[row.itemId] or
        row.available ~= true or row.pending ~= nil and row.pending ~= false then return nil end
    out[row.itemId] = row
  end
  return out
end

local function plan(input)
  local metadata, result = templates(input.metadata, 8), {}
  if not metadata or not array(input.sources, 8) then
    return {comparisons = {}, reason = "party_item_metadata_pending"}
  end
  for _, source in ipairs(input.sources) do
    local item = type(source) == "table" and metadata[source.entry]
    if not ref(source) or not item then return {comparisons = {}, reason = "party_item_metadata_pending"} end
    local quality = item.quality or 0
    if item.itemClass == 4 and uint(quality, UINT32) and quality >= 2 and quality <= 4 then
      result[#result + 1] = {source = source, slot = SLOTS[item.inventoryType or 0] or false}
    end
  end
  return {comparisons = result, reason = #result == 0 and "no_ordinary_quality_gear_candidate" or ""}
end

local function has_bit(mask, index)
  return uint(mask, UINT32) and math.floor(mask / 2 ^ (index - 1)) % 2 == 1
end

local function could_use(item, player)
  return has_bit(item.allowableClass or 0, player.classId) and has_bit(item.allowableRace or 0, player.raceId) and
    item.subclass <= ARMOR[player.classId]
end

local function stats(item)
  local rows, out = item.stats or {}, {}
  if not array(rows, 10) then return nil end
  for _, row in ipairs(rows) do
    if type(row) ~= "table" then return nil end
    local kind, value = row.type or 0, row.value or 0
    if not uint(kind, UINT32) or not uint(value, 2147483647) or out[kind] ~= nil or
        not (kind <= 1 or kind >= 3 and kind <= 7 or kind >= 12 and kind <= 39 or kind >= 43 and kind <= 48) then
      return nil
    end
    out[kind] = value
  end
  return out
end

local function false_or_absent(value)
  return value == nil or value == false
end

local function dominated(item, equipped, current)
  if type(equipped) ~= "table" or type(current) ~= "table" or item.armorComparisonKnown ~= true or
      current.armorComparisonKnown ~= true or not uint(equipped.entry, UINT32) or equipped.entry == 0 or
      equipped.metadataKnown ~= true or equipped.enhancementsKnown ~= true or not false_or_absent(equipped.hasEnhancements) or
      (equipped.stackCount or 1) ~= 1 or not false_or_absent(current.hasSpell) or
      (current.flags or 0) ~= 0 or (current.flags2 or 0) ~= 0 or current.itemClass ~= 4 or
      not uint(current.itemLevel or 0, UINT32) or not uint(current.armor or 0, UINT32) or
      (current.itemLevel or 0) < (item.itemLevel or 0) or (current.armor or 0) < (item.armor or 0) or
      equipped.durabilityKnown ~= true then return false end
  local maximum, durability = equipped.maxDurability or 0, equipped.durability or 0
  if not uint(maximum, UINT32) or not uint(durability, UINT32) or
      maximum > 0 and (durability == 0 or durability > maximum) then return false end
  local old, new = stats(current), stats(item)
  if not old or not new then return false, "party_stat_comparison_unknown" end
  for kind, value in pairs(new) do
    if (old[kind] or 0) < value then return false end
  end
  return true
end

local function compare(input)
  local source, item = input.source, input.item
  if not ref(source) then return "owned_item_enhancement_context_required" end
  if not array(input.protected, 256) or not array(input.questItems, 4096) then
    return "party_upgrade_or_unknown_comparison_retained"
  end
  for _, guid in ipairs(input.protected) do
    if not decimal(guid) then return "party_upgrade_or_unknown_comparison_retained" end
    if guid == source.itemGuid then return "reserved_or_displaced_gear_retained" end
  end
  for _, entry in ipairs(input.questItems) do
    if not uint(entry, UINT32) or entry == 0 then return "party_upgrade_or_unknown_comparison_retained" end
    if entry == source.entry then return "active_party_quest_item_retained" end
  end
  if source.metadataKnown ~= true or source.stackCount ~= 1 or source.enhancementsKnown ~= true or
      not false_or_absent(source.hasEnhancements) then return "owned_item_enhancement_context_required" end
  if type(item) ~= "table" or item.itemId ~= source.entry or item.available ~= true or
      not false_or_absent(item.pending) then return "party_item_metadata_pending" end
  if item.itemClass ~= 4 or not uint(item.subclass, 4) or item.subclass == 0 or
      item.armorComparisonKnown ~= true or not uint(item.quality, 4) or item.quality < 2 or
      not false_or_absent(item.hasSpell) or (item.flags or 0) ~= 0 or (item.flags2 or 0) ~= 0 then
    return "only_ordinary_armor_compared_for_automatic_disenchant"
  end
  local slot = SLOTS[item.inventoryType or 0]
  if slot == nil or item.inventoryType == 16 and item.subclass ~= 1 then
    return "unsupported_party_equipment_comparison"
  end
  for _, field in ipairs({"requiredSpell", "requiredHonorRank", "requiredCityRank",
      "requiredReputationFaction", "requiredReputationRank", "requiredSkill", "requiredSkillRank"}) do
    if (item[field] or 0) ~= 0 then return "special_or_future_requirements_retained" end
  end
  if not uint(item.requiredLevel or 0, UINT32) or not uint(item.itemLevel or 0, UINT32) or
      not uint(item.armor or 0, UINT32) or not array(input.party, 5) or #input.party ~= 5 then
    return "party_upgrade_or_unknown_comparison_retained"
  end
  local current = templates(input.current, 5)
  if not current then return "party_upgrade_or_unknown_comparison_retained" end
  for index, guid in ipairs(PARTY) do
    local view = input.party[index]
    if type(view) ~= "table" or view.guid ~= guid then return "party_upgrade_or_unknown_comparison_retained" end
    local player = view.player
    if type(player) ~= "table" then
      if guid ~= "3" or has_bit(item.allowableClass or 0, 1) then
        return guid == "3" and "owner_gear_comparison_unavailable" or "party_upgrade_or_unknown_comparison_retained"
      end
    else
      if player.classId ~= CLASSES[guid] or not uint(player.raceId, 11) or player.raceId == 0 or
          not uint(player.level, 80) or player.level == 0 then return "party_upgrade_or_unknown_comparison_retained" end
      if could_use(item, player) then
        if (item.requiredLevel or 0) > player.level then return "future_party_equipment_retained" end
        local proficiency = view.proficiency
        if type(proficiency) ~= "table" or proficiency.known ~= true or proficiency.complete ~= true or
            type(proficiency.ranks) ~= "table" then return "future_or_unknown_party_proficiency_retained" end
        local rank = proficiency.ranks[PROFICIENCY[item.subclass]]
        if guid == "3" then
          if rank ~= true then return "future_or_unknown_party_proficiency_retained" end
        elseif not positive_revision(proficiency.revision) or not uint(rank or 0, 65535) or (rank or 0) == 0 then
          return "future_or_unknown_party_proficiency_retained"
        end
        local equipped = view.equipment
        if type(equipped) ~= "table" or equipped.slot ~= slot then
          return "party_upgrade_or_unknown_comparison_retained"
        end
        local proven, reason = dominated(item, equipped, type(equipped) == "table" and current[equipped.entry])
        if not proven then return reason or "party_upgrade_or_unknown_comparison_retained" end
      end
    end
  end
  return ""
end

local function select_source(input)
  if not array(input.results, 8) then return {reason = "no_unused_party_gear_proven"} end
  local reason = "no_unused_party_gear_proven"
  for _, row in ipairs(input.results) do
    if type(row) ~= "table" or not ref(row.source) or type(row.reason) ~= "string" then
      return {reason = "party_upgrade_or_unknown_comparison_retained"}
    end
    reason = row.reason
    if reason == "" then return {source = row.source, reason = ""} end
  end
  return {reason = reason}
end

return {
  decide = function(input)
    if input.phase == "candidates" then return candidates(input) end
    if input.phase == "plan" then return plan(input) end
    if input.phase == "compare" then return {reason = compare(input)} end
    if input.phase == "select" then return select_source(input) end
    return {reason = "party_upgrade_or_unknown_comparison_retained"}
  end,
}
