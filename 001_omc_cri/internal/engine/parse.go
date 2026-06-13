// Package engine drives the sherpa-onnx-offline subprocess and extracts the
// transcript from its output.
package engine

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/timmyb32r/001_omc_cri/internal/asrerr"
)

// resultBlock mirrors the subset of the sherpa-onnx-offline JSON result we care
// about: the transcript text plus a presence flag for the "text" key.
type resultBlock struct {
	Text string `json:"text"`
}

// tailBytes returns up to n bytes from the end of b.
func tailBytes(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[len(b)-n:]
}

// ParseText scans stdout line-by-line looking for JSON result blocks emitted by
// sherpa-onnx-offline. A line is a result block when it starts with '{', is
// valid JSON, and contains a "text" key. The function is tolerant of surrounding
// noise (config-dump lines, status messages, separators) so it works with both
// pure-JSON stdout and mixed-output layouts.
//
// Exactly one result block is expected for a single wav. Two strategies are
// tried, in order:
//
//  1. Fast per-line scan for single-line JSON result blocks (the common case).
//  2. If the line scan finds zero blocks, a streaming json.Decoder fallback
//     over the full stdout decodes successive top-level JSON values, returning
//     the first object that actually carries a "text" key. This recovers a
//     pretty-printed (multi-line) result object the line scan cannot match.
//
// Resolution of the chosen block:
//   - An empty text value returns asrerr.ErrEmptyTranscript.
//   - If neither strategy finds a block, asrerr.ErrParseFailed is returned,
//     wrapping a tail of stderr for diagnostics.
func ParseText(stdout, stderr []byte) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	// Increase the buffer so very long JSON lines (with tokens/timestamps arrays)
	// do not cause a bufio.ErrTooLong. 4 MiB should handle any realistic output.
	const maxBuf = 4 * 1024 * 1024
	scanner.Buffer(make([]byte, maxBuf), maxBuf)

	var first *string

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		// Fast guard: result blocks are JSON objects.
		if line[0] != '{' {
			continue
		}

		// Verify the line is valid JSON before we try to extract "text".
		// This also rejects bare numbers that json.Unmarshal would accept.
		if !json.Valid(line) {
			continue
		}

		// Check that a "text" key is present using a generic map decode.
		// This is cheaper than two full Unmarshal passes for typical lines.
		var m map[string]json.RawMessage
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		if _, ok := m["text"]; !ok {
			continue
		}

		// Extract the typed value.
		var block resultBlock
		if err := json.Unmarshal(line, &block); err != nil {
			continue
		}

		t := block.Text
		first = &t
		break // single-wav: first block is the only one we need
	}

	// Fallback: the line scan found nothing, which happens when sherpa-onnx
	// pretty-prints its JSON result across multiple lines. Stream-decode the
	// full stdout, returning the first top-level object carrying a "text" key.
	if first == nil {
		if t, ok := decodeMultiLine(stdout); ok {
			first = &t
		}
	}

	if first == nil {
		return "", fmt.Errorf("%w: %s", asrerr.ErrParseFailed, tailBytes(stderr, 500))
	}
	if *first == "" {
		return "", asrerr.ErrEmptyTranscript
	}
	return *first, nil
}

// decodeMultiLine streams successive top-level JSON values from stdout and
// returns the text of the first object that contains a "text" key, along with
// true. It returns ("", false) when no such object is present (including when
// stdout carries no decodable JSON at all).
func decodeMultiLine(stdout []byte) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(stdout))
	for dec.More() {
		// Decode into a generic map first so we can distinguish "text present
		// but empty" from "text absent" without a second pass.
		var m map[string]json.RawMessage
		if err := dec.Decode(&m); err != nil {
			if err == io.EOF {
				break
			}
			// Non-object top-level value (or malformed tail): stop scanning.
			return "", false
		}
		raw, ok := m["text"]
		if !ok {
			continue
		}
		var block resultBlock
		if err := json.Unmarshal(raw, &block.Text); err != nil {
			continue
		}
		return block.Text, true
	}
	return "", false
}
