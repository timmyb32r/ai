package tools

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// EditTool performs exact string replacement in a file.
type EditTool struct{}

func (t *EditTool) Name() string        { return "Edit" }
func (t *EditTool) Description() string { return "Replace a string in a file with exact matching" }
func (t *EditTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The absolute path to the file to edit",
			},
			"old_string": map[string]any{
				"type":        "string",
				"description": "The exact text to replace",
			},
			"new_string": map[string]any{
				"type":        "string",
				"description": "The replacement text",
			},
		},
		"required": []string{"file_path", "old_string", "new_string"},
	}
}

func (t *EditTool) Call(ctx context.Context, input map[string]any) (*Result, error) {
	path, ok := input["file_path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("file_path is required")
	}
	oldStr, ok := input["old_string"].(string)
	if !ok {
		return nil, fmt.Errorf("old_string is required")
	}
	newStr, _ := input["new_string"].(string)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	content := string(data)
	count := strings.Count(content, oldStr)
	if count == 0 {
		return nil, fmt.Errorf("old_string not found in file")
	}
	if count > 1 {
		return nil, fmt.Errorf("old_string appears %d times — must be unique", count)
	}

	newContent := strings.Replace(content, oldStr, newStr, 1)
	if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	return &Result{
		Output:   fmt.Sprintf("File edited: %s (1 replacement)", path),
		ExitCode: 0,
	}, nil
}
