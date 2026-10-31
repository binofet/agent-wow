#!/usr/bin/env python3
"""Standing owner-feed quest automation, entirely fake RPC and clocks."""
import copy
import contextlib
import importlib.machinery
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

loader=importlib.machinery.SourceFileLoader("party_quests_tested",str(Path(__file__).with_name("party-quests")))
spec=importlib.util.spec_from_loader(loader.name,loader)
party=importlib.util.module_from_spec(spec)
loader.exec_module(party)
MCBRIDE=0xf1300000c500022b
WILLEM=0xf130000337000210


class FakeRPC:
    def __init__(self,guid=1):
        self.guid,self.calls,self.session=guid,[],"own-quest-session"
        self.active,self.complete={7},{7}
        self.owner_active,self.owner_done={15},{7}
        self.npc,self.entry=MCBRIDE,197
        self.offer,self.reward,self.receipt={},{},{}
        self.revision,self.log_revision=1,1
        self.choices,self.age,self.assembling=[],1000,False
        self.self_x,self.owner_x=-8902.,-8900.
        self.owner_present,self.owner_combat,self.own_combat=True,False,False
        self.good_party,self.apply_accept,self.apply_claim=True,True,True
        self.error_on,self.after_travel=None,None
        self.feed_reads=0
        self.feed_change=None
        self.item_details={}
        self.after_lookup=None
        self.shared_enabled,self.share,self.share_calls=False,{},0
        self.share_error,self.share_apply=False,True

    def feed(self):
        self.feed_reads+=1
        if self.feed_change:self.feed_change(self)
        return {"available":True,"bodySessionId":"body-chat","addonSession":"012345abcdef","revision":self.feed_reads,
                "ageMs":self.age,"assembling":self.assembling,"quests":[{"questId":q,"state":0} for q in sorted(self.owner_active)],
                "completedQuestIds":sorted(self.owner_done),"rewarded":[],"accepted":[]}

    def world(self):
        p={"present":True,"aliveKnown":True,"alive":True,"unitFlagsKnown":True,"mapKnown":True,
           "mapId":0,"positionKnown":True,"x":self.self_x,"y":-100.,"z":80.}
        own=dict(p,guid=str(self.guid),classKnown=True,classId={1:5,4:8,5:4}.get(self.guid,0),unitFlags=0x80000 if self.own_combat else 0)
        owner=dict(p,guid="3",present=self.owner_present,x=self.owner_x,unitFlags=0x80000 if self.owner_combat else 0)
        return {"self":own,"owner":owner}

    def quests(self):
        c={"guid":str(self.npc),"entry":self.entry,"present":True,"aliveKnown":True,"alive":True,
           "mapKnown":True,"mapId":0,"positionKnown":True,"x":-8900.,"y":-100.,"z":80.,"npcFlags":2}
        return {"sessionId":self.session,"questLogKnown":True,"questLogRevision":str(self.log_revision),
                "questLog":[{"questId":q,"complete":q in self.complete} for q in sorted(self.active)],
                "nearby":[{"creature":c,"distanceKnown":True,"distance":abs(self.self_x+8900)}],
                "offer":copy.deepcopy(self.offer),"turnInOffer":copy.deepcopy(self.reward),"lastReward":copy.deepcopy(self.receipt),
                "sharedQuestsEnabled":self.shared_enabled,"pendingShare":copy.deepcopy(self.share)}

    def call(self,method,params):
        self.calls.append((method,copy.deepcopy(params)))
        if method=="buddy-world.snapshot":return self.world()
        if method in ("buddy-quests.discover","buddy-quests.state"):return self.quests()
        if method=="buddy-chat.owner_feed":return self.feed()
        if method=="buddy-actions.status":return {"position":{"known":True,"mapKnown":True,"mapId":0,"x":self.self_x,"y":-100.,"z":80.}}
        if method=="buddy-party.state":return {"group":{"known":True,"inGroup":True,"members":[
            {"guid":str(self.guid),"name":{1:"Arlen",4:"Mira",5:"Rook"}.get(self.guid,"Other"),"self":True,"online":True},
            {"guid":"3","name":"Joel" if self.good_party else "Someone","online":True}]}}
        if method=="buddy-trade.lookup_items":
            result={"sessionId":"trade-session","items":[copy.deepcopy(self.item_details.get(item_id,{"itemId":item_id,"pending":True})) for item_id in params["itemIds"]]}
            if self.after_lookup:self.after_lookup(self)
            return result
        if method=="buddy-actions.acquire_interaction":return {"token":"owned-lease","state":{"interactionOwner":"quests"}}
        if method=="buddy-actions.release_interaction":return {}
        if method=="buddy-actions.travel_quest":
            self.self_x=-8903.
            if self.after_travel:self.after_travel(self)
            return {"reason":"quest_travel_within_distance"}
        if method=="buddy-quests.accept_shared":
            self.share_calls+=1
            assert params=={"sessionId":self.session,"shareSequence":str(self.share["sequence"]),"sharerGuid":"3","questId":self.share["quest"]["questId"]}
            self.share["acceptRequested"]=True
            if self.share_apply and params["questId"] not in self.active:self.active.add(params["questId"]);self.log_revision+=1
            if self.share_error:raise party.Error("Ambiguous shared acceptance")
            return {"state":self.quests()}
        if method=="buddy-quests.turn_in":
            self.revision+=1
            self.reward={"npcGuid":str(self.npc),"questId":params["questId"],"revision":str(self.revision),
                         "source":"reward_offer","canComplete":True,"expiresInMs":"60000","choices":copy.deepcopy(self.choices)}
        elif method=="buddy-quests.claim_reward":
            assert params["offerRevision"]==self.reward["revision"] and params["questId"]==self.reward["questId"]
            self.reward["rewardRequested"]=True
            if self.apply_claim:
                self.active.remove(params["questId"])
                self.log_revision+=1
                self.receipt={"questId":params["questId"],"status":"REWARD_CONFIRMED","serverRewardObserved":True,"logRemovalConfirmed":True}
        elif method=="buddy-quests.offer":
            self.revision+=1
            self.offer={"npcGuid":str(self.npc),"revision":str(self.revision),"expiresInMs":"60000",
                        "quests":[{"questId":q,"available":True} for q in sorted(self.owner_active-self.active)]}
        elif method=="buddy-quests.accept":
            assert params["offerRevision"]==self.offer["revision"]
            assert params["questId"] in {q["questId"] for q in self.offer["quests"]}
            if self.apply_accept:self.active.add(params["questId"]);self.log_revision+=1
            for q in self.offer["quests"]:
                if q["questId"]==params["questId"]:q["acceptRequested"]=True
        else:raise AssertionError(method)
        if self.error_on==method:raise party.Error("Ambiguous request")
        return {"transportWritten":True,"state":self.quests()}


