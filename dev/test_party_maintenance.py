import copy
import hashlib
import importlib.machinery
import importlib.util
from pathlib import Path
import unittest
import threading
import math
import os
import tempfile
import types
import subprocess
from unittest.mock import patch
from test_town_policy import service_source, source_reply, policy_reply, bridge_factory

loader=importlib.machinery.SourceFileLoader('maintenance_test',str(Path(__file__).with_name('party-maintenance')))
spec=importlib.util.spec_from_loader(loader.name,loader)
m=importlib.util.module_from_spec(spec);loader.exec_module(m)
TRAINER_BRIDGE = m.town_policy.runtime.Bridge
MAILBOX = 0xf110000000000000 | 142075 << 24 | 1


def mailbox(guid=MAILBOX, x=0, **fields):
    return dict(dict(guid=str(guid),entry=(int(guid)>>24)&0xffffff,present=True,typeKnown=True,
        gameObjectType=19,mapKnown=True,mapId=0,positionKnown=True,x=x,y=0,z=0,positionRevision="1"),**fields)

def maintenance_source():
    # Keep the actual trainer export and compose the current stipend library;
    # the critical connected tests below use the generated full encounter.
    stipend=(Path(__file__).resolve().parent.parent/'behaviors/stipend-selection.lua').read_text()
    return ('local services=(function()\n'+service_source()+'\nend)()\nlocal stipend=(function()\n'+stipend+
        '\nend)()\nreturn {service_destination=services.service_destination,stipend_selection=stipend.decide,mailbox_target=stipend.mailbox_target}')

