package query

import (
	"context"
	"fmt"
	"strings"

	"github.com/timmy/timmy-code/internal/llm"
	"github.com/timmy/timmy-code/internal/schema"
)

type toolCall struct {
	name  string
	id    string
	input map[string]any
}

// queryLoop is the core agentic loop executed in a goroutine.
// It assembles context, calls the LLM, relays stream events, executes tools,
// and repeats until no more tool calls are requested or an error occurs.
func (e *QueryEngine) queryLoop(ctx context.Context, userInput string, ch chan<- Event) {
	defer close(ch)

	// assemble context
	uc, _ := e.ctxService.GetUserContext(e.workDir)
	sc, _ := e.ctxService.GetSystemContext(e.workDir)

	// build system message with context
	sysMsg := e.systemPrompt
	if uc != nil {
		sysMsg += fmt.Sprintf("\n\n# Context\n- Current date: %s\n- TIMMY.md:\n%s\n", uc.CurrentDate, uc.TIMMYmd)
	}
	if sc != nil {
		sysMsg += fmt.Sprintf("\n# Git Status\nBranch: %s\nStatus:\n%s\nRecent:\n%s\n", sc.GitBranch, sc.GitStatus, sc.RecentCommits)
	}
	sysMsg += "\n\n# Available Tools\n" + e.formatToolDefs()

	messages := []schema.Message{
		{Role: "system", Content: sysMsg},
		{Role: "user", Content: userInput},
	}

	round := 0
	stream := true
	for stream {
		select {
		case <-ctx.Done():
			ch <- Event{Type: EventError, Round: round, Error: ctx.Err()}
			return
		default:
		}

		// call LLM
		params := llm.StreamParams{
			Messages:    messages,
			Tools:       e.tools.GetAllDefs(),
			ModelConfig: e.modelCfg,
		}
		eventCh, err := e.llmClient.StreamChat(ctx, params)
		if err != nil {
			ch <- Event{Type: EventError, Round: round, Error: err}
			return
		}

		var toolCalls []toolCall
		var textBuf strings.Builder

		for ev := range eventCh {
			switch ev.Type {
			case "text_delta":
				textBuf.WriteString(ev.TextDelta)
				ch <- Event{Type: EventTextDelta, Round: round, Text: ev.TextDelta}
			case "tool_use":
				toolCalls = append(toolCalls, toolCall{
					name: ev.ToolUse.Name, id: ev.ToolUse.ID, input: ev.ToolUse.Input,
				})
				ch <- Event{Type: EventToolCall, Round: round, ToolUse: &ToolUseEvent{
					Name: ev.ToolUse.Name, Input: ev.ToolUse.Input,
				}}
			}
		}

		// add assistant message
		messages = append(messages, schema.Message{Role: "assistant", Content: textBuf.String()})

		// if no tool calls, we're done
		if len(toolCalls) == 0 {
			ch <- Event{Type: EventDone, Round: round}
			return
		}

		// execute tools
		for _, tc := range toolCalls {
			tool, found := e.tools.FindByName(tc.name)
			if !found {
				ch <- Event{Type: EventError, Round: round, Error: fmt.Errorf("tool not found: %s", tc.name)}
				return
			}
			result, err := tool.Call(ctx, tc.input)
			if err != nil {
				messages = append(messages, schema.Message{
					Role: "tool", Content: fmt.Sprintf("Error: %v", err), Name: tc.name,
				})
				continue
			}
			ch <- Event{Type: EventToolResult, Round: round, Text: result.Output}
			messages = append(messages, schema.Message{
				Role: "tool", Content: result.Output, Name: tc.name,
			})
		}
		round++
	}
}

func (e *QueryEngine) formatToolDefs() string {
	var b strings.Builder
	for _, t := range e.tools.GetAll() {
		b.WriteString(fmt.Sprintf("- %s: %s\n", t.Name(), t.Description()))
	}
	return b.String()
}
