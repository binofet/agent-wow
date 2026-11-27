-- Free per-character discovery during ordinary play. The exact native query
-- retains wire custody; the realm's status, learning notice and menu are
-- distinct observations. This policy never selects a destination or a fare.
local M={tuning={nearby_yards=12,owner_yards=18,lease_ms=12000,retry_ms=15000,attempt_ms=8500}}
local state={known={},blocked={},serial=0}
local function number(n) return type(n)=="number" and n==n and n>-math.huge and n<math.huge end
local function id(s) return type(s)=="string" and s~="" and s~="0" end
local function distance(a,b)
    if not a or not b or a.positionKnown~=true or b.positionKnown~=true or a.mapKnown~=true or b.mapKnown~=true or
       a.mapId~=b.mapId then return end
    local x,y,z=(a.x or 0)-(b.x or 0),(a.y or 0)-(b.y or 0),(a.z or 0)-(b.z or 0)
    if number(x) and number(y) and number(z) then return math.sqrt(x*x+y*y+z*z),math.abs(z) end
end
local function next_revision(revision)
    if type(revision)~="string" or #revision==0 or #revision>20 or revision=="18446744073709551615" then return end
    local s,carry="",1
    for i=#revision,1,-1 do
        local d=byte_at(revision,i)-48
        if d<0 or d>9 then return end
        d=d+carry;carry=d>=10 and 1 or 0;s=tostring(d%10)..s
    end
    return carry==1 and "1"..s or s
end
local function save_known(key,node)
    state.knownAt=state.knownAt or {}
    if not state.known[key] then
        local count,oldest,oldestAt=0,nil,nil
        for guid in pairs(state.known) do
            count=count+1
            local at=state.knownAt[guid] or 0
            if not oldest or at<oldestAt or at==oldestAt and guid<oldest then oldest,oldestAt=guid,at end
        end
        if count>=128 then state.known[oldest]=nil;state.knownAt[oldest]=nil end
    end
    state.known[key]=node or true
    state.knownAt[key]=state.serial
end
local function withhold(t)
    if not t.query or state.blocked[t.guid] then return end
    local count=0
    for _ in pairs(state.blocked) do count=count+1 end
    if count>=32 then state.overflowVisit=t.visit;return end
    state.blocked[t.guid]={session=t.session,visit=t.visit,guid=t.guid,query=t.query}
end
local function eligible(obs,npc)
    local f=npc and npc.flightmaster
    local flags=f and f.movementFlags or 0
    if not f or f.factionTemplateFriendly~=true or npc.present~=true or npc.aliveKnown~=true or npc.alive~=true or
       npc.healthKnown~=true or (npc.health or 0)<=0 or npc.unitFlagsKnown~=true or
       math.floor((npc.unitFlags or 0)/0x80000)%2==1 or math.floor((npc.unitFlags or 0)/0x2000000)%2==1 or
       not id(npc.guid) or not id(npc.positionRevision) or npc.mapId~=obs.mapId or
       flags%256~=0 or flags%0x10000000>0x100 or flags>=0x80000000 then return false end
    local trajectory=npc.trajectory
    if trajectory and trajectory.known and (not trajectory.stopped or trajectory.transportRelative) then return false end
    return f.stationaryPositionKnown==true or (tonumber(f.positionAgeMs or "0") or math.huge)<=2000 and
        (tonumber(f.observationAgeMs or "0") or math.huge)<=2000
end
local function near_owner(o,npc,follows)
    local nav=o.navigation
    if not follows then return true end
    local owner=nav.followOwner
    if not owner or owner.guid~=nav.ownerGuid then owner=(o.players or {})[nav.ownerGuid] end
    local d=owner and owner.present and owner.guid==nav.ownerGuid and owner.aliveKnown and owner.alive and
        owner.combatKnown and not owner.inCombat and not owner.positionInferred and distance(owner,npc)
    return d and d<=M.tuning.owner_yards
end
local function select_target(o,self,follows)
    local best,best_distance
    for key,npc in pairs(o.targets or {}) do
        if npc.guid==key and not state.known[key] and not state.blocked[key] and eligible(o,npc) and near_owner(o,npc,follows) then
            local d=distance(self,npc)
            if d and d<=M.tuning.nearby_yards and (not best_distance or d<best_distance or d==best_distance and key<best.guid) then
                best,best_distance=npc,d
            end
        end
    end
    return best
