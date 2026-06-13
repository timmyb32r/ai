// Command server is the CRI-radio live-subtitle server. It ingests the live
// HLS stream once, keeps a rolling buffer, transcribes silence-bounded speech
// regions offline, and fans real-time-paced MP3 audio plus SSE subtitles out to
// thin console clients.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/timmyb32r/001_omc_cri/internal/api"
	"github.com/timmyb32r/001_omc_cri/internal/broadcast"
	"github.com/timmyb32r/001_omc_cri/internal/chineseasr"
	"github.com/timmyb32r/001_omc_cri/internal/ingest"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	ffmpeg := flag.String("ffmpeg", "ffmpeg", "path to the ffmpeg binary")
	sherpa := flag.String("sherpa", "sherpa-onnx-offline", "path to the sherpa-onnx-offline binary")
	modelDir := flag.String("model-dir", "", "directory with the ASR model files and tokens.txt (required)")
	channelURL := flag.String("channel-url", "https://sk.cri.cn/905.m3u8", "live HLS stream URL")
	delay := flag.Duration("delay", 180*time.Second, "broadcast delay behind the live edge")
	_ = flag.Duration("buffer", 5*time.Minute, "rolling buffer window (reserved; eviction is delay+margin based)")
	_ = flag.Float64("silence-db", -30, "silencedetect noise floor in dB (reserved; chineseasr uses internal constants in v1)")
	_ = flag.Duration("silence-min", 500*time.Millisecond, "minimum silence duration for a boundary (reserved; chineseasr uses internal constants in v1)")
	flag.Parse()

	if *modelDir == "" {
		fmt.Fprintln(os.Stderr, "cri-radio: -model-dir is required (directory containing model.int8.onnx and tokens.txt)")
		os.Exit(1)
	}

	// Build the offline ASR transcriber. This validates that the binaries and
	// model files exist; fail fast with a clear message rather than discovering
	// the misconfiguration at first client connect.
	transcriber, err := chineseasr.New(chineseasr.Config{
		FFmpegPath:        *ffmpeg,
		SherpaOfflinePath: *sherpa,
		ModelDir:          *modelDir,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "cri-radio: failed to initialize ASR: %v\n", err)
		os.Exit(1)
	}

	ingestor := ingest.New(*ffmpeg, *channelURL, ingest.ExecProcess{})

	buf := &broadcast.Buffer{}
	// NewBroadcast rebuilds the lifecycle internally; pass nil start/stop and
	// only the linger duration (10s: short enough for snappy teardown, long
	// enough to absorb rapid reconnects).
	lc := broadcast.NewLifecycle(nil, nil, 10*time.Second)
	bc := broadcast.NewBroadcast(broadcast.RealClock{}, buf, lc, ingestor, transcriber, *delay, *channelURL)

	mux := api.NewMux(bc)
	srv := &http.Server{Addr: *addr, Handler: mux}

	// Graceful shutdown on SIGINT / SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("cri-radio: listening on %s  channel=%s  delay=%s", *addr, *channelURL, *delay)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("cri-radio: ListenAndServe: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("cri-radio: shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("cri-radio: shutdown: %v", err)
	}
}
