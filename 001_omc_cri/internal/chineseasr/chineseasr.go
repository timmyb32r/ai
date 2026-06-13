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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

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

// Result is the transcription output. It is intentionally minimal in v1 but
// designed to grow without breaking callers (e.g. future Segments []Segment
// or DetectedLanguage string fields).
type Result struct {
	// Text is the recognized Simplified-Chinese text, with punctuation in v1.
	Text string
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

// Probe is the recommended startup check: the caller supplies a known-good
// sample WAV (sampleWavPath, an existing regular file already in 16 kHz mono
// form), and Probe runs the configured engine once against it and asserts that
// its output still parses into a transcript. It is a startup-time guard against
// sherpa-onnx version/schema drift: when the binary produces output the parser
// no longer understands, Probe reports ErrSchemaMismatch.
//
// Probe deliberately skips the ffmpeg decode step and feeds the sample straight
// to the engine. It returns:
//   - nil on a successful, parseable transcript;
//   - ctx.Err() if ctx is already done or is cancelled during the run;
//   - ErrAudioNotFound if sampleWavPath is not an existing regular file;
//   - ErrSchemaMismatch if the engine ran but its output could not be parsed
//     into a transcript (ErrParseFailed / ErrEmptyTranscript drift);
//   - the underlying error otherwise (e.g. the binary failing to launch, which
//     surfaces as ErrToolFailed).
func (t *Transcriber) Probe(ctx context.Context, sampleWavPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// The sample must be an existing regular file; a missing path or a
	// directory is a caller error surfaced as ErrAudioNotFound.
	if err := requireFile(sampleWavPath); err != nil {
		return fmt.Errorf("%w: %s", ErrAudioNotFound, sampleWavPath)
	}

	args, err := engine.Build(string(t.cfg.Model), t.cfg.ModelDir, t.cfg.Language, t.punctuationEnabled(), t.cfg.NumThreads, sampleWavPath)
	if err != nil {
		return err
	}

	_, err = engine.Recognize(ctx, t.run, t.cfg.SherpaOfflinePath, args)
	if err != nil {
		if ce := ctx.Err(); ce != nil {
			return ce
		}
		// A parse/empty result here means the output schema no longer matches
		// what the parser expects: surface it as a schema-drift signal, keeping
		// the underlying error attached via the dual-%w form. A tool/exec
		// failure (ErrToolFailed) is surfaced as-is.
		if errorsIsSchemaDrift(err) {
			return fmt.Errorf("%w: %w", ErrSchemaMismatch, err)
		}
		return err
	}
	return nil
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

	text, err := engine.Recognize(ctx, t.run, t.cfg.SherpaOfflinePath, args)
	if err != nil {
		// Surface a cancelled/expired context as the context error rather than
		// the wrapped tool failure it manifested as.
		if ce := ctx.Err(); ce != nil {
			return nil, ce
		}
		return nil, err
	}

	if text == "" {
		return nil, ErrEmptyTranscript
	}
	return &Result{Text: text}, nil
}

// errorsIsSchemaDrift reports whether err indicates the engine output failed to
// parse into a transcript (the schema-drift signals that Probe maps to
// ErrSchemaMismatch).
func errorsIsSchemaDrift(err error) bool {
	return errors.Is(err, ErrParseFailed) || errors.Is(err, ErrEmptyTranscript)
}

// Segment is one silence-bounded speech region of a wav, transcribed to
// Simplified-Chinese text. Start and End are seconds within the wav (the
// region's playback offsets); Text is the recognized transcript for that
// region (with punctuation in v1). Segments are silence-bounded by
// construction, so there are no mid-utterance cuts.
type Segment struct {
	// Start is the region start, in seconds from the beginning of the wav.
	Start float64
	// End is the region end, in seconds from the beginning of the wav.
	End float64
	// Text is the recognized Simplified-Chinese transcript for the region.
	Text string
}

// Silence-detection defaults for TranscribeSegments. They are unexported
// consts for v1 (a Config knob is out of scope): noise threshold and the
// minimum silence duration that separates speech regions.
const (
	// silenceNoiseDB is the silencedetect noise floor (anything quieter counts
	// as silence). Passed as silencedetect=noise=<silenceNoiseDB>dB.
	silenceNoiseDB = -30
	// silenceMinDurSec is the minimum silence run (seconds) that splits two
	// speech regions. Passed as silencedetect=d=<silenceMinDurSec>.
	silenceMinDurSec = 0.5
)

// TranscribeSegments segments wavPath at silence boundaries (via ffmpeg
// silencedetect) and transcribes each speech region offline with the existing
// Transcribe path, returning one Segment per silence-bounded region with its
// timeline offsets. It is the additive, segment-aware companion to Transcribe
// used by the broadcast ASR driver; it stays fully offline/file-based and does
// not change Transcribe or the single-block parser in internal/engine.
//
// It runs ffmpeg once for silencedetect, then for each silence-bounded region
// cuts a temp wav slice (-ss/-to) and feeds it to the existing Transcribe.
// Empty-text regions are skipped. A trailing region that is not
// silence-terminated (End == 0, per engine.SpeechRegion's convention) is left
// unsegmented for the caller's next pass — it is neither sliced nor returned.
//
// TODO(worker-seg): this runs ffmpeg twice per region (the slice cut here plus
// Transcribe's own decode). A future optimization is in-process PCM slicing of
// the already-16kHz-mono wav to avoid the extra ffmpeg invocations and temp
// files.
func (t *Transcriber) TranscribeSegments(ctx context.Context, wavPath string) ([]Segment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := requireFile(wavPath); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrAudioNotFound, wavPath)
	}

	// 1. Run silencedetect over the whole wav, capturing stderr (the filter
	// logs its markers there, not on stdout). A non-zero ffmpeg exit is a
	// decode failure.
	detectArgs := []string{
		"-hide_banner",
		"-nostdin",
		"-protocol_whitelist", "file,pipe",
		"-i", wavPath,
		"-af", fmt.Sprintf("silencedetect=noise=%ddB:d=%s", silenceNoiseDB, strconv.FormatFloat(silenceMinDurSec, 'f', -1, 64)),
		"-f", "null",
		"-",
	}
	_, stderr, runErr := t.run.Run(ctx, t.cfg.FFmpegPath, detectArgs...)
	if runErr != nil {
		if ce := ctx.Err(); ce != nil {
			return nil, ce
		}
		return nil, fmt.Errorf("%w: %s: %w", ErrDecodeFailed, stderrTail(stderr, 500), runErr)
	}

	regions := engine.ParseSilence(stderr)

	// 2. For each silence-bounded region, cut a slice and transcribe it.
	var segments []Segment
	for _, region := range regions {
		// A not-silence-terminated tail (End == 0) is left for the caller's
		// next pass: do not slice or transcribe an open-ended region.
		if region.End <= region.Start {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		text, err := t.transcribeRegion(ctx, wavPath, region.Start, region.End)
		if err != nil {
			// An empty slice transcript is expected for marginal regions; skip
			// it rather than failing the whole call.
			if errors.Is(err, ErrEmptyTranscript) {
				continue
			}
			return nil, err
		}
		if text == "" {
			continue
		}
		segments = append(segments, Segment{Start: region.Start, End: region.End, Text: text})
	}

	return segments, nil
}

