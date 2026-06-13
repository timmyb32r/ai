package chineseasr

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// segRunner is a Runner for the TranscribeSegments pipeline. It distinguishes
// three call shapes by inspecting the args:
//
//   - the silencedetect probe (an arg starts with "silencedetect="): returns
//     canned stderr carrying silence markers, empty stdout;
//   - an ffmpeg slice cut or decode (the binary name contains "ffmpeg" and it
//     is not the silencedetect probe): writes nothing, returns empty;
//   - the sherpa engine call (any other binary): returns the golden stdout.
//
// It also records, per sherpa call, that it was reached so the test can assert
// the number of transcribed regions.
type segRunner struct {
	ffmpegPath  string
	silenceErr  []byte // stderr returned by the silencedetect probe
	sherpaOut   []byte // stdout returned by each engine call
	silenceCnt  int    // silencedetect probe invocations
	ffmpegCnt   int    // ffmpeg slice/decode invocations
	sherpaCalls int    // engine invocations
}

func (r *segRunner) Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error) {
	isFFmpeg := strings.Contains(name, "ffmpeg") || name == r.ffmpegPath
	if isFFmpeg {
		for _, a := range args {
			if strings.HasPrefix(a, "silencedetect=") {
				r.silenceCnt++
				return nil, r.silenceErr, nil
			}
		}
		// A slice cut or Transcribe's own decode: produces a wav on disk; no
		// captured output. Tests don't need the file to exist because the
		// engine stage is faked too.
		r.ffmpegCnt++
		return nil, nil, nil
	}
	// Engine stage.
	r.sherpaCalls++
	return r.sherpaOut, nil, nil
}

// newSegTranscriber builds a Transcriber with a real on-disk input wav (so the
// requireFile guard passes) and the given segRunner injected.
func newSegTranscriber(t *testing.T, run *segRunner) (*Transcriber, string) {
	t.Helper()
	cfg, ffmpeg := validConfig(t)
	run.ffmpegPath = ffmpeg
	tr, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tr.setRunner(run)

	wav := filepath.Join(t.TempDir(), "input.wav")
	writeFile(t, wav, "fake wav bytes")
	return tr, wav
}

func TestTranscribeSegments_TwoRegions(t *testing.T) {
	// Two bounded speech regions plus a not-silence-terminated tail. The tail
	// (open-ended End == 0) must be skipped, so only two regions are returned.
	//   speech [0, 1.0]
	//   silence [1.0, 2.0]
	//   speech  [2.0, 4.5]
	//   silence [4.5, 5.0]
	//   speech  [5.0, end)  -> open-ended tail, NOT transcribed
	silence := []byte(
		"[silencedetect @ 0xabc] silence_start: 1.0\n" +
			"[silencedetect @ 0xabc] silence_end: 2.0 | silence_duration: 1.0\n" +
			"[silencedetect @ 0xabc] silence_start: 4.5\n" +
			"[silencedetect @ 0xabc] silence_end: 5.0 | silence_duration: 0.5\n",
	)
	run := &segRunner{silenceErr: silence, sherpaOut: readGolden(t)}
	tr, wav := newSegTranscriber(t, run)

	segs, err := tr.TranscribeSegments(context.Background(), wav)
	if err != nil {
		t.Fatalf("TranscribeSegments: %v", err)
	}

	want := []Segment{
		{Start: 0, End: 1.0, Text: goldenText},
		{Start: 2.0, End: 4.5, Text: goldenText},
	}
	if len(segs) != len(want) {
		t.Fatalf("got %d segments, want %d: %#v", len(segs), len(want), segs)
	}
	for i := range want {
		if segs[i] != want[i] {
			t.Errorf("segment[%d] = %#v, want %#v", i, segs[i], want[i])
		}
	}
	// The golden text carries punctuation; assert it survived end to end.
	if !strings.ContainsAny(segs[0].Text, "，。") {
		t.Errorf("segment text lost punctuation: %q", segs[0].Text)
	}

	// One silencedetect probe; two transcribed regions (the open-ended tail is
	// skipped, so the engine ran exactly twice).
	if run.silenceCnt != 1 {
		t.Errorf("silencedetect probe ran %d times, want 1", run.silenceCnt)
	}
	if run.sherpaCalls != 2 {
		t.Errorf("engine ran %d times, want 2 (open-ended tail skipped)", run.sherpaCalls)
	}
}

func TestTranscribeSegments_NoSpeech(t *testing.T) {
	// Ongoing silence from the start: no speech regions at all. The result is
	// an empty (nil) slice and the engine is never invoked.
	silence := []byte(
		"[silencedetect @ 0xabc] silence_start: 0\n",
	)
	run := &segRunner{silenceErr: silence, sherpaOut: readGolden(t)}
	tr, wav := newSegTranscriber(t, run)

	segs, err := tr.TranscribeSegments(context.Background(), wav)
	if err != nil {
		t.Fatalf("TranscribeSegments: %v", err)
	}
	if len(segs) != 0 {
		t.Fatalf("got %d segments, want 0: %#v", len(segs), segs)
	}
	if run.sherpaCalls != 0 {
		t.Errorf("engine ran %d times for no-speech input, want 0", run.sherpaCalls)
	}
}

func TestTranscribeSegments_OnlyOpenEndedTailSkipped(t *testing.T) {
	// No silence at all -> a single open-ended region {0,0}. It is not
	// silence-terminated, so it is left for the caller's next pass: zero
	// segments, no engine call.
	silence := []byte(
		"[Parsed_silencedetect_0 @ 0xabc] No silence detected\n",
	)
	run := &segRunner{silenceErr: silence, sherpaOut: readGolden(t)}
	tr, wav := newSegTranscriber(t, run)

	segs, err := tr.TranscribeSegments(context.Background(), wav)
	if err != nil {
		t.Fatalf("TranscribeSegments: %v", err)
	}
	if len(segs) != 0 {
		t.Fatalf("got %d segments, want 0 (open-ended tail skipped): %#v", len(segs), segs)
	}
	if run.sherpaCalls != 0 {
		t.Errorf("engine ran %d times, want 0 (open-ended tail not transcribed)", run.sherpaCalls)
	}
}

func TestTranscribeSegments_ContextCancelled(t *testing.T) {
	run := &segRunner{
		silenceErr: []byte("[silencedetect @ 0xabc] silence_start: 1.0\n"),
		sherpaOut:  readGolden(t),
	}
	tr, wav := newSegTranscriber(t, run)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := tr.TranscribeSegments(ctx, wav)
	if err == nil {
		t.Fatal("TranscribeSegments on cancelled ctx: want error, got nil")
	}
	if run.silenceCnt != 0 {
		t.Errorf("silencedetect ran %d times on cancelled ctx, want 0", run.silenceCnt)
	}
}

func TestTranscribeSegments_MissingInput(t *testing.T) {
	run := &segRunner{sherpaOut: readGolden(t)}
	tr, _ := newSegTranscriber(t, run)

	missing := filepath.Join(t.TempDir(), "does-not-exist.wav")
	_, err := tr.TranscribeSegments(context.Background(), missing)
	if err == nil {
		t.Fatal("TranscribeSegments(missing): want error, got nil")
	}
}
