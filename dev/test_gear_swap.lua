local source = assert(arg[1], "gear swap Lua path required")
local owner = "0x0000000000000003"
local clock, combat, gear, bag, watcher, texts, buttons, cursor, origin, confirm, moves, refuse, oldOnCursor

local function reset()
    clock, combat, moves, refuse, confirm = 0, false, 0, nil, nil
    gear, bag, texts, buttons = {[5] = 201, [16] = 101, [17] = 102}, {301, 302, 401, 403, 402}, {}, {}
    watcher, cursor, origin, oldOnCursor = nil, nil, nil, false
    SlashCmdList, NUM_BAG_SLOTS, UIParent = {}, 4, {}
    WarcraftBuddyGearLoadouts = {
        characterGUID = "0x3",
        tank = {[5] = 301, [16] = 302, [17] = 102},
        dps = {[5] = 403, [16] = 401, [17] = 0, [18] = 402},
        names = {[401] = "Two-handed test weapon", [403] = "DPS chest", [302] = "Tank sword"},
    }
    local function frame()
        local data = {shown = false}
        for _, method in ipairs({"SetWidth", "SetHeight", "SetPoint", "SetFrameStrata", "SetBackdrop", "SetJustifyH", "RegisterEvent"}) do
            data[method] = function() end
        end
        function data:SetScript(key, fn) self[key] = fn end
        function data:SetText(value) self.text = value end
        function data:Show() self.shown = true end
        function data:IsShown() return self.shown end
        function data:CreateFontString() local text = frame(); texts[#texts + 1] = text; return text end
        return data
    end
    function CreateFrame(kind, name, parent, template)
        local out = frame()
        if not watcher then watcher = out end
        if template == "UIPanelButtonTemplate" then buttons[#buttons + 1] = out end
        return out
    end
    function UnitGUID() return owner end
    function GetTime() return clock end
    function GetItemInfo() return nil end
    function GetInventoryItemID(_, i) return gear[i] end
    function GetContainerNumSlots(b) return b == 0 and 10 or 0 end
    function GetContainerNumFreeSlots(b) return b == 0 and 5 or 0, 0 end
    function GetContainerItemID(_, i) return bag[i] end
    function GetContainerItemInfo(_, i) return bag[i], 1, false end
    function InCombatLockdown() return combat end
    function UnitAffectingCombat() return combat end
    function UnitIsDeadOrGhost() return false end
    function UnitCastingInfo() return nil end
    function UnitChannelInfo() return nil end
    function GetCursorInfo() return cursor and "item", cursor end
    function CursorHasItem() return cursor ~= nil end
    function IsInventoryItemLocked() return false end
    function CursorCanGoInSlot() return cursor ~= refuse end
    function StaticPopup_FindVisible(name, slot) return name == "EQUIP_BIND" and confirm == slot end
    local function pickup(items, i)
        moves = moves + 1
        if cursor then
            if oldOnCursor and items == gear and items[i] then
                items[i], cursor = cursor, items[i]
                return
            end
            origin.items[origin.slot], items[i] = items[i], cursor
            cursor, origin = nil, nil
        else
            cursor, items[i], origin = items[i], nil, {items = items, slot = i}
        end
    end
    function PickupInventoryItem(i)
        if cursor and i == confirm then return end
        pickup(gear, i)
    end
    function PickupContainerItem(_, i) pickup(bag, i) end
    function ClearCursor()
        if cursor then origin.items[origin.slot] = cursor end
        cursor, origin = nil, nil
    end
    dofile(source)
    SlashCmdList.WBGEAR()
end

local function step(n)
    for _ = 1, n do
        clock = clock + 0.25
        if watcher.OnUpdate then watcher.OnUpdate(watcher, 0.25) end
    end
end

reset()
step(10)
assert(moves == 0, "opening or waiting in the panel cannot equip anything")
assert(#buttons == 2 and buttons[1].text == "Tank" and buttons[2].text == "DPS")
bag[3] = nil
watcher.OnEvent()
assert(texts[3].text:find("Two-handed test weapon", 1, true))
combat = true
buttons[2].OnClick()
step(10)
assert(moves == 0 and texts[4].text:find("combat", 1, true))
combat = false
buttons[2].OnClick()
step(20)
assert(gear[5] == 403 and gear[18] == 402, "still equip available pieces")
assert(gear[16] == 101 and gear[17] == 102, "missing two-hander must keep the weapon and shield")
assert(bag[4] == 201 and not watcher.OnUpdate and texts[4].text:find("missing items", 1, true))

bag[3] = 401
buttons[2].OnClick()
step(20)
assert(gear[16] == 401 and not gear[17] and bag[3] == 101, "equip two-hander and stow old items")
buttons[1].OnClick()
step(20)
assert(gear[5] == 301 and gear[16] == 302 and gear[17] == 102, "switch back to Tank")
assert(texts[2].text == "Equipped" and not watcher.OnUpdate)
local completed = moves
watcher.OnEvent()
step(12)
assert(moves == completed, "bag changes after a swap cannot start another")

reset()
refuse = 401
buttons[2].OnClick()
step(20)
assert(gear[16] == 101 and gear[17] == 102 and bag[3] == 401)
assert(gear[5] == 403 and gear[18] == 402, "refused main hand should leave shield and allow other pieces")

reset()
confirm = 16
buttons[2].OnClick()
step(36)
assert(watcher.OnUpdate and texts[4].text:find("binding prompt", 1, true))
confirm = nil
PickupInventoryItem(16)
step(20)
assert(gear[16] == 401 and not gear[17] and not watcher.OnUpdate)

reset()
oldOnCursor = true
buttons[2].OnClick()
step(20)
assert(gear[5] == 403 and gear[16] == 401 and gear[18] == 402 and not cursor,
    "a confirmed swap puts its displaced item back and continues through the plan")
assert(bag[4] == 201 and bag[3] == 101 and not watcher.OnUpdate)
print("Gear swap tests passed")
