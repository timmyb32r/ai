// Package logging provides rate-limited structured logging to stdout.
// Format: [L] HH:MM:SS.mmm module event key=value...
// Rate limit: max 10 lines/sec across all modules.
package logging

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Level represents log severity.
type Level int

const (
	DEBUG Level = iota
	INFO
	WARN
	ERROR
)

func (l Level) String() string {
	switch l {
	case DEBUG:
		return "D"
	case INFO:
		return "I"
	case WARN:
		return "W"
	case ERROR:
		return "E"
	default:
		return "?"
	}
}

// Logger provides rate-limited structured logging.
type Logger interface {
	Debug(module, event string, kv ...interface{})
	Info(module, event string, kv ...interface{})
	Warn(module, event string, kv ...interface{})
	Error(module, event string, kv ...interface{})
}

// NewProductionLogger creates a stdout logger with the given minimum level.
func NewProductionLogger(level string) Logger {
	var minLevel Level
	switch strings.ToLower(level) {
	case "debug":
		minLevel = DEBUG
	case "info":
		minLevel = INFO
	case "warn":
		minLevel = WARN
	case "error":
		minLevel = ERROR
	default:
		minLevel = INFO
	}
	return &productionLogger{
		minLevel: minLevel,
	}
}

type productionLogger struct {
	minLevel  Level
	lineCount atomic.Int64
	lastReset atomic.Int64 // Unix seconds of last counter reset
	mu        sync.Mutex   // serializes writes
}

func (l *productionLogger) Debug(module, event string, kv ...interface{}) {
	l.log(DEBUG, module, event, kv...)
}

func (l *productionLogger) Info(module, event string, kv ...interface{}) {
	l.log(INFO, module, event, kv...)
}

func (l *productionLogger) Warn(module, event string, kv ...interface{}) {
	l.log(WARN, module, event, kv...)
}

func (l *productionLogger) Error(module, event string, kv ...interface{}) {
	l.log(ERROR, module, event, kv...)
}

func (l *productionLogger) log(level Level, module, event string, kv ...interface{}) {
	if level < l.minLevel {
		return
	}

	// Rate limit: 10 lines/sec
	now := time.Now().Unix()
	if lastReset := l.lastReset.Load(); lastReset != now {
		l.lastReset.Store(now)
		l.lineCount.Store(0)
	}
	if l.lineCount.Add(1) > 10 {
		// Drop log line — rate limit exceeded
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now2 := time.Now()
	timestamp := now2.Format("2006-01-02 15:04:05.000")

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[%s] %s %s %s", level.String(), timestamp, module, event))
	for i := 0; i < len(kv); i += 2 {
		if i+1 < len(kv) {
			sb.WriteString(fmt.Sprintf(" %v=%v", kv[i], kv[i+1]))
		}
	}
	fmt.Println(sb.String())
}
