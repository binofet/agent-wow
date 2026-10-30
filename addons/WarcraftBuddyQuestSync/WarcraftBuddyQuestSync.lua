-- Passive WoW3.3.5a quest telemetry. No quest acceptance/reward/UI clicks.
-- The normal client sends PARTY addon chat; the server supplies sender GUID3.
local PREFIX = "WBQUEST1"
local frame = CreateFrame("Frame")
local nonce = string.format("%08x%04x", time() % 4294967296, math.random(0, 65535))
local revision, due, lastSent, suppressUntil = 0, 0, -10, 0
local snapshotting, dialog, pendingAccept = false, nil, nil
local pendingRewards, previous, completed, db = {}, {}, {}, nil

local function validID(value)
    return type(value) == "number" and value >= 1 and value <= 2147483647 and value == math.floor(value)
end

local function npcGUID(value)
    if type(value) ~= "string" then return nil end
    value = string.lower(value):gsub("^0x", "")
    if #value ~= 16 or not value:match("^f130%x%x%x%x%x%x%x%x%x%x%x%x$") then return nil end
    if tonumber(value:sub(11, 16), 16) == 0 then return nil end
    return value
end

local function isJoel()
    local guid = UnitGUID("player")
    return UnitName("player") == "Joel" and type(guid) == "string"
        and guid:lower():gsub("^0x", "") == "0000000000000003"
end

local function queue()
    local at = GetTime() + 0.5
    if due == 0 or at < due then due = at end
end

