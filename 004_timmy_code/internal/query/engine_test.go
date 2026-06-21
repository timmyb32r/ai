package query

import (
	"context"
	"testing"

	ctxpkg "github.com/timmy/timmy-code/internal/context"
	"github.com/timmy/timmy-code/internal/llm"
	"github.com/timmy/timmy-code/internal/schema"
	"github.com/timmy/timmy-code/internal/tools"
)

// mockLLM implements llm.Client for testing.
type mockLLM struct{}

func (m *mockLLM) StreamChat(_ context.Context, _ llm.StreamParams) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, 2)
	ch <- llm.StreamEvent{Type: llm.StreamEventStop, StopReason: "completed"}
	close(ch)
	return ch, nil
}

// mockCtxSvc implements context.Service for testing.
type mockCtxSvc struct{}

func (m *mockCtxSvc) GetUserContext(_ string) (*ctxpkg.UserContext, error) {
	return &ctxpkg.UserContext{TIMMYmd: "test context", CurrentDate: "2026-06-21"}, nil
}

func (m *mockCtxSvc) GetSystemContext(_ string) (*ctxpkg.SystemContext, error) {
	return &ctxpkg.SystemContext{
		GitBranch: "main", GitStatus: "clean", RecentCommits: "commit 1\ncommit 2",
	}, nil
}

func TestQueryEngineCompileCheck(t *testing.T) {
	cfg := Config{
		Tools:        tools.NewRegistry(),
		LLMClient:    &mockLLM{},
		CtxService:   &mockCtxSvc{},
		ModelCfg:     llm.ModelConfig{ModelName: llm.DefaultModel, MaxTokens: llm.DefaultMaxTokens},
		WorkDir:      ".",
		SystemPrompt: "You are a helpful assistant.",
	}
	engine := New(cfg)
	if engine == nil {
		t.Fatal("New returned nil")
	}

	ctx := context.Background()
	eventCh := engine.SubmitMessage(ctx, "hello")
	if eventCh == nil {
		t.Fatal("SubmitMessage returned nil channel")
	}

	var events []Event
	for ev := range eventCh {
		events = append(events, ev)
	}
	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}

	last := events[len(events)-1]
	if last.Type != EventDone {
		t.Errorf("expected final event to be Done, got %s", last.Type)
	}

	if err := engine.Close(); err != nil {
		t.Errorf("Close returned error: %v", err)
	}
}

func TestQueryEngineEventTypes(t *testing.T) {
	if EventTextDelta != schema.EventTextDelta {
		t.Error("EventTextDelta mismatch")
	}
	if EventToolCall != schema.EventToolCall {
		t.Error("EventToolCall mismatch")
	}
	if EventToolResult != schema.EventToolResult {
		t.Error("EventToolResult mismatch")
	}
	if EventDone != schema.EventDone {
		t.Error("EventDone mismatch")
	}
	if EventError != schema.EventError {
		t.Error("EventError mismatch")
	}
}

func TestQueryEngineCancelContext(t *testing.T) {
	cfg := Config{
		Tools:        tools.NewRegistry(),
		LLMClient:    &mockLLM{},
		CtxService:   &mockCtxSvc{},
		ModelCfg:     llm.ModelConfig{ModelName: llm.DefaultModel, MaxTokens: llm.DefaultMaxTokens},
		WorkDir:      ".",
		SystemPrompt: "You are a helpful assistant.",
	}
	engine := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	eventCh := engine.SubmitMessage(ctx, "hello")
	var events []Event
	for ev := range eventCh {
		events = append(events, ev)
	}
	if len(events) == 0 {
		t.Fatal("expected at least one event for cancelled context")
	}
	// should get an error event
	foundError := false
	for _, ev := range events {
		if ev.Type == EventError {
			foundError = true
			break
		}
	}
	if !foundError {
		t.Error("expected Error event for cancelled context, got events:", len(events))
	}
}
