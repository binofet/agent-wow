"""Starter objective stages through the real local Lua bridge; no game RPCs."""

import importlib.machinery
import importlib.util
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
