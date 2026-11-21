"""Editable bag choices through the real Lua bridge and offline town RPCs."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location("bag_town_fixtures", ROOT / "dev/test_town_maintenance.py")
town = importlib.util.module_from_spec(spec)
spec.loader.exec_module(town)
m = town.m
BINARY = os.environ.get("BUDDY_LUAPOLICY_TEST_BINARY", os.environ.get("BUDDY_LUAPOLICY_BINARY"))


class RecordingBridge:
    def __init__(self):
        self.delegate = m.runtime.Bridge(BINARY, ROOT / "modules")
        self.inputs, self.sources = [], []

    def evaluate(self, source, value):
        self.sources.append(source)
        self.inputs.append(copy.deepcopy(value))
        return self.delegate.evaluate(source, value)


def policy(body):
    return "return {decide=function(input)\n" + body + "\nend}\n"


PROPOSAL = """
local result={action="equip",sessionId=input.sessionId,selfGuid=input.selfGuid,
    inventoryRevision=input.inventoryRevision,source=input.candidates[1].source,
    equipmentSlot=input.emptyEquipmentSlots[1]}
"""

HIGHER_POLICY = policy("""
local selected=input.candidates[1].source
for _,candidate in ipairs(input.candidates) do
    local source=candidate.source
    if source.bag>selected.bag or source.bag==selected.bag and source.slot>selected.slot then
        selected=source
    end
end
local equipment=input.emptyEquipmentSlots[1]
for _,slot in ipairs(input.emptyEquipmentSlots) do
    if slot>equipment then equipment=slot end
end
return {action="equip",sessionId=input.sessionId,selfGuid=input.selfGuid,
    inventoryRevision=input.inventoryRevision,source=selected,equipmentSlot=equipment}
