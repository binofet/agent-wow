"""Actor-bound tools for a direct model API; no general command execution.

The conversation host supplies authority and request identity. Gameplay remains
in the accepted Lua source, native game actions and the existing GM task host.
"""
import contextlib
import copy
import fcntl
import hashlib
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess
import sys
import tempfile


class ToolError(ValueError):
    pass


class ToolOutcomeUnknown(RuntimeError):
    """An entered mutation has no confirmed disposition; the model must halt."""
    outcome_unknown = True

    def __init__(self, receipt):
        self.receipt = copy.deepcopy(receipt)
        super().__init__("Tool outcome is unconfirmed; inspect the retained receipt, do not replay")


def packed(value):
    return json.dumps(value, allow_nan=False, separators=(",", ":")).encode()


def sha(value):
    return hashlib.sha256(value).hexdigest()


def require(value, reason):
    if not value:
        raise ToolError(reason)


def clean_error(error):
    return "".join(c if 32 <= ord(c) < 127 else " " for c in str(error))[:384]


def load_helper(path, name):
    loader = importlib.machinery.SourceFileLoader(name, str(path))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


class _PolicyRPC:
    def __init__(self, tools):
        self.tools, self.entered = tools, False

    def call(self, method, params, **kwargs):
        if method not in ("session.reload_policy", "buddy-actions.reload_behavior"):
            return self.tools._read(method, params)
        self.tools._authorise()
        catalog = self.tools._catalog()
        require(self.tools._methods(catalog).get(method, {}).get("effect") == "mutation",
                "installed_policy_mutation_required")
        require(params.get("sessionId") == self.tools.session_id and
                str(params.get("characterGuid", params.get("selfGuid"))) == self.tools.self_guid,
                "policy_body_binding_changed")
        self.tools._authorise()
        self.entered = True
        return self.tools.rpc.call(method, params, **kwargs)


