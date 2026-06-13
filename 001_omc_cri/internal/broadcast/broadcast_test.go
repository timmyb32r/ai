package broadcast

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/timmyb32r/001_omc_cri/internal/ingest"
)

// --- test doubles -----------------------------------------------------------

// blockingProcess is a fake ingest.Process whose streams emit nothing and
// unblock (EOF) only when ctx is cancelled. Used so the broadcast's ingest
// goroutine idles harmlessly while tests feed the buffer directly.
type blockingProcess struct{}

func (blockingProcess) Start(ctx context.Context, _ string, _, _ []string) (io.ReadCloser, io.ReadCloser, func() error, error) {
	return newCtxReader(ctx), newCtxReader(ctx), func() error { return nil }, nil
}

// ctxReader is an io.ReadCloser that blocks on Read until ctx is done, then
// returns EOF.
type ctxReader struct{ ctx context.Context }

func newCtxReader(ctx context.Context) *ctxReader { return &ctxReader{ctx: ctx} }

func (r *ctxReader) Read(_ []byte) (int, error) {
	<-r.ctx.Done()
	return 0, io.EOF
}
func (r *ctxReader) Close() error { return nil }

// newTestBroadcast builds a Broadcast with a fake clock, an idle ingestor (fake
// blocking Process), and a fake segmenter so no real subprocess runs. The
// caller drives the buffer via Append* and the clock via clk.Advance.
func newTestBroadcast(t *testing.T, clk *FakeClock, delay time.Duration) *Broadcast {
	t.Helper()
	buf := &Buffer{}
	ing := ingest.New("ffmpeg", "http://example/stream.m3u8", blockingProcess{})
	// transcriber is nil; we override the ASR segmenter with a no-op fake so the
	// ASR loop never touches a real binary even if PCM appears.
	b := NewBroadcast(clk, buf, NewLifecycle(nil, nil, 0), ing, nil, delay, "test-fm")
	b.asr.setTranscriber(&fakeTranscriber{})
	return b
}

// feedMP3 appends one MP3 entry per whole second on [from, to), each at timeline
// ts == its second and sized to one CBR second (mp3BytesPerSec bytes) so the
// pacer's CBR byte-rate cap aligns with the ts spacing. The first byte of each
// entry encodes its second for assertions; the rest is filler.
func feedMP3(b *Broadcast, from, to int) {
	for s := from; s < to; s++ {
		frame := make([]byte, mp3BytesPerSec)
		frame[0] = byte(s)
		b.buf.AppendMP3(float64(s), frame)
	}
}

// secondsOf decodes the per-second markers from a concatenated audio blob
// produced by feedMP3: the marker byte sits at the start of every mp3BytesPerSec
// window. Returns the max second seen, or -1 if empty.
func maxSecondOf(blob []byte) int {
	maxSec := -1
	for i := 0; i < len(blob); i += mp3BytesPerSec {
		if s := int(blob[i]); s > maxSec {
			maxSec = s
		}
	}
	return maxSec
}

// drainAudio collects all audio chunks currently queued (non-blocking) within a
// short settle window, concatenating them.
func collectAudio(ch <-chan []byte, settle time.Duration) []byte {
	var out []byte
	deadline := time.After(settle)
	for {
		select {
		case b, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, b...)
		case <-deadline:
			return out
		}
	}
}

// --- tests ------------------------------------------------------------------

