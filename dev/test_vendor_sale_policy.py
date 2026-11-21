"""Editable vendor decisions through the real Lua bridge and offline receipts."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location("sale_town_fixtures", ROOT / "dev/test_town_maintenance.py")
town = importlib.util.module_from_spec(spec)
spec.loader.exec_module(town)
m = town.m
BINARY = os.environ.get("BUDDY_LUAPOLICY_TEST_BINARY", os.environ.get("BUDDY_LUAPOLICY_BINARY"))
IDENTITY = ("sessionId", "selfGuid", "revision", "inventoryRevision", "copperKnown", "copper", "copperRevision")
OTHER_VENDOR = town.VENDOR + 1


def policy(body):
    return "return {decide=function(input)\n" + body + "\nend}\n"


IDENTITY_RESULT = """
local result={sessionId=input.sessionId,selfGuid=input.selfGuid,revision=input.revision,
    inventoryRevision=input.inventoryRevision,copperKnown=input.copperKnown,
    copper=input.copper,copperRevision=input.copperRevision}
"""
PROPOSAL = IDENTITY_RESULT + """
result.action="sell"
result.source=input.candidates[1].source
result.vendorGuid=input.vendors[1].guid
result.unitSellPriceCopper=input.candidates[1].unitSellPriceCopper
"""
HIGHER_POLICY = policy(IDENTITY_RESULT + """
if #input.candidates==0 then result.action="complete"; result.reason="no_more_sales"; return result end
local selected=input.candidates[1]
for _,candidate in ipairs(input.candidates) do
    local source=candidate.source
    if source.bag>selected.source.bag or source.bag==selected.source.bag and source.slot>selected.source.slot then
        selected=candidate
    end
end
local vendor=input.vendors[1]
for _,candidate in ipairs(input.vendors) do
    if candidate.distance>vendor.distance then vendor=candidate end
