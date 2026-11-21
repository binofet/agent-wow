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
local M={}
local state={seen={}}
local proposed
local proposed_cancel
local function id(s) return type(s)=="string" and #s>0 and #s<=128 end
local function uint32(n) return type(n)=="number" and n>0 and n<=4294967295 and n%1==0 end
local function copy(value)
    if type(value)~="table" then return value end
    local out={}
    for key,item in pairs(value) do out[key]=copy(item) end
    return out
end
local function finish(reason,receipt)
    local task=state.current
    state.seen[task.id]=reason
    state.last={id=task.id,spellId=task.spellId,phase=reason,receipt=receipt}
    state.current=nil;state.error=nil
end

function M.on_event(event)
    if event.kind=="encounter_observation" and type(event.value)=="table" then
        local o=event.value
        state.observed={sessionId=o.sessionId,selfGuid=o.selfGuid,visitId=o.visitId}
    end
    local task=state.current
    local cancel=state.cancel
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
                          grpcMessage=p.grpc_message}
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
           value.trajectory~=nil and type(value.trajectory)~="table" then
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
                trajectory=copy(value.trajectory),binding=copy(state.observed),phase="requested"}
            state.error=nil
        end
    end
    return true
end

function M.propose(frame,obs,sequence)
    proposed=nil
    local task=state.current
    if not task then return nil,state.error and {phase=state.error} or copy(state.last) end
    local binding=task.binding
    if obs.sessionId~=binding.sessionId or obs.selfGuid~=binding.selfGuid or obs.visitId~=binding.visitId then
        finish("body_or_visit_changed");return nil,copy(state.last)
    end
    local self=(obs.players or {})[obs.selfGuid]
    if not task.issued and self and self.aliveKnown==true and self.alive==false then
        finish("body_died_before_dispatch");return nil,copy(state.last)
    end
    local status={id=task.id,spellId=task.spellId,phase=task.phase,cancelRequested=task.cancelRequested,
                  receipt=copy(task.receipt)}
    if task.issued or #frame.pending~=0 then return nil,status end
    proposed={capability="cast_spell",sessionId=obs.sessionId,selfGuid=obs.selfGuid,
        visitId=obs.visitId,spellId=task.spellId,observationSequence=sequence,
        targets=copy(task.targets),trajectory=copy(task.trajectory)}
    return proposed,status
end

function M.capture(frame)
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
end

function M.export_state() return {version=1,data=copy(state)} end
function M.pending() return state.current~=nil end
function M.import_state(checkpoint)
    if checkpoint.version~=1 or type(checkpoint.data)~="table" or type(checkpoint.data.seen)~="table" then return false end
    local task=checkpoint.data.current
    if task and (not id(task.id) or not uint32(task.spellId) or type(task.binding)~="table" or
       not id(task.binding.sessionId) or not id(task.binding.selfGuid) or not id(task.binding.visitId) or
       task.issued~=nil and type(task.issued)~="boolean") then return false end
    local cancel=checkpoint.data.cancel
    if cancel and (not id(cancel.id) or not uint32(cancel.spellId) or type(cancel.binding)~="table" or
       not id(cancel.expectedOperationRevision)) then return false end
    state=copy(checkpoint.data);proposed=nil
    return true
end
return M
