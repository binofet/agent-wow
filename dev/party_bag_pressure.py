"""Execute one exact cleanup selected by the actor's accepted Lua policy."""
import copy
import hashlib
import os
import re
import time

import item_disposal
from party_mana_supply import ROOT, runtime, _catalog, _identity, _validate_action

PARTY = {8086: 1, 8087: 4, 8088: 5, 8402: 8}
VALUE_BYTES = 8 * 1024 * 1024


def _source(rpc, port, identity):
    view = rpc(port, 'session.policy_source', identity)
    source, revision = view.get('source'), view.get('revision')
    generation, limit = str(view.get('generation', '')), view.get('sourceLimitBytes', runtime.MAX_SOURCE)
    if (view.get('sessionId') != identity['sessionId'] or str(view.get('characterGuid')) != identity['characterGuid']
            or not isinstance(source, str) or not source or not re.fullmatch('[a-f0-9]{64}', str(revision))
            or hashlib.sha256(source.encode()).hexdigest() != revision
            or not re.fullmatch('[1-9][0-9]{0,19}', generation) or int(generation) >= 2**64
            or type(limit) is not int or limit <= 0 or len(source.encode()) > limit):
        raise runtime.Error('current_accepted_cleanup_source_required')
    return source, dict(identity, revision=revision, generation=generation), limit


def _current(rpc, port, binding):
    view = rpc(port, 'session.attach', {key: binding[key] for key in ('sessionId', 'characterGuid')})
    policy = view.get('policy', {})
    if (view.get('sessionId') != binding['sessionId'] or str(view.get('characterGuid')) != binding['characterGuid']
            or policy.get('revision') != binding['revision'] or str(policy.get('generation')) != binding['generation']
            or policy.get('faulted', False) is not False):
        raise runtime.Error('cleanup_policy_or_body_changed')


def _facts(rpc, port, view, identity):
    view = copy.deepcopy(view)
    raw = rpc(port, 'buddy-inventory.raw_gear_facts', {})
    if (not isinstance(raw, dict) or raw.get('sessionId') != identity['sessionId']
            or str(raw.get('selfGuid')) != identity['characterGuid']
            or raw.get('inventoryRevision') != view['inventory'].get('inventoryRevision')
            or raw.get('inventoryRevision') != view['world'].get('inventoryRevision')
            or raw.get('inventoryKnown') is not True):
        raise runtime.Error('current_raw_cleanup_inventory_required')
    # Copy raw instance facts, never cached eligibility/ranking flags.
    view['inventory'].update(raw)
    sources, entries = [], set()
    for bag in raw.get('inventoryBags', []):
        for row in bag.get('slots', []):
            if str(row.get('itemGuid', '0')) != '0':
                source = item_disposal._reference(row)
                item_disposal._layout(raw['inventoryBags'], source)
                sources.append(source)
                entries.add(source['entry'])
    for row in view['world'].get('selfEquipment', []):
        if row.get('entry'):
            entries.add(item_disposal._uint(row['entry']))
    metadata = {}
    ordered = sorted(entries)  # Transport batches, not a candidate shortlist.
    for offset in range(0, len(ordered), 6):
        reply = rpc(port, 'buddy-trade.lookup_items', {'itemIds': ordered[offset:offset + 6]})
        if reply.get('sessionId') != identity['sessionId']:
            raise runtime.Error('cleanup_template_body_changed')
        for row in reply.get('items', []):
            if isinstance(row, dict) and row.get('itemId') in entries:
                metadata[str(row['itemId'])] = copy.deepcopy(row)
    contexts = {}
    for source in sources:
        context = item_disposal.project_context(dict(source=source, inventory=raw,
            world=view['world'], journal=view.get('journal', {})))
        if context is not None:
            contexts[source['itemGuid']] = context
    return view, metadata, contexts


