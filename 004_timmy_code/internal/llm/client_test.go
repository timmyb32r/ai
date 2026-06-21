package llm

import (
	"context"
	"testing"

	"github.com/timmy/timmy-code/internal/rawlog"
)

func TestClientInterfaceCompileCheck(t *testing.T) {
	var client Client = &mockClient{}
	params := StreamParams{
		ModelConfig: ModelConfig{ModelName: DefaultModel, MaxTokens: DefaultMaxTokens},
	}
	ch, err := client.StreamChat(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	if ch == nil {
		t.Fatal("expected non-nil channel")
	}
	evt, ok := <-ch
	if !ok {
		t.Fatal("expected channel to be open")
	}
	if evt.Type != StreamEventStop {
		t.Errorf("expected StreamEventStop, got %s", evt.Type)
	}
	if evt.StopReason != "completed" {
		t.Errorf("expected stop reason 'completed', got '%s'", evt.StopReason)
	}
	_, ok = <-ch
	if ok {
		t.Error("expected channel to be closed")
	}
}

type mockClient struct{}

func (m *mockClient) StreamChat(_ context.Context, _ StreamParams) (<-chan StreamEvent, error) {
	return m.StreamChatWithLog(nil, StreamParams{}, nil)
}

func (m *mockClient) StreamChatWithLog(_ context.Context, _ StreamParams, _ *rawlog.RoundLogger) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent, 1)
	ch <- StreamEvent{Type: StreamEventStop, StopReason: "completed"}
	close(ch)
	return ch, nil
}
