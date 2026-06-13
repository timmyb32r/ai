// Package audio provides audio conversion helpers for the chineseasr pipeline.
// The only external dependency is ffmpeg, invoked through the injectable
// runner.Runner seam so tests never spawn a real subprocess.
package audio

import (
	"context"
	"fmt"
	"os"
	"regexp"

	"github.com/timmyb32r/001_omc_cri/internal/asrerr"
	"github.com/timmyb32r/001_omc_cri/internal/runner"
)

// reScheme matches any URI scheme prefix (e.g. "http://", "s3://").
var reScheme = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.\-]*://`)

// Convert resamples src to a 16 kHz mono PCM WAV written under tmpDir.
// It returns the path of the created WAV file and a cleanup func that removes
// it (always non-nil, safe to defer unconditionally).
//
// Errors:
//   - asrerr.ErrRemoteInput  – src contains a URL scheme
//   - asrerr.ErrAudioNotFound – src does not exist or is not a regular file
//   - asrerr.ErrDecodeFailed  – ffmpeg exits non-zero
func Convert(ctx context.Context, r runner.Runner, ffmpegPath, src, tmpDir string) (wavPath string, cleanup func(), err error) {
	// noop cleanup returned on early errors (so caller can always defer it).
	noop := func() {}

	// 1. Reject remote inputs before touching the filesystem.
	if reScheme.MatchString(src) {
		return "", noop, asrerr.ErrRemoteInput
	}

	// 2. Require src to be an existing regular file.
	fi, statErr := os.Stat(src)
	if statErr != nil || !fi.Mode().IsRegular() {
		if statErr != nil {
			return "", noop, fmt.Errorf("%w: %w", asrerr.ErrAudioNotFound, statErr)
		}
		return "", noop, fmt.Errorf("%w: not a regular file: %s", asrerr.ErrAudioNotFound, src)
	}

	// 3. Create a unique temp WAV path.
	tmpFile, createErr := os.CreateTemp(tmpDir, "chineseasr-*.wav")
	if createErr != nil {
		return "", noop, fmt.Errorf("audio.Convert: create temp file: %w", createErr)
	}
	// Close immediately — ffmpeg will open it by path.
	tmpFile.Close()
	wavPath = tmpFile.Name()

	cleanup = func() {
		os.Remove(wavPath) //nolint:errcheck
	}

	// 4. Invoke ffmpeg through the runner seam.
	args := []string{
		"-hide_banner",
		"-nostdin",
		"-protocol_whitelist", "file,pipe",
		"-vn",
		"-i", src,
		"-ar", "16000",
		"-ac", "1",
		"-c:a", "pcm_s16le",
		"-f", "wav",
		"-y",
		wavPath,
	}

	_, stderr, runErr := r.Run(ctx, ffmpegPath, args...)
	if runErr != nil {
		// Include a tail of stderr (up to 500 bytes) in the wrapped error.
		tail := stderrTail(stderr, 500)
		return wavPath, cleanup, fmt.Errorf("%w: %s: %w", asrerr.ErrDecodeFailed, tail, runErr)
	}

	return wavPath, cleanup, nil
}

// stderrTail returns at most maxBytes from the end of b, as a string.
func stderrTail(b []byte, maxBytes int) string {
	if len(b) > maxBytes {
		b = b[len(b)-maxBytes:]
	}
	return string(b)
}
