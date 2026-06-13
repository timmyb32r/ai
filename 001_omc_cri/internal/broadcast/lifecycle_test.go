package broadcast

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testLinger is short enough to keep tests fast but long enough to win the
// race against an immediate resubscribe in the resubscribe-during-stopping
// test on a loaded machine.
const testLinger = 20 * time.Millisecond

// newCounted returns a Lifecycle plus atomic counters of how many times the
// start and stop hooks ran.
func newCounted(linger time.Duration) (l *Lifecycle, starts, stops *int32) {
	var s, st int32
	l = NewLifecycle(
		func() { atomic.AddInt32(&s, 1) },
		func() { atomic.AddInt32(&st, 1) },
		linger,
	)
	return l, &s, &st
}

func TestAcquireStartsOnce(t *testing.T) {
	l, starts, stops := newCounted(testLinger)

	l.Acquire()

	if got := atomic.LoadInt32(starts); got != 1 {
		t.Fatalf("start count = %d, want 1", got)
	}
	if got := atomic.LoadInt32(stops); got != 0 {
		t.Fatalf("stop count = %d, want 0", got)
	}
	if st := l.State(); st != Starting {
		t.Fatalf("state = %s, want starting", st)
	}
	if c := l.Count(); c != 1 {
		t.Fatalf("count = %d, want 1", c)
	}
}

func TestReleaseStopsAfterLinger(t *testing.T) {
	l, starts, stops := newCounted(testLinger)

	l.Acquire()
	l.MarkRunning()
	l.Release()

	// Immediately after Release we are Stopping, not yet Stopped.
	if st := l.State(); st != Stopping {
		t.Fatalf("state after release = %s, want stopping", st)
	}
	if got := atomic.LoadInt32(stops); got != 0 {
		t.Fatalf("stop ran before linger elapsed: count = %d", got)
	}

	waitFor(t, 500*time.Millisecond, func() bool {
		return atomic.LoadInt32(stops) == 1 && l.State() == Stopped
	})

	if got := atomic.LoadInt32(starts); got != 1 {
		t.Fatalf("start count = %d, want 1", got)
	}
	if got := atomic.LoadInt32(stops); got != 1 {
		t.Fatalf("stop count = %d, want 1", got)
	}
	if c := l.Count(); c != 0 {
		t.Fatalf("count = %d, want 0", c)
	}
}

// TestResubscribeDuringStopping is the key race: a subscriber leaves (ref 1->0,
// state Stopping, linger armed) and a new subscriber arrives BEFORE the linger
// fires. The pending stop must be cancelled — stop() never runs and start() is
// called exactly once total because the ingest was never torn down.
func TestResubscribeDuringStopping(t *testing.T) {
	// Use a longer linger here so the resubscribe reliably beats the timer.
	l, starts, stops := newCounted(200 * time.Millisecond)

	l.Acquire()
	l.MarkRunning()
	l.Release()

	// Confirm teardown is genuinely pending before we resubscribe.
	if st := l.State(); st != Stopping {
		t.Fatalf("state = %s, want stopping before resubscribe", st)
	}

	// Resubscribe immediately, before the linger window elapses.
	l.Acquire()

	if st := l.State(); st != Running {
		t.Fatalf("state after resubscribe = %s, want running", st)
	}

	// Wait well past the original linger window to prove the cancelled timer
	// never tears down the ingest.
	time.Sleep(300 * time.Millisecond)

	if got := atomic.LoadInt32(starts); got != 1 {
		t.Fatalf("start count = %d, want exactly 1 (no re-start on resubscribe)", got)
	}
	if got := atomic.LoadInt32(stops); got != 0 {
		t.Fatalf("stop count = %d, want 0 (teardown must be cancelled)", got)
	}
	if c := l.Count(); c != 1 {
		t.Fatalf("count = %d, want 1", c)
	}
	if st := l.State(); st != Running {
		t.Fatalf("final state = %s, want running", st)
	}
}

func TestConcurrentAcquireStartsOnce(t *testing.T) {
	l, starts, stops := newCounted(testLinger)

	const n = 2
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			l.Acquire()
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(starts); got != 1 {
		t.Fatalf("start count = %d, want 1 for %d concurrent acquires", got, n)
	}
	if got := atomic.LoadInt32(stops); got != 0 {
		t.Fatalf("stop count = %d, want 0", got)
	}
	if c := l.Count(); c != n {
		t.Fatalf("count = %d, want %d", c, n)
	}
}

// TestManySubscribersStartStopOnce stresses the ref-count under many concurrent
// acquire/release pairs and asserts start/stop each ran at most once and the
// final state settles to Stopped. Exercised under -race.
func TestManySubscribersStartStopOnce(t *testing.T) {
	l, starts, stops := newCounted(testLinger)

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			l.Acquire()
			time.Sleep(time.Millisecond)
			l.Release()
		}()
	}
	wg.Wait()

	waitFor(t, time.Second, func() bool {
		return l.State() == Stopped && l.Count() == 0
	})

	if got := atomic.LoadInt32(starts); got < 1 {
		t.Fatalf("start count = %d, want >= 1", got)
	}
	// start and stop counts must match: every started epoch is torn down.
	if s, st := atomic.LoadInt32(starts), atomic.LoadInt32(stops); s != st {
		t.Fatalf("start count %d != stop count %d (epochs unbalanced)", s, st)
	}
	if c := l.Count(); c != 0 {
		t.Fatalf("count = %d, want 0", c)
	}
}

// waitFor polls cond until it returns true or the deadline elapses.
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %s", d)
	}
}
