-- Pure decision helpers; only the host can invoke a descriptor-checked RPC.
local tasks = {}

function tasks.await(context, phase)
    return {memory=context.memory, phase=phase, done=false}
end

function tasks.push(context, kind, data)
    assert(#context.memory.stack < 12, "task stack full")
    context.memory.stack[#context.memory.stack+1] = {kind=kind, data=data or {}, stage="start"}
end

function tasks.pop(context, result)
    context.memory.stack[#context.memory.stack] = nil
    local parent = context.memory.stack[#context.memory.stack]
    if parent then parent.child = result or {} end
end

-- The serial and waiting frame are returned in memory before the host reserves
-- transport. A restart consumes the old matching reply; it never repeats a call.
function tasks.call(context, frame, method, params, phase)
    if frame.waiting then
        assert(frame.waiting.method == method, "waiting method changed")
        local last = context.state.last
        if not last or last.id ~= frame.waiting.id then
            return nil, tasks.await(context, "waiting_for_call_result")
        end
        if last.error then return nil, tasks.await(context, "blocked_on_ambiguous_call") end
        assert(last.method == frame.waiting.method and type(last.response) == "table", "call reply mismatch")
        frame.waiting = nil
        return last.response, nil
    end
    context.memory.serial = context.memory.serial + 1
    local id = context.state.goal.id .. ":" .. tostring(context.memory.serial)
    frame.waiting = {id=id, method=method}
    return nil, {memory=context.memory, phase=phase or "calling", done=false,
                 action={id=id, method=method, params=params}}
end

function tasks.resolve(context, frame, observation, path, equals, outcome)
    assert(frame.waiting, "no reserved call to resolve")
    local id = frame.waiting.id
    frame.waiting = nil
    return {memory=context.memory, phase="observed_pending_resolution", done=false,
            resolve={id=id, observation=observation, path=path, equals=equals, outcome=outcome}}
end

function tasks.run(state, handlers, initial)
    local memory = state.memory or {}
    memory.serial = memory.serial or 0
    assert(type(memory.serial) == "number" and memory.serial >= 0 and memory.serial % 1 == 0, "invalid task serial")
    if not memory.stack then memory.stack = {{kind=initial, data={}, stage="start"}} end
    local context = {state=state, memory=memory, observations=state.observations or {}}
    for _=1,16 do
        local frame = memory.stack[#memory.stack]
        if not frame then return {memory=memory, phase="complete", done=true} end
        local handler = handlers[frame.kind]
        assert(type(handler) == "function", "unknown task kind")
        local result = handler(context, frame)
        if result then return result end
    end
    return tasks.await(context, "local_transition_budget")
end

return tasks
