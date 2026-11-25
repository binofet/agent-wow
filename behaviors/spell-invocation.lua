-- Explicit, one-shot learned casts in the SAME encounter scheduler. The native
-- cast_spell contract validates knowledge, target layout and physical legality;
-- an automatic effect category is never required here.
-- Ground examples (substitute observed coordinates and actual learned ranks):
-- {task="cast_spell",id="flamestrike-1",spellId=2120,
--  targets={mask=64,destination={x=10,y=20,z=30}}}
-- {task="cast_spell",id="blizzard-1",spellId=10,
--  targets={mask=64,destination={x=10,y=20,z=30}}}
-- Implicit caster-centered example:
-- {task="cast_spell",id="arcane-explosion-1",spellId=1449}
-- Explicit cancellation forwards the CURRENT observed native revision:
-- {task="cancel_spell",id="cancel-blizzard-1",spellId=10,
--  expectedOperationRevision="<observed operationRevision>",channel=true}
-- Its transport receipt never settles the original channel/effect ledger.
-- The same shape accepts an unfamiliar learned uint32 ID without another Go
-- handler. Read its reflected mechanics and use its actual target contract.
-- Add requirements={stationary=true} for a ground/channel spell whose timing
-- is not in the encounter feed, or forward generic castTimeMs/channeled facts.
-- An explicit stationary=false preserves caller-selected moving casts.
-- Opt-in unit preparation: requirements={position={minRangeYards=15,maxRangeYards=25}}.
-- This requests current learned-range/LoS/facing preparation with a positive
-- minimum stand-off, not another cast attempt. Default requests still try now.
-- Preparation is finite and requires the attested range-band actuator; a
-- refusal/UNKNOWN never licenses a cast or walking closer without that floor.
local M={learning={quiet_ms=3000,stable_observations=2},preparation={lease_ms=3000,renew_below_ms=1500,position_ms=8000}}
local state={seen={}}
local proposed
local proposed_cancel
local proposed_preparation
local function id(s) return type(s)=="string" and #s>0 and #s<=128 end
local function uint32(n) return type(n)=="number" and n>0 and n<=4294967295 and n%1==0 end
local function copy(value)
    if type(value)~="table" then return value end
    local out={}
    for key,item in pairs(value) do out[key]=copy(item) end
    return out
end
local function number(n) return type(n)=="number" and n==n and n>-math.huge and n<math.huge end
local function positioning(value)
    if value==nil then return true end
    return type(value)=="table" and number(value.minRangeYards) and value.minRangeYards>0 and
        number(value.maxRangeYards) and value.maxRangeYards>value.minRangeYards
end
local function finish(reason,receipt)
    local task=state.current
    state.seen[task.id]=reason
    state.last={id=task.id,spellId=task.spellId,phase=reason,receipt=receipt}
    if task.preparation then state.releasing={id=task.id,binding=task.binding,preparation=task.preparation} end
    state.current=nil;state.error=nil
end

