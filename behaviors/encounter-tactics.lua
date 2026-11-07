-- Shared, transport-free encounter decisions. A chat or autonomous leader
-- supplies the same structured plan. The executor owns all game API writes.
local icon = {star=1,circle=2,diamond=3,triangle=4,moon=5,square=6,x=7,skull=8}
local function guid(v)
    -- The bridge's native GUID fields are decimal strings; keep their exact
    -- identity instead of converting the key to a Lua number.
    if type(v) == "string" and tonumber(v) and tonumber(v) > 0 then return v end
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

local function decide(input)
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
