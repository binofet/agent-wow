#!/usr/bin/env python3
"""Pure selection of an actual quest reward from complete server item facts.

This selects a useful reward, not an equipment upgrade: no equipped inventory
baseline is available. It neither equips nor sells anything. Weapon subclasses
must be supplied from actual character proficiency evidence, never class guesses.

Enums and use restrictions follow AzerothCore ItemTemplate.h / PlayerStorage.cpp:
https://github.com/azerothcore/azerothcore-wotlk/tree/master/src/server/game/Entities
"""

import math


_UINT32 = (1 << 32) - 1
_RACES = {1, 2, 3, 4, 5, 6, 7, 8, 10, 11}
_RACE_MASK = sum(1 << (race - 1) for race in _RACES)
_ALLIANCE = {1, 3, 4, 7, 11}
_BODY_SLOTS = {1, 3, 5, 6, 7, 8, 9, 10, 20}
_COMMON_SLOTS = {2, 11, 12, 16}
_WEAPON_SLOTS = {13, 15, 17, 21, 25, 26}
_CASTER_STATS = {0: .02, 1: .01, 5: 4, 6: 3, 7: 2, 18: 3, 21: 3,
                 30: 3, 31: 3, 32: 3, 36: 3, 41: 2, 42: 5, 43: 3,
                 45: 5, 46: 1, 47: 1}
_ROGUE_STATS = {1: .01, 3: 4, 4: 2, 7: 2, 13: 1, 16: 3, 19: 3,
                28: 3, 31: 3, 32: 3, 36: 3, 37: 3, 38: 2, 44: 2, 46: 1}
_UINT_FIELDS = ("itemClass", "subclass", "inventoryType", "quality", "itemLevel",
                "requiredLevel", "buyPrice", "sellPrice", "allowableClass",
                "allowableRace", "requiredSkill", "requiredSkillRank", "requiredSpell",
                "armor", "delayMs", "flags", "flags2", "requiredReputationFaction",
                "requiredReputationRank", "requiredHonorRank", "requiredCityRank")


def _uint(value, maximum=_UINT32):
    return type(value) is int and 0 <= value <= maximum


def _result(status, reason, choice=None):
    return {"status": status, "choice": choice, "reason": reason}


def _metadata_rows(items):
    if isinstance(items, dict) and "items" in items:
        items = items["items"]
    if isinstance(items, dict):
        if len(items) > 128:
            raise ValueError
        rows = []
        for key, row in items.items():
            if type(key) is str and key.isascii() and key.isdecimal() and str(int(key)) == key:
                key = int(key)
            if not _uint(key) or not isinstance(row, dict) or row.get("itemId") != key:
                raise ValueError
            rows.append(row)
    elif isinstance(items, (list, tuple)) and len(items) <= 128:
        rows = items
    else:
        raise ValueError
    result = {}
    for row in rows:
        if not isinstance(row, dict):
            raise ValueError
        item_id = row.get("itemId")
        if not _uint(item_id, (1 << 31) - 1) or not item_id or item_id in result:
            raise ValueError
        result[item_id] = row
    return result


def _validate_item(item):
    # Proto3 omits zero scalar fields; only Available proves a full server frame.
    for field in _UINT_FIELDS:
        if not _uint(item.get(field, 0)):
            return False
    if not isinstance(item.get("name", ""), str) or len(item.get("name", "")) > 512:
        return False
    stats, damages = item.get("stats", []), item.get("damages", [])
    if not isinstance(stats, list) or len(stats) > 10:
        return False
    if not isinstance(damages, list) or len(damages) > 2:
        return False
    for stat in stats:
        if (not isinstance(stat, dict) or not _uint(stat.get("type", 0)) or
                type(stat.get("value", 0)) is not int or
                not -(1 << 31) <= stat.get("value", 0) < (1 << 31)):
            return False
    for damage in damages:
        if not isinstance(damage, dict) or not _uint(damage.get("school", 0), 6):
            return False
        low, high = damage.get("minimum", 0), damage.get("maximum", 0)
        if (type(low) not in (int, float) or type(high) not in (int, float) or
                not math.isfinite(low) or not math.isfinite(high) or not 0 <= low <= high):
            return False
    return True


def _requirements_met(item, class_id, level, race_id):
    if not item.get("allowableClass", 0) & (1 << (class_id - 1)):
        return False
    race_mask = item.get("allowableRace", 0)
    if race_id is None:
        if race_mask & _RACE_MASK != _RACE_MASK:
            return False
    elif not race_mask & (1 << (race_id - 1)):
        return False
    flags2 = item.get("flags2", 0)
    if flags2 & 3:
        if race_id is None or (flags2 & 1 and race_id in _ALLIANCE) or (flags2 & 2 and race_id not in _ALLIANCE):
            return False
    required_level = item.get("requiredLevel", 0)
    if required_level and (level is None or level < required_level):
        return False
    # These prerequisites are not exposed as actual character knowledge here.
    if any(item.get(field, 0) for field in ("requiredSkill", "requiredSkillRank",
                                           "requiredSpell", "requiredReputationFaction",
                                           "requiredReputationRank", "requiredHonorRank",
                                           "requiredCityRank")):
        return False
    return not item.get("flags", 0) & 0x10  # Deprecated/unusable item.


