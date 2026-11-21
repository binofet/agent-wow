-- A personal follow slot and its restart policy. Navigation and movement
-- custody belong to the caller. Memory is explicit so both Lua hosts and the
-- builtin native bridge use the same state transition.
local M = {}
local tau, half_turn = 6.283185307179586, 3.141592653589793
local roles = {[5]={3.8,-3.1}, [8]={5.1,.2}, [4]={2.4,3.5}, [9]={1,-5.2}}
local near_stop, near_limit = 3,30
local portal_ahead, portal_side = {8,12,16}, {-6,6}
local portal_wait_ms, portal_seen_ms, portal_catchup_ms = 500,20000,75000
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

    if input.mode=="portal_follow" then
        local p=input.portal
        local eligible=type(p)=="table" and p.following==true and p.ownerAbsent==true and
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
        if not input.self then return {} end
        local candidates={{x=owner.x,y=owner.y,z=owner.z}}
        local dx,dy=owner.x-input.self.x,owner.y-input.self.y
        local gap=math.sqrt(dx*dx+dy*dy)
        if gap>45 then
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
