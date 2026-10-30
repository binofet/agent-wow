#!/usr/bin/env python3
"""Quest execution tests with actual-observation-shaped fake normal RPCs."""
import copy
import importlib.machinery
import importlib.util
import threading
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

loader = importlib.machinery.SourceFileLoader("quest_task_tested", str(Path(__file__).with_name("quest-task")))
spec = importlib.util.spec_from_loader(loader.name, loader)
task = importlib.util.module_from_spec(spec)
loader.exec_module(task)
BARREL, FIRE = 0xf11002d77a000012, 0xf130005bf1000013
ITEM = 0x4000000000000123


class RPC:
    url = "http://127.0.0.1:8086/rpc"
    def __init__(self):
        self.calls, self.session, self.guid = [], "item-body", 1
        self.following, self.map_id, self.x = True, 0, 0.
        self.combat, self.alive, self.motion = False, True, False
        self.active, self.operation, self.count, self.inv, self.log = False, {}, 0, 1, 1
        self.quest_active, self.quest_known, self.inventory_known = True, True, True
        self.ready, self.reason = True, "ready_for_bound_quest_item_attempt"
        self.bucket, self.lease, self.lease_count = {}, None, 0
        self.auto, self.credit, self.attempt_error = False, True, False
        self.barrel_x, self.fire_x = 2., 10.
        self.before_acquire, self.after_read, self.block_travel = None, None, None
        self.last_attempt = None

    def target(self, guid, entry, x):
        return dict(guid=str(guid), entry=entry, present=True, mapKnown=True, mapId=self.map_id,
                    positionKnown=True, x=x, y=0., z=0., typeKnown=True, gameObjectType=10,
                    aliveKnown=True, alive=True)

    def state(self):
        return dict(sessionId=self.session, revision="1", selfGuid=str(self.guid), questId=11360,
                    questActive=self.quest_active, questLogKnown=self.quest_known, questLogRevision=str(self.log),
                    inventoryKnown=self.inventory_known, inventoryRevision=str(self.inv),
                    objectiveCount=self.count, requiredCount=5, questComplete=self.count >= 5,
                    active=self.active, operation=copy.deepcopy(self.operation), bucket=copy.deepcopy(self.bucket),
                    barrelTemplateKnown=True, bucketTemplateKnown=True, ready=self.ready, reason=self.reason,
                    barrels=[self.target(BARREL, 186234, self.barrel_x)],
                    fires=[self.target(FIRE, 23537, self.fire_x)])

    def complete(self, credit=True):
        self.active = False
        self.operation.update(status="COMPLETED", leaseReleased=True)
        self.inv += 1
        if self.last_attempt["action"] == "FILL_BUCKET":
            self.bucket = dict(bag=1, slot=2, itemGuid=str(ITEM), entry=32971, count=1)
            self.operation.update(ownItemPushObserved=True, inventoryAddedObserved=True)
        else:
            self.bucket = {}
            self.operation.update(ownCastGoObserved=True, inventoryConsumedObserved=True, questCreditObserved=credit)
            if credit:
                self.count += 1
                self.log += 1
            else:
                self.operation["status"] = "NO_CREDIT_OBSERVED"
        self.operation.update(observedInventoryRevision=str(self.inv),observedQuestLogRevision=str(self.log),afterObjectiveCount=self.count)

    def call(self, method, params):
        self.calls.append((method, copy.deepcopy(params)))
        if method == "buddy-quest-items.state":
            result = self.state()
            if self.after_read:
                hook, self.after_read = self.after_read, None
                hook(self)
            return result
        if method == "buddy-world.snapshot":
            return {"self": dict(guid=str(self.guid), present=True, mapKnown=True, mapId=self.map_id,
                aliveKnown=True, alive=self.alive, unitFlagsKnown=True, unitFlags=0x80000 if self.combat else 8)}
        if method == "buddy-actions.status":
            return dict(following=self.following, motionUnconfirmed=self.motion,
                        interactionOwner="exploration" if self.lease else "", moving=False,
                        position=dict(known=True, mapKnown=True, mapId=self.map_id, x=self.x, y=0., z=0.))
        if method == "buddy-actions.follow":
            self.following = params["enabled"]
            return {"following": self.following}
        if method == "buddy-quest-items.attempt":
            assert self.lease is None, "item RPC must acquire its own quests lease"
            assert params["sessionId"] == self.session and params["questId"] == 11360
            assert int(params["expectedInventoryRevision"]) == self.inv
            assert int(params["expectedQuestLogRevision"]) == self.log
            if params["action"] == "FILL_BUCKET":
                assert int(params["sourceObjectGuid"]) == BARREL and abs(self.x-self.barrel_x) <= 4.5 and not self.bucket
            else:
                assert int(params["targetCreatureGuid"]) == FIRE and int(params["expectedItemGuid"]) == ITEM
                assert abs(self.x-self.fire_x) <= 28 and self.bucket
            self.last_attempt = copy.deepcopy(params)
            self.operation = dict(operationId="operation-"+params["requestId"], requestId=params["requestId"],
                action=params["action"], status="WAITING_SERVER", questId=11360,
                baselineQuestLogRevision=str(self.log),baselineInventoryRevision=str(self.inv),beforeObjectiveCount=self.count,
                sourceObjectGuid=params["sourceObjectGuid"],targetCreatureGuid=params["targetCreatureGuid"])
            self.active = True
            if self.auto:
                self.complete(self.credit)
            if self.attempt_error:
                raise task.Error("ambiguous response")
            return self.state()
        if method == "buddy-quest-items.cancel":
            assert params == {"sessionId": self.session, "operationId": self.operation["operationId"]}
            self.active = False
            self.operation.update(status="CANCELLED", leaseReleased=True)
            return self.state()
        if method == "buddy-actions.acquire_interaction":
            assert params == {"owner": "exploration", "leaseMs": 30000} and self.lease is None
            self.lease_count += 1
            self.lease = "explore-owned-"+str(self.lease_count)
            result = {"token": self.lease, "state": {"interactionOwner": "exploration", "moving": False}}
            if self.before_acquire:
                self.before_acquire(self)
            return result
        if method == "buddy-actions.travel_to":
            assert self.lease == params["token"] and params["mapId"] == self.map_id
            if self.block_travel:
                self.block_travel.wait(2)
            else:
                self.x = params["x"]-2
            return {"reason": "exploration_within_distance", "moving": False}
        if method == "buddy-actions.release_interaction":
            assert params == {"owner": "exploration", "token": self.lease}
            self.lease = None
            if self.block_travel:
                self.block_travel.set()
            return {}
        raise AssertionError(method)


