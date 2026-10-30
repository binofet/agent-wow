#!/usr/bin/env python3
"""Conversation acceptance tests with fake RPC and a local fake Codex process."""

import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import time
import unittest
from unittest.mock import patch


loader = importlib.machinery.SourceFileLoader("converse", str(Path(__file__).with_name("converse")))
spec = importlib.util.spec_from_loader(loader.name, loader)
converse = importlib.util.module_from_spec(spec)
loader.exec_module(converse)


def message(sequence, text="Hello, Arlen", sender=3, channel="SAY", **fields):
    return dict(sequence=str(sequence), text=text, senderGuid=str(sender), channel=channel, **fields)


def poll(messages=(), latest=None, **fields):
    latest = latest if latest is not None else (int(messages[-1]["sequence"]) if messages else 0)
    return dict(messages=list(messages), latestSequence=str(latest), nextSequence=str(latest), **fields)


class FakeRPC:
    def __init__(self, responses):
        self.responses, self.calls = list(responses), []

    def call(self, method, params):
        self.calls.append((method, params))
        if method == "buddy-chat.poll":
            response = self.responses.pop(0)
            if isinstance(response, Exception):
                raise response
            return response
        return {"transportWritten": True}


class FakeBrain:
    def __init__(self, reply=None):
        self.histories = []
        self.reply = reply or {"text": "Hello, Joel!", "emote": "WAVE"}

    def generate(self, history, control_state=None):
        self.histories.append(list(history))
        if isinstance(self.reply, Exception):
            raise self.reply
        return self.reply


