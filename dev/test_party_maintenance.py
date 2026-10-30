import copy
import importlib.machinery
import importlib.util
from pathlib import Path
import unittest
import threading
import math
import os
import tempfile
from unittest.mock import patch

loader=importlib.machinery.SourceFileLoader('maintenance_test',str(Path(__file__).with_name('party-maintenance')))
spec=importlib.util.spec_from_loader(loader.name,loader)
m=importlib.util.module_from_spec(spec);loader.exec_module(m)

class RPC:
    def __init__(self):
        self.calls=[]
        self.world={'self':{'guid':'1','present':True,'aliveKnown':True,'alive':True,'unitFlagsKnown':True,'mapKnown':True,'levelKnown':True,'level':4},
            'owner':{'guid':'3','present':True,'aliveKnown':True,'alive':True,'unitFlagsKnown':True}}
        self.actions={'position':{'known':True,'mapKnown':True,'x':0,'y':0,'z':0}}
        self.group={'known':True,'inGroup':True,'leaderGuid':'3','members':[
            {'guid':'1','self':True,'online':True},{'guid':'3','name':'Joel','online':True}]}
        self.offer={'revision':'2','npcGuid':'17379390965327856171','expiresInMs':'20000','spells':[
            {'spellId':123,'costCopper':100,'requiredLevel':4}]}
        self.training={'sessionId':'body1','copperKnown':True,'copper':10000,'offer':self.offer}
        self.mail={'sessionId':'body1','listKnown':True,'listCurrent':True,'listRevision':'2',
            'mailboxGuid':'17370300000000000001','stipends':[{'mailId':5,'copper':10000}]}
        self.claim={'mailId':5,'status':'CLAIM_CONFIRMED','serverAcknowledged':True,'walletIncreaseObserved':True,'confirmedCopper':10000}
        self.purchase={}
        self.fail_buy=False
        self.trainer_x,self.token,self.block_travel=None,None,None
        self.route_reason,self.before_acquire,self.after_travel='trainer_travel_within_distance',None,None
    def call(self,method,params):
        self.calls.append((method,copy.deepcopy(params)))
        if method=='buddy-world.snapshot':value=self.world
        elif method=='buddy-party.state':value={'group':self.group}
        elif method=='buddy-actions.status':value=self.actions
        elif method=='buddy-training.discover':
            if self.trainer_x is None:value={'state':self.training,'nearby':[{'interactable':True,'creature':{'guid':self.offer['npcGuid']}}]}
            else:
                x=self.actions['position']['x'];distance=abs(self.trainer_x-x)
                creature=dict(guid=self.offer['npcGuid'],entry=(int(self.offer['npcGuid'])>>24)&0xffffff,
                    present=True,mapKnown=True,positionKnown=True,x=self.trainer_x,y=0.,z=0.,
                    aliveKnown=True,alive=True,healthKnown=True,health=100,npcFlags=0x33)
                value={'state':self.training,'nearby':[dict(creature=creature,distanceKnown=True,distance=distance,
                    interactable=distance<=4,blockedReason='' if distance<=4 else 'trainer_outside_conservative_range')]}
        elif method=='buddy-training.list':
            assert self.token is None,'trainer list must acquire its own lease'
            value=self.training
        elif method=='buddy-training.state':value=dict(self.training,lastPurchase=self.purchase)
        elif method=='buddy-training.buy':
            assert self.token is None,'trainer buy must acquire its own lease'
            if self.fail_buy:raise m.Error('uncertain write')
            value=self.training
        elif method=='buddy-mail.list':value=self.mail
        elif method=='buddy-mail.state':value=dict(self.mail,lastClaim=self.claim)
        elif method=='buddy-mail.claim_stipend':
            if m.confirmed_mail(self.claim,params['mailId']):self.training['copper']+=10000
            value={'lastClaim':self.claim}
        elif method=='buddy-actions.acquire_interaction':
            assert params=={'owner':'training','leaseMs':30000} and self.token is None
            self.token='exact-training-token';self.actions['interactionOwner']='training'
            value={'token':self.token,'state':self.actions}
            if self.before_acquire:self.before_acquire(self)
        elif method=='buddy-actions.travel_trainer':
            assert params=={'token':self.token,'npcGuid':self.offer['npcGuid']}
            if self.block_travel:self.block_travel.wait(2)
            if self.route_reason=='trainer_travel_within_distance':self.actions['position']['x']=self.trainer_x-2
            if self.after_travel:self.after_travel(self)
            value=dict(self.actions,reason=self.route_reason)
        elif method=='buddy-actions.release_interaction':
            assert params=={'owner':'training','token':self.token}
            self.token=None;self.actions.pop('interactionOwner',None)
            if self.block_travel:self.block_travel.set()
            value=self.actions
        else:raise AssertionError(method)
        return copy.deepcopy(value)

