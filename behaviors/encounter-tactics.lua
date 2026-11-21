-- Shared, transport-free encounter decisions. A chat or autonomous leader
-- supplies the same structured plan. The executor owns all game API writes.
local icon = {star=1,circle=2,diamond=3,triangle=4,moon=5,square=6,x=7,skull=8}
local function guid(v)
    -- Creature GUIDs can exceed Lua's signed integer range. Keep their exact
    -- decimal representation so adjacent GUIDs remain distinct table keys.
    if type(v) ~= "string" or #v == 0 or #v > 20 or byte_at(v, 1) == 48 then return end
    for i=1,#v do
        local digit = byte_at(v, i)
        if digit < 48 or digit > 57 then return end
    end
    if #v < 20 or v <= "18446744073709551615" then return v end
end
local function wait(phase, bindings, defeated, protected)
    return {phase=phase, bindings=bindings or {}, defeated=defeated or {}, protected=protected or {}}
end
local function alive(npc)
    return npc and npc.present and npc.aliveKnown and npc.alive
end
local function dead(npc)
    return npc and npc.present and npc.aliveKnown and not npc.alive
end

local function finite_number(value)
    return type(value) == "number" and value - value == 0
end

local function positive_number(value)
    return finite_number(value) and value > 0
end

local function target_guid_before(a, b)
    return #a < #b or #a == #b and a < b
end

