-- Each participant receives the same explicit party_summon plan, bound to its
-- own current session and visit. Only the recipient can submit summon_decision
-- for the offer its client actually received.
local M = {tuning={regroup_yards=10, arrival_position_age_ms=5000}}
local latest, inventory, inventory_sequence
local roles={"warlockGuid","helperOneGuid","helperTwoGuid","recipientGuid"}

local function valid_roles(plan)
    local seen={}
    for _,key in ipairs(roles) do
        local g=plan[key]
        if type(g)~="string" or #g==0 or #g>20 or byte_at(g,1)==48 or seen[g] then return false end
        for i=1,#g do local b=byte_at(g,i);if b<48 or b>57 then return false end end
        seen[g]=true
    end
    return seen[plan.selfGuid]==true
end
local function same_group(plan,obs)
    local group=(obs.party or {}).group
    if not group or group.known~=true or group.inGroup~=true or group.groupGuid~=plan.groupGuid then return false end
    local online={}
    for _,member in ipairs(group.members or {}) do if member.online==true then online[member.guid]=true end end
    for _,key in ipairs(roles) do if not online[plan[key]] then return false end end
    return true
end
function M.validate(task)
    return type(task)=="table" and task.kind=="party_summon" and type(task.id)=="string" and
        type(task.sessionId)=="string" and type(task.visitId)=="string" and type(task.groupGuid)=="string" and
        type(task.issued)=="table" and type(task.preexisting)=="table" and valid_roles(task)
end
function M.request(v)
    local o=latest
    if type(v.id)~="string" or #v.id==0 or #v.id>80 or not o or not o.summoning or
       v.sessionId~=o.sessionId or v.selfGuid~=o.selfGuid or v.visitId~=o.visitId or
       not valid_roles(v) or not same_group(v,o) then return end
    local task={kind="party_summon",id=v.id,sessionId=v.sessionId,selfGuid=v.selfGuid,visitId=v.visitId,
        groupGuid=v.groupGuid,issued={},preexisting={},priorOffer=(o.summoning.offer or {}).sequence}
    for _,key in ipairs(roles) do task[key]=v[key] end
    for _,obj in ipairs(o.summoningObjects or {}) do task.preexisting[obj.guid]=true end
    return task
end
function M.on_event(task,event)
    if event.kind=="encounter_observation" then latest=event.value
    elseif event.kind=="inventory_observation" then inventory,inventory_sequence=event.value,event.sequence end
    local pending,r=task and task.pending,event.receipt
    if pending and r then
        local payload=r.payload or {}
        if pending.actionId and r.action_id==pending.actionId or payload.policy_request_id==pending.id then
            pending.actionId=r.action_id
            if r.outcome=="not_dispatched" or r.outcome=="failed" then
                task.pending=nil
                if pending.stage=="response" then
                    task.issued.response,task.decision,task.beforeResponse=nil,nil,nil
                else
                    task.finishing=pending.stage.."_"..r.outcome
                end
            elseif r.outcome=="settled_unverified" or r.outcome=="confirmed" then
                task.pending=nil
                if pending.stage=="response" and not task.decision.accept then
                    task.finishing=payload.transport_written and "decline_requested" or "decline_outcome_unverified"
                end
            end
        end
    end
    local v=event.value
    if not task or event.kind~="command" or type(v)~="table" or v.task~="summon_decision" then return end
    local s=latest and latest.summoning
    local offer=s and s.offer
    if task.selfGuid~=task.recipientGuid or task.id~=v.id or v.sessionId~=task.sessionId or
       v.selfGuid~=task.selfGuid or v.visitId~=task.visitId or not offer or offer.pending~=true or
       s.sessionId~=v.sessionId or s.selfGuid~=v.selfGuid or s.visitId~=v.visitId or
       offer.visitId~=v.visitId or offer.sequence~=v.offerSequence or offer.sequence==task.priorOffer or
       offer.summonerGuid~=task.warlockGuid or v.summonerGuid~=task.warlockGuid or
       type(v.accept)~="boolean" or task.issued.response then return end
    task.decision={sequence=offer.sequence,accept=v.accept}
end

local function point(p,map)
    return p and p.present==true and p.mapKnown==true and p.mapId==map and p.positionKnown==true
