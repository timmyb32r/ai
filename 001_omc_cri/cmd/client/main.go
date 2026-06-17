// Command client is the thin CRI-radio console client. It connects to the
// server's two streams: it pipes GET /v1/stream/audio into a media player
// (ffplay by default, reading PCM from stdin) and concurrently consumes the
// GET /v1/stream/subtitles SSE stream, printing each Chinese subtitle line with
// its timestamp. Ctrl-C cancels cleanly and kills the player.
//
// It imports ONLY the standard library — neither chineseasr nor sherpa nor any
// internal package — so the client stays standalone and dependency-light.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
)

// subtitle is a local mirror of the server's SubtitleEvent JSON. It is defined
// here deliberately so the client need not import the broadcast package.
type subtitle struct {
	Start  float64 `json:"start"`
	End    float64 `json:"end"`
	TextZh string  `json:"text_zh"`
}

func main() {
	server := flag.String("server", "http://localhost:8080", "server base URL")
	player := flag.String("player", "ffplay", "media player binary that reads PCM from stdin")
	flag.Parse()

	base := strings.TrimRight(*server, "/")

	// Cancel everything on Ctrl-C / SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, base, *player); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "client:", err)
		os.Exit(1)
	}
}

// run starts the audio pipeline and the subtitle reader concurrently and
// returns when either fails or the context is cancelled. The first error from
// either goroutine cancels the other.
func run(ctx context.Context, base, player string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errc := make(chan error, 2)

	go func() { errc <- playAudio(ctx, base+"/v1/stream/audio", player) }()
	go func() { errc <- readSubtitles(ctx, base+"/v1/stream/subtitles") }()

	// Wait for the first goroutine to finish (error, EOF, or cancellation),
	// then cancel the other and drain it.
	first := <-errc
	cancel()
	<-errc

	if first != nil && !errors.Is(first, context.Canceled) {
		return first
	}
	return ctx.Err()
}

// playAudio GETs the audio stream and pipes the response body into the player's
// stdin. ffplay is invoked as `ffplay -nodisp -autoexit -i -` (read from stdin).
// The player is killed when ctx is cancelled.
func playAudio(ctx context.Context, url, player string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("audio request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("audio stream HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// `-i -` makes ffplay read the container from stdin; -nodisp/-autoexit keep
	// it headless and exit when the stream ends. Players other than ffplay that
	// also read from stdin work via the same args (mpv accepts `-` too).
	cmd := exec.CommandContext(ctx, player, "-nodisp", "-autoexit", "-i", "-")
	cmd.Stdin = resp.Body
	cmd.Stdout = os.Stderr // player diagnostics must not pollute subtitle stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start player %q: %w", player, err)
	}
	if err := cmd.Wait(); err != nil {
		// A kill triggered by ctx cancellation is expected, not a real failure.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("player %q: %w", player, err)
	}
	return nil
}

// readSubtitles GETs the SSE subtitle stream and prints each subtitle line. SSE
// frames are blank-line-delimited; lines starting with "data: " carry payload,
// and an "event: error" frame signals a server-side error which is surfaced
// clearly. A reconnecting client always rejoins at the live head (the server
// ignores Last-Event-ID), so no replay is attempted here.
func readSubtitles(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("subtitles request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("subtitle stream HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	sc := bufio.NewScanner(resp.Body)
	// Allow large subtitle frames without bufio.ErrTooLong.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		event string          // current frame's `event:` field (default "message")
		data  strings.Builder // accumulated `data:` lines for the current frame
	)

	flush := func() error {
		defer func() { event = ""; data.Reset() }()
		payload := data.String()
		if payload == "" {
			return nil
		}
		if event == "error" {
			return fmt.Errorf("server error event: %s", payload)
		}
		var s subtitle
		if err := json.Unmarshal([]byte(payload), &s); err != nil {
			// Non-JSON data line (e.g. a comment/keep-alive that slipped through);
			// surface it raw rather than dropping it silently.
			fmt.Printf("[subtitle] %s\n", payload)
			return nil
		}
		fmt.Printf("[%7.2f-%7.2f] %s\n", s.Start, s.End, s.TextZh)
		return nil
	}

	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "": // end of one SSE frame
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"): // SSE comment / keep-alive
			continue
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		default:
			// Other SSE fields (id:, retry:) are intentionally ignored.
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("subtitle stream read: %w", err)
	}
	// Stream ended: flush any trailing frame not terminated by a blank line.
	return flush()
}
