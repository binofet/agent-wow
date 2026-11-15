"""Stock the party's casters through Mira's normal conjure and trade RPCs."""

import math
import time

MIRA = 8087
RECIPIENTS = {8086: 1, 8402: 8}
MIRA_GUID = 4
OWNER_GUID = 3
STOCK_MINIMUM = 5
PEER_REORDER_THRESHOLD = 12
IN_COMBAT = 0x80000


def number(value):
    return int(value or 0)


def body_ready(player, guid):
    return (number(player.get("guid")) == guid and player.get("present") is True
            and player.get("aliveKnown") is True and player.get("alive") is True
            and player.get("unitFlagsKnown") is True
            and not number(player.get("unitFlags")) & IN_COMBAT)


def ready_for_trade(observation, guid):
    actions, trade = observation["actions"], observation["trade"]
    session = actions.get("sessionId")
    return (session and number(actions.get("selfGuid")) == guid
            and trade.get("sessionId") == session and trade.get("phase") in ("IDLE", "COMPLETE")
            and not trade.get("waterGiftArmed") and not observation.get("mana_rest_active")
            and not any(actions.get(key) for key in (
                "moving", "motionUnconfirmed", "traveling", "castPending", "interactionOwner",
                "autoAttacking", "autoAttackRequested", "autoAttackUnconfirmed",
            )))


def group_identity(observation, self_guid):
    party = observation["party"]
    group = party.get("group", {})
    if party.get("sessionId") != observation["actions"].get("sessionId"):
        return None
    if group.get("known") is not True or group.get("inGroup") is not True:
        return None
    members = {number(member.get("guid")): member for member in group.get("members", [])}
    own, owner = members.get(self_guid, {}), members.get(OWNER_GUID, {})
    if not (own.get("self") is True and own.get("online") is True and owner.get("online") is True):
        return None
    return group.get("groupGuid")


def stock_count(observation, guid):
    inventory = observation["inventory"]
    if (inventory.get("sessionId") == observation["actions"].get("sessionId")
            and number(inventory.get("selfGuid")) == guid
            and inventory.get("inventoryKnown") is True and "manaDrinkCount" in inventory):
        return number(inventory["manaDrinkCount"])
    return None


def near(first, second):
    if not (first.get("positionKnown") and second.get("positionKnown")
            and first.get("mapKnown") and second.get("mapKnown")
            and number(first.get("mapId")) == number(second.get("mapId"))):
        return False
    a, b = (tuple(row.get(axis, 0) for axis in ("x", "y", "z")) for row in (first, second))
    return math.dist(a, b) <= 10


def trade_position(observation):
    position = observation["actions"].get("position", {})
    if position:
        return dict(position, positionKnown=position.get("known"))
    return observation["world"].get("self", {})


def nearby_player(observation, guid):
    return next((player for player in observation["world"].get("nearbyPlayers", [])
                 if number(player.get("guid")) == guid and player.get("present") is True), {})


def peer_ready(mira, recipient, guid, group):
    return (ready_for_trade(recipient, guid) and group_identity(recipient, guid) == group
            and body_ready(recipient["world"].get("self", {}), guid)
            and near(trade_position(mira), nearby_player(mira, guid))
            and near(trade_position(recipient), nearby_player(recipient, MIRA_GUID)))


def receiving_slots(observation):
    return [slot for bag in observation["inventory"].get("inventoryBags", [])
            if bag.get("contentsKnown") is True
            and number(bag.get("slotCount")) == len(bag.get("slots", []))
            for slot in bag.get("slots", [])]


def receiving_capacity(slots, entry):
    if any(not number(slot.get("itemGuid")) for slot in slots):
        return 20
    return sum(max(0, 20 - number(slot.get("stackCount"))) for slot in slots
               if number(slot.get("entry")) == entry and slot.get("metadataKnown")
               and number(slot.get("stackCount")) > 0)


def water_ranks(rpc, mira):
    session = mira["actions"]["sessionId"]
    toolkit = mira.get("spell_toolkit") or rpc(MIRA, "buddy-actions.spell_toolkit", {})
    if (toolkit.get("sessionId") != session or number(toolkit.get("selfGuid")) != MIRA_GUID
            or toolkit.get("spellbookKnown") is not True):
        return {}
    ranks = {
        number(spell["createdItemEntry"]): (number(spell.get("rank")), number(spell["spellId"]))
        for spell in toolkit.get("spells", [])
        if number(spell.get("familyId")) == 5504 and spell.get("capability") == "create_item"
        and spell.get("createdItemEntry") and not spell.get("blockedReason")
    }
    entries = list(ranks)
    required_level = {}
    for start in range(0, len(entries), 6):
        result = rpc(MIRA, "buddy-trade.lookup_items", {"itemIds": entries[start:start + 6]})
        if result.get("sessionId") != session:
            return {}
        required_level.update((number(item.get("itemId")), number(item.get("requiredLevel")))
                              for item in result.get("items", []) if item.get("available") is True)
    return {item: (rank, spell, required_level[item]) for item, (rank, spell) in ranks.items()
            if item in required_level}


