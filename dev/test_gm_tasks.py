#!/usr/bin/env python3
import contextlib
import io
import json
import os
from pathlib import Path
import struct
import tempfile
import types
import unittest
from unittest.mock import Mock,patch
import gm_tasks as gm

class Store:
 def __init__(self,path):self.folder=Path(path);self.saved=None
 def load(self):return self.saved
 def save(self,value):self.saved=json.loads(json.dumps(value))

class TestGMTaskTransport(unittest.TestCase):
 def rpc(self):
  return types.SimpleNamespace(call=lambda method,params: {'sessionId':'aw-123','selfGuid':'87'} if method=='buddy-actions.status' else {'sessionId':'aw-123'})
 def test_exact_game_command_uses_current_client_and_no_console(self):
  t=gm.SessionTransport(self.rpc(),{'prefix':['ssh','client-host']})
  with patch.object(gm.subprocess,'run',return_value=types.SimpleNamespace(returncode=0,stdout=b'grpc-status: 0\r\n')) as run:
   r=t.gm('.appear Joel','aw-123')
  argv=run.call_args.args[0];wire=run.call_args.kwargs['input']
  self.assertEqual(argv[:2],['ssh','client-host']);self.assertIn('/tmp/aw-123/0/session.sock',argv[-1]);self.assertNotIn('console',argv[-1]);self.assertIn(b'.appear Joel\0',wire);self.assertTrue(r['transportWritten'])
 def test_stale_body_does_not_write(self):
  t=gm.SessionTransport(self.rpc(),{})
  with patch.object(gm.subprocess,'run') as run:
   with self.assertRaises(ValueError):t.gm('.revive Joel','aw-other')
  run.assert_not_called()
 def test_failed_native_write_is_unconfirmed_not_replayed(self):
  t=gm.SessionTransport(self.rpc(),{})
  with patch.object(gm.subprocess,'run',return_value=types.SimpleNamespace(returncode=1,stdout=b'')) as run:
   with self.assertRaises(RuntimeError):t.gm('.revive Joel','aw-123')
  self.assertEqual(run.call_count,1)

class TestDirectGMCommands(unittest.TestCase):
 def rpc(self):
  return types.SimpleNamespace(call=lambda method,params: {'sessionId':'aw-123','selfGuid':'87'})
 def test_selected_command_holds_one_body_writer(self):
  with tempfile.TemporaryDirectory() as folder:
   cfg={'writerRoot':str(Path(folder).resolve())};rpc=self.rpc();events=[]
   class Transport:
    def __init__(self,*args):pass
    def select(self,guid,sid):
     events.append(('select',guid,sid))
     with self_test.assertRaises(gm.runtime.Error):
      gm.execute_command(rpc,cfg,'.appear Joel',expected_session=sid)
    def gm(self,command,sid):events.append(('command',command,sid))
   self_test=self
   with patch.object(gm,'SessionTransport',Transport):
    result=gm.execute_command(rpc,cfg,'.damage 4000',select_guid='17379391034147995791',expected_session='aw-123')
   self.assertEqual(events,[('select','17379391034147995791','aw-123'),('command','.damage 4000','aw-123')])
   self.assertEqual(result['completion'],'effect_not_yet_verified')
 def test_stale_expected_body_writes_nothing(self):
  with tempfile.TemporaryDirectory() as folder,patch.object(gm,'SessionTransport') as transport:
   with self.assertRaisesRegex(ValueError,'body changed'):
    gm.execute_command(self.rpc(),{'writerRoot':str(Path(folder).resolve())},'.modify hp 2349',select_guid='3',expected_session='aw-old')
   transport.assert_not_called()
 def test_body_replacement_after_selection_does_not_send_command(self):
  calls=0
  def read(method,params):
   nonlocal calls
   if method=='buddy-actions.status':calls+=1
   return {'sessionId':'aw-new' if calls>=3 else 'aw-123','selfGuid':'87'}
  with tempfile.TemporaryDirectory() as folder,patch.object(gm.subprocess,'run',return_value=types.SimpleNamespace(returncode=0,stdout=b'grpc-status: 0\r\n')) as run:
   with self.assertRaisesRegex(ValueError,'body changed'):
    gm.execute_command(types.SimpleNamespace(call=read),{'writerRoot':str(Path(folder).resolve())},'.damage 4000',select_guid='99',expected_session='aw-123')
   self.assertEqual(run.call_count,1)
 def test_existing_ordered_task_keeps_its_writer(self):
  with tempfile.TemporaryDirectory() as folder,patch.object(gm,'SessionTransport') as transport:
   cfg={'writerRoot':str(Path(folder).resolve())}
   with gm.runtime.Store(gm.writer_folder(Path(folder),'aw-123',cfg).absolute()) as owner:
    gm.claim_writer(owner,Path(folder)/'ongoing-task')
   with self.assertRaisesRegex(ValueError,'already owns'):
    gm.execute_command(self.rpc(),cfg,'.damage 4000',select_guid='99',expected_session='aw-123')
   transport.assert_not_called()
 def test_any_native_command_is_available_without_allowlist(self):
  with tempfile.TemporaryDirectory() as folder,patch.object(gm,'SessionTransport') as transport:
   gm.execute_command(self.rpc(),{'writerRoot':str(Path(folder).resolve())},'.help damage',expected_session='aw-123')
   transport.return_value.gm.assert_called_once_with('.help damage','aw-123')
 def test_cli_exposes_selection_and_command_without_task_goal(self):
  with tempfile.TemporaryDirectory() as folder:
   path=Path(folder)/'transport.json';path.write_text('{}')
   with patch.object(gm,'execute_command',return_value={'completion':'effect_not_yet_verified'}) as execute,patch.object(gm.runtime,'RPC'),contextlib.redirect_stdout(io.StringIO()) as out:
    self.assertEqual(gm.main(['execute','--rpc-url','http://127.0.0.1:8090/rpc','--transport',str(path),'--session-id','aw-123','--select-guid','3','--gm-command','.modify hp 2349']),0)
   self.assertEqual(execute.call_args.kwargs,{'select_guid':'3','expected_session':'aw-123'})
   self.assertEqual(json.loads(out.getvalue())['completion'],'effect_not_yet_verified')

