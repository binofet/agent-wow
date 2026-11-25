"""Search the pinned public item catalog against observed character requirements."""

from collections import Counter, namedtuple
from functools import lru_cache
import gzip
import hashlib
import json
from pathlib import Path
from typing import NamedTuple


DATA = Path(__file__).resolve().parents[1] / "pkg"
KINDS = ("gear", "armor", "weapon", "consumable", "potion", "healing_potion",
         "mana_potion", "mana_drink", "elixir", "flask", "food_drink", "scroll", "bandage")
CONSUMABLE_SUBCLASSES = {"potion": 1, "healing_potion": 1, "mana_potion": 1,
                         "elixir": 2, "flask": 3, "scroll": 4, "food_drink": 5, "mana_drink": 5, "bandage": 7}
SLOT_TYPES = {"head": (1,), "neck": (2,), "shoulders": (3,), "shirt": (4,), "chest": (5, 20),
              "waist": (6,), "legs": (7,), "feet": (8,), "wrists": (9,), "hands": (10,),
              "finger": (11,), "trinket": (12,), "back": (16,), "main_hand": (13, 17, 21),
              "off_hand": (13, 14, 22, 23), "ranged": (15, 25, 26, 28), "tabard": (19,)}
# Wrath ItemTemplate::GetSkill, indexed by item subclass. Obsolete/exotic
# weapon types without a player proficiency are excluded from general gear.
WEAPON_SKILLS = {0: 44, 1: 172, 2: 45, 3: 46, 4: 54, 5: 160, 6: 229, 7: 43,
                 8: 55, 10: 136, 13: 473, 15: 173, 16: 176, 18: 226, 19: 228, 20: 356}
ARMOR_SKILLS = {1: 415, 2: 414, 3: 413, 4: 293, 6: 433}
ALLIANCE = {1, 3, 4, 7, 11}
HORDE = {2, 5, 6, 8, 10}
ALL_RACES = sum(1 << (race - 1) for race in ALLIANCE | HORDE)
STAT_NAMES = {0: "mana", 1: "health", 3: "agility", 4: "strength", 5: "intellect",
              6: "spirit", 7: "stamina", 38: "attack power", 45: "spell power"}


@lru_cache(maxsize=1)
def requirements():
    root = DATA / "gamecatalog"
    with gzip.open(root / "item-requirements.json.gz", "rt", encoding="utf-8") as stream:
        index = json.load(stream)
    if (index["schemaVersion"] != 1 or hashlib.sha256((root / "catalog.json").read_bytes()).hexdigest()
            != index["provenance"]["catalogSHA256"]):
        raise ValueError("item_requirements_and_catalog_version_mismatch")
    row_type = namedtuple("ItemRequirements", index["columns"])
    return {row.itemId: row for row in map(row_type._make, index["rows"])}


class UseSpell(NamedTuple):
    name: str
    heals: bool
    restores_mana: bool
    battleground_only: bool
    mana_per_tick: int


def _drink_power(spell, effects):
    # Ordinary stationary mana drinks use a self mana-regeneration aura.
    # Some encode its amount in a second periodic dummy. Keep instant potions
    # distinct. Runtime Inventory confirms a carried item's drinkSupported.
    if (not spell.get("durationIndex") or spell.get("castMs") or spell.get("targetMask") or not effects
            or not any(e["aura"] == 85 for e in effects)):
        return 0
    for e in effects:
        if (e["kind"] != 6 or e["a"] != 1 or e["b"] or e["misc"] or e["trigger"] or e["chain"] or e["radius"]
                or e["aura"] not in (85, 226) or (e["aura"] == 226 and not e["periodMs"])):
            return 0
    return max(0, *(e["points"] for e in effects))


@lru_cache(maxsize=1)
def use_spells():
    # Keep the spell reference out of prompts. Only names and mechanics needed
    # for a returned consumable survive parsing the source-pinned spell rows.
    result = {}
    with (DATA / "spellcatalog" / "spells.jsonl").open(encoding="utf-8") as stream:
        for line in stream:
            spell = json.loads(line)
            effects = spell.get("effects", ())
            result[spell["id"]] = UseSpell(
                spell["name"],
                any(e["kind"] in (10, 67, 136) and e["a"] == 1 for e in effects),
                any(e["kind"] in (30, 137) and e["misc"] == 0 and e["a"] == 1 for e in effects),
                bool(spell["attrs"][3] & 0x800),  # SPELL_ATTR3_ONLY_BATTLEGROUNDS
                _drink_power(spell, effects),
            )
    return result


