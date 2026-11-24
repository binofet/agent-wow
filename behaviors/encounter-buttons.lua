-- Pure encounter duty selection. The native caller supplies observed devices
-- and retains exact movement/use admission and uncertain delivery custody.
local M={tuning={role_classes={[4]=true},prefer_nearest=true,scan_ms=400,retry_ms=1000,confirm_ms=800}}
local function number(n) return type(n)=="number" and n==n and n-n==0 end
local function uint(n) return number(n) and n>=0 and n<=4294967295 and n%1==0 end
local function decimal(s,zero)
    if type(s)~="string" or #s==0 or #s>20 or #s>1 and byte_at(s,1)==48 or #s==20 and s>"18446744073709551615" then return false end
    for i=1,#s do local b=byte_at(s,i);if b<48 or b>57 then return false end end
    return zero or s~="0"
end
local function point(p)
    return type(p)=="table" and number(p.x or 0) and number(p.y or 0) and number(p.z or 0)
end
local function pair(out,p)
    out.faceGuid,out.buttonGuid=p.faceGuid or "0",p.buttonGuid or "0"
    out.faceEntry,out.buttonEntry=p.faceEntry or 0,p.buttonEntry or 0
    return out
end
local function result(kind,memory,current)
    return pair({kind=kind,memory=memory},current or {})
end
local function object(o,map)
    return type(o)=="table" and decimal(o.guid) and uint(o.entry) and o.present==true and o.mapKnown==true and o.mapId==map and
        o.typeKnown==true and o.gameObjectType==1 and o.stateKnown==true and o.positionKnown==true and point(o)
end
local function boss_active(input)
    for _,b in ipairs(input.bosses or {}) do
        if type(b)=="table" and decimal(b.guid) and b.entry==7800 and b.present==true and b.mapKnown==true and b.mapId==90 and
           b.aliveKnown==true and b.alive==true and b.healthKnown==true and uint(b.health) and b.health>0 and
           b.unitFlagsKnown==true and uint(b.unitFlags) and math.floor(b.unitFlags/0x80000)%2==1 then return true end
    end
    return false
end
local function usable(button)
    local flags=button.flags
    return button.flagsKnown==true and uint(flags) and flags%2==0 and math.floor(flags/2)%2==0 and math.floor(flags/16)%2==0
end
local function current_pair(c)
    return type(c)=="table" and decimal(c.faceGuid) and decimal(c.buttonGuid) and uint(c.faceEntry) and c.faceEntry>0 and uint(c.buttonEntry) and c.buttonEntry>0
end
local function candidate_pair(row)
    return {faceGuid=row.face.guid,buttonGuid=row.button.guid,faceEntry=row.face.entry,buttonEntry=row.button.entry}
end
local function match(row,c)
    return type(row)=="table" and row.face and row.face.guid==c.faceGuid and row.face.entry==c.faceEntry and
        row.button and row.button.guid==c.buttonGuid and row.button.entry==c.buttonEntry
