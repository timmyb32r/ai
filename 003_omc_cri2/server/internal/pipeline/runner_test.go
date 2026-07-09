package pipeline

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/criradio/server/internal/asr"
	"github.com/criradio/server/internal/dictionary"
	"github.com/criradio/server/internal/ingest"
	"github.com/criradio/server/internal/logging"
	"github.com/criradio/server/internal/models"
	"github.com/criradio/server/internal/storage"
	"github.com/criradio/server/internal/tokenizer"
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

func (m *mockTokenizer) Segment(text string) []tokenizer.Token {
	return []tokenizer.Token{
		{Text: "你好", CharStart: 0, CharEnd: 2},
		{Text: "世界", CharStart: 2, CharEnd: 4},
	}
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
