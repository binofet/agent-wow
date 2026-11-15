do
    local config = WarcraftBuddyGearLoadouts
    if type(config) ~= "table" or type(config.characterGUID) ~= "string" then return end

    local function validPlan(plan)
        if type(plan) ~= "table" or next(plan) == nil then return false end
        for slot, id in pairs(plan) do
            if type(slot) ~= "number" or slot < 1 or slot > 19 or slot % 1 ~= 0
                or type(id) ~= "number" or id < 0 or id >= 2147483648 or id % 1 ~= 0 then return false end
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

    local labels, plans = {"Tank", "DPS"}, {config.tank, config.dps}
    local panel, feedback, columns, active
    local watcher = CreateFrame("Frame")

    local function refresh()
        if not panel or not panel:IsShown() then return end
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
        if reason then feedback:SetText(reason); return end
        local now = GetTime()
        active = {role = i, next = now, locksUntil = now + 4, skipped = {}, changed = 0}
        watcher:SetScript("OnUpdate", advance)
        advance()
    end

    local function open()
        if not rightCharacter() then return end
        if not panel then
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
            local title = panel:CreateFontString(nil, "OVERLAY", "GameFontNormalLarge")
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
                local button = CreateFrame("Button", nil, panel, "UIPanelButtonTemplate")
                button:SetPoint("TOPLEFT", x, -51)
                button:SetWidth(244)
                button:SetHeight(28)
                button:SetText(label)
                button:SetScript("OnClick", function() selectRole(role) end)
                columns[i] = textAt(x + 2, -90, 244, "GameFontHighlightSmall")
            end
            feedback = textAt(24, -346, 512, "GameFontHighlight")
            feedback:SetText("Choose a set to equip its available gear.")
        end
        panel:Show()
        refresh()
    end

    SLASH_WBGEAR1 = "/wbgear"
    SlashCmdList.WBGEAR = open
    watcher:RegisterEvent("BAG_UPDATE")
    watcher:RegisterEvent("PLAYER_EQUIPMENT_CHANGED")
    watcher:SetScript("OnEvent", refresh)
end
