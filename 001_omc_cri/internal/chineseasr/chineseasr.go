// Package chineseasr is a pure-Go (CGO_ENABLED=0 buildable) library that
// transcribes Chinese-speech audio files to Simplified-Chinese text with
// punctuation, fully offline. It drives two external binaries as
// subprocesses: ffmpeg (decode/resample to 16 kHz mono WAV) and
// sherpa-onnx-offline (ASR, default model SenseVoice-Small). The caller
// supplies the binary and model paths via Config.
//
// The library never touches the network at runtime; remote inputs are
// rejected and ffmpeg is restricted to file protocols.
package chineseasr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/timmyb32r/001_omc_cri/internal/audio"
	"github.com/timmyb32r/001_omc_cri/internal/engine"
	"github.com/timmyb32r/001_omc_cri/internal/runner"
)

// Model selects which ASR model the engine drives. Only wired-and-exported
// models are valid; future models (e.g. FireRedASR, Qwen3-ASR) are not
// exported until wired and verified.
type Model string

const (
	// ModelSenseVoice is the default model (SenseVoice-Small),
	// integration-verified for v1.
	ModelSenseVoice Model = "sense-voice"
	// ModelParaformer has a wired arg builder; accuracy is not eyeball-verified
	// in v1.
	ModelParaformer Model = "paraformer"
	// ModelWhisper has a wired arg builder; accuracy is not eyeball-verified in
	// v1.
	ModelWhisper Model = "whisper"
)

// Config holds the caller-provided configuration for a Transcriber. The three
// path fields are required; the remaining fields have defaults applied by New.
type Config struct {
	// FFmpegPath is the path to the ffmpeg binary (required).
	FFmpegPath string
	// SherpaOfflinePath is the path to the sherpa-onnx-offline binary
	// (required).
	SherpaOfflinePath string
	// ModelDir is the directory containing the model file(s) and tokens.txt
	// for the selected Model (required).
	ModelDir string
	// Model selects the ASR model. Defaults to ModelSenseVoice.
	Model Model
	// Language is the source language hint. Defaults to "zh" (SenseVoice
	// accepts zh/yue/en/ja/ko/auto).
	Language string
	// Punctuation controls inverse text normalization so the output carries
	// punctuation (SenseVoice: maps to --sense-voice-use-itn=1). A nil pointer
	// means "use the default", which is ON; set it to a pointer to false to
	// disable punctuation explicitly. (A plain bool cannot distinguish an unset
	// field from an explicit false, hence the pointer.)
	Punctuation *bool
	// NumThreads is the engine thread count. Defaults to 2.
	NumThreads int
	// TempDir is where intermediate WAV files are written. Defaults to
	// os.TempDir().
	TempDir string
}

// Result is the transcription output.
type Result struct {
	// Text is the recognized Simplified-Chinese text, with punctuation in v1.
	Text string
	// Timestamps are per-token start times (seconds) from the SenseVoice model,
	// parallel to Tokens. Length matches Tokens; Timestamps[i] corresponds to
	// the start time of Tokens[i] within the transcribed audio.
	Timestamps []float64
	// Tokens are the individual character tokens as output by SenseVoice.
	Tokens []string
}

// Transcriber transcribes audio files using the configured external tools.
// Construct it with New. It is safe to reuse across calls.
type Transcriber struct {
	cfg Config
	run runner.Runner
}

