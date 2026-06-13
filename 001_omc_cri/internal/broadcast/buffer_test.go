package broadcast

import (
	"bytes"
	"sync"
	"testing"
)

func TestBufferReadMP3FromFrameAligned(t *testing.T) {
	var b Buffer
	// Three frame-aligned chunks on the timeline.
	b.AppendMP3(0.0, []byte("AAAA")) // boundary 0.0
	b.AppendMP3(0.1, []byte("BBBB")) // boundary 0.1
	b.AppendMP3(0.2, []byte("CCCC")) // boundary 0.2

	// Read from exactly a boundary.
	data, start, next := b.ReadMP3From(0.1, 1000)
	if start != 0.1 {
		t.Fatalf("frameStartTsSec = %v, want 0.1", start)
	}
	if want := []byte("BBBBCCCC"); !bytes.Equal(data, want) {
		t.Fatalf("data = %q, want %q", data, want)
	}
	if next != 0.2 {
		t.Fatalf("nextTsSec = %v, want 0.2 (last boundary returned)", next)
	}

	// Read from BETWEEN boundaries: 0.15 must snap back to 0.1.
	data, start, _ = b.ReadMP3From(0.15, 1000)
	if start != 0.1 {
		t.Fatalf("frameStartTsSec for 0.15 = %v, want 0.1 (boundary at or before)", start)
	}
	if want := []byte("BBBBCCCC"); !bytes.Equal(data, want) {
		t.Fatalf("data for 0.15 = %q, want %q", data, want)
	}
}

func TestBufferReadMP3FromOlderThanBuffer(t *testing.T) {
	var b Buffer
	b.AppendMP3(5.0, []byte("XYZ"))
	b.AppendMP3(5.1, []byte("123"))

	// fromTsSec older than everything -> earliest available.
	data, start, _ := b.ReadMP3From(0.0, 1000)
	if start != 5.0 {
		t.Fatalf("frameStartTsSec = %v, want 5.0 (earliest available)", start)
	}
	if want := []byte("XYZ123"); !bytes.Equal(data, want) {
		t.Fatalf("data = %q, want %q", data, want)
	}
}

func TestBufferReadMP3FromMaxBytes(t *testing.T) {
	var b Buffer
	b.AppendMP3(0.0, []byte("AAAA")) // 4 bytes
	b.AppendMP3(0.1, []byte("BBBB")) // 4 bytes
	b.AppendMP3(0.2, []byte("CCCC")) // 4 bytes

	// maxBytes=5: first entry (4) fits; second (would be 8) doesn't -> stop.
	data, start, next := b.ReadMP3From(0.0, 5)
	if start != 0.0 {
		t.Fatalf("frameStartTsSec = %v, want 0.0", start)
	}
	if want := []byte("AAAA"); !bytes.Equal(data, want) {
		t.Fatalf("data = %q, want %q (must not split a frame entry)", data, want)
	}
	if next != 0.1 {
		t.Fatalf("nextTsSec = %v, want 0.1 (next un-returned boundary)", next)
	}

	// Continue from where we left off.
	data, start, _ = b.ReadMP3From(next, 5)
	if start != 0.1 {
		t.Fatalf("continued frameStartTsSec = %v, want 0.1", start)
	}
	if want := []byte("BBBB"); !bytes.Equal(data, want) {
		t.Fatalf("continued data = %q, want %q", data, want)
	}
}

func TestBufferReadMP3FromEmpty(t *testing.T) {
	var b Buffer
	data, start, next := b.ReadMP3From(3.0, 100)
	if data != nil || start != 0 || next != 3.0 {
		t.Fatalf("empty read = (%q, %v, %v), want (nil, 0, 3.0)", data, start, next)
	}
}

func TestBufferReadMP3FromSingleEntryGivesProgress(t *testing.T) {
	var b Buffer
	b.AppendMP3(1.0, []byte("HELLOWORLD")) // 10 bytes

	// maxBytes smaller than the single entry: still return it (progress guarantee).
	data, start, next := b.ReadMP3From(1.0, 3)
	if start != 1.0 {
		t.Fatalf("frameStartTsSec = %v, want 1.0", start)
	}
	if want := []byte("HELLOWORLD"); !bytes.Equal(data, want) {
		t.Fatalf("data = %q, want full single entry %q", data, want)
	}
	if next != 1.0 {
		t.Fatalf("nextTsSec = %v, want 1.0 (last boundary)", next)
	}
}

