package audio

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/timmyb32r/001_omc_cri/internal/asrerr"
)

// fakeRunner records the last invocation and returns canned output.
type fakeRunner struct {
	called bool
	name   string
	args   []string

	stdout []byte
	stderr []byte
	err    error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	f.called = true
	f.name = name
	f.args = args
	return f.stdout, f.stderr, f.err
}

// tempSrcFile creates a real temporary file to use as a valid audio source.
func tempSrcFile(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "src-*.mp3")
	if err != nil {
		t.Fatalf("create src temp file: %v", err)
	}
	f.Close()
	return f.Name()
}

func TestConvert_RemoteInput(t *testing.T) {
	fr := &fakeRunner{}
	_, cleanup, err := Convert(context.Background(), fr, "/usr/bin/ffmpeg", "http://example.com/a.mp3", t.TempDir())
	if cleanup == nil {
		t.Fatal("cleanup must be non-nil even on error")
	}
	cleanup() // must not panic

	if !errors.Is(err, asrerr.ErrRemoteInput) {
		t.Fatalf("want ErrRemoteInput, got %v", err)
	}
	if fr.called {
		t.Fatal("runner must NOT be called for remote input")
	}
}

func TestConvert_RemoteInput_OtherSchemes(t *testing.T) {
	schemes := []string{
		"https://example.com/a.mp3",
		"s3://bucket/key.wav",
		"ftp://host/file.ogg",
	}
	fr := &fakeRunner{}
	for _, src := range schemes {
		_, cleanup, err := Convert(context.Background(), fr, "/usr/bin/ffmpeg", src, t.TempDir())
		if cleanup == nil {
			t.Fatalf("[%s] cleanup must be non-nil", src)
		}
		cleanup()
		if !errors.Is(err, asrerr.ErrRemoteInput) {
			t.Fatalf("[%s] want ErrRemoteInput, got %v", src, err)
		}
		if fr.called {
			t.Fatalf("[%s] runner must NOT be called", src)
		}
	}
}

func TestConvert_MissingFile(t *testing.T) {
	fr := &fakeRunner{}
	_, cleanup, err := Convert(context.Background(), fr, "/usr/bin/ffmpeg", "/nonexistent/path/to/audio.mp3", t.TempDir())
	if cleanup == nil {
		t.Fatal("cleanup must be non-nil even on error")
	}
	cleanup()

	if !errors.Is(err, asrerr.ErrAudioNotFound) {
		t.Fatalf("want ErrAudioNotFound, got %v", err)
	}
	if fr.called {
		t.Fatal("runner must NOT be called for missing file")
	}
}

func TestConvert_DirectoryInput(t *testing.T) {
	fr := &fakeRunner{}
	// Pass a directory (exists but not a regular file).
	dir := t.TempDir()
	_, cleanup, err := Convert(context.Background(), fr, "/usr/bin/ffmpeg", dir, t.TempDir())
	if cleanup == nil {
		t.Fatal("cleanup must be non-nil even on error")
	}
	cleanup()

	if !errors.Is(err, asrerr.ErrAudioNotFound) {
		t.Fatalf("want ErrAudioNotFound for directory input, got %v", err)
	}
	if fr.called {
		t.Fatal("runner must NOT be called for directory input")
	}
}

func TestConvert_RunnerError_ErrDecodeFailed(t *testing.T) {
	src := tempSrcFile(t)
	stderrMsg := []byte("some ffmpeg error output")
	fr := &fakeRunner{
		stderr: stderrMsg,
		err:    errors.New("exit status 1"),
	}

	_, cleanup, err := Convert(context.Background(), fr, "/usr/bin/ffmpeg", src, t.TempDir())
	if cleanup == nil {
		t.Fatal("cleanup must be non-nil on runner error")
	}
	cleanup()

	if !errors.Is(err, asrerr.ErrDecodeFailed) {
		t.Fatalf("want ErrDecodeFailed, got %v", err)
	}
	// Error message must embed stderr context.
	if err != nil && len(stderrMsg) > 0 {
		if got := err.Error(); !containsStr(got, "some ffmpeg error output") {
			t.Fatalf("error message should contain stderr tail, got: %s", got)
		}
	}
}

