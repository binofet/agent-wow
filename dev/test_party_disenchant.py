"""Automatic DE uses only simulated actual-client observations and receipts."""
import copy
import hashlib
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


def load(name, filename):
    loader = importlib.machinery.SourceFileLoader(name, str(Path(__file__).with_name(filename)))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    module = importlib.util.module_from_spec(spec); loader.exec_module(module); return module


m = load('tested_party_disenchant', 'party-disenchant')
f = load('disenchant_inventory_fakes', 'test_party_gear.py')
SOURCE = 0x4000000000009871
CASTERS = (1 << 4) | (1 << 7) | (1 << 8)


def template(entry=5000, **more):
    return dict(dict(itemId=entry, available=True, name='Actual green robe', itemClass=4, subclass=1,
        inventoryType=20, quality=2, itemLevel=5, requiredLevel=5, allowableClass=CASTERS,armorComparisonKnown=True,
        allowableRace=0xffffffff, armor=10, stats=[dict(type=5, value=1)]), **more)


class Clock:
    value = 100.
    def now(self): return self.value
    def sleep(self, value): self.value += value


class Fixture:
    def __init__(self):
        self.calls, self.inventories, self.worlds, self.journals = [], {}, {}, {}
        self.operation, self.active, self.ready, self.mode, self.before_call = None, False, True, 'complete', None
        self.owner_templates={}
        self.metadata = {5000: template(), 5001: template(5001, quality=0, itemLevel=8, armor=30, stats=[dict(type=5, value=3)])}
        for guid, (_, klass) in m.PROFILES.items():
            inv = f.initial(); inv.update(selfGuid=str(guid), sessionId=f'body{guid}', inventoryRevision='11', revision='5')
            inv['inventoryBags'][0]['slots'] = [dict(bag=0, slot=i) for i in range(16)]
            inv['items'] = []; inv['equipment'] = [dict(slot=i, slotKnown=True) for i in range(19)]
            inv['equipment'][4].update(itemGuid=str(0x4000000000010000+guid), entry=5001, stackCount=1,
                metadataKnown=True, enhancementsKnown=True, durabilityKnown=True, durability=30, maxDurability=30)
            self.inventories[guid] = inv
            self.worlds[guid] = dict(self=dict(guid=str(guid), present=True, aliveKnown=True, alive=True,
                unitFlagsKnown=True,healthKnown=True,health=100,maxHealth=100,
                classKnown=True, classId=klass, raceKnown=True, raceId=1, levelKnown=True, level=10),
                selfEquipmentKnown=True, selfEquipmentRevision='3',selfSkillsKnown=True,selfSkillsRevision='1',
                selfSkills=[dict(slot=i,skillId=415 if i==0 else 0,value=75 if i==0 else 0,maxValue=75 if i==0 else 0) for i in range(128)],
                selfQuestLogKnown=True,selfQuestLogRevision='1',selfQuestLog=[dict(slot=i) for i in range(25)])
            self.journals[guid] = dict(sessionId=f'body{guid}', questLogRevision='1', entries=[])
        self.source = dict(bag=0, slot=2, itemGuid=str(SOURCE), entry=5000, stackCount=1,
            metadataKnown=True, enhancementsKnown=True)
        self.inventories[4]['inventoryBags'][0]['slots'][2] = copy.deepcopy(self.source)
        self.group = dict(known=True, inGroup=True, members=[dict(guid=str(g), online=True) for g in sorted(m.PARTY)])
        self.owner_feed = dict(available=True, bodySessionId='LZCMT4RDFLHXKV326WNW24YD3W', addonSession='123456789abc', revision=2)
        self.owner_feed['ownerGear']=dict(available=True,gearKnown=True,equipmentRevision='1',
            classKnown=True,classId=1,raceKnown=True,raceId=1,levelKnown=True,level=9,
            addonSession='123456789abc',revision=2,proficienciesKnown=True,
            proficiencies=[dict(skillId=415,known=True)],slots=[dict(slot=i,slotKnown=True) for i in range(19)])
        self.backend = dict(sessionId='body4', selfGuid='4', revision='7', skillsKnown=True, spellsKnown=True,
            skillsRevision='9', inventoryKnown=True, inventoryRevision='11')
        self.journal = None
    def client(self, url): return Client(self, url)
    def prof(self):
        state = copy.deepcopy(self.backend); state['active'] = self.active
        if self.operation: state['operation'] = copy.deepcopy(self.operation)
        return state
    def call(self, guid, method, params):
        self.calls.append((guid, method, copy.deepcopy(params)))
        if self.before_call: self.before_call(guid, method, params)
        if method == 'buddy-inventory.state': return copy.deepcopy(self.inventories[guid])
        if method == 'buddy-world.snapshot': return copy.deepcopy(self.worlds[guid])
        if method == 'buddy-party.state':
            group=copy.deepcopy(self.group)
            for row in group['members']:row['self']=int(row['guid'])==guid
            return dict(sessionId=f'body{guid}', group=group)
        if method == 'buddy-quests.journal':
            state=copy.deepcopy(self.journals[guid])
            state['requestedTemplates']=[copy.deepcopy(self.owner_templates.get(q,dict(questId=q))) for q in params.get('requestedQuestIds',[])]
            return state
        if method == 'buddy-chat.owner_feed': return copy.deepcopy(self.owner_feed)
        if method == 'buddy-trade.lookup_items':
            return dict(sessionId='body4', items=[copy.deepcopy(self.metadata[i]) for i in params['itemIds']])
        if method == 'buddy-professions.state': return self.prof()
        if method == 'buddy-professions.inspect_disenchant':
            return dict(self.prof(), disenchantItem=dict(bag=0, slot=2, itemGuid=str(SOURCE), entry=self.source['entry'],
                stackCount=1, templateKnown=True, quality=2, itemClass=4, ready=self.ready))
        if method == 'buddy-professions.attempt':
            assert self.journal and self.journal.data['attempts'][-1]['phase'] == 'reserved'
            assert self.journal.data['attempts'][-1]['request'] == params
            self.operation = dict(operationId='real-op', requestId=params['requestId'], skillId=333, spellId=13262,
                targetGuid=str(SOURCE), targetEntry=self.source['entry'], sourceBag=0, sourceSlot=2,
                baselineSkillsRevision='9', baselineInventoryRevision='11', observedInventoryRevision='12',
                status='COMPLETED', leaseReleased=True, castTransportWritten=True, ownGoObserved=True,
                lootWindowObserved=True, lootReleaseObserved=True, inputConsumedObserved=True,
                outputInventoryObserved=True, lootSlotsRemoved=1, ownLootedItemsObserved=2)
            if self.mode == 'lost': raise m.Error('lost actual attempt reply')
            if self.mode == 'unknown': self.operation.update(status='OUTCOME_UNCONFIRMED', leaseReleased=False)
            if self.mode.startswith('omit_'): self.operation.pop(self.mode[5:], None)
            if self.mode == 'complete':
                self.inventories[4]['inventoryBags'][0]['slots'][2] = dict(bag=0,slot=2)
                self.inventories[4]['inventoryRevision']='12'
            return self.prof()
        raise AssertionError(method)


