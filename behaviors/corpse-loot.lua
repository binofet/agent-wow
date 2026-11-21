-- One editable policy for ordinary corpse choice, movement and opening.
-- Exact packet/effect settlement is native; all ordinary window choices are here.
local M={tuning={searchRange=12,maxHeight=1.5,openRange=3.75,responseTimeoutMs=2000,windowTimeoutMs=12000,
 reserveQuestSlots=1,resumeMinFreeSlots=1}}
local state={seen={},paused={},sequence=0}
local observed
local function id(value)return type(value)=="string" and value~="" and value~="0" end
local function finite(value)local n=tonumber(value);return n and n==n and n-n==0 and n end
local function pose(c)
 if not c or c.positionKnown~=true then return end
 local x,y,z=c.x or 0,c.y or 0,c.z or 0
 local t=c.trajectory
 if t and t.known==true then
  if t.transportRelative or not t.stopped and (t.cyclic or not t.destinationKnown or tonumber(t.ageMs or 0)<(t.durationMs or 0)) then return end
  if not t.stopped then x,y,z=t.destinationX or 0,t.destinationY or 0,t.destinationZ or 0 end
 end
 if finite(x) and finite(y) and finite(z) then return x,y,z end
end
local function distance(p,c)
 local x,y,z=pose(c)
 if x and p and p.known and p.mapKnown then
  local dx,dy,dz=(p.x or 0)-x,(p.y or 0)-y,(p.z or 0)-z
  return math.sqrt(dx*dx+dy*dy+dz*dz),math.abs(dz)
 end
end
function M.on_event(event)
 if event.kind=="corpse_loot_observation" then
  observed=event.value;state.sequence=event.sequence
  if state.sessionId~=observed.sessionId then state={seen={},paused={},sessionId=observed.sessionId,sequence=event.sequence} end
  -- Absence is not proof that an uncertain corpse is a new loot source.
  for _,c in ipairs(observed.nearbyCreatures or {}) do
   if c.present and c.aliveKnown and c.alive and c.healthKnown and (c.health or 0)>0 then state.seen[c.guid]=nil end
  end
  local op=observed.operation
  if op and op.openingTransportAttempted and id(op.corpseGuid) and state.lastRequest~=op.requestId then
   state.seen[op.corpseGuid]=true;state.lastRequest=op.requestId
  end
  local fresh=observed.sourceObservation
  if fresh and fresh.present and fresh.mapKnown and (observed.self or {}).mapKnown and
   (fresh.mapId or 0)==(observed.self.mapId or 0) and fresh.aliveKnown and fresh.alive and fresh.healthKnown and (fresh.health or 0)>0 then
   state.seen[fresh.guid]=nil
  end
  if op and id(op.corpseGuid) and (observed.state or {}).inventoryPaused==true then
   state.paused[op.corpseGuid]=state.paused[op.corpseGuid] or observed.inventoryRevision
  end
  if (observed.state or {}).inventoryPaused~=true and (observed.generalFreeSlots or 0)>0 then
   for g,revision in pairs(state.paused) do
    if tostring(observed.inventoryRevision or "0")~=tostring(revision or "0") then state.seen[g]=nil;state.paused[g]=nil end
   end
  end
 elseif event.receipt and event.receipt.payload and event.receipt.payload.capability=="loot_open" then
  local p=event.receipt.payload
  if p.opening_transport_attempted and id(p.corpse_guid) then state.seen[p.corpse_guid]=true end
 end
 return true
end
function M.export_state()return {version=1,data=state}end
function M.import_state(checkpoint)
 if checkpoint.version~=1 or type(checkpoint.data)~="table" or type(checkpoint.data.seen)~="table" then return false end
 state=checkpoint.data;state.paused=state.paused or {};observed=nil;return true
end
local function window_action(frame,o,capability,slot)
 local w=o.window
 local action={capability=capability,sessionId=o.sessionId,selfGuid=o.selfGuid,corpseGuid=w.corpseGuid,
  openRequestId=w.openRequestId,windowRevision=w.revision,requestId="loot-"..tostring(state.sequence),
  observationSequence=frame.input.observationSequence}
 if slot then action.slot=slot.slot or 0;action.expectedItemId=slot.itemId;action.expectedCount=slot.count;action.expectedInventoryRevision=o.inventoryRevision end
 state.mutationKey=action.requestId;state.mutationStarted=frame.clock and frame.clock.monotonicMs
 return action
