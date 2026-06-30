package asr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/criradio/server/internal/models"
)

// sherpaTranscriber implements Transcriber via sherpa-onnx-offline CLI subprocess.
type sherpaTranscriber struct {
	sherpaPath string // path to sherpa-onnx-offline binary
	modelDir   string // directory containing model files
	modelID    string // model identifier for arg building ("sense-voice", "paraformer", "whisper")
	language   string
	threads    int
}

// NewSherpaTranscriber creates a Transcriber backed by sherpa-onnx-offline.
func NewSherpaTranscriber(cfg Config) (Transcriber, error) {
	sherpaPath := cfg.SherpaOnnxPath
	if sherpaPath == "" {
		sherpaPath = "sherpa-onnx-offline"
	}

	// Verify binary exists
	if _, err := exec.LookPath(sherpaPath); err != nil {
		return nil, fmt.Errorf("sherpa-onnx-offline not found at %q: %w", sherpaPath, err)
	}

	if cfg.ModelPath == "" {
		return nil, fmt.Errorf("ModelPath (model directory) is required")
	}

	threads := cfg.Threads
	if threads <= 0 {
		threads = 2
	}
	language := cfg.Language
	if language == "" {
		language = "zh"
	}

	// Resolve model ID: from registry codename, or from direct sherpa model ID field
	modelID := ""
	if cfg.ModelCodename != "" {
		if info, ok := LookupModel(cfg.ModelCodename); ok && info.SherpaModelID != "" {
			modelID = info.SherpaModelID
		}
	}
	if modelID == "" {
		return nil, fmt.Errorf("could not determine sherpa model ID for codename %q", cfg.ModelCodename)
	}

	// Validate required model files from registry
	if cfg.ModelCodename != "" {
		if info, ok := LookupModel(cfg.ModelCodename); ok {
			for _, name := range info.RequiredFiles {
				p := filepath.Join(cfg.ModelPath, name)
				if _, err := os.Stat(p); err != nil {
					return nil, fmt.Errorf("required model file %q missing in %s: %w", name, cfg.ModelPath, err)
				}
			}
		}
	}

	return &sherpaTranscriber{
		sherpaPath: sherpaPath,
		modelDir:   cfg.ModelPath,
		modelID:    modelID,
		language:   language,
		threads:    threads,
	}, nil
}

// Transcribe converts PCM float32 to a TranscriptSegment via sherpa-onnx-offline.
//
// Process:
//  1. Write PCM to temporary WAV
//  2. Run sherpa-onnx-offline with model-specific args
//  3. Parse JSON output for text + per-token timestamps
//  4. Build TranscriptSegment with WordEntry per token
func (t *sherpaTranscriber) Transcribe(pcm []float32, segmentID int) (*models.TranscriptSegment, error) {
	if len(pcm) == 0 {
		return nil, fmt.Errorf("empty PCM data")
	}

	wavPath, err := writeWAV(pcm, 16000)
	if err != nil {
		return nil, fmt.Errorf("write WAV: %w", err)
	}
	defer os.Remove(wavPath)

	args := t.buildArgs(wavPath)

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, t.sherpaPath, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("sherpa-onnx-offline failed: %w\nstderr: %s", err, tailStr(stderr.String(), 500))
	}

	result, err := parseSherpaOutput(stdout.Bytes())
	if err != nil {
		return nil, fmt.Errorf("parse sherpa output: %w\nstderr: %s", err, tailStr(stderr.String(), 500))
	}
	if result == nil || result.Text == "" {
		return &models.TranscriptSegment{
			SegmentID: segmentID,
			TextZh:    "",
		}, nil
	}

	// Sherpa-onnx returns space-separated tokens. Remove spaces only
	// between CJK characters/punctuation so GSE receives clean Chinese,
	// but preserve spaces in English text (e.g. "the future" stays intact).
	// Build clean text from tokens so RawTimestamps indices align perfectly
	// with character positions. Insert spaces between ASCII words for
	// readability (e.g. "the future"), with synthetic timestamps so
	// Timestamps[i] always matches fullText[i].
	var textBuilder strings.Builder
	var rawTimestamps []float64
	if len(result.Tokens) > 0 && len(result.Timestamps) == len(result.Tokens) {
		for i, token := range result.Tokens {
			if i > 0 {
				prevASCII := isAllASCII(result.Tokens[i-1])
				currASCII := isAllASCII(token)
				if prevASCII && currASCII {
					textBuilder.WriteByte(' ')
					rawTimestamps = append(rawTimestamps, result.Timestamps[i])
				}
			}
			textBuilder.WriteString(token)
			rawTimestamps = append(rawTimestamps, result.Timestamps[i])
		}
	} else {
		textBuilder.WriteString(strings.TrimSpace(result.Text))
	}
	fullText := textBuilder.String()

	totalDuration := float64(len(pcm)) / 16000.0

	return &models.TranscriptSegment{
		SegmentID:        segmentID,
		TimelineStartSec: 0,
		TimelineEndSec:   totalDuration,
		TextZh:           fullText,
		RawTimestamps:    rawTimestamps,
	}, nil
}

func (t *sherpaTranscriber) Close() error { return nil }

