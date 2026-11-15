-- One-time WotLK equipment manager setup. The optional config specifies
-- version, characterGUID, and tank/dps maps from equipment slots to item IDs.
-- An ID of 0 requires an empty slot. Other slots keep the initial equipment.
local config = WarcraftBuddyEquipmentSetup
if type(config) ~= "table" or type(config.version) ~= "string" or config.version == ""
    or type(config.characterGUID) ~= "string" then return end

local function validSet(set)
    if type(set) ~= "table" or next(set) == nil then return false end
    for slot, id in pairs(set) do
        if type(slot) ~= "number" or slot < 1 or slot > 19 or slot % 1 ~= 0
            or type(id) ~= "number" or id < 0 or id >= 2147483648 or id % 1 ~= 0 then return false end
    end
    return true
end
if not validSet(config.tank) or not validSet(config.dps) then return end

local function equipped(slot)
    return GetInventoryItemID("player", slot) or 0
end

local function bagItem(bag, slot)
    if GetContainerItemID then return GetContainerItemID(bag, slot) end
    local link = GetContainerItemLink(bag, slot)
    return link and tonumber(link:match("item:(%d+)"))
end

local function matches(wanted, getItem)
    for slot, id in pairs(wanted) do
        if (getItem(slot) or 0) ~= id then return false end
    end
    return true
end

local function saved(name, wanted)
    if not GetEquipmentSetInfoByName(name) then return false end
    local items = GetEquipmentSetItemIDs(name)
    return type(items) == "table" and matches(wanted, function(slot) return items[slot] end)
end

local function findCarried()
    local sources, counts = {}, {}
    for slot = 1, 19 do
        local id = equipped(slot)
        if id ~= 0 then counts[id] = (counts[id] or 0) + 1 end
    end
    for bag = 0, NUM_BAG_SLOTS do
        for slot = 1, GetContainerNumSlots(bag) do
            local id = bagItem(bag, slot)
            if id then
                counts[id] = (counts[id] or 0) + 1
                local _, _, locked = GetContainerItemInfo(bag, slot)
                if not locked and not sources[id] then sources[id] = {bag = bag, slot = slot} end
            end
        end
    end
    return sources, counts
end

local function enoughItems(set, counts)
    local required = {}
    for _, id in pairs(set) do
        if id ~= 0 then
            required[id] = (required[id] or 0) + 1
            if required[id] > (counts[id] or 0) then return false end
        end
    end
    return true
end

local function freeSlot()
    for bag = 0, NUM_BAG_SLOTS do
        local free, family = GetContainerNumFreeSlots(bag)
        if free > 0 and (bag == 0 or family == 0) then
            for slot = 1, GetContainerNumSlots(bag) do
                if not GetContainerItemInfo(bag, slot) then return bag, slot end
            end
        end
    end
end

local function selectChange(wanted)
    -- Stow an offhand before equipping a two-hander, without relying on
    -- an implicit unequip that might fail when the bags are full.
    for slot = 1, 19 do
        if wanted[slot] == 0 and equipped(slot) ~= 0 then return slot, 0 end
    end
    for slot = 1, 19 do
        if wanted[slot] and wanted[slot] ~= equipped(slot) then return slot, wanted[slot] end
    end
end

local function moveItem(slot, id, wanted, sources)
    if IsInventoryItemLocked(slot) then return false end
    if id == 0 then
        local bag, bagSlot = freeSlot()
        if not bag then return false, "waiting for an empty bag slot" end
        PickupInventoryItem(slot)
        if not CursorHasItem() then return false end
        PickupContainerItem(bag, bagSlot)
    else
        local source = sources[id]
        if source then
            PickupContainerItem(source.bag, source.slot)
        else
            for from = 1, 19 do
                if from ~= slot and wanted[from] ~= id and equipped(from) == id and not IsInventoryItemLocked(from) then
                    PickupInventoryItem(from)
                    break
                end
            end
        end
        if not CursorHasItem() then return false end
        if not CursorCanGoInSlot(slot) then
            ClearCursor()
            return false, "the client refused item " .. id .. " in slot " .. slot .. ". Check it, then /reload.", true
        end
        PickupInventoryItem(slot)
    end
    return true
