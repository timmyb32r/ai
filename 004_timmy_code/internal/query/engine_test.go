package query

import (
	"context"
	"testing"

	ctxpkg "github.com/timmy/timmy-code/internal/context"
	"github.com/timmy/timmy-code/internal/llm"
	"github.com/timmy/timmy-code/internal/rawlog"
	"github.com/timmy/timmy-code/internal/schema"
	"github.com/timmy/timmy-code/internal/tools"
)

// mockLLM implements llm.Client for testing.
type mockLLM struct{}

func (m *mockLLM) StreamChat(_ context.Context, _ llm.StreamParams) (<-chan llm.StreamEvent, error) {
	return m.StreamChatWithLog(nil, llm.StreamParams{}, nil)
}

func (m *mockLLM) StreamChatWithLog(_ context.Context, _ llm.StreamParams, _ *rawlog.RoundLogger) (<-chan llm.StreamEvent, error) {
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

func TestQueryEngineMessageAccumulation(t *testing.T) {
	// Verifies that e.messages grows across SubmitMessage calls — context is preserved.
	cfg := Config{
		Tools:        tools.NewRegistry(),
		LLMClient:    &mockLLM{},
		CtxService:   &mockCtxSvc{},
		ModelCfg:     llm.ModelConfig{ModelName: llm.DefaultModel, MaxTokens: llm.DefaultMaxTokens},
		WorkDir:      ".",
		SystemPrompt: "You are test.",
	}
	engine := New(cfg)

	// Turn 1
	ch1 := engine.SubmitMessage(context.Background(), "turn 1")
	for range ch1 {
	}
	msgCount1 := len(engine.messages)
	t.Logf("After turn 1: %d messages", msgCount1)
	if msgCount1 < 2 {
		t.Fatalf("expected at least system+user+assistant messages, got %d", msgCount1)
	}

	// Turn 2
	ch2 := engine.SubmitMessage(context.Background(), "turn 2")
	for range ch2 {
	}
	msgCount2 := len(engine.messages)
	t.Logf("After turn 2: %d messages", msgCount2)
	if msgCount2 <= msgCount1 {
		t.Fatalf("CONTEXT LOST: messages did not grow from turn 1 (%d) to turn 2 (%d)", msgCount1, msgCount2)
	}
	if msgCount2 != msgCount1+2 {
		t.Errorf("expected +2 messages (user+assistant), got %d -> %d", msgCount1, msgCount2)
	}

	// Verify system prompt only injected once
	sysCount := 0
	for _, m := range engine.messages {
		if m.Role == "system" {
			sysCount++
		}
	}
	if sysCount != 1 {
		t.Errorf("expected exactly 1 system message, got %d", sysCount)
	}

	// Turn 3
	ch3 := engine.SubmitMessage(context.Background(), "turn 3")
	for range ch3 {
	}
	msgCount3 := len(engine.messages)
	if msgCount3 <= msgCount2 {
		t.Fatalf("CONTEXT LOST: messages did not grow from turn 2 (%d) to turn 3 (%d)", msgCount2, msgCount3)
	}
	t.Logf("After turn 3: %d messages — context preserved ✓", msgCount3)
	engine.Close()
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
