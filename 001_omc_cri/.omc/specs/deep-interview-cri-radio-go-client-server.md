# Deep Interview Spec: CRI Radio Live-Subtitle Client/Server (Go)

## Metadata
- Interview ID: di-cri-radio-client-server-20260613
- Rounds: 8
- Final Ambiguity Score: 17%
- Type: brownfield (extends `github.com/timmyb32r/001_omc_cri`)
- Generated: 2026-06-13
- Threshold: 0.2 (20%)
- Threshold Source: default
- Initial Context Summarized: no
- Status: PASSED

## Clarity Breakdown
| Dimension | Score | Weight | Weighted |
|-----------|-------|--------|----------|
| Goal Clarity | 0.88 | 0.35 | 0.308 |
| Constraint Clarity | 0.85 | 0.25 | 0.213 |
| Success Criteria | 0.78 | 0.25 | 0.195 |
| Context Clarity | 0.78 | 0.15 | 0.117 |
| **Total Clarity** | | | **0.833** |
| **Ambiguity** | | | **0.167 (17%)** |

(Brownfield weights: Goal 35%, Constraints 25%, Criteria 25%, Context 15%.)

## Topology
| Component | Status | Description | Coverage / Deferral Note |
|-----------|--------|-------------|--------------------------|
| Radio Ingestion | active | Server pulls CRI HLS `905.m3u8` via ffmpeg (network) → decoded PCM windows | On-demand (ref-counted), single channel 905, auto-reconnect with backoff (Rounds 6, 8) |
| Transcription | active | VAD-segment buffered audio → sherpa/SenseVoice → timestamped subtitle lines | Extend `chineseasr` to VAD-segment + expose segment timestamps (Round 3) |
| Buffering / Delay & Timeline | active | Rolling 2–5 min buffer; shared delayed broadcast timeline; clients join at the delayed edge | Multi-minute buffer; configurable (Round 5) |
| REST API | active | Control (translation on/off) + data delivery (audio stream + subtitle SSE) | Server is timeline authority; paces two streams (Round 2) |
| Client console app | active | Lightweight Go console: toggle translation, pipe audio to ffplay, print synced subtitles | Shells out to ffplay/mpv (Round 1) |
| Enrichment & pronunciation-sync | **deferred** | Pinyin + English translation + word-level pronunciation-synced display | **Deferred to API-planned, not built in v1** (Round 0, user-confirmed) |

## Goal
Build a **Go client-server system with a REST API**, on top of the existing `chineseasr` library, that lets **multiple lightweight console clients** watch a live Chinese radio broadcast with **synchronized Chinese subtitles**.

When a client enables translation, it connects to the server's audio + subtitle streams for channel **905** (CRI 环球资讯广播, `https://sk.cri.cn/905.m3u8`, HLS / MPEG-TS). The **server** is the timeline authority: it ingests the HLS stream once via ffmpeg, maintains a rolling **2–5 minute buffer**, VAD-segments the buffered audio and transcribes each segment offline (SenseVoice) into **timestamped Chinese subtitle lines**, and then **paces two streams** to each connected client — a chunked-HTTP audio stream (which the client pipes to `ffplay`/`mpv`) and a parallel SSE/NDJSON stream of timestamped subtitle lines (which the client prints). Because the server already transcribed the buffered window, subtitles always lead the audio and stay in sync. Ingestion + transcription run **once** and are shared across all clients (thin clients need no model). The whole pipeline is **offline w.r.t. third parties except the radio source** (the only network egress is the CRI stream).

