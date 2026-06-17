# CRI Radio — live Chinese-radio client/server with offline subtitles

A Go client/server pair that streams CRI-905 (or any HLS radio URL) to thin
console clients while transcribing the audio offline into real-time Chinese
subtitles, synchronised to within roughly ±1.5 s of the audio.

## What it is

The server ingests one copy of the live HLS stream, keeps a configurable
rolling buffer, and runs silence-bounded ASR (via SenseVoice + sherpa-onnx)
entirely offline — the only outbound network traffic is the single HLS fetch
and the HTTP fan-out to local clients. Console clients connect over HTTP,
receive the audio stream via chunked transfer and subtitles via SSE, and play
audio through ffplay locally.

## Architecture

The server is organised as four cooperating packages. `internal/ingest` spawns
one ffmpeg subprocess that emits PCM (s16le 16 kHz mono) for both ASR and
clients; the PCM frames land in `internal/broadcast`'s rolling buffer.
`internal/broadcast` tracks a configurable delay behind the
live edge (default 180 s), real-time-paces each subscriber's PCM delivery, and
runs an ASR driver that feeds silence-bounded WAV segments to the
`chineseasr.Transcriber` (which shells out to `sherpa-onnx-offline`). Subtitle
events land back in the buffer keyed by timeline position and are fanned to
subscribers alongside the audio. The HTTP surface (`internal/api`) serves the
three endpoints over plain HTTP; the console client (`cmd/client`) connects,
pipes the audio into ffplay, and prints subtitles to stderr. Ingest is
ref-counted: it starts on the first client connection and stops (with a short
linger) after the last client disconnects, so the server is idle when unused.

## Install

### Server dependencies (must be caller-provisioned — not bundled)

| Dependency | Install |
|---|---|
| `ffmpeg` | `brew install ffmpeg` / `apt install ffmpeg` / download from ffmpeg.org |
| `sherpa-onnx-offline` binary | https://github.com/k2-fsa/sherpa-onnx/releases |
| SenseVoice model dir | Download `sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17` from the sherpa-onnx model zoo and point `-model-dir` at it. The directory must contain `model.int8.onnx` and `tokens.txt`. |

### Client dependencies

The client needs only `ffplay` (part of the ffmpeg distribution) and a Go
toolchain to build.

## Run

### Server

```sh
go run ./cmd/server \
  -model-dir /path/to/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17 \
  -delay 180s
```

Additional server flags (all optional):

| Flag | Default | Description |
|---|---|---|
| `-addr` | `:8080` | HTTP listen address |
| `-ffmpeg` | `ffmpeg` | Path to the ffmpeg binary |
| `-sherpa` | `sherpa-onnx-offline` | Path to the sherpa-onnx-offline binary |
| `-model-dir` | _(required)_ | Directory with `model.int8.onnx` + `tokens.txt` |
| `-channel-url` | `https://sk.cri.cn/905.m3u8` | Live HLS stream URL |
| `-delay` | `180s` | Broadcast delay behind the live edge |
| `-buffer` | `5m` | Rolling buffer window (reserved) |
| `-silence-db` | `-30` | Silence noise floor dB (reserved — chineseasr uses internal constants in v1) |
| `-silence-min` | `500ms` | Minimum silence duration (reserved — chineseasr uses internal constants in v1) |

### Client

```sh
go run ./cmd/client -server http://localhost:8080
```

The client prints received subtitles to stderr and pipes audio to ffplay. For
the lowest latency, tune ffplay's input buffer:

```sh
# ffplay flags are passed through FFPLAY_OPTS or set in the client source:
FFPLAY_OPTS="-fflags nobuffer -probesize 32k" go run ./cmd/client -server http://localhost:8080
```

## REST API

| Method | Path | Response | Description |
|---|---|---|---|
| GET | `/v1/stream/audio` | `audio/L16;rate=16000;channels=1` chunked | Real-time-paced PCM s16le 16 kHz mono stream |
| GET | `/v1/stream/subtitles` | `text/event-stream` SSE | JSON `SubtitleEvent` objects (`start`, `end`, `text_zh` fields) |
| GET | `/v1/status` | `application/json` | Snapshot: channel, listeners, state, delay, live-edge offset |

SSE subtitle event shape:

```json
{"start": 12.3, "end": 15.1, "text_zh": "今天的天气非常好。"}
```

### Error behaviour on an unreachable source

If the live source cannot be reached, ingest produces no bytes, so the audio
endpoint responds with an empty `200` (the chunked body simply never carries
audio) rather than a hard error; the client surfaces the problem via the
non-`200` paths it already handles (an HTTP error on connect, or an SSE error on
the subtitle stream). Returning a hard `502` as an explicit readiness signal
when the source is down is a documented v1 follow-up; the endpoint interface is
unchanged.

## Offline scope

The chineseasr library and ASR engine operate entirely offline on local files.
The **only** outbound network egress from the server process is the single HLS
fetch to the radio URL (`-channel-url`). No audio data, no transcriptions, and
no telemetry are sent to any remote service.

## Synchronisation tolerance

Audio and subtitles are aligned to within approximately ±1.5 s. The delay
(`-delay`, default 180 s) gives the ASR engine its lead time: by the time a
speech region is released to subscribers the transcription is already in the
buffer. The ±1.5 s residual comes from the ffplay input buffer and network
jitter; it can be reduced by lowering ffplay's buffer with `-fflags nobuffer
-probesize 32k`.

## On-demand lifecycle

Ingest (ffmpeg subprocess + ASR) starts on the first client connection and
stops approximately 10 s after the last client disconnects. A rapid
reconnect within that linger window resumes the existing stream without
restarting ffmpeg, so toggle off/on is seamless.

## Deferred features

The following are schema-reserved but not implemented in v1:

- Pinyin romanisation (`pinyin` field in SubtitleEvent)
- English translation (`en` field in SubtitleEvent)
- Word-level synchronisation
- `-silence-db` / `-silence-min` passthrough to the ASR engine
