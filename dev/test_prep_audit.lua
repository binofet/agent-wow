local source=assert(arg[1], "audit source required")
SlashCmdList={}
local frames={}
local now=0
function time() return 123456 end
function GetTime() return now end
function isJoel() return true end
function CreateFrame()
 local f={scripts={}}
 function f:RegisterEvent() end
 function f:UnregisterAllEvents() end
 function f:SetScript(event,callback) self.scripts[event]=callback end
 frames[#frames+1]=f
 return f
end
function UnitGUID() return "0x0000000000000003" end
function UnitName() return "Joel" end
function UnitLevel() return 40 end
function GetMoney() return 123 end
NUM_BAG_SLOTS=0
BOOKTYPE_SPELL="spell"
function GetContainerNumFreeSlots() return 15,0 end
function GetContainerNumSlots() return 16 end
function GetContainerItemLink(bag,slot) if slot==1 then return "|Hitem:868:1900:0:0:0:0:0|hWeapon|h" end end
function GetItemInfo() return "Weapon",nil,4,45,40,"Weapon","Sword",1,"INVTYPE_WEAPON",nil,123 end
function GetContainerItemInfo() return nil,1,false,4,false,false end
function GetContainerItemQuestInfo() return false,nil,false end
function GetInventoryItemLink(_,slot) if slot==16 then return "|Hitem:868:1900:0:0:0:0:0|hWeapon|h" end end
function GetInventoryItemTexture() return nil end
function GetInventoryItemDurability() return 90,100 end
function GetNumSpellTabs() return 1 end
function GetSpellTabInfo() return "General",nil,0,1 end
function GetSpellName() return "Attack","" end
function GetNumSkillLines() return 1 end
function GetSkillLineInfo() return "Swords",false,false,200,0,0,200 end
function GetNumTalentGroups() return 2 end
function GetActiveTalentGroup() return 1 end
function GetUnspentTalentPoints() return 3 end
function GetNumTalentTabs() return 3 end
function GetTalentTabInfo() return "Arms",nil,5 end
function GetNumTalents() return 1 end
function GetTalentInfo(tab,index,inspect,pet,group) return "Talent",nil,1,1,group,5 end
function InCombatLockdown() return false end
function UnitAffectingCombat() return false end
function GetCursorInfo() return nil end
function UnitCastingInfo() return nil end
function UnitChannelInfo() return nil end
local reloads=0
function ReloadUI() reloads=reloads+1 end
WarcraftBuddyQuestSyncDB={gearLoadoutOverrides={["3"]={tank={[16]=7786}}}}
WarcraftBuddyGearLoadouts={tank={[16]=1482},dps={[16]=6641,[17]=0}}
dofile(source)
-- Simulate WoW replacing the pre-load table with SavedVariables after files.
WarcraftBuddyQuestSyncDB={gearLoadoutOverrides={["3"]={tank={[16]=7786}}}}
frames[1].scripts.OnEvent(frames[1],"ADDON_LOADED","WarcraftBuddyQuestSync")
assert(WarcraftBuddyQuestSyncDB.prepAuditLoader.addonLoadedAt)
frames[1].scripts.OnEvent(frames[1],"PLAYER_LOGIN")
assert(WarcraftBuddyQuestSyncDB.prepAudit)
frames[1].scripts.OnUpdate(frames[1])
local audit=WarcraftBuddyQuestSyncDB.prepAudit
assert(reloads==1)
assert(audit.complete and audit.bags[0].items[1].enchantID==1900)
assert(audit.totalFreeSlots==15 and audit.effectivePlans.tank[16]==7786)
assert(audit.talentGroups[2].tabs[1].talents[1].rank==2)
WarcraftBuddyPrepAuditVersion=nil
dofile(source)
frames[2].scripts.OnEvent(frames[2],"PLAYER_LOGIN")
assert(frames[2].scripts.OnUpdate==nil and reloads==1)
print("Audit capture, raw enchant, plans, both talent groups and nonce reload guard passed.")
