"""Starter pickup choices and exact query times through the real Lua bridge."""

import copy
from decimal import Decimal
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parent.parent
loader = importlib.machinery.SourceFileLoader(
    "starter_pickup_policy_runtime", str(ROOT / "dev/task-runtime"))
spec = importlib.util.spec_from_loader(loader.name, loader)
runtime = importlib.util.module_from_spec(spec)
loader.exec_module(runtime)
BINARY = Path(os.environ.get("BUDDY_LUAPOLICY_BINARY",
                 os.environ.get("BUDDY_LUAPOLICY_TEST_BINARY", ROOT / "data/bin/luapolicy")))
SESSION = "starter-pickup-session"


def exact_time(value):
    value = Decimal(value)
    text = format(value.copy_abs(), "f")
    whole, _, fraction = text.partition(".")
    return dict(negative=value < 0, whole=whole.lstrip("0") or "0",
                fraction=fraction.rstrip("0"))


def candidate(spawn=1, priority=0, distance=2, entry=823):
    return dict(guid=str(0xf130000000000000 | entry << 24 | spawn),
                priority=priority, distance=distance, entry=entry)


def offer(npc, quests, **fields):
    return dict(npcGuid=npc["guid"], revision="8", expiresInMs="60000", quests=quests) | fields


def facts(**fields):
    return dict(mode="starter_pickup", sessionId=SESSION, autonomous=False,
                neededEntry=823, activeQuestIds=[], allowedQuestIds=[783, 18, 3903],
                candidates=[candidate()], offerObjectKnown=True,
                offer=dict(npcGuid="0", revision="0", expiresInMs="0", quests=[]),
                reservations={}, querySignature=[SESSION, "9", "2"], queries=[]) | fields


def previous(npc, at=0, elapsed=60, signature=None):
    return dict(npc=npc["guid"], signature=[SESSION, "9", "2"] if signature is None else signature,
                at=exact_time(at), elapsed=exact_time(elapsed))


