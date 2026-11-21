-- One editable policy for ordinary corpse choice, movement and opening.
-- Window/slot packet settlement is native; it does not choose the next corpse.
local M={tuning={searchRange=12,maxHeight=1.5,openRange=3.75}}
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
  if op and id(op.corpseGuid) and (observed.state or {}).inventoryPaused==true then
   state.paused[op.corpseGuid]=observed.inventoryRevision
  end
  if (observed.state or {}).inventoryPaused~=true and (observed.generalFreeSlots or 0)>0 then
   for g,revision in pairs(state.paused) do
    if observed.inventoryRevision~=revision then state.seen[g]=nil;state.paused[g]=nil end
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
function M.decide(frame,encounter,quiet,urgent)
 local o=observed
 local out={phase="no_current_corpse_observation"}
 if not o or o.sessionId~=frame.session_id or o.selfGuid~=encounter.selfGuid or
  (frame.input.observationSequences or {}).corpse_loot~=tostring(state.sequence) then return out end
 local a,p,s=o.actions or {},(o.actions or {}).position,o.self or {}
 local op=o.operation
 local pending=(o.state or {}).pendingCorpseGuid
 if id(pending) then
  out.phase="native_window_settlement";out.corpseGuid=pending
  if urgent and op and not op.terminal and op.corpseGuid==pending and (o.state or {}).phase~="waiting_release" then
   out.action={capability="loot_close",sessionId=o.sessionId,selfGuid=o.selfGuid,corpseGuid=pending,
    observationSequence=frame.input.observationSequence}
  end
  return out
 end
 if not quiet or #frame.pending~=0 or s.present~=true or s.aliveKnown~=true or s.alive~=true or
  a.castPending or a.autoAttacking or a.autoAttackRequested or a.autoAttackUnconfirmed or a.motionUnconfirmed or
  a.interactionOwner and a.interactionOwner~="" or not p or not p.known or not p.mapKnown or
  o.inventoryKnown~=true or (o.generalFreeSlots or 0)<1 then out.phase="higher_priority_or_unavailable";return out end
 local retired={};for _,g in ipairs(o.retiredSources or {}) do retired[g]=true end
 local chosen,best
 for _,c in ipairs(o.corpses or {}) do
  if id(c.guid) and not state.seen[c.guid] and not retired[c.guid] and c.present==true and c.mapKnown==true and c.mapId==p.mapId and
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
 end
 return out
end
return M
