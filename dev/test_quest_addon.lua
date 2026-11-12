-- Run with Lua5.1: lua dev/test_quest_addon.lua addons/.../WarcraftBuddyQuestSync.lua
local source = assert(arg[1], "addon source path required")
local clock, rows, sent, hooks, callbacks, collapsed, selected, title, owner, database
local queryCount, completed, questNPC
local printed

local function visible()
    local result, hidden = {}, false
    for _, row in ipairs(rows) do
        if row.header then hidden = row.collapsed; result[#result + 1] = row
        elseif not hidden then result[#result + 1] = row end
    end
    return result
end
local function reset(keep)
    clock, rows, sent, hooks, callbacks, selected, title = keep and 10 or 1, {}, {}, {}, {}, 1, "Quest"
    owner, queryCount, completed, questNPC = "Joel", 0, {}, "0xF1300000C400022C"
    SlashCmdList, printed = {}, {}
    DEFAULT_CHAT_FRAME = {AddMessage = function(_, message) printed[#printed + 1] = message end}
    WarcraftBuddyQuestSyncDB = keep and database or nil
    function time() return 1700000000 + math.floor(clock) end
    function GetTime() return clock end
    function CreateFrame() return {SetScript=function(_,k,v) callbacks[k]=v end, RegisterEvent=function() end} end
    function GetQuestLogSelection() return selected end
    function SelectQuestLogEntry(i) selected=i end
    function GetNumQuestLogEntries() return #visible() end
    function GetQuestLogTitle(i)
        local row=visible()[i]
        if row then return row.title or "Quest",1,nil,0,row.header,row.collapsed,row.complete,nil,row.id end
    end
    function ExpandQuestHeader() for _,row in ipairs(rows) do if row.header then row.collapsed=false end end end
    function CollapseQuestHeader(i) visible()[i].collapsed=true end
    function UnitName() return owner end
    function UnitGUID(unit) return unit == "player" and (owner == "Joel" and "0x0000000000000003" or "0x0000000000000004") or questNPC end
    function UnitLevel() return 3 end
    function GetNumPartyMembers() return 3 end
    function GetTitleText() return title end
    function hooksecurefunc(name, callback) hooks[name]=callback end
    function SendAddonMessage(prefix,payload,channel)
        assert(prefix == "WBQUEST1" and channel == "PARTY" and #payload<=200)
        sent[#sent+1]=payload
    end
    function QueryQuestsCompleted() queryCount=queryCount+1 end
    function GetQuestsCompleted(result) for id in pairs(completed) do result[id]=true end end
    dofile(source)
end
local function event(name) callbacks.OnEvent(nil,name) end
local function update(seconds) clock=clock+(seconds or 1); callbacks.OnUpdate() end
local function body()
    local out={}
    for _,packet in ipairs(sent) do out[#out+1]=packet:match("^[^|]+|[^|]+|[^|]+|[^|]+|[^|]+|[^|]+|(.+)$") end
    return table.concat(out,";")
end

reset()
rows={{header=true,title="Elwynn",collapsed=true},{id=7,title="Camp",complete=1},{id=18,title="Thieves"}}
event("PLAYER_LOGIN"); update()
assert(queryCount==1 and body():find("Q=7,1",1,true) and body():find("Q=18,0",1,true))
assert(rows[1].collapsed and selected==1, "quest header state and selection restored")
assert(not body():find("R=",1,true), "reward window is not a claim")

reset()
rows={{id=7,title="Quest",complete=1}}
event("PLAYER_LOGIN"); update()
event("QUEST_COMPLETE"); update()
assert(not body():find("R=",1,true))
hooks.GetQuestReward(); update()
assert(not body():find("R=",1,true), "request alone is not removal")
rows={}; event("QUEST_LOG_UPDATE"); update()
assert(body():find("R=7,f1300000c400022c,",1,true))
database=WarcraftBuddyQuestSyncDB
reset(true); event("PLAYER_LOGIN"); update()
assert(body():find("R=7,f1300000c400022c,",1,true), "reload replays bounded recent report")

reset()
rows={{id=7,title="Quest",complete=1}}
event("PLAYER_LOGIN"); update(); event("QUEST_COMPLETE"); hooks.GetQuestReward(); hooks.AbandonQuest()
rows={}; event("QUEST_LOG_UPDATE"); update()
assert(not body():find("R=",1,true), "abandonment never becomes reward report")

reset()
event("PLAYER_LOGIN"); update(); event("QUEST_DETAIL"); hooks.AcceptQuest()
rows={{id=3903,title="Quest"}}; event("QUEST_LOG_UPDATE"); update()
assert(body():find("A=3903,f1300000c400022c,",1,true))
completed={[7]=true,[18]=true,[3903]=true}; event("QUEST_QUERY_COMPLETE"); update()
assert(body():find("C=7",1,true) and body():find("C=18",1,true) and body():find("C=3903",1,true))

reset()
for i=1,25 do rows[i]={id=10000+i,title="Quest"..i} end
for i=1,100 do completed[i]=true end
event("PLAYER_LOGIN"); event("QUEST_QUERY_COMPLETE"); update()
assert(#sent>1 and #sent<=12)
local count=0; for _ in body():gmatch("C=%d+") do count=count+1 end
assert(count==64, "completed-query subset bounded64")

reset(); owner="Mira"; rows={{id=7}}; event("PLAYER_LOGIN"); update()
assert(#sent==0, "only Joel broadcasts")

reset(); rows={{id=7}}; event("PLAYER_LOGIN"); update()
for i=1,60 do update(5) end
local healthy=WarcraftBuddyQuestSyncStatus()
assert(healthy.onUpdateTicks==61 and healthy.completedSnapshots==61 and healthy.readErrors==0,
    "five-minute idle heartbeat keeps publishing without quest events")
local sentBeforePause=#sent
clock=clock+300 -- No OnUpdate calls: emulate a suspended/background client UI.
local paused=WarcraftBuddyQuestSyncStatus()
assert(paused.updateAge==300 and paused.snapshotWallAge==300 and paused.onUpdateTicks==61,
    "diagnostic distinguishes frame suspension from packet receipt")
SlashCmdList.WARCRAFTBUDDYQUESTSYNC("")
assert(#printed==1 and #sent==sentBeforePause and printed[1]:find("ticks=61",1,true),
    "status stays local and never sends ordinary or addon chat")
update(1)
assert(#sent==sentBeforePause+1 and WarcraftBuddyQuestSyncStatus().snapshotWallAge==0,
    "foreground update resumes heartbeat without reload")

local realTitle=GetQuestLogTitle
GetQuestLogTitle=function() error("simulated quest log failure") end
update(5)
assert(WarcraftBuddyQuestSyncStatus().readErrors==1 and WarcraftBuddyQuestSyncStatus().phase=="quest_log_read_failed")
GetQuestLogTitle=realTitle;update(5)
assert(WarcraftBuddyQuestSyncStatus().phase=="snapshot_send_calls_completed", "quest read failure recovers on heartbeat")
local realSend=SendAddonMessage
SendAddonMessage=function() error("simulated addon send failure") end
update(5)
assert(WarcraftBuddyQuestSyncStatus().sendErrors==1 and WarcraftBuddyQuestSyncStatus().phase=="addon_send_call_failed")
SendAddonMessage=realSend;update(5)
assert(WarcraftBuddyQuestSyncStatus().phase=="snapshot_send_calls_completed", "send error recovers without reload")
local realLevel=UnitLevel
UnitLevel=function() error("simulated unexpected publish failure") end
update(5)
assert(WarcraftBuddyQuestSyncStatus().publishErrors==1 and WarcraftBuddyQuestSyncStatus().phase=="publish_failed")
UnitLevel=realLevel;update(5)
assert(WarcraftBuddyQuestSyncStatus().phase=="snapshot_send_calls_completed", "unexpected publish error stays local")
local queries=queryCount
SlashCmdList.WARCRAFTBUDDYQUESTSYNC("refresh")
assert(queryCount==queries+1, "explicit refresh uses normal completed-query API")
update(1)
assert(WarcraftBuddyQuestSyncStatus().snapshotAge==0)
print("Addon Lua5.1 behavior checks passed")

-- Equipment telemetry uses a separate authenticated PARTY stream, preserves
-- unknown facts, and never invents item GUIDs or profession skill ranks.
reset()
local gearSent={}
local questSender=SendAddonMessage
function SendAddonMessage(prefix,payload,channel)
    if prefix=="WBGEAR1" then
        assert(channel=="PARTY" and #payload<=200)
        gearSent[#gearSent+1]=payload
    else questSender(prefix,payload,channel) end
end
function UnitClass() return "Warrior","WARRIOR" end
function UnitRace() return "Human","Human" end
local equipment={ [7]="|cff1eff00|Hitem:100:0:0:0:0:0:0:0:10|h[Pants]|h|r" }
function GetInventoryItemLink(_,slot) return equipment[slot] end
function GetInventoryItemTexture(_,slot) return equipment[slot] and "texture" end
function GetItemInfo(link) return "Pants" end
function GetInventoryItemDurability(slot) if slot==7 then return 5,10 end end
function IsSpellKnown(id) return id==201 or id==9078 or id==9116 end
local function gearBody()
    local out={}
    for _,packet in ipairs(gearSent) do out[#out+1]=packet:match("^[^|]+|[^|]+|[^|]+|[^|]+|[^|]+|[^|]+|(.+)$") end
    return table.concat(out,";")
end
event("PLAYER_LOGIN");update()
assert(gearBody():find("G=1,1,3,1,1",1,true))
assert(gearBody():find("E=6,1,100,1,1,0,1,5,10",1,true))
assert(gearBody():find("E=18,1,0,1,1,0,1,0,0",1,true))
assert(gearBody():find("P=43,1",1,true) and gearBody():find("P=44,0",1,true))
local initial=#gearSent;update(0.1);assert(#gearSent==initial,"no gear heartbeat flood")
gearSent={};equipment[7]="|cff1eff00|Hitem:100:5:0:0:0:0:0:0:10|h[Pants]|h|r"
event("PLAYER_EQUIPMENT_CHANGED");update(1)
assert(gearBody():find("E=6,1,100,1,1,1,1,5,10",1,true),"enchant preserved")
gearSent={};GetItemInfo=function() return nil end;update(5)
assert(gearBody():find("G=1,1,3,0,1",1,true) and gearBody():find("E=6,1,100,0,1,1",1,true),"uncached metadata unknown")
gearSent={};IsSpellKnown=function() return nil end;update(5)
assert(gearBody():find("G=1,1,3,0,0",1,true) and not gearBody():find("P=",1,true),"unknown proficiency not false absence")
gearSent={};owner="Mira";update(5);assert(#gearSent==0,"no peer equipment authority")
print("Owner equipment Lua5.1 checks passed")

-- Team UI: normal PARTY request transport, actual party sender identity and
-- live status age. Widgets and host replies are simulated; no body is run.
reset();owner="Joel"
local T=assert(WarcraftBuddyTeam)
local names={party1="Arlen",party2="Mira",party3="Rook",party4="Nixwick"}
local ids={party1="0000000000000001",party2="0000000000000004",party3="0000000000000005",party4="0000000000000008"}
local online={party1=true,party2=true,party3=true,party4=false}
function GetNumPartyMembers() return 4 end
function UnitName(unit) return unit=="player" and owner or names[unit] end
function UnitGUID(unit) return unit=="player" and (owner=="Joel" and "0000000000000003" or "0000000000000004") or ids[unit] end
function UnitIsConnected(unit) return online[unit] end
local teamSent,widgets={},{}
function SendAddonMessage(prefix,payload,channel)
    assert(channel=="PARTY" and #payload<=248)
    if prefix=="WBTEAM1" then teamSent[#teamSent+1]=payload end
end
local methods={}
function methods:SetWidth(v) self.width=v end
function methods:SetHeight(v) self.height=v end
function methods:SetPoint(...) self.point={...} end
function methods:SetFrameStrata(v) assert(v=="DIALOG") end
function methods:EnableMouse() end
function methods:SetMovable() end
function methods:RegisterForDrag() end
function methods:SetScript(k,v) self.scripts[k]=v end
function methods:SetBackdrop(v) assert(v.insets) end
function methods:SetText(v) self.text=tostring(v) end
function methods:SetJustifyH(v) assert(v=="LEFT") end
function methods:Show() self.shown=true end
function methods:Hide() self.shown=false end
function methods:IsShown() return self.shown end
function methods:Enable() self.enabled=true end
function methods:Disable() self.enabled=false end
function methods:StartMoving() self.moving=true end
function methods:StopMovingOrSizing() self.moving=false end
local function widget(kind,name,parent)
    local w=setmetatable({kind=kind,name=name,parent=parent,scripts={},shown=true},{__index=methods})
    widgets[#widgets+1]=w;return w
end
function methods:CreateFontString() return widget("Text",nil,self) end
function CreateFrame(kind,name,parent) return widget(kind,name,parent) end
UIParent={}
T.Tick()
assert(widgets[1].name=="WarcraftBuddyTeamButton" and widgets[1].text=="Team")
widgets[1].scripts.OnClick()
local teamWindow=widgets[2]
assert(teamWindow.name=="WarcraftBuddyTeamFrame" and teamWindow:IsShown())
local resetButtons,unstuckButtons={},{}
for _,w in ipairs(widgets) do
    if w.text=="Reset" then resetButtons[#resetButtons+1]=w end
    if w.text=="Unstuck" then unstuckButtons[#unstuckButtons+1]=w end
end
assert(#resetButtons==4 and #unstuckButtons==4,"four named rows each need both controls")
for _,b in ipairs(resetButtons) do assert(not b.enabled,"no fresh host must not permit a request") end
assert(T.View(8).connection=="Offline" and T.View(8).activity=="No host status")
local epoch="ABCDEF123456"
local seq=0
local function status(subject,phase,request,requestSeq,sender,ttl,bodyEpoch)
    seq=seq+1
    return T.Receive("WBTEAM1",table.concat({"S",bodyEpoch or epoch,seq,subject,phase,request or "-",requestSeq or 0,ttl or 15},"|"),"PARTY",sender or "Rook")
end
assert(T.Receive("WBTEAM1","S|ABCDEF123456|1|5|MANAGING_SKILLS|-|0|15","PARTY","Rook"))
seq=1
assert(T.View(5).activity=="Managing skills" and resetButtons[3].enabled)
assert(status(4,"SELLING_TRASH") and T.View(4).activity=="Selling trash")
assert(status(8,"OFFLINE") and T.View(8).activity=="Offline" and T.View(8).canRequest,
    "an offline companion can be requested through another actual online relay")
resetButtons[3].scripts.OnClick()
local pending=assert(T.pending['5']);local sent=assert(teamSent[1])
assert(sent=="R|ABCDEF123456|"..pending.nonce.."|1|5|RESET","exact relay/owner nonce/sequence/subject/action framing")
assert(T.View(5).request:find("queued",1,true) and not resetButtons[3].enabled)
assert(not T.Request(5,"RESET") and #teamSent==1,"pending click must never duplicate a request")
assert(status(5,"READY",pending.nonce,pending.sequence) and T.pending['5'],"generic readiness is not request completion")
assert(status(5,"COMPLETE","000000000000",pending.sequence) and T.pending['5'],"foreign request nonce cannot settle")
assert(status(5,"COMPLETE",pending.nonce,pending.sequence+1) and T.pending['5'],"foreign sequence cannot settle")
assert(status(5,"DEFERRED_COMBAT",pending.nonce,pending.sequence))
assert(T.View(5).request:find("combat",1,true) and T.pending['5'])
assert(status(5,"STARTING",pending.nonce,pending.sequence))
assert(T.View(5).request:find("Restarting",1,true))
assert(status(5,"COMPLETE",pending.nonce,pending.sequence) and not T.pending['5'])
assert(T.View(5).request:find("completed",1,true) and resetButtons[3].enabled)
unstuckButtons[4].scripts.OnClick()
pending=assert(T.pending['8'])
assert(teamSent[2]:match("|8|UNSTUCK$") and status(8,"UNSTUCK_ROUTING",pending.nonce,pending.sequence))
assert(status(8,"NEEDS_GM_HELP",pending.nonce,pending.sequence) and not T.pending['8'])
assert(T.View(8).request:find("no safe route",1,true),"route failure never implies teleport or successful recovery")
local before=#teamSent
for _,bad in ipairs({"S|ABCDEF123456|99|5|MADE_UP|-|0|15","S|ABCDEF123456|99|5|ONLINE|-|0|16",
    "S|ABCDEF123456|99|99|ONLINE|-|0|15","S|ABCDEF123456|99|5|ONLINE|bad|1|15",
    "S|ABCDEF123456|99|5|ONLINE|-|1|15","S|ABCDEF123456|99|5|ONLINE|-|0|15|extra"}) do
    assert(not T.Receive("WBTEAM1",bad,"PARTY","Rook"))
end
assert(not T.Receive("WBTEAM1","S|ABCDEF123456|99|5|ONLINE|-|0|15","SAY","Rook"))
assert(not T.Receive("WBTEAM1","S|ABCDEF123456|99|5|ONLINE|-|0|15","PARTY","Stranger"))
ids.party3="0000000000000099"
assert(not status(5,"ONLINE"),"names alone do not authenticate a host")
ids.party3="0000000000000005"
assert(status(5,"ONLINE",nil,nil,nil,15,"NEWRELAY1234"))
assert(not status(5,"ONLINE",nil,nil,nil,15,epoch),"retired relay body may not replace its successor")
assert(not T.Receive("WBTEAM1","S|NEWRELAY1234|1|5|ONLINE|-|0|15","PARTY","Rook"),"old status sequence replay")
for i=1,7 do assert(status(5,"ONLINE",nil,nil,nil,15,"RELAYBODY000"..i)) end
assert(not status(5,"ONLINE",nil,nil,nil,15,"RELAYBODY0008"),"epoch tombstone capacity must fail closed")
assert(not status(5,"ONLINE",nil,nil,nil,15,epoch),"capacity must not reopen the first retired body")
clock=clock+16;T.Tick()
assert(T.View(5).activity:find("Stale",1,true) and not T.View(5).canRequest)
assert(not T.Request(1,"RESET") and #teamSent==before,"stale status is not launch authority")
assert(status(5,"ONLINE",nil,nil,nil,15,"RELAYBODY0007"))
function SendAddonMessage() error("ambiguous addon send") end
assert(not T.Request(1,"UNSTUCK") and T.pending['1'].phase=="OUTCOME_UNCONFIRMED")
local held=T.pending['1'];clock=clock+60;T.Tick()
assert(T.pending['1']==held and not T.Request(1,"UNSTUCK"),"timeout never automatically retries an uncertain request")
local teamTick=T.Tick
T.Tick=function() error("simulated panel failure") end
function SendAddonMessage() end
event("PLAYER_LOGIN");local snapshots=WarcraftBuddyQuestSyncStatus().completedSnapshots
update(5)
assert(WarcraftBuddyQuestSyncStatus().completedSnapshots==snapshots+1,
    "a panel failure must not stop the existing quest heartbeat")
assert(WarcraftBuddyQuestSyncStatus().lastError:find("Team panel",1,true),"panel failures remain locally diagnosable")
T.Tick=teamTick
owner="Mira";assert(not T.Request(4,"RESET"),"only exact owner can request")
local count=#widgets;T.Open();assert(not teamWindow:IsShown());T.Open()
assert(teamWindow:IsShown() and #widgets==count,"toggle must reuse the same window")
UIParent=nil -- Keep nested inventory harnesses independent of this fake UI.
print("Team UI, owner-only requests, status freshness and receipt checks passed")
