"""Accepted reward choices through the real public Lua bridge and fake game I/O."""
import copy
import hashlib
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parent.parent
_loader = importlib.machinery.SourceFileLoader(
    'converse_accepted_reward_tests', str(ROOT/'dev/converse'))
_spec = importlib.util.spec_from_loader(_loader.name, _loader)
converse = importlib.util.module_from_spec(_spec)
_loader.exec_module(converse)
BINARY = Path(os.environ.get('BUDDY_LUAPOLICY_BINARY',
    os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY', ROOT/'data/bin/luapolicy')))


def item(entry, **facts):
    return dict(itemId=entry, available=True, name='Reward '+str(entry),
                itemClass=4, subclass=1, inventoryType=5,
                allowableClass=0xffffffff, allowableRace=0xffffffff, **facts)


class RewardRPC:
    """Response-shaped observations and one exact native claim boundary."""
    def __init__(self, source, *, class_id=8, guid=4, quest_id=6):
        self.source, self.generation = source, '1'
        self.sid, self.guid = 'reward-current-session', str(guid)
        self.class_id, self.calls, self.reads = class_id, [], {}
        self.hook = None
        self.rows = [item(900, stats=[dict(type=5, value=10)]),
                     item(901, stats=[dict(type=6, value=10)])]
        self.state = dict(sessionId=self.sid, revision='8', turnInOffer=dict(
            revision='9', npcGuid='17379390962878382081', questId=quest_id,
            title='Observed reward offer', source='reward_offer', canComplete=True,
            expiresInMs='60000', choices=[dict(index=4, itemId=900, count=1),
                                         dict(index=1, itemId=901, count=1)]))
        self.receipt_status = 'REWARD_CONFIRMED'
        self.fail_claim_transport = False

    @property
    def revision(self):
        return hashlib.sha256(self.source.encode()).hexdigest()

    def publish(self, source, expected_revision):
        # Synthetic accepted-publication CAS, not an RPC to any running actor.
        if expected_revision != self.revision:
            raise AssertionError('fixture publication revision changed')
        self.source = source
        self.generation = str(int(self.generation)+1)

    def call(self, method, params, timeout=None):
        self.calls.append((method, copy.deepcopy(params)))
        self.reads[method] = self.reads.get(method, 0)+1
        if self.hook:
            self.hook(method, self.reads[method])
        identity = dict(sessionId=self.sid, characterGuid=self.guid)
        if method == 'session.policy_source':
            result = dict(identity, source=self.source, revision=self.revision,
                          generation=self.generation, sourceLimitBytes=8*1024*1024)
        elif method == 'session.attach':
            result = dict(identity, policy=dict(revision=self.revision, generation=self.generation))
        elif method == 'buddy-world.snapshot':
            result = dict(self=dict(guid=self.guid, classKnown=True, classId=self.class_id,
                                    levelKnown=True, level=40))
        elif method == 'buddy-actions.status':
            result = dict(sessionId=self.sid, selfGuid=self.guid)
        elif method == 'buddy-trade.lookup_items':
            result = dict(sessionId=self.sid, items=self.rows)
        elif method == 'buddy-quests.state':
            result = self.state
        elif method == 'buddy-quests.claim_reward':
            offer = self.state['turnInOffer']
            choices = offer['choices']
            selected = [row for row in choices
                        if row['itemId'] == params['expectedItemId']]
            if (params['sessionId'] != self.state['sessionId']
                    or params['offerRevision'] != offer['revision']
                    or params['questId'] != offer['questId']
                    or params['npcGuid'] != offer['npcGuid']
                    or params['noChoice'] != (not choices)
                    or choices and (len(selected) != 1
                        or params['choiceIndex'] != selected[0]['index'])):
                raise AssertionError('claim must identify the exact current native offer')
            receipt = dict(questId=offer['questId'], status=self.receipt_status)
            if self.receipt_status == 'REWARD_CONFIRMED':
                receipt.update(serverRewardObserved=True, logRemovalConfirmed=True)
            self.state['lastReward'] = receipt
            if self.fail_claim_transport:
                raise converse.ConversationError('claim transport result unavailable')
            result = dict(transportWritten=True, state=self.state)
        else:
            raise AssertionError('Unexpected game or host operation: '+method)
        return copy.deepcopy(result)

    def claims(self):
        return [params for method, params in self.calls if method == 'buddy-quests.claim_reward']


@unittest.skipUnless(BINARY.is_file(), 'provide the existing public luapolicy evaluator')
class AcceptedRewardConsumerTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.bundle = subprocess.check_output(
            [sys.executable, '-B', str(ROOT/'dev/encounter-bundle')], text=True)

    def setUp(self):
        # Every automatic choice must use the connected actor publication.
        self.files = patch.object(converse.quest_rewards, '_evaluate',
                                  side_effect=AssertionError('FILE policy fallback is forbidden'))
        self.files.start()
        self.addCleanup(self.files.stop)

    def controls(self, rpc):
        role = {8:'mage', 9:'warlock'}[rpc.class_id]
        controls = converse.BuddyControls(rpc, character_class=role, character_guid=int(rpc.guid))
        controls.quests_available = True
        controls.quests = copy.deepcopy(rpc.state)
        controls.actions = dict(sessionId=rpc.sid, selfGuid=rpc.guid,
                                classKnown=True, classId=rpc.class_id)
        return controls

    def claim(self, controls, rpc, **arguments):
        return controls.execute('CLAIM_QUEST_REWARD',
            quest_id=rpc.state['turnInOffer']['questId'], **arguments)

    def test_full_bundle_same_session_accepted_source_edit_changes_exact_choice(self):
        rpc = RewardRPC(self.bundle)
        controls = self.controls(rpc)
        before, _, publication = controls.automatic_reward_choice(rpc.state['turnInOffer'])
        self.assertEqual(before['itemId'], 900)
        tuned = self.bundle.replace('[5]=4,[6]=3', '[5]=1,[6]=5', 1)
        self.assertNotEqual(tuned, self.bundle)
        rpc.publish(tuned, publication.revision)
        self.assertFalse(publication.current())
        self.assertIn('Turn-in confirmed', self.claim(controls, rpc))
        self.assertEqual(rpc.sid, 'reward-current-session')
        self.assertEqual(rpc.generation, '2')
        self.assertEqual(rpc.claims(), [dict(sessionId=rpc.sid, offerRevision='9',
            npcGuid='17379390962878382081', questId=6, choiceIndex=1,
            expectedItemId=901, noChoice=False)])
        self.assertIn('useful equipment', controls.reward_items['selectionReason'])

    def test_warlock_uses_the_same_actual_caster_ranker(self):
        rpc = RewardRPC(self.bundle, class_id=9, guid=8)
        rpc.rows[0]['allowableClass'] = 1 << 8
        controls = self.controls(rpc)
        self.assertIn('Turn-in confirmed', self.claim(controls, rpc))
        self.assertEqual(rpc.claims()[0]['expectedItemId'], 900)
        self.assertIn('useful equipment', controls.reward_items['selectionReason'])

    def preference_rpc(self, source=None):
        rpc = RewardRPC(self.bundle if source is None else source, quest_id=21)
        rpc.state['turnInOffer']['choices'] = [dict(index=1, itemId=2186, count=1),
            dict(index=0, itemId=2691, count=1), dict(index=5, itemId=11192, count=1)]
        rpc.rows = [item(entry) for entry in (2186, 2691, 11192)]
        return rpc

    def test_known_preference_changes_only_with_same_accepted_source_edit(self):
        for metadata in ('pending', 'unavailable'):
            with self.subTest(metadata=metadata):
                rpc = self.preference_rpc()
                rpc.rows = [dict(itemId=row['itemId'], pending=True)
                            if metadata == 'pending' else dict(itemId=row['itemId'], available=False)
                            for row in rpc.rows]
                controls = self.controls(rpc)
                before, _, publication = controls.automatic_reward_choice(rpc.state['turnInOffer'])
                self.assertEqual(before['itemId'], 11192)
                tuned = self.bundle.replace('[21]={[5]=11192,[8]=11192,[4]=2186}',
                                            '[21]={[5]=11192,[8]=2186,[4]=2186}', 1)
                self.assertNotEqual(tuned, self.bundle)
                rpc.publish(tuned, publication.revision)
                with patch.object(converse.quest_rewards, 'preferred_reward',
                                  side_effect=AssertionError('legacy preference bypass')):
                    self.assertIn('Turn-in confirmed', self.claim(controls, rpc))
                self.assertEqual((rpc.claims()[0]['expectedItemId'], rpc.claims()[0]['choiceIndex']), (2186, 1))
                self.assertIn('accepted quest preference', controls.reward_items['selectionReason'])
                self.assertNotIn('buddy-trade.lookup_items', rpc.reads)

    def test_faulted_publication_waits_but_healthy_resource_metadata_still_claims(self):
        for faulted in (True, False):
            with self.subTest(faulted=faulted):
                rpc = self.preference_rpc()
                original = rpc.call
                def call(method, params, timeout=None):
                    result = original(method, params, timeout)
                    if method == 'session.policy_source':
                        # This is the actual optional pure-export metadata,
                        # independent of the persistent publication's fault bit.
                        result['followFallback'] = dict(entryPoint='follow_formation', available=True,
                            revision=rpc.revision, lastError='RESOURCE: Lua execution budget exhausted')
                    elif method == 'session.attach':
                        result['policy']['faulted'] = faulted
                    return result
                rpc.call = call
                text = self.claim(self.controls(rpc), rpc)
                self.assertEqual(len(rpc.claims()), 0 if faulted else 1)
                self.assertEqual('Turn-in confirmed' in text, not faulted)
                self.assertNotIn('buddy-trade.lookup_items', rpc.reads)

    def test_malformed_accepted_choice_never_identifies_a_native_claim(self):
        for position, entry, index in (('0', '900', '4'), ('1', '999', '4'),
                                       ('1', '900', '1'), ('1', 'true', '4')):
            with self.subTest(position=position, entry=entry, index=index):
                source = ('return {quest_reward_choice=function(input) return {'
                    'status="selected",reason="quest_preference",position='+position+
                    ',itemId='+entry+',choiceIndex='+index+'} end}')
                rpc = RewardRPC(source)
                self.claim(self.controls(rpc), rpc)
                self.assertEqual(rpc.claims(), [])
                self.assertNotIn('buddy-trade.lookup_items', rpc.reads)

    def test_metadata_free_useful_or_vendor_output_cannot_bypass_unavailable_facts(self):
        for reason in ('useful', 'vendor'):
            with self.subTest(reason=reason):
                source = ('return {quest_reward_choice=function(input) return {'
                    'status="selected",position=1,itemId=900,choiceIndex=4,reason="'+reason+'"} end}')
                rpc = RewardRPC(source)
                rpc.rows = [dict(itemId=900, pending=True), dict(itemId=901, available=False)]
                with patch.object(converse.time, 'sleep'):
                    self.claim(self.controls(rpc), rpc)
                self.assertEqual(rpc.claims(), [])
                self.assertEqual(rpc.reads.get('buddy-trade.lookup_items'), 3)

    def test_identity_bound_actions_class_supports_preference_and_conflicts_refuse(self):
        for mode in ('actions_only', 'conflicting_classes', 'foreign_actions_session'):
            with self.subTest(mode=mode):
                rpc = self.preference_rpc()
                rpc.rows = [dict(itemId=row['itemId'], pending=True) for row in rpc.rows]
                original = rpc.call
                def call(method, params, timeout=None):
                    result = original(method, params, timeout)
                    if method == 'buddy-world.snapshot' and mode == 'actions_only':
                        result['self'].pop('classKnown')
                    elif method == 'buddy-actions.status':
                        result.update(classKnown=True, classId=5 if mode == 'conflicting_classes' else 8)
                        if mode == 'foreign_actions_session':result['sessionId'] = 'replacement'
                    return result
                rpc.call = call
                text = self.claim(self.controls(rpc), rpc)
                self.assertEqual(len(rpc.claims()), 1 if mode == 'actions_only' else 0)
                if mode == 'actions_only':
                    self.assertIn('Turn-in confirmed', text)
                    self.assertEqual(rpc.claims()[0]['expectedItemId'], 11192)
                self.assertNotIn('buddy-trade.lookup_items', rpc.reads)

    def test_missing_actor_export_never_uses_known_file_preference(self):
        rpc = self.preference_rpc('return {decide=function() return {} end}')
        text = self.claim(self.controls(rpc), rpc)
        self.assertIn('waiting', text)
        self.assertEqual(rpc.claims(), [])

    def test_invalid_accepted_source_envelope_never_evaluates_or_claims(self):
        changes = dict(hash=('revision', 'a'*64), generation=('generation', '0'),
            generation_overflow=('generation', str(2**64)),
            body=('characterGuid', '87'), session=('sessionId', 'replacement'),
            bound=('sourceLimitBytes', 1))
        for name, (field, value) in changes.items():
            with self.subTest(change=name):
                rpc = self.preference_rpc()
                original = rpc.call
                def call(method, params, timeout=None):
                    result = original(method, params, timeout)
                    if method == 'session.policy_source':
                        result[field] = value
                    return result
                rpc.call = call
                self.assertIn('waiting', self.claim(self.controls(rpc), rpc))
                self.assertEqual(rpc.claims(), [])
                self.assertNotIn('buddy-trade.lookup_items', rpc.reads)

    def test_final_source_generation_and_attach_body_changes_send_no_claim(self):
        for change in ('source', 'generation', 'attach_session', 'attach_guid'):
            with self.subTest(change=change):
                rpc = RewardRPC(self.bundle)
                original = rpc.call
                def call(method, params, timeout=None):
                    if method == 'buddy-quests.state' and rpc.reads.get(method) == 1:
                        if change == 'source':
                            rpc.publish(rpc.source+'\n--accepted edit', rpc.revision)
                        elif change == 'generation':
                            rpc.generation = '2'
                    result = original(method, params, timeout)
                    if method == 'session.attach':
                        if change == 'attach_session':result['sessionId'] = 'replacement'
                        elif change == 'attach_guid':result['characterGuid'] = '87'
                    return result
                rpc.call = call
                self.claim(self.controls(rpc), rpc)
                self.assertEqual(rpc.claims(), [])
                self.assertEqual(rpc.reads.get('buddy-trade.lookup_items'), 1)

    def test_changed_observed_body_metadata_or_exact_offer_sends_no_claim(self):
        changes = ('body_guid', 'body_session', 'lookup_session', 'quest_session',
                   'revision', 'npc', 'choice_order', 'choice_count', 'choice_item',
                   'expired', 'requested', 'incomplete', 'source', 'unknown_receipt')
        for change in changes:
            with self.subTest(change=change):
                rpc = RewardRPC(self.bundle)
                def hook(method, count):
                    if method != 'buddy-quests.state' or count != 2:
                        return
                    offer = rpc.state['turnInOffer']
                    if change == 'quest_session':rpc.state['sessionId'] = 'replacement'
                    elif change == 'revision':offer['revision'] = '10'
                    elif change == 'npc':offer['npcGuid'] = '17379390962878382082'
                    elif change == 'choice_order':offer['choices'].reverse()
                    elif change == 'choice_count':offer['choices'][0]['count'] = 2
                    elif change == 'choice_item':offer['choices'][0]['itemId'] = 902
                    elif change == 'expired':offer['expiresInMs'] = '0'
                    elif change == 'requested':offer['rewardRequested'] = True
                    elif change == 'incomplete':offer['canComplete'] = False
                    elif change == 'source':offer['source'] = 'quest_details'
                    elif change == 'unknown_receipt':
                        rpc.state['lastReward'] = dict(questId=6, status='REWARD_WRITE_UNCONFIRMED')
                rpc.hook = hook
                original = rpc.call
                def call(method, params, timeout=None):
                    result = original(method, params, timeout)
                    if method == 'buddy-actions.status':
                        if change == 'body_guid':result['selfGuid'] = '87'
                        elif change == 'body_session':result['sessionId'] = 'replacement'
                    elif method == 'buddy-trade.lookup_items' and change == 'lookup_session':
                        result['sessionId'] = 'nonempty-foreign-session'
                    return result
                rpc.call = call
                self.claim(self.controls(rpc), rpc)
                self.assertEqual(rpc.claims(), [])

    def test_native_unknown_is_retained_without_reselection_or_replay(self):
        for transport_failure in (False, True):
            with self.subTest(transport_failure=transport_failure):
                rpc = RewardRPC(self.bundle)
                rpc.receipt_status = 'REWARD_WRITE_UNCONFIRMED'
                rpc.fail_claim_transport = transport_failure
                controls = self.controls(rpc)
                self.assertNotIn('Turn-in confirmed', self.claim(controls, rpc))
                lookup_count = rpc.reads['buddy-trade.lookup_items']
                self.assertIn("won't repeat", self.claim(controls, rpc))
                self.assertEqual(len(rpc.claims()), 1)
                self.assertEqual(rpc.reads['buddy-trade.lookup_items'], lookup_count)
                self.assertEqual(rpc.state['lastReward']['status'], 'REWARD_WRITE_UNCONFIRMED')

    def test_existing_unknown_receipts_never_select_or_claim(self):
        for status in ('REWARD_REQUESTED', 'REWARD_UNCONFIRMED',
                       'REWARD_WRITE_UNCONFIRMED', 'REWARD_CONFIRMED'):
            with self.subTest(status=status):
                rpc = RewardRPC(self.bundle)
                rpc.state['lastReward'] = dict(questId=6, status=status)
                controls = self.controls(rpc)
                self.assertIn("won't repeat", self.claim(controls, rpc))
                self.assertEqual(rpc.claims(), [])
                self.assertNotIn('session.policy_source', rpc.reads)
                self.assertNotIn('buddy-trade.lookup_items', rpc.reads)

    def test_explicit_single_and_no_choice_do_not_require_actor_export(self):
        for mode in ('explicit', 'single', 'none'):
            with self.subTest(mode=mode):
                rpc = RewardRPC('return {}')
                if mode == 'single':
                    rpc.state['turnInOffer']['choices'] = [dict(index=3, itemId=900, count=1)]
                elif mode == 'none':
                    rpc.state['turnInOffer']['choices'] = []
                arguments = dict(item_entry=901) if mode == 'explicit' else {}
                self.assertIn('Turn-in confirmed', self.claim(self.controls(rpc), rpc, **arguments))
                self.assertEqual(len(rpc.claims()), 1)
                self.assertNotIn('session.policy_source', rpc.reads)
                self.assertNotIn('buddy-trade.lookup_items', rpc.reads)


if __name__ == '__main__':
    unittest.main()
