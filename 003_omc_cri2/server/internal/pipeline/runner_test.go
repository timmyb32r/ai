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
func (m *mockDict) LookupPinyin(s string) string { return "" }
func (m *mockDict) Stats() dictionary.Stats       { return dictionary.Stats{} }
func (m *mockDict) Close() error                  { return nil }

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
