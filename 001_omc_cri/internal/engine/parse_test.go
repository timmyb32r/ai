package engine

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/timmyb32r/001_omc_cri/internal/asrerr"
)

// goldenStdout reads the shared SenseVoice golden fixture.
func goldenStdout(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../testdata/golden_sensevoice_stdout.txt")
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	return data
}

// TestParse_Golden verifies the happy path against the realistic fixture.
func TestParse_Golden(t *testing.T) {
	stdout := goldenStdout(t)

	r, err := Parse(stdout, nil)
	if err != nil {
		t.Fatalf("Parse returned unexpected error: %v", err)
	}

	want := "今天天气很好，我们去公园吧。"
	if r.Text != want {
		t.Errorf("text = %q; want %q", r.Text, want)
	}

	// Must contain at least one CJK punctuation mark.
	hasPunct := strings.ContainsAny(r.Text, "，。？！")
	if !hasPunct {
		t.Errorf("text %q contains none of ，。？！", r.Text)
	}

	// Must be valid UTF-8 (no re-encoding mangling).
	if !utf8.ValidString(r.Text) {
		t.Error("text is not valid UTF-8")
	}
}

// TestParse_JSONAmongNoise verifies that noise lines around the JSON block
// are ignored and the correct transcript is still extracted.
func TestParse_JSONAmongNoise(t *testing.T) {
	golden := strings.TrimSpace(string(goldenStdout(t)))

	noise := strings.Join([]string{
		"[config] model_dir=/opt/models/sense-voice",
		"[config] num_threads=2",
		"[status] loading model...",
		"[status] processing audio...",
		golden,
		"----",
		"[status] done",
	}, "\n")

	r, err := Parse([]byte(noise), nil)
	if err != nil {
		t.Fatalf("Parse with surrounding noise returned error: %v", err)
	}

	want := "今天天气很好，我们去公园吧。"
	if r.Text != want {
		t.Errorf("text = %q; want %q", r.Text, want)
	}
}

// TestParse_ZeroBlocks verifies that stdout with no JSON block returns
// ErrParseFailed.
func TestParse_ZeroBlocks(t *testing.T) {
	stdout := []byte("loading...\ndone\n")
	_, err := Parse(stdout, []byte("some stderr output"))
	if !errors.Is(err, asrerr.ErrParseFailed) {
		t.Errorf("expected ErrParseFailed, got: %v", err)
	}
}

// TestParse_EmptyText verifies that a JSON result block with an empty text
// field returns ErrEmptyTranscript.
func TestParse_EmptyText(t *testing.T) {
	stdout := []byte(`{"lang":"<|zh|>","text":"","tokens":[],"words":[]}`)
	_, err := Parse(stdout, nil)
	if !errors.Is(err, asrerr.ErrEmptyTranscript) {
		t.Errorf("expected ErrEmptyTranscript, got: %v", err)
	}
}

// TestParse_MultiLineFallback verifies that a pretty-printed (multi-line)
// JSON object — which the fast per-line scan cannot match — is recovered by the
// streaming json.Decoder fallback, returning the exact transcript.
func TestParse_MultiLineFallback(t *testing.T) {
	stdout := []byte(`{
  "lang": "<|zh|>",
  "text": "多行解析测试。",
  "tokens": [],
  "words": []
}`)

	r, err := Parse(stdout, nil)
	if err != nil {
		t.Fatalf("Parse on multi-line JSON returned error: %v", err)
	}
	want := "多行解析测试。"
	if r.Text != want {
		t.Errorf("text = %q; want %q", r.Text, want)
	}
}

// fakeRunner is a minimal Runner implementation for unit tests.
type fakeRunner struct {
	stdout []byte
	stderr []byte
	err    error
}

func (f fakeRunner) Run(_ context.Context, _ string, _ ...string) ([]byte, []byte, error) {
	return f.stdout, f.stderr, f.err
}

// TestRecognize_ToolFailed verifies that a non-zero exit from the subprocess
// wraps ErrToolFailed.
func TestRecognize_ToolFailed(t *testing.T) {
	fake := fakeRunner{
		stdout: nil,
		stderr: []byte("sherpa: model not found"),
		err:    errors.New("exit status 1"),
	}

	_, err := Recognize(context.Background(), fake, "/usr/local/bin/sherpa-onnx-offline", nil)
	if !errors.Is(err, asrerr.ErrToolFailed) {
		t.Errorf("expected ErrToolFailed, got: %v", err)
	}
}

// TestRecognize_ToolFailed_StderrContext verifies that the returned error
// message carries the injected stderr text for diagnostics, and that the
// dual-%w wrapping keeps both the ErrToolFailed sentinel and the underlying run
// error matchable via errors.Is.
func TestRecognize_ToolFailed_StderrContext(t *testing.T) {
	runErr := errors.New("exit status 2")
	fake := fakeRunner{
		stdout: nil,
		stderr: []byte("sherpa: bad model"),
		err:    runErr,
	}

	_, err := Recognize(context.Background(), fake, "/usr/local/bin/sherpa-onnx-offline", nil)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "sherpa: bad model") {
		t.Errorf("error message %q does not contain injected stderr text", err.Error())
	}
	if !errors.Is(err, asrerr.ErrToolFailed) {
		t.Errorf("errors.Is(err, ErrToolFailed) = false; err: %v", err)
	}
	if !errors.Is(err, runErr) {
		t.Errorf("errors.Is(err, runErr) = false (dual-%%w broken); err: %v", err)
	}
}

// TestRecognize_Success verifies that a zero-exit run with valid stdout returns
// the parsed transcript.
func TestRecognize_Success(t *testing.T) {
	golden := goldenStdout(t)
	fake := fakeRunner{stdout: golden, stderr: nil, err: nil}

	text, err := Recognize(context.Background(), fake, "/usr/local/bin/sherpa-onnx-offline", nil)
	if err != nil {
		t.Fatalf("Recognize returned unexpected error: %v", err)
	}

	want := "今天天气很好，我们去公园吧。"
	if text.Text != want {
		t.Errorf("text = %q; want %q", text.Text, want)
	}
}
