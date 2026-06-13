package engine

import (
	"context"
	"fmt"

	"github.com/timmyb32r/001_omc_cri/internal/asrerr"
	"github.com/timmyb32r/001_omc_cri/internal/runner"
)

// Recognize invokes the sherpa-onnx-offline binary at sherpaPath with the
// provided args, captures stdout and stderr, and delegates transcript
// extraction to ParseText.
//
// A non-zero exit from the subprocess yields asrerr.ErrToolFailed (wrapping a
// tail of stderr for context). The run error is also wrapped via the dual-%w
// form, so both errors.Is(err, asrerr.ErrToolFailed) and errors.Is(err, runErr)
// succeed. On success the result of ParseText is returned directly.
func Recognize(ctx context.Context, r runner.Runner, sherpaPath string, args []string) (text string, err error) {
	stdout, stderr, runErr := r.Run(ctx, sherpaPath, args...)
	if runErr != nil {
		return "", fmt.Errorf("%w: %s: %w", asrerr.ErrToolFailed, tailBytes(stderr, 500), runErr)
	}
	return ParseText(stdout, stderr)
}
