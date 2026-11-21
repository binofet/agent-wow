"""Evaluate the shared disposal template policy on typed item-lookup facts."""
import importlib.machinery
import importlib.util
import math
import os
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
_loader = importlib.machinery.SourceFileLoader("disposal_task_runtime", str(ROOT / "dev/task-runtime"))
_spec = importlib.util.spec_from_loader(_loader.name, _loader)
runtime = importlib.util.module_from_spec(_spec)
_loader.exec_module(runtime)
BASE_FIELDS = ("itemId", "itemClass", "subclass", "inventoryType", "quality", "flags", "flags2", "sellPrice",
               "requiredSkill", "requiredSkillRank", "requiredSpell", "requiredHonorRank", "requiredCityRank",
               "requiredReputationFaction", "requiredReputationRank")
RAW_FIELDS = ("containerSlots", "bonding", "pageText", "startQuest", "lockId", "maxDurability", "bagFamily", "duration")


def project(item):
    """Keep protobuf message presence before Lua's empty-table conversion."""
    if (not isinstance(item, dict) or item.get("available") is not True
            or item.get("pending", False) is not False or type(item.get("rawTemplateFacts")) is not dict):
        return None
    raw = item["rawTemplateFacts"]
    result = {"available": True, "rawTemplateFacts": {}}
    for keys, source, destination in ((BASE_FIELDS, item, result), (RAW_FIELDS, raw, result["rawTemplateFacts"])):
        for key in keys:
            value = source.get(key, 0)
            if type(value) is not int or not 0 <= value <= 2**32-1:
                return None
            destination[key] = value
    if not 0 < result["itemId"] < 2**31 or type(item.get("hasSpell", False)) is not bool:
        return None
    result["hasSpell"] = item.get("hasSpell", False)
    return result


def _object(value):
    if type(value) is not dict or any(type(key) is not str for key in value):
        raise ValueError("typed object required")
    return value


def _uint(value, maximum=2**32-1):
    if type(value) is not int or not 0 <= value <= maximum:
        raise ValueError("typed unsigned integer required")
    return value


def _counter(value):
    if (type(value) is not str or not 1 <= len(value) <= 20 or not value.isascii() or not value.isdecimal()
            or len(value) > 1 and value[0] == "0" or int(value) > 2**64-1):
        raise ValueError("canonical decimal counter required")
    return value


def _array(value, maximum):
    if type(value) is not list or len(value) > maximum:
        raise ValueError("bounded typed array required")
    return value


def _typed(value, *, uints=(), bools=(), counters=()):
    value, out = _object(value), {}
    for key in uints:
        if key in value:
            out[key] = _uint(value[key])
    for key in bools:
        if key in value:
            if type(value[key]) is not bool:
                raise ValueError("typed boolean required")
            out[key] = value[key]
    for key in counters:
        if key in value:
            out[key] = _counter(value[key])
    return out


def _reference(value):
    value = _object(value)
    return {**{key: _uint(value.get(key, 0)) for key in ("bag", "slot", "entry", "stackCount")},
            "itemGuid": _counter(value.get("itemGuid", "0"))}


def _owned(value):
    return {**_reference(value), **_typed(value,
        uints=("durability", "maxDurability"),
        bools=("metadataKnown", "enhancementsKnown", "hasEnhancements", "durabilityKnown"))}


def _equipment(rows):
    return [_typed(row, uints=("slot", "entry", "stackCount", "durability", "maxDurability"),
        bools=("slotKnown", "metadataKnown", "enhancementsKnown", "hasEnhancements", "durabilityKnown"),
        counters=("itemGuid",)) for row in _array(rows, 19)]


def _item_guid(value):
    value = _counter(value)
    if value != "0" and (int(value) >> 48 != 0x4000 or int(value) & (2**48-1) == 0):
        raise ValueError("actual item GUID required")
    return value


