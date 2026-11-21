-- Choose consent for one observed invitation. The host and native party
-- capability retain exact body/session binding and one attempted decision.
local M = {tuning = {accept_owner_invites=true}}

local function wait(reason)
    return {kind="wait",reason=reason}
end

local function text(value)
    return type(value)=="string" and #value>=1 and #value<=128
end

local function positive_id(value)
    if type(value)~="string" or #value==0 or #value>20 or byte_at(value,1)==48 then return false end
    for i=1,#value do
        local digit=byte_at(value,i)
        if digit<48 or digit>57 then return false end
    end
    return #value<20 or value<="18446744073709551615"
end

function M.decide(input)
    if type(input)~="table" then return wait("invitation_context_invalid") end
    local count=0
    for key in pairs(input) do
        if key~="sessionId" and key~="selfGuid" and key~="ownerName" and key~="party" and key~="inviteDecisionType" then
            return wait("invitation_context_invalid")
        end
        count=count+1
    end
    if count~=5 or not text(input.sessionId) or not positive_id(input.selfGuid) or not text(input.ownerName) then
        return wait("invitation_identity_invalid")
    end
    local party=input.party
    if type(party)~="table" or party.sessionId~=input.sessionId then return wait("party_session_changed") end
    local invite=party.pendingInvite
    if type(invite)~="table" or not positive_id(invite.sequence) or not text(invite.inviterName) then
        return wait("observed_invitation_required")
    end
    -- This host shape fact preserves omitted/null and integer/float distinctions
    -- that JSON-to-Lua conversion alone cannot retain. It is not a native field.
    local pending=input.inviteDecisionType=="absent" and invite.decision==nil or
        input.inviteDecisionType=="string" and invite.decision=="PENDING" or
        input.inviteDecisionType=="integer" and type(invite.decision)=="number" and invite.decision==0
    if not pending then
        return wait("invitation_already_decided_or_unknown")
    end
    if M.tuning.accept_owner_invites~=true then return wait("owner_invitation_acceptance_disabled") end
    if invite.inviterName~=input.ownerName then return wait("inviter_is_not_configured_owner") end
    return {kind="accept",sessionId=input.sessionId,inviteSequence=invite.sequence,inviterName=invite.inviterName}
end

return M
