// Package asr provides automatic speech recognition via whisper.cpp or sherpa-onnx.
package asr

import (
	"fmt"
	"path/filepath"

	"github.com/criradio/server/internal/models"
)

// Transcriber converts PCM audio to text with per-word timestamps.
type Transcriber interface {
	// Transcribe performs speech recognition on PCM audio data.
	// pcm: float32 samples, 16kHz mono
	// segmentID: segment identifier for logging
	Transcribe(pcm []float32, segmentID int) (*models.TranscriptSegment, error)
	// Close releases resources.
	Close() error
}

// Config holds the configuration for any Transcriber implementation.
type Config struct {
	// Engine selects "whisper" or "sherpa-onnx".
	Engine string
	// ModelPath is the path to the model file or directory.
	ModelPath string
	// ModelCodename is the short model name from the registry (e.g. "ggml-small").
	// When set, required files are validated against the registry entry.
	ModelCodename string
	// Language is the language code ("zh" for Chinese, "" for auto-detect).
	Language string
	// Threads is the number of CPU threads (0 = auto).
	Threads int
	// MaxContext is the whisper max context length (-1 = auto). Whisper only.
	MaxContext int

	// ── sherpa-onnx specific ──────────────────────────────────────────
	// SherpaOnnxPath is the path to the sherpa-onnx-offline binary.
	SherpaOnnxPath string
}

// NewTranscriber creates the appropriate Transcriber based on cfg.Engine.
// For sherpa-onnx: ModelPath is the base directory, and the actual model
// lives in ModelPath/<codename>/.
// For whisper: ModelPath is the model file or base dir containing the .bin.
func NewTranscriber(cfg Config) (Transcriber, error) {
	if cfg.ModelCodename != "" {
		info, ok := LookupModel(cfg.ModelCodename)
		if !ok {
			return nil, fmt.Errorf("unknown model codename: %q", cfg.ModelCodename)
		}
		if cfg.Engine == "" {
			cfg.Engine = string(info.Engine)
		}
		// For sherpa-onnx, the model directory is ModelPath/<codename>/
		if cfg.Engine == "sherpa-onnx" {
			cfg.ModelPath = filepath.Join(cfg.ModelPath, cfg.ModelCodename)
		}
	}

	switch cfg.Engine {
	case "whisper":
		return NewWhisperTranscriber(cfg)
	case "sherpa-onnx":
		return NewSherpaTranscriber(cfg)
	default:
		return nil, fmt.Errorf("unknown ASR engine: %q (valid: whisper, sherpa-onnx)", cfg.Engine)
	}
}