function M.on_event(event)
    if event.kind=="encounter_observation" and type(event.value)=="table" then
        local o=event.value
        state.observed={sessionId=o.sessionId,selfGuid=o.selfGuid,visitId=o.visitId}
    end
    local task=state.current
    local cancel=state.cancel
    local holder=task and task.preparation and task or state.releasing
    local prep=holder and holder.preparation
    if prep and prep.operation and event.receipt then
        local r=event.receipt
        local p=r.payload or {}
        local op=prep.operation
        local matches=op.kind=="movement_owner" and p.capability==op.kind and p.owner==prep.owner and p.mode==op.mode or
            op.kind=="stop_movement" and p.capability==op.kind and p.target_guid==holder.binding.selfGuid and
            p.visit_id==holder.binding.visitId
        if matches and id(r.action_id) and (not op.actionId or op.actionId==r.action_id) then
            op.actionId=r.action_id
            if r.outcome=="confirmed" or r.outcome=="failed" or r.outcome=="not_dispatched" or
               r.outcome=="settled_unverified" or r.outcome=="unknown" then
                prep.operation=nil
                prep.receipt={actionId=r.action_id,outcome=r.outcome,phase=p.phase}
                if op.kind=="stop_movement" then
                    -- Written/unknown STOP is never replayed: a fresh actual
                    -- stationary sample must reconcile the physical result.
                    prep.stopWaiting=r.outcome~="failed" and r.outcome~="not_dispatched" or nil
                    prep.afterSequence=op.sequence
                elseif r.outcome=="confirmed" then
                    if op.mode=="release" then prep.released=true;prep.token=nil
                    elseif id((p.result or {}).token) then
                        prep.token=p.result.token;prep.afterSequence=op.sequence
                    else prep.uncertainUntil=op.untilMs end
                elseif r.outcome=="unknown" or r.outcome=="settled_unverified" then
                    prep.uncertainUntil=op.untilMs
                elseif op.mode~="acquire" then
                    -- A rejected token may have expired or been replaced.
                    -- Never use it to infer ownership from a matching label.
                    prep.token=nil;prep.refusal=p.error or r.outcome
                end
            end
        end
    end
    if cancel and cancel.issued and event.receipt then
        local r=event.receipt
        local p=r.payload or {}
        if p.capability=="cancel_spell" and p.spell_id==cancel.spellId and id(r.action_id) and
           (not cancel.actionId or cancel.actionId==r.action_id) then
            cancel.actionId=r.action_id
            cancel.receipt={actionId=r.action_id,outcome=r.outcome,phase=p.phase}
            if r.outcome=="confirmed" or r.outcome=="failed" or r.outcome=="not_dispatched" or r.outcome=="settled_unverified" then
                state.lastCancel={id=cancel.id,spellId=cancel.spellId,phase="cancel_transport_receipt",receipt=cancel.receipt}
                state.cancel=nil
            elseif r.outcome=="unknown" then cancel.phase="cancel_transport_unknown" end
        end
    end
    if task and task.issued and event.receipt then
        local receipt=event.receipt
        local p=receipt.payload or {}
        if p.capability=="cast_spell" and p.spell_id==task.spellId and
           id(receipt.action_id) and (not task.actionId or task.actionId==receipt.action_id) then
            task.actionId=receipt.action_id
            local actual={actionId=receipt.action_id,outcome=receipt.outcome,phase=p.phase,grpcStatus=p.grpc_status,
                          grpcMessage=p.grpc_message,targetGuid=p.target_guid,transportWritten=p.transport_written,
                          serverCastGo=p.server_cast_go,effectObserved=p.effect_observed}
            if receipt.outcome=="confirmed" or receipt.outcome=="failed" or
               receipt.outcome=="not_dispatched" or receipt.outcome=="settled_unverified" then
                -- A receipt outcome is not an invented damage/aura/channel
                -- effect. Keep the exact observed outcome for inspection.
                finish("cast_receipt",actual)
            elseif receipt.outcome=="unknown" then
                task.phase="outcome_unknown";task.receipt=actual
            end
        end
    end
    if event.kind~="command" or type(event.value)~="table" then return true end
    local value=event.value
    if value.task=="cancel_spell" then
        if not id(value.id) or not uint32(value.spellId) or not id(value.expectedOperationRevision) or
           value.channel~=nil and type(value.channel)~="boolean" then state.error="invalid_explicit_cancel_request"
        elseif state.seen[value.id] or state.cancel and state.cancel.id==value.id then
        elseif state.cancel and state.cancel.issued then state.error="existing_cancel_outcome_unsettled"
        elseif not state.observed then state.error="current_body_observation_required"
        else
            state.cancel={id=value.id,spellId=value.spellId,expectedOperationRevision=value.expectedOperationRevision,
                channel=value.channel==true,binding=copy(state.observed),phase="cancel_requested"}
            state.error=nil
        end
    elseif value.task=="cancel_spell_request" then
        if state.current and value.id==state.current.id then
            if not state.current.issued then finish("canceled_before_dispatch")
            else state.current.cancelRequested=true end
        end
    elseif value.task=="cast_spell" then
        if not id(value.id) or not uint32(value.spellId) or
           value.targets~=nil and type(value.targets)~="table" or
           value.trajectory~=nil and type(value.trajectory)~="table" or
           value.requirements~=nil and (type(value.requirements)~="table" or
               value.requirements.stationary~=nil and type(value.requirements.stationary)~="boolean" or
               value.requirements.channeled~=nil and type(value.requirements.channeled)~="boolean" or
               value.requirements.castTimeMs~=nil and (not number(value.requirements.castTimeMs) or value.requirements.castTimeMs<0) or
               not positioning(value.requirements.position)) then
            state.error="invalid_explicit_spell_request"
        elseif state.seen[value.id] or state.current and state.current.id==value.id then
            -- Delivery retries and reloads never replay an issued cast.
        elseif state.current and state.current.issued then
            state.error="existing_cast_outcome_unsettled"
        elseif not state.observed or not state.observed.sessionId or not state.observed.visitId then
            state.error="current_body_observation_required"
        else
            if state.current then finish("superseded_before_dispatch") end
            state.current={id=value.id,spellId=value.spellId,targets=copy(value.targets),
                trajectory=copy(value.trajectory),requirements=copy(value.requirements),binding=copy(state.observed),phase="requested"}
            state.error=nil
        end
    end
    return true