// TestPacer_NothingReleasedEarly verifies that, in the steady (Running) state, a
// subscriber receives no MP3 (and no subtitle) whose timeline ts is beyond the
// current broadcastHead = bufferHead - delay, and that advancing the fake clock
// releases more. The subscriber joins after the broadcast has reached Running so
// this exercises the delayed-window gate (warming behaviour is covered
// separately in TestWarming_StartsNearLiveEdgeAndTransitions).
func TestPacer_NothingReleasedEarly(t *testing.T) {
	clk := NewFakeClock(time.Unix(1_000_000, 0))
	b := newTestBroadcast(t, clk, 5*time.Second)

	// A "keeper" subscriber acquires ingest (launches idle goroutines) and keeps
	// the lifecycle alive for the whole test; we never drain it (backpressure
	// isolation guarantees that does not affect the subscriber under test).
	_, _, keeperCancel := b.Subscribe()
	defer keeperCancel()
	b.markIngestStarted() // ingestStartWall = clk.Now()

	// Fill 30s of MP3 history + a subtitle far in the future. Advancing the
	// clock to t=10 drives the buffer head to 10 (>= delay) so the lifecycle
	// flips to Running before the subscriber under test joins.
	feedMP3(b, 0, 30)
	b.buf.AppendSubtitle(SubtitleEvent{Start: 20, End: 21, TextZh: "future"})
	clk.Advance(10 * time.Second) // bufferHead=10, broadcastHead=5 -> Running
	waitFor(t, 2*time.Second, func() bool { return b.State() == Running })

	// Now subscribe: broadcastHead = 10-5 = 5, so the subscriber starts at the
	// frame boundary <= 5. Let its pacer take a tick to fix the start position at
	// the current head (5) before we advance the clock further.
	audio, subs, cancel := b.Subscribe()
	defer cancel()
	time.Sleep(250 * time.Millisecond) // pacer establishes startPos at head=5

	// Advance the clock by 7s: bufferHead=17, broadcastHead=12. The subscriber
	// (started at 5) may now release frames [5..12], never beyond 12, and the
	// subtitle at Start=20 must NOT appear.
	clk.Advance(7 * time.Second)
	waitFor(t, 2*time.Second, func() bool { return len(audio) > 0 })
	got := collectAudio(audio, 400*time.Millisecond)
	if maxSec := maxSecondOf(got); maxSec > 12 {
		t.Errorf("released a frame at second %d beyond broadcastHead=12", maxSec)
	} else if maxSec < 0 {
		t.Errorf("expected at least one frame released up to the delayed head")
	}
	select {
	case ev := <-subs:
		t.Fatalf("subtitle %+v released though Start=20 > head=12", ev)
	default:
	}

	// Advance by 5s more: bufferHead=22, broadcastHead=17. More frames release
	// (up to 17) and the subtitle at Start=20 still must NOT appear.
	clk.Advance(5 * time.Second)
	got = collectAudio(audio, 500*time.Millisecond)
	if maxSec := maxSecondOf(got); maxSec > 17 {
		t.Errorf("after advance: released frame at second %d beyond head=17", maxSec)
	}
	select {
	case ev := <-subs:
		t.Fatalf("subtitle %+v released though Start=20 > head=17", ev)
	default:
	}
}

// TestPacer_FanoutIdenticalToTwoSubscribers verifies two subscribers that both
// drain promptly receive identical audio bytes and identical subtitle events.
// Both join after the broadcast is Running so they share an identical
// head-aligned start; the clock is advanced in small steps while both channels
// are drained continuously so no drop-oldest occurs and the comparison is exact.
func TestPacer_FanoutIdenticalToTwoSubscribers(t *testing.T) {
	clk := NewFakeClock(time.Unix(2_000_000, 0))
	b := newTestBroadcast(t, clk, 2*time.Second)

	// keeper drives the broadcast to Running before the two subscribers join.
	_, _, keeperCancel := b.Subscribe()
	defer keeperCancel()
	b.markIngestStarted()

	feedMP3(b, 0, 40)
	for i := 0; i < 5; i++ {
		b.buf.AppendSubtitle(SubtitleEvent{Start: float64(i), End: float64(i) + 0.5, TextZh: string(rune('A' + i))})
	}
	clk.Advance(5 * time.Second) // bufferHead=5 >= delay 2 -> Running
	waitFor(t, 2*time.Second, func() bool { return b.State() == Running })

	// Both subscribers join now, at the same delayed head (5-2=3), starting at
	// the frame boundary <= 3.
	a1, s1, c1 := b.Subscribe()
	defer c1()
	a2, s2, c2 := b.Subscribe()
	defer c2()
	// Let both pacers establish their identical start position (and subtitle
	// anchor) at the current head before the clock advances further.
	time.Sleep(250 * time.Millisecond)

	var (
		got1, got2 []byte
		subs1      []SubtitleEvent
		subs2      []SubtitleEvent
	)
	// Advance the clock in 1s steps up to bufferHead=30, draining both channels
	// after each step so neither overflows its bounded queue (no drops).
	for step := 0; step < 25; step++ {
		clk.Advance(1 * time.Second)
		time.Sleep(60 * time.Millisecond) // let both pacers tick
		got1 = append(got1, drainAudio(a1)...)
		got2 = append(got2, drainAudio(a2)...)
		subs1 = append(subs1, drainSubs(s1)...)
		subs2 = append(subs2, drainSubs(s2)...)
	}
	// One more settle pass to flush the tail.
	time.Sleep(200 * time.Millisecond)
	got1 = append(got1, drainAudio(a1)...)
	got2 = append(got2, drainAudio(a2)...)
	subs1 = append(subs1, drainSubs(s1)...)
	subs2 = append(subs2, drainSubs(s2)...)

	if !bytes.Equal(got1, got2) {
		t.Errorf("fan-out audio differs: len s1=%d s2=%d", len(got1), len(got2))
	}
	if len(got1) == 0 {
		t.Error("expected non-empty fan-out audio")
	}

	if len(subs1) != len(subs2) {
		t.Fatalf("subtitle count differs: %d vs %d", len(subs1), len(subs2))
	}
	for i := range subs1 {
		if subs1[i] != subs2[i] {
			t.Errorf("subtitle[%d] differs: %+v vs %+v", i, subs1[i], subs2[i])
		}
	}
	if len(subs1) == 0 {
		t.Error("expected subtitles to be fanned out")
	}
}

