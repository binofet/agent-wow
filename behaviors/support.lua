-- Shared support decisions. Go supplies observed bodies, active casts and
-- mechanical spell eligibility; all ordering and movement choices live here.
local state={blocked={}}
local cleanse_tuning={aura_weights={[3]=2,[5]=5,[6]=5,[7]=5,[12]=4,[26]=3,[27]=4},spell_weights={}}
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

local function number(n) return type(n)=="number" and n==n and n-n==0 end
local function target_before(a,b) return #a<#b or #a==#b and a<b end
local function blocked(target,slot,revision)
  local old=(state.blocked[target] or {})[tostring(slot)]
  return old and old.revision==revision
end
local function on_event(event)
  if event.kind=="encounter_observation" and type(event.value)=="table" then
    local obs=event.value
    if state.sessionId and state.sessionId~=obs.sessionId then state.blocked={} end
    state.sessionId=obs.sessionId
    for _,set in ipairs((obs.support or {}).playerAuras or {}) do
      local prior=state.blocked[set.targetGuid]
      if prior then
        local seen={}
        for _,aura in ipairs(set.auras or {}) do seen[tostring(aura.slot or 0)]=aura end
        for slot,attempt in pairs(prior) do
          local aura=seen[slot]
          if aura and aura.revision~=attempt.revision or not aura and set.complete==true then prior[slot]=nil end
        end
      end
    end
  end
  local r=event.receipt
  local p=r and r.payload or {}
  if p.capability=="cleanse" and guid(p.target_guid) and guid(p.aura_revision) and
     number(p.aura_slot) and p.aura_slot>=0 and p.aura_slot<255 then
    local target,slot=p.target_guid,tostring(p.aura_slot)
    state.blocked[target]=state.blocked[target] or {}
    local prior=state.blocked[target][slot]
    if r.outcome=="failed" or r.outcome=="not_dispatched" then
      if prior and prior.revision==p.aura_revision then state.blocked[target][slot]=nil end
    elseif r.outcome=="transport_accepted" or r.outcome=="unknown" or r.outcome=="confirmed" or r.outcome=="settled_unverified" then
      state.blocked[target][slot]={revision=p.aura_revision}
    end
  end
  return true
end

local function cleanse(obs)
  if not current(obs) then return end
  local members={[obs.selfGuid]=true}
  local group=(obs.party or {}).group or {}
  if group.known==true and group.inGroup==true then
    for _,member in ipairs(group.members or {}) do if member.online==true then members[member.guid]=true end end
  end
  local owner=(obs.navigation or {}).ownerGuid
  local best
  local function better(a,b)
    if not b then return true end
    if a.weight~=b.weight then return a.weight>b.weight end
    if a.health~=b.health then return a.health<b.health end
    if a.owner~=b.owner then return a.owner end
    if a.target~=b.target then return target_before(a.target,b.target) end
    if a.slot~=b.slot then return a.slot<b.slot end
    if a.cost~=b.cost then return a.cost<b.cost end
    if a.rank~=b.rank then return a.rank>b.rank end
    return a.spell<b.spell
  end
  for _,set in ipairs(obs.support.playerAuras or {}) do
    local patient=(obs.players or {})[set.targetGuid]
    if members[set.targetGuid] and patient and patient.present==true and patient.aliveKnown==true and patient.alive==true and
       patient.mapKnown==true and patient.mapId==obs.mapId and patient.positionKnown==true then
      local fraction=1
      if patient.healthKnown==true and number(patient.health) and number(patient.maxHealth) and patient.maxHealth>0 then
        fraction=patient.health/patient.maxHealth
      end
      for _,aura in ipairs(set.auras or {}) do
        local slot=aura.slot or 0
        local dtype=aura.dispelType
        if aura.negative==true and aura.metadataKnown==true and guid(aura.revision) and
           number(aura.spellId) and number(slot) and slot>=0 and slot<255 and
           number(dtype) and dtype>=1 and dtype<=4 and
           (aura.durationKnown~=true or number(aura.remainingMs) and aura.remainingMs>0) and
           not blocked(set.targetGuid,slot,aura.revision) then
          local weight=cleanse_tuning.spell_weights[aura.spellId] or 1
          for _,kind in ipairs(aura.auraTypes or {}) do weight=math.max(weight,cleanse_tuning.aura_weights[kind] or 1) end
          for key,rank in pairs(obs.spells or {}) do
            local spell=tonumber(key)
            local mask=rank.friendlyDispelMask
            if spell and rank.known==true and rank.ready==true and rank.metadataKnown==true and rank.capability=="cleanse" and
               number(mask) and math.floor(mask/2^dtype)%2==1 then
              local choice={target=set.targetGuid,slot=slot,revision=aura.revision,aura=aura.spellId,spell=spell,
                rank=rank.rank or 0,cost=tonumber(rank.cost) or 0,weight=weight,health=fraction,owner=set.targetGuid==owner,row=rank}
              if better(choice,best) then best=choice end
            end
          end
        end
      end
    end
  end
  if not best then return end
  local patient=(obs.players or {})[best.target]
  local self=(obs.players or {})[obs.selfGuid]
  local needs=best.target~=obs.selfGuid
  if needs and self and self.positionKnown==true then
    local dx,dy,dz=(self.x or 0)-(patient.x or 0),(self.y or 0)-(patient.y or 0),(self.z or 0)-(patient.z or 0)
    local distance=math.sqrt(dx*dx+dy*dy+dz*dz)
    needs=distance<(best.row.minRange or 0) or distance>(best.row.maxRange or 0)
  end
  local cast=action(obs,"cleanse",best.target,best.spell)
  cast.auraSlot,cast.auraRevision,cast.auraSpellId=best.slot,best.revision,best.aura
  cast.needsApproach=needs
  return cast
end

local function import_state(saved)
  if type(saved)~="table" or saved.version~=1 or type(saved.data)~="table" or type(saved.data.blocked)~="table" then return false end
  state=saved.data
  return true
end
return {resurrect=resurrect,interrupt=interrupt,cleanse=cleanse,on_event=on_event,
        cleanse_tuning=cleanse_tuning,export_state=function()return {version=1,data=state} end,import_state=import_state}
