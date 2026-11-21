-- The session host publishes a coherent healing_observation containing caster
-- readiness and mana, current healable players, and learned ready heal ranks.
-- Distances and spell ranges are in yards. Amount and cost are computed from
-- the observed caster and spell data. Admission rechecks the exact cast.
local observation, sequence
local request_pending = false
local tuning = {heal_below_percent=85, self_urgent_percent=50, critical_percent=40,
                rank_mode="effective_per_resource"}

local function patient_priority(patient, selfGuid)
  if patient.guid == selfGuid and patient.health * 100 < patient.maxHealth * tuning.self_urgent_percent then
    return 1
  end
  if patient.health * 100 < patient.maxHealth * tuning.critical_percent then return 2 end
  if patient.underAttack then return 3 end
  return 4
end

local function tie_priority(patient, selfGuid)
  if patient.isOwner then return 1 end
  if patient.guid == selfGuid then return 2 end
  return 3
end

local function prefer_patient(patient, best, selfGuid)
  if best == nil then return true end
  local a, b = patient_priority(patient, selfGuid), patient_priority(best, selfGuid)
  if a ~= b then return a < b end
  local left, right = patient.health * best.maxHealth, best.health * patient.maxHealth
  if left ~= right then return left < right end
  a, b = tie_priority(patient, selfGuid), tie_priority(best, selfGuid)
  if a ~= b then return a < b end
  -- Compare canonical decimal GUIDs without rounding them through Lua numbers.
  if #patient.guid ~= #best.guid then return #patient.guid < #best.guid end
  return patient.guid < best.guid
end

local function prefer_rank(spell, best)
  if spell.level ~= best.level then return spell.level > best.level end
  if spell.rank ~= best.rank then return spell.rank > best.rank end
  return spell.spellId < best.spellId
end

