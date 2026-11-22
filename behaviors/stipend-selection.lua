-- Choose from the native mail service's qualified training-stipend facts.
-- The caller retains exact request binding, durable custody and server receipts.
local claim_training_stipend = true
local mailbox_interaction_yards = 5
local prefer_nearest_mailbox = true
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

local TARGET = {sessionId=true, selfGuid=true, visitId=true, self=true, actions=true, mailboxes=true, mail=true}
local CUSTODY = {sessionId=true, claimReserved=true, operationInFlight=true}
local SELF = {guid=true, present=true, mapKnown=true, mapId=true, positionKnown=true, positionRevision=true,
  aliveKnown=true, alive=true, unitFlagsKnown=true, unitFlags=true}
local ACTIONS = {moving=true, motionUnconfirmed=true, castPending=true,
  autoAttacking=true, autoAttackRequested=true, autoAttackUnconfirmed=true, interactionOwner=true,
  traveling=true, practicing=true, position=true}
local POSITION = {known=true, mapKnown=true, mapId=true, x=true, y=true, z=true, estimated=true, revision=true}
local BOX = {guid=true, entry=true, present=true, typeKnown=true, gameObjectType=true, mapKnown=true,
  mapId=true, positionKnown=true, x=true, y=true, z=true, positionRevision=true}
local IDLE = {"moving", "motionUnconfirmed", "castPending", "autoAttacking", "autoAttackRequested",
  "autoAttackUnconfirmed", "traveling", "practicing"}

local function coordinate(value)
  return type(value) == "number" and value == value and value > -40000 and value < 40000
end

local function smaller_guid(a, b)
  return #a < #b or #a == #b and a < b
end

local function mailbox_target(input)
  if not object(input, TARGET, 7) or type(input.sessionId) ~= "string" or #input.sessionId == 0 or
      #input.sessionId > 128 or not decimal(input.selfGuid, U64) or not decimal(input.visitId, U64) then
    return wait("current_mailbox_body_and_visit_required")
  end
  local own, actions = input.self, input.actions
  if not object(input.mail, CUSTODY, 3) or input.mail.sessionId ~= input.sessionId or
      type(input.mail.claimReserved) ~= "boolean" or type(input.mail.operationInFlight) ~= "boolean" or
      not object(own, SELF, 10) or own.guid ~= input.selfGuid or type(own.present) ~= "boolean" or
      type(own.mapKnown) ~= "boolean" or not uint(own.mapId, UINT32) or type(own.positionKnown) ~= "boolean" or
      not decimal(own.positionRevision, U64) or type(own.aliveKnown) ~= "boolean" or type(own.alive) ~= "boolean" or
      type(own.unitFlagsKnown) ~= "boolean" or not uint(own.unitFlags, UINT32) or
      not object(actions, ACTIONS, 10) or
      type(actions.interactionOwner) ~= "string" or #actions.interactionOwner > 128 then
    return wait("current_mailbox_body_and_pose_required")
  end
  for _, key in ipairs(IDLE) do
    if type(actions[key]) ~= "boolean" then return wait("current_mailbox_idle_facts_required") end
  end
  local pos = actions.position
  if not object(pos, POSITION, 8) or type(pos.known) ~= "boolean" or type(pos.mapKnown) ~= "boolean" or
      not uint(pos.mapId, UINT32) or not coordinate(pos.x) or not coordinate(pos.y) or not coordinate(pos.z) or
      type(pos.estimated) ~= "boolean" or not decimal(pos.revision, U64) or
      type(input.mailboxes) ~= "table" or #input.mailboxes > 32 then return wait("current_mailbox_pose_and_rows_required") end
  local seen, count = {}, 0
  for index, row in pairs(input.mailboxes) do
    if not uint(index, #input.mailboxes) or index == 0 or not object(row, BOX, 12) or
        not decimal(row.guid, U64) or seen[row.guid] or not uint(row.entry, UINT32) or
        type(row.present) ~= "boolean" or type(row.typeKnown) ~= "boolean" or not uint(row.gameObjectType, UINT32) or
        type(row.mapKnown) ~= "boolean" or not uint(row.mapId, UINT32) or type(row.positionKnown) ~= "boolean" or
        not coordinate(row.x) or not coordinate(row.y) or not coordinate(row.z) or
        not (row.positionRevision == "0" or decimal(row.positionRevision, U64)) or
        row.positionKnown and not decimal(row.positionRevision, U64) then return wait("complete_mailbox_rows_required") end
    seen[row.guid], count = true, count + 1
  end
  if count ~= #input.mailboxes then return wait("complete_mailbox_rows_required") end
  if input.mail.claimReserved or input.mail.operationInFlight then return wait("mail_operation_reserved_or_busy") end
  if not own.present or not own.mapKnown or not own.positionKnown or not own.aliveKnown or not own.alive or
      not own.unitFlagsKnown or not pos.known or not pos.mapKnown or pos.mapId ~= own.mapId then
    return wait("known_same_map_mailbox_pose_required")
  end
  for _, key in ipairs(IDLE) do if actions[key] then return wait("idle_mailbox_pose_required") end end
  if actions.interactionOwner ~= "" then return wait("idle_mailbox_pose_required") end
  if not coordinate(mailbox_interaction_yards) or mailbox_interaction_yards <= 0 or mailbox_interaction_yards > 5 or
      type(prefer_nearest_mailbox) ~= "boolean" then return wait("bounded_mailbox_policy_required") end
  local selected, distance
  for _, row in ipairs(input.mailboxes) do
    if row.present and row.typeKnown and row.gameObjectType == 19 and row.entry > 0 and
        row.mapKnown and row.mapId == pos.mapId and row.positionKnown then
      local dx, dy, dz = row.x-pos.x, row.y-pos.y, row.z-pos.z
      local d = dx*dx + dy*dy + dz*dz
      if d <= mailbox_interaction_yards*mailbox_interaction_yards and (selected == nil or
          prefer_nearest_mailbox and d < distance or not prefer_nearest_mailbox and d > distance or
          d == distance and smaller_guid(row.guid, selected.guid)) then selected, distance = row, d end
    end
  end
  if selected == nil then return wait("no_observed_in_range_mailbox") end
  return {kind="list", sessionId=input.sessionId, selfGuid=input.selfGuid, visitId=input.visitId, mailboxGuid=selected.guid}
end

return {decide=decide, mailbox_target=mailbox_target}