end
local function nearby(a,b,r)
    return ((a.x or 0)-(b.x or 0))^2+((a.y or 0)-(b.y or 0))^2+((a.z or 0)-(b.z or 0))^2<=r*r
end
local function stationary(p)
    return p and p.present==true and p.aliveKnown==true and p.alive==true and
        p.combatKnown==true and p.inCombat==false and p.movementKnown==true and p.moving==false
end
local function witnessed(p,map)
    return point(p,map) and p.positionInferred~=true and p.positionEstimated~=true and
        p.positionAgeKnown==true and tonumber(p.positionAgeMs or "0")<=M.tuning.arrival_position_age_ms
end
local function has_shard(t,frame)
    if not inventory or not inventory_sequence or inventory_sequence~=(frame.input.observationSequences or {}).inventory or
       inventory.sessionId~=t.sessionId or inventory.selfGuid~=t.selfGuid or inventory.inventoryKnown~=true then return false end
    for _,bag in ipairs(inventory.inventoryBags or {}) do
        for _,slot in ipairs(bag.slots or {}) do
            if slot.metadataKnown==true and slot.entry==6265 and (slot.stackCount or 0)>0 then return true end
        end
    end
    return false
end
local function object(t,obs,entry,allow_existing)
    local found
    for _,obj in ipairs(obs.summoningObjects or {}) do
        if obj.present==true and obj.entry==entry and obj.createdByKnown==true and obj.createdByGuid==t.warlockGuid and
           obj.mapKnown==true and obj.mapId==obs.mapId and (allow_existing or not t.preexisting[obj.guid]) then
            if found then return nil,true end
            found=obj
        end
    end
    return found,false
