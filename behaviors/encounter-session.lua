-- Compose this file after the lexical encounter_tactics module, whose source
-- is encounter-tactics.lua wrapped in a function. An optional encounter_healing
-- module follows the same composition rule. The bundle revision covers both.
-- The host enqueues current observations and calls decide with their sequence.
-- Action proposals bind the exact observation, actor, target and learned spell.
local tactics = assert(encounter_tactics)
local healing = encounter_healing
local state = {bindings={}, defeated={}, controls={}}
local terminal = {confirmed=true, failed=true, not_dispatched=true}
local icons = {star=1, circle=2, diamond=3, triangle=4, moon=5, square=6, x=7, skull=8}

local function guid(value)
    if type(value) ~= "string" or #value == 0 or #value > 20 or byte_at(value, 1) == 48 then return end
    for i=1,#value do
        local digit = byte_at(value, i)
        if digit < 48 or digit > 57 then return end
    end
    if #value < 20 or value <= "18446744073709551615" then return value end
end

local function alive(unit)
    return unit and unit.present == true and unit.aliveKnown == true and unit.alive == true
end

local function on_event(event)
    if healing and not healing.on_event(event) then return false end
    if event.kind == "encounter_observation" then
        state.observation, state.sequence = event.value, event.sequence
    elseif event.kind == "healing_observation" then
        state.healingSequence = event.sequence
    end
    if event.receipt and terminal[event.receipt.outcome] then
        -- Wait for an updated observation before proposing another action.
        state.settledAt = state.sequence
    end
    return true
end

local function export_state()
    return {version=1, data={encounter=state, healing=healing and healing.export_state()}}
end

local function import_state(checkpoint)
    if checkpoint.version ~= 1 or type(checkpoint.data) ~= "table" then return false end
    local saved = checkpoint.data.encounter
    if type(saved) ~= "table" or type(saved.bindings) ~= "table" or type(saved.defeated) ~= "table"
       or type(saved.controls) ~= "table" then return false end
    if saved.observation and (type(saved.observation) ~= "table" or
       saved.observation.sessionId ~= checkpoint.session_id or not guid(saved.sequence)) then return false end
    local heal = checkpoint.data.healing
    if healing and not heal then return false end
    if heal and (not healing or not healing.import_state({
        version=heal.version, data=heal.data, session_id=checkpoint.session_id,
        cursor=checkpoint.cursor, pending=checkpoint.pending
    })) then return false end
    state = saved
    return true
end

local function copy_target(target, engaged, protected)
    return {
        present=target.present, aliveKnown=target.aliveKnown, alive=target.alive,
        attackable=target.attackable, health=target.healthKnown and target.health or nil,
        killSeconds=target.killSeconds, incomingDPS=target.incomingDPS, inCombat=engaged,
        engaged=engaged, protected=protected,
    }
end

local function same_map(unit, map)
    return unit and unit.mapKnown == true and unit.mapId == map
end

local function usable_spell(obs, spell)
    local observed = type(spell) == "number" and spell > 0 and spell % 1 == 0 and
        (obs.spells or {})[tostring(spell)]
    return observed and observed.known == true and observed.ready == true
end

local function proposal(obs, kind, target, spell)
    return {capability=kind, sessionId=obs.sessionId, selfGuid=obs.selfGuid,
            targetGuid=target, spellId=spell, observationSequence=state.sequence}
end

local function known_runner(target)
    return target.unitFlagsKnown == true and type(target.unitFlags) == "number" and
           math.floor(target.unitFlags / 8388608) % 2 == 1 and target.healthKnown == true and
           type(target.health) == "number" and type(target.maxHealth) == "number" and
           target.health > 0 and target.health * 4 <= target.maxHealth
end

