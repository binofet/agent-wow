"""Pure town destination decisions from the connected body's accepted Lua."""
import hashlib
import importlib.machinery
import importlib.util
import math
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parent.parent
_loader = importlib.machinery.SourceFileLoader('town_policy_runtime', str(ROOT/'dev/task-runtime'))
_spec = importlib.util.spec_from_loader(_loader.name, _loader)
runtime = importlib.util.module_from_spec(_spec)
_loader.exec_module(runtime)
Error = runtime.Error


def verify_inn_route(rpc, binding):
    identity = {key: binding[key] for key in ('sessionId', 'characterGuid')}
    current = rpc.call('session.attach', identity)
    policy = current.get('policy', {})
    if (current.get('sessionId') != identity['sessionId'] or str(current.get('characterGuid')) != identity['characterGuid']
            or not isinstance(policy, dict) or policy.get('faulted', False) is not False
            or policy.get('revision') != binding['revision'] or str(policy.get('generation')) != binding['generation']):
        raise Error('inn_route_policy_replaced_or_faulted_before_dispatch')


def inn_route(rpc, state, self_guid, creature, own, position, direction='approach', *, bridge=None):
    """Current accepted Lua chooses waypoints; the caller owns physical travel."""
    identity = dict(sessionId=state['sessionId'], characterGuid=str(self_guid))
    if direction not in ('approach', 'return') or str(own.get('guid')) != identity['characterGuid']:
        raise Error('bound_inn_route_facts_required')
    facts = dict(kind='inn_route', direction=direction, classKnown=own.get('classKnown') is True,
        classId=own.get('classId', 0), trainer={key: creature.get(key, 0) for key in ('entry', 'mapId', 'x', 'y', 'z')},
        position={key: position.get(key, 0) for key in ('mapId', 'x', 'y', 'z')})
    view = rpc.call('session.policy_source', identity)
    source, revision, generation = view.get('source'), view.get('revision'), str(view.get('generation', ''))
    limit = view.get('sourceLimitBytes', runtime.MAX_SOURCE)
    if (view.get('sessionId') != identity['sessionId'] or str(view.get('characterGuid')) != identity['characterGuid']
            or not isinstance(source, str) or not source or not re.fullmatch('[a-f0-9]{64}', str(revision))
            or hashlib.sha256(source.encode()).hexdigest() != revision or not re.fullmatch('[1-9][0-9]*', generation)
            or view.get('faulted', False) is not False
            or type(limit) is not int or limit <= 0 or len(source.encode()) > limit):
        raise Error('current_town_policy_source_required')
    wrapped = ('local policy=(function()\n'+source+'\nend)()\nreturn {decide=function(input) '
        'if type(policy.service_destination)~="function" then return {available=false} end '
        'return {available=true,decision=policy.service_destination(input)} end}\n')
    bridge = bridge or runtime.Bridge(ROOT/'data/bin/luapolicy', ROOT/'modules',
        source_bytes=max(limit, len(wrapped.encode())), value_bytes=8*1024*1024, execution_ms=1000)
    result = bridge.evaluate(wrapped, facts)
    binding = dict(identity, revision=revision, generation=generation)
    verify_inn_route(rpc, binding)
    observed = rpc.call('buddy-training.state', {})
    if observed.get('sessionId') != identity['sessionId'] or str(observed.get('revision')) != str(state.get('revision')):
        raise Error('inn_route_training_body_or_observation_changed')
    decision = result.get('decision') if isinstance(result, dict) and result.get('available') is True else None
    if not isinstance(decision, dict) or type(decision.get('eligible')) is not bool:
        raise Error('inn_route_export_unavailable')
    route = decision.get('route', [])
    if route == {}: route = []
    if (not isinstance(route, list) or len(route) > 8 or not decision['eligible'] and route
            or any(not isinstance(row, dict) or set(row) != {'x', 'y', 'z', 'radius'}
                or any(type(row.get(key)) not in (int, float) or not math.isfinite(row[key]) for key in row)
                or not -9490 <= row['x'] <= -9410 or not 0 <= row['y'] <= 85
                or not 54 <= row['z'] <= 65.5 or not 0 < row['radius'] <= 4 for row in route)):
        raise Error('bounded_inn_waypoints_required')
    return dict(eligible=decision['eligible'], route=[((row['x'], row['y'], row['z']), row['radius']) for row in route],
                policyBinding=binding)


