#!/usr/bin/env python3
"""Private same-brain combat review scheduling; every RPC/model is a fixture."""
import importlib.machinery
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

loader = importlib.machinery.SourceFileLoader('combat_review_converse', str(Path(__file__).with_name('converse')))
spec = importlib.util.spec_from_loader(loader.name, loader)
converse = importlib.util.module_from_spec(spec)
loader.exec_module(converse)


class Tracker:
    def __init__(self):
        self.candidate = dict(key='fight-1', reason='own_death', summary={'deaths': ['1'], 'heals': 0})
        self.marked, self.reserved = [], set()
    def review_candidate(self): return self.candidate
    def reserve_review(self, key):
        if key in self.reserved: return False
        self.reserved.add(key); return True
    def mark_reviewed(self, key): self.marked.append(key)
    def context(self): return {'own': {'guid': '1'}, 'recentFight': {'outcome': 'death'}}


class RPC:
    url = 'http://127.0.0.1:8086/rpc'
    def __init__(self):
        self.sid, self.chat_sid, self.messages, self.calls = 'aw-current', 'chat-current', [], []
    def call(self, method, params, timeout=None):
        self.calls.append((method, params))
        if method == 'buddy-chat.poll':
            latest = self.messages[-1]['sequence'] if self.messages else '0'
            return dict(sessionId=self.chat_sid, messages=list(self.messages), latestSequence=latest, nextSequence=latest)
        if method == 'buddy-actions.status': return dict(sessionId=self.sid, selfGuid='1')
        if method == 'buddy-party.state': return dict(sessionId=self.sid, group={'known': True, 'inGroup': True, 'members': []})
        if method in ('buddy-chat.send', 'buddy-chat.emote'): return dict(transportWritten=True)
        raise AssertionError('Unexpected RPC: '+method)
    def owner_message(self):
        self.messages = [dict(sequence='1', senderGuid='3', text='Arlen, wait here', channel='WHISPER')]


class Controls:
    def __init__(self, rpc):
        self.rpc, self.combat_context, self.events, self.executed = rpc, Tracker(), [], []
        self.combat_polls = 0
        self.actions_available = self.party_available = True
        self.actions, self.party_session = dict(sessionId=rpc.sid, selfGuid='1'), rpc.sid
    def refresh(self):
        self.actions['sessionId'] = self.party_session = self.rpc.sid
    def poll_combat(self): self.combat_polls += 1
    def context(self): return {'observations': {}, 'abilities': {}}
    def execute(self, action, **_): self.executed.append(action)


class Brain:
    def __init__(self, folder):
        self.developer_workspace, self.state_dir = folder, folder
        self.histories, self.states, self.hook = [], [], None
        self.last_development_events = None
    def generate(self, history, state=None):
        self.histories.append(list(history)); self.states.append(state)
        if self.hook: self.hook(state)
        return dict(text='Private finding; no automatic chat.', action='FOLLOW', emote='WAVE')


