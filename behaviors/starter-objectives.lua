-- Starter mission objectives and practice lifecycle. The caller owns observed
-- facts, exact native toggles, durable mutation reservations and receipt custody.
local M = {}
M.fargodeep_pull={minimum_health=95, minimum_mana=45, minimum_energy=90,stand_yards={[4]=3.5,[5]=28,[8]=33},quests={47,60},recheck_ms=250}
local function array(v,max)
    if type(v)~="table" or #v>max then return false end
    local n=0
    for k in pairs(v) do
        if type(k)~="number" or k<1 or k>max or k%1~=0 then return false end
        n=n+1
    end
    return n==#v
end
local function ids(v,max)
    if not array(v,max) then return end
    local set={}
    for _,q in ipairs(v) do
        if type(q)~="number" or q<=0 or q>4294967295 or q%1~=0 then return end
        local key=tostring(q)
        if set[key] then return end
        set[key]=true
    end
    return set
end
local function decimal(v)
    if v=="0" then return true end
    if type(v)~="string" or #v==0 or #v>20 or #v>1 and byte_at(v,1)==48 or
       #v==20 and v>"18446744073709551615" then return false end
    for i=1,#v do local c=byte_at(v,i); if c<48 or c>57 then return false end end
    return true
end
local function number(v) return type(v)=="number" and v>=-9007199254740991 and v<=9007199254740991 end
local function uint(v) return number(v) and v>=0 and v<=4294967295 and v%1==0 end
local function bit(v,mask) return math.floor((v or 0)/mask)%2==1 end
local function point(p) return type(p)=="table" and number(p.x or 0) and number(p.y or 0) and number(p.z or 0) end
local function distance2(a,b)
    local x,y,z=(a.x or 0)-(b.x or 0),(a.y or 0)-(b.y or 0),(a.z or 0)-(b.z or 0)
    return x*x+y*y+z*z
end
local function motion_point(c)
    if type(c.motionPositionKnown)=="boolean" then
        return c.motionPositionKnown and point(c.motionPosition) and c.motionPosition or nil
    end
    return c.positionKnown==true and point(c) and c or nil
end
local function northshire(p)
    return point(p) and (p.x or 0)>=-9599 and (p.x or 0)<=-8400 and (p.y or 0)>=-700 and (p.y or 0)<=200 and (p.z or 0)>=40 and (p.z or 0)<=180
end
local function quest_rows(input)
    if input.questLogKnown~=true or not array(input.questLog,25) or #input.questLog~=25 then return end
    local slots,quests={},{}
    for _,q in ipairs(input.questLog) do
        if type(q)~="table" or not uint(q.slot or 0) or (q.slot or 0)>=25 or slots[q.slot or 0] or not uint(q.questId or 0) then return end
        slots[q.slot or 0]=true
        if (q.questId or 0)~=0 then
            if quests[q.questId] then return end
            quests[q.questId]=q
        end
    end
    return quests