class ConversationTests(unittest.TestCase):
    def test_start_tails_old_messages_and_idle_never_calls_model(self):
        rpc = FakeRPC([poll([message(1, "Old chat")]), poll(latest=1)])
        brain = FakeBrain()
        loop = converse.Conversation(rpc, brain)
        loop.tick()
        loop.tick()
        self.assertEqual(brain.histories, [])
        self.assertEqual(loop.cursor, 1)
        self.assertTrue(all(method == "buddy-chat.poll" for method, _ in rpc.calls))

    def test_only_joel_plain_chat_triggers_one_reply_per_burst(self):
        entries = [message(1, sender=99), message(2, self=True),
                   message(3, channel="WHISPER", text="Private"),
                   message(4, text=".server shutdown"), message(5, text="Hi!"),
                   message(6, text="How are you?")]
        rpc = FakeRPC([poll(), poll(entries), poll(latest=6)])
        brain = FakeBrain()
        loop = converse.Conversation(rpc, brain)
        loop.tick()
        loop.tick()
        loop.tick()
        self.assertEqual([entry["text"] for entry in brain.histories[0]], ["Hi!", "How are you?"])
        self.assertEqual(len(brain.histories), 1)
        self.assertEqual(rpc.calls[2], ("buddy-chat.send", {
            "channel": "SAY", "language": "COMMON", "text": "Hello, Joel!"}))
        self.assertEqual(rpc.calls[3], ("buddy-chat.emote", {"emote": "WAVE", "targetGuid": "3"}))

    def test_party_is_routed_to_party_and_history_is_not_exposed_in_say(self):
        rpc = FakeRPC([poll(), poll([message(1, "Party secret", channel="PARTY_LEADER")]),
                       poll([message(2, "Public chat")])])
        brain = FakeBrain({"text": "Understood.", "emote": "NONE"})
        loop = converse.Conversation(rpc, brain)
        for _ in range(3):
            loop.tick()
        self.assertEqual([call[1]["channel"] for call in rpc.calls if call[0] == "buddy-chat.send"], ["PARTY", "SAY"])
        self.assertNotIn("Party secret", json.dumps(brain.histories[1]))

    def test_whisper_reply_stays_private_and_inform_never_triggers(self):
        rpc = FakeRPC([poll(sessionId="one"), poll([message(1, "Private", channel="WHISPER")]), poll(latest=1, sessionId="one"),
                       poll([message(2, "Private echo", channel="WHISPER_INFORM", self=True),
                             message(3, "Public", channel="SAY")]), poll(latest=3, sessionId="one")])
        brain = FakeBrain({"text": "Understood.", "emote": "NONE"})
        loop = converse.Conversation(rpc, brain)
        for _ in range(3):
            loop.tick()
        sends = [params for method, params in rpc.calls if method == "buddy-chat.send"]
        self.assertEqual(sends[0], {"channel": "WHISPER", "language": "COMMON", "text": "Understood.", "recipient": "Joel"})
        self.assertEqual(sends[1]["channel"], "SAY")
        self.assertNotIn("Private", json.dumps(brain.histories[1]))
        self.assertEqual(len(brain.histories), 2)
        self.assertEqual(rpc.calls[1][1]["sessionId"], "one")

    def test_session_binding_is_sent_even_after_replacement_counter_catches_up(self):
        rpc = FakeRPC([poll(latest=5, sessionId="old"), converse.CursorReset(),
                       poll(latest=10, sessionId="replacement"), poll(latest=10)])
        loop = converse.Conversation(rpc, FakeBrain())
        for _ in range(4):
            loop.tick()
        polls = [params for method, params in rpc.calls if method == "buddy-chat.poll"]
        self.assertEqual(polls[1]["sessionId"], "old")
        self.assertEqual(polls[3]["sessionId"], "replacement")
        self.assertEqual(loop.cursor, 10)

    def test_body_restart_during_generation_drops_action_and_reply(self):
        class Controls:
            def __init__(self):
                self.events, self.executed = [], []
            def refresh(self):
                pass
            def context(self):
                return {}
            def execute(self, action):
                self.executed.append(action)
        rpc = FakeRPC([poll(sessionId="old"), poll([message(1, "Follow me")]), converse.CursorReset(),
                       poll(latest=7, sessionId="new")])
        controls = Controls()
        brain = FakeBrain({"text": "Coming along", "emote": "NONE", "action": "FOLLOW"})
        loop = converse.Conversation(rpc, brain, controls=controls)
        loop.tick()
        loop.tick()
        self.assertEqual(controls.executed, [])
        self.assertFalse(any(method == "buddy-chat.send" for method, _ in rpc.calls))
        self.assertFalse(loop.initialized)
        self.assertTrue(all(not history for history in loop.history.values()))
        loop.tick()
        self.assertEqual(loop.session_id, "new")

    def test_restart_baselines_without_replaying_old_messages(self):
        rpc = FakeRPC([poll(latest=50), converse.CursorReset(),
                       poll([message(1, "Already present")]), poll([message(2, "New chat")])])
        brain = FakeBrain()
        loop = converse.Conversation(rpc, brain)
        for _ in range(4):
            loop.tick()
        self.assertEqual(len(brain.histories), 1)
        self.assertEqual(brain.histories[0][0]["text"], "New chat")

    def test_overflow_answers_only_newest_message_and_records_gap(self):
        rpc = FakeRPC([poll(), poll([message(257, "Old backlog"), message(258, "Newest")], cursorLost=True)])
        brain, events = FakeBrain(), []
        loop = converse.Conversation(rpc, brain, status=lambda **fields: events.append(fields))
        loop.tick()
        loop.tick()
        self.assertEqual([entry["text"] for entry in brain.histories[0]], ["Newest"])
        self.assertTrue(any(event.get("event") == "ring_overflow" for event in events))

    def test_invalid_reply_or_generation_failure_cannot_invoke_rpc(self):
        for reply in ({"text": ".server shutdown", "emote": "NONE"},
                      {"text": "Hello", "emote": "DANCE"},
                      {"text": "Hello", "emote": "NONE", "method": "session.logout"},
                      converse.ConversationError("backend unavailable")):
            with self.subTest(reply=reply):
                rpc, brain = FakeRPC([poll(), poll([message(1)])]), FakeBrain(reply)
                loop = converse.Conversation(rpc, brain)
                loop.tick()
                with self.assertRaises(converse.ConversationError):
                    loop.tick()
                self.assertEqual(loop.cursor, 1)
                self.assertTrue(all(method == "buddy-chat.poll" for method, _ in rpc.calls))

    def test_ambiguous_chat_write_is_not_retried_and_no_emote_is_sent(self):
        rpc = FakeRPC([poll(), poll([message(1)]), poll(latest=1)])
        original = rpc.call
        def call(method, params):
            if method == "buddy-chat.send":
                rpc.calls.append((method, params))
                raise converse.ConversationError("write timed out")
            return original(method, params)
        rpc.call = call
        brain = FakeBrain()
        loop = converse.Conversation(rpc, brain)
        loop.tick()
        with self.assertRaises(converse.ConversationError):
            loop.tick()
        loop.tick()
        self.assertEqual(len(brain.histories), 1)
        self.assertEqual(len([call for call in rpc.calls if call[0] == "buddy-chat.send"]), 1)
        self.assertFalse(any(call[0] == "buddy-chat.emote" for call in rpc.calls))

    def test_history_is_bounded_and_stop_after_generation_prevents_send(self):
        rpc = FakeRPC([poll()] + [poll([message(i)]) for i in range(1, 30)])
        brain = FakeBrain({"text": "Hello", "emote": "NONE"})
        loop = converse.Conversation(rpc, brain)
        for _ in range(30):
            loop.tick()
        self.assertLessEqual(len(loop.history["SAY"]), 20)
        self.assertTrue(all(len(history) <= 20 for history in brain.histories))
        rpc = FakeRPC([poll(), poll([message(1)])])
        loop = converse.Conversation(rpc, FakeBrain(), stopped=lambda: True)
        loop.tick()
        with self.assertRaises(converse.Stopped):
            loop.tick()
        self.assertTrue(all(method == "buddy-chat.poll" for method, _ in rpc.calls))

    def test_occasional_conversation_is_private_throttled_and_cannot_start_controls(self):
        class Controls:
            def __init__(self):
                self.events, self.executed = ["server_confirmed_party_join"], []
            def refresh(self):
                pass
            def context(self):
                return {"observations": {"owner": {"present": True, "aliveKnown": True, "alive": True}}}
            def execute(self, action):
                self.executed.append(action)
        clock, controls = [0], Controls()
        rpc = FakeRPC([poll(), poll(), poll(), poll()])
        brain = FakeBrain({"text": "What brought you to Northshire?", "emote": "NONE", "action": "FOLLOW"})
        with patch.object(converse.time, "monotonic", side_effect=lambda: clock[0]):
            loop = converse.Conversation(rpc, brain, controls=controls, banter_seconds=180)
            loop.tick()
            clock[0] = 180
            loop.tick()
            clock[0] = 181
            loop.tick()
            clock[0] = 360
            loop.tick()
        self.assertEqual(len(brain.histories), 2)
        self.assertEqual(controls.executed, [])
        sends = [params for method, params in rpc.calls if method == "buddy-chat.send"]
        self.assertEqual(len(sends), 2)
        self.assertTrue(all(params["channel"] == "WHISPER" and params["recipient"] == "Joel" for params in sends))

    def test_absent_owner_never_triggers_a_model_call(self):
        class Controls:
            events = []
            def refresh(self):
                pass
            def context(self):
                return {"observations": {"owner": {"present": False}}, "party": {"known": True}}
        clock, brain = [0], FakeBrain()
        rpc = FakeRPC([poll(), poll(), poll()])
        with patch.object(converse.time, "monotonic", side_effect=lambda: clock[0]):
            loop = converse.Conversation(rpc, brain, controls=Controls(), banter_seconds=180)
            loop.tick()
            clock[0] = 180
            loop.tick()
            clock[0] = 1000
            loop.tick()
        self.assertEqual(brain.histories, [])

    def test_afk_or_dnd_owner_is_not_interrupted_by_banter(self):
        class Controls:
            events = []
            def refresh(self):
                pass
            def context(self):
                return {"observations": {"owner": {"present": True, "playerFlags": 2}},
                        "party": {"known": True, "inGroup": True,
                                  "members": [{"guid": "3", "online": True}]}}
        clock, brain = [0], FakeBrain()
        rpc = FakeRPC([poll(), poll()])
        with patch.object(converse.time, "monotonic", side_effect=lambda: clock[0]):
            loop = converse.Conversation(rpc, brain, controls=Controls(), banter_seconds=180)
            loop.tick()
            clock[0] = 500
            loop.tick()
        self.assertEqual(brain.histories, [])

    def test_private_history_is_not_used_for_initiated_party_chat(self):
        class Controls:
            events = []
            def refresh(self):
                pass
            def context(self):
                return {"party": {"known": True, "inGroup": True,
                                  "members": [{"guid": "3", "online": True}]}}
        clock, brain = [0], FakeBrain()
        rpc = FakeRPC([poll(), poll()])
        with patch.object(converse.time, "monotonic", side_effect=lambda: clock[0]):
            loop = converse.Conversation(rpc, brain, controls=Controls(), banter_seconds=180)
            loop.history["WHISPER"].append({"speaker": "Joel", "text": "private health details"})
            loop.tick()
            clock[0] = 180
            loop.tick()
        self.assertNotIn("private health details", json.dumps(brain.histories))
        send = next(params for method, params in rpc.calls if method == "buddy-chat.send")
        self.assertEqual(send["channel"], "PARTY")


