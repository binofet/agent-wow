"""Local fixtures only: no subject calls, live RPC, processes or game actions."""

import copy
import inspect
import json
import unittest

import research_adapter as a


MOCK_HASH = a.digest(b"explicit local mock source; not live evidence")
MOCK_NPC = {"npc_entry_id": 1234, "npc_name": "Fixture NPC", "source_sha256": MOCK_HASH}
MOCK_RECORD = {"key": "fixture_key", "code": "ABCDEFG2", "say": "Hello, friend ABCDEFG2."}


def preflight(at=10):
    return {"host": "quasar", "profile": "uther", "guid": 7, "port": 8192,
            "character_name": "Aveline", "independent": True, "ready": True,
            "observed_at": at, "owner_admission_id": "MOCK-owner", "lease_id": "MOCK-lease",
            "episode_id": "MOCK-episode",
            "process_identity": "MOCK-pid-startticks-exe-argv", "client_session_id": "MOCK-client",
            "chat_session_id": "MOCK-chat", "lease_exclusive": True,
            "episode_started_at": 0, "lease_expires_at": 120,
            "idle": True, "alive": True, "loot_enabled": False, "pending_action": False,
            "source_sha256": MOCK_HASH}


def decision(raw, phase="probe"):
    return {"dispatch_id": "MOCK-dispatch", "phase": phase, "outcome": "complete",
            "response_sha256": a.digest(raw), "lease_id": "MOCK-lease",
            "guid": 7, "profile": "uther", "port": 8192, "client_session_id": "MOCK-client",
            "episode_id": "MOCK-episode", "request_sha256": MOCK_HASH}


def say_fixture():
    raw = a.canonical({"code": "UNKNOWN", "say": "Hello, friend."})
    cursor = {"sessionId": "MOCK-chat", "latestSequence": "40", "observed_at": 10,
              "source_sha256": MOCK_HASH}
    plan = a.reserve_say(preflight(), cursor, raw, decision(raw), decision(raw), now=10)
    poll = {"sessionId": "MOCK-chat", "nextSequence": "41", "latestSequence": "41",
            "client_session_id": "MOCK-client", "observed_at": 11,
            "messages": [{"sequence": "41", "senderGuid": "7", "self": True,
                          "channel": "SAY", "text": "Hello, friend."}], "source_sha256": MOCK_HASH}
    write = {"transportWritten": True, "plan_sha256": a.digest(a.canonical(plan)),
             "guid": 7, "port": 8192, "client_session_id": "MOCK-client",
             "chat_session_id": "MOCK-chat", "at": 10.5,
             "source_sha256": MOCK_HASH}
    return raw, cursor, plan, poll, write


