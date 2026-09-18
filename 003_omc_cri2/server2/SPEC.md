# CRI server2: approved specification

Approved 2026-09-18. New implementation beside server; no migration of old recordings.

- Deployment: 4 vCPU, 8 GB RAM, 113 GB disk, up to five clients. Docker Compose; only Go HTTP port public. Internal ASR/HanLP services, bounded CPU/RAM/PIDs/connections and rotated logs.
- Keep SenseVoice 2024 (sherpa-onnx), Chinese processing, dictionaries and pinyin quality. One resident model instance per service, bounded sequential inference, PCM in memory. Never manufacture successful subtitles on inference failure.
- Publish audio only with completed ASR and enrichment. Genuine silence is valid. Target live delay 180 seconds. Pending work retains at most 300 seconds of input; drop oldest unfinished intervals on overflow, reject late results, continue automatically with an explicit timeline discontinuity.
- Retain three hours of completed audio/metadata across restarts, with durable nonreused integer IDs. Best-effort retention under disk pressure: initial recording limit 10 GiB and free-space reserve 10 GiB. Stop writing when cleanup cannot recover; retry automatically. Keep pinned downloads intact until expiry.
- Preserve existing HTTP routes, response fields, numeric TS names and epoch timestamps. Actual media duration, metadata and word timestamps must agree. Minimal Android updates handle gaps and explicit download failure.
- Stable offline snapshots last ten minutes, at most five at once. Stable membership/order/count across pagination and cleanup. Download success requires every advertised audio segment; always release snapshot after finishing.
- Verify protocol compatibility, real inference, media alignment, crash/restart behavior, upstream loss, model hangs, low disk, slow clients and concurrent download/cleanup. Record measured results; do not infer leak freedom from raised ulimits.

## Implementation ownership

Go coordinator owns process lifetimes, PCM ring and inference queue. A single ffmpeg generation produces PCM and private HLS; file completion precedes model work and atomic publication. Storage owns SQLite metadata, public audio, snapshots and reclamation. ASR owns native model lifecycle; its watchdog exits on hung inference so the container restarts. HanLP owns its resident tokenizer. Client owns mapping playlist media positions to real timestamps across discontinuities.

## Acceptance ledger

Implementation and fresh verification results are recorded in VALIDATION.md before delivery. Production deployment is separate; no live server data is modified by local tests.
