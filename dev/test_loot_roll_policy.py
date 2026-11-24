"""Run the shared roll policy in the real bounded Lua evaluator, offline."""
import copy
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parent.parent
loader = importlib.machinery.SourceFileLoader('roll_policy_host', str(ROOT/'dev/task-runtime'))
spec = importlib.util.spec_from_loader(loader.name, loader)
host = importlib.util.module_from_spec(spec)
loader.exec_module(host)
GUID = str(0x4000000000009871)


def armor(entry=5000, **fields):
    return dict(dict(itemId=entry, available=True, itemClass=4, subclass=1, inventoryType=20,
        quality=2, armorComparisonKnown=True, armor=10, itemLevel=10, requiredLevel=10,
        allowableClass=0xffffffff, allowableRace=0xffffffff, stats=[dict(type=5, value=2)]), **fields)


def weapon(entry=6000, **fields):
    return dict(dict(itemId=entry, available=True, itemClass=2, subclass=15, inventoryType=13,
        quality=2, armorComparisonKnown=True, weaponComparisonKnown=True, itemLevel=10, requiredLevel=10, armor=0,
        allowableClass=0xffffffff, allowableRace=0xffffffff, delayMs=2000,
        damages=[dict(minimum=8, maximum=12)], stats=[]), **fields)


