#!/usr/bin/env python3
"""
Anthropic Messages API → OpenAI Chat Completions API proxy for cloud.ru.
Listens on a local port, translates Claude Code's Anthropic-format requests
to OpenAI format, forwards to cloud.ru, and translates responses back.

Env vars:
  CLOUDRU_URL           — cloud.ru base URL (default: https://foundation-models.api.cloud.ru/v1)
  CLOUDRU_API_KEY       — cloud.ru API key (required)
  CLOUDRU_MODEL         — model name (default: zai-org/GLM-5.2)
  PROXY_PORT            — listen port (default: 3201)
  CLOUDRU_PROXY_DEBUG=1 — log request/response bodies to stderr
  CLOUDRU_PROXY_RAW_LOG — path to append raw Cloud.ru SSE chunks for offline analysis
"""

import json, os, sys, uuid, time
from http.server import HTTPServer, BaseHTTPRequestHandler
from urllib.request import Request, urlopen
from urllib.error import HTTPError, URLError

# ── Config ──────────────────────────────────────────────────────────────────
CLOUDRU_URL      = os.environ.get("CLOUDRU_URL",      "https://foundation-models.api.cloud.ru/v1")
CLOUDRU_API_KEY  = os.environ.get("CLOUDRU_API_KEY",  "")
CLOUDRU_MODEL    = os.environ.get("CLOUDRU_MODEL",    "zai-org/GLM-5.2")
LISTEN_PORT      = int(os.environ.get("PROXY_PORT",    3201))
DEBUG            = os.environ.get("CLOUDRU_PROXY_DEBUG") == "1"
RAW_LOG_PATH     = os.environ.get("CLOUDRU_PROXY_RAW_LOG", "")

# ── Helpers ─────────────────────────────────────────────────────────────────

def log(fmt, *args):
    print(f"[cloudru-proxy] {fmt % args}", file=sys.stderr, flush=True)


def debug_log(fmt, *args):
    if DEBUG:
        log("DEBUG: " + fmt, *args)


def raw_log(fmt, *args):
    """Append a line to the raw-log file if configured."""
    if RAW_LOG_PATH:
        try:
            with open(RAW_LOG_PATH, "a") as f:
                f.write(fmt % args + "\n")
        except Exception:
            pass


def sse_event(event: str, data: dict) -> bytes:
    return f"event: {event}\ndata: {json.dumps(data, ensure_ascii=False)}\n\n".encode()


# ── Translation: Anthropic → OpenAI ─────────────────────────────────────────

