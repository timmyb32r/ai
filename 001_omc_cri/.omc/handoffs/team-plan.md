## Handoff: team-plan → team-exec (CRI radio client/server)

- **Decided**: Implement `.omc/plans/cri-radio-client-server-plan.md` as NEW packages on the existing module `github.com/timmyb32r/001_omc_cri`, plus an additive lib extension. Decompose into dependency waves on disjoint files. Network ONLY in `internal/ingest`/`internal/api`/`cmd`; `chineseasr` lib stays offline; do NOT change `internal/engine/parse.go:92`.
- **Waves**: A foundation (skeletons + frozen contracts + full client). B parallel leaves: seg(#14), ingest(#15), buffer(#16), lifecycle(#17). C: broadcast+asr(#18, after B), api(#19, parallel against frozen contract). D: server+docs(#20). E: verify(#21).
- **Rejected**: reusing batch-only `internal/runner.Runner` for streaming ingest (define a new `ingest.Process` seam); fixed-window ASR (use silence-aligned).
- **Risks**: concurrency (pacer/fan-out/lifecycle races) — require `-race`; broadcast package has many files edited across waves — FREEZE its exported API in wave A; api must depend on an INTERFACE (not concrete Broadcast) so it's testable with a fake.
- **Remaining**: everything below the contract.

### Intended contracts (foundation FREEZES the exact final forms and appends them below)
- `chineseasr`: `type Segment struct { Start, End float64; Text string }`; `func (t *Transcriber) TranscribeSegments(ctx context.Context, wavPath string) ([]Segment, error)`.
- `internal/engine`: `type SpeechRegion struct { Start, End float64 }`; `func ParseSilence(stderr []byte) []SpeechRegion`.
- `internal/ingest`: `type Process interface { Start(ctx, name string, args []string) (pcm, mp3 io.ReadCloser, wait func() error, err error) }`; `type Ingestor`; `New(ffmpegPath, channelURL string, p Process) *Ingestor`; `(*Ingestor) Run(ctx, onPCM, onMP3 func(tsSec float64, b []byte)) error`.
- `internal/broadcast`: `Clock` (Real+Fake w/ Advance); `type SubtitleEvent struct { Start, End float64; TextZh string }`(json); `type LifecycleState int` (Stopped/Starting/Warming/Running/Stopping + String); `Buffer` (PCM/MP3-frame-indexed/segments + frame-boundary lookup + eviction); `Lifecycle` (Acquire/Release + start/stop hooks + linger); `Broadcast` w/ `Subscribe() (<-chan []byte, <-chan SubtitleEvent, func())`, `State() LifecycleState`, `Status() Status`.
- `internal/api`: depends on an INTERFACE `type Broadcaster interface { Subscribe() (<-chan []byte, <-chan broadcast.SubtitleEvent, func()); Status() broadcast.Status }` so it's fake-testable; `func NewMux(b Broadcaster) *http.ServeMux`.
- `cmd/client`: standalone, imports NEITHER chineseasr NOR sherpa — only net/http + os/exec (ffplay).

## Foundation published (Wave A done)

worker-foundation, by worker-foundation. `CGO_ENABLED=0 go build ./...` + `go vet ./...` + `gofmt -l .` clean; `go test ./internal/broadcast/` (clock) passes incl. `-race`; existing chineseasr + engine tests still green (no regression); `go list -deps ./cmd/client` contains NO chineseasr/sherpa. The signatures below are FROZEN — Wave B/C fill bodies only, do not change exported forms.

### Frozen exported signatures (exact)

`chineseasr` (root, `chineseasr.go`, additive — existing methods untouched, `parse.go:92` unchanged):
```go
type Segment struct {
    Start float64
    End   float64
    Text  string
}
func (t *Transcriber) TranscribeSegments(ctx context.Context, wavPath string) ([]Segment, error)
// STUB: returns nil, errors.New("chineseasr: TranscribeSegments not implemented")
```

`internal/engine` (`silence.go`):
```go
type SpeechRegion struct {
    Start float64
    End   float64
}
func ParseSilence(stderr []byte) []SpeechRegion // STUB: returns nil
```

`internal/ingest` (`ingest.go`, imports context+io):
```go
type Process interface {
    Start(ctx context.Context, name string, args []string) (pcm, mp3 io.ReadCloser, wait func() error, err error)
}
type Ingestor struct{ /* proc Process; ffmpegPath, channelURL string (unexported) */ }
func New(ffmpegPath, channelURL string, p Process) *Ingestor
func (i *Ingestor) Run(ctx context.Context, onPCM, onMP3 func(tsSec float64, b []byte)) error // STUB: blocks on ctx, returns ctx.Err()
```

`internal/broadcast`:
```go
// clock.go (FULLY IMPLEMENTED + clock_test.go)
type Clock interface{ Now() time.Time }
type RealClock struct{}
func (RealClock) Now() time.Time
type FakeClock struct{ /* mu sync.Mutex; now time.Time */ }   // zero value usable
func NewFakeClock(t time.Time) *FakeClock
func (c *FakeClock) Now() time.Time
func (c *FakeClock) Advance(d time.Duration)
func (c *FakeClock) Set(t time.Time)

// types.go (FULLY IMPLEMENTED)
type SubtitleEvent struct {
    Start  float64 `json:"start"`
    End    float64 `json:"end"`
    TextZh string  `json:"text_zh"`   // future: Pinyin `json:"pinyin,omitempty"`, English `json:"en,omitempty"` (schema-reserved)
}
type LifecycleState int
const ( Stopped LifecycleState = iota; Starting; Warming; Running; Stopping )
func (s LifecycleState) String() string   // "stopped"|"starting"|"warming"|"running"|"stopping"
type Status struct {
    Channel               string  `json:"channel"`
    Listeners             int     `json:"listeners"`
    DelaySeconds          float64 `json:"delaySeconds"`
    State                 string  `json:"state"`
    LiveEdgeOffsetSeconds float64 `json:"liveEdgeOffsetSeconds"`
}

// buffer.go (method SET frozen, STUB bodies — worker-buffer fills #16)
type Buffer struct{ /* mu sync.Mutex; tracks added by worker-buffer */ }
func (b *Buffer) AppendPCM(tsSec float64, p []byte)
func (b *Buffer) AppendMP3(tsSec float64, frame []byte)
func (b *Buffer) AppendSubtitle(ev SubtitleEvent)
func (b *Buffer) ReadMP3From(fromTsSec float64, maxBytes int) (data []byte, frameStartTsSec float64, nextTsSec float64)
func (b *Buffer) FrameBoundaryAtOrBefore(posSec float64) float64
func (b *Buffer) EvictBefore(tsSec float64)

// lifecycle.go (STUB bodies — worker-lifecycle fills #17)
type Lifecycle struct{ /* mu; refCount; state; start,stop func(); linger time.Duration */ }
func NewLifecycle(start, stop func(), linger time.Duration) *Lifecycle
func (l *Lifecycle) Acquire()
func (l *Lifecycle) Release()

// broadcast.go (STUB bodies — worker-broadcast fills #18). NewBroadcast signature FROZEN:
type Broadcast struct{ /* clock, buf, lifecycle, ingestor, asr, delay, channel */ }
func NewBroadcast(
    clock Clock,
    buf *Buffer,
    lifecycle *Lifecycle,
    ingestor *ingest.Ingestor,
    transcriber *chineseasr.Transcriber,
    delay time.Duration,
    channel string,
) *Broadcast
func (b *Broadcast) Subscribe() (audio <-chan []byte, subs <-chan SubtitleEvent, cancel func())
func (b *Broadcast) State() LifecycleState
func (b *Broadcast) Status() Status

// asr.go (STUB bodies — worker-broadcast fills #18)
type ASR struct{ /* transcriber *chineseasr.Transcriber; buf *Buffer */ }
func NewASR(transcriber *chineseasr.Transcriber, buf *Buffer) *ASR
func (a *ASR) Run(ctx context.Context) error  // STUB: blocks on ctx
func (a *ASR) step(ctx context.Context) error // unexported step seam
```

`internal/api` (`api.go`, depends on INTERFACE so fake-testable):
```go
type Broadcaster interface {
    Subscribe() (<-chan []byte, <-chan broadcast.SubtitleEvent, func())
    Status() broadcast.Status
}
func NewMux(b Broadcaster) *http.ServeMux // 3 routes registered to 501 stubs:
//   GET /v1/stream/audio, GET /v1/stream/subtitles, GET /v1/status
```

### Import graph (no cycles)
```
cmd/server      -> internal/api, internal/broadcast, internal/ingest, chineseasr   (worker-server wires; Wave A only parses flags)
cmd/client      -> stdlib ONLY (net/http, os/exec, context, encoding/json, bufio, os/signal, syscall) — NO chineseasr / sherpa / internal/*
internal/api    -> internal/broadcast                       (interface decoupling; fake-testable)
internal/broadcast -> chineseasr (root), internal/ingest    (and transitively internal/engine via chineseasr)
internal/ingest -> stdlib (context, io)
internal/engine -> internal/asrerr (silence.go adds nothing new)
chineseasr (root) -> internal/{audio,engine,runner}         (does NOT import broadcast — asr.go calls chineseasr.TranscribeSegments, so broadcast->chineseasr is the only edge; no cycle)
```
Network lives ONLY in internal/ingest + internal/api + cmd/server; the chineseasr lib + internal/broadcast/asr.go stay offline (file-based).

### Per-file ownership (Wave B/C/D)
- **worker-seg (#14)**: `chineseasr.go` (fill `TranscribeSegments` body only), `internal/engine/silence.go` (fill `ParseSilence`), `internal/engine/silence_test.go` (NEW), `chineseasr` segments test (NEW).
- **worker-ingest (#15)**: `internal/ingest/ingest.go` (real `Process` impl + fill `Run`/backoff), `internal/ingest/ingest_test.go` (NEW, fake Process).
- **worker-buffer (#16)**: `internal/broadcast/buffer.go` (fill all 6 method bodies + internal fields), `internal/broadcast/buffer_test.go` (NEW, -race).
- **worker-lifecycle (#17)**: `internal/broadcast/lifecycle.go` (fill `Acquire`/`Release` + state machine + linger), `internal/broadcast/lifecycle_test.go` (NEW).
- **worker-broadcast (#18, after B)**: `internal/broadcast/broadcast.go` (fill `Subscribe`/`State`/`Status` + pacer/fan-out), `internal/broadcast/asr.go` (fill `Run`/`step`), `internal/broadcast/broadcast_test.go` + `asr_test.go` (NEW, -race, fake clock/ingestor/transcriber). Do NOT change `clock.go`/`types.go`/the frozen signatures.
- **worker-api (#19, parallel vs frozen contract)**: `internal/api/api.go` (fill 3 handlers), `internal/api/api_test.go` (NEW, httptest + fake Broadcaster).
- **worker-server (#20)**: `cmd/server/main.go` (wire flags -> ingest+broadcast+api, graceful shutdown), `README-radio.md` (NEW), `testdata/e2e-checklist.md` (NEW).
- **worker-foundation (Wave A, DONE)**: `cmd/client/main.go` (fully implemented), `clock.go`+`clock_test.go`+`types.go` (fully implemented), all other skeletons above, this handoff section.
