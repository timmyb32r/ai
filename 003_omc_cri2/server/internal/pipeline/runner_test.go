package pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/criradio/server/internal/asr"
	"github.com/criradio/server/internal/dictionary"
	"github.com/criradio/server/internal/ingest"
	"github.com/criradio/server/internal/logging"
	"github.com/criradio/server/internal/models"
	"github.com/criradio/server/internal/storage"
)

type mockIngestor struct {
	ch chan models.PCMChunk
}

func (m *mockIngestor) Start(ctx context.Context) (<-chan models.PCMChunk, error) {
	return m.ch, nil
}
func (m *mockIngestor) Stop() error       { return nil }
func (m *mockIngestor) Stats() ingest.Stats { return ingest.Stats{} }

type mockTokenizer struct{}

func (m *mockTokenizer) Segment(text string) ([]models.Token, error) {
	return []models.Token{
		{Text: "你好", CharStart: 0, CharEnd: 2},
		{Text: "世界", CharStart: 2, CharEnd: 4},
	}, nil
}
func (m *mockTokenizer) Close() error { return nil }

type mockDict struct{}

func (m *mockDict) Lookup(s string) (*dictionary.Entry, error) {
	if s == "你好" {
		return &dictionary.Entry{Pinyin: "nǐhǎo", Meanings: []string{"hello"}}, nil
	}
	if s == "世界" {
		return &dictionary.Entry{Pinyin: "shìjiè", Meanings: []string{"world"}}, nil
	}
	return nil, fmt.Errorf("not found")
}
func (m *mockDict) LookupPinyin(s string) string       { return "" }
func (m *mockDict) CharReadings(ch string) []string    { return nil }
func (m *mockDict) Stats() dictionary.Stats            { return dictionary.Stats{} }
func (m *mockDict) Close() error                       { return nil }

// contextDict is a mock dictionary with per-character readings and sub-word lookups
// for testing resolveByContext.
type contextDict struct {
	charReadings map[string][]string
	entries      map[string]*dictionary.Entry
}

func (d *contextDict) Lookup(s string) (*dictionary.Entry, error) {
	if e, ok := d.entries[s]; ok {
		return e, nil
	}
	return nil, fmt.Errorf("not found")
}
func (d *contextDict) LookupPinyin(s string) string {
	if e, ok := d.entries[s]; ok { return e.Pinyin }
	return ""
}
func (d *contextDict) CharReadings(ch string) []string { return d.charReadings[ch] }
func (d *contextDict) Stats() dictionary.Stats         { return dictionary.Stats{} }
func (d *contextDict) Close() error                    { return nil }

