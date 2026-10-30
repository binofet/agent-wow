#!/usr/bin/env python3
"""Standing town upkeep tests use fake observed public-client state only."""
import copy
import fcntl
import importlib.machinery
import importlib.util
import tempfile
import threading
import os
from pathlib import Path
import unittest
from unittest.mock import patch

def load(name,file):
    loader=importlib.machinery.SourceFileLoader(name,str(Path(__file__).with_name(file)))
    spec=importlib.util.spec_from_loader(loader.name,loader)
    module=importlib.util.module_from_spec(spec);loader.exec_module(module);return module

m=load("party_town_tested","party-town")
finite=load("party_town_finite_tests","test_town_maintenance.py")
VENDOR,MAILBOX=finite.VENDOR,finite.MAILBOX

class RPC:
    url="http://127.0.0.1:8086/rpc"
    def __init__(self):
        self.inv=finite.initial();self.inv["selfGuid"]="1"
        # Leave only a real gray stack and an ordinary item initially.
        self.inv["inventoryBags"][0]["slots"][2]={"slot":2}
        self.inv["items"]=[item for item in self.inv["items"] if item["item"]["slot"] != 2]
        self.calls,self.x,self.following,self.owner_x=[],0.,True,10.
        self.owner_present,self.combat,self.owner_combat,self.moving,self.resting=True,False,False,False,True
        self.owner_moving,self.trade,self.interaction,self.lease=False,"IDLE","",None
        self.vendor_x,self.vendor_present,self.box,self.block=20.,True,False,None
        self.mail=dict(sessionId="body1",listKnown=True,listCurrent=True,bagMails=[dict(mailId=7,
            attachments=[dict(attachmentIndex=0,itemGuidLow=123,entry=41599,count=1)])])
        self.before_acquire=None
    def fork(self):return self
    def player(self,guid,x):
        return dict(guid=str(guid),present=True,mapKnown=True,mapId=0,positionKnown=True,x=x,y=0.,z=0.,
            aliveKnown=True,alive=True,healthKnown=True,health=100,unitFlagsKnown=True,unitFlags=0,
            movementFlagsKnown=True,movementFlags=0,playerFlags=0x20 if self.resting else 0)
    def vendor(self):
        return dict(self.player(VENDOR,self.vendor_x),entry=VENDOR>>24&0xffffff,npcFlags=128)
    def call(self,method,params):
        self.calls.append((method,copy.deepcopy(params)))
        if method=="buddy-inventory.state":return copy.deepcopy(self.inv)
        if method=="buddy-world.snapshot":
            own=self.player(1,self.x);own["unitFlags"]=0x80000 if self.combat else 0
            owner=self.player(3,self.owner_x);owner.update(present=self.owner_present,unitFlags=0x80000 if self.owner_combat else 0,
                                                       movementFlags=1 if self.owner_moving else 0)
            boxes=[dict(guid=str(MAILBOX),entry=MAILBOX>>24&0xffffff,present=True,typeKnown=True,gameObjectType=19,
                        mapKnown=True,mapId=0,positionKnown=True,x=2.,y=0.,z=0.)] if self.box else []
            return dict(self=own,owner=owner,mailboxes=boxes)
        if method=="buddy-actions.status":return dict(following=self.following,moving=self.moving,interactionOwner=self.interaction,
            position=dict(known=True,mapKnown=True,mapId=0,x=self.x,y=0.,z=0.))
        if method=="buddy-trade.state":return dict(sessionId=self.inv["sessionId"],phase=self.trade)
        if method in ("buddy-quests.state","buddy-loot.state"):return dict(sessionId=self.inv["sessionId"])
        if method=="buddy-inventory.discover":return dict(state=copy.deepcopy(self.inv),nearby=[dict(creature=self.vendor())] if self.vendor_present else [])
        if method=="buddy-mail.list":return copy.deepcopy(self.mail)
        if method=="buddy-actions.follow":self.following=params["enabled"];return dict(following=self.following)
        if method=="buddy-actions.acquire_interaction":
            self.lease="exact-owned-trade-token";self.interaction="trade"
            if self.before_acquire:self.before_acquire(self)
            return dict(token=self.lease,state=dict(interactionOwner="trade"))
        if method=="buddy-actions.travel_vendor":
            assert params==dict(token=self.lease,npcGuid=str(VENDOR))
            if self.block:self.block.wait(3)
            else:self.x=self.vendor_x-2
            return dict(reason="vendor_travel_within_distance")
        if method=="buddy-actions.release_interaction":
            assert params==dict(owner="trade",token=self.lease)
            self.lease,self.interaction=None,""
            if self.block:self.block.set()
            return {}
        raise AssertionError(method)

