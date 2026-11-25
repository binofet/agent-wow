"""Bound the initial model snapshot without choosing gameplay or losing custody.

The complete JSON snapshot remains available through context_read. This is a
mechanical prompt view, never a replacement for native action admission.
"""
import copy
import json
import re


CONTEXT_BYTES = 24 * 1024
HISTORY_BYTES = 6 * 1024


class ContextLimitError(ValueError):
    pass


def encode(value):
    try:
        return json.dumps(value, ensure_ascii=False, allow_nan=False,
                          separators=(",", ":")).encode("utf-8")
    except (TypeError, ValueError, UnicodeError, RecursionError):
        raise ContextLimitError("Direct model context is not bounded JSON") from None


def _fields(value, names):
    return {key: copy.deepcopy(value[key]) for key in names if key in value} if isinstance(value, dict) else {}


def _person(value):
    # Preserve all raw scalar facts, including string uint64 ages, knownness,
    # inferred-pose flags and future observed fields; omit nested equipment.
    return {key: copy.deepcopy(item) for key, item in value.items()
            if not isinstance(item, (dict, list)) or key == "position"} if isinstance(value, dict) else {}


def _at(value, path):
    for part in path:
        value = value[part]
    return value


DEVELOPMENT_FIELDS = ("enabled", "directDevelopment", "workspace", "rpcUrl", "selfGuid", "ownerGuid",
                      "sessionId", "reason")
CONTROL_RECORDS = {"position", "recovery", "lastSpellOperation", "lastMeleeOperation", "lastBuff",
    "bodyMovement", "targetMovement", "movementOwner", "lastTransportFailure", "lastFollowPolicyFailure",
    "crowdControl", "engageNpc"}
STATUS_KEYS = {"phase", "status", "outcome"}
NONTERMINAL_TRADE = {"REQUESTED", "INVITED", "OPENING", "OPEN", "ACCEPT_SENT", "CANCEL_SENT"}
CUSTODY_KEYS = {"pending", "pendingactions", "pendingtools", "reservations", "custody",
    "unknownreceipt", "unknownactions", "outcomeunknown", "castpending", "motionunconfirmed",
    "autoattackunconfirmed", "cleanupunconfirmed", "exitunconfirmed", "transportunconfirmed"}


def _custody(value, path=()):
    """Keep exact unresolved records, including future fields, outside bulk views."""
    found = []
    if isinstance(value, dict):
        unsettled = any(key in STATUS_KEYS and isinstance(item, str) and
            (item.upper() in NONTERMINAL_TRADE or re.search(r"unknown|unconfirmed|(?:^|_)(?:pending|issued|reserved|offered|entered|attempted|waiting)(?:_|$)", item, re.I))
            for key, item in value.items())
        if path == ("trade",) and value.get("phase") in NONTERMINAL_TRADE:
            unsettled = True
        if unsettled:
            return [dict(path=list(path), value=copy.deepcopy(value))]
        for key, item in value.items():
            name = key.lower()
            if (item is not None and item is not False and item != 0 and item != [] and item != {} and
                    (name in CUSTODY_KEYS or name.startswith("pending") or name.endswith("pending")
                     or name.endswith("unconfirmed"))):
                found.append(dict(path=[*path, key], value=copy.deepcopy(item)))
            else:
                found.extend(_custody(item, (*path, key)))
    elif isinstance(value, list):
        for index, item in enumerate(value):
            found.extend(_custody(item, (*path, index)))
    return found


