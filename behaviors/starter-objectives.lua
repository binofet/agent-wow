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
    if type(v)~="string" or #v==0 or #v>20 or #v>1 and byte_at(v,1)==48 or
       #v==20 and v>"18446744073709551615" then return false end
    for i=1,#v do local c=byte_at(v,i); if c<48 or c>57 then return false end end
    return true
end
local function number(v) return type(v)=="number" and v-v==0 and math.abs(v)<=9007199254740991 end
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
local function wolf(c)
    return type(c)=="table" and decimal(c.guid) and c.guid~="0" and c.entry==299 and c.present==true and c.mapKnown==true and (c.mapId or 0)==0 and
        c.levelKnown==true and c.level==1 and c.healthKnown==true and uint(c.health) and c.health>0 and uint(c.maxHealth) and c.maxHealth>0 and
        c.aliveKnown==true and c.alive==true and (c.npcFlags or 0)==0 and (c.unitFlags2 or 0)%2==0 and
        not bit(c.unitFlags,2) and not bit(c.unitFlags,0x80) and not bit(c.unitFlags,0x100) and not bit(c.unitFlags,0x10000) and
        not bit(c.unitFlags,0x2000000) and not bit(c.unitFlags,0x80000000)
end
local function unclaimed(c)
    return not bit(c.unitFlags,0x80000) and (c.dynamicFlags or 0)==0 and c.attackVictimKnown~=true and (c.targetKnown~=true or (c.targetGuid or "0")=="0")
end
M.practice_pull={minimum_health=95,priest_mana=45,mage_mana=40,minimum_energy=90,max_range=120,lease_ms=600000,prefer_nearest=true}
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
    for _,key in ipairs({"minimum_health","priest_mana","mage_mana","minimum_energy","max_range","lease_ms"}) do if not uint(tuning[key]) then return refuse end end
    if tuning.minimum_health>100 or tuning.priest_mana>100 or tuning.mage_mana>100 or tuning.max_range>240 or tuning.lease_ms==0 or tuning.lease_ms>600000 then return refuse end
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
    local target=input.selected
    local goal=needed and self.level<=5 or not active and self.level==1 and self.classId~=5 and not p.questGoal
    local finishing=not goal and wolf(target) and owned_fight(input,target)
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
            if d<tuning.max_range*tuning.max_range and (not selected or tuning.prefer_nearest and d<best or not tuning.prefer_nearest and d>best) and wolf(c) and unclaimed(c) then best,selected=d,c end
        end
    end
    return out(selected and "select" or "wait",selected and "fighting_observed_young_wolf" or "waiting_for_observed_level_one_young_wolf",selected and selected.guid or "0",quest)
end