end
local function close_action(frame,o,pending)
 state.closeStarted=state.closeStarted or (frame.clock and frame.clock.monotonicMs)
 return {capability="loot_close",sessionId=o.sessionId,selfGuid=o.selfGuid,corpseGuid=pending,
  observationSequence=frame.input.observationSequence}
end
local function needed_items(o)
 local needed,active,counts={},{},{}
 if o.questLogKnown~=true then return needed,false end
 for _,row in ipairs(o.questLog or {})do if row.questId and row.questId~=0 and not row.complete and not row.failed then active[row.questId]=true end end
 for _,row in ipairs(o.itemCounts or {})do counts[row.itemId]=tonumber(row.count) end
 for _,row in ipairs(o.questRequirements or {})do
  if active[row.questId] and (counts[row.itemId] or 0)<row.count then needed[row.itemId]=true end
 end
 return needed,o.itemCountsKnown==true
end
local function process_window(frame,o,urgent)
 local out={phase="waiting_for_window",corpseGuid=(o.state or {}).pendingCorpseGuid}
 local phase=(o.state or {}).phase
 local now=frame.clock and frame.clock.monotonicMs
 local w=o.window or {}
 if state.windowKey~=w.openRequestId then state.windowKey=w.openRequestId;state.windowStarted=now;state.mutationKey=nil;state.mutationStarted=nil;state.closeStarted=nil end
 if phase=="waiting_release" then
  out.phase=phase
  if now and state.closeStarted and now-state.closeStarted>=M.tuning.responseTimeoutMs then out.action=close_action(frame,o,out.corpseGuid);out.action.retireUnconfirmed=true end
  return out
 end
 if phase=="release_observed" or phase=="loot_mutation_unconfirmed" or phase=="loot_cleanup_unconfirmed" then out.phase=phase;return out end
 local timed_out=now and state.windowStarted and now-state.windowStarted>=M.tuning.windowTimeoutMs
 if urgent or phase=="abort" or timed_out or (o.actions or {}).interactionOwner~="loot" then out.phase="release_window";out.action=close_action(frame,o,out.corpseGuid);return out end
 if w.observed~=true then
  if now and state.windowStarted and now-state.windowStarted>=M.tuning.responseTimeoutMs then out.phase="release_unobserved_window";out.action=close_action(frame,o,out.corpseGuid) end
  return out
 end
 local mutation=w.mutation
 if mutation and mutation.requestId~=state.mutationKey then state.mutationKey=mutation.requestId;state.mutationStarted=now end
 if mutation and mutation.serverRejected then
  if mutation.inventoryError==50 then state.paused[out.corpseGuid]=o.inventoryRevision end
  out.phase="release_rejected_item";out.action=close_action(frame,o,out.corpseGuid);return out
 end
 if mutation and mutation.transportAttempted and not mutation.terminal then
  out.phase="waiting_for_exact_mutation_effect"
  if now and state.mutationStarted and now-state.mutationStarted>=M.tuning.responseTimeoutMs then
   out.phase="release_uncertain_mutation";out.action=close_action(frame,o,out.corpseGuid)
  end
  return out
 end
 if (w.money or 0)>0 and not w.moneyRequested and not w.moneyCleared then
  out.phase="take_money";out.action=window_action(frame,o,"loot_money");return out
 end
 local needed,known=needed_items(o)
 local chosen,priority
 for _,slot in ipairs(w.slots or {})do
  if not slot.removed and ((slot.slotType or 0)==0 or slot.slotType==4) then
   local rank=needed[slot.itemId] and 2 or slot.slotType==4 and 1 or 0
   if not chosen or rank>priority or rank==priority and (slot.slot or 0)<(chosen.slot or 0) then chosen,priority=slot,rank end
  end
 end
 if not chosen then out.phase="window_collected";out.action=close_action(frame,o,out.corpseGuid);return out end
 local free=o.generalFreeSlots or 0
 if o.inventoryKnown~=true or free<1 or known and next(needed) and not needed[chosen.itemId] and free<=M.tuning.reserveQuestSlots then
  out.phase="release_for_quest_or_inventory_space";state.paused[out.corpseGuid]=o.inventoryRevision;out.action=close_action(frame,o,out.corpseGuid);return out
 end
 out.phase="take_selected_slot";out.action=window_action(frame,o,"loot_slot",chosen);return out
