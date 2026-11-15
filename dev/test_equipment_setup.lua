local source = assert(arg[1], "equipment setup Lua path required")
local clock, combat, action, equipped, bag, saved, log, frame, cursor, cursorSource, owner, moves, refuse, bindSlot

local function copy(t)
    local out = {}
    for k, v in pairs(t) do out[k] = v end
    return out
end

local function load()
    dofile(source)
    if frame and frame.OnEvent then frame.OnEvent(frame, "PLAYER_ENTERING_WORLD") end
end

local function reset()
    clock, combat, action, owner, moves, refuse = 0, false, nil, "0x0000000000000003", 0, false
    equipped = {[16] = 101, [17] = 102, [15] = 201}
    bag = {301, 302, 401, 402}
    saved, log, frame, cursor = {}, {}, nil, nil
    bindSlot = nil
    WarcraftBuddyQuestSyncDB = nil
    WarcraftBuddyEquipmentSetup = {
        version = "test",
        characterGUID = owner,
        tank = {[16] = 301, [17] = 302},
        dps = {[16] = 401, [17] = 0, [15] = 402},
    }
    NUM_BAG_SLOTS = 4
    DEFAULT_CHAT_FRAME = {AddMessage = function(_, s) log[#log + 1] = s end}
    function CreateFrame()
        frame = {
            SetScript = function(self, key, fn) self[key] = fn end,
            RegisterEvent = function() end,
            UnregisterAllEvents = function() end,
        }
        return frame
    end
    function GetTime() return clock end
    function UnitGUID() return owner end
    function InCombatLockdown() return combat end
    function UnitAffectingCombat() return combat end
    function UnitCastingInfo() return nil end
    function GetCursorInfo() return cursor or action end
    function CursorHasItem() return cursor ~= nil end
    function IsInventoryItemLocked() return false end
    function CursorCanGoInSlot() return not refuse end
    function GetInventoryItemID(_, slot) return equipped[slot] end
    function GetContainerNumSlots(b) return b == 0 and 8 or 0 end
    function GetContainerNumFreeSlots(b) return b == 0 and 4 or 0, 0 end
    function GetContainerItemID(_, slot) return bag[slot] end
    function GetContainerItemInfo(_, slot) return bag[slot] end
    local function pickup(items, i)
        moves = moves + 1
        if cursor then
            cursorSource.items[cursorSource.slot], items[i] = items[i], cursor
            cursor, cursorSource = nil, nil
        else
            cursor, items[i] = items[i], nil
            cursorSource = {items = items, slot = i}
        end
    end
    function PickupInventoryItem(i)
        if cursor and bindSlot == i then return end
        pickup(equipped, i)
    end
    function PickupContainerItem(_, i) pickup(bag, i) end
    function ClearCursor()
        if cursor then cursorSource.items[cursorSource.slot] = cursor end
        cursor, cursorSource = nil, nil
    end
    function GetEquipmentSetInfoByName(name) return saved[name] and name end
    function GetEquipmentSetItemIDs(name) return saved[name] end
    function EquipmentManagerClearIgnoredSlotsForSave() end
    function GetCVarBool() return true end
    function StaticPopup_FindVisible(which, slot) return which == "EQUIP_BIND" and bindSlot == slot end
    function SaveEquipmentSet(name, icon)
        assert(icon == 1)
        saved[name] = copy(equipped)
    end
end

local function step(n)
    for _ = 1, n do
        clock = clock + .3
        if frame and frame.OnUpdate then frame.OnUpdate(frame, .3) end
    end
end

reset()
combat = true
load()
step(4)
assert(moves == 0 and next(saved) == nil)
combat, action = false, "spell"
step(4)
assert(moves == 0 and next(saved) == nil)
action = nil
bag[2] = nil
step(4)
assert(moves == 0 and next(saved) == nil and #log == 1)
bag[2] = 302
step(10)
assert(saved.DPS and not saved.Tank, "DPS saved before second set")
load() -- Saved baseline must survive a UI reload during provisioning.
step(30)
assert(saved.DPS[16] == 401 and saved.DPS[17] == nil and saved.DPS[15] == 402)
assert(saved.Tank[16] == 301 and saved.Tank[17] == 302 and saved.Tank[15] == 201)
assert(equipped[16] == 301 and equipped[17] == 302 and equipped[15] == 201)
assert(WarcraftBuddyQuestSyncDB.equipmentSetup.complete and not frame.OnUpdate)
local baselineMoves = moves
equipped[17] = nil
load()
step(20)
assert(moves == baselineMoves and equipped[17] == nil and not frame.OnUpdate,
    "a later deliberate gear change must not trigger repeat setup")

reset()
owner = "0x0000000000000004"
load()
step(20)
assert(moves == 0 and not frame.OnUpdate)

reset()
refuse = true
load()
step(20)
assert(not frame.OnUpdate and equipped[16] == 101 and next(saved) == nil and not cursor)

reset()
bindSlot = 15
load()
step(50)
assert(frame.OnUpdate and #log == 1 and log[1]:find("binding prompt", 1, true),
    "only the stock prompt can authorize binding even when held open")
bindSlot = nil
PickupInventoryItem(15) -- The player's Okay click.
step(25)
assert(saved.DPS and saved.Tank and equipped[16] == 301 and not frame.OnUpdate)
print("Equipment setup tests passed")
