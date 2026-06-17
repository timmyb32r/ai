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

// Result is the full sherpa-onnx-offline JSON output we care about: the
// transcript text, per-token start timestamps, and the token strings.
// Timestamps and Tokens are parallel arrays of equal length; Timestamps[i]
// is the start time (seconds) of Tokens[i] within the transcribed audio.
type Result struct {
	Text       string
	Timestamps []float64
	Tokens     []string
}

// resultBlock mirrors the JSON object decoded from a single sherpa-onnx
// output line. It reads all fields we use; unknown fields are ignored.
type resultBlock struct {
	Text       string    `json:"text"`
	Timestamps []float64 `json:"timestamps"`
	Tokens     []string  `json:"tokens"`
}

// tailBytes returns up to n bytes from the end of b.
func tailBytes(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[len(b)-n:]
}

// Parse scans stdout line-by-line looking for JSON result blocks emitted by
// sherpa-onnx-offline. It returns the full result including timestamps and
// tokens. The same tolerance rules as ParseText apply.
func Parse(stdout, stderr []byte) (*Result, error) {
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	const maxBuf = 4 * 1024 * 1024
	scanner.Buffer(make([]byte, maxBuf), maxBuf)

	var first *Result

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' || !json.Valid(line) {
			continue
		}

		var m map[string]json.RawMessage
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		if _, ok := m["text"]; !ok {
			continue
		}

		var block resultBlock
		if err := json.Unmarshal(line, &block); err != nil {
			continue
		}

		first = &Result{
			Text:       block.Text,
			Timestamps: block.Timestamps,
			Tokens:     block.Tokens,
		}
		break
	}

	if first == nil {
		if r, ok := decodeMultiLineFull(stdout); ok {
			first = r
		}
	}

	if first == nil {
		return nil, fmt.Errorf("%w: %s", asrerr.ErrParseFailed, tailBytes(stderr, 500))
	}
	if first.Text == "" {
		return nil, asrerr.ErrEmptyTranscript
	}
	return first, nil
}

// decodeMultiLineFull is the streaming fallback for Parse, returning the full
// Result.
func decodeMultiLineFull(stdout []byte) (*Result, bool) {
	dec := json.NewDecoder(bytes.NewReader(stdout))
	for dec.More() {
		var m map[string]json.RawMessage
		if err := dec.Decode(&m); err != nil {
			if err == io.EOF {
				break
			}
			return nil, false
		}
		raw, ok := m["text"]
		if !ok {
			continue
		}
		var block resultBlock
		if err := json.Unmarshal(raw, &block.Text); err != nil {
			continue
		}
		// Decode the remaining fields we care about.
		if rawTS, ok := m["timestamps"]; ok {
			json.Unmarshal(rawTS, &block.Timestamps)
		}
		if rawTok, ok := m["tokens"]; ok {
			json.Unmarshal(rawTok, &block.Tokens)
		}
		return &Result{
			Text:       block.Text,
			Timestamps: block.Timestamps,
			Tokens:     block.Tokens,
		}, true
	}
	return nil, false
}
