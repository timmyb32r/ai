package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/criradio/server/internal/models"
	"github.com/criradio/server/internal/storage"
)

func setup(t *testing.T, delay time.Duration) (*Server, http.Handler, float64) {
	t.Helper()
	dir := t.TempDir()
	store, e := storage.Open(filepath.Join(dir, "data"), storage.Options{MaxBytes: 64 << 20, Delay: delay})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	start := float64(time.Now().Unix() - 100)
	src := filepath.Join(dir, "fixture.ts")
	if e = os.WriteFile(src, []byte("transport-stream"), 0600); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		id, e := store.NextID(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		seg := &models.TranscriptSegment{SegmentID: id, TSFile: fmt.Sprintf("%09d.ts", id), TimelineStartSec: start + float64(i*3), TimelineEndSec: start + float64(i*3) + 3, TextZh: "你好", Words: []models.WordEntry{{Text: "你好", CharStart: 0, CharEnd: 2, StartSec: start + float64(i*3), EndSec: start + float64(i*3) + 2, CharPinyin: []string{"nǐ", "hǎo"}, Trans: "hello", Senses: []models.WordSense{{Text: "hello"}}}}}
		if e = store.Publish(context.Background(), seg, src); e != nil {
			t.Fatal(e)
		}
	}
	s := &Server{Store: store, Status: func() any {
		return models.ServerStatus{Status: "running", AsrEngine: "sherpa-onnx", AsrModel: "sense-voice-2024"}
	}}
	return s, s.Handler(), start
}
func request(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}
func rangePath(start, end float64, extra string) string {
	return fmt.Sprintf("/api/segments/range?start_sec=%.3f&end_sec=%.3f%s", start, end, extra)
}
func TestExistingProtocolAndLiteFields(t *testing.T) {
	_, h, start := setup(t, 0)
	for _, path := range []string{"/api/status", "/api/metadata/000000000.json", "/api/metadata/index.json", "/api/segments/batch?last=2", fmt.Sprintf("/api/segments/at?sec=%.3f", start+1), "/hls/playlist.m3u8", "/hls/000000000.ts", "/health/live", "/health/ready"} {
		rec := request(h, "GET", path)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("missing CORS: %s", path)
		}
	}
	rec := request(h, "GET", "/api/segments/batch?last=1&lite=true")
	var result struct{ Segments []models.TranscriptSegment }
	if e := json.Unmarshal(rec.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	word := result.Segments[0].Words[0]
	if len(word.CharPinyin) != 2 || word.Trans != "" || word.Senses != nil {
		t.Fatalf("lite contract: %+v", word)
	}
	rec = request(h, "GET", "/api/segments/at?sec=1")
	if !strings.Contains(rec.Body.String(), "\"found\":false") || !strings.Contains(rec.Body.String(), "\"segment\":null") {
		t.Fatal(rec.Body.String())
	}
}
func TestRangeOnlyPinsOnExplicitRequest(t *testing.T) {
	s, h, start := setup(t, 0)
	for i := 0; i < 10; i++ {
		rec := request(h, "GET", rangePath(start, start+9, "&limit=1&offset=1"))
		if rec.Code != 200 {
			t.Fatal(rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "snapshot_id") {
			t.Fatalf("implicit snapshot created: %s", rec.Body.String())
		}
	}
	stats, e := s.Store.Stats(context.Background())
	if e != nil || stats.Snapshots != 0 {
		t.Fatalf("live polling pins: %+v %v", stats, e)
	}
	rec := request(h, "GET", rangePath(start, start+9, "&limit=1&snapshot=create"))
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var p storage.Page
	if e = json.Unmarshal(rec.Body.Bytes(), &p); e != nil {
		t.Fatal(e)
	}
	if len(p.SnapshotID) != 48 || p.Total != 3 || p.Count != 1 {
		t.Fatalf("bad snapshot: %+v", p)
	}
	if _, e = time.Parse(time.RFC3339, p.ExpiresAt); e != nil {
		t.Fatal(e)
	}
	rec = request(h, "GET", rangePath(start, start+9, "&limit=1&offset=1&snapshot="+p.SnapshotID))
	var p2 storage.Page
	if e = json.Unmarshal(rec.Body.Bytes(), &p2); e != nil {
		t.Fatal(e)
	}
	if rec.Code != 200 || p2.Total != 3 || p2.Segments[0].SegmentID != 1 {
		t.Fatalf("page changed: %d %+v", rec.Code, p2)
	}
	rec = request(h, "GET", "/hls/000000001.ts?snapshot="+p.SnapshotID)
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	rec = request(h, "DELETE", "/api/snapshots/"+p.SnapshotID)
	if rec.Code != 204 {
		t.Fatal(rec.Code)
	}
	rec = request(h, "GET", "/hls/000000001.ts?snapshot="+p.SnapshotID)
	if rec.Code != 410 {
		t.Fatalf("released snapshot accepted: %d", rec.Code)
	}
	rec = request(h, "GET", rangePath(start, start+9, "&snapshot="+p.SnapshotID))
	if rec.Code != 410 {
		t.Fatalf("released page accepted: %d", rec.Code)
	}
}
func TestInputLimitsTraversalAndMethods(t *testing.T) {
	_, h, start := setup(t, 0)
	cases := []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/segments/at?sec=NaN", 400},
		{"GET", "/api/segments/at?sec=Inf", 400},
		{"GET", "/api/segments/at", 400},
		{"GET", rangePath(start, start+9, "&limit=1001"), 400},
		{"GET", rangePath(start, start+9, "&offset=-1"), 400},
		{"GET", rangePath(start, start+9, "&limit=0"), 400},
		{"GET", rangePath(start, start+24*3600, ""), 400},
		{"GET", rangePath(start, start+9, "&snapshot=bogus"), 410},
		{"GET", "/api/segments/batch?last=99999999999999999", 400},

		{"GET", "/hls/archive.db", 404},
		{"GET", "/hls/000000000.ts/extra", 404},

		{"POST", "/api/segments/batch", 405},
		{"HEAD", "/api/subtitles", 405},
		{"GET", "/api/snapshots/unknown", 405},
	}
	for _, c := range cases {
		r := request(h, c.method, c.path)
		if r.Code != c.want {
			t.Errorf("%s %s: got %d want %d (%s)", c.method, c.path, r.Code, c.want, r.Body.String())
		}
	}
}
func TestHealthShowsDependencyState(t *testing.T) {
	s, h, _ := setup(t, 0)
	s.Status = func() any { return map[string]any{"status": "asr_waiting"} }
	if r := request(h, "GET", "/health/live"); r.Code != 200 {
		t.Fatal(r.Code)
	}
	if r := request(h, "GET", "/health/ready"); r.Code != 503 {
		t.Fatal(r.Code)
	}
	r := request(h, "GET", "/api/status")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "asr_waiting") {
		t.Fatal(r.Code, r.Body.String())
	}
}
func TestHLSDelayAndAudioRangeRequests(t *testing.T) {
	_, h, _ := setup(t, 180*time.Second)
	r := request(h, "GET", "/hls/playlist.m3u8")
	if r.Code != 200 || strings.Contains(r.Body.String(), ".ts") {
		t.Fatalf("early audio advertised: %s", r.Body.String())
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/hls/000000000.ts", nil)
	req.Header.Set("Range", "bytes=0-2")
	h.ServeHTTP(rec, req)
	if rec.Code != 206 || rec.Body.String() != "tra" {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Range") == "" {
		t.Fatal("missing byte range header")
	}
}
func TestSSEStreamsHistoryAndCapsClients(t *testing.T) {
	s, h, _ := setup(t, 0)
	var cancels []context.CancelFunc
	var done []chan struct{}
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		defer cancel()
		w := &streamWriter{header: http.Header{}, ready: make(chan struct{})}
		ended := make(chan struct{})
		done = append(done, ended)
		go func() {
			defer close(ended)
			h.ServeHTTP(w, httptest.NewRequest("GET", "/api/subtitles", nil).WithContext(ctx))
		}()
		select {
		case <-w.ready:
		case <-time.After(time.Second):
			t.Fatal("SSE did not flush sync")
		}
	}
	sixth := request(h, "GET", "/api/subtitles")
	if sixth.Code != 429 {
		t.Fatalf("unbounded SSE clients: %d", sixth.Code)
	}
	for _, cancel := range cancels {
		cancel()
	}
	for _, ended := range done {
		select {
		case <-ended:
		case <-time.After(time.Second):
			t.Fatal("SSE did not stop on cancellation")
		}
	}
	if n := s.clients.Load(); n != 0 {
		t.Fatalf("SSE clients leaked: %d", n)
	}
}

