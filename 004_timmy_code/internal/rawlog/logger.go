// Package rawlog provides structured logging of raw LLM I/O to disk.
// It creates a filesystem hierarchy under ./.timmy-code/raw_llm_io_log/
// that mirrors the conversational structure: sessions → messages → rounds.
package rawlog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Logger is the root of the logging hierarchy. It owns the base directory
// where all session data is stored.
type Logger struct {
	baseDir string
	mu      sync.Mutex
}

// SessionLogger logs a single interactive session.
type SessionLogger struct {
	sessionID string
	dir       string
	mu        sync.Mutex
	counter   int
}

// MessageLogger logs a single user message (SubmitMessage) within a session.
type MessageLogger struct {
	dir     string
	mu      sync.Mutex
	counter int
}

// RoundLogger logs a single round (LLM request + response) within a message.
// It writes request.json and response.jsonl to the round directory.
type RoundLogger struct {
	dir          string
	mu           sync.Mutex
	requestPath  string
	responsePath string
	responseFile *os.File
}

// SubAgentLogger embeds Logger so it can be used recursively for sub-agent
// interactions, producing its own sessions/messages/rounds hierarchy
// rooted at a sub-agent directory under the parent round.
type SubAgentLogger struct {
	Logger
}

// NewLogger creates a Logger rooted at baseDir. The directory is created
// if it does not already exist.
func NewLogger(baseDir string) *Logger {
	_ = os.MkdirAll(baseDir, 0755)
	return &Logger{baseDir: baseDir}
}

// StartSession creates a new session directory under <baseDir>/sessions/<id>.
func (l *Logger) StartSession(sessionID string) *SessionLogger {
	l.mu.Lock()
	defer l.mu.Unlock()
	dir := filepath.Join(l.baseDir, "sessions", sessionID)
	_ = os.MkdirAll(dir, 0755)
	return &SessionLogger{
		sessionID: sessionID,
		dir:       dir,
	}
}

// NewMessage creates a new message directory with an auto-incrementing
// two-digit counter (msg-01, msg-02, …).
func (s *SessionLogger) NewMessage() *MessageLogger {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counter++
	msgDir := filepath.Join(s.dir, fmt.Sprintf("msg-%02d_%s", s.counter, time.Now().Format("2006-01-02_15-04-05")))
	_ = os.MkdirAll(msgDir, 0755)
	return &MessageLogger{dir: msgDir}
}

// NewRound creates a new round directory with an auto-incrementing counter
// (round-1, round-2, …). It opens response.jsonl for append.
func (m *MessageLogger) NewRound() *RoundLogger {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counter++
	roundDir := filepath.Join(m.dir, fmt.Sprintf("round-%d_%s", m.counter, time.Now().Format("2006-01-02_15-04-05")))
	_ = os.MkdirAll(roundDir, 0755)

	requestPath := filepath.Join(roundDir, "request.json")
	responsePath := filepath.Join(roundDir, "response.jsonl")

	f, err := os.OpenFile(responsePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		f = nil
	}

	return &RoundLogger{
		dir:          roundDir,
		requestPath:  requestPath,
		responsePath: responsePath,
		responseFile: f,
	}
}

// LogRequest writes payload as indented (2-space) JSON to request.json.
// If payload is not valid JSON it falls back to writing raw bytes.
func (r *RoundLogger) LogRequest(payload []byte) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, payload, "", "  "); err != nil {
		return os.WriteFile(r.requestPath, payload, 0644)
	}
	return os.WriteFile(r.requestPath, buf.Bytes(), 0644)
}

// LogResponseLine appends a single JSON line (followed by \n) to response.jsonl.
func (r *RoundLogger) LogResponseLine(data []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.responseFile == nil {
		f, err := os.OpenFile(r.responsePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		r.responseFile = f
	}

	if _, err := r.responseFile.Write(data); err != nil {
		return err
	}
	_, err := r.responseFile.Write([]byte("\n"))
	return err
}

// CloseResponse closes the response.jsonl file handle.
func (r *RoundLogger) CloseResponse() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.responseFile != nil {
		err := r.responseFile.Close()
		r.responseFile = nil
		return err
	}
	return nil
}

// CreateSubAgent creates a SubAgentLogger rooted at
// <round-dir>/sub-agent-<name>. The returned logger can be used recursively
// via StartSession / NewMessage / NewRound to produce nested hierarchies.
func (r *RoundLogger) CreateSubAgent(name string) *SubAgentLogger {
	dir := filepath.Join(r.dir, fmt.Sprintf("sub-agent-%s_%s", name, time.Now().Format("2006-01-02_15-04-05")))
	_ = os.MkdirAll(dir, 0755)
	return &SubAgentLogger{
		Logger: Logger{baseDir: dir},
	}
}
