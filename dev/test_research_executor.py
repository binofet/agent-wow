"""Pure local executor tests: every process, RPC, host and profile is a mock.

These fixtures neither run a subject nor acquire an actual game/profile lease.
Only private temporary evidence files are real; flock itself is mocked.
"""

import copy
import io
import os
from pathlib import Path
import shutil
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

import research_adapter as a
import research_executor as e


MOCK_HASH = a.digest(b"LOCAL MOCK ONLY; no subject or game evidence")
MOCK_PROBE = a.canonical({"code": "UNKNOWN", "say": "Hello, friend."})


def subject_pin(directory):
    binary = directory/"MOCK-never-executed"
    if not binary.exists():
        binary.write_bytes(b"LOCAL MOCK EXECUTABLE IDENTITY; NEVER RUN")
        binary.chmod(0o700)
    config = a.canonical({"host": "quasar", "source": "owner_verified_normal_configuration",
                          "configured_model": "MOCK-pinned-model", "owner_receipt_id": "MOCK-owner"})
    pin = {"binary": str(binary), "binary_sha256": a.digest(binary.read_bytes()),
           "configuration_sha256": a.digest(config), "configured_model": "MOCK-pinned-model",
           "cli_version": "MOCK-cli/1", "decision_timeout_seconds": 5}
    return pin, config


class Clock:
    def __init__(self, value=0):
        self.value = value

    def __call__(self):
        return self.value


def cli_events(thread="MOCK-thread"):
    return b"\n".join(a.canonical(row) for row in (
        {"type": "thread.started", "thread_id": thread},
        {"type": "turn.started"},
        {"type": "item.completed", "item": {"type": "agent_message", "text": "LOCAL MOCK"}},
        {"type": "turn.completed", "usage": {"input_tokens": 12, "output_tokens": 3}},
    )) + b"\n"


class InputSink(io.BytesIO):
    def close(self):
        self.submitted = self.getvalue()
        super().close()


class FakeChild:
    def __init__(self, returncode=0):
        self.stdin = InputSink()
        self.returncode = returncode
        self.pid = 999999999  # Never signaled or used by an actual process API.

    def poll(self):
        return self.returncode


class FakeSocket:
    def __init__(self):
        self.timeouts = []

    def settimeout(self, value):
        self.timeouts.append(value)


class FakeHTTP:
    def __init__(self):
        self.sock = None
        self.connects = self.closes = 0
        self.requests = []
        self.status = 200
        self.raw = None
        self.on_request = None

    def connect(self):
        self.connects += 1
        self.sock = FakeSocket()

    def close(self):
        self.closes += 1
        self.sock = None

    def request(self, method, path, raw, headers):
        self.requests.append((method, path, a.decode(raw), headers))
        if self.on_request:
            self.on_request()

    def getresponse(self):
        raw = self.raw
        if raw is None:
            raw = a.canonical({"jsonrpc": "2.0", "id": self.requests[-1][2]["id"],
                               "result": {"local_mock": True}})
        return SimpleNamespace(status=self.status, read=lambda limit: raw[:limit])


class FakeBuddy:
    def __init__(self, directory):
        self.LOCK_FILE = directory/"MOCK-profile.lock"
        self.LOG_FILE = directory/"MOCK-client.log"
        if not self.LOG_FILE.exists():
            e.retain(self.LOG_FILE, b"MOCK-ready100\n")
        info = self.LOG_FILE.stat()
        self.identity = {"profile": "uther", "character": "Aveline",
                         "character_guid": 7, "rpc_port": 8192}
        self.record = {"pid": 100, "start_ticks": 123, "exe_device": 1,
                       "exe_inode": 2, "argv": ["MOCK-owned-client", "--guid", "7"],
                       "log_device": info.st_dev, "log_inode": info.st_ino, "ready_offset": 0}
        self.alive = True
        self.profile_identity = mock.Mock(side_effect=lambda: dict(self.identity))
        self.load_record = mock.Mock(side_effect=lambda: copy.deepcopy(self.record))
        self.recorded_independent = mock.Mock(return_value=True)
        self.running_owned = mock.Mock(side_effect=lambda record: self.alive and record["pid"] == self.record["pid"])
        self.readiness = mock.Mock(return_value=True)
        self.private_file = mock.Mock(side_effect=e.private_fd)
        self.logout_line = mock.Mock(side_effect=lambda line: line == b"MOCK-logout\n")
        self.ready_line = mock.Mock(side_effect=lambda line, pid: line == ("MOCK-ready"+str(pid)+"\n").encode())
        self.stop = mock.Mock(side_effect=self._stop)
        self.start = mock.Mock(side_effect=self._start)

    def _stop(self, *, deadline):
        with self.LOG_FILE.open("ab") as target:
            target.write(b"MOCK-logout\n")
        self.alive = False

    def _start(self, *, deadline):
        self.record = dict(self.record, pid=self.record["pid"]+1,
                           start_ticks=self.record["start_ticks"]+1)
        self.LOG_FILE.write_bytes(("MOCK-ready"+str(self.record["pid"])+"\n").encode())
        self.alive = True


class FakeRPC:
    def __init__(self, clock, session="MOCK-chat"):
        self.clock, self.session = clock, session
        self.calls = []
        self.closes = 0
        self.echo = True
        self.on_send = None
        self.world = {"self": {"guid": "7", "present": True, "aliveKnown": True,
                               "alive": True, "healthKnown": True, "health": 100},
                      "nearbyCreatures": [{"entry": 823, "present": True}]}
        self.actions = {"classKnown": True, "classId": 8}
        self.loot = {"enabled": False, "pendingCorpseGuid": "0", "pendingGameObjectGuid": "0"}

    def call(self, method, params, *, say_plan=None):
        self.calls.append((method, copy.deepcopy(params), copy.deepcopy(say_plan)))
        if method == "buddy-world.snapshot":
            value = copy.deepcopy(self.world)
        elif method == "buddy-actions.status":
            value = dict(self.actions)
        elif method == "buddy-loot.state":
            value = dict(self.loot)
        elif method == "buddy-chat.send":
            if self.on_send:
                self.on_send(say_plan)
            value = {"transportWritten": True}
        elif method == "buddy-chat.poll":
            value = {"sessionId": self.session, "latestSequence": "40",
                     "nextSequence": "40", "messages": []}
            if params.get("limit") == 256 and self.echo:
                value.update(latestSequence="41", nextSequence="41", messages=[
                    {"sequence": "41", "senderGuid": "7", "self": True,
                     "channel": "SAY", "text": "Hello, friend."}])
        else:
            raise AssertionError("unexpected mocked RPC: "+method)
        raw = a.canonical({"mock_method": method, "result": value})
        return value, {"raw": raw, "source_sha256": a.digest(raw), "at": self.clock()}

    def close(self):
        self.closes += 1