def search_arguments(args):
    kind = args.get("kind")
    ids = args.get("item_ids", [])
    text = args.get("name", "")
    slot = args.get("slot")
    offset, limit = args.get("offset", 0), args.get("limit", 6)
    if kind is not None and kind not in KINDS:
        raise ValueError("unknown_item_kind")
    if not isinstance(ids, list) or len(ids) > 12 or any(type(i) is not int or not 0 < i < 2**32 for i in ids):
        raise ValueError("item_ids_must_be_up_to_12_positive_item_entries")
    if not kind and not ids:
        raise ValueError("supply_kind_for_search_or_item_ids_to_check")
    if not isinstance(text, str) or len(text) > 120:
        raise ValueError("item_name_search_must_be_short_text")
    if slot is not None and (kind not in ("gear", "weapon", "armor") or slot not in SLOT_TYPES):
        raise ValueError("slot_requires_gear_search_and_valid_equipment_slot")
    if type(limit) is not int or not 1 <= limit <= 12 or type(offset) is not int or offset < 0:
        raise ValueError("invalid_item_search_page")
    return kind, ids, text.casefold().split(), slot, offset, limit


def kind_matches(item, kind, spells):
    category, sub = item["itemClass"], item["subclass"]
    if kind in ("gear", "armor", "weapon"):
        return category in {"gear": (2, 4), "armor": (4,), "weapon": (2,)}[kind] and bool(item["inventoryType"])
    if category != 0 or (kind != "consumable" and sub != CONSUMABLE_SUBCLASSES[kind]):
        return False
    if kind == "healing_potion":
        return any(s.heals for s in spells)
    if kind == "mana_potion":
        return any(s.restores_mana for s in spells)
    if kind == "mana_drink":
        return len(spells) == 1 and spells[0].mana_per_tick > 0
    return True


def unmet_requirement(item, facts, who, spells, slot):
    if facts.requiredLevel is None:
        return "required_level_unknown"
    if facts.requiredLevel > who["level"]:
        return "required_level_too_high"
    if facts.allowableClass is None or not facts.allowableClass & (1 << (who["classId"] - 1)):
        return "class_restriction_or_unknown"
    race = who.get("raceId")
    if facts.allowableRace is None:
        return "race_restriction_unknown"
    if race and not facts.allowableRace & (1 << (race - 1)):
        return "race_restriction"
    if not race and (facts.allowableRace & ALL_RACES) != ALL_RACES:
        return "recipient_race_unobserved"
    if facts.flagsExtra is None or facts.flags is None:
        return "item_flags_unknown"
    if facts.flagsExtra & 1 and race not in HORDE or facts.flagsExtra & 2 and race not in ALLIANCE:
        return "faction_restriction_or_unknown"
    if (facts.flagsExtra & 0x800 or facts.flags & 0x10 or
            item["name"].casefold().startswith(("monster -", "[ph]", "zzold", "deprecated", "test ", "qa test"))):
        return "internal_or_deprecated_item"
    if any(value != 0 for value in (facts.requiredSpell, facts.requiredHonorRank, facts.requiredCityRank,
            facts.requiredReputationFaction, facts.requiredReputationRank, facts.area, facts.map, facts.holiday)):
        return "special_training_reputation_rank_or_location_unverified"
    if item["itemClass"] == 0:
        if not spells or len(spells) != len(item.get("useSpells", ())):
            return "consumable_effect_unknown"
        if any(s.battleground_only for s in spells):
            return "requires_battleground"
        if all((s.restores_mana or s.mana_per_tick) and not s.heals for s in spells) and who.get("powerType") != 0:
            return "mana_not_current_resource" if who.get("powerType") is not None else "current_resource_unknown"
    if item["itemClass"] not in (0, 2, 4) or (item["itemClass"] != 0 and not item["inventoryType"]):
        return "outside_gear_and_consumables"
    skills = who.get("skills")
    if facts.requiredSkill is None or facts.requiredSkillRank is None:
        return "required_skill_unknown"
    needed = [(facts.requiredSkill, max(1, facts.requiredSkillRank))] if facts.requiredSkill else []
    if item["itemClass"] == 2:
        proficiency = WEAPON_SKILLS.get(item["subclass"])
        if proficiency is None:
            return "unverified_weapon_proficiency"
        needed.append((proficiency, 1))
        if slot == "off_hand" or item["inventoryType"] == 22:
            needed.append((118, 1))  # Dual Wield
    if item["itemClass"] == 4 and item["subclass"] in ARMOR_SKILLS:
        needed.append((ARMOR_SKILLS[item["subclass"]], 1))
    if needed and skills is None:
        return "recipient_proficiency_unobserved"
    if any(skills.get(skill, 0) < rank for skill, rank in needed):
        return "required_skill_or_proficiency_not_met"
    return None


