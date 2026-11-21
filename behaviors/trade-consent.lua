-- Ordinary owner-trade consent. Native trade execution retains ownership,
-- service-item, lease and final-frame checks; this export only says accept/wait.
local accept_owner_trades = true
local owner_guid = "3"

local UINT32 = 4294967295
local INPUT_KEYS = {sessionId=true, selfGuid=true, trade=true}
local TRADE_KEYS = {sessionId=true, revision=true, phase=true, partnerGuid=true,
  partnerAccepted=true, acceptTransportWritten=true, ownRequestedItems=true,
  ownRequestedCopper=true, partnerItems=true, partnerCopper=true}
local ITEM_KEYS = {slot=true, entry=true, count=true, itemGuid=true, name=true, metadataKnown=true}

local function object(value, keys, size)
  if type(value) ~= "table" then return false end
  local count = 0
  for key in pairs(value) do
    if not keys[key] then return false end
    count = count + 1
  end
  return count == size
end

local function uint(value, maximum)
  return type(value) == "number" and value >= 0 and value <= maximum and value == math.floor(value)
end

local function decimal(value, positive)
  if type(value) ~= "string" or #value == 0 or #value > 20 then return false end
  if byte_at(value, 1) == 48 and (#value > 1 or positive) then return false end
  for i = 1, #value do
    local b = byte_at(value, i)
    if b < 48 or b > 57 then return false end
  end
  return #value < 20 or value <= "18446744073709551615"
end

local function session(value)
  return type(value) == "string" and #value > 0 and #value <= 128
end

local function items(rows, maximum)
  if type(rows) ~= "table" or #rows > maximum then return false end
  local count, slots = 0, {}
  for key, row in pairs(rows) do
    if not uint(key, #rows) or key == 0 or not object(row, ITEM_KEYS, 6) or
        not uint(row.slot, maximum - 1) or slots[row.slot] or not uint(row.entry, UINT32) or
        not uint(row.count, UINT32) or not decimal(row.itemGuid, false) or
        type(row.name) ~= "string" or #row.name > 255 or type(row.metadataKnown) ~= "boolean" then return false end
    slots[row.slot] = true
    count = count + 1
  end
  return count == #rows
end

local function wait(reason)
  return {kind="wait", reason=reason}
end

local function decide(input)
  if not object(input, INPUT_KEYS, 3) or not session(input.sessionId) or not decimal(input.selfGuid, true) then
    return wait("current_trade_identity_required")
  end
  local trade = input.trade
  if not object(trade, TRADE_KEYS, 10) or not session(trade.sessionId) or trade.sessionId ~= input.sessionId or
      not decimal(trade.revision, true) or not decimal(trade.partnerGuid, false) or
      type(trade.phase) ~= "string" or #trade.phase == 0 or #trade.phase > 64 or
      type(trade.partnerAccepted) ~= "boolean" or type(trade.acceptTransportWritten) ~= "boolean" or
      not uint(trade.ownRequestedCopper, UINT32) or not uint(trade.partnerCopper, UINT32) or
      not items(trade.ownRequestedItems, 6) or not items(trade.partnerItems, 7) then
    return wait("current_trade_facts_required")
  end
  if accept_owner_trades ~= true or not decimal(owner_guid, true) then return wait("owner_trade_consent_disabled") end
  if trade.partnerGuid ~= owner_guid then return wait("owner_trade_partner_required") end
  if trade.phase ~= "OPEN" then return wait("open_trade_required") end
  if trade.acceptTransportWritten then return wait("trade_accept_already_written") end
  if not trade.partnerAccepted then return wait("partner_consent_required") end
  return {kind="accept", reason="owner_accepted_current_trade"}
end

return {decide=decide}
