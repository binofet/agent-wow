import os
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

if __name__=='__main__':unittest.main()
