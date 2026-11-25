-- Per-body equipment lifecycle. The shared gear policy chooses upgrades; the
-- native operation verifies the exact swap. No party leader or town controls.
local M={}
local inventory,state=nil,{}
local proposed
local function n(v) return tonumber(v) or 0 end
local function terminal(op)
 return op and op.leaseReleased==true and (op.status=="CONFIRMED" or op.status=="BLOCKED" or op.status=="SERVER_REJECTED")
end
local function key(v)
 return tostring(v.inventoryRevision)..":"..tostring(v.equipmentRevision)..(v.ammoKnown==true and ":"..tostring(v.ammoRevision) or "")
end
function M.on_event(e)
 if e.kind=="inventory_observation" then inventory=e.value end
 local r=e.receipt
 if r and r.payload and (r.payload.capability=="equip_gear" or r.payload.capability=="select_ammo") and state.attempt and r.payload.requestId==state.attempt.requestId then
  if r.outcome=="not_dispatched" or r.outcome=="failed" then state.blocked=state.attempt.vector;state.blockedKind=state.attempt.capability or "equip_gear";state.attempt=nil end
 end
 return true
end
function M.export_state() return {version=1,data=state} end
function M.import_state(c)
 if c.version~=1 or type(c.data)~="table" then return false end
 if c.data.attempt and (type(c.data.attempt)~="table" or type(c.data.attempt.requestId)~="string" or type(c.data.attempt.vector)~="string") then return false end
 state=c.data;inventory=nil;return true
