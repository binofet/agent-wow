-- Ordered GM recovery policy. The host owns identity-bound transport and durable
-- reservations; a sent command never substitutes for an observed game effect.
local policy={transportUnobservedGuids={}}
local function num(v) return tonumber(v) end
local function same(a,b) return tostring(a or "") == tostring(b or "") end
local function finite(v) local n=num(v); return n and n==n and n-n==0 end
local function name(v)
    if type(v)~="string" or #v==0 then return false end
    for i=1,#v do local b=byte_at(v,i);if not (b>=65 and b<=90 or b>=97 and b<=122) then return false end end
    return true
end
local function pose(p)
    return type(p)=="table" and p.positionKnown==true and p.mapId~=nil
        and finite(p.x) and finite(p.y) and finite(p.z)
end
local function near(a,b,r)
    return pose(a) and pose(b) and same(a.mapId,b.mapId)
        and (num(a.x)-num(b.x))^2+(num(a.y)-num(b.y))^2+(num(a.z)-num(b.z))^2<=r*r
end
local function find(rows,guid)
    for _,p in ipairs(rows or {}) do if same(p.guid,guid) then return p end end
end
local function result(m,phase,err)
    return {memory=m,phase=phase,done=false,error=err}
end
local function group_matches(g,guids,leader)
    if type(g)~="table" or g.known~=true or not same(g.leaderGuid,leader) then return false end
    local got={}; for _,p in ipairs(g.members or {}) do got[tostring(p.guid)]=true end
    local count=0; for _ in pairs(got) do count=count+1 end
    if count~=#guids then return false end
    for _,id in ipairs(guids) do if not got[tostring(id)] then return false end end
    return true