def anthropic_to_openai(req: dict) -> dict:
    messages: list = []

    # System prompt — top-level field in Anthropic, role:system message in OpenAI
    system = req.get("system")
    if system:
        if isinstance(system, list):
            text = "\n".join(b["text"] for b in system if b.get("type") == "text")
        else:
            text = system
        if text.strip():
            messages.append({"role": "system", "content": text})

    # Messages
    for msg in req.get("messages", []):
        role = msg.get("role", "user")
        content = msg.get("content")

        if isinstance(content, str):
            messages.append(_translate_simple_msg(role, content))
            continue

        if not isinstance(content, list):
            messages.append(_translate_simple_msg(role, str(content)))
            continue

        texts: list[str] = []
        tool_calls: list[dict] = []

        for block in content:
            t = block.get("type")

            if t == "text":
                texts.append(block.get("text", ""))

            elif t == "tool_use":
                tool_calls.append({
                    "id": block.get("id", f"toolu_{uuid.uuid4().hex[:12]}"),
                    "type": "function",
                    "function": {
                        "name": block.get("name", ""),
                        "arguments": json.dumps(block.get("input", {}), ensure_ascii=False),
                    },
                })

            elif t == "tool_result":
                # Anthropic tool_result → OpenAI tool message
                tc = block.get("content", "")
                if isinstance(tc, list):
                    tc = "\n".join(b.get("text", "") for b in tc if b.get("type") == "text")
                messages.append({
                    "role": "tool",
                    "tool_call_id": block.get("tool_use_id", ""),
                    "content": tc,
                })

            elif t == "image":
                # Pass image blocks as data-uri content arrays (OpenAI vision format)
                src = block.get("source", {})
                messages.append({
                    "role": role,
                    "content": [{
                        "type": "image_url",
                        "image_url": {
                            "url": f"data:{src.get('media_type','image/png')};base64,{src.get('data','')}"
                        }
                    }]
                })

        # Assemble final message for this turn
        if tool_calls:
            messages.append({
                "role": "assistant",
                "content": "\n".join(texts) if texts else None,
                "tool_calls": tool_calls,
            })
        else:
            messages.append({"role": role, "content": "\n".join(texts) if texts else ""})

    oai: dict = {
        "model": CLOUDRU_MODEL,
        "messages": messages,
        "max_tokens": req.get("max_tokens", 4096),
    }

    for key in ("temperature", "top_p"):
        if key in req:
            oai[key] = req[key]
    if req.get("stop_sequences"):
        oai["stop"] = req["stop_sequences"]

    # Tools
    tools = req.get("tools")
    if tools:
        oai["tools"] = [
            {
                "type": "function",
                "function": {
                    "name": t["name"],
                    "description": t.get("description", ""),
                    "parameters": t.get("input_schema", {}),
                },
            }
            for t in tools
        ]

    # Tool choice — Anthropic {type: "auto"|"any"|"tool"} → OpenAI "auto"|"required"|"none"|{...}
    tool_choice = req.get("tool_choice")
    if tool_choice and tools:
        tc_type = tool_choice.get("type", "auto")
        if tc_type == "auto":
            oai["tool_choice"] = "auto"
        elif tc_type == "any":
            oai["tool_choice"] = "required"
        elif tc_type == "tool":
            oai["tool_choice"] = {
                "type": "function",
                "function": {"name": tool_choice.get("name", "")},
            }
        elif tc_type == "none":
            oai["tool_choice"] = "none"

    # Streaming
    if req.get("stream"):
        oai["stream"] = True
        oai["stream_options"] = {"include_usage": True}

    return oai


def _translate_simple_msg(role: str, content: str) -> dict:
    return {"role": role, "content": content}


# ── Translation: OpenAI → Anthropic ─────────────────────────────────────────

def openai_to_anthropic(oai_body: dict, orig_req: dict = None) -> dict:
    choice  = oai_body.get("choices", [{}])[0]
    message = choice.get("message", {})
    usage   = oai_body.get("usage", {})

    content: list[dict] = []

    # Text
    if message.get("content"):
        content.append({"type": "text", "text": message["content"]})

    # Tool calls
    for tc in message.get("tool_calls", []):
        fn = tc.get("function", {})
        raw = fn.get("arguments", "{}")
        # GLM-5.2 may return arguments as an already-parsed dict
        if isinstance(raw, dict):
            inp = raw
        elif isinstance(raw, str):
            try:
                inp = json.loads(raw)
            except json.JSONDecodeError:
                log("WARNING: unparseable tool-call arguments: %.200s", raw)
                inp = {}
        else:
            inp = {}
        content.append({
            "type": "tool_use",
            "id": tc.get("id", f"toolu_{uuid.uuid4().hex[:12]}"),
            "name": fn.get("name", ""),
            "input": inp,
        })

    # Stop reason
    finish = choice.get("finish_reason")
    stop_reason = "end_turn"
    if finish == "length":
        stop_reason = "max_tokens"
    elif finish == "tool_calls":
        stop_reason = "tool_use"

    return {
        "id": f"msg_{uuid.uuid4().hex[:24]}",
        "type": "message",
        "role": "assistant",
        "content": content,
        "model": oai_body.get("model", CLOUDRU_MODEL),
        "stop_reason": stop_reason,
        "stop_sequence": None,
        "usage": {
            "input_tokens":  usage.get("prompt_tokens", 0),
            "output_tokens": usage.get("completion_tokens", 0),
        },
    }


