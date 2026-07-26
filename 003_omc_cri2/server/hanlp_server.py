#!/usr/bin/env python3
"""HanLP REST API server — self-contained, no CLI dependencies.

Serves POST /parse  {"text": "..."}  →  {"tok/fine": [...], "tok/coarse": [...]}
"""
import json
import sys
from http.server import HTTPServer, BaseHTTPRequestHandler

# ── Load tokenizer at startup ──────────────────────────────────────────
print("[hanlp-server] loading tokenizer model...", flush=True)
import hanlp

tok = hanlp.load(hanlp.pretrained.tok.COARSE_ELECTRA_SMALL_ZH)
print(f"[hanlp-server] model loaded", flush=True)


# ── HTTP handler ───────────────────────────────────────────────────────
class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("content-length", 0))
        body = json.loads(self.rfile.read(length)) if length > 0 else {}
        text = body.get("text", "")

        if text:
            result = tok(text)  # returns list[str] of tokenized words
            response = {
                "tok/fine": result,
                "tok/coarse": result,
            }
        else:
            response = {"tok/fine": [], "tok/coarse": []}

        data = json.dumps(response, ensure_ascii=False).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, fmt, *args):
        print(f"[hanlp-server] {args[0]}", flush=True)


# ── Main ───────────────────────────────────────────────────────────────
port = int(sys.argv[1]) if len(sys.argv) > 1 else 8765
srv = HTTPServer(("0.0.0.0", port), Handler)
print(f"[hanlp-server] listening on :{port}", flush=True)

try:
    srv.serve_forever()
except KeyboardInterrupt:
    pass
finally:
    srv.server_close()
