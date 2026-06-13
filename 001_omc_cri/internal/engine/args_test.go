package engine

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/timmyb32r/001_omc_cri/internal/asrerr"
)

const (
	testModelDir = "/models/sense-voice"
	testWavPath  = "/tmp/audio.wav"
)

// containsFlag reports whether s contains the exact element flag.
func containsFlag(s []string, flag string) bool {
	for _, v := range s {
		if v == flag {
			return true
		}
	}
	return false
}

func TestBuild_SenseVoice_PunctuationTrue(t *testing.T) {
	args, err := Build("sense-voice", testModelDir, "zh", true, 2, testWavPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantFlags := []string{
		"--sense-voice-model=" + filepath.Join(testModelDir, "model.int8.onnx"),
		"--tokens=" + filepath.Join(testModelDir, "tokens.txt"),
		"--sense-voice-language=zh",
		"--sense-voice-use-itn=1",
		"--debug=0",
		"--num-threads=2",
	}
	for _, f := range wantFlags {
		if !containsFlag(args, f) {
			t.Errorf("missing flag %q in args %v", f, args)
		}
	}
	if last := args[len(args)-1]; last != testWavPath {
		t.Errorf("wavPath must be last arg; got %q", last)
	}
}

func TestBuild_SenseVoice_PunctuationFalse(t *testing.T) {
	args, err := Build("sense-voice", testModelDir, "zh", false, 2, testWavPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsFlag(args, "--sense-voice-use-itn=0") {
		t.Errorf("expected --sense-voice-use-itn=0; args: %v", args)
	}
	if containsFlag(args, "--sense-voice-use-itn=1") {
		t.Errorf("unexpected --sense-voice-use-itn=1 when punctuation=false; args: %v", args)
	}
	if last := args[len(args)-1]; last != testWavPath {
		t.Errorf("wavPath must be last arg; got %q", last)
	}
}

func TestBuild_Paraformer(t *testing.T) {
	const dir = "/models/paraformer"
	const wav = "/tmp/para.wav"
	args, err := Build("paraformer", dir, "zh", false, 4, wav)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantFlags := []string{
		"--paraformer=" + filepath.Join(dir, "model.int8.onnx"),
		"--tokens=" + filepath.Join(dir, "tokens.txt"),
		"--debug=0",
		"--num-threads=4",
	}
	for _, f := range wantFlags {
		if !containsFlag(args, f) {
			t.Errorf("missing flag %q in args %v", f, args)
		}
	}
	// Paraformer must NOT emit sense-voice flags.
	for _, a := range args {
		if len(a) >= 13 && a[:13] == "--sense-voice" {
			t.Errorf("unexpected sense-voice flag in paraformer args: %q", a)
		}
	}
	if last := args[len(args)-1]; last != wav {
		t.Errorf("wavPath must be last arg; got %q", last)
	}
}

func TestBuild_Whisper(t *testing.T) {
	const dir = "/models/whisper"
	const wav = "/tmp/whisper.wav"
	args, err := Build("whisper", dir, "en", true, 1, wav)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantFlags := []string{
		"--whisper-encoder=" + filepath.Join(dir, "encoder.onnx"),
		"--whisper-decoder=" + filepath.Join(dir, "decoder.onnx"),
		"--whisper-language=en",
		"--tokens=" + filepath.Join(dir, "tokens.txt"),
		"--debug=0",
		"--num-threads=1",
	}
	for _, f := range wantFlags {
		if !containsFlag(args, f) {
			t.Errorf("missing flag %q in args %v", f, args)
		}
	}
	if last := args[len(args)-1]; last != wav {
		t.Errorf("wavPath must be last arg; got %q", last)
	}
}

func TestBuild_UnknownModel(t *testing.T) {
	for _, model := range []string{"fire-red-asr", ""} {
		args, err := Build(model, testModelDir, "zh", false, 1, testWavPath)
		if args != nil {
			t.Errorf("model %q: expected nil args, got %v", model, args)
		}
		if !errors.Is(err, asrerr.ErrModelNotImplemented) {
			t.Errorf("model %q: expected ErrModelNotImplemented, got %v", model, err)
		}
	}
}