end
function M.decide(frame,encounter,quiet,urgent)
 local o=observed
 local out={phase="no_current_corpse_observation"}
 if not o or o.sessionId~=frame.session_id or o.selfGuid~=encounter.selfGuid or
  (frame.input.observationSequences or {}).corpse_loot~=tostring(state.sequence) then return out end
 local a,p,s=o.actions or {},(o.actions or {}).position,o.self or {}
 local owner=o.owner
 if owner and owner.present and owner.aliveKnown and owner.alive and owner.unitFlagsKnown and
  math.floor((owner.unitFlags or 0)/524288)%2==1 then quiet=false;urgent=true end
 if s.unitFlagsKnown and math.floor((s.unitFlags or 0)/524288)%2==1 then quiet=false;urgent=true end
 for _,player in pairs(encounter.players or {}) do if player.present and player.aliveKnown and player.alive and player.inCombat==true then quiet=false;urgent=true end end
 for _,target in pairs(encounter.targets or {}) do if target.present and target.aliveKnown and target.alive and target.engaged==true then quiet=false;urgent=true end end
 if urgent then quiet=false end
 local op=o.operation
 local pending=(o.state or {}).pendingCorpseGuid
 if id(pending) then
  if #frame.pending~=0 then return {phase="existing_action_custody",corpseGuid=pending} end
  return process_window(frame,o,urgent)
 end
 if not quiet or #frame.pending~=0 or s.present~=true or s.aliveKnown~=true or s.alive~=true or
  s.unitFlagsKnown~=true or math.floor((s.unitFlags or 0)/524288)%2==1 or
  a.castPending or a.autoAttacking or a.autoAttackRequested or a.autoAttackUnconfirmed or a.motionUnconfirmed or
  a.interactionOwner and a.interactionOwner~="" or not p or not p.known or not p.mapKnown or
  o.inventoryKnown~=true or (o.generalFreeSlots or 0)<1 then out.phase="higher_priority_or_unavailable";return out end
 local pause=(o.state or {}).capacityPause
 if pause then
  local function newer(a,b)
   a=tostring(a or "0");b=tostring(b or "0")
   return #a>#b or #a==#b and a>b
  end
  out.phase="waiting_for_new_container_capacity"
  if (o.state or {}).enabled==true and id(pause.sourceGuid) and id(pause.gatherId) and id(pause.pauseToken) and
   (o.generalFreeSlots or 0)>=M.tuning.resumeMinFreeSlots and newer(o.inventoryRevision,pause.inventoryRevision) then
   out.phase="resume_container_capacity"
   out.action={capability="loot_resume_source",sessionId=o.sessionId,selfGuid=o.selfGuid,sourceGuid=pause.sourceGuid,
    gatherId=pause.gatherId,pauseToken=pause.pauseToken,expectedInventoryRevision=o.inventoryRevision,observationSequence=frame.input.observationSequence}
  end
  return out
 end
 local retired={};for _,g in ipairs(o.retiredSources or {}) do retired[g]=true end
 local chosen,best
 for _,c in ipairs(o.corpses or {}) do
  if id(c.guid) and not state.seen[c.guid] and not state.paused[c.guid] and not retired[c.guid] and c.present==true and c.mapKnown==true and c.mapId==p.mapId and
   c.aliveKnown==true and c.alive~=true and c.healthKnown==true and (c.health or 0)==0 and (c.dynamicFlags or 0)%2==1 then
   local d,h=distance(p,c)
   if d and d<=M.tuning.searchRange and h<=M.tuning.maxHeight and
    (not chosen or d<best or d==best and c.guid<chosen.guid) then chosen,best=c,d end
  end
 end
 if not chosen then out.phase="no_eligible_corpse";return out end
 out.corpseGuid=chosen.guid;out.phase="approach_corpse"
 if best>M.tuning.openRange or a.moving then
  out.action={capability="approach_target",sessionId=o.sessionId,selfGuid=o.selfGuid,targetGuid=chosen.guid,
   visitId=encounter.visitId,rangeYards=M.tuning.openRange,requireLos=false,observationSequence=frame.input.observationSequence}
 else
  out.phase="open_selected_corpse"
  out.action={capability="loot_open",sessionId=o.sessionId,selfGuid=o.selfGuid,corpseGuid=chosen.guid,
   requestId="corpse-"..tostring(state.sequence),expectedPositionRevision=p.revision,
   expectedCorpseRevision=chosen.positionRevision,expectedInventoryRevision=o.inventoryRevision,
   observationSequence=frame.input.observationSequence}
  state.windowKey=out.action.requestId;state.windowStarted=frame.clock and frame.clock.monotonicMs;state.mutationKey=nil;state.mutationStarted=nil;state.closeStarted=nil
 end
 return out
end
return M