end
local function same_op(t,op)
    local q=t.query
    return q and op and op.sessionId==t.session and op.selfGuid==state.self and op.visitId==t.visit and
        op.npcGuid==t.guid and op.revision==q.revision and op.mode==q.mode
end
local function observe_query(t,fp)
    local op=fp.operation
    if not same_op(t,op) or fp.revision~=op.revision or op.pending==true then return end
    if op.phase=="not_dispatched" and not op.transportWritten and not op.transportUnknown then
        t.query=nil;t.finish="query_not_dispatched";return
    end
    if op.transportWritten~=true and op.transportUnknown~=true then return end
    if op.phase=="status_observed" and op.mode=="status" and op.statusKnown then
        if op.nodeKnown then save_known(t.guid);t.finish=t.finish or "already_known" else t.stage="learn" end
    elseif op.phase=="learned" and op.mode=="discover" and op.learned and op.statusKnown and op.nodeKnown and op.learningNoticeObserved then
        save_known(t.guid);t.stage="menu";t.learned=true
    elseif op.phase=="menu_observed" and op.mode=="discover" and op.menuKnown and number(op.sourceNode) and op.sourceNode>0 then
        -- A menu can arrive without a learning notice. Its source is factual,
        -- its displayed mask can include nodes this character has not learned.
        if state.known[t.guid] then save_known(t.guid,op.sourceNode) end
        t.finish=t.finish or "menu_observed";t.node=op.sourceNode
    else return end
    state.blocked[t.guid]=nil;t.query=nil
end
local function action(t,capability,fields)
    local a=fields or {};a.capability=capability;a.sessionId=t.session;a.selfGuid=state.self;a.visitId=t.visit
    state.serial=state.serial+1
    local request="flightpoint:"..t.guid..":"..t.visit..":"..tostring(state.serial)
    t.waiting={id=request,capability=capability}
    return a,request
end
function M.on_event(e)
    local t=state.current
    if not t then return true end
    if e.kind=="command" then t.finish=t.finish or "owner_command";return true end
    local r,w=e.receipt,t.waiting
    if not r or not w then return true end
    local p=r.payload or {}
    if p.policy_request_id~=w.id and (not w.actionId or r.action_id~=w.actionId) then return true end
    w.actionId=r.action_id
    if r.outcome~="failed" and r.outcome~="not_dispatched" and r.outcome~="confirmed" and r.outcome~="settled_unverified" and r.outcome~="unknown" then return true end
    t.waiting=nil
    if r.outcome=="not_dispatched" then
        if w.capability=="query_flightpath" then t.query=nil end
        if w.capability=="release_interaction" then t.releaseIssued=true end
        t.finish=t.finish or "action_refused";return true
    end
    local result=p.result or {}
    if w.capability=="acquire_idle_interaction" then
        local s=result.state or {}
        if r.outcome=="confirmed" and s.sessionId==t.session and s.selfGuid==state.self and s.interactionOwner=="exploration" and id(result.token) then
            t.token=result.token
        else t.finish="lease_unconfirmed" end
    elseif w.capability=="travel_to" then
        if result.sessionId~=t.session or result.selfGuid~=state.self or result.motionUnconfirmed or p.reconciliationRequired or
           result.reason~="exploration_flightmaster_checkpoint_reached" and result.reason~="exploration_within_distance" then t.finish="approach_interrupted" end
    elseif w.capability=="release_interaction" then
        t.releaseIssued=true
    end
    return true
end
local function safe_party(o)
    local party=(((o.party or {}).group or {}).members or {})
    for _,member in ipairs(party) do
        local player=(o.players or {})[member.guid]
        if member.online and player and player.inCombat then return false end
    end
    for _,npc in pairs(o.targets or {}) do
        if npc.engaged and npc.alive then return false end
    end
    return true
