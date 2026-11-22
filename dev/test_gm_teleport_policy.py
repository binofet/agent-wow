import os
import copy
from pathlib import Path
import unittest
import gm_tasks as gm

@unittest.skipUnless(os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY'), 'compiled typed bridge not requested')
class TeleportPolicyTests(unittest.TestCase):
    def setUp(self):
        self.bridge = gm.task_bridge(os.environ['BUDDY_LUAPOLICY_TEST_BINARY'], gm.ROOT / 'modules')
        self.source = (gm.ROOT / 'behaviors/gm-recovery.lua').read_text()
        names = [('1','Arlen'),('3','Joel'),('8','Nixwick'),('4','Mira'),('5','Rook')]
        self.members = [dict(guid=g,name=n,**({'rpcUrl':'http://127.0.0.1:8086/rpc'} if g!='3' else {})) for g,n in names]
        def player(g,n):
            return dict(guid=g,name=n,sessionId='aw-'+g,positionKnown=True,mapId=189,x=10,y=10,z=10,
                        visibleToGm=True,aliveKnown=True,alive=True,combatKnown=True,inCombat=False,
                        hp=100,maxHp=100,manaUser=False,observationRevision='1')
        rows=[player(g,n) for g,n in names]
        self.frame=dict(goal=dict(id='teleport-check',player=dict(guid='3',name='Joel'),members=self.members),
                        memory={},last=None,pending=[],now=1,
                        observations=dict(gm=player('87','Lotharen'),members=rows,
                            destinationPlayer=rows[1],threats=[],
                            group=dict(known=True,inGroup=True,leaderGuid='1',members=self.members)))
    def evaluate(self,frame=None):
        return self.bridge.evaluate(self.source,frame or self.frame)
    def test_observed_arrivals_complete_without_any_walking_route(self):
        out=self.evaluate()
        self.assertTrue(out['done'] and out['success'])
        self.assertNotIn('action',out)
    def test_failed_walk_planner_cannot_reject_observed_teleport_arrival(self):
        self.frame['observations']['destinationRoute']=dict(goalReached=False,error='physical leg endpoint bounds')
        for row in self.frame['observations']['members']:
            row['landingRoute']=dict(goalReached=False,error='no corridor between continents')
        self.assertTrue(self.evaluate()['success'])
    def test_transport_ack_does_not_substitute_for_observed_arrival(self):
        self.frame['observations']['gm']['mapId']=0
        first=self.evaluate()
        self.assertEqual(first['action']['command'],'.appear Joel')
        self.frame.update(memory=first['memory'],last=dict(id=first['action']['id'],outcome='written'),now=2)
        waiting=self.evaluate()
        self.assertFalse(waiting['done'])
        self.assertEqual(waiting['phase'],'observing_move')
        self.assertNotIn('action',waiting)
    def test_unknown_write_is_not_replayed(self):
        self.frame['observations']['gm']['mapId']=0
        first=self.evaluate()
        self.frame.update(memory=first['memory'],last=dict(id=first['action']['id'],outcome='unconfirmed'),now=20)
        out=self.evaluate()
        self.assertFalse(out['done']);self.assertNotIn('action',out)
        self.assertEqual(out['error'],'effect_not_observed:move')
    def test_actual_member_arrival_is_still_required(self):
        self.frame['observations']['members'][3]['x']=1000
        self.assertFalse(self.evaluate()['done'])
    def test_full_health_and_original_roster_remain_required(self):
        self.frame['observations']['members'][3]['hp']=1
        out=self.evaluate();self.assertFalse(out['done'])
        self.frame['observations']['group']['members']=self.members[:-1]
        out=self.evaluate();self.assertEqual(out['error'],'original_member_name_not_observed')

@unittest.skipUnless(os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY'), 'compiled typed bridge not requested')
class SelectionCustodyTests(unittest.TestCase):
    setUp=TeleportPolicyTests.setUp
    evaluate=TeleportPolicyTests.evaluate
    def selecting_nix(self):
        rows=self.frame['observations']['members']
        nix=next(p for p in rows if p['guid']=='8')
        arlen=next(p for p in rows if p['guid']=='1')
        nix['hp']=50
        arlen.update(manaUser=True,mana=0,maxMana=100)
        first=self.evaluate()
        self.assertEqual(first['action']['kind'],'select_target')
        self.assertEqual(first['action']['targetGuid'],'8')
        self.frame.update(memory=first['memory'],now=2)
        return first,nix,arlen

    def selection_result(self,action,outcome='written',known=True):
        self.frame['last']=dict(id=action['id'],action=copy.deepcopy(action),outcome=outcome,
            sessionId='aw-87',receipt=dict(transportWritten=outcome=='written'))
        self.frame['observations']['selection']=dict(sessionId='aw-87',targetGuid=action['targetGuid'],
            known=known,transportWritten=outcome=='written',observedTargetKnown=known,
            observedTargetGuid=action['targetGuid'] if known else '3')

    def test_health_naturally_fills_after_selection_then_mana_selects_its_own_target(self):
        first,nix,_=self.selecting_nix()
        self.selection_result(first['action']);nix['hp']=100
        out=self.evaluate()
        self.assertEqual(out['action']['kind'],'select_target')
        self.assertEqual(out['action']['targetGuid'],'1')
        self.assertNotEqual(out['action']['id'],first['action']['id'])
        self.assertEqual(out['memory']['stage'],'mana')
        self.assertEqual(out['memory']['selectionReconciled']['id'],first['action']['id'])
        self.assertEqual(out['memory']['selectionReconciled']['targetGuid'],'8')
        self.assertEqual(out['memory']['selectionWaiting']['targetGuid'],'1')
        self.assertEqual(out['memory']['selectionWaiting']['stage'],'mana')
        self.assertEqual(out['memory']['members'],first['memory']['members'])

    def test_actual_legacy_stalled_memory_binds_old_target_from_its_exact_durable_action(self):
        first,nix,_=self.selecting_nix();nix['hp']=100
        old=copy.deepcopy(first['action']);old['id']='teleport-check:20'
        self.frame['memory'].update(stage='mana',memberIndex=1,serial=20,
            selectionWaiting=dict(id=old['id'],at=1))
        self.selection_result(old)
        out=self.evaluate()
        self.assertEqual(out['action'],dict(kind='select_target',targetGuid='1',id='teleport-check:21'))
        self.assertEqual(out['memory']['selectionReconciled']['id'],'teleport-check:20')
        self.assertEqual(out['memory']['selectionReconciled']['reason'],'target_observed')

    def test_unknown_selection_is_not_replayed_or_skipped_when_health_fills(self):
        first,nix,_=self.selecting_nix();nix['hp']=100
        self.selection_result(first['action'],outcome='unconfirmed',known=False)
        self.frame['observations']['selection'].update(targetGuid='3',transportWritten=False)
        for now in (2,3,20,21):
            self.frame['now']=now
            out=self.evaluate();self.frame['memory']=out['memory']
            self.assertNotIn('action',out)
            self.assertEqual(out['memory']['selectionWaiting']['id'],first['action']['id'])
            self.assertEqual(out['memory']['stage'],'health')
        # Only actual target observation can settle an uncertain selection.
        self.frame['observations']['selection'].update(observedTargetKnown=True,observedTargetGuid='8')
        out=self.evaluate()
        self.assertEqual(out['action']['targetGuid'],'1')
        self.assertNotEqual(out['action']['id'],first['action']['id'])

    def test_matching_transport_receipt_and_bound_selection_do_not_claim_server_effect(self):
        first,nix,_=self.selecting_nix();nix['hp']=100
        self.selection_result(first['action'],known=False)
        out=self.evaluate()
        self.assertEqual(out['action']['targetGuid'],'1')
        self.assertEqual(out['memory']['selectionReconciled']['reason'],'selection_transport_written')
        self.assertFalse(out['done'])

    def test_foreign_receipt_cannot_retire_optimistic_selection_and_known_no_write_can(self):
        first,nix,_=self.selecting_nix();nix['hp']=100
        self.selection_result(first['action'],known=False)
        self.frame['last']['id']='another-task:20'
        out=self.evaluate()
        self.assertNotIn('action',out)
        self.assertEqual(out['phase'],'observing_selection_Nixwick')
        self.frame['memory']=out['memory']
        self.selection_result(first['action'],outcome='not_dispatched',known=False)
        out=self.evaluate()
        self.assertEqual(out['action']['targetGuid'],'1')
        self.assertEqual(out['memory']['selectionReconciled']['reason'],'selection_not_dispatched')

    def test_legacy_missing_action_binding_never_assumes_current_member_is_pending_target(self):
        first,nix,_=self.selecting_nix();nix['hp']=100
        self.frame['memory']['selectionWaiting'].pop('targetGuid',None)
        self.frame['last']=dict(id=first['action']['id'],outcome='unconfirmed')
        out=self.evaluate()
        self.assertEqual(out['error'],'selection_target_binding_unknown')
        self.assertNotIn('action',out)

    def test_unknown_resource_maximum_remains_blocked_after_old_selection_settles(self):
        first,nix,arlen=self.selecting_nix();nix['hp']=100
        self.selection_result(first['action']);arlen.pop('maxMana')
        out=self.evaluate()
        self.assertEqual(out['error'],'natural_resource_maximum_unknown:Arlen')
        self.assertNotIn('action',out)
        self.assertFalse(out['done'])

if __name__=='__main__':unittest.main()
