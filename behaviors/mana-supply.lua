-- Shared party supply policy. The host only gathers facts, evaluates this
-- source and reserves/dispatches its exact RPC proposal. It never chooses stock,
-- a caster, rank, recipient, quantity, trade response or retry time.
local M={}
local defaults={family_id=5504,own_reserve=5,peer_reorder=12,lease_ms=5000,renew_below_ms=2000,retry_ms=4000,range_yards=10,recent_attack_ms=5000}
local function n(v) return tonumber(v) or 0 end
local function list(v) return type(v)=="table" and v or {} end
local function guid(v) return type(v)=="string" and v~="" and v~="0" end
local function terms(entry,count) return {{slot=0,entry=entry,count=count}} end
local function same(a,b)
 a,b=list(a),list(b);if #a~=#b then return false end
 for _,i in ipairs(a) do local found=false;for _,j in ipairs(b) do if n(i.slot)==n(j.slot) and i.entry==j.entry and i.count==j.count then found=true end end;if not found then return false end end
 return true
end
local function own(v) return (v.world or {}).self or {} end
local function sid(v) return (v.actions or {}).sessionId end
local function coherent(v)
 local p,a,i,t,g=own(v),v.actions or {},v.inventory or {},v.trade or {},v.party or {}
 return p.present==true and p.aliveKnown==true and p.alive==true and p.unitFlagsKnown==true and n(p.unitFlags)%1048576<524288 and guid(p.guid) and a.selfGuid==p.guid and guid(a.sessionId) and i.sessionId==a.sessionId and i.selfGuid==p.guid and i.inventoryKnown==true and t.sessionId==a.sessionId and g.sessionId==a.sessionId
end
local function group(v)
 local g=(v.party or {}).group or {};if g.known~=true or g.inGroup~=true or not guid(g.groupGuid) then return end
 local found=false;for _,p in ipairs(g.members or {}) do if p.guid==own(v).guid and p.online==true then found=true end end
 if found then return g.groupGuid end
end
local function near(a,b,range)
 local p=(a.actions or {}).position or {};local q
 for _,v in ipairs((a.world or {}).nearbyPlayers or {}) do if v.guid==own(b).guid then q=v end end
 if not q or q.present~=true or q.positionKnown~=true or q.mapKnown~=true or p.known~=true or p.mapKnown~=true or n(p.mapId)~=n(q.mapId) then return false end
 return (n(p.x)-n(q.x))^2+(n(p.y)-n(q.y))^2+(n(p.z)-n(q.z))^2<=range^2
end
local function quiet(views,c)
 local bodies={}
 for _,v in pairs(views) do if coherent(v) then bodies[own(v).guid]=true end end
 for _,v in pairs(views) do
  if not coherent(v) then return false end
  local owner=(v.world or {}).owner or {}
  if owner.present==true and owner.unitFlagsKnown==true and n(owner.unitFlags)%1048576>=524288 then return false end
  for _,e in ipairs((v.world or {}).nearbyCreatures or {}) do if e.present==true and e.alive==true and e.attackVictimKnown==true and bodies[e.attackVictimGuid] and n(e.attackAgeMs)<=c.recent_attack_ms then return false end end
 end
 return true
end
local function ready(v)
 local a,t=v.actions or {},v.trade or {}
 local lease=a.movementOwner or {}
 return coherent(v) and not (a.moving or a.motionUnconfirmed or a.traveling or a.castPending or (a.interactionOwner or "")~="" or a.autoAttacking or a.autoAttackRequested or a.autoAttackUnconfirmed) and lease.active~=true and t.waterGiftArmed~=true and not t.exactTransfer and (t.phase=="IDLE" or t.phase=="COMPLETE" or t.phase=="CLOSED")
end
local function slots(v)
 local out={}
 for _,b in ipairs((v.inventory or {}).inventoryBags or {}) do if b.contentsKnown==true and n(b.slotCount)==#list(b.slots) then for _,s in ipairs(b.slots or {}) do out[#out+1]=s end end end
 return out
