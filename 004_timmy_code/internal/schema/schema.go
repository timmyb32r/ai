package schema

// ToolDef describes a tool for LLM function calling.
type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON Schema
}

// Message represents a chat message.
type Message struct {
	Role    string // "system" | "user" | "assistant" | "tool"
	Content string
	Name    string // tool name for tool results
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
