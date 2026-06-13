package broadcast

import (
	"context"
	"log"
	"math"
	"sync"
	"time"

	"github.com/timmyb32r/001_omc_cri/internal/chineseasr"
	"github.com/timmyb32r/001_omc_cri/internal/ingest"
)

// Pacer / fan-out tuning. The audio queue is sized for several seconds of CBR
// 64 kbit/s MP3 in ~chunkBytes pieces; the subtitle queue is smaller since
// events are sparse. Both are bounded so a stalled subscriber never blocks the
// shared producers (the pacer drops-oldest instead).
const (
	audioQueueCap = 32
	subsQueueCap  = 16

	// chunkBytes is the max MP3 bytes a pacer pulls per tick. At CBR 64 kbit/s
	// (8000 B/s) this is ~1s of audio per chunk, comfortably more than one tick
	// produces so we never starve.
	chunkBytes = 8192

	// mp3BytesPerSec is the CBR MP3 byte rate (64 kbit/s = 8000 B/s), matching
	// the ingestor's MP3 branch. The pacer uses it to cap each read so the
	// released blob never extends past the broadcast head.
	mp3BytesPerSec = 64 * 1000 / 8

	// pacerTick is the wall cadence at which each subscriber pacer re-reads the
	// clock and releases more content. Release is gated by the (injected) clock,
	// so this is only the polling granularity; tests advance a FakeClock and the
	// pacer picks it up on the next tick.
	pacerTick = 100 * time.Millisecond

	// evictMargin is extra retained history beyond the delay so a just-joined
	// subscriber starting at the broadcast head still finds its frame boundary.
	evictMargin = 30 * time.Second

	// evictTick is how often the maintenance loop trims the buffer and drives
	// the warming->running state transition.
	evictTick = 1 * time.Second
)

// Broadcast ties the timeline together: it runs one shared ingest into the
// Buffer, drives the ASR driver over the buffered PCM, maintains
// broadcastHead = bufferHead - delay, and fans real-time-paced MP3 plus
// Start-anchored subtitles out to all subscribers. The ingest lifecycle is
// ref-counted by subscriber count.
//
// Timeline model: the buffer timeline is "seconds since ingest start" (tsSec
// from the ingestor). ingestStartWall is clock.Now() at the first ingested
// bytes. bufferHead(now) = now.Sub(ingestStartWall); broadcastHead(now) =
// bufferHead(now) - delay. The clock advances at real time, so releasing
// content with timeline ts <= broadcastHead is naturally real-time paced.
type Broadcast struct {
	clock     Clock
	buf       *Buffer
	lifecycle *Lifecycle
	ingestor  *ingest.Ingestor
	asr       *ASR
	delay     time.Duration
	channel   string

	mu sync.Mutex
	// ingestStartWall is the wall instant (per the clock) of the first ingested
	// bytes; the buffer timeline's t=0. Zero until set. ingestStarted guards the
	// once-only set.
	ingestStartWall time.Time
	ingestStarted   bool
	// cancel tears down the ingest goroutine group; non-nil only while ingest is
	// running. Set by the start hook, cleared/called by the stop hook.
	cancel context.CancelFunc
	// epochWG tracks THIS epoch's ingest/ASR/maintain goroutines so stop can wait
	// for a clean exit before returning. It is a fresh per-epoch WaitGroup (set by
	// start alongside cancel, captured and waited on by stop) rather than a single
	// reused WaitGroup, so a slow epoch-1 teardown can never overlap an epoch-2
	// start (Fix 2). nil when ingest is not running.
	epochWG *sync.WaitGroup
}