def _layout(rows, source):
    rows = _array(rows, 5)
    if len(rows) != 5:
        raise ValueError("complete carried layout required")
    bags, guids, total = {}, set(), 0
    for row in rows:
        bag = _typed(row, uints=("bag", "entry", "slotCount"),
            bools=("slotKnown", "contentsKnown", "metadataKnown"), counters=("containerGuid",))
        index, count = bag.get("bag", 0), bag.get("slotCount", 0)
        guid = _item_guid(bag.get("containerGuid", "0"))
        slots = _array(row.get("slots", []), 36)
        if (index > 4 or index in bags or count > 36 or len(slots) != count
                or bag.get("slotKnown") is not True or bag.get("contentsKnown") is not True):
            raise ValueError("complete unique bag frame required")
        if index == 0:
            if guid != "0" or bag.get("entry", 0) or bag.get("metadataKnown", False) or count != 16:
                raise ValueError("builtin backpack frame inconsistent")
        elif guid == "0":
            if bag.get("entry", 0) or bag.get("metadataKnown", False) or count:
                raise ValueError("empty bag frame inconsistent")
        elif guid in guids or bag.get("metadataKnown") is not True or not bag.get("entry", 0) or not count:
            raise ValueError("occupied bag frame inconsistent")
        if guid != "0":
            guids.add(guid)
        bags[index] = slots
        total += count
    if total > 160:
        raise ValueError("carried slot count bound")
    selected = None
    for index, slots in bags.items():
        seen = set()
        for row in slots:
            ref = _reference(row)
            metadata = _typed(row, bools=("metadataKnown",)).get("metadataKnown", False)
            guid = _item_guid(ref["itemGuid"])
            if ref["bag"] != index or ref["slot"] >= len(slots) or ref["slot"] in seen:
                raise ValueError("complete unique carried slots required")
            seen.add(ref["slot"])
            if guid == "0":
                if ref["entry"] or ref["stackCount"] or metadata:
                    raise ValueError("empty carried slot inconsistent")
            else:
                if guid in guids or metadata and (not ref["entry"] or not ref["stackCount"]) or not metadata and (ref["entry"] or ref["stackCount"]):
                    raise ValueError("owned carried slot inconsistent")
                guids.add(guid)
            if (ref["bag"], ref["slot"]) == (source["bag"], source["slot"]):
                if ref != source:
                    raise ValueError("exact observed source changed")
                selected = _owned(row)
    if selected is None:
        raise ValueError("observed source location required")
    return selected, guids


def _equipment_match(first, second, carried_guids):
    vectors = []
    for rows in (first, second):
        if len(rows) != 19:
            raise ValueError("complete equipment frame required")
        vector, guids = {}, set(carried_guids)
        for row in rows:
            slot = row.get("slot", 0)
            guid = _item_guid(row.get("itemGuid", "0"))
            entry, count, metadata = row.get("entry", 0), row.get("stackCount", 0), row.get("metadataKnown", False)
            if slot > 18 or slot in vector or row.get("slotKnown") is not True:
                raise ValueError("complete unique equipment slots required")
            if guid == "0":
                if entry or count or metadata:
                    raise ValueError("empty equipment slot inconsistent")
            else:
                if guid in guids or not metadata or not entry or count != 1:
                    raise ValueError("owned equipment slot inconsistent")
                guids.add(guid)
            if row.get("durabilityKnown", False) and row.get("durability", 0) > row.get("maxDurability", 0):
                raise ValueError("equipment durability fields inconsistent")
            vector[slot] = {"itemGuid": guid, "entry": entry, "stackCount": count,
                **{key: row.get(key, False) for key in ("metadataKnown", "enhancementsKnown", "hasEnhancements", "durabilityKnown")},
                **{key: row.get(key, 0) for key in ("durability", "maxDurability")}}
        vectors.append(vector)
    if vectors[0] != vectors[1]:
        raise ValueError("raw equipment frames changed")


def _stats(rows):
    out = []
    for row in _array(rows, 10):
        row = _object(row)
        value = row.get("value", 0)
        if type(value) is not int or not -(2**31) <= value < 2**31:
            raise ValueError("typed signed stat required")
        out.append({"type": _uint(row.get("type", 0)), "value": value})
    return out


def _template(row):
    out = _typed(row, uints=("itemId", "itemClass", "subclass", "inventoryType", "armor", "delayMs",
        "flags2", "scalingDistribution", "scalingValue"),
        bools=("available", "armorComparisonKnown", "hasSpell"))
    out["stats"] = _stats(row.get("stats", []))
    if "resistances" in row:
        out["resistances"] = [_uint(value) for value in _array(row["resistances"], 6)]
    out["damages"] = []
    for damage in _array(row.get("damages", []), 2):
        damage = _object(damage)
        projected = {"school": _uint(damage.get("school", 0))}
        for key in ("minimum", "maximum"):
            value = damage.get(key, 0)
            if type(value) not in (int, float) or not math.isfinite(value) or abs(value) > 2**53-1:
                raise ValueError("finite exact-range damage required")
            projected[key] = value
        out["damages"].append(projected)
    return out