end

local function bound(holder,obs)
    local b=holder.binding
    return obs.sessionId==b.sessionId and obs.selfGuid==b.selfGuid
end
local function stopped(obs)
    local self=(obs.players or {})[obs.selfGuid]
    return self and self.movementKnown==true and self.moving~=true and
        (obs.navigation or {}).motionUnconfirmed~=true
end
local function prepare_action(holder,obs,sequence,now,mode)
    local prep=holder.preparation
    local a={capability="movement_owner",sessionId=obs.sessionId,selfGuid=obs.selfGuid,
        owner=prep.owner,mode=mode,token=mode~="acquire" and prep.token or nil,
        leaseMs=mode~="release" and M.preparation.lease_ms or nil}
    proposed_preparation={action=a,prep=prep,sequence=sequence,now=now}
    return a
end
local function lease_view(holder,obs,now)
    local prep=holder.preparation
    local owner=(obs.navigation or {}).movementOwner
    if not bound(holder,obs) then return nil,"body_changed" end
    if type(owner)~="table" then return nil,"movement_owner_unavailable" end
    if prep.released then return owner,"released" end
    if prep.operation then return owner,"waiting_for_preparation_receipt" end
    if prep.uncertainUntil then
        if owner.active==true and owner.owner==prep.owner or now<prep.uncertainUntil then
            return owner,"reconciling_finite_movement_grant"
        end
        prep.uncertainUntil=nil
    end
    if prep.token and (owner.active~=true or owner.owner~=prep.owner) then prep.token=nil end
    return owner
end
local function required(task,obs)
    local r=task.requirements or {}
    if r.position then return true end
    if r.stationary~=nil then return r.stationary end
    local spell=(obs.spells or {})[tostring(task.spellId)] or {}
    return r.channeled==true or (r.castTimeMs or spell.castTimeMs or 0)>0
end
local function status(task)
    return {id=task.id,spellId=task.spellId,phase=task.phase,cancelRequested=task.cancelRequested,
        receipt=copy(task.receipt),issued=task.issued,actionId=task.actionId,preparation=task.preparation and {phase=task.preparation.phase,
        owner=task.preparation.owner,receipt=copy(task.preparation.receipt)}}
end

