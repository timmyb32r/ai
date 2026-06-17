package ingest

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func argIndex(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

func hasAdjacent(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestBuildArgs(t *testing.T) {
	const url = "https://example.com/live/playlist.m3u8"
	args := buildArgs(url)

	if !hasAdjacent(args, "-protocol_whitelist", "file,http,https,tcp,tls,crypto") {
		t.Fatalf("missing -protocol_whitelist in %v", args)
	}
	if !hasAdjacent(args, "-i", url) {
		t.Fatalf("missing -i %s in %v", url, args)
	}
	if argIndex(args, "-nostdin") < 0 {
		t.Fatalf("missing -nostdin in %v", args)
	}
	// PCM output: s16le, 16kHz, mono, pipe:1
	if !hasAdjacent(args, "-c:a", "pcm_s16le") || !hasAdjacent(args, "-f", "s16le") {
		t.Fatalf("missing PCM codec/format in %v", args)
	}
	if argIndex(args, "pipe:1") < 0 {
		t.Fatalf("missing pipe:1 (PCM stdout) in %v", args)
	}
	// No MP3-related args should be present.
	if argIndex(args, "libmp3lame") >= 0 || argIndex(args, "mp3") >= 0 {
		t.Fatalf("unexpected MP3-related arg in %v", args)
	}
}

// errReader yields data once, then a custom error.
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

// blockReader yields data once, then blocks until ctx is cancelled.
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

// fakeProcess drives a scripted sequence of Start outcomes (single-args API).
type fakeProcess struct {
	mu      sync.Mutex
	calls   int
	gotName string
	gotArgs []string
	starts  []func(ctx context.Context) (pcm io.ReadCloser, wait func() error, err error)
}

func (f *fakeProcess) Start(ctx context.Context, ffmpegPath string, args []string) (io.ReadCloser, func() error, error) {
	f.mu.Lock()
	idx := f.calls
	f.calls++
	f.gotName = ffmpegPath
	f.gotArgs = args
	f.mu.Unlock()

	if idx >= len(f.starts) {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	return f.starts[idx](ctx)
}

// countingBackoff records how many times Next/Reset were called.
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

	fp := &fakeProcess{
		starts: []func(ctx context.Context) (io.ReadCloser, func() error, error){
			func(ctx context.Context) (io.ReadCloser, func() error, error) {
				pcm := &errReader{data: []byte("PCM1"), err: io.ErrUnexpectedEOF}
				wait := func() error { return io.ErrUnexpectedEOF }
				return pcm, wait, nil
			},
			func(ctx context.Context) (io.ReadCloser, func() error, error) {
				pcm := &blockReader{data: []byte("PCM2"), ctx: ctx}
				wait := func() error { <-ctx.Done(); return nil }
				return pcm, wait, nil
			},
		},
	}

	backoff := &countingBackoff{}

	var mu sync.Mutex
	var pcmChunks []string
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

	ing := New("/fake/ffmpeg", "https://example.com/live.m3u8", fp)
	ing.backoff = backoff
	ing.now = func() time.Time { return time.Unix(0, 0) }

	done := make(chan error, 1)
	go func() { done <- ing.Run(ctx, onPCM) }()

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

	if backoff.nextCount() < 1 {
		t.Fatalf("expected backoff.Next to be called at least once, got %d", backoff.nextCount())
	}

	if fp.gotName != "/fake/ffmpeg" {
		t.Fatalf("Start name = %q, want /fake/ffmpeg", fp.gotName)
	}
	if !hasAdjacent(fp.gotArgs, "-i", "https://example.com/live.m3u8") {
		t.Fatalf("Start args missing channel URL: %v", fp.gotArgs)
	}

	mu.Lock()
	defer mu.Unlock()
	if !contains(pcmChunks, "PCM1") || !contains(pcmChunks, "PCM2") {
		t.Fatalf("PCM chunks from both attempts missing: %v", pcmChunks)
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

func TestRunReturnsOnImmediateCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	fp := &fakeProcess{}
	ing := New("/fake/ffmpeg", "u", fp)
	ing.backoff = &countingBackoff{}

	err := ing.Run(ctx, func(float64, []byte) {})
	if err != context.Canceled {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
}

func TestExpBackoffSequence(t *testing.T) {
	b := newExpBackoff()
	want := []time.Duration{
		500 * time.Millisecond,
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second,
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

func TestBuildArgsURLNotSplit(t *testing.T) {
	url := "https://h.example/p.m3u8?a=1&b=2"
	args := buildArgs(url)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, url) {
		t.Fatalf("URL not present intact in args: %v", args)
	}
}