// TestBufferReadMP3FromPastTailNoProgress verifies the re-send fix at the buffer
// level: once the read position advances strictly past the latest frame
// boundary, ReadMP3From reports no forward progress (nil data, nextTsSec ==
// fromTsSec) instead of re-returning the last frame. A read landing exactly on
// the last boundary still returns that frame (first delivery of the tail).
func TestBufferReadMP3FromPastTailNoProgress(t *testing.T) {
	var b Buffer
	b.AppendMP3(0.0, []byte("AAAA"))
	b.AppendMP3(1.0, []byte("BBBB")) // last boundary = 1.0

	// Exactly on the last boundary: still delivered (first read of the tail frame).
	data, start, next := b.ReadMP3From(1.0, 1000)
	if !bytes.Equal(data, []byte("BBBB")) || start != 1.0 || next != 1.0 {
		t.Fatalf("read at last boundary = (%q,%v,%v), want (BBBB,1.0,1.0)", data, start, next)
	}

	// Strictly past the last boundary: no progress, no bytes re-returned.
	data, start, next = b.ReadMP3From(1.5, 1000)
	if data != nil {
		t.Fatalf("read past tail returned %q, want nil (no re-send of last frame)", data)
	}
	if start != 1.0 {
		t.Fatalf("read past tail frameStart = %v, want 1.0 (last boundary)", start)
	}
	if next != 1.5 {
		t.Fatalf("read past tail next = %v, want 1.5 (== from, no forward progress)", next)
	}

	// A newer frame appended past the old tail moves lastBoundary to 2.0, so the
	// SAME read position (1.5) is no longer past the tail and again makes forward
	// progress. The buffer is stateless (snap-back to the boundary <= from), so it
	// returns from 1.0 onward; the pacer is what guarantees each frame is sent
	// once (it advances curPos strictly past every delivered frame). Here we just
	// assert the no-progress guard releases once a newer frame exists.
	b.AppendMP3(2.0, []byte("CCCC"))
	data, _, next = b.ReadMP3From(1.5, 1000)
	if len(data) == 0 {
		t.Fatalf("after appending a newer frame, read(1.5) made no progress: %q", data)
	}
	if next == 1.5 {
		t.Fatalf("after appending a newer frame, read(1.5) still reports no progress (next=%v)", next)
	}
}

func TestBufferFrameBoundaryAtOrBefore(t *testing.T) {
	var b Buffer
	if got := b.FrameBoundaryAtOrBefore(1.0); got != 0 {
		t.Fatalf("empty FrameBoundaryAtOrBefore = %v, want 0", got)
	}
	b.AppendMP3(2.0, []byte("a"))
	b.AppendMP3(4.0, []byte("b"))
	b.AppendMP3(6.0, []byte("c"))

	cases := []struct {
		pos  float64
		want float64
	}{
		{pos: 1.0, want: 2.0},  // before everything -> earliest
		{pos: 2.0, want: 2.0},  // exact
		{pos: 3.9, want: 2.0},  // between -> earlier boundary
		{pos: 4.0, want: 4.0},  // exact
		{pos: 5.5, want: 4.0},  // between
		{pos: 6.0, want: 6.0},  // exact
		{pos: 99.0, want: 6.0}, // after everything -> latest
	}
	for _, c := range cases {
		if got := b.FrameBoundaryAtOrBefore(c.pos); got != c.want {
			t.Errorf("FrameBoundaryAtOrBefore(%v) = %v, want %v", c.pos, got, c.want)
		}
	}
}

