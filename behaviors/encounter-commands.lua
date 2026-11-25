-- Identity-bound command events are queued by the host. Every gameplay choice
-- below stays in Lua, including binding marks, approaching and entering Stealth.
local state = {requests={}}
local ranged_options, ranged_control_proposal, ranged_hold_proposal, ranged_ground_proposal
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

local function cancel(value, stop_movement)
    local requests = {}
    for _, row in ipairs(state.requests) do
        local matches = (not value.id or value.id == row.id) and
            (not value.technique or value.technique == row.technique) and
            (not value.mark or value.mark == row.mark) and
            (not value.targetGuid or value.targetGuid == row.targetGuid or value.targetGuid == row.boundGuid)
        if not matches then requests[#requests+1] = row
        elseif stop_movement and row.boundGuid and row.visitId then
            state.stopping={targetGuid=row.boundGuid,visitId=row.visitId}
        end
    end
    state.requests = requests
end

local function on_event(event)
    if event.receipt and terminal[event.receipt.outcome] then state.settledAt = state.sequence end
    if event.receipt then
        local payload, outcome = event.receipt.payload or {}, event.receipt.outcome
        if state.stopping and payload.capability=="stop_movement" and payload.target_guid==state.stopping.targetGuid and
           (outcome=="failed" or outcome=="not_dispatched") then state.stopping.sent=nil end
        if outcome == "failed" or outcome == "not_dispatched" then
            for _, row in ipairs(state.requests) do
                if payload.capability == "cast_self" and payload.spell_id == row.stealthPending then row.stealthPending = nil end
                if payload.capability == row.technique and payload.target_guid == row.boundGuid and
                   payload.spell_id == row.controlPending then row.controlPending = nil end
            end
        end
    end
    local ground=state.rangedGround
    if ground and ground.operation and event.receipt then
        local receipt,p=event.receipt,event.receipt.payload or {}
        local op=ground.operation
        -- The Session harness validates receipt session/action reservation;
        -- generic spell payloads intentionally have no self/visit fields.
        if guid(receipt.action_id) and (not op.actionId or op.actionId==receipt.action_id) and
           (op.kind=="move_body" and p.capability=="move_body" and p.self_guid==ground.selfGuid and p.visit_id==ground.visitId and p.sequence_id==op.sequence or
            op.kind=="cast_spell" and p.capability=="cast_spell" and p.target_guid==op.target and p.spell_id==op.spell) then
            op.actionId=receipt.action_id
            if receipt.outcome=="unknown" then ground.uncertain=true
            elseif terminal[receipt.outcome] then
                ground.operation=nil
                ground.lastPhase,ground.lastDetail=p.phase,type(p.detail)=="string" and #p.detail>0 and p.detail or p.grpc_message or p.error
                if op.kind=="cast_spell" then
                    if receipt.outcome=="failed" or receipt.outcome=="not_dispatched" then ground.castRefused={key=ground.key,target=op.target,spell=op.spell} end
                elseif p.transport_written==true and p.phase=="body_input_written" then
                    ground.awaiting=op;ground.observedSequence=op.sequence
                else
                    -- A refused ray is not a reason to resend it every tick.
                    -- Falling/STOP-only outcomes are not horizontal progress.
                    ground.blocked[op.heading]=true
                    if p.phase=="body_settled_written" and p.transport_written==true then ground.observedSequence=op.sequence
                    elseif p.phase~="local_physics_refused" then ground.refused=ground.positionKey end
                end
            end
        end
    end
    local hold=state.rangedHold
    if hold and hold.operation and event.receipt then
        local receipt,p=event.receipt,event.receipt.payload or {}
        if p.capability=="movement_owner" and p.owner==hold.owner and p.mode==hold.operation.mode then
            if receipt.outcome=="confirmed" then
                local result=p.result or {}
                local body=hold.operation.mode=="release" and result or result.state
                if body and body.sessionId==hold.sessionId and body.selfGuid==hold.selfGuid then
                    if hold.operation.mode=="release" then hold.released=true;hold.token=nil
                    elseif type(result.token)=="string" and #result.token>0 and (body.movementOwner or {}).active==true and body.movementOwner.owner==hold.owner then hold.token=result.token
                    else hold.uncertain=true end
                else hold.uncertain=true end
                hold.operation=nil
            elseif receipt.outcome=="not_dispatched" or receipt.outcome=="failed" then
                hold.operation=nil;hold.refusal=p.error or receipt.outcome
            elseif receipt.outcome=="settled_unverified" or receipt.outcome=="unknown" then
                hold.operation=nil;hold.uncertain=true
            end
        end
    end
    if event.kind == "encounter_observation" then
        state.sequence=event.sequence
        local obs=event.value or {}
        if state.session and state.session~=obs.sessionId then state.pull=nil end
        local self=(obs.players or {})[obs.selfGuid]
        if self and self.aliveKnown==true and self.alive==false then state.pull=nil end
        state.session,state.visitId,state.selfGuid,state.mapId=obs.sessionId,obs.visitId,obs.selfGuid,obs.mapId
    end
    if event.kind ~= "command" or type(event.value) ~= "table" then return true end
    local value = event.value
    if value.task == "ranged_only" then
        if type(value.id)~="string" or #value.id==0 or #value.id>80 then return true end
        if value.cancel==true or value.enabled==false then
            for _,prior in ipairs(encounter_priors and encounter_priors.rules or {}) do
                if value.id==prior.id and state.session and guid(state.selfGuid) and guid(state.visitId) and state.mapId==prior.mapId then
                    state.rangedPrior={id=value.id,sessionId=state.session,selfGuid=state.selfGuid,mapId=state.mapId,visitId=state.visitId,cancelled=true}
                end
            end
            if state.rangedOnly and state.rangedOnly.id==value.id then state.rangedOnly=nil end
            if state.rangedPending and state.rangedPending.id==value.id then state.rangedPending=nil end
            state.rangedStatus={id=value.id,applied=true,active=false,phase="cancelled"}
        else
            local fields={"creatureEntry","mapId","minRangeYards","maxRangeYards","clearanceYards","durationMs"}
            for _,key in ipairs(fields) do
                local n=value[key]
                if type(n)~="number" or n~=n or n<=-math.huge or n>=math.huge then
                    state.rangedStatus={id=value.id,applied=false,active=false,phase="invalid_directive"};return true
                end
            end
            if not guid(value.visitId) or value.creatureEntry<1 or value.creatureEntry>4294967295 or value.creatureEntry%1~=0 or
               value.mapId<0 or value.mapId>4294967295 or value.mapId%1~=0 or value.durationMs<=0 or
               value.minRangeYards<=0 or value.clearanceYards<0 or value.maxRangeYards-value.minRangeYards<=2*value.clearanceYards then
                state.rangedStatus={id=value.id,applied=false,active=false,phase="invalid_directive"};return true
            end
            local next={id=value.id,visitId=value.visitId,origin="explicit"}
            for _,key in ipairs(fields) do next[key]=value[key] end
            local same=state.rangedOnly and state.rangedOnly.id==next.id and state.rangedOnly.visitId==next.visitId
            if same then for _,key in ipairs(fields) do if state.rangedOnly[key]~=next[key] then same=false end end end
            if not same then state.rangedPending=next end
        end
    elseif value.task == "engage" then
        if value.enabled==false or value.cancel==true then state.pull=nil
        elseif guid(value.targetGuid) then state.pull={targetGuid=value.targetGuid,visitId=state.visitId,pull=value.pull==true} end
    elseif value.task == "control" then
        if value.cancel == true or value.enabled == false then
            if value.auto == true then state.autoPolymorph = false end
            cancel(value,true); return true
        end
        if value.technique ~= "polymorph" and value.technique ~= "sap" then return true end
        local target = selector(value)
        if not target or value.spellId ~= nil and not spell_id(value.spellId) then return true end
        if value.auto == true then state.autoPolymorph = true end
        local row = {technique=value.technique, targetGuid=target.targetGuid, mark=target.mark, auto=target.auto, spellId=value.spellId,
                     behind=value.behind == nil and value.technique == "sap" or value.behind == true,
                     maintain=value.maintain ~= false}
        row.id = type(value.id) == "string" and #value.id > 0 and #value.id <= 80 and value.id or request_id(row)
        -- Retried legacy prepare/commit calls are the same goal. Preserve
        -- pending Stealth/CC custody across duplicates and policy reload.
        for _,prior in ipairs(state.requests) do
            if prior.id==row.id and prior.technique==row.technique and prior.targetGuid==row.targetGuid and
               prior.mark==row.mark and prior.auto==row.auto and prior.spellId==row.spellId and prior.behind==row.behind then
                prior.maintain=row.maintain
                return true
            end
        end
        cancel({id="automatic:polymorph"})
        cancel({id=row.id})
        if #state.requests < 8 then state.requests[#state.requests+1] = row end
    elseif value.task == "cancel_control" then
        if not value.targetGuid and not value.mark then state.autoPolymorph = false end
        cancel(value,true)
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
    if state.pull then
        local pull=state.pull
        pull.visitId=pull.visitId or obs.visitId
        local npc=(obs.targets or {})[pull.targetGuid]
        if pull.visitId~=obs.visitId or npc and npc.present==true and npc.aliveKnown==true and npc.alive==false then
            state.pull=nil
        elseif alive(npc) and npc.mapKnown==true and npc.mapId==obs.mapId then
            out.pullGuid=pull.targetGuid
            if pull.pull==true then out.explicitPullGuid=pull.targetGuid end
        end
    end
    out.focusGuid = out.pullGuid or resolve(obs, state.focus)
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
            attackable=target.attackable and (not target.healthKnown or (tonumber(target.health) or 0) > 0),
            attackVictimKnown=target.engaged == true and guid(attack.victimGuid) ~= nil,
            attackVictimGuid=attack.victimGuid,attackAgeMs=tonumber(attack.ageMs) or 0,
            healthPercent=target.healthKnown == true and (target.maxHealth or 0) > 0 and (tonumber(target.health) or 0)*100/target.maxHealth or nil,
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

local function control_action(obs, frame, focusGuid, planned_control, allow_action)
    ranged_control_proposal=nil
    refresh(obs)
    local stop=state.stopping
    if stop then
        local self=(obs.players or {})[obs.selfGuid]
        local moved=obs.targetMovement or {}
        if obs.visitId~=stop.visitId or (obs.navigation or {}).motionUnconfirmed~=true and
           (self and self.movementKnown==true and self.moving~=true or
            moved.targetGuid==stop.targetGuid and moved.visitId==stop.visitId and moved.phase=="stopped" and moved.ready==true) then state.stopping=nil
        elseif obs.sessionId==frame.session_id and #frame.pending==0 and
               state.settledAt~=frame.input.observationSequence and not stop.sent then
            stop.sent=true
            local request=action(obs,frame,"stop_movement",stop.targetGuid)
            request.rangeYards=0
            return request,{pending=true,phase="stopping_cancelled_control",targetGuid=stop.targetGuid}
        else return nil,{pending=true,phase="waiting_for_control_stop",targetGuid=stop.targetGuid} end
    end
    if planned_control then cancel({id="automatic:polymorph"}) end
    if not planned_control and #state.requests == 0 and state.autoPolymorph ~= false and highest_spell(obs,"polymorph") then
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
        elseif state.rangedOnly and target and target.entry==state.rangedOnly.creatureEntry and
            (request.technique=="sap" or obs.hostTargetRangeBandSupported~=true) then status.phase="waiting_for_ranged_scope"
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
                        local cast=action(obs,frame,"cast_self",obs.selfGuid,stealth.id)
                        if state.rangedOnly then ranged_control_proposal={request=request,key="stealthPending",prior=request.stealthPending,action=cast} end
                        request.stealthPending = stealth.id
                        return cast,status
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
                local options={rangeYards=range,requireLos=true,behind=request.behind == true}
                if state.rangedOnly and ranged_options then
                    if target.entry==state.rangedOnly.creatureEntry then options.rangeYards=range-state.rangedOnly.clearanceYards end
                    options=ranged_options(obs,state.rangedOnly,request.boundGuid,options)
                    if not options then status.phase="range_band_unavailable";return nil,status end
                end
                local moved = movement.decide(obs,frame.input.observationSequence,request.boundGuid,options)
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
                local cast=action(obs,frame,request.technique,request.boundGuid,spell.id)
                if allow_action and not allow_action(cast) then status.phase="waiting_for_task_area";return nil,status end
                if state.rangedOnly then ranged_control_proposal={request=request,key="controlPending",prior=request.controlPending,action=cast} end
                request.controlPending = spell.id
                return cast,status
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
    if saved.data.stopping~=nil and (type(saved.data.stopping)~="table" or
       not guid(saved.data.stopping.targetGuid) or not guid(saved.data.stopping.visitId)) then return false end
    for _,key in ipairs({"rangedOnly","rangedPending"}) do
        local row=saved.data[key]
        if row~=nil then
            if type(row)~="table" or type(row.id)~="string" or #row.id==0 or not guid(row.visitId) then return false end
            for _,field in ipairs({"creatureEntry","mapId","minRangeYards","maxRangeYards","clearanceYards","durationMs"}) do
                local n=row[field];if type(n)~="number" or n~=n or n<=-math.huge or n>=math.huge then return false end
            end
            if row.creatureEntry<1 or row.creatureEntry>4294967295 or row.creatureEntry%1~=0 or row.mapId<0 or row.mapId>4294967295 or row.mapId%1~=0 or row.minRangeYards<=0 or row.clearanceYards<0 or row.maxRangeYards-row.minRangeYards<=2*row.clearanceYards or row.durationMs<=0 then return false end
            if key=="rangedOnly" and (type(row.sessionId)~="string" or not guid(row.selfGuid) or type(row.expiresAt)~="number" or type(row.expiresUnixMs)~="number") then return false end
        end
    end
    local prior=saved.data.rangedPrior
    if prior~=nil and (type(prior)~="table" or type(prior.id)~="string" or type(prior.sessionId)~="string" or
       not guid(prior.selfGuid) or not guid(prior.visitId) or type(prior.mapId)~="number" or prior.mapId<0 or prior.mapId%1~=0 or
       prior.cancelled~=nil and type(prior.cancelled)~="boolean") then return false end
    local automatic=saved.data.rangedAutomatic
    if automatic~=nil and (type(automatic)~="table" or type(automatic.id)~="string" or type(automatic.sessionId)~="string" or
       not guid(automatic.selfGuid) or not guid(automatic.visitId) or type(automatic.sequence)~="string" or
       type(automatic.mapId)~="number" or automatic.mapId<0 or automatic.mapId>4294967295 or automatic.mapId%1~=0 or
       type(automatic.observedAt)~="number" or automatic.observedAt~=automatic.observedAt or math.abs(automatic.observedAt)==math.huge or
       type(automatic.observedUnixMs)~="number" or automatic.observedUnixMs~=automatic.observedUnixMs or math.abs(automatic.observedUnixMs)==math.huge) then return false end
    local ground=saved.data.rangedGround
    if ground~=nil and (type(ground)~="table" or type(ground.sessionId)~="string" or not guid(ground.selfGuid) or not guid(ground.visitId) or type(ground.key)~="string" or type(ground.blocked)~="table" or ground.operation~=nil and type(ground.operation)~="table" or ground.awaiting~=nil and type(ground.awaiting)~="table") then return false end
    if ground then
        if type(ground.id)~="string" or ground.uncertain~=nil and type(ground.uncertain)~="boolean" or ground.observedSequence~=nil and not guid(ground.observedSequence) then return false end
        local refused=ground.castRefused
        if refused~=nil and type(refused)~="string" and (type(refused)~="table" or type(refused.key)~="string" or not guid(refused.target) or not spell_id(refused.spell)) then return false end
        for k,v in pairs(ground.blocked) do if type(k)~="string" or type(v)~="boolean" then return false end end
        for _,op in pairs({operation=ground.operation,awaiting=ground.awaiting}) do
            if type(op)~="table" or not guid(op.sequence) or op.actionId~=nil and not guid(op.actionId) or (op.kind~="move_body" and op.kind~="cast_spell") then return false end
            if op.kind=="move_body" then
                if type(op.heading)~="string" then return false end
                for _,k in ipairs({"x","y","z"}) do local n=op[k];if type(n)~="number" or n~=n or n<=-math.huge or n>=math.huge then return false end end
            elseif not guid(op.target) or not spell_id(op.spell) then return false end
        end
    end
    local hold=saved.data.rangedHold
    if hold~=nil and (type(hold)~="table" or type(hold.owner)~="string" or type(hold.sessionId)~="string" or not guid(hold.selfGuid) or type(hold.untilUnixMs)~="number" or hold.token~=nil and type(hold.token)~="string" or hold.visitId~=nil and not guid(hold.visitId) or hold.mapId~=nil and (type(hold.mapId)~="number" or hold.mapId<0 or hold.mapId>4294967295 or hold.mapId%1~=0) or hold.followScope~=nil and (type(hold.followScope)~="string" or #hold.followScope==0 or #hold.followScope>512)) then return false end
    state = saved.data
    return true
end

local function ranged_stance(obs,frame)
    local now=frame.clock and frame.clock.monotonicMs
    local unix=frame.clock and frame.clock.unixMs
    if obs.sessionId~=frame.session_id or frame.input.observationSequence~=state.sequence then
        return nil,{applied=false,active=false,phase="current_scope_unavailable"}
    end
    if type(now)~="number" or type(unix)~="number" then
        local held=state.rangedOnly
        if held and (held.sessionId~=obs.sessionId or held.selfGuid~=obs.selfGuid or held.mapId~=obs.mapId or held.visitId~=obs.visitId) then held=nil end
        return held,{id=held and held.id,applied=held~=nil,active=held~=nil,phase="expiry_clock_unavailable"}
    end
    local pending=state.rangedPending
    if pending then
        state.rangedPending=nil
        if pending.mapId~=obs.mapId or pending.visitId~=obs.visitId then
            state.rangedStatus={id=pending.id,applied=false,active=false,phase="scope_mismatch",retainedActiveId=state.rangedOnly and state.rangedOnly.id}
        else
            pending.sessionId,pending.selfGuid,pending.expiresAt,pending.expiresUnixMs=obs.sessionId,obs.selfGuid,now+pending.durationMs,unix+pending.durationMs
            state.rangedOnly=pending;state.rangedStatus=nil
        end
    end
    -- Explicit orders (including legacy rows whose origin is ambiguous) remain
    -- finite overlays. Never rewrite their tuning or extend their deadline.
    local row=state.rangedOnly
    if row and (row.sessionId~=obs.sessionId or row.selfGuid~=obs.selfGuid or row.mapId~=obs.mapId or row.visitId~=obs.visitId or now>=row.expiresAt or unix>=row.expiresUnixMs) then
        state.rangedStatus={id=row.id,applied=true,active=false,phase=(now>=row.expiresAt or unix>=row.expiresUnixMs) and "expired" or "scope_ended"}
        state.rangedOnly=nil;row=nil
    end
    if row then
        return row,{id=row.id,origin=row.origin or "legacy_finite_overlay",applied=true,active=true,phase="applied",creatureEntry=row.creatureEntry,mapId=row.mapId,visitId=row.visitId,
            minRangeYards=row.minRangeYards,maxRangeYards=row.maxRangeYards,clearanceYards=row.clearanceYards,remainingMs=math.max(0,row.expiresUnixMs-unix),rejected=state.rangedStatus}
    end
    local prior=encounter_priors and encounter_priors.rule(obs)
    if not prior then return nil,state.rangedStatus or {applied=false,active=false,phase="not_requested"} end
    local seen=state.rangedPrior
    local same=seen and seen.id==prior.id and seen.sessionId==obs.sessionId and seen.selfGuid==obs.selfGuid and seen.mapId==obs.mapId and seen.visitId==obs.visitId
    -- Retain a visible legacy cancellation as well as the new explicit marker.
    local cancelled=same and (seen.cancelled==true or seen.cancelled==nil and state.rangedStatus and state.rangedStatus.id==prior.id and state.rangedStatus.phase=="cancelled")
    if cancelled then return nil,{id=prior.id,origin="learned_prior",applied=true,active=false,phase="cancelled"} end
    local capture=state.rangedAutomatic
    if not capture or capture.id~=prior.id or capture.sessionId~=obs.sessionId or capture.selfGuid~=obs.selfGuid or capture.mapId~=obs.mapId or capture.visitId~=obs.visitId or capture.sequence~=tostring(state.sequence) then
        capture={id=prior.id,sessionId=obs.sessionId,selfGuid=obs.selfGuid,mapId=obs.mapId,visitId=obs.visitId,
            sequence=tostring(state.sequence),observedAt=now,observedUnixMs=unix}
        state.rangedAutomatic=capture
    end
    -- The learned default stands on matching observation ingress. Its duration
    -- bounds each proposed native lease, not the lifetime of learned knowledge.
    -- Committed/UNKNOWN ownership keeps its own horizon in rangedHold.
    row={id=prior.id,origin="learned_prior",sessionId=obs.sessionId,selfGuid=obs.selfGuid,mapId=obs.mapId,visitId=obs.visitId,
        creatureEntry=prior.creatureEntry,minRangeYards=prior.minRangeYards,maxRangeYards=prior.maxRangeYards,
        clearanceYards=prior.clearanceYards,durationMs=prior.durationMs,attackWitnessMaxAgeMs=prior.attackWitnessMaxAgeMs,
        expiresAt=capture.observedAt+prior.durationMs,expiresUnixMs=capture.observedUnixMs+prior.durationMs}
    if now>=row.expiresAt or unix>=row.expiresUnixMs then
        return nil,{id=row.id,origin="learned_prior",applied=true,active=false,phase="awaiting_fresh_prior_observation"}
    end
    return row,{id=row.id,origin="learned_prior",standing=true,applied=true,active=true,phase="applied",creatureEntry=row.creatureEntry,mapId=row.mapId,visitId=row.visitId,
        minRangeYards=row.minRangeYards,maxRangeYards=row.maxRangeYards,clearanceYards=row.clearanceYards,
        leaseBudgetMs=prior.durationMs,leaseProposalUntilUnixMs=row.expiresUnixMs}
end
local function ranged_targets(obs,row)
    local ids,nearest,nearest_distance={},nil,nil
    local self=(obs.players or {})[obs.selfGuid]
    local complete=true
    for id,target in pairs(obs.targets or {}) do
        if target.entry==row.creatureEntry and target.present==true and target.alive~=false then
            if target.aliveKnown~=true or target.positionKnown~=true or target.mapKnown~=true or target.mapId~=obs.mapId or not self or self.positionKnown~=true then complete=false
            else
                ids[#ids+1]=id
                local dx,dy,dz=(self.x or 0)-(target.x or 0),(self.y or 0)-(target.y or 0),(self.z or 0)-(target.z or 0)
                local d=math.sqrt(dx*dx+dy*dy+dz*dz)
                if not nearest_distance or d<nearest_distance then nearest,nearest_distance=id,d end
            end
        end
    end
    for i=2,#ids do
        local value,j=ids[i],i-1
        while j>0 and ids[j]>value do ids[j+1]=ids[j];j=j-1 end
        ids[j+1]=value
    end
    return ids,nearest,nearest_distance,complete
end
ranged_options=function(obs,row,target,options)
    local ids,_,_,complete=ranged_targets(obs,row)
    if not complete then return end
    local copy={}
    for key,value in pairs(options or {}) do copy[key]=value end
    if #ids>0 then copy.clearanceTargetGuids=ids;copy.clearanceYards=row.minRangeYards+row.clearanceYards end
    if (obs.targets or {})[target] and obs.targets[target].entry==row.creatureEntry then
        copy.minRangeYards=row.minRangeYards+row.clearanceYards
        copy.rangeYards=math.min(copy.rangeYards or row.maxRangeYards,row.maxRangeYards-row.clearanceYards)
        copy.behind=false
        if copy.rangeYards<=copy.minRangeYards then return end
    end
    return copy
end
local function ranged_friendly_support(obs,action)
    if not action or (action.capability~="heal" and action.capability~="cleanse" and action.capability~="buff") then return false end
    local target=guid(action.targetGuid)
    local self=(obs.players or {})[obs.selfGuid]
    local patient=target and (obs.players or {})[target]
    local spell=(obs.spells or {})[tostring(action.spellId)]
    if not self or not patient or not spell or spell.metadataKnown~=true or spell.known~=true or
       type(spell.maxRange)~="number" or spell.maxRange<=0 then return false end
    local member=false
    for _,row in ipairs(((obs.party or {}).group or {}).members or {}) do
        if row.guid==target and row.online==true then member=true end
    end
    if not member then return false end
    for _,unit in ipairs({self,patient}) do
        if unit.present~=true or unit.aliveKnown~=true or unit.alive~=true or unit.mapKnown~=true or unit.mapId~=obs.mapId or
           unit.positionKnown~=true or unit.positionInferred==true or unit.positionEstimated==true then return false end
    end
    local dx,dy,dz=(self.x or 0)-(patient.x or 0),(self.y or 0)-(patient.y or 0),(self.z or 0)-(patient.z or 0)
    local d2=dx*dx+dy*dy+dz*dz
    return d2>=(spell.minRange or 0)^2 and d2<=spell.maxRange^2
end
-- Goal identity only, never a position witness or a movement receipt.
local function native_follow_scope(obs)
    local nav=obs.navigation or {}
    if nav.following~=true or type(obs.sessionId)~="string" or not guid(obs.selfGuid) or not guid(obs.visitId) or
       type(obs.mapId)~="number" or obs.mapId<0 or obs.mapId>4294967295 or obs.mapId%1~=0 or
       not guid(nav.ownerGuid) or not guid(nav.followRevision) then return end
    return tostring(#obs.sessionId)..":"..obs.sessionId..":"..obs.selfGuid..":"..obs.visitId..":"..
        tostring(obs.mapId)..":"..nav.ownerGuid..":"..nav.followRevision
end
local function ranged_hold(obs,frame,out,row,status,wanted,stopTarget)
    local hold=state.rangedHold
    local owner=(obs.navigation or {}).movementOwner
    local now=frame.clock and frame.clock.unixMs
    if not hold and not wanted then return false end
    if #frame.pending~=0 then status.phase="waiting_for_native_custody";out.action=nil;return true end
    if not hold then
        hold={owner="ranged:"..row.id,sessionId=obs.sessionId,selfGuid=obs.selfGuid,visitId=obs.visitId,mapId=obs.mapId,untilUnixMs=row.expiresUnixMs}
        -- Record only a new protective lease that interrupted an unowned
        -- native follow goal; never infer this from an existing owner label.
        if row.origin=="learned_prior" and type(owner)=="table" and owner.active~=true then
            hold.followScope=native_follow_scope(obs)
        end
        state.rangedHold=hold
    end
    if hold.sessionId~=obs.sessionId or hold.selfGuid~=obs.selfGuid then status.phase="movement_hold_other_body";out.action=nil;return true end
    if type(owner)~="table" or type(now)~="number" then status.phase="movement_owner_unavailable";out.action=nil;return true end
    if hold.released then state.rangedHold=nil;return false end
    if hold.uncertain then
        -- Unknown ownership is not cleared by an early inactive sample. The
        -- entire caller-selected finite lease horizon must also have ended.
        if owner.active==true or now<hold.untilUnixMs then status.phase="movement_hold_unknown";out.action=nil;return true end
        hold.uncertain=nil;hold.token=nil
    end
    if hold.operation then status.phase="waiting_for_movement_hold_receipt";out.action=nil;return true end
    if hold.token and (owner.active~=true or owner.owner~=hold.owner) then hold.token=nil end
    if wanted and hold.owner~="ranged:"..row.id then wanted=false end
    if not wanted then
        if hold.token and owner.active==true and owner.owner==hold.owner then
            local a={capability="movement_owner",sessionId=obs.sessionId,selfGuid=obs.selfGuid,owner=hold.owner,token=hold.token,mode="release"}
            out.action=a;ranged_hold_proposal={action=a,hold=hold};status.phase="releasing_movement_hold";return true
        end
        if owner.active==true and owner.owner==hold.owner then status.phase="waiting_for_movement_hold_expiry";out.action=nil;return true end
        state.rangedHold=nil;return false
    end
    if owner.active==true and owner.owner~=hold.owner then status.phase="movement_owned_elsewhere";out.action=nil;return true end
    if not hold.token then
        local self=(obs.players or {})[obs.selfGuid]
        if not self or self.aliveKnown~=true or self.alive~=true then
            status.phase="movement_body_unavailable";out.action=nil;return true
        end
        if self.movementKnown~=true or self.moving==true then
            local target=guid((obs.targetMovement or {}).targetGuid) or stopTarget
            out.action=target and movement and movement.stop(obs,frame.input.observationSequence,target) or nil
            status.phase="stopping_prior_movement";return true
        end
        if owner.active==true then status.phase="waiting_for_movement_hold_expiry";out.action=nil;return true end
        local remaining=math.floor(math.min(4294967295,row.expiresUnixMs-now))
        if remaining<1 then status.phase="movement_hold_deadline";out.action=nil;return true end
        local a={capability="movement_owner",sessionId=obs.sessionId,selfGuid=obs.selfGuid,owner=hold.owner,mode="acquire",leaseMs=remaining}
        out.action=a;ranged_hold_proposal={action=a,hold=hold,untilUnixMs=row.expiresUnixMs};status.phase="acquiring_movement_hold";return true
    end
    local self=(obs.players or {})[obs.selfGuid]
    local stopped=self and self.movementKnown==true and self.moving~=true and (obs.navigation or {}).motionUnconfirmed~=true
    if not stopped and obs.hostTargetRangeBandSupported~=true then
        local target=guid((obs.targetMovement or {}).targetGuid) or stopTarget
        out.action=target and movement and movement.stop(obs,frame.input.observationSequence,target) or nil
        status.phase="stopping_prior_movement";return true
    end
    status.movementHoldKnown=owner.active==true and owner.owner==hold.owner
    status.stationaryObserved=stopped==true
    return false
end
-- Older native clients already provide physics-checked straight body inputs.
-- Their observed speed is validated natively in [0.1,14] yards/s. Check the
-- ENTIRE worst-case ray here; the native floor/collision check may refuse it.
-- This is observed geometry, not a claim about future hazards or server arrival.
local function ranged_ground(obs,frame,sequence,out,row,status,ids,nearest,choose_spell)
    out.action=nil
    status.positionReady=false
    status.positioningMode="legacy_ground_input"
    local nav=obs.navigation or {}
    local self=(obs.players or {})[obs.selfGuid]
    local from=movement and movement.current_pose and movement.current_pose(self)
    if nav.bodyMovementSupported~=true then status.phase="body_movement_unsupported";return end
    if not from or self.positionInferred==true or self.mapKnown~=true or self.mapId~=obs.mapId or nav.motionUnconfirmed==true or status.stationaryObserved~=true then
        status.phase="ground_pose_unavailable";return
    end
    local hazards,key={},row.id..":"..tostring(row.minRangeYards)..":"..tostring(row.maxRangeYards)..":"..tostring(row.clearanceYards)..":"..tostring(from.x)..":"..tostring(from.y)..":"..tostring(from.z)
    local positionKey=key
    for _,id in ipairs(ids) do
        local target=obs.targets[id]
        local point=movement.current_pose(target)
        if not point or target.positionInferred==true or target.positionEstimated==true then
            status.phase="hazard_pose_unavailable";return
        end
        hazards[#hazards+1]=point
        key=key..":"..id..":"..tostring(point.x)..":"..tostring(point.y)..":"..tostring(point.z)
    end
    local ground=state.rangedGround
    if ground and (ground.sessionId~=obs.sessionId or ground.selfGuid~=obs.selfGuid or ground.visitId~=obs.visitId or ground.id~=row.id) and (ground.operation or ground.uncertain) then
        status.phase="ground_input_prior_scope_unsettled";return
    end
    if not ground or ground.sessionId~=obs.sessionId or ground.selfGuid~=obs.selfGuid or ground.visitId~=obs.visitId or ground.id~=row.id then
        ground={sessionId=obs.sessionId,selfGuid=obs.selfGuid,visitId=obs.visitId,id=row.id,key=key,positionKey=positionKey,blocked={}}
        state.rangedGround=ground
    end
    status.lastInputPhase,status.lastInputDetail=ground.lastPhase,ground.lastDetail
    if ground.uncertain then status.phase="ground_input_unknown";return end
    if ground.operation then status.phase="waiting_for_ground_input_receipt";return end
    local function matching_body_pose(body,expected)
        if not body or body.sequenceId~=expected or body.visitId~=obs.visitId or body.transportWritten~=true or
           (body.phase~="body_input_written" and body.phase~="body_settled_written") then return false end
        if self.positionEstimated~=true then return true end
        return body.positionEstimated==true and
            math.abs(from.x-(body.x or 0))<=0.0001 and math.abs(from.y-(body.y or 0))<=0.0001 and math.abs(from.z-(body.z or 0))<=0.0001
    end
    if ground.awaiting then
        local op,body=ground.awaiting,nav.bodyMovement
        if not matching_body_pose(body,op.sequence) then
            status.phase="waiting_for_ground_input_observation";return
        end
        ground.awaiting=nil
        if (from.x-op.x)^2+(from.y-op.y)^2+(from.z-op.z)^2<0.00000001 then ground.blocked[op.heading]=true end
        status.poseSource=self.positionEstimated==true and "client_written_pose" or "observed_pose"
    elseif self.positionEstimated==true then
        -- Do not borrow an unrelated estimated pose as a starting point.
        if not ground.observedSequence or not matching_body_pose(nav.bodyMovement,ground.observedSequence) then status.phase="uncorrelated_estimated_pose";return end
    end
    -- Collision/floor refusal is tied to OWN origin and direction. A moving
    -- enemy does not make an unchanged refused physical ray worth retrying.
    if not ground.positionKey then ground.positionKey=positionKey end
    if ground.positionKey~=positionKey then ground.positionKey,ground.blocked=positionKey,{} end
    ground.key=key
    if ground.refused==positionKey then status.phase="ground_input_refused_at_observed_pose";return end
    local focus=out.focusGuid and obs.targets[out.focusGuid]
    local target=focus and focus.entry==row.creatureEntry and focus.present==true and focus.protected~=true and obs.targetProtectionOverflow~=true and focus.aliveKnown==true and focus.alive==true and focus or nil
    local targetId=target and out.focusGuid
    local point=target and movement.current_pose(target)
    local spellId=point and choose_spell and choose_spell(obs,target,{require_ready=false,require_metadata=true})
    local spell=spellId and (obs.spells or {})[tostring(spellId)]
    if spell and (spell.metadataKnown~=true or spell.weaponStrike==true or spell.capability~="offense" or type(spell.maxRange)~="number" or spell.maxRange<=row.minRangeYards+2*row.clearanceYards) then spell=nil end
    local lower=row.minRangeYards+row.clearanceYards
    local upper=spell and math.min(row.maxRangeYards-row.clearanceYards,spell.maxRange-row.clearanceYards)
    local unsafe=false
    for _,p in ipairs(hazards) do if (p.x-from.x)^2+(p.y-from.y)^2<lower*lower then unsafe=true end end
    local dx,dy,distance
    if point then dx,dy=point.x-from.x,point.y-from.y;distance=math.sqrt(dx*dx+dy*dy+(point.z-from.z)^2) end
    if not unsafe and not spell then status.phase="supported_ranged_option_unavailable";return end
    local facing=point and dx*math.cos(from.o)+dy*math.sin(from.o)
    if not unsafe and distance and distance>=lower and distance<=upper and facing and facing>=math.sqrt(dx*dx+dy*dy)*0.98 then
        local ready=choose_spell(obs,target,{require_metadata=true})
        if ready then
            out.action={capability="cast_spell",sessionId=obs.sessionId,selfGuid=obs.selfGuid,visitId=obs.visitId,observationSequence=sequence,
                spellId=ready,targets={mask=2,objectGuid=targetId}}
            status.phase="stationary_targeted_ranged_cast"
            local refused=ground.castRefused
            if refused==key or type(refused)=="table" and refused.key==key and refused.target==targetId and refused.spell==ready then out.action=nil;status.phase="ranged_cast_refused_at_observed_pose"
            else ranged_ground_proposal={action=out.action,ground=ground,operation={kind="cast_spell",sequence=sequence,target=targetId,spell=ready}} end
        else status.phase="waiting_for_ranged_spell" end
        return
    end
    -- A non-fighting stance can withdraw, but cannot choose a new pull target.
    if not unsafe and not point then status.phase="holding_ranged_only";return end
    local length=unsafe and 3 or math.min(3,math.abs(distance-(lower+upper)*0.5))
    local facingOnly=not unsafe and distance>=lower and distance<=upper
    if facingOnly then length=0.014 end
    local duration=math.max(1,math.min(500,math.floor(length/14*1000)))
    local hold=state.rangedHold
    local now=frame.clock and frame.clock.unixMs
    local remaining=hold and type(hold.untilUnixMs)=="number" and type(now)=="number" and
        math.min(hold.untilUnixMs,row.expiresUnixMs)-now
    if type(remaining)~="number" or duration>=remaining then status.phase="ground_input_deadline";return end
    length=14*duration/1000
    local best,bestScore
    for i=0,31 do
        local angle=i*0.196349540849362
        local vx,vy=math.cos(angle),math.sin(angle)
        local ex,ey=from.x+vx*length,from.y+vy*length
        local allowed=ground.blocked[tostring(i)]~=true
        local score=0
        for _,p in ipairs(hazards) do
            local hx,hy=p.x-from.x,p.y-from.y
            local initial=hx*hx+hy*hy
            local projection=math.max(0,math.min(length,hx*vx+hy*vy))
            local closest=(hx-vx*projection)^2+(hy-vy*projection)^2
            if initial<lower*lower then
                -- Inside: never take a first step towards ANY hazard.
                if initial>0 and hx*vx+hy*vy>=-math.min(0.001,math.sqrt(initial)*0.01) then allowed=false end
                score=score+((ex-p.x)^2+(ey-p.y)^2-initial)
            elseif initial<(lower+0.01)^2 then
                -- Relative bearings are encoded as float32 by native. Keep a
                -- strict outward component at the boundary, and a small
                -- numeric margin for rays passing a different hazard.
                if hx*vx+hy*vy>=-0.001 then allowed=false end
            elseif closest<(lower+0.01)^2 then allowed=false end
        end
        if not unsafe then
            if facingOnly then score=dx*vx+dy*vy
            else
                local after=math.sqrt((ex-point.x)^2+(ey-point.y)^2+(from.z-point.z)^2)
                score=math.abs(distance-(lower+upper)*0.5)-math.abs(after-(lower+upper)*0.5)
            end
        end
        if allowed and score>0 and (not bestScore or score>bestScore) then best,bestScore=i,score end
    end
    if not best then status.phase="no_clear_ground_input";return end
    local bearing=best*0.196349540849362-from.o
    while bearing>3.141592653589793 do bearing=bearing-6.283185307179586 end
    while bearing< -3.141592653589793 do bearing=bearing+6.283185307179586 end
    local a={capability="move_body",sessionId=obs.sessionId,selfGuid=obs.selfGuid,visitId=obs.visitId,observationSequence=sequence,
        sequenceId=sequence,bearingRadians=bearing,durationMs=duration,jump=false}
    out.action=a
    ranged_ground_proposal={action=a,ground=ground,operation={kind="move_body",sequence=sequence,heading=tostring(best),x=from.x,y=from.y,z=from.z}}
    status.phase=unsafe and "withdrawing_ground_input" or facingOnly and "facing_ground_input" or "approaching_ground_input"
end

-- Only the automatic default yields to explicit travel. Unknown geometry or an
-- engaged matching hazard cannot establish the harmless, distant exception.
local function ranged_travel_safe(obs,row,in_combat,attack_age_limit)
    if not row or row.origin~="learned_prior" or in_combat then return false end
    local self=(obs.players or {})[obs.selfGuid]
    if not self or self.combatKnown~=true or self.inCombat==true or self.mapKnown~=true or
       self.mapId~=obs.mapId or self.positionKnown~=true or self.positionInferred==true then return false end
    for _,key in ipairs({"x","y","z"}) do
        local value=self[key] or 0
        if type(value)~="number" or value~=value or value==math.huge or value==-math.huge then return false end
    end
    if self.positionEstimated~=true and self.positionAgeKnown==true and type(attack_age_limit)=="number" then
        local age=tonumber(self.positionAgeMs or "0")
        if not age or age<0 or age>attack_age_limit then return false end
    end
    local ids,nearest,distance,complete=ranged_targets(obs,row)
    if not complete then return false end
    if #ids>0 and (not distance or distance<=row.minRangeYards+row.clearanceYards) then return false end
    for _,id in ipairs(ids) do
        local npc=(obs.targets or {})[id]
        if npc.positionInferred==true or npc.positionEstimated==true or npc.engaged==true or
           (obs.engagement or {})[id] then return false end
        for _,key in ipairs({"x","y","z"}) do
            local value=npc[key] or 0
            if type(value)~="number" or value~=value or value==math.huge or value==-math.huge then return false end
        end
        local attack=npc.attackObservation
        if attack and attack.victimKnown==true and guid(attack.victimGuid) then
            local age=tonumber(attack.ageMs or "0")
            local victim=(obs.players or {})[attack.victimGuid]
            local fighting=victim and victim.present==true and victim.mapKnown==true and victim.mapId==obs.mapId and
                victim.combatKnown==true and victim.inCombat==true
            -- Only native-follow handoff supplies an age limit. Rally keeps
            -- its existing conservative witness behavior.
            if fighting or type(attack_age_limit)~="number" or attack_age_limit<0 or
               not age or age-age~=0 or not (age>attack_age_limit) then return false end
        end
    end
    return true
end

local function ranged_filter(obs,frame,sequence,out,row,status,choose_spell,task_view)
    out.rangedStance=status
    if not row then
        if #frame.pending==0 then ranged_hold(obs,frame,out,nil,status,false) end
        return
    end
    local task=task_view and task_view.task
    if row.origin=="learned_prior" and task and task.kind=="rally_to" then
        if task_view.rangedTravelSafe then
            status.phase="yielding_to_explicit_rally"
            if #frame.pending==0 then ranged_hold(obs,frame,out,row,status,false) end
            return
        end
        -- Let the rally settle its own movement/token before the default tries
        -- to acquire. These exact proposals still commit through task custody.
        if task.pausedForEncounter and task_view.action==out.action and out.action and
           (out.action.capability=="stop_movement" or out.action.capability=="movement_owner") then return end
    end
    local ids,nearest,distance,complete=ranged_targets(obs,row)
    status.matchedTargets=#ids
    if #ids==0 and complete then if #frame.pending==0 then ranged_hold(obs,frame,out,row,status,false) end;return end
    local self=(obs.players or {})[obs.selfGuid]
    local prior=row.origin=="learned_prior" and (obs.navigation or {}).following==true and
        encounter_priors and encounter_priors.rule(obs)
    local nav,hold,ground=obs.navigation or {},state.rangedHold,state.rangedGround
    local resumed_follow=hold and hold.followScope~=nil and hold.followScope==native_follow_scope(obs)
    local follow_safe=prior and self and (self.positionEstimated~=true or resumed_follow and self.movementKnown==true) and nav.motionUnconfirmed~=true and
        ranged_travel_safe(obs,row,false,prior.attackWitnessMaxAgeMs)
    -- Native walking publishes its actually written own pose as estimated.
    -- Continuing that already active controller is not a new handoff: never
    -- release an active lease, borrow a token, or settle uncertain work here.
    local follow_continuing=prior and self and self.positionEstimated==true and self.movementKnown==true and
        type(nav.movementOwner)=="table" and nav.movementOwner.active~=true and nav.motionUnconfirmed~=true and
        #frame.pending==0 and not (hold and (hold.operation or hold.uncertain or
            hold.sessionId~=obs.sessionId or hold.selfGuid~=obs.selfGuid)) and
        not (ground and (ground.operation or ground.uncertain)) and
        ranged_travel_safe(obs,row,false,prior.attackWitnessMaxAgeMs)
    if not follow_safe and not follow_continuing then out.followFormation,out.portalCatchup=nil,nil end
    if not self or self.aliveKnown~=true or self.alive~=true then return end
    local action=out.action
    local cleanup=action and (action.capability=="fear_break" or action.capability=="cancel_spell" or action.capability=="loot_close" or action.capability=="release_interaction" or action.capability=="melee_stop")
    if cleanup then return end
    if #frame.pending~=0 then status.phase="waiting_for_native_custody";return end
    local swinging=guid(obs.activeMeleeTargetGuid)
    local active=swinging and (obs.targets or {})[swinging]
    if swinging and (active and active.entry==row.creatureEntry or distance and distance<row.minRangeYards+row.clearanceYards) then
        out.action={capability="melee_stop",sessionId=obs.sessionId,selfGuid=obs.selfGuid,targetGuid=swinging,visitId=obs.visitId,observationSequence=sequence}
        status.phase="stopping_melee";return
    end
    -- Reconcile only the existing finite ownership uncertainty, not an input
    -- or ground UNKNOWN. This step performs cleanup only, never a replacement.
    if prior and #frame.pending==0 and hold and hold.uncertain and not hold.operation and
       hold.sessionId==obs.sessionId and hold.selfGuid==obs.selfGuid and
       hold.visitId==obs.visitId and hold.mapId==obs.mapId and
       type(nav.movementOwner)=="table" and nav.movementOwner.active~=true and
       nav.motionUnconfirmed~=true and type(frame.clock.unixMs)=="number" and
       frame.clock.unixMs>=hold.untilUnixMs and
       not (ground and (ground.operation or ground.uncertain)) then
        ranged_hold(obs,frame,out,row,status,false)
        out.action=nil;status.phase="finite_movement_hold_reconciled"
        return
    end
    -- A known stationary, owned lease can still permit the existing direct
    -- friendly support while unrelated ground custody remains unresolved.
    if row.origin=="learned_prior" and ground and (ground.operation or ground.uncertain) and
       hold and not hold.operation and not hold.uncertain and hold.token and
       hold.sessionId==obs.sessionId and hold.selfGuid==obs.selfGuid and
       type(nav.movementOwner)=="table" and nav.movementOwner.active==true and
       nav.movementOwner.owner==hold.owner and nav.motionUnconfirmed~=true and
       self.movementKnown==true and self.moving~=true and ranged_friendly_support(obs,action) then
        out.followFormation,out.portalCatchup=nil,nil
        status.phase="stationary_friendly_support";status.positionReady=false
        return
    end
    if prior and (nav.motionUnconfirmed==true or hold and (hold.operation or hold.uncertain) or
       ground and (ground.operation or ground.uncertain)) then
        out.action,out.followFormation,out.portalCatchup=nil,nil,nil
        status.phase="waiting_for_native_follow_custody"
        return
    end
    if row.origin=="learned_prior" and encounter_priors and encounter_priors.positioning and
       not encounter_priors.positioning(obs,row) then
        status.phase="passive_distant_prior";status.active=false;status.positionReady=false
        if nav.motionUnconfirmed==true or hold and (hold.operation or hold.uncertain or
           hold.sessionId~=obs.sessionId or hold.selfGuid~=obs.selfGuid or
           hold.visitId~=nil and hold.visitId~=obs.visitId or hold.mapId~=nil and hold.mapId~=obs.mapId) or
           ground and (ground.operation or ground.uncertain) then
            out.action,out.followFormation,out.portalCatchup=nil,nil,nil
            status.phase="waiting_for_passive_prior_custody";return
        end
        -- A tokenless rejected prior proposal owns no native movement. Do
        -- not let that bookkeeping starve the consumables policy's exact own
        -- lease or safe reacquisition after expiry. This retires no acquired,
        -- entered or unknown custody and performs no release; only the admitted stationary drink
        -- can pass. Near hazards/fresh attacks never enter this passive branch.
        local resting=out.consumable
        local owner=nav.movementOwner
        local drink=type(resting)=="table" and resting.hold==true and resting.action==action and action and
           action.sessionId==obs.sessionId and action.selfGuid==obs.selfGuid and type(owner)=="table" and
           (resting.phase=="using_selected_item" and action.capability=="consume" and action.kind=="drink" and
            type(action.requestId)=="string" and owner.active==true and (not hold or owner.owner~=hold.owner) and
            owner.owner=="consumable:"..action.requestId or
            resting.phase=="acquiring_rest_position" and resting.selectedKind=="drink" and
            type(resting.requestId)=="string" and action.capability=="movement_owner" and action.mode=="acquire" and
            owner.active~=true and action.owner=="consumable:"..resting.requestId)
        if hold and not hold.token and not hold.operation and not hold.uncertain and
           self.combatKnown==true and self.inCombat~=true and
           self.movementKnown==true and self.moving~=true and nav.motionUnconfirmed~=true and drink then
            state.rangedHold=nil;hold=nil
        end
        -- Never turn an initial estimated pose into proof for releasing a
        -- previously held lease. A recorded interrupted native goal is distinct.
        if hold and not hold.released and self.positionEstimated==true and not resumed_follow then
            out.action=nil;status.phase="waiting_for_observed_prior_cleanup";return
        end
        if hold and type(nav.movementOwner)=="table" and nav.movementOwner.active==true and nav.movementOwner.owner~=hold.owner then
            out.action=nil;status.phase="movement_owned_elsewhere";return
        end
        ranged_hold(obs,frame,out,row,status,false)
        return
    end
    if follow_safe or follow_continuing then
        status.phase=follow_continuing and "continuing_native_follow_estimate" or
            self.positionEstimated==true and "resuming_native_follow_estimate" or "yielding_to_native_follow"
        if self.positionEstimated==true then status.poseSource="native_written_estimate";status.positionReady=false end
        ranged_hold(obs,frame,out,row,status,false)
        return
    end
    if ranged_hold(obs,frame,out,row,status,true,nearest) then return end
    if obs.hostTargetRangeBandSupported~=true then
        if action and (action.capability=="heal" or action.capability=="buff") and action.targetGuid==obs.selfGuid then return end
        if status.stationaryObserved==true and ranged_friendly_support(obs,action) then
            status.phase="stationary_friendly_support";status.positionReady=false;return
        end
        if not complete then out.action=nil;status.phase="clearance_facts_unavailable";return end
        ranged_ground(obs,frame,sequence,out,row,status,ids,nearest,choose_spell)
        if task and task.pausedForEncounter and status.phase=="withdrawing_ground_input" then
            task_view.rangedWithdrawal=out.action
        end
        return
    end
    if (obs.navigation or {}).motionUnconfirmed==true then out.action=nil;status.phase="waiting_for_native_custody";status.positionReady=false;return end
    if action and (action.capability=="heal" or action.capability=="buff") and action.targetGuid==obs.selfGuid then return end
    if not complete then out.action=nil;status.phase="clearance_facts_unavailable";return end
    if distance and distance<row.minRangeYards+row.clearanceYards then
        local options=ranged_options(obs,row,nearest,{rangeYards=row.maxRangeYards,requireLos=false})
        local result=options and movement and movement.decide(obs,sequence,nearest,options)
        out.action=result and result.action;status.phase=result and result.phase or "withdrawal_unavailable";status.positionReady=false
        if task and task.pausedForEncounter then task_view.rangedWithdrawal=out.action end
        return
    end
    if action and action.capability=="approach_target" then
        local options=ranged_options(obs,row,action.targetGuid,action)
        if not options then out.action=nil;status.phase="range_band_unavailable";return end
        local result=movement and movement.decide(obs,sequence,action.targetGuid,options)
        out.action=result and result.action;status.positionReady=result and result.ready==true;return
    end
    local target=action and (obs.targets or {})[action.targetGuid]
    if target and target.entry==row.creatureEntry then
        if action.capability=="melee_start" or action.capability=="sap" then out.action=nil;status.phase="holding_ranged_only";return end
        if action.capability=="offense" or action.capability=="cast_spell" or action.capability=="polymorph" then
            local spell=(obs.spells or {})[tostring(action.spellId)]
            if not spell or spell.metadataKnown~=true or spell.weaponStrike==true or type(spell.maxRange)~="number" or spell.maxRange<=row.minRangeYards+2*row.clearanceYards then
                out.action=nil;status.phase="supported_ranged_option_unavailable";return
            end
            local options=ranged_options(obs,row,action.targetGuid,{rangeYards=spell.maxRange-row.clearanceYards,requireLos=true})
            local result=options and movement and movement.decide(obs,sequence,action.targetGuid,options)
            status.positionReady=result and result.ready==true
            if not status.positionReady then out.action=result and result.action;status.phase=result and result.phase or "range_band_unavailable" end
        end
    end
end

-- Reuse this role's acknowledged writer and existing ground receipt ledger.
-- No lease is created/cleared/borrowed; only an otherwise idle admitted role
-- may request a small cloud offset through its same winner-only ground path.
local function cloud_point_step(obs,frame,sequence,out,row,status,points)
    local hold,ground=state.rangedHold,state.rangedGround
    local self=(obs.players or {})[obs.selfGuid]
    if not row or row.origin~="learned_prior" or not status or status.phase~="supported_ranged_option_unavailable" or
       out.action or #frame.pending~=0 or not self or self.positionEstimated==true or
       not hold or hold.owner~="ranged:"..row.id or hold.sessionId~=obs.sessionId or hold.selfGuid~=obs.selfGuid or
       hold.visitId~=obs.visitId or hold.mapId~=obs.mapId or hold.operation or hold.uncertain or hold.released or
       type(hold.token)~="string" or #hold.token==0 or
       ground and (ground.operation or ground.uncertain or ground.awaiting or ground.sessionId~=obs.sessionId or ground.selfGuid~=obs.selfGuid or ground.visitId~=obs.visitId or ground.id~=row.id) then return false end
    local from=movement and movement.current_pose(self)
    if not from then return false end
    if not ground then
        local key=row.id..":"..tostring(from.x)..":"..tostring(from.y)..":"..tostring(from.z)
        ground={sessionId=obs.sessionId,selfGuid=obs.selfGuid,visitId=obs.visitId,id=row.id,key=key,positionKey=key,blocked={}}
        state.rangedGround=ground
    end
    if ground.refused==ground.positionKey then return false end
    if not movement.adjust_cloud_points(frame,obs,sequence,out,points,row,hold) then return false end
    local a=out.action
    local heading="cloud:"..tostring(a.bearingRadians)
    if ground.blocked[heading] then out.action=nil;out.cloudAdjustment=nil;return false end
    ranged_ground_proposal={action=a,ground=ground,operation={kind="move_body",sequence=sequence,heading=heading,x=from.x,y=from.y,z=from.z}}
    status.phase="owned_cloud_point_reposition"
    return true
end

local function ranged_finish(selected)
    if ranged_ground_proposal and ranged_ground_proposal.action==selected then
        ranged_ground_proposal.ground.operation=ranged_ground_proposal.operation
    end
    ranged_ground_proposal=nil
    if ranged_hold_proposal and ranged_hold_proposal.action==selected then
        if selected.mode=="acquire" then ranged_hold_proposal.hold.untilUnixMs=ranged_hold_proposal.untilUnixMs end
        ranged_hold_proposal.hold.operation={mode=selected.mode,owner=selected.owner}
    end
    ranged_hold_proposal=nil
    local proposal=ranged_control_proposal
    if proposal and proposal.action~=selected then proposal.request[proposal.key]=proposal.prior end
    ranged_control_proposal=nil
end

return {on_event=on_event, export_state=export_state, import_state=import_state,
        queue_intent=queue_intent, control_action=control_action, ranged_stance=ranged_stance, ranged_options=ranged_options, ranged_travel_safe=ranged_travel_safe,ranged_filter=ranged_filter, ranged_finish=ranged_finish, cloud_point_step=cloud_point_step}