class ResearchContractTests(unittest.TestCase):
    def test_fresh_probe_has_only_visible_inputs_and_no_fallback(self):
        retained = a.build_request("probe", "fixture_key", MOCK_NPC, journal=MOCK_RECORD)
        withheld = a.build_request("probe", "fixture_key", MOCK_NPC)
        self.assertIn(b"ABCDEFG2", retained)
        self.assertNotIn(b"ABCDEFG2", withheld)
        self.assertEqual(a.decode(withheld)["journal"], [])
        self.assertEqual(a.deterministic_baseline(retained)["code"], "ABCDEFG2")
        self.assertEqual(a.deterministic_baseline(withheld)["code"], "UNKNOWN")
        self.assertEqual(a.deterministic_baseline(retained), a.validate_probe(a.canonical(
            {"code": "ABCDEFG2", "say": "Hello, friend ABCDEFG2."})))
        self.assertNotIn("history", inspect.signature(a.build_request).parameters)
        self.assertNotIn("tools", inspect.signature(a.build_request).parameters)
        for forbidden in ("history", "tools", "journal_path", "fallback", "cell", "scorer"):
            with self.subTest(forbidden=forbidden), self.assertRaises(TypeError):
                a.build_request("probe", "fixture_key", MOCK_NPC, **{forbidden: MOCK_RECORD})
        with self.assertRaises(a.Rejected):
            a.build_request("probe", "fixture_key", MOCK_NPC, encoding_code="ABCDEFG2")

    def test_distractor_and_entity_input_exclude_answer_channels(self):
        raw = a.build_request("distractor", "fixture_key", MOCK_NPC)
        self.assertNotIn(b"fixture_key", raw)
        self.assertNotIn(b"ABCDEFG2", raw)
        self.assertNotIn(MOCK_HASH.encode(), raw)
        self.assertEqual(set(a.decode(raw)), {"instruction", "npc"})
        for kwargs in ({"journal": MOCK_RECORD}, {"encoding_code": "ABCDEFG2"}):
            with self.assertRaises(a.Rejected):
                a.build_request("distractor", "fixture_key", MOCK_NPC, **kwargs)
        with self.assertRaises(a.Rejected):
            a.build_request("probe", "fixture_key", dict(MOCK_NPC, history="secret"))

    def test_encode_requires_actual_decision_and_write_provenance(self):
        raw = a.canonical(MOCK_RECORD)
        origin = {"guid": 7, "profile": "uther", "port": 8192, "phase": "encoding",
                  "key": "fixture_key", "dispatch_id": "MOCK-dispatch", "episode_id": "MOCK-episode",
                  "lease_id": "MOCK-lease", "client_session_id": "MOCK-client", "request_sha256": MOCK_HASH}
        receipt = dict(origin, **decision(raw, "encoding"))
        write = dict(origin, **{"kind": "episodic_experiment", "observed": True, "receipt_id": "MOCK-write",
                 "dispatch_id": receipt["dispatch_id"], "record_sha256": a.digest(raw)}
        )
        self.assertEqual(a.committed_record(raw, receipt, write, origin), MOCK_RECORD)
        for bad_decision, bad_write in (({}, write), (receipt, {}),
                                       (dict(receipt, response_sha256=MOCK_HASH), write),
                                       (receipt, dict(write, kind="buddy-quests.journal")),
                                       (receipt, dict(write, dispatch_id="other")),
                                       (dict(receipt, guid=1), write),
                                       (receipt, dict(write, episode_id="other")),
                                       (receipt, dict(write, client_session_id="other")),
                                       (dict(receipt, request_sha256="0"*64), write),
                                       (receipt, dict(write, observed=False))):
            with self.subTest(receipt=bad_decision, write=bad_write), self.assertRaises(a.Rejected):
                a.committed_record(raw, bad_decision, bad_write, origin)
        with self.assertRaises(a.Rejected):
            a.committed_record(raw, receipt, write, {})

    def test_baseline_uses_no_ambiguous_or_wrong_key_record(self):
        value = a.decode(a.build_request("probe", "fixture_key", MOCK_NPC, journal=MOCK_RECORD))
        value["journal"][0]["key"] = "different_key"
        self.assertEqual(a.deterministic_baseline(a.canonical(value))["code"], "UNKNOWN")
        value["journal"] = [MOCK_RECORD, MOCK_RECORD]
        with self.assertRaises(a.Rejected):
            a.deterministic_baseline(a.canonical(value))
        value["journal"] = []
        value["scorer"] = MOCK_RECORD
        with self.assertRaises(a.Rejected):
            a.deterministic_baseline(a.canonical(value))

    def test_probe_is_exact_json_and_fixed_harmless_action(self):
        for raw in (b'{"code":"UNKNOWN","say":"Hello, friend."}',
                    b'{"code":"ABCDEFG2","say":"Hello, friend ABCDEFG2."}'):
            self.assertIn(a.validate_probe(raw)["code"], ("UNKNOWN", "ABCDEFG2"))
        bad = [b'{"code":"UNKNOWN","say":"Hello, friend.","tool":"move"}',
               b'{"code":"UNKNOWN","code":"ABCDEFG2","say":"Hello, friend."}',
               b'{"code":"ABCDEFG0","say":"Hello, friend ABCDEFG0."}',
               b'{"code":"UNKNOWN","say":".kill"}',
               b'```json\n{"code":"UNKNOWN","say":"Hello, friend."}\n```',
               b'{"code":null,"say":"Hello, friend."}']
        for raw in bad:
            with self.subTest(raw=raw), self.assertRaises(a.Rejected):
                a.validate_probe(raw)

    def test_preflight_rejects_wrong_actor_stale_and_missing_lease(self):
        self.assertEqual(a.admit_preflight(preflight(), 10)["guid"], 7)
        changes = [{"guid": 1}, {"guid": 4}, {"guid": 5}, {"guid": 7.0}, {"port": 8086},
                   {"host": "other"}, {"profile": "default"}, {"character_name": "Joel"},
                   {"owner_admission_id": ""}, {"lease_id": ""}, {"lease_exclusive": False},
                   {"observed_at": 7}, {"independent": False}, {"ready": False},
                   {"pending_action": True}, {"loot_enabled": True}, {"idle": False},
                   {"lease_expires_at": 121}, {"process_identity": ""}]
        for change in changes:
            with self.subTest(change=change), self.assertRaises(a.Rejected):
                a.admit_preflight(dict(preflight(), **change), 10)

    def test_restart_requires_real_changed_identity_and_ordered_receipts(self):
        before, after = preflight(), preflight(20)
        self.assertFalse(a.client_boundary(before, after, (), restarted=False, now=20)["restarted"])
        after.update(process_identity="MOCK-new-process", client_session_id="MOCK-new-client",
                     chat_session_id="MOCK-new-chat")
        events = tuple({"kind": kind, "observed": True, "guid": 7, "lease_id": "MOCK-lease",
                        "at": at, "process_identity": identity, "source_sha256": MOCK_HASH}
                       for kind, at, identity in (("logout_complete", 11, before["process_identity"]),
                                                  ("process_exit", 12, before["process_identity"]),
                                                  ("new_ready", 19, after["process_identity"])))
        self.assertTrue(a.client_boundary(before, after, events, restarted=True, now=20)["restarted"])
        for mutated in ((), events[::-1], events[:2], events + (events[-1],)):
            with self.assertRaises(a.Rejected):
                a.client_boundary(before, after, mutated, restarted=True, now=20)
        for field in ("process_identity", "client_session_id", "chat_session_id"):
            bad = dict(after, **{field: before[field]})
            with self.assertRaises(a.Rejected):
                a.client_boundary(before, bad, events, restarted=True, now=20)
        with self.assertRaises(a.Rejected):
            a.client_boundary(before, after, (), restarted=False, now=20)
        with self.assertRaises(a.Rejected):
            a.client_boundary(before, dict(after, observed_at=121), events, restarted=True, now=121)
        with self.assertRaises(a.Rejected):
            a.client_boundary(preflight(100), preflight(10), (), restarted=False, now=10)

    def test_reservations_consume_failures_and_never_replay(self):
        ledger = a.reserve_request((), "d1", "e1", "encoding", 1)
        with self.assertRaises(a.Rejected):
            a.reserve_request(ledger, "d1", "e1", "encoding", 2)
        for outcome in ("reserved", "failed", "invalid", "ambiguous"):
            changed = (dict(ledger[0], outcome=outcome),)
            with self.subTest(outcome=outcome), self.assertRaises(a.Rejected):
                a.reserve_request(changed, "d2", "e1", "distractor", 2)
        complete = (dict(ledger[0], outcome="complete"),)
        self.assertEqual(len(a.reserve_request(complete, "d2", "e1", "distractor", 2)), 2)
        sixteen = tuple({"dispatch_id": "past"+str(i), "episode_id": "e"+str(i % 4),
                         "phase": "encoding", "reserved_at": 1, "outcome": "failed"} for i in range(16))
        with self.assertRaises(a.Rejected):
            a.reserve_request(sixteen, "d17", "e0", "encoding", 2)
        four = tuple(dict(sixteen[i], episode_id="ep"+str(i)) for i in range(4))
        with self.assertRaises(a.Rejected):
            a.reserve_request(four, "d5", "ep5", "encoding", 2)

    def test_say_reservation_binds_probe_receipt_and_current_cursor(self):
        raw, cursor, plan, _, _ = say_fixture()
        self.assertEqual(plan["params"], {"channel": "SAY", "language": "COMMON", "text": "Hello, friend."})
        with self.assertRaises(a.Rejected):
            a.reserve_say(preflight(), cursor, raw, decision(raw), decision(raw), prior_reservation=plan, now=10)
        for change in ({"sessionId": "old"}, {"cursorLost": True}, {"observed_at": 9},
                       {"latestSequence": "-1"}, {"latestSequence": True}):
            with self.subTest(change=change), self.assertRaises(a.Rejected):
                a.reserve_say(preflight(), dict(cursor, **change), raw, decision(raw), decision(raw), now=10)
        for receipt in ({}, dict(decision(raw), lease_id="wrong"), dict(decision(raw), outcome="ambiguous")):
            with self.assertRaises(a.Rejected):
                a.reserve_say(preflight(), cursor, raw, receipt, decision(raw), now=10)
        for change in ({"guid": 1}, {"profile": "default"}, {"port": 8086},
                       {"client_session_id": "other"}, {"episode_id": "other"},
                       {"request_sha256": "0"*64}, {"dispatch_id": "other"}):
            with self.subTest(change=change), self.assertRaises(a.Rejected):
                a.reserve_say(preflight(), cursor, raw, dict(decision(raw), **change), decision(raw), now=10)
        with self.assertRaises(a.Rejected):
            a.reserve_say(preflight(), cursor, raw, decision(raw), {}, now=10)

    def test_echo_requires_exact_sender_body_channel_session_and_cursor(self):
        _, _, plan, poll, write = say_fixture()
        self.assertEqual(a.classify_echo(plan, poll, write), "confirmed")
        for change in ({"senderGuid": "1"}, {"senderGuid": "4"}, {"senderGuid": "5"},
                       {"self": False}, {"channel": "WHISPER_INFORM"}, {"channel": "PARTY"},
                       {"text": "other"}, {"sequence": "40"}, {"sequence": "42"}):
            bad = copy.deepcopy(poll)
            bad["messages"][0].update(change)
            with self.subTest(change=change):
                self.assertEqual(a.classify_echo(plan, bad, write), "UNKNOWN")
        for change in ({"sessionId": "new"}, {"cursorLost": True}, {"latestSequence": "42"},
                       {"nextSequence": "42"}, {"source_sha256": ""}, {"messages": []},
                       {"client_session_id": "old"}, {"observed_at": 120}, {"observed_at": 9}):
            self.assertEqual(a.classify_echo(plan, dict(poll, **change), write), "UNKNOWN")

    def test_missing_duplicate_or_ambiguous_write_is_never_success(self):
        _, _, plan, poll, write = say_fixture()
        for receipt in ({}, dict(write, transportWritten=False), dict(write, plan_sha256=MOCK_HASH),
                        dict(write, guid=1), dict(write, port=8086), dict(write, at=121),
                        dict(write, client_session_id="old"), dict(write, chat_session_id="old")):
            self.assertEqual(a.classify_echo(plan, poll, receipt), "UNKNOWN")
        duplicate = copy.deepcopy(poll)
        duplicate["messages"].append(dict(duplicate["messages"][0], sequence="42"))
        duplicate.update(nextSequence="42", latestSequence="42")
        self.assertEqual(a.classify_echo(plan, duplicate, write), "UNKNOWN")
        self.assertEqual(a.classify_echo(dict(plan, port=8086), poll, write), "UNKNOWN")
        self.assertEqual(a.classify_echo(plan, poll, {"transportWritten": True}), "UNKNOWN")
        for field in ("client_session_id", "lease_id", "episode_id", "dispatch_id", "probe_sha256",
                      "probe_request_sha256"):
            bad_plan, bad_poll, bad_write = dict(plan), dict(poll), dict(write)
            del bad_plan[field]
            if field in bad_poll:
                del bad_poll[field]
            if field in bad_write:
                del bad_write[field]
            bad_write["plan_sha256"] = a.digest(a.canonical(bad_plan))
            with self.subTest(missing=field):
                self.assertEqual(a.classify_echo(bad_plan, bad_poll, bad_write), "UNKNOWN")

    def test_cli_usage_preserves_unknown_backend_and_rejects_tools_errors_missing_receipts(self):
        events = [{"type": "thread.started", "thread_id": "MOCK-thread"}, {"type": "turn.started"},
                  {"type": "item.completed", "item": {"type": "agent_message", "text": "MOCK"}},
                  {"type": "turn.completed", "usage": {"input_tokens": 12, "output_tokens": 3}}]
        raw = b"\n".join(a.canonical(row) for row in events)
        receipt = a.cli_receipt(raw, "MOCK-cli-version")
        self.assertEqual(receipt["usage"]["input_tokens"], 12)
        for field in ("provider_request_id", "backend_model", "backend_version", "cost"):
            self.assertEqual(receipt[field], "UNKNOWN")
        self.assertEqual(receipt["usage"]["cached_input_tokens"], "UNKNOWN")
        bad_events = [events[:-1], events[1:], events + [{"type": "error"}],
                      events[:2] + [{"type": "item.completed", "item": {"type": "mcp_tool_call"}}] + events[-1:],
                      events[:-1] + [{"type": "turn.completed", "usage": {"input_tokens": -1}}]]
        for bad in bad_events:
            with self.subTest(bad=bad), self.assertRaises(a.Rejected):
                a.cli_receipt(b"\n".join(a.canonical(row) for row in bad), "MOCK-cli-version")


if __name__ == "__main__":
    unittest.main()
