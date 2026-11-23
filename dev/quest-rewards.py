#!/usr/bin/env python3
"""Pure selection of an actual quest reward from complete server item facts.

This selects a useful reward, not an equipment upgrade: no equipped inventory
baseline is available. It neither equips nor sells anything. Weapon subclasses
must be supplied from actual character proficiency evidence, never class guesses.

Enums and use restrictions follow AzerothCore ItemTemplate.h / PlayerStorage.cpp:
https://github.com/azerothcore/azerothcore-wotlk/tree/master/src/server/game/Entities
"""

import hashlib
import importlib.machinery
import importlib.util
import math
import os
from pathlib import Path
import re


_UINT32 = (1 << 32) - 1
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


def _label(item):
    # Names are display data, never commands or ranking input.
    name = " ".join("".join(char if char.isprintable() else " "
                            for char in item.get("name", "")).split())[:100]
    return name or f"item {item['itemId']}"


def _evaluate(facts):
    loader = importlib.machinery.SourceFileLoader("quest_reward_task_runtime", str(Path(__file__).with_name("task-runtime")))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    runtime = importlib.util.module_from_spec(spec)
    loader.exec_module(runtime)
    root = Path(__file__).resolve().parent.parent
    binary = os.environ.get("BUDDY_LUAPOLICY_BINARY", os.environ.get("BUDDY_LUAPOLICY_TEST_BINARY", root / "data/bin/luapolicy"))
    try:
        return runtime.Bridge(binary, root / "modules").evaluate(
            (root / "behaviors/quest-rewards.lua").read_text(), facts)
    except (runtime.Error, OSError, UnicodeError):
        return {"status": "pending", "reason": "Waiting for the shared Lua reward policy."}


