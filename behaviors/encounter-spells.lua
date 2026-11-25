-- Pure, reloadable spell selection. The catalog describes mechanics; this
-- module owns rank preference, rotations, resource reserves and cooldown use.
-- Compose as lexical encounter_spells before encounter-session.lua.
local M = {}
M.tuning = {damage_window_ms=8000, minimum_dot_ticks=2, long_cooldown_ms=10000,
            cooldown_min_health_fraction=0.15, mana_reserve_fraction=0,
            cast_pressure_weight=0.65, finish_at_combo=5}
M.class_profiles = {
    [2]={mana_reserve_fraction=0.15}, [5]={mana_reserve_fraction=0.15},
    [7]={mana_reserve_fraction=0.15}, [11]={mana_reserve_fraction=0.15},
}
-- Race profiles and family weights can be authored without changing the host.
M.race_profiles = {}
-- Named starter missions use the same scorer with their established policy
-- profile. Ordinary encounters retain the defaults above.
M.starter_options = {
    all_ranks=true, rank_order="level_rank", require_metadata=true, power_type=0,
    minimum_action_seconds=1.5, ignore_global_cooldown=true,
    damage_window_ms=8000, fixed_damage_window=true, minimum_dot_ticks=1,
    minimum_periodic_ticks=1, skip_own_periodic=true, allow_zero_score=true,
    finish_health_fraction=0.25, exclude_periodic_on_finish=true,
    lethal_raw_cast_time=true, cast_pressure_weight=1, interrupted_cast_weight=0.5,
    avoid_long_cooldowns=false, max_range_padding=2, mana_reserve_fraction=0,
}

local function number(value)
    local n = tonumber(value)
    return n and n == n and n >= 0 and n <= 9007199254740991 and n or nil
end

local function spell_id(value)
    local n = number(value)
    return n and n > 0 and n <= 4294967295 and n % 1 == 0 and n or nil
end

local function caster(obs)
    return (obs.players or {})[obs.selfGuid] or {}
end

local function profile(obs, options)
    local out = {}
    local function apply(values)
        for key, value in pairs(values or {}) do out[key] = value end
    end
    local self = caster(obs)
    apply(M.tuning)
    if self.classKnown == true then
        apply((options.class_profiles or M.class_profiles)[self.classId])
    end
    if self.raceKnown == true then
        apply((options.race_profiles or M.race_profiles)[self.raceId])
    end
    apply(options)
    return out
end

local function eligible(spell, capability, options)
    return type(spell) == "table" and spell.known == true and spell.capability == capability and
        (options.require_metadata ~= true or spell.metadataKnown == true) and
        (options.power_type == nil or spell.powerType == options.power_type) and
        (spell.ready == true or options.require_ready == false and spell.available == true) and
        (options.instant_only ~= true or spell.metadataKnown == true and
         number(spell.castTimeMs == nil and 0 or spell.castTimeMs) == 0) and
        (options.max_cost == nil or spell.metadataKnown ~= true or
         number(spell.cost) ~= nil and number(spell.cost) <= options.max_cost)
end

local function higher_rank(a, b, options)
    local ar, br = number(a.row.rank) or 0, number(b.row.rank) or 0
    local al, bl = number(a.row.level) or 0, number(b.row.level) or 0
    if options and options.rank_order == "level_rank" and al ~= bl then return al > bl end
    if ar ~= br then return ar > br end
    if al ~= bl then return al > bl end
    return a.id < b.id
end

-- One eligible rank per catalog family. Rank IDs are not an ordering.
function M.best_ranks(obs, capability, options)
    options = options or {}
    local families = {}
    for key, row in pairs(obs.spells or {}) do
        local id = spell_id(key)
        if id and eligible(row, capability, options) then
            local family = row.metadataKnown == true and spell_id(row.familyId)
            local group = family and tostring(family) or "unknown:" .. tostring(id)
            if options.all_ranks == true then group = tostring(id) end
            local candidate = {id=id, row=row, family=family}
            if not families[group] or higher_rank(candidate, families[group], options) then families[group] = candidate end
        end
    end
    return families
end

function M.highest_rank(obs, capability, family, options)
    local ranks = M.best_ranks(obs, capability, options)
    local row = ranks[tostring(family)]
    if family == nil then
        for _, candidate in pairs(ranks) do
            if not row or higher_rank(candidate, row, options) then row = candidate end
        end
    end
    return row and row.id or nil