class PartyQuestTests(unittest.TestCase):
    @staticmethod
    def shared_offer(rpc, **changes):
        rpc.shared_enabled=True
        rpc.share={"sequence":9,"sharerGuid":"3","quest":{"questId":62},
                   "kind":"SHARED_DETAILS","expiresInMs":"28000"}
        rpc.share.update(changes)

    def test_actual_shared_quest_accepts_without_addon_feed_or_npc_range(self):
        profile,rpc,_,_=self.rig()
        self.shared_offer(rpc)
        rpc.age=999999
        rpc.owner_x=-8400
        profile.tick()
        self.assertEqual(rpc.share_calls,1)
        self.assertIn(62,rpc.active)
        self.assertEqual(profile.status["sharedQuestPhase"],"confirmed_in_own_quest_log")
        self.assertFalse(any(method in ("buddy-chat.owner_feed","buddy-actions.travel_quest") for method,_ in rpc.calls))

    def test_alternate_autocomplete_share_uses_the_same_bound_acceptance(self):
        for kind in ("SHARED_AUTOCOMPLETE", 3):
            profile,rpc,_,_=self.rig()
            self.shared_offer(rpc,kind=kind)
            self.assertTrue(profile.poll_shared_quest())
            self.assertEqual(rpc.share_calls,1)
            self.assertIn(62,rpc.active)

    def test_shared_acceptance_ambiguity_is_not_replayed_or_reported_confirmed(self):
        profile,rpc,_,_=self.rig()
        self.shared_offer(rpc)
        rpc.share_error=True;rpc.share_apply=False
        profile.tick();profile.tick()
        self.assertEqual(rpc.share_calls,1)
        self.assertNotIn(62,rpc.active)
        self.assertNotEqual(profile.status["sharedQuestPhase"],"confirmed_in_own_quest_log")

    def test_delayed_actual_log_insertion_confirms_without_another_shared_accept(self):
        profile,rpc,_,_=self.rig()
        self.shared_offer(rpc);rpc.share_apply=False
        profile.poll_shared_quest()
        self.assertEqual(profile.status["sharedQuestPhase"],"waiting_for_server_quest_log")
        rpc.active.add(62)
        self.assertFalse(profile.poll_shared_quest())
        self.assertEqual(rpc.share_calls,1)
        self.assertEqual(profile.status["sharedQuestPhase"],"confirmed_in_own_quest_log")

    def test_unusable_shared_offers_never_write_acceptance(self):
        for change in ({"sharerGuid":"99"},{"kind":"NPC_DETAILS"},
                       {"sequence":0},{"quest":{"questId":0}},{"acceptRequested":True}):
            profile,rpc,_,_=self.rig()
            self.shared_offer(rpc,**change)
            profile.poll_shared_quest()
            self.assertEqual(rpc.share_calls,0,change)

    def test_existing_quest_and_expired_share_reach_source_bound_cleanup(self):
        profile,rpc,_,_=self.rig()
        self.shared_offer(rpc);rpc.active.add(62)
        self.assertTrue(profile.poll_shared_quest())
        self.assertEqual(rpc.share_calls,1)
        self.assertEqual(profile.status["sharedQuestPhase"],"confirmed_in_own_quest_log")
        profile,rpc,_,_=self.rig()
        self.shared_offer(rpc,expiresInMs="0");rpc.share_apply=False
        self.assertTrue(profile.poll_shared_quest())
        self.assertEqual(rpc.share_calls,1)
        self.assertNotIn(62,rpc.active)

    def test_busy_quest_operation_can_recheck_share_without_consuming_it(self):
        profile,rpc,_,_=self.rig()
        self.shared_offer(rpc)
        original=rpc.quests
        rpc.quests=lambda:dict(original(),actionInFlight=True)
        self.assertFalse(profile.poll_shared_quest())
        self.assertEqual(rpc.share_calls,0)
        rpc.quests=original
        self.assertTrue(profile.poll_shared_quest())
        self.assertEqual(rpc.share_calls,1)

    def test_area_sweep_defers_npc_sync_while_shared_quest_polling_stays_available(self):
        profile,rpc,_,_=self.rig()
        with patch.object(party.converse,"area_quest_snapshot",return_value={"active":True},create=True):
            profile.tick()
        self.assertEqual(profile.status["phase"],"waiting_for_requested_area_quest_sweep")
        self.assertFalse(self.mutations(rpc))
        self.shared_offer(rpc)
        with patch.object(party.converse,"area_quest_snapshot",return_value={"active":True},create=True):
            profile.tick()
        self.assertEqual(rpc.share_calls,1)

    def rig(self,guid=1,stored=None):
        mock=patch.object(party.converse,"quest_sync_snapshot",return_value={"active":False})
        mock.start();self.addCleanup(mock.stop)
        rpc,clock,saved=FakeRPC(guid),[1000.],[]
        profile=party.PartyQuestSync({1:"Arlen",4:"Mira",5:"Rook"}[guid],guid,rpc,
            save=lambda data:saved.append(copy.deepcopy(data)),stored=stored,now=lambda:clock[0])
        return profile,rpc,clock,saved

    def mutations(self,rpc):return [(m,p) for m,p in rpc.calls if m not in (
        "buddy-world.snapshot","buddy-quests.discover","buddy-quests.state","buddy-actions.status","buddy-party.state","buddy-chat.owner_feed","buddy-trade.lookup_items")]

    def test_owner_completed_own_complete_handoff_then_actual_owner_followup_accepts(self):
        profile,rpc,clock,_=self.rig()
        for _ in range(12):
            profile.tick();clock[0]+=10
            if profile.status.get("activeQuestIds")==[15]:break
        self.assertEqual([m for m,_ in self.mutations(rpc)],["buddy-quests.turn_in","buddy-quests.claim_reward","buddy-quests.offer","buddy-quests.accept"])
        self.assertEqual(profile.status["activeQuestIds"],[15])
        self.assertEqual(profile.status["confirmedRewardQuestIds"],[7])
        self.assertTrue(all("abandon" not in m and "follow" not in m for m,_ in rpc.calls))

    def test_owner_absence_is_not_reward_proof_and_own_incomplete_never_turns_in(self):
        for mode in ("absent_only","own_incomplete","wrong_party","stale","assembling","owner_combat","own_combat","far_owner"):
            profile,rpc,_,_=self.rig()
            rpc.owner_active=set()
            if mode=="absent_only":rpc.owner_done=set()
            elif mode=="own_incomplete":rpc.complete=set()
            elif mode=="wrong_party":rpc.good_party=False
            elif mode=="stale":rpc.age=15001
            elif mode=="assembling":rpc.assembling=True
            elif mode=="owner_combat":rpc.owner_combat=True
            elif mode=="own_combat":rpc.own_combat=True
            elif mode=="far_owner":rpc.owner_x=-8800
            profile.tick()
            self.assertFalse(self.mutations(rpc),mode)

    def test_initial_completed_query_subsets_allow_catchup_without_reward_edge(self):
        profile,rpc,_,_=self.rig()
        rpc.owner_active=set()
        profile.tick()
        self.assertEqual(self.mutations(rpc)[0][0],"buddy-quests.turn_in")
        self.assertEqual(self.mutations(rpc)[0][1]["questId"],7)

    def test_stale_feed_just_before_mutation_or_body_replacement_prevents_write(self):
        for mode in ("stale","body"):
            profile,rpc,_,_=self.rig()
            def change(rpc):
                if rpc.feed_reads>=2:
                    if mode=="stale":rpc.age=15001
                    else:rpc.session="replacement-body"
            rpc.feed_change=change
            profile.tick()
            self.assertFalse(self.mutations(rpc),mode)

    def test_observed_approach_releases_lease_then_revalidates_same_logical_gossip(self):
        profile,rpc,_,_=self.rig()
        rpc.self_x=-8912
        profile.tick()
        self.assertEqual([m for m,_ in self.mutations(rpc)],["buddy-actions.acquire_interaction","buddy-actions.travel_quest","buddy-actions.release_interaction","buddy-quests.turn_in"])
        self.assertEqual(next(p for m,p in rpc.calls if m=="buddy-actions.travel_quest"),{"token":"owned-lease","npcGuid":str(MCBRIDE)})
        self.assertGreaterEqual(rpc.feed_reads,2)

    def test_owner_leaves_during_approach_or_cooperative_stop_still_releases(self):
        profile,rpc,_,_=self.rig()
        rpc.self_x=-8912
        rpc.after_travel=lambda rpc:setattr(rpc,"owner_present",False)
        profile.tick()
        self.assertFalse(any(m.startswith("buddy-quests.") and m not in ("buddy-quests.discover", "buddy-quests.state") for m,_ in rpc.calls))
        self.assertIn(("buddy-actions.release_interaction",{"owner":"quests","token":"owned-lease"}),rpc.calls)
        profile,rpc,_,_=self.rig()
        rpc.self_x=-8912
        original=rpc.call
        def call(method,params):
            result=original(method,params)
            if method=="buddy-actions.acquire_interaction":profile.stopped=lambda:True
            return result
        rpc.call=call
        with self.assertRaises(party.converse.Stopped):profile.tick()
        self.assertEqual(rpc.calls[-1],("buddy-actions.release_interaction",{"owner":"quests","token":"owned-lease"}))

    def test_claim_timeout_never_replays_same_offer_and_transport_never_confirms(self):
        profile,rpc,clock,_=self.rig()
        rpc.owner_active=set();rpc.apply_claim=False
        profile.tick();clock[0]+=10
        rpc.error_on="buddy-quests.claim_reward"
        profile.tick()
        rpc.reward["rewardRequested"]=False
        clock[0]+=10;profile.tick()
        self.assertEqual(len([m for m,_ in rpc.calls if m=="buddy-quests.claim_reward"]),1)
        self.assertEqual(profile.status["confirmedRewardQuestIds"],[])
        clock[0]+=60;profile.tick();clock[0]+=10;profile.tick()
        claims=[p for m,p in rpc.calls if m=="buddy-quests.claim_reward"]
        self.assertEqual(len(claims),2)
        self.assertNotEqual(claims[0]["offerRevision"],claims[1]["offerRevision"])

    def test_verified_northshire_role_choices_bind_actual_index_and_item(self):
        for guid,wanted in ((1,5580),(4,1159),(5,2224)):
            profile,rpc,clock,_=self.rig(guid)
            rpc.npc,rpc.entry=WILLEM,823
            rpc.active,rpc.complete,rpc.owner_active,rpc.owner_done={18},{18},set(),{18}
            rpc.choices=[{"index":5,"itemId":2224,"count":1},{"index":3,"itemId":1159,"count":1},{"index":0,"itemId":5580,"count":1}]
            profile.tick();clock[0]+=10;profile.tick()
            claim=next(p for m,p in rpc.calls if m=="buddy-quests.claim_reward")
            self.assertEqual(claim["expectedItemId"],wanted)
            self.assertEqual(claim["choiceIndex"],{5580:0,1159:3,2224:5}[wanted])

    def test_quest21_role_choices_are_actual_cloth_or_leather_never_mail(self):
        for guid,wanted in ((1,11192),(4,11192),(5,2186)):
            profile,rpc,clock,_=self.rig(guid)
            rpc.active,rpc.complete,rpc.owner_active,rpc.owner_done={21},{21},set(),{21}
            rpc.choices=[{"index":2,"itemId":2691,"count":1},{"index":4,"itemId":2186,"count":1},{"index":1,"itemId":11192,"count":1}]
            profile.tick();clock[0]+=10;profile.tick()
            claim=next(params for method,params in rpc.calls if method=="buddy-quests.claim_reward")
            self.assertEqual(claim["expectedItemId"],wanted)
            self.assertEqual(claim["choiceIndex"],1 if wanted==11192 else 4)

    def test_unknown_multiple_choices_pause_and_do_not_overwrite_actual_offer(self):
        profile,rpc,clock,_=self.rig()
        rpc.choices=[{"index":0,"itemId":123,"count":1},{"index":1,"itemId":456,"count":1}]
        profile.tick();clock[0]+=10;profile.tick()
        self.assertEqual(profile.status["phase"],"waiting_for_reward_item_details")
        self.assertEqual(profile.status["rewardChoiceItemIds"],[123,456])
        self.assertFalse(any(m in ("buddy-quests.claim_reward","buddy-quests.offer") for m,_ in rpc.calls))

    def queried_rewards(self,choices=None):
        profile,rpc,clock,saved=self.rig()
        rpc.choices=choices if choices is not None else [{"index":4,"itemId":6076,"count":1},{"index":2,"itemId":3070,"count":1}]
        rows={6076:{"itemId":6076,"available":True,"name":"Tapered Pants","itemClass":4,"subclass":2,
                    "inventoryType":7,"armor":25,"allowableClass":0xffffffff,"allowableRace":0xffffffff},
              3070:{"itemId":3070,"available":True,"name":"Ensign Cloak","itemClass":4,"subclass":1,
                    "inventoryType":16,"armor":6,"allowableClass":0xffffffff,"allowableRace":0xffffffff}}
        profile.tick();clock[0]+=10
        return profile,rpc,clock,saved,rows

    def test_proto_json_omitted_zero_reward_index_claims_exact_offered_item_once(self):
        for reverse in (False,True):
            with self.subTest(reverse=reverse):
                choices=[{'itemId':3070,'count':1},{'index':4,'itemId':6076,'count':1}]
                if reverse:choices.reverse()
                profile,rpc,clock,_,rows=self.queried_rewards(choices)
                before=copy.deepcopy(rpc.reward)
                rpc.item_details=rows;profile.tick()
                claims=[params for method,params in rpc.calls if method=='buddy-quests.claim_reward']
                self.assertEqual(len(claims),1)
                claim=claims[0]
                self.assertEqual((claim['sessionId'],claim['offerRevision'],claim['questId'],claim['noChoice'],claim['choiceIndex'],claim['expectedItemId']),
                                 (rpc.session,before['revision'],7,False,0,3070))
                self.assertEqual(rpc.reward['choices'],before['choices'])
                self.assertNotIn('index',next(row for row in rpc.reward['choices'] if row['itemId']==3070))
                clock[0]+=10;profile.tick()
                self.assertEqual(profile.status['confirmedRewardQuestIds'],[7])
                self.assertEqual(sum(method=='buddy-quests.claim_reward' for method,_ in rpc.calls),1)

    def test_omitted_zero_index_is_an_actual_offer_slot_not_an_arbitrary_default_choice(self):
        profile,rpc,_,_=self.rig()
        with self.assertRaises(party.Error):
            profile.choice({'questId':7,'choices':[{'itemId':3070,'count':1},{'index':0,'itemId':6076,'count':1}]},rpc.world()['self'])
        self.assertFalse(any(method=='buddy-quests.claim_reward' for method,_ in rpc.calls))
        self.assertEqual(profile.choice({'questId':7,'choices':[{'itemId':3070,'count':1}]},rpc.world()['self']),
                         {'index':0,'itemId':3070,'count':1})

    def test_metadata_pending_has_no_claim_reservation_or_cooldown_then_ready_claims_exact_choice(self):
        profile,rpc,clock,saved,rows=self.queried_rewards()
        before=(profile.data["nextMutationAt"],copy.deepcopy(profile.data["reservations"]),len(saved))
        profile.tick()
        self.assertEqual(profile.status["phase"],"waiting_for_reward_item_details")
        self.assertEqual((profile.data["nextMutationAt"],profile.data["reservations"],len(saved)),before)
        self.assertFalse(any(method=="buddy-quests.claim_reward" for method,_ in rpc.calls))
        rpc.item_details=rows
        clock[0]+=2;profile.tick()
        claims=[params for method,params in rpc.calls if method=="buddy-quests.claim_reward"]
        self.assertEqual(len(claims),1)
        self.assertEqual((claims[0]["sessionId"],claims[0]["offerRevision"],claims[0]["choiceIndex"],claims[0]["expectedItemId"]),
                         (rpc.session,rpc.reward["revision"],2,3070))
        self.assertEqual(profile.status["rewardChoiceName"],"Ensign Cloak")
        clock[0]+=10;profile.tick()
        self.assertIn("7",profile.data["confirmedRewards"])
        self.assertEqual(sum(method=="buddy-quests.claim_reward" for method,_ in rpc.calls),1)

    def test_invalid_lookup_metadata_or_identity_never_reserves_claim(self):
        for change in ("metadata","identity","unavailable"):
            with self.subTest(change=change):
                profile,rpc,clock,saved,rows=self.queried_rewards()
                rpc.item_details=rows
                if change=="metadata":rows[3070]["stats"]=[{"type":5,"value":True}]
                if change=="unavailable":rows[3070]["available"]=False
                original=rpc.call
                def call(method,params):
                    result=original(method,params)
                    if method=="buddy-trade.lookup_items" and change=="identity":result["sessionId"]=""
                    return result
                rpc.call=call
                before=(profile.data["nextMutationAt"],copy.deepcopy(profile.data["reservations"]),len(saved))
                profile.tick()
                self.assertEqual((profile.data["nextMutationAt"],profile.data["reservations"],len(saved)),before)
                self.assertFalse(any(method=="buddy-quests.claim_reward" for method,_ in rpc.calls))

    def test_new_item_lookup_then_changed_body_offer_or_owner_never_claims(self):
        for change in ("body","offer","expired","combat","feed"):
            with self.subTest(change=change):
                profile,rpc,clock,_,rows=self.queried_rewards()
                rpc.item_details=rows
                def after_lookup(mock):
                    if change=="body":mock.session="replacement"
                    elif change=="offer":mock.reward["revision"]="99"
                    elif change=="expired":mock.reward.pop("expiresInMs")
                    elif change=="combat":mock.own_combat=True
                    elif change=="feed":mock.age=16000
                rpc.after_lookup=after_lookup
                profile.tick()
                self.assertFalse(any(method=="buddy-quests.claim_reward" for method,_ in rpc.calls))

    def test_only_owner_active_quests_from_actual_offer_and_fresh_menu_each_accept(self):
        profile,rpc,clock,_=self.rig()
        rpc.active,rpc.complete,rpc.owner_done=set(),set(),set()
        rpc.owner_active={15,21}
        for _ in range(8):profile.tick();clock[0]+=10
        accepts=[p for m,p in rpc.calls if m=="buddy-quests.accept"]
        self.assertEqual([p["questId"] for p in accepts],[15,21])
        self.assertNotEqual(accepts[0]["offerRevision"],accepts[1]["offerRevision"])
        self.assertEqual(rpc.active,{15,21})

    def test_persist_failure_prevents_any_game_mutation_and_restart_reservations_bind_identity(self):
        profile,rpc,_,saved=self.rig()
        profile.save=lambda data:(_ for _ in ()).throw(OSError("disk full"))
        with self.assertRaises(OSError):profile.tick()
        self.assertFalse(self.mutations(rpc))
        with self.assertRaises(party.Error):self.rig(stored={"identity":{"wrong":True}})

    def test_accept_timeout_restart_requires_actual_log_or_new_offer(self):
        profile,rpc,clock,_=self.rig()
        rpc.active,rpc.complete,rpc.owner_done=set(),set(),set()
        profile.tick();clock[0]+=10
        rpc.apply_accept=False;rpc.error_on="buddy-quests.accept"
        profile.tick()
        saved=copy.deepcopy(profile.data)
        recovered=party.PartyQuestSync("Arlen",1,rpc,stored=saved,now=lambda:clock[0])
        clock[0]+=10;recovered.tick()
        self.assertEqual(len([m for m,_ in rpc.calls if m=="buddy-quests.accept"]),1)
        self.assertEqual(recovered.status["activeQuestIds"],[])
        clock[0]+=30;recovered.tick();clock[0]+=10
        rpc.error_on=None;rpc.apply_accept=True;recovered.tick()
        accepts=[p for m,p in rpc.calls if m=="buddy-quests.accept"]
        self.assertEqual(len(accepts),2)
        self.assertNotEqual(accepts[0]["offerRevision"],accepts[1]["offerRevision"])
        self.assertEqual(rpc.active,{15})

    def test_reward_confirmation_requires_both_server_receipt_and_log_removal(self):
        for server,removed,still_active in ((False,True,False),(True,False,False),(True,True,True)):
            profile,rpc,_,_=self.rig()
            rpc.active={7} if still_active else set()
            rpc.owner_done=set();rpc.owner_active=set()
            rpc.receipt={"questId":7,"status":"REWARD_CONFIRMED","serverRewardObserved":server,"logRemovalConfirmed":removed}
            profile.tick()
            self.assertEqual(profile.status["confirmedRewardQuestIds"],[])

    def test_owner_changes_quest_intent_before_accept_prevents_old_offer_write(self):
        profile,rpc,_,_=self.rig()
        rpc.active,rpc.complete,rpc.owner_done=set(),set(),set()
        rpc.offer={"npcGuid":str(MCBRIDE),"revision":"2","expiresInMs":"60000",
                   "quests":[{"questId":15,"available":True}]}
        rpc.feed_change=lambda rpc:setattr(rpc,"owner_active",set()) if rpc.feed_reads>=2 else None
        profile.tick()
        self.assertFalse(self.mutations(rpc))

    def test_route_failure_releases_owned_lease_and_never_gossips(self):
        profile,rpc,_,_=self.rig()
        rpc.self_x=-8912
        original=rpc.call
        def call(method,params):
            if method=="buddy-actions.travel_quest":
                rpc.calls.append((method,params))
                raise party.Error("Route not completed")
            return original(method,params)
        rpc.call=call
        profile.tick()
        self.assertEqual([m for m,_ in self.mutations(rpc)],["buddy-actions.acquire_interaction","buddy-actions.travel_quest","buddy-actions.release_interaction"])
        self.assertEqual(profile.status["phase"],"request_unconfirmed_reconciling_fresh_state")

    def test_lease_acquisition_rechecks_body_owner_and_feed_before_any_travel(self):
        for mode in ("body","owner","feed","intent"):
            profile,rpc,_,_=self.rig()
            rpc.self_x=-8912
            original=rpc.call
            def call(method,params):
                result=original(method,params)
                if method=="buddy-actions.acquire_interaction":
                    if mode=="body":rpc.session="replacement-body"
                    elif mode=="owner":rpc.owner_x=-8800
                    elif mode=="feed":rpc.age=15001
                    else:rpc.owner_done=set()
                return result
            rpc.call=call
            profile.tick()
            self.assertEqual([m for m,_ in self.mutations(rpc)],
                ["buddy-actions.acquire_interaction","buddy-actions.release_interaction"],mode)

    def test_unproven_reward_state_and_malformed_choices_are_rejected(self):
        profile,rpc,_,_=self.rig()
        stored=copy.deepcopy(profile.data)
        stored["confirmedRewards"]={"7":{"questId":7,"session":"session","serverRewardObserved":False,"logRemovalConfirmed":True}}
        with self.assertRaises(party.Error):self.rig(stored=stored)
        for choices in ([{"index":6,"itemId":80,"count":1}],
                        [{"index":0,"itemId":80,"count":1},{"index":0,"itemId":6070,"count":1}],
                        [{"index":0,"itemId":80,"count":0}]):
            with self.subTest(choices=choices),self.assertRaises(party.Error):
                profile.choice({"questId":33,"choices":choices},rpc.world()["self"])

    def test_expired_or_replaced_bound_offer_before_travel_never_moves(self):
        for purpose in ("accept","claim"):
            for change in ("expiresInMs","revision"):
                profile,rpc,_,_=self.rig()
                rpc.self_x=-8912
                if purpose=="accept":
                    rpc.active,rpc.complete,rpc.owner_done=set(),set(),set()
                    rpc.offer={"npcGuid":str(MCBRIDE),"revision":"2","expiresInMs":"60000",
                               "quests":[{"questId":15,"available":True}]}
                    offer=rpc.offer
                else:
                    rpc.reward={"npcGuid":str(MCBRIDE),"questId":7,"revision":"2","expiresInMs":"60000",
                                "source":"reward_offer","canComplete":True,"choices":[]}
                    offer=rpc.reward
                original=rpc.call
                def call(method,params):
                    result=original(method,params)
                    if method=="buddy-actions.acquire_interaction":offer[change]="0" if change=="expiresInMs" else "3"
                    return result
                rpc.call=call
                profile.tick()
                self.assertEqual([m for m,_ in self.mutations(rpc)],
                    ["buddy-actions.acquire_interaction","buddy-actions.release_interaction"],(purpose,change))

    def test_cooperative_cli_stop_binds_locked_owned_instance(self):
        with tempfile.TemporaryDirectory() as temp:
            folder=party.converse.ensure_state(Path(temp)/"party-quests")
            record={"identity":party.IDENTITY,"instance":"c"*32,"routineActive":True,"phase":"running"}
            party.converse.write_json(folder/"status.json",record)
            with os.fdopen(party.converse.private_open(folder/"lock",os.O_RDWR|os.O_CREAT),"r+") as lock:
                party.fcntl.flock(lock,party.fcntl.LOCK_EX)
                with contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(party.main(["stop","--state-dir",str(folder)]),0)
                self.assertEqual(party.converse.read_json(folder/"stop.json"),{"identity":party.IDENTITY,"instance":"c"*32})
                record.pop("instance");party.converse.write_json(folder/"status.json",record)
                with self.assertRaises(party.Error):party.main(["stop","--state-dir",str(folder)])

    def test_expired_run_records_stopped_without_any_rpc_and_status_is_readable(self):
        with tempfile.TemporaryDirectory() as temp:
            folder=Path(temp)/"party-quests"
            with patch.object(party.time,"monotonic",side_effect=(0,100000)),patch.object(party.signal,"signal"),\
                    patch.object(party.sync,"SyncRPC") as rpc,contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(party.main(["run","--state-dir",str(folder)]),0)
                self.assertFalse(rpc.return_value.call.called)
            record=party.converse.read_json(folder/"status.json")
            self.assertFalse(record["routineActive"])
            self.assertEqual(record["phase"],"stopped")
            self.assertEqual(record["identity"],party.IDENTITY)
            output=io.StringIO()
            with contextlib.redirect_stdout(output):party.main(["status","--state-dir",str(folder)])
            self.assertFalse(json.loads(output.getvalue())["running"])
            self.assertEqual((folder/"status.json").stat().st_mode&0o777,0o600)

    def test_private_state_rejects_symlink_oversize_and_other_routine(self):
        with tempfile.TemporaryDirectory() as temp:
            path=Path(temp)/"state.json"
            path.write_text("{}"*40000);path.chmod(0o600)
            with self.assertRaises(party.Error):party.load_private(path)
            path.write_text("{}")
            link=Path(temp)/"linked.json";link.symlink_to(path)
            with self.assertRaises(OSError):party.load_private(link)
            folder=party.converse.ensure_state(Path(temp)/"party-quests")
            party.converse.write_json(folder/"status.json",{"identity":{"other":True}})
            with self.assertRaises(party.Error):party.main(["stop","--state-dir",str(folder)])


if __name__=="__main__":unittest.main(verbosity=2)
