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
	cmd    *exec.Cmd
	cancel context.CancelFunc
	stats  Stats
	mu     sync.Mutex
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

	if f.cmd != nil {
		return nil, fmt.Errorf("ingestor already running")
	}

	ctx, f.cancel = context.WithCancel(ctx)

	// Exactly the 001_omc_cri proven-working ffmpeg command:
	//   PCM s16le, 16kHz, mono → stdout
	// No HLS output — we generate HLS segments ourselves from PCM.
	args := []string{
		"-hide_banner", "-nostdin", "-nostats",
		"-protocol_whitelist", "file,http,https,tcp,tls,crypto",
		"-i", f.config.ChannelURL,
		"-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", "-f", "s16le",
		"pipe:1",
	}

	f.cmd = exec.CommandContext(ctx, "ffmpeg", args...)
	stderrPipe, _ := f.cmd.StderrPipe()

	stdout, err := f.cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}

	if err := f.cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}
	go logStderrIngest(stderrPipe)

	atomic.StoreInt64(&f.stats.Running, 1)

	pcmCh := make(chan models.PCMChunk, 8)
	go func() {
		defer close(pcmCh)
		defer atomic.StoreInt64(&f.stats.Running, 0)

		buf := bufio.NewReaderSize(stdout, 48000*2) // s16le = 2 bytes/sample
		samplesPerChunk := 16000 * f.config.HLSTime
		int16Chunk := make([]int16, samplesPerChunk)
		floatChunk := make([]float32, samplesPerChunk)
		segmentID := 0

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			if err := readS16LEChunk(buf, int16Chunk); err != nil {
				return
			}

			for i, v := range int16Chunk {
				floatChunk[i] = float32(v) / 32768.0
			}

			chunkCopy := make([]float32, len(floatChunk))
			copy(chunkCopy, floatChunk)

			select {
			case pcmCh <- models.PCMChunk{
				SegmentID:   segmentID,
				Samples:     chunkCopy,
				DurationSec: float64(f.config.HLSTime),
			}:
				segmentID++
				atomic.AddInt64(&f.stats.SegmentsIngested, 1)
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() { f.cmd.Wait() }()

	return pcmCh, nil
}

func (f *ffmpegIngestor) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.cancel != nil {
		f.cancel()
		f.cancel = nil
	}
	if f.cmd != nil && f.cmd.Process != nil {
		f.cmd.Process.Kill()
		f.cmd = nil
	}
	return nil
}

func (f *ffmpegIngestor) Stats() Stats {
	return Stats{
		SegmentsIngested: atomic.LoadInt64(&f.stats.SegmentsIngested),
		BytesWritten:     atomic.LoadInt64(&f.stats.BytesWritten),
		Running:          atomic.LoadInt64(&f.stats.Running),
	}
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
