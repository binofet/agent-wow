-- Content comes from the goal. Native target qualification, navigation and
-- profession receipts remain authoritative. The exported handler can interrupt
-- another task stack and returns its result to the unchanged parent frame.
local function n(v) return tonumber(v) or 0 end
local function eq(a,b) return tostring(a or "") == tostring(b or "") end
local function flag(v,bit) return math.floor(n(v)/bit)%2==1 end
local function id(v) return type(v)=="string" and #v>0 and #v<=20 and v~="0" end
local function same(c,s)
    return type(s)=="table" and eq(s.sessionId,c.state.body.sessionId) and eq(s.selfGuid,c.state.body.selfGuid)
end
local function point(p)
    if type(p)~="table" or not p.mapKnown then return false end
    for _,key in ipairs({"x","y","z"}) do
        -- ProtoJSON omits default-zero axes. Presence/known flags establish
        -- the position baseline; absent axes on that baseline mean zero.
        if p[key]~=nil and (type(p[key])~="number" or p[key]~=p[key] or math.abs(p[key])>40000) then return false end
    end
    return true
end
local function distance(a,b) return math.sqrt((n(a.x)-n(b.x))^2+(n(a.y)-n(b.y))^2+(n(a.z)-n(b.z))^2) end
local function config(c,f)
    local p=f.data.resources and f.data or c.state.goal.params
    assert(type(p.resources)=="table" and #p.resources>0 and #p.resources<=4,"bounded gathering resources required")
    for _,r in ipairs(p.resources) do
        assert(type(r.skillId)=="number" and r.skillId>0 and r.skillId%1==0
            and type(r.spellId)=="number" and r.spellId>0 and r.spellId%1==0
            and (r.targetKind=="node" or r.targetKind=="corpse"),"known resource skill/spell and target kind required")
    end
    local limit=p.maxAttempts or 1
    local range=p.approachDistance or 3
    assert(type(limit)=="number" and limit>=1 and limit<=8 and limit%1==0
        and type(range)=="number" and range>=3 and range<=40,"bounded gathering attempts and approach required")
    local prefix=p.requestPrefix or c.state.goal.id
    assert(type(prefix)=="string" and #prefix>0 and #prefix<=48,"bounded native request prefix required")
    return p,limit,range,prefix
end
local function safe(c,lease)
    local a,w=c.observations.actions or {},(c.observations.world or {}).self or {}
    if not same(c,a) or not eq(w.guid,c.state.body.selfGuid) or not w.present or not w.aliveKnown or not w.alive
       or not w.healthKnown or n(w.health)<=0 or n(w.maxHealth)<=0 or n(w.health)>n(w.maxHealth)
       or n(w.health)/n(w.maxHealth)<0.85 or not w.unitFlagsKnown or not w.mapKnown then return false end
    for _,bit in ipairs({1,4,0x40000,0x80000,0x100000,0x400000,0x800000,0x1000000,0x2000000,0x8000000}) do
        if flag(w.unitFlags,bit) then return false end
    end
    local movement=n(w.movementFlags)
    if not w.movementFlagsKnown or movement<0 or movement>=0x80000000
       or movement%0x10000000>0x1ff or n(w.extraMovementFlags)~=0 then return false end
    local p=a.position or {}
    return p.known and point(p) and eq(p.mapId or 0,w.mapId or 0) and not a.moving and not a.traveling
       and not a.motionUnconfirmed and not a.castPending and not a.autoAttacking and not a.autoAttackRequested
       and not a.autoAttackUnconfirmed and not a.practicing and not (a.recovery or {}).active
       and ((a.interactionOwner or "")=="" or lease and a.interactionOwner=="exploration" and a.interactionHarvest)
end
local function settled(s)
    local o=s.operation or {}
    if s.active or s.closing or o.cleanupUnconfirmed then return false end
    if next(o)==nil then return true end
    return o.leaseReleased and (o.status=="COMPLETED" or o.status=="BLOCKED"
        or o.status=="SERVER_REJECTED" or o.status=="CANCELLED")
end
local function known(s,r)
    if not s.skillsKnown or not s.spellsKnown or not s.inventoryKnown or n(s.inventoryRevision)<=0
       or n(s.skillsRevision)<=0 or n(s.revision)<=0 or not settled(s) then return false end
    local rank,recipe
    for _,row in ipairs(s.skills or {}) do
        if eq(row.skillId,r.skillId) then if rank then return false end;rank=n(row.rank) end
    end
    for _,row in ipairs(s.recipes or {}) do
        if eq(row.skillId,r.skillId) and eq(row.spellId,r.spellId) then
            if recipe then return false end;recipe=row
        end
    end
    return rank and rank>0 and recipe and recipe.known and recipe.requirementsVerified and rank,recipe
end
local function source(row,r)
    return row[r.targetKind=="corpse" and "creature" or "object"]
end
local function rows(s,r) return s[r.targetKind=="corpse" and "skinningTargets" or "herbTargets"] or {} end
local function qualified(c,row,r,range)
    local t=source(row,r);local p=(c.observations.actions or {}).position or {}
    if not row.approachable or type(t)~="table" or not id(t.guid) or n(t.entry)<=0 or not t.present
       or not t.positionKnown or not point(t) or n(t.positionRevision)<=0 or not p.known or not point(p)
       or not eq(t.mapId or 0,p.mapId or 0) or distance(t,p)>range or math.abs(n(t.z)-n(p.z))>1.5 then return false end
    if r.targetKind=="corpse" and (not row.fullyLootedObserved or not t.aliveKnown or t.alive
       or not t.healthKnown or n(t.health)~=0) then return false end
    return true
end
local function exact(c,f,s,range)
    if not same(c,s) then return nil end
    local rank=known(s,f.resource)
    if not rank then return nil end
    local found
    for _,row in ipairs(rows(s,f.resource)) do
        local t=source(row,f.resource)
        if t and eq(t.guid,f.target.guid) then
            if found then return nil end
            if not qualified(c,row,f.resource,range) or rank<n(row.requiredSkill) then return nil end
            for _,key in ipairs({"guid","entry","mapId","x","y","z","positionRevision"}) do
                if not eq(t[key] or 0,f.target[key] or 0) then return nil end
            end
            found=row
        end
    end
    return found
end
local function attempted(c,guid)
    for _,row in ipairs(c.memory.gatheringAttempts or {}) do if eq(row.targetGuid,guid) then return true end end
    return false
end
local function select(c,p,range)
    local s=c.observations.professions or {}
    if not same(c,s) then return nil,"gathering_body_observations_unknown" end
    if not settled(s) then return nil,"gathering_prior_operation_unsettled" end
    local selected
    for _,r in ipairs(p.resources) do
        local rank=known(s,r)
        local candidates=rows(s,r)
        if #candidates>32 then return nil,"gathering_target_bound" end
        if rank then for _,row in ipairs(candidates) do
            local t=source(row,r)
            if qualified(c,row,r,range) and rank>=n(row.requiredSkill) and not attempted(c,t.guid) then
                local d=distance(t,c.observations.actions.position)
                if not selected or d<selected.distance or d==selected.distance and t.guid<selected.target.guid then
                    selected={target=t,resource=r,distance=d}
                end
            end
        end end
    end
    return selected,"no_verified_gathering_opportunity"
end
local function finish(c,f,status,reason)
    tasks.pop(c,{status=status,reason=reason,confirmed=n(f.confirmed),attempted=n(f.count)})
end
local function bound(c,f,s)
    local o=s.operation or {};local b=f.binding or {}
    if not same(c,s) or not id(o.targetGuid) or type(o.operationId)~="string" or o.operationId=="" then return false end
    for _,key in ipairs({"requestId","skillId","spellId","targetGuid","targetPositionRevision"}) do
        if not eq(o[key],b[key]) then return false end
    end
    return eq(o.targetEntry,f.target.entry) and eq(o.baselineInventoryRevision,b.expectedInventoryRevision)
       and eq(o.baselineSkillsRevision,b.expectedSkillsRevision)
end
local handlers={}
handlers.gathering=function(c,f)
    local p,limit,range,prefix=config(c,f)
    local a,s=c.observations.actions or {},c.observations.professions or {}
    c.memory.gatheringAttempts=c.memory.gatheringAttempts or {}
    if f.halt then return tasks.await(c,f.halt) end
    if f.stage=="start" or f.stage=="select" then
        if n(f.count)>=limit then finish(c,f,"gathered");return end
        if #c.memory.gatheringAttempts>=128 then finish(c,f,"blocked","gathering_reservation_bound");return end
        if not safe(c,false) then return tasks.await(c,"gathering_waiting_for_safe_idle") end
        local selected,why=select(c,p,range)
        if not selected then
            if why=="gathering_prior_operation_unsettled" or why=="gathering_body_observations_unknown" then return tasks.await(c,why) end
            finish(c,f,n(f.confirmed)>0 and "gathered" or "no_opportunity",why);return
        end
        f.resource,f.target=selected.resource,selected.target;f.count=n(f.count)+1
        local row={targetGuid=f.target.guid,targetEntry=f.target.entry,skillId=f.resource.skillId,spellId=f.resource.spellId,
            targetPositionRevision=f.target.positionRevision,phase="selected"}
        c.memory.gatheringAttempts[#c.memory.gatheringAttempts+1]=row
        f.rowIndex=#c.memory.gatheringAttempts
        f.stage=selected.distance>3 and "acquire" or "attempt"
    end
    -- JSON memory clones do not preserve shared table identity across ticks.
    local record=c.memory.gatheringAttempts[f.rowIndex]
    if f.stage=="acquire" then
        if not f.waiting and (not safe(c,false) or not exact(c,f,s,range)) then
            record.phase="blocked";finish(c,f,"blocked","gathering_target_or_guard_changed");return
        end
        local reply,out=tasks.call(c,f,"buddy-actions.acquire_interaction",
            {owner="exploration",leaseMs=30000,harvest=true},"gathering_acquiring_guarded_route")
        if out then return out end
        if type(reply.token)~="string" or #reply.token==0 or #reply.token>128 then
            f.halt="gathering_acquisition_unconfirmed";return tasks.await(c,f.halt)
        end
        f.token=reply.token
        if not same(c,reply.state) or reply.state.interactionOwner~="exploration" or not reply.state.interactionHarvest
           or n(reply.state.interactionLeaseRemainingMs)<2000 or reply.state.motionUnconfirmed then
            f.halt="gathering_lease_unconfirmed";return tasks.await(c,f.halt)
        end
        f.stage="move"
    end
    if f.stage=="move" then
        if not f.waiting and (not safe(c,true) or not exact(c,f,s,range) or n(a.interactionLeaseRemainingMs)<2000) then
            f.blocked="gathering_target_or_route_guard_changed";f.stage="release"
        else
            local t=f.target
            local reply,out=tasks.call(c,f,"buddy-actions.travel_to",{token=f.token,mapId=t.mapId or 0,
                x=t.x,y=t.y,z=t.z,arrivalDistance=2.5},"gathering_approaching_observed_target")
            if out then return out end
            local pos=reply.position or {}
            if not same(c,reply) or reply.reason~="exploration_within_distance" or not pos.known or not point(pos)
               or not eq(pos.mapId or 0,t.mapId or 0) or distance(pos,t)>3 or reply.moving or reply.traveling
               or reply.motionUnconfirmed then f.blocked="gathering_route_not_confirmed" end
            f.stage="release"
        end
    end
    if f.stage=="release" then
        if f.releaseReturned then
            if (a.interactionOwner or "")~="" or a.motionUnconfirmed or a.moving or a.traveling then
                return tasks.await(c,"gathering_route_cleanup_unconfirmed")
            end
        elseif f.waiting or a.interactionOwner=="exploration" then
            local reply,out=tasks.call(c,f,"buddy-actions.release_interaction",{owner="exploration",token=f.token},"gathering_releasing_route")
            if out then return out end
            f.releaseReturned=true
            if not same(c,reply) or (reply.interactionOwner or "")~="" or reply.motionUnconfirmed or reply.traveling or reply.moving then
                return tasks.await(c,"gathering_route_cleanup_unconfirmed")
            end
        elseif (a.interactionOwner or "")~="" or a.motionUnconfirmed or a.moving or a.traveling then
            return tasks.await(c,"gathering_route_cleanup_unconfirmed")
        end
        f.token=nil
        if f.blocked then record.phase="blocked";finish(c,f,"blocked",f.blocked);return end
        f.stage="attempt"
        return tasks.await(c,"gathering_route_released")
    end
    if f.stage=="attempt" then
        if not f.waiting then
            local row=exact(c,f,s,3);local _,recipe=known(s,f.resource)
            if not safe(c,false) or not row or not row.ready or not recipe or not recipe.ready then
                record.phase="blocked";finish(c,f,"blocked","gathering_final_target_or_guard_changed");return
            end
            f.binding={sessionId=c.state.body.sessionId,requestId=prefix.."_"..tostring(c.memory.serial+1),
                skillId=f.resource.skillId,spellId=f.resource.spellId,targetGuid=f.target.guid,
                targetPositionRevision=f.target.positionRevision,expectedRevision=s.revision,
                expectedSkillsRevision=s.skillsRevision,expectedInventoryRevision=s.inventoryRevision}
            record.phase="attempt_reserved";record.requestId=f.binding.requestId
        end
        local _,out=tasks.call(c,f,"buddy-professions.attempt",f.binding,"gathering_casting_known_profession")
        if out then return out end
        f.stage="receipt";return tasks.await(c,"gathering_waiting_for_material_receipts")
    end
    if f.stage=="receipt" then
        if not bound(c,f,s) then return tasks.await(c,"gathering_exact_receipt_unavailable") end
        local o=s.operation
        record.operationId=o.operationId;record.status=o.status
        if s.active or o.cleanupUnconfirmed or not o.leaseReleased then return tasks.await(c,"gathering_waiting_for_owned_cleanup") end
        if o.status=="COMPLETED" and o.castTransportWritten and o.ownGoObserved and o.lootWindowObserved
           and o.lootReleaseObserved and o.outputInventoryObserved and n(o.ownLootedItemsObserved)>0
           and n(o.lootSlotsRemoved)>0 and s.inventoryKnown and n(o.observedInventoryRevision)>n(f.binding.expectedInventoryRevision)
           and n(s.inventoryRevision)>=n(o.observedInventoryRevision) then
            record.phase="completed";record.observedInventoryRevision=o.observedInventoryRevision
            record.ownLootedItemsObserved=o.ownLootedItemsObserved;record.lootSlotsRemoved=o.lootSlotsRemoved
            record.skillIncreased=o.skillIncreased or false;record.beforeRank=o.beforeRank;record.afterRank=o.afterRank
            f.confirmed=n(f.confirmed)+1;f.stage="select";f.binding=nil;f.blocked=nil;f.releaseReturned=nil
            return tasks.await(c,"gathering_materials_confirmed")
        end
        if o.status=="BLOCKED" or o.status=="SERVER_REJECTED" or o.status=="CANCELLED" then
            record.phase="blocked";finish(c,f,"blocked","gathering_native_"..o.status);return
        end
        return tasks.await(c,o.status=="OUTCOME_UNCONFIRMED" and "gathering_outcome_unconfirmed" or "gathering_material_proof_incomplete")
    end
end
-- Practice is the same profession policy's pure choice surface. The caller
-- retains durable reservations; native Attempt validates the exact recipe and
-- current body, inventory, skill and resulting game receipts.
-- Spell IDs are sparse dictionary keys, not dense VM array indices.
local practice_tuning={spells={["3275"]=129,["2538"]=185,["2540"]=185,["7620"]=356}}
local function practice_uint(v,maximum)
    return type(v)=="number" and v>=0 and v<=maximum and v==math.floor(v)
end
local function practice_decimal(v,positive)
    if type(v)~="string" or #v==0 or #v>20 then return false end
    if v=="0" then return not positive end
    if byte_at(v,1)==48 then return false end
    for i=1,#v do local b=byte_at(v,i);if b<48 or b>57 then return false end end
    return #v<20 or v<="18446744073709551615"
end
local function practice_array(v,maximum)
    if type(v)~="table" or #v>maximum then return false end
    local count=0
    for k in pairs(v) do
        if not practice_uint(k,#v) or k==0 then return false end
        count=count+1
    end
    return count==#v
end
local function practice_boolean(v) return v==nil or type(v)=="boolean" end
local function practice_blocked(reason) return {status="blocked",reason=reason} end
local function practice_choice(input)
    local invalid="invalid_practice_facts"
    if type(input)~="table" or type(input.sessionId)~="string" or #input.sessionId==0 or #input.sessionId>128
       or not practice_decimal(input.selfGuid,true) or (input.mode~="choose" and input.mode~="validate")
       or not practice_array(input.requestedSkills,3) or #input.requestedSkills==0
       or not practice_array(input.attempts,3) or type(input.professionState)~="table" then
        return practice_blocked(invalid)
    end
    local s=input.professionState
    if s.sessionId~=input.sessionId or s.selfGuid~=input.selfGuid then return practice_blocked(invalid) end
    for _,key in ipairs({"skillsKnown","spellsKnown","inventoryKnown","backpackKnown","active","closing"}) do
        if not practice_boolean(s[key]) then return practice_blocked(invalid) end
    end
    local rawSkills=s.skills==nil and {} or s.skills
    local rawRecipes=s.recipes==nil and {} or s.recipes
    if not practice_decimal(s.revision,false) or not practice_decimal(s.skillsRevision,false)
       or not practice_decimal(s.inventoryRevision,false) or not practice_decimal(s.backpackRevision,false)
       or not practice_array(rawSkills,128) or not practice_array(rawRecipes,7) then
        return practice_blocked(invalid)
    end
    local requested,counts,skills,recipes={},{},{},{}
    for _,skill in ipairs(input.requestedSkills) do
        if not practice_uint(skill,65535) or skill==0 or requested[skill] then return practice_blocked(invalid) end
        requested[skill],counts[skill]=true,0
    end
    for _,row in ipairs(input.attempts) do
        if type(row)~="table" or not practice_uint(row.skillId,65535) or row.skillId==0
           or not practice_uint(row.spellId,2147483647) or row.spellId==0 then return practice_blocked(invalid) end
        if requested[row.skillId] then counts[row.skillId]=counts[row.skillId]+1 end
    end
    for _,row in ipairs(rawSkills) do
        if type(row)~="table" or not practice_uint(row.skillId,65535) or row.skillId==0 or skills[row.skillId]
           or not practice_uint(row.rank,65535) or row.rank==0 or not practice_uint(row.maximum,65535)
           or row.rank>row.maximum then return practice_blocked(invalid) end
        skills[row.skillId]=row
    end
    for _,row in ipairs(rawRecipes) do
        if type(row)~="table" or not practice_uint(row.skillId,65535) or row.skillId==0
           or not practice_uint(row.spellId,2147483647) or row.spellId==0 or recipes[row.spellId] then
            return practice_blocked(invalid)
        end
        for _,key in ipairs({"known","ready","requirementsVerified"}) do
            if not practice_boolean(row[key]) then return practice_blocked(invalid) end
        end
        for _,key in ipairs({"reagentEntry","reagentCount","outputEntry","outputCount","focusId","castTimeMs","channelDurationMs"}) do
            if not practice_uint(row[key]==nil and 0 or row[key],4294967295) then return practice_blocked(invalid) end
        end
        if row.reason~=nil and (type(row.reason)~="string" or #row.reason>1024) then return practice_blocked(invalid) end
        recipes[row.spellId]=row
    end
    if type(practice_tuning.spells)~="table" then return practice_blocked("invalid_practice_tuning") end
    local configured,seen=0,{}
    for key,skill in pairs(practice_tuning.spells) do
        local spell=type(key)=="string" and tonumber(key) or key
        if not practice_uint(spell,2147483647) or spell==0 or type(key)=="string" and tostring(spell)~=key
           or not practice_uint(skill,65535) or skill==0 then return practice_blocked("invalid_practice_tuning") end
        local canonical=tostring(spell)
        if not seen[canonical] then configured=configured+1;seen[canonical]=true end
        if configured>7 then return practice_blocked("invalid_practice_tuning") end
    end
    if s.closing then return practice_blocked("module_closing") end
    if s.active then return practice_blocked("another_operation_active") end
    local operation=s.operation
    if operation~=nil then
        if type(operation)~="table" or not practice_boolean(operation.cleanupUnconfirmed) then return practice_blocked(invalid) end
        local status=operation.status
        if status~="RESERVED" and status~="WAITING_CAST" and status~="WAITING_BOBBER" and status~="WAITING_LOOT"
           and status~="WAITING_OBSERVATIONS" and status~="COMPLETED" and status~="BLOCKED"
           and status~="SERVER_REJECTED" and status~="CANCELLED" and status~="OUTCOME_UNCONFIRMED" then
            return practice_blocked(invalid)
        end
        if operation.cleanupUnconfirmed or status=="OUTCOME_UNCONFIRMED" then
            return practice_blocked("prior_operation_unconfirmed")
        end
    end
    if s.skillsKnown~=true or s.spellsKnown~=true or s.inventoryKnown~=true
       or s.revision=="0" or s.skillsRevision=="0" or s.inventoryRevision=="0" then
        return practice_blocked("no_currently_ready_known_recipe")
    end
    local function eligible(row)
        local configured=row and practice_tuning.spells[row.spellId]
        if row and configured==nil then configured=practice_tuning.spells[tostring(row.spellId)] end
        return row and requested[row.skillId] and skills[row.skillId]
           and configured==row.skillId
           and row.known==true and row.ready==true and row.requirementsVerified==true
    end
    if input.mode=="validate" then
        local selected=input.selected
        if type(selected)~="table" or not practice_uint(selected.skillId,65535) or selected.skillId==0
           or not practice_uint(selected.spellId,2147483647) or selected.spellId==0 then return practice_blocked(invalid) end
        local row=recipes[selected.spellId]
        if eligible(row) and row.skillId==selected.skillId then
            return {status="ready",skillId=row.skillId,spellId=row.spellId}
        end
        return practice_blocked("no_currently_ready_known_recipe")
    end
    local chosen
    for _,row in ipairs(rawRecipes) do
        if eligible(row) and (not chosen or counts[row.skillId]<counts[chosen.skillId]
           or counts[row.skillId]==counts[chosen.skillId] and (row.skillId<chosen.skillId
               or row.skillId==chosen.skillId and row.spellId<chosen.spellId)) then chosen=row end
    end
    if not chosen then return practice_blocked("no_currently_ready_known_recipe") end
    return {status="selected",skillId=chosen.skillId,spellId=chosen.spellId}
end
return {handlers=handlers,practice_choice=practice_choice,practice_tuning=practice_tuning,
    push=function(context,params) tasks.push(context,"gathering",params) end,
    decide=function(state)
        if #(state.pending or {})>0 then return {memory=state.memory,phase="blocked_on_ambiguous_call",done=false} end
        return tasks.run(state,handlers,"gathering")
    end}