class Client:
    def __init__(self, fixture, url):
        self.fixture, self.url = fixture, url
        self.guid = next(g for g, (port, _) in m.PROFILES.items() if url == f'http://127.0.0.1:{port}/rpc')
    def call(self, method, params): return self.fixture.call(self.guid, method, params)


class DisenchantTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        binary = os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY')
        if not binary or not Path(binary).is_file() or not os.access(binary, os.X_OK):
            raise RuntimeError('build cmd/luapolicy and set BUDDY_LUAPOLICY_TEST_BINARY to its executable')
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(); self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name).resolve()/'de.json'
        self.fixture, self.clock = Fixture(), Clock()
    def worker(self, journal, scanner=None, **kwargs):
        self.fixture.journal = journal
        return m.Worker(self.fixture.client(journal.url), journal, scanner or m.Scanner(self.fixture.client), guard=lambda: None,
            now=self.clock.now, sleep=self.clock.sleep, **kwargs)
    def attempts(self): return [p for _, method, p in self.fixture.calls if method == 'buddy-professions.attempt']
    def scan(self): return m.Scanner(self.fixture.client).scan(self.fixture.inventories[4])
    def test_all_actual_eligible_party_armor_is_dominated_then_one_normal_de_receipt(self):
        with m.Journal(self.path) as journal:
            result = self.worker(journal).run()
            self.assertEqual(result['phase'], 'complete'); self.assertEqual(result['newConfirmedOperations'], 1)
            self.assertEqual(len(self.attempts()), 1); self.assertEqual(journal.data['attempts'][0]['phase'], 'completed')
            proof=journal.data['attempts'][0]['partyProof']
            self.assertEqual(proof['chatModuleSessionId'],self.fixture.owner_feed['bodySessionId'])
            self.assertNotEqual(proof['chatModuleSessionId'],self.attempts()[0]['sessionId'])
        with m.Journal(self.path) as journal:
            self.worker(journal).run(); self.assertEqual(len(self.attempts()), 1)

    def retag_templates(self, candidate, current):
        offered = self.fixture.metadata.pop(5000)
        worn = self.fixture.metadata.pop(5001)
        offered['itemId'], worn['itemId'] = candidate, current
        self.fixture.metadata = {candidate: offered, current: worn}
        self.fixture.source['entry'] = candidate
        self.fixture.inventories[4]['inventoryBags'][0]['slots'][2]['entry'] = candidate
        for inventory in self.fixture.inventories.values():
            inventory['equipment'][4]['entry'] = current

    def test_sparse_and_max_uint32_templates_complete_exact_workflow_without_replay(self):
        for candidate, current in ((99000, 99001), (99000, 2**32-1), (2**32-1, 99000)):
            with self.subTest(candidate=candidate, current=current), tempfile.TemporaryDirectory() as folder:
                self.fixture = Fixture()
                self.retag_templates(candidate, current)
                path = Path(folder).resolve()/'de.json'
                with m.Journal(path) as journal:
                    result = self.worker(journal).run()
                    self.assertEqual((result['phase'], result['newConfirmedOperations']), ('complete', 1))
                    self.assertEqual(len(self.attempts()), 1)
                    row = journal.data['attempts'][0]
                    self.assertEqual(row['source']['entry'], candidate)
                    self.assertIs(type(row['source']['entry']), int)
                    self.assertEqual(row['receipt']['targetEntry'], candidate)
                    self.assertEqual(row['phase'], 'completed')
                    self.assertEqual(row['request']['targetGuid'], str(SOURCE))
                queried = {item for _, method, params in self.fixture.calls
                    if method == 'buddy-trade.lookup_items' for item in params['itemIds']}
                self.assertEqual(queried, {candidate, current})
                with m.Journal(path) as journal:
                    self.worker(journal).run()
                    self.assertEqual(journal.data['attempts'][0]['source']['entry'], candidate)
                    self.assertEqual(len(self.attempts()), 1)

    def test_string_keyed_templates_still_reject_duplicate_and_invalid_metadata(self):
        self.retag_templates(99000, 2**32-1)
        scanner = m.Scanner(self.fixture.client)
        code = (m.ROOT/'behaviors/disenchant.lua').read_text()
        source = m.town.ref(self.fixture.source)
        for bad_id in (0, -1, 2**32, 1.5, True, '99000'):
            with self.subTest(bad_id=bad_id):
                row = copy.deepcopy(self.fixture.metadata[99000]); row['itemId'] = bad_id
                result = scanner.bridge.evaluate(code, dict(phase='plan', sources=[source], metadata=[row]))
                self.assertEqual(result['reason'], 'party_item_metadata_pending')
                self.assertFalse(result['comparisons'])
        duplicate = self.fixture.metadata[99000]
        result = scanner.bridge.evaluate(code, dict(phase='plan', sources=[source],
            metadata=[duplicate, copy.deepcopy(duplicate)]))
        self.assertEqual(result['reason'], 'party_item_metadata_pending')
        self.assertFalse(result['comparisons'])
        original, comparisons = scanner.bridge, []
        class CapturingBridge:
            def evaluate(self, code, value):
                if value.get('phase') == 'compare': comparisons.append((code, copy.deepcopy(value)))
                return original.evaluate(code, value)
        scanner.bridge = CapturingBridge()
        self.assertIsNotNone(scanner.scan(self.fixture.inventories[4])[0])
        comparison_code, comparison = comparisons[0]
        comparison['current'].append(copy.deepcopy(comparison['current'][0]))
        result = original.evaluate(comparison_code, comparison)
        self.assertEqual(result['reason'], 'party_upgrade_or_unknown_comparison_retained')
        self.assertFalse(self.attempts())
    def test_owner_unknown_equipment_never_assumed_spare(self):
        self.fixture.metadata[5000]['allowableClass'] |= 1
        self.fixture.owner_feed.pop('ownerGear')
        source, reason, _ = self.scan()
        self.assertIsNone(source); self.assertEqual(reason, 'owner_gear_comparison_unavailable'); self.assertFalse(self.attempts())
    def test_chat_epoch_is_separate_and_changed_chat_or_core_epoch_blocks_destruction(self):
        for mode in ('chat','core','self_marker'):
            with self.subTest(mode=mode):
                self.fixture=Fixture();reads=[0]
                def callback(guid,method,params):
                    if method=='buddy-chat.owner_feed':
                        reads[0]+=1
                        if reads[0]==2 and mode=='chat':self.fixture.owner_feed['bodySessionId']='NEWCHATMODULESESSION'
                        if reads[0]==2 and mode=='core':self.fixture.inventories[4]['sessionId']='newbody'
                self.fixture.before_call=callback
                if mode=='self_marker':
                    original=self.fixture.call
                    def wrong_self(guid,method,params):
                        value=original(guid,method,params)
                        if method=='buddy-party.state':
                            for member in value['group']['members']:member['self']=member['guid']=='3'
                        return value
                    self.fixture.call=wrong_self
                if mode=='core':
                    # A final core-party read must report the new endpoint body.
                    original=self.fixture.call
                    def changed_body(guid,method,params):
                        value=original(guid,method,params)
                        if method=='buddy-party.state':value['sessionId']=self.fixture.inventories[guid]['sessionId']
                        return value
                    self.fixture.call=changed_body
                with self.assertRaises(m.Error):self.scan()
                self.assertFalse(self.attempts())
    def test_fresh_actual_owner_slot_feed_unlocks_comparison_without_fake_guids(self):
        self.fixture.metadata[5000]['allowableClass'] |= 1
        owner=dict(available=True,gearKnown=True,equipmentRevision='2',classId=1,raceId=1,level=9,
            classKnown=True,raceKnown=True,levelKnown=True,addonSession='123456789abc',revision=2,
            proficienciesKnown=True,proficiencies=[dict(skillId=415,known=True)],
            slots=[dict(slot=i,slotKnown=True) for i in range(19)])
        owner['slots'][4].update(entry=5001,metadataKnown=True,enhancementsKnown=True,
            durabilityKnown=True,durability=30,maxDurability=30)
        self.fixture.owner_feed['ownerGear']=owner
        source,reason,proof=self.scan()
        self.assertEqual(int(source['itemGuid']),SOURCE);self.assertFalse(reason)
        self.assertIn(3,proof['characters']);self.assertNotIn('itemGuid',owner['slots'][4])
        for change in ('better','uncached','enhanced','proficiency','partial','stale','replacement_incomplete'):
            with self.subTest(change=change):
                before=copy.deepcopy(self.fixture.owner_feed)
                if change=='better':owner['slots'][4]['entry']=0
                elif change=='uncached':owner['slots'][4]['metadataKnown']=False
                elif change=='enhanced':owner['slots'][4]['hasEnhancements']=True
                elif change=='proficiency':owner['proficiencies'][0]['known']=False
                elif change=='partial':owner['slots'].pop()
                elif change=='stale':self.fixture.owner_feed['ageMs']='15001'
                elif change=='replacement_incomplete':owner.update(available=False,ageMs='0')
                try:self.assertIsNone(self.scan()[0])
                except m.c.ConversationError:pass
                self.fixture.owner_feed=before;owner=self.fixture.owner_feed['ownerGear']
    def test_any_party_upgrade_future_gear_enhancement_or_quest_item_is_kept(self):
        for change in ('upgrade', 'future', 'enhancement', 'quest'):
            with self.subTest(change=change):
                self.fixture = Fixture()
                if change == 'upgrade': self.fixture.metadata[5001]['armor'] = 1
                if change == 'future': self.fixture.metadata[5000]['requiredLevel'] = 11
                if change == 'enhancement': self.fixture.source['hasEnhancements'] = True; self.fixture.inventories[4]['inventoryBags'][0]['slots'][2]['hasEnhancements'] = True
                if change == 'quest':
                    self.fixture.journals[8]['entries'] = [dict(questId=47, templateKnown=True, itemProtectionKnown=True,logStateKnown=True, targets=[dict(kind='ITEM', targetId=5000)])]
                    self.fixture.worlds[8]['selfQuestLog'][0]['questId']=47
                self.assertIsNone(self.scan()[0]); self.assertFalse(self.attempts())
    def test_unknown_metadata_party_body_or_equipment_never_dispatches(self):
        for change in ('metadata', 'group', 'sid', 'equipment', 'identity', 'owner_quests'):
            with self.subTest(change=change):
                self.fixture = Fixture()
                if change == 'metadata': self.fixture.metadata[5000].update(available=False, pending=True)
                if change == 'group': self.fixture.group['members'].pop()
                if change == 'sid': self.fixture.journals[8]['sessionId'] = 'foreign'
                if change == 'equipment': self.fixture.inventories[8]['equipment'].pop()
                if change == 'identity': self.fixture.worlds[8]['self']['classId'] = 1
                if change == 'owner_quests': self.fixture.owner_feed['quests'] = [dict(questId=999)]
                with self.assertRaises((m.Error, m.c.ConversationError)): self.scan()
                self.assertFalse(self.attempts())
    def test_reserved_displaced_gear_and_special_or_weapon_metadata_are_kept(self):
        scanner = m.Scanner(self.fixture.client)
        self.assertIsNone(scanner.scan(self.fixture.inventories[4], {SOURCE})[0])
        for field, value in (('flags', 1), ('hasSpell', True), ('requiredSkill', 197), ('itemClass', 2),('armorComparisonKnown',False)):
            self.fixture = Fixture(); self.fixture.metadata[5000][field] = value
            self.assertIsNone(self.scan()[0])

    def test_unknown_partial_or_changed_raw_quest_log_cannot_look_empty(self):
        for mode in ('unknown','partial','epoch','set','duplicate'):
            with self.subTest(mode=mode):
                self.fixture=Fixture();world=self.fixture.worlds[8]
                if mode=='unknown':world['selfQuestLogKnown']=False
                elif mode=='partial':world['selfQuestLog'].pop()
                elif mode=='epoch':world['selfQuestLogRevision']='2'
                elif mode=='set':world['selfQuestLog'][0]['questId']=47
                elif mode=='duplicate':world['selfQuestLog'][-1]['slot']=0
                with self.assertRaises(m.Error):self.scan()
                self.assertFalse(self.attempts())

    def test_owner_only_quests_are_queried_as_static_protection_not_companion_log(self):
        self.fixture.owner_feed['quests']=[dict(questId=332),dict(questId=353)]
        self.fixture.owner_templates={q:dict(questId=q,templateKnown=True,itemProtectionKnown=True) for q in (332,353)}
        self.assertIsNotNone(self.scan()[0])
        requests=[p for g,method,p in self.fixture.calls if g==4 and method=='buddy-quests.journal']
        self.assertEqual(requests[0]['requestedQuestIds'],[332,353])
        self.assertTrue(all(not q['entries'] for q in self.fixture.journals.values()))
        self.fixture.owner_templates[332]['targets']=[dict(kind='SOURCE_ITEM',targetId=5000)]
        self.assertIsNone(self.scan()[0])
        for field in ('templateKnown','itemProtectionKnown'):
            self.fixture.owner_templates[332][field]=False
            with self.assertRaises(m.Error):self.scan()
            self.fixture.owner_templates[332][field]=True
    def test_second_shared_read_change_is_pre_dispatch_abort_with_no_row(self):
        reads = [0]
        def callback(guid, method, params):
            if guid == 8 and method == 'buddy-inventory.state':
                reads[0] += 1
                if reads[0] == 2: self.fixture.inventories[8]['equipmentRevision'] = '4'; self.fixture.worlds[8]['selfEquipmentRevision'] = '4'
        self.fixture.before_call = callback
        with m.Journal(self.path) as journal:
            result = self.worker(journal).run()
            self.assertEqual(result['phase'], 'blocked'); self.assertFalse(journal.data['attempts']); self.assertFalse(self.attempts())
    def test_read_only_metadata_failure_has_no_reservation_or_ambiguous_operation(self):
        self.fixture.metadata[5000].update(available=False,pending=True)
        with m.Journal(self.path) as journal:
            result=self.worker(journal).run()
            self.assertEqual(result['phase'],'blocked');self.assertFalse(journal.data['attempts']);self.assertFalse(self.attempts())
    def test_ambiguous_attempt_persists_and_never_resends_after_restart_or_new_body(self):
        self.fixture.mode = 'lost'
        with m.Journal(self.path) as journal: self.assertEqual(self.worker(journal).run()['phase'], 'unconfirmed')
        self.fixture.operation = None
        with m.Journal(self.path) as journal: self.assertEqual(self.worker(journal).run()['phase'], 'unconfirmed')
        self.fixture.backend['sessionId'] = 'new-body'
        with m.Journal(self.path) as journal:
            self.assertEqual(self.worker(journal).run()['phase'], 'unconfirmed')
            self.assertEqual(journal.data['attempts'][0]['phase'], 'new_session_requires_review')
        self.assertEqual(len(self.attempts()), 1)
    def test_completion_requires_all_actual_material_input_cast_and_cleanup_proofs(self):
        for field in ('ownGoObserved', 'inputConsumedObserved', 'outputInventoryObserved', 'lootReleaseObserved', 'leaseReleased'):
            with self.subTest(field=field), tempfile.TemporaryDirectory() as directory:
                self.fixture = Fixture(); self.fixture.mode = 'omit_'+field
                with m.Journal(Path(directory).resolve()/'de.json') as journal:
                    self.assertEqual(self.worker(journal).run(max_seconds=.3)['phase'], 'unconfirmed')
                    self.assertEqual(len(self.attempts()), 1)
    def test_local_stop_after_reservation_is_not_dispatched_not_server_receipt(self):
        with m.Journal(self.path) as journal:
            stopped = [False]; save = journal.save
            def saving():
                save()
                if journal.data['attempts']: stopped[0] = True
            journal.save = saving
            result = self.worker(journal, stopped=lambda: stopped[0]).run()
            self.assertEqual(result['phase'], 'blocked'); self.assertFalse(self.attempts())
            self.assertEqual(journal.data['attempts'][0]['phase'], 'not_dispatched')
            self.assertNotIn('receipt', journal.data['attempts'][0])

    def test_lua_equal_armor_and_stats_dominate_but_future_level_and_proficiency_keep(self):
        for mode, reason in (
            ('equal', ''),
            ('future', 'future_party_equipment_retained'),
            ('unknown_skill', 'future_or_unknown_party_proficiency_retained'),
        ):
            with self.subTest(mode=mode):
                self.fixture = Fixture()
                self.fixture.metadata[5001].update(itemLevel=5, armor=10, stats=[dict(type=5, value=1)])
                if mode == 'future':
                    self.fixture.metadata[5000]['requiredLevel'] = 11
                elif mode == 'unknown_skill':
                    self.fixture.worlds[1]['selfSkillsKnown'] = False
                source, actual, _ = self.scan()
                self.assertEqual(actual, reason)
                self.assertEqual(source is not None, mode == 'equal')
                self.assertFalse(self.attempts())

    def test_lua_candidate_rotation_reaches_ninth_item_and_selected_recheck_does_not_rotate(self):
        bag = self.fixture.inventories[4]['inventoryBags'][0]['slots']
        for slot in range(9):
            bag[slot] = dict(self.fixture.source, slot=slot, itemGuid=str(SOURCE+slot), entry=6000+slot)
            self.fixture.metadata[6000+slot] = template(6000+slot, itemClass=2 if slot < 8 else 4)
        scanner = m.Scanner(self.fixture.client)
        first, _, _ = scanner.scan(self.fixture.inventories[4])
        self.assertIsNone(first)
        self.assertEqual(scanner.offset, 8)
        second, reason, proof = scanner.scan(self.fixture.inventories[4])
        self.assertFalse(reason)
        self.assertEqual(second['itemGuid'], str(SOURCE+8))
        self.assertEqual(scanner.offset, 16)
        selected, repeated_reason, repeated_proof = scanner.scan(self.fixture.inventories[4], selected=second)
        self.assertEqual(m.town.ref(selected), m.town.ref(second))
        self.assertEqual(repeated_reason, '')
        self.assertEqual(repeated_proof, proof)
        self.assertEqual(scanner.offset, 16)
        self.assertFalse(self.attempts())

    def test_full_carried_inventory_fits_real_lua_evaluator_budget(self):
        sources = []
        for bag in range(5):
            for slot in range(16 if bag == 0 else 36):
                sources.append(dict(bag=bag, slot=slot, itemGuid=str(SOURCE+len(sources)),
                    entry=5000, stackCount=1))
        self.assertEqual(len(sources), 160)
        scanner = m.Scanner(self.fixture.client)
        value = dict(phase='candidates', sources=list(reversed(sources)), offset=156)
        self.assertLess(len(m.runtime.packed(value, m.runtime.MAX_INPUT)), 32768)
        result = scanner.bridge.evaluate((m.ROOT/'behaviors/disenchant.lua').read_text(), value)
        self.assertEqual(result['sources'], sources[156:]+sources[:4])
        self.assertEqual(result['offset'], 164)
        self.assertEqual(result['reason'], '')
        self.assertFalse(self.attempts())

    def test_lua_failure_and_invalid_output_retain_source_without_reservation(self):
        original = (m.ROOT/'behaviors/disenchant.lua').read_text()
        wrapper = 'local base=(function()\n'+original+'\nend)()\n'
        policies = {
            'syntax': 'return { decide = function(',
            'timeout': 'return {decide=function(frame) while true do end end}',
            'bad_result': 'return {decide=function(frame) return {sources="unsafe",offset=8,reason=""} end}',
            'bad_reason': wrapper+'return {decide=function(frame,gear) '
                'if frame.phase=="compare" then return {reason=false} end return base.decide(frame,gear) end}',
            'foreign_source': wrapper+'return {decide=function(frame,gear) local result=base.decide(frame,gear); '
                'if frame.phase=="select" and result.source then result.source.itemGuid="'+str(SOURCE+1)+'" end '
                'return result end}',
        }
        for name, source in policies.items():
            with self.subTest(name=name), tempfile.TemporaryDirectory() as folder:
                policy = Path(folder)/'disenchant.lua'
                policy.write_text(source)
                self.fixture = Fixture()
                scanner = m.Scanner(self.fixture.client, policy=policy)
                with m.Journal(Path(folder).resolve()/'journal.json') as journal:
                    result = self.worker(journal, scanner=scanner).run()
                    self.assertEqual(result['phase'], 'blocked')
                    self.assertFalse(journal.data['attempts'])
                self.assertFalse(self.attempts())
                self.assertEqual(self.fixture.inventories[4]['inventoryBags'][0]['slots'][2]['itemGuid'], str(SOURCE))

    def test_missing_lua_binary_retains_source_and_never_uses_a_python_decision(self):
        with tempfile.TemporaryDirectory() as folder:
            with patch.dict(os.environ, BUDDY_LUAPOLICY_TEST_BINARY=str(Path(folder)/'absent')):
                scanner = m.Scanner(self.fixture.client)
                with m.Journal(Path(folder).resolve()/'journal.json') as journal:
                    result = self.worker(journal, scanner=scanner).run()
                    self.assertEqual(result['phase'], 'blocked')
                    self.assertFalse(journal.data['attempts'])
            self.assertFalse(self.attempts())

    def test_edited_lua_source_changes_decision_for_same_validated_facts(self):
        with tempfile.TemporaryDirectory() as folder:
            policy = Path(folder)/'disenchant.lua'
            original = (m.ROOT/'behaviors/disenchant.lua').read_text()
            policy.write_text(original)
            scanner = m.Scanner(self.fixture.client, policy=policy)
            self.assertIsNotNone(scanner.scan(self.fixture.inventories[4])[0])
            policy.write_text('local original=(function()\n'+original+'\nend)()\n'
                'return {decide=function(frame,gear)\n'
                '  if frame.phase=="compare" then return {reason="edited_lua_keeps_item"} end\n'
                '  return original.decide(frame,gear)\n'
                'end}')
            source, reason, _ = scanner.scan(self.fixture.inventories[4])
            self.assertIsNone(source)
            self.assertEqual(reason, 'edited_lua_keeps_item')
            self.assertFalse(self.attempts())

    def test_policy_edit_between_shared_comparisons_aborts_before_reservation(self):
        with tempfile.TemporaryDirectory() as folder:
            policy = Path(folder)/'disenchant.lua'
            original = (m.ROOT/'behaviors/disenchant.lua').read_text()
            policy.write_text(original)
            def edit_before_second_comparison(guid, method, params):
                if method == 'buddy-professions.inspect_disenchant':
                    policy.write_text(original+'\n-- next reviewed revision\n')
            self.fixture.before_call = edit_before_second_comparison
            scanner = m.Scanner(self.fixture.client, policy=policy)
            with m.Journal(Path(folder).resolve()/'journal.json') as journal:
                result = self.worker(journal, scanner=scanner).run()
                self.assertEqual(result['phase'], 'blocked')
                self.assertFalse(journal.data['attempts'])
            self.assertFalse(self.attempts())


    def copied_policies(self, folder):
        policy, gear = Path(folder)/'disenchant.lua', Path(folder)/'gear.lua'
        policy.write_bytes((m.ROOT/'behaviors/disenchant.lua').read_bytes())
        gear.write_bytes((m.ROOT/'behaviors/gear.lua').read_bytes())
        return policy, gear

    def test_same_scanner_actual_shared_class_profile_changes_selection_and_proof(self):
        policy, gear = self.copied_policies(self.temp.name)
        original_de, original_gear = policy.read_bytes(), gear.read_text()
        scanner = m.Scanner(self.fixture.client, policy=policy, gear_policy=gear)
        source, reason, proof = scanner.scan(self.fixture.inventories[4])
        self.assertEqual(m.town.ref(source), m.town.ref(self.fixture.source))
        self.assertEqual(reason, '')
        self.assertEqual(proof['policySHA256'], hashlib.sha256(original_de).hexdigest())
        self.assertEqual(proof['gearSHA256'], hashlib.sha256(original_gear.encode()).hexdigest())
        # This edits the actual shared scorer's priest profile, not a DE stub.
        # Worn intellect 3 versus carried 1 is a negative gain under this profile.
        self.assertIn('M.class_profiles = {}', original_gear)
        gear.write_text(original_gear.replace('M.class_profiles = {}',
            'M.class_profiles = {[5]={statWeights={[5]=-100},armorWeight=0}}', 1))
        source, reason, _ = scanner.scan(self.fixture.inventories[4])
        self.assertIsNone(source)
        self.assertEqual(reason, 'party_upgrade_or_unknown_comparison_retained')
        self.assertEqual(policy.read_bytes(), original_de)
        gear.write_text(original_gear)
        source, reason, restored = scanner.scan(self.fixture.inventories[4])
        self.assertIsNotNone(source)
        self.assertEqual(reason, '')
        self.assertEqual(restored, proof)
        self.assertFalse(self.attempts())

    def test_shared_gear_revision_change_between_scans_blocks_before_reservation(self):
        policy, gear = self.copied_policies(self.temp.name)
        original = gear.read_text()
        scanner = m.Scanner(self.fixture.client, policy=policy, gear_policy=gear)
        proofs, scan = [], scanner.scan
        def record(*args, **kwargs):
            result = scan(*args, **kwargs)
            proofs.append(copy.deepcopy(result))
            return result
        scanner.scan = record
        def edit(guid, method, params):
            if method == 'buddy-professions.inspect_disenchant':
                gear.write_text(original+'\n-- freshly reviewed shared gear revision\n')
        self.fixture.before_call = edit
        with m.Journal(self.path) as journal:
            result = self.worker(journal, scanner=scanner).run()
            self.assertEqual((result['phase'], result['reason']), ('blocked', 'party_comparison_changed'))
            self.assertFalse(journal.data['attempts'])
        self.assertEqual(len(proofs), 2)
        self.assertTrue(all(row[0] is not None and row[1] == '' for row in proofs))
        self.assertEqual(proofs[0][2]['policySHA256'], proofs[1][2]['policySHA256'])
        self.assertNotEqual(proofs[0][2]['gearSHA256'], proofs[1][2]['gearSHA256'])
        self.assertFalse(self.attempts())

    def test_missing_or_invalid_shared_gear_keeps_comparison_without_reservation(self):
        for mode in ('missing', 'syntax', 'missing_preview'):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as folder:
                self.fixture = Fixture()
                gear = Path(folder)/'gear.lua'
                if mode == 'syntax': gear.write_text('return {preview=function(')
                elif mode == 'missing_preview': gear.write_text('return {}')
                scanner = m.Scanner(self.fixture.client, gear_policy=gear)
                with m.Journal(Path(folder).resolve()/'de.json') as journal:
                    result = self.worker(journal, scanner=scanner).run()
                    self.assertEqual(result['phase'], 'blocked')
                    self.assertFalse(journal.data['attempts'])
                self.assertFalse(self.attempts())
                self.assertEqual(self.fixture.inventories[4]['inventoryBags'][0]['slots'][2]['itemGuid'], str(SOURCE))

    def test_unused_candidate_and_slotless_plans_do_not_read_shared_gear(self):
        for mode in ('no_candidates', 'not_ordinary_armor', 'unsupported_slot'):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as folder:
                self.fixture = Fixture()
                if mode == 'no_candidates':
                    self.fixture.inventories[4]['inventoryBags'][0]['slots'][2] = dict(bag=0, slot=2)
                elif mode == 'not_ordinary_armor': self.fixture.metadata[5000]['itemClass'] = 2
                else: self.fixture.metadata[5000]['inventoryType'] = 99
                missing = Path(folder)/'absent-gear.lua'
                scanner = m.Scanner(self.fixture.client, gear_policy=missing)
                with patch.object(m.runtime, 'read_file', wraps=m.runtime.read_file) as reads:
                    with m.Journal(Path(folder).resolve()/'de.json') as journal:
                        result = self.worker(journal, scanner=scanner).run()
                        self.assertEqual(result['phase'], 'blocked')
                        self.assertFalse(journal.data['attempts'])
                self.assertNotIn(missing, [Path(call.args[0]) for call in reads.call_args_list])
                self.assertFalse(self.attempts())

    def test_old_pending_journal_receipt_settles_without_reading_changed_or_missing_gear(self):
        policy, gear = self.copied_policies(self.temp.name)
        scanner = m.Scanner(self.fixture.client, policy=policy, gear_policy=gear)
        self.fixture.mode = 'lost'
        with m.Journal(self.path) as journal:
            self.assertEqual(self.worker(journal, scanner=scanner).run()['phase'], 'unconfirmed')
            # Pre-shared-gear journals remain valid historical reservations.
            journal.data['attempts'][0]['partyProof'].pop('gearSHA256')
            journal.save()
            request = copy.deepcopy(journal.data['attempts'][0]['request'])
        operation = copy.deepcopy(self.fixture.operation)
        gear.write_text('return {preview=function() error("must not run for retained receipt") end}')
        self.fixture.operation = None
        with patch.object(m.runtime, 'read_file', wraps=m.runtime.read_file) as reads:
            with m.Journal(self.path) as journal:
                self.assertEqual(self.worker(journal, scanner=scanner).run()['phase'], 'unconfirmed')
                self.assertEqual(journal.data['attempts'][0]['request'], request)
            gear.unlink()
            self.fixture.operation = operation
            self.fixture.inventories[4]['inventoryBags'][0]['slots'][2] = dict(bag=0, slot=2)
            self.fixture.inventories[4]['inventoryRevision'] = '12'
            with m.Journal(self.path) as journal:
                result = self.worker(journal, scanner=scanner).run()
                self.assertEqual((result['phase'], result['newConfirmedOperations']), ('complete', 1))
                self.assertEqual(journal.data['attempts'][0]['request'], request)
                self.assertEqual(journal.data['attempts'][0]['receipt'], operation)
            with m.Journal(self.path) as journal:
                self.assertEqual(self.worker(journal, scanner=scanner).run()['phase'], 'blocked')
                self.assertEqual(journal.data['attempts'][0]['phase'], 'completed')
        self.assertNotIn(gear, [Path(call.args[0]) for call in reads.call_args_list])
        self.assertEqual(len(self.attempts()), 1)

    def test_pending_unknown_and_replaced_body_never_replay_after_shared_gear_edit(self):
        _, gear = self.copied_policies(self.temp.name)
        scanner = m.Scanner(self.fixture.client, gear_policy=gear)
        self.fixture.mode = 'unknown'
        with m.Journal(self.path) as journal:
            self.assertEqual(self.worker(journal, scanner=scanner).run(max_seconds=.3)['phase'], 'unconfirmed')
            request = copy.deepcopy(journal.data['attempts'][0]['request'])
        gear.write_text('return {}')
        self.fixture.backend['sessionId'] = 'replacement-body'
        with patch.object(m.runtime, 'read_file', wraps=m.runtime.read_file) as reads:
            with m.Journal(self.path) as journal:
                self.assertEqual(self.worker(journal, scanner=scanner).run()['phase'], 'unconfirmed')
                self.assertEqual(journal.data['attempts'][0]['phase'], 'new_session_requires_review')
                self.assertEqual(journal.data['attempts'][0]['request'], request)
        self.assertNotIn(gear, [Path(call.args[0]) for call in reads.call_args_list])
        self.assertEqual(len(self.attempts()), 1)

    def test_shared_signed_stat_dominance_keeps_any_component_loss(self):
        for carried, worn, eligible in ((-1, -2, False), (-2, -1, True), (-2, -2, True), (1, 0, False)):
            with self.subTest(carried=carried, worn=worn):
                self.fixture = Fixture()
                self.fixture.metadata[5000]['stats'] = [dict(type=5, value=carried)]
                self.fixture.metadata[5001]['stats'] = [dict(type=5, value=worn)]
                source, reason, _ = self.scan()
                self.assertEqual(source is not None, eligible)
                self.assertEqual(reason, '' if eligible else 'party_upgrade_or_unknown_comparison_retained')
                self.assertFalse(self.attempts())

    def test_unknown_duplicate_or_invalid_shared_stats_keep_without_native_attempt(self):
        for location in (5000, 5001):
            for mode in ('unsupported', 'duplicate', 'nonfinite', 'nonnumeric'):
                with self.subTest(location=location, mode=mode), tempfile.TemporaryDirectory() as folder:
                    self.fixture = Fixture()
                    stats = [dict(type=2, value=1)] if mode == 'unsupported' else (
                        [dict(type=5, value=1), dict(type=5, value=1)] if mode == 'duplicate' else
                        [dict(type=5, value=float('inf') if mode == 'nonfinite' else '1')])
                    self.fixture.metadata[location]['stats'] = stats
                    with m.Journal(Path(folder).resolve()/'de.json') as journal:
                        result = self.worker(journal).run()
                        self.assertEqual(result['phase'], 'blocked')
                        self.assertFalse(journal.data['attempts'])
                    self.assertFalse(self.attempts())


if __name__ == '__main__': unittest.main()
