-- Warrior choices belong to this reloadable class/spec script. Rage is the
-- observed native power unit (tenths), compared directly with catalog cost.
-- Generic casts retain the same native learned-spell and operation journal.
local M={}
M.profiles={
 protection={23922,6572,20243,7386,78},
 arms={12294,7384,5308,772,78},
 fury={23881,5308,78},
 leveling={6572,5308,772,78},
}
M.tuning={shield_block_health_fraction=0.60,heroic_strike_reserve=300}
local state={cooldowns={}}
local proposed
local inventory
local function number(v) local n=tonumber(v);return n and n==n and n-n==0 and n or nil end
local function shield(obs)
 local v=inventory
 if not v or v.sessionId~=obs.sessionId or v.selfGuid~=obs.selfGuid or v.equipmentKnown~=true then return false end
 for _,r in ipairs(v.equipment or {}) do if (r.slot or 0)==16 and r.slotKnown==true and r.metadataKnown==true then
  local t=((v.gearContext or {}).templates or {})[tostring(r.entry or 0)]
  return t and t.available==true and t.itemClass==4 and t.subclass==6
 end end
 return false
end
local function rank(obs,family,now)
 local best
 for key,r in pairs(obs.spells or {}) do
  local id=number(key)
  if id and id>0 and id<=4294967295 and id%1==0 and r.known==true and r.metadataKnown==true and r.familyId==family and
    r.ready==true and r.available==true and r.castTimeMs==0 and r.powerType==1 and
    now>=(state.cooldowns[tostring(family)] or 0) then
   if not best or (r.rank or 0)>(best.row.rank or 0) or r.rank==best.row.rank and id<best.id then best={id=id,row=r} end
  end
 end
 return best
end
local function range(self,target,row,point,margin)
 if self.positionKnown~=true or target.positionKnown~=true or self.mapKnown~=true or target.mapKnown~=true or self.mapId~=target.mapId then return false end
 local total=0
 for _,axis in ipairs({'x','y','z'}) do local a,b=number(self[axis] or 0),number(point[axis] or 0);if not a or not b then return false end;total=total+(a-b)^2 end
 local d=math.sqrt(total)
 return d>=(number(row.minRange) or 0) and d+margin<=(number(row.maxRange) or 0)
end
local function settled_pose(npc,current_pose)
 local path=npc.trajectory
 if path and path.known and not path.stopped then
  local age,duration=number(path.ageMs),number(path.durationMs)
  -- The path between spline endpoints is unavailable to this observation.
  if not age or not duration or age<duration then return end
 end
 return current_pose(npc)
end
-- Thunder Clap is centered on the caster (8 yards). Its glyph adds 2 yards
-- and movement leeway can add 2.66 more. Cleave's primary uses melee range.
-- its secondary searches the caster's front within 10 yards. Count only base
-- range hits, but clear the full possible footprint, including creature reach.
local function pack_choices(obs, self, focus, targets, current_pose)
 if obs.nearbyCreaturesComplete~=true or obs.spellbookKnown~=true or obs.meleeKnown~=true or not targets or not current_pose or
    self.guid~=obs.selfGuid or self.positionInferred==true or self.movementKnown~=true or type(self.moving)~='boolean' or
    (obs.navigation or {}).motionUnconfirmed==true then return end
 local origin=current_pose(self)
 if not origin then return end
 local travel=0
 if self.moving==true then
  local age,speed=number(self.positionAgeMs),number(self.groundRunSpeed)
  if self.positionAgeKnown~=true or not age or age<0 or not speed or speed<=0 then return end
  -- The native host polls every 250 ms. Allow ground travel in any direction
  -- since this pose plus one poll while an instant cast reaches the writer.
  travel=speed*(age+250)/1000
 end
 local clap_radius,cleave_radius=8,10
 local clap_count,cleave_secondary=0,false
 local clap_safe,cleave_safe,focus_in_clap=true,true,false
 local facing_x,facing_y=math.cos(origin.o),math.sin(origin.o)
 for guid,npc in pairs(obs.targets) do
  if npc.present==true and not (npc.aliveKnown==true and npc.alive==false) then
   local row=targets[guid]
   local admitted=row and row.engaged==true and not row.protected and npc.attackable==true and npc.aliveKnown==true and npc.alive==true
   local pose=settled_pose(npc,current_pose)
   if pose then
    local dx,dy,dz=pose.x-origin.x,pose.y-origin.y,pose.z-origin.z
    local d=math.sqrt(dx*dx+dy*dy+dz*dz)
    if admitted then
     if d+travel<=clap_radius then
      clap_count=clap_count+1
      focus_in_clap=focus_in_clap or guid==focus.guid
     end
     if guid~=focus.guid and d+travel<=cleave_radius and dx*facing_x+dy*facing_y>=travel then cleave_secondary=true end
    else
     local reach=number(npc.combatReach)
     if not reach or reach<0 then return end
     if d<=clap_radius+2+2.66+reach+travel then clap_safe=false end
     -- A queued swing can turn or land later. Its future center while moving
     -- is unbounded by the instant-cast window. Use it only in an all-engaged
     -- field while moving. A stationary swing still needs both sides clear.
     if self.moving==true or d<=cleave_radius+reach then cleave_safe=false end
    end
   elseif not admitted or guid==focus.guid then return end
  end
 end
 return clap_safe and focus_in_clap and clap_count>=2,cleave_safe and cleave_secondary,travel
