"""Autonomous mana recovery for the companion worker's existing tick."""

import time


CASTERS = {8086: 1, 8087: 4, 8402: 8}
DUNGEON_MAPS = frozenset({33, 34, 36, 43, 48, 389})  # Early dungeons used by the party.
START_BELOW = 80
COMBAT = 0x80000
RECENT_ATTACK_MS = 5000


def number(value):
    return int(value or 0)


def actor(actions, session, guid):
    return actions.get('sessionId') == session and number(actions.get('selfGuid')) == guid


def finish(rpc, rests, port, actions):
    rest = rests.pop(port, None)
    if rest and rest['restoreFollow'] and actor(actions, rest['sessionId'], CASTERS[port]) and not actions.get('following'):
        try:
            rpc(port, 'buddy-actions.follow', {'enabled': True})
        except Exception:
            pass  # RPC failures are isolated to the affected character.


def unsafe(world, own, actions, party):
    owner = world.get('owner', {})
    members = {number(row.get('guid')) for row in party.get('group', {}).get('members', []) if row.get('online')}
    nearby = [row for row in world.get('nearbyPlayers', []) if number(row.get('guid')) in members]
    people = [row for row in [own, owner, *nearby] if row.get('present')]
    if any(row.get('unitFlagsKnown') and number(row.get('unitFlags')) & COMBAT for row in people):
        return True
    victims = {number(row.get('guid')) for row in people}
    if any(row.get('present') and row.get('aliveKnown') and row.get('alive') and row.get('attackVictimKnown')
           and number(row.get('attackAgeMs')) <= RECENT_ATTACK_MS
           and number(row.get('attackVictimGuid')) in victims for row in world.get('nearbyCreatures', [])):
        return True
    urgent_heal = (actions.get('healing') and number(own.get('power')) > 0
                   and any(row.get('alive') and row.get('healthKnown')
                           and 0 < number(row.get('health')) * 5 < number(row.get('maxHealth')) * 4 for row in people))
    return urgent_heal or any(actions.get(key) for key in (
        'castPending', 'motionUnconfirmed', 'traveling', 'practicing', 'interactionOwner',
        'autoAttacking', 'autoAttackRequested', 'autoAttackUnconfirmed')) or actions.get('recovery', {}).get('active')


def aura_present(toolkit, guid, spell):
    return any(number(row.get('targetGuid')) == guid and any(number(aura.get('spellId')) == spell
               and aura.get('positive') and (not aura.get('durationKnown') or number(aura.get('remainingMs')) > 0)
               for aura in row.get('auras', [])) for row in toolkit.get('playerAuras', []))


