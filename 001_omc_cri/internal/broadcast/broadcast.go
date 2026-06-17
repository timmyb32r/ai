// Package broadcast ties the CRI radio ingest pipeline together: a single
// ffmpeg-based ingestor feeds a rolling Buffer (PCM + subtitles), an offline
// ASR driver transcribes the buffered PCM in fixed windows, and a
// per-subscriber pacer releases real-time-paced PCM audio and Start-anchored
// subtitles behind the live edge by a configurable delay.
//
// The lifecycle is ref-counted: ingest starts on the first subscriber and
// stops after the last one leaves (with a linger timer).  A permanent
// always-on reference keeps ingest running independently of clients.
//
// Timeline model: bufferHead = wall-time since first ingested byte.
// broadcastHead = bufferHead - delay.  Content is released up to
// broadcastHead; during warming the offset ramps from 0 toward the delay.
package broadcast

import (
	"bytes"
	"context"
	"io"
	"log"
	"math"
	"os/exec"
	"sync"
	"time"

	"github.com/timmyb32r/001_omc_cri/internal/chineseasr"
	"github.com/timmyb32r/001_omc_cri/internal/ingest"
)

// Pacer / fan-out tuning. The audio queue is sized for several seconds of PCM
// in ~chunkBytes pieces; the subtitle queue is smaller since events are sparse.
// Both are bounded so a stalled subscriber never blocks the shared producers
// (the pacer drops-oldest instead).
const (
	audioQueueCap = 32
	subsQueueCap  = 16
	syncQueueCap  = 1

	// chunkBytes is the max audio bytes a pacer pulls per tick. At 32000 B/s
	// (PCM s16le 16kHz mono) this is ~250ms of audio per chunk, comfortably
	// more than one tick produces so we never starve.
	chunkBytes = 8192

	// pacerTick is the wall cadence at which each subscriber pacer re-reads the
	// clock and releases more content. Release is gated by the (injected) clock,
	// so this is only the polling granularity; tests advance a FakeClock and the
	// pacer picks it up on the next tick.
	pacerTick = 100 * time.Millisecond

	// evictMargin is extra retained history beyond the delay so a just-joined
	// subscriber starting at the broadcast head still finds its boundary.
	evictMargin = 30 * time.Second

	// evictTick is how often the maintenance loop trims the buffer and drives
	// the warming->running state transition.
	evictTick = 1 * time.Second
)

