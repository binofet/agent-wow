import copy
from pathlib import Path
from types import SimpleNamespace
import unittest

from item_recipient import RecipientError, resolve


def person(guid, cls=5, level=40, **extra):
    return dict(guid=guid, present=True, classKnown=True, classId=cls, levelKnown=True, level=level, **extra)


def skills():
    rows=[dict(slot=i) for i in range(128)]
    rows[3].update(skillId=54,value=125,maxValue=200)
    return dict(selfSkillsKnown=True,selfSkillsRevision='7',selfSkills=rows)


class RPC:
    def __init__(self,guid='87',sid='aw-gm',world=None,url='http://gm/rpc'):
        self.guid,self.sid,self.url=guid,sid,url
        self.world=world or dict(self=person(guid,2,47))
        self.calls=[];self.hook=None
    def call(self,method,args):
        self.calls.append((method,copy.deepcopy(args)))
        if self.hook:self.hook(method)
        if method=='buddy-actions.status':return dict(sessionId=self.sid,selfGuid=self.guid)
        if method=='buddy-world.snapshot':return copy.deepcopy(self.world)
        raise AssertionError('Only two exact PUBLIC read routes are allowed')


class GM:
    def __init__(self,rows=None,clients=None):
        self.rows=rows or [];self.clients=clients or {};self.created=[];self.binding_calls=0;self.hook=None
    def bindings(self,root):
        self.binding_calls+=1
        if self.hook:self.hook(self.binding_calls)
        return copy.deepcopy(self.rows)
    def task_rpc(self,url):
        self.created.append(url)
        return self.clients[url]


class Tools:
    def __init__(self,rpc,gm=None,workspace=Path('/SYNTHETIC_WORKSPACE'),owner=True,allowed=True,gm_allowed=True):
        self.rpc,self.self_guid,self.session_id=rpc,rpc.guid,rpc.sid
        self.root=Path('/SYNTHETIC_ROOT');self.workspace=workspace
        self.gm=gm or GM();self.owner=owner;self.allowed_value=allowed;self.gm_value=gm_allowed;self.auth_hook=None
    def owner_allowed(self):return self.owner
    def _authorise_owner(self):
        if self.auth_hook:self.auth_hook()
        if not self.owner:raise ValueError('owner revoked')
    def _authorise(self):
        if self.auth_hook:self.auth_hook()
        if not self.allowed_value:raise ValueError('developer revoked')
    def _authorise_gm(self):
        if self.auth_hook:self.auth_hook()
        if not self.allowed_value or not self.gm_value:raise ValueError('GM revoked')
    def _helper(self,name):
        assert name=='gm_tasks'
        return self.gm


