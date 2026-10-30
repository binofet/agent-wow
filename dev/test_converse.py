#!/usr/bin/env python3
"""Conversation acceptance tests with fake RPC and a local fake Codex process."""

import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import time
import unittest
from unittest.mock import patch


loader = importlib.machinery.SourceFileLoader("converse", str(Path(__file__).with_name("converse")))
spec = importlib.util.spec_from_loader(loader.name, loader)
converse = importlib.util.module_from_spec(spec)
loader.exec_module(converse)


def message(sequence, text="Hello, Arlen", sender=3, channel="SAY", **fields):
    return dict(sequence=str(sequence), text=text, senderGuid=str(sender), channel=channel, **fields)


def poll(messages=(), latest=None, **fields):
    latest = latest if latest is not None else (int(messages[-1]["sequence"]) if messages else 0)
    return dict(messages=list(messages), latestSequence=str(latest), nextSequence=str(latest), **fields)


class FakeRPC:
    def __init__(self, responses):
        self.responses, self.calls = list(responses), []

    def call(self, method, params):
        self.calls.append((method, params))
        if method == "buddy-chat.poll":
            response = self.responses.pop(0)
            if isinstance(response, Exception):
                raise response
            return response
        return {"transportWritten": True}


class FakeBrain:
    def __init__(self, reply=None):
        self.histories = []
        self.reply = reply or {"text": "Hello, Joel!", "emote": "WAVE"}

    def generate(self, history):
        self.histories.append(list(history))
        if isinstance(self.reply, Exception):
            raise self.reply
        return self.reply


class ConversationTests(unittest.TestCase):
    def test_start_tails_old_messages_and_idle_never_calls_model(self):
        rpc = FakeRPC([poll([message(1, "Old chat")]), poll(latest=1)])
        brain = FakeBrain()
        loop = converse.Conversation(rpc, brain)
        loop.tick()
        loop.tick()
        self.assertEqual(brain.histories, [])
        self.assertEqual(loop.cursor, 1)
        self.assertTrue(all(method == "buddy-chat.poll" for method, _ in rpc.calls))

    def test_only_joel_plain_public_chat_triggers_one_reply_per_burst(self):
        entries = [message(1, sender=99), message(2, self=True),
                   message(3, channel="WHISPER", text="Private"),
                   message(4, text=".server shutdown"), message(5, text="Hi!"),
                   message(6, text="How are you?")]
        rpc = FakeRPC([poll(), poll(entries), poll(latest=6)])
        brain = FakeBrain()
        loop = converse.Conversation(rpc, brain)
        loop.tick()
        loop.tick()
        loop.tick()
        self.assertEqual([entry["text"] for entry in brain.histories[0]], ["Hi!", "How are you?"])
        self.assertEqual(len(brain.histories), 1)
        self.assertEqual(rpc.calls[2], ("buddy-chat.send", {
            "channel": "SAY", "language": "COMMON", "text": "Hello, Joel!"}))
        self.assertEqual(rpc.calls[3], ("buddy-chat.emote", {"emote": "WAVE", "targetGuid": "3"}))

    def test_party_is_routed_to_party_and_history_is_not_exposed_in_say(self):
        rpc = FakeRPC([poll(), poll([message(1, "Party secret", channel="PARTY_LEADER")]),
                       poll([message(2, "Public chat")])])
        brain = FakeBrain({"text": "Understood.", "emote": "NONE"})
        loop = converse.Conversation(rpc, brain)
        for _ in range(3):
            loop.tick()
        self.assertEqual([call[1]["channel"] for call in rpc.calls if call[0] == "buddy-chat.send"], ["PARTY", "SAY"])
        self.assertNotIn("Party secret", json.dumps(brain.histories[1]))

    def test_restart_baselines_without_replaying_old_messages(self):
        rpc = FakeRPC([poll(latest=50), converse.CursorReset(),
                       poll([message(1, "Already present")]), poll([message(2, "New chat")])])
        brain = FakeBrain()
        loop = converse.Conversation(rpc, brain)
        for _ in range(4):
            loop.tick()
        self.assertEqual(len(brain.histories), 1)
        self.assertEqual(brain.histories[0][0]["text"], "New chat")

    def test_overflow_answers_only_newest_message_and_records_gap(self):
        rpc = FakeRPC([poll(), poll([message(257, "Old backlog"), message(258, "Newest")], cursorLost=True)])
        brain, events = FakeBrain(), []
        loop = converse.Conversation(rpc, brain, status=lambda **fields: events.append(fields))
        loop.tick()
        loop.tick()
        self.assertEqual([entry["text"] for entry in brain.histories[0]], ["Newest"])
        self.assertTrue(any(event.get("event") == "ring_overflow" for event in events))

    def test_invalid_reply_or_generation_failure_cannot_invoke_rpc(self):
        for reply in ({"text": ".server shutdown", "emote": "NONE"},
                      {"text": "Hello", "emote": "DANCE"},
                      {"text": "Hello", "emote": "NONE", "method": "session.logout"},
                      converse.ConversationError("backend unavailable")):
            with self.subTest(reply=reply):
                rpc, brain = FakeRPC([poll(), poll([message(1)])]), FakeBrain(reply)
                loop = converse.Conversation(rpc, brain)
                loop.tick()
                with self.assertRaises(converse.ConversationError):
                    loop.tick()
                self.assertEqual(loop.cursor, 1)
                self.assertTrue(all(method == "buddy-chat.poll" for method, _ in rpc.calls))

    def test_ambiguous_chat_write_is_not_retried_and_no_emote_is_sent(self):
        rpc = FakeRPC([poll(), poll([message(1)]), poll(latest=1)])
        original = rpc.call
        def call(method, params):
            if method == "buddy-chat.send":
                rpc.calls.append((method, params))
                raise converse.ConversationError("write timed out")
            return original(method, params)
        rpc.call = call
        brain = FakeBrain()
        loop = converse.Conversation(rpc, brain)
        loop.tick()
        with self.assertRaises(converse.ConversationError):
            loop.tick()
        loop.tick()
        self.assertEqual(len(brain.histories), 1)
        self.assertEqual(len([call for call in rpc.calls if call[0] == "buddy-chat.send"]), 1)
        self.assertFalse(any(call[0] == "buddy-chat.emote" for call in rpc.calls))

    def test_history_is_bounded_and_stop_after_generation_prevents_send(self):
        rpc = FakeRPC([poll()] + [poll([message(i)]) for i in range(1, 30)])
        brain = FakeBrain({"text": "Hello", "emote": "NONE"})
        loop = converse.Conversation(rpc, brain)
        for _ in range(30):
            loop.tick()
        self.assertLessEqual(len(loop.history["SAY"]), 20)
        self.assertTrue(all(len(history) <= 20 for history in brain.histories))
        rpc = FakeRPC([poll(), poll([message(1)])])
        loop = converse.Conversation(rpc, FakeBrain(), stopped=lambda: True)
        loop.tick()
        with self.assertRaises(converse.Stopped):
            loop.tick()
        self.assertTrue(all(method == "buddy-chat.poll" for method, _ in rpc.calls))


