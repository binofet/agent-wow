import copy
import importlib.machinery
import importlib.util
from pathlib import Path
import unittest

loader = importlib.machinery.SourceFileLoader('tested_cave_reconcile', str(Path(__file__).with_name('cave-reconcile')))
spec = importlib.util.spec_from_loader(loader.name, loader)
r = importlib.util.module_from_spec(spec); loader.exec_module(r)
from test_cave_catchup import Party, TARGET


class ReconcileTests(unittest.TestCase):
    def pending(self):
        p = Party(); worker = p.worker(); worker.tick()
        data = copy.deepcopy(worker.data); data['encounters'][0]['phase'] = 'unconfirmed'
        for actor in p.rpcs.values():
            actor.actions['fargodeep']['active'] = False
            actor.actions['fargodeep']['phase'] = 'navigation_step_failed'
        p.events = []
        return p, data

    def reconcile(self, p, data):
        saved = []
        receipt = r.reconcile(p.rpcs, {g: a.session for g, a in p.rpcs.items()}, data, TARGET,
            lambda value: saved.append(copy.deepcopy(value)), now=lambda: 1234.)
        return receipt, saved[0]

    def test_abandon_untouched_target_keeps_all_requests_and_old_guid_never_replays(self):
        p, data = self.pending(); requests = copy.deepcopy(data['encounters'][0]['requests'])
        receipt, saved = self.reconcile(p, data)
        self.assertEqual(receipt['proof'], 'observed_alive_full_unclaimed')
        self.assertEqual(len(receipt['targetObservations']), 3)
        self.assertEqual(saved['encounters'][0]['requests'], requests)
        self.assertEqual(saved['encounters'][0]['phase'], 'stopped')
        self.assertFalse(saved['encounters'][0]['deadObserved'])
        worker = p.worker(stored=saved); worker.tick()
        self.assertFalse(any(request['enabled'] for _, request in p.events))
        self.assertEqual(worker.status['phase'], 'waiting_for_actual_nearby_isolated_kobold')
        for actor in p.rpcs.values(): actor.target['guid'] = str(TARGET+1)
        worker.tick()
        self.assertEqual([g for g, request in p.events if request['enabled']], [1, 5, 4])
        self.assertTrue(all(request['targetGuid'] == str(TARGET+1) for _, request in p.events))

    def test_actual_dead_settled_corpse_all_three_receivers_can_be_retired_without_claiming_items(self):
        p, data = self.pending()
        for actor in p.rpcs.values(): actor.target.update(alive=False, health=0, dynamicFlags=8)
        receipt, saved = self.reconcile(p, data)
        self.assertEqual(receipt['proof'], 'observed_dead_loot_settled')
        self.assertTrue(all(row['goldDust'] == row['largeCandles'] == 0 for row in receipt['characters']))
        self.assertFalse(saved['encounters'][0]['deadObserved'])
        self.assertEqual(p.events, [])

    def test_absent_target_is_abandoned_as_unknown_never_as_success_or_replay(self):
        p, data = self.pending()
        for actor in p.rpcs.values():
            original = actor.world
            def absent(params, original=original):
                value = original(params); value.pop('requestedCreature', None); value['nearbyCreatures'] = []; return value
            actor.world = absent
        receipt, saved = self.reconcile(p, data)
        self.assertEqual(receipt['proof'], 'outcome_unknown_target_absent_all_three')
        self.assertEqual(receipt['targetObservations'], [])
        self.assertEqual(saved['encounters'][0]['phase'], 'stopped')
        self.assertFalse(saved['encounters'][0]['deadObserved'])
        self.assertEqual(saved['encounters'][0]['requests'], data['encounters'][0]['requests'])
        self.assertEqual(p.events, [])

    def test_unsafe_body_or_unresolved_source_never_writes_ledger_or_actor(self):
        for mode in ('combat', 'dead', 'lease', 'cast', 'motion', 'auto', 'loot', 'other_scope', 'DE', 'body', 'unknown', 'hurt', 'claimed', 'victim', 'dead_lootable'):
            p, data = self.pending(); actor = p.rpcs[4]
            if mode == 'combat': actor.self['unitFlags'] |= 0x80000
            if mode == 'dead': actor.self['alive'] = False
            if mode == 'lease': actor.actions['interactionOwner'] = 'loot'
            if mode == 'cast': actor.actions['castPending'] = True
            if mode == 'motion': actor.actions['motionUnconfirmed'] = True
            if mode == 'auto': actor.actions['autoAttackUnconfirmed'] = True
            if mode == 'loot': actor.loot['pendingCorpseGuid'] = str(TARGET)
            if mode == 'other_scope': actor.actions['fargodeep']['targetGuid'] = str(TARGET+1)
            if mode == 'DE': actor.professions['operation'] = dict(status='OUTCOME_UNCONFIRMED', leaseReleased=True)
            if mode == 'body': actor.session = 'replacement'
            if mode == 'unknown': actor.target['healthKnown'] = False
            if mode == 'hurt': actor.target['health'] = 49
            if mode == 'claimed': actor.target['dynamicFlags'] = 4
            if mode == 'victim': actor.target.update(attackVictimKnown=True, attackVictimGuid='99')
            if mode == 'dead_lootable':
                for a in p.rpcs.values(): a.target.update(alive=False, health=0, dynamicFlags=9)
            saved = []
            with self.assertRaises(r.Error, msg=mode):
                r.reconcile(p.rpcs, {g: f'body-{g}' for g in p.rpcs}, data, TARGET, saved.append)
            self.assertEqual(saved, [], mode); self.assertEqual(p.events, [], mode)

    def test_second_observation_changed_source_is_not_a_retirement_receipt(self):
        p, data = self.pending(); reads = [0]
        def change(actor, method, params):
            if actor.guid == 1 and method == 'buddy-world.snapshot':
                reads[0] += 1
                if reads[0] == 2: actor.target['health'] = 49
        p.hook = change
        with self.assertRaises(r.Error): self.reconcile(p, data)
        self.assertEqual(p.events, [])


if __name__ == '__main__': unittest.main()
