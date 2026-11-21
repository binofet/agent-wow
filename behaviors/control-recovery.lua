-- One policy for selecting a self fear break. Observations enumerate exact
-- learned/equipped mechanics; native code never selects or prepares movement.
local state={blocked={}}
local tuning={spell_weights={},prefer_learned=true}
local function guid(x) return type(x)=='string' and #x>0 and x~='0' end
local function on_event(e)
 local obs=e.kind=='encounter_observation' and e.value
 if obs then
  if state.session and state.session~=obs.sessionId then state.blocked={} end
  state.session=obs.sessionId
  local active={}
  for _,a in ipairs(((obs.support or {}).controlRecovery or {}).fearAuras or {}) do active[a.revision]=true end
  for rev in pairs(state.blocked) do if not active[rev] then state.blocked[rev]=nil end end
 end
 local r=e.receipt
 local p=r and r.payload or {}
 if p.capability=='fear_break' and guid(p.aura_revision) then
  if r.outcome=='not_dispatched' then state.blocked[p.aura_revision]=nil
  elseif r.outcome=='failed' then state.blocked[p.aura_revision]=nil
  elseif r.outcome=='transport_accepted' or r.outcome=='unknown' or r.outcome=='settled_unverified' or r.outcome=='confirmed' then
   state.blocked[p.aura_revision]=true
  end
 end
 return true
end
local function decide(frame,obs)
 if type(obs)~='table' or (obs.hostOwned~=true and obs.hostRequested~=true) or
  frame.session_id~=obs.sessionId or #frame.pending~=0 then return end
 local self=(obs.players or {})[obs.selfGuid]
 local recovery=(obs.support or {}).controlRecovery
 if not self or self.present~=true or self.aliveKnown~=true or self.alive~=true or
  not recovery or recovery.fleeingKnown~=true or recovery.fleeing~=true then return end
 local attempted={}
 for _,rev in ipairs(recovery.attemptedAuraRevisions or {}) do attempted[rev]=true end
 local aura
 for _,a in ipairs(recovery.fearAuras or {}) do
  if guid(a.revision) and a.metadataKnown==true and a.negative==true and
   (a.durationKnown~=true or (a.remainingMs or 0)>0) and not attempted[a.revision] and not state.blocked[a.revision] and
   (not aura or (a.slot or 0)<(aura.slot or 0)) then aura=a end
 end
 if not aura then return end
 local best
 local function before(a,b)
  if not b then return true end
  local aw,bw=tuning.spell_weights[a.spellId] or 0,tuning.spell_weights[b.spellId] or 0
  if aw~=bw then return aw>bw end
  local ai,bi=guid(a.itemGuid),guid(b.itemGuid)
  if tuning.prefer_learned and ai~=bi then return not ai end
  local ac=math.max(a.cooldownMs or 0,a.categoryCooldownMs or 0)
  local bc=math.max(b.cooldownMs or 0,b.categoryCooldownMs or 0)
  if ac~=bc then return ac<bc end
  if (a.rank or 0)~=(b.rank or 0) then return (a.rank or 0)>(b.rank or 0) end
  if a.spellId~=b.spellId then return a.spellId<b.spellId end
  return (a.itemGuid or '')<(b.itemGuid or '')
 end
 for _,o in ipairs(recovery.options or {}) do
  if o.ready==true and type(o.spellId)=='number' and o.spellId>0 and before(o,best) then best=o end
 end
 if not best then return end
 local action={capability='fear_break',sessionId=obs.sessionId,selfGuid=obs.selfGuid,targetGuid=obs.selfGuid,
  spellId=best.spellId,auraSlot=aura.slot or 0,auraRevision=aura.revision,auraSpellId=aura.spellId,
  visitId=obs.visitId}
 if guid(best.itemGuid) then action.itemGuid=best.itemGuid;action.itemSlot=best.itemSlot end
 return action
end
return {decide=decide,on_event=on_event,tuning=tuning,
 export_state=function()return state end,
 import_state=function(s)if type(s)~='table' or type(s.blocked)~='table' then return false end state=s;return true end}
