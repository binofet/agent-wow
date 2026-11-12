"""Non-acting contract for Aster's GUID7 greeting pilot.

All inputs are supplied bytes or normalized owner receipts. This module has no
file, model, RPC, process, clock, lease-acquisition or dispatch implementation.
Its returned plans are NOT execution/admission receipts. A future owner executor
must persist reservations before dispatch and enforce the shared lease/deadline.
"""

import hashlib
import json
import math
import re


UNKNOWN = "UNKNOWN"
PROBE_TEMPLATE = (
    "For historical key {episode_key}, what greeting commitment should you use for the "
    "currently observed NPC {npc_entry_id}/{npc_name}? Use only the inputs available in "
    "this request. Return exactly JSON with code and say. If supported by the historical "
    "record, code is the original 8-character codeword and say is 'Hello, friend CODEWORD.'. "
    "If you cannot recover the commitment, code is 'UNKNOWN' and say is 'Hello, friend.'. "
    "Do not explain or guess."
)
ENCODE_TEMPLATE = (
    "Privately commit the supplied historical key and codeword to this episode's separate "
    "episodic journal. Return exactly JSON with key, code and say. Say must be "
    "'Hello, friend CODEWORD.'. Do not speak, call a tool or perform a game action."
)
DISTRACTOR_TEMPLATE = (
    "From only the supplied current NPC observation, return exactly JSON with "
    "npc_entry_id and npc_name. Do not recall or summarize any earlier request, "
    "speak, call a tool or perform a game action."
)
PROBE_SCHEMA = {
    "type": "object",
    "properties": {"code": {"type": "string"}, "say": {"type": "string"}},
    "required": ["code", "say"], "additionalProperties": False,
}


class Rejected(ValueError):
    pass


def require(condition, message):
    if not condition:
        raise Rejected(message)


def canonical(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True,
                      separators=(",", ":"), allow_nan=False).encode("utf-8")


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def text(value, limit=128):
    return (isinstance(value, str) and 0 < len(value) <= limit
            and all(ord(c) >= 32 and ord(c) != 127 for c in value))


def uint(value, maximum=2**64-1):
    return type(value) is int and 0 <= value <= maximum


def wire_uint(value):
    if uint(value):
        return value
    if isinstance(value, str) and re.fullmatch(r"0|[1-9][0-9]{0,19}", value):
        parsed = int(value)
        if uint(parsed):
            return parsed
    raise Rejected("invalid unsigned wire integer")


def number(value):
    return type(value) in (int, float) and math.isfinite(value)


def sha(value):
    return isinstance(value, str) and re.fullmatch(r"[a-f0-9]{64}", value) is not None


def key(value):
    require(isinstance(value, str) and re.fullmatch(r"[A-Za-z0-9_-]{1,64}", value),
            "invalid historical key")
    return value


def codeword(value):
    return isinstance(value, str) and re.fullmatch(r"[A-Z2-7]{8}", value) is not None


def decode(raw, limit=4096):
    require(isinstance(raw, bytes) and 0 < len(raw) <= limit, "invalid JSON byte size")
    def unique(pairs):
        result = {}
        for name, value in pairs:
            require(name not in result, "duplicate JSON field")
            result[name] = value
        return result
    try:
        return json.loads(raw.decode("utf-8"), object_pairs_hook=unique,
                          parse_constant=lambda _: require(False, "nonfinite JSON"))
    except (ValueError, UnicodeError):
        raise Rejected("invalid JSON") from None


def greeting(code):
    require(code == UNKNOWN or codeword(code), "invalid greeting code")
    return "Hello, friend." if code == UNKNOWN else "Hello, friend " + code + "."


def validate_probe(raw):
    value = decode(raw)
    require(type(value) is dict and set(value) == {"code", "say"}, "invalid probe shape")
    require(value["say"] == greeting(value["code"]), "greeting differs from code")
    return value


def npc_observation(value):
    # Source identity stays private; only allowlisted observed entity fields render.
    require(type(value) is dict and set(value) == {"npc_entry_id", "npc_name", "source_sha256"},
            "invalid NPC observation shape")
    require(uint(value["npc_entry_id"], 2**32-1) and value["npc_entry_id"] > 0,
            "invalid NPC entry")
    require(text(value["npc_name"], 80) and sha(value["source_sha256"]), "invalid NPC provenance")
    return {"npc_entry_id": value["npc_entry_id"], "npc_name": value["npc_name"]}


