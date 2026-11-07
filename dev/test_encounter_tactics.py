"""Exercise the shared encounter decisions through the actual Lua bridge."""
import json
import os
from pathlib import Path
import subprocess
import unittest


ROOT = Path(__file__).resolve().parent.parent
BINARY = Path(os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY', ROOT / 'data/bin/luapolicy'))


@unittest.skipUnless(BINARY.is_file(), 'installed Lua bridge required')
class EncounterTacticsTests(unittest.TestCase):
    def setUp(self):
        self.frame = dict(
            plan=dict(groupGuid='90', leaderGuid='87', killOrder=['skull', 'x', 'moon', 'triangle'],
                      controls=[dict(mark='moon', technique='sap', casterGuid='5'),
                                dict(mark='triangle', technique='polymorph', refresh=4000)]),
            party=dict(group=dict(known=True, inGroup=True, groupGuid='90', leaderGuid='87',
                                  members=[dict(guid=g, online=True) for g in ('87', '5', '4')]),
                       raidTargets=dict(complete=True, groupGuid='90', marks=[
                           dict(icon=i, targetGuid=g) for i, g in ((8, '100'), (7, '101'), (5, '102'), (4, '103'))])),
            targets={str(g): dict(present=True, aliveKnown=True, alive=True) for g in range(100, 104)},
            capabilities=[dict(guid='5', technique='sap', ready=True, outOfCombatOnly=True),
                          dict(guid='4', technique='polymorph', ready=True)],
            controlObserved={'102': dict(known=True), '103': dict(known=True)},
        )

    def tick(self):
        out = json.loads(subprocess.check_output(
            [str(BINARY), '-source', str(ROOT / 'behaviors/encounter-tactics.lua')],
            input=json.dumps(self.frame), text=True, timeout=5))
        self.frame.update(bindings=out['bindings'], defeated=out['defeated'])
        return out

    def control(self, target, technique, remaining=30000):
        self.frame['controlObserved'][target] = dict(known=True, active=True, technique=technique, remainingMs=remaining)

    def test_joels_order_keeps_triangle_controlled_until_its_turn(self):
        result = self.tick()
        self.assertEqual(result['control'], dict(casterGuid='5', targetGuid='102', technique='sap', mark='moon'))
        self.assertNotIn('focus', result)
        self.control('102', 'sap')
        self.assertEqual(self.tick()['control']['casterGuid'], '4')
        self.control('103', 'polymorph')
        result = self.tick()
        self.assertEqual(result['focus']['mark'], 'skull')
        self.assertEqual(result['protected'], ['102', '103'])
        self.frame['targets']['100']['alive'] = False
        self.frame['party']['raidTargets']['marks'].pop(0)  # Same frame as observed death.
        self.assertEqual(self.tick()['focus']['mark'], 'x')
        self.frame['targets']['101']['alive'] = False
        self.control('103', 'polymorph', remaining=1000)
        self.assertEqual(self.tick()['control']['targetGuid'], '103')
        self.control('103', 'polymorph')
        result = self.tick()
        self.assertEqual(result['focus']['mark'], 'moon')
        self.assertEqual(result['protected'], ['103'])
        self.frame['targets']['102']['alive'] = False
        self.assertEqual(self.tick()['focus']['mark'], 'triangle')
        self.frame['targets']['103']['alive'] = False
        self.assertEqual(self.tick()['phase'], 'encounter_complete')

    def test_another_actual_leader_and_marker_replacement(self):
        self.frame['plan']['leaderGuid'] = '4'
        self.assertEqual(self.tick()['phase'], 'waiting_for_actual_group_leader')
        self.frame['party']['group']['leaderGuid'] = '4'
        self.assertEqual(self.tick()['phase'], 'apply_control')
        self.frame['party']['raidTargets']['marks'][0]['targetGuid'] = '199'
        result = self.tick()
        self.assertEqual(result['phase'], 'raid_mark_changed_replan')
        self.assertNotIn('focus', result)

    def test_partial_marks_and_sap_break_do_not_start_a_pull(self):
        self.frame['party']['raidTargets']['complete'] = False
        self.assertEqual(self.tick()['phase'], 'waiting_for_full_raid_marks')
        self.frame['party']['raidTargets']['complete'] = True
        self.frame['inCombat'] = True
        result = self.tick()
        self.assertEqual(result['phase'], 'control_broken_in_combat_moon')
        self.assertNotIn('focus', result)

    def test_initial_attempt_and_unsettled_cast_do_not_certify_control(self):
        self.frame['controlObserved'] = {}
        self.assertEqual(self.tick()['phase'], 'apply_control')
        self.frame['controlPending'] = {'102': True}
        result = self.tick()
        self.assertEqual(result['phase'], 'waiting_for_control_observation_moon')
        self.assertNotIn('focus', result)
