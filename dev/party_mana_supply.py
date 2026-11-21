"""Mechanical observation/reservation host for the shared editable supply Lua."""
import copy
import hashlib
import importlib.machinery
import importlib.util
from pathlib import Path
import time

ROOT = Path(__file__).resolve().parent.parent
_loader = importlib.machinery.SourceFileLoader('supply_task_runtime', str(ROOT/'dev/task-runtime'))
_spec = importlib.util.spec_from_loader(_loader.name, _loader)
runtime = importlib.util.module_from_spec(_spec)
_loader.exec_module(runtime)
POLICY = ROOT/'behaviors/mana-supply.lua'
METHODS = frozenset(('buddy-trade.lookup_items', 'buddy-trade.transfer_step', 'buddy-actions.create_item'))


def take(row, keys):
    return {key: copy.deepcopy(row[key]) for key in keys if key in row}


def facts(views):
    """Select actual fields for the bounded evaluator; do not classify gameplay."""
    result = {}
    player_keys = ('guid', 'present', 'aliveKnown', 'alive', 'unitFlagsKnown', 'unitFlags',
                   'mapKnown', 'mapId', 'positionKnown', 'x', 'y', 'z', 'levelKnown', 'level',
                   'powerKnown', 'powerType', 'power', 'maxPower')
    for binding, view in views.items():
        world = view.get('world', {})
        result[str(binding)] = dict(
            world=dict(self=take(world.get('self', {}), player_keys),
                       owner=take(world.get('owner', {}), player_keys),
                       nearbyPlayers=[take(p, player_keys) for p in world.get('nearbyPlayers', [])],
                       nearbyCreatures=[take(p, ('present', 'alive', 'attackVictimKnown',
                                                 'attackVictimGuid', 'attackAgeMs'))
                                        for p in world.get('nearbyCreatures', [])]),
            actions=take(view.get('actions', {}), ('sessionId', 'selfGuid', 'position', 'moving',
                'motionUnconfirmed', 'traveling', 'castPending', 'interactionOwner', 'autoAttacking',
                'autoAttackRequested', 'autoAttackUnconfirmed', 'movementOwner', 'lastSpellOperation')),
            inventory=take(view.get('inventory', {}), ('sessionId', 'selfGuid', 'inventoryKnown',
                'inventoryRevision', 'inventoryBags', 'manaDrinkCount')),
            trade=take(view.get('trade', {}), ('sessionId', 'revision', 'phase', 'partnerGuid',
                'partnerAccepted', 'acceptTransportWritten', 'ownRequestedItems', 'ownRequestedCopper',
                'partnerItems', 'partnerCopper', 'completedTrades', 'waterGiftArmed', 'exactTransfer',
                'lastTransferStep')),
            party=take(view.get('party', {}), ('sessionId', 'group')),
            spell_toolkit=take(view.get('spell_toolkit', {}), ('sessionId', 'selfGuid', 'spellbookKnown')))
        result[str(binding)]['inventory']['items'] = [dict(item=take(i.get('item', {}), ('entry',)), **take(i, ('normalBag', 'containerSlots'))) for i in view.get('inventory', {}).get('items', []) if 'normalBag' in i or 'containerSlots' in i]
        result[str(binding)]['spell_toolkit']['spells'] = [take(s, ('spellId', 'familyId', 'rank',
            'capability', 'createdItemEntry', 'blockedReason'))
            for s in view.get('spell_toolkit', {}).get('spells', [])]
    return result


def maintain_supply(rpc, observations, state, *, bridge=None, policy=POLICY, persist=lambda: None,
                    now=None, cancelled=False, config=None):
    """Evaluate current Lua; persist its exact reservation BEFORE any mutation."""
    bridge = bridge or runtime.Bridge(ROOT/'data/bin/luapolicy', ROOT/'modules')
    views = facts(observations)
    source = Path(policy).read_text()
    frame = dict(observations=views, memory=state.get('memory', {}), last=state.get('last'),
                 pending=[], now=time.monotonic()*1000 if now is None else now,
                 cancelled=cancelled, config=config or {})
    out = bridge.evaluate(source, frame)
    if not isinstance(out, dict) or not isinstance(out.get('memory'), dict):
        raise ValueError('Lua supply decision must export memory')
    state.update(memory=out['memory'], phase=out.get('phase'),
                 policySHA256=hashlib.sha256(source.encode()).hexdigest())
    persist()
    action = out.get('action')
    if not action:
        return state['phase']
    if (not isinstance(action, dict) or not isinstance(action.get('id'), str)
            or action.get('binding') not in views or action.get('method') not in METHODS
            or not isinstance(action.get('params'), dict)):
        raise ValueError('Lua must propose one exact admitted supply RPC')
    reservations = state.setdefault('reservations', {})
    if action['id'] in reservations:
        if reservations[action['id']]['action'] != action:
            raise ValueError('supply request ID already bound')
        state['last'] = reservations[action['id']]
        persist()
        return state['phase']
    receipt = dict(id=action['id'], action=copy.deepcopy(action), outcome='reserved')
    reservations[action['id']] = receipt
    state['last'] = receipt
    persist()
    # Type validation cannot dispatch. An absent/incompatible contract is an
    # observable no-write result, rather than a stranded pending reservation.
    try:
        bridge.validate(action['method'], action['params'])
    except Exception as error:
        receipt.update(outcome='not_dispatched', error=str(error))
        persist()
        return state['phase']
    binding = next(key for key in observations if str(key) == action['binding'])
    try:
        expected = views[action['binding']]['actions']
        current = rpc(binding, 'buddy-actions.status', {})
        if current.get('sessionId') != expected.get('sessionId') or current.get('selfGuid') != expected.get('selfGuid'):
            receipt.update(outcome='not_dispatched', error='supply body changed before dispatch')
        else:
            receipt.update(outcome='rpc_result', result=rpc(binding, action['method'], action['params']))
    except Exception as error:
        # These two methods return admission status only before any packet; a
        # deadline/transport error remains unknown and is never blindly replayed.
        definite = str(error).startswith(('rpc error: code = FailedPrecondition desc =',
                                          'rpc error: code = InvalidArgument desc ='))
        receipt.update(outcome='not_dispatched' if definite else 'unconfirmed', error=str(error))
    persist()
    return state['phase']
