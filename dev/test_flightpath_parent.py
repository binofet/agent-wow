"""Real TaskRuntime/Lua decisions with synthetic public RPC facts, no sockets."""
import copy
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
def load(name, filename):
    loader = importlib.machinery.SourceFileLoader(name, str(ROOT / 'dev' / filename))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    module = importlib.util.module_from_spec(spec); loader.exec_module(module)
    return module
host = load('flightpath_parent_host', 'task-runtime')
bundle = load('flightpath_parent_bundle', 'flightpath-bundle')
BINARY = os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY')
NPC = '17379390914641920001'


class ParentRPC:
    url = 'http://127.0.0.1:8090/rpc'
    def __init__(self):
        self.calls, self.lost, self.tokens = [], set(), 0
        self.actions = dict(sessionId='aw-parent', selfGuid='87')
        self.world = dict(visitRevision='1', selfCopperKnown=True, selfCopper=10000, selfCopperRevision='1',
            self=dict(guid='87', present=True, aliveKnown=True, alive=True, healthKnown=True, health=100, maxHealth=100,
                positionKnown=True, positionRevision='1',
                mapKnown=True, mapId=0, unitFlagsKnown=True, movementFlagsKnown=True, channelSpellKnown=True,
                channelObjectKnown=True),
            nearbyCreatures=[dict(guid=NPC, present=True, aliveKnown=True, alive=True, positionKnown=True,
                positionRevision='1', mapKnown=True, npcFlags=8192)])
        self.flight = dict(sessionId='aw-parent', selfGuid='87', visitId='1', visitBound=True, revision='1',
            operation=dict(revision='1', sessionId='aw-parent', selfGuid='87', visitId='1', npcGuid=NPC,
                mode='discover', phase='menu_observed', menuKnown=True, transportWritten=True, sourceNode=1, knownNodes=[1, 2]))
        self.token = None

    def count(self, method): return sum(m == method for m, _ in self.calls)

    def call(self, method, params, timeout=5):
        self.calls.append((method, copy.deepcopy(params)))
        if method == 'buddy-actions.status': return copy.deepcopy(self.actions)
        if method == 'buddy-world.snapshot': return copy.deepcopy(self.world)
        if method == 'buddy-actions.flightpath_observation': return copy.deepcopy(self.flight)
        if method == 'buddy-actions.acquire_idle_interaction':
            assert params['sessionId'] == 'aw-parent' and params['selfGuid'] == '87'
            assert params['visitId'] == self.world['visitRevision'] and params['owner'] == 'exploration'
            if params.get('token'): assert params['token'] == self.token
            else:
                self.tokens += 1; self.token = 'parent-token-' + str(self.tokens)
            self.actions.update(interactionOwner='exploration', interactionLeaseRemainingMs='15000')
            response = dict(token=self.token, state=copy.deepcopy(self.actions))
        elif method == 'buddy-actions.release_interaction':
            assert params['token'] == self.token and params['sessionId'] == 'aw-parent'
            assert params['selfGuid'] == '87' and params['visitId'] == self.world['visitRevision']
            self.token = None
            self.actions.pop('interactionOwner', None); self.actions.pop('interactionLeaseRemainingMs', None)
            response = copy.deepcopy(self.actions)
        elif method == 'buddy-actions.flightpath_reference':
            node = params['nodeId']
            response = dict(referenceOnly=True, nodeFound=True, node=dict(nodeId=node),
                info=dict(claimedBuild=12340, nameLocale='enUS'), outgoingPaths=[] if node == 3 else [dict(
                    pathId=node, sourceNode=node, destinationNode=node+1, endpointsKnown=True, referencePriceKnown=True, referenceBaseCopper=10)])
        elif method == 'buddy-actions.query_flightpath':
            revision = str(int(params.get('expectedRevision', '0')) + 1)
            op = dict(params, revision=revision, transportWritten=True, phase='status_observed', statusKnown=True, nodeKnown=True)
            if params['mode'] != 'status':
                op.update(phase='menu_observed', menuKnown=True, sourceNode=2 if params['mode'] == 'arrival' else 1, knownNodes=[1, 2])
            self.flight.update(revision=revision, operation=op)
            response = copy.deepcopy(self.flight)
        elif method == 'buddy-actions.activate_flightpath':
            self.flight.update(activationRevision=str(int(params.get('expectedRevision', '0'))+1), activation=dict(
                params, revision=str(int(params.get('expectedRevision', '0'))+1), pending=True, phase='accepted_unsettled',
                activationAccepted=True, replyKnown=True, transportWritten=True,
                acceptanceScope=dict(connectionId='synthetic-connection', visitRevision='1', motionRevision='1')))
            response = copy.deepcopy(self.flight)
        else: raise AssertionError('unexpected public method ' + method)
        if method in self.lost:
            self.lost.remove(method)
            raise host.Error('rpc_result_unconfirmed')
        return response


