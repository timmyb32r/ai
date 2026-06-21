package agent

import (
	"context"
	"fmt"

	"github.com/timmy/timmy-code/internal/llm"
	"github.com/timmy/timmy-code/internal/schema"
)

// RunAgent spawns a subagent in a goroutine with a specialized system prompt.
// The system prompt defines the agent's identity and rules; the task is the user's request.
func RunAgent(ctx context.Context, client llm.Client, systemPrompt, task, modelName string) (string, error) {
	params := llm.StreamParams{
		Messages: []schema.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: task},
		},
		ModelConfig: llm.ModelConfig{
			ModelName: modelName,
			MaxTokens: llm.DefaultMaxTokens,
		},
	}

	resultCh := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		stream, err := client.StreamChat(ctx, params)
		if err != nil {
			errCh <- fmt.Errorf("stream chat: %w", err)
			return
		}

		var result string
		for event := range stream {
			switch event.Type {
			case llm.StreamEventTextDelta:
				result += event.TextDelta
			case llm.StreamEventStop:
				resultCh <- result
				return
			}
		}
		// Channel closed without Stop event — send whatever we have.
		resultCh <- result
	}()

	select {
	case r := <-resultCh:
		return r, nil
	case err := <-errCh:
		return "", err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