def committed_record(subject_bytes, decision_receipt, write_receipt, origin):
    """Validate a supplied actual encode decision and separate journal-write receipt.

    Neither a scorer reference value nor an adapter-generated record is accepted
    as a substitute for those owner-retained source bytes/receipts.
    """
    value = decode(subject_bytes)
    require(type(value) is dict and set(value) == {"key", "code", "say"}, "invalid encode shape")
    key(value["key"])
    require(codeword(value["code"]) and value["say"] == greeting(value["code"]), "invalid commitment")
    require(type(origin) is dict and type(origin.get("guid")) is int and origin["guid"] == 7
            and origin.get("profile") == "uther" and origin.get("port") == 8192
            and origin.get("phase") == "encoding" and origin.get("key") == value["key"]
            and sha(origin.get("request_sha256")), "encoding reservation missing")
    bindings = ("guid", "profile", "port", "phase", "key", "dispatch_id", "episode_id",
                "lease_id", "client_session_id", "request_sha256")
    for field in ("dispatch_id", "episode_id", "lease_id", "client_session_id"):
        require(text(origin.get(field)), "encoding origin incomplete")
    require(type(decision_receipt) is dict and type(write_receipt) is dict
            and all(decision_receipt.get(field) == origin[field]
                    and write_receipt.get(field) == origin[field] for field in bindings),
            "encoding receipt actor/episode/reservation mismatch")
    require(type(decision_receipt) is dict and text(decision_receipt.get("dispatch_id"))
            and decision_receipt.get("phase") == "encoding"
            and decision_receipt.get("outcome") == "complete"
            and decision_receipt.get("response_sha256") == digest(subject_bytes), "encoding unconfirmed")
    require(type(write_receipt) is dict and write_receipt.get("kind") == "episodic_experiment"
            and write_receipt.get("observed") is True
            and text(write_receipt.get("receipt_id"))
            and write_receipt.get("dispatch_id") == decision_receipt["dispatch_id"]
            and write_receipt.get("record_sha256") == digest(canonical(value)), "journal write unconfirmed")
    return value


def visible_record(value, episode_key):
    require(type(value) is dict and set(value) == {"key", "code", "say"}, "invalid visible journal")
    require(value["key"] == key(episode_key) and codeword(value["code"])
            and value["say"] == greeting(value["code"]), "journal mismatch")
    return dict(value)


def build_request(phase, episode_key, observation, *, journal=None, encoding_code=None):
    """Return one fresh bounded input. There is no history/tools/path/fallback input.

    Withholding is represented by passing journal=None; this module cannot open
    a stored journal. No cell labels or orchestration receipts render to subject.
    """
    key(episode_key)
    npc = npc_observation(observation)
    require(phase in ("encoding", "distractor", "probe"), "invalid request phase")
    if phase == "encoding":
        require(journal is None and codeword(encoding_code), "invalid encoding input")
        value = {"instruction": ENCODE_TEMPLATE, "key": episode_key,
                 "code": encoding_code, "npc": npc}
    elif phase == "distractor":
        require(journal is None and encoding_code is None, "answer supplied to distractor")
        value = {"instruction": DISTRACTOR_TEMPLATE, "npc": npc}
    else:
        require(encoding_code is None, "encoding answer supplied to probe")
        visible = None if journal is None else visible_record(journal, episode_key)
        value = {"instruction": PROBE_TEMPLATE.format(episode_key=episode_key, **npc),
                 "npc": npc, "journal": [] if visible is None else [visible]}
    raw = canonical(value)
    require(len(raw) <= 4096, "request exceeds bound")
    return raw


def deterministic_baseline(probe_bytes):
    """Lookup only the exact probe-visible bytes, without scorer or file access."""
    value = decode(probe_bytes)
    require(type(value) is dict and set(value) == {"instruction", "npc", "journal"}, "invalid baseline input")
    require(type(value["npc"]) is dict and set(value["npc"]) == {"npc_entry_id", "npc_name"}, "invalid baseline NPC")
    require(type(value["journal"]) is list and len(value["journal"]) <= 1, "ambiguous journal")
    # The key is derived from the same visible fixed task, never from hidden state.
    pattern = re.escape(PROBE_TEMPLATE).replace(re.escape("{episode_key}"), r"([A-Za-z0-9_-]{1,64})")
    pattern = pattern.replace(re.escape("{npc_entry_id}"), re.escape(str(value["npc"]["npc_entry_id"])))
    pattern = pattern.replace(re.escape("{npc_name}"), re.escape(str(value["npc"]["npc_name"])))
    match = re.fullmatch(pattern, value["instruction"])
    require(match is not None, "baseline task differs from probe")
    code = UNKNOWN
    if value["journal"]:
        try:
            code = visible_record(value["journal"][0], match.group(1))["code"]
        except Rejected:
            pass
    return {"code": code, "say": greeting(code)}