class FlightpathParentGoalTests(unittest.TestCase):
    def test_explicit_public_goal_has_current_facts_and_no_legacy_acquisition(self):
        goal = bundle.parent_goal('flight-1', 2, False)
        self.assertEqual(host.checked_goal(goal), goal)
        self.assertFalse(goal['params']['acceptUnknownFare'])
        self.assertNotIn('buddy-actions.acquire_interaction', goal['allow'])
        self.assertIn('interactionLeaseRemainingMs', goal['observations']['actions']['select'])
        self.assertNotIn('explorationToken', goal['params'])
        for args in (('', 2), ('id', 0), ('id', True)):
            with self.assertRaises(ValueError): bundle.parent_goal(*args)
        result = subprocess.run([sys.executable, '-B', str(ROOT/'dev/flightpath-bundle'), '--parent-goal',
                                 '--id', 'flight-1', '--destination-node', '2'], capture_output=True, check=True)
        self.assertEqual(host.decoded(result.stdout), goal)


@unittest.skipUnless(BINARY, 'compiled local typed bridge not requested')
class FlightpathParentRuntimeTests(unittest.TestCase):
    def setUp(self):
        folder = tempfile.TemporaryDirectory(); self.addCleanup(folder.cleanup)
        self.root = Path(folder.name).resolve(); self.source = self.root/'parent.lua'
        self.source.write_text(bundle.compose(ROOT/'behaviors', task_runtime=True, parent=True))
        self.goal = bundle.parent_goal('parent-1', 2, True)
        self.bridge, self.rpc = host.Bridge(BINARY, ROOT/'modules'), ParentRPC()
        self.store = host.Store(self.root/'state'); self.store.__enter__()
        self.addCleanup(self.store.__exit__, None, None, None)
        self.reopen()

    def reopen(self):
        self.runner = host.Runner(self.store, self.bridge, self.rpc, 87, 'aw-parent', self.goal,
                                  self.source, ROOT/'behaviors/task-stack.lua')

    def until(self, method, count=1):
        for _ in range(30):
            if self.rpc.count(method) >= count: return
            self.runner.tick()
        self.fail('finite parent fixture did not call ' + method + ': ' + self.runner.data['phase'])

    def test_real_runner_parent_acquires_discovers_and_books_once(self):
        self.rpc.flight.update(revision='0', operation={})
        self.until('buddy-actions.activate_flightpath')
        for _ in range(4): self.runner.tick()
        self.assertEqual(self.rpc.count('buddy-actions.acquire_idle_interaction'), 1)
        self.assertEqual(self.rpc.count('buddy-actions.query_flightpath'), 2)
        self.assertEqual(self.rpc.count('buddy-actions.activate_flightpath'), 1)
        self.assertFalse(self.runner.data['done'])
        self.assertNotIn('explorationToken', self.goal['params'])
        self.assertEqual(self.runner.data['memory']['stack'][0]['parentToken'], 'parent-token-1')
        self.assertTrue(all(m != 'buddy-actions.acquire_interaction' for m, _ in self.rpc.calls))

    def test_known_child_reply_survives_renewal_and_source_reopen(self):
        self.until('buddy-actions.flightpath_reference')
        child_reply = copy.deepcopy(self.runner.data['last'])
        self.rpc.actions['interactionLeaseRemainingMs'] = '3000'
        self.until('buddy-actions.acquire_idle_interaction', 2)
        self.assertEqual(self.runner.data['memory']['flightpathParent']['childReply'], child_reply)
        self.source.write_text(self.source.read_text()+'\n-- same-session editable parent revision\n')
        self.reopen(); self.until('buddy-actions.activate_flightpath')
        self.assertEqual(self.rpc.count('buddy-actions.acquire_idle_interaction'), 2)
        self.assertEqual(self.rpc.count('buddy-actions.activate_flightpath'), 1)
        self.assertEqual(self.rpc.tokens, 1)

    def test_lost_acquire_stays_unknown_through_expiry_edit_and_reopen(self):
        self.rpc.lost.add('buddy-actions.acquire_idle_interaction')
        self.runner.tick(); original = copy.deepcopy(self.runner.data['pending'])
        self.rpc.actions.pop('interactionOwner', None); self.rpc.actions.pop('interactionLeaseRemainingMs', None)
        self.source.write_text(self.source.read_text()+'\n-- edits never unlock unknown custody\n')
        self.reopen()
        for _ in range(4): self.runner.tick()
        self.assertEqual(self.runner.data['pending'], original)
        self.assertEqual(self.rpc.count('buddy-actions.acquire_idle_interaction'), 1)
        self.assertEqual(self.rpc.count('buddy-actions.activate_flightpath'), 0)
        self.assertEqual(self.runner.data['phase'], 'flight_parent_unknown_custody_no_replay')

    def test_lost_renewal_retains_child_reply_and_never_reacquires(self):
        self.until('buddy-actions.flightpath_reference')
        original_reply = copy.deepcopy(self.runner.data['last'])
        self.rpc.actions['interactionLeaseRemainingMs'] = '3000'
        self.rpc.lost.add('buddy-actions.acquire_idle_interaction')
        self.until('buddy-actions.acquire_idle_interaction', 2)
        original_pending = copy.deepcopy(self.runner.data['pending'])
        self.rpc.actions.pop('interactionOwner', None); self.rpc.actions.pop('interactionLeaseRemainingMs', None)
        self.reopen()
        for _ in range(3): self.runner.tick()
        self.assertEqual(self.runner.data['pending'], original_pending)
        self.assertEqual(self.runner.data['memory']['flightpathParent']['childReply'], original_reply)
        self.assertEqual(self.rpc.count('buddy-actions.acquire_idle_interaction'), 2)
        self.assertEqual(self.rpc.count('buddy-actions.activate_flightpath'), 0)

    def test_existing_foreign_task_and_contradictory_visit_remain_untouched(self):
        original = dict(stack=[dict(kind='encounter_task', stage='hold')], actorOwned=dict(revision='private-fixture'))
        self.runner.data['memory'] = copy.deepcopy(original); self.runner.persist()
        self.runner.tick()
        self.assertEqual(self.runner.data['memory']['stack'], original['stack'])
        self.assertEqual(self.runner.data['memory']['actorOwned'], original['actorOwned'])
        self.assertEqual(self.runner.data['phase'], 'flight_parent_existing_task_requires_its_owner')
        self.rpc.flight['visitId'] = '2'
        self.runner.tick()
        self.assertEqual(self.runner.data['phase'], 'flight_parent_binding_unavailable')
        self.assertEqual(self.rpc.count('buddy-actions.acquire_idle_interaction'), 0)

    def test_new_task_does_not_adopt_an_existing_native_booking(self):
        self.rpc.flight['activation'] = dict(pending=True, activationAccepted=True, replyKnown=True,
                                              revision='3', phase='accepted_unsettled')
        original = copy.deepcopy(self.rpc.flight['activation'])
        for _ in range(3): self.runner.tick()
        self.assertEqual(self.runner.data['phase'], 'flight_parent_existing_booking_requires_its_owner')
        self.assertEqual(self.rpc.flight['activation'], original)
        self.assertEqual(self.rpc.count('buddy-actions.acquire_idle_interaction'), 0)
        self.assertEqual(self.rpc.count('buddy-actions.activate_flightpath'), 0)

    def test_unbooked_same_map_visit_replacement_preserves_old_task_and_custody(self):
        self.until('buddy-actions.flightpath_reference')
        before = copy.deepcopy(self.runner.data['memory'])
        mutations = [(m, p) for m, p in self.rpc.calls if m != 'buddy-actions.status' and m != 'buddy-world.snapshot'
                     and m != 'buddy-actions.flightpath_observation' and m != 'buddy-actions.flightpath_reference']
        self.rpc.world['visitRevision'] = '2'; self.rpc.flight['visitId'] = '2'
        for _ in range(3): self.runner.tick()
        after = self.runner.data['memory']
        self.assertEqual(after['stack'], before['stack'])
        self.assertEqual(after['flightpathParent']['visitId'], '1')
        self.assertEqual(after['flightpathParent']['lease'], before['flightpathParent']['lease'])
        self.assertEqual(self.runner.data['phase'], 'flight_parent_unbooked_visit_replaced')
        current = [(m, p) for m, p in self.rpc.calls if m != 'buddy-actions.status' and m != 'buddy-world.snapshot'
                   and m != 'buddy-actions.flightpath_observation' and m != 'buddy-actions.flightpath_reference']
        self.assertEqual(current, mutations)
        self.assertEqual(self.rpc.count('buddy-actions.activate_flightpath'), 0)

    def test_unbooked_visit_fence_precedes_failed_or_missing_child_reply(self):
        self.rpc.lost.add('buddy-actions.flightpath_reference')
        self.until('buddy-actions.flightpath_reference')
        before = copy.deepcopy(self.runner.data['memory'])
        self.assertEqual(self.runner.data['last']['effect'], 'read')
        self.assertIn('error', self.runner.data['last'])
        self.assertFalse(self.runner.data['pending'])
        self.rpc.world['visitRevision'] = '2'; self.rpc.flight['visitId'] = '2'
        for missing in (False, True):
            if missing: self.runner.data['last'] = None; self.runner.persist()
            self.runner.tick()
            self.assertEqual(self.runner.data['memory']['stack'], before['stack'])
            self.assertEqual(self.runner.data['memory']['flightpathParent']['lease'], before['flightpathParent']['lease'])
            self.assertEqual(self.runner.data['phase'], 'flight_parent_unbooked_visit_replaced')
        self.assertEqual(self.rpc.count('buddy-actions.acquire_idle_interaction'), 1)
        self.assertEqual(self.rpc.count('buddy-actions.release_interaction'), 0)
        self.assertEqual(self.rpc.count('buddy-actions.flightpath_reference'), 1)
        self.assertEqual(self.rpc.count('buddy-actions.activate_flightpath'), 0)

    def test_explicit_fare_refusal_and_foreign_custody_do_not_book_or_steal(self):
        self.goal['params']['acceptUnknownFare'] = False; self.reopen()
        for _ in range(9): self.runner.tick()
        self.assertEqual(self.rpc.count('buddy-actions.activate_flightpath'), 0)
        self.assertEqual(self.runner.data['phase'], 'unknown_fare_not_accepted')
        self.rpc.actions['interactionOwner'] = 'trade'
        for _ in range(2): self.runner.tick()
        self.assertEqual(self.runner.data['phase'], 'flight_parent_foreign_custody')
        self.assertEqual(self.rpc.count('buddy-actions.release_interaction'), 0)
        self.assertEqual(self.rpc.count('buddy-actions.acquire_idle_interaction'), 1)

    def test_later_settled_hop_can_restart_only_its_unissued_plan_after_known_lease_lapse(self):
        self.goal['params']['destinationNode'] = 3; self.reopen()
        self.rpc.flight['operation']['knownNodes'] = [1, 2, 3]
        self.until('buddy-actions.activate_flightpath')
        for _ in range(3): self.runner.tick()
        memory = self.runner.data['memory']; old = copy.deepcopy(self.rpc.flight['activation'])
        # Layered fixture: native arrival/settlement is separately proved by the
        # real-service Go journey. Here inject only its exact settled receipt and
        # the maintained child's next unissued frame to test parent continuation.
        self.rpc.flight['activation'].update(pending=False, arrivalKnown=True, phase='observed_arrival')
        self.rpc.flight.update(revision='2', operation=dict(revision='2', sessionId='aw-parent', selfGuid='87',
            visitId='1', npcGuid=NPC, mode='arrival', phase='menu_observed', menuKnown=True,
            transportWritten=True, sourceNode=2, knownNodes=[1, 2, 3], activationRevision='1'))
        memory['stack'][0]['stage'] = 'settled'
        self.runner.persist()
        # Let the real child settle its prior hop, create its next activation,
        # push its actual planner and issue a known reference read before lapse.
        reads = self.rpc.count('buddy-actions.flightpath_reference')
        self.until('buddy-actions.flightpath_reference', reads+1)
        self.assertEqual([f['kind'] for f in self.runner.data['memory']['stack']],
                         ['flightpath_activation', 'flightpath_plan'])
        self.rpc.actions.pop('interactionOwner', None); self.rpc.actions.pop('interactionLeaseRemainingMs', None)
        self.runner.persist(); self.until('buddy-actions.acquire_idle_interaction', 2)
        self.until('buddy-actions.activate_flightpath', 2)
        self.assertEqual(self.rpc.count('buddy-actions.acquire_idle_interaction'), 2)
        self.assertEqual(old['destinationNode'], 2)
        self.assertEqual(self.rpc.flight['activation']['destinationNode'], 3)
        self.assertEqual(self.rpc.flight['activation']['menuRevision'], '2')
        first = next(r for r in self.runner.data['attempts'] if r['method'] == 'buddy-actions.activate_flightpath')
        self.assertEqual(first['outcome'], 'rpc_result')
        self.assertEqual(first['params']['destinationNode'], 2)

    def test_lost_discovery_reply_resolves_exact_native_operation_without_requery(self):
        self.rpc.flight.update(revision='0', operation={})
        self.until('buddy-actions.query_flightpath')
        self.rpc.lost.add('buddy-actions.query_flightpath')
        self.until('buddy-actions.query_flightpath', 2)
        original = copy.deepcopy(self.runner.data['pending'])
        self.assertEqual(len(original), 1)
        self.reopen(); self.until('buddy-actions.activate_flightpath')
        row = next(r for r in self.runner.data['attempts'] if r['id'] == original[0]['id'])
        self.assertEqual(row['outcome'], 'policy_observation_resolution')
        self.assertEqual(self.rpc.count('buddy-actions.query_flightpath'), 2)

    def test_new_visit_needs_retired_ground_then_releases_old_and_acquires_fresh(self):
        self.until('buddy-actions.activate_flightpath')
        for _ in range(3): self.runner.tick()
        frame = self.runner.data['memory']['stack'][0]
        # This injects qualified child continuation state, not native Core or
        # booking causality. Native real-service proofs live in Go fixtures.
        frame.update(stage='probe', currentVisitId='2')
        self.rpc.world['visitRevision'] = '2'; self.rpc.flight['visitId'] = '2'
        self.runner.persist()
        for _ in range(2): self.runner.tick()
        self.assertEqual(self.rpc.count('buddy-actions.acquire_idle_interaction'), 1)
        self.assertEqual(self.rpc.count('buddy-actions.release_interaction'), 0)
        self.rpc.world['splineCompletion'] = dict(retirementCurrent=True, selfGuid='87', visitRevision='2',
                                                  revision='2', operation=dict(retired=True, visitRevision='2'))
        self.until('buddy-actions.query_flightpath')
        request = next(p for m, p in self.rpc.calls if m == 'buddy-actions.query_flightpath')
        self.assertEqual(request['mode'], 'arrival')
        self.assertEqual(request['token'], 'parent-token-2')
        self.assertEqual(request['visitId'], '2')
        self.assertEqual(frame['parentToken'], 'parent-token-1')
        self.assertEqual(self.rpc.count('buddy-actions.acquire_idle_interaction'), 2)
        self.assertEqual(self.rpc.count('buddy-actions.release_interaction'), 1)
        self.assertEqual(self.rpc.count('buddy-actions.activate_flightpath'), 1)


if __name__ == '__main__': unittest.main()
