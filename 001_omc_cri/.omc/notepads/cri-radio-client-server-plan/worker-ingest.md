# worker-ingest (#15) — internal/ingest

## Scope (owned)
- `internal/ingest/ingest.go` — real Process impl + Run/backoff (frozen exported forms unchanged).
- `internal/ingest/ingest_test.go` — NEW, fake Process, no real ffmpeg/network.

## Decisions / learnings
- **Frozen forms preserved**: `Process`, `Ingestor`, `New`, `Run` signatures untouched. Added
  unexported fields to `Ingestor` (`backoff backoffPolicy`, `now func() time.Time`,
  `sleep func(ctx, d)`) — these are NOT exported, so the frozen API is intact; tests set them directly
  (same package).
- **Real Process = `ExecProcess`**: `exec.CommandContext` so ctx-cancel kills the child. PCM on
  `cmd.StdoutPipe()` (pipe:1). MP3 on fd 3 via `os.Pipe()` — write end goes in
  `cmd.ExtraFiles[0]` (child fd 3 = `pipe:3`), read end returned as the mp3 reader. CRITICAL:
  close the parent's copy of the write end AFTER `cmd.Start()` or the parent reader never sees EOF.
  stderr captured to a bounded `tailBuffer` (last 4 KiB) for error reporting in `wait()`.
- **Backoff injectable** via `backoffPolicy` interface (`Next()`/`Reset()`); prod = `expBackoff`
  0.5s,1s,2s,4s...cap 30s; reset after a run >= `minHealthyRun` (5s). Tests inject `countingBackoff`
  returning 1µs. `now`/`sleep` also injectable for determinism.
- **Concurrent drain**: two goroutines + `sync.WaitGroup`; each owns one reader and Closes it.
  Neither blocks the other. Loop reconnects on wait()/EOF/error until ctx done; returns ctx.Err().

## MP3 timestamp convention (documented in Run godoc)
Both clocks are byte-derived and persist across reconnects (continuous timeline, NOT reset on drop):
- PCM: `tsSec = totalPCMBytes / (16000*2)`  (s16le 16 kHz mono = 32000 B/s)
- MP3: `tsSec = totalMP3Bytes / (64000/8)`  (CBR 64 kbit/s = 8000 B/s)
Both are CBR off the same source audio, so the two byte-clocks track the same wall time; the MP3 ts
is best-effort aligned to the PCM clock by construction. `tsSec` is the position at the START of the chunk.

## ffmpeg arg list (buildArgs)
`-hide_banner -nostdin -protocol_whitelist file,http,https,tcp,tls,crypto -i <URL>
 -map 0:a -ar 16000 -ac 1 -c:a pcm_s16le -f s16le pipe:1
 -map 0:a -c:a libmp3lame -b:a 64k -f mp3 pipe:3`

## Verification
`go test -race ./internal/ingest/` ok; `go vet ./internal/ingest/` clean; gofmt clean;
`CGO_ENABLED=0 go build ./...` ok. 5 tests pass under -race.
