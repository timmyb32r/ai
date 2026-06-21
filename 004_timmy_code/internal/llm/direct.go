package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/timmy/timmy-code/internal/rawlog"
)

// DirectClient implements the Client interface via direct HTTP to DeepSeek API.
type DirectClient struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewDirectClient creates a new DirectClient that calls DeepSeek's API directly.
func NewDirectClient(apiKey string) *DirectClient {
	if apiKey == "" {
		apiKey = os.Getenv("DEEPSEEK_API_KEY")
	}
	// Diagnostic: show key prefix to catch whitespace / Bearer prefix issues.
	keyLen := len(apiKey)
	prefix := apiKey
	suffix := ""
	if keyLen > 12 {
		prefix = apiKey[:7]
		suffix = apiKey[keyLen-4:]
	}
	fmt.Fprintf(os.Stderr, "[llm] API key: len=%d prefix=%q suffix=%q\n", keyLen, prefix, suffix)
	if strings.HasPrefix(apiKey, "Bearer ") {
		fmt.Fprintf(os.Stderr, "[llm] ⚠ API key has 'Bearer ' prefix — this will double-prefix the auth header!\n")
	}
	if strings.TrimSpace(apiKey) != apiKey {
		fmt.Fprintf(os.Stderr, "[llm] ⚠ API key has leading/trailing whitespace — this will break auth!\n")
	}
	return &DirectClient{
		apiKey:  apiKey,
		baseURL: "https://api.deepseek.com/v1",
		client:  http.DefaultClient,
	}
}

// OpenAI-compatible request/response types for the Chat Completions API.

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIMessage struct {
	Role       string            `json:"role"`
	Content    string            `json:"content,omitempty"`
	Name       string            `json:"name,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall  `json:"tool_calls,omitempty"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type openAIRequest struct {
	Model     string          `json:"model"`
	Messages  []openAIMessage `json:"messages"`
	Tools     []openAITool    `json:"tools,omitempty"`
	Stream    bool            `json:"stream"`
	MaxTokens int             `json:"max_completion_tokens,omitempty"`
}

type openAIStreamChoice struct {
	Delta struct {
		Content   string               `json:"content,omitempty"`
		ToolCalls []openAIToolCallDelta `json:"tool_calls,omitempty"`
	} `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

type openAIToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type openAIStreamResponse struct {
	Choices []openAIStreamChoice `json:"choices"`
	Usage   *openAIUsage         `json:"usage,omitempty"`
}

// StreamChat implements the Client interface via direct HTTP SSE streaming.
func (c *DirectClient) StreamChat(ctx context.Context, params StreamParams) (<-chan StreamEvent, error) {
	fmt.Fprintf(os.Stderr, "[llm] StreamChat called: model=%s\n", params.ModelConfig.ModelName)
	return c.StreamChatWithLog(ctx, params, nil)
}

// StreamChatWithLog is like StreamChat but optionally logs raw request/response
// to the provided RoundLogger. Pass nil to disable logging.
func (c *DirectClient) StreamChatWithLog(ctx context.Context, params StreamParams, logger *rawlog.RoundLogger) (<-chan StreamEvent, error) {
	fmt.Fprintf(os.Stderr, "[llm] StreamChatWithLog: model=%s url=%s keyLen=%d\n",
		params.ModelConfig.ModelName, c.baseURL+"/chat/completions", len(c.apiKey))

	req := openAIRequest{
		Model:     params.ModelConfig.ModelName,
		Stream:    true,
		MaxTokens: params.ModelConfig.MaxTokens,
	}
	for _, m := range params.Messages {
		oam := openAIMessage{
			Role:       m.Role,
			Content:    m.Content,
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			oatc := openAIToolCall{ID: tc.ID, Type: "function"}
			oatc.Function.Name = tc.Name
			oatc.Function.Arguments = tc.Arguments
			oam.ToolCalls = append(oam.ToolCalls, oatc)
		}
		req.Messages = append(req.Messages, oam)
	}
	for _, t := range params.Tools {
		req.Tools = append(req.Tools, openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Log the raw request body (pretty-printed) if logger is present.
	if logger != nil {
		_ = logger.LogRequest(body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	// Diagnostic: print auth header masked.
	authVal := "Bearer " + c.apiKey
	if len(authVal) > 20 {
		authVal = authVal[:14] + "..." + authVal[len(authVal)-4:]
	}
	fmt.Fprintf(os.Stderr, "[llm] → %s %s  Authorization: %s\n", httpReq.Method, httpReq.URL.String(), authVal)

	httpResp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		return nil, fmt.Errorf("API error: %s: %s", httpResp.Status, string(respBody))
	}

	ch := make(chan StreamEvent, 8)
	go func() {
		defer close(ch)
		defer httpResp.Body.Close()
		// Close logger when stream is done (success or error).
		if logger != nil {
			defer logger.CloseResponse()
		}

		type pendingToolCall struct {
			index     int
			id        string
			name      string
			arguments string
		}
		var pendingToolCalls []*pendingToolCall
			var lastUsage *TokenUsage


		scanner := bufio.NewScanner(httpResp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}

			// Log the raw SSE data chunk (JSON content only, no SSE wrapper).
			if logger != nil {
				_ = logger.LogResponseLine([]byte(data))
			}

			var chunk openAIStreamResponse
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}
			if len(chunk.Choices) == 0 {
				continue
			}
			choice := chunk.Choices[0]

			if choice.Delta.Content != "" {
				ch <- StreamEvent{Type: StreamEventTextDelta, TextDelta: choice.Delta.Content}
			}

			for _, tc := range choice.Delta.ToolCalls {
				var p *pendingToolCall
				for _, pt := range pendingToolCalls {
					if pt.index == tc.Index {
						p = pt
						break
					}
				}
				if p == nil {
					p = &pendingToolCall{index: tc.Index}
					pendingToolCalls = append(pendingToolCalls, p)
				}
				if tc.ID != "" {
					p.id = tc.ID
				}
				if tc.Function.Name != "" {
					p.name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					p.arguments += tc.Function.Arguments
				}
			}

				// Capture usage from any chunk that provides it
				if chunk.Usage != nil {
					lastUsage = &TokenUsage{
						PromptTokens:     chunk.Usage.PromptTokens,
						CompletionTokens: chunk.Usage.CompletionTokens,
						TotalTokens:      chunk.Usage.TotalTokens,
					}
				}

			if choice.FinishReason != nil && *choice.FinishReason != "" {
				for _, pt := range pendingToolCalls {
					payload := &ToolUsePayload{ID: pt.id, Name: pt.name, Input: make(map[string]any)}
					if pt.arguments != "" {
						_ = json.Unmarshal([]byte(pt.arguments), &payload.Input)
					}
					ch <- StreamEvent{Type: StreamEventToolUse, ToolUse: payload}
				}
				ch <- StreamEvent{Type: StreamEventStop, StopReason: *choice.FinishReason, Usage: lastUsage}
				return
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- StreamEvent{Type: StreamEventStop, StopReason: fmt.Sprintf("stream error: %v", err)}
		}
	}()
	return ch, nil
}
