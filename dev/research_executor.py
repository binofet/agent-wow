"""Source-only finite owner interface; no command-line dispatch entry point.

Production execution requires a Linux Quasar owner, actual backend-pin evidence,
a coordinated GUID7 idle-window receipt and fresh actor preflight. Nothing here
allocates cells, creates scorer keys, changes the protocol or discovers a model
by calling it. Absolute monotonic times never leave their originating host.
"""

from contextlib import contextmanager
import fcntl
import hashlib
import http.client
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import platform
import signal
import socket
import stat
import subprocess
import tempfile
import time

import research_adapter as a


ROOT = Path(__file__).resolve().parent.parent
READ_METHODS = frozenset(("buddy-world.snapshot", "buddy-actions.status",
                          "buddy-loot.state", "buddy-chat.poll"))
ENCODE_SCHEMA = {"type": "object", "properties": {k: {"type": "string"} for k in ("key", "code", "say")},
                 "required": ["key", "code", "say"], "additionalProperties": False}
DISTRACTOR_SCHEMA = {"type": "object", "properties": {"npc_entry_id": {"type": "integer"},
                     "npc_name": {"type": "string"}}, "required": ["npc_entry_id", "npc_name"],
                     "additionalProperties": False}


class Gap(a.Rejected):
    """Missing admission or ambiguous outcome; never an instruction to retry."""


def load_helper(name, path):
    loader = importlib.machinery.SourceFileLoader(name, str(path))
    spec = importlib.util.spec_from_loader(name, loader)
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


class HostBudget:
    def __init__(self, seconds, *, clock=time.monotonic):
        a.require(a.number(seconds) and 0 < seconds <= 120, "invalid episode horizon")
        self.clock = clock
        self.host = socket.gethostname()
        self.system = platform.system()
        self.started = clock()
        self.deadline = self.started + seconds
        self.check_host()

    def check_host(self):
        a.require(self.system == platform.system() == "Linux"
                  and self.host == socket.gethostname()
                  and self.host.split(".")[0].lower() == "quasar", "execution host is not local Linux Quasar")

    def remaining(self, cap=None):
        self.check_host()
        remaining = self.deadline-self.clock()
        a.require(remaining > 0, "episode deadline expired; dispatch forbidden")
        return remaining if cap is None else min(remaining, cap)


def private_directory(path):
    path = Path(path)
    for parent in (path, *path.parents):
        a.require(not parent.is_symlink(), "private path contains symlink")
    existed = path.exists()
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = path.stat()
    a.require(stat.S_ISDIR(info.st_mode) and info.st_uid == os.getuid()
              and info.st_mode & 0o077 == 0, "private directory must be owned mode0700")
    if not existed:
        sync_directory(path.parent)
    return path


