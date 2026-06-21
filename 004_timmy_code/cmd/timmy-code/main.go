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

	ctxpkg "github.com/timmy/timmy-code/internal/context"
	"github.com/timmy/timmy-code/internal/llm"
	"github.com/timmy/timmy-code/internal/prompts"
	"github.com/timmy/timmy-code/internal/query"
	"github.com/timmy/timmy-code/internal/tools"
)

func main() {
	modelFlag := flag.String("model", "", "Override model name")
	fastFlag := flag.Bool("fast", false, "Use fast model (deepseek-v4-flash)")
	debugFlag := flag.Bool("debug", false, "Enable debug output")
	dumpPromptFlag := flag.Bool("dump-prompt", false, "Print system prompt to stderr before sending")
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

	// Model config
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

	if *debugFlag {
		fmt.Fprintf(os.Stderr, "[debug] Using model: %s\n", modelCfg.ModelName)
	}

	// Initialize components
	llmClient, err := llm.NewDeepSeekClient(apiKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating LLM client: %v\n", err)
		os.Exit(1)
	}
	registry := tools.NewRegistry(
		&tools.BashTool{},
		&tools.ReadTool{},
		&tools.WriteTool{},
		&tools.EditTool{},
		tools.NewAgentTool(llmClient),
	)
	ctxService := ctxpkg.NewService()

	systemPrompt := prompts.SystemPrompt
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

	fmt.Println("timmy-code v0.2 — Socratic deep-interview agent (Ouroboros-inspired)")
	fmt.Println("I ask targeted questions to crystallize requirements. Ambiguity scored after every answer.")
	fmt.Println()
	fmt.Println("Commands: /help, /model [pro|flash], /clear, exit, Ctrl+C")
	fmt.Println("Agents: planner, architect, critic, executor, analyst, code-reviewer, verifier")
	fmt.Println()

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT)

	scanner := bufio.NewScanner(os.Stdin)
	var (
		inQuery   bool
		msgCancel context.CancelFunc
	)

	// Signal handler goroutine
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

		// Handle slash commands locally
		if strings.HasPrefix(line, "/") {
			parts := strings.Fields(line)
			switch parts[0] {
			case "/help":
				fmt.Println()
				fmt.Println("=== timmy-code commands ===")
				fmt.Println("  /help               — show this help")
				fmt.Println("  /model pro|flash    — switch model (pro = deepseek-v4-pro, flash = deepseek-v4-flash)")
				fmt.Println("  /clear              — reset conversation")
				fmt.Println("  exit, Ctrl+C        — quit")
				fmt.Println()
				fmt.Println("=== Agent types (use via Agent tool) ===")
				fmt.Println("  planner       — creates work plans (.omc/plans/)")
				fmt.Println("  architect     — architectural analysis (READ-ONLY)")
				fmt.Println("  critic        — quality gate, finds every flaw (READ-ONLY)")
				fmt.Println("  executor      — implements code changes")
				fmt.Println("  analyst       — requirements gap analysis (READ-ONLY)")
				fmt.Println("  code-reviewer — severity-rated code review (READ-ONLY)")
				fmt.Println("  verifier      — evidence-based completion checks (READ-ONLY)")
				fmt.Println()
				continue
			case "/model":
				if len(parts) < 2 {
					fmt.Printf("Current model: %s\n", modelCfg.ModelName)
					fmt.Println("Usage: /model pro | /model flash")
					continue
				}
				switch parts[1] {
				case "pro":
					modelCfg.ModelName = llm.DefaultModel
					modelCfg.FastMode = false
				case "flash":
					modelCfg.ModelName = llm.FastModel
					modelCfg.FastMode = true
				default:
					fmt.Printf("Unknown model: %s. Use 'pro' or 'flash'.\n", parts[1])
					continue
				}
				engine.Close()
				engine = newEngine()
				fmt.Printf("Switched to %s\n", modelCfg.ModelName)
				continue
			case "/clear":
				engine.Close()
				engine = newEngine()
				fmt.Println("Conversation reset.")
				continue
			default:
				fmt.Printf("Unknown command: %s. Type /help for available commands.\n", parts[0])
				continue
			}
		}

		// Submit to engine
		msgCtx, cancel := context.WithCancel(context.Background())
		msgCancel = cancel
		eventCh := engine.SubmitMessage(msgCtx, line)
		inQuery = true

		for event := range eventCh {
			switch event.Type {
			case query.EventTextDelta:
				fmt.Print(event.Text)
			case query.EventToolCall:
				fmt.Printf("\n🔧 [tool: %s] ", event.ToolUse.Name)
				if *debugFlag {
					fmt.Printf("%v", event.ToolUse.Input)
				}
				fmt.Println()
			case query.EventToolResult:
				if event.Text != "" {
					fmt.Printf("→ %s\n", event.Text)
				}
			case query.EventError:
				fmt.Fprintf(os.Stderr, "\nError: %v\n", event.Error)
			case query.EventDone:
				if event.Round == 0 {
					fmt.Println()
				}
				fmt.Println()
			}
		}

		msgCancel()
		inQuery = false
		msgCancel = nil
	}
}
