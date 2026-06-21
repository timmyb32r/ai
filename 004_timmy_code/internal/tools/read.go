package tools

import (
	"context"
	"fmt"
	"os"
)

// ReadTool reads file contents.
type ReadTool struct{}

func (t *ReadTool) Name() string        { return "Read" }
func (t *ReadTool) Description() string { return "Read the contents of a file" }
func (t *ReadTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The absolute path to the file to read",
			},
		},
		"required": []string{"file_path"},
	}
}

func (t *ReadTool) Call(ctx context.Context, input map[string]any) (*Result, error) {
	path, ok := input["file_path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("file_path is required and must be a string")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	return &Result{
		Output:   string(data),
		ExitCode: 0,
	}, nil
}
