# Resident inference services

ASR loads the existing **SenseVoice Small int8, 2024-07-17** model once per worker
lifetime, with Chinese language, ITN enabled, CPU execution, and two native threads.
HanLP preserves **COARSE_ELECTRA_SMALL_ZH** and the `tok/fine` / `tok/coarse` response.
Neither service launches a model CLI per chunk or writes request audio to disk.

The HTTP supervisor and native model are separate processes. Exactly one inference
is admitted; concurrent inference receives 429. There are at most four HTTP handler
threads, a bounded accept backlog, a 5-second request-body read timeout, and explicit
input/output size limits. The supervisor kills and reaps a hung/crashed native worker
and exits. Compose then restarts the container; a new model never overlaps the old
one. `GET /health` returns 200 only after model load and a warm-up inference succeed.
Disconnection of a client does not mark the model idle: the old native computation
must finish or be killed before another job can run.

ASR contract:

- `POST /transcribe`, `Content-Type: application/octet-stream`, fixed PCM16 little
  endian, 16000 Hz mono, 0.1–30 seconds, no WAV header.
- JSON `{ "text": "…", "tokens": ["…"], "timestamps": [0.12] }`.
- One time per model token, relative to this input; a token can contain multiple
  Unicode characters. No claim that tokens equal Chinese words or rune indices.
- Bad body/type: 400/413/415. Busy: 429. Failed native worker: 503 then restart.
- Raw PCM and model responses never appear in access logs.

HanLP accepts `POST /parse` (also `/segment`) with JSON `{ "text": "…" }`, maximum
4096 characters / 32 KiB. CPU inference is serialized and independently supervised.

## Assets and deployment

Model/dictionary paths are host bind mounts, read-only in runtime. No model cache is
copied into image layers. Start from `.env.example`, or run:

```sh
sh scripts/prepare-assets.sh /absolute/path/to/server/.docker-cache /absolute/path/to/hanlp-cache
docker compose --profile prepare run --rm --build prepare-hanlp
docker compose up -d --build
```

Preparation is a deliberate one-off network-enabled action. Runtime inference has
an internal-only Docker network and offline model-cache settings. The preparation
container writes only the selected HanLP cache. Existing cached HanLP assets can
be mounted directly if they contain the same model and all of its dependencies.
Keep those assets outside the application checkout and back them up separately.

Memory ceilings: Go 2560 MiB, ASR 1536 MiB, HanLP 2 GiB (6 GiB total); CPU ceilings
sum to 4. Swap cannot exceed those memory limits. Runtime log files rotate at
10 MiB × 3 per service. Inference ports are not published to the host.

## Verification

```sh
go test -race ./internal/asr
python3 -m unittest discover -s services -p 'test_resident.py' -v
docker compose config --quiet
```

Opt-in real-model test, inside the ASR image, using the model bundle's Chinese sample:

```sh
docker build --target test -f services/Dockerfile.asr -t cri-server2-asr:test .
docker run --rm --network none --read-only --tmpfs /tmp --memory 1536m --cpus 2 \
  -e MODEL_DIR=/models -v /absolute/path/to/sense-voice-2024:/models:ro \
  cri-server2-asr:test python -m unittest test_real_asr -v
```

This checks actual Chinese output, token timing, deterministic reuse and unchanged
worker PID. It does not establish production latency or exact matching with an
unknown previously deployed sherpa-onnx version; measure those on the target VM.
