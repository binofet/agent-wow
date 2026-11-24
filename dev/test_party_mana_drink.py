"""The maintained mana worker observes the same accepted Lua rest lifecycle."""
import copy
import hashlib
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import party_mana_drink as drink
from party_mana_supply import runtime


ROOT = Path(__file__).resolve().parents[1]


class RestTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.module = (ROOT / 'behaviors/consumables.lua').read_text()
        cls.source = ('local m=(function()\n' + cls.module + '\nend)()\n' + '''
return {decide=function(input)
  if input.saved then assert(m.import_state(input.saved)) end
  for _,event in ipairs(input.events) do m.on_event(event) end
  local out=m.decide(input.frame,input.observation,input.available)
  return {decision=out,checkpoint=m.export_state()}
end}
''')
        binary = os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY', ROOT / 'data/bin/luapolicy')
        cls.bridge = runtime.Bridge(binary, ROOT / 'modules', execution_ms=1000)

    def setUp(self):
        self.calls, self.rests, self.saved = [], {}, None
        self.actions = dict(sessionId='aw-rest', selfGuid='1', following=True)
        self.views = {8086: dict(actions=self.actions, world=dict(self=dict(guid='1')))}
        self.inventory = dict(sessionId='aw-rest', selfGuid='1', inventoryKnown=True,
            inventoryRevision='10', manaDrinkCount=3,
            consumableContext=dict(bodyReady=True, foodBuffKnown=True, foodBuffPresent=False,
                dungeon=False, selfAuras=dict(complete=True, revision='1', auras=[])),
            items=[dict(metadataKnown=True,
                item=dict(itemGuid='4611686018427387905', entry=60001, stackCount=3, bag=0, slot=0),
                consumable=dict(usable=True, spells=[dict(spellId=430, drinkSupported=True, manaPerTick=21)]))])
        self.observation = dict(sessionId='aw-rest', selfGuid='1', visitId='1',
            players={'1': dict(present=True, aliveKnown=True, alive=True, powerKnown=True,
                powerType=0, power=60, maxPower=100)}, navigation=dict(movementOwner={}))
        self.state, self.on_attach = None, None
        self.revision = hashlib.sha256(self.source.encode()).hexdigest()

    def lua(self, events=None, *, source=None, available=True):
        # Synthetic native observations/receipts drive the REAL shared Lua
        # lifecycle. This fixture does not claim an actual packet/game effect.
        events = [dict(kind='inventory_observation', sequence='1', value=self.inventory), *(events or [])]
        self.revision = hashlib.sha256((source or self.source).encode()).hexdigest()
        result = self.bridge.evaluate(source or self.source, dict(saved=self.saved, events=events,
            frame=dict(session_id='aw-rest', cursor='9', pending=[], clock=dict(monotonicMs=1000),
                input=dict(observationSequences=dict(inventory='1'))),
            observation=self.observation, available=available))
        self.saved = result['checkpoint']
        self.state = dict(sessionId='aw-rest', characterGuid='1', revision=self.revision, generation='1',
            cursor='9', pendingActionIds=[], fault='', clock=dict(monotonicMs=1100),
            checkpoint=dict(data=dict(consumables=copy.deepcopy(self.saved))),
            latestDecision=dict(revision=self.revision, generation='1', cursor='9',
                clock=dict(monotonicMs=1000), output=dict(consumable=result['decision'])))
        return result['decision']

    def rpc(self, port, method, params):
        self.assertEqual(port, 8086)
        self.calls.append((method, copy.deepcopy(params)))
        self.assertEqual(params, dict(sessionId='aw-rest', characterGuid='1'))
        if method == 'session.policy_state':
            return copy.deepcopy(self.state)
        self.assertEqual(method, 'session.attach', 'Python must never issue a rest mutation')
        result = dict(sessionId='aw-rest', characterGuid='1',
            policy=dict(revision=self.state['revision'], generation=self.state['generation']))
        if self.on_attach:
            self.on_attach(result)
        return result

    def observe(self):
        return drink.step(self.rpc, self.views, self.rests, 8086, 1)

    @staticmethod
    def receipt(action, outcome='confirmed', **extra):
        return dict(kind='action_result', receipt=dict(outcome=outcome,
            payload={**action, **extra}))

    def acquire(self):
        first = self.lua()
        self.assertEqual(first['action']['capability'], 'movement_owner')
        self.assertEqual(first['action']['mode'], 'acquire')
        self.assertEqual(self.observe(), 'acquiring_rest_position')
        self.assertTrue(self.rests[8086]['hold'])
        lease = first['action']
        self.observation['navigation']['movementOwner'] = dict(active=True, owner=lease['owner'], remainingMs='2000')
        use = self.lua([self.receipt(lease, result=dict(token='rest-token'))])
        self.assertEqual(use['action']['capability'], 'consume')
        self.assertEqual(use['action']['source']['itemGuid'], '4611686018427387905')
        self.assertEqual(self.observe(), 'using_selected_item')
        return use['action']

    def test_real_lua_lease_use_effect_release_leaves_follow_intent_untouched(self):
        use = self.acquire()
        self.inventory['lastConsumableUse'] = dict(requestId=use['requestId'], inventoryConfirmed=True, auraObserved=True)
        self.inventory['consumableContext']['selfAuras']['auras'] = [dict(spellId=430, positive=True)]
        self.observation['players']['1']['power'] = 100
        result = self.lua([self.receipt(use, 'settled_unverified')])
        self.assertEqual(result['phase'], 'mana_restored')
        self.assertEqual(result['action']['mode'], 'release')
        self.observe()
        self.lua([self.receipt(result['action'])])
        self.observe()
        self.assertNotIn(8086, self.rests)
        self.assertTrue(self.actions['following'])
        self.assertEqual({method for method, _ in self.calls}, {'session.policy_state', 'session.attach'})

    def test_same_body_accepted_lua_tuning_is_only_rest_threshold(self):
        self.lua(source=self.source.replace('drink_below_percent=70', 'drink_below_percent=40'))
        self.assertEqual(self.observe(), 'no_consumable_needed_or_available')
        self.assertNotIn(8086, self.rests)
        self.lua()
        self.assertEqual(self.observe(), 'acquiring_rest_position')

    def test_unknown_consumption_survives_release_reconstruction_and_never_replays(self):
        use = self.acquire()
        self.lua([self.receipt(use, 'settled_unverified')])
        self.observation['players']['1']['inCombat'] = True
        release = self.lua()
        self.assertEqual(release['action']['mode'], 'release')
        self.lua([self.receipt(release['action'])])
        self.observation['navigation']['movementOwner'] = {}
        self.observation['players']['1']['inCombat'] = False
        before = copy.deepcopy(self.saved['data']['attempt'])
        self.rests = {}  # A restarted reader does not reconstruct an action.
        for _ in range(2):
            result = self.lua()
            self.assertNotIn('action', result)
            self.observe()
        self.assertEqual(self.saved['data']['attempt'], before)
        self.assertTrue(before['sent'])
        self.assertTrue(self.actions['following'])

    def test_missing_consumer_or_feed_is_explicit_and_cannot_fall_back_to_python(self):
        self.lua()
        self.state['checkpoint']['data'].pop('consumables')
        self.assertEqual(self.observe(), 'lua_rest_consumer_unavailable')
        self.lua()
        self.state['latestDecision']['output'] = {}
        self.assertEqual(self.observe(), 'lua_rest_consumer_unavailable')
        self.lua()
        self.state['latestDecision'] = None
        self.assertEqual(self.observe(), 'waiting_for_current_lua_decision')
        self.assertNotIn(8086, self.rests)
        self.assertEqual({method for method, _ in self.calls}, {'session.policy_state'})

    def test_body_revision_or_generation_replacement_cannot_publish_old_hold(self):
        for field, value in [('sessionId', 'aw-new'), ('characterGuid', '4'),
                             ('revision', 'f' * 64), ('generation', '2')]:
            with self.subTest(field=field):
                self.lua()
                self.on_attach = lambda result: result.update({field: value}) if field in ('sessionId', 'characterGuid') else result['policy'].update({field: value})
                self.assertEqual(self.observe(), 'waiting_for_current_lua_state')
                self.assertNotIn(8086, self.rests)

    def test_failed_read_retains_only_previous_observed_hold_and_unknown_checkpoint(self):
        self.acquire()
        before = copy.deepcopy(self.rests)
        def failed(*_):
            raise OSError('read unavailable')
        phases = drink.maintain_drink(failed, self.views, self.rests)
        self.assertEqual(phases[8086], 'lua_rest_observation_unavailable')
        self.assertEqual(self.rests, before)
        self.assertEqual(drink.maintain_drink(failed, {}, self.rests)[8086], 'waiting_for_body')

    def test_fault_or_old_decision_is_reported_without_touching_held_custody(self):
        self.acquire()
        before = copy.deepcopy(self.rests)
        self.state['fault'] = 'callback failed'
        self.assertEqual(self.observe(), 'lua_rest_policy_fault')
        self.state['fault'] = ''
        self.state['latestDecision']['generation'] = '0'
        self.assertEqual(self.observe(), 'waiting_for_current_lua_decision')
        self.assertEqual(self.rests, before)

    def test_fault_reported_by_postread_attachment_cannot_publish_or_release_old_hold(self):
        self.acquire()
        before = copy.deepcopy(self.rests)
        published = copy.deepcopy(self.views[8086]['mana_rest'])
        self.observation['players']['1']['inCombat'] = True
        self.assertEqual(self.lua()['action']['mode'], 'release')
        self.on_attach = lambda result: result['policy'].update(faulted=True)
        self.assertEqual(self.observe(), 'lua_rest_policy_fault')
        self.assertEqual(self.rests, before)
        self.assertEqual(self.views[8086]['mana_rest'], published)

    def test_observation_keeps_original_clocks_and_is_detached_from_reply(self):
        self.lua()
        self.observe()
        report = self.views[8086]['mana_rest']
        self.assertEqual(report['decisionClock'], dict(monotonicMs=1000))
        self.assertEqual(report['policyClock'], dict(monotonicMs=1100))
        self.state['checkpoint']['data']['consumables']['data']['attempt']['id'] = 'changed'
        self.assertNotEqual(report['checkpoint']['data']['attempt']['id'], 'changed')

    def test_actual_worker_reads_attached_lua_and_stop_cannot_restore_old_follow(self):
        self.lua()
        loader = importlib.machinery.SourceFileLoader('party_mana_rest_worker', str(ROOT / 'dev/party-mana'))
        spec = importlib.util.spec_from_loader(loader.name, loader)
        worker = importlib.util.module_from_spec(spec)
        loader.exec_module(worker)
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            catalog = root / 'pkg/gamecatalog/catalog.json'
            catalog.parent.mkdir(parents=True)
            catalog.write_text('{"items": {}}')
            class Stopped(Exception):
                pass
            observed = dict(actions=copy.deepcopy(self.actions), world=dict(self=dict(guid='1')),
                inventory=dict(manaDrinkCount=3))
            with patch.object(worker, '__file__', str(root / 'dev/party-mana')), \
                    patch.object(worker, 'PARTY', {8086: '1'}), \
                    patch.object(worker, 'CASTERS', {8086: '1'}), \
                    patch.object(worker, 'observe', return_value=observed), \
                    patch.object(worker, 'rpc', self.rpc), \
                    patch.object(worker, 'maintain_supply', return_value='waiting'), \
                    patch.object(worker, 'maintain_bag_pressure', return_value={}), \
                    patch.object(worker.signal, 'signal'), \
                    patch.object(worker.time, 'sleep', side_effect=Stopped):
                with self.assertRaises(Stopped):
                    worker.run(root / 'worker-state')
            status = json.loads((root / 'worker-state/status.json').read_text())
            self.assertEqual(status['phases']['drinking']['8086'], 'acquiring_rest_position')
            self.assertTrue(status['casters']['8086']['lua_rest']['hold'])
            self.assertTrue(observed['mana_rest_active'])
            self.assertEqual({method for method, _ in self.calls}, {'session.policy_state', 'session.attach'})
            self.assertTrue(self.actions['following'])


if __name__ == '__main__':
    unittest.main()