end
function M.decide(frame,obs,available,gear)
 proposed=nil
 local v=inventory
 if not v or not obs or v.sessionId~=frame.session_id or v.selfGuid~=obs.selfGuid or obs.sessionId~=v.sessionId then return {phase="equipment_context_unavailable"} end
 if state.session and (state.session~=v.sessionId or state.self~=v.selfGuid) then
  -- A retained unresolved attempt belongs to its original body, never replay.
  if state.attempt then return {phase="prior_body_equipment_unresolved"} end
  state={}
 end
 state.session,state.self=v.sessionId,v.selfGuid
 local a=state.attempt
 if a then
  local op=v.operation
  if not op or op.requestId~=a.requestId or op.kind~=(a.capability or "equip_gear") then return {phase="awaiting_equipment_receipt"} end
  if not terminal(op) then return {phase="equipment_outcome_unresolved"} end
  if op.status=="CONFIRMED" and a.capability=="select_ammo" then
   if op.ammoConfirmed~=true or op.inventoryConfirmed~=true or op.equipmentConfirmed~=true or v.ammoKnown~=true or n(v.ammoEntry)~=n(a.source.entry) or n(v.ammoRevision)<=n(a.expectedAmmoRevision) then return {phase="ammo_selection_proof_incomplete"} end
  elseif op.status=="CONFIRMED" then
   if op.inventoryConfirmed~=true or op.equipmentConfirmed~=true or n(op.observedInventoryRevision)<=n(a.expectedInventoryRevision) or n(op.observedEquipmentRevision)<=n(a.expectedEquipmentRevision) then return {phase="equipment_proof_incomplete"} end
   local worn,source
   for _,r in ipairs(v.equipment or {}) do if n(r.slot)==a.equipmentSlot then worn=r end end
   for _,b in ipairs(v.inventoryBags or {}) do if n(b.bag)==n(a.source.bag) then for _,r in ipairs(b.slots or {}) do if n(r.slot)==n(a.source.slot) then source=r end end end end
   if not worn or worn.itemGuid~=a.source.itemGuid or not source or (source.itemGuid or "0")~=a.expectedEquippedItemGuid then return {phase="equipment_swap_not_observed"} end
  end
  state.blocked=a.vector;state.blockedKind=a.capability or "equip_gear";state.attempt=nil
  return {phase=a.capability=="select_ammo" and (op.status=="CONFIRMED" and "ammunition_confirmed" or "ammunition_refused") or (op.status=="CONFIRMED" and "equipment_confirmed" or "equipment_refused")}
 end
 if not available or v.active==true or v.closing==true or v.operation and not terminal(v.operation) or v.inventoryKnown~=true or v.equipmentKnown~=true or n(v.inventoryRevision)<=0 or n(v.equipmentRevision)<=0 or n(v.revision)<=0 then return {phase="equipment_busy_or_unknown"} end
 local blocked=state.blocked==key(v)
 if blocked and v.ammoKnown~=true then return {phase="equipment_waits_for_changed_items"} end
 local self=(obs.players or {})[obs.selfGuid]
 if not self or self.present~=true or self.aliveKnown~=true or self.alive~=true or self.combatKnown~=true or self.inCombat~=false or self.movementKnown~=true or self.moving~=false or (obs.navigation or {}).motionUnconfirmed==true or ((obs.interaction or {}).owner or "")~="" then return {phase="equipment_waits_for_quiet"} end
 for _,t in pairs(obs.targets or {}) do if t.engaged==true and t.alive~=false then return {phase="equipment_waits_for_engagement"} end end
 if not blocked or state.blockedKind=="select_ammo" then
 for _,row in ipairs(gear.evaluate({inventory=v}).items or {}) do
  if row.metadataKnown==true and row.gearEquipEligible==true and row.conservativeUpgrade==true then
   local source=row.item or {}
   if type(source.itemGuid)=="string" and source.itemGuid~="0" and n(source.stackCount)==1 then
    local request={capability="equip_gear",sessionId=v.sessionId,selfGuid=v.selfGuid,
     requestId="gear-"..tostring(frame.cursor).."-"..source.itemGuid,
     expectedRevision=v.revision,expectedInventoryRevision=v.inventoryRevision,source=source,
     equipmentSlot=row.gearEquipmentSlot,expectedEquipmentRevision=v.equipmentRevision,expectedEquippedItemGuid=row.expectedEquippedItemGuid,vector=key(v)}
    proposed=request
    return {phase="equipment_requested",action=request}
   end
  end
 end
 end
 if blocked and state.blockedKind=="select_ammo" then return {phase="ammunition_waits_for_changed_items"} end
 -- Ammunition choice stays in editable Lua. Keep an already-selected
 -- compatible carried stack; otherwise choose the first eligible owned stack.
 if v.ammoKnown==true and n(v.ammoRevision)>0 then
  for _,row in ipairs(v.items or {}) do
   if row.ammoSelectEligible==true and n((row.item or {}).entry)==n(v.ammoEntry) then return {phase="ammunition_ready"} end
  end
  local ranged
  for _,item in ipairs(v.equipment or {}) do if n(item.slot)==17 and item.slotKnown==true then ranged=item end end
  for _,row in ipairs(v.items or {}) do
   local source=row.item or {}
   if row.ammoSelectEligible==true and ranged and type(ranged.itemGuid)=="string" and ranged.itemGuid~="0" and type(source.itemGuid)=="string" and source.itemGuid~="0" and n(source.stackCount)>0 then
    local request={capability="select_ammo",sessionId=v.sessionId,selfGuid=v.selfGuid,
     requestId="ammo-"..tostring(frame.cursor).."-"..source.itemGuid,
     expectedRevision=v.revision,expectedInventoryRevision=v.inventoryRevision,source=source,
     expectedAmmoRevision=v.ammoRevision,expectedAmmoEntry=v.ammoEntry or 0,
     expectedEquipmentRevision=v.equipmentRevision,expectedRangedItemGuid=ranged.itemGuid,vector=key(v)}
    proposed=request;return {phase="ammunition_requested",action=request}
   end
  end
 end
 return {phase=blocked and "equipment_waits_for_changed_items" or "no_equipment_upgrade"}
end
-- Only final arbitration may turn a proposal into a retained native intent.
-- A loot close, requested-spell maintenance or cancellation may supersede it.
function M.commit(action)
 if proposed and action==proposed then state.attempt=proposed end
 proposed=nil
end
return M