from urllib.parse import urlparse

# ── HTTP Handler ────────────────────────────────────────────────────────────

class ProxyHandler(BaseHTTPRequestHandler):

    def log_message(self, fmt, *args):
        log("[%s] %s", self.client_address[0], fmt % args)

    # ── routing ─────────────────────────────────────────────────────────

    @property
    def _clean_path(self):
        return urlparse(self.path).path

    def do_POST(self):
        p = self._clean_path
        if p == "/v1/messages":
            self._handle_messages()
        elif p == "/v1/messages/count_tokens":
            self._handle_count_tokens()
        else:
            self._not_found()

    def do_GET(self):
        p = self._clean_path
        if p == "/_proxy/status":
            self._status()
        elif p in ("/", ""):
            self._root()
        elif p.startswith("/v1/"):
            self._json_response(200, b"{}")
        else:
            self._not_found()

    def do_HEAD(self):
        """Claude Code probes the base URL with HEAD — must succeed."""
        p = self._clean_path
        if p in ("/", "") or p.startswith("/v1/"):
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", "2")
            self.end_headers()
        else:
            self.send_error(404)

    # ── POST /v1/messages ───────────────────────────────────────────────

    def _handle_messages(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length)
        try:
            req = json.loads(body)
        except json.JSONDecodeError:
            self._error(400, "Invalid JSON")
            return

        debug_log("Anthropic req: %s", json.dumps(req, ensure_ascii=False)[:3000])

        stream = req.get("stream", False)
        oai_req = anthropic_to_openai(req)

        debug_log("OpenAI   req: %s", json.dumps(oai_req, ensure_ascii=False)[:3000])

        try:
            oai_resp = self._call_cloudru(oai_req, stream=stream)
        except HTTPError as e:
            log("cloud.ru HTTP error: %s %s", e.code, e.reason)
            err_body = e.read().decode(errors="replace")
            log("cloud.ru body: %s", err_body[:500])
            self._error(502, f"Upstream error: {e.code} {e.reason}")
            return
        except URLError as e:
            log("cloud.ru connection error: %s", e.reason)
            self._error(502, f"Connection error: {e.reason}")
            return

        if stream:
            self._stream_response(oai_resp)
        else:
            self._non_stream_response(oai_resp, req)

    # ── POST /v1/messages/count_tokens ──────────────────────────────────

    def _handle_count_tokens(self):
        # Best-effort: forward to a real endpoint or estimate
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length) if length else b"{}"

        # Try to get a real count from cloud.ru if they have a tokenizer endpoint
        # Fall back to rough estimate: ~4 chars per token
        try:
            req = json.loads(body)
            total = 0
            for msg in req.get("messages", []):
                content = msg.get("content", "")
                if isinstance(content, str):
                    total += max(1, len(content) // 4)
                elif isinstance(content, list):
                    for block in content:
                        if block.get("type") == "text":
                            total += max(1, len(block.get("text", "")) // 4)
            resp = {"input_tokens": total}
        except Exception:
            resp = {"input_tokens": 100}

        body = json.dumps(resp).encode()
        self._json_response(200, body)

    # ── helpers ─────────────────────────────────────────────────────────

    def _call_cloudru(self, req: dict, stream: bool = False):
        url = f"{CLOUDRU_URL}/chat/completions"
        data = json.dumps(req).encode()
        headers = {
            "Content-Type": "application/json",
            "Authorization": f"Bearer {CLOUDRU_API_KEY}",
        }
        debug_log("Cloud.ru URL: %s", url)
        debug_log("Cloud.ru body: %s", json.dumps(req, ensure_ascii=False)[:3000])
        http_req = Request(url, data=data, headers=headers, method="POST")
        return urlopen(http_req, timeout=300 if stream else 120)

    def _non_stream_response(self, oai_resp, orig_req: dict):
        raw_body = oai_resp.read()
        debug_log("Cloud.ru response: %s", raw_body.decode(errors="replace")[:3000])
        raw_log("NON_STREAM RESPONSE: %s", raw_body.decode(errors="replace"))
        oai_body = json.loads(raw_body)
        anthropic_resp = openai_to_anthropic(oai_body, orig_req)
        body = json.dumps(anthropic_resp, ensure_ascii=False).encode()
        debug_log("Anthropic resp: %s", body.decode(errors="replace")[:3000])
        self._json_response(200, body)

    def _stream_response(self, oai_resp):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        self.close_connection = True

        msg_id = f"msg_{uuid.uuid4().hex[:24]}"
        idx = 0
        started = False
        in_tool = False
        input_tokens = 0
        output_tokens = 0
        saw_finish = False
        _finish_sent = False   # guard against duplicate terminal events (GLM sends finish twice)
        _saw_broken_pipe = False
        _tool_args: dict[int, str] = {}  # index → accumulated arguments so far

        def flush_sse(event: str, data: dict):
            nonlocal _saw_broken_pipe
            if _saw_broken_pipe:
                return
            try:
                self.wfile.write(sse_event(event, data))
            except (BrokenPipeError, ConnectionResetError, OSError):
                _saw_broken_pipe = True

        # message_start
        flush_sse("message_start", {
            "type": "message_start",
            "message": {
                "id": msg_id,
                "type": "message",
                "role": "assistant",
                "content": [],
                "model": CLOUDRU_MODEL,
                "stop_reason": None,
                "stop_sequence": None,
                "usage": {"input_tokens": 0, "output_tokens": 0},
            },
        })

        try:
            for line in oai_resp:
                if _saw_broken_pipe:
                    break
                line = line.decode(errors="replace")
                if not line.startswith("data:"):
                    continue
                payload = line[5:].strip()
                if payload == "[DONE]":
                    saw_finish = True
                    break

                raw_log("SSE chunk: %s", payload)

                try:
                    chunk = json.loads(payload)
                except json.JSONDecodeError:
                    continue

                choice = chunk.get("choices", [{}])[0]
                delta  = choice.get("delta") or {}
                finish = choice.get("finish_reason")
                usage  = chunk.get("usage")

                if usage:
                    input_tokens  = usage.get("prompt_tokens", input_tokens)
                    output_tokens = usage.get("completion_tokens", output_tokens)

                # ── text start ──────────────────────────────────────────
                # Only start a text block on actual text content, NOT on
                # finish_reason alone.  GLM-5.2 sends finish="tool_calls"
                # alongside content="" — without this guard we'd emit a
                # spurious empty text block that breaks Claude Code's parser.
                has_text = bool(delta.get("content"))
                if not started and not delta.get("tool_calls") and has_text:
                    flush_sse("content_block_start", {
                        "type": "content_block_start",
                        "index": idx,
                        "content_block": {"type": "text", "text": ""},
                    })
                    started = True

                # ── text delta ──────────────────────────────────────────
                if delta.get("content") and not delta.get("tool_calls"):
                    flush_sse("content_block_delta", {
                        "type": "content_block_delta",
                        "index": idx,
                        "delta": {"type": "text_delta", "text": delta["content"]},
                    })

                # ── tool start / delta ──────────────────────────────────
                for tc in delta.get("tool_calls", []):
                    tc_index = tc.get("index", idx)

                    if tc.get("id"):
                        if started and not in_tool:
                            flush_sse("content_block_stop", {
                                "type": "content_block_stop", "index": idx,
                            })
                            idx += 1
                            started = False

                        flush_sse("content_block_start", {
                            "type": "content_block_start",
                            "index": idx,
                            "content_block": {
                                "type": "tool_use",
                                "id": tc["id"],
                                "name": tc.get("function", {}).get("name", ""),
                                "input": {},
                            },
                        })
                        in_tool = True

                    raw_args = tc.get("function", {}).get("arguments")
                    if raw_args:
                        # Normalize: GLM-5.2 may return arguments as a dict
                        if not isinstance(raw_args, str):
                            raw_args = json.dumps(raw_args, ensure_ascii=False)

                        # Compute delta: some providers send accumulated, some send deltas
                        prev = _tool_args.get(tc_index, "")
                        if len(raw_args) > len(prev) and raw_args.startswith(prev):
                            # Provider sent accumulated JSON — extract the new part
                            delta_str = raw_args[len(prev):]
                            _tool_args[tc_index] = raw_args
                        elif len(raw_args) > len(prev):
                            # Provider sent something that doesn't extend prev —
                            # treat as delta (normal OpenAI behaviour)
                            delta_str = raw_args
                            _tool_args[tc_index] = prev + raw_args
                        else:
                            delta_str = raw_args

                        if delta_str:
                            flush_sse("content_block_delta", {
                                "type": "content_block_delta",
                                "index": idx,
                                "delta": {
                                    "type": "input_json_delta",
                                    "partial_json": delta_str,
                                },
                            })

                # ── finish ──────────────────────────────────────────────
                if finish and not _finish_sent:
                    _finish_sent = True
                    saw_finish = True
                    if started or in_tool:
                        flush_sse("content_block_stop", {
                            "type": "content_block_stop", "index": idx,
                        })
                        idx += 1
                        started = False
                        in_tool = False

                    sr = "end_turn"
                    if finish == "length":
                        sr = "max_tokens"
                    elif finish == "tool_calls":
                        sr = "tool_use"
                    elif finish == "stop":
                        sr = "end_turn"

                    flush_sse("message_delta", {
                        "type": "message_delta",
                        "delta": {"stop_reason": sr, "stop_sequence": None},
                        "usage": {"output_tokens": output_tokens},
                    })
                    flush_sse("message_stop", {"type": "message_stop"})
        finally:
            # ALWAYS close the stream — even if upstream dies mid-stream.
            # Skip if the client already disconnected (BrokenPipe).
            if not saw_finish and not _saw_broken_pipe:
                if started or in_tool:
                    flush_sse("content_block_stop", {
                        "type": "content_block_stop", "index": idx,
                    })
                flush_sse("message_delta", {
                    "type": "message_delta",
                    "delta": {"stop_reason": "end_turn", "stop_sequence": None},
                    "usage": {"output_tokens": output_tokens},
                })
                flush_sse("message_stop", {"type": "message_stop"})

        if not _saw_broken_pipe:
            try:
                self.wfile.flush()
            except (BrokenPipeError, ConnectionResetError, OSError):
                pass

    # ── generic responses ──────────────────────────────────────────────

    def _json_response(self, code: int, body: bytes):
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _error(self, code: int, msg: str):
        body = json.dumps({"error": {"type": "proxy_error", "message": msg}}).encode()
        self._json_response(code, body)

    def _not_found(self):
        self._error(404, "Not found")

    def _status(self):
        self._json_response(200, json.dumps({
            "backend": CLOUDRU_URL,
            "model": CLOUDRU_MODEL,
            "status": "ok",
        }).encode())

    def _root(self):
        """Minimal Anthropic-compatible root response (Claude Code uses HEAD/GET /)."""
        self._json_response(200, json.dumps({
            "type": "api",
            "api": "anthropic",
            "version": "2023-06-01",
        }).encode())


# ── Main ────────────────────────────────────────────────────────────────────

def main():
    if not CLOUDRU_API_KEY:
        log("FATAL: CLOUDRU_API_KEY not set")
        sys.exit(1)

    # Set allow_reuse_address BEFORE constructing HTTPServer
    # (otherwise server_bind() runs before the attribute takes effect)
    HTTPServer.allow_reuse_address = True
    server = HTTPServer(("127.0.0.1", LISTEN_PORT), ProxyHandler)
    log("listening on 127.0.0.1:%d", LISTEN_PORT)
    log("backend: %s", CLOUDRU_URL)
    log("model:   %s", CLOUDRU_MODEL)
    if DEBUG:
        log("DEBUG MODE ON — verbose logging to stderr")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        log("shutting down")
        server.shutdown()


if __name__ == "__main__":
    main()
