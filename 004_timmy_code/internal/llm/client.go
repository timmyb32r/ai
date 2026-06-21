package llm

import (
	"context"

	"github.com/timmy/timmy-code/internal/schema"
)

// StreamEventType categorizes stream events.
type StreamEventType string

const (
	StreamEventTextDelta StreamEventType = "text_delta"
	StreamEventToolUse   StreamEventType = "tool_use"
	StreamEventStop      StreamEventType = "stop"
)

// ToolUsePayload carries tool call details from the LLM.
type ToolUsePayload struct {
	ID    string
	Name  string
	Input map[string]any
}

// TokenUsage carries API token counts.
type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// StreamEvent is emitted during streaming responses.
type StreamEvent struct {
	Type       StreamEventType
	TextDelta  string
	ToolUse    *ToolUsePayload
	StopReason string
	Usage      *TokenUsage // populated on Stop event
}

// StreamParams configures a streaming chat request.
type StreamParams struct {
	Messages    []schema.Message
	Tools       []schema.ToolDef
	ModelConfig ModelConfig
}

// Client is the interface for LLM API calls.
type Client interface {
	StreamChat(ctx context.Context, params StreamParams) (<-chan StreamEvent, error)
}
