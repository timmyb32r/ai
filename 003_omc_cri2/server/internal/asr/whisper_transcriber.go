package asr

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/criradio/server/internal/models"
)

// whisperTranscriber implements Transcriber by calling the whisper-cli binary as a subprocess.
type whisperTranscriber struct {
	modelPath  string
	language   string
	threads    int
	maxContext int
}

// parseWhisperLine parses a whisper-cli text output line.
// Format: [HH:MM:SS.mmm --> HH:MM:SS.mmm]  text
func parseWhisperLine(line string) (startSec, endSec float64, text string, ok bool) {
	if !strings.HasPrefix(line, "[") {
		return 0, 0, "", false
	}
	closeBracket := strings.Index(line, "]")
	if closeBracket < 0 {
		return 0, 0, "", false
	}
	timestampPart := line[1:closeBracket] // "HH:MM:SS.mmm --> HH:MM:SS.mmm"
	text = strings.TrimSpace(line[closeBracket+1:])

	parts := strings.SplitN(timestampPart, "-->", 2)
	if len(parts) != 2 {
		return 0, 0, "", false
	}

	startSec = parseTimestamp(strings.TrimSpace(parts[0]))
	endSec = parseTimestamp(strings.TrimSpace(parts[1]))
	ok = true
	return
}

// parseTimestamp converts "HH:MM:SS.mmm" to seconds as float64.
func parseTimestamp(s string) float64 {
	var h, m int
	var sec float64
	n, _ := fmt.Sscanf(s, "%d:%d:%f", &h, &m, &sec)
	if n < 3 {
		return 0
	}
	return float64(h*3600) + float64(m*60) + sec
}

// NewWhisperTranscriber creates a Transcriber backed by whisper.cpp CLI.
func NewWhisperTranscriber(cfg Config) (Transcriber, error) {
	if cfg.ModelPath == "" {
		return nil, fmt.Errorf("ModelPath is required")
	}
	threads := cfg.Threads
	if threads <= 0 {
		threads = 4
	}
	maxContext := cfg.MaxContext
	if maxContext <= 0 {
		maxContext = -1
	}
	language := cfg.Language
	if language == "" {
		language = "zh"
	}

	// Verify whisper-cli is available
	if _, err := exec.LookPath("whisper-cli"); err != nil {
		return nil, fmt.Errorf("whisper-cli not found in PATH: %w", err)
	}
	// If model path is a directory, look for the model file inside it
	modelPath := cfg.ModelPath
	if info, err := os.Stat(modelPath); err == nil && info.IsDir() {
		// Registry codename specifies which file to look for
		if cfg.ModelCodename != "" {
			if mi, ok := LookupModel(cfg.ModelCodename); ok && len(mi.RequiredFiles) > 0 {
				modelPath = filepath.Join(cfg.ModelPath, mi.RequiredFiles[0])
			}
		}
	}
	if _, err := os.Stat(modelPath); err != nil {
		return nil, fmt.Errorf("model file not found at %s: %w", modelPath, err)
	}

	return &whisperTranscriber{
		modelPath:  modelPath,
		language:   language,
		threads:    threads,
		maxContext: maxContext,
	}, nil
}

