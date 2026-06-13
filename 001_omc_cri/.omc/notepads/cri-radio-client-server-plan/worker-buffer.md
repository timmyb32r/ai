# worker-buffer (#16) — internal/broadcast/buffer.go

## Done
Implemented the thread-safe rolling timeline Buffer + buffer_test.go. All 6 frozen
methods filled; mutex-guarded; passes `go test -race` (11 tests) and `go vet`.

## Internal design (no signature changes)
- MP3: ordered []mp3Entry{tsSec, bytes}. Each AppendMP3 call = one frame-aligned
  unit starting on a boundary at tsSec. Reads never split an entry.
- PCM: ordered []pcmEntry{tsSec, bytes}.
- Subtitles: ordered by Start.
- Appends copy the caller's slice (no aliasing). Sorted insertion handles rare
  out-of-order appends; fast-path append at tail for the in-order common case.

## ReadMP3From semantics (for worker-broadcast)
- Empty buffer -> (nil, 0, fromTsSec) so caller retries same pos.
- Snaps fromTsSec back to the boundary at-or-before it; if older than buffer,
  uses earliest entry. Always returns >=1 entry when data exists (progress).
- maxBytes: stops before exceeding it, but never splits an entry.
- nextTsSec: tsSec of the first un-returned entry; if all returned, == last
  boundary (caller passing it back re-finds it and reads only NEW frames after).

## ADDITIVE EXPORTED METHODS (not in frozen set — for worker-broadcast to use)
    func (b *Buffer) ReadPCMRange(fromSec, toSec float64) (data []byte, ok bool)
      // contiguous PCM whose anchor tsSec is in [fromSec, toSec); ok=false if none. For asr.go.
    func (b *Buffer) ReadSubtitlesUpTo(headSec float64) []SubtitleEvent
      // all buffered subs with Start <= headSec, ordered by Start. For the pacer.

## EvictBefore note
PCM/MP3 dropped when tsSec < cutoff. Subtitles dropped only when End < cutoff
(so a still-in-flight, not-yet-released subtitle survives eviction).
