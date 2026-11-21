#!/usr/bin/env python3
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import types
import unittest

loader = importlib.machinery.SourceFileLoader('converse_cc', os.environ.get('WB_CONVERSE_SOURCE', str(Path(__file__).with_name('converse'))))
spec = importlib.util.spec_from_loader(loader.name, loader)
converse = importlib.util.module_from_spec(spec)
loader.exec_module(converse)


class ControlRPC:
    def __init__(self, policy=True, failure=None):
        self.policy, self.failure, self.calls = policy, failure, []

    def call(self, method, params):
        self.calls.append((method, params))
        if method == self.failure:
            raise converse.ConversationError('rejected explicit command')
        if method == 'buddy-actions.status':
            return {'sessionId': 'aw-mira', 'selfGuid': '4'}
        if method == 'session.attach':
            return {'sessionId': 'aw-mira', 'characterGuid': '4', 'policy': {'revision': 'current'} if self.policy else None}
        if method == 'session.send_event':
            return {'sessionId': 'aw-mira', 'characterGuid': '4', 'status': 'queued', 'sequence': '9'}
        raise AssertionError('unexpected legacy or tactical RPC: ' + method)


class LuaControlBridgeTests(unittest.TestCase):
    def controls(self, **options):
        return types.SimpleNamespace(rpc=ControlRPC(**options), character_guid=4)

    def test_attached_policy_queues_exact_or_auto_intent_without_selecting_enemy(self):
        for selector, value in [({'mark': 'moon'}, {'mark': 'moon'}),
                                ({'target_guid': '17379390962878382081'}, {'targetGuid': '17379390962878382081'}),
                                ({}, {'auto': True})]:
            with self.subTest(selector=selector):
                controls = self.controls()
                reply = converse.queue_lua_control(controls, 'polymorph', **selector)
                self.assertIn('queued', reply)
                self.assertNotIn('sheeped', reply)
                self.assertEqual(controls.rpc.calls[-1], ('session.send_event', {
                    'sessionId': 'aw-mira', 'characterGuid': '4', 'kind': 'command',
                    'value': dict(task='control', technique='polymorph', **value)}))
                self.assertEqual(controls.last_control_evidence['phase'], 'queued')
                self.assertIsNone(controls.command_fault)

    def test_only_absent_policy_permits_existing_legacy_handler(self):
        controls = self.controls(policy=False)
        self.assertIsNone(converse.queue_lua_control(controls, 'sap', mark='moon'))
        self.assertEqual([method for method, _ in controls.rpc.calls], ['buddy-actions.status', 'session.attach'])

    def test_rejection_never_falls_back_and_is_available_to_fault_observer(self):
        for method in ('session.attach', 'session.send_event'):
            controls = self.controls(failure=method)
            self.assertIsNotNone(converse.queue_lua_control(controls, 'polymorph', mark='moon'))
            self.assertEqual(controls.command_fault, {'sessionId': 'aw-mira', 'selfGuid': '4',
                'method': method, 'error': 'rejected explicit command'})
            controls.rpc.failure = None
            converse.queue_lua_control(controls, 'polymorph', mark='moon')
            self.assertIsNone(controls.command_fault)

    def test_generic_and_explicit_text_produces_intent_without_python_target_ranking(self):
        self.assertEqual(converse.parse_lua_control_request('keep one sheeped when we have adds'),
                         {'technique': 'polymorph', 'mark': None, 'target_guid': None})
        self.assertEqual(converse.parse_lua_control_request('please SAP moon!'),
                         {'technique': 'sap', 'mark': 'moon', 'target_guid': None})
        self.assertIsNone(converse.parse_lua_control_request("why can't you sheep?"))


if __name__ == '__main__':
    unittest.main()
