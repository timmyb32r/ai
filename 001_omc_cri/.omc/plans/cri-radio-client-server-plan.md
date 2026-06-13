# Consensus Plan: CRI Radio Live-Subtitle Client/Server (Go)

- Source spec: `.omc/specs/deep-interview-cri-radio-go-client-server.md`
- Mode: `--consensus --direct` (RALPLAN-DR short), non-interactive
- Status: **pending approval** (consensus reached after Planner → Architect → Critic, iteration 2)
- Builds on existing module `github.com/timmyb32r/001_omc_cri`

## Requirements Summary
A Go server ingests the CRI 905 HLS stream once, keeps a rolling 2–5 min buffer, **segments the buffered audio at silence boundaries and transcribes each speech region offline** (SenseVoice) into timestamped Chinese subtitle lines, then fans out two paced streams to multiple thin Go console clients: a chunked-HTTP MP3 audio stream (piped to `ffplay`/`mpv`) and an SSE subtitle stream (printed). The server is the timeline authority and **paces MP3 release to real-time playback rate**; because the buffered window is transcribed before the broadcast head reaches it, subtitles are always *ready* before their audio is released. End-to-end A/V alignment is *approximate* (bounded by ffplay's buffer + stream jitter), not exact. Ingestion is on-demand (ref-counted), single channel 905. Pinyin/English/word-sync deferred but schema-reserved.

## Verified Substrate Facts (corrected after review — read first)
> These were verified against our own code/fixtures during consensus. The first draft got the segmentation premise wrong; this section is the corrected foundation.
- **sherpa-onnx-offline emits ONE transcript per wav.** `testdata/golden_sensevoice_stdout.txt` shows a single JSON object; its `timestamps` array is **per-token start times index-aligned with `tokens`**, NOT per-segment `{Start,End}`. `internal/engine/parse.go:92` deliberately `break`s after the first block. So "read sherpa's per-segment output" is **impossible** with the existing binary.
- **Segmentation therefore comes from ffmpeg `silencedetect`** (primary v1 path): detect silence intervals, derive speech regions, transcribe each region with the existing `Transcribe`. No new model, no new binary, reuses the proven one-block parser. (The `sherpa-onnx-vad-with-offline-asr` binary is a *future* quality upgrade with an unverified schema — explicitly NOT v1.)
- **`internal/runner/runner.go` is batch-only** (`Run` buffers stdout/stderr and returns on process exit, lines 27-37). It **cannot** drive a 24/7 streaming ingest. A new streaming subprocess seam is required in `internal/ingest`; `chineseasr`'s runner stays untouched.
- **chineseasr ffmpeg layer forbids network** (`-protocol_whitelist file,pipe`). Network ingestion lives only in new server code; the lib's offline guarantee is preserved.

## RALPLAN-DR Summary (short)

### Principles
1. **Server is the single timeline authority**; clients are dumb subscribers that play + print.
2. **Do the expensive work once** — one ingest, one encode, one ASR pass — then fan out shared bytes to N clients.
3. **Keep `chineseasr` offline/file-based**; all network I/O lives only in new server code.
4. **The buffer/delay guarantees *transcription readiness*, not A/V sync.** Subtitles are always computed before the broadcast head reaches their position. End-to-end audio/subtitle alignment at the listener is **approximate (target ≤ ±1.5 s)**, bounded by ffplay's playback buffer and independent-stream network jitter, which the server cannot observe.
5. **Thin client** — only HTTP + a player binary; no model, no sherpa, no Go audio library.

### Decision Drivers (top 3)
1. Subtitle↔audio **sync correctness** (the core UX) — accepting "approximate within tolerance."
2. **Multi-client efficiency** — ASR + encode happen once regardless of client count.
3. **Robustness of a 24/7 live source** — reconnect, backpressure, lifecycle races.

### Viable Options
- **Option A — single ingest → dual output (PCM for ASR + CBR MP3 for clients) → shared frame-indexed rolling buffer → broadcast head trails live by `delay` → real-time-paced HTTP MP3 + SSE subtitles fan-out (CHOSEN).**
  - Pros: encode/ASR once; shared broadcast; thin client; matches every spec decision.
  - Cons: bespoke concurrency (ring buffer + fan-out + ref-count lifecycle + pacing); A/V sync only approximate (see Principle 4).
- **Option B — per-client transcode/stream (REJECTED).** Pros: simpler, natural per-client clock + backpressure. Cons: N× ffmpeg + N× ASR defeats driver 2 and the spec's "ASR once, shared" (Round 4); still needs the same subtitle pacing layer, so it doesn't even remove the hard problem. **Invalidation:** violates driver 2.
- **Option C — mux subtitles into the media (WebVTT-in-HLS / MKV) so the player handles sync (REJECTED, but documented fallback).** Pros: **wins on driver 1** — the player's hardened A/V machinery makes alignment exact, deleting the dual-clock problem and making Principle 4 a true guarantee. Cons: violates the spec's **hard** constraint that the client is a *console app that prints subtitles* (Round 1, Non-Goals) and requires a subtitle-capable player (not a thin console). **Invalidation:** rejected on the spec's hard console-print + thin-client constraints, *with eyes open that it is superior on sync*. **If E2E sync proves unacceptable under Option A's tolerance, Option C is the documented fallback.**

## Architecture: the timeline & pacing model
```
live edge ──────────────────────────────────────────────► (wall clock)
   │ ingestHead (streaming ffmpeg writing newest PCM+MP3 into buffer)
   │     ◄─ silencedetect+ASR works here (behind ingest, ahead of broadcast) ─►
   │                                          │ broadcastHead = clock.Now() - delay
   ▼                                          ▼
[ rolling buffer: PCM(16k mono) | CBR-MP3 frame-indexed | subtitle segments — keyed by timeline t ]
                                              │ per-subscriber goroutine, bounded queue, REAL-TIME paced
                                              ├──► client A: GET /audio (MP3→ffplay, frame-aligned start) + GET /subtitles (SSE)
                                              └──► client B: same shared frame-aligned bytes
```
- One streaming ffmpeg pulls `905.m3u8` and emits **two outputs**: PCM s16le 16 kHz mono (ASR) and **CBR MP3** (clients). Both pipes are drained by dedicated goroutines into the buffer; neither consumer may block the other (else ffmpeg stalls → R-BP).
- `broadcastHead = clock.Now() - delay`. The per-subscriber writer releases **MP3 frames at real-time playback rate** (≈ frame-duration per frame; CBR makes this deterministic), gated so it never releases past `broadcastHead`, after a small bounded prebuffer (~1–2 s). This keeps ffplay's own buffer near-empty so its private clock can't drift far. **Pacing math:** `delay` must satisfy `delay > prebuffer + ffplay_buffer + ASR_worst_case_lag`; with `delay` = 2–5 min and ASR lag of seconds, this holds with large margin.
- **Honest invariant:** "all subscribers are released *identical, frame-aligned bytes from one shared buffer at one real-time rate*." (We do NOT claim all listeners hear the same instant — ffplay buffers differ and are unobservable.)
- Late joiners start at the **MP3 frame boundary ≤ `broadcastHead`** (frame-indexed buffer); a ≤1-frame bit-reservoir artifact at join is accepted.
- ASR runs on buffered PCM between `broadcastHead` and `ingestHead`; `delay` ≫ ASR lag so subtitles are ready. **If ASR ever falls behind within the window**, the affected audio is released with *no* subtitle for that gap (same as a no-speech gap) — never stall the broadcast (R8).

## Proposed Package Layout (same module)
```
chineseasr.go, errors.go, internal/{asrerr,runner,audio,engine}/   # EXISTING lib — extend ONLY additively
chineseasr.go                   # ADD: Segment type + TranscribeSegments(ctx, wavPath) ([]Segment, error)
internal/engine/silence.go      # NEW: parse ffmpeg silencedetect stderr -> []SpeechRegion  (+ test)
                                #   (internal/engine/parse.go:92 single-block break MUST stay unchanged — do-not-regress)
cmd/server/main.go              # flags: -addr, -ffmpeg, -sherpa, -model-dir, -channel-url, -delay, -buffer, -silence-db, -silence-min
cmd/client/main.go              # flags: -server, -player (default ffplay)
internal/ingest/ingest.go       # NEW streaming subprocess seam (NOT runner.Runner): HLS -> {PCM,MP3} pipes; reconnect w/ backoff
internal/broadcast/buffer.go    # rolling timeline buffer: PCM + frame-indexed MP3 + segments; eviction; thread-safe
internal/broadcast/clock.go     # injectable Clock interface (real + fake)
internal/broadcast/lifecycle.go # ref-count state machine: stopped|starting|running|stopping + linger/debounce
internal/broadcast/broadcast.go # broadcast head, subscriber registry, real-time pacer, fan-out
internal/broadcast/asr.go       # drives chineseasr.TranscribeSegments over silence-aligned regions; offsets timestamps
internal/api/api.go             # HTTP: GET /v1/stream/audio, GET /v1/stream/subtitles (SSE), GET /v1/status
README-radio.md, testdata/e2e-checklist.md
```

## Key Interfaces (target)
```go
// chineseasr (additive extension) — offline/file-based, reuses existing Transcribe + ParseText (no change to parse.go:92)
type Segment struct { Start, End float64; Text string } // seconds within the wav
// TranscribeSegments: silencedetect the wav -> speech regions -> for each region slice, run existing Transcribe -> Segment.
// Segments are silence-bounded (no mid-utterance cuts by construction).
func (t *Transcriber) TranscribeSegments(ctx context.Context, wavPath string) ([]Segment, error)

// internal/ingest — NEW streaming seam (the existing runner.Runner is batch-only and unusable here)
type Process interface { // fakeable: in-memory readers that can inject mid-stream EOF/error
    Start(ctx context.Context, name string, args []string) (pcm io.ReadCloser, mp3 io.ReadCloser, wait func() error, err error)
}
type Ingestor struct{ proc Process; ffmpegPath, channelURL string; backoff BackoffPolicy }
func (i *Ingestor) Run(ctx context.Context, onPCM, onMP3 func(ts float64, b []byte)) error // reconnects until ctx done

// internal/broadcast
type Clock interface { Now() time.Time }
type SubtitleEvent struct { Start, End float64; TextZh string /* future: Pinyin, English (schema-reserved) */ }
func (b *Broadcast) Subscribe() (audio <-chan []byte, subs <-chan SubtitleEvent, cancel func())
func (b *Broadcast) State() LifecycleState // stopped|warming|running|stopping
```

## REST API (v1)
| Endpoint | Method | Behavior |
|----------|--------|----------|
| `/v1/stream/audio` | GET | Chunked `audio/mpeg` CBR MP3 from the frame boundary ≤ `broadcastHead`, real-time paced. Connection-open = subscribe (incr ref-count → may start/`warming`); close = unsubscribe (decr → linger then stop). During `warming` (cold start), see policy below. |
| `/v1/stream/subtitles` | GET | `text/event-stream` (SSE) of `SubtitleEvent` JSON, released when `Start ≤ broadcastHead` (Start-anchored). Server **ignores `Last-Event-ID`** — a reconnecting client rejoins at the live head, never replays. |
| `/v1/status` | GET | JSON `{channel, listeners:int, delaySeconds:float, state:string, liveEdgeOffsetSeconds:float}` where `liveEdgeOffsetSeconds = ingestHead − broadcastHead`. |

"Translation on/off" = client opening/closing these streams; ingestion is ref-counted across the union of audio+subtitle subscribers. Unreachable 905 → audio handler returns HTTP 502 + the subtitle SSE emits an `error` event; the client surfaces a clear message.

**Cold-start / warming policy:** first subscriber on an empty buffer enters `warming`. v1 behavior: `/v1/status.state="warming"` and `liveEdgeOffsetSeconds` ramps from 0 toward `delay`; the audio stream serves **from `ingestHead` (near-live, minimal delay) during warming and ramps to the full `delay` as the buffer fills**, so the user hears audio within seconds (subtitles begin as soon as the first region is transcribed). The client prints a one-line "buffering…" until the first subtitle/audio arrives. (Chosen over "make the first user wait 2–5 min".)

## Implementation Steps
1. **Silence segmentation parser** — `internal/engine/silence.go`: `ParseSilence(stderr []byte) []SpeechRegion` from ffmpeg `silencedetect` stderr (`silence_start`/`silence_end`/`silence_duration`); derive speech regions as the complement. Unit-test against a **real** captured silencedetect stderr sample (stable documented format). (`silence.go`, `silence_test.go`)
2. **`chineseasr.TranscribeSegments`** — orchestrate: run `ffmpeg -af silencedetect` on the wav (via the existing offline ffmpeg path, file input only), parse regions, for each region slice (`-ss/-to`) call the existing `Transcribe`, assemble `[]Segment{Start,End,Text}`. Leave any trailing not-yet-silence-terminated speech unsegmented (caller re-processes next pass). Each region is transcribed via the existing public `Transcribe` (which internally uses `engine.ParseText` — keep `parse.go:92` unchanged). Note (efficiency, non-blocking): this runs ffmpeg twice per region (silencedetect pass + `Transcribe`'s own Convert); fine for short radio segments — a later optimization can slice PCM in-process to avoid the re-decode. Unit-test with a fake runner returning canned silencedetect stderr + canned SenseVoice stdout per slice (both real formats; no new model). (`chineseasr.go`, test)
3. **Streaming ingest seam** — `internal/ingest`: define `Process`; real impl owns `*exec.Cmd` with PCM on `pipe:1` (stdout) and MP3 on an `ExtraFiles` fd (`pipe:3`), args include `-protocol_whitelist file,http,https,tcp,tls,crypto` + dual `-map`; `Run` drains both pipes concurrently and reconnects with exponential backoff on EOF/error until ctx cancelled. Test reconnect with a fake `Process` that EOFs/errors mid-stream once then recovers; assert backoff invoked + bytes resumed. (`ingest.go`, test)
4. **Frame-indexed buffer** — `internal/broadcast/buffer.go`: thread-safe rolling buffer storing PCM, **MP3 with parsed frame boundaries** `(timelinePos → frameByteOffset)`, and subtitle segments; eviction past the `buffer` window (define max size; PCM ≈ 32 KB/s → ~9.6 MB/5 min). `-race` tests for append/read/evict + frame-boundary lookup. (`buffer.go`, test)
5. **Clock + lifecycle** — `clock.go` (real+fake); `lifecycle.go`: state machine `stopped→starting→(warming)→running→stopping`, single goroutine/mutex owning `{refCount,state}`; linger timer on last-unsubscribe (configurable, e.g. 10 s) so rapid reconnect doesn't thrash a multi-minute ingest; a new subscribe during `stopping` cancels teardown. Deterministic test: unsubscribe→immediate-resubscribe ⇒ exactly one ingest lifecycle. (`clock.go`, `lifecycle.go`, tests)
6. **Broadcast pacer + fan-out** — `broadcast.go`: maintain `broadcastHead = clock.Now()-delay`; per-subscriber goroutine with a **bounded** queue (drop-oldest, or disconnect a hopeless laggard — never block the shared writer); release MP3 frames at real-time rate gated by `broadcastHead`; release subtitles Start-anchored (`Start ≤ head`). Cold-start ramp per policy. `-race` tests with fake clock + fake ingestor: pacing ("none released early"), fan-out (identical bytes to 2 subs), backpressure isolation (one slow sub doesn't stall another), warming behavior. (`broadcast.go`, test)
7. **ASR driver** — `asr.go`: take the un-transcribed PCM region (lastCursor → ingestHead), write a temp WAV, call `chineseasr.TranscribeSegments`, offset segment times to the broadcast timeline, store segments, advance cursor to the end of the last **silence-bounded** segment (leave trailing speech for next pass — avoids mid-utterance cuts). If ASR lags, audio still flows with no subtitle for the gap. Test with a fake transcriber + fake clock. (`asr.go`, test)
8. **REST API** — `internal/api/api.go`: three endpoints; SSE writer with `Flusher` (ignore `Last-Event-ID`); chunked MP3 writer starting at a frame boundary; subscribe/unsubscribe → lifecycle ref-count; 502 + SSE `error` on unreachable source. `httptest` tests: content-types, streaming, ref-count transitions, error path, warming status. (`api.go`, test)
9. **Server entrypoint** — `cmd/server/main.go`: wire flags → ingest + broadcast + api; graceful shutdown drains subscribers + stops ffmpeg. (`cmd/server/main.go`)
10. **Client** — `cmd/client/main.go`: GET `/v1/stream/audio` piped to `ffplay -nodisp -autoexit -i -` (stdin); concurrently consume `/v1/stream/subtitles` SSE and print each line with its timestamp; surface server errors/`error` events; clean Ctrl-C. Imports neither `chineseasr` nor sherpa. (`cmd/client/main.go`)
11. **Docs + E2E checklist** — `README-radio.md` (install: server needs ffmpeg+sherpa+SenseVoice model; client needs only ffplay; offline-scope note: only egress = the radio; ffplay buffer-tuning flags; the ±1.5 s skew tolerance) and `testdata/e2e-checklist.md` (recorded manual acceptance incl. a **late-joiner** item). (`README-radio.md`, `testdata/e2e-checklist.md`)

## Acceptance Criteria (testable)
**Automated (NO ffmpeg/sherpa/network — fakes + real-format fixtures):**
- [ ] `ParseSilence` turns a real captured `silencedetect` stderr sample into the correct `[]SpeechRegion` (complement of silence) — unit test.
- [ ] `TranscribeSegments` (fake runner: canned silencedetect stderr + canned SenseVoice stdout per slice) returns `[]Segment{Start,End,Text}` with correct region bounds + text incl. punctuation; segments are silence-bounded; existing `Transcribe` and `parse.go:92` behavior unchanged (assert by reusing existing parser tests).
- [ ] Ingestor: builds the expected dual-output, network-whitelisted ffmpeg arg list (auto); and with a fake `Process` that errors mid-stream once then recovers, reconnects with backoff and resumes (auto). (The claim "dual output actually streams from real ffmpeg" is scoped to the **manual E2E gate**, not this test.)
- [ ] Frame-indexed buffer: append/read by position, eviction past window, and frame-boundary lookup are correct; passes `-race` under concurrent append/read.
- [ ] Lifecycle: first subscriber → starting/warming; last unsubscribe → linger → stopped; unsubscribe→immediate-resubscribe ⇒ exactly one ingest lifecycle; 2 concurrent subs ⇒ ingest started once, ref-count 2; `-race`.
- [ ] Pacer (fake clock): no MP3 frame or subtitle released with position > `broadcastHead`; advancing the fake clock releases more; subtitles are Start-anchored; **none early** (now testable — release rule is pinned).
- [ ] Fan-out + backpressure: two subscribers get identical frame-aligned bytes; a deliberately slow subscriber is dropped/bounded and does NOT stall the other (`-race`).
- [ ] Warming: first subscriber on an empty buffer gets audio from near-live and `status.state` reports `warming` then `running` (fake clock).
- [ ] API (`httptest`): `/audio` → `audio/mpeg` streaming from a frame boundary; `/subtitles` → `text/event-stream` SSE events; `/status` fields present + typed; unreachable-source → 502 + SSE error; subscribe/unsubscribe moves ref-count.
- [ ] Client dependency check: `go list -deps ./cmd/client` imports **neither** `chineseasr` nor any sherpa package.
- [ ] Offline boundary: `go list -deps` for `internal/audio`, `internal/engine`, AND `internal/broadcast/asr.go`'s package shows no `net` import (the lib + ASR bridge stay offline; only `internal/ingest`/`internal/api`/`cmd` touch the network).
- [ ] `CGO_ENABLED=0 go build ./...`, `go vet ./...`, `go test -race ./...` pass; `gofmt -l` clean.

**Recorded manual E2E (needs ffmpeg + sherpa + SenseVoice model + network):**
- [ ] Server + 1 client on live `905.m3u8`: audio plays via ffplay AND correct Chinese subtitles appear within **±1.5 s** of the corresponding speech (Chinese reader confirms); recorded pass/fail.
- [ ] Cold start: first client hears audio within seconds (warming) and subtitles begin shortly after.
- [ ] Toggle off/on works; stream drop auto-recovers; no crashes over a multi-minute session.
- [ ] Two clients simultaneously share one broadcast (one ingest/ASR); a **late joiner** gets clean audio after ≤1 frame and synced subtitles.

## Risks and Mitigations
- **R1 Concurrency (fan-out/buffer/lifecycle) races/deadlocks/leaks.** Single-writer broadcast goroutine + per-sub channels; one lock/goroutine owns `{refCount,state}`; `-race` across buffer/broadcast/lifecycle.
- **R2 Segmentation quality (silencedetect thresholds on Chinese radio).** `-silence-db`/`-silence-min` are tunable flags; segments are silence-bounded so no mid-utterance cuts; document tuning; VAD-binary path is a future upgrade. (Replaces the old "VAD window-boundary" risk.)
- **R3 Audio↔subtitle drift (MP3 frame quantization + encoder delay vs PCM/VAD timeline).** CBR MP3 (deterministic frame duration); index MP3 by frame boundary; derive subtitle times from the PCM timeline; state the fixed encoder-delay offset; accept ≤ ±1.5 s (radio doesn't need frame accuracy).
- **R4 Slow client backpressure stalls the broadcast.** Per-sub bounded queue, drop-oldest or disconnect; never block the shared writer; tested.
- **R-BP ffmpeg dual-output pipe backpressure.** Both pipes drained by dedicated goroutines into bounded buffers; a stalled ASR-reader must not block the MP3 fan-out drain (and vice versa) or ffmpeg blocks and ingest dies for all; tested via fake `Process`.
- **R5 HLS reconnect quirks (gaps/discontinuities).** ffmpeg handles HLS; reconnect with backoff on process exit; gaps → silence → no subtitles (acceptable).
- **R6 ffplay-from-stdin behavior / startup buffer (the dual-clock skew).** Real-time pacing + bounded prebuffer keep ffplay's buffer small; tune `ffplay -nodisp -autoexit -fflags nobuffer -probesize 32k`; E2E measures skew against ±1.5 s.
- **R7 chineseasr offline regression.** Network only in `internal/ingest`/`api`/`cmd`; lib keeps `-protocol_whitelist file,pipe`; tests assert no `net` import in lib + ASR-bridge packages.
- **R8 ASR falls behind real-time within the delay window.** `delay` (2–5 min) ≫ ASR lag gives large margin; if it still happens, release audio with no subtitle for the gap (never stall); covered by the cursor logic in Step 7.
- **R9 Cold start (empty buffer, no delayed edge).** Warming policy: serve near-live + ramp; status reports warming; tested.

## Verification Steps
1. `CGO_ENABLED=0 go build ./...`, `go vet ./...`, `gofmt -l .` — clean.
2. `go test -race -count=1 ./...` — all unit/integration tests pass (no external binaries/network).
3. Dependency checks: `go list -deps ./cmd/client` shows no `chineseasr`/sherpa; `go list -deps` for `internal/audio`, `internal/engine`, `internal/broadcast` (asr) shows no `net`.
4. Recorded manual E2E per `testdata/e2e-checklist.md` with real ffmpeg/sherpa/model against live 905 (user environment), including the ±1.5 s skew, cold-start, and late-joiner items.

## ADR
- **Decision:** Single-ingest, dual-output (PCM+CBR-MP3), shared frame-indexed rolling buffer; **silencedetect-based segmentation + per-region offline `Transcribe`**; real-time-paced MP3 + Start-anchored SSE subtitles fanned out to thin ffplay-based console clients; ref-counted on-demand ingestion with a lifecycle state machine + warming.
- **Drivers:** sync correctness (approximate, tolerance-bounded), multi-client efficiency (work once), 24/7 robustness.
- **Alternatives considered:** (B) per-client transcode — rejected (defeats work-once, doesn't remove the pacing problem); (C) muxed subtitles — **superior on sync** and would make Principle 4 a true guarantee, rejected only on the spec's hard console-print + thin-client constraints, **named as the fallback if E2E sync fails tolerance**; (VAD-binary segmentation) — deferred (unverified schema, extra asset) in favor of silencedetect.
- **Why chosen:** honors every spec decision (thin console client prints subtitles, ASR once, server-paced, buffer/delay) while resting only on *verified* substrate (existing Transcribe + silencedetect + a new streaming seam).
- **Consequences:** A/V sync is approximate (≤±1.5 s), not exact; bespoke concurrency to get right (pacing, fan-out, lifecycle); silencedetect segmentation quality depends on tunable thresholds.
- **Follow-ups:** VAD-binary segmentation for better boundaries; pinyin/English in `SubtitleEvent`; word-level pronunciation-sync; multi-channel; rewind/persistence; if sync is insufficient, pivot to Option C (muxed/WebVTT).

## Changelog (consensus improvements applied in iteration 2)
- **[CRITICAL]** Corrected the false "sherpa emits per-segment timestamps" premise; **switched segmentation to ffmpeg `silencedetect` + per-region `Transcribe`** (no new binary/model; genuinely offline-testable; real fixtures). (Architect rec 2/6; Critic C1/C2/C3-fixture, req 1/2/3)
- **[CRITICAL]** Reworded Principle #4 from "sync guarantee" to "transcription readiness; A/V alignment approximate ≤±1.5 s"; added pacing math + ffplay buffer caveat + E2E tolerance. (Architect rec 1; Critic C3, req 4)
- **[MAJOR]** Added a **streaming ingest seam** (`internal/ingest.Process`), explicitly noting `runner.Runner` is batch-only and unusable for streaming. (Architect rec 3; Critic req 8)
- **[MAJOR]** Specified **real-time MP3 pacing** + bounded prebuffer + honest "identical shared bytes" invariant. (Architect rec 1; Critic C3/M4)
- **[MAJOR]** **Frame-indexed MP3 buffer** + frame-aligned late-join (CBR). (Architect rec 4; Critic M3)
- **[MAJOR]** **Cold-start/warming policy** + test. (Architect rec 5; Critic M1)
- **[MAJOR]** **Lifecycle state machine** + linger/debounce + resubscribe-during-stopping test. (Architect rec 5; Critic M2)
- **[MAJOR]** **Silence-aligned segment finalization** (no fixed windows; leave trailing speech). (Architect rec 6; Critic R2 ambiguity)
- **[MAJOR]** ffmpeg dual-output **backpressure** (R-BP). (Architect rec 7)
- **[MAJOR]** Re-argued **Option C honestly** (wins on sync; rejected on hard constraints; documented fallback). (Critic M5, req 10)
- Pinned subtitle release rule (Start-anchored) so "none early" is testable (Critic req 9); added **ASR-behind** behavior R8 (Critic req 11); SSE ignores `Last-Event-ID` (Architect rec 8); defined `/status.liveEdgeOffsetSeconds` (Critic m4); assert no `net` in the ASR bridge (Critic m3); do-not-regress anchor on `parse.go:92` (Critic m1); split "builds args" (auto) vs "dual output streams" (E2E) acceptance (Critic).

## Residual decisions (confirm in implementation, non-blocking)
- `-delay` ships a concrete default of **180 s (3 min)** within the 2–5 min range; tunable.
- MP3 encoder-delay magnitude (R3) is measured at E2E and recorded; it is ~tens of ms, far under the ±1.5 s tolerance.
- silencedetect thresholds (`-silence-db`, `-silence-min`) tuned at E2E against real Chinese radio.
- Client codec = CBR MP3 (assumed); confirm ffplay-from-stdin behavior + buffer flags at E2E; pass-through TS/AAC is a fallback.
- Module path `github.com/timmyb32r/001_omc_cri` placeholder unchanged.
