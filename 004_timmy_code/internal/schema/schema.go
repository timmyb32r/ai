package schema

// ToolDef describes a tool for LLM function calling.
type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON Schema
}

// ToolCall in an assistant message.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // JSON-encoded
}

// Message represents a chat message.
type Message struct {
	Role       string     // "system" | "user" | "assistant" | "tool"
	Content    string
	Name       string     // tool name
	ToolCallID string     // tool call ID, required for "tool" role messages
	ToolCalls  []ToolCall // present when assistant calls tools
}

// EventType for query engine events.
type EventType string

const (
	EventTextDelta  EventType = "text_delta"
	EventToolCall   EventType = "tool_call"
	EventToolResult EventType = "tool_result"
	EventDone       EventType = "done"
	EventError      EventType = "error"
)
