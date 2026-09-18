"""Bounded HTTP front end and a single, independently killable native worker.

The worker loads its model once. A timed-out native call causes the child to be
killed and reaped before this service exits; Docker may then start its replacement.
The watchdog runs outside native inference, so a held Python GIL cannot defeat it.
"""
import json
import logging
import math
import multiprocessing
import os
import socket
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from socketserver import ThreadingMixIn

MAX_AUDIO_BYTES = 30 * 16000 * 2
MAX_JSON_BYTES = 1 << 20


class Busy(Exception):
    pass


class WorkerFailure(Exception):
    pass


def load_backend(kind, config):
    # Heavy imports and native state live only in the model child.
    if kind == "asr":
        from asr_backend import SenseVoice
        return SenseVoice(config)
    if kind == "hanlp":
        from hanlp_backend import HanLP
        return HanLP(config)
    raise ValueError("unknown service kind")


def child_main(connection, kind, config, factory):
    try:
        backend = factory(kind, config)
        connection.send((True, None))
        while True:
            payload = connection.recv_bytes(MAX_AUDIO_BYTES + 4096)
            result = backend(payload)
            encoded = json.dumps(result, ensure_ascii=False, allow_nan=False).encode()
            if len(encoded) > MAX_JSON_BYTES:
                raise ValueError("model response too large")
            connection.send_bytes(encoded)
    except (EOFError, BrokenPipeError):
        pass
    except BaseException:
        logging.exception("native worker failed")
    finally:
        connection.close()


class ResidentWorker:
    def __init__(self, kind, config, startup_timeout=240, factory=load_backend):
        ctx = multiprocessing.get_context("spawn")
        self.connection, child = ctx.Pipe()
        self.process = ctx.Process(target=child_main, args=(child, kind, config, factory))
        self.lock = threading.Lock()
        self.close_lock = threading.Lock()
        self.closed = False
        self.process.start()
        child.close()
        try:
            if not self.connection.poll(startup_timeout):
                raise WorkerFailure("model initialization timed out")
            if self.connection.recv() != (True, None):
                raise WorkerFailure("model failed to initialize")
        except (EOFError, OSError) as exc:
            self.close()
            raise WorkerFailure("model failed to initialize") from exc
        except BaseException:
            self.close()
            raise

    @property
    def ready(self):
        return not self.closed and self.process.is_alive()

    def run(self, payload, timeout):
        if not self.lock.acquire(blocking=False):
            raise Busy("worker is busy")
        watchdog = threading.Timer(timeout, self.close)
        watchdog.daemon = True
        try:
            if not self.ready:
                raise WorkerFailure("worker unavailable")
            # Covers blocked IPC too, e.g. a stopped child that never reads its
            # pipe. This timer is outside the interpreter doing native inference.
            watchdog.start()
            self.connection.send_bytes(payload)
            if not self.connection.poll(timeout):
                raise WorkerFailure("native inference timed out")
            result = self.connection.recv_bytes(MAX_JSON_BYTES)
            if not self.ready:
                raise WorkerFailure("native inference timed out")
            return result
        except (EOFError, OSError, WorkerFailure) as exc:
            self.close()
            raise WorkerFailure(str(exc)) from exc
        finally:
            watchdog.cancel()
            self.lock.release()

    def close(self):
        # Concurrent watchdog/shutdown callers must wait for the *completed*
        # reap, not merely observe that somebody started closing the worker.
        with self.close_lock:
            if self.closed:
                return
            self.closed = True
            # First TERM, then KILL. Always join before allowing replacement.
            if self.process.is_alive():
                self.process.terminate()
                self.process.join(0.5)
            if self.process.is_alive():
                self.process.kill()
            self.process.join()
            self.connection.close()


def validate_request(kind, body):
    if kind == "asr":
        if not 3200 <= len(body) <= MAX_AUDIO_BYTES or len(body) % 2:
            raise ValueError("expected 0.1–30 seconds PCM16 LE at 16000 Hz mono")
        return
    obj = json.loads(body)
    if not isinstance(obj, dict) or not isinstance(obj.get("text"), str):
        raise ValueError("text must be a string")
    if len(obj["text"]) > 4096:
        raise ValueError("text exceeds 4096 characters")


