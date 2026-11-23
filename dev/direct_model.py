"""Direct provider HTTP transports and one bounded caller-owned tool exchange.

The caller supplies the persona, reply schema and actor-bound dispatcher. This
module owns no game connection, shell, task scheduler or persistent action state.
"""
import copy
import hashlib
import http.client
import json
import math
import os
import queue
import re
import socket
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
                 max_tool_rounds=16, max_request_bytes=8*1024*1024, max_response_bytes=1024*1024,
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
        if any(type(v) is not int or v <= 0 for v in (max_tool_rounds,max_request_bytes,max_response_bytes,max_output_tokens)):
            raise ModelError("positive_limits_required")
        self.provider, self.model, self.base_url, self.api_key_env = provider, model, base_url.rstrip("/"), api_key_env
        self.timeout, self.max_tool_rounds = timeout, max_tool_rounds
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
            raise ModelError("provider_invalid_json") from None
        if not 200 <= status < 300:
            detail = value.get("error", value) if isinstance(value,dict) else {}
            self.last_receipt["providerError"] = {k:_safe(detail[k]) if isinstance(detail[k],str) else detail[k]
                for k in ("type","code","message") if isinstance(detail,dict)
                and (isinstance(detail.get(k),str) or type(detail.get(k)) is int)}
            raise ModelError("provider_http_error")
        if not isinstance(value,dict):
            raise ModelError("provider_invalid_response")
        return value

    def _payload(self, system, conversation, tools):
        if self.provider in ("local", "openai"):
            return dict(model=self.model, messages=[dict(role="system",content=system),*conversation],
                tools=[dict(type="function",function=t) for t in tools], tool_choice="required",
                max_tokens=self.max_output_tokens, stream=False)
        if self.provider == "claude":
            return dict(model=self.model, system=system, messages=conversation,
                tools=[dict(name=t["name"],description=t.get("description",""),input_schema=t["parameters"]) for t in tools],
                tool_choice=dict(type="any"), max_tokens=self.max_output_tokens)
        return dict(systemInstruction=dict(parts=[dict(text=system)]), contents=conversation,
            tools=[dict(functionDeclarations=[dict(name=t["name"],description=t.get("description",""),parametersJsonSchema=t["parameters"]) for t in tools])],
            toolConfig=dict(functionCallingConfig=dict(mode="ANY")), generationConfig=dict(maxOutputTokens=self.max_output_tokens))

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
            if value.get("stop_reason")!="tool_use" or not isinstance(native,list):
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

    def _append_results(self, conversation, native, results):
        conversation.append(native)
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

    def generate(self, system, messages, reply_schema, tools, dispatch, *, cancelled=None, request_id=None, progress=None):
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
