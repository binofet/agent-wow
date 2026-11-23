"""Real direct brain/tool integration with local fake HTTP and public RPCs."""
import contextlib
import copy
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.machinery
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
import types
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import direct_model
import direct_tools

loader = importlib.machinery.SourceFileLoader("direct_brain_converse", str(Path(__file__).with_name("converse")))
spec = importlib.util.spec_from_loader(loader.name, loader)
converse = importlib.util.module_from_spec(spec)
loader.exec_module(converse)

REPLY = dict(text="I checked the observed result.", emote="NONE", action="NONE")


def chat(*calls):
    return dict(model="fixture-served-model", choices=[dict(finish_reason="tool_calls", message=dict(
        role="assistant", content=None, tool_calls=[dict(id=identifier, type="function", function=dict(
            name=name, arguments=json.dumps(arguments))) for identifier, name, arguments in calls]))])


def spoken(text=REPLY["text"]):
    return dict(model="fixture-served-model", choices=[dict(finish_reason="stop",
        message=dict(role="assistant", content=text))])


class FakeHTTP:
    def __init__(self, responses):
        self.responses, self.requests = list(responses), []

    def __enter__(self):
        outer = self
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                value = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                outer.requests.append(dict(path=self.path, headers=dict(self.headers), body=value))
                response = outer.responses.pop(0) if outer.responses else dict(error="unexpected request")
                raw = json.dumps(response).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                try: self.wfile.write(raw)
                except (BrokenPipeError, ConnectionResetError): pass
            def log_message(self, *args): pass
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.base = f"http://127.0.0.1:{self.server.server_port}/v1"
        return self

    def __exit__(self, *args):
        self.server.shutdown(); self.server.server_close(); self.thread.join(timeout=2)


class PublicRPC:
    url = "http://127.0.0.1:8087/rpc"
    core, chat, guid = "aw-direct-fixture", "chat-direct-fixture", "4"

    def __init__(self, *, owner_request=False):
        self.calls, self.commands, self.replies = [], [], []
        self.owner_request, self.baselined, self.new_owner = owner_request, False, False
        self.on_command = None
        self.catalog_guid = self.guid

    def call(self, method, params, timeout=None):
        self.calls.append((method, copy.deepcopy(params)))
        if method == "session.describe":
            return dict(sessionId=self.core, characterGuid=self.catalog_guid, revision="a"*64,
                methods=[dict(name="buddy-actions.status", effect="read", inputType="Empty")],
                hostMethods=[dict(name="session.send_event", effect="mutation")],
                messages={"Empty": dict(fields=[])})
        if method == "buddy-chat.poll":
            latest, messages = 1, []
            if self.owner_request and not self.baselined:
                self.baselined = True
                latest = 0
            elif self.new_owner:
                latest = 2
                if int(params.get("afterSequence", 0)) < 2:
                    messages = [dict(sequence="2", text="Mira, stop the old request now", senderGuid="3",
                                     channel="WHISPER", receiverGuid="4")]
            elif self.owner_request and int(params.get("afterSequence", 0)) < 1:
                messages = [dict(sequence="1", text="Mira, hello", senderGuid="3", channel="WHISPER", receiverGuid="4")]
            return dict(sessionId=self.chat, messages=messages, latestSequence=str(latest), nextSequence=str(latest))
        if method == "buddy-actions.status":
            return dict(sessionId=self.core, selfGuid=self.guid)
        if method == "buddy-party.state":
            return dict(sessionId=self.core)
        if method == "session.send_event":
            self.commands.append(copy.deepcopy(params))
            if self.on_command: self.on_command()
            return dict(status="queued", sessionId=self.core, characterGuid=self.guid, sequence="1")
        if method == "buddy-chat.send":
            self.replies.append(copy.deepcopy(params))
            return dict(transportWritten=True)
        raise converse.CapabilityUnavailable("Fixture read capability unavailable")