def sync_directory(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def private_fd(path, flags):
    fd = os.open(path, flags | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
    info = os.fstat(fd)
    if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid()
            or info.st_nlink != 1 or info.st_mode & 0o077):
        os.close(fd)
        raise Gap("private evidence file identity or mode invalid")
    return fd


def retain(path, raw):
    a.require(isinstance(raw, bytes) and len(raw) <= 1024*1024, "evidence exceeds bound")
    with os.fdopen(private_fd(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL), "wb") as target:
        target.write(raw)
        target.flush()
        os.fsync(target.fileno())
    # Persist the directory entry before it is referenced by a durable ledger.
    sync_directory(Path(path).parent)


def read_private(path, limit=65536):
    with os.fdopen(private_fd(path, os.O_RDONLY), "rb") as source:
        raw = source.read(limit+1)
    a.require(len(raw) <= limit, "private evidence exceeds bound")
    return raw


@contextmanager
def retained_call_folders(state_dir):
    # Failed calls keep their exact private output/events/errors, too.
    private = Path(tempfile.mkdtemp(prefix="subject-", dir=state_dir))
    empty = Path(tempfile.mkdtemp(prefix="wow-research-empty-"))
    try:
        yield private, empty
    finally:
        # The empty workspace contains no journal, history or scorer. If a child
        # is unconfirmed, retaining an empty directory avoids pretending cleanup.
        pass


class Ledger:
    """One private append-only chain, reconciled under its file lock per update."""
    def __init__(self, directory, *, wall=time.time):
        self.directory = private_directory(directory)
        self.path = self.directory/"dispatch.jsonl"
        self.wall = wall

    @contextmanager
    def locked(self):
        with os.fdopen(private_fd(self.path, os.O_RDWR | os.O_CREAT | os.O_APPEND), "r+b") as f:
            fcntl.flock(f, fcntl.LOCK_EX)
            raw = f.read(1024*1024+1)
            a.require(len(raw) <= 1024*1024 and (not raw or raw.endswith(b"\n")), "ledger incomplete; no dispatch")
            events, previous = [], "0"*64
            for line in raw.splitlines():
                event = a.decode(line, 65536)
                a.require(type(event) is dict and event.get("previous_sha256") == previous,
                          "ledger chain invalid")
                previous = a.digest(line)
                events.append(event)
            yield f, events, previous

    @staticmethod
    def append(f, previous, value):
        raw = a.canonical(dict(value, previous_sha256=previous))+b"\n"
        f.write(raw)
        f.flush()
        os.fsync(f.fileno())

    def reserve(self, dispatch_id, episode_id, phase, request_sha256, scope):
        a.key(dispatch_id)
        a.key(episode_id)
        a.require(a.sha(request_sha256), "request bytes hash missing")
        with self.locked() as (f, events, previous):
            rows = {}
            for event in events:
                identity = event.get("dispatch_id")
                if event.get("kind") == "reservation":
                    a.require(identity not in rows, "duplicate ledger reservation")
                    rows[identity] = dict(event)
                elif event.get("kind") == "outcome":
                    a.require(identity in rows and rows[identity]["outcome"] == "reserved"
                              and event.get("outcome") in ("complete", "invalid", "failed", "ambiguous"),
                              "invalid ledger outcome")
                    rows[identity]["outcome"] = event["outcome"]
                else:
                    raise Gap("unknown ledger event")
            now = self.wall()
            proposed = a.reserve_request(tuple(rows.values()), dispatch_id, episode_id, phase, now)[-1]
            a.require(not set(scope) & {"dispatch_id", "episode_id", "phase", "reserved_at", "outcome", "kind",
                                        "request_sha256", "previous_sha256"}, "scope cannot override reservation")
            value = dict(proposed, **scope, kind="reservation", request_sha256=request_sha256)
            self.append(f, previous, value)
            sync_directory(self.directory)
            return value

    def outcome(self, dispatch_id, outcome, response_sha256=None):
        a.require(outcome != "complete" or a.sha(response_sha256), "complete response hash missing")
        with self.locked() as (f, events, previous):
            matches = [e for e in events if e.get("dispatch_id") == dispatch_id]
            a.require(len(matches) == 1 and matches[0].get("kind") == "reservation"
                      and outcome in ("complete", "invalid", "failed", "ambiguous"), "outcome already recorded or missing")
            self.append(f, previous, {"kind": "outcome", "dispatch_id": dispatch_id,
                                     "outcome": outcome, "response_sha256": response_sha256})
            sync_directory(self.directory)

    def receipt(self, dispatch_id):
        with self.locked() as (_, events, _):
            rows = [e for e in events if e.get("dispatch_id") == dispatch_id]
            a.require(len(rows) == 2 and rows[0].get("kind") == "reservation"
                      and rows[1].get("kind") == "outcome" and rows[1].get("outcome") == "complete"
                      and a.sha(rows[1].get("response_sha256")), "complete persisted decision missing")
            return dict(rows[0], outcome="complete", response_sha256=rows[1]["response_sha256"])


class FreshSubject:
    """One admitted normal Codex CLI path; no provider/model fallback or resume."""
    def __init__(self, pin, configuration_bytes, state_dir, *, command_builder=None):
        a.require(type(pin) is dict and isinstance(configuration_bytes, bytes)
                  and a.sha(pin.get("configuration_sha256"))
                  and pin["configuration_sha256"] == a.digest(configuration_bytes), "backend configuration receipt missing")
        config = a.decode(configuration_bytes, 65536)
        a.require(config.get("host") == "quasar" and config.get("source") == "owner_verified_normal_configuration"
                  and a.text(config.get("configured_model")) and a.text(config.get("owner_receipt_id"))
                  and pin.get("configured_model") == config["configured_model"], "normal backend/model identity UNKNOWN")
        a.require(a.text(pin.get("cli_version")) and a.sha(pin.get("binary_sha256"))
                  and Path(pin.get("binary", "")).is_absolute()
                  and a.number(pin.get("decision_timeout_seconds"))
                  and 0 < pin["decision_timeout_seconds"] <= 120, "CLI identity/allowance evidence missing")
        self.pin, self.configuration_bytes = dict(pin), configuration_bytes
        self.state_dir = private_directory(state_dir)
        self.command_builder = command_builder
        self.identity_sha256 = a.digest(a.canonical(self.pin))

    def generate(self, request_bytes, schema, budget):
        budget.remaining()
        allowance = self.pin["decision_timeout_seconds"]
        a.require(budget.remaining() >= allowance, "fixed model allowance cannot fit remaining horizon")
        decision_deadline = budget.clock()+allowance
        a.require(isinstance(request_bytes, bytes) and len(request_bytes) <= 4096, "request exceeds bound")
        binary = Path(self.pin["binary"])
        a.require(binary.is_file() and not binary.is_symlink() and os.access(binary, os.X_OK)
                  and a.digest(binary.read_bytes()) == self.pin["binary_sha256"], "CLI changed; no dispatch")
        builder = self.command_builder or load_helper("research_converse_command", ROOT/"dev/converse").codex_command
        with retained_call_folders(self.state_dir) as (private, empty):
            schema_path, output = private/"schema.json", private/"output.json"
            retain(private/"request.json", request_bytes)
            retain(schema_path, a.canonical(schema))
            command = builder(binary, empty, schema_path, output)
            a.require(command[-1] == "-" and "--ephemeral" in command and "--ignore-user-config" in command
                      and "--json" in command and "resume" not in command, "unsafe subject command")
            command = command[:-1]+["--model", self.pin["configured_model"], "-"]
            events_path, errors_path = private/"events.jsonl", private/"stderr.txt"
            with os.fdopen(private_fd(events_path, os.O_RDWR | os.O_CREAT | os.O_EXCL), "w+b") as events, \
                    os.fdopen(private_fd(errors_path, os.O_RDWR | os.O_CREAT | os.O_EXCL), "w+b") as errors:
                budget.remaining()
                a.require(budget.clock() < decision_deadline, "model allowance expired before dispatch")
                child = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=events, stderr=errors,
                                         start_new_session=True, close_fds=True)
                try:
                    child.stdin.write(request_bytes)
                    child.stdin.close()
                    while child.poll() is None:
                        remaining = min(budget.remaining(), decision_deadline-budget.clock())
                        a.require(remaining > 0, "fixed model decision timeout")
                        a.require(events.tell() <= 1024*1024 and errors.tell() <= 1024*1024,
                                  "subject output exceeds bound")
                        time.sleep(min(.05, remaining))
                    a.require(child.returncode == 0, "subject failed; no retry")
                    budget.remaining()
                    a.require(budget.clock() < decision_deadline, "model completion exceeded fixed allowance")
                    events.seek(0)
                    event_bytes = events.read(1024*1024+1)
                    usage = a.cli_receipt(event_bytes, self.pin["cli_version"])
                    a.require(output.is_file() and not output.is_symlink() and output.stat().st_size <= 4096,
                              "subject response missing")
                    response = output.read_bytes()
                    # Exact configured model is request evidence, not a backend-reported version.
                    usage.update(configured_model=self.pin["configured_model"],
                                 configuration_sha256=self.pin["configuration_sha256"])
                    budget.remaining()
                    a.require(budget.clock() < decision_deadline, "model receipt exceeded fixed allowance")
                    return response, usage, event_bytes
                finally:
                    # Cancel this exact owned CLI child once. No actor signals/force-kill/retry.
                    if child.poll() is None:
                        if hasattr(os, "pidfd_open") and hasattr(signal, "pidfd_send_signal"):
                            fd = os.pidfd_open(child.pid)
                            try:
                                if child.poll() is None:
                                    signal.pidfd_send_signal(fd, signal.SIGTERM)
                            finally:
                                os.close(fd)
                        retain(private/"unconfirmed-child.json", a.canonical({"pid": child.pid,
                               "status": "completion_unknown; no force kill or retry", "host": budget.host}))
                        # An unconfirmed child remains an explicit operator reconciliation gap.
                    if child.stdin and not child.stdin.closed:
                        child.stdin.close()


