package output

import (
	"strings"
	"time"

	"github.com/timmy/timmy-code/internal/query"
)

// OutputBuilder converts query engine events into OutputEvents.
// It centralizes all output formatting logic so that both pipes mode
// and client-server mode share the same event generation.
type OutputBuilder struct {
	thinking    bool
	textStarted bool
}

// NewOutputBuilder creates a new OutputBuilder.
func NewOutputBuilder() *OutputBuilder {
	return &OutputBuilder{}
}

// BuildBanner creates a banner event.
func (b *OutputBuilder) BuildBanner(text string) OutputEvent {
	return OutputEvent{Type: EventBanner, Text: text}
}

// BuildInfo creates an info event.
func (b *OutputBuilder) BuildInfo(text string) OutputEvent {
	return OutputEvent{Type: EventInfo, Text: text}
}

// BuildThinkingStart creates a thinking-started event.
func (b *OutputBuilder) BuildThinkingStart(text string) OutputEvent {
	b.thinking = true
	return OutputEvent{Type: EventThinking, Text: text, Active: true}
}

// BuildThinkingStop creates a thinking-stopped event.
func (b *OutputBuilder) BuildThinkingStop() OutputEvent {
	b.thinking = false
	return OutputEvent{Type: EventThinking, Active: false}
}

// BuildDone creates a done event with usage and elapsed time.
func (b *OutputBuilder) BuildDone(usage *query.TokenUsage, elapsed time.Duration) OutputEvent {
	ev := OutputEvent{
		Type:      EventDone,
		ElapsedMs: elapsed.Milliseconds(),
	}
	if usage != nil {
		ev.Usage = &UsageInfo{
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
		}
	}
	return ev
}

// BuildError creates an error event.
func (b *OutputBuilder) BuildError(err error) OutputEvent {
	return OutputEvent{Type: EventError, Text: err.Error()}
}

// ConvertQueryEvent converts a query engine event into one or more output events.
// Returns a slice because a single query event may produce multiple output events
// (e.g., first text delta also stops thinking).
func (b *OutputBuilder) ConvertQueryEvent(ev query.Event) []OutputEvent {
	var events []OutputEvent

	// First text delta: stop thinking spinner.
	if ev.Type == query.EventTextDelta && b.thinking {
		events = append(events, OutputEvent{Type: EventThinking, Active: false})
		b.thinking = false
		b.textStarted = true
	}

	switch ev.Type {
	case query.EventTextDelta:
		events = append(events, OutputEvent{Type: EventTextDelta, Text: ev.Text})

	case query.EventToolCall:
		if ev.ToolUse != nil {
			oe := OutputEvent{
				Type:   EventToolCall,
				Name:   ev.ToolUse.Name,
				Detail: toolCallDetail(ev.ToolUse.Name, ev.ToolUse.Input),
				Input:  ev.ToolUse.Input,
			}
			events = append(events, oe)
		}

	case query.EventToolResult:
		text := strings.TrimSpace(ev.Text)
		truncated := false
		if len(text) > 200 {
			text = text[:200] + "..."
			truncated = true
		}
		events = append(events, OutputEvent{
			Type:      EventToolResult,
			Text:      text,
			Truncated: truncated,
		})

	case query.EventError:
		events = append(events, OutputEvent{Type: EventError, Text: ev.Error.Error()})

	case query.EventDone:
		ev := OutputEvent{Type: EventDone, ElapsedMs: 0} // elapsed filled by BuildDone
		if ev.Usage != nil {
			ev.Usage = &UsageInfo{
				PromptTokens:     ev.Usage.PromptTokens,
				CompletionTokens: ev.Usage.CompletionTokens,
			}
		}
		events = append(events, ev)
	}

	return events
}

// IsThinking returns whether the builder is in thinking state.
func (b *OutputBuilder) IsThinking() bool {
	return b.thinking
}

// Reset resets the builder state for a new query.
func (b *OutputBuilder) Reset() {
	b.thinking = false
	b.textStarted = false
}

// toolCallDetail returns a short description of a tool call for display.
func toolCallDetail(name string, input map[string]any) string {
	switch name {
	case "read":
		if path, ok := input["file_path"].(string); ok {
			return path
		}
	case "write":
		if path, ok := input["file_path"].(string); ok {
			return path
		}
	case "edit":
		if path, ok := input["file_path"].(string); ok {
			return path
		}
	case "bash":
		if cmd, ok := input["command"].(string); ok {
			if len(cmd) > 60 {
				return cmd[:60] + "..."
			}
			return cmd
		}
	case "agent":
		if desc, ok := input["description"].(string); ok {
			return desc
		}
	}
	return ""
}