// NewBroadcast wires the broadcast pipeline from its dependencies. This
// signature is FROZEN: (clock, buf, lifecycle, ingestor, transcriber, delay,
// channel). The transcriber is the offline ASR engine used by the ASR driver;
// broadcast imports the chineseasr root (no cycle, since chineseasr never
// imports broadcast).
//
// NewBroadcast installs the lifecycle start/stop hooks on the *passed-in*
// Lifecycle. The provided lifecycle's own start/stop (from NewLifecycle) are
// superseded; production constructs the Lifecycle with nil hooks and lets
// NewBroadcast own them. (We cannot reach into the frozen Lifecycle struct, so
// we re-create it.) See newLifecycleFor.
func NewBroadcast(
	clock Clock,
	buf *Buffer,
	lifecycle *Lifecycle,
	ingestor *ingest.Ingestor,
	transcriber *chineseasr.Transcriber,
	delay time.Duration,
	channel string,
) *Broadcast {
	b := &Broadcast{
		clock:    clock,
		buf:      buf,
		ingestor: ingestor,
		asr:      NewASR(transcriber, buf),
		delay:    delay,
		channel:  channel,
	}
	// Rebuild the lifecycle with our start/stop hooks, preserving the linger the
	// caller configured. We cannot mutate the frozen Lifecycle's unexported
	// hooks, so we own a lifecycle constructed here.
	b.lifecycle = NewLifecycle(b.start, b.stop, lingerOf(lifecycle))
	return b
}

// lingerOf extracts the linger from a caller-provided lifecycle so NewBroadcast
// can preserve it on the lifecycle it owns. A nil lifecycle yields the zero
// linger (immediate teardown).
func lingerOf(l *Lifecycle) time.Duration {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.linger
}

// start is the lifecycle start hook: it launches the ingest goroutine group
// (ingestor -> buffer, ASR over the buffer, and a maintenance loop driving the
// warming->running transition and eviction). Called by Lifecycle on the 0->1
// subscriber transition, under the lifecycle mutex, so it must not block.
func (b *Broadcast) start() {
	ctx, cancel := context.WithCancel(context.Background())

	// Clear any prior-epoch residue BEFORE launching this epoch's goroutines and
	// re-anchoring the timeline (Fix 2): the reused ingestor restarts its byte
	// clock at 0, so stale high-tsSec buffer frames and a stale ASR cursor would
	// corrupt the new zero-based timeline (and zero out epoch-2's subtitles).
	//
	// This is race-free without any extra synchronisation: the previous epoch's
	// stop() (cancel + WaitGroup join) has already completed before control
	// reaches here. In the cold-start path the lifecycle only settles to Stopped
	// after stop() returns; in the resubscribe-during-teardown path
	// onLingerExpired calls start() itself, sequentially after stop()'s Wait. No
	// prior-epoch goroutine is alive to race the reset.
	b.buf.Reset()
	b.asr.reset()

	wg := &sync.WaitGroup{}

	b.mu.Lock()
	// Reset the timeline anchor for this ingest epoch.
	b.ingestStarted = false
	b.ingestStartWall = time.Time{}
	b.cancel = cancel
	b.epochWG = wg
	b.mu.Unlock()

	wg.Add(3)

	// (a) Ingest: PCM/MP3 into the buffer; record ingestStartWall on first bytes.
	go func() {
		defer wg.Done()
		onPCM := func(tsSec float64, p []byte) {
			b.markIngestStarted()
			b.buf.AppendPCM(tsSec, p)
		}
		onMP3 := func(tsSec float64, p []byte) {
			b.markIngestStarted()
			b.buf.AppendMP3(tsSec, p)
		}
		if err := b.ingestor.Run(ctx, onPCM, onMP3); err != nil && ctx.Err() == nil {
			log.Printf("broadcast: ingest: %v", err)
		}
	}()

	// (b) ASR loop over the buffered PCM.
	go func() {
		defer wg.Done()
		if err := b.asr.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("broadcast: asr: %v", err)
		}
	}()

	// (c) Maintenance: drive Warming->Running and evict the rolling window.
	go func() {
		defer wg.Done()
		b.maintain(ctx)
	}()
}

