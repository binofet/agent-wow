"""Actual accepted Lua chooses cleanup; fake RPCs expose only native facts."""
import copy
import hashlib
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import party_bag_pressure as bags


ROOT = Path(__file__).resolve().parents[1]
GUID = 0x4000000000000000


class BagPressureTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        parts = [('disposal', 'item-disposal.lua'), ('gear', 'gear.lua'), ('supply', 'mana-supply.lua')]
        cls.source = ''.join('local ' + name + '=(function()\n' + (ROOT / 'behaviors' / file).read_text()
            + '\nend)()\n' for name, file in parts)
        cls.source += 'return {mana_supply_plan=function(input)return supply.plan(input,disposal,gear)end}'
        cls.bridge = bags.runtime.Bridge(os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY', ROOT / 'data/bin/luapolicy'),
            ROOT / 'modules', value_bytes=bags.VALUE_BYTES, execution_ms=1000)

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(dir=Path(tempfile.gettempdir()).resolve())
        self.addCleanup(self.temp.cleanup)
        self.store = bags.runtime.Store(Path(self.temp.name) / 'bag-state')
        self.store.__enter__(); self.addCleanup(self.store.__exit__, None, None, None)
        self.attempts, self.writes, self.calls = {}, [], []
        self.code, self.generation, self.effect = self.source, '1', 'mutation'
        self.on_read, self.lost, self.reply_body, self.persist_fails = None, False, 'aw-5', False
        slots = [dict(bag=0, slot=i, entry=900 if i==0 else 901 if i==1 else 117,
            stackCount=2 if i<2 else 1, itemGuid=str(GUID+1+i), metadataKnown=True,
            enhancementsKnown=True, hasEnhancements=False) for i in range(16)]
        layout = [dict(bag=i, slotKnown=True, contentsKnown=True, slotCount=16 if i==0 else 0,
            slots=slots if i==0 else []) for i in range(5)]
        equipment = [dict(slot=i, slotKnown=True) for i in range(19)]
        self.view = dict(world=dict(self=dict(guid='5', present=True, aliveKnown=True, alive=True,
                unitFlagsKnown=True, mapKnown=True, mapId=34),
            inventoryKnown=True, inventoryRevision='19', inventoryBags=layout,
            selfEquipmentKnown=True, selfEquipmentRevision='17', selfEquipment=equipment,
            selfQuestLogKnown=True, selfQuestLogRevision='7', selfQuestLog=[dict(slot=i) for i in range(25)],
            lootableCorpses=[dict(guid='123', present=True, positionKnown=True, mapKnown=True, mapId=34, x=1)]),
            actions=dict(sessionId='aw-5', selfGuid='5', position=dict(known=True, mapKnown=True, mapId=34)),
            inventory=dict(sessionId='aw-5', selfGuid='5', inventoryKnown=True, inventoryRevision='19',
                equipmentKnown=True, equipmentRevision='17', equipment=equipment, inventoryBags=layout,
                items=[dict(item=copy.deepcopy(row), metadataKnown=True, gearFacts={}) for row in slots],
                gearContext=dict(templates={})),
            party=dict(sessionId='aw-5'), journal=dict(sessionId='aw-5', questLogRevision='7', entries=[]),
            trade=dict(sessionId='aw-5', phase='IDLE', revision='20', backpackKnown=True, backpackRevision='21'),
            loot=dict(sessionId='aw-5', enabled=True, inventoryPaused=True))
        self.metadata = {entry: dict(itemId=entry, available=True, itemClass=kind, subclass=0,
            sellPrice=price, rawTemplateFacts={}) for entry, kind, price in [(900,15,5),(901,15,8),(117,0,50)]}
        self.views = {8088: self.view}

    def revision(self):
        return hashlib.sha256(self.code.encode()).hexdigest()

    def catalog(self):
        fields = [('sessionId','string'),('revision','uint64'),('backpackRevision','uint64'),
            ('backpackSlot','uint32'),('inventoryRevision','uint64'),('sourceBag','uint32'),
            ('sourceSlot','uint32'),('expectedItemGuid','uint64')]
        return dict(sessionId='aw-5', characterGuid='5', methods=[dict(name='buddy-trade.destroy_junk',
            effect=self.effect, inputType='Junk')], messages=dict(Junk=dict(fields=[dict(name=n,kind=k) for n,k in fields])))

    def rpc(self, port, method, params):
        self.assertEqual(port, 8088)
        self.calls.append((method, copy.deepcopy(params)))
        if self.on_read:
            self.on_read(method, params)
        if method == 'session.policy_source':
            return dict(sessionId='aw-5', characterGuid='5', source=self.code,
                revision=self.revision(), generation=self.generation, sourceLimitBytes=8*1024*1024)
        if method == 'session.attach':
            return dict(sessionId=self.view['actions']['sessionId'], characterGuid='5',
                policy=dict(revision=self.revision(), generation=self.generation))
        if method == 'session.describe':
            return self.catalog()
        if method == 'buddy-trade.lookup_items':
            return dict(sessionId='aw-5', items=[copy.deepcopy(self.metadata[i]) for i in params['itemIds'] if i in self.metadata])
        if method == 'buddy-inventory.raw_gear_facts':
            return copy.deepcopy(self.view['inventory'])
        fields = {'buddy-actions.status':'actions', 'buddy-world.snapshot':'world',
            'buddy-inventory.state':'inventory', 'buddy-trade.state':'trade', 'buddy-loot.state':'loot'}
        if method in fields:
            return copy.deepcopy(self.view[fields[method]])
        self.assertEqual(method, 'buddy-trade.destroy_junk')
        durable = self.store.load()['attempts']['8088']
        self.assertEqual(durable['last']['outcome'], 'transport_unconfirmed')
        self.assertEqual(durable['memory']['pending']['source']['itemGuid'], params['expectedItemGuid'])
        self.writes.append(copy.deepcopy(params))
        if self.lost:
            raise OSError('write response lost')
        return dict(self.view['trade'], sessionId=self.reply_body)

    def persist(self, attempts):
        if self.persist_fails:
            raise OSError('durable save failed')
        self.store.save(dict(version=1, attempts=attempts))

    def tick(self, now=100, *, persist=True):
        return bags.step(self.rpc, self.views, self.attempts, None, 8088, 5, now,
            persist=self.persist if persist else None, bridge=self.bridge)

    def confirm(self):
        pending = self.attempts['8088']['memory']['pending']
        source = pending['source']
        self.view['trade'].update(revision='22', lastJunkDisposal=dict(itemGuid=source['itemGuid'],
            entry=source['entry'], count=source['stackCount'], backpackRevision=pending['sourceRevision'],
            status='SERVER_SLOT_REMOVAL_CONFIRMED'))
        self.view['world']['inventoryRevision'] = self.view['inventory']['inventoryRevision'] = '20'
        self.view['inventory']['inventoryBags'][0]['slots'][source['slot']] = dict(bag=0, slot=source['slot'])

    def test_lua_ranks_all_observed_templates_and_executes_exact_owned_stack(self):
        self.assertEqual(self.tick(), 'cleanup_requested')
        self.assertEqual(self.writes, [dict(sessionId='aw-5', revision='20', backpackRevision='21',
            backpackSlot=0, expectedItemGuid=str(GUID+1))])
        lookup = [p['itemIds'] for name,p in self.calls if name=='buddy-trade.lookup_items']
        self.assertEqual(set(sum(lookup,[])), {900,901,117})

    def test_actor_source_edit_changes_ranking_in_same_body_without_python_filter(self):
        self.code = self.source.replace('value<best.value', 'value>best.value')
        self.generation = '2'
        self.assertEqual(self.tick(), 'cleanup_requested')
        self.assertEqual(self.writes[0]['backpackSlot'], 1)

    def test_lua_pressure_range_tuning_and_water_only_pressure_are_distinct(self):
        self.view['world']['lootableCorpses'][0]['x'] = 20
        self.assertEqual(self.tick(), 'no_current_bag_pressure')
        self.code = self.source.replace('loot_yards=12', 'loot_yards=25')
        self.assertEqual(self.tick(101), 'cleanup_requested')
        self.assertEqual(len(self.writes),1)

    def test_receipt_alone_cannot_retire_and_only_exact_new_slot_removal_confirms(self):
        self.tick()
        pending = copy.deepcopy(self.attempts['8088']['memory']['pending'])
        self.view['trade']['lastJunkDisposal'] = dict(itemGuid=pending['source']['itemGuid'],
            entry=900,count=2,backpackRevision='21',status='SERVER_SLOT_REMOVAL_CONFIRMED')
        self.assertEqual(self.tick(101), 'awaiting_cleanup_effect')
        self.confirm()
        self.assertEqual(self.tick(102), 'server_confirmed_cleanup')
        self.assertNotIn('pending', self.attempts['8088']['memory'])
        self.assertEqual(len(self.writes),1)

    def test_lost_response_is_durable_across_reconstruction_and_never_replays(self):
        self.lost = True
        self.assertEqual(self.tick(), 'cleanup_outcome_unconfirmed')
        before = copy.deepcopy(self.store.load()['attempts'])
        pending = copy.deepcopy(before['8088']['memory']['pending'])
        self.attempts = before
        self.assertEqual(self.tick(1000), 'cleanup_outcome_unconfirmed')
        self.assertEqual(len(self.writes),1)
        self.assertEqual(self.attempts['8088']['memory']['pending'], pending)

    def test_persist_failure_or_missing_store_never_enters_mutation(self):
        for fail in (False,True):
            with self.subTest(fail=fail):
                self.attempts={};self.persist_fails=fail
                with self.assertRaises((OSError,bags.runtime.Error)):
                    self.tick(persist=fail)
                self.assertEqual(self.writes,[])

    def test_missing_old_consumer_or_installed_route_cannot_fall_back(self):
        original=self.code
        self.code='return {mana_supply_plan=function(input)return {memory={},phase="old_water_path"} end}'
        with self.assertRaisesRegex(bags.runtime.Error,'loot_pressure_consumer_required'):
            self.tick()
        self.code=original;self.effect='read'
        self.assertEqual(self.tick(),'cleanup_capability_unavailable')
        self.assertEqual(self.writes,[])

    def test_body_or_source_changes_after_selection_are_known_no_dispatch(self):
        reads = [0]
        def replace(method,_):
            if method=='session.attach':
                reads[0]+=1
                if reads[0]==2:
                    self.generation='2'
        self.on_read=replace
        self.assertEqual(self.tick(),'cleanup_not_dispatched')
        self.assertEqual(self.writes,[])
        self.assertEqual(self.store.load()['attempts']['8088']['last']['outcome'],'not_dispatched')

    def test_actual_source_slot_or_scene_change_refuses_before_write(self):
        def change(method,_):
            if method=='buddy-actions.status':
                self.view['world']['lootableCorpses']=[]
        self.on_read=change
        self.assertEqual(self.tick(),'cleanup_not_dispatched')
        self.assertEqual(self.writes,[])

    def test_quest_functional_enhanced_and_unknown_items_are_retained(self):
        for mode in ('quest','functional','enhanced','template_unknown'):
            with self.subTest(mode=mode):
                self.attempts={};self.writes=[]
                self.metadata[901]['itemClass']=0
                first=self.view['inventory']['inventoryBags'][0]['slots'][0]
                first['hasEnhancements']=mode=='enhanced'
                self.metadata[900]['rawTemplateFacts']={} if mode!='functional' else dict(startQuest=7)
                self.metadata[900]['available']=mode!='template_unknown'
                self.view['journal']['entries']=[]
                self.view['world']['selfQuestLog'][0]=dict(slot=0)
                if mode=='quest':
                    self.view['world']['selfQuestLog'][0]=dict(slot=0,questId=386)
                    self.view['journal']['entries']=[dict(slot=0,questId=386,logStateKnown=True,
                        templateKnown=True,itemProtectionKnown=True,targets=[dict(kind='ITEM',targetId=900,requiredCount=2)])]
                self.assertEqual(self.tick(),'cleanup_facts_unknown_or_items_retained')
                self.assertEqual(self.writes,[])

    def test_body_replacement_and_legacy_unknown_do_not_clear_pending_custody(self):
        self.lost=True;self.tick();before=copy.deepcopy(self.attempts)
        self.view['actions']['sessionId']='aw-new'
        self.view['inventory']['sessionId']=self.view['trade']['sessionId']=self.view['party']['sessionId']='aw-new'
        with self.assertRaises(bags.runtime.Error):self.tick(200)
        self.assertEqual(self.attempts,before)
        self.attempts={8088:dict(sessionId='old',itemGuid=GUID+1)}
        self.assertEqual(self.tick(201),'legacy_disposal_custody_retained_no_retry')
        self.assertEqual(len(self.writes),1)

    def test_current_native_unknown_blocks_even_when_private_memory_is_empty(self):
        self.view['trade']['junkOperations']=[dict(itemGuid=str(GUID+99),status='TRANSPORT_UNCONFIRMED_NO_RETRY')]
        self.assertEqual(self.tick(),'waiting_for_safe_cleanup')
        self.assertEqual(self.writes,[])

    def test_exact_slot_replacement_after_lua_selection_sends_nothing(self):
        def change(method,_):
            if method=='buddy-actions.status':
                self.view['inventory']['inventoryBags'][0]['slots'][0]['itemGuid']=str(GUID+100)
        self.on_read=change
        self.assertEqual(self.tick(),'cleanup_not_dispatched')
        self.assertEqual(self.writes,[])

    def gray_armor(self):
        self.metadata[901]['itemClass']=0
        armor=dict(itemId=900,available=True,itemClass=4,subclass=1,inventoryType=9,quality=0,
            sellPrice=5,armorComparisonKnown=True,armor=9,stats=[],rawTemplateFacts=dict(maxDurability=10))
        self.metadata[900]=armor
        self.view['inventory']['inventoryBags'][0]['slots'][0]['stackCount']=1
        self.view['inventory']['items'][0]['item']['stackCount']=1
        worn=dict(slot=8,slotKnown=True,itemGuid=str(GUID+999),entry=100,stackCount=1,metadataKnown=True,
            enhancementsKnown=True,hasEnhancements=False,durabilityKnown=True,durability=10,maxDurability=10)
        self.view['world']['selfEquipment'][8]=worn
        self.view['inventory']['equipment']=self.view['world']['selfEquipment']
        self.view['inventory']['items'][0]['gearFacts']=dict(enhancementsKnown=True,hasEnhancements=False,
            durabilityKnown=True,durability=10,maxDurability=10,destinations=[dict(equipmentSlot=8,
                expectedEquippedItemGuid=worn['itemGuid'],eligible=True)])
        self.view['inventory']['inventoryBags'][0]['slots'][0].update(durabilityKnown=True,durability=10,maxDurability=10)
        self.view['inventory']['gearContext']['templates']['100']=dict(itemId=100,available=True,itemClass=4,
            subclass=1,inventoryType=9,armorComparisonKnown=True,armor=15,stats=[],resistances=[0]*6)

    def test_gray_armor_uses_shared_gear_comparison(self):
        self.gray_armor()
        self.assertEqual(self.tick(),'cleanup_requested')
        self.assertEqual(self.writes[0]['expectedItemGuid'],str(GUID+1))

    def test_useful_or_physically_unknown_gray_armor_is_retained(self):
        self.gray_armor()
        self.metadata[900]['armor']=20
        self.assertEqual(self.tick(),'cleanup_facts_unknown_or_items_retained')
        self.view['inventory']['items'][0]['gearFacts'].pop('destinations')
        self.assertEqual(self.tick(106),'cleanup_facts_unknown_or_items_retained')
        self.assertEqual(self.writes,[])

    def test_actual_current_bundle_exports_same_cleanup_query(self):
        self.code=subprocess.check_output([sys.executable,'-B',str(ROOT/'dev/encounter-bundle')],text=True)
        self.assertEqual(self.tick(),'cleanup_requested')
        self.assertEqual(self.writes[0]['expectedItemGuid'],str(GUID+1))

    def test_changed_lua_action_must_match_its_exact_reserved_source(self):
        self.code=self.source.replace('expectedItemGuid=source.itemGuid', "expectedItemGuid='4611686018427387999'")
        with self.assertRaisesRegex(bags.runtime.Error,'cleanup_action_source_mismatch'):
            self.tick()
        self.assertEqual(self.writes,[])

    def test_advertised_native_request_and_context_guards_are_preserved(self):
        original=self.catalog
        def modern():
            catalog=original()
            catalog['messages']['Junk']['fields'] += [dict(name=n,kind=k) for n,k in
                [('requestId','string'),('expectedEquipmentRevision','uint64'),('expectedQuestLogRevision','uint64')]]
            return catalog
        self.catalog=modern
        self.assertEqual(self.tick(),'cleanup_requested')
        self.assertTrue(self.writes[0]['requestId'])
        self.assertEqual(self.writes[0]['expectedEquipmentRevision'],'17')
        self.assertEqual(self.writes[0]['expectedQuestLogRevision'],'7')


if __name__ == '__main__':
    unittest.main()
