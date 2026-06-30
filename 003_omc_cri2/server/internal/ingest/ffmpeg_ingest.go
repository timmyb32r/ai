package ingest

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/criradio/server/internal/models"
)

type ffmpegIngestor struct {
	config Config
	mu     sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc
	loopCancel context.CancelFunc
	stats  Stats
	started bool
}

func New(cfg Config) (Ingestor, error) {
	if cfg.ChannelURL == "" {
		return nil, fmt.Errorf("ChannelURL is required")
	}
	if cfg.OutputDir == "" {
		return nil, fmt.Errorf("OutputDir is required")
	}
	if cfg.HLSTime < 1 {
		cfg.HLSTime = 3
	}
	if cfg.HLSWindow < 1 {
		cfg.HLSWindow = 3600
	}

	hlsDir := filepath.Join(cfg.OutputDir, "hls")
	if err := os.MkdirAll(hlsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create hls dir: %w", err)
	}
	metaDir := filepath.Join(cfg.OutputDir, "metadata")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return nil, fmt.Errorf("create metadata dir: %w", err)
	}

	return &ffmpegIngestor{config: cfg}, nil
}

func (f *ffmpegIngestor) Start(ctx context.Context) (<-chan models.PCMChunk, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.started {
		return nil, fmt.Errorf("ingestor already running")
	}

	loopCtx, loopCancel := context.WithCancel(ctx)
	f.loopCancel = loopCancel
	f.started = true

	pcmCh := make(chan models.PCMChunk, 8)
	go f.runLoop(loopCtx, pcmCh)
	return pcmCh, nil
}

func (f *ffmpegIngestor) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.started = false
	if f.loopCancel != nil {
		f.loopCancel()
		f.loopCancel = nil
	}
	f.killSessionLocked()
	return nil
}

func (f *ffmpegIngestor) Stats() Stats {
	return Stats{
		SegmentsIngested: atomic.LoadInt64(&f.stats.SegmentsIngested),
		BytesWritten:     atomic.LoadInt64(&f.stats.BytesWritten),
		Running:          atomic.LoadInt64(&f.stats.Running),
	}
}

func (f *ffmpegIngestor) runLoop(ctx context.Context, pcmCh chan models.PCMChunk) {
	defer close(pcmCh)
	defer atomic.StoreInt64(&f.stats.Running, 0)

	backoff := 2 * time.Second
	const maxBackoff = 60 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		prevSegments := atomic.LoadInt64(&f.stats.SegmentsIngested)
		fmt.Fprintf(os.Stdout, "[I] %s ingest session_started segments=%d\n",
			time.Now().Format("2006-01-02 15:04:05.000"), prevSegments)

		sessionErr := f.runSession(ctx, pcmCh)
		if ctx.Err() != nil {
			return
		}

		reason := "unknown"
		if sessionErr != nil {
			reason = sessionErr.Error()
		}
		newSegments := atomic.LoadInt64(&f.stats.SegmentsIngested)
		fmt.Fprintf(os.Stdout, "[W] %s ingest session_ended reason=%q segments=%d reconnect_in=%s\n",
			time.Now().Format("2006-01-02 15:04:05.000"),
			reason,
			newSegments,
			backoff,
		)

		f.mu.Lock()
		f.killSessionLocked()
		f.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		// Reset backoff if the session made progress (ingested segments),
		// otherwise keep exponential backoff for transient failures.
		if newSegments > prevSegments {
			backoff = 2 * time.Second
		} else if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

func (f *ffmpegIngestor) runSession(ctx context.Context, pcmCh chan<- models.PCMChunk) error {
	sessionCtx, cancel := context.WithCancel(ctx)

	f.mu.Lock()
	f.cancel = cancel
	f.cmd = f.newFFmpegCmd(sessionCtx)
	cmd := f.cmd
	f.mu.Unlock()

	defer cancel()

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	atomic.StoreInt64(&f.stats.Running, 1)

	go logStderrIngest(stderrPipe)
	go func() {
		if err := cmd.Wait(); err != nil {
			fmt.Fprintf(os.Stdout, "[W] %s ingest-ffmpeg exited err=%v\n",
				time.Now().Format("2006-01-02 15:04:05.000"), err)
		}
	}()

	buf := bufio.NewReaderSize(stdout, 48000*2)
	samplesPerChunk := 16000 * f.config.HLSTime
	int16Chunk := make([]int16, samplesPerChunk)
	floatChunk := make([]float32, samplesPerChunk)

	readTimeout := 30 * time.Second
	if d := time.Duration(f.config.HLSTime*10) * time.Second; d > readTimeout {
		readTimeout = d
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		readErr, timedOut := readS16LEChunkWithTimeout(buf, int16Chunk, readTimeout)
		if timedOut {
			f.killFFmpegProcess()
			return fmt.Errorf("stdout read timeout after %s", readTimeout)
		}
		if readErr != nil {
			return fmt.Errorf("stdout read: %w", readErr)
		}

		for i, v := range int16Chunk {
			floatChunk[i] = float32(v) / 32768.0
		}

		chunkCopy := make([]float32, len(floatChunk))
		copy(chunkCopy, floatChunk)

		select {
		case pcmCh <- models.PCMChunk{
			Samples:     chunkCopy,
			DurationSec: float64(f.config.HLSTime),
		}:
			atomic.AddInt64(&f.stats.SegmentsIngested, 1)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (f *ffmpegIngestor) newFFmpegCmd(ctx context.Context) *exec.Cmd {
	args := []string{
		"-hide_banner", "-nostdin", "-nostats",
		"-rw_timeout", "30000000", // 30s network I/O timeout (microseconds)
		"-reconnect", "1",
		"-reconnect_at_eof", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "5",
		"-protocol_whitelist", "file,http,https,tcp,tls,crypto",
		"-i", f.config.ChannelURL,
		"-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", "-f", "s16le",
		"pipe:1",
	}
	return exec.CommandContext(ctx, "ffmpeg", args...)
}

func (f *ffmpegIngestor) killFFmpegProcess() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cmd != nil && f.cmd.Process != nil {
		f.cmd.Process.Kill()
	}
}

func (f *ffmpegIngestor) killSessionLocked() {
	if f.cancel != nil {
		f.cancel()
		f.cancel = nil
	}
	if f.cmd != nil && f.cmd.Process != nil {
		f.cmd.Process.Kill()
	}
	f.cmd = nil
	atomic.StoreInt64(&f.stats.Running, 0)
}

func logStderrIngest(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			fmt.Fprintf(os.Stdout, "[I] %s ingest-ffmpeg line msg=%s\n",
				time.Now().Format("2006-01-02 15:04:05.000"), line)
		}
	}
}

func readS16LEChunkWithTimeout(r *bufio.Reader, buf []int16, timeout time.Duration) (error, bool) {
	errCh := make(chan error, 1)
	go func() {
		errCh <- readS16LEChunk(r, buf)
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case err := <-errCh:
		return err, false
	case <-timer.C:
		return fmt.Errorf("read timeout"), true
	}
}

func readS16LEChunk(r *bufio.Reader, buf []int16) error {
	for i := range buf {
		lo, err := r.ReadByte()
		if err != nil {
			return err
		}
		hi, err := r.ReadByte()
		if err != nil {
			return err
		}
		buf[i] = int16(uint16(lo) | uint16(hi)<<8)
	}
	return nil
}