class LootRollPolicyTests(unittest.TestCase):
    @staticmethod
    def compose(gear, finalize="policy.commit(result.action)"):
        code = (ROOT/'behaviors/loot-roll.lua').read_text()
        return 'local gear=(function()\n'+gear+'\nend)()\nlocal policy=(function()\n'+code+'\nend)()\n'+"""
return {decide=function(frame)
    if frame.checkpoint then assert(policy.import_state(frame.checkpoint)) end
    for _,event in ipairs(frame.events or {}) do assert(policy.on_event(event)) end
    local result=policy.decide(frame,gear)
"""+finalize+"""
    return {decision=result,checkpoint=policy.export_state()}
end}
"""

    @classmethod
    def setUpClass(cls):
        binary = os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY')
        if not binary or not Path(binary).is_file() or not os.access(binary, os.X_OK):
            raise RuntimeError('build cmd/luapolicy and set BUDDY_LUAPOLICY_TEST_BINARY to its executable')
        cls.bridge = host.Bridge(binary, ROOT/'modules')
        cls.gear_source = (ROOT/'behaviors/gear.lua').read_text()
        cls.source = cls.compose(cls.gear_source)

    def setUp(self):
        self.checkpoint = None
        self.sequence = 0
        self.view = dict(sessionId='aw-roll-test', selfGuid='4', visitId='3', revision='1',
            player=dict(guid='4', present=True, mapKnown=True, classKnown=True, classId=8,
                raceKnown=True, raceId=1, levelKnown=True, level=10),
            skillsKnown=True, skillsComplete=True, skillsRevision='1',
            skills=[dict(skillId=415, value=75, maxValue=75)],
            equipmentKnown=True, equipmentRevision='1', equipment=[dict(slot=i, slotKnown=True) for i in range(19)],
            metadata=[armor()], offers=[self.offer()])

    @staticmethod
    def offer(**fields):
        return dict(dict(rollGuid=GUID, revision='1', visitId='3', mapId=0, lootSlot=2,
            itemEntry=5000, itemCount=1, offeredMask=15, remainingMs='60000',
            available=True, phase='pending'), **fields)

    def tick(self, view=None, new_event=True, **frame_fields):
        events = []
        if new_event:
            self.sequence += 1
            events = [dict(kind='loot_roll_observation', sequence=str(self.sequence),
                value=copy.deepcopy(self.view if view is None else view))]
        frame = dict(session_id='aw-roll-test', input=dict(kind='loot_roll', observationSequence=str(self.sequence)),
            events=events, **frame_fields)
        if self.checkpoint is not None:
            frame['checkpoint'] = self.checkpoint
        result = self.bridge.evaluate(self.source, frame)
        self.checkpoint = result['checkpoint']
        return result['decision']

    def choice(self, expected):
        action = self.tick()['action']
        self.assertEqual(action, dict(capability='loot_roll', sessionId='aw-roll-test', selfGuid='4',
            visitId='3', rollGuid=GUID, offerRevision='1', choice=expected, lootSlot=2,
            mapId=0, itemEntry=5000, randomSuffix=0, randomProperty=0, count=1,
            offeredMask=self.view['offers'][0]['offeredMask']))
        return action

    def equip(self, slot=4, entry=5001):
        self.view['equipment'][slot].update(itemGuid='4611686018427387905', entry=entry,
            stackCount=1, metadataKnown=True, enhancementsKnown=True, durabilityKnown=True,
            durability=30, maxDurability=30)

    def assert_wait_then_fallback(self, expected="greed"):
        self.assertNotIn('action', self.tick())
        self.view['offers'][0]['remainingMs'] = '1500'
        self.assertEqual(self.tick()['action']['choice'], expected)

    def test_empty_supported_slot_needs_and_proposal_preserves_exact_offer(self):
        self.choice('need')

    def test_equal_or_dominated_armor_disenchants_without_enchanting_skill(self):
        for current_armor in (10, 20):
            with self.subTest(current_armor=current_armor):
                self.setUp()
                self.equip()
                self.view['metadata'].append(armor(5001, armor=current_armor))
                self.assertNotIn(333, [s['skillId'] for s in self.view['skills']])
                self.choice('disenchant')

    def test_only_assigned_mira_body_disenchants_other_bodies_greed(self):
        for actor, expected in (('4', 'disenchant'), ('5', 'greed'), ('11', 'greed')):
            with self.subTest(actor=actor):
                self.setUp()
                self.view['selfGuid'] = actor
                self.view['player']['guid'] = actor
                self.equip()
                self.view['metadata'].append(armor(5001, armor=20))
                action = self.tick()['action']
                self.assertEqual((action['selfGuid'], action['choice']), (actor, expected))
        self.setUp()
        self.view['selfGuid'] = self.view['player']['guid'] = '11'
        self.assertEqual(self.tick()['action']['choice'], 'need')

    def test_mira_does_not_invent_an_unoffered_disenchant_choice(self):
        self.equip()
        self.view['metadata'].append(armor(5001, armor=20))
        self.view['offers'][0]['offeredMask'] = 3
        self.assertEqual(self.tick()['action']['choice'], 'pass')

    def test_known_fallback_greeds_when_need_or_assigned_disenchant_is_unoffered(self):
        for useful, mask in ((True, 5), (True, 4), (False, 7), (False, 4)):
            with self.subTest(useful=useful, mask=mask):
                self.setUp()
                if not useful:
                    self.equip()
                    self.view['metadata'].append(armor(5001, armor=20))
                self.view['offers'][0]['offeredMask'] = mask
                action = self.tick()['action']
                self.assertEqual(action['choice'], 'greed')
                self.assertEqual(action['offeredMask'], mask)
                self.assertEqual(action['rollGuid'], GUID)

    def test_unknown_facts_wait_then_use_only_an_offered_safe_deadline_choice(self):
        for mask, expected in ((15, 'greed'), (7, 'greed'), (4, 'greed'),
                               (1, 'pass'), (3, 'pass'), (9, 'pass'), (2, None), (8, None)):
            with self.subTest(mask=mask):
                self.setUp()
                self.view['metadata'] = []
                self.view['offers'][0].update(offeredMask=mask, remainingMs='1501')
                self.assertNotIn('action', self.tick())
                self.view['offers'][0]['remainingMs'] = '1500'
                action = self.tick().get('action')
                if expected is None:
                    self.assertIsNone(action)
                else:
                    self.assertEqual(action['choice'], expected)
                    self.assertEqual(action['offeredMask'], mask)
                    self.assertNotEqual(action['choice'], 'need')
                self.view['offers'] = [self.offer(rollGuid=str(int(GUID)+1), offeredMask=mask, remainingMs='0')]
                self.assertNotIn('action', self.tick())

    def test_greed_checkpoint_and_legacy_reserved_votes_never_revote_after_reload(self):
        self.equip()
        self.view['metadata'].append(armor(5001, armor=20))
        self.view['offers'][0]['offeredMask'] = 7
        self.choice('greed')
        self.assertNotIn('action', self.tick())
        for reserved in ('pass', 'need', 'disenchant', 'greed'):
            with self.subTest(reserved=reserved):
                self.setUp()
                self.checkpoint = dict(version=1, data=dict(sessionId='aw-roll-test', selfGuid='4',
                    reservations={'3:'+GUID+':1': reserved}))
                self.assertNotIn('action', self.tick())
                self.assertEqual(self.checkpoint['data']['reservations']['3:'+GUID+':1'], reserved)

    def test_same_shared_scorer_edit_changes_next_offer_without_replaying_reserved_vote(self):
        self.equip()
        self.view['metadata'][0]['stats'] = [dict(type=5, value=3)]
        self.view['metadata'].append(armor(5001, armor=20))
        self.choice('disenchant')
        actual = 'local gear=(function()\n'+self.gear_source+'\nend)()\n'
        actual += 'gear.class_profiles[8]={armorWeight=1,statWeights={[5]=20}}\nreturn gear'
        self.source = self.compose(actual)
        self.assertNotIn('action', self.tick())
        self.view['offers'] = [self.offer(rollGuid=str(int(GUID)+1), revision='2')]
        self.assertEqual(self.tick()['action']['choice'], 'need')

    def test_actual_preview_receives_no_fabricated_candidate_guid(self):
        actual = 'local gear=(function()\n'+self.gear_source+'\nend)()\n'+"""
local preview=gear.preview
gear.preview=function(frame)
    assert(frame.candidateFacts.itemGuid==nil and frame.candidateFacts.enhancementsKnown==nil)
    assert(frame.currentFacts.itemGuid=='4611686018427387905')
    return preview(frame)
end
return gear
"""
        self.source = self.compose(actual)
        self.equip()
        self.view['metadata'].append(armor(5001, armor=5))
        self.choice('need')

    def test_missing_shared_preview_cannot_fall_back_to_local_comparison(self):
        self.source = self.compose('return {}')
        self.assert_wait_then_fallback()

    def test_sparse_metadata_ids_use_same_bounded_policy(self):
        for entry in (99000, 2147483647):
            with self.subTest(entry=entry):
                self.setUp()
                self.view['metadata'] = [armor(entry)]
                self.view['offers'][0]['itemEntry'] = entry
                self.assertEqual(self.tick()['action']['choice'], 'need')

    def test_shared_armor_upgrade_needs_and_unprofiled_tradeoff_is_not_an_upgrade(self):
        self.equip()
        self.view['metadata'].append(armor(5001, armor=5, stats=[dict(type=5, value=1)]))
        self.choice('need')
        self.setUp()
        self.equip()
        self.view['metadata'].append(armor(5001, armor=5, stats=[dict(type=5, value=3)]))
        self.choice('disenchant')

    def test_future_level_allowance_is_lua_and_permanent_mask_exclusion_is_non_useful(self):
        for required, expected in ((11, 'need'), (12, 'need'), (13, 'need'), (14, 'disenchant')):
            with self.subTest(required=required):
                self.setUp()
                self.view['metadata'][0]['requiredLevel'] = required
                self.choice(expected)
        self.setUp()
        self.view['metadata'][0]['allowableClass'] = 1
        self.choice('disenchant')

    def test_unknown_identity_proficiency_equipment_or_metadata_waits_then_greeds(self):
        for fault in ('class', 'race', 'level', 'skills', 'incomplete_skills', 'equipment', 'missing', 'omitted', 'special'):
            with self.subTest(fault=fault):
                self.setUp()
                if fault in ('class', 'race', 'level'):
                    self.view['player'][fault+'Known'] = False
                elif fault == 'skills':
                    self.view['skillsKnown'] = False
                elif fault == 'incomplete_skills':
                    self.view['skillsComplete'] = False
                elif fault == 'equipment':
                    self.view['equipmentKnown'] = False
                elif fault == 'missing':
                    self.view['metadata'] = []
                elif fault == 'omitted':
                    self.view.pop('metadata')
                else:
                    self.view['metadata'][0]['armorComparisonKnown'] = False
                self.assert_wait_then_fallback()

    def test_equipped_enhancements_and_random_offer_properties_are_unknown(self):
        for fault in ('enhanced', 'unknown_enhancements', 'suffix', 'property'):
            with self.subTest(fault=fault):
                self.setUp()
                self.equip()
                self.view['metadata'].append(armor(5001, armor=1))
                if fault == 'enhanced':
                    self.view['equipment'][4]['hasEnhancements'] = True
                elif fault == 'unknown_enhancements':
                    self.view['equipment'][4]['enhancementsKnown'] = False
                else:
                    self.view['offers'][0]['randomSuffix' if fault == 'suffix' else 'randomProperty'] = 1
                self.assert_wait_then_fallback()

    def test_weapon_needs_only_full_comparison_current_proficiency_and_dps_upgrade(self):
        self.view['metadata'] = [weapon(), weapon(6001, damages=[dict(minimum=2, maximum=4)])]
        self.view['offers'][0]['itemEntry'] = 6000
        self.view['skills'].append(dict(skillId=173, value=50, maxValue=75))
        self.equip(15, 6001)
        self.assertEqual(self.tick()['action']['choice'], 'need')
        self.setUp()
        self.view['metadata'] = [weapon(weaponComparisonKnown=False)]
        self.view['offers'][0]['itemEntry'] = 6000
        self.assert_wait_then_fallback()

    def test_weapon_never_invents_missing_actual_proficiency_or_generic_coverage(self):
        for fault in ('proficiency', 'coverage'):
            with self.subTest(fault=fault):
                self.setUp()
                self.view['metadata'] = [weapon()]
                self.view['offers'][0]['itemEntry'] = 6000
                if fault == 'coverage':
                    self.view['metadata'][0]['armorComparisonKnown'] = False
                    self.view['skills'].append(dict(skillId=173, value=50, maxValue=75))
                self.assert_wait_then_fallback()

    def test_shared_scorer_refuses_zero_damage_weapon_even_for_empty_mainhand(self):
        self.view['metadata'] = [weapon(damages=[])]
        self.view['offers'][0]['itemEntry'] = 6000
        self.view['skills'].append(dict(skillId=173, value=50, maxValue=75))
        self.assert_wait_then_fallback()

    def test_staff_with_occupied_or_unknown_offhand_waits(self):
        self.view['metadata'] = [weapon(subclass=10, inventoryType=17)]
        self.view['offers'][0]['itemEntry'] = 6000
        self.view['skills'].append(dict(skillId=136, value=50, maxValue=75))
        self.equip(16, 6001)
        self.assert_wait_then_fallback()

    def test_proto3_zero_damage_minimum_and_known_empty_head_slot_are_preserved(self):
        self.view['metadata'] = [weapon(damages=[dict(maximum=10)])]
        self.view['offers'][0]['itemEntry'] = 6000
        self.view['skills'].append(dict(skillId=173, value=50, maxValue=75))
        self.assertEqual(self.tick()['action']['choice'], 'need')
        self.setUp()
        self.view['metadata'][0]['inventoryType'] = 1
        self.view['equipment'][0].pop('slot')
        self.choice('need')

    def test_duplicate_unknown_stats_or_multiple_offered_items_wait(self):
        for fault in ('duplicate', 'unsupported', 'count'):
            with self.subTest(fault=fault):
                self.setUp()
                if fault == 'duplicate':
                    self.view['metadata'][0]['stats'].append(dict(type=5, value=2))
                elif fault == 'unsupported':
                    self.view['metadata'][0]['stats'][0]['type'] = 41
                else:
                    self.view['offers'][0]['itemCount'] = 2
                self.assert_wait_then_fallback()

    def test_order_uses_earliest_deadline_then_full_decimal_guid(self):
        smaller, later = str(int(GUID)-1), str(int(GUID)+1)
        self.view['offers'] = [self.offer(rollGuid=later, remainingMs='55000'),
            self.offer(rollGuid=GUID, remainingMs='50000'), self.offer(rollGuid=smaller, remainingMs='50000')]
        self.assertEqual(self.tick()['action']['rollGuid'], smaller)
        self.assertEqual(self.tick()['action']['rollGuid'], GUID)
        self.assertEqual(self.tick()['action']['rollGuid'], later)

    def test_only_the_same_final_action_object_can_commit_a_vote(self):
        self.source = self.compose(self.gear_source, """
    local copy={}
    for k,v in pairs(result.action or {}) do copy[k]=v end
    policy.commit(copy)
    policy.commit(result.action)
""")
        self.assertEqual(self.tick()['action']['choice'], 'need')
        self.assertFalse(self.checkpoint['data']['reservations'])
        self.source = self.compose(self.gear_source)
        self.choice('need')
        self.assertNotIn('action', self.tick())

    def test_reevaluation_and_import_discard_only_the_ephemeral_proposal(self):
        for discard in ('policy.decide({input={}})',
                'assert(policy.import_state(policy.export_state()))'):
            with self.subTest(discard=discard):
                self.setUp()
                self.source = self.compose(self.gear_source, discard+'; policy.commit(result.action)')
                self.assertEqual(self.tick()['action']['choice'], 'need')
                self.assertFalse(self.checkpoint['data']['reservations'])
                self.source = self.compose(self.gear_source)
                self.choice('need')
                self.assertNotIn('action', self.tick())

    def test_consumed_offer_survives_export_import_lost_reply_and_repeated_observation(self):
        self.choice('need')
        self.assertNotIn('action', self.tick())
        self.assertNotIn('action', self.tick(new_event=False))
        self.view['offers'][0]['available'] = False
        self.view['offers'][0]['phase'] = 'vote_reserved'
        self.assertNotIn('action', self.tick())
        self.view['offers'] = [self.offer(rollGuid=str(int(GUID)+1))]
        self.assertIn('action', self.tick())

    def test_observation_sequence_must_exist_and_match_the_consumed_event(self):
        for sequence in (None, '0', '2', '01', 1):
            with self.subTest(sequence=sequence):
                frame = dict(session_id='aw-roll-test', input=dict(kind='loot_roll', observationSequence='1'),
                    events=[dict(kind='loot_roll_observation', value=self.view)])
                if sequence is not None:
                    frame['events'][0]['sequence'] = sequence
                output = self.bridge.evaluate(self.source, frame)
                self.assertNotIn('action', output['decision'])

    def test_unknown_never_disenchants_and_unoffered_choices_never_escape(self):
        self.view['metadata'] = []
        self.view['offers'][0]['offeredMask'] = 8
        self.assertNotIn('action', self.tick())
        self.view['offers'][0]['remainingMs'] = '1'
        self.assertNotIn('action', self.tick())
        self.setUp()
        self.view['offers'][0]['offeredMask'] = 1
        self.choice('pass')  # Complete facts, but no eligible positive choice.

    def test_malformed_or_replaced_visit_and_expired_offer_do_not_act(self):
        for fault in ('visit', 'guid', 'duplicate', 'expired', 'mask', 'metadata_duplicate'):
            with self.subTest(fault=fault):
                self.setUp()
                if fault == 'visit':
                    self.view['offers'][0]['visitId'] = '4'
                elif fault == 'guid':
                    self.view['offers'][0]['rollGuid'] = '017379390973850719988'
                elif fault == 'duplicate':
                    self.view['offers'].append(copy.deepcopy(self.view['offers'][0]))
                elif fault == 'expired':
                    self.view['offers'][0]['remainingMs'] = '0'
                elif fault == 'mask':
                    self.view['offers'][0]['offeredMask'] = 16
                else:
                    self.view['metadata'].append(armor())
                self.assertNotIn('action', self.tick())
        self.setUp()
        self.choice('need')
        self.view['selfGuid'] = '5'
        self.view['offers'][0]['rollGuid'] = '99'
        self.assertNotIn('action', self.tick())

    def test_bounded_sixteen_offers_and_complete_equipment_use_real_evaluator(self):
        self.view['offers'] = [self.offer(rollGuid=str(int(GUID)+i), revision=str(i+1)) for i in range(16)]
        self.view['skills'] = [dict(skillId=415, value=75, maxValue=75)]+[
            dict(skillId=i+1000, value=1, maxValue=1) for i in range(127)]
        self.assertIn('action', self.tick())
        self.view['offers'].append(self.offer(rollGuid='999'))
        self.assertNotIn('action', self.tick())


if __name__ == '__main__':
    unittest.main()
