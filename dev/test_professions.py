#!/usr/bin/env python3
"""Profession learning through fake, strictly bounded normal trainer APIs."""
import contextlib
import copy
import importlib.machinery
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

loader = importlib.machinery.SourceFileLoader("professions_tested", str(Path(__file__).with_name("professions")))
spec = importlib.util.spec_from_loader(loader.name, loader)
p = importlib.util.module_from_spec(spec)
loader.exec_module(p)
NPC = 0xf130000202000001
SPELLS = {165: (2155, 2108), 171: (2275, 2259), 182: (2372, 2366), 197: (3911, 3908),
          333: (7414, 7411), 393: (8615, 8613), 129: (3279, 3273), 185: (2551, 2550), 356: (7733, 7620)}


def offered(skill, cost=10):
    primary = skill not in p.SECONDARY
    wrapper, learned = SPELLS[skill]
    return dict(spellId=wrapper, learnedSpellId=learned, professionSkillId=skill,
                professionName=p.NAMES[skill], primaryProfession=primary, pointCost=[0, 1] if primary else [0, 0],
                usable=0, costCopper=cost, requiredLevel=5 if primary else 1, requiredAbilities=[0, 0, 0])


class RPC:
    def __init__(self, guid=1):
        self.guid, self.calls, self.session, self.revision = guid, [], "profession-body", 1
        self.map_id, self.npc_map, self.action_map, self.map_known = 0, None, None, True
        self.known, self.free_known, self.free, self.copper, self.copper_known = True, True, 2, 100, True
        self.primary, self.secondary = {}, {}
        self.level, self.level_known, self.combat, self.x, self.npc_x = 5, True, False, 0., 2.
        self.npc_present, self.npc_flags, self.owner, self.offer, self.purchase = True, 0x50, "", {}, {}
        self.spells = [offered(skill) for skill in (*p.PRIMARY[guid], *p.SECONDARY)]
        self.apply, self.error_buy, self.prove, self.after_list, self.after_acquire = True, False, True, None, None
        self.reject,self.offer_revision=False,20

    def state(self):
        return dict(sessionId=self.session, professionsKnown=self.known, professionsRevision=str(self.revision),
                    primaryProfessions=list(self.primary.values()), secondaryProfessions=list(self.secondary.values()),
                    freePrimarySlotsKnown=self.free_known, freePrimarySlots=self.free,
                    copperKnown=self.copper_known, copper=self.copper, lastPurchase=copy.deepcopy(self.purchase), offer=copy.deepcopy(self.offer))

    def creature(self):
        return dict(guid=str(NPC), entry=NPC >> 24 & 0xffffff, present=self.npc_present,
                    aliveKnown=True, alive=True, positionKnown=True, mapKnown=True, mapId=self.map_id if self.npc_map is None else self.npc_map,
                    x=self.npc_x, y=0., z=0., npcFlags=self.npc_flags)

    def call(self, method, params):
        self.calls.append((method, copy.deepcopy(params)))
        if method == "buddy-world.snapshot":
            return {"self": dict(guid=str(self.guid), present=True, mapKnown=self.map_known, mapId=self.map_id, aliveKnown=True, alive=True,
                                 unitFlagsKnown=True, unitFlags=0x80000 if self.combat else 0,
                                 levelKnown=self.level_known, level=self.level)}
        if method == "buddy-actions.status":
            return dict(moving=True, following=True, interactionOwner=self.owner,
                        position=dict(known=True, mapKnown=self.map_known, mapId=self.map_id if self.action_map is None else self.action_map, x=self.x, y=0., z=0.))
        if method == "buddy-training.state": return self.state()
        if method == "buddy-training.discover_professions":
            return dict(state=self.state(), nearby=[dict(creature=self.creature())])
        if method == "buddy-training.list":
            assert params == dict(sessionId=self.session, npcGuid=str(NPC), trainerType=2)
            self.offer_revision+=1
            self.offer = dict(npcGuid=str(NPC), revision=str(self.offer_revision), trainerType=2,
                              expiresInMs="30000", spells=copy.deepcopy(self.spells))
            result = self.state()
            if self.after_list: self.after_list(self)
            return result
        if method == "buddy-actions.acquire_interaction":
            self.owner = "training"
            if self.after_acquire: self.after_acquire(self)
            return {"token": "profession-token", "state": {"interactionOwner": "training"}}
        if method == "buddy-actions.release_interaction":
            assert params == {"owner": "training", "token": "profession-token"}
            self.owner = ""
            return {}
        if method == "buddy-actions.travel_trainer":
            assert params == {"token": "profession-token", "npcGuid": str(NPC)}
            self.x = self.npc_x
            return {"reason": "trainer_travel_within_distance"}
        if method != "buddy-training.buy_profession": raise AssertionError(method)
        purchase, skill = params["purchase"], params["skillId"]
        assert purchase["sessionId"] == self.session and purchase["npcGuid"] == str(NPC)
        assert purchase["offerRevision"] == self.offer["revision"]
        assert params["allowedPrimarySkills"] == list(p.PRIMARY[self.guid])
        spell = next(row for row in self.offer["spells"] if row["spellId"] == purchase["spellId"])
        assert spell["professionSkillId"] == skill
        assert skill not in self.primary and skill not in self.secondary
        baseline = self.revision
        if self.apply and not self.reject:
            (self.secondary if skill in p.SECONDARY else self.primary)[skill] = dict(skillId=skill, rank=1, maxRank=75, step=1)
            self.free -= int(skill not in p.SECONDARY)
            self.revision += 1
            self.copper -= purchase["expectedCostCopper"]
        if self.prove:
            self.purchase = dict(id=str(len([1 for m, _ in self.calls if m == method])), npcGuid=str(NPC),
                spellId=purchase["spellId"], costCopper=purchase["expectedCostCopper"], professionSkillId=skill,
                status="SERVER_REJECTED" if self.reject else "TRAINING_CONFIRMED", serverSuccessObserved=not self.reject,
                learnedSpellObserved=not self.reject, professionSkillObserved=self.apply and not self.reject,
                baselineSkillsRevision=str(baseline), confirmedSkillsRevision=str(self.revision))
        if self.error_buy: raise p.Error("ambiguous buy")
        return self.state()


