package pipeline

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/criradio/server/internal/api"
	"github.com/criradio/server/internal/storage"
)

// CRI_SOAK_SECONDS=600 go test -race ./internal/pipeline -run TestSoak -v -timeout 15m
// Actual ffmpeg repeatedly ends/restarts while five clients read and retention
// removes old recordings. Inference is deterministic here; real model reuse and
// quality are checked separately by services/test_real_asr.py.
func TestSoak(t *testing.T) {
	raw := os.Getenv("CRI_SOAK_SECONDS")
	if raw == "" {
		t.Skip("opt-in timed resource soak")
	}
	seconds, e := strconv.Atoi(raw)
	if e != nil || seconds < 30 || seconds > 86400 {
		t.Fatal("invalid soak duration")
	}
	ffmpeg, e := exec.LookPath("ffmpeg")
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	initialGo, initialFD := runtime.NumGoroutine(), processFDCount(t)
	input := filepath.Join(dir, "source.wav")
	if b, e := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=16000", "-t", "12", "-y", input).CombinedOutput(); e != nil {
		t.Fatal(string(b), e)
	}
	store, e := storage.Open(filepath.Join(dir, "data"), storage.Options{Retention: 30 * time.Second, MaxBytes: 64 << 20, SnapshotTTL: 10 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(seconds)*time.Second)
	defer cancel()
	runner := &Runner{Config: Config{URL: input, FFmpeg: ffmpeg, OutputDir: filepath.Join(dir, "data"), SegmentSeconds: 1, QueueSeconds: 10, InferenceTimeout: time.Second, StallTimeout: 5 * time.Second}, Store: store, ASR: silentRecognizer{}, Text: silentEnricher{}}
	apiServer := &api.Server{Store: store, Status: func() any { return map[string]any{"status": runner.Snapshot().Status} }}
	web := httptest.NewServer(apiServer.Handler())
	defer web.Close()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	var clients sync.WaitGroup
	for i := 0; i < 5; i++ {
		clients.Add(1)
		go func() {
			defer clients.Done()
			tr := http.DefaultTransport.(*http.Transport).Clone()
			tr.MaxConnsPerHost = 2
			client := &http.Client{Transport: tr, Timeout: 2 * time.Second}
			defer tr.CloseIdleConnections()
			for ctx.Err() == nil {
				for _, path := range []string{"/hls/playlist.m3u8", "/api/status", "/api/segments/batch?last=3&lite=true"} {
					req, _ := http.NewRequestWithContext(ctx, "GET", web.URL+path, nil)
					res, e := client.Do(req)
					if e == nil {
						_, _ = io.Copy(io.Discard, res.Body)
						res.Body.Close()
					}
				}
				sleep(ctx, 100*time.Millisecond)
			}
		}()
	}
	ticks := time.NewTicker(15 * time.Second)
	defer ticks.Stop()
	baselineFD, baselineGo := 0, 0
	var baselineHeap uint64
	var goroutineWindow []int
	sample := func() {
		runtime.GC()
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		fdCount := processFDCount(t)
		g := runtime.NumGoroutine()
		goroutineWindow = append(goroutineWindow, g)
		if len(goroutineWindow) > 4 {
			goroutineWindow = goroutineWindow[1:]
		}
		s, _ := store.Stats(context.Background())
		state := runner.Snapshot()
		t.Logf("published=%d restarts=%d files=%d bytes=%d fd=%d goroutines=%d heapMiB=%.2f", state.Published, state.Restarts, s.Total, s.Bytes, fdCount, g, float64(mem.HeapAlloc)/(1<<20))
		if baselineFD == 0 {
			baselineFD = fdCount
			baselineGo = g
			baselineHeap = mem.HeapAlloc
		} else {
			if fdCount > baselineFD+12 {
				t.Errorf("growing descriptors: %d -> %d", baselineFD, fdCount)
			}
			// Request handlers and transport dialers briefly overlap. A leak
			// raises the floor across a minute, not merely one sampled peak.
			floor := g
			for _, n := range goroutineWindow {
				floor = min(floor, n)
			}
			if g > 128 || len(goroutineWindow) == 4 && floor > baselineGo+15 {
				_ = pprof.Lookup("goroutine").WriteTo(os.Stderr, 2)
				t.Errorf("sustained goroutine growth: baseline=%d samples=%v", baselineGo, goroutineWindow)
			}
			if mem.HeapAlloc > baselineHeap+64<<20 {
				t.Errorf("growing heap: %d -> %d", baselineHeap, mem.HeapAlloc)
			}
		}
		if s.Total > 40 || s.Bytes > 64<<20 {
			t.Errorf("retention is not bounded: %+v", s)
		}
	}
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-ticks.C:
			sample()
		}
	}
	clients.Wait()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("generation failed to terminate")
	}
	sample()
	if runner.Snapshot().Published < 5 || runner.Snapshot().Restarts < 1 {
		t.Fatal("soak did not exercise publication and restart", runner.Snapshot())
	}
	web.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Also require convergence after load stops. This catches leaked workers
	// even when their count stayed below the steady-state growth threshold.
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > initialGo+2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	finalGo, finalFD := runtime.NumGoroutine(), processFDCount(t)
	t.Logf("after shutdown: goroutines=%d→%d fd=%d→%d", initialGo, finalGo, initialFD, finalFD)
	if finalGo > initialGo+2 || finalFD > initialFD+2 {
		_ = pprof.Lookup("goroutine").WriteTo(os.Stderr, 2)
		t.Errorf("resources did not return after shutdown: goroutines=%d→%d fd=%d→%d", initialGo, finalGo, initialFD, finalFD)
	}
}

func processFDCount(t *testing.T) int {
	t.Helper()
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("/usr/sbin/lsof", "-n", "-P", "-a", "-p", strconv.Itoa(os.Getpid()), "-Ff").Output()
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, line := range strings.Split(string(out), "\n") {
			if len(line) > 1 && line[0] == 'f' {
				if _, err := strconv.Atoi(line[1:]); err == nil {
					count++
				}
			}
		}
		return count
	}
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(fds)
}
