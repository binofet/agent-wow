"""Observed UI-body movement; geometry and pure Lua decide bounded paired keys."""
import copy
import hashlib
import json
import math
import os
from pathlib import Path
import secrets
import re
import stat
import subprocess
import threading
import time

ROOT = Path(__file__).resolve().parent.parent
METHODS = tuple('buddy-actions.' + v for v in ('status', 'acquire_interaction', 'release_interaction', 'travel_to', 'plan_route_leg'))
HELPER = ROOT/'data/bin/mac-game-input'
HELPER_SHA = 'fc2f6f04b4e820f8f9cbc6e40d75db6e184b5eb9df3ba1c9d4fe65256ed61a58'
UNSAFE_UNIT_FLAGS = 0x0bdc0005


class MovementError(ValueError):
    pass


def require(value, reason):
    if not value:
        raise MovementError(reason)


def number(value):
    require(type(value) in (int, float) or isinstance(value, str), 'invalid_number')
    try:
        result = float(value)
    except (ValueError, OverflowError):
        raise MovementError('invalid_number') from None
    require(math.isfinite(result), 'nonfinite_number')
    return result


def xyz(p):
    # Actual positive known flags are checked before protoJSON's omitted zeros.
    return tuple(number(p.get(k, 0)) for k in ('x', 'y', 'z'))


def distance(a, b):
    return math.dist(xyz(a), xyz(b))


def json_process(argv, value=None, timeout=4):
    raw = None if value is None else json.dumps(value, allow_nan=False, separators=(',', ':')).encode()
    result = subprocess.run(argv, input=raw, capture_output=True, timeout=timeout, check=False)
    require(len(result.stdout) <= 65536, 'local_reply_size_bound')
    try:
        decoded = json.loads(result.stdout)
    except (ValueError, UnicodeError):
        raise MovementError('local_reply_unavailable') from None
    if result.returncode != 0:
        error = decoded.get('error', {}) if isinstance(decoded, dict) else {}
        raise MovementError('local_operation_failed:'+str(error.get('message', 'unavailable'))[:160])
    return decoded