// stop is the lifecycle stop hook: it cancels THIS epoch's ingest goroutine
// group and then WAITS for those goroutines (ingest, ASR, maintain) to fully
// exit before returning (Fix 2). Waiting guarantees epoch-1's onPCM/onMP3/ASR
// closures can no longer append into the buffer once stop returns, so the next
// epoch begins from a genuinely drained state and a clean, zero-based timeline.
//
// stop is invoked by Lifecycle.onLingerExpired AFTER it releases l.mu (see that
// method), so this Wait does not hold the lifecycle mutex; the goroutines may
// call SetState (MarkWarming/MarkRunning) as they observe cancellation without
// deadlocking against the teardown. The lifecycle only settles to Stopped after
// this returns, so an Acquire that observes Stopped sees a fully drained epoch.
func (b *Broadcast) stop() {
	b.mu.Lock()
	cancel := b.cancel
	wg := b.epochWG
	b.cancel = nil
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if wg != nil {
		wg.Wait()
	}
	// Drop the drained epoch handle. onLingerExpired only restarts (and thus only
	// installs a new epochWG) AFTER this stop() returns, so there is no concurrent
	// new epoch to clobber.
	b.mu.Lock()
	if b.epochWG == wg {
		b.epochWG = nil
	}
	b.mu.Unlock()
}

// markIngestStarted records the timeline anchor on the first ingested bytes and
// drives the lifecycle into Warming. Idempotent.
func (b *Broadcast) markIngestStarted() {
	b.mu.Lock()
	if b.ingestStarted {
		b.mu.Unlock()
		return
	}
	b.ingestStarted = true
	b.ingestStartWall = b.clock.Now()
	b.mu.Unlock()
	b.lifecycle.MarkWarming()
}

// maintain runs until ctx is cancelled, driving the warming->running transition
// (once bufferHead >= delay) and evicting buffer history older than the rolling
// window (delay + margin behind the buffer head).
func (b *Broadcast) maintain(ctx context.Context) {
	t := time.NewTicker(evictTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		bh, started := b.bufferHead()
		if !started {
			continue
		}
		if bh >= b.delay.Seconds() {
			b.lifecycle.MarkRunning()
		}
		// Keep at least delay + margin of history behind the buffer head.
		evictBefore := bh - b.delay.Seconds() - evictMargin.Seconds()
		if evictBefore > 0 {
			b.buf.EvictBefore(evictBefore)
		}
	}
}

// bufferHead returns the current buffer-timeline head (seconds since ingest
// start) and whether ingest has started. Before the first bytes it returns
// (0, false).
func (b *Broadcast) bufferHead() (float64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.ingestStarted {
		return 0, false
	}
	return b.clock.Now().Sub(b.ingestStartWall).Seconds(), true
}

// broadcastHead returns bufferHead - delay (the steady-state timeline position
// released to subscribers) and whether ingest has started. Used by Status.
func (b *Broadcast) broadcastHead() (float64, bool) {
	bh, ok := b.bufferHead()
	if !ok {
		return 0, false
	}
	return bh - b.delay.Seconds(), true
}

// releaseHead is the timeline position the pacer releases up to this instant.
// While Running it is the delayed head (bufferHead - delay) so content trails
// the live edge by the full delay (giving ASR its lead time). While Warming it
// is the live edge (bufferHead) so a fresh subscriber that started near the
// live edge hears audio within seconds instead of waiting out the whole delay;
// the offset then ramps toward the delay as the buffer fills and the lifecycle
// flips to Running. The returned head is clamped so it never moves backward
// across the warming->running flip would be a concern only for already-served
// positions, which the pacer guards via its monotonic curPos.
func (b *Broadcast) releaseHead() (float64, bool) {
	bh, ok := b.bufferHead()
	if !ok {
		return 0, false
	}
	if b.lifecycle.State() == Running {
		return bh - b.delay.Seconds(), true
	}
	// Warming (or Starting): release up to the live edge.
	return bh, true
}

