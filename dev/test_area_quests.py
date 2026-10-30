#!/usr/bin/env python3
"""Finite area quest sweep acceptance with fake normal server observations."""
import copy
import contextlib
import importlib.machinery
import importlib.util
import os
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

loader = importlib.machinery.SourceFileLoader("area_tested", str(Path(__file__).with_name("area-quests")))
spec = importlib.util.spec_from_loader(loader.name, loader)
area = importlib.util.module_from_spec(spec)
loader.exec_module(area)
A, B = 0xf1300000c500022b, 0xf130000337000210


class RPC:
    def __init__(self):
        self.calls, self.session, self.x = [], "body-quests", 0.
        self.map_id, self.action_map, self.map_known = 0, None, True
        self.active, self.complete, self.following = {7}, {7}, True
        self.offer, self.reward, self.receipt, self.revision = {}, {}, {}, 1
        self.offers = {A: [dict(questId=7, available=False), dict(questId=15, available=True)],
                       B: [dict(questId=18, available=True)]}
        self.creatures = {A: self.creature(A, 20), B: self.creature(B, 35)}
        self.combat, self.travel_step, self.busy = False, 500, False
        self.after_acquire, self.after_travel = None, None
        self.choices, self.items, self.claim_applies, self.prove_reward = [], {}, True, True
        self.accept_error, self.accept_applies = False, True
        self.journal = True

    @staticmethod
    def creature(guid, x):
        return dict(guid=str(guid), entry=guid >> 24 & 0xffffff, present=True, mapKnown=True,
                    mapId=0, positionKnown=True, aliveKnown=True, alive=True, npcFlags=2, x=x, y=0., z=0.)

    def quests(self):
        return dict(sessionId=self.session, questLogKnown=True, questLogRevision=str(self.revision),
            questLog=[dict(questId=q, complete=q in self.complete) for q in sorted(self.active)],
            nearby=[dict(creature=copy.deepcopy(c)) for c in self.creatures.values()],
            offer=copy.deepcopy(self.offer), turnInOffer=copy.deepcopy(self.reward), lastReward=copy.deepcopy(self.receipt))

    def call(self, method, params):
        self.calls.append((method, copy.deepcopy(params)))
        if method in ("buddy-quests.discover", "buddy-quests.state"):
            return self.quests()
        if method == "buddy-world.snapshot":
            return {"self": dict(guid="1", present=True, mapKnown=self.map_known, mapId=self.map_id, aliveKnown=True, alive=True,
                unitFlagsKnown=True, unitFlags=0x80000 if self.combat else 0, movementFlagsKnown=True,
                classKnown=True, classId=5, levelKnown=True, level=6)}
        if method == "buddy-actions.status":
            return dict(following=self.following, position=dict(known=True, mapKnown=self.map_known, mapId=self.map_id if self.action_map is None else self.action_map, x=self.x, y=0., z=0.))
        if method == "buddy-quests.journal":
            return {"entries": [dict(questId=7, observedRewardNpcGuid=str(A))] if self.journal else []}
        if method == "buddy-actions.follow":
            self.following = params["enabled"]
            return {"following": self.following}
        if method == "buddy-actions.acquire_interaction":
            if self.busy:
                raise area.Error("lease busy")
            if self.after_acquire:
                self.after_acquire(self)
            return {"token": "owned-area-token", "state": {"interactionOwner": "quests"}}
        if method == "buddy-actions.release_interaction":
            assert params == {"owner": "quests", "token": "owned-area-token"}
            return {}
        if method == "buddy-actions.travel_quest":
            target = self.creatures[int(params["npcGuid"])]["x"]
            self.x += max(-self.travel_step, min(self.travel_step, target-self.x))
            if self.after_travel:
                self.after_travel(self)
            return {"reason": "quest_travel_within_distance"}
        if method == "buddy-trade.lookup_items":
            return {"sessionId": "body-trade", "items": [self.items.get(q, {"itemId": q, "pending": True}) for q in params["itemIds"]]}
        npc = int(params["npcGuid"])
        assert abs(self.creatures[npc]["x"]-self.x) <= 8
        assert params["sessionId"] == self.session
        if method == "buddy-quests.offer":
            self.revision += 1
            self.offer = dict(revision=str(self.revision), npcGuid=str(npc), expiresInMs="60000",
                quests=[dict(q, available=q["available"] and q["questId"] not in self.active)
                        for q in self.offers[npc]])
            for q in self.offer["quests"]:
                if q.get("flags", 0) & 0x80000:
                    self.active.add(q["questId"])
        elif method == "buddy-quests.turn_in":
            assert params["questId"] in self.complete
            self.revision += 1
            self.reward = dict(revision=str(self.revision), questId=params["questId"], npcGuid=str(npc),
                source="reward_offer", canComplete=True, expiresInMs="60000", choices=copy.deepcopy(self.choices))
        elif method == "buddy-quests.claim_reward":
            assert params["offerRevision"] == self.reward["revision"] and params["questId"] == self.reward["questId"]
            if self.choices:
                assert any(q["index"] == params["choiceIndex"] and q["itemId"] == params["expectedItemId"] for q in self.choices)
            self.reward["rewardRequested"] = True
            if self.claim_applies:
                self.active.remove(params["questId"])
                self.complete.remove(params["questId"])
                self.revision += 1
                self.receipt = dict(questId=params["questId"], status="REWARD_CONFIRMED",
                    serverRewardObserved=self.prove_reward, logRemovalConfirmed=self.prove_reward)
                self.offers[npc] = [q for q in self.offers[npc] if q["questId"] != params["questId"]]
        elif method == "buddy-quests.accept":
            assert params["offerRevision"] == self.offer["revision"]
            assert params["questId"] not in self.active
            if self.accept_applies:
                self.active.add(params["questId"])
                self.revision += 1
            if self.accept_error:
                raise area.Error("ambiguous accept")
        else:
            raise AssertionError(method)
        return {"transportWritten": True, "serverResponseReceived": True, "state": self.quests()}