class KeyActuator:
    """The fixed helper always pairs its own key-down with a bounded key-up."""
    def __init__(self, pid, window_id, helper=HELPER, expected_sha256=HELPER_SHA):
        require(type(pid) is int and pid > 1 and type(window_id) is int and window_id > 0, 'invalid_ui_identity')
        require(Path(helper).is_absolute() and isinstance(expected_sha256, str)
                and re.fullmatch('[0-9a-f]{64}', expected_sha256), 'explicit_helper_pin_required')
        self.pid, self.window_id = pid, window_id
        self.helper, self.helper_sha = Path(helper), expected_sha256
        self.window_attested_until = 0
        self.window_attestation = None

    def attest(self):
        info = self.helper.lstat()
        require(stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid() and not info.st_mode & 0o022
                and hashlib.sha256(self.helper.read_bytes()).hexdigest() == self.helper_sha, 'ui_helper_changed')
        # The input helper independently checks the exact game window before
        # every key-down. Cache inventory for observations, which emit no input.
        if self.window_attestation is not None and time.monotonic() < self.window_attested_until:
            return dict(self.window_attestation)
        rows = json_process([str(self.helper), 'windows', str(self.pid)])
        require(isinstance(rows, list) and len(rows) <= 128, 'ui_window_inventory_unavailable')
        matches = [r for r in rows if r.get('kCGWindowOwnerPID') == self.pid and r.get('kCGWindowNumber') == self.window_id
                   and 'warcraft' in str(r.get('kCGWindowName', '')).lower()]
        require(len(matches) == 1, 'ui_window_changed')
        self.window_attestation = dict(pid=self.pid, windowId=self.window_id, windowQualified=True, helperSha256=self.helper_sha)
        self.window_attested_until = time.monotonic()+1
        return dict(self.window_attestation)

    def press(self, key, milliseconds):
        require(key in ('w', 'left', 'right') and type(milliseconds) is int and 20 <= milliseconds <= 350, 'unadmitted_key')
        self.attest()
        # Never kill a possibly key-holding child on timeout. Its bounded paired
        # helper must finish; a lost reply latches unknown and forbids more keys.
        child = subprocess.Popen([str(self.helper), 'key', str(self.pid), str(self.window_id), key, str(milliseconds)],
                                 stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            raw, _ = child.communicate(timeout=4)
        except subprocess.TimeoutExpired:
            threading.Thread(target=child.communicate, daemon=True).start()
            raise MovementError('ui_key_outcome_unconfirmed') from None
        require(child.returncode == 0 and len(raw) <= 4096, 'ui_key_outcome_unconfirmed')
        try:
            reply = json.loads(raw)
        except (ValueError, UnicodeError):
            raise MovementError('ui_key_outcome_unconfirmed') from None
        return reply


class MovementBackend:
    def __init__(self, session_id, pid, window_id, observe, state_dir, data_dir, *,
                 nav_binary=ROOT/'data/bin/nav-plan', policy_binary=ROOT/'data/bin/luapolicy', actuator=None,
                 nav=None, clock=time.monotonic, sleep=time.sleep):
        require(isinstance(session_id, str) and 1 <= len(session_id) <= 128, 'invalid_ui_session')
        self.sid, self.pid, self.window = session_id, pid, window_id
        self.observe, self.clock, self.sleep = observe, clock, sleep
        self.dir, self.data_dir = Path(state_dir), Path(data_dir)
        self.nav_binary, self.policy_binary = Path(nav_binary), Path(policy_binary)
        self.policy = ROOT/'behaviors/ui-steering.lua'
        self.actuator, self.nav = actuator or KeyActuator(pid, window_id), nav or self._native_plan
        self.mu = threading.RLock()
        self.cancel, self.done = threading.Event(), threading.Event()
        self.done.set()
        self.lease, self.until, self.traveling, self.key_active = None, 0, False, False
        self.reason, self.motion_unknown = 'ui_observer_ready', False
        self.turn_rate = math.pi
        self.observer_binding = None
        self.identity = dict(sessionId=session_id, guid='3', pid=pid, windowId=window_id)
        self.dir.mkdir(mode=0o700, parents=True, exist_ok=True)
        info = self.dir.lstat()
        require(self.dir.resolve() == self.dir.absolute() and stat.S_ISDIR(info.st_mode)
                and info.st_uid == os.getuid() and not info.st_mode & 0o077, 'private_movement_directory_required')
        self.record = dict(identity=self.identity, sequence=0, pendingKey=None, history=[])
        target = self.dir/'movement.json'
        if target.exists() or target.is_symlink():
            info = target.lstat()
            require(stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid() and not info.st_mode & 0o077
                    and info.st_nlink == 1 and info.st_size <= 65536, 'private_movement_record_required')
            self.record = json.loads(target.read_text())
            require(self.record.get('identity') == self.identity, 'saved_ui_body_changed')
            self.motion_unknown = bool(self.record.get('pendingKey') or self.record.get('motionUnconfirmed'))
        # Constructor performs no observer, actuator, helper, or gameplay calls.

    def _save(self):
        self.record['motionUnconfirmed'] = self.motion_unknown
        raw = json.dumps(self.record, allow_nan=False).encode()
        require(len(raw) <= 65536, 'movement_journal_full')
        temporary = self.dir/('.movement-'+secrets.token_hex(8))
        with os.fdopen(os.open(temporary, os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW, 0o600), 'wb') as f:
            f.write(raw); f.flush(); os.fsync(f.fileno())
        require(not (self.dir/'movement.json').is_symlink(), 'movement_record_replaced')
        os.replace(temporary, self.dir/'movement.json')

    def _pose(self, safe=False, map_id=None):
        p = copy.deepcopy(self.observe())
        binding = p.get('uiBinding', {})
        require(isinstance(binding, dict) and all(binding.get(k) == v for k, v in
                dict(sessionId=self.sid, pid=self.pid, windowId=self.window, qualified=True).items()), 'ui_body_binding_changed')
        observer = (binding.get('observerSessionId'), binding.get('observerGuid'))
        require(self.observer_binding in (None, observer), 'ui_observer_body_changed')
        self.observer_binding = observer
        require(str(p.get('guid')) == '3' and p.get('present') is True and p.get('positionKnown') is True
                and p.get('mapKnown') is True and number(p.get('positionRevision', 0)) > 0, 'ui_owner_position_unknown')
        xyz(p); number(p.get('orientation', 0))
        if map_id is not None:
            require(int(number(p.get('mapId', 0))) == map_id, 'ui_map_changed')
        require(p.get('movementFlagsKnown') is True, 'ui_movement_unknown')
        flags = int(number(p.get('movementFlags', 0)))
        require(flags & ~0x700001ff == 0 and int(number(p.get('extraMovementFlags', 0))) == 0, 'ui_ground_mode_unsupported')
        require(not flags & 0xff or number(p.get('positionAgeMs', 0)) <= 2000, 'ui_moving_pose_stale')
        if safe:
            self._safe(p)
        return p

    @staticmethod
    def _safe(p):
        require(p.get('aliveKnown') is True and p.get('alive') is True and p.get('healthKnown') is True
                and 0 < number(p.get('health', 0)) <= number(p.get('maxHealth', 0))
                and number(p['health'])/number(p['maxHealth']) >= .85, 'ui_health_unsafe')
        require(p.get('unitFlagsKnown') is True and int(number(p.get('unitFlags', 0))) & UNSAFE_UNIT_FLAGS == 0
                and not number(p.get('channelSpellId', 0)), 'ui_combat_or_control_unsafe')
        require(p.get('speedsKnown') is True and abs(number(p.get('runSpeed', 0))-7) <= .01
                and int(number(p.get('movementFlags', 0))) & 0x100 == 0, 'ui_normal_run_speed_required')

    def _position(self, p):
        return dict(known=True, mapKnown=True, mapId=int(number(p.get('mapId', 0))),
                    **dict(zip(('x', 'y', 'z'), xyz(p))), orientation=number(p.get('orientation', 0)),
                    revision=str(p['positionRevision']), ageMs=str(int(number(p.get('positionAgeMs', 0)))))

    def status(self):
        p = self._pose()
        with self.mu:
            remaining = max(0, self.until-self.clock()) if self.lease else 0
            return dict(sessionId=self.sid, selfGuid='3', position=self._position(p),
                        moving=bool(int(number(p.get('movementFlags', 0))) & 0xff), traveling=self.traveling,
                        motionUnconfirmed=self.motion_unknown, interactionOwner='exploration' if remaining else '',
                        interactionLeaseRemainingMs=str(int(remaining*1000)), reason=self.reason)

    def _native_plan(self, p, goal):
        payload = dict(mapId=int(number(p.get('mapId', 0))), start=dict(zip(('x', 'y', 'z'), xyz(p))), goal=goal)
        return json_process([str(self.nav_binary), '--data-dir', str(self.data_dir)], payload, timeout=3)

    def _path(self, p, goal):
        result = self.nav(p, goal)
        require(result.get('ok') is True and isinstance(result.get('points'), list), 'ui_navigation_unavailable')
        require(int(number(result.get('mapId', 0))) == int(number(p.get('mapId', 0))), 'ui_navigation_wrong_map')
        points = result['points']
        require(2 <= len(points) <= 256 and distance(p, points[0]) <= 2, 'ui_navigation_bad_endpoint')
        length = 0
        for a, b in zip(points, points[1:]):
            step = distance(a, b)
            require(step <= 2 and abs(xyz(b)[2]-xyz(a)[2]) <= 1, 'ui_navigation_bad_segment')
            length += step
        require(length <= 180 and distance(points[-1], result['endpoint']) <= .01, 'ui_navigation_bad_length')
        return points, length

    def _bound(self, token):
        require(self.lease and secrets.compare_digest(self.lease, token) and self.until-self.clock() > .5
                and not self.cancel.is_set() and not self.motion_unknown, 'ui_exploration_custody_changed')

    def _policy(self, p, points, index, arrival):
        value = dict(pose=dict(self._position(p)), path=points, index=index, arrivalDistance=arrival,
                     runSpeed=7, turnRate=self.turn_rate,
                     bearings=[math.atan2(xyz(q)[1]-xyz(p)[1], xyz(q)[0]-xyz(p)[0]) for q in points])
        return json_process([str(self.policy_binary), '--mode', 'evaluate', '--source', str(self.policy)], value, timeout=2)

    def _pulse(self, token, before, key, milliseconds, deadline):
        require(key in ('w', 'left', 'right') and type(milliseconds) is int and 20 <= milliseconds <= 350, 'ui_policy_key_rejected')
        with self.mu:
            self._bound(token)
            require(self.clock()+milliseconds/1000+.2 < deadline, 'ui_travel_time_limit')
            self.record['sequence'] += 1
            require(self.record['sequence'] <= 10000, 'ui_key_budget_exhausted')
            row = dict(sequence=self.record['sequence'], key=key, heldMs=milliseconds,
                       positionRevision=str(before['positionRevision']), phase='reserved')
            self.record['pendingKey'] = row; self._save()
            try:
                fresh = self._pose(safe=True, map_id=int(number(before.get('mapId', 0))))
                self._bound(token)
                require(str(fresh['positionRevision']) == str(before['positionRevision']) and distance(fresh, before) < .05, 'ui_pose_changed_before_key')
            except Exception:
                row['phase'] = 'not_dispatched'
                self.record['history'] = (self.record['history']+[row])[-64:]
                self.record['pendingKey'] = None; self._save()
                raise
            self.key_active = True
            try:
                # Release cannot acknowledge custody retirement between this
                # final guard and the helper's paired key-up receipt.
                reply = self.actuator.press(key, milliseconds)
                require(reply == dict(pid=self.pid, window=self.window, key=key, heldMs=milliseconds, released=True), 'ui_key_outcome_unconfirmed')
            except Exception:
                self.motion_unknown = True; self.reason = 'ui_key_outcome_unconfirmed'; self._save()
                raise MovementError('ui_key_outcome_unconfirmed') from None
            finally:
                self.key_active = False
            row.update(phase='paired_keys_released'); self.record['history'] = (self.record['history']+[row])[-64:]
            self.record['pendingKey'] = None; self._save()
        # The movement deadline never prevents a final read-only STOP proof.
        stop_deadline = self.clock()+1.5
        while self.clock() < stop_deadline:
            try:
                p = self._pose(map_id=int(number(before.get('mapId', 0))))
            except Exception:
                with self.mu:
                    self.motion_unknown = True; self.reason = 'ui_movement_observation_unconfirmed'; self._save()
                raise MovementError('ui_movement_observation_unconfirmed') from None
            if str(p['positionRevision']) != str(before['positionRevision']) and int(number(p.get('movementFlags', 0))) & 0xff == 0:
                require(distance(p, before) <= 7*milliseconds/1000+1.5, 'ui_unexpected_displacement')
                if key != 'w':
                    delta = (number(p.get('orientation', 0))-number(before.get('orientation', 0))+math.pi) % (2*math.pi)-math.pi
                    require(distance(p, before) <= 1 and (delta >= 0 if key == 'left' else delta <= 0), 'ui_turn_direction_mismatch')
                    if abs(delta) > .02:
                        self.turn_rate = max(1, min(6, abs(delta)/(milliseconds/1000)))
                self._safe(p)
                return p
            self.sleep(.05)
        with self.mu:
            self.motion_unknown = True; self.reason = 'ui_movement_observation_unconfirmed'; self._save()
        raise MovementError('ui_movement_observation_unconfirmed')

    def call(self, method, params):
        require(method in METHODS and isinstance(params, dict), 'ui_method_unsupported')
        if method.endswith('.status'):
            return self.status()
        if method.endswith('.plan_route_leg'):
            p = self._pose()
            require(params.get('sessionId') == self.sid and str(params.get('selfGuid')) == '3'
                    and str(params.get('expectedPositionRevision')) == str(p['positionRevision'])
                    and int(number(params.get('mapId', 0))) == int(number(p.get('mapId', 0))), 'ui_plan_binding_changed')
            goal = dict(zip(('x', 'y', 'z'), xyz(params))); path, length = self._path(p, goal)
            current = self._pose(map_id=int(number(p.get('mapId', 0))))
            require(str(current['positionRevision']) == str(p['positionRevision']) and distance(current, p) < .05, 'ui_plan_origin_changed')
            return dict(sessionId=self.sid, selfGuid='3', mapId=int(number(p.get('mapId', 0))), start=self._position(p),
                        endpoint=dict(known=True, mapKnown=True, mapId=int(number(p.get('mapId', 0))), **path[-1]),
                        pathLength=length, pointCount=len(path), goalReached=distance(path[-1], goal) <= 2,
                        serverPositionRevision=str(p['positionRevision']))
        if method.endswith('.acquire_interaction'):
            p = self._pose(safe=True)
            require(params.get('owner') == 'exploration' and not params.get('harvest')
                    and int(number(p.get('movementFlags', 0))) & 0xff == 0, 'ui_stationary_exploration_only')
            lease_ms = int(number(params.get('leaseMs', 15000))) or 15000
            require(1000 <= lease_ms <= 30000, 'ui_invalid_lease')
            with self.mu:
                require(not self.motion_unknown, 'ui_motion_unconfirmed')
                if params.get('token'):
                    self._bound(params['token'])
                else:
                    require(not self.traveling and (not self.lease or self.until <= self.clock()), 'ui_exploration_busy')
                    self.lease = secrets.token_hex(24); self.cancel.clear()
                self.until = self.clock()+lease_ms/1000
                return dict(token=self.lease, state=self.status())
        if method.endswith('.release_interaction'):
            with self.mu:
                require(params.get('owner') == 'exploration' and self.lease and secrets.compare_digest(self.lease, params.get('token', '')), 'ui_release_token_changed')
                self.cancel.set(); self.lease = None; self.until = 0
            require(self.done.wait(4), 'ui_travel_cleanup_unconfirmed')
            return self.status()
        token = params.get('token', '')
        p = self._pose(safe=True, map_id=int(number(params.get('mapId', 0))))
        goal = dict(zip(('x', 'y', 'z'), xyz(params)))
        arrival = number(params.get('arrivalDistance', 3)) or 3
        require(.5 <= arrival <= 3 and distance(p, goal) <= 200, 'ui_travel_bound')
        with self.mu:
            self._bound(token); require(not self.traveling, 'ui_travel_busy')
            self.traveling = True; self.done.clear(); self.reason = 'exploration_planning_client_navmesh'
        deadline = self.clock()+30
        try:
            points, _ = self._path(p, goal)
            require(distance(points[-1], goal) <= arrival, 'ui_route_endpoint_outside_arrival')
            current = self._pose(safe=True, map_id=int(number(params.get('mapId', 0))))
            require(str(current['positionRevision']) == str(p['positionRevision']) and distance(current, p) < .05, 'ui_plan_origin_changed')
            index, stuck = 1, 0
            for _ in range(150):
                with self.mu:
                    self._bound(token)
                p = self._pose(safe=True, map_id=int(number(params.get('mapId', 0))))
                require(self.clock() < deadline, 'ui_travel_time_limit')
                decision = self._policy(p, points, index, arrival)
                require(decision.get('phase') != 'path_drift', 'ui_path_drift')
                next_index = int(number(decision.get('index', index)))
                require(index <= next_index <= min(index+3, len(points)), 'ui_policy_index_rejected')
                index = next_index
                if decision.get('arrived') is True:
                    require(distance(p, goal) <= arrival and int(number(p.get('movementFlags', 0))) & 0xff == 0, 'ui_arrival_not_observed')
                    self.reason = 'exploration_within_distance'; break
                target = points[index-1]
                angle = (math.atan2(xyz(target)[1]-xyz(p)[1], xyz(target)[0]-xyz(p)[0])-number(p.get('orientation', 0))+math.pi) % (2*math.pi)-math.pi
                key = decision.get('key')
                require(abs(angle) <= .2 if key == 'w' else key == ('left' if angle > 0 else 'right'), 'ui_policy_heading_rejected')
                new = self._pulse(token, p, decision.get('key'), decision.get('durationMs'), deadline)
                stuck = stuck+1 if decision.get('key') == 'w' and distance(p, new) < .1 else 0
                require(stuck < 3, 'ui_travel_stuck')
            else:
                raise MovementError('ui_travel_step_limit')
        except Exception as error:
            self.reason = str(error)[:160] if isinstance(error, MovementError) else 'ui_travel_failed'
            if self.motion_unknown:
                raise MovementError(self.reason) from None
        finally:
            with self.mu:
                self.traveling = False; self.done.set()
        return self.status()
