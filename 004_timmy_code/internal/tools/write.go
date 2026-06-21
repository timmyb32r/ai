package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// WriteTool creates or overwrites a file.
type WriteTool struct{}

func (t *WriteTool) Name() string        { return "Write" }
func (t *WriteTool) Description() string { return "Create or overwrite a file with the given content" }
func (t *WriteTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The absolute path to the file to write",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "The content to write to the file",
			},
		},
		"required": []string{"file_path", "content"},
	}
}

func (t *WriteTool) Call(ctx context.Context, input map[string]any) (*Result, error) {
	path, ok := input["file_path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("file_path is required and must be a string")
	}
	content, ok := input["content"].(string)
	if !ok {
		return nil, fmt.Errorf("content is required and must be a string")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	return &Result{
		Output:   fmt.Sprintf("File written: %s (%d bytes)", path, len(content)),
		ExitCode: 0,
	}, nil
}
