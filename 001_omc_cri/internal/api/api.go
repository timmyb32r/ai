// Package api exposes the v1 HTTP surface: the chunked MP3 audio stream, the
// SSE subtitle stream, and the JSON status endpoint. It depends on the
// Broadcaster interface (not the concrete broadcast.Broadcast) so it can be
// tested with a fake. Network I/O lives here and in internal/ingest / cmd; the
// chineseasr library stays offline.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/timmyb32r/001_omc_cri/internal/broadcast"
)

// Broadcaster is the minimal broadcast surface the HTTP layer needs. Decoupling
// the API from the concrete *broadcast.Broadcast lets worker-api test the
// handlers with a fake implementation.
type Broadcaster interface {
	// Subscribe registers a subscriber and returns its paced audio channel, its
	// subtitle channel, and a cancel func that unsubscribes.
	Subscribe() (<-chan []byte, <-chan broadcast.SubtitleEvent, func())
	// Status returns the current status snapshot for GET /v1/status.
	Status() broadcast.Status
}

// NewMux builds the v1 HTTP mux backed by b. The three routes are:
//
//	GET /v1/stream/audio      — chunked audio/mpeg stream
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

		audio, _, cancel := b.Subscribe()
		defer cancel()

		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
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
		_, subs, cancel := b.Subscribe()
		defer cancel()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-subs:
				if !ok {
					return
				}
				data, err := json.Marshal(ev)
				if err != nil {
					return
				}
				fmt.Fprintf(w, "data: %s\n\n", data)
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
