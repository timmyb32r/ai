package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/criradio/server/internal/asr"
	"github.com/criradio/server/internal/storage"
)

func queued(t *testing.T, dir, name string, start float64, age time.Duration) *work {
	t.Helper()
	p := filepath.Join(dir, name)
	if e := os.WriteFile(p, []byte("audio"), 0600); e != nil {
		t.Fatal(e)
	}
	return &work{mediaSegment: mediaSegment{Duration: 1, Start: start}, path: p, queued: time.Now().Add(-age), generation: name, pcm: make([]float32, sampleRate)}
}
func TestQueueCapacityExpiryAndLateResult(t *testing.T) {
	dir := t.TempDir()
	var dropped uint64
	q := &workQueue{limit: 2, notify: make(chan struct{}, 1), dropped: func(n uint64) { dropped += n }}
	a := queued(t, dir, "a", 1, 0)
	b := queued(t, dir, "b", 2, 0)
	c := queued(t, dir, "c", 3, 0)
	q.offer(a)
	q.offer(b)
	q.offer(c)
	if w, sec := q.head(); w != b || sec != 2 || dropped != 1 {
		t.Fatal(w, sec, dropped)
	}
	called := false
	if ok, e := q.finish(a, func() error { called = true; return nil }); ok || e != nil || called {
		t.Fatal("late result published")
	}
	if _, e := os.Stat(a.path); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("dropped file retained")
	}
	b.queued = time.Now().Add(-3 * time.Second)
	c.queued = b.queued
	if w, _ := q.head(); w != nil || dropped != 3 {
		t.Fatal("expired backlog retained", dropped)
	}
}

type recoveringASR struct{ ready atomic.Bool }

func (a *recoveringASR) Transcribe(context.Context, []float32) (*asr.Result, error) {
	if !a.ready.Load() {
		return nil, errors.New("model restarting")
	}
	return &asr.Result{}, nil
}

func TestCompletedQueueSurvivesCaptureGeneration(t *testing.T) {
	dir := t.TempDir()
	store, e := storage.Open(filepath.Join(dir, "data"), storage.Options{MaxBytes: 64 << 20})
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	a := &recoveringASR{}
	r := &Runner{Store: store, ASR: a, Text: silentEnricher{}, Config: Config{InferenceTimeout: time.Second}}
	r.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	q := &workQueue{limit: 300, notify: make(chan struct{}, 1), dropped: func(uint64) {}}
	now := float64(time.Now().Unix())
	q.offer(queued(t, dir, "old-capture", now-10, 0))
	q.offer(queued(t, dir, "new-capture", now-2, 0))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.consume(ctx, q) }()
	time.Sleep(50 * time.Millisecond)
	a.ready.Store(true)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && r.Snapshot().Published < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if r.Snapshot().Published != 2 {
		t.Fatal("backlog lost across capture generations", r.Snapshot())
	}
	rows, e := store.Playlist(context.Background())
	if e != nil || len(rows) != 2 {
		t.Fatal(rows, e)
	}
	if !rows[1].Discontinuity {
		t.Fatal("capture gap not marked")
	}
}
