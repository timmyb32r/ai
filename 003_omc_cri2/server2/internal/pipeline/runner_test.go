package pipeline

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/criradio/server/internal/asr"
	"github.com/criradio/server/internal/models"
	"github.com/criradio/server/internal/storage"
)

func TestRingBoundedAndAbsolute(t *testing.T) {
	r := newPCMRing(1)
	r.append(make([]float32, sampleRate))
	r.append([]float32{1, 2, 3})
	if _, ok := r.read(0, 2); ok {
		t.Fatal("overwritten samples retained")
	}
	p, ok := r.read(sampleRate, sampleRate+3)
	if !ok || p[2] != 3 {
		t.Fatal(p, ok)
	}
	if _, ok = r.read(sampleRate+2, sampleRate+4); ok {
		t.Fatal("read future samples")
	}
}

func TestAlignedTokensDoNotConfuseRunes(t *testing.T) {
	text, ts, e := alignedText(&asr.Result{Text: "你好世界", Tokens: []string{"你好", "世界"}, Timestamps: []float64{0.3, 2.8}})
	if e != nil || text != "你好世界" || len(ts) != 4 || ts[0] != ts[1] || ts[2] != 2.8 {
		t.Fatalf("%s %v %v", text, ts, e)
	}
	text, ts, e = alignedText(&asr.Result{Text: "中国 hello world", Tokens: []string{"中", "国", "hello", "world"}, Timestamps: []float64{0, 1, 2, 3}})
	if e != nil || text != "中国hello world" || len(ts) != len([]rune(text)) {
		t.Fatal(text, ts, e)
	}
	for _, ts := range [][]float64{{math.NaN()}, {-1}, {2, 1}} {
		tok := make([]string, len(ts))
		for i := range tok {
			tok[i] = "词"
		}
		if _, _, e = alignedText(&asr.Result{Text: "词", Tokens: tok, Timestamps: ts}); e == nil {
			t.Fatal("invalid times accepted")
		}
	}
}

func TestParseActualDurationAndFFmpegTimezone(t *testing.T) {
	p := filepath.Join(t.TempDir(), "live.m3u8")
	body := "#EXTM3U\n#EXT-X-PROGRAM-DATE-TIME:2026-09-18T12:00:00.123+0000\n#EXTINF:3.024,\n000000000.ts\n#EXTINF:2.988,\n000000001.ts\n"
	if e := os.WriteFile(p, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
	s, e := readMediaPlaylist(p)
	if e != nil || len(s) != 2 {
		t.Fatal(s, e)
	}
	if math.Abs(s[1].Start-s[0].Start-3.024) > 1e-6 {
		t.Fatal("duration lost")
	}
	if e = os.WriteFile(p, []byte(strings.ReplaceAll(body, "000000000.ts", "../escape.ts")), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = readMediaPlaylist(p); e == nil {
		t.Fatal("unsafe path accepted")
	}
}

type silentRecognizer struct{}

func (silentRecognizer) Transcribe(context.Context, []float32) (*asr.Result, error) {
	return &asr.Result{}, nil
}

type silentEnricher struct{}

func (silentEnricher) Process(_ context.Context, _ *asr.Result, start, duration float64) (*models.TranscriptSegment, error) {
	return &models.TranscriptSegment{TimelineStartSec: start, TimelineEndSec: start + duration, Words: []models.WordEntry{}}, nil
}

// Uses the actual ffmpeg two-output pipeline. This catches pipe deadlocks,
// private/public file lifetime mistakes and invalid generated media durations.
func TestFFmpegPublicationAndCancellation(t *testing.T) {
	ffmpeg, e := exec.LookPath("ffmpeg")
	if e != nil {
		t.Skip("ffmpeg unavailable")
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "input.wav")
	out, e := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=16000", "-t", "10", "-y", input).CombinedOutput()
	if e != nil {
		t.Fatalf("fixture %s: %v", out, e)
	}
	store, e := storage.Open(filepath.Join(dir, "data"), storage.Options{MaxBytes: 10 << 20})
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &Runner{Config: Config{URL: input, FFmpeg: ffmpeg, OutputDir: filepath.Join(dir, "data"), SegmentSeconds: 1, QueueSeconds: 10, InferenceTimeout: time.Second, StallTimeout: 5 * time.Second}, Store: store, ASR: silentRecognizer{}, Text: silentEnricher{}}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if r.Snapshot().Published >= 3 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pipeline failed to stop")
	}
	if r.Snapshot().Published < 3 {
		t.Fatalf("no audio publication: %+v", r.Snapshot())
	}
	segs, e := store.Playlist(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range segs {
		if _, e = os.Stat(filepath.Join(dir, "data", "hls", s.TSFile)); e != nil {
			t.Fatal(e)
		}
		if d := s.TimelineEndSec - s.TimelineStartSec; d < 0.9 || d > 1.1 {
			t.Fatal("incorrect duration", d)
		}
		if s.TextZh != "" || s.HasContent {
			t.Fatal("manufactured text")
		}
	}
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Errorf("goroutines before=%d after=%d", before, after)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "data", "spool", "*"))
	if len(files) != 0 {
		t.Fatal("unfinished generation remains", files)
	}
	t.Log(fmt.Sprintf("published=%d; generation reaped", len(segs)))
}

// Closing stdout is not proof a subprocess exited: reconnect must still have a
// deadline, and the previous child must be reaped before the next generation.
func TestCaptureReapsChildThatClosesStdoutAndHangs(t *testing.T) {
	if _, e := exec.LookPath("python3"); e != nil {
		t.Skip("python3 unavailable")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-ffmpeg")
	body := "#!/bin/sh\nexec python3 -c 'import os,time; os.close(1); time.sleep(30)'\n"
	if e := os.WriteFile(fake, []byte(body), 0700); e != nil {
		t.Fatal(e)
	}
	pending := filepath.Join(dir, "pending")
	if e := os.Mkdir(pending, 0700); e != nil {
		t.Fatal(e)
	}
	r := &Runner{Config: Config{FFmpeg: fake, URL: "unused", SegmentSeconds: 1, QueueSeconds: 10, StallTimeout: 5 * time.Second}}
	q := &workQueue{limit: 10, notify: make(chan struct{}, 1), dropped: func(uint64) {}}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	began := time.Now()
	err := r.capture(ctx, dir, pending, q)
	if err == nil || ctx.Err() != nil || time.Since(began) > 5*time.Second {
		t.Fatal("EOF did not bound child lifetime", err, time.Since(began))
	}
}
