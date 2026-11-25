"""Starter objective stages through the real local Lua bridge; no game RPCs."""

import copy
import hashlib
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parent.parent
loader = importlib.machinery.SourceFileLoader(
    "starter_objective_policy_runtime", str(ROOT / "dev/task-runtime"))
spec = importlib.util.spec_from_loader(loader.name, loader)
runtime = importlib.util.module_from_spec(spec)
loader.exec_module(runtime)
BINARY = Path(os.environ.get("BUDDY_LUAPOLICY_BINARY",
                 os.environ.get("BUDDY_LUAPOLICY_TEST_BINARY", ROOT / "data/bin/luapolicy")))


def facts(stage="post_hold", **fields):
    return dict(stage=stage, selfGuid="4", autonomous=True,
                activeQuestIds=[7, 18, 33], desiredQuestIds=[7, 18, 3903],
                confirmedQuestIds=[], wolfComplete=False, wolfFailed=False,
                levelKnown=True, level=2, now=100, nextMutationAt=0,
                self=dict(present=True, aliveKnown=True, alive=True,
                          combatFlagsKnown=True, inCombat=False),
                owner=dict(objectKnown=True, guid="3", present=True,
                           aliveKnown=True, alive=True, awayFlagsKnown=True, away=False),
                routine=dict(practiceOwned=False, practiceOwnerPresent=False),
                actions=dict(practicing=False, practicePhase="", practiceLeaseRemainingMs=False,
                             autoAttacking=False, autoAttackRequested=False, autoAttackUnconfirmed=False),
                loot=dict(known=True, enabled=True), actionInFlight=False) | fields


def quest_route_facts(**fields):
    value = facts("quest_route", **(dict(activeQuestIds=[7, 18]) | fields))
    value["questRoute"] = dict(sessionId="quest-session", introProbeCurrent=False,
        eaganProbeCurrent=False, wolfProbeCurrent=False,
        givers=[dict(guid=str((0xf130 << 48) | (entry << 24) | 1), entry=entry)
                for entry in (823, 197, 196)],
        offer=dict(objectKnown=False, questsKnown=False, introQuestsKnown=False,
                   npcGuid="0", expiresInMs="0", quests=[]))
    return value


def quest_route_menu(value, entry, quests=(), expires="30000"):
    route = value["questRoute"]
    guid = next(row["guid"] for row in route["givers"] if row["entry"] == entry)
    route["offer"] = dict(objectKnown=True, questsKnown=True, introQuestsKnown=True,
        npcGuid=guid, expiresInMs=expires, quests=[dict(questId=q, available=available) for q, available in quests])
    return value


