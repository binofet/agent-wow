import copy
import importlib.machinery
import importlib.util
from pathlib import Path
import unittest
import threading
import json

loader = importlib.machinery.SourceFileLoader("tested_cave_followthrough", str(Path(__file__).with_name("cave-followthrough")))
spec = importlib.util.spec_from_loader(loader.name, loader)
h = importlib.util.module_from_spec(spec)
loader.exec_module(h)


def guid(entry):
    return (0xf130 << 48) | entry << 24 | 11


class FakeRPC:
    def __init__(self, *, complete62=False, confirm=True, lost=False, fail_route=False):
        self.calls, self.confirm, self.lost, self.fail_route = [], confirm, lost, fail_route
        self.on_acquire = self.on_travel = self.on_turnin = None
        self.state = dict(sessionId="cave-body", questLogKnown=True, questLogRevision="10", questLog=[
            dict(questId=47, complete=True), dict(questId=60, complete=True), dict(questId=62, complete=complete62)])
        self.own = dict(guid="4", present=True, mapKnown=True, positionKnown=True, aliveKnown=True, alive=True,
                        unitFlagsKnown=True, movementFlagsKnown=True, classKnown=True, classId=8,
                        levelKnown=True, level=7)
        self.actions = dict(following=True, classKnown=True, classId=8,
            position=dict(known=True, mapKnown=True, revision="1", **h.SCOUT))
        self.owner = dict(guid="3", present=False)
        self.npcs = [dict(guid=str(guid(entry)), entry=entry, present=True, mapKnown=True, positionKnown=True,
                          aliveKnown=True, alive=True, npcFlags=2, positionRevision="1", **h.RETURN[-1])
                     for entry in h.GIVERS.values()]

    def call(self, method, params, **kwargs):
        self.calls.append((method, copy.deepcopy(params)))
        if method == "buddy-quests.state":
            return copy.deepcopy(self.state)
        if method == "buddy-world.snapshot":
            return copy.deepcopy(dict(self=self.own, owner=self.owner,
                requestedCreatures=[n for n in self.npcs if n["entry"] == params.get("creatureEntry")]))
        if method == "buddy-actions.status":
            return copy.deepcopy(self.actions)
        if method == "buddy-actions.acquire_interaction":
            assert not self.actions.get("interactionOwner")
            self.actions.update(interactionOwner=params["owner"], interactionLeaseRemainingMs="30000")
            if self.on_acquire:
                self.on_acquire(self)
            return dict(token="lease_" + params["owner"], state=copy.deepcopy(self.actions))
        if method == "buddy-actions.release_interaction":
            assert params["token"] == "lease_" + params["owner"]
            self.actions.pop("interactionOwner", None)
            return copy.deepcopy(self.actions)
        if method in ("buddy-actions.travel_to", "buddy-actions.travel_quest"):
            if method.endswith("travel_to"):
                assert params["token"] == "lease_exploration" and params["arrivalDistance"] == .75
                target = params
            else:
                assert params["token"] == "lease_quests"
                target = next(n for n in self.npcs if n["guid"] == params["npcGuid"])
            if not self.fail_route:
                self.actions["position"].update({k: target[k] for k in ("x", "y", "z")})
            if self.on_travel:
                self.on_travel(self)
            return copy.deepcopy(self.actions)
        if method == "buddy-quests.explore":
            assert not self.actions.get("interactionOwner") and params["questId"] == 62
            baseline = self.state["questLogRevision"]
            if self.confirm:
                self.state["questLogRevision"] = str(int(baseline) + 1)
                next(q for q in self.state["questLog"] if q["questId"] == 62)["complete"] = True
            self.state["lastExploration"] = dict(requestId=params["requestId"], questId=62, triggerId=88,
                status="EXPLORATION_CONFIRMED" if self.confirm else "EXPLORATION_WRITE_UNCONFIRMED",
                baselineLogRevision=baseline, confirmedLogRevision=self.state["questLogRevision"], leaseReleased=True)
            if self.lost:
                raise h.Error("lost trigger response")
            return dict(state=copy.deepcopy(self.state))
        if method == "buddy-quests.turn_in":
            assert not self.actions.get("interactionOwner") and params["questId"] in h.GOAL
            assert params["npcGuid"] == str(guid(h.GIVERS[params["questId"]]))
            assert params["sessionId"] == self.state["sessionId"]
            assert params["questLogRevision"] == self.state["questLogRevision"]
            self.state["turnInOffer"] = dict(questId=params["questId"], npcGuid=params["npcGuid"],
                revision=str(int(self.state["questLogRevision"]) + 20), source="reward_offer", canComplete=True,
                expiresInMs="15000", choices=[])
            if self.on_turnin:
                self.on_turnin(self)
            return dict(state=copy.deepcopy(self.state))
        if method == "buddy-quests.claim_reward":
            assert params["noChoice"] is True and params["questId"] in h.GOAL
            baseline = self.state["questLogRevision"]
            if self.confirm:
                self.state["questLog"] = [q for q in self.state["questLog"] if q["questId"] != params["questId"]]
                self.state["questLogRevision"] = str(int(baseline) + 1)
            self.state["lastReward"] = dict(questId=params["questId"], npcGuid=params["npcGuid"],
                offerRevision=params["offerRevision"], status="REWARD_CONFIRMED" if self.confirm else "REWARD_REQUESTED",
                baselineLogRevision=baseline, confirmedLogRevision=self.state["questLogRevision"],
                serverRewardObserved=self.confirm, logRemovalConfirmed=self.confirm)
            # Actual follow-up menu must never cause acceptance in this worker.
            self.state["offer"] = dict(quests=[dict(questId=61, available=True), dict(questId=76, available=True)])
            if self.lost:
                raise h.Error("lost reward response")
            return dict(state=copy.deepcopy(self.state))
        raise AssertionError(method)


