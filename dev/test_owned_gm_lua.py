"""Owned pure Lua -> existing guarded sender, using only simulated packets."""
import copy
import fcntl
import hashlib
import json
import os
from pathlib import Path
import subprocess
import unittest
from unittest import mock
import direct_tools

from direct_tools import DirectTools, ToolOutcomeUnknown
import test_gm_command_evidence as evidence

# The fixture mocks the shared sender's subprocess transport. Keep the real
# existing evaluator runner separately; no game, model, or network is called.
EVALUATOR_RUN = subprocess.run
SOURCE = '''return {decide=function(frame)
 local target=frame.targetRefs.owner_target
 if not target.known or not target.aliveKnown or target.alive~=true then
  return {output={kind="hold",reason="No current observed live owner target"}}
 end
 return {output={kind="gm_command",target_ref="owner_target",args={command=".die"}}}
end}\n'''


class OwnedGMLuaTests(unittest.TestCase):
    def setUp(self):
        self.rig = evidence.GMCommandEvidenceTests('runTest')
        self.rig.setUp()
        self.addCleanup(self.rig.doCleanups)
        self.rig.die_world(owner_target='17379391167073878321')
        self.binary = Path(os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY',
                          Path(__file__).resolve().parent.parent / 'data/bin/luapolicy'))
        if not self.binary.is_file():
            self.skipTest('maintained public evaluator required; use BUDDY_LUAPOLICY_TEST_BINARY')
        self.request = dict(request=copy.deepcopy(self.rig.tools.context['current_request']),
                            continuationReport=dict(source='private_operator_report', reports=[
                                dict(goalClarification=True, text='Use my current target, not the old selected twin')]))
        self.tools = self.make_tools()
        self.sha = self.write(SOURCE, '')

    def make_tools(self):
        rig = self.rig
        return DirectTools(rig.rpc, rig.rpc.sid, '87', rig.workspace, rig.host,
            allowed=lambda: True, gm_allowed=lambda: rig.rpc.allow_gm,
            cancelled=lambda: rig.rpc.cancelled, request_id='one-owner-request',
            chat_session_id=rig.rpc.chat_sid, context=copy.deepcopy(rig.tools.context),
            helpers={'gm_tasks': rig.gm, 'policy_binary': self.binary},
            runner=EVALUATOR_RUN, gm_owner_request=copy.deepcopy(self.request))

    def write(self, source, previous):
        result = self.tools.dispatch('workspace_write', dict(path='gm-decision.lua',
            content=source, expected_sha256=previous))
        self.assertTrue(result['ok'], result)
        return hashlib.sha256(source.encode()).hexdigest()

    def step(self, *, invoke=False, operation='owned-lua:1', sha=None):
        return self.tools.dispatch('gm_lua_step', dict(expected_source_sha256=sha or self.sha,
            invoke=invoke), operation_id=operation)

    def test_actual_pure_evaluation_dry_run_and_guarded_exact_owner_twin(self):
        original = copy.deepcopy(self.request)
        preview = self.step()
        self.assertTrue(preview['ok'], preview)
        value = preview['result']
        self.assertFalse(value['invoked'])
        self.assertFalse(value['gameplayDispatched'])
        self.assertEqual(value['proposedArguments']['select_guid'], self.rig.die_new)
        self.assertNotEqual(value['proposedArguments']['select_guid'], self.rig.rpc.selected)
        self.assertEqual(value['targetRefs']['owner_target']['entry'], 12222)
        self.assertEqual(self.request, original)
        self.assertFalse(self.rig.rpc.packets)
        result = self.step(invoke=True)
        self.assertTrue(result['ok'], result)
        actual = result['result']['execution']
        self.assertTrue(actual['commandTransportWritten'], actual)
        self.assertEqual(actual['selectedGuid'], self.rig.die_new)
        self.assertEqual(actual['targetAdmission']['ownerTargetGuid'], self.rig.die_new)
        self.assertEqual([code for code, _ in self.rig.rpc.packets], [0x13d, 0x95])
        self.assertEqual(self.rig.rpc.commands, ['.die'])
        self.assertFalse(actual['gameEffectClaimed'])

    def test_missing_dead_or_stale_owner_target_is_a_lua_hold_not_other_twin(self):
        for condition in ('missing', 'dead', 'unknown_life', 'absent'):
            with self.subTest(condition=condition):
                self.rig.die_world(owner_target=self.rig.die_new)
                target = self.rig.die_creatures[self.rig.die_new]
                if condition == 'missing': self.rig.rpc.owner_selected = '99'
                if condition == 'dead': target['alive'] = False
                if condition == 'unknown_life': target['aliveKnown'] = False
                if condition == 'absent': target['present'] = False
                result = self.step(invoke=True, operation='hold:' + condition)
                self.assertTrue(result['ok'], result)
                self.assertEqual(result['result']['decision']['kind'], 'hold')
                self.assertFalse(self.rig.rpc.packets)

    def test_changed_target_during_evaluation_is_not_dispatched(self):
        def runner(*args, **kwargs):
            result = EVALUATOR_RUN(*args, **kwargs)
            self.rig.rpc.owner_selected = self.rig.die_old
            return result
        self.tools.runner = runner
        result = self.step(invoke=True)
        self.assertFalse(result['ok'], result)
        self.assertIn('target_changed', result['error']['error'])
        self.assertFalse(self.rig.rpc.packets)

    def test_source_cas_and_changed_body_cannot_dispatch(self):
        self.assertFalse(self.step(invoke=True, sha='0' * 64)['ok'])
        self.assertFalse(self.tools.dispatch('workspace_write', dict(path='gm-decision.lua',
            content=SOURCE + '-- edit', expected_sha256='0' * 64))['ok'])
        self.assertEqual((self.rig.workspace / 'gm-decision.lua').read_text(), SOURCE)
        self.tools.runner = lambda *args, **kwargs: self.changed_body(args, kwargs)
        result = self.step(invoke=True)
        self.assertFalse(result['ok'], result)
        self.assertFalse(self.rig.rpc.packets)

    def changed_body(self, args, kwargs):
        result = EVALUATOR_RUN(*args, **kwargs)
        self.rig.rpc.sid = 'changed-body'
        return result

    def test_final_sender_target_change_still_blocks_after_explicit_selection(self):
        read = self.rig.rpc.on_read
        def late(method, value):
            if any(code == 0x13d for code, _ in self.rig.rpc.packets):
                self.rig.rpc.owner_selected = self.rig.die_old
            read(method, value)
        self.rig.rpc.on_read = late
        result = self.step(invoke=True)
        self.assertTrue(result['ok'], result)
        self.assertFalse(result['result']['execution']['commandTransportWritten'])
        self.assertEqual([code for code, _ in self.rig.rpc.packets], [0x13d])

    def test_named_command_without_selection_needs_no_creature_reference(self):
        self.sha = self.write('return {decide=function(f) return {output={kind="gm_command",args={command=".gear repair Mira"}}} end}', self.sha)
        self.rig.rpc.owner_selected = '0'
        result = self.step(invoke=True)
        self.assertTrue(result['ok'], result)
        self.assertTrue(result['result']['execution']['commandTransportWritten'])
        self.assertEqual([code for code, _ in self.rig.rpc.packets], [0x95])

    def test_unknown_lua_command_stays_unknown_across_edit_and_raw_call(self):
        self.rig.rpc.post_failure = 'transport'
        with self.assertRaises(ToolOutcomeUnknown): self.step(invoke=True)
        packets = copy.deepcopy(self.rig.rpc.packets)
        records = {p: p.read_bytes() for p in (self.rig.host / 'data/converse/lotharen/gm-commands').glob('*/state.json')}
        self.tools = self.make_tools()
        self.sha = self.write(SOURCE + '-- model changed its source\n', self.sha)
        try: self.step(invoke=True, operation='edited-source:2')
        except ToolOutcomeUnknown: pass
        self.assertEqual(self.rig.rpc.packets, packets)
        self.tools = self.make_tools()
        try: self.tools.dispatch('gm_execute', dict(command='.die', select_guid=self.rig.die_new), operation_id='raw-after-lua:3')
        except ToolOutcomeUnknown: pass
        self.assertEqual(self.rig.rpc.packets, packets)
        for path, raw in records.items(): self.assertEqual(path.read_bytes(), raw)

    def test_unknown_raw_command_is_not_replayed_by_a_new_lua_source(self):
        self.rig.rpc.post_failure = 'transport'
        with self.assertRaises(ToolOutcomeUnknown):
            self.tools.dispatch('gm_execute', dict(command='.die', select_guid=self.rig.die_new), operation_id='raw:1')
        packets = copy.deepcopy(self.rig.rpc.packets)
        self.tools = self.make_tools()
        self.sha = self.write(SOURCE + '-- different source hash\n', self.sha)
        try: self.step(invoke=True, operation='lua-after-raw:2')
        except ToolOutcomeUnknown: pass
        self.assertEqual(self.rig.rpc.packets, packets)

    def ordinary_explicit_request(self):
        self.rig.die_world(owner_target=self.rig.die_old, selected='87')
        self.rig.tools.context['current_request']['messages'] = [dict(text='Act on the exact explicitly chosen creature, preserving unsettled receipts.')]
        self.request['request'] = copy.deepcopy(self.rig.tools.context['current_request'])
        self.tools = self.make_tools()
        self.rig.rpc.settle_selection = False

    def test_raw_explicit_a_then_lua_bare_b_retains_original_unsettled_target(self):
        self.ordinary_explicit_request()
        first = self.tools.dispatch('gm_execute', dict(command='.die', select_guid=self.rig.die_old), operation_id='raw-explicit-a')
        self.assertFalse(first['result']['commandTransportWritten'])
        self.sha = self.write('return {decide=function(f) return {output={kind="gm_command",args={command=".die"}}} end}', self.sha)
        self.rig.rpc.selected = self.rig.die_new
        result = self.step(invoke=True, operation='lua-bare-b')
        self.assertTrue(result['ok'], result)
        actual = result['result']['execution']
        self.assertFalse(actual['commandTransportWritten'])
        self.assertEqual(actual['targetAdmission']['requiredTargetGuid'], self.rig.die_old)
        self.assertEqual([code for code, _ in self.rig.rpc.packets], [0x13d])

    def test_lua_explicit_a_then_raw_bare_b_retains_original_unsettled_target(self):
        self.ordinary_explicit_request()
        first = self.step(invoke=True, operation='lua-explicit-a')
        self.assertTrue(first['ok'], first)
        self.assertFalse(first['result']['execution']['commandTransportWritten'])
        self.rig.rpc.selected = self.rig.die_new
        result = self.tools.dispatch('gm_execute', dict(command='.die'), operation_id='raw-bare-b')
        self.assertTrue(result['ok'], result)
        self.assertFalse(result['result']['commandTransportWritten'])
        self.assertEqual(result['result']['targetAdmission']['requiredTargetGuid'], self.rig.die_old)
        self.assertEqual([code for code, _ in self.rig.rpc.packets], [0x13d])

    def test_valid_but_stale_gm_selection_cannot_replace_current_owner_target(self):
        self.sha = self.write('return {decide=function(f) return {output={kind="gm_command",target_ref="actor_selection",args={command=".die"}}} end}', self.sha)
        result = self.step(invoke=True)
        self.assertTrue(result['ok'], result)
        actual = result['result']['execution']
        self.assertFalse(actual['commandTransportWritten'])
        self.assertEqual(actual['targetAdmission']['ownerTargetGuid'], self.rig.die_new)
        self.assertFalse(self.rig.rpc.packets)

    def test_owned_source_edit_lock_and_missing_host_request_admit_no_packet(self):
        with (self.rig.workspace / '.direct-tools-edit.lock').open('r+') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            result = self.step(invoke=True)
            self.assertFalse(result['ok'], result)
            self.assertIn('edit_in_progress', result['error']['error'])
        self.tools.context['current_request']['messages'] = [dict(text='a different request')]
        self.assertFalse(self.step(invoke=True)['ok'])
        self.tools.gm_owner_request = None  # No host request: tool is not advertised.
        self.assertNotIn('gm_lua_step', {row['function']['name'] for row in self.tools.tool_schemas()})
        self.assertFalse(self.step(invoke=True)['ok'])
        self.assertFalse(self.rig.rpc.packets)

    def test_actual_brain_model_tool_sequence_authors_tests_and_invokes_own_lua(self):
        import test_direct_brain as brain_fixture
        from test_direct_model import FakeHTTP, chat
        converse = brain_fixture.converse
        authored = SOURCE + '-- disposable source authored by the scripted model tool call\n'
        expected = hashlib.sha256(authored.encode()).hexdigest()
        seen = []
        def preview_done(payload):
            row = json.loads(payload['body']['messages'][-1]['content'])
            self.assertTrue(row['ok'], row)
            self.assertEqual(row['result']['sourceSHA256'], expected)
            self.assertFalse(row['result']['invoked'])
            self.assertFalse(self.rig.rpc.packets)
            return chat(('invoke-owned', 'gm_lua_step', dict(expected_source_sha256=expected, invoke=True)))
        responses = [
            chat(('author-owned', 'workspace_write', dict(path='gm-decision.lua', content=authored, expected_sha256=self.sha))),
            chat(('test-owned', 'lua_evaluate', dict(path='gm-decision.lua', frame=dict(targetRefs=dict(owner_target=dict(known=False)))))),
            chat(('preview-owned', 'gm_lua_step', dict(expected_source_sha256=expected, invoke=False))),
            preview_done,
            dict(choices=[dict(finish_reason='stop', message=dict(role='assistant', content='My owned module dispatched the exact owner target; effect remains unverified.'))])]
        original = DirectTools
        def tools_factory(*args, **kwargs):
            seen.append(copy.deepcopy(kwargs['gm_owner_request']))
            kwargs.update(runner=EVALUATOR_RUN, helpers={'gm_tasks': self.rig.gm, 'policy_binary': self.binary})
            return original(*args, **kwargs)
        state = dict(character=dict(guid='87'), gm_controls_available=True,
            development=dict(enabled=True, directDevelopment=True, workspace=str(self.rig.workspace),
                rpcUrl=self.rig.rpc.url, sessionId=self.rig.rpc.sid, selfGuid='87', ownerGuid='3'),
            current_request=dict(source='owner', senderGuid='3', channel='WHISPER', conversationScope='owner',
                messages=[dict(text='Kill my current target using your own tested Lua decision.')]))
        with FakeHTTP(responses) as server, mock.patch.object(converse, 'ROOT', self.rig.host), \
                mock.patch.object(converse, 'RPC', return_value=self.rig.rpc), \
                mock.patch.object(converse, 'checked_developer_workspace', return_value=self.rig.workspace), \
                mock.patch.object(direct_tools, 'DirectTools', side_effect=tools_factory), \
                mock.patch.object(self.rig.gm, 'bindings', return_value=[]), \
                mock.patch.object(self.rig.gm, 'task_rpc', return_value=self.rig.rpc):
            brain = converse.DirectBrain(self.rig.folder / 'brain', developer_workspace=self.rig.workspace,
                character_name='Lotharen', character_class='paladin', model='fixture', provider='local', base_url=server.base + '/v1')
            result = brain.generate([dict(text='Kill my current target using your own tested Lua decision.')], state)
        self.assertIn('owned module', result['text'])
        self.assertEqual(seen[0]['request'], state['current_request'])
        self.assertEqual((self.rig.workspace / 'gm-decision.lua').read_text(), authored)
        self.assertEqual(self.rig.rpc.commands, ['.die'])
        self.assertEqual(self.rig.rpc.selected, self.rig.die_new)
        rows = brain.last_development_result['tools']
        self.assertEqual([row['name'] for row in rows], ['workspace_write', 'lua_evaluate', 'gm_lua_step', 'gm_lua_step'])

    def test_typed_output_and_source_identity_cannot_fabricate_privilege(self):
        self.sha = self.write('return {decide=function(f) return {output={kind="gm_command",args={command=".die",select_guid="87"},target_ref="owner_target"}} end}', self.sha)
        result = self.step(invoke=True)
        self.assertFalse(result['ok'], result)
        self.assertIn('reference_conflict', result['error']['error'])
        self.rig.rpc.allow_gm = False
        self.assertFalse(self.step(invoke=True)['ok'])
        self.assertFalse(self.rig.rpc.packets)


if __name__ == '__main__':
    unittest.main()
