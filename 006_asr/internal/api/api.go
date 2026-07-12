package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/timmyb32r/yt2srt/internal/logging"
	"github.com/timmyb32r/yt2srt/internal/models"
	"github.com/timmyb32r/yt2srt/internal/storage"
	"github.com/timmyb32r/yt2srt/internal/worker"
)

// Max concurrent jobs to prevent resource exhaustion.
const maxConcurrentJobs = 4

var jobSem = make(chan struct{}, maxConcurrentJobs)

// Handler holds HTTP dependencies.
type Handler struct {
	store    *storage.InMemoryStore
	wrk      *worker.Worker
	log      logging.Logger
	staticFS fs.FS
}

// NewHandler creates an HTTP handler.
func NewHandler(store *storage.InMemoryStore, wrk *worker.Worker, log logging.Logger, staticFS fs.FS) *Handler {
	return &Handler{store: store, wrk: wrk, log: log, staticFS: staticFS}
}

// RegisterRoutes sets up the HTTP mux with security headers middleware.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/", h.handleIndex)
	mux.HandleFunc("/health", h.handleHealth)
	fileServer := http.FileServer(http.FS(h.staticFS))
	mux.Handle("/static/", securityHeaders(http.StripPrefix("/static/", fileServer)))
	mux.HandleFunc("/api/transcribe", h.handleTranscribe)
	mux.HandleFunc("/api/status/", h.handleStatus)
	mux.HandleFunc("/api/download/", h.handleDownload)
}

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data, err := fs.ReadFile(h.staticFS, "index.html")
	if err != nil {
		http.Error(w, "index not found", 500)
		return
	}
	w.Write(data)
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

type transcribeRequest struct {
	URL string `json:"url"`
}

func (h *Handler) handleTranscribe(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}

	var req transcribeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		writeJSON(w, 400, map[string]string{"error": "url is required"})
		return
	}

	// Validate YouTube URL using proper hostname matching (prevents SSRF)
	if !isValidYouTubeURL(req.URL) {
		writeJSON(w, 400, map[string]string{"error": "not a YouTube URL"})
		return
	}

	// Rate limit: check global concurrency semaphore
	select {
	case jobSem <- struct{}{}:
	default:
		writeJSON(w, 429, map[string]string{"error": "too many jobs, try later"})
		return
	}

	id := newJobID()
	now := time.Now()
	job := &models.Job{
		ID:        id,
		URL:       req.URL,
		Status:    models.StatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	h.store.Create(job)

	go func() {
		defer func() { <-jobSem }()
		h.wrk.ProcessJob(id)
	}()

	// Log URL without query params
	logURL := stripQueryParams(req.URL)
	h.log.Info("api", "job_created", "job_id", id, "url", logURL)
	writeJSON(w, 201, map[string]string{"job_id": id})
}

func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}

	id := extractID(r.URL.Path, "/api/status/")
	if id == "" {
		writeJSON(w, 400, map[string]string{"error": "missing job id"})
		return
	}

	job := h.store.Get(id)
	if job == nil {
		writeJSON(w, 404, map[string]string{"error": "job not found"})
		return
	}

	writeJSON(w, 200, job)
}

func (h *Handler) handleDownload(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}

	id := extractID(r.URL.Path, "/api/download/")
	if id == "" {
		writeJSON(w, 400, map[string]string{"error": "missing job id"})
		return
	}

	job := h.store.Get(id)
	if job == nil {
		writeJSON(w, 404, map[string]string{"error": "job not found"})
		return
	}

	if job.Status != models.StatusDone {
		http.Error(w, "job not completed yet", 409)
		return
	}

	if job.SRTContent == "" {
		http.Error(w, "no subtitles generated", 404)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"transcript.srt\"")
	w.Write([]byte(job.SRTContent))
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func extractID(path, prefix string) string {
	s := strings.TrimPrefix(path, prefix)
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return s
}

func newJobID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is extremely rare — use timestamp as last resort
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// StartCleanup runs a periodic cleanup goroutine for old jobs.
func StartCleanup(store *storage.InMemoryStore, log logging.Logger, interval, maxAge time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			store.CleanupOlderThan(maxAge)
		}
	}()
	log.Info("api", "cleanup_started", "interval", interval.String(), "max_age", maxAge.String())
}

// isValidYouTubeURL validates YouTube URLs using proper hostname matching.
func isValidYouTubeURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "www.youtube.com", "youtube.com", "youtu.be", "m.youtube.com":
		return true
	}
	return false
}

// stripQueryParams removes query string from URL for safe logging.
func stripQueryParams(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

// setSecurityHeaders adds security-related HTTP headers.
func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Content-Security-Policy", "default-src 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

// securityHeaders wraps an http.Handler with security headers.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)
		next.ServeHTTP(w, r)
	})
}
