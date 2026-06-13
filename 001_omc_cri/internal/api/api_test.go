package api_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/timmyb32r/001_omc_cri/internal/api"
	"github.com/timmyb32r/001_omc_cri/internal/broadcast"
)

// fakeBroadcaster is a controllable test double for the Broadcaster interface.
type fakeBroadcaster struct {
	audio  chan []byte
	subs   chan broadcast.SubtitleEvent
	status broadcast.Status
	// cancelCalled is incremented each time the cancel func returned by
	// Subscribe is invoked, letting tests assert ref-count release.
	cancelCalled atomic.Int64
}

func newFake(status broadcast.Status) *fakeBroadcaster {
	return &fakeBroadcaster{
		audio:  make(chan []byte, 8),
		subs:   make(chan broadcast.SubtitleEvent, 8),
		status: status,
	}
}

func (f *fakeBroadcaster) Subscribe() (<-chan []byte, <-chan broadcast.SubtitleEvent, func()) {
	cancel := func() { f.cancelCalled.Add(1) }
	return f.audio, f.subs, cancel
}

func (f *fakeBroadcaster) Status() broadcast.Status { return f.status }

// TestStatus verifies GET /v1/status returns 200, application/json, and the
// expected fields.
func TestStatus(t *testing.T) {
	want := broadcast.Status{
		Channel:               "cri-cn",
		Listeners:             3,
		DelaySeconds:          12.5,
		State:                 "running",
		LiveEdgeOffsetSeconds: 2.1,
	}
	fake := newFake(want)
	mux := api.NewMux(fake)

	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Fatalf("expected application/json content-type, got %q", ct)
	}

	var got broadcast.Status
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != want {
		t.Fatalf("status mismatch: got %+v want %+v", got, want)
	}
}

// TestStatusMethodNotAllowed verifies POST /v1/status returns 405.
func TestStatusMethodNotAllowed(t *testing.T) {
	fake := newFake(broadcast.Status{})
	mux := api.NewMux(fake)

	req := httptest.NewRequest(http.MethodPost, "/v1/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// TestAudioStream verifies GET /v1/stream/audio returns 200, audio/mpeg
// Content-Type and streams the bytes pushed on the fake audio channel.
func TestAudioStream(t *testing.T) {
	fake := newFake(broadcast.Status{State: "running"})
	mux := api.NewMux(fake)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	chunk1 := []byte("MP3FRAME1")
	chunk2 := []byte("MP3FRAME2")
	fake.audio <- chunk1
	fake.audio <- chunk2

	resp, err := http.Get(srv.URL + "/v1/stream/audio")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "audio/mpeg" {
		t.Fatalf("expected audio/mpeg, got %q", ct)
	}

	// Read exactly len(chunk1)+len(chunk2) bytes then close the connection.
	buf := make([]byte, len(chunk1)+len(chunk2))
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(buf) != string(chunk1)+string(chunk2) {
		t.Fatalf("body mismatch: got %q want %q", buf, string(chunk1)+string(chunk2))
	}

	// Close the connection to trigger client disconnect / cancel.
	resp.Body.Close()

	// Allow handler goroutine time to notice disconnect and call cancel.
	time.Sleep(50 * time.Millisecond)

	if n := fake.cancelCalled.Load(); n < 1 {
		t.Fatalf("expected cancel to be called at least once, got %d", n)
	}
}

// TestAudioStreamMethodNotAllowed verifies POST /v1/stream/audio returns 405.
func TestAudioStreamMethodNotAllowed(t *testing.T) {
	fake := newFake(broadcast.Status{})
	mux := api.NewMux(fake)

	req := httptest.NewRequest(http.MethodPost, "/v1/stream/audio", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// TestSubtitleStream verifies GET /v1/stream/subtitles returns text/event-stream
// and emits `data: {json}` frames for events pushed on the fake subs channel.
// It also asserts that Last-Event-ID is ignored (client still receives live events).
func TestSubtitleStream(t *testing.T) {
	fake := newFake(broadcast.Status{State: "running"})
	mux := api.NewMux(fake)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	want := broadcast.SubtitleEvent{Start: 1.0, End: 3.5, TextZh: "你好世界"}

	// Push the event before the client connects so it's buffered.
	fake.subs <- want

	// Send Last-Event-ID header — handler must ignore it and still deliver live events.
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/stream/subtitles", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Last-Event-ID", "42")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}

	// Read lines until we find a `data:` line then decode the JSON.
	scanner := bufio.NewScanner(resp.Body)
	var gotEvent broadcast.SubtitleEvent
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			if err := json.Unmarshal([]byte(payload), &gotEvent); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no data: line received")
	}
	if gotEvent != want {
		t.Fatalf("event mismatch: got %+v want %+v", gotEvent, want)
	}

	// Close connection; cancel should be called.
	resp.Body.Close()
	time.Sleep(50 * time.Millisecond)

	if n := fake.cancelCalled.Load(); n < 1 {
		t.Fatalf("expected cancel to be called at least once, got %d", n)
	}
}

// TestSubtitleStreamMethodNotAllowed verifies POST /v1/stream/subtitles returns 405.
func TestSubtitleStreamMethodNotAllowed(t *testing.T) {
	fake := newFake(broadcast.Status{})
	mux := api.NewMux(fake)

	req := httptest.NewRequest(http.MethodPost, "/v1/stream/subtitles", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// TestAudioChannelClose verifies that when the audio channel closes the handler
// ends cleanly (no panic, response body closes).
func TestAudioChannelClose(t *testing.T) {
	fake := newFake(broadcast.Status{State: "stopped"})
	mux := api.NewMux(fake)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Close the channel immediately so the handler exits as soon as it starts.
	close(fake.audio)

	resp, err := http.Get(srv.URL + "/v1/stream/audio")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	// Body should be readable to EOF without error.
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("read: %v", err)
	}
}

// TestSubtitleChannelClose verifies that when the subs channel closes the
// handler ends cleanly.
func TestSubtitleChannelClose(t *testing.T) {
	fake := newFake(broadcast.Status{State: "stopped"})
	mux := api.NewMux(fake)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	close(fake.subs)

	resp, err := http.Get(srv.URL + "/v1/stream/subtitles")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("read: %v", err)
	}
}