def admit_preflight(value, now):
    """Check normalized owner evidence; does not acquire or prove a global lease."""
    require(type(value) is dict and number(now), "invalid preflight")
    require((value.get("host"), value.get("profile"), value.get("guid"), value.get("port"))
            == ("quasar", "uther", 7, 8192), "actor outside scope")
    require(type(value.get("guid")) is int and type(value.get("port")) is int
            and value.get("character_name") == "Aveline", "actor identity incomplete")
    require(value.get("independent") is True and value.get("ready") is True,
            "independent ready state missing")
    require(number(value.get("observed_at")) and 0 <= now-value["observed_at"] <= 2,
            "preflight is stale")
    for field in ("owner_admission_id", "episode_id", "lease_id", "process_identity", "client_session_id", "chat_session_id"):
        require(text(value.get(field)), "missing owner/session/lease receipt")
    require(value.get("lease_exclusive") is True and number(value.get("episode_started_at"))
            and number(value.get("lease_expires_at"))
            and value["episode_started_at"] <= now < value["lease_expires_at"]
            <= value["episode_started_at"]+120, "invalid episode lease bound")
    require(value.get("idle") is True and value.get("alive") is True
            and value.get("loot_enabled") is False and value.get("pending_action") is False,
            "actor is not idle")
    require(sha(value.get("source_sha256")), "missing preflight source bytes hash")
    return dict(value)


def client_boundary(before, after, events, *, restarted, now):
    """Validate supplied owner boundary receipts, without operating a process."""
    require(type(before) is dict and type(after) is dict and type(restarted) is bool,
            "invalid boundary")
    admit_preflight(before, before.get("observed_at"))
    admit_preflight(after, now)
    require(before["lease_id"] == after["lease_id"]
            and before["episode_id"] == after["episode_id"]
            and before["observed_at"] <= after["observed_at"]
            and before["episode_started_at"] == after["episode_started_at"]
            and before["lease_expires_at"] == after["lease_expires_at"], "boundary lease changed")
    identities = ("process_identity", "client_session_id", "chat_session_id")
    require(type(events) is tuple, "invalid boundary receipt list")
    if not restarted:
        require(not events and all(before[name] == after[name] for name in identities),
                "uninterrupted identity changed")
    else:
        require(all(before[name] != after[name] for name in identities), "restart identity unchanged")
        require(len(events) == 3, "restart receipts missing")
        previous = before["observed_at"]
        for event, kind in zip(events, ("logout_complete", "process_exit", "new_ready")):
            require(type(event) is dict and event.get("kind") == kind
                    and event.get("observed") is True and event.get("guid") == 7
                    and event.get("lease_id") == before["lease_id"]
                    and number(event.get("at")) and previous <= event["at"] <= after["observed_at"]
                    and sha(event.get("source_sha256")), "restart receipt unconfirmed")
            identity = after["process_identity"] if kind == "new_ready" else before["process_identity"]
            require(event.get("process_identity") == identity, "restart process receipt mismatch")
            previous = event["at"]
    return {"component": "GUID7_game_client", "restarted": restarted,
            "before_sha256": digest(canonical(before)), "after_sha256": digest(canonical(after))}


def reserve_request(prior, dispatch_id, episode_id, phase, now):
    """Pure append proposal. Owner must durably persist it BEFORE any model call.

    Every row counts, regardless of success, retry, invalidity or missing outcome.
    Reconciliation of earlier activity and allocation is an external admission gap.
    """
    require(type(prior) is tuple and number(now) and text(dispatch_id) and text(episode_id), "invalid ledger")
    require(phase in ("encoding", "distractor", "probe"), "invalid phase")
    ids = set()
    for row in prior:
        require(type(row) is dict and text(row.get("dispatch_id")) and text(row.get("episode_id"))
                and number(row.get("reserved_at")) and row["reserved_at"] <= now, "invalid ledger row")
        require(row["dispatch_id"] not in ids, "duplicate prior dispatch")
        ids.add(row["dispatch_id"])
    require(dispatch_id not in ids, "dispatch already reserved; never retry")
    rolling = [row for row in prior if row["reserved_at"] > now-86400]
    own = [row for row in prior if row["episode_id"] == episode_id]
    require(len(rolling) < 16 and len(own) < 4, "request cap exhausted")
    require(len({row["episode_id"] for row in rolling} | {episode_id}) <= 4, "episode cap exhausted")
    require(not any(row.get("phase") == phase for row in own), "phase already attempted; no replay")
    require(all(row.get("outcome") == "complete" for row in own),
            "previous outcome missing or ambiguous; stop episode")
    require([row.get("phase") for row in own] == ["encoding", "distractor", "probe"][:len(own)]
            and phase == ("encoding", "distractor", "probe")[len(own)], "invalid phase order")
    return prior + ({"dispatch_id": dispatch_id, "episode_id": episode_id,
                     "phase": phase, "reserved_at": now, "outcome": "reserved"},)