def _evaluate(rpc, port, view, record, now, bridge):
    identity = _identity(view, PARTY[port])
    source, binding, limit = _source(rpc, port, identity)
    catalog = _catalog(rpc, port, identity)
    view, metadata, contexts = _facts(rpc, port, view, identity)
    wrapped = ('local policy=(function()\n' + source + '\nend)()\nreturn {decide=function(input) '
        'if type(policy.mana_supply_plan)~="function" then return {available=false} end '
        'return {available=true,decision=policy.mana_supply_plan(input)} end}\n')
    evaluator = bridge or runtime.Bridge(os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY', ROOT / 'data/bin/luapolicy'),
        ROOT / 'modules', source_bytes=max(limit, len(wrapped.encode())), value_bytes=VALUE_BYTES, execution_ms=1000)
    result = evaluator.evaluate(wrapped, dict(query='loot_pressure', observations={str(port): view},
        metadata=metadata, disposalContexts=contexts, catalogs={str(port): catalog['methods']},
        catalogMessages={str(port): catalog['messages']}, bindings=dict(magePort=str(port), mageGuid=identity['characterGuid']),
        memory=copy.deepcopy(record.get('memory', {})), last=copy.deepcopy(record.get('last')), nowMs=now * 1000))
    _current(rpc, port, binding)
    decision = result.get('decision', {}) if isinstance(result, dict) and result.get('available') is True else {}
    if (decision.get('consumer') != 'loot_pressure' or not isinstance(decision.get('memory'), dict)
            or not isinstance(decision.get('phase'), str) or not re.fullmatch('[A-Za-z0-9_]{1,128}', decision['phase'])):
        raise runtime.Error('accepted_lua_loot_pressure_consumer_required')
    return decision, binding, catalog, view


def _persist(persist, attempts):
    if persist is None:
        raise runtime.Error('durable_cleanup_store_required')
    if persist(copy.deepcopy(attempts)) is False:
        raise runtime.Error('cleanup_persistence_unconfirmed')


def _scene(view):
    """Compare factual inputs used by Lua, without choosing their meaning."""
    world, actions = view['world'], view['actions']
    def pose(row):
        return {key: row.get(key) for key in ('known', 'positionKnown', 'mapKnown', 'mapId', 'x', 'y', 'z')}
    def person(row):
        return {key: row.get(key) for key in ('guid', 'present', 'aliveKnown', 'alive', 'unitFlagsKnown',
            'unitFlags', 'movementFlagsKnown', 'movementFlags', 'channelSpellId', 'mapKnown', 'mapId',
            'positionKnown', 'positionRevision', 'x', 'y', 'z')}
    return dict(self=person(world.get('self', {})), owner=person(world.get('owner', {})),
        position=pose(actions.get('position', {})), corpses=[dict(pose(row), present=row.get('present'))
            for row in world.get('lootableCorpses', [])],
        actions={key: actions.get(key) for key in ('moving', 'motionUnconfirmed', 'traveling', 'castPending',
            'interactionOwner', 'autoAttacking', 'autoAttackRequested', 'autoAttackUnconfirmed', 'practicing', 'recovery')},
        inventory={key: view['inventory'].get(key) for key in ('active', 'closing')},
        trade={key: view['trade'].get(key) for key in ('phase', 'waterGiftArmed', 'lastJunkDisposal', 'junkOperations')},
        equipment=world.get('selfEquipment'), quests=world.get('selfQuestLog'),
        loot={key: view.get('loot', {}).get(key) for key in ('sessionId', 'enabled', 'inventoryPaused')})


def _prewrite(rpc, port, binding, selected, action):
    actions = rpc(port, 'buddy-actions.status', {})
    world = rpc(port, 'buddy-world.snapshot', {})
    inventory = rpc(port, 'buddy-inventory.raw_gear_facts', {})
    trade = rpc(port, 'buddy-trade.state', {})
    inventory_state = rpc(port, 'buddy-inventory.state', {})
    loot = rpc(port, 'buddy-loot.state', {})
    source = selected['source']
    if (actions.get('sessionId') != binding['sessionId'] or str(actions.get('selfGuid')) != binding['characterGuid']
            or str(world.get('self', {}).get('guid')) != binding['characterGuid']
            or inventory.get('sessionId') != binding['sessionId'] or str(inventory.get('selfGuid')) != binding['characterGuid']
            or trade.get('sessionId') != binding['sessionId'] or trade.get('revision') != action['params']['revision']
            or inventory_state.get('sessionId') != binding['sessionId']
            or str(inventory_state.get('selfGuid')) != binding['characterGuid']
            or any(world.get(key) != selected['world'].get(key)
                   for key in ('inventoryRevision', 'selfEquipmentRevision', 'selfQuestLogRevision'))
            or inventory.get('inventoryRevision') != selected['inventoryRevision']):
        raise runtime.Error('cleanup_native_frame_changed_before_dispatch')
    if _scene(dict(world=world, actions=actions, inventory=inventory_state, trade=trade, loot=loot)) != _scene(selected['view']):
        raise runtime.Error('cleanup_scene_changed_before_dispatch')
    item_disposal._layout(inventory.get('inventoryBags'), source)
    if source['bag'] == 0 and trade.get('backpackRevision') != action['params']['backpackRevision']:
        raise runtime.Error('cleanup_source_revision_changed_before_dispatch')
    _current(rpc, port, binding)


