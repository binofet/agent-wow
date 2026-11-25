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
local function range(self,target,row)
 if self.positionKnown~=true or target.positionKnown~=true or self.mapKnown~=true or target.mapKnown~=true or self.mapId~=target.mapId then return false end
 local total=0
 for _,axis in ipairs({'x','y','z'}) do local a,b=number(self[axis] or 0),number(target[axis] or 0);if not a or not b then return false end;total=total+(a-b)^2 end
 local d=math.sqrt(total)
 return d>=(number(row.minRange) or 0) and d<=(number(row.maxRange) or 0)
end
function M.on_event(e)
 if e.kind=='inventory_observation' then inventory=e.value end
 return true
end
function M.export_state() return {version=1,data=state} end
function M.import_state(c)
 if c.version~=1 or type(c.data)~='table' or type(c.data.cooldowns)~='table' then return false end
 for _,v in pairs(c.data.cooldowns) do if not number(v) or v<0 then return false end end
 state=c.data;inventory=nil;return true
end
function M.choose(frame,obs,target,sequence,intent,active_melee)
 proposed=nil
 local self=(obs.players or {})[obs.selfGuid]
 if not self or self.classKnown~=true or self.classId~=1 or self.powerKnown~=true or self.powerType~=1 or
   self.aliveKnown~=true or self.alive~=true or not target or target.present~=true or target.aliveKnown~=true or target.alive~=true or
   obs.targetProtectionOverflow==true or target.protected==true or target.attackable~=true or target.engaged~=true or #(frame.pending or {})~=0 then return end
 local now=frame.clock and number(frame.clock.monotonicMs)
 if not now then return end
 if state.session~=obs.sessionId or state.self~=obs.selfGuid then state={session=obs.sessionId,self=obs.selfGuid,cooldowns={}} end
 if now<(state.gcdUntil or 0) then return end
 intent=intent or {}
 local function action(family,self_only)
  local selected=rank(obs,family,now)
  if not selected then return end
  local row=selected.row
  if (number(self.power) or 0)<(number(row.cost) or math.huge) or not self_only and not range(self,target,row) then return end
  if (family==23922 or family==20243 or family==2565) and not shield(obs) then return end
  if family==5308 and (target.healthKnown~=true or (target.maxHealth or 0)<=0 or target.health>(target.maxHealth*0.20)) then return end
  if family==78 and (number(self.power) or 0)-(number(row.cost) or 0)<M.tuning.heroic_strike_reserve then return end
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
 for _,family in ipairs(M.profiles[spec]) do local a=action(family);if a then return a end end
end
function M.commit(action)
 if proposed and proposed.action==action then
  local p=proposed
  local delay=math.max(number(p.row.cooldownMs) or 0,number(p.row.categoryCooldownMs) or 0,number(p.row.globalCooldownMs) or 0)
  -- Do not overwrite an existing Rend on every global cooldown when its
  -- observation arrives late; this is policy refresh timing, not effect proof.
  if p.family==772 then delay=math.max(delay,15000) end
  state.cooldowns[tostring(p.family)]=p.now+delay
  state.gcdUntil=p.now+(number(p.row.globalCooldownMs) or 0)
 end
 proposed=nil
end
return M