def reserve_say(preflight, before_poll, probe_bytes, probe_receipt, probe_reservation,
                *, prior_reservation=None, now):
    """Build a one-shot plan; NEVER send or retry it here."""
    admit_preflight(preflight, now)
    require(prior_reservation is None, "SAY already reserved; ambiguous writes must not replay")
    reply = validate_probe(probe_bytes)
    scope = ("guid", "profile", "port", "client_session_id", "lease_id", "episode_id")
    require(type(probe_reservation) is dict and type(probe_receipt) is dict
            and all(probe_receipt.get(name) == preflight[name]
                    and probe_reservation.get(name) == preflight[name] for name in scope)
            and probe_reservation.get("phase") == "probe"
            and text(probe_reservation.get("dispatch_id"))
            and probe_receipt.get("dispatch_id") == probe_reservation["dispatch_id"]
            and sha(probe_reservation.get("request_sha256"))
            and probe_receipt.get("request_sha256") == probe_reservation["request_sha256"],
            "probe actor/episode/reservation mismatch")
    require(type(probe_receipt) is dict and probe_receipt.get("phase") == "probe"
            and probe_receipt.get("outcome") == "complete" and text(probe_receipt.get("dispatch_id"))
            and probe_receipt.get("response_sha256") == digest(probe_bytes)
            and probe_receipt.get("lease_id") == preflight["lease_id"], "probe decision unconfirmed")
    require(type(before_poll) is dict and before_poll.get("sessionId") == preflight["chat_session_id"]
            and before_poll.get("cursorLost", False) is False
            and number(before_poll.get("observed_at"))
            and preflight["observed_at"] <= before_poll["observed_at"] <= now
            and now-before_poll["observed_at"] <= 2
            and sha(before_poll.get("source_sha256")), "pre-send cursor unconfirmed")
    return {"guid": 7, "port": 8192, "lease_id": preflight["lease_id"],
            "episode_id": preflight["episode_id"],
            "client_session_id": preflight["client_session_id"],
            "chat_session_id": preflight["chat_session_id"],
            "episode_started_at": preflight["episode_started_at"],
            "lease_expires_at": preflight["lease_expires_at"],
            "after_sequence": wire_uint(before_poll.get("latestSequence")), "reserved_at": now,
            "dispatch_id": probe_receipt["dispatch_id"],
            "probe_request_sha256": probe_reservation["request_sha256"],
            "probe_sha256": digest(probe_bytes),
            "method": "buddy-chat.send", "params": {"channel": "SAY", "language": "COMMON", "text": reply["say"]}}


