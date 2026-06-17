package broadcast

import "sync"

// Buffer is the thread-safe rolling timeline buffer shared by the ingest
// producer, the ASR driver, and the per-subscriber pacers. It stores two
// timeline-keyed tracks:
//   - PCM (s16le 16 kHz mono) bytes, appended by timestamp, consumed by ASR
//     and served to subscribers;
//   - SubtitleEvents, released Start-anchored by the pacer.
//
// Old data past the configured window is evicted via EvictBefore.
//
// All tracks are kept ordered ascending by their timeline anchor (tsSec for
// PCM, Start for subtitles). The ingestor delivers monotonically increasing
// chunks, so appends are normally already in order; the implementation
// nonetheless inserts in sorted position to stay correct if a slightly
// out-of-order append arrives.
//
// Every method takes b.mu so the buffer is safe for concurrent producers and
// consumers (verified under -race in buffer_test.go).
type Buffer struct {
	mu sync.Mutex

	// pcm is timeline-ordered PCM entries.
	pcm []pcmEntry

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

// subEntry is one subtitle event tagged with its monotonic append sequence.
type subEntry struct {
	ev  SubtitleEvent
	seq int64
}

// Reset clears all buffered PCM and subtitle data under the mutex, returning
// the buffer to its empty zero state.
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

// EvictBefore drops all PCM and subtitle data anchored strictly before tsSec
// to bound memory to the configured window. A subtitle is evicted only once
// its End is before tsSec so in-flight (not-yet-released) subtitles survive.
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

// ReadPCMFrom returns PCM bytes beginning from the position at or before
// fromTsSec, up to maxBytes of contiguous bytes, along with startTsSec (the
// actual timeline position of the first returned byte) and nextTsSec (the
// timeline position to request next).
//
// When no PCM data is buffered it returns (nil, 0, fromTsSec) so the caller can
// retry from the same position once data arrives. nextTsSec is the position
// just past the last returned byte.
func (b *Buffer) ReadPCMFrom(fromTsSec float64, maxBytes int) (data []byte, startTsSec float64, nextTsSec float64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.pcm) == 0 {
		return nil, 0, fromTsSec
	}

	// Find the entry at or before fromTsSec.
	idx := pcmIndexAtOrBefore(b.pcm, fromTsSec)
	entry := b.pcm[idx]

	// Calculate byte offset within this entry for fromTsSec.
	byteOffset := int((fromTsSec - entry.tsSec) * pcmBytesPerSec)
	if byteOffset < 0 {
		byteOffset = 0
	}

	// If offset is past this entry, try the next one.
	if byteOffset >= len(entry.bytes) {
		if idx+1 < len(b.pcm) {
			idx++
			entry = b.pcm[idx]
			byteOffset = 0
		} else {
			return nil, entry.tsSec, fromTsSec
		}
	}

	startTsSec = entry.tsSec + float64(byteOffset)/float64(pcmBytesPerSec)

	var out []byte
	remaining := maxBytes
	i := idx
	for ; i < len(b.pcm) && remaining > 0; i++ {
		e := b.pcm[i]
		off := 0
		if i == idx {
			off = byteOffset
		}
		take := len(e.bytes) - off
		if take > remaining {
			take = remaining
		}
		if take > 0 {
			out = append(out, e.bytes[off:off+take]...)
			remaining -= take
		}
	}

	if len(out) == 0 {
		return nil, startTsSec, fromTsSec
	}

	// PCM is continuous (no frame boundaries), so the next position is always the
	// byte position immediately after the returned data.
	nextTsSec = startTsSec + float64(len(out))/float64(pcmBytesPerSec)
	return out, startTsSec, nextTsSec
}

// PCMFrameBoundaryAtOrBefore returns the timeline position of the earliest PCM
// entry boundary at or before posSec, or the earliest available if none is at
// or before posSec. Returns 0 when no PCM data is buffered.
func (b *Buffer) PCMFrameBoundaryAtOrBefore(posSec float64) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pcm) == 0 {
		return 0
	}
	return b.pcm[pcmIndexAtOrBefore(b.pcm, posSec)].tsSec
}

// pcmIndexAtOrBefore returns the index of the latest PCM entry whose
// tsSec <= posSec, or 0 if every entry is after posSec. Requires len(s) > 0.
func pcmIndexAtOrBefore(s []pcmEntry, posSec float64) int {
	lo, hi := 0, len(s)
	for lo < hi {
		mid := (lo + hi) / 2
		if s[mid].tsSec <= posSec {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		return 0
	}
	return lo - 1
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

