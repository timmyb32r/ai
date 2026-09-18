"""SenseVoice 2024; preserves zh + inverse text normalization from the old CLI."""
import math
from pathlib import Path


class SenseVoice:
    def __init__(self, config):
        import numpy as np
        import sherpa_onnx
        path = Path(config["model_dir"])
        for name in ("model.int8.onnx", "tokens.txt"):
            if not (path / name).is_file():
                raise ValueError(f"required model asset missing: {name}")
        self.np = np
        self.recognizer = sherpa_onnx.OfflineRecognizer.from_sense_voice(
            model=str(path / "model.int8.onnx"), tokens=str(path / "tokens.txt"),
            language="zh", use_itn=True, num_threads=config["threads"],
            provider="cpu", debug=False,
        )
        # Readiness requires a successful native inference, not just file presence.
        self(bytes(3200))

    def __call__(self, body):
        samples = self.np.frombuffer(body, dtype="<i2").astype(self.np.float32) / 32768.0
        stream = self.recognizer.create_stream()
        stream.accept_waveform(16000, samples)
        self.recognizer.decode_stream(stream)
        result = stream.result
        text, tokens, timestamps = result.text, list(result.tokens), list(result.timestamps)
        if len(tokens) != len(timestamps) or (text.strip() and not tokens):
            raise ValueError("SenseVoice result lacks token timestamps")
        duration = len(samples) / 16000
        for i, t in enumerate(timestamps):
            if not math.isfinite(t) or t < 0 or t > duration + 0.25 or (i and t < timestamps[i-1]):
                raise ValueError("invalid SenseVoice timestamp")
        return {"text": text, "tokens": tokens, "timestamps": timestamps}
