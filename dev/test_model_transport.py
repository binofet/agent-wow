"""Provider selection through the existing agent process, without model calls."""
import importlib.machinery
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import time
import tomllib
import unittest
from unittest.mock import patch


loader = importlib.machinery.SourceFileLoader("transport_converse", str(Path(__file__).with_name("converse")))
spec = importlib.util.spec_from_loader(loader.name, loader)
converse = importlib.util.module_from_spec(spec)
loader.exec_module(converse)


class ModelTransportTests(unittest.TestCase):
    model = "nvidia/nemotron-3.5-super-vl"
    endpoint = "http://127.0.0.1:18765/v1"

    def transport(self, **changes):
        return converse.model_transport(**dict(provider="local", model=self.model,
                                               base_url=self.endpoint, **changes))

    def config(self, command):
        result = {}
        def merge(target, incoming):
            for key, data in incoming.items():
                if isinstance(data, dict):
                    merge(target.setdefault(key, {}), data)
                else:
                    target[key] = data
        for index, value in enumerate(command[:-1]):
            if value == "-c":
                merge(result, tomllib.loads(command[index + 1]))
        return result

    def fake(self, folder, *, reply=None, wait=False, tool=False, fail=False):
        executable = folder / "fake-codex"
        reply = reply or dict(text="I checked my observations.", emote="NONE", action="NONE")
        # This process only writes controlled fixture files, never calls a model or game RPC.
        executable.write_text("#!/usr/bin/env python3\nimport json,os,pathlib,sys,time\n"
            "sys.dont_write_bytecode=True\nargs=sys.argv[1:]\nprompt=sys.stdin.read()\n"
            + "folder=pathlib.Path(" + repr(str(folder)) + ")\n"
            + "with (folder/'calls.jsonl').open('a') as out:out.write(json.dumps(dict(args=args,"
              "endpoint=os.environ.get('CODEX_OSS_BASE_URL'),proxy=os.environ.get('HTTPS_PROXY'),"
              "key=os.environ.get('OPENAI_API_KEY'),prompt=prompt))+'\\n')\n"
            + "print(json.dumps(dict(type='turn.started')),flush=True)\n"
            + ("(folder/'owned.lua').write_text('preserved completed edit')\n"
               "print(json.dumps(dict(type='item.completed',item=dict(id='edit-1',type='file_change',status='completed'))),flush=True)\n" if tool else "")
            + ("(folder/'waiting').write_text('ready')\ntime.sleep(30)\n" if wait else "")
            + ("sys.exit(1)\n" if fail else "")
            + "pathlib.Path(args[args.index('--output-last-message')+1]).write_text(" + repr(json.dumps(reply)) + ")\n"
            + "print(json.dumps(dict(type='turn.completed')),flush=True)\n")
        executable.chmod(0o700)
        return executable

    def brain(self, folder, executable, **changes):
        options = dict(provider="local", base_url=self.endpoint, model=self.model)
        options.update(changes)
        return converse.CodexBrain(executable, folder, **options)

    def test_local_keeps_real_tools_schema_and_bounded_sandbox_without_hosted_review(self):
        for developer in (False, True):
            command = converse.codex_command("codex", "owned-worktree", "schema.json", "reply.json",
                developer=developer, model=self.model, transport=self.transport())
            self.assertIn("--oss", command)
            self.assertEqual(command[command.index("--local-provider") + 1], "lmstudio")
            self.assertEqual(command[command.index("--model") + 1], self.model)
            self.assertEqual(command[command.index("--sandbox") + 1], "workspace-write" if developer else "read-only")
            settings = self.config(command)
            self.assertEqual(settings["approval_policy"], "never")
            self.assertEqual(settings["approvals_reviewer"], "user")
            self.assertNotIn("--approve-for-me", command)
            self.assertFalse(any("dangerously" in value for value in command))
            self.assertIn("--output-schema", command)
            self.assertIn("--ignore-user-config", command)
            self.assertIn("--ephemeral", command)
            enabled = [command[index + 1] for index, value in enumerate(command[:-1]) if value == "--enable"]
            self.assertEqual(set(enabled), {"shell_tool", "unified_exec"} if developer else set())
            if developer:
                self.assertIs(settings["sandbox_workspace_write"]["network_access"], True)
                self.assertEqual(command[command.index("--add-dir") + 1], str(converse.ROOT.resolve()))

    def test_explicit_responses_gateway_uses_actual_responses_protocol_and_environment_key_name(self):
        selected = converse.model_transport("responses", model="selected-model", base_url="https://gateway.example/v1",
                                            api_key_env="BUDDY_MODEL_KEY")
        command = converse.codex_command("codex", "repo", "schema", "reply", developer=True,
                                         model="selected-model", transport=selected)
        settings = self.config(command)
        provider = settings["model_providers"]["buddy_responses"]
        self.assertEqual(settings["model_provider"], "buddy_responses")
        self.assertEqual(provider["base_url"], "https://gateway.example/v1")
        self.assertEqual(provider["wire_api"], "responses")
        self.assertIs(provider["requires_openai_auth"], False)
        self.assertEqual(provider["env_key"], "BUDDY_MODEL_KEY")
        self.assertNotIn("--oss", command)
        self.assertNotIn("--approve-for-me", command)
        self.assertIsNone(converse.codex_environment(selected))

    def test_invalid_or_ambiguous_transport_cannot_start_default_hosted_model(self):
        invalid = [dict(provider="claude"), dict(provider="codex", base_url=self.endpoint),
            dict(provider="local", model=self.model),
            dict(provider="local", model=self.model, base_url="http://192.168.1.50:18765/v1"),
            dict(provider="local", model=self.model, base_url="https://hosted.example/v1"),
            dict(provider="local", model=self.model, base_url=self.endpoint, api_key_env="KEY"),
            dict(provider="responses", model=self.model, base_url="http://hosted.example/v1"),
            dict(provider="responses", model=self.model, base_url="https://user:secret@example/v1"),
            dict(provider="responses", model=self.model, base_url="https://example/v1?key=secret"),
            dict(provider="responses", model=self.model, base_url="https://example/v1#other"),
            dict(provider="responses", model=self.model, base_url="https://example:99999/v1"),
            dict(provider="responses", model=self.model, base_url="https://example/v1", api_key_env="KEY=secret")]
        for options in invalid:
            with self.subTest(options=options), self.assertRaises(converse.ConversationError):
                converse.model_transport(**options)
        with self.assertRaises(converse.ConversationError):
            converse.codex_command("codex", "repo", "schema", "reply", model=self.model,
                                   transport=dict(provider="unknown"))

    def test_local_child_endpoint_is_exact_and_hosted_environment_does_not_override_it(self):
        with tempfile.TemporaryDirectory() as temporary:
            folder = Path(temporary)
            executable = self.fake(folder)
            brain = self.brain(folder, executable)
            brain.included_usage_only = True  # An old launcher setting cannot trigger hosted account traffic.
            with patch.dict(os.environ, dict(CODEX_OSS_BASE_URL="https://wrong.example/v1",
                    OPENAI_API_KEY="fixture-hosted-key", HTTPS_PROXY="http://wrong.example:8888")), \
                    patch.object(converse, "check_included_usage", side_effect=AssertionError("hosted account call")):
                reply = brain.generate([], {})
            calls = [json.loads(line) for line in (folder / "calls.jsonl").read_text().splitlines()]
            self.assertEqual(len(calls), 1)
            self.assertEqual(calls[0]["endpoint"], self.endpoint)
            self.assertIsNone(calls[0]["proxy"])
            self.assertIsNone(calls[0]["key"])
            self.assertEqual(reply["action"], "NONE")
            self.assertEqual(brain.model_selection()["provider"], "local")
            self.assertFalse(brain.model_selection()["effectiveModelKnown"])

    def test_local_owner_preemption_reaps_same_child_and_preserves_completed_tools_and_unknown_journal(self):
        with tempfile.TemporaryDirectory() as temporary:
            folder = Path(temporary)
            executable = self.fake(folder, tool=True, wait=True)
            brain = self.brain(folder, executable, developer_workspace=converse.ROOT)
            state = dict(development=dict(sessionId="aw-owned", selfGuid="4", ownerGuid="3",
                workspace=str(converse.ROOT), rpcUrl="http://127.0.0.1:8087/rpc", enabled=True,
                directDevelopment=True), character=dict(guid="4"),
                current_request=dict(source="owner", senderGuid="3"))
            journal = folder / "unknown-actions.json"
            original = b'{"attempt":"unknown","doNotReplay":true}\n'
            journal.write_bytes(original)
            self.assertTrue(brain.development_enabled(state))
            with patch.object(converse, "mission_runtime_context", return_value={}):
                started = time.monotonic()
                with self.assertRaises(converse.OwnerInputPending):
                    brain.generate([], state, cancelled=(folder / "waiting").exists)
            self.assertLess(time.monotonic() - started, 4)
            self.assertEqual((folder / "owned.lua").read_text(), "preserved completed edit")
            self.assertEqual(journal.read_bytes(), original)
            self.assertEqual(brain.last_failure_receipt["reason"], "owner_input_waiting")
            self.assertTrue(brain.last_failure_receipt["childReaped"])
            self.assertEqual(brain.last_development_events["completedPatches"], 1)
            self.assertEqual(len((folder / "calls.jsonl").read_text().splitlines()), 1)

    def test_failed_local_process_never_retries_or_falls_back_to_hosted(self):
        with tempfile.TemporaryDirectory() as temporary:
            folder = Path(temporary)
            brain = self.brain(folder, self.fake(folder, fail=True))
            with patch.object(converse, "check_included_usage", side_effect=AssertionError("hosted account call")):
                with self.assertRaises(converse.ConversationError):
                    brain.generate([], {})
            self.assertEqual(len((folder / "calls.jsonl").read_text().splitlines()), 1)
            self.assertEqual(brain.last_failure_receipt["reason"], "process_failed")

    def test_local_reply_is_subject_to_existing_action_schema_and_body_independent_receipt(self):
        with tempfile.TemporaryDirectory() as temporary:
            folder = Path(temporary)
            brain = self.brain(folder, self.fake(folder,
                reply=dict(text="I did it.", emote="NONE", action="CAST_ARBITRARY_PACKET")))
            with self.assertRaisesRegex(converse.ConversationError, "unsupported game action"):
                brain.generate([], {})
            self.assertEqual(brain.last_failure_receipt["reason"], "reply_validation_failed")
            self.assertEqual(len((folder / "calls.jsonl").read_text().splitlines()), 1)

    def test_existing_hosted_selection_and_approval_command_remain_explicit(self):
        command = converse.codex_command("codex", "repo", "schema", "reply", developer=True)
        self.assertIn("--approve-for-me", command)
        self.assertNotIn("--oss", command)
        self.assertNotIn("--model", command)
        self.assertIsNone(converse.codex_environment(dict(provider="codex")))
        with tempfile.TemporaryDirectory() as temporary:
            brain = converse.CodexBrain("codex", temporary)
            self.assertEqual(brain.model_selection()["provider"], "codex")

    def test_listener_settings_bind_local_provider_to_same_brain_and_publish_honest_status(self):
        class OneTurn:
            def __init__(self, rpc, brain, *args, **kwargs):
                self.replies = 0
                self.brain = brain
            def tick(self):
                self.replies += 1
                self.asserted = self.brain.transport["provider"] == "local"
        with tempfile.TemporaryDirectory() as temporary:
            folder = Path(temporary)
            with patch.object(converse, "Conversation", OneTurn), \
                    patch.object(converse, "RPC", return_value=object()), \
                    patch.object(converse, "BuddyControls", return_value=object()), \
                    patch.object(converse, "registered_developer_workspace", return_value=None), \
                    patch.object(converse.signal, "signal"), patch.object(converse.sys, "stdout", io.StringIO()):
                self.assertEqual(converse.main(["run", "--state-dir", str(folder), "--banter-seconds", "0",
                    "--max-replies", "1", "--poll-seconds", "0.2", "--model-provider", "local",
                    "--model", self.model, "--model-base-url", self.endpoint]), 0)
            status = json.loads((folder / "status.json").read_text())
            self.assertEqual(status["phase"], "stopped")
            self.assertEqual(status["modelSelection"]["provider"], "local")
            self.assertEqual(status["modelSelection"]["baseURL"], self.endpoint)
            self.assertEqual(status["requestedModel"], self.model)
            self.assertFalse(status["modelSelection"]["effectiveModelKnown"])


if __name__ == "__main__":
    unittest.main()
