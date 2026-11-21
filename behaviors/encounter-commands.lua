-- Identity-bound command events are queued by the host. Every gameplay choice
-- below stays in Lua, including binding marks, approaching and entering Stealth.
local state = {requests={}}
local movement = encounter_movement
local tactics = encounter_tactics
local icons = {star=1,circle=2,diamond=3,triangle=4,moon=5,square=6,x=7,skull=8}
local terminal = {confirmed=true, failed=true, not_dispatched=true, settled_unverified=true}

local function guid(value)
    if type(value) ~= "string" or #value == 0 or #value > 20 or byte_at(value,1) == 48 then return end
    for i=1,#value do
        local byte = byte_at(value,i)
        if byte < 48 or byte > 57 then return end
    end
    if #value < 20 or value <= "18446744073709551615" then return value end
end

local function spell_id(value)
    return type(value) == "number" and value > 0 and value <= 4294967295 and value % 1 == 0 and value or nil
end

local function selector(value)
    if type(value) ~= "table" then return end
    local target, mark = guid(value.targetGuid), icons[value.mark] and value.mark
    if target and not mark then return {targetGuid=target} end
    if mark and not target then return {mark=mark} end
    if value.auto == true and not target and not mark then return {auto=true} end
end

local function request_id(value)
    if type(value.id) == "string" and #value.id > 0 and #value.id <= 80 then return value.id end
    return value.technique .. ":" .. (value.targetGuid or value.mark or "auto")
end

