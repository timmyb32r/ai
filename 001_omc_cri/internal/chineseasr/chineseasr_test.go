package chineseasr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldenText is the transcript carried by the SenseVoice golden stdout line.
const goldenText = "今天天气很好，我们去公园吧。"

// readGolden loads the single golden SenseVoice stdout line from testdata.
func readGolden(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden_sensevoice_stdout.txt"))
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	return b
}

// fakeRunner is an in-memory Runner that distinguishes the ffmpeg call from the
// sherpa call, so a single fake can drive the whole two-stage pipeline without
// spawning a process. It branches on the binary name: a path containing
// "ffmpeg" (or matching the configured ffmpegPath) is the decode stage; any
// other name is the engine stage.
type fakeRunner struct {
	ffmpegPath string // identifies the decode-stage binary
	sherpaOut  []byte // stdout returned by the engine-stage call
	ffmpegErr  error  // when non-nil, the decode-stage call fails with this

	calls int // total Run invocations
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error) {
	f.calls++

	isFFmpeg := strings.Contains(name, "ffmpeg") || name == f.ffmpegPath
	if isFFmpeg {
		if f.ffmpegErr != nil {
			return nil, []byte("ffmpeg: simulated decode failure"), f.ffmpegErr
		}
		// ffmpeg writes the WAV to disk by path; stdout/stderr are empty.
		return nil, nil, nil
	}
	// Engine stage: emit the golden JSON line on stdout.
	return f.sherpaOut, nil, nil
}

// newModelDir creates a model directory under dir populated with the files New
// requires for the SenseVoice model (tokens.txt + model.int8.onnx).
func newModelDir(t *testing.T, dir string) string {
	t.Helper()
	modelDir := filepath.Join(dir, "model")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatalf("mkdir model dir: %v", err)
	}
	for _, name := range []string{"tokens.txt", "model.int8.onnx"} {
		writeFile(t, filepath.Join(modelDir, name), "stub")
	}
	return modelDir
}

// writeFile creates a file with the given contents, failing the test on error.
func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// validConfig builds a Config that passes New, with both binaries and the model
// dir materialized under a fresh temp dir. It returns the config and the
// ffmpeg binary path (handy for wiring the fake runner).
func validConfig(t *testing.T) (Config, string) {
	t.Helper()
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	sherpa := filepath.Join(dir, "sherpa-onnx-offline")
	writeFile(t, ffmpeg, "#!/bin/sh\n")
	writeFile(t, sherpa, "#!/bin/sh\n")
	modelDir := newModelDir(t, dir)

	return Config{
		FFmpegPath:        ffmpeg,
		SherpaOfflinePath: sherpa,
		ModelDir:          modelDir,
		TempDir:           dir,
	}, ffmpeg
}

func TestNew_Defaults(t *testing.T) {
	cfg, _ := validConfig(t)
	tr, err := New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	if tr.cfg.Model != ModelSenseVoice {
		t.Errorf("Model default = %q, want %q", tr.cfg.Model, ModelSenseVoice)
	}
	if tr.cfg.Language != "zh" {
		t.Errorf("Language default = %q, want %q", tr.cfg.Language, "zh")
	}
	if tr.cfg.NumThreads != 2 {
		t.Errorf("NumThreads default = %d, want 2", tr.cfg.NumThreads)
	}
	if tr.cfg.Punctuation != nil {
		t.Errorf("Punctuation default = %v, want nil (default ON)", tr.cfg.Punctuation)
	}
	if !tr.punctuationEnabled() {
		t.Error("punctuationEnabled() = false for nil Punctuation, want true (default ON)")
	}
}

func TestNew_PunctuationExplicitFalse(t *testing.T) {
	cfg, _ := validConfig(t)
	off := false
	cfg.Punctuation = &off
	tr, err := New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	if tr.punctuationEnabled() {
		t.Error("punctuationEnabled() = true for explicit false, want false")
	}
}

func TestNew_MissingFFmpeg(t *testing.T) {
	cfg, _ := validConfig(t)
	cfg.FFmpegPath = filepath.Join(t.TempDir(), "does-not-exist")
	_, err := New(cfg)
	if !errors.Is(err, ErrFFmpegNotFound) {
		t.Fatalf("New error = %v, want ErrFFmpegNotFound", err)
	}
}

func TestNew_MissingSherpa(t *testing.T) {
	cfg, _ := validConfig(t)
	cfg.SherpaOfflinePath = filepath.Join(t.TempDir(), "does-not-exist")
	_, err := New(cfg)
	if !errors.Is(err, ErrSherpaNotFound) {
		t.Fatalf("New error = %v, want ErrSherpaNotFound", err)
	}
}

func TestNew_FFmpegIsDirectory(t *testing.T) {
	cfg, _ := validConfig(t)
	cfg.FFmpegPath = t.TempDir() // a directory, not a file
	_, err := New(cfg)
	if !errors.Is(err, ErrFFmpegNotFound) {
		t.Fatalf("New error = %v, want ErrFFmpegNotFound for a directory path", err)
	}
}

func TestNew_MissingModelFiles(t *testing.T) {
	cfg, _ := validConfig(t)
	// Point at a directory that exists but lacks the required model files.
	empty := filepath.Join(t.TempDir(), "emptymodel")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg.ModelDir = empty
	_, err := New(cfg)
	if !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("New error = %v, want ErrModelNotFound", err)
	}
}

func TestNew_ModelDirNotDirectory(t *testing.T) {
	cfg, _ := validConfig(t)
	// A regular file standing in for ModelDir must be rejected.
	cfg.ModelDir = cfg.FFmpegPath
	_, err := New(cfg)
	if !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("New error = %v, want ErrModelNotFound for non-directory ModelDir", err)
	}
}

