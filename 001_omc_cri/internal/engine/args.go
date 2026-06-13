// Package engine builds sherpa-onnx-offline argument slices and parses its
// output. It is an internal package: no external consumers.
package engine

import (
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/timmyb32r/001_omc_cri/internal/asrerr"
)

// Build constructs the argument slice passed to the sherpa-onnx-offline binary
// for the given model. The binary name itself is NOT included — it is provided
// separately as sherpaPath to Recognize.
//
// Assumed sherpa-onnx package file layout (conventional; adjust paths if the
// downloaded package uses different names):
//
//	sense-voice : model.int8.onnx, tokens.txt
//	paraformer  : model.int8.onnx, tokens.txt
//	whisper     : encoder.onnx, decoder.onnx, tokens.txt
//
// All model files are resolved relative to modelDir via filepath.Join.
func Build(model, modelDir, language string, punctuation bool, numThreads int, wavPath string) ([]string, error) {
	n := strconv.Itoa(numThreads)

	switch model {
	case "sense-voice":
		itn := "0"
		if punctuation {
			itn = "1"
		}
		return []string{
			"--sense-voice-model=" + filepath.Join(modelDir, "model.int8.onnx"),
			"--tokens=" + filepath.Join(modelDir, "tokens.txt"),
			"--sense-voice-language=" + language,
			"--sense-voice-use-itn=" + itn,
			"--debug=0",
			"--num-threads=" + n,
			wavPath,
		}, nil

	case "paraformer":
		// Paraformer's language is fixed by the trained model itself, so no
		// language flag is emitted (and none is accepted by the binary).
		return []string{
			"--paraformer=" + filepath.Join(modelDir, "model.int8.onnx"),
			"--tokens=" + filepath.Join(modelDir, "tokens.txt"),
			"--debug=0",
			"--num-threads=" + n,
			wavPath,
		}, nil

	case "whisper":
		return []string{
			"--whisper-encoder=" + filepath.Join(modelDir, "encoder.onnx"),
			"--whisper-decoder=" + filepath.Join(modelDir, "decoder.onnx"),
			"--whisper-language=" + language,
			"--tokens=" + filepath.Join(modelDir, "tokens.txt"),
			"--debug=0",
			"--num-threads=" + n,
			wavPath,
		}, nil

	default:
		return nil, fmt.Errorf("%w: %q", asrerr.ErrModelNotImplemented, model)
	}
}