class QuestTaskTests(unittest.TestCase):
    def rig(self, stored=None, stopped=lambda: False):
        rpc, now, saved = RPC(), [100.], []
        actor = task.QuestTask(1, rpc.url, 11360, rpc.session, rpc,
            save=lambda value: saved.append(copy.deepcopy(value)), stored=stored, now=lambda: now[0], stopped=stopped)
        return actor, rpc, now, saved

    def tick(self, actor, now, n=1):
        with patch.object(task.c, "area_quest_snapshot", return_value={"active": False}):
            for _ in range(n):
                actor.tick()
                now[0] += 10

    def test_complete_five_individual_credits_uses_module_owned_item_steps_only(self):
        actor, rpc, now, saved = self.rig()
        rpc.auto = True
        for _ in range(30):
            self.tick(actor, now)
            if actor.done: break
        self.assertTrue(actor.done)
        self.assertEqual(actor.status["objectiveCount"], 5)
        self.assertEqual(actor.status["phase"], "own_quest_objective_goal_observed")
        requests = [p for m,p in rpc.calls if m == "buddy-quest-items.attempt"]
        self.assertEqual([p["action"] for p in requests], ["FILL_BUCKET", "THROW_BUCKET"]*5)
        self.assertEqual(len({p["requestId"] for p in requests}), 10)
        self.assertTrue(all(len(row["attempts"]) <= 32 for row in saved))
        self.assertFalse(any(m == "buddy-actions.acquire_interaction" for m,p in rpc.calls))
        actor.cleanup()
        self.assertTrue(rpc.following)

    def test_item_cast_and_transport_never_increment_observed_credit(self):
        actor, rpc, now, _ = self.rig()
        self.tick(actor, now, 2)
        self.assertTrue(rpc.active)
        self.assertEqual(actor.status["objectiveCount"], 0)
        rpc.complete()
        self.tick(actor, now, 2)
        self.assertEqual(rpc.last_attempt["action"], "THROW_BUCKET")
        rpc.operation["ownCastGoObserved"] = True
        self.tick(actor, now)
        self.assertEqual(actor.status["objectiveCount"], 0)
        rpc.complete(False)
        self.tick(actor, now)
        self.assertEqual(actor.status["objectiveCount"], 0)
        self.assertFalse(actor.done)
        self.tick(actor, now)
        self.assertEqual(rpc.last_attempt["action"], "FILL_BUCKET")

    def test_ambiguous_attempt_is_reserved_before_write_and_reconciled_without_resend(self):
        actor, rpc, now, saved = self.rig()
        rpc.attempt_error = True
        self.tick(actor, now, 2)
        self.assertEqual(len(saved[-1]["attempts"]), 1)
        stored = copy.deepcopy(actor.data)
        # Resume the same body after a process interruption. Its actual pending
        # operation is reconciled; no new request ID gets sent.
        resumed = task.QuestTask(1, rpc.url, 11360, rpc.session, rpc, stored=stored, now=lambda: now[0])
        self.tick(resumed, now, 4)
        self.assertEqual(sum(m == "buddy-quest-items.attempt" for m,p in rpc.calls), 1)
        rpc.active = False
        rpc.operation.update(status="OUTCOME_UNCONFIRMED", writeUnconfirmed=True, leaseReleased=True)
        self.tick(resumed, now, 3)
        self.assertEqual(sum(m == "buddy-quest-items.attempt" for m,p in rpc.calls), 1)
        self.assertEqual(resumed.status["phase"], "item_outcome_unconfirmed_no_automatic_resend")

    def test_rpc_error_before_module_receipt_cannot_be_automatically_replayed(self):
        actor, rpc, now, _ = self.rig()
        def failed(method, params):
            if method == "buddy-quest-items.attempt":
                rpc.calls.append((method, params)); raise task.Error("lost request")
            return original(method, params)
        original, rpc.call = rpc.call, failed
        self.tick(actor, now, 6)
        self.assertEqual(sum(m == "buddy-quest-items.attempt" for m,p in rpc.calls), 1)

    def test_identity_session_map_and_unsafe_state_never_route_item_actions(self):
        for mode in ("guid", "session", "map", "combat", "death", "unknown_inventory", "unknown_quest", "motion"):
            with self.subTest(mode=mode):
                actor,rpc,now,_ = self.rig()
                self.tick(actor,now)
                if mode == "guid": rpc.guid = 4
                elif mode == "session": rpc.session = "replacement"
                elif mode == "map": rpc.map_id = 1
                elif mode == "combat": rpc.combat = True
                elif mode == "death": rpc.alive = False
                elif mode == "unknown_inventory": rpc.inventory_known = False
                elif mode == "unknown_quest": rpc.quest_known = False
                elif mode == "motion": rpc.motion = True
                try: self.tick(actor,now)
                except task.Error: pass
                self.assertFalse(any(m in ("buddy-quest-items.attempt", "buddy-actions.travel_to") for m,p in rpc.calls))

    def test_exploration_lease_is_separate_released_before_item_attempt_and_revalidated(self):
        actor,rpc,now,_ = self.rig()
        rpc.barrel_x = 50
        self.tick(actor,now,2)
        self.assertIsNone(rpc.lease)
        self.assertTrue(any(m == "buddy-actions.travel_to" for m,p in rpc.calls))
        self.tick(actor,now)
        self.assertEqual(rpc.last_attempt["action"], "FILL_BUCKET")
        actor,rpc,now,_ = self.rig()
        rpc.barrel_x = 50
        rpc.before_acquire = lambda r: setattr(r,"quest_active",False)
        self.tick(actor,now,2)
        self.assertIsNone(rpc.lease)
        self.assertFalse(any(m == "buddy-actions.travel_to" for m,p in rpc.calls))

    def test_stop_during_blocking_travel_releases_exact_owned_token_promptly(self):
        stopped = threading.Event()
        actor,rpc,now,_ = self.rig(stopped=stopped.is_set)
        rpc.barrel_x, rpc.block_travel = 50, threading.Event()
        self.tick(actor,now)
        timer = threading.Timer(.05, stopped.set);timer.start()
        try:
            with self.assertRaises(task.c.Stopped): self.tick(actor,now)
        finally: timer.join()
        self.assertIsNone(rpc.lease)
        self.assertTrue(rpc.block_travel.is_set())
        self.assertFalse(any(m == "buddy-quest-items.attempt" for m,p in rpc.calls))

    def test_cleanup_cancels_only_owned_operation_and_preserves_explicit_follow_override(self):
        actor,rpc,now,_ = self.rig()
        self.tick(actor,now,2)
        actor.suppress_follow_restore = True
        actor.cleanup()
        self.assertTrue(any(m == "buddy-quest-items.cancel" for m,p in rpc.calls))
        self.assertFalse(rpc.following)
        actor,rpc,now,_ = self.rig()
        self.tick(actor,now,2)
        rpc.operation["requestId"] = "another-job"
        actor.cleanup()
        self.assertFalse(any(m == "buddy-quest-items.cancel" for m,p in rpc.calls))
        self.assertFalse(rpc.following)

    def test_saved_endpoint_quest_and_attempt_identity_are_strict(self):
        actor,rpc,_,_ = self.rig()
        for field,value in (("characterGuid",4),("rpcUrl","http://127.0.0.1:8087/rpc"),("questId",7)):
            stored=copy.deepcopy(actor.data);stored["identity"][field]=value
            with self.assertRaises(task.Error): self.rig(stored=stored)
        with self.assertRaises(task.Error): task.QuestTask(1,rpc.url,7,rpc.session,rpc)
        with self.assertRaises(task.Error): task.QuestTask(1,"https://example.com/rpc",11360,rpc.session,rpc)
        for value in ("later", float("nan"), True):
            stored=copy.deepcopy(actor.data);stored["nextAttemptAt"]=value
            with self.assertRaises(task.Error): self.rig(stored=stored)

    def test_same_body_resume_preserves_previously_held_follow_intent(self):
        actor,rpc,now,_ = self.rig()
        self.tick(actor,now)
        self.assertFalse(rpc.following)
        resumed = task.QuestTask(1,rpc.url,11360,rpc.session,rpc,stored=copy.deepcopy(actor.data),now=lambda:now[0])
        self.tick(resumed,now)
        self.assertTrue(resumed.data["originalFollowing"])
        rpc.active = False
        rpc.operation.update(status="CANCELLED",leaseReleased=True)
        resumed.cleanup()
        self.assertTrue(rpc.following)

    def test_global_capability_block_never_travels_but_pending_template_can(self):
        actor,rpc,now,_ = self.rig()
        rpc.barrel_x, rpc.ready, rpc.reason = 50, False, "own_active_quest_required"
        self.tick(actor,now,3)
        self.assertFalse(any(m in ("buddy-actions.travel_to","buddy-quest-items.attempt") for m,p in rpc.calls))
        self.assertEqual(actor.status["blockedReason"], "own_active_quest_required")
        rpc.reason = "waiting_for_normal_server_template"
        self.tick(actor,now,2)
        self.assertTrue(any(m == "buddy-actions.travel_to" for m,p in rpc.calls))
        self.assertFalse(any(m == "buddy-quest-items.attempt" for m,p in rpc.calls))

    def test_unconfirmed_travel_release_stays_visible_and_never_restores_follow(self):
        actor,rpc,now,_ = self.rig()
        rpc.barrel_x = 50
        original = rpc.call
        def uncertain(method,params):
            if method == "buddy-actions.release_interaction":
                raise task.Error("release response lost")
            return original(method,params)
        rpc.call = uncertain
        self.tick(actor,now,3)
        self.assertTrue(actor.data["travelCleanupUnconfirmed"])
        self.assertFalse(any(m == "buddy-quest-items.attempt" for m,p in rpc.calls))
        actor.cleanup()
        self.assertFalse(rpc.following)
        self.assertEqual(actor.status["cleanup"], "exploration_release_unconfirmed_follow_not_restored")
        rpc.lease = None  # A subsequent actual status confirms lease expiry and STOP.
        self.tick(actor,now)
        self.assertFalse(actor.data["travelCleanupUnconfirmed"])
        self.assertEqual(rpc.last_attempt["action"],"FILL_BUCKET")

    def test_authoritative_completed_proof_survives_ambiguous_transport_error(self):
        actor,rpc,now,_ = self.rig()
        self.tick(actor,now,2)
        rpc.complete()
        rpc.operation["writeUnconfirmed"] = True
        self.tick(actor,now,2)
        self.assertEqual(rpc.last_attempt["action"],"THROW_BUCKET")
        rpc.count = 4
        rpc.operation["beforeObjectiveCount"] = 4
        rpc.complete()
        rpc.operation["writeUnconfirmed"] = True
        self.tick(actor,now)
        self.assertTrue(actor.done)
        self.assertEqual(actor.status["objectiveCount"],5)
        actor.cleanup()
        self.assertTrue(rpc.following)
        actor,rpc,now,_ = self.rig()
        self.tick(actor,now,2)
        rpc.complete()
        rpc.operation.update(writeUnconfirmed=True,inventoryAddedObserved=False)
        self.tick(actor,now)
        self.assertFalse(actor.done)
        self.assertEqual(actor.status["phase"],"item_outcome_unconfirmed_no_automatic_resend")

    def test_exhausted_nearest_target_does_not_starve_second_reachable_target(self):
        actor,rpc,now,_ = self.rig()
        rpc.barrel_x = 50
        second = BARREL+1
        original = rpc.state
        def state():
            result=original()
            result["barrels"].append(rpc.target(second,186234,75))
            return result
        rpc.state = state
        actor.travel_attempts[str(BARREL)] = 3
        self.tick(actor,now,2)
        route = next(p for m,p in rpc.calls if m == "buddy-actions.travel_to")
        self.assertEqual(route["x"],75)
        rpc.x = 49
        candidates=actor.candidates(actor.read(),"barrels")
        self.assertEqual(candidates[0]["guid"],BARREL)  # Actual close range can use it again.

    def test_five_successful_refill_return_trips_reset_only_completed_target_budget(self):
        actor, rpc, now, saved = self.rig()
        rpc.auto, rpc.barrel_x, rpc.fire_x = True, 50., 150.
        with patch.object(task.c, "area_quest_snapshot", return_value={"active": False}):
            for _ in range(90):
                actor.tick()
                now[0] += 3
                if actor.done:
                    break
        self.assertTrue(actor.done)
        self.assertEqual(actor.status["phase"], "own_quest_objective_goal_observed")
        self.assertEqual(actor.status["objectiveCount"], 5)
        routes = [p for m, p in rpc.calls if m == "buddy-actions.travel_to"]
        self.assertEqual(sum(p["x"] == 50. for p in routes), 5)
        self.assertEqual(sum(p["x"] == 150. for p in routes), 5)
        requests = [p for m, p in rpc.calls if m == "buddy-quest-items.attempt"]
        self.assertEqual([p["action"] for p in requests], ["FILL_BUCKET", "THROW_BUCKET"] * 5)
        self.assertEqual(len({p["requestId"] for p in requests}), 10)
        self.assertLess(now[0] - 100, 300)
        self.assertTrue(all(len(row["attempts"]) <= 32 for row in saved))
        self.assertNotIn(str(BARREL), actor.travel_attempts)
        self.assertNotIn(str(FIRE), actor.travel_attempts)
        self.assertIsNone(rpc.lease)

    def test_three_failed_routes_remain_exhausted_without_item_progress(self):
        actor, rpc, now, _ = self.rig()
        rpc.barrel_x = 50.
        original = rpc.call
        def failed_route(method, params):
            if method == "buddy-actions.travel_to":
                rpc.calls.append((method, copy.deepcopy(params)))
                return {"reason": "navigation_partial_route", "moving": False}
            return original(method, params)
        rpc.call = failed_route
        self.tick(actor, now, 10)
        self.assertEqual(sum(m == "buddy-actions.travel_to" for m, _ in rpc.calls), 3)
        self.assertEqual(actor.travel_attempts[str(BARREL)], 3)
        self.assertEqual(actor.status["blockedReason"], "observed_candidate_exhausted_travel_limit")
        self.assertFalse(any(m == "buddy-quest-items.attempt" for m, _ in rpc.calls))
        self.assertIsNone(rpc.lease)

    def test_travel_budget_reset_requires_matched_authoritative_completion(self):
        for mode in ("fill", "fill_no_inventory", "fill_wrong_source", "throw", "throw_no_go", "throw_no_credit"):
            with self.subTest(mode=mode):
                actor, rpc, now, _ = self.rig()
                self.tick(actor, now, 2)
                if mode.startswith("throw"):
                    rpc.complete()
                    self.tick(actor, now, 2)
                    self.assertEqual(rpc.last_attempt["action"], "THROW_BUCKET")
                target = FIRE if mode.startswith("throw") else BARREL
                other = BARREL + 2
                actor.travel_attempts.update({str(target): 3, str(other): 2})
                rpc.complete(mode != "throw_no_credit")
                if mode == "fill_no_inventory":
                    rpc.operation["inventoryAddedObserved"] = False
                elif mode == "fill_wrong_source":
                    rpc.operation["sourceObjectGuid"] = str(BARREL + 1)
                elif mode == "throw_no_go":
                    rpc.operation["ownCastGoObserved"] = False
                if mode == "fill_wrong_source":
                    with self.assertRaisesRegex(task.Error, "binding changed"):
                        self.tick(actor, now)
                else:
                    self.tick(actor, now)
                self.assertEqual(actor.travel_attempts.get(str(target)), None if mode in ("fill", "throw") else 3)
                self.assertEqual(actor.travel_attempts[str(other)], 2)

    def test_manual_worker_requests_exact_area_stop_once_then_waits_cleanup(self):
        actor,rpc,now,_ = self.rig()
        rpc.ready,rpc.reason,rpc.motion=False,"own_action_busy_or_unconfirmed",True
        with patch.object(task.c,"area_quest_snapshot",return_value={"active":True}) as snapshot:
            actor.tick();actor.tick()
            self.assertEqual(sum(call.kwargs.get("stop") is True for call in snapshot.call_args_list),1)
            snapshot.assert_any_call(1,stop=True)
        self.assertFalse(any(m in ("buddy-quest-items.attempt","buddy-actions.follow") for m,p in rpc.calls))

    def test_runtime_limit_and_stop_identity_do_not_run_any_game_action(self):
        with self.assertRaises(SystemExit): task.main(["run","--character-guid","1","--rpc-url",RPC.url,"--quest-id","11360","--session-id","item-body","--max-seconds","301"])
        with tempfile.TemporaryDirectory() as temporary, patch.object(task.c,"ROOT",Path(temporary)):
            self.assertEqual(task.main(["stop","--character-guid","1","--rpc-url",RPC.url,"--quest-id","11360"]),0)


if __name__ == "__main__": unittest.main()
