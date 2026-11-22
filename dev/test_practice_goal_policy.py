"""Actual maintained-bundle Practice goal continuation, without game transport."""
import copy
import unittest

from test_practice_target_policy import PracticeTargetPolicyTests as Policy


class PracticeGoalPolicyTests(unittest.TestCase):
    choose = Policy.choose
    frame = Policy.frame
    candidate = Policy.candidate

    @classmethod
    def setUpClass(cls):
        Policy.setUpClass.__func__(cls)

    def goal(self, class_id=8, quest=False):
        frame = self.frame()
        frame.update(selection='practice_goal', intent='begin', intentId='same-task-1', nowMonotonicMs=100,
                     pullStarted=False, castPending=False, castOutcomeUnknown=False)
        obs = frame['observation']
        obs['visitId'] = '7'
        obs['players']['4'].update(classKnown=True, classId=class_id, levelKnown=True, level=1,
                                   unitFlagsKnown=True, unitFlags=0)
        obs['questLogKnown'], obs['quests'] = quest, []
        if quest:
            obs['quests'] = [dict(slot=slot, questId=33 if slot == 0 else 0) for slot in range(25)]
        return frame

    def continue_frame(self, frame):
        frame['task'] = self.choose(frame)['task']
        frame['intent'] = 'tick'
        return frame

    def test_goal_selects_only_observed_quest_or_authored_xp_and_keeps_binding(self):
        for class_id, quest, expected in ((8, False, 'xp'), (4, False, 'xp'), (5, True, 'quest')):
            frame = self.goal(class_id, quest)
            before = copy.deepcopy(frame)
            out = self.choose(frame)
            self.assertEqual(out['kind'], 'continue')
            self.assertEqual(out['task']['goal'], expected)
            self.assertEqual(out['task']['id'], frame['intentId'])
            self.assertEqual(out['task']['visitId'], '7')
            self.assertEqual(out['writerLeaseMs'], 3000)
            self.assertNotIn('deadlineMonotonicMs', out['task'])
            self.assertEqual(frame, before)
        self.assertEqual(self.choose(self.goal(5))['task']['stage'], 'interrupted')
        frame = self.continue_frame(self.goal())
        frame['observation']['visitId'] = '8'
        self.assertEqual(self.choose(frame)['kind'], 'wait')

    def test_known_level_completion_finishes_only_existing_witnessed_own_fight(self):
        frame = self.continue_frame(self.goal())
        target = frame['candidates'][0]
        frame.update(currentTargetGuid=target['guid'], pullStarted=True)
        frame['observation']['players']['4'].update(level=2, unitFlags=524288)
        target.update(attackVictimKnown=True, attackVictimGuid='4', attackAgeMs=0)
        out = self.choose(frame)
        self.assertEqual(out['kind'], 'finish_current')
        self.assertEqual(out['task']['stage'], 'finishing')
        for change in (dict(attackVictimGuid='3'), dict(attackAgeMs=30001), dict(alive=False, health=0)):
            changed = copy.deepcopy(frame)
            changed['candidates'][0].update(change)
            out = self.choose(changed)
            self.assertEqual(out['kind'], 'finish')
            self.assertEqual(out['task']['stage'], 'completed')

    def test_target_death_cannot_advance_an_original_pending_or_unknown_cast(self):
        frame = self.continue_frame(self.goal())
        target = frame['candidates'][0]
        frame.update(currentTargetGuid=target['guid'], pullStarted=True)
        target.update(alive=False, health=0)
        for pending, unknown in ((True, False), (False, True), (True, True)):
            frame.update(castPending=pending, castOutcomeUnknown=unknown)
            out = self.choose(frame)
            self.assertEqual(out['kind'], 'wait')
            self.assertEqual(out['task']['stage'], 'settling')
        frame.update(castPending=False, castOutcomeUnknown=False)
        self.assertEqual(self.choose(frame)['kind'], 'advance')

    def test_unknown_quest_waits_while_known_completion_and_explicit_cancel_are_distinct(self):
        frame = self.continue_frame(self.goal(5, True))
        frame['observation']['questLogKnown'] = False
        self.assertEqual(self.choose(frame)['kind'], 'wait')
        frame['observation']['questLogKnown'] = True
        frame['observation']['quests'][0]['complete'] = True
        out = self.choose(frame)
        self.assertEqual((out['kind'], out['task']['stage']), ('finish', 'completed'))
        frame['intent'] = 'cancel'
        out = self.choose(frame)
        self.assertEqual((out['kind'], out['task']['stage']), ('finish', 'cancelled'))

    def test_same_task_goal_edit_and_only_explicit_deadline_can_finish_on_time(self):
        frame = self.continue_frame(self.goal())
        frame['observation']['players']['4']['level'] = 2
        before = copy.deepcopy(frame)
        self.assertEqual(self.choose(frame)['kind'], 'finish')
        edited = self.source.replace('xp_finish_level=2,writer_lease_ms=3000', 'xp_finish_level=3,writer_lease_ms=3000')
        self.assertNotEqual(edited, self.source)
        self.assertEqual(self.choose(frame, edited)['kind'], 'continue')
        frame['observation']['players']['4']['level'] = 1
        frame['nowMonotonicMs'] = 100000000
        self.assertEqual(self.choose(frame)['kind'], 'continue')
        frame['task']['deadlineMonotonicMs'] = 2100
        frame['nowMonotonicMs'] = 2100
        self.assertEqual(self.choose(frame)['phase'], 'explicit_deadline_reached')
        frame['nowMonotonicMs'] = 2099
        self.assertEqual(self.choose(frame)['kind'], 'continue')
        del frame['task']['deadlineMonotonicMs']
        frame['observation']['players']['4']['level'] = 2
        frame['nowMonotonicMs'] = before['nowMonotonicMs']
        self.assertEqual(frame, before)


if __name__ == '__main__':
    unittest.main()
