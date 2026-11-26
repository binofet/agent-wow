-- The session host publishes a coherent healing_observation containing caster
-- readiness and mana, current healable players, and learned ready heal ranks.
-- Distances and spell ranges are in yards. Amount and cost are computed from
-- the observed caster and spell data. Admission rechecks the exact cast.
local observation, sequence
local request_pending = false
local visit, proposed, refused = nil, nil, {}
-- World supplies up to 64 nearby players plus self and owner. Keep no more
-- identities than that across a visit, even as different patients come and go.
local refusal_limit, refused_count, overflow = 66, 0, false
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

local function range_allows_cast(spell, distance)
  return not spell.selfOnly and spell.maxRange > 0 and
    (distance == nil or distance >= spell.minRange and distance <= spell.maxRange)
end

local function can_heal(patient, selfGuid, spells)
  if patient.guid == selfGuid then return true end
  for _, spell in ipairs(spells) do
    if not spell.selfOnly and spell.maxRange > 0 and
        (patient.distance == nil or patient.distance >= spell.minRange) then return true end
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
    if not self and range_allows_cast(spell, patient.distance) then current_range = true end
  end
  for _, spell in ipairs(spells) do
    if self or range_allows_cast(spell, patient.distance) or not current_range and
        not spell.selfOnly and spell.maxRange > 0 and patient.distance >= spell.minRange then
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

local function needs_heal(patient, explicit)
  return type(patient)=="table" and type(patient.guid)=="string" and
    type(patient.health)=="number" and patient.health>0 and type(patient.maxHealth)=="number" and
    patient.health*100<patient.maxHealth*(explicit and 100 or tuning.heal_below_percent)
end

local function position_unknown(view, patient)
  return patient.distance == nil or view.caster and patient.guid == view.caster.guid and view.casterMotionUnknown == true
end

local function requires_measurement(guid)
  return refused[guid] or overflow or refused_count >= refusal_limit
end

local function clear_refusal(guid)
  if refused[guid] then refused[guid], refused_count = nil, refused_count - 1 end
end

local function hold_refusal(guid, reason)
  if refused[guid] then
    if reason == true then refused[guid] = true end
  elseif refused_count < refusal_limit then
    refused[guid], refused_count = reason, refused_count + 1
  else
    -- A measured cast may also fail while the set is full. Preserve every
    -- earlier refusal and require measurements for this visit.
    overflow = true
  end
end