end
local function max_stack(info)
 -- Raw Stackable is signed in the client protocol. Unknown, zero, negative
 -- and sentinel layouts are conservatively unsupported here, never invented20.
 local raw=(info.rawTemplateFacts or {}).maxStackCount
 if type(raw)=="number" and raw>0 and raw<2147483647 and raw==math.floor(raw) then return raw end
 return 0
end
local function capacity(v,entry,maxstack)
 local count=0
 for _,b in ipairs((v.inventory or {}).inventoryBags or {}) do
  local empty_allowed=n(b.bag)==0
  for _,i in ipairs((v.inventory or {}).items or {}) do if (i.item or {}).entry==b.entry and i.normalBag==true and n(i.containerSlots)==n(b.slotCount) then empty_allowed=true end end
  if b.contentsKnown==true and n(b.slotCount)==#list(b.slots) then for _,s in ipairs(b.slots or {}) do
   if not guid(s.itemGuid) then if empty_allowed then count=count+maxstack end
   elseif s.metadataKnown==true and s.entry==entry then count=count+math.max(0,maxstack-n(s.stackCount)) end
  end end
 end
 return count
end
local function count(v,entry)
 local total=0;for _,s in ipairs(slots(v)) do if s.entry==entry then total=total+n(s.stackCount) end end;return total
