"""Actual local Lua/descriptor bridge; every game RPC below is a fake."""
import copy
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent.parent
loader = importlib.machinery.SourceFileLoader('gathering_host_tested', str(ROOT/'dev/task-runtime'))
spec = importlib.util.spec_from_loader(loader.name, loader)
host = importlib.util.module_from_spec(spec); loader.exec_module(host)
BINARY = os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY')


class Endpoint:
    url = 'http://127.0.0.1:8090/rpc'

    def __init__(self, kind='node'):
        self.guid, self.sid, self.kind = '87', 'aw-gathering-test', kind
        self.skill, self.spell = (182, 2366) if kind == 'node' else (393, 8613)
        self.target_guid = str(((0xf110 if kind == 'node' else 0xf130)<<48) | (1234<<24) | 9)
        self.pose = dict(known=True,mapKnown=True,mapId=0,x=0,y=0,z=5,revision='1',estimated=True)
        self.target = dict(guid=self.target_guid,entry=1234,present=True,mapKnown=True,mapId=0,
            positionKnown=True,x=2,y=0,z=5,positionRevision='6',aliveKnown=True,alive=False,healthKnown=True,health=0)
        self.calls, self.lease, self.operation = [], '', {}
        self.revision, self.inventory, self.skills_revision = 10, 5, 3
        self.health, self.combat, self.moving, self.motion_unknown = 100, False, False, False
        self.active, self.known, self.requirements, self.fully_looted = False, True, True, True
        self.rank, self.required, self.auto_complete = 10, 1, True
        self.nav_reason, self.ambiguous, self.after_release = 'exploration_within_distance', '', None

    def actions(self):
        return dict(sessionId=self.sid,selfGuid=self.guid,position=copy.deepcopy(self.pose),
            interactionOwner='exploration' if self.lease else '',interactionHarvest=bool(self.lease),
            interactionLeaseRemainingMs=30000 if self.lease else 0,moving=self.moving,motionUnconfirmed=self.motion_unknown)

    def world(self):
        # No owner is present. Native harvest custody protects the actual party;
        # the policy must not invent an offline owner's position or require one.
        return dict(self=dict(guid=self.guid,present=True,aliveKnown=True,alive=True,mapKnown=True,mapId=0,
            unitFlagsKnown=True,unitFlags=0x80000 if self.combat else 0,movementFlagsKnown=True,
            movementFlags=0x70000000,healthKnown=True,health=self.health,maxHealth=100))

    def professions(self):
        near=abs(self.pose['x']-self.target['x']) <= 3
        ready=near and not self.lease and not self.active
        row=dict(ready=ready,approachable=True,requiredSkill=self.required,fullyLootedObserved=self.fully_looted)
        row['object' if self.kind=='node' else 'creature']=copy.deepcopy(self.target)
        return dict(sessionId=self.sid,selfGuid=self.guid,revision=str(self.revision),skillsKnown=True,
            spellsKnown=True,skillsRevision=str(self.skills_revision),skills=[dict(skillId=self.skill,rank=self.rank,maximum=75)],
            inventoryKnown=True,inventoryRevision=str(self.inventory),active=self.active,operation=copy.deepcopy(self.operation),
            recipes=[dict(skillId=self.skill,spellId=self.spell,known=self.known,
                requirementsVerified=self.requirements,ready=ready)],
            **{'herbTargets' if self.kind=='node' else 'skinningTargets':[row]})

    def confirm(self):
        self.inventory+=1;self.revision+=1;self.active=False
        self.operation.update(status='COMPLETED',castTransportWritten=True,ownGoObserved=True,
            lootWindowObserved=True,lootReleaseObserved=True,outputInventoryObserved=True,
            ownLootedItemsObserved=2,lootSlotsRemoved=1,leaseReleased=True,
            observedInventoryRevision=str(self.inventory),beforeRank=self.rank,afterRank=self.rank,skillIncreased=False)

    def call(self, method, params, timeout=5):
        self.calls.append((method,copy.deepcopy(params)))
        if method==self.ambiguous: raise host.Error('rpc_result_unconfirmed')
        if method=='buddy-actions.status': return self.actions()
        if method=='buddy-world.snapshot': return self.world()
        if method=='buddy-professions.state': return self.professions()
        if method=='buddy-actions.acquire_interaction':
            assert not self.lease and params==dict(owner='exploration',leaseMs=30000,harvest=True)
            self.lease='exact-private-route-token'
            return dict(token=self.lease,state=self.actions())
        if method=='buddy-actions.travel_to':
            assert self.lease and params['token']==self.lease and params.get('mapId',0)==0
            assert params['x']==self.target['x'] and params.get('y',0)==0 and params['z']==5 and params['arrivalDistance']==2.5
            if self.nav_reason=='exploration_within_distance': self.pose['x']=params['x']
            result=self.actions();result['reason']=self.nav_reason;return result
        if method=='buddy-actions.release_interaction':
            assert self.lease and params==dict(owner='exploration',token=self.lease)
            self.lease=''
            if self.after_release: self.after_release()
            return self.actions()
        if method=='buddy-professions.attempt':
            assert params['sessionId']==self.sid and params['targetGuid']==self.target_guid
            assert params['skillId']==self.skill and params['spellId']==self.spell
            assert params['targetPositionRevision']==self.target['positionRevision']
            assert params['expectedRevision']==str(self.revision)
            assert params['expectedInventoryRevision']==str(self.inventory)
            assert params['expectedSkillsRevision']==str(self.skills_revision)
            assert not self.lease and abs(self.pose['x']-self.target['x'])<=3
            self.operation=dict(operationId='native-operation',requestId=params['requestId'],skillId=self.skill,spellId=self.spell,
                targetGuid=self.target_guid,targetEntry=self.target['entry'],targetPositionRevision=params['targetPositionRevision'],
                baselineInventoryRevision=params['expectedInventoryRevision'],baselineSkillsRevision=params['expectedSkillsRevision'],
                status='WAITING_CAST')
            self.active=True
            if self.auto_complete: self.confirm()
            return self.professions()
        raise AssertionError(method)


