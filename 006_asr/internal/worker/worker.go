package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/timmyb32r/yt2srt/internal/asr"
	"github.com/timmyb32r/yt2srt/internal/config"
	"github.com/timmyb32r/yt2srt/internal/extract"
	"github.com/timmyb32r/yt2srt/internal/logging"
	"github.com/timmyb32r/yt2srt/internal/models"
	"github.com/timmyb32r/yt2srt/internal/storage"
)

// Worker orchestrates a single transcription job end-to-end.
type Worker struct {
	transcriber asr.Transcriber
	store       *storage.InMemoryStore
	log         logging.Logger
	cfg         *config.Config
}

// New creates a Worker.
func New(tr asr.Transcriber, store *storage.InMemoryStore, log logging.Logger, cfg *config.Config) *Worker {
	return &Worker{transcriber: tr, store: store, log: log, cfg: cfg}
}

// ProcessJob runs the full transcription pipeline for a job.
func (w *Worker) ProcessJob(jobID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	job := w.store.Get(jobID)
	if job == nil {
		return
	}

	// Create per-job temp directory
	tempDir, err := os.MkdirTemp(w.cfg.TempDir, "yt2srt-"+jobID)
	if err != nil {
		w.setError(jobID, fmt.Sprintf("create temp dir: %v", err))
		return
	}
	defer os.RemoveAll(tempDir)

	// --- Extraction with live progress + heartbeat ---
	extractStart := time.Now()
	w.updateJob(jobID, func(j *models.Job) {
		j.Status = models.StatusExtracting
		j.Stage = "downloading..."
		j.Progress = 0.02
	})
	// Heartbeat: update elapsed time every second so UI never looks frozen
	heartbeatStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				elapsed := time.Since(extractStart).Round(time.Second)
				w.updateJob(jobID, func(j *models.Job) {
					// Only heartbeat if stage is still the default "downloading..." or our elapsed pattern
					if j.Stage == "downloading..." || strings.HasPrefix(j.Stage, "downloading... (") {
						j.Stage = fmt.Sprintf("downloading... (%v)", elapsed)
					}
				})
			case <-heartbeatStop:
				return
			}
		}
	}()
	w.log.Info("worker", "extracting", "job_id", jobID, "url", job.URL, "temp_dir", tempDir)

	pcmPath := filepath.Join(tempDir, "audio.pcm")
	lastUpdate := time.Now()
	durationSec, err := extract.DownloadAudio(ctx, w.cfg.YtDlpPath, w.cfg.FFmpegPath, job.URL, pcmPath,
		func(pct int, info string) {
			if time.Since(lastUpdate) < 400*time.Millisecond {
				return
			}
			lastUpdate = time.Now()
			w.updateJob(jobID, func(j *models.Job) {
				j.Stage = info
				if pct >= 0 {
					j.Progress = 0.02 + 0.03*float64(pct)/100.0
				}
			})
		})
	close(heartbeatStop)
	if err != nil {
		w.setError(jobID, fmt.Sprintf("download: %v", err))
		return
	}
	defer os.Remove(pcmPath)
	w.log.Info("worker", "downloaded", "job_id", jobID, "duration_sec", durationSec)

	w.updateJob(jobID, func(j *models.Job) { j.DurationSec = durationSec })

	chunkSec := w.cfg.ChunkDuration
	if chunkSec <= 0 {
		chunkSec = 30
	}

	chunks, err := extract.LoadChunks(pcmPath, durationSec, chunkSec)
	if err != nil {
		w.setError(jobID, fmt.Sprintf("chunk: %v", err))
		return
	}

	w.log.Info("worker", "chunked", "job_id", jobID, "chunks", len(chunks), "duration_sec", durationSec)

	// --- Transcription ---
	w.updateJob(jobID, func(j *models.Job) {
		j.Status = models.StatusTranscribing
		j.Stage = fmt.Sprintf("transcribing 0/%d", len(chunks))
	})

	maxParallel := w.cfg.MaxParallel
	if maxParallel <= 0 {
		maxParallel = runtime.NumCPU() / 2
		if maxParallel < 1 {
			maxParallel = 1
		}
	}

	results := w.transcribeParallel(ctx, chunks, maxParallel, jobID)

	// --- Merge to SRT ---
	w.updateJob(jobID, func(j *models.Job) {
		j.Status = models.StatusDone
		j.Stage = "merging"
		j.Progress = 0.95
	})

	srtContent := ToSRT(results, durationSec)

	w.updateJob(jobID, func(j *models.Job) {
		j.SRTContent = srtContent
		j.Status = models.StatusDone
		j.Stage = "ready"
		j.Progress = 1.0
	})

	w.log.Info("worker", "done", "job_id", jobID, "segments", len(results))
}

func (w *Worker) transcribeParallel(ctx context.Context, chunks []models.ChunkInfo, maxParallel int, jobID string) []models.TranscriptResult {
	results := make([]models.TranscriptResult, len(chunks))
	chunkCh := make(chan int, len(chunks))
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i := 0; i < maxParallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range chunkCh {
				chunk := chunks[idx]
				result, err := w.transcriber.Transcribe(chunk.Samples, chunk.Index)
				if err != nil {
					w.log.Warn("worker", "transcribe_error", "chunk", idx, "err", err)
					mu.Lock()
					results[idx] = models.TranscriptResult{ChunkOffset: chunk.StartSec}
					mu.Unlock()
					continue
				}
				mu.Lock()
				results[idx] = models.TranscriptResult{
					Text:        result.Text,
					Timestamps:  result.Timestamps,
					Tokens:      result.Tokens,
					ChunkOffset: chunk.StartSec,
				}
				mu.Unlock()
				// Update progress
				done := 0
				mu.Lock()
				for _, r := range results {
					if r.Text != "" || len(r.Tokens) > 0 {
						done++
					}
				}
				mu.Unlock()
				w.updateJob(jobID, func(j *models.Job) {
					j.Stage = fmt.Sprintf("transcribing %d/%d", done, len(chunks))
					j.Progress = 0.1 + 0.85*float64(done)/float64(len(chunks))
				})
			}
		}()
	}

	for idx := range chunks {
		chunkCh <- idx
	}
	close(chunkCh)
	wg.Wait()
	return results
}

func (w *Worker) setError(jobID, msg string) {
	w.log.Error("worker", "error", "job_id", jobID, "err", msg)
	w.updateJob(jobID, func(j *models.Job) {
		j.Status = models.StatusError
		j.Error = msg
	})
}

func (w *Worker) updateJob(jobID string, fn func(*models.Job)) {
	if err := w.store.Update(jobID, fn); err != nil {
		w.log.Warn("worker", "store_update_error", "job_id", jobID, "err", err)
	}
}
