-- WoW3.3.5a passive quest telemetry and explicit companion inventory controls.
-- The normal client sends PARTY addon chat; the server supplies sender GUID3.
local PREFIX = "WBQUEST1"
local frame = CreateFrame("Frame")
local nonce = string.format("%08x%04x", time() % 4294967296, math.random(0, 65535))
local revision, due, lastSent, suppressUntil = 0, 0, -10, 0
local gearRevision, gearLastSent, gearDue = 0, -10, true
local snapshotting, dialog, pendingAccept = false, nil, nil
local pendingRewards, previous, completed, db = {}, {}, {}, nil
local diagnostics = {ticks = 0, attempts = 0, snapshots = 0, sendCalls = 0, readErrors = 0,
    sendErrors = 0, publishErrors = 0, phase = "waiting_for_login"}
local lastUpdate, lastSnapshot, lastSnapshotWall, lastError, lastErrorWall

local function reportError(kind, message)
    diagnostics[kind] = diagnostics[kind] + 1
    lastError = tostring(message):gsub("[%c]", " "):sub(1, 160)
    lastErrorWall = time()
end

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

-- Equipment is comparison evidence only: the3.3 UI has no complete item GUID
-- API. A separate bounded stream leaves the existing quest feed unchanged.
local gearClasses = {WARRIOR=1,PALADIN=2,HUNTER=3,ROGUE=4,PRIEST=5,DEATHKNIGHT=6,SHAMAN=7,MAGE=8,WARLOCK=9,DRUID=11}
local gearRaces = {Human=1,Orc=2,Dwarf=3,NightElf=4,Scourge=5,Tauren=6,Gnome=7,Troll=8,BloodElf=10,Draenei=11}
local gearProficiencies = {{43,201},{44,196},{45,264},{46,266},{54,198},{55,202},{136,227},{160,199},{172,197},{173,1180},
    {176,2567},{226,5011},{228,5009},{229,200},{293,750},{413,8737},{414,9077},{415,9078},{433,9116},{473,15590}}
local function gearInteger(value, maximum)
    return type(value)=="number" and value>=0 and value<=maximum and value==math.floor(value)
