"""Standing quest choices through the real local Lua bridge; no game RPCs."""

import copy
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parent.parent
loader = importlib.machinery.SourceFileLoader(
    "party_quest_policy_runtime", str(ROOT / "dev/task-runtime"))
spec = importlib.util.spec_from_loader(loader.name, loader)
runtime = importlib.util.module_from_spec(spec)
loader.exec_module(runtime)
BINARY = Path(os.environ.get("BUDDY_LUAPOLICY_BINARY",
                 os.environ.get("BUDDY_LUAPOLICY_TEST_BINARY", ROOT / "data/bin/luapolicy")))
SESSION = "party-quest-policy-session"


def position(x=0, y=0, z=0, map_id=0, known=True):
    return dict(known=known, mapId=map_id, x=x, y=y, z=z)


def giver(entry=197, spawn=1, x=2, y=0, z=0, **fields):
    guid = str(0xf130000000000000 | entry << 24 | spawn)
    return dict(guid=guid, entry=entry, creatureIdentityMatches=True,
                position=position(x, y, z), present=True, aliveKnown=True,
                alive=True, npcFlags=2, unitFlags=0, movementFlags=0) | fields


def pickup(npc, quests, revision="7", expires="60000"):
    return dict(npcGuid=npc["guid"], revision=revision, expiresInMs=expires,
                quests=quests)


def reward(npc, quest_id=7, revision="8", **fields):
    return dict(npcGuid=npc["guid"], questId=quest_id, revision=revision,
                expiresInMs="60000", source="reward_offer", canComplete=True,
                rewardRequested=False) | fields


def facts(**fields):
    return dict(sessionId=SESSION, now=100, ownQuestIds=[], ownerActiveQuestIds=[],
                ownerDoneQuestIds=[], questLog=[], nearby=[giver()],
                ownerPosition=position(), selfPosition=position(),
                offer=dict(npcGuid="0", revision="0", expiresInMs="0", quests=[]),
                turnInOffer=dict(npcGuid="0", questId=0, revision="0", expiresInMs="0"),
                rewarded=[], accepted=[], reservations={}, queries=[],
                rewardSignature=[SESSION, "4"],
                acceptSignature=[SESSION, "4", "addon-session", "15"]) | fields


