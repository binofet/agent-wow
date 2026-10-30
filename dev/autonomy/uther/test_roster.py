#!/usr/bin/env python3
"""Offline checks for identity isolation, durable reservations and safe resumption."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock


SPEC = importlib.util.spec_from_file_location("uther_roster", Path(__file__).with_name("roster.py"))
roster = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(roster)


def character(slot=None, **changes):
    slot = slot or roster.plan()["newAccounts"][0]
    result = {"guid": "12", "name": slot["name"], "race": slot["race"],
              "class": slot["class"], "gender": slot["gender"], "level": 1}
    result.update(changes)
    return result


class FakeCLI:
    def __init__(self, lists, reply=None, failure=None, before_create=lambda name: None):
        self.lists, self.reply, self.failure = list(lists), reply or {"reply": "success"}, failure
        self.creates, self.before_create = [], before_create

    def list(self):
        if not self.lists:
            raise AssertionError("Unexpected extra normal character list")
        result = self.lists.pop(0)
        if isinstance(result, Exception):
            raise result
        return result

    def create(self, name):
        self.before_create(name)
        self.creates.append(name)
        if self.failure:
            raise self.failure
        return self.reply


class PlanTests(unittest.TestCase):
    def test_total_and_reserved_accounts_ports(self):
        result = roster.plan()
        self.assertEqual([row["guid"] for row in result["protected"]], ["1", "4", "5", "7"])
        self.assertEqual(len(result["newAccounts"]) + len(result["protected"]), 40)
        self.assertEqual([row["account"] for row in result["newAccounts"]],
                         [f"WCALLIANCE{number:02}" for number in range(2, 38)])
        self.assertEqual([row["rpcPort"] for row in result["newAccounts"]], list(range(8402, 8438)))
        self.assertNotIn("3", [row["guid"] for row in result["protected"]])

    def test_all_nine_ordinary_classes_and_legal_alliance_pairs(self):
        rows = roster.plan()["newAccounts"]
        self.assertEqual({row["classId"] for row in rows}, set(roster.CLASSES.values()))
        for row in rows:
            self.assertIn(row["classId"], roster.LEGAL[row["race"]])
            self.assertNotEqual(row["classId"], 6)
        self.assertEqual([(row["race"], row["class"]) for row in rows[:5]],
                         [("Gnome", "Warlock"), ("Night Elf", "Druid"), ("Dwarf", "Hunter"),
                          ("Human", "Paladin"), ("Draenei", "Shaman")])

    def test_professions_and_stage_sixty_are_planning_intent_only(self):
        result = roster.plan()
        self.assertEqual(result["progressionIntent"]["softCap"], 60)
        self.assertEqual(result["progressionIntent"]["futureAdmission"], "explicit_user_change_required")
        self.assertEqual([row["desiredProfessions"] for row in result["newAccounts"][:4]],
                         [["Mining", "Engineering"], ["Herbalism", "Inscription"],
                          ["Mining", "Jewelcrafting"], ["Mining", "Blacksmithing"]])
        for row in result["protected"] + result["newAccounts"]:
            self.assertEqual(row["desiredSecondaryProfessions"], ["Cooking", "Fishing", "First Aid"])
            self.assertNotIn("learnedProfessions", row)

    def test_names_are_bounded_and_unique_including_fallbacks(self):
        names = [name.lower() for row in roster.plan()["newAccounts"] for name in row["names"]]
        self.assertEqual(len(names), len(set(names)))
        self.assertTrue(all(roster.valid_name(name) for name in names))
        for name in ("A", "Nix7", "Nix-wick", "Nïxwick", "a" * 13):
            with self.subTest(name=name), self.assertRaises(roster.Error):
                roster.plan(name)
        self.assertEqual(roster.plan("Tinkara")["newAccounts"][0]["profile"], "alliance-tinkara")
        for name in ("Aveline", "Brannic"):
            with self.subTest(name=name), self.assertRaises(roster.Error):
                roster.plan(name)


class LedgerTests(unittest.TestCase):
    def setUp(self):
        self.saved = []
        self.coach = roster.Roster(roster.initial(), lambda value: self.saved.append(copy.deepcopy(value)), now=lambda: 123)
        self.account = "WCALLIANCE02"

    def test_reservation_is_durable_before_create_and_guid_is_from_live_list(self):
        def verify_reservation(name):
            self.assertEqual(self.saved[-1]["attempts"][-1]["name"], name)
            self.assertEqual(self.saved[-1]["attempts"][-1]["state"], "pending")
            self.assertFalse(self.saved[-1]["verified"])
        cli = FakeCLI([[], [character(guid="35")]], before_create=verify_reservation)
        result = self.coach.create_next(self.account, cli)
        self.assertEqual(result["state"], "verified")
        self.assertEqual(result["character"]["guid"], "35")
        self.assertEqual(cli.creates, ["Nixwick"])
        self.assertEqual(self.coach.data["attempts"][0]["state"], "verified")
        self.assertEqual(result["professions"], "unlearned_until_actual_trainer_receipt")

    def test_success_reply_without_character_remains_pending_and_blocks_retry(self):
        cli = FakeCLI([[], []])
        self.assertEqual(self.coach.create_next(self.account, cli)["state"], "pending")
        restarted = roster.Roster(self.saved[-1], lambda value: None)
        again = FakeCLI([[]])
        with self.assertRaisesRegex(roster.Error, "absence.*not retry"):
            restarted.create_next(self.account, again)
        self.assertFalse(again.creates)

    def test_timeout_after_send_is_reconciled_without_duplicate_create(self):
        cli = FakeCLI([[]], failure=roster.Error("timeout"))
        with self.assertRaisesRegex(roster.Error, "reconcile only"):
            self.coach.create_next(self.account, cli)
        restarted = roster.Roster(self.saved[-1], lambda value: None)
        again = FakeCLI([[character(guid="9001")]])
        result = restarted.create_next(self.account, again)
        self.assertEqual(result["character"]["guid"], "9001")
        self.assertFalse(again.creates)

    def test_failure_of_post_create_list_preserves_pending(self):
        cli = FakeCLI([[], roster.Error("unavailable")])
        with self.assertRaises(roster.Error):
            self.coach.create_next(self.account, cli)
        self.assertEqual(self.saved[-1]["attempts"][0]["state"], "pending")

    def test_only_definite_name_rejection_advances_one_candidate_per_command(self):
        first = FakeCLI([[], []], {"reply": "server_rejection", "serverCode": 0x32})
        self.assertEqual(self.coach.create_next(self.account, first)["state"], "name_rejected")
        next_cli = FakeCLI([[], [character(name="Nixwicka")]])
        self.assertEqual(self.coach.create_next(self.account, next_cli)["character"]["name"], "Nixwicka")
        self.assertEqual(first.creates, ["Nixwick"])
        self.assertEqual(next_cli.creates, ["Nixwicka"])

    def test_non_name_server_rejections_block_instead_of_renaming(self):
        for code in (0x30, 0x35, 0x36, 0x3b, 0x3e):
            with self.subTest(code=code):
                coach = roster.Roster(roster.initial(), lambda value: None)
                first = FakeCLI([[], []], {"reply": "server_rejection", "serverCode": code})
                self.assertEqual(coach.create_next(self.account, first)["state"], "blocked")
                second = FakeCLI([[]])
                with self.assertRaises(roster.Error):
                    coach.create_next(self.account, second)
                self.assertFalse(second.creates)

    def test_name_alternatives_are_finite(self):
        for _ in range(5):
            self.coach.create_next(self.account, FakeCLI([[], []], {"reply": "server_rejection", "serverCode": 0x5f}))
        cli = FakeCLI([[]])
        with self.assertRaisesRegex(roster.Error, "alternatives exhausted"):
            self.coach.create_next(self.account, cli)
        self.assertFalse(cli.creates)

    def test_existing_normal_created_character_can_be_observed_without_mutation(self):
        cli = FakeCLI([[character()]])
        result = self.coach.reconcile(self.account, cli)
        self.assertEqual(result["creation"], "observed_existing")
        self.assertEqual(result["proof"], "normal_character_list")
        self.assertFalse(cli.creates)

    def test_wrong_identity_extra_character_and_protected_guid_block(self):
        for rows in ([character(guid="7")], [character(race="Human")], [character(**{"class": "Mage"})],
                     [character(name="Stranger")], [character(gender="Female")],
                     [character(), character(guid="13", name="Nixwicka")]):
            with self.subTest(rows=rows):
                coach = roster.Roster(roster.initial(), lambda value: None)
                cli = FakeCLI([rows])
                with self.assertRaises(roster.Error):
                    coach.create_next(self.account, cli)
                self.assertFalse(cli.creates)

    def test_rejection_and_present_character_conflict_stays_blocked(self):
        cli = FakeCLI([[], [character()]], {"reply": "server_rejection", "serverCode": 0x32})
        with self.assertRaisesRegex(roster.Error, "conflicts"):
            self.coach.create_next(self.account, cli)
        self.assertNotIn(self.account, self.coach.data["verified"])

    def test_global_duplicate_guid_is_rejected(self):
        self.coach.reconcile(self.account, FakeCLI([[character()]]))
        slot = self.coach.slot("WCALLIANCE03")
        with self.assertRaisesRegex(roster.Error, "already owned"):
            self.coach.reconcile(slot["account"], FakeCLI([[character(slot)]]))

    def test_verified_character_absence_is_not_recreation_authority(self):
        self.coach.reconcile(self.account, FakeCLI([[character()]]))
        cli = FakeCLI([[]])
        with self.assertRaisesRegex(roster.Error, "absent"):
            self.coach.create_next(self.account, cli)
        self.assertFalse(cli.creates)

    def test_out_of_order_or_protected_accounts_cannot_be_created(self):
        cli = FakeCLI([[]])
        with self.assertRaisesRegex(roster.Error, "first unverified"):
            self.coach.create_next("WCALLIANCE03", cli)
        self.assertFalse(cli.creates)
        for account in ("WCALLIANCE01", "JOEL", "WCHORDE02"):
            with self.subTest(account=account), self.assertRaises(roster.Error):
                self.coach.slot(account)

    def test_corrupt_ledger_cannot_grant_name_retry(self):
        data = roster.initial()
        data["attempts"] = [{"sequence": 1, "account": self.account, "name": "Nixwick",
                             "state": "name_rejected", "serverCode": 0x36}]
        with self.assertRaisesRegex(roster.Error, "definite server"):
            roster.validate(data)

    def test_earlier_unresolved_or_verified_attempt_cannot_be_hidden_by_name_rejection(self):
        for earlier in ("pending", "blocked", "verified"):
            with self.subTest(earlier=earlier):
                data = roster.initial()
                data["attempts"] = [
                    {"sequence": 1, "account": self.account, "name": "Nixwick", "state": earlier},
                    {"sequence": 2, "account": self.account, "name": "Nixwicka",
                     "state": "name_rejected", "serverCode": 0x32}]
                cli = FakeCLI([[]])
                with self.assertRaisesRegex(roster.Error, "later reservation is forbidden"):
                    coach = roster.Roster(data, lambda value: None)
                    coach.create_next(self.account, cli)
                self.assertFalse(cli.creates)
                self.assertEqual(len(cli.lists), 1)  # Blocked before even a character-list connection.

    def test_verified_records_and_history_must_agree(self):
        self.coach.create_next(self.account, FakeCLI([[], [character()]]))
        valid = copy.deepcopy(self.coach.data)
        variants = []
        no_record = copy.deepcopy(valid)
        no_record["verified"] = {}
        variants.append(no_record)
        wrong_name = copy.deepcopy(valid)
        wrong_name["verified"][self.account]["character"]["name"] = "Nixwicka"
        variants.append(wrong_name)
        wrong_state = copy.deepcopy(valid)
        wrong_state["attempts"][0]["state"] = "pending"
        variants.append(wrong_state)
        missing_history = copy.deepcopy(valid)
        missing_history["attempts"] = []
        variants.append(missing_history)
        wrong_origin = copy.deepcopy(valid)
        wrong_origin["verified"][self.account]["creation"] = "observed_existing"
        variants.append(wrong_origin)
        for data in variants:
            with self.subTest(data=data), self.assertRaises(roster.Error):
                roster.validate(data)
        self.assertEqual(roster.validate(valid), valid)

    def test_retry_names_cannot_skip_reservation_order(self):
        data = roster.initial()
        data["attempts"] = [{"sequence": 1, "account": self.account, "name": "Nixwicka",
                             "state": "name_rejected", "serverCode": 0x32}]
        with self.assertRaisesRegex(roster.Error, "reservation order"):
            roster.validate(data)


class StorageTests(unittest.TestCase):
    def test_private_atomic_roundtrip_and_lock(self):
        with tempfile.TemporaryDirectory() as folder:
            store = roster.Store(Path(folder).resolve() / "state")
            with store.lock():
                store.save(roster.initial())
                self.assertEqual(store.load(), roster.initial())
                with self.assertRaisesRegex(roster.Error, "holds the lock"):
                    store.lock()
            self.assertEqual(store.path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(store.directory.stat().st_mode & 0o777, 0o700)

    def test_public_or_symlink_state_is_refused(self):
        with tempfile.TemporaryDirectory() as folder:
            folder = Path(folder).resolve()
            store = roster.Store(folder / "state")
            store.save(roster.initial())
            store.path.chmod(0o644)
            with self.assertRaisesRegex(roster.Error, "mode0600"):
                store.load()
            store.path.unlink()
            target = folder / "target"
            target.write_text("sentinel")
            store.path.symlink_to(target)
            with self.assertRaises(OSError):
                store.save(roster.initial())
            self.assertEqual(target.read_text(), "sentinel")

    def test_symlink_parent_is_rejected_before_creating_any_state(self):
        with tempfile.TemporaryDirectory() as folder:
            folder = Path(folder).resolve()
            target = folder / "target"
            target.mkdir()
            link = folder / "link"
            link.symlink_to(target)
            with self.assertRaises(roster.Error):
                roster.Store(link / "new-state")
            self.assertFalse((target / "new-state").exists())


class CLITests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.slot = roster.plan()["newAccounts"][0]
        (self.root / "bin").mkdir()
        binary = self.root / "bin/agent-wow"
        binary.write_text("not executed")
        binary.chmod(0o700)
        self.config = self.root / self.slot["configDir"]
        self.config.mkdir(mode=0o700, parents=True)
        (self.root / self.slot["dataDir"]).mkdir(mode=0o700, parents=True)
        for name in ("auth.json", "realm.json", "buddy.yaml"):
            path = self.config / name
            path.write_text("PRIVATE_SENTINEL")
            path.chmod(0o600)
        self.run = mock.Mock()
        self.cli = roster.CLI(self.root, self.slot, self.slot["account"], 1, self.run)

    def result(self, out=b"", err=b"", code=0):
        return subprocess.CompletedProcess([], code, out, err)

    def auth_ok(self):
        return self.result(b"Session valid.\nUsername: WCALLIANCE02\n")

    def test_exact_inactive_account_and_pid_guard_are_required(self):
        with self.assertRaisesRegex(roster.Error, "inactive-account"):
            roster.CLI(self.root, self.slot, "WCALLIANCE01", 1, self.run)
        pid = self.root / "config/buddies/alliance-nixwick/buddy.pid"
        pid.parent.mkdir(parents=True)
        pid.write_text("unknown or stale record")
        with self.assertRaisesRegex(roster.Error, "PID record"):
            self.cli.list()
        self.run.assert_not_called()

    def test_roster_holds_the_maintained_profile_launcher_lock(self):
        other = roster.CLI(self.root, self.slot, self.slot["account"], 1, self.run)
        with self.cli.lock():
            with self.assertRaisesRegex(roster.Error, "launcher is busy"):
                with other.lock():
                    self.fail("Two account controllers held the same profile lock")
        with other.lock():
            pass
        self.run.assert_not_called()

    def test_wrong_saved_session_never_reauthenticates_or_creates(self):
        self.run.return_value = self.result(b"Username: WCALLIANCE01\n")
        with self.assertRaisesRegex(roster.Error, "identity is unconfirmed"):
            self.cli.create("Nixwick")
        self.assertEqual(self.run.call_count, 1)
        self.assertEqual(self.run.call_args.args[0][-4:], ["auth", "status", "--timeout", "10s"])

    def test_saved_key_check_and_create_have_private_explicit_paths_no_credentials(self):
        self.run.side_effect = [self.auth_ok(), self.result(b"Created character: Nixwick\n")]
        self.assertEqual(self.cli.create("Nixwick"), {"reply": "success"})
        argv = self.run.call_args.args[0]
        env = self.run.call_args.kwargs["env"]
        self.assertIn(str(self.config / "buddy.yaml"), argv)
        self.assertEqual(env["AGENT_WOW_AUTH_FILE_PATH"], str(self.config / "auth.json"))
        self.assertEqual(env["AGENT_WOW_DATA_DIR"], str(self.root / self.slot["dataDir"]))
        self.assertNotIn("PRIVATE_SENTINEL", " ".join(argv) + str(env))
        self.assertNotIn("login", argv)
        self.assertEqual(argv[-10:], ["--name", "Nixwick", "--race", "7", "--class", "9", "--gender", "male", "--timeout", "10s"])

    def test_only_explicit_final_server_error_is_name_retry_evidence(self):
        for stderr, expected in (
            (b"Error: character create: name already in use (0x32)\n", {"reply": "server_rejection", "serverCode": 50}),
            (b"Error: character create outcome unknown for name Nixwick; detail (0x32)\n", {"reply": "unknown"}),
            (b"Error: authenticate to realm: name already in use (0x32)\n", {"reply": "unknown"}),
            (b"Error: character create: character creation failed (0x31)\n", {"reply": "server_rejection", "serverCode": 49}),
        ):
            with self.subTest(stderr=stderr):
                self.run.side_effect = [self.auth_ok(), self.result(err=stderr, code=1)]
                self.assertEqual(self.cli.create("Nixwick"), expected)

    def test_normal_json_list_pins_realm_and_validates_duplicates(self):
        rows = [character()]
        raw = json.dumps({"realm": {"id": 1, "name": "Private"}, "characters": rows}).encode()
        self.run.side_effect = [self.auth_ok(), self.result(raw)]
        self.assertEqual(self.cli.list(), rows)
        for bad in (
            {"realm": {"id": 2}, "characters": rows},
            {"realm": {"id": 1}, "characters": rows * 2},
            {"realm": {"id": 1}, "characters": [character(guid=12)]},
            {"realm": {"id": 1}, "characters": [character(), character(guid="13", name="nixwick")]},
        ):
            with self.subTest(bad=bad), self.assertRaises(roster.Error):
                roster.character_list(json.dumps(bad), 1)

    def test_timeout_hides_raw_output_and_leaves_credentials_unread(self):
        self.run.side_effect = subprocess.TimeoutExpired([], 40, output=b"PRIVATE_SENTINEL")
        with self.assertRaises(roster.Error) as result:
            self.cli.list()
        self.assertNotIn("PRIVATE_SENTINEL", str(result.exception))

    def test_cli_never_bootstraps_or_authenticates(self):
        self.assertFalse(hasattr(self.cli, "login"))
        self.assertFalse(hasattr(self.cli, "bootstrap"))
        self.assertFalse(hasattr(self.cli, "start"))
        self.assertFalse(hasattr(self.cli, "stop"))


if __name__ == "__main__":
    unittest.main()
