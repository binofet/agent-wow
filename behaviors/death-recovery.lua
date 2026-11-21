-- Death recovery decisions, shared by every actor. The harness supplies a raw
-- observed death epoch and exact release/query execution with durable no-replay
-- reservations. Ghost navigation/reclaim remains a separate legacy migration.
local M={tuning={release_delay_ms=0}}
local state={}
local function current(obs,frame)
    local self=(obs.players or {})[obs.selfGuid]
    local r=obs.recovery
    return obs.sessionId==frame.session_id and self and self.present==true and
        self.aliveKnown==true and self.healthKnown==true and self.alive==false and
        type(r)=='table' and type(r.deathEpoch)=='string' and r.deathEpoch~='0' and
        r.enabled==true
end
function M.on_event(event)
    local receipt=event.receipt
    local p=receipt and receipt.payload or {}
    if state.session and p.self_guid==state.self and p.death_epoch==state.epoch and
       (p.capability=='release_spirit' or p.capability=='query_corpse') and
       receipt.outcome=='not_dispatched' then
        -- Only a definite no-write outcome relinquishes a local reservation.
        -- The next fresh observation still checks the native death-bound flags.
        state[p.capability]=nil
    end
    return true
end
function M.decide(frame,obs,sequence)
    if type(obs)~='table' or frame.input.observationSequence~=sequence then return {} end
    local self=(obs.players or {})[obs.selfGuid]
    if obs.sessionId~=frame.session_id or not self or self.aliveKnown~=true then return {} end
    if self.alive==true then state={};return {phase='alive'} end
    if not current(obs,frame) then return {phase='waiting_for_observed_enabled_death'} end
    local r=obs.recovery
    local now=frame.clock and frame.clock.monotonicMs
    if state.session~=obs.sessionId or state.self~=obs.selfGuid or state.epoch~=r.deathEpoch then
        state={session=obs.sessionId,self=obs.selfGuid,epoch=r.deathEpoch,observedAt=now}
    end
    if #frame.pending~=0 then return {phase='waiting_for_existing_action_custody'} end
    local kind
    if r.ghostObserved==true then
        if r.corpseQueryKnown==true then return {phase='corpse_observed'} end
        if r.corpseQueryPending==true or state.query_corpse then return {phase='waiting_for_actual_corpse_response'} end
        kind='query_corpse'
    else
        if r.releaseAttempted==true or state.release_spirit then return {phase='waiting_for_actual_ghost'} end
        if self.health~=0 then return {phase='waiting_for_zero_health'} end
        local delay=M.tuning.release_delay_ms
        if delay>0 and (not now or not state.observedAt or now<state.observedAt or now-state.observedAt<delay) then
            return {phase='release_delay'}
        end
        kind='release_spirit'
    end
    state[kind]=true
    return {phase=kind,action={capability=kind,sessionId=obs.sessionId,selfGuid=obs.selfGuid,
        deathEpoch=r.deathEpoch,observationSequence=sequence}}
end
function M.export_state() return {version=1,data=state} end
function M.import_state(saved)
    if type(saved)~='table' or saved.version~=1 or type(saved.data)~='table' then return false end
    state=saved.data
    return true
end
return M
