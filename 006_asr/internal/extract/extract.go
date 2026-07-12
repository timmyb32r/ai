package extract

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/timmyb32r/yt2srt/internal/models"
)

type ProgressFunc func(pct int, info string)

func DownloadAudio(ctx context.Context, ytDlpPath, ffmpegPath, url, pcmPath string, onProgress ProgressFunc) (float64, error) {
	if ytDlpPath == "" {
		ytDlpPath = "yt-dlp"
	}
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}

	tempDir := filepath.Dir(pcmPath)
	outputTemplate := filepath.Join(tempDir, "%(id)s.%(ext)s")

	// Step 1: Download with yt-dlp. --newline forces \n progress (not \r), fixing pipe buffering.
	args := []string{
		"-x",
		"--audio-format", "m4a",
		"-o", outputTemplate,
		"--no-playlist",
		"--socket-timeout", "15",
		"--retries", "1",
		"--newline",
	}
	if proxy := os.Getenv("YTDLP_PROXY"); proxy != "" {
		args = append(args, "--proxy", proxy)
	}
	args = append(args, url)

	dlCmd := exec.CommandContext(ctx, ytDlpPath, args...)
	// --newline sends progress to stdout; stderr (warnings) goes to container logs
	stdoutPipe, err := dlCmd.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := dlCmd.Start(); err != nil {
		return 0, fmt.Errorf("yt-dlp start: %w", err)
	}

	dlPctRe := regexp.MustCompile(`\[download\]\s+([\d.]+)%\s+of\s+(?:~\s*)?([\d.]+)\s*([KMG]iB)`)
	dlStartRe := regexp.MustCompile(`\[download\]\s+Destination:`)
	extractRe := regexp.MustCompile(`\[ExtractAudio\]`)
	youtubeRe := regexp.MustCompile(`^\[youtube\]\s+(.+)`)
	infoRe := regexp.MustCompile(`^\[info\]\s+(.+)`)

	// Parse progress from stdout (--newline sends everything here)
	go streamLines(stdoutPipe, func(line string) {
		switch {
		case dlPctRe.MatchString(line):
			m := dlPctRe.FindStringSubmatch(line)
			p, _ := strconv.ParseFloat(m[1], 64)
			size, _ := strconv.ParseFloat(m[2], 64)
			unit := m[3]
			var bytes float64
			switch unit {
			case "KiB":
				bytes = size * 1024
			case "MiB":
				bytes = size * 1024 * 1024
			case "GiB":
				bytes = size * 1024 * 1024 * 1024
			}
			downloaded := bytes * p / 100.0
			if onProgress != nil {
				onProgress(int(p), fmt.Sprintf("downloading %d%% (%.1f/%.1f MiB)", int(p), downloaded/1024/1024, bytes/1024/1024))
			}
		case dlStartRe.MatchString(line):
			if onProgress != nil {
				onProgress(0, "starting download...")
			}
		case extractRe.MatchString(line):
			if onProgress != nil {
				onProgress(-1, "converting audio...")
			}
		case youtubeRe.MatchString(line):
			m := youtubeRe.FindStringSubmatch(line)
			if onProgress != nil {
				onProgress(-1, strings.TrimSpace(m[1]))
			}
		case infoRe.MatchString(line):
			m := infoRe.FindStringSubmatch(line)
			if onProgress != nil {
				onProgress(-1, strings.TrimSpace(m[1]))
			}
		}
	})

	if err := dlCmd.Wait(); err != nil {
		return 0, fmt.Errorf("yt-dlp download failed: %w", err)
	}

	audioPath := findAudioFile(tempDir)
	if audioPath == "" {
		return 0, fmt.Errorf("no audio file found after download in %s", tempDir)
	}
	defer os.Remove(audioPath)

	// Step 2: ffmpeg PCM conversion
	if onProgress != nil {
		onProgress(-1, "converting to PCM...")
	}

	ffmpegCmd := exec.CommandContext(ctx, ffmpegPath,
		"-i", audioPath, "-ar", "16000", "-ac", "1",
		"-c:a", "pcm_s16le", "-f", "s16le", "-y", pcmPath,
	)
	ffmpegStderr, _ := ffmpegCmd.StderrPipe()
	if err := ffmpegCmd.Start(); err != nil {
		return 0, fmt.Errorf("ffmpeg start: %w", err)
	}

	timeRe := regexp.MustCompile(`time=(\d+):(\d+):(\d+)\.(\d+)`)
	go streamLines(ffmpegStderr, func(line string) {
		if m := timeRe.FindStringSubmatch(line); m != nil && onProgress != nil {
			h, _ := strconv.Atoi(m[1])
			mi, _ := strconv.Atoi(m[2])
			s, _ := strconv.Atoi(m[3])
			_ = h
			onProgress(-1, fmt.Sprintf("converting... %02d:%02d:%02d", h, mi, s))
		}
	})

	if err := ffmpegCmd.Wait(); err != nil {
		return 0, fmt.Errorf("ffmpeg conversion failed: %w", err)
	}
	return GetPCMDuration(pcmPath)
}

func streamLines(r io.Reader, cb func(string)) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1*1024*1024)
	scanner.Split(scanLines)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fmt.Fprintln(os.Stderr, line)
		cb(line)
	}
}

func scanLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i := 0; i < len(data); i++ {
		if data[i] == '\r' || data[i] == '\n' {
			next := i + 1
			if data[i] == '\r' && next < len(data) && data[next] == '\n' {
				next++
			}
			return next, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func findAudioFile(dir string) string {
	entries, _ := os.ReadDir(dir)
	exts := map[string]bool{".m4a": true, ".webm": true, ".opus": true, ".mp3": true, ".aac": true, ".wav": true, ".mp4": true}
	for _, e := range entries {
		if !e.IsDir() && exts[strings.ToLower(filepath.Ext(e.Name()))] {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

func GetPCMDuration(pcmPath string) (float64, error) {
	info, err := os.Stat(pcmPath)
	if err != nil {
		return 0, fmt.Errorf("stat PCM file: %w", err)
	}
	if info.Size() < 2 {
		return 0, fmt.Errorf("PCM file too small: %d bytes", info.Size())
	}
	return float64(info.Size()/2) / 16000.0, nil
}

func LoadChunks(pcmPath string, durationSec float64, chunkSec int) ([]models.ChunkInfo, error) {
	data, err := os.ReadFile(pcmPath)
	if err != nil {
		return nil, fmt.Errorf("read PCM file: %w", err)
	}
	if len(data) < 2 {
		return nil, fmt.Errorf("PCM file too small: %d bytes", len(data))
	}

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
