package asr

// Engine identifies which ASR engine to use.
type Engine string

const (
	EngineSherpaOnnx Engine = "sherpa-onnx"
)

// ModelInfo maps a short model codename to its download URL, required files, and target engine.
type ModelInfo struct {
	Codename      string   // Short name (e.g. "sense-voice-2024")
	URL           string   // Download URL for the model archive
	Engine        Engine   // Required engine
	RequiredFiles []string // Files that must exist in model directory after extraction
	SherpaModelID string   // sherpa-onnx model identifier ("sense-voice", "paraformer", "whisper")
	Language      string   // Default language (e.g. "zh")
}

// ModelRegistry maps short codenames to ModelInfo entries.
var ModelRegistry = map[string]ModelInfo{
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
	"paraformer-zh": {
		Codename: "paraformer-zh",
		URL:      "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-paraformer-zh-2023-09-14.tar.bz2",
		Engine:   EngineSherpaOnnx,
		RequiredFiles: []string{"tokens.txt", "model.int8.onnx"},
		SherpaModelID: "paraformer",
		Language:      "zh",
	},
}

// LookupModel returns the ModelInfo for a codename.
func LookupModel(codename string) (ModelInfo, bool) {
	info, ok := ModelRegistry[codename]
	return info, ok
}
