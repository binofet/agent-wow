#!/usr/bin/env python3
import json
import threading
import unittest
from unittest.mock import patch
import gm_tasks as gm

class GMTaskInspectionTests(unittest.TestCase):
    def test_groupless_gm_does_not_hide_other_observed_party(self):
        configured=[dict(guid=87,name='Lotharen',rpc_url='http://127.0.0.1:8090/rpc'),dict(guid=1,name='Arlen',rpc_url='http://127.0.0.1:8086/rpc')]
        barrier=threading.Barrier(2);calls=[]
        class RPC:
            def __init__(self,url):self.guid='87' if ':8090/' in url else '1'
            def call(self,method,params,timeout=5):
                calls.append((self.guid,method));sid='aw-'+self.guid
                if method=='buddy-actions.status':
                    barrier.wait(timeout=2)
                    return dict(selfGuid=self.guid,sessionId=sid,position=dict(known=True,mapKnown=True,mapId=0,x=0,y=0,z=0))
                if method=='buddy-world.snapshot':return dict(self=dict(guid=self.guid,present=True,healthKnown=True,health=90,maxHealth=100),owner=dict(guid='3'),inventoryBags=['not relevant'])
                if method=='buddy-party.state':return dict(sessionId=sid,group=dict(known=True,inGroup=self.guid=='1',leaderGuid='1' if self.guid=='1' else '0',members=[dict(guid='1'),dict(guid='3')] if self.guid=='1' else []))
                raise AssertionError('inspection attempted a non-read RPC')
        with patch.object(gm,'bindings',return_value=configured):result=gm.inspect_clients(configured[0]['rpc_url'],rpc_factory=RPC)
        self.assertTrue(result['gm']['identityMatches']);self.assertFalse(result['gm']['observations']['party']['group']['inGroup'])
        arlen=result['bindings'][0];self.assertTrue(arlen['identityMatches']);self.assertEqual(arlen['observations']['party']['group']['members'],[dict(guid='1'),dict(guid='3')]);self.assertEqual(arlen['observations']['world']['self']['health'],90)
        self.assertNotIn('inventoryBags',arlen['observations']['world']);self.assertEqual(len(calls),6)

    def test_unavailable_binding_preserves_other_observations_and_reports_error(self):
        class RPC:
            def __init__(self,url):self.offline='8405' in url
            def call(self,method,params,timeout=5):
                if self.offline:raise TimeoutError('client unavailable')
                if method=='buddy-actions.status':return dict(selfGuid='87',sessionId='aw-gm')
                if method=='buddy-world.snapshot':return dict(self=dict(guid='87',present=True))
                if method=='buddy-party.state':return dict(sessionId='aw-gm',group=dict(known=True,inGroup=False))
                raise AssertionError(method)
        with patch.object(gm,'bindings',return_value=[dict(guid=11,name='Celdric',rpc_url='http://127.0.0.1:8405/rpc')]):result=gm.inspect_clients('http://127.0.0.1:8090/rpc',rpc_factory=RPC)
        self.assertTrue(result['gm']['identityMatches']);offline=result['bindings'][0];self.assertFalse(offline['identityMatches']);self.assertEqual(len(offline['errors']),3);self.assertEqual(offline['observations'],{})
        self.assertIn('inspect',gm.tools('/repo',87,'http://127.0.0.1:8090/rpc'))

if __name__=='__main__':unittest.main()
