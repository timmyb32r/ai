// Package asrerr defines the sentinel errors shared across the chineseasr
// module. Keeping them in an internal leaf package (importing only stdlib)
// lets every other package — including the root — reference the same error
// values without creating an import cycle.
package asrerr

import "errors"

var (
	// ErrFFmpegNotFound is returned when the configured ffmpeg binary path
	// does not point to an existing, usable file.
	ErrFFmpegNotFound = errors.New("chineseasr: ffmpeg binary not found at configured path")

	// ErrSherpaNotFound is returned when the configured sherpa-onnx-offline
	// binary path does not point to an existing, usable file.
	ErrSherpaNotFound = errors.New("chineseasr: sherpa-onnx-offline binary not found at configured path")

	// ErrModelNotFound is returned when the model directory is missing the
	// required model file(s) and/or tokens.txt for the selected model.
	ErrModelNotFound = errors.New("chineseasr: model files not found in configured model directory")

	// ErrModelNotImplemented is returned when a requested model is recognized
	// but not yet wired into the engine arg builder.
	ErrModelNotImplemented = errors.New("chineseasr: model not implemented")

	// ErrAudioNotFound is returned when the input audio path does not point to
	// an existing, regular file.
	ErrAudioNotFound = errors.New("chineseasr: audio input file not found")

	// ErrRemoteInput is returned when the input audio path is a remote/URL
	// reference rather than a local file; remote inputs are rejected to keep
	// the pipeline offline by construction.
	ErrRemoteInput = errors.New("chineseasr: remote audio input rejected; only local files are allowed")

	// ErrDecodeFailed is returned when ffmpeg fails to decode/resample the
	// input audio into the intermediate 16 kHz mono WAV.
	ErrDecodeFailed = errors.New("chineseasr: audio decode failed")

	// ErrToolFailed is returned when the sherpa-onnx-offline subprocess exits
	// non-zero.
	ErrToolFailed = errors.New("chineseasr: sherpa-onnx-offline tool failed")

	// ErrParseFailed is returned when the sherpa-onnx-offline stdout cannot be
	// parsed into a transcript (no JSON block with a text field).
	ErrParseFailed = errors.New("chineseasr: failed to parse sherpa-onnx-offline output")

	// ErrEmptyTranscript is returned when the pipeline succeeds but yields an
	// empty transcript.
	ErrEmptyTranscript = errors.New("chineseasr: empty transcript")

	// ErrSchemaMismatch is returned by Probe when the sherpa-onnx-offline
	// output does not match the expected JSON schema (version drift guard).
	ErrSchemaMismatch = errors.New("chineseasr: sherpa-onnx-offline output schema mismatch")
)
