package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/timmy/timmy-code/internal/llm"
)

// mockClient implements llm.Client for testing.
type mockClient struct {
	events []llm.StreamEvent
	err    error
}

func (m *mockClient) StreamChat(_ context.Context, _ llm.StreamParams) (<-chan llm.StreamEvent, error) {
	if m.err != nil {
		return nil, m.err
	}
	ch := make(chan llm.StreamEvent, len(m.events))
	for _, e := range m.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func TestRunAgent_TextResponse(t *testing.T) {
	client := &mockClient{
		events: []llm.StreamEvent{
			{Type: llm.StreamEventTextDelta, TextDelta: "Hello, "},
			{Type: llm.StreamEventTextDelta, TextDelta: "world!"},
			{Type: llm.StreamEventStop, StopReason: "end_turn"},
		},
	}

	ctx := context.Background()
	result, err := RunAgent(ctx, client, "You are a test agent.", "test prompt", llm.FastModel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Hello, world!" {
		t.Errorf("got %q, want %q", result, "Hello, world!")
	}
}

func TestRunAgent_EmptyResponse(t *testing.T) {
	client := &mockClient{
		events: []llm.StreamEvent{
			{Type: llm.StreamEventStop},
		},
	}

	ctx := context.Background()
	result, err := RunAgent(ctx, client, "You are a test agent.", "prompt", llm.FastModel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty, got %q", result)
	}
}

func TestRunAgent_ChannelClose(t *testing.T) {
	client := &mockClient{
		events: []llm.StreamEvent{
			{Type: llm.StreamEventTextDelta, TextDelta: "only"},
		},
	}

	ctx := context.Background()
	result, err := RunAgent(ctx, client, "You are a test agent.", "prompt", llm.FastModel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "only" {
		t.Errorf("got %q, want %q", result, "only")
	}
}

func TestRunAgent_StreamError(t *testing.T) {
	client := &mockClient{
		err: errors.New("api failure"),
	}

	ctx := context.Background()
	_, err := RunAgent(ctx, client, "You are a test agent.", "prompt", llm.FastModel)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRunAgent_CancelledContext(t *testing.T) {
	client := &mockClient{
		events: []llm.StreamEvent{
			{Type: llm.StreamEventTextDelta, TextDelta: "partial"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := RunAgent(ctx, client, "You are a test agent.", "prompt", llm.FastModel)
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestRunAgent_Timeout(t *testing.T) {
	// A client that never sends anything and never closes.
	client := &llmClientHanging{}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := RunAgent(ctx, client, "You are a test agent.", "prompt", llm.FastModel)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

// llmClientHanging never sends events and never closes its channel.
type llmClientHanging struct{}

func (h *llmClientHanging) StreamChat(ctx context.Context, _ llm.StreamParams) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

// compile-time interface check
var _ llm.Client = (*mockClient)(nil)
var _ llm.Client = (*llmClientHanging)(nil)
