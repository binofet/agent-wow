-- Use the learned toolkit rank. The class policy owns which family restores
-- mana and when to spend its cooldown.
local recovery = {
  [8] = {familyId = 12051, atOrBelowPercent = 35}, -- Mage, Evocation.
}
local observation, sequence

local function valid_guid(value)
  if type(value) ~= "string" or #value == 0 or #value > 20 or byte_at(value, 1) == 48 then return false end
  for i = 1, #value do
    local digit = byte_at(value, i)
    if digit < 48 or digit > 57 then return false end
  end
  return #value < 20 or value <= "18446744073709551615"
end

local function uint32(value)
  return type(value) == "number" and value >= 0 and value <= 4294967295 and value % 1 == 0
end

local function quiet_body(view)
  local caster, activity, threat = view.caster, view.activity, view.threat
  -- The feed reports current identity, movement, cast custody and attack
  -- evidence. A missing observation cannot establish a channel window.
  return type(caster) == "table" and caster.guid == view.selfGuid and caster.present == true and
    caster.aliveKnown == true and caster.alive == true and caster.mapKnown == true and caster.positionKnown == true and
    caster.movementKnown == true and caster.moving == false and caster.combatKnown == true and caster.inCombat == false and
    caster.channelKnown == true and caster.channelSpellId == 0 and
    type(activity) == "table" and activity.known == true and activity.idle == true and
    type(threat) == "table" and threat.known == true and threat.attackingSelf == false and threat.attackingParty == false
end

local function learned_rank(spells, familyId)
  local best
  for _, spell in ipairs(spells) do
    if type(spell) == "table" and spell.familyId == familyId and spell.metadataKnown == true and
        spell.selfCastSupported == true and spell.channeled == true and spell.readyKnown == true and spell.ready == true and
        uint32(spell.spellId) and spell.spellId > 0 and uint32(spell.rank) and
        (not best or spell.rank > best.rank or spell.rank == best.rank and spell.spellId < best.spellId) then
      best = spell
    end
  end
  return best
end

return {
  on_event = function(event)
    if event.kind == "self_mana_observation" then
      observation, sequence = event.value, event.sequence
    end
    return true
  end,
  decide = function(frame)
    local input = frame.input
    if input.kind ~= "self_mana" or observation == nil or input.observationSequence ~= sequence then return {} end

    local view, observedAt = observation, sequence
    observation, sequence = nil, nil
    if #frame.pending ~= 0 or view.hostOwned ~= true or view.sessionId ~= frame.session_id or
        not valid_guid(view.selfGuid) or not quiet_body(view) or
        view.spellbookKnown ~= true or type(view.spells) ~= "table" then return {} end

    local caster = view.caster
    local plan = caster.classKnown == true and recovery[caster.classId]
    if not plan or caster.powerKnown ~= true or caster.powerType ~= 0 or not uint32(caster.power) or
        not uint32(caster.maxPower) or caster.maxPower == 0 or
        caster.power * 100 > caster.maxPower * plan.atOrBelowPercent then return {} end

    local spell = learned_rank(view.spells, plan.familyId)
    if not spell then return {} end
    return {action = {
      capability = "cast_self", sessionId = frame.session_id, selfGuid = view.selfGuid,
      targetGuid = view.selfGuid, spellId = spell.spellId, observationSequence = observedAt,
    }}
  end,
  export_state = function()
    return {version = 1, data = {}}
  end,
  import_state = function(checkpoint)
    return checkpoint.version == 1
  end,
}
