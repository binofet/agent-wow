#!/usr/bin/env python3
"""Conversation acceptance tests with fake RPC and a local fake Codex process."""

import copy
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import time
import types
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


class ProfilePartyTests(unittest.TestCase):
    def setup_party(self, responses, reply=None, members=None):
        group={"known":True,"inGroup":True,"members":members or [
            {"guid":"3","name":"Joel","online":True},
            {"guid":"10","name":"Mira","online":True,"self":True},
            {"guid":"1","name":"Arlen","online":True}]}
        class Controls:
            def __init__(self):
                self.events,self.executed=[],[]
                self.party={"group":group}
            def refresh(self): pass
            def context(self): return {"party":group}
            def execute(self, action, **args): self.executed.append(action)
        controls=Controls()
        rpc=FakeRPC(responses)
        original=rpc.call
        def call(method,params):
            if method=="buddy-party.state":
                rpc.calls.append((method,params))
                return {"group":group}
            return original(method,params)
        rpc.call=call
        brain=FakeBrain(reply or {"text":"Good to meet you, Arlen.","emote":"NONE","action":"FOLLOW"})
        loop=converse.Conversation(rpc,brain,controls=controls,character_name="Mira",character_class="mage",character_guid=10,party_agents={1:"Arlen"})
        return loop,rpc,brain,controls,group

    def test_named_peer_gets_bounded_reply_without_control_authority(self):
        clock=[0]
        with patch.object(converse.time,"monotonic",side_effect=lambda:clock[0]):
            loop,rpc,brain,controls,_=self.setup_party([poll(),poll([message(1,"Arlen, tell us a story",channel="PARTY")]),
                poll([message(2,"Mira, follow me?",sender=1,channel="PARTY")]),
                poll([message(3,"Mira, another question?",sender=1,channel="PARTY")]),
                poll([message(4,"Mira, third question?",sender=1,channel="PARTY")]),
                poll([message(5,"Mira, fourth question?",sender=1,channel="PARTY")])])
            loop.tick();loop.tick() # Owner directs another character, quietly opens finite conversation budget.
            clock[0]=1;loop.tick()
            clock[0]=2;loop.tick() # Cooldown: no echo reply.
            clock[0]=32;loop.tick()
            clock[0]=63;loop.tick() # Budget exhausted, regardless of another named question.
        self.assertEqual(len(brain.histories),2)
        self.assertEqual(controls.executed,[])
        self.assertEqual([p["channel"] for m,p in rpc.calls if m=="buddy-chat.send"],["PARTY","PARTY"])
        self.assertTrue(all(h[-1]["speaker"]=="Arlen" for h in brain.histories))
        self.assertEqual(loop.history["PARTY"][-1]["speaker"],"Mira")

    def test_verified_peer_can_request_only_the_separate_repair_queue(self):
        for action in ("REPAIR_CONTROLLER","FOLLOW"):
            loop,rpc,brain,controls,_=self.setup_party([poll(),poll([message(1,"Arlen, debug this",channel="PARTY")]),
                poll([message(2,"Mira, please repair the quest sweep refusal",sender=1,channel="PARTY")])],
                reply={"text":"I'll report that failure.","emote":"NONE","action":action})
            loop.tick();loop.tick();loop.tick()
            self.assertEqual(controls.executed,[action] if action=="REPAIR_CONTROLLER" else [])

    def test_controller_refusal_preserves_failed_report_acknowledgement(self):
        rpc=FakeRPC([poll(),poll([message(1,"Follow me")])])
        controls=types.SimpleNamespace(events=[],refresh=lambda:None,context=lambda:{},
            execute=lambda *a,**k:(_ for _ in ()).throw(converse.ConversationError('refused')),
            report_problem=lambda *a,**k:"I couldn't submit the workshop report. That repair is still unconfirmed.")
        loop=converse.Conversation(rpc,FakeBrain({'text':'Coming','emote':'NONE','action':'FOLLOW'}),controls=controls)
        loop.tick();loop.tick()
        sent=[p for m,p in rpc.calls if m=='buddy-chat.send']
        self.assertIn("couldn't submit",sent[0]['text'])

    def test_peer_requires_roster_named_address_and_current_server_membership(self):
        for mode in ("stranger","unnamed","whisper","absent","offline","wrongname","ownerabsent"):
            with self.subTest(mode=mode):
                peer=message(2,"Mira, hello",sender=1,channel="PARTY")
                loop,rpc,brain,_,group=self.setup_party([poll(),poll([message(1,"Arlen, speak",channel="PARTY")]),poll([peer])])
                loop.tick();loop.tick()
                if mode=="stranger":peer["senderGuid"]="99"
                if mode=="unnamed":peer["text"]="Hello everyone"
                if mode=="whisper":peer["channel"]="WHISPER"
                if mode=="absent":group["members"]=group["members"][:2]
                if mode=="offline":group["members"][2]["online"]=False
                if mode=="wrongname":group["members"][2]["name"]="Other"
                if mode=="ownerabsent":group["members"]=group["members"][1:]
                loop.tick()
                self.assertFalse(brain.histories)
                self.assertFalse(any(m=="buddy-chat.send" for m,_ in rpc.calls))

    def test_peer_leaving_group_during_generation_drops_reply(self):
        loop,rpc,brain,_,group=self.setup_party([poll(),poll([message(1,"Arlen, speak",channel="PARTY")]),poll([message(2,"Mira, hello",sender=1,channel="PARTY")])])
        original=brain.generate
        def generate(history,state):
            reply=original(history,state)
            group["members"]=group["members"][:2]
            return reply
        brain.generate=generate
        for _ in range(3):loop.tick()
        self.assertEqual(len(brain.histories),1)
        self.assertFalse(any(m=="buddy-chat.send" for m,_ in rpc.calls))

    def test_single_agent_owner_commands_skip_others_but_party_wide_request_acts(self):
        loop,_,brain,controls,_=self.setup_party([poll(),poll([message(1,"Arlen, follow me",channel="PARTY")]),
            poll([message(2,"Arlen, follow Mira",channel="PARTY")]),
            poll([message(3,"Everyone, follow me",channel="PARTY")])])
        for _ in range(4):loop.tick()
        self.assertEqual(len(brain.histories),1)
        self.assertEqual(controls.executed,["FOLLOW"])
        self.assertEqual(brain.histories[0][-1]["text"],"Everyone, follow me")
        self.assertEqual(converse.addressed_names("Arlen and Mira, follow",["Arlen","Mira"]),{"arlen","mira"})
        self.assertEqual(converse.addressed_names("Arlen, heal Joel. Mira, attack.",["Arlen","Mira"]),{"arlen","mira"})

    def test_class_persona_capability_truth_and_nonpriest_heal_gate(self):
        for name,role,class_id,known in (("Mira","mage",8,"knownFireball"),("Rook","rogue",4,"knownAutoAttack")):
            persona=converse.character_persona(name,role)
            self.assertIn("You are "+name,persona)
            self.assertIn("embodied as a\n"+role,persona)
            self.assertNotIn("You are Arlen",persona)
            rpc=FakeRPC([])
            controls=converse.BuddyControls(rpc,character_class=role)
            controls.actions_available=True
            controls.actions={"classKnown":True,"classId":class_id,known:True,"knownLesserHeal":True}
            self.assertTrue(controls.context()["abilities"]["assisting"])
            self.assertFalse(controls.context()["abilities"]["healing"])
            rpc.calls.clear()
            self.assertIn("don't have",controls.execute("HEAL_ONCE"))
            self.assertFalse(rpc.calls)
            controls.actions["classKnown"]=False
            self.assertFalse(controls.context()["abilities"]["assisting"])

    def test_warlock_assistance_requires_actual_class_and_known_shadow_bolt(self):
        persona=converse.character_persona("Nixwick","warlock")
        self.assertIn("You are Nixwick",persona)
        self.assertIn("embodied as a\nwarlock",persona)
        self.assertNotIn("embodied as a human",persona)
        rpc=FakeRPC([])
        controls=converse.BuddyControls(rpc,character_class="warlock",character_guid=8)
        controls.actions_available=True
        controls.actions={"classKnown":True,"classId":9,"knownShadowBolt":True}
        self.assertTrue(controls.context()["abilities"]["assisting"])
        self.assertFalse(controls.context()["abilities"]["healing"])
        for change in ({"knownShadowBolt":False},{"knownShadowBolt":None},{"classKnown":False},{"classId":8}):
            with self.subTest(change=change):
                controls.actions={"classKnown":True,"classId":9,"knownShadowBolt":True,**change}
                self.assertFalse(controls.context()["abilities"]["assisting"])
        controls.actions={"classKnown":True,"classId":9,"knownFireball":True}
        self.assertFalse(controls.context()["abilities"]["assisting"])
        self.assertTrue(all(method in ("buddy-world.snapshot","buddy-chat.emotes") for method,_ in rpc.calls))

    def test_profiles_isolate_state_and_validate_roster(self):
        def args(**extra):
            base=dict(profile="default",character_name=None,character_class=None,character_guid=None,
                      player_name="Joel",player_guid=3,state_dir=None,rpc_url="http://127.0.0.1:8086/rpc",party_agent=[])
            base.update(extra)
            return types.SimpleNamespace(**base)
        path,identity,roster=converse.conversation_profile(args())
        self.assertEqual(path,converse.ROOT/"data/converse")
        self.assertEqual(identity["name"],"Arlen")
        a=args(profile="mage",character_name="Mira",character_class="mage",character_guid=10,rpc_url="http://127.0.0.1:8087/rpc",party_agent=["Arlen:1","Rook:11"])
        path,identity,roster=converse.conversation_profile(a)
        self.assertEqual(path,converse.ROOT/"data/converse/mage")
        self.assertEqual(roster,{1:"Arlen",11:"Rook"})
        self.assertEqual(identity["guid"],10)
        for values in (["Arlen:1","Other:1"],["Arlen:3"],["Mira:11"],["Arlen:1:2"],["Arlen:0"]):
            with self.assertRaises(converse.ConversationError):
                converse.parse_roster(values,10,"Mira",3)
        with self.assertRaises(converse.ConversationError):
            converse.conversation_profile(args(profile="../other"))
        with self.assertRaises(converse.ConversationError):
            converse.conversation_profile(args(profile="mage"))

    def test_stop_cannot_target_another_characters_shared_state_directory(self):
        with tempfile.TemporaryDirectory() as temporary:
            folder=converse.ensure_state(Path(temporary)/"brain")
            converse.write_json(folder/"status.json",{"instance":"other-instance","character_identity":{"profile":"other"}})
            with os.fdopen(converse.private_open(folder/"lock",os.O_RDWR|os.O_CREAT),"r+") as lock:
                converse.fcntl.flock(lock,converse.fcntl.LOCK_EX)
                with self.assertRaisesRegex(converse.ConversationError,"another character"):
                    converse.main(["stop","--profile","mira","--character-name","Mira","--character-class","mage",
                                   "--character-guid","4","--rpc-url","http://127.0.0.1:8087/rpc","--state-dir",str(folder)])
            self.assertFalse((folder/"stop.json").exists())


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
                if method == "buddy-quests.state": return {}
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

    def auto_quest(self, flags=0x80008, initial=None, polls=()):
        controls,rpc=self.quest_controls([{"questId":2158,"title":"Rest and Relaxation","available":True,"flags":flags}])
        original=rpc.call
        pending=list(polls)
        def call(method,params):
            if method=="buddy-quests.state":
                rpc.calls.append((method,params))
                return pending.pop(0) if pending else {"sessionId":"quest-session","questLogKnown":True,"questLog":[]}
            result=original(method,params)
            if method=="buddy-quests.offer" and initial is not None:result["state"].update(initial)
            return result
        rpc.call=call
        return controls,rpc

    def test_actual_own_log_after_offer_confirms_without_accept_or_synthetic_receipt(self):
        for flags in (0,0x80008):
            controls,rpc=self.auto_quest(flags,initial={"questLogKnown":True,"questLog":[{"questId":2158}]})
            self.assertIn("It's in my quest log",controls.execute("ACCEPT_QUEST",quest_id=2158))
            self.assertFalse(any(method in ("buddy-quests.accept","buddy-quests.state") for method,_ in rpc.calls))
            self.assertNotIn("lastAcceptance",controls.quests)

    def test_autoaccept_delayed_actual_log_is_polled_without_duplicate_accept(self):
        controls,rpc=self.auto_quest(polls=[{"sessionId":"quest-session","questLogKnown":True,"questLog":[]},
            {"sessionId":"quest-session","questLogKnown":True,"questLog":[{"questId":2158}]}])
        with patch.object(converse.time,"sleep"):
            self.assertIn("It's in my quest log",controls.execute("ACCEPT_QUEST",quest_id=2158))
        self.assertEqual(sum(method=="buddy-quests.state" for method,_ in rpc.calls),2)
        self.assertFalse(any(method=="buddy-quests.accept" for method,_ in rpc.calls))
        self.assertNotIn("lastAcceptance",controls.quests)

    def test_autoaccept_unknown_wrong_log_or_replaced_session_never_claims_confirmation(self):
        for state in ({"sessionId":"quest-session","questLogKnown":False,"questLog":[{"questId":2158}]},
                      {"sessionId":"quest-session","questLogKnown":True,"questLog":[{"questId":7}]},
                      {"sessionId":"replacement","questLogKnown":True,"questLog":[{"questId":2158}]}):
            controls,rpc=self.auto_quest(polls=[state]*3)
            with patch.object(converse.time,"sleep"):
                text=controls.execute("ACCEPT_QUEST",quest_id=2158)
            self.assertIn("waiting",text)
            self.assertFalse(any(method=="buddy-quests.accept" for method,_ in rpc.calls))
            self.assertLessEqual(sum(method=="buddy-quests.state" for method,_ in rpc.calls),3)

    def test_shareable_flag_alone_uses_normal_acceptance(self):
        controls,rpc=self.auto_quest(flags=8)
        controls.execute("ACCEPT_QUEST",quest_id=2158)
        self.assertEqual(sum(method=="buddy-quests.accept" for method,_ in rpc.calls),1)

    def test_accept_rpc_error_reconciles_once_only_with_same_session_actual_own_log(self):
        for mode in ("confirmed","replacement","unknown","wrong_quest"):
            controls,rpc=self.auto_quest(flags=0,polls=[{"sessionId":"replacement" if mode=="replacement" else "quest-session",
                "questLogKnown":mode!="unknown","questLog":[{"questId":7 if mode=="wrong_quest" else 2158}]}])
            original=rpc.call
            def call(method,params):
                if method=="buddy-quests.accept":
                    rpc.calls.append((method,params))
                    raise converse.ConversationError("ambiguous write")
                return original(method,params)
            rpc.call=call
            text=controls.execute("ACCEPT_QUEST",quest_id=2158)
            self.assertIn("It's in my quest log" if mode=="confirmed" else "won't retry",text)
            self.assertEqual(sum(method=="buddy-quests.accept" for method,_ in rpc.calls),1)
            self.assertEqual(sum(method=="buddy-quests.state" for method,_ in rpc.calls),1)

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

    def test_rogue_uncertain_attack_stop_is_not_reported_stopped(self):
        class RPC:
            def call(self, method, params):
                return {"autoAttackUnconfirmed": True}
        controls = converse.BuddyControls(RPC(), character_class="rogue")
        controls.actions_available = True
        self.assertIn("waiting for the server", controls.execute("DISABLE_ASSIST"))


