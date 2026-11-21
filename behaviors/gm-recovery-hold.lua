-- Wrap an existing attached session policy without replacing its event/receipt
-- handling. The recovery host CAS-restores the original source after its task.
return function(policy)
    local observation,sequence,retired=nil,nil,false
    local function retire(frame)
        local checkpoint=policy.export_state()
        local data=checkpoint.data or {}
        if type(data.encounter)=="table" then
            data.encounter.engaged={}
            data.encounter.focusGuid=nil
            data.encounter.burstIntent=nil
        end
        if type(data.commands)=="table" and type(data.commands.data)=="table" then
            data.commands.data.requests={}
            data.commands.data.killOrder={}
            data.commands.data.focus=nil
        end
        checkpoint.session_id,checkpoint.cursor,checkpoint.pending=frame.session_id,frame.cursor,frame.pending
        assert(policy.import_state(checkpoint)==true,"recovery boundary import refused")
        retired=true
    end
    return {
        on_event=function(event)
            if event.kind=="encounter_observation" and type(event.value)=="table" then
                observation,sequence=event.value,event.sequence
            end
            return policy.on_event(event)
        end,
        decide=function(frame)
            if not retired then retire(frame) end
            -- Recovery suppresses stale errands, never current self/party defense.
            local fighting,injured=false,false
            for _,player in pairs((observation or {}).players or {}) do
                if player.present==true and player.aliveKnown==true and player.alive==true
                    and player.combatKnown==true and player.inCombat==true then fighting=true end
                if player.present==true and player.aliveKnown==true and player.alive==true and player.healthKnown==true
                    and (tonumber(player.health) or 0)<(tonumber(player.maxHealth) or 0) then injured=true end
            end
            for target,engagement in pairs((observation or {}).engagement or {}) do
                local victim=((observation or {}).players or {})[tostring(engagement.victimGuid)]
                local creature=((observation or {}).targets or {})[target]
                if victim and victim.present==true and victim.aliveKnown==true and victim.alive==true
                    and creature and creature.present==true and creature.aliveKnown==true and creature.alive==true then fighting=true end
            end
            local ordinary
            if fighting or injured then ordinary=policy.decide(frame) end
            if fighting then return ordinary end
            local action=ordinary and ordinary.action
            local heal=action and action.capability=="heal"
            if action and action.capability=="approach_target" then
                local patient=((observation or {}).players or {})[tostring(action.targetGuid)]
                heal=patient and patient.present==true and patient.aliveKnown==true and patient.alive==true
                    and patient.healthKnown==true and (tonumber(patient.health) or 0)<(tonumber(patient.maxHealth) or 0)
            end
            if heal then return ordinary end
            local out={phase="gm_recovery_hold",queue={}}
            if observation and observation.sessionId==frame.session_id and observation.visitId
                and frame.input and frame.input.kind=="encounter" and frame.input.observationSequence==sequence then
                out.encounterOwnership={visitId=observation.visitId,observationSequence=sequence,
                    resurrection=true,interrupts=true}
            end
            return out
        end,
        export_state=policy.export_state,
        import_state=function(checkpoint)
            local ok=policy.import_state(checkpoint)
            retired=false
            return ok
        end,
    }
end
