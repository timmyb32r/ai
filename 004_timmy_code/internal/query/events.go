package query

import "github.com/timmy/timmy-code/internal/schema"

// EventType is an alias for schema.EventType so callers can use query.EventType.
type EventType = schema.EventType

const (
	EventTextDelta  = schema.EventTextDelta
	EventToolCall   = schema.EventToolCall
	EventToolResult = schema.EventToolResult
	EventDone       = schema.EventDone
	EventError      = schema.EventError
)

// ToolUseEvent carries tool invocation details from the LLM.
type ToolUseEvent struct {
	Name  string
	Input map[string]any
}

// Event is emitted by the query engine during a query loop round.
type Event struct {
	Type    EventType
	Round   int
	Text    string
	ToolUse *ToolUseEvent
	Error   error
	Usage   *TokenUsage // set on Done events, nil otherwise
}

// TokenUsage carries token counts from the LLM API.
type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}
