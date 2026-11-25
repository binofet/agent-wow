import copy
import hashlib
import json
import os
import re
from pathlib import Path
import subprocess
import stat
import struct
import tempfile
import unittest
from unittest import mock

from direct_tools import DirectTools, ToolOutcomeUnknown, load_helper, context_page, context_tool_schema, packed, READ_PAGE_BYTES, GM_INSPECT_BYTES

ROOT = Path(__file__).resolve().parent.parent


def checksum(value):
    return hashlib.sha256(value.encode()).hexdigest()


class RPC:
    url = "http://127.0.0.1:8087/rpc"

    def __init__(self, guid="4"):
        self.sid, self.guid, self.revision = "aw-123", guid, "catalog-1"
        self.chat_sid = "chat-distinct-456"
        self.source = "return {decide=function(f) return {output={value=f.value}} end}\n"
        self.pending = ["issued-before-edit"]
        self.calls, self.writes = [], []
        self.fail_mutation = False
        self.hook = None
        self.last_failure = None

    def catalog(self):
        rows = [dict(name=n, effect=e, inputType="request", outputType="response") for n, e in [
            ("buddy-actions.status", "read"), ("buddy-actions.behavior", "read"),
            ("buddy-chat.poll", "read"),
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
        elif method == "buddy-chat.poll":
            result = dict(sessionId=self.chat_sid, messages=[dict(sequence="10",text="Actual system feedback", channel="CHANNEL_UNSPECIFIED")],
                          nextSequence="10",latestSequence="10",oldestSequence="10")
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
            result = dict(self=dict(guid=self.guid,present=True,mapKnown=True,mapId=349,targetKnown=True,
                                   targetGuid=str(getattr(self,'target',0)),aliveKnown=True,alive=True),
                          owner=dict(guid='3',present=True,mapKnown=True,mapId=349))
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

    def chat_tools(self, chat_session_id):
        return DirectTools(self.rpc, self.rpc.sid, self.rpc.guid, self.workspace, self.host,
            allowed=lambda: self.allowed, cancelled=lambda: self.cancelled,
            helpers={"self-edit": self.edit}, chat_session_id=chat_session_id)

    def legacy_poll_catalog(self):
        # Build the fixture from the maintained public .proto, not the adapter's
        # expected tuple table. Legacy catalogs omit Poll's read declaration.
        catalog = self.rpc.catalog()
        prefix = 'buddy-chat::warcraftbuddy.chat.v1.'
        proto = (ROOT/'modules/buddy-chat/chat.proto').read_text()
        for name in ('PollRequest', 'PollResponse', 'Message'):
            body = re.search(r'message '+name+r' \{(.*?)\n\}', proto, re.S).group(1)
            fields = []
            for repeated, kind, field, number in re.findall(r'(repeated\s+)?(\w+)\s+(\w+)\s*=\s*(\d+)\s*;', body):
                json_name = re.sub(r'_([a-z])', lambda match: match[1].upper(), field)
                scalar = kind in ('uint64','uint32','int32','bool','string')
                fields.append(dict(name=json_name,number=int(number),kind=kind if scalar else 'enum' if kind=='Channel' else 'message',
                    type=kind if scalar else prefix+kind,repeated=bool(repeated),map=False,optional=False,required=False))
            catalog['messages'][prefix+name] = dict(protobufName='warcraftbuddy.chat.v1.'+name,syntax='proto3',fields=fields)
        metadata = next(row for row in catalog['methods'] if row['name']=='buddy-chat.poll')
        metadata.update(module='buddy-chat',path='/warcraftbuddy.chat.v1.BuddyChat/Poll',
            inputType=prefix+'PollRequest',outputType=prefix+'PollResponse',effect='unspecified')
        return catalog

    def test_typed_chat_read_accepts_exact_legacy_contract_without_generic_effect_bypass(self):
        catalog = self.legacy_poll_catalog()
        tools = self.chat_tools(self.rpc.chat_sid)
        with mock.patch.object(self.rpc,'catalog',return_value=catalog):
            generic = tools.dispatch('read_observation',dict(method='buddy-chat.poll',params={}))
            self.assertFalse(generic['ok']);self.assertIn('use chat_read',generic['error']['error'])
            self.assertFalse(any(method=='buddy-chat.poll' for method,_ in self.rpc.calls))
            first = tools.dispatch('chat_read',dict(after_sequence='9',limit=12))
            again = tools.dispatch('chat_read',dict(after_sequence='9',limit=12))
            self.assertTrue(first['ok'],first);self.assertEqual(first,again)
            self.assertEqual(first['result']['messages'][0]['text'],'Actual system feedback')
            self.assertEqual([params for method,params in self.rpc.calls if method=='buddy-chat.poll'],
                [dict(afterSequence='9',limit=12,sessionId=self.rpc.chat_sid)]*2)
            # An unrelated undeclared RPC still has exactly the generic guard.
            self.assertFalse(tools.dispatch('read_observation',dict(method='buddy-trade.state',params={}))['ok'])
        self.assertFalse(self.rpc.writes)

    def test_gm_command_reference_is_present_without_any_private_reference_file(self):
        tools = self.gm_tools(mock.Mock())
        self.assertFalse((self.host/'dev/lotharen-gm-commands.txt').exists())
        description = next(row['function']['description'] for row in tools.tool_schemas()
                           if row['function']['name']=='gm_execute')
        for form in ('.go xyz <x> <y> <z> [mapId] [orientation]', '.appear <playerName>',
                     '.summon <playerName>', '.gps <playerName>', 'otherwise the GM self',
                     'no target name/GUID', '.help <command>', 'chat_read', 'BEFORE'):
            self.assertIn(form,description)
        self.assertFalse(self.rpc.writes)

    def test_typed_chat_reader_rejects_spoofed_route_signature_effect_and_identity(self):
        original = self.legacy_poll_catalog()
        prefix = 'buddy-chat::warcraftbuddy.chat.v1.'
        for change in ('path','module','inputType','outputType','effect','missing','inputfield','outputfield','messagefield'):
            with self.subTest(change=change):
                catalog = copy.deepcopy(original)
                row = next(row for row in catalog['methods'] if row['name']=='buddy-chat.poll')
                if change in ('path','module','inputType','outputType'):row[change]='wrong-mutation-contract'
                elif change=='effect':row['effect']='mutation'
                elif change=='missing':catalog['methods'].remove(row)
                else:
                    name={'inputfield':'PollRequest','outputfield':'PollResponse','messagefield':'Message'}[change]
                    catalog['messages'][prefix+name]['fields'][0]['kind']='string'
                self.rpc.calls.clear()
                with mock.patch.object(self.rpc,'catalog',return_value=catalog):
                    result = self.chat_tools(self.rpc.chat_sid).dispatch('chat_read',{})
                self.assertFalse(result['ok'],result)
                self.assertFalse(any(method=='buddy-chat.poll' for method,_ in self.rpc.calls))
        with mock.patch.object(self.rpc,'catalog',return_value=original):
            for args in (dict(method='buddy-chat.send'),dict(session_id=self.rpc.sid),dict(limit=True),
                         dict(limit=257),dict(after_sequence=str(2**64))):
                self.rpc.calls.clear()
                self.assertFalse(self.chat_tools(self.rpc.chat_sid).dispatch('chat_read',args)['ok'])
                self.assertFalse(any(method=='buddy-chat.poll' for method,_ in self.rpc.calls))
            self.assertFalse(self.chat_tools(None).dispatch('chat_read',{})['ok'])
        self.assertFalse(self.rpc.writes)

    def test_typed_chat_reader_rechecks_core_chat_and_catalog_after_nonconsuming_poll(self):
        original = self.legacy_poll_catalog()
        for change in ('core','chat','catalog'):
            with self.subTest(change=change):
                self.rpc.sid,self.rpc.chat_sid='aw-123','chat-distinct-456'
                catalog=copy.deepcopy(original)
                tools=self.chat_tools(self.rpc.chat_sid)
                original_call=self.rpc.call
                def call(method,params,**kwargs):
                    value=original_call(method,params,**kwargs)
                    if method=='buddy-chat.poll':
                        if change=='core':catalog['sessionId']='aw-replacement'
                        elif change=='chat':value['sessionId']='chat-replacement'
                        else:catalog['revision']='new-contract-revision'
                    return value
                with mock.patch.object(self.rpc,'catalog',side_effect=lambda:copy.deepcopy(catalog)),mock.patch.object(self.rpc,'call',side_effect=call):
                    self.assertFalse(tools.dispatch('chat_read',{})['ok'])
        self.assertFalse(self.rpc.writes)

    def test_actual_fake_api_reads_legacy_system_feedback_through_typed_tool(self):
        from direct_model import DirectModel
        from test_direct_model import FakeHTTP, chat
        catalog = self.legacy_poll_catalog()
        tools = self.chat_tools(self.rpc.chat_sid)
        reply = dict(choices=[dict(finish_reason='stop',message=dict(role='assistant',content='I can read the realm feedback.'))])
        with mock.patch.object(self.rpc,'catalog',return_value=catalog),FakeHTTP([
                chat(('feedback','chat_read',dict(after_sequence='9',limit=20))),reply]) as server:
            client = DirectModel('local','fixture',server.base+'/v1')
            result = client.generate('Use current factual feedback.',[dict(role='user',content='What did the realm reply?')],
                None,tools.tool_schemas(),tools.dispatch,natural=True,conversation_key='bound-core-and-chat',
                read_only_tools={'chat_read'})
            self.assertEqual(result['text'],'I can read the realm feedback.')
            wire = server.requests[1]['body']['messages'][-1]
            self.assertEqual(wire['role'],'tool')
            feedback = json.loads(wire['content'])
            self.assertTrue(feedback['ok'])
            self.assertEqual(feedback['result']['sessionId'],self.rpc.chat_sid)
            self.assertEqual(feedback['result']['messages'][0]['text'],'Actual system feedback')
        self.assertEqual([params for method,params in self.rpc.calls if method=='buddy-chat.poll'],
                         [dict(afterSequence='9',limit=20,sessionId=self.rpc.chat_sid)])
        self.assertFalse(self.rpc.writes)

    def test_chat_poll_uses_trusted_distinct_chat_session_and_keeps_core_reads_bound(self):
        tools = self.chat_tools(self.rpc.chat_sid)
        result = tools.dispatch("read_observation", dict(method="buddy-chat.poll", params=dict(afterSequence="9", limit=20)))
        self.assertTrue(result["ok"], result)
        self.assertEqual(result["result"]["sessionId"], self.rpc.chat_sid)
        self.assertEqual(result["result"]["messages"][0]["text"], "Actual system feedback")
        calls = [params for method, params in self.rpc.calls if method == "buddy-chat.poll"]
        self.assertEqual(calls, [dict(afterSequence="9", limit=20, sessionId=self.rpc.chat_sid)])
        self.assertNotEqual(self.rpc.sid, self.rpc.chat_sid)
        native = tools.dispatch("read_observation", dict(method="session.policy_state", params={}))
        self.assertTrue(native["ok"], native)
        self.assertEqual(next(params for method, params in self.rpc.calls if method == "session.policy_state"),
                         dict(sessionId=self.rpc.sid, characterGuid=self.rpc.guid))
        self.assertEqual(native["result"]["sessionId"], self.rpc.sid)
        self.assertFalse(self.rpc.writes)

    def test_chat_poll_requires_binding_and_refuses_caller_foreign_or_core_session_before_call(self):
        for trusted, supplied in ((None, None), (self.rpc.chat_sid, self.rpc.sid),
                                  (self.rpc.chat_sid, "chat-foreign")):
            with self.subTest(trusted=trusted, supplied=supplied):
                self.rpc.calls.clear()
                params = {} if supplied is None else dict(sessionId=supplied)
                result = self.chat_tools(trusted).dispatch("read_observation", dict(method="buddy-chat.poll", params=params))
                self.assertFalse(result["ok"], result)
                self.assertFalse(any(method == "buddy-chat.poll" for method, _ in self.rpc.calls))
        self.assertFalse(self.rpc.writes)

    def test_chat_poll_refuses_replacement_chat_or_native_body_and_missing_response_identity(self):
        for mode in ("chat", "core", "missing"):
            with self.subTest(mode=mode):
                self.rpc.sid, self.rpc.chat_sid = "aw-123", "chat-distinct-456"
                tools = self.chat_tools(self.rpc.chat_sid)
                original = self.rpc.call
                def call(method, params, **kwargs):
                    result = original(method, params, **kwargs)
                    if method == "buddy-chat.poll":
                        if mode == "chat": result["sessionId"] = "chat-replacement"
                        elif mode == "core": self.rpc.sid = "aw-replacement"
                        else: result.pop("sessionId")
                    return result
                with mock.patch.object(self.rpc, "call", side_effect=call):
                    result = tools.dispatch("read_observation", dict(method="buddy-chat.poll", params={}))
                self.assertFalse(result["ok"], result)
                self.assertIn("read_result_chat_body_changed" if mode != "core" else "current_body_changed",
                              result["error"]["error"])
        self.assertFalse(self.rpc.writes)

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

    def test_default_context_text_reads_whole_311_character_command_section(self):
        # Retained public .gear entry: the old default returned only separators.
        section = ('=' * 72 + '\n.gear\n'
            'Published default minimum security: unspecified (no command.sql row).\n'
            'Group registration: cs_gear.cpp / commandTable -> gearCommandTable\n'
            'Published help: not supplied at this pin. Native signature above; live realm help may differ.\n\n')
        self.assertEqual(len(section), 311)
        context = dict(gmReference=dict(commands={'.gear': dict(text=section)}),
                       pending=dict(outcome='unknown', action_id='already-issued'))
        self.tools.context = copy.deepcopy(context)
        path = ['gmReference', 'commands', '.gear', 'text']
        page = self.page('context_read', path=path)
        self.assertEqual(page['value'], section)
        self.assertEqual((page['total'], page['nextOffset'], page['omitted']), (311, 311, 0))
        self.assertFalse(page['truncated'])
        self.assertEqual(context_page(context, path)['value'], section)  # No-workspace reader uses the same default.
        explicit = self.page('context_read', path=path, limit=20)
        self.assertEqual(explicit['value'], '=' * 20)
        self.assertEqual(explicit['nextOffset'], 20)
        self.assertEqual(self.tools.context, context)
        self.assertEqual(self.rpc.pending, ['issued-before-edit'])
        self.assertFalse(self.rpc.writes)

    def test_context_defaults_are_type_specific_and_explicit_limits_keep_their_units(self):
        text = 'abcdefghijklmnopqrstuvwxyz' * 400
        self.tools.context = dict(text=text, rows=list(range(40)), fields={str(i): i for i in range(40)})
        default = self.page('context_read', path=['text'])
        self.assertEqual(default['value'], text[:4096])
        self.assertEqual(default['nextOffset'], 4096)
        self.assertTrue(default['truncated'])
        explicit = self.page('context_read', path=['text'], offset=100, limit=23)
        self.assertEqual(explicit['value'], text[100:123])
        self.assertEqual(explicit['nextOffset'], 123)
        for field in ('rows', 'fields'):
            page = self.page('context_read', path=[field])
            self.assertEqual(len(page['rows']), 20)
            self.assertEqual(page['nextOffset'], 20)
            self.assertTrue(page['truncated'])
        self.assertEqual(len(context_page(self.tools.context['fields'])['rows']), 20)
        tail = self.page('context_read', path=['rows'], offset=37, limit=2)
        self.assertEqual([row['value'] for row in tail['rows']], [37, 38])
        self.assertEqual(tail['nextOffset'], 39)
        for limit in (None, 0, -1, True, 4097):
            with self.subTest(limit=limit):
                self.assertFalse(self.tools.dispatch('context_read', dict(path=['text'], limit=limit))['ok'])
        description = context_tool_schema()['function']['description']
        self.assertIn('4096 characters', description)
        self.assertIn('20 entries', description)
        self.assertFalse(self.rpc.writes)

    def test_default_context_unicode_pages_preserve_text_and_wrapped_byte_bound(self):
        text = '😀界"\\' * 3000
        self.tools.context = dict(text=text)
        chunks, offset, hashes = [], 0, set()
        while True:
            page = self.page('context_read', path=['text'], offset=offset)
            chunks.append(page['value']); hashes.add(page['viewSHA256'])
            self.assertEqual(page['nextOffset'], offset + len(page['value']))
            if not page['truncated']: break
            self.assertGreater(page['nextOffset'], offset)
            offset = page['nextOffset']
        self.assertEqual(''.join(chunks), text)
        self.assertEqual(len(hashes), 1)
        self.assertFalse(self.rpc.writes)

    def test_context_default_does_not_change_generic_observation_text_paging(self):
        original = self.rpc.call
        def call(method, params, **kwargs):
            result = original(method, params, **kwargs)
            if method == 'buddy-world.snapshot': result['text'] = 'x' * 200
            return result
        with mock.patch.object(self.rpc, 'call', side_effect=call):
            page = self.page('read_observation', method='buddy-world.snapshot', params={}, path=['text'])
        self.assertEqual(page['value'], 'x' * 20)
        self.assertEqual(page['nextOffset'], 20)
        self.assertFalse(self.rpc.writes)

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
        # Existing writer/authority unit fixtures isolate execution after admission.
        # Real pure-Lua rejection and zero-side-effect checks live in test_gm_goal_admission.
        admission=mock.patch.object(self.gm,'admit_goal',return_value=dict(accepted=True))
        admission.start();self.addCleanup(admission.stop)
        self.rpc = RPC('87')
        return DirectTools(self.rpc, self.rpc.sid, '87', self.workspace, self.host,
            allowed=lambda: self.allowed, gm_allowed=lambda: self.gm_allowed,
            cancelled=lambda: self.cancelled,
            request_id='actual-owner:seq9', runner=runner,
            helpers={'gm_tasks': self.gm, 'policy_binary': '/fixed/luapolicy',
                     'gm_bindings': lambda: [dict(guid='4', rpc_url='http://127.0.0.1:8087/rpc')]})

    def current_gm_task(self, tools):
        task_id = 'joel-to-lotharen-7223c707038a16f3'
        folder = self.host/'data/converse/lotharen/gm-tasks'/task_id/'task'
        with self.gm.task_store(folder) as store:
            store.save(dict(version=1, goal=dict(id=task_id, members=[]), phase='waiting_effect',
                identity=dict(sessionId=self.rpc.sid, guid='87', rpcUrl=self.rpc.url),
                memory=dict(waiting=dict(id='original-command', outcome='unknown')),
                reservations={'original-command': dict(outcome='written')},
                holds={'4': dict(heldRevision='original-hold', restored=False)}))
            store.write('invocation.json', json.dumps(dict(rpcUrl=self.rpc.url,
                goal=str(folder.parent/'goal.json'), transport=str(folder/'transport.json'),
                policy=str(folder/'scoped-policy.lua'), policyBinary='/fixed/luapolicy')).encode())
        config = self.host/'data/converse/lotharen/gm-transport.json'
        config.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        config.write_text('{}')
        writer = self.gm.writer_folder(self.host, self.rpc.sid, {})
        with self.gm.task_store(writer) as store:
            store.save(dict(active=True, stateDir=str(folder), pid=123))
        return task_id, folder, writer

    def test_current_nested_task_discovery_status_and_resume_use_actual_retained_helper(self):
        runner = mock.Mock(side_effect=AssertionError('Existing tasks do not create another CLI task'))
        tools = self.gm_tools(runner)
        task_id, folder, _ = self.current_gm_task(tools)
        before = self.gm.task_store(folder).load()
        discovered = tools.dispatch('gm_current_task', {})
        self.assertTrue(discovered['ok'], discovered)
        self.assertEqual(discovered['result']['taskId'], task_id)
        self.assertEqual(discovered['result']['result']['memory'], before['memory'])
        status = tools.dispatch('gm_task_status', dict(task_id=task_id))
        self.assertEqual(status['result']['result']['heldMembers'][0]['heldRevision'], 'original-hold')
        with mock.patch.object(self.gm, 'launch_task', return_value=dict(started=True, completion='not_yet_observed')) as launch:
            resumed = tools.dispatch('gm_task_resume', dict(task_id=task_id))
        self.assertTrue(resumed['ok'], resumed)
        self.assertTrue(resumed['result']['result']['started'])
        self.assertFalse(resumed['result']['gameEffectClaimed'])
        self.assertEqual(launch.call_args.args[0].folder, folder)
        self.assertEqual(launch.call_args.args[1]['policy'], str(folder/'scoped-policy.lua'))
        self.assertEqual(self.gm.task_store(folder).load(), before)
        blocked = tools.dispatch('gm_task_start', dict(goal=dict(members=[])))
        self.assertFalse(blocked['ok'], blocked)
        self.assertIn(task_id, blocked['error']['error'])
        self.assertIn('gm_current_task', blocked['error']['error'])
        runner.assert_not_called()
        self.assertFalse(self.rpc.writes)

    def test_gm_inspect_reads_only_configured_clients_with_real_helper_and_identity(self):
        tools = self.gm_tools(mock.Mock())
        self.gm_allowed = False  # Read-only evidence is not GM mutation authority.
        calls=[]
        class PublicClient:
            def __init__(self, guid): self.guid=guid
            def call(self, method, params, **kwargs):
                calls.append((self.guid,method))
                if method=='buddy-actions.status':return dict(sessionId='aw-'+self.guid,selfGuid=self.guid)
                if method=='buddy-party.state':return dict(sessionId='aw-'+self.guid,group=dict(leaderGuid='1'))
                if method=='buddy-world.snapshot':return dict(self=dict(guid=self.guid,present=True,
                    aliveKnown=True,healthKnown=True,health=1,maxHealth=3000))
                raise AssertionError('No mutation allowed')
        configured=[dict(guid='4',name='Mira',rpc_url='http://127.0.0.1:8087/rpc-companion')]
        with mock.patch.object(self.gm,'bindings',return_value=configured), \
                mock.patch.object(self.gm,'task_rpc',side_effect=lambda url:PublicClient('87' if url==self.rpc.url else '4')):
            observed=tools.dispatch('gm_inspect',{})
            self.assertTrue(observed['ok'],observed)
            rows=observed['result']['clients']
            self.assertEqual([row['guid'] for row in rows],['87','4'])
            self.assertTrue(all(row['identityMatches'] for row in rows))
            self.assertTrue(all(row['self']['alive'] is False for row in rows))
            self.assertEqual(rows[1]['partySessionId'],'aw-4')
            self.assertEqual(rows[1]['group'],dict(leaderGuid='1'))
            self.assertFalse(tools.dispatch('gm_inspect',dict(rpc_url='http://other/rpc'))['ok'])
        self.assertEqual(len(calls),6)
        self.assertFalse(self.rpc.writes)
        self.assertLessEqual(len(packed(observed)),READ_PAGE_BYTES)

    def test_large_gm_inspection_default_keeps_all_client_and_player_facts_readable(self):
        tools=self.gm_tools(mock.Mock())
        configured=[dict(guid=guid,name=name,rpc_url='http://127.0.0.1:'+port+'/rpc')
            for guid,name,port in [('1','Arlen','8086'),('4','Mira','8084'),('5','Rook','8085'),('8','Nixwick','8088')]]
        by_url={row['rpc_url']:row['guid'] for row in configured}
        by_url[self.rpc.url]='87'
        source={}
        for guid in ('87','1','4','5','8'):
            own=dict(guid=guid,present=True,mapKnown=True,mapId=349,positionKnown=True,x=int(guid),y=2,z=3,
                positionRevision='server-'+guid,observationAgeMs='11',positionAgeMs='9',
                aliveKnown=guid!='4',healthKnown=guid!='4',health=1 if guid=='8' else 500,maxHealth=1000)
            if guid!='8':own['alive']=True  # Known-dead Nix's false is omitted by ProtoJSON.
            owner=dict(guid='3',present=True,mapKnown=True,mapId=349,positionKnown=True,x=72,y=73,z=74,
                aliveKnown=guid=='87',positionRevision='joel-'+guid,observationAgeMs='17')
            nearby=[dict(guid='9000',name='Unrelated player',present=True,mapKnown=True,mapId=349),
                dict(guid='4',present=True,mapKnown=True,mapId=349,x=4,y=5,z=6,aliveKnown=False,observationAgeMs='21'),
                dict(guid='5',present=True,mapKnown=True,positionKnown=True,aliveKnown=True),
                dict(guid='42',present=True,mapKnown=True,mapId=349,x=42,y=2,z=3,aliveKnown=True,alive=True)]
            source[guid]=dict(world=dict(self=own,owner=owner,nearbyPlayers=nearby,visitRevision='visit-'+guid,
                nearbyCreatures=[dict(guid=str(10000+i),name='NPC-'+str(i)+'x'*700,present=True,
                    mapKnown=True,mapId=349) for i in range(400)]),
                actions=dict(sessionId='aw-'+guid,selfGuid=guid,position=dict(known=True,mapKnown=True,mapId=349,
                    x=int(guid)+.5,y=2,z=3,estimated=True,inferred=True,revision='client-'+guid)),
                party=dict(sessionId='aw-'+guid,group=dict(known=guid!='4',inGroup=True,groupGuid='777',
                    leaderGuid='1',members=[dict(guid=g,name='member-'+g,online=True) for g in ('1','3','4','5','42')])))
        class PublicClient:
            def __init__(self,guid):self.guid=guid
            def call(self,method,params,**kwargs):
                key={'buddy-world.snapshot':'world','buddy-actions.status':'actions','buddy-party.state':'party'}[method]
                return copy.deepcopy(source[self.guid][key])
        with mock.patch.object(self.gm,'bindings',return_value=configured), \
                mock.patch.object(self.gm,'task_rpc',side_effect=lambda url:PublicClient(by_url[url])):
            for args in ({},{'path':[]}):
                default=tools.dispatch('gm_inspect',args)
                self.assertTrue(default['ok'],default)
                self.assertLessEqual(len(packed(default)),GM_INSPECT_BYTES)
                self.assertLess(len(packed(default)),len(packed(source))//20)
                view=default['result']
                self.assertEqual(view['presentation']['kind'],'compact_identity_scoped_observations')
                self.assertNotIn('rows',view)
                rows={row['guid']:row for row in view['clients']}
                self.assertEqual(set(rows),{'87','1','4','5','8'})
                self.assertIs(rows['8']['self']['alive'],False)
                self.assertIs(rows['4']['self']['aliveKnown'],False)
                self.assertIsNone(rows['4']['self']['alive'])
                self.assertIs(rows['4']['group']['known'],False)
                for guid,row in rows.items():
                    self.assertTrue(row['identityMatches'])
                    self.assertEqual(row['partySessionId'],'aw-'+guid)
                    facts=row['observations']
                    self.assertTrue(facts['actions']['position']['estimated'])
                    self.assertTrue(facts['actions']['position']['inferred'])
                    self.assertEqual(facts['actions']['position']['revision'],'client-'+guid)
                    self.assertEqual(facts['world']['self']['positionRevision'],'server-'+guid)
                    self.assertEqual(facts['world']['owner']['guid'],'3')
                    self.assertEqual(facts['world']['owner']['x'],72)
                    self.assertIs(facts['world']['owner']['aliveKnown'],guid=='87')
                    self.assertIs(facts['world']['owner']['alive'],False if guid=='87' else None)
                    self.assertEqual([player['guid'] for player in facts['world']['nearbyPlayers']],['4','5','42'])
                    self.assertIsNone(facts['world']['nearbyPlayers'][0]['alive'])
                    dead=facts['world']['nearbyPlayers'][1]
                    self.assertIs(dead['alive'],False)
                    self.assertEqual([dead[key] for key in ('mapId','x','y','z','orientation')],[0,0,0,0,0])
                    self.assertEqual(facts['world']['omittedFields']['nearbyCreatures']['count'],400)
                    self.assertEqual(facts['world']['omittedFields']['otherNearbyPlayers']['count'],1)
                witness=rows['87']['observations']['world']['nearbyPlayers'][0]
                exact=tools.dispatch('gm_inspect',dict(path=witness['rawPath']+['guid']))
                self.assertEqual(exact['result']['value'],'4')
                original_dead=tools.dispatch('gm_inspect',dict(path=witness['rawPath'][:-1]+['2']))
                raw_keys={row['key'] for row in original_dead['result']['rows']}
                self.assertEqual(raw_keys,{'guid','present','mapKnown','positionKnown','aliveKnown'})
                npc_path=rows['87']['observations']['world']['omittedFields']['nearbyCreatures']['rawPath']
                raw=tools.dispatch('gm_inspect',dict(path=npc_path+['399','name'],limit=4096))
                self.assertEqual(raw['result']['value'],source['87']['world']['nearbyCreatures'][399]['name'])
                self.assertLessEqual(len(packed(raw)),READ_PAGE_BYTES)
                stranger=tools.dispatch('gm_inspect',dict(path=['clients','0','observations','world','nearbyPlayers','0','name']))
                self.assertEqual(stranger['result']['value'],'Unrelated player')
        self.assertFalse(self.rpc.writes)

    def test_current_task_cancel_writes_only_existing_cancel_marker_without_reset(self):
        tools = self.gm_tools(mock.Mock())
        task_id, folder, _ = self.current_gm_task(tools)
        before = self.gm.task_store(folder).load()
        with mock.patch.object(self.gm, 'launch_task', return_value=dict(started=True)) as launch:
            cancelled = tools.dispatch('gm_task_cancel', dict(task_id=task_id))
        self.assertTrue(cancelled['ok'], cancelled)
        self.assertTrue(cancelled['result']['result']['cancelRequested'])
        self.assertEqual((folder/'cancel.json').read_bytes(), b'{}\n')
        self.assertEqual(launch.call_args.args[0].folder, folder)
        self.assertEqual(self.gm.task_store(folder).load(), before)

    def test_current_task_binding_and_arbitrary_paths_fail_without_launch(self):
        tools = self.gm_tools(mock.Mock())
        task_id, folder, writer = self.current_gm_task(tools)
        before = self.gm.task_store(folder).load()
        with mock.patch.object(self.gm, 'launch_task') as launch:
            for value in (str(folder), '../'+task_id, 'unrelated-task'):
                for action in ('gm_task_status', 'gm_task_resume', 'gm_task_cancel'):
                    self.assertFalse(tools.dispatch(action, dict(task_id=value))['ok'], (action, value))
            for field, value in (('sessionId', 'aw-999'), ('guid', '4'), ('rpcUrl', 'http://127.0.0.1:9999/rpc')):
                data = copy.deepcopy(before)
                data['identity'][field] = value
                with self.gm.task_store(folder) as store: store.save(data)
                self.assertFalse(tools.dispatch('gm_current_task', {})['ok'], field)
                self.assertFalse(tools.dispatch('gm_task_resume', dict(task_id=task_id))['ok'], field)
            with self.gm.task_store(folder) as store: store.save(before)
            data = self.gm.task_store(writer).load()
            linked = self.folder/'linked-task'
            linked.symlink_to(folder, target_is_directory=True)
            data['stateDir'] = str(linked)
            with self.gm.task_store(writer) as store: store.save(data)
            self.assertFalse(tools.dispatch('gm_current_task', {})['ok'])
        launch.assert_not_called()
        self.assertEqual(self.gm.task_store(folder).load(), before)
        self.assertFalse(self.rpc.writes)

    def test_peer_can_read_current_task_but_cannot_resume_cancel_or_execute(self):
        tools = self.gm_tools(mock.Mock())
        task_id, folder, _ = self.current_gm_task(tools)
        before = self.gm.task_store(folder).load()
        self.gm_allowed = False
        names = {row['function']['name'] for row in tools.tool_schemas()}
        self.assertTrue({'gm_current_task', 'gm_task_status'} <= names)
        self.assertFalse({'gm_task_resume', 'gm_task_cancel', 'gm_execute'} & names)
        with mock.patch.object(self.gm, 'launch_task') as launch:
            self.assertTrue(tools.dispatch('gm_current_task', {})['ok'])
            for action, args in (('gm_task_resume', dict(task_id=task_id)),
                                 ('gm_task_cancel', dict(task_id=task_id)),
                                 ('gm_execute', dict(command='.gps'))):
                self.assertFalse(tools.dispatch(action, args)['ok'])
                with self.assertRaisesRegex(ValueError, 'current_owner_gm_authority_required'):
                    tools._gm(action, args)
        launch.assert_not_called()
        self.assertEqual(self.gm.task_store(folder).load(), before)
        self.assertFalse(self.rpc.writes)

    def test_native_command_obeys_actual_writer_interlock_before_any_packet(self):
        tools = self.gm_tools(mock.Mock())
        _, folder, _ = self.current_gm_task(tools)
        before = self.gm.task_store(folder).load()
        with mock.patch.object(self.gm.subprocess, 'run') as packet:
            blocked = tools.dispatch('gm_execute', dict(command='.gps', select_guid='3'))
        self.assertFalse(blocked['ok'], blocked)
        self.assertIn('already owns this body', blocked['error']['error'])
        self.assertIsNone(tools.unknown_receipt)
        packet.assert_not_called()
        self.assertEqual(self.gm.task_store(folder).load(), before)

    def test_native_command_expected_session_selected_guid_and_late_authority(self):
        tools = self.gm_tools(mock.Mock())
        _, _, writer = self.current_gm_task(tools)
        with self.gm.task_store(writer) as store: store.save(dict(active=False))
        original = self.rpc.call
        def call(method, params, **kwargs):
            if method == 'buddy-party.state': return dict(sessionId=self.rpc.sid)
            return original(method, params, **kwargs)
        self.rpc.call = call
        reply = subprocess.CompletedProcess([], 0, b'grpc-status: 0\r\n', b'')
        def send(*args,**kwargs):
            if kwargs.get('input',b'')[5:8] == b'\x08\xbd\x02':
                self.rpc.target=struct.unpack('<Q',kwargs['input'][-8:])[0]
            return reply
        with mock.patch.object(self.gm.subprocess, 'run', side_effect=send) as packet:
            result = tools.dispatch('gm_execute', dict(command='.modify hp 100', select_guid='3'))
            self.assertTrue(result['ok'], result)
            self.assertEqual(result['result']['sessionId'], self.rpc.sid)
            self.assertEqual(result['result']['selectedGuid'], '3')
            self.assertFalse(result['result']['gameEffectClaimed'])
            self.assertEqual(packet.call_count, 2)
            same = tools.dispatch('gm_execute', dict(command='.modify hp 100', select_guid='3'))
            self.assertTrue(same['result']['historicalReceipt'])
            self.assertEqual(packet.call_count, 2)
        for mode in ('body', 'owner'):
            self.gm_allowed = True
            self.rpc.sid = 'aw-123'
            def hook(method):
                if method == 'buddy-party.state':
                    if mode == 'owner': self.gm_allowed = False
                    else: self.rpc.sid = 'aw-456'
            def guarded(method, params, **kwargs):
                value = call(method, params, **kwargs)
                hook(method)
                return value
            self.rpc.call = guarded
            with mock.patch.object(self.gm.subprocess, 'run') as packet:
                rejected = tools.dispatch('gm_execute', dict(command='.gps '+mode))
            self.assertFalse(rejected['ok'], rejected)
            self.assertIsNone(tools.unknown_receipt)
            packet.assert_not_called()

    def test_native_command_unknown_retained_across_tool_recreation_no_replay(self):
        tools = self.gm_tools(mock.Mock())
        _, _, writer = self.current_gm_task(tools)
        with self.gm.task_store(writer) as store: store.save(dict(active=False))
        original = self.rpc.call
        self.rpc.call = lambda method, params, **kw: (dict(sessionId=self.rpc.sid)
            if method == 'buddy-party.state' else original(method, params, **kw))
        with mock.patch.object(self.gm.subprocess, 'run', side_effect=TimeoutError('packet reply lost')) as packet:
            with self.assertRaises(ToolOutcomeUnknown) as caught:
                tools.dispatch('gm_execute', dict(command='.gps'))
        receipt = caught.exception.receipt
        self.assertFalse(receipt['replayAllowed'])
        self.assertEqual(receipt['sessionId'], self.rpc.sid)
        self.assertEqual(packet.call_count, 1)
        replacement = DirectTools(self.rpc, self.rpc.sid, '87', self.workspace, self.host,
            allowed=lambda: True, gm_allowed=lambda: True, request_id='actual-owner:seq9',
            helpers={'gm_tasks': self.gm})
        with mock.patch.object(self.gm.subprocess, 'run') as packet:
            with self.assertRaises(ToolOutcomeUnknown): replacement.dispatch('gm_execute', dict(command='.gps'))
        packet.assert_not_called()

    def test_native_command_reservation_save_then_owner_cancel_is_known_no_packet(self):
        tools = self.gm_tools(mock.Mock())
        _, _, writer = self.current_gm_task(tools)
        with self.gm.task_store(writer) as store: store.save(dict(active=False))
        original_call = self.rpc.call
        self.rpc.call = lambda method, params, **kw: (dict(sessionId=self.rpc.sid)
            if method == 'buddy-party.state' else original_call(method, params, **kw))
        original_save = self.gm.runtime.Store.save
        def save(store, value):
            original_save(store, value)
            if value.get('reserved'): self.cancelled = True
        with mock.patch.object(self.gm.runtime.Store, 'save', save), \
                mock.patch.object(self.gm.subprocess, 'run') as packet:
            refused = tools.dispatch('gm_execute', dict(command='.gps'))
        self.assertFalse(refused['ok'], refused)
        self.assertIn('cancelled', refused['error']['error'])
        self.assertIsNone(tools.unknown_receipt)
        packet.assert_not_called()
        record = next((self.host/'data/converse/lotharen/gm-commands').glob('*/state.json'))
        self.assertTrue(json.loads(record.read_text())['knownNotDispatched'])

    def test_native_command_written_then_result_save_failure_retains_unknown(self):
        tools = self.gm_tools(mock.Mock())
        _, _, writer = self.current_gm_task(tools)
        with self.gm.task_store(writer) as store: store.save(dict(active=False))
        original_call = self.rpc.call
        self.rpc.call = lambda method, params, **kw: (dict(sessionId=self.rpc.sid)
            if method == 'buddy-party.state' else original_call(method, params, **kw))
        original_save = self.gm.runtime.Store.save
        def save(store, value):
            if 'result' in value: raise OSError('disk write unavailable after packet')
            return original_save(store, value)
        reply = subprocess.CompletedProcess([], 0, b'grpc-status: 0\r\n', b'')
        with mock.patch.object(self.gm.runtime.Store, 'save', save), \
                mock.patch.object(self.gm.subprocess, 'run', return_value=reply) as packet:
            with self.assertRaises(ToolOutcomeUnknown) as caught:
                tools.dispatch('gm_execute', dict(command='.gps'))
        self.assertEqual(packet.call_count, 1)
        self.assertFalse(caught.exception.receipt['replayAllowed'])
        self.assertEqual(caught.exception.receipt['characterGuid'], '87')
        with mock.patch.object(self.gm.subprocess, 'run') as packet:
            self.assertFalse(tools.dispatch('gm_execute', dict(command='.gps'))['ok'])
        packet.assert_not_called()

    def test_resume_authority_lost_while_reading_invocation_never_launches(self):
        tools = self.gm_tools(mock.Mock())
        task_id, folder, _ = self.current_gm_task(tools)
        before = self.gm.task_store(folder).load()
        original = self.gm.load_invocation
        def load(store):
            result = original(store)
            self.gm_allowed = False
            return result
        with mock.patch.object(self.gm, 'load_invocation', load), \
                mock.patch.object(self.gm, 'launch_task') as launch:
            refused = tools.dispatch('gm_task_resume', dict(task_id=task_id))
        self.assertFalse(refused['ok'], refused)
        self.assertIsNone(tools.unknown_receipt)
        launch.assert_not_called()
        self.assertEqual(self.gm.task_store(folder).load(), before)

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


class PartyInviteToolTests(unittest.TestCase):
    """Ordinary invitation transport through the advertised native contract; no game client."""
    class PartyRPC(RPC):
        def __init__(self):
            super().__init__('4')
            self.effect='unspecified';self.installed=True;self.contract_edit=None
            self.party_sid=self.sid
            self.party=dict(sessionId=self.sid,revision='7',group=dict(known=True,inGroup=False))
            self.invite_reply=dict(transportWritten=True)

        def catalog(self):
            catalog=super().catalog()
            prefix='buddy-party::warcraftbuddy.party.v1.'
            source=(ROOT/'modules/buddy-party/party.proto').read_text()
            # Same maintained-proto convention as the existing legacy Chat fixture.
            for name in ('InviteRequest','ActionResponse','StateResponse','PendingInvite','Group','Member',
                         'CommandResult','RaidTargetState','RaidTarget'):
                body=re.search(r'message '+name+r' \{(.*?)\n\}',source,re.S).group(1)
                fields=[]
                for repeated,kind,field,number in re.findall(r'(repeated\s+)?(\w+)\s+(\w+)\s*=\s*(\d+)\s*;',body):
                    scalar=kind in ('string','bool','uint64','uint32')
                    fields.append(dict(name=re.sub(r'_([a-z])',lambda m:m[1].upper(),field),number=int(number),
                        kind=kind if scalar else 'enum' if kind=='InvitationDecision' else 'message',
                        type=kind if scalar else prefix+kind,repeated=bool(repeated),map=False,optional=False,required=False))
                catalog['messages'][prefix+name]=dict(protobufName='warcraftbuddy.party.v1.'+name,syntax='proto3',fields=fields)
            catalog['messages']['google.protobuf.Empty']=dict(protobufName='google.protobuf.Empty',syntax='proto3',fields=[])
            catalog['methods'].append(dict(name='buddy-party.state',module='buddy-party',
                path='/warcraftbuddy.party.v1.BuddyParty/State',effect='read',
                inputType='google.protobuf.Empty',outputType=prefix+'StateResponse'))
            if self.installed:
                catalog['methods'].append(dict(name='buddy-party.invite',module='buddy-party',
                    path='/warcraftbuddy.party.v1.BuddyParty/Invite',effect=self.effect,
                    inputType=prefix+'InviteRequest',outputType=prefix+'ActionResponse'))
            if self.contract_edit:self.contract_edit(catalog)
            return catalog

        def call(self,method,params,**kwargs):
            if method=='session.describe':return super().call(method,params,**kwargs)
            self.calls.append((method,copy.deepcopy(params)))
            if method=='buddy-party.state':
                result=dict(copy.deepcopy(self.party),sessionId=self.party_sid)
            elif method=='buddy-party.invite':
                self.writes.append((method,copy.deepcopy(params)))
                if self.fail_mutation:raise TimeoutError('entered invite write; response lost')
                result=copy.deepcopy(self.invite_reply)
            else:raise AssertionError('Invitation must not query position, use GM or dispatch other actions: '+method)
            if self.hook:self.hook(method)
            return result

    def setUp(self):
        folder=tempfile.TemporaryDirectory();self.addCleanup(folder.cleanup)
        self.root=Path(folder.name).resolve();self.rpc=self.PartyRPC()
        self.runtime=load_helper(ROOT/'dev/task-runtime','party_test_runtime')
        self.owner=True;self.cancelled=False
        self.tools=self.make_tools()

    def make_tools(self, **kwargs):
        return DirectTools(self.rpc,self.rpc.sid,self.rpc.guid,None,self.root,
            allowed=lambda:False,gm_allowed=lambda:False,owner_allowed=lambda:self.owner,
            cancelled=lambda:self.cancelled,request_id=kwargs.pop('request_id','owner-invite-request'),
            helpers={'task_runtime':self.runtime},**kwargs)

    def invite(self,name='Joel'):
        return self.tools.dispatch('party_invite',dict(name=name))

    def test_owner_without_workspace_or_gm_gets_exact_advertised_invite_tool(self):
        schemas={row['function']['name']:row['function'] for row in self.tools.tool_schemas()}
        invite=schemas['party_invite']['parameters']
        self.assertEqual(invite['required'],['name'])
        self.assertEqual(set(invite['properties']),{'name'})
        self.assertFalse({'workspace_read','workspace_write','policy_show','policy_input','policy_apply',
                          'gm_execute','gm_task_start'} & schemas.keys())
        self.owner=False
        self.assertNotIn('party_invite',{row['function']['name'] for row in self.tools.tool_schemas()})
        self.assertEqual(self.rpc.writes,[])

    def test_legacy_typed_and_declared_mutation_routes_send_exact_name_only(self):
        for effect,name in (('unspecified','Joel'),('mutation','Élia'),('mutation','李明')):
            self.rpc=self.PartyRPC();self.rpc.effect=effect;self.tools=self.make_tools()
            with self.subTest(effect=effect,name=name):
                reply=self.invite(name)
                self.assertTrue(reply['ok'],reply)
                self.assertEqual(self.rpc.writes,[('buddy-party.invite',dict(name=name))])
                self.assertEqual(reply['result']['name'],name)
                self.assertIs(reply['result']['transportWritten'],True)
                self.assertEqual(reply['result']['completion'],'transport_written; server_result_and_membership_unverified')
                self.assertFalse(reply['result']['gameEffectClaimed'])
                self.assertTrue(any(m=='buddy-party.state' for m,_ in self.rpc.calls))
                self.assertTrue(all(m in ('session.describe','buddy-party.state','buddy-party.invite') for m,_ in self.rpc.calls))

    def test_invalid_names_and_arbitrary_fields_never_reach_native_invite(self):
        for name in ('','J','J'*13,'Joel-Realm','Joel Realm','Joel\n','.groupsummon','Jo\x00el','🙂🙂',None,1,True):
            with self.subTest(name=name):self.assertFalse(self.invite(name)['ok'])
        for fields in (dict(name='Joel',sessionId=self.rpc.sid),dict(name='Joel',command='.gear repair Joel')):
            self.assertFalse(self.tools.dispatch('party_invite',fields)['ok'])
        self.assertEqual(self.rpc.writes,[])
        self.assertFalse(any(m=='buddy-party.invite' for m,_ in self.rpc.calls))

    def test_missing_denied_and_spoofed_contracts_are_known_zero_writes(self):
        prefix='buddy-party::warcraftbuddy.party.v1.'
        def invite(c):return next(row for row in c['methods'] if row['name']=='buddy-party.invite')
        changes=[lambda r:setattr(r,'installed',False),lambda r:setattr(r,'effect','read'),
                 lambda r:setattr(r,'effect','query'),
                 lambda r:setattr(r,'contract_edit',lambda c:invite(c).update(module='buddy-chat')),
                 lambda r:setattr(r,'contract_edit',lambda c:invite(c).update(path='/foreign.Service/Invite')),
                 lambda r:setattr(r,'contract_edit',lambda c:c['messages'][prefix+'InviteRequest']['fields'][0].update(number=2)),
                 lambda r:setattr(r,'contract_edit',lambda c:c['messages'][prefix+'ActionResponse']['fields'][0].update(kind='string',type='string'))]
        for index,change in enumerate(changes):
            self.rpc=self.PartyRPC();change(self.rpc);self.tools=self.make_tools()
            with self.subTest(contract=index):self.assertFalse(self.invite()['ok'])
            self.assertEqual(self.rpc.writes,[])
            self.assertFalse(any(m=='buddy-party.invite' for m,_ in self.rpc.calls))

    def test_owner_cancel_body_and_party_replacement_before_entry_make_zero_writes(self):
        for kind in ('owner','cancel','core','guid','party','late_owner','late_cancel','late_core','late_guid','late_route'):
            self.rpc=self.PartyRPC();self.owner=True;self.cancelled=False;self.tools=self.make_tools()
            if kind=='owner':self.owner=False
            elif kind=='cancel':self.cancelled=True
            elif kind=='core':self.rpc.sid='aw-replaced'
            elif kind=='guid':self.rpc.guid='5'
            elif kind=='party':self.rpc.party_sid='aw-foreign'
            else:
                def replaced(method):
                    if method!='buddy-party.state':return
                    if kind=='late_owner':self.owner=False
                    elif kind=='late_cancel':self.cancelled=True
                    elif kind=='late_core':self.rpc.sid='aw-replaced'
                    elif kind=='late_guid':self.rpc.guid='5'
                    elif kind=='late_route':self.rpc.installed=False
                self.rpc.hook=replaced
            with self.subTest(change=kind):self.assertFalse(self.invite()['ok'])
            self.assertEqual(self.rpc.writes,[])
            self.assertFalse(any(m=='buddy-party.invite' for m,_ in self.rpc.calls))

    def test_lost_response_and_post_entry_binding_loss_keep_unknown_without_retry(self):
        for kind in ('lost','malformed','core','party'):
            self.rpc=self.PartyRPC();self.owner=True;self.tools=self.make_tools(receipt_root=self.root/kind)
            if kind=='lost':self.rpc.fail_mutation=True
            elif kind=='malformed':self.rpc.invite_reply=dict(transportWritten='yes')
            else:
                def changed(method):
                    if method!='buddy-party.invite':return
                    if kind=='core':self.rpc.sid='aw-replaced'
                    elif kind=='party':self.rpc.party_sid='aw-foreign'
                self.rpc.hook=changed
            with self.subTest(change=kind),self.assertRaises(ToolOutcomeUnknown) as caught:self.invite()
            receipt=caught.exception.receipt
            self.assertEqual(receipt['name'],'Joel')
            self.assertEqual(receipt['sessionId'],'aw-123');self.assertEqual(receipt['characterGuid'],'4')
            self.assertFalse(receipt['replayAllowed'])
            self.rpc.hook=None;self.rpc.sid='aw-123';self.rpc.party_sid='aw-123';self.owner=True
            self.rpc.fail_mutation=False;self.rpc.invite_reply=dict(transportWritten=True)
            again=self.invite()
            self.assertFalse(again['ok'],again)
            self.assertEqual(self.rpc.writes,[('buddy-party.invite',dict(name='Joel'))])

    def test_unknown_target_custody_survives_fresh_request_and_case_alias(self):
        receipt_root=self.root/'shared-receipts'
        self.tools=self.make_tools(receipt_root=receipt_root)
        self.rpc.fail_mutation=True
        with self.assertRaises(ToolOutcomeUnknown) as first:self.invite('Joel')
        self.assertFalse(first.exception.receipt['replayAllowed'])
        self.assertEqual(self.rpc.writes,[('buddy-party.invite',dict(name='Joel'))])
        self.rpc.fail_mutation=False
        # A different API conversation/provider creates a fresh dispatcher and host request ID.
        # The same body and case-folded target still retain the first entered write.
        fresh=self.make_tools(receipt_root=receipt_root,request_id='another-provider-owner-request')
        with self.assertRaises(ToolOutcomeUnknown) as retained:
            fresh.dispatch('party_invite',dict(name='joel'))
        self.assertFalse(retained.exception.receipt['replayAllowed'])
        self.assertEqual(retained.exception.receipt['stateDir'],first.exception.receipt['stateDir'])
        self.assertEqual(retained.exception.receipt['sessionId'],'aw-123')
        self.assertEqual(retained.exception.receipt['characterGuid'],'4')
        self.assertEqual(self.rpc.writes,[('buddy-party.invite',dict(name='Joel'))])

    def test_owner_interrupt_after_ack_settles_durable_transport_without_reinvite(self):
        receipt_root=self.root/'interrupted-receipts'
        self.tools=self.make_tools(receipt_root=receipt_root)
        def interrupt(method):
            if method=='buddy-party.invite':
                self.cancelled=True;self.owner=False
        self.rpc.hook=interrupt
        reply=self.invite()
        self.assertTrue(reply['ok'],reply)
        self.assertTrue(reply['result']['transportWritten'])
        self.assertFalse(reply['result']['gameEffectClaimed'])
        self.assertIsNone(self.tools.unknown_receipt)
        receipts=list((receipt_root/'party-invites').glob('*/*/state.json'))
        self.assertEqual(len(receipts),1)
        stored=json.loads(receipts[0].read_text())
        self.assertEqual(stored['result'],reply['result'])
        self.assertNotIn('knownNotDispatched',stored)
        self.rpc.hook=None;self.cancelled=False;self.owner=True
        fresh=self.make_tools(receipt_root=receipt_root)
        historical=fresh.dispatch('party_invite',dict(name='joel'))
        self.assertTrue(historical['ok'],historical)
        self.assertTrue(historical['result']['historicalReceipt'])
        self.assertTrue(historical['result']['transportWritten'])
        self.assertEqual(self.rpc.writes,[('buddy-party.invite',dict(name='Joel'))])

    def test_packet_ack_is_not_server_acceptance_and_membership_requires_its_own_observation(self):
        reply=self.invite()
        self.assertTrue(reply['ok'],reply);self.assertTrue(reply['result']['transportWritten'])
        self.assertFalse(reply['result']['gameEffectClaimed'])
        self.assertFalse(self.rpc.party['group']['inGroup'])
        self.assertIn('server_result_and_membership_unverified',reply['result']['completion'])
        self.rpc.party['group']=dict(known=True,inGroup=True,groupGuid='222',members=[
            dict(guid='4',name='Mira',self=True,online=True),dict(guid='3',name='Joel',online=True)])
        observed=self.tools.dispatch('read_observation',dict(method='buddy-party.state',params={}))
        self.assertTrue(observed['ok'],observed)
        self.assertTrue(observed['result']['group']['inGroup'])
        self.assertEqual(observed['result']['group']['members'][1]['guid'],'3')
        self.assertFalse(reply['result']['gameEffectClaimed']) # Later group data never rewrites the transport receipt.
        self.assertEqual(len(self.rpc.writes),1)

    def test_real_transport_retains_unknown_invite_across_client_and_tool_recreation(self):
        from direct_model import DirectModel, ModelError
        from test_direct_model import FakeHTTP, chat
        final=dict(choices=[dict(finish_reason='stop',message=dict(role='assistant',content='The invitation outcome is still unknown.'))])
        self.rpc.fail_mutation=True
        with FakeHTTP([chat(('invite-entered','party_invite',dict(name='Joel'))),final,
                       chat(('new-call-id','party_invite',dict(name='Joel')))]) as server:
            path=self.root/'api-conversation.json';key='aw-123:4:chat:WHISPER:3'
            def run(client,tools,request):
                return client.generate('Synthetic owner invitation; never invent acceptance.',
                    [dict(role='user',content='Invite Joel.')],None,tools.tool_schemas(),tools.dispatch,
                    natural=True,transcript_path=path,conversation_key=key,request_id=request)
            with self.assertRaisesRegex(ModelError,'tool_outcome_unknown'):
                run(DirectModel('local','fixture',server.base+'/v1'),self.tools,'first-owner')
            self.assertEqual(len(self.rpc.writes),1)
            self.rpc.fail_mutation=False;fresh=self.make_tools()
            client=DirectModel('local','fixture',server.base+'/v1')
            run(client,fresh,'second-owner')
            tool=next(row for row in server.requests[1]['body']['messages'] if row['role']=='tool')
            retained=json.loads(tool['content'])
            self.assertTrue(retained['outcomeUnknown']);self.assertFalse(retained['replayAllowed'])
            self.assertEqual(retained['receipt']['name'],'Joel')
            self.assertEqual(retained['receipt']['sessionId'],'aw-123')
            with self.assertRaisesRegex(ModelError,'tool_outcome_unknown'):run(client,fresh,'third-owner')
            self.assertEqual(self.rpc.writes,[('buddy-party.invite',dict(name='Joel'))])


class OwnerControlToolTests(unittest.TestCase):
    class ControlRPC(RPC):
        def __init__(self):
            super().__init__('4')
            self.state=dict(sessionId=self.sid,selfGuid=self.guid,following=False,motionUnconfirmed=False)
            self.fail_method=None
            self.catalog_edit=None
            self.source_reads=0

        def catalog(self):
            catalog=super().catalog()
            prefix='buddy-actions::warcraftbuddy.actions.v1.'
            status=next(row for row in catalog['methods'] if row['name']=='buddy-actions.status')
            status.update(module='buddy-actions',path='/warcraftbuddy.actions.v1.BuddyActions/Status',outputType=prefix+'State')
            for alias,route in (('follow','Follow'),('buffs','Buffs'),('cast_buff','CastBuff')):
                catalog['methods'].append(dict(name='buddy-actions.'+alias,module='buddy-actions',
                    path='/warcraftbuddy.actions.v1.BuddyActions/'+route,effect='unspecified',
                    inputType=prefix+'ToggleRequest',outputType=prefix+'State'))
            catalog['messages'][prefix+'ToggleRequest']=dict(fields=[dict(name='enabled',number=1,kind='bool',type='bool')])
            catalog['messages'][prefix+'State']=dict(protobufName='warcraftbuddy.actions.v1.State',syntax='proto3',
                fields=[dict(name=name,number=number,kind=kind,type=kind) for name,number,kind in
                        (('following',1,'bool'),('sessionId',106,'string'),('selfGuid',107,'uint64'))])
            if self.catalog_edit:self.catalog_edit(catalog)
            return catalog

        def call(self,method,params,**kwargs):
            if method not in ('buddy-actions.status','buddy-actions.follow','buddy-actions.buffs','session.send_event'):
                return super().call(method,params,**kwargs)
            self.calls.append((method,copy.deepcopy(params)))
            if method=='buddy-actions.status':result=copy.deepcopy(self.state)
            else:
                self.writes.append((method,copy.deepcopy(params)))
                if method==self.fail_method:raise TimeoutError('entered write, response lost')
                if method=='session.send_event':
                    result=dict(sessionId=self.sid,characterGuid=self.guid,status='queued',sequence='29')
                else:
                    if method=='buddy-actions.follow':self.state['following']=params['enabled']
                    result=copy.deepcopy(self.state)
                    if method=='buddy-actions.buffs':result['reason']='lua_buff_request_queued:29'
            if self.hook:self.hook(method)
            return result

    def setUp(self):
        folder=tempfile.TemporaryDirectory();self.addCleanup(folder.cleanup)
        self.root=Path(folder.name).resolve();self.rpc=self.ControlRPC()
        self.owner=True;self.cancelled=False;self.preparations=[];self.prepare_reason=None
        self.runtime=load_helper(ROOT/'dev/task-runtime','owner_control_test_runtime')
        self.tools=self.make_tools()

    def prepare(self,actions,current):
        self.preparations.append(copy.deepcopy(actions));current()
        return self.prepare_reason

    def make_tools(self,request='owner-request',**options):
        return DirectTools(self.rpc,'aw-123','4',None,self.root,
            allowed=lambda:False,owner_allowed=lambda:self.owner,cancelled=lambda:self.cancelled,
            request_id=request,helpers={'task_runtime':self.runtime,'self-edit':load_helper(ROOT/'dev/self-edit','owner_control_self_edit')},
            follow_prepare=self.prepare,**options)

    def test_owner_toolset_has_typed_controls_and_no_developer_gm_or_assist_escape(self):
        specs={row['function']['name']:row['function'] for row in self.tools.tool_schemas()}
        self.assertTrue({'follow','gameplay_command','request_buffs','spell_inspect'} <= specs.keys())
        self.assertFalse({'policy_input','policy_apply','workspace_write','gm_execute','assist'} & specs.keys())
        self.assertEqual(specs['follow']['parameters']['properties'],dict(enabled=dict(type='boolean')))
        self.assertEqual(specs['request_buffs']['parameters']['properties'],{})
        guidance=specs['gameplay_command']['description']
        for task in ('engage','control','focus','kill_order','wait_for_pull','cancel_task','cast_spell','cancel_spell'):
            self.assertIn(task,guidance)
        self.owner=False
        self.assertFalse({'follow','gameplay_command','request_buffs','spell_inspect'} &
            {row['function']['name'] for row in self.tools.tool_schemas()})
        self.assertEqual(self.rpc.writes,[])

    def test_follow_preparation_and_returned_state_never_claim_arrival(self):
        for enabled in (True,False):
            result=self.tools.dispatch('follow',dict(enabled=enabled),operation_id=str(enabled))
            self.assertTrue(result['ok'],result)
            self.assertEqual(result['result']['state']['following'],enabled)
            self.assertEqual(result['result']['requestedEnabled'],enabled)
            self.assertFalse(result['result']['gameEffectClaimed'])
            self.assertIn('physical_movement_unverified',result['result']['completion'])
        self.assertEqual(len(self.preparations),2)
        self.assertEqual(self.rpc.writes,[('buddy-actions.follow',dict(enabled=True)),('buddy-actions.follow',dict(enabled=False))])

    def test_unsettled_task_preparation_blocks_follow_without_replaying_or_clearing_it(self):
        self.prepare_reason='Mission has an uncertain result preserved; following is unchanged.'
        result=self.tools.dispatch('follow',dict(enabled=True))
        self.assertTrue(result['ok'],result);self.assertFalse(result['result']['followDispatched'])
        self.assertIn('uncertain',result['result']['reason']);self.assertEqual(self.rpc.writes,[])

    def test_fresh_follow_intent_a_b_a_does_not_return_first_stale_receipt(self):
        for operation,enabled in (('one',True),('two',False),('three',True)):
            result=self.tools.dispatch('follow',dict(enabled=enabled),operation_id=operation)
            self.assertTrue(result['ok'],result)
            self.assertIs(result['result']['state']['following'],enabled)
            self.assertNotIn('historicalReceipt',result['result'])
        self.assertEqual([args['enabled'] for _,args in self.rpc.writes],[True,False,True])
        duplicate=self.make_tools().dispatch('follow',dict(enabled=True),operation_id='four')
        self.assertTrue(duplicate['result']['historicalReceipt']);self.assertEqual(len(self.rpc.writes),3)
        # Current native intent can expire/change independently of this host's last receipt.
        self.rpc.state['following']=False
        renewed=self.make_tools().dispatch('follow',dict(enabled=True),operation_id='five')
        self.assertTrue(renewed['ok'],renewed);self.assertIs(renewed['result']['state']['following'],True)
        self.assertEqual(len(self.rpc.writes),4)

    def test_fresh_lua_focus_a_b_a_delivers_current_intent_not_old_ack(self):
        for operation,target in (('one','123'),('two','456'),('three','123')):
            result=self.tools.dispatch('gameplay_command',dict(value=dict(task='focus',targetGuid=target)),operation_id=operation)
            self.assertTrue(result['ok'],result);self.assertNotIn('historicalReceipt',result['result'])
        self.assertEqual([args['value']['targetGuid'] for _,args in self.rpc.writes],['123','456','123'])

    def test_proto3_omitted_false_is_known_stop_and_durable_without_resend(self):
        original=self.rpc.call
        def omit(method,params,**kwargs):
            result=original(method,params,**kwargs)
            if method=='buddy-actions.follow':result.pop('following',None)
            return result
        self.rpc.call=omit
        result=self.tools.dispatch('follow',dict(enabled=False),operation_id='stop')
        self.assertTrue(result['ok'],result);self.assertIs(result['result']['state']['following'],False)
        self.assertFalse(result['result']['gameEffectClaimed'])
        again=self.make_tools().dispatch('follow',dict(enabled=False),operation_id='another-stop')
        self.assertTrue(again['ok'],again);self.assertTrue(again['result']['historicalReceipt'])
        self.assertEqual(self.rpc.writes,[('buddy-actions.follow',dict(enabled=False))])
        # An explicit invalid value is not the protobuf default.
        other=self.make_tools(request='new-owner')
        def invalid(method,params,**kwargs):
            result=original(method,params,**kwargs)
            if method=='buddy-actions.follow':result['following']='false'
            return result
        self.rpc.call=invalid
        with self.assertRaises(ToolOutcomeUnknown):other.dispatch('follow',dict(enabled=False),operation_id='bad-result')
        self.assertEqual(len(self.rpc.writes),2)

    def test_lua_command_and_buff_use_fixed_installed_routes_with_delivery_only(self):
        value=dict(task='wait_for_pull',id='wait-1',ownerGuid='3',responseYards=10)
        result=self.tools.dispatch('gameplay_command',dict(value=value))
        self.assertTrue(result['ok'],result);self.assertTrue(result['result']['deliveryOnly'])
        self.assertFalse(result['result']['gameEffectClaimed'])
        self.assertEqual(result['result']['observe']['arguments'],dict(method='session.policy_state',params={}))
        self.assertEqual(self.rpc.writes,[('session.send_event',dict(sessionId='aw-123',characterGuid='4',kind='command',value=value))])
        buffs=self.tools.dispatch('request_buffs',{})
        self.assertTrue(buffs['ok'],buffs);self.assertTrue(buffs['result']['deliveryOnly'])
        self.assertEqual(self.rpc.writes[-1],('buddy-actions.buffs',dict(enabled=True)))
        self.assertFalse(buffs['result']['gameEffectClaimed'])
        before=copy.deepcopy(self.rpc.writes)
        self.assertFalse(self.tools.dispatch('gameplay_command',dict(value=value,kind='receipt'))['ok'])
        self.assertFalse(self.tools.dispatch('request_buffs',dict(enabled=False))['ok'])
        self.assertFalse(self.tools.dispatch('follow',dict(enabled='yes'))['ok'])
        self.assertEqual(self.rpc.writes,before)

    def test_ranged_directive_is_queued_scope_not_applied_or_ability_proof(self):
        value=dict(task='ranged_only',id='owner-tactic-1',creatureEntry=123,mapId=0,visitId='7',
            minRangeYards=12,maxRangeYards=25,clearanceYards=10,durationMs=60000,enabled=True)
        result=self.tools.dispatch('gameplay_command',dict(value=value),operation_id='tactic-1')
        self.assertTrue(result['ok'],result)
        row=result['result']
        self.assertTrue(row['deliveryOnly']);self.assertFalse(row['gameEffectClaimed'])
        self.assertEqual(row['requestedDirective'],value)
        self.assertEqual(row['application'],dict(known=False,reason='queued_delivery_only',
            observePath='latestDecision.rangedStance',expectedTaskId='owner-tactic-1'))
        self.assertEqual(self.rpc.writes,[('session.send_event',dict(sessionId='aw-123',characterGuid='4',kind='command',value=value))])
        schema=next(r['function'] for r in self.tools.tool_schemas() if r['function']['name']=='gameplay_command')
        props=schema['parameters']['properties']['value']['properties']
        self.assertTrue(set(value)<=set(props))
        for phrase in ('ranged_only','latestDecision.rangedStance','queued input is delivery only','equipped wand'):
            self.assertIn(phrase,schema['description'])

    def test_late_owner_body_source_and_contract_changes_are_zero_native_writes(self):
        for kind in ('owner','cancel','body','source','route'):
            with self.subTest(change=kind):
                self.rpc=self.ControlRPC();self.owner=True;self.cancelled=False
                tools=self.make_tools(request=kind)
                seen=[0]
                def changed(method):
                    if method!='session.policy_source':return
                    seen[0]+=1
                    if seen[0]!=2:return
                    if kind=='owner':self.owner=False
                    elif kind=='cancel':self.cancelled=True
                    elif kind=='body':self.rpc.sid='aw-replaced'
                    elif kind=='source':self.rpc.source+='-- edit'
                    else:self.rpc.revision='changed-catalog'
                self.rpc.hook=changed
                result=tools.dispatch('gameplay_command',dict(value=dict(task='engage',targetGuid='123')))
                self.assertFalse(result['ok'],result);self.assertEqual(self.rpc.writes,[])

    def test_known_returns_survive_owner_cancel_and_reconstruct_without_second_write(self):
        for tool,args,method in (('follow',dict(enabled=True),'buddy-actions.follow'),
                ('gameplay_command',dict(value=dict(task='cast_spell',id='cast-1',spellId=1449)),'session.send_event'),
                ('request_buffs',{},'buddy-actions.buffs')):
            with self.subTest(tool=tool):
                self.rpc=self.ControlRPC();self.cancelled=False
                tools=self.make_tools(request=tool)
                self.rpc.hook=lambda called:setattr(self,'cancelled',True) if called==method else None
                result=tools.dispatch(tool,args,operation_id='first')
                self.assertTrue(result['ok'],result)
                self.rpc.hook=None;self.cancelled=False
                retained=self.make_tools(request=tool).dispatch(tool,args,operation_id='fresh-call')
                self.assertTrue(retained['ok'],retained);self.assertTrue(retained['result']['historicalReceipt'])
                self.assertEqual(len(self.rpc.writes),1)

    def test_unknown_command_cannot_change_task_id_or_endpoint_to_reenter_same_request(self):
        known=dict(task='focus',targetGuid='123')
        self.assertTrue(self.tools.dispatch('gameplay_command',dict(value=known),operation_id='known')['ok'])
        self.rpc.fail_method='session.send_event'
        original=dict(task='cast_spell',id='cast-1',spellId=1449)
        with self.assertRaises(ToolOutcomeUnknown) as first:
            self.tools.dispatch('gameplay_command',dict(value=original),operation_id='one')
        folder=Path(first.exception.receipt['stateDir']);before=(folder/'state.json').read_bytes()
        stored=json.loads(before)
        self.assertEqual(stored['arguments'],dict(value=original))
        self.assertEqual(stored['requestId'],'owner-request')
        self.rpc.fail_method=None
        # A fresh dispatcher represents another API model/endpoint, but the logical owner intent is unchanged.
        with self.assertRaises(ToolOutcomeUnknown):
            self.make_tools().dispatch('gameplay_command',dict(value=dict(original,id='made-up-new-id')),operation_id='two')
        with self.assertRaises(ToolOutcomeUnknown):
            self.make_tools(request='different-owner').dispatch('gameplay_command',dict(value=original),operation_id='three')
        # An older matching ACK cannot bypass the later unknown operation.
        with self.assertRaises(ToolOutcomeUnknown):
            self.make_tools().dispatch('gameplay_command',dict(value=known),operation_id='old-known')
        self.assertEqual((folder/'state.json').read_bytes(),before);self.assertEqual(len(self.rpc.writes),2)

    def test_missing_or_legacy_only_buff_route_and_follow_spoof_are_not_dispatched(self):
        for name in ('follow','request_buffs'):
            self.rpc=self.ControlRPC();tools=self.make_tools(request=name)
            def edit(catalog):
                if name=='request_buffs':catalog['methods']=[row for row in catalog['methods'] if row['name']!='buddy-actions.cast_buff']
                else:next(row for row in catalog['methods'] if row['name']=='buddy-actions.follow')['path']='/foreign.Service/Follow'
            self.rpc.catalog_edit=edit
            self.assertFalse(tools.dispatch(name,dict(enabled=True) if name=='follow' else {})['ok'])
            self.assertEqual(self.rpc.writes,[])


if __name__ == '__main__':
    unittest.main()
