"""Starter routing and ordered probe decisions through the real Lua bridge."""

import copy
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parent.parent
loader = importlib.machinery.SourceFileLoader("starter_route_runtime", str(ROOT / "dev/task-runtime"))
spec = importlib.util.spec_from_loader(loader.name, loader)
runtime = importlib.util.module_from_spec(spec)
loader.exec_module(runtime)
BINARY = Path(os.environ.get("BUDDY_LUAPOLICY_BINARY",
                 os.environ.get("BUDDY_LUAPOLICY_TEST_BINARY", ROOT / "data/bin/luapolicy")))


def giver(entry=823, spawn=1):
    return dict(guid=str(0xf130000000000000 | entry << 24 | spawn), entry=entry)


def menu(entry=823, rows=None, **fields):
    return dict(npcGuid=giver(entry)["guid"], expiresInMs="60000", questsKnown=True,
                questsMissing=False, quests=[] if rows is None else rows) | fields


def facts(**fields):
    return dict(mode="starter_route", sessionId="starter-route-session", activeQuestIds=[],
                confirmedQuestIds=[], levelKnown=True, level=2, wolfComplete=False,
                probes=dict(intro=False, eagan=False, wolf=False),
                candidates=[giver(823), giver(197), giver(196)], offerObjectKnown=True,
                offer=dict(npcGuid="0", expiresInMs="0", questsKnown=False,
                           questsMissing=True, quests=[])) | fields


