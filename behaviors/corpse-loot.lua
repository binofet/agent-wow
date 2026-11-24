-- One editable policy for corpse and quest-container choice and opening.
-- Exact packet/effect settlement is native; corpse and bound-container windows
-- share this policy. Native gathering executes only a caller-selected recipe.
local M={tuning={searchRange=12,maxHeight=1.5,openRange=3.75,responseTimeoutMs=2000,windowTimeoutMs=12000,
 reserveQuestSlots=1,resumeMinFreeSlots=1,gatherSearchRange=12,gatherOpenRange=4.5,gatherPreferNearest=true}}
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
local function uint(value,maximum)
 return type(value)=="number" and value>=0 and value<=maximum and value==math.floor(value)
end
local function default(value,fallback)if value==nil then return fallback end;return value end
local function zero(value)return default(value,0)end
local function decimal(value,zero)
 if type(value)~="string" or #value==0 or #value>20 then return false end
 if value=="0" then return zero==true end
 if byte_at(value,1)==48 then return false end
 for i=1,#value do local b=byte_at(value,i);if b<48 or b>57 then return false end end
 return #value<20 or value<="18446744073709551615"
end
local function array(value,maximum)
 if type(value)~="table" or #value>maximum then return false end
 local count=0
 for index in pairs(value) do
  if not uint(index,#value) or index==0 then return false end
  count=count+1
 end
 return count==#value
end
local function boolean(value)return value==nil or type(value)=="boolean" end
local function coordinate(value)
 return value==nil or type(value)=="number" and value==value and math.abs(value)<=40000
end
local function smaller_guid(a,b)return #a<#b or #a==#b and a<b end
local function recipe_before(a,b)
 if a.questId~=b.questId then return a.questId<b.questId end
 if a.itemId~=b.itemId then return a.itemId<b.itemId end
 if a.openSpell~=b.openSpell then return a.openSpell<b.openSpell end
 return a.count<b.count
end
local function gather_frame(o,encounter)
 -- These facts were not exposed by old modules. Their absence never grants
 -- permission to recreate native selection or to guess a recipe.
 if o.gatherCandidatesKnown~=true or o.questLogKnown~=true or
  not decimal(o.selfGuid) or not decimal(o.visitId) or o.visitId~=encounter.visitId or not decimal(o.questLogRevision) or
  not decimal(o.inventoryRevision) or not array(o.questObjects,32) or
  not array(o.gatherRecipes,128) or not array(default(o.gatherAttemptedSources,{}),128) or
  not array(o.questLog,25) or #o.questLog~=25 or not array(default(o.retiredSources,{}),128) then return end
 local active,slots,quests={},{},{}
 for _,q in ipairs(o.questLog) do
  if type(q)~="table" or not uint(zero(q.slot),24) or slots[zero(q.slot)] or
   not uint(zero(q.questId),4294967295) or not uint(zero(q.state),4294967295) or
   not boolean(q.complete) or not boolean(q.failed) or q.questId and q.questId~=0 and quests[q.questId] then return end
  slots[zero(q.slot)]=true
  if zero(q.questId)~=0 then
   quests[q.questId]=true
   if not q.complete and not q.failed and zero(q.state)%4==0 then active[q.questId]=true end
  end
 end
 local objects={}
 for _,g in ipairs(o.questObjects) do
  if type(g)~="table" or not decimal(g.guid) or objects[g.guid] or
   not uint(zero(g.entry),4294967295) or not uint(zero(g.gameObjectType),4294967295) or
   not uint(zero(g.mapId),4294967295) or not uint(zero(g.flags),4294967295) or
   not boolean(g.present) or not boolean(g.typeKnown) or not boolean(g.mapKnown) or
   not boolean(g.positionKnown) or not boolean(g.flagsKnown) or
   not decimal(default(g.positionRevision,"0"),true) or not coordinate(g.x) or not coordinate(g.y) or not coordinate(g.z) then return end
  objects[g.guid]=true
 end
 local recipes={}
 for _,r in ipairs(o.gatherRecipes) do
  if type(r)~="table" or not uint(r.objectEntry,4294967295) or r.objectEntry==0 or
   not uint(r.questId,4294967295) or r.questId==0 or not uint(r.itemId,4294967295) or r.itemId==0 or
   not uint(r.count,4294967295) or r.count==0 or not uint(r.openSpell,4294967295) or r.openSpell==0 then return end
  local key=tostring(r.objectEntry)..":"..tostring(r.questId)..":"..tostring(r.itemId)..":"..tostring(r.openSpell)..":"..tostring(r.count)
  if recipes[key] then return end
  recipes[key]=true
 end
 local attempted,retired={},{}
 for _,guid in ipairs(default(o.gatherAttemptedSources,{})) do
  if not decimal(guid) or attempted[guid] then return end
  attempted[guid]=true
 end
 for _,guid in ipairs(default(o.retiredSources,{})) do
  if not decimal(guid) or retired[guid] then return end
  retired[guid]=true
 end
 return active,attempted,retired
end
local function gather_choice(frame,o,encounter)
 if (o.state or {}).enabled~=true then return end
 local active,attempted,retired=gather_frame(o,encounter)
 if not active then return end
 local a,p,s=o.actions or {},(o.actions or {}).position,o.self or {}
 if not p or p.known~=true or p.mapKnown~=true or not decimal(p.revision) or
  not coordinate(p.x) or not coordinate(p.y) or not coordinate(p.z) or not uint(zero(p.mapId),4294967295) or
  s.guid~=o.selfGuid or s.healthKnown~=true or not uint(zero(s.health),4294967295) or zero(s.health)==0 or
  s.mapKnown~=true or not uint(zero(s.mapId),4294967295) or zero(s.mapId)~=zero(p.mapId) or s.movementFlagsKnown~=true or
  not uint(zero(s.unitFlags),4294967295) or not uint(zero(s.movementFlags),4294967295) or not uint(zero(s.extraMovementFlags),4294967295) or
  zero(s.movementFlags)>=2147483648 or zero(s.movementFlags)%268435456>511 or zero(s.extraMovementFlags)~=0 then return end
 for _,bit in ipairs({1,4,8192,262144,524288,1048576,4194304,8388608,16777216,33554432,134217728}) do
  if math.floor((s.unitFlags or 0)/bit)%2==1 then return end
 end
 local range,opening,height=M.tuning.gatherSearchRange,M.tuning.gatherOpenRange,M.tuning.maxHeight
 if type(range)~="number" or range~=range or range<=0 or range>12 or
  type(opening)~="number" or opening~=opening or opening<=0 or opening>4.5 or
  type(height)~="number" or height~=height or height<0 or height>1.5 or
  type(M.tuning.gatherPreferNearest)~="boolean" then return end
 local selected,recipe,best
 for _,g in ipairs(o.questObjects) do
  if g.present==true and g.typeKnown==true and g.gameObjectType==3 and g.mapKnown==true and
   (g.mapId or 0)==(p.mapId or 0) and g.positionKnown==true and decimal(g.positionRevision) and
   g.flagsKnown==true and (g.flags or 0)%2==0 and math.floor((g.flags or 0)/8)%2==0 and
   math.floor((g.flags or 0)/16)%2==0 and not attempted[g.guid] and not retired[g.guid] and
   not state.seen[g.guid] and not state.paused[g.guid] then
   local d,h=distance(p,g)
   if d and d<=range and h<=height then
    for _,r in ipairs(o.gatherRecipes) do
     if r.objectEntry==g.entry and active[r.questId] and (r.openSpell==3365 or r.openSpell==6477 or r.openSpell==6478) and
      (not selected or M.tuning.gatherPreferNearest and d<best or not M.tuning.gatherPreferNearest and d>best or
       d==best and (smaller_guid(g.guid,selected.guid) or g.guid==selected.guid and recipe_before(r,recipe))) then
      selected,recipe,best=g,r,d
     end
    end
   end
  end
 end
 if not selected then return end
 local approach=best>opening or a.moving==true
 return {phase=approach and "approach_selected_container" or "open_selected_container",sourceGuid=selected.guid,
  action={capability="loot_gather",sessionId=o.sessionId,selfGuid=o.selfGuid,sourceGuid=selected.guid,
   questId=recipe.questId,itemId=recipe.itemId,openSpell=recipe.openSpell,requestId="gather-"..tostring(state.sequence),
   visitId=o.visitId,expectedPositionRevision=p.revision,expectedObjectRevision=selected.positionRevision,
   expectedInventoryRevision=o.inventoryRevision,expectedQuestLogRevision=o.questLogRevision,approach=approach,
   observationSequence=frame.input.observationSequence}}
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
  if op and op.openingTransportAttempted and id(op.sourceGuid) then state.seen[op.sourceGuid]=true end
  local fresh=observed.sourceObservation
  if fresh and fresh.present and fresh.mapKnown and (observed.self or {}).mapKnown and
   (fresh.mapId or 0)==(observed.self.mapId or 0) and fresh.aliveKnown and fresh.alive and fresh.healthKnown and (fresh.health or 0)>0 then
   state.seen[fresh.guid]=nil
  end
  local pausedSource=op and (id(op.corpseGuid) and op.corpseGuid or op.sourceGuid)
  if not id(pausedSource) then pausedSource=(observed.state or {}).pendingGameObjectGuid end
  if id(pausedSource) and (observed.state or {}).inventoryPaused==true then
   state.paused[pausedSource]=state.paused[pausedSource] or observed.inventoryRevision
  end
  if (observed.state or {}).inventoryPaused~=true and (observed.generalFreeSlots or 0)>0 then
   for g,revision in pairs(state.paused) do
    if tostring(observed.inventoryRevision or "0")~=tostring(revision or "0") then state.seen[g]=nil;state.paused[g]=nil end
   end
  end
 elseif event.receipt and event.receipt.payload and event.receipt.payload.capability=="loot_open" then
  local p=event.receipt.payload
  if p.opening_transport_attempted and id(p.corpse_guid) then state.seen[p.corpse_guid]=true end
 elseif event.receipt and event.receipt.payload and event.receipt.payload.capability=="loot_gather" then
  local p,outcome=event.receipt.payload,event.receipt.outcome
  local r=p.result
  -- Acquiring custody may send STOP before a final guard refuses opening.
  -- Its exact terminal receipt permits a fresh normal decision, but cannot
  -- erase custody already retained from an earlier unknown/entered request.
  local no_open=outcome=="settled_unverified" and type(r)=="table" and
   r.phase=="acquired_not_opened" and r.terminal==true and r.sessionId==state.sessionId and
   decimal(r.selfGuid) and decimal(r.sourceGuid) and r.sourceGuid==p.source_guid and
   id(p.request_id) and r.requestId==p.request_id and
   (r.openingTransportAttempted==nil or r.openingTransportAttempted==false) and
   (r.openingTransportWritten==nil or r.openingTransportWritten==false) and
   (p.opening_transport_attempted==nil or p.opening_transport_attempted==false) and
   (p.transport_written==nil or p.transport_written==false)
  if not no_open and id(p.source_guid) and (p.opening_transport_attempted or outcome=="unknown" or
   outcome=="settled_unverified" or outcome=="confirmed") then state.seen[p.source_guid]=true end
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
 local action={capability=capability,sessionId=o.sessionId,selfGuid=o.selfGuid,corpseGuid=w.corpseGuid,sourceGuid=w.sourceKind=="quest_container" and w.sourceGuid or nil,
  openRequestId=w.openRequestId,windowRevision=w.revision,requestId="loot-"..tostring(state.sequence),
  observationSequence=frame.input.observationSequence}
 if slot then action.slot=slot.slot or 0;action.expectedItemId=slot.itemId;action.expectedCount=slot.count;action.expectedInventoryRevision=o.inventoryRevision end
 state.mutationKey=action.requestId;state.mutationStarted=frame.clock and frame.clock.monotonicMs
 return action
end
local function close_action(frame,o,pending)
 state.closeStarted=state.closeStarted or (frame.clock and frame.clock.monotonicMs)
 return {capability="loot_close",sessionId=o.sessionId,selfGuid=o.selfGuid,corpseGuid=(o.window or {}).sourceKind~="quest_container" and pending or nil,sourceGuid=(o.window or {}).sourceKind=="quest_container" and pending or nil,
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
 local pending=(o.state or {}).pendingCorpseGuid
 if not id(pending) then pending=(o.state or {}).pendingGameObjectGuid end
 local out={phase="waiting_for_window",corpseGuid=pending}
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
 if w.sourceKind=="quest_container" then
  local active=false
  for _,q in ipairs(o.questLog or {})do if q.questId==w.boundQuestId and not q.complete and not q.failed then active=true end end
  if (o.state or {}).enabled~=true or o.questLogKnown~=true or not active or
   (o.sourceObject or {}).present~=true or o.sourceObject.guid~=w.sourceGuid then
   out.phase="bound_container_or_quest_unavailable";out.action=close_action(frame,o,out.corpseGuid);return out
  end
 end
 if (w.money or 0)>0 and not w.moneyRequested and not w.moneyCleared then
  out.phase="take_money";out.action=window_action(frame,o,"loot_money");return out
 end
 local needed,known=needed_items(o)
 local chosen,priority
 for _,slot in ipairs(w.slots or {})do
  if not slot.removed and ((slot.slotType or 0)==0 or slot.slotType==4) and (w.sourceKind~="quest_container" or slot.itemId==w.boundItemId) then
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
 if not id(pending) then pending=(o.state or {}).pendingGameObjectGuid end
 if id(pending) then
  if (o.state or {}).pendingGameObjectGuid==pending and (o.window or {}).observed~=true then return {phase="retained_container_opening",corpseGuid=pending} end
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
 local gathering=gather_choice(frame,o,encounter)
 if gathering then return gathering end
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
