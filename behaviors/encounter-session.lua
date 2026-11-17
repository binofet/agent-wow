-- Compose this file after the lexical encounter_tactics module, whose source
-- is encounter-tactics.lua wrapped in a function. Optional encounter_healing
-- and encounter_self_mana modules follow the same composition rule.
-- The host enqueues current observations and calls decide with their sequence.
-- Action proposals bind the exact observation, actor, target and learned spell.
-- encounter_intent may supply the authored plan and damage policy. The
-- reflected game feed supplies observations; it never authors a plan.
local tactics = assert(encounter_tactics)
local healing = encounter_healing
local self_mana = encounter_self_mana
local intent = encounter_intent or {}
local state = {bindings={}, defeated={}, controls={}}
-- A recovery channel requires a new sample after reload or action settlement.
local mana_observation, mana_sequence
local observation
local terminal = {confirmed=true, failed=true, not_dispatched=true, settled_unverified=true}
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

local function host_can_decide(obs)
    return obs.hostOwned == true or obs.hostRequested == true or
        (obs.hostOwned == nil and not obs.worldObservationRevision)
end

local function on_event(event)
    if healing and not healing.on_event(event) then return false end
    if self_mana and not self_mana.on_event(event) then return false end
    if event.kind == "encounter_observation" then
        local obs = event.value
        observation = obs
        state.sequence = event.sequence
        if obs.worldObservationRevision then
            state.observation = nil
            if healing and host_can_decide(obs) and obs.healing and obs.healing.hostOwned == true and
               obs.healing.sessionId == obs.sessionId then
                if not healing.on_event({kind="healing_observation", sequence=event.sequence, value=obs.healing}) then
                    return false
                end
                state.healingSequence = event.sequence
            end
        else
            state.observation = obs
        end
    elseif event.kind == "healing_observation" then
        state.healingSequence = event.sequence
    elseif self_mana and event.kind == "self_mana_observation" then
        mana_observation, mana_sequence = event.value, event.sequence
    end
    if event.receipt and terminal[event.receipt.outcome] then
        -- Wait for an updated observation before proposing another action.
        state.settledAt = state.sequence
        mana_observation, mana_sequence = nil, nil
    end
    return true
end

local function export_state()
    return {version=1, data={encounter=state, healing=healing and healing.export_state(),
                            selfMana=self_mana and self_mana.export_state()}}
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
    local mana = checkpoint.data.selfMana
    if mana and (not self_mana or type(mana) ~= "table" or not self_mana.import_state({
        version=mana.version, data=mana.data, session_id=checkpoint.session_id,
        cursor=checkpoint.cursor, pending=checkpoint.pending
    })) then return false end
    state = saved
    observation = saved.observation
    mana_observation, mana_sequence = nil, nil
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

local function usable_spell(obs, spell, capability)
    local observed = type(spell) == "number" and spell > 0 and spell <= 4294967295 and spell % 1 == 0 and
        (obs.spells or {})[tostring(spell)]
    return observed and observed.known == true and observed.ready == true and
        (not obs.worldObservationRevision or observed.capability == capability)
end

local function proposal(obs, kind, target, spell)
    return {capability=kind, sessionId=obs.sessionId, selfGuid=obs.selfGuid,
            targetGuid=target, spellId=spell, visitId=obs.visitId, observationSequence=state.sequence}
end

local function known_runner(target)
    return target.unitFlagsKnown == true and type(target.unitFlags) == "number" and
           math.floor(target.unitFlags / 8388608) % 2 == 1 and target.healthKnown == true and
           type(target.health) == "number" and type(target.maxHealth) == "number" and
           target.health > 0 and target.health * 4 <= target.maxHealth
end

local function current_visit(obs)
    return type(obs.mapId) == "number" and type(obs.visitId) == "string" and #obs.visitId > 0 and
        #obs.visitId <= 80 and (not obs.worldObservationRevision or guid(obs.visitId))
end

local function bound_plan(obs)
    local plan, group = obs.plan or intent.plan or {}, (obs.party or {}).group or {}
    if type(plan.id) ~= "string" or #plan.id == 0 or #plan.id > 80 or not current_visit(obs) or
       not group.known or not group.inGroup or not guid(group.groupGuid) or not guid(group.leaderGuid) or
       plan.groupGuid ~= group.groupGuid or plan.leaderGuid ~= group.leaderGuid then return end
    return plan
end

