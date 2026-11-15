"""Free one carried slot when verified gray junk blocks party looting or water."""

import math
import time


PARTY = {8086: 1, 8087: 4, 8088: 5, 8402: 8}
CASTERS = {1, 4, 8}
LOOT_RANGE = 12
COOLDOWN = 30
RECHECK = 5
RECEIPT_WAIT = 8
IN_COMBAT = 0x80000


def number(value):
    return int(value or 0)


def safe_junk_entries(catalog):
    """Filter the maintained build-12340 catalog to ordinary non-quest misc."""
    return frozenset(int(entry) for entry, item in catalog['items'].items()
                     if item.get('itemClass') == 15 and item.get('subclass') == 0
                     and item.get('inventoryType', 0) == 0
                     and not any(item.get(key) for key in ('questIds', 'startsQuest', 'useSpells', 'containerSlots')))


def carried_slots(inventory):
    bags = inventory.get('inventoryBags', [])
    if inventory.get('inventoryKnown') is not True or len(bags) != 5:
        return None
    rows, seen = {}, set()
    for bag in bags:
        bag_id, count = number(bag.get('bag')), number(bag.get('slotCount'))
        slots = bag.get('slots', [])
        if (not 0 <= bag_id <= 4 or bag_id in seen or (bag_id == 0 and count != 16)
                or not 0 <= count <= 36 or len(slots) != count
                or bag.get('slotKnown') is not True or bag.get('contentsKnown') is not True):
            return None
        seen.add(bag_id)
        for slot in slots:
            index = number(slot.get('slot'))
            key = (bag_id, index)
            if number(slot.get('bag')) != bag_id or not 0 <= index < count or key in rows:
                return None
            rows[key] = slot
    return rows


def nearby_loot(world, actions):
    own = actions.get('position', {})
    if own.get('known') is not True or own.get('mapKnown') is not True:
        return False
    origin = tuple(own.get(axis, 0) for axis in 'xyz')
    for corpse in world.get('lootableCorpses', []):
        if (corpse.get('present') is True and corpse.get('positionKnown') is True
                and corpse.get('mapKnown') is True and number(corpse.get('mapId')) == number(own.get('mapId'))
                and math.dist(origin, tuple(corpse.get(axis, 0) for axis in 'xyz')) <= LOOT_RANGE):
            return True
    return False


def can_dispose(world, actions, inventory, trade, guid):
    own = world.get('self', {})
    if (number(own.get('guid')) != guid
            or any(own.get(key) is not True for key in ('present', 'aliveKnown', 'alive', 'unitFlagsKnown'))
            or number(own.get('unitFlags')) & IN_COMBAT):
        return False
    owner = world.get('owner', {})
    if owner.get('present') and owner.get('unitFlagsKnown') and number(owner.get('unitFlags')) & IN_COMBAT:
        return False
    return (not any(actions.get(key) for key in ('moving', 'traveling', 'motionUnconfirmed', 'castPending',
                'interactionOwner', 'autoAttacking', 'autoAttackRequested', 'autoAttackUnconfirmed', 'practicing'))
            and not actions.get('recovery', {}).get('active') and not inventory.get('active') and not inventory.get('closing')
            and trade.get('phase') in ('IDLE', 'COMPLETE') and not trade.get('waterGiftArmed'))


def candidate(inventory, slots, safe_entries):
    choices = []
    for row in inventory.get('items', []):
        if row.get('safeJunkSaleEligible') is not True or row.get('metadataKnown') is not True:
            continue
        item = row.get('item', {})
        bag, slot = number(item.get('bag')), number(item.get('slot'))
        entry, guid, count = (number(item.get(key)) for key in ('entry', 'itemGuid', 'stackCount'))
        price = number(row.get('unitSellPriceCopper'))
        current = slots.get((bag, slot), {})
        if (entry not in safe_entries or guid >> 48 != 0x4000 or price <= 0 or count <= 0
                or current.get('metadataKnown') is not True or number(current.get('itemGuid')) != guid
                or number(current.get('entry')) != entry or number(current.get('stackCount')) != count):
            continue
        choices.append((count * price, bag, slot, guid, entry))
    return min(choices, default=None)


