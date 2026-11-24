"""Observe the actor's accepted Lua consumable lifecycle; never issue rest actions.

The normal encounter feed supplies inventory observations to consumables.lua.
That VM owns item choice, finite movement custody, pending uses and release.
This worker only reports a committed decision and coordinates its other readers.
"""
import copy
import re


CASTERS = {8086: 1, 8087: 4, 8402: 8}


def actor(view, session, guid):
    return view.get('sessionId') == session and str(view.get('selfGuid')) == str(guid)


def step(rpc, observations, rests, port, guid):
    view = observations.get(port, {})
    actions = view.get('actions', {})
    session = actions.get('sessionId')
    if (not isinstance(session, str) or not session or not actor(actions, session, guid)
            or str(view.get('world', {}).get('self', {}).get('guid')) != str(guid)):
        return 'waiting_for_body'
    identity = dict(sessionId=session, characterGuid=str(guid))
    state = rpc(port, 'session.policy_state', identity)
    if (not isinstance(state, dict) or state.get('sessionId') != session
            or state.get('characterGuid') != str(guid)
            or not re.fullmatch('[a-f0-9]{64}', str(state.get('revision')))
            or not re.fullmatch('[1-9][0-9]*', str(state.get('generation')))):
        return 'waiting_for_current_lua_state'
    # This read does not drain/decide the VM. Do not report an older revision's
    # decision after an accepted edit, or mistake an exported module for a feed.
    latest = state.get('latestDecision')
    saved = state.get('checkpoint', {}).get('data', {}).get('consumables')
    if not isinstance(saved, dict) or saved.get('version') != 1 or not isinstance(saved.get('data'), dict):
        return 'lua_rest_consumer_unavailable'
    if state.get('fault'):
        return 'lua_rest_policy_fault'
    if (not isinstance(latest, dict) or latest.get('revision') != state['revision']
            or latest.get('generation') != state['generation']):
        return 'waiting_for_current_lua_decision'
    decision = latest.get('output', {}).get('consumable')
    if (not isinstance(decision, dict) or not isinstance(decision.get('phase'), str)
            or not re.fullmatch('[A-Za-z0-9_]{1,128}', decision['phase'])
            or type(decision.get('hold', False)) is not bool):
        return 'lua_rest_consumer_unavailable'
    current = rpc(port, 'session.attach', identity)
    policy = current.get('policy', {})
    if (current.get('sessionId') != session or current.get('characterGuid') != str(guid)
            or policy.get('revision') != state['revision'] or policy.get('generation') != state['generation']):
        return 'waiting_for_current_lua_state'
    # Preserve clocks/cursors as observations: this is the last committed Lua
    # phase, not proof that the feed is still running or a game effect occurred.
    report = dict(sessionId=session, revision=state['revision'], generation=state['generation'],
        cursor=latest.get('cursor'), decisionClock=copy.deepcopy(latest.get('clock')),
        policyClock=copy.deepcopy(state.get('clock')), phase=decision['phase'],
        hold=decision.get('hold', False), checkpoint=copy.deepcopy(saved))
    view['mana_rest'] = report
    if report['hold']:
        rests[port] = report
    else:
        rests.pop(port, None)
    return decision['phase']


def maintain_drink(rpc, observations, rests):
    """Read each current Lua VM; retain its last observed hold on read failure."""
    phases = {}
    for port, guid in CASTERS.items():
        try:
            phases[port] = step(rpc, observations, rests, port, guid)
        except (ValueError, TypeError, AttributeError):
            phases[port] = 'invalid_lua_rest_observation'
        except Exception:
            phases[port] = 'lua_rest_observation_unavailable'
    return phases