class RPC:
    def __init__(self):
        self.calls=[]
        self.world={'visitRevision':'1','self':{'guid':'1','present':True,'aliveKnown':True,'alive':True,'unitFlagsKnown':True,
            'mapKnown':True,'positionKnown':True,'positionRevision':'1','x':0,'y':0,'z':0,'levelKnown':True,'level':4},
            'owner':{'guid':'3','present':True,'aliveKnown':True,'alive':True,'unitFlagsKnown':True}}
        self.actions={'position':{'known':True,'mapKnown':True,'x':0,'y':0,'z':0,'revision':'1'}}
        self.group={'known':True,'inGroup':True,'leaderGuid':'3','members':[
            {'guid':'1','self':True,'online':True},{'guid':'3','name':'Joel','online':True}]}
        self.offer={'revision':'2','npcGuid':'17379390965327856171','expiresInMs':'20000','spells':[
            {'spellId':123,'costCopper':100,'requiredLevel':4}]}
        self.training={'sessionId':'body1','copperKnown':True,'copper':10000,'offer':self.offer}
        self.policy_source,self.generation=maintenance_source(),'1'
        self.mail={'sessionId':'body1','listKnown':True,'listCurrent':True,'listRevision':'2',
            'mailboxGuid':'17370386146390966273','stipends':[{'mailId':5,'copper':10000}]}
        self.claim={'mailId':5,'status':'CLAIM_CONFIRMED','serverAcknowledged':True,'walletIncreaseObserved':True,'confirmedCopper':10000}
        self.purchase={}
        self.fail_buy=False
        self.trainer_x,self.token,self.block_travel=None,None,None
        self.route_reason,self.before_acquire,self.after_travel='trainer_travel_within_distance',None,None
    def call(self,method,params):
        self.calls.append((method,copy.deepcopy(params)))
        if method=='session.policy_source':
            value=source_reply(self.policy_source,dict(self.training,selfGuid=self.world['self']['guid']),self.generation)
        elif method=='session.attach':
            value=policy_reply(self.policy_source,dict(self.training,selfGuid=self.world['self']['guid']),self.generation)
        elif method=='buddy-world.snapshot':value=self.world
        elif method=='buddy-party.state':value={'group':self.group}
        elif method=='buddy-actions.status':value=self.actions
        elif method=='buddy-training.discover':
            if self.trainer_x is None:value={'state':self.training,'nearby':[{'interactable':True,'distanceKnown':True,'distance':0,'creature':{'guid':self.offer['npcGuid']}}]}
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
        elif method=='buddy-mail.list':
            assert set(params)=={'mailboxGuid'},'mail list must bind the observed Lua-selected mailbox'
            self.mail['mailboxGuid']=params['mailboxGuid'];value=self.mail
        elif method=='buddy-mail.state':value=dict(self.mail,lastClaim=self.claim)
        elif method=='buddy-mail.claim_stipend':
            if m.confirmed_mail(self.claim,params['mailId']):self.training['copper']+=10000
            value={'sessionId':self.mail['sessionId'],'lastClaim':self.claim}
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
        bridge_patch=patch.object(m.town_policy.runtime,'Bridge',side_effect=bridge_factory(TRAINER_BRIDGE))
        bridge_patch.start();self.addCleanup(bridge_patch.stop)
        self.rpc=RPC();self.saved=[];self.now=[100]
        for name in ('quest_task_snapshot','area_quest_snapshot','party_town_snapshot'):
            mock=patch.object(m.c,name,return_value={'active':False});mock.start();self.addCleanup(mock.stop)
        mock=patch.object(m,'profession_active',return_value=False);mock.start();self.addCleanup(mock.stop)
        # Unit scenes must not inherit a real standing town controller's lock.
        mock=patch.object(m.harvest,'routine_locked',return_value=False);mock.start();self.addCleanup(mock.stop)
        self.agent=m.Maintenance('Arlen',1,self.rpc,lambda d:self.saved.append(copy.deepcopy(d)),now=lambda:self.now[0])
    def tick(self):self.agent.tick();self.now[0]+=11
    def methods(self):return [x[0] for x in self.rpc.calls]
    def box(self,x=0):
        self.rpc.world['mailboxes']=[mailbox(x=x)]
    def travel_rig(self, owner_present=True):
        self.rpc.trainer_x=50.
        self.rpc.world['self'].update(classKnown=True,classId=5,levelKnown=True,movementFlagsKnown=True)
        if owner_present:
            self.rpc.world['owner'].update(mapKnown=True,positionKnown=True,movementFlagsKnown=True,x=0.,y=0.,z=0.)
        else:self.rpc.world['owner']=None

    def inn_rig(self,name='Arlen',guid=1,class_id=5,start=(-9427.,56.,56.79)):
        self.rpc=InnRPC(guid,class_id,start)
        self.agent=m.Maintenance(name,guid,self.rpc,lambda d:self.saved.append(copy.deepcopy(d)),now=lambda:self.now[0])

    def complete_inn_offer(self):
        self.rpc.offer['spells']=[dict(spellId=123,usable=2,costCopper=100,requiredLevel=4,pointCost=[0,0])]

    def cache_inn_offer(self):
        self.complete_inn_offer()
        self.agent.remember_exhausted_trainer('body1',self.rpc.offer['npcGuid'],self.rpc.world,self.rpc.training,
            self.rpc.offer['spells'],set(),9900)

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

    def test_observed_lower_inn_trap_uses_verified_detour_before_stairs_and_training(self):
        self.inn_rig('Rook',5,4,(-9464.171,33.176174,56.96606));self.tick()
        routes=[p for method,p in self.rpc.calls if method=='buddy-actions.travel_to']
        self.assertEqual([tuple(p[k] for k in ('x','y','z')) for p in routes],
            [(-9467.,30.,56.96606)]+list(m.INN_STAIRS))
        self.assertTrue(all(p['arrivalDistance']==.75 for p in routes))
        self.assertEqual(self.methods().count('buddy-training.buy'),1)
        self.assertLess(max(i for i,(method,_) in enumerate(self.rpc.calls) if method=='buddy-actions.travel_to'),
            self.methods().index('buddy-training.list'))
        self.assertIsNone(self.rpc.token)

    def test_fresh_exhausted_inn_offer_returns_all_classes_without_changing_training_history_or_looping(self):
        for name,guid,class_id,start in (('Arlen',1,5,(-9462.666,31.836138,64.71143)),
                ('Mira',4,8,(-9471.999,32.260372,63.820427)),('Rook',5,4,(-9466.226,15.32826,63.820427))):
            with self.subTest(name=name):
                self.setUp();self.inn_rig(name,guid,class_id,start);self.complete_inn_offer()
                self.agent.data['training']=[dict(session='body1',spell=123,cost=100,npc=self.rpc.offer['npcGuid'],baselineId=0,status='confirmed')]
                self.agent.data.update(reservedCopper=100,spentCopper=100)
                history=copy.deepcopy(self.agent.data)
                self.rpc.actions.update(following=True,healing=True,assisting=True)
                self.tick()
                routes=[p for method,p in self.rpc.calls if method=='buddy-actions.travel_to']
                self.assertEqual([tuple(p[k] for k in ('x','y','z')) for p in routes],list(reversed(m.INN_STAIRS)))
                self.assertTrue(all(p['arrivalDistance']==.75 for p in routes))
                self.assertEqual(self.agent.data,history);self.assertEqual(self.saved,[])
                self.assertNotIn('buddy-training.buy',self.methods());self.assertIsNone(self.rpc.token)
                self.assertEqual(self.agent.status['exhaustedTrainerCount'],1)
                for _ in range(3):self.tick()
                self.assertEqual(self.methods().count('buddy-training.list'),1)
                self.assertEqual(self.methods().count('buddy-actions.travel_to'),4)
                self.assertNotIn('buddy-actions.follow',self.methods())
                self.assertTrue(all(self.rpc.actions[k] for k in ('following','healing','assisting')))

    def test_warm_inn_cache_returns_once_and_new_actual_level_can_establish_another_visit(self):
        upper=(-9466.226,15.32826,63.820427)
        self.inn_rig('Rook',5,4,upper);self.cache_inn_offer();self.tick()
        self.assertEqual(self.methods().count('buddy-actions.travel_to'),4)
        self.assertNotIn('buddy-training.list',self.methods())
        self.rpc.actions['position'].update(x=upper[0],y=upper[1],z=upper[2]);self.tick()
        self.assertEqual(self.methods().count('buddy-actions.travel_to'),4)
        self.rpc.world['self']['level']=6;self.tick()
        self.assertEqual(self.methods().count('buddy-training.list'),1)
        self.assertEqual(self.methods().count('buddy-actions.travel_to'),8)
        self.assertEqual(self.agent.status['exhaustedTrainerCount'],1)

    def test_inn_return_stops_on_human_return_combat_control_or_trainer_changes_and_keeps_cache(self):
        for mode in ('owner_returned','combat','control','trainer_absent','activity'):
            with self.subTest(mode=mode):
                self.setUp();self.inn_rig('Rook',5,4,(-9466.226,15.32826,63.820427));self.cache_inn_offer()
                def change(r):
                    if mode=='owner_returned':
                        r.world['owner']=dict(guid='3',present=True,aliveKnown=True,alive=True,unitFlagsKnown=True,
                            positionKnown=True,mapKnown=True,movementFlagsKnown=True,x=-9470.,y=20.,z=63.82)
                    elif mode=='combat':r.world['self']['unitFlags']=0x80000
                    elif mode=='control':r.actions['following']=True
                    elif mode=='activity':self.agent.activities=lambda:True
                    else:r.creatures=[c for c in r.creatures if c['guid']!=r.offer['npcGuid']]
                self.rpc.after_waypoint=change
                with self.assertRaises(m.Error):self.tick()
                self.assertEqual(self.methods().count('buddy-actions.travel_to'),1)
                self.assertIsNone(self.rpc.token)
                self.assertEqual(self.agent.status['exhaustedTrainerCount'],1)
                self.assertTrue(self.agent.exhausted_trainers[int(self.rpc.offer['npcGuid'])]['returnAttempted'])
                self.assertNotIn('buddy-training.buy',self.methods());self.assertEqual(self.saved,[])
                self.assertNotIn('buddy-actions.follow',self.methods())

    def test_inn_return_requires_actual_upper_floor_and_current_own_class_teacher(self):
        for mode in ('lower_floor','wrong_map','outside_inn','trainer_absent','trainer_dead'):
            with self.subTest(mode=mode):
                self.setUp();self.inn_rig('Rook',5,4,(-9466.226,15.32826,63.820427));self.cache_inn_offer()
                if mode=='lower_floor':self.rpc.actions['position'].update(x=-9464.171,y=33.176174,z=56.96606)
                elif mode=='wrong_map':
                    self.rpc.world['self']['mapId']=1;self.rpc.actions['position']['mapId']=1
                    for creature in self.rpc.creatures:creature['mapId']=1
                elif mode=='outside_inn':self.rpc.actions['position'].update(x=-9420.,y=60.,z=64.)
                elif mode=='trainer_absent':self.rpc.creatures=[]
                else:next(c for c in self.rpc.creatures if c['guid']==self.rpc.offer['npcGuid'])['alive']=False
                self.tick()
                self.assertNotIn('buddy-actions.travel_to',self.methods())
                self.assertNotIn('buddy-actions.acquire_interaction',self.methods())
                self.assertNotIn('buddy-training.buy',self.methods())

    def test_partial_or_timed_out_inn_return_has_one_finite_attempt_and_exact_cleanup(self):
        for mode in ('partial','false_arrival','deadline'):
            with self.subTest(mode=mode):
                self.setUp();self.inn_rig('Rook',5,4,(-9466.226,15.32826,63.820427));self.cache_inn_offer()
                clock=[1000.]
                if mode=='partial':self.rpc.waypoint_reason='exploration_navigation_unavailable'
                elif mode=='false_arrival':self.rpc.false_arrival=True
                else:self.rpc.after_waypoint=lambda r:clock.__setitem__(0,clock[0]+76)
                with patch.object(m.time,'monotonic',side_effect=lambda:clock[0]):
                    if mode=='deadline':
                        with self.assertRaises(m.Error):self.tick()
                    else:self.tick()
                self.assertIsNone(self.rpc.token)
                self.assertEqual(self.methods().count('buddy-actions.travel_to'),1)
                self.rpc.after_waypoint=None
                self.tick();self.assertEqual(self.methods().count('buddy-actions.travel_to'),1)
                self.assertNotIn('buddy-training.buy',self.methods());self.assertEqual(self.saved,[])

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

    def test_unavailable_town_status_with_positive_owned_lock_still_defers(self):
        with patch.object(m.harvest,'routine_locked',return_value=True) as locked:
            self.tick()
        locked.assert_called_once_with(m.ROOT/'data/party-town/1/lock')
        self.assertEqual(self.methods(),[])
        self.assertEqual(self.agent.status['phase'],'waiting_for_requested_routine')

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
        for mode in ('body','owner','intent','buff_intent'):
            with self.subTest(mode=mode):
                self.setUp();self.travel_rig()
                def change(r):
                    if mode=='body':r.training['sessionId']='replacement'
                    elif mode=='owner':r.world['owner']['x']=1.
                    elif mode=='buff_intent':r.actions['buffing']=True
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
        self.rpc.training['sessionId']=self.rpc.mail['sessionId']='body2';self.agent.tick()
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
        self.rpc.training['sessionId']=self.rpc.mail['sessionId']='body2'
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
            self.setUp();self.tick();self.rpc.training['sessionId']=self.rpc.mail['sessionId']='body2'
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
        self.rpc.training['sessionId']=self.rpc.mail['sessionId']='body2';self.tick()
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
        self.rpc.training['sessionId']=self.rpc.mail['sessionId']='body2'
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