def project_context(state, max_bytes=CONTEXT_BYTES):
    raw = encode(state)
    original = len(raw)
    # A typed request may carry host-only bound callbacks as attributes. Copy
    # only its JSON-visible mapping values; authority stays on the original.
    state = json.loads(raw)
    if max_bytes is not None and original <= max_bytes:
        return copy.deepcopy(state), dict(fullContextBytes=original, contextBytes=original,
                                          contextOmittedPaths=0)
    # These required values are never clipped. Oversized authority or unresolved
    # operation records fail explicitly instead of granting an incomplete turn.
    result = _fields(state, ("character", "current_request", "gm_controls_available"))
    if "development" in state:
        result["development"] = _fields(state["development"], DEVELOPMENT_FIELDS)
    controls = state.get("controls", {})
    if isinstance(controls, dict):
        result["controls"] = {key: copy.deepcopy(value) for key, value in controls.items()
            if not isinstance(value, (dict, list)) or key in CONTROL_RECORDS}
    party = state.get("party", {})
    if isinstance(party, dict):
        result["party"] = {key: copy.deepcopy(value) for key, value in party.items()
                           if not isinstance(value, (dict, list))}
        if isinstance(party.get("members"), list):
            result["party"]["members"] = [_person(row) for row in party["members"]]
    world = state.get("observations", {})
    if isinstance(world, dict):
        observed = {key: copy.deepcopy(value) for key, value in world.items()
                    if not isinstance(value, (dict, list))}
        for key in ("self", "owner"):
            if key in world:
                observed[key] = _person(world[key])
        party_guids = {str(row.get("guid")) for row in party.get("members", [])
                       if isinstance(row, dict)} if isinstance(party, dict) else set()
        own = world.get("self", {})
        owner = world.get("owner", {})
        party_guids.update(str(row.get("guid")) for row in (own, owner) if isinstance(row, dict))
        if isinstance(world.get("nearbyPlayers"), list):
            observed["nearbyPlayers"] = [_person(row) for row in world["nearbyPlayers"]
                if isinstance(row, dict) and str(row.get("guid")) in party_guids]
        result["observations"] = observed
    custody = []
    for row in _custody(state):
        try:
            if _at(result, row["path"]) == row["value"]:
                continue
        except (KeyError, IndexError, TypeError):
            pass
        custody.append(row)
    if custody:
        result["currentUnresolvedCustody"] = custody
    omitted = []
    metadata = dict(projected=True, fullStateTool="context_read", fullStatePath=[],
        omittedPaths=omitted, note="Initial brief view, not a model context-window setting. Omitted facts are unknown here; read full captured paths before relying on them.")
    result["contextProjection"] = metadata

    def omissions(original_value, projected, path=()):
        if isinstance(original_value, dict) and isinstance(projected, dict):
            for key, value in original_value.items():
                if key not in projected:
                    omitted.append(dict(path=[*path, key], type=type(value).__name__,
                        count=len(value) if isinstance(value, (dict, list, str)) else 1))
                elif isinstance(value, dict):
                    omissions(value, projected[key], (*path, key))
                elif isinstance(value, list) and value != projected[key]:
                    omitted.append(dict(path=[*path, key], type="list", count=len(value)))

    # Small sections are copied whole, in stable name order, never ranked by
    # class, combat target, item quality or model intent.
    for key in sorted(state):
        if max_bytes is None:
            break  # Semantic brief mode has no byte-budget selection or abort.
        if key in result:
            continue
        value = state[key]
        if len(encode(value)) <= 1536:
            candidate = dict(result, **{key: copy.deepcopy(value)})
            if len(encode(candidate)) <= max_bytes - 4096:
                result[key] = candidate[key]
    omissions(state, result)
    if max_bytes is not None and len(encode(result)) > max_bytes:
        raise ContextLimitError("Required identity, party or unresolved custody exceeds the 24 KiB initial context bound; no model request sent")
    return result, dict(fullContextBytes=original, contextBytes=len(encode(result)),
                        contextOmittedPaths=len(omitted))


# Current observed unit identity/vitals/geometry, not template/equipment dumps.
# Full rows (including future fields) remain at each exact context_read path.
OBSERVED_UNIT_FIELDS = (
    "guid", "entry", "name", "present", "mapKnown", "mapId", "positionKnown",
    "x", "y", "z", "orientation", "positionRevision", "positionAgeMs",
    "observationAgeMs", "vitalsAgeMs", "positionInferred", "positionEstimated",
    "aliveKnown", "alive", "healthKnown", "health", "maxHealth", "powerKnown",
    "powerType", "power", "maxPower", "classKnown", "classId", "levelKnown", "level",
    "movementKnown", "moving", "movementFlags", "movementFlagsKnown", "extraMovementFlags",
    "combatKnown", "inCombat", "unitFlagsKnown", "unitFlags", "unitFlags2", "npcFlags",
    "dynamicFlagsKnown", "dynamicFlags", "factionTemplate", "targetKnown", "targetGuid",
    "attackVictimKnown", "attackVictimGuid", "attackAgeMs", "combatReachKnown", "combatReach",
    "distanceToSelfObservedKnown", "distanceToSelfObserved", "distanceToOwnerObservedKnown",
    "distanceToOwnerObserved", "activeCast", "trajectory")


