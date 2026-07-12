package extract

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/timmyb32r/yt2srt/internal/models"
)

// Download runs yt-dlp to extract audio from a YouTube URL.
// Returns the path to the downloaded audio file.
func Download(ctx context.Context, ytDlpPath, url, outputDir string) (string, error) {
	if ytDlpPath == "" {
		ytDlpPath = "yt-dlp"
	}
	outputTemplate := filepath.Join(outputDir, "%(id)s.%(ext)s")

	cmd := exec.CommandContext(ctx, ytDlpPath,
		"-x",                            // extract audio only
		"--audio-format", "m4a",         // AAC in M4A container
		"-o", outputTemplate,
		"--no-playlist",                 // don't download playlists
		"--no-warnings",
		url,
	)
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("yt-dlp download failed: %w", err)
	}
	_ = out

	// Find the downloaded file — yt-dlp uses the video ID in the filename
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return "", fmt.Errorf("read output dir: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".m4a" {
			return filepath.Join(outputDir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("no audio file found in output directory after download")
}

// ConvertToPCM runs ffmpeg to convert audio to PCM s16le 16kHz mono.
// Returns the audio duration in seconds.
func ConvertToPCM(ctx context.Context, ffmpegPath, audioPath, pcmPath string) (float64, error) {
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}

	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-i", audioPath,
		"-ar", "16000",
		"-ac", "1",
		"-c:a", "pcm_s16le",
		"-f", "s16le",
		"-y",
		pcmPath,
	)
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("ffmpeg conversion failed: %w", err)
	}

	return GetDuration(ctx, "ffprobe", audioPath)
}

// GetDuration returns the audio duration in seconds using ffprobe.
func GetDuration(ctx context.Context, ffprobePath, audioPath string) (float64, error) {
	if ffprobePath == "" {
		ffprobePath = "ffprobe"
	}

	cmd := exec.CommandContext(ctx, ffprobePath,
		"-v", "quiet",
		"-show_entries", "format=duration",
		"-of", "csv=p=0",
		audioPath,
	)

	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe failed: %w", err)
	}

	var duration float64
	if _, err := fmt.Sscanf(string(out), "%f", &duration); err != nil {
		return 0, fmt.Errorf("parse duration: %w", err)
	}
	return duration, nil
}

// LoadChunks reads a PCM s16le file and splits it into equally-sized float32 chunks.
func LoadChunks(pcmPath string, durationSec float64, chunkSec int) ([]models.ChunkInfo, error) {
	data, err := os.ReadFile(pcmPath)
	if err != nil {
		return nil, fmt.Errorf("read PCM file: %w", err)
	}

	// 16kHz mono, 2 bytes per sample (s16le)
	samplesPerChunk := chunkSec * 16000
	totalSamples := len(data) / 2
	numChunks := (totalSamples + samplesPerChunk - 1) / samplesPerChunk

	chunks := make([]models.ChunkInfo, 0, numChunks)
	for i := 0; i < numChunks; i++ {
		startSample := i * samplesPerChunk
		endSample := startSample + samplesPerChunk
		if endSample > totalSamples {
			endSample = totalSamples
		}

		n := endSample - startSample
		samples := make([]float32, n)
		for j := 0; j < n; j++ {
			offset := (startSample + j) * 2
			val := int16(binary.LittleEndian.Uint16(data[offset : offset+2]))
			samples[j] = float32(val) / float32(math.MaxInt16)
		}

		chunks = append(chunks, models.ChunkInfo{
			Index:    i,
			Samples:  samples,
			StartSec: float64(startSample) / 16000.0,
			EndSec:   float64(endSample) / 16000.0,
		})
	}

	return chunks, nil
}
