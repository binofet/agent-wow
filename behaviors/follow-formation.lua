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

-- Town planning and the finite service workers consult this export from the
-- character's current accepted policy. Callers supply mechanically validated
-- observed NPCs; this policy owns the search radius and destination order.
local function inventory_maintenance(input)
    local function uint(v,max) return number(v) and v>=0 and v<=max and v%1==0 end
    local function array(rows,max)
        if type(rows)~="table" or #rows>max then return false end
        local n=0
        for k in pairs(rows) do if not uint(k,max) or k<1 then return false end; n=n+1 end
        return n==#rows
    end
    local function item(row)
        return type(row)=="table" and uint(row.bag,4) and uint(row.slot,35) and identity(row.itemGuid)~=nil and
            uint(row.entry,4294967295) and row.entry>0 and uint(row.stackCount,4294967295) and row.stackCount>0
    end
    if type(input.counts)~="table" or not uint(input.counts.equip,192) or not uint(input.counts.claim,192) or
       not uint(input.counts.sell,192) or not array(input.emptyEquipmentSlots,4) or not array(input.bagCandidates,160) or
       not array(input.saleCandidates,160) or not array(input.vendors,96) or type(input.vendorsKnown)~="boolean" or
       type(input.mailAvailable)~="boolean" or type(input.freeGeneralSlot)~="boolean" or type(input.claimsOnly)~="boolean" or
       type(input.vendorExplicit)~="boolean" then return {} end
    local empty,seen
    seen={}
    for _,slot in ipairs(input.emptyEquipmentSlots) do
        if not uint(slot,22) or slot<19 or seen[slot] then return {} end
        seen[slot]=true
        if not empty or slot<empty then empty=slot end
    end
    local function first(rows)
        local best,seen=nil,{}
        for _,row in ipairs(rows) do
            if not item(row) or seen[row.itemGuid] then return nil,false end
            seen[row.itemGuid]=true
            if not best or row.bag<best.bag or row.bag==best.bag and row.slot<best.slot then best=row end
        end
        return best,true
    end
    local bag,bags_valid=first(input.bagCandidates)
    local sale,sales_valid=first(input.saleCandidates)
    if not bags_valid or not sales_valid then return {} end
    seen={}
    for _,candidate in ipairs(input.vendors) do
        if type(candidate)~="table" or not identity(candidate.guid) or seen[candidate.guid] or
           not number(candidate.distance) or candidate.distance<0 then return {} end
        seen[candidate.guid]=true
    end
    local function finish(phase,reason) return {choice={kind="finish",phase=phase,reason=reason or ""}} end
    -- One editable ordering, shared by the finite worker's planning/dispatch.
    local priorities={"equip","claim","sell"}
    for _,kind in ipairs(priorities) do
        if kind=="equip" and empty and bag and input.counts.equip<4 then
            return {choice={kind="equip",source=bag,equipmentSlot=empty}}
        elseif kind=="claim" and input.mailAvailable and empty and input.freeGeneralSlot and input.counts.claim<4 then
            return {choice={kind="claim"}}
        elseif kind=="sell" and not input.claimsOnly and input.counts.sell<160 then
            if not input.vendorsKnown then return {choice={kind="discover_sales"}} end
            local vendors={}
            if input.vendorExplicit then
                if #input.vendors==1 then vendors={input.vendors[1].guid} end
            else
                -- Reuse the existing destination policy in this same accepted
                -- source evaluation; do not carry a prior revision's ranking.
                local ranked=M.service_destination({kind="vendor",scope="interactable",candidates=input.vendors})
                vendors=type(ranked)=="table" and ranked.vendors or nil
                if type(vendors)~="table" then return {} end
            end
            if #vendors==0 then return finish("blocked","no_actual_in_range_vendor_no_travel_attempted") end
            if sale then return {choice={kind="sell",source=sale,vendorGuid=vendors[1]}} end
        end
    end
    if input.claimsOnly then
        if input.mailAvailable and empty and not input.freeGeneralSlot then
            return finish("blocked","known_general_slot_for_bag_attachment_required")
        end
        return finish("complete","finite_bag_maintenance_finished")
    end
    if input.counts.sell<160 then return finish("complete","no_observed_safe_junk_sale") end
    return finish("operation_limit")
end
function M.service_destination(input)
    if type(input)=="table" and input.kind=="inventory_maintenance" then return inventory_maintenance(input) end
    if type(input)~="table" or (input.kind~="vendor" and input.kind~="trainer") or
       (input.scope~="nearby" and input.scope~="interactable" and input.scope~="travel") or
       type(input.candidates)~="table" or #input.candidates>97 then return {} end
    local trainer=input.kind=="trainer"
    if not trainer and (input.scope=="travel" or #input.candidates>96) then return {} end
    local radius=trainer and trainer_search_yards or
        (input.scope=="nearby" and vendor_search_yards or vendor_interaction_yards)
    local prefer_nearest=trainer and trainer_prefer_nearest or not trainer and vendor_prefer_nearest
    if not number(radius) or radius<=0 then return {} end
    local ranked,seen={},{}
    for _,candidate in ipairs(input.candidates) do
        if type(candidate)~="table" or not identity(candidate.guid) or seen[candidate.guid] or
           not number(candidate.distance) or candidate.distance<0 or
           trainer and type(candidate.interactable)~="boolean" then return {} end
        seen[candidate.guid]=true
        local within=candidate.distance<=radius
        local eligible=false
        if trainer then
            if candidate.interactable then
                if input.scope~="travel" then eligible=true end
            elseif input.scope~="interactable" and candidate.distance>trainer_interaction_yards and within then
                eligible=true
            end
        elseif within then eligible=true end
        if eligible then
            local index=#ranked+1
            for i,other in ipairs(ranked) do
                local before=candidate.distance~=other.distance and
                    ((prefer_nearest and candidate.distance<other.distance) or
                     (not prefer_nearest and candidate.distance>other.distance))
                if candidate.distance==other.distance then
                    before=#candidate.guid<#other.guid or #candidate.guid==#other.guid and candidate.guid<other.guid
                end
                if trainer and candidate.interactable~=other.interactable then before=candidate.interactable end
                if before then index=i;break end
            end
            for i=#ranked,index,-1 do ranked[i+1]=ranked[i] end
            ranked[index]=candidate
        end
    end
    local vendors={}
    for i,candidate in ipairs(ranked) do vendors[i]=candidate.guid end
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