def step(rpc, views, attempts, _safe_entries, port, guid, now, *, persist=None, bridge=None):
    view = views.get(port)
    if not isinstance(view, dict) or str(view.get('actions', {}).get('selfGuid')) != str(guid):
        return 'waiting_for_body'
    key = str(port)
    record = attempts.get(key, attempts.get(port, {}))
    # Do not reinterpret a previous host's uncertain reservation as permission
    # for a new Lua action, or lose it because the process/body changed.
    if 'itemGuid' in record:
        return 'legacy_disposal_custody_retained_no_retry'
    decision, binding, catalog, observed = _evaluate(rpc, port, view, record, now, bridge)
    proposed = dict(sessionId=binding['sessionId'], characterGuid=binding['characterGuid'],
        memory=decision['memory'], phase=decision['phase'], last=copy.deepcopy(record.get('last')))
    action = decision.get('action')
    if action is not None:
        if action.get('method') != 'buddy-trade.destroy_junk' or action.get('port') != key:
            raise runtime.Error('exact_cleanup_mutation_required')
        _validate_action(action, catalog, binding)
        pending = proposed['memory'].get('pending', {})
        if pending.get('actionId') != action['id'] or pending.get('actorSession') != binding['sessionId']:
            raise runtime.Error('cleanup_action_reservation_required')
        source, params = pending.get('source', {}), action['params']
        if (params.get('expectedItemGuid') != source.get('itemGuid')
                or source.get('bag') == 0 and (params.get('backpackSlot', 0) != source.get('slot')
                    or params.get('backpackRevision') != pending.get('sourceRevision'))
                or source.get('bag') != 0 and (params.get('sourceBag') != source.get('bag')
                    or params.get('sourceSlot', 0) != source.get('slot')
                    or params.get('inventoryRevision') != pending.get('sourceRevision'))):
            raise runtime.Error('cleanup_action_source_mismatch')
        proposed['last'] = dict(id=action['id'], outcome='transport_unconfirmed')
    attempts[key] = proposed
    _persist(persist, attempts)  # Every uncertainty is durable before any write.
    if action is None:
        return proposed['phase']
    entered = False
    try:
        pending = proposed['memory']['pending']
        _prewrite(rpc, port, binding, dict(source=pending['source'], world=observed['world'],
            inventoryRevision=pending['inventoryRevision'], view=observed), action)
        _validate_action(action, _catalog(rpc, port, binding), binding)
        _current(rpc, port, binding)
        entered = True
        reply = rpc(port, action['method'], action['params'])
        if not isinstance(reply, dict) or reply.get('sessionId') != binding['sessionId']:
            raise runtime.Error('cleanup_reply_body_unconfirmed')
        proposed['last'] = dict(id=action['id'], outcome='rpc_result', result=copy.deepcopy(reply))
        _persist(persist, attempts)
    except Exception:
        if not entered:
            proposed['last'] = dict(id=action['id'], outcome='not_dispatched')
            _persist(persist, attempts)
            return 'cleanup_not_dispatched'
        return 'cleanup_outcome_unconfirmed'
    return proposed['phase']


def maintain_bag_pressure(rpc, observations, attempts, safe_entries=None, *, persist=None, bridge=None):
    now, phases = time.monotonic(), {}
    for port, guid in PARTY.items():
        try:
            phases[port] = step(rpc, observations, attempts, safe_entries, port, guid, now,
                persist=persist, bridge=bridge)
        except (TypeError, ValueError, KeyError, AttributeError):
            phases[port] = 'invalid_cleanup_observation'
        except Exception:
            phases[port] = 'lua_cleanup_unavailable'
    return phases