class InnRPC(RPC):
    def __init__(self, guid=1, class_id=5, start=(-9427.,56.,56.79)):
        super().__init__()
        self.world['owner']=None
        self.world['self'].update(guid=str(guid),classKnown=True,classId=class_id,levelKnown=True,level=5,
            positionKnown=True,movementFlagsKnown=True,x=start[0],y=start[1],z=start[2])
        self.actions['position'].update(mapId=0,x=start[0],y=start[1],z=start[2])
        positions={377:(-9460.75,33.1338,63.9038),328:(-9471.67,34.451,63.9038),917:(-9465.79,12.6454,63.9038)}
        self.creatures=[]
        for entry,point in positions.items():
            self.creatures.append(dict(guid=str(0xf130000000000000|entry<<24|entry),entry=entry,
                present=True,mapKnown=True,mapId=0,positionKnown=True,x=point[0],y=point[1],z=point[2],
                positionRevision='1',aliveKnown=True,alive=True,healthKnown=True,health=100,npcFlags=0x33))
        self.offer['npcGuid']=next(c['guid'] for c in self.creatures if c['entry']==m.INN_TRAINERS[class_id])
        self.lease_owner=None
        self.after_waypoint=None
        self.waypoint_reason='exploration_within_distance'
        self.false_arrival=False
    def call(self,method,params):
        if method=='buddy-training.discover':
            self.calls.append((method,copy.deepcopy(params)))
            pos=self.actions['position'];rows=[]
            for c in self.creatures:
                distance=math.dist(tuple(pos[k] for k in ('x','y','z')),tuple(c[k] for k in ('x','y','z')))
                rows.append(dict(creature=copy.deepcopy(c),distanceKnown=True,distance=distance,
                    interactable=distance<=4,blockedReason='' if distance<=4 else 'trainer_outside_conservative_range'))
            return copy.deepcopy(dict(state=self.training,nearby=rows))
        if method=='buddy-actions.acquire_interaction':
            self.calls.append((method,copy.deepcopy(params)))
            assert params['owner'] in ('training','exploration') and params['leaseMs']==30000
            if params.get('token'):
                assert params['token']==self.token and params['owner']==self.lease_owner
            else:
                assert self.token is None
                self.lease_owner=params['owner'];self.token='exact-'+self.lease_owner+'-token'
                self.actions['interactionOwner']=self.lease_owner
            return copy.deepcopy(dict(token=self.token,state=self.actions))
        if method=='buddy-actions.travel_to':
            self.calls.append((method,copy.deepcopy(params)))
            assert self.lease_owner=='exploration' and params['token']==self.token and params['arrivalDistance'] in (.5,.75)
            if self.block_travel:self.block_travel.wait(2)
            if self.waypoint_reason=='exploration_within_distance' and not self.false_arrival:
                self.actions['position'].update({k:params[k] for k in ('x','y','z')})
            if self.after_waypoint:self.after_waypoint(self)
            return copy.deepcopy(dict(self.actions,reason=self.waypoint_reason))
        if method=='buddy-actions.travel_trainer':
            self.calls.append((method,copy.deepcopy(params)))
            assert self.lease_owner=='training' and params==dict(token=self.token,npcGuid=self.offer['npcGuid'])
            creature=next(c for c in self.creatures if c['guid']==params['npcGuid'])
            self.actions['position'].update(x=creature['x']+1,y=creature['y'],z=creature['z'])
            return copy.deepcopy(dict(self.actions,reason='trainer_travel_within_distance'))
        if method=='buddy-actions.release_interaction':
            self.calls.append((method,copy.deepcopy(params)))
            assert params==dict(owner=self.lease_owner,token=self.token)
            self.token,self.lease_owner=None,None;self.actions.pop('interactionOwner',None)
            if self.block_travel:self.block_travel.set()
            return copy.deepcopy(self.actions)
        return super().call(method,params)

