package asr

import (
	"fmt"
	"path/filepath"
)

// Transcriber converts PCM audio to text with per-token timestamps.
type Transcriber interface {
	// Transcribe performs speech recognition on PCM audio data.
	// pcm: float32 samples, 16kHz mono
	// segmentID: segment identifier for logging
	Transcribe(pcm []float32, segmentID int) (*TranscriberResult, error)
	// Close releases resources.
	Close() error
}

// TranscriberResult holds the sherpa-onnx output for a single audio chunk.
type TranscriberResult struct {
	Text        string    // Recognized text (Chinese)
	Timestamps  []float64 // Per-token timestamps (seconds relative to chunk start)
	Tokens      []string  // Raw token strings
}

// Config holds the configuration for any Transcriber implementation.
type Config struct {
	Engine         string // ASR engine ("sherpa-onnx")
	ModelPath      string // Base path to models directory
	ModelCodename  string // Short model name from registry (e.g. "sense-voice-2024")
	Language       string // Language code ("zh")
	Threads        int    // CPU threads (0 = auto)
	SherpaOnnxPath string // Path to sherpa-onnx-offline binary
}

// NewTranscriber creates a sherpa-onnx Transcriber.
// ModelPath should be the base directory (e.g. /opt/models).
// The actual model path is ModelPath/<codename>/.
func NewTranscriber(cfg Config) (Transcriber, error) {
	if cfg.ModelCodename != "" {
		info, ok := LookupModel(cfg.ModelCodename)
		if !ok {
			return nil, fmt.Errorf("unknown model codename: %q", cfg.ModelCodename)
		}
		if cfg.Engine == "" {
			cfg.Engine = string(info.Engine)
		}
		if cfg.Engine == "sherpa-onnx" {
			cfg.ModelPath = filepath.Join(cfg.ModelPath, cfg.ModelCodename)
		}
	}

	switch cfg.Engine {
	case "sherpa-onnx":
		return NewSherpaTranscriber(cfg)
	default:
		return nil, fmt.Errorf("unknown ASR engine: %q (valid: sherpa-onnx)", cfg.Engine)
	}
}
