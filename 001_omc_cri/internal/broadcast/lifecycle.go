package broadcast

import (
	"sync"
	"time"
)

// Lifecycle is the ref-counted ingest state machine. A single mutex owns
// {refCount,state,timer,epoch}; the first Acquire starts ingest (start hook),
// and the last Release schedules a stop after the linger window (stop hook), so
// a rapid reconnect does not thrash a multi-minute ingest. A new Acquire during
// the linger/Stopping window cancels the pending teardown and returns to the
// running side without re-running start (ingest is still alive).
//
// States advance Stopped -> Starting -> Warming -> Running on the start side
// (the broadcast drives Starting->Warming->Running via SetState), and
// Running/Warming -> Stopping -> Stopped on the teardown side. start and stop
// are each invoked at most once per epoch, where an epoch is one
// start()..stop() ingest cycle.
type Lifecycle struct {
	mu       sync.Mutex
	refCount int
	state    LifecycleState
	start    func()
	stop     func()
	linger   time.Duration
	timer    *time.Timer
	// epoch increments on every start()->stop() boundary (i.e. each time stop
	// actually runs). The pending linger timer captures the epoch it was armed
	// in and is a no-op if the epoch has since changed, guarding against a
	// stale timer firing after a cancel+rearm or a resubscribe.
	epoch uint64
}

// NewLifecycle constructs a Lifecycle that calls start when the ref-count rises
// from zero and stop after the ref-count returns to zero and the linger window
// elapses without a new Acquire.
func NewLifecycle(start, stop func(), linger time.Duration) *Lifecycle {
	return &Lifecycle{
		state:  Stopped,
		start:  start,
		stop:   stop,
		linger: linger,
	}
}

// Acquire registers a subscriber, incrementing the ref-count. On the 0->1
// transition from Stopped it sets Starting and runs the start hook. If a
// teardown is pending (state Stopping), it cancels the linger timer and returns
// the state to Running without re-running start — the ingest is still alive.
// This is the resubscribe-during-stopping race fix.
func (l *Lifecycle) Acquire() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.refCount++
	if l.refCount > 1 {
		// Already running with other subscribers; nothing to do.
		return
	}

	// refCount transitioned 0 -> 1.
	if l.state == Stopping {
		if l.timer != nil {
			// Teardown was pending but the linger timer has NOT fired yet: the
			// ingest is still alive, so cancel the timer and resume without
			// re-running start(). This is the resubscribe-during-stopping fix.
			l.cancelTimerLocked()
			l.state = Running
			return
		}
		// Timer already fired and teardown (cancel + Wait) is in flight in
		// onLingerExpired. We must NOT start() concurrently with that Wait, so just
		// move to Starting and let onLingerExpired restart a fresh epoch once it
		// has finished draining the old one (it re-checks refCount post-Wait).
		l.state = Starting
		return
	}

	// Cold start: ingest is not running. Begin a new epoch.
	l.state = Starting
	if l.start != nil {
		l.start()
	}
}

// Release unregisters a subscriber, decrementing the ref-count. On the 1->0
// transition it sets Stopping and arms a linger timer; when the timer fires and
// the ref-count is still zero, it runs the stop hook and sets Stopped. A new
// Acquire before the timer fires cancels it (see Acquire).
func (l *Lifecycle) Release() {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.refCount == 0 {
		// Defensive: unbalanced Release. Ignore.
		return
	}
	l.refCount--
	if l.refCount > 0 {
		return
	}

	// refCount transitioned 1 -> 0. Arm the lingered teardown.
	l.cancelTimerLocked()
	l.state = Stopping
	armedEpoch := l.epoch
	l.timer = time.AfterFunc(l.linger, func() { l.onLingerExpired(armedEpoch) })
}