class ExhaustedTrainerTests(unittest.TestCase):
    setUp=MaintenanceTests.setUp
    tick=MaintenanceTests.tick
    methods=MaintenanceTests.methods

    def complete_offer(self, usable=2, **changes):
        self.rpc.offer['spells']=[dict(spellId=123,usable=usable,costCopper=100,
            requiredLevel=4,pointCost=[0,0],**changes)]

    def test_exhausted_visit_never_queries_or_moves_again_same_level(self):
        self.complete_offer();self.rpc.world['owner']=None
        self.rpc.world['self'].update(classKnown=True,classId=5,movementFlagsKnown=True)
        self.rpc.trainer_x=20.
        self.tick();self.assertEqual(self.methods().count('buddy-actions.travel_trainer'),1)
        self.assertEqual(self.methods().count('buddy-training.list'),1)
        self.assertEqual(self.agent.status['exhaustedTrainerCount'],1)
        self.rpc.actions['position']['x']=0.
        for _ in range(3):self.tick()
        self.assertEqual(self.methods().count('buddy-actions.travel_trainer'),1)
        self.assertEqual(self.methods().count('buddy-training.list'),1)
        self.assertNotIn('buddy-training.buy',self.methods())
        self.rpc.training['copper']+=1;self.tick()
        self.assertEqual(self.methods().count('buddy-training.list'),1)

    def test_level_body_and_actual_relevant_prerequisite_progress_reopen(self):
        self.complete_offer(1,requiredSkill=171)
        self.rpc.world.update(selfSkillsKnown=True,selfSkills=[dict(slot=i) for i in range(128)])
        self.rpc.world['selfSkills'][0].update(skillId=171,value=1)
        self.tick();self.tick();self.assertEqual(self.methods().count('buddy-training.list'),1)
        self.rpc.world['selfSkills'][1].update(skillId=43,value=20)
        self.tick();self.assertEqual(self.methods().count('buddy-training.list'),1)
        self.rpc.world['selfSkills'][0]['value']=2
        self.tick();self.assertEqual(self.methods().count('buddy-training.list'),2)
        self.rpc.world['self']['level']=5
        self.tick();self.assertEqual(self.methods().count('buddy-training.list'),3)
        self.rpc.training['sessionId']=self.rpc.mail['sessionId']='body2'
        self.tick();self.assertEqual(self.methods().count('buddy-training.list'),4)
        self.assertEqual(self.agent.exhausted_body,'body2')

    def test_unaffordable_offer_reopens_only_when_actual_budget_reaches_price(self):
        self.complete_offer(0);self.rpc.training['copper']=150
        self.tick();self.tick();self.assertEqual(self.methods().count('buddy-training.list'),1)
        self.rpc.training['copper']=151;self.tick()
        self.assertEqual(self.methods().count('buddy-training.list'),1)
        self.rpc.training['copper']=200;self.tick()
        self.assertEqual(self.methods().count('buddy-training.list'),2)
        self.assertEqual(self.methods().count('buddy-training.buy'),1)

    def test_errors_pending_rejected_unknown_offers_never_establish_exhaustion(self):
        for mode in ('error','pending','rejected','unknown_points','unknown_status','unknown_level','unknown_skill','unknown_ability'):
            with self.subTest(mode=mode):
                self.setUp();self.complete_offer()
                if mode=='pending':self.rpc.purchase=dict(status='UNCONFIRMED_TIMEOUT')
                elif mode=='rejected':self.rpc.purchase=dict(status='SERVER_REJECTED')
                elif mode=='unknown_status':self.rpc.purchase=dict(status='MYSTERY')
                elif mode=='unknown_points':self.rpc.offer['spells'][0].pop('pointCost')
                elif mode=='unknown_level':self.rpc.world['self']['levelKnown']=False
                elif mode=='unknown_skill':self.complete_offer(1,requiredSkill=171)
                elif mode=='unknown_ability':self.complete_offer(1,requiredAbilities=[1000])
                old=self.rpc.call
                def call(method,params):
                    if mode=='error' and method=='buddy-training.list':raise m.Error('list response lost')
                    result=old(method,params)
                    if method=='buddy-training.list' and mode in ('pending','rejected','unknown_status'):
                        result['lastPurchase']=copy.deepcopy(self.rpc.purchase)
                    return result
                self.rpc.call=call
                if mode=='error':
                    with self.assertRaises(m.Error):self.tick()
                else:self.tick()
                self.assertFalse(self.agent.exhausted_trainers)

    def test_cache_is_bounded_and_new_body_drops_all_old_teacher_keys(self):
        self.complete_offer();body='body1';world=self.rpc.world;state=self.rpc.training
        for n in range(140):
            self.agent.remember_exhausted_trainer(body,int(self.rpc.offer['npcGuid'])+n,world,state,
                self.rpc.offer['spells'],set(),9900)
        self.assertEqual(len(self.agent.exhausted_trainers),128)
        self.rpc.training['sessionId']=self.rpc.mail['sessionId']='new-body';self.rpc.offer['expiresInMs']=0
        self.tick();self.assertFalse(self.agent.exhausted_trainers)
        self.assertEqual(self.agent.exhausted_body,'new-body')

class HarvestStatusTests(unittest.TestCase):
    def test_fresh_combat_deferral_still_reports_configured_harvest_without_any_worker_rpc(self):
        actor=types.SimpleNamespace(next_query=10,status=dict(phase='waiting_out_of_combat'),data=dict(training=[],mail=None))
        worker=m.harvest.Worker(5,types.SimpleNamespace(url='http://127.0.0.1:8088/rpc'),priority=lambda:False)
        worker.tick=lambda:self.fail('Combat must not invoke harvest RPCs')
        m.harvest_after_maintenance(actor,worker,9)
        self.assertEqual((actor.status['harvest']['guid'],actor.status['harvest']['skillId']),(5,393))
        self.assertEqual(actor.status['harvest']['reason'],'maintenance_out_of_combat')
        self.assertNotIn('lastReceipt',actor.status['harvest'])

    def test_last_priority_harvest_keeps_exact_wait_reason_without_changing_training_cache(self):
        actor=types.SimpleNamespace(next_query=10,status=dict(phase='no_affordable_unattempted_class_training'),
            data=dict(training=[],mail=None),exhausted_trainers={9:'retained'})
        worker=m.harvest.Worker(1,types.SimpleNamespace(url='http://127.0.0.1:8086/rpc'),priority=lambda:False)
        calls=[]
        def tick():
            calls.append('tick')
            raise m.harvest.Error('Harvest yields to observed party combat or healing')
        worker.tick=tick
        m.harvest_after_maintenance(actor,worker,9)
        self.assertEqual(actor.status['harvest']['reason'],'combat_or_healing')
        self.assertEqual(actor.exhausted_trainers,{9:'retained'})
        self.assertEqual(actor.status['phase'],'no_affordable_unattempted_class_training')
        m.harvest_after_maintenance(actor,worker,10)
        actor.status['phase']='waiting_for_training_confirmation'
        m.harvest_after_maintenance(actor,worker,9)
        actor.status['phase']='no_affordable_unattempted_class_training'
        actor.data['training']=[dict(status='reserved')]
        m.harvest_after_maintenance(actor,worker,9)
        self.assertEqual(calls,['tick'])