local function decide(frame)
    local out = {queue={}, protected={}, phase="waiting_for_encounter_observation"}
    local obs = state.observation
    if not obs or obs.sessionId ~= frame.session_id or not guid(obs.selfGuid) then return out end
    local plan, party, players = obs.plan or {}, obs.party or {}, obs.players or {}
    local group, feed = party.group or {}, party.raidTargets or {}
    if type(plan.id) ~= "string" or #plan.id == 0 or #plan.id > 80 or
       type(obs.visitId) ~= "string" or #obs.visitId == 0 or #obs.visitId > 80 or
       not group.known or not group.inGroup or not guid(group.groupGuid) or not guid(group.leaderGuid) or
       plan.groupGuid ~= group.groupGuid or plan.leaderGuid ~= group.leaderGuid then
        out.phase = "waiting_for_bound_group"; return out
    end
    if state.visitId ~= obs.visitId or state.planId ~= plan.id then
        state.visitId, state.planId, state.groupGuid, state.mapId = obs.visitId, plan.id, group.groupGuid, obs.mapId
        state.leaderGuid = group.leaderGuid
        state.bindings, state.defeated, state.controls = {}, {}, {}
        state.started, state.focusGuid, state.markRevision = false, nil, nil
    elseif state.groupGuid ~= group.groupGuid or state.mapId ~= obs.mapId or state.leaderGuid ~= group.leaderGuid then
        out.phase = "encounter_identity_changed"; return out
    end

    local members, targets, protected = {}, {}, {}
    for _, member in ipairs(group.members or {}) do members[member.guid] = member.online == true end
    local in_combat = false
    for target, npc in pairs(obs.targets or {}) do
        if guid(target) and same_map(npc, obs.mapId) then
            local attack = (obs.engagement or {})[target]
            local victim = attack and attack.victimGuid
            local engaged = npc.engaged == true and victim and members[victim] and
                            same_map(players[victim], obs.mapId) and alive(players[victim])
            local is_protected = npc.protected == true
            targets[target] = copy_target(npc, engaged == true, is_protected)
            if engaged and alive(npc) then in_combat = true end
            if is_protected then protected[target] = true end
        end
    end
    local confirmed, pending = {}, {}
    for _, row in ipairs(plan.controls or {}) do
        local target = state.bindings[row.mark]
        if feed.complete and feed.groupGuid == group.groupGuid then
            -- The marked policy rejects changed bindings. Use the feed to
            -- project the first aura for a mark that has not been bound yet.
            for _, mark in ipairs(feed.marks or {}) do
                if mark.icon == icons[row.mark] and not target then target = mark.targetGuid end
            end
        end
        if guid(target) then
            local aura = (obs.controlObserved or {})[target]
            if aura and aura.active == true then
                protected[target] = true
                local duration_valid = not aura.durationKnown or
                    type(aura.remainingMs) == "number" and aura.remainingMs > 0
                if aura.casterGuid == row.casterGuid and aura.spellId == row.spellId and guid(aura.revision) and duration_valid then
                    confirmed[target] = {known=true, active=true, technique=row.technique,
                                         remainingMs=aura.durationKnown and aura.remainingMs or nil}
                end
            end
            if row.casterGuid == obs.selfGuid and #frame.pending > 0 then pending[target] = true end
        end
    end
    local revision = guid(feed.revision)
    local prior = state.markRevision
    local stale = not revision or prior and (#revision < #prior or #revision == #prior and revision < prior)
    local marked = tactics.decide({
        plan=plan, party=stale and {group=group} or party, targets=targets,
        bindings=state.bindings, defeated=state.defeated,
        capabilities=obs.controlCapabilities, controlObserved=confirmed, controlPending=pending, inCombat=in_combat,
    })
    state.bindings, state.defeated = marked.bindings, marked.defeated
    if not stale and feed.complete and feed.groupGuid == group.groupGuid then
        state.markRevision = revision
    end
    local skull = state.bindings.skull
    for _, row in ipairs(plan.controls or {}) do
        local target = state.bindings[row.mark]
        if target then state.controls[target] = true end
    end
    for target in pairs(state.controls) do protected[target] = true end
    for target in pairs(protected) do
        if targets[target] then targets[target].protected = true end
    end
    if skull and targets[skull] and targets[skull].engaged and alive(targets[skull]) then
        state.started = true
    end
    local order = {}
    for _, mark in ipairs(plan.killOrder or {}) do
        local target = state.bindings[mark]
        if target and not protected[target] then order[#order+1] = target end
    end
    local leader = players[group.leaderGuid]
    local leader_alive = alive(leader)
    local queue = tactics.decide({
        kind="combat_queue", targets=targets, explicitOrder=order, currentFocus=state.focusGuid,
        ownerFocus=leader_alive and obs.observedOwnerFocus or nil,
        partyDPS=obs.partyDPS, switchSeconds=obs.switchSeconds,
    })
    out.queue, out.phase = queue.queue, marked.phase
    for _, row in ipairs(queue.queue) do
        if row.protected then out.protected[#out.protected+1] = row.targetGuid end
    end
    local marks_valid = not stale and (marked.phase == "focus" or marked.phase == "encounter_complete"
        or marked.phase == "apply_control")
    local skull_dead = skull and (state.defeated[skull] or false)
    local all_control_observed = true
    for target in pairs(state.controls) do
        if alive(targets[target]) and not confirmed[target] then all_control_observed = false end
    end
    -- Live leader setup requires the marked policy's exact observations. A
    -- leader who dies stays the party leader; the surviving witnessed fight
    -- can proceed without turning any reserved control into a DPS target.
    local order_ready = marked.phase == "focus" or marked.phase == "encounter_complete"
    if marks_valid and order_ready and state.started and skull and (skull_dead or queue.focusGuid == skull) and
       all_control_observed and (leader_alive or leader and leader.aliveKnown == true and leader.alive == false and skull_dead) then
        out.focusGuid, state.focusGuid = queue.focusGuid, queue.focusGuid
        out.phase = out.focusGuid and "engaged_focus" or "waiting_for_engaged_focus"
    end

    local self = players[obs.selfGuid]
    if frame.input.observationSequence ~= state.sequence or state.settledAt == state.sequence or
       #frame.pending ~= 0 or not members[obs.selfGuid] or not alive(self) or not same_map(self, obs.mapId) then
        return out
    end
    local support
    if healing and state.healingSequence and obs.healingSequence == state.healingSequence then
        local result = healing.decide({session_id=frame.session_id, cursor=frame.cursor, pending=frame.pending,
            input={kind="healing", observationSequence=state.healingSequence}})
        support = result.action
    end
    if support and support.capability == "heal" and support.sessionId == obs.sessionId and
       support.selfGuid == obs.selfGuid and guid(support.targetGuid) and members[support.targetGuid] and
       alive(players[support.targetGuid]) and same_map(players[support.targetGuid], obs.mapId) and
       usable_spell(obs, support.spellId) then
        out.action = proposal(obs, "heal", support.targetGuid, support.spellId); return out
    end
    local control = marks_valid and leader_alive and marked.control
    if control and control.casterGuid == obs.selfGuid then
        for _, row in ipairs(plan.controls or {}) do
            if control.mark == row.mark and (row.technique == "polymorph" or row.technique == "sap") and
               row.casterGuid == obs.selfGuid and alive(targets[control.targetGuid]) and
               (obs.targets[control.targetGuid] or {}).positionKnown == true and usable_spell(obs, row.spellId) then
                out.action = proposal(obs, row.technique, control.targetGuid, row.spellId); return out
            end
        end
    end
    local focus = out.focusGuid and obs.targets[out.focusGuid]
    local damage = obs.damage or {}
    if focus and focus.positionKnown == true and (not focus.trajectory or focus.trajectory.stopped == true) and
       usable_spell(obs, damage.spellId) and
       (damage.scope == "focus" or damage.scope == "runner" and known_runner(focus)) then
        out.action = proposal(obs, "offense", out.focusGuid, damage.spellId)
    end
    return out
end

return {decide=decide, on_event=on_event, export_state=export_state, import_state=import_state}
