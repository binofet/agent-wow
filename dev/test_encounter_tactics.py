"""Exercise the shared encounter decisions through the actual Lua bridge."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
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

    def evaluate(self, frame, source=None):
        return json.loads(subprocess.check_output(
            [str(BINARY), '-source', str(source or ROOT / 'behaviors/encounter-tactics.lua')],
            input=json.dumps(frame), text=True, timeout=5))

    def tick(self):
        out = self.evaluate(self.frame)
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

    @staticmethod
    def enemy(health, **facts):
        return dict(present=True, aliveKnown=True, alive=True, attackable=True, health=health) | facts

    def test_queue_preserves_next_pull_and_switches_among_active_targets(self):
        near, runner, next_pull = '17379391034265436410', '17379391034265436411', '17379391034265436412'
        targets = {near: self.enemy(800), runner: self.enemy(200), next_pull: self.enemy(40)}
        frame = dict(kind='combat_queue', targets=targets, partyDPS=100)
        first = self.evaluate(frame)
        self.assertEqual([row['targetGuid'] for row in first['queue']], [next_pull, runner, near])
        self.assertNotIn('focusGuid', first)
        targets[near]['engaged'] = targets[runner]['engaged'] = True
        frame.update(ownerFocus=near, currentFocus=near, switchSeconds=1)
        self.assertEqual(self.evaluate(frame)['focusGuid'], near)
        frame.pop('ownerFocus')
        self.assertEqual(self.evaluate(frame)['focusGuid'], runner)
        frame['switchSeconds'] = 6
        self.assertEqual(self.evaluate(frame)['focusGuid'], near)
        targets[runner]['protected'] = True
        frame['explicitOrder'] = [runner, near]
        result = self.evaluate(frame)
        self.assertEqual(result['queue'][0], dict(targetGuid=runner, engaged=True, protected=True))
        self.assertEqual(result['focusGuid'], near)

    def test_pull_marks_use_the_queue_for_the_pack(self):
        targets = {'100': self.enemy(800, distance=5, x=5, y=0, z=0),
                   '101': self.enemy(100, distance=7, x=7, y=0, z=0),
                   '102': self.enemy(1, distance=24, x=24, y=0, z=0)}
        frame = dict(kind='assign_pull_marks', targets=targets, ownerZ=0)
        self.assertEqual(self.evaluate(frame)['assignments'][0], dict(mark='skull', targetGuid='101'))
        targets['100']['selected'] = True
        self.assertEqual(self.evaluate(frame)['assignments'][0], dict(mark='skull', targetGuid='100'))

    def test_editing_lua_changes_the_next_decision_with_the_same_runner(self):
        frame = dict(kind='combat_queue', targets={'9': self.enemy(200, engaged=True, incomingDPS=40),
                                                 '10': self.enemy(100, engaged=True, incomingDPS=10)})
        original = (ROOT / 'behaviors/encounter-tactics.lua').read_text()
        with tempfile.TemporaryDirectory() as workspace:
            script = Path(workspace) / 'encounter-tactics.lua'
            script.write_text(original)
            self.assertEqual(self.evaluate(frame, script)['focusGuid'], '9')
            script.write_text(original.replace('row.score = row.rate / row.seconds', 'row.score = 1 / row.seconds'))
            self.assertEqual(self.evaluate(frame, script)['focusGuid'], '10')

    def test_pull_marks_respect_taken_icons_and_the_new_skull_position(self):
        targets = {'100': self.enemy(500, distance=3, x=0, y=0, z=0),
                   '101': self.enemy(100, distance=10, x=12, y=0, z=0),
                   '102': self.enemy(450, distance=11, x=-12, y=0, z=0),
                   '103': self.enemy(20, distance=4, x=1, y=0, z=0, polymorph=True)}
        frame = dict(kind='assign_pull_marks', targets=targets, ownerZ=0, occupiedMarks={'moon': '103'})
        self.assertEqual(self.evaluate(frame)['assignments'], [
            dict(mark='skull', targetGuid='101'), dict(mark='x', targetGuid='100')])
        frame['occupiedMarks']['skull'] = '999'
        self.assertFalse(self.evaluate(frame)['assignments'])
