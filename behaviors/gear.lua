-- Shared, editable gear comparisons for inventory maintenance and loot rolls.
-- The harness supplies owned items, templates and every legal destination.
local M = {}
M.tuning = {allow_enhanced_comparison=false, allow_unknown_enhancements=false,
            allow_special_templates=false, require_usable_durability=true, minimum_score_gain=0}
M.class_profiles = {}
local function number(v)
 local n=tonumber(v)
 return n and n==n and n-n==0 and n or nil
end
local function copy(t)
 local out={};for k,v in pairs(t or {}) do out[k]=v end;return out
end
local function options_for(context,options)
 local out=copy(M.tuning)
 for k,v in pairs(M.class_profiles[(context or {}).classId] or {}) do out[k]=v end
 for k,v in pairs(options or {}) do out[k]=v end
 return out
end
local function stats(item)
 local out={}
 for _,stat in ipairs(item.stats or {}) do
  local kind,value=number(stat.type or 0),number(stat.value or 0)
  if not kind or kind%1~=0 or out[kind]~=nil or not value or value<0 or
   not (kind<=1 or kind>=3 and kind<=7 or kind>=12 and kind<=39 or kind>=43 and kind<=48) then return end
  out[kind]=value
 end
 return out
end
local function resistances(item)
 if type(item.resistances)=="table" and #item.resistances==6 then return item.resistances end
 -- Trade's complete item-query coverage explicitly proves omitted armor
 -- properties zero. A missing coverage fact does not establish zero.
 if item.armorComparisonKnown==true then return {0,0,0,0,0,0} end
end
local function dps(item)
 local delay=number(item.delayMs)
 if not delay or delay<=0 then return end
 local total=0
 for _,row in ipairs(item.damages or {}) do
  local low,high=number(row.minimum or 0),number(row.maximum or 0)
  if not low or not high or low<0 or high<low or (row.school or 0)~=0 then return end
  total=total+(low+high)/2
 end
 return total>0 and total*1000/delay or nil
end
local function item_known(item)
 return type(item)=="table" and item.available==true and number(item.itemId) and item.itemId>0
end
local function enhanced_block(owned,options)
 if not options.allow_unknown_enhancements and owned.enhancementsKnown~=true then return "enhancement_context_pending" end
 if not options.allow_enhanced_comparison and owned.hasEnhancements==true then return "enhanced_item_comparison_requires_policy_choice" end
end
local function same_slot(a,b)
 local function kind(row)
  local slot=row.inventoryType
  if slot==20 then slot=5 end
  if row.itemClass==2 then return "weapon" end
  return tostring(slot)
 end
 return kind(a)==kind(b)
end
-- Returns upgrade, score gain, reason. No ownership or equip effect is implied.
function M.compare(candidate,carried,current,equipped,options)
 options=options or M.tuning;carried,equipped=carried or {},equipped or {}
 if not item_known(candidate) then return false,0,"candidate_template_pending" end
 if options.require_usable_durability~=false and (carried.maxDurability or 0)>0 and
   (carried.durabilityKnown~=true or (carried.durability or 0)==0) then return false,0,"repair_before_automatic_equip" end
 local reason=enhanced_block(carried,options)
 if reason then return false,0,reason end
 if not options.allow_special_templates and ((candidate.flags2 or 0)~=0 or candidate.hasSpell==true or
    (candidate.scalingDistribution or 0)~=0 or (candidate.scalingValue or 0)~=0 or candidate.armorComparisonKnown~=true) then
  return false,0,"special_template_requires_policy_choice"
 end
 local next=stats(candidate)
 if not next then return false,0,"candidate_stats_unknown" end
 if tostring(equipped.itemGuid or "0")=="0" then return true,1,"empty_equipment_slot" end
 if not item_known(current) then return false,0,"displaced_item_template_pending" end
 reason=enhanced_block(equipped,options)
 if reason then return false,0,reason end
 if not options.allow_special_templates and (current.hasSpell==true or current.armorComparisonKnown~=true or
    (current.flags2 or 0)~=0 or (current.scalingDistribution or 0)~=0 or (current.scalingValue or 0)~=0) then return false,0,"special_baseline_requires_policy_choice" end
 if not same_slot(candidate,current) then return false,0,"incomparable_equipment_types" end
 local old=stats(current)
 local nr,pr=resistances(candidate),resistances(current)
 if not old or not nr or not pr then return false,0,"comparison_facts_unknown" end
 local gain=(candidate.armor or 0)-(current.armor or 0)
 local dominates=gain>=0
 for i=1,6 do
  local delta=(nr[i] or 0)-(pr[i] or 0);dominates=dominates and delta>=0;gain=gain+delta
 end
 local losses={}
 for kind,value in pairs(old) do
  local delta=(next[kind] or 0)-value
  if delta<0 then dominates=false;losses[kind]=-delta end
 end
 for kind,value in pairs(next) do gain=gain+value-(old[kind] or 0) end
 for kind,value in pairs(old) do if next[kind]==nil then gain=gain-value end end
 if options.statWeights then
  gain=((candidate.armor or 0)-(current.armor or 0))*(options.armorWeight or 0)
  for kind,value in pairs(next) do gain=gain+(value-(old[kind] or 0))*(options.statWeights[kind] or options.statWeights[tostring(kind)] or 0) end
  for kind,value in pairs(old) do if next[kind]==nil then gain=gain-value*(options.statWeights[kind] or options.statWeights[tostring(kind)] or 0) end end
  dominates=true
 end
 if candidate.itemClass==2 then
  local nd,pd=dps(candidate),dps(current)
  if not nd or not pd then return false,0,"weapon_damage_unknown" end
  local cost=0
  if not dominates and options.classId==4 and candidate.subclass==15 and current.subclass==15 and candidate.delayMs==current.delayMs then
   dominates=(candidate.armor or 0)>=(current.armor or 0)
   for kind,value in pairs(losses) do
    if kind==4 or kind==38 then cost=cost+value/14
    elseif kind==19 or kind==32 then cost=cost+value/4
    else dominates=false end
   end
   for i=1,6 do dominates=dominates and (nr[i] or 0)>=(pr[i] or 0) end
  end
  gain=(nd-pd-cost)*(options.weaponDpsWeight or 1)
 end
 return dominates and gain>(options.minimum_score_gain or 0),gain,dominates and "" or "comparison_not_an_upgrade"
