package api_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	ctrl   chan any
	status broadcast.Status
	// cancelCalled is incremented each time the cancel func returned by
	// Subscribe is invoked, letting tests assert ref-count release.
	cancelCalled atomic.Int64
}

func newFake(status broadcast.Status) *fakeBroadcaster {
	return &fakeBroadcaster{
		audio:  make(chan []byte, 8),
		subs:   make(chan broadcast.SubtitleEvent, 8),
		ctrl:   make(chan any, 1),
		status: status,
	}
}

func (f *fakeBroadcaster) Subscribe() (<-chan []byte, <-chan broadcast.SubtitleEvent, <-chan any, func()) {
	cancel := func() { f.cancelCalled.Add(1) }
	return f.audio, f.subs, f.ctrl, cancel
}

func (f *fakeBroadcaster) SubscribeOpus() (<-chan []byte, <-chan broadcast.SubtitleEvent, <-chan any, func()) {
	cancel := func() { f.cancelCalled.Add(1) }
	return f.audio, f.subs, f.ctrl, cancel
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

// TestAudioStream verifies GET /v1/stream/audio returns 200,
// audio/L16;rate=16000;channels=1 Content-Type and streams the PCM data.
func TestAudioStream(t *testing.T) {
	fake := newFake(broadcast.Status{State: "running"})
	mux := api.NewMux(fake)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	chunk1 := []byte("PCMDATA1")
	chunk2 := []byte("PCMDATA2")
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
	if ct != "audio/L16;rate=16000;channels=1" {
		t.Fatalf("expected audio/L16;rate=16000;channels=1, got %q", ct)
	}

	buf := make([]byte, len(chunk1)+len(chunk2))
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(buf) != string(chunk1)+string(chunk2) {
		t.Fatalf("body mismatch: got %q want %q", buf, string(chunk1)+string(chunk2))
	}

	resp.Body.Close()
	time.Sleep(50 * time.Millisecond)

	if n := fake.cancelCalled.Load(); n < 1 {
		t.Fatalf("expected cancel to be called at least once, got %d", n)
	}
}

// TestAudioStreamHeaders verifies X-Audio-Timeline-Start and X-Audio-Bitrate
// are present when a SyncEvent is received.
func TestAudioStreamHeaders(t *testing.T) {
	fake := newFake(broadcast.Status{State: "running"})
	mux := api.NewMux(fake)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	fake.ctrl <- broadcast.SyncEvent{AudioTimelineStart: 50.0}
	fake.audio <- []byte("PCMDUMMY")

	resp, err := http.Get(srv.URL + "/v1/stream/audio")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "audio/L16;rate=16000;channels=1" {
		t.Fatalf("expected audio/L16;rate=16000;channels=1, got %q", ct)
	}
	if ts := resp.Header.Get("X-Audio-Timeline-Start"); ts != "50" {
		t.Fatalf("expected X-Audio-Timeline-Start: 50, got %q", ts)
	}
	// PCM bitrate is 256 kbps (32000 B/s * 8 / 1000).
	if br := resp.Header.Get("X-Audio-Bitrate"); br != "256" {
		t.Fatalf("expected X-Audio-Bitrate: 256, got %q", br)
	}

	close(fake.audio)
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
// and emits `event: sync` followed by `event: subtitle` frames for events pushed
// on the fake channels. It also asserts that Last-Event-ID is ignored (client
// still receives live events).
func TestSubtitleStream(t *testing.T) {
	fake := newFake(broadcast.Status{State: "running"})
	mux := api.NewMux(fake)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	wantSync := broadcast.SyncEvent{AudioTimelineStart: 100.0}
	wantSub := broadcast.SubtitleEvent{Start: 1.0, End: 3.5, TextZh: "你好世界"}

	// Push the events before the client connects so they're buffered.
	fake.ctrl <- wantSync
	fake.subs <- wantSub

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

	// Read lines; expect event:sync + data, then event:subtitle + data.
	scanner := bufio.NewScanner(resp.Body)
	var gotSync broadcast.SyncEvent
	var gotSub broadcast.SubtitleEvent
	syncFound, subFound := false, false
	currentEvent := ""
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			currentEvent = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			payload := strings.TrimPrefix(line, "data: ")
			switch currentEvent {
			case "sync":
				if err := json.Unmarshal([]byte(payload), &gotSync); err != nil {
					t.Fatalf("unmarshal sync: %v", err)
				}
				syncFound = true
			case "subtitle":
				if err := json.Unmarshal([]byte(payload), &gotSub); err != nil {
					t.Fatalf("unmarshal subtitle: %v", err)
				}
				subFound = true
			}
		}
		if syncFound && subFound {
			break
		}
	}
	if !syncFound {
		t.Fatal("no event:sync received")
	}
	if !subFound {
		t.Fatal("no event:subtitle received")
	}
	if gotSync != wantSync {
		t.Fatalf("sync mismatch: got %+v want %+v", gotSync, wantSync)
	}
	if !reflect.DeepEqual(gotSub, wantSub) {
		t.Fatalf("subtitle mismatch: got %+v want %+v", gotSub, wantSub)
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

// TestSubtitleChannelClose verifies that when both the sync and subs channels
// are closed the handler ends cleanly.
func TestSubtitleChannelClose(t *testing.T) {
	fake := newFake(broadcast.Status{State: "stopped"})
	mux := api.NewMux(fake)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	close(fake.ctrl)
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