class BoundRPC:
    """One already-connected TCP socket to GUID7; never reconnect or replay."""
    def __init__(self, budget):
        self.budget, self.sequence, self.broken = budget, 0, False
        self.connection = http.client.HTTPConnection("127.0.0.1", 8192, timeout=budget.remaining(5))
        self.connection.connect()
        self.socket = self.connection.sock

    def close(self):
        self.broken = True
        self.connection.close()

    def call(self, method, params, *, say_plan=None):
        a.require(method in READ_METHODS or method == "buddy-chat.send" and say_plan is not None,
                  "RPC outside finite allowlist")
        if method == "buddy-chat.send":
            a.require(say_plan.get("guid") == 7 and say_plan.get("port") == 8192
                      and say_plan.get("method") == method and say_plan.get("params") == params,
                      "SAY plan scope mismatch")
            body = params.get("text") if type(params) is dict else None
            code = body[len("Hello, friend "):-1] if isinstance(body, str) and body.startswith("Hello, friend ") else None
            a.require(type(params) is dict and set(params) == {"channel", "language", "text"}
                      and params["channel"] == "SAY" and params["language"] == "COMMON"
                      and (body == a.greeting(a.UNKNOWN) or a.codeword(code) and body == a.greeting(code)),
                      "SAY body outside fixed harmless greeting")
        a.require(not self.broken and self.socket is not None and self.connection.sock is self.socket,
                  "bound RPC socket lost; no reconnect")
        self.socket.settimeout(self.budget.remaining(5))
        self.sequence += 1
        request = a.canonical({"jsonrpc": "2.0", "id": self.sequence, "method": method, "params": params})
        try:
            self.budget.remaining()
            self.connection.request("POST", "/rpc", request, {"Content-Type": "application/json"})
            response = self.connection.getresponse()
            raw = response.read(256*1024+1)
            a.require(response.status == 200 and len(raw) <= 256*1024
                      and self.connection.sock is self.socket, "RPC connection/response ambiguous")
            value = a.decode(raw, 256*1024)
            a.require(value.get("jsonrpc") == "2.0" and type(value.get("id")) is int
                      and value["id"] == self.sequence and "error" not in value
                      and type(value.get("result")) is dict, "RPC rejected or missing receipt")
            self.budget.remaining()
            return value["result"], {"raw": raw, "source_sha256": a.digest(raw), "at": self.budget.clock()}
        except Exception:
            self.close()
            raise Gap("RPC outcome unknown; connection retired and request never replayed") from None


