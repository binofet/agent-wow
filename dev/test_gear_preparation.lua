local source=assert(arg[1], "gear source required")
local configSource=assert(arg[2], "gear config required")
SlashCmdList={}
UIParent={}
NUM_BAG_SLOTS=0
local frames,equipment,bags={},{},{}
local now,cursor,origin,reloads,captures=0,nil,nil,0,0
local applied={}
local prototype={}
function prototype:SetScript(key,fn) self.scripts[key]=fn end
function prototype:SetAttribute(key,value) self.attributes[key]=value end
function prototype:GetAttribute(key) return self.attributes[key] end
function prototype:SetText(text) self.text=text end
function prototype:Show() self.visible=true end
function prototype:Hide() self.visible=false end
function prototype:IsShown() return self.visible end
local function frame()
 local f={scripts={},attributes={},visible=true}
 setmetatable(f,{__index=function(_,key) return prototype[key] or function() end end})
 frames[#frames+1]=f
 return f
end
function prototype:CreateFontString() return frame() end
function CreateFrame() return frame() end
DEFAULT_CHAT_FRAME={AddMessage=function() end}
function GetTime() return now end
function time() return 123 end
function UnitGUID() return "0x0000000000000003" end
function InCombatLockdown() return false end
function UnitAffectingCombat() return false end
function UnitIsDeadOrGhost() return false end
function UnitCastingInfo() end
function UnitChannelInfo() end
local function link(piece) return piece and ("item:"..piece.id..":"..piece.enchant..":0:0:0:0:0:0:40") end
function GetInventoryItemID(_,slot) return equipment[slot] and equipment[slot].id end
function GetInventoryItemLink(_,slot) return link(equipment[slot]) end
function GetContainerItemLink(_,slot) return link(bags[slot]) end
function GetContainerNumSlots() return 100 end
function GetContainerNumFreeSlots()
 local free=0
 for slot=1,100 do if not bags[slot] then free=free+1 end end
 return free,0
end
function GetContainerItemInfo(_,slot)
 local piece=bags[slot]
 return piece and 1,piece and piece.count or 0,false
end
function GetItemInfo() return "Test",nil,3,40,40,"Armor","Plate",1,"INVTYPE_CHEST" end
function IsInventoryItemLocked() return false end
function CursorHasItem() return cursor~=nil end
function CursorCanGoInSlot() return true end
function GetCursorInfo() if cursor then return "item",cursor.id,link(cursor) end end
function PickupContainerItem(_,slot)
 if not cursor then origin=slot end
 cursor,bags[slot]=bags[slot],cursor
end
function PickupInventoryItem(slot) cursor,equipment[slot]=equipment[slot],cursor end
function ClearCursor()
 if cursor then
  local target=origin
  if not target or bags[target] then for slot=1,100 do if not bags[slot] then target=slot;break end end end
  assert(target and not bags[target])
  bags[target]=cursor
 end
 cursor,origin=nil,nil
end
function GetItemCount(id)
 local count=0
 for _,piece in pairs(bags) do if piece.id==id then count=count+(piece.count or 1) end end
 return count
end
function ReloadUI() reloads=reloads+1 end
function WarcraftBuddyCapturePrepAudit() captures=captures+1 end
WarcraftBuddyQuestSyncDB={}
WarcraftBuddyGearLoadouts={characterGUID="0x3",tank={[5]=10775},dps={[5]=10775}}
dofile(configSource)
dofile(source)
local config=WarcraftBuddyLevel40Loadouts
local function add(id,enchant,count)
 for slot=1,100 do
  if not bags[slot] then bags[slot]={id=id,enchant=enchant or 0,count=count or 1};return end
 end
 error("bags full")
end
for slot,id in pairs(config.tank) do equipment[slot]={id=id,enchant=0} end
equipment[13],equipment[14]={id=19024,enchant=0},{id=21568,enchant=0}
equipment[16].enchant=2673
equipment[17].enchant=1071
-- Regression: equipped plain tank chest must select carried correct variant,
-- without consuming a chest scroll, leaving another plain copy for DPS.
add(10775,2659)
add(10775,0)
add(19581,0)
for slot,id in pairs(config.dps) do
 if id~=0 and config.tank[slot]~=id then add(id,0) end
end
local effects={[10546]=663,[38919]=2667,[38902]=2649,[38897]=2647,[38933]=684,[38909]=2649,[37603]=2657,[38940]=368,[38913]=2661,[15564]=1843}
for id in pairs(effects) do add(id,0,(id==38933 or id==15564 or id==10546) and 2 or 1) end
SlashCmdList.WBGEAR("prep40")
local button
for _,f in ipairs(frames) do if f.scripts.PreClick then button=f end end
assert(button,"Combined secure button not created")
for click=1,80 do
 button.scripts.PreClick(button)
 local macro=button.attributes.macrotext or ""
 local scroll=tonumber(macro:match("item:(%d+)"))
 local slot=tonumber(macro:match("\n/use.- (%d+)$"))
 if scroll then
  assert(slot and equipment[slot],"No target")
  assert(equipment[slot].enchant==0,"Overwrote an existing enchant")
  assert(effects[scroll],"Unexpected scroll")
  local consumed=false
  for bagSlot,piece in pairs(bags) do
   if piece.id==scroll then
    piece.count=piece.count-1
    if piece.count==0 then bags[bagSlot]=nil end
    consumed=true;break
   end
  end
  assert(consumed,"Used missing scroll")
  equipment[slot].enchant=effects[scroll]
  applied[scroll]=(applied[scroll] or 0)+1
 end
 for tick=1,100 do
  now=now+0.25
  for _,f in ipairs(frames) do if f.scripts.OnUpdate then f.scripts.OnUpdate(f) end end
 end
 if reloads>0 then break end
end
assert(reloads==1 and captures==1,"Combined preparation did not reach final audited save")
assert(applied[15564]==2 and applied[38933]==2 and applied[10546]==2,"Both kit/glove copies were not completed")
assert(not applied[38911] and not applied[38925] and not applied[38945],"Correct existing enchants were consumed")
assert(equipment[13].id==19024 and equipment[14].id==21568,"Trinkets changed")
assert(WarcraftBuddyQuestSyncDB.prepCompletion.tank and WarcraftBuddyQuestSyncDB.prepCompletion.dps)
print("Normal /wbgear prep40 entry passed Tank→DPS, exact-copy preference, both leg kits, retained correct enchants/trinkets and final audited save.")

SlashCmdList.WBGEAR("l40tank")
for tick=1,100 do
 now=now+0.25
 for _,f in ipairs(frames) do if f.scripts.OnUpdate then f.scripts.OnUpdate(f) end end
end
for slot,id in pairs(config.tank) do
 assert(equipment[slot] and equipment[slot].id==id,"L40 Tank alias wrong item")
 local enchant=config.enchants.tank[slot]
 if enchant then assert(equipment[slot].enchant==enchant,"L40 Tank alias wrong variant") end
end
SlashCmdList.WBGEAR("l40dps")
for tick=1,100 do
 now=now+0.25
 for _,f in ipairs(frames) do if f.scripts.OnUpdate then f.scripts.OnUpdate(f) end end
end
for slot,id in pairs(config.dps) do
 assert((equipment[slot] and equipment[slot].id or 0)==id,"L40 DPS alias wrong item")
 local enchant=config.enchants.dps[slot]
 if enchant then assert(equipment[slot].enchant==enchant,"L40 DPS alias wrong variant") end
end
print("Both current macro target commands equip the exact L40 variants in the fixture.")