@unittest.skipUnless(BINARY.is_file(), "set BUDDY_LUAPOLICY_TEST_BINARY to the local compiled bridge")
class StarterPickupPolicyTests(unittest.TestCase):
    def setUp(self):
        self.bridge = runtime.Bridge(BINARY, ROOT / "modules")
        self.source = (ROOT / "behaviors/party-quests.lua").read_text()

    def choice(self, value, kind, npc=None, quest_id=None):
        result = self.bridge.evaluate(self.source, value)
        self.assertEqual(result.get("status"), "ready", result)
        selected = result["choice"]
        self.assertEqual(selected["kind"], kind, result)
        if npc is not None:
            self.assertEqual(selected.get("npcGuid"), npc["guid"], result)
        if quest_id is not None:
            self.assertEqual(selected.get("questId"), quest_id, result)
        return selected

    def invalid(self, value):
        result = self.bridge.evaluate(self.source, value)
        self.assertEqual(result.get("status"), "invalid", result)
        self.assertNotIn("choice", result)

    def test_intro_is_last_after_other_eligible_quest_ids_regardless_of_menu_order(self):
        npc = candidate()
        value = facts(allowedQuestIds=[783, 3903, 18, 7], offer=offer(npc, [
            dict(questId=783, available=True), dict(questId=3903, available=True),
            dict(questId=18, available=True), dict(questId=7, available=True)]))
        self.choice(value, "accept", npc, 7)
        value["activeQuestIds"] = [7]
        self.choice(value, "accept", npc, 18)
        value["activeQuestIds"] = [7, 18]
        self.choice(value, "accept", npc, 3903)
        value["activeQuestIds"] = [7, 18, 3903]
        self.choice(value, "accept", npc, 783)

    def test_only_actual_available_unconsumed_nonrepeatable_allowed_rows_can_accept(self):
        npc = candidate()
        value = facts(offer=offer(npc, [dict(questId=999, available=True),
            dict(questId=18, available=True, acceptRequested=True),
            dict(questId=3903, available=True, repeatable=True),
            dict(questId=783, available=False)]))
        self.choice(value, "offer", npc)
        value["offer"]["quests"].append(dict(questId="18", available=True))
        selected = self.choice(value, "accept", npc, 18)
        self.assertEqual(selected["offerRevision"], "8")

    def test_large_actual_quest_row_id_does_not_round_to_an_allowed_id(self):
        npc = candidate()
        value = facts(offer=offer(npc, [dict(questId="18446744073709551615", available=True),
                                      dict(questId=18, available=True)]))
        self.choice(value, "accept", npc, 18)

    def test_whole_menu_accept_reservation_requires_matching_session_npc_and_revision(self):
        npc = candidate()
        value = facts(offer=offer(npc, [dict(questId=18, available=True)]),
                      reservations={"accept:783": [SESSION, npc["guid"], "8"]})
        self.choice(value, "offer", npc)
        for index, replacement in ((0, "old-session"), (1, candidate(spawn=2)["guid"]), (2, "7")):
            with self.subTest(index=index):
                changed = copy.deepcopy(value)
                changed["reservations"]["accept:783"][index] = replacement
                self.choice(changed, "accept", npc, 18)
        value["reservations"] = {"claim:783": [SESSION, npc["guid"], "8"]}
        self.choice(value, "accept", npc, 18)

    def test_no_candidate_requests_only_entry_travel_autonomously_and_waits_manually(self):
        value = facts(candidates=[])
        selected = self.choice(value, "wait")
        self.assertEqual(selected["phase"], "waiting_for_observed_starter_questgiver_within_8yd")
        value["autonomous"] = True
        selected = self.choice(value, "travel")
        self.assertEqual(selected["entry"], 823)
        self.assertNotIn("npcGuid", selected)

    def test_query_tuple_orders_priority_before_previous_time_then_distance_then_exact_guid(self):
        a, b = candidate(spawn=2, distance=4), candidate(spawn=1, priority=1, distance=1)
        value = facts(candidates=[b, a], queries=[previous(a, 100), previous(b, -100)])
        self.choice(value, "offer", a)
        b["priority"] = 0
        self.choice(value, "offer", b)
        value["queries"] = [previous(a, 100), previous(b, 100)]
        self.choice(value, "offer", b)
        a["distance"] = b["distance"]
        self.choice(value, "offer", b)
        self.assertGreater(int(a["guid"]), 2**53)
        self.assertEqual(int(a["guid"]) - int(b["guid"]), 1)

    def test_uint64_previous_timestamps_differing_by_one_keep_oldest_before_distance(self):
        older, newer = candidate(spawn=2, distance=5), candidate(spawn=1, distance=1)
        first, second = 18446744073709551614, 18446744073709551615
        self.assertEqual(float(first), float(second))
        value = facts(candidates=[newer, older], queries=[previous(newer, second), previous(older, first)])
        self.choice(value, "offer", older)
        value["queries"] = [previous(newer, first), previous(older, second)]
        self.choice(value, "offer", newer)

    def test_signed_and_fractional_previous_times_order_exactly_against_unseen_zero(self):
        a, b = candidate(spawn=2, distance=5), candidate(spawn=1, distance=1)
        for first, second in (("2.001", "2.01"), ("-2.01", "-2.001"),
                              ("-1e308", "-1"), ("1e307", "1e308")):
            with self.subTest(first=first, second=second):
                self.choice(facts(candidates=[b, a], queries=[previous(a, first), previous(b, second)]),
                            "offer", a)
        self.choice(facts(candidates=[b, a], queries=[previous(a, -1)]), "offer", a)
        self.choice(facts(candidates=[b, a], queries=[previous(a, 1)]), "offer", b)

    def test_matching_signature_skips_only_elapsed_strictly_below_sixty_seconds(self):
        npc = candidate()
        for elapsed, kind in (("59.999999999999999999", "wait"), (60, "offer"),
                              ("60.000000000000000001", "offer"), (-1, "wait"),
                              ("-1e308", "wait"), ("1e308", "offer")):
            with self.subTest(elapsed=elapsed):
                selected = self.choice(facts(queries=[previous(npc, 0, elapsed)]), kind,
                                       npc if kind == "offer" else None)
                if kind == "wait":
                    self.assertEqual(selected["phase"], "waiting_for_new_offer_or_prerequisite_state")

    def test_smallest_finite_float_elapsed_survives_exact_fraction_encoding(self):
        npc = candidate()
        smallest = Decimal(float.fromhex("0x0.0000000000001p-1022"))
        self.assertEqual(len(exact_time(smallest)["fraction"]), 1074)
        self.choice(facts(queries=[previous(npc, 0, smallest)]), "wait")

    def test_changed_signature_uses_previous_time_but_does_not_read_unused_elapsed(self):
        npc = candidate()
        row = previous(npc, -10, 0, [SESSION, "8", "2"])
        row["elapsed"] = False
        self.choice(facts(queries=[row]), "offer", npc)
        row["signature"] = [SESSION, "9", "2"]
        self.invalid(facts(queries=[row]))

    def test_first_previous_row_controls_cooldown_and_order_without_replacing_it(self):
        a, b = candidate(spawn=2), candidate(spawn=1)
        rows = [previous(a, 20), previous(a, -100), previous(b, 10)]
        self.choice(facts(candidates=[a, b], queries=rows), "offer", b)
        rows = [previous(a, 0, 59), previous(a, 0, 60)]
        self.choice(facts(candidates=[a], queries=rows), "wait")

    def test_only_derived_elapsed_accepts_signed_infinity(self):
        npc = candidate()
        row = previous(npc, -1e308)
        row["elapsed"] = dict(infinite=True, negative=False)
        self.choice(facts(queries=[row]), "offer", npc)
        row["elapsed"] = dict(infinite=True, negative=True)
        self.choice(facts(queries=[row]), "wait")
        row["elapsed"] = dict(infinite=True, negative=False)
        row["at"] = dict(infinite=True, negative=False)
        self.invalid(facts(queries=[row]))
        row["at"] = exact_time(0)
        for malformed in (dict(infinite="yes",negative=False,whole="0",fraction=""),
                          dict(infinite=True,negative=False,whole="0"),
                          dict(infinite=True,negative=0)):
            row["elapsed"] = malformed
            self.invalid(facts(queries=[row]))

    def test_unused_other_npc_query_fields_are_not_eagerly_validated(self):
        npc = candidate()
        rows = [dict(npc=candidate(spawn=2)["guid"], signature=False, at=False, elapsed=False),
                dict(npc=False, signature=[False, False, False], at="invalid", elapsed=None)]
        self.choice(facts(queries=rows), "offer", npc)
        rows.append(previous(npc, 0, 59))
        self.choice(facts(queries=rows), "wait")

    def test_only_matching_live_offer_validates_rows_and_selected_revision(self):
        npc = candidate()
        for fields in (dict(offerObjectKnown=False),
                       dict(offer=offer(candidate(spawn=2), False, revision=False)),
                       dict(offer=offer(npc, False, revision=False, expiresInMs="0"))):
            with self.subTest(fields=fields):
                self.choice(facts(**fields), "offer", npc)
        self.invalid(facts(offer=offer(npc, False)))
        self.invalid(facts(offer=offer(npc, [dict(questId=False, available=False)])))
        self.invalid(facts(offer=offer(npc, [dict(questId=18, available=True)], revision=False)))
        self.choice(facts(offer=offer(npc, [dict(questId=999, available=True)], revision=False)), "offer", npc)

    def test_accept_does_not_validate_unneeded_query_signature_or_history(self):
        npc = candidate()
        value = facts(offer=offer(npc, [dict(questId=18, available=True)]),
                      querySignature=[SESSION, False, "2"], queries=[dict(npc=npc["guid"], at=False)])
        self.choice(value, "accept", npc, 18)
        value["offer"]["quests"] = []
        self.invalid(value)

    def test_malformed_used_time_and_selected_offer_facts_are_invalid(self):
        npc = candidate()
        malformed = [dict(negative=False, whole="00", fraction=""),
                     dict(negative=False, whole="0", fraction="10"),
                     dict(negative=True, whole="0", fraction=""),
                     dict(negative=False, whole="1" * 310, fraction=""),
                     dict(negative=False, whole="0", fraction="0" * 1074 + "1"),
                     dict(negative=False, whole="1", fraction="x"),
                     dict(negative=0, whole="1", fraction="")]
        for encoded in malformed:
            for field in ("at", "elapsed"):
                with self.subTest(field=field, encoded=encoded):
                    row = previous(npc)
                    row[field] = encoded
                    self.invalid(facts(queries=[row]))
        for fields in (dict(sessionId=""), dict(activeQuestIds=False),
                       dict(allowedQuestIds=[18, 18]), dict(candidates=False)):
            with self.subTest(fields=fields):
                self.invalid(facts(**fields))


if __name__ == "__main__":
    unittest.main()