class TownTests(unittest.TestCase):
    def setUp(self):
        self.patches=[patch.object(m.c,"quest_task_snapshot",return_value={"active":False}),
                      patch.object(m.c,"area_quest_snapshot",return_value={"active":False}),
                      patch.object(m,"profession_active",return_value=False)]
        for p in self.patches:p.start();self.addCleanup(p.stop)
    def rig(self,worker=None,stored=None,revoked=lambda:False):
        rpc,clock,saved,calls=RPC(),[100.],[],[]
        def finite_worker(kind,target):
            self.assertIsNone(rpc.lease)
            self.assertFalse(rpc.following)
            calls.append((kind,target))
            return dict(phase="complete",newConfirmedOperations=1)
        actor=m.Supervisor(1,rpc,save=lambda value:saved.append(copy.deepcopy(value)),stored=stored,
                           now=lambda:clock[0],worker=worker or finite_worker,revoked=revoked)
        return actor,rpc,clock,saved,calls
    def run_tick(self,actor,clock):actor.tick();clock[0]+=5
    def test_resting_vendor_detour_uses_only_observed_vendor_and_releases_before_worker(self):
        actor,rpc,clock,saved,calls=self.rig()
        self.run_tick(actor,clock);self.run_tick(actor,clock)
        self.assertEqual(calls,[("sell",VENDOR)])
        self.assertTrue(rpc.following)
        self.assertEqual(saved[0]["attempts"][0]["phase"],"reserved")
        self.assertEqual(actor.status["confirmedOperations"],1)
        self.run_tick(actor,clock)
        # The first detour was travel; a changed local trigger can run once.
        self.run_tick(actor,clock)
        count=len(calls);self.run_tick(actor,clock);self.assertEqual(len(calls),count)
    def test_no_town_inference_or_unobserved_vendor_or_long_detour(self):
        for mode in ("no_rest","missing_vendor","distant","invalid_guid","no_junk"):
            actor,rpc,clock,_,calls=self.rig()
            if mode=="no_rest":rpc.resting=False
            elif mode=="missing_vendor":rpc.vendor_present=False
            elif mode=="distant":rpc.vendor_x=36
            elif mode=="invalid_guid":rpc.vendor=lambda:dict(rpc.player(3,20),entry=0,npcFlags=128)
            else:rpc.inv["items"] = []
            self.run_tick(actor,clock);self.run_tick(actor,clock)
            self.assertEqual(calls,[],mode)
            self.assertFalse(any("travel_vendor" in method for method,_ in rpc.calls),mode)
    def test_in_range_or_near_full_can_sell_without_resting(self):
        for full in (False,True):
            actor,rpc,clock,_,calls=self.rig();rpc.resting=False
            if full:
                for slot in rpc.inv["inventoryBags"][0]["slots"]:
                    if not int(slot.get("itemGuid",0)):
                        n=slot.get("slot",0)
                        slot.update(itemGuid=str(0x4000000000010000+n),entry=10000+n,stackCount=1,metadataKnown=True)
            else:rpc.vendor_x=3
            self.run_tick(actor,clock);self.run_tick(actor,clock)
            self.assertTrue(calls)
    def test_requested_routines_combat_motion_unknown_inventory_and_trade_defer(self):
        for mode in ("quest","area","professions","self_combat","owner_combat","owner_moving","self_moving","trade","interaction","unknown"):
            actor,rpc,clock,_,calls=self.rig()
            patcher=None
            if mode=="quest":patcher=patch.object(m.c,"quest_task_snapshot",return_value={"active":True})
            elif mode=="area":patcher=patch.object(m.c,"area_quest_snapshot",return_value={"active":True})
            elif mode=="professions":patcher=patch.object(m,"profession_active",return_value=True)
            elif mode=="self_combat":rpc.combat=True
            elif mode=="owner_combat":rpc.owner_combat=True
            elif mode=="owner_moving":rpc.owner_moving=True
            elif mode=="self_moving":rpc.moving=True
            elif mode=="trade":rpc.trade="OPEN"
            elif mode=="interaction":rpc.interaction="quests"
            else:rpc.inv["inventoryKnown"]=False
            if patcher:patcher.start()
            try:
                for _ in range(2):
                    try:self.run_tick(actor,clock)
                    except (m.Error,m.town.Error):pass
                self.assertEqual(calls,[],mode)
                self.assertFalse(any(method=="buddy-actions.follow" for method,_ in rpc.calls),mode)
            finally:
                if patcher:patcher.stop()
    def test_ambiguous_outcome_is_durable_and_not_repeated(self):
        calls=[]
        def uncertain(kind,target):calls.append((kind,target));return dict(phase="transport_unknown")
        actor,rpc,clock,saved,_=self.rig(worker=uncertain);rpc.vendor_x=3
        self.run_tick(actor,clock);self.run_tick(actor,clock);self.run_tick(actor,clock)
        self.assertEqual(len(calls),1)
        resumed=m.Supervisor(1,rpc,stored=saved[-1],now=lambda:clock[0],worker=uncertain)
        self.run_tick(resumed,clock);self.run_tick(resumed,clock)
        self.assertEqual(len(calls),1)
    def test_owner_moves_or_returns_during_vendor_travel_cancel_exact_token(self):
        for mode in ("moves","returns","combat"):
            actor,rpc,clock,_,calls=self.rig();rpc.block=threading.Event()
            if mode=="returns":rpc.owner_present=False
            else:self.run_tick(actor,clock)
            def change():
                if mode=="moves":rpc.owner_x+=2
                elif mode=="returns":rpc.owner_present=True
                else:rpc.owner_combat=True
            timer=threading.Timer(.05,change);timer.start()
            try:self.run_tick(actor,clock)
            finally:timer.join()
            self.assertIsNone(rpc.lease);self.assertTrue(rpc.block.is_set());self.assertEqual(calls,[])
            self.assertTrue(rpc.following)
    def test_explicit_follow_revocation_prevents_restore_and_stops_travel(self):
        stop=threading.Event()
        actor,rpc,clock,_,calls=self.rig(revoked=stop.is_set);rpc.block=threading.Event()
        self.run_tick(actor,clock)
        timer=threading.Timer(.05,stop.set);timer.start()
        try:self.run_tick(actor,clock)
        finally:timer.join()
        self.assertFalse(rpc.following);self.assertIsNone(rpc.lease);self.assertEqual(calls,[])
    def test_follow_command_during_cleanup_read_is_not_overwritten(self):
        stop=threading.Event()
        actor,rpc,clock,_,_=self.rig(revoked=stop.is_set);rpc.vendor_x=3
        finished=[False]
        def worker(kind,target):finished[0]=True;return dict(phase="complete")
        actor.worker=worker
        original=actor.read
        def read():
            result=original()
            if finished[0]:stop.set()
            return result
        actor.read=read
        self.run_tick(actor,clock);self.run_tick(actor,clock)
        self.assertFalse(rpc.following)
        self.assertFalse(any(method=="buddy-actions.follow" and params["enabled"] for method,params in rpc.calls))
    def test_bag_mail_trigger_only_actual_in_range_exact_attachments(self):
        for mode in ("good","foreign","wrong_item","far"):
            actor,rpc,clock,_,calls=self.rig();rpc.box=True;rpc.inv["items"]=[]
            if mode=="foreign":rpc.mail["sessionId"]="foreign"
            elif mode=="wrong_item":rpc.mail["bagMails"][0]["attachments"][0]["entry"]=123
            elif mode=="far":rpc.x=20
            self.run_tick(actor,clock);self.run_tick(actor,clock)
            self.assertEqual(bool(calls),mode=="good",mode)
            self.assertFalse(any(method=="buddy-actions.travel_vendor" for method,_ in rpc.calls))
    def test_all_bag_context_capacity_items_and_names_are_observed_bounded(self):
        state=finite.initial();state["selfGuid"]="1"
        for bag in state["inventoryBags"][1:]:
            b=bag["bag"]
            bag.update(containerGuid=str(0x4000000000090000+b),entry=41599,metadataKnown=True,slotCount=20,
                       slots=[dict(bag=b,slot=s) for s in range(20)])
        value=m.c.inventory_context(state,1,{41599:"Frostweave Bag"})
        self.assertEqual(value["totalSlots"],96)
        self.assertEqual(value["freeSlots"],94)
        self.assertEqual(len(value["bags"]),4)
        self.assertEqual(next(row for row in value["items"] if row.get("entry")==41599)["name"],"Frostweave Bag")
        state["inventoryBags"][2]["bag"]=1
        with self.assertRaises(m.Error):m.c.inventory_context(state,1)
    def test_fixed_identity_cli_bound_and_guarded_worker_never_mutates_when_owner_moves(self):
        with self.assertRaises(m.Error):m.identity(3)
        with self.assertRaises(m.Error):m.Supervisor(4,RPC())
        with self.assertRaises(SystemExit):m.main(["run","--character-guid","1","--max-hours","9"])
        actor,rpc,clock,_,_=self.rig();actor.session="body1";actor.owner_anchor=dict(mapId=0,x=10.,y=0.,z=0.)
        rpc.owner_x=12
        backend=finite.FakeRPC()
        guard=m.GuardedWorkerRPC(actor,backend)
        with self.assertRaises(m.Error):guard.call("buddy-inventory.sell",{})
        self.assertFalse(backend.mutations())
    def test_operations_above_eight_kib_reload_with_same_sixty_four_kib_bound(self):
        actor,rpc,_,_,_=self.rig()
        for index in range(64):
            actor.data["attempts"].append(dict(key=str(index)+"x"*220,sessionId="body1",phase="complete"))
        with tempfile.TemporaryDirectory() as temporary:
            path=Path(temporary)/"operations.json"
            m.c.write_json(path,actor.data)
            self.assertGreater(path.stat().st_size,8192)
            restored=m.Supervisor(1,rpc,stored=m.load_operations(path))
            self.assertEqual(restored.data,actor.data)
            path.write_text("x"*65537)
            with self.assertRaises(m.Error):m.load_operations(path)
    def test_private_status_follow_marker_is_bound_to_exact_running_activity(self):
        with tempfile.TemporaryDirectory() as temporary,patch.object(m.c,"ROOT",Path(temporary)):
            folder=m.c.ensure_state(m.c.ensure_state(Path(temporary)/"data/party-town")/"1")
            record=dict(identity=m.identity(1),instance="a"*32,routineActive=True,activityId="b"*32,
                        own=dict(busy=True,phase="traveling_to_observed_vendor"))
            m.c.write_json(folder/"status.json",record)
            with os.fdopen(m.c.private_open(folder/"lock",os.O_RDWR|os.O_CREAT),"r+") as lock:
                fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
                value=m.c.party_town_snapshot(1,revoke_follow=True)
                self.assertTrue(value["followRevoked"])
                self.assertEqual(m.c.read_json(folder/"follow-revoked.json"),dict(identity=m.identity(1),instance="a"*32,activityId="b"*32))
                record["identity"]["characterGuid"]=4;m.c.write_json(folder/"status.json",record)
                self.assertFalse(m.c.party_town_snapshot(1,revoke_follow=True)["active"])

if __name__=="__main__":unittest.main()
