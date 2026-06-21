package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/timmy/timmy-code/internal/docker"
)

// ANSI formatting constants.
const (
	reset   = "\033[0m"
	bold    = "\033[1m"
	dim     = "\033[2m"
	italic  = "\033[3m"
	red     = "\033[31m"
	green   = "\033[32m"
	yellow  = "\033[33m"
	blue    = "\033[34m"
	magenta = "\033[35m"
	cyan    = "\033[36m"
	gray    = "\033[90m"
	brightRed    = "\033[91m"
	brightGreen  = "\033[92m"
	brightYellow = "\033[93m"
	brightBlue   = "\033[94m"
	brightCyan   = "\033[96m"
)

// Event types (mirrored from internal/output for standalone client).
type outputEvent struct {
	Type      string     `json:"type"`
	Text      string     `json:"text,omitempty"`
	Name      string     `json:"name,omitempty"`
	Detail    string     `json:"detail,omitempty"`
	Truncated bool       `json:"truncated,omitempty"`
	Active    bool       `json:"active,omitempty"`
	Usage     *usageInfo `json:"usage,omitempty"`
	ElapsedMs int64      `json:"elapsed_ms,omitempty"`
}

type usageInfo struct {
	PromptTokens     int `json:"prompt"`
	CompletionTokens int `json:"completion"`
}

// CLI flags.
var (
	connectAddr = flag.String("connect", "127.0.0.1:9877", "Server address to connect to")
	noDocker    = flag.Bool("no-docker", false, "Skip Docker image rebuild and server start")
	rebuild     = flag.Bool("rebuild", false, "Force rebuild of the Docker image before connecting")
)