class AcceptedRewardPolicy:
    """Read one actor publication; current() is a bounded dispatch preflight."""

    def __init__(self, rpc, session_id, character_guid, *, bridge=None):
        self.rpc = rpc
        self.identity = dict(sessionId=session_id, characterGuid=str(character_guid))
        if (not isinstance(session_id, str) or not 1 <= len(session_id) <= 128
                or re.fullmatch('[1-9][0-9]{0,19}', self.identity['characterGuid']) is None
                or int(self.identity['characterGuid']) > 2**64-1):
            raise ValueError('current_reward_policy_body_required')
        loader = importlib.machinery.SourceFileLoader('quest_reward_accepted_runtime', str(Path(__file__).with_name('task-runtime')))
        spec = importlib.util.spec_from_loader(loader.name, loader)
        self.runtime = importlib.util.module_from_spec(spec)
        loader.exec_module(self.runtime)
        self.source, self.revision, self.generation, limit = self._accepted()
        self.wrapped = ('local policy=(function()\n'+self.source+'\nend)()\n'
            'return {decide=function(input) '
            'if type(policy.quest_reward_choice)~="function" then return {available=false} end '
            'return {available=true,decision=policy.quest_reward_choice(input)} end}\n')
        root = Path(__file__).resolve().parent.parent
        self.bridge = bridge or self.runtime.Bridge(os.environ.get('BUDDY_LUAPOLICY_BINARY',
            os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY', root/'data/bin/luapolicy')), root/'modules',
            source_bytes=max(limit, len(self.wrapped.encode())), execution_ms=1000)

    def _accepted(self):
        view = self.rpc.call('session.policy_source', self.identity)
        source, revision = view.get('source'), view.get('revision')
        generation = str(view.get('generation', ''))
        limit = view.get('sourceLimitBytes', self.runtime.MAX_SOURCE)
        if (view.get('sessionId') != self.identity['sessionId']
                or str(view.get('characterGuid')) != self.identity['characterGuid']
                or not isinstance(source, str) or not source or not isinstance(revision, str)
                or re.fullmatch('[a-f0-9]{64}', revision) is None
                or hashlib.sha256(source.encode()).hexdigest() != revision
                or re.fullmatch('[1-9][0-9]{0,19}', generation) is None or int(generation) > 2**64-1
                or type(limit) is not int or limit <= 0 or len(source.encode()) > limit):
            raise ValueError('current_reward_policy_source_required')
        return source, revision, generation, limit

    def evaluate(self, facts):
        try:
            result = self.bridge.evaluate(self.wrapped, facts)
        except (self.runtime.Error, OSError, UnicodeError) as error:
            raise ValueError('accepted_reward_policy_unavailable') from error
        if (not isinstance(result, dict) or result.get('available') is not True
                or not isinstance(result.get('decision'), dict)):
            raise ValueError('quest_reward_choice_export_required')
        return result['decision']

    def current(self):
        # Native ClaimReward still atomically validates the offer, not a policy
        # revision. Keep this publication check nearest the host write boundary.
        try:
            _, revision, generation, _ = self._accepted()
            attached = self.rpc.call('session.attach', self.identity)
            policy = attached.get('policy', {})
            return (revision == self.revision and generation == self.generation
                    and attached.get('sessionId') == self.identity['sessionId']
                    and str(attached.get('characterGuid')) == self.identity['characterGuid']
                    and policy.get('faulted', False) is False
                    and policy.get('revision') == revision and str(policy.get('generation')) == generation)
        except Exception:
            return False


def preferred_reward(choices, quest_id, class_id):
    """Resolve the Lua policy's exact offered preference without item inference."""
    decision = _evaluate(dict(mode="offer_preference", choices=choices, questId=quest_id, classId=class_id))
    position = decision.get("position")
    if decision.get("status") == "selected" and type(position) is int and 1 <= position <= len(choices):
        choice = choices[position - 1]
        if decision.get("itemId") == choice.get("itemId"):
            return choice
    return None


def starter_handoff(facts):
    """Transport the starter handoff's observations to the same Lua policy."""
    return _evaluate(dict(facts, mode="starter_handoff"))


def select_reward(choices, items, class_id, *, level=None, race_id=None, weapon_subclasses=(),
                  quest_id=None, evaluate=None):
    """Return {status, choice, reason}; choice is an unchanged actual offer row.

    ``items`` accepts a lookup response, its items list, or an item-ID keyed map.
    Useful ranking requires every offered item's metadata. An accepted evaluator
    may first choose an exact quest preference with ``items=None``; no equipment
    or value claim is accepted from that frame. ``weapon_subclasses`` must be
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
    offer_only = items is None and evaluate is not None
    try:
        metadata = {} if offer_only else _metadata_rows(items)
    except (ValueError, TypeError, OverflowError):
        return _result("invalid", "Malformed server item lookup response.")
    for item_id in (() if offer_only else sorted(ids)):
        item = metadata.get(item_id)
        if item is None or item.get("available") is not True or item.get("pending", False) is not False:
            return _result("pending", f"Waiting for complete server item details for item {item_id}.")
        if not _validate_item(item):
            return _result("invalid", f"Invalid server item details for item {item_id}.")
    if not isinstance(weapon_subclasses, (tuple, list, set, frozenset)) or len(weapon_subclasses) > 21:
        return _result("invalid", "Invalid known weapon proficiencies.")
    if any(not _uint(subclass, 20) for subclass in weapon_subclasses):
        return _result("invalid", "Invalid known weapon proficiencies.")
    if level is not None and (not _uint(level, 255) or level == 0):
        return _result("invalid", "Character level is invalid.")
    if race_id is not None and type(race_id) is not int:
        return _result("invalid", "Character race is invalid.")
    # The existing evaluator accepts only bounded facts. Display names and
    # unoffered metadata stay outside the decision input.
    rows = []
    for item_id in (() if offer_only else sorted(ids)):
        item = metadata[item_id]
        damages = []
        for damage in item.get("damages", []):
            row = {"school": damage.get("school", 0)}
            for field in ("minimum", "maximum"):
                value = damage.get(field, 0)
                # The evaluator's JSON-number bound protects exact identities.
                # Preserve larger finite damage facts as decimal text.
                if value > (1 << 53) - 1:
                    row[field + "Decimal"] = format(value, ".17e")
                else:
                    row[field] = value
            damages.append(row)
        rows.append(dict(itemId=item_id, available=True,
                         **{key: item.get(key, 0) for key in _UINT_FIELDS},
                         stats=item.get("stats", []), damages=damages))
    facts = dict(mode="useful", choices=list(choices), items=None if offer_only else rows, classId=class_id if type(class_id) is int else None,
                 level=level, raceId=race_id, weaponSubclasses=list(weapon_subclasses))
    if quest_id is not None:
        if not _uint(quest_id) or quest_id == 0:
            return _result("invalid", "Invalid observed quest identity.")
        facts['questId'] = quest_id
    decision = (evaluate or _evaluate)(facts)
    if not isinstance(decision, dict):
        return _result("invalid", "Invalid Lua reward decision.")
    if decision.get("status") != "selected":
        status = decision.get("status")
        return _result(status if status in ("pending", "invalid") else "invalid", str(decision.get("reason", "Invalid Lua reward decision.")))
    position = decision.get("position")
    if type(position) is not int or not 1 <= position <= len(choices):
        return _result("invalid", "Lua reward decision does not identify an actual offered choice.")
    choice = choices[position - 1]
    if (not _uint(decision.get("itemId")) or not _uint(decision.get("choiceIndex"), 5)
            or decision["itemId"] != choice["itemId"] or decision["choiceIndex"] != choice.get("index", 0)):
        return _result("invalid", "Lua reward decision differs from the actual offered choice.")
    if decision.get("reason") == "quest_preference":
        label = _label(metadata.get(choice['itemId'], dict(itemId=choice['itemId'])))
        return _result("selected", f"Choose {label} from the accepted quest preference.", choice)
    if offer_only:
        return _result("pending", "Waiting for complete item facts for useful or value ranking.")
    if decision.get("reason") == "useful":
        return _result("selected", f"Choose {_label(metadata[choice['itemId']])} for useful equipment and stats.", choice)
    if decision.get("reason") != "vendor":
        return _result("invalid", "Lua reward decision has no supported selection reason.")
    value = metadata[choice["itemId"]].get("sellPrice", 0) * choice["count"]
    return _result("selected", f"Choose {_label(metadata[choice['itemId']])} for its sell value ({value} copper).", choice)
