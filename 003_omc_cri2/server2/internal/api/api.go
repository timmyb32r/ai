// Package api preserves the HLS, metadata and subtitle HTTP protocol.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/criradio/server/internal/models"
	"github.com/criradio/server/internal/storage"
)

type Server struct {
	Store   *storage.Store
	Status  func() any
	clients atomic.Int64
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/hls/", s.hls)
	mux.HandleFunc("/api/metadata/", s.metadata)
	mux.HandleFunc("/api/subtitles", s.sse)
	mux.HandleFunc("/api/segments/at", s.at)
	mux.HandleFunc("/api/segments/batch", s.batch)
	mux.HandleFunc("/api/segments/range", s.segmentRange)
	mux.HandleFunc("/api/snapshots/", s.release)
	mux.HandleFunc("/api/status", s.status)
	mux.HandleFunc("/health/live", func(w http.ResponseWriter, r *http.Request) {
		if getOnly(w, r) {
			writeJSON(w, 200, map[string]string{"status": "alive"})
		}
	})
	mux.HandleFunc("/health/ready", s.ready)
	slots := make(chan struct{}, 64)
	metadataSlots := make(chan struct{}, 8)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Range, Last-Event-ID")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Accept-Ranges, Retry-After")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			writeError(w, storage.ErrBusy)
			return
		}
		// Expanding dictionary metadata and serializing it can use substantially
		// more memory than serving audio. Keep separate nonqueued admission so a
		// few slow metadata readers cannot consume the audio/health capacity.
		if strings.HasPrefix(r.URL.Path, "/api/metadata/") || strings.HasPrefix(r.URL.Path, "/api/segments/") {
			select {
			case metadataSlots <- struct{}{}:
				defer func() { <-metadataSlots }()
			default:
				writeError(w, storage.ErrBusy)
				return
			}
		}
		if len(r.URL.RawQuery) > 4096 {
			writeError(w, storage.ErrInvalid)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func getOnly(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "invalid server response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}
func writeError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	msg := "storage unavailable"
	switch {
	case errors.Is(err, storage.ErrInvalid):
		code = http.StatusBadRequest
		msg = err.Error()
	case errors.Is(err, storage.ErrTooLarge):
		code = http.StatusRequestEntityTooLarge
		msg = err.Error()
	case errors.Is(err, storage.ErrNotFound):
		code = http.StatusNotFound
		msg = "segment not found"
	case errors.Is(err, storage.ErrSnapshotExpired):
		code = http.StatusGone
		msg = err.Error()
	case errors.Is(err, storage.ErrBusy):
		code = http.StatusTooManyRequests
		msg = "capacity exceeded; retry later"
		w.Header().Set("Retry-After", "5")
	case errors.Is(err, storage.ErrLowDisk):
		code = http.StatusServiceUnavailable
		msg = "disk reserve reached; new downloads paused"
		w.Header().Set("Retry-After", "30")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = http.StatusServiceUnavailable
		msg = "request cancelled"
	}
	writeJSON(w, code, map[string]string{"error": msg})
}
func number(raw string) (float64, error) {
	v, e := strconv.ParseFloat(raw, 64)
	if e != nil || math.IsInf(v, 0) || math.IsNaN(v) {
		return 0, storage.ErrInvalid
	}
	return v, nil
}
func integer(raw string, def, min, max int) (int, error) {
	if raw == "" {
		return def, nil
	}
	v, e := strconv.Atoi(raw)
	if e != nil || v < min || v > max {
		return 0, storage.ErrInvalid
	}
	return v, nil
}

func (s *Server) hls(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/hls/")
	if name == "playlist.m3u8" || name == "live.m3u8" {
		entries, err := s.Store.PlaylistView(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		var b strings.Builder
		target := 1
		for _, e := range entries {
			d := int(math.Ceil(e.Segment.TimelineEndSec - e.Segment.TimelineStartSec))
			if d > target {
				target = d
			}
		}
		seq, disc := int64(0), int64(0)
		if len(entries) > 0 {
			seq = entries[0].Sequence
			disc = entries[0].DiscontinuitySequence
			if entries[0].Segment.Discontinuity {
				disc--
			}
		}
		fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:%d\n#EXT-X-DISCONTINUITY-SEQUENCE:%d\n", target, seq, disc)
		for i, e := range entries {
			seg := e.Segment
			if seg.Discontinuity || (i > 0 && seg.TimelineStartSec > entries[i-1].Segment.TimelineEndSec+0.001) {
				b.WriteString("#EXT-X-DISCONTINUITY\n")
			}
			t := time.Unix(0, int64(seg.TimelineStartSec*1e9)).UTC()
			fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n#EXTINF:%.6f,\n%s\n", t.Format(time.RFC3339Nano), seg.TimelineEndSec-seg.TimelineStartSec, seg.TSFile)
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(b.String()))
		}
		return
	}
	id, err := storage.ParseFileID(name, ".ts")
	if err != nil {
		writeError(w, storage.ErrNotFound)
		return
	}
	file, err := s.Store.OpenAudioSnapshot(r.Context(), id, r.URL.Query().Get("snapshot"))
	if err != nil {
		writeError(w, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", fmt.Sprintf("\"segment-%d-%d\"", id, info.Size()))
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
	http.ServeContent(w, r, name, info.ModTime(), file)
}
func (s *Server) metadata(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/metadata/")
	if name == "index.json" {
		segments, err := s.Store.Visible(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		index := models.SegmentIndex{UpdatedAt: time.Now().UTC().Format(time.RFC3339), Segments: []models.SegmentRef{}}
		for _, seg := range segments {
			index.Segments = append(index.Segments, models.SegmentRef{ID: seg.SegmentID, TimelineStartSec: seg.TimelineStartSec, TimelineEndSec: seg.TimelineEndSec, TSFile: seg.TSFile, JSONFile: fmt.Sprintf("%09d.json", seg.SegmentID)})
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, index)
		return
	}
	id, err := storage.ParseFileID(name, ".json")
	if err != nil {
		writeError(w, storage.ErrNotFound)
		return
	}
	seg, err := s.Store.Read(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	writeJSON(w, http.StatusOK, seg)
}
func (s *Server) at(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	sec, err := number(r.URL.Query().Get("sec"))
	if err != nil {
		writeError(w, err)
		return
	}
	seg, err := s.Store.At(r.Context(), sec)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"found": seg != nil, "segment": seg})
}
func (s *Server) batch(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	n, err := integer(r.URL.Query().Get("last"), 3, 1, 100)
	if err != nil {
		writeError(w, err)
		return
	}
	segments, err := s.Store.Latest(r.Context(), n)
	if err != nil {
		writeError(w, err)
		return
	}
	if r.URL.Query().Get("lite") == "true" {
		for i := range segments {
			for j := range segments[i].Words {
				word := &segments[i].Words[j]
				word.Senses = nil
				word.CedictMeanings = nil
				word.WiktionaryMeanings = nil
				word.Trans = ""
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"segments": segments})
}
func (s *Server) segmentRange(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	q := r.URL.Query()
	start, err := number(q.Get("start_sec"))
	if err != nil {
		writeError(w, err)
		return
	}
	end, err := number(q.Get("end_sec"))
	if err != nil {
		writeError(w, err)
		return
	}
	limit, err := integer(q.Get("limit"), 500, 1, 1000)
	if err != nil {
		writeError(w, err)
		return
	}
	offset, err := integer(q.Get("offset"), 0, 0, math.MaxInt32)
	if err != nil {
		writeError(w, err)
		return
	}
	token := q.Get("snapshot")
	if token == "" {
		token = q.Get("snapshot_id")
	}
	if token != "" && token != "create" && len(token) != 48 {
		writeError(w, storage.ErrSnapshotExpired)
		return
	}
	var page storage.Page
	if token == "" {
		page, err = s.Store.Range(r.Context(), start, end, limit, offset)
	} else {
		if token == "create" {
			token = ""
		}
		page, err = s.Store.SnapshotPage(r.Context(), token, start, end, limit, offset)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, page)
}
func (s *Server) release(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if err := s.Store.ReleaseSnapshot(r.Context(), strings.TrimPrefix(r.URL.Path, "/api/snapshots/")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	stats, err := s.Store.Stats(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	status := map[string]any{"status": "running", "channel_url": "", "asr_engine": "", "asr_model": "", "dictionary": "", "live_edge_offset_sec": 0}
	if s.Status != nil {
		if b, e := json.Marshal(s.Status()); e == nil {
			_ = json.Unmarshal(b, &status)
		}
	}
	status["segments_total"] = stats.Total
	status["metadata_files"] = stats.Total
	status["oldest_segment_start_sec"] = stats.Oldest
	status["newest_segment_end_sec"] = stats.Newest
	status["clients_connected"] = s.clients.Load()
	status["storage_bytes"] = stats.Bytes
	status["active_snapshots"] = stats.Snapshots
	if err = s.Store.Writable(r.Context()); err != nil {
		status["status"] = "degraded"
		status["storage_error"] = err.Error()
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, status)
}
func (s *Server) sse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	for {
		n := s.clients.Load()
		if n >= 5 {
			writeError(w, storage.ErrBusy)
			return
		}
		if s.clients.CompareAndSwap(n, n+1) {
			break
		}
	}
	defer s.clients.Add(-1)
	history, ch, cancel, err := s.Store.Subscribe(r.Context(), 20)
	if err != nil {
		writeError(w, err)
		return
	}
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	send := func(event string, payload any, id *int) error {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if id != nil {
			if _, err = fmt.Fprintf(w, "id: %d\n", *id); err != nil {
				return err
			}
		}
		if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return err
		}
		if err = controller.Flush(); err != nil {
			return err
		}
		_ = controller.SetWriteDeadline(time.Time{})
		return nil
	}
	start := float64(0)
	if len(history) > 0 {
		start = history[0].TimelineStartSec
	}
	if err = send("sync", models.SSESync{Type: "sync", TimelineStartSec: start, ServerTime: time.Now().UTC().Format(time.RFC3339)}, nil); err != nil {
		return
	}
	for _, seg := range history {
		if err = send("segment", models.SSESegment{Type: "segment", Segment: seg}, &seg.SegmentID); err != nil {
			return
		}
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case seg, ok := <-ch:
			if !ok {
				return
			}
			if err = send("segment", models.SSESegment{Type: "segment", Segment: seg}, &seg.SegmentID); err != nil {
				return
			}
		case <-ticker.C:
			_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err = fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			if err = controller.Flush(); err != nil {
				return
			}
			_ = controller.SetWriteDeadline(time.Time{})
		}
	}
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	status := map[string]any{"status": "running"}
	if s.Status != nil {
		b, err := json.Marshal(s.Status())
		if err != nil {
			writeJSON(w, 503, map[string]string{"status": "unavailable"})
			return
		}
		if err = json.Unmarshal(b, &status); err != nil {
			writeJSON(w, 503, map[string]string{"status": "unavailable"})
			return
		}
	}
	if err := s.Store.Writable(r.Context()); err != nil {
		writeJSON(w, 503, map[string]string{"status": "degraded", "error": err.Error()})
		return
	}
	if status["status"] != "running" {
		writeJSON(w, 503, status)
		return
	}
	writeJSON(w, 200, status)
}