// Subscribe registers a new subscriber and returns its paced audio channel, its
// subtitle channel, and a cancel func that unsubscribes (decrementing the
// lifecycle ref-count). Channels are bounded; the pacer drops-oldest rather
// than block. cancel is idempotent.
func (b *Broadcast) Subscribe() (audio <-chan []byte, subs <-chan SubtitleEvent, cancel func()) {
	b.lifecycle.Acquire()

	ctx, cancelCtx := context.WithCancel(context.Background())
	audioCh := make(chan []byte, audioQueueCap)
	subsCh := make(chan SubtitleEvent, subsQueueCap)

	var done sync.WaitGroup
	done.Add(1)
	go func() {
		defer done.Done()
		b.pace(ctx, audioCh, subsCh)
	}()

	var once sync.Once
	cancel = func() {
		once.Do(func() {
			cancelCtx()
			done.Wait()
			close(audioCh)
			close(subsCh)
			b.lifecycle.Release()
		})
	}
	return audioCh, subsCh, cancel
}

// pace is one subscriber's pacer goroutine. It establishes a start position on
// the first iteration, then each tick releases MP3 frames and Start-anchored
// subtitles whose timeline ts <= broadcastHead, with non-blocking (drop-oldest)
// sends so a slow subscriber never stalls the shared producers.
func (b *Broadcast) pace(ctx context.Context, audioCh chan []byte, subsCh chan SubtitleEvent) {
	t := time.NewTicker(pacerTick)
	defer t.Stop()

	var (
		started bool
		curPos  float64
		// subStartAnchor is the timeline position this subscriber began at; we
		// never emit a subtitle whose Start is at or before it (none-early).
		subStartAnchor float64
		// lastSentSubSeq is the highest buffer sequence this subscriber has emitted.
		// Deduping by sequence (not by Start) means two subtitles sharing the same
		// Start are both delivered (Fix 5). -1 means nothing emitted yet.
		lastSentSubSeq int64 = -1
		haveSubAnchor  bool
	)

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		head, ingestStarted := b.releaseHead()
		if !ingestStarted {
			continue
		}

		if !started {
			curPos = b.startPosition(head)
			started = true
			// Anchor the subtitle release at the start position so we never emit a
			// subtitle whose Start is at or before where this subscriber began.
			subStartAnchor = curPos
			haveSubAnchor = true
		}

		// --- audio: release frames whose boundary is <= head ---
		for {
			// Cap the read so the returned blob never extends past head. The MP3
			// track is CBR (mp3BytesPerSec), so the bytes covering [curPos, head]
			// are bounded by (head-curPos)*rate; clamp that to chunkBytes per tick.
			span := head - curPos
			if span <= 0 {
				break
			}
			maxBytes := int(span * mp3BytesPerSec)
			if maxBytes <= 0 {
				break
			}
			if maxBytes > chunkBytes {
				maxBytes = chunkBytes
			}

			data, frameStart, next := b.buf.ReadMP3From(curPos, maxBytes)
			// Stop BEFORE sending when there is no new frame to deliver:
			//   - len(data)==0: nothing buffered, or curPos is strictly past the
			//     latest frame boundary so ReadMP3From reports no progress (the
			//     re-send fix: the last frame is NOT re-returned once consumed);
			//   - frameStart > head: the frame has not aged into the window yet.
			// Breaking here, before the send, is what prevents re-emitting the tail
			// frame every tick.
			if len(data) == 0 || frameStart > head {
				break
			}
			send(audioCh, data)
			// Advance strictly past the audio we just delivered. The MP3 track is
			// CBR, so the blob covers [frameStart, frameStart+len/rate) of timeline;
			// advancing curPos to that end (never behind the next un-returned
			// boundary) puts it strictly past every frame we have sent. Once it
			// reaches the tail, the next read's curPos is past the last boundary and
			// ReadMP3From returns no progress, so the final frame is delivered
			// exactly once and never re-included when a newer frame is appended.
			deliveredEnd := frameStart + float64(len(data))/float64(mp3BytesPerSec)
			if deliveredEnd > next {
				curPos = deliveredEnd
			} else {
				curPos = next
			}
		}

		// --- subtitles: Start-anchored, none early, deduped by sequence ---
		if haveSubAnchor {
			events, lastSeq := b.buf.ReadSubtitlesAfterSeq(lastSentSubSeq, head)
			if debugEnabled && len(events) > 0 {
				allSubs := b.buf.ReadSubtitlesUpTo(math.MaxFloat64)
				dbg("pacer state=%s head=%.2f anchor=%.2f lastSeq=%d due=%d bufSubsTotal=%d", b.lifecycle.State(), head, subStartAnchor, lastSentSubSeq, len(events), len(allSubs))
			}
			for _, ev := range events {
				// none-early: skip anything anchored at or before our start.
				if ev.Start <= subStartAnchor {
					dbg("pacer SKIP-early start=%.2f anchor=%.2f text=%q", ev.Start, subStartAnchor, ev.TextZh)
					continue
				}
				dbg("pacer SEND start=%.2f head=%.2f text=%q", ev.Start, head, ev.TextZh)
				sendSub(subsCh, ev)
			}
			// Advance the dedup cursor past everything we considered this tick
			// (including any skipped-as-early events) so they are never re-evaluated;
			// equal-Start events carry distinct sequences, so none is lost.
			lastSentSubSeq = lastSeq
		}
	}
}

