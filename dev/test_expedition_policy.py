"""Actual Go Lua/descriptor bridge plus a private fake public-client endpoint."""
import copy
import importlib.machinery
import importlib.util
import math
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent.parent
loader = importlib.machinery.SourceFileLoader('expedition_host_tested', str(ROOT / 'dev/task-runtime'))
spec = importlib.util.spec_from_loader(loader.name, loader)
host = importlib.util.module_from_spec(spec); loader.exec_module(host)
BINARY = os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY')


class Endpoint:
    url = 'http://127.0.0.1:8090/rpc'
    def __init__(self):
        self.calls, self.lease = [], ''
        self.guid, self.sid = '87', 'aw-offline-test'
        self.npc = str((0xf130 << 48) | (1234 << 24) | 99)
        self.pose = dict(known=True, mapKnown=True, mapId=0, x=10, y=0, z=5, revision='5', estimated=True)
        self.quest = dict(sessionId=self.sid, questLogKnown=True, questLogRevision='1', questLog=[dict(questId=701, complete=True)] + [{} for _ in range(24)])
        self.choices, self.nav_reason, self.fail_claim, self.foreign_receipt = [], 'exploration_within_distance', False, False
        self.accept_requested = False
        self.active_quest = 701
        self.active_baseline = '1'
        self.choices_by_quest = {}
        self.no_reward_offer = set()
        self.giver_x,self.plan_endpoints,self.plan_error=10,[],''

    def actions(self):
        return dict(sessionId=self.sid, selfGuid=self.guid, position=copy.deepcopy(self.pose),
                    interactionOwner=self.lease and 'exploration' or '', interactionLeaseRemainingMs=30000 if self.lease else 0)

    def discover(self):
        self.quest['nearby'] = [dict(creature=dict(guid=self.npc, entry=1234, present=True, aliveKnown=True, alive=True,
            mapKnown=True, mapId=0, positionKnown=True, x=self.giver_x, y=0, z=5, positionRevision='6', unitFlagsKnown=True, npcFlags=2),
            distanceKnown=True, distance=abs(self.pose['x'] - self.giver_x), canInteract=abs(self.pose['x'] - self.giver_x) <= 4)]

    def call(self, method, params, timeout=5):
        self.calls.append((method, copy.deepcopy(params)))
        if method == 'buddy-actions.status': return self.actions()
        if method == 'buddy-world.snapshot':
            return {'self': dict(guid=self.guid, present=True, aliveKnown=True, alive=True, mapKnown=True, mapId=0,
                unitFlagsKnown=True, movementFlagsKnown=True, healthKnown=True, health=100, maxHealth=100,
                positionKnown=True, x=-9000, y=90, z=60, classId=2)}
        if method == 'buddy-quests.state': return copy.deepcopy(self.quest)
        if method == 'buddy-quests.discover': self.discover(); return copy.deepcopy(self.quest)
        if method == 'buddy-actions.plan_route_leg':
            assert not self.lease and params['sessionId']==self.sid and params['selfGuid']==self.guid
            assert params['expectedPositionRevision']==self.pose['revision']
            if self.plan_error:raise host.Error(self.plan_error)
            endpoint=self.plan_endpoints.pop(0) if self.plan_endpoints else {key:params.get(key,0) for key in ('x','y','z')}
            length=math.sqrt(sum((self.pose.get(key,0)-endpoint.get(key,0))**2 for key in ('x','y','z')))
            # Fake geometry honors the real planner's per-leg bound too.
            if length>180:
                endpoint={key:self.pose.get(key,0)+(endpoint.get(key,0)-self.pose.get(key,0))*180/length for key in ('x','y','z')}
                length=180
            reached=all(abs(endpoint.get(key,0)-params.get(key,0))<.001 for key in ('x','y','z'))
            return dict(sessionId=self.sid,selfGuid=self.guid,start=copy.deepcopy(self.pose),
                endpoint=dict(known=True,mapKnown=True,**endpoint),pathLength=length,pointCount=math.ceil(length/2)+1,
                goalReached=reached,serverPositionRevision='9')
        if method == 'buddy-actions.acquire_interaction':
            assert not self.lease and params == {'owner': 'exploration', 'leaseMs': 30000}
            self.lease = 'actual-private-token-'+str(sum(name==method for name,_ in self.calls)); return dict(token=self.lease, state=self.actions())
        if method == 'buddy-actions.travel_to':
            assert params['token'] == self.lease
            if self.nav_reason == 'exploration_within_distance':
                self.pose.update(x=params.get('x', 0), y=params.get('y', 0), z=params.get('z', 0), revision=str(int(self.pose['revision']) + 1))
            value = self.actions(); value['reason'] = self.nav_reason
            return value
        if method == 'buddy-actions.release_interaction':
            assert params == {'owner': 'exploration', 'token': self.lease}
            self.lease = ''; return self.actions()
        if method == 'buddy-quests.turn_in':
            assert params['npcGuid'] == self.npc and params['questId'] in (701,703) and params['questLogRevision'] == self.quest['questLogRevision']
            self.active_quest = params['questId']
            self.active_baseline = self.quest['questLogRevision']
            self.quest['turnInOffer'] = {} if self.active_quest in self.no_reward_offer else dict(revision='3', questId=self.active_quest, npcGuid=self.npc, source='reward_offer',
                canComplete=True, expiresInMs='10000', choices=self.choices_by_quest.get(self.active_quest,self.choices))
        elif method == 'buddy-quests.claim_reward':
            assert params['npcGuid'] == self.npc and params['questId'] == self.active_quest and params['offerRevision'] == '3'
            self.quest['turnInOffer']['rewardRequested'] = True
            if self.fail_claim: raise host.Error('rpc_result_unconfirmed')
        elif method == 'buddy-quests.offer':
            self.quest['offer'] = dict(revision='8', npcGuid=self.npc, expiresInMs='10000',
                                       quests=[dict(questId=702, available=True)])
        elif method == 'buddy-quests.accept':
            assert params['npcGuid'] == self.npc and params['questId'] == 702 and params['offerRevision'] == '8'
            self.accept_requested = True; self.quest['offer']['quests'][0]['acceptRequested'] = True
        else: raise AssertionError(method)
        return dict(transportWritten=True, serverResponseReceived=True, state=copy.deepcopy(self.quest))

    def confirm_reward(self):
        self.quest['questLog'] = [row if row.get('questId')!=self.active_quest else {} for row in self.quest['questLog']]
        self.quest['questLogRevision'] = str(int(self.quest['questLogRevision'])+1)
        self.quest['lastReward'] = dict(questId=self.active_quest, npcGuid=self.npc, offerRevision='3', status='REWARD_CONFIRMED',
            baselineLogRevision=self.active_baseline, confirmedLogRevision=self.quest['questLogRevision'], serverRewardObserved=True, logRemovalConfirmed=True)

    def confirm_acceptance(self):
        self.quest['questLog'][0] = dict(questId=702)
        self.quest['questLogRevision'] = '3'
        self.quest['lastAcceptance'] = dict(questId=702, npcGuid=self.npc, offerRevision='8', status='CONFIRMED_IN_LOG',
            baselineLogRevision='2', confirmedLogRevision='3')


