"""Actual Go Lua/descriptor bridge plus a private fake public-client endpoint."""
import copy
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
loader = importlib.machinery.SourceFileLoader('expedition_host_tested', str(ROOT / 'dev/task-runtime'))
spec = importlib.util.spec_from_loader(loader.name, loader)
host = importlib.util.module_from_spec(spec); loader.exec_module(host)
BINARY = os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY')


class Endpoint:
    url = 'http://127.0.0.1:8090/rpc'
    def __init__(self):
        self.calls, self.lease = [], ''
        self.guid, self.sid = '87', 'aw-offline-test'
        self.npc = str((0xf130 << 48) | (1234 << 24) | 99)
        self.pose = dict(known=True, mapKnown=True, mapId=0, x=10, y=0, z=5, revision='5', estimated=True)
        self.quest = dict(sessionId=self.sid, questLogKnown=True, questLogRevision='1', questLog=[dict(questId=701, complete=True)] + [{} for _ in range(24)])
        self.choices, self.nav_reason, self.fail_claim, self.foreign_receipt = [], 'exploration_within_distance', False, False
        self.accept_requested = False

    def actions(self):
        return dict(sessionId=self.sid, selfGuid=self.guid, position=copy.deepcopy(self.pose),
                    interactionOwner=self.lease and 'exploration' or '', interactionLeaseRemainingMs=30000 if self.lease else 0)

    def discover(self):
        self.quest['nearby'] = [dict(creature=dict(guid=self.npc, entry=1234, present=True, aliveKnown=True, alive=True,
            mapKnown=True, mapId=0, positionKnown=True, x=10, y=0, z=5, positionRevision='6', unitFlagsKnown=True, npcFlags=2),
            distanceKnown=True, distance=abs(self.pose['x'] - 10), canInteract=abs(self.pose['x'] - 10) <= 4)]

    def call(self, method, params, timeout=5):
        self.calls.append((method, copy.deepcopy(params)))
        if method == 'buddy-actions.status': return self.actions()
        if method == 'buddy-world.snapshot':
            return {'self': dict(guid=self.guid, present=True, aliveKnown=True, alive=True, mapKnown=True, mapId=0,
                unitFlagsKnown=True, movementFlagsKnown=True, healthKnown=True, health=100, maxHealth=100,
                positionKnown=True, x=-9000, y=90, z=60, classId=2)}
        if method == 'buddy-quests.state': return copy.deepcopy(self.quest)
        if method == 'buddy-quests.discover': self.discover(); return copy.deepcopy(self.quest)
        if method == 'buddy-actions.acquire_interaction':
            assert not self.lease and params == {'owner': 'exploration', 'leaseMs': 30000}
            self.lease = 'actual-private-token'; return dict(token=self.lease, state=self.actions())
        if method == 'buddy-actions.travel_to':
            assert params['token'] == self.lease
            if self.nav_reason == 'exploration_within_distance':
                self.pose.update(x=params.get('x', 0), y=params.get('y', 0), z=params.get('z', 0), revision=str(int(self.pose['revision']) + 1))
            value = self.actions(); value['reason'] = self.nav_reason
            return value
        if method == 'buddy-actions.release_interaction':
            assert params == {'owner': 'exploration', 'token': self.lease}
            self.lease = ''; return self.actions()
        if method == 'buddy-quests.turn_in':
            assert params['npcGuid'] == self.npc and params['questId'] == 701 and params['questLogRevision'] == self.quest['questLogRevision']
            self.quest['turnInOffer'] = dict(revision='3', questId=701, npcGuid=self.npc, source='reward_offer',
                canComplete=True, expiresInMs='10000', choices=self.choices)
        elif method == 'buddy-quests.claim_reward':
            assert params['npcGuid'] == self.npc and params['questId'] == 701 and params['offerRevision'] == '3'
            self.quest['turnInOffer']['rewardRequested'] = True
            if self.fail_claim: raise host.Error('rpc_result_unconfirmed')
        elif method == 'buddy-quests.offer':
            self.quest['offer'] = dict(revision='8', npcGuid=self.npc, expiresInMs='10000',
                                       quests=[dict(questId=702, available=True)])
        elif method == 'buddy-quests.accept':
            assert params['npcGuid'] == self.npc and params['questId'] == 702 and params['offerRevision'] == '8'
            self.accept_requested = True; self.quest['offer']['quests'][0]['acceptRequested'] = True
        else: raise AssertionError(method)
        return dict(transportWritten=True, serverResponseReceived=True, state=copy.deepcopy(self.quest))

    def confirm_reward(self):
        self.quest['questLog'] = [{} for _ in range(25)]; self.quest['questLogRevision'] = '2'
        self.quest['lastReward'] = dict(questId=701, npcGuid=self.npc, offerRevision='3', status='REWARD_CONFIRMED',
            baselineLogRevision='1', confirmedLogRevision='2', serverRewardObserved=True, logRemovalConfirmed=True)

    def confirm_acceptance(self):
        self.quest['questLog'][0] = dict(questId=702)
        self.quest['questLogRevision'] = '3'
        self.quest['lastAcceptance'] = dict(questId=702, npcGuid=self.npc, offerRevision='8', status='CONFIRMED_IN_LOG',
            baselineLogRevision='2', confirmedLogRevision='3')