// startPosition picks where a subscriber begins reading.
//
// While Running, the steady-state head trails the live edge by the full delay
// and there is at least delay+margin of buffered history, so we start at the
// frame boundary at or before head (clamped to the live edge): the subscriber
// joins delay seconds behind live, with subtitles already transcribed for that
// span.
//
// While Warming (the buffer has not yet filled to the delay), starting at the
// delayed head could be before anything buffered, or could dump a large
// prebuffer. Instead we start at the live edge so the user hears audio within
// seconds with only a bounded (sub-tick) prebuffer; the offset behind live then
// ramps toward the delay as the buffer fills and the lifecycle flips to Running.
func (b *Broadcast) startPosition(head float64) float64 {
	live := b.buf.FrameBoundaryAtOrBefore(math.MaxFloat64) // latest boundary
	if b.lifecycle.State() != Running {
		// Warming: bounded prebuffer near the live edge, never the whole buffer.
		return live
	}
	target := head
	if target > live {
		target = live
	}
	return b.buf.FrameBoundaryAtOrBefore(target)
}

// send does a non-blocking send on a bounded audio queue, dropping the oldest
// buffered chunk to make room if the queue is full. It never blocks the shared
// pacer on a slow subscriber.
func send(ch chan []byte, data []byte) {
	for {
		select {
		case ch <- data:
			return
		default:
			// Full: drop the oldest queued chunk and retry once.
			select {
			case <-ch:
			default:
				// Drained by the consumer between checks; retry the send.
			}
		}
	}
}

// sendSub is send's subtitle counterpart: non-blocking, drop-oldest.
func sendSub(ch chan SubtitleEvent, ev SubtitleEvent) {
	for {
		select {
		case ch <- ev:
			return
		default:
			select {
			case <-ch:
			default:
			}
		}
	}
}

// State reports the current ingest lifecycle state.
func (b *Broadcast) State() LifecycleState {
	return b.lifecycle.State()
}

// Status returns the JSON status payload for GET /v1/status.
//
// LiveEdgeOffsetSeconds is the offset ACTUALLY in effect: bufferHead minus the
// position the pacer currently releases up to (releaseHead). Once Running this
// equals the configured delay (releaseHead == bufferHead - delay). While Warming
// the pacer releases at the live edge (releaseHead == bufferHead), so the offset
// reports ~0 and ramps toward the delay as the buffer fills and the lifecycle
// flips to Running — instead of the old code's full bufferHead (Fix 4).
func (b *Broadcast) Status() Status {
	var offset float64
	if bh, ok := b.bufferHead(); ok {
		if rh, rok := b.releaseHead(); rok {
			offset = bh - rh
		}
	}
	return Status{
		Channel:               b.channel,
		Listeners:             b.lifecycle.Count(),
		DelaySeconds:          b.delay.Seconds(),
		State:                 b.State().String(),
		LiveEdgeOffsetSeconds: offset,
	}
}
