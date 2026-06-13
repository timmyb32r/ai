package broadcast

import "sync"

// Buffer is the thread-safe rolling timeline buffer shared by the ingest
// producer, the ASR driver, and the per-subscriber pacers. It stores three
// timeline-keyed tracks:
//   - PCM (s16le 16 kHz mono) bytes, appended by timestamp, consumed by ASR;
//   - CBR MP3 bytes with frame boundaries (each AppendMP3 chunk starts on a
//     frame boundary at its timeline position) so late joiners can start on a
//     frame boundary;
//   - SubtitleEvents, released Start-anchored by the pacer.
//
// Old data past the configured window is evicted via EvictBefore.
//
// All tracks are kept ordered ascending by their timeline anchor (tsSec for
// PCM/MP3, Start for subtitles). The ingestor delivers monotonically
// increasing, frame-aligned chunks, so appends are normally already in order;
// the implementation nonetheless inserts in sorted position to stay correct if
// a slightly out-of-order append arrives.
//
// Every method takes b.mu so the buffer is safe for concurrent producers and
// consumers (verified under -race in buffer_test.go).
type Buffer struct {
	mu sync.Mutex

	// pcm and mp3 are timeline-ordered entries. Each mp3 entry begins on a
	// frame boundary at its tsSec (the ingestor delivers frame-aligned chunks).
	pcm []pcmEntry
	mp3 []mp3Entry

	// subs are subtitle events kept ordered by Start. Each carries a monotonic
	// append sequence so the pacer can dedup by sequence rather than by Start;
	// two events sharing the same Start therefore both get released (Fix 5).
	subs []subEntry
	// subSeq is the next sequence to assign. It only ever increases and is never
	// reused, so a consumer that remembers the highest sequence it has emitted can
	// resume after eviction without re-sending or skipping anything.
	subSeq int64
}

// pcmEntry is one PCM chunk whose first sample sits at timeline tsSec.
type pcmEntry struct {
	tsSec float64
	bytes []byte
}

// mp3Entry is one or more contiguous CBR MP3 frames that begin on a frame
// boundary at timeline tsSec.
type mp3Entry struct {
	tsSec float64
	bytes []byte
}

// subEntry is one subtitle event tagged with its monotonic append sequence.
type subEntry struct {
	ev  SubtitleEvent
	seq int64
}

// Reset clears all buffered PCM, MP3, and subtitle data under the mutex,
// returning the buffer to its empty zero state.
//
// ADDITIVE (not in the frozen set): used by the broadcast at the start of each
// ingest epoch so a re-acquired broadcast begins from a clean, zero-based
// timeline. Without it, epoch-1 frames anchored at high tsSec would linger in
// the buffer while the reused ingestor restarts its byte clock at 0, corrupting
// the timeline. The slices are truncated (cap retained) so steady-state appends
// do not re-allocate.
func (b *Buffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pcm = b.pcm[:0]
	b.mp3 = b.mp3[:0]
	b.subs = b.subs[:0]
	b.subSeq = 0
}

// AppendPCM appends PCM bytes p whose first sample sits at timeline tsSec.
func (b *Buffer) AppendPCM(tsSec float64, p []byte) {
	if len(p) == 0 {
		return
	}
	cp := make([]byte, len(p))
	copy(cp, p)
	b.mu.Lock()
	defer b.mu.Unlock()
	e := pcmEntry{tsSec: tsSec, bytes: cp}
	// Fast path: append at the end (the common, in-order case).
	if len(b.pcm) == 0 || tsSec >= b.pcm[len(b.pcm)-1].tsSec {
		b.pcm = append(b.pcm, e)
		return
	}
	i := pcmInsertIndex(b.pcm, tsSec)
	b.pcm = append(b.pcm, pcmEntry{})
	copy(b.pcm[i+1:], b.pcm[i:])
	b.pcm[i] = e
}