func TestNew_UnwiredModel(t *testing.T) {
	cfg, _ := validConfig(t)
	cfg.Model = Model("fire-red-asr")
	_, err := New(cfg)
	if !errors.Is(err, ErrModelNotImplemented) {
		t.Fatalf("New error = %v, want ErrModelNotImplemented", err)
	}
}

func TestNew_WhisperRequiresEncoderDecoder(t *testing.T) {
	cfg, _ := validConfig(t)
	cfg.Model = ModelWhisper
	// The default model dir only has model.int8.onnx, not encoder/decoder.
	_, err := New(cfg)
	if !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("New(whisper) error = %v, want ErrModelNotFound", err)
	}

	// Now add encoder/decoder and it should succeed.
	writeFile(t, filepath.Join(cfg.ModelDir, "encoder.onnx"), "stub")
	writeFile(t, filepath.Join(cfg.ModelDir, "decoder.onnx"), "stub")
	if _, err := New(cfg); err != nil {
		t.Fatalf("New(whisper) with encoder+decoder: unexpected error: %v", err)
	}
}

func TestTranscribe_HappyPath(t *testing.T) {
	cfg, ffmpeg := validConfig(t)
	tr, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fake := &fakeRunner{ffmpegPath: ffmpeg, sherpaOut: readGolden(t)}
	tr.setRunner(fake)

	// A real local audio file is required so audio.Convert passes its stat check.
	audio := filepath.Join(t.TempDir(), "input.mp3")
	writeFile(t, audio, "fake audio bytes")

	res, err := tr.Transcribe(context.Background(), audio)
	if err != nil {
		t.Fatalf("Transcribe: unexpected error: %v", err)
	}
	if res == nil {
		t.Fatal("Transcribe returned nil Result")
	}
	if res.Text != goldenText {
		t.Errorf("Text = %q, want %q", res.Text, goldenText)
	}
	if fake.calls != 2 {
		t.Errorf("runner called %d times, want 2 (ffmpeg + sherpa)", fake.calls)
	}
}

func TestTranscribe_CleansUpTempWAV(t *testing.T) {
	cfg, ffmpeg := validConfig(t)
	tr, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tr.setRunner(&fakeRunner{ffmpegPath: ffmpeg, sherpaOut: readGolden(t)})

	audio := filepath.Join(t.TempDir(), "input.mp3")
	writeFile(t, audio, "fake audio bytes")

	if _, err := tr.Transcribe(context.Background(), audio); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	assertNoTempWAV(t, cfg.TempDir)
}

func TestTranscribe_DecodeFailureCleansUp(t *testing.T) {
	cfg, ffmpeg := validConfig(t)
	tr, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// ffmpeg fails -> Convert returns ErrDecodeFailed but still hands back a
	// non-nil cleanup, so the temp WAV must not linger.
	tr.setRunner(&fakeRunner{
		ffmpegPath: ffmpeg,
		ffmpegErr:  errors.New("exit status 1"),
	})

	audio := filepath.Join(t.TempDir(), "input.mp3")
	writeFile(t, audio, "fake audio bytes")

	_, err = tr.Transcribe(context.Background(), audio)
	if !errors.Is(err, ErrDecodeFailed) {
		t.Fatalf("Transcribe error = %v, want ErrDecodeFailed", err)
	}
	assertNoTempWAV(t, cfg.TempDir)
}

func TestTranscribe_ContextCancelled(t *testing.T) {
	cfg, ffmpeg := validConfig(t)
	tr, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fake := &fakeRunner{ffmpegPath: ffmpeg, sherpaOut: readGolden(t)}
	tr.setRunner(fake)

	audio := filepath.Join(t.TempDir(), "input.mp3")
	writeFile(t, audio, "fake audio bytes")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the call

	_, err = tr.Transcribe(ctx, audio)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Transcribe error = %v, want context.Canceled", err)
	}
	// The initial ctx.Err() guard must short-circuit before any subprocess runs.
	if fake.calls != 0 {
		t.Errorf("runner called %d times on cancelled ctx, want 0 (no ffmpeg/sherpa)", fake.calls)
	}
}

func TestTranscribe_RemoteInputRejected(t *testing.T) {
	cfg, ffmpeg := validConfig(t)
	tr, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fake := &fakeRunner{ffmpegPath: ffmpeg, sherpaOut: readGolden(t)}
	tr.setRunner(fake)

	_, err = tr.Transcribe(context.Background(), "http://x/a.mp3")
	if !errors.Is(err, ErrRemoteInput) {
		t.Fatalf("Transcribe error = %v, want ErrRemoteInput", err)
	}
	if fake.calls != 0 {
		t.Errorf("runner called %d times for remote input, want 0", fake.calls)
	}
}

func TestTranscribe_EmptyTranscript(t *testing.T) {
	cfg, ffmpeg := validConfig(t)
	tr, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Engine output with an empty text field surfaces as ErrEmptyTranscript
	// (ParseText returns it directly).
	tr.setRunner(&fakeRunner{
		ffmpegPath: ffmpeg,
		sherpaOut:  []byte(`{"text":""}` + "\n"),
	})

	audio := filepath.Join(t.TempDir(), "input.mp3")
	writeFile(t, audio, "fake audio bytes")

	_, err = tr.Transcribe(context.Background(), audio)
	if !errors.Is(err, ErrEmptyTranscript) {
		t.Fatalf("Transcribe error = %v, want ErrEmptyTranscript", err)
	}
	assertNoTempWAV(t, cfg.TempDir)
}

// assertNoTempWAV fails the test if any chineseasr-*.wav intermediate remains in
// dir.
func assertNoTempWAV(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "chineseasr-*.wav"))
	if err != nil {
		t.Fatalf("glob temp wavs: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("leftover temp WAV files: %v", matches)
	}
}
