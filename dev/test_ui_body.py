"""UI body identity/typed transport fakes only; no game, UI input, model, or sockets."""
import copy
import email.message
import importlib.machinery
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import time
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent.parent
loader = importlib.machinery.SourceFileLoader('ui_body_tested',str(ROOT/'dev/ui-body'))
spec = importlib.util.spec_from_loader(loader.name,loader)
m = importlib.util.module_from_spec(spec); loader.exec_module(m)
BINARY = ROOT/'data/bin/luapolicy'


class Observer:
    def __init__(self):
        self.sid,self.calls,self.hook = 'aw-observer-companion',[],lambda *_:None
        # Literal protoJSON: all known-zero map/axes/flags are omitted.
        self.owner = dict(guid='3',present=True,mapKnown=True,positionKnown=True,positionRevision='8',
                          aliveKnown=True,alive=True,healthKnown=True,health=200,maxHealth=200,
                          unitFlagsKnown=True,movementFlagsKnown=True,classKnown=True,classId=1)
    def call(self,method,params,timeout=5):
        self.calls.append(method); self.hook(method)
        if method == 'buddy-actions.status':return dict(sessionId=self.sid,selfGuid='1')
        return dict(owner=copy.deepcopy(self.owner),self=dict(guid='1',x=9000,mainhand={'itemGuid':'PRIVATE'}),
                    selfQuestLogKnown=True,selfQuestLog=[{'questId':999}],selfBackpack=[{'entry':123}])


class FakeBridge:
    def __init__(self):self.calls=[]
    def validate(self,method,params):
        self.calls.append((method,copy.deepcopy(params)))
        if 'invented' in params:raise m.Error('typed_bridge_rejected')
        return copy.deepcopy(params)


