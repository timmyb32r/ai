package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/openai"

	"github.com/timmy/timmy-code/internal/rawlog"
	"github.com/timmy/timmy-code/internal/schema"
)

// DeepSeekClient implements the Client interface using langchaingo with DeepSeek.
type DeepSeekClient struct {
	llm *openai.LLM
}

// NewDeepSeekClient creates a new DeepSeekClient backed by langchaingo/openai.
func NewDeepSeekClient(apiKey string) (*DeepSeekClient, error) {
	if apiKey == "" {
		apiKey = os.Getenv("DEEPSEEK_API_KEY")
	}
	llm, err := openai.New(
		openai.WithBaseURL("https://api.deepseek.com/v1"),
		openai.WithToken(apiKey),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create DeepSeek client: %w", err)
	}
	return &DeepSeekClient{llm: llm}, nil
}

// StreamChat implements the Client interface.
// Uses GenerateContent (non-streaming for tool call reliability) then emits
// the full text content followed by any tool calls.
func (c *DeepSeekClient) StreamChat(ctx context.Context, params StreamParams) (<-chan StreamEvent, error) {
	return c.StreamChatWithLog(ctx, params, nil)
}

// StreamChatWithLog is like StreamChat but logs raw I/O via logger if non-nil.
// Note: langchaingo path does not expose raw HTTP body, so logging is limited.
func (c *DeepSeekClient) StreamChatWithLog(ctx context.Context, params StreamParams, logger *rawlog.RoundLogger) (<-chan StreamEvent, error) {
	msgs := toLLMMessages(params.Messages)
	opts := []llms.CallOption{
		llms.WithModel(params.ModelConfig.ModelName),
		llms.WithMaxTokens(params.ModelConfig.MaxTokens),
	}
	if len(params.Tools) > 0 {
		opts = append(opts, llms.WithTools(toLLMTools(params.Tools)))
	}

	// Log request if logger is present (limited: we log what we have).
	if logger != nil {
		reqData, _ := json.Marshal(map[string]any{
			"model":      params.ModelConfig.ModelName,
			"messages":   msgs,
			"tools":      params.Tools,
			"max_tokens": params.ModelConfig.MaxTokens,
		})
		_ = logger.LogRequest(reqData)
	}

	ch := make(chan StreamEvent, 8)
	go func() {
		defer close(ch)
		if logger != nil {
			defer logger.CloseResponse()
		}

		resp, err := c.llm.GenerateContent(ctx, msgs, opts...)
		if err != nil {
			select {
			case ch <- StreamEvent{Type: StreamEventStop, StopReason: fmt.Sprintf("error: %v", err)}:
			case <-ctx.Done():
			}
			return
		}
		if resp == nil || len(resp.Choices) == 0 {
			ch <- StreamEvent{Type: StreamEventStop, StopReason: "empty"}
			return
		}

		choice := resp.Choices[0]

		// Emit text content
		if choice.Content != "" {
			ch <- StreamEvent{Type: StreamEventTextDelta, TextDelta: choice.Content}
			// Log response content.
			if logger != nil {
				respData, _ := json.Marshal(map[string]any{"content": choice.Content})
				_ = logger.LogResponseLine(respData)
			}
		}

		// Emit tool calls
		for _, tc := range choice.ToolCalls {
			if tc.FunctionCall == nil {
				continue
			}
			payload := &ToolUsePayload{ID: tc.ID, Name: tc.FunctionCall.Name, Input: make(map[string]any)}
			if tc.FunctionCall.Arguments != "" {
				if err := json.Unmarshal([]byte(tc.FunctionCall.Arguments), &payload.Input); err != nil {
					payload.Input["_raw"] = tc.FunctionCall.Arguments
				}
			}
			ch <- StreamEvent{Type: StreamEventToolUse, ToolUse: payload}
		}
		ch <- StreamEvent{Type: StreamEventStop, StopReason: choice.StopReason}
	}()
	return ch, nil
}

// toLLMMessages converts schema.Message slice to langchaingo MessageContent slice.
func toLLMMessages(msgs []schema.Message) []llms.MessageContent {
	out := make([]llms.MessageContent, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "system":
			out = append(out, llms.TextParts(llms.ChatMessageTypeSystem, m.Content))
		case "user":
			out = append(out, llms.TextParts(llms.ChatMessageTypeHuman, m.Content))
		case "assistant":
			out = append(out, llms.TextParts(llms.ChatMessageTypeAI, m.Content))
		case "tool":
			tcID := m.ToolCallID
			if tcID == "" {
				tcID = m.Name
			}
			out = append(out, llms.MessageContent{
				Role: llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{
					llms.ToolCallResponse{
						ToolCallID: tcID,
						Name:       m.Name,
						Content:    m.Content,
					},
				},
			})
		default:
			// fallback to user role
			out = append(out, llms.TextParts(llms.ChatMessageTypeHuman, m.Content))
		}
	}
	return out
}

// toLLMTools converts schema.ToolDef slice to langchaingo Tool slice.
func toLLMTools(defs []schema.ToolDef) []llms.Tool {
	out := make([]llms.Tool, 0, len(defs))
	for _, d := range defs {
		out = append(out, llms.Tool{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        d.Name,
				Description: d.Description,
				Parameters:  d.Parameters,
			},
		})
	}
	return out
}
