"""Present item links from a captured GM lookup; the original reply remains pageable."""
import copy
import re


_ITEM_LINK = re.compile(
    r"(?P<entry>[1-9][0-9]{0,9}) - \|c[0-9a-fA-F]{8}\|Hitem:(?P=entry)"
    r"(?::0){9}\|h\[(?P<name>[^\[\]|\r\n]+)\]\|h\|r[ \t]*"
)
_CHAT_FIELDS = {"sequence", "channel", "language", "senderGuid", "receiverGuid", "text", "chatTag", "self"}
_PREVIEW = 3


def _sequence(value):
    if type(value) not in (int, str):
        return None
    text = str(value)
    if re.fullmatch(r"0|[1-9][0-9]{0,19}", text):
        number = int(text)
        if number < 2**64:
            return number
    return None


def _system(row):
    return (row.get("channel", 0) in (0, "CHANNEL_UNSPECIFIED") and
            str(row.get("senderGuid", 0)) == "0" and row.get("language", 0) == 0 and
            row.get("self", False) is False)


def project_lookup_feedback(result, args, reference):
    """Return an outgoing receipt with bounded matching links and every other message intact."""
    if not isinstance(result, dict) or result.get("ok") is not True or not isinstance(args, dict):
        return None
    receipt = result.get("result")
    command = args.get("command")
    if (not isinstance(receipt, dict) or not isinstance(command, str) or receipt.get("command") != command
            or receipt.get("selectedGuid") != args.get("select_guid") or receipt.get("transportWritten") is not True
            or receipt.get("commandTransportWritten") is not True or receipt.get("gameEffectClaimed") is not False
            or receipt.get("commandOutcomeUnknown") or receipt.get("selectionOutcomeUnknown")):
        return None
    words = command.split(None, 2)
    if len(words) != 3 or [word.casefold() for word in words[:2]] != [".lookup", "item"]:
        return None
    query = words[2].strip()
    if not query or query.split(None, 1)[0].casefold() == "set":
        return None
    if not isinstance(reference, dict):
        return None
    path = reference.get("contextPath")
    if (not isinstance(path, list) or not path or not all(isinstance(key, str) and key for key in path)
            or not isinstance(reference.get("toolCallId"), str) or not reference["toolCallId"]
            or not isinstance(reference.get("resultSHA256"), str)
            or not re.fullmatch(r"[0-9a-f]{64}", reference["resultSHA256"])
            or type(reference.get("resultBytes")) is not int or reference["resultBytes"] <= 0):
        return None
    feedback = receipt.get("feedback")
    if (not isinstance(feedback, dict) or feedback.get("status") != "observed" or feedback.get("cursorLost")
            or feedback.get("correlation") != "temporal_only" or feedback.get("commandSuccessKnown") is not False
            or feedback.get("targetIdentityKnown") is not False or not isinstance(feedback.get("chatSessionId"), str)
            or not feedback["chatSessionId"]):
        return None
    messages, system = feedback.get("messages"), feedback.get("systemMessages")
    if not isinstance(messages, list) or not messages or not all(isinstance(row, dict) for row in messages):
        return None
    if not isinstance(system, list) or system != [row for row in messages if _system(row)]:
        return None
    start = _sequence(feedback.get("afterSequence"))
    end = _sequence(feedback.get("nextSequence"))
    latest = _sequence(feedback.get("latestSequence"))
    sequences = [_sequence(row.get("sequence")) for row in messages]
    if (start is None or end is None or latest is None or None in sequences or end != sequences[-1]
            or end > latest or any(a >= b for a, b in zip([start] + sequences, sequences))):
        return None

    # AzerothCore's in-game LANG_ITEM_LIST_CHAT repeats the item ID in the prefix and the link.
    # The collector can return after the first SYSTEM-bearing poll. Counts cover only these rows.
    folded_query = query.casefold()
    selection = receipt.get("selection")
    receivers = {"0"}
    if isinstance(selection, dict) and selection.get("selfGuid"):
        receivers.add(str(selection["selfGuid"]))
    matches, exact, partial = set(), {}, []
    for row in system:
        if (set(row) - _CHAT_FIELDS or str(row.get("receiverGuid", 0)) not in receivers or
                row.get("chatTag", 0) != 0 or not isinstance(row.get("text"), str)):
            continue
        link = _ITEM_LINK.fullmatch(row["text"])
        if not link:
            continue
        name, entry = link["name"], int(link["entry"])
        if entry >= 2**32 or folded_query not in name.casefold():
            continue
        sequence = row["sequence"]
        matches.add(sequence)
        if name.casefold() == folded_query:
            exact[entry] = dict(itemId=entry, name=name, sequence=sequence)
        elif len(partial) < _PREVIEW:
            partial.append(sequence)

    # Show the latest witness per exact ID. Several IDs may really have the same display name.
    exact_preview = sorted(exact.values(), key=lambda row: int(row["sequence"]), reverse=True)[:_PREVIEW]
    shown = set(partial) | {row["sequence"] for row in exact_preview}
    omitted = matches - shown
    if not omitted:
        return None
    view = copy.deepcopy(result)
    shown_feedback = view["result"]["feedback"]
    for key in ("messages", "systemMessages"):
        shown_feedback[key] = [row for row in shown_feedback[key] if row["sequence"] not in omitted]
    shown_feedback["itemLookup"] = dict(query=query, capturedMessageCount=len(messages),
        capturedSystemMessageCount=len(system), capturedMatchingItemRows=len(matches),
        distinctExactItemCount=len(exact), exactItems=exact_preview, omittedMatchingRows=len(omitted),
        fullReceipt=copy.deepcopy(reference))
    return view
