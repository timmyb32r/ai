package broadcast

import (
	"context"
	"encoding/binary"
	"sync"
	"testing"

	"github.com/timmyb32r/001_omc_cri/internal/chineseasr"
)

// fakeSegmenter returns canned word boundaries for every call.
type fakeSegmenter struct {
	result []WordBoundary
}

func (f *fakeSegmenter) Segment(_ string) []WordBoundary {
	return f.result
}

// fakeTranscriber records the wav paths it is asked to transcribe and returns a
// canned transcript (or ErrEmptyTranscript when text is empty), simulating
// chineseasr.Transcribe without driving ffmpeg/sherpa.
type fakeTranscriber struct {
	mu    sync.Mutex
	calls int
	paths []string
	text  string // returned for every call; "" -> ErrEmptyTranscript (no speech)
	err   error
}

func (f *fakeTranscriber) Transcribe(_ context.Context, wavPath string) (*chineseasr.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.paths = append(f.paths, wavPath)
	if f.err != nil {
		return nil, f.err
	}
	if f.text == "" {
		return nil, chineseasr.ErrEmptyTranscript
	}
	return &chineseasr.Result{Text: f.text}, nil
}

// pcmFor returns sec seconds of (silent) s16le 16 kHz mono PCM.
func pcmFor(sec float64) []byte {
	return make([]byte, int(sec*float64(pcmBytesPerSec)))
}

// TestASRStep_FixedWindowAppendsSubtitle: a full window of settled audio is
// transcribed into one subtitle anchored at the region start, spanning exactly
// windowSec, and the cursor advances by one window.
func TestASRStep_FixedWindowAppendsSubtitle(t *testing.T) {
	t.Parallel()
	buf := &Buffer{}
	a := NewASR(nil, buf, nil)
	a.tempDir = t.TempDir()
	fake := &fakeTranscriber{text: "你好世界"}
	a.setTranscriber(fake)

	const regionStart = 100.0
	// 12s available: settled (12 - liveMargin 2) = 10 >= windowSec 8.
	buf.AppendPCM(regionStart, pcmFor(12))

	if err := a.step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("expected 1 transcribe call, got %d", fake.calls)
	}
	got := buf.ReadSubtitlesUpTo(1e9)
	if len(got) != 1 {
		t.Fatalf("expected 1 subtitle, got %d: %+v", len(got), got)
	}
	if got[0].Start != regionStart || got[0].End != regionStart+a.windowSec || got[0].TextZh != "你好世界" {
		t.Errorf("subtitle = %+v, want Start=%v End=%v text=你好世界", got[0], regionStart, regionStart+a.windowSec)
	}
	if want := regionStart + a.windowSec; a.cursor != want {
		t.Errorf("cursor = %v, want %v", a.cursor, want)
	}
}

// TestASRStep_WaitsForFullWindow: with less than windowSec+liveMargin of audio,
// no transcription happens and the cursor stays put.
func TestASRStep_WaitsForFullWindow(t *testing.T) {
	t.Parallel()
	buf := &Buffer{}
	a := NewASR(nil, buf, nil)
	a.tempDir = t.TempDir()
	fake := &fakeTranscriber{text: "x"}
	a.setTranscriber(fake)

	// 9s available: settled 7 < windowSec 8 -> wait.
	buf.AppendPCM(0, pcmFor(9))
	if err := a.step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}
	if fake.calls != 0 {
		t.Fatalf("expected no transcribe call, got %d", fake.calls)
	}
	if a.cursor != 0 {
		t.Errorf("cursor moved to %v with no full window", a.cursor)
	}
}

// TestASRStep_MusicOnlyWindowAdvancesNoSubtitle: a window that transcribes to
// empty (ErrEmptyTranscript, e.g. music only) emits no subtitle but still
// advances the cursor so it is never re-scanned.
func TestASRStep_MusicOnlyWindowAdvancesNoSubtitle(t *testing.T) {
	t.Parallel()
	buf := &Buffer{}
	a := NewASR(nil, buf, nil)
	a.tempDir = t.TempDir()
	fake := &fakeTranscriber{text: ""} // -> ErrEmptyTranscript
	a.setTranscriber(fake)

	const regionStart = 50.0
	buf.AppendPCM(regionStart, pcmFor(12))
	if err := a.step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("expected 1 transcribe call, got %d", fake.calls)
	}
	if n := len(buf.ReadSubtitlesUpTo(1e9)); n != 0 {
		t.Errorf("empty window must not produce a subtitle, got %d", n)
	}
	if want := regionStart + a.windowSec; a.cursor != want {
		t.Errorf("cursor = %v, want %v (advanced past the empty window)", a.cursor, want)
	}
}

