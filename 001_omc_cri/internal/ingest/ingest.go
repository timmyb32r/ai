// Package ingest is the streaming subprocess seam for pulling a live HLS
// stream once and emitting two outputs: PCM (s16le 16 kHz mono, for ASR) and
// CBR MP3 (for clients). It is deliberately distinct from internal/runner,
// which is batch-only (buffers stdout/stderr and returns on process exit) and
// therefore unusable for a 24/7 streaming ingest. Network I/O lives only here
// (and in internal/api / cmd); the chineseasr library stays offline.
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
	mp3BitrateKbps  = 64
	mp3BytesPerSec  = mp3BitrateKbps * 1000 / 8 // 8000 bytes/s (CBR 64 kbit/s)
	readChunkSize   = 8192
	stderrTailBytes = 4096
	minHealthyRun   = 5 * time.Second // a run lasting this long resets backoff
	initialBackoff  = 500 * time.Millisecond
	maxBackoff      = 30 * time.Second
	backoffMultiple = 2
)

// Process abstracts launching the streaming ffmpeg subprocesses so the Ingestor
// can be tested with an in-memory fake that injects mid-stream EOF/errors.
//
// The real implementation launches TWO independent single-output ffmpeg
// processes — one emitting PCM (s16le 16 kHz mono, for ASR) on its stdout, and
// one emitting MP3 (for clients) on its stdout. A single ffmpeg with two pipe
// outputs (pipe:1 + pipe:3) was tried first, but ffmpeg does not reliably
// populate the second raw output: the MP3 branch worked while the PCM branch
// came out empty/silent, so ASR saw only silence and produced no subtitles.
// Two independent processes are robust (each is a plain, proven single-output
// command), at the cost of pulling the HLS stream twice.
type Process interface {
	// Start launches two ffmpeg processes via ffmpegPath (one with pcmArgs, one
	// with mp3Args) and returns their stdout streams, a wait func that blocks
	// until EITHER exits (killing the other so both end together) returning the
	// exit error, and an error if a launch itself failed. The caller drains both
	// readers concurrently and must Close them.
	Start(ctx context.Context, ffmpegPath string, pcmArgs, mp3Args []string) (pcm, mp3 io.ReadCloser, wait func() error, err error)
}

// ExecProcess is the production Process: two exec.CommandContext ffmpeg
// processes (PCM and MP3), each writing to its own stdout. Cancelling ctx kills
// both; if one exits on its own, the wait monitor kills the other so the
// Ingestor sees both streams end and reconnects them as a unit. Each process's
// stderr is captured into a bounded ring for error reporting.
type ExecProcess struct{}

func (ExecProcess) Start(ctx context.Context, ffmpegPath string, pcmArgs, mp3Args []string) (io.ReadCloser, io.ReadCloser, func() error, error) {
	pcmCmd := exec.CommandContext(ctx, ffmpegPath, pcmArgs...)
	pcmOut, err := pcmCmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("ingest: pcm stdout pipe: %w", err)
	}
	pcmTail := &tailBuffer{max: stderrTailBytes}
	pcmCmd.Stderr = pcmTail

	mp3Cmd := exec.CommandContext(ctx, ffmpegPath, mp3Args...)
	mp3Out, err := mp3Cmd.StdoutPipe()
	if err != nil {
		_ = pcmOut.Close()
		return nil, nil, nil, fmt.Errorf("ingest: mp3 stdout pipe: %w", err)
	}
	mp3Tail := &tailBuffer{max: stderrTailBytes}
	mp3Cmd.Stderr = mp3Tail

	if err := pcmCmd.Start(); err != nil {
		_ = pcmOut.Close()
		_ = mp3Out.Close()
		return nil, nil, nil, fmt.Errorf("ingest: start pcm ffmpeg: %w", err)
	}
	if err := mp3Cmd.Start(); err != nil {
		killProcess(pcmCmd)
		_ = pcmCmd.Wait()
		_ = pcmOut.Close()
		_ = mp3Out.Close()
		return nil, nil, nil, fmt.Errorf("ingest: start mp3 ffmpeg: %w", err)
	}

	// One Wait goroutine per process; results land in buffered channels.
	pcmWait := make(chan error, 1)
	mp3Wait := make(chan error, 1)
	go func() { pcmWait <- pcmCmd.Wait() }()
	go func() { mp3Wait <- mp3Cmd.Wait() }()

	// Monitor: the instant either process exits, kill the other so both stdout
	// pipes reach EOF and the Ingestor's two drains return together (a dead PCM
	// process must not leave a live MP3 streaming forever with no ASR). Runs
	// independently of wait() being called, then holds both errors for wait().
	pcmErr := make(chan error, 1)
	mp3Err := make(chan error, 1)
	go func() {
		select {
		case e := <-pcmWait:
			pcmErr <- e
			killProcess(mp3Cmd)
			mp3Err <- <-mp3Wait
		case e := <-mp3Wait:
			mp3Err <- e
			killProcess(pcmCmd)
			pcmErr <- <-pcmWait
		}
	}()

	wait := func() error {
		pErr := <-pcmErr
		mErr := <-mp3Err
		// ASR depends on PCM, so surface a PCM failure first.
		if pErr != nil {
			return wrapExit(ffmpegPath+" (pcm)", pErr, pcmTail)
		}
		if mErr != nil {
			return wrapExit(ffmpegPath+" (mp3)", mErr, mp3Tail)
		}
		return nil
	}

	return pcmOut, mp3Out, wait, nil
}

// killProcess sends a kill signal to a started command's process, ignoring
// errors (the process may already have exited).
func killProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
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

// tailBuffer is an io.Writer that retains only the last max bytes written,
// used to keep a bounded stderr tail for error reporting.
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

