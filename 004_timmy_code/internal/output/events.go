// Package output defines the client-server protocol and rendering abstractions.
// The server (inside Docker) generates OutputEvents; the client (on host)
// renders them with full terminal capabilities.
package output

import "encoding/json"

// EventType identifies the kind of output event.
type EventType string

const (
	EventBanner     EventType = "banner"
	EventInfo       EventType = "info"
	EventThinking   EventType = "thinking"
	EventTextDelta  EventType = "text_delta"
	EventToolCall   EventType = "tool_call"
	EventToolResult EventType = "tool_result"
	EventDone       EventType = "done"
	EventError      EventType = "error"
)

// OutputEvent is the unified output structure sent over the wire in client-server
// mode, and consumed by Writers in both modes.
type OutputEvent struct {
	Type      EventType  `json:"type"`
	Text      string     `json:"text,omitempty"`
	Name      string     `json:"name,omitempty"`      // tool name (for tool_call)
	Detail    string     `json:"detail,omitempty"`    // tool detail (for tool_call)
	Truncated bool       `json:"truncated,omitempty"` // tool_result truncated
	Active    bool       `json:"active,omitempty"`    // thinking spinner on/off
	Usage     *UsageInfo `json:"usage,omitempty"`     // token counts (for done)
	ElapsedMs int64      `json:"elapsed_ms,omitempty"` // total time (for done)
	Input     any        `json:"input,omitempty"`     // raw tool input (for tool_call)
}

// UsageInfo carries token usage from the LLM API.
type UsageInfo struct {
	PromptTokens     int `json:"prompt"`
	CompletionTokens int `json:"completion"`
}

// ToJSON marshals the event to a single JSON line (no trailing newline).
func (e *OutputEvent) ToJSON() ([]byte, error) {
	return json.Marshal(e)
}

// ToJSONLine marshals the event to a JSON line terminated with \n.
func (e *OutputEvent) ToJSONLine() ([]byte, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// --- Client → Server commands ---

// Command is sent from client to server.
type Command struct {
	Type  string `json:"type"`            // "prompt", "cancel", "switch_model"
	Text  string `json:"text,omitempty"`  // user input (for prompt)
	Model string `json:"model,omitempty"` // model name (for switch_model)
}

// Command types sent by the client.
const (
	CmdPrompt       = "prompt"
	CmdCancel       = "cancel"
	CmdSwitchModel  = "switch_model"
)
