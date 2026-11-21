"""Evaluate the shared disposal template policy on typed item-lookup facts."""
import importlib.machinery
import importlib.util
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


def classify(item, mode="vendor_sale", *, bridge=None, policy=None):
    facts = project(item)
    if facts is None:
        return {"status": "unknown", "reason": "complete_typed_template_facts_required"}
    if bridge is None:
        bridge = runtime.Bridge(os.environ.get("BUDDY_LUAPOLICY_BINARY",
            os.environ.get("BUDDY_LUAPOLICY_TEST_BINARY", ROOT / "data/bin/luapolicy")), ROOT / "modules")
    path = Path(policy) if policy is not None else ROOT / "behaviors/item-disposal.lua"
    try:
        code = runtime.read_file(path, runtime.MAX_SOURCE).decode("utf-8")
        result = bridge.evaluate(code, {"mode": mode, "item": facts})
    except (runtime.Error, OSError, UnicodeError):
        return {"status": "unknown", "reason": "shared_disposal_policy_unavailable"}
    if (not isinstance(result, dict) or set(result) != {"status", "reason"}
            or result["status"] not in ("eligible", "keep", "unknown")
            or not isinstance(result["reason"], str) or not 1 <= len(result["reason"]) <= 128):
        return {"status": "unknown", "reason": "shared_disposal_policy_output_invalid"}
    return result