// TestASRStep_GapBoundsContiguousRun: a far-future post-gap PCM run is not
// concatenated into the current window; the subtitle anchors to the leading
// contiguous run's start, not drifting toward the gap.
func TestASRStep_GapBoundsContiguousRun(t *testing.T) {
	t.Parallel()
	buf := &Buffer{}
	a := NewASR(nil, buf, nil)
	a.tempDir = t.TempDir()
	fake := &fakeTranscriber{text: "你好"}
	a.setTranscriber(fake)

	// Leading contiguous run [10,26) (16s), then a GAP and a far-future run at 100.
	buf.AppendPCM(10.0, pcmFor(8))
	buf.AppendPCM(18.0, pcmFor(8))
	buf.AppendPCM(100.0, pcmFor(8)) // reconnect hole

	if err := a.step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}
	subs := buf.ReadSubtitlesUpTo(1e9)
	if len(subs) != 1 {
		t.Fatalf("got %d subtitles, want 1", len(subs))
	}
	// Anchored to the leading run start (10), spanning one window — NOT drifting
	// toward the gap/post-gap audio at 100.
	if subs[0].Start != 10.0 || subs[0].End != 10.0+a.windowSec {
		t.Fatalf("subtitle = %+v, want Start=10 End=%v", subs[0], 10.0+a.windowSec)
	}
	if a.cursor != 10.0+a.windowSec {
		t.Fatalf("cursor = %v, want %v (within the leading run, not jumped to 100)", a.cursor, 10.0+a.windowSec)
	}
}

// TestASRStep_WordBoundariesAttached verifies that a segmenter's output is
// attached to the SubtitleEvent stored in the buffer.
func TestASRStep_WordBoundariesAttached(t *testing.T) {
	t.Parallel()
	buf := &Buffer{}
	a := NewASR(nil, buf, nil)
	a.tempDir = t.TempDir()
	fake := &fakeTranscriber{text: "你好世界"}
	a.setTranscriber(fake)
	seg := &fakeSegmenter{result: []WordBoundary{{CharStart: 0, CharEnd: 2}, {CharStart: 2, CharEnd: 4}}}
	a.setSegmenter(seg)

	buf.AppendPCM(0, pcmFor(12))
	if err := a.step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}
	got := buf.ReadSubtitlesUpTo(1e9)
	if len(got) != 1 {
		t.Fatalf("expected 1 subtitle, got %d", len(got))
	}
	wantWords := []WordBoundary{{CharStart: 0, CharEnd: 2}, {CharStart: 2, CharEnd: 4}}
	if len(got[0].Words) != len(wantWords) {
		t.Fatalf("expected %d word boundaries, got %d: %+v",
			len(wantWords), len(got[0].Words), got[0].Words)
	}
	for i, w := range wantWords {
		if got[0].Words[i] != w {
			t.Errorf("word[%d] = %+v, want %+v", i, got[0].Words[i], w)
		}
	}
}

// TestASRStep_NilSegmenterNoWords verifies that when the segmenter is nil,
// the Words field is empty (backward-compatible).
func TestASRStep_NilSegmenterNoWords(t *testing.T) {
	t.Parallel()
	buf := &Buffer{}
	a := NewASR(nil, buf, nil)
	a.tempDir = t.TempDir()
	fake := &fakeTranscriber{text: "测试"}
	a.setTranscriber(fake)

	buf.AppendPCM(0, pcmFor(12))
	if err := a.step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}
	got := buf.ReadSubtitlesUpTo(1e9)
	if len(got) != 1 {
		t.Fatalf("expected 1 subtitle, got %d", len(got))
	}
	if len(got[0].Words) != 0 {
		t.Fatalf("expected no words with nil segmenter, got %d: %+v",
			len(got[0].Words), got[0].Words)
	}
}

func TestWAVHeader_CanonicalFields(t *testing.T) {
	t.Parallel()
	const dataLen = 320 // 0.01s of s16le 16k mono
	h := wavHeader(dataLen)
	if len(h) != 44 {
		t.Fatalf("header len = %d, want 44", len(h))
	}
	if string(h[0:4]) != "RIFF" || string(h[8:12]) != "WAVE" || string(h[12:16]) != "fmt " || string(h[36:40]) != "data" {
		t.Errorf("bad chunk ids: %q %q %q %q", h[0:4], h[8:12], h[12:16], h[36:40])
	}
	if got := binary.LittleEndian.Uint32(h[4:8]); got != uint32(36+dataLen) {
		t.Errorf("ChunkSize = %d, want %d", got, 36+dataLen)
	}
	if got := binary.LittleEndian.Uint16(h[20:22]); got != 1 {
		t.Errorf("AudioFormat = %d, want 1 (PCM)", got)
	}
	if got := binary.LittleEndian.Uint16(h[22:24]); got != 1 {
		t.Errorf("NumChannels = %d, want 1", got)
	}
	if got := binary.LittleEndian.Uint32(h[24:28]); got != 16000 {
		t.Errorf("SampleRate = %d, want 16000", got)
	}
	if got := binary.LittleEndian.Uint32(h[28:32]); got != 32000 {
		t.Errorf("ByteRate = %d, want 32000", got)
	}
	if got := binary.LittleEndian.Uint16(h[34:36]); got != 16 {
		t.Errorf("BitsPerSample = %d, want 16", got)
	}
	if got := binary.LittleEndian.Uint32(h[40:44]); got != uint32(dataLen) {
		t.Errorf("Subchunk2Size = %d, want %d", got, dataLen)
	}
}