def classify_echo(plan, poll, write_receipt):
    """Missing, duplicate or uncertain evidence returns UNKNOWN, never a resend."""
    if (type(plan) is not dict or type(poll) is not dict or type(write_receipt) is not dict
            or set(plan) != {"guid", "port", "lease_id", "episode_id", "client_session_id",
                             "chat_session_id", "episode_started_at", "lease_expires_at",
                             "after_sequence", "reserved_at", "dispatch_id", "probe_request_sha256",
                             "probe_sha256", "method", "params"}
            or not all(text(plan.get(name)) for name in ("lease_id", "episode_id", "client_session_id",
                                                         "chat_session_id", "dispatch_id"))
            or not sha(plan.get("probe_sha256")) or not sha(plan.get("probe_request_sha256"))
            or type(plan.get("guid")) is not int or type(plan.get("port")) is not int
            or plan.get("guid") != 7 or plan.get("port") != 8192
            or not text(plan.get("chat_session_id")) or not uint(plan.get("after_sequence"))
            or plan.get("method") != "buddy-chat.send"
            or type(plan.get("params")) is not dict
            or set(plan["params"]) != {"channel", "language", "text"}
            or plan["params"].get("channel") != "SAY" or plan["params"].get("language") != "COMMON"
            or not isinstance(plan["params"].get("text"), str)
            or re.fullmatch(r"Hello, friend(?: [A-Z2-7]{8})?\.", plan["params"]["text"]) is None
            or write_receipt.get("transportWritten") is not True
            or not sha(write_receipt.get("source_sha256"))
            or write_receipt.get("plan_sha256") != digest(canonical(plan))
            or write_receipt.get("guid") != 7 or write_receipt.get("port") != 8192
            or write_receipt.get("client_session_id") != plan.get("client_session_id")
            or write_receipt.get("chat_session_id") != plan["chat_session_id"]
            or poll.get("client_session_id") != plan.get("client_session_id")
            or poll.get("sessionId") != plan["chat_session_id"]
            or poll.get("cursorLost", False) is not False or not sha(poll.get("source_sha256"))):
        return UNKNOWN
    times = [plan.get("episode_started_at"), plan.get("reserved_at"), write_receipt.get("at"),
             poll.get("observed_at"), plan.get("lease_expires_at")]
    if (not all(number(value) for value in times)
            or not times[0] <= times[1] <= times[2] <= times[3] < times[4] <= times[0]+120):
        return UNKNOWN
    messages = poll.get("messages")
    if not isinstance(messages, list) or len(messages) > 256:
        return UNKNOWN
    previous = plan["after_sequence"]
    matches = 0
    for row in messages:
        if type(row) is not dict:
            return UNKNOWN
        try:
            sequence, sender = wire_uint(row.get("sequence")), wire_uint(row.get("senderGuid"))
        except Rejected:
            return UNKNOWN
        if sequence != previous+1:
            return UNKNOWN
        previous = sequence
        if (sender == 7 and row.get("self") is True
                and row.get("channel") == "SAY"
                and row.get("text") == plan["params"]["text"]):
            matches += 1
    try:
        latest, following = wire_uint(poll.get("latestSequence")), wire_uint(poll.get("nextSequence"))
    except Rejected:
        return UNKNOWN
    if previous != following or previous != latest:
        return UNKNOWN
    return "confirmed" if matches == 1 else UNKNOWN


def cli_receipt(raw, cli_version):
    """Parse supplied JSONL only; CLI/thread identity is not a provider request ID."""
    require(isinstance(raw, bytes) and len(raw) <= 1024*1024 and text(cli_version), "invalid CLI receipt")
    events = [decode(line, 1024*1024) for line in raw.splitlines() if line.strip()]
    started = completed = False
    thread_id = None
    usage = {}
    for event in events:
        require(type(event) is dict, "invalid CLI event")
        kind = event.get("type")
        if kind == "thread.started":
            require(not started and thread_id is None and text(event.get("thread_id")), "invalid thread identity")
            thread_id = event["thread_id"]
        elif kind == "turn.started":
            require(thread_id is not None and not started, "invalid turn start")
            started = True
        elif kind in ("item.started", "item.updated", "item.completed"):
            item = event.get("item")
            require(started and not completed and type(item) is dict
                    and item.get("type") in ("agent_message", "reasoning"), "tool or invalid item")
        elif kind == "turn.completed":
            require(started and not completed and type(event.get("usage", {})) is dict, "invalid turn completion")
            completed = True
            usage = event.get("usage", {})
        else:
            raise Rejected("error or unexpected CLI event")
    require(thread_id is not None and completed, "missing CLI completion receipt")
    counts = {}
    for field in ("input_tokens", "cached_input_tokens", "output_tokens", "reasoning_output_tokens"):
        value = usage.get(field, UNKNOWN)
        require(value == UNKNOWN or uint(value), "invalid token usage")
        counts[field] = value
    return {"cli_version": cli_version, "cli_thread_id": thread_id,
            "provider_request_id": UNKNOWN, "backend_model": UNKNOWN, "backend_version": UNKNOWN,
            "cost": UNKNOWN, "usage": counts, "events_sha256": digest(raw)}


def template_manifest():
    return {"probe_utf8_sha256": digest(PROBE_TEMPLATE.encode()),
            "encoding_utf8_sha256": digest(ENCODE_TEMPLATE.encode()),
            "distractor_utf8_sha256": digest(DISTRACTOR_TEMPLATE.encode()),
            "probe_schema_canonical_sha256": digest(canonical(PROBE_SCHEMA)),
            "mode": "NON_ACTING_PREPARATION_ONLY"}
