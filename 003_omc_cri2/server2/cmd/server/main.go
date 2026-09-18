package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/criradio/server/internal/api"
	"github.com/criradio/server/internal/asr"
	"github.com/criradio/server/internal/config"
	"github.com/criradio/server/internal/dictionary"
	"github.com/criradio/server/internal/pipeline"
	"github.com/criradio/server/internal/storage"
	"github.com/criradio/server/internal/tokenizer"
	"github.com/criradio/server/internal/unihan"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	store, err := storage.Open(cfg.OutputDir, storage.Options{Retention: cfg.Retention, Delay: cfg.Delay, MaxBytes: cfg.MaxBytes, ReserveBytes: cfg.ReserveBytes, SnapshotTTL: cfg.SnapshotTTL, MaxSnapshots: 5})
	if err != nil {
		return err
	}
	defer store.Close()
	bkrs, err := dictionary.LoadBKRS(filepath.Join(cfg.AssetsDir, "dabkrs.gz"))
	if err != nil {
		return fmt.Errorf("BKRS: %w", err)
	}
	defer bkrs.Close()
	cedict, err := dictionary.Load(filepath.Join(cfg.AssetsDir, "cedict_ts.u8"))
	if err != nil {
		return fmt.Errorf("CEDICT: %w", err)
	}
	defer cedict.Close()
	wiki, err := dictionary.LoadWiktionary(filepath.Join(cfg.AssetsDir, "zh-extract.jsonl.gz"))
	if err != nil {
		return fmt.Errorf("Wiktionary: %w", err)
	}
	defer wiki.Close()
	uni, err := unihan.Load(filepath.Join(cfg.AssetsDir, "Unihan_Readings.txt"))
	if err != nil {
		return fmt.Errorf("Unihan: %w", err)
	}
	tok := tokenizer.NewHanLP(cfg.HanLPURL)
	defer tok.Close()
	model := asr.NewClient(cfg.ASRURL, cfg.InferenceTimeout)
	defer model.Close()
	runner := &pipeline.Runner{Config: pipeline.Config{URL: cfg.ChannelURL, FFmpeg: cfg.FFmpeg, OutputDir: cfg.OutputDir, SegmentSeconds: cfg.SegmentSeconds, QueueSeconds: cfg.QueueSeconds, InferenceTimeout: cfg.InferenceTimeout, StallTimeout: cfg.StallTimeout}, Store: store, ASR: model, Text: &pipeline.Processor{Tokenizer: tok, Dictionary: bkrs, Cedict: cedict, Wiktionary: wiki, Unihan: uni}}
	status := func() any {
		s := runner.Snapshot()
		stats, e := store.Stats(context.Background())
		if e != nil {
			s.Status = "storage_error"
			s.Error = e.Error()
		}
		if s.Status == "running" && time.Since(s.LastInput) > cfg.StallTimeout {
			s.Status = "upstream_waiting"
		}
		if s.Status == "running" && (stats.Total == 0 || stats.Oldest > float64(time.Now().Add(-cfg.Delay).UnixMilli())/1000) {
			s.Status = "buffering"
		}
		lag := 0.0
		if stats.Newest > 0 {
			lag = float64(time.Now().UnixMilli())/1000 - stats.Newest
		}
		return map[string]any{"status": s.Status, "channel_url": cfg.ChannelURL, "segments_total": stats.Total, "metadata_files": stats.Total, "live_edge_offset_sec": lag, "clients_connected": 0, "oldest_segment_start_sec": stats.Oldest, "newest_segment_end_sec": stats.Newest, "asr_engine": "sherpa-onnx", "asr_model": "sense-voice-2024", "dictionary": "bkrs", "pipeline": s, "storage": stats}
	}
	handler := (&api.Server{Store: store, Status: status}).Handler()
	server := &http.Server{Addr: cfg.Addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 15 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	pipelineDone := make(chan error, 1)
	go func() { pipelineDone <- runner.Run(ctx) }()
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		cancel()
		<-pipelineDone
		return err
	}
	httpDone := make(chan error, 1)
	go func() { httpDone <- server.Serve(newBoundedListener(listener, 128)) }()
	pipelineFinished := false
	select {
	case err = <-httpDone:
		cancel()
	case err = <-pipelineDone:
		pipelineFinished = true
		cancel()
	case <-ctx.Done():
	}
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if e := server.Shutdown(shutdownCtx); e != nil {
		_ = server.Close()
	}
	// Runner returns after its generation has reaped ffmpeg and stopped readers.
	if !pipelineFinished {
		select {
		case <-pipelineDone:
		case <-time.After(15 * time.Second):
			if err == nil {
				err = fmt.Errorf("pipeline shutdown deadline exceeded")
			}
		}
	}
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