end
function M.decide(input)
    local memory=type(input)=="table" and type(input.memory)=="table" and input.memory or {}
    local c=type(input)=="table" and type(input.current)=="table" and input.current or {}
    local invalid=result("wait",memory,c)
    if type(input)~="table" or input.selection~="thermaplugg_buttons" or type(input.sessionId)~="string" or input.sessionId=="" or
       not decimal(input.selfGuid) or not decimal(input.visitId) or not number(input.nowMs) or math.abs(input.nowMs)>9007199254740000 or
       type(input.independent)~="boolean" or type(input.assisting)~="boolean" or type(input.observationsKnown)~="boolean" or
       type(input.candidates)~="table" or #input.candidates>6 or not point(input.clientPosition) then return invalid end
    local tuning=M.tuning
    if type(tuning)~="table" or type(tuning.role_classes)~="table" or type(tuning.prefer_nearest)~="boolean" then return invalid end
    for _,key in ipairs({"scan_ms","retry_ms","confirm_ms"}) do if not uint(tuning[key]) or tuning[key]>60000 then return invalid end end
    local now=input.nowMs
    if memory.sessionId~=input.sessionId or memory.selfGuid~=input.selfGuid or memory.visitId~=input.visitId then
        memory={sessionId=input.sessionId,selfGuid=input.selfGuid,visitId=input.visitId,retryAfter={}}
    end
    if type(memory.retryAfter)~="table" then return invalid end
    if memory.now and (not number(memory.now) or now<memory.now) then memory.nextScan=nil;memory.retryAfter={} end
    memory.now=now
    local attempt=type(c.attempt)=="table" and c.attempt or {}
    if attempt.attempted==true and attempt.pending~=false and (attempt.sessionId~=input.sessionId or attempt.selfGuid~=input.selfGuid or attempt.visitId~=input.visitId) then
        return result("wait",memory,c)
    end
    local pending=attempt.attempted==true and attempt.pending~=false and attempt.sessionId==input.sessionId and attempt.selfGuid==input.selfGuid and attempt.visitId==input.visitId
    if pending then
        local p=current_pair(attempt) and attempt or c
        if not current_pair(p) then return result("wait",memory,c) end
        if memory.attemptAt~=attempt.sentAtMs or memory.attemptFace~=p.faceGuid or memory.attemptButton~=p.buttonGuid then
            memory.confirmAfter=nil;memory.attemptAt=attempt.sentAtMs;memory.attemptFace=p.faceGuid;memory.attemptButton=p.buttonGuid
        end
        -- Delivery uncertainty is a native fact. Resetting policy memory,
        -- disabling duty, or changing a timer cannot authorize another use.
        if not input.observationsKnown then
            local due=memory.confirmAfter or (number(attempt.sentAtMs) and attempt.sentAtMs+tuning.confirm_ms)
            if not number(due) or now<due then return result("wait",memory,p) end
            return result("observe",memory,p)
        end
        for _,row in ipairs(input.candidates) do
            local face=row.face
            if object(face,90) and face.guid==p.faceGuid and face.entry==p.faceEntry and face.state==1 then
                return result("confirmed",memory,p)
            end
        end
        memory.confirmAfter=now+tuning.confirm_ms
        return result("wait",memory,p)
    end
    local self,owner=input.self,input.owner
    local active=type(self)=="table" and self.guid==input.selfGuid and self.present==true and self.mapKnown==true and self.mapId==90 and
        self.classKnown==true and tuning.role_classes[self.classId]==true and type(owner)=="table" and owner.present==true and
        owner.mapKnown==true and owner.mapId==90 and input.independent==false and input.assisting==true and boss_active(input)
    if not active then return result(current_pair(c) and "clear" or "idle",memory,c) end
    if input.feedback~=nil and input.feedback~="" then
        if current_pair(c) then memory.retryAfter[c.faceGuid]=now+tuning.retry_ms end
        return result("clear",memory,c)
    end
    if not current_pair(c) and memory.nextScan and (not number(memory.nextScan) or now<memory.nextScan) then return result("wait",memory) end
    if not input.observationsKnown then return result("observe",memory,c) end
    if current_pair(c) then
        for _,row in ipairs(input.candidates) do
            if match(row,c) then
                if object(row.face,90) and row.face.state==1 then return result("clear",memory,c) end
                if object(row.face,90) and row.face.state==0 and object(row.button,90) and usable(row.button) then return result("choose",memory,c) end
            end
        end
        memory.retryAfter[c.faceGuid]=now+tuning.retry_ms
        return result("clear",memory,c)
    end
    memory.nextScan=now+tuning.scan_ms
    local selected,best
    for _,row in ipairs(input.candidates) do
        if type(row)=="table" and object(row.face,90) and row.face.state==0 and object(row.button,90) and usable(row.button) then
            local retry=memory.retryAfter[row.face.guid]
            if retry==nil or number(retry) and now>=retry then
                local dx,dy,dz=(row.button.x or 0)-(input.clientPosition.x or 0),(row.button.y or 0)-(input.clientPosition.y or 0),(row.button.z or 0)-(input.clientPosition.z or 0)
                local d=dx*dx+dy*dy+dz*dz
                if not selected or tuning.prefer_nearest and d<best or not tuning.prefer_nearest and d>best then selected,best=row,d end
            end
        end
    end
    return result(selected and "choose" or "idle",memory,selected and candidate_pair(selected) or nil)
end
return M