""")


class BagEquipmentTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not BINARY or not Path(BINARY).is_file():
            raise RuntimeError("set BUDDY_LUAPOLICY_TEST_BINARY to the compiled real Lua bridge")

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.path = self.root / "private/town.json"
        self.rpc = town.FakeRPC()
        self.bridge = RecordingBridge()
        self.source = self.root / "bag-equipment.lua"
        self.source.write_text((ROOT / "behaviors/bag-equipment.lua").read_text())

    def worker(self, journal, bridge=True):
        return m.Worker(self.rpc, 6, journal, sleep=lambda _: None,
                        bridge=self.bridge if bridge else None, bag_policy=self.source)

    def two_bags(self):
        backpack = self.rpc.current["inventoryBags"][0]
        higher = {"bag": 0, "slot": 4, "itemGuid": str(town.POLE_BAG + 8),
                  "entry": 4498, "stackCount": 1, "metadataKnown": True}
        backpack["slots"][4] = higher
        self.rpc.current["items"].insert(0, {"item": m.ref(higher), "metadataKnown": True,
            "normalBag": True, "containerSlots": 8})
        self.lower = m.ref(backpack["slots"][2])
        self.higher = m.ref(higher)
        # Observation order must not stand in for the Lua choice.
        backpack["slots"].reverse()

    def choose(self, worker):
        state = worker.state()
        bags, slots = worker.ready(state)
        return worker.choose_bag(state, bags, slots)

    def occupy_first_equipment_slot(self):
        self.rpc.current["inventoryBags"][1].update(containerGuid=str(town.POLE_BAG + 100),
            entry=4498, metadataKnown=True, slotCount=8,
            slots=[{"bag": 1, "slot": i} for i in range(8)])

    def test_default_chooses_lowest_actual_source_and_reserves_exact_equips_before_write(self):
        self.two_bags()
        reserved = []

        def before(method, request):
            self.assertEqual(method, "buddy-inventory.equip")
            saved = json.loads(self.path.read_text())
            row = saved["attempts"][-1]
            self.assertEqual(row["phase"], "reserved")
            self.assertEqual(row["request"], request)
            reserved.append(copy.deepcopy(row))

        self.rpc.before = before
        with m.Journal(self.path, town.URL, 6) as journal:
            worker = m.Worker(self.rpc, 6, journal, sleep=lambda _: None, bridge=self.bridge)
            chosen = self.choose(worker)
            self.assertEqual((chosen["source"], chosen["equipmentSlot"]), (self.lower, 19))
            self.assertEqual(self.rpc.mutations(), [])
            result = worker.run(claims_only=True)
            self.assertEqual(result["operations"], {"claim": 0, "equip": 2, "sell": 0})
        requests = [request for _, request in self.rpc.mutations()]
        self.assertEqual([(r["source"], r["equipmentSlot"]) for r in requests],
                         [(self.lower, 19), (self.higher, 20)])
        self.assertTrue(all(r["expectedContainerGuid"] == "0" for r in requests))
        self.assertEqual([r["bagTemplate"] for r in reserved],
                         [{"entry": 41599, "containerSlots": 20}, {"entry": 4498, "containerSlots": 8}])
        self.assertEqual(requests[0]["expectedInventoryRevision"], "11")
        self.assertEqual(requests[1]["expectedInventoryRevision"], "12")
        with m.Journal(self.path, town.URL, 6) as journal:
            self.assertEqual([r["phase"] for r in journal.data["attempts"]], ["confirmed", "confirmed"])
            self.assertEqual([r["request"] for r in journal.data["attempts"]], requests)

    def test_default_worker_lazily_uses_configured_real_evaluator(self):
        with m.Journal(self.path, town.URL, 6) as journal:
            worker = m.Worker(self.rpc, 6, journal)
            self.assertIsNone(worker.bridge)
            with patch.dict(os.environ, {"BUDDY_LUAPOLICY_BINARY": BINARY,
                                        "BUDDY_LUAPOLICY_TEST_BINARY": BINARY}):
                chosen = self.choose(worker)
            self.assertIsInstance(worker.bridge, m.runtime.Bridge)
            self.assertEqual((chosen["source"], chosen["equipmentSlot"]),
                             (m.ref(self.rpc.current["inventoryBags"][0]["slots"][2]), 19))
            self.assertEqual(journal.data["attempts"], [])
        self.assertEqual(self.rpc.mutations(), [])

    def test_editing_lua_changes_source_and_destination_on_same_worker(self):
        self.two_bags()
        with m.Journal(self.path, town.URL, 6) as journal:
            worker = self.worker(journal)
            first = self.choose(worker)
            self.source.write_text(HIGHER_POLICY)
            second = self.choose(worker)
            self.assertEqual((first["source"], first["equipmentSlot"]), (self.lower, 19))
            self.assertEqual((second["source"], second["equipmentSlot"]), (self.higher, 22))
            self.assertEqual(self.bridge.inputs[0], self.bridge.inputs[1])
            self.assertNotEqual(self.bridge.sources[0], self.bridge.sources[1])
            result = worker.run(claims_only=True)
            self.assertEqual(result["operations"]["equip"], 2)
        self.assertEqual([(r["source"], r["equipmentSlot"]) for _, r in self.rpc.mutations()],
                         [(self.higher, 22), (self.lower, 21)])

    def test_lua_none_never_uses_python_selection_fallback(self):
        self.two_bags()
        self.source.write_text(policy('return {action="none",sessionId=input.sessionId,selfGuid=input.selfGuid,'
            'inventoryRevision=input.inventoryRevision,reason="keep_bags_for_later"}'))
        with m.Journal(self.path, town.URL, 6) as journal:
            worker = self.worker(journal)
            self.assertIsNone(self.choose(worker))
            self.assertEqual(worker.run(claims_only=True)["operations"]["equip"], 0)
            self.assertEqual(journal.data["attempts"], [])
        self.assertEqual(self.rpc.mutations(), [])

    def test_invalid_lua_or_unobserved_choice_never_reserves_or_writes(self):
        changes = {
            "not_table": 'return "equip"',
            "missing_identity": 'return {action="equip"}',
            "wrong_session": 'result.sessionId="another-body"; return result',
            "wrong_character": 'result.selfGuid="7"; return result',
            "wrong_revision": 'result.inventoryRevision="12"; return result',
            "unobserved_guid": 'result.source.itemGuid="4611686018427390000"; return result',
            "unobserved_entry": 'result.source.entry=result.source.entry+1; return result',
            "unobserved_slot": 'result.source.slot=15; return result',
            "stack_changed": 'result.source.stackCount=2; return result',
            "extra_source_field": 'result.source.extra=true; return result',
            "extra_output_field": 'result.extra=true; return result',
            "boolean_destination": 'result.equipmentSlot=true; return result',
            "occupied_destination": 'result.equipmentSlot=19; return result',
            "invalid_none": 'return {action="none",sessionId=input.sessionId,selfGuid=input.selfGuid,inventoryRevision=input.inventoryRevision}',
            "runtime_error": 'error("deliberate invalid policy")',
        }
        for name, body in changes.items():
            with self.subTest(name=name):
                self.rpc = town.FakeRPC()
                if name == "occupied_destination":
                    self.occupy_first_equipment_slot()
                self.source.write_text(policy(PROPOSAL + body))
                with m.Journal(self.root / name / "town.json", town.URL, 6) as journal:
                    with self.assertRaises(m.Error):
                        self.worker(journal).run(claims_only=True)
                    self.assertEqual(journal.data["attempts"], [])
                self.assertEqual(self.rpc.mutations(), [])

    def test_missing_evaluator_or_unreadable_policy_has_no_native_fallback(self):
        for missing in ("evaluator", "policy"):
            with self.subTest(missing=missing):
                if missing == "policy":
                    self.source.unlink()
                with m.Journal(self.root / missing / "town.json", town.URL, 6) as journal:
                    with patch.dict(os.environ, {"BUDDY_LUAPOLICY_BINARY": str(self.root / "absent-bridge"),
                                                "BUDDY_LUAPOLICY_TEST_BINARY": str(self.root / "absent-bridge")}):
                        with self.assertRaises(m.Error):
                            self.worker(journal, bridge=missing != "evaluator").run(claims_only=True)
                    self.assertEqual(journal.data["attempts"], [])
                self.assertEqual(self.rpc.mutations(), [])

    def test_uncertain_exact_equip_survives_policy_edit_and_reopen_without_resend(self):
        self.two_bags()
        self.rpc.ambiguous = True
        self.rpc.after = lambda _, receipt: receipt.update(inventoryConfirmed=False)
        with m.Journal(self.path, town.URL, 6) as journal:
            result = self.worker(journal).run(claims_only=True)
            self.assertEqual(result["phase"], "transport_unknown")
            original = copy.deepcopy(journal.data["attempts"][0])
        self.assertEqual((original["request"]["source"], original["request"]["equipmentSlot"]), (self.lower, 19))
        self.source.write_text(HIGHER_POLICY)
        self.bridge = RecordingBridge()
        with m.Journal(self.path, town.URL, 6) as journal:
            result = self.worker(journal).run(claims_only=True)
            self.assertEqual(result["phase"], "outcome_unconfirmed")
            self.assertEqual(len(journal.data["attempts"]), 1)
            self.assertEqual(journal.data["attempts"][0]["request"], original["request"])
        self.assertEqual(self.bridge.inputs, [])
        self.assertEqual(len(self.rpc.mutations()), 1)
        self.rpc.current["operation"]["inventoryConfirmed"] = True
        self.rpc.ambiguous = False
        with m.Journal(self.path, town.URL, 6) as journal:
            self.assertEqual(self.worker(journal).reconcile()["phase"], "confirmed")
            self.assertEqual(journal.data["attempts"][0]["request"], original["request"])
        self.assertEqual(len(self.rpc.mutations()), 1)

    def test_160_observed_candidates_fit_actual_bridge_value_budgets(self):
        state = self.rpc.current
        state["items"] = []
        for bag_index, bag in enumerate(state["inventoryBags"]):
            capacity = 16 if bag_index == 0 else 36
            if bag_index:
                bag.update(containerGuid=str(town.POLE_BAG + 1000 + bag_index), entry=41599,
                           metadataKnown=True, slotCount=capacity)
            bag["slots"] = []
            for slot in range(capacity):
                item = {"bag": bag_index, "slot": slot, "itemGuid": str(town.POLE_BAG + 2000 + len(state["items"])),
                        "entry": 4498, "stackCount": 1, "metadataKnown": True}
                bag["slots"].append(item)
                state["items"].append({"item": m.ref(item), "metadataKnown": True,
                                       "normalBag": True, "containerSlots": 8})
        with m.Journal(self.path, town.URL, 6) as journal:
            self.assertIsNone(self.choose(self.worker(journal)))
            self.assertEqual(journal.data["attempts"], [])
        facts = self.bridge.inputs[-1]
        self.assertEqual(len(facts["candidates"]), 160)
        self.assertEqual(facts["emptyEquipmentSlots"], [])
        self.assertEqual(set(facts), {"sessionId", "selfGuid", "inventoryRevision", "candidates", "emptyEquipmentSlots"})
        self.assertTrue(all(set(row) == {"source", "containerSlots"} for row in facts["candidates"]))
        self.assertLess(len(json.dumps(facts, separators=(",", ":")).encode()), 32768)
        self.assertEqual(self.rpc.mutations(), [])


if __name__ == "__main__":
    unittest.main()
