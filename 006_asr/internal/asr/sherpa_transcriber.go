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
)

// sherpaTranscriber implements Transcriber via sherpa-onnx-offline CLI subprocess.
type sherpaTranscriber struct {
	sherpaPath string
	modelDir   string
	modelID    string
	language   string
	threads    int
}

// NewSherpaTranscriber creates a Transcriber backed by sherpa-onnx-offline.
func NewSherpaTranscriber(cfg Config) (Transcriber, error) {
	sherpaPath := cfg.SherpaOnnxPath
	if sherpaPath == "" {
		sherpaPath = "sherpa-onnx-offline"
	}

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

	modelID := ""
	if cfg.ModelCodename != "" {
		if info, ok := LookupModel(cfg.ModelCodename); ok && info.SherpaModelID != "" {
			modelID = info.SherpaModelID
		}
	}
	if modelID == "" {
		return nil, fmt.Errorf("could not determine sherpa model ID for codename %q", cfg.ModelCodename)
	}

	// Validate required model files
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

// Transcribe converts PCM float32 to text + timestamps via sherpa-onnx-offline.
func (t *sherpaTranscriber) Transcribe(pcm []float32, segmentID int) (*TranscriberResult, error) {
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
		return &TranscriberResult{}, nil
	}

	// Build clean text from tokens preserving CJK spacing
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

	return &TranscriberResult{
		Text:       fullText,
		Timestamps: rawTimestamps,
		Tokens:     result.Tokens,
	}, nil
}

func (t *sherpaTranscriber) Close() error { return nil }

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
		return []string{
			"--tokens=" + filepath.Join(t.modelDir, "tokens.txt"),
			"--debug=0",
			"--num-threads=" + n,
			wavPath,
		}
	}
}

// ── JSON output parsing ──────────────────────────────────────────────────

type sherpaResult struct {
	Text       string
	Timestamps []float64
	Tokens     []string
}

type sherpaResultBlock struct {
	Text       string    `json:"text"`
	Timestamps []float64 `json:"timestamps"`
	Tokens     []string  `json:"tokens"`
}

func parseSherpaOutput(stdout []byte) (*sherpaResult, error) {
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

	return nil, nil
}

func isAllASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
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
