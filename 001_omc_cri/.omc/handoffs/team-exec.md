## Handoff: team-exec → team-verify (CRI radio)

- **Built**: full client/server on the existing module. New: cmd/server, cmd/client, internal/{ingest,api}, internal/broadcast/{clock,types,buffer,lifecycle,broadcast,asr}, chineseasr.TranscribeSegments + internal/engine/silence.go. Segmentation = ffmpeg silencedetect + per-region existing Transcribe (no new model/binary). Streaming ingest seam (NOT the batch runner). Server-paced via clock-driven broadcastHead.
- **Mechanical gates (lead-run)**: gofmt clean; CGO_ENABLED=0 build OK; vet OK; `go test -race ./...` all pass (93 test funcs); client imports no chineseasr/sherpa; offline pkgs (audio/engine/broadcast) import no net; network only in ingest/api/cmd; parse.go:92 unchanged.
- **Key design notes / risks for verify**:
  - broadcast.go NewBroadcast REBUILDS the lifecycle with its own start/stop hooks (passed-in Lifecycle used only for linger); server constructs it with nil hooks. Confirm no double-start.
  - Pacer: clock-driven broadcastHead = bufferHead - delay (Running) / live edge (Warming); per-subscriber bounded chans (audio 32 / subs 16) with drop-oldest; subtitles Start-anchored.
  - Warming->Running flip is monotonic (curPos only moves forward) — verify no backward jump / re-serve.
  - ASR: fake-injectable segmenter seam; wraps PCM into a temp WAV; advances cursor only past finalized (End!=0) segments; errors logged, never crash broadcast.
  - Integration tests use fakes/fixtures only (no ffmpeg/sherpa/network); full E2E is the user's manual gate.
- **Remaining for verify**: independent concurrency-correctness + spec-compliance code review; produce defect list for team-fix if any.
