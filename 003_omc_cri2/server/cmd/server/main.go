// CRI Radio Server — live Chinese radio with real-time ASR subtitles.
//
// Architecture:
//   - ffmpeg captures HLS radio stream → produces HLS segments + PCM
//   - whisper.cpp transcribes PCM → text with per-word timestamps
//   - gse tokenizes Chinese text → words
//   - CC-CEDICT enriches words → pinyin + translation
//   - Results written to filesystem as HLS + JSON metadata
//   - HTTP API serves: HLS audio, metadata JSON, SSE subtitles, status
//
// Single source of truth for time:
//   - ffmpeg's #EXT-X-PROGRAM-DATE-TIME (system clock) → HLS playlist
//   - Go server reads playlist → uses same epoch timestamps for metadata
//   - Client correlates: windowStartTimeMs + currentPosition ↔ metadata timeline_start_sec
package main

import (
	"context"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/criradio/server/internal/api"
	"github.com/criradio/server/internal/asr"
	"github.com/criradio/server/internal/config"
	"github.com/criradio/server/internal/dictionary"
	"github.com/criradio/server/internal/ingest"
	"github.com/criradio/server/internal/logging"
	"github.com/criradio/server/internal/pipeline"
	"github.com/criradio/server/internal/storage"
	"github.com/criradio/server/internal/tokenizer"
)

func main() {
	cfg := config.FromEnv()

	logger := logging.NewProductionLogger(cfg.LogLevel)
	logger.Info("main", "starting",
		"channel", cfg.ChannelURL,
		"output", cfg.OutputDir,
		"addr", cfg.Addr,
	)

	if err := cfg.Validate(); err != nil {
		logger.Error("main", "config_invalid", "err", err)
		os.Exit(1)
	}

	// ── Initialize modules ──────────────────────────────────────────────

	// Tokenizer
	tok, err := tokenizer.New(cfg.GSEDictDir)
	if err != nil {
		logger.Error("main", "tokenizer_init_failed", "err", err)
		os.Exit(1)
	}
	defer tok.Close()

	// Dictionary
	dict, err := dictionary.Load(cfg.DictPath)
	if err != nil {
		logger.Error("main", "dictionary_init_failed", "err", err)
		os.Exit(1)
	}
	defer dict.Close()
	logger.Info("main", "dictionary_loaded", "hits", dict.Stats().Hits, "total", dict.Stats().Total)

	// Storage
	store, err := storage.New(cfg.OutputDir)
	if err != nil {
		logger.Error("main", "storage_init_failed", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	// ASR (configurable engine via ASR_ENGINE/ASR_MODEL env; fallback to mock)
	transcriber, err := asr.NewTranscriber(asr.Config{
		Engine:        cfg.AsrEngine,
		ModelPath:     cfg.ModelPath,
		ModelCodename: cfg.AsrModel,
		Language:      "zh",
		Threads:       2,
		MaxContext:    -1,
	})
	if err != nil {
		logger.Warn("main", "asr_init_failed", "err", err, "fallback", "mock")
		transcriber = asr.NewMockTranscriber()
	}
	defer transcriber.Close()

	// Ingest
	ingestor, err := ingest.New(ingest.Config{
		ChannelURL: cfg.ChannelURL,
		OutputDir:  cfg.OutputDir,
		HLSTime:    cfg.HLSTime,
		HLSWindow:  cfg.HLSWindow,
	})
	if err != nil {
		logger.Error("main", "ingest_init_failed", "err", err)
		os.Exit(1)
	}

	// HTTP API
	apiServer := &api.Server{
		Store:   store,
		Logger:  logger,
		HLSDir:  cfg.OutputDir + "/hls",
		MetaDir: cfg.OutputDir + "/metadata",
	}

	// ── pprof server (diagnostics) ────────────────────────────────────
	pprofServer := &http.Server{
		Addr:         ":6060",
		Handler:      http.DefaultServeMux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}
	go func() {
		runtime.SetBlockProfileRate(1)
		runtime.SetMutexProfileFraction(1)
		logger.Info("main", "pprof_listening", "addr", ":6060")
		if err := pprofServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("main", "pprof_error", "err", err)
		}
	}()

	// ── Start HTTP server ───────────────────────────────────────────────

	httpServer := &http.Server{
		Addr:         cfg.Addr,
		Handler:      apiServer.NewRouter(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		logger.Info("main", "http_listening", "addr", cfg.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("main", "http_error", "err", err)
		}
	}()

	// ── Start processing pipeline ──────────────────────────────────────

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pipe := &pipeline.Pipeline{
		Ingestor:    ingestor,
		Transcriber: transcriber,
		Tokenizer:   tok,
		Dictionary:  dict,
		Store:       store,
		Logger:      logger,
		OutputDir:   cfg.OutputDir,
		HLSTime:     cfg.HLSTime,
	}

	go func() {
		backoff := 2 * time.Second
		const maxBackoff = 60 * time.Second

		for {
			if ctx.Err() != nil {
				return
			}

			logger.Info("main", "pipeline_starting")
			err := pipe.Run(ctx)
			if ctx.Err() != nil {
				return
			}

			if err != nil {
				logger.Error("main", "pipeline_error", "err", err, "restart_in", backoff)
			}
			// pipe.Run always returns non-nil; ctx.Err() caught above.
			_ = ingestor.Stop()

			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}

			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}()

	// ── Wait for shutdown signal ──────────────────────────────────────

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	logger.Info("main", "shutdown_signal", "signal", sig.String())

	// Graceful shutdown
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("main", "http_shutdown_error", "err", err)
	}
	if err := pprofServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("main", "pprof_shutdown_error", "err", err)
	}
	if err := ingestor.Stop(); err != nil {
		logger.Warn("main", "ingest_stop_error", "err", err)
	}

	logger.Info("main", "stopped")
}
