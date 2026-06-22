// Package api provides the HTTP interface for the CRI Radio server.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/criradio/server/internal/logging"
	"github.com/criradio/server/internal/models"
	"github.com/criradio/server/internal/storage"
)

// Server holds the HTTP API dependencies.
type Server struct {
	Store     storage.MetadataStore
	Logger    logging.Logger
	HLSDir    string // directory containing playlist.m3u8 and .ts files
	MetaDir   string // directory containing .json metadata files

	clientsConnected atomic.Int64
}

// NewRouter creates and returns an http.Handler with all routes registered.
func (s *Server) NewRouter() http.Handler {
	mux := http.NewServeMux()

	// HLS static files — audio segments and playlist
	hlsFS := http.FileServer(http.Dir(s.HLSDir))
	mux.Handle("/hls/", http.StripPrefix("/hls/", hlsFS))

	// Metadata static files — individual segment JSON
	metaFS := http.FileServer(http.Dir(s.MetaDir))
	mux.Handle("/api/metadata/", http.StripPrefix("/api/metadata/", metaFS))

	// SSE — real-time subtitle push
	mux.HandleFunc("/api/subtitles", s.handleSSE)

	// Segment range — batch metadata query for offline sync
	mux.HandleFunc("/api/segments/range", s.handleSegmentRange)

	// Status — server health and stats
	mux.HandleFunc("/api/status", s.handleStatus)

	return mux
}

// handleSSE streams new subtitle segments as Server-Sent Events.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	s.clientsConnected.Add(1)
	defer s.clientsConnected.Add(-1)

	s.Logger.Info("api", "sse_client_connected", "total", s.clientsConnected.Load())

	// Send initial sync event
	syncData := models.SSESync{
		Type:             "sync",
		TimelineStartSec: 0,
		ServerTime:       "now",
	}
	syncJSON, _ := json.Marshal(syncData)
	fmt.Fprintf(w, "event: sync\ndata: %s\n\n", syncJSON)
	flusher.Flush()

	// Send last 20 existing segments as history (enough to start playback)
	idx, _ := s.Store.ReadIndex()
	if idx != nil && len(idx.Segments) > 0 {
		start := len(idx.Segments) - 20
		if start < 0 { start = 0 }
		for _, ref := range idx.Segments[start:] {
			seg, err := s.Store.Read(ref.ID)
			if err != nil { continue }
			segData := models.SSESegment{Type: "segment", Segment: *seg}
			segJSON, _ := json.Marshal(segData)
			fmt.Fprintf(w, "event: segment\ndata: %s\n\n", segJSON)
		}
	}
	flusher.Flush()

	// Watch for new segments
	ch, err := s.Store.Watch(r.Context())
	if err != nil {
		s.Logger.Error("api", "watch_failed", "err", err)
		return
	}

	for {
		select {
		case <-r.Context().Done():
			s.Logger.Info("api", "sse_client_disconnected", "total", s.clientsConnected.Load())
			return
		case ref, ok := <-ch:
			if !ok {
				return
			}
			seg, err := s.Store.Read(ref.ID)
			if err != nil {
				continue
			}
			segData := models.SSESegment{
				Type:    "segment",
				Segment: *seg,
			}
			segJSON, err := json.Marshal(segData)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: segment\ndata: %s\n\n", segJSON)
			flusher.Flush()
		}
	}
}

// handleStatus returns server health and statistics.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	storeStats := s.Store.Stats()

	status := models.ServerStatus{
		Status:            "running",
		ChannelURL:        "https://sk.cri.cn/905.m3u8",
		SegmentsTotal:     int64(storeStats.NewestID - storeStats.OldestID + 1),
		MetadataFiles:     storeStats.TotalFiles,
		LiveEdgeOffsetSec: 180.0, // TODO: compute from ingest stats
		ClientsConnected:  int(s.clientsConnected.Load()),
	}

	// Populate archive time range from index so clients can validate sync windows.
	if idx, err := s.Store.ReadIndex(); err == nil && len(idx.Segments) > 0 {
		status.OldestSegmentStartSec = idx.Segments[0].TimelineStartSec
		status.NewestSegmentEndSec = idx.Segments[len(idx.Segments)-1].TimelineEndSec
	}

	json.NewEncoder(w).Encode(status)
}

// handleSegmentRange returns segments whose timeline overlaps [start_sec, end_sec].
// Supports pagination via limit and offset query params.
func (s *Server) handleSegmentRange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	startStr := r.URL.Query().Get("start_sec")
	endStr := r.URL.Query().Get("end_sec")
	if startStr == "" || endStr == "" {
		WriteError(w, "start_sec and end_sec query params required", http.StatusBadRequest)
		return
	}

	startSec, err := strconv.ParseFloat(startStr, 64)
	if err != nil {
		WriteError(w, "invalid start_sec", http.StatusBadRequest)
		return
	}
	endSec, err := strconv.ParseFloat(endStr, 64)
	if err != nil {
		WriteError(w, "invalid end_sec", http.StatusBadRequest)
		return
	}
	if endSec <= startSec {
		WriteError(w, "end_sec must be greater than start_sec", http.StatusBadRequest)
		return
	}

	segments, err := s.Store.ReadRange(startSec, endSec)
	if err != nil {
		WriteError(w, "storage error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Pagination
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")
	limit := len(segments)
	offset := 0
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
		}
	}
	if offsetStr != "" {
		if n, err := strconv.Atoi(offsetStr); err == nil && n >= 0 {
			offset = n
		}
	}

	total := len(segments)
	start := offset
	if start > len(segments) {
		start = len(segments)
	}
	end := start + limit
	if end > len(segments) {
		end = len(segments)
	}
	paged := segments[start:end]

	json.NewEncoder(w).Encode(map[string]interface{}{
		"segments": paged,
		"count":    len(paged),
		"total":    total,
	})
}

// WriteError writes a JSON error response.
func WriteError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// segmentIDFromPath extracts the segment ID from a filename like "000000001.json" or "000000001".
func segmentIDFromPath(filename string) int {
	base := filepath.Base(filename)
	base = strings.TrimSuffix(base, ".json")
	id := 0
	for _, c := range base {
		if c >= '0' && c <= '9' {
			id = id*10 + int(c-'0')
		}
	}
	return id
}
