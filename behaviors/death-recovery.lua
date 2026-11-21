-- One shared editable death lifecycle: release, own-corpse query, ghost route,
-- reclaim timing and continuation. Go supplies facts and exact physical inputs.
local M={tuning={release_delay_ms=0,route_retry_ms=1000,reclaim_range=35,
    unknown_reclaim_delay_ms=120000,deadline_ms=0}}
local state={}
local function valid_id(v)return type(v)=='string' and v~='' and v~='0' end
local function alive(p)return p and p.present==true and p.aliveKnown==true and p.alive==true end
local function distance(a,b)
    if not a or not b then return end
    local dx,dy,dz=(a.x or 0)-(b.x or 0),(a.y or 0)-(b.y or 0),(a.z or 0)-(b.z or 0)
    return math.sqrt(dx*dx+dy*dy+dz*dz)
end
function M.on_event(event)
    if event.kind=='death_recovery_request' then state.ensureRequested=true end
    local receipt=event.receipt
    local p=receipt and receipt.payload or {}
    if state.session and p.self_guid==state.self and p.death_epoch==state.epoch and receipt.outcome=='not_dispatched' then
        -- A known no-write may be reconsidered. Uncertain release/query/reclaim
        -- reservations survive reload until actual observations settle them.
        if p.capability=='release_spirit' or p.capability=='query_corpse' or p.capability=='reclaim_corpse' then state[p.capability]=nil end
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
    if now and state.now and now<state.now then state.observedAt=now;state.ghostAt=now;state.retryAt=nil end
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
    if M.tuning.deadline_ms>0 and now and state.observedAt and now-state.observedAt>=M.tuning.deadline_ms then
        if r.motionPending==true or r.active==true then return stop('configured_deadline_owned_stop') end
        return {phase='configured_deadline_reached'}
    end
    if r.ghostObserved~=true then
        if r.releaseAttempted==true or state.release_spirit then return {phase='waiting_for_actual_ghost'} end
        if self.health~=0 then return {phase='waiting_for_zero_health'} end
        if M.tuning.release_delay_ms>0 and (not now or not state.observedAt or now-state.observedAt<M.tuning.release_delay_ms) then return {phase='release_delay'} end
        state.release_spirit=true
        return {phase='release_spirit',action=action('release_spirit')}
    end
    state.ghostAt=state.ghostAt or now
    if r.corpseQueryKnown~=true then
        if r.corpseQueryPending==true or state.query_corpse then return {phase='waiting_for_actual_corpse_response'} end
        state.query_corpse=true
        return {phase='query_corpse',action=action('query_corpse')}
    end
    if r.corpseFound~=true or not r.corpseLocation then return {phase='server_reports_no_reclaimable_corpse'} end
    if r.reclaimAttempted==true or state.reclaim_corpse then return {phase='waiting_for_actual_life_after_reclaim'} end
    if r.ghostMovementSupported~=true or self.positionKnown~=true or self.mapKnown~=true or not valid_id(self.positionRevision) then return {phase='waiting_for_supported_actual_ghost_pose'} end
    local nav=obs.navigation or {}
    if nav.motionUnconfirmed==true then return {phase='waiting_for_actual_motion_reconciliation'} end
    local location=r.corpseLocation
    if location.known~=true or location.mapKnown~=true or location.mapId~=self.mapId or r.corpseMapId~=self.mapId then return {phase='corpse_requires_map_transition'} end
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
    state.reclaim_corpse=true
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