class Recipient(unittest.TestCase):
    def registered(self):
        target=RPC('4','aw-mira',dict(self=person('4',5,35,raceKnown=True,raceId=1),**skills()),'http://mira/rpc')
        gm=GM([dict(guid='4',name='Mira',rpc_url=target.url)],{target.url:target})
        caller=RPC();return Tools(caller,gm),target,gm
    def test_ordinary_self_only_complete_own_skills_and_proto_zero_mana(self):
        rpc=RPC('1','aw-self',dict(self=person('1',5,40,powerKnown=True),**skills()))
        t=Tools(rpc,workspace=None,allowed=False,gm_allowed=False);r=resolve(t,'1')
        self.assertEqual((r['guid'],r['classId'],r['level'],r['powerType']),('1',5,40,0))
        self.assertEqual(r['skills'],{54:125});self.assertTrue(r['provenance']['skillsKnown']);self.assertEqual(len(rpc.calls),3)
        with self.assertRaises(RecipientError):resolve(t,'3')
        self.assertEqual(len(rpc.calls),3);self.assertEqual(t.gm.created,[])
    def test_registered_target_direct_only_requested_client_and_both_bodies_fenced(self):
        t,target,gm=self.registered();r=resolve(t,'4')
        self.assertEqual((r['guid'],r['classId'],r['level'],r['raceId'],r['name']),('4',5,35,1,'Mira'))
        self.assertEqual(r['skills'],{54:125});self.assertEqual(gm.created,[target.url]);self.assertEqual(gm.binding_calls,2)
        self.assertEqual([m for m,_ in t.rpc.calls],['buddy-actions.status']*2)
        self.assertEqual([m for m,_ in target.calls],['buddy-actions.status','buddy-world.snapshot','buddy-actions.status'])
        self.assertEqual(r['provenance']['nameSource'],'configured_actor_identity')
    def test_observed_owner_and_nearby_remote_never_receive_gm_skills_or_session(self):
        for source in ('owner','nearbyPlayers'):
            world=dict(self=person('87',2,47),**skills());row=person('3',1,40,raceKnown=True,raceId=1,powerKnown=True,powerType=1)
            world[source]=row if source=='owner' else [row]
            t=Tools(RPC(world=world));r=resolve(t,'3')
            self.assertEqual((r['classId'],r['level']), (1,40));self.assertNotIn('skills',r);self.assertNotIn('name',r)
            self.assertFalse(r['provenance']['recipientSessionKnown']);self.assertNotIn('recipientSessionId',r['provenance'])
            self.assertEqual(r['provenance']['sources'],[source]);self.assertEqual(t.gm.created,[]);self.assertEqual(len(t.rpc.calls),3)
    def test_absent_unknown_and_contradictory_target_never_fall_back_to_gm(self):
        for world in (dict(self=person('87',2,47)),dict(self=person('87',2,47),owner=dict(guid='3',present=False)),dict(self=person('87',2,47),owner=person('3',1,40),nearbyPlayers=[person('3',5,40)]),dict(self=person('87',2,47),owner=person('3',1,40),nearbyPlayers=[person('3',1,41)])):
            with self.assertRaises(RecipientError):resolve(Tools(RPC(world=world)),'3')
    def test_local_absence_peer_visible_consistent_and_conflicting_witnesses(self):
        peers = [RPC('1','aw-arlen',dict(self=person('1'),owner=person('3',1,40)),'http://arlen/rpc'),
                 RPC('4','aw-mira',dict(self=person('4'),owner=person('3',1,40)),'http://mira/rpc')]
        rows=[dict(guid=p.guid,name='Peer'+p.guid,rpc_url=p.url) for p in peers]
        gm=GM(rows,{p.url:p for p in peers})
        t=Tools(RPC(world=dict(self=person('87'),owner=dict(guid='3',present=False))),gm)
        result=resolve(t,'3')
        self.assertEqual((result['classId'],result['level']),(1,40))
        self.assertEqual({w['observerGuid'] for w in result['provenance']['witnesses']},{'1','4'})
        self.assertNotIn('skills',result);self.assertFalse(result['provenance']['recipientSessionKnown'])
        for p in peers:self.assertEqual(len(p.calls),3)
        peers[1].world['owner']['level']=41
        with self.assertRaisesRegex(RecipientError,'observations_conflict'):resolve(t,'3')
        peers[1].world['owner']=dict(guid='3',present=False)
        self.assertEqual(resolve(t,'3')['level'],40)
        peers[0].hook=lambda method:setattr(peers[0],'sid','changed') if method=='buddy-world.snapshot' else None
        with self.assertRaisesRegex(RecipientError,'body_changed'):resolve(t,'3')

    def test_required_knownness_and_types_no_zero_or_bool_defaults(self):
        for patch in ({'classKnown':False},{'levelKnown':False},{'classId':0},{'classId':True},{'level':0},{'level':True}):
            row=person('1');row.update(patch)
            with self.assertRaises(RecipientError):resolve(Tools(RPC('1','aw-self',dict(self=row))),'1')
    def test_target_sid_guid_world_identity_and_caller_sid_changes_fail_closed(self):
        for kind in ('targetSID','targetGUID','worldSID','callerSID'):
            t,target,_=self.registered()
            if kind=='targetSID':
                target.hook=lambda method:setattr(target,'sid','aw-new') if method=='buddy-world.snapshot' else None
            elif kind=='targetGUID':target.world['self']['guid']='5'
            elif kind=='worldSID':target.world['sessionId']='aw-other'
            else:target.hook=lambda method:setattr(t.rpc,'sid','aw-new') if method=='buddy-world.snapshot' else None
            with self.assertRaises(RecipientError):resolve(t,'4')
    def test_incomplete_or_invalid_skills_are_unknown_not_empty_known(self):
        for kind in ('short','duplicateSlot','duplicateSkill','revision','rank'):
            t,target,_=self.registered()
            if kind=='short':target.world['selfSkills'].pop()
            elif kind=='duplicateSlot':target.world['selfSkills'][1]['slot']=0
            elif kind=='duplicateSkill':target.world['selfSkills'][4].update(skillId=54,value=1,maxValue=200)
            elif kind=='revision':target.world['selfSkillsRevision']='0'
            else:target.world['selfSkills'][3]['value']=999
            r=resolve(t,'4');self.assertNotIn('skills',r);self.assertFalse(r['provenance']['skillsKnown']);self.assertEqual(r['classId'],5)
        t,target,_=self.registered();target.world['selfSkills']=[dict(slot=i) for i in range(128)]
        self.assertEqual(resolve(t,'4')['skills'],{})
    def test_ambiguous_endpoint_and_rebinding_do_not_try_another_client(self):
        t,target,gm=self.registered();gm.rows.append(dict(guid='4',name='Mira',rpc_url='http://other/rpc'))
        with self.assertRaises(RecipientError):resolve(t,'4')
        self.assertEqual(gm.created,[])
        t,target,gm=self.registered();gm.hook=lambda call:gm.rows[0].update(rpc_url='http://new/rpc') if call==2 else None
        with self.assertRaises(RecipientError):resolve(t,'4')
        self.assertEqual(gm.created,[target.url])
    def test_remote_requires_existing_authorized_gm_workspace_and_revocation(self):
        for t in (Tools(RPC(),workspace=None),Tools(RPC(),gm_allowed=False),Tools(RPC(),allowed=False)):
            with self.assertRaises(RecipientError):resolve(t,'3')
            self.assertEqual(t.rpc.calls,[])
        t,target,_=self.registered();target.hook=lambda method:setattr(t,'gm_value',False) if method=='buddy-world.snapshot' else None
        with self.assertRaises(RecipientError):resolve(t,'4')
    def test_bad_requested_guid_has_zero_reads(self):
        for value in ('0','03','3.0',True,3,str(2**64)):
            t=Tools(RPC())
            with self.assertRaises(RecipientError):resolve(t,value)
            self.assertEqual(t.rpc.calls,[])

if __name__=='__main__':unittest.main()
