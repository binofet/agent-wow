"""Real pure-Lua/native geometry with fake observations and fake paired keys."""
import copy
import hashlib
import importlib.util
import json
import math
from pathlib import Path
import tempfile
import threading
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location('ui_movement_tested', ROOT/'dev/ui_body_movement.py')
m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)


class Clock:
    def __init__(self): self.value = 100.0
    def __call__(self): return self.value
    def sleep(self, duration): self.value += duration


class World:
    def __init__(self):
        self.pose = dict(guid='3', present=True, aliveKnown=True, alive=True,
                         healthKnown=True, health=200, maxHealth=200, unitFlagsKnown=True,
                         movementFlagsKnown=True, positionKnown=True, mapKnown=True,
                         positionRevision='1', orientation=0, speedsKnown=True, runSpeed=7,
                         uiBinding=dict(sessionId='ui-fake-test-body', pid=19323, windowId=3369, qualified=True,
                                        observerSessionId='aw-observer', observerGuid='1'))
        self.calls = 0; self.hook = lambda _: None
    def observe(self):
        self.calls += 1; self.hook(self)
        return copy.deepcopy(self.pose)
    def revision(self): self.pose['positionRevision'] = str(int(self.pose['positionRevision'])+1)


class Actuator:
    def __init__(self, world, clock):
        self.world, self.clock, self.calls = world, clock, []
        self.hook = lambda *_: None
        self.mode = 'normal'
    def press(self, key, ms):
        self.calls.append((key, ms)); self.hook(key, ms)
        p = self.world.pose; orientation = p.get('orientation', 0)
        if key == 'w':
            if self.mode != 'stuck':
                p['x'] = p.get('x', 0)+math.cos(orientation)*7*ms/1000
                p['y'] = p.get('y', 0)+math.sin(orientation)*7*ms/1000
                if self.mode == 'drift': p['y'] += 3
        else:
            sign = 1 if key == 'left' else -1
            if self.mode == 'wrong_turn': sign *= -1
            p['orientation'] = (orientation+sign*math.pi*ms/1000) % (2*math.pi)
        self.world.revision(); self.clock.sleep(ms/1000+.1)
        return dict(pid=19323, window=3369, key=key, heldMs=ms, released=self.mode != 'lost_ack')


def dense(p, goal):
    n = max(1, math.ceil(m.distance(p, goal)))
    points = [dict(zip(('x','y','z'), [a+(b-a)*i/n for a,b in zip(m.xyz(p),m.xyz(goal))])) for i in range(n+1)]
    return dict(ok=True, mapId=p.get('mapId', 0), points=points, endpoint=points[-1])


class ActuatorInventoryTests(unittest.TestCase):
    def test_inventory_cache_does_not_skip_helper_identity_check(self):
        with tempfile.TemporaryDirectory(dir='/private/tmp') as folder:
            helper = Path(folder)/'helper'
            helper.write_bytes(b'owned helper')
            helper.chmod(0o700)
            actuator = m.KeyActuator(19323, 3369, helper,
                                    hashlib.sha256(helper.read_bytes()).hexdigest())
            rows = [{'kCGWindowOwnerPID':19323, 'kCGWindowNumber':3369,
                     'kCGWindowName':'World of Warcraft'}]
            with mock.patch.object(m, 'json_process', return_value=rows) as inventory:
                with mock.patch.object(m.time, 'monotonic', return_value=100):
                    self.assertEqual(actuator.attest(), actuator.attest())
                    self.assertEqual(inventory.call_count, 1)
                    helper.write_bytes(b'changed helper')
                    with self.assertRaisesRegex(m.MovementError, 'ui_helper_changed'):
                        actuator.attest()
                helper.write_bytes(b'owned helper')
                with mock.patch.object(m.time, 'monotonic', return_value=102):
                    actuator.attest()
                    self.assertEqual(inventory.call_count, 2)


class MovementTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(dir='/private/tmp'); self.addCleanup(temp.cleanup)
        self.folder = Path(temp.name)
        self.world, self.clock = World(), Clock()
        self.actuator = Actuator(self.world, self.clock)
        self.backend = self.make()
    def make(self, nav=dense):
        return m.MovementBackend('ui-fake-test-body',19323,3369,self.world.observe,self.folder,'/unused/copied-assets',
                                 actuator=self.actuator,nav=nav,clock=self.clock,sleep=self.clock.sleep)
    def acquire(self):
        return self.backend.call('buddy-actions.acquire_interaction',dict(owner='exploration',leaseMs=30000))['token']
    def travel(self, token, goal):
        return self.backend.call('buddy-actions.travel_to',dict(token=token,mapId=0,arrivalDistance=.5,**goal))
    def test_constructor_is_lazy_and_straight_actual_lua_uses_zero_proto_fields(self):
        self.assertEqual(self.world.calls,0); self.assertEqual(self.actuator.calls,[])
        token=self.acquire(); result=self.travel(token,dict(x=4,y=0,z=0))
        self.assertEqual(result['reason'],'exploration_within_distance')
        self.assertLessEqual(m.distance(result['position'],dict(x=4,y=0,z=0)),.5)
        self.assertTrue(self.actuator.calls); self.assertTrue(all(k=='w' and 20<=ms<=350 for k,ms in self.actuator.calls))
        record=json.loads((self.folder/'movement.json').read_text())
        self.assertIsNone(record['pendingKey']); self.assertTrue(all(r['phase']=='paired_keys_released' for r in record['history']))
        released=self.backend.call('buddy-actions.release_interaction',dict(owner='exploration',token=token))
        self.assertFalse(released['moving']); self.assertEqual(released['interactionOwner'],'')
    def test_actual_lua_left_and_right_turns_are_not_strafes(self):
        for direction in (1,-1):
            with self.subTest(direction=direction):
                self.folder=self.folder/str(direction); self.folder.mkdir(mode=0o700)
                self.world,self.clock=World(),Clock(); self.actuator=Actuator(self.world,self.clock); self.backend=self.make()
                result=self.travel(self.acquire(),dict(x=4,y=2*direction,z=0))
                self.assertEqual(result['reason'],'exploration_within_distance')
                self.assertIn('left' if direction==1 else 'right',[k for k,_ in self.actuator.calls])
                self.assertTrue(all(k in ('w','left','right') for k,_ in self.actuator.calls))
    def test_durable_reservation_precedes_fake_key_and_final_guard_prevents_dispatch(self):
        token=self.acquire()
        def guard(world):
            p=self.folder/'movement.json'
            if p.exists() and json.loads(p.read_text()).get('pendingKey'):
                world.pose['unitFlags']=0x80000
        self.world.hook=guard
        result=self.travel(token,dict(x=4,y=0,z=0))
        self.assertEqual(self.actuator.calls,[]); self.assertIn('unsafe',result['reason'])
        saved=json.loads((self.folder/'movement.json').read_text())
        self.assertEqual(saved['history'][-1]['phase'],'not_dispatched'); self.assertIsNone(saved['pendingKey'])
        self.assertFalse(saved['motionUnconfirmed'])
    def test_fake_actuator_sees_fsynced_pending_reservation(self):
        def inspect(key,ms):
            row=json.loads((self.folder/'movement.json').read_text())['pendingKey']
            self.assertEqual((row['key'],row['heldMs'],row['phase']),(key,ms,'reserved'))
        self.actuator.hook=inspect
        self.assertEqual(self.travel(self.acquire(),dict(x=2,y=0,z=0))['reason'],'exploration_within_distance')
    def test_failed_paired_key_latches_unknown_across_restart_and_never_resends(self):
        self.actuator.mode='lost_ack'; token=self.acquire()
        with self.assertRaisesRegex(m.MovementError,'outcome_unconfirmed'): self.travel(token,dict(x=4,y=0,z=0))
        self.assertEqual(len(self.actuator.calls),1); self.backend=self.make()
        with self.assertRaisesRegex(m.MovementError,'motion_unconfirmed'): self.acquire()
        self.assertEqual(len(self.actuator.calls),1); self.assertTrue(self.backend.status()['motionUnconfirmed'])
    def test_missing_actual_observation_after_released_keys_is_not_movement_confirmation(self):
        token=self.acquire()
        def lose(_):
            if self.actuator.calls: raise ValueError('observer unavailable')
        self.world.hook=lose
        with self.assertRaisesRegex(m.MovementError,'observation_unconfirmed'): self.travel(token,dict(x=4,y=0,z=0))
        self.world.hook=lambda _:None
        self.assertTrue(self.backend.status()['motionUnconfirmed']); self.assertEqual(len(self.actuator.calls),1)
    def test_navigation_failure_or_changed_origin_never_emits_keys(self):
        token=self.acquire()
        def fail(*_): raise m.MovementError('native unsupported floor')
        self.backend.nav=fail
        self.assertIn('unsupported',self.travel(token,dict(x=4,y=0,z=0))['reason']); self.assertEqual(self.actuator.calls,[])
        def change(p,g):
            result=dense(p,g); self.world.pose['x']=1; self.world.revision(); return result
        self.backend.nav=change
        self.assertIn('origin_changed',self.travel(token,dict(x=4,y=0,z=0))['reason']); self.assertEqual(self.actuator.calls,[])
    def test_body_observer_and_map_bindings_and_real_health_block_before_keys(self):
        for change in [dict(guid='1'),dict(health=1),dict(unitFlags=0x80000),dict(channelSpellId=7620),
                       dict(runSpeed=14),dict(movementFlags=0x1000000),dict(movementFlagsKnown=False)]:
            with self.subTest(change=change):
                old=copy.deepcopy(self.world.pose); self.world.pose.update(change)
                with self.assertRaises(m.MovementError): self.acquire()
                self.assertEqual(self.actuator.calls,[]); self.world.pose=old
        self.backend.status(); self.world.pose['uiBinding']['observerSessionId']='aw-replaced'
        with self.assertRaisesRegex(m.MovementError,'observer_body_changed'): self.backend.status()
    def test_stuck_drift_and_wrong_turn_are_definite_stops_not_arrival(self):
        for mode in ('stuck','drift','wrong_turn'):
            with self.subTest(mode=mode):
                self.folder=self.folder/mode; self.folder.mkdir(mode=0o700)
                self.world,self.clock=World(),Clock(); self.actuator=Actuator(self.world,self.clock); self.actuator.mode=mode; self.backend=self.make()
                goal=dict(x=4,y=2 if mode=='wrong_turn' else 0,z=0)
                result=self.travel(self.acquire(),goal)
                self.assertNotEqual(result['reason'],'exploration_within_distance'); self.assertFalse(result['traveling'])
                self.assertLessEqual(len(self.actuator.calls),3)
    def test_native_static_geometry_query_has_no_actuator_authority(self):
        assets=Path('/private/tmp/warcraft-buddy-trainer-assets')
        if not assets.exists(): self.skipTest('copied client assets absent')
        self.world.pose.update(x=-8849.944,y=725.56256,z=97.61866)
        self.backend.nav=self.backend._native_plan; self.backend.data_dir=assets
        result=self.backend.call('buddy-actions.plan_route_leg',dict(sessionId=self.backend.sid,selfGuid='3',
            expectedPositionRevision='1',mapId=0,x=-8884.88,y=675.547,z=100.263))
        self.assertEqual(result['pointCount'],85); self.assertLessEqual(result['pathLength'],180)
        self.assertEqual(result['endpoint']['z'],100.08009); self.assertEqual(self.actuator.calls,[])
    def test_release_during_paired_helper_waits_then_prevents_next_key(self):
        token=self.acquire(); entered,finish=threading.Event(),threading.Event(); results=[]
        def wait(*_): entered.set(); self.assertTrue(finish.wait(2))
        self.actuator.hook=wait
        travel=threading.Thread(target=lambda:results.append(self.travel(token,dict(x=4,y=0,z=0))))
        travel.start(); self.assertTrue(entered.wait(2))
        releases=[]; release=threading.Thread(target=lambda:releases.append(self.backend.call('buddy-actions.release_interaction',dict(owner='exploration',token=token))))
        release.start(); finish.set(); travel.join(4); release.join(4)
        self.assertFalse(travel.is_alive()); self.assertFalse(release.is_alive())
        self.assertEqual(len(self.actuator.calls),1); self.assertFalse(releases[0]['traveling'])
        self.assertEqual(releases[0]['interactionOwner'],''); self.assertNotEqual(results[0]['reason'],'exploration_within_distance')


if __name__ == '__main__': unittest.main()
