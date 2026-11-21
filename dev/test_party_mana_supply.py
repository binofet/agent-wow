import copy
import json
import os
from pathlib import Path
import tempfile
import unittest

import party_mana_supply as supply

BINARY = Path(os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY', supply.ROOT/'data/bin/luapolicy'))


def observation(guid, count, level=20):
    sid = 'aw-'+guid
    player = dict(guid=guid, present=True, aliveKnown=True, alive=True, unitFlagsKnown=True,
                  mapKnown=True, mapId=189, positionKnown=True, x=0, y=0, z=0,
                  levelKnown=True, level=level, powerKnown=True, power=100, maxPower=100)
    return dict(world=dict(self=player, owner=dict(player, guid='3'), nearbyPlayers=[]),
        actions=dict(sessionId=sid, selfGuid=guid, position=dict(known=True,mapKnown=True,mapId=189)),
        inventory=dict(sessionId=sid, selfGuid=guid, inventoryKnown=True, inventoryRevision='5',
                       manaDrinkCount=count, inventoryBags=[dict(contentsKnown=True,slotCount=2,slots=[{},{}])]),
        party=dict(sessionId=sid, group=dict(known=True,inGroup=True,groupGuid='77',members=[dict(guid=guid,online=True)])),
        trade=dict(sessionId=sid,revision='1',phase='IDLE'),
        spell_toolkit=dict(sessionId=sid,selfGuid=guid,spellbookKnown=True,spells=[]))


@unittest.skipUnless(BINARY.is_file(), 'real Lua evaluator required')
class SupplyLuaTests(unittest.TestCase):
    def setUp(self):
        self.bridge = supply.runtime.Bridge(BINARY, supply.ROOT/'modules')
        self.mage, self.priest = observation('44',18), observation('11',0)
        self.views = {'mage':self.mage,'priest':self.priest}
        self.mage['world']['nearbyPlayers'] = [self.priest['world']['self']]
        self.priest['world']['nearbyPlayers'] = [self.mage['world']['self']]
        self.mage['spell_toolkit']['spells'] = [dict(capability='create_item',familyId=5504,spellId=5506,rank=3,createdItemEntry=2136)]
        self.mage['inventory']['inventoryBags'][0]['slots'][0] = dict(bag=0,slot=0,itemGuid='4611686018427387911',entry=2136,stackCount=8,metadataKnown=True)
        self.memory = dict(items={'2136':dict(itemId=2136,available=True,requiredLevel=15,rawTemplateFacts=dict(maxStackCount=20))})
        self.last = None
        self.now = 100

    def step(self, **extra):
        out = self.bridge.evaluate(supply.POLICY.read_text(),dict(memory=self.memory,
            observations=self.views,last=self.last,pending=[],now=self.now,**extra))
        self.memory = out['memory']
        return out

    def acknowledge(self, action):
        view = self.views[action['binding']]
        params = action['params']
        trade = view['trade']
        trade['revision'] = str(int(trade['revision'])+1)
        if action['method'] == 'buddy-trade.transfer_step':
            mode = params['mode']
            trade['lastTransferStep'] = dict(transactionId=params['transactionId'],requestId=params['requestId'],transportWritten=mode not in ('reserve','renew'))
            if mode in ('reserve','open'):
                trade['exactTransfer'] = dict(transactionId=params['transactionId'],partnerGuid=params['partnerGuid'],
                    ownItems=params.get('ownItems',[]),partnerItems=params.get('partnerItems',[]),
                    source=params.get('source'),leaseRemainingMs='5000',partnerViewKnown=True)
                trade['phase'] = 'RESERVED' if mode=='reserve' else 'REQUESTED'
            elif mode=='begin':
                trade['phase'] = 'OPEN'
                self.mage['trade']['phase'] = 'OPEN'
                self.mage['trade']['exactTransfer']['partnerViewKnown'] = True
            elif mode=='offer':
                trade['ownRequestedItems'] = params['ownItems']
                self.priest['trade']['partnerItems'] = params['ownItems']
            elif mode=='accept':trade['phase']='ACCEPT_SENT'
            elif mode=='cancel':trade.pop('exactTransfer',None);trade['phase']='CANCEL_SENT'
        self.last = dict(id=action['id'],outcome='rpc_result',result=copy.deepcopy(trade))

    def test_actual_shared_lua_selects_other_character_ids_and_full_lifecycle(self):
        self.assertEqual(self.step()['phase'],'supply_selected_exact_stack')
        a=self.step()['action'];self.assertEqual((a['binding'],a['params']['mode']),('priest','reserve'));self.acknowledge(a)
        a=self.step()['action'];self.assertEqual((a['binding'],a['params']['mode']),('mage','open'));self.acknowledge(a)
        self.priest['trade']['phase']='INVITED'
        a=self.step()['action'];self.assertEqual(a['params']['mode'],'begin');self.acknowledge(a)
        a=self.step()['action'];self.assertEqual(a['params']['mode'],'offer');self.assertEqual(a['params']['source']['count'],8);self.acknowledge(a)
        a=self.step()['action'];self.assertEqual((a['binding'],a['params']['mode']),('mage','accept'));self.acknowledge(a)
        a=self.step()['action'];self.assertEqual((a['binding'],a['params']['mode']),('priest','accept'));self.acknowledge(a)
        self.assertEqual(self.step()['phase'],'awaiting_server_trade_facts')
        for v in self.views.values():v['trade'].update(phase='COMPLETE',completedTrades='1')
        self.assertEqual(self.step()['phase'],'server_confirmed_supply_trade')
        self.assertNotIn('trade',self.memory)


    def test_completed_transfer_survives_received_drink_consumption_and_unrelated_creation(self):
        self.step()
        plan=self.memory['trade']
        plan.update(recipientReserved=True,donorOpened=True)
        for v in self.views.values():v['trade'].update(phase='COMPLETE',completedTrades='1',lastTransferStep=dict(transactionId=plan['id']))
        self.mage['inventory']['inventoryBags'][0]['slots'][0]['stackCount']=12
        self.priest['inventory']['inventoryBags'][0]['slots'][0]=dict(entry=2136,stackCount=7,itemGuid='4611686018427387922',metadataKnown=True)
        self.assertEqual(self.step()['phase'],'server_confirmed_supply_trade')
        self.assertNotIn('trade',self.memory)
        self.assertEqual(self.memory['observedStock']['recipient'],7)

    def test_rank_and_quantity_tuning_changes_same_memory(self):
        self.priest['inventory']['manaDrinkCount']=1
        self.mage['inventory']['manaDrinkCount']=8
        # Reserve5 makes the whole8-stack unavailable; only Lua decides to conjure.
        a=self.step()['action'];self.assertEqual(a['method'],'buddy-actions.create_item');self.assertEqual(a['params']['spellId'],5506)
        self.memory.pop('pending');self.last=None
        out=self.step(config=dict(own_reserve=0))
        self.assertEqual(out['phase'],'supply_selected_exact_stack')
        self.assertEqual(out['memory']['trade']['source']['count'],8)

    def test_omitted_zero_fields_and_old_metadata_are_not_invented(self):
        self.memory={}
        a=self.step()['action'];self.assertEqual(a['method'],'buddy-trade.lookup_items')
        self.last=dict(id=a['id'],outcome='rpc_result',result=dict(sessionId='aw-44',items=[dict(itemId=2136,available=True,requiredLevel=0,rawTemplateFacts={})]))
        self.assertNotIn('action',self.step())
        self.assertNotIn('2136',self.memory.get('items',{}))
        self.now+=5000
        self.assertEqual(self.step()['action']['method'],'buddy-trade.lookup_items')

    def test_combat_and_other_rest_custody_leave_defense_available(self):
        self.priest['actions']['movementOwner']=dict(active=True,owner='consumable:test')
        self.assertNotIn('action',self.step())
        self.priest['actions'].pop('movementOwner')
        self.priest['world']['self']['unitFlags']=0x80000
        self.assertEqual(self.step()['phase'],'waiting_for_safe_party')
        self.assertNotIn('pending',self.memory)

    def test_cancel_and_replaced_body_do_not_reopen_old_intent(self):
        self.step();a=self.step()['action'];self.acknowledge(a)
        out=self.step(cancelled=True)
        self.assertEqual(out['action']['params']['mode'],'cancel')
        self.acknowledge(out['action']);self.step(cancelled=True)
        self.assertNotIn('trade',self.memory)
        self.assertNotIn('action',self.step(cancelled=True))

    def test_unknown_write_survives_export_reload_until_native_expiry(self):
        self.step();a=self.step()['action']
        self.last=dict(id=a['id'],outcome='unconfirmed',error='lost reply')
        self.memory=json.loads(json.dumps(self.memory))
        out=self.step();self.assertNotIn('action',out);self.assertEqual(out['phase'],'trade_input_unconfirmed')
        self.now+=6000
        out=self.step();self.assertNotIn('action',out)
        self.assertNotIn('trade',self.memory)

    def test_changed_peer_terms_never_trigger_acceptance(self):
        self.step();a=self.step()['action'];self.acknowledge(a)
        a=self.step()['action'];self.acknowledge(a);self.priest['trade']['phase']='INVITED'
        a=self.step()['action'];self.acknowledge(a)
        a=self.step()['action'];self.acknowledge(a)
        self.priest['trade']['partnerItems'][0]['count']=9
        self.assertNotIn('action',self.step())

    def test_host_persists_reservation_before_send_and_never_replays(self):
        state=dict(memory=copy.deepcopy(self.memory))
        saves=[];calls=[]
        def persist():saves.append(copy.deepcopy(state))
        def rpc(binding,method,params):
            calls.append((binding,method,params))
            if method=='buddy-actions.status':return self.views[binding]['actions']
            self.assertEqual(saves[-1]['last']['outcome'],'reserved')
            raise OSError('lost wire reply')
        supply.maintain_supply(rpc,self.views,state,bridge=self.bridge,persist=persist,now=100)
        supply.maintain_supply(rpc,self.views,state,bridge=self.bridge,persist=persist,now=101)
        self.assertEqual(state['last']['outcome'],'unconfirmed')
        reloaded=json.loads(json.dumps(state))
        supply.maintain_supply(rpc,self.views,reloaded,bridge=self.bridge,now=102)
        self.assertEqual([c[1] for c in calls],['buddy-actions.status','buddy-trade.transfer_step'])

    def test_host_identity_change_is_known_no_write(self):
        state=dict(memory=copy.deepcopy(self.memory));calls=[]
        def rpc(binding,method,params):calls.append(method);return dict(sessionId='replacement',selfGuid='11')
        supply.maintain_supply(rpc,self.views,state,bridge=self.bridge,now=100)
        supply.maintain_supply(rpc,self.views,state,bridge=self.bridge,now=101)
        self.assertEqual(state['last']['outcome'],'not_dispatched')
        self.assertEqual(calls,['buddy-actions.status'])


if __name__=='__main__':unittest.main()