def _detail(item, r, spells):
    row = {"itemId": r.itemId, "name": item["name"], "requiredLevel": r.requiredLevel,
           "itemLevel": r.itemLevel, "quality": r.quality}
    if item["itemClass"] == 0:
        row["useSpells"] = [{"id": spell_id, "name": spells[spell_id].name}
                            for spell_id in item.get("useSpells", ()) if spell_id in spells]
        if len(row["useSpells"]) == 1:
            mana = spells[row["useSpells"][0]["id"]].mana_per_tick
            if mana:
                row["manaPerTick"] = mana
    else:
        row.update(inventoryType=item["inventoryType"], subclass=item["subclass"], armor=r.armor,
                   stats=[{"stat": STAT_NAMES.get(stat_id, stat_id), "value": value}
                          for _, stat_id, value in r.stats])
        if r.damage:
            row.update(damage=r.damage, delayMs=r.delayMs)
    return row


def find(catalog, recipient, args):
    kind, ids, words, slot, offset, limit = search_arguments(args)
    templates, spells = requirements(), use_spells()
    records = catalog["items"]
    eligible, excluded, checked = [], Counter(), []

    def item_spells(item):
        return [spells[i] for i in item.get("useSpells", ()) if i in spells] if item["itemClass"] == 0 else []

    for item_id in dict.fromkeys(ids):
        item, facts = records.get(str(item_id)), templates.get(item_id)
        if item is None or facts is None:
            checked.append({"itemId": item_id, "matches": False, "reason": "unknown_item_id"})
            continue
        reason = unmet_requirement(item, facts, recipient, item_spells(item), slot)
        checked.append({"itemId": item_id, "name": item["name"], "requiredLevel": facts.requiredLevel,
                        "matches": reason is None, "reason": reason})

    if kind is not None:
        for item_id, facts in templates.items():
            item = records[str(item_id)]
            effects = item_spells(item)
            if not kind_matches(item, kind, effects):
                continue
            if slot and item["inventoryType"] not in SLOT_TYPES[slot]:
                continue
            if words and not all(word in item["name"].casefold() for word in words):
                continue
            reason = unmet_requirement(item, facts, recipient, effects, slot)
            if kind in ("mana_potion", "mana_drink") and recipient.get("powerType") != 0:
                reason = "mana_not_current_resource" if recipient.get("powerType") is not None else "current_resource_unknown"
            if reason:
                excluded[reason] += 1
            else:
                eligible.append(facts)
    eligible.sort(key=lambda r: (-r.requiredLevel, -r.itemLevel, r.itemId))
    matches = [_detail(records[str(r.itemId)], r, spells) for r in eligible[offset:offset + limit]]
    return {"recipient": {k: v for k, v in recipient.items() if k != "skills"},
            "proficienciesKnown": recipient.get("skills") is not None,
            "catalog": {"build": catalog["build"], "sourceCommit": catalog["sourceCommit"]},
            "scope": "Published template requirements and observed character facts. Compare gear stats with the worn slot for upgrades; realm overrides, actual delivery, equipping and use need separate evidence.",
            "matches": matches, "totalMatches": len(eligible), "excludedByFirstReason": dict(excluded),
            "nextOffset": offset + len(matches) if offset + len(matches) < len(eligible) else None,
            "checked": checked, "gameEffectClaimed": False}