class Tests(unittest.TestCase):
    def actor(self, rpc, **kwargs):
        return h.Actor(h.ROSTER[1], rpc, **kwargs)

    def test_actual_scout_then_all_three_exact_receipts_never_accepts_followups(self):
        rpc = FakeRPC()
        actor = self.actor(rpc)
        actor.run()
        self.assertEqual(actor.status["phase"], "finished_fixed_cave_catchup_no_followups")
        self.assertEqual(actor.status["newlyConfirmed"], [47, 60, 62])
        self.assertEqual(set(actor.data["confirmed"]), {"47", "60", "62"})
        methods = [m for m, _ in rpc.calls]
        self.assertEqual(methods.count("buddy-quests.explore"), 1)
        self.assertEqual(methods.count("buddy-quests.claim_reward"), 3)
        self.assertFalse(any(m in ("buddy-quests.accept", "buddy-actions.follow") for m in methods))
        self.assertTrue(rpc.actions["following"])
        self.assertNotIn("interactionOwner", rpc.actions)

    def test_completed_scout_not_repeated_and_absence_is_not_new_reward_proof(self):
        rpc = FakeRPC(complete62=True)
        rpc.state["questLog"] = []
        actor = self.actor(rpc)
        actor.run()
        self.assertEqual(actor.status["newlyConfirmed"], [])
        self.assertEqual(actor.status["alreadyAbsentQuestIds"], [47, 60, 62])
        self.assertFalse(any(m in h.RPC.EXTRA or m == "buddy-quests.claim_reward" for m, _ in rpc.calls))

    def test_incomplete_dust_or_candles_does_not_leave_cave_or_claim(self):
        for qid in (47, 60):
            rpc = FakeRPC()
            next(q for q in rpc.state["questLog"] if q["questId"] == qid)["complete"] = False
            actor = self.actor(rpc)
            actor.run()
            self.assertEqual(actor.status["phase"], "waiting_for_actual_kobold_objective_completion")
            self.assertFalse(any(m in h.RPC.EXTRA for m, _ in rpc.calls))

    def test_trigger_reservation_is_durable_before_write_and_lost_response_actual_proof_counts(self):
        rpc = FakeRPC(lost=True)
        saved = []
        actor = self.actor(rpc, save=lambda value: saved.append(copy.deepcopy(value)))
        actor.session = actor.data["session"] = "cave-body"
        actor.scout()
        self.assertTrue(saved[0]["explore"])
        self.assertIn("explorationConfirmed", actor.data)

    def test_ambiguous_trigger_not_replayed_after_worker_restart(self):
        rpc = FakeRPC(confirm=False, lost=True)
        actor = self.actor(rpc)
        actor.session = actor.data["session"] = "cave-body"
        with self.assertRaisesRegex(h.Error, "reserved"):
            actor.scout()
        restarted = self.actor(rpc, stored=copy.deepcopy(actor.data))
        with self.assertRaisesRegex(h.Error, "Prior scout"):
            restarted.scout()
        self.assertEqual(sum(m == "buddy-quests.explore" for m, _ in rpc.calls), 1)

    def test_three_bad_routes_never_fall_back_to_raw_movement_or_trigger(self):
        rpc = FakeRPC(fail_route=True)
        rpc.actions["position"]["x"] += 20
        actor = self.actor(rpc)
        actor.session = actor.data["session"] = "cave-body"
        with self.assertRaisesRegex(h.Error, "Three bounded"):
            actor.scout()
        self.assertEqual(sum(m == "buddy-actions.travel_to" for m, _ in rpc.calls), 3)
        self.assertFalse(any(m == "buddy-quests.explore" for m, _ in rpc.calls))
        self.assertNotIn("interactionOwner", rpc.actions)

    def test_actor_body_changed_after_acquire_never_travels_and_never_releases_new_body_lease(self):
        rpc = FakeRPC()
        rpc.on_acquire = lambda f: f.state.update(sessionId="new-body")
        actor = self.actor(rpc)
        actor.session = actor.data["session"] = "cave-body"
        with self.assertRaisesRegex(h.Error, "body changed"):
            actor.travel_point(h.RETURN[0])
        self.assertFalse(any(m in ("buddy-actions.travel_to", "buddy-actions.release_interaction") for m, _ in rpc.calls))
        self.assertTrue(actor.release_uncertain)

    def test_missing_foreign_or_cross_map_npc_cannot_authorize_turnin(self):
        for change in (lambda f: f.npcs.clear(), lambda f: f.npcs[0].update(guid=str(guid(240))),
                       lambda f: f.npcs[0].update(mapId=1), lambda f: f.npcs[0].update(movementFlags=1)):
            rpc = FakeRPC(complete62=True)
            rpc.actions["position"].update(h.RETURN[-1])
            change(rpc)
            actor = self.actor(rpc)
            actor.session = actor.data["session"] = "cave-body"
            with self.assertRaises(h.Error):
                actor.turn_in(47)
            self.assertFalse(any(m == "buddy-quests.turn_in" for m, _ in rpc.calls))

    def test_confirmed_reward_after_lost_write_is_real_but_unconfirmed_never_retried(self):
        for confirm in (True, False):
            rpc = FakeRPC(complete62=True, confirm=confirm, lost=True)
            rpc.actions["position"].update(h.RETURN[-1])
            actor = self.actor(rpc)
            actor.session = actor.data["session"] = "cave-body"
            if confirm:
                actor.turn_in(47)
                self.assertIn("47", actor.data["confirmed"])
            else:
                with self.assertRaisesRegex(h.Error, "exact server receipt"):
                    actor.turn_in(47)
                restarted = self.actor(rpc, stored=copy.deepcopy(actor.data))
                with self.assertRaisesRegex(h.Error, "reserved"):
                    restarted.turn_in(47)
                self.assertEqual(sum(m == "buddy-quests.claim_reward" for m, _ in rpc.calls), 1)

    def test_body_or_offer_change_before_claim_never_claims(self):
        for change in (lambda f: f.state.update(sessionId="different"),
                       lambda f: f.state["turnInOffer"].update(expiresInMs="0")):
            rpc = FakeRPC(complete62=True)
            rpc.actions["position"].update(h.RETURN[-1])
            rpc.on_turnin = change
            actor = self.actor(rpc)
            actor.session = actor.data["session"] = "cave-body"
            with self.assertRaises(h.Error):
                actor.turn_in(47)
            self.assertFalse(any(m == "buddy-quests.claim_reward" for m, _ in rpc.calls))

    def test_actual_owner_combat_or_movement_pauses_but_absent_owner_does_not(self):
        for flag in (dict(unitFlags=0x80000), dict(movementFlags=1)):
            rpc = FakeRPC()
            rpc.owner.update(present=True, **flag)
            with self.assertRaisesRegex(h.Error, "Joel resumed"):
                self.actor(rpc).read()
            self.assertFalse(any(m in h.RPC.EXTRA for m, _ in rpc.calls))

    def test_stop_during_travel_still_releases_exact_owned_token(self):
        rpc, stop = FakeRPC(), [False]
        rpc.on_travel = lambda f: stop.__setitem__(0, True)
        actor = self.actor(rpc, stopped=lambda: stop[0])
        actor.session = actor.data["session"] = "cave-body"
        with self.assertRaises(h.c.Stopped):
            actor.travel_point(h.RETURN[0])
        self.assertEqual([p for m, p in rpc.calls if m == "buddy-actions.release_interaction"],
                         [dict(owner="exploration", token="lease_exploration")])

    def test_goal_whitelist_and_foreign_ledger_cannot_expand(self):
        actor = self.actor(FakeRPC())
        with self.assertRaisesRegex(h.Error, "outside"):
            actor.turn_in(76)
        stored = copy.deepcopy(actor.data)
        stored["identity"]["guid"] = 3
        with self.assertRaises(h.Error):
            self.actor(FakeRPC(), stored=stored)

    def test_fresh_objective_regression_after_initial_binding_never_scouts_or_travels(self):
        class ChangedLog(FakeRPC):
            reads = 0
            def call(self, method, params, **kwargs):
                if method == "buddy-quests.state":
                    self.reads += 1
                    if self.reads == 3:
                        self.state["questLog"][0]["complete"] = False
                return super().call(method, params, **kwargs)
        rpc = ChangedLog()
        actor = self.actor(rpc)
        actor.run()
        self.assertEqual(actor.status["phase"], "waiting_for_actual_kobold_objective_completion")
        self.assertFalse(any(m in h.RPC.EXTRA for m, _ in rpc.calls))

    def test_watchdog_releases_exact_token_when_joel_resumes_during_blocking_route(self):
        class BlockingRoute(FakeRPC):
            def __init__(self):
                super().__init__()
                self.exited = threading.Event()
            def call(self, method, params, **kwargs):
                if method == "buddy-actions.travel_to":
                    self.calls.append((method, copy.deepcopy(params)))
                    self.owner.update(present=True, movementFlags=1)
                    if not self.exited.wait(3):
                        raise AssertionError("Watchdog did not release owned movement")
                    return copy.deepcopy(self.actions)
                result = super().call(method, params, **kwargs)
                if method == "buddy-actions.release_interaction":
                    self.exited.set()
                return result
        rpc = BlockingRoute()
        actor = self.actor(rpc)
        actor.session = actor.data["session"] = "cave-body"
        with self.assertRaisesRegex(h.Error, "Joel resumed"):
            actor.travel_point(h.RETURN[0])
        self.assertTrue(rpc.exited.is_set())
        self.assertFalse(actor.release_uncertain)
        self.assertNotIn("interactionOwner", rpc.actions)
        self.assertEqual(sum(m == "buddy-actions.travel_to" for m, _ in rpc.calls), 1)

    def test_production_rpc_extra_transport_uses35second_route_and_matching_ids(self):
        calls = []
        class Response:
            def __init__(self, request):
                self.body = json.loads(request.data)
            def __enter__(self):
                return self
            def __exit__(self, *args):
                pass
            def read(self, bound):
                return json.dumps(dict(jsonrpc="2.0", id=self.body["id"], result={})).encode()
        class Opener:
            def open(self, request, timeout):
                calls.append((json.loads(request.data), timeout))
                return Response(request)
        rpc = h.RPC("http://127.0.0.1:8087/rpc", timeout=6)
        rpc.opener = Opener()
        rpc.call("buddy-actions.travel_to", {})
        rpc.call("buddy-quests.explore", {})
        self.assertEqual([row[1] for row in calls], [35, 6])
        self.assertEqual([row[0]["id"] for row in calls], [1, 2])
        with self.assertRaises(h.Error):
            rpc.call("raw.packet", {})


if __name__ == "__main__":
    unittest.main()
