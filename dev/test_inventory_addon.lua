-- Run with Lua5.1 against the installed addon source.
local source=assert(arg[1])
dofile('dev/test_quest_addon.lua')
local I=assert(WarcraftBuddyInventory)
local now,sent=100,{}
function GetTime() return now end
local names={player='Joel',party1='Arlen',party2='Mira',party3='Rook'}
local guids={player='0x0000000000000003',party1='0x0000000000000001',party2='0x0000000000000004',party3='0x0000000000000005'}
function UnitName(unit) return names[unit] end
function UnitGUID(unit) return guids[unit] end
function GetNumPartyMembers() return 3 end
function SendAddonMessage(prefix,payload,channel)
 assert(prefix=='WBINV1' and channel=='PARTY' and #payload<=248)
 sent[#sent+1]=payload
end
local sid='abcdef123456'
local bags='B=0,16,0,1;B=1,20,41599,1;B=2,0,0,1;B=3,0,0,1;B=4,0,0,1'
local item='I=1,0,32971,1,4000000000000100'
local function receive(body,part,parts,agent,nonce,revision,snapshot)
 local packet=table.concat({'1','S',nonce or I.nonce,snapshot or sid,revision or '19',part or 1,parts or 1,body},'|')
 return I.Receive('WBINV1',packet,'PARTY',agent or 'Arlen')
end
assert(I.Refresh())
assert(sent[#sent]:match('^1|R|%x+$'))
assert(not receive(bags,1,1,'Stranger'))
assert(not receive(bags,1,1,'Arlen','000000000000'))
guids.party1='0x0000000000000099'
assert(not receive(bags))
guids.party1='0x0000000000000001'
assert(receive(item,2,2))
assert(I.views.Arlen==nil,'partial snapshot must not imply empty slots')
assert(receive(bags,1,2))
local v=assert(I.views.Arlen)
assert(v.bags[1].count==20 and v.bags[1].slots[0].entry==32971 and v.bags[1].slots[1]==nil)
assert(v.bags[0].count==16 and v.revision=='19')
assert(not receive(bags,1,1,'Arlen',nil,nil,'111111111111'),'different snapshot cannot replace a coherent one')
I.selected={agent='Arlen',bag=1,slot=0,guid='4000000000000100'}
assert(I.Command('M',0,7))
assert(sent[#sent]:match('^1|M|%x+|abcdef123456|19|1|0|4000000000000100|0|7$'))
local pending=assert(I.pending)
assert(not I.Command('G'),'pending action cannot issue another command')
assert(not I.Receive('WBINV1','1|A|'..pending.id..'|'..sid..'|CONFIRMED|ok','PARTY','Mira'))
assert(I.Receive('WBINV1','1|A|'..pending.id..'|'..sid..'|QUEUED|waiting','PARTY','Arlen'))
assert(I.pending)
assert(I.Receive('WBINV1','1|A|'..pending.id..'|'..sid..'|CONFIRMED|verified','PARTY','Arlen'))
assert(not I.pending and I.status:find('CONFIRMED',1,true))
local freshNonce=I.nonce
assert(freshNonce~=pending.id)
assert(receive(bags..';'..item))
I.selected={agent='Arlen',bag=1,slot=0,guid='4000000000000100'}
assert(not I.Command('M',1,0),'must not overwrite an occupied slot')
assert(not I.Command('E',1),'must not replace an occupied equipped bag')
assert(I.Command('G'))
pending=I.pending
now=now+191;I.Tick()
assert(not I.pending and I.status:find('unconfirmed',1,true))
assert(not I.Command('G'),'stale snapshots must not authorize actions')
assert(I.Refresh())
assert(receive(bags..';'..item))
I.selected={agent='Arlen',bag=1,slot=0,guid='4000000000000100'}
assert(I.Command('G'));pending=I.pending
now=now+35;I.Tick();assert(I.pending,'normal long backend action stays pending')
assert(I.Receive('WBINV1','1|A|'..pending.id..'|'..sid..'|QUEUED|item_offered_review_normal_trade_window','PARTY','Arlen'))
assert(not I.pending and I.status:find('item offered',1,true) and not I.status:find('CONFIRMED',1,true))
assert(I.Refresh())
assert(not receive(bags..';'..item..';I=0,1,159,1,4000000000000100'),'duplicate ownedGUID cannot be actionable')
assert(I.views.Arlen.at<now,'malformed snapshot must not replace last good inventory')
assert(I.Refresh())
assert(not receive('B=0,16,0,1;B=1,20,41599,1'),'all five bags are required')
assert(I.Refresh())
assert(not receive(bags..';I=1,20,159,1,4000000000000999'),'out of range slot rejected')
assert(I.Refresh())
assert(not receive(bags..';'..item,1,1,'Arlen',nil,'18446744073709551616'),'uint64 revision overflow rejected')
assert(I.Refresh())
assert(receive('B=0,16,0,1;B=1,0,0,0;B=2,0,0,1;B=3,0,0,1;B=4,0,0,1'))
assert(I.views.Arlen.bags[1].known==false,'unknown occupied bag never becomes free capacity')
assert(I.Refresh());now=now+31
assert(not receive(bags),'expired response rejected')
print('Companion inventory protocol and action checks passed')

-- Disenchant is an explicit Mira-only, exact selected-GUID, two-click action.
I.agent='Mira'
assert(I.Refresh())
assert(receive(bags..';'..item,1,1,'Mira'))
I.selected={agent='Mira',bag=1,slot=0,guid='4000000000000100'}
local before=#sent
assert(I.Command('D') and I.confirmDisenchant)
assert(#sent==before,'arming destruction confirmation must not send an action')
assert(I.Command('D'))
assert(sent[#sent]:match('^1|D|%x+|abcdef123456|19|1|0|4000000000000100$'))
pending=assert(I.pending)
assert(pending.kind=='D' and pending.agent=='Mira' and not I.confirmDisenchant)
assert(not I.Receive('WBINV1','1|A|'..pending.id..'|'..sid..'|CONFIRMED|ok','PARTY','Arlen'))
assert(not I.Receive('WBINV1','1|A|'..pending.id..'|ffffffffffff|CONFIRMED|ok','PARTY','Mira'))
assert(I.Receive('WBINV1','1|A|'..pending.id..'|'..sid..'|PENDING|still_working','PARTY','Mira'))
assert(I.pending==pending and I.status:find('Keep connected',1,true))
for _,status in ipairs({'QUEUED','REQUESTED','CANCELLED','WRITE_UNCONFIRMED'}) do
 assert(I.Receive('WBINV1','1|A|'..pending.id..'|'..sid..'|'..status..'|await_receipt','PARTY','Mira'))
 assert(I.pending==pending and I.status:find('Keep connected',1,true),'nonterminal or unknown ACK must retain destructive ambiguity')
end
now=now+5;I.Tick()
assert(sent[#sent]=='1|P|'..pending.id..'|'..pending.snapshot,'poll must bind the original action nonce/snapshot')
before=#sent
for n=1,10 do I.Tick() end
assert(#sent==before,'receipt polls must be rate bounded')
assert(I.Receive('WBINV1','1|A|'..pending.id..'|'..sid..'|UNCONFIRMED|awaiting_evidence','PARTY','Mira'))
assert(I.pending==pending and I.status:find('Keep connected',1,true))
now=now+191;I.Tick()
assert(I.pending==pending and pending.timedOut,'timeout must not clear destructive ambiguity')
before=#sent
assert(not I.Command('D') and not I.Command('G') and not I.Refresh())
assert(#sent==before,'uncertainty must not send another mutation or a replacement request')
now=now+5;I.Tick()
assert(sent[#sent]=='1|P|'..pending.id..'|'..pending.snapshot)
assert(I.Receive('WBINV1','1|A|'..pending.id..'|'..sid..'|CONFIRMED|selected_item_materials_verified','PARTY','Mira'))
assert(not I.pending and not I.selected and sent[#sent]:match('^1|R|'),'confirmed receipt refreshes actual inventory')

-- A new selection/revision requires a new confirmation. No other companion can
-- invoke D, even by calling the Lua command entry point directly.
for _,agent in ipairs({'Arlen','Rook'}) do
 I.agent=agent;assert(I.Refresh());assert(receive(bags..';'..item,1,1,agent))
 I.selected={agent=agent,bag=1,slot=0,guid='4000000000000100'}
 before=#sent;assert(not I.Command('D') and #sent==before)
end
I.agent='Mira';assert(I.Refresh());assert(receive(bags..';'..item,1,1,'Mira'))
I.selected={agent='Mira',bag=1,slot=0,guid='4000000000000100'}
assert(I.Command('D'));before=#sent
I.views.Mira.revision='20'
assert(I.Command('D') and #sent==before and I.confirmDisenchant.revision=='20','revision change must rearm, not consume')
I.views.Mira.bags[1].slots[0].guid='4000000000000101'
I.selected.guid='4000000000000101'
assert(I.Command('D') and #sent==before and I.confirmDisenchant.guid=='4000000000000101','different GUID must rearm')
I.views.Mira.bags[1].slots[0].count=2
assert(not I.Command('D') and #sent==before,'DE accepts one explicitly selected item only')
assert(I.Refresh() and not I.confirmDisenchant)
assert(receive(bags..';'..item,1,1,'Mira'))
for _,terminal in ipairs({'BLOCKED','SERVER_REJECTED'}) do
 I.selected={agent='Mira',bag=1,slot=0,guid='4000000000000100'}
 assert(I.Command('D'));assert(I.Command('D'));pending=I.pending
 assert(I.Receive('WBINV1','1|A|'..pending.id..'|'..sid..'|'..terminal..'|definite_safe_terminal','PARTY','Mira'))
 assert(not I.pending,'authenticated definite safe rejection may clear the pending request')
 assert(receive(bags..';'..item,1,1,'Mira'))
end
assert(I.Refresh() and not I.confirmDisenchant)
print('Mira selected disenchant confirmation and read-only receipt polling checks passed')
