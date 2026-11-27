"""Raw GM facts/tool wiring only; scripted API text is not model-planning proof."""
import contextlib
import copy
import hashlib
import json
from pathlib import Path
import re
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

    def test_budgeted_gm_owned_collections_leave_room_for_lookup_grant_and_readback(self):
        from direct_context import encode
        with self.gm_primer_fixture() as (brain, state, clients):
            sid = clients['87'].core
            state['development']['gmTasks'] = brain_fixtures.converse.gm_tasks.tools(self.workspace,'87',clients['87'].url)
            user = 'Look up the requested supplies, grant them once, and inspect the result.'
            state['current_request']['messages'] = [dict(text=user)]
            state['journal'] = dict(sessionId=sid,questLogRevision='19',entries=[
                dict(questId=100+i,templateKnown=True,details='A synthetic quest description. '*23) for i in range(12)])
            inventory = state['inventory'] = dict(sessionId=sid,known=True,inventoryRevision='21',freeSlots=2,
                itemsTruncated=False,items=[dict(itemGuid=str(500+i),entry=1000+i,metadataKnown=True,
                    name='Synthetic carried item',count=3,bag=0,slot=i) for i in range(32)],
                gear=dict(equipmentKnown=True,candidatesTruncated=False,candidates=[dict(entry=1000+i,
                    gearEquipEligible=False,conservativeUpgrade=False,gearBlockedReason='slot_or_skill_unavailable')
                    for i in range(40)]))
            custody=dict(operationId='retained-uncertain',outcomeUnknown=True,replayAllowed=False)
            state['journal']['entries'][7]['pendingReceipt']=custody
            inventory['items'][5]['receipt']=dict(operationId='retained-written',transportWritten=True)
            original = copy.deepcopy(state)
            captured = []
            def seed(payload,*_):
                captured.append(copy.deepcopy(payload))
                return brain_fixtures.spoken()
            with mock.patch.object(brain.client,'_http',side_effect=seed):
                brain.generate([],state)
            transcript = next((self.state/'api-conversations').glob('*.json'))
            saved = json.loads(transcript.read_text())
            receipt = model_fixtures.LocalPromptBudgetTests.gm_selection_receipt
            for i in range(14):
                args = dict(command=f'.lookup item item{i}') if i<8 else dict(command=f'.additem {1000+i}',select_guid='12')
                result = receipt(args,f'historical-{i}')
                saved['messages'].extend([dict(role='user',content=f'Past owner instruction {i}'),
                    brain_fixtures.chat((f'old-{i}','gm_execute',args))['choices'][0]['message'],
                    dict(role='tool',tool_call_id=f'old-{i}',content=encode(result).decode())])
            transcript.write_text(json.dumps(saved))
            # Set up a deterministic synthetic meter with 791 units of room after history-only paging.
            # The production meter still counts the configured model's actual tool-aware template.
            history = saved['messages']+[dict(role='user',content=user)]
            prior, _ = brain.client._local_prompt(captured[0]['messages'][0]['content'],history,
                [tool['function'] for tool in captured[0]['tools']],{'context_read'},{},None,canonical=history,
                aggressive=True,archive_read=lambda *_:None)
            offset = 40169-len(encode(prior))//4
            def meter(payload): return offset+len(encode(payload))//4
            brain.client = direct_model.DirectModel('local',brain.model,brain.client.base_url,
                local_prompt_token_limit=40960,local_token_counter=meter)
            commands = []
            def native(tools, gm, args, operation_id=None):
                commands.append(args['command'])
                result = receipt(args,operation_id)['result']
                result['selection']['rawSelf']['observationAgeMs']=str(len(commands)*70)
                if args['command'].startswith('.additem'):
                    result['feedback'].update(status='no_reply_yet',messages=[],systemMessages=[])
                if args['command'].startswith('.character check bag'):
                    line = dict(sequence=str(len(commands)),text=f'Bag {len(commands)-2}: [Synthetic item] x 3')
                    result['feedback'].update(messages=[line],systemMessages=[line])
                return result
            bag_queries = [f'.character check bag {i}' for i in range(1,6)]
            replies = [brain_fixtures.chat(('lookup','gm_execute',dict(command='.lookup item supplies'))),
                brain_fixtures.chat(('grant','gm_execute',dict(command='.additem 1012',select_guid='12'))),
                *[brain_fixtures.chat((f'bag-{i}','gm_execute',dict(command=command,select_guid='12')))
                    for i,command in enumerate(bag_queries,1)],
                brain_fixtures.chat(('fresh','gm_inspect',dict(path=['clients','1','rawSelf','guid'])),
                    ('quest','context_read',dict(path=['journal','entries','2','questId'])),
                    ('item','context_read',dict(path=['inventory','items','3','itemGuid'])),
                    ('gear','context_read',dict(path=['inventory','gear','candidates','4','entry']))),
                brain_fixtures.spoken()]
            payloads = []
            def provider(payload,*_):
                payloads.append(copy.deepcopy(payload))
                return replies.pop(0)
            with mock.patch.object(brain.client,'_http',side_effect=provider), \
                    mock.patch.object(direct_tools.DirectTools,'_gm_execute',native):
                brain.generate([],state)
            self.assertEqual(commands,['.lookup item supplies','.additem 1012',*bag_queries])
            self.assertEqual(len(payloads),9)
            prefix='Current observations and control state JSON:\n'
            system, brief = payloads[0]['messages'][0]['content'].split(prefix,1)
            brief=json.loads(brief)
            self.assertEqual(brief['inventory']['items']['retainedRowsByIndex']['5'],inventory['items'][5])
            self.assertEqual(brief['journal']['entries']['retainedRowsByIndex']['7'],state['journal']['entries'][7])
            self.assertEqual(brief['inventory']['gear']['candidates']['contextPath'],['inventory','gear','candidates'])
            self.assertEqual(brief['inventory']['inventoryRevision'],'21')
            self.assertEqual(brief['journal']['questLogRevision'],'19')
            last = {r['tool_call_id']:json.loads(r['content']) for r in payloads[-1]['messages'] if r.get('role')=='tool'}
            self.assertEqual([last[key]['result']['value'] for key in ('quest','item','gear')],[102,'503',1004])
            self.assertEqual(last['fresh']['result']['value'],'1')
            self.assertFalse(last['grant']['result']['gameEffectClaimed'])
            self.assertEqual(last['grant']['result']['feedback']['messages'],[])
            self.assertIn('hostArchivedSelection',last['lookup'])
            self.assertNotIn('hostArchivedSelection',last['bag-5'])
            self.assertEqual(last['bag-5']['result']['feedback']['systemMessages'][0]['text'],'Bag 5: [Synthetic item] x 3')
            self.assertEqual(saved['messages'],json.loads(transcript.read_text())['messages'][:len(saved['messages'])])
            self.assertEqual(state,original)
            old_sizes = []
            for payload in payloads:
                full = copy.deepcopy(payload)
                view = json.loads(full['messages'][0]['content'].split(prefix,1)[1])
                view.update(journal=state['journal'],inventory=inventory)
                full['messages'][0]['content'] = system+prefix+encode(view).decode()
                old_sizes.append(meter(full))
                self.assertLessEqual(meter(payload),40960)
            self.assertGreater(max(old_sizes),40960)

    def test_gm_collection_projection_requires_current_own_data_and_retains_small_rows(self):
        from direct_context import gm_character_collections
        state=dict(journal=dict(sessionId='aw-foreign',entries=[dict(questId=1,details='x'*9000)]),
            inventory=dict(sessionId='aw-own',known=False,items=[dict(itemGuid='7',extra='x'*8000)]))
        self.assertEqual(gm_character_collections(state,'aw-own','87'),state)
        state['inventory'].update(known=True,selfGuid='12')
        self.assertEqual(gm_character_collections(state,'aw-own','87'),state)
        state['inventory']=dict(sessionId='aw-own',known=True,items=[dict(itemGuid='7')])
        self.assertEqual(gm_character_collections(state,'aw-own','87'),state)

    def test_index_never_enlarges_a_small_or_already_compact_spellbook(self):
        from direct_context import gm_spellbook_index
        small = dict(spells=[dict(spellId=1, name='One', mechanicsHint=dict(targetMask=2))])
        large = dict(spells=[dict(spellId=i+1, name='水'*100) for i in range(100)])
        large['spells'][0]['passive'] = False
        for book in (small, large):
            self.assertEqual(gm_spellbook_index(book), book)

    def test_budgeted_owner_gm_can_page_an_indexed_spellbook_with_prior_receipts_intact(self):
        from direct_context import encode
        with self.gm_primer_fixture() as (brain, state, clients):
            brain.gm_reference = (ROOT / 'dev/lotharen-gm-commands.txt').read_text()
            state['development']['gmTasks'] = brain_fixtures.converse.gm_tasks.tools(
                self.workspace, '87', clients['87'].url)
            state['current_request']['messages'] = [dict(text='Inspect the current party.')]
            body = dict(sessionId=clients['87'].core, selfGuid='87')
            row = dict(metadataKnown=True, name='Synthetic learned rank', rank=3,
                genericCastSupported=True, classificationReason='automatic_selection_unclassified',
                castTimeMs=1500, flatCost=85, rangeYards=40, mechanics=dict(targetMask=2, castMinMs=1500,
                    gcdMs=1500, durationIndex=0, cooldownMs=0, effects=[dict(targetSelectorA=21)]))
            raw = dict(body, spellbookKnown=True, spellbookRevision='17',
                spells=[dict(row, spellId=20000+i) for i in range(80)],
                lastBuff=dict(phase='outcome_unconfirmed', spellId=20001, targetGuid='3'))
            book = state['spell_toolkit'] = brain_fixtures.converse.spell_toolkit_context(raw, body, 87)
            confirmed = dict(operationId='synthetic-confirmed', commandWritten=True, outcome='confirmed')
            unknown = dict(operationId='synthetic-unknown', outcomeUnknown=True, replayAllowed=False)
            book['spells'][10]['operationReceipt'] = confirmed

            payloads = []
            def capture(payload, *_):
                payloads.append(copy.deepcopy(payload))
                if len(payloads) == 2:
                    return brain_fixtures.chat(('rank', 'context_read',
                        dict(path=['spell_toolkit','spells','79','mechanicsHint','targetSelectors','0','a'])))
                return brain_fixtures.spoken()
            with mock.patch.object(brain.client, '_http', side_effect=capture):
                brain.generate([], state)
            path = next((self.state/'api-conversations').glob('*.json'))
            retained = json.loads(path.read_text())
            result_rows = []
            for identifier, receipt in [('known', confirmed), ('unsettled', unknown)]:
                call = brain_fixtures.chat((identifier, 'gm_execute', {'command':'.fixture'}))
                result = dict(role='tool', tool_call_id=identifier, content=encode(receipt).decode())
                retained['messages'].extend([call['choices'][0]['message'], result])
                result_rows.append(result)
            path.write_text(json.dumps(retained))
            # The original local payload alone exceeds this exact byte budget.
            budget = len(encode(payloads[0])) - 20000
            brain.client = direct_model.DirectModel('local', brain.model, brain.client.base_url,
                local_prompt_budget_bytes=budget)
            with mock.patch.object(brain.client, '_http', side_effect=capture):
                brain.generate([], state)
            self.assertEqual(len(payloads), 3)
            original, wire, read = payloads
            prefix = 'Current observations and control state JSON:\n'
            first = wire['messages'][0]['content']
            brief = json.loads(first.split(prefix, 1)[1])
            self.assertEqual(json.loads(original['messages'][0]['content'].split(prefix, 1)[1])['spell_toolkit'], book)
            index = brief['spell_toolkit']
            self.assertEqual(index['spellDetailsContextPath'], ['spell_toolkit','spells'])
            self.assertEqual([r['spellId'] for r in index['spells']], [r['spellId'] for r in book['spells']])
            self.assertEqual(index['lastBuff'], book['lastBuff'])
            self.assertEqual(index['spells'][10], book['spells'][10])
            self.assertEqual(index['spells'][79], {k:book['spells'][79][k] for k in ('spellId','name','rank','metadataKnown')})
            self.assertLess(len(encode(wire)), budget)
            self.assertGreater(len(encode(original))-len(encode(wire)), 30000)
            self.assertIn('COMPLETE COMMAND NAME INDEX (739)', first)
            self.assertEqual(original['tools'], wire['tools'])
            self.assertEqual(len(brief['gm_observation_primer']['observation']['result']['clients']), 5)
            for result in result_rows:
                self.assertIn(result, wire['messages'])
                self.assertIn(result, json.loads(path.read_text())['messages'])
            fetched = next(r for r in read['messages'] if r.get('tool_call_id') == 'rank')
            self.assertEqual(json.loads(fetched['content'])['result']['value'], 21)
            # Exercise the token-only configuration. This fixture meter records
            # the exact input passed to it without estimating model tokens.
            metered = []
            def meter(payload):
                metered.append(payload['messages'][0]['content'])
                return 1
            brain.client = direct_model.DirectModel('local', brain.model, brain.client.base_url,
                local_prompt_token_limit=2, local_token_counter=meter)
            with mock.patch.object(brain.client, '_http', side_effect=capture):
                brain.generate([], state)
            self.assertEqual(json.loads(metered[-1].split(prefix, 1)[1])['spell_toolkit'], index)
            self.assertFalse(any(peer.commands or peer.replies for peer in clients.values()))

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


