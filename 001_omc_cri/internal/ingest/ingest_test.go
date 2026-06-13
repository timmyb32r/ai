package ingest

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// argIndex returns the index of want in args, or -1.
func argIndex(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

// hasAdjacent reports whether flag is immediately followed by value in args.
func hasAdjacent(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestBuildPCMArgs(t *testing.T) {
	const url = "https://example.com/live/playlist.m3u8"
	args := buildPCMArgs(url)

	if !hasAdjacent(args, "-protocol_whitelist", "file,http,https,tcp,tls,crypto") {
		t.Fatalf("missing/incorrect -protocol_whitelist in %v", args)
	}
	if !hasAdjacent(args, "-i", url) {
		t.Fatalf("missing -i %s in %v", url, args)
	}
	// Single-output PCM: s16le, 16 kHz, mono, to stdout (pipe:1).
	if !hasAdjacent(args, "-c:a", "pcm_s16le") || !hasAdjacent(args, "-f", "s16le") {
		t.Fatalf("missing PCM codec/format (-c:a pcm_s16le / -f s16le) in %v", args)
	}
	if !hasAdjacent(args, "-ar", "16000") || !hasAdjacent(args, "-ac", "1") {
		t.Fatalf("missing PCM rate/channels (-ar 16000 / -ac 1) in %v", args)
	}
	if argIndex(args, "pipe:1") < 0 {
		t.Fatalf("missing pipe:1 (PCM stdout) in %v", args)
	}
	// PCM process must NOT carry the MP3 branch.
	if argIndex(args, "libmp3lame") >= 0 || argIndex(args, "pipe:3") >= 0 {
		t.Fatalf("PCM args must not contain MP3/pipe:3 branch: %v", args)
	}
	if argIndex(args, "-nostdin") < 0 {
		t.Fatalf("missing -nostdin in %v", args)
	}
}

func TestBuildMP3Args(t *testing.T) {
	const url = "https://example.com/live/playlist.m3u8"
	args := buildMP3Args(url)

	if !hasAdjacent(args, "-protocol_whitelist", "file,http,https,tcp,tls,crypto") {
		t.Fatalf("missing/incorrect -protocol_whitelist in %v", args)
	}
	if !hasAdjacent(args, "-i", url) {
		t.Fatalf("missing -i %s in %v", url, args)
	}
	// Single-output MP3: libmp3lame, CBR 64k, to stdout (pipe:1).
	if !hasAdjacent(args, "-c:a", "libmp3lame") || !hasAdjacent(args, "-f", "mp3") {
		t.Fatalf("missing MP3 codec/format (-c:a libmp3lame / -f mp3) in %v", args)
	}
	if !hasAdjacent(args, "-b:a", "64k") {
		t.Fatalf("missing -b:a 64k in %v", args)
	}
	if argIndex(args, "pipe:1") < 0 {
		t.Fatalf("missing pipe:1 (MP3 stdout) in %v", args)
	}
	// MP3 process must NOT carry the PCM branch.
	if argIndex(args, "pcm_s16le") >= 0 || argIndex(args, "s16le") >= 0 {
		t.Fatalf("MP3 args must not contain PCM branch: %v", args)
	}
}

// errReader yields data once, then a custom error (used to simulate a stream
// that delivers a few bytes then fails).
type errReader struct {
	data []byte
	err  error
	off  int
}

func (r *errReader) Read(p []byte) (int, error) {
	if r.off < len(r.data) {
		n := copy(p, r.data[r.off:])
		r.off += n
		return n, nil
	}
	if r.err != nil {
		return 0, r.err
	}
	return 0, io.EOF
}

func (r *errReader) Close() error { return nil }

// blockReader yields data once, then blocks until ctx is cancelled, then
// returns EOF. Models a healthy, long-lived stream.
type blockReader struct {
	data []byte
	ctx  context.Context
	off  int
}

func (r *blockReader) Read(p []byte) (int, error) {
	if r.off < len(r.data) {
		n := copy(p, r.data[r.off:])
		r.off += n
		return n, nil
	}
	<-r.ctx.Done()
	return 0, io.EOF
}

func (r *blockReader) Close() error { return nil }

// fakeProcess drives a scripted sequence of Start outcomes.
type fakeProcess struct {
	mu         sync.Mutex
	calls      int
	gotName    string
	gotPCMArgs []string
	gotMP3Args []string
	starts     []func(ctx context.Context) (pcm, mp3 io.ReadCloser, wait func() error, err error)
}

func (f *fakeProcess) Start(ctx context.Context, ffmpegPath string, pcmArgs, mp3Args []string) (io.ReadCloser, io.ReadCloser, func() error, error) {
	f.mu.Lock()
	idx := f.calls
	f.calls++
	f.gotName = ffmpegPath
	f.gotPCMArgs = pcmArgs
	f.gotMP3Args = mp3Args
	f.mu.Unlock()

	if idx >= len(f.starts) {
		// After scripted attempts, behave like a permanently-blocking healthy
		// stream so the test can cancel ctx to finish.
		<-ctx.Done()
		return nil, nil, nil, ctx.Err()
	}
	return f.starts[idx](ctx)
}

// countingBackoff records how many times Next/Reset were called and returns a
// near-zero delay so the test runs fast.
type countingBackoff struct {
	mu     sync.Mutex
	nexts  int
	resets int
}

func (b *countingBackoff) Next() time.Duration {
	b.mu.Lock()
	b.nexts++
	b.mu.Unlock()
	return time.Microsecond
}

func (b *countingBackoff) Reset() {
	b.mu.Lock()
	b.resets++
	b.mu.Unlock()
}

func (b *countingBackoff) nextCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nexts
}

func TestRunReconnectsAndDeliversFromBothAttempts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// First attempt: a couple of bytes on each stream, then a wait() error.
	// Second attempt: healthy readers that block until ctx done.
	fp := &fakeProcess{
		starts: []func(ctx context.Context) (io.ReadCloser, io.ReadCloser, func() error, error){
			func(ctx context.Context) (io.ReadCloser, io.ReadCloser, func() error, error) {
				pcm := &errReader{data: []byte("PCM1"), err: io.ErrUnexpectedEOF}
				mp3 := &errReader{data: []byte("MP31"), err: io.ErrUnexpectedEOF}
				wait := func() error { return io.ErrUnexpectedEOF }
				return pcm, mp3, wait, nil
			},
			func(ctx context.Context) (io.ReadCloser, io.ReadCloser, func() error, error) {
				pcm := &blockReader{data: []byte("PCM2"), ctx: ctx}
				mp3 := &blockReader{data: []byte("MP32"), ctx: ctx}
				wait := func() error { <-ctx.Done(); return nil }
				return pcm, mp3, wait, nil
			},
		},
	}

	backoff := &countingBackoff{}

	var mu sync.Mutex
	var pcmChunks, mp3Chunks []string
	gotSecondPCM := make(chan struct{}, 1)
	onPCM := func(tsSec float64, b []byte) {
		mu.Lock()
		pcmChunks = append(pcmChunks, string(b))
		mu.Unlock()
		if string(b) == "PCM2" {
			select {
			case gotSecondPCM <- struct{}{}:
			default:
			}
		}
	}
	onMP3 := func(tsSec float64, b []byte) {
		mu.Lock()
		mp3Chunks = append(mp3Chunks, string(b))
		mu.Unlock()
	}

	ing := New("/fake/ffmpeg", "https://example.com/live.m3u8", fp)
	ing.backoff = backoff
	// Deterministic time so the healthy-run reset never triggers spuriously.
	ing.now = func() time.Time { return time.Unix(0, 0) }

	done := make(chan error, 1)
	go func() { done <- ing.Run(ctx, onPCM, onMP3) }()

	// Wait until the second (healthy) attempt has delivered its PCM chunk,
	// proving a reconnect happened, then cancel to end Run.
	select {
	case <-gotSecondPCM:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("timed out waiting for second attempt's PCM bytes")
	}
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}

	// Backoff must have been invoked (reconnect path).
	if backoff.nextCount() < 1 {
		t.Fatalf("expected backoff.Next to be called at least once, got %d", backoff.nextCount())
	}

	// The fake must have been launched with the production ffmpeg path + args.
	if fp.gotName != "/fake/ffmpeg" {
		t.Fatalf("Start name = %q, want /fake/ffmpeg", fp.gotName)
	}
	if !hasAdjacent(fp.gotPCMArgs, "-i", "https://example.com/live.m3u8") {
		t.Fatalf("Start PCM args missing channel URL: %v", fp.gotPCMArgs)
	}
	if !hasAdjacent(fp.gotMP3Args, "-i", "https://example.com/live.m3u8") {
		t.Fatalf("Start MP3 args missing channel URL: %v", fp.gotMP3Args)
	}

	mu.Lock()
	defer mu.Unlock()
	if !contains(pcmChunks, "PCM1") || !contains(pcmChunks, "PCM2") {
		t.Fatalf("PCM chunks from both attempts missing: %v", pcmChunks)
	}
	if !contains(mp3Chunks, "MP31") || !contains(mp3Chunks, "MP32") {
		t.Fatalf("MP3 chunks from both attempts missing: %v", mp3Chunks)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestRunReturnsOnImmediateCancel verifies Run returns ctx.Err() promptly when
// ctx is already cancelled, without launching the process.
func TestRunReturnsOnImmediateCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	fp := &fakeProcess{}
	ing := New("/fake/ffmpeg", "u", fp)
	ing.backoff = &countingBackoff{}

	err := ing.Run(ctx, func(float64, []byte) {}, func(float64, []byte) {})
	if err != context.Canceled {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
}

// TestExpBackoffSequence locks the production backoff schedule.
func TestExpBackoffSequence(t *testing.T) {
	b := newExpBackoff()
	want := []time.Duration{
		500 * time.Millisecond,
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second, // capped (would be 32s)
		30 * time.Second,
	}
	for i, w := range want {
		if got := b.Next(); got != w {
			t.Fatalf("Next #%d = %v, want %v", i, got, w)
		}
	}
	b.Reset()
	if got := b.Next(); got != 500*time.Millisecond {
		t.Fatalf("after Reset, Next = %v, want 500ms", got)
	}
}

// TestBuildArgsURLNotSplit guards against a URL with commas being mistaken for
// flags (it must remain a single -i value).
func TestBuildArgsURLNotSplit(t *testing.T) {
	url := "https://h.example/p.m3u8?a=1&b=2"
	for _, args := range [][]string{buildPCMArgs(url), buildMP3Args(url)} {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, url) {
			t.Fatalf("URL not present intact in args: %v", args)
		}
	}
}
