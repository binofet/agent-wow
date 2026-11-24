"""Raw GM facts/tool wiring only; scripted API text is not model-planning proof."""
import contextlib
import copy
import json
from pathlib import Path
import types
import unittest
from unittest import mock

import direct_model
import direct_tools
import test_direct_brain as brain_fixtures
import test_direct_model as model_fixtures
import test_gm_command_evidence as command_fixtures


ROOT = Path(__file__).resolve().parent.parent


class RawGMContextTests(unittest.TestCase):
    setUp = brain_fixtures.DirectBrainIntegrationTests.setUp
    gm_guide_fixture = brain_fixtures.DirectBrainIntegrationTests.gm_guide_fixture
    gm_primer_fixture = brain_fixtures.DirectBrainIntegrationTests.gm_primer_fixture

    def test_first_real_api_payload_distinguishes_gm_destination_from_observed_owner(self):
        # Synthetic native observations exercise the real inspector and API
        # payload. They do not establish any actual instance or model choice.
        with self.gm_primer_fixture() as (brain, state, clients):
            for guid in ('87', '1'):
                peer = clients[guid]
                original = peer.call

                def read(method, params, timeout=None, *, guid=guid, original=original):
                    value = copy.deepcopy(original(method, params, timeout))
                    if guid == '87':
                        if method == 'buddy-world.snapshot':
                            value['self'].update(mapId=0, x=-900, positionAgeMs='700')
                            value['owner'] = dict(guid='3', present=False, positionKnown=False, aliveKnown=False)
                        elif method == 'buddy-actions.status':
                            value['position'].update(mapId=0, x=-899, ageMs='430', estimated=True)
                    elif method == 'buddy-world.snapshot':
                        value['owner'] = dict(guid='3', name='Joel', present=True, mapKnown=True, mapId=349,
                            positionKnown=True, x=785, y=-143, z=-56, positionAgeMs='1400', positionRevision='22',
                            aliveKnown=True, alive=True, healthKnown=True, health=100, maxHealth=100,
                            vitalsAgeMs='2100', unitFlagsKnown=True, observationAgeMs='2900')
                    return value

                peer.call = read
            brain.gm_reference = (ROOT / 'dev/lotharen-gm-commands.txt').read_text()
            state['development']['gmTasks'] = brain_fixtures.converse.gm_tasks.tools(
                self.workspace, '87', clients['87'].url)
            goal = 'Warp everyone currently inside the instance to my location.'
            state['current_request']['messages'] = [dict(text=goal)]
            original_state = copy.deepcopy(state)
            with brain_fixtures.FakeHTTP([brain_fixtures.spoken('I have the current observations and native tools.')]) as http:
                brain.client = direct_model.DirectModel('local', brain.model, http.base)
                result = brain.generate([dict(text='Historically the GM was inside the dungeon.')], state)
            self.assertEqual(result['action'], 'NONE')
            self.assertEqual(len(http.requests), 1)
            payload = http.requests[0]['body']
            self.assertEqual(payload['tool_choice'], 'auto')
            self.assertEqual([row for row in payload['messages'] if row['role'] == 'user'],
                             [dict(role='user', content=goal)])
            system = payload['messages'][0]['content']
            self.assertIn('choose and invoke native GM commands yourself', system)
            self.assertIn('These GM actions do not require writing Lua', system)
            self.assertIn("The group moves TO the issuing GM's CURRENT location", system)
            prepared = json.loads(system.split(
                'Current observations and control state JSON:\n', 1)[1])
            instructions = prepared['development']['gmTasks']['instructions']
            self.assertTrue(instructions.startswith('Choose native GM commands'), instructions)
            self.assertIn('not necessary to create a task', instructions)
            self.assertIn('OPTIONAL', instructions)
            primer = prepared['gm_observation_primer']
            self.assertEqual(primer['outcome'], 'read')
            self.assertLessEqual(primer['readStartedAtUnixMs'], primer['readCompletedAtUnixMs'])
            rows = {row['guid']:row for row in primer['observation']['result']['clients']}
            gm = rows['87']['observations']['world']
            owner_from_arlen = rows['1']['observations']['world']['owner']
            self.assertEqual((gm['self']['mapId'], gm['self']['x']), (0, -900))
            self.assertFalse(gm['owner']['present'])
            self.assertFalse(gm['owner']['positionKnown'])
            self.assertEqual((owner_from_arlen['guid'], owner_from_arlen['mapId'], owner_from_arlen['x']),
                             ('3', 349, 785))
            self.assertTrue(owner_from_arlen['present'])
            self.assertEqual(owner_from_arlen['positionAgeMs'], '1400')
            self.assertEqual(owner_from_arlen['positionRevision'], '22')
            self.assertEqual(owner_from_arlen['vitalsAgeMs'], '2100')
            self.assertEqual(rows['87']['observations']['actions']['position']['ageMs'], '430')
            self.assertTrue(rows['87']['observations']['actions']['position']['estimated'])
            self.assertNotIn('instanceId', gm['self'])
            self.assertNotIn('instanceId', owner_from_arlen)
            self.assertIn('historical', primer['semantics'])
            tools = {row['function']['name']:row['function'] for row in payload['tools']}
            self.assertIn('gm_execute', tools)
            self.assertNotIn('final_reply', tools)
            description = tools['gm_execute']['description']
            self.assertIn('.groupsummon', description)
            self.assertIn("GM's CURRENT location", description)
            self.assertIn('GPS', description)
            self.assertIn('self', description)
            self.assertEqual(state, original_state)
            self.assertFalse(any(peer.commands or peer.replies for peer in clients.values()))


