"""Real typed Lua bridge and temporary journals; every game RPC is a fake."""
import copy
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent.parent
loader = importlib.machinery.SourceFileLoader('fishing_host_tested', str(ROOT/'dev/task-runtime'))
spec = importlib.util.spec_from_loader(loader.name, loader)
host = importlib.util.module_from_spec(spec); loader.exec_module(host)
BINARY = os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY')


class Endpoint:
    url = 'http://127.0.0.1:8090/rpc'
    def __init__(self, guid=87):
        self.guid, self.sid, self.calls = str(guid), 'aw-fishing-'+str(guid), []
        self.params = dict(skillId=356,spellId=7620,basicPole=6256,strongPole=6365,targetRank=10,
            trainerSpell=7733,maxCasts=20,basicBudgetCopper=100,trainingBudgetCopper=100,
            strongBudgetCopper=1000,vendor={'entry':5494},trainer={'entry':5493},sharedKey='fishing-rank10')
        self.vendor = str((0xf130<<48)|(5494<<24)|12)
        self.trainer = str((0xf130<<48)|(5493<<24)|11)
        self.original, self.basic, self.strong = [str((0x4000<<48)+x+guid*100) for x in (1,2,3)]
        self.hand = dict(slotKnown=True,metadataKnown=True,itemGuid=self.original,entry=35)
        self.bag = [dict(slot=i) for i in range(16)]
        self.rank,self.skills_revision,self.backpack_revision,self.copper_revision = 0,1,1,1
        self.revision,self.copper,self.restore_required = 1,5000,False
        self.offer,self.supply_op,self.purchase,self.prof_op = {},{},{},{}
        self.discoverable,self.stock,self.stale_quote,self.ambiguous = True,1,False,''
        self.rank_step,self.native_failure,self.recipe_ready = 1,False,True
        self.cast_count,self.next_source = 0,0
        self.extra_items=[]

    def actions(self):
        return dict(sessionId=self.sid,selfGuid=self.guid,position=dict(known=True,mapKnown=True,mapId=0,x=1,y=2,z=3))

    def world(self):
        skills=[dict(slot=i) for i in range(128)]
        if self.rank: skills[7].update(skillId=356,value=self.rank,maxValue=75)
        bags=[dict(bag=0,slotKnown=True,contentsKnown=True,slotCount=16,slots=copy.deepcopy(self.bag))]
        bags += [dict(bag=i,slotKnown=True,contentsKnown=True,slotCount=len(self.extra_items) if i==1 else 0,
                      slots=copy.deepcopy(self.extra_items) if i==1 else []) for i in range(1,5)]
        return dict(self=dict(guid=self.guid,present=True,aliveKnown=True,alive=True,healthKnown=True,health=100,
                maxHealth=100,mapKnown=True,unitFlagsKnown=True,movementFlagsKnown=True,mainhand=copy.deepcopy(self.hand),
                offhand=dict(slotKnown=True)),
            backpackKnown=True,backpackRevision=str(self.backpack_revision),backpack=copy.deepcopy(self.bag),
            selfSkillsKnown=True,selfSkillsRevision=str(self.skills_revision),selfSkills=skills,inventoryKnown=True,inventoryBags=bags)

    def supplies(self):
        return dict(sessionId=self.sid,selfGuid=self.guid,revision=str(self.revision),backpackKnown=True,
            backpackRevision=str(self.backpack_revision),backpack=copy.deepcopy(self.bag),copperKnown=True,
            copper=self.copper,copperRevision=str(self.copper_revision),skillsKnown=True,
            skillsRevision=str(self.skills_revision),mainhand=copy.deepcopy(self.hand),offer=copy.deepcopy(self.offer),
            operation=copy.deepcopy(self.supply_op),restoreRequired=self.restore_required,
            originalWeaponGuid=self.original if self.restore_required else '0',
            equippedPoleGuid=self.hand['itemGuid'] if self.restore_required else '0')

    def training(self):
        return dict(sessionId=self.sid,copperKnown=True,copper=self.copper,lastPurchase=copy.deepcopy(self.purchase))

    def professions(self):
        return dict(sessionId=self.sid,selfGuid=self.guid,revision=str(self.revision),skillsKnown=True,
            skillsRevision=str(self.skills_revision),inventoryKnown=True,inventoryRevision=str(self.backpack_revision),
            backpackKnown=True,backpackRevision=str(self.backpack_revision),operation=copy.deepcopy(self.prof_op),
            recipes=[dict(skillId=356,spellId=7620,known=self.rank>0,requirementsVerified=True,
                ready=self.rank>0 and self.recipe_ready,reason='' if self.recipe_ready else 'not_here')])

    def npc(self, guid, entry):
        return dict(creature=dict(guid=guid,entry=entry,present=True,aliveKnown=True,alive=True,mapKnown=True,
            positionKnown=True),interactable=self.discoverable)

    def own_pole(self, entry=6256, equipped=False):
        item=dict(itemGuid=self.basic if entry==6256 else self.strong,entry=entry,stackCount=1,metadataKnown=True)
        if equipped: self.hand=dict(slotKnown=True,**item)
        else: self.bag[2]=dict(slot=2,**item)

    def call(self, method, p, timeout=5):
        self.calls.append((method,copy.deepcopy(p)))
        if method==self.ambiguous: raise host.Error('rpc_result_unconfirmed')
        if method=='buddy-actions.status': return self.actions()
        if method=='buddy-world.snapshot': return self.world()
        if method=='buddy-supplies.state': return self.supplies()
        if method=='buddy-supplies.discover': return dict(state=self.supplies(),nearby=[self.npc(self.vendor,5494)])
        if method=='buddy-supplies.list':
            assert p==dict(sessionId=self.sid,npcGuid=self.vendor)
            self.offer=dict(revision='9',npcGuid=self.vendor,expiresInMs='10000',items=[
                dict(slot=1,entry=6256,buyCount=1,priceCopper=23,availableCount=-1),
                dict(slot=2,entry=6365,buyCount=1,priceCopper=902,availableCount=self.stock)])
            return self.supplies()
        if method=='buddy-supplies.buy':
            assert p['sessionId']==self.sid and p['npcGuid']==self.vendor and p['offerRevision']=='9'
            assert p['requestId'].replace('_','').replace('-','').isalnum()
            if self.stale_quote:
                self.supply_op=dict(requestId=p['requestId'],kind='buy',status='BLOCKED',reason='state_revision_changed',leaseReleased=True)
                return self.supplies()
            assert p['expectedRevision']==str(self.revision)
            assert p['expectedBackpackRevision']==str(self.backpack_revision)
            assert p['expectedSkillsRevision']==str(self.skills_revision)
            assert p['expectedCopperRevision']==str(self.copper_revision)
            assert p['count']==1 and p['itemId'] in (6256,6365)
            entry=p['itemId'];item_guid=self.basic if entry==6256 else self.strong
            slot=next(i for i,item in enumerate(self.bag) if not item.get('itemGuid'))
            self.bag[slot]=dict(slot=slot,itemGuid=item_guid,entry=entry,stackCount=1,metadataKnown=True)
            self.backpack_revision+=1;self.copper_revision+=1;self.revision+=1;self.copper-=p['expectedPriceCopper']
            self.supply_op=dict(requestId=p['requestId'],kind='buy',status='CONFIRMED',transportWritten=True,
                leaseReleased=True,npcGuid=self.vendor,itemId=entry,count=1,costCopper=p['expectedPriceCopper'],
                serverSuccessObserved=True,inventoryConfirmed=True,copperConfirmed=True,
                baselineBackpackRevision=p['expectedBackpackRevision'],baselineCopperRevision=p['expectedCopperRevision'])
            return self.supplies()
        if method=='buddy-training.discover_professions': return dict(state=self.training(),nearby=[self.npc(self.trainer,5493)])
        if method=='buddy-training.list':
            s=self.training();s['offer']=dict(revision='3',npcGuid=self.trainer,trainerType=2,expiresInMs='10000',
                spells=[dict(spellId=7733,costCopper=10,professionSkillId=356,learnedSpellId=7620)])
            return s
        if method=='buddy-training.buy_profession':
            assert p['skillId']==356 and p['purchase']['npcGuid']==self.trainer
            assert p['purchase']['spellId']==7733 and p['purchase']['offerRevision']=='3'
            baseline=self.skills_revision;self.rank=1;self.skills_revision+=1;self.revision+=1
            self.purchase=dict(npcGuid=self.trainer,spellId=7733,professionSkillId=356,costCopper=10,
                status='TRAINING_CONFIRMED',transportWritten=True,serverSuccessObserved=True,
                professionSkillObserved=True,baselineSkillsRevision=str(baseline),confirmedSkillsRevision=str(self.skills_revision))
            return self.training()
        if method=='buddy-training.state': return self.training()
        if method in ('buddy-supplies.equip','buddy-supplies.restore'):
            assert p['sessionId']==self.sid and p['originalWeaponGuid']==self.original
            assert p['expectedRevision']==str(self.revision) and p['expectedBackpackRevision']==str(self.backpack_revision)
            assert p['expectedSkillsRevision']==str(self.skills_revision)
            slot=p.get('backpackSlot',0)
            source=self.bag[slot];assert source['metadataKnown'] and source['itemGuid']==p['itemGuid']
            old=self.hand;self.hand=dict(slotKnown=True,metadataKnown=True,itemGuid=source['itemGuid'],entry=source['entry'])
            self.bag[slot]=dict(slot=slot,metadataKnown=True,itemGuid=old['itemGuid'],entry=old['entry'],stackCount=1)
            self.backpack_revision+=1;self.revision+=1;self.restore_required=method.endswith('.equip')
            self.supply_op=dict(requestId=p['requestId'],kind=method.rsplit('.',1)[-1],status='CONFIRMED',
                itemGuid=p['itemGuid'],originalWeaponGuid=self.original,transportWritten=True,equipmentConfirmed=True,leaseReleased=True)
            return self.supplies()
        if method=='buddy-professions.state': return self.professions()
        if method=='buddy-professions.attempt':
            assert p['sessionId']==self.sid and p['skillId']==356 and p['spellId']==7620
            assert p['expectedRevision']==str(self.revision) and p['expectedSkillsRevision']==str(self.skills_revision)
            assert p['expectedInventoryRevision']==str(self.backpack_revision)
            assert self.hand['entry'] in (6256,6365)
            before=self.rank;baseline=self.skills_revision;self.cast_count+=1
            if not self.native_failure:
                self.rank+=self.rank_step
                if self.rank_step:self.skills_revision+=1
            self.revision+=1
            self.prof_op=dict(operationId='native-operation',requestId=p['requestId'],skillId=356,spellId=7620,
                status='SERVER_REJECTED' if self.native_failure else 'COMPLETED',failureCode=50 if self.native_failure else 0,
                reason='requires_water' if self.native_failure else '',baselineSkillsRevision=str(baseline),
                baselineInventoryRevision=p['expectedInventoryRevision'],observedSkillsRevision=str(self.skills_revision),
                beforeRank=before,afterRank=self.rank,skillIncreased=self.rank>before,leaseReleased=True,
                castTransportWritten=True,ownGoObserved=not self.native_failure,ownBobberObserved=True,
                channelStartObserved=True,channelStopObserved=True,splashObserved=True,
                lootWindowObserved=True,lootReleaseObserved=True)
            return self.professions()
        raise AssertionError(method)


