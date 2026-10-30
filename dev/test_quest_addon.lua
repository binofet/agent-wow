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
