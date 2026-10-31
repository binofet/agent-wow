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
        self.assisting,self.healing,self.looting,self.buffing=False,False,False,False
        self.owner_present,self.combat,self.owner_combat,self.moving,self.resting=True,False,False,False,True
        self.owner_moving,self.trade,self.interaction,self.lease=False,"IDLE","",None
        self.vendor_x,self.vendor_present,self.box,self.block=20.,True,False,None
        self.mail=dict(sessionId="body1",listKnown=True,listCurrent=True,bagMails=[dict(mailId=7,
            attachments=[dict(attachmentIndex=0,itemGuidLow=123,entry=41599,count=1)])])
        self.before_acquire=None
        self.follow_lease=60000
        self.extra_players=[]
    def fork(self):return self
    def player(self,guid,x):
        return dict(guid=str(guid),present=True,mapKnown=True,mapId=0,positionKnown=True,x=x,y=0.,z=0.,
            aliveKnown=True,alive=True,healthKnown=True,health=100,maxHealth=100,unitFlagsKnown=True,unitFlags=0,
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
            return dict(self=own,owner=owner,mailboxes=boxes,nearbyPlayers=copy.deepcopy(self.extra_players))
        if method=='buddy-party.state':return dict(sessionId=self.inv['sessionId'],group=dict(known=True,inGroup=True,groupGuid='7',leaderGuid='3',
            members=[dict(guid=self.inv['selfGuid'],self=True,online=True),dict(guid='3',online=self.owner_present)]+
                [dict(guid=p['guid'],online=True) for p in self.extra_players]))
        if method=="buddy-actions.status":return dict(sessionId=self.inv['sessionId'],selfGuid=self.inv['selfGuid'],following=self.following,healing=self.healing,assisting=self.assisting,buffing=self.buffing,moving=self.moving,interactionOwner=self.interaction,
            followLeaseRemainingMs=self.follow_lease,position=dict(known=True,mapKnown=True,mapId=0,x=self.x,y=0.,z=0.))
        if method=="buddy-trade.state":return dict(sessionId=self.inv["sessionId"],phase=self.trade)
        if method=="buddy-quests.state":return dict(sessionId=self.inv["sessionId"])
        if method=="buddy-loot.state":return dict(sessionId=self.inv["sessionId"],enabled=self.looting)
        if method=="buddy-inventory.discover":return dict(state=copy.deepcopy(self.inv),nearby=[dict(creature=self.vendor())] if self.vendor_present else [])
        if method=="buddy-mail.list":return copy.deepcopy(self.mail)
        if method=="buddy-actions.follow":
            self.following=params['enabled']
            if not self.following:self.moving=False
            return dict(following=self.following,moving=self.moving)
        if method=="buddy-actions.assist":self.assisting=params["enabled"];return dict(assisting=self.assisting)
        if method=="buddy-actions.healing":self.healing=params["enabled"];return dict(healing=self.healing)
        if method=="buddy-actions.buffs":self.buffing=params['enabled'];return dict(buffing=self.buffing)
        if method=="buddy-loot.enable":self.looting=params["enabled"];return dict(enabled=self.looting)
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

    def test_actual_conservative_gear_upgrade_precedes_sale_in_field_without_town_or_vendor(self):
        gear_fakes=load('town_gear_fakes','test_party_gear.py')
        actor,rpc,clock,_,calls=self.rig()
        rpc.inv=gear_fakes.initial();rpc.inv['selfGuid']='1';rpc.resting=False;rpc.vendor_present=False
        self.run_tick(actor,clock);self.run_tick(actor,clock)
        self.assertEqual(calls,[('gear',0)])
        self.assertEqual(actor.status['lastKind'],'gear')
        self.assertNotIn('buddy-actions.travel_vendor',[name for name,_ in rpc.calls])
        self.assertTrue(rpc.following)

    def gear_rig(self):
        fakes=load('town_follow_gear_fakes','test_party_gear.py')
        actor,rpc,clock,saved,calls=self.rig()
        rpc.inv=fakes.initial();rpc.inv['selfGuid']='1'
        rpc.moving=rpc.owner_moving=True;rpc.resting=False;rpc.vendor_present=False
        return actor,rpc,clock,saved,calls

    def test_leased_normal_follow_handoff_equips_while_joel_walks_before_bags_or_services(self):
        actor,rpc,clock,saved,calls=self.gear_rig()
        rpc.assisting=rpc.healing=rpc.looting=rpc.buffing=True
        # A carried bag is independently eligible, but the verified upgrade
        # has priority and never requires a merchant or a town/rest flag.
        original=actor.worker
        def work(kind,target):
            self.assertFalse(rpc.moving)
            self.assertFalse(any((rpc.following,rpc.assisting,rpc.healing,rpc.looting,rpc.buffing)))
            rpc.owner_x+=4
            actor.guard()  # Ordinary leader motion cannot starve own equipment.
            return original(kind,target)
        actor.worker=work
        self.run_tick(actor,clock)
        self.assertEqual(calls,[('gear',0)])
        self.assertEqual(actor.status['selectedTask'],'gear')
        self.assertEqual(actor.status['gearCandidateCount'],1)
        self.assertEqual(actor.status['taskQueue'],m.TASK_PRIORITY)
        self.assertEqual(actor.status['blockedReason'],'')
        self.assertTrue(all((rpc.following,rpc.assisting,rpc.healing,rpc.looting,rpc.buffing)))
        self.assertFalse(any(name in ('buddy-actions.acquire_interaction','buddy-actions.travel_vendor','buddy-mail.list') for name,_ in rpc.calls))
        self.assertEqual(saved[-1]['attempts'][0]['phase'],'complete')

    def test_gear_follow_exception_keeps_combat_unknown_leases_and_human_intent_gates(self):
        for mode in ('not_follow','no_lease','travel','motion_unknown','cast','recovery','interaction',
                     'self_combat','owner_combat','peer_combat','peer_hurt','peer_unknown','peer_far','peer_stale_moving',
                     'owner_far','owner_stale_moving','self_stale_estimate','bad_party','wrong_body','requested','revoked'):
            with self.subTest(mode=mode):
                actor,rpc,clock,_,calls=self.gear_rig();original=rpc.call
                if mode=='not_follow':rpc.following=False
                elif mode=='no_lease':rpc.follow_lease=0
                elif mode=='interaction':rpc.interaction='quests'
                elif mode=='self_combat':rpc.combat=True
                elif mode=='owner_combat':rpc.owner_combat=True
                elif mode=='owner_far':rpc.owner_x=61
                elif mode.startswith('peer_'):
                    p=rpc.player(4,5)
                    if mode=='peer_combat':p['unitFlags']=0x80000
                    elif mode=='peer_hurt':p['health']=84
                    elif mode=='peer_unknown':p['unitFlagsKnown']=False
                    elif mode=='peer_stale_moving':p.update(movementFlags=1,positionAgeMs='2001')
                    else:p['x']=61.
                    rpc.extra_players=[p]
                if mode=='revoked':actor.revoked=lambda:True
                def call(method,params):
                    value=original(method,params)
                    if method=='buddy-actions.status':
                        if mode=='travel':value['traveling']=True
                        if mode=='motion_unknown':value['motionUnconfirmed']=True
                        if mode=='cast':value['castPending']=True
                        if mode=='recovery':value['recovery']={'active':True}
                        if mode=='wrong_body':value['sessionId']='another-body'
                        if mode=='self_stale_estimate':value['position']['ageMs']='2001'
                    if method=='buddy-world.snapshot' and mode=='owner_stale_moving':value['owner']['positionAgeMs']='2001'
                    if method=='buddy-party.state' and mode=='bad_party':value['group']['known']=False
                    return value
                rpc.call=call
                with patch.object(m.c,'quest_task_snapshot',return_value={'active':mode=='requested'}):
                    try:self.run_tick(actor,clock)
                    except m.Error:pass
                self.assertFalse(calls)
                self.assertFalse(any(name=='buddy-actions.follow' for name,_ in rpc.calls))
                self.assertTrue(actor.status['blockedReason'])

    def test_gear_stop_failure_never_dispatches_or_blindly_restores(self):
        for mode in ('still_moving','motion_unknown','lost'):
            with self.subTest(mode=mode):
                actor,rpc,clock,_,calls=self.gear_rig();original=rpc.call
                def call(method,params):
                    value=original(method,params)
                    if method=='buddy-actions.follow' and not params['enabled']:
                        if mode=='lost':raise m.Error('lost stop receipt')
                        value['moving' if mode=='still_moving' else 'motionUnconfirmed']=True
                    return value
                rpc.call=call;self.run_tick(actor,clock)
                self.assertFalse(calls);self.assertFalse(rpc.following)
                self.assertEqual(actor.status['controlsRestore'],'unconfirmed_no_automatic_control_restore')

    def test_stationary_walking_mode_is_not_stale_actual_motion(self):
        actor,rpc,clock,_,calls=self.gear_rig();rpc.owner_moving=False
        rpc.extra_players=[dict(rpc.player(4,5),movementFlags=0x100,positionAgeMs='90000')]
        original=rpc.call
        def call(method,params):
            value=original(method,params)
            if method=='buddy-world.snapshot':value['owner'].update(movementFlags=0x100,positionAgeMs='90000')
            return value
        rpc.call=call;self.run_tick(actor,clock)
        self.assertEqual(calls,[('gear',0)])

    def test_gear_final_guard_rejects_new_party_combat_or_new_group_or_revocation(self):
        for mode in ('combat','group','revoked'):
            with self.subTest(mode=mode):
                actor,rpc,clock,_,calls=self.gear_rig();original=rpc.call;changed=[False]
                def call(method,params):
                    value=original(method,params)
                    if method=='buddy-party.state' and changed[0] and mode=='group':value['group']['groupGuid']='8'
                    return value
                rpc.call=call
                def work(kind,target):
                    changed[0]=True
                    if mode=='combat':rpc.owner_combat=True
                    if mode=='revoked':actor.revoked=lambda:True
                    actor.guard()
                    calls.append((kind,target));return dict(phase='complete')
                actor.worker=work;self.run_tick(actor,clock)
                self.assertFalse(calls)

    def test_disappearing_gear_candidate_never_lends_follow_exception_to_services(self):
        actor,rpc,clock,_,calls=self.gear_rig();original=rpc.call;reads=[0]
        def call(method,params):
            if method=='buddy-inventory.state':
                reads[0]+=1
                if reads[0]>1:
                    for row in rpc.inv['items']:row['conservativeUpgrade']=False
            return original(method,params)
        rpc.call=call;self.run_tick(actor,clock)
        self.assertFalse(calls)
        self.assertEqual(actor.status['blockedReason'],'gear_candidate_changed_before_hold')
        self.assertFalse(any(name in {row[1] for row in m.CONTROLS} for name,_ in rpc.calls))

    def mira(self,result=None):
        rpc=RPC();rpc.url='http://127.0.0.1:8087/rpc';rpc.inv['selfGuid']='4'
        original=rpc.call
        def call(method,params):
            value=original(method,params)
            if method=='buddy-world.snapshot':value['self']['guid']='4'
            return value
        rpc.call=call
        clock,saved,calls=[100.],[],[]
        def work(kind,target):
            self.assertFalse(any((rpc.following,rpc.assisting,rpc.healing,rpc.looting)))
            self.assertIsNone(rpc.lease);calls.append((kind,target))
            return result or dict(phase='complete',newConfirmedOperations=1)
        actor=m.Supervisor(4,rpc,save=lambda value:saved.append(copy.deepcopy(value)),now=lambda:clock[0],worker=work)
        source=dict(itemGuid=str(0x4000000000001234),entry=5000)
        class Scanner:
            def scan(self,*args,**kwargs):return source,'',{}
        actor.disenchant_scanner=Scanner()
        return actor,rpc,clock,saved,calls

    def test_mira_party_unused_gear_precedes_sale_under_existing_control_custody(self):
        with patch.object(m.disenchant,'pending',return_value=False):
            actor,rpc,clock,_,calls=self.mira()
            rpc.assisting=rpc.healing=rpc.looting=rpc.buffing=True
            self.run_tick(actor,clock);self.run_tick(actor,clock)
        self.assertEqual(calls,[('disenchant',0x4000000000001234)])
        self.assertTrue(all((rpc.following,rpc.assisting,rpc.healing,rpc.looting,rpc.buffing)))
        self.assertFalse(any(name=='buddy-actions.travel_vendor' for name,_ in rpc.calls))

    def test_mira_unknown_disposition_keeps_gear_but_does_not_block_gray_sale(self):
        with patch.object(m.disenchant,'pending',return_value=False):
            actor,rpc,clock,_,calls=self.mira()
            actor.disenchant_scanner.scan=lambda *a,**kw:(None,'owner_gear_comparison_unavailable',None)
            self.run_tick(actor,clock);self.run_tick(actor,clock)
        self.assertEqual(calls,[('sell',VENDOR)])
        self.assertEqual(actor.status['disenchantReason'],'owner_gear_comparison_unavailable')

    def test_mira_unconfirmed_destruction_never_restores_controls_or_starts_sale(self):
        with patch.object(m.disenchant,'pending',return_value=True):
            actor,rpc,clock,_,calls=self.mira(dict(phase='unconfirmed',reason='actual_materials_pending'))
            rpc.assisting=rpc.healing=rpc.looting=True
            self.run_tick(actor,clock);self.run_tick(actor,clock)
        self.assertEqual(calls,[('disenchant',0)])
        self.assertFalse(any((rpc.following,rpc.assisting,rpc.healing,rpc.looting)))
        self.assertEqual(actor.status['controlsRestore'],'unconfirmed_no_automatic_control_restore')

    def test_mira_own_upgrade_and_existing_profession_work_keep_priority(self):
        gear_fakes=load('mira_upgrade_before_de','test_party_gear.py')
        with patch.object(m.disenchant,'pending',return_value=False):
            actor,rpc,clock,_,calls=self.mira()
            rpc.inv=gear_fakes.initial();rpc.inv['selfGuid']='4'
            self.run_tick(actor,clock);self.run_tick(actor,clock)
            self.assertEqual(calls,[('gear',0)])
            actor,rpc,clock,_,calls=self.mira()
            with patch.object(m,'profession_active',return_value=True):
                with self.assertRaises(m.Error):self.run_tick(actor,clock)
            self.assertFalse(calls)

    def test_gear_comparison_pending_and_retained_displaced_item_are_not_sold(self):
        for reason in ('displaced_item_template_pending','enhancement_context_pending'):
            actor,rpc,clock,_,calls=self.rig()
            rpc.inv['items'][0]['gearBlockedReason']=reason
            self.run_tick(actor,clock);self.run_tick(actor,clock)
            self.assertFalse(calls);self.assertEqual(actor.status['phase'],'waiting_for_gear_comparison_metadata')
        actor,rpc,clock,_,calls=self.rig()
        protected=int(rpc.inv['items'][0]['item']['itemGuid'])
        with patch.object(m.gear,'ledger_facts',return_value=(False,{protected})):
            self.run_tick(actor,clock);self.run_tick(actor,clock)
        self.assertFalse(calls)

    def test_existing_gear_reservation_is_reconciled_before_any_sale(self):
        actor,rpc,clock,_,calls=self.rig()
        with patch.object(m.gear,'ledger_facts',return_value=(True,set())):
            self.run_tick(actor,clock);self.run_tick(actor,clock)
        self.assertEqual(calls,[('gear',0)])

    def test_real_gear_worker_metadata_abort_restores_controls_but_dispatched_unknown_does_not(self):
        gear_fakes=load('town_gear_abort_fakes','test_party_gear.py')
        for mode in ('metadata_changed','dispatch_unknown'):
            with self.subTest(mode=mode),tempfile.TemporaryDirectory() as temp:
                actor,rpc,clock,saved,_=self.rig()
                rpc.inv=gear_fakes.initial();rpc.inv['selfGuid']='1'
                rpc.assisting=rpc.healing=rpc.looting=True
                actor.worker=actor.run_worker
                root=Path(temp).resolve();path=root/'data/autonomy/town-maintenance/1-8086/gear.json'
                reads=[0];writes=[];late_guards=[];guard=actor.guard
                def guarded(*args,**kwargs):
                    if path.exists():
                        rows=m.gear.json.loads(path.read_text())['attempts']
                        if rows and rows[-1]['phase']=='reserved':late_guards.append(True)
                    return guard(*args,**kwargs)
                actor.guard=guarded
                class Client:
                    url=rpc.url
                    def call(self,method,params):
                        if method=='buddy-inventory.state':
                            reads[0]+=1
                            if mode=='metadata_changed' and reads[0]==4:rpc.inv['revision']='19'
                            return rpc.call(method,params)
                        self_test.assertEqual(method,'buddy-inventory.equip_gear')
                        writes.append(copy.deepcopy(params))
                        rpc.inv['operation']=dict(requestId=params['requestId'],kind='equip_gear',status='OUTCOME_UNCONFIRMED',
                            source=copy.deepcopy(params['source']),equipmentSlot=params['equipmentSlot'],
                            displacedItemGuid=params['expectedEquippedItemGuid'],leaseReleased=False,
                            baselineInventoryRevision=params['expectedInventoryRevision'],
                            baselineEquipmentRevision=params['expectedEquipmentRevision'])
                        return copy.deepcopy(rpc.inv)
                self_test=self
                with patch.object(m,'ROOT',root),patch.object(m.gear,'ROOT',root),patch.object(m.gear,'RPC',return_value=Client()):
                    self.run_tick(actor,clock)
                self.assertFalse(late_guards)
                rows=m.gear.json.loads(path.read_text())['attempts'] if path.exists() else []
                if mode=='metadata_changed':
                    self.assertFalse(writes);self.assertFalse(rows)
                    self.assertEqual(saved[-1]['attempts'][0]['phase'],'blocked')
                    self.assertTrue(all((rpc.following,rpc.assisting,rpc.healing,rpc.looting)))
                else:
                    self.assertEqual(len(writes),1);self.assertEqual(rows[0]['phase'],'unconfirmed')
                    self.assertFalse(any((rpc.following,rpc.assisting,rpc.healing,rpc.looting)))

    def test_gear_metadata_revision_retries_read_only_abort_but_never_replays_issued_request(self):
        gear_fakes=load('town_gear_metadata_retry_fakes','test_party_gear.py')
        for outcome in ('BLOCKED','OUTCOME_UNCONFIRMED'):
            with self.subTest(outcome=outcome),tempfile.TemporaryDirectory() as temp:
                actor,rpc,clock,saved,_=self.rig();rpc.inv=gear_fakes.initial();rpc.inv['selfGuid']='1'
                root=Path(temp).resolve();reads=[0];workers=[];writes=[];run=actor.run_worker
                def worker(kind,target):workers.append((kind,target));return run(kind,target)
                actor.worker=worker
                class Client:
                    url=rpc.url
                    def call(self,method,params):
                        if method=='buddy-inventory.state':
                            reads[0]+=1
                            if reads[0]==4:rpc.inv['revision']='19'  # Template reply, no owned-item change.
                            return rpc.call(method,params)
                        self_test.assertEqual(method,'buddy-inventory.equip_gear');writes.append(copy.deepcopy(params))
                        rpc.inv['operation']=dict(requestId=params['requestId'],kind='equip_gear',status=outcome,
                            source=copy.deepcopy(params['source']),equipmentSlot=params['equipmentSlot'],
                            displacedItemGuid=params['expectedEquippedItemGuid'],leaseReleased=outcome=='BLOCKED',
                            transportAttempted=False,baselineInventoryRevision=params['expectedInventoryRevision'],
                            baselineEquipmentRevision=params['expectedEquipmentRevision'])
                        return copy.deepcopy(rpc.inv)
                self_test=self;inventory_revision=rpc.inv['inventoryRevision']
                with patch.object(m,'ROOT',root),patch.object(m.gear,'ROOT',root),patch.object(m.gear,'RPC',return_value=Client()):
                    self.run_tick(actor,clock)
                    self.assertEqual(len(workers),1);self.assertFalse(writes)
                    first=copy.deepcopy(saved[-1]['attempts'][0]);self.assertEqual(first['phase'],'blocked')
                    self.assertTrue(rpc.following)
                    self.run_tick(actor,clock)
                    self.assertEqual(len(workers),2);self.assertEqual(len(writes),1)
                    self.assertEqual(writes[0]['expectedRevision'],'19')
                    self.assertEqual(rpc.inv['inventoryRevision'],inventory_revision)
                    self.assertNotEqual(saved[-1]['attempts'][1]['key'],first['key'])
                    if outcome=='BLOCKED':
                        self.run_tick(actor,clock)
                        self.assertEqual(len(workers),2)  # Same current module revision is suppressed.
                        self.assertEqual(actor.status['phase'],'unchanged_town_attempt_not_repeated')
                        rpc.inv['revision']='20';self.run_tick(actor,clock)
                        self.assertEqual(len(workers),3)
                    else:
                        rpc.inv['revision']='20'
                        with self.assertRaises(m.Error):self.run_tick(actor,clock)
                        self.assertEqual(len(workers),2)
                    self.assertEqual(len(writes),1)  # Neither settled nor pending request is replayed.
                    self.assertEqual(saved[-1]['attempts'][0],first)

    def test_non_gear_town_key_ignores_metadata_only_module_revision(self):
        actor,rpc,clock,saved,calls=self.rig();rpc.vendor_x=3
        self.run_tick(actor,clock);self.run_tick(actor,clock)
        self.assertEqual(calls,[('sell',VENDOR)])
        key=saved[-1]['attempts'][0]['key']
        self.assertEqual(key,f"body1/{rpc.inv['inventoryRevision']}/sell/{VENDOR}/local")
        rpc.inv['revision']='19';self.run_tick(actor,clock)
        self.assertEqual(calls,[('sell',VENDOR)])
        self.assertEqual(actor.status['phase'],'unchanged_town_attempt_not_repeated')

    def test_actual_complete_trade_with_historical_pants_allows_safe_upkeep(self):
        actor,rpc,clock,_,calls=self.rig()
        original=rpc.call
        completed=dict(sessionId='body1',phase='COMPLETE',partnerGuid='3',acceptTransportWritten=True,partnerAccepted=True,
            partnerItems=[dict(entry=2962,count=1)],lastServerStatus=8,reason='server_confirmed_trade_complete',completedTrades='1')
        def call(method,params):
            if method=='buddy-trade.state':return copy.deepcopy(completed)
            return original(method,params)
        rpc.call=call
        self.run_tick(actor,clock);self.run_tick(actor,clock)
        self.assertEqual(calls,[('sell',VENDOR)])
        self.assertTrue(rpc.following)
        actor,rpc,clock,_,calls=self.rig();original=rpc.call
        completed['lastJunkDisposal']={'status':'TRANSPORT_UNCONFIRMED_NO_RETRY'}
        rpc.call=call
        with self.assertRaises(m.Error):self.run_tick(actor,clock)
        self.assertFalse(calls)
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
                self.assertEqual(m.c.read_json(folder/"follow-revoked.json"),dict(identity=m.identity(1),instance="a"*32,activityId="b"*32,controls=["follow"]))
                m.c.party_town_snapshot(1,revoke_control="assist")
                self.assertEqual(m.c.read_json(folder/"follow-revoked.json")["controls"],["assist","follow"])
                record["identity"]["characterGuid"]=4;m.c.write_json(folder/"status.json",record)
                self.assertFalse(m.c.party_town_snapshot(1,revoke_follow=True)["active"])
    def test_real_normal_follow_assist_healing_loot_all_pause_before_worker_and_restore(self):
        actor,rpc,clock,saved,_=self.rig()
        rpc.assisting=rpc.healing=rpc.looting=True
        ran=[]
        def worker(kind,target):
            self.assertFalse(any((rpc.following,rpc.assisting,rpc.healing,rpc.looting)))
            self.assertIsNone(rpc.lease)
            ran.append(True)
            return dict(phase="complete",newConfirmedOperations=1)
        actor.worker=worker
        self.run_tick(actor,clock);self.run_tick(actor,clock)
        self.assertEqual(ran,[True])
        self.assertTrue(all((rpc.following,rpc.assisting,rpc.healing,rpc.looting)))
        self.assertEqual(saved[-1]["attempts"][0]["heldControls"],dict(follow=True,assist=True,healing=True,loot=True))
    def test_no_qualified_upkeep_never_turns_off_any_enabled_control(self):
        actor,rpc,clock,_,_=self.rig();rpc.assisting=rpc.healing=rpc.looting=True;rpc.inv["items"]=[]
        self.run_tick(actor,clock);self.run_tick(actor,clock)
        self.assertTrue(all((rpc.following,rpc.assisting,rpc.healing,rpc.looting)))
        self.assertFalse(any(method in {row[1] for row in m.CONTROLS} for method,_ in rpc.calls))
    def test_new_explicit_control_commands_win_and_unrelated_held_controls_restore(self):
        for control,attribute in (("assist","assisting"),("healing","healing"),("loot","looting"),("follow","following"),('buffs','buffing')):
            for new_value in (False,True):
                actor,rpc,clock,_,_=self.rig();rpc.assisting=rpc.healing=rpc.looting=rpc.buffing=True
                revoked=set();actor.revoked_controls=lambda:revoked
                def worker(kind,target):
                    revoked.add(control);setattr(rpc,attribute,new_value)
                    return dict(phase="complete")
                actor.worker=worker
                self.run_tick(actor,clock);self.run_tick(actor,clock)
                self.assertEqual(getattr(rpc,attribute),new_value,(control,new_value))
                for other,_,field,_ in m.CONTROLS:
                    name={"enabled":"looting","assisting":"assisting","following":"following","healing":"healing",'buffing':'buffing'}[field]
                    if other!=control:self.assertTrue(getattr(rpc,name),(control,other))
    def test_uncertain_pause_or_worker_outcome_never_automatically_restores_controls(self):
        for mode in ("pause_lost","pause_refused","worker_lost","worker_exception","body_changed"):
            actor,rpc,clock,_,_=self.rig();rpc.assisting=rpc.healing=rpc.looting=True;rpc.vendor_x=3
            original=rpc.call
            def call(method,params):
                result=original(method,params)
                if method=="buddy-loot.enable" and params["enabled"] is False:
                    if mode=="pause_lost":raise m.Error("pause reply lost")
                    if mode=="pause_refused":return dict(enabled=True)
                return result
            rpc.call=call
            def worker(kind,target):
                if mode=="worker_exception":raise m.Error("worker receipt lost")
                if mode=="body_changed":rpc.inv["sessionId"]="newbody";return dict(phase="complete")
                return dict(phase="transport_unknown")
            actor.worker=worker
            self.run_tick(actor,clock);self.run_tick(actor,clock)
            enables=[p for method,p in rpc.calls if method in {row[1] for row in m.CONTROLS} and p["enabled"]]
            self.assertEqual(enables,[],mode)
    def test_conversation_toggle_revokes_only_its_owned_town_control(self):
        for action,control in (("ENABLE_ASSIST","assist"),("DISABLE_ASSIST","assist"),
                               ("ENABLE_HEALING","healing"),("DISABLE_HEALING","healing"),
                               ("ENABLE_LOOTING","loot"),("DISABLE_LOOTING","loot")):
            _,rpc,_,_,_=self.rig()
            controls=m.c.BuddyControls(rpc);controls.actions_available=controls.loot_available=True
            with patch.object(m.c,"party_town_snapshot",return_value={}) as marker:
                controls.execute(action)
                marker.assert_called_once_with(1,revoke_control=control)

if __name__=="__main__":unittest.main()
