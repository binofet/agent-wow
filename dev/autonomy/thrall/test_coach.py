#!/usr/bin/env python3
"""Safety and receipt regressions for the one-character Deathknell coach."""

import copy
import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("thrall_coach", Path(__file__).with_name("coach.py"))
coach = importlib.util.module_from_spec(spec)
spec.loader.exec_module(coach)
MORDO_GUID = "17379390988329437216"


class FakeRPC:
    def __init__(self):
        self.calls, self.active, self.offered = [], False, False
        self.auto, self.observe_accept, self.fail_accept = True, True, False
        self.guid, self.moving, self.npc_x = "6", False, 1671.89
        self.reject_receipt = False
        self.session, self.fail_offer = "own-test-session", False

    def log(self):
        entries = [{"slot": i, "objectiveCounts": [0, 0, 0, 0]} for i in range(25)]
        if self.active:
            entries[0].update(questId=363, state=1, complete=True)
        return entries

    def call(self, method, params):
        self.calls.append((method, copy.deepcopy(params)))
        p = {"mapKnown": True, "positionKnown": True, "x": 1676.71, "y": 1678.31, "z": 121.67}
        if method == "buddy-world.snapshot":
            return {"self": dict(p, guid=self.guid, present=True, aliveKnown=True, alive=True,
                                 healthKnown=True, health=52, unitFlagsKnown=True, unitFlags=8,
                                 classKnown=True, classId=8, levelKnown=True, level=1)}
        if method == "buddy-actions.status":
            return {"position": dict(p, known=True), "moving": self.moving}
        if method == "buddy-quests.discover":
            if self.offered and self.auto and self.observe_accept:
                self.active = True
            result = {"sessionId": self.session, "questLogKnown": True,
                      "questLogRevision": "3" if self.active else "1", "questLog": self.log(),
                      "nearby": [{"creature": dict(p, guid=MORDO_GUID, entry=1568, present=True,
                                                   aliveKnown=True, alive=True, npcFlags=2,
                                                   x=self.npc_x, z=120.802)}]}
            if self.reject_receipt:
                result["lastAcceptance"] = {"questId": 363, "status": "SERVER_REJECTED", "serverReason": 13}
            return result
        if method == "buddy-quests.offer":
            self.offered = True
            if self.fail_offer:
                raise coach.Error("response unavailable")
            return {"state": {"offer": {"npcGuid": MORDO_GUID, "revision": "3", "expiresInMs": "30000",
                    "quests": [{"questId": 363, "available": True, "flags": coach.AUTO_ACCEPT if self.auto else 8}]}}}
        if method == "buddy-quests.accept":
            if self.fail_accept:
                raise coach.Error("response unavailable")
            self.active = self.observe_accept
            return {}
        raise AssertionError(f"Unexpected RPC: {method}")

    def mutations(self):
        return [(m, p) for m, p in self.calls if m not in
                ("buddy-world.snapshot", "buddy-actions.status", "buddy-quests.discover")]


