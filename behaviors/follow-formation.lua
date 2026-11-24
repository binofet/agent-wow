-- A personal follow slot and its restart policy. Navigation and movement
-- custody belong to the caller. Memory is explicit so both Lua hosts and the
-- builtin native bridge use the same state transition.
local M = {}
local tau, half_turn = 6.283185307179586, 3.141592653589793
local roles = {[5]={3.8,-3.1}, [8]={5.1,.2}, [4]={2.4,3.5}, [9]={1,-5.2}}
local near_stop, near_limit = 3,30
local portal_ahead, portal_side = {8,12,16}, {-6,6}
local portal_wait_ms, portal_seen_ms, portal_catchup_ms = 500,20000,75000
local portal_approach_yards = 45
local service_arrival_yards = 3
local vendor_search_yards, vendor_interaction_yards = 35,4.5
local vendor_prefer_nearest = true
local trainer_search_yards, trainer_interaction_yards = 100,4
local trainer_prefer_nearest = true
local profession_trainers={search_yards=120,anchor_yards=160,max_candidates=32,prefer_nearest=true}
local class_training={purchase_cap_copper=1000000,reserve_copper=100,prefer_cheapest=true}
local profession_training={purchase_cap_copper=10000,reserve_copper=0,prefer_cheapest=true,primary_first=true}
local fallback = {{3.8,-3.1}, {5.1,.2}, {2.4,3.5}}

local function number(value)
    return type(value)=="number" and value==value and value-value==0
end

local function pose(value, facing)
    return type(value)=="table" and number(value.x) and math.abs(value.x)<=40000 and
        number(value.y) and math.abs(value.y)<=40000 and number(value.z) and math.abs(value.z)<=40000 and
        (not facing or number(value.o) and math.abs(value.o)<=40000)
end

local function identity(guid)
    if type(guid)~="string" or #guid==0 or #guid>20 or byte_at(guid,1)==48 or
       #guid==20 and guid>"18446744073709551615" then return end
    local hash, remainder, last = 0,0,0
    for i=1,#guid do
        local digit=byte_at(guid,i)-48
        if digit<0 or digit>9 then return end
        -- Each intermediate is below 2^53, including for a full uint64 GUID.
        hash=(hash*131+digit)%2147483647
        remainder,last=(remainder*10+digit)%3,digit
    end
    return hash,remainder,last
end

local function angle(value) return value%tau end

local function revision(value) return value=="0" or identity(value)~=nil end

local function at_target(self, target, radius)
    local dx,dy=self.x-target.x,self.y-target.y
    return dx*dx+dy*dy<=radius*radius and math.abs(self.z-target.z)<=1.5
end

local inn_trainers = {[5]=377,[8]=328,[4]=917}
local inn_stairs = {{x=-9477,y=22.5,z=56.339386,radius=.75},
    {x=-9477,y=14,z=60.184803,radius=.75}, {x=-9473.5,y=14,z=60.190014,radius=.75},
    {x=-9473.5,y=23,z=63.820427,radius=.75}}

