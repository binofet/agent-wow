-- One shared editable death lifecycle: release, own-corpse query, ghost route,
-- reclaim timing and continuation. Go supplies facts and exact physical inputs.
local M={tuning={release_delay_ms=0,resurrection_wait_ms=60000,route_retry_ms=1000,reclaim_range=35,
    unknown_reclaim_delay_ms=120000,deadline_ms=0,offer_max_age_ms=60000,offer_scope='party',trusted_offer_casters={}}}
local state={}
local function valid_id(v)return type(v)=='string' and v~='' and v~='0' end
local function alive(p)return p and p.present==true and p.aliveKnown==true and p.alive==true end
-- These classes can learn a player resurrection in Wrath. An observed ally
-- earns a little time to finish combat or approach; only an offer permits acceptance.
local resurrection_class={[2]=true,[5]=true,[7]=true,[11]=true}
local function observed_rescuer(obs,self)
    local players=obs.players or {}
    local function eligible(guid)
        local ally=players[guid]
        return guid~=obs.selfGuid and alive(ally) and ally.guid==guid and
            self.mapKnown==true and type(self.mapId)=='number' and ally.mapKnown==true and ally.mapId==self.mapId and
            ally.classKnown==true and resurrection_class[ally.classId]
    end
    local group=(obs.party or {}).group or {}
    if group.known==true and group.inGroup==true then
        for _,member in ipairs(group.members or {}) do
            if member.online==true and eligible(member.guid) then return member.guid end
        end
    end
    if type(M.tuning.trusted_offer_casters)=='table' then
        for guid,trusted in pairs(M.tuning.trusted_offer_casters) do
            if trusted==true and eligible(guid) then return guid end
        end
    end
end
local function distance(a,b)
    if not a or not b then return end
    local dx,dy,dz=(a.x or 0)-(b.x or 0),(a.y or 0)-(b.y or 0),(a.z or 0)-(b.z or 0)
    return math.sqrt(dx*dx+dy*dy+dz*dz)
end
function M.on_event(event)
    if event.kind=='death_recovery_request' then state.ensureRequested=true end
    local receipt=event.receipt
    local p=receipt and receipt.payload or {}
    if state.session and p.self_guid==state.self and p.death_epoch==state.epoch and p.capability=='accept_resurrection' then
        if receipt.outcome=='not_dispatched' then
            if state.accept_resurrection==p.offer_revision then state.accept_resurrection=nil end
        else
            state.accept_resurrection=p.offer_revision
        end
    end
    local irreversible=p.capability=='release_spirit' or p.capability=='query_corpse' or p.capability=='reclaim_corpse' or p.capability=='enter_corpse_instance' and p.entry_trigger_not_dispatched~=true
    if state.session and p.self_guid==state.self and p.death_epoch==state.epoch and irreversible and receipt.outcome~='not_dispatched' then
        -- Only an actual reserved action receipt can create private custody.
        -- A wrapper/task may suppress a proposal before the host reserves it.
        state[p.capability]=true
    end
    if state.session and p.self_guid==state.self and p.death_epoch==state.epoch and p.capability=='enter_corpse_instance' and p.entry_trigger_not_dispatched==true then
        -- A known STOP result is separate from a positively unentered trigger.
        -- Fresh observed motion gates below still control any next proposal.
        state.enter_corpse_instance=nil
    end
    if state.session and p.self_guid==state.self and p.death_epoch==state.epoch and receipt.outcome=='not_dispatched' then
        -- A known no-write may be reconsidered. Uncertain release/query/reclaim
        -- reservations survive reload until actual observations settle them.
        if p.capability=='release_spirit' or p.capability=='query_corpse' or p.capability=='reclaim_corpse' or p.capability=='enter_corpse_instance' then state[p.capability]=nil end
        if p.capability=='approach_corpse' then state.retryAt=(state.now or 0)+M.tuning.route_retry_ms end
    end
    return true