@unittest.skipUnless(BINARY, 'set BUDDY_LUAPOLICY_TEST_BINARY to the compiled local bridge')
class ExpeditionTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(dir='/private/tmp'); self.addCleanup(temporary.cleanup)
        self.folder = Path(temporary.name) / 'state'
        self.rpc = Endpoint(); self.rpc.discover()
        self.bridge = host.Bridge(BINARY, ROOT / 'modules')
        self.source = ROOT / 'behaviors/expedition.lua'
        self.goal = dict(id='generic-expedition', kind='journal_sync', params=dict(targetQuestIds=[702],
            initialHandInQuestIds=[701], handIns=[dict(questId=701, npcEntry=1234, mapId=0, x=10, y=0, z=5, followupQuestId=702)],
            rendezvous={'known': False}), allow=['buddy-actions.status', 'buddy-world.snapshot', 'buddy-quests.state',
            'buddy-quests.discover', 'buddy-quests.turn_in', 'buddy-quests.claim_reward', 'buddy-quests.offer', 'buddy-quests.accept',
            'buddy-actions.acquire_interaction', 'buddy-actions.travel_to', 'buddy-actions.release_interaction'],
            observations=dict(world=dict(method='buddy-world.snapshot', params={}, select=['self']),
                actions=dict(method='buddy-actions.status', params={}, select=['sessionId', 'selfGuid', 'position',
                    'interactionOwner', 'interactionLeaseRemainingMs', 'moving', 'traveling', 'motionUnconfirmed']),
                quests=dict(method='buddy-quests.state', params={}, select=['sessionId', 'questLogKnown', 'questLogRevision',
                    'questLog', 'nearby', 'turnInOffer', 'lastReward', 'offer', 'lastAcceptance', 'actionInFlight'])))
        self.goal['allow'].append('buddy-actions.plan_route_leg')

    def runner(self, store):
        return host.Runner(store, self.bridge, self.rpc, 87, self.rpc.sid, self.goal, self.source,
                           ROOT / 'behaviors/task-stack.lua', rpc_timeout=35)

    def count(self, method): return sum(name == method for name, _ in self.rpc.calls)

    def until(self, runner, predicate, limit=12):
        for _ in range(limit):
            runner.tick()
            if predicate(): return
        self.fail(str(host.status(runner.data)))

    def configured_pickup_goal(self):
        self.rpc.quest['questLog']=[{} for _ in range(25)];self.rpc.quest['questLogRevision']='2'
        self.goal['params'].update(handIns=[],initialHandInQuestIds=[],targetQuestIds=[702],
            pickups=[dict(questId=702,npcEntry=1234,mapId=0,x=10,y=0,z=5)])

    def test_configured_missing_target_pickup_requires_actual_offer_and_exact_own_log_receipt(self):
        self.configured_pickup_goal()
        with host.Store(self.folder) as store:
            runner=self.runner(store);self.until(runner,lambda:self.rpc.accept_requested)
            request=next(p for name,p in self.rpc.calls if name=='buddy-quests.accept')
            self.assertEqual(request['questId'],702);self.assertEqual(request['npcGuid'],self.rpc.npc)
            self.assertEqual(request['offerRevision'],'8');self.assertFalse(runner.data['done'])
            runner.tick();self.assertNotIn('702',runner.data['memory'].get('accepted',{}))
            self.rpc.confirm_acceptance();good=copy.deepcopy(self.rpc.quest['lastAcceptance'])
            for key,value in (('npcGuid','999'),('questId',999),('offerRevision','7'),('baselineLogRevision','1')):
                self.rpc.quest['lastAcceptance']=dict(good,**{key:value});runner.tick()
                self.assertNotIn('702',runner.data['memory'].get('accepted',{}))
            self.rpc.quest['lastAcceptance']=good;runner.tick()
            self.assertIn('702',runner.data['memory']['accepted'])
            self.assertTrue(runner.data['memory']['journalSync']['targetIdsPresent'])
            self.assertEqual(runner.data['memory']['journalSync']['unfinishedQuestIds'],[702])
            self.assertFalse(runner.data['memory'].get('completed'));self.assertFalse(runner.data['done'])
            self.assertEqual(self.count('buddy-quests.accept'),1)

    def test_configured_target_already_present_is_not_accepted_again_or_marked_complete(self):
        self.configured_pickup_goal();self.rpc.quest['questLog'][0]={'questId':702}
        self.goal['params']['pickups'].append(dict(questId=704,npcEntry=1234,mapId=0,x=10,y=0,z=5))
        with host.Store(self.folder) as store:
            runner=self.runner(store)
            for _ in range(3):runner.tick()
            self.assertEqual(self.count('buddy-quests.offer'),0);self.assertEqual(self.count('buddy-quests.accept'),0)
            self.assertFalse(runner.data['memory']['journalSync']['missingQuestIds'])
            self.assertEqual(runner.data['memory']['journalSync']['unfinishedQuestIds'],[702])
            self.assertFalse(runner.data['memory'].get('accepted'));self.assertFalse(runner.data['memory'].get('completed'))

    def test_blocked_configured_pickup_defers_and_allows_another_target(self):
        for mode in ('missing_giver','not_offered'):
            with self.subTest(mode=mode),host.Store(self.folder.parent/mode) as store:
                self.rpc=Endpoint();self.rpc.discover();self.configured_pickup_goal()
                self.goal['params']['targetQuestIds']=[704,702]
                self.goal['params']['pickups'].insert(0,dict(questId=704,npcEntry=9999 if mode=='missing_giver' else 1234,mapId=0,x=10,y=0,z=5))
                runner=self.runner(store);self.until(runner,lambda:self.rpc.accept_requested,limit=20)
                self.assertIn('pickup:704',runner.data['memory']['deferredQuests'])
                self.assertEqual(next(p['questId'] for name,p in self.rpc.calls if name=='buddy-quests.accept'),702)
                self.rpc.confirm_acceptance();runner.tick()
                self.assertEqual(runner.data['memory']['journalSync']['missingQuestIds'],[704])
                count=self.count('buddy-quests.offer')
                for _ in range(3):runner.tick()
                self.assertEqual(self.count('buddy-quests.offer'),count)
                self.assertEqual(self.count('buddy-quests.accept'),1)

    def test_unknown_configured_pickup_accept_resolves_only_exact_receipt_without_replay(self):
        self.configured_pickup_goal();original=self.rpc.call
        def call(method,params,timeout=5):
            value=original(method,params,timeout)
            if method=='buddy-quests.accept':raise host.Error('rpc_result_unconfirmed')
            return value
        self.rpc.call=call
        with host.Store(self.folder) as store:
            runner=self.runner(store);self.until(runner,lambda:bool(runner.data['pending']))
            runner.tick();self.assertEqual(runner.data['phase'],'blocked_on_ambiguous_call')
            self.rpc.confirm_acceptance();runner.tick()
            self.assertFalse(runner.data['pending']);self.assertEqual(self.count('buddy-quests.accept'),1)
            runner.tick();self.assertIn('702',runner.data['memory']['accepted'])
            self.assertFalse(runner.data['memory'].get('completed'))

    def test_rendezvous_uses_existing_verified_legs_and_records_arrival_without_objective_credit(self):
        self.configured_pickup_goal();self.rpc.quest['questLog'][0]={'questId':702}
        self.goal['params']['rendezvous']=dict(known=True,mapId=0,x=220,y=0,z=5)
        with host.Store(self.folder) as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['phase']=='journal_ids_present_rendezvous_arrived',limit=24)
            arrival=runner.data['memory']['rendezvous']
            self.assertEqual(arrival['status'],'arrived');self.assertEqual(arrival['sessionId'],self.rpc.sid)
            self.assertEqual(arrival['positionRevision'],'7');self.assertEqual(arrival['x'],220)
            self.assertTrue(arrival['estimated']);self.assertFalse(runner.data['done'])
            self.assertEqual(runner.data['memory']['journalSync']['unfinishedQuestIds'],[702])
            self.assertEqual(self.count('buddy-actions.travel_to'),2)
            self.assertEqual(self.count('buddy-actions.release_interaction'),2)
            self.assertEqual(self.count('buddy-quests.accept'),0);self.assertEqual(self.count('buddy-quests.claim_reward'),0)
            self.assertFalse(runner.data['memory'].get('completed'))

    def test_rendezvous_block_or_unknown_reference_never_claims_arrival_or_loops_movement(self):
        for mode in ('blocked','invalid_reference'):
            with self.subTest(mode=mode),host.Store(self.folder.parent/mode) as store:
                self.rpc=Endpoint();self.rpc.discover();self.configured_pickup_goal();self.rpc.quest['questLog'][0]={'questId':702}
                self.goal['params']['rendezvous']=dict(known=True,mapId=0,x=220,y=0,z=5)
                if mode=='blocked':self.rpc.nav_reason='exploration_navigation_unavailable'
                else:self.goal['params']['rendezvous'].pop('z')
                runner=self.runner(store)
                phase='rendezvous_blocked' if mode=='blocked' else 'rendezvous_reference_invalid'
                self.until(runner,lambda:runner.data['phase']==phase,limit=16)
                count=self.count('buddy-actions.travel_to')
                for _ in range(3):runner.tick()
                self.assertEqual(self.count('buddy-actions.travel_to'),count)
                self.assertEqual(count,1 if mode=='blocked' else 0)
                self.assertNotEqual(runner.data['memory'].get('rendezvous',{}).get('status'),'arrived')
                self.assertFalse(runner.data['memory'].get('completed'));self.assertFalse(runner.data['pending'])

    def test_progress_health_short_wait_is_quiet_then_stable_stall_is_fresh_and_observed_progress_clears(self):
        source='local tasks=(function()\n'+(ROOT/'behaviors/task-stack.lua').read_text()+'\nend)();\n'+self.source.read_text()
        state=dict(body=dict(sessionId=self.rpc.sid,selfGuid='87'),goal=copy.deepcopy(self.goal),memory={},pending=[],now=100,
                   observations=dict(actions=self.rpc.actions(),world=self.rpc.call('buddy-world.snapshot',{}),
                                     quests=copy.deepcopy(self.rpc.quest)))
        state['observations']['quests']['questLog']=[{} for _ in range(25)]
        def evaluate(now):
            state['now']=now;out=self.bridge.evaluate(source,state);state['memory']=out['memory'];return out
        for now in (100,110,129):
            out=evaluate(now);self.assertNotIn('health',out['memory']);self.assertNotIn('action',out)
        # An old sibling deferral must not relabel the current wait.
        state['memory']['deferredQuests']={'hand_in:999':dict(reason='travel_route_plan_unavailable')}
        stalled=evaluate(130)['memory']['health']
        self.assertEqual(stalled['phase'],'remaining_objectives_rendezvous_unknown')
        again=evaluate(140)['memory']['health']
        self.assertEqual((stalled['code'],stalled['phase']),(again['code'],again['phase']))
        self.assertEqual(stalled['noProgressSince'],100);self.assertEqual(again['since'],140)
        state['observations']['quests']['questLogRevision']='2'  # New actual own-log observation.
        self.assertNotIn('health',evaluate(141)['memory'])
        self.assertNotIn('health',evaluate(170)['memory'])
        self.assertEqual(evaluate(171)['memory']['health']['noProgressSince'],141)

    def test_native_travel_grace_is_bounded_and_never_counts_as_progress(self):
        source='local tasks=(function()\n'+(ROOT/'behaviors/task-stack.lua').read_text()+'\nend)();\n'+self.source.read_text()
        source=source.replace('policy.decide=function(state) return progress(state,decide(state)) end',
            'policy.decide=function(state) return progress(state,{memory=state.memory,phase="traveling_verified_native_route",action=state.testAction}) end')
        state=dict(body=dict(sessionId=self.rpc.sid,selfGuid='87'),memory=dict(progress=dict(since=100,seen={},count=0)),
                   observations={},now=200,pending=[],testAction=dict(method='buddy-actions.travel_to'))
        out=self.bridge.evaluate(source,state)
        self.assertNotIn('health',out['memory']);self.assertEqual(out['memory']['progress']['since'],100)
        state['testAction']=None
        state['pending']=[dict(id='unchanged',method='buddy-actions.travel_to',outcome='pending',reservedAt=200)]
        original=copy.deepcopy(state['pending'])
        state['now']=239
        self.assertNotIn('health',self.bridge.evaluate(source,state)['memory'])
        state['now']=240
        self.assertEqual(self.bridge.evaluate(source,state)['memory']['health']['noProgressSince'],100)
        self.assertEqual(state['pending'],original)

    def test_route_read_failure_stall_exposes_concrete_deferral_without_retry_and_receipt_clears(self):
        self.rpc.giver_x=310;self.rpc.discover();self.rpc.plan_error='actual_native_no_physical_route'
        self.goal['params']['handIns'][0]['x']=310
        clock=[1000.0]
        with patch.object(host.time,'time',side_effect=lambda:clock[0]),host.Store(self.folder) as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['phase']=='waiting_for_deferred_quests')
            self.assertNotIn('health',runner.data['memory']);self.assertEqual(self.count('buddy-actions.plan_route_leg'),1)
            clock[0]+=31;runner.tick();health=copy.deepcopy(runner.data['memory']['health'])
            self.assertEqual(health['phase'],'travel_route_plan_unavailable');self.assertEqual(health['code'],'stalled')
            clock[0]+=5;runner.tick()
            self.assertEqual(runner.data['memory']['health']['phase'],health['phase'])
            self.assertEqual(runner.data['memory']['health']['since'],clock[0])
            self.assertEqual(self.count('buddy-actions.plan_route_leg'),1);self.assertFalse(runner.data['pending'])
            # A correlated positive normal arrival is progress, never a new dispatch or quest reward.
            runner.data['last']=dict(id='generic-expedition:arrival-proof',method='buddy-actions.travel_to',effect='mutation',
                                    response=dict(self.rpc.actions(),reason='exploration_within_distance'))
            clock[0]+=1;runner.tick()
            self.assertNotIn('health',runner.data['memory']);self.assertFalse(runner.data['pending'])
            self.assertNotIn('701',runner.data['memory'].get('completed',{}))
            self.assertEqual(self.count('buddy-actions.travel_to'),0)

    def distant_anchor_goal(self):
        self.rpc.giver_x=1380;self.rpc.discover();self.goal['params']['handIns'][0]['x']=1380
        self.goal['params']['travelAnchors']=[dict(mapId=0,x=500,y=0,z=5),dict(mapId=0,x=1000,y=0,z=5)]

    def test_distant_goal_uses_best_verified_anchor_then_replans_unchanged_final_goal(self):
        self.distant_anchor_goal()
        self.rpc.plan_endpoints=[dict(x=x,y=0,z=5) for x in (190,370,550,730,910,1090,1270,1380)]
        with host.Store(self.folder) as store:
            runner=self.runner(store);self.until(runner,lambda:self.count('buddy-quests.claim_reward')==1,limit=64)
            plans=[p for name,p in self.rpc.calls if name=='buddy-actions.plan_route_leg']
            self.assertEqual([p['x'] for p in plans],[1000]+[1380]*7)
            self.assertEqual([p['expectedPositionRevision'] for p in plans],[str(n) for n in range(5,13)])
            self.assertEqual(self.goal['params']['handIns'][0]['x'],1380)
            self.assertEqual(self.count('buddy-actions.travel_to'),8)
            self.assertEqual(self.count('buddy-actions.release_interaction'),8)
            self.assertFalse(self.rpc.lease);self.assertFalse(runner.data['pending'])
            self.assertNotIn('701',runner.data['memory'].get('completed',{}))

    def test_distant_anchor_candidates_fail_bounded_or_are_rejected_without_movement(self):
        for mode in ('none_eligible','too_many','all_failed','best_failed_next_verified'):
            with self.subTest(mode=mode),host.Store(self.folder.parent/mode) as store:
                self.rpc=Endpoint();self.rpc.discover();self.distant_anchor_goal()
                if mode=='none_eligible':self.goal['params']['travelAnchors']=[dict(mapId=1,x=1000,y=0,z=5),dict(mapId=0,x=1200,y=0,z=5),dict(mapId=0,x=50,y=0,z=5),dict(mapId=0,x=900,y=0)]
                elif mode=='too_many':self.goal['params']['travelAnchors']*=9
                original=self.rpc.call
                def call(method,params,timeout=5):
                    if method=='buddy-actions.plan_route_leg' and (mode=='all_failed' or mode=='best_failed_next_verified' and params['x']==1000):
                        self.rpc.calls.append((method,copy.deepcopy(params)));raise host.Error('native_anchor_no_physical_route')
                    return original(method,params,timeout)
                self.rpc.call=call
                if mode=='best_failed_next_verified':self.rpc.plan_endpoints=[dict(x=190,y=0,z=5)]
                runner=self.runner(store)
                if mode=='best_failed_next_verified':
                    self.until(runner,lambda:self.count('buddy-actions.acquire_interaction')==1)
                    plans=[p for name,p in self.rpc.calls if name=='buddy-actions.plan_route_leg']
                    self.assertEqual([p['x'] for p in plans],[1000,500])
                else:
                    self.until(runner,lambda:runner.data['phase']=='waiting_for_deferred_quests')
                    self.assertEqual(self.count('buddy-actions.plan_route_leg'),2 if mode=='all_failed' else 0)
                    self.assertEqual(self.count('buddy-actions.acquire_interaction'),0)
                self.assertEqual(self.count('buddy-actions.travel_to'),0)
                self.assertFalse(runner.data['pending'])

    def partial_travel_endpoint(self, mode='progress'):
        self.rpc.pose['x']=-20;self.rpc.discover();original=self.rpc.call;first=True
        def call(method,params,timeout=5):
            nonlocal first
            value=original(method,params,timeout)
            if method=='buddy-actions.travel_to' and first:
                first=False;self.rpc.pose['x']={'no_progress':-20,'wrong_way':-25,'short_progress':-18}.get(mode,-10)
                value=self.rpc.actions();value['reason']='exploration_step_limit'
                if mode=='foreign_reply':value['sessionId']='another-body'
                elif mode=='uncertain_reply':value['motionUnconfirmed']=True
            return value
        self.rpc.call=call

    def test_definite_partial_progress_releases_then_plans_from_fresh_pose_and_arrives(self):
        self.partial_travel_endpoint()
        with host.Store(self.folder) as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['phase']=='partial_progress_replan')
            self.assertFalse(runner.data['pending']);self.assertFalse(self.rpc.lease)
            self.assertEqual(self.count('buddy-actions.travel_to'),1)
            self.assertNotIn('701',runner.data['memory'].get('completed',{}))
            self.assertEqual(runner.data['memory']['stack'][-1]['stage'],'start')
            self.until(runner,lambda:self.count('buddy-quests.claim_reward')==1)
            plans=[p for name,p in self.rpc.calls if name=='buddy-actions.plan_route_leg']
            self.assertEqual([p['expectedPositionRevision'] for p in plans],['5','6'])
            self.assertEqual([p['x'] for p in plans],[10,10])
            self.assertEqual(self.count('buddy-actions.travel_to'),2)
            self.assertEqual(self.count('buddy-actions.release_interaction'),2)
            self.assertNotIn('701',runner.data['memory'].get('completed',{}))

    def test_partial_without_known_forward_progress_or_known_body_never_replans(self):
        for mode in ('no_progress','wrong_way','short_progress','foreign_reply','uncertain_reply'):
            with self.subTest(mode=mode),host.Store(self.folder.parent/mode) as store:
                self.rpc=Endpoint();self.rpc.discover();self.partial_travel_endpoint(mode)
                runner=self.runner(store);self.until(runner,lambda:runner.data['phase']=='waiting_for_deferred_quests')
                self.assertEqual(self.count('buddy-actions.plan_route_leg'),1)
                self.assertEqual(self.count('buddy-actions.travel_to'),1)
                self.assertEqual(self.count('buddy-actions.release_interaction'),1)
                self.assertFalse(runner.data['pending']);self.assertFalse(self.rpc.lease)
                self.assertEqual(self.count('buddy-quests.claim_reward'),0)

    def test_native_curved_leg_endpoints_replan_full_goal_with_fresh_pose_before_each_lease(self):
        self.rpc.giver_x=310;self.rpc.discover()
        self.goal['params']['handIns'][0]['x']=310
        endpoints=[dict(x=50,y=80,z=5),dict(x=180,y=80,z=5),dict(x=310,y=0,z=5)]
        self.rpc.plan_endpoints=copy.deepcopy(endpoints)
        with host.Store(self.folder) as store:
            runner=self.runner(store);self.until(runner,lambda:self.count('buddy-quests.claim_reward')==1,limit=32)
            plans=[p for name,p in self.rpc.calls if name=='buddy-actions.plan_route_leg']
            travels=[p for name,p in self.rpc.calls if name=='buddy-actions.travel_to']
            self.assertEqual([p['x'] for p in plans],[310,310,310])
            self.assertEqual([p['expectedPositionRevision'] for p in plans],['5','6','7'])
            self.assertEqual([{key:p.get(key,0) for key in ('x','y','z')} for p in travels],endpoints)
            self.assertTrue(all(p['arrivalDistance']==.75 for p in travels))
            effects=[name for name,_ in self.rpc.calls if name in ('buddy-actions.plan_route_leg',
                'buddy-actions.acquire_interaction','buddy-actions.travel_to','buddy-actions.release_interaction')]
            self.assertEqual(effects,['buddy-actions.plan_route_leg','buddy-actions.acquire_interaction',
                'buddy-actions.travel_to','buddy-actions.release_interaction']*3)
            self.assertFalse(self.rpc.lease);self.assertEqual(store.load()['pending'],[])
            self.assertNotIn('701',runner.data['memory'].get('completed',{}))

    def test_definite_read_failure_or_stale_foreign_plan_blocks_before_lease_and_cools_down(self):
        for mode in ('read_failure','foreign_body','pose_changed'):
            self.rpc=Endpoint();self.rpc.giver_x=310;self.rpc.discover()
            self.goal['params']['handIns'][0]['x']=310
            if mode=='read_failure':self.rpc.plan_error='actual_native_no_physical_route'
            original=self.rpc.call
            def call(method,params,timeout=5):
                value=original(method,params,timeout)
                if method=='buddy-actions.plan_route_leg':
                    if mode=='foreign_body':value['sessionId']='foreign'
                    if mode=='pose_changed':self.rpc.pose['revision']='6'
                return value
            self.rpc.call=call
            with self.subTest(mode=mode),host.Store(self.folder.parent/mode) as store:
                runner=self.runner(store);self.until(runner,lambda:runner.data['phase']=='waiting_for_deferred_quests')
                for _ in range(2):runner.tick()
                self.assertEqual(self.count('buddy-actions.plan_route_leg'),1)
                self.assertEqual(self.count('buddy-actions.acquire_interaction'),0)
                self.assertEqual(self.count('buddy-actions.travel_to'),0);self.assertFalse(store.load()['pending'])
                self.assertIn('hand_in:701',runner.data['memory']['deferredQuests'])
                if mode=='read_failure':
                    self.assertEqual(store.load()['last']['effect'],'read')
                    self.assertEqual(runner.data['memory']['lastRoutePlanError']['error'],'actual_native_no_physical_route')

    def test_plan_success_does_not_relax_unknown_travel_write_or_cleanup(self):
        self.rpc.pose['x']=-20;self.rpc.discover();original=self.rpc.call
        def call(method,params,timeout=5):
            if method=='buddy-actions.travel_to':
                self.rpc.calls.append((method,copy.deepcopy(params)));raise host.Error('rpc_result_unconfirmed')
            return original(method,params,timeout)
        self.rpc.call=call
        with host.Store(self.folder) as store:
            runner=self.runner(store);self.until(runner,lambda:bool(runner.data['pending']))
            for _ in range(3):runner.tick()
            self.assertEqual(runner.data['phase'],'blocked_on_ambiguous_call')
            self.assertEqual(store.load()['last']['effect'],'mutation')
            self.assertEqual(self.count('buddy-actions.travel_to'),1)
            self.assertEqual(self.count('buddy-actions.release_interaction'),0)
            self.assertEqual(self.count('buddy-quests.claim_reward'),0)
            # Even an absent lease plus a known position is insufficient when
            # the selected observation omits actual native idle status evidence.
            self.rpc.lease='';runner.tick()
            self.assertEqual(len(runner.data['pending']),1)
            self.assertEqual(self.count('buddy-actions.travel_to'),1)

    def test_goal_configured_child_module_returns_to_same_guarded_parent_and_respects_run_bound(self):
        code=self.source.read_text();self.source=self.folder.parent/'composed.lua'
        self.source.write_text('local mission=(function()\n'+code+'\nend)()\n'+'''
mission.attach({handlers={resource_child=function(c,f)
    assert(f.data.marker=="from_goal")
    c.memory.childRuns=(c.memory.childRuns or 0)+1
    tasks.pop(c,{status="no_opportunity",confirmed=0})
end}})
return mission
''')
        self.goal['params']['opportunities']=[dict(kind='resource_child',params=dict(marker='from_goal'),maxRuns=1,cooldownSeconds=60)]
        with host.Store(self.folder) as store:
            runner=self.runner(store);self.until(runner,lambda:self.count('buddy-quests.claim_reward')==1)
            self.assertEqual(runner.data['memory']['childRuns'],1)
            self.assertEqual(runner.data['memory']['opportunityTasks']['resource_child']['status'],'no_opportunity')
            self.assertNotIn('701',runner.data['memory'].get('completed',{}))
            self.assertEqual(self.count('buddy-quests.claim_reward'),1)
        # Complete modules exceed the JSON frame budget, but use the distinct
        # bounded source budget rather than trimming reviewed implementation.
        raw=code+(ROOT/'behaviors/gathering.lua').read_text()+(ROOT/'behaviors/task-stack.lua').read_text()
        self.assertGreater(len(raw.encode()),host.MAX_INPUT)
        self.assertLess(len(raw.encode()),host.MAX_SOURCE)

    def test_full_reviewed_gathering_module_confirms_materials_then_resumes_parent_handin(self):
        from test_gathering_policy import Endpoint as ResourceEndpoint
        resource=ResourceEndpoint();resource.sid=self.rpc.sid;resource.pose=self.rpc.pose;resource.target['x']=12
        original=self.rpc.call
        def call(method,params,timeout=5):
            if method.startswith('buddy-professions.'):
                self.rpc.calls.append((method,copy.deepcopy(params)));return resource.call(method,params,timeout)
            return original(method,params,timeout)
        self.rpc.call=call
        self.goal['allow']+=['buddy-professions.state','buddy-professions.attempt']
        self.goal['observations']['professions']=dict(method='buddy-professions.state',params={},select=['sessionId','selfGuid',
            'revision','skillsKnown','spellsKnown','skillsRevision','skills','inventoryKnown','inventoryRevision','active',
            'closing','operation','recipes','herbTargets','skinningTargets'])
        self.goal['params']['opportunities']=[dict(kind='gathering',maxRuns=1,cooldownSeconds=60,params=dict(
            resources=[dict(skillId=resource.skill,spellId=resource.spell,targetKind=resource.kind)],maxAttempts=1,
            approachDistance=40,requestPrefix='nested_fixture'))]
        code=self.source.read_text();self.source=self.folder.parent/'full-modules.lua'
        self.source.write_text('local mission=(function()\n'+code+'\nend)()\nlocal child=(function()\n'+
            (ROOT/'behaviors/gathering.lua').read_text()+'\nend)()\nreturn mission.attach(child)\n')
        with host.Store(self.folder) as store:
            runner=self.runner(store);self.until(runner,lambda:self.count('buddy-quests.claim_reward')==1,limit=20)
            self.assertEqual(self.count('buddy-professions.attempt'),1)
            self.assertEqual(runner.data['memory']['gatheringAttempts'][0]['phase'],'completed')
            self.assertEqual(runner.data['memory']['opportunityTasks']['gathering']['confirmed'],1)
            self.assertEqual(runner.data['memory']['opportunityTasks']['gathering']['status'],'gathered')
            self.assertEqual(self.count('buddy-quests.claim_reward'),1)
            self.assertNotIn('701',runner.data['memory'].get('completed',{}))

    def test_actual_descriptor_zero_choice_handin_receipt_followup_and_resume(self):
        with host.Store(self.folder) as store:
            runner = self.runner(store)
            self.until(runner, lambda: self.count('buddy-quests.claim_reward') == 1)
            for _ in range(3): runner.tick()
            self.assertNotIn('701', runner.data['memory'].get('completed', {}))
            self.assertEqual(self.count('buddy-quests.claim_reward'), 1)
            claim = next(p for n, p in self.rpc.calls if n == 'buddy-quests.claim_reward')
            self.assertTrue(claim['noChoice']); self.assertNotIn('expectedItemId', claim)
        # Restart consumes saved frame/call state. No repeated reward write.
        with host.Store(self.folder) as store:
            runner = self.runner(store); self.rpc.confirm_reward()
            self.until(runner, lambda: self.rpc.accept_requested)
            self.assertIn('701', runner.data['memory']['completed'])
            for _ in range(2): runner.tick()
            self.assertNotIn('702', runner.data['memory'].get('accepted', {}))
            self.rpc.confirm_acceptance(); runner.tick()
            self.assertIn('702', runner.data['memory']['accepted'])
            self.assertFalse(runner.data['done'])
            self.assertEqual(runner.data['phase'], 'remaining_objectives_rendezvous_unknown')
            self.assertEqual(runner.data['memory']['remaining'], [dict(questId=702, inLog=True, complete=False)])
        self.assertEqual(self.count('buddy-quests.claim_reward'), 1)
        self.assertEqual(self.count('buddy-quests.accept'), 1)

    def test_single_omitted_zero_choice_and_multiple_choices_block(self):
        self.rpc.choices = [dict(itemId=9001, count=1)]
        with host.Store(self.folder) as store:
            runner = self.runner(store)
            self.until(runner, lambda: self.count('buddy-quests.claim_reward') == 1)
        claim = next(p for n, p in self.rpc.calls if n == 'buddy-quests.claim_reward')
        self.assertEqual(claim['expectedItemId'], 9001); self.assertNotIn('choiceIndex', claim)
        self.rpc = Endpoint(); self.rpc.discover(); self.rpc.choices = [dict(itemId=1), dict(index=1, itemId=2)]
        with host.Store(self.folder.parent / 'multiple') as store:
            runner = self.runner(store)
            self.until(runner, lambda: runner.data['phase'] == 'waiting_for_deferred_quests')
            self.assertEqual(runner.data['memory']['deferredQuests']['hand_in:701']['reason'],'reward_choice_requires_selection')
            self.assertEqual(self.count('buddy-quests.claim_reward'), 0)

    def test_travel_normal_token_route_cleanup_then_fresh_giver(self):
        self.rpc.pose['x'] = -200; self.rpc.quest['nearby'] = []
        with host.Store(self.folder) as store:
            runner = self.runner(store)
            self.until(runner, lambda: self.count('buddy-quests.claim_reward') == 1, limit=16)
            self.assertEqual(self.rpc.lease, '')
            self.assertEqual(self.count('buddy-actions.travel_to'), 2)
            self.assertEqual(self.count('buddy-actions.acquire_interaction'),2)
            self.assertEqual(self.count('buddy-actions.release_interaction'),2)
            plans=[p for name,p in self.rpc.calls if name=='buddy-actions.plan_route_leg']
            travels=[p for name,p in self.rpc.calls if name=='buddy-actions.travel_to']
            releases=[p for name,p in self.rpc.calls if name=='buddy-actions.release_interaction']
            self.assertEqual([p['expectedPositionRevision'] for p in plans],['5','6'])
            self.assertEqual([p['x'] for p in plans],[10,10])
            self.assertEqual([p['x'] for p in travels],[-20,10])
            self.assertEqual([p['token'] for p in travels],[p['token'] for p in releases])
            self.assertEqual(len({p['token'] for p in travels}),2)
            order=[name for name,_ in self.rpc.calls if name in ('buddy-actions.plan_route_leg','buddy-actions.acquire_interaction','buddy-actions.travel_to','buddy-actions.release_interaction')]
            self.assertEqual(order,['buddy-actions.plan_route_leg','buddy-actions.acquire_interaction','buddy-actions.travel_to','buddy-actions.release_interaction']*2)
            self.assertGreaterEqual(self.count('buddy-quests.discover'), 1)
            self.assertEqual(next(p for n, p in self.rpc.calls if n == 'buddy-quests.turn_in')['npcGuid'], self.rpc.npc)

    def test_navigation_block_releases_exact_token_and_does_not_reissue_route(self):
        self.rpc.pose['x'] = -20; self.rpc.quest['nearby'] = []
        self.rpc.nav_reason = 'exploration_navigation_unavailable'
        with host.Store(self.folder) as store:
            runner = self.runner(store)
            self.until(runner, lambda: runner.data['phase'] == 'waiting_for_deferred_quests')
            for _ in range(3): runner.tick()
            self.assertEqual(self.rpc.lease, '')
            self.assertEqual(self.count('buddy-actions.travel_to'), 1)
            self.assertEqual(self.count('buddy-actions.release_interaction'), 1)
            self.assertEqual(self.count('buddy-quests.turn_in'), 0)

    def two_handins(self, first_entry=1234, first_x=10):
        self.rpc.quest['questLog'][1]=dict(questId=703,complete=True)
        first=self.goal['params']['handIns'][0]
        first.update(npcEntry=first_entry,x=first_x,followupQuestId=0)
        self.goal['params']['handIns'].append(dict(questId=703,npcEntry=1234,mapId=0,x=10,y=0,z=5))
        self.goal['params']['initialHandInQuestIds']=[701,703]

    def test_clean_blocked_route_defers_only_its_quest_and_other_handin_progresses_then_cooldown_survives_restart(self):
        self.two_handins(first_entry=9999,first_x=-20)
        self.rpc.nav_reason='exploration_navigation_unavailable'
        clock=1700000000.
        with patch.object(host.time,'time',side_effect=lambda:clock):
            with host.Store(self.folder) as store:
                runner=self.runner(store)
                self.until(runner,lambda:self.count('buddy-quests.claim_reward')==1,limit=20)
                claim=next(p for name,p in self.rpc.calls if name=='buddy-quests.claim_reward')
                self.assertEqual(claim['questId'],703);self.assertFalse(self.rpc.lease)
                deferred=runner.data['memory']['deferredQuests']['hand_in:701']
                self.assertEqual(deferred['retryAfter'],clock+60)
                self.assertNotIn('701',runner.data['memory'].get('completed',{}))
                self.rpc.confirm_reward();runner.tick()
                self.assertEqual(runner.data['phase'],'waiting_for_deferred_quests')
            clock+=59
            with host.Store(self.folder) as store:
                runner=self.runner(store)
                for _ in range(3):runner.tick()
                self.assertEqual(self.count('buddy-actions.travel_to'),1)
                self.assertTrue(self.rpc.quest['questLog'][0]['complete'])
                clock+=1
                self.until(runner,lambda:self.count('buddy-actions.travel_to')==2)
                self.assertEqual(self.count('buddy-quests.claim_reward'),1)

    def test_missing_giver_or_unusable_reward_offer_does_not_starve_second_ready_handin(self):
        for mode in ('missing_giver','multiple_choices','no_reward_offer','giver_disappears_after_query'):
            with self.subTest(mode=mode):
                self.rpc=Endpoint();self.rpc.discover()
                self.goal['params']['handIns']=self.goal['params']['handIns'][:1]
                self.two_handins(first_entry=9999 if mode=='missing_giver' else 1234)
                if mode=='multiple_choices': self.rpc.choices_by_quest[701]=[dict(itemId=1),dict(index=1,itemId=2)]
                if mode=='no_reward_offer': self.rpc.no_reward_offer.add(701)
                if mode=='giver_disappears_after_query':
                    original=self.rpc.call
                    def call(method,params,timeout=5):
                        value=original(method,params,timeout)
                        if method=='buddy-quests.turn_in' and params['questId']==701:self.rpc.quest['nearby']=[]
                        return value
                    self.rpc.call=call
                with host.Store(self.folder.parent/mode) as store:
                    runner=self.runner(store);self.until(runner,lambda:self.count('buddy-quests.claim_reward')==1,limit=20)
                    self.assertEqual(next(p for name,p in self.rpc.calls if name=='buddy-quests.claim_reward')['questId'],703)
                    self.assertIn('hand_in:701',runner.data['memory']['deferredQuests'])
                    self.assertNotIn('701',runner.data['memory'].get('completed',{}))

    def test_ambiguous_route_or_cleanup_never_skips_to_another_quest(self):
        for method in ('buddy-actions.travel_to','buddy-actions.release_interaction'):
            with self.subTest(method=method):
                self.rpc=Endpoint();self.rpc.discover();self.rpc.nav_reason='exploration_navigation_unavailable'
                self.goal['params']['handIns']=self.goal['params']['handIns'][:1]
                self.two_handins(first_entry=9999,first_x=-20)
                original=self.rpc.call
                def fail(name,params,timeout=5):
                    if name==method:
                        self.rpc.calls.append((name,copy.deepcopy(params)));raise host.Error('rpc_result_unconfirmed')
                    return original(name,params,timeout)
                self.rpc.call=fail
                with host.Store(self.folder.parent/method) as store:
                    runner=self.runner(store);self.until(runner,lambda:bool(runner.data['pending']))
                    for _ in range(3):runner.tick()
                    self.assertEqual(runner.data['phase'],'blocked_on_ambiguous_call')
                    self.assertEqual(self.count(method),1);self.assertEqual(self.count('buddy-quests.turn_in'),0)
                    self.assertFalse(runner.data['memory'].get('deferredQuests'))

    def test_blocked_followup_yields_to_ready_handin_and_not_offered_retry_is_not_a_loop(self):
        self.two_handins();self.goal['params']['handIns'][0]['followupQuestId']=702
        clock=1700000000.;original=self.rpc.call;hide_next_discovery=False
        def call(method,params,timeout=5):
            nonlocal hide_next_discovery
            value=original(method,params,timeout)
            if method=='buddy-quests.discover' and hide_next_discovery:
                self.rpc.quest['nearby']=[];hide_next_discovery=False
            if method=='buddy-quests.offer':self.rpc.quest['offer']['quests']=[]
            return value
        self.rpc.call=call
        with patch.object(host.time,'time',side_effect=lambda:clock):
            with host.Store(self.folder) as store:
                runner=self.runner(store);self.until(runner,lambda:self.count('buddy-quests.claim_reward')==1)
                self.rpc.confirm_reward();hide_next_discovery=True
                self.until(runner,lambda:self.count('buddy-quests.claim_reward')==2,limit=20)
                self.assertIn('followup:702',runner.data['memory']['deferredQuests'])
                self.assertEqual(next(p for name,p in reversed(self.rpc.calls) if name=='buddy-quests.claim_reward')['questId'],703)
                self.rpc.confirm_reward();runner.tick();clock+=60
                self.until(runner,lambda:self.count('buddy-quests.offer')==1)
                for _ in range(4):runner.tick()
                self.assertEqual(self.count('buddy-quests.offer'),1);self.assertEqual(self.count('buddy-quests.accept'),0)
                self.assertNotIn('followup:702',runner.data['memory']['deferredQuests'])
                self.assertEqual(runner.data['phase'],'remaining_objectives_rendezvous_unknown')

    def test_exported_handlers_allow_a_nested_child_and_resume_the_same_guarded_handin(self):
        code=self.source.read_text();self.source=self.folder.parent/'derived.lua'
        self.source.write_text('local mission=(function()\n'+code+'\nend)()\n'+'''
local parent=mission.handlers.journal_sync
mission.handlers.example_child=function(c,f) tasks.pop(c,{status="child_done"}) end
mission.handlers.journal_sync=function(c,f)
    if not f.childDone then
        if f.child and f.child.status=="child_done" then
            f.child=nil;f.childDone=true;c.memory.nestedChildResumed=true
        else tasks.push(c,"example_child",{});return end
    end
    return parent(c,f)
end
return mission
''')
        with host.Store(self.folder) as store:
            runner=self.runner(store);self.until(runner,lambda:self.count('buddy-quests.claim_reward')==1)
            self.assertTrue(runner.data['memory']['nestedChildResumed'])
            self.assertEqual(next(p for name,p in self.rpc.calls if name=='buddy-quests.claim_reward')['questId'],701)
            self.assertNotIn('701',runner.data['memory'].get('completed',{}))

    def lost_travel_runner(self, store):
        self.rpc.pose['x']=-20;self.rpc.discover();original=self.rpc.call;actions=self.rpc.actions
        for name in ('reason','castPending','autoAttacking','autoAttackRequested','autoAttackUnconfirmed','recovery'):
            if name not in self.goal['observations']['actions']['select']:self.goal['observations']['actions']['select'].append(name)
        def observed():return dict(actions(),reason='explicit_follow_disabled')
        self.rpc.actions=observed
        def call(method,params,timeout=5):
            value=original(method,params,timeout)
            if method=='buddy-actions.travel_to':
                self.rpc.lease=''  # Native route has ended and its token expired.
                raise host.Error('rpc_result_unconfirmed')
            return value
        self.rpc.call=call;runner=self.runner(store)
        self.until(runner,lambda:bool(runner.data['pending']))
        return runner

    def test_quiescent_unknown_navigation_is_abandoned_without_arrival_ack_or_replay(self):
        with host.Store(self.folder) as store:
            runner=self.lost_travel_runner(store);original=copy.deepcopy(runner.data['pending'][0])
            runner.tick();self.assertFalse(runner.data['pending'])
            frame=runner.data['memory']['stack'][-1]
            self.assertEqual((frame['stage'],frame['blocked']),('release','unknown_travel_abandoned'))
            row=next(r for r in runner.data['attempts'] if r['id']==original['id'])
            self.assertEqual(row['error'],original['error']);self.assertEqual(row['params'],original['params'])
            self.assertEqual(row['requestSHA256'],original['requestSHA256'])
            self.assertEqual(row['resolution']['outcome'],'navigation_abandoned_custody_absent_no_arrival')
            self.assertEqual(row['outcome'],'policy_observation_resolution')
            self.until(runner,lambda:runner.data['phase']=='waiting_for_deferred_quests')
            self.assertEqual(runner.data['memory']['deferredQuests']['hand_in:701']['reason'],'unknown_travel_abandoned')
            self.assertEqual(self.count('buddy-actions.travel_to'),1)
            self.assertEqual(self.count('buddy-actions.release_interaction'),0)
            self.assertEqual(self.count('buddy-quests.claim_reward'),0)
            self.assertNotIn('701',runner.data['memory'].get('completed',{}))

    def test_unknown_navigation_abandonment_requires_exact_current_quiescence_and_request(self):
        for mode in ('body','active_lease','motionUnconfirmed','moving','traveling','castPending','autoAttacking',
                     'recovery','token','destination','map','native_idle_missing'):
            with self.subTest(mode=mode),host.Store(self.folder.parent/mode) as store:
                self.rpc=Endpoint();self.rpc.discover();runner=self.lost_travel_runner(store);actions=self.rpc.actions
                def observed():
                    a=actions()
                    if mode=='body':a['sessionId']='other-body'
                    elif mode=='active_lease':a.update(interactionOwner='exploration',interactionLeaseRemainingMs=5000)
                    elif mode=='recovery':a['recovery']={'active':True}
                    elif mode=='native_idle_missing':a.pop('reason')
                    elif mode in ('motionUnconfirmed','moving','traveling','castPending','autoAttacking'):a[mode]=True
                    return a
                self.rpc.actions=observed
                if mode=='token':runner.data['pending'][0]['params']['token']='another-token'
                elif mode=='destination':runner.data['pending'][0]['params']['x']+=1
                elif mode=='map':runner.data['pending'][0]['params']['mapId']=1
                if mode=='body':
                    with self.assertRaisesRegex(host.Error,'body_changed'):runner.tick()
                else:runner.tick();self.assertEqual(runner.data['phase'],'blocked_on_ambiguous_call')
                self.assertEqual(len(runner.data['pending']),1)
                self.assertEqual(self.count('buddy-actions.travel_to'),1)
                self.assertEqual(self.count('buddy-actions.release_interaction'),0)
                self.assertEqual(self.count('buddy-quests.claim_reward'),0)

    def lost_release_runner(self, store):
        self.rpc.pose['x']=-20;self.rpc.discover();original=self.rpc.call
        def call(method,params,timeout=5):
            value=original(method,params,timeout)
            if method=='buddy-actions.release_interaction':raise host.Error('rpc_result_unconfirmed')
            return value
        self.rpc.call=call;runner=self.runner(store)
        self.until(runner,lambda:bool(runner.data['pending']))
        self.assertEqual(runner.data['pending'][0]['method'],'buddy-actions.release_interaction')
        return runner

    def test_lost_release_reply_resolves_only_current_absent_custody_and_parent_proceeds(self):
        with host.Store(self.folder) as store:
            runner=self.lost_release_runner(store);original=copy.deepcopy(runner.data['pending'][0])
            runner.tick();self.assertEqual(runner.data['pending'],[])
            row=next(r for r in runner.data['attempts'] if r['id']==original['id'])
            self.assertEqual(row['outcome'],'policy_observation_resolution')
            self.assertEqual(row['error'],original['error']);self.assertEqual(row['params'],original['params'])
            self.assertEqual(row['requestSHA256'],original['requestSHA256'])
            self.assertEqual(row['resolution']['path'],'position.revision')
            self.assertEqual(row['resolution']['outcome'],'custody_absent_observed_no_transport_ack')
            self.assertEqual(self.count('buddy-actions.release_interaction'),1)
            self.until(runner,lambda:self.count('buddy-quests.claim_reward')==1)
            self.assertEqual(self.count('buddy-actions.release_interaction'),1)
            self.assertNotIn('701',runner.data['memory'].get('completed',{}))

    def test_lost_release_cannot_resolve_foreign_body_active_custody_or_motion(self):
        for mode in ('session','guid','active_lease','replacement_lease','remaining_lease','moving','traveling',
                     'motionUnconfirmed','unknown_position','unknown_map','zero_revision','token','wrong_method'):
            with self.subTest(mode=mode),host.Store(self.folder.parent/mode) as store:
                self.rpc=Endpoint();self.rpc.discover();runner=self.lost_release_runner(store)
                actions=self.rpc.actions
                def observed():
                    a=actions()
                    if mode=='session':a['sessionId']='another-body'
                    elif mode=='guid':a['selfGuid']='99'
                    elif mode in ('active_lease','replacement_lease'):a.update(interactionOwner='exploration' if mode=='active_lease' else 'quests',interactionLeaseRemainingMs=1000)
                    elif mode=='remaining_lease':a['interactionLeaseRemainingMs']=1000
                    elif mode in ('moving','traveling','motionUnconfirmed'):a[mode]=True
                    elif mode=='unknown_position':a['position']['known']=False
                    elif mode=='unknown_map':a['position']['mapKnown']=False
                    elif mode=='zero_revision':a['position']['revision']='0'
                    return a
                self.rpc.actions=observed
                if mode=='token':runner.data['pending'][0]['params']['token']='another-token'
                elif mode=='wrong_method':
                    runner.data['pending'][0]['method']='buddy-actions.travel_to'
                    runner.data['memory']['stack'][-1]['waiting']['method']='buddy-actions.travel_to'
                if mode in ('session','guid'):
                    with self.assertRaisesRegex(host.Error,'body_changed'):runner.tick()
                else:
                    runner.tick();self.assertEqual(runner.data['phase'],'blocked_on_ambiguous_call')
                self.assertEqual(len(runner.data['pending']),1)
                self.assertEqual(self.count('buddy-actions.release_interaction'),1)
                self.assertEqual(self.count('buddy-quests.claim_reward'),0)

    def test_lost_claim_reply_stays_pending_until_exact_fresh_reward_receipt(self):
        self.rpc.fail_claim = True
        with host.Store(self.folder) as store:
            runner = self.runner(store)
            self.until(runner, lambda: bool(runner.data['pending']))
            runner.tick(); self.assertEqual(runner.data['phase'], 'blocked_on_ambiguous_call')
            self.rpc.confirm_reward()
            receipt = self.rpc.quest['lastReward']; receipt['npcGuid'] = '123'
            runner.tick(); self.assertEqual(len(runner.data['pending']), 1)
            receipt['npcGuid'] = self.rpc.npc
            runner.tick(); self.assertEqual(runner.data['pending'], [])
            self.assertEqual(runner.data['attempts'][-1]['outcome'], 'policy_observation_resolution')
            runner.tick(); self.assertIn('701', runner.data['memory']['completed'])
        self.assertEqual(self.count('buddy-quests.claim_reward'), 1)

    def test_unknown_log_dead_giver_and_no_complete_class_quest_cannot_dispatch(self):
        for mode in ('unknown_log', 'dead_giver', 'own_incomplete'):
            with self.subTest(mode=mode):
                self.rpc = Endpoint(); self.rpc.discover()
                if mode == 'unknown_log': self.rpc.quest['questLogKnown'] = False
                if mode == 'dead_giver': self.rpc.quest['nearby'][0]['creature']['alive'] = False
                if mode == 'own_incomplete': self.rpc.quest['questLog'][0]['complete'] = False
                with host.Store(self.folder.parent / mode) as store:
                    runner = self.runner(store); runner.tick()
                    self.assertEqual(self.count('buddy-quests.claim_reward'), 0)
                    self.assertEqual(self.count('buddy-quests.turn_in'), 0)

    def test_stale_cached_giver_is_refreshed_and_actual_spawn_guid_is_used(self):
        old_guid = self.rpc.npc
        original = self.rpc.call
        def fresh_spawn(method, params, timeout=5):
            if method == 'buddy-quests.discover': self.rpc.npc = str(int(old_guid) + 1)
            return original(method, params, timeout)
        self.rpc.call = fresh_spawn
        with host.Store(self.folder) as store:
            runner = self.runner(store)
            self.until(runner, lambda: self.count('buddy-quests.claim_reward') == 1)
        self.assertEqual(self.count('buddy-quests.discover'), 1)
        self.assertTrue(all(p['npcGuid'] == self.rpc.npc for n, p in self.rpc.calls if n in ('buddy-quests.turn_in', 'buddy-quests.claim_reward')))

    def test_transport_or_foreign_reward_receipt_never_counts_as_completion(self):
        with host.Store(self.folder) as store:
            runner = self.runner(store)
            self.until(runner, lambda: self.count('buddy-quests.claim_reward') == 1)
            self.rpc.confirm_reward(); good = copy.deepcopy(self.rpc.quest['lastReward'])
            for key, value in [('questId', 999), ('npcGuid', '999'), ('offerRevision', '99'),
                               ('baselineLogRevision', '99'), ('confirmedLogRevision', '1'),
                               ('serverRewardObserved', False), ('logRemovalConfirmed', False), ('status', 'REWARD_REQUESTED')]:
                self.rpc.quest['lastReward'] = dict(good, **{key: value})
                runner.tick()
                self.assertNotIn('701', runner.data['memory'].get('completed', {}))
                self.assertEqual(self.count('buddy-quests.claim_reward'), 1)
            self.rpc.quest['lastReward'] = good; runner.tick()
            self.assertIn('701', runner.data['memory']['completed'])


if __name__ == '__main__': unittest.main()
