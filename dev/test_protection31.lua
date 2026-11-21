local source=assert(arg[1], "gear source required")
local file=assert(io.open(source,"r"))
local text=file:read("*a")
file:close()
local begin=text:find("-- BEGIN WB PROTECTION 31 20261121",1,true)
local finish=text:find("-- END WB PROTECTION 31 20261121",begin,true)
local helper=assert(loadstring(text:sub(begin,finish-1),source))
local tabs={
    ["1"]={
        ["name"]="Arms",
        ["talents"]={
            ["1"]={
                ["name"]="Deflection",
                ["tier"]=1,
                ["column"]=2,
                ["rank"]=5,
                ["maxRank"]=5,
            },
        },
    },
    ["2"]={
        ["name"]="Fury",
        ["talents"]={
            ["1"]={
                ["name"]="Cruelty",
                ["tier"]=1,
                ["column"]=3,
                ["rank"]=5,
                ["maxRank"]=5,
            },
            ["2"]={
                ["name"]="Piercing Howl",
                ["tier"]=3,
                ["column"]=2,
                ["rank"]=1,
                ["maxRank"]=1,
            },
            ["3"]={
                ["name"]="Unbridled Wrath",
                ["tier"]=2,
                ["column"]=3,
                ["rank"]=5,
                ["maxRank"]=5,
            },
        },
    },
    ["3"]={
        ["name"]="Protection",
        ["talents"]={
            ["1"]={
                ["name"]="Anticipation",
                ["tier"]=2,
                ["column"]=3,
                ["rank"]=0,
                ["maxRank"]=5,
            },
            ["2"]={
                ["name"]="Concussion Blow",
                ["tier"]=5,
                ["column"]=2,
                ["rank"]=0,
                ["maxRank"]=1,
            },
            ["3"]={
                ["name"]="Gag Order",
                ["tier"]=5,
                ["column"]=3,
                ["rank"]=0,
                ["maxRank"]=2,
            },
            ["4"]={
                ["name"]="Improved Revenge",
                ["tier"]=3,
                ["column"]=2,
                ["rank"]=0,
                ["maxRank"]=2,
            },
            ["5"]={
                ["name"]="Improved Thunder Clap",
                ["tier"]=1,
                ["column"]=3,
                ["rank"]=1,
                ["maxRank"]=3,
            },
            ["6"]={
                ["name"]="Incite",
                ["tier"]=2,
                ["column"]=2,
                ["rank"]=2,
                ["maxRank"]=3,
            },
            ["7"]={
                ["name"]="Last Stand",
                ["tier"]=3,
                ["column"]=1,
                ["rank"]=0,
                ["maxRank"]=1,
            },
            ["8"]={
                ["name"]="One-Handed Weapon Specialization",
                ["tier"]=6,
                ["column"]=3,
                ["rank"]=0,
                ["maxRank"]=5,
            },
            ["9"]={
                ["name"]="Shield Mastery",
                ["tier"]=3,
                ["column"]=3,
                ["rank"]=0,
                ["maxRank"]=2,
            },
            ["10"]={
                ["name"]="Shield Specialization",
                ["tier"]=1,
                ["column"]=2,
                ["rank"]=4,
                ["maxRank"]=5,
            },
            ["11"]={
                ["name"]="Toughness",
                ["tier"]=3,
                ["column"]=4,
                ["rank"]=0,
                ["maxRank"]=5,
            },
            ["12"]={
                ["name"]="Vigilance",
                ["tier"]=7,
                ["column"]=2,
                ["rank"]=0,
                ["maxRank"]=1,
            },
        },
    },
}
local frames={}
local previews,actual={},{}
local free,learns,resets,reloads,captures=8,0,0,0,0
local failPreview=false
local function talent(tab,index) return tabs[tostring(tab)].talents[tostring(index)] end
local function rank(table,tab,index) return table[tab..":"..index] or 0 end
function CreateFrame()
 local f={}
 function f:RegisterEvent() end
 function f:UnregisterAllEvents() end
 function f:SetScript(_,callback) self.callback=callback end
 frames[#frames+1]=f
 return f
end
function UnitGUID() return "0x0000000000000003" end
function UnitClass() return "Warrior","WARRIOR" end
function InCombatLockdown() return false end
function UnitAffectingCombat() return false end
function GetActiveTalentGroup() return 1 end
function GetNumTalentTabs() return 3 end
function GetNumTalents(tab)
 local n=0
 for _ in pairs(tabs[tostring(tab)].talents) do n=n+1 end
 return n
end
function GetTalentTabInfo(tab,inspect,pet,group)
 assert(inspect==false and pet==false and group==1)
 local points=0
 for index=1,GetNumTalents(tab) do points=points+rank(actual,tab,index) end
 return tabs[tostring(tab)].name,nil,points
end
function GetTalentInfo(tab,index,inspect,pet,group)
 assert(inspect==false and pet==false and group==1)
 local t=talent(tab,index)
 return t.name,nil,t.tier,t.column,rank(actual,tab,index),t.maxRank,nil,true,rank(previews,tab,index),true
end
function GetTalentPrereqs(tab,index,inspect,pet,group)
 assert(inspect==false and pet==false and group==1)
 if talent(tab,index).name=="Vigilance" then
  for i=1,GetNumTalents(tab) do
   local t=talent(tab,i)
   if t.name=="Concussion Blow" then return t.tier,t.column,false,rank(previews,tab,i)==1 end
  end
 end
end
function GetUnspentTalentPoints() return free end
function GetGroupPreviewTalentPointsSpent(pet,group)
 assert(pet==false and group==1)
 local n=0 for _,v in pairs(previews) do n=n+v end return n
end
function AddPreviewTalentPoints(tab,index,points,pet,group)
 assert(points==1 and pet==false and group==1)
 if not failPreview then previews[tab..":"..index]=rank(previews,tab,index)+points end
end
function ResetGroupPreviewTalentPoints(pet,group)
 assert(pet==false and group==1)
 previews={}
 resets=resets+1
end
function LearnPreviewTalents(pet) assert(pet==false);learns=learns+1 end
function ReloadUI() reloads=reloads+1 end
function WarcraftBuddyCapturePrepAudit() captures=captures+1 end
function time() return 999 end
SlashCmdList={}
WarcraftBuddyQuestSyncDB={}
for tab,data in pairs(tabs) do
 for index,t in pairs(data.talents) do actual[tab..":"..index]=t.rank end
end
helper()
SlashCmdList.WBPROTECTION31()
assert(learns==0 and next(previews)==nil,"Applied before trainer reset")
actual={}
free=31
failPreview=true
SlashCmdList.WBPROTECTION31()
assert(learns==0 and resets==1 and next(previews)==nil,"Failed preview was not stopped")
failPreview=false
SlashCmdList.WBPROTECTION31()
assert(learns==1 and reloads==0 and WarcraftBuddyQuestSyncDB.prepTalentApplication.status=="submitted")
assert(GetGroupPreviewTalentPointsSpent(false,1)==31)
for key,value in pairs(previews) do actual[key]=value end
free=0
frames[1].callback()
assert(WarcraftBuddyQuestSyncDB.prepTalentApplication.status=="verified")
assert(reloads==1 and captures==1)
SlashCmdList.WBPROTECTION31()
assert(learns==1,"Repeated commit after success")
print("Observed live talent tree passed trainer-reset guard, failed-preview rollback, legal31point preview, one submission and server-observed verification.")