// Spinner frames.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func main() {
	flag.Parse()

	// --- Docker smart rebuild + server start ---
	if !*noDocker {
		projectRoot, rootErr := docker.FindProjectRoot()
		if rootErr != nil {
			if !docker.ImageExists("timmy-code:latest") {
				fmt.Fprintf(os.Stderr, "%s⚠ Docker image 'timmy-code' not found and project root not detected.%s\n", yellow, reset)
				fmt.Fprintf(os.Stderr, "%s  Build it: cd <timmy-code-project> && docker build -t timmy-code .%s\n", dim, reset)
				fmt.Fprintf(os.Stderr, "%s  Or run with --no-docker to skip.%s\n", dim, reset)
				os.Exit(1)
			}
		} else {
			if *rebuild {
				// Force rebuild: wipe the cached hash so EnsureImage sees a mismatch.
				hashFile := projectRoot + "/.timmy-code/.docker-image-hash"
				os.Remove(hashFile)
				fmt.Fprintf(os.Stderr, "%sForcing rebuild of Docker image...%s\n", dim, reset)
			}
			if err := docker.EnsureImage(projectRoot); err != nil {
				fmt.Fprintf(os.Stderr, "%s✖ %v%s\n", red, err, reset)
				os.Exit(1)
			}
		}

		// Kill any previous server container — we always start fresh.
		exec.Command("docker", "stop", "timmy-server").Run()
		exec.Command("docker", "rm", "timmy-server").Run()

		controlPort := 9877
		if _, portStr, err := net.SplitHostPort(*connectAddr); err == nil {
			fmt.Sscanf(portStr, "%d", &controlPort)
		}

		viewerPort, _ := docker.GetFreePort()
		if viewerPort == 0 {
			viewerPort = 9876
		}

		apiKey := os.Getenv("DEEPSEEK_API_KEY")
		if apiKey == "" {
			fmt.Fprintf(os.Stderr, "%s✖ DEEPSEEK_API_KEY is not set — server needs it to call the LLM API.%s\n", red, reset)
			fmt.Fprintf(os.Stderr, "%s  Set it: export DEEPSEEK_API_KEY=sk-...%s\n", dim, reset)
			os.Exit(1)
		}

		workDir, _ := os.Getwd()
		viewerURL := fmt.Sprintf("http://localhost:%d", viewerPort)

		fmt.Fprintf(os.Stderr, "%sStarting timmy-code server in Docker...%s\n", dim, reset)
		fmt.Fprintf(os.Stderr, "%s  Control: %s  |  Log viewer: %s%s\n", dim, *connectAddr, viewerURL, reset)

		// Detached container — no -it flags, just -d.
		bgCmd := exec.Command("docker", "run", "--rm", "-d",
			"--name", "timmy-server",
			"-v", workDir+":/work",
			"-e", fmt.Sprintf("DEEPSEEK_API_KEY=%s", apiKey),
			"-e", fmt.Sprintf("TIMKY_VIEW_PORT=%d", 9876),
			"-e", fmt.Sprintf("TIMKY_VIEWER_URL=%s", viewerURL),
			"-e", fmt.Sprintf("TIMKY_CONTROL_PORT=%d", 9877),
			"-p", fmt.Sprintf("%d:9877", controlPort),
			"-p", fmt.Sprintf("%d:9876", viewerPort),
			"timmy-code",
			"--cli-mode", "client-server",
		)
		// Capture stderr so we can show Docker errors.
		var dockerErr strings.Builder
		bgCmd.Stderr = &dockerErr

		if err := bgCmd.Run(); err != nil {
			errMsg := strings.TrimSpace(dockerErr.String())
			if errMsg == "" {
				errMsg = err.Error()
			}
			fmt.Fprintf(os.Stderr, "%s✖ Failed to start Docker: %s%s\n", red, errMsg, reset)
			fmt.Fprintf(os.Stderr, "%s  Try --no-docker or start the server manually.%s\n", dim, reset)
			os.Exit(1)
		}

		// Wait for the control port to be ready.
		fmt.Fprintf(os.Stderr, "%sWaiting for server to be ready...%s", dim, reset)
		ready := false
		for i := 0; i < 30; i++ {
			time.Sleep(500 * time.Millisecond)
			if testConn, err := net.DialTimeout("tcp", *connectAddr, 200*time.Millisecond); err == nil {
				testConn.Close()
				ready = true
				break
			}
			fmt.Fprint(os.Stderr, ".")
		}
		if ready {
			fmt.Fprintf(os.Stderr, "\n%sServer ready.%s\n", green, reset)
		} else {
			fmt.Fprintf(os.Stderr, "\n%s⚠ Server did not respond in time. Connecting anyway...%s\n", yellow, reset)
		}
	}

	conn, err := net.Dial("tcp", *connectAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s✖ Failed to connect to %s: %v%s\n", red, *connectAddr, err, reset)
		os.Exit(1)
	}
	defer conn.Close()

	fmt.Fprintf(os.Stderr, "%sConnected to %s%s\n", dim, *connectAddr, reset)

	// Channel to coordinate spinner with incoming events.
	eventCh := make(chan outputEvent, 32)
	errCh := make(chan error, 1)

	// Read server events in a goroutine.
	go func() {
		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				errCh <- err
				return
			}
			var ev outputEvent
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				continue // skip malformed lines
			}
			eventCh <- ev
		}
	}()

	// Input handling: read stdin in a goroutine.
	inputCh := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			inputCh <- scanner.Text()
		}
	}()

	// Signal handling.
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// State.
	var (
		spinning      bool
		spinnerIdx    int
		spinnerMsg    string
		spinnerStop   = make(chan struct{})
		codeBlock     bool
		codeLang      string
		codeBuf       []string
		queryInFlight bool // true when waiting for server response
	)

	// Start spinner goroutine.
	startSpinner := func(msg string) {
		if spinning {
			return
		}
		spinning = true
		spinnerMsg = msg
		spinnerIdx = 0
		spinnerStop = make(chan struct{})
		go func() {
			ticker := time.NewTicker(80 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-spinnerStop:
					return
				case <-ticker.C:
					if spinning {
						frame := spinnerFrames[spinnerIdx%len(spinnerFrames)]
						fmt.Fprintf(os.Stderr, "\r\033[K%s %s%s%s", frame, dim, spinnerMsg, reset)
						spinnerIdx++
					}
				}
			}
		}()
	}

	stopSpinner := func() {
		if !spinning {
			return
		}
		spinning = false
		close(spinnerStop)
		fmt.Fprint(os.Stderr, "\r\033[K") // clear spinner line
	}

	// Helpers.
	flushCodeBlock := func() {
		if !codeBlock || len(codeBuf) == 0 {
			codeBuf = nil
			codeBlock = false
			return
		}
		// Render buffered code block with syntax highlighting.
		fmt.Println()
		fmt.Printf("%s┌─ %s%s\n", gray, codeLang, reset)
		for _, line := range codeBuf {
			fmt.Printf("%s│%s %s\n", gray, reset, highlightLine(line, codeLang))
		}
		fmt.Printf("%s└─%s\n", gray, reset)
		codeBuf = nil
		codeBlock = false
		codeLang = ""
	}

	// Main event loop.
	printPrompt := func() {
		fmt.Fprint(os.Stderr, fmt.Sprintf("\r\033[K%s>%s ", bold, reset))
	}

	bannerShown := false

	for {
		select {
		case ev := <-eventCh:
			switch ev.Type {
			case "banner":
				stopSpinner()
				flushCodeBlock()
				fmt.Printf("\n%s╭─ %s%s%s ─╮%s\n", gray, bold, brightCyan, ev.Text, reset)
				fmt.Printf("%s│%s %s%s %s│%s\n", gray, reset, dim, "DeepSeek-powered code assistant", gray, reset)
				fmt.Printf("%s╰──────%s\n", gray, reset)
				if !bannerShown {
					bannerShown = true
					// Show prompt after banner+info settle (info arrives right after banner).
					time.AfterFunc(200*time.Millisecond, func() {
						if !queryInFlight {
							printPrompt()
						}
					})
				}

			case "info":
				stopSpinner()
				flushCodeBlock()
				if ev.Text != "" {
					fmt.Printf("%s%s%s\n", dim, ev.Text, reset)
				}

			case "thinking":
				if ev.Active {
					flushCodeBlock()
					startSpinner(ev.Text)
				} else {
					stopSpinner()
				}

			case "text_delta":
				stopSpinner()
				text := ev.Text

				// Detect code blocks.
				for _, ch := range text {
					s := string(ch)
					if strings.Contains(s, "```") {
						if codeBlock {
							flushCodeBlock()
						} else {
							codeBlock = true
							codeLang = ""
							codeBuf = nil
						}
						continue
					}
				}

				if codeBlock {
					// Accumulate code lines.
					lines := strings.Split(text, "\n")
					for i, line := range lines {
						if i == 0 && len(codeBuf) > 0 {
							codeBuf[len(codeBuf)-1] += line
						} else {
							codeBuf = append(codeBuf, line)
						}
						if strings.HasPrefix(line, "```") {
							// End of code block marker.
							flushCodeBlock()
						}
					}
					// If code block just started, first "line" after ``` is the language.
					if len(codeBuf) == 1 && codeLang == "" && !strings.HasPrefix(codeBuf[0], "```") {
						codeLang = strings.TrimSpace(codeBuf[0])
						codeBuf = nil
					}
					// Handle closing ```.
					if len(codeBuf) > 0 && strings.HasPrefix(codeBuf[len(codeBuf)-1], "```") {
						codeBuf = codeBuf[:len(codeBuf)-1]
						flushCodeBlock()
					}
				} else {
					fmt.Print(text)
				}

			case "tool_call":
				stopSpinner()
				flushCodeBlock()
				detailStr := ""
				if ev.Detail != "" {
					detailStr = fmt.Sprintf(" %s· %s%s", gray, ev.Detail, reset)
				}
				fmt.Printf("\n%s  ⎿%s  %s%s%s%s\n", gray, reset, cyan, ev.Name, reset, detailStr)

			case "tool_result":
				if ev.Text != "" {
					text := ev.Text
					if ev.Truncated {
						text += fmt.Sprintf("%s...%s", dim, reset)
					}
					// Indent each line.
					for _, line := range strings.Split(text, "\n") {
						fmt.Printf("%s     │%s %s\n", gray, reset, line)
					}
				}

			case "done":
				stopSpinner()
				flushCodeBlock()
				parts := []string{fmt.Sprintf("%s⏺%s Done", brightGreen, reset)}
				if ev.Usage != nil {
					parts = append(parts, fmt.Sprintf("%s⬇%d tok%s", dim, ev.Usage.PromptTokens, reset))
					parts = append(parts, fmt.Sprintf("%s⬆%d tok%s", dim, ev.Usage.CompletionTokens, reset))
				}
				if ev.ElapsedMs > 0 {
					parts = append(parts, fmt.Sprintf("%s· %s%s", dim, formatElapsed(ev.ElapsedMs), reset))
				}
				fmt.Printf("\n%s\n", strings.Join(parts, " "))
				queryInFlight = false
				printPrompt()

			case "error":
				stopSpinner()
				flushCodeBlock()
				fmt.Fprintf(os.Stderr, "\r\033[K%s✖%s %s%s%s\n", brightRed, reset, red, ev.Text, reset)
				queryInFlight = false
				printPrompt()
			}

		case input := <-inputCh:
			input = strings.TrimSpace(input)
			if input == "" {
				printPrompt()
				continue
			}
			if input == "exit" || input == "quit" {
				stopSpinner()
				fmt.Fprintf(os.Stderr, "\r\033[K%sGoodbye!%s\n", dim, reset)
				return
			}
			// Handle local slash commands.
			if strings.HasPrefix(input, "/") {
				parts := strings.Fields(input)
				switch parts[0] {
				case "/help":
					fmt.Printf("\n%s  /help                — this help%s\n", bold, reset)
					fmt.Printf("  %s/model pro|flash%s     — switch model\n", dim, reset)
					fmt.Printf("  %s/clear%s               — reset conversation\n", dim, reset)
					fmt.Printf("  %sexit, Ctrl+C%s         — quit\n", dim, reset)
					fmt.Println()
					printPrompt()
					continue
				case "/model":
					model := "pro"
					if len(parts) > 1 {
						model = parts[1]
					}
					cmd := fmt.Sprintf(`{"type":"switch_model","model":"%s"}`, model)
					fmt.Fprintf(conn, "%s\n", cmd)
					fmt.Fprintf(os.Stderr, "\r\033[K%sSwitching to %s...%s\n", dim, model, reset)
					printPrompt()
					continue
				case "/clear":
					cmd := `{"type":"cancel"}`
					fmt.Fprintf(conn, "%s\n", cmd)
					fmt.Fprintf(os.Stderr, "\r\033[K%sConversation reset.%s\n", dim, reset)
					printPrompt()
					continue
				default:
					fmt.Fprintf(os.Stderr, "\r\033[K%sUnknown command: %s. Type /help%s\n", yellow, input, reset)
					printPrompt()
					continue
				}
			}
			// Send prompt to server.
			cmd := fmt.Sprintf(`{"type":"prompt","text":"%s"}`, escapeJSON(input))
			if _, err := fmt.Fprintf(conn, "%s\n", cmd); err != nil {
				fmt.Fprintf(os.Stderr, "\r\033[K%s✖ Connection lost: %v%s\n", red, err, reset)
				return
			}
			queryInFlight = true
			// Clear the prompt line before output starts.
			fmt.Fprint(os.Stderr, "\r\033[K")

		case err := <-errCh:
			stopSpinner()
			flushCodeBlock()
			fmt.Fprintf(os.Stderr, "\r\033[K%s✖ Server disconnected: %v%s\n", red, err, reset)
			return

		case sig := <-sigCh:
			stopSpinner()
			flushCodeBlock()
			if sig == syscall.SIGINT && queryInFlight {
				// First Ctrl+C while query is running: cancel query.
				fmt.Fprintf(conn, `{"type":"cancel"}`+"\n")
				fmt.Fprintf(os.Stderr, "\r\033[K%sCancelled.%s\n", dim, reset)
				queryInFlight = false
				printPrompt()
				continue
			}
			// Second Ctrl+C (or SIGTERM): exit.
			fmt.Fprintf(os.Stderr, "\r\033[K%sGoodbye!%s\n", dim, reset)
			return
		}
	}
}

