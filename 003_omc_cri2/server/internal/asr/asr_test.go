package asr

import (
	"math"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/criradio/server/internal/models"
)

// ── Text format parsing tests ──────────────────────────────────────────

func TestParseWhisperLine(t *testing.T) {
	tests := []struct {
		line           string
		wantStart      float64
		wantEnd        float64
		wantText       string
		wantOK         bool
	}{
		{"[00:00:00.000 --> 00:00:02.000]  从福建泉州楼到海南", 0.0, 2.0, "从福建泉州楼到海南", true},
		{"[00:00:02.000 --> 00:00:03.000]  担任熊州福建", 2.0, 3.0, "担任熊州福建", true},
		{"[00:01:30.500 --> 00:01:33.250]  hello world", 90.5, 93.25, "hello world", true},
		{"not a timestamp line", 0, 0, "", false},
		{"", 0, 0, "", false},
		{"[incomplete", 0, 0, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			start, end, text, ok := parseWhisperLine(tt.line)
			if ok != tt.wantOK {
				t.Errorf("ok: got %v, want %v", ok, tt.wantOK)
				return
			}
			if !ok {
				return
			}
			if start != tt.wantStart {
				t.Errorf("start: got %f, want %f", start, tt.wantStart)
			}
			if end != tt.wantEnd {
				t.Errorf("end: got %f, want %f", end, tt.wantEnd)
			}
			if text != tt.wantText {
				t.Errorf("text: got %q, want %q", text, tt.wantText)
			}
		})
	}
}