end
local function cast_slot(family)
 if family==845 or family==78 then return 'swing' end
 if family==6343 then return 'clap' end
end
function M.on_event(e)
 if e.kind=='inventory_observation' then inventory=e.value end
 local cast,receipt=state.lastPackCast,e.receipt
 if cast and receipt then
  local p=receipt.payload or {}
  if p.capability=='cast_spell' and p.spell_id==cast.id and p.target_guid==cast.target and
     (not cast.actionId or cast.actionId==receipt.action_id) then
   cast.actionId=receipt.action_id
   if receipt.outcome=='settled_unverified' then
    -- A retired timeout can still have a queued swing. A cast GO or an
    -- explicit rejection resolves the write. Elapsed time alone cannot.
    if p.server_cast_go~=true then
     state.uncertainCasts=state.uncertainCasts or {}
     state.uncertainCasts[cast.slot]=cast.visit
    end
    state.lastPackCast=nil
   elseif receipt.outcome=='failed' or receipt.outcome=='not_dispatched' or receipt.outcome=='confirmed' then
    state.lastPackCast=nil
   end
  end
 end
 return true
end
function M.export_state() return {version=1,data=state} end
function M.import_state(c)
 if c.version~=1 or type(c.data)~='table' or type(c.data.cooldowns)~='table' then return false end
 if c.data.uncertainCasts and type(c.data.uncertainCasts)~='table' then return false end
 local cast=c.data.lastPackCast
 if cast and (type(cast)~='table' or cast.slot~='swing' and cast.slot~='clap' or not number(cast.id) or
    type(cast.target)~='string' or type(cast.visit)~='string') then return false end
 for _,v in pairs(c.data.cooldowns) do if not number(v) or v<0 then return false end end
 state=c.data;inventory=nil;return true
