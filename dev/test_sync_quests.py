#!/usr/bin/env python3
"""Quest goal-controller checks with fake RPC, clocks, and private temp state."""

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


loader = importlib.machinery.SourceFileLoader("sync_quests_tested", str(Path(__file__).with_name("sync-quests")))
spec = importlib.util.spec_from_loader(loader.name, loader)
sync = importlib.util.module_from_spec(spec)
loader.exec_module(sync)
MCBRIDE = 0xf1300000c5000001
WILLEM = 0xf130000337000002
EAGAN = 0xf1300000c4000003


class FakeRPC:
    def __init__(self, guid=1):
        self.guid, self.level, self.revision, self.log_revision = guid, 1, 1, 1
        self.active, self.calls, self.session = set(), [], "quest-session"
        self.intro_done, self.offer, self.reward, self.receipt = False, {}, {}, {}
        self.eagan_done, self.wolves_done, self.wolves_complete = False, False, True
        self.rewards_by_quest = {}
        self.apply_accept, self.apply_claim = True, True
        self.error_on, self.rewards, self.nearby, self.owner_present = None, [], True, True
        self.entries = ((WILLEM, 823), (MCBRIDE, 197), (EAGAN, 196))
        self.actions = {"following": True, "navAvailable": True}
        self.loot = {"enabled": True}
        self.far = False
        self.combat = False

    def world(self):
        return {"self": {"guid": str(self.guid), "present": True, "aliveKnown": True,
                         "alive": True, "levelKnown": True, "level": self.level,
                         "classKnown": True, "classId": {1:5,4:8,5:4}.get(self.guid, 0),
                         "unitFlagsKnown": True, "unitFlags": 0x80000 if self.combat else 0},
                "owner": {"guid": "3", "present": self.owner_present, "aliveKnown": True, "alive": True}}

    def state(self):
        nearby = []
        if self.nearby:
            for guid, entry in self.entries:
                nearby.append({"canInteract": True, "distanceKnown": True, "distance": 2,
                               "creature": {"guid": str(guid), "entry": entry}})
                if self.far:
                    nearby[-1].update(canInteract=False, distance=30, blockedReason="questgiver_outside_conservative_range")
                    nearby[-1]["creature"].update(present=True, aliveKnown=True, alive=True,
                        mapKnown=True, positionKnown=True, npcFlags=2)
        return {"sessionId": self.session, "questLogKnown": True,
                "questLogRevision": str(self.log_revision),
                "questLog": [{"questId": quest, "complete": quest in (783,5261) or quest == 33 and self.wolves_complete} for quest in sorted(self.active)],
                "nearby": nearby, "offer": copy.deepcopy(self.offer),
                "turnInOffer": copy.deepcopy(self.reward), "lastReward": copy.deepcopy(self.receipt)}

    def call(self, method, params):
        self.calls.append((method, copy.deepcopy(params)))
        if method == "buddy-world.snapshot":
            return self.world()
        if method == "buddy-quests.discover":
            return self.state()
        if method == "buddy-actions.status":
            return copy.deepcopy(self.actions)
        if method == "buddy-loot.state":
            return copy.deepcopy(self.loot)
        if method == "buddy-loot.enable":
            self.loot["enabled"] = params["enabled"]
            return copy.deepcopy(self.loot)
        if method == "buddy-actions.follow":
            self.actions["following"] = params["enabled"]
            return copy.deepcopy(self.actions)
        if method == "buddy-actions.practice":
            self.actions.update(practicing=params["enabled"], practiceLeaseRemainingMs="600000", practicePhase="observed_young_wolf_practice")
            return copy.deepcopy(self.actions)
        if method == "buddy-actions.acquire_interaction":
            return {"token": "owned-token", "state": {"interactionOwner": "quests"}}
        if method == "buddy-actions.release_interaction":
            return {}
        if method == "buddy-actions.travel_quest":
            if method == self.error_on:
                raise sync.Error("travel timeout")
            self.far = False
            return {"reason": "quest_travel_within_distance"}
        if method == "buddy-quests.offer":
            self.revision += 1
            npc = int(params["npcGuid"])
            offered = []
            if npc == WILLEM:
                intro_done = self.intro_done or 7 in self.active
                offered = [] if intro_done else [783]
                if intro_done and not self.eagan_done and not self.wolves_done:
                    offered += [5261]
                if self.level >= 2:
                    offered += [18] + ([3903] if self.wolves_done else [])
            if npc == MCBRIDE and self.intro_done:
                offered = [7]
            if npc == EAGAN and self.eagan_done and not self.wolves_done:
                offered = [33]
            self.offer = {"revision": str(self.revision), "npcGuid": str(npc), "expiresInMs": "60000",
                          "quests": [{"questId": quest, "available": True} for quest in offered if quest not in self.active]}
        elif method == "buddy-quests.accept":
            if params["questId"] not in sync.DESIRED | sync.PREREQUISITES:
                raise AssertionError("Accepted out-of-goal quest")
            if self.apply_accept:
                self.active.add(params["questId"])
                self.log_revision += 1
            for quest in self.offer.get("quests", []):
                if quest["questId"] == params["questId"]:
                    quest["acceptRequested"] = True
        elif method == "buddy-quests.turn_in":
            quest = params["questId"]
            expected_npc = MCBRIDE if quest == 783 else EAGAN
            if quest not in sync.PREREQUISITES or int(params["npcGuid"]) != expected_npc or quest == 33 and not self.wolves_complete:
                raise AssertionError("Turned in a different quest or NPC")
            self.revision += 1
            self.reward = {"revision": str(self.revision), "npcGuid": str(expected_npc), "questId": quest,
                           "source": "reward_offer", "canComplete": True, "expiresInMs": "60000",
                           "choices": copy.deepcopy(self.rewards_by_quest.get(quest,self.rewards))}
        elif method == "buddy-quests.claim_reward":
            quest = params["questId"]
            if quest not in sync.PREREQUISITES or params["offerRevision"] != self.reward["revision"] or quest != self.reward["questId"]:
                raise AssertionError("Unbound reward")
            self.reward["rewardRequested"] = True
            if self.apply_claim:
                self.active.remove(quest)
                self.log_revision += 1
                if quest == 783: self.intro_done = True
                if quest == 5261: self.eagan_done = True
                if quest == 33: self.wolves_done = True
                self.receipt = {"questId": quest, "status": "REWARD_CONFIRMED",
                                "npcGuid": params["npcGuid"], "offerRevision": params["offerRevision"],
                                "confirmedLogRevision": str(self.log_revision),
                                "serverRewardObserved": True, "logRemovalConfirmed": True}
        else:
            raise AssertionError("Unsupported goal RPC: " + method)
        if method == self.error_on:
            raise sync.Error("Ambiguous transport timeout")
        return {"transportWritten": True, "state": self.state()}


