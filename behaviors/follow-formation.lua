-- A personal follow slot and its restart policy. Navigation and movement
-- custody belong to the caller. Memory is explicit so both Lua hosts and the
-- builtin native bridge use the same state transition.
local M = {}
local tau, half_turn = 6.283185307179586, 3.141592653589793
local roles = {[5]={3.8,-3.1}, [8]={5.1,.2}, [4]={2.4,3.5}, [9]={1,-5.2}}
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
       input.ownerArrival~=nil and type(input.ownerArrival)~="boolean" then return {} end

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