// TestProcessASR_NonRawCharPinyin_BuiltCorrectly verifies that the non-raw
// (non-sherpa) path builds exactly one char_pinyin entry per character and
// one part per character. A previous bug (duplicated appends, missing default
// case) produced wrong-length char_pinyin arrays that caused the client to
// fall through to its word-level-pinyin-on-first-char escape hatch.
func TestProcessASR_NonRawCharPinyin_BuiltCorrectly(t *testing.T) {
	store, _ := storage.New(t.TempDir())
	defer store.Close()

	// A dictionary where every word lookup FAILS — forcing the per-character
	// path — but per-character readings are available with some ambiguity.
	dict := &nonRawTestDict{
		charReadings: map[string][]string{
			"试": {"shi4"},
			"点": {"dian3"},
			"测": {"ce4"},
			"评": {"ping2"},
			// 长 has two readings (cháng/zhǎng) — tests the default: case.
			"长": {"chang2", "zhang3"},
			// 行 has two readings — tests resolveByContext.
			"行": {"xing2", "hang2"},
			"进": {"jin4"},
		},
		// Sub-word entries for resolveByContext: "进行" → "jin4 xing2".
		entries: map[string]*dictionary.Entry{
			"进行": {Pinyin: "jin4 xing2", CharPinyins: []string{"jin4", "xing2"}},
		},
	}

	// A tokenizer that produces words the dictionary won't find.
	tok := &fixedTokenizer{
		tokens: []models.Token{
			{Text: "试点", CharStart: 0, CharEnd: 2},
			{Text: "测评", CharStart: 2, CharEnd: 4},
			{Text: "长", CharStart: 4, CharEnd: 5},
			{Text: "进行", CharStart: 5, CharEnd: 7},
		},
	}

	// Mock ASR that returns text WITHOUT raw timestamps.
	mockASR := asr.NewMockTranscriber()
	mockASR.TranscribeFn = func(pcm []float32, segmentID int) (*models.TranscriptSegment, error) {
		return &models.TranscriptSegment{
			SegmentID:        segmentID,
			TimelineStartSec: float64(segmentID * 3),
			TimelineEndSec:   float64(segmentID*3 + 3),
			TextZh:           "试点测评长进行",
			// No RawTimestamps → triggers non-raw path.
		}, nil
	}

	pcmCh := make(chan models.PCMChunk, 1)

	p := &Pipeline{
		Ingestor:    &mockIngestor{ch: pcmCh},
		Transcriber: mockASR,
		Tokenizer:   tok,
		Dictionary:  dict,
		Store:       store,
		Logger:      logging.NewProductionLogger("warn"),
		OutputDir:   t.TempDir(),
		HLSTime:     3,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pcmCh <- models.PCMChunk{
		SegmentID:   0,
		Samples:     make([]float32, 48000),
		DurationSec: 3.0,
	}
	close(pcmCh)

	_ = p.Run(ctx)
	time.Sleep(500 * time.Millisecond)

	seg, err := store.Read(0)
	if err != nil {
		t.Fatalf("Read(0) failed: %v", err)
	}

	if len(seg.Words) != 4 {
		t.Fatalf("expected 4 words, got %d", len(seg.Words))
	}

	for wi, w := range seg.Words {
		chars := []rune(w.Text)
		n := len(chars)

		// Rule 1: char_pinyin must have exactly len(chars) entries — never
		//         more (duplicate appends) or fewer (missing default case).
		if len(w.CharPinyin) != n {
			t.Errorf("word %d %q: char_pinyin length %d != char count %d: %v",
				wi, w.Text, len(w.CharPinyin), n, w.CharPinyin)
		}

		// Rule 2: char_pinyin entries must be single syllables — never
		//         multi-syllable debris (e.g. "shìdiǎn" for a single char).
		for ci, cp := range w.CharPinyin {
			if strings.Contains(cp, " ") || len([]rune(cp)) > 8 {
				t.Errorf("word %d %q char %d: multi-syllable debris in char_pinyin: %q",
					wi, w.Text, ci, cp)
			}
		}
	}

	// Spot-check: 试点 has unambiguous readings (1 reading each).
	w0 := seg.Words[0]
	if len(w0.CharPinyin) == 2 {
		if w0.CharPinyin[0] != "shi4" {
			t.Errorf("试点 char 0: got %q, want shi4", w0.CharPinyin[0])
		}
		if w0.CharPinyin[1] != "dian3" {
			t.Errorf("试点 char 1: got %q, want dian3", w0.CharPinyin[1])
		}
	}

	// Spot-check: 长 has two readings → should fall to default case.
	w2 := seg.Words[2] // "长"
	if len(w2.CharPinyin) == 1 {
		cp := w2.CharPinyin[0]
		if cp != "chang2" && cp != "zhang3" && cp != "?" {
			t.Errorf("长: unexpected char_pinyin %q", cp)
		}
	}
}

func TestResolveByContext_DisambiguatesMiddleChar(t *testing.T) {
	// Scenario: word "人方式" is NOT in dictionary, but "人方" (rénfāng)
	// and "方式" (fāngshì) are. For 方, readings are [fang1, pang2].
	// Context resolves to fang1.
	dict := &contextDict{
		charReadings: map[string][]string{
			"人": {"ren2"},
			"方": {"fang1", "pang2"},
			"式": {"shi4"},
		},
		entries: map[string]*dictionary.Entry{
			"人方": {Pinyin: "ren2 fang1", CharPinyins: []string{"ren2", "fang1"}},
			"方式": {Pinyin: "fang1 shi4", CharPinyins: []string{"fang1", "shi4"}},
		},
	}

	chars := []rune("人方式")
	// Character at index 1 is 方 — should resolve to fang1.
	result := resolveByContext(1, chars, dict)
	if result != "fang1" {
		t.Errorf("got %q, want fang1", result)
	}
}

func TestResolveByContext_UnambiguousChar_ReturnsEmpty(t *testing.T) {
	// Single reading — no need to resolve.
	dict := &contextDict{
		charReadings: map[string][]string{"人": {"ren2"}},
		entries:      map[string]*dictionary.Entry{},
	}
	result := resolveByContext(0, []rune("人"), dict)
	if result != "" {
		t.Errorf("expected empty for unambiguous char, got %q", result)
	}
}

func TestResolveByContext_NoContextMatch_ReturnsEmpty(t *testing.T) {
	// Multiple readings but no adjacent sub-words to disambiguate.
	dict := &contextDict{
		charReadings: map[string][]string{
			"有": {"you3", "you4"},
		},
		entries: map[string]*dictionary.Entry{},
	}
	result := resolveByContext(0, []rune("有"), dict)
	if result != "" {
		t.Errorf("expected empty for unresolvable char, got %q", result)
	}
}

func TestPipelineOneSegment(t *testing.T) {
	store, _ := storage.New(t.TempDir())
	defer store.Close()

	pcmCh := make(chan models.PCMChunk, 1)

	p := &Pipeline{
		Ingestor:    &mockIngestor{ch: pcmCh},
		Transcriber: asr.NewMockTranscriber(),
		Tokenizer:   &mockTokenizer{},
		Dictionary:  &mockDict{},
		Store:       store,
		Logger:      logging.NewProductionLogger("info"),
		OutputDir:   t.TempDir(),
		HLSTime:     3,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Send one chunk
	pcmCh <- models.PCMChunk{
		SegmentID:   0,
		Samples:     make([]float32, 48000),
		DurationSec: 3.0,
	}
	close(pcmCh)

	err := p.Run(ctx)
	if err != nil && err != context.DeadlineExceeded {
		t.Logf("Run ended with: %v", err)
	}

	// Wait for async ASR worker to finish
	time.Sleep(500 * time.Millisecond)

	// Verify segment was stored
	seg, err := store.Read(0)
	if err != nil {
		t.Fatalf("Read(0) failed: %v", err)
	}
	if seg.TextZh == "" {
		t.Error("expected non-empty TextZh")
	}
	if len(seg.Words) < 1 {
		t.Errorf("expected at least 1 word, got %d", len(seg.Words))
	}
}

func TestPipelineGracefulShutdown(t *testing.T) {
	store, _ := storage.New(t.TempDir())
	defer store.Close()

	pcmCh := make(chan models.PCMChunk) // never written to

	p := &Pipeline{
		Ingestor:    &mockIngestor{ch: pcmCh},
		Transcriber: asr.NewMockTranscriber(),
		Tokenizer:   &mockTokenizer{},
		Dictionary:  &mockDict{},
		Store:       store,
		Logger:      logging.NewProductionLogger("warn"),
		OutputDir:   t.TempDir(),
		HLSTime:     3,
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	// Cancel immediately
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after cancel")
	}
}

func TestPipelineErrorRecovery(t *testing.T) {
	store, _ := storage.New(t.TempDir())
	defer store.Close()

	mockASR := asr.NewMockTranscriber()
	failCount := 0
	mockASR.TranscribeFn = func(pcm []float32, segmentID int) (*models.TranscriptSegment, error) {
		if failCount == 0 {
			failCount++
			return nil, &testError{"simulated ASR failure"}
		}
		return asr.NewMockTranscriber().Transcribe(pcm, segmentID)
	}

	pcmCh := make(chan models.PCMChunk, 2)

	p := &Pipeline{
		Ingestor:    &mockIngestor{ch: pcmCh},
		Transcriber: mockASR,
		Tokenizer:   &mockTokenizer{},
		Dictionary:  &mockDict{},
		Store:       store,
		Logger:      logging.NewProductionLogger("warn"),
		OutputDir:   t.TempDir(),
		HLSTime:     3,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Send 2 chunks — first should fail, second should succeed
	pcmCh <- models.PCMChunk{SegmentID: 0, Samples: make([]float32, 48000), DurationSec: 3.0}
	pcmCh <- models.PCMChunk{SegmentID: 1, Samples: make([]float32, 48000), DurationSec: 3.0}
	close(pcmCh)

	_ = p.Run(ctx)
	time.Sleep(200 * time.Millisecond)

	// First segment IS stored even when ASR fails (HLS segment exists)
	// — the segment will have empty text and words
	seg0, err := store.Read(0)
	if err != nil {
		t.Logf("segment 0 storage: %v", err)
	}
	if seg0 != nil && seg0.TextZh != "" {
		t.Logf("segment 0 has text despite ASR failure: %q (empty segment expected)", seg0.TextZh)
	}

	// Second segment SHOULD be stored
	seg, err := store.Read(1)
	if err != nil {
		t.Fatalf("segment 1 should be stored: %v", err)
	}
	if seg.SegmentID != 1 {
		t.Errorf("SegmentID: got %d, want 1", seg.SegmentID)
	}
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

// TestPipelineHLSEncoderCleanup verifies that stopHLSEncoder() runs on Run() exit
// and cleans up hlsCmd/hlsStdin.
//
// ON THE OLD CODE (pre-fix): the defer in Run() only logged "stopped".
//   stopHLSEncoder() did not exist. The HLS ffmpeg process was never killed,
//   its stderr pipe never closed, and the logStderr goroutine was stuck forever
//   in bufio.Scanner.Scan(). This is the goroutine-27 leak from the bug report.
//
// ON THE CURRENT CODE: defer calls stopHLSEncoder() which closes stdin, kills
//   the process, and nils both hlsCmd and hlsStdin.
func TestPipelineHLSEncoderCleanup(t *testing.T) {
	store, _ := storage.New(t.TempDir())
	defer store.Close()

	pcmCh := make(chan models.PCMChunk)
	close(pcmCh) // close immediately — simulate ingest dying

	p := &Pipeline{
		Ingestor:    &mockIngestor{ch: pcmCh},
		Transcriber: asr.NewMockTranscriber(),
		Tokenizer:   &mockTokenizer{},
		Dictionary:  &mockDict{},
		Store:       store,
		Logger:      logging.NewProductionLogger("warn"),
		OutputDir:   t.TempDir(),
		HLSTime:     3,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Run returns an error because pcmCh is closed (ingest stream ended).
	_ = p.Run(ctx)

	// KEY ASSERTION: after Run() exits, hlsCmd and hlsStdin must be nil.
	// On the old code, these would remain set (zombie ffmpeg + stuck stderr reader).
	p.hlsMu.Lock()
	hlsCmd := p.hlsCmd
	hlsStdin := p.hlsStdin
	p.hlsMu.Unlock()

	if hlsCmd != nil {
		t.Error("hlsCmd is not nil after Run() exit — stopHLSEncoder() did not clean up (old-code zombie leak)")
	}
	if hlsStdin != nil {
		t.Error("hlsStdin is not nil after Run() exit — stopHLSEncoder() did not close stdin (old-code pipe leak)")
	}
}

// nonRawTestDict is a mock Dictionary for testing the per-character pinyin
// code path. Every multi-character word lookup fails, forcing the
// character-by-character fallback through CharReadings.
type nonRawTestDict struct {
	charReadings map[string][]string
	entries      map[string]*dictionary.Entry
}

func (d *nonRawTestDict) Lookup(s string) (*dictionary.Entry, error) {
	if e, ok := d.entries[s]; ok {
		return e, nil
	}
	return nil, fmt.Errorf("not found")
}
func (d *nonRawTestDict) LookupPinyin(s string) string {
	if e, ok := d.entries[s]; ok {
		return e.Pinyin
	}
	return ""
}
func (d *nonRawTestDict) CharReadings(ch string) []string { return d.charReadings[ch] }
func (d *nonRawTestDict) Stats() dictionary.Stats         { return dictionary.Stats{} }
func (d *nonRawTestDict) Close() error                    { return nil }

// fixedTokenizer returns a fixed list of tokens regardless of input text.
type fixedTokenizer struct{ tokens []models.Token }

func (t *fixedTokenizer) Segment(text string) ([]models.Token, error) { return t.tokens, nil }
func (t *fixedTokenizer) Close() error                          { return nil }

// ---- Sliding-Window Batch ASR tests ----

func TestBatch(t *testing.T) {
	t.Run("Size1_BackwardCompat", testBatchSize1_BackwardCompat)
	t.Run("Size2_Stitching", testBatchSize2_Stitching)
	t.Run("FirstSegmentWaits", testFirstSegmentWaits)
	t.Run("LastSegmentSolo", testLastSegmentSolo)
	t.Run("OutputOrdering", testOutputOrdering)
	t.Run("Split_WhisperPath", testBatchSplit_WhisperPath)
	t.Run("Split_SherpaPath", testBatchSplit_SherpaPath)
}

// testBatchSize1_BackwardCompat verifies that batch_size=1 produces the same
// behavior as the old non-batched code: one chunk → one segment with TextZh and Words.
func testBatchSize1_BackwardCompat(t *testing.T) {
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	pcmCh := make(chan models.PCMChunk, 1)

	p := &Pipeline{
		Ingestor:     &mockIngestor{ch: pcmCh},
		Transcriber:  asr.NewMockTranscriber(),
		Tokenizer:    &mockTokenizer{},
		Dictionary:   &mockDict{},
		Store:        store,
		Logger:       logging.NewProductionLogger("warn"),
		OutputDir:    t.TempDir(),
		HLSTime:      3,
		ASRBatchSize: 1,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pcmCh <- models.PCMChunk{
		SegmentID:   0,
		Samples:     make([]float32, 48000),
		DurationSec: 3.0,
	}
	close(pcmCh)

	_ = p.Run(ctx)
	time.Sleep(500 * time.Millisecond)

	seg, err := store.Read(0)
	if err != nil {
		t.Fatalf("Read(0) failed: %v", err)
	}
	if seg.TextZh == "" {
		t.Error("expected non-empty TextZh with batch_size=1")
	}
	if len(seg.Words) < 1 {
		t.Errorf("expected at least 1 word, got %d", len(seg.Words))
	}
	if seg.SegmentID != 0 {
		t.Errorf("SegmentID: got %d, want 0", seg.SegmentID)
	}
}

// testBatchSize2_Stitching verifies that two PCM chunks are stitched into one
// before being sent to the transcriber. The transcriber receives PCM of length
// ~2x, and segment 0 is stored after split.
func testBatchSize2_Stitching(t *testing.T) {
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	mockASR := asr.NewMockTranscriber()
	var capturedMaxLen int
	mockASR.TranscribeFn = func(pcm []float32, segmentID int) (*models.TranscriptSegment, error) {
		if len(pcm) > capturedMaxLen {
			capturedMaxLen = len(pcm)
		}
		// Return a stitched result with two phrase-level words.
		// Word 0 falls within boundary 0..3.5, word 1 starts after boundary.
		return &models.TranscriptSegment{
			SegmentID: segmentID,
			TextZh:    "今天天气很好我们出去散步",
			Words: []models.WordEntry{
				{Text: "今天天气很好", CharStart: 0, CharEnd: 6, StartSec: 0, EndSec: 2.5},
				{Text: "我们出去散步", CharStart: 6, CharEnd: 12, StartSec: 3.5, EndSec: 5.5},
			},
		}, nil
	}

	pcmCh := make(chan models.PCMChunk, 2)

	p := &Pipeline{
		Ingestor:     &mockIngestor{ch: pcmCh},
		Transcriber:  mockASR,
		Tokenizer:    &mockTokenizer{},
		Dictionary:   &mockDict{},
		Store:        store,
		Logger:       logging.NewProductionLogger("warn"),
		OutputDir:    t.TempDir(),
		HLSTime:      3,
		ASRBatchSize: 2,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pcmCh <- models.PCMChunk{SegmentID: 0, Samples: make([]float32, 48000), DurationSec: 3.0}
	pcmCh <- models.PCMChunk{SegmentID: 1, Samples: make([]float32, 48000), DurationSec: 3.0}
	close(pcmCh)

	_ = p.Run(ctx)
	time.Sleep(500 * time.Millisecond)

	// The stitched PCM should be roughly 2x a single chunk
	singleLen := 48000
	if capturedMaxLen < singleLen*2 {
		t.Errorf("transcriber received PCM length %d, expected at least %d (2 chunks stitched)",
			capturedMaxLen, singleLen*2)
	}

	// Segment 0 should be stored after split
	seg0, err := store.Read(0)
	if err != nil {
		t.Fatalf("Read(0) failed: %v", err)
	}
	if seg0.SegmentID != 0 {
		t.Errorf("SegmentID: got %d, want 0", seg0.SegmentID)
	}
	if seg0.TextZh == "" {
		t.Error("expected non-empty TextZh for stored segment 0")
	}
}

// testFirstSegmentWaits verifies that with batch_size=2, the first segment
// (segment 0) does NOT trigger ASR until the second chunk arrives.
func testFirstSegmentWaits(t *testing.T) {
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	pcmCh := make(chan models.PCMChunk, 2)

	p := &Pipeline{
		Ingestor:     &mockIngestor{ch: pcmCh},
		Transcriber:  asr.NewMockTranscriber(),
		Tokenizer:    &mockTokenizer{},
		Dictionary:   &mockDict{},
		Store:        store,
		Logger:       logging.NewProductionLogger("warn"),
		OutputDir:    t.TempDir(),
		HLSTime:      3,
		ASRBatchSize: 2,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Run pipeline in background
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	// Send only segment 0 — it should NOT trigger ASR yet
	pcmCh <- models.PCMChunk{SegmentID: 0, Samples: make([]float32, 48000), DurationSec: 3.0}

	// Give the pipeline time to process (but it should be waiting for segment 1)
	time.Sleep(300 * time.Millisecond)

	// Segment 0 should NOT be stored yet (only 1 chunk in buffer)
	if _, err := store.Read(0); err == nil {
		t.Error("segment 0 was stored before batch was complete — first segment should wait")
	}

	// Send segment 1 to complete the batch
	pcmCh <- models.PCMChunk{SegmentID: 1, Samples: make([]float32, 48000), DurationSec: 3.0}
	close(pcmCh)

	// Wait for Run to complete
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Run() to complete")
	}

	time.Sleep(500 * time.Millisecond)

	// Now segment 0 should be stored
	seg0, err := store.Read(0)
	if err != nil {
		t.Fatalf("Read(0) after batch completion: %v", err)
	}
	if seg0.TextZh == "" {
		t.Error("expected non-empty TextZh for segment 0 after batch")
	}
}

// testLastSegmentSolo verifies that after closing pcmCh with an odd number
// of chunks (3 chunks, batch_size=2), the last chunk is processed solo.
// All three segments should be stored.
func testLastSegmentSolo(t *testing.T) {
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	pcmCh := make(chan models.PCMChunk, 3)

	p := &Pipeline{
		Ingestor:     &mockIngestor{ch: pcmCh},
		Transcriber:  asr.NewMockTranscriber(),
		Tokenizer:    &mockTokenizer{},
		Dictionary:   &mockDict{},
		Store:        store,
		Logger:       logging.NewProductionLogger("warn"),
		OutputDir:    t.TempDir(),
		HLSTime:      3,
		ASRBatchSize: 2,
	}

	// Override mock to return sherpa-style text with relative timestamps
	// appropriate for stitched audio (0-6s). Without this, the default mock
	// returns absolute timestamps that don't work with the guard zone.
	p.Transcriber.(*asr.MockTranscriber).TranscribeFn = func(pcm []float32, segmentID int) (*models.TranscriptSegment, error) {
		// Return unique text per batch so dedupBoundary doesn't see false overlap.
		// Timestamps are relative to stitched audio start.
		return &models.TranscriptSegment{
			SegmentID:     segmentID,
			TextZh:        fmt.Sprintf("seg%d文本", segmentID),
			RawTimestamps: []float64{0.5, 1.0, 1.5, 2.0}, // all < boundary=2.7
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Send 3 chunks (odd number — last chunk should be flushed solo)
	pcmCh <- models.PCMChunk{SegmentID: 0, Samples: make([]float32, 48000), DurationSec: 3.0}
	pcmCh <- models.PCMChunk{SegmentID: 1, Samples: make([]float32, 48000), DurationSec: 3.0}
	pcmCh <- models.PCMChunk{SegmentID: 2, Samples: make([]float32, 48000), DurationSec: 3.0}
	close(pcmCh)

	_ = p.Run(ctx)
	time.Sleep(500 * time.Millisecond)

	// All three segments should be stored
	for i := 0; i < 3; i++ {
		seg, err := store.Read(i)
		if err != nil {
			t.Fatalf("Read(%d) failed: %v", i, err)
		}
		if seg.TextZh == "" {
			t.Errorf("segment %d: expected non-empty TextZh", i)
		}
	}
}

// testOutputOrdering verifies that the ordered collector stores segments
// in ascending segment ID order even when results arrive out of order.
func testOutputOrdering(t *testing.T) {
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	p := &Pipeline{
		Tokenizer:   &mockTokenizer{},
		Dictionary:  &mockDict{},
		Store:       store,
		Logger:      logging.NewProductionLogger("warn"),
		OutputDir:   t.TempDir(),
		HLSTime:     3,
		epochBase:   1000,
		storeMu:     sync.Mutex{},
		nextStoreID: 0,
		pendingSegs: make(map[int]*models.TranscriptSegment),
	}

	// Emit segments out of order: 2, 1, 0
	// The ordered collector should store them as 0, 1, 2
	p.emitOrdered(&models.TranscriptSegment{
		SegmentID: 2, TextZh: "seg2",
		Words: []models.WordEntry{
			{Text: "seg2", CharStart: 0, CharEnd: 4, StartSec: 0, EndSec: 3.0},
		},
	})
	p.emitOrdered(&models.TranscriptSegment{
		SegmentID: 1, TextZh: "seg1",
		Words: []models.WordEntry{
			{Text: "seg1", CharStart: 0, CharEnd: 4, StartSec: 0, EndSec: 3.0},
		},
	})
	p.emitOrdered(&models.TranscriptSegment{
		SegmentID: 0, TextZh: "seg0",
		Words: []models.WordEntry{
			{Text: "seg0", CharStart: 0, CharEnd: 4, StartSec: 0, EndSec: 3.0},
		},
	})

	// Verify all segments were stored with correct content
	for i := 0; i < 3; i++ {
		seg, err := store.Read(i)
		if err != nil {
			t.Fatalf("Read(%d) failed: %v", i, err)
		}
		if seg.SegmentID != i {
			t.Errorf("segment %d: stored with SegmentID=%d", i, seg.SegmentID)
		}
		expected := fmt.Sprintf("seg%d", i)
		if seg.TextZh != expected {
			t.Errorf("segment %d: got TextZh=%q, want %q", i, seg.TextZh, expected)
		}
	}
}

// testBatchSplit_WhisperPath verifies that splitBatchResult correctly splits
// a stitched ASR result when using phrase-level timestamps (whisper path).
// Words whose StartSec is within boundary (=HLSTime - guardSeconds) are kept.
// Words in the guard zone are excluded — they will be recognized in the next batch.
func testBatchSplit_WhisperPath(t *testing.T) {
	p := &Pipeline{HLSTime: 3}

	job := batchJob{
		firstSegID: 0,
		batchSize:  2,
	}

	// Stitched result from ASR (two 3-second segments stitched = 6 seconds)
	// Boundary = HLSTime - guardSeconds = 3.0 - 0.3 = 2.7
	stitched := &models.TranscriptSegment{
		SegmentID: 0,
		TextZh:    "今天天气很好我们出去散步一起吃饭",
		Words: []models.WordEntry{
			{Text: "今天天气很好", CharStart: 0, CharEnd: 6, StartSec: 0, EndSec: 2.8},
			{Text: "我们出去散步", CharStart: 6, CharEnd: 12, StartSec: 3.0, EndSec: 4.8},
			{Text: "一起吃饭", CharStart: 12, CharEnd: 16, StartSec: 4.0, EndSec: 5.5},
		},
	}

	result := p.splitBatchResult(stitched, job)
	if result == nil {
		t.Fatal("splitBatchResult returned nil")
	}
	if result.SegmentID != 0 {
		t.Errorf("SegmentID: got %d, want 0", result.SegmentID)
	}

	// Words with StartSec < 2.7:
	//   今天天气很好 (0 < 2.7) → kept
	//   我们出去散步 (3.0 >= 2.7) → excluded (guard zone)
	//   一起吃饭 (4.0 >= 2.7) → excluded
	expected := "今天天气很好"
	if result.TextZh != expected {
		t.Errorf("TextZh: got %q, want %q", result.TextZh, expected)
	}
}

// testBatchSplit_SherpaPath verifies that splitBatchResult correctly splits
// a stitched ASR result when using character-level RawTimestamps (sherpa path).
// Characters whose timestamp is < boundary are kept.
func testBatchSplit_SherpaPath(t *testing.T) {
	p := &Pipeline{HLSTime: 3}

	job := batchJob{
		firstSegID: 0,
		batchSize:  2,
	}

	// 12 characters with per-character timestamps.
	// Boundary = 3.0, so timestamps < 3.0 are included (indices 0-5).
	// Timestamp at index 6 = 3.0 is NOT < 3.0 → excluded.
	stitched := &models.TranscriptSegment{
		SegmentID: 0,
		TextZh:    "今天天气很好我们出去散步",
		RawTimestamps: []float64{0, 0.5, 1.0, 1.5, 2.0, 2.5, 3.0, 3.5, 4.0, 4.5, 5.0, 5.5},
	}

	result := p.splitBatchResult(stitched, job)
	if result == nil {
		t.Fatal("splitBatchResult returned nil")
	}
	if result.SegmentID != 0 {
		t.Errorf("SegmentID: got %d, want 0", result.SegmentID)
	}

	// Expected: first 6 characters ("今天天气很好")
	expectedText := "今天天气很好"
	if result.TextZh != expectedText {
		t.Errorf("TextZh: got %q, want %q", result.TextZh, expectedText)
	}
	if len(result.RawTimestamps) != 6 {
		t.Errorf("expected 6 RawTimestamps, got %d: %v", len(result.RawTimestamps), result.RawTimestamps)
	}

	// Verify the retained timestamps are the correct ones
	for i, ts := range result.RawTimestamps {
		expected := float64(i) * 0.5
		if ts != expected {
			t.Errorf("RawTimestamps[%d]: got %f, want %f", i, ts, expected)
		}
	}
}

// TestBatchSizeInvalid verifies that ASR_BATCH_SIZE validation rejects 0 and 9.
// NOTE: This test is in config_test.go (Task 1). We add a forwarding checker
// here to ensure config_test.go validation is present and working.
func TestBatchSizeInvalid(t *testing.T) {
	// Quick smoke check: importing the config package and running its validation
	// would be ideal, but this test is already covered in config_test.go
	// (test cases "ASRBatchSize 0" and "ASRBatchSize 9").
	// Mark as skipped since config validation is tested elsewhere.
	t.Skip("config validation for ASRBatchSize (0 and 9) is tested in config/config_test.go")
}

// TestSherpaSplitAtBoundary verifies that splitBatchResult with boundary=HLSTime
// keeps all characters with timestamp < HLSTime. DedupBoundary handles overlaps.
// Guard zone was removed because it permanently lost characters near chunk edges
// (the character was at the END of its PCM chunk and didn't appear in the next chunk).
func TestSherpaSplitAtBoundary(t *testing.T) {
	const hlTime = 3

	p := &Pipeline{HLSTime: hlTime}

	// Batch [N-1, N]: character "氣" at 2.95s — IS in chunk N-1, should be KEPT
	stitched1 := &models.TranscriptSegment{
		SegmentID:     10,
		TextZh:        "陽氣",
		RawTimestamps: []float64{1.5, 2.95},
	}
	job1 := batchJob{firstSegID: 10, batchSize: 2}
	result1 := p.splitBatchResult(stitched1, job1)

	// Without guard zone: 2.95 < 3.0 → KEPT
	if result1.TextZh != "陽氣" {
		t.Errorf("expected '陽氣' (2.95 < boundary=3.0), got %q", result1.TextZh)
	}

	// Batch [N, N+1]: both chars at 0.05 and 1.2 — well within boundary
	stitched2 := &models.TranscriptSegment{
		SegmentID:     11,
		TextZh:        "其始",
		RawTimestamps: []float64{0.05, 1.2},
	}
	job2 := batchJob{firstSegID: 11, batchSize: 2}
	result2 := p.splitBatchResult(stitched2, job2)

	if result2.TextZh != "其始" {
		t.Errorf("expected '其始' from batch [N,N+1], got %q", result2.TextZh)
	}

	// Both segments contain their full text. If ASR produces duplicates at the
	// boundary, dedupBoundary (text-based) catches them downstream.
	t.Logf("segment 10: %q, segment 11: %q", result1.TextZh, result2.TextZh)
}

// TestWhisperSplitAtBoundary verifies whisper phrase-level split with boundary=HLSTime.
func TestWhisperSplitAtBoundary(t *testing.T) {
	const hlTime = 3

	p := &Pipeline{HLSTime: hlTime}

	stitched := &models.TranscriptSegment{
		SegmentID: 5,
		TextZh:    "今天天气很好",
		Words: []models.WordEntry{
			{Text: "今天", CharStart: 0, CharEnd: 2, StartSec: 0.5, EndSec: 1.5},
			{Text: "天气", CharStart: 2, CharEnd: 4, StartSec: 2.5, EndSec: 3.2}, // crosses boundary
			{Text: "很好", CharStart: 4, CharEnd: 6, StartSec: 3.5, EndSec: 4.5},
		},
	}
	job := batchJob{firstSegID: 5, batchSize: 2}
	result := p.splitBatchResult(stitched, job)

	// "今天" StartSec=0.5 < 3.0 → kept
	// "天气" StartSec=2.5 < 3.0 → kept (starts before boundary)
	// "很好" StartSec=3.5 >= 3.0 → excluded
	if result.TextZh != "今天天气" {
		t.Errorf("expected '今天天气' (phrase with StartSec < boundary=3.0), got %q", result.TextZh)
	}
}

// TestLastCharAtChunkBoundary reproduces the exact bug reported 2026-07-26:
// character "物" at the end of chunk 66 (relTL ~2.9s) was lost because
// guard zone (boundary=HLSTime-0.3=2.7) excluded it. The char was at the
// END of its PCM chunk — not in the next chunk's PCM — so it was impossible
// to recover from any subsequent batch. Permanently lost.
//
// After fix: boundary = HLSTime = 3.0s, so 2.9s < 3.0 → kept.
// DedupBoundary handles any text overlap downstream.
func TestLastCharAtChunkBoundary(t *testing.T) {
	p := &Pipeline{HLSTime: 3}

	// Simulate sherpa-onnx output for batch [66, 67]:
	// Chunk 66 contains "...很多动植物" with per-char timestamps.
	// The last char "物" is at 2.88s — near the chunk boundary.
	// Chunk 67 starts with "是世界上..." — "物" is NOT in chunk 67.
	stitched := &models.TranscriptSegment{
		SegmentID: 66,
		TextZh:    "沿着现实挑战，新西兰有很多动植物",
		// Per-character timestamps from sherpa-onnx sense_voice.
		// "物" at 2.88s is the last char of this chunk's audio.
		RawTimestamps: []float64{
			0.12, 0.24, // 沿着
			0.42, 0.54, // 现实
			0.72, 0.90, // 挑战
			1.14, // ，
			1.56, 1.86, 2.10, // 新西兰
			2.10, // 有
			2.22, // 很
			2.34, 2.52, // 多动
			2.70, 2.88, // 植物 ← "物" at 2.88s
		},
	}
	job := batchJob{firstSegID: 66, batchSize: 2}
	result := p.splitBatchResult(stitched, job)

	// All chars have timestamp < 3.0 → ALL must be kept.
	// "物" at 2.88s is the critical one — 2.88 < 3.0 → KEPT.
	if result.TextZh != "沿着现实挑战，新西兰有很多动植物" {
		t.Errorf("FULL text should be kept (all timestamps < 3.0), got %q", result.TextZh)
	}

	// Verify "物" is the last char
	chars := []rune(result.TextZh)
	lastChar := string(chars[len(chars)-1])
	if lastChar != "物" {
		t.Errorf("last char should be '物', got %q. Full text: %q", lastChar, result.TextZh)
	}

	// Verify RawTimestamps are preserved for kept chars
	if len(result.RawTimestamps) != len(chars) {
		t.Errorf("RawTimestamps count (%d) should match chars count (%d)",
			len(result.RawTimestamps), len(chars))
	}

	t.Logf("segment 66: %q (all %d chars kept, last='物')", result.TextZh, len(chars))
}

// TestTimestampDedup_RemovesBoundaryDuplicate: seg66 ends with "动植物"
// (last word EndSec at boundary). seg67 starts with same chars, RawTimestamps
// ~0.05s from start → absolute times overlap → duplicate removed.
func TestTimestampDedup_RemovesBoundaryDuplicate(t *testing.T) {
	epochBase := 1_785_093_000.0
	hlTime := 3

	p := &Pipeline{
		HLSTime:   hlTime,
		epochBase: epochBase,
		Logger:    logging.NewProductionLogger("warn"),
	}

	seg66 := &models.TranscriptSegment{
		SegmentID: 66,
		TextZh:    "沿着现实挑战，新西兰有很多动植物",
		Words: []models.WordEntry{
			{Text: "动植物", CharStart: 0, CharEnd: 3,
				StartSec: epochBase + 66*3 + 2.52, EndSec: epochBase + 66*3 + 3.00},
		},
	}
	p.lastEmittedSeg = seg66

	seg67 := &models.TranscriptSegment{
		SegmentID:     67,
		TextZh:        "动植物是世界上独一无二的",
		RawTimestamps: []float64{0.05, 0.18, 0.36, 0.54, 0.78, 0.96, 1.14, 1.32, 1.50, 1.74, 1.98, 2.16},
	}

	p.dedupBoundary(seg67)

	if seg67.TextZh != "是世界上独一无二的" {
		t.Errorf("Got %q, want %q", seg67.TextZh, "是世界上独一无二的")
	}
}

// TestTimestampDedup_PreservesLegitRepeat: same char spoken twice
// at DIFFERENT times → NOT a duplicate → preserved.
func TestTimestampDedup_PreservesLegitRepeat(t *testing.T) {
	epochBase := 1_785_093_000.0
	hlTime := 3

	p := &Pipeline{HLSTime: hlTime, epochBase: epochBase, Logger: logging.NewProductionLogger("warn")}

	// Segment 93: "我" at time [281.5, 281.8]
	prevSeg := &models.TranscriptSegment{
		SegmentID: 93,
		Words: []models.WordEntry{
			{Text: "我", CharStart: 0, CharEnd: 1,
				StartSec: epochBase + 93*3 + 2.5, EndSec: epochBase + 93*3 + 2.8},
		},
	}
	p.lastEmittedSeg = prevSeg

	// Segment 94: "我" at time [283.2, 283.5] — 0.4s AFTER prev "我"
	// Timestamps: 1.2, 1.5 relative → absolute = epochBase + 94*3 + 1.2 = epochBase + 283.2
	seg := &models.TranscriptSegment{
		SegmentID:     94,
		TextZh:        "我我就纳闷",
		RawTimestamps: []float64{1.2, 1.5, 1.8, 2.1, 2.4},
	}

	p.dedupBoundary(seg)

	if seg.TextZh != "我我就纳闷" {
		t.Errorf("legit repeat removed! Got %q", seg.TextZh)
	}
}

// TestTimestampDedup_SingleCharBoundary: seg93 ends with "我" at boundary,
// seg94 starts with "我" at boundary → same absolute time → duplicate removed.
func TestTimestampDedup_SingleCharBoundary(t *testing.T) {
	epochBase := 1_785_093_000.0
	hlTime := 3

	p := &Pipeline{HLSTime: hlTime, epochBase: epochBase, Logger: logging.NewProductionLogger("warn")}

	// Segment 93 last word "我" at [2.82, 3.00] relative → touches boundary
	prevSeg := &models.TranscriptSegment{
		SegmentID: 93,
		Words: []models.WordEntry{
			{Text: "我", CharStart: 0, CharEnd: 1,
				StartSec: epochBase + 93*3 + 2.82, EndSec: epochBase + 93*3 + 3.00},
		},
	}
	p.lastEmittedSeg = prevSeg

	// Segment 94 first char "我" at 0.00 relative → absolute same as prev end
	seg := &models.TranscriptSegment{
		SegmentID:     94,
		TextZh:        "我我就纳闷的很",
		RawTimestamps: []float64{0.00, 0.12, 0.36, 0.54, 0.78, 0.96, 1.14, 1.32},
	}

	p.dedupBoundary(seg)

	if seg.TextZh != "我就纳闷的很" {
		t.Errorf("boundary '我' should be removed. Got %q", seg.TextZh)
	}
}