class LocalOnly(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="research-executor-unit-")
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name).resolve()
        self.clock = Clock()
        for target, options in (
            ("socket.gethostname", {"return_value": "quasar.MOCK-local"}),
            ("platform.system", {"return_value": "Linux"}),
            ("fcntl.flock", {"return_value": None}),
            ("subprocess.Popen", {"side_effect": AssertionError("real Popen forbidden in local tests")}),
            ("http.client.HTTPConnection", {"side_effect": AssertionError("real HTTP forbidden in local tests")}),
            ("time.sleep", {"side_effect": AssertionError("real waiting forbidden in local tests")}),
        ):
            patcher = mock.patch("research_executor."+target, **options)
            patcher.start()
            self.addCleanup(patcher.stop)

    def budget(self):
        return e.HostBudget(120, clock=self.clock)

    def events(self, directory=None):
        return [a.decode(line, 65536) for line in (directory or self.directory).joinpath("dispatch.jsonl").read_bytes().splitlines()]


class HostBudgetTests(LocalOnly):
    def test_horizon_and_same_host_are_checked_at_every_boundary(self):
        budget = self.budget()
        self.assertEqual(budget.remaining(5), 5)
        self.clock.value = 119.5
        self.assertEqual(budget.remaining(5), .5)
        self.clock.value = 120
        with self.assertRaisesRegex(a.Rejected, "deadline expired"):
            budget.remaining()
        for seconds in (0, -1, 120.001, True, float("inf"), float("nan")):
            with self.subTest(seconds=seconds), self.assertRaises(a.Rejected):
                e.HostBudget(seconds, clock=self.clock)
        self.clock.value = 0
        for target, value in (("socket.gethostname", "quasar-other"),
                              ("socket.gethostname", "quasar.changed"),
                              ("platform.system", "Darwin")):
            with self.subTest(target=target, value=value), mock.patch("research_executor."+target, return_value=value):
                with self.assertRaises(a.Rejected):
                    budget.remaining()
        with mock.patch("research_executor.platform.system", return_value="Darwin"):
            with self.assertRaises(a.Rejected):
                self.budget()


class LedgerTests(LocalOnly):
    def test_reservation_is_durable_chained_and_not_replayed_after_reopen(self):
        ledger = e.Ledger(self.directory, wall=lambda: 100)
        row = ledger.reserve("dispatch1", "episode1", "encoding", MOCK_HASH, {"guid": 7})
        self.assertEqual(row["outcome"], "reserved")
        raw = ledger.path.read_bytes()
        self.assertTrue(raw.endswith(b"\n"))
        self.assertEqual(ledger.path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.events()[0]["previous_sha256"], "0"*64)
        reopened = e.Ledger(self.directory, wall=lambda: 101)
        with self.assertRaises(a.Rejected):
            reopened.reserve("dispatch1", "episode1", "encoding", MOCK_HASH, {})
        with self.assertRaises(a.Rejected):
            reopened.reserve("dispatch2", "episode1", "distractor", MOCK_HASH, {})
        self.assertEqual(ledger.path.read_bytes(), raw)
        reopened.outcome("dispatch1", "ambiguous")
        lines = ledger.path.read_bytes().splitlines()
        self.assertEqual(a.decode(lines[1])["previous_sha256"], a.digest(lines[0]))
        with self.assertRaises(a.Rejected):
            reopened.reserve("dispatch2", "episode1", "distractor", MOCK_HASH, {})
        with self.assertRaises(a.Rejected):
            reopened.outcome("dispatch1", "complete", MOCK_HASH)

    def test_phase_chain_and_completed_outcomes_are_required(self):
        ledger = e.Ledger(self.directory, wall=lambda: 100)
        for index, phase in enumerate(("encoding", "distractor", "probe")):
            ledger.reserve("dispatch"+str(index), "episode1", phase, MOCK_HASH, {})
            ledger.outcome("dispatch"+str(index), "complete", MOCK_HASH)
        self.assertEqual(len(self.events()), 6)
        with self.assertRaises(a.Rejected):
            ledger.reserve("dispatch4", "episode1", "probe", MOCK_HASH, {})
        with self.assertRaises(a.Rejected):
            ledger.reserve("dispatch5", "episode2", "probe", MOCK_HASH, {})
        for override in ("dispatch_id", "episode_id", "phase", "outcome", "request_sha256", "previous_sha256"):
            with self.subTest(override=override), self.assertRaises(a.Rejected):
                ledger.reserve("dispatch5", "episode2", "encoding", MOCK_HASH, {override: "tamper"})

    def test_request_and_episode_caps_include_ambiguous_reservations(self):
        for count in (4, 16):
            folder = self.directory/str(count)
            ledger = e.Ledger(folder, wall=lambda: 100)
            # Synthetic prior local ledger bytes, never actual subject dispatches.
            previous = "0"*64
            rows = []
            for index in range(count):
                event = {"kind": "reservation", "dispatch_id": "old"+str(index),
                         "episode_id": "episode"+str(index % 4), "phase": "encoding",
                         "reserved_at": 99, "outcome": "reserved", "request_sha256": MOCK_HASH,
                         "previous_sha256": previous}
                raw = a.canonical(event)
                rows.append(raw)
                previous = a.digest(raw)
            e.retain(ledger.path, b"\n".join(rows)+b"\n")
            with self.subTest(count=count), self.assertRaisesRegex(a.Rejected, "cap exhausted"):
                ledger.reserve("new", "episode4" if count == 4 else "episode0", "encoding", MOCK_HASH, {})
            self.assertEqual(ledger.path.read_bytes(), b"\n".join(rows)+b"\n")

    def test_partial_or_broken_chain_blocks_dispatch_without_repair(self):
        for name, raw in (("partial", b'{"kind":"reservation"}'),
                          ("chain", a.canonical({"previous_sha256": "1"*64})+b"\n")):
            ledger = e.Ledger(self.directory/name, wall=lambda: 100)
            e.retain(ledger.path, raw)
            with self.subTest(name=name), self.assertRaises(a.Rejected):
                ledger.reserve("new", "episode1", "encoding", MOCK_HASH, {})
            self.assertEqual(ledger.path.read_bytes(), raw)