func TestConvert_RunnerError_StderrTail(t *testing.T) {
	src := tempSrcFile(t)
	// Build a stderr buffer longer than 500 bytes; only the tail should appear.
	long := make([]byte, 600)
	for i := range long {
		long[i] = 'x'
	}
	copy(long[595:], []byte("TAIL!"))
	fr := &fakeRunner{
		stderr: long,
		err:    errors.New("exit status 1"),
	}

	_, cleanup, err := Convert(context.Background(), fr, "/usr/bin/ffmpeg", src, t.TempDir())
	cleanup()

	if !errors.Is(err, asrerr.ErrDecodeFailed) {
		t.Fatalf("want ErrDecodeFailed, got %v", err)
	}
	if !containsStr(err.Error(), "TAIL!") {
		t.Fatalf("expected stderr tail in error, got: %s", err.Error())
	}
}

func TestConvert_Success_ArgList(t *testing.T) {
	src := tempSrcFile(t)
	tmpDir := t.TempDir()
	fr := &fakeRunner{} // err == nil -> success

	wavPath, cleanup, err := Convert(context.Background(), fr, "/usr/bin/ffmpeg", src, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wavPath == "" {
		t.Fatal("wavPath must not be empty on success")
	}
	if cleanup == nil {
		t.Fatal("cleanup must be non-nil on success")
	}

	// Verify the runner was called with the exact binary.
	if !fr.called {
		t.Fatal("runner was not called")
	}
	if fr.name != "/usr/bin/ffmpeg" {
		t.Fatalf("expected ffmpegPath as binary name, got %q", fr.name)
	}

	// Assert exact arg slice (order matters).
	want := []string{
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
	if len(fr.args) != len(want) {
		t.Fatalf("arg count mismatch: want %d, got %d\nwant: %v\n got: %v", len(want), len(fr.args), want, fr.args)
	}
	for i, w := range want {
		if fr.args[i] != w {
			t.Errorf("arg[%d]: want %q, got %q", i, w, fr.args[i])
		}
	}

	// Verify the temp wav file was actually created.
	if _, statErr := os.Stat(wavPath); statErr != nil {
		t.Fatalf("wavPath %q does not exist after Convert: %v", wavPath, statErr)
	}

	// Call cleanup and verify the file is gone.
	cleanup()
	if _, statErr := os.Stat(wavPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected wavPath %q to be removed after cleanup, stat err: %v", wavPath, statErr)
	}
}

func TestConvert_Cleanup_RemovesFileOnRunnerError(t *testing.T) {
	src := tempSrcFile(t)
	fr := &fakeRunner{err: errors.New("exit status 2")}

	wavPath, cleanup, _ := Convert(context.Background(), fr, "/usr/bin/ffmpeg", src, t.TempDir())

	// The temp file should have been created before ffmpeg was called.
	if wavPath == "" {
		t.Skip("no wavPath returned; cannot verify cleanup")
	}
	_, existErr := os.Stat(wavPath)
	if os.IsNotExist(existErr) {
		t.Skip("temp file already gone before cleanup call")
	}

	cleanup()
	if _, statErr := os.Stat(wavPath); !os.IsNotExist(statErr) {
		t.Fatalf("cleanup did not remove temp file %q", wavPath)
	}
}

func TestConvert_CleanupIdempotent(t *testing.T) {
	src := tempSrcFile(t)
	fr := &fakeRunner{}

	_, cleanup, err := Convert(context.Background(), fr, "/usr/bin/ffmpeg", src, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Calling cleanup twice must not panic.
	cleanup()
	cleanup()
}

// containsStr reports whether s contains substr.
func containsStr(s, substr string) bool {
	return len(substr) == 0 || (len(s) >= len(substr) && func() bool {
		for i := 0; i <= len(s)-len(substr); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	}())
}