class UIBodyTests(unittest.TestCase):
    def setUp(self):
        folder=tempfile.TemporaryDirectory(dir='/private/tmp');self.addCleanup(folder.cleanup)
        self.folder=Path(folder.name);self.proof=self.folder/'proof.json'
        self.identity=dict(uid=os.geteuid(),start='Sun Nov 1 12:00:00 2026',command='/fake/owned/wine')
        self.record=dict(version=1,sessionId='ui-joel-fake-session',guid='3',pid=19323,windowId=3369,
                         characterQualified=True,capturedAt=time.time(),process=copy.deepcopy(self.identity),helperSha256='a'*64)
        self.save();self.observer,self.bridge=Observer(),FakeBridge()
        self.window=dict(pid=19323,windowId=3369,windowQualified=True,helperSha256='a'*64)
        self.attest=lambda:copy.deepcopy(self.window)
        self.process=lambda _pid:copy.deepcopy(self.identity)
    def save(self):self.proof.write_text(json.dumps(self.record));self.proof.chmod(0o600)
    def body(self,bridge=None):
        return m.Body(self.record['sessionId'],19323,3369,self.proof,self.observer,bridge or self.bridge,
                      self.attest,process=self.process)

    def test_reads_actual_owner_as_self_without_private_companion_state_or_invented_defaults(self):
        body=self.body();self.assertEqual(self.observer.calls,[])
        result=body.dispatch('buddy-world.snapshot',{})
        self.assertEqual(result,{'self':self.observer.owner})
        self.assertNotIn('selfQuestLogKnown',result);self.assertNotIn('mainhand',result['self'])
        self.assertNotIn('uiBinding',result['self']);self.assertNotIn('mapId',result['self'])
        status=body.dispatch('buddy-actions.status',{})
        self.assertEqual((status['sessionId'],status['selfGuid']),('ui-joel-fake-session','3'))
        self.assertEqual(status['position']['revision'],'8');self.assertFalse(status['position']['estimated'])
        self.assertFalse(status['moving']);self.assertFalse(status['navAvailable'])
        internal=body.observe()['uiBinding']
        self.assertEqual(internal['observerSessionId'],'aw-observer-companion')
        self.assertTrue(internal['qualified'])

    def test_foreign_missing_unknown_or_stale_moving_owner_never_becomes_self(self):
        cases=[dict(guid='1'),dict(present=False),dict(positionKnown=False),dict(mapKnown=False),
               dict(positionRevision='0'),dict(x=float('nan')),dict(x=True),
               dict(movementFlags=1,positionAgeMs='2001')]
        for change in cases:
            with self.subTest(change=change):
                old=copy.deepcopy(self.observer.owner);self.observer.owner.update(change)
                with self.assertRaises(m.Error):self.body().dispatch('buddy-world.snapshot',{})
                self.observer.owner=old
        self.observer.owner.update(positionAgeMs='999999',movementFlags=0x100|0x70000000)
        self.assertEqual(self.body().observe()['guid'],'3')  # Stationary modes do not age into movement.

    def test_observer_epoch_sandwich_and_window_process_replacement_fail_closed(self):
        body=self.body();body.observe();self.observer.sid='aw-replaced'
        with self.assertRaisesRegex(m.Error,'observer_body_changed'):body.observe()
        self.observer.sid='aw-observer-companion'
        def replace(method):
            if method=='buddy-world.snapshot':self.observer.sid='aw-during-read'
        self.observer.hook=replace
        with self.assertRaisesRegex(m.Error,'observer_body_changed'):self.body().observe()
        self.observer.hook=lambda *_:None
        self.window['windowId']=3370
        with self.assertRaisesRegex(m.Error,'window_unqualified'):self.body().observe()
        self.window['windowId']=3369;self.window['helperSha256']='b'*64
        with self.assertRaisesRegex(m.Error,'window_unqualified'):self.body().observe()
        self.window['helperSha256']='a'*64;self.identity['start']='new process reused PID'
        with self.assertRaisesRegex(m.Error,'process_changed'):self.body().observe()

    def test_private_character_proof_requires_correct_body_process_and_fresh_owned_regular_file(self):
        for key,value in [('guid','1'),('characterQualified',False),('capturedAt',time.time()+60),('version',True)]:
            with self.subTest(key=key):
                old=self.record[key];self.record[key]=value;self.save()
                with self.assertRaises(m.Error):self.body()
                self.record[key]=old
        self.record['capturedAt']=time.time()-86400
        self.record['process']['start']='Sun Nov  1 12:00:00 2026';self.save()
        self.assertEqual(self.body().observe()['guid'],'3')  # Static registration still needs fresh PID/window proof.
        self.proof.chmod(0o644)
        with self.assertRaisesRegex(m.Error,'private_owned'):self.body()
        self.proof.chmod(0o600);other=self.folder/'other';self.proof.rename(other);self.proof.symlink_to(other)
        with self.assertRaisesRegex(m.Error,'symlink'):self.body()

    def test_registry_and_real_descriptor_validation_reject_before_backend_or_observer(self):
        body=self.body();calls=[]
        backend=types.SimpleNamespace(METHODS=('buddy-actions.travel_to',),call=lambda method,params:calls.append((method,params)) or {'sessionId':body.sid})
        body.register(backend)
        with self.assertRaisesRegex(m.Error,'unsupported'):body.dispatch('buddy-quests.state',{})
        with self.assertRaisesRegex(m.Error,'typed_bridge'):body.dispatch('buddy-actions.travel_to',{'invented':1})
        self.assertEqual(calls,[]);self.assertEqual(self.observer.calls,[])
        self.assertEqual(body.dispatch('buddy-actions.travel_to',{'token':'owned'}),{'sessionId':body.sid})
        self.assertEqual(calls,[('buddy-actions.travel_to',{'token':'owned'})])
        if BINARY.is_file():
            bridge=m.host.Bridge(BINARY,ROOT/'modules');body.bridge=bridge
            with self.assertRaisesRegex(m.Error,'typed_bridge'):body.dispatch('buddy-actions.travel_to',{'invented':1})
            self.assertEqual(len(calls),1)
            self.assertEqual(bridge.validate('buddy-actions.plan_route_leg',dict(sessionId=body.sid,selfGuid='3',expectedPositionRevision='8',x=12)),
                             dict(sessionId=body.sid,selfGuid='3',expectedPositionRevision='8',x=12))
        with self.assertRaisesRegex(m.Error,'filters_unsupported'):body.dispatch('buddy-world.snapshot',{'creatureGuid':'123'})

    def test_http_parser_is_bounded_loopback_only_and_never_dispatches_unvalidated_requests(self):
        body=self.body();Handler=m.handler(body)
        def request(value,**headers):
            raw=value if isinstance(value,bytes) else json.dumps(value).encode()
            obj=object.__new__(Handler);obj.server=types.SimpleNamespace(server_port=8404)
            obj.connection=types.SimpleNamespace(settimeout=lambda _:None)
            obj.path='/rpc';obj.headers=email.message.Message()
            for key,value in dict(Host='127.0.0.1:8404',**{'Content-Type':'application/json','Content-Length':str(len(raw))},**headers).items():obj.headers[key]=value
            obj.rfile,obj.wfile=io.BytesIO(raw),io.BytesIO()
            obj.send_response=lambda _:None;obj.send_header=lambda *_:None;obj.end_headers=lambda:None
            obj.do_POST();return json.loads(obj.wfile.getvalue())
        good=dict(jsonrpc='2.0',id=7,method='buddy-world.snapshot',params={})
        self.assertEqual(request(good)['result']['self']['guid'],'3')
        before=len(self.observer.calls)
        for value,headers in [(good,{'Origin':'https://foreign.example'}),
                              (dict(good,id=True),{}),(b'{"id":1,"id":2}',{}),
                              (b' '*32769,{}),(dict(good,method='buddy-inventory.equip'),{})]:
            with self.subTest(value=str(value)[:40]):self.assertIn('error',request(value,**headers))
        self.assertEqual(len(self.observer.calls),before)

    def test_actual_movement_registry_accepts_bound_owner_provenance_without_ui_inputs(self):
        import ui_body_movement as movement
        body=self.body();self.observer.owner.update(speedsKnown=True,runSpeed=7)
        actuator=types.SimpleNamespace(press=lambda *_:self.fail('no actual UI input permitted'))
        def plan(p,goal):
            return dict(ok=True,mapId=0,points=[dict(x=0,y=0,z=0),goal],endpoint=goal)
        backend=movement.MovementBackend(body.sid,19323,3369,body.observe,self.folder/'movement',self.folder/'assets',
                                         actuator=actuator,nav=plan)
        backend.METHODS=movement.METHODS;body.register(backend)
        state=body.dispatch('buddy-actions.status',{})
        self.assertEqual(state['selfGuid'],'3');self.assertFalse(state['position'].get('estimated',False))
        planned=body.dispatch('buddy-actions.plan_route_leg',dict(sessionId=body.sid,selfGuid='3',expectedPositionRevision='8',x=1))
        self.assertEqual(planned['endpoint']['x'],1);self.assertEqual(planned['serverPositionRevision'],'8')
        lease=body.dispatch('buddy-actions.acquire_interaction',dict(owner='exploration',leaseMs=15000))
        self.assertTrue(lease['token']);self.assertEqual(lease['state']['interactionOwner'],'exploration')
        final=body.dispatch('buddy-actions.release_interaction',dict(owner='exploration',token=lease['token']))
        self.assertEqual(final['interactionOwner'],'');self.assertFalse(final['motionUnconfirmed'])
        self.assertEqual(backend.record['sequence'],0)

    @unittest.skipUnless(BINARY.is_file(),'local compiled descriptor/Lua bridge unavailable')
    def test_same_existing_runner_drives_guid3_ui_body_with_no_brain_or_real_input(self):
        bridge=m.host.Bridge(BINARY,ROOT/'modules');body=self.body(bridge)
        class Endpoint:
            url='http://127.0.0.1:8404/rpc'
            def call(self,method,params,timeout=5):return body.dispatch(method,params)
        source=self.folder/'policy.lua';source.write_text('return {decide=function(s) return {memory={},phase="owner_observed",done=s.observations.world.self.guid=="3"} end}')
        library=self.folder/'library.lua';library.write_text('return {}')
        goal=dict(id='ui-body-read-proof',kind='observing',params={},allow=['buddy-world.snapshot'],
                  observations={'world':dict(method='buddy-world.snapshot',params={},select=['self.guid'])})
        original_run=m.subprocess.run
        def only_bridge(command,*args,**kwargs):
            self.assertEqual(command[0],str(BINARY))
            return original_run(command,*args,**kwargs)
        # Bridge itself uses a compiled pure-code subprocess; only the observer/actuator are injected fakes.
        with m.host.Store(self.folder/'runtime') as store, patch.object(m.subprocess,'run',side_effect=only_bridge):
            runner=m.host.Runner(store,bridge,Endpoint(),3,body.sid,goal,source,library,
                                 shared=m.host.SharedBoard(self.folder/'shared'))
            runner.tick()
            self.assertEqual(runner.data['phase'],'owner_observed');self.assertTrue(runner.data['done'])
            self.assertEqual(runner.data['attempts'],[]);self.assertEqual(runner.data['pending'],[])
            self.assertEqual(runner.data['identity']['guid'],'3')


if __name__=='__main__':unittest.main()