@unittest.skipUnless(BINARY, 'set BUDDY_LUAPOLICY_TEST_BINARY to the compiled local bridge')
class FishingTests(unittest.TestCase):
    def setUp(self):
        temp=tempfile.TemporaryDirectory(dir='/private/tmp');self.addCleanup(temp.cleanup)
        self.root=Path(temp.name);self.rpc=Endpoint();self.clock=1700000000.
        self.board=host.SharedBoard(self.root/'shared')
        self.bridge=host.Bridge(BINARY,ROOT/'modules')

    def goal(self):
        return dict(id='fishing-'+self.rpc.guid,kind='fishing',params=copy.deepcopy(self.rpc.params),sharedKeys=['fishing-rank10'],
            allow=['buddy-actions.status','buddy-world.snapshot','buddy-supplies.state','buddy-supplies.discover',
                'buddy-supplies.list','buddy-supplies.buy','buddy-supplies.equip','buddy-supplies.restore',
                'buddy-training.discover_professions','buddy-training.list','buddy-training.buy_profession','buddy-training.state',
                'buddy-professions.state','buddy-professions.attempt'],
            observations=dict(actions=dict(method='buddy-actions.status',params={},select=['sessionId','selfGuid','position',
                    'moving','traveling','motionUnconfirmed','castPending','autoAttacking','autoAttackRequested','autoAttackUnconfirmed','interactionOwner','recovery']),
                world=dict(method='buddy-world.snapshot',params={},select=['self','backpackKnown','backpackRevision','backpack',
                    'selfSkillsKnown','selfSkillsRevision','selfSkills','inventoryKnown','inventoryBags'])))

    def runner(self,store):
        return host.Runner(store,self.bridge,self.rpc,int(self.rpc.guid),self.rpc.sid,self.goal(),
            ROOT/'behaviors/fishing.lua',ROOT/'behaviors/task-stack.lua',rpc_timeout=35,shared=self.board)

    def tick(self,runner):
        self.clock+=6
        with patch.object(host.time,'time',return_value=self.clock):return runner.tick()

    def until(self,runner,predicate,limit=180):
        for _ in range(limit):
            self.tick(runner)
            if predicate():return
        self.fail(str(host.status(runner.data)))

    def calls(self,method):return [p for name,p in self.rpc.calls if name==method]

    def test_purchase_before_learning_exact_owned_equip_actual_rank_and_winner_purchase(self):
        with host.Store(self.root/'runtime') as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['done'])
            self.assertEqual(self.rpc.rank,10);self.assertEqual(self.rpc.cast_count,9)
            self.assertEqual([p['itemId'] for p in self.calls('buddy-supplies.buy')],[6256,6365])
            names=[name for name,_ in self.rpc.calls]
            self.assertLess(names.index('buddy-supplies.buy'),names.index('buddy-training.buy_profession'))
            self.assertEqual(self.calls('buddy-supplies.equip')[0]['itemGuid'],self.rpc.basic)
            self.assertEqual(self.rpc.hand['itemGuid'],self.rpc.original);self.assertFalse(self.rpc.restore_required)
            self.assertTrue(any(row.get('itemGuid')==self.rpc.basic for row in self.rpc.bag))
            self.assertTrue(any(row.get('itemGuid')==self.rpc.strong for row in self.rpc.bag))
            self.assertEqual(self.board.read('fishing-rank10')['winnerGuid'],self.rpc.guid)
            self.assertEqual(runner.data['memory']['observedPureRank'],10)
        before=len(self.rpc.calls)
        with host.Store(self.root/'runtime') as store:self.tick(self.runner(store))
        self.assertFalse(any(name!='buddy-actions.status' for name,_ in self.rpc.calls[before:]))

    def test_known_skill_and_pole_skip_buy_and_training_and_existing_restore_history(self):
        self.rpc.rank=10;self.rpc.own_pole(equipped=True)
        self.rpc.restore_required=True
        self.rpc.bag[5]=dict(slot=5,itemGuid=self.rpc.original,entry=35,stackCount=1,metadataKnown=True)
        with host.Store(self.root/'runtime') as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['done'])
            self.assertEqual(self.calls('buddy-training.buy_profession'),[])
            self.assertEqual(self.calls('buddy-supplies.equip'),[]);self.assertEqual(self.calls('buddy-supplies.restore'),[])
            self.assertEqual([p['itemId'] for p in self.calls('buddy-supplies.buy')],[6365])
        self.assertEqual(self.rpc.hand['itemGuid'],self.rpc.basic)
        self.assertTrue(self.rpc.restore_required)
        self.assertEqual(self.rpc.bag[5]['itemGuid'],self.rpc.original)

    def test_cast_count_without_pure_rank_progress_does_not_win_or_buy_strong(self):
        self.rpc.rank=1;self.rpc.rank_step=0;self.rpc.own_pole(equipped=True);self.rpc.params['maxCasts']=2
        with host.Store(self.root/'runtime') as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['phase']=='fishing_cast_budget_exhausted')
            self.assertEqual(self.rpc.cast_count,2);self.assertEqual(self.rpc.rank,1)
            self.assertFalse(runner.data['done']);self.assertIsNone(self.board.read('fishing-rank10'))
            self.assertEqual(self.calls('buddy-supplies.buy'),[])

    def test_only_shared_winner_can_buy_strong(self):
        self.rpc.rank=10;self.rpc.own_pole(equipped=True)
        self.board.claim('fishing-rank10',99,'aw-other','other-goal',
            [dict(observation='own',path='rank',operator='gte',value=10)],{'own':{'rank':10}},lambda:None)
        with host.Store(self.root/'runtime') as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['done'])
            self.assertEqual(self.calls('buddy-supplies.buy'),[])
            self.assertEqual(runner.data['memory']['election']['winnerGuid'],'99')

    def test_stale_quote_and_unavailable_stock_are_not_retried_as_new_purchases(self):
        self.rpc.stale_quote=True
        with host.Store(self.root/'runtime') as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['phase'].startswith('supply_blocked'))
            for _ in range(6):self.tick(runner)
            self.assertEqual(len(self.calls('buddy-supplies.buy')),1)
            self.assertEqual(self.calls('buddy-training.buy_profession'),[]);self.assertFalse(runner.data['done'])
        self.rpc=Endpoint();self.rpc.rank=10;self.rpc.own_pole(equipped=True);self.rpc.stock=0
        with host.Store(self.root/'no-stock') as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['phase']=='vendor_stock_price_or_budget_blocked')
            self.assertEqual(self.calls('buddy-supplies.buy'),[])

    def test_ambiguous_native_or_purchase_call_stays_reserved_across_restart(self):
        for method in ('buddy-supplies.buy','buddy-professions.attempt'):
            with self.subTest(method=method):
                self.rpc=Endpoint();self.rpc.ambiguous=method
                if method.endswith('.attempt'):self.rpc.rank=1;self.rpc.own_pole(equipped=True)
                folder=self.root/method
                with host.Store(folder) as store:
                    runner=self.runner(store);self.until(runner,lambda:bool(runner.data['pending']))
                with host.Store(folder) as store:
                    runner=self.runner(store)
                    for _ in range(4):self.tick(runner)
                    self.assertEqual(runner.data['phase'],'blocked_on_ambiguous_call')
                    self.assertEqual(len(runner.data['pending']),1);self.assertEqual(len(self.calls(method)),1)

    def test_native_water_failure_is_explicit_and_never_counts_as_skill_or_recasts(self):
        self.rpc.rank=1;self.rpc.own_pole(equipped=True);self.rpc.native_failure=True
        with host.Store(self.root/'runtime') as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['phase']=='fishing_server_rejected')
            reads=len(self.calls('buddy-professions.state'))
            for _ in range(5):self.tick(runner)
            self.assertEqual(self.rpc.cast_count,1);self.assertEqual(self.rpc.rank,1)
            self.assertEqual(len(self.calls('buddy-professions.state')),reads)
            self.assertFalse(runner.data['done']);self.assertIsNone(self.board.read('fishing-rank10'))

    def test_unknown_metadata_and_foreign_receipt_cannot_advance(self):
        self.rpc.rank=1;self.rpc.own_pole();self.rpc.bag[2]['metadataKnown']=False
        with host.Store(self.root/'unknown') as store:
            runner=self.runner(store);self.tick(runner)
            self.assertEqual(runner.data['phase'],'owned_pole_observations_unknown')
            self.assertEqual(self.calls('buddy-supplies.equip'),[])
        self.rpc=Endpoint();self.rpc.rank=1;self.rpc.own_pole(equipped=True)
        original=self.rpc.professions
        def foreign():
            result=original()
            if result['operation']:result['operation']['requestId']='foreign-request'
            return result
        self.rpc.professions=foreign
        with host.Store(self.root/'foreign') as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['phase']=='fishing_receipt_binding_unavailable')
            for _ in range(4):self.tick(runner)
            self.assertEqual(self.rpc.cast_count,1);self.assertFalse(runner.data['done'])

    def test_unknown_offhand_or_unsettled_native_operation_prevents_new_actions(self):
        self.rpc.rank=1;self.rpc.own_pole()
        original=self.rpc.world
        def missing_offhand():
            world=original();world['self'].pop('offhand');return world
        self.rpc.world=missing_offhand
        with host.Store(self.root/'offhand') as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['phase']=='offhand_unknown_or_occupied')
            self.assertEqual(self.calls('buddy-supplies.equip'),[])
        self.rpc=Endpoint();self.rpc.rank=10;self.rpc.own_pole(equipped=True)
        self.rpc.prof_op=dict(status='OUTCOME_UNCONFIRMED',cleanupUnconfirmed=True)
        with host.Store(self.root/'unsettled') as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['phase']=='previous_profession_outcome_unsettled')
            self.assertIsNone(self.board.read('fishing-rank10'));self.assertEqual(self.calls('buddy-supplies.buy'),[])

    def test_last_budgeted_attempt_can_reach_target_and_strong_carried_is_not_a_beginner_tool(self):
        self.rpc.rank=9;self.rpc.params['maxCasts']=1;self.rpc.own_pole(equipped=True)
        with host.Store(self.root/'last') as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['done'])
            self.assertEqual(self.rpc.cast_count,1);self.assertEqual(self.rpc.rank,10)
        self.rpc=Endpoint();self.rpc.rank=1;self.rpc.own_pole(entry=6365);self.rpc.rank_step=9
        with host.Store(self.root/'beginner') as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['done'])
            self.assertEqual([p['itemId'] for p in self.calls('buddy-supplies.buy')],[6256])
            self.assertEqual(self.calls('buddy-supplies.equip')[0]['itemGuid'],self.rpc.basic)

    def test_owned_pole_in_another_bag_is_kept_without_duplicate_buy_or_blind_move(self):
        self.rpc.rank=1
        self.rpc.extra_items=[dict(slot=0,itemGuid=self.rpc.basic,entry=6256,stackCount=1,metadataKnown=True)]
        with host.Store(self.root/'outside-backpack') as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['phase']=='actual_owned_equipment_source_unavailable')
            self.assertEqual(self.calls('buddy-supplies.buy'),[]);self.assertEqual(self.calls('buddy-supplies.equip'),[])

    def test_stationary_walking_and_passive_modes_allow_work_but_actual_motion_blocks(self):
        self.rpc.rank=10;self.rpc.own_pole(equipped=True)
        original=self.rpc.world
        def walking():
            world=original();world['self']['movementFlags']=0x70000100;return world
        self.rpc.world=walking
        with host.Store(self.root/'walking') as store:
            runner=self.runner(store);self.until(runner,lambda:runner.data['done'])
            self.assertEqual([p['itemId'] for p in self.calls('buddy-supplies.buy')],[6365])
        self.rpc=Endpoint();original=self.rpc.world
        def moving():
            world=original();world['self']['movementFlags']=0x101;return world
        self.rpc.world=moving
        with host.Store(self.root/'moving') as store:
            runner=self.runner(store);self.tick(runner)
            self.assertEqual(runner.data['phase'],'purchase_waiting_for_safe_idle')
            self.assertEqual(self.calls('buddy-supplies.buy'),[])


if __name__=='__main__':unittest.main()
