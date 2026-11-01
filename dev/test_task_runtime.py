"""Typed-bridge/RPC fakes and private temporary ledgers; no game calls."""
import copy
import concurrent.futures
import importlib.machinery
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import unittest
from unittest.mock import patch

loader = importlib.machinery.SourceFileLoader('task_runtime_tested', str(Path(__file__).with_name('task-runtime')))
spec = importlib.util.spec_from_loader(loader.name, loader)
m = importlib.util.module_from_spec(spec); loader.exec_module(m)


class FakeBridge:
    def __init__(self):
        self.methods = {'buddy-actions.status': 'read', 'vendor.state': 'read', 'vendor.list': 'query',
                        'vendor.buy': 'mutation', 'vendor.unsafe': 'unspecified'}
        self.revision, self.inputs, self.validations, self.sources, self.descriptions = 'a' * 64, [], [], [], 0
        self.decide = lambda value: dict(memory=value['memory'], phase='waiting')

    def describe(self):
        self.descriptions += 1
        return dict(revision=self.revision, methods=[dict(name=k, effect=v) for k, v in self.methods.items()])

    def validate(self, method, params):
        self.validations.append((method, copy.deepcopy(params)))
        if method not in self.methods or not isinstance(params, dict) or params.get('quantity') == 'bad':
            raise m.Error('typed_bridge_rejected')
        return copy.deepcopy(params)

    def evaluate(self, source, value):
        self.inputs.append(copy.deepcopy(value)); self.sources.append(source)
        if 'BROKEN' in source: raise m.Error('typed_bridge_rejected')
        return self.decide(copy.deepcopy(value))


class FakeRPC:
    url = 'http://127.0.0.1:8090/rpc'
    def __init__(self):
        self.sid, self.guid, self.count, self.calls = 'aw-test', '87', 0, []
        self.hook = lambda method, params: None

    def call(self, method, params, timeout=5):
        self.calls.append((method, copy.deepcopy(params)))
        self.hook(method, params)
        if method == 'buddy-actions.status': return dict(sessionId=self.sid, selfGuid=self.guid)
        if method == 'vendor.buy': self.count += 1; return dict(accepted=True, receipt='owned-1')
        return dict(count=self.count, items=[dict(entry=4498, private='omit')], private='not selected')


class BridgeDiagnosticsTests(unittest.TestCase):
    def test_rejected_bridge_preserves_bounded_actual_reason_without_success(self):
        reply = subprocess.CompletedProcess([], 1, stdout=b'{"done":true}',
            stderr=b'invalid behavior input: node bound\n'+b'x'*1000+b'\x00')
        with patch.object(m.subprocess, 'run', return_value=reply):
            with self.assertRaisesRegex(m.Error, 'typed_bridge_rejected: invalid behavior input: node bound') as caught:
                m.Bridge('/not/executed', '/unused').evaluate('return {}', {})
        self.assertLessEqual(len(str(caught.exception)), len('typed_bridge_rejected: ')+192)
        self.assertNotIn('\n', str(caught.exception)); self.assertNotIn('\x00', str(caught.exception))

    @unittest.skipUnless((m.ROOT/'data/bin/luapolicy').is_file(), 'local pure bridge unavailable')
    def test_actual_native_node_rejection_is_distinct_from_small_valid_frame(self):
        bridge = m.Bridge(m.ROOT/'data/bin/luapolicy', m.ROOT/'modules')
        source = 'return {decide=function(s) return {phase="valid"} end}'
        self.assertEqual(bridge.evaluate(source, {})['phase'], 'valid')
        value = {'rows': [[1]*128 for _ in range(32)]}
        self.assertLess(len(m.packed(value)), m.MAX_INPUT)
        with self.assertRaisesRegex(m.BridgeFailure, 'policy value exceeds node, byte or depth bounds') as caught:
            bridge.evaluate(source, value)
        self.assertEqual(caught.exception.evidence['inputBytes'], len(m.packed(value, m.MAX_INPUT)))
        self.assertGreater(caught.exception.evidence['conversionNodes'], 4096)
        self.assertEqual(m.decoded(caught.exception.frame), value)