class BoundaryTests(unittest.TestCase):
    def test_multibyte_limit_and_control_command_markup_rejection(self):
        self.assertEqual(converse.plain_text("é" * 127), "é" * 127)
        for text in ("é" * 128, "", "  ", " x", "x\n", "|Hitem", "/say hi", "!command", ".command", "a\u200bb", "\ud800"):
            with self.subTest(text=repr(text)), self.assertRaises(converse.ConversationError):
                converse.plain_text(text)

    def test_rpc_only_allows_explicit_loopback_and_no_proxy_or_redirects(self):
        for url in ("https://127.0.0.1:8086/rpc", "http://example.com:8086/rpc",
                    "http://127.0.0.1/rpc", "http://u:p@127.0.0.1:8086/rpc",
                    "http://127.0.0.1:8086/rpc?token=x", "http://127.0.0.1:8086/other"):
            with self.subTest(url=url), self.assertRaises(converse.ConversationError):
                converse.RPC(url)
        rpc = converse.RPC("http://127.0.0.1:8086/rpc")
        with self.assertRaises(converse.ConversationError):
            rpc.call("session.logout", {})

    def test_rpc_validates_response_id_and_precise_restart_fault(self):
        class Response:
            def __init__(self, data):
                self.data = data
            def __enter__(self):
                return self
            def __exit__(self, *args):
                pass
            def read(self, limit):
                return json.dumps(self.data).encode()
        rpc = converse.RPC("http://127.0.0.1:8086/rpc")
        with patch.object(rpc.opener, "open", return_value=Response({"jsonrpc": "2.0", "id": 999, "result": {}})):
            with self.assertRaises(converse.ConversationError):
                rpc.call("buddy-chat.poll", {})
        with patch.object(rpc.opener, "open", return_value=Response({"jsonrpc": "2.0", "id": 2, "error": {"message": "rpc error: cursor is newer than this module session; restart polling with after_sequence 0"}})):
            with self.assertRaises(converse.CursorReset):
                rpc.call("buddy-chat.poll", {})

    def test_cli_flags_preserve_readonly_and_disable_integrations(self):
        command = converse.codex_command("codex", "workspace", "schema", "output")
        self.assertEqual(command[command.index("--sandbox") + 1], "read-only")
        self.assertIn("--ignore-user-config", command)
        self.assertIn("--ephemeral", command)
        self.assertIn('web_search="disabled"', command)
        self.assertTrue(all(feature in command for feature in converse.DISABLED_FEATURES))
        self.assertNotIn("--ignore-rules", command)
        self.assertFalse(any("dangerously" in arg for arg in command))
        self.assertNotIn("--model", command)

    def test_tool_error_and_incomplete_events_rejected(self):
        for item in ({"type": "command_execution", "command": "pwd"},
                     {"type": "mcp_tool_call"}, {"type": "error", "message": "bad"}):
            raw = json.dumps({"type": "item.completed", "item": item}) + '\n{"type":"turn.completed"}\n'
            with self.assertRaises(converse.ConversationError):
                converse.inspect_events(raw.encode())
        for raw in (b"{}\n", b"[]\n", b'{"type":"item.completed","item":[]}\n',
                    b'{"type":"turn.failed"}\n', b'{"type":"turn.started"}\n'):
            with self.assertRaises(converse.ConversationError):
                converse.inspect_events(raw)
        converse.inspect_events(b'{"type":"thread.started"}\n{"type":"turn.started"}\n{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}\n{"type":"turn.completed"}\n')

    def test_only_exact_intentional_host_warning_is_allowed_before_turn(self):
        warning = json.dumps({"type": "item.completed", "item": {
            "id": "item_0", "type": "error", "message": converse.DISABLED_CODE_MODE_WARNING}}).encode() + b"\n"
        start, end = b'{"type":"turn.started"}\n', b'{"type":"turn.completed"}\n'
        converse.inspect_events(warning + start + end)
        with self.assertRaises(converse.ConversationError):
            converse.inspect_events(start + warning + end)
        with self.assertRaises(converse.ConversationError):
            converse.inspect_events(warning.replace(b"fail closed", b"something else") + start + end)

    def test_real_subprocess_contract_with_fake_cli_and_timeout(self):
        # Run a real local child to exercise JSONL, output-file and timeout paths;
        # this is not an OpenAI call and does not read CLI authentication.
        with tempfile.TemporaryDirectory() as temporary:
            folder = Path(temporary)
            fake = folder / "fake-codex"
            fake.write_text("#!/usr/bin/env python3\nimport json,pathlib,sys\n"
                            "args=sys.argv; sys.stdin.read()\n"
                            "workspace=pathlib.Path(args[args.index('--cd')+1]); schema=pathlib.Path(args[args.index('--output-schema')+1])\n"
                            "assert not workspace.is_relative_to(schema.parent)\n"
                            "pathlib.Path(args[args.index('--output-last-message')+1]).write_text(json.dumps({'text':'Hi, Joel!','emote':'WAVE'}))\n"
                            "print(json.dumps({'type':'turn.completed'}))\n")
            fake.chmod(0o700)
            reply = converse.CodexBrain(fake, folder).generate([{"speaker": "Joel", "text": "Hi"}])
            self.assertEqual(reply, {"text": "Hi, Joel!", "emote": "WAVE"})
            self.assertEqual(list(folder.iterdir()), [fake])
            fake.write_text("#!/usr/bin/env python3\nimport time\ntime.sleep(10)\n")
            started = time.monotonic()
            with self.assertRaises(converse.ConversationError):
                converse.CodexBrain(fake, folder, timeout=0.15).generate([])
            self.assertLess(time.monotonic() - started, 4)
            self.assertEqual(list(folder.iterdir()), [fake])

    def test_private_state_rejects_symlink_and_singleton_lock(self):
        with tempfile.TemporaryDirectory() as temporary:
            folder = converse.ensure_state(Path(temporary) / "private")
            self.assertEqual(folder.stat().st_mode & 0o777, 0o700)
            converse.write_json(folder / "status.json", {"instance": "mine"})
            self.assertEqual((folder / "status.json").stat().st_mode & 0o777, 0o600)
            (folder / "link").symlink_to(folder / "status.json")
            with self.assertRaises(OSError):
                converse.private_open(folder / "link", os.O_RDONLY)
            with os.fdopen(converse.private_open(folder / "lock", os.O_RDWR | os.O_CREAT), "r+") as first:
                converse.fcntl.flock(first, converse.fcntl.LOCK_EX)
                with os.fdopen(converse.private_open(folder / "lock", os.O_RDWR), "r+") as second:
                    self.assertTrue(converse.is_locked(second))


if __name__ == "__main__":
    unittest.main()