func TestBufferEvictBefore(t *testing.T) {
	var b Buffer
	b.AppendPCM(0.0, []byte("p0"))
	b.AppendPCM(1.0, []byte("p1"))
	b.AppendPCM(2.0, []byte("p2"))
	b.AppendMP3(0.0, []byte("m0"))
	b.AppendMP3(1.0, []byte("m1"))
	b.AppendMP3(2.0, []byte("m2"))
	b.AppendSubtitle(SubtitleEvent{Start: 0.0, End: 0.5, TextZh: "old"})
	b.AppendSubtitle(SubtitleEvent{Start: 1.0, End: 3.0, TextZh: "spanning"})
	b.AppendSubtitle(SubtitleEvent{Start: 2.0, End: 2.5, TextZh: "new"})

	b.EvictBefore(1.0)

	// PCM/MP3 anchored strictly before 1.0 are gone.
	if data, _ := b.ReadPCMRange(0.0, 100.0); !bytes.Equal(data, []byte("p1p2")) {
		t.Fatalf("after evict, PCM = %q, want %q", data, "p1p2")
	}
	if _, start, _ := b.ReadMP3From(0.0, 1000); start != 1.0 {
		t.Fatalf("after evict, earliest MP3 boundary = %v, want 1.0", start)
	}
	// Subtitles: "old" (End 0.5 < 1.0) gone; "spanning" (End 3.0) and "new" kept.
	subs := b.ReadSubtitlesUpTo(100.0)
	if len(subs) != 2 || subs[0].TextZh != "spanning" || subs[1].TextZh != "new" {
		t.Fatalf("after evict, subs = %+v, want [spanning new]", subs)
	}
}

func TestBufferReadPCMRange(t *testing.T) {
	var b Buffer
	b.AppendPCM(0.0, []byte("aa"))
	b.AppendPCM(1.0, []byte("bb"))
	b.AppendPCM(2.0, []byte("cc"))

	data, ok := b.ReadPCMRange(1.0, 2.0)
	if !ok || !bytes.Equal(data, []byte("bb")) {
		t.Fatalf("ReadPCMRange(1,2) = (%q,%v), want (bb,true)", data, ok)
	}
	data, ok = b.ReadPCMRange(0.0, 3.0)
	if !ok || !bytes.Equal(data, []byte("aabbcc")) {
		t.Fatalf("ReadPCMRange(0,3) = (%q,%v), want (aabbcc,true)", data, ok)
	}
	if _, ok := b.ReadPCMRange(10.0, 20.0); ok {
		t.Fatalf("ReadPCMRange(10,20) ok = true, want false (no entries)")
	}
}

func TestBufferReadSubtitlesUpTo(t *testing.T) {
	var b Buffer
	// Insert out of order to exercise sorted insertion.
	b.AppendSubtitle(SubtitleEvent{Start: 2.0, End: 2.5, TextZh: "c"})
	b.AppendSubtitle(SubtitleEvent{Start: 0.0, End: 0.5, TextZh: "a"})
	b.AppendSubtitle(SubtitleEvent{Start: 1.0, End: 1.5, TextZh: "b"})

	got := b.ReadSubtitlesUpTo(1.0)
	if len(got) != 2 || got[0].TextZh != "a" || got[1].TextZh != "b" {
		t.Fatalf("ReadSubtitlesUpTo(1.0) = %+v, want [a b] in order", got)
	}
}

func TestBufferAppendCopiesInput(t *testing.T) {
	var b Buffer
	src := []byte("XYZ")
	b.AppendMP3(0.0, src)
	src[0] = 'Q' // mutate caller's slice after append
	data, _, _ := b.ReadMP3From(0.0, 100)
	if !bytes.Equal(data, []byte("XYZ")) {
		t.Fatalf("buffer retained reference to caller slice: got %q", data)
	}
}

func TestBufferConcurrentAppendRead(t *testing.T) {
	var b Buffer
	const writers = 4
	const reads = 2000
	var wg sync.WaitGroup

	// Concurrent PCM/MP3/subtitle writers, monotonically advancing ts per writer.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				ts := float64(w)*1000 + float64(i)*0.01
				b.AppendPCM(ts, []byte("pcm"))
				b.AppendMP3(ts, []byte("mp3"))
				b.AppendSubtitle(SubtitleEvent{Start: ts, End: ts + 0.05, TextZh: "x"})
			}
		}(w)
	}

	// Concurrent readers + an evictor.
	for r := 0; r < 3; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < reads; i++ {
				_, _, next := b.ReadMP3From(float64(i)*0.005, 64)
				_ = b.FrameBoundaryAtOrBefore(next)
				_, _ = b.ReadPCMRange(0, next+1)
				_ = b.ReadSubtitlesUpTo(next)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < reads; i++ {
			b.EvictBefore(float64(i) * 0.001)
		}
	}()

	wg.Wait()
}
