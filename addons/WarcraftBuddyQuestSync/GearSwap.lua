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

    local enchantPlans = setmetatable({},{__mode="k"})
    local function permanentEnchant(link)
        return type(link) == "string" and tonumber(link:match("item:%d+:(%d+):")) or nil
    end
    local function expectedEnchant(plan,slot)
        return enchantPlans[plan] and enchantPlans[plan][slot]
    end
    local function equippedMatches(slot,id,enchant)
        return equipped(slot)==id and (enchant==nil or
            permanentEnchant(GetInventoryItemLink("player",slot))==enchant)
    end

    local function carried()
        local pieces, sources, empty = {}, {}, nil
        for slot = 1,19 do
            local id=equipped(slot)
            if id~=0 then
                pieces[#pieces+1]={id=id,enchant=permanentEnchant(GetInventoryItemLink("player",slot)),equippedSlot=slot}
            end
        end
        for bag=0,NUM_BAG_SLOTS do
            local free,family=GetContainerNumFreeSlots(bag)
            for slot=1,GetContainerNumSlots(bag) do
                local texture,_,locked=GetContainerItemInfo(bag,slot)
                local id=bagItem(bag,slot)
                if id and id>0 then
                    local piece={id=id,enchant=permanentEnchant(GetContainerItemLink(bag,slot)),bag=bag,slot=slot}
                    pieces[#pieces+1]=piece
                    if not locked then sources[#sources+1]=piece end
                elseif not empty and not texture and (free or 0)>0 and (bag==0 or family==0) then
                    empty={bag=bag,slot=slot}
                end
            end
        end
        return pieces,sources,empty
    end

    local function assess(plan,pieces)
        local used,missing,changed={},{},false
        for slot,id in pairs(plan) do
            if not equippedMatches(slot,id,expectedEnchant(plan,slot)) then changed=true end
        end
        -- Reserve exact variants before ID-only requests, so one physical copy
        -- cannot satisfy two slots or consume a constrained slot's only variant.
        for pass=1,2 do
            for slot=1,19 do
                local id,enchant=plan[slot],expectedEnchant(plan,slot)
                if id and id~=0 and ((pass==1 and enchant~=nil) or (pass==2 and enchant==nil)) then
                    local found
                    for index,piece in ipairs(pieces) do
                        if not used[index] and piece.equippedSlot==slot and piece.id==id
                            and (enchant==nil or piece.enchant==enchant) then
                            used[index],found=true,true
                            break
                        end
                    end
                    for index,piece in ipairs(pieces) do
                        if not found and not used[index] and piece.id==id and (enchant==nil or piece.enchant==enchant) then
                            used[index],found=true,true
                            break
                        end
                    end
                    if not found then missing[slot]=id end
                end
            end
        end
        return missing,changed
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

        local enchant=expectedEnchant(plan,slot)
        local source
        for _,piece in ipairs(sources) do
            if piece.id==id and (enchant==nil or piece.enchant==enchant) then
                source=piece
                break
            end
        end
        if source and bagItem(source.bag,source.slot)==id
            and (enchant==nil or permanentEnchant(GetContainerItemLink(source.bag,source.slot))==enchant) then
            PickupContainerItem(source.bag,source.slot)
        else
            for from=1,19 do
                local ownEnchant=expectedEnchant(plan,from)
                local reserved=plan[from] and equippedMatches(from,plan[from],ownEnchant)
                    and (ownEnchant~=nil or enchant==nil)
                if from~=slot and not reserved and equippedMatches(from,id,enchant) and not IsInventoryItemLocked(from) then
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
    local afterSwap, level40Plan, openLevel40Enchants
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
            local enchants={}
            local configured=type(config.enchants)=="table" and config.enchants[keys[role]]
            for slot,enchant in pairs(type(configured)=="table" and configured or {}) do
                if validBinding(slot,enchant) then enchants[slot]=enchant end
            end
            local saved=type(db)=="table" and db.gearLoadoutEnchantOverrides
            local own=type(saved)=="table" and saved[configuredGUID]
            local changes=type(own)=="table" and own[keys[role]]
            for slot,enchant in pairs(type(changes)=="table" and changes or {}) do
                if validBinding(slot,enchant) then enchants[slot]=enchant
                elseif enchant==false then enchants[slot]=nil end
            end
            enchantPlans[plan]=enchants
            plans[role] = plan
        end
    end

    local function storeBinding(role, slot, id, enchant)
        if type(WarcraftBuddyQuestSyncDB) ~= "table" then WarcraftBuddyQuestSyncDB = {} end
        local db = WarcraftBuddyQuestSyncDB
        if type(db.gearLoadoutOverrides) ~= "table" then db.gearLoadoutOverrides = {} end
        local all = db.gearLoadoutOverrides
        if type(all[configuredGUID]) ~= "table" then all[configuredGUID] = {} end
        local key, own = keys[role], all[configuredGUID]
        if type(own[key]) ~= "table" then own[key] = {} end
        own[key][slot] = id
        plans[role][slot] = id ~= nil and id or presets[role][slot]
        if type(db.gearLoadoutEnchantOverrides)~="table" then db.gearLoadoutEnchantOverrides={} end
        local allEnchants=db.gearLoadoutEnchantOverrides
        if type(allEnchants[configuredGUID])~="table" then allEnchants[configuredGUID]={} end
        if type(allEnchants[configuredGUID][key])~="table" then allEnchants[configuredGUID][key]={} end
        local configured=type(config.enchants)=="table" and config.enchants[key]
        local value
        if id==nil then value=type(configured)=="table" and configured[slot] or nil
        else value=enchant end
        if id==nil then allEnchants[configuredGUID][key][slot]=nil
        else allEnchants[configuredGUID][key][slot]=value~=nil and value or false end
        enchantPlans[plans[role]][slot]=value
    end

    local function plannedName(role, slot)
        local id = plans[role][slot]
        local name=id == nil and "current item" or itemName(id)
        local enchant=expectedEnchant(plans[role],slot)
        return enchant~=nil and (name.." [enchant "..enchant.."]") or name
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
                if missing[slot] then lines[#lines + 1] = plannedName(i,slot) end
            end
            columns[i]:SetText(#lines > 0 and ("Missing (" .. #lines .. ")\n" .. table.concat(lines, "\n"))
                or (changed and "All pieces carried" or "Equipped"))
        end
    end

    local function finish(message)
        local callback=afterSwap
        afterSwap=nil
        active = nil
        loadPlans()
        watcher:SetScript("OnUpdate", nil)
        feedback:SetText(message)
        if not panel:IsShown() and DEFAULT_CHAT_FRAME then DEFAULT_CHAT_FRAME:AddMessage(message) end
        refresh()
        if callback then callback() end
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
            if equippedMatches(pending.slot,pending.id,pending.enchant) then
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
            local needsMainHand = slot == 17 and id == 0 and plan[16] and not equippedMatches(16,plan[16],expectedEnchant(plan,16))
            if id and not equippedMatches(slot,id,expectedEnchant(plan,slot)) and not missing[slot] and not active.skipped[slot] and not needsMainHand then
                local oldID = equipped(slot)
                local outcome, why = putItem(plan, slot, id, sources, empty)
                if outcome == "sent" then
                    local kind, cursorID = GetCursorInfo()
                    active.pending = {slot = slot, id = id, enchant=expectedEnchant(plan,slot), deadline = now + 5,
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

    local function pickUpRoleMacro(role,level40)
        local verb, prefix = keys[role], "WB " .. labels[role]
        if level40 then verb="l40"..verb; prefix=prefix.."40" end
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
        local role, slot, id, enchant = editor.role, editor.slot, nil, nil
        if mode == "equipped" then
            id = equipped(slot)
            enchant=permanentEnchant(GetInventoryItemLink("player",slot))
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
            enchant=permanentEnchant(link)
        end
        storeBinding(role, slot, id, enchant)
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
            panel:SetHeight(500)
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
            for role=1,2 do
                local selected=role
                local x=role==1 and 24 or 288
                local button=makeButton(homeWidgets,x,-350,244,28,"Level 40 "..labels[role],function()
                    if active or blocked() or GetCursorInfo() then return end
                    level40Plan(selected,false)
                    selectRole(selected)
                end)
                button:RegisterForDrag("LeftButton")
                button:SetScript("OnDragStart",function() pickUpRoleMacro(selected,true) end)
                makeButton(homeWidgets,x,-385,244,28,"Prepare / enchant "..labels[role].."40",function()
                    if active or blocked() or GetCursorInfo() then return end
                    level40Plan(selected,true)
                    afterSwap=function() openLevel40Enchants(selected) end
                    selectRole(selected)
                end)
            end
            feedback = textAt(24, -440, 512, "GameFontHighlight")
            feedback:SetText("Click a set to equip it. Drag it to an action bar.")
            panel:Hide()
        end
    end

    -- BEGIN JOEL L30 20261120
    local level30Tank = {
        [1]=13127, [2]=13087, [3]=13131, [5]=7688, [6]=9405,
        [7]=13010, [8]=9510, [9]=4438, [10]=9445, [11]=7686,
        [12]=13097, [15]=13108, [16]=6804, [17]=6694, [18]=6696,
    }
    local function level30Plan(role)
        loadPlans()
        local plan = {}
        for slot, id in pairs(plans[role]) do plan[slot] = id end
        for slot, id in pairs(level30Tank) do plan[slot] = id end
        if role == 2 then plan[16], plan[17] = 6975, 0 end
        for slot, id in pairs(plan) do
            local current = equipped(slot)
            if current > 0 and id > 0 and current ~= id then
                local _, _, _, currentLevel = GetItemInfo(current)
                local _, _, _, proposedLevel = GetItemInfo(id)
                if not currentLevel or not proposedLevel or currentLevel > proposedLevel then
                    plan[slot] = current
                end
            end
        end
        if role == 2 and plan[16] ~= 6975 then plan[17] = equipped(17) end
        plans[role] = plan
    end

    local enchantWindow
    local enchantTargets = {
        {16,38873,{6804,6975},"Weapon"},
        {5,38865,{7688},"Chest"}, {9,38854,{4438},"Bracers"},
        {10,38856,{9445},"Gloves"}, {8,38862,{9510},"Boots"},
        {15,38859,{13108},"Cloak"}, {17,38861,{6694},"Shield"},
    }
    local function enchantReason(target)
        if not rightCharacter() then return "Wrong character" end
        local reason = blocked()
        if reason then return reason end
        if GetCursorInfo() then return "Clear your cursor" end
        local id = equipped(target[1])
        local matched = false
        for _, expected in ipairs(target[3]) do
            if id == expected then matched = true end
        end
        if not matched then return "Equip the level-30 item first" end
        local link = GetInventoryItemLink("player",target[1])
        local enchant = link and tonumber(link:match("item:%d+:(%d+):"))
        if not enchant then return "Item data unavailable" end
        if enchant ~= 0 then return "Existing enchant kept" end
        if GetItemCount(target[2]) < 1 then return "Scroll missing" end
    end
    local function openEnchants()
        if InCombatLockdown() then return end
        if not enchantWindow then
            enchantWindow = CreateFrame("Frame",nil,UIParent)
            enchantWindow:SetSize(390,300)
            enchantWindow:SetPoint("CENTER")
            enchantWindow:SetFrameStrata("DIALOG")
            enchantWindow:SetBackdrop({bgFile="Interface\\DialogFrame\\UI-DialogBox-Background",
                edgeFile="Interface\\DialogFrame\\UI-DialogBox-Border",tile=true,tileSize=32,edgeSize=32,
                insets={left=8,right=8,top=8,bottom=8}})
            local close = CreateFrame("Button",nil,enchantWindow,"UIPanelCloseButton")
            close:SetPoint("TOPRIGHT",-4,-4)
            local title = enchantWindow:CreateFontString(nil,"OVERLAY","GameFontNormal")
            title:SetPoint("TOP",0,-18)
            title:SetText("Level 30 enchants")
            for i, target in ipairs(enchantTargets) do
                local entry = target
                local button = CreateFrame("Button",nil,enchantWindow,"SecureActionButtonTemplate,UIPanelButtonTemplate")
                button:SetSize(340,28)
                button:SetPoint("TOPLEFT",24,-42-(i-1)*31)
                button:SetAttribute("type","macro")
                button:SetScript("PreClick",function(self)
                    if InCombatLockdown() then return end
                    local reason = enchantReason(entry)
                    self:SetAttribute("macrotext",reason and "" or
                        "/use [nocombat] item:"..entry[2].."\n/use [nocombat] "..entry[1])
                    self:SetText(entry[4]..(reason and " - "..reason or " - Apply"))
                end)
                button:SetText(entry[4].." - "..(enchantReason(entry) or "Apply"))
            end
        end
        enchantWindow:Show()
    end
    -- END JOEL L30 20261120

    local level40Window
    local function level40Ready()
        local reason=blocked()
        if not reason and GetCursorInfo() then reason="Clear your cursor, then try again." end
        if reason then feedback:SetText(reason); return false end
        return true
    end
    level40Plan=function(role,prepare)
        local key=keys[role]
        local config40=WarcraftBuddyLevel40Loadouts
        local plan,enchants={},{}
        for slot,id in pairs(config40[key]) do plan[slot]=id end
        local pieces=carried()
        for slot,enchant in pairs(config40.enchants[key]) do
            enchants[slot]=enchant
            if prepare then
                local exact,plain=false,false
                for _,piece in ipairs(pieces) do
                    if piece.id==plan[slot] then
                        if piece.enchant==enchant then exact=true end
                        if piece.enchant==0 then plain=true end
                    end
                end
                if not exact and plain then enchants[slot]=0 end
            end
        end
        enchantPlans[plan]=enchants
        plans[role]=plan
    end
    local function level40EnchantReason(role,slot)
        if not rightCharacter() then return "Wrong character" end
        local reason=blocked()
        if reason then return reason end
        if GetCursorInfo() then return "Clear your cursor" end
        local key=keys[role]
        local config40=WarcraftBuddyLevel40Loadouts
        if equipped(slot)~=config40[key][slot] then return "Target item is not equipped" end
        local enchant=permanentEnchant(GetInventoryItemLink("player",slot))
        if enchant==config40.enchants[key][slot] then return "Complete" end
        if enchant==nil then return "Item data unavailable" end
        if enchant~=0 then return "Existing other enchant kept" end
        if GetItemCount(config40.scrolls[key][slot])<1 then return "Scroll missing" end
    end
    openLevel40Enchants=function(role)
        if InCombatLockdown() then return end
        if level40Window then level40Window:Hide() end
        local window=CreateFrame("Frame",nil,UIParent)
        level40Window=window
        window:SetSize(460,350)
        window:SetPoint("CENTER")
        window:SetFrameStrata("DIALOG")
        window:SetBackdrop({bgFile="Interface\\DialogFrame\\UI-DialogBox-Background",
            edgeFile="Interface\\DialogFrame\\UI-DialogBox-Border",tile=true,tileSize=32,edgeSize=32,
            insets={left=8,right=8,top=8,bottom=8}})
        local close=CreateFrame("Button",nil,window,"UIPanelCloseButton")
        close:SetPoint("TOPRIGHT",-4,-4)
        local title=window:CreateFontString(nil,"OVERLAY","GameFontNormal")
        title:SetPoint("TOP",0,-18)
        title:SetText("Level 40 "..labels[role].." enchants")
        local row=0
        for _,slot in ipairs({16,17,5,9,10,8,15,7,18}) do
            if WarcraftBuddyLevel40Loadouts.enchants[keys[role]][slot] then
                local target=slot
                local button=CreateFrame("Button",nil,window,"SecureActionButtonTemplate,UIPanelButtonTemplate")
                button:SetSize(410,28)
                button:SetPoint("TOPLEFT",24,-42-row*32)
                row=row+1
                button:SetAttribute("type","macro")
                button:SetText(slotNames[target].." - "..(level40EnchantReason(role,target) or "Apply"))
                button:SetScript("PreClick",function(self)
                    if InCombatLockdown() then return end
                    local reason=level40EnchantReason(role,target)
                    local scroll=WarcraftBuddyLevel40Loadouts.scrolls[keys[role]][target]
                    self:SetAttribute("macrotext",reason and "" or
                        "/use [nocombat] item:"..scroll.."\n/use [nocombat] "..target)
                    self:SetText(slotNames[target].." - "..(reason or "Apply"))
                end)
            end
        end
        window:Show()
    end

    local combinedWindow
    local function openCombinedPrep()
        if not rightCharacter() or InCombatLockdown() then return end
        if combinedWindow then combinedWindow:Show(); return end
        local phase="tank"
        local window=CreateFrame("Frame",nil,UIParent)
        combinedWindow=window
        window:SetSize(470,180)
        window:SetPoint("CENTER")
        window:SetFrameStrata("DIALOG")
        window:SetBackdrop({bgFile="Interface\\DialogFrame\\UI-DialogBox-Background",
            edgeFile="Interface\\DialogFrame\\UI-DialogBox-Border",tile=true,tileSize=32,edgeSize=32,
            insets={left=8,right=8,top=8,bottom=8}})
        local close=CreateFrame("Button",nil,window,"UIPanelCloseButton")
        close:SetPoint("TOPRIGHT",-4,-4)
        local status=window:CreateFontString(nil,"OVERLAY","GameFontHighlight")
        status:SetPoint("TOPLEFT",24,-32)
        status:SetWidth(420)
        status:SetJustifyH("LEFT")
        status:SetText("Prepare both level-40 sets. Click Next for each equipment or enchant step.")
        local button=CreateFrame("Button",nil,window,"SecureActionButtonTemplate,UIPanelButtonTemplate")
        button:SetPoint("BOTTOM",0,25)
        button:SetSize(340,30)
        button:SetText("Next")
        button:SetAttribute("type","macro")
        button:SetScript("PreClick",function(self)
            if InCombatLockdown() then return end
            self:SetAttribute("macrotext","")
            if active then status:SetText("Equipment swap is running."); return end
            local reason=blocked()
            if not reason and GetCursorInfo() then reason="Clear your cursor to continue." end
            if reason then status:SetText(reason); return end
            if phase=="save" then
                for role=1,2 do
                    level40Plan(role,false)
                    local pieces=carried()
                    local missing=assess(plans[role],pieces)
                    if next(missing) then
                        loadPlans()
                        status:SetText("A required enchanted set piece is missing. Both sets are not verified.")
                        return
                    end
                end
                loadPlans()
                WarcraftBuddyQuestSyncDB.prepCompletion={verifiedCarriedAt=time(),tank=true,dps=true}
                if WarcraftBuddyCapturePrepAudit then WarcraftBuddyCapturePrepAudit() end
                ReloadUI()
                return
            end
            local role=phase=="tank" and 1 or 2
            local key=keys[role]
            local config40=WarcraftBuddyLevel40Loadouts
            level40Plan(role,true)
            local available=carried()
            local missingPreferred,changePreferred=assess(plans[role],available)
            if next(missingPreferred) then
                loadPlans()
                status:SetText(labels[role]..": required exact or unenchanted gear is missing.")
                return
            end
            if changePreferred then
                afterSwap=function() status:SetText(labels[role].." preferred copies equipped. Next continues enchants.") end
                selectRole(role)
                return
            end
            loadPlans()
            for _,slot in ipairs({16,17,5,9,10,8,15,7,18}) do
                local desired=config40.enchants[key][slot]
                if desired and not equippedMatches(slot,config40[key][slot],desired) then
                    local why=level40EnchantReason(role,slot)
                    if not why then
                        local scroll=config40.scrolls[key][slot]
                        self:SetAttribute("macrotext","/use [nocombat] item:"..scroll.."\n/use [nocombat] "..slot)
                        status:SetText(labels[role].." "..slotNames[slot].." enchant requested. Wait for the cast, then Next.")
                        return
                    end
                    if why=="Target item is not equipped" or why=="Existing other enchant kept" then
                        level40Plan(role,true)
                        local pieces=carried()
                        local missing=assess(plans[role],pieces)
                        if missing[slot] then
                            loadPlans()
                            status:SetText(labels[role].." "..slotNames[slot]..": required exact or unenchanted copy missing.")
                            return
                        end
                        afterSwap=function() status:SetText(labels[role].." equipment checked. Next continues enchants.") end
                        selectRole(role)
                        return
                    end
                    status:SetText(labels[role].." "..slotNames[slot]..": "..why)
                    return
                end
            end
            level40Plan(role,false)
            local pieces=carried()
            local missing,changed=assess(plans[role],pieces)
            if next(missing) then
                loadPlans()
                status:SetText(labels[role]..": required gear is still missing.")
                return
            end
            if changed then
                afterSwap=function() status:SetText(labels[role].." equipment checked. Next verifies the set.") end
                selectRole(role)
                return
            end
            loadPlans()
            phase=role==1 and "dps" or "save"
            status:SetText(role==1 and "Tank verified. Next prepares DPS." or "DPS verified. Next saves the final audit.")
        end)
        window:Show()
    end

    local function open(message)
        if not rightCharacter() then return end
        preparePanel()
        local command = (message or ""):match("^%s*(.-)%s*$"):lower()
        if active then return end
        if command=="prep40" then openCombinedPrep(); return end
        if command=="l40tank" or command=="l40dps" or command=="prep40tank" or command=="prep40dps" then
            if not level40Ready() then return end
            local role=command:find("tank",1,true) and 1 or 2
            local prepare=command:find("prep",1,true)==1
            level40Plan(role,prepare)
            if prepare then afterSwap=function() openLevel40Enchants(role) end end
            selectRole(role)
            return
        end
        if command=="enchant40tank" or command=="enchant40dps" then
            openLevel40Enchants(command=="enchant40tank" and 1 or 2)
            return
        end
        if command == "l30tank" or command == "l30dps" then
            local reason = blocked()
            if not reason and GetCursorInfo() then reason = "Clear your cursor, then try again." end
            if reason then feedback:SetText(reason); return end
            local role = command == "l30tank" and 1 or 2
            level30Plan(role)
            selectRole(role)
            return
        end
        if command == "enchant" then openEnchants(); return end
        if command == "status" then
            local free = 0
            for bag = 0, NUM_BAG_SLOTS do free = free + (GetContainerNumFreeSlots(bag) or 0) end
            DEFAULT_CHAT_FRAME:AddMessage("WB gear: "..free.." free bag slots.")
            for slot = 1, 19 do
                local link = GetInventoryItemLink("player",slot)
                if link then DEFAULT_CHAT_FRAME:AddMessage(slotNames[slot].." "..link) end
            end
            return
        end
        if command == "tank" then loadPlans(); selectRole(1); return end
        if command == "dps" then loadPlans(); selectRole(2); return end
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



-- BEGIN WB CURRENT SET MACROS 20261121
do
    local version="current-level40-20261121-v1"
    local targets={["WB Tank"]="l40tank",["WB DPS"]="l40dps"}
    local watcher=CreateFrame("Frame")
    local running=false
    local function readMacros()
        local result={}
        if not GetNumMacros or not GetMacroInfo then return result end
        local general,character=GetNumMacros()
        local function record(index)
            local name,icon,body=GetMacroInfo(index)
            if targets[name] then
                result[#result+1]={index=index,name=name,icon=icon,body=body or ""}
            end
        end
        for index=1,general do record(index) end
        for index=(MAX_ACCOUNT_MACROS or 36)+1,(MAX_ACCOUNT_MACROS or 36)+character do record(index) end
        return result
    end
    WarcraftBuddyReadGearMacros=readMacros
    local captureAudit=WarcraftBuddyCapturePrepAudit
    if type(captureAudit)=="function" then
        WarcraftBuddyCapturePrepAudit=function(...)
            local result=captureAudit(...)
            if type(result)=="table" then
                result.gearMacros=readMacros()
                result.gearMacroMigration=WarcraftBuddyQuestSyncDB.gearMacroMigration
                result.prepTalentApplication=WarcraftBuddyQuestSyncDB.prepTalentApplication
            end
            return result
        end
    end
    local function replacement(body,verb)
        local changed=false
        local result=body:gsub("[^\r\n]+",function(line)
            local command=line:match("^%s*/wbgear%s+(%w+)%s*$")
            if command=="tank" or command=="dps" or command=="l40tank" or command=="l40dps" then
                changed=true
                return "/wbgear "..verb
            end
            return line
        end)
        return result,changed
    end
    local function migrate()
        if running then return end
        local guid=UnitGUID("player")
        local normalized=type(guid)=="string" and guid:lower():gsub("^0x",""):gsub("^0+","")
        if not normalized then return end
        if normalized~="3" then watcher:UnregisterAllEvents(); return end
        if InCombatLockdown() or UnitAffectingCombat("player") then return end
        if type(WarcraftBuddyQuestSyncDB)~="table" then return end
        local previous=WarcraftBuddyQuestSyncDB.gearMacroMigration
        if previous and previous.version==version and previous.verified then
            watcher:UnregisterAllEvents()
            return
        end
        local before=readMacros()
        if #before==0 then return end
        running=true
        local found,errors={},{}
        for _,macro in ipairs(before) do
            found[macro.name]=true
            local body,recognized=replacement(macro.body,targets[macro.name])
            if not recognized then errors[#errors+1]=macro.name..": no recognized gear command line"
            elseif #body>255 then errors[#errors+1]=macro.name..": updated body exceeds 255 bytes"
            elseif body~=macro.body then
                local ok,message=pcall(EditMacro,macro.index,nil,nil,body)
                if not ok then errors[#errors+1]=macro.name..": "..tostring(message) end
            end
            local name,icon,actual=GetMacroInfo(macro.index)
            if name~=macro.name or icon~=macro.icon or actual~=body then
                errors[#errors+1]=macro.name..": edit did not verify"
            end
        end
        local verified=found["WB Tank"] and found["WB DPS"] and #errors==0
        WarcraftBuddyQuestSyncDB.gearMacroMigration={
            version=version,at=time(),before=before,after=readMacros(),verified=not not verified,errors=errors,
        }
        running=false
        watcher:UnregisterAllEvents()
        if DEFAULT_CHAT_FRAME then
            DEFAULT_CHAT_FRAME:AddMessage(verified and "WB Tank and WB DPS now use the enchanted level-40 sets."
                or "WB macro update needs review. Details are recorded in the prep audit.")
        end
        if WarcraftBuddyCapturePrepAudit then WarcraftBuddyCapturePrepAudit() end
        -- Persist the observed macro bodies after Joel's single refresh. On the
        -- next UI load, the saved migration record prevents another reload.
        if not previous or previous.version~=version then ReloadUI() end
    end
    watcher:RegisterEvent("PLAYER_LOGIN")
    watcher:RegisterEvent("PLAYER_REGEN_ENABLED")
    watcher:RegisterEvent("UPDATE_MACROS")
    watcher:SetScript("OnEvent",migrate)
end
-- END WB CURRENT SET MACROS 20261121

-- BEGIN WB PROTECTION 31 20261121
do
    local allocation={
        {"Improved Thunder Clap",3},{"Shield Specialization",2},{"Anticipation",5},
        {"Incite",2},{"Last Stand",1},{"Improved Revenge",2},{"Shield Mastery",2},
        {"Toughness",5},{"Concussion Blow",1},{"Gag Order",2},
        {"One-Handed Weapon Specialization",5},{"Vigilance",1},
    }
    local pending
    local watcher=CreateFrame("Frame")
    local function notify(message)
        if DEFAULT_CHAT_FRAME then DEFAULT_CHAT_FRAME:AddMessage(message) end
    end
    local function actualState(group)
        local spent,ranks=0,{}
        for tab=1,GetNumTalentTabs() do
            local name,_,points=GetTalentTabInfo(tab,false,false,group)
            spent=spent+(points or 0)
            for index=1,GetNumTalents(tab) do
                local talent,_,_,_,rank=GetTalentInfo(tab,index,false,false,group)
                if (rank or 0)>0 then ranks[talent]={rank=rank,tab=name} end
            end
        end
        return spent,ranks
    end
    local function matches(group)
        local spent,ranks=actualState(group)
        if spent~=31 or GetUnspentTalentPoints()~=0 then return false end
        local expected={}
        for _,entry in ipairs(allocation) do expected[entry[1]]=entry[2] end
        for name,entry in pairs(ranks) do
            if entry.tab~="Protection" or expected[name]~=entry.rank then return false end
            expected[name]=nil
        end
        return next(expected)==nil
    end
    local function finishIfObserved()
        if not pending or GetActiveTalentGroup()~=pending.group or not matches(pending.group) then return end
        local record=WarcraftBuddyQuestSyncDB.prepTalentApplication
        record.status,record.verifiedAt="verified",time()
        pending=nil
        watcher:UnregisterAllEvents()
        notify("Protection 31 verified: 31 spent, 0 unspent.")
        if WarcraftBuddyCapturePrepAudit then WarcraftBuddyCapturePrepAudit() end
        if not InCombatLockdown() and not UnitAffectingCombat("player") then ReloadUI() end
    end
    watcher:SetScript("OnEvent",finishIfObserved)
    local function checkPrerequisites(tab,index,group)
        local function check(...)
            for offset=1,select("#",...),4 do
                local tier,column,_,previewLearnable=select(offset,...)
                if tier and column and not previewLearnable then
                    error("A live talent prerequisite is not satisfied.")
                end
            end
        end
        check(GetTalentPrereqs(tab,index,false,false,group))
    end
    SLASH_WBPROTECTION311="/wbprot31"
    SlashCmdList.WBPROTECTION31=function()
        if pending then notify("Talent allocation is awaiting the server's talent update."); return end
        local guid=UnitGUID("player")
        local normalized=type(guid)=="string" and guid:lower():gsub("^0x",""):gsub("^0+","")
        local _,class=UnitClass("player")
        if normalized~="3" or class~="WARRIOR" then notify("This Protection plan belongs to Joel's warrior."); return end
        if InCombatLockdown() or UnitAffectingCombat("player") then notify("Leave combat before applying talents."); return end
        for _,name in ipairs({"GetActiveTalentGroup","GetTalentInfo","GetTalentPrereqs",
            "AddPreviewTalentPoints","GetGroupPreviewTalentPointsSpent","LearnPreviewTalents",
            "ResetGroupPreviewTalentPoints"}) do
            if type(_G[name])~="function" then notify("Talent preview API unavailable: "..name); return end
        end
        local group=GetActiveTalentGroup()
        if matches(group) then notify("Protection 31 is already applied."); return end
        local spent=actualState(group)
        if spent~=0 or GetUnspentTalentPoints()~=31 then
            notify("Use the warrior trainer's unlearn-talents option first. This requires 0 spent and 31 unspent.")
            return
        end
        if GetGroupPreviewTalentPointsSpent(false,group)~=0 then
            notify("An existing talent preview is present. Clear it in the Talents window first.")
            return
        end
        local protection,known
        for tab=1,GetNumTalentTabs() do
            local name=GetTalentTabInfo(tab,false,false,group)
            if name=="Protection" then protection=tab end
        end
        if not protection then notify("The Protection tree was not found."); return end
        known={}
        for index=1,GetNumTalents(protection) do
            local name,_,tier,column,rank,maxRank=GetTalentInfo(protection,index,false,false,group)
            if type(name)=="string" and type(tier)=="number" and type(column)=="number"
                and type(rank)=="number" and type(maxRank)=="number" then
                known[name]={index=index,tier=tier,column=column,rank=rank,maxRank=maxRank}
            end
        end
        for _,entry in ipairs(allocation) do
            local talent=known[entry[1]]
            if not talent or talent.rank~=0 or talent.maxRank<entry[2] then
                notify("The live talent tree does not match the validated plan: "..entry[1])
                return
            end
        end
        local record={requestedAt=time(),group=group,status="staging",allocation=allocation}
        WarcraftBuddyQuestSyncDB.prepTalentApplication=record
        local ok,message=pcall(function()
            local total=0
            for _,entry in ipairs(allocation) do
                local talent=known[entry[1]]
                for rank=1,entry[2] do
                    if total<(talent.tier-1)*5 then error("The live talent tier is not unlocked: "..entry[1]) end
                    checkPrerequisites(protection,talent.index,group)
                    AddPreviewTalentPoints(protection,talent.index,1,false,group)
                    local _,_,_,_,actual,_,_,_,preview=GetTalentInfo(protection,talent.index,false,false,group)
                    total=total+1
                    if actual~=0 or preview~=rank or GetGroupPreviewTalentPointsSpent(false,group)~=total then
                        error("Talent preview did not verify: "..entry[1])
                    end
                end
            end
            if total~=31 or GetUnspentTalentPoints()~=31 or GetActiveTalentGroup()~=group then
                error("The active talent state changed during preview.")
            end
        end)
        if not ok then
            ResetGroupPreviewTalentPoints(false,group)
            record.status,record.error="preview_failed",tostring(message)
            notify("Protection preview stopped: "..tostring(message))
            return
        end
        record.status="submitted"
        pending={group=group}
        watcher:RegisterEvent("PLAYER_TALENT_UPDATE")
        watcher:RegisterEvent("CHARACTER_POINTS_CHANGED")
        local committed,commitError=pcall(LearnPreviewTalents,false)
        if not committed then
            pending=nil
            watcher:UnregisterAllEvents()
            ResetGroupPreviewTalentPoints(false,group)
            record.status,record.error="submit_failed",tostring(commitError)
            notify("Protection application failed: "..tostring(commitError))
            return
        end
        notify("Protection preview submitted once. Waiting for the server's observed talent state.")
        finishIfObserved()
    end
end
-- END WB PROTECTION 31 20261121