type streamWriter struct {
	header http.Header
	once   sync.Once
	ready  chan struct{}
}

func (w *streamWriter) Header() http.Header         { return w.header }
func (w *streamWriter) WriteHeader(int)             {}
func (w *streamWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *streamWriter) Flush()                      { w.once.Do(func() { close(w.ready) }) }

func TestSnapshotMissingMemberAndCapacity(t *testing.T) {
	_, h, start := setup(t, 0)
	for i := 0; i < 5; i++ {
		r := request(h, "GET", rangePath(start, start+3, "&snapshot=create"))
		if r.Code != 200 {
			t.Fatalf("create: %d %s", r.Code, r.Body.String())
		}
		if i == 0 {
			var p storage.Page
			_ = json.Unmarshal(r.Body.Bytes(), &p)
			r = request(h, "GET", "/hls/000000002.ts?snapshot="+url.QueryEscape(p.SnapshotID))
			if r.Code != 404 {
				t.Fatalf("outside snapshot: %d", r.Code)
			}
		}
	}
	r := request(h, "GET", rangePath(start, start+3, "&snapshot=create"))
	if r.Code != 429 {
		t.Fatalf("unbounded pins: %d", r.Code)
	}
}
func TestSSEWriteFailureReturns(t *testing.T) {
	s, h, _ := setup(t, 0)
	req := httptest.NewRequest("GET", "/api/subtitles", nil)
	h.ServeHTTP(&failedWriter{header: http.Header{}}, req)
	if n := s.clients.Load(); n != 0 {
		t.Fatalf("failed SSE remained: %d", n)
	}
}