class StipendConnectedConsumerTests(unittest.TestCase):
    """Connected accepted source chooses; native mail and the journal execute."""
    tick=MaintenanceTests.tick
    methods=MaintenanceTests.methods
    box=MaintenanceTests.box

    @classmethod
    def setUpClass(cls):
        cls.bundle=subprocess.check_output(['python3','-B',str(m.ROOT/'dev/encounter-bundle')],text=True)
        cls.stipend=(m.ROOT/'behaviors/stipend-selection.lua').read_text()
        if cls.bundle.count(cls.stipend)!=1:
            raise AssertionError('actual encounter bundle must contain exactly one current stipend module')

    def setUp(self):
        MaintenanceTests.setUp(self)
        self.reset_case()

    def reset_case(self):
        self.rpc=RPC();self.rpc.policy_source=self.bundle
        self.rpc.offer['spells']=[]
        self.saved=[];self.now=[100]
        self.agent=m.Maintenance('Arlen',1,self.rpc,lambda data:self.saved.append(copy.deepcopy(data)),now=lambda:self.now[0])
        self.box()

    def edit_source(self,source):
        self.rpc.policy_source=source
        self.rpc.generation=str(int(self.rpc.generation)+1)

    def last_row_source(self):
        marker='if row.copper == 10000 and selected == nil then selected = row end'
        self.assertEqual(self.bundle.count(marker),1)
        return self.bundle.replace(marker,'if row.copper == 10000 then selected = row end',1)

    def assert_no_reservation_or_claim(self):
        self.assertIsNone(self.agent.data['mail'])
        self.assertNotIn('buddy-mail.claim_stipend',self.methods())
        self.assertEqual(self.saved,[])

    def test_actual_full_bundle_selects_exact_request_and_reserves_before_native_call(self):
        self.rpc.mail['stipends']=[dict(mailId=9,copper=10000),dict(mailId=5,copper=10000)]
        selection=m.stipend_policy.choose(self.rpc,copy.deepcopy(self.rpc.mail),1)
        self.assertEqual(selection['mailId'],'9')
        self.assertEqual(selection['policySHA256'],hashlib.sha256(self.bundle.encode()).hexdigest())
        self.assertTrue(m.stipend_policy.current(self.rpc,selection,1))
        original=self.rpc.call
        def observed_call(method,params):
            if method=='buddy-mail.claim_stipend':
                self.assertEqual(self.saved[-1]['mail']['status'],'reserved')
                self.assertEqual({key:self.saved[-1]['mail'][key] for key in params},params)
            return original(method,params)
        self.rpc.call=observed_call
        self.tick()
        request=next(params for method,params in self.rpc.calls if method=='buddy-mail.claim_stipend')
        self.assertEqual(request,dict(sessionId='body1',listRevision='2',mailboxGuid='17370386146390966273',mailId='9'))
        self.assertEqual(self.agent.data['mail']['status'],'reserved')

    def test_connected_source_edit_changes_next_selection_without_python_ranking(self):
        self.rpc.mail['stipends']=[dict(mailId=5,copper=10000),dict(mailId=9,copper=10000)]
        first=m.stipend_policy.choose(self.rpc,copy.deepcopy(self.rpc.mail),1)
        self.assertEqual(first['mailId'],'5')
        self.edit_source(self.last_row_source())
        self.assertFalse(m.stipend_policy.current(self.rpc,first,1))
        self.tick()
        request=next(params for method,params in self.rpc.calls if method=='buddy-mail.claim_stipend')
        self.assertEqual(request['mailId'],'9')
        self.assertEqual(self.saved[-1]['mail']['policySHA256'],hashlib.sha256(self.rpc.policy_source.encode()).hexdigest())

    def test_missing_export_fault_or_unobserved_lua_request_cannot_fallback(self):
        export='stipend_selection=encounter_stipend_selection and encounter_stipend_selection.decide'
        self.assertEqual(self.bundle.count(export),1)
        self.assertEqual(self.bundle.count('mailId=selected.mailId'),1)
        variants=(self.bundle.replace(export,'stipend_selection=false',1),
            self.bundle+'\ninvalid =',self.bundle.replace('mailId=selected.mailId','mailId="999"',1))
        for source in variants:
            with self.subTest(source_length=len(source)):
                self.reset_case();self.edit_source(source)
                with self.assertRaises(m.Error):self.tick()
                self.assert_no_reservation_or_claim()

    def test_unknown_or_reserved_native_list_waits_and_bad_later_row_never_claims(self):
        for field,value in (('listKnown',False),('listCurrent',False),('claimReserved',True),('operationInFlight',True)):
            with self.subTest(field=field):
                self.reset_case();self.rpc.mail[field]=value;self.tick();self.assert_no_reservation_or_claim()
        self.reset_case();self.rpc.mail['stipends'].append(dict(mailId=6,copper=False))
        try:self.tick()
        except m.Error:pass
        self.assert_no_reservation_or_claim()

    def test_source_generation_body_or_mail_change_fences_before_reservation(self):
        for mode in ('source','generation','body','list_revision','mailbox','later_row','reserved','reserved_type'):
            with self.subTest(mode=mode):
                self.reset_case();original=self.rpc.call;changed=[False];listed=[False]
                def changing_read(method,params):
                    if method=='buddy-mail.list':listed[0]=True
                    if listed[0] and not changed[0] and method==('buddy-mail.state' if mode in ('list_revision','mailbox','later_row','reserved','reserved_type') else 'session.attach'):
                        changed[0]=True
                        if mode=='source':self.edit_source(self.last_row_source())
                        elif mode=='generation':self.rpc.generation='2'
                        elif mode=='body':self.rpc.world['self']['guid']='4'
                        elif mode=='list_revision':self.rpc.mail['listRevision']='3'
                        elif mode=='mailbox':self.rpc.mail['mailboxGuid']='17370386146390966274'
                        elif mode=='later_row':self.rpc.mail['stipends'].append(dict(mailId=6,copper=10000))
                        elif mode=='reserved_type':self.rpc.mail['claimReserved']=0
                        else:self.rpc.mail['claimReserved']=True
                    return original(method,params)
                self.rpc.call=changing_read
                with self.assertRaises(m.Error):self.tick()
                self.assertTrue(changed[0]);self.assert_no_reservation_or_claim()

    def test_source_change_after_real_evaluation_has_no_reservation(self):
        binary=Path(os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY',m.ROOT/'data/bin/luapolicy'))
        real=m.stipend_policy.runtime.Bridge(binary,m.ROOT/'modules',execution_ms=1000)
        self.assertEqual(real.value_bytes,32768)
        owner=self
        class EditAfterEvaluation:
            def evaluate(self,source,frame):
                result=real.evaluate(source,frame)
                if 'mail' in frame:owner.edit_source(owner.last_row_source())
                return result
        with patch.object(m.stipend_policy.runtime,'Bridge',return_value=EditAfterEvaluation()):
            with self.assertRaises(m.Error):self.tick()
        self.assert_no_reservation_or_claim()

    def test_post_persist_source_or_mail_veto_keeps_reserved_unknown_without_attempt(self):
        for mode in ('source','mail'):
            with self.subTest(mode=mode):
                self.reset_case()
                self.rpc.claim={}
                def save(data):
                    self.saved.append(copy.deepcopy(data))
                    if data['mail'] is not None:
                        if mode=='source':self.edit_source(self.last_row_source())
                        else:self.rpc.mail['listRevision']='3'
                self.agent.save=save
                with self.assertRaises(m.Error):self.tick()
                self.assertEqual(self.agent.data['mail']['status'],'reserved')
                self.assertEqual(self.saved[-1]['mail']['status'],'reserved')
                self.assertNotIn('buddy-mail.claim_stipend',self.methods())
                self.agent=m.Maintenance('Arlen',1,self.rpc,lambda data:None,self.saved[-1],now=lambda:200)
                self.agent.tick()
                self.assertEqual(self.agent.data['mail']['status'],'reserved')
                self.assertNotIn('buddy-mail.claim_stipend',self.methods())

    def test_unknown_native_claim_retains_custody_across_broken_export_and_restart(self):
        self.rpc.claim=dict(mailId=5,status='CLAIM_REQUESTED');self.tick()
        self.assertEqual(self.methods().count('buddy-mail.claim_stipend'),1)
        stored=copy.deepcopy(self.saved[-1]);self.assertEqual(stored['mail']['status'],'reserved')
        broken=('local p=(function()\n'+self.stipend+'\nend)()\n'
            'p.decide=function(input)error("broken stipend export")end return p')
        self.edit_source(self.bundle.replace(self.stipend,broken,1))
        self.tick()
        self.agent=m.Maintenance('Arlen',1,self.rpc,lambda data:None,stored,now=lambda:200)
        self.agent.tick()
        self.assertEqual(self.agent.data['mail']['status'],'reserved')
        self.assertEqual(self.methods().count('buddy-mail.claim_stipend'),1)
        self.rpc.claim.update(status='CLAIM_CONFIRMED',serverAcknowledged=True,walletIncreaseObserved=True,confirmedCopper=10000)
        self.agent.next_query=0;self.agent.tick()
        self.assertEqual(self.agent.data['mail']['status'],'confirmed')
        self.assertEqual(self.methods().count('buddy-mail.claim_stipend'),1)

    def test_wrong_or_missing_native_reply_session_keeps_reserved_across_tick_and_restart(self):
        for response_session in ('different-body',None):
            with self.subTest(response_session=response_session):
                self.reset_case();original=self.rpc.call
                def mismatched_reply(method,params):
                    reply=original(method,params)
                    if method=='buddy-mail.claim_stipend':
                        if response_session is None:reply.pop('sessionId')
                        else:reply['sessionId']=response_session
                        # Later reads provide no matching receipt to settle the
                        # reserved operation independently of this bad reply.
                        self.rpc.claim={}
                    return reply
                self.rpc.call=mismatched_reply
                self.tick()
                self.assertEqual(self.agent.data['mail']['status'],'reserved')
                self.assertEqual(self.saved[-1]['mail']['status'],'reserved')
                self.assertEqual(len(self.saved),1)
                self.tick()
                stored=copy.deepcopy(self.saved[-1])
                self.agent=m.Maintenance('Arlen',1,self.rpc,lambda data:None,stored,now=lambda:200)
                self.agent.tick()
                self.assertEqual(self.agent.data['mail']['status'],'reserved')
                self.assertEqual(self.methods().count('buddy-mail.claim_stipend'),1)


class StipendSelectionTests(unittest.TestCase):
    """Standalone source proof through the normal default-budget Lua CLI."""
    @classmethod
    def setUpClass(cls):
        binary=Path(os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY',m.ROOT/'data/bin/luapolicy'))
        if not binary.is_file() or not os.access(binary,os.X_OK):
            raise RuntimeError('actual Lua evaluator required: set BUDDY_LUAPOLICY_TEST_BINARY or build data/bin/luapolicy')
        cls.bridge=m.town_policy.runtime.Bridge(binary,m.ROOT/'modules')
        if cls.bridge.execution_ms is not None or cls.bridge.value_bytes!=32768:
            raise RuntimeError('stipend source proof requires unchanged default evaluation budgets')
        cls.source=(m.ROOT/'behaviors/stipend-selection.lua').read_text()

    def frame(self):
        return dict(sessionId='stipend-session',selfGuid='1',mail=dict(sessionId='stipend-session',
            listKnown=True,listCurrent=True,listRevision='2',mailboxGuid='17370386146390966273',
            claimReserved=False,operationInFlight=False,stipends=[dict(mailId='5',copper=10000)]))

    def evaluate(self,frame,source=None):
        before=copy.deepcopy(frame)
        result=self.bridge.evaluate(self.source if source is None else source,frame)
        self.assertEqual(frame,before)
        self.assertIsInstance(result,dict)
        if result.get('kind')=='wait':
            self.assertEqual(set(result),{'kind','reason'})
            self.assertIsInstance(result['reason'],str)
            self.assertTrue(1<=len(result['reason'])<=128)
        else:
            self.assertEqual(set(result),{'kind','sessionId','listRevision','mailboxGuid','mailId'})
            self.assertEqual(result['kind'],'claim')
            for key in ('sessionId','listRevision','mailboxGuid','mailId'):
                self.assertIsInstance(result[key],str)
        return result

    def assert_wait(self,frame):
        self.assertEqual(self.evaluate(frame)['kind'],'wait')

    def test_first_actual_qualified_row_and_reordering_preserve_exact_request(self):
        frame=self.frame()
        frame['mail']['stipends']=[dict(mailId='12',copper=9999),dict(mailId='9',copper=10000),dict(mailId='5',copper=10000)]
        self.assertEqual(self.evaluate(frame),dict(kind='claim',sessionId='stipend-session',listRevision='2',
            mailboxGuid='17370386146390966273',mailId='9'))
        frame['mail']['stipends'].reverse()
        self.assertEqual(self.evaluate(frame)['mailId'],'5')
        frame['mail']['stipends']=[dict(mailId='5',copper=9999)]
        self.assert_wait(frame)

    def test_actual_source_edit_changes_selection_or_disables_fresh_claim(self):
        frame=self.frame();frame['mail']['stipends'].append(dict(mailId='9',copper=10000))
        self.assertEqual(self.evaluate(frame)['mailId'],'5')
        marker='if row.copper == 10000 and selected == nil then selected = row end'
        self.assertEqual(self.source.count(marker),1,'actual first-row choice clause must remain explicit')
        edited=self.source.replace(marker,'if row.copper == 10000 then selected = row end',1)
        self.assertEqual(self.evaluate(frame,edited)['mailId'],'9')
        tuning='local claim_training_stipend = true'
        self.assertEqual(self.source.count(tuning),1)
        disabled=self.source.replace(tuning,'local claim_training_stipend = false',1)
        self.assertEqual(self.evaluate(frame,disabled),dict(kind='wait',reason='training_stipend_claim_disabled'))
        self.assertEqual(self.evaluate(frame)['mailId'],'5')

    def test_full_fifty_rows_and_all_later_rows_are_checked_before_selection(self):
        frame=self.frame();frame['mail']['stipends']=[dict(mailId=str(i+1),copper=10000) for i in range(50)]
        self.assertEqual(self.evaluate(frame)['mailId'],'1')
        for bad in (dict(mailId='50',copper=False),dict(mailId='1',copper=10000),dict(mailId='50')):
            with self.subTest(bad=bad):
                invalid=copy.deepcopy(frame);invalid['mail']['stipends'][-1]=bad;self.assert_wait(invalid)
        invalid=copy.deepcopy(frame);invalid['mail']['stipends'][-1]=None
        with self.assertRaises(m.town_policy.runtime.BridgeFailure):self.bridge.evaluate(self.source,invalid)
        frame['mail']['stipends'].append(dict(mailId='51',copper=10000));self.assert_wait(frame)

    def test_current_known_unreserved_list_is_required(self):
        for field,value in (('listKnown',False),('listCurrent',False),('claimReserved',True),('operationInFlight',True),
                ('listKnown',1),('listCurrent','true'),('claimReserved',0),('operationInFlight',None)):
            with self.subTest(field=field,value=value):
                frame=self.frame();frame['mail'][field]=value;self.assert_wait(frame)
        for field in ('sessionId','listKnown','listCurrent','listRevision','mailboxGuid','claimReserved','operationInFlight','stipends'):
            with self.subTest(missing=field):
                frame=self.frame();del frame['mail'][field];self.assert_wait(frame)

    def test_canonical_full_uint64_identities_are_never_rounded(self):
        frame=self.frame();frame['selfGuid']='18446744073709551613'
        frame['mail'].update(listRevision='18446744073709551615',mailboxGuid='18446744073709551614',
            stipends=[dict(mailId='4294967295',copper=10000)])
        result=self.evaluate(frame)
        self.assertEqual((result['listRevision'],result['mailboxGuid'],result['mailId']),
            ('18446744073709551615','18446744073709551614','4294967295'))
        for field in ('selfGuid','listRevision','mailboxGuid'):
            for value in ('0','01','18446744073709551616',1,True,1.5):
                with self.subTest(field=field,value=value):
                    bad=copy.deepcopy(frame);(bad if field=='selfGuid' else bad['mail'])[field]=value;self.assert_wait(bad)
        for value in ('0','01','4294967296',1,True,1.5):
            with self.subTest(mailId=value):
                bad=copy.deepcopy(frame);bad['mail']['stipends'][0]['mailId']=value;self.assert_wait(bad)

    def test_malformed_shapes_or_body_mismatch_never_claim(self):
        for mutate in (lambda frame:frame.update(eligible=True),lambda frame:frame.update(mail=False),
                lambda frame:frame['mail'].update(sessionId='replaced'),lambda frame:frame['mail'].update(stipends={'1':dict(mailId='5',copper=10000)}),
                lambda frame:frame['mail']['stipends'][0].update(eligible=True),lambda frame:frame['mail']['stipends'][0].update(copper=-1),
                lambda frame:frame['mail']['stipends'][0].update(copper=4294967296),lambda frame:frame['mail']['stipends'][0].update(copper=10000.5)):
            frame=self.frame();mutate(frame);self.assert_wait(frame)
        for field,value in (('sessionId',''),('sessionId','s'*129),('selfGuid',None)):
            frame=self.frame();frame[field]=value;self.assert_wait(frame)

    def test_actual_evaluator_fault_has_no_stock_policy_fallback(self):
        for source in ('return {decide=', 'return {decide=function()while true do end end}', 'return {}'):
            with self.subTest(source=source):
                with self.assertRaises(m.town_policy.runtime.Error):self.bridge.evaluate(source,self.frame())
        self.assertEqual(self.evaluate(self.frame())['mailId'],'5')


class MailboxTargetTests(unittest.TestCase):
    """The real pure selector runs through the unchanged default-5ms CLI."""
    @classmethod
    def setUpClass(cls):
        binary=Path(os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY',m.ROOT/'data/bin/luapolicy'))
        if not binary.is_file() or not os.access(binary,os.X_OK):
            raise RuntimeError('actual Lua evaluator required for mailbox selection')
        cls.bridge=m.stipend_policy.runtime.Bridge(binary,m.ROOT/'modules')
        if cls.bridge.execution_ms is not None or cls.bridge.value_bytes!=32768:
            raise RuntimeError('mailbox source proof requires unchanged default budgets')
        cls.policy=(m.ROOT/'behaviors/stipend-selection.lua').read_text()

    def frame(self):
        return dict(sessionId='mailbox-session',selfGuid='1',visitId='7',
            mail=dict(sessionId='mailbox-session',claimReserved=False,operationInFlight=False),
            self=dict(guid='1',present=True,mapKnown=True,mapId=0,positionKnown=True,
                positionRevision='11',aliveKnown=True,alive=True,unitFlagsKnown=True,unitFlags=0),
            actions=dict(moving=False,motionUnconfirmed=False,
                castPending=False,autoAttacking=False,autoAttackRequested=False,autoAttackUnconfirmed=False,
                interactionOwner='',traveling=False,practicing=False,position=dict(known=True,mapKnown=True,
                    mapId=0,x=0,y=0,z=0,estimated=True,revision='23')),
            mailboxes=[mailbox()])

    def evaluate(self,frame,source=None):
        before=copy.deepcopy(frame)
        code=self.policy if source is None else source
        wrapped='local p=(function()\n'+code+'\nend)()\nreturn {decide=p.mailbox_target}'
        result=self.bridge.evaluate(wrapped,frame)
        self.assertEqual(frame,before)
        self.assertIsInstance(result,dict)
        if result.get('kind')=='wait':
            self.assertEqual(set(result),{'kind','reason'})
            self.assertIsInstance(result['reason'],str)
            self.assertTrue(1<=len(result['reason'])<=128)
        else:
            self.assertEqual(set(result),{'kind','sessionId','selfGuid','visitId','mailboxGuid'})
            self.assertEqual(result['kind'],'list')
            for key in ('sessionId','selfGuid','visitId'):
                self.assertEqual(result[key],frame[key])
            self.assertIsInstance(result['mailboxGuid'],str)
        return result

    def assert_wait(self,frame):
        self.assertEqual(self.evaluate(frame)['kind'],'wait')

    def test_nearest_three_dimensional_mailbox_and_exact_high_guid_ties(self):
        frame=self.frame()
        first,second=mailbox(MAILBOX,x=1),mailbox(MAILBOX+1,x=0,z=1)
        self.assertEqual(float(first['guid']),float(second['guid']))
        for rows in ([second,first],[first,second]):
            frame['mailboxes']=rows
            self.assertEqual(self.evaluate(frame)['mailboxGuid'],str(MAILBOX))
        frame['mailboxes']=[first,second,mailbox(MAILBOX+2,x=.5)]
        self.assertEqual(self.evaluate(frame)['mailboxGuid'],str(MAILBOX+2))
        frame['mailboxes']=[mailbox(x=3,y=4)]
        self.assertEqual(self.evaluate(frame)['mailboxGuid'],str(MAILBOX))
        frame['mailboxes']=[mailbox(z=5.01)]
        self.assert_wait(frame)

    def test_current_source_edits_change_rank_and_radius_without_a_python_choice(self):
        frame=self.frame();frame['mailboxes']=[mailbox(x=1),mailbox(MAILBOX+1,x=4)]
        self.assertEqual(self.evaluate(frame)['mailboxGuid'],str(MAILBOX))
        rank='local prefer_nearest_mailbox = true'
        self.assertEqual(self.policy.count(rank),1)
        self.assertEqual(self.evaluate(frame,self.policy.replace(rank,
            'local prefer_nearest_mailbox = false',1))['mailboxGuid'],str(MAILBOX+1))
        radius='local mailbox_interaction_yards = 5'
        self.assertEqual(self.policy.count(radius),1)
        frame['mailboxes']=[mailbox(x=4)]
        self.assertEqual(self.evaluate(frame,self.policy.replace(radius,
            'local mailbox_interaction_yards = 3',1))['kind'],'wait')

    def test_all_thirty_two_rows_are_validated_before_returning_an_earlier_choice(self):
        frame=self.frame();frame['mailboxes']=[mailbox(MAILBOX+i,x=i) for i in range(32)]
        self.assertEqual(self.evaluate(frame)['mailboxGuid'],str(MAILBOX))
        for bad in (dict(frame['mailboxes'][-1],entry=True),dict(frame['mailboxes'][-1],guid=str(MAILBOX)),
                dict(frame['mailboxes'][-1],positionRevision='01'),dict(frame['mailboxes'][-1],x='31')):
            with self.subTest(bad=bad):
                malformed=copy.deepcopy(frame);malformed['mailboxes'][-1]=bad;self.assert_wait(malformed)
        frame['mailboxes'].append(mailbox(MAILBOX+32,x=32));self.assert_wait(frame)

    def test_old_unknown_or_unusable_observations_never_supply_an_implicit_mailbox(self):
        for fields in (dict(present=False),dict(typeKnown=False),dict(gameObjectType=0),
                dict(mapKnown=False),dict(mapId=1),dict(positionKnown=False),dict(positionRevision='0')):
            with self.subTest(fields=fields):
                frame=self.frame();frame['mailboxes']=[mailbox(**fields)];self.assert_wait(frame)
        for boxes in ([],[dict(present=True,mapKnown=True,positionKnown=True,x=0,y=0,z=0)]):
            frame=self.frame();frame['mailboxes']=boxes;self.assert_wait(frame)
        for mutate in (lambda f:f['self'].update(positionKnown=False),
                lambda f:f['self'].update(unitFlagsKnown=False),
                lambda f:f['actions']['position'].update(known=False),
                lambda f:f['actions'].update(moving=True)):
            frame=self.frame();mutate(frame);self.assert_wait(frame)

    def test_raw_mail_custody_is_lua_eligibility_and_malformed_facts_never_select(self):
        guard='if input.mail.claimReserved or input.mail.operationInFlight then return wait("mail_operation_reserved_or_busy") end'
        self.assertEqual(self.policy.count(guard),1)
        for field in ('claimReserved','operationInFlight'):
            frame=self.frame();frame['mail'][field]=True
            self.assertEqual(self.evaluate(frame),dict(kind='wait',reason='mail_operation_reserved_or_busy'))
            self.assertEqual(self.evaluate(frame,self.policy.replace(guard,'-- editable query eligibility',1))['kind'],'list')
        for fields in (dict(sessionId='other-body'),dict(claimReserved=0),dict(operationInFlight='false')):
            frame=self.frame();frame['mail'].update(fields);self.assert_wait(frame)
        frame=self.frame();del frame['mail'];self.assert_wait(frame)

    def test_body_visit_revision_and_typed_frame_boundaries_are_exact(self):
        frame=self.frame();frame['selfGuid']=frame['self']['guid']='18446744073709551615'
        frame['visitId']='18446744073709551614'
        self.assertEqual(self.evaluate(frame)['selfGuid'],'18446744073709551615')
        for mutate in (lambda f:f.update(sessionId=''),lambda f:f['self'].update(guid='2'),
                lambda f:f.update(visitId='0'),lambda f:f.update(visitId='01'),
                lambda f:f.update(selfGuid='18446744073709551616'),
                lambda f:f['actions']['position'].update(revision=True),
                lambda f:f['self'].update(positionRevision=1),
                lambda f:f.update(mailboxes={'1':mailbox()}),
                lambda f:f['mailboxes'][0].update(gameObjectType=True),
                lambda f:f.update(eligible=True)):
            malformed=self.frame();mutate(malformed);self.assert_wait(malformed)
        numeric_guid=self.frame();numeric_guid['mailboxes'][0]['guid']=MAILBOX
        with self.assertRaisesRegex(m.stipend_policy.runtime.Error,'outside exact JSON integer range'):
            self.evaluate(numeric_guid)
        null_position=self.frame();null_position['mailboxes'][0]['x']=None
        self.assert_wait(null_position)
        for bad in (float('inf'),float('nan')):
            malformed=self.frame();malformed['mailboxes'][0]['x']=bad
            with self.subTest(non_json_position=bad),self.assertRaises(m.stipend_policy.runtime.Error):
                self.evaluate(malformed)


class MailboxTargetConsumerTests(unittest.TestCase):
    tick=MaintenanceTests.tick
    methods=MaintenanceTests.methods
    box=MaintenanceTests.box
    reset_case=StipendConnectedConsumerTests.reset_case
    setUp=StipendConnectedConsumerTests.setUp
    edit_source=StipendConnectedConsumerTests.edit_source

    @classmethod
    def setUpClass(cls):
        cls.bundle=subprocess.check_output(['python3','-B',str(m.ROOT/'dev/encounter-bundle')],text=True)
        cls.stipend=(m.ROOT/'behaviors/stipend-selection.lua').read_text()
        if cls.bundle.count(cls.stipend)!=1:
            raise AssertionError('full accepted bundle must contain exactly one current mailbox/stipend module')

    def assert_no_list_or_claim(self):
        self.assertNotIn('buddy-mail.list',self.methods())
        self.assertNotIn('buddy-mail.claim_stipend',self.methods())
        self.assertIsNone(self.agent.data['mail'])
        self.assertEqual(self.saved,[])

    def choose(self):
        return m.stipend_policy.mailbox_target(self.rpc,copy.deepcopy(self.rpc.world),copy.deepcopy(self.rpc.actions),1)

    def test_actual_full_bundle_lists_exact_selected_mailbox_and_ignores_stale_list_target(self):
        # Actions may own an estimated pose with its independent local revision.
        # Selection must bind these facts without inventing a server ACK.
        self.rpc.actions['position'].update(estimated=True,revision='23')
        self.rpc.world['self']['positionRevision']='11'
        self.assertNotIn('sessionId',self.rpc.actions)
        self.assertNotIn('selfGuid',self.rpc.actions)
        self.rpc.world['mailboxes']=[mailbox(x=4),mailbox(MAILBOX+1,x=1)]
        self.rpc.mail['mailboxGuid']=str(MAILBOX+99)
        self.tick()
        requests=[params for method,params in self.rpc.calls if method=='buddy-mail.list']
        self.assertEqual(requests,[dict(mailboxGuid=str(MAILBOX+1))])
        claim=next(params for method,params in self.rpc.calls if method=='buddy-mail.claim_stipend')
        self.assertEqual(claim['mailboxGuid'],str(MAILBOX+1))
        self.assertEqual(self.agent.data['mail']['status'],'confirmed')

    def test_optional_actual_actions_identity_is_validated_when_present(self):
        self.rpc.actions.update(sessionId='body1',selfGuid='1')
        self.tick()
        self.assertEqual([p for method,p in self.rpc.calls if method=='buddy-mail.list'],[dict(mailboxGuid=str(MAILBOX))])
        for field,value in (('sessionId','other-body'),('sessionId',None),('sessionId',1),
                ('selfGuid','4'),('selfGuid',None),('selfGuid',1),('selfGuid',True)):
            with self.subTest(field=field,value=value):
                self.reset_case();self.rpc.actions[field]=value
                try:self.tick()
                except m.Error:pass
                self.assert_no_list_or_claim()

    def test_same_consumer_uses_current_accepted_rank_and_radius_before_listing(self):
        self.rpc.world['mailboxes']=[mailbox(x=1),mailbox(MAILBOX+1,x=4)]
        selected=self.choose();self.assertEqual(selected['mailboxGuid'],str(MAILBOX))
        rank='local prefer_nearest_mailbox = true';self.assertEqual(self.bundle.count(rank),1)
        self.edit_source(self.bundle.replace(rank,'local prefer_nearest_mailbox = false',1))
        self.assertFalse(m.stipend_policy.mailbox_current(self.rpc,selected,1))
        self.tick()
        self.assertEqual([p for method,p in self.rpc.calls if method=='buddy-mail.list'],[dict(mailboxGuid=str(MAILBOX+1))])
        self.reset_case();self.box(4)
        self.assertEqual(self.choose()['kind'],'list')
        radius='local mailbox_interaction_yards = 5';self.assertEqual(self.bundle.count(radius),1)
        self.edit_source(self.bundle.replace(radius,'local mailbox_interaction_yards = 3',1))
        self.tick();self.assert_no_list_or_claim()

    def test_malformed_later_rows_unknown_or_far_mailboxes_never_fall_back_to_empty_list(self):
        for boxes in ([mailbox(x=40)],[mailbox(typeKnown=False)],
                [dict(present=True,positionKnown=True,mapKnown=True,x=0,y=0,z=0)],
                [mailbox(),mailbox(MAILBOX+1,entry=True)],
                [mailbox(),mailbox()]):
            with self.subTest(boxes=boxes):
                self.reset_case();self.rpc.world['mailboxes']=boxes
                try:self.tick()
                except m.Error:pass
                self.assert_no_list_or_claim()

    def test_body_visit_position_mailbox_source_and_generation_drift_before_list_veto(self):
        for mode in ('body','visit','position','own_position_revision','actions_position_revision','estimated',
                'mailbox','source','generation'):
            with self.subTest(mode=mode):
                self.reset_case();original=m.stipend_policy.mailbox_target;changed=[]
                def changed_after_choice(*args,**kwargs):
                    selected=original(*args,**kwargs);self.assertEqual(selected['kind'],'list')
                    changed.append(mode)
                    if mode=='body':self.rpc.world['self']['guid']='4'
                    elif mode=='visit':self.rpc.world['visitRevision']='2'
                    elif mode=='position':self.rpc.actions['position']['x']=1
                    elif mode=='own_position_revision':self.rpc.world['self']['positionRevision']='2'
                    elif mode=='actions_position_revision':self.rpc.actions['position']['revision']='2'
                    elif mode=='estimated':self.rpc.actions['position']['estimated']=True
                    elif mode=='mailbox':self.rpc.world['mailboxes'][0]['positionRevision']='2'
                    elif mode=='source':self.edit_source(self.bundle+'\n-- current accepted source edit\n')
                    else:self.rpc.generation='2'
                    return selected
                with patch.object(m.stipend_policy,'mailbox_target',side_effect=changed_after_choice):
                    try:self.tick()
                    except m.Error:pass
                self.assertEqual(changed,[mode]);self.assert_no_list_or_claim()

    def test_accepted_lua_owns_busy_query_choice_while_claim_custody_stays_unchanged(self):
        self.rpc.mail['claimReserved']=True
        self.tick();self.assert_no_list_or_claim()
        guard='if input.mail.claimReserved or input.mail.operationInFlight then return wait("mail_operation_reserved_or_busy") end'
        self.assertEqual(self.bundle.count(guard),1)
        self.edit_source(self.bundle.replace(guard,'-- permit an observed list query while claim capacity is used',1))
        self.tick()
        self.assertEqual([p for method,p in self.rpc.calls if method=='buddy-mail.list'],[dict(mailboxGuid=str(MAILBOX))])
        self.assertNotIn('buddy-mail.claim_stipend',self.methods())
        self.assertIsNone(self.agent.data['mail']);self.assertEqual(self.saved,[])

    def test_list_reply_session_or_mailbox_mismatch_never_reaches_stipend_claim(self):
        for field,value in (('sessionId','other-body'),('mailboxGuid',str(MAILBOX+1)),('sessionId',None),('mailboxGuid',None)):
            with self.subTest(field=field,value=value):
                self.reset_case();original=self.rpc.call
                def bad_reply(method,params):
                    reply=original(method,params)
                    if method=='buddy-mail.list':
                        if value is None:reply.pop(field,None)
                        else:reply[field]=value
                    return reply
                self.rpc.call=bad_reply
                try:self.tick()
                except m.Error:pass
                self.assertEqual([p for method,p in self.rpc.calls if method=='buddy-mail.list'],[dict(mailboxGuid=str(MAILBOX))])
                self.assertNotIn('buddy-mail.claim_stipend',self.methods())
                self.assertIsNone(self.agent.data['mail']);self.assertEqual(self.saved,[])

    def test_post_list_fact_or_source_drift_is_rechecked_before_any_claim_reservation(self):
        for mode in ('visit','source'):
            with self.subTest(mode=mode):
                self.reset_case();original=self.rpc.call
                def move_after_listing(method,params):
                    reply=original(method,params)
                    if method=='buddy-mail.list':
                        if mode=='visit':self.rpc.world['visitRevision']='2'
                        else:self.edit_source(self.bundle+'\n-- source changed during list query\n')
                    return reply
                self.rpc.call=move_after_listing
                try:self.tick()
                except m.Error:pass
                self.assertEqual(self.methods().count('buddy-mail.list'),1)
                self.assertNotIn('buddy-mail.claim_stipend',self.methods())
                self.assertIsNone(self.agent.data['mail']);self.assertEqual(self.saved,[])

    def test_pending_claim_reconciles_without_reselecting_or_listing_after_source_loss(self):
        self.rpc.claim=dict(mailId=5,status='CLAIM_REQUESTED');self.tick()
        stored=copy.deepcopy(self.saved[-1])
        self.assertEqual(stored['mail']['status'],'reserved')
        self.edit_source('return {}')
        with patch.object(m.stipend_policy,'mailbox_target',side_effect=AssertionError('pending claim must not reselect')):
            try:self.tick()
            except m.Error:pass
            self.agent=m.Maintenance('Arlen',1,self.rpc,lambda data:None,stored,now=lambda:200)
            try:self.agent.tick()
            except m.Error:pass
            self.assertEqual(self.agent.data['mail']['status'],'reserved')
            self.rpc.claim.update(status='CLAIM_CONFIRMED',serverAcknowledged=True,
                walletIncreaseObserved=True,confirmedCopper=10000)
            self.agent.next_query=0
            try:self.agent.tick()
            except m.Error:pass
            self.assertEqual(self.agent.data['mail']['status'],'confirmed')
        self.assertEqual(self.methods().count('buddy-mail.list'),1)
        self.assertEqual(self.methods().count('buddy-mail.claim_stipend'),1)

    def test_missing_faulted_or_unobserved_mailbox_export_has_no_native_default(self):
        export='mailbox_target=encounter_stipend_selection and encounter_stipend_selection.mailbox_target'
        self.assertEqual(self.bundle.count(export),1)
        wrapped='local p=(function()\n'+self.bundle+'\nend)()\np.mailbox_target=function(input) '
        variants=(self.bundle.replace(export,'mailbox_target=false',1),self.bundle+'\ninvalid =',
            wrapped+'error("mailbox export failure") end return p',
            wrapped+'return {kind="list",sessionId=input.sessionId,selfGuid=input.selfGuid,visitId=input.visitId,mailboxGuid="'+str(MAILBOX+99)+'"} end return p')
        for source in variants:
            with self.subTest(source_length=len(source)):
                self.reset_case();self.edit_source(source)
                with self.assertRaises(m.Error):self.tick()
                self.assert_no_list_or_claim()


from test_town_policy import VendorDestinationTests, TrainerDestinationTests
from test_town_maintenance import GearCompatibilityTests, TownTests

if __name__=='__main__':unittest.main()
