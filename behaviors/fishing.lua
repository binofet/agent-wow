-- Goals supply content, budgets and candidate NPC entries. Only native APIs
-- establish ownership, offers, fishing outcomes and pure skill progress.
local function n(v) return tonumber(v) or 0 end
local function eq(a,b) return tostring(a or "") == tostring(b or "") end
local function guid(v) return type(v)=="string" and #v>0 and #v<=20 and v~="0" end
local function flag(v,bit) return math.floor(n(v)/bit)%2==1 end
local function reason(prefix,value) return prefix..(type(value)=="string" and #value<=80 and value or "unknown") end
local function await(c,phase) return tasks.await(c,phase) end
local function same(c,s) return type(s)=="table" and eq(s.sessionId,c.state.body.sessionId) end
local function quiet(c)
    local a,w=c.observations.actions or {},(c.observations.world or {}).self or {}
    if not same(c,a) or not eq(a.selfGuid,c.state.body.selfGuid) or not eq(w.guid,c.state.body.selfGuid)
       or not w.present or not w.aliveKnown or not w.alive or not w.healthKnown or n(w.maxHealth)<=0
       or n(w.health)>n(w.maxHealth) or n(w.health)/n(w.maxHealth)<0.85 or not w.unitFlagsKnown
       or flag(w.unitFlags,0x80000) or not w.mapKnown or not w.movementFlagsKnown
       or (n(w.movementFlags)%0x10000000~=0 and n(w.movementFlags)%0x10000000~=0x100)
       or n(w.movementFlags)<0 or n(w.movementFlags)>=0x80000000
       or n(w.extraMovementFlags)~=0 then return false end
    if a.moving or a.traveling or a.motionUnconfirmed or a.castPending or a.autoAttacking
       or a.autoAttackRequested or a.autoAttackUnconfirmed or (a.recovery or {}).active
       or (a.interactionOwner or "")~="" then return false end
    return true
end
local function skill(c)
    local w=c.observations.world or {}
    if not w.selfSkillsKnown or n(w.selfSkillsRevision)<=0 or #(w.selfSkills or {})~=128 then return nil end
    local slots,found={},nil
    for index,row in ipairs(w.selfSkills) do
        local slot=n(row.slot)
        if slot<0 or slot>=128 or slots[slot] then return nil end
        slots[slot]=true
        if eq(row.skillId,c.state.goal.params.skillId) then
            if found or n(row.value)<=0 or n(row.value)>n(row.maxValue) then return nil end
            found={rank=n(row.value),index=index-1}
        end
    end
    return found or {rank=0}
end
local function backpack(c)
    local w=c.observations.world or {}
    if not w.backpackKnown or n(w.backpackRevision)<=0 or #(w.backpack or {})~=16 then return nil end
    local slots,seen={},{}
    for _,row in ipairs(w.backpack) do
        local slot=n(row.slot)
        if n(row.bag)~=0 or slot<0 or slot>=16 or slots[slot] then return nil end
        slots[slot]=row
        if guid(row.itemGuid) then
            if not row.metadataKnown or n(row.entry)<=0 or n(row.stackCount)<=0 or seen[row.itemGuid] then return nil end
            seen[row.itemGuid]=true
        elseif n(row.entry)~=0 or n(row.stackCount)~=0 then return nil end
    end
    return slots
end
local function owned(c,entry)
    local bag=backpack(c);if not bag then return nil,nil,false end
    local world=c.observations.world or {};local bags=world.inventoryBags or {}
    if not world.inventoryKnown or #bags~=5 then return nil,nil,false end
    local bagSeen,carried,carriedSlot={},nil,nil
    for _,container in ipairs(bags) do
        local logical=n(container.bag)
        if logical<0 or logical>4 or bagSeen[logical] or not container.slotKnown or not container.contentsKnown
           or #(container.slots or {})~=n(container.slotCount) then return nil,nil,false end
        bagSeen[logical]=true
        for _,row in ipairs(container.slots or {}) do
            if guid(row.itemGuid) then
                if not row.metadataKnown or n(row.entry)<=0 or n(row.stackCount)<=0 then return nil,nil,false end
                if eq(row.entry,entry) then
                    if carried then return nil,nil,false end
                    carried,carriedSlot=row,logical==0 and n(row.slot) or nil
                end
            end
        end
    end
    local hand=((c.observations.world or {}).self or {}).mainhand or {}
    if not hand.slotKnown or (guid(hand.itemGuid) and not hand.metadataKnown) then return nil,nil,false end
    if hand.metadataKnown and guid(hand.itemGuid) and eq(hand.entry,entry) then return hand,nil,true end
    return carried,carriedSlot,true
end
local function body_state(c,s)
    return same(c,s) and (not s.selfGuid or eq(s.selfGuid,c.state.body.selfGuid))
end
local function request_id(c)
    local id=c.state.goal.params.requestPrefix or c.state.goal.id
    assert(type(id)=="string" and #id>0 and #id<=48,"goal ID must be a bounded native request prefix")
    return id.."_"..tostring(c.memory.serial+1)
end
local function query(c,f,method)
    if not f.waiting and n(c.state.now)<n(f.pollAfter) then return nil,await(c,"waiting_for_observed_receipt") end
    local reply,out=tasks.call(c,f,method,{},"reading_current_operation")
    if out then return nil,out end
    f.pollAfter=n(c.state.now)+5
    return reply
end
local function actual_npc(c,reply,entry)
    if not body_state(c,reply.state) then return nil end
    local found
    for _,row in ipairs(reply.nearby or {}) do
        local npc=row.creature or {}
        if eq(npc.entry,entry) and row.interactable and npc.present and npc.aliveKnown and npc.alive
           and npc.mapKnown and npc.positionKnown and guid(npc.guid) then
            if found then return nil end
            found=npc.guid
        end
    end
    return found
end
local function supply_proof(c,f,s)
    local b,o=f.binding or {},s.operation or {}
    if not body_state(c,s) or s.active or not eq(o.requestId,b.requestId) or o.kind~=b.kind
       or o.status~="CONFIRMED" or not o.transportWritten or not o.leaseReleased then return false end
    if b.kind=="buy" then
        return eq(o.npcGuid,b.npcGuid) and eq(o.itemId,b.itemId) and n(o.count)==1
           and n(o.costCopper)==n(b.expectedPriceCopper) and o.serverSuccessObserved and o.inventoryConfirmed
           and o.copperConfirmed and eq(o.baselineBackpackRevision,b.expectedBackpackRevision)
           and eq(o.baselineCopperRevision,b.expectedCopperRevision)
           and n(s.backpackRevision)>n(b.expectedBackpackRevision) and n(s.copperRevision)>n(b.expectedCopperRevision)
    end
    if not o.equipmentConfirmed or not eq(o.itemGuid,b.itemGuid) or not eq(o.originalWeaponGuid,b.originalWeaponGuid) then return false end
    local hand=s.mainhand or {}
    if not hand.slotKnown or not hand.metadataKnown or not eq(hand.itemGuid,b.itemGuid) then return false end
    if b.kind=="restore" then return s.restoreRequired~=true end
    if not s.restoreRequired or not eq(s.originalWeaponGuid,b.originalWeaponGuid) or not eq(s.equippedPoleGuid,b.itemGuid) then return false end
    for _,row in ipairs(s.backpack or {}) do
        if n(row.slot)==n(b.backpackSlot) and row.metadataKnown and n(row.stackCount)==1
           and eq(row.itemGuid,b.displacedGuid) then return true end
    end
    return false
end
local function receipt_state(c,f,s)
    if not body_state(c,s) then return await(c,"operation_body_unavailable") end
    if supply_proof(c,f,s) then
        c.memory.receipts=c.memory.receipts or {};c.memory.receipts[#c.memory.receipts+1]=s.operation
        tasks.pop(c,{status="confirmed",state=s});return
    end
    local o=s.operation or {}
    if eq(o.requestId,(f.binding or {}).requestId) and (o.status=="BLOCKED" or o.status=="SERVER_REJECTED") then
        f.receipt=o;f.stoppedPhase=o.status=="BLOCKED" and "supply_blocked" or "supply_server_rejected"
        return await(c,f.stoppedPhase)
    end
    if o.status=="OUTCOME_UNCONFIRMED" then return await(c,"supply_outcome_unconfirmed") end
    return await(c,"waiting_for_exact_supply_receipt")
end
local handlers={}
handlers.purchase=function(c,f)
    if f.stoppedPhase then return await(c,f.stoppedPhase) end
    if f.stage=="start" then
        if not quiet(c) then return await(c,"purchase_waiting_for_safe_idle") end
        local reply,out=tasks.call(c,f,"buddy-supplies.discover",{},"discovering_actual_vendor")
        if out then return out end
        f.guid=actual_npc(c,reply,c.state.goal.params.vendor.entry)
        if not f.guid then return await(c,"actual_vendor_not_interactable") end
        f.stage="list"
    end
    if f.stage=="list" then
        local s,out=tasks.call(c,f,"buddy-supplies.list",{sessionId=c.state.body.sessionId,npcGuid=f.guid},"requesting_vendor_offer")
        if out then return out end
        if not body_state(c,s) then return await(c,"vendor_offer_body_unavailable") end
        local offer=s.offer or {};local selected
        for _,row in ipairs(offer.items or {}) do
            if eq(row.entry,f.data.entry) then if selected then return await(c,"vendor_item_ambiguous") end;selected=row end
        end
        if not selected or not eq(offer.npcGuid,f.guid) or n(offer.revision)<=0 or n(offer.expiresInMs)<=0 then return await(c,"fresh_vendor_item_unavailable") end
        if n(selected.buyCount)~=1 or n(selected.extendedCost)~=0 or n(selected.availableCount)==0
           or n(selected.priceCopper)<=0 or n(selected.priceCopper)>n(f.data.budget) or not s.copperKnown
           or n(s.copper)<n(selected.priceCopper) or not s.backpackKnown or not s.skillsKnown then
            f.stoppedPhase="vendor_stock_price_or_budget_blocked";return await(c,f.stoppedPhase)
        end
        f.binding={sessionId=c.state.body.sessionId,requestId=request_id(c),expectedRevision=s.revision,
            npcGuid=f.guid,offerRevision=offer.revision,vendorSlot=n(selected.slot),itemId=f.data.entry,count=1,
            expectedPriceCopper=n(selected.priceCopper),maxSpendCopper=f.data.budget,
            expectedBackpackRevision=s.backpackRevision,expectedCopperRevision=s.copperRevision,expectedSkillsRevision=s.skillsRevision}
        f.stage="buy"
    end
    if f.stage=="buy" then
        if not f.waiting and not quiet(c) then return await(c,"purchase_waiting_for_safe_idle") end
        local s,out=tasks.call(c,f,"buddy-supplies.buy",f.binding,"buying_quoted_supply")
        if out then return out end
        f.binding.kind="buy";f.stage="receipt";f.pollAfter=0
        if supply_proof(c,f,s) then return receipt_state(c,f,s) end
        f.lastState=s
    end
    if f.stage=="receipt" then
        local s,out=query(c,f,"buddy-supplies.state");if out then return out end
        return receipt_state(c,f,s)
    end
end
handlers.learn=function(c,f)
    if f.stoppedPhase then return await(c,f.stoppedPhase) end
    local p=c.state.goal.params
    if f.stage=="start" then
        if not quiet(c) then return await(c,"training_waiting_for_safe_idle") end
        local reply,out=tasks.call(c,f,"buddy-training.discover_professions",{},"discovering_actual_trainer")
        if out then return out end
        f.guid=actual_npc(c,reply,p.trainer.entry)
        if not f.guid then return await(c,"actual_trainer_not_interactable") end
        f.stage="list"
    end
    if f.stage=="list" then
        local s,out=tasks.call(c,f,"buddy-training.list",{sessionId=c.state.body.sessionId,npcGuid=f.guid,trainerType=2},"requesting_profession_offer")
        if out then return out end
        local o=s.offer or {};local selected
        for _,row in ipairs(o.spells or {}) do if eq(row.spellId,p.trainerSpell) then if selected then return await(c,"trainer_spell_ambiguous") end;selected=row end end
        if not same(c,s) or not eq(o.npcGuid,f.guid) or n(o.trainerType)~=2 or n(o.expiresInMs)<=0 or n(o.revision)<=0
           or not selected or n(selected.usable)~=0 or not eq(selected.professionSkillId,p.skillId)
           or not eq(selected.learnedSpellId,p.spellId) or n(selected.costCopper)>p.trainingBudgetCopper
           or not s.copperKnown or n(s.copper)<n(selected.costCopper) then return await(c,"fresh_training_offer_unavailable") end
        f.binding={purchase={sessionId=c.state.body.sessionId,npcGuid=f.guid,offerRevision=o.revision,
            spellId=p.trainerSpell,expectedCostCopper=n(selected.costCopper),maxSpendCopper=p.trainingBudgetCopper},skillId=p.skillId}
        f.baselineSkills=(c.observations.world or {}).selfSkillsRevision;f.stage="buy"
    end
    if f.stage=="buy" then
        if not f.waiting and not quiet(c) then return await(c,"training_waiting_for_safe_idle") end
        local s,out=tasks.call(c,f,"buddy-training.buy_profession",f.binding,"learning_offered_profession")
        if out then return out end
        f.stage="receipt";f.pollAfter=0
    end
    if f.stage=="receipt" then
        local s,out=query(c,f,"buddy-training.state");if out then return out end
        local o=s.lastPurchase or {};local rank=skill(c)
        if same(c,s) and not s.actionInFlight and eq(o.npcGuid,f.guid) and eq(o.spellId,p.trainerSpell)
           and eq(o.professionSkillId,p.skillId) and n(o.costCopper)==n(f.binding.purchase.expectedCostCopper)
           and (o.status=="TRAINING_CONFIRMED" or o.status==3) and o.transportWritten and o.serverSuccessObserved
           and o.professionSkillObserved and eq(o.baselineSkillsRevision,f.baselineSkills)
           and n(o.confirmedSkillsRevision)>n(f.baselineSkills)
           and n((c.observations.world or {}).selfSkillsRevision)>=n(o.confirmedSkillsRevision) and rank and rank.rank>0 then
            c.memory.learningReceipt=o;tasks.pop(c,{status="learned"});return
        end
        if o.status=="SERVER_REJECTED" or o.status==4 then
            f.stoppedPhase="training_server_rejected";return await(c,f.stoppedPhase)
        end
        return await(c,"waiting_for_actual_learned_skill")
    end
end
handlers.equipment=function(c,f)
    if f.stoppedPhase then return await(c,f.stoppedPhase) end
    if f.stage=="start" then
        if not quiet(c) then return await(c,"equipment_waiting_for_safe_idle") end
        local off=((c.observations.world or {}).self or {}).offhand or {}
        if not off.slotKnown or guid(off.itemGuid) or n(off.entry)~=0 then return await(c,"offhand_unknown_or_occupied") end
        local s,out=tasks.call(c,f,"buddy-supplies.state",{},"reading_owned_pole_and_restore_history")
        if out then return out end
        if not body_state(c,s) or s.active or not s.backpackKnown or not s.skillsKnown then return await(c,"equipment_observations_unknown") end
        local selected
        for _,row in ipairs(s.backpack or {}) do
            if (f.data.restore and eq(row.itemGuid,c.memory.originalWeaponGuid)) or (not f.data.restore and eq(row.entry,f.data.entry)) then
                if selected then return await(c,"equipment_source_ambiguous") end;selected=row
            end
        end
        local hand=s.mainhand or {}
        if not selected or not selected.metadataKnown or n(selected.stackCount)~=1 or n(selected.slot)>=16
           or not guid(selected.itemGuid) or not hand.slotKnown or not hand.metadataKnown or not guid(hand.itemGuid) then return await(c,"actual_owned_equipment_source_unavailable") end
        local original=s.restoreRequired and s.originalWeaponGuid or hand.itemGuid
        if f.data.restore and (not s.restoreRequired or not eq(original,c.memory.originalWeaponGuid)) then return await(c,"original_restore_history_changed") end
        f.binding={sessionId=c.state.body.sessionId,requestId=request_id(c),expectedRevision=s.revision,
            expectedBackpackRevision=s.backpackRevision,expectedSkillsRevision=s.skillsRevision,
            backpackSlot=n(selected.slot),itemGuid=selected.itemGuid,originalWeaponGuid=original}
        f.displaced=hand.itemGuid;f.priorRestoreRequired=s.restoreRequired==true;f.stage="equip"
    end
    if f.stage=="equip" then
        if not f.waiting and not quiet(c) then return await(c,"equipment_waiting_for_safe_idle") end
        local s,out=tasks.call(c,f,f.data.restore and "buddy-supplies.restore" or "buddy-supplies.equip",f.binding,"changing_exact_owned_equipment")
        if out then return out end
        f.binding.kind=f.data.restore and "restore" or "equip";f.binding.displacedGuid=f.displaced
        f.stage="receipt";f.pollAfter=0
        if not f.data.restore then
            c.memory.originalWeaponGuid=f.binding.originalWeaponGuid;c.memory.changedEquipment=not f.priorRestoreRequired
        end
        if supply_proof(c,f,s) then return receipt_state(c,f,s) end
    end
    if f.stage=="receipt" then
        local s,out=query(c,f,"buddy-supplies.state");if out then return out end
        return receipt_state(c,f,s)
    end
end
handlers.practice=function(c,f)
    if f.stoppedPhase then return await(c,f.stoppedPhase) end
    local p=c.state.goal.params
    if f.stage=="start" then
        if not quiet(c) then return await(c,"fishing_waiting_for_safe_idle") end
        local rank=skill(c);if not rank then return await(c,"pure_skill_observations_unknown") end
        if rank.rank<p.targetRank and n(c.memory.attempts)>=p.maxCasts then return await(c,"fishing_cast_budget_exhausted") end
        local s,out=tasks.call(c,f,"buddy-professions.state",{},"reading_known_fishing_recipe")
        if out then return out end
        local old=s.operation or {}
        if not body_state(c,s) or s.active or s.closing or old.cleanupUnconfirmed or old.status=="OUTCOME_UNCONFIRMED"
           or old.status==10 then return await(c,"previous_profession_outcome_unsettled") end
        if rank.rank>=p.targetRank then tasks.pop(c,{status="rank_observed",rank=rank.rank});return end
        local recipe
        for _,row in ipairs(s.recipes or {}) do if eq(row.skillId,p.skillId) and eq(row.spellId,p.spellId) then recipe=row end end
        if not body_state(c,s) or s.active or s.closing or not s.skillsKnown or not s.inventoryKnown
           or not recipe or not recipe.known or not recipe.requirementsVerified or not recipe.ready then return await(c,reason("fishing_not_ready_",(recipe or {}).reason)) end
        f.binding={sessionId=c.state.body.sessionId,requestId=request_id(c),expectedRevision=s.revision,
            skillId=p.skillId,spellId=p.spellId,expectedSkillsRevision=s.skillsRevision,
            expectedBackpackRevision=s.backpackRevision,expectedInventoryRevision=s.inventoryRevision}
        f.stage="cast"
    end
    if f.stage=="cast" then
        if not f.waiting and not quiet(c) then return await(c,"fishing_waiting_for_safe_idle") end
        local s,out=tasks.call(c,f,"buddy-professions.attempt",f.binding,"attempting_native_fishing")
        if out then if out.action then c.memory.attempts=n(c.memory.attempts)+1 end;return out end
        f.stage="receipt";f.pollAfter=0
    end
    if f.stage=="receipt" then
        local s,out=query(c,f,"buddy-professions.state");if out then return out end
        local o=s.operation or {}
        if not body_state(c,s) or not eq(o.requestId,f.binding.requestId) or not eq(o.skillId,p.skillId)
           or not eq(o.spellId,p.spellId) or not eq(o.baselineSkillsRevision,f.binding.expectedSkillsRevision)
           or not eq(o.baselineInventoryRevision,f.binding.expectedInventoryRevision) then return await(c,"fishing_receipt_binding_unavailable") end
        if o.status=="OUTCOME_UNCONFIRMED" or o.status==10 or o.cleanupUnconfirmed then return await(c,"fishing_outcome_unconfirmed") end
        if o.status=="SERVER_REJECTED" or o.status==8 then
            f.failure=o;f.stoppedPhase="fishing_server_rejected";return await(c,f.stoppedPhase)
        end
        if o.status=="BLOCKED" or o.status==7 or o.status=="CANCELLED" or o.status==9 then
            f.stoppedPhase=reason("fishing_stopped_",o.reason);return await(c,f.stoppedPhase)
        end
        if not s.active and (o.status=="COMPLETED" or o.status==6) and o.leaseReleased and o.castTransportWritten
           and o.ownGoObserved and o.ownBobberObserved and o.channelStartObserved and o.channelStopObserved
           and o.splashObserved and o.lootWindowObserved and o.lootReleaseObserved
           and (n(o.lootSlotsRemoved)==0 or (n(o.ownLootedItemsObserved)>0 and o.outputInventoryObserved
                and n(o.observedInventoryRevision)>n(o.baselineInventoryRevision))) then
            c.memory.lastFishingReceipt=o;f.stage="start";f.binding=nil;f.pollAfter=0
            return await(c,"fishing_receipt_observed_checking_pure_rank")
        end
        return await(c,"waiting_for_native_fishing_cleanup")
    end
end
handlers.mission=function(c,f)
    local p=c.state.goal.params;local rank=skill(c)
    if not rank then return await(c,"pure_skill_observations_unknown") end
    if f.stage=="start" then
        local basic,_,known=owned(c,p.basicPole);local strong,_,strongKnown=owned(c,p.strongPole)
        if not known or not strongKnown then return await(c,"owned_pole_observations_unknown") end
        f.stage="learn"
        if not basic and not (strong and rank.rank>=p.targetRank) then tasks.push(c,"purchase",{entry=p.basicPole,budget=p.basicBudgetCopper});return end
    end
    if f.stage=="learn" then
        f.stage="equip";if rank.rank==0 then tasks.push(c,"learn");return end
    end
    if f.stage=="equip" then
        local hand=((c.observations.world or {}).self or {}).mainhand or {}
        f.stage="practice"
        if not hand.metadataKnown or not (eq(hand.entry,p.basicPole) or (eq(hand.entry,p.strongPole) and rank.rank>=p.targetRank)) then
            local strong=owned(c,p.strongPole);tasks.push(c,"equipment",{entry=strong and rank.rank>=p.targetRank and p.strongPole or p.basicPole});return
        end
    end
    if f.stage=="practice" then f.stage="restore";tasks.push(c,"practice");return end
    if f.stage=="restore" then
        f.stage="election";if c.memory.changedEquipment then tasks.push(c,"equipment",{restore=true});return end
    end
    if f.stage=="election" then
        rank=skill(c);if not rank or rank.rank<p.targetRank then return await(c,"pure_rank_target_not_observed") end
        local key=p.sharedKey;local elected=(c.state.shared or {})[key]
        if not elected then
            return {memory=c.memory,phase="claiming_observed_skill_target",sharedClaim={key=key,evidence={
                {observation="world",path="selfSkillsKnown",operator="equals",value=true},
                {observation="world",path="selfSkills."..rank.index..".skillId",operator="equals",value=p.skillId},
                {observation="world",path="selfSkills."..rank.index..".value",operator="gte",value=p.targetRank}}}}
        end
        c.memory.election=elected;c.memory.observedPureRank=rank.rank
        if not eq(elected.winnerGuid,c.state.body.selfGuid) then tasks.pop(c,{status="rank_observed_nonwinner"});return end
        local strong,_,known=owned(c,p.strongPole)
        if not known then return await(c,"owned_strong_pole_observations_unknown") end
        f.stage="complete"
        if not strong then tasks.push(c,"purchase",{entry=p.strongPole,budget=p.strongBudgetCopper});return end
    end
    if f.stage=="complete" then tasks.pop(c,{status="rank_and_winner_purchase_observed"}) end
end
return {decide=function(state)
    local p=state.goal.params
    for _,key in ipairs({"skillId","spellId","basicPole","strongPole","targetRank","trainerSpell","maxCasts","basicBudgetCopper","trainingBudgetCopper","strongBudgetCopper"}) do
        assert(type(p[key])=="number" and p[key]>0 and p[key]%1==0,"positive fishing goal field required: "..key)
    end
    assert(p.maxCasts<=64 and p.targetRank<=450 and p.basicBudgetCopper<=10000 and p.trainingBudgetCopper<=10000
           and p.strongBudgetCopper<=10000 and type(p.vendor)=="table" and type(p.trainer)=="table"
           and type(p.sharedKey)=="string","bounded fishing goal required")
    if #(state.pending or {})>0 then return {memory=state.memory,phase="blocked_on_ambiguous_call",done=false} end
    return tasks.run(state,handlers,"mission")
end}