type failedWriter struct{ header http.Header }

func (w *failedWriter) Header() http.Header       { return w.header }
func (w *failedWriter) WriteHeader(int)           {}
func (w *failedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (w *failedWriter) Flush()                    {}

func TestPlaylistDeclaresActualDurationAndStableSequence(t *testing.T) {
	s, h, start := setup(t, 0)
	src := filepath.Join(t.TempDir(), "audio")
	_ = os.WriteFile(src, []byte("transport-stream"), 0600)
	// Reserve an unused ID. Playlist media sequence must not follow identity gaps.
	_, _ = s.Store.NextID(context.Background())
	id, _ := s.Store.NextID(context.Background())
	seg := &models.TranscriptSegment{SegmentID: id, TSFile: fmt.Sprintf("%09d.ts", id), TimelineStartSec: start + 15, TimelineEndSec: start + 18.024}
	if e := s.Store.Publish(context.Background(), seg, src); e != nil {
		t.Fatal(e)
	}
	r := request(h, "GET", "/hls/playlist.m3u8")
	body := r.Body.String()
	if !strings.Contains(body, "#EXTINF:3.024000") || !strings.Contains(body, "#EXT-X-DISCONTINUITY\n") || !strings.Contains(body, "#EXT-X-TARGETDURATION:4\n") {
		t.Fatal(body)
	}
	if !strings.Contains(body, fmt.Sprintf("%09d.ts", id)) {
		t.Fatal("missing reserved-gap segment " + strconv.Itoa(id))
	}
}

// Slow metadata consumers hold their response memory until Write finishes.
// Exercise real handlers through that condition; audio and health must remain
// usable, and rejected callers must be able to retry after capacity returns.
func TestSlowMetadataReadersDoNotStarveAudioOrHealth(t *testing.T) {
	_, h, _ := setup(t, 0)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var done []chan struct{}
	for i := 0; i < 8; i++ {
		writer := &blockedBodyWriter{header: http.Header{}, entered: make(chan struct{}), release: release}
		ended := make(chan struct{})
		done = append(done, ended)
		go func() {
			defer close(ended)
			h.ServeHTTP(writer, httptest.NewRequest("GET", "/api/segments/batch?last=3", nil))
		}()
		select {
		case <-writer.entered:
		case <-time.After(time.Second):
			t.Fatal("metadata response did not begin")
		}
	}
	denied := request(h, "GET", "/api/metadata/000000000.json")
	if denied.Code != http.StatusTooManyRequests || denied.Header().Get("Retry-After") == "" {
		t.Fatalf("metadata pressure was not bounded: %d %s", denied.Code, denied.Body.String())
	}
	for _, path := range []string{"/hls/000000000.ts", "/hls/playlist.m3u8", "/health/live", "/health/ready", "/api/status"} {
		reply := request(h, "GET", path)
		if reply.Code != http.StatusOK {
			t.Fatalf("metadata readers starved %s: %d %s", path, reply.Code, reply.Body.String())
		}
	}
	unblock()
	for _, ended := range done {
		select {
		case <-ended:
		case <-time.After(time.Second):
			t.Fatal("metadata handler remained after client resumed")
		}
	}
	if retry := request(h, "GET", "/api/segments/batch?last=3"); retry.Code != http.StatusOK {
		t.Fatalf("capacity did not return: %d", retry.Code)
	}
}

type blockedBodyWriter struct {
	header  http.Header
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (w *blockedBodyWriter) Header() http.Header { return w.header }
func (w *blockedBodyWriter) WriteHeader(int)     {}
func (w *blockedBodyWriter) Write(b []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(b), nil
}