end
local function active_quest(q) return type(q)=="table" and q.failed~=true and not bit(q.state,2) end
local function incomplete(q) return active_quest(q) and q.complete~=true and not bit(q.state,1) end
local function recent_age(v)
    if uint(v) then return v<=30000 end
    return decimal(v) and (#v<5 or #v==5 and v<="30000")
end
local function owned_fight(input,target)
    return input.practice.pullStarted==true and bit(input.self.unitFlags,0x80000) and type(target)=="table" and
        target.attackVictimKnown==true and target.attackVictimGuid==input.selfGuid and recent_age(target.attackAgeMs or 0)
end
local function practice_target(c,tuning)
    return type(c)=="table" and decimal(c.guid) and c.guid~="0" and c.entry==tuning.target_entry and c.present==true and c.mapKnown==true and (c.mapId or 0)==0 and
        c.levelKnown==true and c.level==tuning.target_level and c.healthKnown==true and uint(c.health) and c.health>0 and uint(c.maxHealth) and c.maxHealth>0 and
        c.aliveKnown==true and c.alive==true and (c.npcFlags or 0)==0 and (c.unitFlags2 or 0)%2==0 and
        not bit(c.unitFlags,2) and not bit(c.unitFlags,0x80) and not bit(c.unitFlags,0x100) and not bit(c.unitFlags,0x10000) and
        not bit(c.unitFlags,0x2000000) and not bit(c.unitFlags,0x80000000)
end
local function unclaimed(c)
    return not bit(c.unitFlags,0x80000) and (c.dynamicFlags or 0)==0 and c.attackVictimKnown~=true and (c.targetKnown~=true or (c.targetGuid or "0")=="0")
end
M.practice_pull={minimum_health=95,priest_mana=45,mage_mana=40,minimum_energy=90,max_range=120,lease_ms=600000,prefer_nearest=true,target_entry=299,target_level=1}
function M.practice_lifecycle(input)
    local p=type(input)=="table" and type(input.practice)=="table" and input.practice or {}
    local function out(kind,phase,target,quest,finishing,lease)
        return {kind=kind,phase=phase or "",targetGuid=target or "0",questGoal=quest==true,finishing=finishing==true,leaseMs=lease or 0}
    end
    local refuse=out("stop","own_state_unknown_or_unsafe","0",p.questGoal)
    if type(input)~="table" or type(input.sessionId)~="string" or input.sessionId=="" or not decimal(input.selfGuid) or input.selfGuid=="0" or
       not decimal(input.visitId) or not number(input.nowMs) or not array(input.nearby,32) or not point(input.clientPosition) or
       type(input.self)~="table" or input.self.guid~=input.selfGuid or input.self.classKnown~=true or input.self.levelKnown~=true or
       not uint(input.self.level) or input.self.level<1 or not decimal(p.targetGuid) or type(p.questGoal)~="boolean" or type(p.pullStarted)~="boolean" then return refuse end
    local self,tuning=input.self,M.practice_pull
    if self.classId~=4 and self.classId~=5 and self.classId~=8 then return refuse end
    if type(tuning)~="table" or type(tuning.prefer_nearest)~="boolean" then return refuse end
    for _,key in ipairs({"minimum_health","priest_mana","mage_mana","minimum_energy","max_range","lease_ms","target_entry","target_level"}) do if not uint(tuning[key]) then return refuse end end
    if tuning.target_entry==0 or tuning.target_level==0 or tuning.minimum_health>100 or tuning.priest_mana>100 or tuning.mage_mana>100 or tuning.max_range>240 or tuning.lease_ms==0 or tuning.lease_ms>600000 then return refuse end
    local rows=quest_rows(input)
    local q=rows and rows[33]
    local active,needed=active_quest(q),incomplete(q)
    local quest=p.questGoal or active
    if input.stage=="start" then
        if self.level>5 or not northshire(self) then return refuse end
        if not needed and (active or self.level~=1 or self.classId==5) then return out("stop","own_quest_33_absent_or_unknown","0",quest) end
        return out("start","waiting_for_safe_pull","0",needed,false,tuning.lease_ms)
    end
    if not number(p.remainingMs) or p.remainingMs<=0 then return out("stop","goal_expired","0",quest) end
    -- The same accepted scenario owns unexpected encounter admission at the
    -- initial decision and every native pre-action lifecycle recheck.
    for _,other in ipairs(input.nearby) do
        if type(other)=="table" and other.entry~=tuning.target_entry and other.present==true and other.aliveKnown==true and other.alive==true and
           other.attackVictimKnown==true and other.attackVictimGuid==input.selfGuid and recent_age(other.attackAgeMs or 0) then
            return out("stop","unexpected_hostile_requires_help","0",quest)
        end
    end
    local target=input.selected
    local goal=needed and self.level<=5 or not active and self.level==1 and self.classId~=5 and not p.questGoal
    local finishing=not goal and practice_target(target,tuning) and owned_fight(input,target)
    if not goal and not finishing then
        local phase="level_two_observed"
        if quest then phase=not active and "own_quest_33_absent_or_unknown" or needed and "quest_33_level_limit" or "quest_33_completion_observed" end
        return out("stop",phase,"0",quest)
    end
    if p.targetGuid~="0" and (type(target)~="table" or target.guid~=p.targetGuid) then return out("stop","selected_target_observation_unavailable","0",quest) end
    if type(target)=="table" and target.healthKnown==true and (target.health or 0)==0 and target.aliveKnown==true and target.alive~=true then
        return out("target_dead","target_death_observed",p.targetGuid,quest)
    end
    local phase=finishing and (quest and "finishing_current_fight_to_goal" or "finishing_current_fight_to_level2") or "fighting_observed_young_wolf"
    if p.targetGuid~="0" and input.stage~="pull" then return out("continue",phase,p.targetGuid,quest,finishing) end
    if bit(self.unitFlags,0x80000) then return out(input.stage=="pull" and "stop" or "wait",input.stage=="pull" and "pull_prerequisites_changed" or "waiting_out_of_combat",p.targetGuid,quest) end
    local ready=self.healthKnown==true and uint(self.health) and uint(self.maxHealth) and self.maxHealth>0 and self.health*100>=self.maxHealth*tuning.minimum_health
    if self.classId==4 then ready=ready and self.powerKnown==true and self.powerType==3 and uint(self.power) and self.power>=tuning.minimum_energy
    else
        local pool=self.baseManaKnown==true and (self.baseMana or 0)>0 and self.baseMana or self.maxPower
        local percent=self.classId==5 and tuning.priest_mana or tuning.mage_mana
        ready=ready and self.powerKnown==true and (self.powerType or 0)==0 and uint(self.power) and uint(pool) and self.power>=math.floor((pool*percent+99)/100)
    end
    if not ready then return out(input.stage=="pull" and "stop" or "wait",input.stage=="pull" and "pull_prerequisites_changed" or "recovering_before_next_pull",p.targetGuid,quest) end
    if input.stage=="pull" then return out("pull",phase,p.targetGuid,quest,finishing) end
    if input.stage~="tick" then return refuse end
    local best,selected=tuning.max_range*tuning.max_range,nil
    for _,c in ipairs(input.nearby) do
        local p=type(c)=="table" and motion_point(c)
        if p and northshire(p) then
            local d=distance2(input.clientPosition,p)
            -- A tied or worse row cannot replace the already valid winner.
            -- Validate identity/eligibility only for an actual improvement;
            -- every row and its geometry still participates in the choice.
            if d<tuning.max_range*tuning.max_range and (not selected or tuning.prefer_nearest and d<best or not tuning.prefer_nearest and d>best) and practice_target(c,tuning) and unclaimed(c) then best,selected=d,c end
        end
    end
    return out(selected and "select" or "wait",selected and "fighting_observed_young_wolf" or "waiting_for_observed_level_one_young_wolf",selected and selected.guid or "0",quest)
end

M.coldridge_practice={priority={179,170,183,182},max_range=120,lease_ms=600000,
    objectives={[179]={{entry=705,count=0}},[170]={{entry=707,count=6},{entry=724,count=6}},[183]={{entry=708,count=12}},[182]={{entry=706,count=10}}},
    profiles={[179]={{entry=705,level=1}},[170]={{entry=707,level=2,maxHealth=55,faction=36},{entry=724,level=2,maxHealth=55,faction=36}},
        [183]={{entry=708,level=3,maxHealth=71,faction=189}},[182]={{entry=706,level=3,maxHealth=71,faction=190},{entry=706,level=4,maxHealth=86,faction=190}}}}
-- Defaults retain the recorded public creature tuples. They describe this
-- scenario's filter, never alter the actual native creature/quest observations.
-- Quest182 proof SHA256:84e76d3570162b8fabc05a98bd3cf8d7991293d3d433fb6d18a740cd628821bb.
-- Quest170 tuples SHA256:f63a45043ec5a13631fefb6a57a6f7ad8d1e3cdf2d44d7730c1533da3e64dcad;
-- quest183 tuple SHA256:e6cc24cbbe8df941fecc0e65d76a1dc86fab2e3907980d2803d1197437432409.
function M.coldridge_supports_entry(id,entry)
    local profiles=type(M.coldridge_practice)=="table" and type(M.coldridge_practice.profiles)=="table" and M.coldridge_practice.profiles[id]
    if not array(profiles,4) then return false end
    for _,profile in ipairs(profiles) do if type(profile)=="table" and profile.entry==entry and uint(entry) and entry>0 then return true end end
    return false
end
function M.coldridge_profile(c,id)
    if type(c)~="table" or c.mapKnown~=true or (c.mapId or 0)~=0 or c.levelKnown~=true or not uint(c.level) then return false end
    local profiles=type(M.coldridge_practice)=="table" and type(M.coldridge_practice.profiles)=="table" and M.coldridge_practice.profiles[id]
    if not array(profiles,4) then return false end
    for _,profile in ipairs(profiles) do
        if type(profile)~="table" or not uint(profile.entry) or profile.entry==0 or not uint(profile.level) or profile.level==0 or
           profile.maxHealth~=nil and (not uint(profile.maxHealth) or profile.maxHealth==0) or profile.faction~=nil and not uint(profile.faction) then return false end
        if c.entry==profile.entry and c.level==profile.level and (profile.maxHealth==nil or c.maxHealth==profile.maxHealth) and
           (profile.faction==nil or c.factionTemplate==profile.faction) then return true end
    end
    return false
end
function M.coldridge_eligible(c,self,id)
    return type(c)=="table" and c.physicalAvailable==true and M.coldridge_profile(c,id) and
        (id==179 or type(self)=="table" and self.mapKnown==true and (self.mapId or 0)==0 and self.levelKnown==true and self.level>=c.level)
end
function M.coldridge_lifecycle(input)
    local g=type(input)=="table" and type(input.goal)=="table" and input.goal or {}
    local function out(kind,phase,quest,counts,active,needed,entries,target,finishing,lease,valid)
        return {kind=kind,phase=phase or "",questId=quest or 0,counts=counts or {0,0},active=active==true,needed=needed==true,neededEntries=entries or {},
            targetGuid=target or "0",finishing=finishing==true,leaseMs=lease or 0,valid=valid~=false}
    end
    local bad=out("wait","coldridge_goal_policy_invalid",g.questId,g.counts,false,false,{},"0",false,0,false)
    if type(input)~="table" or type(input.self)~="table" or not decimal(input.selfGuid) or input.selfGuid=="0" or input.self.guid~=input.selfGuid or
       input.self.present~=true or input.self.classKnown~=true or input.self.classId~=9 or input.self.levelKnown~=true or not uint(input.self.level) or
       not uint(g.questId or 0) or not array(g.counts,2) or #g.counts~=2 or not uint(g.counts[1]) or not uint(g.counts[2]) or
       not array(input.nearby,32) or not point(input.clientPosition) or not decimal(input.targetGuid) then return bad end
    local tuning=M.coldridge_practice
    if type(tuning)~="table" or not array(tuning.priority,4) or type(tuning.objectives)~="table" or not uint(tuning.max_range) or tuning.max_range>240 or
       not uint(tuning.lease_ms) or tuning.lease_ms==0 or tuning.lease_ms>600000 then return bad end
    local rows=quest_rows(input)
    if not rows then return bad end
    local function observe(id,prior)
        local q=rows[id]
        local minimum=({[179]=1,[170]=2,[183]=3,[182]=4})[id]
        if not minimum or input.self.level<minimum or input.self.level>(id==182 and 6 or 5) or not active_quest(q) then return end
        local counts={0,0}
        if id~=179 then
            local raw=q.objectiveCounts
            if not array(raw,4) or #raw~=4 then return end
            for i=1,4 do if not uint(raw[i]) then return end end
            local maximum=id==170 and 6 or id==183 and 12 or 10
            if raw[1]>maximum or raw[2]>(id==170 and 6 or 0) or raw[3]~=0 or raw[4]~=0 or raw[1]<prior[1] or raw[2]<prior[2] then return end
            counts={raw[1],raw[2]}
        end
        local needed=incomplete(q)
        local entries={}
        local objectives=tuning.objectives[id]
        if not array(objectives,2) or #objectives==0 then return end
        for i,o in ipairs(objectives) do
            if type(o)~="table" or not uint(o.entry) or not uint(o.count) or not M.coldridge_supports_entry(id,o.entry) then return end
            if needed and (id==179 or counts[i]<o.count) then entries[#entries+1]=o.entry end
        end
        return counts,#entries>0,entries
    end
    local id=g.questId or 0
    if input.stage=="start" then
        for _,q in ipairs(tuning.priority) do
            local counts,needed,entries=observe(q,{0,0})
            if needed then return out("start","waiting_for_safe_coldridge_pull",q,counts,true,true,entries,"0",false,tuning.lease_ms) end
        end
        return out("stop","coldridge_goal_unavailable",0,{0,0},false,false)
    end
    local counts,needed,entries=observe(id,g.counts)
    if not counts then
        if input.stage=="observe" then return out("continue","",id,g.counts,false,false) end
        return out("stop",id==179 and "quest_179_complete_absent_or_level_limit" or "quest_"..tostring(id).."_complete_absent_or_invalid",id,g.counts,false,false)
    end
    if input.stage=="observe" then return out("continue","",id,counts,true,needed,entries) end
    local target=input.selected
    local targetNeeded=false
    for _,entry in ipairs(entries) do if type(target)=="table" and target.entry==entry then targetNeeded=true end end
    local finishing=(not needed or not targetNeeded) and input.pullStarted==true and bit(input.self.unitFlags,0x80000) and type(target)=="table" and
        M.coldridge_eligible(target,input.self,id) and target.attackVictimKnown==true and target.attackVictimGuid==input.selfGuid and recent_age(target.attackAgeMs or 0)
    if not needed and not finishing then return out("stop",id==179 and "quest_179_complete_absent_or_level_limit" or "quest_"..tostring(id).."_complete_absent_or_invalid",id,counts,true,false,entries) end
    if needed and not targetNeeded and input.pullStarted==true and not finishing then return out("stop","selected_quest_objective_complete_without_owned_fight",id,counts,true,needed,entries) end
    if type(target)=="table" and target.healthKnown==true and (target.health or 0)==0 and target.aliveKnown==true and target.alive~=true then return out("target_dead","target_death_observed",id,counts,true,needed,entries) end
    if type(target)=="table" and not M.coldridge_eligible(target,input.self,id) then
        return out("stop","coldridge_target_claimed_unsafe_or_cast_failed",id,counts,true,needed,entries)
    end
    if type(target)=="table" and input.pullStarted~=true and not targetNeeded then return out("clear_target","selected_quest_objective_already_satisfied",id,counts,true,needed,entries) end
    if input.stage=="tick" then return out("continue",finishing and "finishing_current_fight_to_goal" or "",id,counts,true,needed,entries,input.targetGuid,finishing) end
    if input.stage~="targets" then return bad end
    local selected,best=nil,tuning.max_range*tuning.max_range
    for _,c in ipairs(input.nearby) do
        local neededEntry=false
        for _,entry in ipairs(entries) do if type(c)=="table" and c.entry==entry then neededEntry=true end end
        local p=type(c)=="table" and motion_point(c)
        if neededEntry and M.coldridge_eligible(c,input.self,id) and c.positionWithin==true and c.unclaimed==true and p then
            local d=distance2(input.clientPosition,p)
            if d<best then selected,best=c,d end
        end
    end
    local phase=id==179 and "waiting_for_observed_level_one_ragged_young_wolf" or "waiting_for_observed_required_quest_"..tostring(id).."_target"
    return out(selected and "select" or "wait",phase,id,counts,true,needed,entries,selected and selected.guid or "0",finishing)
end
local function wait(phase) return {kind="wait",phase=phase} end
local function ready(choice) return {status="ready",choice=choice} end
local function invalid() return {status="invalid"} end
local function fargodeep_resources(players,tuning,mana_pool)
    if type(players)~="table" or type(tuning)~="table" or not uint(tuning.minimum_health) or tuning.minimum_health>100 or
       not uint(tuning.minimum_mana) or tuning.minimum_mana>100 or not uint(tuning.minimum_energy) then return false end
    for _,member in ipairs({{guid="1",class=5},{guid="4",class=8},{guid="5",class=4}}) do
        local player=players[member.guid]
        if type(player)~="table" or player.guid~=member.guid or player.classKnown~=true or player.classId~=member.class or
           player.healthKnown~=true or not uint(player.health) or not uint(player.maxHealth) or
           player.health==0 or player.maxHealth==0 or player.health>player.maxHealth or
           player.powerKnown~=true or not uint(player.power) or not uint(player.maxPower) or
           player.maxPower==0 or player.power>player.maxPower or type(player.baseManaKnown)~="boolean" or
           not uint(player.baseMana) then return false end
        if player.health*100<player.maxHealth*tuning.minimum_health then return false end
        if member.class==4 then
            if player.powerType~=3 or player.power<tuning.minimum_energy then return false end
        else
            if player.powerType~=0 then return false end
            local pool=mana_pool~="maximum" and player.baseManaKnown and player.baseMana>0 and player.baseMana or player.maxPower
            if player.power<math.floor((pool*tuning.minimum_mana+99)/100) then return false end
        end
    end
    return true
end
local function fargodeep_readiness(input)
    local selection="fargodeep_pull_readiness"
    local hold={kind="wait",spellId=0,selection=selection,standYards=0}
    local function uint(value)
        return type(value)=="number" and value>=0 and value<=4294967295 and value%1==0
    end
    local function session(value) return type(value)=="string" and #value>0 and #value<=128 end
    if type(input)~="table" or not session(input.sessionId) or
       (input.selfGuid~="1" and input.selfGuid~="4" and input.selfGuid~="5") or
       type(input.autoAttackKnown)~="boolean" then return hold end
    local obs,mission=input.observation,input.mission
    if type(obs)~="table" or obs.sessionId~=input.sessionId or obs.selfGuid~=input.selfGuid or
       not decimal(obs.visitId) or type(obs.players)~="table" or
       type(mission)~="table" or not session(mission.partySessionId) or
       not decimal(mission.targetGuid) or mission.targetGuid=="0" or
       (mission.engagerGuid~="1" and mission.engagerGuid~="4" and mission.engagerGuid~="5") then return hold end
    local tuning=M.fargodeep_pull
    if type(tuning)~="table" or not uint(tuning.minimum_health) or tuning.minimum_health>100 or
       not uint(tuning.minimum_mana) or tuning.minimum_mana>100 or not uint(tuning.minimum_energy) or
       type(tuning.stand_yards)~="table" then return hold end
    local self=obs.players[input.selfGuid]
    local stand=type(self)=="table" and self.classKnown==true and tuning.stand_yards[self.classId]
    if not number(stand) or stand<=0 or stand>240 then return hold end
    hold.standYards=stand
    if not fargodeep_resources(obs.players,tuning) then return hold end
    return {kind="ready",spellId=0,selection=selection,standYards=stand}
end

-- The accepted policy owns the whole explicit encounter's goal, roles and
-- lifecycle. The native caller supplies actual views and executes only the
-- returned physical step; a new observation is required across every callback.
function M.fargodeep_lifecycle(input)
    local w=type(input)=="table" and input.workflow
    local m=type(input)=="table" and input.mission
    local stand,budget=0,0
    local function out(kind,phase,pulled)
        return {kind=kind,phase=phase,selection="fargodeep_pull_readiness",standYards=stand,pulled=pulled==true,validForMs=math.floor(budget)}
    end
    local function future(ms) if number(ms) and ms>=0 then budget=math.min(budget,ms) end end
    local function motion_expiry(c)
        local t=c.trajectory
        if c.motionPositionKnown==true and type(t)=="table" and t.known==true and t.stopped~=true then
            local age=tonumber(t.ageMs or "0")
            if age<(t.durationMs or 0) then future(750-math.max(age,tonumber(c.positionAgeMs or "0")))
            else future((t.durationMs or 0)+5000-age) end
        end
    end
    local function stop(phase) return out("stop",phase,m and m.pulled) end
    local function area(p)
        return point(p) and (p.x or 0)>=-10000 and (p.x or 0)<=-9700 and (p.y or 0)>=50 and
            (p.y or 0)<=320 and (p.z or 0)>=-10 and (p.z or 0)<=65
    end
    if type(w)~="table" or type(m)~="table" or not decimal(m.targetGuid) or m.targetGuid=="0" or
       type(m.pulled)~="boolean" or (m.engagerGuid~="1" and m.engagerGuid~="4" and m.engagerGuid~="5") or
       type(w.scene)~="table" or not uint(w.nearbyPlayerCount) or w.nearbyPlayerCount>64 or not array(w.scene.nearbyCreatures or {},32) or
       not array(w.scene.selfQuestLog or {},25) or type(w.selfSafe)~="boolean" then return stop("observations_unavailable") end
    local scene,target=w.scene,w.scene.requestedCreature
    local self=((input.observation or {}).players or {})[input.selfGuid]
    local classes={["1"]=5,["4"]=8,["5"]=4}
    if not classes[input.selfGuid] or type(self)~="table" or self.guid~=input.selfGuid or not w.selfSafe or
       self.mapKnown~=true or (self.mapId or 0)~=0 or self.classKnown~=true or self.classId~=classes[input.selfGuid] or
       self.levelKnown~=true or not uint(self.level) or self.level<4 or self.level>10 or
       not uint(self.health) or not uint(self.maxHealth) or self.maxHealth==0 or self.health>self.maxHealth or
       self.speedsKnown~=true or not number(self.runSpeed) or math.abs(self.runSpeed-7)>.01 or
       not area(w.clientPosition) then return stop("own_state_unknown_or_unsafe") end
    local readiness=fargodeep_readiness(input)
    stand=readiness.standYards
    if not number(stand) or stand<=0 or stand>240 then return stop("readiness_policy_unavailable_or_changed") end
    if not uint(M.fargodeep_pull.recheck_ms) or M.fargodeep_pull.recheck_ms>30000 then return stop("readiness_policy_unavailable_or_changed") end
    budget=M.fargodeep_pull.recheck_ms
    local rows=quest_rows({questLogKnown=scene.selfQuestLogKnown,questLog=scene.selfQuestLog})
    local goals=ids(M.fargodeep_pull.quests,25)
    if not goals or not next(goals) then return stop("readiness_policy_unavailable_or_changed") end
    local active,needed=false,false
    if rows then for q in pairs(goals) do
        active=active or active_quest(rows[tonumber(q)])
        needed=needed or incomplete(rows[tonumber(q)])
    end end
    if type(target)~="table" or target.guid~=m.targetGuid then return stop("target_unsafe_or_cast_failed") end
    if target.aliveKnown==true and target.alive~=true and target.healthKnown==true and (target.health or 0)==0 and w.stage~="arm" then
        return stop("target_death_observed")
    end
    if not active or not m.pulled and m.engagerGuid==input.selfGuid and not needed then return stop("own_quest_goal_complete_or_unknown") end
    if w.targetGuidType~=0xf130 or w.targetGuidEntry~=target.entry or (target.entry~=40 and target.entry~=475) or
       target.present~=true or target.mapKnown~=true or (target.mapId or 0)~=0 or target.levelKnown~=true or
       not uint(target.level) or target.level<5 or target.level>7 or target.level>self.level+1 or
       target.aliveKnown~=true or target.alive~=true or target.healthKnown~=true or not uint(target.health) or
       not uint(target.maxHealth) or target.health==0 or target.health>target.maxHealth or (target.npcFlags or 0)~=0 or
       target.unitFlagsKnown~=true or target.dynamicFlagsKnown~=true or bit(target.unitFlags2,1) or bit(target.dynamicFlags,0x20) or
       w.failedTarget==true then return stop("target_unsafe_or_cast_failed") end
    for _,mask in ipairs({2,0x80,0x100,0x10000,0x2000000,0x80000000}) do
        if bit(target.unitFlags,mask) then return stop("target_unsafe_or_cast_failed") end
    end
    local p=w.targetPosition
    if not area(p) or distance2(w.clientPosition,p)>35*35 or
       w.targetPositionKnown~=true and w.targetPatrol~=true then
        return stop("target_position_unavailable_or_outside_range")
    end
    if w.stage=="arm" and w.targetPositionKnown~=true then
        local t=target.trajectory or {}
        local endpoint={x=t.destinationX,y=t.destinationY,z=t.destinationZ}
        if w.targetPatrol~=true or not point(endpoint) or distance2(w.clientPosition,endpoint)>35*35 then
            return stop("target_position_unavailable_or_outside_range")
        end
    end
    motion_expiry(target)
    for _,other in ipairs(scene.nearbyCreatures or {}) do motion_expiry(other) end
    local party=w.party
    local group=type(party)=="table" and party.group
    if type(group)~="table" or party.sessionId~=m.partySessionId or group.known~=true or group.inGroup~=true or
       not array(group.members,40) or #group.members<3 then return stop("current_party_unknown_or_changed") end
    if scene.owner and scene.owner.present==true then return stop("owner_returned") end
    local members={}
    for _,member in ipairs(group.members) do
        if type(member)~="table" or not decimal(member.guid) or member.guid=="0" or #member.guid>15 or
           #member.guid==15 and member.guid>"281474976710655" or members[member.guid]~=nil or
           (member.self==true)~=(member.guid==input.selfGuid) then return stop("current_party_unknown_or_changed") end
        members[member.guid]=member.online==true
    end
    local full=(input.observation or {}).players or {}
    for guid,class in pairs(classes) do
        local v=full[guid]
        if members[guid]~=true or type(v)~="table" or w.safePlayers[guid]~=true or v.classKnown~=true or v.classId~=class or
           v.levelKnown~=true or not uint(v.level) or v.level<4 or v.level>10 or not uint(v.maxHealth) or v.maxHealth==0 or
           not uint(v.health) or v.health>v.maxHealth or bit(v.playerFlags,2) then return stop("companion_observation_unknown_or_unsafe") end
        local pos=guid==input.selfGuid and w.clientPosition or v
        if distance2(w.clientPosition,pos)>35*35 then return stop("companions_outside_support_range") end
    end
    -- Extra actual group members do not acquire a role in this explicit task.
    members={["1"]=true,["4"]=true,["5"]=true}
    if w.stage=="arm" then return out("arm","armed_waiting_for_designated_engager",false) end
    if w.authorized~=true then return stop("authorization_expired") end
    local victim=target.attackVictimKnown==true and target.attackVictimGuid or "0"
    if victim~="0" and not members[victim] or bit(target.dynamicFlags,4) and not bit(target.dynamicFlags,8) then
        return stop("target_claimed_or_attacking_foreign_player")
    end
    for _,other in ipairs(scene.nearbyCreatures or {}) do
        if other.guid~=m.targetGuid and other.present==true and other.aliveKnown==true and other.alive==true and
           other.attackVictimKnown==true and members[other.attackVictimGuid] and recent_age(other.attackAgeMs or "0") then
            return stop("additional_attacker_observed")
        end
    end
    if w.targetPositionKnown~=true then return out("hold","waiting_for_fresh_patrol_position",m.pulled) end
    local age=tonumber(target.attackAgeMs or "0")
    local engaged=members[victim]==true and age and age<=30000 and full[victim] and (bit(full[victim].unitFlags,0x80000) or age<=5000)
    if engaged then future((bit(full[victim].unitFlags,0x80000) and 30000 or 5000)-age) end
    local pulled=m.pulled or engaged
    if not engaged and m.engagerGuid~=input.selfGuid then return out("hold","waiting_for_actual_party_attack_start",pulled) end
    if not engaged and not pulled then
        for guid,class in pairs(classes) do
            local v=full[guid]
            local pos=guid==input.selfGuid and w.clientPosition or v
            if distance2(pos,p)>28*28 then return out("hold","companions_outside_support_range",pulled) end
            if v.healthKnown~=true or (v.health or 0)==0 or bit(v.unitFlags,0x80000) or v.powerKnown~=true or
               (class==4 and v.powerType~=3 or class~=4 and (v.powerType or 0)~=0) then
                return out("hold","companions_recovering_before_pull",pulled)
            end
        end
        if bit(target.unitFlags,0x80000) or (target.dynamicFlags or 0)~=0 or victim~="0" or
           target.targetKnown~=true or (target.targetGuid or "0")~="0" then return stop("initial_target_not_idle_unclaimed") end
        for _,other in ipairs(scene.nearbyCreatures or {}) do
            local pos=other.motionPositionKnown==true and (other.motionPosition or other) or nil
            local near=pos and distance2(pos,p)<8*8
            if not pos and other.patrolKnown==true then
                local motion=other.trajectory
                local age,duration=tonumber(motion.ageMs or "0"),motion.durationMs
                local endpoint={x=motion.destinationX,y=motion.destinationY,z=motion.destinationZ}
                near=math.sqrt(distance2(other,p))<8+7*age/1000 and math.sqrt(distance2(endpoint,p))<8+7*math.abs(duration-age)/1000
                local start_reach=(math.sqrt(distance2(other,p))-8)*1000/7
                local end_reach=(math.sqrt(distance2(endpoint,p))-8)*1000/7
                future(start_reach-age);future(duration-end_reach-age);future(duration+end_reach-age)
            elseif not pos then near=other.positionKnown==true and point(other) and distance2(other,p)<8*8 end
            if other.guid~=m.targetGuid and other.present==true and other.aliveKnown==true and other.alive==true and
               (other.npcFlags or 0)==0 and near then return out("hold","waiting_for_isolated_kobold",pulled) end
        end
        if readiness.kind~="ready" then return out("hold","companions_recovering_before_pull",pulled) end
    end
    if self.classId==5 and w.learnedHealing~=true then return stop("priest_healing_intent_required") end
    if w.stage=="tick" and w.castPending==true then return out("hold","waiting_for_actual_cast_result",pulled) end
    if w.stage=="tick" and w.autoAttackUnconfirmed==true then return out("hold","waiting_for_server_attack_stop",pulled) end
    if m.engagerGuid==input.selfGuid and (self.classId==4 and not pulled or distance2(w.clientPosition,p)>stand*stand) then
        if w.routeKnown~=true or not pulled and w.routeTargetDrift>.25 then return out("plan","waiting_for_verified_route",pulled) end
        if distance2(w.clientPosition,p)>stand*stand or w.routeRemaining>stand or w.routeDirect~=true then
            return out("approach","approaching_designated_kobold_on_verified_route",pulled)
        end
    end
    if not engaged and pulled then return out("hold","waiting_for_actual_party_attack_start",pulled) end
    return out("attack",engaged and "fighting_actual_bound_kobold" or "waiting_for_class_gate",pulled)
end
local function practice(input,active)
    local a=input.actions or {}
    if a.practicing==true then
        local lease=a.practiceLeaseRemainingMs
        if not decimal(lease) then return invalid() end
        if #lease>5 or #lease==5 and lease>"60000" then
            return ready(wait(active["33"] and "practicing_for_wolf_quest" or "practicing_to_level_2"))
        end
    end
    if not number(input.now) or not number(input.nextMutationAt) then return invalid() end
    if input.now<input.nextMutationAt then return ready(wait()) end
    return ready({kind="enable_practice",practiceOwned=true})
end
local function quest_route(input,active,missing,confirmed,known,level,complete)
    local r=input.questRoute
    if type(r)~="table" or type(r.sessionId)~="string" or #r.sessionId<1 or #r.sessionId>128 or
       type(r.introProbeCurrent)~="boolean" or type(r.eaganProbeCurrent)~="boolean" or
       type(r.wolfProbeCurrent)~="boolean" or type(r.givers)~="table" or #r.givers>128 or type(r.offer)~="table" then return invalid() end
    local o,giver,giverIndex,giverCount=r.offer,nil,0,0
    for index,row in pairs(r.givers) do
        if type(index)~="number" or index<1 or index>128 or index%1~=0 or
           type(row)~="table" or not decimal(row.guid) or row.guid=="0" or
           type(row.entry)~="number" or row.entry%1~=0 or row.entry<1 or row.entry>4294967295 then return invalid() end
        giverCount=giverCount+1
        if index>giverIndex and row.guid==o.npcGuid then giver,giverIndex=row.entry,index end
    end
    if giverCount~=#r.givers then return invalid() end
    if type(o.objectKnown)~="boolean" or type(o.questsKnown)~="boolean" or type(o.introQuestsKnown)~="boolean" or
       not decimal(o.npcGuid) or not decimal(o.expiresInMs) or type(o.quests)~="table" or #o.quests>32 then return invalid() end
    local intro,eagan,wolves,rest,offerCount=false,false,false,false,0
    for index,row in pairs(o.quests) do
        if type(index)~="number" or index<1 or index>32 or index%1~=0 or
           type(row)~="table" or type(row.questId)~="number" or row.questId%1~=0 or
           row.questId<0 or row.questId>4294967295 or type(row.available)~="boolean" then return invalid() end
        offerCount=offerCount+1
        if row.available then
            if row.questId==783 then intro=true
            elseif row.questId==5261 then eagan=true
            elseif row.questId==33 then wolves=true
            elseif row.questId==3903 then rest=true end
        end
    end
    if offerCount~=#o.quests then return invalid() end
    local reward=0
    if active["783"] then reward=783
    elseif active["5261"] then reward=5261
    elseif active["33"] and complete then reward=33 end
    local needed=823
    if reward~=0 then needed=reward==783 and 197 or 196
    elseif missing["7"] then needed=confirmed["783"] and 197 or 823
    elseif missing["3903"] and not confirmed["33"] and not r.wolfProbeCurrent and
           (confirmed["5261"] or r.eaganProbeCurrent) then needed=196 end
    local probes={}
    local fresh=o.objectKnown and o.expiresInMs~="0"
    if missing["7"] and not active["783"] then
        if fresh and giver==823 and o.introQuestsKnown and not intro then
            probes.intro=true
        end
        if r.introProbeCurrent or probes.intro then needed=197 end
    end
    if active["7"] and missing["3903"] and not confirmed["5261"] and not confirmed["33"] and
       not active["5261"] and not active["33"] and fresh and giver==823 and
       o.questsKnown and not eagan and not rest then
        probes.eagan,probes.wolf=true,false
        needed=196
    end
    -- An empty fresh Eagan menu permits another Willem query. It never
    -- manufactures reward confirmation or permission to accept an absent row.
    if reward==0 and missing["3903"] and not active["5261"] and not active["33"] and
       fresh and giver==196 and o.questsKnown and not wolves then
        probes.wolf=true
        needed=823
    end
    local allowed={}
    if needed==197 and missing["7"] then allowed={7}
    elseif needed==823 then
        if missing["7"] then allowed[#allowed+1]=783 end
        if known and level>=2 then
            if missing["18"] then allowed[#allowed+1]=18 end
            if missing["3903"] then allowed[#allowed+1]=3903 end
        end
        if missing["3903"] and not confirmed["5261"] and not confirmed["33"] then allowed[#allowed+1]=5261 end
    elseif needed==196 and missing["3903"] and not confirmed["33"] then allowed={33} end
    return ready({kind="quest_route",neededEntry=needed,rewardQuestId=reward,allowedQuestIds=allowed,probes=probes})
end
-- Cohort policy shares the explicit encounter's resource validator. The
-- stronger initial cohort gate deliberately retains the old 60% max-mana
-- admission rule; per-body continuation still uses 45% of known base mana.
local function cave_uint(v) return type(v)=="number" and v>=0 and v<=4294967295 and v%1==0 end
local function cave_bit(value,mask) return value%(mask*2)>=mask end
function M.cave_tuning()
    return {minimum_mana=60,mana_pool="maximum",engagers={"4","1","5"},prefer_nearest=true,
        maximum_range=28,isolation_yards=8,minimum_level=5,maximum_level=7,level_above_self=1,
        entries={[40]=true,[475]=true},idle_polls=3,loot_wait=2,loot_limit=35,
        search_legs=8,search_history=64,search_seconds=36,arrival=.75,station_floor=2,
        origin_yards=18,station_yards=28,reconnect_yards=60,preempted_yards=40,
        corpse_yards=12,corpse_floor=1.5,corpse_legs=3,corpse_range=60,
        route="33b3fc5d5667036167c668ae7ba69fc7f5cf6af57944790a46e185eb3f6de14d",
        stations={{-9821.469,215.5862,16.341442},{-9820.893,227.19437,24.214392},
            {-9826.351,237.92334,31.82744},{-9834.488,240.53362,26.891457},
            {-9839.6,230.13333,18.551823},{-9834.666,217.06573,14.587091},
            {-9828.954,204.60019,14.721489},{-9828.05,190.13332,13.459157},
            {-9828.8,176.71109,12.31047},{-9835.635,164.62372,5.3633475},
            {-9842.402,153.3405,5.521588},{-9849.066,140.4,6.935242},
            {-9843.54,127.525,5.370034}}}
end

local function cave_guid_less(a,b) return #a<#b or #a==#b and a<b end
local function cave_age(v)
    -- All admitted coordinates have abs <40000, and duration <=120000.
    -- At >=1e9ms both 7yd/s spheres exceed the entire coordinate cube;
    -- the original exact uint64 string remains in the factual frame.
    return #v>9 and 1000000000 or tonumber(v)
end
-- Creature tuples remain immutable after the complete validator. Derive a
-- point only where a selector consumes it; do not expand 99 flag maps eagerly.
local function cave_xyz(r) return {x=r[5],y=r[6],z=r[7]} end
local function cave_end(t) return {x=t[4],y=t[5],z=t[6]} end
local function cave_movement(r)
    local flags=r[18] or 0
    return flags<0x80000000 and flags%0x08000000<512
end
local function cave_area(p) return p.x>=-10000 and p.x<=-9700 and p.y>=50 and p.y<=320 and p.z>=-10 and p.z<=65 end
local function cave_patrol(r)
    local t=r[20]
    if r[3]%8<6 or not cave_movement(r) or not t or not cave_bit(t[1],1) or cave_bit(t[1],2) or
        cave_bit(t[1],4) or cave_bit(t[1],8) or not cave_bit(t[1],16) or t[3]<1 or t[3]>120000 then return false end
    local start,finish=cave_xyz(r),cave_end(t)
    return cave_area(start) and cave_area(finish) and distance2(start,finish)<=3600
end
local function cave_point(r,patrol)
    if r[3]%8~=7 or not cave_movement(r) then return end
    local t=r[20]
    if t and cave_bit(t[1],1) then
        if cave_bit(t[1],8) then return end
        if not cave_bit(t[1],2) then
            if not patrol then return end
            local age=cave_age(t[2])
            if age>=t[3] then return age-t[3]<=5000 and cave_end(t) or nil end
            if age>750 or cave_age(r[19] or "0")>750 then return end
        end
    else
        local flags=r[18] or 0
        if flags%256~=0 or cave_bit(flags,0x08000000) then return end
    end
    return cave_xyz(r)
end

local function cave_creature(r,id)
    if not array(r,20) or #r<10 or not id(r[1]) or not cave_uint(r[2]) or not cave_uint(r[3]) or r[3]>4095 or
        not cave_uint(r[4]) or not cave_uint(r[8]) or not cave_uint(r[9]) or not cave_uint(r[10]) or not cave_uint(r[11] or 0) or
        not cave_uint(r[12] or 0) or not cave_uint(r[13] or 0) or not cave_uint(r[14] or 0) or not cave_uint(r[18] or 0) or
        not number(r[5]) or math.abs(r[5])>=40000 or not number(r[6]) or math.abs(r[6])>=40000 or
        not number(r[7]) or math.abs(r[7])>=40000 or not id(r[15] or "0") or not id(r[16] or "0") or
        not id(r[17] or "0") or not id(r[19] or "0") then return end
    local t=r[20]
    if t and (not array(t,6) or #t~=6 or not cave_uint(t[1]) or t[1]>31 or not id(t[2]) or not cave_uint(t[3]) or
        not number(t[4]) or math.abs(t[4])>=40000 or not number(t[5]) or math.abs(t[5])>=40000 or
        not number(t[6]) or math.abs(t[6])>=40000) then return end
    return r
end

local function cave_scenes(input)
    if type(input)~="table" or input.format~=1 or input.selfGuid~="1" or type(input.actors)~="table" or not array(input.creatures,99) or
       type(input.task)~="table" then return end
    -- Each repeated immutable decimal is fully validated once in this decode;
    -- a different value, including a malformed late row, still runs the check.
    local checked={["0"]=true}
    local function id(v)
        if type(v)~="string" then return false end
        if checked[v] then return true end
        if not decimal(v) then return false end
        checked[v]=true;return true
    end
    local rows,scenes,players={},{},{}
    for i,row in ipairs(input.creatures) do
        rows[i]=cave_creature(row,id);if not rows[i] then return end
    end
    local count=0
    for guid in pairs(input.actors) do if guid~="1" and guid~="4" and guid~="5" then return end;count=count+1 end
    if count~=3 then return end
    for _,guid in ipairs({"1","4","5"}) do
        local v=input.actors[guid]
        if type(v)~="table" or not array(v.b,5) or #v.b~=5 or type(v.b[1])~="string" or #v.b[1]==0 or #v.b[1]>128 or
           not array(v.s,13) or #v.s~=13 or not array(v.p,6) or #v.p~=6 or not array(v.a,7) or #v.a~=7 or
           not array(v.l,5) or #v.l~=5 or not array(v.o,6) or #v.o~=6 or not array(v.i,3) or #v.i~=3 or
           not array(v.q,25) or #v.q~=25 or not array(v.c,32) or not array(v.g,40) or #v.g<3 or type(v.t)~="boolean" then return end
        for i=2,5 do if not id(v.b[i]) then return end end
        for _,n in ipairs(v.s) do if not cave_uint(n) then return end end
        for i=1,2 do if not cave_uint(v.p[i]) then return end end
        for i=3,5 do if not number(v.p[i]) or math.abs(v.p[i])>=40000 then return end end
        if not id(v.p[6]) or not cave_uint(v.a[1]) or not id(v.a[3]) or not cave_uint(v.a[4]) or
           not id(v.a[5]) or not id(v.a[6]) or type(v.a[2])~="string" or #v.a[2]>128 or type(v.a[7])~="string" or #v.a[7]>128 or
           not cave_uint(v.l[1]) or type(v.l[2])~="string" or #v.l[2]>128 or not id(v.l[3]) or not id(v.l[4]) or type(v.l[5])~="boolean" or
           type(v.o[1])~="boolean" or type(v.o[2])~="boolean" or not cave_uint(v.o[3]) or type(v.o[4])~="string" or #v.o[4]>128 or
           not cave_uint(v.o[5]) or type(v.o[6])~="boolean" then return end
        for _,n in ipairs(v.i) do if not number(n) or n<0 or n%1~=0 then return end end
        local s=v.s
        local player={guid=guid,present=cave_bit(s[1],1),aliveKnown=cave_bit(s[1],2),alive=cave_bit(s[1],4),healthKnown=cave_bit(s[1],8),
            classKnown=cave_bit(s[1],16),powerKnown=cave_bit(s[1],32),unitFlagsKnown=cave_bit(s[1],64),levelKnown=cave_bit(s[1],128),
            movementFlagsKnown=cave_bit(s[1],256),mapKnown=cave_bit(s[1],512),positionKnown=cave_bit(s[1],1024),baseManaKnown=cave_bit(s[1],2048),
            classId=s[2],level=s[3],health=s[4],maxHealth=s[5],powerType=s[6],power=s[7],maxPower=s[8],baseMana=s[9],
            unitFlags=s[10],movementFlags=s[11],extraMovementFlags=s[12],playerFlags=s[13]}
        local log,group,nearby={},{},{}
        for _,q in ipairs(v.q) do
            if not array(q,3) or #q~=3 or not cave_uint(q[1]) or not cave_uint(q[2]) or not cave_uint(q[3]) or q[3]>3 or q[1]~=0 and log[q[1]]~=nil then return end
            if q[1]~=0 then log[q[1]]=cave_bit(q[3],1) and cave_bit(q[2],1) end
        end
        for _,m in ipairs(v.g) do
            if not array(m,2) or #m~=2 or not id(m[1]) or m[1]=="0" or group[m[1]]~=nil or not cave_uint(m[2]) or m[2]>3 or
               cave_bit(m[2],2)~=(m[1]==guid) then return end
            group[m[1]]=cave_bit(m[2],1)
        end
        for _,g in ipairs({"1","4","5"}) do if group[g]~=true then return end end
        for _,ref in ipairs(v.c) do if not cave_uint(ref) or not rows[ref] then return end;nearby[#nearby+1]=rows[ref] end
        if not cave_uint(v.r) or v.r~=0 and not rows[v.r] then return end
        scenes[guid]={raw=v,self=player,log=log,nearby=nearby,target=rows[v.r],group=v.b[5],
            position={x=v.p[3],y=v.p[4],z=v.p[5]},farg={active=cave_bit(v.a[4],1),target=v.a[5],engager=v.a[6],phase=v.a[7]}}
        players[guid]=player
    end
    if scenes["1"].raw.b[1]~=input.sessionId or scenes["1"].group~=scenes["4"].group or scenes["1"].group~=scenes["5"].group then return end
    return scenes,players
end

local function cave_admission(scenes,players,tuning,row,search,clock)
    if not fargodeep_resources(players,{minimum_health=M.fargodeep_pull.minimum_health,
        minimum_mana=tuning.minimum_mana,minimum_energy=M.fargodeep_pull.minimum_energy},tuning.mana_pool) then return false end
    for guid,s in pairs(scenes) do
        local v,p,a=s.raw,s.self,s.raw.a
        local own_loot=clock and row and v.l[3]==row.target
        if not p.present or not p.aliveKnown or not p.alive or not p.unitFlagsKnown or cave_bit(p.unitFlags,0x80000) or not clock and v.i[3]==0 or v.b[3]=="0" or not cave_bit(a[1],4) or
           cave_bit(a[1],1) or cave_bit(a[1],8) or cave_bit(a[1],64) or cave_bit(a[1],128) or cave_bit(a[1],256) or
           not own_loot and (not search or not v.t) and (cave_bit(a[1],16) or cave_bit(a[1],32) or a[2]~="") or
           own_loot and a[2]~="" and a[2]~="loot" or
           v.l[1]~=1 or not own_loot and (v.l[2]~="idle" or v.l[3]~="0") or v.l[4]~="0" or
           s.farg.active and (not row or s.farg.target~=row.target or s.farg.engager~=row.engager) or
           v.o[1] or not v.o[2] or v.o[3]~=0 or cave_bit(v.o[5],2) or
           v.o[4]~="" and (not ({COMPLETED=true,SERVER_REJECTED=true,BLOCKED=true,CANCELLED=true})[v.o[4]] or not cave_bit(v.o[5],1)) then return false end
        if search then
            if v.o[6] or not p.movementFlagsKnown or p.extraMovementFlags~=0 or cave_bit(a[1],512) or cave_bit(a[1],1024) or s.farg.active then return false end
            for _,mask in ipairs({0x1,0x4,0x40000,0x100000,0x400000,0x800000,0x1000000,0x2000000,0x8000000}) do if cave_bit(p.unitFlags,mask) then return false end end
            if p.movementFlags>=0x80000000 or p.movementFlags%0x10000000>=512 then return false end
            if v.t and (a[2]~="exploration" or not cave_bit(a[1],2048) or a[3]=="0") then return false end
            for _,c in ipairs(s.nearby) do if cave_bit(c[3],1) and cave_bit(c[3],16) and cave_bit(c[3],1024) and
                (c[16]=="1" or c[16]=="3" or c[16]=="4" or c[16]=="5") and cave_age(c[17] or "0")<=5000 then return false end end
        end
    end
    return cave_bit(scenes["1"].raw.a[1],2)
end

local function cave_candidate(scenes,tuning,attempted)
    local seen,candidates,points,patrols={},{},{},{}
    local function current(c)
        if points[c]==nil then
            patrols[c]=cave_patrol(c);points[c]=cave_point(c,patrols[c]) or false
        end
        return points[c]
    end
    for _,guid in ipairs({"1","4","5"}) do
        for _,c in ipairs(scenes[guid].nearby) do
            seen[c[1]]=c
            local f,u=c[3],c[11] or 0
            -- Bits 0..9 prove the complete known/alive header; bit11 proves
            -- the exact encoded creature identity. Attack-victim bit10 is optional.
            local okay=not attempted[c[1]] and f>=2048 and f%1024==1023 and tuning.entries[c[2]]==true and
                c[8]>0 and c[8]==c[9] and c[10]>=tuning.minimum_level and c[10]<=tuning.maximum_level and c[4]==0 and
                (c[13] or 0)==0 and (c[14] or 0)==0 and (c[15] or "0")=="0" and
                not (cave_bit(f,1024) and (c[16] or "0")~="0") and (c[12] or 0)%2==0 and
                u<0x80000000 and u%4<2 and u%512<128 and u%0x20000<0x10000 and
                u%0x100000<0x80000 and u%0x4000000<0x2000000
            local p=okay and current(c)
            okay=okay and p
            if okay then
                for _,scene in pairs(scenes) do
                    if not scene.self.levelKnown or c[10]>scene.self.level+tuning.level_above_self or
                        distance2(p,scene.position)>tuning.maximum_range^2 then okay=false;break end
                end
            end
            if okay then candidates[c[1]]={creature=c,position=p} end
        end
    end
    local best,score
    for guid,candidate in pairs(candidates) do
        local isolated=true
        for other,c in pairs(seen) do
            local f=c[3]
            if other~=guid and cave_bit(f,1) and f%32>=24 and (c[14] or 0)==0 and cave_bit(f,2) and c[4]==0 then
                local p=current(c)
                local near=p and distance2(p,candidate.position)<tuning.isolation_yards^2
                if not p and patrols[c] then
                    local t=c[20];local age=cave_age(t[2])
                    near=distance2(cave_xyz(c),candidate.position)<(tuning.isolation_yards+7*age/1000)^2 and
                        distance2(cave_end(t),candidate.position)<(tuning.isolation_yards+7*math.abs(t[3]-age)/1000)^2
                elseif not p then near=cave_bit(f,4) and distance2(cave_xyz(c),candidate.position)<tuning.isolation_yards^2 end
                if near then isolated=false;break end
            end
        end
        if isolated then
            local rank=0
            for _,scene in pairs(scenes) do rank=math.max(rank,distance2(candidate.position,scene.position)) end
            if not best or tuning.prefer_nearest and rank<score or tuning.prefer_nearest==false and rank>score or
                rank==score and cave_guid_less(guid,best.guid) then best,score={guid=guid,entry=candidate.creature[2]},rank end
        end
    end
    return best
end

local function cave_goal_complete(scenes)
    for _,s in pairs(scenes) do
        for _,qid in ipairs(M.fargodeep_pull.quests) do if s.log[qid]~=true then return false end end
    end
    return true
end

local function cave_order(scenes,tuning)
    if not array(tuning.engagers,3) or #tuning.engagers~=3 then return end
    local seen,engager={},nil
    for _,guid in ipairs(tuning.engagers) do
        if not scenes[guid] or seen[guid] then return end
        seen[guid]=true
        if not engager then
            for _,qid in ipairs(M.fargodeep_pull.quests) do if scenes[guid].log[qid]~=true then engager=guid;break end end
        end
    end
    if not engager then return end
    local order={}
    for _,guid in ipairs({"1","4","5"}) do if guid~=engager then order[#order+1]=guid end end
    order[3]=engager
    return engager,order
end

local function cave_corpse(scenes,row,tuning)
    local missing,near,lootable=false,true,false
    local goal
    for _,guid in ipairs({"1","4","5"}) do
        local scene=scenes[guid];local c=scene.target
        if not c then missing=true
        else
            local f,p=c[3],cave_xyz(c)
            if c[1]~=row.target or c[2]~=row.entry or not tuning.entries[c[2]] or not cave_bit(f,1) or
                not cave_bit(f,8) or cave_bit(f,16) or not cave_bit(f,32) or c[8]~=0 or not cave_bit(f,64) or
                c[10]<tuning.minimum_level or c[10]>tuning.maximum_level or not cave_bit(f,128) or
                not cave_bit(f,256) or not cave_bit(c[13] or 0,8) or not cave_bit(f,2) or c[4]~=0 or
                not cave_bit(f,4) or not cave_area(p) then return end
            if guid=="1" then goal=p end
            near=near and distance2(scene.position,p)<=tuning.corpse_yards^2 and math.abs(scene.position.z-p.z)<=tuning.corpse_floor
            lootable=lootable or cave_bit(c[13] or 0,1)
        end
    end
    return {missing=missing,near=near,lootable=lootable,goal=goal}
end

local function cave_search(scenes,task,tuning)
    if task.legs>=tuning.search_legs or (task.search.count or 0)>=tuning.search_history then return {kind="stop",phase="finite_corridor_search_limit"} end
    if task.remaining<tuning.search_seconds then return {kind="stop",phase="finite_corridor_search_time_remaining"} end
    local stations=tuning.stations
    if not array(stations,64) or #stations<2 then return end
    for _,p in ipairs(stations) do
        if not array(p,3) or #p~=3 then return end
        for _,n in ipairs(p) do if not number(n) or math.abs(n)>=40000 then return end end
    end
    local function station(i)
        local p=stations[i+1]
        return p and {x=p[1],y=p[2],z=p[3]} or nil
    end
    local function all_near(i,yards,floor)
        local p=station(i)
        if not p then return false end
        for _,s in pairs(scenes) do if distance2(s.position,p)>yards^2 or floor and math.abs(s.position.z-p.z)>floor then return false end end
        return true
    end
    local function nearest(yards,floor,prefer_floor)
        local best,rank,best_floor
        for i=0,#stations-1 do
            if all_near(i,yards,floor) then
                local p,d=station(i),0
                local same_floor=all_near(i,yards,tuning.station_floor)
                for _,s in pairs(scenes) do d=math.max(d,distance2(s.position,p)) end
                if not best or prefer_floor and same_floor and not best_floor or
                   (not prefer_floor or same_floor==best_floor) and (d<rank or d==rank and i<best) then
                    best,rank,best_floor=i,d,same_floor
                end
            end
        end
        return best
    end
    local saved=task.search
    local cursor,direction,reconnect,reanchor=saved.cursor,saved.direction or 1,nil,false
    if cursor==nil then
        cursor=nearest(tuning.origin_yards,tuning.station_floor)
        if cursor==nil then
            if task.corpseArrival~="0" then reconnect=nearest(tuning.reconnect_yards,nil,true) end
            if reconnect==nil then return {kind="stop",phase="corridor_not_near_current_party_floor"} end
            cursor,reanchor=reconnect,true
        end
    else
        if not cave_uint(cursor) or not station(cursor) or direction~=1 and direction~=-1 then return end
        if saved.phase=="preempted" then
            if saved.preempted~=true then return {kind="stop",phase="corridor_previous_leg_requires_reconciliation"} end
            if all_near(saved.station,tuning.arrival+.0001) then cursor=saved.station end
        elseif not all_near(cursor,tuning.station_yards,tuning.station_floor) then
            reconnect=nearest(tuning.reconnect_yards,nil,true)
            if reconnect==nil then return {kind="stop",phase="corridor_party_left_current_station_floor"} end
            reanchor=true
        end
    end
    local goal=cursor
    if all_near(cursor,tuning.arrival+.0001) then
        if cursor+direction<0 or cursor+direction>=#stations then direction=-direction end
        goal=cursor+direction
    end
    if saved.phase=="preempted" then
        if not all_near(saved.station,tuning.arrival+.0001) then goal=saved.station end
        if task.corpseArrival~="0" and not all_near(cursor,tuning.station_yards,tuning.station_floor) then
            reconnect=nearest(tuning.reconnect_yards,nil,true);if reconnect then reanchor=true end
        end
        if not reconnect and not all_near(goal,tuning.preempted_yards) then return {kind="stop",phase="corridor_preempted_party_left_bounded_goal"} end
    end
    if reconnect then
        goal=reconnect
        if cursor~=goal then direction=goal>cursor and 1 or -1 end
    end
    return {kind="search",station=goal,cursor=cursor,direction=direction,goal=station(goal),arrival=tuning.arrival,route=tuning.route,
        reanchor=reanchor,corpseTargetGuid=task.corpseArrival}
end

function M.cave_cohort(input)
    local scenes,players=cave_scenes(input)
    if not scenes then return invalid() end
    local tuning,task=M.cave_tuning(),input.task
    if type(tuning)~="table" or type(tuning.entries)~="table" or type(tuning.prefer_nearest)~="boolean" or
       not array(M.fargodeep_pull.quests,25) or not array(task.attempted,96) or type(task.controlsMatch)~="boolean" or
       not cave_uint(task.idle) or not cave_uint(task.legs) or not number(task.remaining) or task.remaining<0 or
       not decimal(task.corpseArrival) or type(task.search)~="table" then return invalid() end
    for _,key in ipairs({"minimum_mana","maximum_range","isolation_yards","minimum_level","maximum_level","level_above_self", "idle_polls",
        "loot_wait","loot_limit","search_legs","search_history","search_seconds","arrival","station_floor","origin_yards","station_yards","reconnect_yards","preempted_yards",
        "corpse_yards","corpse_floor","corpse_legs","corpse_range"}) do
        if not number(tuning[key]) or tuning[key]<0 then return invalid() end
    end
    local attempted={}
    for _,guid in ipairs(task.attempted) do if not decimal(guid) or guid=="0" then return invalid() end;attempted[guid]=true end
    local row=task.row
    if row and (type(row)~="table" or not decimal(row.target) or not cave_uint(row.entry) or not scenes[row.engager] or type(row.requests)~="table" or
        type(row.phase)~="string" or type(row.deadObserved)~="boolean") then return invalid() end
    if input.mode=="safe_pull" then return ready({kind="admission",allowed=cave_admission(scenes,players,tuning,row,false)}) end
    if input.mode=="safe_search" then return ready({kind="admission",allowed=task.controlsMatch and cave_admission(scenes,players,tuning,nil,true)}) end
    if input.mode=="corpse_clock" then
        return ready({kind="admission",allowed=row~=nil and task.controlsMatch and cave_admission(scenes,players,tuning,row,true,true)})
    end
    if input.mode=="corpse_plan" then
        if not row or not cave_uint(row.approachCount) then return invalid() end
        local corpse=cave_corpse(scenes,row,tuning)
        if not corpse then return ready({kind="stop",phase="bound_corpse_context_changed_no_retry"}) end
        if corpse.missing then return ready({kind="missing"}) end
        if corpse.near then return ready({kind="near"}) end
        if not corpse.lootable then return ready({kind="none"}) end
        if not task.controlsMatch or not cave_admission(scenes,players,tuning,nil,true) then
            return ready({kind="wait",phase="waiting_for_safe_idle_party_before_corpse_approach"})
        end
        if row.approachCount>=tuning.corpse_legs or task.remaining<tuning.search_seconds then
            return ready({kind="stop",phase="finite_bound_corpse_approach_limit"})
        end
        for _,s in pairs(scenes) do if distance2(s.position,corpse.goal)>tuning.corpse_range^2 then
            return ready({kind="stop",phase="finite_bound_corpse_approach_limit"}) end end
        return ready({kind="move",goal=corpse.goal,arrival=tuning.arrival})
    end
    if input.mode=="priority" then
        for _,guid in ipairs({"1","4","5"}) do
            local s=scenes[guid]
            if s.self.unitFlagsKnown and cave_bit(s.self.unitFlags,0x80000) then return ready({kind="actual_own_combat",observerGuid=guid}) end
            for _,c in ipairs(s.nearby) do
                if cave_bit(c[3],1) and c[3]%32>=24 and cave_bit(c[3],1024) and scenes[c[16]] and cave_age(c[17] or "0")<=5000 then
                    return ready({kind="actual_fresh_party_attacker",observerGuid=guid})
                end
            end
        end
        return ready({kind="none"})
    end
    if input.mode=="search" then
        if not task.controlsMatch or not cave_admission(scenes,players,tuning,nil,true) then return ready({kind="wait",phase="waiting_for_safe_idle_corridor_search"}) end
        local result=cave_search(scenes,task,tuning)
        return result and ready(result) or invalid()
    end
    if input.mode=="search_arrival" then
        local saved=task.search
        if not cave_uint(saved.station) or not array(tuning.stations,64) or not tuning.stations[saved.station+1] or
            saved.direction~=1 and saved.direction~=-1 then return invalid() end
        local direction=saved.direction
        if saved.station~=saved.cursor then direction=saved.station>saved.cursor and 1 or -1 end
        return ready({kind="arrival",direction=direction})
    end
    if input.mode=="arm" then
        if not row or not cave_admission(scenes,players,tuning,row,false) then return ready({kind="refuse",phase="party_changed_before_arming"}) end
        attempted[row.target]=nil
        local target=cave_candidate(scenes,tuning,attempted)
        if not target or target.guid~=row.target or target.entry~=row.entry then return ready({kind="refuse",phase="target_changed_before_arming"}) end
        local engager,order=cave_order(scenes,tuning)
        if engager~=row.engager then return ready({kind="refuse",phase="engager_goal_changed_before_arming"}) end
        for _,guid in ipairs(order) do
            if not row.requests[guid] or row.requests[guid].phase=="reserved" then return ready({kind="arm",guid=guid,engagerGuid=engager,targetGuid=row.target}) end
        end
        return ready({kind="armed"})
    end
    if input.mode=="candidate" then
        local target=cave_candidate(scenes,tuning,attempted)
        return ready(target and {kind="candidate",targetGuid=target.guid,entry=target.entry} or {kind="none"})
    end
    if input.mode~="encounter" then return invalid() end
    if row then
        if row.phase=="active" then
            for _,s in pairs(scenes) do if cave_bit(s.self.unitFlags,0x80000) then return ready({kind="wait",phase="actual_party_combat_in_progress"}) end end
            local terminal=scenes[row.engager].farg
            local causes={additional_attacker_observed=true,navigation_unavailable=true,route_replan_limit=true,navigation_step_failed=true,
                target_unsafe_or_cast_failed=true,initial_target_not_idle_unclaimed=true,target_position_unavailable_or_outside_range=true,
                target_changed_during_plan=true,observations_changed_during_plan=true,quest_changed_during_plan=true,prewrite_observations_changed=true}
            if not row.deadObserved and terminal.target==row.target and terminal.engager==row.engager and not terminal.active and causes[terminal.phase] then
                return ready({kind="abandon",cause=terminal.phase})
            end
            if row.deadObserved then return ready({kind="cleanup_dead"}) end
            for _,s in pairs(scenes) do if s.farg.active then return ready({kind="wait",phase="waiting_for_actual_engagement"}) end end
            return ready({kind="unconfirmed",phase="encounter_ended_without_observed_dead_target"})
        elseif row.phase=="loot" then
            local pending,eligible=false,false
            for _,s in pairs(scenes) do pending=pending or s.raw.l[3]~="0" or s.raw.l[2]~="idle";eligible=eligible or s.raw.l[5] end
            if pending or eligible or row.lootElapsed<tuning.loot_wait then
                return ready({kind=row.lootElapsed>tuning.loot_limit and "stop" or "wait",
                    phase=row.lootElapsed>tuning.loot_limit and "loot_uncollected_or_cleanup_unconfirmed_no_new_pull" or "waiting_for_each_own_server_permitted_loot"})
            end
            return ready({kind="settle"})
        end
        return ready({kind="reconcile"})
    end
    if cave_goal_complete(scenes) then return ready({kind="complete",phase="own_collection_quests_complete_for_all_three"}) end
    if not cave_admission(scenes,players,tuning,nil,false) then return ready({kind="wait",phase="waiting_for_safe_healthy_idle_party"}) end
    local target=cave_candidate(scenes,tuning,attempted)
    if not target then return ready({kind=task.idle+1>=tuning.idle_polls and "search" or "idle",phase="waiting_for_actual_nearby_isolated_kobold"}) end
    local engager,order=cave_order(scenes,tuning)
    if not engager then return invalid() end
    return ready({kind="arm",targetGuid=target.guid,entry=target.entry,engagerGuid=engager,order=order})
end

function M.decide(input)
    if type(input)~="table" then return invalid() end
    if input.selection=="cave_cohort" then return M.cave_cohort(input) end
    if input.selection=="practice_lifecycle" then return M.practice_lifecycle(input) end
    if input.selection=="coldridge_lifecycle" then return M.coldridge_lifecycle(input) end
    if input.selection=="fargodeep_pull_readiness" then
        if input.workflow then return M.fargodeep_lifecycle(input) end
        return fargodeep_readiness(input)
    end
    local active,desired,confirmed=ids(input.activeQuestIds,25),ids(input.desiredQuestIds,25),ids(input.confirmedQuestIds,3)
    if not active or not desired or not confirmed or not decimal(input.selfGuid) or type(input.autonomous)~="boolean" then return invalid() end
    local missing,exact={},true
    for q in pairs(desired) do if not active[q] then missing[q]=true; exact=false end end
    local complete=input.wolfComplete==true
    local known,level=input.levelKnown==true,input.level
    if known and (type(level)~="number" or level<1 or level>80 or level%1~=0) then return invalid() end
    local routine,a=input.routine,input.actions
    if routine~=nil and type(routine)~="table" or a~=nil and type(a)~="table" then return invalid() end
    if input.stage=="quest_route" then
        return quest_route(input,active,missing,confirmed,known,level,complete)
    end
    for q in pairs(active) do if not desired[q] then exact=false end end
    local failed=input.wolfFailed==true
    local wolves=active["33"] and not complete and not failed
    local xp=known and level==1 and (input.selfGuid=="4" or input.selfGuid=="5") and missing["18"] and (active["3903"] or confirmed["33"])
    local needed=wolves and known and level>=1 and level<=5 or xp
    if routine==nil then routine={} end
    if a==nil then a={} end
    if input.stage=="classify" then
        if exact and not (input.autonomous and routine.practiceOwned==true) then
            return ready(wait("matched_requested_active_quest_ids"))
        end
        local self,owner=input.self or {},input.owner or {}
        if input.autonomous then
            if self.present~=true or self.aliveKnown~=true or self.alive~=true then
                return ready(wait("waiting_for_alive_observed_character"))
            end
            return ready({kind="hold_follow"})
        end
        if owner.objectKnown~=true then return ready(wait("waiting_for_alive_present_owner_and_character")) end
        if not decimal(owner.guid) then return invalid() end
        if owner.guid~="3" or self.present~=true or self.aliveKnown~=true or self.alive~=true or
           owner.present~=true or owner.aliveKnown~=true or owner.alive~=true then
            return ready(wait("waiting_for_alive_present_owner_and_character"))
        end
        if owner.awayFlagsKnown~=true then return invalid() end
        if owner.away==true then return ready(wait("waiting_for_alive_present_owner_and_character")) end
        return ready({kind="continue"})
    elseif input.stage=="post_hold" then
        if input.autonomous then
            if a.practicePhase=="finishing_current_fight_to_level2" or a.practicePhase=="finishing_current_fight_to_goal" or
               not needed and (complete or known and level>=2) and a.practicing==true then
                return ready(wait("finishing_current_practice_engagement_before_quests"))
            end
            if (input.self or {}).combatFlagsKnown~=true then return invalid() end
            if input.self.inCombat==true then return ready(wait("waiting_out_of_combat_before_quests")) end
            if exact then return ready(wait("matched_requested_active_quest_ids")) end
        end
        if input.actionInFlight==true then return ready(wait("waiting_for_existing_quest_operation")) end
        if failed then return ready(wait("waiting_for_failed_wolf_prerequisite_resolution")) end
        if needed then
            if input.autonomous then return ready({kind="practice",lootRequired=wolves==true}) end
            return ready(wait(wolves and "waiting_for_own_wolf_objectives" or "waiting_for_level_2"))
        end
        if wolves then return ready(wait("waiting_for_supported_wolf_practice_level")) end
        return ready({kind="continue_quests"})
    elseif input.stage=="loot" then
        local loot=input.loot or {}
        if loot.known~=true then return ready(wait("waiting_for_wolf_loot_module")) end
        if loot.enabled==true then return ready({kind="continue"}) end
        return ready({kind="enable_loot",phase="waiting_for_confirmed_wolf_looting"})
    elseif input.stage=="practice" then
        return practice(input,active)
    elseif input.stage=="before_quests" then
        if a.practicing==true or routine.practiceOwned==true and
           (a.autoAttacking==true or a.autoAttackRequested==true or a.autoAttackUnconfirmed==true) then
            return ready({kind="disable_practice"})
        end
        if a.autoAttackUnconfirmed==true then return ready(wait("waiting_for_practice_attack_stop")) end
        local choice={kind="continue_quests"}
        if routine.practiceOwnerPresent==true then choice.practiceOwned=false end
        return ready(choice)
    elseif input.stage=="fallback" then
        if next(missing)==nil then return ready(wait("requested_ids_present_extra_quests_unchanged")) end
        local phase=known and level<2 and "waiting_for_level_2" or "waiting_for_level_observation"
        if input.autonomous and known and level==1 and (missing["18"] or missing["3903"]) then
            return ready({kind="practice",phase=phase})
        end
        return ready(wait(phase))
    end
    return invalid()
end
return M