// TestPacer_DuplicateStartSubtitlesBothDelivered pins the Fix-5 behaviour: two
// subtitle events that share the same Start (but differ in End/Text) are BOTH
// released, instead of the second being dropped by a Start-only dedup.
func TestPacer_DuplicateStartSubtitlesBothDelivered(t *testing.T) {
	clk := NewFakeClock(time.Unix(9_000_000, 0))
	b := newTestBroadcast(t, clk, 2*time.Second)

	_, _, keeperCancel := b.Subscribe()
	defer keeperCancel()
	b.markIngestStarted()

	feedMP3(b, 0, 60)
	// Two events with the SAME Start, plus a later distinct one. All anchored
	// after the subscriber's start position.
	b.buf.AppendSubtitle(SubtitleEvent{Start: 10, End: 11, TextZh: "A"})
	b.buf.AppendSubtitle(SubtitleEvent{Start: 10, End: 12, TextZh: "B"})
	b.buf.AppendSubtitle(SubtitleEvent{Start: 11, End: 12, TextZh: "C"})

	clk.Advance(5 * time.Second) // bufferHead=5 >= delay -> Running
	waitFor(t, 2*time.Second, func() bool { return b.State() == Running })

	_, subs, cancel := b.Subscribe()
	defer cancel()
	time.Sleep(250 * time.Millisecond) // establish start at head ~3

	// Advance so head passes Start=10 and 11; all three subtitles are due.
	clk.Advance(20 * time.Second)

	got := map[string]int{}
	deadline := time.After(1 * time.Second)
collect:
	for {
		select {
		case ev, ok := <-subs:
			if !ok {
				break collect
			}
			got[ev.TextZh]++
		case <-deadline:
			break collect
		}
	}

	if got["A"] != 1 || got["B"] != 1 {
		t.Fatalf("equal-Start subtitles not both delivered exactly once: got A=%d B=%d (want 1,1)", got["A"], got["B"])
	}
	if got["C"] != 1 {
		t.Fatalf("later subtitle C delivered %d times, want 1", got["C"])
	}
}

