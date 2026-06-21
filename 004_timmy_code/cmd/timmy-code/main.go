package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	ctxpkg "github.com/timmy/timmy-code/internal/context"
	"github.com/timmy/timmy-code/internal/llm"
	"github.com/timmy/timmy-code/internal/prompts"
	"github.com/timmy/timmy-code/internal/query"
	"github.com/timmy/timmy-code/internal/tools"
)

func main() {
	modelFlag := flag.String("model", "", "Override model name")
	fastFlag := flag.Bool("fast", false, "Use fast model (deepseek-v4-flash)")
	_ = flag.Bool("debug", false, "Enable debug output")
	dumpPromptFlag := flag.Bool("dump-prompt", false, "Print system prompt to stderr before sending")
	executeFlag := flag.String("execute", "", "Non-interactive mode: execute a single prompt autonomously")
	flag.Parse()

	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: DEEPSEEK_API_KEY environment variable is required")
		os.Exit(1)
	}

	workDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	modelCfg := llm.ModelConfig{
		ModelName: llm.DefaultModel,
		MaxTokens: llm.DefaultMaxTokens,
	}
	if *fastFlag {
		modelCfg.ModelName = llm.FastModel
		modelCfg.FastMode = true
	}
	if *modelFlag != "" {
		modelCfg.ModelName = *modelFlag
	}

	// Use direct HTTP client for proper SSE streaming + token counting.
	llmClient := llm.NewDirectClient(apiKey)
	registry := tools.NewRegistry(
		&tools.BashTool{},
		&tools.ReadTool{},
		&tools.WriteTool{},
		&tools.EditTool{},
		tools.NewAgentTool(llmClient),
	)
	ctxService := ctxpkg.NewService()

	systemPrompt := prompts.SystemPrompt
	if *executeFlag != "" {
		systemPrompt = prompts.AutonomousPrompt
		modelCfg.MaxTokens = 32768
	}
	if *dumpPromptFlag {
		fmt.Fprintf(os.Stderr, "=== System Prompt ===\n%s\n=== End System Prompt ===\n", systemPrompt)
	}

	newEngine := func() *query.QueryEngine {
		return query.New(query.Config{
			Tools:        registry,
			LLMClient:    llmClient,
			CtxService:   ctxService,
			ModelCfg:     modelCfg,
			WorkDir:      workDir,
			SystemPrompt: systemPrompt,
		})
	}
	engine := newEngine()
	defer engine.Close()

	// Non-interactive: one shot.
	if *executeFlag != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
		defer cancel()
		runQuery(ctx, engine, *executeFlag)
		return
	}

	fmt.Println("timmy-code v0.3 — DeepSeek CLI assistant")
	fmt.Println("Commands: /help, /model [pro|flash], /clear, exit, Ctrl+C")
	fmt.Println("Agents: planner, architect, critic, executor, analyst, code-reviewer, verifier")
	fmt.Println()

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT)
	var (
		inQuery   bool
		msgCancel context.CancelFunc
	)
	go func() {
		for range sigCh {
			if inQuery && msgCancel != nil {
				msgCancel()
			} else {
				fmt.Fprintln(os.Stderr, "\nExiting.")
				os.Exit(0)
			}
		}
	}()

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			fmt.Println("Goodbye!")
			break
		}
		if strings.HasPrefix(line, "/") {
			handleSlash(line, &modelCfg, &engine, newEngine)
			continue
		}

		ctx, cancel := context.WithCancel(context.Background())
		msgCancel = cancel
		inQuery = true
		runQuery(ctx, engine, line)
		cancel()
		inQuery = false
		msgCancel = nil
	}
}

// runQuery sends one prompt and displays events with spinner + token counts.
func runQuery(ctx context.Context, engine *query.QueryEngine, prompt string) {
	start := time.Now()
	eventCh := engine.SubmitMessage(ctx, prompt)
	spinner := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	si := 0
	gotText := false

	for event := range eventCh {
		switch event.Type {
		case query.EventTextDelta:
			if !gotText {
				gotText = true
				fmt.Fprint(os.Stderr, "\r\033[K") // clear spinner
			}
			fmt.Print(event.Text)
		case query.EventToolCall:
			fmt.Printf("\n  ⎿  %s\n", event.ToolUse.Name)
		case query.EventToolResult:
			text := strings.TrimSpace(event.Text)
			if len(text) > 120 {
				text = text[:120] + "..."
			}
			if text != "" {
				fmt.Printf("     %s\n", text)
			}
		case query.EventError:
			fmt.Fprintf(os.Stderr, "\r\033[K✖ Error: %v\n", event.Error)
		case query.EventDone:
			if !gotText {
				fmt.Fprint(os.Stderr, "\r\033[K")
			}
			elapsed := time.Since(start).Round(100 * time.Millisecond)
			tokPart := ""
			if event.Usage != nil {
				tokPart = fmt.Sprintf(" · ⬇%d tok · ⬆%d tok",
					event.Usage.PromptTokens, event.Usage.CompletionTokens)
			}
			fmt.Printf("\n⏺ Done%s · %s\n", tokPart, elapsed)
		}
		// Spinner while waiting
		if !gotText && event.Type != query.EventDone {
			fmt.Fprintf(os.Stderr, "\r\033[K%s Thinking...", spinner[si%len(spinner)])
			si++
		}
	}
	fmt.Fprint(os.Stderr, "\r\033[K") // final spinner cleanup
}

func handleSlash(line string, modelCfg *llm.ModelConfig, engine **query.QueryEngine, newEngine func() *query.QueryEngine) {
	parts := strings.Fields(line)
	switch parts[0] {
	case "/help":
		fmt.Println()
		fmt.Println("  /help               — this help")
		fmt.Println("  /model pro|flash    — switch model")
		fmt.Println("  /clear              — reset conversation")
		fmt.Println("  exit, Ctrl+C        — quit")
		fmt.Println()
		fmt.Println("Agent types: planner, architect, critic, executor, analyst, code-reviewer, verifier")
		fmt.Println()
	case "/model":
		if len(parts) < 2 {
			fmt.Printf("Current: %s. Usage: /model pro|flash\n", modelCfg.ModelName)
			return
		}
		switch parts[1] {
		case "pro":
			modelCfg.ModelName = llm.DefaultModel
			modelCfg.FastMode = false
		case "flash":
			modelCfg.ModelName = llm.FastModel
			modelCfg.FastMode = true
		default:
			fmt.Printf("Unknown model: %s\n", parts[1])
			return
		}
		(*engine).Close()
		*engine = newEngine()
		fmt.Printf("Switched to %s\n", modelCfg.ModelName)
	case "/clear":
		(*engine).Close()
		*engine = newEngine()
		fmt.Println("Conversation reset.")
	default:
		fmt.Printf("Unknown: %s. Type /help\n", parts[0])
	}
}