// AppendMP3 appends one or more CBR MP3 frames starting on a frame boundary at
// timeline tsSec. Frames within a single call are contiguous; consecutive calls
// extend the timeline.
func (b *Buffer) AppendMP3(tsSec float64, frame []byte) {
	if len(frame) == 0 {
		return
	}
	cp := make([]byte, len(frame))
	copy(cp, frame)
	b.mu.Lock()
	defer b.mu.Unlock()
	e := mp3Entry{tsSec: tsSec, bytes: cp}
	if len(b.mp3) == 0 || tsSec >= b.mp3[len(b.mp3)-1].tsSec {
		b.mp3 = append(b.mp3, e)
		return
	}
	i := mp3InsertIndex(b.mp3, tsSec)
	b.mp3 = append(b.mp3, mp3Entry{})
	copy(b.mp3[i+1:], b.mp3[i:])
	b.mp3[i] = e
}

// AppendSubtitle stores a transcribed subtitle event, keeping the track ordered
// by Start for later Start-anchored release by the pacer. Each event is tagged
// with the next monotonic sequence so equal-Start events stay distinguishable.
func (b *Buffer) AppendSubtitle(ev SubtitleEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := subEntry{ev: ev, seq: b.subSeq}
	b.subSeq++
	if len(b.subs) == 0 || ev.Start >= b.subs[len(b.subs)-1].ev.Start {
		b.subs = append(b.subs, e)
		return
	}
	i := subInsertIndex(b.subs, ev.Start)
	b.subs = append(b.subs, subEntry{})
	copy(b.subs[i+1:], b.subs[i:])
	b.subs[i] = e
}

// ReadMP3From returns MP3 bytes beginning at the frame boundary at or before
// fromTsSec (or the earliest available frame if fromTsSec predates the buffer),
// up to maxBytes of contiguous bytes, along with frameStartTsSec (the boundary
// actually used) and nextTsSec (the timeline position to request next).
//
// When the buffer is empty it returns (nil, 0, fromTsSec) so the caller can
// retry from the same position once data arrives. nextTsSec is the tsSec of the
// first frame not returned; if everything available was returned it is the
// boundary just past the last returned frame (== the last entry's tsSec when
// only one entry exists, advanced to the would-be next boundary otherwise).
//
// No-progress guarantee (re-send fix): when fromTsSec is STRICTLY past the
// latest frame boundary there is no newer frame to deliver, so it returns
// (nil, lastBoundary, fromTsSec) — i.e. nextTsSec == fromTsSec (no forward
// progress) and no bytes — rather than re-returning the last frame. Once the
// pacer's curPos has advanced past the tail boundary, this stops it re-emitting
// the final frame every tick. A read whose fromTsSec lands exactly ON a
// boundary still returns that frame (first delivery of the tail frame).
func (b *Buffer) ReadMP3From(fromTsSec float64, maxBytes int) (data []byte, frameStartTsSec float64, nextTsSec float64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.mp3) == 0 {
		return nil, 0, fromTsSec
	}

	// No newer frame to deliver: fromTsSec sits strictly past the latest frame
	// boundary. Report no forward progress (nextTsSec == fromTsSec, nil data) so
	// the caller does not re-emit the last frame until a newer one is appended.
	lastBoundary := b.mp3[len(b.mp3)-1].tsSec
	if fromTsSec > lastBoundary {
		return nil, lastBoundary, fromTsSec
	}

	// Find the index of the frame boundary at or before fromTsSec. If
	// fromTsSec is older than the buffer, start at the earliest entry.
	start := mp3BoundaryIndexAtOrBefore(b.mp3, fromTsSec)
	frameStartTsSec = b.mp3[start].tsSec

	if maxBytes <= 0 {
		// Nothing requested; report where to resume.
		return nil, frameStartTsSec, frameStartTsSec
	}

	var out []byte
	i := start
	for ; i < len(b.mp3); i++ {
		entry := b.mp3[i]
		// Never split an entry: each AppendMP3 chunk is an atomic frame-aligned
		// unit. Include it only if it fits, but always include at least the
		// first (start) entry so the reader makes progress.
		if len(out) > 0 && len(out)+len(entry.bytes) > maxBytes {
			break
		}
		out = append(out, entry.bytes...)
		if len(out) >= maxBytes {
			i++
			break
		}
	}

	if i < len(b.mp3) {
		// More data remains: resume at the next un-returned boundary.
		nextTsSec = b.mp3[i].tsSec
	} else {
		// Returned everything available. Resume just past the last frame; we do
		// not know the next frame's tsSec yet, so report the last boundary so a
		// subsequent ReadMP3From with the same value re-finds it and reads only
		// newly appended frames after it.
		nextTsSec = b.mp3[len(b.mp3)-1].tsSec
	}
	return out, frameStartTsSec, nextTsSec
}

