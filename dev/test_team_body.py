import copy, importlib.machinery, importlib.util, json, os
from pathlib import Path
import tempfile, types, unittest, uuid
from unittest.mock import patch
loader=importlib.machinery.SourceFileLoader('team_body',str(Path(__file__).with_name('team-body')))
spec=importlib.util.spec_from_loader(loader.name,loader);m=importlib.util.module_from_spec(spec);loader.exec_module(m)

class Buddy:
    def __init__(self):self.alive=True;self.pid=10;self.stops=0;self.starts=0;self.fail_stop=False;self.fail_start=False;self.BINARY=Path('/fake/core')
    def process_info(self,pid):return {'owned':True} if self.alive and pid==self.pid else None
    def stop(self):
        self.stops+=1
        if self.fail_stop:raise OSError('ambiguous stop')
        self.alive=False;return 0
    def start(self):
        self.starts+=1;self.alive=True;self.pid=11
        if self.fail_start:raise OSError('ambiguous start')
        return 0
    @staticmethod
    def requested_independent():return False
    @staticmethod
    def logout_line(line):return line==b'logout\n'

class Body(m.Body):
    def __init__(self,folder):
        self.b=Buddy();self.folder=folder;self.guid=1;self.port=8086
    def status(self):
        return dict(guid=1,state='trackedReady',ready=True,online=True,pid=self.b.pid,
            fingerprint=('a' if self.b.pid==10 else 'b')*64,sessionId='aw-old' if self.b.pid==10 else 'aw-new',logOffset=100)
    def record(self):return ({'pid':10,'private':'never output'},'sha')
    def scan(self,*args):return True,b'logout\n',None

class LifecycleTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup)
        self.body=Body(Path(self.temp.name)/'1');self.op=str(uuid.uuid4())
        self.patches=[patch.object(m,'port_closed',return_value=True),patch.object(m,'containers_gone',return_value=True),patch.object(m,'digest',return_value=m.CORE)]
        for p in self.patches:p.start();self.addCleanup(p.stop)
    def stop(self):return self.body.mutate('stop',dict(operationId=self.op,expectedFingerprint='a'*64))
    def start(self):return self.body.mutate('start',dict(operationId=self.op,stopReceiptRef=self.op))
    def test_positive_normal_stop_and_same_uuid_new_ready_start(self):
        result=self.stop();self.assertEqual(result['guid'],1);self.assertEqual(result['outcome'],'stopped')
        self.assertTrue(all(result[k] for k in ('normalLogoutObserved','oldPidGone','rpcClosed','sessionContainersGone')))
        self.assertNotIn('private',json.dumps(result));self.assertEqual((self.body.folder/(self.op+'.logout-tail')).read_bytes(),b'logout\n')
        result=self.start();self.assertEqual(result['sessionId'],'aw-new');self.assertEqual(result['outcome'],'started')
        self.assertEqual((self.body.b.stops,self.body.b.starts),(1,1))
        self.start();self.assertEqual(self.body.b.starts,1)
    def test_completed_start_replay_never_claims_a_later_replacement_body(self):
        self.stop();self.start()
        prior=self.body.status
        self.body.status=lambda:dict(prior(),pid=99,sessionId='aw-other',fingerprint='c'*64)
        with self.assertRaises(m.Refusal):self.start()
        self.assertEqual(self.body.b.starts,1)
    def test_duplicate_stop_is_receipt_only_and_conflicting_binding_is_refused(self):
        self.stop();self.stop();self.assertEqual(self.body.b.stops,1)
        with self.assertRaises(m.Refusal):self.body.mutate('stop',dict(operationId=self.op,expectedFingerprint='b'*64))
        self.assertEqual(self.body.b.stops,1)
    def test_wrong_fingerprint_never_reserves_or_stops(self):
        with self.assertRaises(m.Refusal):self.body.mutate('stop',dict(operationId=self.op,expectedFingerprint='b'*64))
        self.assertFalse(self.body.path(self.op).exists());self.assertEqual(self.body.b.stops,0)
    def test_ambiguous_stop_blocks_same_and_new_uuid_without_retry(self):
        self.body.b.fail_stop=True
        with self.assertRaises(m.Refusal):self.stop()
        self.assertEqual(self.stop()['outcome'],'unconfirmed')
        with self.assertRaises(m.Refusal):self.body.mutate('stop',dict(operationId=str(uuid.uuid4()),expectedFingerprint='a'*64))
        with self.assertRaises(m.Refusal):self.start()
        self.assertEqual((self.body.b.stops,self.body.b.starts),(1,0))
    def test_start_ambiguity_never_replays_even_with_actual_new_process(self):
        self.stop();self.body.b.fail_start=True
        with self.assertRaises(m.Refusal):self.start()
        with self.assertRaises(m.Refusal):self.start()
        self.assertEqual(self.body.b.starts,1)
    def test_no_ack_or_live_port_or_old_containers_never_authorizes_start(self):
        for mode in ('ack','port','containers'):
            with self.subTest(mode=mode),tempfile.TemporaryDirectory() as t:
                self.body=Body(Path(t)/'1')
                p=patch.object(self.body,'scan',return_value=(False,b'',None)) if mode=='ack' else patch.object(m,'port_closed' if mode=='port' else 'containers_gone',return_value=False)
                with p:
                    with self.assertRaises(m.Refusal):self.stop()
                    with self.assertRaises(m.Refusal):self.start()
                self.assertEqual(self.body.b.starts,0)
    def test_wrong_or_absent_stop_receipt_never_starts(self):
        with self.assertRaises(m.Refusal):self.start()
        self.stop()
        with self.assertRaises(m.Refusal):self.body.mutate('start',dict(operationId=self.op,stopReceiptRef=str(uuid.uuid4())))
        self.assertEqual(self.body.b.starts,0)
    def test_new_request_cannot_replace_positive_stop_awaiting_its_start(self):
        self.stop()
        with self.assertRaises(m.Refusal):self.body.mutate('stop',dict(operationId=str(uuid.uuid4()),expectedFingerprint='a'*64))
    def test_uuid_and_input_allowlist(self):
        for value in ('../secrets','a'*32,str(uuid.uuid1()),str(uuid.uuid4()).upper()):
            with self.assertRaises(m.Refusal):m.operation_id(value)
        with self.assertRaises(m.Refusal):self.body.mutate('stop',dict(operationId=self.op,expectedFingerprint='a'*64,argv=['anything']))
        self.assertEqual(self.body.b.stops,0)

class ReadinessTests(unittest.TestCase):
    def test_cached_ready_line_does_not_read_20mb_following_log_or_rewrite_record(self):
        with tempfile.TemporaryDirectory() as t:
            path=Path(t)/'log';path.write_bytes(b'prefix\nREADY\n'+b'x'*20*1024*1024);path.chmod(0o600);info=path.stat()
            b=object.__new__(m.Body);b.b=types.SimpleNamespace(LOG_FILE=path,ready_line=lambda line,pid:line==b'READY\n')
            r=dict(pid=10,log_device=info.st_dev,log_inode=info.st_ino,ready_offset=7)
            before=path.stat();self.assertTrue(b.ready(r)[0]);after=path.stat()
            self.assertEqual((before.st_ino,before.st_size,before.st_mtime_ns),(after.st_ino,after.st_size,after.st_mtime_ns))
    def test_private_read_refuses_symlink_and_permissions(self):
        with tempfile.TemporaryDirectory() as t:
            p=Path(t)/'record';p.write_text('{}');p.chmod(0o600);link=Path(t)/'link';link.symlink_to(p)
            with self.assertRaises(OSError):m.private_read(link)
            p.chmod(0o644)
            with self.assertRaises(m.Refusal):m.private_read(p)
            p.chmod(0o600);self.assertEqual(m.private_read(p),b'{}')

if __name__=='__main__':unittest.main()