local function choose_cast(view, explicit, allow_busy)
  local caster = view.caster
  if type(caster) ~= "table" or not allow_busy and caster.ready ~= true or
      type(caster.guid) ~= "string" or type(caster.mana) ~= "number" or caster.mana < 0 or
      type(view.patients) ~= "table" or type(view.spells) ~= "table" then
    return nil, nil
  end
  local spells = affordable_ranks(view.spells, caster.mana)
  if #spells == 0 then return nil, nil end
  local target
  for _, patient in ipairs(view.patients) do
    if needs_heal(patient,explicit) and
        not (position_unknown(view, patient) and requires_measurement(patient.guid)) and
        (patient.guid == caster.guid or patient.distance == nil or
          type(patient.distance) == "number" and patient.distance >= 0 and patient.distance < math.huge) and
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
  -- Planning a custody handoff is not a claim that a cast is currently ready.
  -- Reuse triage rather than duplicating its thresholds in maintenance policy.
  needs_access = function(view, obs)
    local target,rank=choose_cast(view,request_pending,true)
    if target and rank then return true end
    if type(view.patients) == "table" and #view.patients > 0 and
        type(view.spells) == "table" and #view.spells > 0 then return false end
    -- Older Actions builds hide patient/rank rows while another interaction
    -- owns the body. Their raw player vitals and available spell metadata can
    -- still prove that this healer needs access for self/party triage.
    if not obs or not obs.selfGuid then return false end
    local self_heal,ally_heal=false,false
    for _,spell in pairs(obs.spells or {}) do
      if spell.known==true and spell.available==true and spell.metadataKnown==true and spell.capability=="heal" then
        self_heal=true
        if spell.targetRule=="ally" or (spell.maxRange or 0)>0 then ally_heal=true end
      end
    end
    local allies={[obs.selfGuid]=true}
    for _,member in ipairs(((obs.party or {}).group or {}).members or {}) do
      if member.online==true then allies[member.guid]=true end
    end
    local measured = {[obs.selfGuid]=view.casterMotionUnknown ~= true}
    for _, patient in ipairs(view.patients or {}) do
      if not position_unknown(view, patient) then measured[patient.guid] = true end
    end
    for key,patient in pairs(obs.players or {}) do
      if allies[key] and (measured[key] or not requires_measurement(key)) and
        (key==obs.selfGuid and self_heal or key~=obs.selfGuid and ally_heal) and
        patient.present==true and patient.aliveKnown==true and patient.alive==true and patient.healthKnown==true and
        patient.mapKnown==true and (patient.mapId or 0)==(obs.mapId or 0) and needs_heal(patient,request_pending) then return true end
    end
    return false
  end,
  on_event = function(event)
    if event.kind == "healing_observation" then
      observation, sequence = event.value, event.sequence
      if observation.visitId ~= visit then
        visit, proposed, refused, refused_count, overflow = observation.visitId, nil, {}, 0, false
      end
      local caster = observation.caster
      if caster then
        for guid, reason in pairs(refused) do
          if reason == "awaiting_busy" and caster.ready ~= true and observation.reason == "healing_caster_busy" then
            refused[guid] = "observed_busy"
          elseif reason == "observed_busy" and caster.ready == true then clear_refusal(guid) end
        end
      end
      if observation.caster and observation.caster.ready == true and type(observation.patients) == "table" then
        -- Only this patient's actual range/own motion can clear its refusal.
        -- Temporary loss of a row or its unit fields supplies no such evidence.
        for _, patient in ipairs(observation.patients or {}) do
          if not position_unknown(observation, patient) then clear_refusal(patient.guid) end
        end
      end
    elseif event.kind == "healing_request" then
      request_pending = true
    elseif event.receipt then
      local receipt = event.receipt
      local p = receipt.payload or {}
      if proposed and p.capability == "heal" and p.target_guid == proposed.target and p.spell_id == proposed.spell and
          (not proposed.id or proposed.id == receipt.action_id) then
        proposed.id = receipt.action_id
        if receipt.outcome == "failed" or receipt.outcome == "unknown" or receipt.outcome == "not_dispatched" or
            receipt.outcome == "settled_unverified" then
          local caster_busy = receipt.outcome == "not_dispatched" and p.phase == "admission_rejected" and
            p.transport_written == false and p.grpc_status == "FailedPrecondition" and
            (p.grpc_message == "healing_caster_busy" or p.grpc_message == "healing_caster_changed_before_cast")
          hold_refusal(p.target_guid, caster_busy and "awaiting_busy" or true)
        elseif receipt.outcome == "confirmed" then clear_refusal(p.target_guid) end
      end
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
    proposed = {target=target.guid, spell=rank.spellId}
    local action = {
      capability = "heal", sessionId = frame.session_id, selfGuid = view.caster.guid,
      targetGuid = target.guid, spellId = rank.spellId,
    }
    if target.guid ~= view.caster.guid and target.distance == nil then
      action.rangeUnknown = true
    elseif target.guid ~= view.caster.guid and not range_allows_cast(rank, target.distance) then
      action.needsApproach = true
      action.rangeYards, action.minRangeYards, action.requireLos = rank.maxRange, rank.minRange, true
    end
    return {action = action}
  end,
  export_state = function()
    return {version = 1, data = {requestPending=request_pending, visit=visit, proposed=proposed, refused=refused,
      refusalOverflow=overflow or nil}}
  end,
  import_state = function(checkpoint)
    if checkpoint.version ~= 1 or type(checkpoint.data) ~= "table" then return false end
    request_pending = checkpoint.data.requestPending == true
    visit, proposed, refused = checkpoint.data.visit, checkpoint.data.proposed, checkpoint.data.refused or {}
    if type(refused) ~= "table" or proposed ~= nil and type(proposed) ~= "table" then return false end
    refused_count, overflow = 0, checkpoint.data.refusalOverflow == true
    for guid, held in pairs(refused) do
      refused_count = refused_count + 1
      if type(guid) ~= "string" or held ~= true and held ~= "awaiting_busy" and held ~= "observed_busy" or
          refused_count > refusal_limit then return false end
    end
    return true
  end,
}
