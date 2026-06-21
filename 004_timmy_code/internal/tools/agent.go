package tools

import (
	"context"
	"fmt"

	"github.com/timmy/timmy-code/internal/agent"
	"github.com/timmy/timmy-code/internal/llm"
	"github.com/timmy/timmy-code/internal/prompts"
)

// AgentTool spawns a typed subagent (planner, architect, critic, executor, etc.)
type AgentTool struct {
	client llm.Client
}

// NewAgentTool creates an AgentTool backed by the given LLM client.
func NewAgentTool(client llm.Client) *AgentTool {
	return &AgentTool{client: client}
}

func (t *AgentTool) Name() string { return "Agent" }

func (t *AgentTool) Description() string {
	return "Spawn a typed subagent. Available types: planner, architect, critic, executor, analyst, code-reviewer, verifier. Each has a specialized system prompt and behavioral rules."
}

func (t *AgentTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"description": "The task for the subagent",
			},
			"agentType": map[string]any{
				"type":        "string",
				"description": "Agent type: planner, architect, critic, executor, analyst, code-reviewer, verifier. Defaults to executor.",
				"enum":        []string{"planner", "architect", "critic", "executor", "analyst", "code-reviewer", "verifier"},
			},
		},
		"required": []any{"prompt"},
	}
}

func (t *AgentTool) Call(ctx context.Context, input map[string]any) (*Result, error) {
	prompt, ok := input["prompt"].(string)
	if !ok || prompt == "" {
		return &Result{ExitCode: 1, Output: "missing required 'prompt' field"}, fmt.Errorf("missing required 'prompt' field")
	}

	agentType := "executor"
	if at, ok := input["agentType"].(string); ok && at != "" {
		agentType = at
	}

	// Resolve system prompt and model for this agent type
	agentPrompt, hasPrompt := prompts.AgentPrompts[agentType]
	if !hasPrompt {
		return nil, fmt.Errorf("unknown agent type: %s (valid: planner, architect, critic, executor, analyst, code-reviewer, verifier)", agentType)
	}

	modelName := llm.DefaultModel
	if m, ok := prompts.AgentModels[agentType]; ok {
		modelName = m
	}

	output, err := agent.RunAgent(ctx, t.client, agentPrompt, prompt, modelName)
	if err != nil {
		return &Result{ExitCode: 1, Output: fmt.Sprintf("agent error: %v", err)}, err
	}

	return &Result{Output: output, ExitCode: 0}, nil
}
