-- Pure policy over actual eligible candidates. No game/file/network access.
return {
  decide = function(state)
    if state.kind == "heal_target" then
      local best = nil
      for _, player in ipairs(state.targets) do
        if best == nil or player.health * best.maxHealth < best.health * player.maxHealth
          or (player.health * best.maxHealth == best.health * player.maxHealth
            and player.guid == state.defaultTargetGuid) then
          best = player
        end
      end
      if best then return { targetGuid = best.guid } end
    elseif state.kind == "heal_spell" then
      local best, bestScore = nil, -1
      local missing = state.target.maxHealth - state.target.health
      local urgent = state.target.health * 100 < state.target.maxHealth * 40
      for _, spell in ipairs(state.spells) do
        local score
        if urgent then
          score = math.min(spell.amount, missing) / math.max(1.5, spell.castMs / 1000)
        else
          score = math.min(spell.amount, missing) / math.max(1, spell.cost)
        end
        if score > bestScore then best, bestScore = spell, score end
      end
      if best then return { spellId = best.spellId } end
    elseif state.kind == "damage_spell" then
      local best, bestScore = nil, -1
      for _, spell in ipairs(state.spells) do
        if spell.score > bestScore then best, bestScore = spell, spell.score end
      end
      if best then return { spellId = best.spellId } end
    end
    return {}
  end
}