// TestStatus_WarmingOffsetRampsToDelay pins the Fix-4 behaviour: during warming
// LiveEdgeOffsetSeconds reports the actual (~0) offset the pacer uses, ramping
// toward the configured delay, and reports exactly the delay once Running.
func TestStatus_WarmingOffsetRampsToDelay(t *testing.T) {
	clk := NewFakeClock(time.Unix(8_000_000, 0))
	const delay = 5 * time.Second
	b := newTestBroadcast(t, clk, delay)

	_, _, keeperCancel := b.Subscribe()
	defer keeperCancel()
	b.markIngestStarted() // ingestStartWall = now; state Warming

	feedMP3(b, 0, 60)

	// Just after ingest start (bufferHead ~0) the pacer releases at the live edge,
	// so the live-edge offset in effect is ~0 — NOT the full bufferHead.
	if st := b.Status(); st.State != "warming" {
		t.Fatalf("state = %s, want warming", st.State)
	} else if st.LiveEdgeOffsetSeconds > 0.5 {
		t.Fatalf("warming offset = %v, want ~0 (live-edge release)", st.LiveEdgeOffsetSeconds)
	}

	// Part-way through warming: bufferHead grows but releaseHead is still the live
	// edge, so the reported offset stays ~0 (it has not yet reached the delay).
	clk.Advance(3 * time.Second) // bufferHead=3 < delay 5 -> still warming
	if st := b.Status(); st.State == "warming" && st.LiveEdgeOffsetSeconds > 0.5 {
		t.Fatalf("mid-warming offset = %v, want ~0 (not the full bufferHead)", st.LiveEdgeOffsetSeconds)
	}

	// Past the delay: maintenance flips to Running and the offset settles to the
	// configured delay (the pacer now trails the live edge by the full delay).
	clk.Advance(5 * time.Second) // bufferHead=8 >= delay -> Running
	waitFor(t, 2*time.Second, func() bool { return b.State() == Running })
	if st := b.Status(); st.State != "running" {
		t.Fatalf("state = %s, want running", st.State)
	} else if got := st.LiveEdgeOffsetSeconds; got < delay.Seconds()-0.01 || got > delay.Seconds()+0.01 {
		t.Fatalf("running offset = %v, want %v (the configured delay)", got, delay.Seconds())
	}
}

// drainAudio pulls every audio chunk currently queued without blocking.
func drainAudio(ch <-chan []byte) []byte {
	var out []byte
	for {
		select {
		case b := <-ch:
			out = append(out, b...)
		default:
			return out
		}
	}
}

