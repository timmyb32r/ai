package worker

import (
	"fmt"
	"strings"

	"github.com/timmyb32r/yt2srt/internal/models"
)

// ToSRT converts transcribed chunks to SRT format string.
func ToSRT(results []models.TranscriptResult, totalDurationSec float64) string {
	var entries []srtEntry
	index := 1

	for i, chunk := range results {
		if len(chunk.Timestamps) == 0 || chunk.Text == "" {
			// Fallback: one entry spanning the chunk duration
			chunkStart := chunkStartSec(chunk)
			chunkEnd := chunkEndSec(chunk)
			dur := chunkEnd - chunkStart
			if dur <= 0 {
				// Infer duration from next chunk or total
				if i+1 < len(results) {
					dur = results[i+1].ChunkOffset - chunk.ChunkOffset
				} else {
					dur = totalDurationSec - chunk.ChunkOffset
				}
			}
			if dur <= 0 {
				dur = 1.0 // minimum
			}
			text := chunk.Text
			if text == "" {
				text = "..."
			}
			entries = append(entries, srtEntry{
				Index:   index,
				StartMs: int(chunk.ChunkOffset * 1000),
				EndMs:   int((chunk.ChunkOffset + dur) * 1000),
				Text:    text,
			})
			index++
			continue
		}

		// Group tokens into subtitle entries
		text := []rune(chunk.Text)
		timestamps := chunk.Timestamps

		entryStart := 0
		for j := 0; j < len(timestamps); j++ {
			span := timestamps[j] - timestamps[entryStart]
			if span > 5.0 || (j > 0 && isSentenceEnd(text[j-1])) {
				end := j
				if end <= entryStart {
					end = entryStart + 1
				}
				if end > len(timestamps) {
					end = len(timestamps)
				}

				charStart := entryStart
				charEnd := end
				if charEnd > len(text) {
					charEnd = len(text)
				}
				entryText := strings.TrimSpace(string(text[charStart:charEnd]))
				if entryText == "" {
					entryStart = end
					continue
				}

				entries = append(entries, srtEntry{
					Index:   index,
					StartMs: int((chunk.ChunkOffset + timestamps[entryStart]) * 1000),
					EndMs:   int((chunk.ChunkOffset + timestamps[end-1]) * 1000),
					Text:    entryText,
				})
				index++
				entryStart = end
			}
		}

		// Remaining tokens
		if entryStart < len(timestamps) {
			charStart := entryStart
			charEnd := len(text)
			if charEnd > len(text) {
				charEnd = len(text)
			}
			entryText := strings.TrimSpace(string(text[charStart:charEnd]))
			if entryText != "" {
				endTs := timestamps[len(timestamps)-1]
				entries = append(entries, srtEntry{
					Index:   index,
					StartMs: int((chunk.ChunkOffset + timestamps[entryStart]) * 1000),
					EndMs:   int((chunk.ChunkOffset + endTs + 1.0) * 1000), // +1s grace
					Text:    entryText,
				})
				index++
			}
		}
	}

	// Handle chunk-boundary gaps: extend previous entry's end time if gap ≤ 0.5s
	for i := 1; i < len(entries); i++ {
		gap := entries[i].StartMs - entries[i-1].EndMs
		if gap > 0 && gap <= 500 {
			entries[i-1].EndMs = entries[i].StartMs
		} else if gap < 0 {
			entries[i-1].EndMs = entries[i].StartMs
		}
	}

	// Format to SRT string
	var sb strings.Builder
	for _, e := range entries {
		sb.WriteString(fmt.Sprintf("%d\n", e.Index))
		sb.WriteString(fmt.Sprintf("%s --> %s\n", formatSRTTime(e.StartMs), formatSRTTime(e.EndMs)))
		sb.WriteString(e.Text + "\n\n")
	}
	return strings.TrimSpace(sb.String())
}

type srtEntry struct {
	Index   int
	StartMs int
	EndMs   int
	Text    string
}

func formatSRTTime(totalMs int) string {
	if totalMs < 0 {
		totalMs = 0
	}
	hours := totalMs / 3600000
	minutes := (totalMs % 3600000) / 60000
	seconds := (totalMs % 60000) / 1000
	millis := totalMs % 1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", hours, minutes, seconds, millis)
}

func isSentenceEnd(r rune) bool {
	switch r {
	case '。', '！', '？', '；', '.', '!', '?', '\n':
		return true
	}
	return false
}

func chunkStartSec(r models.TranscriptResult) float64 {
	if len(r.Timestamps) > 0 {
		return r.ChunkOffset + r.Timestamps[0]
	}
	return r.ChunkOffset
}

func chunkEndSec(r models.TranscriptResult) float64 {
	if len(r.Timestamps) > 0 {
		return r.ChunkOffset + r.Timestamps[len(r.Timestamps)-1]
	}
	return r.ChunkOffset
}