end

local frame = CreateFrame("Frame")
local database, wanted, pending, current = nil, nil, nil, 1
local roles = {"DPS", "Tank"}
local message, nextUpdate

local function report(text)
    if message ~= text and DEFAULT_CHAT_FRAME then
        DEFAULT_CHAT_FRAME:AddMessage("Warcraft Buddy equipment: " .. text)
    end
    message = text
end

local function finish()
    frame:UnregisterAllEvents()
    frame:SetScript("OnUpdate", nil)
    frame:SetScript("OnEvent", nil)
end

local function prepare()
    WarcraftBuddyQuestSyncDB = WarcraftBuddyQuestSyncDB or {}
    local store = WarcraftBuddyQuestSyncDB.equipmentSetup
    if type(store) ~= "table" or store.version ~= config.version or type(store.baseline) ~= "table" then
        local baseline = {}
        for slot in pairs(config.tank) do baseline[slot] = equipped(slot) end
        for slot in pairs(config.dps) do baseline[slot] = equipped(slot) end
        store = {version = config.version, baseline = baseline}
        WarcraftBuddyQuestSyncDB.equipmentSetup = store
    end
    database = store
    wanted = {{}, {}}
    for slot, id in pairs(store.baseline) do
        wanted[1][slot], wanted[2][slot] = config.dps[slot] or id, config.tank[slot] or id
    end
end

local function tick()
    local now = GetTime()
    if nextUpdate and now < nextUpdate then return end
    nextUpdate = now + 0.25
    local guid = UnitGUID("player")
    if not guid then return end
    if guid:lower():gsub("^0x", ""):gsub("^0+", "") ~= config.characterGUID:lower():gsub("^0x", ""):gsub("^0+", "") then finish(); return end
    if not database then prepare() end
    if database.complete then finish(); return end
    if InCombatLockdown() or UnitAffectingCombat("player") or UnitCastingInfo("player") then return end

    local name, set = roles[current], wanted[current]
    if pending then
        if pending.slot and equipped(pending.slot) == pending.id or not pending.slot and saved(name, set) then
            pending = nil
        elseif pending.slot and StaticPopup_FindVisible
            and (StaticPopup_FindVisible("EQUIP_BIND", pending.slot) or StaticPopup_FindVisible("AUTOEQUIP_BIND", pending.slot)) then
            report("click Okay on the game's binding prompt for item " .. pending.id .. " to continue " .. name)
            pending.deadline = now + 8
        elseif now >= pending.deadline then
            report("the client did not finish setting up " .. name .. ". Check the equipment or binding prompt, then /reload.")
            finish()
        end
        return
    end
    if GetCursorInfo() then return end

    if saved(name, set) and (current == 1 or matches(set, equipped)) then
        if current == 1 then current = 2; return end
        database.complete = true
        report("Tank and DPS are saved. Tank is equipped.")
        finish()
        return
    end

    local sources, counts = findCarried()
    if not enoughItems(wanted[1], counts) or not enoughItems(wanted[2], counts) then
        report("waiting for all configured items to be in your carried bags or equipped")
        return
    end
    local slot, id = selectChange(set)
    if slot then
        local moved, reason, failed = moveItem(slot, id, set, sources)
        if moved then pending = {slot = slot, id = id, deadline = now + 8}
        elseif reason then report(reason) end
        if failed then finish() end
        return
    end
    if not saved(name, set) then
        if GetCVarBool and not GetCVarBool("equipmentManager") then
            SetCVar("equipmentManager", "1")
            if GearManagerToggleButton then GearManagerToggleButton:Show() end
        end
        EquipmentManagerClearIgnoredSlotsForSave()
        SaveEquipmentSet(name, 1)
        pending = {deadline = now + 8}
    end
end

frame:RegisterEvent("PLAYER_ENTERING_WORLD")
frame:SetScript("OnEvent", function()
    if not SaveEquipmentSet or not GetEquipmentSetItemIDs or not GetEquipmentSetInfoByName
        or not EquipmentManagerClearIgnoredSlotsForSave then finish(); return end
    frame:SetScript("OnUpdate", tick)
end)
