#!/usr/bin/env python3
"""Offline receipts and isolation checks for the bounded Horde roster."""

from collections import Counter
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock


spec = importlib.util.spec_from_file_location("thrall_roster", Path(__file__).with_name("roster.py"))
roster = importlib.util.module_from_spec(spec)
spec.loader.exec_module(roster)


def state_copy():
    # The immutable plan is shared intentionally; a malformed-state fixture must
    # never mutate that module-level identity while testing its validation.
    return copy.deepcopy(roster.new_state())


def character(row, guid=None):
    p = row["plan"]
    return {"guid": guid or str(1000 + p["slot"]), "name": p["name"],
            "race": p["race"], "class": p["class"], "gender": p["gender"], "level": 1}


def listing(*characters):
    return {"realm": {"id": 1, "name": "Private test realm"}, "characters": list(characters)}


class FakeAdapter:
    """Every adapter operation is an in-memory receipt, never a live client."""

    def __init__(self, saved):
        self.saved, self.calls = saved, []
        self.account_result, self.account_error = "created", False
        self.login_exit, self.status_exit = 0, 0
        self.create_exit, self.lost_create_response, self.publish_created = 0, False, True
        self.listings = {}

    def record(self, method, row):
        durable = copy.deepcopy(self.saved[-1]["rows"][row["plan"]["slot"] - 1]) if self.saved else None
        self.calls.append({"method": method, "slot": row["plan"]["slot"], "durable": durable})

    def guard(self, row):
        self.record("guard", row)

    def prepare(self, row):
        self.record("prepare", row)

    def create_account(self, row):
        self.record("create_account", row)
        if self.account_error:
            raise OSError("simulated lost account response")
        return self.account_result

    def login(self, row):
        self.record("login", row)
        return {"exit": self.login_exit, "stdout": "", "stderr": ""}

    def auth_status(self, row):
        self.record("auth_status", row)
        return {"exit": self.status_exit, "stdout": "", "stderr": ""}

    def characters(self, row):
        self.record("characters", row)
        return copy.deepcopy(self.listings.get(row["plan"]["slot"], listing()))

    def create_character(self, row):
        self.record("create_character", row)
        if self.publish_created:
            self.listings[row["plan"]["slot"]] = listing(character(row))
        if self.lost_create_response:
            raise OSError("simulated lost character response")
        return {"exit": self.create_exit, "stdout": "", "stderr": ""}

    def profile(self, row):
        self.record("profile", row)

    def methods(self):
        return [call["method"] for call in self.calls]

    def network_methods(self):
        return [m for m in self.methods() if m in
                ("create_account", "login", "auth_status", "characters", "create_character")]


class FakeConsole:
    def __init__(self, chunks):
        self.chunks, self.sent, self.address, self.timeouts = list(chunks), [], None, []

    def __enter__(self):
        return self

    def __exit__(self, *args):
        return False

    def settimeout(self, value):
        self.timeouts.append(value)

    def connect(self, address):
        self.address = address

    def sendall(self, body):
        self.sent.append(body)

    def recv(self, maximum):
        value = self.chunks.pop(0) if self.chunks else b""
        if isinstance(value, Exception):
            raise value
        if len(value) > maximum:
            self.chunks.insert(0, value[maximum:])
        return value[:maximum]