end
function M.step(t,obs,frame,sequence,in_combat,can_act)
    local body=(obs.players or {})[obs.selfGuid]
    local function view(phase,stage,action)
        if action then action.sessionId,action.selfGuid,action.visitId=t.sessionId,t.selfGuid,obs.visitId end
        return {task=t,stage=stage,status={id=t.id,kind=t.kind,phase=phase,recipientGuid=t.recipientGuid,
            offerSequence=(obs.summoning and obs.summoning.offer or {}).sequence},
            hold=not in_combat and body and body.combatKnown==true and body.inCombat==false,action=can_act and action or nil}
    end
    local function finish(why)
        t.finishing=why
        local v=view(why);v.done=true;v.hold=false
        return v
    end
    if t.finishing then return finish(t.finishing) end
    if t.sessionId~=obs.sessionId or t.selfGuid~=obs.selfGuid then return finish("body_changed") end
    local recipient=t.selfGuid==t.recipientGuid
    if t.visitId~=obs.visitId and not (recipient and t.issued.response and t.decision.accept) then return finish("visit_changed") end
    if t.pending then return view("waiting_for_"..t.pending.stage.."_receipt") end
    if not same_group(t,obs) then return view("waiting_for_online_group") end
    local s=obs.summoning
    if not s or s.selfGuid~=t.selfGuid or s.sessionId~=t.sessionId or s.visitId~=obs.visitId then return view("waiting_for_summoning_observation") end
    local p=obs.players or {}
    local self,warlock,destination=p[t.selfGuid],p[t.warlockGuid],p[t.recipientGuid]
    if not self or self.aliveKnown~=true then return view("waiting_for_body") end
    if self.alive~=true then return finish("dead") end
    local together=witnessed(warlock,obs.mapId) and witnessed(destination,obs.mapId) and
        nearby(warlock,destination,M.tuning.regroup_yards)
    if t.wasApart==nil then
        if together then return finish("already_regrouped") end
        t.wasApart=true
    end
    if together and (not recipient or t.issued.response and t.decision.accept and
       (t.visitId~=obs.visitId or t.beforeResponse~=self.positionRevision)) then return finish("regroup_observed") end
    if in_combat or not stationary(self) or (obs.navigation or {}).motionUnconfirmed==true then return view("waiting_for_safe_stationary_body") end
    if recipient then
        local offer=s.offer
        if t.issued.response then return view("awaiting_observed_regroup") end
        if not offer or offer.pending~=true or offer.visitId~=obs.visitId or offer.summonerGuid~=t.warlockGuid or
           offer.sequence==t.priorOffer then return view("awaiting_intended_offer") end
        if not t.decision or t.decision.sequence~=offer.sequence then return view("awaiting_recipient_decision") end
        return view("responding_to_observed_offer","response",{capability="respond_to_summon",summonerGuid=t.warlockGuid,expectedGroupGuid=t.groupGuid,
            offerSequence=offer.sequence,accept=t.decision.accept})
    end
    if #(obs.summoningObjects or {})>=32 then return view("summoning_object_view_at_capacity") end
    if not stationary(warlock) or not point(warlock,obs.mapId) or not point(self,obs.mapId) then return view("waiting_for_warlock") end
    local stone,ambiguous=object(t,obs,194097,true)
    if ambiguous then return view("ambiguous_portal") end
    local helper=t.selfGuid~=t.warlockGuid
    if stone then
        t.stoneGuid=t.stoneGuid or stone.guid
        if t.stoneGuid~=stone.guid then return view("portal_changed") end
    end
    local portal=t.stoneGuid
    local function use(stage,obj,target)
        if not point(obj,obs.mapId) or not nearby(self,obj,5) then return view("waiting_in_ritual_range") end
        return view("using_"..stage,stage,{capability="use_summoning_object",objectGuid=obj.guid,
            expectedCreatorGuid=t.warlockGuid,expectedGroupGuid=t.groupGuid,
            expectedUseRevision=s.useRevision or "0",selectedMemberGuid=target,
            expectedCreatorTargetGuid=stage=="channel" and t.recipientGuid or nil})
    end
    if not portal then
        local ritual,multiple=object(t,obs,194108,false)
        if multiple then return view("ambiguous_ritual") end
        for _,guid in ipairs({t.helperOneGuid,t.helperTwoGuid}) do
            if not stationary(p[guid]) or not point(p[guid],obs.mapId) or not nearby(warlock,p[guid],5) then
                return view("waiting_for_two_helpers")
            end
        end
        if helper then
            if ritual and not t.issued.assist then return use("assist",ritual) end
        elseif not t.issued.cast and not ritual then
            local before,other=object(t,obs,194108,true)
            if before or other then return view("existing_ritual_in_progress") end
            local spell=(obs.spells or {})["698"]
            if obs.spellbookKnown~=true or not spell or spell.known~=true or spell.ready~=true or not has_shard(t,frame) then
                return view("waiting_for_ritual_and_shard")
            end
            return view("casting_ritual","cast",{capability="cast_spell",spellId=698,observationSequence=sequence})
        end
        return view("awaiting_warlock_portal")
    end
    if t.selfGuid==t.helperTwoGuid then return view("awaiting_observed_regroup") end
    if not stationary(p[t.helperOneGuid]) or not point(p[t.helperOneGuid],obs.mapId) or not nearby(warlock,p[t.helperOneGuid],5) then
        return view("waiting_for_summon_helper")
    end
    if not helper and warlock.selectedTargetGuid~=t.recipientGuid then
        if not t.issued.select and not t.issued.stone then
            return view("selecting_recipient","select",{capability="select_target",expectedGroupGuid=t.groupGuid,targetGuid=t.recipientGuid})
        end
        return view("awaiting_selected_recipient")
    end
    if not helper and not t.issued.stone and stone then
        local existing,multiple=object(t,obs,179944,true)
        if existing or multiple then return view("existing_summon_channel") end
        return use("stone",stone,t.recipientGuid)
    end
    if helper and not t.issued.channel then
        local channel,multiple=object(t,obs,179944,false)
        if multiple then return view("ambiguous_summon_channel") end
        if channel then
            if channel.creatorSelectedTargetGuid~=t.recipientGuid then return view("awaiting_selected_recipient") end
            return use("channel",channel)
        end
    end
    return view("awaiting_observed_regroup")
end
function M.commit(view,chosen)
    if not view.stage or not chosen or chosen~=view.action then return end
    local t=view.task
    t.issued[view.stage]=true
    t.serial=(t.serial or 0)+1
    t.pending={id=t.id.."/"..view.stage.."/"..t.serial,stage=view.stage}
    if view.stage=="response" then
        t.beforeResponse=((latest.players or {})[t.selfGuid] or {}).positionRevision
    end
    return t.pending.id
end
return M
