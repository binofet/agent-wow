"""Actual accepted-Lua vendor selection; public observations and no game I/O."""
import copy
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location('vendor_policy_test_subject', ROOT/'dev/town_policy.py')
policy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(policy)
BINARY = Path(os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY', ROOT/'data/bin/luapolicy'))
VENDOR = 0xf130000a23000001


def service_source():
    return ('local services=(function()\n'+(ROOT/'behaviors/follow-formation.lua').read_text()+
        '\nend)()\nreturn {service_destination=services.service_destination}')


def source_reply(source, state, generation='1'):
    return dict(sessionId=state['sessionId'],characterGuid=state['selfGuid'],source=source,
        revision=hashlib.sha256(source.encode()).hexdigest(),generation=generation,sourceLimitBytes=8*1024*1024)


def policy_reply(source, state, generation='1'):
    view=source_reply(source,state,generation)
    return dict(sessionId=view['sessionId'],characterGuid=view['characterGuid'],
        policy={key:view[key] for key in ('revision','generation')})


def bridge_factory(original):
    return lambda binary,modules,**options: original(BINARY,modules,**options)


class RPC:
    def __init__(self,source=None):
        self.state=dict(sessionId='current-body',selfGuid='1',inventoryRevision='12')
        self.source,self.generation,self.calls=source or service_source(),'1',[]
    def call(self,method,params):
        self.calls.append((method,copy.deepcopy(params)))
        if method=='session.policy_source':return source_reply(self.source,self.state,self.generation)
        if method=='session.attach':return policy_reply(self.source,self.state,self.generation)
        if method=='buddy-inventory.state':return copy.deepcopy(self.state)
        raise AssertionError('No mutation is allowed: '+method)


class TrainingRPC(RPC):
    def __init__(self,source=None):
        super().__init__(source)
        self.state['revision']='7'
    def call(self,method,params):
        if method=='buddy-training.state':
            self.calls.append((method,copy.deepcopy(params)))
            return {k:v for k,v in self.state.items() if k not in ('selfGuid','inventoryRevision')}
        return super().call(method,params)


@unittest.skipUnless(BINARY.is_file(), 'build or provide the existing luapolicy evaluator')
class TrainerDestinationTests(unittest.TestCase):
    def setUp(self):
        self.patch=patch.object(policy.runtime,'Bridge',side_effect=bridge_factory(policy.runtime.Bridge))
        self.patch.start();self.addCleanup(self.patch.stop)

    def rank(self,rpc,rows,scope='nearby',**options):
        return policy.rank_trainers(rpc,copy.deepcopy(rpc.state),1,rows,scope,**options)

    def test_actual_bundle_interaction_priority_range_ties_and_same_body_tuning(self):
        source=subprocess.check_output(['python3','-B',str(ROOT/'dev/encounter-bundle')],text=True)
        rpc=TrainingRPC(source)
        rows=[(50.,VENDOR+3,False),(20.,VENDOR+2,False),(20.,VENDOR,False),
            (101.,VENDOR+4,False),(2.,VENDOR+1,True)]
        self.assertEqual(self.rank(rpc,rows),[(2.,VENDOR+1),(20.,VENDOR),(20.,VENDOR+2),(50.,VENDOR+3)])
        self.assertEqual(self.rank(rpc,rows,'travel'),[(20.,VENDOR),(20.,VENDOR+2),(50.,VENDOR+3)])
        rpc.source=source.replace('trainer_search_yards, trainer_interaction_yards = 100,4',
            'trainer_search_yards, trainer_interaction_yards = 120,4').replace(
            'trainer_prefer_nearest = true','trainer_prefer_nearest = false')
        rpc.generation='2'
        self.assertEqual(self.rank(rpc,rows),[(2.,VENDOR+1),(101.,VENDOR+4),(50.,VENDOR+3),(20.,VENDOR),(20.,VENDOR+2)])
        self.assertEqual(rpc.state['sessionId'],'current-body')
        self.assertTrue(all(m.startswith('session.') or m=='buddy-training.state' for m,_ in rpc.calls))

    def test_training_revision_or_body_change_rejects_current_source_selection(self):
        for field in ('revision','sessionId'):
            rpc=TrainingRPC()
            class Bridge:
                def evaluate(self,source,value):
                    rpc.state[field]='8' if field=='revision' else 'replacement'
                    return dict(available=True,decision=dict(trainers=[str(VENDOR)]))
            with self.subTest(field=field),self.assertRaisesRegex(policy.Error,'body_or_observation_changed'):
                self.rank(rpc,[(20.,VENDOR,False)],bridge=Bridge())

    def test_unsupported_trainer_export_never_falls_back_or_writes(self):
        rpc=TrainingRPC('return {service_destination=function() return {} end}')
        with self.assertRaisesRegex(policy.Error,'current_observed_trainers'):
            self.rank(rpc,[(20.,VENDOR,False)])
        self.assertTrue(all(m.startswith('session.') or m=='buddy-training.state' for m,_ in rpc.calls))

    def test_maintenance_selects_lua_trainer_and_preserves_exact_pending_request(self):
        import test_party_maintenance as maintenance
        subject=maintenance.m
        rpc=maintenance.RPC()
        npc=int(rpc.offer['npcGuid'])
        positions={npc:50.,npc+1:20.}
        rpc.world['self'].update(classKnown=True,classId=5,levelKnown=True,movementFlagsKnown=True)
        rpc.world['owner'].update(mapKnown=True,positionKnown=True,movementFlagsKnown=True,x=0.,y=0.,z=0.)
        original=rpc.call
        def call(method,params):
            if method=='buddy-training.discover':
                rpc.calls.append((method,copy.deepcopy(params)))
                rows=[]
                for guid,x in positions.items():
                    distance=abs(x-rpc.actions['position']['x'])
                    rows.append(dict(creature=dict(guid=str(guid),entry=guid>>24&0xffffff,
                        present=True,mapKnown=True,mapId=0,positionKnown=True,x=x,y=0.,z=0.,
                        aliveKnown=True,alive=True,healthKnown=True,health=100,npcFlags=0x33),
                        distanceKnown=True,distance=distance,interactable=distance<=4,
                        blockedReason='' if distance<=4 else 'trainer_outside_conservative_range'))
                return copy.deepcopy(dict(state=rpc.training,nearby=rows))
            if method=='buddy-actions.travel_trainer':
                rpc.offer['npcGuid']=params['npcGuid']
                rpc.trainer_x=positions[int(params['npcGuid'])]
            return original(method,params)
        rpc.call=call
        with patch.object(subject.town_policy.runtime,'Bridge',side_effect=bridge_factory(maintenance.TRAINER_BRIDGE)),\
                patch.object(subject.c,'quest_task_snapshot',return_value=dict(active=False)),\
                patch.object(subject.c,'area_quest_snapshot',return_value=dict(active=False)),\
                patch.object(subject.c,'party_town_snapshot',return_value=dict(active=False)),\
                patch.object(subject,'profession_active',return_value=False),\
                patch.object(subject.harvest,'routine_locked',return_value=False):
            saved=[]
            actor=subject.Maintenance('Arlen',1,rpc,lambda row:saved.append(copy.deepcopy(row)),now=lambda:100.)
            actor.tick()
            self.assertEqual([p['npcGuid'] for m,p in rpc.calls if m=='buddy-actions.travel_trainer'],[str(npc+1)])
            self.assertEqual([p['npcGuid'] for m,p in rpc.calls if m=='buddy-training.list'],[str(npc+1)])
            self.assertEqual([p['npcGuid'] for m,p in rpc.calls if m=='buddy-training.buy'],[str(npc+1)])
            self.assertEqual(saved[-1]['training'][-1]['status'],'reserved')
            methods=[m for m,_ in rpc.calls]
            self.assertLess(methods.index('buddy-actions.release_interaction'),methods.index('buddy-training.list'))
            self.assertIsNone(rpc.token)
            # A current ambiguous purchase remains bound to its exact NPC;
            # changing ranking cannot silently move that request to a peer.
            rpc.policy_source=rpc.policy_source.replace('trainer_prefer_nearest = true','trainer_prefer_nearest = false')
            actor.next_query=0
            actor.tick()
            self.assertEqual(len([m for m,_ in rpc.calls if m=='buddy-training.buy']),1)
            self.assertEqual(saved[-1]['training'][-1]['npc'],str(npc+1))


@unittest.skipUnless(BINARY.is_file(), 'build or provide the existing luapolicy evaluator')
class VendorDestinationTests(unittest.TestCase):
    def setUp(self):
        self.patch=patch.object(policy.runtime,'Bridge',side_effect=bridge_factory(policy.runtime.Bridge))
        self.patch.start();self.addCleanup(self.patch.stop)

    def rank(self,rpc,rows,scope='nearby',**options):
        return policy.rank_vendors(rpc,copy.deepcopy(rpc.state),rows,scope,**options)

    def test_full_bundle_distance_cutoff_ties_and_same_session_source_tuning(self):
        source=subprocess.check_output(['python3','-B',str(ROOT/'dev/encounter-bundle')],text=True)
        rpc=RPC(source)
        rows=[(20.,VENDOR+1),(4.,VENDOR+2),(4.,VENDOR),(36.,VENDOR+3)]
        self.assertEqual(self.rank(rpc,rows),[(4.,VENDOR),(4.,VENDOR+2),(20.,VENDOR+1)])
        self.assertEqual(self.rank(rpc,rows,'interactable'),[(4.,VENDOR),(4.,VENDOR+2)])
        rpc.source=source.replace('vendor_search_yards, vendor_interaction_yards = 35,4.5',
            'vendor_search_yards, vendor_interaction_yards = 40,4.5').replace(
            'vendor_prefer_nearest = true','vendor_prefer_nearest = false')
        rpc.generation='2'
        self.assertEqual(self.rank(rpc,rows),[(36.,VENDOR+3),(20.,VENDOR+1),(4.,VENDOR),(4.,VENDOR+2)])
        self.assertEqual(rpc.state['sessionId'],'current-body')
        self.assertTrue(all(method.startswith('session.') or method=='buddy-inventory.state' for method,_ in rpc.calls))

    def test_current_source_missing_export_never_falls_back_to_python_order(self):
        rpc=RPC('return {decide=function() return {} end}')
        with self.assertRaisesRegex(policy.Error,'export_unavailable'):self.rank(rpc,[(1.,VENDOR)])

    def test_source_body_generation_and_inventory_changes_reject_selection(self):
        for field in ('source','generation','body','inventory'):
            with self.subTest(field=field):
                rpc=RPC()
                class Bridge:
                    def evaluate(inner,source,value):
                        if field=='source':rpc.source+='\n--new revision'
                        elif field=='generation':rpc.generation='2'
                        elif field=='body':rpc.state['sessionId']='replacement-body'
                        else:rpc.state['inventoryRevision']='13'
                        return dict(available=True,decision=dict(vendors=[str(VENDOR)]))
                with self.assertRaisesRegex(policy.Error,'body_or_observation_changed'):
                    self.rank(rpc,[(1.,VENDOR)],bridge=Bridge())
                self.assertTrue(all(not m.startswith('buddy-actions.') for m,_ in rpc.calls))

    def test_unobserved_or_duplicate_lua_destinations_are_rejected(self):
        for result in ([str(VENDOR+1)],[str(VENDOR),str(VENDOR)],[VENDOR]):
            class Bridge:
                def evaluate(self,source,value):return dict(available=True,decision=dict(vendors=result))
            with self.subTest(result=result),self.assertRaisesRegex(policy.Error,'current_observed_vendors'):
                self.rank(RPC(),[(1.,VENDOR)],bridge=Bridge())

    def test_source_hash_mismatch_is_not_evaluated(self):
        rpc=RPC()
        original=rpc.call
        def call(method,params):
            result=original(method,params)
            if method=='session.policy_source':result['source']+='\n--not accepted'
            return result
        rpc.call=call
        with self.assertRaisesRegex(policy.Error,'current_town_policy_source_required'):
            self.rank(rpc,[(1.,VENDOR)])

    def test_standing_and_finite_worker_use_same_accepted_policy_and_exact_override(self):
        import test_party_town as standing
        import test_town_maintenance as finite
        rpc=standing.RPC()
        rpc.policy_source=service_source().replace('vendor_prefer_nearest = true','vendor_prefer_nearest = false')
        original=rpc.call
        def call(method,params):
            result=original(method,params)
            if method=='buddy-inventory.discover':
                near=dict(rpc.vendor(),guid=str(VENDOR+1),x=2.)
                result['nearby'].append(dict(creature=near))
            return result
        rpc.call=call
        with patch.object(standing.m.town.destination_policy.runtime,'Bridge',
                side_effect=bridge_factory(standing.m.town.destination_policy.runtime.Bridge)):
            actor=standing.m.Supervisor(1,rpc)
            actor.session=rpc.inv['sessionId']
            self.assertEqual(actor.vendors(actor.read()),[(20.,VENDOR),(2.,VENDOR+1)])
        local=finite.FakeRPC()
        local.policy_source=rpc.policy_source
        original=local.call
        def call(method,params):
            result=original(method,params)
            if method=='buddy-inventory.discover':
                other=copy.deepcopy(result['nearby'][0]);other['distance']=3.
                other['creature']['guid']=str(VENDOR+1);result['nearby'].append(other)
            return result
        local.call=call
        with patch.object(finite.m.destination_policy.runtime,'Bridge',
                side_effect=bridge_factory(finite.m.destination_policy.runtime.Bridge)):
            worker=finite.m.Worker(local,6)
            self.assertEqual(worker.discovery()[1],[(3.,VENDOR+1),(1,VENDOR)])
            before=len(local.calls)
            self.assertEqual(worker.discovery(VENDOR)[1],[(1,VENDOR)])
            self.assertFalse(any(m.startswith('session.') for m,_ in local.calls[before:]))
            self.assertEqual(local.mutations(),[])

    def test_finite_metadata_reader_accepts_actual_sized_catalog_with_one_matching_read(self):
        import test_town_maintenance as finite
        result=policy_reply(service_source(),dict(sessionId='body1',selfGuid='6'))
        result['catalog']=[dict(name='actual-reflected-metadata',methods='x'*440000)]
        raw=json.dumps(dict(jsonrpc='2.0',id=1,result=result)).encode()
        self.assertGreater(len(raw),262144)
        class Opener:
            def __init__(self):self.calls=[]
            def open(self,request,timeout):
                self.calls.append((json.loads(request.data),timeout))
                return io.BytesIO(raw)
        opener=Opener()
        rpc=finite.m.RPC(finite.URL,opener=opener)
        self.assertEqual(rpc.call('session.attach',dict(sessionId='body1',characterGuid='6')),result)
        self.assertEqual(len(opener.calls),1)
        self.assertEqual(opener.calls[0][0]['method'],'session.attach')
        self.assertEqual(opener.calls[0][0]['params'],dict(sessionId='body1',characterGuid='6'))


if __name__=='__main__':unittest.main()