class CoachTests(unittest.TestCase):
    def run_coach(self, rpc, stored=None, step=True):
        return coach.Coach(rpc, stored=stored, pause=lambda seconds: None).run(step=step)

    def test_autoaccept_waits_for_own_log_without_sending_accept(self):
        rpc = FakeRPC()
        result = self.run_coach(rpc)
        self.assertEqual(result["phase"], "awaiting_verified_deathknell_route")
        self.assertEqual(rpc.mutations(), [("buddy-quests.offer", {"sessionId": "own-test-session", "npcGuid": MORDO_GUID})])
        self.assertTrue(result["starterQuestInOwnLog"])
        self.assertFalse(result["sarvisObserved"])

    def test_historical_rejection_does_not_retry_present_quest(self):
        rpc = FakeRPC()
        rpc.active, rpc.reject_receipt = True, True
        result = self.run_coach(rpc)
        self.assertEqual(rpc.mutations(), [])
        self.assertTrue(result["acceptanceReceiptConflict"])
        self.assertEqual(result["lastAcceptance"]["serverReason"], 13)

    def test_autoaccept_without_log_confirmation_stops_without_accept(self):
        rpc = FakeRPC()
        rpc.observe_accept = False
        result = self.run_coach(rpc)
        self.assertEqual(result["phase"], "autoaccept_log_confirmation_unavailable")
        self.assertEqual([m for m, _ in rpc.mutations()], ["buddy-quests.offer"])
        rpc.calls = []
        self.assertEqual(self.run_coach(rpc, stored=result)["phase"], "offer_requires_reconciliation")
        self.assertEqual(rpc.mutations(), [])

    def test_ambiguous_autoaccept_offer_does_not_retry(self):
        rpc = FakeRPC()
        rpc.fail_offer, rpc.observe_accept = True, False
        saved = []
        worker = coach.Coach(rpc, save=lambda value: saved.append(copy.deepcopy(value)), pause=lambda _: None)
        with self.assertRaises(coach.Error):
            worker.run(step=True)
        self.assertTrue(saved[-1]["offerAttempted"])
        rpc.calls = []
        self.assertEqual(self.run_coach(rpc, stored=saved[-1])["phase"], "offer_requires_reconciliation")
        self.assertEqual(rpc.mutations(), [])

    def test_new_session_requires_actual_inspection_before_step(self):
        rpc = FakeRPC()
        saved = {"characterGuid": "6", "character": "Gravemint", "sessionId": "old-body"}
        worker = coach.Coach(rpc, stored=saved, pause=lambda _: None)
        for _ in range(2):
            with self.assertRaises(coach.Error):
                worker.run(step=True)
            self.assertEqual(saved["sessionId"], "old-body")
        self.assertEqual(rpc.mutations(), [])
        worker.run(step=False)
        self.assertEqual(saved["sessionId"], rpc.session)
        self.assertNotIn("pendingSessionId", saved)

    def test_explicit_accept_is_durable_before_ambiguous_response(self):
        rpc = FakeRPC()
        rpc.auto, rpc.fail_accept = False, True
        saved = []
        worker = coach.Coach(rpc, save=lambda value: saved.append(copy.deepcopy(value)), pause=lambda _: None)
        with self.assertRaises(coach.Error):
            worker.run(step=True)
        self.assertTrue(saved[-1]["acceptAttempted"])
        rpc.calls = []
        result = self.run_coach(rpc, stored=saved[-1])
        self.assertEqual(result["phase"], "acceptance_requires_reconciliation")
        self.assertEqual(rpc.mutations(), [])

    def test_inspect_does_not_request_offer(self):
        rpc = FakeRPC()
        self.assertEqual(self.run_coach(rpc, step=False)["phase"], "starter_offer_not_requested")
        self.assertEqual(rpc.mutations(), [])

    def test_wrong_character_or_active_controller_cannot_mutate(self):
        for changed in ({"guid": "3"}, {"guid": "7"}, {"moving": True}):
            with self.subTest(changed=changed):
                rpc = FakeRPC()
                for key, value in changed.items():
                    setattr(rpc, key, value)
                with self.assertRaises(coach.Error):
                    self.run_coach(rpc)
                self.assertEqual(rpc.mutations(), [])

    def test_distant_mordo_does_not_invent_route(self):
        rpc = FakeRPC()
        rpc.npc_x = 1600
        self.assertEqual(self.run_coach(rpc)["phase"], "mordo_outside_existing_safe_approach")
        self.assertEqual(rpc.mutations(), [])

    def test_other_party_rpc_ports_are_rejected(self):
        for url in ("http://127.0.0.1:8086/rpc", "http://127.0.0.1:8192/rpc",
                    "http://quasar:8191/rpc", "http://127.0.0.1:8191/rpc?x=1"):
            with self.subTest(url=url), self.assertRaises(coach.Error):
                coach.RPC(url)

    def test_checkpoint_file_is_private(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "status.json"
            coach.private_save(path, {"characterGuid": "6"})
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)


if __name__ == "__main__":
    unittest.main()