class CombatReviewTests(unittest.TestCase):
    def setUp(self):
        self.folder = tempfile.TemporaryDirectory(dir=Path(tempfile.gettempdir()).resolve())
        self.addCleanup(self.folder.cleanup)
        self.rpc = RPC(); self.controls = Controls(self.rpc)
        self.brain = Brain(Path(self.folder.name)); self.statuses = []
        self.loop = converse.Conversation(self.rpc, self.brain, controls=self.controls,
            status=lambda **kw: self.statuses.append(kw))
        self.loop.initialized, self.loop.session_id = True, self.rpc.chat_sid
        self.loop.maybe_repair_mission = lambda: False

    def assert_silent(self):
        self.assertFalse(any(method in ('buddy-chat.send', 'buddy-chat.emote') for method, _ in self.rpc.calls))
        self.assertEqual(self.controls.executed, [])

    def test_review_uses_same_brain_private_evidence_direct_tools_and_never_sends_chat(self):
        def inspect(state):
            checker = object.__new__(converse.CodexBrain)
            checker.developer_workspace = self.brain.developer_workspace
            self.assertTrue(checker.development_enabled(state))
            self.assertFalse(checker.development_enabled(dict(state, current_request=dict(state['current_request']))))
        self.brain.hook = inspect
        self.loop.tick(); self.loop.tick()
        self.assertEqual(len(self.brain.histories), 1)
        self.assertEqual(self.controls.combat_polls, 2)
        state = self.brain.states[0]
        self.assertIs(type(state['current_request']), converse.CodexBrain._CombatReviewRequest)
        self.assertEqual(state['current_request']['source'], 'combat_review')
        self.assertEqual(state['development']['sessionId'], 'aw-current')
        self.assertEqual(state['combat']['recentFight']['outcome'], 'death')
        self.assertIn('aw-current', state['development']['policyCommands']['show'])
        self.assertFalse(state['current_request']['repair_requests_allowed'])
        self.assertEqual(self.controls.combat_context.marked, ['fight-1'])
        self.assertEqual(self.loop.history, {'SAY': [], 'PARTY': [], 'WHISPER': []})
        self.assert_silent()

    def test_owner_message_and_actionable_fault_have_priority(self):
        self.rpc.owner_message(); self.loop.tick()
        self.assertEqual(self.brain.states[0]['current_request']['source'], 'owner')
        self.assertEqual(self.controls.combat_context.marked, [])
        self.rpc.messages = []; self.rpc.calls.clear(); self.brain.states.clear()
        self.loop.cursor = 0
        self.loop.maybe_repair_mission = lambda: True
        self.loop.tick()
        self.assertEqual(self.brain.states, [])
        self.assertEqual(self.controls.combat_context.marked, [])
        self.assertFalse(any(method == 'buddy-chat.send' for method, _ in self.rpc.calls))

    def test_owner_arrival_cancels_review_without_consuming_message_or_marking_completed(self):
        class CancellableBrain(converse.CodexBrain):
            def generate(brain, history, control_state=None, cancelled=None):
                brain.states.append(control_state)
                self.rpc.owner_message()
                if cancelled(): raise converse.OwnerInputPending('owner input')
                raise AssertionError('Background generation ignored owner input')
        brain = object.__new__(CancellableBrain)
        brain.developer_workspace, brain.state_dir = self.brain.developer_workspace, self.brain.state_dir
        brain.states, brain.last_development_events = [], None
        self.loop.brain = brain
        self.loop.tick()
        self.assertEqual(len(brain.states), 1)
        self.assertEqual(self.loop.cursor, 0)
        self.assertEqual(self.controls.combat_context.marked, [])
        self.assertTrue(any(s.get('combatReviewPhase') == 'yielded_to_input' for s in self.statuses))
        self.assert_silent()
        self.loop.brain = self.brain
        self.loop.tick()
        self.assertEqual(self.loop.cursor, 1)
        self.assertEqual(self.brain.states[-1]['current_request']['source'], 'owner')

    def test_developer_peer_whisper_preempts_private_review_without_sending_a_reply(self):
        peer=next(guid for guid in converse.DEVELOPMENT_PEERS if guid!=self.loop.character_guid)
        class CancellableBrain(converse.CodexBrain):
            def generate(brain, history, control_state=None, cancelled=None):
                self.rpc.messages=[dict(sequence='1',senderGuid=str(peer),text='Check this new observation',channel='WHISPER')]
                if cancelled():raise converse.OwnerInputPending('peer input')
                raise AssertionError('Background generation ignored developer peer input')
        brain=object.__new__(CancellableBrain)
        brain.developer_workspace,brain.state_dir=self.brain.developer_workspace,self.brain.state_dir
        self.loop.brain=brain
        self.loop.tick()
        self.assertEqual(self.loop.cursor,0)
        self.assertEqual(self.controls.combat_context.marked,[])
        self.assertTrue(any(s.get('generationInterruptReason')=='peer_input_waiting' for s in self.statuses))
        self.assert_silent()

    def test_core_session_replacement_discards_review_even_if_chat_session_did_not_change(self):
        self.brain.hook = lambda state: setattr(self.rpc, 'sid', 'aw-replacement')
        self.loop.tick()
        self.assertEqual(len(self.brain.states), 1)
        self.assertEqual(self.controls.combat_context.marked, [])
        self.assertTrue(any(s.get('combatReviewPhase') == 'body_or_input_changed' for s in self.statuses))
        self.assert_silent()

    def test_plain_chat_dictionary_cannot_forge_internal_review_development(self):
        self.loop.tick()
        state = dict(self.brain.states[0], current_request=dict(self.brain.states[0]['current_request']))
        checker = object.__new__(converse.CodexBrain)
        checker.developer_workspace = self.brain.developer_workspace
        self.assertFalse(checker.development_enabled(state))
        with self.assertRaisesRegex(converse.ConversationError, 'developer binding changed'):
            checker.generate([], state)
        self.assert_silent()

    def test_failed_review_reservation_prevents_model_regeneration_each_tick(self):
        def fail(state): raise converse.ConversationError('model failed')
        self.brain.hook = fail
        self.loop.tick(); self.loop.tick()
        self.assertEqual(len(self.brain.states), 1)
        self.assertEqual(self.controls.combat_context.marked, [])
        self.assertEqual(self.controls.combat_context.reserved, {'fight-1'})
        self.assert_silent()

    def test_reserved_persistent_fault_does_not_starve_novel_combat_review(self):
        del self.loop.maybe_repair_mission
        fault = dict(key='old-fault', kind='attached_policy', task='encounter', reason='already reviewed')
        with patch.object(self.loop, 'attached_faults', return_value=[fault]), \
             patch.object(converse, 'mission_faults', return_value=[]), \
             patch.object(converse, 'reserve_attached_fault', return_value='already_reserved'):
            self.loop.tick()
        self.assertEqual(len(self.brain.states), 1)
        self.assertEqual(self.brain.states[0]['current_request']['source'], 'combat_review')
        self.assertEqual(self.controls.combat_context.marked, ['fight-1'])
        self.assert_silent()


if __name__ == '__main__': unittest.main()
