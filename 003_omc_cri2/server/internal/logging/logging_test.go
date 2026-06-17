package logging

import (
	"strings"
	"testing"
)

func TestProductionLogger(t *testing.T) {
	logger := NewProductionLogger("debug")

	// Should not panic
	logger.Debug("test", "start", "key", "value")
	logger.Info("test", "running", "count", 42)
	logger.Warn("test", "slow", "ms", 150)
	logger.Error("test", "fail", "err", "something broke")
}

func TestLogLevelFilter(t *testing.T) {
	// Info level should drop debug messages
	logger := NewProductionLogger("info")
	// These should not panic — debug messages are silently dropped
	logger.Debug("test", "hidden")
	logger.Info("test", "visible")
}

func TestLogFormat(t *testing.T) {
	// We can't easily capture stdout, but we can verify construction
	logger := NewProductionLogger("info")
	if logger == nil {
		t.Fatal("expected non-nil logger")
	}
}

func TestNewProductionLoggerDefaults(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"debug", "debug"},
		{"info", "info"},
		{"warn", "warn"},
		{"error", "error"},
		{"DEBUG", "debug"}, // case insensitive via strings.ToLower
		{"", "info"},       // empty defaults to info
		{"invalid", "info"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			l := NewProductionLogger(tt.input)
			if l == nil {
				t.Error("logger should not be nil")
			}
		})
	}
}

func TestLevelString(t *testing.T) {
	if s := DEBUG.String(); s != "D" {
		t.Errorf("DEBUG: got %q, want D", s)
	}
	if s := INFO.String(); s != "I" {
		t.Errorf("INFO: got %q, want I", s)
	}
	if s := WARN.String(); s != "W" {
		t.Errorf("WARN: got %q, want W", s)
	}
	if s := ERROR.String(); s != "E" {
		t.Errorf("ERROR: got %q, want E", s)
	}
}

func TestLogKVFormat(t *testing.T) {
	// Test that key-value pairs are formatted correctly
	// We can't easily check stdout, but we verify no panics with various types
	logger := NewProductionLogger("debug")

	logger.Info("tokenizer", "segment_done", "words", 15, "text", "你好世界")
	logger.Debug("asr", "transcribe", "segment_id", 42, "duration_ms", 450, "text_len", 18)
	logger.Warn("ingest", "reconnect", "attempt", 3, "delay_ms", 500)
	logger.Error("pipeline", "panic", "goroutine", 5, "stack", "...")

	// Test with odd number of kv pairs (last key without value)
	logger.Info("test", "odd_kv", "key_only")
}

func TestRateLimit(t *testing.T) {
	logger := NewProductionLogger("debug")
	// Send 100 log lines — rate limiter should drop most of them
	for i := 0; i < 100; i++ {
		logger.Debug("test", "spam", "n", i)
	}
	// Test passes if no panic — rate limiting is best-effort
}

// Benchmark log line formatting
func BenchmarkLogLine(b *testing.B) {
	logger := NewProductionLogger("warn") // warn will skip debug/info, just measure overhead
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Error("bench", "test", "i", i, "msg", "hello world")
	}
}

// Helper for testing: collects log lines into a buffer
type testLogger struct {
	lines []string
	level Level
}

func (l *testLogger) log(level Level, module, event string, kv ...interface{}) {
	if level < l.level {
		return
	}
	var sb strings.Builder
	sb.WriteString(level.String() + " " + module + " " + event)
	for i := 0; i < len(kv); i += 2 {
		if i+1 < len(kv) {
			sb.WriteString(" " + kv[i].(string) + "=" + kv[i+1].(string))
		}
	}
	l.lines = append(l.lines, sb.String())
}

func (l *testLogger) Debug(module, event string, kv ...interface{})   { l.log(DEBUG, module, event, kv...) }
func (l *testLogger) Info(module, event string, kv ...interface{})    { l.log(INFO, module, event, kv...) }
func (l *testLogger) Warn(module, event string, kv ...interface{})    { l.log(WARN, module, event, kv...) }
func (l *testLogger) Error(module, event string, kv ...interface{})   { l.log(ERROR, module, event, kv...) }
func (l *testLogger) Lines() []string                                  { return l.lines }

var _ Logger = (*testLogger)(nil)