class ControlsTests(unittest.TestCase):
    def quest_controls(self, quests=None, status="CONFIRMED_IN_LOG"):
        quests = quests or [{"questId": 783, "title": "A Threat Within", "available": True}]
        class RPC:
            def __init__(self):
                self.calls = []
            def call(self, method, params):
                self.calls.append((method, params))
                if method == "buddy-quests.discover":
                    return {"sessionId": "quest-session", "revision": "3", "nearby": [
                        {"canInteract": True, "distanceKnown": True, "distance": 3.5, "creature": {"guid": "17379390966904913963"}},
                        {"canInteract": True, "distanceKnown": True, "distance": 2, "creature": {"guid": "17379390966904913962"}}]}
                if method == "buddy-quests.offer":
                    return {"transportWritten": True, "serverResponseReceived": True, "state": {
                        "sessionId": "quest-session", "offer": {"revision": "7", "npcGuid": params["npcGuid"], "quests": quests}}}
                if method == "buddy-quests.accept":
                    return {"transportWritten": True, "state": {"lastAcceptance": {
                        "status": status, "questId": params["questId"]}}}
                raise AssertionError(method)
        rpc = RPC()
        controls = converse.BuddyControls(rpc)
        controls.quests_available = True
        return controls, rpc

    def test_quest_discovery_selects_nearest_real_offer_without_accepting(self):
        controls, rpc = self.quest_controls()
        self.assertEqual(controls.execute("DISCOVER_QUESTS"), "Available here: A Threat Within (783).")
        self.assertEqual(rpc.calls[1], ("buddy-quests.offer", {"sessionId": "quest-session", "npcGuid": "17379390966904913962"}))
        self.assertFalse(any(method == "buddy-quests.accept" for method, _ in rpc.calls))

    def test_quest_accept_binds_offer_and_requires_actual_log_confirmation(self):
        controls, rpc = self.quest_controls()
        self.assertEqual(controls.execute("ACCEPT_QUEST"), "It's in my quest log: A Threat Within.")
        self.assertEqual(rpc.calls[-1], ("buddy-quests.accept", {"sessionId": "quest-session", "offerRevision": "7",
            "npcGuid": "17379390966904913962", "questId": 783}))
        for state in ("REQUESTED", "UNCONFIRMED_TIMEOUT", "WRITE_UNCONFIRMED"):
            controls, _ = self.quest_controls(status=state)
            self.assertIn("haven't confirmed", controls.execute("ACCEPT_QUEST", quest_id=783))
        controls, _ = self.quest_controls(status="SERVER_REJECTED")
        self.assertIn("server rejected", controls.execute("ACCEPT_QUEST"))

    def test_unavailable_quest_offer_does_not_invent_a_distance_problem(self):
        controls, rpc = self.quest_controls([{"questId": 783, "title": "A Threat Within", "available": False}])
        self.assertEqual(controls.execute("ACCEPT_QUEST"), "This NPC isn't offering me a quest I can accept right now.")
        self.assertFalse(any(method == "buddy-quests.accept" for method, _ in rpc.calls))

    def test_quest_ambiguity_wrong_id_and_ambiguous_write_never_retry(self):
        entries = [{"questId": 783, "title": "First", "available": True},
                   {"questId": 784, "title": "Second", "available": True}]
        controls, rpc = self.quest_controls(entries)
        self.assertIn("Which", controls.execute("ACCEPT_QUEST"))
        self.assertFalse(any(method == "buddy-quests.accept" for method, _ in rpc.calls))
        self.assertIn("isn't in the current offer", controls.execute("ACCEPT_QUEST", quest_id=999))
        controls, rpc = self.quest_controls()
        original = rpc.call
        def call(method, params):
            if method == "buddy-quests.accept":
                rpc.calls.append((method, params))
                raise converse.ConversationError("ambiguous write")
            return original(method, params)
        rpc.call = call
        self.assertIn("won't retry", controls.execute("ACCEPT_QUEST"))
        self.assertEqual(sum(method == "buddy-quests.accept" for method, _ in rpc.calls), 1)

    def test_loot_is_explicit_and_independent_of_missing_follow_module(self):
        rpc = FakeRPC([])
        controls = converse.BuddyControls(rpc)
        self.assertIn("isn't ready", controls.execute("ENABLE_LOOTING"))
        self.assertEqual(rpc.calls, [])
        controls.loot_available = True
        def call(method, params):
            rpc.calls.append((method, params))
            return {"enabled": params["enabled"]}
        rpc.call = call
        self.assertIn("loot nearby", controls.execute("ENABLE_LOOTING"))
        self.assertEqual(controls.execute("DISABLE_LOOTING"), "Automatic looting is off.")
        self.assertEqual(rpc.calls, [("buddy-loot.enable", {"enabled": True}), ("buddy-loot.enable", {"enabled": False})])

    def trade_controls(self, backpack=None):
        backpack = backpack if backpack is not None else [{"slot": 2, "entry": 159, "count": 3,
            "itemGuid": "4611686018427387970", "name": "Refreshing Spring Water", "metadataKnown": True}]
        class RPC:
            def __init__(self):
                self.calls = []
            def call(self, method, params):
                self.calls.append((method, params))
                return {"sessionId": "trade-session", "revision": "8", "phase": "OPEN", "backpackKnown": True,
                    "backpackRevision": "11", "backpack": backpack}
        rpc = RPC()
        controls = converse.BuddyControls(rpc)
        controls.trade_available = True
        return controls, rpc

    def test_trade_whole_stack_binds_current_inventory_and_never_claims_completion(self):
        controls, rpc = self.trade_controls()
        result = controls.execute("OFFER_TRADE_ITEM", item_entry=159)
        self.assertIn("whole stack of 3 Refreshing Spring Water", result)
        self.assertIn("Check your trade window", result)
        self.assertNotIn("completed", result)
        self.assertEqual(rpc.calls[-1], ("buddy-trade.offer_item", {"sessionId": "trade-session", "revision": "8",
            "backpackRevision": "11", "backpackSlot": 2, "expectedItemGuid": "4611686018427387970", "tradeSlot": 0}))

    def test_trade_rejects_ambiguous_or_unknown_stacks_and_unspecified_gold(self):
        row = {"slot": 2, "entry": 159, "count": 3, "itemGuid": "42", "metadataKnown": True}
        for backpack in ([], [row, dict(row, slot=3, itemGuid="43")], [dict(row, metadataKnown=False)]):
            controls, rpc = self.trade_controls(backpack)
            controls.execute("OFFER_TRADE_ITEM", item_entry=159)
            self.assertEqual([method for method, _ in rpc.calls], ["buddy-trade.state"])
        controls, rpc = self.trade_controls()
        self.assertIn("exact amount", controls.execute("OFFER_TRADE_GOLD"))
        self.assertFalse(any(method == "buddy-trade.offer_gold" for method, _ in rpc.calls))
        controls.execute("OFFER_TRADE_GOLD", gold_copper=12345)
        self.assertEqual(rpc.calls[-1], ("buddy-trade.offer_gold", {"sessionId": "trade-session", "revision": "8", "copper": 12345}))

    def test_missing_new_capabilities_never_enable_or_write_by_default(self):
        class RPC:
            def __init__(self):
                self.calls = []
            def call(self, method, params):
                self.calls.append((method, params))
                raise converse.CapabilityUnavailable("not installed")
        rpc = RPC()
        controls = converse.BuddyControls(rpc)
        controls.refresh()
        self.assertFalse(controls.quests_available or controls.loot_available or controls.trade_available)
        self.assertFalse(any(method.endswith((".enable", ".assist", ".accept", ".open")) for method, _ in rpc.calls))

    def test_only_exact_owner_invitation_is_accepted_once_even_after_ambiguous_failure(self):
        class RPC:
            def __init__(self):
                self.calls, self.session, self.name = [], "one", "Joel"
            def call(self, method, params):
                self.calls.append((method, params))
                if method == "buddy-party.state":
                    return {"sessionId": self.session, "pendingInvite": {"sequence": "1", "inviterName": self.name}}
                if method == "buddy-party.accept":
                    raise converse.ConversationError("ambiguous write")
                return {}
        rpc = RPC()
        controls = converse.BuddyControls(rpc)
        controls.refresh()
        controls.refresh()
        rpc.name = "Otherplayer"
        controls.refresh()
        rpc.name, rpc.session = "Joel", "replacement"
        controls.refresh()
        accepted = [params for method, params in rpc.calls if method == "buddy-party.accept"]
        self.assertEqual(accepted, [{"sessionId": "one", "inviteSequence": "1", "inviterName": "Joel"},
                                    {"sessionId": "replacement", "inviteSequence": "1", "inviterName": "Joel"}])

    def test_model_action_maps_only_to_fixed_owner_control(self):
        rpc = FakeRPC([])
        controls = converse.BuddyControls(rpc)
        self.assertEqual(controls.execute("FOLLOW"), "That ability isn't ready yet.")
        self.assertEqual(rpc.calls, [])
        controls.actions_available = True
        controls.execute("FOLLOW")
        controls.execute("HEAL_ONCE")
        self.assertEqual(rpc.calls, [("buddy-actions.follow", {"enabled": True}), ("buddy-actions.heal_once", {})])
        with self.assertRaises(converse.ConversationError):
            converse.validate_reply({"text": "hello", "emote": "NONE", "action": "session.logout"})

    def test_blocked_heal_is_never_described_as_a_cast_attempt(self):
        class RPC:
            def __init__(self):
                self.reason = "mana_unknown_or_insufficient"
            def call(self, method, params):
                return {"reason": self.reason}
        rpc = RPC()
        controls = converse.BuddyControls(rpc)
        controls.actions_available = True
        self.assertIn("mana", controls.execute("HEAL_ONCE"))
        rpc.reason = "heal_transport_written_waiting_for_server"
        self.assertEqual(controls.execute("HEAL_ONCE"), "I'm trying Lesser Heal now.")
        rpc.reason = "unknown_blocker"
        self.assertEqual(controls.execute("HEAL_ONCE"), "I couldn't start Lesser Heal yet.")