// onLingerExpired runs when a linger timer fires. It tears the ingest down only
// if the lifecycle is still in the same epoch the timer was armed in and the
// ref-count is still zero (no resubscribe arrived).
//
// Ordering (Fix 2): the stop hook (cancel + WAIT for this epoch's goroutines)
// runs AFTER l.mu is released, and the lifecycle only transitions to Stopped
// AFTER stop returns. Thus, by the time State() observes Stopped, the prior
// epoch's producers have fully exited — so a later Acquire's start() begins from
// a genuinely drained, clean state. Running stop() off l.mu is required because
// the epoch goroutines call back into the lifecycle (SetState via
// MarkWarming/MarkRunning); calling stop() under l.mu would deadlock its Wait.
//
// During the Wait window the state stays Stopping but the timer is cleared, so a
// resubscribe that races the teardown takes Acquire's cold-start path (which
// drains this dying epoch before relaunching) rather than reusing dead ingest.
func (l *Lifecycle) onLingerExpired(armedEpoch uint64) {
	l.mu.Lock()

	if l.epoch != armedEpoch {
		// A cancel+rearm or resubscribe happened; this timer is stale.
		l.mu.Unlock()
		return
	}
	if l.refCount != 0 || l.state != Stopping {
		// A subscriber returned (Acquire would have cancelled us, but guard
		// against a race where the timer already fired): do not tear down.
		l.mu.Unlock()
		return
	}

	// Commit the teardown: clear the timer (so a racing Acquire cold-starts a new
	// epoch) and close this epoch so any stale timer is a no-op. Keep the state
	// Stopping until stop() has finished waiting for the goroutines.
	l.timer = nil
	l.epoch++
	stop := l.stop
	l.mu.Unlock()

	if stop != nil {
		stop() // cancel + Wait for this epoch's goroutines (off l.mu).
	}

	l.mu.Lock()
	if l.refCount > 0 {
		// A resubscribe arrived during the Wait. Acquire moved us to Starting but
		// deliberately did not call start() (it must not run concurrently with the
		// teardown). Now that the old epoch is fully drained, start a fresh one
		// here, sequentially after stop() — no overlap, so the buffer/ASR reset is
		// safe.
		if l.state != Stopped {
			l.state = Starting
		}
		if l.start != nil {
			l.start()
		}
	} else {
		// No resubscribe: settle to Stopped.
		l.state = Stopped
	}
	l.mu.Unlock()
}

// cancelTimerLocked stops and clears the pending linger timer, if any. Callers
// must hold l.mu. It does not advance the epoch; the timer callback already
// guards on epoch and state, so a fire that races a successful Stop is a no-op.
func (l *Lifecycle) cancelTimerLocked() {
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
}

// SetState advances the running-side state. The broadcast calls this to drive
// Starting -> Warming -> Running as the buffer fills. It is ignored unless the
// lifecycle is currently on the running side (Starting/Warming/Running) and the
// target is one of those states, so a late SetState cannot resurrect a torn-down
// or tearing-down ingest.
func (l *Lifecycle) SetState(s LifecycleState) {
	l.mu.Lock()
	defer l.mu.Unlock()

	switch l.state {
	case Starting, Warming, Running:
	default:
		// Stopped or Stopping: ignore running-side transitions.
		return
	}
	switch s {
	case Starting, Warming, Running:
		l.state = s
	default:
		// Refuse to set Stopped/Stopping via SetState; that is owned by
		// Acquire/Release/onLingerExpired.
	}
}

// MarkWarming is a convenience for SetState(Warming).
func (l *Lifecycle) MarkWarming() { l.SetState(Warming) }

// MarkRunning is a convenience for SetState(Running).
func (l *Lifecycle) MarkRunning() { l.SetState(Running) }

// State returns the current lifecycle state. Safe for concurrent use.
func (l *Lifecycle) State() LifecycleState {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state
}

// Count returns the current subscriber ref-count. Safe for concurrent use.
func (l *Lifecycle) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.refCount
}

// ForceStopped immediately transitions the lifecycle to Stopped, resetting the
// ref-count and cancelling any pending linger timer. It is used by Broadcast's
// ForceStop during graceful server shutdown so the always-on ingest pipeline is
// torn down cleanly without waiting for the linger window.
//
// Unlike onLingerExpired, ForceStopped does NOT call the stop hook — the caller
// (Broadcast.ForceStop) has already cancelled the epoch context and waited for
// goroutines. ForceStopped only cleans up lifecycle bookkeeping.
func (l *Lifecycle) ForceStopped() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cancelTimerLocked()
	l.state = Stopped
	l.refCount = 0
}
