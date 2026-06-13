# worker-wire learnings (Wave C: root wiring + tests)

## Authorized contract change: `Config.Punctuation bool` -> `*bool`
- A plain `bool` can't distinguish "unset" from "explicit false", yet the plan
  needs punctuation ON by default AND disableable. Changed the public field to
  `*bool`. `nil` = default (ON). Non-nil = honored verbatim.
- `New` leaves the pointer as-is (no forced default). The default is resolved at
  call time via `(*Transcriber).punctuationEnabled()`, which derefs to a plain
  `bool` before calling `engine.Build` (whose signature stays `bool` —
  unchanged).
- **worker-docs / team-verify: callers that want punctuation OFF must pass
  `cfg.Punctuation = &someFalse`**. README/CLI examples must use `*bool`.

## New validation (matches contract)
- `requireFile(path)`: stat must succeed AND not be a directory. A directory
  passed where a binary is expected is rejected with the matching `Err*NotFound`.
- Model file requirements live in `requiredModelFiles` map:
  - sense-voice / paraformer: `tokens.txt` + `model.int8.onnx`
  - whisper: `tokens.txt` + `encoder.onnx` + `decoder.onnx`
- Order: ffmpeg -> sherpa -> unwired-model (ErrModelNotImplemented) ->
  ModelDir-is-dir -> required files (ErrModelNotFound).

## Transcribe
- `defer cleanup()` is placed immediately after `audio.Convert` returns (cleanup
  is always non-nil per the audio contract), BEFORE the err check, so the temp
  WAV is removed on every path including decode failure.
- Cancellation: `ctx.Err()` checked up front and again between Convert and
  Recognize; after Recognize errors, `ctx.Err()` is preferred so a cancel
  surfaces as `context.Canceled`/`DeadlineExceeded` rather than the wrapped
  `ErrToolFailed`.

## Probe
- Wired but unexercised by unit tests (needs a real binary + sample). Skips the
  ffmpeg step (sample is already 16k mono), feeds `testdata/sample_zh.wav`
  straight to `engine.Build`/`engine.Recognize`, and maps an
  `ErrParseFailed`/`ErrEmptyTranscript` result to `ErrSchemaMismatch`.

## Tests (chineseasr_test.go, package chineseasr)
- `fakeRunner` branches on binary name: contains "ffmpeg" (or == configured
  ffmpegPath) -> decode stage (empty stdout); else -> engine stage (golden
  stdout). Golden line read from `testdata/golden_sensevoice_stdout.txt`.
- Happy path asserts `Result.Text == "今天天气很好，我们去公园吧。"` and exactly 2 runner
  calls. Cancel + remote-input tests assert 0 runner calls.

## Verification (all green)
`CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...` pass.
gofmt clean. `go test -race .` clean. 15 root tests pass.