// FrameBoundaryAtOrBefore returns the timeline position of the latest MP3 frame
// boundary at or before posSec, or the earliest available boundary if none is
// at or before posSec. Returns 0 when no MP3 data is buffered.
func (b *Buffer) FrameBoundaryAtOrBefore(posSec float64) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.mp3) == 0 {
		return 0
	}
	return b.mp3[mp3BoundaryIndexAtOrBefore(b.mp3, posSec)].tsSec
}

// EvictBefore drops all PCM, MP3, and subtitle data anchored strictly before
// tsSec to bound memory to the configured window. A subtitle is evicted only
// once its End is before tsSec so in-flight (not-yet-released) subtitles survive.
func (b *Buffer) EvictBefore(tsSec float64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// PCM: drop entries whose tsSec < tsSec.
	pi := 0
	for pi < len(b.pcm) && b.pcm[pi].tsSec < tsSec {
		pi++
	}
	if pi > 0 {
		b.pcm = append(b.pcm[:0], b.pcm[pi:]...)
	}

	// MP3: drop entries whose tsSec < tsSec.
	mi := 0
	for mi < len(b.mp3) && b.mp3[mi].tsSec < tsSec {
		mi++
	}
	if mi > 0 {
		b.mp3 = append(b.mp3[:0], b.mp3[mi:]...)
	}

	// Subtitles: drop only fully-past events (End < tsSec). Sequences on the
	// surviving entries are preserved (never renumbered), so a pacer that tracks
	// the highest sequence it emitted stays correct across eviction.
	if len(b.subs) > 0 {
		kept := b.subs[:0]
		for _, e := range b.subs {
			if e.ev.End < tsSec {
				continue
			}
			kept = append(kept, e)
		}
		b.subs = kept
	}
}

