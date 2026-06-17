// Package ingest is the streaming subprocess seam for pulling a live HLS
// stream and emitting PCM (s16le 16 kHz mono, for ASR and clients) via a
// single ffmpeg process.
//
// Network I/O lives only here (and in internal/api / cmd); the chineseasr
// library stays offline.
package ingest

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// PCM output format constants. ffmpeg emits signed 16-bit little-endian mono
// at 16 kHz, so one second of audio is 16000 samples * 2 bytes.
const (
	pcmSampleRate   = 16000
	pcmBytesPerSec  = pcmSampleRate * 2 // 32000 bytes/s (s16le mono)
	PcmBitrateKbps  = pcmBytesPerSec * 8 / 1000 // 256 kbps
	readChunkSize   = 8192
	stderrTailBytes = 4096
	minHealthyRun   = 5 * time.Second
	initialBackoff  = 500 * time.Millisecond
	maxBackoff      = 30 * time.Second
	backoffMultiple = 2
)

// Process abstracts launching the streaming ffmpeg subprocess so the Ingestor
// can be tested with an in-memory fake.
//
// The real implementation launches a single ffmpeg process emitting PCM
// (s16le 16 kHz mono) on stdout.
type Process interface {
	Start(ctx context.Context, ffmpegPath string, args []string) (pcm io.ReadCloser, wait func() error, err error)
}

// ExecProcess is the production Process: a single exec.CommandContext ffmpeg
// writing PCM to stdout. Cancelling ctx kills the process. Stderr is captured
// into a bounded ring for error reporting.
type ExecProcess struct{}

func (ExecProcess) Start(ctx context.Context, ffmpegPath string, args []string) (io.ReadCloser, func() error, error) {
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)

	pcmOut, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("ingest: pcm stdout pipe: %w", err)
	}

	tail := &tailBuffer{max: stderrTailBytes}
	cmd.Stderr = tail

	if err := cmd.Start(); err != nil {
		_ = pcmOut.Close()
		return nil, nil, fmt.Errorf("ingest: start ffmpeg: %w", err)
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	wait := func() error {
		err := <-waitCh
		if err != nil {
			return wrapExit(ffmpegPath, err, tail)
		}
		return nil
	}

	return pcmOut, wait, nil
}

// wrapExit annotates a non-nil process exit error with a bounded stderr tail.
func wrapExit(name string, err error, tail *tailBuffer) error {
	if err == nil {
		return nil
	}
	if t := tail.String(); t != "" {
		return fmt.Errorf("ingest: %s exited: %w; stderr tail: %s", name, err, t)
	}
	return fmt.Errorf("ingest: %s exited: %w", name, err)
}

// tailBuffer is an io.Writer that retains only the last max bytes written.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// buildArgs builds the ffmpeg arg list: PCM s16le 16 kHz mono on stdout.
func buildArgs(channelURL string) []string {
	return []string{
		"-hide_banner", "-nostdin",
		"-protocol_whitelist", "file,http,https,tcp,tls,crypto",
		"-i", channelURL,
		"-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", "-f", "s16le", "pipe:1",
	}
}

// backoffPolicy controls reconnect timing. Next returns the wait before the
// next attempt and advances internal state; Reset returns to the initial delay
// after a healthy run.
type backoffPolicy interface {
	Next() time.Duration
	Reset()
}

type expBackoff struct {
	cur     time.Duration
	initial time.Duration
	max     time.Duration
	mult    int
}

func newExpBackoff() *expBackoff {
	return &expBackoff{initial: initialBackoff, max: maxBackoff, mult: backoffMultiple}
}

func (b *expBackoff) Next() time.Duration {
	if b.cur == 0 {
		b.cur = b.initial
	} else {
		b.cur *= time.Duration(b.mult)
		if b.cur > b.max {
			b.cur = b.max
		}
	}
	return b.cur
}

func (b *expBackoff) Reset() { b.cur = 0 }

// Ingestor drives a Process to stream a single channel, reconnecting with
// backoff until its context is cancelled.
type Ingestor struct {
	proc       Process
	ffmpegPath string
	channelURL string

	backoff backoffPolicy
	now     func() time.Time
	sleep   func(ctx context.Context, d time.Duration)
}

// New constructs an Ingestor for channelURL.
func New(ffmpegPath, channelURL string, p Process) *Ingestor {
	return &Ingestor{proc: p, ffmpegPath: ffmpegPath, channelURL: channelURL}
}

func defaultSleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// Run streams the channel until ctx is cancelled, invoking onPCM with the
// timeline timestamp (seconds) and the bytes for each PCM chunk.
func (i *Ingestor) Run(ctx context.Context, onPCM func(tsSec float64, b []byte)) error {
	backoff := i.backoff
	if backoff == nil {
		backoff = newExpBackoff()
	}
	now := i.now
	if now == nil {
		now = time.Now
	}
	sleep := i.sleep
	if sleep == nil {
		sleep = defaultSleep
	}

	args := buildArgs(i.channelURL)

	// Byte clock persists across reconnects for a continuous timeline.
	var byteClock int64

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		started := now()
		pcm, wait, err := i.proc.Start(ctx, i.ffmpegPath, args)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			sleep(ctx, backoff.Next())
			continue
		}

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer pcm.Close()
			byteClock = drain(pcm, byteClock, pcmBytesPerSec, onPCM)
		}()
		wg.Wait()

		waitErr := wait()
		ranFor := now().Sub(started)

		if ctx.Err() != nil {
			return ctx.Err()
		}

		_ = waitErr
		if ranFor >= minHealthyRun {
			backoff.Reset()
		}

		sleep(ctx, backoff.Next())
	}
}

// drain reads r to EOF (or error), invoking cb with the byte-clock timestamp
// (in seconds) at the start of each chunk. Returns the updated byte count.
func drain(r io.Reader, startBytes int64, bytesPerSec int, cb func(tsSec float64, b []byte)) int64 {
	total := startBytes
	buf := make([]byte, readChunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			tsSec := float64(total) / float64(bytesPerSec)
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if cb != nil {
				cb(tsSec, chunk)
			}
			total += int64(n)
		}
		if err != nil {
			return total
		}
	}
}
