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
	"github.com/timmy/timmy-code/internal/llm"
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
	flag.Parse()

	inDocker := isInDocker()

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
		runQuery(ctx, engine, *executeFlag)
		return
	}

	// --- Interactive mode: start raw log server ---
	logBaseDir := workDir + "/.timmy-code/raw_llm_io_log"
	rawLogger := rawlog.NewLogger(logBaseDir)
	sessionID := fmt.Sprintf("session-%s", time.Now().Format("20060102T150405"))

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

	// Determine viewer port.
	viewerPort := 9876 // default internal port
	if envPort := os.Getenv("TIMKY_VIEW_PORT"); envPort != "" {
		fmt.Sscanf(envPort, "%d", &viewerPort)
	}

	// Start the HTTP server.
	server := rawlog.NewServer(logBaseDir)
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", viewerPort))
	if err != nil {
		// Fall back to random port.
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

	// Interactive REPL
	if inDocker {
		fmt.Println("timmy-code v0.3 — DeepSeek CLI assistant (Docker container)")
	} else {
		fmt.Println("timmy-code v0.3 — DeepSeek CLI assistant")
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

// isInDocker returns true if running inside a Docker container.
func isInDocker() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

// isTerminal returns true if f is a character device (real terminal).
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// autoDockerWrap re-launches the current binary inside a Docker container.
// It picks a random host port and maps it to the container's viewer port (9876).
func autoDockerWrap() {
	// Pick a free host port.
	hostPort, err := rawlog.GetFreePort()
	if err != nil {
		hostPort = 9876 // fallback
	}

	// Always rebuild Docker image to ensure the binary is fresh.
	if _, statErr := os.Stat("Dockerfile"); statErr != nil {
		fmt.Fprintln(os.Stderr, "Error: Dockerfile not found in current directory.")
		fmt.Fprintln(os.Stderr, "Run from the timmy-code project root where Dockerfile exists.")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "Building Docker image timmy-code...")
	buildCmd := exec.Command("docker", "build", "-t", "timmy-code", ".")
	buildCmd.Stdout = os.Stderr
	buildCmd.Stderr = os.Stderr
	if buildErr := buildCmd.Run(); buildErr != nil {
		fmt.Fprintf(os.Stderr, "Error: docker build failed: %v\n", buildErr)
		os.Exit(1)
	}

	workDir, _ := os.Getwd()

	// Use -it only when stdin is a real terminal.
	ttyFlag := "-i"
	if isTerminal(os.Stdin) {
		ttyFlag = "-it"
	}

	viewerURL := fmt.Sprintf("http://localhost:%d", hostPort)

	runCmd := exec.Command("docker", "run", "--rm", ttyFlag,
		"-v", workDir+":/work",
		"-e", "DEEPSEEK_API_KEY",
		"-e", fmt.Sprintf("TIMKY_VIEW_PORT=%d", 9876),
		"-e", fmt.Sprintf("TIMKY_VIEWER_URL=%s", viewerURL),
		"-p", fmt.Sprintf("%d:9876", hostPort),
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
