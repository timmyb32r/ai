package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	ctxpkg "github.com/timmy/timmy-code/internal/context"
	"github.com/timmy/timmy-code/internal/docker"
	"github.com/timmy/timmy-code/internal/llm"
	"github.com/timmy/timmy-code/internal/output"
	"github.com/timmy/timmy-code/internal/prompts"
	"github.com/timmy/timmy-code/internal/query"
	"github.com/timmy/timmy-code/internal/rawlog"
	"github.com/timmy/timmy-code/internal/tools"
)

func main() {
	modelFlag := flag.String("model", "", "Override model name")
	fastFlag := flag.Bool("fast", false, "Use fast model (deepseek-v4-flash)")
	_ = flag.Bool("debug", false, "Enable debug output")
	dumpPromptFlag := flag.Bool("dump-prompt", false, "Print system prompt to stderr before sending")
	executeFlag := flag.String("execute", "", "Non-interactive mode: execute a single prompt autonomously")
	maxTokensFlag := flag.Int("max-tokens", 0, "Limit max output tokens (0 = default)")
	cliModeFlag := flag.String("cli-mode", "client-server", "CLI mode: 'pipes' (plain text) or 'client-server' (rich UI)")
	flag.Parse()

	inDocker := docker.IsInDocker()

	// Auto-wrap: on host with no arguments → re-launch inside Docker.
	if !inDocker && len(os.Args) == 1 {
		autoDockerWrap()
		return
	}

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
	if *maxTokensFlag > 0 {
		modelCfg.MaxTokens = *maxTokensFlag
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

	// Non-interactive: one shot.
	if *executeFlag != "" {
		engine := query.New(query.Config{
			Tools:        registry,
			LLMClient:    llmClient,
			CtxService:   ctxService,
			ModelCfg:     modelCfg,
			WorkDir:      workDir,
			SystemPrompt: systemPrompt,
		})
		defer engine.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
		defer cancel()
		runQueryPlain(ctx, engine, *executeFlag)
		return
	}

	// --- Interactive mode: start raw log server ---
	logBaseDir := workDir + "/.timmy-code/raw_llm_io_log"
	rawLogger := rawlog.NewLogger(logBaseDir)
	sessionID := fmt.Sprintf("session-%s", time.Now().Format("2006-01-02_15-04-05"))

	newEngine := func() *query.QueryEngine {
		return query.New(query.Config{
			Tools:        registry,
			LLMClient:    llmClient,
			CtxService:   ctxService,
			ModelCfg:     modelCfg,
			WorkDir:      workDir,
			SystemPrompt: systemPrompt,
			RawLogger:    rawLogger,
			SessionID:    sessionID,
		})
	}
	engine := newEngine()
	defer engine.Close()

	// Factory for model switching (recreates engine with current modelCfg).
	recreateEngine := func() *query.QueryEngine {
		engine.Close()
		engine = query.New(query.Config{
			Tools:        registry,
			LLMClient:    llmClient,
			CtxService:   ctxService,
			ModelCfg:     modelCfg,
			WorkDir:      workDir,
			SystemPrompt: systemPrompt,
			RawLogger:    rawLogger,
			SessionID:    sessionID,
		})
		return engine
	}

	// Determine viewer port.
	viewerPort := 9876 // default internal port
	if envPort := os.Getenv("TIMKY_VIEW_PORT"); envPort != "" {
		fmt.Sscanf(envPort, "%d", &viewerPort)
	}

	// Start the HTTP server for raw log viewer.
	server := rawlog.NewServer(logBaseDir)
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", viewerPort))
	if err != nil {
		ln, viewerPort, err = rawlog.ListenDynamicPort()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cannot start viewer server: %v\n", err)
		}
	}
	if ln != nil {
		go func() {
			if err := server.Serve(ln); err != nil {
				fmt.Fprintf(os.Stderr, "Viewer server error: %v\n", err)
			}
		}()
	}

	// Build viewer URL.
	viewerURL := ""
	if envURL := os.Getenv("TIMKY_VIEWER_URL"); envURL != "" {
		viewerURL = envURL
	} else {
		viewerURL = fmt.Sprintf("http://localhost:%d", viewerPort)
	}

	switch *cliModeFlag {
	case "pipes":
		runPipesMode(inDocker, viewerURL, &modelCfg, newEngine, recreateEngine, engine)
	case "client-server":
		runClientServerMode(viewerURL, &modelCfg, newEngine, recreateEngine, engine)
	default:
		fmt.Fprintf(os.Stderr, "Unknown --cli-mode: %s (expected 'pipes' or 'client-server')\n", *cliModeFlag)
		os.Exit(1)
	}
}