class FreshSubjectTests(LocalOnly):
    def subject(self, folder="subject-state", builder=None):
        pin, config = subject_pin(self.directory)
        return e.FreshSubject(pin, config, self.directory/folder, command_builder=builder or self.command)

    def command(self, binary, empty, schema, output):
        self.addCleanup(shutil.rmtree, empty, ignore_errors=True)
        self.assertEqual(list(empty.iterdir()), [])
        return [str(binary), "exec", "--ephemeral", "--ignore-user-config", "--json",
                "--cd", str(empty), "--output-schema", str(schema), "--output-last-message", str(output), "-"]

    def popen(self, *, response=MOCK_PROBE, events=None, errors=b"", returncode=0):
        children = []
        def create(command, **kwargs):
            child = FakeChild(returncode)
            children.append(child)
            kwargs["stdout"].write(events if events is not None else cli_events("MOCK-thread-"+str(len(children))))
            kwargs["stderr"].write(errors)
            output = Path(command[command.index("--output-last-message")+1])
            if response is not None:
                output.write_bytes(response)
            return child
        return mock.Mock(side_effect=create), children

    def test_exact_model_new_context_and_usage_are_preserved_without_resume(self):
        subject = self.subject()
        popen, children = self.popen()
        request = b'{"local_mock":"one bounded request"}'
        with mock.patch("research_executor.subprocess.Popen", popen):
            first = subject.generate(request, a.PROBE_SCHEMA, self.budget())
            second = subject.generate(request, a.PROBE_SCHEMA, self.budget())
        self.assertEqual(popen.call_count, 2)
        commands = [call.args[0] for call in popen.call_args_list]
        for command, child, call in zip(commands, children, popen.call_args_list):
            self.assertEqual(command[-3:], ["--model", "MOCK-pinned-model", "-"])
            self.assertNotIn("resume", command)
            self.assertEqual(command.count("--model"), 1)
            self.assertTrue(call.kwargs["start_new_session"])
            self.assertTrue(call.kwargs["close_fds"])
            self.assertEqual(child.stdin.submitted, request)
        for flag in ("--cd", "--output-schema", "--output-last-message"):
            self.assertNotEqual(commands[0][commands[0].index(flag)+1], commands[1][commands[1].index(flag)+1])
        self.assertNotEqual(first[1]["cli_thread_id"], second[1]["cli_thread_id"])
        self.assertEqual(first[0], MOCK_PROBE)
        self.assertEqual(first[1]["usage"]["input_tokens"], 12)
        self.assertEqual(first[1]["configured_model"], "MOCK-pinned-model")
        for field in ("backend_model", "backend_version", "provider_request_id", "cost"):
            self.assertEqual(first[1][field], a.UNKNOWN)
        folders = list(subject.state_dir.glob("subject-*"))
        self.assertEqual(len(folders), 2)
        for folder in folders:
            self.assertEqual(folder.stat().st_mode & 0o777, 0o700)
            self.assertEqual((folder/"request.json").read_bytes(), request)
            self.assertEqual((folder/"schema.json").read_bytes(), a.canonical(a.PROBE_SCHEMA))

    def test_changed_identity_or_unsafe_command_never_launches(self):
        subject = self.subject()
        Path(subject.pin["binary"]).write_bytes(b"changed local mock binary")
        with self.assertRaises(a.Rejected):
            subject.generate(b"{}", a.PROBE_SCHEMA, self.budget())
        e.subprocess.Popen.assert_not_called()
        for index, forbidden in enumerate(("--ephemeral", "--ignore-user-config", "--json", "resume")):
            def unsafe(binary, empty, schema, output, forbidden=forbidden):
                command = self.command(binary, empty, schema, output)
                if forbidden == "resume":
                    command.insert(2, "resume")
                else:
                    command.remove(forbidden)
                return command
            subject = self.subject("unsafe"+str(index), unsafe)
            with self.subTest(forbidden=forbidden), self.assertRaises(a.Rejected):
                subject.generate(b"{}", a.PROBE_SCHEMA, self.budget())
            e.subprocess.Popen.assert_not_called()

    def test_failure_error_missing_receipts_and_missing_output_retain_evidence(self):
        cases = (("exit", {"returncode": 1}),
                 ("error", {"events": cli_events()+b'{"type":"error"}\n'}),
                 ("missing-receipt", {"events": b'{"type":"thread.started","thread_id":"MOCK"}\n'}),
                 ("missing-output", {"response": None}))
        for name, options in cases:
            subject = self.subject(name)
            popen, _ = self.popen(errors=b"LOCAL MOCK STDERR", **options)
            with self.subTest(name=name), mock.patch("research_executor.subprocess.Popen", popen):
                with self.assertRaises(a.Rejected):
                    subject.generate(b"{}", a.PROBE_SCHEMA, self.budget())
            popen.assert_called_once()
            folders = list(subject.state_dir.glob("subject-*"))
            self.assertEqual(len(folders), 1)
            self.assertEqual((folders[0]/"request.json").read_bytes(), b"{}")
            self.assertEqual((folders[0]/"stderr.txt").read_bytes(), b"LOCAL MOCK STDERR")
            self.assertTrue((folders[0]/"events.jsonl").is_file())

    def test_deadline_cancels_only_the_mocked_owned_child_and_retains_unknown(self):
        subject = self.subject()
        budget = self.budget()
        popen, children = self.popen(returncode=None)
        original_create = popen.side_effect
        def create(*args, **kwargs):
            child = original_create(*args, **kwargs)
            self.clock.value = budget.deadline
            return child
        popen.side_effect = create
        marker = self.directory/"MOCK-pidfd-file"
        marker.write_bytes(b"LOCAL MOCK descriptor only")
        pidfd = mock.Mock(side_effect=lambda pid: os.open(marker, os.O_RDONLY))
        send = mock.Mock()
        with mock.patch("research_executor.subprocess.Popen", popen), \
                mock.patch("research_executor.os.pidfd_open", pidfd, create=True), \
                mock.patch("research_executor.signal.pidfd_send_signal", send, create=True):
            with self.assertRaisesRegex(a.Rejected, "deadline expired"):
                subject.generate(b"{}", a.PROBE_SCHEMA, budget)
        popen.assert_called_once()
        pidfd.assert_called_once_with(children[0].pid)
        self.assertEqual(send.call_count, 1)
        self.assertEqual(send.call_args.args[1], e.signal.SIGTERM)
        folders = list(subject.state_dir.glob("subject-*"))
        evidence = a.decode((folders[0]/"unconfirmed-child.json").read_bytes())
        self.assertEqual(evidence["pid"], children[0].pid)
        self.assertIn("unknown", evidence["status"])
        self.assertTrue(children[0].stdin.closed)

    def test_fixed_allowance_must_fit_before_any_subject_launch(self):
        subject = self.subject()
        budget = self.budget()
        self.clock.value = 116
        with self.assertRaisesRegex(a.Rejected, "allowance cannot fit"):
            subject.generate(b"{}", a.PROBE_SCHEMA, budget)
        e.subprocess.Popen.assert_not_called()

    def test_completion_beyond_fixed_decision_allowance_is_not_success(self):
        subject = self.subject()
        budget = self.budget()
        popen, _ = self.popen()
        original_create = popen.side_effect
        def create(*args, **kwargs):
            child = original_create(*args, **kwargs)
            self.clock.value = subject.pin["decision_timeout_seconds"]+1
            return child
        popen.side_effect = create
        with mock.patch("research_executor.subprocess.Popen", popen):
            with self.assertRaises(a.Rejected):
                subject.generate(b"{}", a.PROBE_SCHEMA, budget)
        popen.assert_called_once()

    def test_setup_cannot_launch_a_subject_after_the_fixed_decision_deadline(self):
        def slow_setup(*args):
            command = self.command(*args)
            self.clock.value = 6
            return command
        subject = self.subject(builder=slow_setup)
        with self.assertRaises(a.Rejected):
            subject.generate(b"{}", a.PROBE_SCHEMA, self.budget())
        e.subprocess.Popen.assert_not_called()


