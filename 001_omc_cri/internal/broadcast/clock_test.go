package broadcast

import (
	"sync"
	"testing"
	"time"
)

func TestRealClockNowAdvances(t *testing.T) {
	var c RealClock
	before := time.Now()
	got := c.Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("RealClock.Now()=%v outside [%v,%v]", got, before, after)
	}
}

func TestFakeClockNowIsStableUntilChanged(t *testing.T) {
	start := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	c := NewFakeClock(start)

	if got := c.Now(); !got.Equal(start) {
		t.Fatalf("Now()=%v, want %v", got, start)
	}
	// Reading again without advancing must not move time.
	if got := c.Now(); !got.Equal(start) {
		t.Fatalf("second Now()=%v, want %v", got, start)
	}
}

func TestFakeClockAdvance(t *testing.T) {
	start := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	c := NewFakeClock(start)

	c.Advance(90 * time.Second)
	if got, want := c.Now(), start.Add(90*time.Second); !got.Equal(want) {
		t.Fatalf("after Advance: Now()=%v, want %v", got, want)
	}

	c.Advance(-30 * time.Second)
	if got, want := c.Now(), start.Add(60*time.Second); !got.Equal(want) {
		t.Fatalf("after negative Advance: Now()=%v, want %v", got, want)
	}
}

func TestFakeClockSet(t *testing.T) {
	c := NewFakeClock(time.Unix(0, 0).UTC())
	target := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	c.Set(target)
	if got := c.Now(); !got.Equal(target) {
		t.Fatalf("after Set: Now()=%v, want %v", got, target)
	}
}

func TestFakeClockZeroValueUsable(t *testing.T) {
	var c FakeClock
	if got := c.Now(); !got.IsZero() {
		t.Fatalf("zero FakeClock Now()=%v, want zero time", got)
	}
	c.Advance(time.Hour)
	want := (time.Time{}).Add(time.Hour)
	if got := c.Now(); !got.Equal(want) {
		t.Fatalf("zero FakeClock after Advance: Now()=%v, want %v", got, want)
	}
}

// TestFakeClockConcurrent exercises the mutex guard under -race.
func TestFakeClockConcurrent(t *testing.T) {
	c := NewFakeClock(time.Unix(0, 0).UTC())
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); c.Advance(time.Millisecond) }()
		go func() { defer wg.Done(); _ = c.Now() }()
	}
	wg.Wait()
	if got, want := c.Now(), time.Unix(0, 0).UTC().Add(50*time.Millisecond); !got.Equal(want) {
		t.Fatalf("after concurrent advances: Now()=%v, want %v", got, want)
	}
}

// Compile-time assertions that both clocks satisfy Clock.
var (
	_ Clock = RealClock{}
	_ Clock = (*FakeClock)(nil)
)