class DirectBrainIntegrationTests(unittest.TestCase):
    def gm_guide_fixture(self):
        rpc = PublicRPC(); rpc.guid = rpc.catalog_guid = "87"
        state = dict(character=dict(guid="87"), gm_controls_available=True,
            development=dict(enabled=True, directDevelopment=True, workspace=str(self.workspace),
                rpcUrl=rpc.url, sessionId=rpc.core, selfGuid="87", ownerGuid="3"),
            current_request=dict(source="owner", senderGuid="3", channel="WHISPER", conversationScope="WHISPER"))
        return rpc, state

    def test_loaded_gm_guide_preserves_full_catalog_and_native_tool_contract(self):
        reference = Path(__file__).with_name("lotharen-gm-commands.txt").read_text()
        catalog = reference.split("FULL COMMAND HELP AND SOURCE SIGNATURES\n", 1)[1].split(
            "\nPUBLIC SOURCE FILE MANIFEST\n", 1)[0]
        self.assertEqual(hashlib.sha256(catalog.encode()).hexdigest(),
            "43204986573bd7c99ada6e92d499be5b8b05a481b2be3d5763ebef4002d94581")
        rpc, state = self.gm_guide_fixture(); captured = []
        with patch.object(converse, "ROOT", self.workspace), patch.object(converse, "RPC", return_value=rpc):
            brain = converse.DirectBrain(self.state, character_name="Lotharen", character_class="paladin",
                developer_workspace=self.workspace, model="fixture-model", provider="local", base_url="http://127.0.0.1:1/v1")
            def fake(payload, *_):
                captured.append(copy.deepcopy(payload)); return spoken()
            with patch.object(brain.client, "_http", side_effect=fake): brain.generate([dict(text="Use your native tools.")], state)
        system = captured[0]["messages"][0]["content"]
        self.assertEqual(brain.gm_reference, reference); self.assertEqual(system.count(reference), 1)
        self.assertGreater(len(reference.encode()), 343804)
        for fact in (".groupsummon Arlen has produced actual arrivals", "Individual .summon attempts",
                     "GM self", "no target name/GUID", "newInstance is false", ".pinfo <online name>",
                     ".group join <existingGroupMember> Lotharen", "Keep the old messages and every entered action/unknown receipt"):
            self.assertIn(fact, system)
        tools = {r["function"]["name"]: r["function"] for r in captured[0]["tools"]}
        self.assertTrue({"gm_execute", "gm_inspect", "chat_read"} <= tools.keys())
        self.assertEqual(tools["gm_execute"]["parameters"]["required"], ["command"])
        self.assertEqual(tools["gm_execute"]["parameters"]["properties"]["command"]["type"], "string")
        self.assertNotIn("final_reply", tools); self.assertEqual(captured[0]["tool_choice"], "auto")
        self.assertFalse(rpc.commands or rpc.replies)

    def test_synthetic_recovery_keeps_history_feedback_and_partial_effects_without_host_replay(self):
        # Scripted API responses prove presentation/tool-history wiring, not real model planning or realm effects.
        old = "An older cheat sheet said to individually .summon each member after joining the group."
        goal = "Regroup our party and revive anyone who is actually dead."
        status = "What was confirmed? Do not issue new commands."
        partial = "The group command was written. Arlen is visible; Joel's arrival and life remain unknown."
        responses = [spoken("I will use current facts and the corrected guide."),
            chat(("help", "gm_execute", dict(command=".help groupsummon"))),
            chat(("feedback", "chat_read", dict(after_sequence="10", limit=20))),
            chat(("gather", "gm_execute", dict(command=".groupsummon Arlen"))),
            chat(("observe", "gm_inspect", {})), spoken(partial), spoken("Only partial arrival is confirmed; no new command issued.")]
        rpc, state = self.gm_guide_fixture(); entered = []
        state["party"] = dict(known=True, inGroup=True, leaderGuid="87", members=[dict(guid=str(g), online=True) for g in (1,3,4,5,87)])
        def gm(tools, name, args, operation_id=None):
            if name == "gm_execute":
                tools._authorise_gm(); entered.append(args["command"])
                return dict(transportWritten=True, completion="effect_not_yet_verified", gameEffectClaimed=False)
            self.assertEqual(name, "gm_inspect")
            return dict(gameEffectClaimed=False, clients=[dict(guid="87", identityMatches=True, group=state["party"],
                observations=dict(nearbyPlayers=[dict(guid="1", present=True, aliveKnown=True, alive=True)],
                    owner=dict(guid="3", present=False, aliveKnown=False, positionKnown=False)))])
        def feedback(tools, args):
            self.assertEqual(args, dict(after_sequence="10", limit=20))
            return dict(sessionId=rpc.chat, latestSequence="11", nextSequence="11", messages=[dict(
                sequence="11", channel="CHANNEL_UNSPECIFIED", senderGuid="0", text="Syntax: .groupsummon [playername]")])
        with FakeHTTP(responses) as http, patch.object(converse, "ROOT", self.workspace), patch.object(converse, "RPC", return_value=rpc), \
                patch.object(direct_tools.DirectTools, "_gm", autospec=True, side_effect=gm), \
                patch.object(direct_tools.DirectTools, "_chat_read", autospec=True, side_effect=feedback):
            brain = converse.DirectBrain(self.state, developer_workspace=self.workspace, character_name="Lotharen",
                character_class="paladin", model="fixture-model", provider="local", base_url=http.base)
            loop = converse.Conversation(rpc, brain, character_name="Lotharen", character_class="paladin", character_guid=87)
            loop.initialized, loop.session_id, loop.cursor = True, rpc.chat, 1
            for text in (old, goal, status):
                state["current_request"]["messages"] = [dict(text=text)]; reply = loop.generate_reply([], state)
                if text == goal: self.assertEqual(reply["text"], partial)
            final = http.requests[-1]["body"]["messages"]
            self.assertEqual([m["content"] for m in final if m["role"] == "user"], [old,goal,status])
            self.assertEqual(entered, [".help groupsummon", ".groupsummon Arlen"]); self.assertEqual(len(http.requests), 7)
            rows = [json.loads(m["content"])["result"] for m in final if m["role"] == "tool"]
            self.assertEqual(len(rows), 4); self.assertEqual(rows[0]["completion"], "effect_not_yet_verified")
            self.assertEqual(rows[1]["messages"][0]["channel"], "CHANNEL_UNSPECIFIED")
            self.assertFalse(rows[-1]["clients"][0]["observations"]["owner"]["aliveKnown"])
            self.assertEqual(sum(m["role"] == "system" for m in final), 1)
            self.assertIn("Later source-verified or actually observed corrections", final[0]["content"])
            self.assertFalse(rpc.commands or rpc.replies)

    def test_live_gm_facade_separates_owner_gm_tools_from_verified_peer_development(self):
        for source, gm_available, expected in (("owner", True, True), ("party_agent", True, False),
                                               ("owner", False, False)):
            with self.subTest(source=source, gm_available=gm_available):
                rpc = PublicRPC()
                rpc.guid = rpc.catalog_guid = "87"
                rpc.url = "http://127.0.0.1:8090/rpc"
                development = dict(enabled=True, directDevelopment=True, workspace=str(self.workspace),
                    rpcUrl=rpc.url, sessionId=rpc.core, selfGuid="87", ownerGuid="3")
                request = dict(source=source, senderGuid="3" if source == "owner" else "8",
                               verifiedPeer=source == "party_agent", developmentWhisper=source == "party_agent")
                state = dict(character=dict(guid="87"), development=development,
                             current_request=request, gm_controls_available=gm_available)
                with patch.object(converse, "ROOT", self.workspace), patch.object(converse, "RPC", return_value=rpc):
                    brain = converse.DirectBrain(self.state, developer_workspace=self.workspace,
                        character_name="Lotharen", character_class="paladin", model="fixture-requested-model",
                        provider="local", base_url="http://127.0.0.1:1/v1")
                    brain.gm_reference += '\nExisting local realm command reference fixture.'
                    self.assertTrue(brain.development_enabled(state))
                    requests = []
                    def capture(payload, *_):
                        requests.append(copy.deepcopy(payload))
                        return spoken()
                    with patch.object(brain.client, "_http", side_effect=capture):
                        brain.generate([dict(speaker="Joel" if source == "owner" else "Nixwick", text="Inspect your Lua")], state)
                names = {row["function"]["name"] for row in requests[0]["tools"]}
                self.assertTrue({"policy_apply", "workspace_write", "policy_input", "gm_task_status", "gm_current_task"} <= names)
                self.assertEqual({"gm_task_start", "gm_task_cancel", "gm_task_resume", "gm_execute"} <= names, expected)
                prompt = requests[0]['messages'][0]['content']
                self.assertEqual('Existing local realm command reference fixture.' in prompt, expected)
                self.assertNotIn('Do not replace that lifecycle with a lone revive', prompt)
                self.assertNotIn('Only with no current task use gm_task_start', prompt)
                self.assertFalse(rpc.commands or rpc.replies)

    def test_direct_gm_reference_keeps_actual_private_file_loader_without_task_mandate(self):
        reference = "Realm command .fixture has this local syntax."
        actual_read = Path.read_text
        def read(path, *args, **kwargs):
            return reference if path.name == "lotharen-gm-commands.txt" else actual_read(path, *args, **kwargs)
        with patch.object(Path, "read_text", read):
            brain = converse.DirectBrain(self.state, character_name="Lotharen", character_class="paladin",
                model="fixture-requested-model", provider="local", base_url="http://127.0.0.1:1/v1")
        self.assertEqual(brain.gm_reference, reference)
        self.assertIn(reference, brain.gm_instructions)  # Legacy path retains its existing instructions.
        captured = []
        def capture(payload, *_):
            captured.append(payload)
            return dict(choices=[dict(finish_reason="stop", message=dict(role="assistant", content="Understood."))])
        with patch.object(brain.client, "_http", side_effect=capture):
            brain.generate([dict(text="What GM tools can you use?")], dict(character=dict(guid="87"),
                current_request=dict(source="owner", senderGuid="3"), gm_controls_available=True,
                development=dict(gmTasks=dict(instructions="FORCED OLD TASK LIFECYCLE", currentTaskId="retained-task"))))
        system = captured[0]["messages"][0]["content"]
        self.assertIn(reference, system)
        self.assertIn("retained-task", system)
        self.assertNotIn("FORCED OLD TASK LIFECYCLE", system)
        self.assertNotIn("first read gm_current_task", system)

    def test_new_gm_mutation_tools_cannot_append_a_second_final_command(self):
        for name, arguments in (('gm_task_resume', dict(task_id='joel-to-lotharen-existing')),
                                ('gm_execute', dict(command='.gps'))):
            with self.subTest(tool=name):
                rpc = PublicRPC()
                rpc.guid = rpc.catalog_guid = '87'
                state = dict(character=dict(guid='87'), gm_controls_available=True,
                    development=dict(enabled=True, directDevelopment=True, workspace=str(self.workspace),
                        rpcUrl=rpc.url, sessionId=rpc.core, selfGuid='87', ownerGuid='3'),
                    current_request=dict(source='owner', senderGuid='3'))
                with patch.object(converse, 'ROOT', self.workspace), patch.object(converse, 'RPC', return_value=rpc):
                    brain = converse.DirectBrain(self.state, developer_workspace=self.workspace,
                        character_name='Lotharen', character_class='paladin', model='fixture-requested-model',
                        provider='local', base_url='http://127.0.0.1:1/v1')
                    responses = [chat(('mutation', name, arguments)),
                        chat(('final', 'final_reply', dict(REPLY, gm_command='.gps')))]
                    with patch.object(direct_tools.DirectTools, '_gm', return_value=dict(gameEffectClaimed=False)) as tool, \
                            patch.object(brain.client, '_http', side_effect=responses):
                        with self.assertRaises(converse.ConversationError) as refused:
                            brain.generate([dict(speaker='Joel', text='Continue my recovery')], state)
                    self.assertIn('invalid_tool_call', str(refused.exception))
                    self.assertEqual(tool.call_count, 1)
                    self.assertTrue(brain.last_failure_receipt['mutationToolEntered'])
                    self.assertFalse(rpc.commands or rpc.replies)

    def test_nonterminal_trade_and_pending_prefix_remain_exact_in_large_initial_view(self):
        from direct_context import CONTEXT_BYTES, encode, project_context
        for phase in ("CANCEL_SENT", "ACCEPT_SENT", "OPENING", "REQUESTED", "INVITED", "OPEN"):
            with self.subTest(phase=phase):
                trade = dict(sessionId="aw-current", phase=phase, partnerGuid="8", pendingCancel=True,
                             ownRequestedItems=[dict(itemGuid=str(index), count=20) for index in range(60)])
                state = dict(character=dict(guid="4"), current_request=dict(source="owner", senderGuid="3"),
                    trade=trade, professions=dict(pendingCancel=dict(operationId="exact-old-operation", sent=True)),
                    observations=dict(nearbyCreatures=[dict(raw="bulk-facts "*80) for _ in range(80)]))
                projected, _ = project_context(state)
                self.assertLessEqual(len(encode(projected)), CONTEXT_BYTES)
                custody = projected["currentUnresolvedCustody"]
                self.assertIn(dict(path=["trade"], value=trade), custody)
                self.assertIn(dict(path=["professions", "pendingCancel"],
                                   value=state["professions"]["pendingCancel"]), custody)

    def test_typed_request_mapping_is_projected_without_copying_host_callback_attributes(self):
        from direct_context import project_context
        class Request(dict):
            def __deepcopy__(self, memo):
                raise AssertionError("Host callback objects must not be deep-copied")
        request = Request(source="owner", senderGuid="3", commission=dict(goalId="original"))
        request.host_callback = object()
        for bulk in (False, True):
            with self.subTest(bulk=bulk):
                state = dict(character=dict(guid="4"), current_request=request)
                if bulk: state["spell_toolkit"] = dict(spells=[dict(raw="fact"*1000) for _ in range(20)])
                projected, _ = project_context(state)
                self.assertEqual(projected["current_request"], dict(request))
                self.assertIs(state["current_request"], request)
                self.assertTrue(hasattr(request, "host_callback"))

    def test_large_initial_facts_are_bounded_without_clipping_authority_party_or_unknown(self):
        from direct_context import CONTEXT_BYTES, encode, project_context
        people = [dict(guid=str(guid), present=True, aliveKnown=True, alive=guid != 5,
            healthKnown=True, health=300+guid, maxHealth=900, powerKnown=True, power=100,
            maxPower=700, unitFlagsKnown=True, unitFlags=0x80000 if guid == 5 else 0,
            mapKnown=True, mapId=349, positionKnown=True, x=guid, y=-220, z=-79,
            positionAgeMs="9007199254740993", vitalsAgeMs="37", observationAgeMs="99",
            positionInferred=False) for guid in (1, 3, 4, 5, 8)]
        state = dict(character=dict(guid="4", name="Mira", **{"class":"mage"}),
            current_request=dict(source="owner", senderGuid="3", controls_allowed=True,
                                 commission=dict(goalId="same-human-request", sessionId="aw-current")),
            development=dict(enabled=True, directDevelopment=True, workspace="/owned/mira",
                rpcUrl="http://127.0.0.1:8087/rpc", selfGuid="4", ownerGuid="3", sessionId="aw-current",
                policyCommands={"show":["same", "mechanical", "helper"]}),
            controls=dict(sessionId="aw-current", selfGuid="4", castPending=True, moving=False,
                lastSpellOperation=dict(spellId=8427, actionId="original-id", phase="transport_unconfirmed",
                                        operationRevision="18446744073709550001")),
            party=dict(known=True, inGroup=True, leaderGuid="1", members=[dict(guid=p["guid"], online=True) for p in people]),
            observations=dict(self=people[2], owner=people[1], nearbyPlayers=people,
                nearbyCreatures=[dict(guid=str(1000+i), health=100, observedText="raw-observed-data"*90) for i in range(60)]),
            spell_toolkit=dict(known=True, revision="88", spells=[dict(spellId=i+100, learned=True,
                mechanics="raw-client-spell-mechanics"*20) for i in range(180)]),
            inventory=dict(sessionId="aw-current", revision="74", items=[dict(guid=str(2000+i),
                count=20, metadata="raw-item-template-facts"*10) for i in range(48)]),
            trade=dict(phase="IDLE", lastJunkDisposal=dict(status="OUTCOME_UNCONFIRMED",
                       itemGuid="uncertain-original", requestId="preserve-me", count=1)))
        original = copy.deepcopy(state)
        self.assertGreater(len(encode(state)), 170*1024)
        projected, metrics = project_context(state)
        self.assertLessEqual(len(encode(projected)), CONTEXT_BYTES)
        self.assertEqual(metrics["contextBytes"], len(encode(projected)))
        self.assertEqual(metrics["fullContextBytes"], len(encode(state)))
        self.assertEqual(projected["character"], state["character"])
        self.assertEqual(projected["current_request"], state["current_request"])
        self.assertEqual(projected["development"]["sessionId"], "aw-current")
        self.assertEqual(projected["development"]["rpcUrl"], state["development"]["rpcUrl"])
        self.assertEqual(projected["controls"], state["controls"])
        self.assertEqual(projected["observations"]["nearbyPlayers"], people)
        self.assertEqual(projected["observations"]["self"], people[2])
        self.assertEqual(projected["observations"]["owner"], people[1])
        self.assertTrue(any(row["path"] == ["observations", "nearbyCreatures"]
                            for row in projected["contextProjection"]["omittedPaths"]))
        retained = projected.get("currentUnresolvedCustody", [])
        self.assertTrue(any(row["value"] == state["trade"]["lastJunkDisposal"] for row in retained)
                        or projected.get("trade") == state["trade"])
        self.assertEqual(state, original)

    def test_required_context_and_latest_history_overflow_refuse_without_silent_truncation(self):
        from direct_context import CONTEXT_BYTES, HISTORY_BYTES, ContextLimitError, encode, project_context, project_history
        state = dict(character=dict(guid="4"), current_request=dict(source="owner", commission="x"*CONTEXT_BYTES))
        with self.assertRaisesRegex(ContextLimitError, "Required identity"):
            project_context(state)
        latest = dict(speaker="Joel", channel="WHISPER", text="Latest exact request: " + "é"*100)
        old = [dict(speaker="Mira", text="old context "*100) for _ in range(20)]
        history, metrics = project_history([*old, latest])
        self.assertEqual(history[-1], latest)
        self.assertLessEqual(len(encode(history)), HISTORY_BYTES)
        self.assertGreater(metrics["historyOmittedRows"], 0)
        with self.assertRaisesRegex(ContextLimitError, "Latest user request"):
            project_history([dict(speaker="Joel", text="é"*HISTORY_BYTES)])

    def test_actual_direct_request_publishes_bytes_before_http_and_reads_full_captured_context(self):
        from direct_context import CONTEXT_BYTES, HISTORY_BYTES
        state = dict(character=dict(guid="4"), current_request=dict(source="owner", senderGuid="3"),
            observations=dict(self=dict(guid="4", aliveKnown=True, alive=True, healthKnown=True, health=900),
                nearbyCreatures=[dict(guid=str(index+100), rawFacts="large-observation "*100) for index in range(90)]),
            spell_toolkit=dict(known=True, revision="17", spells=[dict(spellId=100+index,
                mechanics="large-mechanical-facts "*50) for index in range(80)]))
        original = copy.deepcopy(state)
        latest = dict(speaker="Joel", channel="WHISPER", text="Tell me what you observed. é")
        history = [*[dict(speaker="Mira", text="older known chat "*120) for _ in range(12)], latest]
        brain = converse.DirectBrain(self.state, character_name="Mira", character_class="mage",
            model="fixture-requested-model", provider="local", base_url="http://127.0.0.1:1/v1")
        requests, early = [], []
        def capture(payload, *_):
            requests.append(copy.deepcopy(payload))
            if len(requests) == 1:
                early.append(json.loads((self.state/"development-events-last.json").read_text()))
                return chat(("full-facts", "context_read", dict(path=["spell_toolkit", "spells"], offset=79, limit=1)))
            return spoken()
        with patch.object(brain.client, "_http", side_effect=capture), \
             patch.object(converse, "RPC", side_effect=AssertionError("Captured context needs no game RPC")):
            self.assertEqual(brain.generate(history, state), REPLY)
        self.assertEqual(early[0]["childPhase"], "api.request")
        system = requests[0]["messages"][0]["content"]
        context_raw = system.split("Current observations and control state JSON:\n", 1)[1]
        self.assertGreater(len(context_raw.encode()), 170*1024)
        self.assertEqual(requests[0]["messages"][1], dict(role="user", content=latest["text"]))
        self.assertEqual(json.loads(context_raw)["spell_toolkit"], state["spell_toolkit"])
        self.assertEqual(early[0]["promptMetrics"]["contextBytes"], len(context_raw.encode()))
        self.assertEqual(early[0]["promptMetrics"]["historyBytes"], len(json.dumps(history,
            ensure_ascii=False, separators=(",", ":")).encode()))
        self.assertGreater(early[0]["promptMetrics"]["fullCapturedContextBytes"], 170*1024)
        page = json.loads(requests[1]["messages"][-1]["content"])["result"]
        self.assertEqual(page["path"], ["spell_toolkit", "spells"])
        self.assertEqual(page["total"], 80)
        self.assertEqual(page["rows"][0]["key"], 79)
        self.assertEqual(page["rows"][0]["preview"]["spellId"], 179)
        self.assertIn("context_read", [row["function"]["name"] for row in requests[0]["tools"]])
        self.assertEqual(state, original)

    def test_exact_http_body_metrics_are_published_before_connection_without_claiming_write(self):
        brain = converse.DirectBrain(self.state, character_name="Mira", character_class="mage",
            model="fixture-requested-model", provider="local", base_url="http://127.0.0.1:1/v1")
        observed = []
        class Connection:
            sock = None
            def connect(inner):
                record = json.loads((self.state/"development-events-last.json").read_text())
                observed.append(record)
            def request(inner, method, path, body, headers):
                self.assertEqual(method, "POST")
                self.assertEqual(path, "/v1/chat/completions")
                self.assertEqual(observed[0]["api"]["initialRequestBytes"], len(body))
                self.assertEqual(observed[0]["api"]["lastRequestBytes"], len(body))
                self.assertEqual(observed[0]["api"]["httpRequests"], 0)
                self.assertEqual(observed[0]["api"]["requestPhase"], "prepared")
                self.assertEqual(observed[0]["api"]["requestIndex"], 1)
            def getresponse(inner):
                return types.SimpleNamespace(status=200, read=lambda _: json.dumps(spoken()).encode())
            def close(inner): pass
        with patch.object(direct_model.http.client, "HTTPConnection", return_value=Connection()):
            self.assertEqual(brain.generate([dict(speaker="Joel", text="Hello")],
                dict(character=dict(guid="4"), current_request=dict(source="owner", senderGuid="3"))), REPLY)
        self.assertEqual(len(observed), 1)
        self.assertEqual(observed[0]["actionOutcome"], "unknown")

    def test_direct_request_compacts_redundant_emote_table_without_advertising_reply_tool(self):
        rows = [dict(id=1000+index, name="EMOTE_%03d" % index) for index in range(512)]
        state = dict(character=dict(guid="4"), current_request=dict(source="owner", senderGuid="3"),
            emote_catalog=dict(available=True, revision="emotes-observed", source="installed-chat",
                               sessionId="chat-current", emotes=rows),
            observations=dict(self=dict(guid="4", health=711, healthMax=900)),
            controls=dict(sessionId="aw-current", lastSpellOperation=dict(phase="transport_unconfirmed")),
            inventory=dict(revision="73", items=[dict(guid="42", count=3)]))
        original = copy.deepcopy(state)
        brain = converse.DirectBrain(self.state, character_name="Mira", character_class="mage",
            model="fixture-requested-model", provider="local", base_url="http://127.0.0.1:1/v1")
        selection = brain.model_selection()
        requests = []
        def capture(payload, *_):
            requests.append(copy.deepcopy(payload))
            return spoken()
        with patch.object(brain.client, "_http", side_effect=capture):
            reply = brain.generate([dict(speaker="Joel", text="Hello")], state)
        self.assertEqual(reply["emote"], "NONE")
        self.assertEqual(len(requests), 1)
        content = requests[0]["messages"][0]["content"]
        actual = json.loads(content.split("Current observations and control state JSON:\n", 1)[1])
        expected = copy.deepcopy(state)
        expected["emote_catalog"] = dict(available=True, revision="emotes-observed",
                                         source="installed-chat", count=512)
        expected["model_selection"] = selection
        self.assertEqual(actual, expected)
        self.assertNotIn("final_reply", {row["function"]["name"] for row in requests[0]["tools"]})
        self.assertEqual(len(original["emote_catalog"]["emotes"]), 512)
        self.assertEqual(state, original)
        self.assertLess(len(json.dumps(actual)), len(json.dumps(dict(state,
            model_selection=expected["model_selection"]))))

    def test_natural_owner_turns_preserve_actual_tool_transcript_and_new_raw_input(self):
        def said(text):
            return dict(model="fixture-served-model", choices=[dict(finish_reason="stop",
                message=dict(role="assistant", content=text))])
        first = "Mira, inspect your current body"
        second = "And what did that exact read say?"
        responses = [chat(("body-read", "read_observation", dict(method="buddy-actions.status", params={}))),
                     said("I read my current body."), said("That read identified body 4.")]
        with self.rig(responses) as (loop, brain, state, http):
            state["current_request"].update(channel="WHISPER", conversationScope="WHISPER",
                messages=[dict(speaker="Joel", text=first)])
            acknowledgement = dict(speaker="Mira", text="Got it. I'm checking that now.")
            self.assertEqual(loop.generate_reply([dict(speaker="Joel", text=first), acknowledgement], state)["text"],
                             "I read my current body.")
            # A listener restart creates a new model client but retains the same bound API transcript.
            brain.client = direct_model.DirectModel("local", brain.model, http.base)
            state["current_request"]["messages"] = [dict(speaker="Joel", text=second)]
            state["observations"] = dict(freshMarker="current-turn-only")
            reply = loop.generate_reply([dict(speaker="Joel", text=first), acknowledgement,
                                         dict(speaker="Joel", text=second)], state)
            self.assertEqual(reply["text"], "That read identified body 4.")
            actual = http.requests[2]["body"]["messages"]
            self.assertEqual([row["content"] for row in actual if row["role"] == "user"], [first, second])
            self.assertTrue(any(row.get("tool_calls", [{}])[0].get("id") == "body-read"
                                for row in actual if row["role"] == "assistant"))
            result = next(row for row in actual if row["role"] == "tool")
            self.assertEqual(json.loads(result["content"])["result"]["selfGuid"], "4")
            self.assertIn("current-turn-only", actual[0]["content"])
            self.assertNotIn(acknowledgement["text"], json.dumps(actual))
            self.assertNotIn(first, actual[0]["content"])
            self.assertNotIn(second, actual[0]["content"])
            self.assertEqual(http.requests[0]["body"]["tool_choice"], "auto")
            self.assertNotIn("max_tokens", http.requests[0]["body"])
            self.assertEqual(sum(method == "buddy-actions.status" for method, _ in self.rpc.calls), 2)

    def test_natural_chat_chunks_stop_on_new_owner_before_the_next_packet(self):
        brain = converse.DirectBrain(self.state, character_name="Mira", character_class="mage",
            model="fixture-requested-model", provider="local", base_url="http://127.0.0.1:1/v1")
        loop = converse.Conversation(self.rpc, brain, character_name="Mira", character_class="mage", character_guid=4)
        loop.initialized, loop.session_id, loop.cursor = True, self.rpc.chat, 1
        original = self.rpc.call
        def call(method, params, timeout=None):
            value = original(method, params, timeout)
            if method == "buddy-chat.send": self.rpc.new_owner = True
            return value
        with patch.object(self.rpc, "call", side_effect=call):
            with self.assertRaises(converse.OwnerInputPending):
                loop.send_reply(dict(text="Long natural reply "*50, emote="NONE"), "WHISPER", target_guid=3)
        self.assertEqual(len(self.rpc.replies), 1)
        self.assertLessEqual(len(self.rpc.replies[0]["text"].encode()), 255)

    def test_natural_context_isolated_by_private_peer_chat_and_body(self):
        requests = []
        brain = converse.DirectBrain(self.state, character_name="Mira", character_class="mage",
            model="fixture-requested-model", provider="local", base_url="http://127.0.0.1:1/v1")
        def capture(payload, *_):
            requests.append(copy.deepcopy(payload))
            return dict(choices=[dict(finish_reason="stop", message=dict(role="assistant", content="Understood."))])
        with patch.object(brain.client, "_http", side_effect=capture):
            for sender, channel, chat_id, sid, text in (("3", "WHISPER", "chat-a", "aw-a", "Owner private"),
                    ("8", "WHISPER", "chat-a", "aw-a", "Peer private"),
                    ("3", "PARTY", "chat-a", "aw-a", "Owner party"),
                    ("3", "WHISPER", "chat-b", "aw-b", "Replacement body")):
                brain._generation_context = dict(sessionId=sid, selfGuid="4", chatSessionId=chat_id, cursor=1)
                state = dict(character=dict(guid="4"), current_request=dict(source="owner", senderGuid=sender,
                    channel=channel, messages=[dict(text=text)]))
                brain.generate([], state)
        self.assertEqual([[row["content"] for row in request["messages"] if row["role"] == "user"]
                          for request in requests], [["Owner private"], ["Peer private"], ["Owner party"], ["Replacement body"]])
        self.assertEqual(len(list((self.state/"api-conversations").glob("*.json"))), 4)

    def test_visible_assistant_text_precedes_tool_work_and_long_final_is_chunked(self):
        progress = "I'll read the actual observation first."
        long_text = "é"*180 + "\nThen I can explain the observed result without clipping it."
        tool = chat(("read", "read_observation", dict(method="buddy-actions.status", params={})))
        tool["choices"][0]["message"]["content"] = progress
        final = dict(choices=[dict(finish_reason="stop", message=dict(role="assistant", content=long_text))])
        with self.rig([tool, final]) as (loop, brain, state, http):
            state["current_request"].update(channel="WHISPER", conversationScope="WHISPER",
                messages=[dict(text="Tell me what you observe")])
            order = []
            original = self.rpc.call
            def call(method, params, timeout=None):
                if method in ("buddy-chat.send", "buddy-actions.status"): order.append((method, dict(params)))
                return original(method, params, timeout)
            with patch.object(self.rpc, "call", side_effect=call):
                reply = loop.generate_reply([], state)
                self.assertEqual(reply["text"], long_text)
                loop.send_reply(reply, "WHISPER", target_guid=3, history_key="WHISPER")
            self.assertEqual(order[0], ("buddy-chat.send", dict(channel="WHISPER", language="COMMON",
                text=progress, recipient="Joel")))
            self.assertEqual(order[1][0], "buddy-actions.status")
            chunks = [row["text"] for row in self.rpc.replies[1:]]
            self.assertGreater(len(chunks), 1)
            self.assertTrue(all(len(row.encode()) <= 255 for row in chunks))
            self.assertEqual("".join(chunks), " ".join(long_text.split()))
            self.assertEqual(loop.history["WHISPER"][-1]["text"], long_text)

    def setUp(self):
        temporary = tempfile.TemporaryDirectory(dir=Path(tempfile.gettempdir()).resolve())
        self.addCleanup(temporary.cleanup)
        self.folder = Path(temporary.name)
        self.workspace, self.state = self.folder / "actor", self.folder / "listener"
        self.workspace.mkdir(mode=0o700); self.state.mkdir(mode=0o700)
        subprocess.run(["git", "init", "--quiet", str(self.workspace)], check=True,
                       stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        self.rpc = PublicRPC()
        self.statuses = []
        for name in ("CodexBrain", "codex_command"):
            blocker = patch.object(converse, name, side_effect=AssertionError("Codex must never be invoked"))
            blocker.start(); self.addCleanup(blocker.stop)

    @contextlib.contextmanager
    def rig(self, responses, *, developer=True):
        with FakeHTTP(responses) as http, patch.object(converse, "ROOT", self.workspace), \
             patch.object(converse, "RPC", side_effect=lambda url: self.rpc if url == self.rpc.url else
                          (_ for _ in ()).throw(converse.ConversationError("Foreign RPC"))):
            brain = converse.DirectBrain(self.state, developer_workspace=self.workspace if developer else None,
                character_name="Mira", character_class="mage", model="fixture-requested-model",
                provider="local", base_url=http.base)
            controls = types.SimpleNamespace(actions_available=True, party_available=True,
                actions=self.rpc.call("buddy-actions.status", {}), party_session=self.rpc.core, events=[])
            loop = converse.Conversation(self.rpc, brain, controls=controls,
                character_name="Mira", character_class="mage", character_guid=4,
                status=lambda **value: self.statuses.append(copy.deepcopy(value)))
            loop.initialized, loop.session_id, loop.cursor = True, self.rpc.chat, 1
            state = dict(character=dict(guid="4"), development=loop.development_context("owner"),
                         current_request=dict(source="owner", senderGuid="3"))
            self.assertEqual(brain.development_enabled(state), developer)
            yield loop, brain, state, http

    def test_real_tool_roundtrip_workspace_public_read_input_schema_and_progress(self):
        source = "return { authored = true }\n"
        intent = dict(task="fixture_spell", id="owned-one-shot")
        responses = [chat(("write", "workspace_write", dict(path="candidate.lua", content=source, expected_sha256=""))),
                     chat(("read", "read_observation", dict(method="buddy-actions.status", params={}))),
                     chat(("input", "policy_input", dict(value=intent))),
                     spoken()]
        with self.rig(responses) as (loop, brain, state, http):
            self.assertEqual(loop.generate_reply([], state), REPLY)
            self.assertIsInstance(brain.client, direct_model.DirectModel)
            self.assertEqual((self.workspace / "candidate.lua").read_text(), source)
            self.assertEqual(self.rpc.commands, [dict(sessionId=self.rpc.core, characterGuid="4", kind="command", value=intent)])
            self.assertEqual(len(http.requests), 4)
            self.assertTrue(all(request["path"] == "/v1/chat/completions" for request in http.requests))
            tools = {tool["function"]["name"] for tool in http.requests[0]["body"]["tools"]}
            self.assertTrue({"workspace_write", "read_observation", "policy_input"} <= tools)
            self.assertNotIn("final_reply", tools)
            self.assertNotIn("Authorization", http.requests[0]["headers"])
            read_result = json.loads(http.requests[2]["body"]["messages"][-1]["content"])
            self.assertEqual(read_result["result"]["selfGuid"], "4")
            command_result = json.loads(http.requests[3]["body"]["messages"][-1]["content"])
            self.assertTrue(command_result["result"]["deliveryOnly"])
            self.assertFalse(command_result["result"]["gameEffectClaimed"])
            receipt = brain.last_development_result
            self.assertEqual((receipt["sessionId"], receipt["chatSessionId"], receipt["selfGuid"], receipt["cursor"]),
                             (self.rpc.core, self.rpc.chat, "4", 1))
            self.assertEqual(receipt["counts"]["completedTools"], 3)
            self.assertFalse(receipt["pendingTools"])
            self.assertEqual(receipt["actionOutcome"], "not_inferred_from_model_reply")
            self.assertEqual(brain.model_selection()["execution"], "direct_api")
            self.assertFalse(brain.model_selection()["codexInvoked"])
            self.assertEqual(brain.model_selection()["providerReportedModel"], "fixture-served-model")
            self.assertTrue(any(value.get("brainProgress", {}).get("pendingToolCount") == 1 for value in self.statuses))
            progress = [value["brainProgress"] for value in self.statuses if isinstance(value.get("brainProgress"), dict)]
            self.assertEqual(progress[-1]["outcome"], "completed")

    def test_invalid_final_schema_retains_failed_api_receipt_without_fallback(self):
        with self.rig([chat(("invalid-final", "final_reply", dict(text="Incomplete reply")))]) as (loop, brain, state, http):
            with self.assertRaisesRegex(converse.ConversationError, "invalid_tool_call"):
                loop.generate_reply([], state)
            self.assertEqual(len(http.requests), 1)
            self.assertFalse(self.rpc.commands or self.rpc.replies)
            receipt = brain.last_failure_receipt
            self.assertEqual(receipt["reason"], "invalid_tool_call")
            self.assertEqual(receipt["api"]["execution"], "direct_api")
            self.assertEqual(receipt["api"]["requestId"], receipt["requestId"])
            self.assertEqual(receipt["api"]["httpRequests"], 1)
            self.assertTrue(receipt["receiptStored"])

    def test_owner_preemption_preserves_entered_unknown_and_forbids_later_batch_dispatch(self):
        def entered():
            self.rpc.new_owner = True
            time.sleep(.8)  # Existing Conversation cancellation polling interval is .75s.
        self.rpc.on_command = entered
        responses = [chat(("entered", "policy_input", dict(value=dict(task="fixture", id="do-not-repeat"))),
                          ("later", "workspace_write", dict(path="must-not-exist.lua", content="return {}", expected_sha256="")))]
        with self.rig(responses) as (loop, brain, state, http):
            with self.assertRaises(converse.ConversationError):
                loop.generate_reply([], state)
            self.assertEqual(len(self.rpc.commands), 1)
            self.assertEqual(len(http.requests), 1)
            self.assertFalse((self.workspace / "must-not-exist.lua").exists())
            self.assertEqual(loop.cursor, 1)  # New owner input remains for the normal next turn.
            receipt = brain.last_failure_receipt
            self.assertEqual(receipt["sessionId"], self.rpc.core)
            self.assertEqual(len(receipt["pendingTools"]), 1)
            self.assertEqual(receipt["tools"][0]["status"], "unknown")
            self.assertEqual(receipt["actionOutcome"], "unknown")
            saved = json.loads((self.state / "brain-failures" / (receipt["requestId"] + ".json")).read_text())
            self.assertEqual(saved["pendingTools"], receipt["pendingTools"])
            unknown = saved["tools"][0]["unknownReceipt"]
            value_hash = hashlib.sha256(json.dumps(self.rpc.commands[0]["value"], allow_nan=False,
                                                   separators=(",", ":")).encode()).hexdigest()
            self.assertEqual(unknown["valueSHA256"], value_hash)
            self.assertEqual((unknown["sessionId"], unknown["characterGuid"]), (self.rpc.core, "4"))
            self.assertIs(unknown["replayAllowed"], False)
            self.assertEqual(unknown["outcome"], "unconfirmed")
            self.assertNotIn("value", unknown)
            self.assertTrue(any(value.get("brainFailure", {}).get("pendingToolCount") == 1 for value in self.statuses))

    def test_neither_mutating_nor_read_tool_can_append_unadvertised_legacy_follow(self):
        helper_dir = self.workspace / "dev"
        helper_dir.mkdir()
        (helper_dir / "task-runtime").write_bytes(Path(__file__).with_name("task-runtime").read_bytes())
        for mutation in (True, False):
            with self.subTest(mutation=mutation):
                self.state = self.folder / ("mutation-listener" if mutation else "read-listener")
                self.state.mkdir(mode=0o700)
                self.rpc = PublicRPC()
                self.rpc.new_owner = True  # Current owner request2, consumed normally by tick.
                command = dict(value=dict(task="fixture", id="exactly-once"))
                tool = ("first", "policy_input", command) if mutation else (
                    "first", "read_observation", dict(method="buddy-actions.status", params={}))
                responses = [chat(tool), chat(("final-follow", "final_reply", dict(REPLY, action="FOLLOW")))]
                with self.rig(responses) as (loop, brain, _, http):
                    legacy_actions = []
                    loop.controls.refresh = lambda: None
                    loop.controls.context = lambda: {}
                    loop.controls.execute = lambda action, **kw: legacy_actions.append(action)
                    with self.assertRaisesRegex(converse.ConversationError, "invalid_tool_call"):
                        loop.tick()
                    self.assertEqual(len(self.rpc.commands), 1 if mutation else 0)
                    self.assertFalse(legacy_actions)
                    self.assertEqual(brain.last_failure_receipt.get("mutationToolEntered", False), mutation)
                    self.assertEqual(brain.last_failure_receipt["counts"]["completedTools"], 1)
                    self.assertEqual(brain.last_failure_receipt["outcome"], "failed")
                    self.assertEqual(len(http.requests), 2)

    def test_live_facade_passes_actual_chat_session_to_feedback_read(self):
        responses = [chat(("feedback", "read_observation", dict(method="buddy-chat.poll",
            params=dict(afterSequence="0", limit=20)))), spoken()]
        original = self.rpc.call
        def call(method, params, timeout=None):
            result = original(method, params, timeout)
            if method == "session.describe":
                result["methods"].append(dict(name="buddy-chat.poll", effect="read", inputType="PollRequest"))
                result["messages"]["PollRequest"] = dict(fields=[dict(name="sessionId")])
            return result
        with patch.object(self.rpc, "call", side_effect=call), self.rig(responses) as (loop, brain, state, http):
            self.assertNotEqual(self.rpc.core, self.rpc.chat)
            self.assertEqual(loop.generate_reply([], state), REPLY)
            feedback = [params for method, params in self.rpc.calls
                        if method == "buddy-chat.poll" and params.get("limit") == 20]
            self.assertEqual(feedback, [dict(afterSequence="0", limit=20, sessionId=self.rpc.chat)])
            read_result = json.loads(http.requests[1]["body"]["messages"][-1]["content"])
            self.assertEqual(read_result["result"]["sessionId"], self.rpc.chat)
            self.assertFalse(self.rpc.commands)

    def test_actual_tools_reject_replaced_body_before_a_policy_command(self):
        self.rpc.catalog_guid = "8"
        with self.rig([chat(("foreign", "policy_input", dict(value=dict(task="fixture", id="foreign")))),
                       spoken()]) as (loop, _, state, http):
            loop.generate_reply([], state)
            self.assertFalse(self.rpc.commands)
            result = json.loads(http.requests[1]["body"]["messages"][-1]["content"])
            self.assertFalse(result["ok"])
            self.assertIn("current_body_changed", result["error"]["error"])

    def test_direct_without_workspace_never_queues_legacy_repair_after_control_failure(self):
        helper_dir = self.workspace / "dev"
        helper_dir.mkdir()
        (helper_dir / "task-runtime").write_bytes(Path(__file__).with_name("task-runtime").read_bytes())
        self.rpc.new_owner = True
        response = chat(("follow", "final_reply", dict(REPLY, action="FOLLOW")))
        with self.rig([response], developer=False) as (loop, brain, _, http):
            self.assertIsNone(brain.developer_workspace)
            attempted = []
            def refused(action, **kwargs):
                attempted.append(action)
                raise converse.ConversationError("Fixture control refusal")
            loop.controls.refresh = lambda: None
            loop.controls.context = lambda: {}
            loop.controls.execute = refused
            with patch.object(loop.controls, "report_problem", create=True,
                              side_effect=AssertionError("No repair worker may be queued")) as repair:
                with self.assertRaisesRegex(converse.ConversationError, "invalid_tool_call"):
                    loop.tick()
                repair.assert_not_called()
            self.assertEqual(attempted, [])
            self.assertFalse(self.rpc.commands)
            request = http.requests[0]["body"]
            prepared = json.loads(request["messages"][0]["content"].split(
                "Current observations and control state JSON:\n", 1)[1])
            self.assertIs(prepared["current_request"]["repair_requests_allowed"], False)
            tools = {tool["function"]["name"]: tool["function"] for tool in request["tools"]}
            self.assertEqual(set(tools), {"context_read"})
            self.assertNotIn("final_reply", tools)
            self.assertFalse(brain.model_selection()["codexInvoked"])

    def test_actual_converse_main_local_config_uses_direct_brain_and_emits_reply(self):
        self.rpc.owner_request = True
        with FakeHTTP([spoken()]) as http, \
             patch.object(converse, "RPC", return_value=self.rpc), \
             patch.object(converse, "registered_developer_workspace", return_value=None), \
             patch.object(converse.signal, "signal"), contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(converse.main(["run", "--profile", "mira", "--character-name", "Mira",
                "--character-class", "mage", "--character-guid", "4", "--rpc-url", self.rpc.url,
                "--state-dir", str(self.state), "--banter-seconds", "0", "--max-replies", "1",
                "--poll-seconds", ".2", "--max-hours", ".002", "--model-provider", "local", "--model-base-url", http.base,
                "--model", "fixture-requested-model"]), 0)
            self.assertEqual(len(http.requests), 1)
            user_rows = [row for row in http.requests[0]["body"]["messages"] if row["role"] == "user"]
            self.assertEqual(user_rows, [dict(role="user", content="Mira, hello")])
            self.assertNotIn("Got it.", json.dumps(user_rows))
            self.assertEqual(self.rpc.replies[-1]["text"], REPLY["text"])
            self.assertFalse(self.rpc.commands)
            status = json.loads((self.state / "status.json").read_text())
            self.assertEqual(status["coreSessionId"], self.rpc.core)
            self.assertEqual(status["chatSessionId"], self.rpc.chat)
            self.assertEqual(status["modelSelection"]["execution"], "direct_api")
            self.assertFalse(status["modelSelection"]["codexInvoked"])
            self.assertTrue(status["modelSelection"]["effectiveModelKnown"])
            self.assertEqual(status["modelSelection"]["providerReportedModel"], "fixture-served-model")
            self.assertEqual(status["brainProgress"]["outcome"], "completed")
            self.assertEqual(status["brainProgress"]["pendingToolCount"], 0)


if __name__ == "__main__":
    unittest.main()