@unittest.skipUnless(BINARY.is_file(), "set BUDDY_LUAPOLICY_TEST_BINARY to the local compiled bridge")
class StarterObjectivePolicyTests(unittest.TestCase):
    def setUp(self):
        self.bridge = runtime.Bridge(BINARY, ROOT / "modules")
        self.source = (ROOT / "behaviors/starter-objectives.lua").read_text()

    def evaluate(self, value):
        result = self.bridge.evaluate(self.source, value)
        self.assertEqual(result.get("status"), "ready", result)
        return result["choice"]

    def choice(self, value, kind, phase=None):
        selected = self.evaluate(value)
        self.assertEqual(selected["kind"], kind, selected)
        if phase is not None:
            self.assertEqual(selected.get("phase"), phase, selected)
        return selected

    def quest_route_choice(self, value, entry, allowed=(), reward=0, probes=None):
        selected = self.choice(value, "quest_route")
        self.assertEqual(set(selected), {"kind", "neededEntry", "rewardQuestId", "allowedQuestIds", "probes"})
        self.assertEqual(selected["neededEntry"], entry, selected)
        self.assertEqual(selected["rewardQuestId"], reward, selected)
        # The actual Lua bridge represents an empty table as an object. This
        # normalization accepts only that known empty-array representation.
        actual = [] if selected["allowedQuestIds"] == {} else selected["allowedQuestIds"]
        self.assertIsInstance(actual, list)
        self.assertCountEqual(actual, allowed)
        self.assertEqual(len(actual), len(set(actual)))
        self.assertEqual(selected["probes"], probes or {}, selected)
        return selected

    def test_quest_route_preserves_intro_chain_level_gates_and_allowed_sets(self):
        cases = [
            ([], [], 1, 823, [783, 5261]),
            ([], [], 2, 823, [783, 18, 3903, 5261]),
            ([], [783], 2, 197, [7]),
            ([7], [], 1, 823, [5261]),
            ([7], [], 2, 823, [18, 3903, 5261]),
            ([7, 18], [5261], 2, 196, [33]),
            ([7], [33], 2, 823, [18, 3903]),
            ([7, 18, 3903], [33], 2, 823, []),
        ]
        for active, confirmed, level, entry, allowed in cases:
            with self.subTest(active=active, confirmed=confirmed, level=level):
                self.quest_route_choice(quest_route_facts(activeQuestIds=active,
                    confirmedQuestIds=confirmed, level=level), entry, allowed)
        value = quest_route_facts(levelKnown=False, level=0)
        self.quest_route_choice(value, 823, [5261])

    def test_quest_route_reward_priority_requires_active_quests_and_actual_wolf_completion(self):
        for active, complete, reward, entry in (
                ([7, 783, 5261, 33], True, 783, 197),
                ([7, 5261, 33], True, 5261, 196),
                ([7, 33], True, 33, 196),
                ([7, 33], False, 0, 823)):
            with self.subTest(active=active, complete=complete):
                value = quest_route_facts(activeQuestIds=active, wolfComplete=complete)
                allowed = [] if entry == 197 else [33] if entry == 196 else [18, 3903, 5261]
                self.quest_route_choice(value, entry, allowed, reward)
        self.quest_route_choice(quest_route_facts(confirmedQuestIds=[783, 5261]), 196, [33])
        value = quest_route_menu(quest_route_facts(activeQuestIds=[783]), 196)
        self.quest_route_choice(value, 197, [7], reward=783) # A pending reward cannot become a wolf-history probe.

    def test_quest_route_fresh_menus_reconcile_only_observed_missing_prerequisites(self):
        value = quest_route_menu(quest_route_facts(activeQuestIds=[]), 823, [(783, False)])
        self.quest_route_choice(value, 197, [7], probes={"intro": True})
        value["questRoute"]["offer"]["quests"] = [dict(questId=783, available=True)]
        self.quest_route_choice(value, 823, [783, 18, 3903, 5261])
        value = quest_route_menu(quest_route_facts(), 823)
        value["questRoute"]["wolfProbeCurrent"] = True
        self.quest_route_choice(value, 196, [33], probes={"eagan": True, "wolf": False})
        for available in (5261, 3903):
            with self.subTest(offered=available):
                self.quest_route_choice(quest_route_menu(quest_route_facts(), 823, [(available, True)]),
                                        823, [3903, 5261])
        value = quest_route_menu(quest_route_facts(confirmedQuestIds=[5261]), 196)
        self.quest_route_choice(value, 823, [3903], probes={"wolf": True})
        value["questRoute"]["offer"]["quests"] = [dict(questId=33, available=True)]
        self.quest_route_choice(value, 196, [33])
        value = quest_route_menu(quest_route_facts(activeQuestIds=[]), 823)
        value["questRoute"]["offer"]["questsKnown"] = False
        self.quest_route_choice(value, 197, [7], probes={"intro": True})
        value["activeQuestIds"] = [7, 18]
        self.quest_route_choice(value, 823, [3903, 5261]) # Intro's old default-empty rule cannot create an Eagan probe.

    def test_quest_route_stale_unknown_and_unobserved_menus_cannot_set_probe_flags(self):
        for probe in ("intro", "eagan", "wolf"):
            for cause in ("expired", "unobserved", "wrong_entry", "unknown_object", "unknown_quests"):
                with self.subTest(probe=probe, cause=cause):
                    value = quest_route_facts(activeQuestIds=[] if probe == "intro" else [7, 18],
                        confirmedQuestIds=[5261] if probe == "wolf" else [])
                    quest_route_menu(value, 196 if probe == "wolf" else 823)
                    route, offer = value["questRoute"], value["questRoute"]["offer"]
                    if cause == "expired": offer["expiresInMs"] = "0"
                    elif cause == "unobserved": route["givers"] = []
                    elif cause == "wrong_entry":
                        next(row for row in route["givers"] if row["guid"] == offer["npcGuid"])["entry"] = 197
                    elif cause == "unknown_object": offer["objectKnown"] = False
                    else: offer["questsKnown"] = offer["introQuestsKnown"] = False
                    entry, allowed = ((196, [33]) if probe == "wolf" else
                                      (823, [783, 18, 3903, 5261]) if probe == "intro" else (823, [3903, 5261]))
                    self.quest_route_choice(value, entry, allowed)

    def test_quest_route_current_probe_flags_are_body_scoped_evidence_not_reward_confirmation(self):
        for flag, fields, current_entry, current_allowed, old_entry, old_allowed in (
                ("introProbeCurrent", dict(activeQuestIds=[]), 197, [7], 823, [783, 18, 3903, 5261]),
                ("eaganProbeCurrent", {}, 196, [33], 823, [3903, 5261]),
                ("wolfProbeCurrent", dict(confirmedQuestIds=[5261]), 823, [3903], 196, [33])):
            with self.subTest(flag=flag):
                value = quest_route_facts(**fields)
                value["questRoute"][flag] = True
                self.quest_route_choice(value, current_entry, current_allowed)
                # The adapter computes these booleans by equality with the
                # current native SID; old-body markers enter policy as false.
                value["questRoute"].update(sessionId="replacement-quest-session", **{flag: False})
                self.quest_route_choice(value, old_entry, old_allowed)

    def test_quest_route_guid_and_expiry_decimal_strings_preserve_uint64_identity(self):
        maximum = "18446744073709551615"
        for expires in ("0", "1", maximum):
            with self.subTest(expires=expires):
                value = quest_route_menu(quest_route_facts(activeQuestIds=[]), 823, expires=expires)
                value["questRoute"]["givers"] = [dict(guid=maximum, entry=823)]
                value["questRoute"]["offer"]["npcGuid"] = maximum
                if expires == "0": self.quest_route_choice(value, 823, [783, 18, 3903, 5261])
                else: self.quest_route_choice(value, 197, [7], probes={"intro": True})
        value["questRoute"]["offer"]["npcGuid"] = "18446744073709551614"
        self.quest_route_choice(value, 823, [783, 18, 3903, 5261]) # Adjacent uint64s must never merge through a float.

    def test_quest_route_malformed_bounded_facts_fail_closed(self):
        changes = [
            lambda value: value.pop("questRoute"),
            lambda value: value["questRoute"].update(sessionId=""),
            lambda value: value["questRoute"].update(introProbeCurrent=1),
            lambda value: value["questRoute"].update(eaganProbeCurrent="false"),
            lambda value: value["questRoute"].update(wolfProbeCurrent=None),
            lambda value: value["questRoute"].update(givers=False),
            lambda value: value["questRoute"]["givers"][0].update(guid="0"),
            lambda value: value["questRoute"]["givers"][0].update(guid="018446744073709551615"),
            lambda value: value["questRoute"]["givers"][0].update(guid="18446744073709551616"),
            lambda value: value["questRoute"]["givers"][0].update(entry=True),
            lambda value: value["questRoute"]["givers"][0].update(entry=4294967296),
            lambda value: value["questRoute"].update(offer=[]),
            lambda value: value["questRoute"]["offer"].update(objectKnown="true"),
            lambda value: value["questRoute"]["offer"].update(expiresInMs="18446744073709551616"),
            lambda value: value["questRoute"]["offer"].update(quests=[dict(questId=7, available="true")]),
            lambda value: value["questRoute"]["offer"].update(quests=[dict(questId=4294967296, available=True)]),
            lambda value: value["questRoute"]["offer"].update(quests=[dict(questId=7, available=True)] * 33),
        ]
        for index, change in enumerate(changes):
            with self.subTest(case=index):
                value = quest_route_menu(quest_route_facts(activeQuestIds=[]), 823)
                change(value)
                result = self.bridge.evaluate(self.source, value)
                self.assertEqual(result.get("status"), "invalid", result)
                self.assertNotIn("choice", result)

    def test_quest_route_actual_source_edit_changes_priority_on_same_bridge_without_frame_mutation(self):
        value = quest_route_facts(activeQuestIds=[783, 5261])
        before = copy.deepcopy(value)
        self.quest_route_choice(value, 197, [7], reward=783)
        expression = ('    if active["783"] then reward=783\n'
                      '    elseif active["5261"] then reward=5261\n'
                      '    elseif active["33"] and complete then reward=33 end')
        self.assertEqual(self.source.count(expression), 1)
        priority = ('    if active["5261"] then reward=5261\n'
                    '    elseif active["783"] then reward=783\n'
                    '    elseif active["33"] and complete then reward=33 end')
        edited = self.source.replace(expression, priority, 1)
        self.assertNotEqual(hashlib.sha256(self.source.encode()).hexdigest(), hashlib.sha256(edited.encode()).hexdigest())
        # Bridge.evaluate launches the normal evaluator with fresh source per
        # call. This proves edit effect on one Bridge, not a VM reload/CAS API.
        result = self.bridge.evaluate(edited, value)
        self.assertEqual(result.get("status"), "ready", result)
        self.assertEqual((result["choice"]["neededEntry"], result["choice"]["rewardQuestId"]), (196, 5261))
        self.assertEqual(result["choice"]["allowedQuestIds"], [33])
        self.quest_route_choice(value, 197, [7], reward=783)
        self.assertEqual(value, before)
        self.assertEqual(json.dumps(value, sort_keys=True), json.dumps(before, sort_keys=True))

    def test_only_actual_own_completion_releases_wolf_objectives(self):
        value = facts(objectiveCounts=[8, 8, 8, 8])
        selected = self.choice(value, "practice")
        self.assertIs(selected["lootRequired"], True)
        value["wolfComplete"] = True
        self.choice(value, "continue_quests")
        value.update(wolfComplete=False, activeQuestIds=[7, 18])
        self.choice(value, "continue_quests")

    def test_failed_unknown_level_and_unsupported_wolf_levels_wait(self):
        self.choice(facts(wolfFailed=True), "wait", "waiting_for_failed_wolf_prerequisite_resolution")
        self.choice(facts(wolfFailed=True, wolfComplete=True), "wait",
                    "waiting_for_failed_wolf_prerequisite_resolution")
        self.choice(facts(levelKnown=False, level=0), "wait", "waiting_for_supported_wolf_practice_level")
        self.choice(facts(level=6), "wait", "waiting_for_supported_wolf_practice_level")
        for level in (1, 5):
            self.assertIs(self.choice(facts(level=level), "practice")["lootRequired"], True)
        self.choice(facts(autonomous=False), "wait", "waiting_for_own_wolf_objectives")

    def test_early_xp_fallback_requires_guid4_or5_and_actual_chain_evidence(self):
        for guid in ("1", "4", "5", "87"):
            for evidence in ("active_followup", "confirmed_wolves", "none"):
                with self.subTest(guid=guid, evidence=evidence):
                    active = [7, 3903] if evidence == "active_followup" else [7]
                    value = facts(selfGuid=guid, level=1, activeQuestIds=active,
                                  confirmedQuestIds=[33] if evidence == "confirmed_wolves" else [])
                    if guid in ("4", "5") and evidence != "none":
                        self.assertIs(self.choice(value, "practice")["lootRequired"], False)
                    else:
                        self.choice(value, "continue_quests")
        self.choice(facts(level=1, activeQuestIds=[7, 18, 3903],
                          routine=dict(practiceOwned=True, practiceOwnerPresent=True)),
                    "wait", "matched_requested_active_quest_ids")

    def test_manual_early_xp_wait_does_not_start_practice(self):
        self.choice(facts(autonomous=False, level=1, activeQuestIds=[7, 3903]),
                    "wait", "waiting_for_level_2")

    def test_late_fallback_keeps_existing_all_guid_level1_rule(self):
        for guid in ("1", "4", "5", "87", "18446744073709551615"):
            with self.subTest(guid=guid):
                self.choice(facts("fallback", selfGuid=guid, level=1, activeQuestIds=[7]),
                            "practice", "waiting_for_level_2")
        self.choice(facts("fallback", autonomous=False, level=1, activeQuestIds=[7]),
                    "wait", "waiting_for_level_2")
        self.choice(facts("fallback", levelKnown=False, level=0, activeQuestIds=[7]),
                    "wait", "waiting_for_level_observation")
        self.choice(facts("fallback", level=2, activeQuestIds=[7]),
                    "wait", "waiting_for_level_observation")

    def test_late_fallback_preserves_extra_quests_and_requires_missing_level_gated_goal(self):
        self.choice(facts("fallback", activeQuestIds=[7, 18, 3903, 999], level=1),
                    "wait", "requested_ids_present_extra_quests_unchanged")
        self.choice(facts("fallback", activeQuestIds=[18, 3903], level=1),
                    "wait", "waiting_for_level_2")

    def test_sparse_dynamic_desired_ids_keep_exact_and_missing_membership(self):
        value = facts("classify", desiredQuestIds=[99000, 4294967295],
                      activeQuestIds=[4294967295, 99000])
        self.choice(value, "wait", "matched_requested_active_quest_ids")
        value["activeQuestIds"] = [99000]
        self.choice(value, "hold_follow")
        value["stage"] = "fallback"
        self.choice(value, "wait", "waiting_for_level_observation")
        value.update(level=1, desiredQuestIds=[18, 4294967295], activeQuestIds=[4294967295])
        self.choice(value, "practice", "waiting_for_level_2")
        value["activeQuestIds"].append(18)
        self.choice(value, "wait", "requested_ids_present_extra_quests_unchanged")

    def test_extra_sparse_active_ids_do_not_match_exact_goal_or_request_abandonment(self):
        value = facts("classify", activeQuestIds=[7, 18, 3903, 99000, 4294967295])
        self.assertEqual(self.choice(value, "hold_follow"), {"kind": "hold_follow"})
        value["stage"] = "post_hold"
        self.assertEqual(self.choice(value, "continue_quests"), {"kind": "continue_quests"})
        value.update(stage="fallback", level=1)
        self.assertEqual(self.choice(value, "wait", "requested_ids_present_extra_quests_unchanged"),
                         {"kind": "wait", "phase": "requested_ids_present_extra_quests_unchanged"})

    def test_confirmed_wolves_among_sparse_ids_preserves_only_the_existing_early_xp_rule(self):
        value = facts(level=1, activeQuestIds=[7, 99000, 4294967295],
                      confirmedQuestIds=[4294967295, 33, 99000])
        self.assertIs(self.choice(value, "practice")["lootRequired"], False)
        value["selfGuid"] = "87"
        self.choice(value, "continue_quests")
        value.update(selfGuid="4", confirmedQuestIds=[99000, 4294967295])
        self.choice(value, "continue_quests")

    def test_actual_wolf_and_existing_practice_phase_survive_sparse_active_ids(self):
        value = facts(activeQuestIds=[4294967295, 33, 99000])
        self.assertIs(self.choice(value, "practice")["lootRequired"], True)
        value["wolfComplete"] = True
        self.choice(value, "continue_quests")
        value["actions"].update(practicing=True, practiceLeaseRemainingMs="60001")
        self.choice(value, "wait", "finishing_current_practice_engagement_before_quests")
        value["stage"] = "practice"
        self.choice(value, "wait", "practicing_for_wolf_quest")
        value["activeQuestIds"].remove(33)
        self.choice(value, "wait", "practicing_to_level_2")

    def test_sparse_objective_sets_still_reject_duplicates_and_uint32_overflow(self):
        for field in ("activeQuestIds", "desiredQuestIds", "confirmedQuestIds"):
            for rows in ([99000, 99000], [4294967295, 4294967295], [4294967296]):
                with self.subTest(field=field, rows=rows):
                    result = self.bridge.evaluate(self.source, facts(**{field: rows}))
                    self.assertEqual(result.get("status"), "invalid", result)
                    self.assertNotIn("choice", result)

    def test_exact_goal_precedes_owner_and_alive_checks_unless_exact_owned_practice(self):
        value = facts("classify", activeQuestIds=[7, 18, 3903])
        value["self"]["alive"] = False
        value["owner"] = dict(objectKnown=False)
        self.choice(value, "wait", "matched_requested_active_quest_ids")
        value["routine"] = dict(practiceOwned=True, practiceOwnerPresent=True)
        self.choice(value, "wait", "waiting_for_alive_observed_character")
        value["self"]["alive"] = True
        self.choice(value, "hold_follow")
        value["routine"] = dict(practiceOwned=False, practiceOwnerPresent=True)
        self.choice(value, "wait", "matched_requested_active_quest_ids")
        value["autonomous"] = False
        value["routine"] = dict(practiceOwned=True, practiceOwnerPresent=True)
        self.choice(value, "wait", "matched_requested_active_quest_ids")

    def test_classify_checks_observed_body_and_manual_owner_without_inventing_owner_access(self):
        for field in ("present", "aliveKnown", "alive"):
            with self.subTest(field=field):
                value = facts("classify")
                value["self"][field] = False
                self.choice(value, "wait", "waiting_for_alive_observed_character")
        self.choice(facts("classify"), "hold_follow")
        self.choice(facts("classify", autonomous=False), "continue")
        for fields in (dict(objectKnown=False), dict(guid="87"), dict(present=False),
                       dict(aliveKnown=False), dict(alive=False), dict(away=True)):
            with self.subTest(owner=fields):
                value = facts("classify", autonomous=False)
                value["owner"].update(fields)
                self.choice(value, "wait", "waiting_for_alive_present_owner_and_character")

    def test_finishing_fight_precedes_combat_exact_goal_and_busy_quest_checks(self):
        for phase in ("finishing_current_fight_to_level2", "finishing_current_fight_to_goal"):
            value = facts(activeQuestIds=[7, 18, 3903], actionInFlight=True)
            value["self"]["inCombat"] = True
            value["actions"].update(practicePhase=phase)
            self.choice(value, "wait", "finishing_current_practice_engagement_before_quests")
        value = facts(wolfComplete=True)
        value["actions"]["practicing"] = True
        self.choice(value, "wait", "finishing_current_practice_engagement_before_quests")

    def test_post_hold_combat_and_match_precede_busy_failed_and_practice(self):
        value = facts(activeQuestIds=[7, 18, 3903], actionInFlight=True, wolfFailed=True)
        value["self"]["inCombat"] = True
        self.choice(value, "wait", "waiting_out_of_combat_before_quests")
        value["self"]["inCombat"] = False
        self.choice(value, "wait", "matched_requested_active_quest_ids")
        value["activeQuestIds"] = [7, 18, 33]
        self.choice(value, "wait", "waiting_for_existing_quest_operation")
        value["actionInFlight"] = False
        self.choice(value, "wait", "waiting_for_failed_wolf_prerequisite_resolution")

    def test_practice_lease_60000_boundary_and_long_uint64_strings_remain_exact(self):
        for lease in ("0", "59999", "60000", "60001", "18446744073709551615"):
            with self.subTest(lease=lease):
                value = facts("practice")
                value["actions"].update(practicing=True, practiceLeaseRemainingMs=lease)
                if int(lease) > 60000:
                    self.choice(value, "wait", "practicing_for_wolf_quest")
                else:
                    self.assertIs(self.choice(value, "enable_practice")["practiceOwned"], True)
        value = facts("practice", activeQuestIds=[7], nextMutationAt="unused-bad-clock")
        value["actions"].update(practicing=True, practiceLeaseRemainingMs="60001")
        self.choice(value, "wait", "practicing_to_level_2")

    def test_practice_cooldown_and_unused_bad_lease_preserve_noop_state(self):
        value = facts("practice", now=100, nextMutationAt=100.001)
        value["actions"]["practiceLeaseRemainingMs"] = "unused-malformed-lease"
        selected = self.choice(value, "wait")
        self.assertNotIn("practiceOwned", selected)
        value["nextMutationAt"] = 100
        self.assertIs(self.choice(value, "enable_practice")["practiceOwned"], True)
        value["actions"].update(practicing=True, practiceLeaseRemainingMs="60000")
        self.choice(value, "enable_practice")

    def test_loot_enable_requires_fresh_observed_state_before_practice_continues(self):
        value = facts("loot", loot=dict(known=False, enabled=True))
        self.choice(value, "wait", "waiting_for_wolf_loot_module")
        value["loot"] = dict(known=True, enabled=False)
        first = self.choice(value, "enable_loot", "waiting_for_confirmed_wolf_looting")
        self.assertNotIn("practiceOwned", first)
        self.choice(value, "enable_loot", "waiting_for_confirmed_wolf_looting")
        value["loot"]["enabled"] = True
        self.choice(value, "continue")

    def test_before_quests_stops_observed_practice_even_without_owned_record(self):
        value = facts("before_quests")
        value["actions"]["practicing"] = True
        self.choice(value, "disable_practice")
        for flag in ("autoAttacking", "autoAttackRequested", "autoAttackUnconfirmed"):
            with self.subTest(flag=flag):
                value = facts("before_quests", routine=dict(practiceOwned=True, practiceOwnerPresent=True))
                value["actions"][flag] = True
                self.choice(value, "disable_practice")

    def test_unowned_attack_and_truthy_only_cleanup_do_not_authorize_practice_stop(self):
        for flag in ("autoAttacking", "autoAttackRequested"):
            for present in (False, True):
                with self.subTest(flag=flag, practiceOwnerPresent=present):
                    value = facts("before_quests", routine=dict(practiceOwned=False, practiceOwnerPresent=present))
                    value["actions"][flag] = True
                    selected = self.choice(value, "continue_quests")
                    if present:
                        self.assertIs(selected["practiceOwned"], False)
                    else:
                        self.assertNotIn("practiceOwned", selected)
        value = facts("before_quests", routine=dict(practiceOwned=False, practiceOwnerPresent=True))
        value["actions"]["autoAttackUnconfirmed"] = True
        self.choice(value, "wait", "waiting_for_practice_attack_stop")
        value["actions"]["autoAttackUnconfirmed"] = False
        self.assertIs(self.choice(value, "continue_quests")["practiceOwned"], False)

    def test_malformed_core_facts_and_used_lease_or_clocks_return_invalid(self):
        changes = [dict(stage="unknown"), dict(selfGuid="01"), dict(selfGuid=4),
                   dict(selfGuid="18446744073709551616"), dict(autonomous=1),
                   dict(activeQuestIds=False), dict(activeQuestIds=[33, 33]),
                   dict(desiredQuestIds=[0]), dict(confirmedQuestIds=[1, 2, 3, 4]),
                   dict(level=0), dict(level=81), dict(level=True),
                   dict(routine=False), dict(actions=False)]
        for fields in changes:
            with self.subTest(fields=fields):
                result = self.bridge.evaluate(self.source, facts(**fields))
                self.assertEqual(result.get("status"), "invalid", result)
        for lease in (False, "060001", "18446744073709551616", "-1", 60001):
            with self.subTest(usedLease=lease):
                value = facts("practice")
                value["actions"].update(practicing=True, practiceLeaseRemainingMs=lease)
                self.assertEqual(self.bridge.evaluate(self.source, value).get("status"), "invalid")
        for fields in (dict(now="100"), dict(nextMutationAt=False)):
            with self.subTest(clock=fields):
                self.assertEqual(self.bridge.evaluate(self.source, facts("practice", **fields)).get("status"), "invalid")
        value = facts()
        value["self"]["combatFlagsKnown"] = False
        self.assertEqual(self.bridge.evaluate(self.source, value).get("status"), "invalid")
        value = facts("classify", autonomous=False)
        value["owner"]["awayFlagsKnown"] = False
        self.assertEqual(self.bridge.evaluate(self.source, value).get("status"), "invalid")


if __name__ == "__main__":
    unittest.main()