// formatElapsed formats milliseconds as a human-readable duration.
func formatElapsed(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	sec := float64(ms) / 1000.0
	if sec < 60 {
		return fmt.Sprintf("%.1fs", sec)
	}
	min := int(sec) / 60
	s := int(sec) % 60
	return fmt.Sprintf("%dm %ds", min, s)
}

// escapeJSON escapes a string for inclusion in a JSON string value.
func escapeJSON(s string) string {
	b, _ := json.Marshal(s)
	// Trim surrounding quotes.
	if len(b) >= 2 {
		return string(b[1 : len(b)-1])
	}
	return s
}

// highlightLine applies basic syntax highlighting to a line of code.
func highlightLine(line, language string) string {
	// Basic keyword highlighting for common languages.
	keywords := goKeywords
	switch language {
	case "python", "py":
		keywords = pyKeywords
	case "javascript", "js", "typescript", "ts", "tsx":
		keywords = jsKeywords
	case "go", "golang":
		keywords = goKeywords
	case "rust", "rs":
		keywords = rustKeywords
	case "bash", "sh", "shell":
		keywords = bashKeywords
	}

	// Tokenize and highlight.
	var result strings.Builder
	wordStart := -1
	for i, r := range line {
		if unicode.IsLetter(r) || r == '_' {
			if wordStart < 0 {
				wordStart = i
			}
		} else {
			if wordStart >= 0 {
				word := line[wordStart:i]
				result.WriteString(highlightWord(word, keywords))
				wordStart = -1
			}
			result.WriteRune(r)
		}
	}
	if wordStart >= 0 {
		word := line[wordStart:]
		result.WriteString(highlightWord(word, keywords))
	}
	return result.String()
}