// ReadPCMRange returns a copy of the contiguous PCM bytes whose anchoring tsSec
// falls in [fromSec, toSec). ok is false when no PCM entry intersects the range.
//
// ADDITIVE (not in the frozen set): exposed for the ASR driver (asr.go) to pull
// the PCM span it needs to transcribe. Documented in the worker-buffer handoff.
func (b *Buffer) ReadPCMRange(fromSec, toSec float64) (data []byte, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []byte
	for _, e := range b.pcm {
		if e.tsSec < fromSec {
			continue
		}
		if e.tsSec >= toSec {
			break
		}
		out = append(out, e.bytes...)
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// ReadContiguousPCMFrom returns the LEADING CONTIGUOUS run of buffered PCM
// starting at the first entry whose tsSec >= fromSec, stopping at the first
// timeline discontinuity (a reconnect gap). It returns the concatenated bytes of
// that run, spanEnd (the timeline position just past the last byte of the run),
// and ok=false when no PCM entry intersects [fromSec, +inf).
//
// Contiguity (Fix 3): consecutive entries are contiguous when the next entry's
// tsSec is not meaningfully ahead of the running end of the bytes already
// accumulated. The running end advances by len(bytes)/bytesPerSec per entry
// (the PCM track is fixed-rate s16le), so the next entry is part of the same run
// iff next.tsSec <= runningEnd + epsilon. epsilon is one PCM read-chunk's worth
// of cadence — small enough to detect a real reconnect hole, loose enough to
// tolerate the sub-chunk rounding of the ingestor's byte clock.
//
// Bounding each ASR pass to this leading run prevents a gap from mis-anchoring
// every later subtitle: ReadPCMRange would concatenate across the hole and shift
// all post-gap times. ADDITIVE (not in the frozen set).
func (b *Buffer) ReadContiguousPCMFrom(fromSec float64, bytesPerSec int) (data []byte, spanEnd float64, ok bool) {
	if bytesPerSec <= 0 {
		return nil, 0, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	// epsilon: ~one read chunk of cadence. readChunkSize matches the ingestor's
	// per-read size; dividing by the byte rate yields its duration in seconds.
	const readChunkSize = 8192
	epsilon := float64(readChunkSize) / float64(bytesPerSec)

	var out []byte
	started := false
	runningEnd := 0.0
	for _, e := range b.pcm {
		if e.tsSec < fromSec {
			continue
		}
		if !started {
			started = true
			out = append(out, e.bytes...)
			runningEnd = e.tsSec + float64(len(e.bytes))/float64(bytesPerSec)
			continue
		}
		// A discontinuity: this entry starts meaningfully after the running end of
		// the contiguous span so far. Stop — leave the post-gap PCM for a later
		// pass once the cursor has advanced past the hole.
		if e.tsSec > runningEnd+epsilon {
			break
		}
		out = append(out, e.bytes...)
		runningEnd = e.tsSec + float64(len(e.bytes))/float64(bytesPerSec)
	}
	if !started {
		return nil, 0, false
	}
	return out, runningEnd, true
}

// ReadSubtitlesUpTo returns a copy of all buffered subtitle events whose Start
// is at or before headSec, ordered by Start.
//
// ADDITIVE (not in the frozen set): exposed for the per-subscriber pacer to
// release subtitles as the broadcast head passes their Start. Documented in the
// worker-buffer handoff.
func (b *Buffer) ReadSubtitlesUpTo(headSec float64) []SubtitleEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []SubtitleEvent
	for _, e := range b.subs {
		if e.ev.Start > headSec {
			break
		}
		out = append(out, e.ev)
	}
	return out
}

// ReadSubtitlesAfterSeq returns, in Start order, every buffered subtitle event
// whose Start is at or before headSec AND whose append sequence is strictly
// greater than afterSeq, together with the highest sequence among the returned
// events (or afterSeq unchanged when none qualify).
//
// ADDITIVE (not in the frozen set): used by the pacer to release subtitles
// exactly once each. Keying release off the monotonic sequence rather than Start
// means two events that share the same Start are BOTH delivered (Fix 5), while
// still preserving Start-anchored ordering and the none-early guarantee (the
// caller filters by headSec here and by its own start anchor separately).
func (b *Buffer) ReadSubtitlesAfterSeq(afterSeq int64, headSec float64) (events []SubtitleEvent, lastSeq int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	lastSeq = afterSeq
	for _, e := range b.subs {
		if e.ev.Start > headSec {
			break
		}
		if e.seq <= afterSeq {
			continue
		}
		events = append(events, e.ev)
		if e.seq > lastSeq {
			lastSeq = e.seq
		}
	}
	return events, lastSeq
}

// --- ordering helpers (callers hold b.mu) ---

// pcmInsertIndex returns the index at which an entry with the given tsSec should
// be inserted to keep b.pcm ascending (stable: after equal tsSec).
func pcmInsertIndex(s []pcmEntry, tsSec float64) int {
	lo, hi := 0, len(s)
	for lo < hi {
		mid := (lo + hi) / 2
		if s[mid].tsSec <= tsSec {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// mp3InsertIndex mirrors pcmInsertIndex for the MP3 track.
func mp3InsertIndex(s []mp3Entry, tsSec float64) int {
	lo, hi := 0, len(s)
	for lo < hi {
		mid := (lo + hi) / 2
		if s[mid].tsSec <= tsSec {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// subInsertIndex mirrors pcmInsertIndex for the subtitle track, keyed by the
// event's Start (stable: after equal Start, preserving append order).
func subInsertIndex(s []subEntry, start float64) int {
	lo, hi := 0, len(s)
	for lo < hi {
		mid := (lo + hi) / 2
		if s[mid].ev.Start <= start {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// mp3BoundaryIndexAtOrBefore returns the index of the latest MP3 entry whose
// tsSec <= posSec, or 0 if every entry is after posSec. Requires len(s) > 0.
func mp3BoundaryIndexAtOrBefore(s []mp3Entry, posSec float64) int {
	// Binary search for the last index with tsSec <= posSec.
	lo, hi := 0, len(s)
	for lo < hi {
		mid := (lo + hi) / 2
		if s[mid].tsSec <= posSec {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	// lo is the count of entries with tsSec <= posSec.
	if lo == 0 {
		return 0 // posSec older than the buffer; use earliest.
	}
	return lo - 1
}
