import copy
import importlib.machinery
import importlib.util
from pathlib import Path
import unittest
import threading
from unittest.mock import patch

loader=importlib.machinery.SourceFileLoader('maintenance_test',str(Path(__file__).with_name('party-maintenance')))
spec=importlib.util.spec_from_loader(loader.name,loader)
m=importlib.util.module_from_spec(spec);loader.exec_module(m)

class RPC:
    def __init__(self):
        self.calls=[]
        self.world={'self':{'guid':'1','present':True,'aliveKnown':True,'alive':True,'unitFlagsKnown':True,'mapKnown':True,'level':4},
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
            'offerRevision':'2','spellId':123,'expectedCostCopper':100,'maxSpendCopper':10000})
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
    def test_actual_wallet_and_cumulative_budget(self):
        self.rpc.training['copper']=99;self.tick()
        self.assertNotIn('buddy-training.buy',self.methods())
        self.rpc.training['copper']=10000;self.rpc.offer['spells']=[{'spellId':123,'costCopper':9990}]
        self.tick();self.rpc.purchase={'id':1,'npcGuid':self.rpc.offer['npcGuid'],'spellId':123,'costCopper':9990,'status':'SERVER_REJECTED'}
        self.rpc.offer['spells']=[{'spellId':124,'costCopper':11}];self.tick()
        self.assertEqual(self.methods().count('buddy-training.buy'),1)
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
        for mutation in [('reservedCopper',101),('identity',{})]:
            bad=copy.deepcopy(stored);bad[mutation[0]]=mutation[1]
            with self.assertRaises(m.Error):m.Maintenance('Arlen',1,self.rpc,lambda d:None,bad)
    def test_wrong_character_never_mutates(self):
        self.rpc.world['self']['guid']='4'
        with self.assertRaises(m.Error):self.tick()
        self.assertNotIn('buddy-training.buy',self.methods())

if __name__=='__main__':unittest.main()
