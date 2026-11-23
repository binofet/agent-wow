-- Lua5.1, full public TOC with simulated widgets/events only; no game or UI calls.
-- lua5.1 dev/test_addon_ui_isolation.lua addons/WarcraftBuddyQuestSync/WarcraftBuddyQuestSync.toc
local toc = arg[1] or "addons/WarcraftBuddyQuestSync/WarcraftBuddyQuestSync.toc"
local directory = assert(toc:match("^(.*[/\\])"), "TOC path must include its directory")
local files = {}
for line in io.lines(toc) do
    local file = line:match("^%s*([^#%s].-%.lua)%s*$")
    if file then files[#files + 1] = file end
end
assert(#files > 0, "TOC must name its actual Lua files")

local function fixture()
    local env = setmetatable({}, {__index = _G})
    env._G = env
    local x = {env = env, frames = {}, loaded = {}, messages = {}, addon = {}, hooks = {},
        now = 100, reloads = 0, macroEdits = 0, ordinaryChatCalls = 0}
    local methods = {}
    function methods:SetScript(name, callback) self.scripts[name] = callback end
    function methods:GetScript(name) return self.scripts[name] end
    function methods:RegisterEvent(name) self.events[name] = true end
    function methods:UnregisterAllEvents() self.events = {} end
    function methods:Show() self.shown = true end
    function methods:Hide() self.shown = false end
    function methods:IsShown() return self.shown end
    function methods:SetText(value) self.text = tostring(value) end
    function methods:Enable() self.enabled = true end
    function methods:Disable() self.enabled = false end
    for _, name in ipairs({"SetWidth", "SetHeight", "SetPoint", "SetFrameStrata", "SetBackdrop",
        "SetJustifyH", "EnableMouse", "SetMovable", "RegisterForDrag", "StartMoving", "StopMovingOrSizing"}) do
        methods[name] = function() end
    end
    local function widget(kind, name, parent)
        return setmetatable({kind = kind, name = name, parent = parent, scripts = {}, events = {}, shown = true},
            {__index = methods})
    end
    function methods:CreateFontString() return widget("FontString", nil, self) end
    env.UIParent = widget("Frame", "UIParent")
    env.DEFAULT_CHAT_FRAME = {AddMessage = function(_, value) x.messages[#x.messages + 1] = value end}
    env.SlashCmdList = {FRIENDS = function() end, WHISPER = function() end}
    env.FriendsFrame = widget("Frame", "FriendsFrame", env.UIParent)
    env.ChatFrame1 = widget("Frame", "ChatFrame1", env.UIParent)
    env.FriendsFrame.scripts.OnEvent = function() end
    env.ChatFrame1.scripts.OnEvent = function() end
    env.WB_TEST_STOCK_SENTINEL = {}
    x.stock = {}
    for _, name in ipairs({"FriendsFrame_Update", "FriendsFrame_OnEvent", "FriendsFrame_OnShow",
        "FriendsFrame_ShowSubFrame", "ToggleFriendsFrame", "GetNumFriends", "GetFriendInfo", "AddFriend",
        "RemoveFriend", "ChatFrame_OnEvent", "ChatFrame_MessageEventHandler", "ChatFrame_OpenChat",
        "ChatFrame_SendTell", "ChatFrame_SendSmartTell", "ChatEdit_SendText", "ChatEdit_OnEnterPressed"}) do
        env[name] = function() error("stock social function must not be invoked: " .. name) end
        x.stock[name] = env[name]
    end
    env.SendChatMessage = function() x.ordinaryChatCalls = x.ordinaryChatCalls + 1 end
    for _, name in ipairs({"_G", "UIParent", "DEFAULT_CHAT_FRAME", "FriendsFrame", "ChatFrame1",
        "SendChatMessage", "WB_TEST_STOCK_SENTINEL"}) do x.stock[name] = env[name] end
    local friendsEvent, chatEvent = env.FriendsFrame.scripts.OnEvent, env.ChatFrame1.scripts.OnEvent
    local friendsSlash, whisperSlash = env.SlashCmdList.FRIENDS, env.SlashCmdList.WHISPER
    local addMessage = env.DEFAULT_CHAT_FRAME.AddMessage
    function x.assertStock()
        for name, original in pairs(x.stock) do assert(env[name] == original, "stock global replaced: " .. name) end
        assert(env.FriendsFrame.scripts.OnEvent == friendsEvent, "stock Friends event script changed")
        assert(env.ChatFrame1.scripts.OnEvent == chatEvent, "stock chat event script changed")
        assert(env.SlashCmdList.FRIENDS == friendsSlash and env.SlashCmdList.WHISPER == whisperSlash,
            "stock social slash handler changed")
        assert(env.DEFAULT_CHAT_FRAME.AddMessage == addMessage, "stock chat output function changed")
        assert(x.ordinaryChatCalls == 0, "addon emitted ordinary chat")
        for name in pairs(x.hooks) do
            assert(name == "AcceptQuest" or name == "GetQuestReward" or name == "AbandonQuest",
                "unexpected global function hook: " .. name)
        end
    end
    function env.CreateFrame(kind, name, parent)
        if name then assert(env[name] == nil, "global frame collision: " .. name) end
        local frame = widget(kind, name, parent)
        x.frames[#x.frames + 1] = frame
        if name then env[name] = frame end
        return frame
    end
    function env.time() return 1700000000 + math.floor(x.now) end
    function env.GetTime() return x.now end
    local names = {party1 = "Arlen", party2 = "Mira", party3 = "Rook", party4 = "Nixwick"}
    local guids = {party1 = "0000000000000001", party2 = "0000000000000004",
        party3 = "0000000000000005", party4 = "0000000000000008"}
    function env.UnitName(unit) return unit == "player" and "Joel" or names[unit] end
    function env.UnitGUID(unit) return unit == "player" and "0x0000000000000003" or guids[unit] end
    function env.UnitLevel() return 40 end
    function env.UnitClass() return "Warrior", "WARRIOR" end
    function env.UnitRace() return "Human", "Human" end
    function env.UnitIsConnected() return true end
    function env.UnitIsAFK() return false end
    function env.GetNumPartyMembers() return 4 end
    function env.GetQuestLogSelection() return 1 end
    function env.SelectQuestLogEntry() end
    function env.GetNumQuestLogEntries() return 1 end
    function env.GetQuestLogTitle(index) if index == 1 then return "Observed quest", 40, nil, 0, false, false, 1, nil, 7 end end
    function env.ExpandQuestHeader() end
    function env.CollapseQuestHeader() end
    function env.QueryQuestsCompleted() x.questQueries = (x.questQueries or 0) + 1 end
    function env.GetQuestsCompleted(result) result[18] = true end
    function env.GetMoney() return 0 end
    function env.GetContainerNumFreeSlots() return 16, 0 end
    function env.GetContainerNumSlots() return 0 end
    function env.GetContainerItemLink() end
    function env.GetContainerItemInfo() end
    function env.GetContainerItemQuestInfo() return false, nil, false end
    function env.GetInventoryItemID() end
    function env.GetInventoryItemLink() end
    function env.GetInventoryItemTexture() end
    function env.GetInventoryItemDurability() end
    function env.GetItemInfo() end
    function env.IsSpellKnown() return false end
    function env.GetNumSpellTabs() return 0 end
    function env.GetNumSkillLines() return 0 end
    function env.GetNumTalentGroups() return 1 end
    function env.GetActiveTalentGroup() return 1 end
    function env.GetUnspentTalentPoints() return 0 end
    function env.GetNumTalentTabs() return 0 end
    function env.InCombatLockdown() return false end
    function env.UnitAffectingCombat() return false end
    function env.UnitIsDeadOrGhost() return false end
    function env.UnitCastingInfo() end
    function env.UnitChannelInfo() end
    function env.GetCursorInfo() end
    function env.ReloadUI() x.reloads = x.reloads + 1 end -- Recorded, never a real UI reload.
    function env.SetCVar(name, value) assert(name == "profanityFilter" and value == "0") end
    function env.hooksecurefunc(name, callback)
        x.hooks[name] = x.hooks[name] or {}
        x.hooks[name][#x.hooks[name] + 1] = callback
    end
    function env.SendAddonMessage(prefix, payload, channel)
        assert(channel == "PARTY" and #payload <= 248, "unexpected addon transport")
        x.addon[#x.addon + 1] = {prefix = prefix, payload = payload}
    end
    env.NUM_BAG_SLOTS, env.BOOKTYPE_SPELL = 0, "spell"
    local macros = {[37] = {"WB Tank", 1, "/wbgear tank"}, [38] = {"WB DPS", 1, "/wbgear dps"}}
    function env.GetNumMacros() return 0, 2 end
    function env.GetMacroInfo(index) if macros[index] then return unpack(macros[index]) end end
    function env.EditMacro(index, name, icon, body)
        local entry = assert(macros[index])
        entry[1], entry[2], entry[3] = name or entry[1], icon or entry[2], body
        x.macroEdits = x.macroEdits + 1
    end
    for _, file in ipairs(files) do
        local chunk = assert(loadfile(directory .. file))
        setfenv(chunk, env)
        chunk("WarcraftBuddyQuestSync")
        x.loaded[file] = true
    end
    assert(x.loaded["PrepAudit.lua"] and x.loaded["Catalog.lua"] and x.loaded["WarcraftBuddyQuestSync.lua"]
        and x.loaded["GearSwapConfig.lua"] and x.loaded["GearSwap.lua"], "full TOC was not loaded")
    function x.event(name, ...)
        for index = 1, #x.frames do
            local frame = x.frames[index]
            if frame.events[name] and frame.scripts.OnEvent then frame.scripts.OnEvent(frame, name, ...) end
        end
    end
    function x.update(seconds)
        x.now = x.now + seconds
        for index = 1, #x.frames do
            local frame = x.frames[index]
            if frame.scripts.OnUpdate then frame.scripts.OnUpdate(frame, seconds) end
        end
    end
    x.assertStock()
    x.event("ADDON_LOADED", "WarcraftBuddyQuestSync")
    x.event("PLAYER_LOGIN")
    assert(env.WarcraftBuddyQuestSyncStatus().initialized, "main addon did not receive actual login")
    assert(env.WarcraftBuddyQuestSyncDB.prepAudit, "TOC audit did not observe login")
    assert(x.macroEdits == 2 and x.reloads == 1, "existing gear migration/reload hook was not simulated")
    assert(env.WarcraftBuddyQuestSyncDB.gearMacroMigration.verified, "actual gear migration did not verify its mock result")
    return x
end

local function test_full_toc_login_keeps_stock_social_globals()
    local x = fixture()
    x.update(1)
    assert(x.reloads == 2, "existing PrepAudit update/reload hook was not simulated")
    x.update(5)
    assert(x.reloads == 2, "existing reload hooks must not repeat without another UI load")
    assert(x.env.WarcraftBuddyQuestSyncStatus().completedSnapshots == 2, "normal heartbeat did not progress")
    x.assertStock()
end

local function test_inventory_receive_error_is_local()
    local x = fixture()
    local env, inventory = x.env, x.env.WarcraftBuddyInventory
    local original = inventory.Receive
    inventory.Receive = function() error("injected Inventory.Receive failure") end
    local before = env.WarcraftBuddyQuestSyncStatus()
    local ok, message = pcall(x.event, "CHAT_MSG_ADDON", "WBTEAM1",
        "S|RELAYISOLATE1|1|5|ONLINE|-|0|15", "PARTY", "Rook")
    assert(ok, "Inventory.Receive escaped the addon event handler: " .. tostring(message))
    local after = env.WarcraftBuddyQuestSyncStatus()
    assert(after.readErrors == before.readErrors + 1 and after.lastError:find("injected Inventory.Receive failure", 1, true),
        "inventory event failure was not retained in local diagnostics")
    assert(env.WarcraftBuddyTeam.rows["5"].sequence == 1 and env.WarcraftBuddyTeam.View(5).activity == "Online",
        "the real Team receiver was skipped after the inventory exception")
    inventory.Receive = original
    x.event("QUEST_QUERY_COMPLETE")
    x.update(1)
    assert(env.WarcraftBuddyQuestSyncStatus().completedSnapshots == before.completedSnapshots + 1,
        "quest handling/heartbeat stopped after the event exception")
    env.SlashCmdList.WARCRAFTBUDDYQUESTSYNC("")
    assert(x.messages[#x.messages]:find("injected Inventory.Receive failure", 1, true), "local status hid the failure")
    x.assertStock()
end

local function test_inventory_tick_error_keeps_heartbeat_and_team()
    local x = fixture()
    local env, inventory, team = x.env, x.env.WarcraftBuddyInventory, x.env.WarcraftBuddyTeam
    local original, realTeamTick, teamTicks = inventory.Tick, team.Tick, 0
    inventory.Tick = function() error("injected Inventory.Tick failure") end
    team.Tick = function(...) teamTicks = teamTicks + 1; return realTeamTick(...) end
    local before = env.WarcraftBuddyQuestSyncStatus()
    local ok, message = pcall(x.update, 1)
    assert(ok, "Inventory.Tick escaped the addon update handler: " .. tostring(message))
    local after = env.WarcraftBuddyQuestSyncStatus()
    assert(after.publishErrors == before.publishErrors + 1 and after.lastError:find("injected Inventory.Tick failure", 1, true),
        "inventory update failure was not retained in local diagnostics")
    assert(after.onUpdateTicks == before.onUpdateTicks + 1 and after.completedSnapshots == before.completedSnapshots + 1,
        "inventory update exception suppressed the actual quest heartbeat")
    assert(teamTicks == 1 and env.WarcraftBuddyTeamButton, "actual Team initialization/tick was skipped")
    assert(x.reloads == 2, "audit's separate update hook was not simulated")
    inventory.Tick, team.Tick = original, realTeamTick
    x.update(5)
    assert(env.WarcraftBuddyQuestSyncStatus().completedSnapshots == after.completedSnapshots + 1,
        "heartbeat did not recover on the next ordinary update")
    assert(env.WarcraftBuddyQuestSyncStatus().publishErrors == after.publishErrors, "restored inventory kept failing")
    x.assertStock()
end

test_full_toc_login_keeps_stock_social_globals()
test_inventory_receive_error_is_local()
test_inventory_tick_error_keeps_heartbeat_and_team()
print("Full-TOC inventory failure containment and stock social UI isolation checks passed")
