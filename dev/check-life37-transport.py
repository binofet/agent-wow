#!/usr/bin/env python3
"""Verify the synthetic dungeon fixture's captured Go transport writes."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import struct
import sys


CAST_SPELL = 0x12E
UNIT_TARGET = 2
LABELS = {
    "scripted_transport_smoke": "Scripted Go transport smoke",
    "instrumented_controller": "Instrumented controller transport capture",
}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, f"duplicate JSON field {key!r}")
        result[key] = value
    return result


def decimal_uint64(value):
    return (isinstance(value, str) and re.fullmatch(r"[1-9][0-9]{0,19}", value)
            and int(value) < 2**64)


def nonempty_string(value):
    return isinstance(value, str) and bool(value.strip())


def decode_spell(opcode, payload_hex):
    require(type(opcode) is int and opcode == CAST_SPELL, "unexpected gameplay opcode")
    require(isinstance(payload_hex, str) and re.fullmatch(r"(?:[0-9a-fA-F]{2}){12,19}", payload_hex),
            "invalid cast payload hex or length")
    payload = bytes.fromhex(payload_hex)
    count, spell, flags, target_mask = struct.unpack_from("<BIBI", payload)
    require(spell != 0 and flags == 0 and target_mask == UNIT_TARGET, "invalid unit-target cast header")
    mask = payload[10]
    offsets = [index for index in range(8) if mask & (1 << index)]
    octets = payload[11:]
    require(offsets and len(octets) == len(offsets) and all(octets), "noncanonical packed target GUID")
    target = sum(octet << (offset * 8) for octet, offset in zip(octets, offsets))
    return count, spell, str(target)


def verify(fixture_bytes, capture, policy_revision, generation):
    fixture = json.loads(fixture_bytes, object_pairs_hook=unique_object)
    require(fixture["schema"] == "wb.lua-transition.dungeon-replay/1"
            and fixture["provenance"] == "synthetic", "unsupported fixture")
    require(capture["schema"] == "wb.life37.gameplay-write/1", "unsupported capture schema")
    mode = capture["mode"]
    require(mode in LABELS, "unknown capture mode")
    fixture_sha = hashlib.sha256(fixture_bytes).hexdigest()
    require(capture["fixtureSHA256"] == fixture_sha, "capture fixture SHA-256 mismatch")

    actors = fixture["actors"]
    require(isinstance(actors, dict) and actors, "fixture needs actors")
    fixture_sessions = set()
    for actor_guid, actor in actors.items():
        session_id = actor["sessionId"]
        require(decimal_uint64(actor_guid) and nonempty_string(session_id) and session_id not in fixture_sessions,
                "invalid or duplicate fixture actor/session identity")
        fixture_sessions.add(session_id)
    session_aliases = {session: session for session in fixture_sessions}
    action_aliases = None
    if mode == "instrumented_controller":
        session_aliases = capture["sessionAliases"]
        action_aliases = capture["actionAliases"]
        require(isinstance(session_aliases, dict) and session_aliases.keys() == fixture_sessions
                and all(nonempty_string(value) for value in session_aliases.values())
                and len(set(session_aliases.values())) == len(fixture_sessions),
                "invalid runtime session bindings")
        require(isinstance(action_aliases, dict), "invalid runtime action bindings")
        require(capture["policyRevision"] == policy_revision, "capture policy revision mismatch")
    sessions = dict.fromkeys(session_aliases.values(), 0)

    summaries = capture["sessions"]
    require(isinstance(summaries, dict) and summaries.keys() == sessions.keys(),
            "capture sessions do not match all fixture actors")
    for session_id, summary in summaries.items():
        dropped = summary["dropped"]
        require(type(dropped) is int and dropped == 0, f"{session_id}: dropped or unknown capture rows")

    expected_frames, captured_frames = fixture["frames"], capture["frames"]
    require(isinstance(expected_frames, list) and expected_frames and isinstance(captured_frames, list)
            and len(expected_frames) == len(captured_frames), "capture frame count mismatch")
    frame_ids, action_ids, reserved = set(), set(), set()
    casts = []
    for expected_frame, captured_frame in zip(expected_frames, captured_frames):
        frame_id = expected_frame["id"]
        require(nonempty_string(frame_id) and frame_id not in frame_ids, "invalid or duplicate fixture frame ID")
        frame_ids.add(frame_id)
        require(captured_frame["frameId"] == frame_id, f"{frame_id}: missing or reordered capture frame")
        expected_actions = expected_frame.get("emit", [])
        attempts = captured_frame["transportAttempts"]
        require(isinstance(expected_actions, list) and isinstance(attempts, list)
                and len(expected_actions) == len(attempts), f"{frame_id}: missing or unexpected transport attempt")
        for action, attempt in zip(expected_actions, attempts):
            action_id, actor_guid, target_guid, spell_id = (
                action["id"], action["actorGuid"], action["targetGuid"], action["spellId"])
            require(nonempty_string(action_id) and action_id not in action_ids and actor_guid in actors
                    and decimal_uint64(target_guid) and type(spell_id) is int and 0 < spell_id < 2**32,
                    f"{frame_id}: invalid or duplicate fixture action")
            action_ids.add(action_id)
            session_id = session_aliases[actors[actor_guid]["sessionId"]]
            detail = f"{frame_id}/{action_id}"
            reservation = action_id
            if action_aliases is not None:
                binding = action_aliases[action_id]
                reservation = binding["actionId"]
                require(binding["sessionId"] == session_id and decimal_uint64(reservation)
                        and (session_id, reservation) not in reserved,
                        f"{detail}: invalid or duplicate runtime reservation")
                reserved.add((session_id, reservation))
            origin = attempt["origin"]
            require(isinstance(origin, dict) and origin.get("module") == "buddy-actions"
                    and origin.get("writer") == "lua", f"{detail}: legacy or unknown writer provenance")
            require(origin.get("sessionId") == session_id and origin.get("actionId") == reservation
                    and attempt["characterGuid"] == actor_guid, f"{detail}: wrong action, session, or character")
            require(origin.get("generation") == generation and origin.get("policyRevision") == policy_revision,
                    f"{detail}: stale or unknown generation or policy revision")
            sequence = attempt["sequence"]
            require(type(sequence) is int and sequence == sessions[session_id] + 1,
                    f"{detail}: duplicate or noncontiguous session attempt sequence")
            sessions[session_id] = sequence
            require(attempt["outcome"] == "written" and attempt["anyBytes"] is True,
                    f"{detail}: transport write not confirmed")
            try:
                count, spell, target = decode_spell(attempt["opcode"], attempt["payloadHex"])
            except ValueError as exc:
                raise ValueError(f"{detail}: {exc}") from exc
            require(spell == spell_id and target == target_guid, f"{detail}: packet spell or target differs")
            casts.append({"actionId": action_id, "reservationId": reservation,
                          "sessionId": session_id, "sequence": sequence,
                          "castCount": count, "spellId": spell, "targetGuid": target})

    require(casts, "fixture contains no positive transport attempts")
    if action_aliases is not None:
        require(action_aliases.keys() == action_ids, "capture has unexplained runtime action bindings")
    for session_id, count in sessions.items():
        terminal = summaries[session_id]["attemptCount"]
        require(type(terminal) is int and terminal == count, f"{session_id}: terminal attempt count mismatch")
    return {"mode": mode, "label": LABELS[mode], "fixtureSHA256": fixture_sha,
            "policyRevision": policy_revision, "generation": generation, "frames": len(expected_frames),
            "sessions": len(sessions), "transportAttempts": len(casts), "casts": casts}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("fixture", type=Path)
    parser.add_argument("capture", type=Path)
    parser.add_argument("--policy-revision", required=True)
    parser.add_argument("--generation", default="1")
    args = parser.parse_args()
    if not decimal_uint64(args.generation):
        parser.error("--generation must be a positive uint64 decimal string")
    if not nonempty_string(args.policy_revision):
        parser.error("--policy-revision must be nonempty")
    try:
        capture = json.loads(args.capture.read_bytes(), object_pairs_hook=unique_object)
        result = verify(args.fixture.read_bytes(), capture, args.policy_revision, args.generation)
    except (KeyError, OSError, TypeError, ValueError) as exc:
        sys.exit(f"Go transport verification failed: {exc}")
    print(json.dumps(result, sort_keys=True))


if __name__ == "__main__":
    main()
