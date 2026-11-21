-- Choose one NPC quest operation from independent owner and own-log facts.
-- Transport, fresh offer checks and the durable mutation ledger stay outside.
local M = {}
local reward_givers = {[783]=197,[7]=197,[15]=197,[18]=823,[21]=197,[54]=240,[3903]=9296,[5261]=196,[33]=196}
local function uint(v,max) return type(v)=="number" and v>=0 and v<=max and v%1==0 end
local function number(v) return type(v)=="number" and v-v==0 end
local function array(v,max)
    if type(v)~="table" or #v>max then return false end
    local n=0
    for k in pairs(v) do if not uint(k,max) or k<1 then return false end; n=n+1 end
    return n==#v
end
local function text(v,max) return type(v)=="string" and #v>0 and #v<=max end
local function decimal(v)
    if not text(v,20) or #v>1 and byte_at(v,1)==48 or #v==20 and v>"18446744073709551615" then return false end
    for i=1,#v do local d=byte_at(v,i); if d<48 or d>57 then return false end end
    return true
end
local function ids(rows,max)
    if not array(rows,max) then return end
    local out={}
    for _,q in ipairs(rows) do if not uint(q,4294967295) or q==0 or out[q] then return end; out[q]=true end
    return out
end
local function sorted_ids(set)
    local rows={}
    for q in pairs(set) do
        local i=#rows+1
        while i>1 and q<rows[i-1] do rows[i]=rows[i-1]; i=i-1 end
        rows[i]=q
    end
    return rows
end
local function same(a,b)
    if type(a)~="table" or type(b)~="table" or #a~=#b then return false end
    for i=1,#a do if a[i]~=b[i] then return false end end
    return true
end
local function pose(p)
    return type(p)=="table" and p.known==true and uint(p.mapId,4294967295) and
        number(p.x) and math.abs(p.x)<40000 and number(p.y) and math.abs(p.y)<40000 and
        number(p.z) and math.abs(p.z)<40000
end
local function distance(a,b)
    local x,y,z=a.x-b.x,a.y-b.y,a.z-b.z
    return math.sqrt(x*x+y*y+z*z)
end
local function bit(v,p) return math.floor(v/p)%2==1 end
local function less_guid(a,b) return #a<#b or #a==#b and a<b end
local function less_giver(a,b)
    return a.ownerDistance<b.ownerDistance or a.ownerDistance==b.ownerDistance and
        (a.distance<b.distance or a.distance==b.distance and less_guid(a.guid,b.guid))
end
local function valid_offer(o)
    return type(o)=="table" and decimal(o.npcGuid) and decimal(o.revision) and decimal(o.expiresInMs)
end
local function offer_defaults(o)
    if type(o)~="table" then return end
    return {npcGuid=o.npcGuid or "0",revision=o.revision or "0",expiresInMs=o.expiresInMs or "0",
        questId=o.questId or 0,source=o.source,canComplete=o.canComplete,rewardRequested=o.rewardRequested,
        quests=o.quests or {}}
end
local function reward_offer(o,q,g)
    return valid_offer(o) and o.questId==q and o.npcGuid==g.guid and o.source=="reward_offer" and
        o.canComplete==true and o.rewardRequested~=true and o.expiresInMs~="0" and o.revision~="0"
end
local function signature(v)
    if not array(v,4) then return false end
    for _,s in ipairs(v) do if type(s)~="string" or #s>512 then return false end end
    return true
end
-- Exact finite clock facts are decimal parts, so large prior query times keep
-- their original ordering without crossing the bridge's exact-number bound.
local function clock_parts(v)
    if type(v)=="table" and v.infinite~=nil and type(v.infinite)~="boolean" then return false end
    if type(v)=="table" and v.infinite==true then
        return type(v.negative)=="boolean" and v.whole==nil and v.fraction==nil
    end
    if type(v)~="table" or type(v.negative)~="boolean" or type(v.whole)~="string" or
       #v.whole==0 or #v.whole>309 or type(v.fraction)~="string" or #v.fraction>1074 or
       #v.whole>1 and byte_at(v.whole,1)==48 or #v.fraction>0 and byte_at(v.fraction,#v.fraction)==48 then return false end
    for _,part in ipairs({v.whole,v.fraction}) do
        for i=1,#part do local c=byte_at(part,i); if c<48 or c>57 then return false end end
    end
    return not (v.negative and v.whole=="0" and v.fraction=="")
