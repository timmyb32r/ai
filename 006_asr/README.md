# yt2srt — YouTube to SRT Subtitle Service

Single-page web service that transcribes Chinese speech from YouTube videos into SRT subtitles using
[sense_voice](https://github.com/k2-fsa/sherpa-onnx) (sherpa-onnx).

## Quick Start (Docker)

```bash
# 1. Download the model (one time)
./download-cache.sh

# 2. Build and start
./docker-build.sh
docker run --rm -p 8080:8080 yt2srt:latest

# 3. Open http://localhost:8080
```

## Docker Compose (convenience)

```bash
# After docker-build.sh:
docker compose up
```

## How the Docker Build Works

Two-layer image strategy (same as 003_omc_cri2):

| Layer | Image | Built When | Contents |
|-------|-------|-----------|----------|
| Base | `Dockerfile.base` | Model / sherpa-onnx version changes | ffmpeg, sherpa-onnx, yt-dlp, sense_voice model |
| Server | `Dockerfile` | Code changes | Just the `yt2srt` Go binary |

```bash
./download-cache.sh               # → .docker-cache/sense-voice-2024/
./docker-build.sh                 # → yt2srt-base:latest → yt2srt:latest
./docker-build.sh --rebuild-base  # Force rebuild base layer
```

After code changes, `./docker-build.sh` only recompiles the binary and rebuilds the thin server layer — seconds, not minutes.

## Local Development (macOS)

```bash
brew install go ffmpeg
pip3 install yt-dlp sherpa-onnx-bin

# Download model
./download-cache.sh
mkdir -p /opt/models
cp -r .docker-cache/sense-voice-2024 /opt/models/

# Run
go build -o yt2srt ./cmd/server
MODEL_PATH=/opt/models ASR_MODEL=sense-voice-2024 ./yt2srt
```

## API

| Method | Path | Description |
|--------|------|-------------|
| GET | `/` | Frontend SPA |
| GET | `/health` | Health check |
| POST | `/api/transcribe` | Submit YouTube URL → `{"job_id":"..."}` |
| GET | `/api/status/{id}` | Poll job status and progress |
| GET | `/api/download/{id}` | Download SRT file |

## Configuration

See `.env.example` for all environment variables. Key ones:

- `MODEL_PATH` — path to models directory (default: `/opt/models`)
- `ASR_MODEL` — model codename (`sense-voice-2024`, `paraformer-zh`)
- `DEPLOY_MODE` — `local` (default) or `ycloud`

## Deployment Modes

- **local** — Docker on MacBook/ARM64, chunks on local disk
- **ycloud** — Yandex Cloud Serverless Containers, chunks via Object Storage

## Architecture

```
Browser → [Go Server]
            ├─ yt-dlp: download YouTube audio
            ├─ ffmpeg: convert to PCM 16kHz mono
            ├─ sherpa-onnx-offline (sense_voice): parallel chunk transcription
            └─ SRT merge + download
```

## License

MIT
