# FFmpeg HLS Ingest — Debug Knowledge

## Critical: `-reconnect_at_eof` breaks HLS live streams

**DO NOT add `-reconnect_at_eof 1` to FFmpeg args for HLS ingest.**

### Why

HLS `.m3u8` playlists are short HTTP responses that always end with EOF.
`-reconnect_at_eof 1` makes FFmpeg reconnect on **every** EOF, including the
playlist fetch. This causes an infinite loop:

1. FFmpeg downloads the playlist
2. Playlist response ends → EOF
3. `-reconnect_at_eof 1` triggers HTTP-level reconnect
4. FFmpeg re-downloads playlist → EOF → reconnect → …

The HLS demuxer never gets a chance to download `.ts` segments.
Logs fill with `Skip ('#EXTM3U')`, `Will reconnect at 296 in 0 second(s), error=End of file`.

**The HLS demuxer handles playlist refresh internally.** It does NOT need
HTTP-level reconnect on EOF.

### Correct flags (in `internal/ingest/ffmpeg_ingest.go`)

```
-reconnect 1              # reconnect on real connection drops (good)
-reconnect_delay_max 5    # max 5s between reconnects (good)
-rw_timeout 10000000      # 10s per-read timeout, microseconds (good)
```

### Banned flags

```
-reconnect_at_eof 1       # BREAKS HLS — infinite reconnect on playlist EOF
-reconnect_streamed 1     # BREAKS HLS — treats streamed content EOF as reconnect
```

## CRI CDN behavior (`sk.cri.cn`)

- Server: Tencent Cloud Live (`MC_VCLOUD_LIVE`)
- TLS: TLSv1.3, any User-Agent works (curl, FFmpeg `Lavf/*`, browser)
- No geo-blocking — accessible from Russia and worldwide
- `.ts` segment URLs contain `txspiseq=` token — valid only briefly (live stream)
- Segment lifetime: ~3-4 seconds (TARGETDURATION:4)

## Verifying FFmpeg manually

```bash
# Inside Docker container:
ffmpeg -v trace -i 'https://sk.cri.cn/905.m3u8' -t 5 -f null /dev/null 2>&1 | grep -E 'GET |HTTP/1|error'

# Download a fresh segment (token expires fast, must be immediate):
SEG=$(curl -s 'https://sk.cri.cn/905.m3u8' | grep '\.ts' | head -1)
curl -v "https://sk.cri.cn/$SEG" -o /tmp/test.ts
```

## Existing diagnostic features

1. **Playlist dump** at startup (`dumpPlaylist()` in `ffmpeg_ingest.go`) — logs first 50 lines of `.m3u8`
2. **FFmpeg command log** — logs the full `ffmpeg` command line before execution
3. **`HTTP_HEADERS` env var** — custom HTTP headers passed via `-headers`
4. **`FFMPEG_EXTRA_ARGS` env var** — extra ffmpeg args inserted before `-i` (space/comma-separated)

## Files changed (2026-06-30)

- `internal/ingest/ffmpeg_ingest.go` — reconnect fix, playlist dump, command logging, HTTP headers, extra args
- `internal/ingest/ingest.go` — `FFmpegExtraArgs`, `HTTPHeaders` in Config
- `internal/config/config.go` — `HTTP_HEADERS`, `FFMPEG_EXTRA_ARGS` env vars
- `cmd/server/main.go` — pass new config fields to ingest

## Problem history

| Symptom | Root Cause | Fix |
|---------|-----------|-----|
| `End of file` spam, 0 segments ingested | `-reconnect_at_eof 1` caused infinite playlist reconnect | Removed flag |
| Could not see what playlist contained | No diagnostic output | Added `dumpPlaylist()` |
| Could not see what FFmpeg was really running | No command logging | Added cmd log |
| No way to pass headers/args without code change | Missing config | Added `HTTP_HEADERS`, `FFMPEG_EXTRA_ARGS` |