class RawGMNamedDispatchTests(unittest.TestCase):
    setUp = command_fixtures.GMCommandEvidenceTests.setUp
    make_tools = command_fixtures.GMCommandEvidenceTests.make_tools
    execute = command_fixtures.GMCommandEvidenceTests.execute

    def test_offscreen_named_appear_needs_no_selection_and_unknown_is_not_replayed(self):
        self.rpc.local = []  # Public World cannot locally resolve Joel.
        self.rpc.post_failure = 'transport'
        command = '.appear Joel'
        with self.assertRaises(direct_tools.ToolOutcomeUnknown) as caught:
            self.execute(command, operation='real-owner:appear')
        self.assertFalse(caught.exception.receipt['replayAllowed'])
        self.assertEqual(self.rpc.commands, [command])
        self.assertEqual(len(self.rpc.packets), 1)
        opcode, body = self.rpc.packets[0]
        self.assertEqual(opcode, 0x95)
        self.assertEqual(body[8:], command.encode()+b'\0')
        self.assertFalse(any(code == 0x13d for code, _ in self.rpc.packets))
        path = Path(caught.exception.receipt['stateDir']) / 'state.json'
        original = path.read_bytes()
        self.rpc.cancelled = True
        self.assertFalse(self.make_tools().dispatch('gm_execute', dict(command=command),
                                                   operation_id='real-owner:appear')['ok'])
        self.assertEqual(path.read_bytes(), original)
        self.rpc.cancelled = False
        self.rpc.post_failure = None  # The fake transport would now accept a replay.
        self.tools = self.make_tools()
        with self.assertRaises(direct_tools.ToolOutcomeUnknown):
            self.execute(command, operation='real-owner:appear')
        self.tools = self.make_tools()
        again = self.execute(command, operation='real-owner:new-call')
        self.assertTrue(again['ok'], again)
        self.assertFalse(again['result']['commandTransportWritten'])
        self.assertFalse(again['result']['gameEffectClaimed'])
        self.assertEqual(path.read_bytes(), original)
        self.assertEqual(self.rpc.commands, [command])
        self.assertEqual(len(self.rpc.packets), 1)


