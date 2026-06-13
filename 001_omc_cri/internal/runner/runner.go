// Package runner provides the subprocess execution seam used by the
// chineseasr engine and audio layers. The Runner interface is the single
// boundary between the library and the external ffmpeg / sherpa-onnx-offline
// binaries, which lets unit tests inject a fake that returns canned output
// without spawning any process.
package runner

import (
	"bytes"
	"context"
	"os/exec"
)

// Runner executes an external command and returns its stdout and stderr
// captured separately (never combined), along with any execution error.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

// Exec is the production Runner. It shells out via exec.CommandContext so that
// cancelling ctx kills the subprocess.
type Exec struct{}

// Run executes name with args, capturing stdout and stderr into separate
// buffers. The returned err is whatever exec.Cmd.Run reports (e.g. an
// *exec.ExitError for a non-zero exit, or ctx.Err()-derived errors on cancel).
func (Exec) Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error) {
	var outBuf, errBuf bytes.Buffer

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err = cmd.Run()

	return outBuf.Bytes(), errBuf.Bytes(), err
}