end
result.action="sell"; result.source=selected.source; result.vendorGuid=vendor.guid
result.unitSellPriceCopper=selected.unitSellPriceCopper
return result
""")


class RecordingBridge:
    def __init__(self):
        self.delegate = m.runtime.Bridge(BINARY, ROOT / "modules")
        self.inputs, self.sources = [], []

    def evaluate(self, source, value):
        self.sources.append(source)
        self.inputs.append(copy.deepcopy(value))
        return self.delegate.evaluate(source, value)


class VendorRPC(town.FakeRPC):
    def __init__(self):
        super().__init__()
        # Isolate disposal from the separately tested bag equipment policy.
        self.current["inventoryBags"][0]["slots"][2] = {"slot": 2}
        self.current["items"] = self.current["items"][1:]
        self.vendors, self.discoveries, self.on_discovery = [(1, town.VENDOR)], 0, None

    def call(self, method, params):
        if method == "buddy-inventory.discover":
            self.discoveries += 1
            if self.on_discovery:
                self.on_discovery(self.discoveries, params)
        result = super().call(method, params)
        if method == "buddy-inventory.discover":
            result["nearby"] = [{"interactable": True, "distance": distance, "creature": {
                "guid": str(guid), "entry": guid >> 24 & 0xffffff, "present": True,
                "mapKnown": True, "positionKnown": True, "aliveKnown": True, "alive": True,
                "healthKnown": True, "health": 100, "npcFlags": 128}} for distance, guid in self.vendors]
        return result


class VendorSalePolicyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not BINARY or not Path(BINARY).is_file():
            raise RuntimeError("set BUDDY_LUAPOLICY_TEST_BINARY to the compiled real Lua bridge")

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.path = self.root / "private/town.json"
        self.rpc, self.bridge = VendorRPC(), RecordingBridge()
        self.source = self.root / "vendor-sale.lua"
        self.source.write_text((ROOT / "behaviors/vendor-sale.lua").read_text())

    def worker(self, journal, bridge=True):
        return m.Worker(self.rpc, 6, journal, sleep=lambda _: None,
                        bridge=self.bridge if bridge else None, sale_policy=self.source)

    def two_sources(self):
        bag = self.rpc.current["inventoryBags"][0]
        higher = {"bag": 0, "slot": 5, "itemGuid": str(town.JUNK + 8),
                  "entry": 7071, "stackCount": 1, "metadataKnown": True}
        bag["slots"][5] = higher
        self.rpc.current["items"].insert(0, {"item": m.ref(higher), "metadataKnown": True,
            "safeJunkSaleEligible": True, "unitSellPriceCopper": 7})
        self.lower, self.higher = m.ref(bag["slots"][3]), m.ref(higher)
        bag["slots"].reverse()

    def choose(self, worker):
        state, vendors = worker.discovery()
        worker.ready(state)
        return worker.choose_sale(state, vendors)

    def test_default_source_vendor_and_credit_are_exact_and_durable_before_write(self):
        self.two_sources()
        self.rpc.vendors = [(3, OTHER_VENDOR), (1, town.VENDOR)]
        reserved = []

        def before(method, request):
            self.assertEqual(method, "buddy-inventory.sell")
            row = json.loads(self.path.read_text())["attempts"][-1]
            self.assertEqual(row["phase"], "reserved")
            self.assertEqual(row["request"], request)
            reserved.append(copy.deepcopy(row))

        self.rpc.before = before
        with m.Journal(self.path, town.URL, 6) as journal:
            worker = m.Worker(self.rpc, 6, journal, sleep=lambda _: None, bridge=self.bridge)
            result = worker.run()
            self.assertEqual(result["operations"], {"claim": 0, "equip": 0, "sell": 2})
        requests = [request for _, request in self.rpc.mutations()]
        self.assertEqual([(r["source"], r["vendorGuid"]) for r in requests],
                         [(self.lower, str(town.VENDOR)), (self.higher, str(town.VENDOR))])
        self.assertEqual([r["expectedUnitSellPriceCopper"] for r in requests], [4, 7])
        self.assertEqual([r["expectedCopperRevision"] for r in requests], ["7", "8"])
        self.assertEqual([r["expectedInventoryRevision"] for r in requests], ["11", "12"])
        self.assertEqual([r["baselineCopper"] for r in reserved], [50, 58])
        self.assertEqual(self.rpc.current["copper"], 65)
        with m.Journal(self.path, town.URL, 6) as journal:
            self.assertEqual([r["phase"] for r in journal.data["attempts"]], ["confirmed", "confirmed"])
            self.assertEqual([r["request"] for r in journal.data["attempts"]], requests)
            self.assertEqual([r["receipt"]["expectedCreditCopper"] for r in journal.data["attempts"]], [8, 7])

    def test_equal_distance_vendor_tie_keeps_full_decimal_guid_precision(self):
        with m.Journal(self.path, town.URL, 6) as journal:
            worker = self.worker(journal)
            state = worker.state()
            chosen = worker.choose_sale(state, [(1, OTHER_VENDOR), (1, town.VENDOR)])
            self.assertEqual(chosen["vendorGuid"], str(town.VENDOR))
            self.assertEqual(self.bridge.inputs[-1]["vendors"],
                             [{"guid": str(OTHER_VENDOR), "distance": 1}, {"guid": str(town.VENDOR), "distance": 1}])
        self.assertEqual(self.rpc.mutations(), [])

    def test_policy_edit_changes_actual_source_and_vendor_on_same_worker(self):
        self.two_sources()
        self.rpc.vendors = [(1, town.VENDOR), (3, OTHER_VENDOR)]
        with m.Journal(self.path, town.URL, 6) as journal:
            worker = self.worker(journal)
            first = self.choose(worker)
            self.source.write_text(HIGHER_POLICY)
            second = self.choose(worker)
            self.assertEqual((first["source"], first["vendorGuid"]), (self.lower, str(town.VENDOR)))
            self.assertEqual((second["source"], second["vendorGuid"]), (self.higher, str(OTHER_VENDOR)))
            self.assertEqual(self.bridge.inputs[0], self.bridge.inputs[1])
            self.assertNotEqual(self.bridge.sources[0], self.bridge.sources[1])
            self.assertEqual(worker.run()["operations"]["sell"], 2)
        self.assertEqual([(r["source"], r["vendorGuid"]) for _, r in self.rpc.mutations()],
                         [(self.higher, str(OTHER_VENDOR)), (self.lower, str(OTHER_VENDOR))])

    def test_explicit_complete_or_blocked_never_uses_python_selection_fallback(self):
        for action in ("complete", "blocked"):
            with self.subTest(action=action):
                self.source.write_text(policy(IDENTITY_RESULT +
                    f'result.action="{action}"; result.reason="policy_decision"; return result'))
                with m.Journal(self.root / action / "town.json", town.URL, 6) as journal:
                    result = self.worker(journal).run()
                    self.assertEqual((result["phase"], result["reason"]), (action, "policy_decision"))
                    self.assertEqual(journal.data["attempts"], [])
                self.assertEqual(self.rpc.mutations(), [])

    def test_invalid_identity_source_vendor_or_price_never_reserves_or_writes(self):
        changes = {
            "not_table": 'return "sell"',
            "missing_identity": 'return {action="sell"}',
            "wrong_session": 'result.sessionId="another-body"; return result',
            "wrong_character": 'result.selfGuid="7"; return result',
            "wrong_revision": 'result.revision="3"; return result',
            "wrong_inventory_revision": 'result.inventoryRevision="12"; return result',
            "wrong_copper_known": 'result.copperKnown=false; return result',
            "numeric_copper_known": 'result.copperKnown=1; return result',
            "wrong_copper": 'result.copper=51; return result',
            "wrong_copper_revision": 'result.copperRevision="8"; return result',
            "unobserved_guid": 'result.source.itemGuid="4611686018427390000"; return result',
            "unobserved_entry": 'result.source.entry=result.source.entry+1; return result',
            "unobserved_slot": 'result.source.slot=15; return result',
            "stack_changed": 'result.source.stackCount=1; return result',
            "boolean_source_bag": 'result.source.bag=false; return result',
            "unobserved_vendor": f'result.vendorGuid="{OTHER_VENDOR}"; return result',
            "numeric_vendor": f'result.vendorGuid={town.VENDOR}; return result',
            "wrong_price": 'result.unitSellPriceCopper=5; return result',
            "boolean_price": 'result.unitSellPriceCopper=true; return result',
            "extra_source_field": 'result.source.extra=true; return result',
            "extra_output_field": 'result.extra=true; return result',
            "invalid_complete": 'result.action="complete"; result.reason=""; return result',
            "runtime_error": 'error("deliberate invalid policy")',
        }
        for name, body in changes.items():
            with self.subTest(name=name):
                self.rpc = VendorRPC()
                self.source.write_text(policy(PROPOSAL + body))
                with m.Journal(self.root / name / "town.json", town.URL, 6) as journal:
                    with self.assertRaises(m.Error):
                        self.worker(journal).run()
                    self.assertEqual(journal.data["attempts"], [])
                self.assertEqual(self.rpc.mutations(), [])

    def test_selected_vendor_is_kept_when_another_becomes_nearer_before_write(self):
        def fresh(number, params):
            if number == 2:
                self.assertEqual(params, {"vendorGuid": str(town.VENDOR)})
                self.rpc.vendors = [(0.25, OTHER_VENDOR), (1, town.VENDOR)]
        self.rpc.on_discovery = fresh
        with m.Journal(self.path, town.URL, 6) as journal:
            self.assertEqual(self.worker(journal).run()["operations"]["sell"], 1)
        self.assertEqual(self.rpc.mutations()[0][1]["vendorGuid"], str(town.VENDOR))

    def test_selected_vendor_disappearing_never_substitutes_another_vendor(self):
        def fresh(number, _params):
            if number == 2:
                self.rpc.vendors = [(0.25, OTHER_VENDOR)]
        self.rpc.on_discovery = fresh
        with m.Journal(self.path, town.URL, 6) as journal:
            with self.assertRaises(m.Error):
                self.worker(journal).run()
            self.assertEqual(journal.data["attempts"], [])
        self.assertEqual(self.rpc.mutations(), [])

    def test_changed_fresh_identity_source_or_price_blocks_before_reservation(self):
        def change_source(state, **changes):
            state["inventoryBags"][0]["slots"][3].update(changes)
            state["items"][0]["item"].update(changes)

        changes = {
            "session": lambda s: s.update(sessionId="new-body"),
            "character": lambda s: s.update(selfGuid="7"),
            "revision": lambda s: s.update(revision="3"),
            "inventory_revision": lambda s: s.update(inventoryRevision="12"),
            "copper_known": lambda s: s.update(copperKnown=False),
            "copper": lambda s: s.update(copper=51),
            "copper_revision": lambda s: s.update(copperRevision="8"),
            "source": lambda s: change_source(s, itemGuid=str(town.JUNK + 9)),
            "source_entry": lambda s: change_source(s, entry=7071),
            "source_stack": lambda s: change_source(s, stackCount=1),
            "price": lambda s: s["items"][0].update(unitSellPriceCopper=5),
        }
        for name, change in changes.items():
            with self.subTest(name=name):
                self.rpc = VendorRPC()
                def fresh(number, _params, change=change):
                    if number == 2:
                        change(self.rpc.current)
                self.rpc.on_discovery = fresh
                with m.Journal(self.root / name / "town.json", town.URL, 6) as journal:
                    with self.assertRaises(m.Error):
                        self.worker(journal).run()
                    self.assertEqual(journal.data["attempts"], [])
                self.assertEqual(self.rpc.mutations(), [])

    def test_uncertain_credit_survives_policy_edit_and_reopen_without_redecision_or_resend(self):
        self.two_sources()
        self.rpc.ambiguous = True
        self.rpc.after = lambda _, receipt: receipt.update(copperConfirmed=False)
        with m.Journal(self.path, town.URL, 6) as journal:
            self.assertEqual(self.worker(journal).run()["phase"], "transport_unknown")
            original = copy.deepcopy(journal.data["attempts"][0])
        self.source.write_text(HIGHER_POLICY)
        self.bridge = RecordingBridge()
        with m.Journal(self.path, town.URL, 6) as journal:
            self.assertEqual(self.worker(journal).run()["phase"], "outcome_unconfirmed")
            self.assertEqual(len(journal.data["attempts"]), 1)
            self.assertEqual(journal.data["attempts"][0]["request"], original["request"])
        self.assertEqual(self.bridge.inputs, [])
        self.assertEqual(len(self.rpc.mutations()), 1)
        self.rpc.current["operation"]["copperConfirmed"] = True
        self.rpc.ambiguous = False
        with m.Journal(self.path, town.URL, 6) as journal:
            self.assertEqual(self.worker(journal).reconcile()["phase"], "confirmed")
            self.assertEqual(journal.data["attempts"][0]["request"], original["request"])
        self.assertEqual(len(self.rpc.mutations()), 1)

    def test_missing_evaluator_has_no_native_fallback(self):
        with m.Journal(self.path, town.URL, 6) as journal:
            with patch.dict(os.environ, {"BUDDY_LUAPOLICY_BINARY": str(self.root / "absent-bridge"),
                                        "BUDDY_LUAPOLICY_TEST_BINARY": str(self.root / "absent-bridge")}):
                with self.assertRaises(m.Error):
                    self.worker(journal, bridge=False).run()
            self.assertEqual(journal.data["attempts"], [])
        self.assertEqual(self.rpc.mutations(), [])

    def test_160_sources_and_96_vendors_fit_actual_bridge_value_budgets(self):
        state = self.rpc.current
        state["items"] = []
        for bag_index, bag in enumerate(state["inventoryBags"]):
            capacity = 16 if bag_index == 0 else 36
            if bag_index:
                bag.update(containerGuid=str(town.POLE_BAG + 1000 + bag_index), entry=41599,
                           metadataKnown=True, slotCount=capacity)
            bag["slots"] = []
            for slot in range(capacity):
                item = {"bag": bag_index, "slot": slot, "itemGuid": str(town.JUNK + 2000 + len(state["items"])),
                        "entry": 7070, "stackCount": 1, "metadataKnown": True}
                bag["slots"].append(item)
                state["items"].append({"item": m.ref(item), "metadataKnown": True,
                                       "safeJunkSaleEligible": True, "unitSellPriceCopper": 1})
        self.rpc.vendors = [(1, town.VENDOR + i) for i in range(96)]
        with m.Journal(self.path, town.URL, 6) as journal:
            selected = self.choose(self.worker(journal))
            self.assertEqual((selected["source"]["bag"], selected["source"]["slot"], selected["vendorGuid"]),
                             (0, 0, str(town.VENDOR)))
            self.assertEqual(journal.data["attempts"], [])
        facts = self.bridge.inputs[-1]
        self.assertEqual((len(facts["candidates"]), len(facts["vendors"])), (160, 96))
        self.assertEqual(set(facts), {*IDENTITY, "candidates", "vendors"})
        self.assertTrue(all(set(row) == {"source", "unitSellPriceCopper"} for row in facts["candidates"]))
        self.assertLess(len(json.dumps(facts, separators=(",", ":")).encode()), 32768)
        self.assertEqual(self.rpc.mutations(), [])


if __name__ == "__main__":
    unittest.main()