def _useful_score(item, class_id, level, race_id, weapon_subclasses):
    if not _requirements_met(item, class_id, level, race_id):
        return None
    kind, subclass, slot = (item.get(key, 0) for key in ("itemClass", "subclass", "inventoryType"))
    is_weapon = kind == 2 and slot in _WEAPON_SLOTS and subclass in weapon_subclasses
    if kind == 4:
        if slot in _COMMON_SLOTS:
            if subclass not in ({0, 1, 2} if class_id == 4 else {0, 1}):
                return None
        elif slot in _BODY_SLOTS:
            if subclass not in ({1, 2} if class_id == 4 else {1}):
                return None
        elif slot == 23 and class_id in {5, 8} and subclass in {0, 1}:
            pass  # Caster held-in-off-hand item; not a weapon or shield.
        else:
            return None
    elif not is_weapon:
        return None
    weights = _ROGUE_STATS if class_id == 4 else _CASTER_STATS
    score = 10 + item.get("armor", 0) * .15
    score += sum(weights.get(stat.get("type", 0), 0) * stat.get("value", 0)
                 for stat in item.get("stats", []))
    if is_weapon and item.get("delayMs", 0):
        dps = sum((damage.get("minimum", 0) + damage.get("maximum", 0)) / 2
                  for damage in item.get("damages", [])) * 1000 / item["delayMs"]
        score += dps * (4 if class_id == 4 else 1)
    return score


def _label(item):
    # Names are display data, never commands or ranking input.
    name = " ".join("".join(char if char.isprintable() else " "
                            for char in item.get("name", "")).split())[:100]
    return name or f"item {item['itemId']}"


def select_reward(choices, items, class_id, *, level=None, race_id=None, weapon_subclasses=()):
    """Return {status, choice, reason}; choice is an unchanged actual offer row.

    ``items`` accepts a lookup response, its items list, or an item-ID keyed map.
    Any unavailable offered item pauses selection. ``weapon_subclasses`` must be
    actual learned proficiencies. No absent baseline is treated as an empty slot.
    """
    if not isinstance(choices, (list, tuple)) or not 1 <= len(choices) <= 6:
        return _result("invalid", "Expected one to six actual server reward choices.")
    indices, ids = set(), set()
    for choice in choices:
        if not isinstance(choice, dict):
            return _result("invalid", "Malformed server reward choice.")
        index, item_id, count = choice.get("index", 0), choice.get("itemId"), choice.get("count")
        if (not _uint(index, 5) or not _uint(item_id, (1 << 31) - 1) or not item_id or
                not _uint(count) or not count or index in indices or item_id in ids):
            return _result("invalid", "Reward choice indices, item IDs, or counts are invalid.")
        indices.add(index)
        ids.add(item_id)
    try:
        metadata = _metadata_rows(items)
    except (ValueError, TypeError, OverflowError):
        return _result("invalid", "Malformed server item lookup response.")
    for item_id in sorted(ids):
        item = metadata.get(item_id)
        if item is None or item.get("available") is not True or item.get("pending", False) is not False:
            return _result("pending", f"Waiting for complete server item details for item {item_id}.")
        if not _validate_item(item):
            return _result("invalid", f"Invalid server item details for item {item_id}.")
    if type(class_id) is not int or class_id not in {4, 5, 8}:
        return _result("pending", "Character class is unknown or unsupported for reward selection.")
    if level is not None and (not _uint(level, 255) or level == 0):
        return _result("invalid", "Character level is invalid.")
    if race_id is not None and (type(race_id) is not int or race_id not in _RACES):
        return _result("invalid", "Character race is invalid.")
    if not isinstance(weapon_subclasses, (tuple, list, set, frozenset)) or len(weapon_subclasses) > 21:
        return _result("invalid", "Invalid known weapon proficiencies.")
    if any(not _uint(subclass, 20) for subclass in weapon_subclasses):
        return _result("invalid", "Invalid known weapon proficiencies.")
    ranked = []
    for choice in choices:
        item = metadata[choice["itemId"]]
        score = _useful_score(item, class_id, level, race_id, weapon_subclasses)
        if score is not None and score > 0:
            ranked.append((score, item.get("sellPrice", 0) * choice["count"], -choice["itemId"], -choice.get("index", 0), choice))
    if ranked:
        choice = max(ranked, key=lambda row: row[:4])[-1]
        return _result("selected", f"Choose {_label(metadata[choice['itemId']])} for useful equipment and stats.", choice)
    choice = max(choices, key=lambda row: (metadata[row["itemId"]].get("sellPrice", 0) * row["count"], -row["itemId"], -row.get("index", 0)))
    value = metadata[choice["itemId"]].get("sellPrice", 0) * choice["count"]
    return _result("selected", f"Choose {_label(metadata[choice['itemId']])} for its sell value ({value} copper).", choice)
