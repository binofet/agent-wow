import copy
import hashlib
import json
import os
from pathlib import Path
import subprocess
import stat
import tempfile
import unittest
from unittest import mock

from direct_tools import DirectTools, ToolOutcomeUnknown, load_helper, context_page, context_tool_schema, packed, READ_PAGE_BYTES

ROOT = Path(__file__).resolve().parent.parent


def checksum(value):
    return hashlib.sha256(value.encode()).hexdigest()


class RPC:
    url = "http://127.0.0.1:8087/rpc"

    def __init__(self, guid="4"):
        self.sid, self.guid, self.revision = "aw-123", guid, "catalog-1"
        self.source = "return {decide=function(f) return {output={value=f.value}} end}\n"
        self.pending = ["issued-before-edit"]
        self.calls, self.writes = [], []
        self.fail_mutation = False
        self.hook = None
        self.last_failure = None

    def catalog(self):
        rows = [dict(name=n, effect=e, inputType="request", outputType="response") for n, e in [
            ("buddy-actions.status", "read"), ("buddy-actions.behavior", "read"),
            ("buddy-actions.spell_toolkit", "read"), ("buddy-world.snapshot", "read"),
            ("buddy-actions.cast_spell", "mutation"), ("buddy-trade.state", "unspecified")]]
        host = [dict(name=n, effect=e, input=dict(properties=dict(sessionId={}, characterGuid={})))
                for n, e in [("session.describe", "read"), ("session.policy_source", "read"),
                             ("session.policy_state", "read"), ("session.reload_policy", "mutation"),
                             ("session.send_event", "mutation")]]
        return dict(sessionId=self.sid, characterGuid=self.guid, revision=self.revision,
                    methods=rows, hostMethods=host,
                    messages=dict(request=dict(fields=[]), response=dict(fields=[]),
                                  irrelevant=dict(fields=[dict(name="unrelated")])) )

    def call(self, method, params, **_):
        self.calls.append((method, copy.deepcopy(params)))
        if method == "session.describe":
            result = self.catalog()
        elif method == "session.policy_source":
            result = dict(sessionId=self.sid, characterGuid=self.guid, revision=checksum(self.source),
                          source=self.source, sourceLimitBytes=8 * 1024 * 1024)
        elif method == "session.policy_state":
            result = dict(sessionId=self.sid, characterGuid=self.guid, pendingActionIds=list(self.pending))
        elif method in ("buddy-actions.status", "buddy-actions.behavior"):
            result = dict(sessionId=self.sid, selfGuid=self.guid, revision=checksum(self.source))
        elif method == "buddy-actions.spell_toolkit":
            result = dict(sessionId=self.sid, selfGuid=self.guid, spellbookKnown=True,
                          spellbookRevision="7", spells=[dict(spellId=987654, genericCastSupported=True,
                          classificationReason="automatic_selection_unclassified", metadataKnown=False)])
        elif method == "buddy-world.snapshot":
            result = dict(self=dict(guid=self.guid, aliveKnown=True, alive=True))
        elif method in ("session.reload_policy", "session.send_event"):
            self.writes.append((method, copy.deepcopy(params)))
            if method == "session.reload_policy":
                if params["expectedRevision"] != checksum(self.source):
                    raise ValueError("native CAS rejected")
                self.source = params["source"]
                result = dict(revision=checksum(self.source), sessionId=self.sid, characterGuid=self.guid)
            else:
                result = dict(sequence="19", queued=True)
            if self.fail_mutation:
                raise TimeoutError("reply lost after entry")
        else:
            raise AssertionError("Unexpected RPC " + method)
        if self.hook:
            self.hook(method)
        return result


class DirectToolTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.edit = load_helper(ROOT / "dev/self-edit", "direct_test_real_self_edit")
        cls.gm = load_helper(ROOT / "dev/gm_tasks.py", "direct_test_real_gm_tasks")

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.folder = Path(self.temp.name).resolve()
        self.workspace = self.folder / "workspace"
        self.workspace.mkdir(mode=0o700)
        self.host = self.folder / "host"
        self.host.mkdir(mode=0o700)
        self.rpc = RPC()
        self.allowed = True
        self.gm_allowed = True  # These GM fixtures explicitly represent a current owner commission.
        self.cancelled = False
        self.edit.DATA = self.folder / "self-edit-cache"
        self.tools = DirectTools(self.rpc, self.rpc.sid, self.rpc.guid, self.workspace, self.host,
            allowed=lambda: self.allowed, cancelled=lambda: self.cancelled,
            helpers={"self-edit": self.edit}, request_id="owner-chat:9")

    def write(self, path="policy.lua", content=None, expected=""):
        return self.tools.dispatch("workspace_write", dict(path=path,
            content=content if content is not None else self.rpc.source, expected_sha256=expected))

    def test_installed_read_effect_only_and_catalog_filter(self):
        self.assertTrue(self.tools.dispatch("read_observation", dict(method="buddy-world.snapshot", params={}))['ok'])
        for method in ("buddy-actions.cast_spell", "buddy-trade.state", "absent.read"):
            self.assertFalse(self.tools.dispatch("read_observation", dict(method=method, params={}))['ok'])
        self.assertEqual(self.rpc.writes, [])
        selected = self.tools.dispatch("session_catalog", dict(method="buddy-actions.cast_spell"))['result']
        self.assertEqual(set(selected['messages']), {'request', 'response'})

    def test_read_detects_body_and_contract_replacement(self):
        for mode in ("body", "catalog"):
            self.rpc.sid, self.rpc.revision = "aw-123", "catalog-1"
            def hook(method):
                if method == "buddy-world.snapshot":
                    if mode == "body": self.rpc.sid = "aw-456"
                    else: self.rpc.revision = "catalog-2"
            self.rpc.hook = hook
            self.assertFalse(self.tools.dispatch("read_observation", dict(method="buddy-world.snapshot", params={}))['ok'])
        self.assertEqual(self.rpc.writes, [])

    def page(self, name, **args):
        result = self.tools.dispatch(name, args)
        self.assertTrue(result['ok'], result)
        self.assertLessEqual(len(packed(result)), READ_PAGE_BYTES)
        return result['result']

    def test_captured_context_is_immutable_and_full_array_and_unicode_pages_survive(self):
        captured = dict(pending=[dict(action_id='issued', phase='unknown')],
            inventory=dict(items=[dict(guid=str(i), name='Item '+str(i), count=i) for i in range(150)]),
            longText='😀界"\\' * 2000)
        original = copy.deepcopy(captured)
        self.tools = DirectTools(self.rpc, self.rpc.sid, self.rpc.guid, self.workspace, self.host,
            allowed=lambda: self.allowed, cancelled=lambda: self.cancelled, context=captured)
        captured['pending'][0]['phase'] = 'must-not-change-captured-facts'
        captured['inventory']['items'].clear()
        pending = self.page('context_read', path=['pending', '0', 'phase'])
        self.assertEqual(pending['value'], 'unknown')
        items, offset, hashes = [], 0, set()
        while True:
            view = self.page('context_read', path=['inventory', 'items'], offset=offset, limit=31)
            items.extend(row['value'] for row in view['rows'])
            hashes.add(view['viewSHA256'])
            if not view['truncated']: break
            self.assertGreater(view['nextOffset'], offset)
            offset = view['nextOffset']
        self.assertEqual(items, original['inventory']['items'])
        chunks, offset = [], 0
        while True:
            view = self.page('context_read', path=['longText'], offset=offset, limit=4096)
            chunks.append(view['value']); hashes.add(view['viewSHA256'])
            if not view['truncated']: break
            self.assertGreater(view['nextOffset'], offset)
            offset = view['nextOffset']
        self.assertEqual(''.join(chunks), original['longText'])
        self.assertEqual(len(hashes), 1)
        self.assertEqual(self.rpc.writes, [])

    def test_pure_context_read_needs_no_rpc_authority_and_paths_are_data_only(self):
        facts = dict(rows=[dict(value=42)], pending=dict(outcome='unknown'))
        self.assertEqual(context_page(facts, ['rows', '0', 'value'])['value'], 42)
        self.assertEqual(context_page(facts, ['rows', 0, 'value'])['value'], 42)
        self.assertEqual(context_tool_schema()['function']['parameters']['properties']['path']['items'], dict(type='string'))
        self.assertFalse(self.rpc.calls)
        for path in ('rows[0].value', ['rows', True], ['rows', '-1'], ['missing'], ['rows', '__class__']):
            with self.subTest(path=path), self.assertRaises(ValueError):
                context_page(facts, path)
        self.assertEqual(facts, dict(rows=[dict(value=42)], pending=dict(outcome='unknown')))

    def test_large_fresh_observation_projects_pages_after_existing_body_guards(self):
        original = self.rpc.call
        players = [dict(guid=str(i), name='Player '+str(i), detail='x'*900) for i in range(100)]
        def call(method, params, **kwargs):
            result = original(method, params, **kwargs)
            if method == 'buddy-world.snapshot': result['players'] = copy.deepcopy(players)
            return result
        self.rpc.call = call
        default = self.page('read_observation', method='buddy-world.snapshot', params={})
        summary = next(row for row in default['rows'] if row['key'] == 'players')
        self.assertTrue(summary['omitted']); self.assertEqual(summary['total'], 100)
        exact = self.page('read_observation', method='buddy-world.snapshot', params={}, path=['players', '99', 'detail'], limit=4096)
        self.assertEqual(exact['value'], players[-1]['detail'])
        self.assertFalse(exact['truncated'])
        self.rpc.hook = lambda method: setattr(self.rpc, 'sid', 'aw-replaced') if method == 'buddy-world.snapshot' else None
        self.assertFalse(self.tools.dispatch('read_observation', dict(method='buddy-world.snapshot', params={}, path=['players']))['ok'])
        self.assertFalse(self.rpc.writes)

    def test_catalog_index_and_exact_large_message_dependencies_are_pageable(self):
        original = self.rpc.catalog
        fields = [dict(name='field'+str(i), kind='string', description='exact-fact '*40) for i in range(300)]
        def catalog():
            result = original()
            result['methods'] += [dict(name='extra.read_'+str(i), effect='read', inputType='request', outputType='response') for i in range(100)]
            result['messages']['request']['fields'] = fields
            return result
        self.rpc.catalog = catalog
        index = self.page('session_catalog')
        self.assertEqual(index['type'], 'array')
        self.assertTrue(index['truncated'])
        self.assertEqual(len(index['rows']), 20)
        self.assertEqual(index['rows'][0]['value']['name'], 'buddy-actions.status')
        selected = self.page('session_catalog', method='buddy-actions.cast_spell', path=['messages', 'request', 'fields'], offset=299)
        self.assertEqual(selected['rows'][0]['value'], fields[-1])
        message = self.page('session_catalog', message='request', path=['message', 'fields'], offset=299)
        self.assertEqual(message['rows'][0]['value'], fields[-1])
        self.assertFalse(self.rpc.writes)

    def test_large_policy_default_is_metadata_and_long_single_source_line_can_be_read(self):
        self.rpc.source = "return {text='" + 'x'*330000 + "'}\n"
        default = self.page('policy_show')
        source_row = next(row for row in default['rows'] if row['key'] == 'source')
        self.assertTrue(source_row['omitted'])
        self.assertEqual(source_row['sha256'], checksum(self.rpc.source))
        self.assertNotIn('x'*1000, json.dumps(default))
        exact = self.page('policy_show', path=['source'], offset=len(self.rpc.source)-40, limit=4096)
        self.assertEqual(exact['value'], self.rpc.source[-40:])
        snapshot = self.tools.dispatch('policy_show', dict(save_as='snapshot.lua', expected_sha256=''))
        self.assertTrue(snapshot['ok'], snapshot)
        first = self.page('workspace_read', path='snapshot.lua')
        self.assertTrue(first['truncated']); self.assertGreater(first['nextOffset'], 0)
        tail = self.page('workspace_read', path='snapshot.lua', offset=len(self.rpc.source)-40, limit=4096)
        self.assertEqual(tail['content'], self.rpc.source[-40:])
        self.assertEqual(tail['sha256'], checksum(self.rpc.source))
        self.assertEqual(self.rpc.pending, ['issued-before-edit'])
        self.assertFalse(self.rpc.writes)

    def test_large_spell_index_pages_but_exact_learned_mechanics_are_complete(self):
        original = self.rpc.call
        rows = [dict(spellId=1000+i, metadataKnown=True, name='Learned '+str(i)+'x'*80,
                     genericCastSupported=True, mechanics=dict(effects=[dict(effect=6, rawFact='full')])) for i in range(500)]
        def call(method, params, **kwargs):
            result = original(method, params, **kwargs)
            if method == 'buddy-actions.spell_toolkit': result['spells'] = copy.deepcopy(rows)
            return result
        self.rpc.call = call
        default = self.page('spell_inspect')
        self.assertTrue(next(row for row in default['rows'] if row['key'] == 'spells')['omitted'])
        page = self.page('spell_inspect', path=['spells'], offset=499)
        self.assertEqual(page['rows'][0]['value']['spellId'], 1499)
        exact = self.tools.dispatch('spell_inspect', dict(spell_id=1499))
        self.assertTrue(exact['ok'], exact)
        self.assertEqual(exact['result']['spell'], rows[-1])
        rows[-1]['mechanics']['effects'][0]['rawFact'] = 'complete-raw-mechanic' * 2000
        oversized = self.page('spell_inspect', spell_id=1499)
        self.assertTrue(next(row for row in oversized['rows'] if row['key'] == 'spell')['omitted'])
        raw = self.page('spell_inspect', spell_id=1499,
            path=['spell', 'mechanics', 'effects', '0', 'rawFact'], offset=39980, limit=4096)
        self.assertEqual(raw['value'], rows[-1]['mechanics']['effects'][0]['rawFact'][39980:])
        self.assertFalse(self.rpc.writes)

    def test_authority_and_cancellation_hide_and_refuse_mutations(self):
        for value in (False, True):
            self.allowed, self.cancelled = value, value
            if not value:
                names = {t['function']['name'] for t in self.tools.tool_schemas()}
                self.assertNotIn('policy_apply', names)
            self.assertFalse(self.write()['ok'])
        self.assertEqual(self.rpc.writes, [])
        self.assertFalse((self.workspace / 'policy.lua').exists())

    def test_workspace_cas_patch_read_and_owned_path_guards(self):
        first = self.write(content='return {value=1}\n')
        self.assertTrue(first['ok'])
        old = first['result']['sha256']
        self.assertFalse(self.write(content='return {value=2}\n')['ok'])
        result = self.tools.dispatch('workspace_patch', dict(path='policy.lua', expected_sha256=old,
            edits=[dict(old='value=1', new='value=2')]))
        self.assertTrue(result['ok'])
        view = self.tools.dispatch('workspace_read', dict(path='policy.lua', find='value=2'))
        self.assertEqual(view['result']['content'], 'return {value=2}\n')
        for path in ('../outside.lua', '/tmp/outside.lua', 'config/auth.json', 'keys/token.json'):
            self.assertFalse(self.write(path=path)['ok'])
        outside = self.folder / 'outside.lua'
        outside.write_text('untouched')
        (self.workspace / 'linked.lua').symlink_to(outside)
        self.assertFalse(self.tools.dispatch('workspace_read', dict(path='linked.lua'))['ok'])
        (self.workspace / 'directory').symlink_to(self.folder, target_is_directory=True)
        self.assertFalse(self.write(path='directory/outside.lua')['ok'])
        (self.workspace / 'hard.lua').hardlink_to(outside)
        self.assertFalse(self.tools.dispatch('workspace_read', dict(path='hard.lua'))['ok'])
        self.assertEqual(outside.read_text(), 'untouched')

    def test_source_snapshot_patch_and_actual_maintained_cas_preserve_pending(self):
        original = self.rpc.source
        snap = self.tools.dispatch('policy_show', dict(save_as='policy.lua', expected_sha256=''))
        self.assertTrue(snap['ok'], snap)
        self.assertNotIn('source', snap['result'])
        old = snap['result']['revision']
        change = self.tools.dispatch('workspace_patch', dict(path='policy.lua', expected_sha256=old,
            edits=[dict(old='value=f.value', new='value=f.value+1')]))
        self.assertTrue(change['ok'], change)
        result = self.tools.dispatch('policy_apply', dict(path='policy.lua', expected_revision=old))
        self.assertTrue(result['ok'], result)
        self.assertNotEqual(self.rpc.source, original)
        self.assertEqual(len(self.rpc.writes), 1)
        self.assertEqual(self.rpc.pending, ['issued-before-edit'])
        self.assertTrue((self.edit.DATA / '4' / (checksum(self.rpc.source)+'.lua')).exists())

    def test_changed_source_or_late_authority_reject_before_native_write(self):
        self.assertTrue(self.write()['ok'])
        old = checksum(self.rpc.source)
        self.rpc.source += '-- newer own edit\n'
        self.assertFalse(self.tools.dispatch('policy_apply', dict(path='policy.lua', expected_revision=old))['ok'])
        self.assertEqual(self.rpc.writes, [])
        self.rpc.hook = lambda method: setattr(self, 'allowed', False) if method == 'session.policy_source' else None
        self.assertFalse(self.tools.dispatch('policy_apply', dict(path='policy.lua', expected_revision=checksum(self.rpc.source)))['ok'])
        self.assertEqual(self.rpc.writes, [])

    def test_owner_change_during_final_catalog_after_real_candidate_save_is_zero_write(self):
        for change in ('cancel', 'authority'):
            with self.subTest(change=change):
                self.allowed, self.cancelled = True, False
                candidate = self.rpc.source + '-- candidate ' + change + '\n'
                path = 'candidate-' + change + '.lua'
                self.assertTrue(self.write(path=path, content=candidate)['ok'])
                saved = self.edit.DATA / '4' / (checksum(candidate) + '.lua')
                self.assertFalse(saved.exists())
                observed = []
                def hook(method):
                    # The real self-edit helper saves its candidate only after
                    # its source reads, before its final admission catalog read.
                    if method == 'session.describe' and saved.exists():
                        observed.append(method)
                        if change == 'cancel': self.cancelled = True
                        else: self.allowed = False
                self.rpc.hook = hook
                before = self.rpc.source
                result = self.tools.dispatch('policy_apply', dict(path=path,
                    expected_revision=checksum(before)))
                self.assertTrue(observed)
                self.assertTrue(saved.exists())
                self.assertFalse(result['ok'], result)
                self.assertEqual(self.rpc.source, before)
                self.assertEqual(self.rpc.writes, [])
                self.assertIsNone(self.tools.unknown_receipt)
                self.rpc.hook = None
        self.allowed, self.cancelled = True, False

    def test_workspace_cancel_during_final_catalog_or_temp_sync_never_replaces(self):
        observed = []
        def hook(method):
            if method == 'session.describe' and (self.workspace/'.direct-tools-edit.lock').exists():
                observed.append(method)
                self.cancelled = True
        self.rpc.hook = hook
        self.assertFalse(self.write(path='catalog.lua')['ok'])
        self.assertTrue(observed)
        self.assertFalse((self.workspace/'catalog.lua').exists())
        self.rpc.hook, self.cancelled = None, False
        sync = os.fsync
        def fsync(fd):
            value = sync(fd)
            if stat.S_ISREG(os.fstat(fd).st_mode): self.cancelled = True
            return value
        with mock.patch('direct_tools.os.fsync', side_effect=fsync):
            self.assertFalse(self.write(path='sync.lua')['ok'])
        self.assertFalse((self.workspace/'sync.lua').exists())
        self.assertIsNone(self.tools.unknown_receipt)

    def test_actual_apply_lost_reply_halts_and_retains_exact_candidate(self):
        candidate = self.rpc.source + '-- actual new policy\n'
        self.assertTrue(self.write(content=candidate)['ok'])
        old = checksum(self.rpc.source)
        self.rpc.fail_mutation = True
        with self.assertRaises(ToolOutcomeUnknown) as caught:
            self.tools.dispatch('policy_apply', dict(path='policy.lua', expected_revision=old))
        self.assertFalse(caught.exception.receipt['replayAllowed'])
        self.assertEqual(caught.exception.receipt['targetRevision'], checksum(candidate))
        self.assertEqual(len(self.rpc.writes), 1)
        self.assertEqual(self.rpc.pending, ['issued-before-edit'])
        self.assertTrue((self.edit.DATA / '4' / (checksum(candidate)+'.lua')).exists())

    def test_future_learned_spell_is_not_blocked_by_classifier(self):
        result = self.tools.dispatch('spell_inspect', dict(spell_id=987654))
        self.assertTrue(result['ok'], result)
        self.assertTrue(result['result']['learned'])
        self.assertTrue(result['result']['spell']['genericCastSupported'])
        self.assertTrue(result['result']['effectClassificationIsNotCastAvailability'])
        self.assertEqual(self.rpc.writes, [])

    def test_structured_input_cannot_forge_receipts_and_unknown_is_one_write(self):
        self.assertFalse(self.tools.dispatch('policy_input', dict(value={}, kind='action_result'))['ok'])
        self.rpc.fail_mutation = True
        with self.assertRaises(ToolOutcomeUnknown):
            self.tools.dispatch('policy_input', dict(value=dict(task='cast_spell', id='owner-9', spellId=987654)))
        self.assertEqual(len(self.rpc.writes), 1)
        method, params = self.rpc.writes[0]
        self.assertEqual((method, params['kind'], params['sessionId'], params['characterGuid']),
                         ('session.send_event', 'command', 'aw-123', '4'))
        retry = self.tools.dispatch('policy_input', dict(value=dict(id='another-id')))
        self.assertFalse(retry['ok'])
        self.assertEqual(len(self.rpc.writes), 1)
        self.assertIsNotNone(self.tools.unknown_receipt)

    def test_local_replace_followed_by_uncertain_fsync_is_not_no_dispatch(self):
        original = os.fsync
        def fsync(fd):
            if stat.S_ISDIR(os.fstat(fd).st_mode):
                raise OSError('directory sync uncertain')
            return original(fd)
        with mock.patch('direct_tools.os.fsync', side_effect=fsync):
            with self.assertRaises(ToolOutcomeUnknown) as caught:
                self.write(content='return {}\n')
        self.assertEqual((self.workspace/'policy.lua').read_text(), 'return {}\n')
        self.assertEqual(caught.exception.receipt['targetSHA256'], checksum('return {}\n'))
        self.assertEqual(self.rpc.writes, [])

    def test_evaluator_runs_only_static_binary_with_bounded_frame_no_actions(self):
        self.assertTrue(self.write()['ok'])
        calls = []
        def runner(argv, **kwargs):
            calls.append((argv, kwargs))
            self.assertEqual(argv[0], '/fixed/native/luapolicy')
            self.assertNotIn('shell', kwargs)
            self.assertEqual(json.loads(kwargs['input']), {'value': 2})
            return subprocess.CompletedProcess(argv, 0, b'{"output":{"value":2}}\n', b'')
        self.tools.runner = runner
        self.tools.helpers['policy_binary'] = Path('/fixed/native/luapolicy')
        result = self.tools.dispatch('lua_evaluate', dict(path='policy.lua', frame={'value': 2}))
        self.assertTrue(result['ok'], result)
        self.assertTrue(result['result']['valid'])
        self.assertEqual(len(calls), 1)
        self.assertEqual(self.rpc.writes, [])

    def test_actual_maintained_evaluator_if_available(self):
        binary = Path('/Users/jmax/dev/warcraft_buddy/data/bin/luapolicy')
        if not binary.is_file(): self.skipTest('existing evaluator not present; no build/download')
        self.assertTrue(self.write()['ok'])
        self.tools.helpers['policy_binary'] = binary
        result = self.tools.dispatch('lua_evaluate', dict(path='policy.lua', frame={'value': 12}))
        self.assertTrue(result['ok'], result)
        self.assertTrue(result['result']['valid'], result)
        self.assertEqual(result['result']['decision']['output']['value'], 12)
        self.assertEqual(self.rpc.writes, [])

    def gm_tools(self, runner):
        self.rpc = RPC('87')
        return DirectTools(self.rpc, self.rpc.sid, '87', self.workspace, self.host,
            allowed=lambda: self.allowed, gm_allowed=lambda: self.gm_allowed,
            request_id='actual-owner:seq9', runner=runner,
            helpers={'gm_tasks': self.gm, 'policy_binary': '/fixed/luapolicy',
                     'gm_bindings': lambda: [dict(guid='4', rpc_url='http://127.0.0.1:8087/rpc')]})

    def test_gm_mutations_default_deny_while_peer_lua_development_and_status_remain(self):
        self.rpc = RPC('87')
        runner = mock.Mock(side_effect=AssertionError('Peer development cannot start a GM helper'))
        tools = DirectTools(self.rpc, self.rpc.sid, '87', self.workspace, self.host,
            allowed=lambda: True, request_id='verified-peer:9', runner=runner,
            helpers={'gm_tasks': self.gm})
        names = {row['function']['name'] for row in tools.tool_schemas()}
        self.assertTrue({'workspace_write', 'policy_apply', 'policy_input', 'gm_task_status'} <= names)
        self.assertFalse({'gm_task_start', 'gm_task_cancel'} & names)
        self.assertFalse(tools.dispatch('gm_task_start', dict(goal=dict(members=[])))['ok'])
        self.assertFalse(tools.dispatch('gm_task_cancel', dict(task_id='direct-'+'a'*24))['ok'])
        for name, args in (('gm_task_start', dict(goal=dict(members=[]))),
                           ('gm_task_cancel', dict(task_id='direct-'+'a'*24))):
            with self.assertRaisesRegex(ValueError, 'current_owner_gm_authority_required'):
                tools._gm(name, args)  # The execution guard also refuses a bypass of schema filtering.
        self.assertFalse((self.host/'data/converse/lotharen/gm-tasks').exists())
        self.assertFalse(self.rpc.writes)
        runner.assert_not_called()

    def test_gm_owner_authority_lost_during_final_catalog_preserves_journal_without_child(self):
        runner = mock.Mock(side_effect=AssertionError('Lost GM authority cannot enter helper'))
        tools = self.gm_tools(runner)
        observed = []
        def hook(method):
            state_root = self.host/'data/converse/lotharen/gm-tasks'
            if method == 'session.describe' and list(state_root.glob('direct-*/state.json')):
                self.gm_allowed = False
                observed.append(method)
        self.rpc.hook = hook
        result = tools.dispatch('gm_task_start', dict(goal=dict(members=[])))
        self.assertFalse(result['ok'], result)
        self.assertIn('current_owner_gm_authority_required', result['error']['error'])
        self.assertTrue(observed)
        self.assertTrue(self.allowed)  # General Lua development was not revoked.
        retained = next((self.host/'data/converse/lotharen/gm-tasks').glob('direct-*/state.json'))
        self.assertEqual(json.loads(retained.read_text())['reservations'], {})
        self.assertIsNone(tools.unknown_receipt)
        self.assertFalse(self.rpc.writes)
        runner.assert_not_called()

    def test_gm_cancel_needs_current_owner_but_status_preserves_unknown_task(self):
        runner = mock.Mock(return_value=subprocess.CompletedProcess([], 0, b'{"started":true}', b''))
        tools = self.gm_tools(runner)
        started = tools.dispatch('gm_task_start', dict(goal=dict(members=[])))
        task_id = started['result']['taskId']
        folder = tools._gm_folder(task_id)
        with self.gm.task_store(folder) as store:
            data = store.load()
            data['reservations']['exact-unknown'] = dict(outcome='unknown', id='original-no-replay')
            store.save(data)
        before = self.gm.task_store(folder).load()
        self.gm_allowed = False
        self.assertFalse(tools.dispatch('gm_task_cancel', dict(task_id=task_id))['ok'])
        self.assertTrue(tools.dispatch('gm_task_status', dict(task_id=task_id))['ok'])
        self.assertEqual(self.gm.task_store(folder).load(), before)
        self.assertEqual(runner.call_count, 1)
        self.assertFalse(self.rpc.writes)

    def test_gm_whole_task_uses_existing_journal_and_exact_one_helper(self):
        calls = []
        def runner(argv, **kwargs):
            calls.append(argv)
            self.assertEqual(argv[2:4], [str(self.host/'dev/gm-task'), 'start'])
            self.assertNotIn('shell', kwargs)
            folder = Path(argv[argv.index('--state-dir')+1])
            data = self.gm.task_store(folder).load()
            self.assertEqual(data['identity'], dict(sessionId='aw-123', guid='87', rpcUrl=self.rpc.url))
            self.assertEqual(data['reservations'], {})
            self.assertEqual(data['memory'], {})
            self.assertEqual(argv[argv.index('--policy-binary')+1], '/fixed/luapolicy')
            return subprocess.CompletedProcess(argv, 0, b'{"started":true,"pid":321}', b'')
        tools = self.gm_tools(runner)
        goal = dict(player=dict(guid='3', name='Joel'), members=[dict(guid='4', name='Mira', rpcUrl=self.rpc.url)])
        result = tools.dispatch('gm_task_start', dict(goal=goal))
        self.assertTrue(result['ok'], result)
        task_id = result['result']['taskId']
        self.assertFalse(tools.dispatch('gm_task_start', dict(goal=goal))['ok'])
        self.assertEqual(len(calls), 1)
        status = tools.dispatch('gm_task_status', dict(task_id=task_id))
        self.assertTrue(status['ok'], status)
        self.assertEqual(status['result']['result']['phase'], 'starting')
        self.assertFalse(status['result']['gameEffectClaimed'])
        self.assertEqual(self.rpc.writes, [])

    def test_retained_gm_status_pages_large_facts_without_execution_or_state_change(self):
        runner = mock.Mock(side_effect=AssertionError('Status must not start a task/helper'))
        tools = self.gm_tools(runner)
        task_id = 'direct-' + 'a'*24
        folder = tools._gm_folder(task_id)
        with self.gm.task_store(folder) as store:
            host = self.gm.Host(store, self.rpc, None, None, dict(id=task_id, members=[]),
                self.host/'behaviors/gm-recovery.lua', self.host/'behaviors/gm-recovery-hold.lua')
            host.data['identity'] = dict(sessionId=self.rpc.sid, guid='87', rpcUrl=self.rpc.url)
            host.data['lastObservations'] = dict(threats=[dict(guid=str(i), raw='x'*1000) for i in range(100)])
            host.data['memory'] = dict(pending=dict(id='unknown-original', outcome='unknown'))
            host.persist()
        before = self.gm.task_store(folder).load()
        result = tools.dispatch('gm_task_status', dict(task_id=task_id))
        self.assertTrue(result['ok'], result)
        self.assertLessEqual(len(packed(result)), READ_PAGE_BYTES)
        self.assertTrue(next(row for row in result['result']['rows'] if row['key'] == 'result')['omitted'])
        exact = tools.dispatch('gm_task_status', dict(task_id=task_id,
            path=['result', 'lastObservations', 'threats', '99', 'raw'], limit=4096))
        self.assertTrue(exact['ok'], exact)
        self.assertLessEqual(len(packed(exact)), READ_PAGE_BYTES)
        self.assertEqual(exact['result']['value'], 'x'*1000)
        self.assertEqual(self.gm.task_store(folder).load(), before)
        runner.assert_not_called()
        self.assertFalse(self.rpc.writes)

    def test_gm_unknown_start_is_retained_and_cannot_start_again(self):
        calls = []
        def runner(argv, **kwargs):
            calls.append(argv)
            raise subprocess.TimeoutExpired(argv, 15)
        tools = self.gm_tools(runner)
        with self.assertRaises(ToolOutcomeUnknown) as caught:
            tools.dispatch('gm_task_start', dict(goal=dict(members=[])))
        folder = Path(caught.exception.receipt['stateDir'])
        self.assertEqual(self.gm.task_store(folder).load()['identity']['sessionId'], 'aw-123')
        self.assertFalse(tools.dispatch('gm_task_start', dict(goal=dict(members=[])))['ok'])
        self.assertEqual(len(calls), 1)
        self.rpc.sid = 'aw-456'
        self.assertFalse(tools.dispatch('gm_task_cancel', dict(task_id=folder.name))['ok'])
        self.assertEqual(len(calls), 1)

    def test_gm_owner_cancel_during_catalog_after_real_reservation_is_zero_child(self):
        runner = mock.Mock(side_effect=AssertionError('cancelled request cannot start a helper'))
        tools = self.gm_tools(runner)
        observed = []
        def hook(method):
            state_root = self.host/'data/converse/lotharen/gm-tasks'
            if method == 'session.describe' and list(state_root.glob('direct-*/state.json')):
                observed.append(method)
                self.cancelled = True
        tools.cancelled = lambda: self.cancelled
        self.rpc.hook = hook
        result = tools.dispatch('gm_task_start', dict(goal=dict(members=[])))
        self.assertTrue(observed)
        self.assertFalse(result['ok'], result)
        runner.assert_not_called()
        self.assertEqual(self.rpc.writes, [])
        journal = next((self.host/'data/converse/lotharen/gm-tasks').glob('direct-*/state.json'))
        retained = json.loads(journal.read_text())
        self.assertEqual(retained['identity']['sessionId'], 'aw-123')
        self.assertEqual(retained['reservations'], {})
        self.assertIsNone(tools.unknown_receipt)

    def test_lua_helper_cancel_observed_after_source_read_is_zero_child(self):
        self.assertTrue(self.write()['ok'])
        runner = mock.Mock(side_effect=AssertionError('cancelled request cannot evaluate'))
        self.tools.runner = runner
        source_read = []
        read_file = self.tools._read_file
        def read(path):
            value = read_file(path)
            source_read.append(path)
            return value
        self.tools._read_file = read
        self.rpc.hook = lambda method: setattr(self, 'cancelled', True) if (
            method == 'session.describe' and source_read) else None
        result = self.tools.dispatch('lua_evaluate', dict(path='policy.lua', frame={}))
        self.assertTrue(source_read)
        self.assertFalse(result['ok'], result)
        runner.assert_not_called()

    def test_gm_rejects_unobserved_member_and_non_gm(self):
        tools = self.gm_tools(mock.Mock(side_effect=AssertionError('no child permitted')))
        result = tools.dispatch('gm_task_start', dict(goal=dict(members=[dict(guid='8', name='Nixwick', rpcUrl='http://127.0.0.1:1/rpc')])))
        self.assertFalse(result['ok'])
        self.assertFalse((self.host/'data/converse/lotharen/gm-tasks').exists())
        self.assertFalse(self.tools.dispatch('gm_task_start', dict(goal={}))['ok'])

    def test_production_gm_helper_filename_is_importable_without_injection(self):
        tools = DirectTools(self.rpc, self.rpc.sid, '4', self.workspace, ROOT)
        helper = tools._helper('gm_tasks')
        self.assertEqual(Path(helper.__file__), ROOT / 'dev/gm_tasks.py')
        self.assertTrue(callable(helper.Host))
        self.assertTrue(callable(helper.task_store))

    def test_existing_gm_run_rejects_replaced_body_before_shared_writer_claim(self):
        calls = []
        def runner(argv, **kwargs):
            calls.append(argv)
            return subprocess.CompletedProcess(argv, 0, b'{"started":true}', b'')
        tools = self.gm_tools(runner)
        started = tools.dispatch('gm_task_start', dict(goal=dict(members=[])))
        self.assertTrue(started['ok'], started)
        config = self.host/'data/converse/lotharen/gm-transport.json'
        config.write_text('{}')
        replaced = RPC('87')
        replaced.sid = 'aw-456'
        args = list(calls[0][3:])
        args[0] = 'run'
        with mock.patch.object(self.gm, 'task_rpc', return_value=replaced), \
                mock.patch.object(self.gm, 'claim_writer') as claim:
            with self.assertRaisesRegex(ValueError, 'another GM body'):
                self.gm.main(args)
        claim.assert_not_called()
        self.assertEqual(replaced.writes, [])

    def test_gm_cancel_uses_only_existing_bound_helper(self):
        calls = []
        def runner(argv, **kwargs):
            calls.append(argv)
            return subprocess.CompletedProcess(argv, 0, b'{"cancelRequested":true}', b'')
        tools = self.gm_tools(runner)
        started = tools.dispatch('gm_task_start', dict(goal=dict(members=[])))
        task_id = started['result']['taskId']
        cancelled = tools.dispatch('gm_task_cancel', dict(task_id=task_id))
        self.assertTrue(cancelled['ok'], cancelled)
        self.assertEqual(calls[1][2:4], [str(self.host/'dev/gm-task'), 'cancel'])
        self.assertFalse(cancelled['result']['gameEffectClaimed'])
        self.assertEqual(self.rpc.writes, [])


if __name__ == '__main__':
    unittest.main()