class MaintenanceTests(unittest.TestCase):
    def setUp(self):
        self.rpc=RPC();self.saved=[];self.now=[100]
        for name in ('quest_task_snapshot','area_quest_snapshot','party_town_snapshot'):
            mock=patch.object(m.c,name,return_value={'active':False});mock.start();self.addCleanup(mock.stop)
        mock=patch.object(m,'profession_active',return_value=False);mock.start();self.addCleanup(mock.stop)
        self.agent=m.Maintenance('Arlen',1,self.rpc,lambda d:self.saved.append(copy.deepcopy(d)),now=lambda:self.now[0])
    def tick(self):self.agent.tick();self.now[0]+=11
    def methods(self):return [x[0] for x in self.rpc.calls]
    def box(self,x=0):
        self.rpc.world['mailboxes']=[{'present':True,'positionKnown':True,'mapKnown':True,'x':x,'y':0,'z':0}]
    def travel_rig(self, owner_present=True):
        self.rpc.trainer_x=50.
        self.rpc.world['self'].update(classKnown=True,classId=5,levelKnown=True,movementFlagsKnown=True)
        if owner_present:
            self.rpc.world['owner'].update(mapKnown=True,positionKnown=True,movementFlagsKnown=True,x=0.,y=0.,z=0.)
        else:self.rpc.world['owner']=None

    def inn_rig(self,name='Arlen',guid=1,class_id=5,start=(-9427.,56.,56.79)):
        self.rpc=InnRPC(guid,class_id,start)
        self.agent=m.Maintenance(name,guid,self.rpc,lambda d:self.saved.append(copy.deepcopy(d)),now=lambda:self.now[0])

    def test_all_three_actual_class_teachers_use_precise_inn_route_before_normal_offer(self):
        for name,guid,class_id,start in (('Arlen',1,5,(-9427.,56.,56.79)),
                ('Mira',4,8,(-9461.812,17.964062,58.20209)),('Rook',5,4,(-9448.55,68.236,56.3225))):
            with self.subTest(name=name):
                self.setUp();self.inn_rig(name,guid,class_id,start);self.tick()
                routes=[p for method,p in self.rpc.calls if method=='buddy-actions.travel_to']
                expected=[(-9465.93,24.9355,56.52882)]+list(m.INN_STAIRS)
                if name=='Arlen':expected.insert(0,(-9432.,60.,56.625515))
                self.assertEqual([tuple(p[k] for k in ('x','y','z')) for p in routes],expected)
                self.assertEqual([p['arrivalDistance'] for p in routes],([.5,.5] if name=='Arlen' else [.5])+[.75]*4)
                release=next(i for i,(method,p) in enumerate(self.rpc.calls)
                    if method=='buddy-actions.release_interaction' and p['owner']=='exploration')
                acquire=next(i for i,(method,p) in enumerate(self.rpc.calls)
                    if method=='buddy-actions.acquire_interaction' and p['owner']=='training')
                self.assertLess(release,acquire)
                self.assertLess(self.methods().index('buddy-actions.travel_trainer'),self.methods().index('buddy-training.list'))
                self.assertEqual(self.agent.data['reservedCopper'],100)
                self.assertEqual(self.agent.data['training'][0]['npc'],self.rpc.offer['npcGuid'])
                self.assertEqual(self.agent.data['training'][0]['status'],'reserved')
                self.assertEqual(list(self.agent.travel_attempts.values()),[1])
                self.assertIsNone(self.rpc.token)
                self.assertNotIn('buddy-actions.follow',self.methods())

    def test_nearer_inn_teacher_for_another_class_does_not_starve_own_teacher(self):
        self.inn_rig('Mira',4,8,(-9460.75,33.1338,63.9038));self.tick()
        self.assertNotIn('buddy-actions.travel_to',self.methods())
        listed=next(p for method,p in self.rpc.calls if method=='buddy-training.list')
        self.assertEqual(int(listed['npcGuid'])>>24&0xffffff,328)
        self.assertEqual(self.methods().count('buddy-actions.travel_trainer'),1)

    def test_inn_landmarks_never_apply_on_another_map_or_from_a_distant_city(self):
        self.inn_rig()
        self.rpc.world['self']['mapId']=1;self.rpc.actions['position']['mapId']=1
        for creature in self.rpc.creatures:creature['mapId']=1
        self.tick()
        self.assertNotIn('buddy-actions.travel_to',self.methods())
        self.assertEqual(self.methods().count('buddy-actions.travel_trainer'),1)
        self.setUp();self.inn_rig(start=(-8900.,800.,93.));self.tick()
        self.assertNotIn('buddy-actions.travel_to',self.methods())
        self.assertNotIn('buddy-actions.acquire_interaction',self.methods())

    def test_inn_partial_or_false_precise_arrival_releases_without_buy_or_unbounded_retry(self):
        for false_arrival in (False,True):
            self.setUp();self.inn_rig();self.rpc.false_arrival=false_arrival
            if not false_arrival:self.rpc.waypoint_reason='exploration_navigation_unavailable'
            for _ in range(6):self.tick()
            self.assertEqual(self.methods().count('buddy-actions.travel_to'),3)
            self.assertNotIn('buddy-actions.travel_trainer',self.methods())
            self.assertNotIn('buddy-training.buy',self.methods())
            self.assertIsNone(self.rpc.token)
            self.assertEqual(self.saved,[])

    def test_inn_fresh_body_trainer_owner_control_and_activity_changes_stop_next_leg(self):
        for mode in ('body','trainer_moved','trainer_absent','owner_returned','control','activity','combat'):
            with self.subTest(mode=mode):
                self.setUp();self.inn_rig()
                def changed(r):
                    if mode=='body':r.training['sessionId']='replacement'
                    elif mode=='trainer_moved':
                        next(c for c in r.creatures if c['guid']==r.offer['npcGuid'])['x']+=1
                    elif mode=='trainer_absent':r.creatures=[c for c in r.creatures if c['guid']!=r.offer['npcGuid']]
                    elif mode=='owner_returned':
                        r.world['owner']=dict(guid='3',present=True,aliveKnown=True,alive=True,unitFlagsKnown=True,
                            positionKnown=True,mapKnown=True,movementFlagsKnown=True,x=-9427.,y=56.,z=56.79)
                    elif mode=='control':r.actions['following']=True
                    elif mode=='activity':self.agent.activities=lambda:True
                    else:r.world['self']['unitFlags']=0x80000
                self.rpc.after_waypoint=changed
                with self.assertRaises(m.Error):self.tick()
                self.assertEqual(self.methods().count('buddy-actions.travel_to'),1)
                self.assertNotIn('buddy-training.buy',self.methods())
                if mode!='body':self.assertIsNone(self.rpc.token)
                self.assertEqual(self.saved,[])

    def test_inn_renews_only_exact_owned_token_and_refuses_changed_renewal(self):
        self.inn_rig();original=self.rpc.call
        def changed(method,params):
            result=original(method,params)
            if method=='buddy-actions.acquire_interaction' and params.get('token'):result['token']='foreign-token'
            return result
        self.rpc.call=changed
        with self.assertRaises(m.Error):self.tick()
        self.assertNotIn('buddy-actions.travel_to',self.methods())
        self.assertIsNone(self.rpc.token)
        renewals=[p for method,p in self.rpc.calls if method=='buddy-actions.acquire_interaction' and p.get('token')]
        self.assertEqual(renewals,[dict(owner='exploration',token='exact-exploration-token',leaseMs=30000)])

    def test_inn_total_budget_cannot_be_reset_by_each_waypoint_or_renewal(self):
        self.inn_rig();clock=[1000.]
        self.rpc.after_waypoint=lambda r:clock.__setitem__(0,clock[0]+76)
        with patch.object(m.time,'monotonic',side_effect=lambda:clock[0]):
            with self.assertRaises(m.Error):self.tick()
        self.assertEqual(self.methods().count('buddy-actions.travel_to'),1)
        self.assertNotIn('buddy-training.buy',self.methods())
        self.assertIsNone(self.rpc.token)

    def test_stop_during_inn_leg_releases_exploration_and_never_changes_enabled_intents(self):
        self.inn_rig();self.rpc.block_travel=threading.Event()
        stopped=threading.Event();self.agent.stopped=stopped.is_set
        timer=threading.Timer(.05,stopped.set);timer.start()
        try:
            with self.assertRaises(m.Error):self.tick()
        finally:timer.join()
        self.assertIsNone(self.rpc.token)
        self.assertEqual(self.methods().count('buddy-actions.travel_to'),1)
        self.assertNotIn('buddy-actions.travel_trainer',self.methods())
        self.assertNotIn('buddy-actions.follow',self.methods())

    def test_inn_retry_from_observed_stair_phase_does_not_return_downstairs(self):
        for start,count in (((-9477.,18.,59.),3),((-9477.,14.,60.184803),2),
                ((-9473.5,18.,62.3),1),((-9473.5,23.,63.820427),0)):
            with self.subTest(start=start):
                self.setUp();self.inn_rig(start=start);self.tick()
                self.assertEqual(self.methods().count('buddy-actions.travel_to'),count)
                self.assertEqual(self.methods().count('buddy-training.buy'),1)

    def test_observed_class_trainer_travel_releases_before_offer_and_purchase(self):
        for present in (True,False):
            with self.subTest(owner_present=present):
                self.setUp();self.travel_rig(present)
                self.tick()
                methods=self.methods()
                self.assertEqual(methods.count('buddy-actions.travel_trainer'),1)
                self.assertLess(methods.index('buddy-actions.release_interaction'),methods.index('buddy-training.list'))
                self.assertLess(methods.index('buddy-training.list'),methods.index('buddy-training.buy'))
                self.assertEqual(self.saved[-1]['training'][0]['status'],'reserved')
                self.assertEqual(self.saved[-1]['reservedCopper'],100)
                self.assertIsNone(self.rpc.token)
                self.assertNotIn('buddy-actions.follow',methods)
                self.rpc.purchase={'id':1,'npcGuid':self.rpc.offer['npcGuid'],'spellId':123,'costCopper':100,
                    'status':'TRAINING_CONFIRMED','serverSuccessObserved':True,'learnedSpellObserved':True}
                self.tick();self.assertEqual(self.agent.data['training'][0]['status'],'confirmed')

    def test_no_trainer_trip_during_requested_quest_area_profession_or_town(self):
        for name in ('quest_task_snapshot','area_quest_snapshot','party_town_snapshot','profession_active'):
            with self.subTest(name=name):
                self.setUp();self.travel_rig()
                owner=m if name=='profession_active' else m.c
                with patch.object(owner,name,return_value=True if name=='profession_active' else {'active':True}):self.tick()
                self.assertEqual(self.methods(),[])
                expected='waiting_for_requested_quest_task' if name=='quest_task_snapshot' else 'waiting_for_requested_routine'
                self.assertEqual(self.agent.status['phase'],expected)

    def test_idle_standing_town_does_not_starve_class_or_last_harvest_opportunity(self):
        with patch.object(m.c,'party_town_snapshot',return_value={'active':True,'busy':False}):
            self.rpc.training['copper']=0
            self.assertIsNone(self.agent.activity_phase())
            self.rpc.training['copper']=10000
            self.rpc.offer['spells']=[]
            self.tick()
        self.assertEqual(self.agent.status['phase'],'no_affordable_unattempted_class_training')
        self.assertTrue(m.harvest.maintenance_idle(self.agent))

    def test_busy_or_unknown_town_and_reserved_class_work_precede_harvest(self):
        for town in ({'active':True,'busy':True},{'active':True}):
            with patch.object(m.c,'party_town_snapshot',return_value=town):
                self.assertEqual(self.agent.activity_phase(),'waiting_for_requested_routine')
        self.agent.status['phase']='no_affordable_unattempted_class_training'
        self.agent.data['training']=[{'status':'reserved'}]
        self.assertFalse(m.harvest.maintenance_idle(self.agent))
        self.agent.data['training']=[]
        self.agent.data['mail']={'status':'reserved'}
        self.assertFalse(m.harvest.maintenance_idle(self.agent))

    def test_travel_requires_stationary_safe_owner_and_positive_actual_wallet(self):
        for mode in ('owner_moving','owner_unknown_movement','owner_map','own_unknown_level','wallet_unknown','no_money','too_far'):
            with self.subTest(mode=mode):
                self.setUp();self.travel_rig()
                if mode=='owner_moving':self.rpc.world['owner']['movementFlags']=1
                elif mode=='owner_unknown_movement':self.rpc.world['owner']['movementFlagsKnown']=False
                elif mode=='owner_map':self.rpc.world['owner']['mapId']=1
                elif mode=='own_unknown_level':self.rpc.world['self']['levelKnown']=False
                elif mode=='wallet_unknown':self.rpc.training['copperKnown']=False
                elif mode=='no_money':self.rpc.training['copper']=0
                elif mode=='too_far':self.rpc.trainer_x=101
                try:self.tick()
                except m.Error:pass
                self.assertNotIn('buddy-actions.acquire_interaction',self.methods())
                self.assertNotIn('buddy-training.buy',self.methods())

    def test_changed_body_or_owner_after_acquire_never_moves_or_buys(self):
        for mode in ('body','owner','intent'):
            with self.subTest(mode=mode):
                self.setUp();self.travel_rig()
                def change(r):
                    if mode=='body':r.training['sessionId']='replacement'
                    elif mode=='owner':r.world['owner']['x']=1.
                    else:r.actions['following']=True
                self.rpc.before_acquire=change
                with self.assertRaises(m.Error):self.tick()
                self.assertNotIn('buddy-actions.travel_trainer',self.methods())
                self.assertNotIn('buddy-training.buy',self.methods())
                if mode!='body':self.assertIsNone(self.rpc.token)
                self.assertEqual(self.saved,[])

    def test_owner_move_after_route_and_body_change_during_list_never_reserves_purchase(self):
        for mode in ('owner','body_list'):
            with self.subTest(mode=mode):
                self.setUp();self.travel_rig()
                if mode=='owner':self.rpc.after_travel=lambda r:r.world['owner'].update(x=1.)
                else:
                    original=self.rpc.call
                    def changed(method,params):
                        if method=='buddy-training.list':self.rpc.training['sessionId']='replacement'
                        return original(method,params)
                    self.rpc.call=changed
                with self.assertRaises(m.Error):self.tick()
                self.assertNotIn('buddy-training.buy',self.methods())
                self.assertIsNone(self.rpc.token)
                self.assertEqual(self.saved,[])

    def test_three_failed_class_trainer_routes_do_not_loop_or_purchase(self):
        self.travel_rig();self.rpc.route_reason='navigation_partial_route'
        for _ in range(6):self.tick()
        self.assertEqual(self.methods().count('buddy-actions.travel_trainer'),3)
        self.assertNotIn('buddy-training.list',self.methods())
        self.assertNotIn('buddy-training.buy',self.methods())
        self.assertIsNone(self.rpc.token)

    def test_stop_during_trainer_route_releases_exact_token_without_intent_toggles(self):
        self.travel_rig();self.rpc.block_travel=threading.Event()
        stopped=threading.Event();self.agent.stopped=stopped.is_set
        timer=threading.Timer(.05,stopped.set);timer.start()
        try:
            with self.assertRaises(m.Error):self.tick()
        finally:timer.join()
        self.assertIsNone(self.rpc.token)
        self.assertTrue(self.rpc.block_travel.is_set())
        self.assertNotIn('buddy-training.buy',self.methods())
        self.assertNotIn('buddy-actions.follow',self.methods())

    def test_absent_owner_collects_existing_stipend_and_confirms_class_learning(self):
        for name,guid in (('Arlen',1),('Mira',4),('Rook',5)):
            for owner in (None, {}, {'guid':'3','present':False,'unitFlagsKnown':True,'unitFlags':0x80000}):
                with self.subTest(guid=guid,owner=owner):
                    self.setUp()
                    self.rpc.world['self']['guid']=str(guid)
                    self.rpc.world['owner']=owner
                    self.rpc.group={'known':False}
                    self.agent=m.Maintenance(name,guid,self.rpc,lambda d:self.saved.append(copy.deepcopy(d)),now=lambda:self.now[0])
                    self.rpc.training['copper']=99
                    self.box()
                    self.tick()
                    self.assertEqual(self.agent.data['mail']['status'],'confirmed')
                    self.assertEqual(self.rpc.training['copper'],10099)
                    self.assertEqual(self.methods().count('buddy-mail.claim_stipend'),1)
                    self.tick()
                    self.assertEqual(self.methods().count('buddy-training.buy'),1)
                    self.assertEqual(self.agent.data['training'][0]['status'],'reserved')
                    self.rpc.purchase={'id':1,'npcGuid':self.rpc.offer['npcGuid'],'spellId':123,'costCopper':100,
                        'status':'TRAINING_CONFIRMED','serverSuccessObserved':True,'learnedSpellObserved':True}
                    self.tick()
                    self.assertEqual(self.agent.data['training'][0]['status'],'confirmed')
                    self.assertEqual(self.agent.data['reservedCopper'],100)
                    self.assertNotIn('buddy-party.state',self.methods())
                    self.assertEqual(self.methods().count('buddy-mail.claim_stipend'),1)
                    self.assertEqual(self.methods().count('buddy-training.buy'),1)

    def test_only_actually_present_known_owner_combat_defers_local_services(self):
        for known,present,expected in ((True,True,False),(False,True,True),(True,False,True)):
            with self.subTest(known=known,present=present):
                self.setUp()
                self.rpc.world['owner']={'guid':'3','present':present,'unitFlagsKnown':known,'unitFlags':0x80000}
                self.tick()
                self.assertEqual('buddy-training.buy' in self.methods(),expected)
                self.assertNotIn('buddy-party.state',self.methods())
        self.setUp()
        self.rpc.world['owner']={'guid':'0','present':True,'unitFlagsKnown':True,'unitFlags':0x80000}
        self.tick()
        self.assertIn('buddy-training.buy',self.methods())

    def test_absent_owner_does_not_relax_own_position_identity_or_busy_gates(self):
        for mode in ('dead','unknown_alive','own_combat','own_map_unknown','position_unknown','map_mismatch','nonfinite',
                     'moving','motionUnconfirmed','castPending','autoAttacking','autoAttackRequested','autoAttackUnconfirmed',
                     'interactionOwner','traveling','practicing'):
            with self.subTest(mode=mode):
                self.setUp();self.rpc.world['owner']=None;self.box()
                if mode=='dead':self.rpc.world['self']['alive']=False
                elif mode=='unknown_alive':self.rpc.world['self']['aliveKnown']=False
                elif mode=='own_combat':self.rpc.world['self']['unitFlags']=0x80000
                elif mode=='own_map_unknown':self.rpc.world['self']['mapKnown']=False
                elif mode=='position_unknown':self.rpc.actions['position']['known']=False
                elif mode=='map_mismatch':self.rpc.actions['position']['mapId']=530
                elif mode=='nonfinite':self.rpc.actions['position']['x']=float('nan')
                else:self.rpc.actions[mode]=True
                self.tick()
                self.assertNotIn('buddy-mail.list',self.methods())
                self.assertNotIn('buddy-training.discover',self.methods())
                self.assertEqual(self.saved,[])
        self.setUp();self.rpc.world['owner']={'guid':'4','present':False}
        with self.assertRaises(m.Error):self.tick()
        self.assertEqual(self.methods(),['buddy-world.snapshot'])
        for name,guid in (('Nixwick',8),('Mira',1)):
            with self.assertRaises(m.Error):m.Maintenance(name,guid,self.rpc,lambda d:None)

    def test_offline_owner_ambiguous_mail_reservation_survives_body_change_without_replay(self):
        self.rpc.world['owner']=None;self.rpc.training['copper']=99;self.box()
        self.rpc.claim={'mailId':5,'status':'CLAIM_REQUESTED'}
        self.tick()
        stored=copy.deepcopy(self.saved[-1])
        self.agent=m.Maintenance('Arlen',1,self.rpc,lambda d:None,stored,now=lambda:200)
        self.rpc.mail['sessionId']='replacement'
        self.agent.tick()
        self.assertEqual(self.agent.data['mail']['status'],'reserved')
        self.assertEqual(self.methods().count('buddy-mail.claim_stipend'),1)
        self.assertNotIn('buddy-training.buy',self.methods())
        self.assertEqual(self.agent.data['identity'],stored['identity'])

    def test_budget_reserved_before_write_and_exact_offer(self):
        self.tick()
        self.assertEqual(self.saved[0]['reservedCopper'],100)
        self.assertEqual(self.saved[0]['training'][0]['status'],'reserved')
        request=[p for method,p in self.rpc.calls if method=='buddy-training.buy'][0]
        self.assertEqual(request,{'sessionId':'body1','npcGuid':self.rpc.offer['npcGuid'],
            'offerRevision':'2','spellId':123,'expectedCostCopper':100,'maxSpendCopper':9900,'reserveCopper':100})
    def test_save_failure_prevents_purchase(self):
        self.agent.save=lambda d:(_ for _ in ()).throw(OSError('disk full'))
        with self.assertRaises(OSError):self.tick()
        self.assertNotIn('buddy-training.buy',self.methods())
    def test_ambiguous_purchase_is_not_replayed_after_restart_or_body_change(self):
        self.rpc.fail_buy=True
        with self.assertRaises(m.Error):self.tick()
        self.agent=m.Maintenance('Arlen',1,self.rpc,lambda d:None,self.saved[-1],now=lambda:200)
        self.rpc.training['sessionId']='body2';self.agent.tick()
        self.assertEqual(self.methods().count('buddy-training.buy'),1)
        self.assertEqual(self.agent.status['phase'],'waiting_for_training_confirmation')
    def test_success_needs_server_and_learning_then_next_spell(self):
        self.tick();self.rpc.offer['spells'].append({'spellId':124,'costCopper':50})
        self.rpc.purchase={'id':1,'npcGuid':self.rpc.offer['npcGuid'],'spellId':123,'costCopper':100,
            'status':'TRAINING_CONFIRMED','serverSuccessObserved':True}
        self.tick();self.assertEqual(self.methods().count('buddy-training.buy'),1)
        self.rpc.purchase['learnedSpellObserved']=True
        self.tick();self.assertEqual(self.methods().count('buddy-training.buy'),2)
        self.assertEqual(self.agent.data['reservedCopper'],150)
    def test_actual_wallet_reserve_and_no_lifetime_spending_limit(self):
        self.rpc.training['copper']=99;self.tick()
        self.assertNotIn('buddy-training.buy',self.methods())
        self.rpc.training['copper']=10000;self.rpc.offer['spells']=[{'spellId':123,'costCopper':9900}]
        self.tick();self.rpc.purchase={'id':1,'npcGuid':self.rpc.offer['npcGuid'],'spellId':123,'costCopper':9900,'status':'SERVER_REJECTED'}
        self.rpc.offer['spells']=[{'spellId':124,'costCopper':11}];self.tick()
        self.assertEqual(self.methods().count('buddy-training.buy'),2)
    def test_prior_or_wrong_npc_receipt_cannot_release_pending_reservation(self):
        self.rpc.training['lastPurchase']={'id':7}
        self.tick()
        self.rpc.purchase={'id':7,'npcGuid':self.rpc.offer['npcGuid'],'spellId':123,'costCopper':100,
            'status':'TRAINING_CONFIRMED','serverSuccessObserved':True,'learnedSpellObserved':True}
        self.tick();self.assertEqual(self.agent.data['training'][0]['status'],'reserved')
        self.rpc.purchase['id']=8;self.rpc.purchase['npcGuid']='1'
        self.tick();self.assertEqual(self.agent.data['training'][0]['status'],'reserved')
    def test_late_mail_receipt_reconciles_without_replaying_claim(self):
        self.box();self.rpc.claim={'mailId':5,'status':'CLAIM_REQUESTED'};self.tick()
        self.rpc.claim.update(status='CLAIM_CONFIRMED',serverAcknowledged=True,walletIncreaseObserved=True,confirmedCopper=10000)
        self.tick()
        self.assertEqual(self.agent.data['mail']['status'],'confirmed')
        self.assertEqual(self.methods().count('buddy-mail.claim_stipend'),1)
    def test_unavailable_point_cost_wrong_type_expired_and_unknown_wallet_rejected(self):
        for key,value in [('usable',1),('pointCost',[1,0]),('requiredLevel',5)]:
            with self.subTest(key=key):
                self.rpc.offer['spells']=[dict(spellId=123,costCopper=1,**{key:value})]
                self.tick()
        self.rpc.offer['spells']=[{'spellId':123,'costCopper':1}]
        self.rpc.offer['trainerType']=2;self.tick();self.rpc.offer.pop('trainerType')
        self.rpc.offer['expiresInMs']=0;self.tick();self.rpc.offer['expiresInMs']=1000
        self.rpc.training['copperKnown']=False;self.tick()
        self.assertNotIn('buddy-training.buy',self.methods())
    def test_combat_movement_and_other_lease_block_services(self):
        self.rpc.world['owner']['unitFlags']=0x80000;self.tick();self.rpc.world['owner'].pop('unitFlags')
        for key in ['moving','motionUnconfirmed','castPending','autoAttacking','interactionOwner']:
            self.rpc.actions[key]=True;self.tick();self.rpc.actions.pop(key)
        self.assertNotIn('buddy-training.discover',self.methods())
    def test_stipend_once_persisted_before_claim_and_receipt_required(self):
        self.box();self.tick()
        self.assertEqual(self.saved[0]['mail']['status'],'reserved')
        self.assertEqual(self.saved[1]['mail']['status'],'confirmed')
        self.tick();self.assertEqual(self.methods().count('buddy-mail.claim_stipend'),1)
    def test_ambiguous_stipend_persists_across_restart(self):
        self.box();self.rpc.claim={'status':'CLAIM_REQUESTED'};self.tick()
        stored=self.saved[-1];self.agent=m.Maintenance('Arlen',1,self.rpc,lambda d:None,stored,now=lambda:200)
        self.agent.tick();self.assertEqual(self.methods().count('buddy-mail.claim_stipend'),1)
    def test_distant_mailbox_does_not_block_training(self):
        self.box(40);self.tick()
        self.assertNotIn('buddy-mail.list',self.methods());self.assertIn('buddy-training.buy',self.methods())
    def test_invalid_saved_identity_and_budget_rejected(self):
        self.tick();stored=self.saved[-1]
        for mutation in [('reservedCopper',99),('identity',{})]:
            bad=copy.deepcopy(stored);bad[mutation[0]]=mutation[1]
            with self.assertRaises(m.Error):m.Maintenance('Arlen',1,self.rpc,lambda d:None,bad)
    def test_wrong_character_never_mutates(self):
        self.rpc.world['self']['guid']='4'
        with self.assertRaises(m.Error):self.tick()
        self.assertNotIn('buddy-training.buy',self.methods())