def step(rpc, views, attempts, safe_entries, port, guid, now):
    view = views.get(port, {})
    world, actions, inventory, trade, loot = (view.get(key, {}) for key in ('world', 'actions', 'inventory', 'trade', 'loot'))
    session = actions.get('sessionId')
    if (not session or number(actions.get('selfGuid')) != guid or number(inventory.get('selfGuid')) != guid
            or inventory.get('sessionId') != session or trade.get('sessionId') != session):
        return 'waiting_for_body'
    record = attempts.get(port)
    if record and record.get('sessionId') != session:
        record = None
        attempts.pop(port)
    slots = carried_slots(inventory)
    if slots is None or not number(inventory.get('inventoryRevision')):
        return 'waiting_for_inventory'
    receipt = trade.get('lastJunkDisposal') or {}
    if record and 'itemGuid' in record:
        if (number(receipt.get('itemGuid')) == record['itemGuid']
                and receipt.get('status') == 'SERVER_SLOT_REMOVAL_CONFIRMED'
                and number(inventory.get('inventoryRevision')) > record['inventoryRevision']
                and all(number(row.get('itemGuid')) != record['itemGuid'] for row in slots.values())):
            attempts[port] = {'sessionId': session, 'nextCheck': now + COOLDOWN}
            return 'confirmed_removed'
        return 'waiting_for_disposal' if now < record['deadline'] else 'disposal_unconfirmed_no_retry'
    if receipt and receipt.get('status') != 'SERVER_SLOT_REMOVAL_CONFIRMED':
        return 'another_disposal_unconfirmed'
    if any(not number(row.get('itemGuid')) for row in slots.values()):
        return 'room_available'
    loot_needs_room = (loot.get('sessionId') == session and loot.get('enabled') is True
                       and loot.get('inventoryPaused') is True and nearby_loot(world, actions))
    water_needs_room = (guid in CASTERS and 'manaDrinkCount' in inventory and inventory['manaDrinkCount'] is not None
                        and number(inventory['manaDrinkCount']) == 0)
    if not loot_needs_room and not water_needs_room:
        return 'no_current_bag_pressure'
    if not can_dispose(world, actions, inventory, trade, guid) or view.get('mana_rest_active'):
        return 'waiting_for_safe_pause'
    if record and now < record['nextCheck']:
        return 'cooldown'
    selected = candidate(inventory, slots, safe_entries)
    if selected is None:
        attempts[port] = {'sessionId': session, 'nextCheck': now + COOLDOWN}
        return 'no_verified_gray_junk'
    _, bag, slot, item_guid, entry = selected
    template = rpc(port, 'buddy-trade.lookup_items', {'itemIds': [entry]}) or {}
    if (template.get('sessionId') != session or not any(
           number(item.get('itemId')) == entry and item.get('available') is True for item in template.get('items', []))):
        attempts[port] = {'sessionId': session, 'nextCheck': now + RECHECK}
        return 'waiting_for_item_template'
    if bag == 0 and (trade.get('backpackKnown') is not True or not number(trade.get('backpackRevision'))):
        return 'waiting_for_trade_revision'
    source = (dict(sourceBag=bag, sourceSlot=slot, inventoryRevision=inventory['inventoryRevision']) if bag else
              dict(backpackSlot=slot, backpackRevision=trade['backpackRevision']))
    attempts[port] = dict(sessionId=session, itemGuid=item_guid, inventoryRevision=number(inventory['inventoryRevision']),
                          deadline=now + RECEIPT_WAIT)
    try:
        rpc(port, 'buddy-trade.destroy_junk', dict(source, sessionId=session, revision=str(number(trade.get('revision'))),
                                                expectedItemGuid=str(item_guid)))
    except Exception as error:
        if 'FailedPrecondition' in str(error) or 'InvalidArgument' in str(error):
            attempts[port] = {'sessionId': session, 'nextCheck': now + RECHECK}
            return 'disposal_refused_before_dispatch'
        return 'disposal_unconfirmed_no_retry'
    return 'waiting_for_disposal'


def maintain_bag_pressure(rpc, observations, attempts, safe_entries):
    """Tick public game-client endpoints with fresh observations; return per-port phases."""
    now, phases = time.monotonic(), {}
    for port, guid in PARTY.items():
        try:
            phases[port] = step(rpc, observations, attempts, safe_entries, port, guid, now)
        except (TypeError, ValueError, KeyError, AttributeError):
            phases[port] = 'invalid_inventory_observation'
        except Exception:
            phases[port] = 'bag_pressure_rpc_unavailable'
    return phases
