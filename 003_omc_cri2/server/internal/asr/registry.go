package asr

// Engine identifies which ASR engine to use.
type Engine string

const (
	EngineWhisper    Engine = "whisper"
	EngineSherpaOnnx Engine = "sherpa-onnx"
)

// ModelInfo maps a short model codename to its download URL, required files,
// and target engine.
type ModelInfo struct {
	// Codename is the short name used in env var ASR_MODEL (e.g. "sense-voice-2024").
	Codename string
	// URL is the public download URL for the model archive/bundle.
	URL string
	// Engine is the ASR engine required by this model.
	Engine Engine
	// RequiredFiles lists the files that must exist in the model directory
	// after extraction for validation. Paths are relative to model dir.
	RequiredFiles []string
	// SherpaModelID is the model identifier passed to sherpa-onnx-offline
	// (--sense-voice-model, --paraformer, --whisper-encoder/--whisper-decoder).
	// Only meaningful for EngineSherpaOnnx.
	SherpaModelID string
	// Language is the default language for this model. Empty means auto-detect.
	Language string
}

// ModelRegistry maps short codenames to ModelInfo entries.
// Add new models here with a stable codename and a verified download URL.
var ModelRegistry = map[string]ModelInfo{
	// ── SenseVoice models (sherpa-onnx engine) ──────────────────────────
	"sense-voice-2024": {
		Codename: "sense-voice-2024",
		URL:      "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2024-07-17.tar.bz2",
		Engine:   EngineSherpaOnnx,
		RequiredFiles: []string{"tokens.txt", "model.int8.onnx"},
		SherpaModelID: "sense-voice",
		Language:      "zh",
	},
	"sense-voice-v1": {
		Codename: "sense-voice-v1",
		URL:      "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2024-07-17.tar.bz2",
		Engine:   EngineSherpaOnnx,
		RequiredFiles: []string{"tokens.txt", "model.int8.onnx"},
		SherpaModelID: "sense-voice",
		Language:      "zh",
	},

	// ── Paraformer models (sherpa-onnx engine) ─────────────────────────
	"paraformer-zh": {
		Codename: "paraformer-zh",
		URL:      "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-paraformer-zh-2023-09-14.tar.bz2",
		Engine:   EngineSherpaOnnx,
		RequiredFiles: []string{"tokens.txt", "model.int8.onnx"},
		SherpaModelID: "paraformer",
		Language:      "zh",
	},

	// ── sherpa-onnx Whisper models (sherpa-onnx engine, ONNX runtime) ──
	"sherpa-whisper-small": {
		Codename: "sherpa-whisper-small",
		URL:      "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-whisper-small.tar.bz2",
		Engine:   EngineSherpaOnnx,
		RequiredFiles: []string{"tokens.txt", "encoder.onnx", "decoder.onnx"},
		SherpaModelID: "whisper",
		Language:      "zh",
	},

	// ── whisper.cpp GGML models (whisper engine, whisper-cli binary) ───
	"ggml-small": {
		Codename: "ggml-small",
		URL:      "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.bin",
		Engine:   EngineWhisper,
		RequiredFiles: []string{"ggml-small.bin"},
		Language:      "zh",
	},
	"ggml-large": {
		Codename: "ggml-large",
		URL:      "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3.bin",
		Engine:   EngineWhisper,
		RequiredFiles: []string{"ggml-large-v3.bin"},
		Language:      "zh",
	},
	"ggml-medium": {
		Codename: "ggml-medium",
		URL:      "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-medium.bin",
		Engine:   EngineWhisper,
		RequiredFiles: []string{"ggml-medium.bin"},
		Language:      "zh",
	},
	"ggml-tiny": {
		Codename: "ggml-tiny",
		URL:      "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-tiny.bin",
		Engine:   EngineWhisper,
		RequiredFiles: []string{"ggml-tiny.bin"},
		Language:      "zh",
	},
}

// LookupModel returns the ModelInfo for a codename. The second return value
// is false if the codename is not registered.
func LookupModel(codename string) (ModelInfo, bool) {
	info, ok := ModelRegistry[codename]
	return info, ok
}