class RosterTests(unittest.TestCase):
    def setUp(self):
        self.state, self.saved = state_copy(), []
        self.adapter = FakeAdapter(self.saved)
        self.worker = roster.Roster(self.state, self.adapter, lambda value: self.saved.append(copy.deepcopy(value)))
        self.row = self.state["rows"][1]

    def recover(self):
        self.state = copy.deepcopy(self.saved[-1])
        self.worker = roster.Roster(self.state, self.adapter, lambda value: self.saved.append(copy.deepcopy(value)))
        self.row = self.state["rows"][1]
        self.adapter.calls = []

    def saved_session(self, phase, character_phase="planned"):
        self.row.update(accountPhase="created", authPhase=phase, characterPhase=character_phase,
                        receipts=[{"at": 1, "kind": "account-bootstrap",
                                   "value": {"account": self.row["plan"]["account"], "classification": "created"}}])
        self.worker = roster.Roster(self.state, self.adapter, lambda value: self.saved.append(copy.deepcopy(value)))

    def test_plan_has_exactly_forty_unique_supported_intentions(self):
        self.assertEqual(len(roster.PLAN), 40)
        self.assertEqual([p["slot"] for p in roster.PLAN], list(range(1, 41)))
        for field in ("account", "name", "profile", "rpcPort"):
            with self.subTest(field=field):
                self.assertEqual(len({p[field] for p in roster.PLAN}), 40)
        self.assertEqual(Counter(p["class"] for p in roster.PLAN), Counter(roster.CLASSES))
        self.assertEqual(Counter(p["intendedRole"] for p in roster.PLAN)["tank"], 6)
        self.assertEqual(Counter(p["intendedRole"] for p in roster.PLAN)["healer"], 8)
        profession_pairs = set()
        for p in roster.PLAN:
            self.assertIn(p["race"], roster.RACES[p["class"]])
            self.assertIn(p["gender"], ("female", "male"))
            self.assertTrue(p["name"].isalpha())
            self.assertLessEqual(len(p["name"]), 12)
            profession_pairs.add(tuple(p["primaryProfessionsPlanned"]))
            self.assertEqual(p["secondaryProfessionsPlanned"], ["cooking", "fishing", "first-aid"])
            self.assertIs(p["professionLearningConfirmed"], False)
            self.assertEqual(p["levelStageCap"], 60)
            self.assertIs(p["runtimeLaunchAllowed"], False)
        self.assertEqual(profession_pairs, set(roster.PROFESSIONS))
        anchor = roster.PLAN[0]
        self.assertEqual({k: anchor[k] for k in ("account", "name", "race", "class", "profile", "rpcPort")},
                         {"account": "WCHORDE01", "name": "Gravemint", "race": "undead", "class": "mage",
                          "profile": "horde-thrall", "rpcPort": 8191})
        self.assertIs(self.state["deathKnightsDeferred"]["countedInCurrent40"], False)
        self.assertIs(self.state["deathKnightsDeferred"]["creationAllowed"], False)

    def test_protected_anchor_never_calls_adapter(self):
        before = copy.deepcopy(self.state["rows"][0])
        self.worker.step(self.state["rows"][0])
        self.assertEqual(self.adapter.calls, [])
        self.assertEqual(self.saved, [])
        self.assertEqual(self.state["rows"][0], before)
        with self.assertRaises(roster.Error):
            roster.RealmAdapter("/unused").paths(self.state["rows"][0])

    def test_batch_rejects_unbounded_or_noninteger_values_without_adapter_calls(self):
        for batch in (0, 6, -1, 100, True, False, 1.0, "1", None):
            with self.subTest(batch=batch), self.assertRaises(roster.Error):
                self.worker.apply(batch)
        self.assertEqual(self.adapter.calls, [])

    def test_batch_creates_only_requested_distinct_slots(self):
        result = self.worker.apply(2)
        self.assertEqual([c["slot"] for c in self.adapter.calls if c["method"] == "create_account"], [2, 3])
        self.assertEqual([c["slot"] for c in self.adapter.calls if c["method"] == "create_character"], [2, 3])
        self.assertNotIn(1, [c["slot"] for c in self.adapter.calls])
        self.assertEqual(result["actualConfirmedCharacters"], 3)
        self.assertEqual(result["newAccountsCreated"], 2)
        self.assertEqual(result["professionsTrainedByTool"], 0)
        self.assertEqual(result["runtimesStartedByTool"], 0)

    def test_all_forty_identities_can_be_receipt_confirmed_offline_in_bounded_batches(self):
        for _ in range(8):
            result = self.worker.apply(5)
        self.assertEqual(result["actualConfirmedCharacters"], 40)
        self.assertEqual(result["newAccountsCreated"], 39)
        self.assertEqual(result["classCountsConfirmed"], roster.CLASSES)
        self.assertEqual(len(result["newCharacterGuids"]), len(set(result["newCharacterGuids"])))
        self.assertEqual(result["unresolvedSlots"], [])
        self.assertNotIn(1, [c["slot"] for c in self.adapter.calls])
        for method in ("create_account", "login", "create_character"):
            self.assertEqual(len([c for c in self.adapter.calls if c["method"] == method]), 39)
        self.assertIs(roster.validate_state(self.state), self.state)

    def test_new_states_do_not_share_mutable_plan_or_progress(self):
        first, other = roster.new_state(), roster.new_state()
        original = copy.deepcopy(roster.PLAN[1]["primaryProfessionsPlanned"])
        try:
            first["rows"][1]["plan"]["primaryProfessionsPlanned"].append("unplanned")
            first["rows"][1]["receipts"].append({"local": "fixture"})
            self.assertNotIn("unplanned", other["rows"][1]["plan"]["primaryProfessionsPlanned"])
            self.assertNotIn("unplanned", roster.PLAN[1]["primaryProfessionsPlanned"])
            self.assertEqual(other["rows"][1]["receipts"], [])
        finally:
            # Keep a failing regression from poisoning the remaining fixtures.
            roster.PLAN[1]["primaryProfessionsPlanned"] = original

    def test_each_creation_and_authentication_has_a_saved_intent_first(self):
        self.worker.step(self.row)
        for method, field in (("create_account", "accountPhase"), ("login", "authPhase"),
                              ("create_character", "characterPhase")):
            calls = [c for c in self.adapter.calls if c["method"] == method]
            self.assertEqual(len(calls), 1)
            self.assertEqual(calls[0]["durable"][field], "intent")
        self.assertEqual(self.row["characterPhase"], "confirmed")
        self.assertTrue(self.row["profilesWritten"])
        self.assertEqual(self.row["actualCharacter"], character(self.row))
        self.assertEqual(self.row["actualRealm"]["id"], 1)

    def test_account_collision_or_unknown_never_authenticates_or_retries(self):
        for outcome in ("collision", "unknown", "unexpected", "exception"):
            with self.subTest(outcome=outcome):
                self.setUp()
                self.adapter.account_result = outcome
                self.adapter.account_error = outcome == "exception"
                with self.assertRaises(roster.Error):
                    self.worker.step(self.row)
                self.assertEqual(self.adapter.network_methods(), ["create_account"])
                self.assertIn(self.row["accountPhase"], ("collision", "unknown"))
                self.recover()
                with self.assertRaises(roster.Error):
                    self.worker.step(self.row)
                self.assertEqual(self.adapter.network_methods(), [])

    def test_saved_account_intent_cannot_be_retried_or_adopted(self):
        self.row["accountPhase"] = "intent"
        with self.assertRaises(roster.Error):
            self.worker.step(self.row)
        self.assertEqual(self.adapter.network_methods(), [])
        self.assertNotIn("prepare", self.adapter.methods())

    def test_lost_character_response_reconciles_exact_enumerated_receipt(self):
        self.adapter.lost_create_response = True
        self.worker.step(self.row)
        self.assertEqual(self.row["characterPhase"], "confirmed")
        self.assertEqual(self.row["guid"], character(self.row)["guid"])
        self.assertEqual(self.adapter.methods().count("create_character"), 1)
        self.assertEqual(self.adapter.methods().count("characters"), 2)
        self.recover()
        self.worker.step(self.row)
        self.assertEqual(self.adapter.calls, [])

    def test_absent_character_after_lost_response_is_not_recreated(self):
        self.adapter.lost_create_response, self.adapter.publish_created = True, False
        with self.assertRaises(roster.Error):
            self.worker.step(self.row)
        self.assertEqual(self.row["characterPhase"], "unknown")
        self.assertEqual(self.adapter.methods().count("create_character"), 1)
        self.recover()
        with self.assertRaises(roster.Error):
            self.worker.step(self.row)
        self.assertEqual(self.adapter.network_methods(), ["auth_status", "characters"])
        self.assertEqual(self.row["characterPhase"], "unknown")

    def test_rejected_character_is_not_recreated(self):
        self.adapter.create_exit, self.adapter.publish_created = 1, False
        with self.assertRaises(roster.Error):
            self.worker.step(self.row)
        self.assertEqual(self.row["characterPhase"], "rejected")
        self.recover()
        with self.assertRaises(roster.Error):
            self.worker.step(self.row)
        self.assertEqual(self.adapter.network_methods(), ["auth_status", "characters"])

    def test_saved_authentication_uses_status_and_never_fresh_login(self):
        for phase in ("intent", "unknown", "confirmed"):
            with self.subTest(phase=phase):
                self.setUp()
                self.saved_session(phase)
                self.adapter.listings[2] = listing(character(self.row))
                self.worker.step(self.row)
                self.assertEqual(self.adapter.network_methods(), ["auth_status", "characters"])
                self.assertEqual(self.row["characterPhase"], "confirmed")

    def test_unconfirmed_saved_session_does_not_enumerate_or_login(self):
        self.saved_session("unknown")
        self.adapter.status_exit = 1
        with self.assertRaises(roster.Error):
            self.worker.step(self.row)
        self.assertEqual(self.adapter.network_methods(), ["auth_status"])

    def test_confirmed_row_has_zero_adapter_calls(self):
        self.worker.step(self.row)
        self.recover()
        self.worker.step(self.row)
        self.assertEqual(self.adapter.calls, [])

    def test_confirmed_row_can_finish_profile_without_network(self):
        self.worker.step(self.row)
        self.recover()
        self.row["profilesWritten"] = False
        self.worker.step(self.row)
        self.assertEqual(self.adapter.methods(), ["profile"])
        self.assertTrue(self.row["profilesWritten"])

    def test_mismatched_or_extra_character_stops_before_create(self):
        for field, value in (("name", "Othername"), ("race", "human"), ("class", "warrior"),
                             ("gender", "other"), ("guid", "0"), ("guid", 1002), ("extra", None)):
            with self.subTest(field=field, value=value):
                self.setUp()
                c = character(self.row)
                if field == "extra":
                    received = listing(c, dict(c, guid="9999", name="Othername"))
                else:
                    c[field] = value
                    received = listing(c)
                self.adapter.listings[2] = received
                with self.assertRaises(roster.Error):
                    self.worker.step(self.row)
                self.assertEqual(self.row["characterPhase"], "conflict")
                self.assertNotIn("create_character", self.adapter.methods())
                self.assertNotIn("profile", self.adapter.methods())

    def test_enumerated_guid_cannot_duplicate_protected_anchor(self):
        self.adapter.listings[2] = listing(character(self.row, "6"))
        with self.assertRaises(roster.Error):
            self.worker.step(self.row)
        self.assertNotIn("create_character", self.adapter.methods())
        self.assertNotIn("profile", self.adapter.methods())
        self.assertIsNone(self.row["guid"])

    def test_malformed_character_listings_fail_closed(self):
        for received in (None, [], {}, {"realm": None, "characters": []},
                         {"realm": {"id": 2}, "characters": []},
                         {"realm": {"id": True}, "characters": []},
                         {"realm": {"id": 1}, "characters": None},
                         {"realm": {"id": 1}, "characters": [None]},
                         listing(character(self.row, "01002"))):
            with self.subTest(received=received):
                self.setUp()
                self.saved_session("confirmed")
                with self.assertRaises(roster.Error):
                    self.worker.reconcile(self.row, received)
                self.assertEqual(self.adapter.calls, [])

    def test_confirmed_saved_evidence_must_match_identity_realm_and_unique_guid(self):
        self.worker.apply(2)
        valid = copy.deepcopy(self.state)
        mutations = [
            lambda s: s["rows"][1].pop("actualCharacter"),
            lambda s: s["rows"][1].pop("actualRealm"),
            lambda s: s["rows"][1]["actualCharacter"].update(name="Othername"),
            lambda s: s["rows"][1]["actualCharacter"].update(race="human"),
            lambda s: s["rows"][1]["actualCharacter"].update(guid="9999"),
            lambda s: s["rows"][1]["actualRealm"].update(id=True),
            lambda s: s["rows"][1]["actualRealm"].update(id=2),
            lambda s: s["rows"][2].update(guid=s["rows"][1]["guid"],
                                          actualCharacter={**s["rows"][2]["actualCharacter"], "guid": s["rows"][1]["guid"]}),
        ]
        self.adapter.calls = []
        for index, mutate in enumerate(mutations):
            value = copy.deepcopy(valid)
            mutate(value)
            with self.subTest(case=index), self.assertRaises(roster.Error):
                roster.Roster(value, self.adapter, lambda _: None)
        self.assertEqual(self.adapter.calls, [])

    def test_malformed_state_is_rejected_before_any_adapter_call(self):
        invalid = [None, [], {}, {**state_copy(), "rows": None}, {**state_copy(), "rows": {}},
                   {**state_copy(), "schemaVersion": True}, {**state_copy(), "planHash": "altered"}]
        mutations = [
            lambda s: s["rows"].pop(),
            lambda s: s["rows"].append(copy.deepcopy(s["rows"][-1])),
            lambda s: s["rows"].__setitem__(1, None),
            lambda s: s["rows"][1]["plan"].update(race="human"),
            lambda s: s["rows"][1].update(accountPhase="not-a-phase"),
            lambda s: s["rows"][1].update(accountPhase=[]),
            lambda s: s["rows"][1].update(authPhase="not-a-phase"),
            lambda s: s["rows"][1].update(characterPhase="not-a-phase"),
            lambda s: s["rows"][1].update(characterPhase="confirmed", guid=None),
            lambda s: s["rows"][1].update(guid="6"),
            lambda s: s["rows"][1].update(guid=1002),
            lambda s: s["rows"][1].update(guid="0"),
            lambda s: s["rows"][1].update(guid="06"),
            lambda s: s["rows"][1].update(guid=str(2**48)),
            lambda s: s["rows"][1].update(receipts=None),
            lambda s: s["rows"][1].update(profilesWritten="yes"),
            lambda s: s["rows"][0].update(guid="7"),
            lambda s: s["rows"][0].update(accountPhase="planned"),
            lambda s: s["rows"][0].update(authPhase="planned"),
            lambda s: s["rows"][0].update(profilesWritten=False),
            lambda s: s["deathKnightsDeferred"].update(creationAllowed=True),
        ]
        for mutate in mutations:
            value = state_copy()
            mutate(value)
            invalid.append(value)
        for index, value in enumerate(invalid):
            with self.subTest(case=index), self.assertRaises(roster.Error):
                roster.Roster(value, self.adapter, lambda _: None)
        self.assertEqual(self.adapter.calls, [])


