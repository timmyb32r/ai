package chineseasr

import (
	"github.com/timmyb32r/001_omc_cri/internal/asrerr"
)

// Public re-exports of the internal sentinel errors. Callers match these with
// errors.Is. They are aliased from internal/asrerr so that the internal
// packages and the root package share a single set of error values without an
// import cycle (internal packages never import the root).
var (
	ErrFFmpegNotFound      = asrerr.ErrFFmpegNotFound
	ErrSherpaNotFound      = asrerr.ErrSherpaNotFound
	ErrModelNotFound       = asrerr.ErrModelNotFound
	ErrModelNotImplemented = asrerr.ErrModelNotImplemented
	ErrAudioNotFound       = asrerr.ErrAudioNotFound
	ErrRemoteInput         = asrerr.ErrRemoteInput
	ErrDecodeFailed        = asrerr.ErrDecodeFailed
	ErrToolFailed          = asrerr.ErrToolFailed
	ErrParseFailed         = asrerr.ErrParseFailed
	ErrEmptyTranscript     = asrerr.ErrEmptyTranscript
	ErrSchemaMismatch      = asrerr.ErrSchemaMismatch
)