class TestGMTaskHost(unittest.TestCase):
 def host(self,path,transport=None,bridge=None):
  return gm.Host(Store(path),Mock(),transport or Mock(),bridge or Mock(),{'id':'test','members':[]},Path(path)/'policy.lua',Path(path)/'hold.lua')
 def test_reservation_persisted_before_write_and_not_replayed(self):
  with tempfile.TemporaryDirectory() as folder:
   h=self.host(folder);action={'id':'test:1','kind':'gm_command','command':'.revive Joel'}
   def write(*args):
    self.assertEqual(h.store.saved['reservations']['test:1']['outcome'],'reserved')
    raise TimeoutError('unknown ACK')
   h.transport.gm.side_effect=write
   h.dispatch(action,{'gm':{'sessionId':'aw-123'}});h.dispatch(action,{'gm':{'sessionId':'aw-123'}})
   self.assertEqual(h.transport.gm.call_count,1);self.assertEqual(h.data['last']['outcome'],'unconfirmed')
 def test_blocked_phase_keeps_hold_for_observed_resume(self):
  with tempfile.TemporaryDirectory() as folder:
   h=self.host(folder);h.install_holds=Mock();h.restore_holds=Mock();h.step=Mock(return_value={'memory':{},'phase':'blocked_arrival','error':'effect_not_observed','done':False})
   r=h.run();self.assertEqual(r['phase'],'blocked_arrival');h.restore_holds.assert_not_called();self.assertTrue(h.data['resumable'])
 def test_cancelled_cleanup_restores_and_returns_without_loop(self):
  with tempfile.TemporaryDirectory() as folder:
   h=self.host(folder);h.install_holds=Mock();h.restore_holds=Mock(return_value=[]);h.step=Mock(return_value={'cancelled':True,'cleanupComplete':True,'done':False})
   h.run();h.restore_holds.assert_called_once();self.assertEqual(h.step.call_count,1)
 def test_failed_restore_prevents_success_claim(self):
  with tempfile.TemporaryDirectory() as folder:
   h=self.host(folder);h.install_holds=Mock();h.restore_holds=Mock(return_value=['changed policy']);h.step=Mock(return_value={'done':True,'success':True})
   result=h.run();self.assertFalse(result['success']);self.assertFalse(h.data['done'])
 def test_body_writer_exposes_existing_task_instead_of_interleaving(self):
  with tempfile.TemporaryDirectory() as folder:
   store=Store(folder);gm.claim_writer(store,Path(folder)/'task-a')
   self.assertEqual(gm.claim_writer(store,Path(folder)/'task-a')['stateDir'],str(Path(folder)/'task-a'))
   with self.assertRaisesRegex(ValueError,'task-a'):gm.claim_writer(store,Path(folder)/'task-b')
   with self.assertRaisesRegex(ValueError,'task-a'):gm.claim_writer(store)
 def test_detached_start_releases_task_lock_before_child_runs(self):
  with tempfile.TemporaryDirectory() as folder:
   state=Path(folder).resolve()/'state';goal=Path(folder)/'goal.json';goal.write_text('{}');transport=Path(folder)/'transport.json';transport.write_text('{}')
   def spawn(*args,**kwargs):
    with gm.runtime.Store(state.absolute()):pass
    return types.SimpleNamespace(pid=123456789)
   with patch.object(gm.subprocess,'Popen',side_effect=spawn) as popen,contextlib.redirect_stdout(io.StringIO()) as output:
    self.assertEqual(gm.main(['start','--state-dir',str(state),'--goal',str(goal),'--transport',str(transport),'--rpc-url','http://127.0.0.1:8090/rpc']),0)
   self.assertEqual(popen.call_count,1);self.assertEqual(json.loads(output.getvalue())['completion'],'not_yet_observed')
 def test_tools_are_direct_editable_and_only_exposed_to_gm(self):
  self.assertIsNone(gm.tools('/repo',1,'http://127.0.0.1:8086/rpc'))
  tools=gm.tools('/repo',87,'http://127.0.0.1:8090/rpc')
  self.assertIn('start',tools);self.assertIn('status',tools);self.assertIn('cancel',tools);self.assertIn('behaviors/gm-recovery.lua',tools['policy']);self.assertIn('broad rank-2',tools['instructions'])
  self.assertIn('execute',tools);self.assertIn('select',tools)
  self.assertIn('--select-guid',tools['execute']);self.assertIn('--session-id',tools['execute'])

