"""Real direct brain/tool integration with local fake HTTP and public RPCs."""
import contextlib
import copy
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

REPLY = dict(text="I checked the observed result.", emote="NONE", action="NONE",
             quest_id=0, item_entry=0, gold_copper=0)


def chat(*calls):
    return dict(model="fixture-served-model", choices=[dict(finish_reason="tool_calls", message=dict(
        role="assistant", content=None, tool_calls=[dict(id=identifier, type="function", function=dict(
            name=name, arguments=json.dumps(arguments))) for identifier, name, arguments in calls]))])


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
    def rig(self, responses):
        with FakeHTTP(responses) as http, patch.object(converse, "ROOT", self.workspace), \
             patch.object(converse, "RPC", side_effect=lambda url: self.rpc if url == self.rpc.url else
                          (_ for _ in ()).throw(converse.ConversationError("Foreign RPC"))):
            brain = converse.DirectBrain(self.state, developer_workspace=self.workspace,
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
            self.assertTrue(brain.development_enabled(state))
            yield loop, brain, state, http

    def test_real_tool_roundtrip_workspace_public_read_input_schema_and_progress(self):
        source = "return { authored = true }\n"
        intent = dict(task="fixture_spell", id="owned-one-shot")
        responses = [chat(("write", "workspace_write", dict(path="candidate.lua", content=source, expected_sha256=""))),
                     chat(("read", "read_observation", dict(method="buddy-actions.status", params={}))),
                     chat(("input", "policy_input", dict(value=intent))),
                     chat(("final", "final_reply", REPLY))]
        with self.rig(responses) as (loop, brain, state, http):
            self.assertEqual(loop.generate_reply([], state), REPLY)
            self.assertIsInstance(brain.client, direct_model.DirectModel)
            self.assertEqual((self.workspace / "candidate.lua").read_text(), source)
            self.assertEqual(self.rpc.commands, [dict(sessionId=self.rpc.core, characterGuid="4", kind="command", value=intent)])
            self.assertEqual(len(http.requests), 4)
            self.assertTrue(all(request["path"] == "/v1/chat/completions" for request in http.requests))
            tools = {tool["function"]["name"] for tool in http.requests[0]["body"]["tools"]}
            self.assertTrue({"workspace_write", "read_observation", "policy_input", "final_reply"} <= tools)
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
            with self.assertRaisesRegex(converse.ConversationError, "json_schema_mismatch"):
                loop.generate_reply([], state)
            self.assertEqual(len(http.requests), 1)
            self.assertFalse(self.rpc.commands or self.rpc.replies)
            receipt = brain.last_failure_receipt
            self.assertEqual(receipt["reason"], "json_schema_mismatch")
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
            self.assertTrue(any(value.get("brainFailure", {}).get("pendingToolCount") == 1 for value in self.statuses))

    def test_actual_tools_reject_replaced_body_before_a_policy_command(self):
        self.rpc.catalog_guid = "8"
        with self.rig([chat(("foreign", "policy_input", dict(value=dict(task="fixture", id="foreign")))),
                       chat(("final", "final_reply", REPLY))]) as (loop, _, state, http):
            loop.generate_reply([], state)
            self.assertFalse(self.rpc.commands)
            result = json.loads(http.requests[1]["body"]["messages"][-1]["content"])
            self.assertFalse(result["ok"])
            self.assertIn("current_body_changed", result["error"]["error"])

    def test_actual_converse_main_local_config_uses_direct_brain_and_emits_reply(self):
        self.rpc.owner_request = True
        with FakeHTTP([chat(("reply", "final_reply", REPLY))]) as http, \
             patch.object(converse, "RPC", return_value=self.rpc), \
             patch.object(converse, "registered_developer_workspace", return_value=None), \
             patch.object(converse.signal, "signal"), contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(converse.main(["run", "--profile", "mira", "--character-name", "Mira",
                "--character-class", "mage", "--character-guid", "4", "--rpc-url", self.rpc.url,
                "--state-dir", str(self.state), "--banter-seconds", "0", "--max-replies", "1",
                "--poll-seconds", ".2", "--max-hours", ".002", "--model-provider", "local", "--model-base-url", http.base,
                "--model", "fixture-requested-model"]), 0)
            self.assertEqual(len(http.requests), 1)
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
