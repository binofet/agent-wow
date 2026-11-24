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


def verify_inventory_maintenance(rpc, binding):
    if (not isinstance(binding, dict) or set(binding) != {'sessionId', 'characterGuid', 'revision', 'generation'}
            or not isinstance(binding['sessionId'], str) or not 1 <= len(binding['sessionId']) <= 128
            or not re.fullmatch('[1-9][0-9]*', str(binding['characterGuid']))
            or not re.fullmatch('[a-f0-9]{64}', str(binding['revision']))
            or not re.fullmatch('[1-9][0-9]*', str(binding['generation']))):
        raise Error('bound_inventory_policy_required')
    current = rpc.call('session.attach', {key: binding[key] for key in ('sessionId', 'characterGuid')})
    policy = current.get('policy', {})
    if (current.get('sessionId') != binding['sessionId'] or str(current.get('characterGuid')) != binding['characterGuid']
            or not isinstance(policy, dict) or policy.get('faulted', False) is not False
            or policy.get('revision') != binding['revision'] or str(policy.get('generation')) != binding['generation']):
        raise Error('inventory_policy_replaced_or_faulted_before_dispatch')


def inventory_maintenance(rpc, state, facts, *, bridge=None):
    """One inventory priority decision from the current accepted service export."""
    identity = dict(sessionId=state['sessionId'], characterGuid=str(state['selfGuid']))
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
    result = bridge.evaluate(wrapped, dict(facts, kind='inventory_maintenance'))
    current = rpc.call('session.attach', identity)
    policy = current.get('policy', {})
    observed = rpc.call('buddy-inventory.state', {})
    if (current.get('sessionId') != identity['sessionId'] or str(current.get('characterGuid')) != identity['characterGuid']
            or policy.get('revision') != revision or str(policy.get('generation')) != generation or policy.get('faulted', False) is not False
            or observed.get('sessionId') != identity['sessionId'] or str(observed.get('selfGuid')) != identity['characterGuid']
            or any(str(observed.get(key)) != str(state.get(key)) for key in ('revision', 'inventoryRevision', 'copperRevision'))):
        raise Error('town_inventory_policy_body_or_observation_changed')
    decision = result.get('decision') if isinstance(result, dict) and result.get('available') is True else None
    if not isinstance(decision, dict) or not isinstance(decision.get('choice'), dict):
        raise Error('town_inventory_priority_export_unavailable')
    return dict(decision['choice'], policyBinding=dict(identity, revision=revision, generation=generation))


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