class SyncTests(unittest.TestCase):
    def rig(self, guid=1, stored=None):
        clock, saved = [1000.0], []
        rpc = FakeRPC(guid)
        profile = sync.CharacterSync({1: "Arlen", 4: "Mira", 5: "Rook"}[guid], guid, rpc,
            save=lambda state: saved.append(copy.deepcopy(state)), stored=stored, now=lambda: clock[0])
        return profile, rpc, clock, saved

    def mutate_calls(self, rpc):
        return [(method, params) for method, params in rpc.calls if method not in ("buddy-world.snapshot", "buddy-quests.discover", "buddy-actions.status", "buddy-loot.state")]

    def test_full_prerequisite_then_level_gate_and_goal_matching(self):
        profile, rpc, clock, saved = self.rig()
        phases, mutation_times = [], []
        for _ in range(200):
            before = len(self.mutate_calls(rpc))
            profile.tick()
            phases.append(profile.status["phase"])
            if len(self.mutate_calls(rpc)) > before:
                mutation_times.append(clock[0])
            if profile.status.get("phase") == "waiting_for_level_2":
                self.assertIn(7, profile.status["activeQuestIds"])
                self.assertEqual(profile.status["missingQuestIds"], [18, 3903])
                rpc.level = 2
            if profile.status.get("exactGoalMatched"):
                break
            clock[0] += 2
        self.assertEqual(profile.status["activeQuestIds"], [7, 18, 3903])
        self.assertTrue(profile.status["exactGoalMatched"])
        self.assertTrue(profile.status["introRewardConfirmed"])
        self.assertIn("waiting_for_level_2", phases)
        self.assertTrue(all(b - a >= 10 for a, b in zip(mutation_times, mutation_times[1:])))
        self.assertTrue(saved)
        self.assertTrue(all(not method.startswith(("buddy-chat.", "buddy-actions.")) for method, _ in rpc.calls))

    def test_ambiguous_accept_applied_is_reconciled_without_repeating(self):
        profile, rpc, clock, saved = self.rig()
        rpc.intro_done = True
        profile.data["introRewardConfirmed"] = True
        rpc.entries = ((MCBRIDE, 197),)
        profile.tick()
        clock[0] += 10
        rpc.error_on = "buddy-quests.accept"
        profile.tick()
        self.assertEqual(profile.status["phase"], "request_unconfirmed_reconciling_fresh_state")
        profile.tick()
        self.assertEqual(profile.status["activeQuestIds"], [7])
        restarted, _, _, _ = self.rig(stored=saved[-1])
        restarted.rpc, restarted.now = rpc, lambda: clock[0]
        clock[0] += 20
        restarted.tick()
        self.assertEqual(len([m for m, _ in rpc.calls if m == "buddy-quests.accept"]), 1)

    def test_accept_retry_requires_fresh_actual_offer_revision_and_fresh_absent_log(self):
        profile, rpc, clock, saved = self.rig()
        rpc.intro_done, rpc.apply_accept = True, False
        profile.data["introRewardConfirmed"] = True
        rpc.entries = ((MCBRIDE, 197),)
        profile.tick()
        clock[0] += 10
        rpc.error_on = "buddy-quests.accept"
        profile.tick()
        first = [p for m, p in rpc.calls if m == "buddy-quests.accept"][0]
        rpc.offer["quests"][0]["acceptRequested"] = False # Even malformed/stale offer cannot replay its bound request.
        clock[0] += 10
        profile.tick()
        self.assertEqual(len([m for m, _ in rpc.calls if m == "buddy-quests.accept"]), 1)
        rpc.offer["revision"] = str(int(first["offerRevision"]) + 20)
        clock[0] += 10
        profile.tick()
        second = [p for m, p in rpc.calls if m == "buddy-quests.accept"][-1]
        self.assertNotEqual(first["offerRevision"], second["offerRevision"])
        self.assertEqual(first["questId"], second["questId"])

    def test_only_intro_turnin_and_zero_or_one_actual_reward_choice(self):
        for choices in ([], [{"index": 2, "itemId": 123, "count": 1}], [{"index": 0, "itemId": 123, "count": 1}, {"index": 1, "itemId": 456, "count": 1}]):
            profile, rpc, clock, _ = self.rig()
            rpc.active, rpc.rewards = {783, 18}, choices
            profile.tick()
            clock[0] += 10
            profile.tick()
            claims = [p for m, p in rpc.calls if m == "buddy-quests.claim_reward"]
            if len(choices) > 1:
                self.assertFalse(claims)
                self.assertEqual(profile.status["phase"], "waiting_for_verified_prerequisite_reward_choice")
            else:
                self.assertEqual(len(claims), 1)
                self.assertEqual(claims[0]["noChoice"], not choices)
                self.assertEqual(claims[0]["expectedItemId"], 123 if choices else 0)
            self.assertTrue(all(p.get("questId") == 783 for m, p in self.mutate_calls(rpc) if m in ("buddy-quests.turn_in", "buddy-quests.claim_reward")))

    def test_unavailable_reward_evaluator_never_uses_a_python_handoff(self):
        profile,rpc,_,_=self.rig()
        rpc.active={783}
        original=sync.converse.quest_rewards.starter_handoff
        def unavailable(facts):
            with patch.dict(os.environ,{"BUDDY_LUAPOLICY_BINARY":"/nonexistent/starter-reward-evaluator"}):
                return original(facts)
        with patch.object(sync.converse.quest_rewards,"starter_handoff",side_effect=unavailable):
            profile.tick()
        self.assertEqual(profile.status["phase"],"waiting_for_shared_lua_prerequisite_reward_policy")
        self.assertFalse(self.mutate_calls(rpc))
        self.assertEqual(profile.data["reservations"],{})
        self.assertEqual(profile.data["nextMutationAt"],0)
        self.assertNotIn("lastMutation",profile.data)

    def test_lua_reward_output_cannot_invent_npc_menu_or_manual_travel(self):
        for action in ({"kind":"turn_in","npcGuid":"123","questId":783},
                       {"kind":"claim_reward","npcGuid":str(MCBRIDE),"questId":783,
                        "offerRevision":"99","choicePosition":0,"choiceIndex":0,"itemId":0},
                       {"kind":"travel","entry":196}):
            profile,rpc,_,_=self.rig()
            rpc.active={783}
            with patch.object(sync.converse.quest_rewards,"starter_handoff",return_value={"status":"ready","choice":action}):
                with self.assertRaises(sync.Error):profile.tick()
            self.assertFalse(self.mutate_calls(rpc))
            self.assertEqual(profile.data["reservations"],{})
            self.assertEqual(profile.data["nextMutationAt"],0)

    def test_wolf_shared_preference_retains_unrelated_full_uint32_item(self):
        profile,rpc,clock,_=self.rig()
        rpc.active,rpc.level={7,18,33},2
        rpc.rewards=[{"index":5,"itemId":2**32-1,"count":2**32-1},
                     {"index":4,"itemId":80,"count":1}]
        profile.tick()
        clock[0]+=10
        profile.tick()
        claim=next(p for m,p in rpc.calls if m=="buddy-quests.claim_reward")
        self.assertEqual(claim["expectedItemId"],80)
        self.assertEqual(claim["choiceIndex"],4)

    def test_unrelated_reward_offer_does_not_validate_its_unused_revision(self):
        profile,rpc,_,_=self.rig()
        rpc.active={783}
        state=rpc.state()
        state["turnInOffer"]={"questId":33,"revision":False,"npcGuid":False,"expiresInMs":False}
        self.assertTrue(profile.reward(state,rpc.session,profile.candidates(state),{783}))
        self.assertEqual(self.mutate_calls(rpc)[-1][0],"buddy-quests.turn_in")

    def test_single_reward_does_not_validate_unused_class_log_revision_or_last_mutation(self):
        profile,rpc,_,_=self.rig()
        rpc.active={783}
        rpc.reward={"questId":783,"npcGuid":str(MCBRIDE),"revision":"5","expiresInMs":"60000",
                    "source":"reward_offer","canComplete":True,"choices":[{"itemId":80,"count":1}]}
        profile.self_player={"classKnown":True,"classId":False}
        profile.data["lastMutation"]=False
        state=rpc.state();state["questLogRevision"]=False
        self.assertTrue(profile.reward(state,rpc.session,profile.candidates(state),{783}))
        claim=self.mutate_calls(rpc)[-1]
        self.assertEqual(claim[0],"buddy-quests.claim_reward")
        self.assertEqual(claim[1]["expectedItemId"],80)

    def test_unused_oversized_mutation_timers_do_not_cross_the_bridge_number_bound(self):
        for timer in (2**64-1,1e300,2**2048):
            for ready_claim in (False,True):
                profile,rpc,_,_=self.rig()
                rpc.active={783}
                profile.data["lastMutation"]={"method":"buddy-quests.turn_in","questId":33,"at":timer}
                if ready_claim:
                    rpc.reward={"questId":783,"npcGuid":str(MCBRIDE),"revision":"5","expiresInMs":"60000",
                                "source":"reward_offer","canComplete":True,"choices":[{"itemId":80,"count":1}]}
                state=rpc.state()
                self.assertTrue(profile.reward(state,rpc.session,profile.candidates(state),{783}))
                self.assertEqual(self.mutate_calls(rpc)[-1][0],
                                 "buddy-quests.claim_reward" if ready_claim else "buddy-quests.turn_in")

    def test_consulted_oversized_mutation_timer_rejects_before_reserving(self):
        profile,rpc,_,_=self.rig()
        rpc.active={783}
        profile.data["lastMutation"]={"method":"buddy-quests.turn_in","questId":783,"at":2**64-1}
        state=rpc.state()
        with self.assertRaises(sync.Error):
            profile.reward(state,rpc.session,profile.candidates(state),{783})
        self.assertFalse(self.mutate_calls(rpc))
        self.assertEqual(profile.data["reservations"],{})
        self.assertEqual(profile.data["nextMutationAt"],0)

    def test_unused_oversized_enum_text_does_not_cross_the_bridge_frame_bound(self):
        for field in ("source","method"):
            profile,rpc,_,_=self.rig()
            rpc.active={783}
            state=rpc.state()
            if field=="source":
                state["turnInOffer"]={"questId":783,"npcGuid":str(MCBRIDE),"source":"x"*35000,
                                      "canComplete":True,"expiresInMs":False}
            else:
                profile.data["lastMutation"]={"method":"x"*35000,"questId":783,"at":False}
            self.assertTrue(profile.reward(state,rpc.session,profile.candidates(state),{783}))
            self.assertEqual(self.mutate_calls(rpc)[-1][0],"buddy-quests.turn_in")

    def test_claim_timeout_never_replays_same_offer_and_needs_both_reward_proofs(self):
        profile, rpc, clock, _ = self.rig()
        rpc.active, rpc.apply_claim = {783}, False
        profile.tick()
        clock[0] += 10
        rpc.error_on = "buddy-quests.claim_reward"
        profile.tick()
        first = [p for m, p in rpc.calls if m == "buddy-quests.claim_reward"][0]
        rpc.reward["rewardRequested"] = False
        clock[0] += 10
        profile.tick()
        self.assertEqual(len([m for m, _ in rpc.calls if m == "buddy-quests.claim_reward"]), 1)
        self.assertFalse(profile.status.get("introRewardConfirmed"))
        clock[0] += 10
        profile.tick() # A new actual TurnIn response can produce a new bound reward offer.
        claims = [p for m, p in rpc.calls if m == "buddy-quests.claim_reward"]
        self.assertNotEqual(first["offerRevision"], claims[-1]["offerRevision"])
        rpc.active.remove(783)
        rpc.receipt = {"questId": 783, "status": "REWARD_CONFIRMED", "serverRewardObserved": True}
        profile.tick()
        self.assertFalse(profile.status.get("introRewardConfirmed"))

    def test_absent_unknown_foreign_or_unobserved_npc_never_gets_mutation(self):
        for mode in ("far", "unknown_log", "wrong_guid", "owner_absent", "wrong_npc"):
            profile, rpc, _, _ = self.rig()
            if mode == "far":
                rpc.nearby = False
            elif mode == "wrong_guid":
                rpc.guid = 99
            elif mode == "owner_absent":
                rpc.owner_present = False
            else:
                original = rpc.state
                def state():
                    value = original()
                    if mode == "unknown_log": value["questLogKnown"] = False
                    else:
                        for row in value["nearby"]: row["creature"]["entry"] = 999
                    return value
                rpc.state = state
            try:
                profile.tick()
            except sync.Error:
                pass
            self.assertFalse(self.mutate_calls(rpc), mode)

    def test_fixed_roster_independent_cooldowns_and_no_accept_unrelated_quest(self):
        for guid in (1, 4, 5):
            profile, rpc, clock, _ = self.rig(guid)
            rpc.intro_done = True
            profile.data["introRewardConfirmed"] = True
            rpc.entries = ((MCBRIDE, 197),)
            profile.tick()
            rpc.offer["quests"].insert(0, {"questId": 999, "available": True})
            clock[0] += 10
            profile.tick()
            self.assertEqual([p["questId"] for m, p in rpc.calls if m == "buddy-quests.accept"], [7])

    def test_pickup_adapter_keeps_exact_large_prior_query_times(self):
        profile,rpc,_,_=self.rig()
        other=MCBRIDE+1
        state=rpc.state()
        candidates=[(0,1,MCBRIDE,197),(0,7,other,197)]
        profile.data["queries"]=[{"npc":str(MCBRIDE),"signature":["old","1","1"],"at":2**64-1},
                                 {"npc":str(other),"signature":["old","1","1"],"at":2**64-2}]
        with patch.object(profile,"mutate",return_value=True) as mutate:
            profile.pickup(state,rpc.session,candidates,set(),{7},197,1)
        self.assertEqual(mutate.call_args.args[0],"buddy-quests.offer")
        self.assertEqual(mutate.call_args.args[1]["npcGuid"],str(other))

    def test_pickup_missing_evaluator_and_invented_outputs_never_reserve(self):
        for decision in (None,{"status":"ready","choice":{"kind":"offer","npcGuid":"123"}},
                         {"status":"ready","choice":{"kind":"accept","npcGuid":str(WILLEM),"questId":999,"offerRevision":"99"}},
                         {"status":"ready","choice":{"kind":"travel","entry":197}}):
            profile,rpc,_,saved=self.rig()
            state=rpc.state();candidates=profile.candidates(state)
            if decision is None:
                with patch.dict(os.environ,{"BUDDY_LUAPOLICY_BINARY":"/nonexistent/starter-pickup-evaluator"}):
                    profile.pickup(state,rpc.session,candidates,set(),{783},823,1)
                self.assertEqual(profile.status["phase"],"waiting_for_shared_lua_starter_pickup_policy")
            else:
                with patch.object(sync.pickup_policy,"evaluate",return_value=decision):
                    with self.assertRaises(sync.Error):profile.pickup(state,rpc.session,candidates,set(),{783},823,1)
            self.assertFalse(self.mutate_calls(rpc))
            self.assertFalse(saved)
            self.assertEqual(profile.data["reservations"],{})

    def test_pickup_source_edit_and_invalid_candidate_preserve_ambiguous_menu(self):
        profile,rpc,clock,_=self.rig()
        rpc.active,rpc.level={7,18},2
        rpc.apply_accept=False;rpc.error_on="buddy-quests.accept"
        profile.tick();clock[0]+=10;profile.tick()
        reserved=copy.deepcopy(profile.data["reservations"])
        self.assertIn("accept:5261",reserved)
        for row in rpc.offer["quests"]:row["acceptRequested"]=False
        count=len(self.mutate_calls(rpc))
        source=sync.pickup_policy.SOURCE.read_text()
        with tempfile.TemporaryDirectory() as folder:
            path=Path(folder)/"party-quests.lua"
            path.write_text(source.replace('local sixty={negative=false,whole="60",fraction=""}',
                                           'local sixty={negative=false,whole="300",fraction=""}'))
            self.assertNotEqual(path.read_text(),source)
            with patch.object(sync.pickup_policy,"SOURCE",path):
                clock[0]+=60;profile.tick()
                self.assertEqual(profile.status["phase"],"waiting_for_new_offer_or_prerequisite_state")
                self.assertEqual(len(self.mutate_calls(rpc)),count)
                path.write_text("invalid candidate")
                profile.tick()
                self.assertEqual(profile.status["phase"],"waiting_for_shared_lua_starter_pickup_policy")
                self.assertEqual(profile.data["reservations"],reserved)
                self.assertEqual(len(self.mutate_calls(rpc)),count)
                path.write_text(source)
                profile.tick()
        self.assertEqual(rpc.session,"quest-session")
        self.assertEqual(profile.data["reservations"],reserved)
        self.assertEqual([m for m,_ in rpc.calls].count("buddy-quests.accept"),1)
        self.assertEqual(self.mutate_calls(rpc)[-1][0],"buddy-quests.offer")

    def test_reservation_persist_failure_prevents_every_mutation(self):
        profile, rpc, _, _ = self.rig()
        profile.save = lambda value: (_ for _ in ()).throw(OSError("disk full"))
        with self.assertRaises(OSError):
            profile.tick()
        self.assertFalse(self.mutate_calls(rpc))

    def test_stop_between_read_and_mutation_never_sends_and_state_identity_is_exact(self):
        profile, rpc, _, _ = self.rig()
        original = rpc.call
        def call(method, params):
            result = original(method, params)
            if method == "buddy-quests.discover": profile.stopped = lambda: True
            return result
        rpc.call = call
        with self.assertRaises(sync.converse.Stopped):
            profile.tick()
        self.assertFalse(self.mutate_calls(rpc))
        with tempfile.TemporaryDirectory() as temporary:
            folder = sync.converse.ensure_state(Path(temporary))
            sync.converse.write_json(folder / "status.json", {"identity": {"wrong": True}, "instance": "other"})
            with self.assertRaisesRegex(sync.Error, "different quest goal"):
                sync.main(["stop", "--state-dir", str(folder)])
            self.assertFalse((folder / "stop.json").exists())
            with contextlib.redirect_stdout(io.StringIO()):
                sync.converse.write_json(folder / "status.json", {"identity": sync.IDENTITY})
                self.assertEqual(sync.main(["status", "--state-dir", str(folder)]), 0)
        with self.assertRaises(sync.Error):
            self.rig(stored={"identity": {"guid": 99}})

    def test_autonomous_holds_and_restores_follow_with_exact_session_and_offline_owner(self):
        profile, rpc, clock, saved = self.rig()
        profile.autonomous, rpc.owner_present = True, False
        profile.tick()
        self.assertFalse(rpc.actions["following"])
        self.assertTrue(saved[-1]["routine"]["originalFollowing"])
        profile.stopped = lambda: True
        profile.cleanup() # Cleanup deliberately bypasses cooperative cancellation.
        self.assertTrue(rpc.actions["following"])
        self.assertTrue(profile.data["routine"]["closed"])
        profile, rpc, _, _ = self.rig()
        profile.autonomous = True
        profile.tick()
        rpc.session = "replacement-body"
        profile.cleanup()
        self.assertFalse(rpc.actions["following"])
        self.assertEqual(profile.status["cleanup"], "body_changed_no_stale_restore")

    def test_unapplied_ambiguous_follow_hold_blocks_goalwork_until_observed_disabled(self):
        profile, rpc, clock, _ = self.rig()
        profile.autonomous = True
        original = rpc.call
        fail = [True]
        def call(method, params):
            if method == "buddy-actions.follow" and params == {"enabled":False} and fail[0]:
                rpc.calls.append((method,copy.deepcopy(params)))
                raise sync.Error("disable outcome unknown")
            return original(method,params)
        rpc.call = call
        profile.tick()
        self.assertTrue(rpc.actions["following"])
        clock[0] += 2
        profile.tick()
        self.assertEqual(profile.status["phase"],"waiting_for_confirmed_follow_hold")
        self.assertFalse(any(m.startswith("buddy-quests.") and m != "buddy-quests.discover" for m,_ in rpc.calls))
        fail[0] = False
        clock[0] += 8
        profile.tick()
        self.assertFalse(rpc.actions["following"])
        self.assertTrue(profile.data["routine"]["originalFollowing"])

    def test_autonomous_real_nav_transaction_owns_and_releases_token_on_timeout(self):
        profile, rpc, clock, _ = self.rig()
        profile.autonomous, rpc.owner_present, rpc.far = True, False, True
        rpc.actions["navAvailable"] = False # Initial unknown must not block the first verified planner request.
        profile.tick()
        clock[0] += 10
        rpc.error_on = "buddy-actions.travel_quest"
        profile.tick()
        methods = [method for method, _ in rpc.calls]
        self.assertEqual(methods[-3:], ["buddy-actions.acquire_interaction", "buddy-actions.travel_quest", "buddy-actions.release_interaction"])
        self.assertEqual(rpc.calls[-3][1], {"owner": "quests", "leaseMs": 30000})
        self.assertEqual(rpc.calls[-1][1], {"owner": "quests", "token": "owned-token"})
        self.assertNotIn("travelToken", profile.data["routine"])
        self.assertFalse(any(method == "buddy-quests.accept" for method in methods))

    def test_stop_after_lease_acquisition_still_releases_exact_token(self):
        profile, rpc, clock, _ = self.rig()
        profile.autonomous, rpc.far = True, True
        profile.tick()
        original = rpc.call
        def call(method, params):
            result = original(method, params)
            if method == "buddy-actions.acquire_interaction": profile.stopped = lambda: True
            return result
        rpc.call = call
        clock[0] += 10
        with self.assertRaises(sync.converse.Stopped):
            profile.tick()
        self.assertEqual(rpc.calls[-1], ("buddy-actions.release_interaction", {"owner": "quests", "token": "owned-token"}))
        self.assertFalse(any(m == "buddy-actions.travel_quest" for m, _ in rpc.calls))

    def test_travel_rpc_uses_35second_timeout_and_other_controls_remain_bounded(self):
        rpc = sync.SyncRPC("http://127.0.0.1:8086/rpc")
        timeouts = []
        class Response:
            def __enter__(self): return self
            def __exit__(self, *args): pass
            def read(self, limit): return json.dumps({"jsonrpc":"2.0","id":rpc.request_id,"result":{}}).encode()
        def open(request, timeout):
            timeouts.append(timeout)
            return Response()
        with patch.object(rpc.opener, "open", side_effect=open):
            rpc.call("buddy-actions.travel_quest", {"token":"opaque","npcGuid":str(MCBRIDE)})
            rpc.call("buddy-actions.release_interaction", {"owner":"quests","token":"opaque"})
            with self.assertRaises(sync.Error):rpc.call("arbitrary.raw_packet",{})
        self.assertEqual(timeouts,[35,6])

    def test_practice_uses_installed_control_and_level2_stops_before_quest_work(self):
        profile, rpc, clock, _ = self.rig(4)
        profile.autonomous, rpc.owner_present, rpc.active = True, False, {7,3903}
        profile.tick()
        clock[0] += 10
        profile.tick()
        self.assertTrue(rpc.actions["practicing"])
        self.assertEqual(profile.status["phase"], "practicing_to_level_2")

        rpc.level = 2
        clock[0] += 10
        profile.tick()
        self.assertTrue(rpc.actions["practicing"])
        self.assertEqual(profile.status["phase"], "finishing_current_practice_engagement_before_quests")
        rpc.actions["practicing"] = False # Go's observed out-of-combat auto-stop.
        clock[0] += 2
        profile.tick()
        self.assertFalse(rpc.actions["practicing"])
        self.assertFalse(any(method == "buddy-quests.accept" for method, _ in rpc.calls))
        profile, rpc, clock, _ = self.rig(4)
        profile.autonomous, rpc.active, rpc.actions["navAvailable"] = True, {7,3903}, False
        profile.tick()
        clock[0] += 10
        profile.tick()
        self.assertTrue(any(method == "buddy-actions.practice" for method, _ in rpc.calls))
        self.assertEqual(profile.status["phase"], "practicing_to_level_2")

    def test_objective_policy_failure_at_each_stage_cannot_patch_ownership_or_write(self):
        for stage in ("classify","post_hold","loot","practice","before_quests","fallback"):
            profile,rpc,clock,_=self.rig()
            profile.autonomous,rpc.active,rpc.level,rpc.wolves_complete=True,{7,18,33},2,False
            if stage=="fallback":rpc.active,rpc.level={7,3903},1
            profile.tick()  # Establish the existing, observed follow hold.
            if stage=="before_quests":rpc.wolves_complete=True
            before=len(self.mutate_calls(rpc))
            owned=profile.data["routine"]["practiceOwned"]
            original=sync.objective_policy.evaluate
            def rejected(facts):
                return {"status":"pending"} if facts["stage"]==stage else original(facts)
            clock[0]+=10
            with patch.object(sync.objective_policy,"evaluate",side_effect=rejected):profile.tick()
            self.assertEqual(profile.status["phase"],"waiting_for_shared_lua_starter_objective_policy",stage)
            self.assertEqual(len(self.mutate_calls(rpc)),before,stage)
            self.assertEqual(profile.data["routine"]["practiceOwned"],owned,stage)

    def test_unavailable_objective_evaluator_has_no_python_objective_fallback(self):
        profile,rpc,_,_=self.rig()
        profile.autonomous,rpc.active,rpc.level,rpc.wolves_complete=True,{7,18,33},2,False
        with patch.dict(os.environ,{"BUDDY_LUAPOLICY_BINARY":"/nonexistent/starter-objective-evaluator"}):profile.tick()
        self.assertEqual(profile.status["phase"],"waiting_for_shared_lua_starter_objective_policy")
        self.assertFalse(self.mutate_calls(rpc))
        self.assertNotIn("routine",profile.data)

    def test_uncertain_practice_enable_keeps_saved_ownership_and_observed_lease_prevents_replay(self):
        profile,rpc,clock,saved=self.rig()
        profile.autonomous,rpc.active,rpc.level,rpc.wolves_complete=True,{7,18,33},2,False
        profile.tick()
        original_call=rpc.call
        def ambiguous(method,params):
            result=original_call(method,params)
            if method=="buddy-actions.practice" and params.get("enabled") is True:
                raise sync.Error("Practice enable transport outcome is unknown")
            return result
        rpc.call=ambiguous
        clock[0]+=10
        profile.tick()
        self.assertEqual(profile.status["phase"],"request_unconfirmed_reconciling_fresh_state")
        self.assertTrue(saved[-1]["routine"]["practiceOwned"])
        clock[0]+=10
        profile.tick()
        self.assertEqual([m for m,_ in rpc.calls].count("buddy-actions.practice"),1)
        self.assertTrue(profile.data["routine"]["practiceOwned"])
        rpc.call=original_call
        profile.cleanup()
        self.assertFalse(rpc.actions["practicing"])
        self.assertTrue(profile.data["routine"]["closed"])

    def test_lua_cannot_clear_owned_or_uncertain_practice_before_observed_stop(self):
        for state in ({"practicing":True},{"autoAttacking":True},
                      {"autoAttackRequested":True},{"autoAttackUnconfirmed":True}):
            profile,rpc,_,saved=self.rig()
            profile.data["routine"]={"session":rpc.session,"practiceOwned":True}
            profile.actions=state
            with patch.object(sync.objective_policy,"evaluate",return_value={"status":"ready",
                              "choice":{"kind":"continue_quests","practiceOwned":False}}):
                with self.assertRaises(sync.Error):profile.stop_practice_for_quests()
            self.assertTrue(profile.data["routine"]["practiceOwned"])
            self.assertFalse(saved)
            self.assertFalse(self.mutate_calls(rpc))

    def test_lua_cannot_stop_an_unowned_uncertain_attack_as_practice(self):
        profile,rpc,_,saved=self.rig()
        profile.data["routine"]={"session":rpc.session,"practiceOwned":False}
        profile.actions={"autoAttackUnconfirmed":True}
        with patch.object(sync.objective_policy,"evaluate",return_value={"status":"ready",
                          "choice":{"kind":"disable_practice"}}):
            with self.assertRaises(sync.Error):profile.stop_practice_for_quests()
        self.assertFalse(saved)
        self.assertFalse(self.mutate_calls(rpc))

    def test_objective_source_edit_and_broken_candidate_keep_same_profile_ownership(self):
        profile,rpc,clock,_=self.rig()
        profile.autonomous,rpc.active,rpc.level,rpc.wolves_complete=True,{7,18,33},2,False
        profile.tick();clock[0]+=10;profile.tick()
        rpc.actions["practiceLeaseRemainingMs"]="90000"
        clock[0]+=10;profile.tick()
        count=[m for m,_ in rpc.calls].count("buddy-actions.practice")
        self.assertEqual(count,1)
        source=sync.objective_policy.SOURCE.read_text()
        with tempfile.TemporaryDirectory() as folder:
            path=Path(folder)/"starter-objectives.lua"
            path.write_text(source.replace('#lease>5 or #lease==5 and lease>"60000"',
                                           '#lease>6 or #lease==6 and lease>"120000"'))
            self.assertNotEqual(path.read_text(),source)
            with patch.object(sync.objective_policy,"SOURCE",path):
                original_call=rpc.call
                def ambiguous(method,params):
                    result=original_call(method,params)
                    if method=="buddy-actions.practice" and params.get("enabled") is True:
                        raise sync.Error("Practice renewal transport outcome is unknown")
                    return result
                rpc.call=ambiguous
                clock[0]+=10;profile.tick()
                self.assertEqual([m for m,_ in rpc.calls].count("buddy-actions.practice"),count+1)
                self.assertTrue(profile.data["routine"]["practiceOwned"])
                path.write_text("invalid candidate")
                clock[0]+=10;profile.tick()
                self.assertEqual(profile.status["phase"],"waiting_for_shared_lua_starter_objective_policy")
                self.assertEqual([m for m,_ in rpc.calls].count("buddy-actions.practice"),count+1)
                self.assertTrue(profile.data["routine"]["practiceOwned"])
                path.write_text(source)
                clock[0]+=10;profile.tick()
        self.assertEqual(rpc.session,"quest-session")
        self.assertEqual([m for m,_ in rpc.calls].count("buddy-actions.practice"),count+1)
        self.assertTrue(profile.data["routine"]["practiceOwned"])

    def test_party_xp_level2_does_not_interrupt_owned_current_fight(self):
        profile, rpc, clock, _ = self.rig(4)
        profile.autonomous, rpc.active = True, {7,3903}
        profile.tick()
        clock[0] += 10
        profile.tick()
        before = len(self.mutate_calls(rpc))
        rpc.level, rpc.combat = 2, True
        rpc.actions["practicePhase"] = "finishing_current_fight_to_level2"
        clock[0] += 10
        profile.tick()
        self.assertEqual(len(self.mutate_calls(rpc)), before)
        self.assertEqual(profile.status["phase"], "finishing_current_practice_engagement_before_quests")
        rpc.active = set(sync.DESIRED) # Even external acceptance cannot force goal cleanup mid-fight.
        profile.tick()
        self.assertNotEqual(profile.status["phase"], "matched_requested_active_quest_ids")
        rpc.combat = False
        rpc.actions.update(practicing=False, practicePhase="level_2_reached")
        profile.tick()
        self.assertEqual(profile.status["phase"], "matched_requested_active_quest_ids")

    def test_level2_with7_near_mcbride_travels_to_willem_then_refreshes_each_offer(self):
        profile, rpc, clock, _ = self.rig()
        profile.autonomous, rpc.level, rpc.active, rpc.far = True, 2, {7}, True
        original = rpc.state
        def state():
            value = original()
            for row in value["nearby"]:
                if row["creature"]["entry"] == 197:
                    row.update(canInteract=True, distance=2, blockedReason="")
            return value
        rpc.state = state
        profile.tick() # Hold previous follow intent first.
        clock[0] += 10
        profile.tick()
        travel = next(params for method, params in rpc.calls if method == "buddy-actions.travel_quest")
        self.assertEqual(travel["npcGuid"], str(WILLEM))
        self.assertEqual(profile.status["neededQuestgiverEntry"], 823)
        for _ in range(40):
            clock[0] += 10
            profile.tick()
            if profile.status.get("exactGoalMatched"):
                break
        accepts = [params for method, params in rpc.calls if method == "buddy-quests.accept"]
        self.assertEqual([params["questId"] for params in accepts], [18, 5261, 33, 3903])
        self.assertNotEqual(accepts[0]["offerRevision"], accepts[1]["offerRevision"])
        self.assertTrue(all(params["npcGuid"] == str(EAGAN if params["questId"] == 33 else WILLEM) for params in accepts))
        self.assertEqual(profile.status["activeQuestIds"], [7,18,3903])

    def test_wolf_prerequisite_full_chain_all_roles_uses_own_complete_and_actual_reward_choice(self):
        for guid, expected_item in ((1,80),(4,80),(5,6070)):
            profile,rpc,clock,saved = self.rig(guid)
            profile.autonomous, rpc.active, rpc.level = True, {7,18}, 2
            rpc.wolves_complete = False
            rpc.rewards_by_quest[33] = [{"index":4,"itemId":6070,"count":1},{"index":1,"itemId":80,"count":1}]
            practiced = False
            for _ in range(70):
                profile.tick()
                if 33 in rpc.active and rpc.actions.get("practicing"):
                    practiced = True
                    self.assertEqual(profile.status["phase"],"practicing_for_wolf_quest")
                    self.assertFalse(any(m == "buddy-quests.turn_in" and p["questId"] == 33 for m,p in rpc.calls))
                    # Only this authoritative own-log transition allows turn-in.
                    rpc.wolves_complete = True
                    rpc.actions.update(practicing=False,practicePhase="quest_objectives_complete")
                if profile.status.get("exactGoalMatched"):
                    break
                clock[0] += 10
            self.assertTrue(practiced)
            self.assertEqual(profile.status["activeQuestIds"],[7,18,3903])
            self.assertEqual([p["questId"] for m,p in rpc.calls if m == "buddy-quests.accept"],[5261,33,3903])
            turns=[p for m,p in rpc.calls if m == "buddy-quests.turn_in"]
            self.assertEqual([p["questId"] for p in turns],[5261,33])
            self.assertTrue(all(p["npcGuid"] == str(EAGAN) for p in turns))
            claim=next(p for m,p in rpc.calls if m == "buddy-quests.claim_reward" and p["questId"] == 33)
            self.assertEqual(claim["expectedItemId"],expected_item)
            self.assertEqual(claim["choiceIndex"],1 if expected_item == 80 else 4)
            self.assertFalse(claim["noChoice"])
            self.assertEqual(profile.status["confirmedPrerequisiteQuestIds"],[33,5261])
            restarted,_,_,_=self.rig(guid,stored=saved[-1])
            self.assertTrue(restarted.data["confirmedRewards"]["33"]["serverRewardObserved"])

    def test_wolf_objective_counts_never_substitute_for_complete_and_finishing_fight_waits(self):
        profile,rpc,clock,_=self.rig()
        profile.autonomous,rpc.active,rpc.level,rpc.wolves_complete=True,{7,18,33},2,False
        original=rpc.state
        def state():
            value=original()
            next(q for q in value["questLog"] if q["questId"] == 33)["objectiveCounts"]=[8,8,8,8]
            return value
        rpc.state=state
        profile.tick()
        clock[0]+=10
        profile.tick()
        self.assertTrue(rpc.actions["practicing"])
        self.assertFalse(any(m == "buddy-quests.turn_in" for m,_ in rpc.calls))
        rpc.wolves_complete,rpc.combat=True,True
        rpc.actions["practicePhase"]="finishing_current_fight_to_goal"
        before=len(self.mutate_calls(rpc))
        clock[0]+=10
        profile.tick()
        self.assertEqual(len(self.mutate_calls(rpc)),before)
        self.assertEqual(profile.status["phase"],"finishing_current_practice_engagement_before_quests")
        rpc.actions.update(practicing=False,practicePhase="quest_objectives_complete")
        rpc.combat=False
        clock[0]+=10
        profile.tick()
        self.assertEqual(rpc.calls[-1][0],"buddy-quests.turn_in")
        self.assertEqual(rpc.calls[-1][1]["questId"],33)

    def test_unknown_actual_wolf_reward_choices_or_class_pause_without_claim(self):
        for guid,choices,unknown_class in ((1,[{"index":0,"itemId":999,"count":1},{"index":1,"itemId":6070,"count":1}],False),
                                          (5,[{"index":0,"itemId":80,"count":1},{"index":1,"itemId":999,"count":1}],False),
                                          (4,[{"index":0,"itemId":80,"count":1},{"index":1,"itemId":6070,"count":1}],True)):
            profile,rpc,clock,_=self.rig(guid)
            rpc.active,rpc.level,rpc.rewards={7,18,33},2,choices
            if unknown_class:
                original=rpc.world
                def world():
                    value=original()
                    value["self"]["classKnown"]=False
                    return value
                rpc.world=world
            profile.tick()
            clock[0]+=10
            profile.tick()
            self.assertEqual(profile.status["phase"],"waiting_for_verified_prerequisite_reward_choice")
            self.assertFalse(any(m == "buddy-quests.claim_reward" for m,_ in rpc.calls))

    def test_prerequisite_reward_needs_both_proofs_and_survives_new_body_without_reaccept(self):
        profile,rpc,clock,saved=self.rig()
        rpc.active,rpc.level={7,18},2
        rpc.receipt={"questId":33,"status":"REWARD_CONFIRMED","serverRewardObserved":True}
        profile.tick()
        self.assertNotIn("33",profile.data["confirmedRewards"])
        rpc.receipt.update(logRemovalConfirmed=True,npcGuid=str(EAGAN),offerRevision="9",confirmedLogRevision="7")
        profile.tick()
        self.assertIn("33",profile.data["confirmedRewards"])
        restarted,_,_,_=self.rig(stored=saved[-1])
        restarted.rpc,restarted.now=rpc,lambda:clock[0]
        rpc.session,rpc.receipt,rpc.wolves_done="new-body",{},True
        before=len(rpc.calls)
        for _ in range(8):
            clock[0]+=10
            restarted.tick()
            if restarted.status.get("exactGoalMatched"):break
        self.assertTrue(restarted.status["exactGoalMatched"])
        self.assertFalse(any(m == "buddy-quests.accept" and p["questId"] in sync.PREREQUISITES for m,p in rpc.calls[before:]))

    def test_malformed_prerequisite_reward_choice_never_emits_claim(self):
        for choices in ([{"index":6,"itemId":80,"count":1}],
                        [{"index":0,"itemId":80,"count":0}],
                        [{"index":0,"itemId":2**64-1,"count":1}],
                        [{"index":0,"itemId":80,"count":2**64-1}],
                        [{"index":0,"itemId":80,"count":1},{"index":0,"itemId":6070,"count":1}]):
            profile,rpc,clock,_=self.rig()
            rpc.active,rpc.rewards={7,18,33},choices
            profile.tick()
            clock[0]+=10
            with self.assertRaises(sync.Error):profile.tick()
            self.assertFalse(any(m == "buddy-quests.claim_reward" for m,_ in rpc.calls))

    def test_wolf_practice_confirms_enabled_loot_on_fresh_tick_before_starting(self):
        profile,rpc,clock,_=self.rig()
        profile.autonomous,rpc.active,rpc.level,rpc.wolves_complete=True,{7,18,33},2,False
        rpc.loot["enabled"]=False
        profile.tick() # Hold the previous following intent first.
        clock[0]+=10
        profile.tick()
        self.assertEqual(rpc.calls[-1],("buddy-loot.enable",{"enabled":True}))
        self.assertEqual(profile.status["phase"],"waiting_for_confirmed_wolf_looting")
        self.assertFalse(any(m == "buddy-actions.practice" for m,_ in rpc.calls))
        clock[0]+=10
        profile.tick()
        methods=[m for m,_ in rpc.calls]
        self.assertEqual(methods[-2:],["buddy-loot.state","buddy-actions.practice"])
        self.assertTrue(rpc.actions["practicing"])
        self.assertEqual(len([m for m,_ in rpc.calls if m == "buddy-loot.enable"]),1)

    def test_wolf_loot_unavailable_waits_and_other_practice_never_enables_loot(self):
        profile,rpc,clock,_=self.rig()
        profile.autonomous,rpc.active,rpc.level,rpc.wolves_complete=True,{7,18,33},2,False
        profile.tick()
        original=rpc.call
        def call(method,params):
            if method == "buddy-loot.state":raise sync.Error("module unavailable")
            return original(method,params)
        rpc.call=call
        clock[0]+=10
        profile.tick()
        self.assertEqual(profile.status["phase"],"waiting_for_wolf_loot_module")
        self.assertFalse(any(m in ("buddy-actions.practice","buddy-loot.enable") for m,_ in rpc.calls))
        for active,complete in (({7,3903},False),({7,18,33},True)):
            profile,rpc,clock,_=self.rig(4)
            profile.autonomous,rpc.active,rpc.wolves_complete=True,active,complete
            rpc.loot["enabled"]=False
            profile.tick()
            clock[0]+=10
            profile.tick()
            self.assertFalse(any(m.startswith("buddy-loot.") for m,_ in rpc.calls))

    def test_observed_waypoint_reveals_required_giver_and_same_scope_never_retries(self):
        profile,rpc,clock,saved=self.rig(4)
        rpc.entries,rpc.far=((EAGAN,196),),True
        profile.data["routine"]={"session":rpc.session}
        profile.actions={"position":{"known":True,"mapKnown":True,"x":-8846.0,"y":-70.0,"z":82.0}}
        unseen_target_state=rpc.state()
        rpc.error_on="buddy-actions.travel_quest"
        self.assertTrue(profile.travel(unseen_target_state,entry=823))
        sent=[p for m,p in rpc.calls if m == "buddy-actions.travel_quest"]
        self.assertEqual(sent,[{"token":"owned-token","npcGuid":str(EAGAN)}])
        self.assertEqual(profile.status["waypointQuestgiverEntry"],196)
        # A restart and an own-log revision change do not authorize retrying an
        # ambiguous waypoint from unchanged position and visibility.
        restarted,_,_,_=self.rig(4,stored=saved[-1])
        restarted.rpc,restarted.now,restarted.actions=rpc,lambda:clock[0],copy.deepcopy(profile.actions)
        clock[0]+=20
        before=len(rpc.calls)
        unseen_target_state["questLogRevision"]="99"
        self.assertFalse(restarted.travel(unseen_target_state,entry=823))
        self.assertEqual(len(rpc.calls),before)
        self.assertEqual(restarted.status["phase"],"waiting_for_new_position_or_questgiver_observation")
        # A real client position change permits one new bounded attempt.
        restarted.actions["position"]["x"]+=9
        rpc.error_on=None
        self.assertTrue(restarted.travel(unseen_target_state,entry=823))
        # Once Willem is actually observed, route directly to his GUID.
        clock[0]+=10
        rpc.entries,rpc.far=((EAGAN,196),(WILLEM,823)),True
        self.assertTrue(restarted.travel(rpc.state(),entry=823))
        self.assertEqual([p["npcGuid"] for m,p in rpc.calls if m == "buddy-actions.travel_quest"],
                         [str(EAGAN),str(EAGAN),str(WILLEM)])
        self.assertFalse(any(m.startswith("buddy-quests.") for m,_ in rpc.calls))

    def test_waypoint_requires_safe_observation_position_and_absent_required_target(self):
        for mode in ("unknown_position","wrong_map","moving_giver","foreign_giver","unsafe_required_target"):
            profile,rpc,_,_=self.rig(4)
            profile.data["routine"]={"session":rpc.session}
            profile.actions={"position":{"known":True,"mapKnown":True,"x":-8846.0,"y":-70.0,"z":82.0}}
            rpc.entries,rpc.far=((EAGAN,196),),True
            state=rpc.state()
            if mode == "unknown_position":profile.actions["position"]["known"]=False
            elif mode == "wrong_map":state["nearby"][0]["creature"]["mapId"]=1
            elif mode == "moving_giver":state["nearby"][0]["creature"]["movementFlags"]=1
            elif mode == "foreign_giver":state["nearby"][0]["creature"]["entry"]=999
            else:
                unsafe=copy.deepcopy(state["nearby"][0])
                unsafe["creature"].update(entry=823,guid=str(WILLEM),alive=False)
                state["nearby"].append(unsafe)
            self.assertFalse(profile.travel(state,entry=823),mode)
            self.assertFalse(rpc.calls,mode)

    def test_waypoint_scope_memory_is_finite_and_body_session_changes_reset_it(self):
        profile,rpc,clock,_=self.rig(4)
        profile.data["routine"]={"session":rpc.session}
        profile.actions={"position":{"known":True,"mapKnown":True,"x":-8846.0,"y":-70.0,"z":82.0}}
        profile.data["waypoints"]={"session":rpc.session,"scopes":[str(i) for i in range(32)]}
        rpc.entries,rpc.far=((EAGAN,196),),True
        self.assertFalse(profile.travel(rpc.state(),entry=823))
        self.assertFalse(rpc.calls)
        rpc.session="new-body"
        profile.data["routine"]={"session":rpc.session}
        self.assertTrue(profile.travel(rpc.state(),entry=823))
        self.assertEqual(len(profile.data["waypoints"]["scopes"]),1)

    def test_busy_waypoint_lease_does_not_consume_route_scope_and_save_failure_releases_token(self):
        profile,rpc,clock,_=self.rig(4)
        profile.data["routine"]={"session":rpc.session}
        profile.actions={"position":{"known":True,"mapKnown":True,"x":-8846.0,"y":-70.0,"z":82.0}}
        rpc.entries,rpc.far=((EAGAN,196),),True
        original=rpc.call
        def busy(method,params):
            if method == "buddy-actions.acquire_interaction":raise sync.Error("lease busy")
            return original(method,params)
        rpc.call=busy
        self.assertTrue(profile.travel(rpc.state(),entry=823))
        self.assertNotIn("waypoints",profile.data)
        clock[0]+=10
        rpc.call=original
        def save(value):
            if value.get("waypoints"):raise OSError("disk full")
        profile.save=save
        with self.assertRaises(OSError):profile.travel(rpc.state(),entry=823)
        self.assertEqual(rpc.calls[-1],("buddy-actions.release_interaction",{"owner":"quests","token":"owned-token"}))
        self.assertFalse(any(m == "buddy-actions.travel_quest" for m,_ in rpc.calls))

    def test_observed_giver_history_requires_matching_guid_entry_map_and_northshire_bounds(self):
        profile,rpc,_,saved=self.rig()
        row={"creature":{"guid":str(EAGAN),"entry":196,"present":True,"aliveKnown":True,"alive":True,
                         "mapKnown":True,"positionKnown":True,"npcFlags":2,"x":-8869.0,"y":-163.0,"z":80.0}}
        for bad in ({"mapId":1},{"x":-10000},{"z":float('nan')},{"guid":str(WILLEM)},{"entry":999}):
            candidate=copy.deepcopy(row)
            candidate["creature"].update(bad)
            profile.remember_observed_givers({"nearby":[candidate]},rpc.session)
            self.assertFalse(profile.data["knownGivers"])
        profile.remember_observed_givers({"nearby":[row]},rpc.session)
        self.assertEqual(profile.data["knownGivers"]["196"],{"guid":str(EAGAN),"source":"observed","session":rpc.session})
        self.assertTrue(saved)
        self.assertFalse(any(key in profile.data["knownGivers"]["196"] for key in ("x","y","z")))

    def test_actual_historical_receipt_migrates_across_body_and_landmark_never_proves_interaction(self):
        profile,rpc,clock,_=self.rig()
        stored=copy.deepcopy(profile.data)
        stored.pop("knownGivers")
        stored["confirmedRewards"]={"5261":{"npcGuid":str(EAGAN),"session":"prior-body",
            "serverRewardObserved":True,"logRemovalConfirmed":True}}
        migrated,_,_,_=self.rig(stored=stored)
        self.assertEqual(migrated.data["knownGivers"]["196"]["source"],"reward_receipt")
        migrated.rpc,migrated.now=rpc,lambda:clock[0]
        migrated.data["routine"]={"session":rpc.session}
        migrated.actions={"position":{"known":True,"mapKnown":True,"x":-8846.0,"y":-70.0,"z":82.0}}
        original=rpc.call
        def call(method,params):
            result=original(method,params)
            return {"reason":"quest_travel_landmark_reached_requires_observation"} if method == "buddy-actions.travel_quest" else result
        rpc.call=call
        self.assertTrue(migrated.travel({"sessionId":rpc.session,"nearby":[]},entry=196))
        self.assertEqual(migrated.status["phase"],"landmark_route_stopped_rediscovering_actual_questgiver")
        self.assertEqual(migrated.status["rememberedQuestgiverEntry"],196)
        self.assertEqual(next(p for m,p in rpc.calls if m == "buddy-actions.travel_quest"),
                         {"token":"owned-token","npcGuid":str(EAGAN)})
        self.assertFalse(any(m.startswith("buddy-quests.") for m,_ in rpc.calls))
        clock[0]+=10
        self.assertFalse(migrated.travel({"sessionId":rpc.session,"nearby":[]},entry=196))

    def test_historical_bindings_migrate_only_matching_actual_giver_guids(self):
        profile,_,_,_=self.rig()
        stored=copy.deepcopy(profile.data)
        stored["reservations"]={"accept:33":["old",str(WILLEM),"9"],"accept:18":["old",str(WILLEM),"10"]}
        stored["queries"]=[{"npc":"196","signature":["old","1","2"],"at":1000}]
        migrated,_,_,_=self.rig(stored=stored)
        self.assertEqual(set(migrated.data["knownGivers"]),{"823"})
        self.assertEqual(migrated.data["knownGivers"]["823"]["guid"],str(WILLEM))
        stored["knownGivers"]={"196":{"guid":str(WILLEM),"source":"observed","session":"old"}}
        with self.assertRaises(sync.Error):self.rig(stored=stored)

    def test_remembered_landmark_never_overrides_current_observed_unsafe_giver(self):
        profile,rpc,_,_=self.rig()
        profile.remember_giver(EAGAN,"observed",rpc.session)
        profile.actions={"position":{"known":True,"mapKnown":True,"x":-8846.0,"y":-70.0,"z":82.0}}
        state={"sessionId":rpc.session,"nearby":[{"distanceKnown":True,"distance":30,
               "creature":{"guid":str(EAGAN),"entry":196,"present":True,"aliveKnown":True,"alive":False}}]}
        self.assertFalse(profile.travel(state,entry=196))
        self.assertFalse(rpc.calls)

    def test_legacy_active_goal_remains_stoppable_after_chain_identity_expands(self):
        with tempfile.TemporaryDirectory() as temporary:
            folder=sync.converse.ensure_state(Path(temporary))
            with os.fdopen(sync.converse.private_open(folder/"lock",os.O_RDWR|os.O_CREAT),"r+") as lock:
                sync.fcntl.flock(lock,sync.fcntl.LOCK_EX|sync.fcntl.LOCK_NB)
                record={"identity":sync.converse.LEGACY_QUEST_SYNC_IDENTITY,"instance":"a"*32}
                sync.converse.write_json(folder/"status.json",record)
                with contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(sync.main(["stop","--state-dir",str(folder)]),0)
                self.assertEqual(sync.converse.read_json(folder/"stop.json"),record)

    def test_applied_wolf_reward_lost_receipt_after_body_replacement_reconciles_actual_giver_menus(self):
        profile,rpc,clock,_=self.rig()
        rpc.active,rpc.level,rpc.eagan_done,rpc.wolves_done={7,18},2,True,True
        profile.data["confirmedRewards"]["5261"]={"serverRewardObserved":True,"logRemovalConfirmed":True}
        rpc.session="replacement-after-ambiguous-reward"
        for _ in range(12):
            profile.tick()
            if profile.status.get("exactGoalMatched"):break
            clock[0]+=10
        queried=[int(p["npcGuid"]) for m,p in rpc.calls if m == "buddy-quests.offer"]
        self.assertEqual(queried[:2],[EAGAN,WILLEM])
        self.assertEqual([p["questId"] for m,p in rpc.calls if m == "buddy-quests.accept"],[3903])
        self.assertTrue(profile.status["exactGoalMatched"])
        self.assertNotIn("33",profile.data["confirmedRewards"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
