package output

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Writer consumes OutputEvents and renders them.
type Writer interface {
	WriteEvent(ev OutputEvent) error
	Flush() error
}

// --- PlainWriter (pipes mode) ---

// PlainWriter converts OutputEvents to plain text (no ANSI codes, no animations).
// It strips all formatting and writes human-readable output to the underlying writer.
type PlainWriter struct {
	w      *bufio.Writer
	spinning bool
}

// NewPlainWriter creates a PlainWriter that writes to w.
func NewPlainWriter(w io.Writer) *PlainWriter {
	return &PlainWriter{w: bufio.NewWriter(w)}
}

// WriteEvent renders an OutputEvent as plain text.
func (p *PlainWriter) WriteEvent(ev OutputEvent) error {
	w := p.w
	switch ev.Type {
	case EventBanner:
		fmt.Fprintln(w, ev.Text)
	case EventInfo:
		fmt.Fprintln(w, ev.Text)
	case EventThinking:
		if ev.Active {
			p.spinning = true
		} else {
			p.spinning = false
		}
		// In plain mode, thinking events are silent (no spinner possible).
	case EventTextDelta:
		if p.spinning {
			fmt.Fprint(w, "\r\033[K") // clear spinner line
			p.spinning = false
		}
		fmt.Fprint(w, ev.Text)
	case EventToolCall:
		fmt.Fprintf(w, "\n  ⎿  %s\n", ev.Name)
	case EventToolResult:
		text := ev.Text
		if len(text) > 120 {
			text = text[:120] + "..."
		}
		if text != "" {
			fmt.Fprintf(w, "     %s\n", text)
		}
	case EventDone:
		parts := []string{"⏺ Done"}
		if ev.Usage != nil {
			parts = append(parts, fmt.Sprintf("· ⬇%d tok · ⬆%d tok",
				ev.Usage.PromptTokens, ev.Usage.CompletionTokens))
		}
		if ev.ElapsedMs > 0 {
			parts = append(parts, fmt.Sprintf("· %s", formatElapsed(ev.ElapsedMs)))
		}
		fmt.Fprintln(w, strings.Join(parts, " "))
	case EventError:
		fmt.Fprintf(w, "\r\033[K✖ Error: %s\n", ev.Text)
	}
	return nil
}

// Flush flushes buffered output.
func (p *PlainWriter) Flush() error {
	return p.w.Flush()
}

// --- JSONWriter (client-server mode) ---

// JSONWriter serializes OutputEvents as JSON Lines (one JSON object per line).
type JSONWriter struct {
	w       *bufio.Writer
	encoder interface{ writeLine([]byte) error } // helper
}

// NewJSONWriter creates a JSONWriter that writes JSON Lines to w.
func NewJSONWriter(w io.Writer) *JSONWriter {
	return &JSONWriter{w: bufio.NewWriter(w)}
}

// WriteEvent serializes the event as a JSON line.
func (j *JSONWriter) WriteEvent(ev OutputEvent) error {
	line, err := ev.ToJSONLine()
	if err != nil {
		return err
	}
	_, err = j.w.Write(line)
	return err
}

// Flush flushes buffered JSON output.
func (j *JSONWriter) Flush() error {
	return j.w.Flush()
}

// formatElapsed formats milliseconds as a human-readable duration.
func formatElapsed(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	sec := float64(ms) / 1000.0
	return fmt.Sprintf("%.1fs", sec)
}
