do
    local config = WarcraftBuddyGearLoadouts
    if type(config) ~= "table" or type(config.characterGUID) ~= "string" then return end

    local function validBinding(slot, id)
        return type(slot) == "number" and slot >= 1 and slot <= 19 and slot % 1 == 0
            and type(id) == "number" and id >= 0 and id < 2147483648 and id % 1 == 0
    end

    local function validPlan(plan)
        if type(plan) ~= "table" or next(plan) == nil then return false end
        for slot, id in pairs(plan) do
            if not validBinding(slot, id) then return false end
        end
        return true
    end
    if not validPlan(config.tank) or not validPlan(config.dps) then return end

    local function normalizedGUID(guid)
        if type(guid) ~= "string" then return nil end
        local hex = guid:lower():gsub("^0x", ""):gsub("^0+", "")
        return hex:match("^%x+$")
    end

    local configuredGUID = normalizedGUID(config.characterGUID)
    local function rightCharacter()
        return configuredGUID and normalizedGUID(UnitGUID("player")) == configuredGUID
    end

    local function equipped(slot)
        return GetInventoryItemID("player", slot) or 0
    end

    local function bagItem(bag, slot)
        if GetContainerItemID then return GetContainerItemID(bag, slot) end
        local link = GetContainerItemLink(bag, slot)
        return link and tonumber(link:match("item:(%d+)"))
    end

    local function itemName(id)
        if id == 0 then return "empty slot" end
        local names = config.names
        return GetItemInfo(id) or type(names) == "table" and names[id] or "Item " .. id
    end

    local function carried()
        local counts, sources, empty = {}, {}, nil
        for slot = 1, 19 do
            local id = equipped(slot)
            if id ~= 0 then counts[id] = (counts[id] or 0) + 1 end
        end
        for bag = 0, NUM_BAG_SLOTS do
            local free, family = GetContainerNumFreeSlots(bag)
            for slot = 1, GetContainerNumSlots(bag) do
                local texture, _, locked = GetContainerItemInfo(bag, slot)
                local id = bagItem(bag, slot)
                if id and id > 0 then
                    counts[id] = (counts[id] or 0) + 1
                    if not locked and not sources[id] then sources[id] = {bag = bag, slot = slot} end
                elseif not empty and not texture and (free or 0) > 0 and (bag == 0 or family == 0) then
                    empty = {bag = bag, slot = slot}
                end
            end
        end
        return counts, sources, empty
    end

    local function assess(plan, counts)
        local remaining, missing, changed = {}, {}, false
        for id, n in pairs(counts) do remaining[id] = n end
        for slot, id in pairs(plan) do
            if equipped(slot) == id then
                if id ~= 0 then remaining[id] = (remaining[id] or 1) - 1 end
            else
                changed = true
            end
        end
        for slot = 1, 19 do
            local id = plan[slot]
            if id and id ~= 0 and equipped(slot) ~= id then
                if (remaining[id] or 0) <= 0 then missing[slot] = id
                else remaining[id] = remaining[id] - 1 end
            end
        end
        return missing, changed
    end

    local function blocked()
        if InCombatLockdown() or UnitAffectingCombat("player") then return "Gear swaps wait until you leave combat." end
        if UnitIsDeadOrGhost("player") then return "You need to be alive to swap gear." end
        if UnitCastingInfo("player") or UnitChannelInfo("player") then return "Finish casting before swapping gear." end
    end

    local function putItem(plan, slot, id, sources, empty)
        if IsInventoryItemLocked(slot) then return "wait" end
        if id == 0 then
            if not empty then return "skip", "no free bag slot for your current item" end
            PickupInventoryItem(slot)
            if not CursorHasItem() then return "wait" end
            PickupContainerItem(empty.bag, empty.slot)
            return "sent"
        end

        local source = sources[id]
        if source and bagItem(source.bag, source.slot) == id then
            PickupContainerItem(source.bag, source.slot)
        else
            for from = 1, 19 do
                if from ~= slot and plan[from] ~= id and equipped(from) == id and not IsInventoryItemLocked(from) then
                    PickupInventoryItem(from)
                    break
                end
            end
        end
        if not CursorHasItem() then return "wait" end
        if not CursorCanGoInSlot(slot) then
            ClearCursor()
            return "skip", "the client refused " .. itemName(id)
        end
        PickupInventoryItem(slot)
        return "sent"
    end

    local labels, keys = {"Tank", "DPS"}, {"tank", "dps"}
    local presets, plans = {config.tank, config.dps}, {}
    local slotNames = {"Head", "Neck", "Shoulders", "Shirt", "Chest", "Waist", "Legs", "Feet", "Wrists",
        "Hands", "Ring 1", "Ring 2", "Trinket 1", "Trinket 2", "Back", "Main Hand", "Off Hand", "Ranged", "Tabard"}
    local allowedSlots = {
        INVTYPE_HEAD = {1}, INVTYPE_NECK = {2}, INVTYPE_SHOULDER = {3}, INVTYPE_BODY = {4},
        INVTYPE_CHEST = {5}, INVTYPE_ROBE = {5}, INVTYPE_WAIST = {6}, INVTYPE_LEGS = {7},
        INVTYPE_FEET = {8}, INVTYPE_WRIST = {9}, INVTYPE_HAND = {10}, INVTYPE_FINGER = {11, 12},
        INVTYPE_TRINKET = {13, 14}, INVTYPE_CLOAK = {15}, INVTYPE_WEAPON = {16, 17},
        INVTYPE_WEAPONMAINHAND = {16}, INVTYPE_2HWEAPON = {16, 17}, INVTYPE_WEAPONOFFHAND = {17},
        INVTYPE_SHIELD = {17}, INVTYPE_HOLDABLE = {17}, INVTYPE_RANGED = {18}, INVTYPE_RANGEDRIGHT = {18},
        INVTYPE_THROWN = {18}, INVTYPE_RELIC = {18}, INVTYPE_TABARD = {19},
    }
    local panel, title, feedback, columns, active, editor
    local homeWidgets, editorWidgets, slotButtons = {}, {}, {}
    local watcher = CreateFrame("Frame")

    local function loadPlans()
        local db = WarcraftBuddyQuestSyncDB
        local stored = type(db) == "table" and db.gearLoadoutOverrides
        local overrides = type(stored) == "table" and stored[configuredGUID]
        for role, preset in ipairs(presets) do
            local plan = {}
            for slot, id in pairs(preset) do plan[slot] = id end
            local changes = type(overrides) == "table" and overrides[keys[role]]
            if type(changes) == "table" then
                for slot, id in pairs(changes) do
                    if validBinding(slot, id) then plan[slot] = id end
                end
            end
            plans[role] = plan
        end
    end

    local function storeBinding(role, slot, id)
        if type(WarcraftBuddyQuestSyncDB) ~= "table" then WarcraftBuddyQuestSyncDB = {} end
        local db = WarcraftBuddyQuestSyncDB
        if type(db.gearLoadoutOverrides) ~= "table" then db.gearLoadoutOverrides = {} end
        local all = db.gearLoadoutOverrides
        if type(all[configuredGUID]) ~= "table" then all[configuredGUID] = {} end
        local key, own = keys[role], all[configuredGUID]
        if type(own[key]) ~= "table" then own[key] = {} end
        own[key][slot] = id
        plans[role][slot] = id ~= nil and id or presets[role][slot]
    end

    local function plannedName(role, slot)
        local id = plans[role][slot]
        return id == nil and "current item" or itemName(id)
    end

    local function refresh()
        if not panel or not panel:IsShown() then return end
        if editor then
            for slot, button in ipairs(slotButtons) do
                button:SetText((slot == editor.slot and "> " or "") .. slotNames[slot] .. " - " .. plannedName(editor.role, slot))
            end
            return
        end
        local counts = carried()
        for i, plan in ipairs(plans) do
            local missing, changed = assess(plan, counts)
            local lines = {}
            for slot = 1, 19 do
                if missing[slot] then lines[#lines + 1] = itemName(missing[slot]) end
            end
            columns[i]:SetText(#lines > 0 and ("Missing (" .. #lines .. ")\n" .. table.concat(lines, "\n"))
                or (changed and "All pieces carried" or "Equipped"))
        end
    end

    local function finish(message)
        active = nil
        watcher:SetScript("OnUpdate", nil)
        feedback:SetText(message)
        if not panel:IsShown() and DEFAULT_CHAT_FRAME then DEFAULT_CHAT_FRAME:AddMessage(message) end
        refresh()
    end

    local function advance()
        local now = GetTime()
        if not active or now < active.next then return end
        active.next = now + 0.2
        if not rightCharacter() then finish("This gear list belongs to a different character."); return end
        local reason = blocked()
        if reason then finish(reason .. " Click the set again to continue."); return end

        local pending = active.pending
        if pending then
            if equipped(pending.slot) == pending.id then
                local kind, cursorID = GetCursorInfo()
                if pending.ownCursorID and kind == "item" and cursorID == pending.ownCursorID then ClearCursor() end
                active.pending = nil
                active.changed = active.changed + 1
                active.locksUntil = now + 4
                refresh()
            elseif StaticPopup_FindVisible and (StaticPopup_FindVisible("EQUIP_BIND", pending.slot)
                or StaticPopup_FindVisible("AUTOEQUIP_BIND", pending.slot)) then
                feedback:SetText("Click Okay in the game's binding prompt to continue.")
                pending.deadline = now + 5
                return
            elseif now >= pending.deadline then
                active.skipped[pending.slot] = "the game did not equip " .. itemName(pending.id)
                active.pending = nil
            else return end
        end
        if GetCursorInfo() then finish("Clear your cursor, then click the set again."); return end

        local plan = plans[active.role]
        local counts, sources, empty = carried()
        local missing, changed = assess(plan, counts)
        local waiting
        for slot = 1, 19 do
            local id = plan[slot]
            -- A missing or refused two-hander never prompts a shield removal.
            local needsMainHand = slot == 17 and id == 0 and plan[16] and equipped(16) ~= plan[16]
            if id and id ~= equipped(slot) and not missing[slot] and not active.skipped[slot] and not needsMainHand then
                local oldID = equipped(slot)
                local outcome, why = putItem(plan, slot, id, sources, empty)
                if outcome == "sent" then
                    local kind, cursorID = GetCursorInfo()
                    active.pending = {slot = slot, id = id, deadline = now + 5,
                        ownCursorID = oldID ~= 0 and kind == "item" and cursorID == oldID and oldID or nil}
                    feedback:SetText("Equipping " .. labels[active.role] .. "…")
                    return
                elseif outcome == "skip" then active.skipped[slot] = why
                else waiting = waiting or slot end
            end
        end
        if waiting and now < active.locksUntil then return end
        local name = labels[active.role]
        if not changed then finish(name .. " equipped."); return end
        local reasons = {}
        if next(missing) then reasons[#reasons + 1] = "missing items kept their current slots" end
        if waiting then reasons[#reasons + 1] = "an item remained locked or unavailable" end
        for slot = 1, 19 do
            if active.skipped[slot] then reasons[#reasons + 1] = active.skipped[slot]; break end
        end
        local result = #reasons > 0 and table.concat(reasons, "; ") or "some slots stayed unchanged"
        finish(name .. ": " .. active.changed .. " slots changed. " .. result .. ".")
    end

    local function selectRole(i)
        if active then return end
        local reason = blocked()
        if not rightCharacter() then reason = "This loadout belongs to a different character." end
        if not reason and GetCursorInfo() then reason = "Clear your cursor, then try again." end
        if reason then finish(reason); return end
        local now = GetTime()
        active = {role = i, next = now, locksUntil = now + 4, skipped = {}, changed = 0}
        watcher:SetScript("OnUpdate", advance)
        advance()
    end

    local function pickUpRoleMacro(role)
        local verb, prefix = keys[role], "WB " .. labels[role]
        local body = "/wbgear " .. verb
        if InCombatLockdown() or UnitAffectingCombat("player") then
            feedback:SetText("Leave combat to add the macro to your action bar.")
            return
        end
        if GetCursorInfo() then feedback:SetText("Clear your cursor, then drag the set again."); return end
        if not GetNumMacros or not GetMacroInfo or not CreateMacro or not PickupMacro then
            feedback:SetText("Create a macro with " .. body .. " and drag that macro to your action bar.")
            return
        end

        local general, character = GetNumMacros()
        local base, limit = MAX_ACCOUNT_MACROS or 36, MAX_CHARACTER_MACROS or 18
        local used, index = {}, nil
        for i = 1, general do
            local name = GetMacroInfo(i)
            if name then used[name:lower()] = true end
        end
        for i = base + 1, base + character do
            local name, _, existing = GetMacroInfo(i)
            if name then used[name:lower()] = true end
            if existing == body then index = i end
        end
        if not index then
            if character >= limit then
                feedback:SetText("Character macros are full. Free a slot in /macro, or make a macro with " .. body .. ".")
                return
            end
            local name, suffix = prefix, 1
            while used[name:lower()] do suffix = suffix + 1; name = prefix .. " " .. suffix end
            index = CreateMacro(name, 1, body, true)
            if index and index > base then
                local actualName, _, actualBody = GetMacroInfo(index)
                if actualName ~= name or actualBody ~= body then index = nil end
            else index = nil end
            if not index then feedback:SetText("Create a macro with " .. body .. " and drag it to your action bar."); return end
        end
        PickupMacro(index)
        feedback:SetText("Drop " .. labels[role] .. " on an action bar slot.")
    end

    local function updateSelected(mode)
        if not editor or not editor.slot or active or not rightCharacter() then return end
        local role, slot, id = editor.role, editor.slot, nil
        if mode == "equipped" then
            id = equipped(slot)
        elseif mode == "cursor" then
            local kind, itemID, link = GetCursorInfo()
            if kind ~= "item" or not validBinding(slot, itemID) or itemID == 0 then
                feedback:SetText("Pick up an item from your bags first.")
                return
            end
            local _, _, _, _, _, _, _, _, inventoryType = GetItemInfo(link or itemID)
            if inventoryType ~= nil then
                local compatible = false
                for _, candidate in ipairs(allowedSlots[inventoryType] or {}) do
                    if candidate == slot then compatible = true; break end
                end
                if not compatible then feedback:SetText("That item does not fit " .. slotNames[slot] .. "."); return end
            end
            id = itemID
        end
        storeBinding(role, slot, id)
        feedback:SetText(slotNames[slot] .. " saved for " .. labels[role] .. " (" .. plannedName(role, slot) .. ").")
        refresh()
    end

    local function showWidgets(widgets, visible)
        for _, widget in ipairs(widgets) do
            if visible then widget:Show() else widget:Hide() end
        end
    end

    local function goBack()
        editor = nil
        title:SetText("Gear loadouts")
        showWidgets(editorWidgets, false)
        showWidgets(homeWidgets, true)
        feedback:SetText("Click a set to equip it. Drag it to an action bar.")
        refresh()
    end

    local function makeButton(widgets, x, y, width, height, label, click)
        local button = CreateFrame("Button", nil, panel, "UIPanelButtonTemplate")
        button:SetPoint("TOPLEFT", x, y)
        button:SetWidth(width)
        button:SetHeight(height)
        button:SetText(label)
        button:SetScript("OnClick", click)
        widgets[#widgets + 1] = button
        return button
    end

    local function editRole(role)
        if active then feedback:SetText("Wait for the current swap to finish."); return end
        editor = {role = role, slot = 16}
        if #editorWidgets == 0 then
            makeButton(editorWidgets, 435, -16, 88, 24, "Back", goBack)
            for slot = 1, 19 do
                local selectedSlot, x = slot, slot <= 10 and 24 or 288
                local row = slot <= 10 and slot - 1 or slot - 11
                slotButtons[slot] = makeButton(editorWidgets, x, -48 - row * 24, 244, 22, "", function()
                    editor.slot = selectedSlot
                    feedback:SetText(slotNames[selectedSlot] .. " - " .. plannedName(editor.role, selectedSlot))
                    refresh()
                end)
            end
            makeButton(editorWidgets, 24, -305, 150, 25, "Use Equipped", function() updateSelected("equipped") end)
            makeButton(editorWidgets, 190, -305, 164, 25, "Use Cursor Item", function() updateSelected("cursor") end)
            makeButton(editorWidgets, 370, -305, 160, 25, "Reset Slot", function() updateSelected("reset") end)
        end
        title:SetText("Edit " .. labels[role])
        showWidgets(homeWidgets, false)
        showWidgets(editorWidgets, true)
        feedback:SetText("Choose a slot. Pick up a bag item to use it from the cursor.")
        refresh()
    end

    local function preparePanel()
        if not panel then
            loadPlans()
            panel = CreateFrame("Frame", "WarcraftBuddyGearFrame", UIParent)
            panel:SetWidth(560)
            panel:SetHeight(405)
            panel:SetPoint("CENTER")
            panel:SetFrameStrata("DIALOG")
            panel:SetBackdrop({
                bgFile = "Interface\\DialogFrame\\UI-DialogBox-Background",
                edgeFile = "Interface\\DialogFrame\\UI-DialogBox-Border",
                tile = true, tileSize = 32, edgeSize = 24,
                insets = {left = 7, right = 7, top = 7, bottom = 7},
            })
            title = panel:CreateFontString(nil, "OVERLAY", "GameFontNormalLarge")
            title:SetPoint("TOPLEFT", 24, -20)
            title:SetText("Gear loadouts")
            local close = CreateFrame("Button", nil, panel, "UIPanelCloseButton")
            close:SetPoint("TOPRIGHT", -6, -6)
            local function textAt(x, y, width, template)
                local text = panel:CreateFontString(nil, "OVERLAY", template)
                text:SetPoint("TOPLEFT", x, y)
                text:SetWidth(width)
                text:SetJustifyH("LEFT")
                return text
            end
            columns = {}
            for i, label in ipairs(labels) do
                local role, x = i, i == 1 and 24 or 288
                local button = makeButton(homeWidgets, x, -51, 244, 28, label, function() selectRole(role) end)
                button:RegisterForDrag("LeftButton")
                button:SetScript("OnDragStart", function() pickUpRoleMacro(role) end)
                columns[i] = textAt(x + 2, -115, 244, "GameFontHighlightSmall")
                homeWidgets[#homeWidgets + 1] = columns[i]
            end
            for i, label in ipairs(labels) do
                local role, x = i, i == 1 and 24 or 288
                makeButton(homeWidgets, x, -82, 244, 24, "Edit " .. label, function() editRole(role) end)
            end
            feedback = textAt(24, -346, 512, "GameFontHighlight")
            feedback:SetText("Click a set to equip it. Drag it to an action bar.")
            panel:Hide()
        end
    end

    local function open(message)
        if not rightCharacter() then return end
        preparePanel()
        local command = (message or ""):match("^%s*(.-)%s*$"):lower()
        if command == "tank" then selectRole(1); return end
        if command == "dps" then selectRole(2); return end
        panel:Show()
        if command ~= "" then feedback:SetText("Use /wbgear, /wbgear tank, or /wbgear dps.") end
        refresh()
    end

    SLASH_WBGEAR1 = "/wbgear"
    SlashCmdList.WBGEAR = open
    watcher:RegisterEvent("BAG_UPDATE")
    watcher:RegisterEvent("PLAYER_EQUIPMENT_CHANGED")
    watcher:SetScript("OnEvent", refresh)
end
