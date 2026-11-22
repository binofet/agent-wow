import copy
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location('gm_task_observation_subject', Path(__file__).with_name('gm_tasks.py'))
gm = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gm)


class Store:
    def __init__(self, folder):
        self.folder, self.value = Path(folder), None

    def load(self):
        return copy.deepcopy(self.value)

    def save(self, value):
        self.value = copy.deepcopy(value)


class RPC:
    def __init__(self, guid, sid, following=False):
        self.calls, self.sid, self.guid = [], sid, guid
        self.status = dict(selfGuid=guid, sessionId=sid, following=following,
                           position=dict(known=True, mapKnown=True, revision='11'))
        self.world = dict(self=dict(guid=guid, present=True, positionKnown=True, mapKnown=True,
                                   aliveKnown=True, alive=True, healthKnown=True, health=100,
                                   maxHealth=100, powerKnown=True, maxPower=100,
                                   targetKnown=True, targetGuid='3', positionRevision='7'))
        self.party = dict(sessionId=sid, group=dict(known=True, inGroup=True, leaderGuid='1',
                         members=[dict(guid='1', name='Arlen', online=True), dict(guid='3', name='Joel', online=True)]))
        self.source, self.revision = 'return {decide=function() return {} end}', 'original'

    def call(self, method, params):
        self.calls.append((method, copy.deepcopy(params)))
        if method == 'buddy-actions.status':
            return copy.deepcopy(self.status)
        if method == 'buddy-world.snapshot':
            return copy.deepcopy(self.world)
        if method == 'buddy-party.state':
            return copy.deepcopy(self.party)
        if method == 'buddy-actions.plan_route_leg':
            return dict(sessionId=self.sid, goalReached=True, serverPositionRevision='7',
                        endpoint=dict(known=True, x=1))
        if method == 'session.policy_source':
            return dict(source=self.source, revision=self.revision)
        if method == 'session.reload_policy':
            if params['sessionId'] != self.sid or params['expectedRevision'] != self.revision:
                raise ValueError('body or source revision changed')
            self.source, self.revision = params['source'], self.revision + '-next'
            return dict(revision=self.revision)
        if method == 'buddy-actions.follow':
            self.status['following'] = params['enabled']
            self.status.update(moving=False,motionUnconfirmed=False,
                               reason='follow_enabled_waiting_for_safe_observations' if params['enabled'] else 'follow_disabled')
            return copy.deepcopy(self.status)
        raise AssertionError('unexpected RPC: ' + method)


class GMTaskObservationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.gm = RPC('87', 'aw-gm')
        self.member = RPC('1', 'aw-arlen', following=True)
        self.gm.world['owner'] = dict(guid='3', present=True, positionKnown=True, mapKnown=True,
                                      aliveKnown=True, alive=False, healthKnown=True, maxHealth=100)
        self.goal = dict(id='observation-test', destination=dict(mapId=0, x=1, y=0, z=0),
                         members=[dict(guid='1', name='Arlen', rpcUrl='http://127.0.0.1:8086/rpc'),
                                  dict(guid='3', name='Joel')])
        self.host = gm.Host(Store(self.temp.name), self.gm, object(), object(), self.goal,
                            Path(__file__).parents[1] / 'behaviors/gm-recovery.lua',
                            Path(__file__).parents[1] / 'behaviors/gm-recovery-hold.lua')
        self.host.member_rpcs = {'1': self.member}

    def test_protojson_known_zero_pose_vitals_and_mana_remain_known(self):
        p = gm.normalized(dict(guid='3', present=True, positionKnown=True, mapKnown=True,
                               healthKnown=True, maxHealth=100, powerKnown=True, maxPower=200))
        self.assertEqual((p['mapId'], p['x'], p['y'], p['z']), (0, 0, 0, 0))
        self.assertEqual((p['hp'], p['mana'], p['manaUser']), (0, 0, True))
        self.assertTrue(p['hpKnown'] and p['manaKnown'])
        unknown = gm.normalized(dict(guid='3', present=True))
        self.assertFalse(unknown['positionKnown'])
        self.assertIsNone(unknown['hp'])
        self.assertIsNone(unknown['mana'])

    def test_selection_transport_and_server_observation_remain_distinct_facts(self):
        self.host.data['selection'] = dict(sessionId='aw-gm', targetGuid='5', transportWritten=True)
        selection = self.host.observe()['selection']
        self.assertTrue(selection['observedTargetKnown'])
        self.assertEqual(selection['observedTargetGuid'], '3')
        self.assertTrue(selection['transportWritten'])
        self.assertEqual(selection['targetGuid'], '5')
        self.assertFalse(selection.get('known', False))

    def test_zero_selection_and_foreign_session_receipts_are_not_invented(self):
        self.gm.world['self'].pop('targetGuid')
        self.assertEqual(self.host.observe()['selection']['targetGuid'], '0')
        self.gm.world['self']['targetKnown'] = False
        self.host.data['selection'] = dict(sessionId='aw-old', targetGuid='5', transportWritten=True)
        selection = self.host.observe()['selection']
        self.assertFalse(selection['known'])
        self.assertFalse(selection.get('transportWritten', False))

    def test_route_distinguishes_client_and_server_position_revisions(self):
        self.host.goal['walkTarget'] = dict(self.goal['destination'])
        observed = self.host.observe()
        queries = [params for method, params in self.gm.calls if method == 'buddy-actions.plan_route_leg']
        self.assertTrue(queries)
        self.assertEqual(queries[-1]['expectedPositionRevision'], '11')
        self.assertEqual(observed['gm']['observationRevision'], '7')
        self.assertEqual(observed['destinationRoute']['endpoint']['mapId'], 0)

    def test_teleport_observations_do_not_query_a_walkable_corridor(self):
        self.host.observe()
        for rpc in (self.gm, self.member):
            self.assertFalse(any(m == 'buddy-actions.plan_route_leg' for m, _ in rpc.calls))

    def test_real_faction_only_creature_keeps_raw_classification_facts(self):
        self.gm.world['nearbyCreatures'] = [dict(guid='17379390969991956137', present=True,
            positionKnown=True, mapKnown=True, aliveKnown=True, alive=True, factionTemplate=67,
            attackVictimKnown=True, attackVictimGuid='3')]
        threat = self.host.observe()['threats'][0]
        self.assertEqual(threat['factionTemplate'], 67)
        self.assertTrue(threat['engaged'])
        self.assertNotIn('hostile', threat)

    def test_member_rpc_identity_mismatch_never_becomes_an_observation(self):
        self.member.party['sessionId'] = 'aw-replaced'
        with self.assertRaisesRegex(ValueError, 'identity'):
            self.host.observe()

    def test_hold_and_restore_preserve_native_follow_opt_in_and_policy(self):
        source = self.member.source
        self.host.install_holds()
        self.assertFalse(self.member.status['following'])
        self.assertNotEqual(self.member.source, source)
        self.assertEqual(self.host.restore_holds(), [])
        self.assertTrue(self.member.status['following'])
        self.assertEqual(self.member.source, source)
        self.assertEqual([p['enabled'] for m, p in self.member.calls if m == 'buddy-actions.follow'], [False, True])

    def test_independent_policy_edit_is_not_overwritten_on_restore(self):
        self.host.install_holds()
        self.member.source, self.member.revision = 'independent owner edit', 'owner-revision'
        errors = self.host.restore_holds()
        self.assertTrue(errors)
        self.assertEqual(self.member.source, 'independent owner edit')

    def test_own_gm_hold_stops_motion_after_cas_without_joining_roster(self):
        source=self.gm.source
        self.gm.status['moving']=True
        members=copy.deepcopy(self.goal['members'])
        self.host.install_holds()
        self.assertEqual(set(self.host.data['holds']),{'87','1'})
        self.assertNotEqual(self.gm.source,source)
        self.assertFalse(self.gm.status['following'])
        self.assertFalse(self.gm.status['moving'])
        methods=[m for m,_ in self.gm.calls]
        self.assertLess(methods.index('session.reload_policy'),methods.index('buddy-actions.follow'))
        self.assertEqual(self.host.data['holds']['87']['movementStop']['outcome'],'written')
        self.host.install_holds()
        self.assertEqual(len([m for m,_ in self.gm.calls if m=='buddy-actions.follow']),1)
        observed=self.host.observe()
        self.assertEqual(self.goal['members'],members)
        self.assertEqual([m['guid'] for m in observed['members']],['1','3'])
        self.assertEqual([m['guid'] for m in self.host.data['originalGroup']['members']],['1','3'])
        self.assertEqual(self.host.restore_holds(),[])
        self.assertEqual(self.gm.source,source)
        self.assertFalse(self.gm.status['following'])
        self.assertTrue(self.host.data['holds']['87']['restored'])

    def test_unknown_gm_stop_is_reserved_once_and_does_not_dispatch_task(self):
        original=self.gm.call
        def lost_stop(method,params):
            result=original(method,params)
            if method=='buddy-actions.follow':
                self.assertEqual(self.host.store.value['holds']['87']['movementStop']['outcome'],'reserved')
                raise TimeoutError('stop response lost')
            return result
        self.gm.call=lost_stop
        self.host.step=lambda: self.fail('task must not dispatch before own-body stop')
        with self.assertRaisesRegex(TimeoutError,'response lost'):self.host.run()
        with self.assertRaisesRegex(ValueError,'stop outcome is unconfirmed'):self.host.run()
        self.assertEqual(len([m for m,_ in self.gm.calls if m=='buddy-actions.follow']),1)
        self.assertEqual(self.host.data['reservations'],{})
        self.assertEqual(self.host.data['holds']['87']['movementStop']['outcome'],'unconfirmed')
        self.assertEqual(self.host.restore_holds(),[])

    def test_gm_independent_policy_edit_is_not_overwritten_on_restore(self):
        self.host.install_holds()
        self.gm.source,self.gm.revision='new GM owner source','owner-revision'
        self.assertTrue(self.host.restore_holds())
        self.assertEqual(self.gm.source,'new GM owner source')
        self.assertFalse(self.host.data['holds']['87'].get('restored',False))

    def test_lost_gm_hold_ack_reconciles_source_before_one_stop(self):
        original=self.gm.call
        lost=[True]
        def lost_reload(method,params):
            result=original(method,params)
            if method=='session.reload_policy' and lost[0]:
                lost[0]=False
                raise TimeoutError('reload response lost')
            return result
        self.gm.call=lost_reload
        with self.assertRaisesRegex(TimeoutError,'reload response lost'):self.host.install_holds()
        self.assertFalse(any(m=='buddy-actions.follow' for m,_ in self.gm.calls))
        self.host.install_holds()
        self.assertEqual(len([m for m,_ in self.gm.calls if m=='session.reload_policy']),1)
        self.assertEqual(len([m for m,_ in self.gm.calls if m=='buddy-actions.follow']),1)

    def partial_hold_clients(self, abort=False):
        self.goal['members'].extend([
            dict(guid='4',name='Mira',rpcUrl='http://127.0.0.1:8087/rpc'),
            dict(guid='5',name='Rook',rpcUrl='http://127.0.0.1:8088/rpc'),
            dict(guid='8',name='Nixwick',rpcUrl='http://127.0.0.1:8089/rpc')])
        clients={'87':self.gm,'1':self.member,'4':RPC('4','aw-mira',True),
                 '5':RPC('5','aw-rook',True),'8':RPC('8','aw-nix',True)}
        def unavailable(method,params):
            clients['8'].calls.append((method,copy.deepcopy(params)))
            if abort:self.host.stopping=True
            raise TimeoutError('member 8 RPC unavailable')
        clients['8'].call=unavailable
        self.host.member_rpcs={g:r for g,r in clients.items() if g!='87'}
        self.host.data['identity']=dict(sessionId=self.gm.sid,guid='87',rpcUrl='http://127.0.0.1:8090/rpc')
        self.host.transport,self.host.bridge=Mock(),Mock()
        return clients,{g:r.source for g,r in clients.items()}

    def test_cancel_partial_holds_never_installs_or_observes_unheld_unavailable_member(self):
        clients,original=self.partial_hold_clients()
        goal=copy.deepcopy(self.goal)
        with self.assertRaisesRegex(TimeoutError,'member 8'):self.host.run()
        self.assertEqual(set(self.host.data['holds']),{'87','1','4','5'})
        self.assertEqual(self.host.data['reservations'],{})
        self.assertEqual(self.host.data['memory'],{})
        self.assertIsNone(self.host.data['last'])
        (self.host.store.folder/'cancel.json').write_text('{}')
        out=self.host.run()
        self.assertTrue(out['cancelled'] and out['cleanupComplete'])
        self.assertFalse(out['done'] or out['success'] or out['resumable'])
        self.assertEqual(len(clients['8'].calls),1,'cleanup retried the unheld client')
        for guid in ('87','1','4','5'):
            self.assertEqual(clients[guid].source,original[guid])
            self.assertTrue(self.host.data['holds'][guid]['restored'])
            reloads=[p for m,p in clients[guid].calls if m=='session.reload_policy']
            self.assertEqual(len(reloads),2,'cleanup installed another hold')
            self.assertEqual(reloads[-1]['source'],original[guid])
        self.assertEqual([p['enabled'] for m,p in self.member.calls if m=='buddy-actions.follow'],[False,True])
        self.assertEqual(self.host.data['goal'],goal)
        self.host.bridge.evaluate.assert_not_called()
        self.host.transport.gm.assert_not_called();self.host.transport.select.assert_not_called()

    def test_abort_during_partial_hold_failure_restores_only_recorded_holds(self):
        clients,original=self.partial_hold_clients(abort=True)
        out=self.host.run()
        self.assertTrue(out['cancelled'] and out['cleanupComplete'])
        self.assertEqual(len(clients['8'].calls),1)
        for guid in ('87','1','4','5'):self.assertEqual(clients[guid].source,original[guid])
        self.host.bridge.evaluate.assert_not_called()
        self.host.transport.gm.assert_not_called();self.host.transport.select.assert_not_called()

    def test_cli_partial_cancel_releases_only_same_task_writer_and_preserves_invocation(self):
        self.gm.sid=self.gm.status['sessionId']=self.gm.party['sessionId']='aw-123'
        clients,_=self.partial_hold_clients()
        with self.assertRaises(TimeoutError):self.host.run()
        root=self.host.store.folder.resolve();state=root/'runtime';goal=root/'goal.json';config=root/'transport.json'
        goal.write_text(json.dumps(self.goal));cfg=dict(writerRoot=str(root/'writers'));config.write_text(json.dumps(cfg))
        args=['run','--state-dir',str(state),'--goal',str(goal),'--transport',str(config),'--rpc-url','http://127.0.0.1:8090/rpc']
        invocation=dict(rpcUrl=args[-1],goal=str(goal),transport=str(config),
            policy=str(gm.ROOT/'behaviors/gm-recovery.lua'),policyBinary=str(gm.ROOT/'data/bin/luapolicy'))
        original_invocation=json.dumps(invocation).encode()
        with gm.task_store(state) as store:
            store.save(self.host.data);store.write('invocation.json',original_invocation,gm.runtime.MAX_INPUT)
        writer=gm.task_store(gm.writer_folder(root,'aw-123',cfg))
        with writer:gm.claim_writer(writer,state)
        (state/'cancel.json').write_text('{}')
        by_url={m['rpcUrl']:clients[str(m['guid'])] for m in self.goal['members'] if m.get('rpcUrl')}
        by_url[args[-1]]=self.gm;transport,bridge=Mock(),Mock()
        with patch.object(gm,'task_rpc',side_effect=lambda url:by_url[url]),patch.object(gm,'SessionTransport',return_value=transport),patch.object(gm,'task_bridge',return_value=bridge),contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(gm.main(args),1) # cancellation is not a success claim
        saved=gm.task_store(state).load()
        self.assertTrue(saved['cancelled'] and saved['cleanupComplete'])
        self.assertFalse(writer.load()['active'])
        self.assertEqual(writer.load()['stateDir'],str(state))
        self.assertEqual((state/'invocation.json').read_bytes(),original_invocation)
        self.assertEqual(saved['goal'],self.goal)
        self.assertEqual(len(clients['8'].calls),1)
        self.assertTrue(all(r['restored'] for r in saved['holds'].values()))
        bridge.evaluate.assert_not_called();transport.gm.assert_not_called();transport.select.assert_not_called()

    def test_partial_cancel_preserves_independently_changed_source_and_resumable_custody(self):
        clients,_=self.partial_hold_clients()
        with self.assertRaises(TimeoutError):self.host.run()
        clients['4'].source,clients['4'].revision='new actor source','actor-revision'
        (self.host.store.folder/'cancel.json').write_text('{}')
        out=self.host.run()
        self.assertFalse(out['cleanupComplete'] or out['success'])
        self.assertTrue(out['resumable'])
        self.assertEqual(out['error'],'policy_restore_incomplete')
        self.assertEqual(clients['4'].source,'new actor source')
        self.assertFalse(self.host.data['holds']['4'].get('restored',False))
        self.assertEqual(len(clients['8'].calls),1)
        self.host.transport.gm.assert_not_called()

    def test_partial_cancel_rejects_replaced_gm_before_restoring_any_hold(self):
        clients,_=self.partial_hold_clients()
        with self.assertRaises(TimeoutError):self.host.run()
        (self.host.store.folder/'cancel.json').write_text('{}')
        self.gm.status['sessionId']=self.gm.party['sessionId']='aw-replaced'
        with self.assertRaisesRegex(ValueError,'current GM task body'):self.host.run()
        self.assertFalse(any(r.get('restored') for r in self.host.data['holds'].values()))
        self.assertTrue(all(len([m for m,_ in clients[g].calls if m=='session.reload_policy'])==1 for g in ('87','1','4','5')))
        self.host.transport.gm.assert_not_called()

    def test_unknown_game_reservation_cannot_use_predispatch_cleanup(self):
        clients,_=self.partial_hold_clients()
        with self.assertRaises(TimeoutError):self.host.run()
        receipt=dict(id='task:1',outcome='unconfirmed',sessionId='aw-gm')
        self.host.data['reservations']={'task:1':copy.deepcopy(receipt)}
        self.host.data['last']=copy.deepcopy(receipt)
        (self.host.store.folder/'cancel.json').write_text('{}')
        with self.assertRaises(TimeoutError):self.host.run()
        self.assertEqual(self.host.data['reservations']['task:1'],receipt)
        self.assertEqual(self.host.data['last'],receipt)
        self.assertFalse(any(r.get('restored') for r in self.host.data['holds'].values()))
        self.host.transport.gm.assert_not_called();self.host.transport.select.assert_not_called()


if __name__ == '__main__':
    unittest.main()