end
local function clock_less(a,b)
    if a.infinite==true then
        if b.infinite==true then return a.negative and not b.negative end
        return a.negative
    end
    if b.infinite==true then return not b.negative end
    if a.negative~=b.negative then return a.negative end
    local function magnitude_less(x,y)
        if #x.whole~=#y.whole then return #x.whole<#y.whole end
        if x.whole~=y.whole then return x.whole<y.whole end
        return x.fraction<y.fraction
    end
    if a.negative then return magnitude_less(b,a) end
    return magnitude_less(a,b)
end
local function starter_pickup(input)
    local function ready(choice) return {status="ready",choice=choice} end
    local invalid={status="invalid"}
    local active,allowed=ids(input.activeQuestIds,25),ids(input.allowedQuestIds,25)
    if not active or not allowed or not text(input.sessionId,128) or type(input.autonomous)~="boolean" or
       not uint(input.neededEntry,4294967295) or not array(input.candidates,64) or type(input.reservations)~="table" then return invalid end
    local givers={}
    for _,g in ipairs(input.candidates) do
        if type(g)~="table" or not decimal(g.guid) or not uint(g.priority,4294967295) or
           not number(g.distance) or g.distance<0 or g.distance>9007199254740991 or not uint(g.entry,4294967295) then return invalid end
        givers[g.guid]=true
    end
    local offer=input.offer
    if input.offerObjectKnown==true then
        if type(offer)~="table" or not decimal(offer.npcGuid) then return invalid end
        if givers[offer.npcGuid] then
            if not decimal(offer.expiresInMs) then return invalid end
            if offer.expiresInMs~="0" then
                if not array(offer.quests,32) then return invalid end
                local desired={}
                for _,row in ipairs(offer.quests) do
                    if type(row)~="table" then return invalid end
                    local q=row.questId
                    if not uint(q,9007199254740991) and not decimal(q) then return invalid end
                    if type(q)=="string" then q=#q<=10 and tonumber(q) or nil end
                    if q and row.available==true and row.acceptRequested~=true and row.repeatable~=true and
                       not active[q] and allowed[q] then desired[q]=true end
                end
                local ordered=sorted_ids(desired)
                local q=ordered[1]
                if q==783 and #ordered>1 then q=ordered[2] end -- Intro is deliberately last.
                if q then
                    if not decimal(offer.revision) or offer.revision=="0" then return invalid end
                    local used=false
                    for key,bound in pairs(input.reservations) do
                        if not text(key,64) then return invalid end
                        if byte_at(key,1)==97 and byte_at(key,2)==99 and byte_at(key,3)==99 and
                           byte_at(key,4)==101 and byte_at(key,5)==112 and byte_at(key,6)==116 and byte_at(key,7)==58 and
                           same(bound,{input.sessionId,offer.npcGuid,offer.revision}) then used=true end
                    end
                    if not used then
                        return ready({kind="accept",questId=q,npcGuid=offer.npcGuid,offerRevision=offer.revision})
                    end
                end
            end
        end
    end
    if #input.candidates==0 then
        if input.autonomous then return ready({kind="travel",entry=input.neededEntry}) end
        return ready({kind="wait",phase="waiting_for_observed_starter_questgiver_within_8yd"})
    end
    if not signature(input.querySignature) or #input.querySignature~=3 or not array(input.queries,8) then return invalid end
    local previous={}
    for _,row in ipairs(input.queries) do
        if type(row)~="table" then return invalid end
        if type(row.npc)=="string" and previous[row.npc]==nil then previous[row.npc]=row end
    end
    local zero={negative=false,whole="0",fraction=""}
    local sixty={negative=false,whole="60",fraction=""}
    local best,best_at
    local validated={}
    local function valid_clock(value)
        if type(value)~="table" then return false end
        if validated[value]==nil then validated[value]=clock_parts(value) end
        return validated[value]
    end
    for _,g in ipairs(input.candidates) do
        local prior=previous[g.guid]
        local skip=false
        if prior and same(prior.signature,input.querySignature) then
            if not valid_clock(prior.elapsed) then return invalid end
            skip=clock_less(prior.elapsed,sixty)
        end
        if not skip then
            local at=prior and prior.at or zero
            if not valid_clock(at) or at.infinite==true then return invalid end
            local better=not best or g.priority<best.priority
            if best and g.priority==best.priority then
                if clock_less(at,best_at) then better=true
                elseif not clock_less(best_at,at) then
                    better=g.distance<best.distance or g.distance==best.distance and less_guid(g.guid,best.guid)
                end
            end
            if better then best,best_at=g,at end
        end
    end
    if not best then return ready({kind="wait",phase="waiting_for_new_offer_or_prerequisite_state"}) end
    return ready({kind="offer",npcGuid=best.guid})