end
function M.active() return state.current~=nil end
function M.decide(frame,o,quiet,available,defaultFollow)
    local nav=o.navigation or {};local self=(o.players or {})[o.selfGuid];local fp=o.flightpath
    local now=frame.clock and frame.clock.monotonicMs
    if not number(now) or not id(o.selfGuid) or not id(o.visitId) or not o.interaction or not self then return end
    if state.self and state.self~=o.selfGuid then state={known={},blocked={},serial=0} end
    state.self=o.selfGuid
    if state.session~=o.sessionId then state.session=o.sessionId;state.retryAt=nil end
    local t=state.current
    if t and (t.session~=o.sessionId or t.visit~=o.visitId) then
        withhold(t)
        state.current=nil;t=nil
    end
    if fp and fp.sessionId==o.sessionId and fp.selfGuid==o.selfGuid and fp.visitBound==true and fp.visitId==o.visitId then
        local op=fp.operation
        local previous=op and state.blocked[op.npcGuid]
        if previous then
            observe_query(previous,fp)
            if not previous.query then state.blocked[op.npcGuid]=nil end
        end
    else
        if not t then return end
        fp={};t.finish=t.finish or "flightpoint_observation_lost"
    end
    local rev=nav.followRevision or "0"
    local follows=nav.following==true or defaultFollow==true and nav.independent~=true
    local idle=quiet and self.present and self.aliveKnown and self.alive and self.combatKnown and not self.inCombat and safe_party(o) and
        self.movementKnown and not self.moving and not self.swimming and not self.positionInferred and self.positionKnown and
        not nav.motionUnconfirmed and not (o.recovery or {}).active and not (nav.movementOwner or {}).active and (nav.following or rev=="0") and
        not (fp.activation or {}).pending
    if not t then
        if not idle or state.overflowVisit==o.visitId or now<(state.retryAt or 0) or (o.interaction.owner or "")~="" or (fp.operation or {}).pending then return end
        local npc=select_target(o,self,follows)
        if not npc then return end
        t={session=o.sessionId,visit=o.visitId,guid=npc.guid,position=npc.positionRevision,follow=rev,
            follows=follows,owner=nav.ownerGuid,stage="status",deadline=now+M.tuning.attempt_ms};state.current=t
    end
    if t.query then observe_query(t,fp) end
    if t.waiting then return end
    local npc=(o.targets or {})[t.guid]
    if not idle or t.follow~=rev or t.follows~=follows or t.owner~=nav.ownerGuid or not eligible(o,npc) or npc.positionRevision~=t.position or
       not near_owner(o,npc,follows) or now>=t.deadline then t.finish=t.finish or "opportunity_ended" end
    if t.token and (o.interaction.token~=t.token or o.interaction.owner~="exploration") then
        t.token=nil;t.finish=t.finish or "lease_ended"
    end
    if t.finish then
        withhold(t)
        if t.token and not t.releaseIssued then
            if not available then return end
            return action(t,"release_interaction",{owner="exploration",token=t.token})
        end
        state.last={guid=t.guid,visit=t.visit,result=t.finish,learned=t.learned,node=t.node}
        state.current=nil;state.retryAt=now+M.tuning.retry_ms
        return
    end
    if t.query or #frame.pending~=0 then return end
    if not t.token then
        if (o.interaction.owner or "")~="" then t.finish="foreign_lease";return end
        return action(t,"acquire_idle_interaction",{owner="exploration",leaseMs=M.tuning.lease_ms})
    end
    local d,z=distance(self,npc)
    if not d or d>M.tuning.nearby_yards then t.finish="flightmaster_out_of_range";return end
    local target={guid=t.guid,positionRevision=t.position,followRevision=t.follow}
    if follows then target.followOwnerGuid=t.owner;target.ownerRangeYards=M.tuning.owner_yards end
    if d>5 or z>2.5 then
        if nav.groundNavigationConfigured~=true or not number(self.groundRunSpeed) or self.groundRunSpeed<=0 then
            t.finish="ground_approach_unavailable";return
        end
        return action(t,"travel_to",{token=t.token,mapId=o.mapId,x=npc.x or 0,y=npc.y or 0,z=npc.z or 0,
            arrivalDistance=2.5,flightmaster=target})
    end
    local revision=fp.revision or "0";local next=next_revision(revision)
    if not next or (fp.operation or {}).pending then t.finish="query_occupied";return end
    local mode=t.stage=="status" and "status" or "discover"
    t.query={revision=next,mode=mode}
    return action(t,"query_flightpath",{token=t.token,npcGuid=t.guid,mode=mode,expectedRevision=revision,opportunity=target})
end
function M.export_state() return {version=1,data=state} end
function M.import_state(checkpoint)
    if type(checkpoint)~="table" or checkpoint.version~=1 or type(checkpoint.data)~="table" then return false end
    local s=checkpoint.data
    if type(s.known)~="table" or type(s.blocked)~="table" or not number(s.serial) or s.serial<0 then return false end
    state=s;return true
end
return M
