"""Exercise the accepted Lua supply policy and real adapter, with fake game RPCs."""
import copy
import hashlib
import io
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import party_mana_supply as supply


def observation(guid,count,x,level=20):
    sid=f'aw-{guid}'
    player=dict(guid=str(guid),present=True,aliveKnown=True,alive=True,unitFlagsKnown=True,
        mapKnown=True,mapId=34,levelKnown=True,level=level,positionKnown=True,x=x,power=100,
        maxPower=100,powerKnown=True)
    bags=[dict(bag=i,slotKnown=True,contentsKnown=True,slotCount=1 if i==0 else 0,
        slots=[dict(bag=0,slot=0)] if i==0 else []) for i in range(5)]
    return dict(world=dict(self=player,owner=dict(player,guid='3'),inventoryKnown=True,inventoryRevision='5',inventoryBags=bags),
        party=dict(sessionId=sid,group=dict(known=True,inGroup=True,groupGuid='77',
            members=[dict(guid=str(guid),self=True,online=True),dict(guid='3',online=True)])),
        actions=dict(sessionId=sid,selfGuid=str(guid)),
        inventory=dict(sessionId=sid,selfGuid=str(guid),inventoryKnown=True,inventoryRevision='5',
            manaDrinkCount=count,inventoryBags=bags),
        trade=dict(sessionId=sid,phase='IDLE',revision='2',completedTrades='0'))


def carried(mage,entry,count):
    mage['world'].update(backpackKnown=True,backpackRevision='5',backpack=[dict(entry=entry,
        stackCount=count,metadataKnown=True,slot=8,itemGuid=str(0x4000000000000001))])


class SupplyTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        root=Path(__file__).resolve().parents[1]
        binary=Path(os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY',root/'data/bin/luapolicy'))
        if not binary.is_file():raise RuntimeError('Actual Lua evaluator required; set BUDDY_LUAPOLICY_TEST_BINARY')
        cls.root=root;cls.lua=(root/'behaviors/mana-supply.lua').read_text()
        cls.dependencies='local disposal=(function()\n'+(root/'behaviors/item-disposal.lua').read_text()+'\nend)()\nlocal gear=(function()\n'+(root/'behaviors/gear.lua').read_text()+'\nend)()\n'
        cls.bridge=supply.runtime.Bridge(binary,root/'modules',value_bytes=supply.VALUE_BYTES,execution_ms=1000)

    def setUp(self):
        self.mira=observation(4,18,0);self.arlen=observation(1,0,3);self.nix=observation(8,16,4)
        self.observations={8086:self.arlen,8087:self.mira,8402:self.nix};self.calls=[]
        self.source=self.dependencies+'local m=(function()\n'+self.lua+'\nend)()\nreturn {mana_supply_plan=function(input)return m.plan(input,disposal,gear)end}'
        self.generation=1;self.gift_available=True;self.fail_method=None;self.replace_body=None
        self.toolkit=dict(sessionId='aw-4',selfGuid='4',spellbookKnown=True,spellbookRevision='7',spells=[
            dict(familyId=5504,createdItemEntry=item,rank=rank,spellId=spell)
            for rank,spell,item in [(2,5505,2288),(3,5506,2136)]])
        self.metadata={entry:dict(itemId=entry,available=True,requiredLevel=level,
            rawTemplateFacts=dict(maxStackCount=20)) for entry,level in [(2288,5),(2136,15)]}
        self.refresh_world()

    def refresh_world(self):
        for view in self.observations.values():
            view['world']['nearbyPlayers']=[other['world']['self'] for other in self.observations.values() if other is not view]

    def revision(self):return hashlib.sha256(self.source.encode()).hexdigest()

    def catalog(self,port):
        view=self.observations[port];guid=view['actions']['selfGuid']
        fields=lambda values:[dict(name=name,kind=kind,repeated=False,map=False,optional=False,required=False) for name,kind in values]
        methods=[dict(name='buddy-actions.create_item',effect='mutation',inputType='CreateItem'),
            dict(name='buddy-trade.state',effect='read',outputType='TradeState')]
        methods.append(dict(name='buddy-trade.destroy_junk',effect='mutation',inputType='Junk'))
        if self.gift_available:methods.append(dict(name='buddy-trade.water_gift',effect='mutation',inputType='Gift'))
        return dict(sessionId=view['actions']['sessionId'],characterGuid=guid,revision='c'*64,methods=methods,
            messages=dict(TradeState=dict(fields=fields([('sessionId','string'),('revision','uint64'),('phase','enum')])),CreateItem=dict(fields=fields([('sessionId','string'),('selfGuid','uint64'),('spellId','uint32')])),
                Junk=dict(fields=fields([('sessionId','string'),('revision','uint64'),('backpackRevision','uint64'),
                    ('backpackSlot','uint32'),('expectedItemGuid','uint64'),('inventoryRevision','uint64'),('sourceBag','uint32'),('sourceSlot','uint32')])),
                Gift=dict(fields=fields([('sessionId','string'),('revision','uint64'),('partnerGuid','uint64'),
                    ('itemEntry','uint32'),('itemCount','uint32'),('expectedItemGuid','uint64'),
                    ('backpackRevision','uint64'),('backpackSlot','uint32'),('inventoryRevision','uint64'),
                    ('sourceBag','uint32'),('sourceSlot','uint32')]))))

    def rpc(self,port,method,params):
        view=self.observations[port];actions=view['actions']
        if method=='session.policy_source':return dict(sessionId='aw-4',characterGuid='4',source=self.source,
            revision=self.revision(),generation=str(self.generation),sourceLimitBytes=8*1024*1024)
        if method=='session.attach':return dict(sessionId='aw-4',characterGuid='4',
            policy=dict(revision=self.revision(),generation=str(self.generation)))
        if method=='session.describe':return self.catalog(port)
        if method=='buddy-actions.status':return dict(actions,sessionId='replacement') if self.replace_body==port else dict(actions)
        if method=='buddy-actions.spell_toolkit':return copy.deepcopy(self.toolkit)
        if method=='buddy-trade.lookup_items':return dict(sessionId='aw-4',items=[self.metadata[i] for i in params['itemIds'] if i in self.metadata])
        self.calls.append((port,method,copy.deepcopy(params)))
        if method==self.fail_method:raise OSError('Reply lost after possible native write')
        if method=='buddy-trade.water_gift':
            view['trade'].update(waterGiftArmed=True,partnerGuid=params['partnerGuid'],revision=str(int(view['trade'].get('revision','0'))+1))
            return copy.deepcopy(view['trade'])
        if method=='buddy-trade.destroy_junk':return copy.deepcopy(view['trade'])
        self.assertEqual(method,'buddy-actions.create_item')
        entry=next((row['createdItemEntry'] for row in self.toolkit['spells'] if row['spellId']==params['spellId']),0)
        return dict(actions,lastSpellOperation=dict(capability='create_item',spellId=params['spellId'],phase='waiting_server',
            transportWritten=True,createdItemEntry=entry,baselineInventoryRevision=view['world']['inventoryRevision']))

    def turn(self,state=None,now=1000,bridge=None,persist=None):
        return supply.maintain_supply(self.rpc,self.observations,state if state is not None else {},
            bridge=bridge or self.bridge,now_ms=now,persist=persist)

    def gift(self):
        state={};self.assertEqual(self.turn(state),'recipient_water_reservation_requested')
        self.assertEqual(self.turn(state),'water_gift_offer_requested');return state

    def cleanup_fixture(self):
        carried(self.mira,2136,8)
        row=self.arlen['inventory']['inventoryBags'][0]['slots'][0]
        row.update(entry=900,stackCount=2,itemGuid=str(0x4000000000000042),metadataKnown=True,
            enhancementsKnown=True,hasEnhancements=False)
        self.metadata[900]=dict(itemId=900,available=True,itemClass=15,sellPrice=5,rawTemplateFacts={})
        self.arlen['world'].update(selfQuestLogKnown=True,selfQuestLogRevision='7',selfQuestLog=[dict(slot=i) for i in range(25)])
        self.arlen['journal']=dict(sessionId='aw-1',questLogRevision='7',entries=[])
        self.arlen['trade'].update(backpackKnown=True,backpackRevision='5')
        return row

    def test_omitted_proto3_zero_trade_revision_on_both_bodies(self):
        carried(self.mira,2136,8)
        self.mira['trade'].pop('revision');self.arlen['trade'].pop('revision')
        original=copy.deepcopy(self.observations)
        decision,_,_=supply._evaluate(self.rpc,self.observations,{},self.bridge,1000)
        self.assertEqual(self.observations,original)
        self.assertEqual(decision['action']['params']['revision'],'0')
        self.gift()
        self.assertEqual([(port,method,params['revision']) for port,method,params in self.calls],
            [(8086,'buddy-trade.water_gift','0'),(8087,'buddy-trade.water_gift','0')])

    def test_missing_state_schema_or_malformed_receiver_sends_nothing(self):
        carried(self.mira,2136,8)
        self.mira['trade'].pop('revision');self.arlen['trade'].pop('revision')
        original_catalog=self.catalog
        def missing(port):
            result=original_catalog(port);result['messages'].pop('TradeState');return result
        self.catalog=missing;state={}
        self.assertEqual(self.turn(state),'water_supply_unavailable')
        self.assertEqual(state['error'],'current_reflected_trade_state_revision_required')
        self.assertEqual(self.calls,[])
        self.catalog=original_catalog;self.arlen['trade']['revision']='bad';state={}
        self.assertEqual(self.turn(state),'water_supply_unavailable')
        self.assertEqual(state['error'],'installed_supply_field_type_required:revision')
        self.assertEqual(self.calls,[])

    def test_missing_zero_requires_current_state_and_singular_uint64_schema(self):
        catalog=self.catalog(8087);identity=supply._identity(self.mira,4)
        for fault in ('missing_schema','wrong_kind','repeated','map','optional','required','hasDefault','oneof','missing_presence','sid','state'):
            view=copy.deepcopy(self.mira);view['trade'].pop('revision');schema=copy.deepcopy(catalog)
            field=schema['messages']['TradeState']['fields'][1]
            if fault=='missing_schema':schema['messages'].pop('TradeState')
            elif fault=='wrong_kind':field['kind']='string'
            elif fault in ('repeated','map','optional','required','hasDefault'):field[fault]=True
            elif fault=='oneof':field['oneof']='selected'
            elif fault=='missing_presence':field.pop('optional')
            elif fault=='sid':view['trade']['sessionId']='other'
            else:view.pop('trade')
            before=copy.deepcopy(view)
            with self.assertRaisesRegex(supply.runtime.Error,'current_reflected_trade_state_revision_required'):
                supply._trade_state_revision(view,schema,identity)
            self.assertEqual(view,before)
        omitted_default=copy.deepcopy(self.mira);omitted_default['trade'].pop('revision')
        normal=copy.deepcopy(catalog);normal['messages']['TradeState']['fields'][1].pop('hasDefault',None)
        supply._trade_state_revision(omitted_default,normal,identity)
        self.assertEqual(omitted_default['trade']['revision'],'0')
        for value in ('','bad',None,0,'19'):
            view=copy.deepcopy(self.mira);view['trade']['revision']=value
            supply._trade_state_revision(view,catalog,identity)
            self.assertEqual(view['trade']['revision'],value)
            action=dict(id='test',method='buddy-trade.water_gift',params=dict(sessionId=identity['sessionId'],revision=value))
            if value=='19':supply._validate_action(action,catalog,identity)
            else:
                with self.assertRaisesRegex(supply.runtime.Error,'installed_supply_field_type_required:revision'):
                    supply._validate_action(action,catalog,identity)

    def test_carried_water_goes_to_empty_caster_through_two_correlated_lua_turns(self):
        carried(self.mira,2136,8);self.gift()
        self.assertEqual([call[:2] for call in self.calls],[(8086,'buddy-trade.water_gift'),(8087,'buddy-trade.water_gift')])
        self.assertEqual(self.calls[1][2]['partnerGuid'],'1')
        self.assertEqual(self.calls[1][2]['expectedItemGuid'],str(0x4000000000000001))
        self.assertEqual(self.calls[1][2]['backpackSlot'],8)

    def test_own_reserve_and_recipient_level_choose_current_compatible_rank(self):
        carried(self.mira,2288,14);self.arlen['world']['self']['level']=14
        self.arlen['inventory']['manaDrinkCount']=1
        self.assertEqual(self.turn(),'conjure_requested')
        self.assertEqual(self.calls,[(8087,'buddy-actions.create_item',dict(sessionId='aw-4',selfGuid='4',spellId=5505))])

    def test_empty_peer_can_receive_small_whole_stack_without_waiting_for_a_second_conjure(self):
        self.mira['inventory']['manaDrinkCount']=3;carried(self.mira,2136,3)
        self.gift();self.assertEqual(self.calls[1][2]['itemCount'],3)

    def test_rank_selection_uses_arbitrary_new_projected_learned_id_without_python_spell_list(self):
        self.toolkit['spells'].append(dict(familyId=5504,createdItemEntry=7777,rank=9,spellId=4294967294))
        self.metadata[7777]=dict(itemId=7777,available=True,requiredLevel=15,rawTemplateFacts=dict(maxStackCount=20))
        self.assertEqual(self.turn(),'conjure_requested');self.assertEqual(self.calls[0][2]['spellId'],4294967294)

    def test_fighting_distant_or_resting_peers_do_not_start_supply(self):
        for change in [dict(x=15),dict(unitFlags=0x80000)]:
            with self.subTest(change=change):
                original=copy.deepcopy(self.arlen['world']['self']);self.arlen['world']['self'].update(change)
                self.assertEqual(self.turn(),'water_stock_sufficient');self.arlen['world']['self'].update(original)
        self.arlen['mana_rest_active']=True;self.assertEqual(self.turn(),'water_stock_sufficient')
        self.arlen['mana_rest_active']=False;self.mira['mana_rest_active']=True;self.assertEqual(self.turn(),'mage_busy')
        self.assertEqual(self.calls,[])

    def test_full_bags_and_observed_partial_stack_capacity(self):
        carried(self.mira,2136,6);row=self.arlen['inventory']['inventoryBags'][0]['slots'][0]
        row.update(entry=117,stackCount=5,metadataKnown=True,itemGuid='11')
        state={};self.assertEqual(self.turn(state),'cleanup_facts_unknown_or_items_retained');self.assertEqual(self.calls,[])
        row.update(entry=2136,stackCount=8);self.arlen['inventory']['manaDrinkCount']=8
        self.gift();self.assertEqual(self.calls[1][2]['itemCount'],6)

    def test_old_absent_stack_fact_is_unknown_and_present_signed_sentinel_is_honored(self):
        carried(self.mira,2136,6);self.arlen['inventory']['manaDrinkCount']=8
        self.arlen['inventory']['inventoryBags'][0]['slots'][0].update(entry=2136,stackCount=8,metadataKnown=True,itemGuid='14')
        self.metadata[2136]['rawTemplateFacts']={}
        self.assertEqual(self.turn(),'cleanup_facts_unknown_or_items_retained');self.assertEqual(self.calls,[])
        self.metadata[2136]['rawTemplateFacts']['maxStackCount']=0xFFFFFFFF
        self.gift();self.assertEqual(self.calls[-1][2]['itemCount'],6)

    def test_same_lua_threshold_and_observed_capacity_supply_bag_pressure_without_actions(self):
        self.arlen['inventory']['manaDrinkCount']=8
        row=self.arlen['inventory']['inventoryBags'][0]['slots'][0]
        row.update(entry=2136,stackCount=8,metadataKnown=True,itemGuid='14')
        pressure=lambda:supply.supply_space_pressure(self.rpc,self.observations,bridge=self.bridge)
        self.assertIs(pressure()['8086'],False)
        row.update(entry=117)
        self.assertIs(pressure()['8086'],True)
        self.source=self.source.replace('peer_reorder_threshold=12','peer_reorder_threshold=5');self.generation+=1
        self.assertNotIn('8086',pressure())
        self.source='return {}';self.generation+=1
        self.assertEqual(pressure(),{});self.assertEqual(self.calls,[])

    def test_empty_sparse_interaction_owner_is_not_a_gameplay_off_state(self):
        self.mira['actions']['interactionOwner']='';self.arlen['actions']['interactionOwner']=''
        carried(self.mira,2136,8);self.gift()

    def test_duplicate_created_entry_uses_highest_observed_rank_independent_of_spellbook_order(self):
        self.toolkit['spells'].insert(0,dict(familyId=5504,createdItemEntry=2136,rank=9,spellId=123456))
        self.assertEqual(self.turn(),'conjure_requested');self.assertEqual(self.calls[0][2]['spellId'],123456)

    def test_current_rank_cost_and_matching_resource_choose_usable_spell_at_exact_boundary(self):
        self.toolkit['spells'][0].update(flatCost=20)
        self.toolkit['spells'][1].update(flatCost=2,manaPercent=20,mechanics=dict(costPerLevel=1))
        self.mira['world']['self'].update(power=41,baseManaKnown=True,baseMana=100)
        self.assertEqual(self.turn(),'conjure_requested');self.assertEqual(self.calls[-1][2]['spellId'],5505)
        self.mira['world']['self']['power']=42
        self.assertEqual(self.turn(),'conjure_requested');self.assertEqual(self.calls[-1][2]['spellId'],5506)
        self.mira['world']['self']['powerType']=1;self.calls.clear()
        self.assertEqual(self.turn(),'waiting_for_mana_to_conjure');self.assertEqual(self.calls,[])

    def test_actual_action_position_is_used_instead_of_stale_world_self_pose(self):
        carried(self.mira,2136,8);self.mira['world']['self']['x']=500
        self.mira['actions']['position']=dict(known=True,mapKnown=True,mapId=34,x=0)
        self.arlen['world']['nearbyPlayers'][0]=dict(self.mira['world']['self'],x=0)
        self.gift()

    def test_mage_needs_room_and_pending_native_conjure_is_not_repeated(self):
        self.mira['inventory']['manaDrinkCount']=0
        self.mira['inventory']['inventoryBags'][0]['slots'][0].update(entry=117,stackCount=3,metadataKnown=True,itemGuid='11')
        state={};self.assertEqual(self.turn(state),'cleanup_facts_unknown_or_items_retained')
        self.mira['inventory']['inventoryBags'][0]['slots'][0].update(entry=2288,stackCount=5)
        self.assertEqual(self.turn(),'conjure_requested');self.assertEqual(self.calls[-1][2]['spellId'],5505)
        self.calls.clear();self.mira['actions']['lastSpellOperation']=dict(capability='create_item',phase='waiting_created_item')
        self.assertEqual(self.turn(),'conjure_pending');self.assertEqual(self.calls,[])

    def rejected_create_rpc(self, changes=None, *, response_id=1, code=-32000, request_method=None, data_override=...):
        ordinary=self.rpc
        def call(port,method,params):
            if method!='buddy-actions.create_item':return ordinary(port,method,params)
            self.calls.append((port,method,params))
            data=dict(module='buddy-actions',method='create_item',grpc_status='FailedPrecondition',
                      grpc_message='stationary caster required')
            if changes:data.update(changes)
            if data_override is not ...:data=data_override
            client=supply.runtime.RPC(f'http://127.0.0.1:{port}/rpc')
            response=io.BytesIO(json.dumps(dict(jsonrpc='2.0',id=response_id,error=dict(
                code=code,message='native rejection',data=data))).encode())
            with patch.object(client.opener,'open',return_value=response):
                return client.call(request_method or method,params)
        return call

    def test_create_refusal_keeps_same_id_durable_reason_and_cooldown_without_replay(self):
        self.rpc=self.rejected_create_rpc();state={};saved=[]
        persist=lambda value:saved.append(copy.deepcopy(value))
        self.assertEqual(self.turn(state,persist=persist),'water_supply_not_dispatched')
        first=state['last']['id']
        self.assertEqual(saved[0]['last']['outcome'],'transport_unconfirmed')
        self.assertEqual(saved[-1]['last'],dict(id=first,outcome='not_dispatched',
            reason='create_item_failed_precondition',rejection=dict(module='buddy-actions',method='create_item',
            grpc_status='FailedPrecondition',grpc_message='stationary caster required')))
        self.assertEqual(self.turn(state,now=1001),'create_item_failed_precondition')
        self.assertNotIn('pending',state['memory'])
        for now in (1002,2000,5000):self.turn(state,now=now)
        self.assertEqual(len(self.calls),1)
        self.assertEqual(self.turn(state,now=5001),'water_supply_not_dispatched')
        self.assertNotEqual(state['last']['id'],first)
        self.turn(state,now=5002);self.turn(state,now=9001)
        self.assertEqual(len(self.calls),2)

    def test_only_exact_create_precondition_provenance_can_settle_unknown(self):
        for options in (dict(changes=dict(module='buddy-world')),dict(changes=dict(method='cast')),
                        dict(changes=dict(grpc_status='Internal')),dict(changes=dict(grpc_message=None)),
                        dict(code=9),dict(response_id=2),dict(request_method='buddy-actions.cast'),
                        dict(data_override=None),dict(data_override=[]),dict(data_override='FailedPrecondition')):
            with self.subTest(options=options):
                ordinary=self.rpc;self.rpc=self.rejected_create_rpc(**options)
                state={}
                self.assertEqual(self.turn(state),'water_supply_rpc_unconfirmed')
                self.assertEqual(state['last']['outcome'],'transport_unconfirmed')
                self.turn(state,now=50000)
                self.assertEqual(state['last']['outcome'],'transport_unconfirmed')
                self.rpc=ordinary

    def test_standalone_worker_preserves_create_refusal_but_keeps_read_error_handling(self):
        loader=importlib.machinery.SourceFileLoader('supply_worker_refusal_test',str(self.root/'dev/party-mana'))
        spec=importlib.util.spec_from_loader(loader.name,loader)
        worker=importlib.util.module_from_spec(spec);loader.exec_module(worker)
        bound=self.rejected_create_rpc()
        class Client:
            def __init__(self,port):self.port=port
            def call(self,method,params,timeout):return bound(self.port,method,params)
        worker._RPC_CLIENTS={port:Client(port) for port in self.observations}
        state={}
        self.assertEqual(supply.maintain_supply(worker.rpc,self.observations,state,
            bridge=self.bridge,now_ms=1000),'water_supply_not_dispatched')
        self.assertEqual(state['last']['rejection']['grpc_status'],'FailedPrecondition')
        self.assertEqual(len(self.calls),1)
        error=supply.runtime.RPCRejected('buddy-world.snapshot','http://127.0.0.1:8087/rpc',1,
            -32000,'read unavailable',dict(module='buddy-world',method='snapshot'))
        with patch.object(worker._RPC_CLIENTS[8087],'call',side_effect=error):
            with self.assertRaises(worker.RPCError):worker.rpc(8087,'buddy-world.snapshot')
            self.assertIsNone(worker.observe(8087))

    def test_error_text_alone_and_refusal_persistence_failure_remain_unknown(self):
        ordinary=self.rpc
        def text_error(port,method,params):
            if method=='buddy-actions.create_item':
                raise supply.runtime.Error('rpc_rejected(-32000): FailedPrecondition create_item')
            return ordinary(port,method,params)
        self.rpc=text_error;state={}
        self.assertEqual(self.turn(state),'water_supply_rpc_unconfirmed')
        self.assertEqual(state['last']['outcome'],'transport_unconfirmed')
        self.rpc=ordinary;self.rpc=self.rejected_create_rpc();state={};saved=[]
        def persist(value):
            if value.get('last',{}).get('outcome')=='not_dispatched':raise OSError('disk failed')
            saved.append(copy.deepcopy(value))
        self.assertEqual(self.turn(state,persist=persist),'water_supply_persistence_unconfirmed')
        self.assertEqual(state['last']['outcome'],'transport_unconfirmed')
        self.assertEqual(saved[-1]['last']['outcome'],'transport_unconfirmed')
        self.assertEqual(self.turn(state,now=50000),'conjure_outcome_unconfirmed')
        self.assertEqual(len(self.calls),1)

    def test_conjure_unknown_survives_same_body_policy_tuning_without_replay(self):
        state={};self.fail_method='buddy-actions.create_item'
        self.assertEqual(self.turn(state),'water_supply_rpc_unconfirmed');self.assertEqual(len(self.calls),1)
        self.source=self.source.replace('poll_ms=4000','poll_ms=0');self.generation+=1
        self.assertEqual(self.turn(state,now=50000),'conjure_outcome_unconfirmed');self.assertEqual(len(self.calls),1)
        self.mira['actions']['lastSpellOperation']=dict(capability='create_item',spellId=5506,phase='confirmed_item',
            createdItemEntry=2136,baselineInventoryRevision='5',observedInventoryRevision='6',serverCastGo=True,
            effectObserved=True,inventoryConfirmed=True,itemPushObserved=True)
        self.assertEqual(self.turn(state,now=60000),'conjure_outcome_unconfirmed');self.assertIn('pending',state['memory'])

    def test_creation_effect_requires_correlated_receipt_and_fresh_matching_inventory_baseline(self):
        state={};self.assertEqual(self.turn(state),'conjure_requested')
        old=dict(capability='create_item',spellId=5506,phase='confirmed_item',createdItemEntry=2136,
            baselineInventoryRevision='4',observedInventoryRevision='5',serverCastGo=True,
            effectObserved=True,inventoryConfirmed=True,itemPushObserved=True)
        self.mira['actions']['lastSpellOperation']=old
        self.assertEqual(self.turn(state),'conjure_pending');self.assertEqual(len(self.calls),1)
        self.mira['actions']['lastSpellOperation']=dict(old,baselineInventoryRevision='5',observedInventoryRevision='6')
        self.assertEqual(self.turn(state),'observed_created_water');self.assertNotIn('pending',state['memory'])

    def test_definitive_correlated_server_rejection_retires_attempt_but_unknown_native_creation_does_not_replay(self):
        state={};self.assertEqual(self.turn(state),'conjure_requested')
        self.mira['actions']['lastSpellOperation']=dict(capability='create_item',spellId=5506,
            createdItemEntry=2136,baselineInventoryRevision='5',phase='server_rejected_no_replay')
        self.assertEqual(self.turn(state),'creation_server_rejected');self.assertNotIn('pending',state['memory'])
        self.assertEqual(self.turn(state,now=4999),'creation_server_rejected');self.assertEqual(len(self.calls),1)
        self.assertEqual(self.turn(state,now=5000),'conjure_requested');self.assertEqual(len(self.calls),2)
        self.mira['actions']['lastSpellOperation']['phase']='outcome_unconfirmed_no_retry';self.calls.clear()
        self.assertEqual(self.turn({}),'conjure_outcome_unconfirmed');self.assertEqual(self.calls,[])

    def test_completion_uses_native_transaction_not_brittle_whole_inventory_conservation(self):
        carried(self.mira,2136,8);state=self.gift()
        for view,partner in [(self.mira,'1'),(self.arlen,'4')]:
            view['trade'].update(phase='COMPLETE',partnerGuid=partner,lastServerStatus=8,completedTrades='1')
        self.arlen['inventory']['manaDrinkCount']=7 # One received drink already consumed.
        self.mira['inventory']['manaDrinkCount']=15 # Another stack was created/looted.
        self.assertEqual(self.turn(state,now=3000),'server_confirmed_peer_trade')
        self.assertNotIn('pending',state['memory']);self.assertEqual(len(self.calls),2)

    def test_missing_peer_observation_and_unknown_reservation_do_not_replay(self):
        carried(self.mira,2136,8);state={};self.fail_method='buddy-trade.water_gift'
        self.assertEqual(self.turn(state),'water_supply_rpc_unconfirmed');self.assertEqual(len(self.calls),1)
        saved=self.observations.pop(8086)
        self.assertEqual(self.turn(state),'awaiting_recipient_observations');self.assertEqual(len(self.calls),1)
        self.observations[8086]=saved
        self.assertEqual(self.turn(state),'recipient_reservation_unconfirmed');self.assertEqual(len(self.calls),1)

    def anonymous_receiver_arm(self):
        carried(self.mira,2136,8);self.arlen['trade']['revision']='0';state={}
        self.assertEqual(self.turn(state),'recipient_water_reservation_requested')
        for receipt in (state['last']['result'],self.arlen['trade']):
            receipt.pop('partnerGuid',None);receipt.update(phase='IDLE',waterGiftArmed=True,reason='waiting_for_water_gift_invitation')
        self.assertEqual(state['last']['result']['revision'],'1')
        return state

    def test_native_idle_anonymous_arm_then_offer_and_actual_completion(self):
        state=self.anonymous_receiver_arm()
        self.assertEqual(self.turn(state),'water_gift_offer_requested');self.assertEqual(len(self.calls),2)
        self.assertNotIn('partnerGuid',self.arlen['trade'])
        for view,partner in ((self.mira,'1'),(self.arlen,'4')):
            view['trade'].update(phase='COMPLETE',partnerGuid=partner,lastServerStatus=8,completedTrades='1')
        self.assertEqual(self.turn(state),'server_confirmed_peer_trade');self.assertNotIn('pending',state['memory'])

    def test_native_idle_anonymous_arm_expired_without_donor_replay(self):
        state=self.anonymous_receiver_arm();self.arlen['trade'].update(waterGiftArmed=False,revision='2')
        self.assertEqual(self.turn(state),'recipient_reservation_ended_before_offer');self.assertEqual(len(self.calls),1);self.assertNotIn('pending',state['memory'])

    def test_anonymous_arm_wrong_peer_changed_revision_unknown_or_body_blocks(self):
        for fault in ('wrong_peer','changed_revision','unknown','receipt_reason','peer_reason','body','count','source_revision'):
            self.setUp();state=self.anonymous_receiver_arm()
            if fault=='wrong_peer':self.arlen['trade']['partnerGuid']='8'
            elif fault=='changed_revision':self.arlen['trade']['revision']='2'
            elif fault=='unknown':state['last']['outcome']='transport_unconfirmed'
            elif fault=='receipt_reason':state['last']['result']['reason']='other'
            elif fault=='peer_reason':self.arlen['trade']['reason']='other'
            elif fault=='body':self.arlen['trade']['sessionId']='different'
            elif fault=='count':self.mira['world']['backpack'][0]['stackCount']=7
            else:self.mira['world']['backpackRevision']='6'
            self.turn(state);self.assertEqual(len(self.calls),1,fault);self.assertIn('pending',state['memory'],fault)

    def test_receiver_reservation_requires_new_exact_partner_receipt_and_current_body_before_offer(self):
        carried(self.mira,2136,8);state={}
        self.assertEqual(self.turn(state),'recipient_water_reservation_requested')
        receipt=lambda:state['last']['result']
        receipt()['partnerGuid']='8'
        self.assertEqual(self.turn(state),'recipient_reservation_unconfirmed')
        receipt()['partnerGuid']='4';receipt()['revision']='2'
        self.assertEqual(self.turn(state),'recipient_reservation_unconfirmed')
        receipt()['revision']='3';self.arlen['trade']['partnerGuid']='8'
        self.assertEqual(self.turn(state),'awaiting_recipient_reservation_observation')
        self.arlen['trade']['partnerGuid']='4';self.arlen['inventory']['sessionId']='old-body'
        self.assertEqual(self.turn(state),'awaiting_current_recipient_body')
        self.assertEqual(len(self.calls),1)
        self.arlen['inventory']['sessionId']='aw-1'
        self.assertEqual(self.turn(state),'water_gift_offer_requested');self.assertEqual(len(self.calls),2)

    def test_observed_finite_receiver_reservation_end_can_replan_before_any_donor_write(self):
        carried(self.mira,2136,8);state={}
        self.assertEqual(self.turn(state),'recipient_water_reservation_requested')
        self.arlen['trade'].update(waterGiftArmed=False,phase='IDLE',revision='4')
        self.assertEqual(self.turn(state),'recipient_reservation_ended_before_offer')
        self.assertEqual(len(self.calls),1);self.assertNotIn('pending',state['memory'])
        self.assertEqual(self.turn(state),'recipient_water_reservation_requested')
        self.assertEqual([call[0] for call in self.calls],[8086,8086]) # No donor item write was replayed.

    def test_structured_current_party_request_is_lua_owned_and_completes_once_from_actual_trade(self):
        carried(self.mira,2136,8);self.arlen['inventory']['manaDrinkCount']=16
        self.assertEqual(self.turn(),'water_stock_sufficient')
        state={'request':dict(id='owner-water-1',sessionId='aw-4',groupGuid='77',recipientGuids=['1'])}
        self.assertEqual(self.turn(state),'recipient_water_reservation_requested')
        self.assertEqual(self.turn(state),'water_gift_offer_requested')
        for view,partner in [(self.mira,'1'),(self.arlen,'4')]:
            view['trade'].update(phase='COMPLETE',partnerGuid=partner,lastServerStatus=8,completedTrades='1',waterGiftArmed=False)
        self.assertEqual(self.turn(state),'server_confirmed_peer_trade')
        self.assertEqual(state['memory']['completedRequestId'],'owner-water-1');self.assertNotIn('request',state['memory'])
        self.assertEqual(self.turn(state,now=5000),'water_stock_sufficient');self.assertEqual(len(self.calls),2)

    def test_structured_request_cannot_use_a_previous_party_or_preempt_uncertain_transfer(self):
        carried(self.mira,2136,8)
        state={'request':dict(id='old-party',sessionId='aw-4',groupGuid='old',recipientGuids=['1'])}
        self.assertEqual(self.turn(state),'requested_supply_body_or_party_changed');self.assertEqual(self.calls,[])
        state={};self.fail_method='buddy-trade.water_gift'
        self.assertEqual(self.turn(state),'water_supply_rpc_unconfirmed')
        state['request']=dict(id='new-owner-request',sessionId='aw-4',groupGuid='77',recipientGuids=['8'])
        self.assertEqual(self.turn(state),'recipient_reservation_unconfirmed');self.assertEqual(len(self.calls),1)

    def test_same_session_lua_stock_and_recipient_tuning_is_the_actual_dispatch_choice(self):
        self.arlen['inventory']['manaDrinkCount']=16
        self.assertEqual(self.turn(),'water_stock_sufficient');self.assertEqual(self.calls,[])
        self.source=self.source.replace('stock_minimum=5','stock_minimum=30');self.generation+=1
        self.assertEqual(self.turn(),'conjure_requested');self.assertEqual(self.calls[-1][2]['spellId'],5506)
        self.calls.clear();self.mira['inventory']['manaDrinkCount']=18
        self.arlen['inventory']['manaDrinkCount']=1;self.nix['inventory']['manaDrinkCount']=2;carried(self.mira,2136,8)
        self.source=self.source.replace('stock_minimum=30','stock_minimum=5').replace('recipient_priority={}','recipient_priority={["8"]=100}');self.generation+=1
        state={};self.assertEqual(self.turn(state),'recipient_water_reservation_requested');self.assertEqual(self.calls[0][0],8402)

    def test_missing_installed_route_or_export_has_no_python_fallback(self):
        carried(self.mira,2136,8);self.gift_available=False
        self.assertEqual(self.turn(),'water_gift_capability_unavailable');self.assertEqual(self.calls,[])
        self.source='return {}';self.generation+=1;state={}
        self.assertEqual(self.turn(state),'water_supply_unavailable')
        self.assertIn('mana_supply_policy_export_unavailable',state['error']);self.assertEqual(self.calls,[])

    def test_preserved_legacy_installed_methods_with_unspecified_effect_keep_typed_exact_dispatch(self):
        original=self.catalog
        def legacy(port):
            catalog=original(port)
            for method in catalog['methods']:
                if method['name'].startswith('buddy-trade.'):method['effect']='unspecified'
            return catalog
        self.catalog=legacy;carried(self.mira,2136,8);self.gift()
        self.assertEqual([call[1] for call in self.calls],['buddy-trade.water_gift','buddy-trade.water_gift'])
        self.calls.clear();self.setUp();self.catalog=legacy;self.cleanup_fixture()
        self.assertEqual(self.turn(),'cleanup_requested');self.assertEqual(self.calls[-1][1],'buddy-trade.destroy_junk')

    def test_shared_disposal_selects_exact_lowest_value_source_then_requires_native_removal_before_supply(self):
        row=self.cleanup_fixture();state={}
        self.arlen['inventory']['inventoryBags'][1].update(slotCount=1,slots=[dict(bag=1,slot=0,entry=901,stackCount=1,
            itemGuid=str(0x4000000000000043),metadataKnown=True,enhancementsKnown=True)])
        self.metadata[901]=dict(self.metadata[900],itemId=901,sellPrice=20)
        self.assertEqual(self.turn(state),'cleanup_requested')
        self.assertEqual(self.calls,[(8086,'buddy-trade.destroy_junk',dict(sessionId='aw-1',revision='2',
            expectedItemGuid=row['itemGuid'],backpackRevision='5',backpackSlot=0))])
        self.assertEqual(state['memory']['pending']['source']['stackCount'],2)
        self.assertEqual(self.turn(state,now=90000),'awaiting_cleanup_effect');self.assertEqual(len(self.calls),1)
        self.arlen['trade'].update(revision='3',lastJunkDisposal=dict(itemGuid=row['itemGuid'],entry=900,count=2,
            backpackRevision='5',status='SERVER_SLOT_REMOVAL_CONFIRMED'))
        self.assertEqual(self.turn(state),'awaiting_cleanup_effect') # Receipt without fresh actual removal is insufficient.
        self.arlen['inventory']['inventoryBags'][0]['slots'][0]=dict(bag=0,slot=0)
        self.arlen['inventory']['inventoryRevision']='6'
        self.assertEqual(self.turn(state),'server_confirmed_cleanup')
        self.assertEqual(self.turn(state),'recipient_water_reservation_requested')

    def test_cleanup_never_replays_unknown_write_or_uses_new_request_to_replace_it(self):
        self.cleanup_fixture();state={};self.fail_method='buddy-trade.destroy_junk'
        self.assertEqual(self.turn(state),'water_supply_rpc_unconfirmed')
        state['request']=dict(id='other-water',sessionId='aw-4',groupGuid='77',recipientGuids=['8'])
        self.source+='\n--same-body policy tune';self.generation+=1
        self.assertEqual(self.turn(state,now=99999),'cleanup_outcome_unconfirmed');self.assertEqual(len(self.calls),1)

    def test_cleanup_retains_quest_enhanced_functional_and_unknown_templates_without_python_fallback(self):
        row=self.cleanup_fixture()
        for edit in ('quest','enhanced','functional','unknown'):
            with self.subTest(edit=edit):
                before=copy.deepcopy(self.arlen);meta=copy.deepcopy(self.metadata[900])
                if edit=='quest':
                    self.arlen['world']['selfQuestLog'][0]['questId']=20
                    self.arlen['journal']['entries']=[dict(questId=20,slot=0,logStateKnown=True,templateKnown=True,itemProtectionKnown=True,
                        targets=[dict(kind='ITEM',targetId=900)])]
                elif edit=='enhanced':self.arlen['inventory']['inventoryBags'][0]['slots'][0]['hasEnhancements']=True
                elif edit=='functional':self.metadata[900]['hasSpell']=True
                else:self.metadata[900].pop('rawTemplateFacts')
                self.assertEqual(self.turn(),'cleanup_facts_unknown_or_items_retained');self.assertEqual(self.calls,[])
                self.arlen.clear();self.arlen.update(before);self.metadata[900]=meta

    def test_cleanup_uses_advertised_request_identity_and_context_guards_when_installed(self):
        self.cleanup_fixture();original=self.catalog
        def catalog(port):
            value=original(port)
            value['messages']['Junk']['fields'] += [dict(name=name,kind=kind) for name,kind in
                [('requestId','string'),('expectedEquipmentRevision','uint64'),('expectedQuestLogRevision','uint64')]]
            return value
        self.catalog=catalog
        self.arlen['world'].update(selfEquipmentKnown=True,selfEquipmentRevision='9')
        self.arlen['inventory'].update(equipmentKnown=True,equipmentRevision='9')
        state={};self.assertEqual(self.turn(state),'cleanup_requested')
        params=self.calls[-1][2];self.assertEqual(params['expectedEquipmentRevision'],'9');self.assertEqual(params['expectedQuestLogRevision'],'7')
        self.assertEqual(params['requestId'],state['memory']['pending']['requestId'])

    def test_body_replacement_before_selected_cleanup_sends_no_packet(self):
        self.cleanup_fixture();self.replace_body=8086;state={}
        self.assertEqual(self.turn(state),'water_supply_unavailable');self.assertEqual(self.calls,[])

    def test_lua_cleanup_defers_to_real_channel_or_inventory_custody(self):
        self.cleanup_fixture();self.arlen['world']['self']['channelSpellId']=430
        self.assertEqual(self.turn(),'water_stock_sufficient');self.assertEqual(self.calls,[])
        self.arlen['world']['self']['channelSpellId']=0;self.arlen['inventory']['active']=True
        self.assertEqual(self.turn(),'water_stock_sufficient');self.assertEqual(self.calls,[])

    def test_existing_native_uncertain_disposal_is_not_overwritten_by_fresh_supply_memory(self):
        self.cleanup_fixture()
        self.arlen['trade']['lastJunkDisposal']=dict(status='TRANSPORT_UNCONFIRMED_NO_RETRY')
        self.assertEqual(self.turn(),'water_stock_sufficient');self.assertEqual(self.calls,[])
        self.mira['trade']['junkOperations']=[dict(status='UNCONFIRMED_NO_RETRY')]
        self.assertEqual(self.turn(),'mage_busy');self.assertEqual(self.calls,[])

    def test_source_change_during_evaluation_or_body_change_before_write_sends_nothing(self):
        actual=self.bridge;outer=self
        class Change:
            def evaluate(self,source,frame):
                value=actual.evaluate(source,frame);outer.source+='\n-- concurrently changed';outer.generation+=1;return value
        state={};self.assertEqual(self.turn(state,bridge=Change()),'water_supply_unavailable')
        self.assertIn('supply_policy_changed_before_dispatch',state['error']);self.assertEqual(self.calls,[])
        carried(self.mira,2136,8);self.replace_body=8086;state={}
        self.assertEqual(self.turn(state),'water_supply_unavailable');self.assertIn('supply_body_changed_before_dispatch',state['error'])
        self.assertEqual(self.calls,[])

    def test_full_current_encounter_bundle_exposes_same_supply_decision(self):
        self.source=subprocess.check_output(['python3','-B',str(self.root/'dev/encounter-bundle')],text=True)
        self.generation+=1;self.assertEqual(self.turn(),'conjure_requested')
        self.assertEqual(self.calls[-1][2]['spellId'],5506)

    def test_persistence_receives_detached_full_reservation_before_rpc_and_reply_after(self):
        carried(self.mira,2136,8);state={'callerFact':'preserve'};saved=[];original=self.rpc
        def save(value):
            saved.append(copy.deepcopy(value))
            value['callerFact']='writer-local-mutation'
        def rpc(port,method,params):
            if method=='buddy-trade.water_gift':
                self.assertEqual(saved[-1]['last']['outcome'],'transport_unconfirmed')
                self.assertEqual(saved[-1]['memory']['pending']['actionId'],saved[-1]['last']['id'])
            return original(port,method,params)
        self.rpc=rpc
        self.assertEqual(self.turn(state,persist=save),'recipient_water_reservation_requested')
        self.assertEqual([row['last']['outcome'] for row in saved],['transport_unconfirmed','rpc_result'])
        self.assertEqual(state['callerFact'],'preserve');self.assertEqual(len(self.calls),1)

    def test_actual_store_crash_after_native_write_reloads_unknown_and_never_replays(self):
        class Crash(BaseException):pass
        carried(self.mira,2136,8);original=self.rpc
        def crash(port,method,params):
            result=original(port,method,params)
            if method=='buddy-trade.water_gift':raise Crash()
            return result
        self.rpc=crash
        with tempfile.TemporaryDirectory() as folder:
            with supply.runtime.Store(Path(folder).resolve()) as store:
                with self.assertRaises(Crash):self.turn({},persist=store.save)
            with supply.runtime.Store(Path(folder).resolve()) as store:
                restored=store.load();self.rpc=original
                self.assertEqual(restored['last']['outcome'],'transport_unconfirmed')
                self.assertEqual(self.turn(restored,now=100,persist=store.save),'recipient_reservation_unconfirmed')
                self.assertEqual(len(self.calls),1)
                self.assertEqual(store.load()['memory']['pending'],restored['memory']['pending'])

    def test_persistence_failure_before_rpc_sends_zero_and_retains_reservation(self):
        carried(self.mira,2136,8);state={}
        def failed(_):raise OSError('store write failed')
        self.assertEqual(self.turn(state,persist=failed),'water_supply_persistence_unconfirmed')
        self.assertEqual(self.calls,[]);self.assertIn('pending',state['memory'])
        self.assertEqual(state['last']['outcome'],'not_dispatched')
        self.assertEqual(self.turn(state,persist=lambda value:None),'supply_reservation_not_persisted')
        self.assertNotIn('pending',state['memory'])
        self.assertEqual(self.calls,[])

    def test_reply_save_failure_keeps_only_durable_unknown_in_memory_and_store(self):
        carried(self.mira,2136,8);state={}
        with tempfile.TemporaryDirectory() as folder:
            with supply.runtime.Store(Path(folder).resolve()) as store:
                writes=[]
                def save(value):
                    writes.append(value['last']['outcome'])
                    if len(writes)==2:raise OSError('reply fsync failed')
                    store.save(value)
                self.assertEqual(self.turn(state,persist=save),'water_supply_persistence_unconfirmed')
                self.assertEqual(writes,['transport_unconfirmed','rpc_result'])
                self.assertEqual(state['last']['outcome'],'transport_unconfirmed')
                restored=store.load();self.assertEqual(restored['last']['outcome'],'transport_unconfirmed')
                self.assertEqual(self.turn(restored,persist=store.save),'recipient_reservation_unconfirmed')
                self.assertEqual(len(self.calls),1)

    def test_failed_native_reply_persists_unknown_outcome_without_replay(self):
        carried(self.mira,2136,8);self.fail_method='buddy-trade.water_gift'
        with tempfile.TemporaryDirectory() as folder:
            with supply.runtime.Store(Path(folder).resolve()) as store:
                self.assertEqual(self.turn({},persist=store.save),'water_supply_rpc_unconfirmed')
                restored=store.load();self.assertEqual(restored['phase'],'water_supply_rpc_unconfirmed')
                self.assertEqual(restored['last']['outcome'],'transport_unconfirmed')
                self.assertEqual(self.turn(restored,persist=store.save),'recipient_reservation_unconfirmed')
                self.assertEqual(len(self.calls),1)

    def test_observed_completion_is_not_committed_in_ram_if_its_save_fails(self):
        state={};self.assertEqual(self.turn(state),'conjure_requested')
        prior=copy.deepcopy(state['memory']['pending'])
        self.mira['actions']['lastSpellOperation']=dict(capability='create_item',spellId=5506,phase='confirmed_item',
            createdItemEntry=2136,baselineInventoryRevision='5',observedInventoryRevision='6',serverCastGo=True,
            effectObserved=True,inventoryConfirmed=True,itemPushObserved=True)
        def fail(_):return False
        self.assertEqual(self.turn(state,persist=fail),'water_supply_persistence_unconfirmed')
        self.assertEqual(state['memory']['pending'],prior);self.assertEqual(len(self.calls),1)

    def test_known_no_send_cleanup_settles_only_on_normal_read_after_durable_save(self):
        self.cleanup_fixture();state={}
        def fail(_):raise OSError('fsync failed')
        self.assertEqual(self.turn(state,persist=fail),'water_supply_persistence_unconfirmed')
        self.assertEqual(state['last']['outcome'],'not_dispatched');self.assertIn('pending',state['memory'])
        self.assertEqual(self.turn(state,persist=fail),'water_supply_persistence_unconfirmed')
        self.assertIn('pending',state['memory']);self.assertEqual(self.calls,[])
        self.assertEqual(self.turn(state,persist=lambda value:None),'supply_reservation_not_persisted')
        self.assertNotIn('pending',state['memory']);self.assertEqual(self.calls,[])
        self.assertEqual(self.turn(state,now=5000,persist=lambda value:None),'cleanup_requested')
        self.assertEqual(len(self.calls),1)

    def test_typed_owner_refusal_before_client_call_persists_no_send_without_inline_retry(self):
        carried(self.mira,2136,8);original=self.rpc
        def refused(port,method,params):
            if method=='buddy-trade.water_gift':raise supply.NotDispatched('owner_intent_superseded')
            return original(port,method,params)
        self.rpc=refused
        with tempfile.TemporaryDirectory() as folder:
            with supply.runtime.Store(Path(folder).resolve()) as store:
                state={};self.assertEqual(self.turn(state,persist=store.save),'water_supply_not_dispatched')
                self.assertEqual(store.load()['last']['outcome'],'not_dispatched');self.assertEqual(self.calls,[])
                self.assertEqual(self.turn(state,persist=store.save),'owner_intent_superseded')
                self.assertNotIn('pending',state['memory']);self.assertEqual(self.calls,[])

    def test_source_change_during_reservation_save_never_enters_selected_rpc(self):
        carried(self.mira,2136,8);saved=[]
        def save(value):
            saved.append(copy.deepcopy(value))
            if value.get('last',{}).get('outcome')=='transport_unconfirmed':
                self.source+='\n-- source replaced during fsync';self.generation+=1
        state={};self.assertEqual(self.turn(state,persist=save),'water_supply_unavailable')
        self.assertEqual(self.calls,[]);self.assertEqual(state['last']['outcome'],'not_dispatched')
        self.assertEqual(saved[-1]['last']['outcome'],'not_dispatched')

    def test_durable_poll_clock_regression_does_not_disable_fresh_supply(self):
        self.arlen['inventory']['manaDrinkCount']=16
        with tempfile.TemporaryDirectory() as folder:
            with supply.runtime.Store(Path(folder).resolve()) as store:
                self.assertEqual(self.turn({},now=100000,persist=store.save),'water_stock_sufficient')
                restored=store.load();self.arlen['inventory']['manaDrinkCount']=0
                self.assertEqual(self.turn(restored,now=100,persist=store.save),'conjure_requested')
                self.assertEqual(len(self.calls),1)


if __name__=='__main__':unittest.main()
