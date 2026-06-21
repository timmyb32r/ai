package rawlog

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed webui/*
var webuiFS embed.FS

// Server serves the raw log viewer web UI and API endpoints.
type Server struct {
	baseDir string
	mux     *http.ServeMux
}

// NewServer creates a Server for the given log base directory.
func NewServer(baseDir string) *Server {
	s := &Server{
		baseDir: baseDir,
		mux:     http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

// ListenDynamicPort finds a free TCP port, binds to it, and returns the listener
// along with the assigned port number.
func ListenDynamicPort() (net.Listener, int, error) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		return nil, 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	return ln, port, nil
}

// GetFreePort finds a free TCP port and returns it. The port is immediately
// released after discovery (best-effort race).
func GetFreePort() (int, error) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port, nil
}

// Serve starts the HTTP server on the given listener.
func (s *Server) Serve(ln net.Listener) error {
	return http.Serve(ln, s.mux)
}

// Handler returns the underlying HTTP handler (for use with custom servers).
func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) registerRoutes() {
	// API endpoints
	s.mux.HandleFunc("/api/sessions", s.handleSessions)
	s.mux.HandleFunc("/api/sessions/", s.handleSessionTree)
	s.mux.HandleFunc("/api/log/", s.handleLogFile)

	// Static web UI
	webRoot, _ := fs.Sub(webuiFS, "webui")
	s.mux.Handle("/", http.FileServer(http.FS(webRoot)))
}

// handleSessions returns a JSON array of session IDs.
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	sessionsDir := filepath.Join(s.baseDir, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		writeJSON(w, []any{})
		return
	}

	type sessionInfo struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var sessions []sessionInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Skip empty sessions (no messages logged yet).
		sessionDir := filepath.Join(sessionsDir, e.Name())
		subEntries, subErr := os.ReadDir(sessionDir)
		if subErr != nil || len(subEntries) == 0 {
			continue
		}
		sessions = append(sessions, sessionInfo{ID: e.Name(), Name: humanizeName(e.Name(), "session")})
	}
	writeJSON(w, sessions)
}

// handleSessionTree returns the full tree for a session.
// URL: /api/sessions/{id}/tree
func (s *Server) handleSessionTree(w http.ResponseWriter, r *http.Request) {
	// Extract session ID: /api/sessions/{id}/tree
	path := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	parts := strings.SplitN(path, "/", 2)
	sessionID := parts[0]

	sessionDir := filepath.Join(s.baseDir, "sessions", sessionID)
	root := walkSessionDir(s.baseDir, sessionDir, "session", sessionID)
	if root == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, root)
}

// handleLogFile returns the raw content of a log file.
// URL: /api/log/{path...}
func (s *Server) handleLogFile(w http.ResponseWriter, r *http.Request) {
	relPath := strings.TrimPrefix(r.URL.Path, "/api/log/")

	// Prevent path traversal.
	clean := filepath.Clean(relPath)
	if strings.Contains(clean, "..") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	fullPath := filepath.Join(s.baseDir, clean)
	// Ensure the resolved path is still within baseDir.
	if !strings.HasPrefix(fullPath, filepath.Clean(s.baseDir)) {
		http.Error(w, "access denied", http.StatusForbidden)
		return
	}

	http.ServeFile(w, r, fullPath)
}

// --- Tree walking ---

type treeNode struct {
	Type     string      `json:"type"`
	Name     string      `json:"name"`
	Path     string      `json:"path,omitempty"` // for file nodes
	Children []*treeNode `json:"children,omitempty"`
}

func walkSessionDir(baseDir, dir, nodeType, name string) *treeNode {
	node := &treeNode{Type: nodeType, Name: humanizeName(name, nodeType)}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	for _, e := range entries {
		ePath := filepath.Join(dir, e.Name())
		if e.IsDir() {
			childType := detectNodeType(e.Name())
			child := walkSessionDir(baseDir, ePath, childType, e.Name())
			if child != nil {
				node.Children = append(node.Children, child)
			}
		} else {
			// File node — compute relative path for API lookup.
			relPath, _ := filepath.Rel(baseDir, ePath)
			fileType := "file"
			if strings.HasSuffix(e.Name(), ".json") {
				fileType = "request"
			} else if strings.HasSuffix(e.Name(), ".jsonl") {
				fileType = "response"
			}
			node.Children = append(node.Children, &treeNode{
				Type: fileType,
				Name: e.Name(),
				Path: relPath,
			})
		}
	}
	return node
}

func detectNodeType(name string) string {
	switch {
	case strings.HasPrefix(name, "msg-"):
		return "message"
	case strings.HasPrefix(name, "round-"):
		return "round"
	case strings.HasPrefix(name, "sub-agent-"):
		return "subagent"
	case strings.HasPrefix(name, "sessions"):
		return "session"
	default:
		return "folder"
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// humanizeName converts a raw directory name into a human-readable display name.
// It parses the timestamp embedded in log directory names and formats them nicely.
func humanizeName(raw, nodeType string) string {
	switch nodeType {
	case "session":
		// "session-20260621T152345" or "session-2026-06-21_15-04-05"
		ts := strings.TrimPrefix(raw, "session-")
		if t, err := parseLogTimestamp(ts); err == nil {
			return fmt.Sprintf("Session — %s", t.Format("2006-01-02 15:04:05"))
		}
	case "message":
		// "msg-01_2026-06-21_15-04-05" → "Message 1 — 2026-06-21 15:04:05"
		// "msg-01" → "Message 1"
		rest := strings.TrimPrefix(raw, "msg-")
		num, tsPart := splitNumTS(rest)
		if t, err := parseLogTimestamp(tsPart); err == nil {
			return fmt.Sprintf("Message %d — %s", num, t.Format("2006-01-02 15:04:05"))
		}
		return fmt.Sprintf("Message %d", num)
	case "round":
		// "round-2_2026-06-21_15-04-05" → "Round 2 — 2026-06-21 15:04:05"
		// "round-2" → "Round 2"
		rest := strings.TrimPrefix(raw, "round-")
		num, tsPart := splitNumTS(rest)
		if t, err := parseLogTimestamp(tsPart); err == nil {
			return fmt.Sprintf("Round %d — %s", num, t.Format("2006-01-02 15:04:05"))
		}
		return fmt.Sprintf("Round %d", num)
	case "subagent":
		// "sub-agent-executor_2026-06-21_15-04-05" → "executor — 2026-06-21 15:04:05"
		// "sub-agent-executor" → "executor"
		name := strings.TrimPrefix(raw, "sub-agent-")
		if idx := strings.LastIndex(name, "_20"); idx >= 0 {
			agentName := name[:idx]
			tsPart := name[idx+1:]
			if t, err := parseLogTimestamp(tsPart); err == nil {
				return fmt.Sprintf("%s — %s", agentName, t.Format("2006-01-02 15:04:05"))
			}
		}
		return name
	}
	return raw
}

// parseLogTimestamp tries to parse a timestamp string in all formats used by the logger.
func parseLogTimestamp(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("empty")
	}
	formats := []string{
		"2006-01-02_15-04-05", // new human-readable format
		"20060102T150405",      // old compact format
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unknown timestamp: %s", s)
}

// splitNumTS splits a string like "01_2006-01-02_15-04-05" into number and timestamp.
func splitNumTS(s string) (int, string) {
	idx := strings.Index(s, "_")
	if idx < 0 {
		num, _ := strconv.Atoi(s)
		return num, ""
	}
	num, _ := strconv.Atoi(s[:idx])
	return num, s[idx+1:]
}
