"""Direct provider HTTP transports and one bounded caller-owned tool exchange.

The caller supplies the persona, reply schema and actor-bound dispatcher. This
module owns no game connection, shell, task scheduler or persistent action state.
"""
import copy
import fcntl
import hashlib
import http.client
import json
import math
import os
from pathlib import Path
import queue
import re
import socket
import stat
import tempfile
import threading
import time
import urllib.parse
import uuid


FINAL_REPLY = "final_reply"
ANTHROPIC_VERSION = "2026-10-21"


class ModelError(RuntimeError):
    def __init__(self, reason, receipt=None):
        super().__init__(reason)
        self.reason = reason
        self.receipt = copy.deepcopy(receipt or {})


class Cancelled(ModelError):
    pass


class ToolOutcomeUnknown(RuntimeError):
    """The caller cannot safely repeat an attempted tool operation."""
    outcome_unknown = True


class _Transcript:
    """Private API conversation, not a queue of actions to execute."""
    def __init__(self, path, binding, limit, memory):
        self.path, self.binding, self.limit = Path(path) if path is not None else None, binding, limit
        self.memory, self.lock = memory, None

    def __enter__(self):
        if self.path is None:
            self.state = self.memory.setdefault(self.binding, {})
        else:
            if self.path.is_symlink(): raise ModelError("conversation_symlink_refused")
            self.path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            self.path = self.path.parent.resolve() / self.path.name
            info = self.path.parent.stat()
            if info.st_uid != os.geteuid() or info.st_mode & 0o077:
                raise ModelError("private_conversation_directory_required")
            fd = os.open(str(self.path)+".lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
            self.lock = os.fdopen(fd, "r+")
            try:
                info = os.fstat(fd)
                if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077 or info.st_nlink != 1:
                    raise ModelError("private_conversation_lock_required")
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
                try:
                    with os.fdopen(os.open(self.path, os.O_RDONLY | os.O_NOFOLLOW), "rb") as source:
                        info = os.fstat(source.fileno())
                        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077:
                            raise ModelError("private_conversation_file_required")
                        raw = source.read(self.limit+1)
                    if len(raw) > self.limit: raise ModelError("conversation_storage_bound")
                    self.state = _decode(raw)
                except FileNotFoundError:
                    self.state = {}
                if not isinstance(self.state, dict): raise ModelError("invalid_conversation_state")
            except BaseException:
                self.lock.close(); self.lock = None
                raise
        if self.state and self.state.get("binding") != self.binding:
            self.__exit__(); raise ModelError("conversation_binding_changed")
        if not self.state:
            self.state.update(version=1, binding=self.binding, messages=[], requests={}, seen={}, unknown={}, pending=None)
        return self

    def save(self):
        raw = _packed(self.state, self.limit)
        if self.path is None: return
        fd, name = tempfile.mkstemp(prefix=".conversation-", dir=self.path.parent)
        try:
            with os.fdopen(fd, "wb") as output:
                output.write(raw); output.flush(); os.fsync(output.fileno())
            os.replace(name, self.path)
            directory = os.open(self.path.parent, os.O_RDONLY)
            try: os.fsync(directory)
            finally: os.close(directory)
        finally:
            if os.path.exists(name): os.unlink(name)

    def __exit__(self, *_):
        if self.lock: self.lock.close(); self.lock = None


def _safe(value, limit=384):
    if not isinstance(value, str):
        return None
    value = " ".join("".join(c if c.isprintable() else " " for c in value).split())
    value = re.sub(r"(?i)\b(Bearer|Basic)\s+[A-Za-z0-9./_+~=-]+", r"\1 [redacted]", value)
    value = re.sub(r"(?i)(https?://)[^/\s@]+@", r"\1[redacted]@", value)
    value = re.sub(r"(?i)\b(api[_-]?key|access[_-]?token|refresh[_-]?token|token|authorization|password|secret|key)"
                   r"[\"']?\s*[:=]\s*[\"']?[^\s,\"'&;}]+", r"\1=[redacted]", value)
    value = re.sub(r"\bsk-[A-Za-z0-9_-]+", "[redacted]", value)
    raw = value.encode("utf-8", "replace")
    return raw.decode() if len(raw) <= limit else raw[:limit-3].decode("utf-8", "ignore") + "..."


def _provider_error(value):
    """Retain bounded diagnostic fields, never the response/prompt wholesale."""
    nested = value.get("error") if isinstance(value, dict) else None
    sources = [nested, value] if isinstance(nested, dict) else [value]
    result = {}
    for source in sources:
        if not isinstance(source, dict): continue
        for key in ("type", "code", "reason", "message", "status"):
            item = source.get(key)
            if key not in result and (isinstance(item, str) or type(item) is int and abs(item) < 2**53):
                result[key] = _safe(item, 384 if key in ("message", "reason") else 128) if isinstance(item, str) else item
    if "message" not in result and isinstance(nested, str): result["message"] = _safe(nested)
    if "message" not in result and isinstance(value, dict) and isinstance(value.get("detail"), str):
        result["message"] = _safe(value["detail"])
    return result or dict(reason="empty_error_object" if nested == {} else "provider_error_without_detail")


def _read_custody(value):
    """Explicit unresolved action evidence must remain in the active window."""
    if isinstance(value, list): return any(_read_custody(row) for row in value)
    if not isinstance(value, dict): return False
    if value.get("ok") is False or value.get("outcomeUnknown") is True or value.get("replayAllowed") is False:
        return True
    for key, item in value.items():
        name = key.lower() if isinstance(key, str) else ""
        if (name.startswith("pending") or name.endswith(("pending", "unconfirmed")) or
                name in ("unknownreceipt", "unknownreceipts", "unknownactions", "reservations", "custody")) and item:
            return True
        if name in ("phase", "status", "outcome") and isinstance(item, str) and (
                re.search(r"unknown|unconfirmed|(?:^|_)(?:pending|issued|reserved|offered|entered|attempted|waiting)(?:_|$)", item, re.I)
                or item.upper() in ("REQUESTED", "INVITED", "OPENING", "OPEN", "ACCEPT_SENT", "CANCEL_SENT")):
            return True
        if _read_custody(item): return True
    return False


def _decode(raw):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError("duplicate JSON key")
            result[key] = value
        return result
    return json.loads(raw, object_pairs_hook=unique,
        parse_constant=lambda _: (_ for _ in ()).throw(ValueError("nonfinite JSON")))


def _packed(value, limit):
    try:
        raw = json.dumps(value, ensure_ascii=False, allow_nan=False, separators=(",", ":")).encode()
    except (TypeError, ValueError, UnicodeError, RecursionError):
        raise ModelError("invalid_json_value") from None
    if len(raw) > limit:
        raise ModelError("json_value_bound")
    return raw


def _schema(value, schema, depth=0):
    """Validate the bounded JSON Schema subset used by the supplied reply/tools."""
    if depth > 32 or not isinstance(schema, dict):
        raise ModelError("unsupported_json_schema")
    supported = {"type", "properties", "required", "additionalProperties", "items", "enum", "const",
        "minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems", "pattern",
        "anyOf", "oneOf", "allOf", "description", "title", "default", "examples", "$schema"}
    if set(schema) - supported:
        raise ModelError("unsupported_json_schema")
    for key in ("anyOf", "oneOf", "allOf"):
        if key not in schema:
            continue
        matches = 0
        for option in schema[key]:
            try:
                _schema(value, option, depth+1)
                matches += 1
            except ModelError as error:
                if error.reason == "unsupported_json_schema":
                    raise
        if (key == "anyOf" and not matches or key == "oneOf" and matches != 1
                or key == "allOf" and matches != len(schema[key])):
            raise ModelError("json_schema_mismatch")
    kind = schema.get("type")
    types = kind if isinstance(kind, list) else [kind] if kind else []
    checks = {"object": isinstance(value, dict), "array": isinstance(value, list), "string": isinstance(value, str),
        "integer": type(value) is int, "number": type(value) is int or type(value) is float and math.isfinite(value),
        "boolean": type(value) is bool, "null": value is None}
    if types and (any(t not in checks for t in types) or not any(checks[t] for t in types)):
        raise ModelError("json_schema_mismatch")
    if "enum" in schema and not any(type(value) is type(v) and value == v for v in schema["enum"]):
        raise ModelError("json_schema_mismatch")
    if "const" in schema and (type(value) is not type(schema["const"]) or value != schema["const"]):
        raise ModelError("json_schema_mismatch")
    if isinstance(value, dict):
        properties = schema.get("properties", {})
        if not set(schema.get("required", [])) <= set(value):
            raise ModelError("json_schema_mismatch")
        extra = schema.get("additionalProperties", True)
        for key, item in value.items():
            child = properties.get(key, extra)
            if child is False:
                raise ModelError("json_schema_mismatch")
            if isinstance(child, dict):
                _schema(item, child, depth+1)
    if isinstance(value, list):
        if len(value) < schema.get("minItems", 0) or len(value) > schema.get("maxItems", math.inf):
            raise ModelError("json_schema_mismatch")
        if "items" in schema:
            for item in value:
                _schema(item, schema["items"], depth+1)
    if isinstance(value, str):
        if len(value) < schema.get("minLength", 0) or len(value) > schema.get("maxLength", math.inf):
            raise ModelError("json_schema_mismatch")
        if "pattern" in schema and re.search(schema["pattern"], value) is None:
            raise ModelError("json_schema_mismatch")
    if type(value) in (int, float) and not schema.get("minimum", -math.inf) <= value <= schema.get("maximum", math.inf):
        raise ModelError("json_schema_mismatch")


class DirectModel:
    def __init__(self, provider, model, base_url, api_key_env=None, timeout=None, *,
                 max_tool_rounds=None, max_request_bytes=8*1024*1024, max_response_bytes=1024*1024,
                 max_output_tokens=8192):
        if provider not in ("local", "openai", "claude", "gemini"):
            raise ModelError("unsupported_provider")
        if not isinstance(model, str) or re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}", model) is None:
            raise ModelError("explicit_model_required")
        if not isinstance(base_url, str) or not base_url or len(base_url) > 2048 or any(c.isspace() or ord(c)<32 for c in base_url):
            raise ModelError("explicit_endpoint_required")
        try:
            parsed = urllib.parse.urlsplit(base_url)
            port = parsed.port
        except ValueError:
            raise ModelError("invalid_endpoint") from None
        local = parsed.hostname in ("127.0.0.1", "localhost", "::1")
        if (parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.username is not None
                or parsed.password is not None or parsed.query or parsed.fragment
                or port is not None and not 0 < port < 65536 or parsed.scheme == "http" and not local
                or provider == "local" and not local):
            raise ModelError("invalid_endpoint")
        if api_key_env is not None and (not isinstance(api_key_env,str) or re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]{0,127}",api_key_env) is None):
            raise ModelError("api_key_environment_name_required")
        if provider != "local" and api_key_env is None:
            raise ModelError("api_key_environment_name_required")
        if timeout is not None and (type(timeout) not in (int,float) or not math.isfinite(timeout) or timeout <= 0):
            raise ModelError("invalid_timeout")
        if (max_tool_rounds is not None and (type(max_tool_rounds) is not int or max_tool_rounds <= 0)
                or any(type(v) is not int or v <= 0 for v in (max_request_bytes,max_response_bytes,max_output_tokens))):
            raise ModelError("positive_limits_required")
        self.provider, self.model, self.base_url, self.api_key_env = provider, model, base_url.rstrip("/"), api_key_env
        self.timeout, self.max_tool_rounds = timeout, max_tool_rounds if max_tool_rounds is not None else 16
        self.natural_tool_rounds, self._conversations = max_tool_rounds, {}
        self.max_request_bytes, self.max_response_bytes = max_request_bytes, max_response_bytes
        self.max_output_tokens, self.last_receipt = max_output_tokens, None

    def _check(self, cancelled, deadline):
        if cancelled is not None and cancelled():
            raise Cancelled("cancelled")
        if deadline is not None and time.monotonic() >= deadline:
            raise ModelError("timeout")

    def _http(self, payload, cancelled, deadline, progress=None):
        self._check(cancelled, deadline)
        headers = {"Content-Type": "application/json", "Accept": "application/json"}
        if self.api_key_env:
            key = os.environ.get(self.api_key_env)
            if not key or any(ord(c)<33 or ord(c)>126 for c in key):
                raise ModelError("provider_key_unavailable")
            headers["x-goog-api-key" if self.provider == "gemini" else "Authorization"] = key if self.provider == "gemini" else "Bearer " + key
        if self.provider == "claude":
            headers["anthropic-version"] = ANTHROPIC_VERSION
        suffix = "/messages" if self.provider == "claude" else (
            "/models/" + urllib.parse.quote(self.model, safe="") + ":generateContent" if self.provider == "gemini" else "/chat/completions")
        target = urllib.parse.urlsplit(self.base_url + suffix)
        body = _packed(payload, self.max_request_bytes)
        index = self.last_receipt["httpRequests"] + 1
        self.last_receipt.update(lastRequestBytes=len(body), requestIndex=index,
            requestPreparedAtUnixMs=int(time.time()*1000), requestPhase="prepared")
        if index == 1:
            self.last_receipt["initialRequestBytes"] = len(body)
        if progress is not None:
            progress(copy.deepcopy(self.last_receipt))
        self._check(cancelled, deadline)
        connection_type = http.client.HTTPSConnection if target.scheme == "https" else http.client.HTTPConnection
        remaining = None if deadline is None else max(.001, deadline-time.monotonic())
        connection = connection_type(target.hostname, target.port, timeout=remaining)
        result, aborted, holder = queue.Queue(maxsize=1), threading.Event(), {}
        def worker():
            try:
                connection.connect()
                holder["socket"] = connection.sock
                if aborted.is_set():
                    return
                connection.request("POST", target.path or "/", body=body, headers=headers)
                response = connection.getresponse()
                raw = response.read(self.max_response_bytes+1)
                result.put((response.status, raw, None))
            except Exception as error:
                result.put((None, None, type(error).__name__))
            finally:
                connection.close()
        self.last_receipt["httpRequests"] += 1
        threading.Thread(target=worker, name="direct-model-http", daemon=True).start()
        try:
            while True:
                self._check(cancelled, deadline)
                try:
                    status, raw, error = result.get(timeout=.05)
                    break
                except queue.Empty:
                    continue
            self._check(cancelled, deadline)
        except BaseException:
            aborted.set()
            sock = holder.get("socket")
            if sock is not None:
                try: sock.shutdown(socket.SHUT_RDWR)
                except OSError: pass
            connection.close()
            raise
        if error:
            self.last_receipt["transportErrorType"] = error
            raise ModelError("provider_transport_unconfirmed")
        self.last_receipt["httpStatus"] = status
        if len(raw) > self.max_response_bytes:
            raise ModelError("provider_response_bound")
        try:
            value = _decode(raw)
        except (ValueError,UnicodeError,RecursionError):
            if not 200 <= status < 300:
                self.last_receipt["providerError"] = dict(reason="non_json_error_response", responseBytes=len(raw))
                raise ModelError("provider_http_error") from None
            raise ModelError("provider_invalid_json") from None
        if not 200 <= status < 300:
            self.last_receipt["providerError"] = _provider_error(value)
            raise ModelError("provider_http_error")
        if not isinstance(value,dict):
            raise ModelError("provider_invalid_response")
        return value

    def _payload(self, system, conversation, tools, *, natural=False):
        if self.provider in ("local", "openai"):
            result = dict(model=self.model, messages=[dict(role="system",content=system),*conversation],
                tools=[dict(type="function",function=t) for t in tools], tool_choice="auto" if natural else "required", stream=False)
            if not natural: result["max_tokens"] = self.max_output_tokens
            return result
        if self.provider == "claude":
            return dict(model=self.model, system=system, messages=conversation,
                tools=[dict(name=t["name"],description=t.get("description",""),input_schema=t["parameters"]) for t in tools],
                tool_choice=dict(type="auto" if natural else "any"), max_tokens=self.max_output_tokens)
        return dict(systemInstruction=dict(parts=[dict(text=system)]), contents=conversation,
            tools=[dict(functionDeclarations=[dict(name=t["name"],description=t.get("description",""),parametersJsonSchema=t["parameters"]) for t in tools])],
            toolConfig=dict(functionCallingConfig=dict(mode="AUTO" if natural else "ANY")),
            **({} if natural else dict(generationConfig=dict(maxOutputTokens=self.max_output_tokens))))

    def _parse(self, value, round_index):
        usage = value.get("usageMetadata" if self.provider == "gemini" else "usage", {})
        allowed_usage = {"input_tokens","output_tokens","prompt_tokens","completion_tokens","total_tokens",
                         "promptTokenCount","candidatesTokenCount","totalTokenCount","thoughtsTokenCount"}
        if isinstance(usage,dict):
            for key, count in usage.items():
                if key in allowed_usage and type(count) is int and 0 <= count < 2**53:
                    self.last_receipt["usage"][key] = self.last_receipt["usage"].get(key,0) + count
        reported = value.get("modelVersion" if self.provider == "gemini" else "model")
        if isinstance(reported,str):self.last_receipt["providerReportedModel"] = _safe(reported,128)
        if self.provider in ("local", "openai"):
            choices = value.get("choices")
            if not isinstance(choices,list) or len(choices)!=1 or not isinstance(choices[0],dict) or choices[0].get("finish_reason") not in ("tool_calls","stop"):
                raise ModelError("provider_incomplete_response")
            native = choices[0].get("message")
            if not isinstance(native,dict) or native.get("role")!="assistant" or native.get("refusal"):
                raise ModelError("provider_invalid_response")
            raw_calls = native.get("tool_calls", [])
            if not isinstance(raw_calls,list):raise ModelError("invalid_tool_call")
            calls = []
            for row in raw_calls:
                if not isinstance(row,dict):raise ModelError("invalid_tool_call")
                function = row.get("function",{})
                if row.get("type")!="function" or not isinstance(function.get("arguments"),str):
                    raise ModelError("invalid_tool_call")
                try: arguments = _decode(function["arguments"])
                except (ValueError,UnicodeError,RecursionError): raise ModelError("invalid_tool_arguments") from None
                calls.append(dict(id=row.get("id"),name=function.get("name"),arguments=arguments))
            return copy.deepcopy(native), calls
        if self.provider == "claude":
            native = value.get("content")
            if value.get("stop_reason") not in ("tool_use", "end_turn") or not isinstance(native,list):
                raise ModelError("provider_incomplete_response")
            return dict(role="assistant",content=copy.deepcopy(native)), [dict(id=r.get("id"),name=r.get("name"),arguments=r.get("input"))
                for r in native if isinstance(r,dict) and r.get("type")=="tool_use"]
        candidates = value.get("candidates")
        if not isinstance(candidates,list) or len(candidates)!=1 or not isinstance(candidates[0],dict) or candidates[0].get("finishReason")!="STOP":
            raise ModelError("provider_incomplete_response")
        native = candidates[0].get("content")
        if not isinstance(native,dict) or native.get("role")!="model" or not isinstance(native.get("parts"),list):
            raise ModelError("provider_invalid_response")
        calls = []
        for index, part in enumerate(native["parts"]):
            if not isinstance(part,dict) or "functionCall" not in part:continue
            call = part["functionCall"]
            if not isinstance(call,dict):raise ModelError("invalid_tool_call")
            calls.append(dict(id=call.get("id") or f"gemini-{round_index}-{index}", nativeId=call.get("id"),
                name=call.get("name"),arguments=call.get("args",{})))
        # Preserve opaque thoughtSignature and all original model parts verbatim.
        return copy.deepcopy(native), calls

    def _append_results(self, conversation, native, results, *, append_native=True):
        if append_native: conversation.append(native)
        if self.provider in ("local", "openai"):
            conversation.extend(dict(role="tool",tool_call_id=c["id"],content=_packed(v,self.max_request_bytes).decode()) for c,v in results)
        elif self.provider == "claude":
            conversation.append(dict(role="user",content=[dict(type="tool_result",tool_use_id=c["id"],content=_packed(v,self.max_request_bytes).decode()) for c,v in results]))
        else:
            parts = []
            for call, value in results:
                reply = dict(name=call["name"], response=value if isinstance(value,dict) else dict(result=value))
                if call.get("nativeId"):reply["id"] = call["nativeId"]
                parts.append(dict(functionResponse=reply))
            conversation.append(dict(role="user",parts=parts))

    def _text(self, native):
        if self.provider in ("local", "openai"):
            value = native.get("content")
            return value if isinstance(value, str) else ""
        parts = native.get("content", []) if self.provider == "claude" else native.get("parts", [])
        return "".join(part["text"] for part in parts
                       if isinstance(part, dict) and isinstance(part.get("text"), str)
                       and (part.get("type")=="text" if self.provider=="claude" else part.get("thought") is not True))

    def _active_conversation(self, conversation, read_only_tools, unknown, pending):
        """Supersede only repeated, completed pure-read bodies in the API copy.

        The original calls/results, human messages and action custody remain in
        the durable transcript. Each marker addresses that original message;
        the newest recorded result for the exact tool/arguments stays verbatim.
        No observation is promoted to current merely because it is retained.
        """
        active = copy.deepcopy(conversation)
        if pending: return active
        pairs, calls = [], {}
        for index, message in enumerate(conversation):
            if not isinstance(message, dict): continue
            role = message.get("role")
            if self.provider in ("local", "openai"):
                if role == "assistant":
                    calls = {}
                    rows = message.get("tool_calls", [])
                    for row in rows if isinstance(rows, list) else []:
                        try:
                            function = row["function"]
                            calls[row["id"]] = (function["name"], _decode(function["arguments"]))
                        except (KeyError, TypeError, ValueError, UnicodeError): continue
                elif role == "tool" and message.get("tool_call_id") in calls:
                    pairs.append((index, ("content",), calls.pop(message["tool_call_id"]), message["tool_call_id"]))
            elif self.provider == "claude":
                blocks = message.get("content")
                if not isinstance(blocks, list): continue
                if role == "assistant":
                    calls = {row.get("id"): (row.get("name"), row.get("input")) for row in blocks
                             if isinstance(row, dict) and row.get("type") == "tool_use"}
                elif role == "user":
                    for part, row in enumerate(blocks):
                        if isinstance(row, dict) and row.get("type") == "tool_result" and row.get("tool_use_id") in calls:
                            pairs.append((index, ("content", part, "content"), calls.pop(row["tool_use_id"]), row["tool_use_id"]))
            else:
                parts = message.get("parts", [])
                if not isinstance(parts, list): continue
                if role == "model":
                    calls = []
                    for row in parts:
                        if isinstance(row, dict) and isinstance(row.get("functionCall"), dict):
                            call = row["functionCall"]
                            calls.append((call.get("id"), call.get("name"), call.get("args", {})))
                elif role == "user" and isinstance(calls, list):
                    for part, row in enumerate(parts):
                        response = row.get("functionResponse") if isinstance(row, dict) else None
                        if not isinstance(response, dict): continue
                        matches = [n for n, call in enumerate(calls) if call[1] == response.get("name")
                                   and call[0] == response.get("id")]
                        if not matches: continue
                        native_id, name, args = calls.pop(matches[0])
                        pairs.append((index, ("parts", part, "functionResponse", "response"), (name, args), native_id))
        latest, eligible = {}, []
        for index, path, (name, args), call_id in pairs:
            if not isinstance(name, str) or name not in read_only_tools or not isinstance(args, dict): continue
            fingerprint = hashlib.sha256(_packed([name, args], self.max_request_bytes)).hexdigest()
            if fingerprint in unknown: continue
            original = conversation[index]
            for segment in path: original = original[segment]
            try: value = _decode(original) if isinstance(original, str) else original
            except (ValueError, UnicodeError, RecursionError): continue
            if _read_custody(value): continue
            # Object key order does not create a different observation query.
            key = json.dumps([name, args], ensure_ascii=False, sort_keys=True, separators=(",", ":"))
            row = (index, path, name, call_id, original)
            eligible.append((key, row)); latest[key] = row
        superseded = 0
        for key, row in eligible:
            if row is latest[key]: continue
            index, path, name, call_id, original = row
            raw = original.encode() if isinstance(original, str) else _packed(original, self.max_request_bytes)
            marker = dict(historicalRead=True, tool=name,
                original=dict(transcriptMessage=index, toolCallId=call_id, resultPath=list(path)),
                resultSHA256=hashlib.sha256(raw).hexdigest(), resultBytes=len(raw),
                supersededBy=dict(transcriptMessage=latest[key][0], toolCallId=latest[key][3]),
                note="Full result remains in the durable transcript. The newer recorded result is not proof of current state; refresh this read for current facts.")
            replacement = _packed(marker, self.max_request_bytes)
            if len(replacement) >= len(raw): continue
            target = active[index]
            for segment in path[:-1]: target = target[segment]
            target[path[-1]] = replacement.decode() if isinstance(original, str) else marker
            superseded += 1
        self.last_receipt["contextProjection"] = dict(supersededReadResults=superseded,
            canonicalConversationBytes=len(_packed(conversation, self.max_request_bytes*8)),
            activeConversationBytes=len(_packed(active, self.max_request_bytes*8)))
        return active

    def _generate_natural(self, system, messages, reply_schema, tools, dispatch, *, cancelled,
                          request_id, progress, transcript_path, conversation_key, assistant_text, read_only_tools):
        identifier = request_id or uuid.uuid4().hex
        self.last_receipt = dict(requestId=identifier, execution="direct_api", provider=self.provider,
            requestedModel=self.model, baseURL=self.base_url, outcome="in_progress", httpRequests=0,
            toolCalls=0, tools=[], omittedTools=0, usage={}, naturalConversation=True)
        deadline = None if self.timeout is None else time.monotonic()+self.timeout
        try:
            if (not isinstance(system, str) or not isinstance(messages, list) or not isinstance(tools, list)
                    or len(tools)>64 or not callable(dispatch)):
                raise ModelError("invalid_generation_input")
            if not isinstance(identifier, str) or re.fullmatch(r"[A-Za-z0-9_.:-]{1,128}", identifier) is None:
                raise ModelError("invalid_request_id")
            if conversation_key is None and transcript_path is None: conversation_key = "in-memory"
            if not isinstance(conversation_key, str) or not 0 < len(conversation_key) <= 4096:
                raise ModelError("conversation_identity_required")
            functions = {}
            if read_only_tools is None: read_only_tools = frozenset()
            if not isinstance(read_only_tools, (list, tuple, set, frozenset)) or not all(isinstance(name, str) for name in read_only_tools):
                raise ModelError("invalid_read_only_tool_declaration")
            read_only_tools = frozenset(read_only_tools)
            for row in copy.deepcopy(tools):
                function = row.get("function") if isinstance(row, dict) and row.get("type")=="function" else None
                if (not isinstance(function, dict) or not isinstance(function.get("name"), str)
                        or re.fullmatch(r"[A-Za-z_][A-Za-z0-9_-]{0,63}", function["name"]) is None
                        or function["name"] in functions or function["name"]==FINAL_REPLY
                        or not isinstance(function.get("parameters"), dict)):
                    raise ModelError("invalid_tool_schema")
                functions[function["name"]] = {k:function[k] for k in ("name", "description", "parameters") if k in function}
            # Optional compatibility for existing explicit control actions;
            # ordinary conversation is normal assistant text, never forced JSON.
            if reply_schema is not None:
                functions[FINAL_REPLY] = dict(name=FINAL_REPLY, description=
                    "Optional structured game control reply. Ordinary conversation needs no tool.", parameters=copy.deepcopy(reply_schema))
            incoming = []
            for message in copy.deepcopy(messages):
                if (not isinstance(message, dict) or message.get("role") not in ("user", "assistant")
                        or not isinstance(message.get("content"), str)):
                    raise ModelError("invalid_message")
                incoming.append(dict(role="model" if message["role"]=="assistant" else "user", parts=[dict(text=message["content"])])
                    if self.provider=="gemini" else dict(role=message["role"], content=message["content"]))
            binding = json.dumps([self.provider, self.model, self.base_url, conversation_key], separators=(",", ":"))
            with _Transcript(transcript_path, binding, self.max_request_bytes*8, self._conversations) as journal:
                state = journal.state
                conversation = state["messages"]
                # Recover an interrupted tool exchange as factual results, not
                # commands waiting to be retried by this or the next listener.
                if state["pending"]:
                    pending = state["pending"]
                    recovered = []
                    for row in pending["calls"]:
                        call = row["call"]
                        result = row.get("result")
                        if result is None:
                            if row.get("readOnly", False):
                                result = dict(ok=False, readOnly=True, refreshAllowed=True, reason="read_interrupted")
                            else:
                                result = dict(ok=False, outcomeUnknown=row["phase"]=="entered", replayAllowed=False,
                                              reason="interrupted_tool_exchange" if row["phase"]=="entered" else "not_dispatched")
                        if isinstance(result, dict) and result.get("outcomeUnknown") is True and not row.get("readOnly", False):
                            state["unknown"][call["fingerprint"]] = copy.deepcopy(result)
                        recovered.append((call, result))
                    self._append_results(conversation, None, recovered, append_native=False)
                    state["pending"] = None
                    journal.save()
                digest = hashlib.sha256(_packed(incoming, self.max_request_bytes)).hexdigest()
                prior = state["requests"].get(identifier)
                if prior is not None:
                    if prior["inputSHA256"] != digest: raise ModelError("conversation_request_identity_changed")
                    raise ModelError("conversation_request_already_entered")
                state["requests"][identifier] = dict(inputSHA256=digest)
                conversation.extend(incoming)
                state["seen"][identifier] = {}
                seen = state["seen"][identifier]
                journal.save()
                index = 0
                while self.natural_tool_rounds is None or index < self.natural_tool_rounds:
                    self._check(cancelled, deadline)
                    active = self._active_conversation(conversation, read_only_tools, state["unknown"], state["pending"])
                    value = self._http(self._payload(system, active, list(functions.values()), natural=True),
                                       cancelled, deadline, progress)
                    native, calls = self._parse(value, index)
                    self._check(cancelled, deadline)
                    if not calls:
                        text = self._text(native)
                        if not text: raise ModelError("provider_empty_reply")
                        conversation.append(native)
                        journal.save()
                        self.last_receipt["outcome"] = "completed"
                        return dict(text=text, emote="NONE", action="NONE")
                    if len(calls)>16: raise ModelError("unbounded_tool_calls")
                    identifiers = set()
                    for call in calls:
                        if (not isinstance(call["id"], str) or not 1 <= len(call["id"]) <= 256 or call["id"] in identifiers
                                or call["name"] not in functions or not isinstance(call["arguments"], dict)):
                            raise ModelError("invalid_tool_call")
                        identifiers.add(call["id"])
                        _schema(call["arguments"], functions[call["name"]]["parameters"])
                        call["fingerprint"] = hashlib.sha256(_packed([call["name"], call["arguments"]], self.max_request_bytes)).hexdigest()
                        if call["id"] in seen and seen[call["id"]][0] != call["fingerprint"]:
                            raise ModelError("tool_call_identity_changed")
                    if any(call["name"]==FINAL_REPLY for call in calls):
                        if len(calls)!=1: raise ModelError("mixed_final_reply")
                        conversation.append(native)
                        self._append_results(conversation, None, [(calls[0], dict(replyReturned=True))], append_native=False)
                        journal.save()
                        self.last_receipt["outcome"] = "completed"
                        return copy.deepcopy(calls[0]["arguments"])
                    conversation.append(native)
                    state["pending"] = dict(calls=[dict(call=call, phase="not_entered", readOnly=call["name"] in read_only_tools) for call in calls])
                    journal.save()
                    text = self._text(native)
                    if text and assistant_text is not None:
                        self._check(cancelled, deadline)
                        assistant_text(text)
                    results = []
                    for row in state["pending"]["calls"]:
                        call = row["call"]
                        self._check(cancelled, deadline)
                        fingerprint = call["fingerprint"]
                        if call["id"] in seen:
                            returned = copy.deepcopy(seen[call["id"]][1])
                        elif fingerprint in state["unknown"]:
                            returned = copy.deepcopy(state["unknown"][fingerprint])
                        else:
                            row["phase"] = "entered"
                            journal.save()  # Written before the caller can enter a mutation.
                            try:
                                self._check(cancelled, deadline)
                            except Cancelled:
                                row["phase"], row["result"] = "returned", dict(ok=False, reason="not_dispatched", outcomeUnknown=False)
                                journal.save()
                                raise
                            self.last_receipt["toolCalls"] += 1
                            try:
                                returned = dispatch(call["name"], copy.deepcopy(call["arguments"]))
                                _packed(returned, self.max_request_bytes)
                            except BaseException as error:
                                if row["readOnly"]:
                                    returned = dict(ok=False, readOnly=True, refreshAllowed=True,
                                        reason="read_failed", errorType=type(error).__name__)
                                else:
                                    row["result"] = dict(ok=False, outcomeUnknown=True, replayAllowed=False,
                                        reason="tool_outcome_unknown", receipt=copy.deepcopy(getattr(error, "receipt", {})))
                                    state["unknown"][fingerprint] = copy.deepcopy(row["result"])
                                    journal.save()
                                    raise ModelError("tool_outcome_unknown") from error
                        row["phase"], row["result"] = "returned", copy.deepcopy(returned)
                        seen[call["id"]] = [fingerprint, copy.deepcopy(returned)]
                        journal.save()
                        results.append((call, returned))
                        if isinstance(returned, dict) and returned.get("outcomeUnknown") is True:
                            raise ModelError("tool_outcome_unknown")
                        self._check(cancelled, deadline)
                    self._append_results(conversation, None, results, append_native=False)
                    state["pending"] = None
                    journal.save()
                    index += 1
                raise ModelError("tool_round_bound")
        except BaseException as error:
            reason = error.reason if isinstance(error, ModelError) else type(error).__name__
            self.last_receipt.update(outcome="cancelled" if isinstance(error, Cancelled) else "failed", reason=reason)
            if isinstance(error, Cancelled): raise Cancelled(reason, self.last_receipt) from None
            raise ModelError(reason, self.last_receipt) from error

    def generate(self, system, messages, reply_schema, tools, dispatch, *, cancelled=None, request_id=None, progress=None,
                 natural=False, transcript_path=None, conversation_key=None, assistant_text=None, read_only_tools=None):
        if natural:
            return self._generate_natural(system, messages, reply_schema, tools, dispatch, cancelled=cancelled,
                request_id=request_id, progress=progress, transcript_path=transcript_path, conversation_key=conversation_key,
                assistant_text=assistant_text, read_only_tools=read_only_tools)
        identifier = request_id or uuid.uuid4().hex
        self.last_receipt = dict(requestId=identifier,execution="direct_api",provider=self.provider,
            requestedModel=self.model,baseURL=self.base_url,outcome="in_progress",httpRequests=0,
            toolCalls=0,tools=[],omittedTools=0,usage={})
        deadline = None if self.timeout is None else time.monotonic()+self.timeout
        try:
            if not isinstance(system,str) or not isinstance(messages,list) or not isinstance(tools,list) or len(tools)>64 or not callable(dispatch):
                raise ModelError("invalid_generation_input")
            if not isinstance(identifier,str) or re.fullmatch(r"[A-Za-z0-9_.:-]{1,128}",identifier) is None:
                raise ModelError("invalid_request_id")
            functions = {}
            for tool in copy.deepcopy(tools):
                function = tool.get("function") if isinstance(tool,dict) and tool.get("type")=="function" else None
                if (not isinstance(function,dict) or not isinstance(function.get("name"),str)
                        or re.fullmatch(r"[A-Za-z_][A-Za-z0-9_-]{0,63}",function["name"]) is None
                        or function["name"] in functions or function["name"]==FINAL_REPLY
                        or not isinstance(function.get("parameters"),dict)):
                    raise ModelError("invalid_tool_schema")
                functions[function["name"]] = {k:function[k] for k in ("name","description","parameters") if k in function}
            functions[FINAL_REPLY] = dict(name=FINAL_REPLY,description="Finish with the caller's exact reply JSON, after tool results. Call this alone.",parameters=copy.deepcopy(reply_schema))
            system += "\nFinish using final_reply alone, after observing any tool results. Never mix final_reply with another call."
            conversation = []
            for message in copy.deepcopy(messages):
                if not isinstance(message,dict) or message.get("role") not in ("user","assistant") or not isinstance(message.get("content"),str):
                    raise ModelError("invalid_message")
                conversation.append(dict(role="model" if message["role"]=="assistant" else "user",parts=[dict(text=message["content"])])
                    if self.provider=="gemini" else dict(role=message["role"],content=message["content"]))
            seen = {}
            for index in range(self.max_tool_rounds):
                self._check(cancelled, deadline)
                value = self._http(self._payload(system,conversation,list(functions.values())),cancelled,deadline,progress)
                native, calls = self._parse(value,index)
                if not calls or len(calls)>16:
                    raise ModelError("missing_or_unbounded_tool_calls")
                identifiers = set()
                for call in calls:
                    if (not isinstance(call["id"],str) or not 1<=len(call["id"])<=256 or call["id"] in identifiers
                            or call["name"] not in functions or not isinstance(call["arguments"],dict)):
                        raise ModelError("invalid_tool_call")
                    identifiers.add(call["id"])
                    _schema(call["arguments"],functions[call["name"]]["parameters"])
                    call["fingerprint"] = hashlib.sha256(_packed([call["name"],call["arguments"]],self.max_request_bytes)).hexdigest()
                    if call["id"] in seen and seen[call["id"]][0] != call["fingerprint"]:
                        raise ModelError("tool_call_identity_changed")
                self._check(cancelled, deadline)
                if any(c["name"]==FINAL_REPLY for c in calls):
                    if len(calls)!=1:raise ModelError("mixed_final_reply")
                    self.last_receipt["outcome"] = "completed"
                    return copy.deepcopy(calls[0]["arguments"])
                results = []
                for call in calls:
                    self._check(cancelled, deadline)
                    fingerprint = call["fingerprint"]
                    if call["id"] in seen:
                        previous, returned = seen[call["id"]]
                        if previous!=fingerprint:raise ModelError("tool_call_identity_changed")
                        results.append((call,copy.deepcopy(returned)))
                        continue
                    row = dict(id=_safe(call["id"],128),name=call["name"],outcome="unknown")
                    self.last_receipt["toolCalls"] += 1
                    self.last_receipt["tools"].append(row)
                    if len(self.last_receipt["tools"])>16:
                        self.last_receipt["tools"].pop(0);self.last_receipt["omittedTools"]+=1
                    try:
                        returned = dispatch(call["name"],copy.deepcopy(call["arguments"]))
                    except BaseException as error:
                        row["errorType"] = type(error).__name__
                        if isinstance(error,ToolOutcomeUnknown) or getattr(error,"outcome_unknown",False):
                            raise ModelError("tool_outcome_unknown") from error
                        raise ModelError("tool_dispatch_failed") from error
                    _packed(returned,self.max_request_bytes)
                    if isinstance(returned,dict) and returned.get("outcomeUnknown") is True:
                        raise ModelError("tool_outcome_unknown")
                    row["outcome"] = "returned"
                    seen[call["id"]] = (fingerprint,copy.deepcopy(returned))
                    results.append((call,returned))
                    self._check(cancelled, deadline)
                self._append_results(conversation,native,results)
            raise ModelError("tool_round_bound")
        except BaseException as error:
            reason = error.reason if isinstance(error,ModelError) else type(error).__name__
            self.last_receipt.update(outcome="cancelled" if isinstance(error,Cancelled) else "failed",reason=reason)
            if isinstance(error,Cancelled):raise Cancelled(reason,self.last_receipt) from None
            raise ModelError(reason,self.last_receipt) from error