def project_context(context):
    """Retain typed facts referenced by this source, without choosing its use."""
    try:
        context = _object(context)
        inv, world, journal = (_object(context.get(key)) for key in ("inventory", "world", "journal"))
        source = _reference(context.get("source"))
        inventory = _typed(inv, counters=("selfGuid", "inventoryRevision", "equipmentRevision"),
                           bools=("inventoryKnown", "equipmentKnown"))
        if type(inv.get("sessionId")) is not str or not 1 <= len(inv["sessionId"]) <= 128:
            raise ValueError("inventory session required")
        inventory["sessionId"] = inv["sessionId"]
        if inventory.get("inventoryKnown") is not True:
            raise ValueError("known carried layout required")
        carried_source, carried_guids = _layout(inv.get("inventoryBags"), source)
        inventory["equipment"] = _equipment(inv.get("equipment"))
        inventory["items"] = []
        for row in _array(inv.get("items"), 160):
            row = _object(row)
            if _reference(row.get("item")) == source:
                facts = _typed(row.get("gearFacts"), uints=("durability", "maxDurability"),
                    bools=("enhancementsKnown", "hasEnhancements", "durabilityKnown"))
                inventory["items"].append({"item": source.copy(), "gearFacts": facts,
                    **_typed(row, bools=("metadataKnown",))})
        gear_context = _object(inv.get("gearContext"))
        templates = _object(gear_context.get("templates"))
        referenced = {source["entry"], *(_uint(row.get("entry", 0)) for row in inventory["equipment"])}
        inventory["gearContext"] = {"templates": {str(entry): _template(templates[str(entry)])
            for entry in referenced if entry and str(entry) in templates}}
        raw = _typed(world, counters=("inventoryRevision", "selfEquipmentRevision", "selfQuestLogRevision"),
                     bools=("inventoryKnown", "selfEquipmentKnown", "selfQuestLogKnown"))
        raw["self"] = _typed(world.get("self"), counters=("guid",), bools=("present",))
        world_equipment = _equipment(world.get("selfEquipment"))
        if inventory.get("equipmentKnown") is not True or raw.get("selfEquipmentKnown") is not True:
            raise ValueError("known raw equipment frames required")
        _equipment_match(inventory["equipment"], world_equipment, carried_guids)
        raw["selfQuestLog"] = [_typed(row, uints=("slot", "questId"))
            for row in _array(world.get("selfQuestLog"), 25)]
        if type(journal.get("sessionId")) is not str or not 1 <= len(journal["sessionId"]) <= 128:
            raise ValueError("journal session required")
        log = {"sessionId": journal["sessionId"], **_typed(journal, counters=("questLogRevision",))}
        log["entries"] = []
        for row in _array(journal.get("entries", []), 25):
            projected = _typed(row, uints=("questId", "slot"),
                bools=("templateKnown", "logStateKnown", "itemProtectionKnown"))
            if "failure" in row:
                if type(row["failure"]) is not str or len(row["failure"]) > 128:
                    raise ValueError("bounded journal failure required")
                projected["failure"] = row["failure"]
            projected["targets"] = []
            for target in _array(row.get("targets", []), 32):
                target = _object(target)
                kind, target_id = target.get("kind", ""), target.get("targetId", 0)
                if (type(kind) is not str or len(kind) > 32 or type(target_id) is not int
                        or not -(2**31) <= target_id < 2**31):
                    raise ValueError("typed quest objective required")
                projected["targets"].append({"kind": kind, "targetId": target_id,
                    "requiredCount": _uint(target.get("requiredCount", 0))})
            log["entries"].append(projected)
        return {"inventory": inventory, "world": raw, "journal": log, "source": source,
                "carriedSource": carried_source, "facts": {"inventoryLayoutKnown": True, "equipmentFramesMatch": True}}
    except ValueError:
        return None


def classify(item, mode="vendor_sale", *, bridge=None, policy=None, context=None, gear_policy=None):
    facts = project(item)
    if facts is None:
        return {"status": "unknown", "reason": "complete_typed_template_facts_required"}
    frame = {"mode": mode, "item": facts}
    if mode == "gray_armor_destroy":
        projected = project_context(context)
        if projected is None:
            return {"status": "unknown", "reason": "complete_typed_armor_context_required"}
        try:
            facts.update(_typed(item, uints=("armor",), bools=("armorComparisonKnown",)))
            facts["stats"] = _stats(item.get("stats", []))
        except ValueError:
            return {"status": "unknown", "reason": "complete_typed_template_facts_required"}
        frame["context"] = projected
    if bridge is None:
        bridge = runtime.Bridge(os.environ.get("BUDDY_LUAPOLICY_BINARY",
            os.environ.get("BUDDY_LUAPOLICY_TEST_BINARY", ROOT / "data/bin/luapolicy")), ROOT / "modules")
    path = Path(policy) if policy is not None else ROOT / "behaviors/item-disposal.lua"
    try:
        code = runtime.read_file(path, runtime.MAX_SOURCE).decode("utf-8")
        if mode == "gray_armor_destroy":
            gear_path = Path(gear_policy) if gear_policy is not None else ROOT / "behaviors/gear.lua"
            gear = runtime.read_file(gear_path, runtime.MAX_SOURCE).decode("utf-8")
            code = ("local gear=(function()\n" + gear + "\nend)()\nlocal disposal=(function()\n" + code +
                    "\nend)()\nreturn {decide=function(input) return disposal.classify(input.mode,input.item,input.context,gear) end}\n")
            runtime.require(0 < len(code.encode()) <= runtime.MAX_SOURCE, "combined_source_size_bound")
            runtime.packed(frame, runtime.MAX_INPUT)
        result = bridge.evaluate(code, frame)
    except (runtime.Error, OSError, UnicodeError):
        return {"status": "unknown", "reason": "shared_disposal_policy_unavailable"}
    if (not isinstance(result, dict) or set(result) != {"status", "reason"}
            or result["status"] not in ("eligible", "keep", "unknown")
            or not isinstance(result["reason"], str) or not 1 <= len(result["reason"]) <= 128):
        return {"status": "unknown", "reason": "shared_disposal_policy_output_invalid"}
    return result