local function inn_route(input)
    local p,t=input.position,input.trainer
    if (input.direction~="approach" and input.direction~="return") or
       input.classKnown~=true or not number(input.classId) or not pose(p,false) or not pose(t,false) or
       p.mapId~=0 or t.mapId~=0 or t.entry~=inn_trainers[input.classId] or
       t.x < -9480 or t.x > -9455 or t.y < 8 or t.y > 39 or t.z < 63 or t.z > 65 or
       p.x < -9490 or p.x > -9410 or p.y < 0 or p.y > 85 or p.z < 54 or p.z > 65.5 then
        return {eligible=false}
    end
    local route={}
    if input.direction=="return" then
        if p.x < -9480 or p.x > -9455 or p.y < 8 or p.y > 39 or p.z < 62.8 then
            return {eligible=false}
        end
        for i=#inn_stairs,1,-1 do route[#route+1]=inn_stairs[i] end
    elseif p.z < 62.8 then
        local first=1
        if p.x>=-9474.5 and p.x<=-9472.5 and p.y>=14 and p.y<=23.5 and p.z>=60 then
            first=4
        elseif p.x>=-9478.5 and p.x<=-9473 and p.y>=13 and p.y<=15 and p.z>=59.5 and p.z<=61 then
            first=3
        elseif p.x>=-9478.5 and p.x<=-9475.5 and p.y>15 and p.y<=22.5 and p.z>=56.8 and p.z<=60.7 then
            first=2
        elseif p.x>=-9468 and p.x<=-9462 and p.y>=29 and p.y<=35.5 and p.z>=56 and p.z<=58.3 then
            route[1]={x=-9467,y=30,z=56.96606,radius=.75}
        else
            if p.x > -9435 and p.y>=45 and p.y<=70 then
                route[#route+1]={x=-9432,y=60,z=56.625515,radius=.5}
            end
            route[#route+1]={x=-9465.93,y=24.9355,z=56.52882,radius=.5}
        end
        for i=first,#inn_stairs do route[#route+1]=inn_stairs[i] end
    end
    return {eligible=true,route=route}
end

local function training_choice(input)
    local function uint(v) return number(v) and v>=0 and v<=4294967295 and v%1==0 end
    local profession=input.kind=="profession_training"
    local tuning=profession and profession_training or class_training
    if input.copperKnown~=true or not uint(input.copper) or not uint(input.nativePurchaseCap)
        or not uint(tuning.purchase_cap_copper) or not uint(tuning.reserve_copper)
        or type(tuning.prefer_cheapest)~="boolean" or type(input.spells)~="table" or #input.spells>256
        or type(input.eligibility)~="table" or type(input.attempted)~="table" then return {} end
    local reserve=input.reserveCopper
    if reserve==nil then reserve=tuning.reserve_copper end
    if not uint(reserve) then return {} end
    local cap=math.min(tuning.purchase_cap_copper,input.nativePurchaseCap)
    local assigned,wanted,observed,primary_count,conflict={},{},{},0,false
    local profession_state=input.professions or {}
    if profession then
        if profession_state.known~=true or type(tuning.primary_first)~="boolean" or input.levelKnown~=true or not uint(input.level)
            or not uint(input.goalBudgetCopper) or not uint(input.reservedCopper) or input.reservedCopper>input.goalBudgetCopper
            or type(input.primaryGoals)~="table" or #input.primaryGoals>2 or type(input.secondaryGoals)~="table" or #input.secondaryGoals>3
            or type(profession_state.primary)~="table" or #profession_state.primary>11
            or type(profession_state.secondary)~="table" or #profession_state.secondary>3 then return {} end
        cap=math.min(cap,input.goalBudgetCopper-input.reservedCopper)
        for _,skill in ipairs(input.primaryGoals) do
            if not uint(skill) or skill==0 or wanted[tostring(skill)] then return {} end
            assigned[tostring(skill)]=true;wanted[tostring(skill)]=true
        end
        for _,skill in ipairs(input.secondaryGoals) do
            if not uint(skill) or skill==0 or wanted[tostring(skill)] then return {} end
            wanted[tostring(skill)]=true
        end
        for _,rows in ipairs({profession_state.primary,profession_state.secondary}) do
            for _,row in ipairs(rows) do
                if type(row)~="table" or not uint(row.skillId) or row.skillId==0 or observed[tostring(row.skillId)] then return {} end
                observed[tostring(row.skillId)]=true
                if rows==profession_state.primary then
                    primary_count=primary_count+1
                    if not assigned[tostring(row.skillId)] then conflict=true end
                end
            end
        end
    end
    local budget=math.min(cap,math.max(0,input.copper-reserve))
    local out={consumer=profession and "profession_training" or "class_training",budgetCopper=budget,reserveCopper=reserve,purchaseCapCopper=cap}
    local best,seen
    seen={}
    for _,spell in ipairs(input.spells) do
        if type(spell)~="table" then return {} end
        local id,cost,level=spell.spellId,spell.costCopper or 0,spell.requiredLevel or 0
        local usable,points=spell.usable or 0,spell.pointCost or {}
        if not uint(id) or id==0 or seen[tostring(id)] or not uint(cost)
            or not uint(level) or not uint(usable) or usable>2 or type(points)~="table" or #points>2 then return {} end
        seen[tostring(id)]=true
        local no_points=true
        for _,value in ipairs(points) do if not uint(value) then return {} end;if value~=0 then no_points=false end end
        local evidence=input.eligibility[tostring(id)] or {}
        local eligible=evidence.known==true and evidence.retryAllowed==true and uint(evidence.level) and level<=evidence.level
            and usable==0 and no_points and input.attempted[tostring(id)]~=true
        local order_id,priority,skill,learned=id,0
        if profession then
            skill,learned=spell.professionSkillId,spell.learnedSpellId
            local primary=assigned[tostring(skill)]==true
            local abilities=spell.requiredAbilities or {};local abilities_clear=type(abilities)=="table" and #abilities<=3
            for _,value in ipairs(abilities_clear and abilities or {}) do
                if not uint(value) or value~=0 then abilities_clear=false end
            end
            eligible=uint(skill) and skill>0 and uint(learned) and learned>0
                and wanted[tostring(skill)]==true and not observed[tostring(skill)] and input.attempted[tostring(skill)]~=true
                and usable==0 and id<2147483648 and learned<2147483648 and (spell.primaryProfession==true)==primary
                and #points==2 and points[1]==0 and points[2]==(primary and 1 or 0) and level<=input.level
                and (spell.requiredSkill or 0)==0 and (spell.requiredSkillRank or 0)==0 and abilities_clear
            if primary and (conflict or primary_count>=2 or profession_state.freePrimarySlotsKnown~=true
                or not uint(profession_state.freePrimarySlots) or profession_state.freePrimarySlots<1
                or profession_state.freePrimarySlots>2 or input.level<5) then eligible=false end
            order_id=skill;priority=tuning.primary_first and not primary and 1 or 0
        end
        if ((not profession and usable==0 and input.attempted[tostring(id)]~=true) or profession and eligible)
            and cost>budget and cost<=cap
            and (not out.nextCost or cost<out.nextCost) then out.nextCost=cost end
        if eligible and cost<=budget then
            if not best or priority<best.priority or priority==best.priority and
                (cost~=best.costCopper and (tuning.prefer_cheapest and cost<best.costCopper or not tuning.prefer_cheapest and cost>best.costCopper)
                or cost==best.costCopper and order_id<best.orderId) then
                best={spellId=id,costCopper=cost,priority=priority,orderId=order_id,skillId=skill,learnedSpellId=learned}
            end
        end
    end
    if best then
        out.spellId=best.spellId;out.costCopper=best.costCopper
        out.skillId=best.skillId;out.learnedSpellId=best.learnedSpellId
    end
    return out
end

-- Town planning and the finite service workers consult this export from the
-- character's current accepted policy. Callers supply mechanically validated
-- observed NPCs; this policy owns the search radius and destination order.
function M.service_destination(input)
    if type(input)=="table" and input.kind=="inn_route" then return inn_route(input) end
    if type(input)=="table" and (input.kind=="class_training" or input.kind=="profession_training") then return training_choice(input) end
    if type(input)~="table" or (input.kind~="vendor" and input.kind~="trainer") or
       (input.scope~="nearby" and input.scope~="interactable" and input.scope~="travel") or
       type(input.candidates)~="table" or #input.candidates>97 then return {} end
    local trainer=input.kind=="trainer"
    local profession=trainer and input.purpose=="profession"
    local class=trainer and input.purpose=="class"
    if input.purpose~=nil and not profession and not class then return {} end
    if class and (type(input.classKnown)~="boolean" or not number(input.classId) or
       input.classId<0 or input.classId>4294967295 or input.classId%1~=0) then return {} end
    if not trainer and (input.scope=="travel" or #input.candidates>96) then return {} end
    local radius=trainer and trainer_search_yards or
        (input.scope=="nearby" and vendor_search_yards or vendor_interaction_yards)
    local prefer_nearest=trainer and trainer_prefer_nearest or not trainer and vendor_prefer_nearest
    if profession then
        radius=profession_trainers.search_yards
        prefer_nearest=profession_trainers.prefer_nearest
        if input.scope~="nearby" or not number(profession_trainers.anchor_yards) or profession_trainers.anchor_yards<0 or
           not number(profession_trainers.max_candidates) or profession_trainers.max_candidates%1~=0 or
           profession_trainers.max_candidates<1 or profession_trainers.max_candidates>97 or
           type(prefer_nearest)~="boolean" then return {} end
    end
    if not number(radius) or radius<=0 then return {} end
    local ranked,seen={},{}
    for _,candidate in ipairs(input.candidates) do
        if type(candidate)~="table" or not identity(candidate.guid) or seen[candidate.guid] or
           not number(candidate.distance) or candidate.distance<0 or
           trainer and type(candidate.interactable)~="boolean" or
           class and (not number(candidate.entry) or candidate.entry<1 or
               candidate.entry>16777215 or candidate.entry%1~=0) or
           profession and (not number(candidate.anchorDistance) or candidate.anchorDistance<0 or
               type(candidate.blockedReason)~="string") then return {} end
        seen[candidate.guid]=true
        local within=candidate.distance<=radius
        local eligible=false
        if profession then
            -- Native List acquires its own stop lease and revalidates the exact
            -- NPC. Current interactability is an observation, not a claim that
            -- a stationary nearby trainer cannot be prepared for interaction.
            eligible=within and candidate.anchorDistance<=profession_trainers.anchor_yards
        elseif trainer then
            if candidate.interactable then
                if input.scope~="travel" then eligible=true end
            elseif input.scope~="interactable" and candidate.distance>trainer_interaction_yards and within then
                eligible=true
            end
        elseif within then eligible=true end
        if eligible and class then
            local curated=false
            for _,entry in pairs(inn_trainers) do
                if candidate.entry==entry then curated=true end
            end
            if curated then
                eligible=false
                if input.classKnown==true then eligible=candidate.entry==inn_trainers[input.classId] end
            end
        end
        if eligible then
            local index=#ranked+1
            for i,other in ipairs(ranked) do
                local before=candidate.distance~=other.distance and
                    ((prefer_nearest and candidate.distance<other.distance) or
                     (not prefer_nearest and candidate.distance>other.distance))
                if candidate.distance==other.distance then
                    before=#candidate.guid<#other.guid or #candidate.guid==#other.guid and candidate.guid<other.guid
                end
                if trainer and not profession and candidate.interactable~=other.interactable then before=candidate.interactable end
                if before then index=i;break end
            end
            for i=#ranked,index,-1 do ranked[i+1]=ranked[i] end
            ranked[index]=candidate
        end
    end
    local vendors,inn={},{}
    for i,candidate in ipairs(ranked) do
        if not profession or i<=profession_trainers.max_candidates then vendors[#vendors+1]=candidate.guid end
        if class and input.classKnown==true and candidate.entry==inn_trainers[input.classId] then inn[#inn+1]=candidate.guid end
    end
    if profession then return {trainers=vendors,purpose="profession"} end
    if class then return {trainers=vendors,purpose="class",innTrainers=inn} end
    if trainer then return {trainers=vendors} end
    return {vendors=vendors}
end

function M.decide(input)
    local hash,remainder,last=identity(input.selfGuid)
    local now,class,owner=input.nowMs,input.classId,input.owner
    if not hash or not number(now) or math.abs(now)>9007199254740000 or
       not number(class) or class<0 or class>4294967295 or class%1~=0 or
       not pose(owner,true) or type(owner.moving)~="boolean" or
       input.self~=nil and (not pose(input.self,false) or type(input.self.moving)~="boolean") or
       input.ownerArrival~=nil and type(input.ownerArrival)~="boolean" then
        if input.mode=="portal_follow" then return {eligible=false,ready=false} end
        return {}
    end

    if input.mode=="service_travel" then
        local t=input.travel
        if not input.self or type(t)~="table" or (t.kind~="vendor" and t.kind~="trainer") or
           type(t.routeKnown)~="boolean" or not number(service_arrival_yards) or service_arrival_yards<=0 or
           t.remainingRoute~=nil and (not number(t.remainingRoute) or t.remainingRoute<0) or
           t.routeKnown and not number(t.remainingRoute) then return {} end
        local dx,dy,dz=input.self.x-owner.x,input.self.y-owner.y,input.self.z-owner.z
        return {arrived=dx*dx+dy*dy+dz*dz<=service_arrival_yards*service_arrival_yards and
            (not t.routeKnown or t.remainingRoute<=service_arrival_yards)}
    end

    if input.mode=="portal_sighting" then
        if not input.self or not number(portal_approach_yards) or portal_approach_yards<=0 then return {} end
        local dx,dy,dz=input.self.x-owner.x,input.self.y-owner.y,input.self.z-owner.z
        return {catchingUp=dx*dx+dy*dy+dz*dz>portal_approach_yards*portal_approach_yards}
    end

    if input.mode=="portal_follow" then
        local p=input.portal
        local in_range=false
        if input.self and number(portal_approach_yards) and portal_approach_yards>0 then
            local dx,dy,dz=input.self.x-owner.x,input.self.y-owner.y,input.self.z-owner.z
            in_range=type(p)=="table" and (p.catchingUp==true or
                dx*dx+dy*dy+dz*dz<=portal_approach_yards*portal_approach_yards)
        end
        local eligible=in_range and type(p)=="table" and p.following==true and p.ownerAbsent==true and
            type(p.catchingUp)=="boolean" and revision(p.sourceVisitId) and
            p.sourceVisitId==p.currentVisitId and revision(p.followRevision) and
            p.followRevision==p.currentFollowRevision and p.sightingAgeKnown==true and
            number(p.sightingAgeMs) and p.sightingAgeMs>=0 and
            p.sightingAgeMs<=(p.catchingUp and portal_catchup_ms or portal_seen_ms)
        local ready=eligible and p.disappearanceAgeKnown==true and
            number(p.disappearanceAgeMs) and p.disappearanceAgeMs>=0 and p.disappearanceAgeMs>=portal_wait_ms
        return {eligible=eligible,ready=ready}
    end

    if input.mode=="portal_catch_up" then
        if not input.self or not number(portal_approach_yards) or portal_approach_yards<=0 then return {} end
        local candidates={{x=owner.x,y=owner.y,z=owner.z}}
        local dx,dy=owner.x-input.self.x,owner.y-input.self.y
        local gap=math.sqrt(dx*dx+dy*dy)
        if gap>portal_approach_yards then
            dx,dy=dx/gap,dy/gap
            for _,ahead in ipairs(portal_ahead) do
                for _,side in ipairs(portal_side) do
                    local candidate={x=input.self.x+ahead*dx-side*dy,
                        y=input.self.y+ahead*dy+side*dx,z=input.self.z}
                    if not pose(candidate,false) then return {} end
                    candidates[#candidates+1]=candidate
                end
            end
        end
        return {candidates=candidates}
    end

    if input.mode=="near_owner" then
        if not input.self then return {} end
        local dx,dy,dz=input.self.x-owner.x,input.self.y-owner.y,input.self.z-owner.z
        local d=math.sqrt(dx*dx+dy*dy+dz*dz)
        local phase=d>near_limit or math.abs(dz)>1.5
        phase=phase and "follow_target_too_far_or_vertical" or d<=near_stop and "within_follow_distance" or "following_formation"
        return {position={x=owner.x,y=owner.y,z=owner.z,o=owner.o},startDistance=near_stop,stopDistance=near_stop,
            ready=true,hold=phase~="following_formation",phase=phase,memory={guid=input.selfGuid,classId=class,lastAtMs=now}}
    end

    local role=roles[class] or fallback[remainder+1]
    local rear=role[1]+(hash%241-120)/1000
    local side=role[2]+(math.floor(hash/241)%241-120)/1000
    local start=1.8+math.floor(hash/58081)%201/1000
    local stop=.8+math.floor(hash/11674281)%151/1000
    local reaction=200+50*last
    local previous=input.memory
    local current={guid=input.selfGuid,classId=class,lastAtMs=now,ownerMoving=owner.moving}
    if type(previous)~="table" or previous.guid~=input.selfGuid or previous.classId~=class or
       not number(previous.heading) or previous.heading<0 or previous.heading>=tau or
       type(previous.ownerMoving)~="boolean" or not number(previous.readyAtMs) or
       not number(previous.lastAtMs) or now<previous.lastAtMs then
        current.heading,current.readyAtMs=angle(owner.o),now+reaction
    else
        current.heading,current.readyAtMs=previous.heading,previous.readyAtMs
        if owner.moving then
            if not previous.ownerMoving then
                current.heading,current.readyAtMs=angle(owner.o),now+reaction
            else
                local delta=(owner.o-current.heading+half_turn)%tau-half_turn
                if math.abs(delta)>.3 then
                    local limit=math.max(0,math.min((now-previous.lastAtMs)/1000,.4))*half_turn/2
                    current.heading=angle(current.heading+math.max(-limit,math.min(delta,limit)))
                end
            end
        end
    end
    local c,s=math.cos(current.heading),math.sin(current.heading)
    local target={x=owner.x-rear*c-side*s,y=owner.y-rear*s+side*c,z=owner.z,o=current.heading}
    if not pose(target,true) then return {} end
    local ready=now>=current.readyAtMs
    local hold,phase=false,"following_formation"
    if input.self then
        if input.ownerArrival~=true and (at_target(input.self,target,stop) or
           not input.self.moving and at_target(input.self,target,start)) then
            hold,phase=true,"within_personal_follow_space"
        elseif not input.self.moving and not ready then
            hold,phase=true,"formation_reaction_delay"
        end
    end
    return {position=target,startDistance=start,stopDistance=stop,ready=ready,hold=hold,phase=phase,memory=current}
end

return M
