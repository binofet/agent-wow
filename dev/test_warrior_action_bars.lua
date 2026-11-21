local source=assert(arg[1], "gear source required")
local file=assert(io.open(source,"r"))
local text=file:read("*a")
file:close()
local begin=text:find("-- BEGIN WB WARRIOR ACTION BARS 20261121",1,true)
local finish=text:find("-- END WB WARRIOR ACTION BARS 20261121",begin,true)
local helper=assert(loadstring(text:sub(begin,finish-1),source))
local book={
{name="Auto Attack",rank="",id=6603,passive=false},
{name="Basic Campfire",rank="",id=818,passive=false},
{name="Blacksmithing",rank="Expert",id=3538,passive=false},
{name="Block",rank="Passive",id=107,passive=true},
{name="Cooking",rank="Journeyman",id=3102,passive=false},
{name="Diplomacy",rank="Racial Passive",id=20599,passive=true},
{name="Dodge",rank="Passive",id=81,passive=true},
{name="Dual Wield",rank="Passive",id=674,passive=true},
{name="Every Man for Himself",rank="Racial",id=59752,passive=false},
{name="Find Minerals",rank="",id=2580,passive=false},
{name="First Aid",rank="Expert",id=7924,passive=false},
{name="Fishing",rank="Apprentice",id=7620,passive=false},
{name="Mace Specialization",rank="Racial Passive",id=20864,passive=true},
{name="Parry",rank="Passive",id=3127,passive=true},
{name="Perception",rank="Racial Passive",id=58985,passive=true},
{name="Shoot",rank="",id=3018,passive=false},
{name="Smelting",rank="",id=2656,passive=false},
{name="Sword Specialization",rank="Racial Passive",id=20597,passive=true},
{name="The Human Spirit",rank="Racial Passive",id=20598,passive=true},
{name="Throw",rank="",id=2764,passive=false},
{name="Toughness",rank="Rank 2",id=53121,passive=true},
{name="Battle Stance",rank="",id=2457,passive=false},
{name="Charge",rank="Rank 2",id=6178,passive=false},
{name="Hamstring",rank="",id=1715,passive=false},
{name="Heroic Strike",rank="Rank 6",id=11565,passive=false},
{name="Mocking Blow",rank="",id=694,passive=false},
{name="Overpower",rank="",id=7384,passive=false},
{name="Rend",rank="Rank 5",id=11572,passive=false},
{name="Retaliation",rank="",id=20230,passive=false},
{name="Thunder Clap",rank="Rank 4",id=8205,passive=false},
{name="Battle Shout",rank="Rank 4",id=11549,passive=false},
{name="Berserker Rage",rank="",id=18499,passive=false},
{name="Berserker Stance",rank="",id=2458,passive=false},
{name="Challenging Shout",rank="",id=1161,passive=false},
{name="Cleave",rank="Rank 3",id=11608,passive=false},
{name="Demoralizing Shout",rank="Rank 3",id=11554,passive=false},
{name="Execute",rank="Rank 3",id=20660,passive=false},
{name="Intercept",rank="",id=20252,passive=false},
{name="Intimidating Shout",rank="",id=5246,passive=false},
{name="Pummel",rank="",id=6552,passive=false},
{name="Slam",rank="Rank 2",id=8820,passive=false},
{name="Victory Rush",rank="",id=34428,passive=false},
{name="Whirlwind",rank="",id=1680,passive=false},
{name="Bloodrage",rank="",id=2687,passive=false},
{name="Defensive Stance",rank="",id=71,passive=false},
{name="Disarm",rank="",id=676,passive=false},
{name="Revenge",rank="Rank 3",id=7379,passive=false},
{name="Shield Bash",rank="",id=72,passive=false},
{name="Shield Block",rank="",id=2565,passive=false},
{name="Shield Slam",rank="Rank 1",id=23922,passive=false},
{name="Shield Wall",rank="",id=871,passive=false},
{name="Stance Mastery",rank="Passive",id=12678,passive=true},
{name="Sunder Armor",rank="",id=7386,passive=false},
{name="Taunt",rank="",id=355,passive=false},
{name="Last Stand",rank="",id=12975},
{name="Concussion Blow",rank="",id=12809},
{name="Vigilance",rank="",id=50720},
{name="Heroic Strike",rank="Rank 1",id=78}
}
local actions={[1]={kind="spell",id=78},[25]={kind="item",id=5510},[61]={kind="macro",id=37},[73]={kind="macro",id=38},[86]={kind="spell",id=78}}
local cursor,watcher,ammoID,now,reloads=nil,nil,3030,0,0
local updates={}
BOOKTYPE_SPELL="spell"
NUM_ACTIONBAR_PAGES=6
NUM_BAG_SLOTS=0
SlashCmdList={}
WarcraftBuddyQuestSyncDB={}
function CreateFrame()
 local f={}
 function f:SetScript(event,fn) self[event]=fn end
 function f:RegisterEvent() end
 function f:UnregisterAllEvents() end
 watcher=f
 return f