function M.propose(frame,obs,sequence,geometry)
    proposed=nil
    local task=state.current
    if not task then return nil,state.error and {phase=state.error} or copy(state.last) end
    local binding=task.binding
    if not bound(task,obs) or obs.visitId~=binding.visitId then
        finish("body_or_visit_changed");return nil,copy(state.last)
    end
    local self=(obs.players or {})[obs.selfGuid]
    if self and self.aliveKnown==true and self.alive==false then
        finish(task.issued and "body_died_after_dispatch" or "body_died_before_dispatch")
        return nil,copy(state.last)
    end
    if task.issued or #frame.pending~=0 then return nil,status(task) end
    if state.releasing then task.phase="waiting_for_prior_movement_release";return nil,status(task) end
    local position=(task.requirements or {}).position
    local now=(frame.clock or {}).monotonicMs
    if position then
        if not number(now) then task.phase="positioning_clock_unavailable";return nil,status(task) end
        task.positionDeadline=task.positionDeadline or now+M.preparation.position_ms
        if (obs.navigation or {}).motionUnconfirmed==true then
            task.phase="positioning_motion_unconfirmed";return nil,status(task)
        end
        if now>=task.positionDeadline and stopped(obs) then
            finish("positioning_timeout");return nil,copy(state.last)
        elseif now>=task.positionDeadline and (not self or self.movementKnown~=true) then
            task.phase="positioning_timeout_waiting_for_stationary_facts";return nil,status(task)
        end
    end
    if required(task,obs) then
        local prep=task.preparation
        if not prep then prep={owner="spell:"..task.id};task.preparation=prep end
        local now=(frame.clock or {}).monotonicMs
        if not number(now) then prep.phase="task_clock_unavailable";return nil,status(task) end
        local owner,reason=lease_view(task,obs,now)
        if reason then prep.phase=reason;return nil,status(task) end
        if owner.active==true and owner.owner~=prep.owner then
            prep.phase="movement_owned_elsewhere";return nil,status(task)
        end
        if prep.stopWaiting then
            if not stopped(obs) or sequence==prep.afterSequence then
                prep.phase="waiting_for_observed_stationary_body";return nil,status(task)
            end
            prep.stopWaiting=nil
        end
        if (obs.navigation or {}).motionUnconfirmed==true then
            prep.phase="movement_outcome_unconfirmed";return nil,status(task)
        end
        -- STOP may be needed before acquisition because bounded body input can
        -- still own the writer. The exact self target needs no enemy/route.
        if self and self.movementKnown==true and self.moving==true then
            local a={capability="stop_movement",sessionId=obs.sessionId,selfGuid=obs.selfGuid,
                visitId=obs.visitId,observationSequence=sequence,targetGuid=obs.selfGuid,rangeYards=0}
            proposed_preparation={action=a,prep=prep,sequence=sequence,now=now}
            prep.phase="stopping_for_requested_spell";return a,status(task)
        end
        if not prep.token then
            if owner.active==true then prep.phase="waiting_for_owned_lease_expiry";return nil,status(task) end
            prep.phase="acquiring_stationary_cast_movement"
            return prepare_action(task,obs,sequence,now,"acquire"),status(task)
        end
        if not stopped(obs) or sequence==prep.afterSequence then
            prep.phase="waiting_for_observed_stationary_body";return nil,status(task)
        end
        if (tonumber(owner.remainingMs) or 0)<=M.preparation.renew_below_ms then
            prep.phase="renewing_stationary_cast_movement"
            return prepare_action(task,obs,sequence,now,"renew"),status(task)
        end
        prep.phase="stationary_cast_ready"
    end
    if position then
        local target=task.targets and task.targets.objectGuid
        local spell=(obs.spells or {})[tostring(task.spellId)]
        local unit=target and ((obs.targets or {})[target] or (obs.players or {})[target])
        if not geometry or not geometry.movement or obs.hostTargetRangeBandSupported~=true then
            task.phase="positioning_unsupported";return nil,status(task)
        end
        if not task.targets or task.targets.mask~=2 or not id(target) or target==obs.selfGuid or
           not unit or unit.guid~=target or unit.present~=true or unit.mapKnown~=true or unit.mapId~=obs.mapId or
           unit.positionKnown~=true or unit.positionInferred==true or unit.positionEstimated==true or
           not self or self.mapKnown~=true or self.mapId~=obs.mapId or self.positionKnown~=true or
           self.positionInferred==true or self.positionEstimated==true or not number(self.orientation or 0) then
            task.phase="positioning_target_facts_unavailable";return nil,status(task)
        end
        if not spell or spell.known~=true or spell.metadataKnown~=true or not number(spell.maxRange) or spell.maxRange<=0 then
            task.phase="positioning_spell_range_unavailable";return nil,status(task)
        end
        local options={minRangeYards=math.max(position.minRangeYards,spell.minRange or 0),
            rangeYards=math.min(position.maxRangeYards,spell.maxRange),requireLos=true,requireFacing=true}
        if geometry.ranged then options=geometry.ranged(obs,target,options) end
        if not options or options.rangeYards<=options.minRangeYards then
            task.phase="positioning_safe_range_unavailable";return nil,status(task)
        end
        local step=geometry.movement.decide(obs,sequence,target,options)
        task.phase=step.phase
        if not step.ready then return step.action,status(task) end
    end
    proposed={capability="cast_spell",sessionId=obs.sessionId,selfGuid=obs.selfGuid,
        visitId=obs.visitId,spellId=task.spellId,observationSequence=sequence,
        targets=copy(task.targets),trajectory=copy(task.trajectory)}
    return proposed,status(task)
