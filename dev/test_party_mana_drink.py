import unittest
from unittest.mock import patch


import party_mana_drink as drink


def snapshot(guid, mana=75, map_id=0):
    return dict(world={'self': dict(guid=str(guid), present=True, aliveKnown=True, alive=True, powerKnown=True,
        unitFlagsKnown=True, movementFlagsKnown=True, mapKnown=True, mapId=map_id, power=mana, maxPower=100)},
        actions=dict(sessionId='session', selfGuid=str(guid)),
        inventory=dict(sessionId='session', selfGuid=str(guid), drinkingSupported=True, manaDrinkCount=2),
        trade=dict(sessionId='session', phase='IDLE'), spell_toolkit=dict(sessionId='session'))


class RestTests(unittest.TestCase):
    def setUp(self):
        self.views = {port: snapshot(guid) for port, guid in drink.CASTERS.items()}
        self.calls = []
        self.rests = {}

    def rpc(self, port, method, params):
        self.calls.append((port, method, params))
        actor = self.views[port]['actions']
        if method == 'buddy-actions.follow':
            return dict(actor, following=params['enabled'])
        return dict(sessionId=actor['sessionId'], selfGuid=actor['selfGuid'], spellId=430, transportWritten=True)

    def tick(self, seconds=100):
        with patch.object(drink.time, 'monotonic', return_value=seconds):
            return drink.maintain_drink(self.rpc, self.views, self.rests)

    def test_three_casters_start_without_chat(self):
        self.views[8086]['world']['owner'] = dict(guid='3', present=True, movementFlagsKnown=True, movementFlags=1)
        self.assertEqual(set(self.tick().values()), {'drink_dispatched'})
        self.assertEqual([port for port, method, _ in self.calls if method.endswith('.drink')], [8086, 8087, 8402])

    def test_overworld_threshold_and_dungeon_top_off(self):
        self.views[8086]['world']['self']['power'] = 80
        self.views[8087]['world']['self'].update(power=98, mapId=36)
        self.views[8402]['inventory']['manaDrinkCount'] = 0
        self.assertEqual(self.tick(), {8086: 'ready', 8087: 'drink_dispatched', 8402: 'needs_water'})
        self.assertEqual(set(self.rests), {8087})

    def test_combat_trade_and_another_movement_take_priority(self):
        self.views[8086]['world']['owner'] = dict(guid='3', present=True, unitFlagsKnown=True, unitFlags=drink.COMBAT)
        self.views[8087]['trade']['phase'] = 'OPEN'
        self.views[8402]['actions']['moving'] = True
        self.assertEqual(self.tick(), {8086: 'waiting_for_safe_rest', 8087: 'waiting_for_trade_or_creation', 8402: 'waiting_for_stop'})
        self.assertEqual(self.calls, [])

    def test_stale_attack_history_does_not_block_dungeon_recovery(self):
        target = dict(present=True, aliveKnown=True, alive=True, attackVictimKnown=True,
                      attackVictimGuid='1', attackAgeMs='30000')
        self.views[8086]['world']['nearbyCreatures'] = [target]
        self.views[8086]['world']['self']['mapId'] = 34
        self.assertEqual(self.tick()[8086], 'drink_dispatched')
        self.rests.clear()
        target['attackAgeMs'] = '400'
        self.assertEqual(self.tick(101)[8086], 'waiting_for_safe_rest')

    def test_managed_follow_resumes_after_real_mana_recovery(self):
        trapped = self.views[8086]
        trapped['actions'].update(following=True, moving=True)
        self.assertEqual(self.tick()[8086], 'settling')
        self.assertEqual(self.calls[0], (8086, 'buddy-actions.follow', {'enabled': False}))
        trapped['actions'].update(following=False, moving=False)
        self.assertEqual(self.tick(101)[8086], 'drink_dispatched')
        trapped['spell_toolkit']['playerAuras'] = [{'targetGuid': '1', 'auras': [{'spellId': 430, 'positive': True}]}]
        trapped['world']['self']['power'] = 95
        self.assertEqual(self.tick(102)[8086], 'recovering')
        trapped['world']['self']['power'] = 100
        self.assertEqual(self.tick(103)[8086], 'full')
        self.assertEqual(self.calls[-1], (8086, 'buddy-actions.follow', {'enabled': True}))
        self.assertNotIn(8086, self.rests)

    def test_combat_interrupt_rest_without_further_drink(self):
        self.views[8086]['actions']['following'] = True
        self.assertEqual(self.tick()[8086], 'settling')
        self.views[8086]['actions']['following'] = False
        self.views[8086]['world']['self']['unitFlags'] = drink.COMBAT
        self.assertEqual(self.tick(101)[8086], 'waiting_for_safe_rest')
        self.assertEqual([method for port, method, _ in self.calls if port == 8086], ['buddy-actions.follow'] * 2)
        self.assertNotIn(8086, self.rests)


if __name__ == '__main__':
    unittest.main()