@unittest.skipUnless(BINARY.is_file(), "set BUDDY_LUAPOLICY_TEST_BINARY to the local compiled bridge")
class StarterRoutePolicyTests(unittest.TestCase):
    def setUp(self):
        self.bridge = runtime.Bridge(BINARY, ROOT / "modules")
        self.source = (ROOT / "behaviors/party-quests.lua").read_text()

    def route(self, value, entry, reward=0, allowed=(), events=()):
        result = self.bridge.evaluate(self.source, value)
        self.assertEqual(result.get("status"), "ready", result)
        selected = result["choice"]
        self.assertEqual(selected["neededEntry"], entry, result)
        self.assertEqual(selected["rewardQuestId"], reward, result)
        self.assertEqual(selected["allowedQuestIds"] or [], list(allowed), result)
        self.assertEqual(selected["probeEvents"] or [], list(events), result)
        self.assertEqual(set(selected), {"neededEntry", "rewardQuestId", "allowedQuestIds", "probeEvents"})
        return selected

    def invalid(self, value):
        result = self.bridge.evaluate(self.source, value)
        self.assertEqual(result.get("status"), "invalid", result)
        self.assertNotIn("choice", result)

    def test_initial_route_and_allowed_ids_keep_goal_and_prerequisite_distinct(self):
        self.route(facts(), 823, allowed=[18, 783, 3903, 5261])
        self.route(facts(level=1), 823, allowed=[783, 5261])
        self.route(facts(levelKnown=False, level=False), 823, allowed=[783, 5261])
        self.route(facts(confirmedQuestIds=[783]), 197, allowed=[7])
        self.route(facts(activeQuestIds=[7, 18, 3903]), 823)
        self.route(facts(activeQuestIds=[7, 18, 3903, 999]), 823)

    def test_reward_priority_is_intro_then_eagan_then_only_completed_own_wolves(self):
        self.route(facts(activeQuestIds=[33, 5261, 783], wolfComplete=True),
                   197, reward=783, allowed=[7])
        self.route(facts(activeQuestIds=[33, 5261], wolfComplete=True),
                   196, reward=5261, allowed=[33])
        self.route(facts(activeQuestIds=[7, 18, 33], wolfComplete=True),
                   196, reward=33, allowed=[33])
        self.route(facts(activeQuestIds=[7, 18, 33], objectiveCounts=[8, 8, 8, 8]),
                   823, allowed=[3903, 5261])
        self.route(facts(activeQuestIds=[7, 18], wolfComplete=True), 823, allowed=[3903, 5261])

    def test_current_probe_and_confirmation_evidence_choose_only_dialogue_routes(self):
        self.route(facts(activeQuestIds=[7], confirmedQuestIds=[5261]), 196, allowed=[33])
        self.route(facts(activeQuestIds=[7], probes=dict(intro=False, eagan=True, wolf=False)),
                   196, allowed=[33])
        self.route(facts(activeQuestIds=[7], confirmedQuestIds=[5261],
                         probes=dict(intro=False, eagan=True, wolf=True)),
                   823, allowed=[18, 3903])
        self.route(facts(activeQuestIds=[7], confirmedQuestIds=[33]), 823, allowed=[18, 3903])

    def test_fresh_empty_willem_menu_records_intro_probe_and_saved_probe_repeats_save(self):
        value = facts(offer=menu())
        self.route(value, 197, allowed=[7], events=["intro"])
        value["probes"]["intro"] = True
        self.route(value, 197, allowed=[7], events=["intro"])
        value["offerObjectKnown"] = False
        self.route(value, 197, allowed=[7])

    def test_original_sequential_intro_override_can_follow_a_selected_eagan_reward(self):
        value = facts(activeQuestIds=[5261], offer=menu())
        self.route(value, 197, reward=5261, allowed=[7], events=["intro"])
        value["offer"] = menu(rows=[dict(questId="783", available=True)])
        self.route(value, 196, reward=5261, allowed=[33])
        value["probes"]["intro"] = True
        self.route(value, 197, reward=5261, allowed=[7])

    def test_eagan_probe_overrides_saved_wolf_probe_and_repeats_same_probe_event(self):
        value = facts(activeQuestIds=[7, 18], probes=dict(intro=False, eagan=True, wolf=True),
                      offer=menu())
        self.route(value, 196, allowed=[33], events=["eagan"])
        # The event means set eagan=current and clear wolf before any later
        # event is dispatched; it is not a reward/completion confirmation.
        value["probes"]["wolf"] = False
        self.route(value, 196, allowed=[33], events=["eagan"])

    def test_empty_eagan_menu_records_wolf_probe_but_never_confirms_wolf_reward(self):
        value = facts(activeQuestIds=[7, 18], confirmedQuestIds=[5261], offer=menu(196))
        self.route(value, 823, allowed=[3903], events=["wolf"])
        value["probes"]["wolf"] = True
        self.route(value, 823, allowed=[3903], events=["wolf"])
        value["activeQuestIds"].append(33)
        self.route(value, 823, allowed=[3903])

    def test_available_prerequisite_or_followup_menu_row_stops_its_probe_scan(self):
        self.route(facts(offer=menu(rows=[dict(questId="783", available=True), False])),
                   823, allowed=[18, 783, 3903, 5261])
        for qid in ("5261", "3903"):
            with self.subTest(qid=qid):
                self.route(facts(activeQuestIds=[7], offer=menu(rows=[dict(questId=qid, available=True)])),
                           823, allowed=[18, 3903, 5261])
        self.route(facts(activeQuestIds=[7], confirmedQuestIds=[5261],
                         offer=menu(196, [dict(questId="33", available=True)])), 196, allowed=[33])

    def test_missing_quests_defaults_to_empty_only_for_intro_probe(self):
        for known in (False, True):
            with self.subTest(questsKnown=known):
                self.route(facts(offer=menu(questsKnown=known, questsMissing=True, quests=False)),
                           197, allowed=[7], events=["intro"])
                self.route(facts(activeQuestIds=[7], offer=menu(questsKnown=known, questsMissing=True, quests=False)),
                           823, allowed=[18, 3903, 5261])
                self.route(facts(activeQuestIds=[7], confirmedQuestIds=[5261],
                                 offer=menu(196, questsKnown=known, questsMissing=True, quests=False)),
                           196, allowed=[33])
        self.route(facts(offer=menu(questsKnown=False, quests=False)),
                   823, allowed=[18, 783, 3903, 5261])

    def test_unconsulted_object_npc_expiry_and_rows_are_not_eagerly_validated(self):
        bad = dict(npcGuid=False, expiresInMs=False, questsKnown=True, questsMissing=False, quests=False)
        self.route(facts(activeQuestIds=[7, 18, 3903], offer=bad), 823)
        self.route(facts(activeQuestIds=[783], offer=bad), 197, reward=783, allowed=[7])
        self.route(facts(offerObjectKnown=False, offer=bad), 823, allowed=[18, 783, 3903, 5261])
        self.route(facts(offer=menu(197, False, expiresInMs=False)), 823, allowed=[18, 783, 3903, 5261])
        self.route(facts(offer=menu(rows=False, expiresInMs="0")), 823, allowed=[18, 783, 3903, 5261])
        self.invalid(facts(offer=bad))
        self.invalid(facts(offer=menu(expiresInMs=False)))

    def test_any_short_circuit_ignores_later_malformed_id_but_reads_earlier_unavailable_id(self):
        cases = [({}, 823, "783", 823, [18, 783, 3903, 5261]),
                 (dict(activeQuestIds=[7]), 823, "5261", 823, [18, 3903, 5261]),
                 (dict(activeQuestIds=[7], confirmedQuestIds=[5261]), 196, "33", 196, [33])]
        for fields, entry, qid, needed, allowed in cases:
            with self.subTest(entry=entry, qid=qid):
                value = facts(**fields, offer=menu(entry, [dict(questId=qid, available=True),
                                                          dict(questId=False, available=False)]))
                self.route(value, needed, allowed=allowed)
                value["offer"]["quests"].reverse()
                self.invalid(value)
                value["offer"]["quests"] = [dict(questId=qid, available=False), dict(questId=False, available=False)]
                self.invalid(value)

    def test_nonobject_rows_are_skipped_and_full_uint64_ids_never_match_prerequisites(self):
        self.route(facts(offer=menu(rows=[False, dict(questId="18446744073709551615", available=True)])),
                   197, allowed=[7], events=["intro"])
        self.route(facts(offer=menu(rows=[False, dict(questId="783", available=True)])),
                   823, allowed=[18, 783, 3903, 5261])

    def test_probe_predicates_keep_active_confirmed_and_level_conditions_independent(self):
        self.route(facts(activeQuestIds=[7, 18], confirmedQuestIds=[33], offer=menu()), 823, allowed=[3903])
        self.route(facts(activeQuestIds=[7, 18, 5261], offer=menu()), 196, reward=5261, allowed=[33])
        self.route(facts(activeQuestIds=[7, 18, 3903], offer=menu(196)), 823)
        self.route(facts(activeQuestIds=[7, 18], confirmedQuestIds=[5261], levelKnown=False, level=False,
                         offer=menu(196)), 823, events=["wolf"])

    def test_sparse_active_and_confirmed_extras_preserve_fixed_prerequisite_routes_and_override(self):
        extras = [99000, 4294967295]
        self.route(facts(activeQuestIds=[7] + extras, confirmedQuestIds=[5261] + extras),
                   196, allowed=[33])
        self.route(facts(activeQuestIds=[7] + extras, confirmedQuestIds=[33] + extras),
                   823, allowed=[18, 3903])
        self.route(facts(activeQuestIds=extras, confirmedQuestIds=[783] + extras), 197, allowed=[7])
        self.route(facts(activeQuestIds=[7, 33] + extras, wolfComplete=True),
                   196, reward=33, allowed=[33])
        value = facts(activeQuestIds=[5261] + extras, confirmedQuestIds=extras, offer=menu())
        self.route(value, 197, reward=5261, allowed=[7], events=["intro"])
        value["offer"] = menu(rows=[dict(questId="783", available=True)])
        self.route(value, 196, reward=5261, allowed=[33])

    def test_sparse_candidate_entries_are_irrelevant_to_actual_starter_giver_membership(self):
        irrelevant = [giver(99000), giver(4294967295)]
        value = facts(candidates=[giver(823), giver(197), giver(196)] + irrelevant)
        for npc in irrelevant:
            with self.subTest(entry=npc["entry"]):
                value["offer"] = dict(npcGuid=npc["guid"], expiresInMs=False,
                                      questsKnown=True, questsMissing=False, quests=False)
                self.route(value, 823, allowed=[18, 783, 3903, 5261])
        value["offer"] = menu()
        self.route(value, 197, allowed=[7], events=["intro"])
        value["candidates"] = irrelevant + [copy.deepcopy(irrelevant[0])]
        self.route(value, 823, allowed=[18, 783, 3903, 5261])

    def test_sparse_route_ids_keep_numeric_uint32_validation_and_duplicate_refusal(self):
        for field in ("activeQuestIds", "confirmedQuestIds"):
            for rows in ([99000, 99000], [4294967295, 4294967295], [4294967296], [0], ["99000"]):
                with self.subTest(field=field, rows=rows):
                    self.invalid(facts(**{field: rows}))
        for entry in (-1, 4294967296, "99000"):
            with self.subTest(entry=entry):
                self.invalid(facts(candidates=[dict(guid=giver()["guid"], entry=entry)]))

    def test_core_invalid_facts_and_conflicting_candidate_identity_are_rejected(self):
        for fields in (dict(sessionId=""), dict(activeQuestIds=False), dict(activeQuestIds=[7, 7]),
                       dict(confirmedQuestIds=[1, 2, 3, 4]), dict(levelKnown=True, level=0),
                       dict(levelKnown=True, level=81), dict(probes=False), dict(candidates=False)):
            with self.subTest(fields=fields):
                self.invalid(facts(**fields))
        npc = giver()
        self.invalid(facts(candidates=[npc, dict(guid=npc["guid"], entry=196)]))
        self.route(facts(candidates=[npc, copy.deepcopy(npc)]), 823, allowed=[18, 783, 3903, 5261])


if __name__ == "__main__":
    unittest.main()
