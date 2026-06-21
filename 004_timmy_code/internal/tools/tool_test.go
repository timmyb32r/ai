package tools

import (
	"context"
	"testing"
)

func TestNewRegistryEmpty(t *testing.T) {
	r := NewRegistry()
	if r == nil {
		t.Fatal("NewRegistry returned nil")
	}
	tools := r.GetAll()
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
	_, ok := r.FindByName("nonexistent")
	if ok {
		t.Error("expected false for nonexistent tool")
	}
	defs := r.GetAllDefs()
	if len(defs) != 0 {
		t.Errorf("expected 0 defs, got %d", len(defs))
	}
}

type mockTool struct{}

func (m *mockTool) Name() string        { return "mock" }
func (m *mockTool) Description() string { return "mock tool for testing" }
func (m *mockTool) InputSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (m *mockTool) Call(_ context.Context, _ map[string]any) (*Result, error) {
	return &Result{Output: "ok", ExitCode: 0}, nil
}

func TestRegistryWithTool(t *testing.T) {
	r := NewRegistry(&mockTool{})
	tools := r.GetAll()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tool, ok := r.FindByName("mock")
	if !ok {
		t.Fatal("expected to find mock tool")
	}
	if tool.Name() != "mock" {
		t.Errorf("expected name 'mock', got '%s'", tool.Name())
	}
	defs := r.GetAllDefs()
	if len(defs) != 1 {
		t.Fatalf("expected 1 def, got %d", len(defs))
	}
	if defs[0].Name != "mock" {
		t.Errorf("expected def name 'mock', got '%s'", defs[0].Name)
	}
	if defs[0].Description != "mock tool for testing" {
		t.Errorf("expected def description 'mock tool for testing', got '%s'", defs[0].Description)
	}
}

func TestMockToolCall(t *testing.T) {
	tool := &mockTool{}
	result, err := tool.Call(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output != "ok" {
		t.Errorf("expected output 'ok', got '%s'", result.Output)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
}
