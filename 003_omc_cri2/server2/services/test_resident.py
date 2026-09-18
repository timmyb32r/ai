import http.client
import json
import os
import signal
import threading
import time
import unittest

from resident import BoundedHTTPServer, Busy, ResidentWorker, WorkerFailure, validate_request


class FakeBackend:
    def __init__(self, config):
        self.config, self.calls = config, 0
        if config.get("ignore_term"):
            signal.signal(signal.SIGTERM, signal.SIG_IGN)

    def __call__(self, payload):
        self.calls += 1
        if self.config.get("crash"):
            os._exit(72)
        time.sleep(self.config.get("sleep", 0))
        return {"text": "中国", "tokens": ["中国"], "timestamps": [0], "calls": self.calls}


def fake_factory(kind, config):
    if config.get("startup_hang"):
        time.sleep(30)
    return FakeBackend(config)


class WorkerTests(unittest.TestCase):
    def worker(self, config=None):
        w = ResidentWorker("asr", config or {}, startup_timeout=3, factory=fake_factory)
        self.addCleanup(w.close)
        return w

    def test_model_lives_across_requests(self):
        w = self.worker()
        pid = w.process.pid
        for i in range(1, 5):
            self.assertEqual(json.loads(w.run(bytes(3200), 1))["calls"], i)
            self.assertEqual(w.process.pid, pid)

    def test_native_timeout_kills_and_reaps_worker(self):
        w = self.worker({"sleep": 30, "ignore_term": True})
        started = time.monotonic()
        with self.assertRaises(WorkerFailure):
            w.run(bytes(3200), 0.1)
        self.assertLess(time.monotonic()-started, 2)
        self.assertFalse(w.ready)
        self.assertFalse(w.process.is_alive())
        with self.assertRaises(WorkerFailure):
            w.run(bytes(3200), 1)

    @unittest.skipUnless(hasattr(signal, "SIGSTOP"), "requires POSIX process signals")
    def test_watchdog_also_unblocks_pipe_write(self):
        w = self.worker()
        os.kill(w.process.pid, signal.SIGSTOP)
        started = time.monotonic()
        with self.assertRaises(WorkerFailure):
            w.run(bytes(960000), 0.1)
        self.assertLess(time.monotonic()-started, 2)
        self.assertFalse(w.process.is_alive())

    def test_no_overlapping_inference_or_queue(self):
        w = self.worker({"sleep": 0.2})
        completed = []
        thread = threading.Thread(target=lambda: completed.append(w.run(bytes(3200), 1)))
        thread.start()
        deadline = time.monotonic()+1
        while not w.lock.locked() and time.monotonic()<deadline:
            time.sleep(0.001)
        with self.assertRaises(Busy):
            w.run(bytes(3200), 1)
        thread.join(2)
        self.assertEqual(len(completed), 1)

    def test_worker_crash_is_unavailable(self):
        w = self.worker({"crash": True})
        with self.assertRaises(WorkerFailure):
            w.run(bytes(3200), 1)
        self.assertFalse(w.ready)

    def test_initialization_deadline(self):
        started = time.monotonic()
        with self.assertRaises(WorkerFailure):
            ResidentWorker("asr", {"startup_hang": True}, startup_timeout=0.1, factory=fake_factory)
        self.assertLess(time.monotonic()-started, 2)


class HTTPTests(unittest.TestCase):
    def setUp(self):
        self.worker = ResidentWorker("asr", {}, factory=fake_factory)
        self.addCleanup(self.worker.close)
        self.server = BoundedHTTPServer(("127.0.0.1", 0), self.worker, "asr", 1)
        self.thread = threading.Thread(target=self.server.serve_forever)
        self.thread.start()

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()
        self.worker.close()

    def request(self, path, body=None, headers=None, method="POST"):
        conn = http.client.HTTPConnection(*self.server.server_address, timeout=2)
        try:
            conn.request(method, path, body=body, headers=headers or {})
            resp = conn.getresponse()
            return resp.status, resp.read()
        finally:
            conn.close()

    def test_health_and_real_protocol(self):
        self.assertEqual(self.request("/health", method="GET"), (200, b'{"ready":true}'))
        status, data = self.request("/transcribe", bytes(3200), {"Content-Type": "application/octet-stream"})
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(data)["tokens"], ["中国"])
        self.worker.close()
        self.assertEqual(self.request("/health", method="GET")[0], 503)

    def test_rejects_input_before_native_code(self):
        headers = {"Content-Type": "application/octet-stream"}
        self.assertEqual(self.request("/transcribe", b"", headers)[0], 400)
        self.assertEqual(self.request("/transcribe", b"x"*3201, headers)[0], 400)
        self.assertEqual(self.request("/transcribe", bytes(3200), {"Content-Type": "audio/wav"})[0], 415)
        self.assertEqual(self.request("/transcribe", b"", {**headers, "Content-Length": "99999999"})[0], 413)
        self.assertEqual(self.request("/missing", b"", headers)[0], 404)

    def test_saturated_handlers_are_bounded(self):
        for _ in range(4):
            self.assertTrue(self.server.slots.acquire(False))
        try:
            self.assertEqual(self.request("/health", method="GET")[0], 503)
        finally:
            for _ in range(4):
                self.server.slots.release()


class InputTests(unittest.TestCase):
    def test_hanlp_unicode_and_limits(self):
        validate_request("hanlp", json.dumps({"text": "你好AI"}).encode())
        for body in [b'null', b'{"text":3}', b'[]', json.dumps({"text": "中"*4097}).encode()]:
            with self.assertRaises(ValueError):
                validate_request("hanlp", body)


if __name__ == "__main__":
    unittest.main()