end
function M.decide(frame,obs,sequence)
    if type(obs)~='table' or frame.input.observationSequence~=sequence or obs.sessionId~=frame.session_id then return {} end
    local self=(obs.players or {})[obs.selfGuid]
    local r=obs.recovery
    if not self or self.present~=true or self.aliveKnown~=true or type(r)~='table' then return {phase='waiting_for_actual_self'} end
    if alive(self) and r.active~=true then state={};return {phase='alive'} end
    if not valid_id(r.deathEpoch) then return {phase='waiting_for_observed_death_epoch'} end
    local now=frame.clock and frame.clock.monotonicMs
    if state.session~=obs.sessionId or state.self~=obs.selfGuid or state.epoch~=r.deathEpoch then
        state={session=obs.sessionId,self=obs.selfGuid,epoch=r.deathEpoch,observedAt=now}
    end
    -- Keep time since this death across reloads and repeated recovery requests.
    state.deathAt=state.deathAt or state.observedAt or now
    if now and state.now and now<state.now then
        state.deathAt=now-math.max(0,state.now-state.deathAt)
        state.observedAt=now;state.ghostAt=now;state.retryAt=nil
    end
    state.now=now
    if state.ensureRequested then state.observedAt=now;state.retryAt=nil;state.ensureRequested=nil end
    if #frame.pending~=0 then return {phase='waiting_for_existing_action_custody'} end
    local function action(kind)
        return {capability=kind,sessionId=obs.sessionId,selfGuid=obs.selfGuid,deathEpoch=r.deathEpoch,
            expectedPositionRevision=self.positionRevision,observationSequence=sequence}
    end
    local function stop(phase)
        if valid_id(self.positionRevision) then return {phase=phase,action=action('stop_corpse')} end
        return {phase='waiting_for_stop_pose'}
    end
    if alive(self) then
        if r.active==true then return stop('alive_retiring_physical_motion') end
        state={};return {phase='alive'}
    end
    if self.healthKnown~=true then return {phase='waiting_for_actual_health'} end
    local offer=r.resurrectionOffer
    if offer and offer.deathEpoch==r.deathEpoch and valid_id(offer.revision) and valid_id(offer.casterGuid) then
        -- A later genuine server offer supersedes an earlier offer identity.
        -- Successful/uncertain response writes otherwise retain custody until
        -- actual life; an ACK or local age limit never proves resurrection.
        if state.accept_resurrection and state.accept_resurrection~=offer.revision then state.accept_resurrection=nil end
        if offer.responseAttempted==true or state.accept_resurrection==offer.revision then
            return {phase='waiting_for_actual_life_after_offer_acceptance'}
        end
        local member=false
        for _,p in ipairs((((obs.party or {}).group or {}).members or {})) do
            if p.guid==offer.casterGuid and p.online==true then member=true end
        end
        local within_age=M.tuning.offer_max_age_ms<=0 or tonumber(offer.ageMs or '0')<=M.tuning.offer_max_age_ms
        -- Per-actor editable exact GUID set. Empty by default; this extends
        -- party trust without accepting arbitrary outsiders or inventing offers.
        local trusted=type(M.tuning.trusted_offer_casters)=='table' and
            M.tuning.trusted_offer_casters[offer.casterGuid]==true
        if (member or trusted or M.tuning.offer_scope=='any_offer') and within_age then
            if (obs.navigation or {}).motionUnconfirmed==true then return {phase='waiting_for_actual_motion_before_offer'} end
            if r.active==true or r.motionPending==true then return stop('resurrection_offer_owned_stop') end
            local a=action('accept_resurrection');a.casterGuid=offer.casterGuid;a.expectedOfferRevision=offer.revision
            return {phase='accepting_observed_resurrection_offer',action=a}
        end
    elseif state.accept_resurrection then
        return {phase='waiting_for_actual_life_after_offer_acceptance'}
    end
    if M.tuning.deadline_ms>0 and now and state.observedAt and now-state.observedAt>=M.tuning.deadline_ms then
        if r.motionPending==true or r.active==true then return stop('configured_deadline_owned_stop') end
        return {phase='configured_deadline_reached'}
    end
    if r.ghostObserved~=true then
        if r.releaseAttempted==true or state.release_spirit then return {phase='waiting_for_actual_ghost'} end
        if self.health~=0 then return {phase='waiting_for_zero_health'} end
        if M.tuning.release_delay_ms>0 and (not now or not state.observedAt or now-state.observedAt<M.tuning.release_delay_ms) then return {phase='release_delay'} end
        if now and state.deathAt then
            local remaining=(tonumber(M.tuning.resurrection_wait_ms) or 0)-(now-state.deathAt)
            if remaining>0 then
                local guid=observed_rescuer(obs,self)
                if guid then return {phase='waiting_for_resurrection_offer',rescuerGuid=guid,remainingMs=remaining} end
            end
        end
        return {phase='release_spirit',action=action('release_spirit')}
    end
    state.ghostAt=state.ghostAt or now
    if r.ghostMovementSupported==true and r.motionPending==true and r.active~=true and (obs.navigation or {}).motionUnconfirmed~=true then return stop('graveyard_stationary_baseline_stop') end
    if r.corpseEntryDestinationObserved==true and r.corpseMapId==self.mapId and not state.entryInterior then
        -- Only native scoped adjacent-entry evidence can release the exterior
        -- query reservation. Entry itself remains consumed for this death.
        state.entryInterior=true;state.query_corpse=nil
    end
    if r.corpseQueryKnown~=true then
        if r.corpseQueryPending==true or state.query_corpse then return {phase='waiting_for_actual_corpse_response'} end
        return {phase='query_corpse',action=action('query_corpse')}
    end
    if r.corpseFound~=true or not r.corpseLocation then return {phase='server_reports_no_reclaimable_corpse'} end
    if r.reclaimAttempted==true or state.reclaim_corpse then return {phase='waiting_for_actual_life_after_reclaim'} end
    if r.ghostMovementSupported~=true or self.positionKnown~=true or self.mapKnown~=true or not valid_id(self.positionRevision) then return {phase='waiting_for_supported_actual_ghost_pose'} end
    local nav=obs.navigation or {}
    if nav.motionUnconfirmed==true then return {phase='waiting_for_actual_motion_reconciliation'} end
    local location=r.corpseLocation
    if (r.corpseEntryAttempted==true or state.enter_corpse_instance) and not (r.corpseEntryDestinationObserved==true and r.corpseMapId==self.mapId) then
        return {phase='waiting_for_actual_life_or_dungeon_entry'}
    end
    if location.known~=true or location.mapKnown~=true or location.mapId~=self.mapId then return {phase='corpse_marker_not_on_current_map'} end
    if r.corpseMapId~=self.mapId then
        -- The server exterior marker is not the corpse. Select only a public
        -- entrance from this marker's map to the exact queried corpse map.
        local chosen,nearest
        for _,v in ipairs(r.corpseEntrances or {}) do
            if v.referenceOnly==true and (v.sourceMapId or 0)==self.mapId and (v.destinationMapId or 0)==r.corpseMapId and
               type(v.triggerId)=='number' and v.triggerId>0 then
                local d=distance(location,v)
                if not nearest or d<nearest then chosen,nearest=v,d end
            end
        end
        if not chosen then return {phase='corpse_dungeon_entrance_reference_unavailable'} end
        if not valid_id(r.corpseQueryRevision) then return {phase='waiting_for_query_revision'} end
        if state.retryAt and (not now or now<state.retryAt) then return {phase='route_retry_delay'} end
        local a=action('approach_corpse');a.corpseQueryRevision=r.corpseQueryRevision;a.rangeYards=.25;a.triggerId=chosen.triggerId
        if distance(self,chosen)<=.25 then a.capability='enter_corpse_instance' end
        return {phase=a.capability=='enter_corpse_instance' and 'entering_queried_corpse_dungeon' or 'approaching_queried_dungeon_entrance',action=a}
    end
    local d=distance(self,location)
    if d>M.tuning.reclaim_range then
        if state.retryAt and (not now or now<state.retryAt) then return {phase='route_retry_delay'} end
        if not valid_id(r.corpseQueryRevision) then return {phase='waiting_for_query_revision'} end
        local a=action('approach_corpse');a.corpseQueryRevision=r.corpseQueryRevision;a.rangeYards=M.tuning.reclaim_range
        return {phase='approaching_own_corpse',action=a}
    end
    if r.motionPending==true or r.active==true then return stop('corpse_arrival_stop') end
    if r.reclaimDelayKnown==true then
        if tonumber(r.reclaimDelayRemainingMs or '0')>0 then return {phase='waiting_for_observed_reclaim_delay'} end
    elseif not now or not state.ghostAt or now-state.ghostAt<M.tuning.unknown_reclaim_delay_ms then
        -- AzerothCore's source maximum is120s. This is editable conservative
        -- policy for a missing timer; the exact reclaim still faces the server.
        return {phase='waiting_for_unknown_reclaim_delay'}
    end
    local c=r.ownCorpse
    local cp=c and c.position
    if not c or not valid_id(c.guid) or c.ownerGuid~=obs.selfGuid or c.present~=true or c.flagsKnown~=true or
       (c.flags or 0)%2~=0 or not cp or cp.known~=true or cp.mapKnown~=true or cp.mapId~=self.mapId or
       not valid_id(cp.revision) or distance(cp,location)>2 or distance(self,cp)>M.tuning.reclaim_range then return {phase='waiting_for_actual_own_corpse'} end
    local a=action('reclaim_corpse');a.corpseGuid=c.guid;a.expectedCorpseRevision=cp.revision
    return {phase='reclaiming_own_corpse',action=a}
end
function M.export_state()return {version=1,data=state}end
function M.import_state(saved)
    if type(saved)~='table' or saved.version~=1 or type(saved.data)~='table' then return false end
    state=saved.data
    return true
end
return M