// transcribeRegion cuts [start,end] seconds out of wavPath into a temp wav with
// ffmpeg (via the injected runner) and transcribes the slice with the existing
// Transcribe path. The temp slice is always removed before returning.
func (t *Transcriber) transcribeRegion(ctx context.Context, wavPath string, start, end float64) (string, error) {
	tmpFile, err := os.CreateTemp(t.cfg.TempDir, "chineseasr-seg-*.wav")
	if err != nil {
		return "", fmt.Errorf("chineseasr: create slice temp file: %w", err)
	}
	slicePath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(slicePath) //nolint:errcheck

	// Cut the region. -ss/-to are in seconds; -c copy would risk inexact cuts
	// on a re-encode boundary, so re-encode the slice to the same 16 kHz mono
	// pcm_s16le wav the engine expects.
	cutArgs := []string{
		"-hide_banner",
		"-nostdin",
		"-protocol_whitelist", "file,pipe",
		"-ss", strconv.FormatFloat(start, 'f', -1, 64),
		"-to", strconv.FormatFloat(end, 'f', -1, 64),
		"-i", wavPath,
		"-ar", "16000",
		"-ac", "1",
		"-c:a", "pcm_s16le",
		"-f", "wav",
		"-y",
		slicePath,
	}
	_, stderr, runErr := t.run.Run(ctx, t.cfg.FFmpegPath, cutArgs...)
	if runErr != nil {
		if ce := ctx.Err(); ce != nil {
			return "", ce
		}
		return "", fmt.Errorf("%w: %s: %w", ErrDecodeFailed, stderrTail(stderr, 500), runErr)
	}

	// Transcribe the slice through the existing path. Transcribe re-decodes the
	// slice with ffmpeg (the documented double-ffmpeg cost noted above).
	res, err := t.Transcribe(ctx, slicePath)
	if err != nil {
		return "", err
	}
	return res.Text, nil
}

// stderrTail returns at most maxBytes from the end of b, as a string. It
// mirrors the helper in internal/audio for wrapping ffmpeg stderr context.
func stderrTail(b []byte, maxBytes int) string {
	if len(b) > maxBytes {
		b = b[len(b)-maxBytes:]
	}
	return string(b)
}