end
function M.choose(frame,obs,target,sequence,intent,active_melee,targets,current_pose)
 proposed=nil
 local self=(obs.players or {})[obs.selfGuid]
 if not self or self.classKnown~=true or self.classId~=1 or self.powerKnown~=true or self.powerType~=1 or
   self.aliveKnown~=true or self.alive~=true or not target or target.present~=true or target.aliveKnown~=true or target.alive~=true or
   obs.targetProtectionOverflow==true or target.protected==true or target.attackable~=true or target.engaged~=true or #(frame.pending or {})~=0 then return end
 local now=frame.clock and number(frame.clock.monotonicMs)
 if not now then return end
 if state.session~=obs.sessionId or state.self~=obs.selfGuid then state={session=obs.sessionId,self=obs.selfGuid,cooldowns={}} end
 intent=intent or {}
 local function action(family,self_only,margin)
  -- Cleave queues the next weapon swing and has no global cooldown.
  if now<(state.gcdUntil or 0) and family~=845 then return end
  local slot=cast_slot(family)
  if slot and state.uncertainCasts and state.uncertainCasts[slot]==obs.visitId then return end
  local selected=rank(obs,family,now)
  if not selected then return end
  local row=selected.row
  local point=target
  if family==845 then
   if obs.meleeKnown~=true or row.globalCooldownMs~=0 then return end
   point=settled_pose(target,current_pose)
  end
  margin=margin or 0
  if (number(self.power) or 0)<(number(row.cost) or math.huge) or not self_only and (not point or not range(self,target,row,point,margin)) then return end
  if not self_only and family~=355 then
   local orientation=number(self.orientation or 0)
   if not orientation or ((point.x or 0)-(self.x or 0))*math.cos(orientation)+((point.y or 0)-(self.y or 0))*math.sin(orientation)<margin then return end
   -- Yield to the existing approach/face actuator instead of repeatedly
   -- sending an in-range weapon ability while turned away.
  end
  if (family==23922 or family==20243 or family==2565) and not shield(obs) then return end
  if family==5308 and (target.healthKnown~=true or (target.maxHealth or 0)<=0 or target.health>(target.maxHealth*0.20)) then return end
  if (family==78 or family==845) and (obs.meleeKnown~=true or
     (number(self.power) or 0)-(number(row.cost) or 0)<M.tuning.heroic_strike_reserve) then return end
  if family==772 then for _,f in ipairs(target.ownDamageFamilies or {}) do if f==772 then return end end end
  local result={capability='cast_spell',sessionId=obs.sessionId,selfGuid=obs.selfGuid,visitId=obs.visitId,
    observationSequence=sequence,spellId=selected.id,targetGuid=self_only and obs.selfGuid or target.guid,
    targets=self_only and {} or {mask=2,objectGuid=target.guid}}
  proposed={action=result,family=family,now=now,row=row}
  return result
 end
 local tank=intent.role=='tank'
 local victim=(obs.engagement or {})[target.guid]
 victim=victim and victim.victimGuid
 if tank and victim and victim~=obs.selfGuid then
  local group=(obs.party or {}).group or {}
  if group.known==true and group.inGroup==true then for _,m in ipairs(group.members or {}) do
   if m.guid==victim and m.online==true then local a=action(355);if a then return a end end
  end end
 end
 if not active_melee then return end -- begin ordinary autoswing first
 if tank and victim==obs.selfGuid and self.healthKnown==true and (self.maxHealth or 0)>0 and self.health/self.maxHealth<=M.tuning.shield_block_health_fraction then
  local a=action(2565,true);if a then return a end
 end
 local spec=intent.spec
 local function learned(family)
  for _,r in pairs(obs.spells or {}) do if r.known==true and r.metadataKnown==true and r.familyId==family then return true end end
 end
 if not M.profiles[spec] then
  if learned(23922) or learned(20243) then spec='protection'
  elseif learned(23881) then spec='fury'
  elseif learned(12294) then spec='arms'
  else spec='leveling' end
 end
 local clap,cleave,travel
 if tank then
  clap,cleave,travel=pack_choices(obs,self,target,targets,current_pose)
  if clap then local a=action(6343,true);if a then return a end end
 end
 for _,family in ipairs(M.profiles[spec]) do
  if family==78 and cleave then local a=action(845,false,travel);if a then return a end end
  local a=action(family);if a then return a end
 end
end
function M.commit(action)
 if proposed and proposed.action==action then
  local p=proposed
  local slot=cast_slot(p.family)
  if slot then
   state.lastPackCast={id=action.spellId,slot=slot,visit=action.visitId,target=action.targets.objectGuid or '0'}
  end
  local delay=math.max(number(p.row.cooldownMs) or 0,number(p.row.categoryCooldownMs) or 0,number(p.row.globalCooldownMs) or 0)
  -- Do not overwrite an existing Rend on every global cooldown when its
  -- observation arrives late; this is policy refresh timing, not effect proof.
  if p.family==772 then delay=math.max(delay,15000) end
  state.cooldowns[tostring(p.family)]=p.now+delay
  state.gcdUntil=math.max(state.gcdUntil or 0,p.now+(number(p.row.globalCooldownMs) or 0))
 end
 proposed=nil
end
return M