end
local function newer(a,b) return type(a)=="string" and type(b)=="string" and (#a>#b or #a==#b and a>b) end
local function source(v,s)
 return {bag=n(s.bag),slot=n(s.slot),itemGuid=s.itemGuid,entry=s.entry,count=s.stackCount,inventoryRevision=v.inventory.inventoryRevision}
end
local function output(m,phase,a) return {memory=m,phase=phase,action=a} end
local function proposal(m,key,method,params,kind)
 m.serial=n(m.serial)+1
 local id="supply:"..tostring(m.serial)
 m.pending={id=id,key=key,method=method,params=params,kind=kind}
 return {id=id,binding=key,method=method,params=params}
end
local function transfer(m,views,c,side,mode)
 local p=m.trade;local key=p[side];local v=views[key];local ownterms=side=="donor" and p.items or {};local peerterms=side=="recipient" and p.items or {}
 local params={sessionId=sid(v),selfGuid=own(v).guid,revision=v.trade.revision or "0",transactionId=p[side.."Transaction"] or p.id,requestId=(p[side.."Transaction"] or p.id)..":"..tostring(n(m.serial)+1),partnerGuid=own(views[p[side=="donor" and "recipient" or "donor"]]).guid,mode=mode,leaseMs=c.lease_ms,source=side=="donor" and p.source or nil}
 if #ownterms>0 then params.ownItems=ownterms end;if #peerterms>0 then params.partnerItems=peerterms end
 return proposal(m,key,"buddy-trade.transfer_step",params,"trade")
end
local function outcome(m,views,last,now,c)
 local p=m.pending;if not p then return end
 if not last or last.id~=p.id then return "reserved_input_unresolved" end
 -- A host reservation can survive a process crash before the send. Do not
 -- guess whether it ran. Native per-step receipts/current facts settle it.
 local v=views[p.key]
 if p.kind=="lookup" then
  if last.result and v and last.result.sessionId==sid(v) then
   m.items=m.items or {};for _,i in ipairs(last.result.items or {}) do if i.available==true and i.rawTemplateFacts and i.rawTemplateFacts.maxStackCount~=nil then m.items[tostring(i.itemId)]=i end end
  end
  m.pending=nil;m.retryAt=now+c.retry_ms;return
 end
 if p.kind=="trade" then
  local r=v and (v.trade or {}).lastTransferStep
  if r and r.requestId==p.params.requestId then m.pending=nil;return end
  if last.outcome=="not_dispatched" then m.pending=nil;m.retryAt=now+c.retry_ms;if m.trade then m.trade.failed=true end;return end
  p.expires=p.expires or now+c.lease_ms
  if v and not (v.trade or {}).exactTransfer and now>=p.expires then m.pending=nil;if m.trade then m.trade.failed=true end;return end
  return "trade_input_unconfirmed"
 end
 if p.kind=="conjure" then
  if not v or sid(v)~=p.params.sessionId then m.pending=nil;return "body_changed" end
  local op=(v.actions or {}).lastSpellOperation or {}
  if newer((v.inventory or {}).inventoryRevision,p.baselineInventoryRevision) and count(v,p.entry)>n(p.beforeCount) then m.pending=nil;return end
  if op.capability=="create_item" and op.spellId==p.params.spellId and op.baselineInventoryRevision==p.baselineInventoryRevision then
   if op.effectObserved==true and op.inventoryConfirmed==true and op.itemPushObserved==true then m.pending=nil;return end
   if op.phase=="server_rejected" or op.phase=="cast_failed" or op.phase=="cast_interrupted" then m.pending=nil;m.retryAt=now+c.retry_ms;return end
  end
  if last.outcome=="not_dispatched" then m.pending=nil;m.retryAt=now+c.retry_ms;return end
  return "conjure_effect_unconfirmed"
 end
end
function M.decide(frame)
 local m=frame.memory or {};local views=frame.observations or {};local c={}
 for k,v in pairs(defaults) do c[k]=(frame.config or {})[k] or v end
 local now=n(frame.now)
 local pending=outcome(m,views,frame.last,now,c)
 if pending then return output(m,pending) end
 local p=m.trade
 if p then
  local d,r=views[p.donor],views[p.recipient]
  if not d or not r or sid(d)~=p.donorSession or sid(r)~=p.recipientSession then m.trade=nil;return output(m,"trade_body_changed") end
  local dt,rt=d.trade or {},r.trade or {}
  if dt.exactTransfer and dt.exactTransfer.transactionId~=(p.donorTransaction or p.id) or rt.exactTransfer and rt.exactTransfer.transactionId~=(p.recipientTransaction or p.id) then m.trade=nil;return output(m,"transfer_owner_replaced") end
  if dt.phase=="COMPLETE" and rt.phase=="COMPLETE" and n(dt.completedTrades)>p.donorCompleted and n(rt.completedTrades)>p.recipientCompleted and (dt.lastTransferStep or {}).transactionId==(p.donorTransaction or p.id) and (rt.lastTransferStep or {}).transactionId==(p.recipientTransaction or p.id) then
   -- Server status8 is the normal committed transaction receipt. Current
   -- inventory is reported separately: drinking/conjuring/loot may already
   -- change either total after custody releases, so conservation is not a lock.
   m.observedStock={entry=p.source.entry,donor=count(d,p.source.entry),recipient=count(r,p.source.entry),donorInventoryRevision=d.inventory.inventoryRevision,recipientInventoryRevision=r.inventory.inventoryRevision}
   m.trade=nil;return output(m,"server_confirmed_supply_trade")
  end
  if p.failed or not quiet(views,c) or dt.phase=="CLOSED" or rt.phase=="CLOSED" or frame.cancelled then
   if dt.exactTransfer then return output(m,"cancelling_donor",transfer(m,views,c,"donor","cancel")) end
   if rt.exactTransfer then return output(m,"cancelling_recipient",transfer(m,views,c,"recipient","cancel")) end
   m.trade=nil;return output(m,"supply_interrupted")
  end
  if not rt.exactTransfer and not p.recipientReserved then p.recipientReserved=true;return output(m,"reserve_recipient",transfer(m,views,c,"recipient","reserve")) end
  if not dt.exactTransfer and not p.donorOpened then p.donorOpened=true;return output(m,"open_exact_trade",transfer(m,views,c,"donor","open")) end
  -- An expired/ambiguous transaction is retired, never blindly reopened.
  if not rt.exactTransfer or not dt.exactTransfer then
   if dt.exactTransfer then return output(m,"expired_recipient_cancel",transfer(m,views,c,"donor","cancel")) end
   if rt.exactTransfer then return output(m,"expired_donor_cancel",transfer(m,views,c,"recipient","cancel")) end
   m.trade=nil;m.retryAt=now+c.retry_ms;return output(m,"trade_expired")
  end
  if rt.phase=="INVITED" then return output(m,"begin_exact_trade",transfer(m,views,c,"recipient","begin")) end
  if dt.phase=="OPEN" and #list(dt.ownRequestedItems)==0 then return output(m,"offer_exact_stack",transfer(m,views,c,"donor","offer")) end
  -- Both normal client views must agree on the exact gift. An ACK from
  -- offer/accept cannot substitute for the recipient's actual peer view.
  if dt.phase=="OPEN" and rt.phase=="OPEN" and same(dt.ownRequestedItems,p.items) and same(rt.partnerItems,p.items) and #list(dt.partnerItems)==0 and #list(rt.ownRequestedItems)==0 and (dt.exactTransfer or {}).partnerViewKnown==true and (rt.exactTransfer or {}).partnerViewKnown==true then
   return output(m,"accept_exact_donor",transfer(m,views,c,"donor","accept"))
  end
  if dt.phase=="ACCEPT_SENT" and rt.phase=="OPEN" and same(rt.partnerItems,p.items) then return output(m,"accept_exact_recipient",transfer(m,views,c,"recipient","accept")) end
  for _,side in ipairs({"donor","recipient"}) do local t=side=="donor" and dt or rt;if t.exactTransfer and n(t.exactTransfer.leaseRemainingMs)<=c.renew_below_ms then return output(m,"renew_exact_trade",transfer(m,views,c,side,"renew")) end end
  return output(m,"awaiting_server_trade_facts")
 end
 -- Explicit legacy gift terms use this same policy. Adopt only mutually
 -- matching normal-client reservations; no named-peer or item-ID shortcut.
 for key,v in pairs(views) do local x=(v.trade or {}).exactTransfer
  if x and x.source and #list(x.ownItems)==1 then
   for peer,w in pairs(views) do local y=(w.trade or {}).exactTransfer
    if key~=peer and y and x.partnerGuid==own(w).guid and y.partnerGuid==own(v).guid and same(x.ownItems,y.partnerItems) and #list(x.partnerItems)==0 and #list(y.ownItems)==0 then
     m.trade={id=x.transactionId,donorTransaction=x.transactionId,recipientTransaction=y.transactionId,donor=key,recipient=peer,donorSession=sid(v),recipientSession=sid(w),source=x.source,items=x.ownItems,donorCompleted=n(v.trade.completedTrades),recipientCompleted=n(w.trade.completedTrades),donorInventoryRevision=v.inventory.inventoryRevision,recipientInventoryRevision=w.inventory.inventoryRevision,donorCount=count(v,x.source.entry),recipientCount=count(w,x.source.entry),donorOpened=true,recipientReserved=true}
     return output(m,"adopt_explicit_transfer_terms")
    end
   end
  end
 end
 if frame.cancelled then return output(m,"cancelled") end
 if now<n(m.retryAt) then return output(m,"retry_wait") end
 if not quiet(views,c) then return output(m,"waiting_for_safe_party") end
 -- Discover a producer from the actual learned catalog, not a named character.
 for key,v in pairs(views) do if ready(v) and group(v) and (v.spell_toolkit or {}).spellbookKnown==true and v.spell_toolkit.sessionId==sid(v) and v.inventory.manaDrinkCount~=nil then
  local ranks={};local missing={}
  for _,s in ipairs(v.spell_toolkit.spells or {}) do if s.capability=="create_item" and s.familyId==c.family_id and s.createdItemEntry and (s.blockedReason==nil or s.blockedReason=="") then
   local info=(m.items or {})[tostring(s.createdItemEntry)];if info then ranks[#ranks+1]={spell=s,info=info} else missing[#missing+1]=s.createdItemEntry end
  end end
  if #missing>0 then local ids={};for i=1,math.min(6,#missing) do ids[i]=missing[i] end;return output(m,"query_created_item_facts",proposal(m,key,"buddy-trade.lookup_items",{itemIds=ids},"lookup")) end
  local peers={}
  for peer,w in pairs(views) do local self=own(w)
   if peer~=key and ready(w) and group(w)==group(v) and self.powerKnown==true and n(self.powerType)==0 and n(self.maxPower)>0 and self.levelKnown==true and w.inventory.manaDrinkCount~=nil and n(w.inventory.manaDrinkCount)<c.peer_reorder and near(v,w,c.range_yards) and near(w,v,c.range_yards) then peers[#peers+1]={key=peer,view=w,count=n(w.inventory.manaDrinkCount)} end
  end
  -- Avoid table.sort (the restricted evaluator deliberately omits it).
  for i=1,#peers do for j=i+1,#peers do if peers[j].count<peers[i].count or peers[j].count==peers[i].count and own(peers[j].view).guid<own(peers[i].view).guid then peers[i],peers[j]=peers[j],peers[i] end end end
  local bestConjure
  for _,peer in ipairs(peers) do
   local spare=n(v.inventory.manaDrinkCount)-(peer.count==0 and 0 or c.own_reserve)
   local best
   for _,rank in ipairs(ranks) do if n(rank.info.requiredLevel)<=n(own(peer.view).level) and capacity(peer.view,rank.spell.createdItemEntry,max_stack(rank.info))>0 then
    for _,s in ipairs(slots(v)) do if s.metadataKnown==true and s.entry==rank.spell.createdItemEntry and guid(s.itemGuid) and n(s.stackCount)>0 and n(s.stackCount)<=spare and n(s.stackCount)<=capacity(peer.view,s.entry,max_stack(rank.info)) then
     if not best or n(rank.spell.rank)>n(best.rank) or rank.spell.rank==best.rank and n(s.stackCount)>n(best.stack.stackCount) then best={rank=rank.spell.rank,stack=s} end
    end end
    if not bestConjure or n(rank.spell.rank)>n(bestConjure.spell.rank) then bestConjure=rank end
   end end
   if best then
    local tx="supply:"..sid(v)..":"..tostring(n(m.serial)+1)
    m.trade={id=tx,donor=key,recipient=peer.key,donorSession=sid(v),recipientSession=sid(peer.view),source=source(v,best.stack),items=terms(best.stack.entry,best.stack.stackCount),donorCompleted=n(v.trade.completedTrades),recipientCompleted=n(peer.view.trade.completedTrades),donorInventoryRevision=v.inventory.inventoryRevision,recipientInventoryRevision=peer.view.inventory.inventoryRevision,donorCount=count(v,best.stack.entry),recipientCount=count(peer.view,best.stack.entry)}
    return output(m,"supply_selected_exact_stack")
   end
  end
  if n(v.inventory.manaDrinkCount)<c.own_reserve then for _,rank in ipairs(ranks) do if own(v).levelKnown==true and n(rank.info.requiredLevel)<=n(own(v).level) and (not bestConjure or n(rank.spell.rank)>n(bestConjure.spell.rank)) then bestConjure=rank end end end
  if bestConjure and capacity(v,bestConjure.spell.createdItemEntry,max_stack(bestConjure.info))>0 and n(own(v).power)>0 then
   local op=(v.actions or {}).lastSpellOperation or {}
   if op.capability=="create_item" and (op.phase=="waiting_server" or op.phase=="waiting_created_item" or op.phase=="transport_unconfirmed_no_retry") then return output(m,"existing_conjure_pending") end
   local a=proposal(m,key,"buddy-actions.create_item",{sessionId=sid(v),selfGuid=own(v).guid,spellId=bestConjure.spell.spellId},"conjure");m.pending.baselineInventoryRevision=v.inventory.inventoryRevision;m.pending.entry=bestConjure.spell.createdItemEntry;m.pending.beforeCount=count(v,bestConjure.spell.createdItemEntry);return output(m,"conjure_selected_rank",a)
  end
 end end
 return output(m,"supply_sufficient_or_unavailable")
end
return M
