#!/usr/bin/env python3
import contextlib
import io
import json
from pathlib import Path
import tempfile
import types
import unittest
from unittest.mock import patch
import gm_tasks as gm

class GMTaskLifecycleTests(unittest.TestCase):
 def fixture(self,root):
  folder=root/'data/converse/lotharen/gm-tasks/a/runtime'
  policy=root/'behaviors/gm-recovery.lua';policy.parent.mkdir();policy.write_text('return {}')
  invocation=dict(rpcUrl='http://127.0.0.1:8090/rpc',goal=str(root/'goal.json'),transport=str(root/'transport.json'),policy=str(policy),policyBinary=str(root/'lua'))
  with gm.runtime.Store(folder) as store:
   store.save(dict(version=1,identity=dict(guid='87',sessionId='aw-123',rpcUrl=invocation['rpcUrl']),goal={'id':'a'},phase='blocked_mana_Arlen',error='effect_not_observed',resumable=True,holds={'1':{'restored':False}},memory={},last={'outcome':'written'}))
   store.write('invocation.json',json.dumps(invocation).encode())
  with gm.runtime.Store(root/'data/gm-task-writers/aw-123') as writer:gm.claim_writer(writer,folder)
  return folder,policy
 def test_cancel_exited_resumable_task_starts_same_cleanup_runner(self):
  with tempfile.TemporaryDirectory() as temp:
   root=Path(temp).resolve();folder,_=self.fixture(root)
   with patch.object(gm.subprocess,'Popen',return_value=types.SimpleNamespace(pid=54321)) as launch:
    result=gm.request_cancel(folder)
   self.assertEqual(result['cleanup'],'cleanup_runner_started');self.assertEqual(result['completion'],'not_yet_observed')
   argv=launch.call_args.args[0]
   self.assertEqual(argv[3],'run');self.assertEqual(argv[argv.index('--state-dir')+1],str(folder))
   self.assertEqual(argv[argv.index('--rpc-url')+1],'http://127.0.0.1:8090/rpc')
   self.assertTrue((folder/'cancel.json').exists());self.assertFalse(gm.runtime.Store(folder).load().get('done'))
 def test_cancel_running_task_uses_existing_runner_without_duplicate_spawn(self):
  with tempfile.TemporaryDirectory() as temp:
   root=Path(temp).resolve();folder,_=self.fixture(root)
   with gm.runtime.Store(folder),patch.object(gm.subprocess,'Popen') as launch:
    result=gm.request_cancel(folder)
   self.assertEqual(result['cleanup'],'running_task_will_observe');launch.assert_not_called()
 def test_fault_is_body_bound_stopped_and_revision_sensitive_without_actions(self):
  with tempfile.TemporaryDirectory() as temp:
   root=Path(temp).resolve();folder,policy=self.fixture(root)
   read=lambda sid='aw-123':gm.task_faults(root,87,sid,'http://127.0.0.1:8090/rpc')
   first=read()[0];self.assertEqual(first['kind'],'gm_task');self.assertEqual(first['sourcePath'],str(policy));self.assertEqual(first['taskStateDir'],str(folder))
   self.assertEqual(read()[0]['key'],first['key']);self.assertEqual(read('aw-456'),[])
   self.assertEqual(gm.task_faults(root,1,'aw-123','http://127.0.0.1:8090/rpc'),[])
   with gm.runtime.Store(folder):self.assertEqual(read(),[])
   policy.write_text('return {changed=true}')
   self.assertNotEqual(read()[0]['key'],first['key'])
 def test_resume_reuses_stored_goal_policy_and_does_not_clear_unknown_reservations(self):
  with tempfile.TemporaryDirectory() as temp:
   root=Path(temp).resolve();folder,_=self.fixture(root);store=gm.runtime.Store(folder)
   data=store.load();data['reservations']={'a:1':{'outcome':'unconfirmed'}};store.save(data)
   before=(folder/'state.json').read_bytes()
   with patch.object(gm.subprocess,'Popen',return_value=types.SimpleNamespace(pid=54321)) as launch:
    self.assertTrue(gm.request_resume(folder)['started'])
   self.assertEqual((folder/'state.json').read_bytes(),before)
   argv=launch.call_args.args[0];self.assertEqual(argv[argv.index('--policy')+1],str(root/'behaviors/gm-recovery.lua'))
 def test_missing_policy_is_reported_as_fault_evidence_not_silence(self):
  with tempfile.TemporaryDirectory() as temp:
   root=Path(temp).resolve();folder,policy=self.fixture(root);policy.unlink()
   fault=gm.task_faults(root,87,'aw-123','http://127.0.0.1:8090/rpc')[0]
   self.assertTrue(fault['evidence']['sourceError']);self.assertEqual(fault['sourcePath'],str(policy))
 def test_replaced_body_cannot_claim_writer_for_old_recovery(self):
  with tempfile.TemporaryDirectory() as temp:
   root=Path(temp).resolve();folder,_=self.fixture(root)
   (root/'goal.json').write_text('{"id":"a"}');(root/'transport.json').write_text('{}')
   rpc=types.SimpleNamespace(call=lambda *a,**k:{'sessionId':'aw-456','selfGuid':'87'})
   with patch.object(gm.runtime,'RPC',return_value=rpc),patch.object(gm,'claim_writer') as claim:
    with self.assertRaisesRegex(ValueError,'another GM body'):
     gm.main(['run','--state-dir',str(folder),'--goal',str(root/'goal.json'),'--transport',str(root/'transport.json'),'--rpc-url','http://127.0.0.1:8090/rpc'])
   claim.assert_not_called()
 def test_legacy_process_metadata_is_interpreted_not_executed_as_argv(self):
  with tempfile.TemporaryDirectory() as temp:
   root=Path(temp).resolve();folder,_=self.fixture(root);store=gm.runtime.Store(folder)
   invocation=gm.load_invocation(store);(folder/'invocation.json').unlink()
   argv=['python3','-B',str(gm.ROOT/'dev/gm-task'),'run','--state-dir',str(folder)]
   for key,flag in [('rpcUrl','--rpc-url'),('goal','--goal'),('transport','--transport'),('policy','--policy'),('policyBinary','--policy-binary')]:argv.extend([flag,invocation[key]])
   store.write('process.json',json.dumps({'argv':argv}).encode())
   self.assertEqual(gm.load_invocation(store),invocation)
   argv[2]='/tmp/arbitrary.py';store.write('process.json',json.dumps({'argv':argv}).encode())
   with self.assertRaisesRegex(ValueError,'maintained task'):gm.load_invocation(store)

if __name__=='__main__':unittest.main()