@unittest.skipUnless(BINARY, 'set BUDDY_LUAPOLICY_TEST_BINARY to the compiled local bridge')
class ExpeditionTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(dir='/private/tmp'); self.addCleanup(temporary.cleanup)
        self.folder = Path(temporary.name) / 'state'
        self.rpc = Endpoint(); self.rpc.discover()
        self.bridge = host.Bridge(BINARY, ROOT / 'modules')
        self.goal = dict(id='generic-expedition', kind='journal_sync', params=dict(targetQuestIds=[702],
            initialHandInQuestIds=[701], handIns=[dict(questId=701, npcEntry=1234, mapId=0, x=10, y=0, z=5, followupQuestId=702)],
            rendezvous={'known': False}), allow=['buddy-actions.status', 'buddy-world.snapshot', 'buddy-quests.state',
            'buddy-quests.discover', 'buddy-quests.turn_in', 'buddy-quests.claim_reward', 'buddy-quests.offer', 'buddy-quests.accept',
            'buddy-actions.acquire_interaction', 'buddy-actions.travel_to', 'buddy-actions.release_interaction'],
            observations=dict(world=dict(method='buddy-world.snapshot', params={}, select=['self']),
                actions=dict(method='buddy-actions.status', params={}, select=['sessionId', 'selfGuid', 'position',
                    'interactionOwner', 'interactionLeaseRemainingMs', 'moving', 'traveling', 'motionUnconfirmed']),
                quests=dict(method='buddy-quests.state', params={}, select=['sessionId', 'questLogKnown', 'questLogRevision',
                    'questLog', 'nearby', 'turnInOffer', 'lastReward', 'offer', 'lastAcceptance', 'actionInFlight'])))

    def runner(self, store):
        return host.Runner(store, self.bridge, self.rpc, 87, self.rpc.sid, self.goal, ROOT / 'behaviors/expedition.lua',
                           ROOT / 'behaviors/task-stack.lua', rpc_timeout=35)

    def count(self, method): return sum(name == method for name, _ in self.rpc.calls)

    def until(self, runner, predicate, limit=12):
        for _ in range(limit):
            runner.tick()
            if predicate(): return
        self.fail(str(host.status(runner.data)))

    def test_actual_descriptor_zero_choice_handin_receipt_followup_and_resume(self):
        with host.Store(self.folder) as store:
            runner = self.runner(store)
            self.until(runner, lambda: self.count('buddy-quests.claim_reward') == 1)
            for _ in range(3): runner.tick()
            self.assertNotIn('701', runner.data['memory'].get('completed', {}))
            self.assertEqual(self.count('buddy-quests.claim_reward'), 1)
            claim = next(p for n, p in self.rpc.calls if n == 'buddy-quests.claim_reward')
            self.assertTrue(claim['noChoice']); self.assertNotIn('expectedItemId', claim)
        # Restart consumes saved frame/call state. No repeated reward write.
        with host.Store(self.folder) as store:
            runner = self.runner(store); self.rpc.confirm_reward()
            self.until(runner, lambda: self.rpc.accept_requested)
            self.assertIn('701', runner.data['memory']['completed'])
            for _ in range(2): runner.tick()
            self.assertNotIn('702', runner.data['memory'].get('accepted', {}))
            self.rpc.confirm_acceptance(); runner.tick()
            self.assertIn('702', runner.data['memory']['accepted'])
            self.assertFalse(runner.data['done'])
            self.assertEqual(runner.data['phase'], 'remaining_objectives_rendezvous_unknown')
            self.assertEqual(runner.data['memory']['remaining'], [dict(questId=702, inLog=True, complete=False)])
        self.assertEqual(self.count('buddy-quests.claim_reward'), 1)
        self.assertEqual(self.count('buddy-quests.accept'), 1)

    def test_single_omitted_zero_choice_and_multiple_choices_block(self):
        self.rpc.choices = [dict(itemId=9001, count=1)]
        with host.Store(self.folder) as store:
            runner = self.runner(store)
            self.until(runner, lambda: self.count('buddy-quests.claim_reward') == 1)
        claim = next(p for n, p in self.rpc.calls if n == 'buddy-quests.claim_reward')
        self.assertEqual(claim['expectedItemId'], 9001); self.assertNotIn('choiceIndex', claim)
        self.rpc = Endpoint(); self.rpc.discover(); self.rpc.choices = [dict(itemId=1), dict(index=1, itemId=2)]
        with host.Store(self.folder.parent / 'multiple') as store:
            runner = self.runner(store)
            self.until(runner, lambda: runner.data['phase'] == 'reward_choice_requires_selection')
            self.assertEqual(self.count('buddy-quests.claim_reward'), 0)

    def test_travel_normal_token_route_cleanup_then_fresh_giver(self):
        self.rpc.pose['x'] = -200; self.rpc.quest['nearby'] = []
        with host.Store(self.folder) as store:
            runner = self.runner(store)
            self.until(runner, lambda: self.count('buddy-quests.claim_reward') == 1, limit=16)
            self.assertEqual(self.rpc.lease, '')
            self.assertGreaterEqual(self.count('buddy-actions.travel_to'), 2)
            self.assertEqual(self.count('buddy-actions.acquire_interaction'), self.count('buddy-actions.release_interaction'))
            self.assertGreaterEqual(self.count('buddy-quests.discover'), 1)
            for name, params in self.rpc.calls:
                if name == 'buddy-actions.travel_to': self.assertLessEqual(abs(params.get('x', 0) + 200), 210)
            self.assertEqual(next(p for n, p in self.rpc.calls if n == 'buddy-quests.turn_in')['npcGuid'], self.rpc.npc)

    def test_navigation_block_releases_exact_token_and_does_not_reissue_route(self):
        self.rpc.pose['x'] = -20; self.rpc.quest['nearby'] = []
        self.rpc.nav_reason = 'exploration_navigation_unavailable'
        with host.Store(self.folder) as store:
            runner = self.runner(store)
            self.until(runner, lambda: runner.data['phase'].startswith('travel_stopped_'))
            for _ in range(3): runner.tick()
            self.assertEqual(self.rpc.lease, '')
            self.assertEqual(self.count('buddy-actions.travel_to'), 1)
            self.assertEqual(self.count('buddy-actions.release_interaction'), 1)
            self.assertEqual(self.count('buddy-quests.turn_in'), 0)

    def test_lost_claim_reply_stays_pending_until_exact_fresh_reward_receipt(self):
        self.rpc.fail_claim = True
        with host.Store(self.folder) as store:
            runner = self.runner(store)
            self.until(runner, lambda: bool(runner.data['pending']))
            runner.tick(); self.assertEqual(runner.data['phase'], 'blocked_on_ambiguous_call')
            self.rpc.confirm_reward()
            receipt = self.rpc.quest['lastReward']; receipt['npcGuid'] = '123'
            runner.tick(); self.assertEqual(len(runner.data['pending']), 1)
            receipt['npcGuid'] = self.rpc.npc
            runner.tick(); self.assertEqual(runner.data['pending'], [])
            self.assertEqual(runner.data['attempts'][-1]['outcome'], 'policy_observation_resolution')
            runner.tick(); self.assertIn('701', runner.data['memory']['completed'])
        self.assertEqual(self.count('buddy-quests.claim_reward'), 1)

    def test_unknown_log_dead_giver_and_no_complete_class_quest_cannot_dispatch(self):
        for mode in ('unknown_log', 'dead_giver', 'own_incomplete'):
            with self.subTest(mode=mode):
                self.rpc = Endpoint(); self.rpc.discover()
                if mode == 'unknown_log': self.rpc.quest['questLogKnown'] = False
                if mode == 'dead_giver': self.rpc.quest['nearby'][0]['creature']['alive'] = False
                if mode == 'own_incomplete': self.rpc.quest['questLog'][0]['complete'] = False
                with host.Store(self.folder.parent / mode) as store:
                    runner = self.runner(store); runner.tick()
                    self.assertEqual(self.count('buddy-quests.claim_reward'), 0)
                    self.assertEqual(self.count('buddy-quests.turn_in'), 0)

    def test_stale_cached_giver_is_refreshed_and_actual_spawn_guid_is_used(self):
        old_guid = self.rpc.npc
        original = self.rpc.call
        def fresh_spawn(method, params, timeout=5):
            if method == 'buddy-quests.discover': self.rpc.npc = str(int(old_guid) + 1)
            return original(method, params, timeout)
        self.rpc.call = fresh_spawn
        with host.Store(self.folder) as store:
            runner = self.runner(store)
            self.until(runner, lambda: self.count('buddy-quests.claim_reward') == 1)
        self.assertEqual(self.count('buddy-quests.discover'), 1)
        self.assertTrue(all(p['npcGuid'] == self.rpc.npc for n, p in self.rpc.calls if n in ('buddy-quests.turn_in', 'buddy-quests.claim_reward')))

    def test_transport_or_foreign_reward_receipt_never_counts_as_completion(self):
        with host.Store(self.folder) as store:
            runner = self.runner(store)
            self.until(runner, lambda: self.count('buddy-quests.claim_reward') == 1)
            self.rpc.confirm_reward(); good = copy.deepcopy(self.rpc.quest['lastReward'])
            for key, value in [('questId', 999), ('npcGuid', '999'), ('offerRevision', '99'),
                               ('baselineLogRevision', '99'), ('confirmedLogRevision', '1'),
                               ('serverRewardObserved', False), ('logRemovalConfirmed', False), ('status', 'REWARD_REQUESTED')]:
                self.rpc.quest['lastReward'] = dict(good, **{key: value})
                runner.tick()
                self.assertNotIn('701', runner.data['memory'].get('completed', {}))
                self.assertEqual(self.count('buddy-quests.claim_reward'), 1)
            self.rpc.quest['lastReward'] = good; runner.tick()
            self.assertIn('701', runner.data['memory']['completed'])


if __name__ == '__main__': unittest.main()