class BoundedHTTPServer(ThreadingMixIn, HTTPServer):
    daemon_threads = True
    request_queue_size = 8

    def __init__(self, address, worker, kind, inference_timeout):
        self.worker, self.kind = worker, kind
        self.inference_timeout = inference_timeout
        self.slots = threading.BoundedSemaphore(4)
        self.fatal = threading.Event()
        super().__init__(address, Handler)

    def process_request(self, request, client_address):
        if not self.slots.acquire(blocking=False):
            try:
                request.settimeout(0.1)
                request.sendall(b"HTTP/1.1 503 Service Unavailable\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
            except OSError:
                pass
            self.shutdown_request(request)
            return
        try:
            super().process_request(request, client_address)
        except BaseException:
            self.slots.release()
            raise

    def process_request_thread(self, request, client_address):
        try:
            super().process_request_thread(request, client_address)
        finally:
            self.slots.release()


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def setup(self):
        self.request.settimeout(5)
        super().setup()

    def reply(self, status, body):
        self.close_connection = True
        try:
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Connection", "close")
            if status in (429, 503):
                self.send_header("Retry-After", "1")
            self.end_headers()
            self.wfile.write(body)
        except OSError:
            pass  # Client disappearance must not abort the resident worker.

    def do_GET(self):
        if self.path != "/health":
            self.reply(404, b'{"error":"not found"}')
        elif self.server.worker.ready and not self.server.fatal.is_set():
            self.reply(200, b'{"ready":true}')
        else:
            self.reply(503, b'{"ready":false}')

    def do_POST(self):
        kind = self.server.kind
        allowed = ("/transcribe",) if kind == "asr" else ("/parse", "/segment")
        if self.path not in allowed:
            self.reply(404, b'{"error":"not found"}')
            return
        expected = "application/octet-stream" if kind == "asr" else "application/json"
        if self.headers.get_content_type() != expected:
            self.reply(415, b'{"error":"unsupported media type"}')
            return
        lengths = self.headers.get_all("Content-Length", [])
        if self.headers.get("Transfer-Encoding") or len(lengths) != 1:
            self.reply(411, b'{"error":"one Content-Length required"}')
            return
        try:
            length = int(lengths[0])
        except ValueError:
            self.reply(400, b'{"error":"invalid Content-Length"}')
            return
        max_body = MAX_AUDIO_BYTES if kind == "asr" else 32768
        if length < 0 or length > max_body:
            self.reply(413, b'{"error":"body too large"}')
            return
        try:
            body = self.rfile.read(length)
            if len(body) != length:
                raise ValueError("incomplete body")
            validate_request(kind, body)
        except (ValueError, UnicodeError):
            self.reply(400, b'{"error":"invalid input"}')
            return
        except (TimeoutError, OSError):
            self.reply(408, b'{"error":"body read timeout"}')
            return
        try:
            result = self.server.worker.run(body, self.server.inference_timeout)
            self.reply(200, result)
        except Busy:
            self.reply(429, b'{"error":"worker busy"}')
        except WorkerFailure:
            logging.exception("worker retired; restarting service")
            self.server.fatal.set()
            self.reply(503, b'{"error":"worker failed"}')

    def log_message(self, *_):
        pass  # No transcript logging or per-chunk stdout growth.


def serve(kind, config, port, inference_timeout):
    worker = ResidentWorker(kind, config)
    server = BoundedHTTPServer(("0.0.0.0", port), worker, kind, inference_timeout)
    server.timeout = 0.5
    logging.info("%s model ready on port %d", kind, port)
    try:
        while worker.ready and not server.fatal.is_set():
            server.handle_request()
    finally:
        server.server_close()
        worker.close()
    raise SystemExit(70)


if __name__ == "__main__":
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    kind = os.environ.get("SERVICE_KIND", "asr")
    threads = int(os.environ.get("MODEL_THREADS", "2" if kind == "asr" else "1"))
    timeout = float(os.environ.get("INFERENCE_TIMEOUT", "30" if kind == "asr" else "10"))
    if not 1 <= threads <= 4 or not math.isfinite(timeout) or not 1 <= timeout <= 60:
        raise SystemExit("invalid worker bounds")
    serve(kind, {"model_dir": os.environ.get("MODEL_DIR", "/models"), "threads": threads},
          int(os.environ.get("PORT", "8766" if kind == "asr" else "8765")), timeout)