@unittest.skipUnless(BINARY.is_file(), "set BUDDY_LUAPOLICY_TEST_BINARY to the local compiled bridge")
class PartyQuestPolicyTests(unittest.TestCase):
    def setUp(self):
        self.bridge = runtime.Bridge(BINARY, ROOT / "modules")
        self.source = (ROOT / "behaviors/party-quests.lua").read_text()

    def evaluate(self, value):
        result = self.bridge.evaluate(self.source, value)
        self.assertEqual(result.get("status"), "ready", result)
        return result

    def rows(self, result, field):
        value = result[field]
        # Empty Lua tables have no intrinsic array/object distinction.
        if value == {}:
            return []
        self.assertIsInstance(value, list)
        return value

    def choice(self, value, kind, npc=None, quest_id=None):
        result = self.evaluate(value)
        selected = result["choice"]
        self.assertEqual(selected["kind"], kind, result)
        if npc is not None:
            self.assertEqual(selected["npcGuid"], npc["guid"], result)
        if quest_id is not None:
            self.assertEqual(selected["questId"], quest_id, result)
        return result, selected

    def ready_reward(self, value, kind, npc, quest_id=7):
        result, selected = self.choice(value, "reward_candidate", npc, quest_id)
        self.assertEqual(selected["ready"]["kind"], kind, result)
        if kind != "wait":
            self.assertEqual(selected["ready"]["npcGuid"], npc["guid"], result)
            self.assertEqual(selected["ready"]["questId"], quest_id, result)
        self.assertNotEqual(selected["ready"].get("kind"), "reward_candidate")
        return result, selected

    def test_givers_sort_by_owner_then_self_distance_then_exact_guid(self):
        a, b = giver(spawn=1, x=-1), giver(spawn=2, x=-1)
        c, d = giver(spawn=3, x=1), giver(spawn=4, x=5)
        value = facts(selfPosition=position(-4), nearby=[d, b, c, a],
                      ownerActiveQuestIds=[15])
        result, _ = self.choice(value, "offer", a)
        rows = self.rows(result, "givers")
        self.assertEqual([row["guid"] for row in rows],
                         [npc["guid"] for npc in (a, b, c, d)])
        self.assertEqual([(row["distance"], row["ownerDistance"]) for row in rows],
                         [(3, 1), (3, 1), (5, 1), (9, 5)])
        self.assertGreater(int(a["guid"]), 2**53)
        self.assertEqual(int(b["guid"]) - int(a["guid"]), 1)

    def test_accepted_event_prioritizes_only_a_current_missing_quest_giver(self):
        near, recorded = giver(x=1), giver(spawn=2, x=5)
        value = facts(ownerActiveQuestIds=[15], nearby=[near, recorded],
                      accepted=[dict(questId=15, npcGuid=recorded["guid"])])
        self.choice(value, "offer", recorded)
        value["accepted"][0]["questId"] = 21
        self.choice(value, "offer", near)

    def test_explicit_reward_event_guid_never_falls_back_to_entry_or_offer(self):
        ordinary, absent = giver(), giver(spawn=2)
        value = facts(ownQuestIds=[7], ownerDoneQuestIds=[7],
                      questLog=[dict(questId=7, complete=True)],
                      turnInOffer=reward(ordinary),
                      rewarded=[dict(questId=7, npcGuid=absent["guid"])])
        self.choice(value, "wait")
        value["nearby"].append(absent)
        self.choice(value, "turn_in", absent, 7)
        value["turnInOffer"] = reward(absent)
        _, selected = self.ready_reward(value, "claim_reward", absent)
        self.assertEqual(selected["offerRevision"], "8")

    def test_current_reward_offer_supplies_an_observed_unmapped_giver_only(self):
        npc = giver(entry=999)
        value = facts(ownQuestIds=[900], ownerDoneQuestIds=[900], nearby=[npc],
                      questLog=[dict(questId=900, complete=True)],
                      turnInOffer=reward(npc, 900))
        self.ready_reward(value, "claim_reward", npc, 900)
        value["turnInOffer"]["questId"] = 901
        self.choice(value, "wait")
        value["turnInOffer"]["questId"] = 900
        value["nearby"] = []
        self.choice(value, "wait")

    def test_reward_work_precedes_pickup_and_completed_quest_ids_are_ordered(self):
        npc = giver()
        value = facts(ownQuestIds=[15, 7], ownerDoneQuestIds=[15, 7],
                      ownerActiveQuestIds=[21],
                      questLog=[dict(questId=15, complete=True), dict(questId=7, complete=True)],
                      offer=pickup(npc, [dict(questId=21, available=True)]))
        self.choice(value, "turn_in", npc, 7)
        value["questLog"][1]["complete"] = False
        self.choice(value, "turn_in", npc, 15)

    def test_accept_keeps_actual_offer_row_order_and_excludes_consumed_repeatable_rows(self):
        npc = giver()
        value = facts(ownerActiveQuestIds=[15, 21, 54], offer=pickup(npc, [
            dict(questId=54, available=True, repeatable=True),
            dict(questId=15, available=True, acceptRequested=True),
            dict(questId=999, available=True),
            dict(questId=21, available=True),
            dict(questId=15, available=True),
        ]))
        _, selected = self.choice(value, "accept", npc, 21)
        self.assertEqual(selected["offerRevision"], "7")
        value["offer"]["quests"][-2:] = list(reversed(value["offer"]["quests"][-2:]))
        self.choice(value, "accept", npc, 15)

    def test_full_own_log_prevents_pickup_but_does_not_prevent_completed_reward(self):
        npc = giver()
        value = facts(ownQuestIds=list(range(1, 26)), ownerActiveQuestIds=[100],
                      offer=pickup(npc, [dict(questId=100, available=True)]))
        result, _ = self.choice(value, "wait")
        self.assertEqual(self.rows(result, "allowedAcceptQuestIds"), [])
        value.update(ownerDoneQuestIds=[7], questLog=[dict(questId=7, complete=True)])
        self.choice(value, "turn_in", npc, 7)

    def test_menu_reservation_consumes_whole_menu_only_for_same_session_npc_revision(self):
        npc = giver()
        value = facts(ownerActiveQuestIds=[15, 21],
                      offer=pickup(npc, [dict(questId=21, available=True)]),
                      reservations={"accept:15": [SESSION, npc["guid"], "7"]})
        self.choice(value, "offer", npc)
        for index, replacement in ((0, "previous-session"), (1, giver(spawn=2)["guid"]), (2, "6")):
            changed = copy.deepcopy(value)
            changed["reservations"]["accept:15"][index] = replacement
            self.choice(changed, "accept", npc, 21)
        value["reservations"] = {"claim:15": [SESSION, npc["guid"], "7"]}
        self.choice(value, "accept", npc, 21)

    def test_offer_query_retries_at_30_seconds_and_changed_signature_is_new_evidence(self):
        npc = giver()
        value = facts(ownerActiveQuestIds=[15])
        value["queries"] = [dict(npc=npc["guid"], signature=value["acceptSignature"], at=70.001)]
        self.choice(value, "wait")
        value["queries"][0]["at"] = 70
        self.choice(value, "offer", npc)
        value["queries"][0]["at"] = 99
        value["queries"][0]["signature"] = [SESSION, "3", "addon-session", "15"]
        self.choice(value, "offer", npc)

    def test_turnin_query_retries_at_60_seconds_without_accept_delay_shortcut(self):
        npc = giver()
        value = facts(ownQuestIds=[7], ownerDoneQuestIds=[7],
                      questLog=[dict(questId=7, complete=True)])
        key = "reward:7:" + npc["guid"]
        value["queries"] = [dict(npc=key, signature=value["rewardSignature"], at=40.001)]
        self.choice(value, "wait")
        value["queries"][0]["at"] = 40
        self.choice(value, "turn_in", npc, 7)
        value["queries"][0]["at"] = 99
        value["queries"][0]["signature"] = [SESSION, "3"]
        self.choice(value, "turn_in", npc, 7)

    def test_queries_skip_only_the_matching_giver_and_signature(self):
        first, second = giver(x=1), giver(spawn=2, x=2)
        value = facts(nearby=[second, first], ownerActiveQuestIds=[15])
        value["queries"] = [dict(npc=first["guid"], signature=value["acceptSignature"], at=99)]
        self.choice(value, "offer", second)

    def test_own_and_owner_evidence_are_not_interchangeable(self):
        value = facts(ownQuestIds=[7, 15, 54], ownerActiveQuestIds=[15, 21],
                      ownerDoneQuestIds=[7, 18, 54], questLog=[
                          dict(questId=7, complete=True), dict(questId=15, complete=True),
                          dict(questId=18, complete=True), dict(questId=54, complete=True, failed=True)])
        result = self.evaluate(value)
        self.assertEqual(self.rows(result, "allowedAcceptQuestIds"), [21])
        self.assertEqual(self.rows(result, "allowedRewardQuestIds"), [7])
        value["ownerDoneQuestIds"] = []
        self.choice(value, "offer", value["nearby"][0])

    def test_unknown_or_wrong_map_positions_never_create_a_giver_candidate(self):
        for field in ("ownerPosition", "selfPosition"):
            with self.subTest(field=field):
                value = facts(ownerActiveQuestIds=[15])
                value[field]["known"] = False
                result, _ = self.choice(value, "wait")
                self.assertEqual(self.rows(result, "givers"), [])
        value = facts(ownerActiveQuestIds=[15])
        value["nearby"][0]["position"]["known"] = False
        result, _ = self.choice(value, "wait")
        self.assertEqual(self.rows(result, "givers"), [])
        value["nearby"][0]["position"] = position(map_id=1)
        result, _ = self.choice(value, "wait")
        self.assertEqual(self.rows(result, "givers"), [])

    def test_giver_visibility_identity_combat_movement_and_distance_filtering(self):
        changes = [dict(present=False), dict(aliveKnown=False), dict(alive=False),
                   dict(creatureIdentityMatches=False), dict(npcFlags=0),
                   dict(unitFlags=0x80000), dict(movementFlags=1),
                   dict(movementFlags=0x08000000), dict(position=position(12.001))]
        for fields in changes:
            with self.subTest(fields=fields):
                value = facts(nearby=[giver(**fields)], ownerActiveQuestIds=[15])
                result, _ = self.choice(value, "wait")
                self.assertEqual(self.rows(result, "givers"), [])
        value = facts(nearby=[giver(x=12)], selfPosition=position(-12), ownerActiveQuestIds=[15])
        self.choice(value, "offer", value["nearby"][0])
        value["selfPosition"]["x"] = -12.001
        result, _ = self.choice(value, "wait")
        self.assertEqual(self.rows(result, "givers"), [])

    def test_reward_preflight_wrapper_remains_even_when_claim_menu_was_reserved(self):
        npc = giver()
        value = facts(ownQuestIds=[7], ownerDoneQuestIds=[7],
                      questLog=[dict(questId=7, complete=True)], turnInOffer=reward(npc))
        self.ready_reward(value, "claim_reward", npc)
        value["reservations"] = {"claim:7": [SESSION, npc["guid"], "8"]}
        self.ready_reward(value, "turn_in", npc)
        value["queries"] = [dict(npc="reward:7:" + npc["guid"],
                                 signature=value["rewardSignature"], at=99)]
        self.ready_reward(value, "wait", npc)

    def test_reserved_claim_preflight_wraps_the_following_accept_without_nesting(self):
        npc = giver()
        value = facts(ownQuestIds=[7], ownerDoneQuestIds=[7], ownerActiveQuestIds=[15],
                      questLog=[dict(questId=7, complete=True)], turnInOffer=reward(npc),
                      offer=pickup(npc, [dict(questId=15, available=True)]),
                      reservations={"claim:7": [SESSION, npc["guid"], "8"]})
        value["queries"] = [dict(npc="reward:7:" + npc["guid"],
                                 signature=value["rewardSignature"], at=99)]
        result, selected = self.choice(value, "reward_candidate", npc, 7)
        self.assertEqual(selected["ready"], dict(kind="accept", npcGuid=npc["guid"],
                                                 questId=15, offerRevision="7"), result)

    def test_expired_consumed_or_incomplete_reward_offer_is_not_a_claim_candidate(self):
        npc = giver()
        for fields in (dict(expiresInMs="0"), dict(rewardRequested=True),
                       dict(canComplete=False), dict(source="requirements")):
            with self.subTest(fields=fields):
                value = facts(ownQuestIds=[7], ownerDoneQuestIds=[7],
                              questLog=[dict(questId=7, complete=True)], turnInOffer=reward(npc, **fields))
                self.choice(value, "turn_in", npc, 7)

    def test_malformed_direct_inputs_return_invalid_instead_of_an_action(self):
        changes = [dict(sessionId=""), dict(now="100"), dict(ownQuestIds=False),
                   dict(ownQuestIds=[0]), dict(ownQuestIds=[True]),
                   dict(ownQuestIds=list(range(1, 27))), dict(ownerActiveQuestIds=list(range(1, 27))),
                   dict(ownerDoneQuestIds=list(range(1, 74))), dict(nearby=[giver()] * 65),
                   dict(nearby={"not": "an array"}), dict(questLog=False),
                   dict(queries=False), dict(reservations=False)]
        for fields in changes:
            with self.subTest(fields=fields):
                result = self.bridge.evaluate(self.source, facts(**fields))
                self.assertEqual(result.get("status"), "invalid", result)
                self.assertNotIn("choice", result)
        for fields in (dict(guid="018446744073709551615"),
                       dict(guid="18446744073709551616"), dict(npcFlags=-1),
                       dict(movementFlags=2**32), dict(entry=True)):
            with self.subTest(creature=fields):
                result = self.bridge.evaluate(self.source, facts(nearby=[giver(**fields)]))
                self.assertEqual(result.get("status"), "invalid", result)


if __name__ == "__main__":
    unittest.main()
