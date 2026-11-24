"""Real bounded Lua decisions on synthetic quest-container observations only."""
import copy
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parent.parent
loader = importlib.machinery.SourceFileLoader('quest_gather_policy_host', str(ROOT/'dev/task-runtime'))
spec = importlib.util.spec_from_loader(loader.name, loader)
host = importlib.util.module_from_spec(spec)
loader.exec_module(host)
GUID = str(0xf110000000000000 | (161557 << 24) | 42)


def object_row(guid=GUID, **fields):
    return dict(dict(guid=guid, entry=161557, present=True, typeKnown=True, gameObjectType=3,
        mapKnown=True, mapId=0, positionKnown=True, positionRevision='17', flagsKnown=True,
        flags=0, x=2, y=0, z=0), **fields)


def recipe(**fields):
    return dict(dict(objectEntry=161557, questId=3904, itemId=11119, count=1, openSpell=6478), **fields)


def observation():
    quests = [dict(slot=i) for i in range(25)]
    quests[0]['questId'] = 3904
    return dict(sessionId='gather-policy-fixture', selfGuid='6', visitId='3',
        self=dict(guid='6', present=True, aliveKnown=True, alive=True, healthKnown=True, health=100,
            mapKnown=True, mapId=0, unitFlagsKnown=True, unitFlags=0,
            movementFlagsKnown=True, movementFlags=0, extraMovementFlags=0),
        actions=dict(position=dict(known=True, mapKnown=True, mapId=0, revision='11', x=0, y=0, z=0)),
        inventoryKnown=True, inventoryRevision='13', generalFreeSlots=4, state=dict(enabled=True),
        gatherCandidatesKnown=True, questLogKnown=True, questLogRevision='19', questLog=quests,
        questObjects=[object_row()], gatherRecipes=[recipe()], gatherAttemptedSources=[], retiredSources=[])


class GatherTargetPolicyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        binary = os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY')
        if not binary or not Path(binary).is_file() or not os.access(binary, os.X_OK):
            raise RuntimeError('set BUDDY_LUAPOLICY_TEST_BINARY to the ordinary local Lua evaluator')
        cls.bridge = host.Bridge(binary, ROOT/'modules')
        cls.canonical = (ROOT/'behaviors/corpse-loot.lua').read_text()

    def setUp(self):
        self.source = self.canonical
        self.view = observation()
        self.checkpoint = None
        self.sequence = 0

    def tick(self, *, receipt=None, pending=None, quiet=True, urgent=False, visit='3'):
        self.sequence += 1
        events = []
        if receipt is not None:
            events.append(dict(kind='action_receipt', receipt=copy.deepcopy(receipt)))
        events.append(dict(kind='corpse_loot_observation', sequence=str(self.sequence), value=copy.deepcopy(self.view)))
        frame = dict(session_id='gather-policy-fixture', cursor=str(self.sequence), pending=pending or [],
            input=dict(observationSequence=str(self.sequence), observationSequences=dict(corpse_loot=str(self.sequence)),
                quiet=quiet, urgent=urgent), events=events, encounter=dict(selfGuid='6', visitId=visit))
        if self.checkpoint is not None:
            frame['checkpoint'] = copy.deepcopy(self.checkpoint)
        source = 'local policy=(function()\n'+self.source+'\nend)()\n'+'''
return {decide=function(frame)
 if frame.checkpoint then assert(policy.import_state(frame.checkpoint)) end
 for _,event in ipairs(frame.events) do assert(policy.on_event(event)) end
 local decision=policy.decide(frame,frame.encounter,frame.input.quiet,frame.input.urgent)
 return {decision=decision,checkpoint=policy.export_state()}
end}
'''
        result = self.bridge.evaluate(source, frame)
        self.checkpoint = result['checkpoint']
        return result['decision']

    def test_exact_selected_recipe_body_visit_and_revisions_are_preserved(self):
        action = self.tick()['action']
        self.assertEqual(action, dict(capability='loot_gather', sessionId='gather-policy-fixture', selfGuid='6',
            sourceGuid=GUID, questId=3904, itemId=11119, openSpell=6478, requestId='gather-1', visitId='3',
            expectedPositionRevision='11', expectedObjectRevision='17', expectedInventoryRevision='13',
            expectedQuestLogRevision='19', approach=False, observationSequence='1'))
        self.assertFalse(self.checkpoint['data']['seen'])

    def test_nearest_target_precedes_corpse_and_lua_edit_changes_next_choice(self):
        far = str(int(GUID)+1)
        self.view['questObjects'].append(object_row(far, x=7))
        self.view['corpses'] = [dict(guid='17379390962022744065', present=True, aliveKnown=True, alive=False,
            healthKnown=True, health=0, mapKnown=True, mapId=0, dynamicFlags=1,
            positionKnown=True, positionRevision='2', x=1, y=0, z=0)]
        self.assertEqual(self.tick()['action']['sourceGuid'], GUID)
        self.source = self.source.replace('gatherPreferNearest=true', 'gatherPreferNearest=false', 1)
        action = self.tick()['action']
        self.assertEqual((action['sourceGuid'], action['approach']), (far, True))

    def test_full_decimal_guid_ties_and_recipe_ties_ignore_observation_order(self):
        low, high = '9999999999999999999', '10000000000000000000'
        self.view['questObjects'] = [object_row(high), object_row(low)]
        self.view['questLog'][1]['questId'] = 3905
        self.view['gatherRecipes'] = [recipe(questId=3905, itemId=22222), recipe()]
        self.assertEqual((self.tick()['action']['sourceGuid'], self.tick()['action']['questId']), (low, 3904))
        self.view['questObjects'].reverse()
        self.view['gatherRecipes'].reverse()
        self.assertEqual((self.tick()['action']['sourceGuid'], self.tick()['action']['questId']), (low, 3904))

    def test_recipe_priority_is_editable_in_the_same_lua_without_go_selection(self):
        self.view['questLog'][1]['questId'] = 3905
        self.view['gatherRecipes'].append(recipe(questId=3905, itemId=22222))
        self.assertEqual(self.tick()['action']['questId'], 3904)
        edited = self.source.replace('a.questId<b.questId', 'a.questId>b.questId', 1)
        self.assertNotEqual(edited, self.source)
        self.source = edited
        action = self.tick()['action']
        self.assertEqual((action['questId'], action['itemId']), (3905, 22222))

    def test_policy_search_and_open_ranges_are_separate_from_corpse_tuning(self):
        self.view['questObjects'][0]['x'] = 4
        self.assertFalse(self.tick()['action']['approach'])
        self.source = self.source.replace('gatherOpenRange=4.5', 'gatherOpenRange=3', 1)
        self.assertTrue(self.tick()['action']['approach'])
        self.source = self.source.replace('gatherSearchRange=12', 'gatherSearchRange=2', 1)
        self.assertNotIn('action', self.tick())
        for old, new in (('gatherSearchRange=12', 'gatherSearchRange=13'),
                         ('gatherOpenRange=4.5', 'gatherOpenRange=5'), ('maxHeight=1.5', 'maxHeight=2')):
            with self.subTest(new=new):
                self.setUp(); self.source = self.source.replace(old, new, 1)
                self.assertNotIn('action', self.tick())

    def test_old_absent_and_unknown_gather_facts_cannot_start_gathering(self):
        for field in ('gatherCandidatesKnown', 'questObjects', 'gatherRecipes',
                      'questLogRevision', 'visitId', 'questLogKnown'):
            with self.subTest(field=field):
                self.setUp(); self.view.pop(field)
                self.assertNotIn('action', self.tick())
        for field in ('gatherCandidatesKnown', 'questLogKnown'):
            with self.subTest(unknown=field):
                self.setUp(); self.view[field] = False
                self.assertNotIn('action', self.tick())

    def test_explicit_known_frame_preserves_protojson_omitted_zeros_and_empty_history(self):
        self.view.pop('gatherAttemptedSources')
        self.view.pop('retiredSources')
        self.view['questLog'] = [dict(slot=i) if i else {} for i in range(25)]
        self.view['questLog'][0]['questId'] = 3904
        for key in ('mapId', 'unitFlags', 'movementFlags', 'extraMovementFlags'):
            self.view['self'].pop(key)
        for key in ('mapId', 'x', 'y', 'z'):
            self.view['actions']['position'].pop(key)
        for key in ('mapId', 'flags', 'y', 'z'):
            self.view['questObjects'][0].pop(key)
        self.assertEqual(self.tick()['action']['sourceGuid'], GUID)

    def test_old_observation_retains_ordinary_corpse_policy(self):
        for field in ('gatherCandidatesKnown', 'questObjects', 'gatherRecipes', 'gatherAttemptedSources'):
            self.view.pop(field)
        corpse = '17379390962022744065'
        self.view['corpses'] = [dict(guid=corpse, present=True, aliveKnown=True, alive=False,
            healthKnown=True, health=0, mapKnown=True, mapId=0, dynamicFlags=1,
            positionKnown=True, positionRevision='2', x=2, y=0, z=0)]
        action = self.tick()['action']
        self.assertEqual((action['capability'], action['corpseGuid']), ('loot_open', corpse))

    def test_every_supplied_row_is_validated_before_the_nearest_choice(self):
        changes = {
            'late_duplicate_object': lambda o: o['questObjects'].append(copy.deepcopy(o['questObjects'][0])),
            'late_malformed_object': lambda o: o['questObjects'].append(object_row(str(int(GUID)+1), x='2')),
            'late_malformed_recipe': lambda o: o['gatherRecipes'].append(recipe(questId=3905, count=False)),
            'duplicate_recipe': lambda o: o['gatherRecipes'].append(copy.deepcopy(o['gatherRecipes'][0])),
            'duplicate_quest_slot': lambda o: o['questLog'][-1].update(slot=0),
            'duplicate_active_quest': lambda o: o['questLog'][-1].update(questId=3904),
            'malformed_empty_quest': lambda o: o['questLog'][-1].update(questId=False),
            'short_quest_log': lambda o: o['questLog'].pop(),
            'keyed_object_array': lambda o: o.update(questObjects={'1': o['questObjects'][0]}),
            'duplicate_attempted_history': lambda o: o.update(gatherAttemptedSources=['8', '8']),
            'malformed_retired_history': lambda o: o.update(retiredSources=['01']),
        }
        for name, change in changes.items():
            with self.subTest(name=name):
                self.setUp(); change(self.view)
                self.assertNotIn('action', self.tick())

    def test_source_quest_and_current_visit_must_remain_eligible(self):
        changes = {
            'quest_complete': lambda o: o['questLog'][0].update(complete=True),
            'quest_failed': lambda o: o['questLog'][0].update(failed=True),
            'quest_complete_state': lambda o: o['questLog'][0].update(state=1),
            'quest_failed_state': lambda o: o['questLog'][0].update(state=2),
            'missing_quest': lambda o: o['questLog'][0].pop('questId'),
            'disabled': lambda o: o['state'].update(enabled=False),
            'unknown_movement': lambda o: o['self'].update(movementFlagsKnown=False),
            'airborne': lambda o: o['self'].update(movementFlags=0x1000),
            'locked_object': lambda o: o['questObjects'][0].update(flags=1),
            'in_use_object': lambda o: o['questObjects'][0].update(flags=8),
            'unavailable_object': lambda o: o['questObjects'][0].update(flags=16),
            'different_map': lambda o: o['questObjects'][0].update(mapId=1),
            'wrong_type': lambda o: o['questObjects'][0].update(gameObjectType=19),
            'unknown_object_revision': lambda o: o['questObjects'][0].update(positionRevision='0'),
            'unsupported_opening': lambda o: o['gatherRecipes'][0].update(openSpell=999999),
            'body_changed': lambda o: o['self'].update(guid='7'),
        }
        for name, change in changes.items():
            with self.subTest(name=name):
                self.setUp(); change(self.view)
                self.assertNotIn('action', self.tick())
        self.setUp()
        self.assertNotIn('action', self.tick(visit='4'))

    def test_pending_native_history_and_higher_priority_work_prevent_new_choice(self):
        for field in ('gatherAttemptedSources', 'retiredSources'):
            with self.subTest(field=field):
                self.setUp(); self.view[field] = [GUID]
                self.assertNotIn('action', self.tick())
        for kwargs in (dict(pending=[dict(id='1')]), dict(quiet=False), dict(urgent=True)):
            with self.subTest(kwargs=kwargs):
                self.setUp(); self.assertNotIn('action', self.tick(**kwargs))
        self.setUp(); self.view['state']['pendingGameObjectGuid'] = GUID
        self.assertEqual(self.tick()['phase'], 'retained_container_opening')

    def test_unselected_proposal_does_not_reserve_source_across_export_import(self):
        self.assertEqual(self.tick()['action']['sourceGuid'], GUID)
        self.assertFalse(self.checkpoint['data']['seen'])
        self.assertEqual(self.tick()['action']['sourceGuid'], GUID)
        self.assertFalse(self.checkpoint['data']['seen'])

    def test_known_non_dispatch_does_not_block_a_later_normal_decision(self):
        self.tick()
        receipt = dict(outcome='not_dispatched', payload=dict(capability='loot_gather', source_guid=GUID,
            opening_transport_attempted=False))
        self.assertEqual(self.tick(receipt=receipt)['action']['sourceGuid'], GUID)

    def test_admitted_or_unknown_rpc_survives_reload_without_source_replay(self):
        for outcome in ('unknown', 'settled_unverified', 'confirmed'):
            with self.subTest(outcome=outcome):
                self.setUp(); self.tick()
                receipt = dict(outcome=outcome, payload=dict(capability='loot_gather', source_guid=GUID,
                    opening_transport_attempted=False))
                self.assertNotIn('action', self.tick(receipt=receipt))
                self.assertTrue(self.checkpoint['data']['seen'][GUID])
                self.source = self.source.replace('gatherPreferNearest=true', 'gatherPreferNearest=false', 1)
                self.view['inventoryRevision'] = '14'
                self.assertNotIn('action', self.tick())

    def test_exact_acquired_no_open_allows_fresh_facts_without_erasing_old_unknown(self):
        def refused():
            return dict(outcome='settled_unverified', payload=dict(capability='loot_gather', source_guid=GUID,
                request_id='gather-1', opening_transport_attempted=False, transport_written=False,
                result=dict(sessionId='gather-policy-fixture', selfGuid='6', sourceGuid=GUID, requestId='gather-1',
                    phase='acquired_not_opened', terminal=True, openingTransportAttempted=False, openingTransportWritten=False)))

        for omitted in (False, True):
            with self.subTest(omitted_proto_false=omitted):
                self.setUp(); self.tick()
                receipt = refused()
                if omitted:
                    receipt['payload']['result'].pop('openingTransportAttempted')
                    receipt['payload']['result'].pop('openingTransportWritten')
                self.view['actions']['position']['revision'] = '12'
                self.view['questObjects'][0]['positionRevision'] = '18'
                self.view['inventoryRevision'], self.view['questLogRevision'] = '14', '20'
                action = self.tick(receipt=receipt)['action']
                self.assertEqual((action['sourceGuid'], action['requestId'], action['expectedPositionRevision'],
                    action['expectedObjectRevision'], action['expectedInventoryRevision'], action['expectedQuestLogRevision']),
                    (GUID, 'gather-2', '12', '18', '14', '20'))
                self.assertFalse(self.checkpoint['data']['seen'])

        # A truthful no-opening result for another attempt never deletes an
        # existing reservation retained through export/import from uncertainty.
        self.setUp(); self.tick()
        unknown = dict(outcome='unknown', payload=dict(capability='loot_gather', source_guid=GUID,
            request_id='gather-1', opening_transport_attempted=False))
        self.assertNotIn('action', self.tick(receipt=unknown))
        self.assertNotIn('action', self.tick(receipt=refused()))
        self.assertTrue(self.checkpoint['data']['seen'][GUID])

        for change in ('source', 'request', 'terminal', 'attempted', 'written', 'unknown', 'acquisition_unconfirmed'):
            with self.subTest(not_a_known_no_open=change):
                self.setUp(); self.tick()
                receipt = refused(); result = receipt['payload']['result']
                if change == 'source': result['sourceGuid'] = str(int(GUID)+1)
                elif change == 'request': result['requestId'] = 'other-request'
                elif change == 'terminal': result['terminal'] = False
                elif change == 'attempted': result['openingTransportAttempted'] = True
                elif change == 'written': result['openingTransportWritten'] = True
                elif change == 'unknown': receipt['outcome'] = 'unknown'
                else: result['phase'] = 'acquisition_unconfirmed'
                self.assertNotIn('action', self.tick(receipt=receipt))
                self.assertTrue(self.checkpoint['data']['seen'][GUID])

    def test_only_observed_capacity_clear_can_release_its_matching_seen_source(self):
        self.tick()
        self.view['state'].update(inventoryPaused=True, pendingGameObjectGuid=GUID)
        self.view['operation'] = dict(sourceGuid=GUID, requestId='actual-native-request', openingTransportAttempted=True)
        self.view['gatherAttemptedSources'] = [GUID]
        self.tick()
        self.assertEqual(self.checkpoint['data']['paused'][GUID], '13')
        self.view['state'].update(inventoryPaused=False)
        self.view['state'].pop('pendingGameObjectGuid')
        self.view.pop('operation')
        self.view['inventoryRevision'] = '14'
        # A freed slot never clears still-admitted native custody.
        self.assertNotIn('action', self.tick())
        self.view['gatherAttemptedSources'] = []
        self.assertEqual(self.tick()['action']['sourceGuid'], GUID)

    def test_native_opening_observation_retains_the_same_source_custody(self):
        self.view['operation'] = dict(sourceGuid=GUID, requestId='actual-native-request', openingTransportAttempted=True)
        self.assertNotIn('action', self.tick())
        self.view.pop('operation')
        self.assertNotIn('action', self.tick())


if __name__ == '__main__':
    unittest.main()
