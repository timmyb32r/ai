// Package api exposes the v1 HTTP surface: the chunked PCM audio stream, the
// SSE subtitle stream, and the JSON status endpoint. It depends on the
// Broadcaster interface (not the concrete broadcast.Broadcast) so it can be
// tested with a fake. Network I/O lives here and in internal/ingest / cmd; the
// chineseasr library stays offline.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/timmyb32r/001_omc_cri/internal/broadcast"
	"github.com/timmyb32r/001_omc_cri/internal/ingest"
)

// Broadcaster is the minimal broadcast surface the HTTP layer needs. Decoupling
// the API from the concrete *broadcast.Broadcast lets worker-api test the
// handlers with a fake implementation.
type Broadcaster interface {
	// Subscribe registers a PCM subscriber.
	Subscribe() (<-chan []byte, <-chan broadcast.SubtitleEvent, <-chan any, func())
	// SubscribeOpus registers an Opus subscriber whose audio channel carries
	// OGG/Opus (32 kbps libopus) instead of raw PCM.
	SubscribeOpus() (<-chan []byte, <-chan broadcast.SubtitleEvent, <-chan any, func())
	// Status returns the current status snapshot for GET /v1/status.
	Status() broadcast.Status
}

// NewMux builds the v1 HTTP mux backed by b. The three routes are:
//
//	GET /v1/stream/audio      — chunked PCM s16le 16kHz audio stream
//	GET /v1/stream/subtitles  — text/event-stream SSE of SubtitleEvent JSON
//	GET /v1/status            — JSON Status snapshot
func NewMux(b Broadcaster) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/stream/audio", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		audio, _, ctrlCh, cancel := b.Subscribe()
		defer cancel()

		contentType := "audio/L16;rate=16000;channels=1"
		bitrate := ingest.PcmBitrateKbps

		ctx := r.Context()

		// Phase 1: wait for the SyncEvent so we can communicate the
		// audio timeline start to the client via HTTP header.
		select {
		case ctrl, ok := <-ctrlCh:
			if !ok {
				return
			}
			switch ev := ctrl.(type) {
			case broadcast.SyncEvent:
				w.Header().Set("Content-Type", contentType)
				w.Header().Set("X-Audio-Timeline-Start",
					strconv.FormatFloat(ev.AudioTimelineStart, 'f', -1, 64))
				w.Header().Set("X-Audio-Bitrate", strconv.Itoa(bitrate))
			case broadcast.JumpEvent:
				http.Error(w, "jump requested", http.StatusServiceUnavailable)
				return
			}
		case <-time.After(5 * time.Second):
			// No sync event within timeout; proceed without the header.
			w.Header().Set("Content-Type", contentType)
		case <-ctx.Done():
			return
		}
		w.WriteHeader(http.StatusOK)

		// Phase 2: stream audio chunks, listening for JumpEvent.
		for {
			select {
			case <-ctx.Done():
				return
			case ctrl, ok := <-ctrlCh:
				if !ok {
					return
				}
				switch ctrl.(type) {
				case broadcast.JumpEvent:
					return
				case broadcast.SyncEvent:
					// Late sync — ignore.
				}
			case chunk, ok := <-audio:
				if !ok {
					return
				}
				_, err := w.Write(chunk)
				if err != nil {
					return
				}
				flusher.Flush()
			}
		}
	})

	mux.HandleFunc("/v1/stream/audio.opus", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		audio, _, ctrlCh, cancel := b.SubscribeOpus()
		defer cancel()

		contentType := "audio/ogg; codecs=opus"
		bitrate := 32

		ctx := r.Context()

		// Phase 1: wait for SyncEvent.
		select {
		case ctrl, ok := <-ctrlCh:
			if !ok {
				return
			}
			switch ev := ctrl.(type) {
			case broadcast.SyncEvent:
				w.Header().Set("Content-Type", contentType)
				w.Header().Set("X-Audio-Timeline-Start",
					strconv.FormatFloat(ev.AudioTimelineStart, 'f', -1, 64))
				w.Header().Set("X-Audio-Bitrate", strconv.Itoa(bitrate))
			case broadcast.JumpEvent:
				http.Error(w, "jump requested", http.StatusServiceUnavailable)
				return
			}
		case <-time.After(5 * time.Second):
			w.Header().Set("Content-Type", contentType)
		case <-ctx.Done():
			return
		}
		w.WriteHeader(http.StatusOK)

		// Phase 2: stream OGG/Opus chunks.
		for {
			select {
			case <-ctx.Done():
				return
			case ctrl, ok := <-ctrlCh:
				if !ok {
					return
				}
				switch ctrl.(type) {
				case broadcast.JumpEvent:
					return
				case broadcast.SyncEvent:
					// ignore late sync
				}
			case chunk, ok := <-audio:
				if !ok {
					return
				}
				if _, err := w.Write(chunk); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	})

	mux.HandleFunc("/v1/stream/subtitles", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		// Last-Event-ID is intentionally ignored: no replay, live-only.
		// PCM entries are always available (they feed the ASR pipeline).
		// The audio channel is discarded; only the control-channel SyncEvent
		// matters here.
		_, subs, ctrlCh, cancel := b.Subscribe()
		defer cancel()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		ctx := r.Context()

		// Phase 1: Wait for control event (sync or jump) or timeout.
		select {
		case ctrl, ok := <-ctrlCh:
			if !ok {
				return
			}
			switch ev := ctrl.(type) {
			case broadcast.SyncEvent:
				data, err := json.Marshal(ev)
				if err == nil {
					fmt.Fprintf(w, "event: sync\ndata: %s\n\n", data)
					flusher.Flush()
				}
			case broadcast.JumpEvent:
				data, err := json.Marshal(ev)
				if err == nil {
					fmt.Fprintf(w, "event: jump\ndata: %s\n\n", data)
					flusher.Flush()
				}
				return
			}
		case <-time.After(5 * time.Second):
			// No sync event within timeout; continue without one
			// (backward compatibility with non-sync servers).
		case <-ctx.Done():
			return
		}

		// Phase 2: Relay subtitle events and watch for jump.
		for {
			select {
			case <-ctx.Done():
				return
			case ctrl, ok := <-ctrlCh:
				if !ok {
					return
				}
				switch ev := ctrl.(type) {
				case broadcast.JumpEvent:
					data, err := json.Marshal(ev)
					if err == nil {
						fmt.Fprintf(w, "event: jump\ndata: %s\n\n", data)
						flusher.Flush()
					}
					return
				case broadcast.SyncEvent:
					// Late sync event; ignore (already handled in Phase 1).
				}
			case ev, ok := <-subs:
				if !ok {
					return
				}
				data, err := json.Marshal(ev)
				if err != nil {
					return
				}
				fmt.Fprintf(w, "event: subtitle\ndata: %s\n\n", data)
				flusher.Flush()
			}
		}
	})

	mux.HandleFunc("/v1/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(b.Status())
	})

	return mux
}
