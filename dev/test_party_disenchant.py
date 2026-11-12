"""Automatic DE uses only simulated actual-client observations and receipts."""
import copy
import importlib.machinery
import importlib.util
from pathlib import Path
import tempfile
import unittest


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
            return dict(self.prof(), disenchantItem=dict(bag=0, slot=2, itemGuid=str(SOURCE), entry=5000,
                stackCount=1, templateKnown=True, quality=2, itemClass=4, ready=self.ready))
        if method == 'buddy-professions.attempt':
            assert self.journal and self.journal.data['attempts'][-1]['phase'] == 'reserved'
            assert self.journal.data['attempts'][-1]['request'] == params
            self.operation = dict(operationId='real-op', requestId=params['requestId'], skillId=333, spellId=13262,
                targetGuid=str(SOURCE), targetEntry=5000, sourceBag=0, sourceSlot=2,
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
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(); self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name).resolve()/'de.json'
        self.fixture, self.clock = Fixture(), Clock()
    def worker(self, journal, **kwargs):
        self.fixture.journal = journal
        return m.Worker(self.fixture.client(journal.url), journal, m.Scanner(self.fixture.client), guard=lambda: None,
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


if __name__ == '__main__': unittest.main()