class RuntimeTests(unittest.TestCase):
    def setUp(self):
        folder = tempfile.TemporaryDirectory(dir='/private/tmp'); self.addCleanup(folder.cleanup)
        self.root = Path(folder.name)
        self.source, self.library = self.root / 'source.lua', self.root / 'library.lua'
        self.source.write_text('return {decide=function(s) return {} end} --GOOD')
        self.library.write_text('return {}')
        self.bridge, self.rpc = FakeBridge(), FakeRPC()
        self.goal = dict(id='supplies', kind='shopping', params={'entry': 4498},
            observations={'stock': dict(method='vendor.state', params={}, select=['count', 'items.*.entry'])},
            allow=['vendor.state', 'vendor.list', 'vendor.buy'])
        self.folder = self.root / 'state'
        self.shared = m.SharedBoard(self.root / 'shared')

    def runner(self, store):
        return m.Runner(store, self.bridge, self.rpc, 87, 'aw-test', self.goal, self.source, self.library, shared=self.shared)

    def buy(self, request='buy-1'):
        return dict(memory={'attempted': True}, phase='buying', action=dict(id=request, method='vendor.buy', params={'quantity': 1}))

    def purchases(self): return [v for v in self.rpc.calls if v[0] == 'vendor.buy']

    def test_native_failure_keeps_exact_private_frame_and_preserves_pending(self):
        self.bridge.decide = lambda _: self.buy()
        self.rpc.hook = lambda method, _: (_ for _ in ()).throw(m.Error('lost')) if method == 'vendor.buy' else None
        with m.Store(self.folder) as store:
            runner = self.runner(store); runner.tick()
            pending = copy.deepcopy(runner.data['pending'])
            def reject(_source, value):
                raise m.BridgeFailure('evaluate', 'policy value exceeds node, byte or depth bounds',
                                      m.packed(value, m.MAX_INPUT))
            self.bridge.evaluate = reject
            with self.assertRaises(m.BridgeFailure): runner.tick()
            saved = store.load()
            self.assertEqual(saved['pending'], pending)
            evidence = saved['bridgeFailure']
            raw = m.read_file(self.folder/evidence['inputFile'], m.MAX_INPUT, True)
            self.assertEqual(len(raw), evidence['inputBytes'])
            self.assertEqual(m.hashlib.sha256(raw).hexdigest(), evidence['inputSHA256'])
            self.assertEqual(m.decoded(raw)['pending'], pending)
            self.assertIn('observations.stock', evidence['components'])
            self.assertEqual(len(self.purchases()), 1)

    def test_previously_good_source_rechecks_new_frame_without_erasing_rejection(self):
        with m.Store(self.folder) as store:
            runner = self.runner(store); runner.tick()
            revision = runner.data['goodRevision']
            runner.data['rejectedRevision'] = revision
            def decide(value):
                if value['now'] == 1:
                    raise m.BridgeFailure('evaluate', 'input node bound', m.packed(value,m.MAX_INPUT))
                return dict(memory=value['memory'], phase='fresh_frame_valid')
            self.bridge.decide = decide
            frame = dict(memory=runner.data['memory'], now=1)
            with self.assertRaises(m.BridgeFailure): runner.evaluate(frame)
            self.assertEqual(store.load()['bridgeFailure']['stage'], 'fallback')
            result = runner.evaluate(dict(frame, now=2))
            self.assertEqual(result['phase'], 'fresh_frame_valid')
            self.assertEqual(runner.data['rejectedRevision'], revision)
            self.assertEqual(runner.data['pending'], [])
            # A different, invalid source remains quarantined on later frames.
            self.source.write_text('BROKEN')
            runner.evaluate(dict(frame, now=2))
            bad_revision = runner.data['rejectedRevision']
            self.bridge.sources.clear()
            runner.evaluate(dict(frame, now=3))
            self.assertEqual(runner.data['rejectedRevision'], bad_revision)
            self.assertFalse(any('BROKEN' in source for source in self.bridge.sources))

    def enable_run(self, runner, store):
        runner.data['run'] = dict(instance='a'*32, pid=os.getpid(), active=True)
        runner.persist()
        runner.stopped = lambda: m.stop_requested(store, runner.data)

    def test_stop_marker_binds_exact_body_instance_and_preserves_all_history(self):
        self.bridge.decide = lambda _: self.buy()
        self.rpc.hook = lambda method, _: (_ for _ in ()).throw(m.Error('lost')) if method == 'vendor.buy' else None
        with m.Store(self.folder) as store:
            runner = self.runner(store); self.enable_run(runner, store); runner.tick()
            before = store.load()
            self.assertTrue(m.request_stop(m.Store(self.folder),87,'aw-test',self.rpc.url)['stopRequested'])
            self.assertTrue(m.stop_requested(store, runner.data))
            self.assertEqual(store.load(), before)
            with self.assertRaisesRegex(m.Error,'stopped_or_deadline'): runner.tick()
            for guid,sid,url in ((8,'aw-test',self.rpc.url),(87,'aw-other',self.rpc.url),(87,'aw-test',self.rpc.url+'x')):
                with self.assertRaisesRegex(m.Error,'stop_body_binding'): m.request_stop(store,guid,sid,url)
            runner.data['run']['instance']='b'*32;runner.persist()
            self.assertFalse(m.stop_requested(store,runner.data))
            self.assertEqual(len(self.purchases()),1)
        self.assertFalse(m.Store(self.folder).held())

    def test_stop_during_returned_call_keeps_authenticated_reply_and_never_replays(self):
        self.bridge.decide = lambda _: self.buy()
        with m.Store(self.folder) as store:
            runner=self.runner(store);self.enable_run(runner,store)
            def stop(method,_):
                if method=='vendor.buy':m.request_stop(m.Store(self.folder),87,'aw-test',self.rpc.url)
            self.rpc.hook=stop;runner.tick()
            self.assertEqual(runner.data['last']['response']['receipt'],'owned-1')
            self.assertEqual(runner.data['pending'],[])
            self.assertEqual(runner.data['attempts'][0]['outcome'],'rpc_result')
            with self.assertRaisesRegex(m.Error,'stopped_or_deadline'):runner.tick()
            self.assertEqual(len(self.purchases()),1)

    def test_stop_during_lost_reply_keeps_issued_mutation_pending(self):
        self.bridge.decide=lambda _:self.buy()
        with m.Store(self.folder) as store:
            runner=self.runner(store);self.enable_run(runner,store)
            def lost(method,_):
                if method=='vendor.buy':
                    m.request_stop(m.Store(self.folder),87,'aw-test',self.rpc.url)
                    raise m.Error('rpc_result_unconfirmed')
            self.rpc.hook=lost;runner.tick()
            self.assertEqual(runner.data['pending'][0]['id'],'buy-1')
            self.assertEqual(runner.data['attempts'][0]['outcome'],'pending')
            self.assertNotIn('response',runner.data['last'])
            with self.assertRaisesRegex(m.Error,'stopped_or_deadline'):runner.tick()
            self.assertEqual(len(self.purchases()),1)

    def test_stop_after_reservation_prevents_write_and_records_no_dispatch_not_success(self):
        self.bridge.decide=lambda _:self.buy()
        with m.Store(self.folder) as store:
            runner=self.runner(store);self.enable_run(runner,store);save=store.save
            def save_and_stop(data):
                save(data)
                if data['pending']:m.request_stop(m.Store(self.folder),87,'aw-test',self.rpc.url)
            with patch.object(store,'save',side_effect=save_and_stop):runner.tick()
            self.assertEqual(self.purchases(),[])
            self.assertEqual(store.load()['pending'],[])
            self.assertEqual(store.load()['attempts'][0]['id'],'buy-1')
            self.assertEqual(store.load()['attempts'][0]['outcome'],'not_dispatched')
            self.assertNotIn('response',store.load()['last'])
            self.runner(store)  # The durable terminal row remains a valid, non-replayable history.

    def test_private_stop_marker_and_crashed_or_inactive_host_cannot_claim_cleanup(self):
        with m.Store(self.folder) as store:
            runner=self.runner(store);self.enable_run(runner,store)
            target=self.folder/('stop-'+'a'*32+'.json')
            outside=self.root/'outside';outside.write_text('{}');outside.chmod(0o600);target.symlink_to(outside)
            with self.assertRaises(OSError):m.request_stop(store,87,'aw-test',self.rpc.url)
            target.unlink();target.write_text('{}');target.chmod(0o600)
            with self.assertRaisesRegex(m.Error,'stop_request_binding'):m.stop_requested(store,runner.data)
            target.unlink();runner.data['run']['active']=False;runner.persist()
            self.assertFalse(m.request_stop(store,87,'aw-test',self.rpc.url)['stopRequested'])
        # An abandoned active flag without a live lock is not a live stop request.
        data=m.Store(self.folder).load();data['run']['active']=True;m.Store(self.folder).save(data)
        self.assertFalse(m.request_stop(m.Store(self.folder),87,'aw-test',self.rpc.url)['stopRequested'])

    def test_cli_cooperative_exit_releases_lock_without_clearing_pending(self):
        self.bridge.decide=lambda _:self.buy()
        self.rpc.hook=lambda method,_:(_ for _ in ()).throw(m.Error('lost')) if method=='vendor.buy' else None
        goal=self.root/'goal.json';goal.write_text(json.dumps(self.goal))
        def stop(_):m.request_stop(m.Store(self.folder),87,'aw-test',self.rpc.url)
        with patch.object(m,'Bridge',return_value=self.bridge),patch.object(m,'RPC',return_value=self.rpc),\
                patch.object(m.signal,'signal'),patch.object(m.time,'sleep',side_effect=stop),patch('sys.stdout',new=io.StringIO()):
            self.assertEqual(m.main(['run','--guid','87','--session-id','aw-test','--rpc-url',self.rpc.url,
                '--goal',str(goal),'--source',str(self.source),'--state-dir',str(self.folder)]),0)
        data=m.Store(self.folder).load()
        self.assertFalse(data['run']['active']);self.assertGreater(data['run']['stoppedAt'],0)
        self.assertEqual(data['run']['sourcePath'],str(self.source.absolute()))
        self.assertEqual(data['pending'][0]['id'],'buy-1');self.assertEqual(len(self.purchases()),1)
        self.assertFalse(m.Store(self.folder).held())

    def test_cli_error_stamps_actual_run_and_normal_restart_retains_old_error_provenance(self):
        self.source.write_text('BROKEN');goal=self.root/'goal.json';goal.write_text(json.dumps(self.goal))
        argv=['run','--guid','87','--session-id','aw-test','--rpc-url',self.rpc.url,
            '--goal',str(goal),'--source',str(self.source),'--state-dir',str(self.folder)]
        with patch.object(m,'Bridge',return_value=self.bridge),patch.object(m,'RPC',return_value=self.rpc),\
                patch.object(m.signal,'signal'),patch('sys.stdout',new=io.StringIO()):
            with self.assertRaisesRegex(m.Error,'no_valid_policy'):m.main(argv)
            failed=m.Store(self.folder).load();old=failed['run']['instance']
            self.assertFalse(failed['run']['active'])
            self.assertEqual(failed['hostErrorInstance'],old);self.assertEqual(failed['policyErrorInstance'],old)
            self.source.write_text('return {} --GOOD')
            self.bridge.decide=lambda value:dict(memory=value['memory'],phase='complete',done=True)
            self.assertEqual(m.main(argv),0)
        current=m.Store(self.folder).load()
        self.assertNotEqual(current['run']['instance'],old);self.assertFalse(current['run']['active'])
        self.assertEqual(current['hostError'],failed['hostError']);self.assertEqual(current['hostErrorInstance'],old)
        self.assertEqual(current['policyError'],'')

    def test_receipt_survives_observation_reads_and_completed_restart(self):
        def decide(value):
            if value['last']:
                self.assertEqual(value['last']['response']['receipt'], 'owned-1')
                return dict(memory={'receipt': value['last']['response']}, phase='complete', done=True)
            return self.buy()
        self.bridge.decide = decide
        with m.Store(self.folder) as store:
            runner = self.runner(store)
            def hook(method, params):
                if method == 'vendor.buy':
                    saved = store.load()
                    self.assertEqual(saved['memory'], {'attempted': True})
                    self.assertEqual(saved['pending'][0]['id'], 'buy-1')
                    self.assertEqual(saved['pending'][0]['params'], params)
            self.rpc.hook = hook
            runner.tick(); runner.tick()
            self.assertTrue(runner.data['done']); self.assertEqual(runner.data['pending'], [])
            self.assertEqual(self.bridge.descriptions, 1)
        with m.Store(self.folder) as store:
            self.runner(store).tick()
        self.assertEqual(len(self.purchases()), 1)
        self.assertEqual(self.bridge.inputs[0]['body'], {'sessionId': 'aw-test', 'selfGuid': '87'})
        self.assertEqual(self.bridge.inputs[0]['observations'], {'stock': {'count': 0, 'items': [{'entry': 4498}]}})

    def test_lost_write_reply_restart_never_replays_same_or_new_id_until_predicate(self):
        self.bridge.decide = lambda _: self.buy()
        def lose(method, params):
            if method == 'vendor.buy': self.rpc.count = 1; raise m.Error('rpc_result_unconfirmed')
        self.rpc.hook = lose
        with m.Store(self.folder) as store: self.runner(store).tick()
        self.rpc.hook = lambda *_: None
        with m.Store(self.folder) as store:
            runner = self.runner(store)
            with self.assertRaisesRegex(m.Error, 'already_attempted'): runner.tick()
            self.bridge.decide = lambda _: self.buy('new-id-cannot-bypass')
            with self.assertRaisesRegex(m.Error, 'pending_outcome_blocks'): runner.tick()
            self.bridge.decide = lambda _: dict(memory={}, phase='resolved', resolve=dict(
                id='buy-1', observation='stock', path='count', equals=1, outcome='actual_inventory_count_observed'))
            runner.tick()
            self.assertEqual(runner.data['pending'], [])
            self.assertEqual(runner.data['attempts'][0]['outcome'], 'policy_observation_resolution')
            self.assertIn('rpc_result_unconfirmed', runner.data['attempts'][0]['error'])
            self.bridge.decide = lambda _: dict(memory={}, phase='complete', done=True)
            runner.tick()
        self.assertEqual(len(self.purchases()), 1)

    def test_ambiguous_query_not_reissued_but_other_fresh_reads_queries_allowed(self):
        self.goal['observations']['catalog'] = dict(method='vendor.list', params={}, select=['items.*.entry'])
        def lose(method, params):
            if method == 'vendor.list': raise m.Error('rpc_result_unconfirmed')
        self.rpc.hook = lose
        with m.Store(self.folder) as store: self.runner(store).tick()
        with m.Store(self.folder) as store:
            runner = self.runner(store); runner.tick()
            self.assertEqual(len(runner.data['pending']), 1)
            self.assertEqual(len([v for v in self.rpc.calls if v[0] == 'vendor.list']), 1)
            self.rpc.hook = lambda *_: None
            self.bridge.decide = lambda _: dict(memory={}, phase='query_other', action=dict(
                id='another-query', method='vendor.list', params={'entry': 1}))
            runner.tick()
            self.assertEqual(len(runner.data['pending']), 1)
            self.assertEqual(len([v for v in self.rpc.calls if v[0] == 'vendor.list']), 2)

    def test_resolution_requires_fresh_selected_exact_value_never_missing_or_boolean_coercion(self):
        self.bridge.decide = lambda _: self.buy()
        self.rpc.hook = lambda method, _: (_ for _ in ()).throw(m.Error('lost')) if method == 'vendor.buy' else None
        with m.Store(self.folder) as store:
            runner = self.runner(store); runner.tick()
            self.rpc.hook = lambda *_: None
            for path, expected in [('missing', None), ('count', True), ('count', 99)]:
                self.bridge.decide = lambda _, p=path, e=expected: dict(memory={}, phase='resolve', resolve=dict(
                    id='buy-1', observation='stock', path=p, equals=e))
                with self.assertRaisesRegex(m.Error, 'predicate_not_observed'): runner.tick()
                self.assertEqual(len(runner.data['pending']), 1)
            self.rpc.hook = lambda method, _: (_ for _ in ()).throw(m.Error('read_failed')) if method == 'vendor.state' else None
            with self.assertRaisesRegex(m.Error, 'fresh_resolution'): runner.tick()
            self.bridge.decide = lambda _: dict(memory={}, phase='done', done=True)
            with self.assertRaisesRegex(m.Error, 'prevents_done'): runner.tick()
            self.assertFalse(store.load()['done'])

    def test_hot_source_invalid_candidate_falls_back_across_restart_then_new_revision(self):
        with m.Store(self.folder) as store:
            runner = self.runner(store); runner.tick(); old = runner.data['goodRevision']
        self.source.write_text('BROKEN syntax')
        with m.Store(self.folder) as store:
            runner = self.runner(store); runner.tick()
            self.assertEqual(runner.data['goodRevision'], old)
            self.assertTrue(runner.data['policyError'])
            attempts = len([s for s in self.bridge.sources if 'BROKEN' in s])
            runner.tick()
            self.assertEqual(len([s for s in self.bridge.sources if 'BROKEN' in s]), attempts)
            self.source.write_text('return {} --NEW')
            runner.tick(); self.assertNotEqual(runner.data['goodRevision'], old)
            self.assertEqual(runner.data['policyError'], '')
        self.assertEqual(self.purchases(), [])

    def test_changed_body_before_tick_or_final_write_has_no_dispatch(self):
        self.bridge.decide = lambda _: self.buy()
        with m.Store(self.folder) as store:
            runner = self.runner(store)
            self.rpc.sid = 'aw-other'
            with self.assertRaisesRegex(m.Error, 'body_changed'): runner.tick()
            self.assertEqual(self.purchases(), [])
            self.rpc.sid = 'aw-test'
            def decide(_): self.rpc.sid = 'aw-other'; return self.buy()
            self.bridge.decide = decide
            with self.assertRaisesRegex(m.Error, 'body_changed'): runner.tick()
            self.assertEqual(self.purchases(), []); self.assertEqual(store.load()['pending'], [])

    def test_body_change_during_reservation_or_reply_retains_pending_never_claims_result(self):
        self.bridge.decide = lambda _: self.buy()
        with m.Store(self.folder) as store:
            runner = self.runner(store); save = store.save
            def replace_after_reservation(data):
                save(data)
                if data['pending']: self.rpc.sid = 'aw-replacement'
            with patch.object(store, 'save', side_effect=replace_after_reservation):
                with self.assertRaisesRegex(m.Error, 'body_changed'): runner.tick()
            self.assertEqual(self.purchases(), [])
            self.assertEqual(store.load()['pending'][0]['id'], 'buy-1')
        self.rpc.sid = 'aw-test'
        with m.Store(self.root / 'post-reply') as store:
            runner = self.runner(store)
            def replace_during_call(method, _):
                if method == 'vendor.buy': self.rpc.sid = 'aw-replacement'
            self.rpc.hook = replace_during_call
            with self.assertRaisesRegex(m.Error, 'body_changed'): runner.tick()
            self.assertEqual(len(store.load()['pending']), 1)
            self.assertNotIn('response', store.load()['last'])
        self.assertEqual(len(self.purchases()), 1)

    def test_goal_catalog_identity_changes_refuse_resume_and_unknown_effect_denied(self):
        with m.Store(self.folder) as store: self.runner(store).tick()
        with m.Store(self.folder) as store:
            before = (self.folder / 'state.json').read_bytes()
            self.goal['params']['entry'] = 41599
            with self.assertRaisesRegex(m.Error, 'binding_or_shape'): self.runner(store)
            self.goal['params']['entry'] = 4498; self.bridge.revision = 'b' * 64
            with self.assertRaisesRegex(m.Error, 'binding_or_shape'): self.runner(store)
            self.assertEqual((self.folder / 'state.json').read_bytes(), before)
        self.goal['allow'].append('vendor.unsafe')
        with m.Store(self.root / 'other') as store:
            with self.assertRaisesRegex(m.Error, 'method_not_admitted'): self.runner(store)

    def test_descriptor_rejection_and_unallowed_policy_never_invoke_rpc(self):
        for action in [dict(id='x', method='vendor.buy', params={'quantity': 'bad'}),
                       dict(id='x', method='vendor.unsafe', params={}),
                       dict(id='x', method='server.packet', params={})]:
            with self.subTest(action=action), m.Store(self.root / action['method']) as store:
                self.bridge.decide = lambda _, a=action: dict(memory={}, phase='invalid', action=a)
                with self.assertRaisesRegex(m.Error, 'no_valid_policy'): self.runner(store).tick()
        self.assertEqual(self.purchases(), [])
        self.assertTrue(all(method in ('buddy-actions.status', 'vendor.state') for method, _ in self.rpc.calls))

    def test_reservation_save_failure_no_dispatch_and_reply_save_failure_stays_pending(self):
        self.bridge.decide = lambda _: self.buy()
        with m.Store(self.folder) as store:
            runner = self.runner(store); real_save = store.save
            def failed_reservation(value):
                if value['pending']: raise OSError('disk unavailable')
                real_save(value)
            with patch.object(store, 'save', side_effect=failed_reservation):
                with self.assertRaises(OSError): runner.tick()
            self.assertEqual(self.purchases(), [])
        with m.Store(self.folder) as store:
            runner = self.runner(store); real_save = store.save
            def failed_reply(value):
                if value['last'] and 'response' in value['last']: raise OSError('disk unavailable')
                real_save(value)
            with patch.object(store, 'save', side_effect=failed_reply):
                with self.assertRaises(OSError): runner.tick()
            self.assertEqual(store.load()['pending'][0]['id'], 'buy-1')
        with m.Store(self.folder) as store:
            with self.assertRaisesRegex(m.Error, 'already_attempted'): self.runner(store).tick()
        self.assertEqual(len(self.purchases()), 1)

    def test_private_lock_symlink_and_size_bounds(self):
        with m.Store(self.folder) as store:
            with self.assertRaisesRegex(m.Error, 'already_running'):
                with m.Store(self.folder): pass
            runner = self.runner(store)
            self.bridge.decide = lambda _: dict(memory={'large': 'x' * m.MAX_INPUT}, phase='oversize')
            with self.assertRaisesRegex(m.Error, 'no_valid_policy'): runner.tick()
        self.source.unlink(); self.source.symlink_to(self.library)
        with m.Store(self.root / 'symlink') as store:
            with self.assertRaisesRegex(m.Error, 'no_valid_policy'): self.runner(store).tick()
        (self.folder / 'state.json').chmod(0o644)
        with self.assertRaisesRegex(m.Error, 'private_owned'): m.Store(self.folder).load()
        self.assertEqual(self.purchases(), [])

    def test_attempt_capacity_and_corrupt_pending_fail_closed(self):
        self.bridge.decide = lambda _: self.buy()
        self.rpc.hook = lambda method, _: (_ for _ in ()).throw(m.Error('lost')) if method == 'vendor.buy' else None
        with m.Store(self.folder) as store:
            runner = self.runner(store); runner.tick()
            data = store.load(); data['pending'] = []
            store.save(data)
            with self.assertRaisesRegex(m.Error, 'pending_invalid'): self.runner(store)
            data['pending'] = [copy.deepcopy(data['attempts'][0])]; store.save(data)
            runner = self.runner(store)
            # Another query can be read while mutation is pending, but capacity
            # exhaustion must refuse it before dispatch and preserve old IDs.
            self.bridge.decide = lambda _: dict(memory={}, phase='query', action=dict(id='query', method='vendor.list', params={}))
            with patch.object(m, 'MAX_ACTIONS', 1):
                with self.assertRaisesRegex(m.Error, 'history_full'): runner.tick()
            self.assertEqual(len(store.load()['attempts']), 1)
            self.assertFalse(any(method == 'vendor.list' for method, _ in self.rpc.calls))

    def test_deadline_or_stop_after_decision_prevents_dispatch(self):
        with m.Store(self.folder) as store:
            runner = self.runner(store)
            def decide(_): runner.stopped = lambda: True; return self.buy()
            self.bridge.decide = decide
            with self.assertRaisesRegex(m.Error, 'stopped_or_deadline'): runner.tick()
            self.assertEqual(self.purchases(), []); self.assertEqual(store.load()['pending'], [])

    def test_generic_rpc_timeout_is_explicit_bounded_and_deadline_capped(self):
        with m.Store(self.folder) as store:
            runner = m.Runner(store, self.bridge, self.rpc, 87, 'aw-test', self.goal, self.source, self.library, rpc_timeout=35)
            with patch.object(self.rpc, 'call', wraps=self.rpc.call) as call:
                runner.bind(); self.assertEqual(call.call_args.kwargs['timeout'], 35)
                runner.deadline = m.time.monotonic() + 2
                runner.bind(); self.assertLessEqual(call.call_args.kwargs['timeout'], 2)
            for timeout in (0, 41, float('nan'), True):
                with self.assertRaisesRegex(m.Error, 'rpc_timeout_bounds'):
                    m.Runner(store, self.bridge, self.rpc, 87, 'aw-test', self.goal, self.source, self.library, rpc_timeout=timeout)

    def test_projection_preserves_nested_arrays_and_resolution_paths_are_present(self):
        raw = {'rows': [{'id': '1', 'nested': {'count': 0}, 'extra': 42}, {'id': '2', 'nested': {'count': 4}}]}
        value = m.project(raw, ['rows.*.id', 'rows.*.nested.count'])
        self.assertEqual(value, {'rows': [{'id': '1', 'nested': {'count': 0}}, {'id': '2', 'nested': {'count': 4}}]})
        self.assertEqual(m.lookup(value, ['rows', '*', 'nested', 'count']), (True, [0, 4]))
        self.assertEqual(m.lookup(value, ['rows', '0', 'nested', 'count']), (True, 0))
        self.assertEqual(m.lookup(value, ['rows', '0', 'missing']), (False, None))
        self.assertNotIn('extra', json.dumps(value))

    def test_actual_large_read_and_query_observation_are_projected_before_policy_receipt_bound(self):
        raw=dict(count=7,items=[dict(entry=4498)],unselected='x'*56091)
        original=self.rpc.call
        def call(method,params,timeout=5):
            if method in ('vendor.state','vendor.list'):
                self.rpc.calls.append((method,params));return copy.deepcopy(raw)
            return original(method,params,timeout)
        self.rpc.call=call
        for method in ('vendor.state','vendor.list'):
            with self.subTest(method=method),m.Store(self.folder/method) as store:
                self.goal['observations']['stock']['method']=method
                runner=self.runner(store);runner.tick()
                self.assertEqual(self.bridge.inputs[-1]['observations']['stock'],dict(count=7,items=[dict(entry=4498)]))
                self.assertEqual(store.load()['observationErrors'],{})
                self.assertEqual(store.load()['pending'],[]);self.assertIsNone(store.load()['last'])
                self.assertNotIn('unselected',(store.folder/'state.json').read_text())
                if method=='vendor.list':self.assertEqual(store.load()['attempts'][0]['outcome'],'rpc_result')
                else:self.assertEqual(store.load()['attempts'],[])

    def test_observation_failures_are_specific_current_run_evidence_then_clear_on_positive_read(self):
        original=self.rpc.call
        def oversized(method,params,timeout=5):
            if method=='vendor.state':return dict(count=7,items=[dict(entry='x'*m.MAX_INPUT)])
            return original(method,params,timeout)
        with m.Store(self.folder) as store:
            runner=self.runner(store);self.enable_run(runner,store)
            self.rpc.call=oversized;runner.tick()
            self.assertNotIn('stock',self.bridge.inputs[-1]['observations'])
            self.assertEqual(store.load()['observationErrors']['stock'],dict(method='vendor.state',error='json_size_bound'))
            self.assertEqual(store.load()['observationErrorInstance'],'a'*32)
            self.assertIn('stock:json_size_bound',m.status(store.load())['observationError'])
            self.rpc.call=original
            self.rpc.hook=lambda method,_:(_ for _ in ()).throw(m.Error('rpc_result_unconfirmed')) if method=='vendor.state' else None
            runner.tick();self.assertIn('rpc_result_unconfirmed',store.load()['observationError'])
            self.rpc.hook=lambda *_:None;runner.tick()
            self.assertEqual(store.load()['observationErrors'],{});self.assertEqual(store.load()['observationError'],'')
            self.assertIn('stock',self.bridge.inputs[-1]['observations']);self.assertEqual(self.purchases(),[])

    def test_projection_exemption_does_not_relax_direct_query_mutation_receipts_or_transport_cap(self):
        original=self.rpc.call
        def large(method,params,timeout=5):
            if method in ('vendor.list','vendor.buy'):
                self.rpc.calls.append((method,params));return dict(payload='x'*m.MAX_INPUT)
            return original(method,params,timeout)
        self.rpc.call=large
        for method in ('vendor.list','vendor.buy'):
            with self.subTest(method=method),m.Store(self.folder/method) as store:
                runner=self.runner(store)
                self.assertIsNone(runner.request(dict(id='issued',method=method,params={})))
                self.assertEqual(store.load()['pending'][0]['outcome'],'pending')
                self.assertEqual(store.load()['last']['error'],'json_size_bound')
                with self.assertRaisesRegex(m.Error,'already_attempted'):
                    runner.request(dict(id='issued',method=method,params={}))
        rpc=m.RPC(self.rpc.url)
        raw=json.dumps(dict(jsonrpc='2.0',id=1,result=dict(payload='x'*1048576))).encode()
        with patch.object(rpc.opener,'open',return_value=io.BytesIO(raw)):
            with self.assertRaisesRegex(m.Error,'rpc_response_size_bound'):rpc.call('vendor.state',{})

    def test_last_effect_is_catalog_derived_for_read_query_and_mutation_errors_and_success(self):
        for method,effect in (('vendor.state','read'),('vendor.list','query'),('vendor.buy','mutation')):
            with self.subTest(method=method),m.Store(self.folder/method) as store:
                runner=self.runner(store)
                runner.request(dict(id='first',method=method,params={}))
                self.assertEqual(store.load()['last']['effect'],effect)
                self.rpc.hook=lambda name,_:(_ for _ in ()).throw(m.Error('lost')) if name==method else None
                runner.request(dict(id='second',method=method,params={}))
                self.assertEqual(store.load()['last']['effect'],effect)
                self.assertEqual(store.load()['last']['error'],'lost')
                self.assertEqual(len(store.load()['pending']),0 if effect=='read' else 1)
                self.rpc.hook=lambda *_:None

    def test_bounded_module_source_and_retained_fallback_are_separate_from_json_frame_bound(self):
        self.source.write_text('return {} --GOOD\n'+' '*(70*1024))
        with m.Store(self.folder) as store:
            runner=self.runner(store);runner.tick();runner.tick()
            revision=runner.data['goodRevision'];cached=store.folder/('policy-'+revision+'.lua')
            self.assertGreater(cached.stat().st_size,m.MAX_STATE)
            self.assertLess(cached.stat().st_size,m.MAX_SOURCE)
            self.source.write_text('BROKEN');runner.tick()
            self.assertEqual(runner.data['goodRevision'],revision)
            self.assertTrue(runner.data['policyError'])
            with self.assertRaisesRegex(m.Error,'json_size_bound'):m.packed({'large':'x'*m.MAX_INPUT},m.MAX_INPUT)
        self.source.write_text('x'*(m.MAX_SOURCE+1))
        with m.Store(self.folder/'oversize') as store:
            with self.assertRaisesRegex(m.Error,'no_valid_policy'):self.runner(store).tick()

    def shared_claim(self):
        return dict(key='first-qualified', evidence=[
            dict(observation='stock', path='items.0.entry', operator='equals', value=4498),
            dict(observation='stock', path='count', operator='gte', value=10)])

    def test_shared_claim_fresh_predicates_then_immutable_winner_and_next_tick_input(self):
        self.goal['sharedKeys'] = ['first-qualified']
        self.bridge.decide = lambda _: dict(memory={}, phase='claiming', sharedClaim=self.shared_claim())
        with m.Store(self.folder) as store:
            runner = self.runner(store)
            with self.assertRaisesRegex(m.Error, 'predicate_not_observed'): runner.tick()
            self.assertFalse(self.shared.folder.exists())
            self.rpc.count = 10; runner.tick()
            self.assertEqual(self.bridge.inputs[-1]['goal']['sharedKeys'], ['first-qualified'])
            record = self.shared.read('first-qualified')
            self.assertEqual(record['winnerGuid'], '87'); self.assertEqual(record['sessionId'], 'aw-test')
            self.assertEqual(record['goalId'], 'supplies')
            self.assertEqual(record['observationSHA256'], {'stock': m.hashlib.sha256(m.packed(
                {'count': 10, 'items': [{'entry': 4498}]})).hexdigest()})
            path = self.shared.folder / 'first-qualified.json'; before = path.read_bytes()
            runner.tick()
            self.assertEqual(self.bridge.inputs[-1]['shared']['first-qualified'], record)
            self.assertEqual(path.read_bytes(), before)
            self.assertTrue(runner.data['last']['won'])
            with self.assertRaisesRegex(m.Error, 'task_stopped'):
                self.shared.claim('other', 1, 'old', 'goal', [], {}, lambda: (_ for _ in ()).throw(m.Error('task_stopped')))
            self.assertFalse((self.shared.folder / 'other.json').exists())
        self.assertEqual(self.purchases(), [])

    def test_shared_gte_excludes_boolean_string_and_missing_fresh_observations(self):
        self.goal['sharedKeys'] = ['first-qualified']
        self.bridge.decide = lambda _: dict(memory={}, phase='claiming', sharedClaim=self.shared_claim())
        with m.Store(self.folder) as store:
            runner = self.runner(store)
            for count in (True, '10', 9):
                self.rpc.count = count
                with self.assertRaisesRegex(m.Error, 'predicate_not_observed'): runner.tick()
            self.rpc.hook = lambda method, _: (_ for _ in ()).throw(m.Error('read_failed')) if method == 'vendor.state' else None
            with self.assertRaisesRegex(m.Error, 'fresh_shared_observation'): runner.tick()
            self.assertFalse(self.shared.folder.exists())

    def test_shared_competing_claims_exactly_one_winner_no_overwrite_and_private_files(self):
        entered = threading.Barrier(2)
        evidence = [dict(observation='stock', path='count', operator='gte', value=10)]
        def claim(guid):
            entered.wait(timeout=2)
            return self.shared.claim('race', guid, 'aw-' + str(guid), 'goal', evidence, {'stock': {'count': 10}}, lambda: None)
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            winners = list(pool.map(claim, (1, 4)))
        self.assertEqual(winners[0], winners[1])
        self.assertIn(winners[0]['winnerGuid'], ('1', '4'))
        path = self.shared.folder / 'race.json'; original = path.read_bytes()
        self.shared.claim('race', 8, 'new', 'new-goal', evidence, {'stock': {'count': 99}}, lambda: None)
        self.assertEqual(path.read_bytes(), original)
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        outside = self.root / 'outside'; outside.write_bytes(original); outside.chmod(0o600)
        path.unlink(); path.symlink_to(outside)
        with self.assertRaises((m.Error, OSError)): self.shared.read('race')
        path.unlink(); path.write_bytes(original); path.chmod(0o600)
        self.shared.folder.chmod(0o755)
        with self.assertRaisesRegex(m.Error, 'private_shared'): self.shared.read('race')
        with self.assertRaisesRegex(m.Error, 'shared_key_invalid'): self.shared.read('../escape')

    def test_shared_claim_cannot_mix_game_action_or_survive_body_replacement(self):
        self.goal['sharedKeys'] = ['first-qualified']; self.rpc.count = 10
        with m.Store(self.folder) as store:
            runner = self.runner(store)
            self.bridge.decide = lambda _: dict(self.buy(), sharedClaim=self.shared_claim())
            with self.assertRaisesRegex(m.Error, 'no_valid_policy'): runner.tick()
            self.assertEqual(self.purchases(), [])
            self.source.write_text('return {} --newvalid')
            self.bridge.decide = lambda _: dict(memory={}, phase='claiming', sharedClaim=self.shared_claim())
            save = store.save
            def replace(data):
                save(data)
                if data['phase'] == 'claiming': self.rpc.sid = 'aw-replaced'
            with patch.object(store, 'save', side_effect=replace):
                with self.assertRaisesRegex(m.Error, 'body_changed'): runner.tick()
            self.assertFalse((self.shared.folder / 'first-qualified.json').exists())

    def test_shared_metadata_contention_yields_then_rechecks_fresh_evidence(self):
        self.goal['sharedKeys'] = ['first-qualified']; self.rpc.count = 10
        self.bridge.decide = lambda _: dict(memory={}, phase='claiming', sharedClaim=self.shared_claim())
        with m.Store(self.folder) as store:
            runner = self.runner(store)
            with patch.object(self.shared, 'claim', side_effect=m.Error('task_already_running')):
                runner.tick()
            self.assertEqual(runner.data['phase'], 'shared_claim_waiting_for_lock')
            self.assertFalse(self.shared.folder.exists())
            self.rpc.count = 9
            with self.assertRaisesRegex(m.Error, 'predicate_not_observed'): runner.tick()
            self.assertFalse(self.shared.folder.exists())

    def test_rpc_loopback_only_and_typed_bridge_has_no_shell_or_game_transport(self):
        for url in ('https://127.0.0.1:8/rpc', 'http://localhost:8/rpc', 'http://127.0.0.1/rpc',
                    'http://user@127.0.0.1:8/rpc', 'http://127.0.0.1:8/rpc?forward=yes'):
            with self.assertRaises(m.Error): m.RPC(url)
        bridge = m.Bridge('/fixed/luapolicy', '/fixed/modules')
        with patch.object(m.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, b'{"quantity":1}\n', b'')) as run:
            self.assertEqual(bridge.validate('vendor.buy', {'quantity': 1}), {'quantity': 1})
            self.assertEqual(run.call_args.args[0], ['/fixed/luapolicy', '--mode', 'validate', '--modules', '/fixed/modules', '--method', 'vendor.buy'])
            self.assertNotIn('shell', run.call_args.kwargs)
        self.assertIsNone(m.Store(self.root / 'not-created').load())
        self.assertFalse((self.root / 'not-created').exists())

    @unittest.skipUnless(os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY'), 'optional compiled local typed bridge')
    def test_real_typed_bridge_wrapper_and_descriptor_with_fake_rpc(self):
        self.library.write_text('return {tag="real-library"}')
        self.source.write_text('return {decide=function(s) assert(tasks.tag=="real-library"); '
                               'return {memory={seen=s.observations.own.selfGuid},phase="observed"} end}')
        self.goal = dict(id='bridge', kind='observe', params={}, allow=['buddy-actions.status'],
            observations={'own': dict(method='buddy-actions.status', params={}, select=['sessionId', 'selfGuid'])})
        self.bridge = m.Bridge(os.environ['BUDDY_LUAPOLICY_TEST_BINARY'], m.ROOT / 'modules')
        with m.Store(self.folder) as store:
            runner = self.runner(store); runner.tick()
            self.assertEqual(runner.data['memory'], {'seen': '87'})
            self.assertEqual(runner.data['phase'], 'observed')
        self.assertTrue(all(method == 'buddy-actions.status' for method, _ in self.rpc.calls))


if __name__ == '__main__': unittest.main()
