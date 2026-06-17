  # whisper.cpp с ggml-small
  docker build -f Dockerfile.base \
    --build-arg ASR_ENGINE=whisper --build-arg ASR_MODEL=ggml-small \
    -t criradio-base:latest .

  # sherpa-onnx с SenseVoice
  docker build -f Dockerfile.base \
    --build-arg ASR_ENGINE=sherpa-onnx --build-arg ASR_MODEL=sense-voice-2024 \
    -t criradio-base:latest .
