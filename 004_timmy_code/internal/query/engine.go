package query

import (
	"context"

	ctxpkg "github.com/timmy/timmy-code/internal/context"
	"github.com/timmy/timmy-code/internal/llm"
	"github.com/timmy/timmy-code/internal/schema"
	"github.com/timmy/timmy-code/internal/tools"
)

// Config carries all dependencies for creating a QueryEngine.
type Config struct {
	Tools        tools.Registry
	LLMClient    llm.Client
	CtxService   ctxpkg.Service
	ModelCfg     llm.ModelConfig
	WorkDir      string
	SystemPrompt string
}

// QueryEngine is the core orchestration engine for conversational queries.
// It manages the agentic loop: context assembly, LLM calls, tool execution.
type QueryEngine struct {
	tools        tools.Registry
	llmClient    llm.Client
	ctxService   ctxpkg.Service
	modelCfg     llm.ModelConfig
	workDir      string
	systemPrompt string
	messages     []schema.Message
}

// New creates a QueryEngine with the given configuration.
func New(cfg Config) *QueryEngine {
	return &QueryEngine{
		tools:        cfg.Tools,
		llmClient:    cfg.LLMClient,
		ctxService:   cfg.CtxService,
		modelCfg:     cfg.ModelCfg,
		workDir:      cfg.WorkDir,
		systemPrompt: cfg.SystemPrompt,
	}
}

// SubmitMessage accepts user input and returns a channel of events.
// The channel is closed by the engine after a Done or Error event.
// Cancelling ctx aborts the loop and returns an Error event.
func (e *QueryEngine) SubmitMessage(ctx context.Context, input string) <-chan Event {
	ch := make(chan Event, 8)
	go e.queryLoop(ctx, input, ch)
	return ch
}

// Close releases engine resources.
func (e *QueryEngine) Close() error { return nil }
