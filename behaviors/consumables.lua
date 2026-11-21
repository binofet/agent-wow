-- Item choice, reserves, resting and retry timing belong to this editable policy.
-- Inventory supplies exact owned slots, catalog facts and observed use effects.
local M={}
local tuning={drink_below_percent=70,drink_until_percent=100,drink_reserve=0,
              food_reserve=0,automatic_buff_food=true,food_only_in_dungeons=true,
              lease_ms=2000,renew_below_ms=1000,retry_ms=5000,stat_weights={}}
local state={}
local inventory,inventory_sequence
-- Rogues prepare buff food outdoors too; absent class choices retain the default.
local class_food_policy={[4]={food_only_in_dungeons=false}}
local function number(v) return type(v)=="number" and v==v and v-v==0 end
local function value(v) return tonumber(v) or 0 end
local function guid(v) return type(v)=="string" and #v>0 and v~="0" end
local function newer(a,b) return type(a)=="string" and type(b)=="string" and (#a>#b or #a==#b and a>b) end
local function alive(p) return p and p.present==true and p.aliveKnown==true and p.alive==true end
local function aura(set,id)
  for _,a in ipairs((set or {}).auras or {}) do
    if a.spellId==id and a.positive==true and (a.durationKnown~=true or value(a.remainingMs)>0) then return a end
  end
end
local function quiet(obs)
  local self=(obs.players or {})[obs.selfGuid]
  if not alive(self) or self.inCombat==true then return false end
  for _,npc in pairs(obs.targets or {}) do if alive(npc) and npc.engaged==true then return false end end
  local owner=(obs.players or {})[(obs.navigation or {}).ownerGuid]
  if owner and owner.present==true and (owner.inCombat==true or owner.movementKnown~=true or owner.moving==true) then return false end
  return true
end
local function source_before(a,b)
  if value(a.bag)~=value(b.bag) then return value(a.bag)<value(b.bag) end
  if value(a.slot)~=value(b.slot) then return value(a.slot)<value(b.slot) end
  return (a.itemGuid or "")<(b.itemGuid or "")
end

-- The same selector is used by automatic resting and explicit legacy requests.
function M.choose(view,obs,requested,fallback_food)
  if not view or view.sessionId~=obs.sessionId or view.selfGuid~=obs.selfGuid or view.inventoryKnown~=true or
     type(view.consumableContext)~="table" or not quiet(obs) then return end
  local self=(obs.players or {})[obs.selfGuid]
  local mana=self.powerKnown==true and self.powerType==0 and value(self.maxPower)>0
  local percent=mana and value(self.power)*100/value(self.maxPower) or 100
  local want_drink=mana and percent<(requested=="drink" and 100 or tuning.drink_below_percent)
  local set=(view.consumableContext or {}).selfAuras
  local food_count,food_known=0,true
  for _,item in ipairs(view.items or {}) do
    if item.metadataKnown~=true then food_known=false end
    for _,spell in ipairs((item.consumable or {}).spells or {}) do
      if item.metadataKnown==true and spell.foodSupported==true then food_count=food_count+value((item.item or {}).stackCount) end
    end
  end
  local food_only_in_dungeons=tuning.food_only_in_dungeons
  local class_policy
  if self.classKnown==true then class_policy=class_food_policy[self.classId] end
  if class_policy and class_policy.food_only_in_dungeons~=nil then
    food_only_in_dungeons=class_policy.food_only_in_dungeons
  end
  local kind=fallback_food and "eat_buff_food" or requested or (want_drink and "drink" or "eat_buff_food")
  if kind=="drink" and not want_drink then return end
  if kind=="eat_buff_food" and (not requested and (tuning.automatic_buff_food~=true or
     food_only_in_dungeons and (view.consumableContext or {}).dungeon~=true) or not set or set.complete~=true or (view.consumableContext or {}).foodBuffKnown~=true or (view.consumableContext or {}).foodBuffPresent==true) then return end
  if kind=="drink" and tuning.drink_reserve>0 and
     (view.manaDrinkCount==nil or value(view.manaDrinkCount)<=tuning.drink_reserve) then return end
  if kind=="eat_buff_food" and tuning.food_reserve>0 and (not food_known or food_count<=tuning.food_reserve) then return end
  -- Any carried food's observed Well Fed aura prevents wasting another food.
  for _,item in ipairs(view.items or {}) do for _,spell in ipairs((item.consumable or {}).spells or {}) do
    if spell.foodSupported==true and aura(set,spell.foodAuraSpellId) then if kind=="eat_buff_food" then return end end
  end end
  local best
  for _,item in ipairs(view.items or {}) do
    local source=item.item or {}
    if item.metadataKnown==true and (item.consumable or {}).usable==true and guid(source.itemGuid) and value(source.stackCount)>0 then
      for _,spell in ipairs(item.consumable.spells or {}) do
        local supported=kind=="drink" and spell.drinkSupported==true or kind=="eat_buff_food" and spell.foodSupported==true
        if supported then
          local score=value(spell.manaPerTick)
          if kind=="eat_buff_food" then
            score=0
            for _,effect in ipairs(spell.foodEffects or {}) do
              local weight=tuning.stat_weights[effect.miscValue] or 1
              score=score+math.max(0,value(effect.points))*weight
            end
          end
          if not best or score>best.score or score==best.score and source_before(source,best.source) then
            best={kind=kind,source=source,spellId=spell.spellId,auraId=kind=="drink" and spell.spellId or spell.foodAuraSpellId,
                  score=score,inventoryRevision=view.inventoryRevision}
          end
        end
      end
    end
  end
  if not best and kind=="drink" and not requested then return M.choose(view,obs,nil,true) end
  return best
end

local function custody(frame,obs,mode)
  return {capability="movement_owner",sessionId=frame.session_id,selfGuid=obs.selfGuid,
    owner=state.owner,mode=mode,leaseMs=tuning.lease_ms,token=state.token}
end
local function release(frame,obs,available,phase,retire)
  if retire~=nil then state.keepAttempt=not retire and state.attempt and state.attempt.sent end
  state.phase=phase
  state.finished=true
  if state.token and available then return {phase=phase,action=custody(frame,obs,"release")} end
  if not state.token and not state.leaseUnknown then
    if state.keepAttempt then state.restAbandoned=true else state.attempt=nil end
    state.owner=nil;state.finished=nil;state.keepAttempt=nil
  end
  return {phase=phase}
end

function M.on_event(event)
  if event.kind=="inventory_observation" then inventory,inventory_sequence=event.value,event.sequence end
  if event.kind=="consumable_request" then
    local kind=(event.value or {}).kind
    if kind=="drink" or kind=="eat_buff_food" then state.requested=kind end
  end
  local r=event.receipt
  local p=r and r.payload or {}
  if p.capability=="movement_owner" and p.owner==state.owner then
    if r.outcome=="confirmed" then
      if p.mode=="release" then
        state.token=nil;state.leaseUnknown=nil
        if state.keepAttempt then state.restAbandoned=true else state.attempt=nil end
        state.owner=nil;state.finished=nil;state.keepAttempt=nil
      elseif p.result and guid(p.result.token) then state.token=p.result.token;state.leaseUnknown=nil end
    elseif r.outcome=="settled_unverified" then state.token=nil;state.leaseUnknown=true
    elseif r.outcome=="not_dispatched" or r.outcome=="failed" then state.phase="movement_lease_refused" end
  elseif p.capability=="consume" and state.attempt and p.requestId==state.attempt.id then
    if r.outcome=="not_dispatched" or r.outcome=="failed" then state.finished=true;state.keepAttempt=false;state.retryNeeded=true;state.phase="use_not_dispatched"
    else state.attempt.sent=true;state.phase="awaiting_consumption_and_aura" end
  end
  return true
end

function M.decide(frame,obs,available)
  local view=inventory
  if not view or view.sessionId~=frame.session_id or view.selfGuid~=obs.selfGuid then return {phase="inventory_context_unavailable"} end
  if not frame.clock or not number(frame.clock.monotonicMs) or type((obs.navigation or {}).movementOwner)~="table" then
    return {phase="clock_or_movement_lease_unavailable"}
  end
  local now=frame.clock.monotonicMs
  local owner=(obs.navigation or {}).movementOwner
  if state.session and (state.session~=frame.session_id or state.self~=obs.selfGuid) then state={};return {phase="body_changed"} end
  state.session,state.self=frame.session_id,obs.selfGuid
  if state.owner and owner.active==true and owner.owner~=state.owner then
    state.token=nil;state.leaseUnknown=nil;state.owner=nil
    if state.attempt and state.attempt.sent then state.restAbandoned=true else state.attempt=nil end
    return {phase="movement_owner_replaced"}
  end
  if state.owner and owner.active~=true then
    state.token=nil;state.leaseUnknown=nil
    if state.finished then
      if state.keepAttempt then state.restAbandoned=true else state.attempt=nil end
      state.owner=nil;state.finished=nil;state.keepAttempt=nil
    end
  end
  if state.finished then
    if state.retryNeeded then state.retryAt=now+tuning.retry_ms;state.retryNeeded=nil end
    return release(frame,obs,available,state.phase or "finished")
  end
  local attempt=state.attempt
  if attempt and (not quiet(obs) or attempt.visit~=obs.visitId) then
    return release(frame,obs,available,"rest_interrupted",not attempt.sent)
  end
  if attempt and attempt.sent then
    local receipt=view.lastConsumableUse
    local active_use=aura((view.consumableContext or {}).selfAuras,attempt.spellId)
    if receipt and receipt.requestId==attempt.id then
      if receipt.serverRejected==true then state.retryAt=now+tuning.retry_ms;return release(frame,obs,available,"server_rejected",true) end
      local set=(view.consumableContext or {}).selfAuras
      local use_aura=aura(set,attempt.spellId)
      attempt.sawUseAura=attempt.sawUseAura or use_aura~=nil
      if receipt.inventoryConfirmed==true and receipt.auraObserved==true then
        if attempt.kind=="eat_buff_food" then return release(frame,obs,available,"food_buff_observed",true) end
        local self=(obs.players or {})[obs.selfGuid] or {}
        if self.powerKnown==true and value(self.maxPower)>0 and value(self.power)*100>=value(self.maxPower)*tuning.drink_until_percent then
          return release(frame,obs,available,"mana_restored",true)
        end
        if attempt.sawUseAura and set and set.complete==true and not use_aura then return release(frame,obs,available,"drink_aura_ended",true) end
      end
      if receipt.inventoryConfirmed==true and set and set.complete==true and
         newer(set.revision,receipt.baselineAuraRevision) and not use_aura and not aura(set,attempt.auraId) then
        state.retryAt=now+tuning.retry_ms
        return release(frame,obs,available,"consumed_rest_interrupted",true)
      end
    end
    -- An ambiguous write remains this exact private intent across reload. It
    -- never monopolizes the global action ledger or repeats the item use.
    if state.token and available and value(owner.remainingMs)<=tuning.renew_below_ms then
      if active_use then return {phase=state.phase,hold=true,action=custody(frame,obs,"renew")} end
      return release(frame,obs,available,"consumable_effect_unverified",false)
    end
    return {phase=state.phase,hold=state.token~=nil and not state.restAbandoned}
  end
  if not available then return {phase=state.phase} end
  if not attempt then
    if owner.active==true then return {phase="other_movement_owner"} end
    if state.retryAt and now<state.retryAt then return {phase="retry_wait"} end
    local choice=M.choose(view,obs,state.requested)
    if not choice then
      if view.inventoryKnown==true and (view.consumableContext or {}).bodyReady==true then state.requested=nil end
      return {phase="no_consumable_needed_or_available"}
    end
    choice.id="consume_"..tostring(frame.cursor).."_"..tostring(now)
    choice.visit=obs.visitId
    choice.requested=state.requested
    state.restAbandoned=nil;state.attempt=choice;state.owner="consumable:"..choice.id;state.requested=nil;attempt=choice
  end
  if state.leaseUnknown then return {phase="movement_lease_unconfirmed",hold=true} end
  if not state.token then
    if owner.active==true then return {phase="other_movement_owner"} end
    return {phase="acquiring_rest_position",hold=true,action=custody(frame,obs,"acquire")}
  end
  if inventory_sequence~=(frame.input.observationSequences or {}).inventory then return {phase="awaiting_current_inventory",hold=true} end
  if not (view.consumableContext or {}).bodyReady then return {phase="awaiting_stationary_body",hold=true} end
  -- Custody may have required several observations. Re-run this same selector
  -- against the current facts/source before the first irreversible item use.
  local selected=M.choose(view,obs,attempt.requested)
  if not selected then return release(frame,obs,available,"rest_no_longer_needed",true) end
  selected.id,selected.visit,selected.requested=attempt.id,attempt.visit,attempt.requested
  state.attempt=selected;attempt=selected
  return {phase="using_selected_item",hold=true,action={capability="consume",sessionId=frame.session_id,selfGuid=obs.selfGuid,
    kind=attempt.kind,requestId=attempt.id,source=attempt.source,spellId=attempt.spellId,expectedInventoryRevision=attempt.inventoryRevision}}
end

function M.export_state() return {version=1,data=state} end
function M.import_state(checkpoint)
  if checkpoint.version~=1 or type(checkpoint.data)~="table" then return false end
  state=checkpoint.data;inventory=nil;inventory_sequence=nil;return true
end
return M
