-- Run with Lua5.1. This adds visible-widget behavior checks to protocol tests.
assert(arg[1], "addon source path required")
dofile("dev/test_inventory_addon.lua")

local I = assert(WarcraftBuddyInventory)
I.views, I.assembling, I.pending, I.selected, I.agent, I.bag = {}, {}, nil, nil, "Arlen", 0
local frames, sent, clock = {}, {}, 1000
UIParent = {kind = "Root"}
function GetTime() return clock end
function SendAddonMessage(prefix, payload, channel)
    assert(prefix == "WBINV1" and channel == "PARTY" and #payload <= 248)
    sent[#sent + 1] = payload
end
GetItemIcon = nil -- Exercise the Wrath GetItemInfo icon fallback too.
function GetItemInfo(entry)
    assert(type(entry) == "number")
    return "Observed item", "item:" .. entry, 1, 1, 1, "Container", "Bag", 1, "", "icon:" .. entry
end
GameTooltip = {
    SetOwner = function(self, owner, anchor) assert(anchor == "ANCHOR_RIGHT"); self.owner = owner end,
    SetHyperlink = function(self, link) self.link = link end,
    Show = function(self) self.shown = true end,
    Hide = function(self) self.shown = false end
}

local methods = {}
function methods:SetWidth(value) self.width = value end
function methods:SetHeight(value) self.height = value end
function methods:SetPoint(...) self.point = {...} end
function methods:SetFrameStrata(value) assert(value == "DIALOG") end
function methods:EnableMouse(value) assert(value == true) end
function methods:SetMovable(value) assert(value == true) end
function methods:RegisterForDrag(value) assert(value == "LeftButton") end
function methods:SetScript(event, fn) assert(type(fn) == "function"); self.scripts[event] = fn end
function methods:SetBackdrop(value) assert(type(value) == "table" and value.insets) end
function methods:SetText(value) self.text = tostring(value) end
function methods:SetJustifyH(value) assert(value == "LEFT") end
function methods:SetAllPoints() self.allPoints = true end
function methods:SetTexture(value) assert(type(value) == "string"); self.texture = value end
function methods:SetBlendMode(value) assert(value == "ADD") end
function methods:SetAlpha(value) self.alpha = value end
function methods:Show() self.shown = true end
function methods:Hide() self.shown = false end
function methods:IsShown() return self.shown end
function methods:StartMoving() self.moving = true end
function methods:StopMovingOrSizing() self.moving = false end
local function widget(kind, parent)
    return setmetatable({kind = kind, parent = parent, scripts = {}, shown = true}, {__index = methods})
end
function methods:CreateFontString() return widget("FontString", self) end
function methods:CreateTexture() return widget("Texture", self) end
function CreateFrame(kind, name, parent, template)
    local frame = widget(kind, parent)
    frame.name, frame.template = name, template
    frames[#frames + 1] = frame
    return frame
end
local function button(label)
    for _, frame in ipairs(frames) do
        if frame.kind == "Button" and frame.text == label then return frame end
    end
    error("missing button " .. label)
end
local function click(frame)
    assert(frame.scripts.OnClick, "button has no click handler")
    frame.scripts.OnClick(frame, "LeftButton")
end
local function snapshot(body, id)
    return I.Receive("WBINV1", "1|S|" .. I.nonce .. "|" .. (id or "abcdef123456") .. "|19|1|1|" .. body,
        "PARTY", I.agent)
end
local function ack(status, reason)
    local p = assert(I.pending)
    return I.Receive("WBINV1", "1|A|" .. p.id .. "|" .. p.snapshot .. "|" .. status .. "|" .. reason,
        "PARTY", p.agent)
end

I.Open()
local window = frames[1]
assert(window.name == "WarcraftBuddyInventoryFrame" and window:IsShown())
assert(sent[#sent]:match("^1|R|%x+$"), "opening requests a fresh observation")
window.scripts.OnDragStart(window); assert(window.moving)
window.scripts.OnDragStop(window); assert(not window.moving)
local slots = {}
for _, frame in ipairs(frames) do
    if frame.kind == "Button" and frame.width == 39 then slots[#slots + 1] = frame end
end
assert(#slots == 36)
for _, frame in ipairs(slots) do assert(not frame:IsShown(), "unknown inventory exposed a slot") end

local bags = "B=0,16,0,1;B=1,6,4496,1;B=2,0,0,1;B=3,0,0,1;B=4,0,0,1"
local items = ";I=0,0,4496,1,4000000000000100;I=1,0,32971,1,4000000000000101"
I.agent = "Arlen"
assert(snapshot(bags .. items))
for n, frame in ipairs(slots) do assert(frame:IsShown() == (n <= 16)) end
assert(slots[1].icon.texture == "icon:4496" and slots[2].icon.texture:find("EmptySlot", 1, true))
slots[1].scripts.OnEnter(slots[1]); assert(GameTooltip.shown and GameTooltip.link == "item:4496")
slots[1].scripts.OnLeave(slots[1]); assert(not GameTooltip.shown)
click(slots[1]); assert(I.selected.guid == "4000000000000100" and slots[1].mark.alpha == 1)
click(button("Bag 1"))
for n, frame in ipairs(slots) do assert(frame:IsShown() == (n <= 6)) end
click(slots[2])
assert(sent[#sent]:match("^1|M|%x+|abcdef123456|19|0|0|4000000000000100|1|1$"),
    "cross-bag empty-slot click lost the selected source binding")
assert(ack("CONFIRMED", "server_inventory_change_verified"))
assert(not I.pending and not I.selected)

-- Equip uses a known empty equipped-bag slot; the server still validates that
-- the selected actual item is a bag. Nothing silently replaces a carried bag.
assert(snapshot(bags .. items, "abcdef123457"))
click(button("Pack")); click(slots[1]); click(button("Equip bag"))
assert(sent[#sent]:match("^1|E|%x+|abcdef123457|19|0|0|4000000000000100|2$"))
assert(ack("BLOCKED", "fixture_no_game_actions"))
assert(not I.pending)

assert(snapshot(bags .. items, "abcdef123458"))
click(button("Bag 1")); click(slots[1]); click(button("Give item"))
assert(sent[#sent]:match("^1|G|%x+|abcdef123458|19|1|0|4000000000000101$"))
assert(ack("QUEUED", "normal_trade_requested_waiting_for_joel") and I.pending)
-- The final offered ACK is not a claim that the human accepted a trade.
assert(ack("QUEUED", "item_offered_review_normal_trade_window") and not I.pending)
assert(I.status:find("item offered", 1, true) and not I.status:find("CONFIRMED", 1, true))

assert(snapshot(bags .. items, "abcdef123459"))
click(button("Sell junk")); assert(sent[#sent]:match("^1|J|%x+|abcdef123459|19$"))
assert(ack("BLOCKED", "fixture_no_game_actions"))

assert(snapshot("B=0,16,0,0;B=1,0,0,0;B=2,0,0,1;B=3,0,0,1;B=4,0,0,1", "abcdef12345a"))
click(button("Pack"))
for _, frame in ipairs(slots) do assert(not frame:IsShown(), "unknown refresh retained actionable slot widgets") end
local before = #sent
click(slots[1]); click(button("Give item")); assert(#sent == before)

local count = #frames
I.Open(); assert(not window:IsShown())
I.Open(); assert(window:IsShown() and #frames == count, "reopen duplicated the lazy window")
print("Companion inventory lazy UI widget checks passed")
