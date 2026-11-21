local source=assert(arg[1], "gear source required")
local file=assert(io.open(source,"r"))
local text=file:read("*a")
file:close()
local begin=text:find("-- BEGIN WB CURRENT SET MACROS 20261121",1,true)
local finish=text:find("-- END WB CURRENT SET MACROS 20261121",begin,true)
local helper=assert(loadstring(text:sub(begin,finish-1),source))
local frames={}
local combat=true
local edits,reloads,captures=0,0,0
local macros={
 [37]={name="WB Tank",icon="TankIcon",body="#showtooltip\n/wbgear tank\n/say retained"},
 [38]={name="WB DPS",icon="DPSIcon",body="/wbgear dps"},
}
function CreateFrame()
 local f={}
 function f:RegisterEvent() end
 function f:UnregisterAllEvents() self.stopped=true end
 function f:SetScript(_,callback) self.callback=callback end
 frames[#frames+1]=f
 return f
end
function UnitGUID() return "0x0000000000000003" end
function InCombatLockdown() return combat end
function UnitAffectingCombat() return combat end
function GetNumMacros() return 0,2 end
function GetMacroInfo(index)
 local m=macros[index]
 if m then return m.name,m.icon,m.body end
end
function EditMacro(index,name,icon,body)
 assert(name==nil and icon==nil,"Name or icon changed")
 assert(macros[index],"Existing index not used")
 macros[index].body=body
 edits=edits+1
end
function ReloadUI() reloads=reloads+1 end
function WarcraftBuddyCapturePrepAudit() captures=captures+1 end
function time() return 456 end
WarcraftBuddyQuestSyncDB={}
helper()
frames[1].callback()
assert(edits==0,"Migration edited macros during combat")
combat=false
frames[1].callback()
assert(edits==2 and reloads==1 and captures==1)
assert(macros[37].body=="#showtooltip\n/wbgear l40tank\n/say retained")
assert(macros[38].body=="/wbgear l40dps")
assert(WarcraftBuddyQuestSyncDB.gearMacroMigration.verified)
assert(WarcraftBuddyQuestSyncDB.gearMacroMigration.after[1].index==37)
helper()
frames[2].callback()
assert(edits==2 and reloads==1,"Saved migration repeated")
print("Macro IDs/icons/names/custom lines, combat deferral, observed bodies and one-time save passed.")
