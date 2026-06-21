package output

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/timmy/timmy-code/internal/query"
)

// EngineFactory creates a new QueryEngine with the current configuration.
type EngineFactory func() *query.QueryEngine

// EngineFactoryWithModel creates a new QueryEngine with a specific model configuration.
// The modelCfg is updated in-place and a fresh engine is returned.
type EngineFactoryWithModel func() *query.QueryEngine

// ControlServer listens for TCP client connections and runs the interactive loop.
// It implements the JSON Lines protocol for client-server communication.
type ControlServer struct {
	ln              net.Listener
	engineFactory   EngineFactory
	recreateEngine  EngineFactoryWithModel // called after model switch
	port            int
	wg              sync.WaitGroup
	mu              sync.Mutex
	currentSession  *session
}

// session represents one active client connection and query execution.
type session struct {
	conn       net.Conn
	reader     *bufio.Reader
	writer     *JSONWriter
	engine     *query.QueryEngine
	builder    *OutputBuilder
	cancel     context.CancelFunc // current query cancel
	ctx        context.Context    // current query context
}

// NewControlServer creates a ControlServer that listens on the given port.
// engineFactory creates a fresh engine for each new client session.
// recreateEngine is called after model switches to rebuild the engine.
func NewControlServer(port int, engineFactory EngineFactory, recreateEngine EngineFactoryWithModel) (*ControlServer, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, fmt.Errorf("listen on port %d: %w", port, err)
	}
	return &ControlServer{
		ln:             ln,
		engineFactory:  engineFactory,
		recreateEngine: recreateEngine,
		port:           port,
	}, nil
}

// Port returns the port the server is listening on.
func (s *ControlServer) Port() int {
	return s.ln.Addr().(*net.TCPAddr).Port
}

// Addr returns the full listen address.
func (s *ControlServer) Addr() string {
	return s.ln.Addr().String()
}

// Serve accepts client connections and handles them. Blocks until the listener
// is closed. Each connection gets its own engine instance.
func (s *ControlServer) Serve() error {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			// Listener closed — graceful shutdown.
			if opErr, ok := err.(*net.OpError); ok && opErr.Err.Error() == "use of closed network connection" {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		s.wg.Add(1)
		go s.handleConn(conn)
	}
}

// Shutdown gracefully closes the listener and waits for active sessions.
func (s *ControlServer) Shutdown() {
	s.ln.Close()
	s.wg.Wait()
}

// handleConn manages one client connection.
func (s *ControlServer) handleConn(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	engine := s.engineFactory()
	builder := NewOutputBuilder()

	sess := &session{
		conn:    conn,
		reader:  bufio.NewReader(conn),
		writer:  NewJSONWriter(conn),
		engine:  engine,
		builder: builder,
	}

	s.mu.Lock()
	s.currentSession = sess
	s.mu.Unlock()

	// Send banner immediately.
	sess.writer.WriteEvent(builder.BuildBanner("timmy-code v0.4 — DeepSeek CLI assistant"))
	if viewerURL := os.Getenv("TIMKY_VIEWER_URL"); viewerURL != "" {
		sess.writer.WriteEvent(builder.BuildInfo(fmt.Sprintf("Raw Log Viewer: %s", viewerURL)))
	}
	sess.writer.WriteEvent(builder.BuildInfo("Commands: /help, /model pro|flash, /clear, exit, Ctrl+C"))
	sess.writer.WriteEvent(builder.BuildInfo(""))
	sess.writer.Flush()

	// Read commands from the client.
	for {
		line, err := sess.reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				fmt.Fprintf(os.Stderr, "[server] read error: %v\n", err)
			}
			break
		}

		var cmd Command
		if err := json.Unmarshal([]byte(line), &cmd); err != nil {
			// Skip malformed lines.
			continue
		}

		switch cmd.Type {
		case CmdPrompt:
			s.handlePrompt(sess, cmd.Text)
		case CmdCancel:
			s.handleCancel(sess)
		case CmdSwitchModel:
			s.handleSwitchModel(sess, cmd.Model)
		default:
			// Unknown command — ignore.
		}
	}

	// Cleanup.
	if sess.cancel != nil {
		sess.cancel()
	}
	sess.engine.Close()
}