end

-- Metadata maintenance deliberately runs outside the ordinary noPending cast
-- gate: an actual channel keeps its finite movement hold through reload. It
-- never cancels that cast, authors another spell, or changes standing follow.
function M.maintain(frame,obs,sequence)
    local task=state.current
    if task and (not bound(task,obs) or task.binding.visitId~=obs.visitId or
       ((obs.players or {})[obs.selfGuid] or {}).aliveKnown==true and
       ((obs.players or {})[obs.selfGuid] or {}).alive==false) then
        finish("body_or_visit_or_life_changed");task=nil
    end
    local holder=state.releasing or task
    if not holder or not holder.preparation then return nil,task and status(task) or copy(state.last) end
    local prep=holder.preparation
    local now=(frame.clock or {}).monotonicMs
    if not number(now) then prep.phase="task_clock_unavailable";return nil,task and status(task) or copy(state.last) end
    local owner,reason=lease_view(holder,obs,now)
    if state.releasing then
        if reason=="body_changed" or reason=="released" or owner and not reason and
           (owner.active~=true or owner.owner~=prep.owner) then
            state.releasing=nil;return nil,task and status(task) or copy(state.last)
        end
        if reason then prep.phase=reason;return nil,{phase=reason} end
        if prep.token then
            prep.phase="releasing_stationary_cast_movement"
            return prepare_action(holder,obs,sequence,now,"release"),{phase=prep.phase}
        end
        prep.phase="waiting_for_owned_lease_expiry";return nil,{phase=prep.phase}
    end
    if not task.issued or proposed or proposed_preparation then return nil,status(task) end
    if reason then prep.phase=reason;return nil,status(task) end
    if prep.token and owner.active==true and owner.owner==prep.owner and
       (tonumber(owner.remainingMs) or 0)<=M.preparation.renew_below_ms then
        prep.phase="renewing_pending_cast_movement"
        return prepare_action(task,obs,sequence,now,"renew"),status(task)
    end
    return nil,status(task)
end

function M.capture(frame)
    proposed_preparation=nil
    local task=state.current
    -- The scheduler selected exactly one action from an empty pending lane.
    -- Retain its native reservation ID whenever the next frame observes it.
    if task and task.issued and not task.actionId and not (state.cancel and state.cancel.issued) and
       #frame.pending==1 and id(frame.pending[1].action_id) then
        task.actionId=frame.pending[1].action_id
    end
end

function M.cancel_action(frame,obs,sequence)
    proposed_cancel=nil
    local cancel=state.cancel
    if not cancel then return nil,copy(state.lastCancel) end
    local bound=cancel.binding
    if obs.sessionId~=bound.sessionId or obs.selfGuid~=bound.selfGuid or obs.visitId~=bound.visitId then
        state.seen[cancel.id]="cancel_body_or_visit_changed";state.cancel=nil
        return nil,{phase="cancel_body_or_visit_changed"}
    end
    if cancel.issued then return nil,{id=cancel.id,phase=cancel.phase,receipt=copy(cancel.receipt)} end
    proposed_cancel={capability="cancel_spell",sessionId=obs.sessionId,selfGuid=obs.selfGuid,
        visitId=obs.visitId,observationSequence=sequence,spellId=cancel.spellId,
        expectedOperationRevision=cancel.expectedOperationRevision,channel=cancel.channel}
    return proposed_cancel,{id=cancel.id,phase="cancel_requested"}