local function readLog()
    -- Collapsed zone headers conceal entries from the legacy quest API.
    -- Restore the exact header states and selection in this same Lua turn.
    snapshotting = true
    local collapsed, ordinal, selected = {}, 0, GetQuestLogSelection()
    local selectedID = selected and select(9, GetQuestLogTitle(selected))
    local selectedHeader
    for i = 1, GetNumQuestLogEntries() do
        local title, _, _, _, header, hidden = GetQuestLogTitle(i)
        if header then
            ordinal = ordinal + 1
            collapsed[ordinal] = hidden and true or false
            if selected == i then selectedHeader = ordinal end
        end
    end
    local expanded = false
    for _, hidden in pairs(collapsed) do if hidden then expanded = true end end
    if expanded then ExpandQuestHeader(0) end
    local result, names, restore, duplicate = {}, {}, {}, false
    ordinal = 0
    for i = 1, GetNumQuestLogEntries() do
        local title, _, _, _, header, _, complete, _, id = GetQuestLogTitle(i)
        if header then
            ordinal = ordinal + 1
            if collapsed[ordinal] then restore[#restore + 1] = i end
        elseif validID(id) then
            if result[id] ~= nil then duplicate = true end
            result[id] = complete == 1 and 1 or complete == -1 and 2 or 0
            names[id] = title
        end
    end
    for i = #restore, 1, -1 do CollapseQuestHeader(restore[i]) end
    if expanded then
        ordinal = 0
        for i = 1, GetNumQuestLogEntries() do
            local _, _, _, _, header, _, _, _, id = GetQuestLogTitle(i)
            if header then ordinal = ordinal + 1 end
            if validID(selectedID) and id == selectedID or selectedHeader and header and ordinal == selectedHeader then
                SelectQuestLogEntry(i)
                break
            end
        end
    end
    suppressUntil, snapshotting = expanded and GetTime() + 0.2 or 0, false
    local count = 0
    for _ in pairs(result) do count = count + 1 end
    if duplicate or count > 25 then error("invalid quest log") end
    return result, names, count
end

local function cleanEvents(events)
    local result, seen, now = {}, {}, time()
    if type(events) ~= "table" then return result end
    for _, event in ipairs(events) do
        if type(event) == "table" and validID(event.id) and npcGUID(event.npc)
            and type(event.at) == "number" and now >= event.at and now - event.at <= 900 and not seen[event.id] then
            result[#result + 1] = {id = event.id, npc = npcGUID(event.npc), at = event.at}
            seen[event.id] = true
            if #result == 8 then break end
        end
    end
    return result
end

local function retain(kind, id, npc)
    if not db then return end
    local events = {{id = id, npc = npc, at = time()}}
    for _, event in ipairs(db[kind] or {}) do events[#events + 1] = event end
    db[kind] = cleanEvents(events)
end

local function captureDialog()
    local npc = npcGUID(UnitGUID("questnpc"))
    local title = GetTitleText and GetTitleText()
    dialog = npc and {npc = npc, title = title} or nil
    if not dialog then return end
    local ok, quests, names = pcall(readLog)
    if not ok then snapshotting = false; return end
    for id, name in pairs(names) do
        if name == title then
            if dialog.id then dialog.id = nil; return end
            dialog.id = id
        end
    end
end

local function publish()
    lastSent = GetTime()
    if not db or not isJoel() or GetNumPartyMembers() < 1 then return end
    local ok, quests, _, count = pcall(readLog)
    if not ok then snapshotting = false; return end
    local now = GetTime()
    for id, pending in pairs(pendingRewards) do
        if now - pending.at > 30 then pendingRewards[id] = nil
        elseif quests[id] == nil then
            -- This is an owner-addon report of a reward request followed by
            -- own-log removal, not a companion's independent server receipt.
            retain("rewarded", id, pending.npc)
            pendingRewards[id] = nil
        end
    end
    if pendingAccept then
        if now - pendingAccept.at <= 15 then
            for id in pairs(quests) do
                if previous[id] == nil then retain("accepted", id, pendingAccept.npc) end
            end
        end
        pendingAccept = nil
    end
    previous = quests
    db.rewarded, db.accepted = cleanEvents(db.rewarded), cleanEvents(db.accepted)
    local level = UnitLevel("player")
    if type(level) ~= "number" or level < 1 or level > 80 then return end
    local tokens, ids = {"L=" .. level}, {}
    for id in pairs(quests) do ids[#ids + 1] = id end
    table.sort(ids)
    for _, id in ipairs(ids) do tokens[#tokens + 1] = "Q=" .. id .. "," .. quests[id] end
    local completedIDs, chosen = {}, {}
    -- A bounded positive subset never proves that an omitted quest is not
    -- completed. Keep our initial shared quests in the subset when present.
    for _, id in ipairs({7, 18, 3903}) do
        if completed[id] then completedIDs[#completedIDs + 1], chosen[id] = id, true end
    end
    local remainder = {}
    for id in pairs(completed) do if not chosen[id] then remainder[#remainder + 1] = id end end
    table.sort(remainder, function(a, b) return a > b end)
    for _, id in ipairs(remainder) do if #completedIDs < 64 then completedIDs[#completedIDs + 1] = id end end
    for _, id in ipairs(completedIDs) do tokens[#tokens + 1] = "C=" .. id end
    for _, kind in ipairs({"rewarded", "accepted"}) do
        for _, event in ipairs(db[kind]) do
            tokens[#tokens + 1] = (kind == "rewarded" and "R=" or "A=") .. event.id .. "," .. event.npc .. "," .. math.floor(time() - event.at)
        end
    end
    local parts, current = {}, ""
    for _, token in ipairs(tokens) do
        if #current + #token + 1 > 140 then parts[#parts + 1], current = current, "" end
        current = current == "" and token or current .. ";" .. token
    end
    if current ~= "" then parts[#parts + 1] = current end
    if #parts > 12 then return end
    revision = revision % 2147483647 + 1
    for index, body in ipairs(parts) do
        local payload = "1|" .. nonce .. "|" .. revision .. "|" .. index .. "|" .. #parts .. "|" .. count .. "|" .. body
        if #payload > 200 then return end
        SendAddonMessage(PREFIX, payload, "PARTY")
    end
    lastSent, due = GetTime(), 0
end

frame:SetScript("OnEvent", function(_, event)
    if event == "PLAYER_LOGIN" then
        WarcraftBuddyQuestSyncDB = type(WarcraftBuddyQuestSyncDB) == "table" and WarcraftBuddyQuestSyncDB or {}
        db = WarcraftBuddyQuestSyncDB
        db.rewarded, db.accepted = cleanEvents(db.rewarded), cleanEvents(db.accepted)
        local ok, log = pcall(readLog)
        if ok then previous = log else snapshotting = false end
        if type(QueryQuestsCompleted) == "function" then QueryQuestsCompleted() end
    elseif event == "QUEST_QUERY_COMPLETE" then
        if type(GetQuestsCompleted) == "function" then
            local result = {}
            GetQuestsCompleted(result)
            completed = {}
            for id, rewarded in pairs(result) do
                if validID(id) and rewarded then completed[id] = true end
            end
        end
    elseif event == "QUEST_DETAIL" or event == "QUEST_COMPLETE" then
        captureDialog()
    elseif event == "QUEST_FINISHED" then
        dialog = nil
    end
    if not snapshotting and GetTime() >= suppressUntil then queue() end
end)
for _, event in ipairs({"PLAYER_LOGIN", "QUEST_LOG_UPDATE", "QUEST_ACCEPTED", "QUEST_QUERY_COMPLETE", "QUEST_DETAIL", "QUEST_COMPLETE", "QUEST_FINISHED", "PARTY_MEMBERS_CHANGED", "PLAYER_LEVEL_UP"}) do
    frame:RegisterEvent(event)
end
hooksecurefunc("GetQuestReward", function()
    if dialog and validID(dialog.id) then pendingRewards[dialog.id] = {npc = dialog.npc, at = GetTime()} end
    queue()
end)
hooksecurefunc("AcceptQuest", function()
    if dialog then pendingAccept = {npc = dialog.npc, at = GetTime()} end
    queue()
end)
hooksecurefunc("AbandonQuest", function()
    pendingRewards = {} -- Removal after abandonment never becomes a reward report.
    queue()
end)
frame:SetScript("OnUpdate", function()
    if GetTime() - lastSent >= 5 or due > 0 and GetTime() >= due and GetTime() - lastSent >= 1 then publish() end
end)
