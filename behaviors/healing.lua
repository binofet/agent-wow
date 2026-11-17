-- The session host publishes a coherent healing_observation containing caster
-- readiness and mana, current healable players, and learned ready heal ranks.
-- Distances and spell ranges are in yards. Amount and cost are computed from
-- the observed caster and spell data. Admission rechecks the exact cast.
local observation, sequence

local function patient_priority(patient, selfGuid)
  if patient.guid == selfGuid and patient.health * 100 < patient.maxHealth * 50 then
    return 1
  end
  if patient.health * 100 < patient.maxHealth * 40 then return 2 end
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

local function can_reach(patient, selfGuid, spells)
  if patient.guid == selfGuid then return true end
  for _, spell in ipairs(spells) do
    if within_range(spell, patient.distance) then return true end
  end
  return false
end

local function choose_rank(patient, selfGuid, spells)
  local best, bestScore = nil, -1
  local missing = patient.maxHealth - patient.health
  local self = patient.guid == selfGuid
  local urgent = patient.underAttack or patient.health * 100 < patient.maxHealth * 40
  for _, spell in ipairs(spells) do
    if self or within_range(spell, patient.distance) then
      local effective = math.min(spell.amount, missing)
      local denominator = math.max(1, spell.cost)
      if urgent then denominator = math.max(1500, spell.castMs) end
      local score = effective / denominator
      if score > bestScore or (score == bestScore and prefer_rank(spell, best)) then
        best, bestScore = spell, score
      end
    end
  end
  return best
end

local function choose_cast(view)
  local caster = view.caster
  if view.standing == false or type(caster) ~= "table" or caster.ready ~= true or
      type(caster.guid) ~= "string" or type(caster.mana) ~= "number" or caster.mana < 0 or
      type(view.patients) ~= "table" or type(view.spells) ~= "table" then
    return nil, nil
  end
  local reach = view.maxReach or 35
  if type(reach) ~= "number" or reach < 0 then return nil, nil end
  local spells = affordable_ranks(view.spells, caster.mana)
  if #spells == 0 then return nil, nil end
  local target
  for _, patient in ipairs(view.patients) do
    if type(patient) == "table" and type(patient.guid) == "string" and
        type(patient.health) == "number" and patient.health > 0 and
        type(patient.maxHealth) == "number" and patient.health * 100 < patient.maxHealth * 85 and
        (patient.guid == caster.guid or
          (type(patient.distance) == "number" and patient.distance >= 0 and patient.distance <= reach)) and
        prefer_patient(patient, target, caster.guid) and can_reach(patient, caster.guid, spells) then
      target = patient
    end
  end
  if not target then return nil, nil end
  return target, choose_rank(target, caster.guid, spells)
end

return {
  on_event = function(event)
    if event.kind == "healing_observation" then
      observation, sequence = event.value, event.sequence
    end
    return true
  end,
  decide = function(frame)
    local input = frame.input
    if input.kind ~= "healing" or observation == nil or input.observationSequence ~= sequence then return {} end

    -- A transport or effect receipt cannot make a consumed snapshot fresh.
    local view = observation
    observation, sequence = nil, nil
    local target, rank = choose_cast(view)
    if not target then return {} end
    return {action = {
      capability = "heal", sessionId = frame.session_id, selfGuid = view.caster.guid,
      targetGuid = target.guid, spellId = rank.spellId,
    }}
  end,
  export_state = function()
    return {version = 1, data = {}}
  end,
  import_state = function(checkpoint)
    return checkpoint.version == 1
  end,
}
