# Consensus Plan: Local Chinese Speech-Recognition Go Library

- Source spec: `.omc/specs/deep-interview-go-chinese-asr-library.md`
- Mode: `--consensus --direct` (RALPLAN-DR short), non-interactive
- Status: **pending approval** (consensus reached after Planner → Architect → Critic, iteration 2)

## Requirements Summary
A reusable Go library that transcribes Chinese-speech audio files to **Simplified-Chinese text with punctuation**, fully offline, by shelling out to `ffmpeg` (decode/resample → 16 kHz mono WAV) and the `sherpa-onnx-offline` CLI (ASR, default model SenseVoice-Small, pluggable). Paths to the external tools and the model directory are caller-provided config parameters. Targets Linux + macOS. Ships install/usage docs.

## RALPLAN-DR Summary (short)

### Principles
1. **Subprocess isolation** — no CGo/native linking; the library stays pure-Go-buildable (`CGO_ENABLED=0`) and drives external binaries via `os/exec`.
2. **Caller-provided dependencies** — no downloads, no bundling; explicit paths in config; validate early and fail with actionable errors that surface the tool's own stderr.
3. **Offline by construction** — zero network at runtime, scoped to the Go layer **and** the inputs: reject non-local inputs and restrict ffmpeg to file protocols so a subprocess cannot fetch remote data.
4. **Extensible-but-minimal output** — v1 returns text inside a `Result` struct that can grow (segments/metadata) without breaking callers.
5. **Pluggable model behind one boundary** — model choice is config; the engine layer builds model-specific CLI args; advertise only wired models, gate the rest with a clear error.

### Decision Drivers (top 3)
1. Best offline Mandarin accuracy with the least Go-side complexity.
2. Easy to swap/upgrade models as SOTA moves (SenseVoice → Paraformer/Whisper/…).
3. Robustness of the subprocess boundary: output parsing, error surfacing, context cancellation, temp-file hygiene.

