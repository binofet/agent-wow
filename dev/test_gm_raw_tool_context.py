"""Raw GM facts/tool wiring only; scripted API text is not model-planning proof."""
import copy
import json
from pathlib import Path
import unittest

import direct_model
import direct_tools
import test_direct_brain as brain_fixtures
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


if __name__ == '__main__':
    unittest.main()
