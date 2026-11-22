"""Launcher/config tests with fake children and endpoint replies only."""
import contextlib
import copy
import importlib.machinery
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

loader = importlib.machinery.SourceFileLoader("companions_launcher", str(Path(__file__).with_name("companions")))
spec = importlib.util.spec_from_loader(loader.name, loader)
app = importlib.util.module_from_spec(spec)
loader.exec_module(app)


def settings(root):
    return dict(version=1, default_host="test", hosts={"test": dict(root=str(root), local=True)},
                actors={"mira": dict(name="Mira", guid=4, **{"class": "mage"}, profile="mira", native_port=8087)})


def body(sid="aw-fake"):
    return dict(sessionId=sid, characterGuid="4", catalogRevision="b"*64,
                methods=["buddy-actions.cast_spell", "buddy-actions.cancel_spell"], policyRevision="a"*64, policyHealthy=True)


class FakeChildren:
    def __init__(self, actor, native=False, chat=False):
        self.actor, self.native, self.chat = actor, native, chat
        self.calls, self.spawns = [], []
        self.sid = "aw-fake"
        self.fail_start = False
        self.selection = dict(provider=actor["model"]["provider"], requestedModel=actor["model"]["model"],
                              baseURL=actor["model"].get("base_url"), localProvider=actor["model"]["local_provider"])

    def run(self, argv, actor, env=None):
        self.calls.append((list(argv), env))
        if argv[-1] == "--help":
            return " ".join(app.PROVIDER_FLAGS)
        kind, command = Path(argv[2]).name, argv[3]
        if kind == "buddy":
            if command == "start":
                if self.fail_start:
                    raise app.Error("Startup unconfirmed; native may still exist")
                self.native = True
            elif command == "stop":
                self.native = False
            return "Running: " + ("yes" if self.native else "no") + "\nReadiness: " + ("matching ready event observed" if self.native else "not observed") + "\n"
        if command == "stop":
            self.chat = False
            return "Conversation stop requested"
        return json.dumps(dict(running=self.chat, sessionId=self.sid, phase="listening", modelSelection=self.selection,
            character_identity=dict(profile=actor["profile"], name=actor["name"], **{"class": actor["class"]},
                                    guid=actor["guid"], owner_guid=3, rpc_url=f"http://127.0.0.1:{actor['native_port']}/rpc")))

    def spawn(self, argv, actor, log):
        self.spawns.append(list(argv))
        self.chat = True
        self.selection = dict(provider=actor["model"]["provider"], requestedModel=actor["model"]["model"],
                              baseURL=actor["model"].get("base_url"), localProvider=actor["model"]["local_provider"])
        return 12345

    def commands(self, kind, command):
        return [row for row, _ in self.calls if len(row)>3 and Path(row[2]).name==kind and row[3]==command]