class DirectTools:
    def __init__(self, rpc, session_id, self_guid, workspace, root, *,
                 allowed=lambda: False, cancelled=lambda: False, request_id=None,
                 helpers=None, runner=None, max_bytes=8 * 1024 * 1024, execution_ms=1000):
        require(isinstance(session_id, str) and re.fullmatch(r"[A-Za-z0-9._-]{1,128}", session_id),
                "explicit_session_required")
        require(str(self_guid).isdigit() and 0 < int(self_guid) < 2**48, "explicit_actor_required")
        require(type(max_bytes) is int and max_bytes > 0 and type(execution_ms) is int and
                0 < execution_ms <= 60000, "bounded_tool_options_required")
        self.rpc, self.session_id, self.self_guid = rpc, session_id, str(self_guid)
        self.workspace, self.root = Path(workspace).absolute(), Path(root).absolute()
        self.allowed, self.cancelled, self.request_id = allowed, cancelled, request_id
        self.helpers, self.runner = dict(helpers or {}), runner or subprocess.run
        self.max_bytes, self.execution_ms = max_bytes, execution_ms
        self.last_receipt = None
        self.unknown_receipt = None

    def _authorise(self):
        require(not self.cancelled(), "request_cancelled_before_dispatch")
        require(self.allowed() is True, "current_owner_development_authority_required")
        require(self.unknown_receipt is None, "unconfirmed_tool_requires_observation_not_retry")

    def _catalog(self):
        require(not self.cancelled(), "request_cancelled_before_dispatch")
        catalog = self.rpc.call("session.describe", {})
        require(catalog.get("sessionId") == self.session_id and
                str(catalog.get("characterGuid")) == self.self_guid, "current_body_changed")
        require(isinstance(catalog.get("revision"), str), "catalog_revision_unavailable")
        return catalog

    @staticmethod
    def _methods(catalog):
        rows = list(catalog.get("methods", [])) + list(catalog.get("hostMethods", []))
        result = {}
        for row in rows:
            if not isinstance(row, dict) or not isinstance(row.get("name"), str):
                continue
            require(row["name"] not in result, "duplicate_reflected_method")
            result[row["name"]] = row
        return result

    def _read(self, method, params):
        require(isinstance(params, dict), "object_parameters_required")
        before = self._catalog()
        metadata = self._methods(before).get(method)
        require(metadata is not None and metadata.get("effect") == "read",
                "installed_read_effect_required")
        params = copy.deepcopy(params)
        shape = before.get("messages", {}).get(metadata.get("inputType"), {})
        fields = {f.get("name") for f in shape.get("fields", []) if isinstance(f, dict)}
        fields.update(metadata.get("input", {}).get("properties", {}))
        for key, expected in (("sessionId", self.session_id), ("selfGuid", self.self_guid),
                              ("characterGuid", self.self_guid)):
            if key in params:
                require(str(params[key]) == expected, "foreign_read_identity_refused")
            if key in fields:
                params[key] = expected
        value = self.rpc.call(method, params)
        after = self._catalog()
        require(after["revision"] == before["revision"], "catalog_changed_during_read")
        require(len(packed(value)) <= self.max_bytes, "read_result_size_bound")
        if isinstance(value, dict):
            for key, expected in (("sessionId", self.session_id), ("selfGuid", self.self_guid),
                                  ("characterGuid", self.self_guid)):
                if key in value:
                    require(str(value[key]) == expected, "read_result_body_changed")
        return copy.deepcopy(value)

    def _helper(self, name):
        if name not in self.helpers:
            filename = {"self-edit": "self-edit", "gm_tasks": "gm_tasks.py"}.get(name)
            require(filename is not None, "maintained_helper_required")
            self.helpers[name] = load_helper(self.root / "dev" / filename,
                                             "direct_" + name.replace("-", "_"))
        return self.helpers[name]

    def _policy_binary(self):
        return Path(self.helpers.get("policy_binary", self.root / "data/bin/luapolicy"))

    def _path(self, value):
        require(isinstance(value, str) and 0 < len(value) <= 512 and "\\" not in value,
                "relative_workspace_path_required")
        path = PurePosixPath(value)
        require(not path.is_absolute() and str(path) == value and ".." not in path.parts,
                "relative_workspace_path_required")
        require(path.suffix.lower() in (".lua", ".json"), "lua_or_json_file_required")
        forbidden = {".git", ".ssh", ".aws", ".codex", ".agents", "config", "data"}
        require(not any(part.lower() in forbidden or part.startswith(".") for part in path.parts),
                "protected_workspace_path")
        require(not re.search(r"(?:^|[_.-])(?:auth|credentials?|secrets?|tokens?|passwords?)(?:[_.-]|$)",
                              path.name.lower()), "credential_path_refused")
        return path

    @contextlib.contextmanager
    def _parent(self, value, create=False):
        path = self._path(value)
        if create:
            self._authorise()
            self.workspace.mkdir(mode=0o700, parents=True, exist_ok=True)
        require(self.workspace.resolve() == self.workspace and not self.workspace.is_symlink(),
                "workspace_symlink_refused")
        info = self.workspace.stat()
        require(info.st_uid == os.geteuid() and not info.st_mode & 0o022,
                "owned_workspace_required")
        fd = os.open(self.workspace, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            for part in path.parts[:-1]:
                if create:
                    try:
                        os.mkdir(part, mode=0o700, dir_fd=fd)
                    except FileExistsError:
                        pass
                child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
                os.close(fd)
                fd = child
                info = os.fstat(fd)
                require(info.st_uid == os.geteuid() and not info.st_mode & 0o022,
                        "owned_workspace_directory_required")
            yield fd, path.name
        finally:
            os.close(fd)

    def _read_file(self, value):
        with self._parent(value) as (parent, name):
            fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
            with os.fdopen(fd, "rb") as stream:
                info = os.fstat(stream.fileno())
                require(stat.S_ISREG(info.st_mode) and info.st_uid == os.geteuid() and
                        info.st_nlink == 1 and info.st_size <= self.max_bytes,
                        "bounded_owned_regular_file_required")
                data = stream.read(self.max_bytes + 1)
            require(len(data) <= self.max_bytes, "workspace_file_size_bound")
            return data

    def _write_file(self, path, content, expected):
        self._authorise()
        require(isinstance(expected, str) and re.fullmatch(r"|[a-f0-9]{64}", expected),
                "explicit_file_hash_required")
        require(isinstance(content, str), "utf8_file_content_required")
        data = content.encode()
        require(len(data) <= self.max_bytes, "workspace_file_size_bound")
        if self._path(path).suffix.lower() == ".json":
            value = json.loads(content)
            packed(value)
        self._catalog()
        with self._parent(path, create=True) as (parent, name):
            lock = os.open(".direct-tools-edit.lock", os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW,
                           0o600, dir_fd=parent)
            temporary = None
            replacing = False
            try:
                require(os.fstat(lock).st_uid == os.geteuid() and os.fstat(lock).st_nlink == 1,
                        "owned_edit_lock_required")
                fcntl.flock(lock, fcntl.LOCK_EX)
                try:
                    old = self._read_file(path)
                except FileNotFoundError:
                    old = None
                require((sha(old) if old is not None else "") == expected,
                        "workspace_file_changed")
                self._authorise()
                self._catalog()
                self._authorise()
                temporary = ".direct-tools-" + os.urandom(12).hex()
                fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                             0o600, dir_fd=parent)
                with os.fdopen(fd, "wb") as stream:
                    stream.write(data)
                    stream.flush()
                    os.fsync(stream.fileno())
                self._authorise()
                replacing = True
                os.replace(temporary, name, src_dir_fd=parent, dst_dir_fd=parent)
                temporary = None
                os.fsync(parent)
            except Exception as error:
                if replacing:
                    self._unknown("workspace_write", dict(path=path, expectedSHA256=expected,
                                                          targetSHA256=sha(data)), error)
                raise
            finally:
                if temporary is not None:
                    os.unlink(temporary, dir_fd=parent)
                os.close(lock)
        return dict(path=path, sha256=sha(data), bytes=len(data), gameplayDispatched=False)

    def _unknown(self, name, details, error):
        receipt = dict(tool=name, sessionId=self.session_id, characterGuid=self.self_guid,
                       outcome="unconfirmed", replayAllowed=False, **details)
        receipt["error"] = clean_error(error)
        failure = getattr(self.rpc, "last_failure", None)
        if isinstance(failure, dict):
            receipt["rpcFailure"] = {k: failure[k] for k in ("method", "code", "grpcStatus", "reason")
                                     if k in failure}
        self.last_receipt = copy.deepcopy(receipt)
        self.unknown_receipt = copy.deepcopy(receipt)
        raise ToolOutcomeUnknown(receipt) from error

    def _policy_show(self, args):
        helper = self._helper("self-edit")
        result = helper.edit("show", int(self.self_guid), self.session_id, _PolicyRPC(self))
        self._catalog()
        if args.get("save_as") is not None:
            require(result.get("sourceKnown") is True and isinstance(result.get("source"), str),
                    "attached_source_unavailable")
            saved = self._write_file(args["save_as"], result["source"], args.get("expected_sha256", ""))
            return dict(sessionId=self.session_id, characterGuid=self.self_guid,
                        revision=sha(result["source"].encode()), saved=saved,
                        sourceKnown=True, gameplayDispatched=False)
        return result

    def _policy_apply(self, args):
        self._authorise()
        source = self._read_file(args["path"]).decode()
        tracked = _PolicyRPC(self)
        details = dict(expectedRevision=args["expected_revision"], targetRevision=sha(source.encode()))
        try:
            result = self._helper("self-edit").edit("apply", int(self.self_guid), self.session_id,
                                                    tracked, source, args["expected_revision"])
            self._catalog()
        except Exception as error:
            if tracked.entered:
                self._unknown("policy_apply", details, error)
            raise
        return dict(result=result, gameEffectClaimed=False, **details)

    def _policy_input(self, args):
        self._authorise()
        require(isinstance(args["value"], dict), "structured_command_required")
        value = packed(args["value"])
        require(len(value) <= self.max_bytes, "command_value_size_bound")
        catalog = self._catalog()
        require(self._methods(catalog).get("session.send_event", {}).get("effect") == "mutation",
                "installed_policy_input_required")
        self._authorise()
        try:
            # Identical fixed public request to dev/policy-input. That helper
            # has no callable function; avoid an argv-size-limited child.
            result = self.rpc.call("session.send_event", dict(sessionId=self.session_id,
                characterGuid=self.self_guid, kind="command", value=copy.deepcopy(args["value"])))
            self._catalog()
        except Exception as error:
            self._unknown("policy_input", dict(valueSHA256=sha(value)), error)
        return dict(result=result, deliveryOnly=True, gameEffectClaimed=False)

    def _evaluate(self, args):
        self._authorise()
        source = self._read_file(args["path"])
        require(self._path(args["path"]).suffix == ".lua" and isinstance(args["frame"], dict),
                "lua_source_and_object_frame_required")
        frame = packed(args["frame"])
        require(len(frame) <= self.max_bytes, "evaluation_frame_size_bound")
        self._catalog()
        self._authorise()
        with tempfile.TemporaryDirectory(prefix="direct-lua-") as folder:
            path = Path(folder) / "policy.lua"
            path.write_bytes(source)
            command = [str(self._policy_binary()), "--mode", "evaluate", "--source", str(path),
                       "--source-bytes", str(self.max_bytes), "--value-bytes", str(self.max_bytes),
                       "--execution-ms", str(self.execution_ms)]
            self._authorise()
            reply = self.runner(command, input=frame, capture_output=True, timeout=15, check=False)
        self._catalog()
        require(len(reply.stdout) <= self.max_bytes + 1, "evaluation_output_size_bound")
        if reply.returncode:
            return dict(valid=False, diagnostic=clean_error(reply.stderr.decode(errors="replace")),
                        sourceSHA256=sha(source), executionMs=self.execution_ms, gameplayDispatched=False)
        return dict(valid=True, decision=json.loads(reply.stdout), sourceSHA256=sha(source),
                    executionMs=self.execution_ms, gameplayDispatched=False)

    def _gm_folder(self, task_id):
        require(self.self_guid == "87" and isinstance(task_id, str) and
                re.fullmatch(r"direct-[a-f0-9]{24}", task_id), "bound_gm_task_id_required")
        return self.root / "data/converse/lotharen/gm-tasks" / task_id

    def _gm(self, name, args):
        require(self.self_guid == "87", "authenticated_gm_actor_required")
        gm = self._helper("gm_tasks")
        if name == "gm_task_start":
            self._authorise()
            require(isinstance(self.request_id, str) and 0 < len(self.request_id) <= 512,
                    "host_request_identity_required")
            goal = copy.deepcopy(args["goal"])
            require(isinstance(goal, dict), "gm_goal_object_required")
            task_id = "direct-" + sha((self.session_id + "\0" + self.request_id).encode())[:24]
            folder = self._gm_folder(task_id)
            require(not folder.exists() and not folder.is_symlink(), "gm_request_already_reserved")
            for member in goal.get("members", []):
                require(isinstance(member, dict), "observed_member_required")
                if member.get("rpcUrl"):
                    bindings = self.helpers.get("gm_bindings")
                    known = bindings() if bindings is not None else gm.bindings(self.root)
                    require(any(str(row["guid"]) == str(member.get("guid")) and
                                row["rpc_url"] == member["rpcUrl"] for row in known),
                            "current_observed_member_binding_required")
            goal["id"] = task_id
            require(len(packed(goal)) <= self.max_bytes, "gm_goal_size_bound")
            self._catalog()
            self._authorise()
            folder.mkdir(mode=0o700, parents=True, exist_ok=False)
            goal_path = folder / "goal.json"
            with goal_path.open("xb") as output:
                output.write(packed(goal))
                output.flush()
                os.fsync(output.fileno())
            goal_path.chmod(0o600)
            # Existing Host state, not a second ledger. Its run identity guard
            # rejects a replaced body before claiming the shared GM writer.
            with gm.task_store(folder) as store:
                host = gm.Host(store, self.rpc, None, None, goal, self.root / "behaviors/gm-recovery.lua",
                               self.root / "behaviors/gm-recovery-hold.lua")
                host.data["identity"] = dict(sessionId=self.session_id, guid="87", rpcUrl=self.rpc.url)
                host.persist()
            command = [sys.executable, "-B", str(self.root / "dev/gm-task"), "start",
                       "--rpc-url", self.rpc.url, "--session-id", self.session_id,
                       "--goal", str(goal_path), "--state-dir", str(folder),
                       "--transport", str(self.root / "data/converse/lotharen/gm-transport.json"),
                       "--policy-binary", str(self._policy_binary())]
        else:
            task_id = args["task_id"]
            folder = self._gm_folder(task_id)
            data = gm.task_store(folder).load()
            require(isinstance(data, dict) and data.get("identity") ==
                    dict(sessionId=self.session_id, guid="87", rpcUrl=self.rpc.url),
                    "gm_task_body_binding_changed")
            if name == "gm_task_status":
                self._catalog()
                return dict(taskId=task_id, result=gm.public_status(data), gameEffectClaimed=False)
            self._authorise()
            command = [sys.executable, "-B", str(self.root / "dev/gm-task"), "cancel",
                       "--state-dir", str(folder)]
        self._authorise()
        self._catalog()
        self._authorise()
        try:
            reply = self.runner(command, cwd=self.root, capture_output=True, timeout=15, check=False)
            require(reply.returncode == 0, "gm_task_helper_return_unconfirmed")
            result = json.loads(reply.stdout)
            self._catalog()
        except Exception as error:
            self._unknown(name, dict(taskId=task_id, stateDir=str(folder)), error)
        return dict(taskId=task_id, result=result, gameEffectClaimed=False)

    def tool_schemas(self):
        string = {"type": "string"}
        obj = {"type": "object"}
        specs = [
            ("session_catalog", "Inspect this body's current reflected actions/read inputs. Filter by method to include its schema.", {"method": string}, []),
            ("read_observation", "Call only an installed effect=read RPC on this exact actor; no arbitrary endpoint or mutation.", {"method": string, "params": obj}, ["method", "params"]),
            ("policy_show", "Read the actual accepted Lua. save_as snapshots it into owned Lua workspace without retranscribing the bundle; empty expected_sha256 means a new file.", {"save_as": string, "expected_sha256": string}, []),
            ("spell_inspect", "Inspect an actual learned spell and generic casting mechanics; effect classification is not cast availability.", {"spell_id": {"type": "integer", "minimum": 0}}, []),
            ("workspace_read", "Read bounded owned Lua/JSON text and its full SHA256; optional literal find locates a source section.", {"path": string, "start_line": {"type": "integer", "minimum": 1}, "max_lines": {"type": "integer", "minimum": 1, "maximum": 1000}, "find": string}, ["path"]),
        ]
        if self.allowed() is True:
            specs += [
                ("workspace_write", "Write an owned Lua/JSON file with exact old SHA256 CAS; empty hash requires a nonexistent file.", {"path": string, "content": string, "expected_sha256": string}, ["path", "content", "expected_sha256"]),
                ("workspace_patch", "Apply exact literal replacements to owned Lua/JSON under old-file SHA256 CAS. Each old text must occur exactly once.", {"path": string, "expected_sha256": string, "edits": {"type": "array", "items": {"type": "object", "properties": {"old": string, "new": string}, "required": ["old", "new"], "additionalProperties": False}}}, ["path", "expected_sha256", "edits"]),
                ("lua_evaluate", "Evaluate owned Lua with the maintained bounded pure evaluator. Decisions are not dispatched or game effects.", {"path": string, "frame": obj}, ["path", "frame"]),
                ("policy_apply", "CAS-apply owned Lua through maintained self-edit on this exact body, preserving pending state. Unknown reply halts, never retries.", {"path": string, "expected_revision": string}, ["path", "expected_revision"]),
                ("policy_input", "Deliver one structured command to an already-present Lua handler. Delivery is not game success; no arbitrary receipt/event injection.", {"value": obj}, ["value"]),
            ]
            if self.self_guid == "87" and self.request_id:
                specs += [("gm_task_start", "Start the whole commissioned GM recovery/regroup using the existing ordered Lua task. Goal uses current observed roster/bindings; one task per owner request, never overwrite/replay. Inspect status for actual arrivals/health/cleanup.", {"goal": obj}, ["goal"]),
                          ("gm_task_cancel", "Request existing task's observed cleanup; this is not a completion receipt.", {"task_id": string}, ["task_id"])]
        if self.self_guid == "87":
            specs += [("gm_task_status", "Read this body's retained task phase and actual receipts; no task execution.", {"task_id": string}, ["task_id"])]
        return [{"type": "function", "function": {"name": name, "description": description,
                 "parameters": {"type": "object", "properties": properties,
                                "required": required, "additionalProperties": False}}}
                for name, description, properties, required in specs]

    def dispatch(self, name, args):
        self.last_receipt = None
        try:
            require(isinstance(args, dict) and len(packed(args)) <= self.max_bytes,
                    "bounded_tool_arguments_required")
            schemas = {row["function"]["name"]: row["function"]["parameters"] for row in self.tool_schemas()}
            require(name in schemas, "tool_unavailable_for_this_authority")
            schema = schemas[name]
            require(not set(args) - set(schema["properties"]) and
                    set(schema["required"]) <= set(args), "tool_argument_fields_invalid")
            self._catalog()
            if name == "session_catalog":
                catalog = self._catalog()
                methods = self._methods(catalog)
                if args.get("method"):
                    method = methods.get(args["method"])
                    require(method is not None, "method_not_installed")
                    selected = {}
                    def include(name):
                        if name in selected:
                            return
                        shape = catalog.get("messages", {}).get(name)
                        if not isinstance(shape, dict):
                            return
                        selected[name] = copy.deepcopy(shape)
                        for field in shape.get("fields", []):
                            if field.get("kind") == "message":
                                include(field.get("type"))
                    include(method.get("inputType"))
                    include(method.get("outputType"))
                    result = dict(method=method, messages=selected, revision=catalog["revision"])
                else:
                    result = dict(methods=list(methods.values()), revision=catalog["revision"],
                                  sessionId=self.session_id, characterGuid=self.self_guid)
            elif name == "read_observation":
                result = self._read(args["method"], args["params"])
            elif name == "policy_show":
                result = self._policy_show(args)
            elif name == "spell_inspect":
                result = self._helper("self-edit").inspect_spell(int(self.self_guid), self.session_id,
                                                                 _PolicyRPC(self), args.get("spell_id", 0))
                self._catalog()
            elif name == "workspace_read":
                data = self._read_file(args["path"])
                lines = data.decode().splitlines(True)
                start = args.get("start_line", 1)
                count = args.get("max_lines", 200)
                require(type(start) is int and start > 0 and type(count) is int and 0 < count <= 1000,
                        "bounded_line_range_required")
                if args.get("find") is not None:
                    require(isinstance(args["find"], str) and 0 < len(args["find"]) <= 1024,
                            "bounded_literal_find_required")
                    offset = data.decode().find(args["find"])
                    require(offset >= 0, "literal_text_not_found")
                    start = data.decode()[:offset].count("\n") + 1
                result = dict(path=args["path"], sha256=sha(data), bytes=len(data), start_line=start,
                              content="".join(lines[start - 1:start - 1 + count]), total_lines=len(lines))
            elif name == "workspace_write":
                result = self._write_file(args["path"], args["content"], args["expected_sha256"])
            elif name == "workspace_patch":
                data = self._read_file(args["path"])
                require(sha(data) == args["expected_sha256"], "workspace_file_changed")
                value = data.decode()
                require(isinstance(args["edits"], list) and 0 < len(args["edits"]) <= 64,
                        "bounded_literal_edits_required")
                for edit in args["edits"]:
                    require(isinstance(edit, dict) and set(edit) == {"old", "new"} and
                            isinstance(edit["old"], str) and edit["old"] and
                            isinstance(edit["new"], str) and value.count(edit["old"]) == 1,
                            "unique_literal_patch_required")
                    value = value.replace(edit["old"], edit["new"], 1)
                result = self._write_file(args["path"], value, args["expected_sha256"])
            elif name == "lua_evaluate":
                result = self._evaluate(args)
            elif name == "policy_apply":
                result = self._policy_apply(args)
            elif name == "policy_input":
                result = self._policy_input(args)
            elif name.startswith("gm_task_"):
                result = self._gm(name, args)
            else:
                raise ToolError("tool_not_implemented")
            require(len(packed(result)) <= self.max_bytes, "tool_result_size_bound")
            self.last_receipt = dict(tool=name, sessionId=self.session_id, characterGuid=self.self_guid,
                                     outcome="completed", gameEffectClaimed=False)
            return dict(ok=True, result=result)
        except ToolOutcomeUnknown:
            raise
        except Exception as error:
            self.last_receipt = dict(tool=name, sessionId=self.session_id, characterGuid=self.self_guid,
                                     outcome="not_dispatched", error=clean_error(error))
            return dict(ok=False, error=self.last_receipt)