local function active_aura(aura, row)
    return aura.casterGuid == row.casterGuid and aura.spellId == row.spellId and guid(aura.revision) and
        (not aura.durationKnown or type(aura.remainingMs) == "number" and aura.remainingMs > 0)
end

local function damage_spell(obs, target)
    local damage = obs.damage or intent.damage
    if damage then
        if (damage.scope == "focus" or damage.scope == "runner" and known_runner(target)) and
           usable_spell(obs, damage.spellId, "offense") then return damage.spellId end
        return
    end
    local selected
    for key, spell in pairs(obs.spells or {}) do
        local id = guid(key) and tonumber(key)
        -- Rows carry readiness but no relative rank preference. Use a
        -- stable numeric tie-break among exact eligible offensive spells.
        if id and id <= 4294967295 and spell.capability == "offense" and spell.known == true and
           spell.ready == true and (not selected or id < selected) then selected = id end
    end
    return selected
end

local function owns_current_observation(obs, frame, self)
    if frame.input.kind ~= "encounter" or frame.input.observationSequence ~= state.sequence or
       not host_can_decide(obs) or obs.spellbookKnown ~= true or not alive(self) or
       not same_map(self, obs.mapId) then return false end
    for _, spell in pairs(obs.spells or {}) do
        if spell.capability == "offense" and spell.known == true then return true end
    end
    return false
end

