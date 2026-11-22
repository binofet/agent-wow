-- Choose from the native mail service's qualified training-stipend facts.
-- The caller retains exact request binding, durable custody and server receipts.
local claim_training_stipend = true
local UINT32 = 4294967295

local function decimal(value, maximum)
  if type(value) ~= "string" or #value == 0 or #value > #maximum or byte_at(value, 1) == 48 then return false end
  for i = 1, #value do
    local b = byte_at(value, i)
    if b < 48 or b > 57 then return false end
  end
  return #value < #maximum or value <= maximum
end

local function object(value, keys, count)
  if type(value) ~= "table" then return false end
  local n = 0
  for key in pairs(value) do
    if not keys[key] then return false end
    n = n + 1
  end
  return n == count
end

local function uint(value, maximum)
  return type(value) == "number" and value >= 0 and value <= maximum and value == math.floor(value)
end

local function wait(reason)
  return {kind="wait", reason=reason}
end

local INPUT = {sessionId=true, selfGuid=true, mail=true}
local MAIL = {sessionId=true, listKnown=true, listCurrent=true, listRevision=true,
  mailboxGuid=true, claimReserved=true, operationInFlight=true, stipends=true}
local ROW = {mailId=true, copper=true}
local U64 = "18446744073709551615"

local function decide(input)
  if not object(input, INPUT, 3) or type(input.sessionId) ~= "string" or #input.sessionId == 0 or
      #input.sessionId > 128 or not decimal(input.selfGuid, U64) then return wait("current_mail_body_required") end
  local mail = input.mail
  if not object(mail, MAIL, 8) or mail.sessionId ~= input.sessionId or
      not decimal(mail.listRevision, U64) or not decimal(mail.mailboxGuid, U64) or
      type(mail.listKnown) ~= "boolean" or type(mail.listCurrent) ~= "boolean" or
      type(mail.claimReserved) ~= "boolean" or type(mail.operationInFlight) ~= "boolean" or
      type(mail.stipends) ~= "table" or #mail.stipends > 50 then return wait("current_mail_list_required") end
  local count, seen, selected = 0, {}, nil
  -- Validate every supplied row before returning even the first choice.
  for index, row in pairs(mail.stipends) do
    if not uint(index, #mail.stipends) or index == 0 or not object(row, ROW, 2) or
        not decimal(row.mailId, "4294967295") or not uint(row.copper, UINT32) or seen[row.mailId] then
      return wait("complete_stipend_rows_required")
    end
    seen[row.mailId] = true
    count = count + 1
  end
  if count ~= #mail.stipends then return wait("complete_stipend_rows_required") end
  if not mail.listKnown or not mail.listCurrent then return wait("current_mail_list_required") end
  if mail.claimReserved or mail.operationInFlight then return wait("mail_claim_already_reserved_or_busy") end
  if claim_training_stipend ~= true then return wait("training_stipend_claim_disabled") end
  for _, row in ipairs(mail.stipends) do
    if row.copper == 10000 and selected == nil then selected = row end
  end
  if selected == nil then return wait("no_qualified_training_stipend") end
  return {kind="claim", sessionId=input.sessionId, listRevision=mail.listRevision,
    mailboxGuid=mail.mailboxGuid, mailId=selected.mailId}
end

return {decide=decide}