class BoundRPCTests(LocalOnly):
    def connection(self):
        connection = FakeHTTP()
        factory = mock.Mock(return_value=connection)
        with mock.patch("research_executor.http.client.HTTPConnection", factory):
            rpc = e.BoundRPC(self.budget())
        factory.assert_called_once_with("127.0.0.1", 8192, timeout=5)
        return rpc, connection

    def test_single_pinned_connection_and_raw_receipts(self):
        rpc, connection = self.connection()
        for sequence, method in enumerate(sorted(e.READ_METHODS), 1):
            value, source = rpc.call(method, {})
            self.assertEqual(value, {"local_mock": True})
            self.assertEqual(source["source_sha256"], a.digest(source["raw"]))
            self.assertEqual(connection.requests[-1][2]["id"], sequence)
            self.assertEqual(connection.requests[-1][:2], ("POST", "/rpc"))
        self.assertEqual(connection.connects, 1)
        self.assertEqual(connection.closes, 0)
        self.assertEqual(connection.sock.timeouts, [5]*len(e.READ_METHODS))

    def test_forbidden_methods_wrong_say_scope_and_lost_socket_never_send(self):
        rpc, connection = self.connection()
        for method, plan in (("buddy-actions.practice", None), ("buddy-chat.send", None),
                             ("buddy-chat.send", {"guid": 1, "port": 8192, "method": "buddy-chat.send", "params": {}})):
            with self.subTest(method=method, plan=plan), self.assertRaises(a.Rejected):
                rpc.call(method, {}, say_plan=plan)
        connection.sock = FakeSocket()
        with self.assertRaisesRegex(a.Rejected, "no reconnect"):
            rpc.call("buddy-world.snapshot", {})
        self.assertEqual(connection.requests, [])
        self.assertEqual(connection.connects, 1)

    def test_ambiguous_write_retires_socket_and_is_never_replayed(self):
        rpc, connection = self.connection()
        connection.on_request = mock.Mock(side_effect=OSError("LOCAL MOCK socket loss"))
        params = {"channel": "SAY", "language": "COMMON", "text": "Hello, friend."}
        with self.assertRaises(e.Gap):
            rpc.call("buddy-chat.send", params, say_plan={
                "guid": 7, "port": 8192, "method": "buddy-chat.send", "params": params})
        with self.assertRaises(a.Rejected):
            rpc.call("buddy-world.snapshot", {})
        self.assertTrue(rpc.broken)
        self.assertEqual(len(connection.requests), 1)
        self.assertEqual(connection.connects, 1)
        self.assertEqual(connection.closes, 1)

    def test_direct_say_rejects_wrong_channel_language_or_nonfixed_text(self):
        rpc, connection = self.connection()
        for change in ({"channel": "PARTY"}, {"language": "ORCISH"}, {"text": ".kill"}, {"target": "Joel"}):
            params = dict(channel="SAY", language="COMMON", text="Hello, friend.")
            params.update(change)
            plan = {"guid": 7, "port": 8192, "method": "buddy-chat.send", "params": params}
            with self.subTest(change=change), self.assertRaises(a.Rejected):
                rpc.call("buddy-chat.send", params, say_plan=plan)
        self.assertEqual(connection.requests, [])

    def test_invalid_response_receipts_are_fail_closed_without_retry(self):
        for raw in (b"not JSON", a.canonical({"jsonrpc": "2.0", "id": 2, "result": {}}),
                    a.canonical({"jsonrpc": "2.0", "id": 1, "error": {"code": -1}}),
                    a.canonical({"jsonrpc": "2.0", "id": 1, "result": []}), b"x"*(256*1024+1)):
            rpc, connection = self.connection()
            connection.raw = raw
            with self.subTest(size=len(raw)), self.assertRaises(e.Gap):
                rpc.call("buddy-world.snapshot", {})
            self.assertEqual(connection.closes, 1)
            self.assertEqual(len(connection.requests), 1)
        rpc, connection = self.connection()
        self.clock.value = 120
        with self.assertRaises(a.Rejected):
            rpc.call("buddy-world.snapshot", {})
        self.assertEqual(connection.requests, [])

    def test_http_failure_or_connection_replacement_after_write_retires_connection(self):
        for change in ("status", "socket"):
            rpc, connection = self.connection()
            if change == "status":
                connection.status = 503
            else:
                connection.on_request = lambda: setattr(connection, "sock", FakeSocket())
            with self.subTest(change=change), self.assertRaises(e.Gap):
                rpc.call("buddy-world.snapshot", {})
            self.assertEqual(connection.connects, 1)
            self.assertEqual(connection.closes, 1)
            self.assertEqual(len(connection.requests), 1)