// handlePrompt starts a new query with the given input.
func (s *ControlServer) handlePrompt(sess *session, input string) {
	if input == "" {
		return
	}

	// Handle slash commands locally.
	if len(input) > 0 && input[0] == '/' {
		s.handleSlashCommand(sess, input)
		return
	}

	sess.builder.Reset()
	_ = sess.writer.WriteEvent(sess.builder.BuildThinkingStart("Thinking..."))
	sess.writer.Flush()

	ctx, cancel := context.WithCancel(context.Background())
	sess.cancel = cancel
	sess.ctx = ctx

	start := time.Now()
	eventCh := sess.engine.SubmitMessage(ctx, input)

	var lastUsage *query.TokenUsage
	textStarted := false

	for ev := range eventCh {
		switch ev.Type {
		case query.EventTextDelta:
			if !textStarted {
				textStarted = true
			}
			_ = sess.writer.WriteEvent(sess.builder.ConvertQueryEvent(ev)[0])

		case query.EventToolCall:
			events := sess.builder.ConvertQueryEvent(ev)
			for _, oe := range events {
				_ = sess.writer.WriteEvent(oe)
			}

		case query.EventToolResult:
			events := sess.builder.ConvertQueryEvent(ev)
			for _, oe := range events {
				_ = sess.writer.WriteEvent(oe)
			}

		case query.EventError:
			_ = sess.writer.WriteEvent(sess.builder.BuildError(ev.Error))

		case query.EventDone:
			lastUsage = ev.Usage
		}
		_ = sess.writer.Flush()
	}

	elapsed := time.Since(start)
	_ = sess.writer.WriteEvent(sess.builder.BuildDone(lastUsage, elapsed))
	_ = sess.writer.Flush()

	cancel()
	sess.cancel = nil
}

// handleCancel cancels the currently running query.
func (s *ControlServer) handleCancel(sess *session) {
	if sess.cancel != nil {
		sess.cancel()
		_ = sess.writer.WriteEvent(OutputEvent{Type: EventError, Text: "Cancelled."})
		_ = sess.writer.Flush()
	}
}

// handleSwitchModel switches the LLM model.
func (s *ControlServer) handleSwitchModel(sess *session, modelName string) {
	// Close old engine.
	sess.engine.Close()

	// Recreate engine with new model (handled by the factory callback).
	sess.engine = s.recreateEngine()

	_ = sess.writer.WriteEvent(OutputEvent{
		Type: EventInfo,
		Text: fmt.Sprintf("Switched to %s", modelName),
	})
	_ = sess.writer.Flush()
}

// handleSlashCommand processes slash commands locally on the server.
func (s *ControlServer) handleSlashCommand(sess *session, line string) {
	switch {
	case len(line) >= 5 && line[:5] == "/help":
		help := `  /help               — this help
  /model pro|flash    — switch model
  /clear              — reset conversation
  exit, Ctrl+C        — quit

Agent types: planner, architect, critic, executor, analyst, code-reviewer, verifier
`
		_ = sess.writer.WriteEvent(OutputEvent{Type: EventTextDelta, Text: help})
		_ = sess.writer.WriteEvent(OutputEvent{Type: EventDone})
		_ = sess.writer.Flush()

	case len(line) >= 7 && line[:6] == "/clear":
		sess.engine.Close()
		sess.engine = s.engineFactory()
		sess.builder.Reset()
		_ = sess.writer.WriteEvent(OutputEvent{Type: EventInfo, Text: "Conversation reset."})
		_ = sess.writer.Flush()

	case len(line) >= 7 && line[:6] == "/model":
		// Model switch handled via CmdSwitchModel — here just forward.
		_ = sess.writer.WriteEvent(OutputEvent{
			Type: EventInfo,
			Text: "Use client-side /model command.",
		})
		_ = sess.writer.Flush()

	default:
		_ = sess.writer.WriteEvent(OutputEvent{
			Type: EventInfo,
			Text: fmt.Sprintf("Unknown: %s. Type /help", line),
		})
		_ = sess.writer.Flush()
	}
}