M.coldridge_practice={priority={179,170,183,182},max_range=120,lease_ms=600000,
    objectives={[179]={{entry=705,count=0}},[170]={{entry=707,count=6},{entry=724,count=6}},[183]={{entry=708,count=12}},[182]={{entry=706,count=10}}}}
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
            if type(o)~="table" or not uint(o.entry) or not uint(o.count) then return end
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
        target.eligible==true and target.attackVictimKnown==true and target.attackVictimGuid==input.selfGuid and recent_age(target.attackAgeMs or 0)
    if not needed and not finishing then return out("stop",id==179 and "quest_179_complete_absent_or_level_limit" or "quest_"..tostring(id).."_complete_absent_or_invalid",id,counts,true,false,entries) end
    if needed and not targetNeeded and input.pullStarted==true and not finishing then return out("stop","selected_quest_objective_complete_without_owned_fight",id,counts,true,needed,entries) end
    if type(target)=="table" and target.healthKnown==true and (target.health or 0)==0 and target.aliveKnown==true and target.alive~=true then return out("target_dead","target_death_observed",id,counts,true,needed,entries) end
    if type(target)=="table" and input.pullStarted~=true and not targetNeeded then return out("clear_target","selected_quest_objective_already_satisfied",id,counts,true,needed,entries) end
    if input.stage=="tick" then return out("continue",finishing and "finishing_current_fight_to_goal" or "",id,counts,true,needed,entries,input.targetGuid,finishing) end
    if input.stage~="targets" then return bad end
    local selected,best=nil,tuning.max_range*tuning.max_range
    for _,c in ipairs(input.nearby) do
        local neededEntry=false
        for _,entry in ipairs(entries) do if type(c)=="table" and c.entry==entry then neededEntry=true end end
        local p=type(c)=="table" and motion_point(c)
        if neededEntry and c.eligible==true and c.positionWithin==true and c.unclaimed==true and p then
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
    for _,member in ipairs({{guid="1",class=5},{guid="4",class=8},{guid="5",class=4}}) do
        local player=obs.players[member.guid]
        if type(player)~="table" or player.guid~=member.guid or player.classKnown~=true or player.classId~=member.class or
           player.healthKnown~=true or not uint(player.health) or not uint(player.maxHealth) or
           player.health==0 or player.maxHealth==0 or player.health>player.maxHealth or
           player.powerKnown~=true or not uint(player.power) or not uint(player.maxPower) or
           player.maxPower==0 or player.power>player.maxPower or type(player.baseManaKnown)~="boolean" or
           not uint(player.baseMana) then return hold end
        if player.health*100<player.maxHealth*tuning.minimum_health then return hold end
        if member.class==4 then
            if player.powerType~=3 or player.power<tuning.minimum_energy then return hold end
        else
            if player.powerType~=0 then return hold end
            local pool=player.baseManaKnown and player.baseMana>0 and player.baseMana or player.maxPower
            local budget=math.floor((pool*tuning.minimum_mana+99)/100)
            if player.power<budget then return hold end
        end
    end
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
       type(r.wolfProbeCurrent)~="boolean" or not array(r.givers,128) or type(r.offer)~="table" then return invalid() end
    local giver={}
    for _,row in ipairs(r.givers) do
        if type(row)~="table" or not decimal(row.guid) or row.guid=="0" or
           type(row.entry)~="number" or row.entry%1~=0 or row.entry<1 or row.entry>4294967295 then return invalid() end
        giver[row.guid]=row.entry
    end
    local o=r.offer
    if type(o.objectKnown)~="boolean" or type(o.questsKnown)~="boolean" or type(o.introQuestsKnown)~="boolean" or
       not decimal(o.npcGuid) or not decimal(o.expiresInMs) or not array(o.quests,32) then return invalid() end
    local offered={}
    for _,row in ipairs(o.quests) do
        if type(row)~="table" or type(row.questId)~="number" or row.questId%1~=0 or
           row.questId<0 or row.questId>4294967295 or type(row.available)~="boolean" then return invalid() end
        if row.available then offered[tostring(row.questId)]=true end
    end
    local reward=0
    for _,q in ipairs({783,5261,33}) do
        if active[tostring(q)] and (q~=33 or complete) then reward=q;break end
    end
    local needed=823
    if reward~=0 then needed=reward==783 and 197 or 196
    elseif missing["7"] then needed=confirmed["783"] and 197 or 823
    elseif missing["3903"] and not confirmed["33"] and not r.wolfProbeCurrent and
           (confirmed["5261"] or r.eaganProbeCurrent) then needed=196 end
    local probes={}
    local fresh=o.objectKnown and o.expiresInMs~="0"
    if missing["7"] and not active["783"] then
        if fresh and giver[o.npcGuid]==823 and o.introQuestsKnown and not offered["783"] then
            probes.intro=true
        end
        if r.introProbeCurrent or probes.intro then needed=197 end
    end
    if active["7"] and missing["3903"] and not confirmed["5261"] and not confirmed["33"] and
       not active["5261"] and not active["33"] and fresh and giver[o.npcGuid]==823 and
       o.questsKnown and not offered["5261"] and not offered["3903"] then
        probes.eagan,probes.wolf=true,false
        needed=196
    end
    -- An empty fresh Eagan menu permits another Willem query. It never
    -- manufactures reward confirmation or permission to accept an absent row.
    if reward==0 and missing["3903"] and not active["5261"] and not active["33"] and
       fresh and giver[o.npcGuid]==196 and o.questsKnown and not offered["33"] then
        probes.wolf=true
        needed=823
    end
    local allowed={}
    if needed==197 and missing["7"] then allowed={7}
    elseif needed==823 then
        if missing["7"] then allowed[#allowed+1]=783 end
        if known and level>=2 then
            for _,q in ipairs({18,3903}) do if missing[tostring(q)] then allowed[#allowed+1]=q end end
        end
        if missing["3903"] and not confirmed["5261"] and not confirmed["33"] then allowed[#allowed+1]=5261 end
    elseif needed==196 and missing["3903"] and not confirmed["33"] then allowed={33} end
    return ready({kind="quest_route",neededEntry=needed,rewardQuestId=reward,allowedQuestIds=allowed,probes=probes})
end
function M.decide(input)
    if type(input)~="table" then return invalid() end
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
    for q in pairs(active) do if not desired[q] then exact=false end end
    local complete,failed=input.wolfComplete==true,input.wolfFailed==true
    local wolves=active["33"] and not complete and not failed
    local known,level=input.levelKnown==true,input.level
    if known and (type(level)~="number" or level<1 or level>80 or level%1~=0) then return invalid() end
    local xp=known and level==1 and (input.selfGuid=="4" or input.selfGuid=="5") and missing["18"] and (active["3903"] or confirmed["33"])
    local needed=wolves and known and level>=1 and level<=5 or xp
    local routine,a=input.routine,input.actions
    if routine==nil then routine={} end
    if a==nil then a={} end
    if type(routine)~="table" or type(a)~="table" then return invalid() end
    if input.stage=="quest_route" then
        return quest_route(input,active,missing,confirmed,known,level,complete)
    elseif input.stage=="classify" then
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