local function affordable_ranks(spells, mana)
  local eligible = {}
  for _, spell in ipairs(spells) do
    if type(spell) == "table" and type(spell.spellId) == "number" and spell.spellId > 0 and
        type(spell.cost) == "number" and spell.cost >= 0 and spell.cost <= mana and
        type(spell.amount) == "number" and spell.amount > 0 and
        type(spell.castMs) == "number" and spell.castMs >= 0 and
        type(spell.level) == "number" and type(spell.rank) == "number" and
        type(spell.minRange) == "number" and type(spell.maxRange) == "number" and
        spell.minRange >= 0 and spell.maxRange >= spell.minRange then
      eligible[#eligible + 1] = spell
    end
  end
  return eligible
end

local function within_range(spell, distance)
  return not spell.selfOnly and distance >= spell.minRange and distance <= spell.maxRange
end

local function can_heal(patient, selfGuid, spells)
  if patient.guid == selfGuid then return true end
  for _, spell in ipairs(spells) do
    if not spell.selfOnly and spell.maxRange > 0 and patient.distance >= spell.minRange then return true end
  end
  return false
end

local function choose_rank(patient, selfGuid, spells)
  local best, bestScore = nil, -1
  local missing = patient.maxHealth - patient.health
  local self = patient.guid == selfGuid
  local urgent = patient.underAttack or patient.health * 100 < patient.maxHealth * tuning.critical_percent
  local current_range
  for _, spell in ipairs(spells) do
    if not self and within_range(spell, patient.distance) then current_range = true end
  end
  for _, spell in ipairs(spells) do
    if self or not spell.selfOnly and patient.distance >= spell.minRange and
      (current_range and within_range(spell, patient.distance) or not current_range and spell.maxRange > 0) then
      local effective = math.min(spell.amount, missing)
      local denominator = math.max(1, spell.cost)
      if urgent then denominator = math.max(1500, spell.castMs) end
      local score = tuning.rank_mode == "largest" and effective or effective / denominator
      -- If no rank reaches yet, choose the longest available friendly reach
      -- before comparing efficiency. That is the range the movement policy
      -- should achieve; it must not silently substitute a different rank.
      local longer = not self and not current_range and best and spell.maxRange ~= best.maxRange
      if not best or longer and spell.maxRange > best.maxRange or not longer and
        (score > bestScore or score == bestScore and prefer_rank(spell, best)) then
        best, bestScore = spell, score
      end
    end
  end
  return best
end

local function choose_cast(view, explicit)
  local caster = view.caster
  if type(caster) ~= "table" or caster.ready ~= true or
      type(caster.guid) ~= "string" or type(caster.mana) ~= "number" or caster.mana < 0 or
      type(view.patients) ~= "table" or type(view.spells) ~= "table" then
    return nil, nil
  end
  local spells = affordable_ranks(view.spells, caster.mana)
  if #spells == 0 then return nil, nil end
  local target
  for _, patient in ipairs(view.patients) do
    if type(patient) == "table" and type(patient.guid) == "string" and
        type(patient.health) == "number" and patient.health > 0 and
        type(patient.maxHealth) == "number" and patient.health * 100 < patient.maxHealth * (explicit and 100 or tuning.heal_below_percent) and
        (patient.guid == caster.guid or
          (type(patient.distance) == "number" and patient.distance >= 0)) and
        prefer_patient(patient, target, caster.guid) and can_heal(patient, caster.guid, spells) then
      target = patient
    end
  end
  if not target then return nil, nil end
  return target, choose_rank(target, caster.guid, spells)
end

-- A future heal reserve is a resource policy, independent of today's patient,
-- cooldown, range or affordability. Keep uint64 costs exact until clamping
-- above every possible uint32 caster mana value.
local function decimal_cost(value)
  if #value == 0 or #value > 20 or #value > 1 and byte_at(value, 1) == 48 or
      #value == 20 and value > "18446744073709551615" then return false end
  for i = 1, #value do
    local digit = byte_at(value, i)
    if digit < 48 or digit > 57 then return false end
  end
  return true
end

local function reserve_cost(value)
  local ceiling = 4294967296
  if type(value) == "string" then
    if not decimal_cost(value) then return end
    if #value > 10 or #value == 10 and value >= "4294967296" then return ceiling end
    return tonumber(value)
  end
  if type(value) == "number" and value == value and value >= 0 and
      value <= 9007199254740991 and value % 1 == 0 then return math.min(value, ceiling) end
end

local function resource_reserve(obs)
  if type(obs) ~= "table" or type(obs.spells) ~= "table" then return end
  local reserve = 0
  for _, spell in pairs(obs.spells) do
    if type(spell) == "table" and spell.known == true and spell.capability == "heal" then
      if type(spell.powerType) ~= "number" or spell.powerType-spell.powerType ~= 0 or spell.powerType % 1 ~= 0 then return end
      if spell.powerType == 0 then
        if spell.metadataKnown ~= true then return end
        local cost = reserve_cost(spell.cost)
        if cost == nil then return end
        reserve = math.max(reserve, cost)
      end
    end
  end
  return reserve
end

return {
  resource_reserve = resource_reserve,
  on_event = function(event)
    if event.kind == "healing_observation" then
      observation, sequence = event.value, event.sequence
    elseif event.kind == "healing_request" then
      request_pending = true
    end
    return true
  end,
  decide = function(frame)
    local input = frame.input
    if input.kind ~= "healing" or observation == nil or input.observationSequence ~= sequence then return {} end

    -- A transport or effect receipt cannot make a consumed snapshot fresh.
    local view = observation
    observation, sequence = nil, nil
    if #frame.pending ~= 0 then return {} end
    local target, rank = choose_cast(view, request_pending)
    if view.caster and view.caster.ready == true then request_pending = false end
    if not target then return {} end
    local action = {
      capability = "heal", sessionId = frame.session_id, selfGuid = view.caster.guid,
      targetGuid = target.guid, spellId = rank.spellId,
    }
    if target.guid ~= view.caster.guid and not within_range(rank, target.distance) then
      action.needsApproach = true
      action.rangeYards, action.minRangeYards, action.requireLos = rank.maxRange, rank.minRange, true
    end
    return {action = action}
  end,
  export_state = function()
    return {version = 1, data = {requestPending=request_pending}}
  end,
  import_state = function(checkpoint)
    if checkpoint.version ~= 1 or type(checkpoint.data) ~= "table" then return false end
    request_pending = checkpoint.data.requestPending == true
    return true
  end,
}