def rank_vendors(rpc, state, candidates, scope, *, bridge=None):
    return rank_services(rpc,state,candidates,scope,'vendor',bridge=bridge)


def rank_trainers(rpc, state, self_guid, candidates, scope='nearby', *, bridge=None):
    return rank_services(rpc,dict(state,selfGuid=str(self_guid)),candidates,scope,'trainer',bridge=bridge)


def rank_services(rpc, state, candidates, scope, kind, *, bridge=None):
    identity = dict(sessionId=state['sessionId'], characterGuid=str(state['selfGuid']))
    distances, facts = {}, []
    for candidate in candidates:
        distance, guid = candidate[:2]
        if (type(guid) is not int or not 0 < guid < 2**64 or guid in distances
                or type(distance) not in (int, float) or not math.isfinite(distance) or distance < 0):
            raise Error('observed_'+kind+'_candidates_required')
        distances[guid] = distance
        fact=dict(guid=str(guid),distance=distance)
        if kind=='trainer':
            if len(candidate)!=3 or type(candidate[2]) is not bool:raise Error('observed_trainer_interaction_required')
            fact['interactable']=candidate[2]
        facts.append(fact)
    if (kind not in ('vendor','trainer') or len(distances)>(97 if kind=='trainer' else 96)
            or scope not in (('nearby','interactable','travel') if kind=='trainer' else ('nearby','interactable'))):
        raise Error('bounded_'+kind+'_candidates_required')
    view = rpc.call('session.policy_source', identity)
    source, revision, generation = view.get('source'), view.get('revision'), str(view.get('generation', ''))
    limit = view.get('sourceLimitBytes', runtime.MAX_SOURCE)
    if (view.get('sessionId')!=identity['sessionId'] or str(view.get('characterGuid'))!=identity['characterGuid']
            or not isinstance(source,str) or not source or not re.fullmatch('[a-f0-9]{64}',str(revision))
            or hashlib.sha256(source.encode()).hexdigest()!=revision or not re.fullmatch('[1-9][0-9]*',generation)
            or type(limit) is not int or limit<=0 or len(source.encode())>limit):
        raise Error('current_town_policy_source_required')
    wrapped=('local policy=(function()\n'+source+'\nend)()\nreturn {decide=function(input) '
        'if type(policy.service_destination)~="function" then return {available=false} end '
        'return {available=true,decision=policy.service_destination(input)} end}\n')
    bridge = bridge or runtime.Bridge(ROOT/'data/bin/luapolicy', ROOT/'modules',
        source_bytes=max(limit,len(wrapped.encode())), value_bytes=8*1024*1024, execution_ms=1000)
    result = bridge.evaluate(wrapped, dict(kind=kind,scope=scope,candidates=facts))
    current = rpc.call('session.attach', identity)
    policy = current.get('policy', {})
    observation = rpc.call('buddy-inventory.state' if kind=='vendor' else 'buddy-training.state', {})
    revision_field='inventoryRevision' if kind=='vendor' else 'revision'
    if (current.get('sessionId')!=identity['sessionId'] or str(current.get('characterGuid'))!=identity['characterGuid']
            or policy.get('revision')!=revision or str(policy.get('generation'))!=generation
            or observation.get('sessionId')!=identity['sessionId']
            or kind=='vendor' and str(observation.get('selfGuid'))!=identity['characterGuid']
            or str(observation.get(revision_field))!=str(state.get(revision_field))):
        raise Error('town_policy_body_or_observation_changed')
    if not isinstance(result,dict) or result.get('available') is not True:
        raise Error('town_service_destination_export_unavailable')
    decision = result.get('decision')
    vendors = decision.get(kind+'s') if isinstance(decision,dict) else None
    if vendors == {}: vendors = []  # The Lua bridge represents an empty table as an object.
    if (not isinstance(vendors,list) or len(vendors)>len(distances)
            or any(not isinstance(guid,str) or guid not in {str(g) for g in distances} for guid in vendors)
            or len(set(vendors))!=len(vendors)):
        raise Error('lua_must_select_current_observed_'+kind+'s')
    return [(distances[int(guid)],int(guid)) for guid in vendors]