end
local function ownerGearTokens()
    if type(GetInventoryItemLink)~="function" or type(GetInventoryItemTexture)~="function"
        or type(GetItemInfo)~="function" or type(UnitClass)~="function" or type(UnitRace)~="function" then return end
    local _, class = UnitClass("player")
    local _, race = UnitRace("player")
    local level = UnitLevel("player")
    if not gearClasses[class] or not gearRaces[race] or not gearInteger(level,80) or level<1 then return end
    local tokens, complete = {}, true
    for slot=1,19 do
        local link, texture = GetInventoryItemLink("player",slot), GetInventoryItemTexture("player",slot)
        local known, entry, metadata, enhancementKnown, enhanced, durabilityKnown, durability, maximum = 0,0,0,0,0,0,0,0
        if not link and not texture then known,metadata,enhancementKnown,durabilityKnown=1,1,1,1
        elseif type(link)=="string" then
            local body = link:match("item:([^|]+)")
            local fields = {}
            if body then for value in (body..":"):gmatch("(.-):") do fields[#fields+1]=value end end
            local id = tonumber(fields[1])
            if validID(id) then
                known,entry=1,id
                metadata=GetItemInfo(link) and 1 or 0
                if #fields>=7 then
                    enhancementKnown=1
                    for i=2,7 do
                        local value=fields[i]=="" and 0 or tonumber(fields[i])
                        if type(value)~="number" or value~=math.floor(value) or math.abs(value)>2147483647 then enhancementKnown=0;enhanced=0;break end
                        if value~=0 then enhanced=1 end
                    end
                end
                if type(GetInventoryItemDurability)=="function" then
                    local current,max=GetInventoryItemDurability(slot)
                    if current==nil and max==nil then durabilityKnown=1
                    elseif gearInteger(current,1000000) and gearInteger(max,1000000) and current<=max then
                        durabilityKnown,durability,maximum=1,current,max
                    end
                end
            end
        end
        if known~=1 or metadata~=1 or enhancementKnown~=1 or durabilityKnown~=1 then complete=false end
        tokens[#tokens+1]=table.concat({"E="..(slot-1),known,entry,metadata,enhancementKnown,enhanced,durabilityKnown,durability,maximum},",")
    end
    local proficiencyTokens, proficiencyKnown={},type(IsSpellKnown)=="function"
    if proficiencyKnown then
        for _, pair in ipairs(gearProficiencies) do
            local ok, known=pcall(IsSpellKnown,pair[2])
            if not ok or type(known)~="boolean" then proficiencyKnown=false;break end
            proficiencyTokens[#proficiencyTokens+1]="P="..pair[1]..","..(known and 1 or 0)
        end
    end
    if proficiencyKnown then for _, token in ipairs(proficiencyTokens) do tokens[#tokens+1]=token end end
    table.insert(tokens,1,table.concat({"G="..gearClasses[class],gearRaces[race],level,complete and 1 or 0,proficiencyKnown and 1 or 0},","))
    return tokens
end
local function publishOwnerGear()
    if not db or not isJoel() or GetNumPartyMembers()<1 then return end
    local now=GetTime()
    if now-gearLastSent<5 and not (gearDue and now-gearLastSent>=1) then return end
    gearLastSent=now
    local tokens=ownerGearTokens()
    if not tokens then return end
    local parts,current={},""
    for _, token in ipairs(tokens) do
        if #current+#token+1>140 then parts[#parts+1],current=current,"" end
        current=current=="" and token or current..";"..token
    end
    if current~="" then parts[#parts+1]=current end
    if #parts>12 then error("owner gear exceeds bounded part count") end
    gearRevision=gearRevision%2147483647+1
    for i,part in ipairs(parts) do
        local payload="1|"..nonce.."|"..gearRevision.."|"..i.."|"..#parts.."|19|"..part
        if #payload>200 then error("owner gear exceeds payload bound") end
        SendAddonMessage("WBGEAR1",payload,"PARTY")
    end
    gearDue=false
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
    diagnostics.attempts = diagnostics.attempts + 1
    if not db then diagnostics.phase = "waiting_for_login"; return end
    if not isJoel() then diagnostics.phase = "not_joel"; return end
    if GetNumPartyMembers() < 1 then diagnostics.phase = "not_in_party"; return end
    local ok, quests, _, count = pcall(readLog)
    if not ok then
        snapshotting = false
        diagnostics.phase = "quest_log_read_failed"
        reportError("readErrors", quests)
        return
    end
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
    if type(level) ~= "number" or level < 1 or level > 80 then diagnostics.phase = "unknown_player_level"; return end
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
    if #parts > 12 then diagnostics.phase = "snapshot_exceeds_part_limit"; return end
    revision = revision % 2147483647 + 1
    for index, body in ipairs(parts) do
        local payload = "1|" .. nonce .. "|" .. revision .. "|" .. index .. "|" .. #parts .. "|" .. count .. "|" .. body
        if #payload > 200 then diagnostics.phase = "snapshot_exceeds_payload_limit"; return end
        local sent, message = pcall(SendAddonMessage, PREFIX, payload, "PARTY")
        if not sent then
            diagnostics.phase = "addon_send_call_failed"
            reportError("sendErrors", message)
            return
        end
        diagnostics.sendCalls = diagnostics.sendCalls + 1
    end
    diagnostics.snapshots = diagnostics.snapshots + 1
    diagnostics.phase = "snapshot_send_calls_completed"
    lastSnapshot, lastSnapshotWall = GetTime(), time()
    lastSent, due = GetTime(), 0
end

-- Local diagnostics never send ordinary chat. A send-call count is not evidence
-- that the server or companions received a snapshot; compare receiver counters.
function WarcraftBuddyQuestSyncStatus()
    return {version = "0.2.0", phase = diagnostics.phase, initialized = db ~= nil,
        ownerMatches = isJoel(), partyMembers = GetNumPartyMembers(),
        onUpdateTicks = diagnostics.ticks, publishAttempts = diagnostics.attempts,
        completedSnapshots = diagnostics.snapshots, sendCalls = diagnostics.sendCalls,
        readErrors = diagnostics.readErrors, sendErrors = diagnostics.sendErrors,
        publishErrors = diagnostics.publishErrors, revision = revision,
        updateAge = lastUpdate and GetTime() - lastUpdate,
        attemptAge = diagnostics.attempts > 0 and GetTime() - lastSent or nil,
        snapshotAge = lastSnapshot and GetTime() - lastSnapshot,
        snapshotWallAge = lastSnapshotWall and time() - lastSnapshotWall,
        lastError = lastError, errorWallAge = lastErrorWall and time() - lastErrorWall,
        frameShown = frame.IsShown and frame:IsShown(),
        uiShown = UIParent and UIParent.IsShown and UIParent:IsShown(),
        afk = UnitIsAFK and UnitIsAFK("player") and true or false}
end

SLASH_WARCRAFTBUDDYQUESTSYNC1 = "/wbq"
SlashCmdList.WARCRAFTBUDDYQUESTSYNC = function(command)
    if command and command:lower():match("^%s*refresh%s*$") then
        if isJoel() and type(QueryQuestsCompleted) == "function" then QueryQuestsCompleted() end
        queue()
    end
    local status = WarcraftBuddyQuestSyncStatus()
    local function age(value) return value and string.format("%.1fs", value) or "never" end
    local line = "WBQ " .. status.version .. " " .. status.phase .. " owner=" .. tostring(status.ownerMatches)
        .. " party=" .. status.partyMembers .. " AFK=" .. tostring(status.afk)
        .. " frame=" .. tostring(status.frameShown) .. " UI=" .. tostring(status.uiShown)
        .. " ticks=" .. status.onUpdateTicks .. " attempts=" .. status.publishAttempts
        .. " snapshots=" .. status.completedSnapshots .. " sendCalls=" .. status.sendCalls
        .. " updateAge=" .. age(status.updateAge) .. " sentAge=" .. age(status.snapshotAge)
        .. " wallAge=" .. age(status.snapshotWallAge) .. " errors=" .. (status.readErrors + status.sendErrors + status.publishErrors)
    DEFAULT_CHAT_FRAME:AddMessage(line)
    if status.lastError then DEFAULT_CHAT_FRAME:AddMessage("WBQ last error: " .. status.lastError) end
end

frame:SetScript("OnEvent", function(_, event, ...)
    if event == "CHAT_MSG_ADDON" then
        if WarcraftBuddyInventory then WarcraftBuddyInventory.Receive(...) end
        if WarcraftBuddyTeam then
            local ok,message=pcall(WarcraftBuddyTeam.Receive,...)
            if not ok then reportError("readErrors","Team panel: "..tostring(message)) end
        end
        return
    end
    if event=="PLAYER_EQUIPMENT_CHANGED" or event=="UPDATE_INVENTORY_DURABILITY" or event=="SPELLS_CHANGED" or event=="PLAYER_LEVEL_UP" then gearDue=true end
    if event == "PLAYER_LOGIN" then
        WarcraftBuddyQuestSyncDB = type(WarcraftBuddyQuestSyncDB) == "table" and WarcraftBuddyQuestSyncDB or {}
        db = WarcraftBuddyQuestSyncDB
        -- Joel's requested client preference, applied once so the normal game
        -- setting remains editable afterward.
        if not db.languageFilterPreferenceApplied and type(SetCVar) == "function" then
            SetCVar("profanityFilter", "0")
            db.languageFilterPreferenceApplied = true
        end
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
for _, event in ipairs({"PLAYER_LOGIN", "QUEST_LOG_UPDATE", "QUEST_ACCEPTED", "QUEST_QUERY_COMPLETE", "QUEST_DETAIL", "QUEST_COMPLETE", "QUEST_FINISHED", "PARTY_MEMBERS_CHANGED", "PLAYER_LEVEL_UP", "CHAT_MSG_ADDON", "PLAYER_EQUIPMENT_CHANGED", "UPDATE_INVENTORY_DURABILITY", "SPELLS_CHANGED"}) do
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
    if WarcraftBuddyInventory then WarcraftBuddyInventory.Tick() end
    if WarcraftBuddyTeam then
        local ok,message=pcall(WarcraftBuddyTeam.Tick)
        if not ok then reportError("publishErrors","Team panel: "..tostring(message)) end
    end
    diagnostics.ticks = diagnostics.ticks + 1
    lastUpdate = GetTime()
    if GetTime() - lastSent >= 5 or due > 0 and GetTime() >= due and GetTime() - lastSent >= 1 then
        local ok, message = pcall(publish)
        if not ok then
            snapshotting = false
            diagnostics.phase = "publish_failed"
            reportError("publishErrors", message)
        end
    end
    local gearOK, gearError=pcall(publishOwnerGear)
    if not gearOK then reportError("publishErrors",gearError) end
end)

-- Companion inventory: owner-requested snapshots and explicitly selected actions.
do
local I = {views = {}, assembling = {}, agent = "Arlen", bag = 0, status = "Refresh to inspect your companions' bags."}
WarcraftBuddyInventory = I
local PREFIX, roster = "WBINV1", {Arlen = "0000000000000001", Mira = "0000000000000004", Rook = "0000000000000005"}
local serial, window, render = 0, nil, function() end
local function hex(s, n) return type(s) == "string" and #s == n and not s:find("[^0-9a-f]") end
local function number(s, lo, hi)
    if type(s) ~= "string" or not s:match("^%d+$") or #s > 10 then return nil end
    local v = tonumber(s)
    return v and v >= lo and v <= hi and tostring(v) == s and v or nil
end
local function revision(s)
    return type(s) == "string" and s:match("^%d+$") and (#s == 1 or s:sub(1,1) ~= "0")
        and (#s < 20 or #s == 20 and s <= "18446744073709551615")
end
local function nonce()
    serial = (serial + 1) % 65536
    return string.format("%08x%04x", time() % 4294967296, (math.random(0,65535) + serial) % 65536)
end
local function split(s, sep)
    local out, at = {}, 1
    while true do
        local j = s:find(sep, at, true)
        if not j then out[#out+1] = s:sub(at); return out end
        out[#out+1], at = s:sub(at,j-1), j+1
    end
end
local function partySender(sender)
    if type(sender) ~= "string" then return nil end
    for n = 1, GetNumPartyMembers() do
        local unit = "party" .. n
        local name, guid = UnitName(unit), UnitGUID(unit)
        if name == sender and roster[name] and type(guid) == "string"
            and guid:lower():gsub("^0x", "") == roster[name] then return name end
    end
end
local function send(payload)
    if not isJoel() or GetNumPartyMembers() < 1 or #payload > 248 then return false end
    return pcall(SendAddonMessage, PREFIX, payload, "PARTY")
end
function I.Refresh()
    if I.pending then I.status = I.pending.kind == "D" and "Disenchant is pending. Keep connected; do not retry." or "An inventory action is still pending."; render(); return false end
    I.nonce, I.assembling, I.requestedAt = nonce(), {}, GetTime()
    I.selected, I.confirmDisenchant = nil, nil
    local ok = send("1|R|" .. I.nonce)
    I.status = ok and "Waiting for companion inventory..." or "Join the party to inspect inventory."
    render(); return ok
end
local function decode(parts)
    local result, seen, total = {bags = {}}, {}, 0
    for _, body in ipairs(parts) do
        for _, token in ipairs(split(body, ";")) do
            local kind, text = token:match("^([BI])=(.+)$")
            if not kind then return nil end
            local f = split(text, ",")
            if kind == "B" then
                if #f ~= 4 then return nil end
                local bag, count, entry, known = number(f[1],0,4), number(f[2],0,36), number(f[3],0,2147483647), number(f[4],0,1)
                if not bag or not count or not entry or not known or result.bags[bag] then return nil end
                if bag == 0 and (count ~= 16 or entry ~= 0) then return nil end
                result.bags[bag] = {count=count, entry=entry, known=known==1, slots={}}
            else
                if #f ~= 5 then return nil end
                local bag, slot, entry, count = number(f[1],0,4), number(f[2],0,35), number(f[3],1,2147483647), number(f[4],1,2147483647)
                if not bag or not slot or not entry or not count or not hex(f[5],16) or f[5] == "0000000000000000" or seen[f[5]] then return nil end
                total = total + 1
                if total > 160 then return nil end
                seen[f[5]] = true
                result[#result+1] = {bag=bag, slot=slot, entry=entry, count=count, guid=f[5]}
            end
        end
    end
    for bag = 0,4 do if not result.bags[bag] then return nil end end
    for _, item in ipairs(result) do
        local b = result.bags[item.bag]
        if not b.known or item.slot >= b.count or b.slots[item.slot] then return nil end
        b.slots[item.slot] = item
    end
    return result
end
function I.Receive(prefix, message, channel, sender)
    if prefix ~= PREFIX or channel ~= "PARTY" or type(message) ~= "string" or #message > 248
        or message:find("[%c]") or not isJoel() then return false end
    local agent = partySender(sender)
    if not agent then return false end
    local f = split(message, "|")
    if f[1] ~= "1" then return false end
    if f[2] == "A" then
        local p = I.pending
        if #f ~= 6 or not p or f[3] ~= p.id or f[4] ~= p.snapshot or agent ~= p.agent
            or not f[5]:match("^[A-Z_]+$") or #f[5] > 32 or #f[6] > 48 then return false end
        I.status = agent .. ": " .. f[5] .. (f[6] ~= "" and " - " .. f[6] or "")
        if p.kind == "D" and f[5] ~= "CONFIRMED" and f[5] ~= "BLOCKED" and f[5] ~= "SERVER_REJECTED" then
            local waiting = f[5] == "QUEUED" or f[5] == "PENDING" or f[5] == "REQUESTED"
            I.status = "Mira: disenchant " .. (waiting and "pending" or "unconfirmed") .. ". Keep connected; do not retry."
            render(); return true
        end
        local offered = f[5] == "QUEUED" and f[6] == "item_offered_review_normal_trade_window"
        if offered then I.status = agent .. ": item offered. Review the normal trade window." end
        if not offered and (f[5] == "QUEUED" or f[5] == "PENDING" or f[5] == "REQUESTED") then render(); return true end
        I.pending, I.selected, I.confirmDisenchant = nil, nil, nil
        local receipt = I.status
        I.Refresh(); I.status = receipt; render(); return true
    end
    if f[2] ~= "S" or #f ~= 8 or f[3] ~= I.nonce or not hex(f[3],12) or not hex(f[4],12) or not revision(f[5]) then return false end
    local part, count = number(f[6],1,64), number(f[7],1,64)
    if not part or not count or part > count or GetTime() - (I.requestedAt or -100) > 30 then return false end
    local a = I.assembling[agent]
    if a and (a.snapshot ~= f[4] or a.revision ~= f[5] or a.count ~= count) then return false end
    if not a then a = {snapshot=f[4], revision=f[5], count=count, parts={}, bytes=0, received=0}; I.assembling[agent] = a end
    if a.done then return false end
    if a.parts[part] then return a.parts[part] == f[8] end
    a.parts[part], a.bytes, a.received = f[8], a.bytes + #f[8], a.received + 1
    if a.bytes > 16384 then I.assembling[agent] = nil; return false end
    if a.received ~= count then return true end
    local view = decode(a.parts)
    a.done = true
    if not view then I.status = agent .. ": incomplete or invalid inventory."; render(); return false end
    view.snapshot, view.revision, view.at = a.snapshot, a.revision, GetTime()
    I.views[agent] = view
    I.status = agent .. " inventory received."
    render(); return true
end
function I.Command(kind, destinationBag, destinationSlot)
    local view, selected = I.views[I.agent], I.selected
    if I.pending or not view or GetTime() - view.at > 30 then I.status=I.pending and I.pending.kind == "D" and "Disenchant is pending. Keep connected; do not retry." or "Refresh inventory before acting."; render(); return false end
    if kind ~= "J" and (not selected or selected.agent ~= I.agent) then I.status="Select an item first."; render(); return false end
    local id = nonce()
    local out = {"1",kind,id,view.snapshot,view.revision}
    if kind ~= "J" then
        local b = view.bags[selected.bag]
        local item = b and b.slots[selected.slot]
        if not b or not b.known or not item or item.guid ~= selected.guid then return false end
        if kind == "D" then
            if I.agent ~= "Mira" or item.count ~= 1 then I.status="Select one carried item belonging to Mira."; render(); return false end
            local c = I.confirmDisenchant
            if not c or c.agent ~= I.agent or c.snapshot ~= view.snapshot or c.revision ~= view.revision
                or c.bag ~= item.bag or c.slot ~= item.slot or c.guid ~= item.guid or c.entry ~= item.entry or c.count ~= item.count then
                I.confirmDisenchant = {agent=I.agent,snapshot=view.snapshot,revision=view.revision,
                    bag=item.bag,slot=item.slot,guid=item.guid,entry=item.entry,count=item.count}
                I.status="The selected item will be consumed. Click Confirm disenchant to continue."
                render(); return true
            end
        end
        out[#out+1], out[#out+2], out[#out+3] = tostring(item.bag), tostring(item.slot), item.guid
    end
    if kind == "M" then
        local b = view.bags[destinationBag]
        if not b or not b.known or destinationSlot < 0 or destinationSlot >= b.count or b.slots[destinationSlot] then return false end
        out[#out+1], out[#out+2] = tostring(destinationBag), tostring(destinationSlot)
    elseif kind == "E" then
        if destinationBag < 1 or destinationBag > 4 or view.bags[destinationBag].count ~= 0 then return false end
        out[#out+1] = tostring(destinationBag)
    elseif kind ~= "G" and kind ~= "J" and kind ~= "D" then return false end
    if not send(table.concat(out,"|")) then return false end
    I.confirmDisenchant = nil
    I.pending = {id=id, snapshot=view.snapshot, agent=I.agent, kind=kind, at=GetTime(), deadline=GetTime()+190, nextPoll=GetTime()+5}
    I.status = "Waiting for " .. I.agent .. " to confirm the action."
    render(); return true
end
local function makeWindow()
    if window then return end
    window = CreateFrame("Frame", "WarcraftBuddyInventoryFrame", UIParent)
    window:SetWidth(440); window:SetHeight(445); window:SetPoint("CENTER")
    window:SetFrameStrata("DIALOG"); window:EnableMouse(true); window:SetMovable(true)
    window:RegisterForDrag("LeftButton"); window:SetScript("OnDragStart", window.StartMoving); window:SetScript("OnDragStop", window.StopMovingOrSizing)
    window:SetBackdrop({bgFile="Interface\\DialogFrame\\UI-DialogBox-Background",edgeFile="Interface\\DialogFrame\\UI-DialogBox-Border",tile=true,tileSize=32,edgeSize=32,insets={left=10,right=10,top=10,bottom=10}})
    local title=window:CreateFontString(nil,"OVERLAY","GameFontNormalLarge"); title:SetPoint("TOPLEFT",20,-18); title:SetText("Companion bags")
    local close=CreateFrame("Button",nil,window,"UIPanelCloseButton"); close:SetPoint("TOPRIGHT",-5,-5)
    local function button(text,x,y,width,fn)
        local b=CreateFrame("Button",nil,window,"UIPanelButtonTemplate"); b:SetWidth(width); b:SetHeight(24); b:SetPoint("TOPLEFT",x,y); b:SetText(text); b:SetScript("OnClick",fn); return b
    end
    for n,name in ipairs({"Arlen","Mira","Rook"}) do
        local selectedName=name
        button(name,20+(n-1)*132,-48,126,function() I.agent=selectedName; I.selected=nil; I.confirmDisenchant=nil; render() end)
    end
    for bag=0,4 do
        local selectedBag=bag
        button(bag==0 and "Pack" or "Bag "..bag,20+bag*80,-80,76,function() I.bag=selectedBag; I.confirmDisenchant=nil; render() end)
    end
    local summary=window:CreateFontString(nil,"OVERLAY","GameFontHighlight"); summary:SetPoint("TOPLEFT",24,-116); summary:SetWidth(392); summary:SetJustifyH("LEFT")
    local slots={}
    for n=0,35 do
        local slot=n
        local b=CreateFrame("Button",nil,window); b:SetWidth(39); b:SetHeight(39); b:SetPoint("TOPLEFT",24+(n%9)*44,-140-math.floor(n/9)*44)
        b.icon=b:CreateTexture(nil,"ARTWORK"); b.icon:SetAllPoints()
        b.count=b:CreateFontString(nil,"OVERLAY","NumberFontNormal"); b.count:SetPoint("BOTTOMRIGHT",-2,2)
        b.mark=b:CreateTexture(nil,"OVERLAY"); b.mark:SetAllPoints(); b.mark:SetTexture("Interface\\Buttons\\UI-ActionButton-Border"); b.mark:SetBlendMode("ADD")
        b:SetScript("OnClick",function()
            local v=I.views[I.agent]; local bag=v and v.bags[I.bag]
            if not bag or not bag.known or slot>=bag.count then return end
            local item=bag.slots[slot]
            if item then I.selected={agent=I.agent,bag=I.bag,slot=slot,guid=item.guid}; I.confirmDisenchant=nil; render()
            elseif I.selected then I.Command("M",I.bag,slot) end
        end)
        b:SetScript("OnEnter",function(self)
            local v=I.views[I.agent]; local bag=v and v.bags[I.bag]; local item=bag and bag.slots[slot]
            if item then GameTooltip:SetOwner(self,"ANCHOR_RIGHT"); GameTooltip:SetHyperlink("item:"..item.entry); GameTooltip:Show() end
        end)
        b:SetScript("OnLeave",function() GameTooltip:Hide() end)
        slots[n]=b
    end
    local help=window:CreateFontString(nil,"OVERLAY","GameFontHighlightSmall"); help:SetPoint("TOPLEFT",24,-326); help:SetWidth(392); help:SetJustifyH("LEFT"); help:SetText("Select an item, then click an empty slot to move it.\nGive opens a normal trade with you. Equip uses an empty bag slot.")
    button("Refresh",20,-365,94,function() I.Refresh() end)
    button("Give item",120,-365,94,function() I.Command("G") end)
    button("Equip bag",220,-365,94,function()
        local v=I.views[I.agent]
        if v then for bag=1,4 do if v.bags[bag].known and v.bags[bag].count==0 then I.Command("E",bag); return end end end
        I.status="No empty equipped-bag slot. Move its contents out first."; render()
    end)
    button("Sell junk",320,-365,94,function() I.Command("J") end)
    local disenchant=button("Disenchant item",20,-397,394,function() I.Command("D") end)
    local status=window:CreateFontString(nil,"OVERLAY","GameFontHighlightSmall"); status:SetPoint("TOPLEFT",24,-402); status:SetWidth(392); status:SetJustifyH("LEFT")
    render=function()
        if not window then return end
        local v=I.views[I.agent]; local b=v and v.bags[I.bag]
        local known=b and b.known
        if I.agent == "Mira" then
            disenchant:Show(); disenchant:SetText(I.confirmDisenchant and "Confirm disenchant" or "Disenchant item")
            window:SetHeight(480); status:SetPoint("TOPLEFT",24,-436)
        else
            disenchant:Hide(); window:SetHeight(445); status:SetPoint("TOPLEFT",24,-402)
        end
        local free=0
        if known then for slot=0,b.count-1 do if not b.slots[slot] then free=free+1 end end end
        summary:SetText(I.agent.." / "..(I.bag==0 and "Backpack" or "Bag "..I.bag)..(known and (" - "..free.."/"..b.count.." slots free") or " - inventory unknown"))
        for slot,widget in pairs(slots) do
            local item=known and b.slots[slot]
            if known and slot<b.count then
                widget:Show(); widget.icon:SetTexture(item and ((GetItemIcon and GetItemIcon(item.entry)) or select(10,GetItemInfo(item.entry)) or "Interface\\Icons\\INV_Misc_QuestionMark") or "Interface\\PaperDoll\\UI-Backpack-EmptySlot")
                widget.count:SetText(item and item.count>1 and item.count or "")
                local s=I.selected; widget.mark:SetAlpha(s and s.agent==I.agent and s.bag==I.bag and s.slot==slot and 1 or 0)
            else widget:Hide() end
        end
        status:SetText(I.status)
    end
    window:Hide()
end
function I.Open()
    makeWindow()
    if window:IsShown() then window:Hide() else window:Show(); I.Refresh() end
end
function I.Tick()
    if I.pending and GetTime() > I.pending.deadline then
        if I.pending.kind == "D" then
            if not I.pending.timedOut then I.pending.timedOut=true; I.status="Mira: disenchant unconfirmed. Keep connected; do not retry."; render() end
        else I.pending=nil; I.status="Action unconfirmed. Refresh before making another request."; render() end
    end
    local p=I.pending
    if p and p.kind == "D" and GetTime() >= p.nextPoll then
        p.nextPoll=GetTime()+5
        send("1|P|"..p.id.."|"..p.snapshot)
    end
    if window and window:IsShown() and not I.pending and GetTime()-(I.requestedAt or -100)>20 then I.Refresh() end
end
SlashCmdList["WBINV"] = function() I.Open() end
SLASH_WBINV1 = "/wbinv"
end

-- Team controls send requests only. The host owns safe stopping, normal
-- recovery and receipt checks; neither a click nor an activity implies success.
do
local T={rows={},relays={},pending={},ack={},sequence=0}
WarcraftBuddyTeam=T
local roster={{name="Arlen",guid="1",hex="0000000000000001"},{name="Mira",guid="4",hex="0000000000000004"},
    {name="Rook",guid="5",hex="0000000000000005"},{name="Nixwick",guid="8",hex="0000000000000008"}}
local byGuid={};for _,row in ipairs(roster) do byGuid[row.guid]=row end
local labels={ONLINE="Online",OFFLINE="Offline",FOLLOWING="Following",FIGHTING="Fighting",HEALING="Healing",
    BUFFING="Buffing",LOOTING="Looting",GATHERING="Gathering",MANAGING_SKILLS="Managing skills",
    MANAGING_GEAR="Managing gear",SELLING_TRASH="Selling trash",MAIL="Collecting mail",BUSY="Busy",
    REQUESTED="Request queued",DEFERRED_COMBAT="Waiting for combat to end",STOPPING="Stopping safely",
    STARTING="Restarting",READY="Ready",UNSTUCK_ROUTING="Taking a safe recovery route",
    NEEDS_GM_HELP="Needs help: no safe route",FAILED="Request failed",OUTCOME_UNCONFIRMED="Outcome unconfirmed",
    COMPLETE="Request completed"}
local window,entry,render,lastTick=nil,nil,function() end,-1
local function integer(text,low,high)
    if type(text)~="string" or not text:match("^%d+$") or #text>10 then return end
    local value=tonumber(text)
    if value and value>=low and value<=high and tostring(value)==text then return value end
end
local function hex(text) return type(text)=="string" and #text==12 and not text:find("[^0-9a-f]") end
local function member(subject)
    local expected=byGuid[subject]
    if not expected then return end
    for i=1,GetNumPartyMembers() do
        local unit="party"..i;local guid=UnitGUID(unit)
        if UnitName(unit)==expected.name and type(guid)=="string"
            and guid:lower():gsub("^0x","")==expected.hex then return unit end
    end
end
local function connected(subject)
    local unit=member(subject)
    if not unit then return false,"Not in party" end
    if type(UnitIsConnected)~="function" then return false,"Connection unknown" end
    local online=UnitIsConnected(unit)
    return online==true or online==1, (online==true or online==1) and "Online" or "Offline"
end
local function relay()
    local selected
    for _,row in ipairs(roster) do
        local current=T.relays[row.guid]
        if current and GetTime()>=current.at and GetTime()-current.at<=current.ttl and connected(row.guid)
            and (not selected or current.at>selected.at) then selected=current end
    end
    return selected
end
function T.Request(subject,kind)
    subject=tostring(subject)
    if not isJoel() or not byGuid[subject] or (kind~="RESET" and kind~="UNSTUCK") or T.pending[subject] then return false end
    local current=relay()
    if not current or T.sequence>=2147483647 then return false end
    T.sequence=T.sequence+1
    local pending={nonce=nonce,sequence=T.sequence,kind=kind,at=GetTime(),phase="REQUESTED"}
    T.pending[subject]=pending;T.ack[subject]=nil -- Reserve before the possibly ambiguous send call.
    local payload=table.concat({"R",current.epoch,nonce,T.sequence,subject,kind},"|")
    local ok=pcall(SendAddonMessage,"WBTEAM1",payload,"PARTY")
    if not ok then pending.phase="OUTCOME_UNCONFIRMED" end
    render();return ok
end
function T.Receive(prefix,message,channel,sender)
    if prefix~="WBTEAM1" or channel~="PARTY" or not isJoel() or type(message)~="string"
        or #message>248 or message:find("[%c]") then return false end
    local senderGuid
    for _,row in ipairs(roster) do if sender==row.name and member(row.guid) then senderGuid=row.guid end end
    if not senderGuid then return false end
    local fields={};for value in (message.."|"):gmatch("(.-)|") do fields[#fields+1]=value end
    local epoch,seq,subject,phase,request,requestSeq,ttl=fields[2],integer(fields[3],1,2147483647),fields[4],fields[5],fields[6],integer(fields[7],0,2147483647),integer(fields[8],1,15)
    if #fields~=8 or fields[1]~="S" or type(epoch)~="string" or #epoch<12 or #epoch>64 or epoch:find("[^A-Za-z0-9]")
        or not seq or not byGuid[subject] or not labels[phase] or not requestSeq or not ttl
        or not (request=="-" and requestSeq==0 or hex(request) and requestSeq>0) then return false end
    local previous=T.relays[senderGuid]
    local retired=previous and previous.retired or {}
    for _,old in ipairs(retired) do if epoch==old then return false end end
    local sequences=previous and previous.epoch==epoch and previous.sequences or {}
    if seq<=(sequences[subject] or 0) then return false end
    if previous and previous.epoch~=epoch then
        if #retired>=8 then return false end -- Never forget a retired body to admit another.
        retired[#retired+1]=previous.epoch
    end
    sequences[subject]=seq
    T.relays[senderGuid]={epoch=epoch,at=GetTime(),ttl=ttl,retired=retired,sequences=sequences}
    T.rows[subject]={phase=phase,at=GetTime(),ttl=ttl,relay=senderGuid,epoch=epoch,sequence=seq}
    local pending=T.pending[subject]
    if pending and request==pending.nonce and requestSeq==pending.sequence then
        pending.phase=phase
        if phase=="COMPLETE" or phase=="FAILED" or phase=="NEEDS_GM_HELP" then
            T.ack[subject]={phase=phase,kind=pending.kind,at=GetTime()};T.pending[subject]=nil
        end
    end
    render();return true
end
function T.View(subject)
    subject=tostring(subject)
    local online,connection=connected(subject)
    local row,pending,ack=T.rows[subject],T.pending[subject],T.ack[subject]
    local age=row and math.max(0,math.floor(GetTime()-row.at)) or nil
    local fresh=row and GetTime()>=row.at and GetTime()-row.at<=row.ttl
    local activity=fresh and labels[row.phase] or row and ("Stale: "..labels[row.phase]) or "No host status"
    local request=""
    if pending then
        request=(pending.kind=="RESET" and "Reset: " or "Unstuck: ")..labels[pending.phase]
        if GetTime()-pending.at>15 and pending.phase=="REQUESTED" then request=request.." — awaiting reply" end
    elseif ack then request=(ack.kind=="RESET" and "Reset: " or "Unstuck: ")..labels[ack.phase] end
    return {name=byGuid[subject].name,online=online,connection=connection,activity=activity,age=age,request=request,
        canRequest=isJoel() and not pending and relay()~=nil and T.sequence<2147483647}
end
local function makeWindow()
    if window then return end
    window=CreateFrame("Frame","WarcraftBuddyTeamFrame",UIParent)
    window:SetWidth(590);window:SetHeight(330);window:SetPoint("CENTER");window:SetFrameStrata("DIALOG")
    window:EnableMouse(true);window:SetMovable(true);window:RegisterForDrag("LeftButton")
    window:SetScript("OnDragStart",window.StartMoving);window:SetScript("OnDragStop",window.StopMovingOrSizing)
    window:SetBackdrop({bgFile="Interface\\DialogFrame\\UI-DialogBox-Background",edgeFile="Interface\\DialogFrame\\UI-DialogBox-Border",tile=true,tileSize=32,edgeSize=32,insets={left=10,right=10,top=10,bottom=10}})
    local function text(parent,x,y,width,style)
        local value=parent:CreateFontString(nil,"OVERLAY",style or "GameFontHighlightSmall")
        value:SetPoint("TOPLEFT",x,y);value:SetWidth(width);value:SetJustifyH("LEFT");return value
    end
    text(window,22,-20,500,"GameFontNormalLarge"):SetText("Companions")
    text(window,22,-46,540):SetText("Reset restarts a companion safely. Unstuck requests normal recovery.")
    local close=CreateFrame("Button",nil,window,"UIPanelCloseButton");close:SetPoint("TOPRIGHT",-5,-5)
    local widgets={}
    for i,person in ipairs(roster) do
        local subject,y=person.guid,-76-(i-1)*54
        local row={name=text(window,22,y,80,"GameFontNormal"),connection=text(window,105,y,95),activity=text(window,205,y,205),
            request=text(window,105,y-19,340),age=text(window,410,y,45)}
        for n,kind in ipairs({"RESET","UNSTUCK"}) do
            local action=kind;local button=CreateFrame("Button",nil,window,"UIPanelButtonTemplate")
            button:SetWidth(75);button:SetHeight(23);button:SetPoint("TOPLEFT",465,y-(n-1)*25)
            button:SetText(action=="RESET" and "Reset" or "Unstuck");button:SetScript("OnClick",function() T.Request(subject,action) end)
            row[action]=button
        end
        widgets[subject]=row
    end
    text(window,22,-301,540):SetText("Stale or offline status is shown as observed. A queued request is not success.")
    render=function()
        for _,person in ipairs(roster) do
            local view,row=T.View(person.guid),widgets[person.guid]
            row.name:SetText(view.name);row.connection:SetText(view.connection);row.activity:SetText(view.activity)
            row.age:SetText(view.age and (view.age.."s") or "—");row.request:SetText(view.request)
            for _,kind in ipairs({"RESET","UNSTUCK"}) do if view.canRequest then row[kind]:Enable() else row[kind]:Disable() end end
        end
    end
    window:Hide();render()
end
function T.Init()
    if entry or not UIParent or not isJoel() then return end
    entry=CreateFrame("Button","WarcraftBuddyTeamButton",UIParent,"UIPanelButtonTemplate")
    entry:SetWidth(60);entry:SetHeight(22);entry:SetPoint("TOPRIGHT",UIParent,"TOPRIGHT",-16,-190)
    entry:SetText("Team");entry:SetScript("OnClick",function() T.Open() end)
end
function T.Open()
    if not UIParent then return false end
    makeWindow();if window:IsShown() then window:Hide() else window:Show();render() end
    return true
end
function T.Tick()
    T.Init()
    if GetTime()-lastTick>=1 then lastTick=GetTime();render() end
end
SlashCmdList.WBTEAM=function() T.Open() end
SLASH_WBTEAM1="/wbteam"
end
