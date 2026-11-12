#!/usr/bin/env python3
"""Quest goal routing and cooperative controller ownership, without live RPCs."""
import copy
import fcntl
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import time
import unittest
from unittest.mock import patch


def load(name, filename):
    loader = importlib.machinery.SourceFileLoader(name, str(Path(__file__).with_name(filename)))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


c = load("quest_chat_tested", "converse")


class RPC:
    url = "http://127.0.0.1:8086/rpc"
    def __init__(self):
        self.calls = []
        self.state = dict(sessionId="actual-item-body", selfGuid="1", questId=11360,
                          questActive=True, questLogKnown=True, objectiveCount=2, requiredCount=5)
    def call(self, method, params):
        self.calls.append((method, copy.deepcopy(params)))
        if method == "buddy-quest-items.state":
            if self.state is None: raise c.CapabilityUnavailable("not installed")
            return copy.deepcopy(self.state)
        if method == "buddy-actions.follow": return {"following": params["enabled"]}
        if method == "buddy-actions.status": return {}
        raise c.CapabilityUnavailable("not installed")


class QuestChatTests(unittest.TestCase):
    def rig(self):
        rpc = RPC()
        controls = c.BuddyControls(rpc)
        controls.actions_available = True
        return controls, rpc

    def test_model_selects_numeric_goal_and_cannot_supply_item_spells_or_coordinates(self):
        for action in ("EXECUTE_QUEST", "STOP_QUEST"):
            self.assertIn(action, c.REPLY_SCHEMA["properties"]["action"]["enum"])
            value = dict(text="I will try the objective.", emote="NONE", action=action, quest_id=11360)
            self.assertEqual(c.validate_reply(value)["quest_id"], 11360)
            for key, arg in (("quest_id", True), ("quest_id", -1), ("quest_id", "11360"), ("x", 5), ("spell_id", 42340)):
                with self.subTest(action=action, key=key):
                    with self.assertRaises(c.ConversationError): c.validate_reply(dict(value, **{key: arg}))
        self.assertEqual(c.validate_reply(dict(text="Hello.", emote="NONE"))["action"], "NONE")

    def test_execution_launch_is_bound_to_actual_character_body_endpoint_and_goal(self):
        controls, rpc = self.rig()
        with tempfile.TemporaryDirectory() as temporary, patch.object(c, "ROOT", Path(temporary)), \
                patch.object(c, "quest_task_snapshot", return_value={"active": False}), \
                patch.object(c, "area_quest_snapshot", return_value={"active": False}) as area, \
                patch.object(c.subprocess, "Popen") as spawn:
            reply = controls.execute("EXECUTE_QUEST", quest_id=11360)
            self.assertIn("up to five minutes", reply)
            args = spawn.call_args.args[0]
            self.assertEqual(args[-8:], ["--character-guid", "1", "--rpc-url", rpc.url,
                                       "--quest-id", "11360", "--session-id", "actual-item-body"])
            self.assertTrue(spawn.call_args.kwargs["start_new_session"])
            area.assert_called_once_with(1, stop=True)
            self.assertEqual([m for m,p in rpc.calls], ["buddy-quest-items.state"])
            self.assertEqual((Path(temporary)/"data/quest-task/1/live.log").stat().st_mode & 0o777, 0o600)

    def test_uninstalled_wrong_body_absent_complete_or_unsupported_quest_cannot_spawn(self):
        for mode in ("missing", "foreign", "absent", "unknown", "complete", "unsupported", "moving_missing"):
            controls, rpc = self.rig()
            if mode == "missing": rpc.state = None
            elif mode == "foreign": rpc.state["selfGuid"] = "4"
            elif mode == "absent": rpc.state["questActive"] = False
            elif mode == "unknown": rpc.state["questLogKnown"] = False
            elif mode == "complete": rpc.state["questComplete"] = True
            elif mode == "moving_missing": controls.actions_available = False
            with self.subTest(mode=mode), patch.object(c, "quest_task_snapshot", return_value={"active": False}), \
                    patch.object(c.subprocess, "Popen") as spawn:
                reply = controls.execute("EXECUTE_QUEST", quest_id=7 if mode == "unsupported" else 11360)
                spawn.assert_not_called()
                self.assertNotIn("I'm starting", reply)

    def test_stop_and_explicit_follow_have_exact_owned_stop_identity(self):
        controls, rpc = self.rig()
        with patch.object(c, "quest_task_snapshot", return_value={"active": True, "stopRequested": True}) as snapshot:
            self.assertIn("requested", controls.execute("STOP_QUEST", quest_id=11360))
            snapshot.assert_called_with(1, rpc.url, 11360, stop=True)
        with patch.object(c, "quest_task_snapshot") as snapshot, patch.object(c, "area_quest_snapshot"):
            controls.execute("STOP_FOLLOWING")
            snapshot.assert_called_once_with(1, rpc.url, stop=True, restore_follow=False)
        self.assertEqual(rpc.calls[-1], ("buddy-actions.follow", {"enabled": False}))

    def test_status_requires_private_live_lock_exact_endpoint_goal_and_safe_file(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(c, "ROOT", Path(temporary)):
            folder = c.ensure_state(c.ensure_state(Path(temporary)/"data/quest-task")/"1")
            record = dict(identity=c.quest_task_identity(1, RPC.url, 11360), instance="a"*32,
                          bodySessionId="actual-item-body",routineActive=True, updatedAt=time.time(), own=dict(phase="waiting_for_server_item_operation",
                          objectiveCount=2, requiredCount=5, attempts=3, observationsCurrent=True))
            c.write_json(folder/"status.json", record)
            with os.fdopen(c.private_open(folder/"lock", os.O_RDWR|os.O_CREAT), "r+") as lock:
                fcntl.flock(lock, fcntl.LOCK_EX|fcntl.LOCK_NB)
                self.assertTrue(c.quest_task_snapshot(1, RPC.url, 11360)["active"])
                self.assertFalse(c.quest_task_snapshot(1, "http://127.0.0.1:8087/rpc")["active"])
                self.assertFalse(c.quest_task_snapshot(1, RPC.url, 7)["active"])
                result = c.quest_task_snapshot(1, RPC.url, 11360, stop=True, restore_follow=False)
                self.assertTrue(result["stopRequested"])
                self.assertEqual(json.loads((folder/"stop.json").read_text()),
                    dict(identity=record["identity"], instance="a"*32, restoreFollowing=False))
                record["updatedAt"] -= 20; c.write_json(folder/"status.json", record)
                self.assertFalse(c.quest_task_snapshot(1)["observationsCurrent"])
                (folder/"status.json").unlink(); (folder/"status.json").symlink_to(folder/"stop.json")
                self.assertFalse(c.quest_task_snapshot(1)["active"])

    def test_readonly_module_refresh_capability_and_foreign_module_failure(self):
        controls, rpc = self.rig()
        controls.refresh()
        self.assertTrue(controls.quest_items_available)
        rpc.state["selfGuid"] = "4"
        controls.refresh()
        self.assertFalse(controls.quest_items_available)
        self.assertTrue(all(m != "buddy-quest-items.attempt" for m,p in rpc.calls))

    def test_task_counter_is_stale_after_actual_body_session_changes(self):
        controls,rpc=self.rig()
        controls.quest_items_available=True
        controls.quest_items=dict(sessionId="new-body")
        with patch.object(c,"quest_task_snapshot",return_value=dict(active=True,bodySessionId="old-body",observationsCurrent=True)):
            self.assertFalse(controls.context()["quest_task"]["observationsCurrent"])

    def test_background_controllers_defer_but_shared_offer_poll_remains_first(self):
        area = load("task_defer_area", "area-quests")
        maintenance = load("task_defer_maintenance", "party-maintenance")
        party = load("task_defer_party", "party-quests")
        rpc = RPC()
        for module, actor, helper in (
            (area, area.AreaSweep("Arlen", 1, rpc), area.converse),
            (maintenance, maintenance.Maintenance("Arlen", 1, rpc, lambda value: None), maintenance.c),
            (party, party.PartyQuestSync("Arlen", 1, rpc), party.converse)):
            with self.subTest(module=module.__name__), patch.object(helper, "quest_task_snapshot", return_value={"active": True}):
                if module is party:
                    with patch.object(actor, "poll_shared_quest", return_value=False) as poll:
                        actor.tick(); poll.assert_called_once_with()
                else: actor.tick()
                self.assertEqual(actor.status["phase"], "waiting_for_requested_quest_task")
        self.assertEqual(rpc.calls, [])


if __name__ == "__main__": unittest.main()