// buildArgs constructs the sherpa-onnx-offline argument slice.
func (t *sherpaTranscriber) buildArgs(wavPath string) []string {
	n := fmt.Sprintf("%d", t.threads)

	switch t.modelID {
	case "sense-voice":
		return []string{
			"--sense-voice-model=" + filepath.Join(t.modelDir, "model.int8.onnx"),
			"--tokens=" + filepath.Join(t.modelDir, "tokens.txt"),
			"--sense-voice-language=" + t.language,
			"--sense-voice-use-itn=1",
			"--debug=0",
			"--num-threads=" + n,
			wavPath,
		}
	case "paraformer":
		return []string{
			"--paraformer=" + filepath.Join(t.modelDir, "model.int8.onnx"),
			"--tokens=" + filepath.Join(t.modelDir, "tokens.txt"),
			"--debug=0",
			"--num-threads=" + n,
			wavPath,
		}
	case "whisper":
		return []string{
			"--whisper-encoder=" + filepath.Join(t.modelDir, "encoder.onnx"),
			"--whisper-decoder=" + filepath.Join(t.modelDir, "decoder.onnx"),
			"--whisper-language=" + t.language,
			"--tokens=" + filepath.Join(t.modelDir, "tokens.txt"),
			"--debug=0",
			"--num-threads=" + n,
			wavPath,
		}
	default:
		// Fallback: unknown model, try common flags
		return []string{
			"--tokens=" + filepath.Join(t.modelDir, "tokens.txt"),
			"--debug=0",
			"--num-threads=" + n,
			wavPath,
		}
	}
}

// ── sherpa-onnx JSON output parsing ─────────────────────────────────────

// sherpaResult holds the parsed sherpa-onnx-offline JSON output.
type sherpaResult struct {
	Text       string
	Timestamps []float64
	Tokens     []string
}

// sherpaResultBlock mirrors the JSON object from a single sherpa-onnx output line.
type sherpaResultBlock struct {
	Text       string    `json:"text"`
	Timestamps []float64 `json:"timestamps"`
	Tokens     []string  `json:"tokens"`
}

// parseSherpaOutput extracts the first JSON result block from sherpa-onnx-offline stdout.
func parseSherpaOutput(stdout []byte) (*sherpaResult, error) {
	// Try line-by-line JSON first
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	const maxBuf = 4 * 1024 * 1024
	scanner.Buffer(make([]byte, maxBuf), maxBuf)

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
		var block sherpaResultBlock
		if err := json.Unmarshal(line, &block); err != nil {
			continue
		}
		return &sherpaResult{
			Text:       block.Text,
			Timestamps: block.Timestamps,
			Tokens:     block.Tokens,
		}, nil
	}

	// Fallback: streaming JSON decoder
	dec := json.NewDecoder(bytes.NewReader(stdout))
	for dec.More() {
		var m map[string]json.RawMessage
		if err := dec.Decode(&m); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("parse JSON: %w", err)
		}
		if _, ok := m["text"]; !ok {
			continue
		}
		var block sherpaResultBlock
		raw, _ := json.Marshal(m)
		if err := json.Unmarshal(raw, &block); err != nil {
			continue
		}
		return &sherpaResult{
			Text:       block.Text,
			Timestamps: block.Timestamps,
			Tokens:     block.Tokens,
		}, nil
	}

	return nil, nil // no result found, not an error — empty audio
}

func isAllASCII(s string) bool {
	for _, r := range s {
		if r > 127 { return false }
	}
	return true
}

func tailStr(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[len(runes)-n:])
}

// removeCJKSpace strips spaces that appear between two CJK characters
// (including CJK punctuation), preserving spaces in English text.
// "在 智能化 ， 绿色 化 the future 。" → "在智能化，绿色化 the future。"
func removeCJKSpace(s string) string {
	runes := []rune(s)
	if len(runes) < 3 {
		return s
	}
	var out []rune
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		// Skip space only if BOTH neighbours are CJK or CJK punctuation
		if r == ' ' && i > 0 && i < len(runes)-1 {
			if isCJKOrPunct(runes[i-1]) && isCJKOrPunct(runes[i+1]) {
				continue
			}
		}
		out = append(out, r)
	}
	return string(out)
}

// isCJKOrPunct returns true for CJK characters and common CJK punctuation.
func isCJKOrPunct(r rune) bool {
	if r >= 0x4E00 && r <= 0x9FFF {
		return true // CJK Unified Ideographs
	}
	if r >= 0x3400 && r <= 0x4DBF {
		return true // CJK Extension A
	}
	if r >= 0x20000 && r <= 0x2A6DF {
		return true // CJK Extension B
	}
	if r >= 0xF900 && r <= 0xFAFF {
		return true // CJK Compatibility Ideographs
	}
	// CJK punctuation and symbols
	if r >= 0x3000 && r <= 0x303F {
		return true // CJK Symbols and Punctuation
	}
	if r >= 0xFF00 && r <= 0xFFEF {
		return true // Halfwidth and Fullwidth Forms
	}
	if r >= 0xFE30 && r <= 0xFE4F {
		return true // CJK Compatibility Forms
	}
	return false
}
