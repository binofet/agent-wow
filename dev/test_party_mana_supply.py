"""Exercise the accepted Lua supply policy and real adapter, with fake game RPCs."""
import copy
import hashlib
import os
from pathlib import Path
import subprocess
import unittest

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
        fields=lambda values:[dict(name=name,kind=kind) for name,kind in values]
        methods=[dict(name='buddy-actions.create_item',effect='mutation',inputType='CreateItem')]
        methods.append(dict(name='buddy-trade.destroy_junk',effect='mutation',inputType='Junk'))
        if self.gift_available:methods.append(dict(name='buddy-trade.water_gift',effect='mutation',inputType='Gift'))
        return dict(sessionId=view['actions']['sessionId'],characterGuid=guid,revision='c'*64,methods=methods,
            messages=dict(CreateItem=dict(fields=fields([('sessionId','string'),('selfGuid','uint64'),('spellId','uint32')])),
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
            view['trade'].update(waterGiftArmed=True,partnerGuid=params['partnerGuid'],revision=str(int(view['trade']['revision'])+1))
            return copy.deepcopy(view['trade'])
        if method=='buddy-trade.destroy_junk':return copy.deepcopy(view['trade'])
        self.assertEqual(method,'buddy-actions.create_item')
        entry=next((row['createdItemEntry'] for row in self.toolkit['spells'] if row['spellId']==params['spellId']),0)
        return dict(actions,lastSpellOperation=dict(capability='create_item',spellId=params['spellId'],phase='waiting_server',
            transportWritten=True,createdItemEntry=entry,baselineInventoryRevision=view['world']['inventoryRevision']))

    def turn(self,state=None,now=1000,bridge=None):
        return supply.maintain_supply(self.rpc,self.observations,state if state is not None else {},
            bridge=bridge or self.bridge,now_ms=now)

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

    def test_receiver_reservation_requires_new_exact_partner_receipt_and_current_body_before_offer(self):
        carried(self.mira,2136,8);state={}
        self.assertEqual(self.turn(state),'recipient_water_reservation_requested')
        receipt=state['last']['result'];receipt['partnerGuid']='8'
        self.assertEqual(self.turn(state),'recipient_reservation_unconfirmed')
        receipt['partnerGuid']='4';receipt['revision']='2'
        self.assertEqual(self.turn(state),'recipient_reservation_unconfirmed')
        receipt['revision']='3';self.arlen['trade']['partnerGuid']='8'
        self.assertEqual(self.turn(state),'awaiting_recipient_reservation_observation')
        self.arlen['trade']['partnerGuid']='4';self.arlen['inventory']['sessionId']='old-body'
        self.assertEqual(self.turn(state),'awaiting_current_recipient_body')
        self.assertEqual(len(self.calls),1)
        self.arlen['inventory']['sessionId']='aw-1'
        self.assertEqual(self.turn(state),'water_gift_offer_requested');self.assertEqual(len(self.calls),2)

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


if __name__=='__main__':unittest.main()
