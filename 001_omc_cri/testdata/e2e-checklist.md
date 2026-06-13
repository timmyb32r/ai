# End-to-end acceptance checklist — CRI radio client/server

Recorded manual acceptance checklist. Run against a real deployment with the
live CRI-905 HLS URL (`https://sk.cri.cn/905.m3u8`) and a valid SenseVoice
model directory. A Chinese reader should confirm subtitle accuracy.

## Setup

```sh
# Terminal 1 — server
go run ./cmd/server -model-dir /path/to/sense-voice -delay 180s

# Terminal 2 — first client
go run ./cmd/client -server http://localhost:8080

# Terminal 3 — second client (for multi-client tests)
go run ./cmd/client -server http://localhost:8080
```

## Checklist

| # | Scenario | Steps | Expected | Pass/Fail | Notes |
|---|---|---|---|---|---|
| 1 | **Basic: audio + subtitles** | Server up, connect 1 client. Wait for audio to start playing, then wait for first subtitle. Have a Chinese reader verify subtitle content. | Audio plays within seconds of client connect (warming phase). Correct Chinese subtitles appear within ±1.5 s of corresponding audio. | | |
| 2 | **Cold start warm-up** | Start server and immediately connect a client. | Audio begins within seconds (warming: releases near live edge). Subtitles follow once ASR has processed the first speech region (delay window). No errors, no crash. | | |
| 3 | **Subtitle timing** | Monitor audio playback position and note the wall time a phrase is heard. Record the `start` field of the corresponding SSE subtitle event. | `abs(subtitle_start_wall - audio_heard_wall) <= 1.5 s` for the majority of events. | | |
| 4 | **Toggle off/on (linger)** | Connect 1 client. Disconnect (Ctrl-C client). Wait 5 s. Reconnect immediately. | Reconnect resumes in under 2 s without restarting ffmpeg (stream continues in the linger window). Audio resumes quickly, no gap in subtitles beyond the linger period. | | |
| 5 | **Toggle off then cold restart** | Connect 1 client. Disconnect and wait >15 s (beyond 10 s linger). Reconnect. | Server starts a new ingest epoch. Audio begins again in warming phase. No crash. | | |
| 6 | **Stream-drop auto-recover** | Simulate a stream drop: block the HLS URL at the firewall or use a bad URL for 10 s then restore. | Ingestor reconnects with exponential backoff. Audio resumes without server restart. Log shows reconnect attempts. | | |
| 7 | **Two clients share one ingest** | Connect 2 clients simultaneously. Confirm `/v1/status` shows `"listeners": 2`. | Both clients hear the same audio (compare output). One ffmpeg subprocess is running (check `ps`). One ASR pass for both. | | |
| 8 | **Late joiner gets clean audio + synced subs** | Let client 1 run for 60 s. Connect client 2. | Client 2 starts at the broadcast head (not the beginning of the buffer). Audio starts immediately. Subtitles are aligned to client 2's audio position, not client 1's history. | | |
| 9 | **No crash over multi-minute session** | Run server + 1 client for at least 5 minutes. | No panic, no goroutine leak (check `/v1/status` listener count stays accurate), memory stable (rough check with `ps -o rss`). | | |
| 10 | **`/v1/status` accuracy** | Poll `GET /v1/status` with 0, 1, and 2 connected clients. | `listeners` matches actual client count. `state` transitions: `stopped` → `starting` → `warming` → `running`. `delaySeconds` matches `-delay` flag. `channel` matches `-channel-url`. | | |
| 11 | **Missing model-dir fails fast** | Start server without `-model-dir`. | Server prints a clear error to stderr and exits non-zero immediately (before binding the port). | | |
| 12 | **Bad model-dir fails fast** | Start server with `-model-dir /nonexistent`. | Server prints a clear ASR-init error to stderr and exits non-zero immediately. | | |
