-- Public party-mana policy. The existing worker supplies observations and
-- invokes the selected installed primitive; it does not choose water or peers.
local M={stock_minimum=5,peer_reorder_threshold=12,poll_ms=4000,trade_yards=10,
    water_family=5504,recipient_priority={}}
local function n(v) return tonumber(v) or 0 end
local function id(v)
    if type(v)=='string' then return v end
    if type(v)=='number' and v>=0 and v<=9007199254740991 then return tostring(v) end
    return ''
end
local function after(a,b)
    a,b=id(a),id(b)
    return #a>#b or (#a==#b and a>b)
end
local function body(p,guid)
    return type(p)=='table' and id(p.guid)==id(guid) and p.present==true
        and p.aliveKnown==true and p.alive==true and p.unitFlagsKnown==true
        and math.floor(n(p.unitFlags)/524288)%2==0
end
local function current(v,guid)
    local a=v and v.actions or {};local sid=a.sessionId
    return sid and id(a.selfGuid)==id(guid) and (v.trade or {}).sessionId==sid
        and (v.inventory or {}).sessionId==sid and (v.party or {}).sessionId==sid
end
local function free(v,guid)
    if not current(v,guid) then return false end
    local a,t=v.actions,v.trade
    return (t.phase=='IDLE' or t.phase=='COMPLETE') and not t.waterGiftArmed and not v.mana_rest_active
        and not(a.moving or a.motionUnconfirmed or a.traveling or a.castPending or (a.interactionOwner and a.interactionOwner~='')
            or a.autoAttacking or a.autoAttackRequested or a.autoAttackUnconfirmed)
end
local function group(v,guid,owner)
    if not current(v,guid) then return nil end
    local g=v.party.group or {};local own,leader=false,false
    if g.known~=true or g.inGroup~=true then return nil end
    for _,p in ipairs(g.members or {}) do
        if id(p.guid)==id(guid) and p.self==true and p.online==true then own=true end
        if id(p.guid)==id(owner) and p.online==true then leader=true end
    end
    if own and leader and n(g.groupGuid)>0 then return id(g.groupGuid) end
end
local function position(v)
    local p=(v.actions or {}).position
    if p then return {positionKnown=p.known,mapKnown=p.mapKnown,mapId=p.mapId,x=p.x,y=p.y,z=p.z} end
    return (v.world or {}).self or {}
end
local function nearby(v,guid)
    for _,p in ipairs((v.world or {}).nearbyPlayers or {}) do
        if id(p.guid)==id(guid) and p.present==true then return p end
    end
    return {}
end
local function near(a,b)
    if not(a.positionKnown and b.positionKnown and a.mapKnown and b.mapKnown)
        or n(a.mapId)~=n(b.mapId) then return false end
    local x,y,z=n(a.x)-n(b.x),n(a.y)-n(b.y),n(a.z)-n(b.z)
    return x*x+y*y+z*z<=M.trade_yards*M.trade_yards
end
local function count(v,guid)
    local inv=v.inventory or {}
    if current(v,guid) and id(inv.selfGuid)==id(guid) and inv.inventoryKnown==true
        and inv.manaDrinkCount~=nil then return n(inv.manaDrinkCount) end
