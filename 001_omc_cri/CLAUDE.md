# CRI Radio Live-Subtitle Server

Go server that ingests a live HLS radio stream, transcribes Chinese speech to text offline, and broadcasts real-time-paced PCM audio with synchronized SSE subtitles to thin clients.

## Architecture

```
  HLS URL ──> [ffmpeg] ──── PCM (stdout) ──> [Buffer] <── [ASR Driver]
                           │                    │  PCM track      │ 8s fixed windows
                           │                    │  Subs track ────┘ writes SubtitleEvents
                           │                    │
               ┌──────────────────────────┘
               v
       [Pacer x N subscribers]
               │
       ┌───────┴───────┐
       v               v
 GET /v1/stream/audio    GET /v1/stream/subtitles
 (chunked audio/L16)    (SSE text/event-stream)
```

- **Single ingest**: one ffmpeg process — PCM on stdout
- **Rolling buffer**: `Buffer` stores PCM and subtitles in a delay+margin window, thread-safe
- **ASR**: fixed 8s windows, 2s pass interval, 2s live margin. Uses `chineseasr` library (sherpa-onnx-offline + ffmpeg)
- **Pacer**: one goroutine per subscriber, ticks every 100ms, releases content up to broadcastHead (bufferHead - delay), drop-oldest on bounded queues
- **Lifecycle**: ref-counted with linger timer; `StartAlwaysOn()` keeps ingest running independently of clients

## Package Map

| Package | Role |
|---------|------|
| `cmd/server/` | Entry point: wires ingest, broadcast, ASR, API mux, graceful shutdown |
| `cmd/client/` | Reference CLI client: plays PCM via ffplay, prints SSE subtitles (stdlib only) |
| `examples/cli/` | Demo CLI wrapping the chineseasr library directly |
| `internal/api/` | HTTP mux: `GET /v1/stream/audio` (audio/L16), `GET /v1/stream/subtitles` (SSE), `GET /v1/status` (JSON) |
| `internal/broadcast/` | Core pipeline: `Buffer`, `Broadcast`, `Lifecycle`, `ASR` driver, `Clock`, word segmentation, CC-CEDICT enrichment |
| `internal/chineseasr/` | Offline ASR library: `Transcriber.Transcribe()` via ffmpeg + sherpa-onnx-offline |
| `internal/engine/` | sherpa-onnx arg builder, stdout parser, silence detector |
| `internal/audio/` | ffmpeg subprocess wrapper: decode/resample to 16kHz mono WAV |
| `internal/ingest/` | HLS ingest: ffmpeg subprocess, reconnect with exponential backoff |
| `internal/runner/` | Test seam: `Runner` interface for subprocess execution |
| `internal/asrerr/` | Sentinel errors shared across packages |

## Key Interfaces

| Interface | Package | Purpose |
|-----------|---------|---------|
| `Broadcaster` | `api` | Subscribe (audio + subs), Status |
| `Clock` | `broadcast` | Deterministic time for tests |
| `WordSegmenter` | `broadcast` | Chinese word segmentation (gse) |
| `Runner` | `runner` | Subprocess execution seam |
| `Process` | `ingest` | ffmpeg process lifecycle |

## Build & Test

```bash
go build ./...                    # build all
go test ./... -count=1            # run all tests
gofmt -l .                        # check formatting (must be empty)
GOCACHE=/tmp/gocache go build ./... # if /Volumes/GoBuildCache permission issue
```

## Docker (layered: base + server)

```
Dockerfile.base  →  criradio-base:latest     (build once, rarely changes)
Dockerfile       →  criradio-server:latest   (build every time Go changes — fast)
```

**Base image** (`Dockerfile.base`): ffmpeg + sherpa-onnx-offline + SenseVoice model + CC-CEDICT + gse dicts + entrypoint. Rebuild only when model/sherpa/CEDICT versions change.

**Server image** (`Dockerfile`): `FROM criradio-base`, Go build stage → copy binary. Only ~10s of Go compilation; no model extraction, pip install, or apt-get.

```bash
./download-cache.sh               # one-time: cache model + wheels + dicts
./docker-build.sh                 # builds base (once) + server (every time)
./docker-build.sh --rebuild-base  # force base rebuild too
docker compose up                 # start
```

## Environment Variables

| Variable | Purpose |
|----------|---------|
| `CRIRADIO_DEBUG=1` | Enable verbose pacer/subtitle logging in broadcast |

## Run

```bash
go run ./cmd/server \
  -ffmpeg $(which ffmpeg) \
  -sherpa $(which sherpa-onnx-offline) \
  -model-dir /path/to/sense-voice-model \
  -delay 3m0s \
  -always-on
```

## API

| Endpoint | Content-Type | Description |
|----------|-------------|-------------|
| `GET /v1/stream/audio` | `audio/L16;rate=16000;channels=1` | Chunked PCM s16le 16kHz mono stream. Header `X-Audio-Timeline-Start` on connect |
| `GET /v1/stream/subtitles` | `text/event-stream` | SSE stream: `event: sync`, `event: subtitle`, `event: jump` |
| `GET /v1/status` | `application/json` | `{"channel","listeners","delaySeconds","state","liveEdgeOffsetSeconds"}` |

## Common AI Agent Tasks

- **Add a new ASR model**: wire `internal/engine/args.go` (`requiredModelFiles` + `Build`), add to `internal/chineseasr/chineseasr.go` (`Model` const + `requiredModelFiles`)
- **Change the broadcast delay**: modify `-delay` default in `cmd/server/main.go` or pass at runtime
- **Add a new API endpoint**: add handler to `internal/api/api.go`, wire in `NewMux`
- **Fix a pacer bug**: tests are in `internal/broadcast/broadcast_test.go` with `FakeClock` — run with `-count=1` to avoid cache
