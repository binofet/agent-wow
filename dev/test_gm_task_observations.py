import copy
import importlib.util
from pathlib import Path
import tempfile
import unittest

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
        observed = self.host.observe()
        queries = [params for method, params in self.gm.calls if method == 'buddy-actions.plan_route_leg']
        self.assertTrue(queries)
        self.assertEqual(queries[-1]['expectedPositionRevision'], '11')
        self.assertEqual(observed['gm']['observationRevision'], '7')
        self.assertEqual(observed['destinationRoute']['endpoint']['mapId'], 0)

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


if __name__ == '__main__':
    unittest.main()
