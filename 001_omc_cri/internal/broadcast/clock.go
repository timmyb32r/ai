// Package broadcast owns the rolling timeline buffer, the broadcast head /
// real-time pacer, the ref-counted ingest lifecycle, and the ASR driver that
// fans one shared ingest out to many subscribers. The clock is injectable so
// pacing and lifecycle logic can be tested deterministically without sleeping.
package broadcast

import (
	"sync"
	"time"
)

// Clock is the injectable time source used throughout the broadcast package so
// pacing and lifecycle timing can be driven deterministically in tests.
type Clock interface {
	// Now returns the current time according to this clock.
	Now() time.Time
}

// RealClock is the production Clock backed by the wall clock.
type RealClock struct{}

// Now returns time.Now().
func (RealClock) Now() time.Time { return time.Now() }

// FakeClock is a Clock whose time only changes when Advance or Set is called,
// for deterministic tests. The zero value is usable and starts at the zero
// time; prefer NewFakeClock to start at a chosen instant. All methods are
// safe for concurrent use.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewFakeClock returns a FakeClock whose Now reports t until advanced or set.
func NewFakeClock(t time.Time) *FakeClock {
	return &FakeClock{now: t}
}

// Now returns the clock's current time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward by d. A negative d moves it backward.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Set forces the clock to report t from now on.
func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}