local function observe_targets(obs, members)
    local targets, auras = {}, {}
    local players = obs.players or {}
    local in_combat = false
    for target, npc in pairs(obs.targets or {}) do
        if guid(target) and same_map(npc, obs.mapId) then
            local attack = (obs.engagement or {})[target]
            local victim = attack and guid(attack.victimGuid)
            local engaged = npc.engaged == true and victim and (victim == obs.selfGuid or members[victim]) and
                            same_map(players[victim], obs.mapId) and alive(players[victim])
            targets[target] = copy_target(npc, engaged == true, npc.protected == true or state.controls[target] == true)
            if engaged and alive(npc) then in_combat = true end
        end
    end
    for _, aura in ipairs(obs.protectedAuras or {}) do
        local target = targets[aura.targetGuid]
        if target then
            target.protected = true
            local rows = auras[aura.targetGuid] or {}
            rows[#rows+1] = aura
            auras[aura.targetGuid] = rows
        end
    end
    for guid, aura in pairs(obs.controlObserved or {}) do
        if targets[guid] and aura.active == true then
            targets[guid].protected = true
            local rows = auras[guid] or {}
            rows[#rows+1] = aura
            auras[guid] = rows
        end
    end
    return targets, auras, in_combat
end

local function marked_plan(frame, obs, plan, targets, auras, in_combat)
    local party = obs.party
    local group, feed = party.group, party.raidTargets or {}
    local confirmed, pending = {}, {}
    for _, row in ipairs(plan.controls or {}) do
        local target = state.bindings[row.mark]
        if feed.groupGuid == group.groupGuid then
            for _, mark in ipairs(feed.marks or {}) do
                if mark.icon == icons[row.mark] then
                    -- Preserve observed reservations while a full current
                    -- list is unavailable, without rebinding the authored plan.
                    if targets[mark.targetGuid] then targets[mark.targetGuid].protected = true end
                    target = target or mark.targetGuid
                end
            end
        end
        if guid(target) then
            for _, aura in ipairs(auras[target] or {}) do
                if active_aura(aura, row) then
                    local remaining = aura.durationKnown and aura.remainingMs or nil
                    local earlier = confirmed[target]
                    if not earlier or remaining and (not earlier.remainingMs or remaining > earlier.remainingMs) then
                        confirmed[target] = {known=true, active=true, technique=row.technique, remainingMs=remaining}
                    end
                end
            end
            if row.casterGuid == obs.selfGuid and #frame.pending > 0 then pending[target] = true end
        end
    end
    local revision, prior = guid(feed.revision), state.markRevision
    local stale = feed.complete ~= true or feed.groupGuid ~= group.groupGuid or
        not revision or prior and (#revision < #prior or #revision == #prior and revision < prior)
    local marked = tactics.decide({
        plan=plan, party=stale and {group=group} or party, targets=targets,
        bindings=state.bindings, defeated=state.defeated,
        capabilities=obs.controlCapabilities, controlObserved=confirmed, controlPending=pending, inCombat=in_combat,
    })
    state.bindings, state.defeated = marked.bindings, marked.defeated
    if not stale then state.markRevision = revision end
    for _, row in ipairs(plan.controls or {}) do
        local target = state.bindings[row.mark]
        if target then state.controls[target] = true end
    end
    local all_control_observed = true
    for target in pairs(state.controls) do
        if targets[target] then targets[target].protected = true end
        if alive(targets[target]) and not confirmed[target] then all_control_observed = false end
    end
    return marked, stale, all_control_observed
end

local function decide_encounter(frame)
    local out = {queue={}, protected={}, phase="waiting_for_encounter_observation"}
    local obs = observation
    if not obs or obs.sessionId ~= frame.session_id or not guid(obs.selfGuid) or not current_visit(obs) then return out end
    local plan = bound_plan(obs)
    local group, players = (obs.party or {}).group or {}, obs.players or {}
    if state.visitId == obs.visitId and state.mapId ~= obs.mapId then
        out.phase = "encounter_identity_changed"; return out
    end
    if state.visitId ~= obs.visitId or plan and state.planId ~= plan.id then
        state.visitId, state.mapId = obs.visitId, obs.mapId
        state.planId, state.groupGuid, state.leaderGuid = plan and plan.id, group.groupGuid, group.leaderGuid
        state.bindings, state.defeated, state.controls = {}, {}, {}
        state.started, state.focusGuid, state.markRevision = false, nil, nil
    end
    if plan and (state.groupGuid ~= group.groupGuid or state.leaderGuid ~= group.leaderGuid) then
        out.phase = "encounter_identity_changed"
        plan = nil
    elseif not plan then
        out.phase = "waiting_for_bound_group"
    end

    local members = {}
    if group.known == true and group.inGroup == true and guid(group.groupGuid) then
        for _, member in ipairs(group.members or {}) do
            if guid(member.guid) then members[member.guid] = member.online == true end
        end
    end
    local targets, auras, in_combat = observe_targets(obs, members)
    local marked, stale, all_control_observed
    if plan then
        marked, stale, all_control_observed = marked_plan(frame, obs, plan, targets, auras, in_combat)
        out.phase = marked.phase
    end
    local has_binding = false
    for _ in pairs(state.bindings) do has_binding = true; break end
    local fallback = not plan or stale or not has_binding or marked.phase == "waiting_for_actual_group_leader"
    local order, queue_targets = {}, targets
    if fallback and (in_combat or not plan) then
        queue_targets = {}
        for target, npc in pairs(targets) do
            if npc.engaged then queue_targets[target] = npc end
        end
    else
        for _, mark in ipairs(plan.killOrder or {}) do
            local target = state.bindings[mark]
            if target and (not targets[target] or not targets[target].protected) then order[#order+1] = target end
        end
    end
    local leader = players[group.leaderGuid]
    local leader_alive = alive(leader)
    local queue = tactics.decide({
        kind="combat_queue", targets=queue_targets, explicitOrder=order,
        currentFocus=not fallback and state.focusGuid or nil,
        ownerFocus=not fallback and leader_alive and obs.observedOwnerFocus or nil,
        partyDPS=obs.partyDPS or intent.partyDPS, switchSeconds=obs.switchSeconds or intent.switchSeconds,
    })
    out.queue = queue.queue
    for _, row in ipairs(queue.queue) do
        if row.protected then out.protected[#out.protected+1] = row.targetGuid end
    end
    local skull = plan and state.bindings.skull
    if skull and alive(targets[skull]) and targets[skull].engaged then state.started = true end
    local control
    if fallback then
        if queue.focusGuid then out.focusGuid, out.phase = queue.focusGuid, "engaged_defense" end
    else
        local skull_dead = skull and state.defeated[skull]
        local order_ready = marked.phase == "focus" or marked.phase == "encounter_complete"
        -- A dead but witnessed leader stays the leader. Continue only after
        -- the marked Skull's death is also witnessed.
        if order_ready and state.started and skull and (skull_dead or queue.focusGuid == skull) and
           all_control_observed and (leader_alive or leader and leader.aliveKnown == true and leader.alive == false and skull_dead) then
            out.focusGuid, state.focusGuid = queue.focusGuid, queue.focusGuid
            out.phase = out.focusGuid and "engaged_focus" or "waiting_for_engaged_focus"
        end
        control = leader_alive and marked.phase == "apply_control" and marked.control
    end

    local self = players[obs.selfGuid]
    if owns_current_observation(obs, frame, self) then
        out.encounterOwnership = {visitId=obs.visitId, observationSequence=state.sequence}
    end
    if not host_can_decide(obs) or fallback and obs.hostOwned ~= true or
       frame.input.observationSequence ~= state.sequence or state.settledAt == state.sequence or
       #frame.pending ~= 0 or not alive(self) or not same_map(self, obs.mapId) then
        return out
    end
    if not fallback and not members[obs.selfGuid] then return out end
    local support
    local heal_sequence = obs.healingSequence
    if obs.worldObservationRevision and obs.healing and obs.healing.hostOwned == true then heal_sequence = state.sequence end
    if plan and members[obs.selfGuid] and (not fallback or in_combat) and healing and
       state.healingSequence and heal_sequence == state.healingSequence then
        local result = healing.decide({session_id=frame.session_id, cursor=frame.cursor, pending=frame.pending,
            input={kind="healing", observationSequence=state.healingSequence}})
        support = result.action
    end
    if support and support.capability == "heal" and support.sessionId == obs.sessionId and
       support.selfGuid == obs.selfGuid and guid(support.targetGuid) and members[support.targetGuid] and
       alive(players[support.targetGuid]) and same_map(players[support.targetGuid], obs.mapId) and
       usable_spell(obs, support.spellId, "heal") then
        out.action = proposal(obs, "heal", support.targetGuid, support.spellId); return out
    end
    if obs.targetProtectionOverflow == true then return out end
    if control and control.casterGuid == obs.selfGuid then
        for _, row in ipairs(plan.controls or {}) do
            if control.mark == row.mark and (row.technique == "polymorph" or row.technique == "sap") and
               row.casterGuid == obs.selfGuid and alive(targets[control.targetGuid]) and
               (obs.targets[control.targetGuid] or {}).positionKnown == true and usable_spell(obs, row.spellId, row.technique) then
                out.action = proposal(obs, row.technique, control.targetGuid, row.spellId); return out
            end
        end
    end
    local focus = out.focusGuid and obs.targets[out.focusGuid]
    if focus and focus.positionKnown == true and (fallback or not focus.trajectory or focus.trajectory.stopped == true) then
        local spell = damage_spell(obs, focus)
        if spell then out.action = proposal(obs, "offense", out.focusGuid, spell) end
    end
    return out
end

local function quiet_for_mana(view, session_id, encounter)
    if type(view) ~= "table" or view.sessionId ~= session_id or view.hostOwned ~= true or
       not guid(view.selfGuid) then return false end
    if encounter then
        if encounter.sessionId ~= view.sessionId or encounter.selfGuid ~= view.selfGuid then return false end
        for _, target in pairs(encounter.targets or {}) do
            if target.engaged == true and alive(target) then return false end
        end
    end
    local caster, activity, threat = view.caster, view.activity, view.threat
    return type(caster) == "table" and caster.guid == view.selfGuid and alive(caster) and
        caster.mapKnown == true and caster.positionKnown == true and
        caster.movementKnown == true and caster.moving == false and
        caster.combatKnown == true and caster.inCombat == false and
        caster.channelKnown == true and caster.channelSpellId == 0 and
        type(activity) == "table" and activity.known == true and activity.idle == true and
        type(threat) == "table" and threat.known == true and
        threat.attackingSelf == false and threat.attackingParty == false
end

local function decide(frame)
    local out = decide_encounter(frame)
    if not self_mana or out.action or #frame.pending ~= 0 then return out end
    local input, encounter = frame.input, observation
    local expected
    if input.kind == "self_mana" then
        -- A separate mana tick cannot supersede a bound encounter decision.
        if encounter and bound_plan(encounter) then return out end
        expected = input.observationSequence
    elseif encounter and host_can_decide(encounter) and input.observationSequence == state.sequence and
           state.settledAt ~= state.sequence then
        expected = (input.observationSequences or {}).self_mana
    end
    if not guid(expected) or expected ~= mana_sequence then return out end
    local view = mana_observation
    mana_observation, mana_sequence = nil, nil
    if not quiet_for_mana(view, frame.session_id, encounter) then return out end
    local result = self_mana.decide({session_id=frame.session_id, cursor=frame.cursor, pending=frame.pending,
        input={kind="self_mana", observationSequence=expected}})
    local action = result.action
    if action and action.capability == "cast_self" and action.sessionId == view.sessionId and
       action.selfGuid == view.selfGuid and action.targetGuid == view.selfGuid and
       action.observationSequence == expected and type(action.spellId) == "number" and
       action.spellId > 0 and action.spellId <= 4294967295 and action.spellId % 1 == 0 then
        out.action = action
    end
    return out
end

return {decide=decide, on_event=on_event, export_state=export_state, import_state=import_state}
