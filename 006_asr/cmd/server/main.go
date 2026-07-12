package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/timmyb32r/yt2srt/internal/api"
	"github.com/timmyb32r/yt2srt/internal/asr"
	"github.com/timmyb32r/yt2srt/internal/config"
	"github.com/timmyb32r/yt2srt/internal/logging"
	"github.com/timmyb32r/yt2srt/internal/storage"
	"github.com/timmyb32r/yt2srt/internal/worker"
)

//go:embed web/static/*
var staticFS embed.FS

func main() {
	cfg := config.FromEnv()
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	log := logging.New(os.Getenv("LOG_LEVEL"))

	// Create transcriber
	tr, err := asr.NewTranscriber(asr.Config{
		Engine:         cfg.Engine,
		ModelPath:      cfg.ModelPath,
		ModelCodename:  cfg.ModelCodename,
		Language:       "zh",
		SherpaOnnxPath: cfg.SherpaOnnxPath,
	})
	if err != nil {
		log.Error("main", "transcriber_init_failed", "err", err)
		os.Exit(1)
	}
	defer tr.Close()
	log.Info("main", "transcriber_ready", "model", cfg.ModelCodename)

	// Set up store and worker
	store := storage.NewInMemoryStore()
	wrk := worker.New(tr, store, log, cfg)

	// Start cleanup goroutine (every 5 min, remove jobs > 1 hour)
	api.StartCleanup(store, log, 5*time.Minute, 1*time.Hour)

	// Create static FS (sub to web/static/ so paths are "index.html" etc.)
	staticSub, err := fs.Sub(staticFS, "web/static")
	if err != nil {
		log.Error("main", "static_fs_error", "err", err)
		os.Exit(1)
	}

	// Set up HTTP
	handler := api.NewHandler(store, wrk, log, staticSub)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	srv := &http.Server{
		Addr:    cfg.Addr,
		Handler: mux,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		log.Info("main", "shutting_down", "signal", sig.String())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	log.Info("main", "starting", "addr", cfg.Addr, "mode", cfg.DeployMode)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Error("main", "server_error", "err", err)
		os.Exit(1)
	}
	log.Info("main", "stopped")
}