class ResearchExecutor:
    """Owner-operated per-episode primitives, not a scientific batch orchestrator."""
    def __init__(self, budget, directory, owner_admission, *, buddy, rpc_factory=BoundRPC):
        budget.remaining()
        a.require(type(owner_admission) is dict and owner_admission.get("guid") == 7
                  and owner_admission.get("profile") == "uther" and owner_admission.get("port") == 8192
                  and a.text(owner_admission.get("engineering_receipt_id"))
                  and a.text(owner_admission.get("progression_idle_receipt_id"))
                  and a.sha(owner_admission.get("source_sha256")), "exclusive idle window not admitted")
        a.require(a.text(owner_admission.get("protocol_freeze_receipt_id"))
                  and a.text(owner_admission.get("ledger_reconciliation_receipt_id"))
                  and a.sha(owner_admission.get("backend_pin_sha256"))
                  and owner_admission.get("templates_sha256") == a.digest(a.canonical(a.template_manifest())),
                  "protocol/backend/templates/rolling allowance not frozen and reconciled")
        self.budget, self.directory, self.buddy = budget, private_directory(directory), buddy
        self.owner = dict(owner_admission)
        self.rpc_factory, self.rpc, self.lease_fd, self.buddy_fd = rpc_factory, None, None, None
        self.ledger = Ledger(self.directory)
        self.scope = None
        self.say_reserved = False
        self.cue = None
        self.visible_entries = set()
        self.episode_key = None
        self.restart_reserved = False

    def __enter__(self):
        b = self.buddy
        a.require(b.profile_identity().get("profile") == "uther"
                  and b.profile_identity().get("character") == "Aveline"
                  and b.profile_identity().get("character_guid") == 7
                  and b.profile_identity().get("rpc_port") == 8192, "launcher identity outside GUID7")
        try:
            for name, path in (("lease_fd", self.directory/"owner.lock"), ("buddy_fd", b.LOCK_FILE)):
                fd = private_fd(path, os.O_RDWR | os.O_CREAT)
                setattr(self, name, fd)
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            self.budget.remaining()
            self.rpc = self.rpc_factory(self.budget)
            return self
        except Exception:
            self.__exit__(None, None, None)
            raise

    def __exit__(self, *_):
        if self.rpc is not None:
            self.rpc.close()
            self.rpc = None
        for name in ("buddy_fd", "lease_fd"):
            fd = getattr(self, name)
            if fd is not None:
                os.close(fd)
                setattr(self, name, None)

    def process_receipt(self):
        self.budget.remaining()
        record = self.buddy.load_record()
        a.require(record is not None and self.buddy.recorded_independent(record)
                  and self.buddy.running_owned(record) and self.buddy.readiness(record), "owned GUID7 client not ready")
        identity = {k: record[k] for k in ("pid", "start_ticks", "exe_device", "exe_inode", "argv")}
        return {"record": record, "process_identity": a.digest(a.canonical(identity))}

    def preflight(self, episode_id, lease_id, *, allow_new_session=False):
        a.key(episode_id)
        a.key(lease_id)
        a.require(self.lease_fd is not None and self.buddy_fd is not None and self.rpc is not None, "owner lease absent")
        process = self.process_receipt()
        world, wr = self.rpc.call("buddy-world.snapshot", {})
        actions, ar = self.rpc.call("buddy-actions.status", {})
        loot, lr = self.rpc.call("buddy-loot.state", {})
        chat, cr = self.rpc.call("buddy-chat.poll", {"afterSequence": "0", "limit": 1})
        own = world.get("self", {})
        a.require(a.wire_uint(own.get("guid")) == 7 and own.get("present") is True
                  and own.get("aliveKnown") is True and own.get("alive") is True
                  and own.get("healthKnown") is True and own.get("health", 0) > 0
                  and not world.get("owner") and not world.get("ownerTarget"), "actor identity/vitals/owner isolation unconfirmed")
        flags = ("following", "healing", "moving", "castPending", "assisting", "traveling", "practicing",
                 "motionUnconfirmed", "autoAttacking", "autoAttackRequested", "autoAttackUnconfirmed")
        a.require(actions.get("classKnown") is True and actions.get("classId") == 8
                  and all(actions.get(k, False) is False for k in flags)
                  and not actions.get("interactionOwner") and loot.get("enabled", False) is False
                  and a.wire_uint(loot.get("pendingCorpseGuid", "0")) == 0
                  and a.wire_uint(loot.get("pendingGameObjectGuid", "0")) == 0, "controller/loot not idle")
        a.require(self.process_receipt()["process_identity"] == process["process_identity"], "process changed during preflight")
        receipts = [{k: v.hex() if isinstance(v, bytes) else v for k, v in receipt.items()}
                    for receipt in (wr, ar, lr, cr)]
        raw = a.canonical({"world": world, "actions": actions, "loot": loot, "chat": chat,
                           "process": process, "rpc_receipts": receipts})
        # RPC raw bytes stay private; only hashes render in normalized evidence.
        value = {"host": "quasar", "profile": "uther", "guid": 7, "port": 8192, "character_name": "Aveline",
                 "independent": True, "ready": True, "observed_at": self.budget.clock(),
                 "owner_admission_id": self.owner["engineering_receipt_id"], "episode_id": episode_id,
                 "lease_id": lease_id, "lease_exclusive": True, "process_identity": process["process_identity"],
                 "client_session_id": "owned-process-"+process["process_identity"], "chat_session_id": chat.get("sessionId"),
                 "episode_started_at": self.budget.started, "lease_expires_at": self.budget.deadline,
                 "idle": True, "alive": True, "loot_enabled": False, "pending_action": False,
                 "source_sha256": a.digest(raw)}
        a.admit_preflight(value, self.budget.clock())
        episode_path = private_directory(self.directory/("episode-"+episode_id))
        episode_bound = a.canonical({"episode_id": episode_id, "lease_id": lease_id,
                                     "host": self.budget.host, "started": self.budget.started,
                                     "deadline": self.budget.deadline,
                                     "owner_admission_sha256": a.digest(a.canonical(self.owner))})
        bound_path = episode_path/"original-bound.json"
        if bound_path.exists():
            a.require(read_private(bound_path) == episode_bound, "episode deadline/lease cannot reset or resume")
        else:
            retain(bound_path, episode_bound)
        if self.scope is not None and not allow_new_session:
            a.require(all(self.scope[field] == value[field] for field in
                          ("episode_id", "lease_id", "process_identity", "client_session_id", "chat_session_id")),
                      "actor/session changed outside admitted restart")
        source_path = self.directory/("preflight-"+a.digest(raw)+".json")
        if source_path.exists():
            a.require(read_private(source_path, 1024*1024) == raw, "preflight source changed")
        else:
            retain(source_path, raw)
        self.visible_entries = {row.get("entry") for name in ("questGivers", "nearbyCreatures")
                                for row in world.get(name, []) if type(row) is dict and row.get("present") is True}
        self.scope = value
        return value, raw

    def admit_cue(self, observation, source_bytes):
        """Name needs an actual owner-normalized public readback; snapshot lacks names.

        There is deliberately no database/static-name fallback or guessed NPC name.
        The maintained name-readback path remains an explicit host admission gap.
        """
        a.require(self.scope is not None and type(observation) is dict
                  and set(observation) == {"npc_entry_id", "npc_name", "source_sha256"}
                  and isinstance(source_bytes, bytes)
                  and a.digest(source_bytes) == observation["source_sha256"], "NPC source bytes missing")
        source = a.decode(source_bytes, 65536)
        a.require(source.get("source_kind") == "actual_public_npc_name_readback"
                  and source.get("guid") == 7 and source.get("client_session_id") == self.scope["client_session_id"]
                  and source.get("npc_entry_id") == observation["npc_entry_id"]
                  and source.get("npc_name") == observation["npc_name"]
                  and observation["npc_entry_id"] in self.visible_entries, "safe observed NPC cue unavailable")
        a.npc_observation(observation)
        path = self.directory/("npc-source-"+a.digest(source_bytes)+".json")
        if not path.exists():
            retain(path, source_bytes)
        self.cue = dict(observation)

    def validate_request(self, phase, raw):
        value = a.decode(raw)
        a.require(self.cue is not None and self.cue["npc_entry_id"] in self.visible_entries,
                  "current owner-selected NPC cue absent")
        npc = a.npc_observation(self.cue)
        a.require(type(value) is dict and value.get("npc") == npc, "request cue changed")
        if phase == "encoding":
            a.require(set(value) == {"instruction", "key", "code", "npc"}, "encoding input channels invalid")
            expected = a.build_request(phase, value["key"], self.cue, encoding_code=value["code"])
            self.episode_key = a.key(value["key"])
        elif phase == "distractor":
            a.require(set(value) == {"instruction", "npc"} and self.episode_key is not None,
                      "distractor input channels invalid")
            expected = a.build_request(phase, self.episode_key, self.cue)
        elif phase == "probe":
            a.require(set(value) == {"instruction", "npc", "journal"} and self.episode_key is not None
                      and type(value["journal"]) is list and len(value["journal"]) <= 1,
                      "probe input channels invalid")
            journal = None
            if value["journal"]:
                # Withheld [] never reads this file, prior model output or fallback.
                path = self.directory/("episode-"+a.key(self.scope["episode_id"]))/"episodic-experiment.json"
                journal = a.decode(read_private(path, 4096))
                write = a.decode(read_private(path.with_suffix(".receipt.json")))
                decision = a.decode(read_private(self.directory/a.key(write["dispatch_id"])/"decision-receipt.json"))
                response = read_private(self.directory/a.key(write["dispatch_id"])/"response.json", 4096)
                a.require(write.get("episode_id") == self.scope["episode_id"]
                          and write.get("lease_id") == self.scope["lease_id"], "journal scope mismatch")
                a.committed_record(response, decision, write, write)
                a.require(value["journal"] == [journal], "probe journal is not actual committed record")
            expected = a.build_request(phase, self.episode_key, self.cue, journal=journal)
        else:
            raise Gap("unsupported subject phase")
        a.require(raw == expected, "subject input differs from fixed fresh template")
        return value

    def decide(self, subject, phase, request_bytes, dispatch_id):
        self.budget.remaining()
        a.require(self.scope is not None and self.lease_fd is not None, "fresh actor admission absent")
        a.require(isinstance(subject, FreshSubject)
                  and subject.identity_sha256 == self.owner["backend_pin_sha256"],
                  "subject differs from frozen normal backend")
        self.preflight(self.scope["episode_id"], self.scope["lease_id"])
        value = self.validate_request(phase, request_bytes)
        schemas = {"encoding": ENCODE_SCHEMA, "distractor": DISTRACTOR_SCHEMA, "probe": a.PROBE_SCHEMA}
        a.require(phase in schemas, "unsupported subject phase")
        scope = {k: self.scope[k] for k in ("guid", "profile", "port", "client_session_id", "lease_id")}
        folder = private_directory(self.directory/a.key(dispatch_id))
        retain(folder/"request.json", request_bytes)
        reservation = self.ledger.reserve(dispatch_id, self.scope["episode_id"], phase, a.digest(request_bytes), scope)
        received = False
        try:
            self.budget.remaining()
            response, usage, events = subject.generate(request_bytes, schemas[phase], self.budget)
            received = True
            retain(folder/"response.json", response)
            retain(folder/"events.jsonl", events)
            retain(folder/"usage.json", a.canonical(usage))
            parsed = a.decode(response)
            if phase == "probe":
                a.validate_probe(response)
            elif phase == "distractor":
                a.require(parsed == value.get("npc"), "distractor invalid; no repair")
            else:
                a.require(type(parsed) is dict and set(parsed) == {"key", "code", "say"}
                          and parsed["key"] == value.get("key") and parsed["code"] == value.get("code")
                          and parsed["say"] == a.greeting(parsed["code"]), "encoding invalid; no substituted record")
            receipt = dict(reservation, outcome="complete", response_sha256=a.digest(response))
            if phase == "encoding":
                receipt["key"] = parsed["key"]
            retain(folder/"decision-receipt.json", a.canonical(receipt))
            self.budget.remaining()
        except Exception:
            self.ledger.outcome(dispatch_id, "invalid" if received else "ambiguous")
            raise Gap("subject decision incomplete/invalid; reservation retained, no retry") from None
        self.ledger.outcome(dispatch_id, "complete", a.digest(response))
        return response, receipt, reservation

    def commit_encoding(self, response, receipt, reservation):
        self.budget.remaining()
        a.require(self.scope is not None and self.lease_fd is not None and self.buddy_fd is not None,
                  "encoding scope/lease absent")
        fields = ("guid", "profile", "port", "client_session_id", "lease_id", "episode_id")
        a.require(all(reservation.get(field) == self.scope[field] and receipt.get(field) == self.scope[field]
                      for field in fields), "encoding belongs to another actor/episode/session")
        persisted = self.ledger.receipt(reservation["dispatch_id"])
        a.require(all(persisted.get(field) == reservation.get(field) for field in
                      (*fields, "dispatch_id", "phase", "request_sha256", "reserved_at"))
                  and persisted["response_sha256"] == a.digest(response)
                  and read_private(self.directory/a.key(reservation["dispatch_id"])/"response.json", 4096) == response,
                  "encoding decision not persisted")
        a.require(a.decode(read_private(self.directory/a.key(reservation["dispatch_id"])/"decision-receipt.json")) == receipt,
                  "actual encoding decision receipt differs")
        record = a.decode(response)
        origin = dict(reservation, key=record.get("key"))
        decision = dict(receipt, key=record.get("key"))
        a.require(origin["key"] == self.episode_key, "encoding historical key changed")
        journal = private_directory(self.directory/("episode-"+a.key(self.scope["episode_id"]))) / "episodic-experiment.json"
        raw = a.canonical(record)
        write = dict(origin, kind="episodic_experiment", observed=True,
                     receipt_id="journal-"+reservation["dispatch_id"], record_sha256=a.digest(raw))
        # Validate every binding before the actual separate-journal write.
        a.committed_record(response, decision, write, origin)
        retain(journal, raw)
        retain(journal.with_suffix(".receipt.json"), a.canonical(write))
        return record, write

    def say(self, probe_bytes, probe_receipt, probe_reservation):
        self.budget.remaining()
        a.require(self.scope is not None and not self.say_reserved, "SAY already attempted or no admission")
        a.require(self.lease_fd is not None and self.buddy_fd is not None, "owner lease absent")
        persisted = self.ledger.receipt(probe_reservation["dispatch_id"])
        fields = ("guid", "profile", "port", "client_session_id", "lease_id", "episode_id")
        a.require(all(probe_reservation.get(field) == self.scope[field]
                      and probe_receipt.get(field) == self.scope[field] for field in fields)
                  and persisted.get("phase") == "probe"
                  and all(persisted.get(field) == probe_reservation.get(field) for field in
                          (*fields, "dispatch_id", "phase", "request_sha256", "reserved_at"))
                  and persisted["response_sha256"] == a.digest(probe_bytes), "probe decision not persisted for current actor")
        call_path = self.directory/a.key(probe_reservation["dispatch_id"])
        a.require(read_private(call_path/"response.json", 4096) == probe_bytes
                  and a.decode(read_private(call_path/"decision-receipt.json")) == probe_receipt,
                  "actual probe response/decision receipt differs")
        fresh, _ = self.preflight(self.scope["episode_id"], self.scope["lease_id"])
        cursor, source = self.rpc.call("buddy-chat.poll", {"afterSequence": "0", "limit": 1,
                                                          "sessionId": fresh["chat_session_id"]})
        cursor = dict(cursor, observed_at=source["at"], source_sha256=source["source_sha256"])
        plan = a.reserve_say(fresh, cursor, probe_bytes, probe_receipt, probe_reservation, now=self.budget.clock())
        path = self.directory/("episode-"+a.key(fresh["episode_id"]))
        private_directory(path)
        retain(path/"say-reservation.json", a.canonical(plan))
        self.say_reserved = True  # Durable reservation above prevents replay across a new process.
        self.budget.remaining()
        try:
            result, source = self.rpc.call("buddy-chat.send", plan["params"], say_plan=plan)
            write = dict(result, guid=7, port=8192, client_session_id=fresh["client_session_id"],
                         chat_session_id=fresh["chat_session_id"], at=source["at"],
                         source_sha256=source["source_sha256"], plan_sha256=a.digest(a.canonical(plan)))
            retain(path/"say-write.json", a.canonical(write))
            # One complete readback only: lack of an echo is UNKNOWN, never resend.
            poll, source = self.rpc.call("buddy-chat.poll", {"afterSequence": str(plan["after_sequence"]),
                                                             "limit": 256, "sessionId": fresh["chat_session_id"]})
            poll = dict(poll, observed_at=source["at"], source_sha256=source["source_sha256"],
                        client_session_id=fresh["client_session_id"])
            retain(path/"say-echo.json", a.canonical(poll))
            return a.classify_echo(plan, poll, write)
        except Exception:
            return a.UNKNOWN

    def restart_client(self):
        a.require(self.scope is not None and self.buddy_fd is not None and not self.restart_reserved,
                  "owner lease missing or restart already attempted")
        self.budget.remaining()
        before = dict(self.scope)
        old = self.process_receipt()
        path = private_directory(self.directory/("episode-"+a.key(before["episode_id"])))
        retain(path/"client-restart-reservation.json", a.canonical({"before": before,
                  "deadline": self.budget.deadline, "status": "reserved_before_stop; no retry"}))
        self.restart_reserved = True
        with os.fdopen(self.buddy.private_file(self.buddy.LOG_FILE, os.O_RDONLY), "rb") as log:
            info = os.fstat(log.fileno())
            a.require((info.st_dev, info.st_ino) == (old["record"]["log_device"], old["record"]["log_inode"]),
                      "old session log identity changed")
            offset = info.st_size
        self.rpc.close()
        self.rpc = None
        # These helpers share this host's exact one episode deadline, never a new120s budget.
        self.buddy.stop(deadline=self.budget.deadline)
        a.require(not self.buddy.running_owned(old["record"]), "old process exit missing")
        stopped_at = self.budget.clock()
        # Capture the actual attributed logout window BEFORE start truncates it.
        with os.fdopen(self.buddy.private_file(self.buddy.LOG_FILE, os.O_RDONLY), "rb") as log:
            info = os.fstat(log.fileno())
            a.require((info.st_dev, info.st_ino) == (old["record"]["log_device"], old["record"]["log_inode"])
                      and info.st_size >= offset, "old logout log replaced/truncated")
            log.seek(offset)
            logout_bytes = log.read(256*1024+1)
        a.require(len(logout_bytes) <= 256*1024 and any(self.buddy.logout_line(line)
                  for line in logout_bytes.splitlines(keepends=True)), "actual server logout receipt missing")
        retain(path/"client-old-logout.log", logout_bytes)
        exit_bytes = a.canonical({"old_process": old, "running_owned": False, "observed_at": stopped_at})
        retain(path/"client-old-exit.json", exit_bytes)
        self.budget.remaining()
        self.buddy.start(deadline=self.budget.deadline)
        new = self.process_receipt()
        a.require(new["process_identity"] != old["process_identity"], "already-running is not a restart")
        with os.fdopen(self.buddy.private_file(self.buddy.LOG_FILE, os.O_RDONLY), "rb") as log:
            ready_offset = new["record"].get("ready_offset")
            a.require(a.uint(ready_offset), "new ready offset missing")
            log.seek(ready_offset)
            ready_bytes = log.readline(65537)
        a.require(len(ready_bytes) <= 65536 and self.buddy.ready_line(ready_bytes, new["record"]["pid"]),
                  "actual new ready receipt missing")
        retain(path/"client-new-ready.log", ready_bytes)
        self.rpc = self.rpc_factory(self.budget)
        after, raw = self.preflight(before["episode_id"], before["lease_id"], allow_new_session=True)
        self.cue = None  # New client requires a fresh owner-supported NPC-name readback.
        sources = {"logout_complete": a.digest(logout_bytes), "process_exit": a.digest(exit_bytes),
                   "new_ready": a.digest(ready_bytes)}
        events = tuple({"kind": kind, "observed": True, "guid": 7, "lease_id": before["lease_id"],
                        "at": at, "process_identity": identity, "source_sha256": sources[kind]}
                       for kind, at, identity in (("logout_complete", stopped_at, old["process_identity"]),
                                                  ("process_exit", stopped_at, old["process_identity"]),
                                                  ("new_ready", after["observed_at"], new["process_identity"])))
        boundary = a.client_boundary(before, after, events, restarted=True, now=self.budget.clock())
        retain(path/"client-restart.json", a.canonical({"before": before, "after": after,
                                                       "events": events, "boundary": boundary}))
        return boundary