// drainSubs pulls every subtitle currently queued without blocking.
func drainSubs(ch <-chan SubtitleEvent) []SubtitleEvent {
	var out []SubtitleEvent
	for {
		select {
		case ev := <-ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}

// TestPacer_SlowSubscriberDoesNotStallOther verifies a subscriber that never
// drains its channel does not prevent a second subscriber from receiving audio
// (backpressure isolation via bounded drop-oldest queues).
func TestPacer_SlowSubscriberDoesNotStallOther(t *testing.T) {
	clk := NewFakeClock(time.Unix(3_000_000, 0))
	b := newTestBroadcast(t, clk, 2*time.Second)

	// slow subscriber: we never read aSlow.
	_, _, cSlow := b.Subscribe()
	defer cSlow()
	aFast, _, cFast := b.Subscribe()
	defer cFast()
	b.markIngestStarted()

	feedMP3(b, 0, 200) // far more than the bounded queue can hold
	clk.Advance(300 * time.Second)

	// The fast subscriber must keep receiving despite the slow one being stuck.
	waitFor(t, 2*time.Second, func() bool { return len(aFast) > 0 })
	got := collectAudio(aFast, 500*time.Millisecond)
	if len(got) == 0 {
		t.Fatal("fast subscriber received no audio")
	}
}

// TestPacer_HeadPastTailNoDuplicateFrame verifies the re-send fix: once the
// broadcast head advances past the buffer tail (the latest frame), the pacer
// delivers each frame exactly once and never re-emits the last frame on
// subsequent ticks. We feed a finite buffer, drive the head far beyond it, and
// assert every per-second marker byte appears at most once across all ticks.
func TestPacer_HeadPastTailNoDuplicateFrame(t *testing.T) {
	clk := NewFakeClock(time.Unix(5_500_000, 0))
	b := newTestBroadcast(t, clk, 2*time.Second)

	audio, _, cancel := b.Subscribe()
	defer cancel()
	b.markIngestStarted()

	// A small finite buffer: frames 0..9. No frames are ever appended past 9.
	feedMP3(b, 0, 10)
	// Drive the head far past the tail (bufferHead=60, broadcastHead=58 >> 9) and
	// keep advancing across many ticks so a re-sending pacer would re-emit frame 9
	// every tick.
	clk.Advance(60 * time.Second)
	waitFor(t, 2*time.Second, func() bool { return b.State() == Running })

	// Collect over a window spanning many 100ms pacer ticks.
	counts := make(map[int]int)
	deadline := time.After(1500 * time.Millisecond)
collect:
	for {
		select {
		case blob, ok := <-audio:
			if !ok {
				break collect
			}
			for i := 0; i < len(blob); i += mp3BytesPerSec {
				counts[int(blob[i])]++
			}
		case <-deadline:
			break collect
		}
	}

	if len(counts) == 0 {
		t.Fatal("subscriber received no audio at all")
	}
	for sec, n := range counts {
		if n > 1 {
			t.Errorf("frame for second %d delivered %d times (want exactly 1, no re-send)", sec, n)
		}
	}
}

// payloadProcess is a fake ingest.Process that, on each Start (each ingest
// epoch), emits a fixed PCM and MP3 payload exactly once and then blocks until
// ctx is cancelled. Because internal/ingest derives tsSec from a per-Run byte
// clock that starts at 0, every epoch therefore produces a clean zero-based
// timeline — the exact condition the restart fix must survive.
type payloadProcess struct {
	pcm []byte
	mp3 []byte
}

func (p payloadProcess) Start(ctx context.Context, _ string, _, _ []string) (io.ReadCloser, io.ReadCloser, func() error, error) {
	return newOnceReader(ctx, p.pcm), newOnceReader(ctx, p.mp3), func() error { return nil }, nil
}

// onceReader yields its payload across one or more Reads, then blocks until ctx
// is done and returns EOF. It never EOFs early, so the ingestor does not
// reconnect (re-feed) within a single epoch.
type onceReader struct {
	ctx  context.Context
	data []byte
	off  int
}

func newOnceReader(ctx context.Context, data []byte) *onceReader {
	return &onceReader{ctx: ctx, data: data}
}

func (r *onceReader) Read(p []byte) (int, error) {
	if r.off < len(r.data) {
		n := copy(p, r.data[r.off:])
		r.off += n
		return n, nil
	}
	<-r.ctx.Done()
	return 0, io.EOF
}
func (r *onceReader) Close() error { return nil }

// TestLifecycleRestart_CleanTimelineAndSubtitles drives the REAL start/stop
// hooks through a full restart (Acquire -> Release -> wait past linger ->
// Acquire) and asserts the second epoch begins on a clean, zero-based timeline
// (the stale epoch-1 buffer is gone) AND that ASR emits subtitles in epoch 2
// (the ASR cursor was reset, so it does not skip the whole zero-based epoch).
// This is the regression test for the restart-corruption fix.
func TestLifecycleRestart_CleanTimelineAndSubtitles(t *testing.T) {
	clk := NewFakeClock(time.Unix(6_000_000, 0))
	buf := &Buffer{}
	// 10s of PCM and 10s of MP3 per epoch, zero-based via the ingest byte clock.
	proc := payloadProcess{
		pcm: make([]byte, 10*pcmBytesPerSec),
		mp3: make([]byte, 10*mp3BytesPerSec),
	}
	ing := ingest.New("ffmpeg", "http://example/stream.m3u8", proc)
	// Short linger so the test does not wait long for stop() to fire.
	b := NewBroadcast(clk, buf, NewLifecycle(nil, nil, 30*time.Millisecond), ing, nil, 2*time.Second, "test-fm")
	// A non-empty transcript so every ASR window that runs produces a subtitle.
	fake := &fakeTranscriber{text: "你好"}
	b.asr.setTranscriber(fake)

	// --- Epoch 1 --------------------------------------------------------------
	_, _, cancel1 := b.Subscribe() // Acquire -> start()
	// The ingest goroutine feeds the buffer; advance the clock so bufferHead grows
	// and ASR has settled audio. Wait for epoch-1 subtitles to confirm ASR ran.
	clk.Advance(10 * time.Second)
	waitFor(t, 5*time.Second, func() bool {
		return len(b.buf.ReadSubtitlesUpTo(1e9)) > 0
	})
	epoch1Calls := func() int { fake.mu.Lock(); defer fake.mu.Unlock(); return fake.calls }()
	if epoch1Calls == 0 {
		t.Fatal("epoch 1: ASR never transcribed")
	}

	// --- Teardown across the linger ------------------------------------------
	cancel1() // Release -> Stopping -> linger -> stop() waits for goroutines
	waitFor(t, 5*time.Second, func() bool { return b.State() == Stopped })

	// --- Epoch 2 --------------------------------------------------------------
	clk.Set(time.Unix(7_000_000, 0)) // fresh wall anchor for the new epoch
	_, _, cancel2 := b.Subscribe()   // Acquire -> start() (resets buf + asr)
	defer cancel2()

	// The buffer must have been reset: epoch-2's earliest MP3 boundary is the new
	// zero-based timeline, not epoch-1's stale high tsSec.
	waitFor(t, 5*time.Second, func() bool {
		_, start, _ := b.buf.ReadMP3From(0, 1)
		return start >= 0 && start < 1.0 && b.State() != Stopped
	})
	if _, start, _ := b.buf.ReadMP3From(0, 1); start >= 1.0 {
		t.Fatalf("epoch 2: earliest MP3 boundary = %v, want a clean zero-based timeline (< 1.0)", start)
	}

	// Advance the new epoch's clock and assert ASR emits subtitles again — only
	// possible if the cursor was reset to 0 (otherwise it sits past epoch-2's
	// entire zero-based PCM and produces nothing).
	callsBefore := func() int { fake.mu.Lock(); defer fake.mu.Unlock(); return fake.calls }()
	clk.Advance(10 * time.Second)
	waitFor(t, 6*time.Second, func() bool {
		fake.mu.Lock()
		more := fake.calls > callsBefore
		fake.mu.Unlock()
		return more && len(b.buf.ReadSubtitlesUpTo(1e9)) > 0
	})
	if subs := b.buf.ReadSubtitlesUpTo(1e9); len(subs) == 0 {
		t.Fatal("epoch 2: ASR emitted no subtitles (cursor not reset?)")
	} else if subs[0].Start >= 5.0 {
		t.Fatalf("epoch 2: subtitle anchored at %v, want zero-based (~0.5)", subs[0].Start)
	}
}

// TestWarming_StartsNearLiveEdgeAndTransitions verifies that with a just-started
// buffer a subscriber gets audio from near the live edge (bounded prebuffer,
// not the whole buffer) and that Status().State transitions warming -> running.
func TestWarming_StartsNearLiveEdgeAndTransitions(t *testing.T) {
	clk := NewFakeClock(time.Unix(4_000_000, 0))
	b := newTestBroadcast(t, clk, 5*time.Second)

	audio, _, cancel := b.Subscribe()
	defer cancel()
	b.markIngestStarted()

	// Warming state right after ingest starts.
	if got := b.State(); got != Warming {
		t.Fatalf("state after ingest start = %s, want warming", got)
	}

	// Simulate a buffer that already holds a lot of history at the live edge,
	// but bufferHead is only just past 0 (we are warming): broadcastHead < 0.
	feedMP3(b, 0, 100)

	// The subscriber should start near the live edge, so it must NOT dump the
	// whole 100-frame buffer. Give the pacer a few ticks.
	got := collectAudio(audio, 400*time.Millisecond)
	// While warming (broadcastHead negative) the prebuffer is bounded: far fewer
	// than the full 100 frames (300 bytes) should appear.
	if len(got) > 30 { // > ~10 frames would mean we dumped the buffer
		t.Errorf("warming dumped %d bytes (too much); expected a bounded prebuffer near live edge", len(got))
	}

	// Advance the clock past the delay so the maintenance loop marks Running.
	clk.Advance(10 * time.Second)
	waitFor(t, 3*time.Second, func() bool { return b.State() == Running })
	if st := b.Status(); st.State != "running" || st.Listeners != 1 {
		t.Errorf("status = %+v, want state=running listeners=1", st)
	}
}
