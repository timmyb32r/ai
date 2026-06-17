package broadcast

import (
	"bytes"
	"sync"
	"testing"
)

func TestBufferEvictBefore(t *testing.T) {
	var b Buffer
	b.AppendPCM(0.0, []byte("p0"))
	b.AppendPCM(1.0, []byte("p1"))
	b.AppendPCM(2.0, []byte("p2"))
	b.AppendSubtitle(SubtitleEvent{Start: 0.0, End: 0.5, TextZh: "old"})
	b.AppendSubtitle(SubtitleEvent{Start: 1.0, End: 3.0, TextZh: "spanning"})
	b.AppendSubtitle(SubtitleEvent{Start: 2.0, End: 2.5, TextZh: "new"})

	b.EvictBefore(1.0)

	// PCM anchored strictly before 1.0 are gone.
	if data, _, _ := b.ReadContiguousPCMFrom(0.0, 2); !bytes.Equal(data, []byte("p1p2")) {
		t.Fatalf("after evict, PCM = %q, want %q", data, "p1p2")
	}
	// Subtitles: "old" (End 0.5 < 1.0) gone; "spanning" (End 3.0) and "new" kept.
	subs := b.ReadSubtitlesUpTo(100.0)
	if len(subs) != 2 || subs[0].TextZh != "spanning" || subs[1].TextZh != "new" {
		t.Fatalf("after evict, subs = %+v, want [spanning new]", subs)
	}
}

func TestBufferReset(t *testing.T) {
	var b Buffer
	b.AppendPCM(0.0, []byte("pcm"))
	b.AppendSubtitle(SubtitleEvent{Start: 0.0, End: 1.0, TextZh: "hello"})

	b.Reset()

	if data, _, _ := b.ReadContiguousPCMFrom(0.0, 2); data != nil {
		t.Fatalf("after reset, PCM = %q, want nil", data)
	}
	subs := b.ReadSubtitlesUpTo(100.0)
	if len(subs) != 0 {
		t.Fatalf("after reset, subs = %+v, want empty", subs)
	}
}

func TestBufferReadContiguousPCMFrom(t *testing.T) {
	var b Buffer
	b.AppendPCM(0.0, []byte("aa"))
	b.AppendPCM(1.0, []byte("bb"))
	b.AppendPCM(2.0, []byte("cc"))

	data, _, ok := b.ReadContiguousPCMFrom(1.0, 2)
	if !ok || !bytes.Equal(data, []byte("bbcc")) {
		t.Fatalf("ReadContiguousPCMFrom(1,2) = (%q,%v), want (bbcc,true)", data, ok)
	}
	data, _, ok = b.ReadContiguousPCMFrom(0.0, 2)
	if !ok || !bytes.Equal(data, []byte("aabbcc")) {
		t.Fatalf("ReadContiguousPCMFrom(0,2) = (%q,%v), want (aabbcc,true)", data, ok)
	}
	if _, _, ok := b.ReadContiguousPCMFrom(10.0, 2); ok {
		t.Fatalf("ReadContiguousPCMFrom(10,2) ok = true, want false (no entries)")
	}
}

func TestBufferReadPCMFrom(t *testing.T) {
	var b Buffer
	b.AppendPCM(0.0, []byte("AAAA"))
	b.AppendPCM(2.0, []byte("BBBB"))
	b.AppendPCM(4.0, []byte("CCCC"))

	// Read from exactly a boundary.
	data, start, next := b.ReadPCMFrom(0.0, 100)
	if start != 0.0 {
		t.Fatalf("start = %v, want 0.0", start)
	}
	if want := []byte("AAAABBBBCCCC"); !bytes.Equal(data, want) {
		t.Fatalf("data = %q, want %q", data, want)
	}
	wantNext := 0.0 + float64(len(data))/float64(pcmBytesPerSec)
	if next != wantNext {
		t.Fatalf("next = %v, want %v (past end = end of data)", next, wantNext)
	}

	// Read with maxBytes limiting mid-entry.
	data, start, next = b.ReadPCMFrom(0.0, 6)
	if start != 0.0 {
		t.Fatalf("start with maxBytes = %v, want 0.0", start)
	}
	if want := []byte("AAAABB"); !bytes.Equal(data, want) {
		t.Fatalf("data with maxBytes = %q, want %q", data, want)
	}
	// PCM is continuous: next is byte-position after returned data.
	wantNext = float64(len(data)) / float64(pcmBytesPerSec)
	if next != wantNext {
		t.Fatalf("next with maxBytes = %v, want %v", next, wantNext)
	}

	// Read from within an entry (byte offset).
	data, start, next = b.ReadPCMFrom(1.0, 100)
	if start != 2.0 {
		t.Fatalf("start from mid-entry = %v, want 2.0 (next entry)", start)
	}
	if want := []byte("BBBBCCCC"); !bytes.Equal(data, want) {
		t.Fatalf("data from mid-entry = %q, want %q", data, want)
	}
}

func TestBufferReadPCMFromEmpty(t *testing.T) {
	var b Buffer
	data, start, next := b.ReadPCMFrom(3.0, 100)
	if data != nil || start != 0 || next != 3.0 {
		t.Fatalf("empty read = (%q, %v, %v), want (nil, 0, 3.0)", data, start, next)
	}
}

func TestBufferReadPCMFromPastTail(t *testing.T) {
	var b Buffer
	b.AppendPCM(0.0, []byte("AAAA"))
	b.AppendPCM(2.0, []byte("BBBB"))

	// Read past the end of all data.
	data, start, next := b.ReadPCMFrom(10.0, 100)
	if data != nil {
		t.Fatalf("read past tail returned %q, want nil", data)
	}
	// start should be the last entry's tsSec (the one at/before 10.0)
	if start != 2.0 {
		t.Fatalf("start past tail = %v, want 2.0", start)
	}
	// next should equal fromTsSec since there's no data to advance
	if next != 10.0 {
		t.Fatalf("next past tail = %v, want 10.0 (no forward progress)", next)
	}
}

func TestBufferPCMFrameBoundaryAtOrBefore(t *testing.T) {
	var b Buffer
	if got := b.PCMFrameBoundaryAtOrBefore(1.0); got != 0 {
		t.Fatalf("empty PCMFrameBoundaryAtOrBefore = %v, want 0", got)
	}
	b.AppendPCM(2.0, []byte("a"))
	b.AppendPCM(4.0, []byte("b"))
	b.AppendPCM(6.0, []byte("c"))

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
		if got := b.PCMFrameBoundaryAtOrBefore(c.pos); got != c.want {
			t.Errorf("PCMFrameBoundaryAtOrBefore(%v) = %v, want %v", c.pos, got, c.want)
		}
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
	b.AppendPCM(0.0, src)
	src[0] = 'Q' // mutate caller's slice after append
	data, _, _ := b.ReadPCMFrom(0.0, 100)
	if !bytes.Equal(data, []byte("XYZ")) {
		t.Fatalf("buffer retained reference to caller slice: got %q", data)
	}
}

func TestBufferConcurrentAppendRead(t *testing.T) {
	var b Buffer
	const writers = 4
	const reads = 2000
	var wg sync.WaitGroup

	// Concurrent PCM/subtitle writers, monotonically advancing ts per writer.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				ts := float64(w)*1000 + float64(i)*0.01
				b.AppendPCM(ts, []byte("pcm"))
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
				_, _, next := b.ReadPCMFrom(float64(i)*0.005, 64)
				_ = b.PCMFrameBoundaryAtOrBefore(next)
				_, _, _ = b.ReadContiguousPCMFrom(0, 32000)
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