def step(rpc, observations, rests, port, guid, now):
    view = observations.get(port, {})
    world, actions, inventory, trade = (view.get(key, {}) for key in ('world', 'actions', 'inventory', 'trade'))
    own = world.get('self', {})
    session = actions.get('sessionId')
    rest = rests.get(port)
    if rest and session and rest['sessionId'] != session:
        rests.pop(port)
        rest = None
    if not session or not actor(actions, session, guid) or number(own.get('guid')) != guid:
        return 'waiting_for_body'
    if not all(own.get(key) for key in ('present', 'aliveKnown', 'alive', 'powerKnown', 'unitFlagsKnown', 'movementFlagsKnown')):
        finish(rpc, rests, port, actions)
        return 'waiting_for_pose'
    mana, maximum = number(own.get('power')), number(own.get('maxPower'))
    if number(own.get('powerType')) or maximum <= 0 or mana >= maximum:
        finish(rpc, rests, port, actions)
        return 'full' if maximum > 0 and mana >= maximum else 'waiting_for_mana'
    if unsafe(world, own, actions, view.get('party', {})):
        finish(rpc, rests, port, actions)
        return 'waiting_for_safe_rest'
    moving = actions.get('moving') or number(own.get('movementFlags')) & 0xff
    if (moving and (rest or not actions.get('following')) and not (rest and rest['phase'] == 'settling')):
        finish(rpc, rests, port, actions)
        return 'waiting_for_stop'
    if rest and rest['phase'] == 'drinking':
        toolkit = view.get('spell_toolkit') or view.get('toolkit', {})
        aura = toolkit.get('sessionId') == session and aura_present(toolkit, guid, rest['spellId'])
        if aura:
            rest['auraSeen'] = True
            return 'recovering' if mana > rest['baselineMana'] else 'drinking'
        if rest['auraSeen'] or now >= rest['deadline']:
            rest.update(phase='settling', deadline=now + 4)
        else:
            return 'drink_dispatched'
    dungeon = own.get('mapKnown') and number(own.get('mapId')) in DUNGEON_MAPS
    if not rest and not dungeon and mana * 100 >= START_BELOW * maximum:
        return 'ready'
    if (inventory.get('sessionId') != session or number(inventory.get('selfGuid')) != guid
            or inventory.get('drinkingSupported') is not True or inventory.get('active') or inventory.get('closing')):
        finish(rpc, rests, port, actions)
        return 'waiting_for_inventory'
    if str(inventory.get('manaDrinkCount')) == '0':
        finish(rpc, rests, port, actions)
        return 'needs_water'
    spell = actions.get('lastSpellOperation', {})
    if (trade.get('sessionId') != session or trade.get('phase') not in ('IDLE', 'COMPLETE') or trade.get('waterGiftArmed')
            or spell.get('capability') == 'create_item' and spell.get('phase') in (
                'waiting_server', 'waiting_created_item', 'transport_unconfirmed_no_retry')):
        finish(rpc, rests, port, actions)
        return 'waiting_for_trade_or_creation'
    if not rest:
        rest = rests[port] = dict(sessionId=session, restoreFollow=False, phase='settling', deadline=now + 4)
        if actions.get('following'):
            stopped = rpc(port, 'buddy-actions.follow', {'enabled': False}) or {}
            if not actor(stopped, session, guid) or stopped.get('following'):
                finish(rpc, rests, port, actions)
                return 'follow_pause_unconfirmed'
            rest['restoreFollow'] = True
            return 'settling'
    if moving or actions.get('following'):
        if now >= rest['deadline']:
            finish(rpc, rests, port, actions)
        return 'waiting_for_stop'
    rest.update(phase='drinking', deadline=now + 5, auraSeen=False, baselineMana=mana, spellId=0)
    try:
        receipt = rpc(port, 'buddy-inventory.drink', {'sessionId': session, 'selfGuid': str(guid)}) or {}
    except Exception as exc:
        reason = str(exc)
        if 'no_usable_mana_drink_with_confirmed_item_template' in reason:
            finish(rpc, rests, port, actions)
            return 'needs_water'
        if 'FailedPrecondition' in reason or 'InvalidArgument' in reason:
            finish(rpc, rests, port, actions)
            return 'drink_blocked'
        return 'drink_unconfirmed'
    if (actor(receipt, session, guid) and number(receipt.get('spellId')) > 0
            and (receipt.get('transportWritten') or receipt.get('reason') == 'drink_transport_unconfirmed')):
        rest.update(deadline=now + 26, spellId=number(receipt['spellId']), baselineMana=number(receipt.get('baselineMana', mana)))
        return 'drink_dispatched' if receipt.get('transportWritten') else 'drink_unconfirmed'
    return 'drink_unconfirmed'


def maintain_drink(rpc, observations, rests):
    """Mutate active `rests` (integer ports), and return each caster's phase."""
    now = time.monotonic()
    phases = {}
    for port, guid in CASTERS.items():
        try:
            phases[port] = step(rpc, observations, rests, port, guid, now)
        except (ValueError, TypeError, AttributeError):
            phases[port] = 'invalid_observation'
        except Exception:
            phases[port] = 'rest_rpc_unconfirmed'
    return phases