// Transcribe converts PCM float32 audio to a TranscriptSegment with per-word timestamps.
//
// Process:
//  1. Write PCM samples to a temporary WAV file
//  2. Run whisper-cli -ojf on the WAV file
//  3. Parse the JSON output to extract per-token timestamps
//  4. Build a TranscriptSegment with WordEntry per token
func (t *whisperTranscriber) Transcribe(pcm []float32, segmentID int) (*models.TranscriptSegment, error) {
	if len(pcm) == 0 {
		return nil, fmt.Errorf("empty PCM data")
	}

	// 1. Write PCM to temporary WAV file
	wavPath, err := writeWAV(pcm, 16000)
	if err != nil {
		return nil, fmt.Errorf("write WAV: %w", err)
	}
	defer os.Remove(wavPath)

	// 2. Run whisper-cli
	args := []string{
		"-m", t.modelPath,
		"-l", t.language,
		"-f", wavPath,
		"-otxt", // text output with timestamps
	}
	if t.threads > 0 {
		args = append(args, "-t", fmt.Sprintf("%d", t.threads))
	}
	// Prompt whisper to output Simplified Chinese, not Traditional
	args = append(args, "--prompt", "以下是普通话的句子，使用简体中文输出。")
	// Speed optimizations: small beam for faster decoding
	args = append(args, "-bs", "2") // beam size (default 5, smaller = faster)
	if t.maxContext > 0 {
		args = append(args, "-mc", fmt.Sprintf("%d", t.maxContext))
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.Command("whisper-cli", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("whisper-cli failed: %w\nstderr: %s", err, stderr.String())
	}

	// 3. Parse text output format: [HH:MM:SS.mmm --> HH:MM:SS.mmm]  text
	output := strings.TrimSpace(stdout.String())
	if output == "" {
		return &models.TranscriptSegment{
			SegmentID: segmentID,
			TextZh:    "",
		}, nil
	}

	var allWords []models.WordEntry
	var fullText strings.Builder
	charIdx := 0

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Parse: [HH:MM:SS.mmm --> HH:MM:SS.mmm]  text
		segStart, segEnd, text, ok := parseWhisperLine(line)
		if !ok || text == "" {
			continue
		}

		fullText.WriteString(text)
		// Whisper only gives segment-level timestamps in text mode.
		// Per-word timing will be added by the pipeline (gse tokenizer + char distribution).
		allWords = append(allWords, models.WordEntry{
			Text:      text,
			CharStart: charIdx,
			CharEnd:   charIdx + len([]rune(text)),
			StartSec:  segStart,
			EndSec:    segEnd,
		})
		charIdx += len([]rune(text))
	}

	totalDuration := float64(len(pcm)) / 16000.0

	return &models.TranscriptSegment{
		SegmentID: segmentID,
		TimelineStartSec: 0,
		TimelineEndSec:   totalDuration,
		TextZh:           strings.TrimSpace(fullText.String()),
		Words:            allWords,
	}, nil
}

func (t *whisperTranscriber) Close() error {
	return nil // no persistent resources for subprocess-based approach
}

// Helper functions

// writeWAV writes PCM float32 samples as a 16-bit PCM WAV file.
func writeWAV(samples []float32, sampleRate int) (string, error) {
	f, err := os.CreateTemp("", "whisper-*.wav")
	if err != nil {
		return "", err
	}
	defer f.Close()

	numSamples := len(samples)
	byteRate := sampleRate * 2 // 16-bit = 2 bytes per sample
	dataSize := numSamples * 2
	fileSize := 36 + dataSize

	// RIFF header
	writeLE(f, []byte("RIFF"), uint32(fileSize), []byte("WAVE"))

	// fmt chunk
	writeLE(f,
		[]byte("fmt "),
		uint32(16),          // chunk size
		uint16(1),           // PCM format
		uint16(1),           // mono
		uint32(sampleRate),  // sample rate
		uint32(byteRate),    // byte rate
		uint16(2),           // block align
		uint16(16),          // bits per sample
	)

	// data chunk
	writeLE(f, []byte("data"), uint32(dataSize))

	// Convert float32 [-1.0, 1.0] to int16 and write
	for _, s := range samples {
		// Clamp
		if s > 1.0 {
			s = 1.0
		}
		if s < -1.0 {
			s = -1.0
		}
		val := int16(s * math.MaxInt16)
		binary.Write(f, binary.LittleEndian, val)
	}

	return filepath.Abs(f.Name())
}

func writeLE(f *os.File, args ...interface{}) {
	for _, arg := range args {
		switch v := arg.(type) {
		case []byte:
			f.Write(v)
		case uint32:
			binary.Write(f, binary.LittleEndian, v)
		case uint16:
			binary.Write(f, binary.LittleEndian, v)
		}
	}
}