class BoundaryTests(unittest.TestCase):
    def test_numeric_action_schema_is_required_bounded_and_legacy_compatible(self):
        for key in converse.NUMERIC_ARGS:
            self.assertIn(key, converse.REPLY_SCHEMA["required"])
        legacy = converse.validate_reply({"text": "Hello", "emote": "NONE"})
        self.assertEqual(legacy, {"text": "Hello", "emote": "NONE", "action": "NONE"})
        value = {"text": "I'll check.", "emote": "NONE", "action": "ACCEPT_QUEST", "quest_id": 783, "item_entry": 0, "gold_copper": 0}
        self.assertEqual(converse.validate_reply(value), value)
        for bad in (True, "783", 1.5, -1, 2**32):
            with self.subTest(bad=bad), self.assertRaises(converse.ConversationError):
                converse.validate_reply(dict(value, quest_id=bad))
        with self.assertRaises(converse.ConversationError):
            converse.validate_reply(dict(value, npc_guid=123))

    def test_multibyte_limit_and_control_command_markup_rejection(self):
        self.assertEqual(converse.plain_text("é" * 127), "é" * 127)
        for text in ("é" * 128, "", "  ", " x", "x\n", "|Hitem", "/say hi", "!command", ".command", "a\u200bb", "\ud800"):
            with self.subTest(text=repr(text)), self.assertRaises(converse.ConversationError):
                converse.plain_text(text)

    def test_rpc_only_allows_explicit_loopback_and_no_proxy_or_redirects(self):
        for url in ("https://127.0.0.1:8086/rpc", "http://example.com:8086/rpc",
                    "http://127.0.0.1/rpc", "http://u:p@127.0.0.1:8086/rpc",
                    "http://127.0.0.1:8086/rpc?token=x", "http://127.0.0.1:8086/other"):
            with self.subTest(url=url), self.assertRaises(converse.ConversationError):
                converse.RPC(url)
        rpc = converse.RPC("http://127.0.0.1:8086/rpc")
        with self.assertRaises(converse.ConversationError):
            rpc.call("session.logout", {})

    def test_rpc_validates_response_id_and_precise_restart_fault(self):
        class Response:
            def __init__(self, data):
                self.data = data
            def __enter__(self):
                return self
            def __exit__(self, *args):
                pass
            def read(self, limit):
                return json.dumps(self.data).encode()
        rpc = converse.RPC("http://127.0.0.1:8086/rpc")
        with patch.object(rpc.opener, "open", return_value=Response({"jsonrpc": "2.0", "id": 999, "result": {}})):
            with self.assertRaises(converse.ConversationError):
                rpc.call("buddy-chat.poll", {})
        with patch.object(rpc.opener, "open", return_value=Response({"jsonrpc": "2.0", "id": 2, "error": {"message": "rpc error: cursor is newer than this module session; restart polling with after_sequence 0"}})):
            with self.assertRaises(converse.CursorReset):
                rpc.call("buddy-chat.poll", {})

    def test_cli_flags_preserve_readonly_and_disable_integrations(self):
        command = converse.codex_command("codex", "workspace", "schema", "output")
        self.assertEqual(command[command.index("--sandbox") + 1], "read-only")
        self.assertIn("--ignore-user-config", command)
        self.assertIn("--ephemeral", command)
        self.assertIn('web_search="disabled"', command)
        self.assertTrue(all(feature in command for feature in converse.DISABLED_FEATURES))
        self.assertNotIn("--ignore-rules", command)
        self.assertFalse(any("dangerously" in arg for arg in command))
        self.assertNotIn("--model", command)

    def test_tool_error_and_incomplete_events_rejected(self):
        for item in ({"type": "command_execution", "command": "pwd"},
                     {"type": "mcp_tool_call"}, {"type": "error", "message": "bad"}):
            raw = json.dumps({"type": "item.completed", "item": item}) + '\n{"type":"turn.completed"}\n'
            with self.assertRaises(converse.ConversationError):
                converse.inspect_events(raw.encode())
        for raw in (b"{}\n", b"[]\n", b'{"type":"item.completed","item":[]}\n',
                    b'{"type":"turn.failed"}\n', b'{"type":"turn.started"}\n'):
            with self.assertRaises(converse.ConversationError):
                converse.inspect_events(raw)
        converse.inspect_events(b'{"type":"thread.started"}\n{"type":"turn.started"}\n{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}\n{"type":"turn.completed"}\n')

    def test_only_exact_intentional_host_warning_is_allowed_before_turn(self):
        warning = json.dumps({"type": "item.completed", "item": {
            "id": "item_0", "type": "error", "message": converse.DISABLED_CODE_MODE_WARNING}}).encode() + b"\n"
        start, end = b'{"type":"turn.started"}\n', b'{"type":"turn.completed"}\n'
        converse.inspect_events(warning + start + end)
        with self.assertRaises(converse.ConversationError):
            converse.inspect_events(start + warning + end)
        with self.assertRaises(converse.ConversationError):
            converse.inspect_events(warning.replace(b"fail closed", b"something else") + start + end)

    def test_real_subprocess_contract_with_fake_cli_and_timeout(self):
        # Run a real local child to exercise JSONL, output-file and timeout paths;
        # this is not an OpenAI call and does not read CLI authentication.
        with tempfile.TemporaryDirectory() as temporary:
            folder = Path(temporary)
            fake = folder / "fake-codex"
            fake.write_text("#!/usr/bin/env python3\nimport json,pathlib,sys\n"
                            "args=sys.argv; sys.stdin.read()\n"
                            "workspace=pathlib.Path(args[args.index('--cd')+1]); schema=pathlib.Path(args[args.index('--output-schema')+1])\n"
                            "assert not workspace.is_relative_to(schema.parent)\n"
                            "pathlib.Path(args[args.index('--output-last-message')+1]).write_text(json.dumps({'text':'Hi, Joel!','emote':'WAVE'}))\n"
                            "print(json.dumps({'type':'turn.completed'}))\n")
            fake.chmod(0o700)
            reply = converse.CodexBrain(fake, folder).generate([{"speaker": "Joel", "text": "Hi"}])
            self.assertEqual(reply, {"text": "Hi, Joel!", "emote": "WAVE", "action": "NONE"})
            self.assertEqual(list(folder.iterdir()), [fake])
            fake.write_text("#!/usr/bin/env python3\nimport time\ntime.sleep(10)\n")
            started = time.monotonic()
            with self.assertRaises(converse.ConversationError):
                converse.CodexBrain(fake, folder, timeout=0.15).generate([])
            self.assertLess(time.monotonic() - started, 4)
            self.assertEqual(list(folder.iterdir()), [fake])

    def test_private_state_rejects_symlink_and_singleton_lock(self):
        with tempfile.TemporaryDirectory() as temporary:
            folder = converse.ensure_state(Path(temporary) / "private")
            self.assertEqual(folder.stat().st_mode & 0o777, 0o700)
            converse.write_json(folder / "status.json", {"instance": "mine"})
            self.assertEqual((folder / "status.json").stat().st_mode & 0o777, 0o600)
            (folder / "link").symlink_to(folder / "status.json")
            with self.assertRaises(OSError):
                converse.private_open(folder / "link", os.O_RDONLY)
            with os.fdopen(converse.private_open(folder / "lock", os.O_RDWR | os.O_CREAT), "r+") as first:
                converse.fcntl.flock(first, converse.fcntl.LOCK_EX)
                with os.fdopen(converse.private_open(folder / "lock", os.O_RDWR), "r+") as second:
                    self.assertTrue(converse.is_locked(second))


if __name__ == "__main__":
    unittest.main()
