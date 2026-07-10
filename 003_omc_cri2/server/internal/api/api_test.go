package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/criradio/server/internal/logging"
	"github.com/criradio/server/internal/models"
	"github.com/criradio/server/internal/storage"
)

func setupTestServer(t *testing.T) (*Server, string) {
	t.Helper()

	dir := t.TempDir()
	hlsDir := filepath.Join(dir, "hls")
	metaDir := filepath.Join(dir, "metadata")
	os.MkdirAll(hlsDir, 0o755)
	os.MkdirAll(metaDir, 0o755)

	store, err := storage.New(dir)
	if err != nil {
		t.Fatalf("storage.New failed: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// Create a test playlist file
	os.WriteFile(filepath.Join(hlsDir, "playlist.m3u8"), []byte("#EXTM3U\n"), 0o644)

	// Create test metadata
	store.Write(&models.TranscriptSegment{
		SegmentID: 1, TimelineStartSec: 0.0, TimelineEndSec: 3.0,
		TSFile: "000000001.ts", TextZh: "测试",
	})

	return &Server{
		Store:   store,
		Logger:  logging.NewProductionLogger("warn"),
		HLSDir:  hlsDir,
		MetaDir: metaDir,
	}, dir
}

func TestHLSPlaylist(t *testing.T) {
	srv, _ := setupTestServer(t)
	router := srv.NewRouter()

	req := httptest.NewRequest("GET", "/hls/playlist.m3u8", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestHLSPlaylistNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	router := srv.NewRouter()

	req := httptest.NewRequest("GET", "/hls/nonexistent.ts", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != 404 {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestMetadataJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	router := srv.NewRouter()

	req := httptest.NewRequest("GET", "/api/metadata/000000001.json", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var seg models.TranscriptSegment
	if err := json.NewDecoder(rec.Body).Decode(&seg); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if seg.SegmentID != 1 {
		t.Errorf("SegmentID: got %d, want 1", seg.SegmentID)
	}
}

func TestMetadataNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	router := srv.NewRouter()

	req := httptest.NewRequest("GET", "/api/metadata/99999.json", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != 404 {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestSSESync(t *testing.T) {
	srv, _ := setupTestServer(t)
	router := srv.NewRouter()

	req := httptest.NewRequest("GET", "/api/subtitles", nil)
	rec := httptest.NewRecorder()

	// Use a goroutine because SSE is long-lived
	done := make(chan struct{})
	go func() {
		router.ServeHTTP(rec, req)
		close(done)
	}()

	// Wait for some data then cancel
	time.Sleep(100 * time.Millisecond)
	rec.Result().Body.Close()

	body := rec.Body.String()
	if body == "" {
		t.Error("expected non-empty SSE body")
	}
	if !contains(body, "event: sync") {
		t.Error("expected sync event in SSE body")
	}
}

func TestStatus(t *testing.T) {
	srv, _ := setupTestServer(t)
	router := srv.NewRouter()

	req := httptest.NewRequest("GET", "/api/status", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}

	var status models.ServerStatus
	if err := json.NewDecoder(rec.Body).Decode(&status); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if status.Status != "running" {
		t.Errorf("Status: got %q, want running", status.Status)
	}
}

func TestHealthEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)
	router := srv.NewRouter()

	req := httptest.NewRequest("GET", "/api/status", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("health check failed: %d", rec.Code)
	}
}

// TestBatchSegmentsLitePreservesCharPinyin verifies that lite mode keeps
// per-character pinyin (char_pinyin, char_pinyin_uncertain). These fields
// are already computed and stored during ASR processing — stripping them
// saves negligible bandwidth but breaks per-character pinyin rendering
// because the client never re-fetches lite-loaded segments with full data.
//
// We test by writing segments directly to the store and exercising the
// lite-stripping logic via the batch-segments endpoint.
func TestBatchSegmentsLitePreservesCharPinyin(t *testing.T) {
	dir := t.TempDir()
	hlsDir := filepath.Join(dir, "hls")
	metaDir := filepath.Join(dir, "metadata")
	os.MkdirAll(hlsDir, 0o755)
	os.MkdirAll(metaDir, 0o755)

	store, err := storage.New(dir)
	if err != nil {
		t.Fatalf("storage.New failed: %v", err)
	}
	defer store.Close()
	os.WriteFile(filepath.Join(hlsDir, "playlist.m3u8"), []byte("#EXTM3U\n"), 0o644)

	srv := &Server{
		Store:   store,
		Logger:  logging.NewProductionLogger("warn"),
		HLSDir:  hlsDir,
		MetaDir: metaDir,
	}

	// Write segments (must write at least 2 to ensure ReadLatest finds them).
	for _, seg := range []models.TranscriptSegment{
		{
			SegmentID: 1, TimelineStartSec: 0.0, TimelineEndSec: 3.0,
			TSFile: "000000001.ts", TextZh: "试点",
			Words: []models.WordEntry{{
				Text: "试点", CharStart: 0, CharEnd: 2,
				StartSec: 0.0, EndSec: 3.0,
				Pinyin: "shìdiǎn", CharPinyin: []string{"shì", "diǎn"},
				CharPinyinUncertain: []bool{false, true},
				Trans: "pilot project",
				Senses: []models.WordSense{{Number: 1, Text: "test"}},
				CedictMeanings: []string{"pilot"},
			}},
		},
		{
			SegmentID: 2, TimelineStartSec: 3.0, TimelineEndSec: 6.0,
			TSFile: "000000002.ts", TextZh: "测试",
			Words: []models.WordEntry{{
				Text: "测试", CharStart: 0, CharEnd: 2,
				StartSec: 3.0, EndSec: 6.0,
				Pinyin: "cèshì", CharPinyin: []string{"cè", "shì"},
				CharPinyinUncertain: []bool{false, false},
				Trans: "test",
			}},
		},
	} {
		if err := store.Write(&seg); err != nil {
			t.Fatalf("Write seg %d failed: %v", seg.SegmentID, err)
		}
	}

	// Force index.json to disk so ReadLatest can find the segments.
	// (Write only flushes the index every 100 writes by default.)
	store.ForceFlush()

	router := srv.NewRouter()

	// Fetch with lite=true.
	req := httptest.NewRequest("GET", "/api/segments/batch?last=1&lite=true", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Segments []models.TranscriptSegment `json:"segments"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(body.Segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(body.Segments))
	}

	s := body.Segments[0]
	if len(s.Words) != 1 {
		t.Fatalf("expected 1 word, got %d", len(s.Words))
	}

	// Assert: char_pinyin MUST be preserved in lite mode.
	w0 := s.Words[0]
	if len(w0.CharPinyin) != 2 || w0.CharPinyin[0] != "cè" || w0.CharPinyin[1] != "shì" {
		t.Errorf("word 0 char_pinyin: got %v, want [cè shì]", w0.CharPinyin)
	}
	if len(w0.CharPinyinUncertain) != 2 || w0.CharPinyinUncertain[0] || w0.CharPinyinUncertain[1] {
		t.Errorf("word 0 char_pinyin_uncertain: got %v, want [false false]", w0.CharPinyinUncertain)
	}

	// Assert: heavy data IS stripped in lite mode.
	if w0.Trans != "" {
		t.Errorf("word 0 Trans: got %q, want empty (lite stripping)", w0.Trans)
	}
	if len(w0.Senses) != 0 {
		t.Errorf("word 0 Senses: got %d items, want 0 (lite stripping)", len(w0.Senses))
	}
	if len(w0.CedictMeanings) != 0 {
		t.Errorf("word 0 CedictMeanings: got %d items, want 0 (lite stripping)", len(w0.CedictMeanings))
	}

	// Assert: critical fields are preserved.
	if w0.Pinyin != "cèshì" {
		t.Errorf("word 0 Pinyin: got %q, want cèshì", w0.Pinyin)
	}
	if w0.StartSec != 3.0 || w0.EndSec != 6.0 {
		t.Errorf("word 0 timing: got [%.2f-%.2f], want [3.00-6.00]", w0.StartSec, w0.EndSec)
	}
}

func TestSegmentIDFromPath(t *testing.T) {
	tests := []struct {
		name     string
		expected int
	}{
		{"000000001.json", 1},
		{"000000042.json", 42},
		{"/api/metadata/000000001.json", 1},
		{"foo", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := segmentIDFromPath(tt.name); got != tt.expected {
				t.Errorf("segmentIDFromPath(%q) = %d, want %d", tt.name, got, tt.expected)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsBrute(s, substr))
}

func containsBrute(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
