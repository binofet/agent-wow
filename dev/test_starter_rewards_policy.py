"""Starter handoff decisions through the real local Lua bridge; no game RPCs."""

import copy
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parent.parent
loader = importlib.machinery.SourceFileLoader(
    "starter_rewards_policy_runtime", str(ROOT / "dev/task-runtime"))
spec = importlib.util.spec_from_loader(loader.name, loader)
runtime = importlib.util.module_from_spec(spec)
loader.exec_module(runtime)
BINARY = Path(os.environ.get("BUDDY_LUAPOLICY_BINARY",
                 os.environ.get("BUDDY_LUAPOLICY_TEST_BINARY", ROOT / "data/bin/luapolicy")))
SESSION = "starter-rewards-session"


def candidate(entry=197, spawn=1, priority=0, distance=2):
    return dict(guid=str(0xf130000000000000 | entry << 24 | spawn),
                entry=entry, priority=priority, distance=distance)


def item(item_id=80, index=0, count=1):
    return dict(itemId=item_id, index=index, count=count)


def offer(npc, quest_id=783, choices=None, **fields):
    return dict(npcGuid=npc["guid"], questId=quest_id, revision="8",
                expiresInMs="60000", source="reward_offer", canComplete=True,
                rewardRequested=False, choices=[] if choices is None else choices) | fields


def facts(**fields):
    return dict(mode="starter_handoff", sessionId=SESSION, now=100, questId=783,
                ownQuestIds=[783], autonomous=False, candidates=[candidate()],
                turnInOffer=dict(npcGuid="0", questId=0, revision="0",
                                 expiresInMs="0", source="", canComplete=False,
                                 rewardRequested=False, choices=[]),
                classKnown=True, classId=5, claimReservation=False,
                lastMutation=dict(method="", questId=0, at=0), questLogRevision="9") | fields