end

local function in_range(obs, target, spell, options)
    local self = caster(obs)
    if spell.requiresComboPoints == true and (self.comboKnown ~= true or
       self.comboTargetGuid ~= (target or {}).guid or (number(self.comboPoints) or 0) < 1) then return false end
    if options.require_ready == false then return true end
    if spell.requiresBehind == true or spell.requiresFacing == true then
        if self.positionKnown ~= true or not target or target.positionKnown ~= true then return false end
        -- Known protobuf poses omit zero-valued orientation in ordinary JSON.
        local self_o, target_o = self.orientation or 0, target.orientation or 0
        if type(self_o) ~= "number" or type(target_o) ~= "number" then return false end
        local dx,dy = (self.x or 0)-(target.x or 0), (self.y or 0)-(target.y or 0)
        if spell.requiresBehind == true and dx*math.cos(target_o)+dy*math.sin(target_o) >= -0.25 then return false end
        if spell.requiresFacing == true and -dx*math.cos(self_o)-dy*math.sin(self_o) < 0 then return false end
    end
    if self.positionKnown ~= true or not target or target.positionKnown ~= true then return true end
    local x, y, z = (self.x or 0)-(target.x or 0), (self.y or 0)-(target.y or 0), (self.z or 0)-(target.z or 0)
    local distance = math.sqrt(x*x+y*y+z*z)
    return distance >= (number(spell.minRange) or 0) and
        distance <= (number(spell.maxRange) or 1000000)-(number(options.max_range_padding) or 0)
end

local function own_periodic(target, family)
    for _, active in ipairs(target and target.ownDamageFamilies or {}) do
        if active == family then return true end
    end
    return false
end

local function runner(target)
    return target and target.unitFlagsKnown == true and type(target.unitFlags) == "number" and
        math.floor(target.unitFlags / 8388608) % 2 == 1 and target.healthKnown == true and
        type(target.health) == "number" and type(target.maxHealth) == "number" and
        target.health > 0 and target.health * 4 <= target.maxHealth
end

local function target_health(target)
    if target and target.healthKnown == true then return number(target.health) end
end

local function family_weight(tuning, family)
    local value = (tuning.family_weights or {})[family] or (tuning.family_weights or {})[tostring(family)]
    return value == nil and 1 or number(value) or 0
end

local function action_seconds(row, options)
    local cast, gcd = (number(row.castTimeMs) or 0)/1000, (number(row.globalCooldownMs) or 0)/1000
    if options.ignore_global_cooldown == true then gcd = 0 end
    -- A zero-GCD instant still needs a finite comparison window; this score
    -- does not impose an execution cooldown. Rogue's catalog GCD is one second.
    return math.max(cast,gcd,number(options.minimum_action_seconds) or (cast == 0 and gcd == 0 and 1 or 0))
end