local function cancel(value)
    local requests = {}
    for _, row in ipairs(state.requests) do
        local matches = (not value.id or value.id == row.id) and
            (not value.technique or value.technique == row.technique) and
            (not value.mark or value.mark == row.mark) and
            (not value.targetGuid or value.targetGuid == row.targetGuid or value.targetGuid == row.boundGuid)
        if not matches then requests[#requests+1] = row end
    end
    state.requests = requests
end

local function on_event(event)
    if event.receipt and terminal[event.receipt.outcome] then state.settledAt = state.sequence end
    if event.receipt then
        local payload, outcome = event.receipt.payload or {}, event.receipt.outcome
        if outcome == "failed" or outcome == "not_dispatched" then
            for _, row in ipairs(state.requests) do
                if payload.capability == "cast_self" and payload.spell_id == row.stealthPending then row.stealthPending = nil end
                if payload.capability == row.technique and payload.target_guid == row.boundGuid and
                   payload.spell_id == row.controlPending then row.controlPending = nil end
            end
        end
    end
    if event.kind == "encounter_observation" then state.sequence = event.sequence end
    if event.kind ~= "command" or type(event.value) ~= "table" then return true end
    local value = event.value
    if value.task == "control" then
        if value.cancel == true or value.enabled == false then
            if value.auto == true then state.autoPolymorph = false end
            cancel(value); return true
        end
        if value.technique ~= "polymorph" and value.technique ~= "sap" then return true end
        local target = selector(value)
        if not target or value.spellId ~= nil and not spell_id(value.spellId) then return true end
        if value.auto == true then state.autoPolymorph = true end
        local row = {technique=value.technique, targetGuid=target.targetGuid, mark=target.mark, auto=target.auto, spellId=value.spellId,
                     behind=value.behind == nil and value.technique == "sap" or value.behind == true,
                     maintain=value.maintain ~= false}
        row.id = type(value.id) == "string" and #value.id > 0 and #value.id <= 80 and value.id or request_id(row)
        cancel({id="automatic:polymorph"})
        cancel({id=row.id})
        if #state.requests < 8 then state.requests[#state.requests+1] = row end
    elseif value.task == "cancel_control" then
        if not value.targetGuid and not value.mark then state.autoPolymorph = false end
        cancel(value)
    elseif value.task == "focus" then
        if value.cancel == true then state.focus = nil else state.focus = selector(value) or state.focus end
    elseif value.task == "kill_order" then
        local order = value.order or value.killOrder
        if value.cancel == true then state.killOrder = nil
        elseif type(order) == "table" and #order > 0 and #order <= 32 then
            local copied = {}
            for _, target in ipairs(order) do
                if not guid(target) and not icons[target] then return true end
                copied[#copied+1] = target
            end
            state.killOrder = copied
        end
    end
    return true
end

local function marked_target(obs, mark)
    local party = obs.party or {}
    local group, marks = party.group or {}, party.raidTargets or {}
    if group.known ~= true or group.inGroup ~= true or not guid(group.groupGuid) or
       marks.complete ~= true or marks.groupGuid ~= group.groupGuid then return end
    for _, row in ipairs(marks.marks or {}) do
        if row.icon == icons[mark] then return guid(row.targetGuid) end
    end
end

local function resolve(obs, selected)
    return selected and (selected.targetGuid or marked_target(obs, selected.mark))
end

local function alive(row)
    return row and row.present == true and row.aliveKnown == true and row.alive == true
end

local function observed_control(obs, request)
    for _, aura in ipairs(obs.protectedAuras or {}) do
        if aura.targetGuid == request.boundGuid and aura.capability == request.technique and
           (aura.durationKnown ~= true or type(aura.remainingMs) == "number" and aura.remainingMs > 0) then return true end
    end
    return false
end

local function refresh(obs)
    local requests = {}
    for _, row in ipairs(state.requests) do
        if not row.boundGuid and guid(obs.visitId) then
            row.boundGuid = resolve(obs, row)
            if row.boundGuid then row.visitId = obs.visitId end
        end
        local target = row.visitId == obs.visitId and (obs.targets or {})[row.boundGuid]
        local dead = target and target.present == true and target.aliveKnown == true and target.alive == false
        if row.auto and (dead or row.visitId ~= obs.visitId) then
            row.boundGuid,row.visitId,row.controlPending,row.stealthPending = nil,nil,nil,nil
            dead = false
        end
        row.controlActive = row.visitId == obs.visitId and observed_control(obs, row) or false
        if row.controlActive then row.controlPending = nil end
        if not dead and not (row.controlActive and row.maintain == false) then requests[#requests+1] = row end
    end
    state.requests = requests
end

local function queue_intent(obs)
    refresh(obs)
    local out = {killOrder={}, controlTargets={}}
    for _, target in ipairs(state.killOrder or {}) do
        local resolved = guid(target) or marked_target(obs, target)
        if resolved then out.killOrder[#out.killOrder+1] = resolved end
    end
    out.focusGuid = resolve(obs, state.focus)
    for _, row in ipairs(state.requests) do
        if guid(row.boundGuid) and row.visitId == obs.visitId then out.controlTargets[row.boundGuid] = true end
    end
    return out
end

local function highest_spell(obs, capability, exact)
    local best
    for key, row in pairs(obs.spells or {}) do
        local id = spell_id(tonumber(key))
        local ready = row.ready == true and 2 or row.available == true and 1 or 0
        if id and (not exact or id == exact) and row.known == true and row.capability == capability and
           (not best or ready > best.ready or ready == best.ready and
            ((row.rank or 0) > (best.row.rank or 0) or row.rank == best.row.rank and id < best.id)) then
            best = {id=id,row=row,ready=ready}
        end
    end
    return best
end

local function action(obs, frame, capability, target, spell)
    return {capability=capability, sessionId=obs.sessionId, selfGuid=obs.selfGuid,
        targetGuid=target, spellId=spell, visitId=obs.visitId, observationSequence=frame.input.observationSequence}
end

local function movement_ready(obs, target, range, behind)
    local moved = obs.targetMovement or {}
    return moved.targetGuid == target and moved.visitId == obs.visitId and moved.rangeYards == range and
           moved.requireLos == true and (moved.behind == true) == behind and moved.ready == true
end

local function distance(self, target)
    if self.positionKnown ~= true or target.positionKnown ~= true then return end
    local x, y, z = (self.x or 0)-(target.x or 0), (self.y or 0)-(target.y or 0), (self.z or 0)-(target.z or 0)
    return math.sqrt(x*x+y*y+z*z)
end

local function automatic_target(obs, request, focusGuid)
    if not tactics then return end
    local self = (obs.players or {})[obs.selfGuid]
    if not alive(self) then return end
    local spell = highest_spell(obs, request.technique, request.spellId)
    if not spell or spell.ready == 0 then return end
    local focused,observed,targets = {},{},{}
    for _, target in ipairs({focusGuid or "",obs.observedOwnerFocus or "",obs.activeMeleeTargetGuid or ""}) do
        if guid(target) then focused[target] = true end
    end
    for _, aura in ipairs(obs.protectedAuras or {}) do
        if aura.durationKnown ~= true or (tonumber(aura.remainingMs) or 0) > 0 then
            observed[aura.targetGuid] = {active=true,casterGuid=aura.casterGuid,technique=aura.capability}
        end
    end
    for key, target in pairs(obs.targets or {}) do
        local attack = (obs.engagement or {})[key] or {}
        local separation = distance(self,target)
        targets[key] = {present=target.present,aliveKnown=target.aliveKnown,alive=target.alive,
            attackable=target.attackable,attackVictimKnown=target.engaged == true and guid(attack.victimGuid) ~= nil,
            attackVictimGuid=attack.victimGuid,attackAgeMs=tonumber(attack.ageMs) or 0,
            healthPercent=target.healthKnown == true and (target.maxHealth or 0) > 0 and target.health*100/target.maxHealth or nil,
            inRange=target.mapKnown == true and target.mapId == obs.mapId and separation ~= nil and
                    separation >= (spell.row.minRange or 0) and separation <= (spell.row.maxRange or 0)}
        if target.protected == true then observed[key] = observed[key] or {active=true} end
    end
    if request.technique == "sap" then
        if self.combatKnown ~= true or self.inCombat == true then return end
        local best,health
        for key,target in pairs(targets) do
            local raw = obs.targets[key]
            if guid(key) and alive(target) and target.attackable and target.inRange and not focused[key] and not observed[key] and
               raw.unitFlagsKnown == true and math.floor((raw.unitFlags or 0)/524288)%2 == 0 and target.healthPercent and
               (not best or target.healthPercent > health or target.healthPercent == health and key < best) then
                best,health = key,target.healthPercent
            end
        end
        return best
    end
    local result = tactics.decide({kind="automatic_control",casterGuid=obs.selfGuid,inCombat=true,
        party=obs.party,focusGuid=focusGuid,focusTargets=focused,controlObserved=observed,targets=targets,
        preferredGuid=request.boundGuid,capabilities={{guid=obs.selfGuid,technique="polymorph",ready=true}}})
    return result.control and result.control.targetGuid
end

local function control_action(obs, frame, focusGuid)
    refresh(obs)
    if #state.requests == 0 and state.autoPolymorph ~= false and highest_spell(obs,"polymorph") then
        local automatic = {id="automatic:polymorph",technique="polymorph",auto=true,maintain=true,behind=false}
        local target = automatic_target(obs,automatic,focusGuid)
        if target then
            automatic.boundGuid,automatic.visitId = target,obs.visitId
            state.requests[1] = automatic
        end
    end
    for _, request in ipairs(state.requests) do
        if request.auto and not request.controlActive and not request.controlPending then
            local target = automatic_target(obs,request,focusGuid)
            if target then request.boundGuid,request.visitId = target,obs.visitId
            elseif request.boundGuid == focusGuid or request.boundGuid == obs.observedOwnerFocus then
                request.boundGuid,request.visitId = nil,nil
            end
        end
    end
    local first = state.requests[1]
    if not first then return end
    local status = {pending=true, requestId=first.id, technique=first.technique,
                    targetGuid=first.boundGuid, phase="waiting_for_target"}
    if #frame.pending ~= 0 or state.settledAt == frame.input.observationSequence then return nil,status end
    local self = (obs.players or {})[obs.selfGuid]
    if obs.sessionId ~= frame.session_id or not guid(obs.visitId) or not alive(self) or
       frame.input.observationSequence ~= state.sequence or
       self.mapKnown ~= true or self.mapId ~= obs.mapId or
       obs.hostOwned ~= true and obs.hostRequested ~= true then return nil,status end
    if obs.targetProtectionOverflow == true then status.phase = "waiting_for_protection_observations"; return nil,status end
    for _, request in ipairs(state.requests) do
        local target = (obs.targets or {})[request.boundGuid]
        local spell = highest_spell(obs, request.technique, request.spellId)
        if request.controlActive then
            status = {pending=true,requestId=request.id,technique=request.technique,
                      targetGuid=request.boundGuid,phase="control_observed"}
        elseif request.boundGuid == focusGuid then status.phase = "waiting_for_focus_change"
        elseif request.visitId ~= obs.visitId then status.phase = "waiting_for_visit"
        elseif alive(target) and target.mapKnown == true and target.mapId == obs.mapId and target.attackable == true and spell then
            status = {pending=true, requestId=request.id, technique=request.technique,
                      targetGuid=request.boundGuid, phase="waiting_for_spell"}
            if request.technique == "sap" then
                if self.combatKnown ~= true or self.inCombat == true or target.unitFlagsKnown ~= true or
                   math.floor((target.unitFlags or 0)/524288)%2 == 1 then return nil,status end
                local stealth = highest_spell(obs, "stealth")
                if not stealth then return nil,status end
                if stealth.row.selfAuraActive ~= true then
                    status.phase = "preparing_stealth"
                    if request.stealthPending then status.phase = "waiting_for_stealth_aura"; return nil,status end
                    if stealth.row.available == true and stealth.row.ready ~= true then
                        status.phase = "stopping_for_stealth"
                        local stop = action(obs,frame,"stop_movement",request.boundGuid)
                        stop.rangeYards = 0
                        return stop,status
                    end
                    if stealth.row.ready == true and stealth.row.selfCastSupported == true then
                        local active = guid(obs.activeMeleeTargetGuid)
                        if active then return action(obs,frame,"melee_stop",active),status end
                        request.stealthPending = stealth.id
                        return action(obs,frame,"cast_self",obs.selfGuid,stealth.id),status
                    end
                    return nil,status
                end
                request.stealthPending = nil
            end
            if request.controlPending then status.phase = "waiting_for_control_aura"; return nil,status end
            local range = spell.row.maxRange
            if type(range) ~= "number" or range <= 0 then return nil,status end
            -- Verify an approach even inside range: the movement capability
            -- observes current line of sight and stops/faces this exact target.
            local separation = distance(self,target)
            if movement then
                local moved = movement.decide(obs,frame.input.observationSequence,request.boundGuid,
                    {rangeYards=range,requireLos=true,behind=request.behind == true})
                if moved.action or moved.ready ~= true then
                    status.phase = "approaching_control"
                    return moved.action,status
                end
            elseif self.moving == true or not separation or separation > range or
               not movement_ready(obs,request.boundGuid,range,request.behind == true) then
                status.phase = "approaching_control"
                local move = action(obs,frame,"approach_target",request.boundGuid)
                move.rangeYards,move.requireLos,move.behind = range,true,request.behind == true
                return move,status
            end
            if spell.row.ready == true then
                status.phase = "casting_control"
                local active = guid(obs.activeMeleeTargetGuid)
                if active then return action(obs,frame,"melee_stop",active),status end
                request.controlPending = spell.id
                return action(obs,frame,request.technique,request.boundGuid,spell.id),status
            end
            return nil,status
        end
    end
    return nil,status
end

local function export_state()
    return {version=1,data=state}
end

local function import_state(saved)
    if saved.version ~= 1 or type(saved.data) ~= "table" or type(saved.data.requests) ~= "table" or
       #saved.data.requests > 8 then return false end
    for _, row in ipairs(saved.data.requests) do
        if type(row) ~= "table" or type(row.id) ~= "string" or #row.id == 0 or #row.id > 80 or
           (row.technique ~= "polymorph" and row.technique ~= "sap") or not selector(row) or
           row.spellId ~= nil and not spell_id(row.spellId) or
           row.boundGuid ~= nil and (not guid(row.boundGuid) or not guid(row.visitId)) or
           row.stealthPending ~= nil and not spell_id(row.stealthPending) or
           row.controlPending ~= nil and not spell_id(row.controlPending) then return false end
    end
    if saved.data.focus ~= nil and not selector(saved.data.focus) then return false end
    if saved.data.killOrder ~= nil then
        if type(saved.data.killOrder) ~= "table" or #saved.data.killOrder > 32 then return false end
        for _, target in ipairs(saved.data.killOrder) do if not guid(target) and not icons[target] then return false end end
    end
    state = saved.data
    return true
end

return {on_event=on_event, export_state=export_state, import_state=import_state,
        queue_intent=queue_intent, control_action=control_action}
