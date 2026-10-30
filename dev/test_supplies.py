"""Offline supplies RPC and private receipt regressions; no live client calls."""
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

loader = importlib.machinery.SourceFileLoader("supplies_test", str(Path(__file__).with_name("supplies")))
spec = importlib.util.spec_from_loader(loader.name, loader)
m = importlib.util.module_from_spec(spec)
loader.exec_module(m)
URL = "http://127.0.0.1:8191/rpc"
VENDOR = "17379390988329437216"
ORIGINAL = str(0x4000000000000000 + 100)
POLE = str(0x4000000000000000 + 200)
OTHER_ORIGINAL = str(0x4000000000000000 + 101)
OTHER_POLE = str(0x4000000000000000 + 201)
BUY = {"vendor_guid": VENDOR, "vendor_slot": 1, "item_id": 4471, "count": 1, "max_spend_copper": 100}
EQUIP = {"item_guid": POLE, "backpack_slot": 2, "original_mainhand_guid": ORIGINAL}


def initial_state():
    return {"selfGuid": "6", "sessionId": "body1", "revision": "5", "active": False,
            "backpackKnown": True, "backpackRevision": "11", "skillsKnown": True, "skillsRevision": "3",
            "copperKnown": True, "copper": 1000, "copperRevision": "7",
            "mainhand": {"slotKnown": True, "metadataKnown": True, "itemGuid": ORIGINAL, "entry": 36},
            "backpack": [{"bag": 0, "slot": i, **({"itemGuid": POLE, "entry": 6256, "stackCount": 1, "metadataKnown": True}
                          if i == 2 else {})} for i in range(16)],
            "offer": {"revision": "9", "npcGuid": VENDOR, "expiresInMs": "30000", "items": [
                {"slot": 1, "entry": 4471, "availableCount": -1, "priceCopper": 23, "buyCount": 1}]}}


class FakeRPC:
    url = URL

    def __init__(self):
        self.current, self.calls = initial_state(), []
        self.before_mutation, self.after_mutation = None, None
        self.status, self.ambiguous = "CONFIRMED", False

    def call(self, method, params):
        self.calls.append((method, copy.deepcopy(params)))
        if method == "buddy-supplies.state":
            return copy.deepcopy(self.current)
        if method == "buddy-supplies.discover":
            return {"state": copy.deepcopy(self.current), "nearby": []}
        if method == "buddy-supplies.list":
            return copy.deepcopy(self.current)
        kind = method.removeprefix("buddy-supplies.")
        if kind not in ("buy", "equip", "restore"):
            raise AssertionError(method)
        if self.before_mutation:
            self.before_mutation(kind, params)
        op = {"requestId": params["requestId"], "kind": kind, "status": self.status,
              "transportWritten": self.status != "BLOCKED", "leaseReleased": self.status != "REQUESTED",
              "baselineBackpackRevision": params["expectedBackpackRevision"],
              "serverSuccessObserved": True, "inventoryConfirmed": True, "copperConfirmed": True,
              "equipmentConfirmed": True}
        if kind == "buy":
            op.update(npcGuid=params["npcGuid"], itemId=params["itemId"], count=params["count"],
                      costCopper=params["expectedPriceCopper"] * params["count"],
                      baselineCopperRevision=params["expectedCopperRevision"])
        else:
            op.update(itemGuid=params["itemGuid"], originalWeaponGuid=params["originalWeaponGuid"])
            selected = next(i for i in self.current["backpack"] if i["slot"] == params["backpackSlot"])
            hand = self.current["mainhand"]
            old = {"itemGuid": hand["itemGuid"], "entry": hand["entry"]}
            hand.update(itemGuid=selected["itemGuid"], entry=selected["entry"])
            selected.update(old)
            self.current.update(originalWeaponGuid=ORIGINAL, equippedPoleGuid=POLE, restoreRequired=kind == "equip")
        self.current["operation"] = op
        if self.after_mutation:
            self.after_mutation(self.current)
        if self.ambiguous:
            raise m.Error("rpc_response_unconfirmed")
        return copy.deepcopy(self.current)

    def mutations(self):
        return [(method, params) for method, params in self.calls if method in
                ("buddy-supplies.buy", "buddy-supplies.equip", "buddy-supplies.restore")]