class CommonGMReferenceTests(unittest.TestCase):
    setUp = brain_fixtures.DirectBrainIntegrationTests.setUp
    gm_guide_fixture = brain_fixtures.DirectBrainIntegrationTests.gm_guide_fixture
    gm_primer_fixture = brain_fixtures.DirectBrainIntegrationTests.gm_primer_fixture
    start = 'COMMON GM EXCHANGES — TARGET, SYNTAX AND EVIDENCE'
    end = 'END COMMON GM EXCHANGES'

    def test_common_exchanges_reach_initial_system_and_full_catalog_remains_readable(self):
        reference = (ROOT/'dev/lotharen-gm-commands.txt').read_text()
        section = reference.split(self.start, 1)[1].split(self.end, 1)[0]
        forms = ('.character level Joel 40', '.levelup Joel 2', '.learn <spellId> [all]',
                 '.learn all my trainer', '.learn all default <playerName>',
                 '.learn all recipes <professionName>', '.setskill <skillId> <value> [max]',
                 '.additem Joel <itemId> <count>', '.gear repair Joel',
                 *('.quest '+verb+' <questId> [playerName]' for verb in ('add','complete','remove','status','reward')))
        prose = ' '.join(section.split())
        for form in forms:self.assertIn(form, prose)
        for fact in ('ABSOLUTE level 40', 'DELTA +2', 'GM SELF even when another player is selected',
                     'adding is not equipping', 'reward choice index 0', 'Current realm syntax'):
            self.assertIn(fact, prose)
        catalog = reference.split('FULL COMMAND HELP AND SOURCE SIGNATURES\n', 1)[1].split(
            '\nPUBLIC SOURCE FILE MANIFEST\n', 1)[0]
        self.assertEqual(hashlib.sha256(catalog.encode()).hexdigest(),
                         '43204986573bd7c99ada6e92d499be5b8b05a481b2be3d5763ebef4002d94581')
        headers = list(re.finditer(r'^={72}\n(\.[^\r\n]+)\n', catalog, re.M))
        names = [row[1] for row in headers]
        self.assertEqual(len(names), 739)
        selected = names.index('.character level')
        entry = catalog[headers[selected].start():headers[selected+1].start()]
        captured = []
        actual_tools = direct_tools.DirectTools
        class Capture(actual_tools):
            def __init__(self, *args, **kwargs):
                super().__init__(*args, **kwargs)
                captured.append(self)
        reads = brain_fixtures.chat(
            ('entry', 'context_read', dict(path=['gmReference','commands','.character level','text'], limit=4096)),
            ('raw', 'context_read', dict(path=['gmReference','raw'], limit=4096)))
        with self.gm_primer_fixture() as (brain, state, clients), \
                brain_fixtures.FakeHTTP([reads, brain_fixtures.spoken('The reference is available.')]) as http, \
                mock.patch.object(direct_tools, 'DirectTools', Capture):
            self.assertEqual(brain.gm_reference, reference)  # The maintained file loader, not a test prompt override.
            state['current_request']['messages'] = [dict(text='What GM commands can address these ordinary requests? Read only.')]
            brain.client = direct_model.DirectModel('local', brain.model, http.base)
            brain.generate([], state)
            first = http.requests[0]['body']
            system = first['messages'][0]['content']
            self.assertIn(self.start, system)
            self.assertIn(section, system)
            self.assertIn(self.end, system)
            initial_names = system.split('COMPLETE COMMAND NAME INDEX (739)\n', 1)[1].split(
                '\n\nThe full exact reference', 1)[0].splitlines()
            self.assertEqual(initial_names, names)
            self.assertNotIn(reference, system)  # The complete raw reference remains in the existing lazy view.
            self.assertEqual(first['tool_choice'], 'auto')
            functions = {row['function']['name'] for row in first['tools']}
            self.assertIn('gm_execute', functions)
            self.assertNotIn('final_reply', functions)
            returned = {row['tool_call_id']:json.loads(row['content'])['result']
                for row in http.requests[1]['body']['messages'] if row['role'] == 'tool'}
            self.assertEqual(returned['entry']['value'], entry[:4096])
            self.assertEqual(returned['raw']['value'], reference[:4096])
            full = captured[-1].context['gmReference']
            self.assertEqual(full['raw'].encode(), reference.encode())
            self.assertEqual(full['sha256'], hashlib.sha256(reference.encode()).hexdigest())
            self.assertEqual(list(full['commands']), names)
            self.assertEqual(full['commands']['.character level']['text'], entry)
            self.assertFalse(any(peer.commands or peer.replies for peer in clients.values()))

    def test_common_reference_does_not_grant_peer_owner_gm_authority(self):
        with self.gm_primer_fixture() as (brain, state, clients), \
                brain_fixtures.FakeHTTP([brain_fixtures.spoken('I can inspect my accepted Lua.')]) as http:
            state['current_request'].update(source='party_agent', senderGuid='8', verifiedPeer=True,
                developmentWhisper=True, messages=[dict(text='Inspect your current Lua, without GM actions.')])
            self.assertTrue(brain.development_enabled(state))
            brain.client = direct_model.DirectModel('local', brain.model, http.base)
            brain.generate([], state)
            payload = http.requests[0]['body']
            tools = {row['function']['name'] for row in payload['tools']}
            self.assertIn('policy_input', tools)
            self.assertFalse({'gm_execute','gm_task_start','gm_task_resume','gm_task_cancel'} & tools)
            self.assertNotIn(self.start, payload['messages'][0]['content'])
            prepared = json.loads(payload['messages'][0]['content'].split(
                'Current observations and control state JSON:\n', 1)[1])
            self.assertNotIn('gmReference', prepared)
            self.assertFalse(any(peer.commands or peer.replies for peer in clients.values()))


if __name__ == '__main__':
    unittest.main()
