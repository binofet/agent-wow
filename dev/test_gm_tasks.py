#!/usr/bin/env python3
import contextlib
import io
import json
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

if __name__=='__main__':unittest.main()