class TestGMTaskCapacity(unittest.TestCase):
 def test_hold_reload_uses_explicit_rpc_budget_before_any_real_write(self):
  params=dict(sessionId='aw-capacity',characterGuid='1',expectedRevision='old',source='x'*185000)
  old=gm.runtime.RPC('http://127.0.0.1:8090/rpc')
  with patch.object(old.opener,'open') as transport:
   with self.assertRaisesRegex(gm.runtime.Error,'json_size_bound'):old.call('session.reload_policy',params)
  transport.assert_not_called()
  rpc=gm.task_rpc('http://127.0.0.1:8090/rpc')
  reply=io.BytesIO(json.dumps(dict(jsonrpc='2.0',id=1,result=dict(revision='next'))).encode())
  with patch.object(rpc.opener,'open',return_value=reply) as transport:
   self.assertEqual(rpc.call('session.reload_policy',params),dict(revision='next'))
  sent=json.loads(transport.call_args.args[0].data)
  self.assertEqual(sent['params'],params);self.assertGreater(len(transport.call_args.args[0].data),32768)
  self.assertEqual(transport.call_count,1)
 def test_large_source_response_and_overflow_are_bounded_without_retry(self):
  rpc=gm.task_rpc('http://127.0.0.1:8090/rpc');source='x'*(1100*1024)
  reply=io.BytesIO(json.dumps(dict(jsonrpc='2.0',id=1,result=dict(source=source))).encode())
  with patch.object(rpc.opener,'open',return_value=reply):self.assertEqual(rpc.call('session.policy_source',{})['source'],source)
  bounded=gm.runtime.RPC('http://127.0.0.1:8090/rpc',response_bytes=64)
  with patch.object(bounded.opener,'open',return_value=io.BytesIO(b'x'*65)) as transport:
   with self.assertRaisesRegex(gm.runtime.Error,'rpc_response_size_bound'):bounded.call('buddy-world.snapshot',{})
  self.assertEqual(transport.call_count,1)
 def test_four_saved_policies_retain_private_reservations_with_explicit_state_budget(self):
  with tempfile.TemporaryDirectory() as folder:
   path=Path(folder).resolve()/'state';store=gm.task_store(path)
   self.assertEqual(store.max_bytes,gm.STATE_BYTES)
   saved=dict(version=1,holds={str(g):dict(source='x'*185000,revision='original')for g in range(1,5)},
    reservations={'task:1':dict(outcome='unconfirmed',sessionId='aw-original')},memory=dict(stage='waiting'))
   with store:store.save(saved)
   loaded=gm.task_store(path).load()
   self.assertEqual(loaded['holds'],saved['holds']);self.assertEqual(loaded['reservations'],saved['reservations'])
   bridge=gm.task_bridge('/unused','/unused')
   self.assertEqual((bridge.source_bytes,bridge.value_bytes,bridge.execution_ms),(gm.SOURCE_BYTES,gm.VALUE_BYTES,gm.EXECUTION_MS))
 @unittest.skipUnless(os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY'),'compiled typed bridge not requested')
 def test_rich_world_frame_passes_actual_configured_evaluator(self):
  frame=dict(goal=dict(id='capacity'),memory=dict(stage='waiting',requestId='keep-existing'),last=None,pending=[],now=1,
   observations=dict(members=[dict(guid=str(g))for g in range(1,5)],threats=[dict(guid=str(0xf130000001000001+i),
    present=True,aliveKnown=True,alive=True,healthKnown=True,health=100,maxHealth=100,mapKnown=True,mapId=189,
    positionKnown=True,x=1,y=2,z=3,factionTemplate=67,engaged=False)for i in range(256)]))
  self.assertGreater(len(gm.runtime.packed(frame,gm.VALUE_BYTES)),32768)
  bridge=gm.task_bridge(os.environ['BUDDY_LUAPOLICY_TEST_BINARY'],gm.ROOT/'modules')
  result=bridge.evaluate('return {decide=function(f)return {memory=f.memory,count=#f.observations.threats}end}',frame)
  self.assertEqual(result,dict(memory=frame['memory'],count=256))

if __name__=='__main__':unittest.main()