-- The queue covers potential pulls as well as current enemies. Focus is only
-- a recommendation for a target the harness already observed in the fight.
local function combat_queue(input)
    local rows, candidates = {}, {}
    local measured_rate, measured_count = 0, 0
    for key, npc in pairs(input.targets or {}) do
        local target = guid(key)
        if target and type(npc) == "table" and alive(npc) and npc.attackable == true then
            local seconds = npc.killSeconds
            if not positive_number(seconds) and positive_number(npc.health) then
                -- With no party damage estimate, compare the remaining work in HP.
                seconds = positive_number(input.partyDPS) and npc.health / input.partyDPS or npc.health
            end
            if positive_number(seconds) then
                local rate = npc.incomingDPS
                if not finite_number(rate) or rate < 0 then
                    rate = nil
                elseif rate > 0 then
                    measured_rate, measured_count = measured_rate + rate, measured_count + 1
                end
                local row = {targetGuid=target, seconds=seconds, rate=rate,
                             engaged=npc.engaged == true, protected=npc.protected == true}
                rows[#rows+1], candidates[target] = row, row
            end
        end
    end
    local typical = measured_count > 0 and measured_rate / measured_count or 1
    local aggregate = 0
    for _, row in ipairs(rows) do
        row.rate = row.rate or typical
        row.score = row.rate / row.seconds
        if row.engaged and not row.protected then aggregate = aggregate + row.rate end
    end
    -- The Lua harness supplies no table library. Visible candidates are
    -- bounded by the observation feed, and GUIDs remain exact decimal strings.
    for i=2,#rows do
        local row, j = rows[i], i-1
        while j >= 1 do
            local other = rows[j]
            if row.score < other.score or row.score == other.score and
               not target_guid_before(row.targetGuid, other.targetGuid) then break end
            rows[j+1], j = other, j-1
        end
        rows[j+1] = row
    end

    local result, added = {queue={}}, {}
    local function append(target)
        local row = candidates[target]
        if not row or added[target] then return end
        added[target] = true
        result.queue[#result.queue+1] = {targetGuid=target, engaged=row.engaged, protected=row.protected}
        if not result.focusGuid and row.engaged and not row.protected then result.focusGuid = target end
    end

    for _, target in ipairs(input.explicitOrder or {}) do append(target) end
    if #result.queue == 0 then append(input.ownerFocus) end
    local current = candidates[input.currentFocus]
    if not result.focusGuid and current and current.engaged and not current.protected then
        for _, next_target in ipairs(rows) do
            if next_target.engaged and not next_target.protected then
                local saved = next_target.rate * current.seconds - current.rate * next_target.seconds
                -- Switching time is supplied by the caller. When only HP is
                -- known, both kill estimates are relative, so omit its cost.
                local switch = input.switchSeconds
                local estimated_time = positive_number(input.partyDPS) or
                    positive_number(input.targets[current.targetGuid].killSeconds) and
                    positive_number(input.targets[next_target.targetGuid].killSeconds)
                if next_target ~= current and estimated_time and positive_number(switch)
                   and saved <= switch * aggregate then append(current.targetGuid) end
                break
            end
        end
    end
    for _, row in ipairs(rows) do append(row.targetGuid) end
    return result
end

local function pull_position(target)
    for _, coordinate in ipairs({"x", "y", "z"}) do
        local value = target[coordinate]
        if type(value) ~= "number" or not (math.abs(value) <= 40000) then return false end
    end
    return true
end

local function pull_distance_known(distance)
    return type(distance) == "number" and distance >= 0
end

local function assign_pull_marks(input)
    local targets, casters = input.targets or {}, input.casters or {}
    local occupied = input.occupiedMarks or {}
    local marked = {}
    for _, target in pairs(occupied) do marked[target] = true end
    if occupied.skull then return {assignments={}} end
    local pack_height = 4
    local focus, focus_distance, focus_priority
    for target, npc in pairs(targets) do
        local distance = type(npc) == "table" and npc.distance
        if guid(target) and not marked[target] and pull_distance_known(distance) then
            local same_level = type(input.ownerZ) == "number" and pull_position(npc)
                and math.abs(npc.z - input.ownerZ) <= pack_height
            local priority = npc.selected == true and 2 or same_level and 1 or 0
            if not focus or priority > focus_priority or priority == focus_priority and
               (distance < focus_distance or distance == focus_distance and target < focus) then
                focus, focus_distance, focus_priority = target, distance, priority
            end
        end
    end
    local assignments = {}
    if not focus then return {assignments=assignments} end
    assignments[1] = {mark="skull", targetGuid=focus}
    local primary = targets[focus]
    if not pull_position(primary) then return {assignments=assignments} end

    -- Choose a nearby pack first. Then share the same queue's order inside it.
    local pack = {}
    for target, npc in pairs(targets) do
        if guid(target) and not marked[target] and type(npc) == "table" and pull_distance_known(npc.distance) and pull_position(npc) then
            local dz = npc.z - primary.z
            local separation = (npc.x-primary.x)^2 + (npc.y-primary.y)^2 + dz^2
            if math.abs(dz) <= pack_height and separation <= 12^2 then pack[target] = npc end
        end
    end
    local ranked = combat_queue({targets=pack, ownerFocus=primary.selected and focus or nil})
    if ranked.queue[1] and not primary.selected then
        focus = ranked.queue[1].targetGuid
        assignments[1].targetGuid = focus
    end

    local candidates = {}
    for target, npc in pairs(targets) do
        local distance = type(npc) == "table" and npc.distance
        if target ~= focus and guid(target) and pull_distance_known(distance) and pull_position(npc) then
            if pack[target] then
                local chosen = targets[focus]
                local dz = npc.z-chosen.z
                local separation = (npc.x-chosen.x)^2 + (npc.y-chosen.y)^2 + dz^2
                if math.abs(dz) <= pack_height and separation <= 12^2 then candidates[target] = separation end
            end
        end
    end
    local function nearest(technique, exclude_sap)
        local best, best_distance
        for target, distance in pairs(candidates) do
            local npc = targets[target]
            if (not technique or npc[technique] == true) and (not exclude_sap or npc.sap ~= true) and
               (not best or distance < best_distance or distance == best_distance and target < best) then
                best, best_distance = target, distance
            end
        end
        return best
    end
    local sheep = not occupied.moon and casters.polymorph == true and (casters.sap == true and nearest("polymorph", true) or nearest("polymorph"))
    if sheep then candidates[sheep] = nil end
    local sap = not occupied.square and casters.sap == true and nearest("sap")
    if sap then candidates[sap] = nil end
    local next_target
    for _, row in ipairs(ranked.queue) do
        if candidates[row.targetGuid] then next_target = row.targetGuid; break end
    end
    next_target = next_target or nearest()
    if next_target and not occupied.x then assignments[#assignments+1] = {mark="x", targetGuid=next_target} end
    if sap then assignments[#assignments+1] = {mark="square", targetGuid=sap} end
    if sheep then assignments[#assignments+1] = {mark="moon", targetGuid=sheep} end
    return {assignments=assignments}
end

local function automatic_control(input)
    local group = (input.party or {}).group or {}
    local caster, focus = guid(input.casterGuid), guid(input.focusGuid)
    local preferred = guid(input.preferredGuid)
    local members = {}
    for _, member in ipairs(group.members or {}) do
        if member.online and guid(member.guid) then members[member.guid] = true end
    end
    if not group.known or not group.inGroup or not caster or not members[caster] or not input.inCombat then
        return wait("automatic_control_waiting_for_party_combat")
    end
    local technique
    for _, capability in ipairs(input.capabilities or {}) do
        if capability.guid == caster and capability.technique == "polymorph" and capability.ready then
            technique = capability.technique
            break
        end
    end
    if not technique then return wait("automatic_control_waiting_for_caster") end

    local observed = input.controlObserved or {}
    for _, aura in pairs(observed) do
        if aura.active and aura.casterGuid == caster and aura.technique == technique then
            return wait("automatic_control_active")
        end
    end
    for _, pending in pairs(input.controlPending or {}) do
        if pending then return wait("automatic_control_pending") end
    end

    local focused, attempted = input.focusTargets or {}, input.attemptedTargets or {}
    local threats, choice, choiceHealth, choiceAge = 0, nil, nil, nil
    local renew, renewIsThreat = nil, false
    for target, npc in pairs(input.targets or {}) do
        local age = tonumber(npc.attackAgeMs)
        local aura = observed[target]
        if guid(target) and alive(npc) and npc.attackable and not (aura and aura.active) then
            local threatening = npc.attackVictimKnown and members[npc.attackVictimGuid] and age and age >= 0 and age <= 5000
            if threatening then threats = threats + 1 end
            local health = tonumber(npc.healthPercent)
            if target ~= focus and not focused[target] and not attempted[target] and npc.inRange and health and health > 25 then
                if target == preferred then
                    renew, renewIsThreat = target, threatening
                elseif threatening and (not choice or health > choiceHealth or
                        health == choiceHealth and (age < choiceAge or age == choiceAge and target < choice)) then
                    choice, choiceHealth, choiceAge = target, health, age
                end
            end
        end
    end
    if renew and threats > (renewIsThreat and 1 or 0) then
        choice = renew
    elseif threats < 2 then
        return wait("automatic_control_waiting_for_add")
    end
    if not choice then return wait("automatic_control_no_eligible_add") end
    local out = wait("apply_control")
    out.control = {casterGuid=caster, targetGuid=choice, technique=technique}
    return out
end

local function decide(input)
    if input.kind == "combat_queue" then return combat_queue(input) end
    if input.kind == "automatic_control" then return automatic_control(input) end
    if input.kind == "assign_pull_marks" then return assign_pull_marks(input) end
    local plan, party = input.plan or {}, input.party or {}
    local group, feed = party.group or {}, party.raidTargets or {}
    local previous, defeated = input.bindings or {}, input.defeated or {}
    local function hold(phase) return wait(phase,previous,defeated) end
    if not group.known or not group.inGroup or not guid(group.groupGuid)
       or plan.groupGuid ~= group.groupGuid or not guid(plan.leaderGuid)
       or plan.leaderGuid ~= group.leaderGuid then return hold("waiting_for_actual_group_leader") end
    if not feed.complete or feed.groupGuid ~= group.groupGuid then return hold("waiting_for_full_raid_marks") end
    local marks, marked = {}, {}
    for _,row in ipairs(feed.marks or {}) do
        local n,g = row.icon, guid(row.targetGuid)
        if type(n) ~= "number" or n < 1 or n > 8 or n%1 ~= 0 or not g or marks[n] or marked[g] then
            return hold("ambiguous_raid_marks")
        end
        marks[n],marked[g]=g,true
    end

    local order, used, bindings = plan.killOrder or {}, {}, {}
    local targets = input.targets or {}
    if #order == 0 or #order > 8 then return hold("invalid_kill_order") end
    for _,name in ipairs(order) do
        local n = icon[name]
        if n == nil or used[name] then return hold("invalid_kill_order") end
        used[name]=true
        local before, current=previous[name], marks[n]
        if before and current and before ~= current then return hold("raid_mark_changed_replan") end
        local g = current or (before and (defeated[before] or dead(targets[before])) and before)
        if not g then return hold("waiting_for_mark_"..name) end
        bindings[name]=g
    end

    local focus = nil
    for _,name in ipairs(order) do
        local g=bindings[name]
        if not defeated[g] then
            if dead(targets[g]) then defeated[g]=true
            else focus=name; break end
        end
    end
    if not focus then return wait("encounter_complete",bindings,defeated) end

    local control, protected = {}, {}
    for _,row in ipairs(plan.controls or {}) do
        if not used[row.mark] or control[row.mark] or type(row.technique) ~= "string"
           or #row.technique == 0 then return wait("invalid_control_order",bindings,defeated) end
        control[row.mark]=row
        local g=bindings[row.mark]
        if row.mark ~= focus and not defeated[g] and not dead(targets[g]) then protected[#protected+1]=g end
    end
    local out=wait("waiting_for_focus_observation",bindings,defeated,protected)
    local members={}
    for _,m in ipairs(group.members or {}) do if m.online and guid(m.guid) then members[m.guid]=true end end
    if not members[plan.leaderGuid] then out.phase="waiting_for_actual_group_leader";return out end
    for _,name in ipairs(order) do
        local row,g = control[name], bindings[name]
        if row and name ~= focus and not defeated[g] and not dead(targets[g]) then
            if not alive(targets[g]) then out.phase="waiting_for_control_target_"..name;return out end
            local caster=nil
            for _,cap in ipairs(input.capabilities or {}) do
                if members[cap.guid] and cap.technique==row.technique and cap.ready and
                   (not row.casterGuid or cap.guid==row.casterGuid) and
                   (not caster or cap.guid<caster.guid) then caster=cap end
            end
            local aura=(input.controlObserved or {})[g] or {}
            if (input.controlPending or {})[g] then out.phase="waiting_for_control_observation_"..name;return out end
            if not aura.known or not aura.active or aura.technique~=row.technique or
               (row.refresh and type(aura.remainingMs)=="number" and aura.remainingMs<=row.refresh) then
                if not caster then out.phase="waiting_for_control_caster_"..name;return out end
                if caster.outOfCombatOnly and (input.inCombat or targets[g].inCombat) then
                    out.phase="control_broken_in_combat_"..name;return out
                end
                out.phase="apply_control"
                out.control={casterGuid=caster.guid, targetGuid=g, technique=row.technique, mark=name}
                return out
            end
        end
    end
    if alive(targets[bindings[focus]]) then out.phase="focus";out.focus={mark=focus,targetGuid=bindings[focus]} end
    return out
end

return {decide=decide}
