package storage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/criradio/server/internal/models"
)

func setupTempStore(t *testing.T) MetadataStore {
	t.Helper()
	dir := t.TempDir()
	store, err := New(dir)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	return store
}

func TestWriteRead(t *testing.T) {
	store := setupTempStore(t)
	defer store.Close()

	seg := &models.TranscriptSegment{
		SegmentID:        1,
		TimelineStartSec: 100.0,
		TimelineEndSec:   103.0,
		TSFile:           "000000001.ts",
		TextZh:           "你好世界",
		Words: []models.WordEntry{
			{Text: "你好", CharStart: 0, CharEnd: 2, StartSec: 100.0, EndSec: 100.8, Pinyin: "nǐhǎo", Trans: "hello"},
		},
	}

	if err := store.Write(seg); err != nil {
		t.Fatalf("Write() failed: %v", err)
	}

	read, err := store.Read(1)
	if err != nil {
		t.Fatalf("Read() failed: %v", err)
	}

	if read.SegmentID != seg.SegmentID {
		t.Errorf("SegmentID: got %d, want %d", read.SegmentID, seg.SegmentID)
	}
	if read.TextZh != seg.TextZh {
		t.Errorf("TextZh: got %q, want %q", read.TextZh, seg.TextZh)
	}
	if len(read.Words) != 1 || read.Words[0].Pinyin != "nǐhǎo" {
		t.Errorf("Words mismatch: %+v", read.Words)
	}
}

func TestReadRange(t *testing.T) {
	store := setupTempStore(t)
	defer store.Close()

	// Write 5 segments: 0-3, 3-6, 6-9, 9-12, 12-15
	for i := 0; i < 5; i++ {
		seg := &models.TranscriptSegment{
			SegmentID:        i,
			TimelineStartSec: float64(i * 3),
			TimelineEndSec:   float64(i*3 + 3),
			TSFile:           segmentFileName(i)[:len(segmentFileName(i))-5] + ".ts",
			TextZh:           "test",
		}
		if err := store.Write(seg); err != nil {
			t.Fatalf("Write(%d) failed: %v", i, err)
		}
	}

	// Read range 3.0 to 9.0 — should get segments at 3-6 and 6-9
	segs, err := store.ReadRange(3.0, 9.0)
	if err != nil {
		t.Fatalf("ReadRange() failed: %v", err)
	}
	if len(segs) != 2 {
		t.Errorf("ReadRange: got %d segments, want 2", len(segs))
		for _, s := range segs {
			t.Logf("  segment %d: [%f, %f]", s.SegmentID, s.TimelineStartSec, s.TimelineEndSec)
		}
	}
}

func TestReadRangeEmpty(t *testing.T) {
	store := setupTempStore(t)
	defer store.Close()

	segs, err := store.ReadRange(999.0, 1000.0)
	if err != nil {
		t.Fatalf("ReadRange() failed: %v", err)
	}
	if len(segs) != 0 {
		t.Errorf("expected 0 segments, got %d", len(segs))
	}
}

func TestIndexIntegrity(t *testing.T) {
	store := setupTempStore(t)
	defer store.Close()

	for i := 0; i < 5; i++ {
		seg := &models.TranscriptSegment{
			SegmentID:        i,
			TimelineStartSec: float64(i * 3),
			TimelineEndSec:   float64(i*3 + 3),
			TextZh:           "test",
		}
		if err := store.Write(seg); err != nil {
			t.Fatalf("Write(%d) failed: %v", i, err)
		}
	}

	idx, err := store.ReadIndex()
	if err != nil {
		t.Fatalf("ReadIndex() failed: %v", err)
	}
	if len(idx.Segments) != 5 {
		t.Errorf("index has %d segments, want 5", len(idx.Segments))
	}

	// Verify sorted by ID
	for i := 1; i < len(idx.Segments); i++ {
		if idx.Segments[i-1].ID >= idx.Segments[i].ID {
			t.Errorf("index not sorted: seg[%d].ID=%d, seg[%d].ID=%d",
				i-1, idx.Segments[i-1].ID, i, idx.Segments[i].ID)
		}
	}
}

func TestCleanup(t *testing.T) {
	store := setupTempStore(t)
	defer store.Close()

	// Write a segment (mod time = now)
	seg := &models.TranscriptSegment{
		SegmentID: 1, TimelineStartSec: 0.0, TimelineEndSec: 3.0,
		TextZh: "test",
	}
	if err := store.Write(seg); err != nil {
		t.Fatalf("Write() failed: %v", err)
	}

	// Cleanup with 0 TTL — should delete everything
	deleted, err := store.Cleanup(0)
	if err != nil {
		t.Errorf("Cleanup(0) failed: %v", err)
	}
	if deleted != 1 {
		t.Errorf("Cleanup(0): deleted %d, want 1", deleted)
	}

	// After cleanup, segment should be gone
	_, err = store.Read(1)
	if !os.IsNotExist(err) {
		t.Errorf("Read after cleanup: expected IsNotExist, got %v", err)
	}
}

func TestWatch(t *testing.T) {
	store := setupTempStore(t)
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := store.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch() failed: %v", err)
	}

	// Write a segment
	go func() {
		seg := &models.TranscriptSegment{
			SegmentID: 1, TimelineStartSec: 0.0, TimelineEndSec: 3.0,
			TextZh: "test", TSFile: "001.ts",
		}
		store.Write(seg)
	}()

	select {
	case ref := <-ch:
		if ref.ID != 1 {
			t.Errorf("watcher got ID=%d, want 1", ref.ID)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for Watch event")
	}
}

func TestConcurrentWrite(t *testing.T) {
	store := setupTempStore(t)
	defer store.Close()

	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			seg := &models.TranscriptSegment{
				SegmentID:        id,
				TimelineStartSec: float64(id * 3),
				TimelineEndSec:   float64(id*3 + 3),
				TextZh:           "test",
			}
			store.Write(seg)
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	idx, err := store.ReadIndex()
	if err != nil {
		t.Fatalf("ReadIndex() failed: %v", err)
	}
	if len(idx.Segments) != 10 {
		t.Errorf("concurrent writes: index has %d segments, want 10", len(idx.Segments))
	}
}

func TestReadNonexistent(t *testing.T) {
	store := setupTempStore(t)
	defer store.Close()

	_, err := store.Read(99999)
	if !os.IsNotExist(err) {
		t.Errorf("expected IsNotExist, got %v", err)
	}
}

func TestStats(t *testing.T) {
	store := setupTempStore(t)
	defer store.Close()

	for i := 0; i < 3; i++ {
		store.Write(&models.TranscriptSegment{
			SegmentID: i, TimelineStartSec: float64(i * 3), TimelineEndSec: float64(i*3 + 3),
			TextZh: "test",
		})
	}

	stats := store.Stats()
	if stats.TotalFiles != 3 {
		t.Errorf("TotalFiles: got %d, want 3", stats.TotalFiles)
	}
	if stats.OldestID != 0 || stats.NewestID != 2 {
		t.Errorf("IDs: oldest=%d, newest=%d, want oldest=0, newest=2", stats.OldestID, stats.NewestID)
	}
}

func TestParseSegmentID(t *testing.T) {
	tests := []struct {
		name     string
		expected int
	}{
		{"000000001.json", 1},
		{"000000042.json", 42},
		{"123456789.json", 123456789},
		{"abc", -1},
		{"nosuffix", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseSegmentID(tt.name); got != tt.expected {
				t.Errorf("parseSegmentID(%q) = %d, want %d", tt.name, got, tt.expected)
			}
		})
	}
}