def ordinary_context(state):
    """Lossless-access ordinary briefing; no model/window cap or history edit.

    The existing threshold only avoids changing already-small snapshots. Large
    snapshots use semantic field selection, with NO maximum required-fact size.
    Authority and tool admission continue to use the original captured state.
    """
    raw = encode(state)
    if len(raw) <= CONTEXT_BYTES:
        return json.loads(raw), dict(fullContextBytes=len(raw), contextBytes=len(raw), contextOmittedPaths=0)
    source = json.loads(raw)
    result, _ = project_context(source, max_bytes=None)
    # These useful current role/request/learning values are never clipped.
    for key in ("abilities", "controlFieldScopes", "model_selection", "agentContext",
                "learned_spell_changes", "emote_catalog", "shared_development"):
        if key in source:
            result[key] = copy.deepcopy(source[key])
    for key, value in source.items():
        if not isinstance(value, (dict, list)):
            result[key] = copy.deepcopy(value)
    if source.get('current_request',{}).get('source') == 'combat_review':
        combat=source.get('combat')
        if isinstance(combat,dict):
            view=_fields(combat,('available','reason','sessionId','selfGuid','inCombat','source','window',
                'eventCount','undecodedEvents','snapshotAgeMs','rawEvidence','incomingDamage','omittedGroups','semantics'))
            view.update(contextPath=['combat'],eventsContextPath=['combat','events'],eventsDeferred=True,
                sceneContextPath=['combat','scene'])
            result['combat']=view
        if isinstance(source.get('combat_review'),dict):
            result['combat_review']={key:copy.deepcopy(value) for key,value in source['combat_review'].items()
                if not isinstance(value,(dict,list))}
            result['combat_review']['contextPath']=['combat_review']
    world = source.get("observations")
    if isinstance(world, dict):
        view = result.setdefault("observations", {})
        for key in ("nearbyCreatures", "nearbyPlayers"):
            rows = world.get(key)
            if isinstance(rows, list):
                view[key] = []
                for index, row in enumerate(rows):
                    # Invalid rows stay explicit; a missing list is never a
                    # fabricated complete empty encounter.
                    compact = _fields(row, OBSERVED_UNIT_FIELDS) if isinstance(row, dict) else {"invalidRow": copy.deepcopy(row)}
                    compact["contextPath"] = ["observations", key, str(index)]
                    view[key].append(compact)
        view["contextPath"] = ["observations"]
    if isinstance(result.get("development"), dict):
        result["development"]["contextPath"] = ["development"]
    # Every deferred section has a direct discovery path. Scalar status and
    # identity facts are retained exactly; no false unavailable/empty claim.
    metadata_fields = ("available", "known", "reason", "sessionId", "selfGuid", "revision",
        "inventoryKnown", "inventoryRevision", "equipmentKnown", "equipmentRevision",
        "ammoKnown", "ammoEntry", "ammoRevision", "active", "closing", "phase", "status")
    for key, value in source.items():
        if key in result:
            continue
        descriptor = _fields(value, metadata_fields) if isinstance(value, dict) else {}
        descriptor.update(contextPath=[key], deferred=True, valueType=type(value).__name__)
        if isinstance(value, (dict, list, str)):
            descriptor["capturedCount"] = len(value)
        result[key] = descriptor
    omitted = []
    for row in result["contextProjection"]["omittedPaths"]:
        try:
            if _at(result, row["path"]) == _at(source, row["path"]):
                continue  # A required section restored whole is not deferred.
        except (KeyError, IndexError, TypeError):
            pass
        omitted.append(row)
    result["contextProjection"]["omittedPaths"] = omitted
    result["contextProjection"].update(mode="ordinary_factual_brief", byteLimit=None,
        note="Compact factual presentation only. Deferred data is not absent or unavailable. "
             "context_read accesses the complete immutable snapshot at the indicated paths; "
             "observed ranges retain server-pose ages, not arrival or current-instance proof.")
    return result, dict(fullContextBytes=len(raw), contextBytes=len(encode(result)),
        contextOmittedPaths=len(result["contextProjection"]["omittedPaths"]))


def project_history(history, max_bytes=HISTORY_BYTES):
    if not isinstance(history, list):
        raise ContextLimitError("Direct conversation history must be a list")
    original = len(encode(history))
    result = []
    if history:
        result = [copy.deepcopy(history[-1])]
        if len(encode(result)) > max_bytes:
            raise ContextLimitError("Latest user request exceeds the 6 KiB history bound; no model request sent")
        for row in reversed(history[:-1]):
            candidate = [copy.deepcopy(row), *result]
            if len(encode(candidate)) > max_bytes:
                break
            result = candidate
    return result, dict(fullHistoryBytes=original, historyBytes=len(encode(result)),
                        historyOmittedRows=len(history)-len(result))