class DirectFailureFeedbackTests(unittest.TestCase):
    generic = "I couldn't finish confirming that request. Its outcome still needs checking."

    def setUp(self):
        brain_fixtures.DirectBrainIntegrationTests.setUp(self)
        (self.workspace/'dev').mkdir()
        (self.workspace/'dev/task-runtime').write_bytes((ROOT/'dev/task-runtime').read_bytes())

    @contextlib.contextmanager
    def conversation(self, responses, *, developer=False):
        converse = brain_fixtures.converse
        self.rpc = brain_fixtures.PublicRPC(owner_request=True)
        self.control_state = {}
        self.legacy_actions = []
        with model_fixtures.FakeHTTP(responses) as http, \
                mock.patch.object(converse, 'ROOT', self.workspace), \
                mock.patch.object(converse, 'RPC', side_effect=lambda url:self.rpc if url == self.rpc.url else
                    (_ for _ in ()).throw(converse.ConversationError('Foreign RPC'))):
            brain = converse.DirectBrain(self.state, developer_workspace=self.workspace if developer else None,
                character_name='Mira', character_class='mage', model='fixture-model', provider='local',
                base_url=http.base+'/v1')
            controls = types.SimpleNamespace(actions_available=True, party_available=True,
                actions=self.rpc.call('buddy-actions.status', {}), party_session=self.rpc.core, events=[],
                refresh=lambda:None, context=lambda:dict(self.control_state),
                execute=lambda action, **_:self.legacy_actions.append(action))
            loop = converse.Conversation(self.rpc, brain, controls=controls,
                character_name='Mira', character_class='mage', character_guid=4,
                status=lambda **row:self.statuses.append(copy.deepcopy(row)))
            self.assertIsNone(loop.baseline())
            self.assertEqual((loop.session_id, loop.cursor), (self.rpc.chat, 0))
            yield loop, brain, http

    def test_current_zero_tool_http_400_reports_api_failure_not_model_refusal(self):
        error = dict(error=dict(code='context_size_exceeded',
            message='Context size exceeded; authorization=private-token'))
        with self.conversation([(400, error)]) as (loop, brain, http):
            with self.assertRaisesRegex(brain_fixtures.converse.ConversationError, 'provider_http_error'):
                loop.tick()
            self.assertEqual(len(http.requests), 1)
            self.assertEqual(loop.cursor, 1)
            self.assertEqual(self.rpc.commands, [])
            self.assertEqual(self.legacy_actions, [])
            self.assertEqual(len(self.rpc.replies), 2)  # Host ACK, then host transport status.
            text = self.rpc.replies[-1]['text']
            self.assertTrue(text.startswith('This local model API request failed before any model tool ran (HTTP 400).'), text)
            self.assertIn('Cause: Context size exceeded;', text)
            self.assertNotIn('private-token', text)
            self.assertNotIn(self.generic, text)
            self.assertNotIn('try again', text.lower())
            self.assertNotIn('repeat', text.lower())
            failure, api = brain.last_failure_receipt, brain.last_api_receipt
            self.assertEqual(api['requestId'], brain.last_request_id)
            self.assertEqual(failure['requestId'], api['requestId'])
            self.assertEqual(failure['toolCustodyRequestId'], api['requestId'])
            self.assertEqual((failure['chatSessionId'], failure['selfGuid'], failure['cursor']),
                             (self.rpc.chat, '4', 1))
            self.assertEqual(api['toolCalls'], 0)
            self.assertEqual(failure['counts'], dict(completedTools=0, failedTools=0))
            self.assertFalse(failure['pendingTools'])
            saved = json.loads((self.state/'brain-failures'/(brain.last_request_id+'.json')).read_text())
            self.assertEqual(saved['requestId'], api['requestId'])
            self.assertEqual(saved['api']['httpStatus'], 400)

    def test_actual_policy_delivery_then_http_400_keeps_generic_uncertainty_and_receipts(self):
        intent = dict(task='cast_spell', id='one-delivered-request', spellId=1449)
        before_error = {}
        def failed_after_delivery(_):
            paths = list((self.state/'api-conversations').glob('*.json'))
            self.assertEqual(len(paths), 1)
            before_error[paths[0]] = paths[0].read_bytes()
            return 400, dict(error=dict(message='Context size exceeded after a tool result.'))
        with self.conversation([brain_fixtures.chat(('delivered', 'policy_input', dict(value=intent))),
                                failed_after_delivery], developer=True) as (loop, brain, http):
            with self.assertRaisesRegex(brain_fixtures.converse.ConversationError, 'provider_http_error'):
                loop.tick()
            self.assertEqual(len(http.requests), 2)
            self.assertEqual(self.rpc.commands, [dict(sessionId=self.rpc.core, characterGuid='4',
                                                    kind='command', value=intent)])
            self.assertEqual(self.rpc.replies[-1]['text'], self.generic)
            self.assertEqual(self.legacy_actions, [])
            self.assertEqual(brain.last_api_receipt['toolCalls'], 1)
            failure = brain.last_failure_receipt
            self.assertEqual(failure['counts']['completedTools'], 1)
            self.assertTrue(failure['mutationToolEntered'])
            self.assertEqual(failure['tools'][0]['name'], 'policy_input')
            self.assertEqual(failure['tools'][0]['status'], 'returned')
            for path, original in before_error.items():
                self.assertEqual(path.read_bytes(), original)
                transcript = json.loads(original)
                tool = next(row for row in transcript['messages'] if row['role'] == 'tool')
                returned = json.loads(tool['content'])
                self.assertTrue(returned['result']['deliveryOnly'])
                self.assertFalse(returned['result']['gameEffectClaimed'])
                self.assertEqual(returned['result']['result']['status'], 'queued')
                self.assertIsNone(transcript['pending'])
            saved = json.loads((self.state/'brain-failures'/(brain.last_request_id+'.json')).read_text())
            self.assertEqual(saved['tools'], failure['tools'])

    def test_stale_prior_http_receipt_cannot_describe_current_preparation_failure(self):
        with self.conversation([(400, dict(error=dict(message='OLD failure must not label a later request.')))]) as (loop, brain, http):
            with self.assertRaisesRegex(brain_fixtures.converse.ConversationError, 'provider_http_error'):
                loop.tick()
            prior_api = copy.deepcopy(brain.last_api_receipt)
            prior_file = self.state/'brain-failures'/(brain.last_request_id+'.json')
            prior_bytes = prior_file.read_bytes()
            self.rpc.new_owner = True
            catalog = dict(available=True, sessionId=self.rpc.chat, emotes=[dict(name='WAVE', id=1)])
            self.control_state = dict(emote_catalog=catalog)
            original = self.rpc.call
            def changed_during_binding(method, params, **kwargs):
                result = original(method, params, **kwargs)
                if method == 'session.describe':
                    # The current conversation's first catalog validation passed;
                    # a subsequent native binding read exposes invalid new data.
                    catalog['emotes'] = []
                return result
            with mock.patch.object(self.rpc, 'call', side_effect=changed_during_binding):
                with self.assertRaisesRegex(brain_fixtures.converse.ConversationError, 'Invalid installed emote catalog'):
                    loop.tick()
            self.assertEqual(loop.cursor, 2)
            self.assertNotEqual(brain.last_request_id, prior_api['requestId'])
            self.assertEqual(brain.last_api_receipt, prior_api)
            self.assertEqual(brain.last_failure_receipt['requestId'], brain.last_request_id)
            self.assertEqual(brain.last_failure_receipt['cursor'], 2)
            self.assertEqual(len(http.requests), 1)  # No new HTTP request; the old 400 is stale.
            self.assertEqual(self.rpc.replies[-1]['text'], self.generic)
            self.assertNotIn('HTTP 400', self.rpc.replies[-1]['text'])
            self.assertEqual(prior_file.read_bytes(), prior_bytes)
            self.assertEqual(self.rpc.commands, [])


if __name__ == '__main__':
    unittest.main()
