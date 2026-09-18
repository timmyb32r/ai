"""Opt-in real-model integration: MODEL_DIR=/models python -m unittest test_real_asr.

Uses the model's own bundled Chinese test_wavs/zh.wav. No downloaded test audio.
"""
import json
import os
from pathlib import Path
import unittest
import wave

from resident import ResidentWorker


@unittest.skipUnless(os.environ.get("MODEL_DIR"), "set MODEL_DIR for real SenseVoice integration")
class RealASRTests(unittest.TestCase):
    def test_chinese_model_and_resident_reuse(self):
        model_dir = Path(os.environ["MODEL_DIR"])
        path = model_dir / "test_wavs" / "zh.wav"
        if not path.is_file():
            self.skipTest("model bundle has no Chinese test WAV")
        with wave.open(str(path), "rb") as wav:
            self.assertEqual((wav.getnchannels(), wav.getsampwidth(), wav.getframerate()), (1,2,16000))
            body = wav.readframes(wav.getnframes())
        worker = ResidentWorker("asr", {"model_dir": str(model_dir), "threads": 2})
        try:
            pid = worker.process.pid
            first = json.loads(worker.run(body, 30))
            second = json.loads(worker.run(body, 30))
            self.assertEqual(worker.process.pid, pid)
            self.assertEqual(first, second)
            self.assertTrue(first["text"])
            self.assertTrue(any('\u4e00' <= c <= '\u9fff' for c in first["text"]))
            self.assertGreater(len(first["tokens"]), 5)
            self.assertEqual(len(first["tokens"]), len(first["timestamps"]))
            self.assertEqual(sorted(first["timestamps"]), first["timestamps"])
            print(json.dumps(first, ensure_ascii=False))
        finally:
            worker.close()