def carried_water(world, usable, spare, slots):
    stacks = []
    if world.get("backpackKnown") and number(world.get("backpackRevision")):
        for item in world.get("backpack", []):
            source = {"backpackRevision": world["backpackRevision"], "backpackSlot": number(item.get("slot"))}
            stacks.append((item, source))
    if world.get("inventoryKnown") and number(world.get("inventoryRevision")):
        for bag in world.get("inventoryBags", []):
            bag_id = number(bag.get("bag"))
            if bag_id and bag.get("contentsKnown"):
                for item in bag.get("slots", []):
                    source = {"inventoryRevision": world["inventoryRevision"], "sourceBag": bag_id,
                              "sourceSlot": number(item.get("slot"))}
                    stacks.append((item, source))
    eligible = [(item, source) for item, source in stacks if item.get("metadataKnown") is True
                and number(item.get("entry")) in usable and number(item.get("itemGuid")) >> 48 == 0x4000
                and 0 < number(item.get("stackCount")) <= min(
                    20, spare, receiving_capacity(slots, number(item.get("entry"))))]
    return max(eligible, key=lambda pair: (usable[number(pair[0]["entry"])][0],
                                          number(pair[0]["stackCount"])), default=None)


def give_water(rpc, mira, port, recipient, item, source):
    recipient_trade = recipient["trade"]
    terms = {"itemEntry": number(item["entry"]), "itemCount": number(item["stackCount"])}
    armed = rpc(port, "buddy-trade.water_gift", dict(
        terms, sessionId=recipient_trade["sessionId"], revision=recipient_trade.get("revision", "0"),
        partnerGuid=str(MIRA_GUID),
    ))
    if armed.get("sessionId") != recipient_trade["sessionId"] or armed.get("waterGiftArmed") is not True:
        return "water_gift_reservation_unconfirmed"
    mira_trade = mira["trade"]
    gift = rpc(MIRA, "buddy-trade.water_gift", dict(
        terms, sessionId=mira_trade["sessionId"], revision=mira_trade.get("revision", "0"),
        partnerGuid=str(RECIPIENTS[port]), expectedItemGuid=item["itemGuid"], **source,
    ))
    if gift.get("sessionId") == mira_trade["sessionId"] and gift.get("waterGiftArmed") is True:
        return "water_trade_started"
    return "water_trade_unconfirmed"


def maintain_supply(rpc, observations, state):
    """Run one bounded pass against fresh world, party, actions, inventory and trade snapshots."""
    try:
        phase = supply_step(rpc, observations, state)
    except Exception as error:
        phase = "water_supply_rpc_error"
        state["error"] = str(error)
    else:
        state.pop("error", None)
    state["phase"] = phase
    return phase


def supply_step(rpc, observations, state):
    now = time.monotonic()
    if now < state.get("next", 0):
        return state.get("phase", "waiting_for_supply_tick")
    state["next"] = now + 4
    mira = observations.get(MIRA)
    if not mira or not ready_for_trade(mira, MIRA_GUID):
        return "mage_busy"
    group = group_identity(mira, MIRA_GUID)
    world = mira["world"]
    if not group or not body_ready(world.get("self", {}), MIRA_GUID):
        return "waiting_for_live_mage_and_party"
    owner = world.get("owner", {})
    if owner.get("present") and not body_ready(owner, OWNER_GUID):
        return "waiting_for_party_combat_to_end"
    own_stock = stock_count(mira, MIRA_GUID)
    if own_stock is None:
        return "waiting_for_mage_water_count"
    peers = []
    for port, guid in RECIPIENTS.items():
        recipient = observations.get(port)
        if not recipient or not peer_ready(mira, recipient, guid, group):
            continue
        drink_count = stock_count(recipient, guid)
        player = recipient["world"]["self"]
        if (drink_count is not None and drink_count < PEER_REORDER_THRESHOLD
                and player.get("levelKnown") is True and player.get("powerKnown") is True
                and number(player.get("powerType")) == 0 and number(player.get("maxPower")) > 0):
            peers.append((drink_count, port, recipient, receiving_slots(recipient)))
    if not peers and own_stock >= STOCK_MINIMUM:
        return "water_stock_sufficient"
    ranks = water_ranks(rpc, mira)

    def compatible(player):
        level = number(player.get("level"))
        return {item: rank for item, rank in ranks.items() if rank[2] <= level}

    needs_bag_space = False
    usable = {}
    for drink_count, port, recipient, slots in sorted(peers):
        available = compatible(recipient["world"]["self"])
        if not available:
            continue
        # Help an empty caster immediately. Conjuring into the same stack
        # cannot make it fit within a reserve, because the trade moves it whole.
        spare = own_stock if drink_count == 0 else own_stock - STOCK_MINIMUM
        stack = carried_water(world, available, spare, slots)
        if stack:
            return give_water(rpc, mira, port, recipient, *stack)
        if any(not number(slot.get("itemGuid")) for slot in slots):
            usable = available
            break
        needs_bag_space = True
    if not usable and own_stock < STOCK_MINIMUM:
        if world["self"].get("levelKnown") is not True:
            return "waiting_for_caster_level"
        usable = compatible(world["self"])
    if not usable:
        return "recipient_needs_bag_space" if needs_bag_space else "no_compatible_conjure_water_rank"
    actions = mira["actions"]
    last = actions.get("lastSpellOperation", {})
    if last.get("capability") == "create_item" and last.get("phase") in (
        "waiting_server", "waiting_created_item", "transport_unconfirmed_no_retry",
    ):
        return "conjure_pending"
    own_slots = receiving_slots(mira)
    has_room = {item: rank for item, rank in usable.items()
                if receiving_capacity(own_slots, item) > 0}
    if not has_room:
        return "mage_needs_bag_space"
    _, spell, _ = max(has_room.values())
    if number(world["self"].get("power")) == 0:
        return "waiting_for_mana_to_conjure"
    result = rpc(MIRA, "buddy-actions.create_item", {
        "sessionId": actions["sessionId"], "selfGuid": str(MIRA_GUID), "spellId": spell,
    })
    if result.get("sessionId") == actions["sessionId"]:
        return "conjure_requested"
    return "conjure_unconfirmed"
