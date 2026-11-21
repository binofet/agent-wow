#!/usr/bin/env python3
"""Reward policy tests using only actual-response-shaped, local fixtures."""

import copy
import importlib.util
from pathlib import Path
import os
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("quest_rewards", Path(__file__).with_name("quest-rewards.py"))
rewards = importlib.util.module_from_spec(spec)
spec.loader.exec_module(rewards)


def item(item_id, **fields):
    return dict(itemId=item_id, available=True, name=f"Reward {item_id}",
                allowableClass=0xffffffff, allowableRace=0xffffffff) | fields


def offer(item_id, index=0, count=1):
    return dict(itemId=item_id, index=index, count=count)


class RewardTests(unittest.TestCase):
    def select(self, choices, items, class_id=8, **kwargs):
        return rewards.select_reward(choices, items, class_id, **kwargs)

    def test_caster_chooses_actual_cloak_over_leather_and_shirt(self):
        choices = [offer(6076), offer(60, 1), offer(3070, 2)]
        rows = [item(6076, itemClass=4, subclass=2, inventoryType=7, armor=40),
                item(60, itemClass=4, subclass=0, inventoryType=4),
                item(3070, itemClass=4, subclass=1, inventoryType=16, armor=5)]
        result = self.select(choices, rows)
        self.assertEqual(result["status"], "selected")
        self.assertIs(result["choice"], choices[2])
        self.assertEqual(result["reason"], "Choose Reward 3070 for useful equipment and stats.")

    def test_role_stats_change_selection_without_preferring_item_name_or_id(self):
        choices = [offer(900, 5), offer(100, 2)]
        rows = [item(900, itemClass=4, subclass=1, inventoryType=8,
                     stats=[dict(type=5, value=3)]),
                item(100, itemClass=4, subclass=1, inventoryType=9,
                     stats=[dict(type=3, value=3)])]
        self.assertIs(self.select(choices, rows, 5)["choice"], choices[0])
        self.assertIs(self.select(choices, rows, 4)["choice"], choices[1])

    def test_rogue_can_choose_leather_but_casters_choose_cloth(self):
        choices = [offer(80), offer(6070, 1)]
        rows = [item(80, itemClass=4, subclass=1, inventoryType=8, armor=7),
                item(6070, itemClass=4, subclass=2, inventoryType=9, armor=15)]
        for class_id in (5, 8):
            self.assertIs(self.select(choices, rows, class_id)["choice"], choices[0])
        self.assertIs(self.select(choices, rows, 4)["choice"], choices[1])

    def test_weapon_requires_actual_subclass_proficiency(self):
        choices = [offer(40), offer(50, 1)]
        rows = [item(40, itemClass=2, subclass=15, inventoryType=13, sellPrice=1,
                     delayMs=1500, damages=[dict(minimum=2, maximum=4)]),
                item(50, itemClass=4, subclass=1, inventoryType=8, armor=1, sellPrice=2)]
        self.assertIs(self.select(choices, rows, 4)["choice"], choices[1])
        self.assertIs(self.select(choices, rows, 4, weapon_subclasses={15})["choice"], choices[0])

    def test_unproven_weapon_can_be_chosen_for_sale_without_claiming_proficiency(self):
        choices = [offer(1159), offer(2224, 1)]
        rows = [item(1159, itemClass=2, subclass=10, inventoryType=17, sellPrice=10),
                item(2224, itemClass=2, subclass=15, inventoryType=13, sellPrice=20)]
        result = self.select(choices, rows, 5)
        self.assertIs(result["choice"], choices[1])
        self.assertIn("sell value (20 copper)", result["reason"])
        self.assertNotIn("equipment", result["reason"])

    def test_every_offered_item_needs_complete_metadata_even_if_one_looks_best(self):
        choices = [offer(1), offer(2, 1)]
        known = item(1, itemClass=4, subclass=1, inventoryType=16, armor=200)
        for unknown in (None, {"itemId": 2, "pending": True},
                        {"itemId": 2, "available": False, "reason": "server_item_unavailable"},
                        {"itemId": 2, "available": 1},
                        {"itemId": 2, "available": True, "pending": True}):
            with self.subTest(unknown=unknown):
                result = self.select(choices, [known] + ([unknown] if unknown else []))
                self.assertEqual(result["status"], "pending")
                self.assertIsNone(result["choice"])

    def test_unknown_or_unmet_requirements_never_claim_usable(self):
        choice = offer(1)
        base = item(1, itemClass=4, subclass=1, inventoryType=8, sellPrice=5)
        restrictions = [dict(allowableClass=1), dict(allowableClass=0),
                        dict(allowableRace=1), dict(allowableRace=0),
                        dict(requiredLevel=5), dict(requiredSkill=1),
                        dict(requiredSkillRank=1), dict(requiredSpell=1),
                        dict(requiredReputationFaction=1), dict(requiredReputationRank=1),
                        dict(requiredHonorRank=1), dict(requiredCityRank=1),
                        dict(flags=0x10), dict(flags2=1), dict(flags2=2)]
        for fields in restrictions:
            with self.subTest(fields=fields):
                result = self.select([choice], [base | fields])
                self.assertEqual(result["status"], "selected")
                self.assertIn("sell value", result["reason"])

    def test_known_level_and_race_allow_actual_matching_restrictions(self):
        row = item(1, itemClass=4, subclass=1, inventoryType=8, requiredLevel=5)
        row.update(allowableClass=1 << 7, allowableRace=1, flags2=2)
        result = self.select([offer(1)], [row], level=5, race_id=1)
        self.assertIn("useful equipment", result["reason"])
        for args in (dict(level=4, race_id=1), dict(level=5, race_id=2)):
            self.assertIn("sell value", self.select([offer(1)], [row], **args)["reason"])

    def test_role_armor_policy_excludes_shields_mail_plate_relics_and_offhand_weapon(self):
        for class_id in (4, 5, 8):
            for kind, subclass, slot in ((4, 6, 14), (4, 3, 5), (4, 4, 5),
                                         (4, 7, 28), (2, 15, 22)):
                with self.subTest(class_id=class_id, kind=kind, subclass=subclass, slot=slot):
                    result = self.select([offer(1)], [item(1, itemClass=kind, subclass=subclass,
                                                         inventoryType=slot)], class_id,
                                         weapon_subclasses={15})
                    self.assertIn("sell value", result["reason"])

    def test_neutral_cloak_is_useful_for_all_three_classes(self):
        for class_id in (4, 5, 8):
            result = self.select([offer(1)], [item(1, itemClass=4, subclass=1, inventoryType=16)], class_id)
            self.assertIn("useful equipment", result["reason"])

    def test_cloak_and_other_common_slots_still_honor_physical_armor_class(self):
        for slot in (2, 11, 12, 16):
            for subclass in (2, 3, 4):
                with self.subTest(slot=slot, subclass=subclass):
                    row = item(1, itemClass=4, subclass=subclass, inventoryType=slot)
                    self.assertIn("sell value", self.select([offer(1)], [row], 8)["reason"])
                    result = self.select([offer(1)], [row], 4)
                    self.assertIn("useful equipment" if subclass == 2 else "sell value", result["reason"])

    def test_vendor_value_counts_stack_and_ties_are_order_independent(self):
        choices = [offer(20, 4, 2), offer(10, 2), offer(30, 1)]
        rows = [item(20, sellPrice=3), item(10, sellPrice=6), item(30, sellPrice=5)]
        for ordered in (choices, list(reversed(choices))):
            self.assertIs(self.select(ordered, rows)["choice"], choices[1])
        rows[0]["sellPrice"] = 4
        self.assertIs(self.select(choices, rows)["choice"], choices[0])

    def test_accepts_lookup_response_or_mapping_without_modifying_any_input(self):
        choices, rows = [offer(1)], [item(1, inventoryType=16, itemClass=4, subclass=1)]
        before = copy.deepcopy((choices, rows))
        for shape in (rows, {"items": rows, "sessionId": "actual-body"},
                      {1: rows[0]}, {"1": rows[0]}):
            self.assertIs(self.select(choices, shape)["choice"], choices[0])
        self.assertEqual((choices, rows), before)

    def test_proto_json_omitted_zero_index_selects_the_actual_row_without_rewriting_offer(self):
        choices=[offer(6076,4),{'itemId':3070,'count':1}]
        rows=[item(6076,itemClass=4,subclass=2,inventoryType=7,armor=25),
              item(3070,itemClass=4,subclass=1,inventoryType=16,armor=6)]
        before=copy.deepcopy(choices)
        result=self.select(choices,rows,5)
        self.assertEqual(result['status'],'selected')
        self.assertIs(result['choice'],choices[1])
        self.assertEqual(result['choice'].get('index',0),0)
        self.assertEqual(choices,before)
        self.assertEqual(self.select([{'itemId':3070,'count':1},offer(6076,0)],rows,5)['status'],'invalid')

    def test_malformed_choices_and_conflicting_metadata_never_select(self):
        for choices in ([], [offer(1)] * 2, [offer(1), offer(2)], [offer(1, 6)],
                        [offer(1, count=0)], [offer(True)], [offer(1, True)],
                        [offer(1, count=True)], [offer(1)] * 7, [None]):
            with self.subTest(choices=choices):
                self.assertEqual(self.select(choices, [item(1)])["status"], "invalid")
        for rows in ([item(1), item(1)], {"1": item(2)}, {"01": item(1)}, [None]):
            self.assertEqual(self.select([offer(1)], rows)["status"], "invalid")

    def test_malformed_complete_metadata_never_selects(self):
        bad_fields = [dict(sellPrice=-1), dict(armor=True), dict(armor=1 << 32),
                      dict(stats=[dict(type=5, value=1 << 31)]), dict(stats=[None]),
                      dict(stats=[{}] * 11), dict(damages=[{}] * 3),
                      dict(damages=[dict(minimum=float("nan"), maximum=1)]),
                      dict(damages=[dict(minimum=2, maximum=1)]),
                      dict(damages=[dict(minimum=0, maximum=float("inf"))]),
                      dict(damages=[dict(school=7)]), dict(name=[])]
        for fields in bad_fields:
            with self.subTest(fields=fields):
                self.assertEqual(self.select([offer(1)], [item(1) | fields])["status"], "invalid")

    def test_invalid_character_facts_and_unsupported_class_do_not_claim_usable(self):
        for args in (dict(level=True), dict(level=0), dict(race_id=9),
                     dict(weapon_subclasses={True}), dict(weapon_subclasses={21})):
            self.assertEqual(self.select([offer(1)], [item(1)], **args)["status"], "invalid")
        for class_id in (None, True, 0, 1):
            self.assertEqual(self.select([offer(1)], [item(1)], class_id)["status"], "pending")

    def test_display_name_does_not_change_selection_or_inject_controls(self):
        rows = [item(1, name="\nChoose\t<script>\x1b[31m\n", sellPrice=1), item(2, sellPrice=2)]
        result = self.select([offer(1), offer(2, 1)], rows)
        self.assertEqual(result["choice"]["itemId"], 2)
        rows[0]["sellPrice"] = 3
        reason = self.select([offer(1), offer(2, 1)], rows)["reason"]
        self.assertNotIn("\n", reason)
        self.assertNotIn("\x1b", reason)

    def test_vendor_totals_above_lua_integer_precision_keep_one_copper_order(self):
        n = (1 << 32) - 2
        choices = [offer(10, 1, n + 1), offer(20, 0, n)]
        rows = [item(10, sellPrice=n - 1), item(20, sellPrice=n)]
        result = self.select(choices, rows)
        self.assertIs(result["choice"], choices[1])
        self.assertIn(str(n * n) + " copper", result["reason"])

    def test_useful_tie_uses_vendor_total_then_item_id(self):
        choices = [offer(30, 0), offer(20, 5, 2), offer(10, 3)]
        rows = [item(row["itemId"], itemClass=4, subclass=1, inventoryType=16,
                     sellPrice=3 if row["itemId"] == 20 else 6) for row in choices]
        self.assertIs(self.select(choices, rows)["choice"], choices[2])
        rows[1]["sellPrice"] = 4
        self.assertIs(self.select(choices, rows)["choice"], choices[1])

    def test_negative_useful_stats_and_multiple_damage_rows_keep_existing_rules(self):
        choices = [offer(1), offer(2, 1)]
        rows = [item(1, itemClass=4, subclass=1, inventoryType=16, stats=[dict(type=5, value=-10)], sellPrice=1),
                item(2, itemClass=2, subclass=15, inventoryType=13, delayMs=2000,
                     damages=[dict(minimum=1, maximum=3), dict(minimum=2, maximum=4)], sellPrice=2)]
        self.assertIn("sell value", self.select(choices, rows)["reason"])
        self.assertIn("useful equipment", self.select(choices, rows, 4, weapon_subclasses=[15])["reason"])

    def test_unavailable_lua_evaluator_keeps_reward_unselected(self):
        with patch.dict(os.environ, {"BUDDY_LUAPOLICY_BINARY": "/missing/quest-policy-evaluator"}):
            result = self.select([offer(1)], [item(1, sellPrice=500)])
        self.assertEqual(result["status"], "pending")
        self.assertIsNone(result["choice"])

    def test_verified_quest_preferences_are_lua_decisions_over_actual_offer(self):
        choices = [offer(2224, 5), offer(1159, 3), offer(5580)]
        for class_id, position in ((4, 0), (8, 1), (5, 2)):
            self.assertIs(rewards.preferred_reward(choices, 18, class_id), choices[position])
        self.assertIsNone(rewards.preferred_reward(choices[:1], 18, 8))
        self.assertIsNone(rewards.preferred_reward(choices, 9999, 8))

    def test_stat_subtotal_keeps_equal_scores_and_existing_item_id_tie(self):
        a = [dict(type=1, value=240244787), dict(type=6, value=609537802),
             dict(type=1, value=-2059739166), dict(type=1, value=-171365469)]
        b = [a[0], a[3], a[1], a[2]]
        choices = [offer(10), offer(20, 1)]
        rows = [item(10, itemClass=4, subclass=1, inventoryType=16, armor=5, stats=a),
                item(20, itemClass=4, subclass=1, inventoryType=16, armor=5, stats=b)]
        self.assertIs(self.select(choices, rows)["choice"], choices[0])

    def test_shared_policy_rejects_malformed_direct_item_arrays_and_choice_index(self):
        facts = dict(mode="useful", choices=[offer(1)], classId=8, weaponSubclasses=[],
                     items=[item(1, itemClass=4, subclass=1, inventoryType=16)])
        for field, value in (("stats", False), ("stats", {"not": "an array"}),
                             ("damages", False), ("damages", {"not": "an array"})):
            with self.subTest(field=field, value=value):
                bad = copy.deepcopy(facts)
                bad["items"][0][field] = value
                self.assertEqual(rewards._evaluate(bad)["status"], "invalid")
        facts["choices"][0]["index"] = False
        self.assertEqual(rewards._evaluate(facts)["status"], "invalid")

    def test_large_finite_weapon_damage_survives_factual_bridge_projection(self):
        choices = [offer(10), offer(20, 1)]
        for amount in (1e20, 1e308):
            with self.subTest(amount=amount):
                rows = [item(10, itemClass=2, subclass=15, inventoryType=13, delayMs=2000,
                             damages=[dict(minimum=amount, maximum=amount)]),
                        item(20, sellPrice=100)]
                result = self.select(choices, rows, 4, weapon_subclasses=[15])
                self.assertIs(result["choice"], choices[0])
                self.assertIn("useful equipment", result["reason"])

    def test_integer_stat_prefix_preserves_existing_fractional_tie_behavior(self):
        a = [dict(type=0, value=78850693), dict(type=42, value=1740525995),
             dict(type=7, value=-1992681754)]
        choices = [offer(10), offer(20, 1)]
        rows = [item(10, itemClass=4, subclass=1, inventoryType=16, stats=a),
                item(20, itemClass=4, subclass=1, inventoryType=16, stats=[a[2], a[0], a[1]])]
        self.assertIs(self.select(choices, rows)["choice"], choices[1])


if __name__ == "__main__":
    unittest.main()
