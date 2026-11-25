"""Bounded fact transport to the shared starter objective policy."""
import importlib.machinery
import importlib.util
import hashlib
import os
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parent.parent
SOURCE = ROOT / "behaviors/starter-objectives.lua"
ACCEPTED_QUERY_EXECUTION_MS = 1000


def evaluate(facts):
    loader = importlib.machinery.SourceFileLoader("starter_objective_runtime", str(ROOT / "dev/task-runtime"))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    runtime = importlib.util.module_from_spec(spec)
    loader.exec_module(runtime)
    binary = os.environ.get("BUDDY_LUAPOLICY_BINARY", os.environ.get("BUDDY_LUAPOLICY_TEST_BINARY", ROOT / "data/bin/luapolicy"))
    try:
        return runtime.Bridge(binary, ROOT / "modules").evaluate(SOURCE.read_text(), facts)
    except (runtime.Error, OSError, UnicodeError):
        return {"status": "pending"}


def verify_accepted(rpc, binding):
    identity = {key: binding[key] for key in ('sessionId', 'characterGuid')}
    current = rpc.call('session.attach', identity)
    if not isinstance(current, dict): raise ValueError('starter_policy_body_or_source_changed')
    policy = current.get('policy', {})
    if (current.get('sessionId') != identity['sessionId'] or str(current.get('characterGuid')) != identity['characterGuid']
            or not isinstance(policy, dict) or policy.get('faulted', False) is not False
            or policy.get('revision') != binding['revision'] or str(policy.get('generation')) != binding['generation']):
        raise ValueError('starter_policy_body_or_source_changed')


def evaluate_accepted(rpc, identity, facts, *, bridge=None):
    """Use the connected body's accepted export, never the repository source."""
    view = rpc.call('session.policy_source', identity)
    if not isinstance(view, dict): raise ValueError('current_accepted_starter_source_required')
    source, revision, generation = view.get('source'), view.get('revision'), str(view.get('generation', ''))
    limit = view.get('sourceLimitBytes', 8 * 1024 * 1024)
    if (view.get('sessionId') != identity['sessionId'] or str(view.get('characterGuid')) != identity['characterGuid']
            or not isinstance(source, str) or not source or not re.fullmatch('[a-f0-9]{64}', str(revision))
            or hashlib.sha256(source.encode()).hexdigest() != revision or not re.fullmatch('[1-9][0-9]{0,19}', generation)
            or int(generation) >= 2**64 or view.get('faulted', False) is not False
            or type(limit) is not int or limit <= 0 or len(source.encode()) > limit):
        raise ValueError('current_accepted_starter_source_required')
    loader = importlib.machinery.SourceFileLoader('accepted_starter_runtime', str(ROOT / 'dev/task-runtime'))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    runtime = importlib.util.module_from_spec(spec); loader.exec_module(runtime)
    wrapped = ('local policy=(function()\n'+source+'\nend)()\nreturn {decide=function(input) '
        'if type(policy.starter_spell)~="function" then return {available=false} end '
        'return {available=true,decision=policy.starter_spell(input)} end}\n')
    binary = os.environ.get('BUDDY_LUAPOLICY_BINARY', os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY', ROOT / 'data/bin/luapolicy'))
    # Match the existing full accepted-source reward/profession query profile.
    # This does not change the native selector/CLI default or JSON byte bound.
    bridge = bridge or runtime.Bridge(binary, ROOT / 'modules', source_bytes=max(limit, len(wrapped.encode())),
        execution_ms=ACCEPTED_QUERY_EXECUTION_MS)
    try: result = bridge.evaluate(wrapped, facts)
    except runtime.Error as error: raise ValueError(str(error)) from None
    binding = dict(identity, revision=revision, generation=generation)
    verify_accepted(rpc, binding)
    if not isinstance(result, dict) or result.get('available') is not True or not isinstance(result.get('decision'), dict):
        raise ValueError('accepted_starter_export_unavailable')
    return result['decision'], binding