@unittest.skipUnless(BINARY.is_file(), "set BUDDY_LUAPOLICY_TEST_BINARY to the local compiled bridge")
class StarterRewardsPolicyTests(unittest.TestCase):
    def setUp(self):
        self.bridge = runtime.Bridge(BINARY, ROOT / "modules")
        self.source = (ROOT / "behaviors/quest-rewards.lua").read_text()

    def evaluate(self, value, source=None):
        result = self.bridge.evaluate(self.source if source is None else source, value)
        self.assertEqual(result.get("status"), "ready", result)
        return result

    def choice(self, value, kind, npc=None, source=None):
        result = self.evaluate(value, source)
        selected = result["choice"]
        self.assertEqual(selected["kind"], kind, result)
        if npc is not None:
            self.assertEqual(selected["npcGuid"], npc["guid"], result)
            self.assertEqual(selected["questId"], value["questId"], result)
        return selected

    def claim(self, value, position, source=None):
        selected = self.choice(value, "claim_reward", source=source)
        current = value["turnInOffer"]
        self.assertEqual(selected["npcGuid"], current["npcGuid"])
        self.assertEqual(selected["questId"], current["questId"])
        self.assertEqual(selected["offerRevision"], current["revision"])
        self.assertEqual(selected["choicePosition"], position)
        if position:
            row = current["choices"][position - 1]
            if "choiceIndex" in selected:
                self.assertEqual(selected["choiceIndex"], row.get("index", 0))
            if "itemId" in selected:
                self.assertEqual(selected["itemId"], row["itemId"])
        return selected

    def test_only_an_active_supported_prerequisite_is_handled(self):
        self.choice(facts(ownQuestIds=[]), "ignore")
        self.choice(facts(questId=7, ownQuestIds=[7]), "ignore")
        for quest_id in (783, 5261, 33):
            entry = 197 if quest_id == 783 else 196
            npc = candidate(entry)
            self.choice(facts(questId=quest_id, ownQuestIds=[quest_id], candidates=[npc]),
                        "turn_in", npc)

    def test_prerequisite_handoff_selects_its_original_turnin_entry(self):
        mcbride, eagan, willem = candidate(197), candidate(196), candidate(823)
        for quest_id, expected in ((783, mcbride), (5261, eagan), (33, eagan)):
            with self.subTest(quest_id=quest_id):
                value = facts(questId=quest_id, ownQuestIds=[quest_id],
                              candidates=[willem, eagan, mcbride])
                self.choice(value, "turn_in", expected)

    def test_giver_selection_keeps_priority_distance_and_full_width_guid_ties(self):
        first, second = candidate(spawn=1), candidate(spawn=2)
        far = candidate(spawn=3, distance=3)
        deferred = candidate(spawn=4, priority=1, distance=1)
        value = facts(candidates=[deferred, far, second, first, candidate(196, distance=0)])
        self.choice(value, "turn_in", first)
        self.assertGreater(int(first["guid"]), 2**53)
        self.assertEqual(int(second["guid"]) - int(first["guid"]), 1)
        value["candidates"].remove(first)
        self.choice(value, "turn_in", second)

    def test_absent_original_giver_waits_manually_and_requests_only_entry_travel_autonomously(self):
        value = facts(candidates=[candidate(196)])
        selected = self.choice(value, "wait")
        self.assertEqual(selected["phase"], "waiting_for_observed_prerequisite_reward_giver")
        value["autonomous"] = True
        selected = self.choice(value, "travel")
        self.assertEqual(selected["entry"], 197)
        self.assertNotIn("npcGuid", selected)
        value.update(questId=33, ownQuestIds=[33], candidates=[candidate(197)])
        self.assertEqual(self.choice(value, "travel")["entry"], 196)

    def test_no_choice_and_single_actual_choice_do_not_need_class_or_preference(self):
        npc = candidate()
        value = facts(classKnown=False, classId=0, turnInOffer=offer(npc))
        self.claim(value, 0)
        value["turnInOffer"]["choices"] = [item(4294967295, index=5, count=4294967295)]
        self.claim(value, 1)
        value["turnInOffer"]["choices"] = [dict(itemId=123, count=1)]
        self.claim(value, 1)  # Proto JSON may omit the real index-zero field.

    def test_shared_wolf_preferences_select_actual_row_and_server_index_for_each_role(self):
        npc = candidate(196)
        choices = [item(6070, index=4), item(999, index=0), item(80, index=2)]
        for class_id, position in ((5, 3), (8, 3), (4, 1)):
            with self.subTest(class_id=class_id):
                value = facts(questId=33, ownQuestIds=[33], classId=class_id,
                              candidates=[npc], turnInOffer=offer(npc, 33, choices))
                self.claim(value, position)

    def test_unrelated_uint32_item_in_multirow_offer_does_not_narrow_existing_choices(self):
        npc = candidate(196)
        value = facts(questId=33, ownQuestIds=[33], candidates=[npc],
                      turnInOffer=offer(npc, 33, [item(4294967295), item(80, index=3)]))
        self.claim(value, 2)

    def test_editing_shared_quest33_preference_changes_starter_handoff(self):
        old = "[33]={[5]=80,[8]=80,[4]=6070}"
        self.assertEqual(self.source.count(old), 1)
        changed = self.source.replace(old, "[33]={[5]=6070,[8]=80,[4]=6070}", 1)
        npc = candidate(196)
        value = facts(questId=33, ownQuestIds=[33], candidates=[npc],
                      turnInOffer=offer(npc, 33, [item(80, index=2), item(6070, index=5)]))
        self.claim(value, 1)
        self.claim(value, 2, source=changed)

    def test_unverified_or_ambiguous_multirow_choice_waits_even_with_consumed_binding(self):
        npc = candidate(196)
        base = facts(questId=33, ownQuestIds=[33], candidates=[npc],
                     turnInOffer=offer(npc, 33, [item(80), item(6070, index=1)]),
                     claimReservation=[SESSION, npc["guid"], "8"])
        for fields in (dict(classKnown=False), dict(classId=1)):
            with self.subTest(fields=fields):
                value = copy.deepcopy(base) | fields
                selected = self.choice(value, "wait")
                self.assertEqual(selected["phase"], "waiting_for_verified_prerequisite_reward_choice")
        for choices in ([item(999), item(6070, index=1)], [item(80), item(80, index=1)]):
            with self.subTest(choices=choices):
                value = copy.deepcopy(base)
                value["turnInOffer"]["choices"] = choices
                selected = self.choice(value, "wait")
                self.assertEqual(selected["phase"], "waiting_for_verified_prerequisite_reward_choice")
        intro = facts(turnInOffer=offer(candidate(), choices=[item(80), item(6070, index=1)]))
        self.choice(intro, "wait")

    def test_wrong_npc_quest_expired_consumed_or_nonreward_offer_falls_back_to_query(self):
        npc = candidate()
        changes = [dict(npcGuid=candidate(spawn=2)["guid"]), dict(questId=5261),
                   dict(expiresInMs="0"), dict(rewardRequested=True),
                   dict(canComplete=False), dict(source="requirements")]
        for fields in changes:
            with self.subTest(fields=fields):
                value = facts(turnInOffer=offer(npc, choices=False, **fields))
                self.choice(value, "turn_in", npc)

    def test_same_claim_binding_never_replays_and_new_session_npc_or_revision_can_claim(self):
        npc = candidate()
        value = facts(turnInOffer=offer(npc, choices=[item(123)]),
                      claimReservation=[SESSION, npc["guid"], "8"])
        self.choice(value, "turn_in", npc)
        for index, replacement in ((0, "previous-body"), (1, candidate(spawn=2)["guid"]), (2, "7")):
            with self.subTest(index=index):
                changed = copy.deepcopy(value)
                changed["claimReservation"][index] = replacement
                self.claim(changed, 1)

    def test_malformed_claim_reservation_cannot_be_treated_as_an_unreserved_offer(self):
        npc = candidate()
        for bound in ([SESSION], [SESSION, npc["guid"]], True):
            with self.subTest(bound=bound):
                value = facts(turnInOffer=offer(npc), claimReservation=bound)
                result = self.bridge.evaluate(self.source, value)
                self.assertEqual(result.get("status"), "invalid", result)
                self.assertNotIn("choice", result)

    def test_consumed_claim_reconciliation_waits_until_exact_60_second_boundary(self):
        npc = candidate()
        value = facts(turnInOffer=offer(npc), claimReservation=[SESSION, npc["guid"], "8"],
                      lastMutation=dict(method="buddy-quests.turn_in", questId=783, at=40.001))
        selected = self.choice(value, "wait")
        self.assertEqual(selected["phase"], "waiting_for_prerequisite_turn_in_response")
        value["lastMutation"]["at"] = 40
        self.choice(value, "turn_in", npc)
        value["lastMutation"]["at"] = 99
        value["lastMutation"]["questId"] = 33
        self.choice(value, "turn_in", npc)
        value["lastMutation"].update(questId=783, method="buddy-quests.claim_reward")
        self.choice(value, "turn_in", npc)

    def test_fresh_unreserved_offer_is_claimed_despite_recent_dialogue_query(self):
        npc = candidate()
        value = facts(turnInOffer=offer(npc),
                      lastMutation=dict(method="buddy-quests.turn_in", questId=783, at=99))
        self.claim(value, 0)

    def test_malformed_matching_reward_choices_and_missing_revision_are_invalid(self):
        npc = candidate()
        malformed = [False, {"not": "an array"}, [item()] * 7,
                     [item(0)], [item(4294967296)], [item(80, count=0)],
                     [item(80, count=4294967296)], [item(80, index=6)],
                     [item(80, index=-1)], [item(80), item(6070)],
                     [dict(itemId=True, index=0, count=1)]]
        for choices in malformed:
            with self.subTest(choices=choices):
                result = self.bridge.evaluate(self.source, facts(turnInOffer=offer(npc, choices=choices)))
                self.assertEqual(result.get("status"), "invalid", result)
                self.assertNotIn("choice", result)
        result = self.bridge.evaluate(self.source, facts(turnInOffer=offer(npc, revision="0")))
        self.assertEqual(result.get("status"), "invalid", result)

    def test_original_shared_preference_useful_and_vendor_modes_still_select(self):
        choices = [item(80), item(6070, index=4)]
        result = self.bridge.evaluate(self.source, dict(mode="offer_preference", choices=choices,
                                                       questId=33, classId=5))
        self.assertEqual((result["status"], result["position"], result["itemId"]),
                         ("selected", 1, 80))
        rows = [dict(itemId=80, available=True, sellPriceKnown=True, sellPrice=1,
                     itemClass=4, subclass=1, inventoryType=8, armor=5,
                     allowableClass=4294967295, allowableRace=4294967295),
                dict(itemId=6070, available=True, sellPriceKnown=True, sellPrice=2)]
        result = self.bridge.evaluate(self.source, dict(mode="vendor_value", choices=choices, items=rows))
        self.assertEqual((result["status"], result["position"]), ("selected", 2))
        result = self.bridge.evaluate(self.source, dict(mode="useful", choices=choices, items=rows,
                                                       classId=5, weaponSubclasses=[]))
        self.assertEqual((result["status"], result["position"]), ("selected", 1))

    def test_malformed_direct_identity_ledger_and_candidate_facts_are_invalid(self):
        changes = [dict(sessionId=""), dict(now="100"), dict(ownQuestIds=False),
                   dict(ownQuestIds=list(range(1, 27))), dict(candidates=False),
                   dict(candidates=[candidate()] * 65), dict(autonomous=1),
                   dict(questLogRevision="09")]
        for fields in changes:
            with self.subTest(fields=fields):
                result = self.bridge.evaluate(self.source, facts(**fields))
                self.assertEqual(result.get("status"), "invalid", result)
        for fields in (dict(guid="18446744073709551616"), dict(guid="01"),
                       dict(distance=-1), dict(distance="2"), dict(entry=True)):
            with self.subTest(candidate=fields):
                result = self.bridge.evaluate(self.source, facts(candidates=[candidate() | fields]))
                self.assertEqual(result.get("status"), "invalid", result)


if __name__ == "__main__":
    unittest.main()