// backoffPolicy controls reconnect timing. It is injectable so tests can use
// near-zero delays. Next returns the wait before the next attempt and advances
// internal state; Reset returns to the initial delay after a healthy run.
type backoffPolicy interface {
	Next() time.Duration
	Reset()
}

// expBackoff is the production exponential backoff (0.5s, 1s, 2s, 4s, ... cap
// 30s).
type expBackoff struct {
	cur     time.Duration
	initial time.Duration
	max     time.Duration
	mult    int
}

func newExpBackoff() *expBackoff {
	return &expBackoff{
		cur:     0,
		initial: initialBackoff,
		max:     maxBackoff,
		mult:    backoffMultiple,
	}
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
// backoff until its context is cancelled. It is the only producer feeding the
// broadcast buffer.
type Ingestor struct {
	proc       Process
	ffmpegPath string
	channelURL string

	// backoff is the reconnect policy; nil means use the production
	// exponential default. It is overridable so tests can inject near-zero
	// delays. now/sleep are injectable clocks for the same reason.
	backoff backoffPolicy
	now     func() time.Time
	sleep   func(ctx context.Context, d time.Duration) // returns when d elapses or ctx done
}

// New constructs an Ingestor for channelURL using the ffmpeg binary at
// ffmpegPath, launched through p. Pass a real Process in production and a fake
// in tests.
func New(ffmpegPath, channelURL string, p Process) *Ingestor {
	return &Ingestor{
		proc:       p,
		ffmpegPath: ffmpegPath,
		channelURL: channelURL,
	}
}

// ingestInputArgs are the shared input options (network whitelist + the HLS
// input) used by both single-output ffmpeg commands.
func ingestInputArgs(channelURL string) []string {
	return []string{
		"-hide_banner",
		"-nostdin",
		"-protocol_whitelist", "file,http,https,tcp,tls,crypto",
		"-i", channelURL,
	}
}

// buildPCMArgs builds the ffmpeg arg list for the PCM-for-ASR process: decode
// the HLS audio to s16le 16 kHz mono on stdout (pipe:1). Extracted so tests can
// assert the args without running ffmpeg.
func buildPCMArgs(channelURL string) []string {
	return append(ingestInputArgs(channelURL),
		"-map", "0:a",
		"-ar", "16000",
		"-ac", "1",
		"-c:a", "pcm_s16le",
		"-f", "s16le",
		"pipe:1",
	)
}

// buildMP3Args builds the ffmpeg arg list for the MP3-for-clients process: CBR
// 64 kbit/s MP3 on stdout (pipe:1).
func buildMP3Args(channelURL string) []string {
	return append(ingestInputArgs(channelURL),
		"-map", "0:a",
		"-c:a", "libmp3lame",
		"-b:a", "64k",
		"-f", "mp3",
		"pipe:1",
	)
}

// defaultSleep waits for d or until ctx is done, whichever comes first.
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

// Run streams the channel until ctx is cancelled, invoking onPCM and onMP3 with
// the timeline timestamp (seconds) and the bytes for each chunk. It reconnects
// with exponential backoff on EOF/error and returns ctx.Err() when ctx is done.
//
// Timeline / timestamp convention: tsSec is a monotonic wall-clock-style
// position derived from total bytes drained on each stream, NOT reset between
// reconnects, so downstream consumers see a continuous timeline across drops.
//   - PCM:  tsSec = totalPCMBytes / (16000*2)         (s16le 16 kHz mono)
//   - MP3:  tsSec = totalMP3Bytes / (64000/8)         (CBR 64 kbit/s = 8000 B/s)
//
// Because both encodings are CBR and derived from the same source audio, the
// two byte-clocks track the same wall time; the MP3 timestamp is therefore a
// best-effort value aligned to the PCM clock by construction (both count
// elapsed seconds of audio). The tsSec passed to a callback is the position at
// the START of that chunk.
func (i *Ingestor) Run(ctx context.Context, onPCM, onMP3 func(tsSec float64, b []byte)) error {
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

	pcmArgs := buildPCMArgs(i.channelURL)
	mp3Args := buildMP3Args(i.channelURL)

	// Byte clocks persist across reconnects to keep a continuous timeline.
	var pcmBytes, mp3Bytes int64

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		started := now()
		pcm, mp3, wait, err := i.proc.Start(ctx, i.ffmpegPath, pcmArgs, mp3Args)
		if err != nil {
			// Launch failed: back off and retry (unless ctx done).
			if ctx.Err() != nil {
				return ctx.Err()
			}
			sleep(ctx, backoff.Next())
			continue
		}

		// Drain both streams concurrently; neither may block the other.
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			defer pcm.Close()
			pcmBytes = drain(pcm, pcmBytes, pcmBytesPerSec, onPCM)
		}()
		go func() {
			defer wg.Done()
			defer mp3.Close()
			mp3Bytes = drain(mp3, mp3Bytes, mp3BytesPerSec, onMP3)
		}()
		wg.Wait()

		waitErr := wait()
		ranFor := now().Sub(started)

		if ctx.Err() != nil {
			return ctx.Err()
		}

		// A run that lasted long enough resets the backoff so a brief healthy
		// period doesn't keep us in a long-delay regime. waitErr is otherwise
		// only informational here (we always reconnect while ctx is live).
		_ = waitErr
		if ranFor >= minHealthyRun {
			backoff.Reset()
		}

		sleep(ctx, backoff.Next())
	}
}

// drain reads r to EOF (or error), invoking cb with the running byte-clock
// timestamp (in seconds, derived from startBytes + prior reads) at the start of
// each chunk. It returns the updated total byte count. Read errors other than
// EOF end the drain (the surrounding loop reconnects); cb is never given an
// empty slice.
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