// New builds a Transcriber from cfg, applying defaults for any unset optional
// fields, validating the binary and model paths, and wiring the production
// runner.
//
// Defaults: Model -> ModelSenseVoice, Language -> "zh", NumThreads -> 2,
// TempDir -> os.TempDir(). Punctuation is left as configured (nil meaning the
// default, ON).
//
// Validation errors (match with errors.Is):
//   - ErrFFmpegNotFound      – FFmpegPath is not an existing file
//   - ErrSherpaNotFound      – SherpaOfflinePath is not an existing file
//   - ErrModelNotImplemented – Model is not a wired model
//   - ErrModelNotFound       – ModelDir is missing, not a directory, or lacks
//     the required model file(s) / tokens.txt for the selected model
func New(cfg Config) (*Transcriber, error) {
	if cfg.Model == "" {
		cfg.Model = ModelSenseVoice
	}
	if cfg.Language == "" {
		cfg.Language = "zh"
	}
	if cfg.NumThreads <= 0 {
		cfg.NumThreads = 2
	}
	if cfg.TempDir == "" {
		cfg.TempDir = os.TempDir()
	}
	// Punctuation is left as-is: a nil pointer is interpreted as the default
	// (ON) at Build time, a non-nil pointer is honored verbatim.

	// Validate the ffmpeg binary path: must be an existing, non-directory file.
	if err := requireFile(cfg.FFmpegPath); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrFFmpegNotFound, cfg.FFmpegPath)
	}
	// Validate the sherpa-onnx-offline binary path.
	if err := requireFile(cfg.SherpaOfflinePath); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrSherpaNotFound, cfg.SherpaOfflinePath)
	}

	// Reject unwired models before touching the model directory.
	modelFiles, ok := requiredModelFiles[cfg.Model]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrModelNotImplemented, string(cfg.Model))
	}

	// ModelDir must be an existing directory.
	fi, err := os.Stat(cfg.ModelDir)
	if err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%w: model directory %q is not a directory", ErrModelNotFound, cfg.ModelDir)
	}
	// Every required file (tokens.txt + the per-model model file(s)) must exist.
	for _, name := range modelFiles {
		p := filepath.Join(cfg.ModelDir, name)
		if err := requireFile(p); err != nil {
			return nil, fmt.Errorf("%w: missing %s in %q", ErrModelNotFound, name, cfg.ModelDir)
		}
	}

	return &Transcriber{
		cfg: cfg,
		run: runner.Exec{},
	}, nil
}

// requiredModelFiles maps each wired Model to the files that must exist inside
// ModelDir (in addition to being a valid directory). The set mirrors the
// internal/engine arg builder's assumed sherpa-onnx package layout.
var requiredModelFiles = map[Model][]string{
	ModelSenseVoice: {"tokens.txt", "model.int8.onnx"},
	ModelParaformer: {"tokens.txt", "model.int8.onnx"},
	ModelWhisper:    {"tokens.txt", "encoder.onnx", "decoder.onnx"},
}

// requireFile returns nil only when path points to an existing, non-directory
// file. A non-nil error (the underlying stat error or a sentinel for a
// directory) means the path is unusable as a file.
func requireFile(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("path is a directory, not a file: %s", path)
	}
	return nil
}

// punctuationEnabled resolves the Punctuation pointer to a plain bool, applying
// the default (ON) when the pointer is nil.
func (t *Transcriber) punctuationEnabled() bool {
	if t.cfg.Punctuation != nil {
		return *t.cfg.Punctuation
	}
	return true
}

// setRunner injects a Runner, used by tests to substitute a fake for the real
// Exec runner. Unexported on purpose: it is a test seam, not public API.
func (t *Transcriber) setRunner(r runner.Runner) {
	t.run = r
}

// Transcribe converts audioPath to 16 kHz mono WAV with ffmpeg, runs the
// configured model via sherpa-onnx-offline, and returns the recognized text.
//
// The intermediate WAV is always cleaned up before returning, on every path.
// Errors (match with errors.Is): ErrRemoteInput / ErrAudioNotFound for a bad
// input, ErrDecodeFailed if ffmpeg fails, ErrToolFailed / ErrParseFailed if the
// engine fails, ErrEmptyTranscript on a successful-but-empty result, and
// context.Canceled / context.DeadlineExceeded when ctx is done.
func (t *Transcriber) Transcribe(ctx context.Context, audioPath string) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Decode/resample to a temp 16 kHz mono WAV. cleanup is always non-nil, so
	// defer it unconditionally to guarantee the temp file is removed.
	wav, cleanup, err := audio.Convert(ctx, t.run, t.cfg.FFmpegPath, audioPath, t.cfg.TempDir)
	defer cleanup()
	if err != nil {
		return nil, err
	}

	// Re-check cancellation between the two subprocess stages.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	args, err := engine.Build(string(t.cfg.Model), t.cfg.ModelDir, t.cfg.Language, t.punctuationEnabled(), t.cfg.NumThreads, wav)
	if err != nil {
		return nil, err
	}

	res, err := engine.Recognize(ctx, t.run, t.cfg.SherpaOfflinePath, args)
	if err != nil {
		// Surface a cancelled/expired context as the context error rather than
		// the wrapped tool failure it manifested as.
		if ce := ctx.Err(); ce != nil {
			return nil, ce
		}
		return nil, err
	}

	if res.Text == "" {
		return nil, ErrEmptyTranscript
	}
	return &Result{Text: res.Text, Timestamps: res.Timestamps, Tokens: res.Tokens}, nil
}