class PrivateAdapterTests(unittest.TestCase):
    def setUp(self):
        base = "/private/tmp" if Path("/private/tmp").is_dir() else None
        self.temporary = tempfile.TemporaryDirectory(dir=base)
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.root.chmod(0o700)
        self.row = state_copy()["rows"][1]
        self.adapter = roster.RealmAdapter(self.root)

    def test_private_json_permissions_and_roundtrip(self):
        path = self.root / "private" / "state.json"
        roster.write_private(path, {"only": "test receipt"})
        self.assertEqual(stat.S_IMODE(path.parent.stat().st_mode), 0o700)
        self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
        self.assertEqual(roster.read_private(path), {"only": "test receipt"})
        path.chmod(0o644)
        with self.assertRaises(roster.Error):
            roster.read_private(path)

    def test_private_files_and_directories_reject_symlinks(self):
        target = self.root / "target.json"
        roster.write_private(target, {"preserved": True})
        alias = self.root / "alias.json"
        alias.symlink_to(target)
        with self.assertRaises((roster.Error, OSError)):
            roster.read_private(alias)
        with self.assertRaises(roster.Error):
            roster.write_private(alias, {"preserved": False})
        self.assertEqual(roster.read_private(target), {"preserved": True})
        private = self.root / "real-directory"
        private.mkdir(mode=0o700)
        linked = self.root / "linked-directory"
        linked.symlink_to(private, target_is_directory=True)
        with self.assertRaises(roster.Error):
            roster.write_private(linked / "state.json", {})
        self.assertFalse((private / "state.json").exists())

    def test_nonprivate_directory_is_rejected(self):
        path = self.root / "public"
        path.mkdir(mode=0o755)
        path.chmod(0o755)
        with self.assertRaises(roster.Error):
            roster.private_directory(path)

    def test_prepare_creates_private_separate_credentials_and_stable_profile(self):
        with mock.patch.object(self.adapter, "guard"):
            self.adapter.prepare(self.row)
            config, data = self.adapter.paths(self.row)
            credentials = roster.read_private(config / "auth.json")
            self.assertEqual(credentials["username"], "WCHORDE02")
            self.assertRegex(credentials["password"], r"^[A-Za-z0-9]{16}$")
            self.assertEqual(stat.S_IMODE(config.stat().st_mode), 0o700)
            self.assertEqual(stat.S_IMODE(data.stat().st_mode), 0o700)
            self.assertEqual(stat.S_IMODE((config / "client.yaml").stat().st_mode), 0o600)
            body = (config / "client.yaml").read_text()
            self.assertIn("port: 8302", body)
            self.assertIn("config_dir: " + json.dumps(str(config)), body)
            self.assertNotIn(credentials["password"], body)
            self.adapter.prepare(self.row)
            self.assertEqual(roster.read_private(config / "auth.json"), credentials)
            roster.write_private(config / "auth.json", {"username": "OTHER", "password": credentials["password"]})
            with self.assertRaises(roster.Error):
                self.adapter.prepare(self.row)

    def test_missing_credentials_after_intent_are_never_replaced(self):
        for phase in ("intent", "created", "unknown", "collision"):
            with self.subTest(phase=phase), mock.patch.object(self.adapter, "guard"):
                self.row["accountPhase"] = phase
                with self.assertRaises(roster.Error):
                    self.adapter.prepare(self.row)
                config, _ = self.adapter.paths(self.row)
                self.assertFalse((config / "auth.json").exists())

    def test_malformed_private_credentials_are_never_used_or_replaced(self):
        config, _ = self.adapter.paths(self.row)
        for credentials in (None, [], {}, {"username": "WCHORDE02", "password": "short"},
                            {"username": "WCHORDE01", "password": "a" * 16},
                            {"username": "WCHORDE02", "password": "a" * 16, "extra": True}):
            with self.subTest(credentials=credentials), mock.patch.object(self.adapter, "guard"):
                roster.write_private(config / "auth.json", credentials)
                with self.assertRaises(roster.Error):
                    self.adapter.prepare(self.row)
                self.assertEqual(roster.read_private(config / "auth.json"), credentials)

    def test_cli_removes_inherited_client_overrides_and_uses_own_config(self):
        result = SimpleNamespace(returncode=0, stdout="{}", stderr="")
        with mock.patch.object(self.adapter, "guard"), \
                mock.patch.dict(os.environ, {"AGENT_WOW_CONFIG_DIR": "foreign", "AGENT_WOW_PASSWORD": "private-test-value",
                                             "THRALL_TEST_PRESERVE": "yes"}), \
                mock.patch.object(roster.subprocess, "run", return_value=result) as run:
            self.adapter.login(self.row)
        args, kwargs = run.call_args
        command = args[0]
        config, _ = self.adapter.paths(self.row)
        self.assertEqual(command[:3], [str(self.root / "bin/agent-wow"), "--config", str(config / "client.yaml")])
        self.assertEqual(command[3:], ["auth", "login", "--timeout", "15s"])
        self.assertFalse(any(k.startswith("AGENT_WOW_") for k in kwargs["env"]))
        self.assertEqual(kwargs["env"]["THRALL_TEST_PRESERVE"], "yes")
        self.assertNotIn("private-test-value", repr(command))
        self.assertNotIn("input", kwargs)

    def test_live_tracked_pid_blocks_cli_before_subprocess(self):
        pid_path = self.root / "config/buddies" / self.row["plan"]["profile"] / "buddy.pid"
        roster.write_private(pid_path, {"pid": 4242})

        def process_stat(path, *args, **kwargs):
            self.assertEqual(path, Path("/proc/4242/stat"))
            return "4242 (agent wow) S 1 0 0 0"

        with mock.patch.object(Path, "read_text", autospec=True, side_effect=process_stat) as stat_read, \
                mock.patch.object(Path, "iterdir", side_effect=AssertionError("unexpected process enumeration")), \
                mock.patch.object(roster.subprocess, "run") as run:
            with self.assertRaisesRegex(roster.Error, "running process"):
                self.adapter.login(self.row)
        self.assertEqual(stat_read.call_count, 1)
        run.assert_not_called()

    def test_untracked_own_config_play_blocks_cli_before_subprocess(self):
        config, _ = self.adapter.paths(self.row)

        def processes(path):
            self.assertEqual(path, Path("/proc"))
            return iter([Path("/proc/self"), Path("/proc/4243"), Path("/proc/4242")])

        def command_line(path):
            if path == Path("/proc/4243/cmdline"):
                return b"agent-wow\0--config\0/another/profile/client.yaml\0play\0"
            self.assertEqual(path, Path("/proc/4242/cmdline"))
            return b"\0".join([b"agent-wow", b"--config", str(config / "client.yaml").encode(), b"play", b""])

        with mock.patch.object(Path, "iterdir", autospec=True, side_effect=processes), \
                mock.patch.object(Path, "read_bytes", autospec=True, side_effect=command_line) as commands_read, \
                mock.patch.object(Path, "read_text", side_effect=AssertionError("unexpected stat read")), \
                mock.patch.object(roster.subprocess, "run") as run:
            with self.assertRaisesRegex(roster.Error, "untracked body"):
                self.adapter.auth_status(self.row)
        self.assertEqual(commands_read.call_count, 2)
        run.assert_not_called()

    def test_malformed_or_dangling_pid_record_blocks_cli_before_proc_or_subprocess(self):
        pid_path = self.root / "config/buddies" / self.row["plan"]["profile"] / "buddy.pid"
        for record in ([], None, {"pid": "4242"}, {"pid": True}, {"pid": 1}, "dangling-symlink"):
            with self.subTest(record=record):
                pid_path.unlink(missing_ok=True)
                if record == "dangling-symlink":
                    pid_path.symlink_to(self.root / "missing-record.json")
                else:
                    roster.write_private(pid_path, record)
                with mock.patch.object(Path, "iterdir", side_effect=AssertionError("unexpected process enumeration")), \
                        mock.patch.object(Path, "read_text", side_effect=AssertionError("unexpected stat read")), \
                        mock.patch.object(roster.subprocess, "run") as run:
                    with self.assertRaises((roster.Error, OSError)):
                        self.adapter.login(self.row)
                run.assert_not_called()

    def prepare_console_credentials(self):
        with mock.patch.object(self.adapter, "guard"):
            self.adapter.prepare(self.row)
        self.row["accountPhase"] = "intent"
        config, _ = self.adapter.paths(self.row)
        return config, roster.read_private(config / "auth.json")

    def console_result(self, chunks, docker_ids="0123456789ab\n"):
        console = FakeConsole(chunks)
        with mock.patch.object(self.adapter, "guard"), \
                mock.patch.object(roster.subprocess, "run", return_value=SimpleNamespace(stdout=docker_ids)) as docker, \
                mock.patch.object(roster.socket, "socket", return_value=console) as socket_factory:
            result = self.adapter.create_account(self.row)
        return result, console, docker, socket_factory

    def test_account_command_requires_intent_before_console_or_subprocess(self):
        with mock.patch.object(self.adapter, "guard") as guard, \
                mock.patch.object(roster.subprocess, "run") as run, \
                mock.patch.object(roster.socket, "socket") as socket_factory:
            with self.assertRaises(roster.Error):
                self.adapter.create_account(self.row)
        guard.assert_not_called()
        run.assert_not_called()
        socket_factory.assert_not_called()

    def test_console_exact_success_uses_reviewed_transport_and_only_redacted_result(self):
        config, credentials = self.prepare_console_credentials()
        before = {p.relative_to(self.root): p.read_bytes() for p in self.root.rglob("*") if p.is_file()}
        before_row = copy.deepcopy(self.row)
        raw = ("account create WCHORDE02 " + credentials["password"] + "\r\n"
               "CONCURRENT_PRIVATE_TEST\r\n\x1b[32mAccount created: WCHORDE02\x1b[0m\r\n").encode()
        for status in (b"101 UPGRADED", b"101 Switching Protocols"):
            with self.subTest(status=status), \
                    mock.patch.object(roster.sys, "stdout", new_callable=io.StringIO) as output, \
                    mock.patch.object(roster.sys, "stderr", new_callable=io.StringIO) as errors:
                result, console, docker, socket_factory = self.console_result(
                    [b"HTTP/1.1 " + status + b"\r\nConnection: Upgrade\r\n\r\n", raw, b""])
                self.assertEqual(result, "created")
                self.assertEqual(output.getvalue(), "")
                self.assertEqual(errors.getvalue(), "")
                self.assertEqual(console.address, "/var/run/docker.sock")
                self.assertEqual(console.sent[0],
                                 b"POST /containers/0123456789ab/attach?stream=1&stdin=1&stdout=1&stderr=1 HTTP/1.1\r\n"
                                 b"Host: localhost\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Length: 0\r\n\r\n")
                self.assertEqual(console.sent[1], f"account create WCHORDE02 {credentials['password']}\n".encode())
                self.assertEqual(len(console.sent), 2)
                socket_factory.assert_called_once_with(roster.socket.AF_UNIX, roster.socket.SOCK_STREAM)
                command = docker.call_args.args[0]
                self.assertEqual(command, ["docker", "ps", "--filter", "label=com.docker.compose.project=warcraft-buddy-realm",
                                           "--filter", "label=com.docker.compose.service=worldserver", "--format", "{{.ID}}"])
                self.assertNotIn(credentials["password"], repr(docker.call_args))
                self.assertNotIn("shell", docker.call_args.kwargs)
        self.assertEqual(self.row, before_row)
        self.assertEqual({p.relative_to(self.root): p.read_bytes() for p in self.root.rglob("*") if p.is_file()}, before)
        self.assertEqual(roster.read_private(config / "auth.json"), credentials)

    def test_console_collision_and_unknown_receipts_do_not_invent_success(self):
        self.prepare_console_credentials()
        for raw, expected in (
                (b"Account WCHORDE02 already exists.\r\n", "collision"),
                (b"Account WCHORDE03 already exists.\r\n", "unknown"),
                (b"Account with this name already exists.\r\n", "unknown"),
                (b"Account created: WCHORDE020\r\n", "unknown"),
                (b"Account created: WCHORDE03\r\n", "unknown"),
                (b"Command sent.\r\n", "unknown"),
                (b"", "unknown")):
            with self.subTest(raw=raw):
                result, console, _, _ = self.console_result([b"HTTP/1.1 101 UPGRADED\r\n\r\n", raw, b""])
                self.assertEqual(result, expected)
                self.assertEqual(len(console.sent), 2)

    def test_console_rejects_failed_or_incomplete_handshake_before_command(self):
        self.prepare_console_credentials()
        for chunks in ([b"HTTP/1.1 200 OK\r\n\r\n"], [b"HTTP/1.1 101 UPGRADED\r\n", b""],
                       [b"HTTP/1.1 1010 Invalid\r\n\r\n"], [b"x" * 16384]):
            with self.subTest(chunks=chunks):
                console = FakeConsole(chunks)
                with mock.patch.object(self.adapter, "guard"), \
                        mock.patch.object(roster.subprocess, "run", return_value=SimpleNamespace(stdout="0123456789ab\n")), \
                        mock.patch.object(roster.socket, "socket", return_value=console):
                    with self.assertRaises(roster.Error):
                        self.adapter.create_account(self.row)
                self.assertEqual(len(console.sent), 1)

    def test_wrong_identity_or_credentials_cannot_open_console(self):
        config, credentials = self.prepare_console_credentials()
        variants = (None, {}, {**credentials, "username": "WCHORDE01"},
                    {**credentials, "password": "unsafe command\n"})
        for value in variants:
            with self.subTest(value=value):
                roster.write_private(config / "auth.json", value)
                with mock.patch.object(self.adapter, "guard"), \
                        mock.patch.object(roster.subprocess, "run") as docker, \
                        mock.patch.object(roster.socket, "socket") as socket_factory:
                    with self.assertRaises(roster.Error):
                        self.adapter.create_account(self.row)
                docker.assert_not_called()
                socket_factory.assert_not_called()
        roster.write_private(config / "auth.json", credentials)
        self.row["plan"]["account"] = "WCHORDE03"
        with mock.patch.object(self.adapter, "guard"), mock.patch.object(roster.socket, "socket") as socket_factory:
            with self.assertRaises(roster.Error):
                self.adapter.create_account(self.row)
        socket_factory.assert_not_called()

    def test_console_requires_one_exact_known_container_identity(self):
        self.prepare_console_credentials()
        for docker_ids in ("", "0123456789ab\nfedcba987654\n", "not-a-container\n", "0123456789ab;bad\n"):
            with self.subTest(docker_ids=docker_ids), mock.patch.object(self.adapter, "guard"), \
                    mock.patch.object(roster.subprocess, "run", return_value=SimpleNamespace(stdout=docker_ids)), \
                    mock.patch.object(roster.socket, "socket") as socket_factory:
                with self.assertRaises(roster.Error):
                    self.adapter.create_account(self.row)
                socket_factory.assert_not_called()

    def test_live_apply_requires_acknowledgement_before_any_adapter_or_profile_preparation(self):
        path = self.root / "data/autonomy/thrall/roster/state.json"
        before = state_copy()
        roster.write_private(path, before)
        with mock.patch.object(roster.sys, "argv", ["roster", "apply", "--root", str(self.root)]), \
                mock.patch.object(roster.sys, "platform", "linux"), \
                mock.patch.object(roster, "RealmAdapter", side_effect=AssertionError("unexpected live adapter")), \
                mock.patch.object(roster.sys, "stderr", new_callable=io.StringIO) as errors:
            self.assertEqual(roster.main(), 1)
        self.assertIn("requires --acknowledge-account-creation", errors.getvalue())
        self.assertEqual(roster.read_private(path), before)
        self.assertFalse((self.root / "config/autonomy/thrall/roster").exists())

    def test_acknowledged_apply_is_blocked_on_mac_before_adapter(self):
        path = self.root / "data/autonomy/thrall/roster/state.json"
        roster.write_private(path, state_copy())
        with mock.patch.object(roster.sys, "argv", ["roster", "apply", "--root", str(self.root), "--acknowledge-account-creation"]), \
                mock.patch.object(roster.sys, "platform", "darwin"), \
                mock.patch.object(roster, "RealmAdapter") as adapter, \
                mock.patch.object(roster.sys, "stderr", new_callable=io.StringIO) as errors:
            self.assertEqual(roster.main(), 1)
        self.assertIn("known Linux realm host", errors.getvalue())
        adapter.assert_not_called()

    def test_acknowledged_linux_apply_uses_fake_adapter_for_one_pilot_then_bounded_batch(self):
        path = self.root / "data/autonomy/thrall/roster/state.json"
        roster.write_private(path, state_copy())
        adapter = FakeAdapter([])
        for batch, expected_slots in ((1, [2]), (5, [3, 4, 5, 6, 7])):
            adapter.calls = []
            with self.subTest(batch=batch), \
                    mock.patch.object(roster.sys, "argv", ["roster", "apply", "--root", str(self.root),
                                                           "--acknowledge-account-creation", "--batch-size", str(batch)]), \
                    mock.patch.object(roster.sys, "platform", "linux"), \
                    mock.patch.object(roster, "RealmAdapter", return_value=adapter), \
                    mock.patch.object(roster.sys, "stdout", new_callable=io.StringIO) as output, \
                    mock.patch.object(roster.subprocess, "run", side_effect=AssertionError("unexpected subprocess")), \
                    mock.patch.object(roster.socket, "socket", side_effect=AssertionError("unexpected socket")):
                self.assertEqual(roster.main(), 0)
                result = json.loads(output.getvalue())
                self.assertTrue(result["liveApplyEnabled"])
                self.assertTrue(result["liveApplyRequiresAcknowledgement"])
                self.assertEqual([c["slot"] for c in adapter.calls if c["method"] == "create_account"], expected_slots)
        self.assertEqual(roster.summary(roster.read_private(path))["actualConfirmedCharacters"], 7)

    def test_acknowledged_linux_apply_rejects_oversized_batch_and_unverified_multi_pilot(self):
        path = self.root / "data/autonomy/thrall/roster/state.json"
        before = state_copy()
        roster.write_private(path, before)
        for batch in (0, 6, 2):
            with self.subTest(batch=batch), \
                    mock.patch.object(roster.sys, "argv", ["roster", "apply", "--root", str(self.root),
                                                           "--acknowledge-account-creation", "--batch-size", str(batch)]), \
                    mock.patch.object(roster.sys, "platform", "linux"), \
                    mock.patch.object(roster, "RealmAdapter") as adapter, \
                    mock.patch.object(roster.sys, "stderr", new_callable=io.StringIO):
                self.assertEqual(roster.main(), 1)
            adapter.assert_not_called()
            self.assertEqual(roster.read_private(path), before)


if __name__ == "__main__":
    unittest.main()