## Constraints
- **Languages/runtime:** Go for both server and client. Server reuses + extends `chineseasr` (ffmpeg + sherpa-onnx-offline / SenseVoice).
- **Client playback:** client shells out to `ffplay`/`mpv` (pipes the server's audio stream to it). Client carries **no** ASR model / sherpa dependency; it only needs a player binary. (Round 1)
- **Transport & sync:** server is the timeline authority. Audio = chunked HTTP stream; subtitles = parallel SSE/NDJSON timestamped lines; server paces subtitle delivery to the audio it sends. (Round 2)
- **Transcription:** VAD-segmented **offline** transcription; each speech segment → one subtitle line with start/end timestamps. Requires extending `chineseasr` to run VAD and expose `Result.Segments []Segment{Start, End, Text}` (sherpa already emits `timestamps`, currently dropped). (Round 3)
- **Architecture rationale:** thin clients + heavy server, **multi-client** sharing one ingest+transcription. ASR runs once regardless of client count. (Round 4)
- **Delay model:** rolling **2–5 minute buffer** (configurable); clients join at the delayed live edge. (Round 5)
- **Ingestion lifecycle:** **on-demand, single channel 905.** Ref-counted: ingestion+transcription starts when the first client enables translation, stops when the last disables. Channel URL is config, not client-selectable in v1. (Round 6)
- **Failure/edge handling (basic resilience):** auto-reconnect to the HLS stream with backoff; no-speech segments produce no subtitle lines (audio still flows); graceful client disconnect; ingestion stops when the last client leaves; unreachable 905 → clear API error surfaced by the client. (Round 8)
- **chineseasr offline guarantee preserved:** network radio ingestion is implemented in **new server code** (its own ffmpeg invocation that allows `https,tls,tcp` for `905.m3u8`), NOT by relaxing `chineseasr`'s file-only/offline audio layer. The library keeps transcribing local buffered WAV/PCM windows.

## Non-Goals (v1)
- Pinyin and English translation (deferred — design the subtitle schema/API to carry them later).
- Word-level pronunciation-synced display ("text appears with pronunciation") — deferred.
- In-client channel selection / multi-channel ingestion (single channel 905 only).
- Rewind/replay UI, persistence of audio/subtitles, authentication, rate limiting.
- Metrics/health dashboard, gap-filling, advanced observability.
- Hard real-time (sub-buffer) latency — the buffer delay is intentional.
- Windows as a primary target; auto-download of ffmpeg/sherpa/model (caller-provisioned, per `chineseasr`).

## Acceptance Criteria
**Automated (plumbing):**
- [ ] `chineseasr` extension: parsing a golden sherpa stdout that includes `timestamps` yields `Result.Segments` with correct `{Start, End, Text}`; the VAD-segmented path returns ≥1 segment for multi-utterance input (unit test with fixtures / fake runner).
- [ ] Ingestor: given a fake stream source, produces PCM windows; on injected stream drop it reconnects (backoff) and resumes (unit/integration test, no real network).
- [ ] Subtitle pacer: given timestamped segments + a fake broadcast clock, emits each subtitle line at the correct broadcast position (deterministic unit test).
- [ ] REST API: audio endpoint streams chunked audio with correct content-type; subtitle endpoint streams SSE/NDJSON timestamped lines; opening/closing connections toggles translation (connection = on/off).
- [ ] Ref-counted lifecycle: ingestion starts on first subscriber and stops on last (test with a fake ingestor; assert start/stop calls).
- [ ] Multi-client fan-out: two simulated clients receive the same broadcast content from one ingest; closing one keeps ingestion alive, closing both stops it.
- [ ] Client is lightweight: builds and runs without any ASR/model dependency; needs only a player binary (verified by dependency/build check).
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` pass.

**Recorded manual end-to-end (needs ffmpeg + sherpa + model + network):**
- [ ] Run server + 1 client against live `905.m3u8`: audio plays via ffplay AND correct Chinese subtitles appear roughly in sync (within the buffer delay), confirmed by a Chinese reader/listener.
- [ ] Toggling translation off/on works; stream-drop auto-recovers; no crashes over a multi-minute session.
- [ ] Two clients connected simultaneously share one broadcast (one ingest/ASR), both see synced subtitles.
- [ ] Result recorded (pass/fail per item) in a committed E2E checklist.

## Assumptions Exposed & Resolved
| Assumption | Challenge | Resolution |
|------------|-----------|------------|
| "Lightweight client plays the radio" implies trivial audio playback | Asked how audio is actually played in Go | Client **shells out to ffplay/mpv**; no audio lib (Round 1) |
| Audio+subtitles "just sent" — sync unspecified | Asked the transport+sync model | **Server paces 2 streams** (chunked audio + SSE subtitles); server is timeline authority (Round 2) |
| Subtitles come straight from the ASR | `chineseasr` returns text only, drops timestamps | **VAD-segmented offline** + extend lib to expose segment timestamps (Round 3) |
| Client-server + REST is required | **Contrarian:** what if it's one local program? | Confirmed essential: **thin clients + heavy server, multi-client**, ASR shared once (Round 4) |
| "Buffer some minutes / broadcast with delay" | Asked exact delay; SenseVoice is fast | **2–5 min rolling buffer**, clients join at delayed edge (Round 5) |
| Server "starts listening when translation on" | **Simplifier:** simplest scope? | **On-demand, ref-counted, single channel 905** (Round 6) |
| "Just works" | Asked the acceptance bar | **Auto plumbing tests + recorded manual E2E** (Round 7) |
| 24/7 source never fails | Asked failure/edge behavior | **Basic resilience** — reconnect w/ backoff, no-speech=no subs, graceful disconnect (Round 8) |

## Technical Context (brownfield)
- **Reused library:** `github.com/timmyb32r/001_omc_cri` — `Transcribe(ctx, audioPath) → Result{Text}` via ffmpeg + `sherpa-onnx-offline` (SenseVoice). `Result` is extensible; sherpa's per-segment `timestamps` are parsed-but-dropped today.
- **Required lib extension:** add VAD segmentation + `Result.Segments []Segment{Start, End float64; Text string}` (read sherpa's `timestamps`/per-segment output). Keep the offline/file-based contract; do NOT add network input to the library.
- **Radio source (verified):** `https://sk.cri.cn/905.m3u8` — live HLS, ~3s MPEG-TS segments, sliding `#EXT-X-MEDIA-SEQUENCE`, no `ENDLIST`. CRI 905 环球资讯广播. ffmpeg reads the `.m3u8` directly and can emit PCM (for ASR) and a client-facing streamable audio format (e.g., MP3) for ffplay.
- **Proposed new packages (same repo/module):** `cmd/server`, `cmd/client`, `internal/ingest` (network ffmpeg → PCM windows + reconnect), `internal/broadcast` (rolling buffer, shared timeline, pacer, ref-count), `internal/api` (REST: audio stream + subtitle SSE + control). Server depends on `chineseasr`; client depends on neither `chineseasr` nor sherpa.
- **Audio-to-client format:** server emits a streamable audio format ffplay reads from stdin (e.g., MP3 or pass-through) — exact codec is an implementation detail to confirm during planning.
- **External tools:** server needs ffmpeg + sherpa-onnx-offline + SenseVoice model (per `chineseasr`); client needs only ffplay/mpv. None are installed in the current environment, so full E2E is the user's manual gate.

## Ontology (Key Entities) — final round
| Entity | Type | Fields | Relationships |
|--------|------|--------|---------------|
| RadioStream | external system | url (905.m3u8), protocol (HLS), segments (MPEG-TS), channel | Ingestor pulls RadioStream |
| Ingestor | core | ffmpegPath, channelURL, reconnectBackoff | Produces PCM windows into Broadcast |
| Broadcast | core | channel, bufferMinutes(2–5), delay, position, subscribers, refCount | Shared delayed timeline; fans out to Clients |
| Transcriber | external/extended (chineseasr) | VAD, Result.Segments{Start,End,Text} | Consumes PCM windows → SubtitleEvents |
| AudioStream | core | codec (e.g. MP3), chunked-HTTP | Server → Client → ffplay |
| SubtitleEvent | core | textZh, start, end, [future: pinyin, english] | Server → Client (SSE/NDJSON) |
| TranslationSubscription | core | clientConn, channel, on/off (= connection lifecycle) | Increments/decrements Broadcast.refCount |
| Server | core | ingestor, broadcast, transcriber, api | Orchestrates all of the above |
| Client | core | playerCmd (ffplay), subtitlePrinter, apiBaseURL | Subscribes; plays audio; prints subtitles |

## Ontology Convergence
| Round | Entity Count | New | Changed | Stable | Stability Ratio |
|-------|-------------|-----|---------|--------|----------------|
| 1 | 8 | 8 | - | - | N/A |
| 2 | 8 | 0 | 1 | 7 | 100% |
| 3 | 8 | 0 | 1 | 7 | 100% |
| 4 | 9 | 1 (Broadcast) | 0 | 8 | 89% |
| 5 | 9 | 0 | 0 | 9 | 100% |
| 6 | 9 | 0 | 0 | 9 | 100% |
| 7 | 9 | 0 | 0 | 9 | 100% |
| 8 | 9 | 0 | 0 | 9 | 100% |

The domain stabilized early; the one structural addition was `Broadcast` (round 4) when the multi-client shared-timeline concept emerged. `Subtitle→SubtitleEvent` (+timestamps) and `TranslationSession→TranslationSubscription` were renames counted as stable.

## Interview Transcript
<details>
<summary>Full Q&A (8 rounds + Round 0)</summary>

### Round 0 — Topology
Confirmed 5 active components (Radio Ingestion, Transcription, Buffering/Delay, REST API, Client) + `Enrichment` deferred (pinyin/English + pronunciation-sync, API-planned).

### Round 1 — Client playback (Goal)
**Q:** How should the client play audio? **A:** Shell out to ffplay/mpv. **Ambiguity:** 64%

### Round 2 — Transport & sync (Constraints)
**Q:** How do audio + subtitles travel and stay synced? **A:** Server paces 2 streams (chunked audio + SSE subtitles); server is timeline authority. **Ambiguity:** 51%

### Round 3 — Transcription (Goal+Constraint)
**Q:** How to produce timestamped subtitle lines? **A:** VAD-segmented offline; extend chineseasr to expose timestamps. **Ambiguity:** 45%

### Round 4 — Contrarian (architecture)
**Q:** What is the server actually for / why not local-only? **A:** Thin clients + heavy server, multi-client sharing one ingest+ASR. **Ambiguity:** 40%

### Round 5 — Delay model (Constraints)
**Q:** How much delay + startup experience? **A:** Multi-minute buffer (2–5 min), join at delayed edge. **Ambiguity:** 37%

### Round 6 — Simplifier (ingestion scope)
**Q:** Simplest ingestion scope? **A:** On-demand, single channel 905, ref-counted. **Ambiguity:** 33%

### Round 7 — Success criteria
**Q:** How do we decide it works? **A:** Auto plumbing tests + recorded manual E2E. **Ambiguity:** 20%

### Round 8 — Edge handling (Constraints)
**Q:** Failure/edge behavior? **A:** Basic resilience (reconnect w/ backoff, no-speech=no subs, graceful disconnect). **Ambiguity:** 17% — PASSED

</details>
