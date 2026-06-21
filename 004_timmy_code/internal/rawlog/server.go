package rawlog

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
		if e.IsDir() {
			sessions = append(sessions, sessionInfo{ID: e.Name(), Name: e.Name()})
		}
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
	root := walkSessionDir(sessionDir, "session", sessionID)
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

func walkSessionDir(dir, nodeType, name string) *treeNode {
	node := &treeNode{Type: nodeType, Name: name}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	for _, e := range entries {
		ePath := filepath.Join(dir, e.Name())
		if e.IsDir() {
			childType := detectNodeType(e.Name())
			child := walkSessionDir(ePath, childType, e.Name())
			if child != nil {
				node.Children = append(node.Children, child)
			}
		} else {
			// File node — compute relative path for API lookup.
			relPath, _ := filepath.Rel(filepath.Dir(filepath.Dir(dir)), ePath) // relative to baseDir
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
