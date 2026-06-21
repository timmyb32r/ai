package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/openai"

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
func (c *DeepSeekClient) StreamChat(ctx context.Context, params StreamParams) (<-chan StreamEvent, error) {
	msgs := toLLMMessages(params.Messages)
	opts := []llms.CallOption{
		llms.WithModel(params.ModelConfig.ModelName),
		llms.WithMaxTokens(params.ModelConfig.MaxTokens),
	}
	if len(params.Tools) > 0 {
		opts = append(opts, llms.WithTools(toLLMTools(params.Tools)))
	}

	ch := make(chan StreamEvent, 8)
	go func() {
		defer close(ch)

		opts = append(opts, llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
			select {
			case ch <- StreamEvent{Type: StreamEventTextDelta, TextDelta: string(chunk)}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}))

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
		for _, tc := range choice.ToolCalls {
			if tc.FunctionCall == nil {
				continue
			}
			payload := &ToolUsePayload{ID: tc.ID, Name: tc.FunctionCall.Name}
			if tc.FunctionCall.Arguments != "" {
				_ = json.Unmarshal([]byte(tc.FunctionCall.Arguments), &payload.Input)
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
			out = append(out, llms.MessageContent{
				Role: llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{
					llms.ToolCallResponse{
						ToolCallID: m.Name,
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
