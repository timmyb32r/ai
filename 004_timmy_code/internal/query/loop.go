package query

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/timmy/timmy-code/internal/llm"
	"github.com/timmy/timmy-code/internal/rawlog"
	"github.com/timmy/timmy-code/internal/schema"
)

type toolCall struct {
	name  string
	id    string
	input map[string]any
}

func (e *QueryEngine) queryLoop(ctx context.Context, userInput string, ch chan<- Event) {
	defer close(ch)

	// Create a message logger for this SubmitMessage if raw logging is enabled.
	if e.sessionLogger != nil {
		e.msgLogger = e.sessionLogger.NewMessage()
	}

	// On first turn: inject system prompt + context + tools.
	// On subsequent turns: just append the user message.
	if len(e.messages) == 0 {
		uc, _ := e.ctxService.GetUserContext(e.workDir)
		sc, _ := e.ctxService.GetSystemContext(e.workDir)

		sysMsg := e.systemPrompt
		if uc != nil {
			sysMsg += fmt.Sprintf("\n\n# Context\n- Current date: %s\n- Working directory: %s\n- TIMMY.md:\n%s\n",
				uc.CurrentDate, e.workDir, uc.TIMMYmd)
		}
		if sc != nil {
			sysMsg += fmt.Sprintf("\n# Git Status\nBranch: %s\nStatus:\n%s\nRecent:\n%s\n",
				sc.GitBranch, sc.GitStatus, sc.RecentCommits)
		}
		sysMsg += "\n\n# Available Tools\n" + e.formatToolDefs()
		e.messages = append(e.messages, schema.Message{Role: "system", Content: sysMsg})
	}
	e.messages = append(e.messages, schema.Message{Role: "user", Content: userInput})

	round := 0
	for {
		select {
		case <-ctx.Done():
			ch <- Event{Type: EventError, Round: round, Error: ctx.Err()}
			return
		default:
		}

		// Create a round logger for this LLM call if logging is enabled.
		var roundLogger *rawlog.RoundLogger
		if e.msgLogger != nil {
			e.roundLogger = e.msgLogger.NewRound()
			roundLogger = e.roundLogger
		}

		params := llm.StreamParams{
			Messages:    e.messages,
			Tools:       e.tools.GetAllDefs(),
			ModelConfig: e.modelCfg,
		}

		var eventCh <-chan llm.StreamEvent
		var err error
		if roundLogger != nil {
			eventCh, err = e.llmClient.StreamChatWithLog(ctx, params, roundLogger)
		} else {
			eventCh, err = e.llmClient.StreamChat(ctx, params)
		}
		if err != nil {
			ch <- Event{Type: EventError, Round: round, Error: err}
			return
		}

		var toolCalls []toolCall
		var textBuf strings.Builder
		var lastUsage *TokenUsage

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
			case "stop":
				if ev.Usage != nil {
					lastUsage = &TokenUsage{
						PromptTokens:     ev.Usage.PromptTokens,
						CompletionTokens: ev.Usage.CompletionTokens,
						TotalTokens:      ev.Usage.TotalTokens,
					}
				}
			}
		}

		// Build assistant message with tool calls if any.
		assistantMsg := schema.Message{Role: "assistant", Content: textBuf.String()}
		for _, tc := range toolCalls {
			args, _ := json.Marshal(tc.input)
			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, schema.ToolCall{
				ID: tc.id, Name: tc.name, Arguments: string(args),
			})
		}
		e.messages = append(e.messages, assistantMsg)

		if len(toolCalls) == 0 {
			ch <- Event{Type: EventDone, Round: round, Usage: lastUsage}
			return
		}

		for _, tc := range toolCalls {
			tool, found := e.tools.FindByName(tc.name)
			if !found {
				ch <- Event{Type: EventError, Round: round, Error: fmt.Errorf("tool not found: %s", tc.name)}
				return
			}
			// Inject round logger into context so sub-agents can use it.
			toolCtx := ctx
			if e.roundLogger != nil {
				toolCtx = rawlog.WithRoundLogger(ctx, e.roundLogger)
			}
			result, err := tool.Call(toolCtx, tc.input)
			toolMsg := schema.Message{Role: "tool", Name: tc.name, ToolCallID: tc.id}
			if err != nil {
				errMsg := fmt.Sprintf("Error: %v", err)
				ch <- Event{Type: EventToolResult, Round: round, Text: errMsg}
				toolMsg.Content = errMsg
				e.messages = append(e.messages, toolMsg)
				continue
			}
			resultText := result.Output
			if resultText == "" {
				resultText = fmt.Sprintf("%s: ok (exit=%d)", tc.name, result.ExitCode)
			}
			ch <- Event{Type: EventToolResult, Round: round, Text: resultText}
			toolMsg.Content = resultText
			e.messages = append(e.messages, toolMsg)
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