// Broadcast ties the timeline together: it runs one shared ingest into the
// Buffer, drives the ASR driver over the buffered PCM, maintains
// broadcastHead = bufferHead - delay, and fans real-time-paced PCM plus
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
	epochWG  *sync.WaitGroup
	alwaysOn bool
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
	segmenter WordSegmenter,
	delay time.Duration,
	channel string,
) *Broadcast {
	b := &Broadcast{
		clock:    clock,
		buf:      buf,
		ingestor: ingestor,
		asr:      NewASR(transcriber, buf, segmenter),
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

	// (a) Ingest: PCM into the buffer; record ingestStartWall on first bytes.
	go func() {
		defer wg.Done()
		onPCM := func(tsSec float64, p []byte) {
			b.markIngestStarted()
			b.buf.AppendPCM(tsSec, p)
		}
		if err := b.ingestor.Run(ctx, onPCM); err != nil && ctx.Err() == nil {
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
// exit before returning (Fix 2). Waiting guarantees epoch-1's onPCM/ASR
// closure can no longer append into the buffer once stop returns, so the next
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
	// Warming: use a fixed pre-buffer delay so ASR has time to produce
	// subtitles before audio is released.  Audio flows continuously.
	const prebufWarmingSeconds = 20.0
	head := bh - prebufWarmingSeconds
	if head < 0 {
		head = 0
	}
	return head, true
}

// subscriber bundles per-subscriber state for the pacer goroutine. For Opus
// subscribers the ffmpegCmd is non-nil and PCM is transcoded to OGG/Opus on the
// fly; for PCM subscribers ffmpegCmd is nil and raw PCM is fanned out directly.
type subscriber struct {
	audioCh   chan []byte
	subsCh    chan SubtitleEvent
	ctrlCh    chan any
	ffmpegCmd *exec.Cmd
	cancelFn  context.CancelFunc
}

// Subscribe registers a new PCM subscriber and returns its paced audio,
// subtitle, and control channels plus an idempotent cancel func. The audio
// channel carries raw s16le 16 kHz mono PCM bytes.
func (b *Broadcast) Subscribe() (audio <-chan []byte, subs <-chan SubtitleEvent, ctrlCh <-chan any, cancel func()) {
	return b.subscribe(false)
}

// SubscribeOpus registers a subscriber whose audio channel carries OGG/Opus
// instead of raw PCM. PCM is transcoded per-subscriber through ffmpeg.
func (b *Broadcast) SubscribeOpus() (audio <-chan []byte, subs <-chan SubtitleEvent, ctrlCh <-chan any, cancel func()) {
	return b.subscribe(true)
}

// subscribe is the shared implementation. When opus is true, PCM bytes are
// piped through ffmpeg (libopus, 32 kbps, OGG container) before being sent to
// the subscriber's audio channel.
func (b *Broadcast) subscribe(opus bool) (audio <-chan []byte, subs <-chan SubtitleEvent, ctrlCh <-chan any, cancel func()) {
	b.lifecycle.Acquire()

	ctx, cancelCtx := context.WithCancel(context.Background())
	audioCh := make(chan []byte, audioQueueCap)
	subsCh := make(chan SubtitleEvent, subsQueueCap)
	ctrlChan := make(chan any, syncQueueCap)
	ctrlCh = ctrlChan

	sub := &subscriber{
		audioCh:  audioCh,
		subsCh:   subsCh,
		ctrlCh:   ctrlChan,
		cancelFn: cancelCtx,
	}

	var done sync.WaitGroup
	done.Add(1)
	go func() {
		defer done.Done()
		if opus {
			b.paceOpus(ctx, sub)
		} else {
			b.pace(ctx, sub.audioCh, sub.subsCh, sub.ctrlCh)
		}
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
	return audioCh, subsCh, ctrlCh, cancel
}

// pace is one subscriber's pacer goroutine. It establishes a start position on
// the first iteration, then each tick releases PCM audio frames and
// Start-anchored subtitles whose timeline ts <= broadcastHead, with
// non-blocking (drop-oldest) sends so a slow subscriber never stalls the
// shared producers.
func (b *Broadcast) pace(ctx context.Context, audioCh chan []byte, subsCh chan SubtitleEvent, ctrlCh chan<- any) {
	t := time.NewTicker(pacerTick)
	defer t.Stop()
	defer close(ctrlCh)

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

	bytesPerSec := pcmBytesPerSec
	readFn := b.buf.ReadPCMFrom

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
			// Send the sync event so the client knows the absolute timeline
			// position at which audio begins for this subscriber.
			ctrlCh <- SyncEvent{AudioTimelineStart: curPos}
		}

		// --- audio: release frames whose boundary is <= head ---
		for {
			// Cap the read so the returned blob never extends past head. The audio
			// track is constant-rate (bytesPerSec), so the bytes covering [curPos,
			// head] are bounded by (head-curPos)*rate; clamp that to chunkBytes per
			// tick.
			span := head - curPos
			if span <= 0 {
				break
			}
			maxBytes := int(span * float64(bytesPerSec))
			if maxBytes <= 0 {
				break
			}
			if maxBytes > chunkBytes {
				maxBytes = chunkBytes
			}

			data, frameStart, next := readFn(curPos, maxBytes)
			// Stop BEFORE sending when there is no new frame to deliver:
			//   - len(data)==0: nothing buffered, or curPos is strictly past the
			//     latest frame boundary so readFn reports no progress (the re-send
			//     fix: the last frame is NOT re-returned once consumed);
			//   - frameStart > head: the frame has not aged into the window yet.
			// Breaking here, before the send, is what prevents re-emitting the tail
			// frame every tick.
			if len(data) == 0 || frameStart > head {
				break
			}
			send(audioCh, data)
			// Advance strictly past the audio we just delivered. The audio track is
			// constant-rate, so the blob covers [frameStart, frameStart+len/rate) of
			// timeline; advancing curPos to that end (never behind the next
			// un-returned boundary) puts it strictly past every frame we have sent.
			// Once it reaches the tail, the next read's curPos is past the last
			// boundary and readFn returns no progress, so the final frame is
			// delivered exactly once and never re-included when a newer frame is
			// appended.
			deliveredEnd := frameStart + float64(len(data))/float64(bytesPerSec)
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

// paceOpus is the pacer for Opus subscribers. It runs a ffmpeg subprocess
// that transcodes PCM s16le → Opus/OGG (32 kbps) on the fly. PCM bytes are
// read from the shared Buffer using the same pacing logic as pace(), written to
// ffmpeg's stdin, and OGG bytes are read from ffmpeg's stdout and sent to the
// subscriber's audio channel.
//
// ffmpeg command (no shell — exec directly):
//
//	ffmpeg -f s16le -ar 16000 -ac 1 -i pipe:0 \
//	  -c:a libopus -b:a 32k -fflags flush_packets \
//	  -max_delay 200000 -f ogg pipe:1
//
// The subscriber struct carries the ffmpeg Cmd so the cancel func can clean it
// up.
func (b *Broadcast) paceOpus(ctx context.Context, sub *subscriber) {
	cmd := exec.CommandContext(ctx,
		"ffmpeg",
		"-hide_banner", "-nostdin",
		"-f", "s16le", "-ar", "16000", "-ac", "1", "-i", "pipe:0",
		"-c:a", "libopus", "-b:a", "32k",
		"-fflags", "flush_packets",
		"-max_delay", "200000",
		"-f", "ogg", "pipe:1",
	)
	sub.ffmpegCmd = cmd

	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Printf("broadcast: paceOpus: stdin pipe: %v", err)
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("broadcast: paceOpus: stdout pipe: %v", err)
		return
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		log.Printf("broadcast: paceOpus: ffmpeg start: %v", err)
		return
	}

	// Read OGG output in a separate goroutine and feed the subscriber's audio
	// channel. This runs independently of the PCM pacing loop below.
	oggDone := make(chan struct{})
	go func() {
		defer close(oggDone)
		buf := make([]byte, 8192)
		for {
			n, readErr := stdout.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				select { case sub.audioCh <- chunk: case <-ctx.Done(): return }
			}
			if readErr != nil {
				if readErr != io.EOF {
					log.Printf("broadcast: paceOpus: ffmpeg stdout read: %v", readErr)
				}
				return
			}
		}
	}()

	// PCM pacing loop — same logic as pace(), but writes to ffmpeg stdin instead
	// of sending directly to the audio channel.
	t := time.NewTicker(pacerTick)
	defer t.Stop()

	var (
		started        bool
		curPos         float64
		subStartAnchor float64
		lastSentSubSeq int64 = -1
		haveSubAnchor  bool
	)

	bytesPerSec := pcmBytesPerSec
	readFn := b.buf.ReadPCMFrom

	for {
		select {
		case <-ctx.Done():
			// Close stdin so ffmpeg flushes and exits.
			stdin.Close()
			// Wait for the OGG reader to drain.
			<-oggDone
			// Wait for ffmpeg to exit (or kill after timeout).
			waitCh := make(chan struct{})
			go func() { cmd.Wait(); close(waitCh) }()
			select {
			case <-waitCh:
			case <-time.After(5 * time.Second):
				log.Printf("broadcast: paceOpus: ffmpeg did not exit, killing")
				cmd.Process.Kill()
			}
			if stderrBuf.Len() > 0 {
				log.Printf("broadcast: paceOpus: ffmpeg stderr (%d bytes): %s",
					stderrBuf.Len(), stderrBuf.String())
			}
			return
		case <-t.C:
		}

		head, ingestStarted := b.releaseHead()
		if !ingestStarted {
			continue
		}

		if !started {
			curPos = b.startPosition(head)
			haveSubAnchor = true
			}
		// PCM pacing: read frames ≤ head and write into ffmpeg stdin.
		for {
			span := head - curPos
			if span <= 0 {
				break
			}
			maxBytes := int(span * float64(bytesPerSec))
			if maxBytes <= 0 {
				break
			}
			if maxBytes > chunkBytes {
				maxBytes = chunkBytes
			}

			data, frameStart, next := readFn(curPos, maxBytes)
			if len(data) == 0 || frameStart > head {
				break
			}
			// Write PCM into ffmpeg stdin. If ffmpeg has exited, break.
			if _, err := stdin.Write(data); err != nil {
				log.Printf("broadcast: paceOpus: ffmpeg stdin write: %v", err)
				return
			}
			if !started {
				started = true
				sub.ctrlCh <- SyncEvent{AudioTimelineStart: curPos}
			}
			deliveredEnd := frameStart + float64(len(data))/float64(bytesPerSec)
			if deliveredEnd > next {
				curPos = deliveredEnd
			} else {
				curPos = next
			}
		}

		// Subtitles: same logic as pace().
		if haveSubAnchor {
			events, lastSeq := b.buf.ReadSubtitlesAfterSeq(lastSentSubSeq, head)
			for _, ev := range events {
				if ev.Start <= subStartAnchor {
					continue
				}
				sendSub(sub.subsCh, ev)
			}
			lastSentSubSeq = lastSeq
		}
	}
}

// startPosition picks where a subscriber begins reading.
//
// While Running, the steady-state head trails the live edge by the full delay
// and there is at least delay+margin of buffered history, so we start at the
// PCM entry boundary at or before head (clamped to the live edge): the
// subscriber joins delay seconds behind live, with subtitles already
// transcribed for that span.
//
// While Warming (the buffer has not yet filled to the delay), starting at the
// delayed head could be before anything buffered, or could dump a large
// prebuffer. Instead we start at the live edge so the user hears audio within
// seconds with only a bounded (sub-tick) prebuffer; the offset behind live then
// ramps toward the delay as the buffer fills and the lifecycle flips to Running.
func (b *Broadcast) startPosition(head float64) float64 {
	boundary := b.buf.PCMFrameBoundaryAtOrBefore
	live := boundary(math.MaxFloat64) // latest boundary
	if b.lifecycle.State() != Running {
		// Warming: bounded prebuffer near the live edge, never the whole buffer.
		return boundary(head)
	}
	target := head
	if target > live {
		target = live
	}
	return boundary(target)
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
// SetEnricher installs an Enricher that adds pinyin and English to every
// SubtitleEvent produced by the ASR driver. May be nil.
func (b *Broadcast) SetEnricher(e *Enricher) { b.asr.SetEnricher(e) }

// StartAlwaysOn acquires a permanent lifecycle reference so the ingest pipeline
// runs independently of client connections. Idempotent.
func (b *Broadcast) StartAlwaysOn() {
	b.mu.Lock()
	if b.alwaysOn {
		b.mu.Unlock()
		return
	}
	b.alwaysOn = true
	b.mu.Unlock()
	// Release b.mu before Acquire — the start hook also takes b.mu.
	b.lifecycle.Acquire()
}

// ForceStop tears down the ingest pipeline immediately, bypassing the linger timer.
func (b *Broadcast) ForceStop() {
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
	b.mu.Lock()
	if b.epochWG == wg {
		b.epochWG = nil
	}
	b.alwaysOn = false
	b.mu.Unlock()
	b.lifecycle.ForceStopped()
}

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
