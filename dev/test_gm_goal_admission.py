"""Fresh recovery goals use the real Lua initializer before physical custody."""
import contextlib
import copy
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

import gm_tasks as gm
from direct_tools import DirectTools

ROOT = Path(__file__).resolve().parent.parent


class ReadOnlyClient:
    def __init__(self, guid, group):
        self.guid, self.sid, self.group = str(guid), 'aw-'+str(guid), copy.deepcopy(group)
        self.url = 'http://127.0.0.1:'+str(8000+int(guid))+'/rpc'
        self.calls, self.hook = [], None

    def call(self, method, params, **kwargs):
        self.calls.append((method, copy.deepcopy(params)))
        own = dict(guid=self.guid, present=True, aliveKnown=True, alive=True,
            healthKnown=True, health=100, maxHealth=100, powerKnown=True, powerType=3,
            power=0, maxPower=0, mapKnown=True, mapId=349, positionKnown=True,
            x=0, y=0, z=0, unitFlagsKnown=True, unitFlags=0)
        if method == 'session.describe':
            result = dict(sessionId=self.sid, characterGuid=self.guid, revision='catalog', methods=[], hostMethods=[], messages={})
        elif method == 'buddy-actions.status': result = dict(sessionId=self.sid, selfGuid=self.guid)
        elif method == 'buddy-world.snapshot': result = dict(self=own, owner=dict(guid='3', present=False), nearbyPlayers=[], nearbyCreatures=[])
        elif method == 'buddy-party.state': result = dict(sessionId=self.sid, group=copy.deepcopy(self.group))
        else: raise AssertionError('Admission attempted a non-observation RPC: '+method)
        if self.hook: self.hook(method)
        return result


class GMGoalAdmissionTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.binary = Path(os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY', ROOT/'data/bin/luapolicy'))
        self.assertTrue(self.binary.is_file(), 'Use the maintained compiled Lua evaluator for admission tests')
        group = dict(known=True, inGroup=True, leaderGuid='1', members=[
            dict(guid='1', name='Arlen', online=True), dict(guid='3', name='Joel', online=True)])
        self.gm = ReadOnlyClient('87', dict(known=True, inGroup=False))
        self.arlen = ReadOnlyClient('1', group)
        self.goal = dict(id='synthetic-admission', destination=dict(mapId=349,x=10,y=20,z=30),
            members=[dict(guid='1',name='Arlen',rpcUrl=self.arlen.url),dict(guid='3',name='Joel')])
        self.bridge = gm.task_bridge(self.binary, ROOT/'modules')
        self.policy, self.hold = ROOT/'behaviors/gm-recovery.lua', ROOT/'behaviors/gm-recovery-hold.lua'
        patcher=patch.object(gm,'task_rpc',side_effect=lambda url:self.gm if url==self.gm.url else self.arlen if url==self.arlen.url else self.fail('Unbound RPC'))
        patcher.start(); self.addCleanup(patcher.stop)

    def preview(self, goal):
        return gm.admit_goal(self.gm,goal,self.bridge,self.policy,self.hold,expected_session=self.gm.sid)

    def test_real_initializer_rejects_bad_goals_without_hold_or_transport(self):
        cases=[]
        missing=copy.deepcopy(self.goal);missing.pop('destination');cases.append((missing,'explicit_destination_required'))
        duplicate=copy.deepcopy(self.goal);duplicate['members'].append(duplicate['members'][0]);cases.append((duplicate,'invalid_member_identity'))
        wrong=copy.deepcopy(self.goal);wrong['members'][0]['name']='Wrong';cases.append((wrong,'original_member_name_not_observed'))
        tuning=copy.deepcopy(self.goal);tuning['effectTimeoutSeconds']=0;cases.append((tuning,'invalid_task_tuning'))
        wrong_map=copy.deepcopy(self.goal);wrong_map['destination']['mapId']='wrong';cases.append((wrong_map,'invalid_destination'))
        wrong_id=copy.deepcopy(self.goal);wrong_id['id']={};cases.append((wrong_id,'invalid_task_id'))
        wrong_factions=copy.deepcopy(self.goal);wrong_factions['hostileFactions']='wrong';cases.append((wrong_factions,'invalid_task_tuning'))
        for goal, reason in cases:
            with self.subTest(reason=reason), patch.object(gm.Host,'install_holds',side_effect=AssertionError('No holds')), \
                    patch.object(gm,'claim_writer',side_effect=AssertionError('No writer')):
                original=copy.deepcopy(goal); result=self.preview(goal)
                self.assertFalse(result['accepted']);self.assertEqual(result['error'],reason);self.assertEqual(goal,original)
        self.assertTrue(all(m in ('buddy-actions.status','buddy-world.snapshot','buddy-party.state') for m,_ in self.gm.calls+self.arlen.calls))

    def test_container_errors_are_useful_before_constructor_or_reads(self):
        for goal in (dict(members='wrong'),dict(members=[False]),dict(members=[],player='Joel')):
            with self.subTest(goal=goal),patch.object(gm,'Host') as host:
                with self.assertRaisesRegex(ValueError,'goal_'):self.preview(goal)
                host.assert_not_called()
        self.assertFalse(self.gm.calls or self.arlen.calls)

    def test_valid_goal_is_admitted_without_advancing_memory_or_proposing_action(self):
        original=copy.deepcopy(self.goal);result=self.preview(self.goal)
        self.assertEqual(result,dict(accepted=True));self.assertEqual(self.goal,original)
        self.assertNotIn('action',result);self.assertNotIn('memory',result)
        with gm.task_store(self.root/'task') as store:
            host=gm.Host(store,self.gm,Mock(),self.bridge,self.goal,self.policy,self.hold)
            host.data['identity']=dict(sessionId=self.gm.sid,guid='87',rpcUrl=self.gm.url)
            host.install_holds=Mock();host.step=Mock(return_value=dict(error='synthetic_wait',phase='waiting',done=False))
            host.run();host.install_holds.assert_called_once();host.step.assert_called_once()

    def tools(self, allowed=lambda:True, runner=None):
        for directory in ('behaviors','modules','data/converse/lotharen','workspace'):(self.root/directory).mkdir(parents=True,exist_ok=True)
        for source in (self.policy,self.hold):(self.root/'behaviors'/source.name).write_bytes(source.read_bytes())
        # Bridge's registry is the maintained public source, not a fake schema.
        if not (self.root/'modules').is_symlink():
            (self.root/'modules').rmdir();(self.root/'modules').symlink_to(ROOT/'modules',target_is_directory=True)
        (self.root/'data/converse/lotharen/gm-transport.json').write_text(json.dumps(dict(writerRoot=str(self.root/'writers'))))
        return DirectTools(self.gm,self.gm.sid,'87',self.root/'workspace',self.root,
            allowed=allowed,gm_allowed=allowed,request_id='synthetic-owner',runner=runner or Mock(),
            helpers=dict(gm_tasks=gm,policy_binary=str(self.binary),gm_bindings=lambda:[dict(guid='1',rpc_url=self.arlen.url)]))

    def test_bad_tool_goal_has_no_folder_child_or_writer_and_can_be_corrected(self):
        runner=Mock(return_value=type('Result',(),dict(returncode=0,stdout=b'{"started":true}'))())
        tools=self.tools(runner=runner);bad=copy.deepcopy(self.goal);bad.pop('destination')
        result=tools.dispatch('gm_task_start',dict(goal=bad))
        self.assertFalse(result['ok']);self.assertEqual(result['error']['outcome'],'not_dispatched')
        self.assertIn('explicit_destination_required',result['error']['error'])
        self.assertFalse((self.root/'data/converse/lotharen/gm-tasks').exists());runner.assert_not_called()
        self.assertIsNone(gm.task_store(self.root/'writers'/self.gm.sid).load())
        good=tools.dispatch('gm_task_start',dict(goal=self.goal));self.assertTrue(good['ok'],good);runner.assert_called_once()

    def test_owner_or_body_changes_during_preflight_are_zero_child_and_goal_reservation(self):
        for change in ('owner','body'):
            with self.subTest(change=change):
                active=[True];runner=Mock();self.gm.sid='aw-87'
                tools=self.tools(allowed=lambda:active[0],runner=runner)
                def changed(method):
                    if method=='buddy-party.state':
                        if change=='owner':active[0]=False
                        else:self.gm.sid='aw-88'
                self.arlen.hook=changed
                result=tools.dispatch('gm_task_start',dict(goal=self.goal))
                self.assertFalse(result['ok']);runner.assert_not_called()
                self.assertFalse((self.root/'data/converse/lotharen/gm-tasks').exists())
                self.arlen.hook=None

    def main_args(self, goal=None):
        (self.root/'goal.json').write_text(json.dumps(goal or self.goal));(self.root/'transport.json').write_text(json.dumps(dict(writerRoot=str(self.root/'writers'))))
        return ['run','--state-dir',str(self.root/'task'),'--goal',str(self.root/'goal.json'),
            '--transport',str(self.root/'transport.json'),'--rpc-url',self.gm.url,'--policy',str(self.policy),'--policy-binary',str(self.binary)]

    def test_main_rejects_before_writer_and_holds(self):
        bad=copy.deepcopy(self.goal);bad.pop('destination')
        with patch.object(gm,'ROOT',ROOT),patch.object(gm,'claim_writer') as claim, \
                patch.object(gm.Host,'install_holds') as holds,contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(gm.main(self.main_args(bad)),1)
        claim.assert_not_called();holds.assert_not_called()
        self.assertEqual(json.loads(output.getvalue())['outcome'],'not_dispatched')

    def test_second_fresh_admission_rejection_releases_only_its_empty_writer(self):
        reads=[0]
        def change_group(method):
            if method=='buddy-party.state':
                reads[0]+=1
                if reads[0]==1:self.arlen.group['leaderGuid']='999'
        self.arlen.hook=change_group
        with patch.object(gm.Host,'install_holds') as holds,contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(gm.main(self.main_args()),1)
        holds.assert_not_called();self.assertGreaterEqual(reads[0],2)
        state=gm.task_store(self.root/'task').load();writer=gm.task_store(self.root/'writers'/'aw-87').load()
        self.assertEqual(state['phase'],'not_admitted');self.assertFalse(writer['active'])
        self.assertEqual(writer['stateDir'],str(self.root/'task'))
        self.assertEqual((state['memory'],state['holds'],state['reservations'],state['last']),({},{},{},None))

    def test_body_replacement_at_first_hold_status_is_zero_hold_and_releases_empty_writer(self):
        original=gm.Host.admission;calls=[0]
        def admission(host,**kwargs):
            result=original(host,**kwargs);calls[0]+=1
            if calls[0]==2 and result and result['accepted']:self.gm.sid='aw-88'
            return result
        with patch.object(gm.Host,'admission',admission),contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(gm.main(self.main_args()),1)
        result=json.loads(output.getvalue());self.assertEqual(result['error'],'gm_session_changed_before_holds')
        self.assertEqual(result['outcome'],'not_dispatched')
        state=gm.task_store(self.root/'task').load();writer=gm.task_store(self.root/'writers'/'aw-87').load()
        self.assertEqual(state['holds'],{});self.assertFalse(writer['active'])
        self.assertTrue(all(method in ('buddy-actions.status','buddy-world.snapshot','buddy-party.state') for method,_ in self.gm.calls+self.arlen.calls))

    def test_foreign_writer_claim_is_never_cleared_by_goal_admission(self):
        with gm.task_store(self.root/'writers'/'aw-87') as store:gm.claim_writer(store,self.root/'foreign')
        before=gm.task_store(self.root/'writers'/'aw-87').load()
        with patch.object(gm.Host,'install_holds') as holds:
            with self.assertRaisesRegex(ValueError,'already owns this body'):gm.main(self.main_args())
        holds.assert_not_called();self.assertEqual(gm.task_store(self.root/'writers'/'aw-87').load(),before)

    def test_existing_custody_and_cancel_bypass_new_initialization_without_rewrite(self):
        for field,value in (('memory',dict(waiting='owned')),('last',dict(outcome='unknown')),
                ('reservations',dict(one=dict(outcome='reserved'))),('holds',dict(one=dict(restored=False)))):
            with self.subTest(field=field),gm.task_store(self.root/field) as store:
                host=gm.Host(store,self.gm,Mock(),self.bridge,self.goal,self.policy,self.hold);host.data[field]=value
                before=copy.deepcopy(host.data)
                with patch.object(host,'observe',side_effect=AssertionError('No new initialization')):
                    self.assertIsNone(host.admission())
                self.assertEqual(host.data,before)
        with gm.task_store(self.root/'cancel') as store:
            host=gm.Host(store,self.gm,Mock(),self.bridge,self.goal,self.policy,self.hold);host.stopping=True
            with patch.object(host,'observe',side_effect=AssertionError('No admission during cancellation')):self.assertIsNone(host.admission())
