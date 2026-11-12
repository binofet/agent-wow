"""Acceptance scenarios use simulated public RPC only; never a live client."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("uther_progress", Path(__file__).with_name("progress.py"))
progress = importlib.util.module_from_spec(spec)
spec.loader.exec_module(progress)

NPCS = {823: str(0xf130000337000210), 197: str(0xf1300000c500022b), 196: str(0xf1300000c400022c)}
POINTS = {823: (-8947., -132., 83.), 197: (-8902., -162., 82.), 196: (-8869., -163., 81.)}
READS = {"buddy-world.snapshot", "buddy-quests.discover", "buddy-actions.status", "buddy-loot.state"}
TEMP_ROOT = Path(tempfile.gettempdir()).resolve()


class Realm:
    """Models auto-added breadcrumbs separately from explicit acceptance."""
    def __init__(self):
        self.calls, self.active, self.rewards = [], {}, set()
        self.session, self.log_revision, self.revision = "own-body", 1, 1
        self.offer, self.turn_in, self.last_reward = {}, {}, {}
        self.guid, self.class_id, self.level = 7, 8, 1
        self.alive, self.log_known, self.duplicate_slot = True, True, False
        self.current = dict(zip(("x", "y", "z"), POINTS[823]))
        self.world_position = dict(self.current)  # Intentionally never echoed by movement.
        self.actions = {"knownFireball": True, "reason": "disabled", "practicePhase": ""}
        self.loot = {"phase": "disabled"}
        self.lease = False
        self.fail_before, self.fail_after = None, None
        self.discoveries, self.on_discover = 0, None
        self.no_breadcrumb, self.no_starter = False, False
        self.nav_failure = None
        self.partial_travel = False
        self.position_age = 0
        self.choices = {33: [{"index": 0, "itemId": 6070, "count": 1}, {"index": 1, "itemId": 80, "count": 1}]}

    def creature(self, entry):
        return dict(zip(("x", "y", "z"), POINTS[entry]), guid=NPCS[entry], entry=entry,
                    present=True, aliveKnown=True, alive=True, mapKnown=True, positionKnown=True,
                    npcFlags=2)

    def log(self):
        rows = [{"slot": i} for i in range(25)]
        for index, (qid, complete) in enumerate(sorted(self.active.items())):
            rows[index].update(questId=qid, complete=complete, state=1 if complete else 0)
        if self.duplicate_slot:
            rows[-1]["slot"] = 0
        return rows

    def world(self):
        return {"self": dict(self.world_position, guid=str(self.guid), classKnown=True, classId=self.class_id,
            levelKnown=True, level=self.level, present=True, aliveKnown=True, alive=self.alive,
            healthKnown=True, health=50, unitFlagsKnown=True, movementFlagsKnown=True,
            mapKnown=True, positionKnown=True)}  # No owner or name API exists.

    def quests(self):
        return {"sessionId": self.session, "questLogKnown": self.log_known,
            "questLogRevision": str(self.log_revision), "questLog": self.log(),
            "nearby": [{"creature": self.creature(entry)} for entry in (823, 197, 196)],
            "offer": copy.deepcopy(self.offer), "turnInOffer": copy.deepcopy(self.turn_in),
            "lastReward": copy.deepcopy(self.last_reward)}

    def action_state(self):
        self.position_age += 1
        return dict(self.actions, position=dict(self.current, known=True, mapKnown=True,
                    ageMs=str(self.position_age), revision=str(self.position_age), estimated=self.lease),
                    **({"interactionOwner": "quests"} if self.lease else {}))

    def quest_response(self):
        return {"transportWritten": True, "serverResponseReceived": True, "state": self.quests()}

    def set_offer(self, entry, quests, source="quest_list"):
        self.revision += 1
        self.offer = {"npcGuid": NPCS[entry], "revision": str(self.revision), "expiresInMs": "60000",
                      "source": source, "quests": [{"questId": q, "available": True} for q in quests]}

    def add(self, qid, complete=False):
        self.active[qid] = complete
        self.log_revision += 1

    def call(self, method, params):
        self.calls.append((method, copy.deepcopy(params)))
        if method == self.fail_before:
            raise progress.Error("response lost before observable effect")
        if method == "buddy-world.snapshot":
            return self.world()
        if method == "buddy-quests.discover":
            self.discoveries += 1
            if self.on_discover:
                self.on_discover(self)
            return self.quests()
        if method == "buddy-actions.status":
            return self.action_state()
        if method == "buddy-loot.state":
            return copy.deepcopy(self.loot)
        if method == "buddy-actions.acquire_interaction":
            self.lease = True
            return {"token": "private-lease-token", "state": self.action_state()}
        if method == "buddy-actions.release_interaction":
            self.lease = False
            return self.action_state()
        if method == "buddy-actions.travel_quest":
            if self.nav_failure:
                return dict(self.action_state(), reason=self.nav_failure, navAvailable=False, navReason="client_navmesh_partial_route")
            destination = dict(zip(("x", "y", "z"), POINTS[progress.giver_entry(params["npcGuid"])]))
            if self.partial_travel:
                self.partial_travel = False
                self.current = {axis: (self.current[axis] + destination[axis]) / 2 for axis in ("x", "y", "z")}
                result = dict(self.action_state(), reason="quest_travel_lease_changed_or_expiring", navAvailable=True)
            else:
                self.current = destination
                result = dict(self.action_state(), reason="quest_travel_within_distance")
        elif method == "buddy-quests.offer":
            entry = progress.giver_entry(params["npcGuid"])
            available = []
            if entry == 823:
                if 783 not in self.rewards and not self.no_starter:
                    available = [783]
                elif 783 in self.rewards and 5261 not in self.rewards and not self.no_breadcrumb:
                    available = [5261]
            if entry == 196 and self.no_breadcrumb and self.level >= 2:
                available = [33]
            self.set_offer(entry, available)
            result = self.quest_response()
        elif method == "buddy-quests.details":
            self.assert_bound(params)
            qid = params["questId"]
            if qid in (783, 5261):
                self.add(qid, True)
            self.set_offer(progress.giver_entry(params["npcGuid"]), [qid], "quest_details")
            result = self.quest_response()
        elif method == "buddy-quests.accept":
            self.assert_bound(params)
            if params["questId"] in self.active:
                raise AssertionError("Blind Accept after auto-added own quest")
            self.add(params["questId"])
            result = self.quest_response()
        elif method == "buddy-quests.turn_in":
            assert str(self.log_revision) == params["questLogRevision"]
            assert params["questId"] in self.active
            self.revision += 1
            self.turn_in = {"revision": str(self.revision), "npcGuid": params["npcGuid"],
                "questId": params["questId"], "expiresInMs": "60000", "source": "reward_offer",
                "canComplete": True, "choices": copy.deepcopy(self.choices.get(params["questId"], []))}
            result = self.quest_response()
        elif method == "buddy-quests.claim_reward":
            qid = params["questId"]
            assert params["offerRevision"] == self.turn_in["revision"]
            assert qid == self.turn_in["questId"] and params["npcGuid"] == self.turn_in["npcGuid"]
            del self.active[qid]
            self.log_revision += 1
            self.rewards.add(qid)
            self.last_reward = {"questId": qid, "npcGuid": params["npcGuid"], "offerRevision": params["offerRevision"],
                "status": "REWARD_CONFIRMED", "serverRewardObserved": True, "logRemovalConfirmed": True,
                "rewardXp": {783: 40, 5261: 85, 33: 170}[qid]}
            if qid == 783:
                self.add(7)
            elif qid == 5261:
                self.add(33)
            self.turn_in = {}
            result = self.quest_response()
        elif method == "buddy-actions.practice":
            self.actions["practicing"] = params["enabled"]
            self.actions["practicePhase"] = "waiting_for_safe_pull" if params["enabled"] else "disabled"
            self.actions["reason"] = "practice_enabled" if params["enabled"] else "practice_disabled"
            result = self.action_state()
        elif method == "buddy-loot.enable":
            self.loot.update(enabled=params["enabled"], phase="idle" if params["enabled"] else "disabled")
            result = copy.deepcopy(self.loot)
        else:
            raise AssertionError("Forbidden/unimplemented public call: " + method)
        if method == self.fail_after:
            raise progress.Error("response lost after possible effect")
        return result

    def assert_bound(self, params):
        assert params["sessionId"] == self.session and params["offerRevision"] == self.offer["revision"]
        assert params["npcGuid"] == self.offer["npcGuid"]
        assert params["questId"] in {q["questId"] for q in self.offer["quests"]}


class CoachTests(unittest.TestCase):
    def rig(self, realm=None, stored=None):
        realm, saved = realm or Realm(), []
        coach = progress.Coach(realm, stored=stored, save=lambda data: saved.append(copy.deepcopy(data)), now=lambda: 1000.)
        return coach, realm, saved

    def mutations(self, realm):
        return [(method, params) for method, params in realm.calls if method not in READS]

    def advance(self, coach, limit=40, until=lambda status: False):
        for _ in range(limit):
            status = coach.step()
            if until(status):
                return status
            self.assertFalse(status["phase"].startswith("blocked_"), status)
        self.fail("Coach did not reach expected bounded phase: " + str(status))

    def test_actual_breadcrumb_autolog_reward_followups_and_wolf_receipts(self):
        coach, realm, saved = self.rig()
        self.advance(coach, until=lambda status: status["phase"] == "waiting_for_wolf_practice")
        self.assertEqual(realm.active, {7: False, 33: False})
        self.assertEqual(realm.rewards, {783, 5261})
        self.assertFalse(any(method == "buddy-quests.accept" for method, _ in realm.calls))
        self.assertFalse(any(row.get("stage") == "xp" for row in coach.data["reservations"]))
        self.assertEqual(realm.world_position, dict(zip(("x", "y", "z"), POINTS[823])))
        self.assertEqual(realm.current, dict(zip(("x", "y", "z"), POINTS[196])))
        realm.active[33] = True
        realm.log_revision += 1
        realm.actions.update(practicing=False, reason="practice_quest_33_completion_observed",
                             practicePhase="quest_33_completion_observed", fireballDamageTotal="41", practiceKills="1")
        realm.loot.update(itemsStoredConfirmed="2", recentItems=[{"itemId": 7073, "count": 1}])
        self.advance(coach, until=lambda status: status["phase"] == "complete")
        self.assertEqual(realm.rewards, {783, 5261, 33})
        self.assertFalse(realm.loot["enabled"])
        claim = [p for m, p in realm.calls if m == "buddy-quests.claim_reward" and p["questId"] == 33]
        self.assertEqual(claim[0]["expectedItemId"], 80)
        self.assertEqual(coach.data["evidence"]["rewards"]["783"]["receipt"]["rewardXp"], 40)
        self.assertEqual(coach.data["evidence"]["rewards"]["5261"]["receipt"]["rewardXp"], 85)
        self.assertEqual(coach.data["evidence"]["combat"]["fireballDamageTotal"], "41")
        self.assertTrue(saved)
        before = len(self.mutations(realm))
        resumed, _, _ = self.rig(realm, saved[-1])
        self.assertEqual(resumed.step()["phase"], "complete")
        self.assertEqual(len(self.mutations(realm)), before)

    def test_reservation_exists_before_mutation_and_lost_accept_reconciles_without_retry(self):
        realm = Realm()
        realm.add(7)
        realm.current = dict(zip(("x", "y", "z"), POINTS[196]))
        realm.set_offer(196, [33], "quest_details")
        realm.fail_after = "buddy-quests.accept"
        coach, _, saved = self.rig(realm)
        status = coach.step()
        self.assertEqual(status["phase"], "blocked_unconfirmed_request")
        self.assertEqual(saved[0]["pending"], 1)
        self.assertEqual(saved[0]["reservations"][0]["outcome"], "reserved_before_request")
        resumed, _, _ = self.rig(realm, saved[-1])
        resumed.step()
        self.assertEqual(sum(m == "buddy-quests.accept" for m, _ in realm.calls), 1)
        self.assertIn(33, realm.active)
        self.assertEqual(resumed.data["reservations"][0]["outcome"], "reconciled_from_own_state")

    def test_unknown_accept_does_not_retry_on_new_offer_or_replacement_body(self):
        realm = Realm()
        realm.add(7)
        realm.current = dict(zip(("x", "y", "z"), POINTS[196]))
        realm.set_offer(196, [33], "quest_details")
        realm.fail_before = "buddy-quests.accept"
        coach, _, saved = self.rig(realm)
        coach.step()
        realm.fail_before = None
        realm.session = "replacement-body"
        realm.set_offer(196, [33], "quest_details")
        resumed, _, _ = self.rig(realm, saved[-1])
        for _ in range(3):
            self.assertEqual(resumed.step()["phase"], "blocked_unconfirmed_request")
        self.assertEqual(sum(m == "buddy-quests.accept" for m, _ in realm.calls), 1)
        self.assertNotIn(33, realm.active)

    def test_stopped_live_practice_failure_blocks_without_any_toggle_or_retry(self):
        coach, realm, _ = self.rig()
        realm.add(7)
        realm.add(33)
        realm.actions.update(practicing=False, reason="practice_target_unknown_claimed_or_cast_failed",
                             practicePhase="target_unknown_claimed_or_cast_failed")
        for _ in range(3):
            self.assertEqual(coach.step()["phase"], "blocked_practice_target_unknown_claimed_or_cast_failed")
        self.assertEqual(self.mutations(realm), [])

    def test_wrong_character_unknown_class_dead_and_incomplete_log_never_mutate(self):
        for mode in ("guid", "class", "dead", "unknown_log", "duplicate_slot"):
            with self.subTest(mode=mode):
                coach, realm, _ = self.rig()
                if mode == "guid":
                    realm.guid = 8
                elif mode == "class":
                    realm.class_id = 4
                elif mode == "dead":
                    realm.alive = False
                elif mode == "unknown_log":
                    realm.log_known = False
                else:
                    realm.duplicate_slot = True
                self.assertTrue(coach.step()["phase"].startswith("blocked_"))
                self.assertEqual(self.mutations(realm), [])

    def test_changed_offer_during_fresh_guard_never_sends_stale_accept(self):
        coach, realm, _ = self.rig()
        realm.add(7)
        realm.current = dict(zip(("x", "y", "z"), POINTS[196]))
        realm.set_offer(196, [33], "quest_details")
        realm.on_discover = lambda peer: peer.set_offer(196, [], "quest_details") if peer.discoveries == 4 else None
        self.assertEqual(coach.step()["phase"], "waiting_for_fresh_plan_after_state_change")
        self.assertEqual(self.mutations(realm), [])

    def test_expired_successful_positive_query_can_refresh_without_repeating_acceptance(self):
        coach, realm, saved = self.rig()
        coach.step()  # Actual Willem783 offer received.
        realm.offer["expiresInMs"] = "0"
        resumed, _, _ = self.rig(realm, saved[-1])
        self.assertEqual(resumed.step()["phase"], "requested_action_reconciling")
        self.assertEqual(sum(m == "buddy-quests.offer" for m, _ in realm.calls), 2)
        self.assertEqual(sum(m == "buddy-quests.accept" for m, _ in realm.calls), 0)
        resumed.step()
        self.assertIn(783, realm.active)  # Fresh bound Details auto-adds it.

    def test_expired_empty_offer_still_queries_breadcrumb_before_xp(self):
        coach, realm, saved = self.rig()
        realm.rewards.add(783)
        realm.add(7)
        realm.current = dict(zip(("x", "y", "z"), POINTS[196]))
        realm.no_breadcrumb = True
        coach.step()  # Actual empty Eagan offer.
        realm.offer["expiresInMs"] = "0"
        resumed, _, _ = self.rig(realm, saved[-1])
        self.assertEqual(resumed.step()["phase"], "requested_action_reconciling")
        self.assertFalse(any(m == "buddy-actions.practice" for m, _ in realm.calls))
        resumed.step()
        offered = [progress.giver_entry(p["npcGuid"]) for m, p in realm.calls if m == "buddy-quests.offer"]
        self.assertEqual(offered, [196, 823])

    def test_navigation_partial_route_is_precise_block_and_releases_lease(self):
        coach, realm, saved = self.rig()
        realm.add(783, True)
        realm.nav_failure = "quest_travel_navigation_unavailable"
        coach.step()
        self.assertFalse(realm.lease)
        resumed, _, _ = self.rig(realm, saved[-1])
        status = resumed.step()
        self.assertEqual(status["phase"], "blocked_navigation")
        self.assertEqual(status["detail"]["reason"], "quest_travel_navigation_unavailable")
        self.assertEqual(sum(m == "buddy-actions.travel_quest" for m, _ in realm.calls), 1)
        self.assertNotIn("private-lease-token", json.dumps(saved))

    def test_verified_stopped_lease_expiry_can_resume_after_actual_progress(self):
        coach, realm, saved = self.rig()
        realm.add(783, True)
        realm.partial_travel = True
        coach.step()
        self.assertFalse(realm.lease)
        resumed, _, _ = self.rig(realm, saved[-1])
        status = resumed.step()
        self.assertEqual(status["phase"], "requested_action_reconciling")
        self.assertTrue(resumed.data["reservations"][0]["safePartialTravelObserved"])
        self.assertEqual(sum(m == "buddy-actions.travel_quest" for m, _ in realm.calls), 2)
        self.assertFalse(realm.lease)

    def test_travel_intent_ignores_changing_position_age_revision_and_estimate_metadata(self):
        coach, realm, _ = self.rig()
        realm.add(783, True)
        status = coach.step()
        self.assertEqual(status["phase"], "requested_action_reconciling")
        self.assertEqual(sum(m == "buddy-actions.travel_quest" for m, _ in realm.calls), 1)
        self.assertGreater(realm.position_age, 4)

    def test_unknown_partial_travel_cannot_use_progress_as_permission_to_retry(self):
        coach, realm, saved = self.rig()
        realm.add(783, True)
        realm.partial_travel, realm.fail_after = True, "buddy-actions.travel_quest"
        coach.step()
        self.assertFalse(realm.lease)
        resumed, _, _ = self.rig(realm, saved[-1])
        self.assertEqual(resumed.step()["phase"], "blocked_navigation")
        self.assertEqual(sum(m == "buddy-actions.travel_quest" for m, _ in realm.calls), 1)

    def test_full_route_is_used_for_nearby_giver_above_short_approach_limit(self):
        coach, realm, _ = self.rig()
        realm.add(783, True)
        realm.current = dict(zip(("x", "y", "z"), POINTS[197]))
        realm.current["x"] -= 4
        realm.current["z"] -= 2
        coach.step()
        self.assertEqual([m for m, _ in self.mutations(realm)], [
            "buddy-actions.acquire_interaction", "buddy-actions.travel_quest", "buddy-actions.release_interaction"])

    def test_xp_fallback_only_after_eagan_and_willem_actual_empty_offers(self):
        coach, realm, _ = self.rig()
        realm.rewards.add(783)
        realm.add(7)
        realm.no_breadcrumb = True
        self.advance(coach, until=lambda status: status["phase"] == "waiting_for_level_two")
        offered = [progress.giver_entry(p["npcGuid"]) for m, p in realm.calls if m == "buddy-quests.offer"]
        self.assertEqual(offered, [196, 823])
        self.assertFalse(realm.loot.get("enabled", False))
        self.assertEqual(sum(m == "buddy-actions.practice" and p["enabled"] for m, p in realm.calls), 1)
        for _ in range(3):
            coach.step()
        self.assertEqual(sum(m == "buddy-actions.practice" and p["enabled"] for m, p in realm.calls), 1)
        realm.level = 2
        realm.actions.update(practicing=False, reason="practice_level_two_observed", practicePhase="level_two_observed")
        self.advance(coach, until=lambda status: 33 in realm.active)
        self.assertEqual(sum(m == "buddy-quests.accept" for m, _ in realm.calls), 1)

    def test_missing_fireball_blocks_training_without_trainer_movement(self):
        coach, realm, _ = self.rig()
        realm.add(33)
        realm.loot.update(enabled=True, phase="idle")
        realm.actions["knownFireball"] = False
        self.assertEqual(coach.step()["phase"], "blocked_training_required_fireball")
        self.assertEqual(self.mutations(realm), [])

    def test_unconfirmed_reward_is_not_completion_and_is_never_reclaimed(self):
        coach, realm, saved = self.rig()
        realm.add(33, True)
        realm.current = dict(zip(("x", "y", "z"), POINTS[196]))
        coach.step()  # Bound turn-in offer.
        realm.fail_before = "buddy-quests.claim_reward"
        coach.step()
        realm.last_reward = {"questId": 33, "npcGuid": NPCS[196], "status": "REWARD_UNCONFIRMED", "transportWritten": True}
        resumed, _, _ = self.rig(realm, saved[-1])
        status = resumed.step()
        self.assertEqual(status["phase"], "blocked_unconfirmed_request")
        self.assertEqual(resumed.data["evidence"]["rewards"], {})
        self.assertEqual(sum(m == "buddy-quests.claim_reward" for m, _ in realm.calls), 1)

    def test_returned_confirmed_receipt_survives_reconnect_before_next_observation(self):
        coach, realm, saved = self.rig()
        realm.add(33, True)
        realm.current = dict(zip(("x", "y", "z"), POINTS[196]))
        coach.step()  # Turn-in offer.
        coach.step()  # Claim response is saved, but no subsequent read yet.
        self.assertNotIn("33", saved[-1]["evidence"]["rewards"])
        self.assertEqual(saved[-1]["reservations"][-1]["response"]["state"]["lastReward"]["status"], "REWARD_CONFIRMED")
        realm.session, realm.last_reward = "reconnected-body", {}
        resumed, _, _ = self.rig(realm, saved[-1])
        self.assertEqual(resumed.step()["phase"], "complete")
        self.assertEqual(resumed.data["evidence"]["rewards"]["33"]["sessionId"], "own-body")
        self.assertEqual(sum(m == "buddy-quests.claim_reward" for m, _ in realm.calls), 1)

    def test_returned_turnin_query_after_reconnect_requests_fresh_offer_before_claim(self):
        coach, realm, saved = self.rig()
        realm.add(33, True)
        realm.current = dict(zip(("x", "y", "z"), POINTS[196]))
        coach.step()
        realm.session, realm.turn_in = "replacement-body", {}
        resumed, _, _ = self.rig(realm, saved[-1])
        self.assertEqual(resumed.step()["phase"], "requested_action_reconciling")
        self.assertEqual(sum(m == "buddy-quests.turn_in" for m, _ in realm.calls), 2)
        self.assertEqual(sum(m == "buddy-quests.claim_reward" for m, _ in realm.calls), 0)

    def test_reconnect_during_observation_never_pairs_old_player_with_new_actions(self):
        coach, realm, _ = self.rig()
        realm.add(33)
        realm.on_discover = lambda peer: setattr(peer, "session", "replacement") if peer.discoveries == 2 else None
        self.assertEqual(coach.step()["phase"], "blocked_observation")
        self.assertEqual(self.mutations(realm), [])


class StorageTests(unittest.TestCase):
    def test_private_atomic_state_roundtrip_and_symlink_rejection(self):
        with tempfile.TemporaryDirectory(dir=TEMP_ROOT) as folder:
            path = Path(folder) / "coach"
            store = progress.Store(path)
            store.save({"value": 1})
            self.assertEqual(store.load(), {"value": 1})
            self.assertEqual(store.path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(path.stat().st_mode & 0o777, 0o700)
            store.path.unlink()
            target = Path(folder) / "target"
            target.write_text("preserve")
            store.path.symlink_to(target)
            with self.assertRaises((progress.Error, OSError)):
                store.save({"value": 2})
            self.assertEqual(target.read_text(), "preserve")

    def test_remote_or_credential_urls_and_other_profile_state_are_rejected(self):
        for url in ("http://example.com:8192/rpc", "http://user:password@127.0.0.1:8192/rpc", "http://127.0.0.1:8192/other"):
            with self.assertRaises(progress.Error):
                progress.RPC(url)
        coach = progress.Coach(Realm())
        with self.assertRaises(progress.Error):
            progress.Coach(Realm(), guid=8, stored=coach.data)

    def test_default_command_performs_one_step_and_status_does_no_network(self):
        with tempfile.TemporaryDirectory(dir=TEMP_ROOT) as folder:
            realm = Realm()
            with patch.object(progress, "RPC", return_value=realm), patch("builtins.print"):
                self.assertEqual(progress.main(["--state-dir", folder]), 0)
            self.assertEqual(sum(method == "buddy-quests.offer" for method, _ in realm.calls), 1)
            realm.calls.clear()
            with patch.object(progress, "RPC", return_value=realm), patch("builtins.print"):
                self.assertEqual(progress.main(["status", "--state-dir", folder]), 0)
            self.assertEqual(realm.calls, [])


if __name__ == "__main__":
    unittest.main()
