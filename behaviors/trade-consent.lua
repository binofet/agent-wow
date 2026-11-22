-- Ordinary owner-trade consent. Native trade execution retains ownership,
-- service-item, lease and final-frame checks; preparation and consent are
-- separate choices in this same editable export.
local accept_owner_trades = true
local prepare_owner_trade_services = true
local retry_trade_service_try_again = true
local owner_guid = "3"

local UINT32 = 4294967295
local INPUT_KEYS = {sessionId=true, selfGuid=true, trade=true}
local TRADE_KEYS = {sessionId=true, revision=true, phase=true, partnerGuid=true,
  partnerAccepted=true, acceptTransportWritten=true, ownRequestedItems=true,
  ownRequestedCopper=true, partnerItems=true, partnerCopper=true}
local ITEM_KEYS = {slot=true, entry=true, count=true, itemGuid=true, name=true, metadataKnown=true}

local function object(value, keys, size)
  if type(value) ~= "table" then return false end
  local count = 0
  for key in pairs(value) do
    if not keys[key] then return false end
    count = count + 1
  end
  return count == size
end

local function uint(value, maximum)
  return type(value) == "number" and value >= 0 and value <= maximum and value == math.floor(value)
end

local function decimal(value, positive)
  if type(value) ~= "string" or #value == 0 or #value > 20 then return false end
  if byte_at(value, 1) == 48 and (#value > 1 or positive) then return false end
  for i = 1, #value do
    local b = byte_at(value, i)
    if b < 48 or b > 57 then return false end
  end
  return #value < 20 or value <= "18446744073709551615"
end

local function session(value)
  return type(value) == "string" and #value > 0 and #value <= 128
end

local function items(rows, maximum)
  if type(rows) ~= "table" or #rows > maximum then return false end
  local count, slots = 0, {}
  for key, row in pairs(rows) do
    if not uint(key, #rows) or key == 0 or not object(row, ITEM_KEYS, 6) or
        not uint(row.slot, maximum - 1) or slots[row.slot] or not uint(row.entry, UINT32) or
        not uint(row.count, UINT32) or not decimal(row.itemGuid, false) or
        type(row.name) ~= "string" or #row.name > 255 or type(row.metadataKnown) ~= "boolean" then return false end
    slots[row.slot] = true
    count = count + 1
  end
  return count == #rows
end

local function wait(reason)
  return {kind="wait", reason=reason}
end

local PREPARATION_KEYS = {kind=true, sessionId=true, selfGuid=true, trade=true, service=true,
  attempt=true, world=true, spell=true, reference=true, window=true}
local SERVICE_KEYS = {slot=true, entry=true, count=true, wrapped=true, lockId=true, partnerSpell=true}
local ATTEMPT_KEYS = {item=true, castCount=true, queued=true, accepted=true, castGo=true, retry=true,
  deadlineKnown=true, remainingMs=true}
local ATTEMPT_ITEM_KEYS = {entry=true, count=true, wrapped=true, lockId=true}
local WINDOW_KEYS = {deadlineKnown=true, remainingMs=true}
local WORLD_KEYS = {visitId=true, mapKnown=true, mapId=true, self=true, owner=true, skills=true}
local SELF_KEYS = {guid=true, present=true, aliveKnown=true, alive=true, mapKnown=true, mapId=true,
  positionKnown=true, positionRevision=true, classKnown=true, classId=true, unitFlagsKnown=true,
  unitFlags=true, movementFlagsKnown=true, movementFlags=true}
local OWNER_KEYS = {guid=true, present=true, aliveKnown=true, alive=true, mapKnown=true, mapId=true,
  positionKnown=true, positionRevision=true, unitFlagsKnown=true, unitFlags=true, x=true, y=true, z=true}
local SKILLS_KEYS = {known=true, complete=true, revision=true, rows=true}
local SKILL_KEYS = {slot=true, skillId=true, value=true, maxValue=true, temporaryBonus=true, permanentBonus=true}
local SPELL_KEYS = {sessionId=true, selfGuid=true, spellbookKnown=true, spellbookRevision=true,
  present=true, spellId=true, metadataKnown=true, castTimeMs=true}
local REFERENCE_KEYS = {catalog=true, lockId=true, known=true, requiredSkillRank=true}

local function trade_facts(trade, session_id)
  return object(trade, TRADE_KEYS, 10) and session(trade.sessionId) and trade.sessionId == session_id and
    decimal(trade.revision, true) and decimal(trade.partnerGuid, false) and
    type(trade.phase) == "string" and #trade.phase > 0 and #trade.phase <= 64 and
    type(trade.partnerAccepted) == "boolean" and type(trade.acceptTransportWritten) == "boolean" and
    uint(trade.ownRequestedCopper, UINT32) and uint(trade.partnerCopper, UINT32) and
    items(trade.ownRequestedItems, 6) and items(trade.partnerItems, 7)
end

local function service_fields(item)
  return uint(item.entry, UINT32) and uint(item.count, UINT32) and
    type(item.wrapped) == "boolean" and uint(item.lockId, UINT32)
end

local function unit_fields(unit, keys, size)
  return object(unit, keys, size) and decimal(unit.guid, true) and type(unit.present) == "boolean" and
    type(unit.aliveKnown) == "boolean" and type(unit.alive) == "boolean" and
    type(unit.mapKnown) == "boolean" and uint(unit.mapId, UINT32) and type(unit.positionKnown) == "boolean" and
    decimal(unit.positionRevision, false) and type(unit.unitFlagsKnown) == "boolean" and uint(unit.unitFlags, UINT32)
end

local function coordinate(value)
  return type(value) == "number" and value == value and value > -40000 and value < 40000
end

local function signed(value)
  return type(value) == "number" and value >= -2147483648 and value <= 2147483647 and value == math.floor(value)
end

local function safe_unit(unit, map_id)
  return unit.present and unit.aliveKnown and unit.alive and unit.mapKnown and unit.mapId == map_id and
    unit.positionKnown and decimal(unit.positionRevision, true) and unit.unitFlagsKnown and
    math.floor(unit.unitFlags / 524288) % 2 == 0 and math.floor(unit.unitFlags / 8192) % 2 == 0
end

local function prepare(input)
  if not object(input, PREPARATION_KEYS, 10) or not session(input.sessionId) or not decimal(input.selfGuid, true) or
      not trade_facts(input.trade, input.sessionId) then return wait("current_service_trade_facts_required") end
  local item, attempt, world, spell, reference, window = input.service, input.attempt, input.world,
    input.spell, input.reference, input.window
  if not object(item, SERVICE_KEYS, 6) or item.slot ~= 6 or not service_fields(item) or
      not uint(item.partnerSpell, UINT32) or not object(attempt, ATTEMPT_KEYS, 8) or
      not object(attempt.item, ATTEMPT_ITEM_KEYS, 4) or not service_fields(attempt.item) or
      not uint(attempt.castCount, 255) or type(attempt.queued) ~= "boolean" or type(attempt.accepted) ~= "boolean" or
      type(attempt.castGo) ~= "boolean" or type(attempt.retry) ~= "boolean" or
      type(attempt.deadlineKnown) ~= "boolean" or not uint(attempt.remainingMs, UINT32) or
      not object(window, WINDOW_KEYS, 2) or type(window.deadlineKnown) ~= "boolean" or not uint(window.remainingMs, UINT32) then
    return wait("current_service_attempt_facts_required")
  end
  if not attempt.deadlineKnown and attempt.remainingMs ~= 0 or attempt.item.entry ~= 0 and attempt.castCount == 0 then
    return wait("current_service_attempt_facts_required")
  end
  if not object(world, WORLD_KEYS, 6) or not decimal(world.visitId, true) or type(world.mapKnown) ~= "boolean" or
      not uint(world.mapId, UINT32) or not unit_fields(world.self, SELF_KEYS, 14) or
      not unit_fields(world.owner, OWNER_KEYS, 13) or world.self.guid ~= input.selfGuid or
      type(world.self.classKnown) ~= "boolean" or not uint(world.self.classId, UINT32) or
      type(world.self.movementFlagsKnown) ~= "boolean" or not uint(world.self.movementFlags, UINT32) or
      not coordinate(world.owner.x) or not coordinate(world.owner.y) or not coordinate(world.owner.z) then
    return wait("current_service_world_required")
  end
  local skills = world.skills
  if not object(skills, SKILLS_KEYS, 4) or type(skills.known) ~= "boolean" or type(skills.complete) ~= "boolean" or
      not decimal(skills.revision, false) or type(skills.rows) ~= "table" or #skills.rows > 1 then
    return wait("current_lockpicking_skill_required")
  end
  local count, rank = 0, nil
  for index, row in pairs(skills.rows) do
    if not uint(index, #skills.rows) or index == 0 or not object(row, SKILL_KEYS, 6) or not uint(row.slot, 127) or
        row.skillId ~= 633 or not uint(row.value, UINT32) or not uint(row.maxValue, UINT32) or row.value > row.maxValue or
        not signed(row.temporaryBonus) or not signed(row.permanentBonus) then return wait("current_lockpicking_skill_required") end
    count = count + 1
    if row.value > 0 then rank = row.value + row.temporaryBonus + row.permanentBonus end
  end
  if count ~= #skills.rows then return wait("current_lockpicking_skill_required") end
  if not object(spell, SPELL_KEYS, 8) or spell.sessionId ~= input.sessionId or spell.selfGuid ~= input.selfGuid or
      type(spell.spellbookKnown) ~= "boolean" or not decimal(spell.spellbookRevision, false) or
      type(spell.present) ~= "boolean" or not uint(spell.spellId, UINT32) or type(spell.metadataKnown) ~= "boolean" or
      not uint(spell.castTimeMs, UINT32) or not object(reference, REFERENCE_KEYS, 4) or
      reference.catalog ~= "client_build_12340_lockpicking" or reference.lockId ~= item.lockId or
      type(reference.known) ~= "boolean" or not uint(reference.requiredSkillRank, UINT32) then
    return wait("known_lockpicking_spell_and_reference_required")
  end
  if prepare_owner_trade_services ~= true or not decimal(owner_guid, true) then return wait("owner_trade_preparation_disabled") end
  local trade = input.trade
  if trade.partnerGuid ~= owner_guid or world.owner.guid ~= owner_guid then return wait("owner_trade_partner_required") end
  if trade.phase ~= "OPEN" or trade.acceptTransportWritten or not window.deadlineKnown or window.remainingMs == 0 then
    return wait("open_service_trade_required")
  end
  local slot
  for _, row in ipairs(trade.partnerItems) do if row.slot == 6 then slot = row end end
  if not slot or slot.entry ~= item.entry or slot.count ~= item.count or item.entry == 0 or item.count ~= 1 or
      item.wrapped or item.lockId == 0 or item.partnerSpell ~= 0 then return wait("actual_unwrapped_service_slot_required") end
  if not world.mapKnown or not safe_unit(world.self, world.mapId) or not safe_unit(world.owner, world.mapId) or
      not world.self.classKnown or world.self.classId ~= 4 or
      world.self.movementFlagsKnown and world.self.movementFlags % 256 ~= 0 then return wait("safe_service_body_required") end
  if not skills.known or not skills.complete or not decimal(skills.revision, true) or rank == nil or
      not reference.known or rank < reference.requiredSkillRank or not spell.spellbookKnown or
      not decimal(spell.spellbookRevision, true) or not spell.present or spell.spellId ~= 1804 or
      not spell.metadataKnown or spell.castTimeMs == 0 then return wait("known_lockpicking_capability_required") end
  local prior = attempt.item
  local same = prior.entry == item.entry and prior.count == item.count and prior.wrapped == item.wrapped and prior.lockId == item.lockId
  if prior.entry == 0 and (prior.count ~= 0 or prior.wrapped or prior.lockId ~= 0 or attempt.castCount ~= 0 or
      attempt.queued or attempt.accepted or attempt.castGo or attempt.retry or attempt.deadlineKnown or attempt.remainingMs ~= 0) then
    return wait("current_service_attempt_facts_required")
  end
  if attempt.queued or attempt.castGo or same and (not attempt.retry or retry_trade_service_try_again ~= true) then
    return wait("service_attempt_retained_no_replay")
  end
  return {kind="prepare", reason=same and "correlated_try_again_service_retry" or "owner_trade_service_ready"}
end

local function decide(input)
  if type(input) == "table" and input.kind == "prepare_trade_service" then return prepare(input) end
  if not object(input, INPUT_KEYS, 3) or not session(input.sessionId) or not decimal(input.selfGuid, true) then
    return wait("current_trade_identity_required")
  end
  local trade = input.trade
  if not trade_facts(trade, input.sessionId) then
    return wait("current_trade_facts_required")
  end
  if accept_owner_trades ~= true or not decimal(owner_guid, true) then return wait("owner_trade_consent_disabled") end
  if trade.partnerGuid ~= owner_guid then return wait("owner_trade_partner_required") end
  if trade.phase ~= "OPEN" then return wait("open_trade_required") end
  if trade.acceptTransportWritten then return wait("trade_accept_already_written") end
  if not trade.partnerAccepted then return wait("partner_consent_required") end
  return {kind="accept", reason="owner_accepted_current_trade"}
end

return {decide=decide}