end
function M.decide(input)
    if type(input)=="table" and input.mode=="starter_pickup" then return starter_pickup(input) end
    local invalid={status="invalid"}
    if type(input)~="table" or not text(input.sessionId,128) or not number(input.now) then return invalid end
    local pickup_offer,turnin_offer=offer_defaults(input.offer),offer_defaults(input.turnInOffer)
    local own,active,done=ids(input.ownQuestIds,25),ids(input.ownerActiveQuestIds,25),ids(input.ownerDoneQuestIds,72)
    if not own or not active or not done or not array(input.questLog,25) or not array(input.nearby,64) or
       not array(input.rewarded,8) or not array(input.accepted,8) or not array(input.queries,8) or
       type(input.reservations)~="table" or not signature(input.rewardSignature) or not signature(input.acceptSignature) or
       not valid_offer(pickup_offer) or not valid_offer(turnin_offer) then return invalid end
    local reservations=0
    for key,bound in pairs(input.reservations) do
        reservations=reservations+1
        if reservations>96 or not text(key,64) or not signature(bound) or #bound~=3 then return invalid end
    end
    for _,row in ipairs(input.queries) do
        if type(row)~="table" or not text(row.npc,64) or not signature(row.signature) or not number(row.at) then return invalid end
    end
    for _,events in ipairs({input.rewarded,input.accepted}) do
        for _,row in ipairs(events) do
            if type(row)~="table" or not uint(row.questId,2147483647) or row.questId==0 or not decimal(row.npcGuid) then return invalid end
        end
    end
    local eligible_accept,eligible_reward={},{}
    for q in pairs(active) do if not own[q] and #input.ownQuestIds<25 then eligible_accept[q]=true end end
    for _,row in ipairs(input.questLog) do
        if type(row)~="table" or not uint(row.questId,4294967295) then return invalid end
        if own[row.questId] and done[row.questId] and row.complete==true and row.failed~=true then eligible_reward[row.questId]=true end
    end
    local givers={}
    if pose(input.ownerPosition) and pose(input.selfPosition) and input.ownerPosition.mapId==input.selfPosition.mapId then
        for _,c in ipairs(input.nearby) do
            if type(c)~="table" or not decimal(c.guid) or not uint(c.entry,16777215) or
               not uint(c.npcFlags,4294967295) or not uint(c.unitFlags,4294967295) or not uint(c.movementFlags,4294967295) then return invalid end
            if c.creatureIdentityMatches==true and c.present==true and c.aliveKnown==true and c.alive==true and
               pose(c.position) and c.position.mapId==input.selfPosition.mapId and bit(c.npcFlags,2) and
               not bit(c.unitFlags,524288) and c.movementFlags%256==0 and not bit(c.movementFlags,134217728) then
                local d,od=distance(input.selfPosition,c.position),distance(input.ownerPosition,c.position)
                if d<=24 and od<=12 then
                    local row={guid=c.guid,entry=c.entry,distance=d,ownerDistance=od}
                    local i=#givers+1
                    while i>1 and less_giver(row,givers[i-1]) do givers[i]=givers[i-1]; i=i-1 end
                    givers[i]=row
                end
            end
        end
    end
    local result={status="ready",sessionId=input.sessionId,givers=givers,
        allowedAcceptQuestIds=sorted_ids(eligible_accept),allowedRewardQuestIds=sorted_ids(eligible_reward)}
    local function query_ready(key,sig,delay)
        for _,row in ipairs(input.queries) do
            if row.npc==key and same(row.signature,sig) and input.now-row.at<delay then return false end
        end
        return true
    end
    local pending
    local function finish(choice)
        if pending then pending.ready=choice; choice=pending end
        result.choice=choice
        return result
    end
    local phase="following_joels_quest_changes"
    local completed={}
    for q in pairs(own) do if done[q] then completed[q]=true end end
    for _,q in ipairs(sorted_ids(completed)) do
        if eligible_reward[q] then
            local expected
            for _,event in ipairs(input.rewarded) do if event.questId==q then expected=event.npcGuid; break end end
            local giver
            for _,g in ipairs(givers) do
                if expected and g.guid==expected or not expected and g.entry==reward_givers[q] then giver=g; break end
            end
            if not giver and not expected then
                for _,g in ipairs(givers) do
                    if turnin_offer.npcGuid==g.guid and turnin_offer.questId==q then giver=g; break end
                end
            end
            if not giver then phase="waiting_for_joel_at_recorded_reward_giver"
            else
                if reward_offer(turnin_offer,q,giver) then
                    local candidate={kind="reward_candidate",npcGuid=giver.guid,questId=q,offerRevision=turnin_offer.revision}
                    if not same(input.reservations["claim:"..q],{input.sessionId,giver.guid,turnin_offer.revision}) then
                        candidate.ready={kind="claim_reward",npcGuid=giver.guid,questId=q,offerRevision=turnin_offer.revision}
                        return finish(candidate)
                    end
                    pending=candidate
                end
                local key="reward:"..q..":"..giver.guid
                if query_ready(key,input.rewardSignature,60) then return finish({kind="turn_in",npcGuid=giver.guid,questId=q}) end
            end
        end
    end
    local missing=false
    for q in pairs(active) do if not own[q] then missing=true end end
    if missing and #input.ownQuestIds<25 then
        local offer,giver=pickup_offer
        for _,g in ipairs(givers) do if g.guid==offer.npcGuid then giver=g; break end end
        if giver and offer.expiresInMs~="0" and offer.revision~="0" then
            if not array(offer.quests,32) then return invalid end
            local bound,used={input.sessionId,giver.guid,offer.revision},false
            for key,value in pairs(input.reservations) do
                if byte_at(key,1)==97 and byte_at(key,2)==99 and byte_at(key,3)==99 and byte_at(key,4)==101 and
                   byte_at(key,5)==112 and byte_at(key,6)==116 and byte_at(key,7)==58 and same(value,bound) then used=true end
            end
            if not used then
                for _,q in ipairs(offer.quests) do
                    if type(q)=="table" and eligible_accept[q.questId] and q.available==true and q.repeatable~=true and q.acceptRequested~=true then
                        return finish({kind="accept",npcGuid=giver.guid,questId=q.questId,offerRevision=offer.revision})
                    end
                end
            end
        end
        local event_guids={}
        for _,event in ipairs(input.accepted) do if active[event.questId] and not own[event.questId] then event_guids[event.npcGuid]=true end end
        local ordered={}
        for _,g in ipairs(givers) do
            local i=#ordered+1
            -- Stable order retains the distance/GUID tie from the giver list.
            while i>1 do
                local p=ordered[i-1]
                local priority,prior=event_guids[g.guid]==true,event_guids[p.guid]==true
                if not (priority and not prior or priority==prior and g.ownerDistance<p.ownerDistance) then break end
                ordered[i]=p; i=i-1
            end
            ordered[i]=g
        end
        for _,g in ipairs(ordered) do
            if query_ready(g.guid,input.acceptSignature,30) then return finish({kind="offer",npcGuid=g.guid}) end
        end
        phase="waiting_for_joel_at_followup_giver_or_server_offer"
    elseif next(completed)~=nil then
        if phase~="waiting_for_joel_at_recorded_reward_giver" then phase="waiting_for_own_quest_objectives" end
    else phase="following_joels_quest_changes" end
    return finish({kind="wait",phase=phase})
end
return M