end
local function slots(v)
    local out={}
    for _,bag in ipairs((v.inventory or {}).inventoryBags or {}) do
        if bag.contentsKnown~=true or n(bag.slotCount)~=#(bag.slots or {}) then return nil end
        for _,slot in ipairs(bag.slots or {}) do out[#out+1]=slot end
    end
    return out
end
local function max_stack(meta)
    local raw=(meta.rawTemplateFacts or {}).maxStackCount
    if raw==nil then return nil end -- Older eight-field metadata did not observe this value.
    raw=n(raw);if raw>=2147483648 then raw=raw-4294967296 end
    if raw<=0 or raw==2147483647 then return 2147483646 end
    return raw
end
local function capacity(rows,entry,meta,want)
    if not rows then return 0 end
    for _,slot in ipairs(rows) do if n(slot.itemGuid)==0 then return want end end
    local maximum=max_stack(meta);if not maximum then return 0 end
    local room=0
    for _,slot in ipairs(rows) do
        if n(slot.entry)==entry and slot.metadataKnown and n(slot.stackCount)>0 then
            room=room+math.max(0,maximum-n(slot.stackCount))
        end
    end
    return room
end
local function ranks(v,metadata)
    local toolkit=v.spell_toolkit or {};local out={}
    if toolkit.sessionId~=v.actions.sessionId or id(toolkit.selfGuid)~=id(v.actions.selfGuid)
        or toolkit.spellbookKnown~=true then return out end
    for _,spell in ipairs(toolkit.spells or {}) do
        local entry=n(spell.createdItemEntry);local meta=metadata[id(entry)]
        if n(spell.familyId)==M.water_family and entry>0 and meta and meta.available==true then
            local candidate={rank=n(spell.rank),spellId=n(spell.spellId),requiredLevel=n(meta.requiredLevel),meta=meta,spell=spell}
            local old=out[entry]
            if not old or candidate.rank>old.rank or candidate.rank==old.rank and candidate.spellId>old.spellId then out[entry]=candidate end
        end
    end
    return out
end
local function affordable(rank,player)
    local spell=rank.spell;local base=n(player.maxPower)
    if player.baseManaKnown==true and n(player.baseMana)>0 then base=n(player.baseMana) end
    local cost=n(spell.flatCost)+n((spell.mechanics or {}).costPerLevel)*n(player.level)
        +math.ceil(base*n(spell.manaPercent)/100)
    return player.powerKnown==true and n(player.powerType)==n(spell.powerType) and n(player.power)>=cost
end
local function compatible(all,p)
    local out={};if p.levelKnown~=true then return out end
    for entry,rank in pairs(all) do if rank.requiredLevel<=n(p.level) then out[entry]=rank end end
    return out
end
local function carried(v,usable,spare,destination)
    local world=v.world or {};local choices={}
    local function add(item,source)
        local entry,amount=n(item.entry),n(item.stackCount);local rank=usable[entry]
        if rank and item.metadataKnown==true and math.floor(n(item.itemGuid)/281474976710656)==16384
            and amount>0 and amount<=spare and capacity(destination,entry,rank.meta,amount)>=amount then
            choices[#choices+1]={item=item,source=source,rank=rank.rank,count=amount,entry=entry}
        end
    end
    if world.backpackKnown and n(world.backpackRevision)>0 then
        for _,item in ipairs(world.backpack or {}) do
            add(item,{backpackRevision=id(world.backpackRevision),backpackSlot=n(item.slot)})
        end
    end
    if world.inventoryKnown and n(world.inventoryRevision)>0 then
        for _,bag in ipairs(world.inventoryBags or {}) do
            if n(bag.bag)>0 and bag.contentsKnown then
                for _,item in ipairs(bag.slots or {}) do add(item,{inventoryRevision=id(world.inventoryRevision),sourceBag=n(bag.bag),sourceSlot=n(item.slot)}) end
            end
        end
    end
    local best
    for _,c in ipairs(choices) do
        if not best or c.rank>best.rank or c.rank==best.rank and c.count>best.count then best=c end
    end
    return best
end
local function result(memory,phase,action)
    memory.phase=phase;return {memory=memory,phase=phase,action=action}
end
local function advertised(frame,port,method)
    for _,row in ipairs((frame.catalogs or {})[id(port)] or {}) do
        if row.name==method and row.effect=='mutation' then return true end
    end
    return false
end
local function issue(m,port,method,params,phase)
    m.sequence=n(m.sequence)+1
    local action={id=tostring(m.sequence),port=id(port),method=method,params=params}
    m.pending.actionId=action.id
    return result(m,phase,action)
end

local function space_pressure(frame,mage,b)
    local out={};local all=ranks(mage,frame.metadata or {})
    local clients={{port=b.magePort,guid=b.mageGuid}}
    for _,peer in ipairs(b.recipients or {}) do clients[#clients+1]=peer end
    for _,binding in ipairs(clients) do
        local view=(frame.observations or {})[id(binding.port)]
        if view then
            local stock=count(view,binding.guid)
            local minimum=id(binding.guid)==id(b.mageGuid) and M.stock_minimum or M.peer_reorder_threshold
            if stock and stock<minimum then
                local room=false;local known=false
                for entry,rank in pairs(compatible(all,(view.world or {}).self or {})) do
                    known=true
                    if capacity(slots(view),entry,rank.meta,1)>0 then room=true end
                end
                out[id(binding.port)]=stock==0 or known and not room
            end
        end
    end
    return out
end

function M.plan(frame)
    local m=frame.memory or {};local now=n(frame.nowMs);local b=frame.bindings or {}
    local views=frame.observations or {};local mage=views[id(b.magePort)];local guid=id(b.mageGuid)
    if not mage or not current(mage,guid) then return result(m,'waiting_for_current_mage') end
    if frame.query=='space_pressure' then return {bagPressure=space_pressure(frame,mage,b)} end
    local p=m.pending;local last=frame.last or {}
    if p then
        local peer=p.recipientPort and views[p.recipientPort]
        if p.recipientSession and not peer then return result(m,'awaiting_recipient_observations') end
        if mage.actions.sessionId~=p.mageSession or p.recipientSession and (peer.actions or {}).sessionId~=p.recipientSession then
            m.pending=nil;return result(m,'previous_body_replaced')
        end
        if peer and not current(peer,p.recipientGuid) then return result(m,'awaiting_current_recipient_body') end
        if last.id==p.actionId and last.outcome=='not_dispatched' then
            m.pending=nil;return result(m,last.reason or 'supply_not_dispatched')
        end
        if p.kind=='conjure' then
            local receipt=(last.id==p.actionId and last.outcome=='rpc_result' and last.result or {}).lastSpellOperation or {}
            if last.id==p.actionId and last.outcome=='rpc_result' and receipt.capability=='create_item'
                and n(receipt.spellId)==p.spellId and receipt.phase=='not_dispatched' and receipt.transportWritten~=true then
                m.pending=nil;m.nextAtMs=now+M.poll_ms;return result(m,'creation_not_dispatched')
            end
            if receipt.capability=='create_item' and n(receipt.spellId)==p.spellId and receipt.transportWritten==true
                and n(receipt.createdItemEntry)==p.entry and receipt.baselineInventoryRevision
                and not after(p.inventoryRevision,receipt.baselineInventoryRevision) then
                p.operationBaseline=id(receipt.baselineInventoryRevision)
                if n(receipt.operationRevision)>0 then p.operationRevision=id(receipt.operationRevision) end
            end
            local op=mage.actions.lastSpellOperation or {}
            local same=p.operationBaseline and id(op.baselineInventoryRevision)==p.operationBaseline
                and (not p.operationRevision or id(op.operationRevision)==p.operationRevision)
                and n(op.spellId)==p.spellId and op.capability=='create_item' and n(op.createdItemEntry)==p.entry
            if same and op.phase=='server_rejected_no_replay' then
                m.pending=nil;m.nextAtMs=now+M.poll_ms;return result(m,'creation_server_rejected')
            end
            if n(op.spellId)==p.spellId and op.capability=='create_item' and op.phase=='confirmed_item'
                and p.operationBaseline and id(op.baselineInventoryRevision)==p.operationBaseline
                and (not p.operationRevision or id(op.operationRevision)==p.operationRevision)
                and n(op.createdItemEntry)==p.entry and after(op.observedInventoryRevision,p.operationBaseline)
                and op.serverCastGo==true and op.effectObserved==true and op.inventoryConfirmed==true and op.itemPushObserved==true then
                m.pending=nil;m.nextAtMs=now+M.poll_ms;return result(m,'observed_created_water')
            end
            local uncertain=last.id==p.actionId and last.outcome=='transport_unconfirmed'
                or op.phase=='transport_unconfirmed_no_retry' or op.phase=='outcome_unconfirmed_no_retry'
            return result(m,uncertain and 'conjure_outcome_unconfirmed' or 'conjure_pending')
        end
        if p.stage=='receiver' then
            if last.id~=p.actionId then return result(m,'awaiting_recipient_reservation') end
            if last.outcome~='rpc_result' then return result(m,'recipient_reservation_unconfirmed') end
            local receipt=last.result or {}
            if receipt.sessionId~=p.recipientSession or receipt.waterGiftArmed~=true or id(receipt.partnerGuid)~=guid
                or not after(receipt.revision,p.recipientRevision) then return result(m,'recipient_reservation_unconfirmed') end
            if peer.trade.waterGiftArmed~=true or id(peer.trade.partnerGuid)~=guid or after(receipt.revision,peer.trade.revision) then
                return result(m,'awaiting_recipient_reservation_observation')
            end
            if not body(mage.world.self,guid) or not body(peer.world.self,p.recipientGuid)
                or not near(position(mage),nearby(mage,p.recipientGuid)) or not near(position(peer),nearby(peer,guid))
                or (mage.actions or {}).castPending or (mage.actions or {}).moving then
                return result(m,'awaiting_safe_trade_continuation')
            end
            p.stage='offered'
            local params={sessionId=p.mageSession,revision=id(mage.trade.revision),partnerGuid=p.recipientGuid,
                itemEntry=p.entry,itemCount=p.count,expectedItemGuid=p.itemGuid}
            for key,value in pairs(p.source) do params[key]=value end
            return issue(m,b.magePort,'buddy-trade.water_gift',params,'water_gift_offer_requested')
        end
        local a,c=mage.trade,peer.trade
        if a.phase=='COMPLETE' and c.phase=='COMPLETE' and id(a.partnerGuid)==p.recipientGuid
            and id(c.partnerGuid)==guid and n(a.lastServerStatus)==8 and n(c.lastServerStatus)==8
            and after(a.completedTrades,p.mageCompleted) and after(c.completedTrades,p.recipientCompleted) then
            -- Completion belongs to the actual native transaction. Current stock
            -- is separate: drinking/loot/conjuring must not freeze this lifecycle.
            if m.request and m.request.id==p.requestId then
                m.request.remaining[p.recipientGuid]=nil
                if not next(m.request.remaining) then m.completedRequestId=m.request.id;m.request=nil end
            end
            m.pending=nil;m.nextAtMs=now+M.poll_ms;return result(m,'server_confirmed_peer_trade')
        end
        return result(m,last.id==p.actionId and last.outcome=='transport_unconfirmed' and 'water_gift_outcome_unconfirmed' or 'awaiting_peer_trade')
    end
    local request=frame.request
    if type(request)=='table' and type(request.id)=='string' and #request.id>0 and #request.id<=128
        and request.id~=m.completedRequestId and (not m.request or request.id~=m.request.id) then
        if request.sessionId~=mage.actions.sessionId or id(request.groupGuid)~=group(mage,guid,b.ownerGuid) then
            return result(m,'requested_supply_body_or_party_changed')
        end
        local remaining={}
        for _,peer in ipairs(b.recipients or {}) do
            for _,wanted in ipairs(request.recipientGuids or {}) do
                if id(wanted)=='0' or id(wanted)==id(peer.guid) then remaining[id(peer.guid)]=true end
            end
        end
        if not next(remaining) then return result(m,'requested_supply_recipient_unbound') end
        m.request={id=request.id,sessionId=request.sessionId,groupGuid=id(request.groupGuid),remaining=remaining}
        m.nextAtMs=now
    end
    if m.request and (m.request.sessionId~=mage.actions.sessionId or m.request.groupGuid~=group(mage,guid,b.ownerGuid)) then
        m.request=nil;return result(m,'requested_supply_body_or_party_changed')
    end
    if now<n(m.nextAtMs) then return result(m,m.phase or 'waiting_for_supply_tick') end
    m.nextAtMs=now+M.poll_ms
    if not free(mage,guid) then return result(m,'mage_busy') end
    local group_id=group(mage,guid,b.ownerGuid);local world=mage.world or {}
    if not group_id or not body(world.self,guid) then return result(m,'waiting_for_live_mage_and_party') end
    if (world.owner or {}).present and not body(world.owner,b.ownerGuid) then return result(m,'waiting_for_party_combat_to_end') end
    local stock=count(mage,guid);if stock==nil then return result(m,'waiting_for_mage_water_count') end
    local peers={}
    for _,binding in ipairs(b.recipients or {}) do
        local v=views[id(binding.port)];local recipient=id(binding.guid)
        if v and free(v,recipient) and group(v,recipient,b.ownerGuid)==group_id and body(v.world.self,recipient)
            and near(position(mage),nearby(mage,recipient)) and near(position(v),nearby(v,guid)) then
            local amount=count(v,recipient);local player=v.world.self
            local wanted=m.request and m.request.remaining[recipient]
            if amount~=nil and (wanted or not m.request and amount<M.peer_reorder_threshold) and player.levelKnown and player.powerKnown
                and n(player.powerType)==0 and n(player.maxPower)>0 then
                peers[#peers+1]={view=v,port=id(binding.port),guid=recipient,count=amount,priority=n(M.recipient_priority[recipient])}
            end
        end
    end
    if #peers==0 and m.request then return result(m,'awaiting_requested_water_recipient') end
    if #peers==0 and stock>=M.stock_minimum then return result(m,'water_stock_sufficient') end
    local all=ranks(mage,frame.metadata or {});local usable={};local needs_space=false
    local chosen={}
    for _=1,#peers do
        local peer,index
        for i,row in ipairs(peers) do
            if not chosen[i] and (not peer or row.priority>peer.priority or row.priority==peer.priority and
                (row.count<peer.count or row.count==peer.count and row.port<peer.port)) then peer,index=row,i end
        end
        chosen[index]=true
        local available=compatible(all,peer.view.world.self);local receiving=slots(peer.view)
        local spare=peer.count==0 and stock or stock-M.stock_minimum
        local stack=carried(mage,available,spare,receiving)
        if stack then
            if not advertised(frame,b.magePort,'buddy-trade.water_gift') or not advertised(frame,peer.port,'buddy-trade.water_gift') then
                return result(m,'water_gift_capability_unavailable')
            end
            m.pending={kind='gift',stage='receiver',mageSession=mage.actions.sessionId,recipientSession=peer.view.actions.sessionId,
                recipientPort=peer.port,recipientGuid=peer.guid,itemGuid=id(stack.item.itemGuid),entry=stack.entry,count=stack.count,
                source=stack.source,mageCompleted=id(mage.trade.completedTrades or '0'),recipientCompleted=id(peer.view.trade.completedTrades or '0'),
                recipientRevision=id(peer.view.trade.revision),requestId=m.request and m.request.id}
            return issue(m,peer.port,'buddy-trade.water_gift',{sessionId=peer.view.trade.sessionId,
                revision=id(peer.view.trade.revision),partnerGuid=guid,itemEntry=stack.entry,itemCount=stack.count},'recipient_water_reservation_requested')
        end
        local empty=false
        for _,slot in ipairs(receiving or {}) do if n(slot.itemGuid)==0 then empty=true end end
        if empty and next(available) then usable=available;break end
        if next(available) then needs_space=true end
    end
    if not next(usable) and stock<M.stock_minimum then usable=compatible(all,world.self or {}) end
    if not next(usable) then return result(m,needs_space and 'recipient_needs_bag_space' or 'no_compatible_conjure_water_rank') end
    local op=mage.actions.lastSpellOperation or {}
    if op.capability=='create_item' then
        if op.phase=='transport_unconfirmed_no_retry' or op.phase=='outcome_unconfirmed_no_retry' then return result(m,'conjure_outcome_unconfirmed') end
        if op.phase=='waiting_server' or op.phase=='waiting_created_item' then return result(m,'conjure_pending') end
    end
    local best;local own_slots=slots(mage);local room=false
    for entry,rank in pairs(usable) do
        if capacity(own_slots,entry,rank.meta,1)>0 then
            room=true
            if affordable(rank,world.self) and (not best or rank.rank>best.rank or rank.rank==best.rank and rank.spellId>best.spellId) then best=rank end
        end
    end
    if not best then return result(m,room and 'waiting_for_mana_to_conjure' or 'mage_needs_bag_space') end
    if not advertised(frame,b.magePort,'buddy-actions.create_item') then return result(m,'create_item_capability_unavailable') end
    if world.inventoryKnown~=true or n(world.inventoryRevision)<=0 then return result(m,'waiting_for_complete_creation_inventory') end
    local entry
    for key,value in pairs(usable) do if value==best then entry=key end end
    m.pending={kind='conjure',mageSession=mage.actions.sessionId,spellId=best.spellId,entry=entry,inventoryRevision=id(world.inventoryRevision)}
    return issue(m,b.magePort,'buddy-actions.create_item',{sessionId=mage.actions.sessionId,selfGuid=guid,spellId=best.spellId},'conjure_requested')
end

return M