class DurableClassTrainingTests(unittest.TestCase):
    setUp=MaintenanceTests.setUp
    tick=MaintenanceTests.tick
    methods=MaintenanceTests.methods

    def receipt(self,status='TRAINING_CONFIRMED',**extra):
        row=self.agent.data['training'][-1]
        self.rpc.purchase=dict(id=row['baselineId']+1,npcGuid=row['npc'],spellId=row['spell'],costCopper=row['cost'],
            status=status,serverSuccessObserved=status=='TRAINING_CONFIRMED',learnedSpellObserved=status=='TRAINING_CONFIRMED',**extra)

    def test_v1_migration_preserves_128_rows_totals_identity_and_ambiguous_tail(self):
        rows=[dict(session='old-body',npc=self.rpc.offer['npcGuid'],spell=i+1,cost=1,baselineId=0,
            status='reserved' if i==127 else 'confirmed') for i in range(128)]
        old=dict(identity=dict(name='Arlen',guid=1,ownerGuid=3,budgetCopper=10000),reservedCopper=128,training=rows,mail=None)
        untouched=copy.deepcopy(old)
        agent=m.Maintenance('Arlen',1,self.rpc,lambda d:None,old,now=lambda:100)
        self.assertEqual(old,untouched)
        self.assertEqual(agent.data['identity'],dict(version=2,name='Arlen',guid=1,ownerGuid=3))
        self.assertEqual(len(agent.data['training']),m.HISTORY_LIMIT)
        self.assertEqual(agent.data['training'][-1],rows[-1])
        self.assertEqual((agent.data['reservedCopper'],agent.data['spentCopper']),(128,127))
        agent.tick()
        self.assertNotIn('buddy-training.buy',self.methods())
        for mutation in ('identity','sum','pending_order'):
            broken=copy.deepcopy(old)
            if mutation=='identity':broken['identity']['guid']=4
            elif mutation=='sum':broken['reservedCopper']+=1
            else:broken['training'][0]['status']='reserved'
            with self.assertRaises(m.Error):m.Maintenance('Arlen',1,self.rpc,lambda d:None,broken)

    def test_migrated_exhausted_stipend_can_buy_a_real_offer_above_one_gold(self):
        old=dict(identity=dict(name='Arlen',guid=1,ownerGuid=3,budgetCopper=10000),reservedCopper=10000,
            training=[dict(session='old',npc=self.rpc.offer['npcGuid'],spell=99,cost=10000,baselineId=0,status='confirmed')],mail=None)
        self.agent=m.Maintenance('Arlen',1,self.rpc,self.agent.save,old,now=lambda:self.now[0])
        self.rpc.training['copper']=50100;self.rpc.offer['spells']=[dict(spellId=124,costCopper=50000)]
        self.tick()
        request=next(p for method,p in self.rpc.calls if method=='buddy-training.buy')
        self.assertEqual((request['expectedCostCopper'],request['maxSpendCopper'],request['reserveCopper']),(50000,50000,100))
        self.assertEqual(self.agent.data['reservedCopper'],60000)
        self.assertEqual(self.agent.data['spentCopper'],10000)

    def test_actual_wallet_reserve_and_explicit_cap_include_free_zero_wallet_offer(self):
        for wallet,cost,expected in ((199,100,False),(200,100,True),(0,0,True),(100,1,False),
                (1000100,1000000,True),(2000100,1000001,False)):
            with self.subTest(wallet=wallet,cost=cost):
                self.setUp();self.rpc.training['copper']=wallet;self.rpc.offer['spells']=[dict(spellId=123,costCopper=cost)]
                self.tick();buys=[p for method,p in self.rpc.calls if method=='buddy-training.buy']
                self.assertEqual(bool(buys),expected)
                if buys:self.assertGreaterEqual(buys[0]['maxSpendCopper'],1)

    def test_compaction_and_restart_after_150_purchases_preserve_counters_and_next_offer(self):
        self.rpc.training['copper']=100000
        for i in range(150):
            self.rpc.offer['spells']=[dict(spellId=1000+i,costCopper=100)]
            self.tick();self.receipt();self.rpc.offer['spells'][0]['usable']=2;self.tick()
        self.assertEqual(self.methods().count('buddy-training.buy'),150)
        self.assertEqual(len(self.agent.data['training']),m.HISTORY_LIMIT)
        self.assertEqual((self.agent.data['reservedCopper'],self.agent.data['spentCopper']),(15000,15000))
        stored=copy.deepcopy(self.saved[-1])
        self.agent=m.Maintenance('Arlen',1,self.rpc,self.agent.save,stored,now=lambda:self.now[0])
        self.rpc.offer['spells']=[dict(spellId=9999,costCopper=20000)]
        self.tick();self.assertEqual(self.methods().count('buddy-training.buy'),151)
        self.assertEqual(self.agent.data['reservedCopper'],35000)

    def test_new_body_positive_trainer_knowledge_is_distinct_from_old_receipt(self):
        self.tick();old_session=self.agent.data['training'][-1]['session']
        self.rpc.training['sessionId']='body2'
        self.rpc.offer['spells']=[dict(spellId=123,usable=2,costCopper=100),dict(spellId=124,costCopper=100)]
        self.tick()
        old=self.agent.data['training'][0]
        self.assertEqual((old['session'],old['knowledgeSession'],old['status']),(old_session,'body2','known_at_trainer'))
        self.assertEqual(self.agent.data['spentCopper'],0)
        self.assertEqual([p['spellId'] for method,p in self.rpc.calls if method=='buddy-training.buy'],[123,124])

    def test_same_body_known_row_requires_module_pending_retirement(self):
        self.tick();self.rpc.offer['spells']=[dict(spellId=123,usable=2,costCopper=100)]
        self.tick();self.assertEqual(self.agent.data['training'][-1]['status'],'reserved')
        self.receipt('KNOWN_AT_TRAINER',refreshedTrainerKnows=True)
        self.tick();self.assertEqual(self.agent.data['training'][-1]['status'],'known_at_trainer')
        self.assertEqual(self.agent.data['spentCopper'],0)
        self.assertFalse(self.rpc.purchase['serverSuccessObserved'])
        self.assertEqual(self.methods().count('buddy-training.buy'),1)

    def test_new_body_available_unavailable_missing_or_foreign_trainer_never_replays(self):
        for mode in ('available','unavailable','missing','foreign_npc'):
            self.setUp();self.tick();self.rpc.training['sessionId']='body2'
            if mode=='unavailable':self.rpc.offer['spells'][0]['usable']=1
            elif mode=='missing':self.rpc.offer['spells']=[]
            elif mode=='foreign_npc':self.rpc.offer['npcGuid']=str(int(self.rpc.offer['npcGuid'])+1)
            self.tick();self.assertEqual(self.agent.data['training'][-1]['status'],'reserved')
            self.assertEqual(self.methods().count('buddy-training.buy'),1)

    def test_definite_rejection_retries_only_on_fresh_actual_higher_level_and_at_most_three(self):
        self.tick();self.receipt('SERVER_REJECTED');self.tick()
        self.rpc.offer['revision']='999';self.tick()
        self.assertEqual(self.methods().count('buddy-training.buy'),1)
        for level in (5,6):
            self.rpc.world['self']['level']=level;self.tick()
            self.receipt('SERVER_REJECTED');self.tick()
        self.rpc.world['self']['level']=7;self.tick()
        self.assertEqual(self.methods().count('buddy-training.buy'),3)
        self.assertEqual(self.agent.data['spentCopper'],0)
        self.assertEqual(self.agent.data['rejections']['123']['attempts'],3)

    def test_rejection_requires_actual_matching_prerequisite_skill_progress(self):
        self.rpc.offer['spells'][0]['requiredSkill']=171
        self.rpc.world.update(selfSkillsKnown=True,selfSkills=[dict(slot=i) for i in range(128)])
        self.rpc.world['selfSkills'][0].update(skillId=171,value=1)
        self.tick();self.receipt('SERVER_REJECTED');self.tick()
        self.rpc.world['selfSkills'][1].update(skillId=182,value=99);self.tick()
        self.assertEqual(self.methods().count('buddy-training.buy'),1)
        self.rpc.world['selfSkills'][0]['value']=2;self.rpc.world['selfSkillsKnown']=False;self.tick()
        self.assertEqual(self.methods().count('buddy-training.buy'),1)
        self.rpc.world['selfSkillsKnown']=True;self.tick()
        self.assertEqual(self.methods().count('buddy-training.buy'),2)

    def test_ambiguous_write_never_retries_even_after_new_actual_level_or_skill(self):
        self.tick();self.rpc.world['self']['level']=80;self.tick()
        self.rpc.offer['revision']='9999';self.tick()
        self.assertEqual(self.methods().count('buddy-training.buy'),1)
        self.assertEqual(self.agent.data['training'][-1]['status'],'reserved')

    def test_v1_rejection_needs_a_new_evidence_baseline_then_higher_level(self):
        old=dict(identity=dict(name='Arlen',guid=1,ownerGuid=3,budgetCopper=10000),reservedCopper=100,
            training=[dict(session='old',npc=self.rpc.offer['npcGuid'],spell=123,cost=100,baselineId=0,status='rejected')],mail=None)
        self.agent=m.Maintenance('Arlen',1,self.rpc,self.agent.save,old,now=lambda:self.now[0])
        self.tick();self.tick();self.assertNotIn('buddy-training.buy',self.methods())
        self.rpc.world['self']['level']=5;self.tick()
        self.assertEqual(self.methods().count('buddy-training.buy'),1)

    def test_settlement_save_failure_latches_without_a_second_purchase(self):
        self.tick();self.receipt()
        self.agent.save=lambda d:(_ for _ in ()).throw(OSError('disk full'))
        with self.assertRaises(OSError):self.tick()
        self.rpc.offer['spells']=[dict(spellId=124,costCopper=100)]
        with self.assertRaises(m.Error):self.tick()
        self.assertEqual(self.methods().count('buddy-training.buy'),1)

    def test_rejection_and_counters_survive_restart_without_same_level_retry(self):
        self.tick();self.receipt('SERVER_REJECTED');self.tick()
        stored=copy.deepcopy(self.saved[-1])
        self.agent=m.Maintenance('Arlen',1,self.rpc,self.agent.save,stored,now=lambda:self.now[0])
        self.rpc.training['sessionId']='body2';self.tick()
        self.assertEqual(self.methods().count('buddy-training.buy'),1)
        self.rpc.world['self']['level']=5;self.tick()
        self.assertEqual(self.methods().count('buddy-training.buy'),2)
        self.assertEqual(self.agent.data['reservedCopper'],200)

    def test_unknown_skill_baseline_needs_real_later_progress_and_unknown_level_never_retries(self):
        self.rpc.offer['spells'][0]['requiredSkill']=171
        self.tick();self.receipt('SERVER_REJECTED');self.tick()
        self.rpc.world.update(selfSkillsKnown=True,selfSkills=[dict(slot=i) for i in range(128)])
        self.rpc.world['selfSkills'][0].update(skillId=171,value=10)
        self.tick();self.assertEqual(self.methods().count('buddy-training.buy'),1)
        self.rpc.world['selfSkills'][0]['value']=11
        self.rpc.world['self']['levelKnown']=False;self.tick()
        self.assertEqual(self.methods().count('buddy-training.buy'),1)
        self.rpc.world['self']['levelKnown']=True;self.tick()
        self.assertEqual(self.methods().count('buddy-training.buy'),2)

    def test_corrupt_v2_history_counters_and_retry_evidence_rejected(self):
        self.tick();stored=copy.deepcopy(self.saved[-1])
        for mode in ('spent','rows','pending','attempts','eligibility'):
            broken=copy.deepcopy(stored)
            if mode=='spent':broken['spentCopper']=101
            elif mode=='rows':broken['training']*=m.HISTORY_LIMIT+1
            elif mode=='pending':broken['training']*=2;broken['reservedCopper']=200
            elif mode=='attempts':broken['rejections']={'123':dict(attempts=4,eligibility=None)}
            else:broken['training'][0]['eligibility']['level']=81
            with self.assertRaises(m.Error):m.Maintenance('Arlen',1,self.rpc,lambda d:None,broken)

    def test_actual_v2_writer_journal_is_accepted_by_owned_running_chat_context_reader(self):
        self.tick();self.receipt();self.rpc.offer['spells'][0]['usable']=2;self.tick()
        with tempfile.TemporaryDirectory() as temp,patch.object(m.c,'ROOT',Path(temp)):
            folder=m.c.ensure_state(Path(temp)/'data/party-maintenance')
            m.c.write_json(folder/'character-1.json',self.agent.data)
            m.c.write_json(folder/'status.json',dict(identity=m.c.PARTY_MAINTENANCE_IDENTITY,phase='running',
                instance='d'*32,updatedAt=m.time.time(),characters=[dict(name=n,guid=g,phase='idle') for n,g,p in m.ROSTER]))
            with os.fdopen(m.c.private_open(folder/'lock',os.O_RDWR|os.O_CREAT),'r+') as lock:
                m.c.fcntl.flock(lock,m.c.fcntl.LOCK_EX)
                state=m.c.party_maintenance_snapshot(1)
                self.assertTrue(state['active']);self.assertTrue(state['own']['recordsKnown'])
                self.assertEqual(state['own']['confirmedSpellIds'],[123])
                self.assertEqual(state['own']['spentCopper'],100)
                self.assertEqual(state['own']['knownAtTrainerSpellIds'],[])
        bad=copy.deepcopy(self.agent.data);bad['spentCopper']=0
        with self.assertRaises(m.Error):m.Maintenance('Arlen',1,self.rpc,lambda d:None,bad)

    def test_integrated_v2_class_work_preempts_harvest_until_positive_knowledge_or_idle(self):
        self.tick()
        self.assertFalse(m.harvest.maintenance_idle(self.agent))
        buy=next(p for method,p in self.rpc.calls if method=='buddy-training.buy')
        self.assertEqual((buy['maxSpendCopper'],buy['reserveCopper']),(9900,100))
        self.rpc.training['sessionId']='body2'
        self.tick()
        self.assertEqual(self.agent.status['phase'],'waiting_for_training_confirmation')
        self.assertFalse(m.harvest.maintenance_idle(self.agent))
        self.assertEqual(self.methods().count('buddy-training.buy'),1)
        self.rpc.offer['spells'][0]['usable']=2
        self.tick()
        self.assertEqual(self.agent.data['training'][-1]['status'],'known_at_trainer')
        self.assertEqual(self.agent.data['spentCopper'],0)
        self.assertTrue(m.harvest.maintenance_idle(self.agent))
        self.rpc.training['copper']=0
        self.rpc.offer['spells']=[dict(spellId=124,costCopper=0)]
        self.tick()
        self.assertFalse(m.harvest.maintenance_idle(self.agent))
        self.assertEqual(self.agent.data['training'][-1]['status'],'reserved')
        free=[p for method,p in self.rpc.calls if method=='buddy-training.buy'][-1]
        self.assertEqual((free['maxSpendCopper'],free['reserveCopper']),(1,100))

if __name__=='__main__':unittest.main()