function M.damage_spell(obs, target, override, options)
    options = options or {}
    if override then
        local id = spell_id(override.spellId)
        local row = id and (obs.spells or {})[tostring(id)]
        local legacy = options.require_metadata ~= true and not obs.worldObservationRevision and type(row) == "table" and
                       row.known == true and row.ready == true
        if (override.scope == "focus" or override.scope == "runner" and runner(target)) and
           (legacy or eligible(row, "offense", options)) and in_range(obs, target, row, options) then return id end
        return nil
    end
    local tuning, self = profile(obs, options), caster(obs)
    local reserve = self.powerKnown == true and self.powerType == 0 and
                    (number(self.maxPower) or 0) * (number(tuning.mana_reserve_fraction) or 0) or 0
    if tuning.mana_reserve ~= nil then reserve = number(tuning.mana_reserve) or 0 end
    local rank_options = {}
    for key, value in pairs(options) do rank_options[key] = value end
    if self.powerKnown == true then
        rank_options.max_cost = math.max(0, (number(self.power) or 0)-reserve)
    end
    local ranks, health = M.best_ranks(obs, "offense", rank_options), target_health(target)
    local best, best_candidate, best_score, best_kills, best_time, best_finisher, fallback, filler_dps
    local finish = health and health > 0
    if tuning.finish_health_fraction ~= nil then
        local maximum = target and number(target.maxHealth)
        finish = finish and maximum and maximum > 0 and health <= maximum*tuning.finish_health_fraction
    end
    filler_dps = 0
    for _, candidate in pairs(ranks) do
        local row = candidate.row
        if row.metadataKnown == true then
            local seconds = action_seconds(row, tuning)
            filler_dps = math.max(filler_dps, (number(row.directDamage) or 0)/seconds)
        end
    end
    for _, candidate in pairs(ranks) do
        local id, row = candidate.id, candidate.row
        if in_range(obs, target, row, options) then
            if row.metadataKnown ~= true then
                if not fallback or id < fallback then fallback = id end
            else
                local cost, direct = number(row.cost), number(row.directDamage) or 0
                local cast_ms = number(row.castTimeMs) or 0
                local seconds = action_seconds(row, tuning)
                local allowed = cost ~= nil and (self.powerKnown ~= true or self.powerType == row.powerType and
                                (number(self.power) or 0)-cost >= reserve)
                local finisher = row.requiresComboPoints == true and self.comboKnown == true and
                    self.comboTargetGuid == (target or {}).guid and (number(self.comboPoints) or 0) >= (number(tuning.finish_at_combo) or 5)
                if row.requiresComboPoints == true and not finisher and row.damageModelKnown ~= true then allowed = false end
                local periodic, window = 0, (number(tuning.damage_window_ms) or 8000)/1000
                if tuning.fixed_damage_window ~= true and health and filler_dps > 0 then window = math.min(window, health/filler_dps) end
                local active_periodic = own_periodic(target, candidate.family)
                if not active_periodic or tuning.skip_own_periodic == true then
                    for _, tick in ipairs(row.periodicDamage or {}) do
                        local period = (number(tick.periodMs) or 0)/1000
                        local ticks = period > 0 and math.floor(window/period) or 0
                        if period > 0 then ticks = math.max(ticks, number(tuning.minimum_periodic_ticks) or 0) end
                        if ticks >= (number(tuning.minimum_dot_ticks) or 2) then
                            periodic = periodic + (number(tick.damagePerTick) or 0)*ticks
                        end
                    end
                end
                if tuning.skip_own_periodic == true and periodic > 0 and active_periodic then allowed = false end
                if tuning.exclude_periodic_on_finish == true and finish then
                    if periodic > 0 and direct == 0 then allowed = false end
                    periodic = 0
                end
                local cooldown = math.max(number(row.cooldownMs) or 0, number(row.categoryCooldownMs) or 0)
                if tuning.avoid_long_cooldowns ~= false and options.burst ~= true and cooldown >= (number(tuning.long_cooldown_ms) or 10000) and health and target.maxHealth and
                   health < target.maxHealth*(number(tuning.cooldown_min_health_fraction) or 0.15) and
                   filler_dps > 0 and health <= filler_dps*1.5 then allowed = false end
                local score = (direct+periodic)/seconds * family_weight(tuning, candidate.family)
                local attack = (obs.engagement or {})[target and tostring(target.guid)]
                if cast_ms > 0 and attack and attack.victimGuid == obs.selfGuid then
                    score = score*(number(tuning.cast_pressure_weight) or 0.65)
                end
                local failure = obs.lastCast or {}
                if cast_ms > 0 and failure.targetGuid == (target or {}).guid and
                   (failure.failure == 40 or failure.failure == 41) then
                    score = score*(number(tuning.interrupted_cast_weight) or 1)
                end
                local kills = finish and direct >= health or false
                local lethal_time = tuning.lethal_raw_cast_time == true and cast_ms or seconds
                local prefer = not best
                if best then
                    if kills ~= best_kills then prefer = kills
                    elseif kills and lethal_time ~= best_time then prefer = lethal_time < best_time
                    elseif finisher ~= best_finisher then prefer = finisher
                    else prefer = score > best_score or score == best_score and
                        (tuning.rank_order == "level_rank" and higher_rank(candidate, best_candidate, tuning) or
                         tuning.rank_order ~= "level_rank" and id < best) end
                end
                if allowed and (score > 0 or tuning.allow_zero_score == true and score == 0) and prefer then
                    best, best_candidate, best_score, best_kills, best_time, best_finisher =
                        id, candidate, score, kills, lethal_time, finisher
                end
            end
        end
    end
    return best or fallback
end

return M