func TestParseTimestamp(t *testing.T) {
	tests := []struct {
		input string
		want  float64
	}{
		{"00:00:00.000", 0.0},
		{"00:00:01.000", 1.0},
		{"00:00:01.500", 1.5},
		{"00:01:00.000", 60.0},
		{"01:00:00.000", 3600.0},
		{"01:02:03.456", 3723.456},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseTimestamp(tt.input)
			if got != tt.want {
				t.Errorf("parseTimestamp(%q) = %f, want %f", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseWhisperOutput(t *testing.T) {
	// Simulate whisper-cli text output
	output := `
[00:00:00.000 --> 00:00:00.720]  欢迎
[00:00:00.720 --> 00:00:01.440]  收听
[00:00:01.440 --> 00:00:02.100]  国际
[00:00:02.100 --> 00:00:02.550]  广播
[00:00:02.550 --> 00:00:03.000]  电台
`

	lines := strings.Split(output, "\n")
	var words []models.WordEntry
	charIdx := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		start, end, text, ok := parseWhisperLine(line)
		if !ok || text == "" {
			continue
		}
		words = append(words, models.WordEntry{
			Text:      text,
			CharStart: charIdx,
			CharEnd:   charIdx + len([]rune(text)),
			StartSec:  start,
			EndSec:    end,
		})
		charIdx += len([]rune(text))
	}

	if len(words) != 5 {
		t.Fatalf("expected 5 words, got %d", len(words))
	}

	expected := []struct {
		text  string
		start float64
		end   float64
	}{
		{"欢迎", 0.0, 0.72},
		{"收听", 0.72, 1.44},
		{"国际", 1.44, 2.10},
		{"广播", 2.10, 2.55},
		{"电台", 2.55, 3.0},
	}

	for i, exp := range expected {
		if words[i].Text != exp.text {
			t.Errorf("word[%d].Text: got %q, want %q", i, words[i].Text, exp.text)
		}
		if words[i].StartSec != exp.start {
			t.Errorf("word[%d].StartSec: got %f, want %f", i, words[i].StartSec, exp.start)
		}
		if words[i].EndSec != exp.end {
			t.Errorf("word[%d].EndSec: got %f, want %f", i, words[i].EndSec, exp.end)
		}
	}

	t.Log("✅ Per-segment timestamps parsed from text format")
}

// ── WAV generation tests ─────────────────────────────────────────────────

func TestWriteWAV(t *testing.T) {
	sampleRate := 16000
	duration := 1.0
	samples := make([]float32, int(float64(sampleRate)*duration))
	for i := range samples {
		samples[i] = float32(0.5 * math.Sin(2*math.Pi*440*float64(i)/float64(sampleRate)))
	}

	path, err := writeWAV(samples, sampleRate)
	if err != nil {
		t.Fatalf("writeWAV failed: %v", err)
	}
	defer os.Remove(path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	expectedSize := int64(44 + len(samples)*2)
	if info.Size() != expectedSize {
		t.Errorf("WAV size: got %d, want %d", info.Size(), expectedSize)
	}
}

func TestWriteWAVSilence(t *testing.T) {
	samples := make([]float32, 16000)
	path, err := writeWAV(samples, 16000)
	if err != nil {
		t.Fatalf("writeWAV failed: %v", err)
	}
	defer os.Remove(path)

	info, _ := os.Stat(path)
	if info.Size() == 0 {
		t.Error("silent WAV should be non-empty")
	}
}

// ── Mock transcriber tests ──────────────────────────────────────────────

func TestMockTranscriberTimestamps(t *testing.T) {
	mock := NewMockTranscriber()
	defer mock.Close()

	pcm := make([]float32, 48000)

	seg, err := mock.Transcribe(pcm, 0)
	if err != nil {
		t.Fatalf("Transcribe failed: %v", err)
	}

	if len(seg.Words) == 0 {
		t.Fatal("expected at least 1 word")
	}

	for i, w := range seg.Words {
		if w.StartSec >= w.EndSec {
			t.Errorf("word[%d](%s): StartSec(%f) >= EndSec(%f)", i, w.Text, w.StartSec, w.EndSec)
		}
		if w.Text == "" {
			t.Errorf("word[%d]: empty text", i)
		}
	}

	t.Logf("✅ Mock produces %d words with valid timestamps", len(seg.Words))
}

func TestMockTranscriberConsistency(t *testing.T) {
	mock := NewMockTranscriber()
	defer mock.Close()

	for id := 0; id < 5; id++ {
		pcm := make([]float32, 48000)
		seg, err := mock.Transcribe(pcm, id)
		if err != nil {
			t.Fatalf("Transcribe(%d) failed: %v", id, err)
		}
		if seg.SegmentID != id {
			t.Errorf("SegmentID: got %d, want %d", seg.SegmentID, id)
		}
	}

	segs := mock.GetSegments()
	if len(segs) != 5 {
		t.Errorf("GetSegments: got %d, want 5", len(segs))
	}
}

// ── Integration test (skipped if whisper-cli not installed) ─────────────

func TestWhisperCLIIntegration(t *testing.T) {
	if _, err := exec.LookPath("whisper-cli"); err != nil {
		t.Skip("whisper-cli not installed — skipping integration test")
	}

	modelPath := os.Getenv("WHISPER_MODEL")
	if modelPath == "" {
		modelPath = "/opt/models/ggml-base.bin"
	}
	if _, err := os.Stat(modelPath); err != nil {
		t.Skipf("model not found at %s — set WHISPER_MODEL env var", modelPath)
	}

	transcriber, err := NewWhisperTranscriber(Config{
		ModelPath: modelPath,
		Language:  "zh",
		Threads:   2,
	})
	if err != nil {
		t.Fatalf("NewWhisperTranscriber failed: %v", err)
	}
	defer transcriber.Close()

	pcm := make([]float32, 48000)
	for i := range pcm {
		pcm[i] = float32(0.3 * math.Sin(2*math.Pi*440*float64(i)/16000))
	}

	seg, err := transcriber.Transcribe(pcm, 42)
	if err != nil {
		t.Fatalf("Transcribe failed: %v", err)
	}

	if seg.SegmentID != 42 {
		t.Errorf("SegmentID: got %d, want 42", seg.SegmentID)
	}

	t.Logf("Whisper transcribed %d PCM samples → %d words, text=%q",
		len(pcm), len(seg.Words), seg.TextZh)

	for i, w := range seg.Words {
		if w.StartSec < 0 || w.EndSec < 0 {
			t.Errorf("word[%d](%s): negative timestamp", i, w.Text)
		}
	}
}

func TestNewWhisperTranscriberErrors(t *testing.T) {
	_, err := NewWhisperTranscriber(Config{ModelPath: ""})
	if err == nil {
		t.Error("expected error for empty ModelPath")
	}
}

// Ensure mock satisfies the interface at compile time
var _ Transcriber = (*MockTranscriber)(nil)