@unittest.skipUnless(BINARY, 'set BUDDY_LUAPOLICY_TEST_BINARY to the compiled local bridge')
class GatheringTests(unittest.TestCase):
    def setUp(self):
        temporary=tempfile.TemporaryDirectory(dir='/private/tmp');self.addCleanup(temporary.cleanup)
        self.root=Path(temporary.name);self.rpc=Endpoint();self.clock=1700000000.
        self.bridge=host.Bridge(BINARY,ROOT/'modules');self.source=ROOT/'behaviors/gathering.lua'

    def goal(self):
        return dict(id='gathering-test',kind='resource_child',params=dict(resources=[dict(skillId=self.rpc.skill,
            spellId=self.rpc.spell,targetKind=self.rpc.kind)],maxAttempts=1,approachDistance=40,requestPrefix='gather_test'),
            allow=['buddy-actions.status','buddy-world.snapshot','buddy-professions.state','buddy-professions.attempt',
                'buddy-actions.acquire_interaction','buddy-actions.travel_to','buddy-actions.release_interaction'],
            observations=dict(actions=dict(method='buddy-actions.status',params={},select=['sessionId','selfGuid','position',
                'moving','traveling','motionUnconfirmed','castPending','autoAttacking','autoAttackRequested','autoAttackUnconfirmed',
                'interactionOwner','interactionHarvest','interactionLeaseRemainingMs','practicing','recovery']),
                world=dict(method='buddy-world.snapshot',params={},select=['self']),
                professions=dict(method='buddy-professions.state',params={},select=['sessionId','selfGuid','revision','skillsKnown',
                    'spellsKnown','skillsRevision','skills','inventoryKnown','inventoryRevision','active','closing',
                    'operation','recipes','herbTargets','skinningTargets'])))

    def runner(self,store):
        return host.Runner(store,self.bridge,self.rpc,int(self.rpc.guid),self.rpc.sid,self.goal(),self.source,
            ROOT/'behaviors/task-stack.lua',rpc_timeout=35)

    def tick(self,runner):
        self.clock+=1
        with patch.object(host.time,'time',return_value=self.clock): return runner.tick()

    def until(self,runner,predicate,limit=35):
        for _ in range(limit):
            self.tick(runner)
            if predicate(): return
        self.fail(str(host.status(runner.data)))

    def calls(self,method): return [params for name,params in self.rpc.calls if name==method]

    def test_node_and_party_cleared_corpse_confirm_materials_without_claiming_skill_increase(self):
        for kind in ('node','corpse'):
            with self.subTest(kind=kind):
                self.rpc=Endpoint(kind)
                with host.Store(self.root/kind) as store:
                    runner=self.runner(store);self.until(runner,lambda:runner.data['done'])
                    record=runner.data['memory']['gatheringAttempts'][0]
                    self.assertEqual(record['phase'],'completed');self.assertFalse(record['skillIncreased'])
                    self.assertEqual(record['ownLootedItemsObserved'],2)
                    self.assertEqual(record['targetGuid'],self.rpc.target_guid)
                    self.assertEqual(len(self.calls('buddy-professions.attempt')),1)
                    self.assertFalse(self.calls('buddy-actions.acquire_interaction'))

    def test_observed_approach_uses_one_guarded_route_and_releases_before_cast(self):
        self.rpc.target['x']=18
        with host.Store(self.root/'runtime') as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['done'])
            methods=[method for method,_ in self.rpc.calls]
            self.assertEqual(len(self.calls('buddy-actions.travel_to')),1)
            self.assertLess(methods.index('buddy-actions.acquire_interaction'),methods.index('buddy-actions.travel_to'))
            self.assertLess(methods.index('buddy-actions.release_interaction'),methods.index('buddy-professions.attempt'))
            self.assertFalse(self.rpc.lease)

    def test_child_returns_to_existing_parent_and_preserves_parent_progress(self):
        source=self.source.read_text()
        self.source=self.root/'composed.lua'
        self.source.write_text('local gathering=(function()\n'+source+'\nend)()\n'+'''
local handlers=gathering.handlers
handlers.parent=function(c,f)
    if f.stage=="start" then
        f.stage="after_child";f.preserved="journal-position";gathering.push(c,c.state.goal.params);return
    end
    assert(f.child and f.child.confirmed==1 and f.preserved=="journal-position")
    c.memory.parentResumed=true;c.memory.childResult=f.child;tasks.pop(c,{status="parent_resumed"})
end
return {decide=function(state)
    if #(state.pending or {})>0 then return {memory=state.memory,phase="blocked_on_ambiguous_call",done=false} end
    return tasks.run(state,handlers,"parent")
end}
''')
        with host.Store(self.root/'runtime') as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['done'])
            self.assertTrue(runner.data['memory']['parentResumed'])
            self.assertEqual(runner.data['memory']['childResult']['status'],'gathered')

    def test_native_learning_rank_flags_and_range_qualification_are_not_invented(self):
        changes=[lambda r:setattr(r,'known',False),lambda r:setattr(r,'requirements',False),
            lambda r:setattr(r,'required',20),lambda r:r.target.update(positionKnown=False),
            lambda r:r.target.update(mapId=1),lambda r:r.target.update(x=41),lambda r:r.target.update(z=7),
            lambda r:setattr(r,'fully_looted',False)]
        for index,change in enumerate(changes):
            with self.subTest(index=index):
                self.rpc=Endpoint('corpse');change(self.rpc)
                with host.Store(self.root/str(index)) as store:
                    runner=self.runner(store);self.until(runner,lambda:runner.data['done'])
                    self.assertFalse(self.calls('buddy-professions.attempt'))
                    self.assertFalse(self.calls('buddy-actions.acquire_interaction'))

    def test_missing_native_mining_recipe_is_no_opportunity_and_never_a_cast(self):
        original=self.goal
        def mining_goal():
            goal=original();goal['params']['resources']=[dict(skillId=186,spellId=2575,targetKind='node')];return goal
        self.goal=mining_goal
        with host.Store(self.root/'runtime') as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['done'])
            self.assertFalse(self.calls('buddy-professions.attempt'));self.assertFalse(self.calls('buddy-actions.travel_to'))

    def test_known_combat_health_and_motion_uncertainty_wait_without_mutation(self):
        for field,value in (('combat',True),('health',84),('motion_unknown',True),('moving',True)):
            with self.subTest(field=field):
                self.rpc=Endpoint();setattr(self.rpc,field,value)
                with host.Store(self.root/field) as store:
                    runner=self.runner(store)
                    for _ in range(3): self.tick(runner)
                    self.assertEqual(runner.data['phase'],'gathering_waiting_for_safe_idle')
                    self.assertFalse(runner.data['done']);self.assertFalse(self.calls('buddy-professions.attempt'))

    def test_partial_route_releases_exact_token_but_never_casts_or_claims_materials(self):
        self.rpc.target['x']=18;self.rpc.nav_reason='exploration_step_or_transport_failed'
        with host.Store(self.root/'runtime') as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['done'])
            self.assertEqual(len(self.calls('buddy-actions.travel_to')),1)
            self.assertEqual(len(self.calls('buddy-actions.release_interaction')),1)
            self.assertFalse(self.calls('buddy-professions.attempt'));self.assertFalse(self.rpc.lease)
            self.assertEqual(runner.data['memory']['gatheringAttempts'][0]['phase'],'blocked')

    def test_actual_target_revision_change_after_release_cannot_authorize_cast(self):
        self.rpc.target['x']=18;self.rpc.after_release=lambda:self.rpc.target.update(positionRevision='7')
        with host.Store(self.root/'runtime') as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['done'])
            self.assertFalse(self.calls('buddy-professions.attempt'));self.assertFalse(self.rpc.lease)

    def test_attempt_and_acquire_ambiguity_are_preserved_without_retry_after_restart(self):
        for method in ('buddy-professions.attempt','buddy-actions.acquire_interaction','buddy-actions.travel_to','buddy-actions.release_interaction'):
            with self.subTest(method=method):
                self.rpc=Endpoint();self.rpc.ambiguous=method
                if not method.endswith('.attempt'): self.rpc.target['x']=18
                folder=self.root/method
                with host.Store(folder) as store:
                    runner=self.runner(store);self.until(runner,lambda:bool(runner.data['pending']))
                with host.Store(folder) as store:
                    runner=self.runner(store)
                    for _ in range(3): self.tick(runner)
                    self.assertEqual(runner.data['phase'],'blocked_on_ambiguous_call')
                    self.assertEqual(len(self.calls(method)),1)

    def test_wrong_receipt_binding_or_missing_material_proof_never_resumes_parent(self):
        corruptions=[('requestId','foreign'),('targetGuid','99'),('targetEntry',999),('targetPositionRevision','7'),
            ('skillId',999),('spellId',999),('baselineInventoryRevision','4'),('baselineSkillsRevision','2'),
            ('ownGoObserved',False),('lootWindowObserved',False),('lootReleaseObserved',False),
            ('outputInventoryObserved',False),('ownLootedItemsObserved',0),('lootSlotsRemoved',0),
            ('observedInventoryRevision','5'),('leaseReleased',False),('cleanupUnconfirmed',True)]
        for index,(field,value) in enumerate(corruptions):
            with self.subTest(field=field):
                self.rpc=Endpoint();self.rpc.auto_complete=False
                with host.Store(self.root/str(index)) as store:
                    runner=self.runner(store);self.until(runner,lambda:bool(self.rpc.operation))
                    self.rpc.confirm();self.rpc.operation[field]=value
                    for _ in range(3): self.tick(runner)
                    self.assertFalse(runner.data['done'])
                    self.assertEqual(len(self.calls('buddy-professions.attempt')),1)

    def test_terminal_rejection_returns_blocked_and_same_source_is_never_retried(self):
        self.rpc.auto_complete=False
        with host.Store(self.root/'runtime') as store:
            runner=self.runner(store);self.until(runner,lambda:bool(self.rpc.operation))
            self.rpc.active=False;self.rpc.operation.update(status='SERVER_REJECTED',leaseReleased=True)
            self.until(runner,lambda:runner.data['done'])
            self.assertEqual(runner.data['memory']['gatheringAttempts'][0]['phase'],'blocked')
            for _ in range(3): self.tick(runner)
            self.assertEqual(len(self.calls('buddy-professions.attempt')),1)

    def test_distinct_source_bound_and_repeat_child_skips_old_guid(self):
        self.rpc.auto_complete=True
        original=self.goal
        def goal():
            value=original();value['params']['maxAttempts']=2;return value
        self.goal=goal
        with host.Store(self.root/'runtime') as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['done'])
            self.assertEqual(len(self.calls('buddy-professions.attempt')),1)
            self.assertEqual(len(runner.data['memory']['gatheringAttempts']),1)

    def test_malformed_acquisition_and_unclean_release_reply_are_never_reissued(self):
        for method in ('buddy-actions.acquire_interaction','buddy-actions.release_interaction'):
            with self.subTest(method=method):
                self.rpc=Endpoint();self.rpc.target['x']=18
                original=self.rpc.call
                def corrupted(name,params,timeout=5):
                    value=original(name,params,timeout)
                    if name==method:
                        if name.endswith('acquire_interaction'): value.pop('token')
                        else:
                            self.rpc.lease='still-unconfirmed';value=self.rpc.actions()
                    return value
                self.rpc.call=corrupted
                with host.Store(self.root/method) as store:
                    runner=self.runner(store)
                    self.until(runner,lambda:runner.data['phase'] in ('gathering_acquisition_unconfirmed','gathering_route_cleanup_unconfirmed'))
                    for _ in range(3): self.tick(runner)
                    self.assertEqual(len(self.calls(method)),1)
                    self.assertFalse(self.calls('buddy-professions.attempt'));self.assertFalse(runner.data['done'])

    def test_foreign_profession_body_and_unsettled_old_operation_block_new_work(self):
        for scenario in ('foreign_body','old_waiting'):
            with self.subTest(scenario=scenario):
                self.rpc=Endpoint();original=self.rpc.professions
                def modified():
                    value=original()
                    if scenario=='foreign_body': value['sessionId']='aw-another-body'
                    else: value['operation']=dict(operationId='previous',status='WAITING_CAST')
                    return value
                self.rpc.professions=modified
                with host.Store(self.root/scenario) as store:
                    runner=self.runner(store)
                    for _ in range(3): self.tick(runner)
                    self.assertFalse(self.calls('buddy-professions.attempt'));self.assertFalse(runner.data['done'])

    def test_literal_protojson_omitted_zero_axes_and_map_work_only_with_positive_known_flags(self):
        for target_x in (2,18):
            with self.subTest(target_x=target_x):
                self.rpc=Endpoint();self.rpc.target['x']=target_x
                actions,professions,world=self.rpc.actions,self.rpc.professions,self.rpc.world
                def omit_zero(point):
                    for key in ('x','y','z','mapId'):
                        if point.get(key)==0: point.pop(key)
                    return point
                def actual_actions():
                    value=actions();omit_zero(value['position']);return value
                def actual_professions():
                    value=professions();omit_zero(value['herbTargets'][0]['object']);return value
                def actual_world():
                    value=world();omit_zero(value['self']);return value
                self.rpc.actions,self.rpc.professions,self.rpc.world=actual_actions,actual_professions,actual_world
                with host.Store(self.root/str(target_x)) as store:
                    runner=self.runner(store);self.until(runner,lambda:runner.data['done'])
                    self.assertEqual(len(self.calls('buddy-professions.attempt')),1)
                    self.assertEqual(runner.data['memory']['gatheringAttempts'][0]['phase'],'completed')


if __name__=='__main__': unittest.main()
