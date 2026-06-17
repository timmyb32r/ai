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
