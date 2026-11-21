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


def rank_vendors(rpc, state, candidates, scope, *, bridge=None):
    identity = dict(sessionId=state['sessionId'], characterGuid=str(state['selfGuid']))
    distances = {}
    for distance, guid in candidates:
        if (type(guid) is not int or not 0 < guid < 2**64 or guid in distances
                or type(distance) not in (int, float) or not math.isfinite(distance) or distance < 0):
            raise Error('observed_vendor_candidates_required')
        distances[guid] = distance
    if len(distances)>96 or scope not in ('nearby', 'interactable'):
        raise Error('bounded_vendor_candidates_required')
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
    result = bridge.evaluate(wrapped, dict(kind='vendor',scope=scope,
        candidates=[dict(guid=str(guid),distance=distance) for guid,distance in distances.items()]))
    current = rpc.call('session.attach', identity)
    policy = current.get('policy', {})
    inventory = rpc.call('buddy-inventory.state', {})
    if (current.get('sessionId')!=identity['sessionId'] or str(current.get('characterGuid'))!=identity['characterGuid']
            or policy.get('revision')!=revision or str(policy.get('generation'))!=generation
            or inventory.get('sessionId')!=identity['sessionId'] or str(inventory.get('selfGuid'))!=identity['characterGuid']
            or str(inventory.get('inventoryRevision'))!=str(state.get('inventoryRevision'))):
        raise Error('town_policy_body_or_observation_changed')
    if not isinstance(result,dict) or result.get('available') is not True:
        raise Error('town_service_destination_export_unavailable')
    decision = result.get('decision')
    vendors = decision.get('vendors') if isinstance(decision,dict) else None
    if vendors == {}: vendors = []  # The Lua bridge represents an empty table as an object.
    if (not isinstance(vendors,list) or len(vendors)>len(distances)
            or any(not isinstance(guid,str) or guid not in {str(g) for g in distances} for guid in vendors)
            or len(set(vendors))!=len(vendors)):
        raise Error('lua_must_select_current_observed_vendors')
    return [(distances[int(guid)],int(guid)) for guid in vendors]