### Viable Options
- **Option A — subprocess to `sherpa-onnx-offline` + `ffmpeg` (CHOSEN).**
  - Pros: simplest Go build (no CGo), trivial engine/model swap, matches the interview decision, fault isolation (an onnxruntime crash doesn't take down the caller).
  - Cons: stdout-contract coupling (parse JSON), per-call process spawn + model-load overhead, depends on external binaries at configured paths, failure surface moves to the user's runtime.
- **Option B — in-process `sherpa-onnx-go` CGo bindings (REJECTED).**
  - Pros: faster (recognizer loaded once, reused), typed result (no parsing), single self-contained binary; **prebuilt libs ship for Linux+macOS so no C toolchain is actually required** (the original rejection reason was weaker than assumed — see ADR).
  - Cons: CGo enabled; cross-compile friction; tighter compile-time coupling. **Invalidation:** explicitly considered and declined by the user in deep-interview Round 4 (chose looser subprocess coupling). Honored as a firm constraint, not a technical verdict.
- **Option C — pure-Go ONNX inference (REJECTED).**
  - Pros: no external deps, single binary.
  - Cons: immature/slow runtimes, large porting effort, worse accuracy. **Invalidation:** "pure Go" goal explicitly dropped by the user (Round 2 + clarification).

## Verified Tool Facts (source of truth for implementation)
> These were verified against the sherpa-onnx CLI driver source and SenseVoice docs during consensus review. **Reviewers disagreed on stream layout** (stdout-only JSON vs. JSON-among-noise), so the parser is designed to tolerate both and the executor MUST pin the truth with a real captured fixture (see Open Questions).

- `sherpa-onnx-offline` prints, **per input wav**, a JSON object containing a `text` field (plus `lang`, `emotion`, `event`, `timestamps`, `tokens`, `words`). The transcript is `.text`.
- Status/config/diagnostic output (`OfflineRecognizerConfig(...)` dump, `Started`/`Done!`, filename echo, `----` separators, RTF summary) is verbose. **stdout and stderr MUST be captured separately**; never `CombinedOutput()`.
- Flags accept both `--flag=value` and `--flag value`; wav paths are trailing positional args. The Kaldi-style parser is used.
- **SenseVoice emits punctuation ONLY with `--sense-voice-use-itn=1` (defaults OFF).** Omitting it produces unpunctuated text → spec violation. `--debug=0` keeps output predictable.
- `ffmpeg` to 16 kHz mono PCM s16le WAV: `-ar 16000 -ac 1 -c:a pcm_s16le -f wav`; add `-vn` (drop cover-art/video streams) and `-protocol_whitelist file,pipe` (offline hardening).

## Proposed Package Layout
```
go.mod                          // module github.com/timmyb32r/001_omc_cri (placeholder; user sets real path)
chineseasr.go                   // public API: Config, Transcriber, Result, Model, New, Probe, Transcribe
errors.go                       // sentinel errors
runner.go                       // unexported runner interface (exec seam for unit tests)
internal/audio/ffmpeg.go        // ffmpeg subprocess: local input -> 16kHz mono s16le WAV (temp file)
internal/audio/ffmpeg_test.go
internal/engine/args.go         // model -> []string flag mapping (SenseVoice/Paraformer/Whisper)
internal/engine/args_test.go    // arg-builder tests (asserts ITN, debug, language, per-model flags)
internal/engine/sherpa.go       // builds + runs the sherpa-onnx-offline command via the runner
internal/engine/parse.go        // stdout -> recognized text (JSON-line scan, stream-layout tolerant)
internal/engine/parse_test.go   // golden-fixture parsing tests (real captured stdout)
chineseasr_test.go              // config validation + Transcribe-with-fake-runner + guarded integration
examples/cli/main.go            // optional thin demo CLI (not the product)
testdata/
  sample_zh.wav                 // tiny real Chinese clip (16k mono)
  golden_sensevoice_stdout.txt  // REAL captured stdout, version-commented
  reference_transcripts.md      // >=3 clips, human-confirmed expected transcripts + sign-off
  bad_input.bin                 // non-audio file for decode-failure test
README.md                       // install + usage docs (paths, tools, macOS/Linux install, offline scope)
```

## Public API (target shape)
```go
package chineseasr

type Model string
const (
    ModelSenseVoice Model = "sense-voice" // default, integration-verified for v1
    ModelParaformer Model = "paraformer"  // wired arg-builder, accuracy not eyeball-verified in v1
    ModelWhisper    Model = "whisper"      // wired arg-builder, accuracy not eyeball-verified in v1
)
// (FireRedASR / Qwen3-ASR are future; not exported until wired + verified.)

type Config struct {
    FFmpegPath        string // path to ffmpeg binary (required)
    SherpaOfflinePath string // path to sherpa-onnx-offline binary (required)
    ModelDir          string // dir with model file(s) + tokens.txt (required)
    Model             Model  // default ModelSenseVoice
    Language          string // default "zh" (SenseVoice: zh/yue/en/ja/ko/auto)
    Punctuation       bool   // default true -> maps to --sense-voice-use-itn=1
    NumThreads        int    // default 2
    TempDir           string // optional; default os.TempDir()
}

type Result struct {
    Text string // recognized Simplified-Chinese text WITH punctuation (v1)
    // future (non-breaking): Segments []Segment, DetectedLanguage string, ...
}

func New(cfg Config) (*Transcriber, error)                          // fast: validates paths + model files exist; applies defaults
func (t *Transcriber) Probe(ctx context.Context) error             // runs sherpa once on testdata wav; asserts JSON schema (version drift guard)
func (t *Transcriber) Transcribe(ctx context.Context, audioPath string) (*Result, error)
```
> Note: a passing `New` does **not** guarantee a working pipeline (it only checks paths). `Probe` is the recommended startup check; the real contract is `Transcribe`.

## Implementation Steps

1. **Module + scaffolding** — `go mod init`; create files above; Go 1.22+. (`go.mod`, `chineseasr.go`)
2. **runner seam** — `runner.go`: `type runner interface { run(ctx context.Context, name string, args []string) (stdout, stderr []byte, exitErr error) }`; default impl wraps `exec.CommandContext` capturing stdout/stderr **separately**. `Transcriber` holds a `runner` (defaults to real; tests inject a fake). (`runner.go`)
3. **Config + validation (`New`)** — verify `FFmpegPath`/`SherpaOfflinePath` are existing files and `ModelDir` contains the required model + `tokens.txt` for the selected `Model`; reject unwired models with `ErrModelNotImplemented`; apply defaults (`Model=sense-voice`, `Language=zh`, `Punctuation=true`, `NumThreads=2`). Return wrapped sentinel errors. (`chineseasr.go`, `errors.go`)
4. **Engine arg builder** — `internal/engine/args.go`: map `(Model, ModelDir, Language, Punctuation, NumThreads, wavPath)` → `[]string`. SenseVoice MUST include `--sense-voice-model`, `--tokens`, `--sense-voice-language=<lang>`, `--sense-voice-use-itn=<0|1>`, `--debug=0`, `--num-threads=<n>`, then the wav path. Paraformer (`--paraformer`, `--tokens`, …) and Whisper (`--whisper-encoder`/`--whisper-decoder`, `--tokens`, …) flag sets wired and unit-tested. (`internal/engine/args.go`)
5. **ffmpeg layer** — `internal/audio/ffmpeg.go`: reject non-local input (no `scheme://`; must be an existing regular file → `ErrAudioNotFound`/`ErrRemoteInputRejected`); run `ffmpeg -hide_banner -nostdin -protocol_whitelist file,pipe -vn -i <in> -ar 16000 -ac 1 -c:a pcm_s16le -f wav -y <tmp.wav>` via the runner; on non-zero exit return `ErrDecodeFailed` wrapping the stderr tail; return temp path + cleanup func. Note: empty/zero-byte files pass the regular-file check and surface as `ErrDecodeFailed` when ffmpeg fails to decode them. (`internal/audio/ffmpeg.go`)
6. **Engine run + parse** — `internal/engine/parse.go`: scan **stdout** for non-empty lines; for each, attempt `json.Unmarshal` into `struct{ Text string }`; collect blocks that parse and contain a `text` key. Expect exactly one (single wav): zero → `ErrParseFailed` (wrap stderr tail); take its `Text`. This tolerates both "pure-JSON stdout" and "JSON-among-noise stdout" layouts. On non-zero sherpa exit → `ErrToolFailed` wrapping stderr tail. Read stdout as raw UTF-8 bytes (no re-encode). Command construction + execution live in `internal/engine/sherpa.go`; stdout→text extraction lives in `internal/engine/parse.go`. (`internal/engine/sherpa.go`, `internal/engine/parse.go`)
7. **Transcribe orchestration** — wire validate → `ctx` check → ffmpeg → `ctx` check → engine → parse → `Result`; `defer` temp cleanup on **every** path (success/error/cancel); after the run, return `ctx.Err()` if non-nil (so cancellation surfaces as `context.Canceled`/`DeadlineExceeded`, not raw `signal: killed`); empty `.text` → `ErrEmptyTranscript`. (`chineseasr.go`)
8. **Probe** — `Probe(ctx)` runs sherpa on `testdata/sample_zh.wav`, asserts a JSON block with a `text` field is parseable → `ErrSchemaMismatch` on drift. (`chineseasr.go`)
9. **Unit tests** — arg-builder per model (assert ITN/debug/language/per-model flags); parser against the **real** `golden_sensevoice_stdout.txt` (asserts exact text incl. punctuation); config-validation error cases incl. `ErrModelNotImplemented`; `Transcribe` end-to-end with a **fake runner** returning golden stdout (covers orchestration wiring without binaries); temp-cleanup asserted on success AND on injected ffmpeg/sherpa failure; ctx-cancel test asserts `errors.Is(err, context.Canceled)`; remote-URL input rejected before any subprocess. (`*_test.go`)
10. **Guarded integration test** — if real `ffmpeg`/`sherpa-onnx-offline`/model are discoverable (env vars), run end-to-end on `testdata/sample_zh.wav`; assert non-empty, valid-UTF-8, contains CJK, contains ≥1 Chinese punctuation mark, and **≥ 70% character containment (longest-common-subsequence based) vs the committed reference transcript** — this 70% is a conservative starting bar to be calibrated and re-committed from the first real golden run; until calibrated, assert only the non-empty/UTF-8/CJK/punctuation checks; else `t.Skip`. (`chineseasr_test.go`)
11. **Example CLI** — `examples/cli/main.go`: flags for the three paths + audio + `-model`/`-lang`/`-punct`; prints transcript. Demonstrates the library; not the product. (`examples/cli/main.go`)
12. **README/docs** — (1) what each path param points to (ffmpeg binary, sherpa-onnx-offline binary, model dir = model file(s)+tokens.txt); (2) required tools with the **exact pinned** SenseVoice package name + sherpa-onnx release + model download URL, ffmpeg; (3) macOS (`brew install ffmpeg`, sherpa-onnx prebuilt, model tarball) and Linux (`apt install ffmpeg`, sherpa-onnx prebuilt, model tarball) install steps; a copy-pasteable `Config` example; the **offline scope statement** (Go layer + locally-resolved binaries/model; caller supplies offline-capable tools); the **no-default-timeout** note (caller's `ctx` governs runtime). (`README.md`)

## Acceptance Criteria (testable)
- [ ] `New` returns a wrapped sentinel error for each of: missing `FFmpegPath`, missing `SherpaOfflinePath`, missing model file/`tokens.txt`, and unwired `Model` (`ErrModelNotImplemented`) — one unit test per case.
- [ ] `args.Build(ModelSenseVoice, Punctuation=true, Language="zh", ...)` includes `--sense-voice-use-itn=1`, `--debug=0`, `--sense-voice-language=zh`, and `--num-threads` — unit test asserts each flag's presence and value.
- [ ] Arg-builder produces a correct, distinct flag slice for each **wired** model (SenseVoice/Paraformer/Whisper); switching `Config.Model` among wired models is a config-only change (no caller recompile) — unit-tested for all three.
- [ ] The stdout parser extracts the correct transcript (incl. punctuation) from the checked-in **real** golden fixture — unit test asserts the exact string and that it contains ≥1 of `，。？！`.
- [ ] `Transcribe` orchestration is exercised end-to-end with a fake runner (no binaries) and returns the expected `Result.Text` — unit test.
- [ ] On a corrupt/empty/non-audio input, `Transcribe` returns `ErrDecodeFailed` whose message contains ffmpeg stderr context (not an opaque exec error) — unit + guarded-integration test with `testdata/bad_input.bin`.
- [ ] A remote input (e.g. `http://…`) is rejected with `ErrRemoteInputRejected` **before** any subprocess runs — unit test.
- [ ] No `net`/`net/http` import in library code (`go list`/grep) AND ffmpeg is invoked with `-protocol_whitelist file,pipe`; README states the offline-scope boundary — grep test + doc check.
- [ ] Temp WAV is removed after `Transcribe` on success AND on injected ffmpeg/sherpa failure AND on ctx cancellation — unit tests assert temp dir clean on all three paths.
- [ ] `ctx` cancellation aborts an in-flight `Transcribe` and returns an error with `errors.Is(err, context.Canceled)` — unit test (fake runner blocks; ctx cancelled).
- [ ] Guarded integration: `Transcribe` on `testdata/sample_zh.wav` returns text that is non-empty, valid UTF-8, contains CJK, contains ≥1 Chinese punctuation mark, and meets the character-containment threshold vs the committed reference (≥70% LCS-based, calibrated from the first real run) — skipped cleanly if binaries/model absent.
- [ ] Human sign-off gate: `testdata/reference_transcripts.md` holds ≥3 representative real clips, each with a Chinese-reader-confirmed expected transcript and a recorded "usable: yes/no"; v1 passes only if all are "yes".
- [ ] `README.md` contains the three doc sections + offline-scope statement + the exact pinned model/sherpa version + a copy-pasteable `Config` example.
- [ ] `CGO_ENABLED=0 go build ./...` and `go vet ./...` pass.

## Risks and Mitigations
- **R1: `sherpa-onnx-offline` stdout layout/stream differs by version (reviewers disagreed: pure-JSON vs JSON-among-noise).** Mitigation: parser scans stdout line-by-line for JSON blocks with a `text` field (tolerant of both layouts); stdout/stderr captured separately; **executor pins the truth by capturing a real `golden_sensevoice_stdout.txt`** from the documented version; `Probe` asserts the schema at startup; `ErrParseFailed`/`ErrSchemaMismatch` fail loudly rather than returning garbage.
- **R2 [was the CRITICAL miss]: Missing ITN flag → unpunctuated output violating the spec.** Mitigation: `--sense-voice-use-itn=1` is mandatory in the SenseVoice arg set (`Config.Punctuation` default true); arg-builder test + golden-fixture test + integration test all assert punctuation.
- **R3: Non-SenseVoice models advertised but unverified.** Mitigation: export only wired models; `New` returns `ErrModelNotImplemented` for the rest; docs mark Paraformer/Whisper as "wired, accuracy verify-it-yourself," SenseVoice as the verified default.
- **R4: Offline guarantee defeated by a subprocess fetching remote data.** Mitigation: reject non-local inputs; `-protocol_whitelist file,pipe` on ffmpeg; documented offline scope; test rejects `http://` input.
- **R5: ffmpeg/sherpa not present or wrong path/version.** Mitigation: `New` validates and returns `ErrFFmpegNotFound`/`ErrSherpaNotFound`/`ErrModelNotFound`; `Probe` catches version/schema drift; README pins versions + install steps.
- **R6: Subprocess errors are opaque.** Mitigation: capture stderr separately; wrap stderr tail into `ErrDecodeFailed`/`ErrToolFailed`; distinguish non-zero exit from parse failure from empty transcript.
- **R7: Large audio → long/unbounded runtime.** Mitigation: temp-file streaming (no full in-memory decode); caller `ctx` governs timeout; README documents no default timeout; v1 is batch/whole-file.
- **R8: Temp-file / process leakage on crash or cancel.** Mitigation: unique temp names; `defer` cleanup on all paths; `CommandContext` kills the process on cancel; cleanup tests cover failure + cancel paths.
- **R9: Encoding issues with Chinese output.** Mitigation: treat stdout as raw UTF-8 bytes; `json.Unmarshal` handles escapes; tests assert valid UTF-8 Han.

## Verification Steps
1. `CGO_ENABLED=0 go build ./...` and `go vet ./...` — must pass (proves pure-Go, no native linking).
2. `go test ./...` — all unit tests pass: arg-builder (incl. ITN/debug), parser golden (incl. punctuation), config validation (incl. `ErrModelNotImplemented`), fake-runner orchestration, bad-input `ErrDecodeFailed`, remote-input rejection, temp cleanup (success/failure/cancel), ctx cancellation.
3. `grep -rn "net/http\|\"net\"" --include=*.go .` excluding tests — returns nothing in library code; confirm ffmpeg cmd includes `-protocol_whitelist file,pipe`.
4. **Recorded human gate (primary spec bar):** with tools+model installed, run `examples/cli` on the ≥3 reference clips in `testdata/reference_transcripts.md`; a Chinese reader records "usable: yes/no" for each; v1 passes only if all "yes". This replaces the unrecorded "eyeball."
5. Run the guarded integration test with real binaries; confirm it asserts CJK + punctuation + containment threshold.
6. Follow README install steps on a clean macOS and a clean Linux box; confirm a working setup from docs alone.

## ADR

- **Decision:** Build a pure-Go library that drives `ffmpeg` and the `sherpa-onnx-offline` CLI as subprocesses, default model SenseVoice-Small (punctuation/ITN on), returning Simplified-Chinese text via an extensible `Result`; external tool/model paths are caller config; offline is enforced by rejecting remote inputs and whitelisting ffmpeg file protocols.
- **Drivers:** best offline Mandarin accuracy with minimal Go complexity; easy model swap/upgrade; robust subprocess boundary.
- **Alternatives considered:** (B) in-process `sherpa-onnx-go` CGo — technically lower-risk for a Linux+macOS-only v1 (prebuilt libs, typed result, no parsing, recognizer reuse), but **declined by the user** for looser coupling/crash isolation; (C) pure-Go inference — dropped for immaturity/accuracy.
- **Why chosen:** honors the user's explicit subprocess decision while the design reclaims most of CGo's safety via stdout-only JSON parsing, a startup `Probe`, and an injectable runner seam.
- **Consequences:** failure surface lives at the user's runtime (mitigated by `Probe` + actionable stderr-wrapped errors); per-call process spawn + model-load overhead (acceptable for batch v1; future: batch multiple wavs per invocation to amortize); coupling to sherpa's JSON `text` field + flag strings (version-pinned in docs, schema-checked by `Probe`).
- **Follow-ups:** segment timestamps/metadata in `Result`; Traditional-Chinese output via OpenCC; verify+promote Paraformer/FireRedASR/Qwen3; optional multi-file batching; optional auto-download helper script (kept out of the library per spec).

## Open Questions (must-verify at implementation; do not block planning)
1. **Stream layout:** does the pinned sherpa-onnx release print the result JSON to stdout-only (Architect) or buried in stdout noise (Critic)? Resolve by capturing a real `golden_sensevoice_stdout.txt`; the tolerant parser works either way but the fixture must be real.
2. **Simplified vs Traditional:** confirm `--sense-voice-language=zh` yields Simplified for the representative clips (spec defers Traditional).

## Changelog (consensus improvements applied in iteration 2)
- **[CRITICAL]** Added mandatory `--sense-voice-use-itn=1` (punctuation) + `Config.Punctuation` + `--debug=0`; added punctuation acceptance criteria (Architect rec 2; Critic req 1, 2).
- **[MAJOR]** Replaced speculative parsing with stream-separated, JSON-line-scan parser tolerant to both reviewer-disputed layouts; mandated a **real** golden fixture; documented the verified tool contract (Architect rec 1, 7; Critic req 3).
- **[MAJOR]** Hardened offline guarantee: reject remote inputs, `-protocol_whitelist file,pipe`, scoped offline statement + test (Critic req 4).
- **[MAJOR]** Resolved model-swap vs delivery contradiction: export only wired models, `ErrModelNotImplemented`, reworded AC; wired Paraformer/Whisper arg-builders (Architect rec 6; Critic req 5).
- **[MAJOR]** Added distinct bad-audio error path `ErrDecodeFailed` + fixture + AC/test (Architect rec 4; Critic req 6).
- **[MAJOR]** Added injectable `runner` seam so orchestration is unit-tested without binaries (Architect rec 3).
- Added `Probe` startup schema check (Architect synthesis); fixed ctx-cancellation semantics + inter-stage check + cleanup-on-all-paths (Architect rec 5; Critic gaps); added `-vn` (Architect rec 8); pinned model/sherpa version in README (Critic req 7); split non-testable "non-empty Simplified" AC into mechanical checks + recorded human sign-off gate; documented no-default-timeout and that `New` ≠ working pipeline.
- **Post-approval polish (Critic APPROVED, non-blocking items):** defined the previously-placeholder containment threshold (≥70% LCS, calibrated from first real run); added `internal/engine/sherpa.go` to the layout; noted empty/zero-byte files surface as `ErrDecodeFailed`.

## Residual decisions (defaults; from spec)
- Output script = Simplified Chinese (SenseVoice default). Traditional (OpenCC) is out of v1 scope.
- Mandarin primary; Cantonese (yue) supported by SenseVoice as a bonus, not a v1 gate.
- Module path `github.com/timmyb32r/001_omc_cri` is a placeholder — set to the user's real repo before publishing.
