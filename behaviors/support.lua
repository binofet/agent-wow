-- Shared support decisions. Go supplies observed bodies, active casts and
-- mechanical spell eligibility; all ordering and movement choices live here.
local function guid(value)
  if type(value) ~= "string" or #value == 0 or #value > 20 or byte_at(value, 1) == 48 then return false end
  for i=1,#value do
    local digit = byte_at(value, i)
    if digit < 48 or digit > 57 then return false end
  end
  return #value < 20 or value <= "18446744073709551615"
end

local function current(obs)
  local self = (obs.players or {})[obs.selfGuid]
  return (obs.hostOwned == true or obs.hostRequested == true) and guid(obs.selfGuid) and
    type(obs.sessionId) == "string" and type(obs.support) == "table" and
    self and self.present == true and self.aliveKnown == true and self.alive == true
end

local function action(obs, capability, target, spell)
  return {capability=capability, sessionId=obs.sessionId, selfGuid=obs.selfGuid,
          targetGuid=target, spellId=spell, visitId=obs.visitId}
end

local function approach(obs, target, range)
  local request = action(obs, "approach_target", target)
  request.rangeYards, request.requireLos = range, true
  return request
end

local function spell_number(obs, id, field)
  local spell = (obs.spells or {})[tostring(id)] or {}
  return type(spell[field]) == "number" and spell[field] or 0
end

local function party_online(obs, patient)
  for _, member in ipairs((((obs.party or {}).group or {}).members or {})) do
    if member.guid == patient then return member.online == true end
  end
  -- A visible same-faction nonparty player may also receive a resurrection.
  -- The harness's option is the faction/body evidence for such a player.
  return true
end

local function resurrect(obs)
  if not current(obs) or obs.support.casterInCombat == true then return end
  local best, leader = nil, (((obs.party or {}).group or {}).leaderGuid)
  for _, option in ipairs(obs.support.resurrections or {}) do
    if guid(option.patientGuid) and guid(option.castTargetGuid) and option.patientGuid ~= obs.selfGuid and
       party_online(obs, option.patientGuid) and type(option.spellId) == "number" and option.spellId > 0 and
       (option.ready == true or option.canApproach == true) then
      local patient = (obs.players or {})[option.patientGuid]
      if not patient or patient.aliveKnown ~= true or patient.alive ~= true then
        local rank, best_rank = spell_number(obs, option.spellId, "rank"), best and spell_number(obs, best.spellId, "rank") or 0
        if not best or (option.ready == true and best.ready ~= true) or option.ready == best.ready and
          ((option.patientGuid == leader and best.patientGuid ~= leader) or
           (option.patientGuid == leader) == (best.patientGuid == leader) and
           (option.patientGuid < best.patientGuid or option.patientGuid == best.patientGuid and
            (rank > best_rank or rank == best_rank and option.spellId < best.spellId))) then
          best = option
        end
      end
    end
  end
  if not best then return end
  if obs.activeMeleeTargetGuid and obs.activeMeleeTargetGuid ~= "0" then
    return action(obs, "melee_stop", obs.activeMeleeTargetGuid)
  end
  if best.ready ~= true then return approach(obs, best.castTargetGuid, best.rangeYards) end
  local request = action(obs, "resurrect", best.patientGuid, best.spellId)
  request.castTargetGuid = best.castTargetGuid
  return request
end

local function interrupt(obs)
  if not current(obs) then return end
  local best
  for _, option in ipairs(obs.support.interrupts or {}) do
    local target = (obs.targets or {})[option.targetGuid]
    local fighting = target and (target.engaged == true or (obs.engagement or {})[option.targetGuid] or
      target.unitFlagsKnown == true and type(target.unitFlags) == "number" and math.floor(target.unitFlags / 524288) % 2 == 1)
    if guid(option.targetGuid) and guid(option.castRevision) and type(option.spellId) == "number" and option.spellId > 0 and
       type(option.remainingMs) == "number" and option.remainingMs > 0 and
       (option.ready == true or option.canApproach == true) and fighting and target.present == true and
       target.aliveKnown == true and target.alive == true and target.attackable == true and target.protected ~= true then
      if not best or option.ready == true and best.ready ~= true or option.ready == best.ready and
        (option.healing == true and best.healing ~= true or option.healing == best.healing and
         (option.remainingMs < best.remainingMs or option.remainingMs == best.remainingMs and
          (option.targetGuid < best.targetGuid or option.targetGuid == best.targetGuid and option.spellId < best.spellId))) then
        best = option
      end
    end
  end
  if not best then return end
  if best.ready ~= true then return approach(obs, best.targetGuid, best.rangeYards) end
  local request = action(obs, "interrupt", best.targetGuid, best.spellId)
  request.castRevision = best.castRevision
  return request
end

return {resurrect=resurrect, interrupt=interrupt}