// runPipesMode runs the existing plain-text REPL (backward compatible).
func runPipesMode(inDocker bool, viewerURL string, modelCfg *llm.ModelConfig, newEngine func() *query.QueryEngine, recreateEngine func() *query.QueryEngine, engine *query.QueryEngine) {
	if inDocker {
		fmt.Println("timmy-code v0.4 — DeepSeek CLI assistant (Docker container)")
	} else {
		fmt.Println("timmy-code v0.4 — DeepSeek CLI assistant")
	}
	fmt.Printf("Raw Log Viewer: %s\n", viewerURL)
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

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("> ")
		firstLine, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		firstLine = strings.TrimRight(firstLine, "\n")

		// Drain any immediately buffered data.
		var extraLines []string
		for reader.Buffered() > 0 {
			b, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			extraLines = append(extraLines, strings.TrimRight(b, "\n"))
		}

		line := firstLine
		if len(extraLines) > 0 {
			line = firstLine + "\n" + strings.Join(extraLines, "\n")
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			fmt.Println("Goodbye!")
			break
		}
		if strings.HasPrefix(line, "/") {
			handleSlash(line, modelCfg, &engine, newEngine)
			continue
		}

		ctx, cancel := context.WithCancel(context.Background())
		msgCancel = cancel
		inQuery = true
		runQueryPlain(ctx, engine, line)
		cancel()
		inQuery = false
		msgCancel = nil
	}
}

// runClientServerMode starts the TCP control server and waits for client connections.
func runClientServerMode(viewerURL string, modelCfg *llm.ModelConfig, newEngine func() *query.QueryEngine, recreateEngine func() *query.QueryEngine, engine *query.QueryEngine) {
	controlPort := 9877
	if envPort := os.Getenv("TIMKY_CONTROL_PORT"); envPort != "" {
		fmt.Sscanf(envPort, "%d", &controlPort)
	}

	// Build a factory that returns a fresh engine for each client session.
	engineFactory := func() *query.QueryEngine {
		return newEngine()
	}

	// Recreate engine factory (for model switches — updates the shared modelCfg).
	recreateFactory := func() *query.QueryEngine {
		return recreateEngine()
	}

	cs, err := output.NewControlServer(controlPort, engineFactory, recreateFactory)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error starting control server: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "timmy-code server v0.4\n")
	fmt.Fprintf(os.Stderr, "Control:    tcp://localhost:%d\n", cs.Port())
	fmt.Fprintf(os.Stderr, "Log viewer: %s\n", viewerURL)
	fmt.Fprintf(os.Stderr, "Waiting for client connection...\n")

	// Handle SIGINT/SIGTERM for graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintf(os.Stderr, "\nShutting down...\n")
		cs.Shutdown()
		os.Exit(0)
	}()

	if err := cs.Serve(); err != nil {
		fmt.Fprintf(os.Stderr, "Control server error: %v\n", err)
		os.Exit(1)
	}
}

// autoDockerWrap re-launches the current binary inside a Docker container.
func autoDockerWrap() {
	// Validate API key BEFORE launching Docker — gives a clear error early.
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: DEEPSEEK_API_KEY environment variable is required")
		fmt.Fprintln(os.Stderr, "Set it with: export DEEPSEEK_API_KEY=sk-...")
		os.Exit(1)
	}

	hostPort, err := docker.GetFreePort()
	if err != nil {
		hostPort = 9876
	}

	controlPort, err := docker.GetFreePort()
	if err != nil {
		controlPort = 9877
	}

	projectRoot, rootErr := docker.FindProjectRoot()
	if rootErr != nil {
		if !docker.ImageExists("timmy-code:latest") {
			fmt.Fprintln(os.Stderr, "Error: Docker image 'timmy-code' not found.")
			fmt.Fprintln(os.Stderr, "Build it first: cd <timmy-code-project> && docker build -t timmy-code .")
			os.Exit(1)
		}
	} else {
		if err := docker.EnsureImage(projectRoot); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}

	workDir, _ := os.Getwd()

	ttyFlag := "-i"
	if docker.IsTerminal(os.Stdin) {
		ttyFlag = "-it"
	}

	viewerURL := fmt.Sprintf("http://localhost:%d", hostPort)

	runCmd := exec.Command("docker", "run", "--rm", ttyFlag,
		"-v", workDir+":/work",
		"-e", fmt.Sprintf("DEEPSEEK_API_KEY=%s", apiKey),
		"-e", fmt.Sprintf("TIMKY_VIEW_PORT=%d", 9876),
		"-e", fmt.Sprintf("TIMKY_VIEWER_URL=%s", viewerURL),
		"-e", fmt.Sprintf("TIMKY_CONTROL_PORT=%d", 9877),
		"-p", fmt.Sprintf("%d:9876", hostPort),
		"-p", fmt.Sprintf("%d:9877", controlPort),
		"timmy-code",
	)
	runCmd.Stdin = os.Stdin
	runCmd.Stdout = os.Stdout
	runCmd.Stderr = os.Stderr
	if err := runCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: docker run failed: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// runQueryPlain sends one prompt and displays events with spinner + token counts.
// Used by both pipes mode and non-interactive execution.
func runQueryPlain(ctx context.Context, engine *query.QueryEngine, prompt string) {
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
		// Spinner while waiting.
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