class ProfessionTests(unittest.TestCase):
    def rig(self, guid=1, stored=None, allow_travel=False):
        rpc, now, saved = RPC(guid), [100.], []
        actor = p.Learner(guid, rpc, save=lambda value: saved.append(copy.deepcopy(value)), stored=stored,
                          now=lambda: now[0], allow_travel=allow_travel)
        return actor, rpc, now, saved

    def step(self, actor, now, count=1):
        for _ in range(count): actor.tick(); now[0] += 10

    def buys(self, rpc): return [params for method, params in rpc.calls if method == "buddy-training.buy_profession"]

    def test_nonzero_maps_learn_only_actual_same_map_trainer_offers(self):
        for map_id in (1,530):
            actor,rpc,now,_=self.rig()
            rpc.map_id=map_id
            self.step(actor,now,8)
            self.assertTrue(actor.done)
            self.assertEqual(set(actor.status["confirmedPurchases"]),set(p.PRIMARY[1])|set(p.SECONDARY))
            self.assertEqual(actor.anchor["mapId"],map_id)

    def test_unknown_or_cross_map_observations_never_list_or_buy(self):
        for mode in ("unknown","actions_disagree","npc_disagree"):
            actor,rpc,now,_=self.rig()
            rpc.map_id=530
            if mode=="unknown":rpc.map_known=False
            elif mode=="actions_disagree":rpc.action_map=1
            else:rpc.npc_map=1
            self.step(actor,now)
            self.assertFalse(any(method in ("buddy-training.list","buddy-training.buy_profession","buddy-actions.travel_trainer") for method,_ in rpc.calls))

    def test_map_change_during_travel_lease_or_offer_prevents_stale_profession_write(self):
        for during in ("lease","offer"):
            actor,rpc,now,_=self.rig(allow_travel=True)
            rpc.map_id=1
            if during=="lease":
                rpc.npc_x=20
                rpc.after_acquire=lambda mock:setattr(mock,"map_id",530)
            else:rpc.after_list=lambda mock:setattr(mock,"map_id",530)
            self.step(actor,now)
            self.assertEqual(self.buys(rpc),[])
            self.assertEqual(actor.data["attempts"],[])
            self.assertFalse(any(method=="buddy-actions.travel_trainer" for method,_ in rpc.calls))
            if during=="lease":self.assertEqual(rpc.calls[-1][0],"buddy-actions.release_interaction")


    def test_all_three_profiles_learn_only_actual_assigned_pair_and_three_secondary_skills(self):
        for guid in (1, 4, 5):
            actor, rpc, now, _ = self.rig(guid)
            self.step(actor, now, 6)
            self.assertTrue(actor.done)
            self.assertEqual(set(rpc.primary), set(p.PRIMARY[guid]))
            self.assertEqual(set(rpc.secondary), set(p.SECONDARY))
            self.assertEqual(len(self.buys(rpc)), 5)
            self.assertEqual(set(actor.status["confirmedPurchases"]), set(p.PRIMARY[guid]) | set(p.SECONDARY))
            self.assertFalse(any(method == "buddy-actions.follow" for method, _ in rpc.calls))

    def test_known_professions_are_preserved_and_never_relearned_or_replaced(self):
        actor, rpc, now, _ = self.rig()
        rpc.primary[171] = dict(skillId=171, rank=25, maxRank=75)
        rpc.free = 1
        self.step(actor, now, 6)
        self.assertEqual(rpc.primary[171]["rank"], 25)
        self.assertNotIn(171, {row["skillId"] for row in self.buys(rpc)})
        actor, rpc, now, _ = self.rig()
        rpc.primary = {164: dict(skillId=164, rank=1, maxRank=75), 186: dict(skillId=186, rank=1, maxRank=75)}
        rpc.free_known, rpc.free = False, 0
        self.step(actor, now, 6)
        self.assertEqual(set(rpc.primary), {164, 186})
        self.assertEqual({row["skillId"] for row in self.buys(rpc)}, set(p.SECONDARY))
        self.assertEqual(actor.status["preservingUnassignedPrimarySkills"], [164, 186])

    def test_unknown_complete_skill_baseline_never_purchases(self):
        actor, rpc, _, _ = self.rig()
        rpc.known = False
        actor.tick()
        self.assertEqual(actor.status["phase"], "waiting_for_actual_complete_profession_evidence")
        self.assertEqual(self.buys(rpc), [])

    def test_primary_slot_level_wallet_mapping_and_point_cost_restrictions(self):
        for mode in ("free_unknown", "full", "level", "wallet", "wallet_unknown", "point_cost", "annotation", "unusable", "skill_prerequisite"):
            actor, rpc, _, _ = self.rig()
            rpc.spells = [offered(171)]
            if mode == "free_unknown": rpc.free_known = False
            elif mode == "full": rpc.free = 0
            elif mode == "level": rpc.level = 4
            elif mode == "wallet": rpc.copper = 9
            elif mode == "wallet_unknown": rpc.copper_known = False
            elif mode == "point_cost": rpc.spells[0]["pointCost"] = [1, 0]
            elif mode == "annotation": rpc.spells[0]["primaryProfession"] = False
            elif mode == "unusable": rpc.spells[0]["usable"] = 1
            elif mode == "skill_prerequisite": rpc.spells[0]["requiredSkill"] = 171
            actor.tick()
            self.assertEqual(self.buys(rpc), [], mode)

    def test_pending_attempt_is_persisted_before_write_and_never_replayed_across_restart(self):
        actor, rpc, now, saved = self.rig()
        rpc.apply, rpc.prove, rpc.error_buy = False, False, True
        with self.assertRaises(p.Error): actor.tick()
        self.assertEqual(saved[-1]["attempts"][-1]["status"], "reserved")
        self.assertEqual(saved[-1]["reservedCopper"], 10)
        restarted = p.Learner(1, rpc, stored=saved[-1], now=lambda: now[0]+100)
        rpc.session = "new-body"
        restarted.tick()
        self.assertEqual(len(self.buys(rpc)), 1)
        self.assertEqual(restarted.status["phase"], "waiting_for_unconfirmed_profession_purchase")

    def test_actual_skill_without_matching_receipt_is_goal_evidence_but_not_purchase_confirmation(self):
        actor, rpc, now, _ = self.rig()
        rpc.prove = False
        self.step(actor, now, 6)
        self.assertTrue(actor.done)
        self.assertEqual(actor.status["confirmedPurchases"], [])
        self.assertTrue(all(row["status"] == "skill_observed_purchase_unconfirmed" for row in actor.data["attempts"]))

    def test_server_ack_without_new_skill_remains_unconfirmed(self):
        actor, rpc, now, _ = self.rig()
        rpc.apply = False
        self.step(actor, now, 2)
        self.assertEqual(actor.data["attempts"][0]["status"], "reserved")
        self.assertEqual(actor.status["confirmedPurchases"], [])
        self.assertEqual(len(self.buys(rpc)), 1)

    def test_definite_rejection_allows_only_one_retry_bound_to_new_actual_offer(self):
        actor,rpc,now,saved=self.rig()
        rpc.spells=[offered(171)]
        rpc.reject=True
        self.step(actor,now)
        self.assertEqual(actor.data["attempts"][0]["status"],"rejected")
        rpc.reject=False
        self.step(actor,now)
        self.assertEqual(len(self.buys(rpc)),2)
        self.assertNotEqual(self.buys(rpc)[0]["purchase"]["offerRevision"],self.buys(rpc)[1]["purchase"]["offerRevision"])
        self.assertIn(171,rpc.primary)
        p.Learner(1,rpc,stored=saved[-1])
        actor,rpc,now,_=self.rig()
        rpc.spells=[offered(171)];rpc.reject=True
        self.step(actor,now,4)
        self.assertEqual(len(self.buys(rpc)),2)

    def test_changed_body_offer_wallet_slot_npc_or_combat_after_list_prevents_buy(self):
        for mode in ("body", "offer", "wallet", "slot", "npc", "combat", "mapping"):
            actor, rpc, _, _ = self.rig()
            rpc.spells = [offered(171)]
            def after_list(mock):
                if mode == "body": mock.session = "replacement"
                elif mode == "offer": mock.offer["revision"] = "999"
                elif mode == "wallet": mock.copper = 0
                elif mode == "slot": mock.free = 0
                elif mode == "npc": mock.npc_present = False
                elif mode == "combat": mock.combat = True
                elif mode == "mapping": mock.offer["spells"][0]["learnedSpellId"] = 999
            rpc.after_list = after_list
            actor.tick()
            self.assertEqual(self.buys(rpc), [], mode)
            self.assertEqual(actor.data["reservedCopper"], 0, mode)

    def test_actual_trainer_navigation_releases_exact_lease_then_rechecks_before_training(self):
        actor, rpc, now, _ = self.rig(allow_travel=True)
        rpc.npc_x = 30
        actor.tick()
        self.assertEqual(rpc.calls[-1], ("buddy-actions.release_interaction", {"owner": "training", "token": "profession-token"}))
        self.assertEqual(self.buys(rpc), [])
        now[0] += 10; actor.tick()
        self.assertEqual(len(self.buys(rpc)), 1)

    def test_navigation_is_opt_in_and_malformed_or_unobserved_npcs_never_become_targets(self):
        actor, rpc, _, _ = self.rig()
        rpc.npc_x = 30
        actor.tick()
        self.assertFalse(any(method == "buddy-actions.acquire_interaction" for method, _ in rpc.calls))
        self.assertEqual(actor.status["phase"], "waiting_beside_observed_profession_trainer")
        for mode in ("absent", "wrong_flags", "too_far"):
            actor, rpc, _, _ = self.rig(allow_travel=True)
            if mode == "absent": rpc.npc_present = False
            elif mode == "wrong_flags": rpc.npc_flags = 0x30
            else: rpc.npc_x = 121
            actor.tick()
            self.assertFalse(any(method == "buddy-actions.travel_trainer" for method, _ in rpc.calls), mode)

    def test_stop_or_body_change_after_acquire_releases_without_movement(self):
        for mode in ("stop", "body", "foreign_owner"):
            actor, rpc, _, _ = self.rig(allow_travel=True)
            rpc.npc_x = 30
            stopped = [False]
            actor.stopped = lambda: stopped[0]
            def after_acquire(mock):
                if mode == "stop": stopped[0] = True
                elif mode == "body": mock.session = "replacement"
                else: mock.owner = "trade"
            rpc.after_acquire = after_acquire
            if mode == "stop":
                with self.assertRaises(p.c.Stopped): actor.tick()
            else: actor.tick()
            self.assertFalse(any(method == "buddy-actions.travel_trainer" for method, _ in rpc.calls))
            self.assertEqual(rpc.calls[-1][0], "buddy-actions.release_interaction")

    def test_persistence_failure_and_foreign_character_prevent_any_purchase(self):
        actor, rpc, _, _ = self.rig()
        actor.save = lambda _: (_ for _ in ()).throw(OSError("disk full"))
        with self.assertRaises(OSError): actor.tick()
        self.assertEqual(self.buys(rpc), [])
        actor, rpc, _, _ = self.rig()
        rpc.guid = 4
        with self.assertRaises(p.Error): actor.tick()
        self.assertEqual(self.buys(rpc), [])

    def test_new_wallet_evidence_allows_query_after_previously_unaffordable_offer(self):
        actor, rpc, now, _ = self.rig()
        rpc.spells, rpc.copper = [offered(171)], 0
        actor.tick()
        self.assertEqual(self.buys(rpc), [])
        rpc.copper = 20
        now[0] += 10; actor.tick()
        self.assertEqual(len(self.buys(rpc)), 1)

    def test_saved_identity_budget_and_duplicate_skill_attempts_are_validated(self):
        actor, rpc, _, saved = self.rig()
        actor.tick()
        valid = saved[-1]
        for mode in ("identity", "budget", "duplicate", "route"):
            stored = copy.deepcopy(valid)
            if mode == "identity": stored["identity"]["guid"] = 4
            elif mode == "budget": stored["reservedCopper"] += 1
            elif mode == "duplicate": stored["attempts"] *= 2
            else: stored["travelAttempts"] = {str(NPC): True}
            with self.assertRaises(p.Error): p.Learner(1, rpc, stored=stored)

    def test_cli_expiry_and_exact_owned_stop_do_not_signal_unrelated_processes(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(p, "ROOT", Path(temporary)),\
                patch.object(p.time, "monotonic", side_effect=[0]+[100]*30), patch.object(p.signal, "signal"),\
                patch.object(p, "RPC") as rpc:
            self.assertEqual(p.main(["run", "--character-guid", "1", "--max-minutes", ".001"]), 0)
            rpc.return_value.call.assert_not_called()
            record = json.loads((Path(temporary)/"data/professions/1/status.json").read_text())
            self.assertFalse(record["routineActive"])
        with tempfile.TemporaryDirectory() as temporary, patch.object(p, "ROOT", Path(temporary)):
            folder = p.c.ensure_state(Path(temporary)/"data/professions/1")
            record = dict(identity=p.identity(1), instance="e"*32, routineActive=True)
            p.c.write_json(folder/"status.json", record)
            with os.fdopen(p.c.private_open(folder/"lock", os.O_RDWR | os.O_CREAT), "r+") as lock:
                p.fcntl.flock(lock, p.fcntl.LOCK_EX)
                with contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(p.main(["stop", "--character-guid", "1"]), 0)
                self.assertEqual(p.c.read_json(folder/"stop.json"), {"identity": record["identity"], "instance": record["instance"]})


if __name__ == "__main__":
    unittest.main()