class SuppliesTests(unittest.TestCase):
    def setUp(self):
        base = "/private/tmp" if Path("/private/tmp").is_dir() else None
        temporary = tempfile.TemporaryDirectory(dir=base)
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.path, self.rpc = self.root / "receipts" / "supplies.json", FakeRPC()

    def run_action(self, kind="buy", **options):
        with m.Journal(self.path, URL, 6) as journal:
            return m.Worker(self.rpc, 6, journal).mutation(kind, **options)

    def reconcile(self):
        with m.Journal(self.path, URL, 6) as journal:
            return m.Worker(self.rpc, 6, journal).reconcile()

    def saved(self):
        return json.loads(self.path.read_text())

    def test_exact_buy_request_is_private_and_durable_before_single_send(self):
        def durable(kind, request):
            saved = self.saved()
            self.assertEqual(saved["attempts"][-1]["request"], request)
            self.assertEqual(saved["attempts"][-1]["phase"], "reserved")
            self.assertEqual(saved["attempts"][-1]["originalMainhandGuid"], ORIGINAL)
        self.rpc.before_mutation = durable
        result = self.run_action(**BUY)
        self.assertEqual(result["phase"], "confirmed")
        self.assertEqual(len(self.rpc.mutations()), 1)
        request = self.rpc.mutations()[0][1]
        self.assertEqual({k: v for k, v in request.items() if k != "requestId"},
                         {"sessionId": "body1", "expectedRevision": "5", "expectedBackpackRevision": "11",
                          "expectedSkillsRevision": "3", "npcGuid": VENDOR, "offerRevision": "9",
                          "vendorSlot": 1, "itemId": 4471, "count": 1, "expectedPriceCopper": 23,
                          "maxSpendCopper": 100, "expectedCopperRevision": "7"})
        self.assertEqual(self.path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.path.parent.stat().st_mode & 0o777, 0o700)

    def test_ambiguous_buy_response_reconciles_without_second_mutation(self):
        self.rpc.ambiguous = True
        self.assertEqual(self.run_action(**BUY)["phase"], "transport_unknown")
        self.assertEqual(self.run_action(**BUY)["phase"], "confirmed")
        self.assertEqual(len(self.rpc.mutations()), 1)

    def test_requested_and_missing_receipts_never_retry_or_claim_success(self):
        self.rpc.status = "REQUESTED"
        self.assertEqual(self.run_action(**BUY)["phase"], "requested")
        self.rpc.current.pop("operation")
        self.assertEqual(self.run_action(**BUY)["phase"], "receipt_mismatch")
        self.assertEqual(self.run_action(**BUY)["phase"], "receipt_mismatch")
        self.assertEqual(len(self.rpc.mutations()), 1)

    def test_wrong_request_identity_or_receipt_fields_are_not_success(self):
        for changed in ({"requestId": "foreign"}, {"npcGuid": "1"}, {"itemId": 1}, {"count": 2},
                        {"costCopper": 24}, {"baselineBackpackRevision": "12"}, {"baselineCopperRevision": "8"}):
            with self.subTest(changed=changed):
                self.setUp()
                self.rpc.after_mutation = lambda state, changed=changed: state["operation"].update(changed)
                self.assertEqual(self.run_action(**BUY)["phase"], "receipt_mismatch")
                self.run_action(**BUY)
                self.assertEqual(len(self.rpc.mutations()), 1)

    def test_confirmed_label_requires_all_actual_buy_receipts(self):
        for field in ("serverSuccessObserved", "inventoryConfirmed", "copperConfirmed", "leaseReleased"):
            with self.subTest(field=field):
                self.setUp()
                self.rpc.after_mutation = lambda state, field=field: state["operation"].update({field: False})
                self.assertEqual(self.run_action(**BUY)["phase"], "outcome_unconfirmed")
                self.run_action(**BUY)
                self.assertEqual(len(self.rpc.mutations()), 1)

    def test_lost_transport_callback_with_complete_actual_proofs_confirms_without_resend(self):
        for kind, options in (("buy", BUY), ("equip", EQUIP)):
            with self.subTest(kind=kind):
                self.setUp()
                self.rpc.ambiguous = True
                self.rpc.after_mutation = lambda state: state["operation"].update(transportWritten=False)
                self.assertEqual(self.run_action(kind, **options)["phase"], "transport_unknown")
                request = copy.deepcopy(self.rpc.mutations()[0][1])
                self.assertEqual(self.run_action(kind, **options)["phase"], "confirmed")
                self.assertEqual(self.rpc.mutations(), [("buddy-supplies." + kind, request)])
                self.assertIs(self.saved()["attempts"][-1]["operation"]["transportWritten"], False)

    def test_lost_transport_callback_with_incomplete_actual_proofs_remains_unconfirmed(self):
        for kind, options, missing in (("buy", BUY, "copperConfirmed"), ("equip", EQUIP, "equipmentConfirmed")):
            with self.subTest(kind=kind):
                self.setUp()
                self.rpc.after_mutation = lambda state, missing=missing: state["operation"].update(
                    {"transportWritten": False, missing: False})
                self.assertEqual(self.run_action(kind, **options)["phase"], "outcome_unconfirmed")
                self.assertEqual(self.run_action(kind, **options)["phase"], "outcome_unconfirmed")
                self.assertEqual(len(self.rpc.mutations()), 1)

    def test_new_session_never_resends_or_discards_pending_original_weapon(self):
        self.rpc.ambiguous = True
        self.run_action("equip", **EQUIP)
        before = copy.deepcopy(self.saved()["backup"])
        self.rpc.current["sessionId"] = "body2"
        self.assertEqual(self.run_action("restore")["phase"], "new_session_requires_review")
        self.assertEqual(self.saved()["backup"], before)
        self.assertEqual(len(self.rpc.mutations()), 1)

    def test_equip_preserves_exact_original_and_restore_derives_current_actual_slot(self):
        def durable(kind, request):
            if kind == "equip":
                backup = self.saved()["backup"]
                self.assertEqual((backup["originalWeaponGuid"], backup["originalWeaponEntry"], backup["poleGuid"]),
                                 (ORIGINAL, 36, POLE))
                self.assertFalse(backup["confirmed"])
        self.rpc.before_mutation = durable
        self.assertEqual(self.run_action("equip", **EQUIP)["phase"], "confirmed")
        self.rpc.current["backpack"][2], self.rpc.current["backpack"][8] = (
            {**self.rpc.current["backpack"][8], "slot": 2}, {**self.rpc.current["backpack"][2], "slot": 8})
        self.assertEqual(self.run_action("restore")["phase"], "confirmed")
        request = self.rpc.mutations()[-1][1]
        self.assertEqual((request["itemGuid"], request["originalWeaponGuid"], request["backpackSlot"]), (ORIGINAL, ORIGINAL, 8))
        self.assertTrue(self.saved()["backup"]["restored"])
        self.assertEqual([method for method, _ in self.rpc.mutations()], ["buddy-supplies.equip", "buddy-supplies.restore"])

    def test_restore_after_module_history_loss_is_blocked_and_keeps_backup(self):
        self.run_action("equip", **EQUIP)
        before = copy.deepcopy(self.saved()["backup"])
        self.rpc.current.update(sessionId="body2", originalWeaponGuid="0", restoreRequired=False)
        self.rpc.current.pop("operation")
        with self.assertRaisesRegex(m.Error, "same_module"):
            self.run_action("restore")
        self.assertEqual(self.saved()["backup"], before)
        self.assertEqual(len(self.rpc.mutations()), 1)

    def test_explicit_equip_identity_and_known_metadata_are_required(self):
        for change in ({"original_mainhand_guid": OTHER_ORIGINAL}, {"item_guid": OTHER_POLE}, {"backpack_slot": 3}):
            with self.subTest(change=change), self.assertRaises(m.Error):
                self.run_action("equip", **{**EQUIP, **change})
        self.rpc.current["mainhand"]["metadataKnown"] = False
        with self.assertRaises(m.Error):
            self.run_action("equip", **EQUIP)
        self.assertEqual(self.rpc.mutations(), [])

    def test_nonpole_equipment_is_rejected_before_intent_or_send(self):
        self.rpc.current["backpack"][2]["entry"] = 7005
        with self.assertRaisesRegex(m.Error, "fishing_pole_6256"):
            self.run_action("equip", **EQUIP)
        self.assertEqual(self.rpc.mutations(), [])
        self.assertFalse(self.path.exists())

    def test_partial_duplicate_unknown_revision_and_inconsistent_inventory_never_send(self):
        mutations = [
            lambda s: s["backpack"].pop(),
            lambda s: s["backpack"][15].update(slot=14),
            lambda s: s.update(backpackRevision="0"),
            lambda s: s["backpack"][2].update(itemGuid="200"),
            lambda s: s["backpack"][2].update(itemGuid=str(0x4000000000000000)),
            lambda s: s["backpack"][2].update(entry=0),
            lambda s: s["backpack"][2].update(stackCount=0),
            lambda s: s["backpack"][2].update(metadataKnown=False),
            lambda s: s["backpack"][3].update(entry=6256),
            lambda s: s["backpack"][3].update(metadataKnown=True),
            lambda s: s["mainhand"].update(itemGuid="100"),
        ]
        for index, mutate in enumerate(mutations):
            with self.subTest(case=index):
                self.setUp()
                mutate(self.rpc.current)
                with self.assertRaises(m.Error):
                    self.run_action(**BUY)
                self.assertEqual(self.rpc.mutations(), [])
                self.assertFalse(self.path.exists())

    def test_equipment_confirmation_requires_expected_entry_for_equip_and_restore(self):
        self.rpc.after_mutation = lambda state: state["mainhand"].update(entry=7005)
        self.assertEqual(self.run_action("equip", **EQUIP)["phase"], "outcome_unconfirmed")
        self.assertFalse(self.saved()["backup"]["confirmed"])
        self.setUp()
        self.run_action("equip", **EQUIP)
        self.rpc.after_mutation = lambda state: state["mainhand"].update(entry=999999)
        self.assertEqual(self.run_action("restore")["phase"], "outcome_unconfirmed")
        self.assertFalse(self.saved()["backup"]["restored"])

    def test_count_price_wallet_and_budget_are_explicit_and_bounded(self):
        for changes in ({"count": 0}, {"count": 2}, {"count": 21}, {"count": True}, {"max_spend_copper": 0},
                        {"max_spend_copper": 10001}, {"max_spend_copper": 22}, {"vendor_guid": "1"},
                        {"vendor_slot": 2}, {"item_id": 1}):
            with self.subTest(changes=changes), self.assertRaises(m.Error):
                self.run_action(**{**BUY, **changes})
        self.rpc.current["copper"] = 22
        with self.assertRaises(m.Error):
            self.run_action(**BUY)
        self.assertEqual(self.rpc.mutations(), [])

    def test_fixed_catalog_and_durable_completed_tool_purchase_prevent_repeat(self):
        self.rpc.current["backpack"] = [{"slot": i} for i in range(16)]
        for item in sorted(m.TOOLS):
            with self.subTest(item=item):
                self.setUp()
                self.rpc.current["backpack"] = [{"slot": i} for i in range(16)]
                self.rpc.current["offer"]["items"][0]["entry"] = item
                options = {**BUY, "item_id": item}
                self.assertEqual(self.run_action(**options)["phase"], "confirmed")
                with self.assertRaisesRegex(m.Error, "no_repeat"):
                    self.run_action(**options)
                self.assertEqual(len(self.rpc.mutations()), 1)
        self.setUp()
        self.rpc.current["offer"]["items"][0]["entry"] = 999999
        with self.assertRaisesRegex(m.Error, "starter_catalog"):
            self.run_action(**{**BUY, "item_id": 999999})
        self.assertEqual(self.rpc.mutations(), [])
        self.assertFalse(self.path.exists())

    def test_already_owned_tool_cannot_be_purchased_and_definite_block_can_use_new_request(self):
        self.rpc.current["offer"]["items"][0]["entry"] = 6256
        with self.assertRaisesRegex(m.Error, "already_owned"):
            self.run_action(**{**BUY, "item_id": 6256})
        self.assertEqual(self.rpc.mutations(), [])
        self.rpc.current = initial_state()
        self.rpc.status = "BLOCKED"
        self.assertEqual(self.run_action(**BUY)["phase"], "blocked")
        prior = self.rpc.mutations()[-1][1]["requestId"]
        self.rpc.status = "CONFIRMED"
        self.assertEqual(self.run_action(**BUY)["phase"], "confirmed")
        self.assertNotEqual(self.rpc.mutations()[-1][1]["requestId"], prior)

    def test_malformed_full_request_or_backup_is_rejected_before_any_new_rpc(self):
        self.run_action("equip", **EQUIP)
        original = self.saved()
        variants = []
        missing = copy.deepcopy(original)
        missing["attempts"][0]["request"].pop("itemGuid")
        variants.append(missing)
        backup = copy.deepcopy(original)
        backup["backup"]["originalWeaponGuid"] = OTHER_ORIGINAL
        variants.append(backup)
        terminal = copy.deepcopy(original)
        terminal["attempts"][0].pop("operation")
        variants.append(terminal)
        self.rpc.calls = []
        for value in variants:
            self.path.write_text(json.dumps(value))
            with self.assertRaises(m.Error):
                self.run_action("restore")
        self.assertEqual(self.rpc.calls, [])

    def test_bundle_extended_cost_unavailable_or_expired_offer_never_sends_buy(self):
        for changes in ({"buyCount": 2}, {"extendedCost": 1}, {"availableCount": 0}, {"priceCopper": 0}):
            with self.subTest(changes=changes):
                self.rpc.current = initial_state()
                self.rpc.current["offer"]["items"][0].update(changes)
                with self.assertRaises(m.Error):
                    self.run_action(**BUY)
        self.rpc.current = initial_state()
        self.rpc.current["offer"]["expiresInMs"] = "0"
        with self.assertRaises(m.Error):
            self.run_action(**BUY)
        self.assertEqual(self.rpc.mutations(), [])

    def test_spoofed_actor_and_nonboolean_evidence_cannot_mutate(self):
        for change in ({"selfGuid": "3"}, {"selfGuid": "7"}, {"active": "false"}, {"sessionId": "bad session"}):
            with self.subTest(change=change):
                self.rpc.current = {**initial_state(), **change}
                with self.assertRaises(m.Error):
                    self.run_action(**BUY)
        self.assertEqual(self.rpc.mutations(), [])

    def test_failed_private_reservation_write_prevents_send(self):
        with m.Journal(self.path, URL, 6) as journal, mock.patch.object(journal, "save", side_effect=OSError("disk failure")):
            with self.assertRaises(OSError):
                m.Worker(self.rpc, 6, journal).mutation("buy", **BUY)
        self.assertEqual(self.rpc.mutations(), [])

    def test_private_identity_modes_symlinks_and_concurrent_lock_fail_closed(self):
        self.run_action(**BUY)
        with self.assertRaises(m.Error):
            with m.Journal(self.path, URL, 7):
                pass
        with m.Journal(self.path, URL, 6):
            with self.assertRaises(OSError):
                with m.Journal(self.path, URL, 6):
                    pass
        self.path.chmod(0o644)
        with self.assertRaises(m.Error):
            with m.Journal(self.path, URL, 6):
                pass
        self.path.chmod(0o600)
        target = self.path.with_name("target.json")
        self.path.rename(target)
        self.path.symlink_to(target)
        with self.assertRaises(OSError):
            with m.Journal(self.path, URL, 6):
                pass

    def test_directory_symlink_or_nonprivate_directory_is_rejected(self):
        actual = self.root / "actual"
        actual.mkdir(mode=0o700)
        alias = self.root / "alias"
        alias.symlink_to(actual, target_is_directory=True)
        with self.assertRaises(m.Error):
            with m.Journal(alias / "state.json", URL, 6):
                pass
        actual.chmod(0o755)
        with self.assertRaises(m.Error):
            with m.Journal(actual / "state.json", URL, 6):
                pass

    def test_cli_requires_explicit_identity_and_explicit_mutation_arguments(self):
        for args in (["state"], ["state", "--rpc-url", URL],
                     ["buy", "--rpc-url", URL, "--character-guid", "6"]):
            with self.subTest(args=args), mock.patch.object(m.sys, "stderr", new_callable=io.StringIO):
                with self.assertRaises(SystemExit):
                    m.main(args)

    def test_readonly_commands_never_mutate_or_create_private_journal(self):
        for command in ("state", "discover", "list"):
            argv = [command, "--rpc-url", URL, "--character-guid", "6", "--journal", str(self.path)]
            if command == "list":
                argv.extend(["--vendor-guid", VENDOR])
            with self.subTest(command=command), mock.patch.object(m, "RPC", return_value=self.rpc), \
                    mock.patch.object(m.sys, "stdout", new_callable=io.StringIO) as output:
                self.assertEqual(m.main(argv), 0)
                self.assertIsInstance(json.loads(output.getvalue()), dict)
            self.assertEqual(self.rpc.mutations(), [])
            self.assertFalse(self.path.exists())

    def test_server_rejection_requires_real_failure_receipt_and_can_use_new_explicit_request(self):
        self.rpc.status = "SERVER_REJECTED"
        self.rpc.after_mutation = lambda state: state["operation"].update(serverFailure=13)
        self.assertEqual(self.run_action(**BUY)["phase"], "server_rejected")
        prior = self.rpc.mutations()[-1][1]["requestId"]
        self.rpc.status, self.rpc.after_mutation = "CONFIRMED", None
        self.assertEqual(self.run_action(**BUY)["phase"], "confirmed")
        self.assertNotEqual(self.rpc.mutations()[-1][1]["requestId"], prior)

    def test_equipment_receipt_must_match_original_guid_baseline_and_actual_hand(self):
        for changed, phase in (({"originalWeaponGuid": OTHER_ORIGINAL}, "receipt_mismatch"),
                               ({"baselineBackpackRevision": "12"}, "receipt_mismatch"),
                               ({"equipmentConfirmed": False}, "outcome_unconfirmed")):
            with self.subTest(changed=changed):
                self.setUp()
                self.rpc.after_mutation = lambda state, changed=changed: state["operation"].update(changed)
                self.assertEqual(self.run_action("equip", **EQUIP)["phase"], phase)
                self.assertFalse(self.saved()["backup"]["confirmed"])
                self.run_action("equip", **EQUIP)
                self.assertEqual(len(self.rpc.mutations()), 1)

    def test_loopback_url_exactness_and_all_legal_ports(self):
        for port in (1, 80, 8191, 65535):
            self.assertEqual(m.rpc_url(f"http://127.0.0.1:{port}/rpc"), f"http://127.0.0.1:{port}/rpc")
        for url in ("http://localhost:8191/rpc", "http://127.0.0.1:0/rpc", "http://127.0.0.1:65536/rpc",
                    "https://127.0.0.1:8191/rpc", "http://127.0.0.1:8191/rpc?x=1", "http://user@127.0.0.1:8191/rpc",
                    "http://127.0.0.1:08191/rpc", "http://127.0.0.1:8191/rpc#x", "http://[::1]:8191/rpc"):
            with self.subTest(url=url), self.assertRaises(m.Error):
                m.rpc_url(url)

    def test_http_disables_proxy_and_redirect_and_rejects_ambiguous_json(self):
        class Response:
            def __enter__(self): return self
            def __exit__(self, *args): pass
            def read(self, bound): return b'{"jsonrpc":"2.0","id":1,"id":1,"result":{}}'
        opener = mock.Mock()
        opener.open.return_value = Response()
        with mock.patch.object(m.urllib.request, "build_opener", return_value=opener) as build:
            with self.assertRaises(m.Error):
                m.RPC(URL).call("buddy-supplies.state", {})
        self.assertEqual(build.call_args.args[0].proxies, {})
        with self.assertRaises(m.Error):
            build.call_args.args[1].redirect_request(None)
        with self.assertRaises(m.Error):
            m.RPC(URL, opener).call("buddy-professions.attempt", {})


if __name__ == "__main__":
    unittest.main()