class LauncherTests(unittest.TestCase):
    def setUp(self):
        folder = tempfile.TemporaryDirectory()
        self.addCleanup(folder.cleanup)
        self.root = Path(folder.name)
        self.raw = settings(self.root)
        self.actor = app.normalize(self.raw)[0]
        self.runner = FakeChildren(self.actor)
        self.identity = mock.patch.object(app, "native_identity", return_value=body())
        self.identity.start(); self.addCleanup(self.identity.stop)
        self.discover = mock.patch.object(app, "models", return_value=dict(advertised=True, inferenceVerified=False))
        self.discover.start(); self.addCleanup(self.discover.stop)

    def test_default_local_plan_and_no_reasoning_override(self):
        model = self.actor["model"]
        self.assertEqual((model["provider"], model["base_url"], model["model"]),
                         ("local", "http://127.0.0.1:18765/v1", app.LOCAL_MODEL))
        planned = app.dry_plan(self.actor, "start")
        self.assertNotIn("--reasoning-effort", planned["listener_run_if_unowned"])
        self.assertNotIn("--resume-whispers", planned["listener_run_if_unowned"])
        self.assertEqual(planned["native_start_if_stopped"][3], "start")
        command = planned["listener_run_if_unowned"]
        self.assertEqual(command[command.index("--banter-seconds") + 1], "0")

    def test_banter_is_explicit_and_validated_before_child_changes(self):
        self.raw["actors"]["mira"]["banter_seconds"] = 240
        command = app.commands(app.normalize(self.raw)[0])["run"]
        self.assertEqual(command[command.index("--banter-seconds") + 1], "240")
        for value in (True, -1, 1, 119, 3601, float("nan"), float("inf")):
            self.raw["actors"]["mira"]["banter_seconds"] = value
            with self.subTest(value=value), self.assertRaisesRegex(app.Error, "banter_seconds"):
                app.normalize(self.raw)
        self.assertFalse(self.runner.calls or self.runner.spawns)

    def test_transport_validation_precedes_models_or_any_child(self):
        invalid = [dict(provider="responses", base_url=url, model="claude", api_key_env="TEST_KEY")
                   for url in ("http://gateway.example/v1", "http://127.0.0.2/v1", "http://127.0.0.1:0/v1",
                               "http://127.0.0.1:65536/v1", "https://gateway.example/with space", "https://:bad/v1")]
        invalid += [dict(model="two words"), dict(model="a" * 129), dict(model="/bad"),
                    dict(base_url="http://127.0.0.2/v1"), dict(api_key_env="TEST_KEY")]
        with mock.patch.object(app, "get_json", side_effect=AssertionError("no endpoint request")), \
             mock.patch.object(app.subprocess, "run", side_effect=AssertionError("no child")):
            for model in invalid:
                raw = copy.deepcopy(self.raw)
                raw["model"] = model
                with self.subTest(model=model), self.assertRaises(app.Error):
                    app.normalize(raw)
        self.assertFalse(self.runner.calls or self.runner.spawns)

    def test_supported_loopback_and_https_gateways_keep_exact_transport(self):
        for url in ("http://127.0.0.1:18765/v1", "http://localhost:18765/v1", "http://[::1]:18765/v1"):
            with self.subTest(url=url):
                self.assertEqual(app.model_settings({}, dict(base_url=url))["base_url"], url)
        model = app.model_settings({}, dict(provider="gemini", base_url="https://gateway.example/v1",
                                           model="gemini-route", api_key_env="TEST_KEY"))
        self.assertEqual(model["provider"], "responses")
        self.assertEqual(model["base_url"], "https://gateway.example/v1")

    def test_start_once_then_repeated_start_reuses_both_owners(self):
        first = app.operate(self.actor, "start", self.runner)
        second = app.operate(self.actor, "start", self.runner)
        self.assertTrue(first["nativeReady"] and second["listenerReady"])
        self.assertFalse(first["inferenceVerified"])
        self.assertEqual(len(self.runner.commands("buddy", "start")), 1)
        self.assertEqual(len(self.runner.spawns), 1)
        self.assertFalse(self.runner.commands("buddy", "stop"))

    def test_model_change_is_explicit_chat_restart_without_native_reconnect(self):
        app.operate(self.actor, "start", self.runner)
        changed = copy.deepcopy(self.actor)
        changed["model"]["model"] = "another-loaded-local-model"
        with self.assertRaisesRegex(app.Error, "restart-chat"):
            app.operate(changed, "start", self.runner)
        previous = len(self.runner.commands("buddy", "start"))
        result = app.operate(changed, "restart-chat", self.runner)
        self.assertEqual(result["body"]["sessionId"], "aw-fake")
        self.assertEqual(len(self.runner.commands("buddy", "start")), previous)
        self.assertFalse(self.runner.commands("buddy", "stop"))
        self.assertEqual(len(self.runner.spawns), 2)
        self.assertEqual(len(self.runner.commands("converse", "stop")), 1)

    def test_native_start_unknown_is_not_replayed_and_old_journals_remain(self):
        self.runner.fail_start = True
        with self.assertRaisesRegex(app.Error, "unconfirmed"):
            app.operate(self.actor, "start", self.runner)
        self.runner.fail_start = False
        with self.assertRaisesRegex(app.Error, "unresolved"):
            app.operate(self.actor, "start", self.runner)
        self.assertEqual(len(self.runner.commands("buddy", "start")), 1)
        record = json.loads((self.root / "data/companions/4/owner.json").read_text())
        self.assertEqual(record["phase"], "native_starting")

    def test_stop_requests_owned_chat_first_then_managed_native_stop(self):
        app.operate(self.actor, "start", self.runner)
        result = app.operate(self.actor, "stop", self.runner)
        self.assertTrue(result["stopped"])
        commands = [(Path(row[2]).name, row[3]) for row, _ in self.runner.calls if len(row)>3 and row[-1]!="--help"]
        self.assertLess(commands.index(("converse", "stop")), commands.index(("buddy", "stop")))
        self.assertEqual(len(self.runner.commands("buddy", "stop")), 1)

    def test_stop_failure_keeps_native_and_preserves_operation_record(self):
        app.operate(self.actor, "start", self.runner)
        original = self.runner.run
        def refusing(argv, actor, env=None):
            if Path(argv[2]).name == "converse" and argv[3] == "stop":
                raise app.Error("owned listener stop unavailable")
            return original(argv, actor, env)
        self.runner.run = refusing
        with self.assertRaisesRegex(app.Error, "unavailable"):
            app.operate(self.actor, "stop", self.runner)
        self.assertTrue(self.runner.native)
        self.assertFalse(self.runner.commands("buddy", "stop"))

    def test_gateway_missing_key_or_model_does_not_stop_running_chat(self):
        changed = copy.deepcopy(self.actor)
        changed["model"] = app.model_settings({}, dict(provider="claude", base_url="http://127.0.0.1:4000/v1",
                                                        model="claude", api_key_env="MISSING_TEST_GATEWAY_KEY"))
        self.runner.native = self.runner.chat = True
        self.discover.stop()
        with mock.patch.dict(os.environ, {}, clear=True), self.assertRaisesRegex(app.Error, "MISSING_TEST_GATEWAY_KEY"):
            app.operate(changed, "restart-chat", self.runner)
        self.assertFalse(self.runner.commands("converse", "stop"))
        self.assertFalse(self.runner.spawns)

    def test_unknown_listener_start_is_not_replayed_by_start_or_restart_chat(self):
        self.runner.native = True
        def stalled_spawn(argv, actor, log):
            self.runner.spawns.append(argv)
            return 12345
        self.runner.spawn = stalled_spawn
        with mock.patch.object(app, "wait_chat", side_effect=app.Error("unconfirmed")), self.assertRaisesRegex(app.Error, "unconfirmed"):
            app.operate(self.actor, "start", self.runner)
        for command in ("start", "restart-chat"):
            with mock.patch.object(app, "child_exited", return_value=False), self.assertRaisesRegex(app.Error, "unresolved"):
                app.operate(self.actor, command, self.runner)
        self.assertEqual(len(self.runner.spawns), 1)
        self.assertFalse(self.runner.commands("buddy", "start") or self.runner.commands("buddy", "stop"))

    def test_listener_session_change_during_chat_stop_never_reconnects_native(self):
        self.runner.native = self.runner.chat = True
        self.identity.stop()
        with mock.patch.object(app, "native_identity", side_effect=[body(), body("replacement-session")]), \
             self.assertRaisesRegex(app.Error, "session changed"):
            app.operate(self.actor, "restart-chat", self.runner)
        self.assertFalse(self.runner.spawns)
        self.assertFalse(self.runner.commands("buddy", "start") or self.runner.commands("buddy", "stop"))

    def test_public_identity_reads_bind_body_session_contract_and_policy(self):
        self.identity.stop()
        methods = [dict(name=value) for value in body()["methods"]]
        catalog = dict(sessionId="aw-fake", characterGuid="4", revision="b"*64, methods=methods)
        values = {"session.describe": catalog, "buddy-world.snapshot": dict(self=dict(guid="4", present=True)),
                  "buddy-party.state": dict(sessionId="aw-fake"),
                  "session.attach": dict(catalog, policy=dict(revision="a"*64))}
        observed = []
        def reply(url, data=None, key_env=None):
            observed.append((data["method"], data["params"]))
            return dict(jsonrpc="2.0", id=1, result=copy.deepcopy(values[data["method"]]))
        with mock.patch.object(app, "get_json", side_effect=reply):
            result = app.native_identity(self.actor)
            self.assertEqual(result["sessionId"], "aw-fake")
            self.assertEqual(observed[-1], ("session.attach", dict(sessionId="aw-fake", characterGuid="4")))
            values["buddy-party.state"]["sessionId"] = "foreign"
            with self.assertRaisesRegex(app.Error, "identity changed"):
                app.native_identity(self.actor)
            values["buddy-party.state"]["sessionId"] = "aw-fake"
            values["session.describe"]["methods"] = methods[:1]
            with self.assertRaisesRegex(app.Error, "missing required"):
                app.native_identity(self.actor)

    def test_busy_real_body_lock_and_foreign_journal_never_spawn(self):
        with app.journal(self.actor):
            with self.assertRaisesRegex(app.Error, "owns this body"):
                app.operate(self.actor, "start", self.runner)
        with app.journal(self.actor) as (_, save, _):
            save(phase="running")
        changed = copy.deepcopy(self.actor)
        changed["state_dir"] += "-other"
        with self.assertRaisesRegex(app.Error, "different settings"):
            app.operate(changed, "start", self.runner)
        self.assertFalse(self.runner.calls or self.runner.spawns)

    def test_invalid_or_secret_settings_fail_closed(self):
        for override in (dict(provider="unknown"), dict(provider="claude", model="claude"),
                         dict(provider="responses", model="route", api_key_env="KEY"),
                         dict(api_key="DO_NOT_PRINT_SECRET"), dict(base_url="http://example.com/v1")):
            with self.subTest(override=override), self.assertRaises(app.Error) as error:
                app.model_settings({}, override)
            self.assertNotIn("DO_NOT_PRINT_SECRET", str(error.exception))

    def test_duplicate_body_port_profile_or_state_never_launches(self):
        for key, value in (("guid", 4), ("native_port", 8087), ("native_profile", "mira"),
                           ("state_dir", "data/converse/mira")):
            raw = copy.deepcopy(self.raw)
            peer = dict(name="Rook", guid=5, **{"class": "rogue"}, profile="rook", native_port=8088)
            peer[key] = value
            raw["actors"]["rook"] = peer
            with self.subTest(key=key), self.assertRaisesRegex(app.Error, "one owner"):
                app.normalize(raw)

    def test_dry_run_is_pure_and_accepts_json_settings(self):
        source = self.root / "settings.json"
        source.write_text(json.dumps(self.raw))
        before = sorted(str(p.relative_to(self.root)) for p in self.root.rglob("*"))
        with mock.patch.object(app.subprocess, "run", side_effect=AssertionError("no child")), \
             mock.patch.object(app, "get_json", side_effect=AssertionError("no endpoint")), \
             mock.patch.object(app, "journal", side_effect=AssertionError("no files")), contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(app.main(["start", "--settings", str(source), "--dry-run"]), 0)
        planned = json.loads(output.getvalue())
        self.assertFalse(planned[0]["network"] or planned[0]["mutation"])
        self.assertEqual(before, sorted(str(p.relative_to(self.root)) for p in self.root.rglob("*")))

    def test_example_toml_targets_four_distinct_bodies_on_laniakea(self):
        source = Path(__file__).resolve().parent.parent / "config/companions.example.toml"
        actors = app.normalize(app.tomllib.loads(source.read_text()))
        self.assertEqual([row["guid"] for row in actors], [1, 4, 5, 8])
        self.assertEqual(actors[-1]["native_profile"], "alliance-nixwick")
        self.assertTrue(all(row["model"]["provider"] == "local" for row in actors))
        self.assertTrue(all(row["native_env"]["AGENT_WOW_AUTHSERVER_HOST"] == "10.247.37.1" for row in actors))


if __name__ == "__main__":
    unittest.main()