end

-- Only the final scheduler winner reserves this script's request. A heal,
-- window-close or ownership release can preempt a proposal without losing it.
function M.commit(action)
    local selected=proposed_preparation
    if selected and action==selected.action then
        local a=selected.action
        selected.prep.operation={kind=a.capability,mode=a.mode,sequence=selected.sequence,
            untilMs=selected.now+M.preparation.lease_ms}
    end
    if proposed and action==proposed and state.current then
        state.current.issued=true;state.current.phase="awaiting_cast_receipt"
        state.seen[state.current.id]="issued"
    end
    if proposed_cancel and action==proposed_cancel and state.cancel then
        state.cancel.issued=true;state.cancel.phase="awaiting_cancel_transport"
        state.seen[state.cancel.id]="cancel_issued"
    end
    proposed=nil
    proposed_cancel=nil
    proposed_preparation=nil
end

function M.export_state() return {version=1,data=copy(state)} end
function M.pending() return state.current~=nil or state.cancel~=nil or state.releasing~=nil end
-- Read-only coordination for an exact caller-owned child request. This never
-- settles, cancels or retries native custody.
function M.request_status(request)
    if state.current and state.current.id==request then
        local out=status(state.current);out.current=true;return out
    end
    if state.last and state.last.id==request then
        local out=copy(state.last)
        out.pendingCleanup=state.releasing and state.releasing.id==request or false
        return out
    end
end
-- A coalesced spellbook change is an observation, not a failed cast. The host
-- only computes the actual ID delta; this accepted policy chooses when one
-- ordinary private review should inspect the new abilities.
function M.learning_review(input)
    local notice=input.notice
    if type(notice)~="table" or type(notice.spellIds)~="table" or #notice.spellIds==0 then
        return {phase="no_new_learned_spells"}
    end
    if input.nowMs-notice.changedAtMs<M.learning.quiet_ms or
       notice.stableObservations<M.learning.stable_observations then
        return {phase="coalescing_learned_spells"}
    end
    return {phase="review_new_learned_spells",ready=true}
end
local function valid_preparation(holder)
    if not holder or not holder.preparation then return true end
    local p,b=holder.preparation,holder.binding
    if type(p)~="table" or type(b)~="table" or not id(b.sessionId) or not id(b.selfGuid) or not id(b.visitId) or
       type(p.owner)~="string" or p.owner=="" or p.token~=nil and not id(p.token) or
       p.uncertainUntil~=nil and not number(p.uncertainUntil) then return false end
    local op=p.operation
    return op==nil or type(op)=="table" and
        (op.kind=="stop_movement" or op.kind=="movement_owner" and
         (op.mode=="acquire" or op.mode=="renew" or op.mode=="release")) and
        id(op.sequence) and number(op.untilMs) and (op.actionId==nil or id(op.actionId))
end
function M.import_state(checkpoint)
    if checkpoint.version~=1 or type(checkpoint.data)~="table" or type(checkpoint.data.seen)~="table" then return false end
    local task=checkpoint.data.current
    if task and (not id(task.id) or not uint32(task.spellId) or type(task.binding)~="table" or
       not id(task.binding.sessionId) or not id(task.binding.selfGuid) or not id(task.binding.visitId) or
       task.issued~=nil and type(task.issued)~="boolean" or
       task.positionDeadline~=nil and not number(task.positionDeadline) or
       task.requirements~=nil and (type(task.requirements)~="table" or not positioning(task.requirements.position))) then return false end
    if not valid_preparation(task) or not valid_preparation(checkpoint.data.releasing) then return false end
    local cancel=checkpoint.data.cancel
    if cancel and (not id(cancel.id) or not uint32(cancel.spellId) or type(cancel.binding)~="table" or
       not id(cancel.expectedOperationRevision)) then return false end
    state=copy(checkpoint.data);proposed=nil;proposed_preparation=nil
    return true
end
return M