class ExecutorTests(LocalOnly):
    def executor(self, folder="executor", budget=None):
        directory = self.directory/folder
        directory.mkdir(mode=0o700, exist_ok=True)
        buddy = FakeBuddy(directory)
        created = []
        def factory(budget):
            rpc = FakeRPC(self.clock, "MOCK-chat-"+str(len(created)+1))
            created.append(rpc)
            return rpc
        admission = {"guid": 7, "profile": "uther", "port": 8192,
                     "engineering_receipt_id": "MOCK-engineering",
                     "progression_idle_receipt_id": "MOCK-idle", "source_sha256": MOCK_HASH,
                     "protocol_freeze_receipt_id": "MOCK-protocol-freeze",
                     "ledger_reconciliation_receipt_id": "MOCK-ledger-reconciliation",
                     "backend_pin_sha256": a.digest(a.canonical(subject_pin(self.directory)[0])),
                     "templates_sha256": a.digest(a.canonical(a.template_manifest()))}
        owner = e.ResearchExecutor(budget or self.budget(), directory, admission, buddy=buddy, rpc_factory=factory)
        owner.ledger.wall = lambda: 100+self.clock()
        return owner, buddy, created

    def probe_receipts(self, owner):
        """Synthetic persisted local receipts, never a scientific/model trial."""
        scope = owner.scope
        folder = owner.directory/"MOCK-probe"
        if folder.exists():
            return (a.decode((folder/"decision-receipt.json").read_bytes()),
                    a.decode((folder/"MOCK-reservation.json").read_bytes()))
        fields = {k: scope[k] for k in ("guid", "profile", "port", "client_session_id", "lease_id")}
        for phase in ("encoding", "distractor", "probe"):
            dispatch = "MOCK-probe" if phase == "probe" else "MOCK-fixture-"+phase
            reservation = owner.ledger.reserve(dispatch, scope["episode_id"], phase, MOCK_HASH, fields)
            owner.ledger.outcome(dispatch, "complete", a.digest(MOCK_PROBE) if phase == "probe" else MOCK_HASH)
        receipt = dict(reservation, outcome="complete", response_sha256=a.digest(MOCK_PROBE))
        e.private_directory(folder)
        e.retain(folder/"response.json", MOCK_PROBE)
        e.retain(folder/"decision-receipt.json", a.canonical(receipt))
        e.retain(folder/"MOCK-reservation.json", a.canonical(reservation))
        return receipt, reservation

    def unpersisted_probe_receipts(self, scope):
        reservation = {k: scope[k] for k in ("guid", "profile", "port", "client_session_id", "lease_id", "episode_id")}
        reservation.update(dispatch_id="MOCK-probe", phase="probe", request_sha256=MOCK_HASH, outcome="reserved")
        return dict(reservation, outcome="complete", response_sha256=a.digest(MOCK_PROBE)), reservation

    def cue(self, owner):
        source = a.canonical({"source_kind": "actual_public_npc_name_readback", "guid": 7,
                              "client_session_id": owner.scope["client_session_id"],
                              "npc_entry_id": 823, "npc_name": "LOCAL MOCK NPC"})
        observation = {"npc_entry_id": 823, "npc_name": "LOCAL MOCK NPC", "source_sha256": a.digest(source)}
        owner.admit_cue(observation, source)
        return observation

    def subject_mock(self):
        def generate(raw, schema, budget):
            value = a.decode(raw)
            if "code" in value:
                response = a.canonical({"key": value["key"], "code": value["code"],
                                        "say": a.greeting(value["code"])})
            elif "journal" in value:
                response = MOCK_PROBE
            else:
                response = a.canonical(value["npc"])
            events = cli_events()
            return response, a.cli_receipt(events, "MOCK-cli/1"), events
        pin, config = subject_pin(self.directory)
        subject = e.FreshSubject(pin, config, self.directory/"MOCK-subject-state",
                                 command_builder=lambda *args: (_ for _ in ()).throw(AssertionError("mock generate only")))
        subject.generate = mock.Mock(side_effect=generate)
        return subject

    def test_wrong_launcher_actor_never_opens_rpc(self):
        owner, buddy, rpcs = self.executor()
        buddy.identity["character_guid"] = 3
        with self.assertRaises(a.Rejected):
            with owner:
                self.fail("wrong launcher actor admitted")
        self.assertEqual(rpcs, [])
        buddy.stop.assert_not_called()
        buddy.start.assert_not_called()

    def test_wrong_backend_pin_is_rejected_before_dispatch_or_reservation(self):
        owner, _, _ = self.executor()
        subject = self.subject_mock()
        subject.identity_sha256 = "0"*64
        with owner:
            owner.preflight("MOCK-episode", "MOCK-lease")
            cue = self.cue(owner)
            request = a.build_request("encoding", "MOCK-key", cue, encoding_code="ABCDEFG2")
            with self.assertRaisesRegex(a.Rejected, "frozen normal backend"):
                owner.decide(subject, "encoding", request, "MOCK-encoding")
            self.assertFalse((owner.directory/"dispatch.jsonl").exists())
        subject.generate.assert_not_called()

    def test_preflight_retains_exact_private_rpc_sources_and_rejects_actor_or_idle_changes(self):
        owner, buddy, rpcs = self.executor()
        with owner:
            scope, raw = owner.preflight("MOCK-episode", "MOCK-lease")
            envelope = a.decode(raw, 65536)
            self.assertEqual(scope["guid"], 7)
            self.assertEqual(scope["source_sha256"], a.digest(raw))
            self.assertEqual((owner.directory/("preflight-"+a.digest(raw)+".json")).read_bytes(), raw)
            for receipt in envelope["rpc_receipts"]:
                self.assertEqual(a.digest(bytes.fromhex(receipt["raw"])), receipt["source_sha256"])
            self.assertEqual([method for method, _, _ in rpcs[0].calls],
                             ["buddy-world.snapshot", "buddy-actions.status", "buddy-loot.state", "buddy-chat.poll"])
            for change in ("actor", "owner", "moving", "loot"):
                rpc = rpcs[0]
                original = copy.deepcopy((rpc.world, rpc.actions, rpc.loot))
                if change == "actor":
                    rpc.world["self"]["guid"] = "3"
                elif change == "owner":
                    rpc.world["owner"] = {"guid": "3"}
                elif change == "moving":
                    rpc.actions["moving"] = True
                else:
                    rpc.loot["pendingGameObjectGuid"] = "1"
                with self.subTest(change=change), self.assertRaises(a.Rejected):
                    owner.preflight("MOCK-episode", "MOCK-lease")
                rpc.world, rpc.actions, rpc.loot = original
        self.assertEqual(rpcs[0].closes, 1)
        self.assertIsNone(owner.lease_fd)
        self.assertIsNone(owner.buddy_fd)

    def test_repeated_identical_readback_is_idempotent_but_changed_session_is_rejected(self):
        owner, _, rpcs = self.executor()
        with owner:
            first, raw = owner.preflight("MOCK-episode", "MOCK-lease")
            second, again = owner.preflight("MOCK-episode", "MOCK-lease")
            self.assertEqual(first, second)
            self.assertEqual(raw, again)
            self.assertEqual(len(list(owner.directory.glob("preflight-*.json"))), 1)
            rpcs[0].session = "MOCK-unadmitted-session"
            with self.assertRaisesRegex(a.Rejected, "changed outside"):
                owner.preflight("MOCK-episode", "MOCK-lease")

    def test_controller_reopen_cannot_reset_the_original_episode_horizon(self):
        owner, _, _ = self.executor()
        with owner:
            owner.preflight("MOCK-episode", "MOCK-lease")
        self.clock.value = 1
        replacement, buddy, rpcs = self.executor()
        with replacement:
            self.assertEqual(replacement.budget.deadline, 121)
            with self.assertRaisesRegex(a.Rejected, "cannot reset"):
                replacement.preflight("MOCK-episode", "MOCK-lease")
            self.assertIsNone(replacement.scope)
        self.assertFalse(any(method == "buddy-chat.send" for method, _, _ in rpcs[0].calls))
        buddy.start.assert_not_called()
        buddy.stop.assert_not_called()

    def test_subject_dispatch_has_durable_reservation_and_duplicate_is_blocked_after_reopen(self):
        owner, _, _ = self.executor()
        subject = self.subject_mock()
        with owner:
            owner.preflight("MOCK-episode", "MOCK-lease")
            cue = self.cue(owner)
            request = a.build_request("encoding", "MOCK-key", cue, encoding_code="ABCDEFG2")
            generate = subject.generate.side_effect
            def reserved_before_generate(raw, schema, budget):
                events = self.events(owner.directory)
                self.assertEqual(events[-1]["kind"], "reservation")
                self.assertEqual(events[-1]["outcome"], "reserved")
                self.assertEqual(events[-1]["request_sha256"], a.digest(raw))
                self.assertEqual((owner.directory/"MOCK-encoding"/"request.json").read_bytes(), raw)
                return generate(raw, schema, budget)
            subject.generate.side_effect = reserved_before_generate
            response, receipt, reservation = owner.decide(subject, "encoding", request, "MOCK-encoding")
            self.assertEqual(receipt["outcome"], "complete")
            self.assertEqual(reservation["outcome"], "reserved")
            self.assertEqual(a.digest(response), receipt["response_sha256"])
            with self.assertRaises((a.Rejected, FileExistsError)):
                owner.decide(subject, "encoding", request, "MOCK-encoding")
        replacement, _, _ = self.executor(budget=owner.budget)
        with replacement:
            self.clock.value += 1
            replacement.preflight("MOCK-episode", "MOCK-lease")
            self.cue(replacement)
            with self.assertRaises(a.Rejected):
                replacement.decide(subject, "encoding", request, "MOCK-other-dispatch")
        subject.generate.assert_called_once()

    def test_ambiguous_subject_attempt_consumes_phase_and_keeps_exact_logs(self):
        owner, _, _ = self.executor()
        subject = self.subject_mock()
        subject.generate.side_effect = RuntimeError("LOCAL MOCK ambiguous subject failure")
        with owner:
            owner.preflight("MOCK-episode", "MOCK-lease")
            cue = self.cue(owner)
            request = a.build_request("encoding", "MOCK-key", cue, encoding_code="ABCDEFG2")
            with self.assertRaises(e.Gap):
                owner.decide(subject, "encoding", request, "MOCK-encoding")
            self.assertEqual(self.events(owner.directory)[-1]["outcome"], "ambiguous")
            with self.assertRaises(a.Rejected):
                owner.decide(subject, "distractor", a.build_request("distractor", "MOCK-key", cue), "MOCK-distractor")
            self.assertEqual((owner.directory/"MOCK-encoding"/"request.json").read_bytes(), request)
        subject.generate.assert_called_once()

    def test_template_or_cue_changes_are_rejected_before_subject_reservation(self):
        owner, _, _ = self.executor()
        subject = self.subject_mock()
        with owner:
            owner.preflight("MOCK-episode", "MOCK-lease")
            cue = self.cue(owner)
            request = a.decode(a.build_request("encoding", "MOCK-key", cue, encoding_code="ABCDEFG2"))
            for index, change in enumerate(({"history": "LOCAL MOCK hidden channel"},
                                            {"instruction": "changed task"},
                                            {"npc": {"npc_entry_id": 1234, "npc_name": "wrong"}})):
                with self.subTest(change=change), self.assertRaises(a.Rejected):
                    owner.decide(subject, "encoding", a.canonical(dict(request, **change)), "MOCK-bad"+str(index))
            self.assertFalse((owner.directory/"dispatch.jsonl").exists())
        subject.generate.assert_not_called()

    def test_withheld_probe_reads_no_journal_and_receives_only_fresh_visible_bytes(self):
        owner, _, _ = self.executor()
        subject = self.subject_mock()
        with owner:
            owner.preflight("MOCK-episode", "MOCK-lease")
            cue = self.cue(owner)
            encoding = a.build_request("encoding", "MOCK-key", cue, encoding_code="ABCDEFG2")
            response, receipt, reservation = owner.decide(subject, "encoding", encoding, "MOCK-encoding")
            owner.commit_encoding(response, receipt, reservation)
            distractor = a.build_request("distractor", "MOCK-key", cue)
            owner.decide(subject, "distractor", distractor, "MOCK-distractor")
            probe = a.build_request("probe", "MOCK-key", cue)
            original_read = e.read_private
            def private_read(path, *args, **kwargs):
                self.assertNotEqual(Path(path).name, "episodic-experiment.json")
                return original_read(path, *args, **kwargs)
            with mock.patch("research_executor.read_private", side_effect=private_read):
                owner.decide(subject, "probe", probe, "MOCK-probe")
            self.assertEqual(subject.generate.call_count, 3)
            self.assertEqual([call.args[0] for call in subject.generate.call_args_list], [encoding, distractor, probe])
            self.assertNotIn(b"ABCDEFG2", distractor)
            self.assertNotIn(b"ABCDEFG2", probe)
            self.assertEqual(a.decode(probe)["journal"], [])

    def test_encoding_commit_rejects_cross_scope_receipts_before_journal_write(self):
        owner, _, _ = self.executor()
        subject = self.subject_mock()
        with owner:
            owner.preflight("MOCK-episode", "MOCK-lease")
            cue = self.cue(owner)
            request = a.build_request("encoding", "MOCK-key", cue, encoding_code="ABCDEFG2")
            response, receipt, reservation = owner.decide(subject, "encoding", request, "MOCK-encoding")
            for field in ("episode_id", "lease_id", "client_session_id"):
                with self.subTest(field=field), self.assertRaises(a.Rejected):
                    owner.commit_encoding(response, dict(receipt, **{field: "MOCK-other"}),
                                          dict(reservation, **{field: "MOCK-other"}))
            self.assertFalse((owner.directory/"episode-MOCK-episode"/"episodic-experiment.json").exists())

    def test_deadline_prevents_preflight_say_and_restart_dispatch(self):
        owner, buddy, rpcs = self.executor()
        with owner:
            owner.preflight("MOCK-episode", "MOCK-lease")
            receipt, reservation = self.probe_receipts(owner)
            before = len(rpcs[0].calls)
            self.clock.value = owner.budget.deadline
            for call in (lambda: owner.preflight("MOCK-episode", "MOCK-lease"),
                         lambda: owner.say(MOCK_PROBE, receipt, reservation), owner.restart_client):
                with self.assertRaises(a.Rejected):
                    call()
            self.assertEqual(len(rpcs[0].calls), before)
            buddy.stop.assert_not_called()
            buddy.start.assert_not_called()

    def test_say_reserves_before_write_and_never_replays_on_unknown_echo(self):
        owner, _, rpcs = self.executor()
        with owner:
            owner.preflight("MOCK-episode", "MOCK-lease")
            receipt, reservation = self.probe_receipts(owner)
            self.clock.value += 1
            rpcs[0].echo = False
            def before_send(plan):
                path = owner.directory/"episode-MOCK-episode"/"say-reservation.json"
                self.assertEqual(a.decode(path.read_bytes()), plan)
            rpcs[0].on_send = before_send
            self.assertEqual(owner.say(MOCK_PROBE, receipt, reservation), a.UNKNOWN)
            with self.assertRaises(a.Rejected):
                owner.say(MOCK_PROBE, receipt, reservation)
            self.assertEqual(sum(method == "buddy-chat.send" for method, _, _ in rpcs[0].calls), 1)
            self.assertEqual(sum(method == "buddy-chat.poll" and params.get("limit") == 256
                                 for method, params, _ in rpcs[0].calls), 1)

    def test_say_rejects_unpersisted_or_changed_probe_before_any_readback_or_write(self):
        owner, _, rpcs = self.executor()
        with owner:
            owner.preflight("MOCK-episode", "MOCK-lease")
            receipt, reservation = self.unpersisted_probe_receipts(owner.scope)
            calls = len(rpcs[0].calls)
            with self.assertRaises(a.Rejected):
                owner.say(MOCK_PROBE, receipt, reservation)
            self.assertEqual(len(rpcs[0].calls), calls)
            receipt, reservation = self.probe_receipts(owner)
            (owner.directory/"MOCK-probe"/"response.json").write_bytes(b"{}")
            with self.assertRaises(a.Rejected):
                owner.say(MOCK_PROBE, receipt, reservation)
            self.assertEqual(len(rpcs[0].calls), calls)
            self.assertFalse(owner.say_reserved)
            self.assertFalse((owner.directory/"episode-MOCK-episode"/"say-reservation.json").exists())

    def test_say_durable_reservation_blocks_new_executor_replay(self):
        owner, _, first_rpcs = self.executor()
        with owner:
            owner.preflight("MOCK-episode", "MOCK-lease")
            receipt, reservation = self.probe_receipts(owner)
            self.clock.value += 1
            self.assertEqual(owner.say(MOCK_PROBE, receipt, reservation), "confirmed")
        replacement, _, second_rpcs = self.executor(budget=owner.budget)
        with replacement:
            self.clock.value += 1
            replacement.preflight("MOCK-episode", "MOCK-lease")
            receipt, reservation = self.probe_receipts(replacement)
            self.clock.value += 1
            with self.assertRaises((a.Rejected, FileExistsError)):
                replacement.say(MOCK_PROBE, receipt, reservation)
        self.assertEqual(sum(method == "buddy-chat.send" for method, _, _ in first_rpcs[0].calls), 1)
        self.assertFalse(any(method == "buddy-chat.send" for method, _, _ in second_rpcs[0].calls))

    def test_deadline_after_durable_say_reservation_prevents_the_write(self):
        owner, _, rpcs = self.executor()
        with owner:
            owner.preflight("MOCK-episode", "MOCK-lease")
            receipt, reservation = self.probe_receipts(owner)
            original_retain = e.retain
            def reserve_then_expire(path, raw):
                original_retain(path, raw)
                if Path(path).name == "say-reservation.json":
                    self.clock.value = owner.budget.deadline
            with mock.patch("research_executor.retain", side_effect=reserve_then_expire):
                with self.assertRaises(a.Rejected):
                    owner.say(MOCK_PROBE, receipt, reservation)
            self.assertTrue((owner.directory/"episode-MOCK-episode"/"say-reservation.json").is_file())
            self.assertTrue(owner.say_reserved)
            self.assertFalse(any(method == "buddy-chat.send" for method, _, _ in rpcs[0].calls))

    def test_restart_uses_original_deadline_and_changed_process_chat_identity(self):
        owner, buddy, rpcs = self.executor()
        with owner:
            before, _ = owner.preflight("MOCK-episode", "MOCK-lease")
            self.clock.value += 1
            boundary = owner.restart_client()
            self.assertTrue(boundary["restarted"])
            buddy.stop.assert_called_once_with(deadline=120)
            buddy.start.assert_called_once_with(deadline=120)
            self.assertNotEqual(before["process_identity"], owner.scope["process_identity"])
            self.assertNotEqual(before["chat_session_id"], owner.scope["chat_session_id"])
            self.assertEqual(owner.scope["lease_expires_at"], 120)
            self.assertEqual(rpcs[0].closes, 1)
            path = owner.directory/"episode-MOCK-episode"
            receipt = a.decode((path/"client-restart.json").read_bytes(), 65536)
            for event, filename in zip(receipt["events"], ("client-old-logout.log", "client-old-exit.json", "client-new-ready.log")):
                self.assertEqual(event["source_sha256"], a.digest((path/filename).read_bytes()))
            self.assertEqual((path/"client-old-logout.log").read_bytes(), b"MOCK-logout\n")
            self.assertEqual((path/"client-new-ready.log").read_bytes(), b"MOCK-ready101\n")
            with self.assertRaises(a.Rejected):
                owner.restart_client()
            self.assertEqual(buddy.stop.call_count, 1)
            self.assertEqual(buddy.start.call_count, 1)

    def test_completed_restart_reservation_blocks_retry_after_controller_reopen(self):
        owner, buddy, _ = self.executor()
        with owner:
            owner.preflight("MOCK-episode", "MOCK-lease")
            self.clock.value += 1
            owner.restart_client()
        replacement, new_buddy, _ = self.executor(budget=owner.budget)
        new_buddy.record = dict(buddy.record)
        with replacement:
            replacement.preflight("MOCK-episode", "MOCK-lease")
            with self.assertRaises((a.Rejected, FileExistsError)):
                replacement.restart_client()
            new_buddy.stop.assert_not_called()
            new_buddy.start.assert_not_called()

    def test_stop_timeout_never_starts_and_already_running_is_not_restart(self):
        for name in ("timeout", "unchanged"):
            owner, buddy, rpcs = self.executor(name)
            with owner:
                owner.preflight("MOCK-episode", "MOCK-lease")
                if name == "timeout":
                    def stop(*, deadline):
                        buddy._stop(deadline=deadline)
                        self.clock.value = deadline
                    buddy.stop.side_effect = stop
                else:
                    buddy.start.side_effect = lambda **kwargs: setattr(buddy, "alive", True)
                with self.subTest(name=name), self.assertRaises(a.Rejected):
                    owner.restart_client()
                if name == "timeout":
                    buddy.start.assert_not_called()
                self.assertEqual(len(rpcs), 1)
                self.assertFalse((owner.directory/"episode-MOCK-episode"/"client-restart.json").exists())
            self.clock.value = 0

    def test_restart_requires_new_logout_and_attributed_ready_bytes(self):
        for name in ("missing-logout", "missing-ready"):
            owner, buddy, rpcs = self.executor(name)
            with owner:
                owner.preflight("MOCK-episode", "MOCK-lease")
                if name == "missing-logout":
                    buddy.stop.side_effect = lambda **kwargs: setattr(buddy, "alive", False)
                else:
                    def start(*, deadline):
                        buddy._start(deadline=deadline)
                        buddy.LOG_FILE.write_bytes(b"MOCK-unattributed-readiness\n")
                    buddy.start.side_effect = start
                with self.subTest(name=name), self.assertRaises(a.Rejected):
                    owner.restart_client()
                if name == "missing-logout":
                    buddy.start.assert_not_called()
                self.assertEqual(len(rpcs), 1)
                self.assertFalse((owner.directory/"episode-MOCK-episode"/"client-restart.json").exists())
                stop_calls, start_calls = buddy.stop.call_count, buddy.start.call_count
                with self.assertRaises(a.Rejected):
                    owner.restart_client()
                self.assertEqual(buddy.stop.call_count, stop_calls)
                self.assertEqual(buddy.start.call_count, start_calls)


if __name__ == "__main__":
    unittest.main()