end

function M.preview(frame)
 local options=options_for(frame.context,frame.options)
 options.classId=(frame.context or {}).classId or options.classId
 local upgrade,gain,reason=M.compare(frame.candidate,frame.candidateFacts,frame.current,frame.currentFacts,options)
 return {upgrade=upgrade,scoreGain=gain,reason=reason}
end
function M.evaluate(frame)
 if frame.mode=="preview" then return M.preview(frame) end
 local inventory=frame.inventory or frame
 local context=inventory.gearContext or {}
 local options=options_for(context,frame.options)
 options.classId=context.classId
 local templates=context.templates or {}
 local equipment={}
 for _,row in ipairs(inventory.equipment or {}) do equipment[row.slot or 0]=row end
 local rows={}
 for _,source in ipairs(inventory.items or {}) do
  local row=copy(source)
  row.gearEquipEligible,row.conservativeUpgrade=false,false
  row.gearEquipmentSlot,row.expectedEquippedItemGuid=0,"0"
  row.gearBlockedReason="no_observed_legal_equipment_destination"
  local facts=source.gearFacts or {}
  local entry=source.item and source.item.entry
  local candidate=entry and templates[tostring(entry)]
  local best
  for _,dest in ipairs(facts.destinations or {}) do
   if dest.eligible==true then
    local slot=dest.equipmentSlot or 0
    local worn=equipment[slot]
    if worn and tostring(worn.itemGuid or "0")==tostring(dest.expectedEquippedItemGuid or "0") then
     local current=templates[tostring(worn.entry or 0)]
     local upgrade,gain,reason=M.compare(candidate,facts,current,worn,options)
     local empty=tostring(worn.itemGuid or "0")=="0"
     if not best or upgrade and not best.upgrade or upgrade==best.upgrade and
       (empty and not best.empty or empty==best.empty and (gain>best.gain or gain==best.gain and slot<best.slot)) then
      best={slot=slot,old=dest.expectedEquippedItemGuid or "0",upgrade=upgrade,gain=gain,reason=reason,empty=empty}
     end
    end
   elseif row.gearBlockedReason=="no_observed_legal_equipment_destination" and dest.blockedReason then row.gearBlockedReason=dest.blockedReason end
  end
  if best then
   row.gearEquipEligible=(best.reason=="" or best.reason=="comparison_not_an_upgrade" or best.reason=="empty_equipment_slot")
   row.conservativeUpgrade=best.upgrade
   row.gearEquipmentSlot,row.expectedEquippedItemGuid=best.slot,best.old
   row.gearBlockedReason=best.reason or ""
   -- Empty-slot infinity is only an internal ordering value, never JSON data.
   row.gearScoreGain=best.empty and 0 or best.gain
  end
  rows[#rows+1]=row
 end
 return {items=rows}
end
return M