func highlightWord(word string, keywords map[string]bool) string {
	if keywords[word] {
		return magenta + word + reset
	}
	// Highlight string literals, numbers, comments are handled at line level.
	return word
}

// Keyword sets for basic syntax highlighting.
var (
	goKeywords = map[string]bool{
		"func": true, "package": true, "import": true, "var": true, "const": true,
		"type": true, "struct": true, "interface": true, "map": true, "chan": true,
		"if": true, "else": true, "for": true, "range": true, "switch": true,
		"case": true, "default": true, "return": true, "break": true, "continue": true,
		"go": true, "defer": true, "select": true, "fallthrough": true,
		"nil": true, "true": true, "false": true,
		"int": true, "string": true, "bool": true, "byte": true, "error": true,
		"float64": true, "float32": true, "int64": true, "int32": true,
		"uint": true, "uint64": true, "uint32": true,
	}
	pyKeywords = map[string]bool{
		"def": true, "class": true, "import": true, "from": true,
		"if": true, "elif": true, "else": true, "for": true, "while": true,
		"try": true, "except": true, "finally": true, "with": true, "as": true,
		"return": true, "yield": true, "break": true, "continue": true, "pass": true,
		"raise": true, "lambda": true, "and": true, "or": true, "not": true,
		"in": true, "is": true, "None": true, "True": true, "False": true,
		"self": true, "print": true, "async": true, "await": true,
	}
	jsKeywords = map[string]bool{
		"function": true, "const": true, "let": true, "var": true, "class": true,
		"if": true, "else": true, "for": true, "while": true, "do": true,
		"switch": true, "case": true, "default": true, "return": true, "break": true,
		"continue": true, "try": true, "catch": true, "finally": true, "throw": true,
		"new": true, "this": true, "super": true, "extends": true, "implements": true,
		"import": true, "export": true, "from": true, "async": true, "await": true,
		"true": true, "false": true, "null": true, "undefined": true,
		"typeof": true, "instanceof": true, "interface": true, "type": true,
		"enum": true, "yield": true, "of": true,
	}
	rustKeywords = map[string]bool{
		"fn": true, "struct": true, "impl": true, "trait": true, "enum": true,
		"let": true, "mut": true, "const": true, "static": true, "type": true,
		"if": true, "else": true, "for": true, "while": true, "loop": true,
		"match": true, "return": true, "break": true, "continue": true,
		"pub": true, "use": true, "mod": true, "crate": true, "self": true,
		"super": true, "where": true, "as": true, "in": true, "ref": true,
		"true": true, "false": true, "Some": true, "None": true, "Ok": true, "Err": true,
		"async": true, "await": true, "move": true, "dyn": true, "unsafe": true,
		"extern": true, "macro_rules": true,
	}
	bashKeywords = map[string]bool{
		"if": true, "then": true, "else": true, "elif": true, "fi": true,
		"for": true, "while": true, "do": true, "done": true, "case": true,
		"esac": true, "in": true, "function": true, "return": true, "exit": true,
		"local": true, "export": true, "unset": true, "readonly": true,
		"echo": true, "source": true, "shift": true,
	}
)