end
local function roster(m,without,withGM)
    local ids={}; for _,p in ipairs(m.members) do if not same(p.guid,without) then ids[#ids+1]=p.guid end end
    if withGM then ids[#ids+1]="87" end
    return ids
end
local function unobserved_transport(m,obs,member,p,originalParty)
    local explicit=false
    for _,guid in ipairs(policy.transportUnobservedGuids or {}) do
        if same(guid,member.guid) then explicit=true;break end
    end
    if not explicit or member.rpcUrl or p.visibleToGm~=false or p.inCombat==true then return false end
    local group=obs.group or {}
    local current=find(group.members,member.guid)
    if group.known~=true or group.inGroup~=true or not current or
        current.name~=member.name or current.online~=true then return false end
    if originalParty and not group_matches(group,roster(m),m.originalLeader) then return false end
    return true
end
local function departure_still_admitted(m,obs,without)
    for guid in pairs(m.unobservedTransport or {}) do
        if not same(guid,without) then
            local member=find(m.members,guid)
            if not member then return false,tostring(guid) end
            local p=find(obs.members,guid) or {}
            local group=obs.group or {};local current=find(group.members,guid)
            if group.known~=true or group.inGroup~=true or not current or
                current.name~=member.name or current.online~=true then return false,member.name end
            -- A fresh observed combat-free member no longer needs the exception.
            if not (p.combatKnown==true and p.inCombat==false) and
                not unobserved_transport(m,obs,member,p,false) then return false,member.name end
        end
    end
    return true
end
local function target(m,obs)
    if m.destination then return m.destination end
    local p=obs.destinationPlayer
    if p and same(p.guid,m.player.guid) and pose(p) then return p end
end
local function safe_destination(m,obs,p)
    if not pose(p) then return false,"destination_position_unavailable" end
    for _,t in ipairs(obs.threats or {}) do
        if t.alive~=false and near(t,p,m.clearanceYards) then
            local hostile,classified=t.hostile==true,type(t.hostile)=="boolean"
            for _,faction in ipairs(m.hostileFactions or {}) do if same(t.factionTemplate,faction) then hostile,classified=true,true end end
            for _,faction in ipairs(m.friendlyFactions or {}) do if same(t.factionTemplate,faction) then classified=true end end
            if hostile then return false,"destination_under_attack" end
            if not classified then return false,"destination_hostility_unclassified" end
        end
    end
    return true
end
local function selected(obs,guid)
    local s=obs.selection
    return type(s)=="table" and (s.known==true or s.transportWritten==true) and same(s.targetGuid,guid)
end
-- Selection custody survives a resource becoming full without our refill.
-- Reconcile that exact prior action before another member/stage can select.
local function reconcile_selection(m,frame,now)
    local pending=m.selectionWaiting
    if not pending then return end
    local last=frame.last
    local matching=last and same(last.id,pending.id) and
        (last.sessionId==nil or last.sessionId==m.gmSessionId)
    local action=matching and last.action
    if not pending.targetGuid and type(action)=="table" and action.kind=="select_target" and
       find(m.members,action.targetGuid) then pending.targetGuid=tostring(action.targetGuid) end
    local member=find(m.members,pending.targetGuid)
    if not member then return result(m,"blocked_selection","selection_target_binding_unknown") end
    if type(action)=="table" and (action.kind~="select_target" or not same(action.targetGuid,pending.targetGuid)) then
        return result(m,"blocked_selection","selection_receipt_binding_changed")
    end
    local selection=frame.observations.selection or {}
    local current=selection.sessionId==nil or selection.sessionId==m.gmSessionId
    local observed=current and (selection.known==true and same(selection.targetGuid,pending.targetGuid) or
        selection.observedTargetKnown==true and same(selection.observedTargetGuid,pending.targetGuid))
    local written=matching and (last.outcome=="written" or last.outcome=="transport_written") and
        current and selection.transportWritten==true and same(selection.targetGuid,pending.targetGuid)
    local refused=matching and (last.outcome=="not_dispatched" or last.rejected==true)
    if observed or written or refused then
        m.selectionReconciled={id=pending.id,targetGuid=pending.targetGuid,stage=pending.stage,
            reason=observed and "target_observed" or written and "selection_transport_written" or "selection_not_dispatched"}
        m.selectionWaiting=nil
        return
    end
    if now-pending.at>=m.effectTimeoutSeconds then return result(m,"blocked_selection","selection_not_observed:"..member.name) end
    return result(m,"observing_selection_"..member.name)
end
local function issue(m,now,phase,action)
    m.serial=(m.serial or 0)+1
    action.id=m.taskId..":"..tostring(m.serial)
    m.waiting={id=action.id,phase=phase,at=now}
    local out=result(m,phase);out.action=action;return out
end
local function wait_effect(m,frame,confirmed,nextPhase)
    local pending=m.waiting
    if confirmed then m.waiting=nil;m.stage=nextPhase;return nil end
    local last=frame.last
    if last and same(last.id,pending.id) and (last.outcome=="not_dispatched" or last.rejected==true) then
        return result(m,"blocked_"..pending.phase,"command_not_dispatched:"..tostring(last.error or last.reason or pending.phase))
    end
    if num(frame.now)-pending.at>=m.effectTimeoutSeconds then
        m.failedPhase=pending.phase
        return result(m,"blocked_"..pending.phase,"effect_not_observed:"..pending.phase)
    end
    return result(m,"observing_"..pending.phase)
end
local function move_command(m)
    if m.player then return ".appear "..m.player.name end
    local p=m.destination
    return ".go xyz "..tostring(p.x).." "..tostring(p.y).." "..tostring(p.z).." "..tostring(p.mapId)
end
local function initialize(frame)
    local g,obs=frame.goal or {},frame.observations or {}
    if type(g)~="table" then return nil,"goal_object_required" end
    if g.id and type(g.id)~="string" and type(g.id)~="number" then return nil,"invalid_task_id" end
    if g.hostileFactions and type(g.hostileFactions)~="table" or
       g.friendlyFactions and type(g.friendlyFactions)~="table" then return nil,"invalid_task_tuning" end
    local gm=obs.gm or {}
    if not same(gm.guid,"87") or type(gm.sessionId)~="string" or gm.sessionId=="" then
        return nil,"verified_lotharen_session_required"
    end
    local m={version=1,taskId=g.id or "gm-recovery",gmSessionId=gm.sessionId,stage="move",members={},serial=0,
        effectTimeoutSeconds=num(g.effectTimeoutSeconds) or 15,clearanceYards=num(g.clearanceYards) or 20,
        destinationProvenance=g.provenance or (g.destination or {}).provenance or (g.player or {}).provenance,
        hostileFactions=g.hostileFactions or {},friendlyFactions=g.friendlyFactions or {}}
    if not finite(m.effectTimeoutSeconds) or m.effectTimeoutSeconds<=0 or not finite(m.clearanceYards) or m.clearanceYards<0 then return nil,"invalid_task_tuning" end
    if g.destination then
        m.destination=g.destination
        if not pose(m.destination) and type(m.destination)=="table" then m.destination.positionKnown=true end
        if not pose(m.destination) or not finite(m.destination.mapId) or num(m.destination.mapId)%1~=0 or num(m.destination.mapId)<0 then return nil,"invalid_destination" end
        m.tolerance=num(m.destination.tolerance) or 7
    elseif g.player and name(g.player.name) and num(g.player.guid) and num(g.player.guid)>0 then
        m.player=g.player;m.tolerance=num(g.player.tolerance) or 7
    else return nil,"explicit_destination_required" end
    if not finite(m.tolerance) or m.tolerance<=0 then return nil,"invalid_destination_tolerance" end
    local seen={}
    for _,p in ipairs(g.members or {}) do
        if not name(p.name) or not num(p.guid) or num(p.guid)<=0 or same(p.guid,"87") or seen[tostring(p.guid)] then return nil,"invalid_member_identity" end
        seen[tostring(p.guid)]=true
        local observed=find(obs.members,p.guid)
        local groupMember=find((obs.group or {}).members,p.guid)
        if not groupMember or groupMember.name~=p.name then return nil,"original_member_name_not_observed" end
        m.members[#m.members+1]={guid=tostring(p.guid),name=p.name,sessionId=observed and observed.sessionId,rpcUrl=p.rpcUrl}
    end
    if #m.members<1 or #m.members>5 then return nil,"recovery_party_requires_one_to_five_members" end
    local group=obs.group or {};m.originalLeader=tostring(group.leaderGuid or "")
    m.leader=find(m.members,m.originalLeader)
    if not m.leader or not group_matches(group,roster(m),m.originalLeader) then return nil,"original_party_not_observed" end
    if #m.members==5 then
        for i=#m.members,1,-1 do local p=m.members[i]
            if not same(p.guid,m.originalLeader) and (not m.player or not same(p.guid,m.player.guid)) then m.displaced=p;break end
        end
        for i=#m.members,1,-1 do local p=m.members[i]
            if not same(p.guid,m.originalLeader) and not same(p.guid,m.displaced.guid) and (not m.player or not same(p.guid,m.player.guid)) then m.swap=p;break end
        end
        if not m.displaced or not m.swap then return nil,"party_transport_slots_unavailable" end
    end
    return m
end
local function decide(frame)
    local m=frame.memory or {};local obs=frame.observations or {};local now=num(frame.now)
    if not finite(now) then return result(m,"blocked","current_time_required") end
    if m.version==nil then local err;m,err=initialize(frame);if not m then return result({},"blocked",err) end end
    if m.version~=1 or not same((obs.gm or {}).guid,"87") or (obs.gm or {}).sessionId~=m.gmSessionId then return result(m,"blocked","gm_session_changed") end
    for _,p in ipairs(m.members) do local current=find(obs.members,p.guid)
        if current and p.sessionId and current.sessionId and current.sessionId~=p.sessionId then return result(m,"blocked","member_session_changed:"..p.name) end
    end
    if frame.cancelled==true then
        m.cancelled=true
        if m.waiting then m.interruptedAction=m.waiting;m.waiting=nil end
    end
    if m.cancelled then
        local group=obs.group or {}
        if group.known~=true then return result(m,"cleanup_pending","party_observation_unavailable") end
        local wanted={};for _,p in ipairs(m.members) do wanted[p.guid]=p end
        local present={};local count=0
        for _,p in ipairs(group.members or {}) do
            local id=tostring(p.guid);present[id]=true;count=count+1
            if id~="87" and not wanted[id] then return result(m,"cleanup_pending","foreign_party_member_preserved") end
        end
        if not present[m.originalLeader] then return result(m,"cleanup_pending","original_leader_not_in_observed_party") end
        local cleanupCommand
        if not same(group.leaderGuid,m.originalLeader) then cleanupCommand=".group leader "..m.leader.name
        elseif present["87"] then cleanupCommand=".group remove Lotharen"
        else
            for _,p in ipairs(m.members) do if not present[p.guid] then cleanupCommand=".group join "..m.leader.name.." "..p.name;break end end
        end
        if not cleanupCommand then
            local out=result(m,"cancelled");out.cancelled=true;out.cleanupComplete=group_matches(group,roster(m),m.originalLeader);return out
        end
        if m.cleanupWaiting then
            if m.cleanupWaiting.command==cleanupCommand then
                return result(m,"cleanup_pending","roster_restore_effect_not_observed")
            end
            m.cleanupWaiting=nil
        end
        m.serial=m.serial+1;local id=m.taskId..":"..m.serial
        m.cleanupWaiting={id=id,command=cleanupCommand,at=now}
        local out=result(m,"restoring_cancelled_roster");out.action={id=id,kind="gm_command",command=cleanupCommand};return out
    end
    local destination=target(m,obs)
    local safe,why=safe_destination(m,obs,destination)
    if m.stage=="move" and m.player and not destination then safe=true end
    if not safe then
        local out=result(m,"blocked_destination",why);out.blocker={destination=destination,threats=obs.threats or {}};return out
    end
    local function colocated(without)
        for _,p in ipairs(m.members) do
            if not same(p.guid,without) then
                local observed=find(obs.members,p.guid)
                if not near(observed,destination,m.tolerance) or observed.visibleToGm~=true then return false end
            end
        end
        return true
    end
    local function effect(ok,nextPhase)
        if m.waiting then return wait_effect(m,frame,ok,nextPhase) end
        if ok then m.stage=nextPhase;return nil end
    end
    local function command(phase,text) return issue(m,now,phase,{kind="gm_command",command=text}) end
    -- Bounded local phase transitions; only one actual action is ever emitted.
    for _=1,40 do
        local stage=m.stage;local out
        if stage~="move" and not near(obs.gm,destination,m.tolerance) then return result(m,"blocked_destination","gm_left_verified_destination") end
        if stage=="move" then
            out=effect(near(obs.gm,destination,m.tolerance) and (not m.player or destination.visibleToGm==true),"validate_destination")
            if out then return out end
            if m.stage==stage then return command("move",move_command(m)) end
        elseif stage=="validate_destination" then
            -- Teleport completion is an observed position, not a walking route.
            m.stage="clear_combat"
        elseif stage=="clear_combat" then
            m.stopIndex=m.stopIndex or 1
            local member=m.members[m.stopIndex]
            if not member then
                if colocated() and group_matches(obs.group,roster(m),m.originalLeader) then m.stage="revive"
                else m.stage=m.displaced and "remove_displaced" or "join_gm" end
            else
                local p=find(obs.members,member.guid) or {}
                local quiet=p.combatKnown==true and p.inCombat==false
                if m.waiting then out=wait_effect(m,frame,quiet,stage);if out then return out end end
                if quiet then m.stopIndex=m.stopIndex+1
                elseif p.combatKnown~=true then
                    if unobserved_transport(m,obs,member,p,true) then
                        -- This is permission to depart, not observed life,
                        -- combat clearance, arrival or a settled prior write.
                        m.unobservedTransport=m.unobservedTransport or {}
                        m.unobservedTransport[member.guid]=true
                        m.stopIndex=m.stopIndex+1
                    else return result(m,"observing_combat_"..member.name,"combat_state_unknown:"..member.name) end
                else return command("combatstop_"..member.name,".combatstop "..member.name) end
            end
        elseif stage=="remove_displaced" then
            out=effect(group_matches(obs.group,roster(m,m.displaced.guid),m.originalLeader),"join_gm")
            if out then return out end
            if m.stage==stage then return command(stage,".group remove "..m.displaced.name) end
        elseif stage=="join_gm" then
            out=effect(group_matches(obs.group,roster(m,m.displaced and m.displaced.guid,true),m.originalLeader),"lead_gm")
            if out then return out end
            if m.stage==stage then return command(stage,".group join "..m.leader.name.." Lotharen") end
        elseif stage=="lead_gm" then
            out=effect(group_matches(obs.group,roster(m,m.displaced and m.displaced.guid,true),"87"),"summon_first")
            if out then return out end
            if m.stage==stage then return command(stage,".group leader Lotharen") end
        elseif stage=="summon_first" then
            out=effect(colocated(m.displaced and m.displaced.guid),m.displaced and "remove_swap" or "restore_leader")
            if out then return out end
            if m.stage==stage then
                local admitted,who=departure_still_admitted(m,obs,m.displaced and m.displaced.guid)
                if not admitted then return result(m,"blocked_departure","unobserved_transport_not_admitted:"..who) end
                return command(stage,".groupsummon "..m.leader.name)
            end
        elseif stage=="remove_swap" then
            local ids={};for _,id in ipairs(roster(m,m.displaced.guid,true)) do if not same(id,m.swap.guid) then ids[#ids+1]=id end end
            out=effect(group_matches(obs.group,ids,"87"),"join_displaced")
            if out then return out end
            if m.stage==stage then return command(stage,".group remove "..m.swap.name) end
        elseif stage=="join_displaced" then
            out=effect(group_matches(obs.group,roster(m,m.swap.guid,true),"87"),"summon_second")
            if out then return out end
            if m.stage==stage then return command(stage,".group join Lotharen "..m.displaced.name) end
        elseif stage=="summon_second" then
            out=effect(colocated(),"restore_leader")
            if out then return out end
            if m.stage==stage then
                local admitted,who=departure_still_admitted(m,obs)
                if not admitted then return result(m,"blocked_departure","unobserved_transport_not_admitted:"..who) end
                return command(stage,".groupsummon "..m.leader.name)
            end
        elseif stage=="restore_leader" then
            out=effect(group_matches(obs.group,roster(m,m.swap and m.swap.guid,true),m.originalLeader),"remove_gm")
            if out then return out end
            if m.stage==stage then return command(stage,".group leader "..m.leader.name) end
        elseif stage=="remove_gm" then
            out=effect(group_matches(obs.group,roster(m,m.swap and m.swap.guid),m.originalLeader),m.swap and "restore_swap" or "revive")
            if out then return out end
            if m.stage==stage then return command(stage,".group remove Lotharen") end
        elseif stage=="restore_swap" then
            out=effect(group_matches(obs.group,roster(m),m.originalLeader),"revive")
            if out then return out end
            if m.stage==stage then return command(stage,".group join "..m.leader.name.." "..m.swap.name) end
        elseif stage=="revive" or stage=="health" or stage=="mana" then
            if not group_matches(obs.group,roster(m),m.originalLeader) then return result(m,"blocked_roster","original_party_not_restored") end
            if not colocated() then return result(m,"blocked_arrival","party_left_verified_destination") end
            out=reconcile_selection(m,frame,now);if out then return out end
            m.memberIndex=m.memberIndex or 1
            local member=m.members[m.memberIndex]
            if not member then
                m.stage=stage=="revive" and "health" or stage=="health" and "mana" or "verify";m.memberIndex=1
            else
                local p=find(obs.members,member.guid) or {};local ok
                if stage=="revive" then ok=p.aliveKnown==true and p.alive==true
                elseif stage=="health" then ok=p.aliveKnown==true and p.alive==true and finite(p.maxHp) and num(p.maxHp)>0 and num(p.hp)==num(p.maxHp)
                else ok=p.manaUser==false or finite(p.maxMana) and num(p.maxMana)==0 or p.manaUser==true and finite(p.maxMana) and num(p.maxMana)>0 and num(p.mana)==num(p.maxMana) end
                if m.waiting and stage~="revive" and not ok then
                    local previous=m.waiting
                    local maximum=stage=="health" and num(p.maxHp) or num(p.maxMana)
                    local current=stage=="health" and num(p.hp) or num(p.mana)
                    -- Buffs/gear can change the real maximum while a refill is
                    -- observed. Once the old requested amount is actually
                    -- present, retire that completed goal and refill the new
                    -- maximum in this same phase. An ACK or max change alone
                    -- never authorizes replay of an uncertain write.
                    if previous.resourceKind==stage and same(previous.targetGuid,member.guid)
                        and finite(previous.resourceMaximum) and finite(maximum) and maximum>0
                        and maximum~=previous.resourceMaximum and finite(current)
                        and current>=previous.resourceMaximum then
                        m.resourceReconciled={id=previous.id,targetGuid=member.guid,kind=stage,
                            requestedMaximum=previous.resourceMaximum,observedMaximum=maximum}
                        m.waiting=nil
                    end
                end
                if m.waiting then out=wait_effect(m,frame,ok,stage);if out then return out end end
                if ok then m.memberIndex=m.memberIndex+1
                elseif stage=="revive" then return command("revive_"..member.name,".revive "..member.name)
                else
                    local maximum=stage=="health" and num(p.maxHp) or num(p.maxMana)
                    if not maximum or maximum<=0 then return result(m,"blocked_resources","natural_resource_maximum_unknown:"..member.name) end
                    if not selected(obs,member.guid) then
                        m.serial=m.serial+1;local id=m.taskId..":"..m.serial
                        m.selectionWaiting={id=id,at=now,targetGuid=member.guid,stage=stage}
                        local r=result(m,"selecting_"..member.name);r.action={id=id,kind="select_target",targetGuid=member.guid};return r
                    end
                    m.selectionWaiting=nil
                    local refill=command(stage.."_"..member.name,".modify "..(stage=="health" and "hp" or "mana").." "..tostring(math.floor(maximum)))
                    m.waiting.resourceKind,m.waiting.targetGuid,m.waiting.resourceMaximum=stage,member.guid,maximum
                    return refill
                end
            end
        elseif stage=="verify" then
            if not group_matches(obs.group,roster(m),m.originalLeader) then return result(m,"blocked_roster","original_party_not_restored") end
            if not colocated() then return result(m,"blocked_arrival","party_left_verified_destination") end
            for _,member in ipairs(m.members) do local p=find(obs.members,member.guid) or {}
                if p.aliveKnown~=true or p.alive~=true or not finite(p.maxHp) or num(p.hp)~=num(p.maxHp) or num(p.maxHp)<=0 then return result(m,"blocked_final_verification","full_health_not_observed:"..member.name) end
                if p.manaUser~=false and (not finite(p.maxMana) or num(p.maxMana)>0 and num(p.mana)~=num(p.maxMana)) then return result(m,"blocked_final_verification","full_mana_not_observed:"..member.name) end
            end
            local r=result(m,"complete");r.done=true;r.success=true;return r
        else return result(m,"blocked","unknown_recovery_phase") end
    end
    return result(m,"local_transitions")
end
-- The same initializer admits a fresh goal before the host can claim a writer
-- or install holds. This pure entry never advances the FSM or proposes actions.
policy.admit=function(frame)
    local m,err=initialize(frame)
    if not m then return {accepted=false,error=err} end
    return {accepted=true}
end
policy.decide=decide
return policy
