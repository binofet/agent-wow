-- One pure policy for the existing observer's repair eligibility and ordering.
-- The host owns body/source checks and durable no-replay reservations.
local M={tuning={spatialGraceSeconds=10,freshSeconds=15,windowSeconds=120,maxPerWindow=2,
 retentionSeconds=300,maxRetained=64,
 priority={attached_policy=30,gm_task=20,expedition=10,fishing=10},
 limitedKinds={expedition=true,fishing=true}}}
local function number(value)
 local n=tonumber(value);if n and n==n and n-n==0 then return n end
end
local function settled(row)
 return row.outcome=="model_returned" or row.outcome=="not_started"
end
local spatialCodes={[47]=true,[51]=true,[57]=true,[61]=true,[97]=true,[133]=true,[134]=true}
local enemyCapabilities={offense=true,polymorph=true,sap=true,interrupt=true}
local function attached_eligible(fault,t)
 if fault.kind~="attached_policy" or fault.field~="lastSpellOperation" then return true end
 local evidence=fault.evidence or {};local code=number(evidence.lastCastFailure)
 local observation=fault.repairObservation or {};local target=observation.target or {}
 if (code==12 or spatialCodes[code]) and enemyCapabilities[evidence.capability] and target.known and
  (not target.present or target.aliveKnown and not target.alive) then return false end
 return not spatialCodes[code] or
  (number(observation.unchangedSeconds) or 0)>=(number(t.spatialGraceSeconds) or 0)
end
function M.decide(input)
 local now=number(input.now)
 if not now then return {phase="clock_unavailable",retain={}} end
 local t=M.tuning
 local keep,count,recent={},0,0
 local attempts=input.attempts or {}
 -- Unknown attempts cannot be retired. The host independently enforces this
 -- even for an edited policy; dropping history never removes its no-replay key.
 for _,row in ipairs(attempts)do
  local age=number(row.at) and now-number(row.at)
  if not settled(row) then keep[row.key]=true;count=count+1 end
  if row.outcome~="not_started" and age and age>=0 and
   number(t.windowSeconds) and age<t.windowSeconds then recent=recent+1 end
 end
 for i=#attempts,1,-1 do
  local row=attempts[i];local age=number(row.at) and now-number(row.at)
  if settled(row) and age and age>=0 and
   (not number(t.retentionSeconds) or age<=t.retentionSeconds) and
   (not number(t.maxRetained) or t.maxRetained<=0 or count<t.maxRetained) then
   keep[row.key]=true;count=count+1
  end
 end
 local selected,best,phase
 for _,fault in ipairs(input.faults or {})do
  local eligible=attached_eligible(fault,t)
  if fault.kind=="expedition" or fault.kind=="fishing" then
   local at=number(fault.faultAt or fault.updatedAt)
   if not at or at>now or number(t.freshSeconds) and now-at>t.freshSeconds then eligible=false end
  end
  if eligible and (t.limitedKinds or {})[fault.kind] then
   if number(t.maxPerWindow) and t.maxPerWindow>0 and recent>=t.maxPerWindow then
    eligible=false;phase=phase or "rate_limited"
   elseif number(t.maxRetained) and t.maxRetained>0 and count>=t.maxRetained then
    eligible=false;phase=phase or "capacity_reached"
   end
  end
  local priority=number((t.priority or {})[fault.kind]) or 0
  if eligible and (not selected or priority>best) then selected,best=fault,priority end
 end
 return {key=selected and selected.key,phase=selected and "available" or phase or "no_eligible_fault",retain=keep}
end
return M