end
function UnitGUID() return "0x0000000000000003" end
function UnitClass() return "Warrior","WARRIOR" end
function InCombatLockdown() return false end
function UnitAffectingCombat() return false end
function GetTime() return now end
function time() return 123 end
function GetShapeshiftForm() return 2 end
function GetNumShapeshiftForms() return 3 end
function GetShapeshiftFormInfo(i) return nil,({"Battle Stance","Defensive Stance","Berserker Stance"})[i] end
function GetBonusBarOffset() return 2 end
function GetActionInfo(slot) local a=actions[slot];if a then return a.kind,a.id,a.subtype end end
function GetMacroInfo(id) return id==37 and "WB DPS" or "WB Tank","icon","/wbgear l40tank" end
function GetNumSpellTabs() return 1 end
function GetSpellTabInfo() return "All",nil,0,#book end
function GetSpellName(i) return book[i].name,book[i].rank end
function GetSpellLink(i) return "spell:"..book[i].id end
function IsPassiveSpell(i) return book[i].passive end
function GetNumCompanions() return 1 end
function GetCompanionInfo(_,i) if i==1 then return 12345,"Swift Brown Steed",23229 end end
function PickupCompanion(_,i) cursor={kind="companion",id=i,subtype="MOUNT"} end
function PickupSpell(i) cursor={kind="spell",id=book[i].id} end
function GetCursorInfo() if cursor then return cursor.kind,cursor.id end end
function PlaceAction(slot) actions[slot],cursor=cursor,actions[slot] end
function ClearCursor() cursor=nil end
function PickupAction(slot) cursor,actions[slot]=actions[slot],nil end
function GetInventoryItemID(_,slot) if slot==0 then return ammoID end end
function GetInventoryItemLink(_,slot) if slot==0 then return "item:"..ammoID..":0:0:0:0:0:0:0:40" end end
function GetInventoryItemCount() return 1000 end
function GetContainerNumSlots() return 1 end
function GetContainerItemLink() return "item:11285:0:0:0:0:0:0:0:40" end
function EquipItemByName(link,slot) assert(slot==0 and link:match("item:11285:"));ammoID=11285 end
function SetActionBarToggles(...) updates={...} end
function GetActionBarToggles() return unpack(updates) end
function SetCVar() end
function ReloadUI() reloads=reloads+1 end
function WarcraftBuddyReadProtection31() return {spent=31,unspent=0,protection31=true} end
function WarcraftBuddyCapturePrepAudit()
 local r={equipment={}}
 WarcraftBuddyQuestSyncDB.prepAudit=r
 return r
end
helper()
SlashCmdList.WBWARRIORBARS()
now=1
watcher.OnUpdate()
local r=WarcraftBuddyQuestSyncDB.actionBarLayout
assert(r.verified and #r.missingActive==0 and #r.mismatches==0)
assert(r.stancePages[1].page==7 and r.stancePages[2].page==8 and r.stancePages[3].page==9)
assert(actions[1].id==78,"Base page1 changed")
assert(actions[25].id==5510 and actions[61].id==37 and actions[73].id==38,"Useful item/macro moved")
for slot in pairs(r.managedSlots) do
 assert(not(actions[slot] and actions[slot].id==78),"Old rank remains on managed bars")
 for _,b in ipairs(book) do if b.passive then assert(not(actions[slot] and actions[slot].id==b.id),"Passive placed") end end
end
assert(r.ammo.id==11285 and reloads==1 and r.talents.protection31)
assert(WarcraftBuddyQuestSyncDB.prepAudit.ammo.id==11285)
local mount=false
for _,e in pairs(r.expected) do if e.id==23229 then mount=true end end
assert(mount,"Mount was not placed")
print("Action-bar normal entry passed pages7/8/9, highest ranks, passive exclusion, protected items/macros, new talents, mount, ammo and audited save.")
