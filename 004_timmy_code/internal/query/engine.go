package query

import (
	"context"

	ctxpkg "github.com/timmy/timmy-code/internal/context"
	"github.com/timmy/timmy-code/internal/llm"
	"github.com/timmy/timmy-code/internal/rawlog"
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
	RawLogger    *rawlog.Logger    // optional: if set, raw LLM I/O is logged
	SessionID    string            // session identifier for logging
}

// QueryEngine is the core orchestration engine for conversational queries.
// It manages the agentic loop: context assembly, LLM calls, tool execution.
type QueryEngine struct {
	tools         tools.Registry
	llmClient     llm.Client
	ctxService    ctxpkg.Service
	modelCfg      llm.ModelConfig
	workDir       string
	systemPrompt  string
	messages      []schema.Message
	rawLogger     *rawlog.Logger
	sessionLogger *rawlog.SessionLogger
	msgLogger     *rawlog.MessageLogger // current message logger, set per SubmitMessage
	roundLogger   *rawlog.RoundLogger   // current round logger, set per round
}

// New creates a QueryEngine with the given configuration.
func New(cfg Config) *QueryEngine {
	e := &QueryEngine{
		tools:        cfg.Tools,
		llmClient:    cfg.LLMClient,
		ctxService:   cfg.CtxService,
		modelCfg:     cfg.ModelCfg,
		workDir:      cfg.WorkDir,
		systemPrompt: cfg.SystemPrompt,
		rawLogger:    cfg.RawLogger,
	}
	if cfg.RawLogger != nil && cfg.SessionID != "" {
		e.sessionLogger = cfg.RawLogger.StartSession(cfg.SessionID)
	}
	return e
}

// RawRoundLogger returns the current round logger, or nil.
// Used by AgentTool to propagate logging to sub-agents.
func (e *QueryEngine) RawRoundLogger() *rawlog.RoundLogger {
	return e.roundLogger
}

// SetRawLogger sets the raw logger and optionally a session logger.
// Used for sub-agents to inherit logging.
func (e *QueryEngine) SetRawLogger(logger *rawlog.Logger, session *rawlog.SessionLogger) {
	e.rawLogger = logger
	e.sessionLogger = session
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