class AreaTests(unittest.TestCase):
    def rig(self):
        rpc, clock, saved = RPC(), [100.], []
        actor = area.AreaSweep("Arlen", 1, rpc, save=lambda value: saved.append(copy.deepcopy(value)), now=lambda: clock[0])
        return actor, rpc, clock, saved

    def run_sweep(self, actor, clock, limit=70):
        for _ in range(limit):
            actor.tick()
            clock[0] += 10
            if actor.done:
                return
        self.fail("Area sweep did not finish in its bounded fake scenario")

    def test_owner_free_sweep_turns_in_and_accepts_actual_local_quests_then_restores_follow(self):
        actor, rpc, clock, _ = self.rig()
        self.run_sweep(actor, clock)
        self.assertEqual(rpc.active, {15, 18})
        self.assertEqual(actor.status["acceptedQuestIds"], [15, 18])
        self.assertEqual(actor.status["rewardedQuestIds"], [7])
        self.assertFalse(any(method in ("buddy-party.state", "buddy-chat.owner_feed") for method, _ in rpc.calls))
        actor.cleanup()
        self.assertTrue(rpc.following)
        self.assertEqual(actor.status["cleanup"], "previous_follow_intent_restored")

    def test_nonzero_map_sweep_uses_actual_same_map_givers_and_rewards(self):
        for map_id in (1,530):
            actor,rpc,clock,_=self.rig()
            rpc.map_id=map_id
            for creature in rpc.creatures.values():creature["mapId"]=map_id
            self.run_sweep(actor,clock)
            self.assertEqual(rpc.active,{15,18})
            self.assertEqual(actor.rewarded,{7})
            self.assertEqual(actor.anchor["mapId"],map_id)
            actor.cleanup()
            self.assertTrue(rpc.following)

    def test_map_transition_after_lease_stops_sweep_releases_and_restores_follow_intent(self):
        for mode in ("changed","transfer_pending"):
            actor,rpc,clock,_=self.rig()
            actor.tick()
            self.assertFalse(rpc.following)
            def change(mock):
                if mode=="changed":
                    mock.map_id=530
                    for creature in mock.creatures.values():creature["mapId"]=530
                else:mock.map_known=False
            rpc.after_acquire=change
            clock[0]+=10
            actor.tick()
            self.assertTrue(actor.done)
            self.assertEqual(actor.status["phase"],"stopped_after_area_map_transition")
            self.assertEqual(rpc.calls[-1][0],"buddy-actions.release_interaction")
            self.assertFalse(any(method in ("buddy-actions.travel_quest","buddy-quests.offer","buddy-quests.turn_in","buddy-quests.claim_reward","buddy-quests.accept") for method,_ in rpc.calls))
            actor.cleanup()
            self.assertTrue(rpc.following)
            self.assertEqual(actor.status["cleanup"],"previous_follow_intent_restored")

    def test_unknown_or_disagreeing_initial_map_never_starts_area_work(self):
        for mode in ("unknown","actions_disagree"):
            actor,rpc,_,_=self.rig()
            if mode=="unknown":rpc.map_known=False
            else:rpc.action_map=1
            actor.tick()
            self.assertIsNone(actor.anchor)
            self.assertFalse(any(method in ("buddy-actions.follow","buddy-actions.travel_quest","buddy-quests.offer") for method,_ in rpc.calls))

    def test_actual_menu_finds_complete_turnin_even_without_prior_journal_npc(self):
        actor, rpc, clock, _ = self.rig()
        rpc.journal = False
        self.run_sweep(actor, clock)
        self.assertEqual(actor.rewarded, {7})

    def test_120yard_anchor_unknown_dead_moving_wrong_map_and_repeatable_are_excluded(self):
        actor, rpc, clock, _ = self.rig()
        rpc.following = False
        for n, fields in enumerate((dict(x=121), dict(present=False), dict(alive=False), dict(mapId=1),
                                     dict(movementFlags=1), dict(positionKnown=False))):
            guid = 0xf1300000c6000400+n
            rpc.creatures[guid] = rpc.creature(guid, 40) | fields
        rpc.offers[B].append(dict(questId=999, available=True, repeatable=True))
        self.run_sweep(actor, clock)
        traveled = {int(params["npcGuid"]) for method, params in rpc.calls if method == "buddy-actions.travel_quest"}
        self.assertTrue(traveled <= {A, B})
        self.assertNotIn(999, rpc.active)

    def test_combat_pauses_without_movement_or_quest_writes(self):
        actor, rpc, clock, _ = self.rig()
        rpc.combat = True
        actor.tick()
        self.assertEqual(actor.status["phase"], "paused_for_combat_or_character_state")
        self.assertFalse(any(method.endswith((".follow", ".travel_quest", ".accept", ".turn_in")) for method, _ in rpc.calls))
        rpc.combat = False
        self.run_sweep(actor, clock)

    def test_short_travel_retries_without_consuming_query_or_quest_offer(self):
        actor, rpc, clock, _ = self.rig()
        rpc.following, rpc.travel_step = False, 10
        rpc.journal = False
        actor.tick()
        self.assertEqual(actor.queries, set())
        self.assertNotIn("lastMutation", actor.data)
        self.assertFalse(any(method == "buddy-quests.offer" for method, _ in rpc.calls))
        clock[0] += 10
        self.run_sweep(actor, clock)
        self.assertEqual(rpc.active, {15, 18})

    def test_unreachable_complete_turnin_does_not_starve_other_reachable_givers(self):
        actor,rpc,clock,_=self.rig()
        rpc.following,rpc.travel_step=False,0
        rpc.creatures[B]["x"]=0.
        self.run_sweep(actor,clock)
        self.assertIn(18,rpc.active)
        self.assertIn(7,rpc.active)
        self.assertEqual(actor.travel_attempts[A],3)
        self.assertEqual(actor.status["phase"],"finished_with_unconfirmed_area_requests")

    def test_busy_lease_does_not_consume_query_and_release_is_bound_after_stop(self):
        actor, rpc, clock, _ = self.rig()
        rpc.following, rpc.journal, rpc.busy = False, False, True
        actor.tick()
        self.assertEqual(actor.queries, set())
        rpc.busy = False
        clock[0] += 10
        stop = [False]
        actor.stopped = lambda: stop[0]
        rpc.after_acquire = lambda _: stop.__setitem__(0, True)
        with self.assertRaises(area.converse.Stopped):
            actor.tick()
        self.assertEqual(rpc.calls[-1], ("buddy-actions.release_interaction", {"owner": "quests", "token": "owned-area-token"}))
        self.assertFalse(any(method == "buddy-quests.offer" for method, _ in rpc.calls))

    def test_body_change_or_disappearing_giver_after_lease_never_travels(self):
        for change in ("body", "giver", "combat"):
            actor, rpc, _, _ = self.rig()
            rpc.following, rpc.journal = False, False
            def after_acquire(mock):
                if change == "body": mock.session = "replacement"
                elif change == "giver": mock.creatures.clear()
                else: mock.combat = True
            rpc.after_acquire = after_acquire
            actor.tick()
            self.assertFalse(any(method in ("buddy-actions.travel_quest", "buddy-quests.offer") for method, _ in rpc.calls))
            self.assertEqual(rpc.calls[-1][0], "buddy-actions.release_interaction")

    def test_pending_metadata_never_reserves_claim_then_actual_choice_is_exact(self):
        actor, rpc, clock, saved = self.rig()
        rpc.choices = [dict(index=4, itemId=100, count=1), dict(index=2, itemId=200, count=1)]
        for _ in range(2): actor.tick(); clock[0] += 10
        actor.tick()
        self.assertEqual(actor.status["phase"], "waiting_for_reward_item_details")
        self.assertNotIn("claim:7", actor.data["reservations"])
        rpc.items = {item_id: dict(itemId=item_id, available=True, name="Cloak", itemClass=4, subclass=1,
            inventoryType=16, armor=armor, allowableClass=0xffffffff, allowableRace=0xffffffff)
            for item_id, armor in ((100, 1), (200, 5))}
        clock[0] += 1
        self.run_sweep(actor, clock)
        claim = next(params for method, params in rpc.calls if method == "buddy-quests.claim_reward")
        self.assertEqual((claim["choiceIndex"], claim["expectedItemId"]), (2, 200))

    def test_unproven_reward_and_ambiguous_accept_never_fake_progress_or_replay_same_offer(self):
        actor, rpc, clock, _ = self.rig()
        rpc.prove_reward, rpc.accept_error, rpc.accept_applies = False, True, False
        for _ in range(25):
            actor.tick(); clock[0] += 10
            if actor.done: break
        self.assertEqual(actor.rewarded, set())
        self.assertEqual(actor.accepted, set())
        accepts = [(p["npcGuid"], p["questId"], p["offerRevision"]) for m, p in rpc.calls if m == "buddy-quests.accept"]
        self.assertEqual(len(accepts), len(set(accepts)))

    def test_suppressed_restore_and_replaced_body_never_restart_old_follow_intent(self):
        for mode in ("human_stop", "body"):
            actor, rpc, clock, _ = self.rig()
            actor.tick()
            if mode == "human_stop": actor.suppress_follow_restore = True
            else: rpc.session = "replacement"
            actor.cleanup()
            self.assertFalse(rpc.following)

    def test_persistence_failure_prevents_first_control_write(self):
        actor, rpc, _, _ = self.rig()
        actor.save = lambda _: (_ for _ in ()).throw(OSError("full disk"))
        with self.assertRaises(OSError): actor.tick()
        self.assertFalse(any(method == "buddy-actions.follow" for method, _ in rpc.calls))

    def test_autoaccepted_quest_uses_own_log_without_duplicate_accept(self):
        actor, rpc, clock, _ = self.rig()
        rpc.offers[B] = [dict(questId=2158, available=True, flags=0x80008)]
        self.run_sweep(actor, clock)
        self.assertIn(2158, actor.accepted)
        self.assertFalse(any(method == "buddy-quests.accept" and params["questId"] == 2158 for method, params in rpc.calls))

    def test_cli_deadline_stops_and_records_private_status_without_game_actions(self):
        with tempfile.TemporaryDirectory() as temporary,patch.object(area.converse,"ROOT",Path(temporary)),\
                patch.object(area.time,"monotonic",side_effect=[0]+[100]*30),patch.object(area.signal,"signal"),\
                patch.object(area.sync,"SyncRPC") as rpc:
            self.assertEqual(area.main(["run","--character-guid","1","--max-seconds",".01"]),0)
            rpc.return_value.call.assert_not_called()
            path=Path(temporary)/"data/area-quests/1/status.json"
            record=json.loads(path.read_text())
            self.assertFalse(record["routineActive"])
            self.assertEqual(record["own"]["phase"],"stopped_or_area_sweep_time_limit_reached")
            self.assertEqual(path.stat().st_mode&0o777,0o600)

    def test_cli_duplicate_lock_refuses_second_controller_and_stop_binds_instance(self):
        with tempfile.TemporaryDirectory() as temporary,patch.object(area.converse,"ROOT",Path(temporary)):
            base=area.converse.ensure_state(Path(temporary)/"data/area-quests")
            folder=area.converse.ensure_state(base/"1")
            record={"identity":area.converse.area_quest_identity(1),"instance":"f"*32,"routineActive":True,"own":{}}
            area.converse.write_json(folder/"status.json",record)
            with os.fdopen(area.converse.private_open(folder/"lock",os.O_RDWR|os.O_CREAT),"r+") as lock:
                area.fcntl.flock(lock,area.fcntl.LOCK_EX)
                with self.assertRaises(area.Error):area.main(["run","--character-guid","1"])
                with contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(area.main(["stop","--character-guid","1"]),0)
                self.assertEqual(area.converse.read_json(folder/"stop.json"),{"identity":record["identity"],"instance":record["instance"]})


if __name__ == "__main__":
    unittest.main()
