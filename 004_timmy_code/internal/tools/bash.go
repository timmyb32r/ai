package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// BashTool executes shell commands.
type BashTool struct{}

func (t *BashTool) Name() string        { return "Bash" }
func (t *BashTool) Description() string { return "Execute a shell command in the current working directory" }
func (t *BashTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command to execute",
			},
			"timeout": map[string]any{
				"type":        "integer",
				"description": "Optional timeout in seconds (default 120)",
			},
		},
		"required": []string{"command"},
	}
}

func (t *BashTool) Call(ctx context.Context, input map[string]any) (*Result, error) {
	cmdStr, ok := input["command"].(string)
	if !ok {
		return nil, fmt.Errorf("command must be a string")
	}
	if cmdStr == "" {
		return nil, fmt.Errorf("command is required")
	}

	timeout := 120
	if t, ok := input["timeout"].(float64); ok {
		timeout = int(t)
	}

	cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "sh", "-c", cmdStr)
	output, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
			err = nil // non-zero exit is not a Go error
		}
	}

	if cmdCtx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("command timed out after %d seconds", timeout)
	}

	return &Result{
		Output:   strings.TrimSpace(string(output)),
		ExitCode: exitCode,
	}, err
}