class QuestJournalRewardTests(unittest.TestCase):
    def test_goal_context_and_owner_stop_bind_private_exact_identity_and_instance(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(converse, "ROOT", Path(temporary)):
            folder=converse.ensure_state(Path(temporary)/"data/sync-quests")
            characters=[{"name":name,"guid":guid,"phase":"waiting_for_level_2","missingQuestIds":[18,3903],
                         "activeQuestIds":[7],"levelKnown":True,"level":1,"privateToken":"never_forward"}
                        for name,guid in (("Arlen",1),("Mira",4),("Rook",5))]
            record={"identity":converse.QUEST_SYNC_IDENTITY,"instance":"a"*32,"routineActive":True,
                    "phase":"running","characters":characters,"updatedAt":10}
            converse.write_json(folder/"status.json",record)
            with os.fdopen(converse.private_open(folder/"lock",os.O_RDWR|os.O_CREAT),"r+") as lock:
                converse.fcntl.flock(lock,converse.fcntl.LOCK_EX)
                controls=converse.BuddyControls(FakeRPC([]),character_guid=4,character_class="mage")
                state=controls.context()
                self.assertTrue(state["abilities"]["quest_sync_stop"])
                self.assertEqual(state["quest_sync"]["own"]["missingQuestIds"],[18,3903])
                self.assertNotIn("never_forward",json.dumps(state))
                self.assertIn("requested",controls.execute("STOP_QUEST_SYNC"))
                stop=converse.read_json(folder/"stop.json")
                self.assertEqual(stop,{"instance":"a"*32,"identity":converse.QUEST_SYNC_IDENTITY})
                record["identity"]={"wrong":True}
                converse.write_json(folder/"status.json",record)
                self.assertFalse(converse.quest_sync_snapshot(4)["active"])
            self.assertFalse(converse.quest_sync_snapshot(4)["active"])

    def test_private_human_quest_snapshot_is_labeled_and_separate_from_own_journal(self):
        with tempfile.TemporaryDirectory() as temporary:
            path=Path(temporary)/"joel-quest-report.json"
            snapshot={"recordedAt":"2026-10-30T12:00:00Z","source":"Joel screenshot",
                      "quests":[{"questId":7,"title":"Kobold Camp Cleanup","objectives":[{"text":"Kobold Vermin slain","current":0,"required":8}]},
                                {"questId":3903,"title":"Milly Osworth","objectives":[{"text":"Speak to Milly Osworth."}]}]}
            path.write_text(json.dumps(snapshot));path.chmod(0o600)
            controls=converse.BuddyControls(FakeRPC([]),owner_quest_context=path)
            controls.journal_available=True
            controls.journal={"entries":[{"questId":783,"title":"A Threat Within","templateKnown":True}]}
            state=controls.context()
            report=state["owner_reported_quests"]
            self.assertTrue(report["available"])
            self.assertIs(report["live"],False)
            self.assertEqual(report["recordedAt"],snapshot["recordedAt"])
            self.assertEqual(report["quests"],snapshot["quests"])
            self.assertIn("stale",report["progressSemantics"])
            self.assertEqual(state["journal"]["entries"][0]["questId"],783)
            self.assertNotIn(str(path),json.dumps(state))

    def test_invalid_or_missing_quest_report_is_unknown_without_private_data(self):
        with tempfile.TemporaryDirectory() as temporary:
            path=Path(temporary)/"report.json"
            valid={"recordedAt":"2026-10-30T12:00:00Z","source":"Joel screenshot","quests":[]}
            cases=[dict(valid,recordedAt="not-a-time"),dict(valid,recordedAt="2026-10-30T12:00:00"),
                   dict(valid,source="|cprivate-secret"),dict(valid,password="private-secret"),
                   dict(valid,quests=[{"questId":7,"title":"Quest","objectives":[]}]*26),
                   dict(valid,quests=[{"questId":0,"title":"Quest","objectives":[]}]),
                   dict(valid,quests=[{"questId":7,"title":"Quest","objectives":[{"text":"Kill","current":9,"required":8}]}])]
            for value in cases:
                path.write_text(json.dumps(value));path.chmod(0o600)
                result=converse.owner_quest_snapshot(path)
                self.assertIs(result["available"],False)
                self.assertNotIn("private-secret",json.dumps(result))
            path.write_text(json.dumps(valid));path.chmod(0o644)
            self.assertFalse(converse.owner_quest_snapshot(path)["available"])
            path.chmod(0o600)
            link=path.with_name("link");link.symlink_to(path)
            self.assertFalse(converse.owner_quest_snapshot(link)["available"])
            path.write_bytes(b" "*65537)
            self.assertFalse(converse.owner_quest_snapshot(path)["available"])
            path.unlink()
            self.assertFalse(converse.owner_quest_snapshot(path)["available"])
            os.mkfifo(path,0o600)
            self.assertFalse(converse.owner_quest_snapshot(path)["available"])

    def reward_controls(self, choices=(), receipt=None):
        npc="17379390962878382081"
        discovery={"sessionId":"quest-session","revision":"4","questLogKnown":True,"questLogRevision":"7",
                   "questLog":[{"questId":783,"complete":True}],
                   "nearby":[{"canInteract":True,"distanceKnown":True,"distance":2,"creature":{"guid":npc}}]}
        offer={"revision":"9","npcGuid":npc,"questId":783,"title":"A Threat Within",
               "source":"reward_offer","canComplete":True,"expiresInMs":"60000","choices":list(choices)}
        state={"sessionId":"quest-session","revision":"8","turnInOffer":offer}
        result=receipt if receipt is not None else {"questId":783,"status":"REWARD_CONFIRMED","serverRewardObserved":True,"logRemovalConfirmed":True}
        class RPC:
            def __init__(self):self.calls=[]
            def call(self,method,params):
                self.calls.append((method,params))
                if method=="buddy-quests.discover":return discovery
                if method=="buddy-actions.status":return QuestApproachCandidateTests.safe_scene()[0]
                if method=="buddy-world.snapshot":return QuestApproachCandidateTests.safe_scene()[1]
                if method=="buddy-quests.turn_in":return {"transportWritten":True,"serverResponseReceived":True,"state":state}
                if method=="buddy-quests.state":return state
                if method=="buddy-quests.claim_reward":return {"transportWritten":True,"state":dict(state,lastReward=result)}
                return {}
        rpc=RPC();controls=converse.BuddyControls(rpc);controls.quests_available=True
        return controls,rpc,discovery,offer

    def test_zero_and_single_actual_reward_choice_are_bound_and_confirmed(self):
        for choices in ([],[{"index":2,"itemId":1001,"count":1}]):
            controls,rpc,_,_=self.reward_controls(choices)
            text=controls.execute("TURN_IN_QUEST")
            self.assertIn("Turn-in confirmed",text)
            turn=next(p for m,p in rpc.calls if m=="buddy-quests.turn_in")
            self.assertEqual(turn["questLogRevision"],"7")
            self.assertEqual(turn["questId"],783)
            reward=next(p for m,p in rpc.calls if m=="buddy-quests.claim_reward")
            self.assertEqual(reward["offerRevision"],"9")
            self.assertEqual(reward["noChoice"],not choices)
            self.assertEqual(reward["choiceIndex"],2 if choices else 0)
            self.assertEqual(reward["expectedItemId"],1001 if choices else 0)

    def test_explicit_reward_choice_remains_available_while_automatic_details_unknown(self):
        controls,rpc,_,_=self.reward_controls([{"index":0,"itemId":1001},{"index":1,"itemId":1002}])
        text=controls.execute("TURN_IN_QUEST",quest_id=783)
        self.assertIn("waiting",text)
        self.assertNotIn("reward item ID",text)
        self.assertFalse(any(m=="buddy-quests.claim_reward" for m,_ in rpc.calls))
        self.assertIn("isn't",controls.execute("CLAIM_QUEST_REWARD",quest_id=783,item_entry=9999))
        self.assertFalse(any(m=="buddy-quests.claim_reward" for m,_ in rpc.calls))
        self.assertIn("Turn-in confirmed",controls.execute("CLAIM_QUEST_REWARD",quest_id=783,item_entry=1002))
        reward=next(p for m,p in rpc.calls if m=="buddy-quests.claim_reward")
        self.assertEqual((reward["choiceIndex"],reward["expectedItemId"]),(1,1002))

    def test_transport_or_server_reward_without_log_removal_is_unconfirmed(self):
        for receipt in ({"questId":783,"status":"REWARD_REQUESTED"},
                        {"questId":783,"status":"REWARD_CONFIRMED","serverRewardObserved":True},
                        {"questId":784,"status":"REWARD_CONFIRMED","serverRewardObserved":True,"logRemovalConfirmed":True}):
            controls,_,_,_=self.reward_controls(receipt=receipt)
            self.assertIn("haven't confirmed",controls.execute("TURN_IN_QUEST",quest_id=783))

    def test_expired_explicit_reward_reopens_same_quest_and_rebinds_reordered_actual_item(self):
        controls,rpc,discovery,offer=self.reward_controls([
            {"index":0,"itemId":2186},{"index":1,"itemId":2691},{"index":2,"itemId":11192}])
        discovery["questLog"]=[{"questId":21,"complete":True}]
        offer.update(questId=21,title="Skirmish at Echo Ridge")
        offer.pop("expiresInMs")
        original=rpc.call
        def call(method,params):
            if method=="buddy-quests.turn_in":
                offer.update(expiresInMs="60000",revision="12",choices=[
                    {"index":0,"itemId":2691},{"index":4,"itemId":2186},{"index":2,"itemId":11192}])
            result=original(method,params)
            if method=="buddy-quests.claim_reward":
                result["state"]["lastReward"]["questId"]=21
            return result
        rpc.call=call
        self.assertIn("Turn-in confirmed",controls.execute("CLAIM_QUEST_REWARD",quest_id=21,item_entry=2186))
        self.assertEqual([method for method,_ in rpc.calls],["buddy-quests.state","buddy-quests.discover","buddy-quests.turn_in","buddy-quests.claim_reward"])
        claim=rpc.calls[-1][1]
        self.assertEqual((claim["questId"],claim["choiceIndex"],claim["expectedItemId"],claim["offerRevision"]),(21,4,2186,"12"))
        self.assertEqual(rpc.calls[2][1]["npcGuid"],offer["npcGuid"])

    def test_expired_reward_never_refreshes_ambiguous_request_or_unproven_completion(self):
        for mode in ("REWARD_REQUESTED","REWARD_UNCONFIRMED","REWARD_WRITE_UNCONFIRMED","REWARD_CONFIRMED",
                     "requested_offer","incomplete","failed","giver_changed","body_changed"):
            controls,rpc,discovery,offer=self.reward_controls([{ "index":0,"itemId":2186}])
            offer.pop("expiresInMs")
            original=rpc.call
            if mode=="requested_offer":offer["rewardRequested"]=True
            elif mode=="incomplete":discovery["questLog"][0]["complete"]=False
            elif mode=="failed":discovery["questLog"][0]["failed"]=True
            elif mode=="giver_changed":discovery["nearby"][0]["creature"]["guid"]="17379390962878382082"
            elif mode=="body_changed":discovery["sessionId"]="replacement"
            def call(method,params):
                result=original(method,params)
                if mode.startswith("REWARD_") and method in ("buddy-quests.state","buddy-quests.discover"):
                    result=dict(result,lastReward={"questId":783,"status":mode})
                return result
            rpc.call=call
            controls.execute("CLAIM_QUEST_REWARD",quest_id=783,item_entry=2186)
            self.assertFalse(any(method in ("buddy-quests.turn_in","buddy-quests.claim_reward") for method,_ in rpc.calls),mode)

    def test_expired_reward_never_substitutes_an_item_removed_from_new_offer(self):
        controls,rpc,_,offer=self.reward_controls([{ "index":0,"itemId":2186}])
        offer.pop("expiresInMs")
        original=rpc.call
        def call(method,params):
            if method=="buddy-quests.turn_in":offer.update(expiresInMs="60000",revision="12",choices=[{"index":0,"itemId":11192}])
            return original(method,params)
        rpc.call=call
        self.assertIn("isn't",controls.execute("CLAIM_QUEST_REWARD",quest_id=783,item_entry=2186))
        self.assertFalse(any(method=="buddy-quests.claim_reward" for method,_ in rpc.calls))

    def test_delegated_quest21_choice_uses_actual_confirmed_role_and_offered_id(self):
        for role,cls,wanted in (("priest",5,11192),("mage",8,11192),("rogue",4,2186)):
            controls,rpc,discovery,offer=self.reward_controls([
                {"index":1,"itemId":2186},{"index":0,"itemId":2691},{"index":5,"itemId":11192}])
            controls.character_class=role;controls.actions={"classKnown":True,"classId":cls}
            discovery["questLog"]=[{"questId":21,"complete":True}];offer["questId"]=21
            controls.execute("CLAIM_QUEST_REWARD",quest_id=21)
            claim=next(params for method,params in rpc.calls if method=="buddy-quests.claim_reward")
            self.assertEqual(claim["expectedItemId"],wanted)
            self.assertEqual(claim["choiceIndex"],5 if wanted==11192 else 1)

    def lookup_controls(self):
        controls,rpc,discovery,offer=self.reward_controls([
            {"index":4,"itemId":6076,"count":1},{"index":1,"itemId":60,"count":1},{"index":3,"itemId":3070,"count":1}])
        discovery["questLog"]=[{"questId":6,"complete":True}]
        offer.update(questId=6,title="Bounty on Garrick Padfoot")
        controls.character_class="mage"
        rows=[{"itemId":6076,"name":"Tapered Pants","available":True,"itemClass":4,"subclass":2,"inventoryType":7,"armor":25,"allowableClass":0xffffffff,"allowableRace":0xffffffff},
              {"itemId":60,"name":"Layered Tunic","available":True,"itemClass":4,"inventoryType":4,"allowableClass":0xffffffff,"allowableRace":0xffffffff},
              {"itemId":3070,"name":"Ensign Cloak","available":True,"itemClass":4,"subclass":1,"inventoryType":16,"armor":6,"allowableClass":0xffffffff,"allowableRace":0xffffffff}]
        original=rpc.call
        def call(method,params):
            if method=="buddy-world.snapshot":
                rpc.calls.append((method,params))
                return {"self":{"guid":"1","classKnown":True,"classId":8,"levelKnown":True,"level":8}}
            if method=="buddy-trade.lookup_items":
                rpc.calls.append((method,params))
                return {"sessionId":"trade-body","items":rows}
            result=original(method,params)
            if method=="buddy-quests.claim_reward":result["state"]["lastReward"]["questId"]=6
            return result
        rpc.call=call
        return controls,rpc,discovery,offer,rows

    def test_automatic_reward_pending_then_actual_metadata_claims_exact_offer_and_exposes_names(self):
        controls,rpc,_,_,_=self.lookup_controls()
        original=rpc.call
        reads=[]
        def call(method,params):
            result=original(method,params)
            if method=="buddy-trade.lookup_items":
                reads.append(params)
                if len(reads)==1:return {"sessionId":"trade-body","items":[{"itemId":row["itemId"],"pending":True} for row in result["items"]]}
            return result
        rpc.call=call
        with patch.object(converse.time,"sleep"):
            text=controls.execute("CLAIM_QUEST_REWARD",quest_id=6)
        self.assertIn("Turn-in confirmed",text)
        self.assertEqual(len(reads),2)
        claim=next(params for method,params in rpc.calls if method=="buddy-quests.claim_reward")
        self.assertEqual((claim["sessionId"],claim["offerRevision"],claim["questId"],claim["choiceIndex"],claim["expectedItemId"]),
                         ("quest-session","9",6,3,3070))
        self.assertEqual(controls.reward_items["selectedItemId"],3070)
        self.assertIn("Ensign Cloak",str(controls.reward_items))
        self.assertNotIn("ask Joel",converse.PERSONA)
        self.assertNotIn("ask Joel for an actual item ID",converse.PERSONA)

    def test_automatic_reward_unavailable_metadata_has_bounded_reads_and_no_claim(self):
        controls,rpc,_,_,rows=self.lookup_controls()
        rows[2].update(available=False,pending=True)
        with patch.object(converse.time,"sleep"):
            text=controls.execute("CLAIM_QUEST_REWARD",quest_id=6)
        self.assertIn("waiting",text)
        self.assertEqual(sum(method=="buddy-trade.lookup_items" for method,_ in rpc.calls),3)
        self.assertFalse(any(method=="buddy-quests.claim_reward" for method,_ in rpc.calls))

    def test_automatic_reward_lookup_rejects_invalid_metadata_and_changed_body_offer_or_receipt(self):
        for change in ("metadata","lookup_session","quest_session","revision","choices","expired","requested","receipt","foreign_character"):
            with self.subTest(change=change):
                controls,rpc,_,offer,rows=self.lookup_controls()
                original=rpc.call
                def call(method,params):
                    result=original(method,params)
                    if method=="buddy-world.snapshot" and change=="foreign_character":result["self"]["guid"]="4"
                    if method=="buddy-trade.lookup_items":
                        if change=="metadata":rows[2]["armor"]=-1
                        elif change=="lookup_session":result["sessionId"]=""
                        elif change=="revision":offer["revision"]="10"
                        elif change=="choices":offer["choices"]=list(reversed(offer["choices"]))
                        elif change=="expired":offer.pop("expiresInMs")
                        elif change=="requested":offer["rewardRequested"]=True
                    if method=="buddy-quests.state" and any(m=="buddy-trade.lookup_items" for m,_ in rpc.calls):
                        if change=="quest_session":result=dict(result,sessionId="replacement")
                        elif change=="receipt":result=dict(result,lastReward={"questId":6,"status":"REWARD_REQUESTED"})
                    return result
                rpc.call=call
                with patch.object(converse.time,"sleep"):
                    controls.execute("CLAIM_QUEST_REWARD",quest_id=6)
                self.assertFalse(any(method=="buddy-quests.claim_reward" for method,_ in rpc.calls))

    def test_reward_context_sanitizes_actual_item_details_and_honors_explicit_chosen_item(self):
        controls,rpc,_,_,rows=self.lookup_controls()
        rows[2]["secretToken"]="must-not-forward"
        controls.execute("CLAIM_QUEST_REWARD",quest_id=6)
        controls.quests_available=True
        with patch.object(converse,"quest_sync_snapshot",return_value={}),patch.object(converse,"party_quest_sync_snapshot",return_value={}),patch.object(converse,"party_maintenance_snapshot",return_value={}):
            context=controls.context()
        self.assertIn("Ensign Cloak",str(context["reward_items"]))
        self.assertNotIn("secretToken",str(context))
        controls,rpc,_,_,_=self.lookup_controls()
        self.assertIn("Turn-in confirmed",controls.execute("CLAIM_QUEST_REWARD",quest_id=6,item_entry=6076))
        self.assertFalse(any(method=="buddy-trade.lookup_items" for method,_ in rpc.calls))
        claim=next(params for method,params in rpc.calls if method=="buddy-quests.claim_reward")
        self.assertEqual((claim["choiceIndex"],claim["expectedItemId"]),(4,6076))

    def test_zero_quest_id_requires_unique_completed_own_quest(self):
        controls,rpc,discovery,_=self.reward_controls()
        discovery["questLog"].append({"questId":7,"complete":True})
        self.assertIn("unambiguous",controls.execute("TURN_IN_QUEST"))
        self.assertFalse(any(m=="buddy-quests.turn_in" for m,_ in rpc.calls))
        discovery["questLog"]=[{"questId":783,"complete":False}]
        self.assertIn("unambiguous",controls.execute("TURN_IN_QUEST"))

    def test_journal_context_uses_actual_templates_and_planning_has_no_actuator(self):
        journal={"sessionId":"quest-session","questLogRevision":"7","entries":[
            {"questId":783,"templateKnown":True,"title":"A Threat Within","objectives":"Speak to Marshal McBride.","targets":[]},
            {"questId":7,"templateKnown":False,"failure":"awaiting_query_response"}]}
        class RPC:
            def __init__(self):self.calls=[]
            def call(self,method,params):
                self.calls.append((method,params))
                return journal if method=="buddy-quests.journal" else {}
        rpc=RPC();controls=converse.BuddyControls(rpc)
        self.assertEqual(controls.context()["journal"],{"available":False,"entries":[]})
        controls.refresh()
        self.assertEqual(controls.context()["journal"],journal)
        self.assertIn(("buddy-quests.journal",{"refresh":False}),rpc.calls)
        rpc.calls.clear()
        self.assertIsNone(controls.execute("PLAN_QUEST",quest_id=783))
        self.assertIn("haven't received",controls.execute("PLAN_QUEST",quest_id=7))
        self.assertEqual(rpc.calls,[])


class QuestApproachCandidateTests(unittest.TestCase):
    @staticmethod
    def safe_scene():
        return ({"moving": True, "position": {"known": True, "mapKnown": True}},
                {"self": {"guid": "1", "present": True, "mapKnown": True,
                          "aliveKnown": True, "alive": True, "unitFlagsKnown": True,
                          "movementFlagsKnown": True}})

    def candidate(self, **overrides):
        candidate={"distanceKnown":True,"distance":8,"blockedReason":"questgiver_outside_conservative_range",
                   "creature":{"guid":"17379390962878382081","present":True,"mapKnown":True,
                               "positionKnown":True,"aliveKnown":True,"alive":True,"npcFlags":2}}
        candidate.update(overrides)
        return candidate

    def test_range_only_candidate_uses_quest_endpoint_and_prefers_immediate(self):
        candidate=self.candidate()
        class RPC:
            def __init__(self):self.calls=[];self.nearby=[candidate]
            def call(self,method,params):
                self.calls.append((method,params))
                if method=="buddy-quests.discover":return {"sessionId":"quests","revision":"1","nearby":self.nearby}
                if method=="buddy-actions.status":return QuestApproachCandidateTests.safe_scene()[0]
                if method=="buddy-world.snapshot":return QuestApproachCandidateTests.safe_scene()[1]
                if method=="buddy-quests.offer":return {"serverResponseReceived":True,"state":{"offer":{"npcGuid":params["npcGuid"],"quests":[]}}}
                raise AssertionError(method)
        rpc=RPC();controls=converse.BuddyControls(rpc)
        controls.quest_offer()
        self.assertEqual(rpc.calls[-1],("buddy-quests.offer",{"sessionId":"quests","npcGuid":candidate["creature"]["guid"]}))
        rpc.nearby.append({"distanceKnown":True,"distance":3.9,"canInteract":True,"creature":{"guid":"17379390962878382082"}})
        controls.quest_offer()
        self.assertEqual(rpc.calls[-1][1]["npcGuid"],"17379390962878382082")
        self.assertFalse(any(method in ("buddy-actions.acquire_interaction", "buddy-actions.travel_quest") for method,_ in rpc.calls))

    def test_proto_omitted_zero_distance_is_valid_only_with_known_distance(self):
        zero={"distanceKnown":True,"canInteract":True,"creature":{"guid":"17379390962878382081"}}
        self.assertEqual(converse.BuddyControls.quest_candidates([zero]),[(0,0,17379390962878382081)])
        for fields in ({"distanceKnown":False},{"distanceKnown":None},{"distance":None}):
            self.assertEqual(converse.BuddyControls.quest_candidates([dict(zero,**fields)]),[])

    def test_unknown_or_other_blocked_candidates_never_become_approach_targets(self):
        invalid=[]
        for change in ({"distanceKnown":False},{"distance":8.01},{"distance":float("nan")},{"distance":None},
                       {"distance":4},{"canInteract":True},{"blockedReason":"character_motion_or_cast_unconfirmed"},
                       {"blockedReason":"moving_questgiver_position_stale"},{"blockedReason":""}):
            invalid.append(self.candidate(**change))
        for change in ({"guid":"3"},{"present":False},{"positionKnown":False},{"alive":False},{"npcFlags":0},
                       {"mapId":1},{"movementFlags":0x08000000},{"movementFlags":0x200000},
                       {"movementFlags":1,"positionAgeMs":"1501"}):
            candidate=self.candidate()
            candidate["creature"].update(change)
            invalid.append(candidate)
        self.assertEqual(converse.BuddyControls.quest_candidates(invalid),[])

    def test_turn_in_selects_bounded_range_only_observed_npc(self):
        helper=QuestJournalRewardTests()
        controls,rpc,discovery,_=helper.reward_controls()
        discovery["nearby"]=[self.candidate(distance=6.5)]
        self.assertIn("Turn-in confirmed",controls.execute("TURN_IN_QUEST",quest_id=783))
        request=next(params for method,params in rpc.calls if method=="buddy-quests.turn_in")
        self.assertEqual(request["npcGuid"],discovery["nearby"][0]["creature"]["guid"])
        controls,rpc,discovery,_=helper.reward_controls()
        discovery["nearby"]=[self.candidate(blockedReason="character_motion_or_cast_unconfirmed")]
        original=rpc.call
        rpc.call=lambda method,params: {} if method in ("buddy-actions.status","buddy-world.snapshot") else original(method,params)
        self.assertIn("observed questgiver",controls.execute("TURN_IN_QUEST",quest_id=783))
        self.assertFalse(any(method=="buddy-quests.turn_in" for method,_ in rpc.calls))

    def test_ordinary_formation_movement_uses_normal_quest_endpoint_without_outer_lease(self):
        actions, world = self.safe_scene()
        candidate = self.candidate(distance=3, blockedReason="character_motion_or_cast_unconfirmed")
        candidate["creature"]["x"] = 3
        class RPC:
            def __init__(self): self.calls = []
            def call(self, method, params):
                self.calls.append((method, params))
                if method == "buddy-quests.discover":
                    return {"sessionId": "quests", "nearby": [candidate]}
                if method == "buddy-actions.status": return actions
                if method == "buddy-world.snapshot": return world
                if method == "buddy-quests.offer":
                    return {"serverResponseReceived": True, "state": {"offer": {"npcGuid": params["npcGuid"], "quests": []}}}
                raise AssertionError(method)
        rpc = RPC(); controls = converse.BuddyControls(rpc)
        controls.quest_offer()
        self.assertEqual(rpc.calls[-1], ("buddy-quests.offer", {"sessionId": "quests", "npcGuid": candidate["creature"]["guid"]}))
        self.assertFalse(any(m in ("buddy-actions.acquire_interaction", "buddy-actions.travel_quest") for m, _ in rpc.calls))

        controls, rpc, discovery, _ = QuestJournalRewardTests().reward_controls()
        discovery["nearby"] = [candidate]
        original = rpc.call
        def call(method, params):
            if method == "buddy-actions.status": return actions
            if method == "buddy-world.snapshot": return world
            return original(method, params)
        rpc.call = call
        self.assertIn("Turn-in confirmed", controls.execute("TURN_IN_QUEST", quest_id=783))

    def test_nonzero_map_fallback_candidates_require_known_matching_self_actions_and_npc(self):
        for map_id in (1,530):
            for moving in (False,True):
                actions,world=self.safe_scene()
                actions["moving"]=moving
                actions["position"]["mapId"]=world["self"]["mapId"]=map_id
                candidate=self.candidate(distance=6,blockedReason="character_motion_or_cast_unconfirmed" if moving else "questgiver_outside_conservative_range")
                candidate["creature"].update(mapId=map_id,x=6)
                self.assertEqual(converse.BuddyControls.quest_candidates([candidate],actions,world),[(1,6.,17379390962878382081)])
                for mode in ("npc_map","action_map","world_unknown","missing_context"):
                    a,w=copy.deepcopy(actions),copy.deepcopy(world)
                    c=copy.deepcopy(candidate)
                    if mode=="npc_map": c["creature"]["mapId"]=571
                    elif mode=="action_map":a["position"]["mapId"]=571
                    elif mode=="world_unknown":w["self"]["mapKnown"]=False
                    else:a=w=None
                    self.assertEqual(converse.BuddyControls.quest_candidates([c],a,w),[],mode)

    def test_moving_candidate_requires_actual_safe_self_and_current_distance(self):
        candidate = self.candidate(distance=3, blockedReason="character_motion_or_cast_unconfirmed")
        candidate["creature"]["x"] = 3
        for mode in ("stopped", "casting", "motion_unconfirmed", "combat", "foreign_self", "unknown_position", "other_map", "jumping", "now_far"):
            with self.subTest(mode=mode):
                actions, world = self.safe_scene()
                if mode == "stopped": actions["moving"] = False
                elif mode == "casting": actions["castPending"] = True
                elif mode == "motion_unconfirmed": actions["motionUnconfirmed"] = True
                elif mode == "combat": world["self"]["unitFlags"] = 0x80000
                elif mode == "foreign_self": world["self"]["guid"] = "4"
                elif mode == "unknown_position": actions["position"]["known"] = False
                elif mode == "other_map": actions["position"]["mapId"] = world["self"]["mapId"] = 1
                elif mode == "jumping": world["self"]["movementFlags"] = 0x2000
                elif mode == "now_far": actions["position"]["x"] = 20
                self.assertEqual(converse.BuddyControls.quest_candidates([candidate], actions, world), [])


class AreaQuestControlsTests(unittest.TestCase):
    def record(self, folder, guid=1):
        record={"identity":converse.area_quest_identity(guid),"instance":"d"*32,"routineActive":True,
                "own":{"phase":"traveling_to_observed_area_questgiver","activeQuestIds":[7],
                       "acceptedQuestIds":[15],"rewardedQuestIds":[783],"privateToken":"hidden"}}
        converse.write_json(folder/"status.json",record)
        return record

    def test_owned_locked_profile_status_and_cooperative_stop_are_sanitized_and_exact(self):
        with tempfile.TemporaryDirectory() as temporary,patch.object(converse,"ROOT",Path(temporary)):
            base=converse.ensure_state(Path(temporary)/"data/area-quests")
            folder=converse.ensure_state(base/"1")
            record=self.record(folder)
            with os.fdopen(converse.private_open(folder/"lock",os.O_RDWR|os.O_CREAT),"r+") as lock:
                converse.fcntl.flock(lock,converse.fcntl.LOCK_EX)
                snapshot=converse.area_quest_snapshot(1)
                self.assertTrue(snapshot["active"])
                self.assertEqual(snapshot["own"]["acceptedQuestIds"],[15])
                self.assertNotIn("privateToken",str(snapshot))
                controls=converse.BuddyControls(FakeRPC([]))
                self.assertIn("requested",controls.execute("STOP_AREA_QUESTS"))
                self.assertEqual(converse.read_json(folder/"stop.json"),{"identity":record["identity"],"instance":record["instance"]})
                controls.actions_available=True
                controls.execute("STOP_FOLLOWING")
                self.assertEqual(converse.read_json(folder/"stop.json"),{"identity":record["identity"],"instance":record["instance"],"restoreFollowing":False})
                record["identity"]=converse.area_quest_identity(4)
                converse.write_json(folder/"status.json",record)
                self.assertFalse(converse.area_quest_snapshot(1)["active"])

    def test_start_uses_fixed_actual_character_profile_and_never_reconnects_actor(self):
        class RPC:
            url="http://127.0.0.1:8087/rpc"
            def call(self,method,params):
                return {"self":{"guid":"4"}}
        with tempfile.TemporaryDirectory() as temporary,patch.object(converse,"ROOT",Path(temporary)),\
                patch.object(converse.subprocess,"Popen") as launch:
            controls=converse.BuddyControls(RPC(),character_class="mage",character_guid=4)
            controls.actions_available=controls.quests_available=True
            self.assertIn("starting",controls.execute("AREA_QUESTS"))
            args,kwargs=launch.call_args
            self.assertEqual(args[0],[converse.sys.executable,"-B",str(Path(temporary)/"dev/area-quests"),"run","--character-guid","4"])
            self.assertTrue(kwargs["start_new_session"])
            self.assertEqual(kwargs["stdin"],converse.subprocess.DEVNULL)
            controls.rpc.url="http://127.0.0.1:8088/rpc"
            self.assertIn("doesn't match",controls.execute("AREA_QUESTS"))
            self.assertEqual(launch.call_count,1)

    def test_active_sweep_is_not_duplicated_and_new_actions_are_in_schema(self):
        controls=converse.BuddyControls(FakeRPC([]))
        controls.actions_available=controls.quests_available=True
        with patch.object(converse,"area_quest_snapshot",return_value={"active":True}),patch.object(converse.subprocess,"Popen") as launch:
            self.assertIn("already running",controls.execute("AREA_QUESTS"))
            launch.assert_not_called()
        self.assertIn("AREA_QUESTS",converse.REPLY_SCHEMA["properties"]["action"]["enum"])
        self.assertIn("STOP_AREA_QUESTS",converse.REPLY_SCHEMA["properties"]["action"]["enum"])

    def test_nixwick_sweep_uses_own_profile_and_rejects_wrong_character_or_port(self):
        class RPC:
            url="http://127.0.0.1:8402/rpc"
            guid="8"
            def call(self,method,params):
                return {"self":{"guid":self.guid}}
        with tempfile.TemporaryDirectory() as temporary,patch.object(converse,"ROOT",Path(temporary)),\
                patch.object(converse.subprocess,"Popen") as launch:
            rpc=RPC()
            controls=converse.BuddyControls(rpc,character_class="warlock",character_guid=8)
            controls.actions_available=controls.quests_available=True
            self.assertEqual(converse.area_quest_identity(8)["name"],"Nixwick")
            self.assertIn("starting",controls.execute("AREA_QUESTS"))
            self.assertEqual(launch.call_args.args[0][-2:],["--character-guid","8"])
            rpc.guid="1"
            self.assertIn("couldn't confirm",controls.execute("AREA_QUESTS"))
            rpc.url="http://127.0.0.1:8086/rpc"
            self.assertIn("doesn't match",controls.execute("AREA_QUESTS"))
            self.assertEqual(launch.call_count,1)


class OwnerFeedTests(unittest.TestCase):
    def feed(self):
        return {"available":True,"bodySessionId":"chat-session","addonSession":"012345abcdef",
                "revision":5,"ageMs":"1000","quests":[{"questId":15,"state":1}],
                "completedQuestIds":[7],"rewarded":[{"questId":7,"npcGuid":str(0xf1300000c500022b),"ageMs":"2000"}]}

    def test_fresh_feed_is_separate_positive_owner_report_and_ages_events(self):
        feed=self.feed()
        feed["accepted"]=[{"questId":15,"npcGuid":str(0xf130000337000210),"ageMs":"900001"}]
        clean=converse.validated_owner_feed(feed)
        self.assertEqual(clean["quests"],[{"questId":15,"state":1}])
        self.assertEqual(clean["completedQuestIds"],[7])
        self.assertEqual(clean["accepted"],[])
        self.assertEqual(clean["rewarded"][0]["ageMs"],2000)
        self.assertIn("client addon",clean["source"])
        self.assertNotIn("path",clean)

    def test_feed_fails_closed_on_stale_partial_duplicate_and_untrusted_shapes(self):
        alterations=[{"available":False},{"ageMs":"15001"},{"assembling":True},
            {"addonSession":"not-a-session"},{"revision":0},
            {"quests":[{"questId":7,"state":0},{"questId":7,"state":1}]},
            {"quests":[{"questId":7,"state":3}]},{"completedQuestIds":[7,7]},
            {"completedQuestIds":list(range(1,66))},
            {"rewarded":[{"questId":7,"npcGuid":"3","ageMs":0}]}]
        for change in alterations:
            with self.subTest(change=change),self.assertRaises(converse.ConversationError):
                converse.validated_owner_feed(dict(self.feed(),**change))

    def test_party_requires_current_exact_online_owner_and_self_without_duplicates(self):
        group={"known":True,"inGroup":True,"members":[
            {"guid":"4","self":True,"online":True,"name":"Mira"},
            {"guid":"3","online":True,"name":"Joel"}]}
        self.assertTrue(converse.owner_party_known(group,4))
        for key,value in (("name","Someone"),("online",False)):
            bad=json.loads(json.dumps(group));bad["members"][1][key]=value
            self.assertFalse(converse.owner_party_known(bad,4))
        self.assertFalse(converse.owner_party_known(dict(group,members=group["members"]*2),4))
        self.assertFalse(converse.owner_party_known(group,1))

    def test_brain_receives_live_feed_only_with_verified_party_membership(self):
        feed=self.feed()
        class RPC:
            def __init__(self):self.calls=[]
            def call(self,method,params):
                self.calls.append(method)
                return feed if method=="buddy-chat.owner_feed" else {}
        rpc=RPC();controls=converse.BuddyControls(rpc)
        self.assertFalse(controls.context()["owner_shared_quests"]["available"])
        self.assertNotIn("buddy-chat.owner_feed",rpc.calls)
        controls.party_available=True
        controls.party={"group":{"known":True,"inGroup":True,"members":[
            {"guid":"1","self":True,"online":True},{"guid":"3","name":"Joel","online":True}]}}
        self.assertEqual(controls.context()["owner_shared_quests"]["quests"],[{"questId":15,"state":1}])
        feed["ageMs"]="15001"
        self.assertFalse(controls.context()["owner_shared_quests"]["available"])

    def test_standing_sync_context_and_stop_use_owned_fixed_instance(self):
        with tempfile.TemporaryDirectory() as temporary,patch.object(converse,"ROOT",Path(temporary)):
            folder=converse.ensure_state(Path(temporary)/"data/party-quests")
            record={"identity":converse.PARTY_QUEST_IDENTITY,"instance":"b"*32,"routineActive":True,
                "phase":"running","characters":[{"name":name,"guid":guid,"phase":"waiting_for_reward_choice",
                    "activeQuestIds":[18],"ownerActiveQuestIds":[15],"confirmedRewardQuestIds":list(range(1,33)),
                    "privateToken":"never_forward"} for name,guid in (("Arlen",1),("Mira",4),("Rook",5))]}
            converse.write_json(folder/"status.json",record)
            with os.fdopen(converse.private_open(folder/"lock",os.O_RDWR|os.O_CREAT),"r+") as lock:
                converse.fcntl.flock(lock,converse.fcntl.LOCK_EX)
                controls=converse.BuddyControls(FakeRPC([]))
                context=controls.context()
                self.assertTrue(context["party_quest_sync"]["active"])
                self.assertTrue(context["abilities"]["quest_sync_stop"])
                self.assertEqual(context["party_quest_sync"]["own"]["phase"],"waiting_for_reward_choice")
                self.assertNotIn("never_forward",json.dumps(context))
                self.assertIn("requested",controls.execute("STOP_QUEST_SYNC"))
                self.assertEqual(converse.read_json(folder/"stop.json"),{"identity":converse.PARTY_QUEST_IDENTITY,"instance":"b"*32})
            self.assertFalse(converse.party_quest_sync_snapshot(1)["active"])


class PartyMaintenanceContextTests(unittest.TestCase):
    def fixture(self, root):
        folder=converse.ensure_state(root/"data/party-maintenance")
        record={"identity":converse.PARTY_MAINTENANCE_IDENTITY,"phase":"running","instance":"d"*32,
            "updatedAt":time.time(),"characters":[{"name":name,"guid":guid,"phase":"waiting_for_training_confirmation",
                "privateToken":"never_forward"} for name,guid in (("Arlen",1),("Mira",4),("Rook",5))]}
        own={"identity":{"name":"Arlen","guid":1,"ownerGuid":3,"budgetCopper":10000},
             "reservedCopper":300,"training":[{"spell":585,"cost":100,"status":"confirmed","session":"private_session"},
                {"spell":2050,"cost":200,"status":"reserved"}],
             "mail":{"sessionId":"private_mail_session","listRevision":"1","mailboxGuid":str(0xf110022b0e000001),
                "mailId":77,"status":"reserved","privateToken":"never_forward"}}
        converse.write_json(folder/"status.json",record);converse.write_json(folder/"character-1.json",own)
        return folder,record,own

    def test_owned_locked_routine_context_contains_only_sanitized_confirmed_facts(self):
        with tempfile.TemporaryDirectory() as temp,patch.object(converse,"ROOT",Path(temp)):
            folder,_,own=self.fixture(Path(temp))
            with os.fdopen(converse.private_open(folder/"lock",os.O_RDWR|os.O_CREAT),"r+") as lock:
                converse.fcntl.flock(lock,converse.fcntl.LOCK_EX)
                rpc=FakeRPC([]);state=converse.BuddyControls(rpc).context()["party_maintenance"]
                self.assertTrue(state["active"]);self.assertTrue(state["observationsCurrent"])
                self.assertEqual(state["own"],{"name":"Arlen","guid":1,"phase":"waiting_for_training_confirmation",
                    "recordsKnown":True,"reservedCopper":300,"confirmedSpellIds":[585],"stipendStatus":"reserved_unconfirmed"})
                self.assertNotIn("private",json.dumps(state));self.assertNotIn("never_forward",json.dumps(state))
                self.assertEqual(rpc.calls,[("buddy-chat.emotes",{}),("buddy-world.snapshot",{})])
                own["mail"]["status"]="confirmed";converse.write_json(folder/"character-1.json",own)
                self.assertEqual(converse.party_maintenance_snapshot(1)["own"]["stipendStatus"],"confirmed")
            self.assertFalse(converse.party_maintenance_snapshot(1)["active"])

    def test_missing_character_record_never_invents_budget_or_training(self):
        with tempfile.TemporaryDirectory() as temp,patch.object(converse,"ROOT",Path(temp)):
            folder,_,_=self.fixture(Path(temp));(folder/"character-1.json").unlink()
            with os.fdopen(converse.private_open(folder/"lock",os.O_RDWR|os.O_CREAT),"r+") as lock:
                converse.fcntl.flock(lock,converse.fcntl.LOCK_EX)
                state=converse.party_maintenance_snapshot(1)
                self.assertTrue(state["active"])
                self.assertFalse(state["own"]["recordsKnown"])
                self.assertIsNone(state["own"]["reservedCopper"])
                self.assertEqual(state["own"]["confirmedSpellIds"],[])
                self.assertEqual(state["own"]["stipendStatus"],"unrecorded")

    def test_wrong_identity_budget_and_nonprivate_or_symlinked_records_fail_closed(self):
        for mode in ("identity","character","budget","cost","spell","public","symlink","oversize","roster"):
            with self.subTest(mode=mode),tempfile.TemporaryDirectory() as temp,patch.object(converse,"ROOT",Path(temp)):
                folder,record,own=self.fixture(Path(temp))
                with os.fdopen(converse.private_open(folder/"lock",os.O_RDWR|os.O_CREAT),"r+") as lock:
                    converse.fcntl.flock(lock,converse.fcntl.LOCK_EX)
                    if mode=="identity":record["identity"]={"wrong":True}
                    elif mode=="roster":record["characters"][1]["guid"]=1
                    elif mode=="character":own["identity"]["guid"]=4
                    elif mode=="budget":own["reservedCopper"]=10001
                    elif mode=="cost":own["training"][0]["cost"]=99
                    elif mode=="spell":own["training"][0]["spell"]=-1
                    converse.write_json(folder/"status.json",record);converse.write_json(folder/"character-1.json",own)
                    if mode=="public":(folder/"character-1.json").chmod(0o644)
                    elif mode=="symlink":
                        (folder/"character-1.json").rename(folder/"original.json")
                        (folder/"character-1.json").symlink_to(folder/"original.json")
                    elif mode=="oversize":(folder/"character-1.json").write_text(" "*65537)
                    self.assertFalse(converse.party_maintenance_snapshot(1)["active"])

    def test_stale_phase_is_labeled_and_stopped_record_is_inactive(self):
        with tempfile.TemporaryDirectory() as temp,patch.object(converse,"ROOT",Path(temp)):
            folder,record,_=self.fixture(Path(temp))
            with os.fdopen(converse.private_open(folder/"lock",os.O_RDWR|os.O_CREAT),"r+") as lock:
                converse.fcntl.flock(lock,converse.fcntl.LOCK_EX)
                record["updatedAt"]=time.time()-120;converse.write_json(folder/"status.json",record)
                state=converse.party_maintenance_snapshot(1)
                self.assertTrue(state["active"]);self.assertFalse(state["observationsCurrent"])
                record["phase"]="stopped";converse.write_json(folder/"status.json",record)
                self.assertFalse(converse.party_maintenance_snapshot(1)["active"])
                self.assertFalse(converse.party_maintenance_snapshot(99)["active"])


    def v2_fixture(self,root):
        folder,record,own=self.fixture(root)
        own.update(identity=dict(version=2,name="Arlen",guid=1,ownerGuid=3),reservedCopper=500000,
            spentCopper=400000,rejections={},training=[
                dict(spell=585,cost=100,status="confirmed",session="private_purchase_body"),
                dict(spell=1243,cost=50000,status="known_at_trainer",knowledgeSession="private_new_body"),
                dict(spell=2050,cost=200,status="reserved")])
        converse.write_json(folder/"character-1.json",own)
        return folder,record,own

    def test_v2_compacted_lifetime_audit_and_trainer_knowledge_are_distinct_private_facts(self):
        with tempfile.TemporaryDirectory() as temp,patch.object(converse,"ROOT",Path(temp)):
            folder,_,_=self.v2_fixture(Path(temp))
            with os.fdopen(converse.private_open(folder/"lock",os.O_RDWR|os.O_CREAT),"r+") as lock:
                converse.fcntl.flock(lock,converse.fcntl.LOCK_EX)
                state=converse.party_maintenance_snapshot(1)
                self.assertTrue(state["active"]);self.assertTrue(state["observationsCurrent"])
                own=state["own"]
                self.assertTrue(own["recordsKnown"])
                self.assertEqual((own["reservedCopper"],own["spentCopper"]),(500000,400000))
                self.assertEqual(own["confirmedSpellIds"],[585])
                self.assertEqual(own["knownAtTrainerSpellIds"],[1243])
                self.assertFalse(own["historyComplete"])
                self.assertNotIn("private",json.dumps(state));self.assertNotIn("never_forward",json.dumps(state))
                self.assertNotIn(2050,own["knownAtTrainerSpellIds"])

    def test_running_owned_v2_with_unknown_record_facts_remains_active_without_partial_claims(self):
        for mode in ("spent","history","status","cost","pending_order","sum","rejections"):
            with self.subTest(mode=mode),tempfile.TemporaryDirectory() as temp,patch.object(converse,"ROOT",Path(temp)):
                folder,_,own=self.v2_fixture(Path(temp))
                if mode=="spent":own["spentCopper"]=500001
                elif mode=="history":own["training"]*=22
                elif mode=="status":own["training"][1]["status"]="future_unknown_status"
                elif mode=="cost":own["training"][1]["cost"]=1000001
                elif mode=="pending_order":own["training"][0]["status"]="reserved"
                elif mode=="sum":own["reservedCopper"]=1
                else:own["rejections"]="unknown"
                converse.write_json(folder/"character-1.json",own)
                with os.fdopen(converse.private_open(folder/"lock",os.O_RDWR|os.O_CREAT),"r+") as lock:
                    converse.fcntl.flock(lock,converse.fcntl.LOCK_EX)
                    state=converse.party_maintenance_snapshot(1)
                    self.assertTrue(state["active"])
                    self.assertFalse(state["own"]["recordsKnown"])
                    self.assertEqual(state["own"]["confirmedSpellIds"],[])
                    self.assertEqual(state["own"]["knownAtTrainerSpellIds"],[])
                    self.assertIsNone(state["own"]["spentCopper"])
                    self.assertIsNone(state["own"]["reservedCopper"])

    def test_v2_larger_private_bound_and_exact_profile_are_preserved(self):
        with tempfile.TemporaryDirectory() as temp,patch.object(converse,"ROOT",Path(temp)):
            folder,_,own=self.v2_fixture(Path(temp));own["privatePadding"]="x"*70000
            converse.write_json(folder/"character-1.json",own)
            with os.fdopen(converse.private_open(folder/"lock",os.O_RDWR|os.O_CREAT),"r+") as lock:
                converse.fcntl.flock(lock,converse.fcntl.LOCK_EX)
                state=converse.party_maintenance_snapshot(1)
                self.assertTrue(state["active"]);self.assertTrue(state["own"]["recordsKnown"])
                self.assertNotIn("privatePadding",json.dumps(state))
                own["identity"]["guid"]=4;converse.write_json(folder/"character-1.json",own)
                self.assertFalse(converse.party_maintenance_snapshot(1)["active"])
                own["identity"]["guid"]=1;own["privatePadding"]="x"*262144
                converse.write_json(folder/"character-1.json",own)
                self.assertFalse(converse.party_maintenance_snapshot(1)["active"])

    def test_v2_journal_cannot_override_stopped_or_unlocked_routine(self):
        with tempfile.TemporaryDirectory() as temp,patch.object(converse,"ROOT",Path(temp)):
            folder,record,_=self.v2_fixture(Path(temp))
            with os.fdopen(converse.private_open(folder/"lock",os.O_RDWR|os.O_CREAT),"r+") as lock:
                converse.fcntl.flock(lock,converse.fcntl.LOCK_EX)
                record["phase"]="stopped";converse.write_json(folder/"status.json",record)
                self.assertFalse(converse.party_maintenance_snapshot(1)["active"])
            record["phase"]="running";converse.write_json(folder/"status.json",record)
            self.assertFalse(converse.party_maintenance_snapshot(1)["active"])

    def harvest_record(self,guid=5):
        now=time.time()
        skill,spell=(393,8613) if guid==5 else (182,2366)
        return dict(guid=guid,skillId=skill,spellId=spell,phase='waiting_for_harvest',reason='combat_or_healing',
            busy=False,updatedAt=now,bodySessionId='actual_body',privateToken='never_forward',
            lastReceipt=dict(status='COMPLETED',bodySessionId='actual_body',targetGuid=str((0xf130 if guid==5 else 0xf110)<<48|30<<24|17),
                targetEntry=30,ownGoObserved=True,lootWindowObserved=True,lootReleaseObserved=True,
                outputInventoryObserved=True,ownLootedItemsObserved=2,lootSlotsRemoved=1,
                leaseReleased=True,baselineInventoryRevision='8',observedInventoryRevision='9',
                beforeRank=1,afterRank=2,skillIncreased=True,observedAt=now-5,privateToken='never_forward'))

    def test_harvest_context_reads_only_owned_locked_fixed_role_status_and_confirmed_material_proof(self):
        with tempfile.TemporaryDirectory() as temp,patch.object(converse,'ROOT',Path(temp)):
            folder,record,_=self.fixture(Path(temp))
            next(row for row in record['characters'] if row['guid']==5)['harvest']=self.harvest_record()
            converse.write_json(folder/'status.json',record)
            with os.fdopen(converse.private_open(folder/'lock',os.O_RDWR|os.O_CREAT),'r+') as lock:
                converse.fcntl.flock(lock,converse.fcntl.LOCK_EX)
                state=converse.party_maintenance_snapshot(5,'actual_body')
                harvest=state['own']['harvest']
                self.assertTrue(harvest['configured']);self.assertTrue(harvest['observationsCurrent'])
                self.assertEqual((harvest['skillId'],harvest['spellId']),(393,8613))
                self.assertIn('party member',harvest['reasonText'])
                self.assertEqual(harvest['lastReceipt']['status'],'COMPLETED')
                self.assertTrue(harvest['lastReceipt']['skillIncreased'])
                self.assertNotIn('private',json.dumps(harvest));self.assertNotIn('never_forward',json.dumps(harvest))
                self.assertNotIn('bodySessionId',json.dumps(harvest))
                self.assertFalse(converse.party_maintenance_snapshot(5,'replacement_body')['own']['harvest']['observationsCurrent'])
                self.assertFalse(converse.party_maintenance_snapshot(5)['own']['harvest']['observationsCurrent'])
                controls=converse.BuddyControls(FakeRPC([]),character_guid=5,character_class='rogue')
                self.assertFalse(controls.quests_available)
                self.assertFalse(controls.context()['party_maintenance']['own']['harvest']['observationsCurrent'])
                record['phase']='stopped';converse.write_json(folder/'status.json',record)
                self.assertFalse(converse.party_maintenance_snapshot(5)['active'])
            self.assertFalse(converse.party_maintenance_snapshot(5)['active'])

    def test_harvest_snapshot_rejects_unknown_identity_and_never_promotes_missing_receipt_proof(self):
        for mode in ('role','skill','spell','phase','reason','phase_shape','reason_shape','time','busy'):
            row=self.harvest_record()
            if mode=='role':row['guid']=1
            elif mode=='skill':row['skillId']=333
            elif mode=='spell':row['spellId']=13262
            elif mode=='phase':row['phase']='run private command'
            elif mode=='reason':row['reason']='arbitrary_private_error'
            elif mode=='phase_shape':row['phase']={}
            elif mode=='reason_shape':row['reason']=[]
            elif mode=='time':row['updatedAt']=float('nan')
            else:row['busy']='false'
            self.assertEqual(converse.harvest_snapshot(row,5),{'configured':False},mode)
        self.assertEqual(converse.harvest_snapshot(self.harvest_record(),4),{'configured':False})
        for mode in ('pending','foreign_body','foreign_kind','no_inventory','no_release','no_output','no_slots','lease','cleanup','old_inventory'):
            row=self.harvest_record();receipt=row['lastReceipt']
            if mode=='pending':receipt['status']='REQUESTED'
            elif mode=='foreign_body':receipt['bodySessionId']='old_body'
            elif mode=='foreign_kind':receipt['targetGuid']=str(0xf110001e000011)
            elif mode=='no_inventory':receipt['outputInventoryObserved']=False
            elif mode=='no_release':receipt['lootReleaseObserved']=False
            elif mode=='no_output':receipt['ownLootedItemsObserved']=0
            elif mode=='lease':receipt['leaseReleased']=False
            elif mode=='cleanup':receipt['cleanupUnconfirmed']=True
            elif mode=='old_inventory':receipt['observedInventoryRevision']='8'
            else:receipt['lootSlotsRemoved']=0
            self.assertNotIn('lastReceipt',converse.harvest_snapshot(row,5),mode)
        row=self.harvest_record();row['updatedAt']-=120;row['lastReceipt']['observedAt']-=120
        self.assertFalse(converse.harvest_snapshot(row,5)['observationsCurrent'])
        self.assertTrue(converse.harvest_snapshot(self.harvest_record(1),1)['configured'])
        self.assertNotIn('SKIN',converse.ACTIONS)
        with self.assertRaises(converse.ConversationError):
            converse.RPC('http://127.0.0.1:8088/rpc').call('buddy-professions.attempt',{})


class TradeIdleProofTests(unittest.TestCase):
    def completed(self):
        return json.loads('{"phase":"COMPLETE","partnerGuid":"3","partnerAccepted":true,"acceptTransportWritten":true,"partnerItems":[{"entry":2962,"count":1}],"ownRequestedItems":[{"entry":7070,"count":1}],"ownRequestedCopper":7,"partnerCopper":9,"lastServerStatus":8,"reason":"server_confirmed_trade_complete","completedTrades":"1","junkDisposalEnabled":true}')
    def test_exact_server_completion_allows_retained_historical_offers(self):
        self.assertTrue(converse.trade_idle(self.completed()))
        self.assertTrue(converse.trade_idle(dict(phase='IDLE',junkDisposalEnabled=True)))
        for field,value in (('partnerGuid','4'),('lastServerStatus',7),('reason','unexpected_completion_status'),('completedTrades','0')):
            with self.subTest(field=field):
                state=self.completed();state[field]=value;self.assertFalse(converse.trade_idle(state))
    def test_unknown_closed_accept_or_junk_states_are_never_inferred_idle(self):
        for phase in ('CLOSED','CANCELLED','CANCEL_SENT','CANCEL_CONFIRMED','COMPLETED','ACCEPT_SENT','OPEN',''):
            state=self.completed();state['phase']=phase;self.assertFalse(converse.trade_idle(state),phase)
        for status in ('AWAITING_SERVER_SLOT_UPDATE','TRANSPORT_UNCONFIRMED_NO_RETRY','UNKNOWN',''):
            state=self.completed();state['lastJunkDisposal']={'status':status};self.assertFalse(converse.trade_idle(state))
        for extra in (dict(partnerGuid='3'),dict(acceptTransportWritten=True),dict(partnerAccepted=True),
                dict(partnerItems=[{'entry':2962}]),dict(ownRequestedCopper=1),dict(partnerItems={})):
            self.assertFalse(converse.trade_idle(dict(phase='IDLE',**extra)))
        state=self.completed();state['lastJunkDisposal']={'status':'SERVER_SLOT_REMOVAL_CONFIRMED'}
        self.assertTrue(converse.trade_idle(state))


class GearContextTests(unittest.TestCase):
    def test_nix_town_snapshot_binds_8402_body_and_revokes_only_matching_held_controls(self):
        with tempfile.TemporaryDirectory() as temp,patch.object(converse,'ROOT',Path(temp)):
            folder=converse.ensure_state(converse.ensure_state(Path(temp)/'data/party-town')/'8')
            identity=dict(version=1,mode='party_town',characterGuid=8,rpcUrl='http://127.0.0.1:8402/rpc')
            queue=['combat_or_recovery','existing_game_operation','usable_gear','bag_space_or_service','idle_gathering_quests_or_training']
            record=dict(identity=copy.deepcopy(identity),instance='a'*32,activityId='b'*32,routineActive=True,
                own=dict(busy=True,bodySessionId='aw-nix-current',observationsCurrent=True,taskQueue=queue,selectedTask='bags'))
            with os.fdopen(converse.private_open(folder/'lock',os.O_RDWR|os.O_CREAT),'r+') as lock:
                converse.fcntl.flock(lock,converse.fcntl.LOCK_EX);converse.write_json(folder/'status.json',record)
                self.assertTrue(converse.party_town_snapshot(8,body_session='aw-nix-current')['observationsCurrent'])
                self.assertFalse(converse.party_town_snapshot(8,body_session='aw-old')['observationsCurrent'])
                for control in ('follow','buffs'):
                    result=converse.party_town_snapshot(8,revoke_control=control)
                    self.assertTrue(result['busy']);self.assertTrue(result['followRevoked'])
                marker=converse.read_json(folder/'follow-revoked.json')
                self.assertEqual(marker,dict(identity=identity,instance='a'*32,activityId='b'*32,controls=['buffs','follow']))
                record['activityId']='c'*32;converse.write_json(folder/'status.json',record)
                converse.party_town_snapshot(8,revoke_control='assist')
                self.assertEqual(converse.read_json(folder/'follow-revoked.json')['controls'],['assist'])
                record['identity']['rpcUrl']='http://127.0.0.1:8088/rpc';converse.write_json(folder/'status.json',record)
                self.assertEqual(converse.party_town_snapshot(8,revoke_control='loot'),dict(active=False,busy=False))
                self.assertEqual(converse.read_json(folder/'follow-revoked.json')['controls'],['assist'])
            record['identity']=identity;converse.write_json(folder/'status.json',record)
            self.assertFalse(converse.party_town_snapshot(8)['active'])

    def test_inventory_comparison_context_requires_exact_actual_carried_item(self):
        item_guid=str(0x4000000000000001)
        slots=[dict(bag=0,slot=i) for i in range(16)]
        slots[0].update(itemGuid=item_guid,entry=1158,stackCount=1,metadataKnown=True)
        bags=[dict(bag=0,slotCount=16,slotKnown=True,contentsKnown=True,slots=slots)]
        bags += [dict(bag=i,slotCount=0,slotKnown=True,contentsKnown=True,slots=[]) for i in range(1,5)]
        candidate=dict(item=dict(bag=0,slot=0,itemGuid=item_guid,entry=1158,stackCount=1),metadataKnown=True,
            name='Solid Metal Club',gearEquipEligible=True,conservativeUpgrade=True)
        state=dict(selfGuid='1',sessionId='aw-body',inventoryKnown=True,inventoryBags=bags,equipmentKnown=True,items=[candidate])
        result=converse.inventory_context(state,1)
        self.assertEqual(result['gear']['candidates'],[dict(entry=1158,name='Solid Metal Club',gearEquipEligible=True,conservativeUpgrade=True)])
        candidate['item']['entry']=999
        self.assertEqual(converse.inventory_context(state,1)['gear']['candidates'],[])
        candidate['item']['entry']=1158;candidate['metadataKnown']=False
        self.assertEqual(converse.inventory_context(state,1)['gear']['candidates'],[])

    def test_town_queue_is_bounded_and_current_body_is_explicit(self):
        with tempfile.TemporaryDirectory() as temp,patch.object(converse,'ROOT',Path(temp)):
            folder=converse.ensure_state(converse.ensure_state(Path(temp)/'data/party-town')/'1')
            queue=['combat_or_recovery','existing_game_operation','usable_gear','bag_space_or_service','idle_gathering_quests_or_training']
            record=dict(identity=dict(version=1,mode='party_town',characterGuid=1,rpcUrl='http://127.0.0.1:8086/rpc'),
                instance='a'*32,routineActive=True,own=dict(bodySessionId='aw-body',observationsCurrent=True,taskQueue=queue,selectedTask='gear',gearCandidateCount=2,blockedReason='gear_waits_for_safe_party'))
            with os.fdopen(converse.private_open(folder/'lock',os.O_RDWR|os.O_CREAT),'r+') as lock:
                converse.fcntl.flock(lock,converse.fcntl.LOCK_EX);converse.write_json(folder/'status.json',record)
                actual=converse.party_town_snapshot(1,body_session='aw-body')
                self.assertEqual(actual['taskQueue'],queue);self.assertEqual(actual['selectedTask'],'gear')
                self.assertTrue(actual['observationsCurrent'])
                self.assertFalse(converse.party_town_snapshot(1,body_session='aw-other')['observationsCurrent'])
                record['own']['observationsCurrent']=False;converse.write_json(folder/'status.json',record)
                self.assertFalse(converse.party_town_snapshot(1,body_session='aw-body')['observationsCurrent'])
                record['own']['taskQueue']=['run arbitrary command'];converse.write_json(folder/'status.json',record)
                self.assertNotIn('taskQueue',converse.party_town_snapshot(1,body_session='aw-body'))

    def test_disenchant_context_requires_current_body_and_full_material_receipt(self):
        with tempfile.TemporaryDirectory() as temp,patch.object(converse,'ROOT',Path(temp)):
            folder=converse.ensure_state(converse.ensure_state(Path(temp)/'data/party-town')/'4')
            receipt=dict(entry=100,name='Old cloth',bodySessionId='actual_body',status='COMPLETED',
                ownGoObserved=True,inputConsumedObserved=True,outputInventoryObserved=True,lootReleaseObserved=True,
                leaseReleased=True,baselineInventoryRevision='3',observedInventoryRevision='4',privateToken='secret')
            record=dict(identity=dict(version=1,mode='party_town',characterGuid=4,rpcUrl='http://127.0.0.1:8087/rpc'),
                instance='a'*32,routineActive=True,own=dict(bodySessionId='actual_body',disenchantConfigured=True,
                disenchantPhase='completed',disenchantedItems=[receipt]))
            with os.fdopen(converse.private_open(folder/'lock',os.O_RDWR|os.O_CREAT),'r+') as lock:
                converse.fcntl.flock(lock,converse.fcntl.LOCK_EX)
                converse.write_json(folder/'status.json',record)
                result=converse.party_town_snapshot(4,body_session='actual_body')['disenchant']
                self.assertTrue(result['observationsCurrent']);self.assertEqual(result['lastConfirmedItem']['entry'],100)
                self.assertNotIn('private',json.dumps(result));self.assertNotIn('secret',json.dumps(result))
                self.assertNotIn('lastConfirmedItem',converse.party_town_snapshot(4,body_session='new_body')['disenchant'])
                receipt['cleanupUnconfirmed']=True;converse.write_json(folder/'status.json',record)
                self.assertNotIn('lastConfirmedItem',converse.party_town_snapshot(4,body_session='actual_body')['disenchant'])
                receipt['cleanupUnconfirmed']=False;receipt['outputInventoryObserved']=False;converse.write_json(folder/'status.json',record)
                self.assertNotIn('lastConfirmedItem',converse.party_town_snapshot(4,body_session='actual_body')['disenchant'])

    def test_only_bound_confirmed_gear_facts_are_exposed_from_owned_town_status(self):
        with tempfile.TemporaryDirectory() as temp,patch.object(converse,'ROOT',Path(temp)):
            folder=converse.ensure_state(converse.ensure_state(Path(temp)/'data/party-town')/'5')
            identity=dict(version=1,mode='party_town',characterGuid=5,rpcUrl='http://127.0.0.1:8088/rpc')
            receipt=dict(entry=2962,equipmentSlot=6,status='CONFIRMED',bodySessionId='actual_body',name='Burnt Leather Breeches',
                inventoryConfirmed=True,equipmentConfirmed=True,leaseReleased=True,displacedRetained=True,
                baselineInventoryRevision='3',observedInventoryRevision='4',baselineEquipmentRevision='7',observedEquipmentRevision='8',
                privateToken='never_forward')
            own=dict(lastKind='gear',phase='town_upkeep_complete',busy=False,bodySessionId='actual_body',confirmedOperations=1,equippedItems=[receipt])
            record=dict(identity=identity,instance='a'*32,routineActive=True,own=own)
            with os.fdopen(converse.private_open(folder/'lock',os.O_RDWR|os.O_CREAT),'r+') as lock:
                converse.fcntl.flock(lock,converse.fcntl.LOCK_EX)
                converse.write_json(folder/'status.json',record)
                result=converse.party_town_snapshot(5)
                self.assertEqual(result['kind'],'gear');self.assertEqual(result['equippedItems'][0]['name'],'Burnt Leather Breeches')
                self.assertNotIn('private',json.dumps(result));self.assertNotIn('bodySessionId',json.dumps(result))
                for key in ('inventoryConfirmed','equipmentConfirmed','leaseReleased','displacedRetained'):
                    receipt[key]=False;converse.write_json(folder/'status.json',record)
                    self.assertNotIn('equippedItems',converse.party_town_snapshot(5));receipt[key]=True
                receipt['bodySessionId']='old_body';converse.write_json(folder/'status.json',record)
                self.assertNotIn('equippedItems',converse.party_town_snapshot(5))
        self.assertNotIn('EQUIP_GEAR',converse.ACTIONS)


class BoundaryTests(unittest.TestCase):
    def test_new_buff_and_profession_reads_pass_real_rpc_allowlist(self):
        rpc = converse.RPC('http://127.0.0.1:8086/rpc')
        import io
        methods = ('buddy-actions.buffs', 'buddy-actions.spell_toolkit', 'buddy-professions.state')
        for method in methods:
            with patch.object(rpc.opener, 'open', return_value=io.BytesIO(json.dumps({
                    'jsonrpc': '2.0', 'id': rpc.request_id+1, 'result': {}}).encode())) as transport:
                rpc.call(method, {'enabled': True} if method.endswith('.buffs') else {})
                self.assertEqual(json.loads(transport.call_args.args[0].data)['method'], method)
        for method in ('buddy-actions.cast', 'buddy-professions.attempt', 'session.logout'):
            with self.assertRaises(converse.ConversationError): rpc.call(method, {})

    def test_spell_context_is_bound_unknown_preserving_and_buff_control_is_fixed(self):
        raw = {'sessionId': 'aw-body', 'selfGuid': '4', 'spellbookKnown': True,
               'spellbookRevision': '2', 'spells': [
                   {'spellId': 7300, 'metadataKnown': True, 'name': 'Frost Armor', 'rank': 2, 'automaticBuffSupported': True},
                   {'spellId': 999, 'metadataKnown': False, 'name': 'invented', 'automaticBuffSupported': True}],
               'lastBuff': {'phase': 'waiting_aura', 'spellId': 7300, 'targetGuid': '4', 'serverCastGo': True}}
        actions = {'sessionId': 'aw-body', 'selfGuid': '4'}
        context = converse.spell_toolkit_context(raw, actions, 4)
        self.assertTrue(context['available'])
        self.assertNotIn('name', next(r for r in context['spells'] if r['spellId']==999))
        self.assertFalse(context['lastBuff']['auraConfirmed'])
        self.assertEqual(converse.spell_toolkit_context(raw, dict(actions,sessionId='new-body'),4), {'available':False})
        class BuffRPC:
            def __init__(self): self.calls=[]
            def call(self,method,params):
                self.calls.append((method,params));return {'buffing':params['enabled']}
        rpc=BuffRPC();controls=converse.BuddyControls(rpc,character_class='mage',character_guid=4)
        controls.actions_available=True
        self.assertIn('supported learned buff', controls.execute('ENABLE_BUFFS'))
        self.assertEqual(rpc.calls,[])
        controls.spell_toolkit=context
        controls.execute('ENABLE_BUFFS');controls.execute('DISABLE_BUFFS')
        self.assertEqual(rpc.calls,[('buddy-actions.buffs',{'enabled':True}),('buddy-actions.buffs',{'enabled':False})])

    def test_profession_context_does_not_mix_bodies_or_promote_unknown_skills(self):
        raw={'sessionId':'aw-body','selfGuid':'4','skillsKnown':False,
             'skills':[{'skillId':333,'rank':1,'maximum':75}],
             'spellsKnown':True,'recipes':[{'spellId':13262,'skillId':333,'known':True}]}
        self.assertEqual(converse.professions_context(raw,4,'another-body'),{'available':False})
        context=converse.professions_context(raw,4,'aw-body')
        self.assertFalse(context['skillsKnown']);self.assertEqual(context['skills'],[])
        self.assertTrue(context['recipes'][0]['known'])

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
        self.assertNotIn("--add-dir",command)

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


class NixSharedQuestTests(unittest.TestCase):
    def rig(self, **kwargs):
        state = dict(sessionId="aw-current-nix", sharedQuestsEnabled=True, questLogKnown=True,
            questLogRevision="7", questLog=[dict(slot=i, questId=218 if i == 0 else 2160 if i == 1 else 0) for i in range(25)],
            pendingShare=dict(sequence="1", sharerGuid="3", kind="SHARED_DETAILS", expiresInMs="30000",
                quest=dict(questId=9, title="The Killing Fields", available=True)))
        party = dict(sessionId="aw-current-nix", group=dict(known=True, inGroup=True, leaderGuid="3",
            members=[dict(guid=str(g), self=g == 8, online=True, name="Joel" if g == 3 else "Nixwick" if g == 8 else "Peer") for g in (1,3,4,5,8)]))
        class RPC:
            def __init__(self): self.calls=[]; self.state=state; self.party=party; self.timeout=False; self.tail=None; self.result_change=None
            def call(self, method, params):
                self.calls.append((method, copy.deepcopy(params)))
                if method == "buddy-party.state":
                    out = copy.deepcopy(self.party)
                    if self.tail and sum(m == method for m,p in self.calls) % 2 == 0: self.tail(out)
                    return out
                if method == "buddy-quests.state": return copy.deepcopy(self.state)
                if method == "buddy-chat.poll": return poll(sessionId="SeparateChatEpoch123")
                if method == "buddy-quests.accept_shared":
                    if self.timeout: raise converse.ConversationError("Response unavailable")
                    self.state["pendingShare"]["acceptRequested"] = True
                    expired=not int(self.state['pendingShare'].get('expiresInMs',0))
                    self.state["lastSharedAcceptance"] = dict(shareSequence=params['shareSequence'], sharerGuid="3", questId=params['questId'],
                        status="SHARE_EXPIRED" if expired else "REQUESTED", baselineLogRevision="7", transportWritten=True)
                    result=dict(transportWritten=True, state=copy.deepcopy(self.state))
                    if self.result_change:self.result_change(result)
                    return result
                raise AssertionError(method)
        rpc=RPC(); events=[]; brain=FakeBrain()
        loop=converse.Conversation(rpc,brain,character_guid=8,character_name="Nixwick",character_class="warlock",
            status=lambda **fields:events.append(fields),**kwargs)
        return loop,rpc,events,brain

    def test_real_nix_share9_is_accepted_once_before_llm_with_distinct_core_chat_sessions(self):
        loop,rpc,events,brain=self.rig();loop.tick();loop.tick()
        writes=[p for m,p in rpc.calls if m=="buddy-quests.accept_shared"]
        self.assertEqual(writes,[dict(sessionId="aw-current-nix",shareSequence="1",sharerGuid="3",questId=9)])
        self.assertLess(next(i for i,c in enumerate(rpc.calls) if c[0]=="buddy-quests.accept_shared"),
                        next(i for i,c in enumerate(rpc.calls) if c[0]=="buddy-chat.poll"))
        self.assertEqual(loop.session_id,"SeparateChatEpoch123")
        self.assertFalse(any(e.get("sharedQuestPhase")=="confirmed_in_own_quest_log" for e in events))
        rpc.state["questLog"][2]["questId"]=9;rpc.state["questLogRevision"]="8"
        rpc.state["lastSharedAcceptance"].update(status="CONFIRMED_IN_LOG",confirmedLogRevision="8")
        loop.tick()
        self.assertTrue(any(e.get("sharedQuestPhase")=="confirmed_in_own_quest_log" for e in events))
        self.assertEqual(len([m for m,p in rpc.calls if m=="buddy-quests.accept_shared"]),1)
        self.assertEqual(brain.histories,[]);self.assertFalse(any(m in ("buddy-chat.send","buddy-chat.emote") for m,p in rpc.calls))

    def test_ambiguous_accept_is_reserved_locally_and_backend_consumption_survives_brain_restart(self):
        loop,rpc,events,_=self.rig();rpc.timeout=True;loop.tick();loop.tick()
        self.assertEqual(len([m for m,p in rpc.calls if m=="buddy-quests.accept_shared"]),1)
        self.assertFalse(any(e.get("sharedQuestPhase")=="confirmed_in_own_quest_log" for e in events))
        rpc.state["pendingShare"]["acceptRequested"]=True
        replacement=converse.Conversation(rpc,FakeBrain(),character_guid=8,character_class="warlock")
        replacement.tick()
        self.assertEqual(len([m for m,p in rpc.calls if m=="buddy-quests.accept_shared"]),1)

    def test_foreign_disabled_busy_unknown_log_and_body_changes_do_not_accept(self):
        changes=[lambda r:r.state.update(sharedQuestsEnabled=False),lambda r:r.state.update(actionInFlight=True),
            lambda r:r.state.update(questLogKnown=False),lambda r:r.state.update(questLog=r.state['questLog'][:2]),
            lambda r:r.state['questLog'][2].update(slot=1),lambda r:r.state['pendingShare'].update(sharerGuid="5"),
            lambda r:r.state['pendingShare'].update(sequence="0"),
            lambda r:r.state['pendingShare'].update(kind="SHARED_KIND_NONE"),lambda r:r.state['pendingShare']['quest'].update(available=False),
            lambda r:r.party.update(sessionId="aw-replacement"),lambda r:r.party['group']['members'][1].update(online=False),
            lambda r:setattr(r,'tail',lambda p:p.update(sessionId="aw-replaced-during-read"))]
        for change in changes:
            with self.subTest(change=change):
                loop,rpc,_,_=self.rig();change(rpc);loop.tick()
                self.assertFalse(any(m=="buddy-quests.accept_shared" for m,p in rpc.calls))

    def test_transport_or_wrong_receipt_never_claims_confirmed_own_log(self):
        for change in (lambda s:s['lastSharedAcceptance'].update(status='REQUESTED'),lambda s:s['lastSharedAcceptance'].update(questId=10),
                       lambda s:s['lastSharedAcceptance'].update(shareSequence="2"),
                       lambda s:s['lastSharedAcceptance'].update(confirmedLogRevision="7")):
            loop,rpc,events,_=self.rig();loop.tick()
            rpc.state['questLog'][2]['questId']=9;rpc.state['questLogRevision']="8"
            rpc.state['lastSharedAcceptance'].update(status="CONFIRMED_IN_LOG",confirmedLogRevision="8")
            change(rpc.state)
            loop.tick()
            self.assertFalse(any(e.get('sharedQuestPhase')=='confirmed_in_own_quest_log' for e in events))

    def test_expired_share_declines_once_without_acceptance_then_fresh_seq_can_be_accepted(self):
        loop,rpc,events,_=self.rig();rpc.state['pendingShare'].pop('expiresInMs')  # protoJSON omitted zero
        loop.tick();loop.tick()
        self.assertEqual(len([m for m,p in rpc.calls if m=='buddy-quests.accept_shared']),1)
        self.assertTrue(any(e.get('sharedQuestPhase')=='expired_share_decline_sent' for e in events))
        self.assertFalse(any(e.get('sharedQuestPhase') in ('accepting_joels_shared_quest','confirmed_in_own_quest_log') for e in events))
        self.assertNotIn(9,[q.get('questId') for q in rpc.state['questLog']])
        rpc.state['pendingShare'].update(sequence='2',expiresInMs='30000',acceptRequested=False)
        loop.tick()
        self.assertEqual([p['shareSequence'] for m,p in rpc.calls if m=='buddy-quests.accept_shared'],['1','2'])
        rpc.state['questLog'][2]['questId']=9;rpc.state['questLogRevision']='8'
        rpc.state['lastSharedAcceptance'].update(status='CONFIRMED_IN_LOG',confirmedLogRevision='8')
        loop.tick();self.assertTrue(any(e.get('sharedQuestPhase')=='confirmed_in_own_quest_log' for e in events))

    def test_expired_cleanup_unknown_transport_and_mismatched_receipts_never_claim_decline_sent(self):
        changes=[lambda v:v['state'].update(sessionId='aw-other'),
            lambda v:v['state']['lastSharedAcceptance'].update(questId=10),
            lambda v:v['state']['lastSharedAcceptance'].update(shareSequence='2'),
            lambda v:v['state']['lastSharedAcceptance'].update(sharerGuid='5'),
            lambda v:v['state']['lastSharedAcceptance'].update(transportWritten=False),
            lambda v:v['state']['lastSharedAcceptance'].update(status='WRITE_UNCONFIRMED')]
        for change in changes:
            loop,rpc,events,_=self.rig();rpc.state['pendingShare']['expiresInMs']='0';rpc.result_change=change;loop.tick()
            self.assertFalse(any(e.get('sharedQuestPhase')=='expired_share_decline_sent' for e in events))
        loop,rpc,events,_=self.rig();rpc.state['pendingShare']['expiresInMs']='0';rpc.timeout=True;loop.tick();loop.tick()
        self.assertEqual(len([m for m,p in rpc.calls if m=='buddy-quests.accept_shared']),1)
        self.assertFalse(any(e.get('sharedQuestPhase') in ('expired_share_decline_sent','confirmed_in_own_quest_log') for e in events))

    def test_expired_alternate_offer_local_retirement_does_not_claim_decline_transport(self):
        loop,rpc,events,_=self.rig();rpc.state['pendingShare'].update(kind='SHARED_AUTOCOMPLETE',expiresInMs='0')
        def local(result):
            result['transportWritten']=False
            result['state']['lastSharedAcceptance'].update(transportWritten=False,failure='shared_alternate_offer_retired_without_acceptance')
        rpc.result_change=local;loop.tick()
        self.assertTrue(any(e.get('sharedQuestPhase')=='expired_shared_offer_retired_without_acceptance' for e in events))
        self.assertFalse(any(e.get('sharedQuestPhase') in ('expired_share_decline_sent','confirmed_in_own_quest_log') for e in events))

    def test_main_three_and_other_owner_profiles_keep_existing_chat_poll_only(self):
        for guid,owner,role in ((1,3,'priest'),(4,3,'mage'),(5,3,'rogue'),(8,7,'warlock'),(8,3,'mage')):
            rpc=FakeRPC([poll()]);loop=converse.Conversation(rpc,FakeBrain(),character_guid=guid,player_guid=owner,character_class=role)
            loop.tick();self.assertEqual([m for m,p in rpc.calls],['buddy-chat.poll'])

    def test_stop_before_accept_and_attempt_capacity_do_not_dispatch(self):
        for stop,capacity in ((True,0),(False,128)):
            loop,rpc,_,_=self.rig(stopped=lambda:stop)
            loop.shared_quest_attempts={("aw-previous",i) for i in range(capacity)}
            if stop:
                with self.assertRaises(converse.Stopped):loop.tick()
            else:loop.tick()
            self.assertFalse(any(m=="buddy-quests.accept_shared" for m,p in rpc.calls))

    def test_normal_rpc_whitelist_accepts_exact_shared_request_without_a_model_action(self):
        rpc=converse.RPC('http://127.0.0.1:8402/rpc');seen=[]
        class Reply:
            def __enter__(self):return self
            def __exit__(self,*args):pass
            def read(self,limit):return json.dumps(dict(jsonrpc='2.0',id=rpc.request_id,result=dict(transportWritten=True))).encode()
        def open_reply(request,timeout):seen.append(json.loads(request.data));return Reply()
        params=dict(sessionId='aw-current-nix',shareSequence='1',sharerGuid='3',questId=9)
        with patch.object(rpc,'opener',types.SimpleNamespace(open=open_reply)):
            self.assertTrue(rpc.call('buddy-quests.accept_shared',params)['transportWritten'])
        self.assertEqual(seen[0]['method'],'buddy-quests.accept_shared');self.assertEqual(seen[0]['params'],params)
        self.assertNotIn('ACCEPT_SHARED',converse.ACTIONS)


class DeveloperBrainTests(unittest.TestCase):
    def repository(self, path):
        path.mkdir()
        converse.subprocess.check_call(['git','init','-q',str(path)],stdout=converse.subprocess.DEVNULL)
        return path.resolve()

    def state(self, repo, source='owner'):
        return dict(character=dict(guid='1'),current_request=dict(source=source,senderGuid='3',verifiedPeer=source=='party_agent'),
            development=dict(enabled=True,directDevelopment=True,workspace=str(repo),selfGuid='1',ownerGuid='3',
                sessionId='aw-current',rpcUrl='http://127.0.0.1:8086/rpc',policyCommands=dict(show=['python3','-B','dev/self-edit','show','--guid','1','--session-id','aw-current','--rpc-url','http://127.0.0.1:8086/rpc'])))

    def test_developer_flags_enable_coding_without_sandbox_or_other_agents(self):
        command=converse.codex_command('codex','repo','schema','output',developer=True)
        self.assertIn('--approve-for-me',command);self.assertNotIn('--sandbox',command)
        self.assertEqual(command.count('--add-dir'),1)
        self.assertEqual(command[command.index('--add-dir')+1],str(converse.ROOT.resolve()))
        self.assertEqual(command[command.index('--cd')+1],'repo')
        self.assertIn('no extra server, secret, model, or actor-control authority',converse.DEVELOPER_PERMISSIONS)
        self.assertIn('sandbox_workspace_write.network_access=true',command)
        disabled=[command[i+1] for i,v in enumerate(command[:-1]) if v=='--disable']
        enabled=[command[i+1] for i,v in enumerate(command[:-1]) if v=='--enable']
        self.assertEqual(set(enabled),{'shell_tool','unified_exec'})
        self.assertFalse(set(disabled)&converse.DEVELOPER_FEATURES)
        self.assertTrue({'multi_agent','multi_agent_v2','apps','plugins','hooks','browser_use','computer_use'}<=set(disabled))
        self.assertNotIn('--model',command);self.assertFalse(any('dangerously' in x for x in command))

    def test_workspace_is_exact_same_repository_and_accepts_real_worktree(self):
        with tempfile.TemporaryDirectory() as temporary:
            folder=Path(temporary).resolve();repo=self.repository(folder/'repo');other=self.repository(folder/'other')
            (repo/'dev').mkdir()
            with patch.object(converse,'ROOT',repo):
                self.assertEqual(converse.checked_developer_workspace(repo),repo)
                for path in (repo/'dev',other,folder/'missing'):
                    with self.assertRaises(converse.ConversationError):converse.checked_developer_workspace(path)
                converse.subprocess.check_call(['git','-C',str(repo),'-c','user.name=Test','-c','user.email=test@example.invalid','commit','--allow-empty','-qm','fixture'])
                tree=folder/'worktree'
                converse.subprocess.check_call(['git','-C',str(repo),'worktree','add','--quiet','--detach',str(tree),'HEAD'])
                self.assertTrue((tree/'.git').is_file())
                self.assertEqual(converse.checked_developer_workspace(tree),tree)

    def test_same_generation_can_edit_go_and_lua_but_banter_and_unverified_peer_cannot(self):
        with tempfile.TemporaryDirectory() as temporary:
            folder=Path(temporary).resolve();repo=self.repository(folder/'repo');state_dir=folder/'state';state_dir.mkdir()
            fake=folder/'fake-codex';audit=folder/'calls.jsonl'
            fake.write_text('#!/usr/bin/env python3\nimport json,pathlib,sys\n'
                'a=sys.argv;p=sys.stdin.read();w=pathlib.Path(a[a.index("--cd")+1]);dev="--approve-for-me" in a\n'
                f'with pathlib.Path({str(audit)!r}).open("a") as f:f.write(json.dumps(dict(args=a,prompt=p,developer=dev))+"\\n")\n'
                'print(json.dumps(dict(type="turn.started")))\n'
                'if dev:\n'
                ' assert "--sandbox" not in a\n'
                ' (w/"policy.lua").write_text("return {decide=function(s) return {} end}")\n'
                ' (w/"controller.go").write_text("package controller\\n")\n'
                ' print(json.dumps(dict(type="item.completed",item=dict(id="cmd1",type="command_execution",status="completed",exit_code=1 if "forceToolFailure" in p else 0))))\n'
                ' print(json.dumps(dict(type="item.completed",item=dict(id="patch1",type="file_change",status="completed"))))\n'
                ' if "repairAfterFailure" in p: print(json.dumps(dict(type="item.completed",item=dict(id="cmd2",type="command_execution",status="completed",exit_code=0))))\n'
                ' if "fatalToolError" in p: print(json.dumps(dict(type="item.completed",item=dict(id="err1",type="error",message="CLI failure"))))\n'
                'else: assert a[a.index("--sandbox")+1]=="read-only"\n'
                'if "largeOutput" in p: print(json.dumps(dict(type="item.completed",item=dict(type="reasoning",text="x"*1100000))))\n'
                'pathlib.Path(a[a.index("--output-last-message")+1]).write_text(json.dumps(dict(text="I checked the controller.",emote="NONE")))\n'
                'print(json.dumps(dict(type="turn.completed")))\n')
            fake.chmod(0o700)
            with patch.object(converse,'ROOT',repo):brain=converse.CodexBrain(fake,state_dir,developer_workspace=repo)
            state=self.state(repo);state['largeOutput']=True
            self.assertEqual(brain.generate([{'speaker':'Joel','text':'Fix this controller.'}],state)['action'],'NONE')
            self.assertTrue((repo/'controller.go').is_file());self.assertTrue((repo/'policy.lua').is_file())
            for source in ('initiative','missing','party_agent'):
                denied=self.state(repo,source);denied['current_request']['verifiedPeer']=False
                brain.generate([],denied)
            unbound=self.state(repo);del unbound['development']['sessionId'];brain.generate([],unbound)
            peer=self.state(repo,'party_agent');brain.generate([],peer)
            repaired=self.state(repo);repaired.update(forceToolFailure=True,repairAfterFailure=True)
            self.assertEqual(brain.generate([],repaired)['action'],'NONE')
            self.assertEqual(brain.last_development_events,dict(failedCommands=1,successfulCommands=1,failedPatches=0,completedPatches=1))
            failed=self.state(repo);failed['fatalToolError']=True
            with self.assertRaises(converse.ConversationError):brain.generate([],failed)
            oversized=self.state(repo,'initiative');oversized['largeOutput']=True
            with self.assertRaisesRegex(converse.ConversationError,'output exceeds'):brain.generate([],oversized)
            rows=[json.loads(line) for line in audit.read_text().splitlines()]
            self.assertEqual([r['developer'] for r in rows],[True,False,False,False,False,True,True,True,False])
            self.assertIn('public owned-client APIs',rows[0]['prompt']);self.assertIn('aw-current',rows[0]['prompt'])
            self.assertIn('/private/tmp/warcraft-buddy-tools/go/bin/go',rows[0]['prompt'])
            self.assertIn('you have no tools',rows[1]['prompt'])
            self.assertEqual(len(rows),9) # Each explicit generation ran once, without a queued coding generation.

    def test_developer_tools_require_complete_success_and_unknown_tools_remain_forbidden(self):
        start=b'{"type":"turn.started"}\n';end=b'{"type":"turn.completed"}\n'
        for item in (dict(id='x',type='command_execution',status='completed',exit_code='0'),
                     dict(id='x',type='command_execution',status='completed'),
                     dict(id='x',type='file_change',status='unknown'),dict(id='x',type='mcp_tool_call'),
                     dict(id='x',type='error',message='failed')):
            with self.assertRaises(converse.ConversationError):
                converse.inspect_events(start+json.dumps(dict(type='item.completed',item=item)).encode()+b'\n'+end,developer=True)
        failure=dict(id='x',type='command_execution',status='failed',exit_code=None)
        result=converse.inspect_events(start+json.dumps(dict(type='item.completed',item=failure)).encode()+b'\n'+end,developer=True)
        self.assertEqual(result['failedCommands'],1) # Honest unresolved failure may be reported, never promoted to host success.
        with self.assertRaises(converse.ConversationError):
            converse.inspect_events(start+b'{"type":"item.started","item":{"id":"x","type":"command_execution"}}\n'+end,developer=True)

    def test_direct_mode_refusal_never_starts_workshop_and_context_is_body_bound(self):
        rpc=FakeRPC([poll(),poll([message(1,'Fix my follow controller')])]);rpc.url='http://127.0.0.1:8086/rpc'
        controls=types.SimpleNamespace(events=[],actions_available=True,party_available=True,party_session='aw-current',
            actions=dict(sessionId='aw-current',selfGuid='1'),refresh=lambda:None,context=lambda:{},
            execute=lambda *a,**kw:(_ for _ in ()).throw(converse.ConversationError('refused')),
            report_problem=lambda *a,**kw:(_ for _ in ()).throw(AssertionError('unexpected queued job')))
        class Brain(FakeBrain):
            developer_workspace=converse.ROOT
            def generate(self,history,control_state=None):self.state=control_state;return super().generate(history,control_state)
        brain=Brain(dict(text='Checking.',emote='NONE',action='FOLLOW'))
        loop=converse.Conversation(rpc,brain,controls=controls);loop.tick();loop.tick()
        development=brain.state['development'];self.assertTrue(development['directDevelopment'])
        self.assertEqual(development['sessionId'],'aw-current');self.assertEqual(development['selfGuid'],'1')
        self.assertIn('--expected-revision',development['policyCommands']['apply'])
        self.assertIn('No repair was queued',[p['text'] for m,p in rpc.calls if m=='buddy-chat.send'][0])
        controls.actions['sessionId']='new';self.assertFalse(loop.development_context('owner')['enabled'])

    def test_peer_tools_need_fresh_membership_in_the_same_core_body(self):
        for fresh_sid in ('aw-current','replacement'):
            loop,rpc,brain,controls,_=ProfilePartyTests().setup_party([poll(),
                poll([message(1,'Arlen, tell us a story',channel='PARTY')]),
                poll([message(2,'Mira, debug my controller',sender=1,channel='PARTY')])],
                reply=dict(text='I checked that source.',emote='NONE',action='REPAIR_CONTROLLER'))
            controls.actions_available=controls.party_available=True;controls.party_session='aw-current'
            controls.actions=dict(sessionId='aw-current',selfGuid='10');rpc.url='http://127.0.0.1:8087/rpc'
            brain.developer_workspace=converse.ROOT
            old_generate=brain.generate
            def generate(history,state=None):brain.state=state;return old_generate(history,state)
            brain.generate=generate
            old_call=rpc.call
            def call(method,params):
                result=old_call(method,params)
                if method=='buddy-party.state':result=dict(result,sessionId=fresh_sid)
                return result
            rpc.call=call
            loop.tick();loop.tick();loop.tick()
            self.assertEqual(len(brain.histories),1 if fresh_sid=='aw-current' else 0)
            self.assertEqual(controls.executed,[])
            if fresh_sid=='aw-current':
                self.assertTrue(brain.state['current_request']['verifiedPeer'])
                self.assertTrue(brain.state['development']['enabled'])


class MissionRuntimeContextTests(unittest.TestCase):
    def fixture(self):
        return dict(version=1,identity=dict(guid='1',sessionId='aw-current',rpcUrl='http://127.0.0.1:8086/rpc'),
            goalId='joel-expedition',phase='waiting_for_call_result',goodRevision='a'*64,updatedAt=time.time(),
            pending=[dict(outcome='pending',effect='mutation',params={'secret':'omit'})],
            memory=dict(stack=[dict(kind='walk',stage='step_'+str(i),data={'private':'omit'}) for i in range(10)]),
            last={'response':{'private':'omit'}})

    def install(self, repo, kind, data):
        (repo/'dev').mkdir(exist_ok=True)
        (repo/'dev/task-runtime').write_bytes(Path(__file__).with_name('task-runtime').read_bytes())
        folder=repo/'data/tasks'/kind/'1/runtime';folder.mkdir(parents=True,mode=0o700,exist_ok=True)
        path=folder/'state.json';path.write_text(json.dumps(data));path.chmod(0o600);return path

    def read(self):
        return converse.mission_runtime_context(1,'aw-current','http://127.0.0.1:8086/rpc')

    def test_current_body_metadata_projects_bounded_task_frames_and_paths_only(self):
        with tempfile.TemporaryDirectory(dir='/private/tmp') as temporary,patch.object(converse,'ROOT',Path(temporary)):
            repo=Path(temporary);data=self.fixture();path=self.install(repo,'expedition',data);before=path.read_bytes()
            self.install(repo,'fishing',data)
            result=self.read();row=result['expedition']
            self.assertTrue(row['available']);self.assertEqual(row['goalId'],'joel-expedition')
            self.assertEqual(row['phase'],'waiting_for_call_result');self.assertEqual(row['sourceRevision'],'a'*64)
            self.assertEqual(row['pendingCount'],1);self.assertEqual(len(row['stack']),8)
            self.assertEqual(row['stack'][0],dict(kind='walk',phase='step_2'))
            self.assertTrue(result['fishing']['available'])
            self.assertEqual(row['goalPath'],str(repo/'data/tasks/expedition/1/goal.json'))
            self.assertEqual(result['runtimeCLI'],str(repo/'dev/task-runtime'))
            self.assertEqual(result['sharedScripts']['taskStack'],str(repo/'behaviors/task-stack.lua'))
            self.assertEqual(result['sharedScripts']['expedition'],str(repo/'behaviors/expedition.lua'))
            self.assertNotIn('"private"',json.dumps(result));self.assertNotIn('"secret"',json.dumps(result))
            self.assertEqual(before,path.read_bytes())
            data['phase']='complete';data['memory']['stack']=[];data['pending']=[];path.write_text(json.dumps(data))
            self.assertEqual(self.read()['expedition']['phase'],'complete')

    def test_large_controller_metadata_uses_terminal_status_and_never_calls_crashed_hosts_active(self):
        with tempfile.TemporaryDirectory(dir='/private/tmp') as temporary,patch.object(converse,'ROOT',Path(temporary)):
            repo=Path(temporary);data=self.fixture()
            data['run']=dict(instance='a'*32,pid=os.getpid(),active=True)
            data['attempts']=[dict(id='audit-'+str(i),private='retained audit '*40) for i in range(165)]
            path=self.install(repo,'expedition',data);before=path.read_bytes()
            self.assertGreater(len(before),65536)
            runtime=converse.task_runtime_module();store=runtime.Store(path.parent)
            with store:
                row=self.read()['expedition'];self.assertTrue(row['available']);self.assertTrue(row['active'])
                self.assertNotIn('audit-',json.dumps(row));self.assertEqual(row['pendingCount'],1)
            crashed=self.read()['expedition']
            self.assertFalse(crashed['active']);self.assertTrue(crashed['exitUnconfirmed']);self.assertFalse(crashed['stopped'])
            terminal=copy.deepcopy(data);terminal['run'].update(instance='b'*32,previousInstance='a'*32,active=False,stoppedAt=time.time())
            terminal.update(hostError='controller_storage_budget_reached',hostErrorInstance='b'*32)
            store.save_run_status(terminal)
            row=self.read()['expedition'];self.assertTrue(row['available']);self.assertFalse(row['active'])
            self.assertTrue(row['stopped']);self.assertFalse(row['exitUnconfirmed']);self.assertEqual(row['pendingCount'],1)
            self.assertEqual(converse.mission_control_snapshot(1,'aw-current','http://127.0.0.1:8086/rpc')['runtimes']['expedition']['stopped'],True)
            faults=converse.mission_faults(1,'aw-current','http://127.0.0.1:8086/rpc')
            self.assertEqual([(r['instance'],r['field']) for r in faults],[('b'*32,'hostError')])
            self.assertEqual(path.read_bytes(),before)  # Metadata/terminal overlay never rewrites audit or pending.
            # Storage exhaustion may leave an older full snapshot and a fresh, exact terminal record.
            old=json.loads(before);old['updatedAt']=time.time()-60;path.write_text(json.dumps(old))
            row=self.read()['expedition'];self.assertTrue(row['available']);self.assertTrue(row['stopped'])
            self.assertEqual(row['updatedAt'],terminal['run']['stoppedAt'])
            self.assertEqual(converse.mission_faults(1,'aw-current','http://127.0.0.1:8086/rpc')[0]['field'],'hostError')
            path.write_bytes(before)
            terminal['identity']['sessionId']='aw-foreign';store.save_run_status(terminal)
            row=self.read()['expedition'];self.assertFalse(row['stopped']);self.assertTrue(row['exitUnconfirmed'])

    def test_actual_run_source_path_is_developer_metadata_only_and_root_bounded(self):
        with tempfile.TemporaryDirectory(dir='/private/tmp') as temporary,patch.object(converse,'ROOT',Path(temporary)):
            repo=Path(temporary);data=self.fixture();source=repo/'data/tasks/expedition/1/policy.lua'
            data['run']=dict(instance='a'*32,pid=os.getpid(),active=True,sourcePath=str(source))
            self.install(repo,'expedition',data);source.write_text('return {}')
            self.assertEqual(self.read()['expedition']['sourcePath'],str(source))
            for bad in ('relative.lua','/private/tmp/unrelated.lua',str(repo/'data/../outside.lua')):
                data['run']['sourcePath']=bad;self.install(repo,'expedition',data)
                self.assertNotIn('sourcePath',self.read()['expedition'])
            source.unlink();source.symlink_to(repo/'behaviors/expedition.lua')
            data['run']['sourcePath']=str(source);self.install(repo,'expedition',data)
            self.assertNotIn('sourcePath',self.read()['expedition'])
            self.assertIn('actual shared_tasks.<kind>.sourcePath',converse.DEVELOPER_PERMISSIONS)
            self.assertIn('host hot reloads this path on its next tick',converse.DEVELOPER_PERMISSIONS)
            self.assertIn('last-good source',converse.DEVELOPER_PERMISSIONS)
            self.assertNotIn('sourcePath',converse.CHAT_PERMISSIONS)

    def test_wrong_body_malformed_and_stale_metadata_remain_unavailable(self):
        changes=[lambda d:d['identity'].update(guid='4'),lambda d:d['identity'].update(sessionId='aw-old'),
            lambda d:d['identity'].update(rpcUrl='http://127.0.0.1:8087/rpc'),lambda d:d.update(version=True),
            lambda d:d.update(updatedAt=time.time()-16),lambda d:d.update(updatedAt=time.time()+5),
            lambda d:d.update(updatedAt=float('nan')),lambda d:d.update(goalId=''),
            lambda d:d.update(phase={'invented':'bad'}),lambda d:d.update(goodRevision='bad'),
            lambda d:d.update(pending={}),lambda d:d.update(pending=[{}]),
            lambda d:d['memory'].update(stack=[{}]),lambda d:d['memory'].update(stack=[dict(kind='walk',stage='s')]*13)]
        with tempfile.TemporaryDirectory(dir='/private/tmp') as temporary,patch.object(converse,'ROOT',Path(temporary)):
            repo=Path(temporary)
            self.assertFalse(self.read()['expedition']['available'])
            for change in changes:
                data=self.fixture();change(data);self.install(repo,'expedition',data)
                self.assertFalse(self.read()['expedition']['available'])
            path=self.install(repo,'expedition',self.fixture())
            for raw in ('[]','{"version":1,"version":1}','x'*65537):
                path.write_text(raw);self.assertFalse(self.read()['expedition']['available'])

    def test_private_regular_file_and_real_parent_directories_are_required(self):
        with tempfile.TemporaryDirectory(dir='/private/tmp') as temporary,patch.object(converse,'ROOT',Path(temporary)):
            repo=Path(temporary);path=self.install(repo,'expedition',self.fixture());raw=path.read_bytes()
            path.chmod(0o644);self.assertFalse(self.read()['expedition']['available'])
            path.chmod(0o600);outside=repo/'outside';outside.write_bytes(raw);outside.chmod(0o600)
            path.unlink();path.symlink_to(outside);self.assertFalse(self.read()['expedition']['available'])
            path.unlink();os.link(outside,path);self.assertFalse(self.read()['expedition']['available'])
            path.unlink();path.write_bytes(raw);path.chmod(0o600)
            folder=path.parent;folder.rename(folder.with_name('real-runtime'));folder.symlink_to(folder.with_name('real-runtime'),target_is_directory=True)
            self.assertFalse(self.read()['expedition']['available'])


class MissionHandoffTests(unittest.TestCase):
    def setUp(self):
        temporary=tempfile.TemporaryDirectory(dir='/private/tmp');self.addCleanup(temporary.cleanup)
        self.repo=Path(temporary.name);(self.repo/'dev').mkdir()
        source=Path(__file__).with_name('task-runtime').read_bytes()
        (self.repo/'dev/task-runtime').write_bytes(source)
        loader=importlib.machinery.SourceFileLoader('handoff_host',str(self.repo/'dev/task-runtime'))
        spec=importlib.util.spec_from_loader(loader.name,loader)
        self.host=importlib.util.module_from_spec(spec);loader.exec_module(self.host)
        self.folder=self.repo/'data/tasks/expedition/1/runtime'
        self.store=self.host.Store(self.folder).__enter__();self.addCleanup(self.store.__exit__)
        self.data=dict(version=1,identity=dict(guid='1',sessionId='aw-current',rpcUrl='http://127.0.0.1:8086/rpc'),
            run=dict(instance='a'*32,pid=os.getpid(),active=True),goalId='mission',phase='walking',goodRevision='b'*64,
            memory=dict(stack=[dict(kind='walk',stage='waiting',private='omit')]),pending=[],last={'secret':'omit'})
        self.store.save(self.data)
        patcher=patch.object(converse,'ROOT',self.repo);patcher.start();self.addCleanup(patcher.stop)
        case=self
        class RPC:
            url='http://127.0.0.1:8086/rpc'
            def __init__(self):self.calls=[];self.hook=lambda _:None
            def call(self,method,params):
                self.calls.append((method,params));self.hook(method)
                if method=='buddy-party.state':return dict(sessionId='aw-current')
                if method=='buddy-professions.state':return copy.deepcopy(case.professions)
                if method=='buddy-actions.status':return copy.deepcopy(case.actions)
                if method=='buddy-actions.follow':return dict(case.actions,following=params['enabled'])
                return {}
        self.actions=dict(sessionId='aw-current',selfGuid='1',following=False,healing=True,assisting=True,buffing=True,
            healingLeaseRemainingMs='14400000',assistLeaseRemainingMs='14400000',buffLeaseRemainingMs='14400000')
        self.professions=dict(sessionId='aw-current')
        self.rpc=RPC();self.controls=converse.BuddyControls(self.rpc)
        self.controls.actions_available=True;self.controls.actions=self.actions

    def stopped(self):
        self.data['run']['active']=False;self.store.save(self.data);self.store.__exit__()

    def test_stop_mission_writes_only_exact_marker_preserving_native_and_chat_history(self):
        before=(self.folder/'state.json').read_bytes()
        self.assertIn('requested',self.controls.execute('STOP_MISSION'))
        self.assertTrue(self.host.stop_requested(self.store,self.data))
        self.assertEqual((self.folder/'state.json').read_bytes(),before);self.assertEqual(self.rpc.calls,[])
        self.assertTrue(converse.mission_chat_context(1,'aw-current',self.rpc.url)['runtimes']['expedition']['stopRequested'])
        self.assertIn('STOP_MISSION',converse.ACTIONS)
        # A separate brain can continue serving chat while the task lock remains held.
        brain=FakeBrain();chat=FakeRPC([poll(),poll([message(1,'How is the mission?')])])
        loop=converse.Conversation(chat,brain);loop.tick();loop.tick()
        self.assertEqual(len(brain.histories),1);self.assertTrue(self.store.held())

    def test_follow_waits_for_exact_cooperative_exit_then_passive_intents_do_not_block(self):
        for command in ('FOLLOW','STOP_FOLLOWING'):
            self.data['run']['active']=True;self.store.save(self.data)
            if self.store.lock.closed:self.store=self.host.Store(self.folder).__enter__()
            def finish(_):
                self.assertTrue(self.host.stop_requested(self.store,self.data));self.stopped()
            with patch.object(converse.time,'sleep',side_effect=finish),\
                    patch.object(converse,'party_town_snapshot',return_value={}) as town,\
                    patch.object(converse,'area_quest_snapshot',return_value={}),\
                    patch.object(converse,'quest_task_snapshot',return_value={}):
                result=self.controls.execute(command)
            self.assertIn('follow',result);self.assertEqual(self.rpc.calls[-1],('buddy-actions.follow',{'enabled':command=='FOLLOW'}))
            town.assert_called_once();self.rpc.calls=[]

    def test_pending_uncertain_state_never_follows_or_revokes_another_controller(self):
        self.data['pending']=[dict(outcome='pending',effect='mutation')];self.store.save(self.data)
        with patch.object(converse,'party_town_snapshot') as town:
            self.assertIn('uncertain',self.controls.execute('FOLLOW'))
        town.assert_not_called();self.assertEqual(self.rpc.calls,[])
        self.assertEqual(self.store.load()['pending'],self.data['pending'])

    def test_active_recovery_cast_cleanup_or_changed_body_refuses_follow(self):
        self.stopped()
        for mutation in (lambda:self.actions.update(recovery={'active':True}),lambda:self.actions.update(castPending=True),
                lambda:self.actions.update(interactionLeaseRemainingMs='1000'),
                lambda:self.professions.update(operation={'status':'OUTCOME_UNCONFIRMED','leaseReleased':True}),
                lambda:self.professions.update(operation=[]),lambda:self.actions.update(sessionId='aw-replacement')):
            base=copy.deepcopy(self.actions);self.professions=dict(sessionId='aw-current');mutation()
            self.assertIsNotNone(self.controls.mission_handoff(follow=True,wait_seconds=0))
            self.assertFalse(any(m=='buddy-actions.follow' for m,_ in self.rpc.calls));self.rpc.calls=[];self.actions=base

    def test_replacement_instance_during_final_native_reads_never_gets_follow(self):
        self.stopped()
        def replace(method):
            if method=='buddy-actions.status':
                self.data['run']['instance']='c'*32;self.store.save(self.data)
        self.rpc.hook=replace
        self.assertIn('instance changed',self.controls.mission_handoff(follow=True,wait_seconds=0))
        self.assertFalse(any(m=='buddy-actions.follow' for m,_ in self.rpc.calls))

    def test_controller_stop_callbacks_cannot_replace_mission_or_body_before_follow_write(self):
        self.stopped()
        for changed in ('body','instance','cast'):
            self.actions.update(sessionId='aw-current',castPending=False);self.data['run']['instance']='a'*32;self.store.save(self.data)
            def replace(*_,**__):
                if changed=='body':self.actions['sessionId']='aw-new'
                elif changed=='cast':self.actions['castPending']=True
                else:self.data['run']['instance']='c'*32;self.store.save(self.data)
                return {}
            self.rpc.calls=[]
            with patch.object(converse,'party_town_snapshot',side_effect=replace),\
                    patch.object(converse,'area_quest_snapshot',return_value={}),\
                    patch.object(converse,'quest_task_snapshot',return_value={}):
                self.assertIn('unchanged',self.controls.execute('FOLLOW'))
            self.assertFalse(any(m=='buddy-actions.follow' for m,_ in self.rpc.calls))

    def test_ordinary_context_is_sanitized_current_metadata_and_crash_is_not_stopped(self):
        result=converse.mission_chat_context(1,'aw-current',self.rpc.url)
        row=result['runtimes']['expedition'];self.assertTrue(row['active']);self.assertEqual(row['goalId'],'mission')
        for private in ('sourcePath','statePath','goalPath','runtimeCLI','instance','pid','secret','private'):
            self.assertNotIn(private,json.dumps(result))
        state=self.controls.context();self.assertTrue(state['abilities']['mission_stop'])
        self.assertTrue(state['missions']['runtimes']['expedition']['active'])
        self.store.__exit__()  # A released lock without a cooperative inactive record is a crash/unknown.
        self.assertIn("couldn't confirm",self.controls.execute('STOP_MISSION'))
        self.assertIsNotNone(self.controls.mission_handoff(follow=True,wait_seconds=0))
        self.data['identity']['sessionId']='aw-old';self.store.save(self.data)
        self.assertFalse(converse.mission_chat_context(1,'aw-current',self.rpc.url)['runtimes']['expedition']['available'])


class MissionFaultRepairTests(unittest.TestCase):
    def setUp(self):
        MissionHandoffTests.setUp(self)
        self.data.update(policyError='typed_bridge_rejected',policyErrorInstance='a'*32,rejectedRevision='c'*64,hostError='')
        self.store.save(self.data)
        self.controls.party_available=True;self.controls.party_session='aw-current'
        self.controls.refresh=lambda:None
        self.controls.execute=lambda action,**kw:self.assertEqual(action,'NONE')
        self.brain=FakeBrain(dict(text='I checked the source; installation remains unconfirmed.',emote='WAVE',action='FOLLOW'))
        self.brain.developer_workspace=self.repo;self.brain.state_dir=self.repo/'brain';self.brain.state_dir.mkdir(mode=0o700)
        self.brain.states=[];generate=self.brain.generate
        def record(history,state=None):self.brain.states.append(state);return generate(history,state)
        self.brain.generate=record
        self.statuses=[]
        self.loop=self.new_loop()
        call=self.rpc.call;self.polls=[]
        def rpc(method,params):
            if method=='buddy-chat.poll':
                self.rpc.calls.append((method,params))
                return self.polls.pop(0) if self.polls else poll(sessionId='chat-current')
            if method=='buddy-chat.send':self.rpc.calls.append((method,params));return dict(transportWritten=True)
            return call(method,params)
        self.rpc.call=rpc

    def new_loop(self):
        loop=converse.Conversation(self.rpc,self.brain,controls=self.controls,status=lambda **kw:self.statuses.append(kw))
        loop.initialized=True;loop.session_id='chat-current';return loop

    def test_fresh_fault_runs_same_brain_once_across_restart_preserving_unknowns_no_game_writes(self):
        self.data['pending']=[dict(outcome='pending',effect='mutation',id='issued-1')]
        self.data.update(hostError='no_valid_policy',hostErrorInstance='a'*32,policyError='')
        self.data['run'].update(active=False,stoppedAt=time.time());self.store.save(self.data);self.store.__exit__()
        before=(self.folder/'state.json').read_bytes()
        self.loop.tick();self.new_loop().tick()
        self.assertEqual(len(self.brain.histories),1)
        request=self.brain.states[0]['current_request'];self.assertIs(type(request),converse._MissionFaultRequest)
        self.assertEqual(request['source'],'mission_fault');self.assertFalse(request['controls_allowed'])
        self.assertEqual(self.brain.states[0]['development']['sessionId'],'aw-current')
        self.assertEqual((self.folder/'state.json').read_bytes(),before)
        self.assertFalse(any(m in ('buddy-chat.send','buddy-chat.emote','buddy-actions.follow') for m,_ in self.rpc.calls))
        self.assertTrue(any(s.get('missionRepairPhase')=='developer_turn_completed' for s in self.statuses))
        self.assertNotIn('installed',self.statuses[-1]['missionRepairPhase'])

    def test_only_actual_fresh_errors_or_explicit_fresh_stall_are_faults(self):
        baseline=copy.deepcopy(self.data)
        for mutation in (lambda d:d.update(updatedAt=time.time()-16),lambda d:d['identity'].update(sessionId='aw-old'),
                lambda d:d.update(policyError='',phase='waiting_for_server_receipt'),
                lambda d:d.update(policyError='',hostError='task_stopped_or_deadline'),lambda d:d.update(version=2),
                lambda d:d.update(policyError='',hostError='historical_prior_run_error'),
                lambda d:(d.update(policyError='',hostError='old_error',hostErrorInstance='a'*32),
                    d['run'].update(instance='b'*32,active=False,stoppedAt=time.time())),
                lambda d:d['run'].update(instance='b'*32,active=False,stoppedAt=time.time()),
                lambda d:d.update(policyErrorInstance=''),
                lambda d:(d.update(policyError='',hostError='old_stop'),d['run'].update(active=False,stoppedAt=time.time()-16)),
                lambda d:d.update(policyError='',memory=dict(health=dict(code='stalled',phase='cast',since=time.time()-16),stack=[]))):
            data=copy.deepcopy(baseline);mutation(data);self.store.write('state.json',json.dumps(data).encode())
            self.assertFalse(converse.mission_faults(1,'aw-current',self.rpc.url))
        data=copy.deepcopy(baseline);data.update(policyError='',memory=dict(health=dict(code='stalled',phase='cast',since=time.time()),stack=[]))
        self.store.save(data);self.loop.tick()
        self.assertEqual(self.brain.states[0]['mission_fault']['field'],'health')
        self.assertEqual(len(self.brain.histories),1)

    def test_new_rejected_candidate_same_fallback_and_error_is_a_distinct_bounded_fault(self):
        self.loop.tick()
        with patch.object(self.controls,'context',side_effect=AssertionError('reserved fault must stay cheap')) as context:
            self.loop.tick()
        context.assert_not_called()
        self.assertEqual(len(self.brain.histories),1)
        self.data['rejectedRevision']='d'*64;self.store.save(self.data);self.loop.tick()
        self.assertEqual(len(self.brain.histories),2)
        self.assertNotEqual(self.brain.states[0]['mission_fault']['key'],self.brain.states[1]['mission_fault']['key'])
        self.assertEqual(self.brain.states[1]['mission_fault']['rejectedSourceRevision'],'d'*64)

    def test_current_observation_error_wakes_fault_turn_and_recovered_or_wrong_run_error_does_not(self):
        self.data.update(policyError='',observationError='world:rpc_result_unconfirmed',observationErrorInstance='a'*32)
        self.store.save(self.data);self.loop.tick()
        self.assertEqual(self.brain.states[0]['mission_fault']['field'],'observationError')
        self.data['observationError']='';self.store.save(self.data)
        self.assertFalse(converse.mission_faults(1,'aw-current',self.rpc.url))
        self.data.update(observationError='world:json_size_bound',observationErrorInstance='old-run')
        self.store.save(self.data);self.assertFalse(converse.mission_faults(1,'aw-current',self.rpc.url))

    def test_human_selected_first_and_fresh_pre_generation_poll_defers_without_consuming_cursor(self):
        self.brain.reply=dict(text='Hello, Joel.',emote='NONE',action='NONE')
        self.polls=[poll(sessionId='chat-current'),poll([message(1,'Hello, Arlen')],sessionId='chat-current')]
        self.loop.tick()
        self.assertEqual(self.loop.cursor,0);self.assertEqual(self.brain.histories,[])
        self.assertFalse((self.brain.state_dir/'mission-faults').exists())
        self.polls=[poll([message(1,'Hello, Arlen')],sessionId='chat-current'),poll(latest=1,sessionId='chat-current')]
        self.loop.tick()
        self.assertEqual(self.brain.states[0]['current_request']['source'],'owner')
        self.assertEqual(len(self.brain.histories),1)
        self.assertFalse((self.brain.state_dir/'mission-faults').exists())

    def test_rate_and_capacity_are_durable_fail_closed_before_model(self):
        clock=time.time()
        with patch.object(converse.time,'time',side_effect=lambda:clock):
            for error in ('first_error','second_error','third_error'):
                self.data['policyError']=error;self.store.save(self.data);self.loop.tick()
            self.assertEqual(len(self.brain.histories),2)
            self.assertEqual(self.statuses[-1]['missionRepairPhase'],'rate_limited')
            clock+=121;self.store.save(self.data);self.new_loop().tick()
            self.assertEqual(len(self.brain.histories),3)
            journal=self.host.Store(self.brain.state_dir/'mission-faults')
            data=journal.load();data['claims']=[dict(key=('%064x'%i),at=clock-121) for i in range(64)]
            journal.save(data);self.data['policyError']='fourth';self.store.save(self.data);self.loop.tick()
            self.assertEqual(len(self.brain.histories),3)
            self.assertEqual(self.statuses[-1]['missionRepairPhase'],'capacity_reached')
            self.assertEqual(len(journal.load()['claims']),64)

    def test_private_fault_source_is_not_spoofable_and_is_revalidated_before_developer_tools(self):
        self.loop.tick();state=self.brain.states[0]
        brain=object.__new__(converse.CodexBrain);brain.developer_workspace=self.repo
        self.assertTrue(brain.development_enabled(state))
        spoof=dict(state,current_request=dict(state['current_request']))
        self.assertFalse(brain.development_enabled(spoof))
        self.data['identity']['sessionId']='aw-replaced';self.store.save(self.data)
        self.assertFalse(brain.development_enabled(state))
        with self.assertRaisesRegex(converse.ConversationError,'developer binding changed'):
            brain.generate([],state)  # Refuse before any subprocess/model call; never fall back to ordinary generation.

    def test_generation_failure_is_reserved_once_and_body_change_prevents_generation(self):
        self.brain.reply=converse.ConversationError('model timeout')
        self.loop.tick();self.new_loop().tick()
        self.assertEqual(len(self.brain.histories),1)
        self.assertTrue(any(s.get('missionRepairPhase')=='unconfirmed' for s in self.statuses))
        self.data['policyError']='new_fault';self.store.save(self.data)
        self.actions['sessionId']='aw-replaced';self.loop.tick()
        self.assertEqual(len(self.brain.histories),1)

    def test_body_replacement_during_reservation_never_starts_model_on_old_local_fault(self):
        reserve=converse.reserve_mission_fault
        def replace(*args,**kwargs):
            result=reserve(*args,**kwargs)
            if kwargs.get('reserve',True) and result=='reserved':self.actions['sessionId']='aw-replaced'
            return result
        with patch.object(converse,'reserve_mission_fault',side_effect=replace):self.loop.tick()
        self.assertEqual(self.brain.histories,[])
        self.assertEqual(self.store.load()['identity']['sessionId'],'aw-current')
        self.assertEqual(len(self.host.Store(self.brain.state_dir/'mission-faults').load()['claims']),1)
        self.assertEqual(self.statuses[-1]['missionRepairPhase'],'body_changed_before_generation')


class SharedRoleConfigurationTests(unittest.TestCase):
    def config(self):
        return json.loads((Path(__file__).resolve().parent.parent/'config/companion-roles.json').read_text())

    def install(self, repo, data):
        folder=repo/'config';folder.mkdir(exist_ok=True)
        path=folder/'companion-roles.json';path.write_text(json.dumps(data));return path

    def test_all_five_roles_are_validated_and_external_edits_are_read_fresh(self):
        data=self.config()
        with tempfile.TemporaryDirectory() as temporary,patch.object(converse,'ROOT',Path(temporary)):
            repo=Path(temporary);path=self.install(repo,data)
            first=converse.companion_role_context('Arlen')
            self.assertTrue(first['available']);self.assertEqual(first['self']['gameplay'],['healer','party_coordinator'])
            self.assertEqual(first['self']['development'],['mission_acceptance'])
            team={row['name']:row for row in first['team']}
            self.assertEqual(set(team),{'Mira','Rook','Nixwick','Lothar'})
            self.assertEqual(team['Lothar']['gameplay'],[]);self.assertEqual(team['Lothar']['development'],['shared_integration'])
            data['roles']['Arlen']['development']=['combat_verification'];path.write_text(json.dumps(data))
            second=converse.companion_role_context('Arlen')
            self.assertEqual(second['self']['development'],['combat_verification'])
            self.assertNotEqual(first['revision'],second['revision'])
            self.assertEqual(converse.companion_role_context('Unknown'),{'available':False})

    def test_missing_unknown_or_malformed_role_configuration_never_invents_assignments(self):
        changes=[lambda d:d['roles'].pop('Lothar'),lambda d:d['roles'].update(Other=d['roles']['Arlen']),
            lambda d:d['roles']['Arlen'].update(gameplay=['invented_power']),
            lambda d:d['roles']['Mira'].update(development=['navigation','navigation']),
            lambda d:d['roles']['Rook'].update(extra=True),lambda d:d['roles']['Nixwick'].update(gameplay=[]),
            lambda d:d.update(version=True),lambda d:d['architecture'].update(lua={'bad':'value'})]
        with tempfile.TemporaryDirectory() as temporary,patch.object(converse,'ROOT',Path(temporary)):
            repo=Path(temporary)
            self.assertEqual(converse.companion_role_context('Arlen'),{'available':False})
            for change in changes:
                data=self.config();change(data);self.install(repo,data)
                self.assertEqual(converse.companion_role_context('Arlen'),{'available':False})
            path=self.install(repo,self.config());path.write_text('x'*8193)
            self.assertEqual(converse.companion_role_context('Arlen'),{'available':False})
            path.write_text('{"version":1,"version":1,"roles":{}}')
            self.assertEqual(converse.companion_role_context('Arlen'),{'available':False})

    def test_role_config_refuses_symlink_parent_file_hardlink_and_writable_input(self):
        with tempfile.TemporaryDirectory() as temporary,patch.object(converse,'ROOT',Path(temporary)):
            repo=Path(temporary);path=self.install(repo,self.config());saved=path.read_bytes()
            outside=repo/'other.json';outside.write_bytes(saved)
            path.unlink();path.symlink_to(outside)
            self.assertEqual(converse.companion_role_context('Arlen'),{'available':False})
            path.unlink();os.link(outside,path)
            self.assertEqual(converse.companion_role_context('Arlen'),{'available':False})
            path.unlink();path.write_bytes(saved);path.chmod(0o666)
            self.assertEqual(converse.companion_role_context('Arlen'),{'available':False})
            path.chmod(0o644);path.parent.rename(repo/'other-config');(repo/'config').symlink_to(repo/'other-config',target_is_directory=True)
            self.assertEqual(converse.companion_role_context('Arlen'),{'available':False})

    def test_each_fake_generation_gets_current_roles_only_in_developer_context(self):
        with tempfile.TemporaryDirectory() as temporary:
            repo=Path(temporary).resolve();state_dir=repo/'state';state_dir.mkdir();data=self.config();path=self.install(repo,data)
            fake=repo/'fake-codex';audit=repo/'prompts.jsonl'
            fake.write_text('#!/usr/bin/env python3\nimport json,pathlib,sys\n'
                'a=sys.argv;p=sys.stdin.read()\n'
                f'with pathlib.Path({str(audit)!r}).open("a") as f:f.write(json.dumps(dict(args=a,prompt=p))+"\\n")\n'
                'pathlib.Path(a[a.index("--output-last-message")+1]).write_text(json.dumps(dict(text="Checked.",emote="NONE")))\n'
                'print(json.dumps(dict(type="turn.completed")))\n')
            fake.chmod(0o700)
            with patch.object(converse,'ROOT',repo),patch.object(converse,'checked_developer_workspace',return_value=repo):
                brain=converse.CodexBrain(fake,state_dir,developer_workspace=repo)
                state=DeveloperBrainTests().state(repo)
                state['shared_development']={'untrusted':'ignored'};state['model_selection']={'requestedModel':'made-up'}
                state['shared_tasks']={'untrusted':'ignored'}
                brain.generate([],state)
                data['roles']['Arlen']['development']=['profession_verification'];path.write_text(json.dumps(data));brain.generate([],state)
                state['current_request']['source']='initiative';brain.generate([],state)
                state['current_request']['source']='owner'
                explicit=converse.CodexBrain(fake,state_dir,developer_workspace=repo,model='gpt-6.1-sol',reasoning_effort='high')
                explicit.generate([],state)
            rows=[json.loads(row) for row in audit.read_text().splitlines()]
            states=[json.loads(row['prompt'].split('Current observations and control state JSON:\n',1)[1].split('\nConversation JSON:\n',1)[0]) for row in rows]
            self.assertEqual(states[0]['shared_development']['self']['development'],['mission_acceptance'])
            self.assertEqual(states[1]['shared_development']['self']['development'],['profession_verification'])
            self.assertIn(str(repo/'dev/task-claim'),rows[0]['prompt']);self.assertIn('semantic task AND every touched file',rows[0]['prompt'])
            self.assertIn('durable observed progress',json.dumps(states[0]['shared_development']))
            self.assertNotIn('shared_development',states[2]);self.assertNotIn('model_selection',states[2])
            self.assertNotIn('shared_tasks',states[2]);self.assertNotIn('untrusted',json.dumps(states[0]['shared_tasks']))
            self.assertNotIn('Shared development ownership:',rows[2]['prompt'])
            self.assertTrue(all('--model' not in row['args'] for row in rows[:3]))
            self.assertFalse(states[0]['model_selection']['configured'])
            self.assertEqual(rows[3]['args'][rows[3]['args'].index('--model')+1],'gpt-6.1-sol')
            self.assertEqual(states[3]['model_selection']['requestedModel'],'gpt-6.1-sol')
            self.assertTrue(states[3]['model_selection']['configured'])
            self.assertIn('model_reasoning_effort="high"',rows[3]['args'])
            self.assertEqual(states[3]['model_selection']['requestedReasoningEffort'],'high')
            self.assertTrue(states[3]['model_selection']['reasoningOverride'])

    def test_explicit_model_argument_and_status_preserve_the_unpinned_default_and_reasoning(self):
        for developer in (False,True):
            default=converse.codex_command('codex','repo','schema','out',developer=developer)
            self.assertNotIn('--model',default)
            explicit=converse.codex_command('codex','repo','schema','out',developer=developer,model='gpt-6.1-sol')
            self.assertEqual(explicit[explicit.index('--model')+1],'gpt-6.1-sol')
            self.assertIn('--ignore-user-config',explicit)
            self.assertFalse(any('reasoning_effort' in arg for arg in explicit))
            removed=list(explicit);i=removed.index('--model');del removed[i:i+2]
            self.assertEqual(removed,default)
        with tempfile.TemporaryDirectory() as directory:
            default=converse.CodexBrain('codex',directory).model_selection()
            explicit=converse.CodexBrain('codex',directory,model='gpt-6.1-sol').model_selection()
        self.assertEqual(default['source'],'cli_default_unpinned');self.assertIsNone(default['requestedModel'])
        self.assertTrue(explicit['configured']);self.assertEqual(explicit['requestedModel'],'gpt-6.1-sol')
        self.assertEqual(explicit['source'],'explicit_cli');self.assertFalse(explicit['effectiveModelKnown'])
        self.assertIsNone(default['requestedReasoningEffort']);self.assertFalse(default['reasoningOverride'])
        for bad in ('','--other-flag','gpt\nmodel','x'*129):
            with self.assertRaises(converse.ConversationError):converse.codex_command('codex','repo','s','o',model=bad)

    def test_explicit_reasoning_argument_and_status_never_change_omitted_defaults(self):
        for developer in (False,True):
            default=converse.codex_command('codex','repo','schema','out',developer=developer)
            self.assertFalse(any('model_reasoning_effort' in arg for arg in default))
            for effort in ('low','medium','high','xhigh','max','ultra'):
                command=converse.codex_command('codex','repo','schema','out',developer=developer,
                                               model='gpt-6.1-sol',reasoning_effort=effort)
                literal='model_reasoning_effort='+json.dumps(effort)
                self.assertEqual(command.count(literal),1)
                self.assertEqual(command[command.index(literal)-1],'-c')
                removed=list(command);i=removed.index('--model');del removed[i:i+2]
                i=removed.index(literal);del removed[i-1:i+1]
                self.assertEqual(removed,default)
                with tempfile.TemporaryDirectory() as directory:
                    selected=converse.CodexBrain('codex',directory,model='gpt-6.1-sol',reasoning_effort=effort).model_selection()
                self.assertEqual(selected['requestedReasoningEffort'],effort)
                self.assertTrue(selected['reasoningOverride']);self.assertFalse(selected['effectiveModelKnown'])
        for bad in ('','light','HIGH','high\n','high" -c injected=true',False,1,[]):
            with self.assertRaises(converse.ConversationError):
                converse.codex_command('codex','repo','s','o',reasoning_effort=bad)


if __name__ == "__main__":
    unittest.main()
